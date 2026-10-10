package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// allowAll grants every request.
type allowAll struct{}

func (allowAll) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}

// mapStore is a store.Store that keeps seeded objects as JSON, validates nothing and counts every write, which it
// refuses: a dry run must not reach one.
type mapStore struct {
	store.Store
	mu     sync.Mutex
	objs   map[v1.ObjectRef][]byte
	writes atomic.Int64
}

func newMapStore() *mapStore { return &mapStore{objs: map[v1.ObjectRef][]byte{}} }

func (s *mapStore) seed(t *testing.T, o v1.Object) {
	t.Helper()
	b, err := json.Marshal(o)
	require.NoError(t, err)
	m := o.GetObjectMeta()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[v1.ObjectRef{Kind: o.GroupVersionKind().Kind, Namespace: m.Namespace, Name: m.Name}] = b
}

func (s *mapStore) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	s.mu.Lock()
	b, ok := s.objs[v1.ObjectRef{Kind: gvk.Kind, Namespace: ns, Name: name}]
	s.mu.Unlock()
	if !ok {
		return nil, fault.NotFoundf("store.Get", "%s %q not found", gvk.Kind, name)
	}
	o, _ := v1.NewObject(gvk.Kind)
	if err := json.Unmarshal(b, o); err != nil {
		return nil, fault.Internalf("mapStore", "decode: %v", err)
	}
	return o, nil
}

func (s *mapStore) List(context.Context, v1.GroupVersionKind, store.ListOptions) (store.List, error) {
	return store.List{}, nil
}

func (s *mapStore) Create(context.Context, v1.Object) (v1.Object, error) { return nil, s.write() }
func (s *mapStore) Update(context.Context, v1.Object) (v1.Object, error) { return nil, s.write() }
func (s *mapStore) Delete(context.Context, v1.GroupVersionKind, v1.NamespaceName, v1.ObjectName, string) error {
	return s.write()
}

func (s *mapStore) write() error {
	s.writes.Add(1)
	return fault.Internalf("mapStore", "a dry run wrote to the store")
}

// fixedPlanner answers plan and counts its calls.
type fixedPlanner struct {
	plan  v1.AppPlan
	calls atomic.Int64
}

func (p *fixedPlanner) PlanApp(context.Context, *v1.App) (v1.AppPlan, error) {
	p.calls.Add(1)
	return p.plan, nil
}

// dryRunAPI serves h's operations to one identity of every right, as NewServer does, and returns the API to read its
// operations.
func dryRunAPI(h controlplane.Handlers) (http.Handler, huma.API) {
	r := chi.NewRouter()
	r.Use(middleware.Authn(middleware.NewStaticCredentials(map[string]auth.Identity{devToken: {Subject: "dev"}})))
	return r, controlplane.NewAPI(r, h)
}

// operations lists api's operations by ID.
func operations(api huma.API) map[string]*huma.Operation {
	ops := map[string]*huma.Operation{}
	for _, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Head, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				ops[op.OperationID] = op
			}
		}
	}
	return ops
}

func declaresDryRun(op *huma.Operation) bool {
	for _, p := range op.Parameters {
		if p.In == "query" && p.Name == "dryRun" {
			return true
		}
	}
	return false
}

// itemPath fills op's path for name in team-a.
func itemPath(op *huma.Operation, name string) string {
	return strings.NewReplacer("{namespace}", "team-a", "{name}", name).Replace(op.Path)
}

// sample is an object of kind k named name, empty but for the fields the wire schema requires to be names.
func sample(k v1.Kind, name v1.ObjectName) v1.Object {
	o, _ := v1.NewObject(k)
	m := o.GetObjectMeta()
	m.Name = name
	if k.Namespaced() {
		m.Namespace, m.ResourceGroup = "team-a", "rg1"
	}
	switch x := o.(type) {
	case *v1.CatalogService:
		x.Spec.Catalog = v1.CatalogRef{Bucket: "b", Prefix: "p"}
	case *v1.WorkflowRun:
		x.Spec.Workflow = "w"
	case *v1.Site:
		x.Spec.Bucket.Name, x.Spec.Prefix = "b", "p"
	case *v1.Identity:
		x.Spec.Type = v1.IdentityTypeExternal
	}
	return o
}

