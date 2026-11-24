package multinode

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	defaultWait   = 25 * time.Second
	maxWait       = 60 * time.Second
	feedLimit     = 500
	maxJoinBody   = 64 << 10
	maxStatusBody = 1 << 20
	maxObjectBody = 4 << 20
	// readHeaderTimeout bounds a node's request header, as on the platform's own listeners, and bodyReadTimeout its body.
	readHeaderTimeout = 10 * time.Second
	bodyReadTimeout   = 30 * time.Second
)

// ServerConfig is what the node API serves from.
type ServerConfig struct {
	Store     store.Store
	CA        *CA
	Placement *Placement
	Liveness  *Liveness
	Calls     *Calls
	Keys      *NamespaceKeys
	Secrets   SecretWriter
	Held      func() bool // the control plane is held after its restore; nil ⇒ never
	Logger    *slog.Logger
	Version   string // the control plane's release (decision 12)
	Now       func() time.Time
}

// Server is the node API (FEAT-0002 decisions 2, 3, 4 and 9): HTTPS and JSON on one listener, mTLS for every route but
// the join, which a node calls with its one-time token.
type Server struct {
	cfg ServerConfig
	cl  store.ChangeLog
	srv *http.Server
	ln  net.Listener
}

// NewServer binds the node API on listenAddr, with a certificate for hosts that the CA signs.
func NewServer(cfg ServerConfig, listenAddr string, hosts []string) (*Server, error) {
	const op = "multinode.NewServer"
	cl, ok := cfg.Store.(store.ChangeLog)
	if !ok {
		return nil, fault.Invalidf(op, "the store keeps no change log")
	}
	cert := &serverCertificate{ca: cfg.CA, hosts: hosts, now: cfg.Now}
	if _, err := cert.get(nil); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "bind the node API on %s", listenAddr)
	}
	s := &Server{cfg: cfg, cl: cl, ln: ln}
	s.srv = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelWarn),
		TLSConfig: &tls.Config{
			GetCertificate: cert.get, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: cfg.CA.Pool(),
			MinVersion: tls.VersionTLS13,
		},
	}
	return s, nil
}

// Addr is the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve serves until ctx ends, then closes every connection, long polls included.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.srv.Close()
	}()
	if err := s.srv.ServeTLS(s.ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops the listener.
func (s *Server) Close() error { return s.srv.Close() }

// routes are the node API on the API's own stack (decision 9): the join, then the routes of a joined node.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(fault.LogCauses(s.cfg.Logger))
	handle(nodeAPI(r), op(http.MethodPost, PathJoin, maxJoinBody), s.join)
	r.Group(func(r chi.Router) {
		r.Use(s.authNode)
		api := nodeAPI(r)
		handle(api, op(http.MethodPost, PathRenew, maxJoinBody), s.renew)
		handle(api, op(http.MethodPut, PathRenew, 0), s.renewed)
		handle(api, op(http.MethodGet, PathSnapshot, 0), s.snapshot)
		handle(api, op(http.MethodGet, PathFeed, 0), s.feed)
		handle(api, op(http.MethodGet, PathChildren, 0), s.children)
		handle(api, op(http.MethodPut, PathStatus, maxStatusBody), s.status)
		handle(api, op(http.MethodPut, PathObjects, maxObjectBody), s.putObject)
		handle(api, op(http.MethodDelete, PathObjects, 0), s.deleteObject)
		handle(api, op(http.MethodPut, PathReport, maxStatusBody), s.report)
		handle(api, op(http.MethodGet, PathKeys, 0), s.keys)
		handle(api, op(http.MethodPost, PathSecrets, maxObjectBody), s.secret)
		handle(api, op(http.MethodGet, PathCalls, 0), s.poll)
		handle(api, op(http.MethodPost, PathCalls+"/{id}", maxObjectBody), s.answer)
	})
	return r
}

