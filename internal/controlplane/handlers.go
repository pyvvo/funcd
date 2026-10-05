// Package controlplane: the store-backed Handlers implementation (ADR-0018) that fills
// ADR-0005's seam. Each of the 15 kinds' CRUD operations runs the same path —
// authorize (PDP) → admit (validate, writes only) → store — through six non-generic
// v1.Object helpers; the per-kind methods are thin typed wrappers. No generics, no any.
package controlplane

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
)

// storeHandlers is the real, store-backed Handlers: authenticated (via the authn
// middleware that populates ctx), namespace-authorized (the PDP), and admission-validated.
type storeHandlers struct {
	store     store.Store
	authz     auth.Authorizer
	admit     *admission.Pipeline
	locks     *nsLocks       // ADR-0147
	collector OwnerCollector // ADR-0170: a forced ResourceGroup delete; nil ⇒ force answers Unavailable
}

// NewStoreHandlers builds the store-backed control-plane Handlers (ADR-0018). The admission
// pipeline (ADR-0063) is the admit step on every write; pass admission.NewPipeline(...).
func NewStoreHandlers(st store.Store, authz auth.Authorizer, admit *admission.Pipeline, collector OwnerCollector) Handlers {
	return &storeHandlers{store: st, authz: authz, admit: admit, locks: newNSLocks(), collector: collector}
}

// listReader adapts store.Store to admission.StoreReader.
type listReader struct{ s store.Store }

func (r listReader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// lockFor takes ns's admission lock when an admission of (kind, op) reads the namespace (ADR-0147), so
// the Old fetch, the admission and the store write run as one step against concurrent marked writes.
// An unmarked write returns a no-op unlock.
func (h *storeHandlers) lockFor(ctx context.Context, kind v1.Kind, op admission.Operation, ns v1.NamespaceName) (func(), error) {
	if !h.admit.ReadsNamespace(kind.GVK(), op) {
		return func() {}, nil
	}
	return h.locks.lock(ctx, ns)
}

// --- the six shared helpers (one authz + admission + store path) ---

func (h *storeHandlers) authorize(ctx context.Context, verb auth.Verb, kind v1.Kind, ns v1.NamespaceName) error {
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf("controlplane.authz", "no authenticated identity")
	}
	dec, err := h.authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: kind, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "controlplane.authz", "authorize %s", kind)
	}
	if !dec.Allowed {
		return fault.Forbiddenf("controlplane.authz", "%s %s in %q denied: %s", verb, kind, ns, dec.Reason)
	}
	return nil
}

func (h *storeHandlers) getObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if err := h.authorize(ctx, auth.VerbGet, kind, ns); err != nil {
		return nil, err
	}
	return h.store.Get(ctx, kind.GVK(), ns, name)
}

