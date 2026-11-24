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

	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	defaultWait   = 25 * time.Second
	maxWait       = 60 * time.Second
	feedLimit     = 500
	maxJoinBody   = 64 << 10
	maxStatusBody = 1 << 20
	maxObjectBody = 4 << 20
	// readHeaderTimeout bounds a node's request header, as on the platform's own listeners.
	readHeaderTimeout = 10 * time.Second
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
	cert, err := cfg.CA.ServerCertificate(hosts, cfg.Now())
	if err != nil {
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
			Certificates: []tls.Certificate{cert}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: cfg.CA.Pool(),
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

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Post(PathJoin, s.join)
	r.Group(func(r chi.Router) {
		r.Use(s.authNode)
		r.Get(PathSnapshot, s.snapshot)
		r.Get(PathFeed, s.feed)
		r.Put(PathStatus, s.status)
		r.Put(PathObjects, s.putObject)
		r.Delete(PathObjects, s.deleteObject)
		r.Get(PathCalls, s.poll)
		r.Post(PathCalls+"/{id}", s.answer)
		r.Get(PathKeys, s.keys)
		r.Put(PathBackup, s.backup)
		r.Post(PathSecrets, s.secret)
		r.Get(PathChildren, s.children)
	})
	return r
}

type nodeKey struct{}

func nodeOf(ctx context.Context) v1.ObjectName {
	n, _ := ctx.Value(nodeKey{}).(v1.ObjectName)
	return n
}

// authNode admits a node by the certificate it joined with: verified against the CA, and still the one its WorkerNode
// names, so a new join or a deleted WorkerNode ends the old certificate. A node speaking a node API version this control
// plane does not serve gets 426, and one on a newer release than the control plane's 409 (decision 12).
func (s *Server) authNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api, err := strconv.Atoi(r.Header.Get(HeaderAPIVersion))
		if err != nil || !slices.Contains(SupportedAPIVersions, api) {
			http.Error(w, "this control plane serves node API versions "+versions()+"; upgrade or downgrade the node",
				http.StatusUpgradeRequired)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "a node certificate is required", http.StatusUnauthorized)
			return
		}
		if release := r.Header.Get(HeaderVersion); newerRelease(release, s.cfg.Version) {
			http.Error(w, "this node runs funcd "+release+", newer than the control plane's "+s.cfg.Version+
				": upgrade the control plane first", http.StatusConflict)
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		node := v1.ObjectName(leaf.Subject.CommonName)
		obj, err := s.cfg.Store.Get(r.Context(), v1.KindWorkerNode.GVK(), "", node)
		if err != nil {
			http.Error(w, "worker node "+string(node)+" is not declared", http.StatusUnauthorized)
			return
		}
		n, ok := obj.(*v1.WorkerNode)
		if !ok || n.Status.Certificate == nil || n.Status.Certificate.Serial != leaf.SerialNumber.Text(16) {
			http.Error(w, "this certificate is not the node's current one; join again", http.StatusUnauthorized)
			return
		}
		s.cfg.Liveness.Seen(node, r.Header.Get(HeaderVersion), api, r.Header.Get(HeaderPlatform))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nodeKey{}, node)))
	})
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

//nolint:forbidigo // v is one of the node API's typed answers
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeFault(w http.ResponseWriter, err error) { http.Error(w, err.Error(), fault.StatusOf(err)) }

//nolint:forbidigo // v is one of the node API's typed requests
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		http.Error(w, "decode the request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// join swaps a one-time token for a node certificate (decision 2). The token must match the hash the node's
// WorkerNode keeps, not be expired, and no running node may hold the name; the swap uses the token up.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.join"
	var in JoinRequest
	if !decode(w, r, maxJoinBody, &in) {
		return
	}
	tok, err := ParseToken(in.Token)
	if err != nil {
		writeFault(w, err)
		return
	}
	if tok.CAHash != s.cfg.CA.Hash() {
		writeFault(w, fault.Forbiddenf(op, "the join token was minted by another control plane"))
		return
	}
	if s.cfg.Liveness.Ready(tok.Node) {
		writeFault(w, fault.Conflictf(op, "a running node holds the name %s", tok.Node))
		return
	}
	if newerRelease(in.Version, s.cfg.Version) {
		writeFault(w, fault.Conflictf(op, "this node runs funcd %s, newer than the control plane's %s: upgrade the control plane first",
			in.Version, s.cfg.Version))
		return
	}
	now := s.cfg.Now()
	var cert []byte
	var denied error
	err = writeNodeStatus(r.Context(), s.cfg.Store, tok.Node, func(n *v1.WorkerNode) bool {
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
		n.Status.PendingJoin = nil
		return true
	})
	switch {
	case fault.KindOf(err) == fault.NotFound:
		writeFault(w, fault.Forbiddenf(op, "no WorkerNode %s is declared", tok.Node))
	case err != nil:
		writeFault(w, err)
	case denied != nil:
		writeFault(w, denied)
	default:
		s.cfg.Logger.InfoContext(r.Context(), "worker node joined", "node", tok.Node, "version", in.Version, "platform", in.Platform)
		writeJSON(w, JoinResponse{Certificate: cert, CA: s.cfg.CA.PEM()})
	}
}

