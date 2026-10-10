package function_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0225 tests: a pool worker that never listens is a boot crash, counted, created again after the growing wait and
// reported as CrashLoopBackOff; a failed Start of it goes on the same count.

const poolBootTimeout = time.Second

// crashHarness is healthHarness (a manual clock, a 10 s period, the 10 s / 5 min backoff) with a node pool host and a
// 1 s boot timeout.
func crashHarness(t *testing.T, opts ...func(*function.Deps)) (*shimHarness, *clock.Manual) {
	t.Helper()
	opts = append([]func(*function.Deps){withNodePool, func(d *function.Deps) { d.BootTimeout = poolBootTimeout }}, opts...)
	h, clk, _ := healthHarness(t, opts...)
	return h, clk
}

// bootWait is the growing wait after the n-th boot crash at the default backoff.
func bootWait(n int) time.Duration { return 10 * time.Second << (n - 1) }

// crashMessage is the status message of the n-th boot crash of a pool worker that did not listen within 1 s.
func crashMessage(n int) string {
	return fmt.Sprintf("the pool worker did not listen within 1s; boot crash %d in a row, retried %s after its last start", n, bootWait(n))
}

// holdPool keeps every pool worker of worker id w from listening (true), or lets it listen (false).
func (h *shimHarness) holdPool(w string, held bool) {
	h.rt.hold(runtime.NewInstanceID("default", poolOf(w), "", 0), held)
}

// deployHeld creates name in pool worker w, which never listens, and runs its first pass.
func (h *shimHarness) deployHeld(t *testing.T, name, w string) {
	t.Helper()
	h.holdPool(w, true)
	h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = w })
	h.reconcile(t, name)
}

// bootCrashes runs name's passes through n boot crashes of its held pool worker, started by the pass before: each boot
// is stopped bootTimeout after its start and created again once its wait has passed. The last one stays stopped.
func (h *shimHarness) bootCrashes(t *testing.T, clk *clock.Manual, name string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if i > 1 {
			clk.Advance(bootWait(i-1) - poolBootTimeout)
			h.reconcile(t, name)
		}
		clk.Advance(poolBootTimeout)
		h.reconcile(t, name)
		require.Contains(t, h.condition(t, name, "Ready").Message, fmt.Sprintf("boot crash %d in a row", i))
	}
}

// poolID is the id of the newest pool worker of worker id w.
func (h *shimHarness) poolID(t *testing.T, w string) runtime.InstanceID {
	t.Helper()
	ids := h.rt.poolWorkers("default", poolOf(w))
	require.NotEmpty(t, ids)
	return ids[len(ids)-1]
}

// stateOf is the state of instance id.
func (f *fakeRuntime) stateOf(id runtime.InstanceID) runtime.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[id]
}

// poolShim is a pool worker's own endpoint at url: /health/members as the fake answers it for that worker, counted, and
// /health/liveness with the code a test sets.
type poolShim struct {
	url           string
	live, members atomic.Int32
}

func (s *poolShim) setLive(code int) { s.live.Store(int32(code)) }

// membersCalls is how many GET /health/members the worker has answered.
func (s *poolShim) membersCalls() int { return int(s.members.Load()) }