func (h *storeHandlers) listObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName) ([]v1.Object, error) {
	if err := h.authorize(ctx, auth.VerbList, kind, ns); err != nil {
		return nil, err
	}
	res, err := h.store.List(ctx, kind.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

func (h *storeHandlers) createObj(ctx context.Context, kind v1.Kind, obj v1.Object) (v1.Object, error) {
	if err := h.authorize(ctx, auth.VerbCreate, kind, obj.GetObjectMeta().Namespace); err != nil {
		return nil, err
	}
	stampTypeMeta(obj, kind) // the route's kind owns TypeMeta (k8s-style)
	// Server-side name generation (ObjectMeta.GenerateName): fill Name before admission + the store
	// validate the object, so a client can create without inventing a unique name. store.Create retries
	// on the rare collision. An explicit Name ignores GenerateName: dropping it keeps the store from
	// renaming a duplicate, which must stay a Conflict.
	if m := obj.GetObjectMeta(); m.Name == "" && m.GenerateName != "" {
		m.Name = v1.GenerateObjectName(m.GenerateName)
	} else {
		m.GenerateName = ""
	}
	unlock, err := h.lockFor(ctx, kind, admission.Create, obj.GetObjectMeta().Namespace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	id, _ := middleware.IdentityFrom(ctx)
	admitted, err := h.admit.Admit(ctx, admission.Request{ // admit step (ADR-0063 pipeline)
		Operation: admission.Create, GVK: kind.GVK(), Object: obj, Identity: id,
	})
	if err != nil {
		return nil, err
	}
	if admitted, err = withStatus(admitted, nil); err != nil {
		return nil, err
	}
	withServerMeta(admitted, nil)
	return h.store.Create(ctx, admitted)
}

// stampTypeMeta sets the object's apiVersion/kind from the route's kind. The control-plane
// endpoint determines the kind, not the request body, so the server normalizes it.
func stampTypeMeta(obj v1.Object, kind v1.Kind) {
	tm := v1.TypeMeta{APIVersion: kind.GVK().APIVersion(), Kind: kind}
	switch o := obj.(type) {
	case *v1.Namespace:
		o.TypeMeta = tm
	case *v1.ResourceGroup:
		o.TypeMeta = tm
	case *v1.Function:
		o.TypeMeta = tm
	case *v1.Revision:
		o.TypeMeta = tm
	case *v1.Route:
		o.TypeMeta = tm
	case *v1.Service:
		o.TypeMeta = tm
	case *v1.EventSource:
		o.TypeMeta = tm
	case *v1.ConfigMap:
		o.TypeMeta = tm
	case *v1.Secret:
		o.TypeMeta = tm
	case *v1.Grant:
		o.TypeMeta = tm
	case *v1.EgressPolicy:
		o.TypeMeta = tm
	case *v1.Invocation:
		o.TypeMeta = tm
	case *v1.RuntimeClass:
		o.TypeMeta = tm
	case *v1.WorkerNode:
		o.TypeMeta = tm
	case *v1.Gateway:
		o.TypeMeta = tm
	case *v1.KVStore:
		o.TypeMeta = tm
	case *v1.Bucket:
		o.TypeMeta = tm
	case *v1.CatalogService:
		o.TypeMeta = tm
	case *v1.Policy:
		o.TypeMeta = tm
	case *v1.Workflow:
		o.TypeMeta = tm
	case *v1.WorkflowRun:
		o.TypeMeta = tm
	case *v1.Sensor:
		o.TypeMeta = tm
	case *v1.Identity:
		o.TypeMeta = tm
	case *v1.Role:
		o.TypeMeta = tm
	case *v1.RolesAssignment:
		o.TypeMeta = tm
	case *v1.Site:
		o.TypeMeta = tm
	}
}

func (h *storeHandlers) replaceObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, obj v1.Object) (v1.Object, error) {
	if err := h.authorize(ctx, auth.VerbUpdate, kind, ns); err != nil {
		return nil, err
	}
	meta := obj.GetObjectMeta()
	if err := matchPathNamespace(ns, meta); err != nil {
		return nil, err
	}
	if meta.Name != name {
		return nil, fault.Invalidf("controlplane.admit", "body name %q does not match path %q", meta.Name, name)
	}
	stampTypeMeta(obj, kind) // route's kind owns TypeMeta (see createObj)
	unlock, err := h.lockFor(ctx, kind, admission.Update, ns)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cur, err := h.store.Get(ctx, kind.GVK(), ns, name) // fetch Old BEFORE admit (reused for the RV read)
	if err != nil {
		return nil, err
	}
	id, _ := middleware.IdentityFrom(ctx)
	admitted, err := h.admit.Admit(ctx, admission.Request{ // admit step (ADR-0063 pipeline) — sees Old
		Operation: admission.Update, GVK: kind.GVK(), Object: obj, Old: cur, Identity: id,
	})
	if err != nil {
		return nil, err
	}
	if admitted, err = withStatus(admitted, cur); err != nil {
		return nil, err
	}
	withServerMeta(admitted, cur)
	admitted.GetObjectMeta().ResourceVersion = cur.GetObjectMeta().ResourceVersion // read-RV-then-update (ADR-0018 workaround)
	return h.store.Update(ctx, admitted)
}

// matchPathNamespace is ADR-0018 §4's path/body namespace consistency check: the body's
// metadata.namespace must equal the path namespace (→ 400). The create routes run it before
// CreateX, whose ADR-0005 signature carries no path namespace.
func matchPathNamespace(path v1.NamespaceName, meta *v1.ObjectMeta) error {
	if meta.Namespace != path {
		return fault.Invalidf("controlplane.admit", "body namespace %q does not match path %q", meta.Namespace, path)
	}
	return nil
}

// withStatus returns obj with from's status, or with none when from is nil. Status is server-owned: controllers
// write it through the store, never through the API, so a create or a replace never takes it from the client
// (ADR-0048 ignores the server-set metadata on input the same way).
func withStatus(obj, from v1.Object) (v1.Object, error) {
	const op = "controlplane.withStatus"
	fields, err := jsonFields(obj)
	if err != nil {
		return nil, err
	}
	delete(fields, "status")
	if from != nil {
		src, serr := jsonFields(from)
		if serr != nil {
			return nil, serr
		}
		if st, ok := src["status"]; ok {
			fields["status"] = st
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fault.Internalf(op, "marshal: %v", err)
	}
	kind := obj.GroupVersionKind().Kind
	fresh, ok := v1.NewObject(kind)
	if !ok {
		return nil, fault.Internalf(op, "unknown kind %q", kind)
	}
	if err := json.Unmarshal(raw, fresh); err != nil {
		return nil, fault.Internalf(op, "unmarshal: %v", err)
	}
	return fresh, nil
}

// withServerMeta sets obj's ownerReferences and deletionTimestamp to from's, or clears them when from is nil.
// ADR-0048 ignores both on input, and the store cannot drop them itself: reconcilers set owner references
// through store.Create and store.Update.
func withServerMeta(obj, from v1.Object) {
	m := obj.GetObjectMeta()
	m.OwnerReferences, m.DeletionTime = nil, nil
	if from != nil {
		fm := from.GetObjectMeta()
		m.OwnerReferences, m.DeletionTime = fm.OwnerReferences, fm.DeletionTime
	}
}

// jsonFields returns obj's top-level JSON fields.
func jsonFields(obj v1.Object) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("controlplane.jsonFields", "marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.Internalf("controlplane.jsonFields", "unmarshal: %v", err)
	}
	return m, nil
}

func (h *storeHandlers) deleteObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObjIf(ctx, kind, ns, name, "")
}

// deleteObjIf is the whole delete path: authorize → the namespace admission lock when a Delete admission reads
// the namespace (ADR-0147) → Get Old → Admit → store.Delete with rv as the precondition ("" ⇒ none) → unlock.
func (h *storeHandlers) deleteObjIf(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	if err := h.authorize(ctx, auth.VerbDelete, kind, ns); err != nil {
		return err
	}
	unlock, err := h.lockFor(ctx, kind, admission.Delete, ns)
	if err != nil {
		return err
	}
	defer unlock()
	// Run the admit step on Delete only when an admission handles it (e.g. ADR-0064 deletion-protection),
	// so a build with no Delete admission does no extra store fetch.
	if h.admit.Handles(kind.GVK(), admission.Delete) {
		old, err := h.store.Get(ctx, kind.GVK(), ns, name)
		if err != nil {
			return err
		}
		id, _ := middleware.IdentityFrom(ctx)
		if _, err := h.admit.Admit(ctx, admission.Request{
			Operation: admission.Delete, GVK: kind.GVK(), Old: old, Identity: id,
		}); err != nil {
			return err
		}
	}
	return h.store.Delete(ctx, kind.GVK(), ns, name, rv)
}

// forceDeleteResourceGroup deletes a ResourceGroup's members, then the group (ADR-0170 Decision 8). Each pass
// lists the members and deletes each through deleteObjIf with its listed resourceVersion, Sensors first, so every
// member's authorization and admissions apply; then it collects the namespace's dead-owned children. A pass that
// deleted a member runs another; one that deleted none stops with its first 409, else deletes the group normally.
func (h *storeHandlers) forceDeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	const op = "controlplane.forceDeleteResourceGroup"
	if err := h.authorize(ctx, auth.VerbDelete, v1.KindResourceGroup, ns); err != nil {
		return err
	}
	if h.collector == nil {
		return fault.Unavailablef(op, "no owner garbage collector is wired; force is unavailable")
	}
	if _, err := h.store.Get(ctx, v1.KindResourceGroup.GVK(), ns, name); err != nil {
		return err
	}
	group := v1.ResourceGroupName(name)
	for {
		members, err := admission.Members(ctx, listReader{h.store}, ns, group)
		if err != nil {
			return err
		}
		slices.SortStableFunc(members, func(a, b v1.Object) int {
			return boolRank(b.GroupVersionKind().Kind == v1.KindSensor) - boolRank(a.GroupVersionKind().Kind == v1.KindSensor)
		})
		deleted := false
		var refused error
		for _, m := range members {
			ok, err := h.deleteMember(ctx, m, group)
			switch {
			case ok:
				deleted = true
			case fault.KindOf(err) == fault.Conflict:
				if refused == nil {
					refused = err
				}
			case err != nil:
				return err
			}
		}
		if err := h.collector.CollectNamespace(ctx, ns); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "collect children in %q", ns)
		}
		if deleted {
			continue
		}
		if refused != nil {
			return refused
		}
		return h.deleteObj(ctx, v1.KindResourceGroup, ns, name)
	}
}

