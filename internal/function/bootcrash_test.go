package function_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

func sigkill() runtime.Exit { return runtime.Exit{Cause: runtime.ExitBySignal, Signal: 9} }

func exitZero() runtime.Exit { return runtime.Exit{Cause: runtime.ExitByCode, Code: 0} }

// replicaID is the instance of replica i of name's revision of generation gen.
func replicaID(name string, gen int64, i int) runtime.InstanceID {
	return runtime.NewInstanceID("default", v1.ObjectName(name), v1.ObjectName(fmt.Sprintf("%s-%d", name, gen)), i)
}

// end marks instance id as ended on its own with ex, created age ago.
func (f *fakeRuntime) end(id runtime.InstanceID, ex runtime.Exit, age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[id], f.exits[id] = runtime.StateFailed, ex
	f.created[id] = f.clk.Now().Add(-age)
}

// processHarness runs the reconciler on the process driver, every worker running shim as its shim.
func processHarness(t *testing.T, shim string) (*shimHarness, *createCounter) {
	t.Helper()
	rt := &createCounter{Runtime: process.New()}
	t.Cleanup(func() { _ = rt.Close() })
	return newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) {
		d.Runtime = rt
		d.ShimCommand = []string{"sh", "-c", shim}
	}), rt
}

// reconcileUntil reconciles name until its Ready condition has reason, and returns that condition.
func (h *shimHarness) reconcileUntil(t *testing.T, name, reason string) v1.Condition {
	t.Helper()
	var ready v1.Condition
	require.Eventually(t, func() bool {
		h.reconcile(t, name)
		ready = h.condition(t, name, "Ready")
		require.NotEqual(t, v1.PhaseFailed, h.getFn(t, name).Status.Phase, "a boot crash is never Failed")
		return ready.Reason == reason
	}, 5*time.Second, 10*time.Millisecond)
	return ready
}

// scenario: killed-while-booting-is-retried (ADR-0160, issue #74) — a new Function's worker SIGKILLed before it
// listens reads ShapeValid Unknown/NotStarted and Ready=False/CrashLoopBackOff naming signal 9, is re-created after the
// initial wait, and is Ready once it boots.
func TestScenarioKilledWhileBootingIsRetried(t *testing.T) {
	t.Parallel()
	t.Run("fake", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod)
		id := replicaID("killed", 1, 0)
		h.rt.endStarts(id, sigkill(), true)
		h.createFn(t, "killed")

		res := h.reconcile(t, "killed")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "killed").Status.Phase)
		ready := h.condition(t, "killed", "Ready")
		require.Equal(t, v1.ConditionFalse, ready.Status)
		require.Equal(t, "CrashLoopBackOff", ready.Reason)
		require.Contains(t, ready.Message, "replica 0 was killed by signal 9 before it listened")
		sv := h.condition(t, "killed", "ShapeValid")
		require.Equal(t, v1.ConditionUnknown, sv.Status)
		require.Equal(t, "NotStarted", sv.Reason)
		require.Positive(t, res.RequeueAfter)
		require.LessOrEqual(t, res.RequeueAfter, testPeriod, "it comes back when the initial wait ends")

		creates, _ := h.rt.counts()
		h.reconcile(t, "killed")
		again, _ := h.rt.counts()
		require.Equal(t, creates, again, "not re-created inside the wait")

		h.rt.endStarts(id, runtime.Exit{}, false)
		time.Sleep(testPeriod)
		h.reconcile(t, "killed")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after, "re-created after the initial wait")
		require.Equal(t, v1.PhaseReady, h.getFn(t, "killed").Status.Phase)
		ready = h.condition(t, "killed", "Ready")
		require.Equal(t, v1.ConditionTrue, ready.Status)
		require.Empty(t, ready.Reason)
		require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "killed"))
	})
	t.Run("process-driver", func(t *testing.T) {
		t.Parallel()
		h, rt := processHarness(t, "kill -9 $$")
		h.createFn(t, "killed")
		ready := h.reconcileUntil(t, "killed", "CrashLoopBackOff")
		require.Contains(t, ready.Message, "was killed by signal 9 before it listened")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "killed").Status.Phase)
		require.Equal(t, v1.ConditionUnknown, h.shapeValid(t, "killed"))
		require.Eventually(t, func() bool {
			h.reconcile(t, "killed")
			return rt.creates.Load() >= 2
		}, 5*time.Second, 10*time.Millisecond, "the killed worker is created again")
	})
}

// scenario: boot-crash-at-start-is-counted (ADR-0160) — a woken scale-to-zero worker that ends before the creating
// pass lists it leaves the Function Deploying with Ready=False/CrashLoopBackOff, never Idle.
func TestScenarioBootCrashAtStartIsCounted(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.createFn(t, "sleepy")
	fn := h.getFn(t, "sleepy")
	fn.Spec.Replicas = 0
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	h.reconcile(t, "sleepy")
	fn = h.getFn(t, "sleepy")
	h.rt.endStarts(replicaID("sleepy", fn.Generation, 0), sigkill(), true)

	h.setPhase(t, "sleepy", v1.PhaseDeploying)
	res := h.reconcile(t, "sleepy")
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "sleepy").Status.Phase, "never Idle")
	ready := h.condition(t, "sleepy", "Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "CrashLoopBackOff", ready.Reason)
	require.Positive(t, res.RequeueAfter)

	h.reconcile(t, "sleepy")
	require.Equal(t, v1.PhaseDeploying, h.getFn(t, "sleepy").Status.Phase, "a later pass inside the wait stays Deploying")
}