func nodeAPI(r chi.Router) huma.API {
	return controlplane.NewInnerAPI(r, "funcd node API", strconv.Itoa(APIVersion))
}

// op is the operation of method on path, whose body is at most limit bytes.
func op(method, path string, limit int64) huma.Operation {
	return huma.Operation{OperationID: method + " " + path, Method: method, Path: path, MaxBodyBytes: limit,
		BodyReadTimeout: bodyReadTimeout}
}

// handle registers h, which answers with the fault of its error.
func handle[I, O any](api huma.API, op huma.Operation, h func(context.Context, *I) (*O, error)) { //nolint:forbidigo // huma's own constraint
	huma.Register(api, op, func(ctx context.Context, in *I) (*O, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, controlplane.FaultError(err)
		}
		return out, nil
	})
}

type nodeKey struct{}

func nodeOf(ctx context.Context) v1.ObjectName {
	n, _ := ctx.Value(nodeKey{}).(v1.ObjectName)
	return n
}

// authNode admits a node by the certificate it joined with, or the one it renewed it with: verified against the CA, and
// one its WorkerNode names, so a new join or a deleted WorkerNode ends the old ones, and the first call with a renewed
// certificate ends the one before. A node speaking a node API version this control plane does not serve gets 426, and
// one on a newer release than the control plane's 409 (decision 12).
func (s *Server) authNode(next http.Handler) http.Handler {
	const op = "multinode.authNode"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api, err := strconv.Atoi(r.Header.Get(HeaderAPIVersion))
		if err != nil || !slices.Contains(SupportedAPIVersions, api) {
			upgradeRequired(w)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			fault.WriteProblem(w, fault.Unauthorizedf(op, "a node certificate is required"))
			return
		}
		if release := r.Header.Get(HeaderVersion); newerRelease(release, s.cfg.Version) {
			fault.WriteProblem(w, tooNew(op, release, s.cfg.Version))
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		node := v1.ObjectName(leaf.Subject.CommonName)
		obj, err := s.cfg.Store.Get(r.Context(), v1.KindWorkerNode.GVK(), "", node)
		if err != nil {
			fault.WriteProblem(w, fault.Unauthorizedf(op, "worker node %s is not declared", node))
			return
		}
		n, ok := obj.(*v1.WorkerNode)
		serial := leaf.SerialNumber.Text(16)
		switch renewed := ok && n.Status.RenewedCertificate.Is(serial); {
		case ok && n.Status.Certificate.Is(serial):
		case renewed:
			if err := s.useRenewed(r.Context(), node, serial); err != nil {
				fault.WriteProblem(w, err)
				return
			}
		default:
			fault.WriteProblem(w, fault.Unauthorizedf(op, "this certificate is not the node's current one; join again"))
			return
		}
		s.cfg.Liveness.Seen(node, r.Header.Get(HeaderVersion), api, r.Header.Get(HeaderPlatform))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeKey{}, node)))
	})
}

// useRenewed makes node's renewed certificate of serial its certificate.
func (s *Server) useRenewed(ctx context.Context, node v1.ObjectName, serial string) error {
	return writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		if !n.Status.RenewedCertificate.Is(serial) {
			return false
		}
		n.Status.Certificate, n.Status.RenewedCertificate = n.Status.RenewedCertificate, nil
		return true
	})
}

type renewInput struct{ Body RenewRequest }

// renew answers POST …/renew: a new certificate for the node's key. The node's certificate stays until it uses the new one,
// so an answer that never reaches it costs nothing.
func (s *Server) renew(ctx context.Context, in *renewInput) (*joinOutput, error) {
	node, now := nodeOf(ctx), s.cfg.Now()
	cert, serial, notAfter, err := s.cfg.CA.SignNode(in.Body.CSR, node, now)
	if err != nil {
		return nil, err
	}
	if err := writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		n.Status.RenewedCertificate = &v1.NodeCertificate{Serial: serial, NotAfter: v1.NewTimestamp(notAfter), IssuedAt: v1.NewTimestamp(now)}
		return true
	}); err != nil {
		return nil, err
	}
	s.cfg.Logger.InfoContext(ctx, "worker node certificate renewed", "node", node, "notAfter", notAfter)
	return &joinOutput{Body: JoinResponse{Certificate: cert, CA: s.cfg.CA.PEM()}}, nil
}