// placedOn reports whether namespace ns is placed on node.
func (s *Server) placedOn(ctx context.Context, ns v1.NamespaceName, node v1.ObjectName) bool {
	at, err := s.cfg.Placement.NodeOf(ctx, ns)
	return err == nil && at == node && node != ""
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

// snapshot answers GET …/snapshot[?namespace=]: every object of the node's namespaces (or the one asked), and the
// cluster-scoped ones that concern it, at the revision it follows the feed after. The node's own children are left out.
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	ctx, node := r.Context(), nodeOf(r.Context())
	head, err := s.cl.Head(ctx)
	if err != nil {
		writeFault(w, err)
		return
	}
	var namespaces []v1.NamespaceName
	if ns := v1.NamespaceName(r.URL.Query().Get("namespace")); ns != "" {
		if !s.placedOn(ctx, ns, node) {
			writeFault(w, fault.Forbiddenf("multinode.snapshot", "namespace %s is not placed on worker node %s", ns, node))
			return
		}
		namespaces = []v1.NamespaceName{ns}
	} else if namespaces, err = s.cfg.Placement.Namespaces(ctx, node); err != nil {
		writeFault(w, err)
		return
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
			writeFault(w, err)
			return
		}
		for _, obj := range res.Items {
			if !s.concerns(ctx, node, obj) {
				continue
			}
			if err := add(obj); err != nil {
				writeFault(w, err)
				return
			}
		}
	}
	for _, ns := range namespaces {
		nsObj, err := s.cfg.Store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
		if err != nil {
			writeFault(w, err)
			return
		}
		if err := add(nsObj); err != nil {
			writeFault(w, err)
			return
		}
		for _, k := range v1.AllKinds() {
			if !k.Namespaced() {
				continue
			}
			res, err := s.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
			if err != nil {
				writeFault(w, err)
				return
			}
			for _, obj := range res.Items {
				if len(obj.GetObjectMeta().OwnerReferences) > 0 {
					continue
				}
				if err := add(obj); err != nil {
					writeFault(w, err)
					return
				}
			}
		}
	}
	writeJSON(w, out)
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

// children answers GET …/children: the children a node made in the namespaces placed on it, as the control plane holds them.
func (s *Server) children(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	namespaces, err := s.cfg.Placement.Namespaces(ctx, nodeOf(ctx))
	if err != nil {
		writeFault(w, err)
		return
	}
	out := []store.Change{}
	for _, ns := range namespaces {
		for _, k := range childKinds() {
			res, err := s.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
			if err != nil {
				writeFault(w, err)
				return
			}
			for _, obj := range res.Items {
				if len(obj.GetObjectMeta().OwnerReferences) == 0 {
					continue
				}
				c, err := added(obj, 0)
				if err != nil {
					writeFault(w, err)
					return
				}
				out = append(out, c)
			}
		}
	}
	writeJSON(w, out)
}

func waitOf(r *http.Request) time.Duration {
	d, err := time.ParseDuration(r.URL.Query().Get("wait"))
	if err != nil || d <= 0 || d > maxWait {
		return defaultWait
	}
	return d
}