// scenario: boot-crash-beside-ready-replica (ADR-0160) — with replicas: 2, replica 1 SIGKILLed before it listens while
// replica 0 serves leaves the Function Ready with Ready=True/CrashLoopBackOff; replica 1 is re-created at the first pass
// after its wait, and once it listens Ready=True carries no reason.
func TestScenarioBootCrashBesideReadyReplica(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.createFn(t, "pair")
	fn := h.getFn(t, "pair")
	fn.Spec.Replicas = 2
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
	id := replicaID("pair", h.getFn(t, "pair").Generation, 1)
	h.rt.endStarts(id, sigkill(), true)

	res := h.reconcile(t, "pair")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "pair").Status.Phase)
	ready := h.condition(t, "pair", "Ready")
	require.Equal(t, v1.ConditionTrue, ready.Status)
	require.Equal(t, "CrashLoopBackOff", ready.Reason)
	require.True(t, strings.HasPrefix(ready.Message, "replicas 1 ready of 2: replica 1 was killed by signal 9"), ready.Message)
	require.Equal(t, testPeriod, res.RequeueAfter, "the re-create comes at a period pass")

	creates, _ := h.rt.counts()
	h.reconcile(t, "pair")
	again, _ := h.rt.counts()
	require.Equal(t, creates, again, "a pass inside the wait runs in full but does not re-create")

	h.rt.endStarts(id, runtime.Exit{}, false)
	time.Sleep(testPeriod)
	h.reconcile(t, "pair")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "re-created at the first pass after its wait")
	ready = h.condition(t, "pair", "Ready")
	require.Equal(t, v1.ConditionTrue, ready.Status)
	require.Empty(t, ready.Reason, "once it listens, no reason")
	require.Equal(t, 2, h.getFn(t, "pair").Status.Replicas)
}

// scenario: exit-zero-before-listening-backs-off (ADR-0160, issue #140) — a worker that exits 0 before listening at
// every boot is re-created at most once per wait, with Ready=False/CrashLoopBackOff naming code 0, never Failed.
func TestScenarioExitZeroBeforeListeningBacksOff(t *testing.T) {
	t.Parallel()
	t.Run("fake", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod)
		h.rt.endStarts(replicaID("quits", 1, 0), exitZero(), true)
		h.createFn(t, "quits")

		h.reconcile(t, "quits")
		ready := h.condition(t, "quits", "Ready")
		require.Equal(t, "CrashLoopBackOff", ready.Reason)
		require.Contains(t, ready.Message, "replica 0 exited with code 0 before it listened; boot crash 1 in a row")
		creates, _ := h.rt.counts()

		h.reconcile(t, "quits")
		again, _ := h.rt.counts()
		require.Equal(t, creates, again, "at most once per wait")

		time.Sleep(testPeriod)
		h.reconcile(t, "quits")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after)
		fn := h.getFn(t, "quits")
		require.Equal(t, v1.PhaseDeploying, fn.Status.Phase, "never Failed")
		ready = h.condition(t, "quits", "Ready")
		require.Equal(t, "CrashLoopBackOff", ready.Reason)
		require.Contains(t, ready.Message, "boot crash 2 in a row")
	})
	t.Run("process-driver", func(t *testing.T) {
		t.Parallel()
		h, _ := processHarness(t, "exit 0")
		h.createFn(t, "quits")
		ready := h.reconcileUntil(t, "quits", "CrashLoopBackOff")
		require.Contains(t, ready.Message, "exited with code 0 before it listened")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "quits").Status.Phase)
	})
}

// scenario: backoff-resets-after-boot (ADR-0160) — after three crashes and a listen, the next crash waits the initial
// wait again.
func TestScenarioBackoffResetsAfterBoot(t *testing.T) {
	t.Parallel()
	const initial = 10 * time.Millisecond
	h := newShimHarness(t, http.StatusOK, false, withPeriod, func(d *function.Deps) {
		d.BootBackoffInitial, d.BootBackoffMax = initial, time.Second
	})
	id := replicaID("reset", 1, 0)
	h.rt.endStarts(id, sigkill(), true)
	h.createFn(t, "reset")

	for n, wait := range []time.Duration{initial, 2 * initial, 4 * initial} {
		h.reconcile(t, "reset")
		msg := h.condition(t, "reset", "Ready").Message
		require.Contains(t, msg, fmt.Sprintf("boot crash %d in a row, retried %s after its last start", n+1, wait))
		time.Sleep(wait)
	}
	h.rt.endStarts(id, runtime.Exit{}, false)
	h.reconcile(t, "reset")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reset").Status.Phase, "it boots and listens")

	h.rt.endStarts(id, sigkill(), true)
	h.rt.end(id, sigkill(), time.Minute)
	h.reconcile(t, "reset")
	ready := h.condition(t, "reset", "Ready")
	require.Equal(t, "CrashLoopBackOff", ready.Reason, "the replacement crashed while booting")
	require.Contains(t, ready.Message, fmt.Sprintf("boot crash 1 in a row, retried %s after its last start", initial))
}

