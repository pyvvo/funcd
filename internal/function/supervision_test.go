package function_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// testPeriod is the supervision period the ADR-0142 tests run with.
const testPeriod = 50 * time.Millisecond

func withPeriod(d *function.Deps) { d.SupervisionPeriod = testPeriod }

func (h *shimHarness) setPhase(t *testing.T, name string, phase v1.Phase) {
	t.Helper()
	fn := h.getFn(t, name)
	fn.Status.Phase = phase
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
}

func (h *shimHarness) shapeValid(t *testing.T, name string) v1.ConditionStatus {
	t.Helper()
	c, ok := h.getFn(t, name).Status.Conditions.Get("ShapeValid")
	require.True(t, ok)
	return c.Status
}

// deployReady creates name and reconciles it to Ready.
func (h *shimHarness) deployReady(t *testing.T, name string) {
	t.Helper()
	h.createFn(t, name)
	res := h.reconcile(t, name)
	require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase)
	require.Equal(t, testPeriod, res.RequeueAfter, "a Ready function comes back after the supervision period")
}

// scenario: crashed-function-worker-restarts (ADR-0142) — a Ready function whose worker dies is replaced on the next
// periodic pass, with no write to the Function, and is Ready again — never ShapeInvalid.
func TestScenarioCrashedFunctionWorkerRestarts(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "crash")
	creates, _ := h.rt.counts()

	h.rt.exit("crash", runtime.StateFailed, time.Minute)
	res := h.reconcile(t, "crash")

	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the dead replica was re-created")
	fn := h.getFn(t, "crash")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "crash"), "a crash after serving is not a shape failure")
	require.Equal(t, 1, fn.Status.Replicas)
	require.Equal(t, testPeriod, res.RequeueAfter)
}

// scenario: second-wake-after-reclaim (ADR-0142) — a scale-to-zero function that served and was reclaimed wakes and
// serves again; before, the reclaimed instance stayed listed and the wake created nothing.
func TestScenarioSecondWakeAfterReclaim(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.createFn(t, "sleepy")
	fn := h.getFn(t, "sleepy")
	fn.Spec.Replicas = 0
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcile(t, "sleepy")

	h.setPhase(t, "sleepy", v1.PhaseDeploying)
	h.reconcile(t, "sleepy")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "sleepy").Status.Phase, "first wake")

	h.setPhase(t, "sleepy", v1.PhaseIdle)
	h.reconcile(t, "sleepy")
	require.Equal(t, 0, h.getFn(t, "sleepy").Status.Replicas, "reclaimed")

	time.Sleep(testPeriod)
	h.setPhase(t, "sleepy", v1.PhaseDeploying)
	h.reconcile(t, "sleepy")
	fn = h.getFn(t, "sleepy")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the second wake serves again")
	require.Equal(t, 1, fn.Status.Replicas)
}

// scenario: failed-restart-retries-with-backoff (ADR-0142) — a replacement that cannot boot is retried at most once
// per period while the function stays Degraded, and the function is Ready again once a worker boots.
func TestScenarioFailedRestartRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "flaky")

	h.rt.exit("flaky", runtime.StateFailed, time.Minute)
	h.rt.setFailing(true)
	h.reconcile(t, "flaky")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "flaky").Status.Phase, "no replica is ready while it is replaced")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "flaky"))
	c1, _ := h.rt.counts()

	res := h.reconcile(t, "flaky")
	c2, _ := h.rt.counts()
	require.Equal(t, c1, c2, "a replacement younger than one period is not replaced again")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "flaky").Status.Phase)
	require.Positive(t, res.RequeueAfter)
	require.LessOrEqual(t, res.RequeueAfter, testPeriod, "it comes back when the backoff ends")

	time.Sleep(testPeriod)
	h.reconcile(t, "flaky")
	c3, _ := h.rt.counts()
	require.Equal(t, c2+1, c3, "retried once the backoff has passed")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "flaky").Status.Phase)

	h.rt.setFailing(false)
	time.Sleep(testPeriod)
	h.reconcile(t, "flaky")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "flaky").Status.Phase, "Ready again once a worker boots")
}

// scenario: boot-failure-stays-failed (ADR-0142) — a new function whose handler cannot load ends Failed (ShapeInvalid)
// and its worker is not restarted.
func TestScenarioBootFailureStaysFailed(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, true, withPeriod)
	h.createFn(t, "broken")
	res := h.reconcile(t, "broken")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "broken").Status.Phase)
	require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "broken"))
	require.Zero(t, res.RequeueAfter)
	creates, _ := h.rt.counts()

	time.Sleep(testPeriod)
	h.reconcile(t, "broken")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "a boot failure of a tried spec is not restarted")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "broken").Status.Phase)
}

// A new function whose handler blocks while it loads — the shim runs but never binds its port — ends Failed
// (ShapeInvalid) once its replica has run for the boot timeout without becoming ready (issue #76, ADR-0030 §4b).
func TestIssue76_NeverReadyHandlerFailsAfterBootTimeout(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.rt.hold(runtime.NewInstanceID("default", "hang", "hang-1", 0), true)
	h.createFn(t, "hang")
	h.reconcile(t, "hang")
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "hang").Status.Phase, "a replica that just started is still booting")

	h.rt.exitRevision("hang", "hang-1", 0, runtime.StateRunning, time.Hour) // still running, started an hour ago
	res := h.reconcile(t, "hang")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "hang").Status.Phase)
	require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "hang"))
	require.Contains(t, h.condition(t, "hang", "ShapeValid").Message, "did not become ready", "a hung handler has no load error to carry")
	require.Zero(t, res.RequeueAfter, "a Failed function is not polled again")
	require.Empty(t, h.routes(t))
}

