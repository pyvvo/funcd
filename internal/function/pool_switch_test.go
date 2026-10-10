package function_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0224 tests: a rebuilt pool switches to its new worker when every carried-over member reads ready on it, or once
// runtime.bootTimeout has passed since it listened; until then the old worker serves the carried members.

const switchDrainPoll = 3 * time.Second

// switchHarness is crashHarness (a manual clock, a 1 s boot timeout, a 1 ms hand-out settle) with a call tracker on that
// clock and a 3 s drain poll, so a pass's requeue tells the drain poll from the load clock.
func switchHarness(t *testing.T, opts ...func(*function.Deps)) (*shimHarness, *clock.Manual) {
	t.Helper()
	return crashHarness(t, append([]func(*function.Deps){func(d *function.Deps) {
		d.Calls, d.DrainPollInterval = activator.NewCallTracker(d.Clock), switchDrainPoll
	}}, opts...)...)
}

// switchPool is a pool rebuild under test: o, the worker the members were served on, and n, the rebuild's, each with its
// own endpoint.
type switchPool struct {
	o, n   runtime.InstanceID
	os, ns *poolShim
}

// rebuild creates members in pool worker w, serves them on o and redeploys b (redeploy).
func rebuild(t *testing.T, h *shimHarness, clk *clock.Manual, members ...string) switchPool {
	t.Helper()
	for _, name := range members {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	}
	sp := serveOn(t, h, members...)
	return sp.redeploy(t, h, clk, func() {
		h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
		h.reconcile(t, "b")
	})
}

// serveOn reconciles the stored members of pool worker w to Ready on o, which gets its own endpoint.
func serveOn(t *testing.T, h *shimHarness, members ...string) switchPool {
	t.Helper()
	for range 2 {
		for _, name := range members {
			h.reconcile(t, name)
		}
	}
	for _, name := range members {
		require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase, name)
	}
	sp := switchPool{o: h.poolID(t, "w")}
	sp.os = h.rt.servePoolWorker(t, sp.o)
	return sp
}

// redeploy runs change, which rebuilds the pool: n is created held, with its own endpoint, and listens only once the
// test releases it (listen).
func (sp switchPool) redeploy(t *testing.T, h *shimHarness, clk *clock.Manual, change func()) switchPool {
	t.Helper()
	clk.Advance(time.Millisecond)
	h.rt.setHoldNew(true)
	change()
	h.rt.setHoldNew(false)
	sp.n = h.poolID(t, "w")
	require.NotEqual(t, sp.o, sp.n, "the rebuild starts a second pool worker")
	sp.ns = h.rt.servePoolWorker(t, sp.n)
	return sp
}

// listen sets each member's entry on n to state and lets n listen.
func (sp switchPool) listen(h *shimHarness, state string, members ...string) {
	for _, name := range members {
		h.rt.setWorkerMember(sp.n, name, state, "")
	}
	h.rt.hold(sp.n, false)
}

// requireHandedOut requires each member's upstream to be on the worker at url.
func requireHandedOut(t *testing.T, h *shimHarness, url string, members ...string) {
	t.Helper()
	for _, name := range members {
		up, _ := h.upstream(t, name)
		require.Equal(t, url+"/function/"+name, up, name)
	}
}

// requireWaits requires the wait: a and b Ready, b serving b-1 with its current revision booting beside it, and both
// handed out o.
func (sp switchPool) requireWaits(t *testing.T, h *shimHarness) {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		require.Equal(t, v1.PhaseReady, h.getFn(t, name).Status.Phase, name)
		h.requireCondition(t, name, "Ready", v1.ConditionTrue, "")
	}
	b := h.getFn(t, "b")
	require.Equal(t, "b-1", b.Status.ServingRevision)
	require.Equal(t, "b-2", b.Status.CurrentRevision)
	rr := h.requireCondition(t, "b", "RevisionReady", v1.ConditionFalse, "Progressing")
	require.Equal(t, "the current revision is booting beside the serving one", rr.Message)
	requireHandedOut(t, h, sp.os.url, "a", "b")
}

