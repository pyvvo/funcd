package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// fakeMat is a no-op Materializer so workerSpec takes the shim (Env-building) branch.
type fakeMat struct{}

func (fakeMat) Materialize(context.Context, *v1.Function) (string, error) { return "/art/app.mjs", nil }

// fakeResolver is an in-package SecretResolver double.
type fakeResolver struct {
	env map[string]string
	err error
}

func (f fakeResolver) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return f.env, f.err
}

// newShimReconciler builds a reconciler in process (shim) mode so workerSpec builds Env.
func newShimReconciler(t *testing.T, secretsResolver SecretResolver) *Reconciler {
	t.Helper()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New(nil)
	t.Cleanup(func() { _ = rt.Close() })
	r, err := NewReconciler(Deps{
		Store: store.New(memory.New()), Runtime: rt, Scheduler: sch,
		Gateway: embedded.New(), Validator: NewBasicValidator(),
		Materializer: fakeMat{}, ShimCommand: []string{"node", "shim.mjs"},
		Secrets: secretsResolver,
	})
	require.NoError(t, err)
	return r
}

func sampleFn() *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace = "echo", "default"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "app.handler"
	return fn
}

// mustWorkerSpec is replica 0's workerSpec with bindEnv, for a test whose spec builds.
func mustWorkerSpec(t *testing.T, r *Reconciler, fn *v1.Function, artifactPath string, bindEnv envresolve.Resolved, catalogEnv map[string]string) runtime.WorkerSpec {
	t.Helper()
	spec, err := r.workerSpec(fn, 0, artifactPath, bindEnv, catalogEnv)
	require.NoError(t, err)
	return spec
}

// scenario: secret-shadows-config (ADR-0249) — a bound Secret's values never reach the worker env, nor does a config
// key a bound Secret also holds, nor FUNCD_SECRET_KEYS; the config keys the Secret does not hold do, and
// FUNCD_CONFIG_KEYS lists them.
func TestScenarioSecretShadowsConfig(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{env: map[string]string{"STRIPE_KEY": "sk", "MODE": "prod"}})
	createCM(t, r, "default", "app-settings", map[string]string{"LOG_LEVEL": "debug", "MODE": "dev"})
	fn := configFn("app-settings")
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}
	bind, err := r.resolveBindingEnv(context.Background(), fn)
	require.NoError(t, err)

	spec := mustWorkerSpec(t, r, fn, "/art/app.mjs", bind, nil)
	require.Equal(t, "debug", spec.Env["LOG_LEVEL"])
	require.Equal(t, "LOG_LEVEL", spec.Env["FUNCD_CONFIG_KEYS"])
	for _, k := range []string{"STRIPE_KEY", "MODE", "FUNCD_SECRET_KEYS"} {
		require.NotContains(t, spec.Env, k)
	}
}

// scenario: reserved-env-not-overridable — a FUNCD_-prefixed bound key cannot shadow a reserved key.
func TestScenarioReservedEnvNotOverridable(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	createCM(t, r, "default", "evil", map[string]string{"FUNCD_ARTIFACT": "/evil/override", "FUNCD_PORT": "9999", "SAFE": "ok"})
	bind, err := r.resolveBindingEnv(context.Background(), configFn("evil"))
	require.NoError(t, err)
	spec := mustWorkerSpec(t, r, configFn("evil"), "/art/app.mjs", bind, nil)

	require.Equal(t, "/art/app.mjs", spec.Env["FUNCD_ARTIFACT"], "the reserved key keeps its real value")
	require.NotContains(t, spec.Env, "FUNCD_PORT", "a FUNCD_-prefixed bound key is dropped, not injected")
	require.Equal(t, "ok", spec.Env["SAFE"], "a non-reserved config key still reaches the env")
}

// resolveBindingEnv is fail-closed on every non-happy path (ADR-0057 Decision 5; ADR-0093 extends the gate to config).
func TestResolveSecretEnvFailClosed(t *testing.T) {
	t.Parallel()
	fn := sampleFn()
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-creds"}

	t.Run("no-secrets-returns-nil", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{})
		env, err := r.resolveBindingEnv(context.Background(), sampleFn())
		require.NoError(t, err)
		require.Zero(t, env)
	})
	t.Run("not-configured-fails-closed", func(t *testing.T) {
		r := newShimReconciler(t, nil) // no resolver wired
		_, err := r.resolveBindingEnv(context.Background(), fn)
		require.Error(t, err)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	})
	t.Run("pooled-fails-closed", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{env: map[string]string{"API_KEY": "x"}})
		pooled := sampleFn()
		pooled.Spec.Bindings.Secret = []v1.ObjectName{"api-creds"}
		pooled.Spec.Pooling.Worker = "agents"
		env, err := r.resolveBindingEnv(context.Background(), pooled)
		require.NoError(t, err, "a pooled member resolves its secrets like a solo one")
		require.Equal(t, "x", env.Env["API_KEY"])
	})
	t.Run("resolver-error-propagates", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{err: fault.Forbiddenf("test", "denied")})
		_, err := r.resolveBindingEnv(context.Background(), fn)
		require.Error(t, err)
		require.Equal(t, fault.Forbidden, fault.KindOf(err))
	})
}

func TestSecretNames(t *testing.T) {
	t.Parallel()
	// The reserved-FUNCD_-key guard moved to internal/secrets (ADR-0092, single definition — see
	// secrets.TestMergeEnvGuarded / secrets.IsReservedKey); only secretNames remains local here.
	require.Equal(t, []string{"a", "b"}, secretNames([]v1.ObjectName{"a", "b"}))
}

// A solo worker gets the non-empty FUNCD_CONFIG_KEYS directly, past the reserved-key guard (ADR-0245), and no list when
// empty; it never gets FUNCD_SECRET_KEYS (ADR-0249).
func TestWorkerSpecWritesBindingKeyLists(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	spec, err := r.workerSpec(sampleFn(), 0, "/art/app.mjs", envresolve.Resolved{
		Env:        map[string]string{"LOG_LEVEL": "debug", "MODE": "prod", "STRIPE_KEY": "sk"},
		ConfigKeys: []string{"LOG_LEVEL"},
		SecretKeys: []string{"MODE", "STRIPE_KEY"},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, "LOG_LEVEL", spec.Env["FUNCD_CONFIG_KEYS"])
	require.NotContains(t, spec.Env, "FUNCD_SECRET_KEYS", "secrets are read from funcd (ADR-0249)")

	spec = mustWorkerSpec(t, r, sampleFn(), "/art/app.mjs", envresolve.Resolved{}, nil)
	require.NotContains(t, spec.Env, "FUNCD_CONFIG_KEYS")
}