// deleteMember deletes one listed member. On a 409 it re-reads it once and retries with the new
// resourceVersion if it is still a member; gone or moved is skipped. It reports whether it deleted it.
func (h *storeHandlers) deleteMember(ctx context.Context, m v1.Object, group v1.ResourceGroupName) (bool, error) {
	kind, meta := m.GroupVersionKind().Kind, m.GetObjectMeta()
	err := h.deleteObjIf(ctx, kind, meta.Namespace, meta.Name, meta.ResourceVersion)
	if err == nil {
		return true, nil
	}
	switch fault.KindOf(err) {
	case fault.NotFound:
		return false, nil
	case fault.Conflict:
	default:
		return false, err
	}
	cur, gerr := h.store.Get(ctx, kind.GVK(), meta.Namespace, meta.Name)
	if fault.KindOf(gerr) == fault.NotFound {
		return false, nil
	}
	if gerr != nil {
		return false, gerr
	}
	cm := cur.GetObjectMeta()
	if _, owned := v1.ControllerOf(cm.OwnerReferences); cm.ResourceGroup != group || owned {
		return false, nil
	}
	err = h.deleteObjIf(ctx, kind, cm.Namespace, cm.Name, cm.ResourceVersion)
	if fault.KindOf(err) == fault.NotFound {
		return false, nil
	}
	return err == nil, err
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- Namespace (cluster-scoped) ---

func (h *storeHandlers) GetNamespace(ctx context.Context, name v1.ObjectName) (v1.Namespace, error) {
	o, err := h.getObj(ctx, v1.KindNamespace, "", name)
	if err != nil {
		return v1.Namespace{}, err
	}
	return *o.(*v1.Namespace), nil
}

func (h *storeHandlers) CreateNamespace(ctx context.Context, ns v1.Namespace) (v1.Namespace, error) {
	o, err := h.createObj(ctx, v1.KindNamespace, &ns)
	if err != nil {
		return v1.Namespace{}, err
	}
	return *o.(*v1.Namespace), nil
}

func (h *storeHandlers) ListNamespaces(ctx context.Context) ([]v1.Namespace, error) {
	objs, err := h.listObj(ctx, v1.KindNamespace, "")
	if err != nil {
		return nil, err
	}
	out := make([]v1.Namespace, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Namespace)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceNamespace(ctx context.Context, name v1.ObjectName, ns v1.Namespace) (v1.Namespace, error) {
	o, err := h.replaceObj(ctx, v1.KindNamespace, "", name, &ns)
	if err != nil {
		return v1.Namespace{}, err
	}
	return *o.(*v1.Namespace), nil
}

func (h *storeHandlers) DeleteNamespace(ctx context.Context, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindNamespace, "", name)
}

// --- ResourceGroup (namespaced) ---

func (h *storeHandlers) GetResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ResourceGroup, error) {
	o, err := h.getObj(ctx, v1.KindResourceGroup, ns, name)
	if err != nil {
		return v1.ResourceGroup{}, err
	}
	return *o.(*v1.ResourceGroup), nil
}

func (h *storeHandlers) CreateResourceGroup(ctx context.Context, rg v1.ResourceGroup) (v1.ResourceGroup, error) {
	o, err := h.createObj(ctx, v1.KindResourceGroup, &rg)
	if err != nil {
		return v1.ResourceGroup{}, err
	}
	return *o.(*v1.ResourceGroup), nil
}

func (h *storeHandlers) ListResourceGroups(ctx context.Context, ns v1.NamespaceName) ([]v1.ResourceGroup, error) {
	objs, err := h.listObj(ctx, v1.KindResourceGroup, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.ResourceGroup, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.ResourceGroup)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rg v1.ResourceGroup) (v1.ResourceGroup, error) {
	o, err := h.replaceObj(ctx, v1.KindResourceGroup, ns, name, &rg)
	if err != nil {
		return v1.ResourceGroup{}, err
	}
	return *o.(*v1.ResourceGroup), nil
}

