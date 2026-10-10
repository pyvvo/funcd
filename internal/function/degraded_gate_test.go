package function_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/pooling"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0221 tests: while a gate or a pass fails, a Degraded Function serves again once a worker of its serving revision
// passes its readiness probe; a Ready one keeps ADR-0161's rule.

func bindApp(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"app"} }

func pooledApp(fn *v1.Function) {
	bindApp(fn)
	fn.Spec.Pooling.Worker = "w1"
}

func (h *shimHarness) hasRoute(t *testing.T, name string) bool {
	t.Helper()
	return slices.ContainsFunc(h.routes(t), func(r gateway.Route) bool { return r.ID == gateway.RouteID("default/"+name) })
}

// poolWorkerOf is the name of member's pool worker.
func (h *shimHarness) poolWorkerOf(t *testing.T, member string) v1.ObjectName {
	t.Helper()
	key, ok := pooling.ParsePool("default", h.getFn(t, member).Status.Pool)
	require.True(t, ok)
	return v1.ObjectName("__pool__" + key.Runtime + "__" + key.Worker + "__" + key.AccessHash)
}

// releasePool lets every pool worker named pool listen, and the ones created later.
func (h *shimHarness) releasePool(pool v1.ObjectName) {
	h.rt.setHoldNew(false)
	for _, id := range h.rt.poolWorkers("default", pool) {
		h.rt.hold(id, false)
	}
}

// requireDegradedUnderGate asserts name is Degraded with no replica, Ready False, RevisionReady False with reason at the
// current generation, no route and no ready upstream.
func (h *shimHarness) requireDegradedUnderGate(t *testing.T, name, reason string) {
	t.Helper()
	fn := h.getFn(t, name)
	require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas)
	require.Equal(t, v1.ConditionFalse, h.condition(t, name, "Ready").Status)
	rr := h.requireCondition(t, name, "RevisionReady", v1.ConditionFalse, reason)
	require.Equal(t, fn.Generation, rr.ObservedGeneration)
	require.False(t, h.hasRoute(t, name), "no route")
	_, ready := h.upstream(t, name)
	require.False(t, ready)
}

// requireServesUnderGate asserts name serves while its gate fails with reason: Ready with one replica, Ready True with
// no reason, RevisionReady False with reason at the current generation, a route and a ready upstream.
func (h *shimHarness) requireServesUnderGate(t *testing.T, name, reason string) {
	t.Helper()
	fn := h.getFn(t, name)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	h.requireCondition(t, name, "Ready", v1.ConditionTrue, "")
	rr := h.requireCondition(t, name, "RevisionReady", v1.ConditionFalse, reason)
	require.Equal(t, fn.Generation, rr.ObservedGeneration)
	require.True(t, h.hasRoute(t, name), "a route")
	_, ready := h.upstream(t, name)
	require.True(t, ready)
}

// requireGateCleared runs passes over name until RevisionReady is True, and requires it Ready.
func (h *shimHarness) requireGateCleared(t *testing.T, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, _ = h.tryReconcile(name)
		c, ok := h.getFn(t, name).Status.Conditions.Get("RevisionReady")
		return ok && c.Status == v1.ConditionTrue
	}, 5*time.Second, 10*time.Millisecond, "RevisionReady turns True once the gate clears")
	require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase)
	h.requireCondition(t, name, "Ready", v1.ConditionTrue, "")
}

// serveStalling gives revision rev a readiness endpoint that answers 200 until stall is called, then answers only
// after the probe has timed out.
func (f *fakeRuntime) serveStalling(t *testing.T, rev v1.ObjectName) (stall func()) {
	t.Helper()
	var stalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stalled.Load() && r.URL.Path == "/health/readiness" {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	port, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	require.NoError(t, err)
	f.mu.Lock()
	f.revPort[rev] = port
	f.mu.Unlock()
	return func() { stalled.Store(true) }
}

// scenario: pooled-restarting-recovers-under-gate
func TestScenarioPooledRestartingRecoversUnderGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.configMap(t, "app")
	h.create(t, "member", pooledApp)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
	h.rt.setMember("member", "restarting", "")
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "member").Status.Phase)

	h.deleteConfigMap(t, "app")
	res := h.reconcile(t, "member")
	h.requireDegradedUnderGate(t, "member", "ConfigResolveFailed")
	require.Equal(t, testPeriod, res.RequeueAfter, "the next pass is one supervision period later")

	h.rt.setMember("member", "ready", "")
	h.reconcile(t, "member")
	h.requireServesUnderGate(t, "member", "ConfigResolveFailed")

	h.configMap(t, "app")
	h.requireGateCleared(t, "member")
}