// servePoolWorker gives pool worker id, and each one created again under id, its own poolShim.
func (f *fakeRuntime) servePoolWorker(t *testing.T, id runtime.InstanceID) *poolShim {
	t.Helper()
	s := &poolShim{}
	s.setLive(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/members":
			s.members.Add(1)
			f.serveMembersOf(w, id)
		case "/health/liveness":
			w.WriteHeader(int(s.live.Load()))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	f.mu.Lock()
	f.idPort[id] = port
	f.mu.Unlock()
	return s
}

// scenario: pool-worker-never-listens-backs-off — a new member's pool worker that never listens is stopped 1 s after
// its start, b reads CrashLoopBackOff, the pass comes back at the end of the 10 s wait and not before, the next boot is
// polled at its boot deadline, and the next boot crash waits 20 s.
func TestScenarioPoolWorkerNeverListensBacksOff(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.deployHeld(t, "b", "never")
	creates := h.creates()

	clk.Advance(poolBootTimeout)
	res := h.reconcile(t, "b")
	require.Equal(t, creates, h.creates(), "stopped, not created again at once")
	require.Equal(t, runtime.StateStopped, h.rt.stateOf(h.poolID(t, "never")))
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "b").Status.Phase)
	ready := h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, crashMessage(1), ready.Message)
	rr := h.requireCondition(t, "b", "RevisionReady", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, crashMessage(1), rr.Message)
	h.requireCondition(t, "b", "ShapeValid", v1.ConditionUnknown, "NotStarted")
	require.Equal(t, 9*time.Second, res.RequeueAfter, "the pass comes back when the wait ends")

	clk.Advance(9*time.Second - time.Millisecond)
	h.reconcile(t, "b")
	require.Equal(t, creates, h.creates(), "not created again before 10 s after its last start")
	clk.Advance(time.Millisecond)
	res = h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "created again once the wait has passed")
	require.Equal(t, poolBootTimeout, res.RequeueAfter, "a counted boot is polled at its boot deadline, not at 1 ms or the readiness poll")
	require.Equal(t, crashMessage(1), h.condition(t, "b", "Ready").Message)

	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, crashMessage(2), h.condition(t, "b", "Ready").Message)
}

// silentRebuild serves a and b on one pool worker, which it returns with its URL, then redeploys b, whose new pool
// worker never listens and is stopped at its boot timeout in b's pass.
func silentRebuild(t *testing.T, h *shimHarness, clk *clock.Manual) (old runtime.InstanceID, oldURL string) {
	t.Helper()
	old = pooledPair(t, h)
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	release()
	clk.Advance(time.Millisecond) // the new pool worker is the newer one
	h.rt.setHoldNew(true)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 2)
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	return old, oldURL
}

// scenario: redeployed-member-degrades-when-old-worker-gone — while the redeployed member's new pool worker crash-loops,
// the old one exits and is retired: nothing serves b, which reads Degraded and CrashLoopBackOff on Ready and
// RevisionReady.
func TestScenarioRedeployedMemberDegradesWhenOldWorkerGone(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	old, _ := silentRebuild(t, h, clk)
	h.requireCondition(t, "b", "Ready", v1.ConditionTrue, "")

	h.rt.end(old, sigkill(), 0)
	h.reconcile(t, "b")
	require.True(t, h.rt.wasRemoved(old), "the exited old pool worker is retired")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseDegraded, b.Status.Phase)
	ready := h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, crashMessage(1), ready.Message)
	rr := h.requireCondition(t, "b", "RevisionReady", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, crashMessage(1), rr.Message)
}

// scenario: serving-member-degrades-on-crash-loop — b's only pool worker exits after listening, and its replacement
// never listens: 1 s after its start b reads Degraded, Ready=False/CrashLoopBackOff.
func TestScenarioServingMemberDegradesOnCrashLoop(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "serving" })
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)

	h.holdPool("serving", true)
	h.rt.exitRevision(poolOf("serving"), "", 0, runtime.StateFailed, healthPeriod)
	creates := h.creates()
	h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "an end after listening is replaced a period after its start")
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase)
	ready := h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, crashMessage(1), ready.Message)
}

// scenario: first-listen-resets-pool-count — after boot crash 2 the pool worker listens and b is Ready with no reason;
// a later boot that never listens is boot crash 1.
func TestScenarioFirstListenResetsPoolCount(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.deployHeld(t, "b", "reset")
	h.bootCrashes(t, clk, "b", 2)
	clk.Advance(bootWait(2) - poolBootTimeout)
	h.holdPool("reset", false)
	h.reconcile(t, "b")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
	h.requireCondition(t, "b", "Ready", v1.ConditionTrue, "")
	require.Zero(t, function.BootCount(h.r, h.poolID(t, "reset")), "the first listen resets the count")

	h.holdPool("reset", true)
	h.rt.exitRevision(poolOf("reset"), "", 0, runtime.StateFailed, healthPeriod)
	h.reconcile(t, "b")
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, crashMessage(1), h.condition(t, "b", "Ready").Message)
}

