package function_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0259 tests: a readiness read that runs past probeTimeout counts by the worker's last answer for
// runtime.readinessTimeout; past it the Function reads NotAnswering. A late read here costs the harness's probe timeout.

// readinessWindow is runtime.readinessTimeout's default.
const readinessWindow = 5 * time.Second

func withReadinessTimeout(d time.Duration) func(*function.Deps) {
	return func(deps *function.Deps) { deps.ReadinessTimeout = d }
}

// placed is a Ready Function on the manual-clock health harness: its name, the switch that makes its readiness answers
// late (a solo replica's /health/readiness, a pool host's /health/members), the end of its worker, and Ready's message
// past the window.
type placed struct {
	h       *shimHarness
	clk     *clock.Manual
	name    string
	silence func(bool)
	exit    func()
	message string
}

// deployPlaced deploys a Ready Function solo or pooled.
func deployPlaced(t *testing.T, pooled bool, opts ...func(*function.Deps)) placed {
	t.Helper()
	if pooled {
		h, clk, _ := healthHarness(t, append(opts, withNodePool)...)
		h.create(t, "member", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
		h.reconcile(t, "member")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
		return placed{h: h, clk: clk, name: "member", silence: h.rt.setMembersLate,
			exit:    func() { h.rt.exitRevision(poolOf("w1"), "", 0, runtime.StateFailed, healthPeriod) },
			message: "the pool host has not answered /health/members for 5s"}
	}
	h, clk, _ := healthHarness(t, opts...)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 1)
	return placed{h: h, clk: clk, name: "todo-api", silence: shim.setSilent,
		exit:    func() { h.rt.exit("todo-api", runtime.StateFailed, healthPeriod) },
		message: "a replica has not answered /health/readiness for 5s"}
}

// eachPlacement runs test for a solo and a pooled Function: the rule is the same for both (ADR-0259 Decision 5's
// table).
func eachPlacement(t *testing.T, test func(t *testing.T, p placed)) {
	t.Helper()
	for name, pooled := range map[string]bool{"solo": false, "pooled": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			test(t, deployPlaced(t, pooled))
		})
	}
}

// Issue #1127: one /health/members answer later than probeTimeout no longer flips a Ready pooled member to Degraded
// "a replica exited and is being replaced" and back: the late read counts by the host's last answer, and nothing is
// written.
func TestIssue1127_LateMembersAnswerKeepsReady(t *testing.T) {
	t.Parallel()
	p := deployPlaced(t, true)
	rv := p.h.getFn(t, p.name).ResourceVersion

	p.silence(true)
	p.h.reconcile(t, p.name)
	p.silence(false)
	p.h.reconcile(t, p.name)
	require.Equal(t, rv, p.h.getFn(t, p.name).ResourceVersion, "no status write for one late answer")
	p.h.requireCondition(t, p.name, "Ready", v1.ConditionTrue, "")
}

// scenario: late-answer-keeps-ready — reads with no answer for less than the window keep the Function Ready with no
// status write; each pass comes back by the window's end, and an answer closes the window.
func TestScenarioLateAnswerKeepsReady(t *testing.T) {
	t.Parallel()
	eachPlacement(t, func(t *testing.T, p placed) {
		rv := p.h.getFn(t, p.name).ResourceVersion

		p.silence(true)
		require.Equal(t, readinessWindow, p.h.reconcile(t, p.name).RequeueAfter, "back by the window's end")
		p.clk.Advance(readinessWindow - time.Millisecond)
		require.Equal(t, time.Millisecond, p.h.reconcile(t, p.name).RequeueAfter)
		p.silence(false)
		p.clk.Advance(time.Millisecond)
		require.Equal(t, healthPeriod, p.h.reconcile(t, p.name).RequeueAfter, "an answer closes the window")
		require.Equal(t, rv, p.h.getFn(t, p.name).ResourceVersion, "no status write")
		p.h.requireCondition(t, p.name, "Ready", v1.ConditionTrue, "")

		p.silence(true)
		require.Equal(t, readinessWindow, p.h.reconcile(t, p.name).RequeueAfter, "the next late read opens a new window")
	})
}

// scenario: unanswered-past-window — a check unanswered for the whole window makes the Function Degraded,
// NotAnswering, with its placement's message; an answer makes it Ready again.
func TestScenarioUnansweredPastWindow(t *testing.T) {
	t.Parallel()
	eachPlacement(t, func(t *testing.T, p placed) {
		p.silence(true)
		p.h.reconcile(t, p.name)
		p.clk.Advance(readinessWindow)
		p.h.reconcile(t, p.name)
		require.Equal(t, v1.PhaseDegraded, p.h.getFn(t, p.name).Status.Phase)
		ready := p.h.requireCondition(t, p.name, "Ready", v1.ConditionFalse, "NotAnswering")
		require.Equal(t, p.message, ready.Message)

		p.silence(false)
		p.h.reconcile(t, p.name)
		require.Equal(t, v1.PhaseReady, p.h.getFn(t, p.name).Status.Phase)
		p.h.requireCondition(t, p.name, "Ready", v1.ConditionTrue, "")
	})
}

