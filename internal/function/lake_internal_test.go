package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
)

// seedLake stores a Lake in the reconciler's memory store with the given
// published endpoint + spec.secrets, so resolveLakeEnv can Get it. A non-empty endpoint models a
// Ready lake (the reconciler publishes the proxy URL and Phase Ready together); endpoint == ""
// models one still deploying.
func seedLake(t *testing.T, r *Reconciler, name, endpoint string) {
	t.Helper()
	phase := v1.PhasePending
	if endpoint != "" {
		phase = v1.PhaseReady
	}
	seedLakeIn(t, r, name, endpoint, phase)
}

// seedLakeIn is seedLake with an explicit phase, for the not-Ready states that
// still publish an endpoint (the raw engine address while the engine starts).
func seedLakeIn(t *testing.T, r *Reconciler, name, endpoint string, phase v1.Phase) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindLake)
	cs := obj.(*v1.Lake)
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	// A Validate-passing shape: the lake (bucket, prefix) must be one of spec.bindings.blob.
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lakehouse", Bucket: "lakehouse", Prefix: "gold"}}
	cs.Spec.Location = v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	cs.Spec.Bindings.Secret = []v1.ObjectName{"quack-token"} // names the Secret carrying QUACK_TOKEN
	cs.Status.Endpoint = endpoint
	cs.Status.Phase = phase
	_, err := r.store.Create(context.Background(), cs)
	require.NoError(t, err)
}

// lakeConsumerFn is a Function that consumes one lake under a given alias.
func lakeConsumerFn(alias, lake string) *v1.Function {
	fn := sampleFn()
	fn.Name = "reader"
	fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: alias, Lake: v1.ObjectName(lake)}}
	return fn
}

// scenario: binding-injects-endpoint-and-token — a Ready Lake (status.endpoint set, now the
// node-private lake PEP proxy URL, ADR-0137) makes the worker env carry FUNCD_LAKE_LAKE_URL AND
// _TOKEN, both PRESENT and NON-EMPTY. The _TOKEN is the per-function MAC bearer (ADR-0137), NOT the
// shared QUACK_TOKEN: it round-trips via LakeKeys.PrincipalFor to THIS Function principal. This also
// guards the M1 regression: routing the FUNCD_-prefixed keys through addBindingEnv would drop them.
func TestScenarioBindingInjectsEndpointAndToken(t *testing.T) {
	t.Parallel()
	master := []byte("lake-inject-test-node-master")
	// The secret resolver still carries QUACK_TOKEN, but lake injection no longer reads it — the
	// shared token stays with the proxy; the function gets a per-function derived token instead.
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken-abc"}})
	r.lakeMaster = master
	const proxyURL = "http://127.0.0.1:54321" // the node-private proxy URL the reconciler publishes
	seedLake(t, r, "lake", proxyURL)

	fn := lakeConsumerFn("lake", "lake") // namespace "default", name "reader"
	env, requeue, err := r.resolveLakeEnv(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "a Ready lake does not requeue")

	// Present AND non-empty — the values survive the DIRECT write (not routed through addBindingEnv).
	require.Contains(t, env, "FUNCD_LAKE_LAKE_URL")
	require.Contains(t, env, "FUNCD_LAKE_LAKE_TOKEN")
	require.Equal(t, proxyURL, env["FUNCD_LAKE_LAKE_URL"], "URL is status.endpoint (the proxy), verbatim")

	// The injected token is the per-function MAC token — it resolves to THIS Function principal, and is
	// NOT the shared QUACK_TOKEN (the proxy holds that and swaps it in only after an allow).
	injected := env["FUNCD_LAKE_LAKE_TOKEN"]
	require.NotEmpty(t, injected, "token must survive non-empty")
	require.NotEqual(t, "t0ken-abc", injected, "the shared QUACK_TOKEN is NOT handed to the function")
	want, derr := lakegw.DeriveLakeToken(master, fn.Namespace, fn.Name)
	require.NoError(t, derr)
	require.Equal(t, want, injected, "the per-function token is derived over the node master")
	ref, ok := lakegw.NewLakeKeys(master, r.store).PrincipalFor(injected)
	require.True(t, ok, "the injected token resolves via the proxy's LakeKeys")
	require.Equal(t, v1.KindFunction, ref.Type)
	require.Equal(t, fn.Namespace, ref.Namespace)
	require.Equal(t, fn.Name, ref.Name)

	// End-to-end through workerSpec: the DIRECT write means the FUNCD_-prefixed keys reach Env even
	// though addBindingEnv would have dropped them.
	spec := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, env)
	require.Equal(t, proxyURL, spec.Env["FUNCD_LAKE_LAKE_URL"], "the URL reaches the worker env DIRECTLY")
	require.Equal(t, injected, spec.Env["FUNCD_LAKE_LAKE_TOKEN"], "the token reaches the worker env DIRECTLY")
}

// scenario: requeue-until-lake-ready — a bound lake with no status.endpoint (still deploying)
// makes resolveLakeEnv requeue fail-closed, injecting no (empty) URL.
func TestScenarioRequeueUntilLakeReady(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedLake(t, r, "lake", "") // not Ready: no endpoint published

	env, requeue, err := r.resolveLakeEnv(context.Background(), lakeConsumerFn("lake", "lake"))
	require.NoError(t, err)
	require.True(t, requeue, "a not-Ready lake (no endpoint) requeues fail-closed")
	require.NotContains(t, env, "FUNCD_LAKE_LAKE_URL", "no empty URL is injected while the lake is not Ready")
}

