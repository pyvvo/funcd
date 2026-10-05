package function_test

import (
	"context"
	"net/http"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/activator/storescaler"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// testPeriod is the supervision period the ADR-0142 tests run with.
const testPeriod = 50 * time.Millisecond

func withPeriod(d *function.Deps) {
	d.SupervisionPeriod = testPeriod
	d.BootBackoffInitial, d.BootBackoffMax = testPeriod, testPeriod
}

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

// degradedSinceAnHour moves name's Ready condition transition an hour back, as if name had been Degraded that long.
func (h *shimHarness) degradedSinceAnHour(t *testing.T, name string) {
	t.Helper()
	fn := h.getFn(t, name)
	for i := range fn.Status.Conditions {
		if fn.Status.Conditions[i].Type == "Ready" {
			fn.Status.Conditions[i].LastTransitionTime = time.Now().Add(-time.Hour)
		}
	}
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
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
	ready, _ := h.getFn(t, "flaky").Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "CrashLoopBackOff", ready.Reason, "a replacement that cannot boot is a boot crash (ADR-0160)")
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

// Issue #309: a serving Function whose replacement runs but never becomes ready is not re-probed every 200 ms for good.
// Once the replacement has run for the boot timeout with no replica ready, it is stopped and replaced after the
// backoff, as a crash under repair is (ADR-0142, ADR-0030 §4b).
func TestIssue309_NeverReadyReplacementIsReplacedAfterBackoff(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "stall")
	id := runtime.NewInstanceID("default", "stall", "stall-1", 0)
	h.rt.exit("stall", runtime.StateFailed, time.Minute)
	h.rt.hold(id, true)
	h.reconcile(t, "stall")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement boots")

	h.rt.exitRevision("stall", "stall-1", 0, runtime.StateRunning, time.Hour)
	res := h.reconcile(t, "stall")
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a replica is kept while the Function has been Degraded for less than the boot timeout")

	h.degradedSinceAnHour(t, "stall")
	creates, _ := h.rt.counts()
	res = h.reconcile(t, "stall")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase)
	require.Equal(t, testPeriod, res.RequeueAfter, "the pass waits out the backoff instead of re-probing the hung replica")
	require.Contains(t, h.condition(t, "stall", "Ready").Message, "did not become ready")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "stall"), "a hung replacement of a serving Function is not a shape failure")

	h.rt.hold(id, false)
	h.reconcile(t, "stall")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the hung replacement is replaced")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "stall").Status.Phase)
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
// and a pass after its growing wait starts the same instances again instead of replacing them (ADR-0169), writing
// nothing while it still fails.
func TestIssue73_StartFailureWritesFailedStatus(t *testing.T) {
	t.Parallel()
	rt := &createCounter{Runtime: process.New()}
	t.Cleanup(func() { _ = rt.Close() })
	sf := &startFailer{Runtime: rt}
	clk := clock.NewManual(time.Now())
	h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) {
		d.Runtime, d.Clock = sf, clk
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
	require.Equal(t, v1.ConditionUnknown, h.shapeValid(t, "calm"), "a worker that cannot start is not a shape failure, and nothing loaded the generation (ADR-0174)")
	require.Positive(t, res.RequeueAfter)
	require.LessOrEqual(t, res.RequeueAfter, testPeriod, "a start failure is retried after its wait")
	starts := sf.starts.Load()

	rv := fn.ResourceVersion
	res = h.reconcile(t, "calm")
	require.Equal(t, starts, sf.starts.Load(), "a pass inside the wait starts nothing")
	require.Equal(t, rv, h.getFn(t, "calm").ResourceVersion, "a pass inside the wait writes nothing")
	require.Positive(t, res.RequeueAfter)
	require.LessOrEqual(t, res.RequeueAfter, testPeriod)

	clk.Advance(testPeriod)
	h.reconcile(t, "calm")
	require.Equal(t, starts+2, sf.starts.Load(), "both replicas are started again once the wait ends")
	require.EqualValues(t, 2, rt.creates.Load(), "the instances that failed to start are started again, not replaced")
	require.Equal(t, rv, h.getFn(t, "calm").ResourceVersion, "a repeated start failure writes nothing")
}