// scenario: fixed-spec-recovers-failed-function (ADR-0142) — applying a fixed spec to a Failed (ShapeInvalid)
// function deploys the new spec, and the function becomes Ready.
func TestScenarioFixedSpecRecoversFailedFunction(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, true, withPeriod)
	h.createFn(t, "fixme")
	h.reconcile(t, "fixme")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "fixme").Status.Phase)

	h.rt.setFailing(false)
	fn := h.getFn(t, "fixme")
	fn.Spec.Handler = "handleFixed"
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcile(t, "fixme")

	fn = h.getFn(t, "fixme")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the untried spec got a fresh worker")
	spec, ok := h.rt.specFor("fixme")
	require.True(t, ok)
	require.Equal(t, "handleFixed", spec.Env["FUNCD_HANDLER"], "the replacement runs the new spec")
}

// scenario: ready-function-stays-quiescent (ADR-0142, ADR-0047) — steady-state passes over a Ready function write
// nothing to the store and only check each replica's status.
func TestScenarioReadyFunctionStaysQuiescent(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "calm")
	rv := h.getFn(t, "calm").ResourceVersion
	creates, lists := h.rt.counts()

	for range 5 {
		res := h.reconcile(t, "calm")
		require.Equal(t, testPeriod, res.RequeueAfter)
	}
	c, l := h.rt.counts()
	require.Equal(t, rv, h.getFn(t, "calm").ResourceVersion, "no store write in steady state")
	require.Equal(t, creates, c, "no worker created")
	require.Equal(t, lists, l, "steady state checks replicas with Status only, never a List")
}

// A reclaim that lands while a replacement waits out its backoff scales the function to zero instead of failing it,
// and a wake before the backoff ends waits in Deploying instead of dropping back to Idle (ADR-0142).
func TestReclaimDuringRepairBackoffIsNotAShapeFailure(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.createFn(t, "nap")
	fn := h.getFn(t, "nap")
	fn.Spec.Replicas = 0
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcile(t, "nap")
	h.setPhase(t, "nap", v1.PhaseDeploying)
	h.reconcile(t, "nap")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "nap").Status.Phase)

	h.rt.exit("nap", runtime.StateFailed, time.Minute)
	h.rt.setFailing(true)
	h.reconcile(t, "nap")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "nap").Status.Phase, "the replacement cannot boot")

	h.setPhase(t, "nap", v1.PhaseIdle)
	h.reconcile(t, "nap")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "nap").Status.Phase, "reclaimed, not failed")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "nap"))

	h.rt.setFailing(false)
	h.setPhase(t, "nap", v1.PhaseDeploying)
	res := h.reconcile(t, "nap")
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "nap").Status.Phase, "a wake inside the backoff waits")
	require.Positive(t, res.RequeueAfter)
	require.LessOrEqual(t, res.RequeueAfter, testPeriod, "it comes back when the backoff ends")

	time.Sleep(testPeriod)
	h.reconcile(t, "nap")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "nap").Status.Phase, "the wake serves once the backoff has passed")
}

// createCounter counts the Creates its runtime serves.
type createCounter struct {
	runtime.Runtime
	creates atomic.Int32
}

func (c *createCounter) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	c.creates.Add(1)
	return c.Runtime.Create(ctx, spec)
}

// Issue #73: a worker that cannot start (its interpreter is missing) ends Failed with a reason naming the start error,
// and a later pass starts the same instances again instead of replacing them, writing nothing while it still fails.
func TestIssue73_StartFailureWritesFailedStatus(t *testing.T) {
	t.Parallel()
	rt := &createCounter{Runtime: process.New()}
	t.Cleanup(func() { _ = rt.Close() })
	h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) {
		d.Runtime = rt
		d.ShimCommand = []string{"/nonexistent/bin/node", "/opt/funcd/shim.mjs"}
	})
	h.createFn(t, "calm")
	fn := h.getFn(t, "calm")
	fn.Spec.Replicas = 2
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)

	res := h.reconcile(t, "calm")
	fn = h.getFn(t, "calm")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	require.Equal(t, fn.Generation, fn.Status.ObservedGeneration)
	ready, ok := fn.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "StartFailed", ready.Reason)
	require.Contains(t, ready.Message, "/nonexistent/bin/node")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "calm"), "a worker that cannot start is not a shape failure")
	require.Equal(t, testPeriod, res.RequeueAfter, "a start failure is retried once per period")

	rv := fn.ResourceVersion
	res = h.reconcile(t, "calm")
	require.EqualValues(t, 2, rt.creates.Load(), "the instances that failed to start are started again, not replaced")
	require.Equal(t, rv, h.getFn(t, "calm").ResourceVersion, "a repeated start failure writes nothing")
	require.Equal(t, testPeriod, res.RequeueAfter)
}