func (h *storeHandlers) DeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, force bool) error {
	if force {
		return h.forceDeleteResourceGroup(ctx, ns, name)
	}
	return h.deleteObj(ctx, v1.KindResourceGroup, ns, name)
}

// --- Function (namespaced) ---

func (h *storeHandlers) GetFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Function, error) {
	o, err := h.getObj(ctx, v1.KindFunction, ns, name)
	if err != nil {
		return v1.Function{}, err
	}
	return *o.(*v1.Function), nil
}

func (h *storeHandlers) CreateFunction(ctx context.Context, fn v1.Function) (v1.Function, error) {
	o, err := h.createObj(ctx, v1.KindFunction, &fn)
	if err != nil {
		return v1.Function{}, err
	}
	return *o.(*v1.Function), nil
}

func (h *storeHandlers) ListFunctions(ctx context.Context, ns v1.NamespaceName) ([]v1.Function, error) {
	objs, err := h.listObj(ctx, v1.KindFunction, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Function, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Function)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, fn v1.Function) (v1.Function, error) {
	o, err := h.replaceObj(ctx, v1.KindFunction, ns, name, &fn)
	if err != nil {
		return v1.Function{}, err
	}
	return *o.(*v1.Function), nil
}

func (h *storeHandlers) DeleteFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindFunction, ns, name)
}

// --- Revision (namespaced) ---

func (h *storeHandlers) GetRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Revision, error) {
	o, err := h.getObj(ctx, v1.KindRevision, ns, name)
	if err != nil {
		return v1.Revision{}, err
	}
	return *o.(*v1.Revision), nil
}

func (h *storeHandlers) CreateRevision(ctx context.Context, rev v1.Revision) (v1.Revision, error) {
	o, err := h.createObj(ctx, v1.KindRevision, &rev)
	if err != nil {
		return v1.Revision{}, err
	}
	return *o.(*v1.Revision), nil
}

func (h *storeHandlers) ListRevisions(ctx context.Context, ns v1.NamespaceName) ([]v1.Revision, error) {
	objs, err := h.listObj(ctx, v1.KindRevision, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Revision, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Revision)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rev v1.Revision) (v1.Revision, error) {
	o, err := h.replaceObj(ctx, v1.KindRevision, ns, name, &rev)
	if err != nil {
		return v1.Revision{}, err
	}
	return *o.(*v1.Revision), nil
}

func (h *storeHandlers) DeleteRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindRevision, ns, name)
}

// --- Route (namespaced) ---

func (h *storeHandlers) GetRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Route, error) {
	o, err := h.getObj(ctx, v1.KindRoute, ns, name)
	if err != nil {
		return v1.Route{}, err
	}
	return *o.(*v1.Route), nil
}

func (h *storeHandlers) CreateRoute(ctx context.Context, rt v1.Route) (v1.Route, error) {
	o, err := h.createObj(ctx, v1.KindRoute, &rt)
	if err != nil {
		return v1.Route{}, err
	}
	return *o.(*v1.Route), nil
}

func (h *storeHandlers) ListRoutes(ctx context.Context, ns v1.NamespaceName) ([]v1.Route, error) {
	objs, err := h.listObj(ctx, v1.KindRoute, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Route, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Route)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rt v1.Route) (v1.Route, error) {
	o, err := h.replaceObj(ctx, v1.KindRoute, ns, name, &rt)
	if err != nil {
		return v1.Route{}, err
	}
	return *o.(*v1.Route), nil
}

func (h *storeHandlers) DeleteRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindRoute, ns, name)
}

// --- Site (namespaced) — ADR-0139, FEAT-0003/F103 ---

func (h *storeHandlers) GetSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Site, error) {
	o, err := h.getObj(ctx, v1.KindSite, ns, name)
	if err != nil {
		return v1.Site{}, err
	}
	return *o.(*v1.Site), nil
}

func (h *storeHandlers) CreateSite(ctx context.Context, si v1.Site) (v1.Site, error) {
	o, err := h.createObj(ctx, v1.KindSite, &si)
	if err != nil {
		return v1.Site{}, err
	}
	return *o.(*v1.Site), nil
}

func (h *storeHandlers) ListSites(ctx context.Context, ns v1.NamespaceName) ([]v1.Site, error) {
	objs, err := h.listObj(ctx, v1.KindSite, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Site, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Site)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, si v1.Site) (v1.Site, error) {
	o, err := h.replaceObj(ctx, v1.KindSite, ns, name, &si)
	if err != nil {
		return v1.Site{}, err
	}
	return *o.(*v1.Site), nil
}

func (h *storeHandlers) DeleteSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindSite, ns, name)
}

// --- Service (namespaced) ---

func (h *storeHandlers) GetService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Service, error) {
	o, err := h.getObj(ctx, v1.KindService, ns, name)
	if err != nil {
		return v1.Service{}, err
	}
	return *o.(*v1.Service), nil
}

func (h *storeHandlers) CreateService(ctx context.Context, svc v1.Service) (v1.Service, error) {
	o, err := h.createObj(ctx, v1.KindService, &svc)
	if err != nil {
		return v1.Service{}, err
	}
	return *o.(*v1.Service), nil
}

func (h *storeHandlers) ListServices(ctx context.Context, ns v1.NamespaceName) ([]v1.Service, error) {
	objs, err := h.listObj(ctx, v1.KindService, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Service, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Service)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, svc v1.Service) (v1.Service, error) {
	o, err := h.replaceObj(ctx, v1.KindService, ns, name, &svc)
	if err != nil {
		return v1.Service{}, err
	}
	return *o.(*v1.Service), nil
}

func (h *storeHandlers) DeleteService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindService, ns, name)
}

// --- EventSource (namespaced) ---

func (h *storeHandlers) GetEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EventSource, error) {
	o, err := h.getObj(ctx, v1.KindEventSource, ns, name)
	if err != nil {
		return v1.EventSource{}, err
	}
	return *o.(*v1.EventSource), nil
}

