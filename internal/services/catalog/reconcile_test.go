package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/route"
	"github.com/pyvvo/funcd/internal/secrets"
	catalogsvc "github.com/pyvvo/funcd/internal/services/catalog"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/freeport"
)

// fakeProvider is a provider.Runtime double: it records the ProviderSpec it was Converge'd with and
// the refs it was asked to Teardown, so a test can assert what the reconciler assembled WITHOUT a
// real container runtime. It NEVER creates a Function (proving the engine is not a backing Function).
type fakeProvider struct {
	converged []provider.ProviderSpec
	tornDown  []provider.ProviderRef
	status    provider.ProviderStatus
	err       error
}

func (f *fakeProvider) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	f.converged = append(f.converged, spec)
	if f.err != nil {
		return provider.ProviderStatus{}, f.err
	}
	return f.status, nil
}

func (f *fakeProvider) Teardown(_ context.Context, ref provider.ProviderRef) error {
	f.tornDown = append(f.tornDown, ref)
	return nil
}

func (f *fakeProvider) lastSpec(t *testing.T) provider.ProviderSpec {
	t.Helper()
	require.NotEmpty(t, f.converged, "Converge was never called")
	return f.converged[len(f.converged)-1]
}

// fakeSecrets is a catalogsvc.SecretResolver double resolving any named Secret to a fixed env map.
type fakeSecrets struct {
	env map[string]string
	err error
}

func (f fakeSecrets) ResolveEnv(_ context.Context, _ auth.Identity, _ v1.NamespaceName, _ []string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.env, nil
}

// mkCatalogService builds a VALID CatalogService: the catalog (lakehouse/gold) is always present as
// a spec.blob binding (CatalogService.Validate requires the catalog prefix to be a declared blob
// binding so the engine gets the per-fn S3 keypair). Extra blob bindings the caller passes are
// appended; a caller-supplied {lakehouse,gold} binding is not duplicated.
func mkCatalogService(name string, blob ...v1.FunctionBlob) *v1.CatalogService {
	cs := &v1.CatalogService{}
	cs.TypeMeta = v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cs.Spec.Catalog = v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	hasCatalogBinding := false
	for _, b := range blob {
		if b.Bucket == cs.Spec.Catalog.Bucket && b.Prefix == cs.Spec.Catalog.Prefix {
			hasCatalogBinding = true
			break
		}
	}
	if !hasCatalogBinding {
		cs.Spec.Blob = append(cs.Spec.Blob, v1.FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"})
	}
	cs.Spec.Blob = append(cs.Spec.Blob, blob...)
	return cs
}

// engineIdentity is the engine identity kept in status.Function (repointed from a backing Function):
// "<cs-name>-duckdb".
func engineIdentity(csName string) v1.ObjectName { return v1.ObjectName(csName + "-duckdb") }

// newReconciler builds a reconciler over the fake provider (+ optional deps tweaks).
func newReconciler(t *testing.T, st store.Store, prov provider.Runtime, opts func(*catalogsvc.ReconcilerDeps)) *catalogsvc.Reconciler {
	t.Helper()
	d := catalogsvc.ReconcilerDeps{
		Store:    st,
		Provider: prov,
		ImageFor: func(rt string) string { return "funcd/runtime-" + rt },
	}
	if opts != nil {
		opts(&d)
	}
	r, err := catalogsvc.NewReconciler(d)
	require.NoError(t, err)
	return r
}

func reconcileOnce(t *testing.T, r *catalogsvc.Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

// seedCatalogBucket stores the lakehouse Bucket (gold prefix) so ADR-0121's reconcile-time bucket-
// existence gate resolves and the reconciler proceeds past it to Converge. Owner is the catalog itself
// (the single writer) — owner EXISTENCE is no longer admission-checked (ADR-0121), only the prefix must be
// present for the binding to resolve.
func seedCatalogBucket(t *testing.T, st store.Store) {
	t.Helper()
	b := &v1.Bucket{}
	b.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	b.Name, b.Namespace, b.ResourceGroup = "lakehouse", "default", "rg1"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "gold", Owner: "lake"}}
	_, err := st.Create(context.Background(), b)
	require.NoError(t, err)
}