// renewed answers PUT …/renew, a node's first call with its renewed certificate: authNode made it the node's certificate.
func (s *Server) renewed(context.Context, *struct{}) (*struct{}, error) { return nil, nil }

func tooNew(op, release, ours string) error {
	return fault.Conflictf(op, "this node runs funcd %s, newer than the control plane's %s: upgrade the control plane first", release, ours)
}

// upgradeRequired answers a node API version this control plane does not serve.
func upgradeRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUpgradeRequired)
	_ = json.NewEncoder(w).Encode(fault.Problem{Type: "about:blank", Title: http.StatusText(http.StatusUpgradeRequired),
		Status: http.StatusUpgradeRequired, Detail: "this control plane serves node API versions " + versions() +
			"; upgrade or downgrade the node"})
}

func versions() string {
	out := ""
	for i, v := range SupportedAPIVersions {
		if i > 0 {
			out += ", "
		}
		out += strconv.Itoa(v)
	}
	return out
}

type joinInput struct{ Body JoinRequest }
type joinOutput struct{ Body JoinResponse }

// join swaps a one-time token for a node certificate (decision 2). The token must match the hash the node's
// WorkerNode keeps, not be expired, and no running node may hold the name; the swap uses the token up.
func (s *Server) join(ctx context.Context, req *joinInput) (*joinOutput, error) {
	const op = "multinode.join"
	in := req.Body
	tok, err := ParseToken(in.Token)
	switch {
	case err != nil:
		return nil, err
	case tok.CAHash != s.cfg.CA.Hash():
		return nil, fault.Forbiddenf(op, "the join token was minted by another control plane")
	case s.cfg.Liveness.Ready(tok.Node):
		return nil, fault.Conflictf(op, "a running node holds the name %s", tok.Node)
	case newerRelease(in.Version, s.cfg.Version):
		return nil, tooNew(op, in.Version, s.cfg.Version)
	}
	now := s.cfg.Now()
	var cert []byte
	var denied error
	err = writeNodeStatus(ctx, s.cfg.Store, tok.Node, func(n *v1.WorkerNode) bool {
		pj := n.Status.PendingJoin
		switch {
		case pj == nil:
			denied = fault.Forbiddenf(op, "worker node %s has no join token waiting; mint one with funcdctl node token", tok.Node)
			return false
		case now.After(time.Time(pj.ExpiresAt)):
			denied = fault.Forbiddenf(op, "the join token of worker node %s expired; mint a new one", tok.Node)
			return false
		case !secretMatches(tok.Secret, pj.TokenHash):
			denied = fault.Forbiddenf(op, "the join token does not match the one minted for worker node %s", tok.Node)
			return false
		}
		pemCert, serial, notAfter, serr := s.cfg.CA.SignNode(in.CSR, tok.Node, now)
		if serr != nil {
			denied = serr
			return false
		}
		cert = pemCert
		n.Status.Certificate = &v1.NodeCertificate{Serial: serial, NotAfter: v1.NewTimestamp(notAfter), IssuedAt: v1.NewTimestamp(now)}
		n.Status.PendingJoin, n.Status.RenewedCertificate = nil, nil
		return true
	})
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, fault.Forbiddenf(op, "no WorkerNode %s is declared", tok.Node)
	case err != nil:
		return nil, err
	case denied != nil:
		return nil, denied
	}
	s.cfg.Logger.InfoContext(ctx, "worker node joined", "node", tok.Node, "version", in.Version, "platform", in.Platform)
	return &joinOutput{Body: JoinResponse{Certificate: cert, CA: s.cfg.CA.PEM()}}, nil
}