func (h *storeHandlers) CreateEventSource(ctx context.Context, es v1.EventSource) (v1.EventSource, error) {
	o, err := h.createObj(ctx, v1.KindEventSource, &es)
	if err != nil {
		return v1.EventSource{}, err
	}
	return *o.(*v1.EventSource), nil
}

func (h *storeHandlers) ListEventSources(ctx context.Context, ns v1.NamespaceName) ([]v1.EventSource, error) {
	objs, err := h.listObj(ctx, v1.KindEventSource, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.EventSource, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.EventSource)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, es v1.EventSource) (v1.EventSource, error) {
	o, err := h.replaceObj(ctx, v1.KindEventSource, ns, name, &es)
	if err != nil {
		return v1.EventSource{}, err
	}
	return *o.(*v1.EventSource), nil
}

func (h *storeHandlers) DeleteEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindEventSource, ns, name)
}

// --- ConfigMap (namespaced) ---

func (h *storeHandlers) GetConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ConfigMap, error) {
	o, err := h.getObj(ctx, v1.KindConfigMap, ns, name)
	if err != nil {
		return v1.ConfigMap{}, err
	}
	return *o.(*v1.ConfigMap), nil
}

func (h *storeHandlers) CreateConfigMap(ctx context.Context, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	o, err := h.createObj(ctx, v1.KindConfigMap, &cfg)
	if err != nil {
		return v1.ConfigMap{}, err
	}
	return *o.(*v1.ConfigMap), nil
}

func (h *storeHandlers) ListConfigMaps(ctx context.Context, ns v1.NamespaceName) ([]v1.ConfigMap, error) {
	objs, err := h.listObj(ctx, v1.KindConfigMap, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.ConfigMap, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.ConfigMap)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	o, err := h.replaceObj(ctx, v1.KindConfigMap, ns, name, &cfg)
	if err != nil {
		return v1.ConfigMap{}, err
	}
	return *o.(*v1.ConfigMap), nil
}

func (h *storeHandlers) DeleteConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindConfigMap, ns, name)
}

// --- Secret (namespaced) ---

func (h *storeHandlers) GetSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Secret, error) {
	o, err := h.getObj(ctx, v1.KindSecret, ns, name)
	if err != nil {
		return v1.Secret{}, err
	}
	return *o.(*v1.Secret), nil
}

func (h *storeHandlers) CreateSecret(ctx context.Context, sec v1.Secret) (v1.Secret, error) {
	o, err := h.createObj(ctx, v1.KindSecret, &sec)
	if err != nil {
		return v1.Secret{}, err
	}
	return *o.(*v1.Secret), nil
}

func (h *storeHandlers) ListSecrets(ctx context.Context, ns v1.NamespaceName) ([]v1.Secret, error) {
	objs, err := h.listObj(ctx, v1.KindSecret, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Secret, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Secret)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, sec v1.Secret) (v1.Secret, error) {
	o, err := h.replaceObj(ctx, v1.KindSecret, ns, name, &sec)
	if err != nil {
		return v1.Secret{}, err
	}
	return *o.(*v1.Secret), nil
}

func (h *storeHandlers) DeleteSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindSecret, ns, name)
}

// --- Grant (namespaced) ---

func (h *storeHandlers) GetGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Grant, error) {
	o, err := h.getObj(ctx, v1.KindGrant, ns, name)
	if err != nil {
		return v1.Grant{}, err
	}
	return *o.(*v1.Grant), nil
}

func (h *storeHandlers) CreateGrant(ctx context.Context, g v1.Grant) (v1.Grant, error) {
	o, err := h.createObj(ctx, v1.KindGrant, &g)
	if err != nil {
		return v1.Grant{}, err
	}
	return *o.(*v1.Grant), nil
}

func (h *storeHandlers) ListGrants(ctx context.Context, ns v1.NamespaceName) ([]v1.Grant, error) {
	objs, err := h.listObj(ctx, v1.KindGrant, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Grant, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Grant)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, g v1.Grant) (v1.Grant, error) {
	o, err := h.replaceObj(ctx, v1.KindGrant, ns, name, &g)
	if err != nil {
		return v1.Grant{}, err
	}
	return *o.(*v1.Grant), nil
}

func (h *storeHandlers) DeleteGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindGrant, ns, name)
}

// --- KVStore (namespaced) — ADR-0072 ---

func (h *storeHandlers) GetKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.KVStore, error) {
	o, err := h.getObj(ctx, v1.KindKVStore, ns, name)
	if err != nil {
		return v1.KVStore{}, err
	}
	return *o.(*v1.KVStore), nil
}

func (h *storeHandlers) CreateKVStore(ctx context.Context, ks v1.KVStore) (v1.KVStore, error) {
	o, err := h.createObj(ctx, v1.KindKVStore, &ks)
	if err != nil {
		return v1.KVStore{}, err
	}
	return *o.(*v1.KVStore), nil
}

func (h *storeHandlers) ListKVStores(ctx context.Context, ns v1.NamespaceName) ([]v1.KVStore, error) {
	objs, err := h.listObj(ctx, v1.KindKVStore, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.KVStore, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.KVStore)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ks v1.KVStore) (v1.KVStore, error) {
	o, err := h.replaceObj(ctx, v1.KindKVStore, ns, name, &ks)
	if err != nil {
		return v1.KVStore{}, err
	}
	return *o.(*v1.KVStore), nil
}

func (h *storeHandlers) DeleteKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindKVStore, ns, name)
}

// --- Bucket (namespaced) — ADR-0080 ---

func (h *storeHandlers) GetBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Bucket, error) {
	o, err := h.getObj(ctx, v1.KindBucket, ns, name)
	if err != nil {
		return v1.Bucket{}, err
	}
	return *o.(*v1.Bucket), nil
}

