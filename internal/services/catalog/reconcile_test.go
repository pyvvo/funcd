package catalog_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/provider"
	catalogsvc "github.com/green-0-rabbit/funcd/internal/services/catalog"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// fakeProvider is a provider.Runtime double: it records the ProviderSpec it was Converge'd with and
// the refs it was asked to Teardown, so a test can assert what the reconciler assembled WITHOUT a
// real container runtime. It NEVER creates a Function (proving the engine is not a backing Function).
type fakeProvider struct {
	converged []provider.ProviderSpec
	tornDown  []provider.ProviderRef
	status    provider.ProviderStatus
}

func (f *fakeProvider) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	f.converged = append(f.converged, spec)
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

// scenario: catalogservice-uses-provider-runtime — a CatalogService reconciles into a call to the
// provider-runtime's Converge with a ProviderSpec (image=duckdb, port=8080, readiness GET / 200,
// pinned single replica, the catalog s3:// key + FUNCD_QUACK_PORT in env). NO backing Function is
// created in the store (the engine is NOT a Function).
func TestReconcile_catalogservice_uses_provider_runtime(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, func(d *catalogsvc.ReconcilerDeps) {
		d.Derive = func(ns, name string) (string, string) { return "AKIA-" + name, "secret-" + name }
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
	require.Equal(t, "AKIA-lake", spec.Env["AWS_ACCESS_KEY_ID"])
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

// scenario: provider-torn-down (delete path) — a deleted (absent) CatalogService tears the engine
// down via the provider-runtime; it is idempotent (a missing engine is fine).
func TestReconcile_delete_tears_down_provider(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
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

// DEFERRED node-gated scenarios (ADR-0086/0087): the live-DuckDB scenarios need the native DuckDB
// image (a separate process) on real containerd, so they run on the homebox/Lima FUNCD_IT=1 lane
// (the ADR-0080 s3gateway precedent), NOT this in-process suite:
//   - query-over-quack              — a Quack SELECT reads bound Parquet via the F47 S3 surface.
//   - write-creates-ducklake-snapshot — an INSERT writes Parquet + a DuckLake catalog snapshot on blob.
//   - tenant-isolation              — A's endpoint reading B's bucket → 403 (the F47 keypair, cryptographic).
//   - arbitrary-url-confined        — a non-funcd URL is refused by the shim lockdown.
//   - catalog-persists-across-restart — the catalog (loaded from blob) survives a replica restart.
