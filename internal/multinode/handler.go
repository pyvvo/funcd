package multinode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// feedLimit bounds the records of one answer of the feed.
const feedLimit = 500

// HandlerConfig is what the node API's rules read.
type HandlerConfig struct {
	Store     store.Store
	CA        *CA
	Placement *Placement
	Liveness  *Liveness
	Keys      *NamespaceKeys
	Secrets   SecretWriter
	Held      func() bool // the control plane is held after its restore; nil ⇒ never
	Logger    *slog.Logger
	Version   string // the control plane's release (decision 12)
	Now       func() time.Time
}

// Handler is the node API's rules (FEAT-0002 decisions 2, 3, 4, 5, 7, 8 and 12), the nodeapi.Handler a driver carries:
// who may join and send, what each node follows, and what it may write back.
type Handler struct {
	cfg HandlerConfig
	cl  store.ChangeLog
}

var _ nodeapi.Handler = (*Handler)(nil)

// NewHandler serves the node API from cfg's store, which must keep the change log the nodes follow.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	cl, ok := cfg.Store.(store.ChangeLog)
	if !ok {
		return nil, fault.Invalidf("multinode.NewHandler", "the store keeps no change log")
	}
	return &Handler{cfg: cfg, cl: cl}, nil
}

// Admit lets a joined node in by the certificate it joined with, or the one it renewed it with: one its WorkerNode names,
// so a new join or a deleted WorkerNode ends the old ones, and the first request with a renewed certificate ends the one
// before. A node on a newer release than the control plane's is refused (decision 12).
func (s *Handler) Admit(ctx context.Context, p nodeapi.Peer) error {
	const op = "multinode.Admit"
	if newerRelease(p.Version, s.cfg.Version) {
		return tooNew(op, p.Version, s.cfg.Version)
	}
	obj, err := s.cfg.Store.Get(ctx, v1.KindWorkerNode.GVK(), "", p.Node)
	if err != nil {
		return fault.Unauthorizedf(op, "worker node %s is not declared", p.Node)
	}
	n, ok := obj.(*v1.WorkerNode)
	switch renewed := ok && n.Status.RenewedCertificate.Is(p.Serial); {
	case ok && n.Status.Certificate.Is(p.Serial):
	case renewed:
		if err := s.useRenewed(ctx, p.Node, p.Serial); err != nil {
			return err
		}
	default:
		return fault.Unauthorizedf(op, "this certificate is not the node's current one; join again")
	}
	s.cfg.Liveness.Seen(p.Node, p.Version, p.APIVersion, p.Platform)
	return nil
}

// useRenewed makes node's renewed certificate of serial its certificate.
func (s *Handler) useRenewed(ctx context.Context, node v1.ObjectName, serial string) error {
	return writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		if !n.Status.RenewedCertificate.Is(serial) {
			return false
		}
		n.Status.Certificate, n.Status.RenewedCertificate = n.Status.RenewedCertificate, nil
		return true
	})
}

// Renew is a new certificate for the node's key. The node's certificate stays until it uses the new one, so an answer that
// never reaches it costs nothing.
func (s *Handler) Renew(ctx context.Context, node v1.ObjectName, in nodeapi.RenewRequest) (nodeapi.JoinResponse, error) {
	now := s.cfg.Now()
	cert, serial, notAfter, err := s.cfg.CA.SignNode(in.CSR, node, now)
	if err != nil {
		return nodeapi.JoinResponse{}, err
	}
	if err := writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		n.Status.RenewedCertificate = &v1.NodeCertificate{Serial: serial, NotAfter: v1.NewTimestamp(notAfter), IssuedAt: v1.NewTimestamp(now)}
		return true
	}); err != nil {
		return nodeapi.JoinResponse{}, err
	}
	s.cfg.Logger.InfoContext(ctx, "worker node certificate renewed", "node", node, "notAfter", notAfter)
	return nodeapi.JoinResponse{Certificate: cert, CA: s.cfg.CA.PEM()}, nil
}

func tooNew(op, release, ours string) error {
	return fault.Conflictf(op, "this node runs funcd %s, newer than the control plane's %s: upgrade the control plane first", release, ours)
}

