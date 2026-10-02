package function_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type harness struct {
	r  *function.Reconciler
	st store.Store
	rt runtime.Runtime
	gw gateway.Gateway
}

func newHarness(t *testing.T, opts ...func(*function.Deps)) *harness {
	t.Helper()
	st := store.New(memory.New())
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local")
	require.NoError(t, err)
	gw := embedded.New()
	deps := function.Deps{
		Store: st, Runtime: rt, Scheduler: sch, Gateway: gw, Validator: function.NewBasicValidator(),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	r, err := function.NewReconciler(deps)
	require.NoError(t, err)
	return &harness{r: r, st: st, rt: rt, gw: gw}
}

func (h *harness) createFn(t *testing.T, name string, replicas int, valid bool) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	fn.Spec.Replicas = replicas
	if valid {
		fn.Spec.Runtime = "nodejs22"
		fn.Spec.Handler = "app.handler"
		fn.Spec.Image = "blob://artifacts/" + name
	}
	_, err := h.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func (h *harness) reconcile(t *testing.T, name string) {
	t.Helper()
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

func (h *harness) getFn(t *testing.T, name string) *v1.Function {
	t.Helper()
	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function)
}

func (h *harness) setPhase(t *testing.T, name string, phase v1.Phase) {
	t.Helper()
	fn := h.getFn(t, name)
	fn.Status.Phase = phase
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
}

func (h *harness) running(t *testing.T, name string) int {
	t.Helper()
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	n := 0
	for _, in := range insts {
		if in.Name == v1.ObjectName(name) && in.State == runtime.StateRunning {
			n++
		}
	}
	return n
}

func (h *harness) routes(t *testing.T) []gateway.Route {
	t.Helper()
	rs, err := h.gw.Routes(context.Background())
	require.NoError(t, err)
	return rs
}

// scenario: apply-stamps-revision.
func TestScenarioApplyStampsRevision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "echo", 1, true)
	h.reconcile(t, "echo")

	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.CurrentRevision)
	_, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", "echo-1")
	require.NoError(t, err, "an immutable Revision was stamped")
}

// scenario: reconcile-provisions-and-routes.
func TestScenarioReconcileProvisionsAndRoutes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "echo", 2, true)
	h.reconcile(t, "echo")

	require.Equal(t, 2, h.running(t, "echo"), "2 workeres provisioned")
	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 2, fn.Status.Replicas)
	require.Len(t, h.routes(t), 1, "a route was programmed")
}

// scenario: shape-invalid-blocks-ready.
func TestScenarioShapeInvalidBlocksReady(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "broken", 1, false) // missing runtime/handler/artifact
	h.reconcile(t, "broken")

	fn := h.getFn(t, "broken")
	require.NotEqual(t, v1.PhaseReady, fn.Status.Phase)
	c, ok := fn.Status.Conditions.Get("ShapeValid")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, c.Status, "ShapeValid: False")
	require.Empty(t, h.routes(t), "no route to a shape-invalid function")
	require.Equal(t, 0, h.running(t, "broken"), "no workeres provisioned")
}

// scenario: endpoints-resolves-ready-upstream.
func TestScenarioEndpointsResolvesReadyUpstream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "echo", 1, true)
	h.reconcile(t, "echo")

	ep := h.r.Endpoints()
	up, ready, err := ep.Upstream(context.Background(), activator.FunctionRef{Namespace: "default", Name: "echo"})
	require.NoError(t, err)
	require.True(t, ready)
	require.NotEmpty(t, up)

	_, ready, err = ep.Upstream(context.Background(), activator.FunctionRef{Namespace: "default", Name: "absent"})
	require.NoError(t, err)
	require.False(t, ready, "an unknown/not-ready function is not ready")
}

