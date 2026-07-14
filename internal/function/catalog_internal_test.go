package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	cataloggw "github.com/green-0-rabbit/funcd/internal/catalog/gateway"
)

// seedCatalogService stores a CatalogService in the reconciler's memory store with the given
// published endpoint + spec.secrets, so resolveCatalogEnv can Get it. endpoint == "" models a
// not-Ready catalog (no status.endpoint published yet).
func seedCatalogService(t *testing.T, r *Reconciler, name, endpoint string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindCatalogService)
	cs := obj.(*v1.CatalogService)
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	// A Validate-passing shape: the catalog (bucket, prefix) must be one of spec.blob bindings.
	cs.Spec.Blob = []v1.FunctionBlob{{Alias: "lakehouse", Bucket: "lakehouse", Prefix: "gold"}}
	cs.Spec.Catalog = v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}
	cs.Spec.Secrets = []v1.ObjectName{"quack-token"} // names the Secret carrying QUACK_TOKEN
	cs.Status.Endpoint = endpoint
	_, err := r.store.Create(context.Background(), cs)
	require.NoError(t, err)
}

// catalogConsumerFn is a Function that consumes one catalog under a given alias.
func catalogConsumerFn(alias, catalog string) *v1.Function {
	fn := sampleFn()
	fn.Name = "reader"
	fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: alias, Catalog: v1.ObjectName(catalog)}}
	return fn
}

// scenario: binding-injects-endpoint-and-token — a Ready CatalogService (status.endpoint set, now the
// node-private catalog PEP proxy URL, ADR-0137) makes the worker env carry FUNCD_CATALOG_LAKE_URL AND
// _TOKEN, both PRESENT and NON-EMPTY. The _TOKEN is the per-function MAC bearer (ADR-0137), NOT the
// shared QUACK_TOKEN: it round-trips via CatalogKeys.PrincipalFor to THIS Function principal. This also
// guards the M1 regression: routing the FUNCD_-prefixed keys through mergeSecretEnv would drop them.
func TestScenarioBindingInjectsEndpointAndToken(t *testing.T) {
	t.Parallel()
	master := []byte("catalog-inject-test-node-master")
	// The secret resolver still carries QUACK_TOKEN, but catalog injection no longer reads it — the
	// shared token stays with the proxy; the function gets a per-function derived token instead.
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken-abc"}})
	r.catalogMaster = master
	const proxyURL = "http://127.0.0.1:54321" // the node-private proxy URL the reconciler publishes
	seedCatalogService(t, r, "lake", proxyURL)

	fn := catalogConsumerFn("lake", "lake") // namespace "default", name "reader"
	env, requeue, err := r.resolveCatalogEnv(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "a Ready catalog does not requeue")

	// Present AND non-empty — the values survive the DIRECT write (not routed through mergeSecretEnv).
	require.Contains(t, env, "FUNCD_CATALOG_LAKE_URL")
	require.Contains(t, env, "FUNCD_CATALOG_LAKE_TOKEN")
	require.Equal(t, proxyURL, env["FUNCD_CATALOG_LAKE_URL"], "URL is status.endpoint (the proxy), verbatim")

	// The injected token is the per-function MAC token — it resolves to THIS Function principal, and is
	// NOT the shared QUACK_TOKEN (the proxy holds that and swaps it in only after an allow).
	injected := env["FUNCD_CATALOG_LAKE_TOKEN"]
	require.NotEmpty(t, injected, "token must survive non-empty")
	require.NotEqual(t, "t0ken-abc", injected, "the shared QUACK_TOKEN is NOT handed to the function")
	want, derr := cataloggw.DeriveCatalogToken(master, fn.Namespace, fn.Name)
	require.NoError(t, derr)
	require.Equal(t, want, injected, "the per-function token is derived over the node master")
	ref, ok := cataloggw.NewCatalogKeys(master, r.store).PrincipalFor(injected)
	require.True(t, ok, "the injected token resolves via the proxy's CatalogKeys")
	require.Equal(t, v1.KindFunction, ref.Type)
	require.Equal(t, fn.Namespace, ref.Namespace)
	require.Equal(t, fn.Name, ref.Name)

	// End-to-end through workerSpec: the DIRECT write means the FUNCD_-prefixed keys reach Env even
	// though mergeSecretEnv would have dropped them.
	spec := r.workerSpec(fn, 0, "/art/app.mjs", nil, env)
	require.Equal(t, proxyURL, spec.Env["FUNCD_CATALOG_LAKE_URL"], "the URL reaches the worker env DIRECTLY")
	require.Equal(t, injected, spec.Env["FUNCD_CATALOG_LAKE_TOKEN"], "the token reaches the worker env DIRECTLY")
}