// Join swaps a one-time token for a node certificate (decision 2). The token must match the hash the node's WorkerNode
// keeps, not be expired, and no running node may hold the name; the swap uses the token up.
func (s *Handler) Join(ctx context.Context, in nodeapi.JoinRequest) (nodeapi.JoinResponse, error) {
	const op = "multinode.join"
	tok, err := ParseToken(in.Token)
	switch {
	case err != nil:
		return nodeapi.JoinResponse{}, err
	case tok.CAHash != s.cfg.CA.Hash():
		return nodeapi.JoinResponse{}, fault.Forbiddenf(op, "the join token was minted by another control plane")
	case s.cfg.Liveness.Ready(tok.Node):
		return nodeapi.JoinResponse{}, fault.Conflictf(op, "a running node holds the name %s", tok.Node)
	case newerRelease(in.Version, s.cfg.Version):
		return nodeapi.JoinResponse{}, tooNew(op, in.Version, s.cfg.Version)
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
		return nodeapi.JoinResponse{}, fault.Forbiddenf(op, "no WorkerNode %s is declared", tok.Node)
	case err != nil:
		return nodeapi.JoinResponse{}, err
	case denied != nil:
		return nodeapi.JoinResponse{}, denied
	}
	s.cfg.Logger.InfoContext(ctx, "worker node joined", "node", tok.Node, "version", in.Version, "platform", in.Platform)
	return nodeapi.JoinResponse{Certificate: cert, CA: s.cfg.CA.PEM()}, nil
}

// placedOn reports whether namespace ns is placed on node.
func (s *Handler) placedOn(ctx context.Context, ns v1.NamespaceName, node v1.ObjectName) bool {
	at, err := s.cfg.Placement.NodeOf(ctx, ns)
	return err == nil && at == node && node != ""
}

// mayFollow refuses a node that follows while the control plane is held after its restore (ADR-0206): the metastore is at
// an older point until a person releases it, and the node runs on what it holds.
func (s *Handler) mayFollow() error {
	if s.cfg.Held != nil && s.cfg.Held() {
		return fault.Unavailablef("multinode.follow", "the control plane is held after its restore: its nodes follow it "+
			"again once `funcdctl hold release` releases it")
	}
	return nil
}

// mustBePlacedOn refuses a call about a namespace that is not placed on node.
func (s *Handler) mustBePlacedOn(ctx context.Context, op string, ns v1.NamespaceName, node v1.ObjectName) error {
	if !s.placedOn(ctx, ns, node) {
		return fault.Forbiddenf(op, "namespace %s is not placed on worker node %s", ns, node)
	}
	return nil
}