// scenario: catalogservice-uses-provider-runtime — a CatalogService reconciles into a call to the
// provider-runtime's Converge with a ProviderSpec (image=duckdb, port=8080, readiness GET / 200,
// pinned single replica, the catalog s3:// key + FUNCD_QUACK_PORT in env). NO backing Function is
// created in the store (the engine is NOT a Function).
func TestReconcile_catalogservice_uses_provider_runtime(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Derive = func(kind v1.Kind, _, name string) (string, string) {
			return "AKIA-" + string(kind) + "-" + name, "secret-" + name
		}
		d.S3Endpoint = "http://10.63.0.1:9000"
	})
	_, err := st.Create(ctx, mkCatalogService("lake",
		v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	spec := prov.lastSpec(t)
	require.Equal(t, provider.ProviderRef{Namespace: "default", Name: "lake"}, spec.Ref, "provider identity is the CatalogService (ns, name)")
	require.Equal(t, "funcd/runtime-duckdb", spec.Image, "the curated duckdb engine image")
	require.Equal(t, 8080, spec.Port, "the Quack serving port")
	require.Equal(t, provider.ReadinessProbe{Path: "/", ExpectStatus: 200}, spec.Readiness, "the Quack HTTP readiness probe (not the funcd shim's)")
	require.Equal(t, 1, spec.Replicas, "pinned single writer")
	require.Nil(t, spec.Route, "internal-only in V1 — no ingress route programmed")

	// env: the ADR-0085 keypair (derived over the provider identity) + catalog key + quack port.
	require.Equal(t, "AKIA-CatalogService-lake", spec.Env["AWS_ACCESS_KEY_ID"], "the engine key is derived over its CatalogService identity")
	require.Equal(t, "secret-lake", spec.Env["AWS_SECRET_ACCESS_KEY"])
	require.Equal(t, "us-east-1", spec.Env["AWS_REGION"])
	require.Equal(t, "http://10.63.0.1:9000", spec.Env["AWS_ENDPOINT_URL_S3"])
	require.Equal(t, "s3://lakehouse/gold/_ducklake/catalog.db", spec.Env["FUNCD_DUCKLAKE_CATALOG"])
	require.Equal(t, "8080", spec.Env["FUNCD_QUACK_PORT"])

	// NO backing Function exists — the engine is not a Function (scenario: provider-not-a-function).
	_, gerr := st.Get(ctx, v1.KindFunction.GVK(), "default", engineIdentity("lake"))
	require.Error(t, gerr, "the provider-runtime deploys the engine — NO backing Function is created")

	// status.Function is kept, repointed at the engine identity; status reflects the provider.
	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.CatalogService)
	require.Equal(t, engineIdentity("lake"), cs.Status.Function)
}

// scenario: provider-bindings-injected — spec.secrets (QUACK_TOKEN) + spec.config (DUCKDB_*) appear
// in the ProviderSpec.Env merged from the resolved Secret + ConfigMap Data.
func TestReconcile_provider_bindings_injected(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}

	// a ConfigMap carrying DUCKDB_* engine tuning.
	cm := &v1.ConfigMap{}
	cm.TypeMeta = v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}
	cm.Name, cm.Namespace, cm.ResourceGroup = "lake-engine-config", "default", "rg1"
	cm.Spec.Data = map[string]string{"DUCKDB_MEMORY_LIMIT": "3GB", "DUCKDB_THREADS": "2"}
	_, err := st.Create(ctx, cm)
	require.NoError(t, err)

	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Secrets = fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
	})
	cs := mkCatalogService("lake", v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"})
	cs.Spec.Secrets = []v1.ObjectName{"lake-quack-token"}
	cs.Spec.Config = []v1.ObjectName{"lake-engine-config"}
	_, err = st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	env := prov.lastSpec(t).Env
	require.Equal(t, "change-me", env["QUACK_TOKEN"], "the resolved Secret Data (the Quack token) is in the engine env")
	require.Equal(t, "3GB", env["DUCKDB_MEMORY_LIMIT"], "the resolved ConfigMap Data (engine tuning) is in the engine env")
	require.Equal(t, "2", env["DUCKDB_THREADS"])
}

// scenario: provider-pinned-single-writer — the assembled ProviderSpec is pinned at exactly one
// replica (no scale-to-zero).
func TestReconcile_provider_pinned_single_writer(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")
	require.Equal(t, 1, prov.lastSpec(t).Replicas, "pinned single replica (no scale-to-zero)")
}

// scenario (Ready reflection) — when the provider reports Ready, the CatalogService is Ready and its
// endpoint is the netns Address (internal-only, no ingress route).
func TestReconcile_ready_reflects_provider_status(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.CatalogService)
	require.Equal(t, v1.PhaseReady, cs.Status.Phase)
	require.Equal(t, "10.63.0.7:8080", cs.Status.Endpoint, "internal-only ⇒ endpoint is the netns Address")
	cond, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, cond.Status)
}