// scenario: pooled-probe-failed-recovers-under-shape-gate
func TestScenarioPooledProbeFailedRecoversUnderShapeGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.configMap(t, "app")
	h.create(t, "member", pooledApp)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
	h.rt.setMembersDown(true)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "member").Status.Phase)

	h.apply(t, "member", func(fn *v1.Function) { fn.Spec.Handler = "" })
	_, err := h.tryReconcile("member")
	require.Error(t, err, "an unanswered pool host fails the pass")
	h.requireDegradedUnderGate(t, "member", "ReconcileFailed")

	h.rt.setMembersDown(false)
	h.reconcile(t, "member")
	h.requireServesUnderGate(t, "member", "ShapeInvalid")
	h.requireCondition(t, "member", "ShapeValid", v1.ConditionFalse, "ShapeInvalid")

	h.apply(t, "member", func(fn *v1.Function) { fn.Spec.Handler = "handle" })
	h.requireGateCleared(t, "member")
}

// scenario: pooled-replaced-worker-recovers-under-gate
func TestScenarioPooledReplacedWorkerRecoversUnderGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.configMap(t, "app")
	h.create(t, "member", pooledApp)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
	pool := h.poolWorkerOf(t, "member")
	h.rt.setHoldNew(true)
	h.rt.exitRevision(pool, "", 0, runtime.StateFailed, time.Minute)
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "member").Status.Phase)
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates(pool)[""][0], "the replacement runs without a port")

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "member")
	h.requireDegradedUnderGate(t, "member", "ConfigResolveFailed")

	h.releasePool(pool)
	h.reconcile(t, "member")
	h.requireServesUnderGate(t, "member", "ConfigResolveFailed")

	h.configMap(t, "app")
	h.requireGateCleared(t, "member")
}

// scenario: solo-unlistened-replica-recovers-under-gate
func TestScenarioSoloUnlistenedReplicaRecoversUnderGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.configMap(t, "app")
	h.create(t, "echo", bindApp)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
	id := replicaID("echo", 1, 0)
	h.rt.hold(id, true)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "echo").Status.Phase)

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "echo")
	h.requireDegradedUnderGate(t, "echo", "ConfigResolveFailed")

	h.rt.hold(id, false)
	h.reconcile(t, "echo")
	h.requireServesUnderGate(t, "echo", "ConfigResolveFailed")

	h.configMap(t, "app")
	h.requireGateCleared(t, "echo")
}

// scenario: solo-replaced-replica-recovers-under-gate
func TestScenarioSoloReplacedReplicaRecoversUnderGate(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.configMap(t, "app")
	h.create(t, "echo", bindApp)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
	h.rt.setHoldNew(true)
	h.rt.exit("echo", runtime.StateFailed, time.Minute)
	h.reconcile(t, "echo")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "echo").Status.Phase)
	id := replicaID("echo", 1, 0)
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0], "the replacement runs without a port")

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "echo")
	h.requireDegradedUnderGate(t, "echo", "ConfigResolveFailed")

	h.rt.setHoldNew(false)
	h.rt.hold(id, false)
	h.reconcile(t, "echo")
	h.requireServesUnderGate(t, "echo", "ConfigResolveFailed")

	h.configMap(t, "app")
	h.requireGateCleared(t, "echo")
}