// scenario: scale-changes-replicas.
// A replica change stamps a Revision, so it switches (ADR-0143 Decision 9): the count converges once the old revision
// has drained.
func TestScenarioScaleChangesReplicas(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(d *function.Deps) { d.HandOutSettle = time.Millisecond })
	h.createFn(t, "echo", 1, true)
	h.reconcile(t, "echo")
	require.Equal(t, 1, h.running(t, "echo"))

	fn := h.getFn(t, "echo")
	fn.Spec.Replicas = 3
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcileDrained(t, "echo")
	require.Equal(t, 3, h.running(t, "echo"), "converged up to 3")

	fn = h.getFn(t, "echo")
	fn.Spec.Replicas = 1
	_, err = h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcileDrained(t, "echo")
	require.Equal(t, 1, h.running(t, "echo"), "converged back down to 1")
}

// reconcileDrained reconciles name until no revision drains (ADR-0143).
func (h *harness) reconcileDrained(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		h.reconcile(t, name)
		return h.getFn(t, name).Status.DrainingRevision == ""
	}, 5*time.Second, 5*time.Millisecond, "the old revision drains")
}

// scenario: delete-reclaims.
func TestScenarioDeleteReclaims(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "echo", 1, true)
	h.reconcile(t, "echo")
	require.Equal(t, 1, h.running(t, "echo"))
	require.Len(t, h.routes(t), 1)

	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "echo", ""))
	h.reconcile(t, "echo") // sees it gone → teardown

	require.Equal(t, 0, h.running(t, "echo"), "workeres stopped")
	require.Empty(t, h.routes(t), "route removed")
}

// scenario: wake-provisions-scaled-to-zero.
func TestScenarioWakeProvisionsScaledToZero(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "agent", 0, true) // MinReplicas defaults to 0 (scale-to-zero); spec.replicas 0
	h.reconcile(t, "agent")
	require.Equal(t, 0, h.running(t, "agent"), "scaled to zero — no workeres")

	// The activator wakes it (partitioned Phase = Deploying).
	h.setPhase(t, "agent", v1.PhaseDeploying)
	h.reconcile(t, "agent")
	require.Equal(t, 1, h.running(t, "agent"), "wake (Phase=Deploying) provisions ≥1 — the wake is not dropped")

	// The activator reclaims it (Phase = Idle).
	h.setPhase(t, "agent", v1.PhaseIdle)
	h.reconcile(t, "agent")
	require.Equal(t, 0, h.running(t, "agent"), "reclaim (Phase=Idle) converges to 0")
}

// scenario: routes-preserved-across-functions.
func TestScenarioRoutesPreservedAcrossFunctions(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "fa", 1, true)
	h.createFn(t, "fb", 1, true)

	h.reconcile(t, "fa")
	require.Len(t, h.routes(t), 1)
	h.reconcile(t, "fb") // full-table replace-all must NOT clobber fa's route

	rs := h.routes(t)
	require.Len(t, rs, 2, "both functions' routes are present (no clobber)")
}

// scenario: route-programming tolerates an unresolved upstream — a Ready function whose worker is
// momentarily unresolvable (empty upstream) must NOT make programAllRoutes reject the whole route
// table; the other Ready functions stay routed. Regression: programAllRoutes used to pass the empty
// upstream straight to the gateway, which rejects the ENTIRE batch — so one mid-transition function
// would drop every route and fail the reconcile that programmed them.
func TestScenarioRouteToleratesUnresolvedUpstream(t *testing.T) {
	h := newHarness(t)
	h.createFn(t, "alpha", 1, true)
	h.reconcile(t, "alpha")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "alpha").Status.Phase)

	// beta is Ready in the store but was never provisioned — it has no running worker, so its
	// upstream resolves to "". Re-reconciling alpha re-programs ALL routes and must tolerate beta.
	h.createFn(t, "beta", 1, true)
	h.setPhase(t, "beta", v1.PhaseReady)

	h.reconcile(t, "alpha") // must NOT error despite beta's empty upstream

	var haveAlpha, haveBeta bool
	for _, rt := range h.routes(t) {
		switch rt.PathPrefix {
		case "/function/alpha":
			haveAlpha = true
		case "/function/beta":
			haveBeta = true
		}
	}
	require.True(t, haveAlpha, "the resolved function stays routed")
	require.False(t, haveBeta, "the unresolved-upstream function is omitted, not a batch-rejecting error")
}