// ADR-0199 Decision 5: every Ready the reconciler writes (engine Ready, engine not Ready, held for a missing
// Bucket) records the generation it observed, also after a spec change.
func TestReadyObservesGeneration(t *testing.T) {
	for name, tc := range map[string]struct {
		bucket bool
		status provider.ProviderStatus
		want   v1.ConditionStatus
	}{
		"engine ready":     {bucket: true, status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, want: v1.ConditionTrue},
		"engine not ready": {bucket: true, want: v1.ConditionFalse},
		"bucket missing":   {status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, want: v1.ConditionFalse},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(storemem.New())
			if tc.bucket {
				seedCatalogBucket(t, st)
			}
			r := newReconciler(t, st, &fakeProvider{status: tc.status}, nil)
			_, err := st.Create(ctx, mkCatalogService("lake"))
			require.NoError(t, err)
			get := func() *v1.CatalogService {
				obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.CatalogService)
			}
			reconcileOnce(t, r, "lake")
			cond, ok := get().Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, int64(1), cond.ObservedGeneration)

			cs := get()
			cs.Spec.Resources.Memory = "4Gi"
			_, err = st.Update(ctx, cs)
			require.NoError(t, err)
			reconcileOnce(t, r, "lake")
			cs = get()
			require.Equal(t, int64(2), cs.Generation)
			cond, ok = cs.Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, tc.want, cond.Status)
			require.Equal(t, cs.Generation, cond.ObservedGeneration)
		})
	}
}

// scenario (ADR-0137 internal enforcement) — when a Manager is wired and the provider reports Ready,
// the reconciler Ensures a node-private catalog PEP proxy fronting the engine and publishes the PROXY
// address (bare 127.0.0.1:<port> — the function wraps it in quack://) as Status.Endpoint — internal
// functions inject the proxy, not the engine. A delete then Removes the proxy.
func TestReconcile_ready_publishes_proxy_endpoint(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.CatalogService)
	require.Equal(t, v1.PhaseReady, cs.Status.Phase)
	require.True(t, strings.HasPrefix(cs.Status.Endpoint, "127.0.0.1:"),
		"Status.Endpoint is the node-private catalog PEP proxy address (bare host:port), got %q", cs.Status.Endpoint)

	// delete → the proxy is Removed (idempotent; no assertion needed beyond no panic/error).
	require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
}

// scenario: catalog-engine-move-keeps-endpoint — the engine comes back on a new netns IP (a crashed
// instance re-converged): the reconciler re-Ensures the proxy with the new upstream, and Status.Endpoint,
// the URL consumers were injected with, stays the same: a running worker keeps the env it started with.
func TestReconcile_engine_move_keeps_proxy_endpoint(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)
	endpoint := func() string {
		obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.CatalogService).Status.Endpoint
	}

	reconcileOnce(t, r, "lake")
	before := endpoint()
	require.NotEmpty(t, before)

	prov.status.Address = "10.63.0.9:8080"
	reconcileOnce(t, r, "lake")
	require.Equal(t, before, endpoint(), "the engine moved, but the endpoint consumers hold is unchanged")
}

// scenario: crashed-catalog-engine-restarts (ADR-0142) — a Ready catalog asks to run again after the supervision
// period, with no write; that pass converges the engine the provider recreates after a crash — first starting, then
// Ready on a new address — and the endpoint consumers were injected with stays the same.
func TestScenarioCrashedCatalogEngineRestarts(t *testing.T) {
	const period = 50 * time.Millisecond
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.SupervisionPeriod = period
	})
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"}
	get := func() *v1.CatalogService {
		obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.CatalogService)
	}

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, period, res.RequeueAfter, "a Ready catalog comes back after the supervision period")
	proxyURL := get().Status.Endpoint

	prov.status = provider.ProviderStatus{Running: 1, Ready: false, Address: "10.63.0.9:8080"}
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhasePending, get().Status.Phase, "the recreated engine is still starting")
	require.Equal(t, period, res.RequeueAfter, "the engine wait is min(enginePollInterval, supervisionPeriod) (ADR-0163 Decision 7)")

	prov.status.Ready = true
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, prov.converged, 3, "every pass converged the engine")
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
	require.Equal(t, period, res.RequeueAfter)
}

// TestIssue104_ConvergeErrorMarksCatalogNotReady: a Ready catalog whose engine cannot be recreated (Converge fails)
// is reported not Ready, drops the endpoint of the dead engine and retracts its edge route, while the pass still
// fails so the controller backs off; once Converge succeeds it is Ready again on the same proxy URL.
func TestIssue104_ConvergeErrorMarksCatalogNotReady(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	routes := newRecordingRoutes()
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.Routes = routes
	})
	cs := mkCatalogService("lake")
	cs.Spec.Ingress = &v1.CatalogIngress{PathPrefix: "/catalog/lake"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"}
	get := func() *v1.CatalogService {
		obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.CatalogService)
	}

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	proxyURL := get().Status.Endpoint
	require.Len(t, routes.get(lakeRouteSource), 1)

	prov.err = errors.New("create engine replica 0: pull image: not found")
	_, err = r.Reconcile(ctx, req)
	require.Error(t, err, "a failed converge still fails the pass, so the controller backs off")
	got := get()
	require.Equal(t, v1.PhasePending, got.Status.Phase, "an engine that cannot be recreated is not Ready")
	cond, ok := got.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "EngineConvergeFailed", cond.Reason)
	require.NotEqual(t, proxyURL, got.Status.Endpoint, "the endpoint of the dead engine is no longer published")
	require.Empty(t, routes.get(lakeRouteSource), "the edge route is retracted while the engine is down")

	prov.err = nil
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
}