// scenario: rebuild-keeps-old-until-members-ready — while a and b read loading on n, both wait on o, and the pass comes
// back by the drain poll before n listens, then by the rest of the load clock; once both read ready on n, the next pass
// of either moves both to n, b serves b-2, and o is retired once idle for HandOutSettle.
func TestScenarioRebuildKeepsOldUntilMembersReady(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	require.Equal(t, switchDrainPoll, h.reconcile(t, "a").RequeueAfter, "before n listens: the drain poll")

	sp.listen(h, "loading", "a", "b")
	require.Equal(t, poolBootTimeout, h.reconcile(t, "a").RequeueAfter, "n listens: the load clock")
	h.reconcile(t, "b")
	sp.requireWaits(t, h)
	clk.Advance(400 * time.Millisecond)
	require.Equal(t, 600*time.Millisecond, h.reconcile(t, "a").RequeueAfter, "the rest of the load clock")
	sp.requireWaits(t, h)

	sp.listen(h, "ready", "a", "b")
	h.reconcile(t, "a")
	requireHandedOut(t, h, sp.ns.url, "a", "b")
	h.reconcile(t, "b")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, "b-2", b.Status.ServingRevision)
	h.requireCondition(t, "b", "RevisionReady", v1.ConditionTrue, "")
	require.False(t, h.rt.wasRemoved(sp.o), "o was handed out within HandOutSettle")
	clk.Advance(time.Millisecond)
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(sp.o), "o is retired once idle")
}

// scenario: pooled-redeploy-stays-ready-while-loading (#70's pinned case, changed) — a single member b reads ready on o
// and loading on n: b stays Ready, serving b-1; once it reads ready on n, it serves b-2.
func TestScenarioPooledRedeployStaysReadyWhileLoading(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "b")
	sp.listen(h, "loading", "b")
	h.reconcile(t, "b")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	h.requireCondition(t, "b", "Ready", v1.ConditionTrue, "")
	require.Equal(t, "b-1", b.Status.ServingRevision)
	requireHandedOut(t, h, sp.os.url, "b")

	sp.listen(h, "ready", "b")
	h.reconcile(t, "b")
	b = h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, "b-2", b.Status.ServingRevision)
}

// scenario: listen-alone-does-not-switch — n has just listened, with every member ready on it, and no pass has run
// since: a and b are still handed out o.
func TestScenarioListenAloneDoesNotSwitch(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "ready", "a", "b")
	requireHandedOut(t, h, sp.os.url, "a", "b")
}

// scenario: switch-after-load-timeout — a reads ready on n and b loading: once runtime.bootTimeout has passed since n
// first listened, both are handed n, and b follows its entry there: Degraded while loading, CrashLoopBackOff once its
// load timed out.
func TestScenarioSwitchAfterLoadTimeout(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "ready", "a")
	sp.listen(h, "loading", "b")
	require.Equal(t, poolBootTimeout, h.reconcile(t, "a").RequeueAfter)
	clk.Advance(poolBootTimeout - time.Millisecond)
	require.Equal(t, time.Millisecond, h.reconcile(t, "a").RequeueAfter, "the pass comes back at the load timeout")
	h.reconcile(t, "b")
	sp.requireWaits(t, h)

	clk.Advance(time.Millisecond)
	h.reconcile(t, "a")
	requireHandedOut(t, h, sp.ns.url, "a", "b")
	h.reconcile(t, "b")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase, "b loads on n")
	require.Equal(t, "b-1", h.getFn(t, "b").Status.ServingRevision)

	h.rt.setWorkerMember(sp.n, "b", "failed", "load timed out")
	h.reconcile(t, "b")
	h.requireCondition(t, "b", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
}

// scenario: failed-member-holds-switch — b reads failed on n with a shape error: a and b wait on o until
// runtime.bootTimeout has passed since n first listened, then both are handed n.
func TestScenarioFailedMemberHoldsSwitch(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	h.rt.setWorkerMember(sp.n, "b", "failed", "TypeError: handle is not exported")
	sp.listen(h, "ready", "a")
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	clk.Advance(poolBootTimeout - time.Millisecond)
	h.reconcile(t, "b")
	sp.requireWaits(t, h)

	clk.Advance(time.Millisecond)
	h.reconcile(t, "b")
	requireHandedOut(t, h, sp.ns.url, "a", "b")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "b").Status.Phase)
}