// concerns reports whether node takes the cluster-scoped obj whole: its own Namespaces, the API tokens that span one of
// them (decision 7), and every object of the other kinds it follows.
func (s *Handler) concerns(ctx context.Context, node v1.ObjectName, obj v1.Object) bool {
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
func (s *Handler) forNode(ctx context.Context, node v1.ObjectName, c store.Change) (store.Change, error) {
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

// Snapshot is every object of the node's namespaces (or the one asked), and the cluster-scoped ones that concern it, at
// the revision it follows the feed after. The node's own children are left out.
func (s *Handler) Snapshot(ctx context.Context, node v1.ObjectName, ns v1.NamespaceName) (nodeapi.Snapshot, error) {
	if err := s.mayFollow(); err != nil {
		return nodeapi.Snapshot{}, err
	}
	head, err := s.cl.Head(ctx)
	if err != nil {
		return nodeapi.Snapshot{}, err
	}
	var namespaces []v1.NamespaceName
	if ns != "" {
		if err := s.mustBePlacedOn(ctx, "multinode.snapshot", ns, node); err != nil {
			return nodeapi.Snapshot{}, err
		}
		namespaces = []v1.NamespaceName{ns}
	} else if namespaces, err = s.cfg.Placement.Namespaces(ctx, node); err != nil {
		return nodeapi.Snapshot{}, err
	}
	out := nodeapi.Snapshot{Rev: head, Timeline: s.cl.Timeline(), Objects: []store.Change{}}
	add := func(obj v1.Object) error {
		c, err := added(obj, head)
		out.Objects = append(out.Objects, c)
		return err
	}
	for _, k := range NodeKinds() {
		res, err := s.cfg.Store.List(ctx, k.GVK(), store.ListOptions{})
		if err != nil {
			return nodeapi.Snapshot{}, err
		}
		for _, obj := range res.Items {
			if !s.concerns(ctx, node, obj) {
				continue
			}
			if err := add(obj); err != nil {
				return nodeapi.Snapshot{}, err
			}
		}
	}
	for _, ns := range namespaces {
		nsObj, err := s.cfg.Store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
		if err != nil {
			return nodeapi.Snapshot{}, err
		}
		if err := add(nsObj); err != nil {
			return nodeapi.Snapshot{}, err
		}
		if err := s.each(ctx, v1.AllKinds(), ns, func(obj v1.Object) error {
			if len(obj.GetObjectMeta().OwnerReferences) > 0 {
				return nil
			}
			return add(obj)
		}); err != nil {
			return nodeapi.Snapshot{}, err
		}
	}
	return out, nil
}

// each calls f with each object of the namespaced kinds in namespace ns.
func (s *Handler) each(ctx context.Context, kinds []v1.Kind, ns v1.NamespaceName, f func(v1.Object) error) error {
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

// Children are the children a node made in the namespaces placed on it, as the control plane holds them.
func (s *Handler) Children(ctx context.Context, node v1.ObjectName) ([]store.Change, error) {
	namespaces, err := s.cfg.Placement.Namespaces(ctx, node)
	if err != nil {
		return nil, err
	}
	out := []store.Change{}
	for _, ns := range namespaces {
		if err := s.each(ctx, childKinds(), ns, func(obj v1.Object) error {
			if len(obj.GetObjectMeta().OwnerReferences) == 0 {
				return nil
			}
			c, err := added(obj, 0)
			out = append(out, c)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Changes are the node's records after since (decision 3): those of the namespaces placed on it and of the cluster-scoped
// kinds it follows, as they concern it, so it sees placements come and go, but not the ones it wrote itself. A since older
// than the change log, or of another timeline of the metastore (one restored from a backup since), is
// fault.PreconditionFailed: the node starts again from a snapshot.
func (s *Handler) Changes(ctx context.Context, node v1.ObjectName, since uint64, timeline string) (nodeapi.Feed, error) {
	const op = "multinode.feed"
	if err := s.mayFollow(); err != nil {
		return nodeapi.Feed{}, err
	}
	if timeline != s.cl.Timeline() {
		return nodeapi.Feed{}, fault.PreconditionFailedf(op, "the revision is of another timeline of the metastore, which was restored since")
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
	out, cursor, err := s.cl.Changes(ctx, since, feedLimit, keep)
	if err != nil {
		return nodeapi.Feed{}, err
	}
	for i := range out {
		if out[i], err = s.forNode(ctx, node, out[i]); err != nil {
			return nodeapi.Feed{}, err
		}
	}
	return nodeapi.Feed{Rev: cursor, Changes: out}, nil
}

// Changed is closed at the change log's next record.
func (s *Handler) Changed() <-chan struct{} { return s.cl.ChangeSignal() }

// Report writes what the node reports of itself into its WorkerNode's status.
func (s *Handler) Report(ctx context.Context, node v1.ObjectName, in nodeapi.NodeReport) error {
	return writeNodeStatus(ctx, s.cfg.Store, node, func(n *v1.WorkerNode) bool {
		n.Status.BlobStore, n.Status.Backup = in.BlobStore, in.Backup
		return true
	})
}

// Status writes a node's status back (decision 4): the object's status becomes the node's, but the decisions the control
// plane records there; nothing else of it changes. The object must sit in a namespace placed on the node, be the one the
// node runs, by its uid, and pass validation (decision 5).
func (s *Handler) Status(ctx context.Context, node v1.ObjectName, in nodeapi.StatusWrite) error {
	const op = "multinode.status"
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, node); err != nil {
		return err
	}
	ctx = store.WithoutChangeLog(ctx)
	for range 5 {
		cur, err := s.cfg.Store.Get(ctx, in.Kind.GVK(), in.Namespace, in.Name)
		if err != nil {
			return err
		}
		if cur.GetObjectMeta().UID != in.UID {
			return fault.Conflictf(op, "%s %s was created again since the node took it", in.Kind, in.Name)
		}
		obj, err := withStatus(cur, in.Status)
		if err != nil {
			return err
		}
		KeepDecisions(obj, cur)
		if err := obj.Validate(); err != nil {
			return err
		}
		if _, err = s.cfg.Store.Update(ctx, obj); fault.KindOf(err) != fault.Conflict {
			return err
		}
	}
	return fault.Conflictf(op, "%s %s kept changing", in.Kind, in.Name)
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

// PutObject writes, in a namespace placed on the node, a child the node owns (one with ownerReferences), created or
// replaced, or an object the node makes with no owner, created, after which only its status changes. The copy is the
// node's object, under its uid. A node never writes an object a person wrote, and one it makes under a name another object
// holds is refused: the node takes that object instead.
func (s *Handler) PutObject(ctx context.Context, node v1.ObjectName, in nodeapi.ObjectWrite) error {
	const op = "multinode.putObject"
	obj, ok := v1.NewObject(in.Kind)
	if !ok {
		return fault.Invalidf(op, "unknown kind %q", in.Kind)
	}
	if err := json.Unmarshal(in.Object, obj); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "decode the %s", in.Kind)
	}
	m := obj.GetObjectMeta()
	if err := s.mustBePlacedOn(ctx, op, m.Namespace, node); err != nil {
		return err
	}
	if !WrittenBack(in.Kind, len(m.OwnerReferences) > 0) {
		return fault.Forbiddenf(op, "a node writes back only the children it owns and the objects it makes; not %s %s",
			in.Kind, m.Name)
	}
	if err := obj.Validate(); err != nil {
		return err
	}
	ctx = store.AsCopy(store.WithOrigin(ctx, node))
	for range 5 {
		err := s.putCopy(ctx, node, obj, in.Object)
		if fault.KindOf(err) != fault.Conflict || errors.Is(err, errNameHeld) {
			return err
		}
	}
	return fault.Conflictf(op, "%s %s kept changing", in.Kind, m.Name)
}

// errNameHeld is an object a node made under a name another object holds on the control plane.
var errNameHeld = fault.Conflictf("multinode.putObject", "another object holds the name")

// putCopy writes obj, which node sent as raw, once: a Conflict is a write to try again, but errNameHeld.
func (s *Handler) putCopy(ctx context.Context, node v1.ObjectName, obj v1.Object, raw json.RawMessage) error {
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
		return fault.Forbiddenf(op, "%s %s was written by a person, not by worker node %s", gvk.Kind, m.Name, node)
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

// DeleteObject deletes an object the node holds, when it is one a node writes back whole, or one whose deletion the
// control plane held until the node ended it (ADR-0345 Decision 6). An object of another uid took the name since, so it
// stays.
func (s *Handler) DeleteObject(ctx context.Context, node v1.ObjectName, in nodeapi.ObjectRef) error {
	const op = "multinode.deleteObject"
	if in.UID == "" {
		return fault.Invalidf(op, "a node's delete names the object's uid")
	}
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, node); err != nil {
		return err
	}
	cur, err := s.cfg.Store.Get(ctx, in.Kind.GVK(), in.Namespace, in.Name)
	if fault.KindOf(err) == fault.NotFound || err == nil && cur.GetObjectMeta().UID != in.UID {
		return nil
	}
	if err != nil {
		return err
	}
	if m := cur.GetObjectMeta(); !WrittenBack(in.Kind, len(m.OwnerReferences) > 0) && m.DeletionTime == nil {
		return fault.Forbiddenf(op, "%s %s was written by a person, not by worker node %s", in.Kind, in.Name, node)
	}
	err = s.cfg.Store.Delete(store.WithOrigin(ctx, node), in.Kind.GVK(), in.Namespace, in.Name, "")
	if fault.KindOf(err) == fault.NotFound {
		err = nil
	}
	return err
}

// SecretWriter writes one key of a Secret as a workload that binds it (ADR-0327): the platform's secret service.
type SecretWriter interface {
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key, value string) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, binding, key string) error
}

// Secret makes a workload's write, from a namespace placed on the node, of one key of a Secret it binds. The control
// plane holds the Secret, so it makes the checks and the write as for a workload of its own, and the change reaches the
// node through the feed.
func (s *Handler) Secret(ctx context.Context, node v1.ObjectName, in nodeapi.SecretWrite) (nodeapi.SecretAnswer, error) {
	const op = "multinode.secret"
	if err := s.mustBePlacedOn(ctx, op, in.Namespace, node); err != nil {
		return nodeapi.SecretAnswer{}, err
	}
	if s.cfg.Secrets == nil {
		return nodeapi.SecretAnswer{}, fault.Unavailablef(op, "this control plane writes no Secret")
	}
	var out nodeapi.SecretAnswer
	if err := s.writeSecret(ctx, in, &out); err != nil {
		out.Error, out.Fault = err.Error(), fault.KindOf(err)
	}
	return out, nil
}

func (s *Handler) writeSecret(ctx context.Context, in nodeapi.SecretWrite, out *nodeapi.SecretAnswer) error {
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

// Key is the master of a namespace placed on the node (decision 8).
func (s *Handler) Key(ctx context.Context, node v1.ObjectName, ns v1.NamespaceName) (nodeapi.NamespaceKey, error) {
	const op = "multinode.keys"
	if err := s.mustBePlacedOn(ctx, op, ns, node); err != nil {
		return nodeapi.NamespaceKey{}, err
	}
	if s.cfg.Keys == nil {
		return nodeapi.NamespaceKey{}, fault.Unavailablef(op, "this control plane derives no namespace keys")
	}
	return s.cfg.Keys.Of(ctx, ns)
}
