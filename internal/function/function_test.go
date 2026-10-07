package function_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
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
	rt := process.New(nil)
	t.Cleanup(func() { _ = rt.Close() })
	sch, err := singlenode.New("local", v1.HostPlatform())
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

	require.Equal(t, 2, h.running(t, "echo"), "2 workers provisioned")
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
	require.Equal(t, 0, h.running(t, "broken"), "no workers provisioned")
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

	require.Equal(t, 0, h.running(t, "echo"), "workers stopped")
	require.Empty(t, h.routes(t), "route removed")
}

// scenario: wake-provisions-scaled-to-zero.
func TestScenarioWakeProvisionsScaledToZero(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.createFn(t, "agent", 0, true) // MinReplicas defaults to 0 (scale-to-zero); spec.replicas 0
	h.reconcile(t, "agent")
	require.Equal(t, 0, h.running(t, "agent"), "scaled to zero — no workers")

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

// listFailer is a runtime whose List always fails.
type listFailer struct{ runtime.Runtime }

func (listFailer) List(context.Context, v1.NamespaceName) ([]runtime.Instance, error) {
	return nil, fault.Unavailablef("test.List", "runtime down")
}

// Issue #448: the error a failed runtime List returns says "list workers", not "list workers".
func TestIssue448_ListFailureSaysListWorkers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(d *function.Deps) { d.Runtime = listFailer{Runtime: d.Runtime} })
	_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "gone"})
	require.ErrorContains(t, err, "function.instances: list workers: test.List: runtime down")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
}

// ADR-0174 tests: RevisionReady and ShapeValid report no True before a replica of the latest generation has been ready.

func scaleToZero(fn *v1.Function) { fn.Spec.Replicas = 0 }

// requireRevisionStatus asserts name's RevisionReady and ShapeValid status and reason, observed at its generation.
func (h *shimHarness) requireRevisionStatus(t *testing.T, name string, status v1.ConditionStatus, reason string) {
	t.Helper()
	fn := h.getFn(t, name)
	for _, typ := range []v1.ConditionType{"RevisionReady", "ShapeValid"} {
		c, ok := fn.Status.Conditions.Get(typ)
		require.True(t, ok, "%s is set", typ)
		require.Equal(t, status, c.Status, typ)
		require.Equal(t, reason, c.Reason, typ)
		require.Equal(t, fn.Generation, c.ObservedGeneration, typ)
	}
}

// wakeToReady creates name scale-to-zero, wakes it as the activator does and reconciles it to Ready.
func (h *shimHarness) wakeToReady(t *testing.T, name string) {
	t.Helper()
	h.create(t, name, scaleToZero)
	h.reconcile(t, name)
	h.setPhase(t, name, v1.PhaseDeploying)
	h.reconcile(t, name)
	require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase)
}

// scenario: never-booted-idle-is-unknown (ADR-0174).
func TestScenarioNeverBootedIdleIsUnknown(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusServiceUnavailable, false)
	h.create(t, "agent", scaleToZero)
	h.reconcile(t, "agent")

	fn := h.getFn(t, "agent")
	require.Equal(t, v1.PhaseIdle, fn.Status.Phase)
	require.Empty(t, fn.Status.ServingRevision)
	creates, _ := h.rt.counts()
	require.Zero(t, creates, "no worker is created")
	h.requireRevisionStatus(t, "agent", v1.ConditionUnknown, "NotStarted")
	require.Equal(t, "no replica of this generation has been ready yet", h.condition(t, "agent", "ShapeValid").Message)
}

// scenario: first-ready-replica-turns-true (ADR-0174).
func TestScenarioFirstReadyReplicaTurnsTrue(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	_, setReady := h.rt.serveRevision(t, "agent-1", http.StatusServiceUnavailable)
	h.create(t, "agent", scaleToZero)
	h.reconcile(t, "agent")

	h.setPhase(t, "agent", v1.PhaseDeploying)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "agent").Status.Phase, "the woken replica boots")
	sv := h.condition(t, "agent", "ShapeValid")
	require.Equal(t, v1.ConditionUnknown, sv.Status)
	require.Equal(t, "NotStarted", sv.Reason)
	rr := h.condition(t, "agent", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "Progressing", rr.Reason)

	setReady(http.StatusOK)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "agent").Status.Phase)
	h.requireRevisionStatus(t, "agent", v1.ConditionTrue, "")
}