// placedOn reports whether namespace ns is placed on node.
func (s *Server) placedOn(ctx context.Context, ns v1.NamespaceName, node v1.ObjectName) bool {
	at, err := s.cfg.Placement.NodeOf(ctx, ns)
	return err == nil && at == node && node != ""
}

// mayFollow refuses a node that follows while the control plane is held after its restore (ADR-0206): the metastore is at
// an older point until a person releases it, and the node runs on what it holds.
func (s *Server) mayFollow() error {
	if s.cfg.Held != nil && s.cfg.Held() {
		return fault.Unavailablef("multinode.follow", "the control plane is held after its restore: its nodes follow it "+
			"again once `funcdctl hold release` releases it")
	}
	return nil
}

// mustBePlacedOn refuses a call about a namespace that is not placed on node.
func (s *Server) mustBePlacedOn(ctx context.Context, op string, ns v1.NamespaceName, node v1.ObjectName) error {
	if !s.placedOn(ctx, ns, node) {
		return fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", ns, node)
	}
	return nil
}

// concerns reports whether node takes the cluster-scoped obj whole: its own Namespaces, the API tokens that span one of
// them (decision 7), and every object of the other kinds it follows.
func (s *Server) concerns(ctx context.Context, node v1.ObjectName, obj v1.Object) bool {
	switch o := obj.(type) {
	case *v1.Namespace:
		return o.Spec.Node == node
	case *v1.APIToken:
		return len(o.Spec.Namespaces) == 0 || slices.ContainsFunc(o.Spec.Namespaces, func(ns v1.NamespaceName) bool {
			return s.placedOn(ctx, ns, node)
		})
	}
	return true
}

// forNode is record c as node gets it. One of a Namespace or of an API token that does not concern the node is a delete of
// its name and carries nothing else, so a node that held it drops it, and no node reads what is not its own.
func (s *Server) forNode(ctx context.Context, node v1.ObjectName, c store.Change) (store.Change, error) {
	if c.Kind != v1.KindNamespace && c.Kind != v1.KindAPIToken {
		return c, nil
	}
	obj, ok := v1.NewObject(c.Kind)
	if !ok {
		return c, nil
	}
	if err := json.Unmarshal(c.Object, obj); err != nil {
		return store.Change{}, fault.Wrapf(err, fault.Internal, "multinode.feed", "decode %s %s", c.Kind, c.Name)
	}
	if s.concerns(ctx, node, obj) {
		return c, nil
	}
	return store.Change{Rev: c.Rev, Type: store.Deleted, Kind: c.Kind, Name: c.Name}, nil
}

type snapshotInput struct {
	Namespace v1.NamespaceName `query:"namespace"`
}
type snapshotOutput struct{ Body Snapshot }