func encode(t *testing.T, o v1.Object) []byte {
	t.Helper()
	b, err := json.Marshal(o)
	require.NoError(t, err)
	return b
}

func problem(t *testing.T, body []byte) fault.Problem {
	t.Helper()
	var p fault.Problem
	require.NoError(t, json.Unmarshal(body, &p), "%s", body)
	return p
}

// ADR-0220 Decision 1: every write operation rejects an undeclared query parameter, and exactly the create and
// replace operations of the writable kinds declare dryRun; a read-only kind has neither. No count is fixed.
func TestDryRunOperations(t *testing.T) {
	t.Parallel()
	api := controlplane.NewAPI(chi.NewRouter(), controlplane.NewStubHandlers())
	controlplane.RegisterStubLogs(api)
	controlplane.RegisterStubWorkflowRunLogs(api)
	controlplane.RegisterStubDeadLetters(api)
	controlplane.RegisterStubAppRetry(api)
	controlplane.RegisterStubPlatformBackup(api)
	ops := operations(api)
	writes := map[string]bool{}
	for _, k := range v1.AllKinds() {
		create, replace := ops["create"+string(k)], ops["replace"+string(k)]
		require.Equal(t, create == nil, replace == nil, "%s has create and replace, or neither (read-only)", k)
		if create != nil {
			writes[create.OperationID], writes[replace.OperationID] = true, true
		}
	}
	require.NotContains(t, writes, "createRevision")
	require.NotContains(t, writes, "createAppRevision")
	require.Contains(t, writes, "createApp")
	for id, op := range ops {
		read := op.Method == http.MethodGet || op.Method == http.MethodHead
		require.Equal(t, !read, op.RejectUnknownQueryParameters, "%s %s rejects an unknown query parameter iff it writes", op.Method, id)
		require.Equal(t, writes[id], declaresDryRun(op), "%s declares dryRun iff it is a create or replace of a writable kind", id)
	}
}

// ADR-0220 Decisions 2 and 3, over every writable kind: a dry-run create and replace write nothing and answer the
// admitted object with the store-set fields as the store would set them; a taken name answers the store's 409 and an
// absent replace 404.
func TestDryRunEveryWritableKind(t *testing.T) {
	t.Parallel()
	_, api := dryRunAPI(controlplane.NewStubHandlers())
	ops := operations(api)
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, k := range v1.AllKinds() {
		create, replace := ops["create"+string(k)], ops["replace"+string(k)]
		if create == nil {
			continue
		}
		t.Run(string(k), func(t *testing.T) {
			t.Parallel()
			st := newMapStore()
			planner := &fixedPlanner{plan: v1.AppPlan{Revision: "x-1"}}
			srv, _ := dryRunAPI(controlplane.NewStoreHandlers(st, allowAll{}, admission.NewPipeline(), nil, planner))
			old := sample(k, "old")
			m := old.GetObjectMeta()
			m.UID, m.Generation, m.ResourceVersion, m.CreationTime = "uid-old", 3, "rv-7", v1.NewTimestamp(created)
			st.seed(t, old)

			fresh := sample(k, "fresh")
			fresh.GetObjectMeta().UID = "uid-client"
			rec := do(t, srv, http.MethodPost, itemPath(create, "")+"?dryRun=true", devToken, encode(t, fresh))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got, _ := v1.NewObject(k)
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), got))
			gm := got.GetObjectMeta()
			require.Equal(t, v1.ObjectName("fresh"), gm.Name)
			require.Empty(t, gm.UID, "a create answers no uid, whatever the body carried")
			require.Empty(t, gm.ResourceVersion)
			require.Zero(t, gm.Generation)
			require.True(t, time.Time(gm.CreationTime).IsZero())

			rec = do(t, srv, http.MethodPost, itemPath(create, "")+"?dryRun=true", devToken, encode(t, sample(k, "old")))
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			require.Equal(t, `store.Create: `+string(k)+` "old" already exists`, problem(t, rec.Body.Bytes()).Detail)

			rec = do(t, srv, http.MethodPut, itemPath(replace, "old")+"?dryRun=true", devToken, encode(t, sample(k, "old")))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got, _ = v1.NewObject(k)
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), got))
			gm = got.GetObjectMeta()
			require.Equal(t, v1.UID("uid-old"), gm.UID, "a replace answers the stored uid")
			require.Equal(t, "rv-7", gm.ResourceVersion)
			require.Equal(t, int64(3), gm.Generation)
			require.True(t, time.Time(gm.CreationTime).Equal(created))
			if a, ok := got.(*v1.App); ok {
				require.Equal(t, &planner.plan, a.Status.Plan, "an App answer carries the plan")
			}

			rec = do(t, srv, http.MethodPut, itemPath(replace, "absent")+"?dryRun=true", devToken, encode(t, sample(k, "absent")))
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Zero(t, st.writes.Load(), "a dry run writes nothing")
		})
	}
}