// scenario: pool-exit-before-listen-counts — a boot that exits with code 1 before it listens goes on the same count
// and is created again its wait after its last start, not once per period.
func TestScenarioPoolExitBeforeListenCounts(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.deployHeld(t, "b", "exits")
	h.rt.endStarts(h.poolID(t, "exits"), runtime.Exit{Cause: runtime.ExitByCode, Code: 1}, true)
	h.bootCrashes(t, clk, "b", 1)

	clk.Advance(bootWait(1) - poolBootTimeout)
	h.reconcile(t, "b")
	ready := h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
	require.Equal(t, "the pool worker exited with code 1 before it listened; boot crash 2 in a row, retried 20s after its last start", ready.Message)
	creates := h.creates()
	clk.Advance(bootWait(2) - time.Millisecond)
	h.reconcile(t, "b")
	require.Equal(t, creates, h.creates(), "not created again before its wait, though a period has passed")
	clk.Advance(time.Millisecond)
	h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "created again at its last start plus its wait")
}

// startFailing is crashHarness with every pool worker Start failing while sf.failing is set.
func startFailing(t *testing.T) (*shimHarness, *clock.Manual, *startFailer) {
	t.Helper()
	sf := &startFailer{}
	h, clk := crashHarness(t, sf.wrap)
	return h, clk, sf
}

// switchStartFails serves a and b on one pool worker, then redeploys b while every Start fails: b reads Ready from the
// old pool worker and RevisionReady=False/StartFailed. It returns the old pool worker and its URL.
func switchStartFails(t *testing.T, h *shimHarness, sf *startFailer) (old runtime.InstanceID, oldURL string) {
	t.Helper()
	old = pooledPair(t, h)
	oldURL, release := h.rt.serveWorker(t, old, "old pool")
	release()
	sf.failing.Store(true)
	h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "b")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase, "the old pool worker serves b")
	require.Equal(t, "b-1", b.Status.ServingRevision)
	h.requireCondition(t, "b", "Ready", v1.ConditionTrue, "")
	rr := h.requireCondition(t, "b", "RevisionReady", v1.ConditionFalse, "StartFailed")
	require.Contains(t, rr.Message, "executable file not found")
	return old, oldURL
}

// scenario: pool-start-failure-backs-off — a pool worker whose Start fails is retried 10 s, 20 s, then 40 s after each
// failure, not once per period, while its never-served member reads Failed/StartFailed; a redeployed member that an
// old pool worker serves reads Ready from it and RevisionReady=False/StartFailed.
func TestScenarioPoolStartFailureBacksOff(t *testing.T) {
	t.Parallel()
	t.Run("never served", func(t *testing.T) {
		t.Parallel()
		h, clk, sf := startFailing(t)
		sf.failing.Store(true)
		h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "nostart" })
		res := h.reconcile(t, "b")
		require.Equal(t, v1.PhaseFailed, h.getFn(t, "b").Status.Phase)
		h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "StartFailed")
		require.Equal(t, bootWait(1), res.RequeueAfter)
		for n := 1; n <= 2; n++ {
			starts := sf.starts.Load()
			clk.Advance(bootWait(n) - time.Millisecond)
			h.reconcile(t, "b")
			require.Equal(t, starts, sf.starts.Load(), "not started again within wait %d", n)
			clk.Advance(time.Millisecond)
			res = h.reconcile(t, "b")
			require.Equal(t, starts+1, sf.starts.Load(), "started again once wait %d has passed", n)
			require.Equal(t, bootWait(n+1), res.RequeueAfter)
			h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "StartFailed")
		}
	})
	t.Run("served by an old pool worker", func(t *testing.T) {
		t.Parallel()
		h, _, sf := startFailing(t)
		switchStartFails(t, h, sf)
	})
}

// scenario: rebuild-during-crash-loop-starts-at-zero — after boot crash 2, a member joins the key: a pool worker of the
// new manifest is created in the same pass, the stopped one is retired, and the new one's first boot crash is 1.
func TestScenarioRebuildDuringCrashLoopStartsAtZero(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.deployHeld(t, "b", "rebuild")
	h.bootCrashes(t, clk, "b", 2)
	stopped := h.poolID(t, "rebuild")

	h.create(t, "c", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "rebuild" })
	creates := h.creates()
	h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "the new manifest's pool worker is created in the same pass")
	require.True(t, h.rt.wasRemoved(stopped), "the stopped pool worker is retired")
	require.NotEqual(t, stopped, h.poolID(t, "rebuild"))
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, crashMessage(1), h.condition(t, "b", "Ready").Message)
}

