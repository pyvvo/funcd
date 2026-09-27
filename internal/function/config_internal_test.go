package function

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
)

// createCM stores a ConfigMap in the reconciler's store so resolveBindingEnv can read it.
func createCM(t *testing.T, r *Reconciler, ns v1.NamespaceName, name string, data map[string]string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	cm.Spec.Data = data
	_, err := r.store.Create(context.Background(), cm)
	require.NoError(t, err)
}

func configFn(names ...v1.ObjectName) *v1.Function {
	fn := sampleFn()
	fn.Spec.Config = names
	return fn
}

// scenario: config-injects-env — a spec.config ConfigMap's Data reaches the worker env.
func TestScenarioConfigInjectsEnv(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil) // config needs no secret resolver
	createCM(t, r, "default", "tuning", map[string]string{"DUCKDB_THREADS": "4"})

	env, err := r.resolveBindingEnv(context.Background(), configFn("tuning"), false)
	require.NoError(t, err)
	require.Equal(t, "4", env["DUCKDB_THREADS"], "the ConfigMap value is injected as env")

	spec := r.workerSpec(configFn("tuning"), 0, "/art/app.mjs", env, nil)
	require.Equal(t, "4", spec.Env["DUCKDB_THREADS"], "and it reaches WorkerSpec.Env")
}

// scenario: config-then-secret-order — a key present in both a bound ConfigMap and a bound Secret
// resolves to the SECRET value (config merged first, secret overrides).
func TestScenarioConfigThenSecretOrder(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"SHARED": "from-secret"}})
	createCM(t, r, "default", "tuning", map[string]string{"SHARED": "from-config", "ONLY_CFG": "c"})

	fn := configFn("tuning")
	fn.Spec.Secrets = []v1.ObjectName{"creds"}
	env, err := r.resolveBindingEnv(context.Background(), fn, false)
	require.NoError(t, err)
	require.Equal(t, "from-secret", env["SHARED"], "the secret overrides the config default (config-then-secret)")
	require.Equal(t, "c", env["ONLY_CFG"], "a config-only key survives")
}

// scenario: config-missing-fails-closed — a spec.config naming an absent ConfigMap fails closed,
// attributed to the config side (ErrConfig → ConfigResolveFailed at the reconciler).
func TestScenarioConfigMissingFailsClosed(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	_, err := r.resolveBindingEnv(context.Background(), configFn("nope"), false)
	require.Error(t, err)
	require.True(t, errors.Is(err, envresolve.ErrConfig), "a missing ConfigMap is a config-side failure")
	require.False(t, errors.Is(err, envresolve.ErrSecret))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "the underlying store NotFound kind survives")
}

// scenario: pooled-config-solo-gated — a pooled function declaring spec.config (no secrets) fails
// closed: per-function env can't isolate in a shared pooled worker (the gate flips to config-OR-secrets).
func TestScenarioPooledConfigSoloGated(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	createCM(t, r, "default", "tuning", map[string]string{"DUCKDB_THREADS": "4"})
	_, err := r.resolveBindingEnv(context.Background(), configFn("tuning"), true) // pooled
	require.Error(t, err, "a pooled config-only function is gated closed")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// A config-only function with neither config nor secrets still returns (nil, nil) — no gate trips.
func TestBindingEnvEmptyReturnsNil(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	env, err := r.resolveBindingEnv(context.Background(), sampleFn(), false)
	require.NoError(t, err)
	require.Nil(t, env)
}