// scenario: recreated-new-worker-restarts-load-clock — n listens, stops answering its liveness and is created again
// under its id: a and b wait for a new runtime.bootTimeout from its listen, not from n's first one.
func TestScenarioRecreatedNewWorkerRestartsLoadClock(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t, func(d *function.Deps) { d.BootTimeout = time.Minute })
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "ready", "a")
	sp.listen(h, "loading", "b")
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	sp.requireWaits(t, h)

	sp.ns.setLive(503)
	clk.Advance(healthLiveness)
	creates := h.creates()
	h.reconcile(t, "a")
	require.Equal(t, creates+1, h.creates(), "n is created again")
	require.Equal(t, sp.n, h.poolID(t, "w"), "under its id")
	sp.ns.setLive(200)
	clk.Advance(time.Minute - healthLiveness)
	h.reconcile(t, "a")
	h.reconcile(t, "b")
	sp.requireWaits(t, h)

	clk.Advance(healthLiveness - time.Millisecond)
	h.reconcile(t, "a")
	requireHandedOut(t, h, sp.os.url, "a", "b")
	clk.Advance(time.Millisecond)
	h.reconcile(t, "a")
	requireHandedOut(t, h, sp.ns.url, "a", "b")
}

// scenario: added-and-removed-members — the rebuild adds c and removes d: only a and b are awaited, c is handed n as
// soon as it listens and turns Ready on its own entry, and d, which n does not hold, does not hold the switch.
func TestScenarioAddedAndRemovedMembers(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	for _, name := range []string{"a", "b", "d"} {
		h.create(t, name, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	}
	sp := serveOn(t, h, "a", "b", "d").redeploy(t, h, clk, func() {
		h.create(t, "c", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
		h.apply(t, "d", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w2" })
		h.reconcile(t, "a")
	})
	sp.listen(h, "loading", "a", "b")

	h.reconcile(t, "a")
	h.reconcile(t, "c")
	requireHandedOut(t, h, sp.os.url, "a", "b")
	requireHandedOut(t, h, sp.ns.url, "c")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "c").Status.Phase, "c is judged on n")

	sp.listen(h, "ready", "a", "b")
	h.reconcile(t, "a")
	requireHandedOut(t, h, sp.ns.url, "a", "b", "c")
}

// scenario: member-left-out-keeps-old — during the wait a's artifact cannot be materialized, so a leaves the manifest:
// a is not awaited and is handed o until b, the one awaited member, reads ready on n.
func TestScenarioMemberLeftOutKeepsOld(t *testing.T) {
	t.Parallel()
	down := &downMaterializer{next: function.NewFileMaterializer(), handler: "handleA"}
	h, clk := switchHarness(t, func(d *function.Deps) { d.Materializer = down })
	h.create(t, "a", func(fn *v1.Function) {
		fn.Spec.Pooling.Worker = "w"
		fn.Spec.Handler = "handleA"
	})
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w" })
	sp := serveOn(t, h, "a", "b").redeploy(t, h, clk, func() {
		down.down.Store(true)
		h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
		h.reconcile(t, "b")
	})
	require.NotContains(t, h.poolManifest(t, poolOf("w")), `"name":"a"`, "a left the manifest")
	sp.listen(h, "loading", "b")
	h.reconcile(t, "b")
	requireHandedOut(t, h, sp.os.url, "a", "b")

	sp.listen(h, "ready", "b")
	h.reconcile(t, "b")
	requireHandedOut(t, h, sp.ns.url, "a", "b")
}

// scenario: old-worker-gone-switches — o exits while b reads loading on n: b is handed n at once, the next pass
// switches, and b is Degraded until it reads ready on n.
func TestScenarioOldWorkerGoneSwitches(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "ready", "a")
	sp.listen(h, "loading", "b")
	h.reconcile(t, "b")
	sp.requireWaits(t, h)

	h.rt.end(sp.o, sigkill(), 0)
	requireHandedOut(t, h, sp.ns.url, "a", "b")
	h.reconcile(t, "b")
	require.True(t, h.rt.wasRemoved(sp.o))
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "b").Status.Phase)
	requireHandedOut(t, h, sp.ns.url, "a", "b")

	sp.listen(h, "ready", "b")
	h.reconcile(t, "b")
	b := h.getFn(t, "b")
	require.Equal(t, v1.PhaseReady, b.Status.Phase)
	require.Equal(t, "b-2", b.Status.ServingRevision)
}