// scenario: listened-hung-pool-worker-restarts-at-once — a pool worker that listened and stops answering
// /health/liveness for livenessTimeout is restarted at once, with no boot count.
func TestScenarioListenedHungPoolWorkerRestartsAtOnce(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "hung" })
	h.reconcile(t, "b")
	id := h.poolID(t, "hung")
	shim := h.rt.servePoolWorker(t, id)
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)

	shim.setLive(http.StatusServiceUnavailable)
	clk.Advance(healthLiveness)
	creates := h.creates()
	h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "restarted at once")
	require.Zero(t, function.BootCount(h.r, id), "a hang after listening is no boot crash")
	require.NotEqual(t, "CrashLoopBackOff", h.condition(t, "b", "Ready").Reason)
}

// scenario: pool-host-exits-after-listen-keeps-period — the pool host listens, then exits while it loads b: no count,
// created again once per period, and b reads no CrashLoopBackOff.
func TestScenarioPoolHostExitsAfterListenKeepsPeriod(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.rt.setMember("b", "loading", "")
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "loads" })
	h.reconcile(t, "b")
	id := h.poolID(t, "loads")

	h.rt.end(id, runtime.Exit{Cause: runtime.ExitByCode, Code: 1}, 0)
	creates := h.creates()
	h.reconcile(t, "b")
	require.Equal(t, creates, h.creates(), "not created again before a period")
	require.Zero(t, function.BootCount(h.r, id), "an end after listening is no boot crash")
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "ShimNotReady")
	clk.Advance(healthPeriod)
	h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "created again once a period after its start")
}

// scenario: asleep-member-reports-no-crash — an asleep member's status carries no CrashLoopBackOff while its sibling
// keeps the crash-looping pool worker up (ADR-0193).
func TestScenarioAsleepMemberReportsNoCrash(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t)
	h.create(t, "a", func(fn *v1.Function) {
		fn.Spec.Pooling.Worker = "asleep"
		fn.Spec.Replicas = 0
	})
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseIdle, h.getFn(t, "a").Status.Phase)
	h.deployHeld(t, "b", "asleep")
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")

	h.reconcile(t, "a")
	a := h.getFn(t, "a")
	require.Equal(t, v1.PhaseIdle, a.Status.Phase)
	for _, c := range a.Status.Conditions {
		require.NotEqual(t, "CrashLoopBackOff", c.Reason, c.Type)
	}
}

// A pool worker's boot count goes with the worker: when a rebuild retires it, when the key loses its last member, and
// when its members scale to zero (ADR-0225 Decision 3).
func TestPoolWorkerCountDroppedOnRetire(t *testing.T) {
	t.Parallel()
	crashed := func(t *testing.T, w string) (*shimHarness, runtime.InstanceID) {
		h, clk := crashHarness(t)
		h.deployHeld(t, "b", w)
		h.bootCrashes(t, clk, "b", 1)
		id := h.poolID(t, w)
		require.Equal(t, 1, function.BootCount(h.r, id))
		return h, id
	}
	t.Run("retired by a rebuild", func(t *testing.T) {
		t.Parallel()
		h, id := crashed(t, "retire")
		h.create(t, "c", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "retire" })
		h.reconcile(t, "b")
		require.True(t, h.rt.wasRemoved(id))
		require.Zero(t, function.BootCount(h.r, id))
	})
	t.Run("last member deleted", func(t *testing.T) {
		t.Parallel()
		h, id := crashed(t, "orphan")
		require.NoError(t, h.st.Delete(t.Context(), v1.KindFunction.GVK(), "default", "b", ""))
		h.reconcile(t, "b")
		require.Empty(t, h.rt.revisionStates(poolOf("orphan")), "the pool is reclaimed")
		require.Zero(t, function.BootCount(h.r, id))
	})
	t.Run("scaled to zero", func(t *testing.T) {
		t.Parallel()
		h, id := crashed(t, "zero")
		h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Replicas = 0 })
		h.setPhase(t, "b", v1.PhaseIdle)
		h.reconcile(t, "b")
		require.Zero(t, function.BootCount(h.r, id))
	})
}