// scenario: shape-error-stays-failed (ADR-0160) — a shim that exits 3 on the process driver leaves the Function
// Failed (ShapeInvalid) after one create, not restarted, and the port reports ExitByCode 3, not Listened.
func TestScenarioShapeErrorStaysFailed(t *testing.T) {
	t.Parallel()
	h, rt := processHarness(t, "exit 3")
	h.createFn(t, "shape")
	require.Eventually(t, func() bool {
		h.reconcile(t, "shape")
		return h.getFn(t, "shape").Status.Phase == v1.PhaseFailed
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "shape"))
	require.Equal(t, "ShapeInvalid", h.condition(t, "shape", "Ready").Reason)

	in, err := rt.Status(context.Background(), replicaID("shape", 1, 0))
	require.NoError(t, err)
	require.Equal(t, runtime.Exit{Cause: runtime.ExitByCode, Code: 3}, in.Exit)
	require.False(t, in.Listened)

	time.Sleep(testPeriod)
	h.reconcile(t, "shape")
	require.EqualValues(t, 1, rt.creates.Load(), "a shape error is not restarted")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "shape").Status.Phase)
}

// scenario: exit-after-serving-is-replaced (ADR-0160) — a worker that listened and then exits, with any code or signal,
// is replaced once it is one period old, Degraded meanwhile if serving and Deploying otherwise, then Ready; it is never
// ShapeInvalid or CrashLoopBackOff.
func TestScenarioExitAfterServingIsReplaced(t *testing.T) {
	t.Parallel()
	for _, ex := range []runtime.Exit{exitZero(), {Cause: runtime.ExitByCode, Code: 3}, sigkill()} {
		t.Run(fmt.Sprintf("serving/%s-%d-%d", ex.Cause, ex.Code, ex.Signal), func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withPeriod)
			h.deployReady(t, "served")
			creates, _ := h.rt.counts()

			h.rt.end(replicaID("served", 1, 0), ex, 0)
			h.reconcile(t, "served")
			require.Equal(t, v1.PhaseDegraded, h.getFn(t, "served").Status.Phase)
			require.Equal(t, "Restarting", h.condition(t, "served", "Ready").Reason)
			require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "served"))
			again, _ := h.rt.counts()
			require.Equal(t, creates, again, "replaced only once it is one period old")

			time.Sleep(testPeriod)
			h.reconcile(t, "served")
			after, _ := h.rt.counts()
			require.Equal(t, creates+1, after)
			require.Equal(t, v1.PhaseReady, h.getFn(t, "served").Status.Phase)
		})
	}
	t.Run("not-serving", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusServiceUnavailable, false, withPeriod)
		h.createFn(t, "warming")
		h.reconcile(t, "warming")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "warming").Status.Phase)
		creates, _ := h.rt.counts()

		h.rt.end(replicaID("warming", 1, 0), runtime.Exit{Cause: runtime.ExitByCode, Code: 3}, 0)
		h.reconcile(t, "warming")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "warming").Status.Phase)
		require.Equal(t, "ShimNotReady", h.condition(t, "warming", "Ready").Reason, "an exit after listening is not a crash loop")
		require.NotEqual(t, v1.ConditionFalse, h.shapeValid(t, "warming"), "nor a shape failure")
		again, _ := h.rt.counts()
		require.Equal(t, creates, again)

		time.Sleep(testPeriod)
		h.reconcile(t, "warming")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after, "replaced once it is one period old")
	})
}

// scenario: redeploy-killed-while-booting-is-retried (ADR-0160) — the new revision's worker SIGKILLed while booting
// leaves the calls on the serving revision, with RevisionReady=False/CrashLoopBackOff and ShapeValid=Unknown/NotStarted,
// and the calls move once the new worker boots.
func TestScenarioRedeployKilledWhileBootingIsRetried(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "echo")
	id := replicaID("echo", 2, 0)
	h.rt.endStarts(id, sigkill(), true)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "other" })

	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-1", fn.Status.ServingRevision, "the serving revision keeps the calls")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	rr := h.condition(t, "echo", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "CrashLoopBackOff", rr.Reason)
	require.Contains(t, rr.Message, "was killed by signal 9")
	require.Equal(t, fn.Generation, rr.ObservedGeneration)
	sv := h.condition(t, "echo", "ShapeValid")
	require.Equal(t, v1.ConditionUnknown, sv.Status)
	require.Equal(t, "NotStarted", sv.Reason)

	h.rt.endStarts(id, runtime.Exit{}, false)
	time.Sleep(testPeriod)
	require.Eventually(t, func() bool {
		h.reconcile(t, "echo")
		return h.getFn(t, "echo").Status.ServingRevision == "echo-2"
	}, 5*time.Second, 10*time.Millisecond, "the calls move once the new worker boots")
}