// TestIssue372_NotReadyGateStopsServing: a Ready catalog that loses its Bucket or a bound Secret stops its engine,
// unpublishes its endpoint and stops forwarding through the proxy consumers were injected with; once the binding is
// back it is Ready again on the same proxy URL (#59).
func TestIssue372_NotReadyGateStopsServing(t *testing.T) {
	cases := []struct {
		name   string
		unbind func(t *testing.T, st store.Store, secrets *fakeSecrets)
		rebind func(t *testing.T, st store.Store, secrets *fakeSecrets)
	}{
		{
			name: "bucket-deleted",
			unbind: func(t *testing.T, st store.Store, _ *fakeSecrets) {
				require.NoError(t, st.Delete(context.Background(), v1.KindBucket.GVK(), "default", "lakehouse", ""))
			},
			rebind: func(t *testing.T, st store.Store, _ *fakeSecrets) { seedCatalogBucket(t, st) },
		},
		{
			name: "secret-deleted",
			unbind: func(_ *testing.T, _ store.Store, secrets *fakeSecrets) {
				secrets.err = errors.New(`secret "quack" not found`)
			},
			rebind: func(_ *testing.T, _ store.Store, secrets *fakeSecrets) { secrets.err = nil },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var engineHits atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				engineHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(engine.Close)
			st := store.New(storemem.New())
			seedCatalogBucket(t, st)
			secrets := &fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
			mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
			t.Cleanup(mgr.Shutdown)
			prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(engine.URL, "http://")}}
			r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
				d.Secrets = secrets
				d.Proxy = mgr
			})
			cs := mkCatalogService("lake")
			cs.Spec.Secrets = []v1.ObjectName{"quack"}
			_, err := st.Create(ctx, cs)
			require.NoError(t, err)
			get := func() *v1.CatalogService {
				obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.CatalogService)
			}
			query := func(addr string) int {
				resp, qerr := http.Get("http://" + addr + "/")
				require.NoError(t, qerr)
				require.NoError(t, resp.Body.Close())
				return resp.StatusCode
			}

			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			proxyURL := get().Status.Endpoint
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(1), engineHits.Load())

			tc.unbind(t, st, secrets)
			reconcileOnce(t, r, "lake")
			got := get()
			require.Equal(t, v1.PhasePending, got.Status.Phase)
			require.Empty(t, got.Status.Endpoint, "a not-Ready catalog publishes no endpoint")
			require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown, "the engine is stopped")
			require.Equal(t, http.StatusServiceUnavailable, query(proxyURL), "the proxy consumers hold no longer forwards")
			require.Equal(t, int32(1), engineHits.Load(), "no query reached the engine")

			tc.rebind(t, st, secrets)
			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(2), engineHits.Load())
		})
	}
}

// scenario: provider-torn-down (delete path) — a deleted (absent) CatalogService tears the engine
// down via the provider-runtime; it is idempotent (a missing engine is fine).
func TestReconcile_delete_tears_down_provider(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)
	reconcileOnce(t, r, "lake")

	// delete the CatalogService, then reconcile — the engine is torn down.
	require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
	require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown,
		"a deleted CatalogService tears the engine down via the provider-runtime")

	// a second delete-reconcile is a no-op-safe (idempotent — Teardown called again, no error).
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.tornDown, 2)
}

// scenario (fail-closed) — a CatalogService declaring spec.secrets with NO resolver wired holds the
// service not-Ready (BindingResolveFailed) and does NOT converge the engine.
func TestReconcile_secrets_without_resolver_fails_closed(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil) // no Secrets resolver
	cs := mkCatalogService("lake")
	cs.Spec.Secrets = []v1.ObjectName{"lake-quack-token"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	require.Empty(t, prov.converged, "no engine is converged when a declared secret can't be resolved")
	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	got := csObj.(*v1.CatalogService)
	require.Equal(t, v1.PhasePending, got.Status.Phase)
	cond, ok := got.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BindingResolveFailed", cond.Reason)
}

