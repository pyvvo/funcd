package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
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

// scenario: binding-injects-endpoint-and-token — a Ready CatalogService (status.endpoint set + a
// Secret carrying QUACK_TOKEN) makes the worker env carry FUNCD_CATALOG_LAKE_URL AND _TOKEN, both
// PRESENT and NON-EMPTY. This specifically guards the M1 regression: routing them through
// mergeSecretEnv (whose FUNCD_ guard drops them) would leave one or both empty/absent.
func TestScenarioBindingInjectsEndpointAndToken(t *testing.T) {
	t.Parallel()
	// The secret resolver returns the catalog's Secret Data — QUACK_TOKEN is selected out of it.
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken-abc", "OTHER": "ignored"}})
	seedCatalogService(t, r, "lake", "lake-quack.default:8080")

	env, requeue, err := r.resolveCatalogEnv(context.Background(), catalogConsumerFn("lake", "lake"))
	require.NoError(t, err)
	require.False(t, requeue, "a Ready catalog does not requeue")

	// Present AND non-empty — the values survive the DIRECT write (not routed through mergeSecretEnv).
	require.Contains(t, env, "FUNCD_CATALOG_LAKE_URL")
	require.Contains(t, env, "FUNCD_CATALOG_LAKE_TOKEN")
	require.Equal(t, "lake-quack.default:8080", env["FUNCD_CATALOG_LAKE_URL"], "URL is status.endpoint, verbatim")
	require.NotEmpty(t, env["FUNCD_CATALOG_LAKE_URL"], "URL must survive non-empty")
	require.Equal(t, "t0ken-abc", env["FUNCD_CATALOG_LAKE_TOKEN"], "token is the QUACK_TOKEN key, re-keyed")
	require.NotEmpty(t, env["FUNCD_CATALOG_LAKE_TOKEN"], "token must survive non-empty")
	require.NotContains(t, env, "OTHER", "only QUACK_TOKEN is selected — the map is NOT merged")

	// End-to-end through workerSpec: the DIRECT write means the FUNCD_-prefixed keys reach Env even
	// though mergeSecretEnv would have dropped them.
	spec := r.workerSpec(catalogConsumerFn("lake", "lake"), 0, "/art/app.mjs", nil, env)
	require.Equal(t, "lake-quack.default:8080", spec.Env["FUNCD_CATALOG_LAKE_URL"], "the URL reaches the worker env DIRECTLY")
	require.Equal(t, "t0ken-abc", spec.Env["FUNCD_CATALOG_LAKE_TOKEN"], "the token reaches the worker env DIRECTLY")
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

// scenario: missing-QUACK_TOKEN — a Ready catalog whose Secret carries no QUACK_TOKEN requeues
// fail-closed rather than injecting an empty token (ADR-0091 fail-closed on a missing token key).
func TestScenarioMissingQuackTokenRequeues(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"SOME_OTHER_KEY": "x"}}) // no QUACK_TOKEN
	seedCatalogService(t, r, "lake", "lake-quack.default:8080")

	env, requeue, err := r.resolveCatalogEnv(context.Background(), catalogConsumerFn("lake", "lake"))
	require.NoError(t, err)
	require.True(t, requeue, "a catalog Secret with no QUACK_TOKEN requeues fail-closed")
	require.NotContains(t, env, "FUNCD_CATALOG_LAKE_TOKEN", "no empty token is injected")
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