// feed answers GET …/feed?since=&wait= (decision 3): the node's records after since, or none once wait passes. A node
// gets the records of the namespaces placed on it and of the cluster-scoped kinds it follows, as they concern it, so it
// sees placements come and go, but not the ones it wrote itself. A since older than the change log, or of another timeline of the metastore (one restored
// from a backup since), is 410: the node starts again from a snapshot.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	ctx, node := r.Context(), nodeOf(r.Context())
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		http.Error(w, "since must be a revision", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("timeline") != s.cl.Timeline() {
		http.Error(w, "the revision is of another timeline of the metastore, which was restored since", http.StatusGone)
		return
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
	deadline := time.Now().Add(waitOf(r))
	for {
		signal := s.cl.ChangeSignal()
		out, cursor, err := s.cl.Changes(ctx, since, feedLimit, keep)
		if fault.KindOf(err) == fault.PreconditionFailed {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		if err != nil {
			writeFault(w, err)
			return
		}
		for i := range out {
			if out[i], err = s.forNode(ctx, node, out[i]); err != nil {
				writeFault(w, err)
				return
			}
		}
		if len(out) > 0 || !time.Now().Before(deadline) {
			writeJSON(w, Feed{Rev: cursor, Changes: out})
			return
		}
		since = cursor
		t := time.NewTimer(time.Until(deadline))
		select {
		case <-signal:
			t.Stop()
		case <-t.C:
			writeJSON(w, Feed{Rev: cursor})
			return
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// backup answers PUT …/backup (F251): the node's report of its own platform backup goes into its WorkerNode's status.
func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	ctx, node := r.Context(), nodeOf(r.Context())
	var in v1.NodeBackup
	if !decode(w, r, maxStatusBody, &in) {
		return
	}
	if err := writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		n.Status.Backup = &in
		return true
	}); err != nil {
		writeFault(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// status answers PUT …/status (decision 4): the object's status becomes the node's, but the decisions the control plane
// records there; nothing else of it changes. The object must sit in a namespace placed on the node, be the one the node
// runs, by its uid, and pass validation (decision 5).
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.status"
	ctx, node := r.Context(), nodeOf(r.Context())
	var in StatusWrite
	if !decode(w, r, maxStatusBody, &in) {
		return
	}
	if !s.placedOn(ctx, in.Namespace, node) {
		writeFault(w, fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", in.Namespace, node))
		return
	}
	ctx = store.WithoutChangeLog(ctx)
	for range 5 {
		cur, err := s.cfg.Store.Get(ctx, in.Kind.GVK(), in.Namespace, in.Name)
		if err != nil {
			writeFault(w, err)
			return
		}
		if cur.GetObjectMeta().UID != in.UID {
			writeFault(w, fault.Conflictf(op, "%s %s was created again since the node took it", in.Kind, in.Name))
			return
		}
		obj, err := withStatus(cur, in.Status)
		if err != nil {
			writeFault(w, err)
			return
		}
		KeepDecisions(obj, cur)
		if err := obj.Validate(); err != nil {
			writeFault(w, err)
			return
		}
		if _, err = s.cfg.Store.Update(ctx, obj); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		} else if fault.KindOf(err) != fault.Conflict {
			writeFault(w, err)
			return
		}
	}
	writeFault(w, fault.Conflictf(op, "%s %s kept changing", in.Kind, in.Name))
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

// putObject answers PUT …/objects, in a namespace placed on the node: a child the node owns (one with ownerReferences)
// is created or replaced, and an object the node makes with no owner is created, after which only its status changes. The
// copy is the node's object, under its uid. A node never writes an object a person wrote, and one it makes under a name
// another object holds is refused: the node takes that object instead.
func (s *Server) putObject(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.putObject"
	ctx, node := r.Context(), nodeOf(r.Context())
	var in ObjectWrite
	if !decode(w, r, maxObjectBody, &in) {
		return
	}
	obj, ok := v1.NewObject(in.Kind)
	if !ok {
		writeFault(w, fault.Invalidf(op, "unknown kind %q", in.Kind))
		return
	}
	if err := json.Unmarshal(in.Object, obj); err != nil {
		writeFault(w, fault.Wrapf(err, fault.Invalid, op, "decode the %s", in.Kind))
		return
	}
	m := obj.GetObjectMeta()
	switch {
	case !s.placedOn(ctx, m.Namespace, node):
		writeFault(w, fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", m.Namespace, node))
		return
	case !WrittenBack(in.Kind, len(m.OwnerReferences) > 0):
		writeFault(w, fault.Forbiddenf(op, "a node writes back only the children it owns and the objects it makes; not %s %s",
			in.Kind, m.Name))
		return
	}
	if err := obj.Validate(); err != nil {
		writeFault(w, err)
		return
	}
	ctx = store.AsCopy(store.WithOrigin(ctx, node))
	for range 5 {
		err := s.putCopy(ctx, obj, in.Object)
		if fault.KindOf(err) != fault.Conflict || errors.Is(err, errNameHeld) {
			writeDone(w, err)
			return
		}
	}
	writeFault(w, fault.Conflictf(op, "%s %s kept changing", in.Kind, m.Name))
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

func writeDone(w http.ResponseWriter, err error) {
	if err != nil {
		writeFault(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteObject answers DELETE …/objects?kind=&namespace=&name=&uid=: an object the node holds goes, when it is one a node
// writes back whole, or one whose deletion the control plane held until the node ended it (ADR-0345 Decision 6). An
// object of another uid took the name since, so it stays.
func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.deleteObject"
	ctx, node := r.Context(), nodeOf(r.Context())
	q := r.URL.Query()
	kind, ns, name, uid := v1.Kind(q.Get("kind")), v1.NamespaceName(q.Get("namespace")), v1.ObjectName(q.Get("name")), v1.UID(q.Get("uid"))
	switch {
	case !s.placedOn(ctx, ns, node):
		writeFault(w, fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", ns, node))
		return
	case uid == "":
		writeFault(w, fault.Invalidf(op, "the uid of the deleted %s %s is required", kind, name))
		return
	}
	cur, err := s.cfg.Store.Get(ctx, kind.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound || err == nil && cur.GetObjectMeta().UID != uid {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeFault(w, err)
		return
	}
	if m := cur.GetObjectMeta(); !WrittenBack(kind, len(m.OwnerReferences) > 0) && m.DeletionTime == nil {
		writeFault(w, fault.Forbiddenf(op, "%s %s was written by a person, not by worker node %s", kind, name, node))
		return
	}
	err = s.cfg.Store.Delete(store.WithOrigin(ctx, node), kind.GVK(), ns, name, "")
	if fault.KindOf(err) == fault.NotFound {
		err = nil
	}
	writeDone(w, err)
}

// SecretWriter writes one key of a Secret as a workload that binds it (ADR-0327): the platform's secret service.
type SecretWriter interface {
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key, value string) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key string) error
}

// secret answers POST …/secrets: a workload of a namespace placed on the node writes one key of a Secret it binds. The
// control plane holds the Secret, so it makes the checks and the write as for a workload of its own, and the change
// reaches the node through the feed.
func (s *Server) secret(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.secret"
	ctx, node := r.Context(), nodeOf(r.Context())
	var in SecretWrite
	if !decode(w, r, maxObjectBody, &in) {
		return
	}
	switch {
	case !s.placedOn(ctx, in.Namespace, node):
		writeFault(w, fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", in.Namespace, node))
		return
	case s.cfg.Secrets == nil:
		writeFault(w, fault.Unavailablef(op, "this control plane writes no Secret"))
		return
	}
	var out SecretAnswer
	if err := s.writeSecret(ctx, in, &out); err != nil {
		out.Error, out.Fault = err.Error(), fault.KindOf(err)
	}
	writeJSON(w, out)
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

// poll answers GET …/calls?wait=: the node's queued calls, or 204 once wait passes.
func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	calls := s.cfg.Calls.Poll(r.Context(), nodeOf(r.Context()), waitOf(r))
	if len(calls) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, calls)
}

// answer answers POST …/calls/{id}: the node's answer reaches the waiting caller.
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var a Answer
	if !decode(w, r, maxObjectBody, &a) {
		return
	}
	writeDone(w, s.cfg.Calls.Answer(nodeOf(r.Context()), chi.URLParam(r, "id"), a))
}

// keys answers GET …/keys?namespace=: the master of a namespace placed on the node (decision 8).
func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	const op = "multinode.keys"
	ctx, node := r.Context(), nodeOf(r.Context())
	ns := v1.NamespaceName(r.URL.Query().Get("namespace"))
	if !s.placedOn(ctx, ns, node) {
		writeFault(w, fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", ns, node))
		return
	}
	if s.cfg.Keys == nil {
		writeFault(w, fault.Unavailablef(op, "this control plane derives no namespace keys"))
		return
	}
	k, err := s.cfg.Keys.Of(ctx, ns)
	if err != nil {
		writeFault(w, err)
		return
	}
	writeJSON(w, k)
}