// scenario: catalog-waits-for-bucket (ADR-0121) — a CatalogService binding a not-yet-applied Bucket is
// ADMITTED and held not-Ready (BucketNotFound), converging its engine only once the Bucket exists. This is
// the reconcile-time replacement for the removed catalog-blob-validity admission (no synchronous reject).
func TestReconcile_waits_for_bucket(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New()) // deliberately NOT seeding the bucket — the gate must catch its absence
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")
	require.Empty(t, prov.converged, "no engine converges while the bound Bucket is absent")
	csObj, err := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.CatalogService)
	require.Equal(t, v1.PhasePending, cs.Status.Phase)
	cond, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BucketNotFound", cond.Reason)

	// apply the Bucket → the next reconcile resolves the gate and converges the engine (convergence).
	seedCatalogBucket(t, st)
	reconcileOnce(t, r, "lake")
	require.NotEmpty(t, prov.converged, "once the Bucket exists the engine converges")
}

// A CatalogService applied before the ConfigMap or Secret it binds is held not-Ready and requeued, so its engine
// converges once the binding exists: no ConfigMap or Secret event reconciles the CatalogService again (issue #77).
func TestIssue77_MissingBindingRecoversWhenApplied(t *testing.T) {
	t.Parallel()
	for _, kind := range []v1.Kind{v1.KindConfigMap, v1.KindSecret} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := store.New(storemem.New())
			seedCatalogBucket(t, st)
			prov := &fakeProvider{}
			sr, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
			require.NoError(t, err)
			r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) { d.Secrets = sr })
			cs := mkCatalogService("lake")
			if kind == v1.KindConfigMap {
				cs.Spec.Config = []v1.ObjectName{"late"}
			} else {
				cs.Spec.Secrets = []v1.ObjectName{"late"}
			}
			_, err = st.Create(ctx, cs)
			require.NoError(t, err)

			res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"})
			require.NoError(t, err)
			require.Empty(t, prov.converged, "no engine converges while a bound %s is absent", kind)
			require.Positive(t, res.RequeueAfter, "a missing binding requeues the CatalogService")

			binding, ok := v1.NewObject(kind)
			require.True(t, ok)
			meta := binding.GetObjectMeta()
			meta.Name, meta.Namespace, meta.ResourceGroup = "late", "default", "rg1"
			_, err = st.Create(ctx, binding)
			require.NoError(t, err)

			reconcileOnce(t, r, "lake")
			require.NotEmpty(t, prov.converged, "the engine converges once its binding exists")
		})
	}
}

// DEFERRED node-gated scenarios (ADR-0086/0087): the live-DuckDB scenarios need the native DuckDB
// image (a separate process) on real containerd, so they run on the homebox/Lima FUNCD_IT=1 lane
// (the ADR-0080 s3gateway precedent), NOT this in-process suite:
//   - query-over-quack              — a Quack SELECT reads bound Parquet via the F47 S3 surface.
//   - write-creates-ducklake-snapshot — an INSERT writes Parquet + a DuckLake catalog snapshot on blob.
//   - tenant-isolation              — A's endpoint reading B's bucket → 403 (the F47 keypair, cryptographic).
//   - arbitrary-url-confined        — a non-funcd URL is refused by the shim lockdown.
//   - catalog-persists-across-restart — the catalog (loaded from blob) survives a replica restart.

// edgeHarness is a store, a real edge aggregator and router, the Route reconciler and a catalog reconciler with
// a Ready engine behind a real PEP proxy; NotifyFunc records owners so run can re-run them as the controller does.
type edgeHarness struct {
	st       store.Store
	rtr      router.Router
	prov     *fakeProvider
	routes   *route.Reconciler
	catalogs *catalogsvc.Reconciler
	notified []router.Owner
}

func newEdgeHarness(t *testing.T, modes router.ModeFunc) *edgeHarness {
	t.Helper()
	h := &edgeHarness{st: store.New(storemem.New()), rtr: router.New()}
	agg := router.NewAggregator(h.rtr, modes, func(o router.Owner) { h.notified = append(h.notified, o) }, nil)
	var err error
	h.routes, err = route.NewReconciler(route.Deps{Store: h.st, Routes: agg})
	require.NoError(t, err)
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, h.st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	h.prov = &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	h.catalogs = newReconciler(t, h.st, h.prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.Routes = agg
	})
	seedCatalogBucket(t, h.st)
	return h
}

// run reconciles req, then every owner the aggregator asked to re-run, until none is left.
func (h *edgeHarness) run(t *testing.T, req controller.Request) {
	t.Helper()
	queue := []controller.Request{req}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		var err error
		if r.GVK == v1.KindRoute.GVK() {
			_, err = h.routes.Reconcile(context.Background(), r)
		} else {
			_, err = h.catalogs.Reconcile(context.Background(), r)
		}
		require.NoError(t, err)
		for _, o := range h.notified {
			queue = append(queue, controller.Request{GVK: o.Kind.GVK(), Namespace: o.Namespace, Name: o.Name})
		}
		h.notified = nil
	}
}