// snapshot answers GET …/snapshot[?namespace=]: every object of the node's namespaces (or the one asked), and the
// cluster-scoped ones that concern it, at the revision it follows the feed after. The node's own children are left out.
func (s *Server) snapshot(ctx context.Context, in *snapshotInput) (*snapshotOutput, error) {
	if err := s.mayFollow(); err != nil {
		return nil, err
	}
	node := nodeOf(ctx)
	head, err := s.cl.Head(ctx)
	if err != nil {
		return nil, err
	}
	var namespaces []v1.NamespaceName
	if ns := in.Namespace; ns != "" {
		if err := s.mustBePlacedOn(ctx, "multinode.snapshot", ns, node); err != nil {
			return nil, err
		}
		namespaces = []v1.NamespaceName{ns}
	} else if namespaces, err = s.cfg.Placement.Namespaces(ctx, node); err != nil {
		return nil, err
	}
	out := Snapshot{Rev: head, Timeline: s.cl.Timeline(), Objects: []store.Change{}}
	add := func(obj v1.Object) error {
		c, err := added(obj, head)
		out.Objects = append(out.Objects, c)
		return err
	}
	for _, k := range NodeKinds() {
		res, err := s.cfg.Store.List(ctx, k.GVK(), store.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, obj := range res.Items {
			if !s.concerns(ctx, node, obj) {
				continue
			}
			if err := add(obj); err != nil {
				return nil, err
			}
		}
	}
	for _, ns := range namespaces {
		nsObj, err := s.cfg.Store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
		if err != nil {
			return nil, err
		}
		if err := add(nsObj); err != nil {
			return nil, err
		}
		if err := s.each(ctx, v1.AllKinds(), ns, func(obj v1.Object) error {
			if len(obj.GetObjectMeta().OwnerReferences) > 0 {
				return nil
			}
			return add(obj)
		}); err != nil {
			return nil, err
		}
	}
	return &snapshotOutput{Body: out}, nil
}

// each calls f with each object of the namespaced kinds in namespace ns.
func (s *Server) each(ctx context.Context, kinds []v1.Kind, ns v1.NamespaceName, f func(v1.Object) error) error {
	for _, k := range kinds {
		if !k.Namespaced() {
			continue
		}
		res, err := s.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return err
		}
		for _, obj := range res.Items {
			if err := f(obj); err != nil {
				return err
			}
		}
	}
	return nil
}

// added is obj as a record at rev.
func added(obj v1.Object, rev uint64) (store.Change, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return store.Change{}, fault.Wrapf(err, fault.Internal, "multinode.snapshot", "marshal")
	}
	m := obj.GetObjectMeta()
	return store.Change{Rev: rev, Type: store.Added, Kind: obj.GroupVersionKind().Kind, Namespace: m.Namespace, Name: m.Name,
		Object: raw}, nil
}

type childrenOutput struct{ Body []store.Change }