// scenario: failed-pass-promotes-degraded
func TestScenarioFailedPassPromotesDegraded(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withPlatforms(&fakePlatforms{}))
	h.configMap(t, "app")
	_, setStatus := h.rt.serveRevision(t, "stall-1", http.StatusOK)
	h.create(t, "stall", func(fn *v1.Function) {
		bindApp(fn)
		fn.Spec.ImageDigest = digestHere
	})
	h.reconcile(t, "stall")
	h.rt.exit("stall", runtime.StateFailed, time.Minute)
	setStatus(http.StatusServiceUnavailable)
	h.reconcile(t, "stall")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement listens but is not ready")

	setStatus(http.StatusOK)
	h.apply(t, "stall", func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage })
	_, err := h.tryReconcile("stall")
	require.Error(t, err)
	h.requireServesUnderGate(t, "stall", "ReconcileFailed")
	require.Equal(t, "stall-1", h.getFn(t, "stall").Status.ServingRevision)

	h.apply(t, "stall", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.requireGateCleared(t, "stall")
}

// scenario: failed-pass-during-pool-rebuild-promotes
func TestScenarioFailedPassDuringPoolRebuildPromotes(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, withPlatforms(&fakePlatforms{}))
	pooledPair(t, h)
	h.rt.setHoldNew(true)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 2, "the new pool worker boots beside the old one")
	h.rt.setMember("b", "restarting", "")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase)

	h.rt.setMember("b", "ready", "")
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage })
	_, err := h.tryReconcile("b")
	require.Error(t, err)
	h.requireServesUnderGate(t, "b", "ReconcileFailed")
	require.Equal(t, "b-1", h.getFn(t, "b").Status.ServingRevision, "it serves at S")

	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.ImageDigest = "" })
	h.reconcile(t, "b")
	fn := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "finish keeps b Ready")
	h.requireCondition(t, "b", "Ready", v1.ConditionTrue, "")
}

// scenario: ready-function-stays-ready-under-gate
func TestScenarioReadyFunctionStaysReadyUnderGate(t *testing.T) {
	t.Parallel()
	for name, unready := range map[string]func(h *shimHarness, t *testing.T) func(){
		"not-ready": func(h *shimHarness, t *testing.T) func() {
			_, setStatus := h.rt.serveRevision(t, "echo-1", http.StatusOK)
			return func() { setStatus(http.StatusServiceUnavailable) }
		},
		"timed-out": func(h *shimHarness, t *testing.T) func() { return h.rt.serveStalling(t, "echo-1") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch)
			h.configMap(t, "app")
			turn := unready(h, t)
			h.create(t, "echo", bindApp)
			h.reconcile(t, "echo")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)

			turn()
			h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "" })
			h.reconcile(t, "echo")
			h.requireServesUnderGate(t, "echo", "ShapeInvalid")
		})
	}
}

// scenario: asleep-gate-not-promoted
func TestScenarioAsleepGateNotPromoted(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.configMap(t, "app")
	h.create(t, "sleepy", func(fn *v1.Function) {
		bindApp(fn)
		fn.Spec.Scaling.MinReplicas = 0
	})
	h.reconcile(t, "sleepy")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "sleepy").Status.Phase)
	h.setPhase(t, "sleepy", v1.PhaseIdle)
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("sleepy")["sleepy-1"][0], "the S replica still runs")

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "sleepy")
	fn := h.getFn(t, "sleepy")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase, "the gate's phase, never Ready")
	require.Zero(t, fn.Status.Replicas)
	h.requireCondition(t, "sleepy", "Ready", v1.ConditionFalse, "ConfigResolveFailed")
	h.requireCondition(t, "sleepy", "Asleep", v1.ConditionTrue, "ScaledToZero")
	rr := h.requireCondition(t, "sleepy", "RevisionReady", v1.ConditionFalse, "ConfigResolveFailed")
	require.Equal(t, fn.Generation, rr.ObservedGeneration)
	require.Equal(t, runtime.StateStopped, h.rt.revisionStates("sleepy")["sleepy-1"][0], "its worker stops")
	require.False(t, h.hasRoute(t, "sleepy"))
}