func (h *edgeHarness) condition(t *testing.T, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, ct v1.ConditionType) v1.Condition {
	t.Helper()
	obj, err := h.st.Get(context.Background(), gvk, ns, name)
	require.NoError(t, err)
	var conds v1.Conditions
	switch o := obj.(type) {
	case *v1.Route:
		conds = o.Status.Conditions
	case *v1.CatalogService:
		conds = o.Status.Conditions
	}
	c, ok := conds.Get(ct)
	require.True(t, ok, "%s %s/%s has %s", gvk.Kind, ns, name, ct)
	return c
}

// TestScenario_catalog_loses_to_earlier_route covers scenario: catalog-loses-to-earlier-route — Route a/r and
// CatalogService default/lake on (h, /q), reconciled in either order: the Route serves the claim and is Ready, the
// catalog reports IngressReady=False (RouteConflict naming Route a/r) and stays Ready.
func TestScenario_catalog_loses_to_earlier_route(t *testing.T) {
	routeReq := controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "a", Name: "r"}
	catalogReq := controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"}
	for name, order := range map[string][]controller.Request{
		"route-first":   {routeReq, catalogReq},
		"catalog-first": {catalogReq, routeReq},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := newEdgeHarness(t, nil)
			fn := &v1.Function{}
			fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
			fn.Name, fn.Namespace, fn.ResourceGroup = "fn", "a", "rg1"
			fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
			_, err := h.st.Create(ctx, fn)
			require.NoError(t, err)
			rt := &v1.Route{}
			rt.TypeMeta = v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}
			rt.Name, rt.Namespace, rt.ResourceGroup = "r", "a", "rg1"
			rt.Spec = v1.RouteSpec{Host: "h", Rules: []v1.RouteRule{{Path: "/q", Backend: v1.RouteBackend{Function: "fn"}}}}
			_, err = h.st.Create(ctx, rt)
			require.NoError(t, err)
			cs := mkCatalogService("lake")
			cs.Spec.Ingress = &v1.CatalogIngress{Host: "h", PathPrefix: "/q"}
			_, err = h.st.Create(ctx, cs)
			require.NoError(t, err)

			for _, req := range order {
				h.run(t, req)
			}

			m, ok := h.rtr.Resolve("h", "/q", "GET")
			require.True(t, ok)
			require.Equal(t, v1.NamespaceName("a"), m.Namespace)
			require.Equal(t, v1.ObjectName("fn"), m.Function, "/q on h reaches a/r's Function")
			require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindRoute.GVK(), "a", "r", "Ready").Status)
			ing := h.condition(t, v1.KindCatalogService.GVK(), "default", "lake", "IngressReady")
			require.Equal(t, v1.ConditionFalse, ing.Status)
			require.Equal(t, "RouteConflict", ing.Reason)
			require.Contains(t, ing.Message, "Route a/r")
			require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindCatalogService.GVK(), "default", "lake", "Ready").Status)
		})
	}
}

// TestScenario_catalog_host_required_explicit covers scenario: catalog-host-required-explicit — a host-less
// catalog ingress in an explicit-mode namespace gets no edge entry and IngressReady=False (HostRequired), while
// the catalog stays Ready and its engine keeps serving the functions bound to it.
func TestScenario_catalog_host_required_explicit(t *testing.T) {
	ctx := context.Background()
	h := newEdgeHarness(t, func(context.Context, v1.NamespaceName) (v1.ExposureMode, error) {
		return v1.ExposureExplicit, nil
	})
	h.run(t, controller.Request{GVK: v1.KindRoute.GVK()})
	cs := mkCatalogService("lake")
	cs.Spec.Ingress = &v1.CatalogIngress{PathPrefix: "/q"}
	_, err := h.st.Create(ctx, cs)
	require.NoError(t, err)

	h.run(t, controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"})

	_, ok := h.rtr.Resolve("any", "/q", "GET")
	require.False(t, ok, "no edge entry exists for the catalog")
	ing := h.condition(t, v1.KindCatalogService.GVK(), "default", "lake", "IngressReady")
	require.Equal(t, v1.ConditionFalse, ing.Status)
	require.Equal(t, "HostRequired", ing.Reason)
	require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindCatalogService.GVK(), "default", "lake", "Ready").Status)
	obj, err := h.st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
	require.NoError(t, err)
	endpoint := obj.(*v1.CatalogService).Status.Endpoint
	require.NotEmpty(t, endpoint, "spec.catalogs consumers still get the proxy endpoint")
	require.NotEqual(t, "10.63.0.7:8080", endpoint, "the endpoint is the PEP proxy, not the engine")
	require.Empty(t, h.prov.tornDown, "the engine is not torn down")
}