// scenario: reclaim-during-wait — every member goes idle during the wait: o and n are reclaimed and the record ends,
// so the next call wakes n alone, which is handed out with no wait.
func TestScenarioReclaimDuringWait(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "loading", "a", "b")
	h.reconcile(t, "a")

	for _, name := range []string{"a", "b"} {
		h.apply(t, name, func(fn *v1.Function) { fn.Spec.Replicas = 0 })
		h.setPhase(t, name, v1.PhaseIdle)
	}
	h.reconcile(t, "a")
	require.True(t, h.rt.wasRemoved(sp.o), "o is reclaimed")
	require.Equal(t, runtime.StateStopped, h.rt.stateOf(sp.n), "n is reclaimed")

	clk.Advance(healthPeriod)
	h.setPhase(t, "b", v1.PhaseDeploying)
	h.reconcile(t, "b")
	require.Equal(t, []runtime.InstanceID{sp.n}, h.rt.poolWorkers("default", poolOf("w")))
	require.Equal(t, runtime.StateRunning, h.rt.stateOf(sp.n), "the call wakes n")
	requireHandedOut(t, h, sp.ns.url, "b")
}

// After a restart of funcd the record is made again from the runtime (ADR-0224 Decision 2), with no carried member:
// the first pass switches when n already listens, and while n does not listen o is handed out until n listens.
func TestPoolSwitchAfterRestart(t *testing.T) {
	t.Parallel()
	restarted := func(t *testing.T, h *shimHarness) *shimHarness {
		r2, err := function.NewReconciler(h.deps)
		require.NoError(t, err)
		t.Cleanup(func() { _ = r2.Close() })
		return &shimHarness{r: r2, st: h.st, rt: h.rt, gw: h.gw, artifact: h.artifact, deps: h.deps}
	}
	t.Run("n listens", func(t *testing.T) {
		t.Parallel()
		h, clk := switchHarness(t)
		sp := rebuild(t, h, clk, "a", "b")
		sp.listen(h, "loading", "b")
		r2 := restarted(t, h)
		r2.reconcile(t, "a")
		requireHandedOut(t, r2, sp.ns.url, "a", "b")
		r2.reconcile(t, "b")
		requireHandedOut(t, r2, sp.ns.url, "a", "b")
		clk.Advance(time.Millisecond)
		r2.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.o), "o drains from the restart's switch")
	})
	t.Run("n does not listen yet", func(t *testing.T) {
		t.Parallel()
		h, clk := switchHarness(t)
		sp := rebuild(t, h, clk, "a", "b")
		r2 := restarted(t, h)
		r2.reconcile(t, "a")
		requireHandedOut(t, r2, sp.os.url, "a", "b")
		sp.listen(h, "loading", "b")
		requireHandedOut(t, r2, sp.ns.url, "a", "b")
		r2.reconcile(t, "a")
		requireHandedOut(t, r2, sp.ns.url, "a", "b")
	})
}

// A rebuild during the wait keeps the worker the record hands out (ADR-0224 Decision 2): before the switch o keeps the
// carried members until the third manifest's worker n2 switches, and n, never handed out, is retired once n2 listens;
// after a switch to n, n keeps them, and o is retired once idle without waiting for n2's switch.
func TestPoolSwitchRebuildDuringWait(t *testing.T) {
	t.Parallel()
	rebuildAgain := func(t *testing.T, h *shimHarness, clk *clock.Manual) (runtime.InstanceID, *poolShim) {
		t.Helper()
		clk.Advance(time.Millisecond)
		h.rt.setHoldNew(true)
		h.apply(t, "b", func(fn *v1.Function) { fn.Spec.Image = otherArtifact(t) })
		h.reconcile(t, "b")
		h.rt.setHoldNew(false)
		n2 := h.poolID(t, "w")
		require.Len(t, h.rt.poolWorkers("default", poolOf("w")), 3)
		shim := h.rt.servePoolWorker(t, n2)
		for _, name := range []string{"a", "b"} {
			h.rt.setWorkerMember(n2, name, "loading", "")
		}
		return n2, shim
	}
	t.Run("before the switch", func(t *testing.T) {
		t.Parallel()
		h, clk := switchHarness(t)
		sp := rebuild(t, h, clk, "a", "b")
		sp.listen(h, "loading", "a", "b")
		h.reconcile(t, "a")
		n2, n2s := rebuildAgain(t, h, clk)
		require.False(t, h.rt.wasRemoved(sp.n), "n is kept while n2 does not listen")
		requireHandedOut(t, h, sp.os.url, "a", "b")

		h.rt.hold(n2, false)
		h.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.n), "n, never handed out, is retired once n2 listens")
		require.False(t, h.rt.wasRemoved(sp.o))
		requireHandedOut(t, h, sp.os.url, "a", "b")

		for _, name := range []string{"a", "b"} {
			h.rt.setWorkerMember(n2, name, "ready", "")
		}
		h.reconcile(t, "a")
		requireHandedOut(t, h, n2s.url, "a", "b")
		clk.Advance(time.Millisecond)
		h.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.o), "o is retired once idle after n2's switch")
	})
	t.Run("after a switch", func(t *testing.T) {
		t.Parallel()
		h, clk := switchHarness(t)
		sp := rebuild(t, h, clk, "a", "b")
		sp.listen(h, "ready", "a", "b")
		requireHandedOut(t, h, sp.os.url, "a")
		h.reconcile(t, "a")
		requireHandedOut(t, h, sp.ns.url, "a", "b")
		require.False(t, h.rt.wasRemoved(sp.o), "o was handed out within HandOutSettle")

		n2, _ := rebuildAgain(t, h, clk)
		h.rt.hold(n2, false)
		h.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.o), "o is retired once idle without waiting for n2's switch")
		require.False(t, h.rt.wasRemoved(sp.n), "n keeps the carried members until n2's switch")
		requireHandedOut(t, h, sp.ns.url, "a", "b")
	})
}