// readinessListFailer fails List when the reconciler's readiness judgment calls it, so the pass's earlier Lists succeed.
type readinessListFailer struct {
	runtime.Runtime
	failing atomic.Bool
}

func (l *readinessListFailer) List(ctx context.Context, ns v1.NamespaceName) ([]runtime.Instance, error) {
	if l.failing.Load() && calledFrom(".readyReplicas") {
		return nil, fault.Unavailablef("test.List", "the runtime could not list its workers")
	}
	return l.Runtime.List(ctx, ns)
}

// calledFrom reports whether a function whose name ends in suffix is on the caller's stack.
func calledFrom(suffix string) bool {
	pcs := make([]uintptr, 64)
	frames := goruntime.CallersFrames(pcs[:goruntime.Callers(2, pcs)])
	for {
		f, more := frames.Next()
		if strings.HasSuffix(f.Function, suffix) {
			return true
		}
		if !more {
			return false
		}
	}
}

// Issue #353: a List error while the pass judges readiness fails the pass, so it is retried, and writes no status;
// before, it counted zero ready replicas and wrote a serving Function Degraded.
func TestIssue353_ReadinessListErrorWritesNoStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, h *shimHarness){
		"serving": func(t *testing.T, h *shimHarness) {
			h.rt.exitRevision("flaky", "flaky-1", 1, runtime.StateFailed, time.Minute)
		},
		"switch": func(t *testing.T, h *shimHarness) {
			h.apply(t, "flaky", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lf := &readinessListFailer{}
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { lf.Runtime, d.Runtime = d.Runtime, lf })
			h.create(t, "flaky", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
			h.reconcile(t, "flaky")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "flaky").Status.Phase)
			change(t, h)
			rv := h.getFn(t, "flaky").ResourceVersion

			lf.failing.Store(true)
			_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: "flaky"})
			fn := h.getFn(t, "flaky")
			require.Equal(t, v1.PhaseReady, fn.Status.Phase, "a failed read does not mark a serving Function Degraded")
			require.Equal(t, rv, fn.ResourceVersion, "no status is written from a failed read")
			require.Error(t, err, "the pass fails, so it is retried")

			lf.failing.Store(false)
			h.reconcile(t, "flaky")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "flaky").Status.Phase)
		})
	}
}

// brokenSockets is a local API socket provider whose SocketFor fails while broken is set.
type brokenSockets struct{ broken atomic.Bool }

func (b *brokenSockets) SocketFor(_ v1.NamespaceName, name v1.ObjectName) (string, error) {
	if b.broken.Load() {
		return "", fault.Unavailablef("test.SocketFor", "create socket dir: permission denied")
	}
	return "/run/test/" + string(name) + ".sock", nil
}

func (*brokenSockets) Remove(v1.NamespaceName, v1.ObjectName) {}

// Issue #358: a local API socket that cannot be provisioned keeps the Function from going Ready, with a reason naming
// the error, and starts no worker without it; the pass retries, and the Function is Ready once the socket is provisioned.
func TestIssue358_SocketFailureBlocksReady(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]func(*function.Deps){
		"process": func(*function.Deps) {},
		"container": func(d *function.Deps) {
			d.EndpointMode = function.EndpointNetnsFixedPort
			d.ImageFor = func(rt string) string { return "funcd/runtime-" + rt + ":latest" }
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sockets := &brokenSockets{}
			sockets.broken.Store(true)
			h := newShimHarness(t, http.StatusOK, false, withPeriod, mode, func(d *function.Deps) { d.InvokeSockets = sockets })
			h.createFn(t, "lonely")

			res := h.reconcile(t, "lonely")
			fn := h.getFn(t, "lonely")
			require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
			ready, ok := fn.Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, v1.ConditionFalse, ready.Status)
			require.Equal(t, "StartFailed", ready.Reason)
			require.Contains(t, ready.Message, "permission denied")
			creates, _ := h.rt.counts()
			require.Zero(t, creates, "no worker starts without its local API socket")
			require.Positive(t, res.RequeueAfter, "the pass retries")
			require.LessOrEqual(t, res.RequeueAfter, testPeriod, "after the growing wait (ADR-0169)")

			sockets.broken.Store(false)
			time.Sleep(testPeriod)
			h.reconcile(t, "lonely")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "lonely").Status.Phase)
			spec, ok := h.rt.specFor("lonely")
			require.True(t, ok)
			require.NotEmpty(t, spec.Env["FUNCD_INVOKE_SOCKET"])
		})
	}
}