// ADR-0220 Decision 2: a taken name's dry-run create gets the real create's 409 text, from the real store.
func TestDryRunTakenNameMatchesTheStore(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "g", "rg1")).Code)
	real := do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "g", "rg1"))
	dry := do(t, srv, http.MethodPost, fnBase+"?dryRun=true", devToken, functionBody(t, "team-a", "g", "rg1"))
	require.Equal(t, http.StatusConflict, real.Code)
	require.Equal(t, real.Code, dry.Code)
	require.Equal(t, problem(t, real.Body.Bytes()).Detail, problem(t, dry.Body.Bytes()).Detail)
}

// ADR-0220 Decision 2: the KVStore guard refuses a dry-run replace of a store a live Workflow made, as the real one.
func TestDryRunKVStoreGuard(t *testing.T) {
	t.Parallel()
	srv, st := newKVServer(t)
	wf := kvWorkflow(t, st, "w-keep", v1.DeletionRetain)
	ks := keptStore(t, st, "w-keep", markerOf(wf.Name, wf.UID))
	ks.OwnerReferences, ks.ResourceVersion = nil, ""
	path := "/apis/funcd.io/v1alpha1/namespaces/team-kv/kvstores/w-keep"
	real := do(t, srv, http.MethodPut, path, "operator-token", encode(t, ks))
	dry := do(t, srv, http.MethodPut, path+"?dryRun=true", "operator-token", encode(t, ks))
	require.Equal(t, http.StatusConflict, real.Code, real.Body.String())
	require.Equal(t, real.Code, dry.Code)
	require.Equal(t, problem(t, real.Body.Bytes()).Detail, problem(t, dry.Body.Bytes()).Detail)
}

// ADR-0220 Decision 2: a dry run authorizes the real write's verb, so a caller without it gets 403 and no plan.
func TestDryRunNeedsTheWriteRight(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	planner := &fixedPlanner{}
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(), Planner: planner,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
		}),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "g", "rg1")).Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPost, fnBase+"?dryRun=true", viewToken, functionBody(t, "team-a", "f", "rg1")).Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, fnBase+"/g?dryRun=true", viewToken, functionBody(t, "team-a", "g", "rg1")).Code)
	app := encode(t, &v1.App{ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: "team-a", ResourceGroup: "rg1"}})
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/apps?dryRun=true", viewToken, app).Code)
	require.Zero(t, planner.calls.Load(), "a refused dry run plans nothing")
}

// ADR-0220 Decision 1: a write that does not declare dryRun answers 422 and writes nothing.
func TestDryRunUndeclaredOnAWrite(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "g", "rg1")).Code)
	rec := do(t, srv, http.MethodDelete, fnBase+"/g?dryRun=true", devToken, nil)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "unknown query parameter")
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, fnBase+"/g?dryRun=true", devToken, nil).Code,
		"a read still ignores an unknown parameter, and g remains")
	rec = do(t, srv, http.MethodPost, fnBase+"?dryrun=true", devToken, functionBody(t, "team-a", "f", "rg1"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a misspelt flag is refused, not ignored")
	require.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, fnBase+"/f", devToken, nil).Code)
}

// ADR-0220 Decision 5: with no planner wired, a dry-run App write answers 503 and any other kind is unaffected.
func TestDryRunWithoutPlanner(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	app := encode(t, &v1.App{ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: "team-a", ResourceGroup: "rg1"}})
	rec := do(t, srv, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/apps?dryRun=true", devToken, app)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, "controlplane.dryRun: no App planner is wired", problem(t, rec.Body.Bytes()).Detail)
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPost, fnBase+"?dryRun=true", devToken, functionBody(t, "team-a", "f", "rg1")).Code)
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/apps", devToken, app).Code,
		"a real App write needs no planner")
}