// scenario: reclaim-keeps-true (ADR-0174).
func TestScenarioReclaimKeepsTrue(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.wakeToReady(t, "agent")
	h.setPhase(t, "agent", v1.PhaseIdle)
	h.reconcile(t, "agent")
	creates, _ := h.rt.counts()

	h.reconcile(t, "agent")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "no worker boots")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "agent").Status.Phase)
	require.Equal(t, runtime.StateStopped, h.rt.revisionStates("agent")["agent-1"][0])
	h.requireRevisionStatus(t, "agent", v1.ConditionTrue, "")
}

// scenario: wake-of-served-generation (ADR-0174).
func TestScenarioWakeOfServedGeneration(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	_, setReady := h.rt.serveRevision(t, "agent-1", http.StatusOK)
	h.wakeToReady(t, "agent")
	h.setPhase(t, "agent", v1.PhaseIdle)
	h.reconcile(t, "agent")

	time.Sleep(testPeriod) // past the reclaimed replica's restart backoff (ADR-0142)
	setReady(http.StatusServiceUnavailable)
	h.setPhase(t, "agent", v1.PhaseDeploying)
	h.reconcile(t, "agent")
	fn := h.getFn(t, "agent")
	require.Equal(t, v1.PhaseDeploying, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas, "the woken replica boots and gets no call yet (ADR-0161)")
	require.Equal(t, v1.ConditionTrue, h.condition(t, "agent", "ShapeValid").Status, "the generation has served")
	rr := h.condition(t, "agent", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "Progressing", rr.Reason)

	setReady(http.StatusOK)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "agent").Status.Phase)
	h.requireRevisionStatus(t, "agent", v1.ConditionTrue, "")
}

// scenario: redeploy-while-idle-is-unknown (ADR-0174).
func TestScenarioRedeployWhileIdleIsUnknown(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false)
	h.wakeToReady(t, "agent")
	h.setPhase(t, "agent", v1.PhaseIdle)
	h.reconcile(t, "agent")
	creates, _ := h.rt.counts()

	h.apply(t, "agent", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "agent")
	fn := h.getFn(t, "agent")
	require.Equal(t, "agent-2", fn.Status.CurrentRevision)
	require.Equal(t, v1.PhaseIdle, fn.Status.Phase)
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "no worker boots")
	h.requireRevisionStatus(t, "agent", v1.ConditionUnknown, "NotStarted")

	h.setPhase(t, "agent", v1.PhaseDeploying)
	h.reconcile(t, "agent")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "agent").Status.Phase)
	h.requireRevisionStatus(t, "agent", v1.ConditionTrue, "")
}

// scenario: switch-in-progress-shape-unknown (ADR-0174).
func TestScenarioSwitchInProgressShapeUnknown(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	_, setReady2 := h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-1", fn.Status.ServingRevision, "revision 1 serves while revision 2 boots")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	sv := h.condition(t, "echo", "ShapeValid")
	require.Equal(t, v1.ConditionUnknown, sv.Status)
	require.Equal(t, "NotStarted", sv.Reason)
	require.Equal(t, fn.Generation, sv.ObservedGeneration)
	require.Equal(t, "Progressing", h.condition(t, "echo", "RevisionReady").Reason)

	setReady2(http.StatusOK)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision)
	h.requireRevisionStatus(t, "echo", v1.ConditionTrue, "")
}

// scenario: failed-generation-keeps-false (ADR-0174).
func TestScenarioFailedGenerationKeepsFalse(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, true)
	h.createFn(t, "broken")
	h.reconcile(t, "broken")

	require.Equal(t, v1.PhaseFailed, h.getFn(t, "broken").Status.Phase)
	h.requireRevisionStatus(t, "broken", v1.ConditionFalse, "ShapeInvalid")
}