// startFailer fails each Start while failing is set, before the runtime it wraps sees it, and counts every Start.
type startFailer struct {
	runtime.Runtime
	failing atomic.Bool
	starts  atomic.Int32
}

func (s *startFailer) Start(ctx context.Context, id runtime.InstanceID) error {
	s.starts.Add(1)
	if s.failing.Load() {
		return fault.Unavailablef("test.Start", "exec: %q: executable file not found in $PATH", "node")
	}
	return s.Runtime.Start(ctx, id)
}

// wrap puts s in front of the harness runtime.
func (s *startFailer) wrap(d *function.Deps) { s.Runtime, d.Runtime = d.Runtime, s }

// activator is the data path's activator over the reconciler's Endpoints and the store scaler, on clk.
func (h *shimHarness) activator(t *testing.T, clk clock.Clock) *activator.Activator {
	t.Helper()
	a, err := activator.New(activator.Deps{
		Store: h.st, Endpoints: h.r.Endpoints(), Scaler: storescaler.New(h.st), Clock: clk, ActivationTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	return a
}

// refusedAtOnce calls name through a and requires the call to be refused within a second, naming the Failed reason.
func refusedAtOnce(t *testing.T, a *activator.Activator, name, reason string) {
	t.Helper()
	begin := time.Now()
	_, err := a.Wake(context.Background(), activator.FunctionRef{Namespace: "default", Name: v1.ObjectName(name)})
	requireRefused(t, err, name, reason)
	require.Less(t, time.Since(begin), time.Second, "the call is refused at once, not held")
}

// requireRefused requires err to be the 503 a call to a Failed Function gets (ADR-0169 Decision 5).
func requireRefused(t *testing.T, err error, name, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.Contains(t, err.Error(), "function default/"+name+" is Failed ("+reason+")")
}

// phaseIs reports whether name is in phase p, false on a read error, for polling from another goroutine.
func (h *shimHarness) phaseIs(name string, p v1.Phase) bool {
	obj, err := h.st.Get(context.Background(), v1.KindFunction.GVK(), "default", v1.ObjectName(name))
	if err != nil {
		return false
	}
	fn, ok := obj.(*v1.Function)
	return ok && fn.Status.Phase == p
}

// reclaimPastIdle runs idle reclaim twice, the clock moved past idle after each, so a Function a's tracker first sees
// in the first run is claimed in the second.
func reclaimPastIdle(t *testing.T, a *activator.Activator, clk *clock.Manual, idle time.Duration) {
	t.Helper()
	for range 2 {
		require.NoError(t, a.ReclaimIdle(context.Background()))
		clk.Advance(2 * idle)
	}
}

// scenario: broken-handler-stays-failed (ADR-0169) — a woken scale-to-zero Function whose handler cannot load gets the
// call refused once the pass writes Failed; it stays Failed with the load error, and a second call is refused at once
// with no worker created or started.
func TestScenarioBrokenHandlerStaysFailed(t *testing.T) {
	t.Parallel()
	const loadErr = `funcd-shim: shape error: export "handle" is not a function`
	sf := &startFailer{}
	h := newShimHarness(t, http.StatusOK, true, withPeriod, sf.wrap)
	h.rt.setLog(loadErr + "\n")
	h.create(t, "broken", func(fn *v1.Function) { fn.Spec.Replicas = 0 })
	h.reconcile(t, "broken")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "broken").Status.Phase)
	a := h.activator(t, clock.NewManual(time.Now()))

	errc := make(chan error, 1)
	go func() {
		_, err := a.Wake(context.Background(), activator.FunctionRef{Namespace: "default", Name: "broken"})
		errc <- err
	}()
	require.Eventually(t, func() bool { return h.phaseIs("broken", v1.PhaseDeploying) }, 5*time.Second, 5*time.Millisecond, "the call wakes it")
	h.reconcile(t, "broken")
	select {
	case err := <-errc:
		requireRefused(t, err, "broken", "ShapeInvalid")
	case <-time.After(time.Second):
		t.Fatal("the call is not refused within a second of the pass writing Failed")
	}

	rv := h.getFn(t, "broken").ResourceVersion
	for range 3 {
		require.Zero(t, h.reconcile(t, "broken").RequeueAfter, "a shape failure is not retried")
	}
	fn := h.getFn(t, "broken")
	require.Equal(t, rv, fn.ResourceVersion, "a repeated pass writes nothing")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	shape := h.condition(t, "broken", "ShapeValid")
	require.Equal(t, v1.ConditionFalse, shape.Status)
	require.Equal(t, loadErr, shape.Message)
	rr := h.condition(t, "broken", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "ShapeInvalid", rr.Reason)

	refusedAtOnce(t, a, "broken", "ShapeInvalid")
	creates, _ := h.rt.counts()
	require.Equal(t, 1, creates, "the broken worker is not created again")
	require.EqualValues(t, 1, sf.starts.Load(), "the broken worker is not started again")
}