func (h *storeHandlers) CreateBucket(ctx context.Context, b v1.Bucket) (v1.Bucket, error) {
	o, err := h.createObj(ctx, v1.KindBucket, &b)
	if err != nil {
		return v1.Bucket{}, err
	}
	return *o.(*v1.Bucket), nil
}

func (h *storeHandlers) ListBuckets(ctx context.Context, ns v1.NamespaceName) ([]v1.Bucket, error) {
	objs, err := h.listObj(ctx, v1.KindBucket, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Bucket, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Bucket)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, b v1.Bucket) (v1.Bucket, error) {
	o, err := h.replaceObj(ctx, v1.KindBucket, ns, name, &b)
	if err != nil {
		return v1.Bucket{}, err
	}
	return *o.(*v1.Bucket), nil
}

func (h *storeHandlers) DeleteBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindBucket, ns, name)
}

// --- CatalogService (namespaced) — ADR-0086 ---

func (h *storeHandlers) GetCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.CatalogService, error) {
	o, err := h.getObj(ctx, v1.KindCatalogService, ns, name)
	if err != nil {
		return v1.CatalogService{}, err
	}
	return *o.(*v1.CatalogService), nil
}

func (h *storeHandlers) CreateCatalogService(ctx context.Context, cs v1.CatalogService) (v1.CatalogService, error) {
	o, err := h.createObj(ctx, v1.KindCatalogService, &cs)
	if err != nil {
		return v1.CatalogService{}, err
	}
	return *o.(*v1.CatalogService), nil
}

func (h *storeHandlers) ListCatalogServices(ctx context.Context, ns v1.NamespaceName) ([]v1.CatalogService, error) {
	objs, err := h.listObj(ctx, v1.KindCatalogService, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.CatalogService, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.CatalogService)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, cs v1.CatalogService) (v1.CatalogService, error) {
	o, err := h.replaceObj(ctx, v1.KindCatalogService, ns, name, &cs)
	if err != nil {
		return v1.CatalogService{}, err
	}
	return *o.(*v1.CatalogService), nil
}

func (h *storeHandlers) DeleteCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindCatalogService, ns, name)
}

// --- Identity (namespaced) — ADR-0135, FEAT-0008/F100 ---

func (h *storeHandlers) GetIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Identity, error) {
	o, err := h.getObj(ctx, v1.KindIdentity, ns, name)
	if err != nil {
		return v1.Identity{}, err
	}
	return *o.(*v1.Identity), nil
}

func (h *storeHandlers) CreateIdentity(ctx context.Context, id v1.Identity) (v1.Identity, error) {
	o, err := h.createObj(ctx, v1.KindIdentity, &id)
	if err != nil {
		return v1.Identity{}, err
	}
	return *o.(*v1.Identity), nil
}

func (h *storeHandlers) ListIdentities(ctx context.Context, ns v1.NamespaceName) ([]v1.Identity, error) {
	objs, err := h.listObj(ctx, v1.KindIdentity, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Identity, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Identity)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, id v1.Identity) (v1.Identity, error) {
	o, err := h.replaceObj(ctx, v1.KindIdentity, ns, name, &id)
	if err != nil {
		return v1.Identity{}, err
	}
	return *o.(*v1.Identity), nil
}

func (h *storeHandlers) DeleteIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindIdentity, ns, name)
}

// --- Role + RolesAssignment (namespaced) — ADR-0136, FEAT-0008/F101 ---

func (h *storeHandlers) GetRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Role, error) {
	o, err := h.getObj(ctx, v1.KindRole, ns, name)
	if err != nil {
		return v1.Role{}, err
	}
	return *o.(*v1.Role), nil
}

func (h *storeHandlers) CreateRole(ctx context.Context, ro v1.Role) (v1.Role, error) {
	o, err := h.createObj(ctx, v1.KindRole, &ro)
	if err != nil {
		return v1.Role{}, err
	}
	return *o.(*v1.Role), nil
}

func (h *storeHandlers) ListRoles(ctx context.Context, ns v1.NamespaceName) ([]v1.Role, error) {
	objs, err := h.listObj(ctx, v1.KindRole, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Role, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Role)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ro v1.Role) (v1.Role, error) {
	o, err := h.replaceObj(ctx, v1.KindRole, ns, name, &ro)
	if err != nil {
		return v1.Role{}, err
	}
	return *o.(*v1.Role), nil
}

func (h *storeHandlers) DeleteRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindRole, ns, name)
}

func (h *storeHandlers) GetRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.RolesAssignment, error) {
	o, err := h.getObj(ctx, v1.KindRolesAssignment, ns, name)
	if err != nil {
		return v1.RolesAssignment{}, err
	}
	return *o.(*v1.RolesAssignment), nil
}

func (h *storeHandlers) CreateRolesAssignment(ctx context.Context, ra v1.RolesAssignment) (v1.RolesAssignment, error) {
	o, err := h.createObj(ctx, v1.KindRolesAssignment, &ra)
	if err != nil {
		return v1.RolesAssignment{}, err
	}
	return *o.(*v1.RolesAssignment), nil
}

func (h *storeHandlers) ListRolesAssignments(ctx context.Context, ns v1.NamespaceName) ([]v1.RolesAssignment, error) {
	objs, err := h.listObj(ctx, v1.KindRolesAssignment, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.RolesAssignment, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.RolesAssignment)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ra v1.RolesAssignment) (v1.RolesAssignment, error) {
	o, err := h.replaceObj(ctx, v1.KindRolesAssignment, ns, name, &ra)
	if err != nil {
		return v1.RolesAssignment{}, err
	}
	return *o.(*v1.RolesAssignment), nil
}

func (h *storeHandlers) DeleteRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindRolesAssignment, ns, name)
}

// --- Policy (namespaced) — ADR-0074 ---