// children answers GET …/children: the children a node made in the namespaces placed on it, as the control plane holds them.
func (s *Server) children(ctx context.Context, _ *struct{}) (*childrenOutput, error) {
	namespaces, err := s.cfg.Placement.Namespaces(ctx, nodeOf(ctx))
	if err != nil {
		return nil, err
	}
	out := &childrenOutput{Body: []store.Change{}}
	for _, ns := range namespaces {
		if err := s.each(ctx, childKinds(), ns, func(obj v1.Object) error {
			if len(obj.GetObjectMeta().OwnerReferences) == 0 {
				return nil
			}
			c, err := added(obj, 0)
			out.Body = append(out.Body, c)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// WaitInput is a long poll's wait, a duration; one left out, or out of bounds, is defaultWait.
type WaitInput struct {
	Wait string `query:"wait"`
}

func (w WaitInput) duration() time.Duration {
	d, err := time.ParseDuration(w.Wait)
	if err != nil || d <= 0 || d > maxWait {
		return defaultWait
	}
	return d
}

type feedInput struct {
	WaitInput
	Since    uint64 `query:"since" required:"true"`
	Timeline string `query:"timeline"`
}
type feedOutput struct{ Body Feed }

// feed answers GET …/feed?since=&wait= (decision 3): the node's records after since, or none once wait passes. A node
// gets the records of the namespaces placed on it and of the cluster-scoped kinds it follows, as they concern it, so it
// sees placements come and go, but not the ones it wrote itself. A since older than the change log, or of another
// timeline of the metastore (one restored from a backup since), is 410: the node starts again from a snapshot.
func (s *Server) feed(ctx context.Context, in *feedInput) (*feedOutput, error) {
	if err := s.mayFollow(); err != nil {
		return nil, err
	}
	node, since := nodeOf(ctx), in.Since
	if in.Timeline != s.cl.Timeline() {
		return nil, huma.Error410Gone("the revision is of another timeline of the metastore, which was restored since")
	}
	keep := func(c store.Change) bool {
		switch {
		case c.Origin == node:
			return false
		case c.Kind == v1.KindNamespace || nodeKind(c.Kind):
			return true
		case c.Namespace == "":
			return false
		default:
			return s.placedOn(ctx, c.Namespace, node)
		}
	}
	deadline := time.Now().Add(in.duration())
	for {
		signal := s.cl.ChangeSignal()
		out, cursor, err := s.cl.Changes(ctx, since, feedLimit, keep)
		if fault.KindOf(err) == fault.PreconditionFailed {
			return nil, huma.Error410Gone(err.Error())
		}
		if err != nil {
			return nil, err
		}
		for i := range out {
			if out[i], err = s.forNode(ctx, node, out[i]); err != nil {
				return nil, err
			}
		}
		if len(out) > 0 || !time.Now().Before(deadline) {
			return &feedOutput{Body: Feed{Rev: cursor, Changes: out}}, nil
		}
		since = cursor
		t := time.NewTimer(time.Until(deadline))
		select {
		case <-signal:
			t.Stop()
		case <-t.C:
			return &feedOutput{Body: Feed{Rev: cursor}}, nil
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

type reportInput struct{ Body NodeReport }

// report answers PUT …/report: what the node reports of itself goes into its WorkerNode's status.
func (s *Server) report(ctx context.Context, in *reportInput) (*struct{}, error) {
	return nil, writeNodeStatus(ctx, s.cfg.Store, nodeOf(ctx), func(n *v1.WorkerNode) bool {
		n.Status.BlobStore, n.Status.Backup = in.Body.BlobStore, in.Body.Backup
		return true
	})
}

type statusInput struct{ Body StatusWrite }

// status answers PUT …/status (decision 4): the object's status becomes the node's, but the decisions the control plane
// records there; nothing else of it changes. The object must sit in a namespace placed on the node, be the one the node
// runs, by its uid, and pass validation (decision 5).
func (s *Server) status(ctx context.Context, req *statusInput) (*struct{}, error) {
	const op = "multinode.status"
	in := req.Body
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, nodeOf(ctx)); err != nil {
		return nil, err
	}
	ctx = store.WithoutChangeLog(ctx)
	for range 5 {
		cur, err := s.cfg.Store.Get(ctx, in.Kind.GVK(), in.Namespace, in.Name)
		if err != nil {
			return nil, err
		}
		if cur.GetObjectMeta().UID != in.UID {
			return nil, fault.Conflictf(op, "%s %s was created again since the node took it", in.Kind, in.Name)
		}
		obj, err := withStatus(cur, in.Status)
		if err != nil {
			return nil, err
		}
		KeepDecisions(obj, cur)
		if err := obj.Validate(); err != nil {
			return nil, err
		}
		if _, err = s.cfg.Store.Update(ctx, obj); fault.KindOf(err) != fault.Conflict {
			return nil, err
		}
	}
	return nil, fault.Conflictf(op, "%s %s kept changing", in.Kind, in.Name)
}

// withStatus is cur with its status replaced by status.
func withStatus(cur v1.Object, status json.RawMessage) (v1.Object, error) {
	const op = "multinode.withStatus"
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	m["status"] = status
	if raw, err = json.Marshal(m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal with the status")
	}
	kind := cur.GroupVersionKind().Kind
	obj, ok := v1.NewObject(kind)
	if !ok {
		return nil, fault.Internalf(op, "unknown kind %q", kind)
	}
	if err := json.Unmarshal(raw, obj); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "the status does not fit %s", kind)
	}
	return obj, nil
}

type objectInput struct{ Body ObjectWrite }

// putObject answers PUT …/objects, in a namespace placed on the node: a child the node owns (one with ownerReferences)
// is created or replaced, and an object the node makes with no owner is created, after which only its status changes. The
// copy is the node's object, under its uid. A node never writes an object a person wrote, and one it makes under a name
// another object holds is refused: the node takes that object instead.
func (s *Server) putObject(ctx context.Context, req *objectInput) (*struct{}, error) {
	const op = "multinode.putObject"
	in, node := req.Body, nodeOf(ctx)
	obj, ok := v1.NewObject(in.Kind)
	if !ok {
		return nil, fault.Invalidf(op, "unknown kind %q", in.Kind)
	}
	if err := json.Unmarshal(in.Object, obj); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "decode the %s", in.Kind)
	}
	m := obj.GetObjectMeta()
	if err := s.mustBePlacedOn(ctx, op, m.Namespace, node); err != nil {
		return nil, err
	}
	if !WrittenBack(in.Kind, len(m.OwnerReferences) > 0) {
		return nil, fault.Forbiddenf(op, "a node writes back only the children it owns and the objects it makes; not %s %s",
			in.Kind, m.Name)
	}
	if err := obj.Validate(); err != nil {
		return nil, err
	}
	ctx = store.AsCopy(store.WithOrigin(ctx, node))
	for range 5 {
		err := s.putCopy(ctx, obj, in.Object)
		if fault.KindOf(err) != fault.Conflict || errors.Is(err, errNameHeld) {
			return nil, err
		}
	}
	return nil, fault.Conflictf(op, "%s %s kept changing", in.Kind, m.Name)
}

// errNameHeld is an object a node made under a name another object holds on the control plane.
var errNameHeld = fault.Conflictf("multinode.putObject", "another object holds the name")

// putCopy writes obj, which a node sent as raw, once: a Conflict is a write to try again, but errNameHeld.
func (s *Server) putCopy(ctx context.Context, obj v1.Object, raw json.RawMessage) error {
	const op = "multinode.putObject"
	m, gvk := obj.GetObjectMeta(), obj.GroupVersionKind()
	cur, err := s.cfg.Store.Get(ctx, gvk, m.Namespace, m.Name)
	if fault.KindOf(err) == fault.NotFound {
		m.ResourceVersion = ""
		_, err = s.cfg.Store.Create(ctx, obj)
		return err
	}
	if err != nil {
		return err
	}
	cm, next := cur.GetObjectMeta(), obj
	switch {
	case !WrittenBack(gvk.Kind, len(cm.OwnerReferences) > 0):
		return fault.Forbiddenf(op, "%s %s was written by a person, not by worker node %s", gvk.Kind, m.Name, nodeOf(ctx))
	case len(m.OwnerReferences) > 0:
	case cm.UID != m.UID:
		return fault.Wrapf(errNameHeld, fault.Conflict, op, "%s %s", gvk.Kind, m.Name)
	default:
		if next, err = withStatus(cur, statusOf(raw)); err != nil {
			return err
		}
		KeepDecisions(next, cur)
		KeepPause(next, obj)
		ctx = store.WithoutChangeLog(ctx)
	}
	next.GetObjectMeta().ResourceVersion = cm.ResourceVersion
	_, err = s.cfg.Store.Update(ctx, next)
	return err
}

// statusOf is the status of an object in JSON.
func statusOf(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m["status"]) == 0 {
		return json.RawMessage("{}")
	}
	return m["status"]
}

type deleteInput struct {
	Kind      v1.Kind          `query:"kind" required:"true"`
	Namespace v1.NamespaceName `query:"namespace" required:"true"`
	Name      v1.ObjectName    `query:"name" required:"true"`
	UID       v1.UID           `query:"uid" required:"true"`
}

// deleteObject answers DELETE …/objects?kind=&namespace=&name=&uid=: an object the node holds goes, when it is one a node
// writes back whole, or one whose deletion the control plane held until the node ended it (ADR-0345 Decision 6). An
// object of another uid took the name since, so it stays.
func (s *Server) deleteObject(ctx context.Context, in *deleteInput) (*struct{}, error) {
	const op = "multinode.deleteObject"
	node := nodeOf(ctx)
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, node); err != nil {
		return nil, err
	}
	cur, err := s.cfg.Store.Get(ctx, in.Kind.GVK(), in.Namespace, in.Name)
	if fault.KindOf(err) == fault.NotFound || err == nil && cur.GetObjectMeta().UID != in.UID {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m := cur.GetObjectMeta(); !WrittenBack(in.Kind, len(m.OwnerReferences) > 0) && m.DeletionTime == nil {
		return nil, fault.Forbiddenf(op, "%s %s was written by a person, not by worker node %s", in.Kind, in.Name, node)
	}
	err = s.cfg.Store.Delete(store.WithOrigin(ctx, node), in.Kind.GVK(), in.Namespace, in.Name, "")
	if fault.KindOf(err) == fault.NotFound {
		err = nil
	}
	return nil, err
}

// SecretWriter writes one key of a Secret as a workload that binds it (ADR-0327): the platform's secret service.
type SecretWriter interface {
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key, value string) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key string) error
}