// storeFunction stores Function name in ns default with change applied; processed marks its spec processed.
func storeFunction(t *testing.T, st store.Store, name string, processed bool, change func(*v1.Function)) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "handle"
	fn.Spec.Image = "file:///handler.mjs"
	if processed {
		fn.Status.ObservedGeneration = 1
	}
	if change != nil {
		change(fn)
	}
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func bindsLake(fn *v1.Function) {
	fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
}

// TestReconcileRecordsProxyPort: every pass with the proxy wired records the listener's port before its branch writes
// the status, and a new run binds that port again. Not parallel, nor its subtests: a port bound in parallel could take
// the recorded port between one run's Shutdown and the next run's rebind. The first run's listener is bound on a
// freeport port before the pass, as another test process's :0 bind could take an ephemeral one in between (#758).
func TestReconcileRecordsProxyPort(t *testing.T) {
	cases := map[string]struct {
		status provider.ProviderStatus
		bucket bool
		phase  v1.Phase
	}{
		"ready":            {status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, bucket: true, phase: v1.PhaseReady},
		"engine-not-ready": {status: provider.ProviderStatus{Running: 1, Address: "10.63.0.7:8080"}, bucket: true, phase: v1.PhasePending},
		"hold-not-ready":   {phase: v1.PhasePending},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(storemem.New())
			if tc.bucket {
				seedCatalogBucket(t, st)
			}
			_, err := st.Create(ctx, mkCatalogService("lake"))
			require.NoError(t, err)
			port := freeport.Port(t)
			for run := range 2 {
				mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
				if run == 0 {
					_, _, err = mgr.Listen("default", "lake", port)
					require.NoError(t, err)
				}
				r := newReconciler(t, st, &fakeProvider{status: tc.status}, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
				reconcileOnce(t, r, "lake")
				obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
				require.NoError(t, gerr)
				cs := obj.(*v1.CatalogService)
				require.Equal(t, tc.phase, cs.Status.Phase)
				require.Equal(t, port, cs.Status.ProxyPort, "the pass records the listener's port; a new run binds it again")
				url, ok := mgr.ProxyURL("default", "lake")
				require.True(t, ok)
				require.Equal(t, "127.0.0.1:"+strconv.Itoa(cs.Status.ProxyPort), url)
				mgr.Shutdown()
			}
		})
	}
}

// failingFunctionList fails every List of Functions.
type failingFunctionList struct{ store.Store }

func (s failingFunctionList) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	if gvk == v1.KindFunction.GVK() {
		return store.List{}, errors.New("list failed")
	}
	return s.Store.List(ctx, gvk, opts)
}

// TestReconcileDeleteKeepsBoundListener: a deleted catalog's listener is released, not closed, while a Function binds
// the catalog or the bound-Functions check fails; it closes once none binds it.
func TestReconcileDeleteKeepsBoundListener(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedCatalogBucket(t, st)
	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkCatalogService("lake"))
	require.NoError(t, err)
	storeFunction(t, st, "reader", true, bindsLake)
	reconcileOnce(t, r, "lake")
	url, ok := mgr.ProxyURL("default", "lake")
	require.True(t, ok)

	require.NoError(t, st.Delete(ctx, v1.KindCatalogService.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
	got, ok := mgr.ProxyURL("default", "lake")
	require.True(t, ok, "reader binds lake, so its listener stays")
	require.Equal(t, url, got)
	require.Equal(t, []v1.ObjectName{"lake"}, mgr.Released("default"))
	require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown, "the engine is torn down")

	require.NoError(t, st.Delete(ctx, v1.KindFunction.GVK(), "default", "reader", ""))
	failing := newReconciler(t, failingFunctionList{st}, prov, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err = failing.Reconcile(ctx, controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"})
	require.Error(t, err, "a failed bound-Functions check fails the pass")
	_, ok = mgr.ProxyURL("default", "lake")
	require.True(t, ok, "and keeps the listener")

	reconcileOnce(t, r, "lake")
	_, ok = mgr.ProxyURL("default", "lake")
	require.False(t, ok, "no Function binds lake, so its listener closes")
}

// TestAnyFunctionBindsCountsSwitchingFunction: a Function binds a catalog it names, and every catalog of its
// namespace while a worker of an earlier spec may run.
func TestAnyFunctionBindsCountsSwitchingFunction(t *testing.T) {
	cases := map[string]struct {
		processed bool
		change    func(*v1.Function)
		want      bool
	}{
		"steady":      {processed: true, change: func(fn *v1.Function) { fn.Status.CurrentRevision, fn.Status.ServingRevision = "r1", "r1" }},
		"other":       {processed: true, change: func(fn *v1.Function) { fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "sea", Catalog: "sea"}} }},
		"binds":       {processed: true, change: bindsLake, want: true},
		"unprocessed": {want: true},
		"switching":   {processed: true, change: func(fn *v1.Function) { fn.Status.CurrentRevision, fn.Status.ServingRevision = "r2", "r1" }, want: true},
		"draining":    {processed: true, change: func(fn *v1.Function) { fn.Status.DrainingRevision = "r1" }, want: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := store.New(storemem.New())
			storeFunction(t, st, "fn", tc.processed, tc.change)
			binds, err := catalogsvc.AnyFunctionBinds(context.Background(), st, "default", "lake")
			require.NoError(t, err)
			require.Equal(t, tc.want, binds)
			binds, err = catalogsvc.AnyFunctionBinds(context.Background(), st, "ops", "lake")
			require.NoError(t, err)
			require.False(t, binds, "only Functions of the catalog's namespace count")
		})
	}
}