// scenario: requeue-until-catalog-ready — a bound catalog with no status.endpoint (still deploying)
// makes resolveCatalogEnv requeue fail-closed, injecting no (empty) URL.
func TestScenarioRequeueUntilCatalogReady(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedCatalogService(t, r, "lake", "") // not Ready: no endpoint published

	env, requeue, err := r.resolveCatalogEnv(context.Background(), catalogConsumerFn("lake", "lake"))
	require.NoError(t, err)
	require.True(t, requeue, "a not-Ready catalog (no endpoint) requeues fail-closed")
	require.NotContains(t, env, "FUNCD_CATALOG_LAKE_URL", "no empty URL is injected while the catalog is not Ready")
}

// scenario: token-decoupled-from-catalog-secret (ADR-0137) — the per-function token no longer comes
// from the catalog's Secret, so a Ready catalog whose Secret carries no QUACK_TOKEN STILL injects a
// valid per-function token (the proxy holds the shared engine token; the function never sees it).
// Readiness now gates on status.endpoint alone (covered by TestScenarioRequeueUntilCatalogReady).
func TestScenarioTokenDecoupledFromCatalogSecret(t *testing.T) {
	t.Parallel()
	master := []byte("decoupled-token-master")
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"SOME_OTHER_KEY": "x"}}) // no QUACK_TOKEN
	r.catalogMaster = master
	seedCatalogService(t, r, "lake", "http://127.0.0.1:54321")

	fn := catalogConsumerFn("lake", "lake")
	env, requeue, err := r.resolveCatalogEnv(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "a Ready catalog injects even when its Secret lacks QUACK_TOKEN (token is derived, not read)")
	want2, derr2 := cataloggw.DeriveCatalogToken(master, fn.Namespace, fn.Name)
	require.NoError(t, derr2)
	require.Equal(t, want2, env["FUNCD_CATALOG_LAKE_TOKEN"],
		"the per-function token is derived over the node master, independent of the catalog Secret")
}

// scenario: dev-catalog-extension-dir-to-consumer — when catalogExtensionDir is set (funcdctl dev),
// a catalog-consumer function gets DUCKDB_EXTENSION_DIRECTORY in its worker env (so its handler's
// LOAD quack resolves offline), while a non-consumer function does NOT. Empty dir ⇒ no injection.
func TestScenarioDevCatalogExtensionDirToConsumer(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedCatalogService(t, r, "lake", "lake-quack.default:8080")
	r.catalogExtensionDir = "/dev/ext"

	// consumer: DUCKDB_EXTENSION_DIRECTORY injected.
	consumer := catalogConsumerFn("lake", "lake")
	env, _, err := r.resolveCatalogEnv(context.Background(), consumer)
	require.NoError(t, err)
	spec := r.workerSpec(consumer, 0, "/art/app.mjs", nil, env)
	require.Equal(t, "/dev/ext", spec.Env["DUCKDB_EXTENSION_DIRECTORY"], "a catalog consumer gets the extension dir")

	// non-consumer: no injection.
	nonSpec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", nil, nil)
	require.NotContains(t, nonSpec.Env, "DUCKDB_EXTENSION_DIRECTORY", "a non-consumer gets no extension dir")

	// empty dir (prod default): no injection even for a consumer.
	r.catalogExtensionDir = ""
	prodSpec := r.workerSpec(consumer, 0, "/art/app.mjs", nil, env)
	require.NotContains(t, prodSpec.Env, "DUCKDB_EXTENSION_DIRECTORY", "empty dir (prod) injects nothing")
}

// scenario: token-only-to-declared-consumer — a Function that declares no spec.catalogs receives no
// FUNCD_CATALOG_* env (the token reaches only declared consumers).
func TestScenarioTokenOnlyToDeclaredConsumer(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedCatalogService(t, r, "lake", "lake-quack.default:8080")

	env, requeue, err := r.resolveCatalogEnv(context.Background(), sampleFn()) // no spec.catalogs
	require.NoError(t, err)
	require.False(t, requeue)
	require.Nil(t, env, "a non-declaring function resolves to no catalog env")

	// And through workerSpec: no FUNCD_CATALOG_* keys appear.
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", nil, env)
	for k := range spec.Env {
		require.NotContains(t, k, "FUNCD_CATALOG_", "a non-declaring function gets no FUNCD_CATALOG_* env")
	}
}