func (h *storeHandlers) GetPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Policy, error) {
	o, err := h.getObj(ctx, v1.KindPolicy, ns, name)
	if err != nil {
		return v1.Policy{}, err
	}
	return *o.(*v1.Policy), nil
}

func (h *storeHandlers) CreatePolicy(ctx context.Context, pol v1.Policy) (v1.Policy, error) {
	o, err := h.createObj(ctx, v1.KindPolicy, &pol)
	if err != nil {
		return v1.Policy{}, err
	}
	return *o.(*v1.Policy), nil
}

func (h *storeHandlers) ListPolicies(ctx context.Context, ns v1.NamespaceName) ([]v1.Policy, error) {
	objs, err := h.listObj(ctx, v1.KindPolicy, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Policy, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Policy)
	}
	return out, nil
}

func (h *storeHandlers) ReplacePolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, pol v1.Policy) (v1.Policy, error) {
	o, err := h.replaceObj(ctx, v1.KindPolicy, ns, name, &pol)
	if err != nil {
		return v1.Policy{}, err
	}
	return *o.(*v1.Policy), nil
}

func (h *storeHandlers) DeletePolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindPolicy, ns, name)
}

// --- EgressPolicy (namespaced) ---

func (h *storeHandlers) GetEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EgressPolicy, error) {
	o, err := h.getObj(ctx, v1.KindEgressPolicy, ns, name)
	if err != nil {
		return v1.EgressPolicy{}, err
	}
	return *o.(*v1.EgressPolicy), nil
}

func (h *storeHandlers) CreateEgressPolicy(ctx context.Context, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	o, err := h.createObj(ctx, v1.KindEgressPolicy, &ep)
	if err != nil {
		return v1.EgressPolicy{}, err
	}
	return *o.(*v1.EgressPolicy), nil
}

func (h *storeHandlers) ListEgressPolicies(ctx context.Context, ns v1.NamespaceName) ([]v1.EgressPolicy, error) {
	objs, err := h.listObj(ctx, v1.KindEgressPolicy, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.EgressPolicy, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.EgressPolicy)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	o, err := h.replaceObj(ctx, v1.KindEgressPolicy, ns, name, &ep)
	if err != nil {
		return v1.EgressPolicy{}, err
	}
	return *o.(*v1.EgressPolicy), nil
}

func (h *storeHandlers) DeleteEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindEgressPolicy, ns, name)
}

// --- Invocation (namespaced) ---

func (h *storeHandlers) GetInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Invocation, error) {
	o, err := h.getObj(ctx, v1.KindInvocation, ns, name)
	if err != nil {
		return v1.Invocation{}, err
	}
	return *o.(*v1.Invocation), nil
}

func (h *storeHandlers) CreateInvocation(ctx context.Context, inv v1.Invocation) (v1.Invocation, error) {
	o, err := h.createObj(ctx, v1.KindInvocation, &inv)
	if err != nil {
		return v1.Invocation{}, err
	}
	return *o.(*v1.Invocation), nil
}

func (h *storeHandlers) ListInvocations(ctx context.Context, ns v1.NamespaceName) ([]v1.Invocation, error) {
	objs, err := h.listObj(ctx, v1.KindInvocation, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Invocation, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Invocation)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, inv v1.Invocation) (v1.Invocation, error) {
	o, err := h.replaceObj(ctx, v1.KindInvocation, ns, name, &inv)
	if err != nil {
		return v1.Invocation{}, err
	}
	return *o.(*v1.Invocation), nil
}

func (h *storeHandlers) DeleteInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindInvocation, ns, name)
}

// --- RuntimeClass (cluster-scoped) ---

func (h *storeHandlers) GetRuntimeClass(ctx context.Context, name v1.ObjectName) (v1.RuntimeClass, error) {
	o, err := h.getObj(ctx, v1.KindRuntimeClass, "", name)
	if err != nil {
		return v1.RuntimeClass{}, err
	}
	return *o.(*v1.RuntimeClass), nil
}

func (h *storeHandlers) CreateRuntimeClass(ctx context.Context, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	o, err := h.createObj(ctx, v1.KindRuntimeClass, &rc)
	if err != nil {
		return v1.RuntimeClass{}, err
	}
	return *o.(*v1.RuntimeClass), nil
}

func (h *storeHandlers) ListRuntimeClasses(ctx context.Context) ([]v1.RuntimeClass, error) {
	objs, err := h.listObj(ctx, v1.KindRuntimeClass, "")
	if err != nil {
		return nil, err
	}
	out := make([]v1.RuntimeClass, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.RuntimeClass)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceRuntimeClass(ctx context.Context, name v1.ObjectName, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	o, err := h.replaceObj(ctx, v1.KindRuntimeClass, "", name, &rc)
	if err != nil {
		return v1.RuntimeClass{}, err
	}
	return *o.(*v1.RuntimeClass), nil
}

func (h *storeHandlers) DeleteRuntimeClass(ctx context.Context, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindRuntimeClass, "", name)
}

// --- WorkerNode (cluster-scoped) ---

func (h *storeHandlers) GetWorkerNode(ctx context.Context, name v1.ObjectName) (v1.WorkerNode, error) {
	o, err := h.getObj(ctx, v1.KindWorkerNode, "", name)
	if err != nil {
		return v1.WorkerNode{}, err
	}
	return *o.(*v1.WorkerNode), nil
}

func (h *storeHandlers) CreateWorkerNode(ctx context.Context, w v1.WorkerNode) (v1.WorkerNode, error) {
	o, err := h.createObj(ctx, v1.KindWorkerNode, &w)
	if err != nil {
		return v1.WorkerNode{}, err
	}
	return *o.(*v1.WorkerNode), nil
}

