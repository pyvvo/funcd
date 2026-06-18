package function_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/function"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/scheduler/singlenode"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// blackboxResolver is a function.SecretResolver double for the reconcile-level tests.
type blackboxResolver struct {
	env map[string]string
	err error
}

func (b blackboxResolver) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return b.env, b.err
}

// newSecretHarness builds a reconciler (legacy mode) wired with a fake secret resolver, so the
// ADR-0057 resolve gate runs before any worker is provisioned.
func newSecretHarness(t *testing.T, sr function.SecretResolver) *harness {
	t.Helper()
	st := store.New(memory.New())
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	r, err := function.NewReconciler(function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw,
		Validator: function.NewBasicValidator(), Secrets: sr,
	})
	require.NoError(t, err)
	return &harness{r: r, st: st, rt: rt, gw: gw}
}

func (h *harness) createSecretFn(t *testing.T, name string, secrets ...v1.ObjectName) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "app.handler"
	fn.Spec.Artifact = v1.ArtifactRef{URI: "blob://artifacts/" + name}
	fn.Spec.Secrets = secrets
	_, err := h.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

// scenario: unauthorized-secret-fails-materialization — a PDP-deny fails the function closed.
func TestScenarioUnauthorizedSecretFailsMaterialization(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, blackboxResolver{err: fault.Forbiddenf("secrets", "read denied")})
	h.createSecretFn(t, "echo", "api-creds")
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	cond, ok := fn.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "SecretResolveFailed", cond.Reason)
	require.Equal(t, 0, h.running(t, "echo"), "no worker is started when a secret cannot be resolved")
}

// scenario: missing-secret-fails — a named Secret that does not exist fails closed (not silent).
func TestScenarioMissingSecretFails(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, blackboxResolver{err: fault.NotFoundf("secrets", "no such secret")})
	h.createSecretFn(t, "echo", "nope")
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	cond, _ := fn.Status.Conditions.Get("Ready")
	require.Equal(t, "SecretResolveFailed", cond.Reason)
	require.Equal(t, 0, h.running(t, "echo"))
}

// scenario: not-configured-fails — declaring secrets with no resolver wired fails closed.
func TestScenarioSecretsNotConfiguredFails(t *testing.T) {
	t.Parallel()
	h := newSecretHarness(t, nil) // no resolver
	h.createSecretFn(t, "echo", "api-creds")
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	cond, _ := fn.Status.Conditions.Get("Ready")
	require.Equal(t, "SecretResolveFailed", cond.Reason)
	require.Equal(t, 0, h.running(t, "echo"))
}

// scenario: secret-value-not-persisted — after reconcile the stored Function holds only the
// secret NAMES; the resolved VALUE never lands in the persisted spec/status.
func TestScenarioSecretValueNotPersisted(t *testing.T) {
	t.Parallel()
	const plaintext = "super-secret-value"
	h := newSecretHarness(t, blackboxResolver{env: map[string]string{"API_KEY": plaintext}})
	h.createSecretFn(t, "echo", "api-creds")
	_, err := h.r.Reconcile(context.Background(),
		controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "echo"})
	require.NoError(t, err)

	fn := h.getFn(t, "echo")
	require.Equal(t, []v1.ObjectName{"api-creds"}, fn.Spec.Secrets, "only the secret name is persisted")
	raw, err := json.Marshal(fn)
	require.NoError(t, err)
	require.NotContains(t, string(raw), plaintext, "the resolved secret value is never written to the Function resource")
}