// A failed Start goes on the boot count of the crashes before it: after boot crash 2 it is the third, retried 40 s
// later, not at 1 ms or once per period; a later successful Start clears the error and keeps the count, so the next
// boot that never listens is the fourth.
func TestPoolStartFailureSharesBootCount(t *testing.T) {
	t.Parallel()
	h, clk, sf := startFailing(t)
	h.deployHeld(t, "b", "shared")
	h.bootCrashes(t, clk, "b", 2)

	sf.failing.Store(true)
	clk.Advance(bootWait(2) - poolBootTimeout)
	res := h.reconcile(t, "b")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "b").Status.Phase)
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "StartFailed")
	require.Equal(t, 3, function.BootCount(h.r, h.poolID(t, "shared")))
	require.Equal(t, bootWait(3), res.RequeueAfter)

	sf.failing.Store(false)
	clk.Advance(bootWait(3))
	res = h.reconcile(t, "b")
	require.Equal(t, poolBootTimeout, res.RequeueAfter, "a started boot with a count is polled at its boot deadline")
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, crashMessage(4), h.condition(t, "b", "Ready").Message)
}

// The new pool worker's failed Start while the old one serves: b keeps its route on the old pool worker, its sibling
// stays Ready, and the Start is tried again only once its wait has passed, the pass coming back within a period.
func TestPoolStartErrorWhileSwitching(t *testing.T) {
	t.Parallel()
	h, clk, sf := startFailing(t)
	_, oldURL := switchStartFails(t, h, sf)
	up, ready := h.upstream(t, "b")
	require.True(t, ready)
	require.Equal(t, oldURL+"/function/b", up)
	h.reconcile(t, "a")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "a").Status.Phase)

	starts := sf.starts.Load()
	clk.Advance(bootWait(1) - time.Millisecond)
	res := h.reconcile(t, "b")
	require.Equal(t, starts, sf.starts.Load(), "not started again within its wait")
	require.Equal(t, time.Millisecond, res.RequeueAfter)
	clk.Advance(time.Millisecond)
	h.reconcile(t, "b")
	require.Equal(t, starts+1, sf.starts.Load(), "started again once its wait has passed")
}

// A member whose current revision serves (S = C) on an old pool worker while a sibling's redeploy left the current
// manifest's worker booting again with a count: b reads Ready and its pass comes back at that boot deadline, not after
// the period and not at 1 ms (ADR-0225 Decision 3). The old worker's drain poll is a period, so it does not set the
// requeue.
func TestPooledServedMemberPolledAtBootDeadline(t *testing.T) {
	t.Parallel()
	h, clk := crashHarness(t, func(d *function.Deps) { d.DrainPollInterval = healthPeriod })
	old := pooledPair(t, h)
	_, release := h.rt.serveWorker(t, old, "old pool")
	release()
	clk.Advance(time.Millisecond) // the new pool worker is the newer one
	h.rt.setHoldNew(true)
	h.apply(t, "a", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
	h.reconcile(t, "a")
	require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 2)
	clk.Advance(poolBootTimeout)
	h.reconcile(t, "b")
	require.Equal(t, 1, function.BootCount(h.r, h.poolID(t, "w")), "boot crash 1 of the current manifest's worker")

	creates := h.creates()
	clk.Advance(bootWait(1) - poolBootTimeout)
	res := h.reconcile(t, "b")
	require.Equal(t, creates+1, h.creates(), "created again once the wait has passed")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase, "the old pool worker serves b")
	require.Equal(t, b.Status.CurrentRevision, b.Status.ServingRevision)
	require.Equal(t, poolBootTimeout, res.RequeueAfter)

	clk.Advance(400 * time.Millisecond)
	res = h.reconcile(t, "b")
	require.Equal(t, 600*time.Millisecond, res.RequeueAfter, "the rest of the boot timeout")
}
