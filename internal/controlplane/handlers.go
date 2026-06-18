// Package controlplane: the store-backed Handlers implementation (ADR-0018) that fills
// ADR-0005's seam. Each of the 15 kinds' CRUD operations runs the same path —
// authorize (PDP) → admit (validate, writes only) → store — through six non-generic
// v1.Object helpers; the per-kind methods are thin typed wrappers. No generics, no any.
package controlplane

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// storeHandlers is the real, store-backed Handlers: authenticated (via the authn
// middleware that populates ctx), namespace-authorized (the PDP), and admission-validated.
type storeHandlers struct {
	store store.Store
	authz auth.Authorizer
}

// NewStoreHandlers builds the store-backed control-plane Handlers (ADR-0018).
func NewStoreHandlers(st store.Store, authz auth.Authorizer) Handlers {
	return &storeHandlers{store: st, authz: authz}
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
	stampTypeMeta(obj, kind)               // the route's kind owns TypeMeta (k8s-style)
	if err := obj.Validate(); err != nil { // admission: envelope + resourceGroup-required (ADR-0003)
		return nil, err
	}
	return h.store.Create(ctx, obj)
}

// stampTypeMeta sets the object's apiVersion/kind from the route's kind. The control-plane
// endpoint determines the kind, not the request body — and the ",inline" TypeMeta does not
// round-trip through huma's generated request schema (ADR-0005), so the server normalizes it.
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
	case *v1.Config:
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
	}
}

func (h *storeHandlers) replaceObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, obj v1.Object) (v1.Object, error) {
	if err := h.authorize(ctx, auth.VerbUpdate, kind, ns); err != nil {
		return nil, err
	}
	meta := obj.GetObjectMeta()
	if meta.Namespace != ns {
		return nil, fault.Invalidf("controlplane.admit", "body namespace %q does not match path %q", meta.Namespace, ns)
	}
	if meta.Name != name {
		return nil, fault.Invalidf("controlplane.admit", "body name %q does not match path %q", meta.Name, name)
	}
	stampTypeMeta(obj, kind) // route's kind owns TypeMeta (see createObj)
	if err := obj.Validate(); err != nil {
		return nil, err
	}
	cur, err := h.store.Get(ctx, kind.GVK(), ns, name)
	if err != nil {
		return nil, err
	}
	meta.ResourceVersion = cur.GetObjectMeta().ResourceVersion // read-RV-then-update (ADR-0018 workaround)
	return h.store.Update(ctx, obj)
}

func (h *storeHandlers) deleteObj(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error {
	if err := h.authorize(ctx, auth.VerbDelete, kind, ns); err != nil {
		return err
	}
	return h.store.Delete(ctx, kind.GVK(), ns, name, "")
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

func (h *storeHandlers) DeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
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

// --- Config (namespaced) ---

func (h *storeHandlers) GetConfig(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Config, error) {
	o, err := h.getObj(ctx, v1.KindConfig, ns, name)
	if err != nil {
		return v1.Config{}, err
	}
	return *o.(*v1.Config), nil
}

func (h *storeHandlers) CreateConfig(ctx context.Context, cfg v1.Config) (v1.Config, error) {
	o, err := h.createObj(ctx, v1.KindConfig, &cfg)
	if err != nil {
		return v1.Config{}, err
	}
	return *o.(*v1.Config), nil
}

func (h *storeHandlers) ListConfigs(ctx context.Context, ns v1.NamespaceName) ([]v1.Config, error) {
	objs, err := h.listObj(ctx, v1.KindConfig, ns)
	if err != nil {
		return nil, err
	}
	out := make([]v1.Config, len(objs))
	for i, o := range objs {
		out[i] = *o.(*v1.Config)
	}
	return out, nil
}

func (h *storeHandlers) ReplaceConfig(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, cfg v1.Config) (v1.Config, error) {
	o, err := h.replaceObj(ctx, v1.KindConfig, ns, name, &cfg)
	if err != nil {
		return v1.Config{}, err
	}
	return *o.(*v1.Config), nil
}

func (h *storeHandlers) DeleteConfig(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	return h.deleteObj(ctx, v1.KindConfig, ns, name)
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
