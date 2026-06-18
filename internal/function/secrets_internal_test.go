package function

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
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
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	rt := process.New()
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

// scenario: handler-reads-injected-secret — the resolved secret reaches WorkerSpec.Env.
func TestScenarioHandlerReadsInjectedSecret(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", map[string]string{"API_KEY": "s3kr3t", "DB_URL": "postgres://x"})

	require.Equal(t, "s3kr3t", spec.Env["API_KEY"], "the secret value is injected into the worker env")
	require.Equal(t, "postgres://x", spec.Env["DB_URL"])
	require.Equal(t, "app.handler", spec.Env["FUNCD_HANDLER"], "reserved keys remain alongside the secrets")
}

// scenario: reserved-env-not-overridable — a FUNCD_-prefixed secret key cannot shadow a reserved key.
func TestScenarioReservedEnvNotOverridable(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, fakeResolver{})
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs",
		map[string]string{"FUNCD_ARTIFACT": "/evil/override", "FUNCD_PORT": "9999", "SAFE": "ok"})

	require.Equal(t, "/art/app.mjs", spec.Env["FUNCD_ARTIFACT"], "the reserved key keeps its real value; the secret cannot override it")
	require.NotContains(t, spec.Env, "FUNCD_PORT", "a FUNCD_-prefixed secret key is dropped, not injected")
	require.Equal(t, "ok", spec.Env["SAFE"], "a non-reserved secret key still merges")
}

// resolveSecretEnv is fail-closed on every non-happy path (ADR-0057 Decision 5).
func TestResolveSecretEnvFailClosed(t *testing.T) {
	t.Parallel()
	fn := sampleFn()
	fn.Spec.Secrets = []v1.ObjectName{"api-creds"}

	t.Run("no-secrets-returns-nil", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{})
		env, err := r.resolveSecretEnv(context.Background(), sampleFn(), false)
		require.NoError(t, err)
		require.Nil(t, env)
	})
	t.Run("not-configured-fails-closed", func(t *testing.T) {
		r := newShimReconciler(t, nil) // no resolver wired
		_, err := r.resolveSecretEnv(context.Background(), fn, false)
		require.Error(t, err)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	})
	t.Run("pooled-fails-closed", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{env: map[string]string{"API_KEY": "x"}})
		_, err := r.resolveSecretEnv(context.Background(), fn, true) // pooled
		require.Error(t, err, "a pooled function cannot inject secrets into its shared worker")
	})
	t.Run("resolver-error-propagates", func(t *testing.T) {
		r := newShimReconciler(t, fakeResolver{err: fault.Forbiddenf("test", "denied")})
		_, err := r.resolveSecretEnv(context.Background(), fn, false)
		require.Error(t, err)
		require.Equal(t, fault.Forbidden, fault.KindOf(err))
	})
}

func TestIsReservedFuncdKeyAndSecretNames(t *testing.T) {
	t.Parallel()
	require.True(t, isReservedFuncdKey("FUNCD_PORT"))
	require.True(t, isReservedFuncdKey("FUNCD_ANYTHING_FUTURE"))
	require.False(t, isReservedFuncdKey("API_KEY"))
	require.Equal(t, []string{"a", "b"}, secretNames([]v1.ObjectName{"a", "b"}))
}