func (h *storeHandlers) ListWorkerNodes(ctx context.Context) ([]v1.WorkerNode, error) {
	objs, err := h.listObj(ctx, v1.KindWorkerNode, "")
	if err != nil {
		return nil, err
	}
	out := make([]v1.WorkerNode, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.WorkerNode)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceWorkerNode(ctx context.Context, name v1.ObjectName, w v1.WorkerNode) (v1.WorkerNode, error) {
	o, err := h.replaceObj(ctx, v1.KindWorkerNode, "", name, &w)
	if err != nil {
		return v1.WorkerNode{}, err
	}
	return *o.(*v1.WorkerNode), nil
}

func (h *storeHandlers) DeleteWorkerNode(ctx context.Context, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindWorkerNode, "", name)
}

// --- Gateway (cluster-scoped) ---

func (h *storeHandlers) GetGateway(ctx context.Context, name v1.ObjectName) (v1.Gateway, error) {
	o, err := h.getObj(ctx, v1.KindGateway, "", name)
	if err != nil {
		return v1.Gateway{}, err
	}
	return *o.(*v1.Gateway), nil
}

func (h *storeHandlers) CreateGateway(ctx context.Context, gw v1.Gateway) (v1.Gateway, error) {
	o, err := h.createObj(ctx, v1.KindGateway, &gw)
	if err != nil {
		return v1.Gateway{}, err
	}
	return *o.(*v1.Gateway), nil
}

func (h *storeHandlers) ListGateways(ctx context.Context) ([]v1.Gateway, error) {
	objs, err := h.listObj(ctx, v1.KindGateway, "")
	if err != nil {
		return nil, err
	}
	out := make([]v1.Gateway, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Gateway)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceGateway(ctx context.Context, name v1.ObjectName, gw v1.Gateway) (v1.Gateway, error) {
	o, err := h.replaceObj(ctx, v1.KindGateway, "", name, &gw)
	if err != nil {
		return v1.Gateway{}, err
	}
	return *o.(*v1.Gateway), nil
}

func (h *storeHandlers) DeleteGateway(ctx context.Context, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindGateway, "", name)
}

// ---- Workflow (ADR-0094) ----

func (h *storeHandlers) GetWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Workflow, error) {
	o, err := h.getObj(ctx, v1.KindWorkflow, ns, name)
	if err != nil {
		return v1.Workflow{}, err
	}
	return *o.(*v1.Workflow), nil
}
func (h *storeHandlers) CreateWorkflow(ctx context.Context, wf v1.Workflow) (v1.Workflow, error) {
	o, err := h.createObj(ctx, v1.KindWorkflow, &wf)
	if err != nil {
		return v1.Workflow{}, err
	}
	return *o.(*v1.Workflow), nil
}
func (h *storeHandlers) ListWorkflows(ctx context.Context, ns v1.NamespaceName) ([]v1.Workflow, error) {
	objs, err := h.listObj(ctx, v1.KindWorkflow, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Workflow, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Workflow)
	}
	return out, nil
}
func (h *storeHandlers) ReplaceWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, wf v1.Workflow) (v1.Workflow, error) {
	o, err := h.replaceObj(ctx, v1.KindWorkflow, ns, name, &wf)
	if err != nil {
		return v1.Workflow{}, err
	}
	return *o.(*v1.Workflow), nil
}
func (h *storeHandlers) DeleteWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindWorkflow, ns, name)
}

// ---- WorkflowRun (ADR-0094) ----

func (h *storeHandlers) GetWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowRun, error) {
	o, err := h.getObj(ctx, v1.KindWorkflowRun, ns, name)
	if err != nil {
		return v1.WorkflowRun{}, err
	}
	return *o.(*v1.WorkflowRun), nil
}
func (h *storeHandlers) CreateWorkflowRun(ctx context.Context, run v1.WorkflowRun) (v1.WorkflowRun, error) {
	o, err := h.createObj(ctx, v1.KindWorkflowRun, &run)
	if err != nil {
		return v1.WorkflowRun{}, err
	}
	return *o.(*v1.WorkflowRun), nil
}
func (h *storeHandlers) ListWorkflowRuns(ctx context.Context, ns v1.NamespaceName) ([]v1.WorkflowRun, error) {
	objs, err := h.listObj(ctx, v1.KindWorkflowRun, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.WorkflowRun, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.WorkflowRun)
	}
	return out, nil
}
func (h *storeHandlers) ReplaceWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, run v1.WorkflowRun) (v1.WorkflowRun, error) {
	o, err := h.replaceObj(ctx, v1.KindWorkflowRun, ns, name, &run)
	if err != nil {
		return v1.WorkflowRun{}, err
	}
	return *o.(*v1.WorkflowRun), nil
}
func (h *storeHandlers) DeleteWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindWorkflowRun, ns, name)
}

// ---- Sensor (ADR-0109) ----

func (h *storeHandlers) GetSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Sensor, error) {
	o, err := h.getObj(ctx, v1.KindSensor, ns, name)
	if err != nil {
		return v1.Sensor{}, err
	}
	return *o.(*v1.Sensor), nil
}
func (h *storeHandlers) CreateSensor(ctx context.Context, se v1.Sensor) (v1.Sensor, error) {
	o, err := h.createObj(ctx, v1.KindSensor, &se)
	if err != nil {
		return v1.Sensor{}, err
	}
	return *o.(*v1.Sensor), nil
}
func (h *storeHandlers) ListSensors(ctx context.Context, ns v1.NamespaceName) ([]v1.Sensor, error) {
	objs, err := h.listObj(ctx, v1.KindSensor, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Sensor, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Sensor)
	}
	return out, nil
}
func (h *storeHandlers) ReplaceSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, se v1.Sensor) (v1.Sensor, error) {
	o, err := h.replaceObj(ctx, v1.KindSensor, ns, name, &se)
	if err != nil {
		return v1.Sensor{}, err
	}
	return *o.(*v1.Sensor), nil
}
func (h *storeHandlers) DeleteSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindSensor, ns, name)
}