// scenario: requeue-until-lake-ready (the engine-starting case) — while the engine starts, the lake
// reconciler publishes the raw engine address as status.endpoint with Phase Pending. Injecting it would
// bypass the PEP proxy, and the engine rejects the per-function token ("Authentication failed"), so the
// consumer must requeue until the lake is Ready and its endpoint is the proxy URL.
func TestScenarioRequeueUntilLakeReadyNotOnEngineAddress(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	r.lakeMaster = []byte("lake-engine-starting-test-master")
	seedLakeIn(t, r, "lake", "10.63.0.5:9494", v1.PhasePending) // the engine's netns address

	env, requeue, err := r.resolveLakeEnv(context.Background(), lakeConsumerFn("lake", "lake"))
	require.NoError(t, err)
	require.True(t, requeue, "a Pending lake requeues even though it publishes an endpoint")
	require.NotContains(t, env, "FUNCD_LAKE_LAKE_URL", "the raw engine address is never injected")
	require.NotContains(t, env, "FUNCD_LAKE_LAKE_TOKEN")
}

// scenario: token-decoupled-from-lake-secret (ADR-0137) — the per-function token no longer comes
// from the lake's Secret, so a Ready lake whose Secret carries no QUACK_TOKEN STILL injects a
// valid per-function token (the proxy holds the shared engine token; the function never sees it).
// Readiness gates on the lake's Phase (TestScenarioRequeueUntilLakeReady*), not on its Secret.
func TestScenarioTokenDecoupledFromLakeSecret(t *testing.T) {
	t.Parallel()
	master := []byte("decoupled-token-master")
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"SOME_OTHER_KEY": "x"}}) // no QUACK_TOKEN
	r.lakeMaster = master
	seedLake(t, r, "lake", "http://127.0.0.1:54321")

	fn := lakeConsumerFn("lake", "lake")
	env, requeue, err := r.resolveLakeEnv(context.Background(), fn)
	require.NoError(t, err)
	require.False(t, requeue, "a Ready lake injects even when its Secret lacks QUACK_TOKEN (token is derived, not read)")
	want2, derr2 := lakegw.DeriveLakeToken(master, fn.Namespace, fn.Name)
	require.NoError(t, derr2)
	require.Equal(t, want2, env["FUNCD_LAKE_LAKE_TOKEN"],
		"the per-function token is derived over the node master, independent of the lake Secret")
}

// scenario: dev-lake-extension-dir-to-consumer — when lakeExtensionDir is set (funcdctl dev),
// a lake-consumer function gets DUCKDB_EXTENSION_DIRECTORY in its worker env (so its handler's
// LOAD quack resolves offline), while a non-consumer function does NOT. Empty dir ⇒ no injection.
func TestScenarioDevLakeExtensionDirToConsumer(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedLake(t, r, "lake", "lake-quack.default:8080")
	r.lakeExtensionDir = "/dev/ext"

	// consumer: DUCKDB_EXTENSION_DIRECTORY injected.
	consumer := lakeConsumerFn("lake", "lake")
	env, _, err := r.resolveLakeEnv(context.Background(), consumer)
	require.NoError(t, err)
	spec := mustWorkerSpec(t, r, consumer, "/art/app.mjs", envresolve.Resolved{}, env)
	require.Equal(t, "/dev/ext", spec.Env["DUCKDB_EXTENSION_DIRECTORY"], "a lake consumer gets the extension dir")

	// non-consumer: no injection.
	nonSpec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", envresolve.Resolved{}, nil)
	require.NotContains(t, nonSpec.Env, "DUCKDB_EXTENSION_DIRECTORY", "a non-consumer gets no extension dir")

	// empty dir (prod default): no injection even for a consumer.
	r.lakeExtensionDir = ""
	prodSpec := mustWorkerSpec(t, r, consumer, "/art/app.mjs", envresolve.Resolved{}, env)
	require.NotContains(t, prodSpec.Env, "DUCKDB_EXTENSION_DIRECTORY", "empty dir (prod) injects nothing")
}

// scenario: token-only-to-declared-consumer — a Function that declares no spec.lakes receives no
// FUNCD_LAKE_* env (the token reaches only declared consumers).
func TestScenarioTokenOnlyToDeclaredConsumer(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"QUACK_TOKEN": "t0ken"}})
	seedLake(t, r, "lake", "lake-quack.default:8080")

	env, requeue, err := r.resolveLakeEnv(context.Background(), sampleFn()) // no spec.lakes
	require.NoError(t, err)
	require.False(t, requeue)
	require.Nil(t, env, "a non-declaring function resolves to no lake env")

	// And through workerSpec: no FUNCD_LAKE_* keys appear.
	spec := mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", envresolve.Resolved{}, env)
	for k := range spec.Env {
		require.NotContains(t, k, "FUNCD_LAKE_", "a non-declaring function gets no FUNCD_LAKE_* env")
	}
}