type secretInput struct{ Body SecretWrite }
type secretOutput struct{ Body SecretAnswer }

// secret answers POST …/secrets: a workload of a namespace placed on the node writes one key of a Secret it binds. The
// control plane holds the Secret, so it makes the checks and the write as for a workload of its own, and the change
// reaches the node through the feed.
func (s *Server) secret(ctx context.Context, in *secretInput) (*secretOutput, error) {
	const op = "multinode.secret"
	if err := s.mustBePlacedOn(ctx, op, in.Body.Namespace, nodeOf(ctx)); err != nil {
		return nil, err
	}
	if s.cfg.Secrets == nil {
		return nil, fault.Unavailablef(op, "this control plane writes no Secret")
	}
	out := &secretOutput{}
	if err := s.writeSecret(ctx, in.Body, &out.Body); err != nil {
		out.Body.Error, out.Body.Fault = err.Error(), fault.KindOf(err)
	}
	return out, nil
}

func (s *Server) writeSecret(ctx context.Context, in SecretWrite, out *SecretAnswer) error {
	var err error
	if in.Value == nil {
		err = s.cfg.Secrets.Delete(ctx, in.Namespace, in.Workload, string(in.Secret), in.Key)
	} else {
		err = s.cfg.Secrets.Put(ctx, in.Namespace, in.Workload, string(in.Secret), in.Key, *in.Value)
	}
	if err != nil {
		return err
	}
	obj, err := s.cfg.Store.Get(ctx, v1.KindSecret.GVK(), in.Namespace, in.Secret)
	if err != nil {
		return err
	}
	out.Generation = obj.GetObjectMeta().Generation
	return nil
}