// scenario: fixed-spec-recovers-scale-to-zero-function (ADR-0169) — a fixed spec applied to a Failed scale-to-zero
// Function makes it Ready with no call, and Idle, its worker stopped, after idleTimeout with no call.
func TestScenarioFixedSpecRecoversScaleToZeroFunction(t *testing.T) {
	t.Parallel()
	const idle = time.Minute
	h := newShimHarness(t, http.StatusOK, true, withPeriod)
	h.create(t, "fixme", func(fn *v1.Function) {
		fn.Spec.Replicas = 0
		fn.Spec.Scaling.IdleTimeout = idle
	})
	h.reconcile(t, "fixme")
	h.setPhase(t, "fixme", v1.PhaseDeploying)
	h.reconcile(t, "fixme")
	h.reconcile(t, "fixme")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "fixme").Status.Phase)

	h.rt.setFailing(false)
	h.apply(t, "fixme", func(fn *v1.Function) { fn.Spec.Handler = "handleFixed" })
	h.reconcile(t, "fixme")
	fn := h.getFn(t, "fixme")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the fixed spec boots with no call")
	spec, ok := h.rt.specFor("fixme")
	require.True(t, ok)
	require.Equal(t, "handleFixed", spec.Env["FUNCD_HANDLER"])

	clk := clock.NewManual(time.Now())
	reclaimPastIdle(t, h.activator(t, clk), clk, idle)
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "fixme").Status.Phase)
	h.reconcile(t, "fixme")
	fn = h.getFn(t, "fixme")
	require.Equal(t, v1.PhaseIdle, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas)
	require.Equal(t, runtime.StateStopped, h.rt.revisionStates("fixme")["fixme-2"][0], "the worker is stopped")
}

// scenario: idle-reclaim-skips-failed (ADR-0169) — idle reclaim past idleTimeout leaves a Failed (ShapeInvalid)
// Function Failed, and a call to it is refused at once.
func TestScenarioIdleReclaimSkipsFailed(t *testing.T) {
	t.Parallel()
	const idle = time.Minute
	h := newShimHarness(t, http.StatusOK, true, withPeriod)
	h.create(t, "broken", func(fn *v1.Function) { fn.Spec.Scaling.IdleTimeout = idle })
	h.reconcile(t, "broken")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "broken").Status.Phase)

	clk := clock.NewManual(time.Now())
	a := h.activator(t, clk)
	reclaimPastIdle(t, a, clk, idle)
	h.reconcile(t, "broken")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "broken").Status.Phase, "idle reclaim never moves a Failed Function")
	refusedAtOnce(t, a, "broken", "ShapeInvalid")
	creates, _ := h.rt.counts()
	require.Equal(t, 1, creates)
}