// TestMapFunctionMapsReleasedListeners: a Function event maps to the released listeners of its namespace only.
func TestMapFunctionMapsReleasedListeners(t *testing.T) {
	st := store.New(storemem.New())
	fn := &v1.Function{}
	fn.Name, fn.Namespace = "reader", "default"
	require.Nil(t, newReconciler(t, st, &fakeProvider{}, nil).MapFunction(context.Background(), fn), "no proxy wired maps nothing")

	mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	for _, ref := range []struct {
		ns   v1.NamespaceName
		name v1.ObjectName
	}{{"default", "lake"}, {"default", "kept"}, {"ops", "lake"}} {
		_, _, err := mgr.Listen(ref.ns, ref.name, 0)
		require.NoError(t, err)
	}
	mgr.Release("default", "lake")
	mgr.Release("ops", "lake")
	r := newReconciler(t, st, &fakeProvider{}, func(d *catalogsvc.ReconcilerDeps) { d.Proxy = mgr })
	require.Equal(t, []controller.Request{{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"}},
		r.MapFunction(context.Background(), fn))
}

// TestIssue716_NotReadyEngineStopsServing: a pass that finds the engine not Ready, whether it moved and is not
// probed Ready yet or Converge failed, suspends the proxy consumers hold, so it forwards nothing (and no engine
// token) to the previous engine until a Ready pass retargets it on the same URL.
func TestIssue716_NotReadyEngineStopsServing(t *testing.T) {
	cases := []struct {
		name     string
		notReady func(prov *fakeProvider)
		wantErr  bool
	}{
		{
			name: "engine-moved",
			notReady: func(prov *fakeProvider) {
				prov.status = provider.ProviderStatus{Running: 1, Ready: false, Address: "10.63.0.250:8080"}
			},
		},
		{
			name:     "converge-failed",
			notReady: func(prov *fakeProvider) { prov.err = errors.New("create engine replica 0: pull image: not found") },
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var engineHits atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				engineHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(engine.Close)
			engineReady := provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(engine.URL, "http://")}
			st := store.New(storemem.New())
			seedCatalogBucket(t, st)
			mgr := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(nil, st), nil, nil)
			t.Cleanup(mgr.Shutdown)
			prov := &fakeProvider{status: engineReady}
			r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
				d.Secrets = &fakeSecrets{env: map[string]string{"QUACK_TOKEN": "shared-engine-token"}}
				d.Proxy = mgr
			})
			cs := mkCatalogService("lake")
			cs.Spec.Secrets = []v1.ObjectName{"quack"}
			_, err := st.Create(ctx, cs)
			require.NoError(t, err)
			req := controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"}
			get := func() *v1.CatalogService {
				obj, gerr := st.Get(ctx, v1.KindCatalogService.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.CatalogService)
			}
			query := func(addr string) int {
				resp, qerr := http.Get("http://" + addr + "/")
				require.NoError(t, qerr)
				require.NoError(t, resp.Body.Close())
				return resp.StatusCode
			}

			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			proxyURL := get().Status.Endpoint
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(1), engineHits.Load())

			tc.notReady(prov)
			_, err = r.Reconcile(ctx, req)
			require.Equal(t, tc.wantErr, err != nil, "reconcile error: %v", err)
			require.Equal(t, v1.PhasePending, get().Status.Phase)
			require.Equal(t, http.StatusServiceUnavailable, query(proxyURL), "the proxy consumers hold no longer forwards")
			require.Equal(t, int32(1), engineHits.Load(), "no query reached the previous engine")

			prov.status, prov.err = engineReady, nil
			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(2), engineHits.Load())
		})
	}
}