// A set runtime.readinessTimeout is the window, and Ready's message names it.
func TestReadinessTimeoutSetsTheWindow(t *testing.T) {
	t.Parallel()
	p := deployPlaced(t, true, withReadinessTimeout(2*time.Second))
	p.silence(true)
	require.Equal(t, 2*time.Second, p.h.reconcile(t, p.name).RequeueAfter)
	p.clk.Advance(2 * time.Second)
	p.h.reconcile(t, p.name)
	ready := p.h.requireCondition(t, p.name, "Ready", v1.ConditionFalse, "NotAnswering")
	require.Equal(t, "the pool host has not answered /health/members for 2s", ready.Message)
}

// scenario: exit-seen-at-once — a worker that exits within its window is not covered by its last answer: the next pass
// reports the exit. Its replacement starts with no record, so its late reads keep that message past the window.
func TestScenarioExitSeenAtOnce(t *testing.T) {
	t.Parallel()
	eachPlacement(t, func(t *testing.T, p placed) {
		p.silence(true)
		p.h.reconcile(t, p.name)
		p.exit()
		for range 2 {
			p.h.reconcile(t, p.name)
			require.Equal(t, v1.PhaseDegraded, p.h.getFn(t, p.name).Status.Phase)
			ready := p.h.requireCondition(t, p.name, "Ready", v1.ConditionFalse, "Restarting")
			require.Equal(t, "a replica exited and is being replaced", ready.Message)
			p.clk.Advance(readinessWindow)
		}
	})
}

// A replica that exited and waits out its period before it is created again is judged too: beside a replica late past
// its window, the Function reads Restarting, not NotAnswering, so the exit is not hidden.
func TestExitBesideALateReplicaKeepsRestarting(t *testing.T) {
	t.Parallel()
	h, clk, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 2)
	shim.setSilent(true)
	h.reconcile(t, "todo-api")
	clk.Advance(readinessWindow)
	h.rt.exitRevision("todo-api", "todo-api-1", 1, runtime.StateFailed, time.Second)
	h.reconcile(t, "todo-api")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "todo-api").Status.Phase)
	h.requireCondition(t, "todo-api", "Ready", v1.ConditionFalse, "Restarting")
}

// A full pass while a solo replica's readiness answers late, here after its sibling exited, counts the replica by its
// last answer, so the Function stays Ready; the sibling's replacement, which never answered, is not ready.
func TestLateAnswerKeepsSoloReadyInAFullPass(t *testing.T) {
	t.Parallel()
	h, _, _ := healthHarness(t)
	shim := h.rt.serveShim(t, "todo-api-1")
	h.deployServing(t, "todo-api", 2)
	shim.setSilent(true)
	h.rt.exitRevision("todo-api", "todo-api-1", 1, runtime.StateFailed, healthPeriod)
	h.reconcile(t, "todo-api")
	fn := h.getFn(t, "todo-api")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
}

// A kept answer is never the pool host's liveness (ADR-0259 Decision 2): a host that stops answering both checks is
// restarted runtime.livenessTimeout after its last liveness answer, while its member reads still count by its last
// answer.
func TestKeptAnswerIsNotLiveness(t *testing.T) {
	t.Parallel()
	p := deployPlaced(t, true, withReadinessTimeout(time.Hour))
	host := p.h.rt.servePoolWorker(t, p.h.poolID(t, "w1"))
	p.h.reconcile(t, p.name)
	creates := p.h.creates()

	host.setLive(http.StatusServiceUnavailable)
	p.silence(true)
	for range 3 {
		p.clk.Advance(healthPeriod)
		p.h.reconcile(t, p.name)
	}
	require.Equal(t, creates+1, p.h.creates(), "restarted, though every member read in between was kept")
}

// A held revision follows the window too (ADR-0259 Decision 3): late past it, its Revision reads Degraded,
// NotAnswering, not "a replica exited".
func TestHeldRevisionLatePastWindowIsNotAnswering(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withReadinessTimeout(time.Nanosecond))
	ref := heldAtZero(t, h)
	stall := h.rt.serveStalling(t, "echo-1")
	require.Equal(t, v1.PhaseReady, h.wakeHeld(t, ref).Status.Phase)

	stall()
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	rev := h.revisionEvent(t, "echo-1")
	require.Equal(t, v1.PhaseDegraded, rev.Status.Phase)
	c, ok := rev.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, "NotAnswering", c.Reason)
	require.Equal(t, "a replica has not answered /health/readiness for 1ns", c.Message)
}

// serveReset gives worker id an endpoint that closes every connection with no answer.
func (f *fakeRuntime) serveReset(t *testing.T, id runtime.InstanceID) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	port, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	require.NoError(t, err)
	f.mu.Lock()
	f.idPort[id] = port
	f.mu.Unlock()
}

// A read that fails before probeTimeout, as on a reset connection, is not late: it is judged at once, as before
// (ADR-0259 Decision 1).
func TestResetReadIsJudgedAtOnce(t *testing.T) {
	t.Parallel()
	p := deployPlaced(t, true)
	p.h.rt.serveReset(t, p.h.poolID(t, "w1"))
	p.h.reconcile(t, p.name)
	require.Equal(t, v1.PhaseDegraded, p.h.getFn(t, p.name).Status.Phase)
	p.h.requireCondition(t, p.name, "Ready", v1.ConditionFalse, "Restarting")
}