// scenario: start-failure-retried-with-growing-wait (ADR-0169) — a woken scale-to-zero Function whose worker cannot
// start is Failed/StartFailed and is started again after 20, 40, 80 and 80 ms, never sooner, with each requeue the
// remaining wait; a call meanwhile is refused at once and starts nothing. Once the cause is gone the next retry makes it
// Ready, and Idle after idleTimeout.
func TestScenarioStartFailureRetriedWithGrowingWait(t *testing.T) {
	t.Parallel()
	const idle = time.Minute
	clk := clock.NewManual(time.Now())
	sf := &startFailer{}
	sf.failing.Store(true)
	h := newShimHarness(t, http.StatusOK, false, sf.wrap, func(d *function.Deps) {
		d.BootBackoffInitial, d.BootBackoffMax, d.Clock = 20*time.Millisecond, 80*time.Millisecond, clk
	})
	h.create(t, "stuck", func(fn *v1.Function) {
		fn.Spec.Replicas = 0
		fn.Spec.Scaling.IdleTimeout = idle
	})
	h.reconcile(t, "stuck")
	h.setPhase(t, "stuck", v1.PhaseDeploying)
	a := h.activator(t, clk)

	res := h.reconcile(t, "stuck")
	for i, wait := range []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond} {
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "stuck").Status.Phase, i)
		require.Equal(t, "StartFailed", h.condition(t, "stuck", "Ready").Reason, i)
		require.Equal(t, wait, res.RequeueAfter, "the pass comes back when the wait ends (%d)", i)
		starts, rv := sf.starts.Load(), h.getFn(t, "stuck").ResourceVersion

		clk.Advance(wait - time.Millisecond)
		res = h.reconcile(t, "stuck")
		require.Equal(t, time.Millisecond, res.RequeueAfter, "the remaining wait (%d)", i)
		require.Equal(t, rv, h.getFn(t, "stuck").ResourceVersion, "a pass inside the wait writes nothing (%d)", i)
		refusedAtOnce(t, a, "stuck", "StartFailed")
		require.Equal(t, starts, sf.starts.Load(), "no Start inside the wait, from a pass or a call (%d)", i)

		clk.Advance(time.Millisecond)
		res = h.reconcile(t, "stuck")
		require.Equal(t, starts+1, sf.starts.Load(), "started again once the wait ends (%d)", i)
	}
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "stuck").Status.Phase)

	sf.failing.Store(false)
	clk.Advance(res.RequeueAfter)
	h.reconcile(t, "stuck")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "stuck").Status.Phase, "the next retry serves once the cause is gone")
	creates, _ := h.rt.counts()
	require.Equal(t, 1, creates, "the replica that could not start is started, not replaced")

	reclaimPastIdle(t, a, clk, idle)
	h.reconcile(t, "stuck")
	fn := h.getFn(t, "stuck")
	require.Equal(t, v1.PhaseIdle, fn.Status.Phase, "a started Function is reclaimed as usual")
	require.Zero(t, fn.Status.Replicas)
}

// scenario: deleted-and-reapplied-function-starts-fresh (ADR-0169) — a Function whose worker spec failed once (its
// per-name local API socket) is deleted and re-applied with the same name; the first pass creates and starts its
// replica, with no StartFailed status left over.
func TestScenarioDeletedAndReappliedFunctionStartsFresh(t *testing.T) {
	t.Parallel()
	sockets := &brokenSockets{}
	sockets.broken.Store(true)
	h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) { d.InvokeSockets = sockets })
	h.createFn(t, "phoenix")
	h.reconcile(t, "phoenix")
	require.Equal(t, "StartFailed", h.condition(t, "phoenix", "Ready").Reason)

	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "phoenix", ""))
	h.reconcile(t, "phoenix")
	sockets.broken.Store(false)
	h.createFn(t, "phoenix")
	h.reconcile(t, "phoenix")

	fn := h.getFn(t, "phoenix")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the first pass creates and starts the replica")
	require.Empty(t, h.condition(t, "phoenix", "Ready").Reason, "no StartFailed is left over")
	creates, _ := h.rt.counts()
	require.Equal(t, 1, creates)
}