type callsOutput struct{ Body []Call }

// poll answers GET …/calls?wait=: the node's queued calls, none once wait passes.
func (s *Server) poll(ctx context.Context, in *WaitInput) (*callsOutput, error) {
	return &callsOutput{Body: s.cfg.Calls.Poll(ctx, nodeOf(ctx), in.duration())}, nil
}

type answerInput struct {
	ID   string `path:"id"`
	Body Answer
}

// answer answers POST …/calls/{id}: the node's answer reaches the waiting caller.
func (s *Server) answer(ctx context.Context, in *answerInput) (*struct{}, error) {
	return nil, s.cfg.Calls.Answer(nodeOf(ctx), in.ID, in.Body)
}

type keysInput struct {
	Namespace v1.NamespaceName `query:"namespace" required:"true"`
}
type keysOutput struct{ Body NamespaceKey }

// keys answers GET …/keys?namespace=: the master of a namespace placed on the node (decision 8).
func (s *Server) keys(ctx context.Context, in *keysInput) (*keysOutput, error) {
	const op = "multinode.keys"
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, nodeOf(ctx)); err != nil {
		return nil, err
	}
	if s.cfg.Keys == nil {
		return nil, fault.Unavailablef(op, "this control plane derives no namespace keys")
	}
	k, err := s.cfg.Keys.Of(ctx, in.Namespace)
	if err != nil {
		return nil, err
	}
	return &keysOutput{Body: k}, nil
}