// The resolver reads the record and never probes (ADR-0224 Decision 4): handing out a and b during the wait makes no
// /health/members call on either worker.
func TestServingPoolMakesNoProbe(t *testing.T) {
	t.Parallel()
	h, clk := switchHarness(t)
	sp := rebuild(t, h, clk, "a", "b")
	sp.listen(h, "loading", "a", "b")
	h.reconcile(t, "a")
	o, n := sp.os.membersCalls(), sp.ns.membersCalls()
	for range 5 {
		requireHandedOut(t, h, sp.os.url, "a", "b")
	}
	require.Equal(t, o, sp.os.membersCalls())
	require.Equal(t, n, sp.ns.membersCalls())
}

// heldCall carries a call that stays in flight until its gate closes.
type heldCall struct{ gate chan struct{} }

func (c heldCall) RoundTrip(req *http.Request) (*http.Response, error) {
	<-c.gate
	return http.DefaultTransport.RoundTrip(req)
}

// The drain clock of the worker the record handed out starts at the switch, not at the load clock (ADR-0224 Decision
// 5): with runtime.drainGrace (30 s) below runtime.bootTimeout (1 min), as the defaults are, a switch at the load
// timeout keeps o while a call to it is in flight, and retires it once the call has ended or drainGrace has passed
// since the switch.
func TestPoolSwitchDrainsFromTheSwitch(t *testing.T) {
	t.Parallel()
	const drainGrace = 30 * time.Second
	switchedWithCall := func(t *testing.T) (*shimHarness, *clock.Manual, switchPool, func() string) {
		t.Helper()
		var calls *activator.CallTracker
		h, clk := switchHarness(t, func(d *function.Deps) {
			d.BootTimeout, d.DrainGrace, calls = time.Minute, drainGrace, d.Calls
		})
		sp := rebuild(t, h, clk, "a", "b")
		call := heldCall{gate: make(chan struct{})}
		release := sync.OnceFunc(func() { close(call.gate) })
		t.Cleanup(release)
		answer := callThrough(t, calls, call, sp.os.url)
		sp.listen(h, "ready", "a")
		sp.listen(h, "loading", "b")
		h.reconcile(t, "a")
		sp.requireWaits(t, h)

		clk.Advance(time.Minute)
		h.reconcile(t, "a")
		requireHandedOut(t, h, sp.ns.url, "a", "b")
		require.False(t, h.rt.wasRemoved(sp.o), "the call in flight holds o at the switch")
		return h, clk, sp, func() string {
			release()
			return answer()
		}
	}
	t.Run("drainGrace from the switch", func(t *testing.T) {
		t.Parallel()
		h, clk, sp, _ := switchedWithCall(t)
		clk.Advance(drainGrace - time.Millisecond)
		h.reconcile(t, "a")
		require.False(t, h.rt.wasRemoved(sp.o), "within drainGrace of the switch")
		clk.Advance(time.Millisecond)
		h.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.o), "drainGrace after the switch")
	})
	t.Run("idle after the call", func(t *testing.T) {
		t.Parallel()
		h, clk, sp, end := switchedWithCall(t)
		require.NotContains(t, end(), "error")
		clk.Advance(time.Millisecond)
		h.reconcile(t, "a")
		require.True(t, h.rt.wasRemoved(sp.o), "o is retired once the call has ended and HandOutSettle has passed")
	})
}
