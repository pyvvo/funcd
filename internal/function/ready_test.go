package function_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
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
)

// ADR-0161 tests: a failed pass writes the status, and only listening workers count as ready and get calls.

// createFailer fails every Create while failing is set, with a counter in its message, so each failure differs.
type createFailer struct {
	runtime.Runtime
	failing atomic.Bool
	n       atomic.Int32
}

func (c *createFailer) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	if c.failing.Load() {
		return runtime.Instance{}, fault.Unavailablef("test.Create", "create failed (attempt %d)", c.n.Add(1))
	}
	return c.Runtime.Create(ctx, spec)
}

func (c *createFailer) wrap(d *function.Deps) { c.Runtime, d.Runtime = d.Runtime, c }

// runtimeFailer fails every List and Status while failing is set, as an unreachable runtime does; with onlyFrom set,
// only those called from that function.
type runtimeFailer struct {
	runtime.Runtime
	failing  atomic.Bool
	onlyFrom string
}

func (f *runtimeFailer) fails() bool {
	return f.failing.Load() && (f.onlyFrom == "" || calledFrom(f.onlyFrom))
}

func (f *runtimeFailer) List(ctx context.Context, ns v1.NamespaceName) ([]runtime.Instance, error) {
	if f.fails() {
		return nil, fault.Unavailablef("test.List", "the runtime is unreachable")
	}
	return f.Runtime.List(ctx, ns)
}

func (f *runtimeFailer) Status(ctx context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	if f.fails() {
		return runtime.Instance{}, fault.Unavailablef("test.Status", "the runtime is unreachable")
	}
	return f.Runtime.Status(ctx, id)
}

func (f *runtimeFailer) wrap(d *function.Deps) { f.Runtime, d.Runtime = d.Runtime, f }

// withManualClock runs the reconciler and the fake runtime on clk with ADR-0160's boot backoff bounds.
func withManualClock(clk *clock.Manual, initial, limit time.Duration) func(*function.Deps) {
	return func(d *function.Deps) {
		d.SupervisionPeriod, d.HandOutSettle = testPeriod, time.Millisecond
		d.Clock, d.BootBackoffInitial, d.BootBackoffMax = clk, initial, limit
	}
}

// tryReconcile runs one pass over name and returns its result and error.
func (h *shimHarness) tryReconcile(name string) (controller.Result, error) {
	return h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
}

// requireCondition asserts name's condition typ has status and reason.
func (h *shimHarness) requireCondition(t *testing.T, name string, typ v1.ConditionType, status v1.ConditionStatus, reason string) v1.Condition {
	t.Helper()
	c := h.condition(t, name, typ)
	require.Equal(t, status, c.Status, typ)
	require.Equal(t, reason, c.Reason, typ)
	return c
}

// shimURL is the upstream of a listening fake worker.
func (h *shimHarness) shimURL() string { return "http://" + h.rt.ip + ":" + strconv.Itoa(h.rt.port) }

// restartWithoutArtifact simulates a daemon restart after which name's artifact is gone.
func (h *shimHarness) restartWithoutArtifact(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile(h.artifact)
	require.NoError(t, err)
	require.NoError(t, os.Remove(h.artifact))
	h.rt.forget()
	return src
}

// requireNotServing asserts name is phase with Ready and RevisionReady False with reason, no replica and no route.
func (h *shimHarness) requireNotServing(t *testing.T, name string, phase v1.Phase, reason string) {
	t.Helper()
	fn := h.getFn(t, name)
	require.Equal(t, phase, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas)
	ready := h.requireCondition(t, name, "Ready", v1.ConditionFalse, reason)
	rr := h.requireCondition(t, name, "RevisionReady", v1.ConditionFalse, reason)
	require.NotEmpty(t, ready.Message)
	require.Equal(t, ready.Message, rr.Message)
	require.Empty(t, h.routes(t))
}

// scenario: restart-missing-artifact-not-ready
func TestScenarioRestartMissingArtifactNotReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "echo")
	h.restartWithoutArtifact(t)

	_, err := h.tryReconcile("echo")
	require.Error(t, err, "the pass is retried")
	h.requireNotServing(t, "echo", v1.PhaseDegraded, "StartFailed")
	require.Contains(t, h.condition(t, "echo", "Ready").Message, "materialize artifact")
	_, ready := h.upstream(t, "echo")
	require.False(t, ready)
}

// scenario: registry-outage-not-ready
func TestScenarioRegistryOutageNotReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	outage := func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage }

	h.create(t, "fresh", outage)
	_, err := h.tryReconcile("fresh")
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	fn := h.getFn(t, "fresh")
	require.Empty(t, fn.Status.Phase, "a new Function keeps its empty phase")
	require.Zero(t, fn.Status.Replicas)
	h.requireCondition(t, "fresh", "Ready", v1.ConditionFalse, "ReconcileFailed")
	h.requireCondition(t, "fresh", "RevisionReady", v1.ConditionFalse, "ReconcileFailed")

	h.create(t, "serving", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "serving")
	h.apply(t, "serving", outage)
	_, err = h.tryReconcile("serving")
	require.Error(t, err)
	fn = h.getFn(t, "serving")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "a listening worker keeps it Ready")
	require.Equal(t, 1, fn.Status.Replicas)
	h.requireCondition(t, "serving", "Ready", v1.ConditionTrue, "")
	h.requireCondition(t, "serving", "RevisionReady", v1.ConditionFalse, "ReconcileFailed")

	h.create(t, "dead", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "dead")
	h.rt.exit("dead", runtime.StateFailed, time.Minute)
	h.apply(t, "dead", outage)
	_, err = h.tryReconcile("dead")
	require.Error(t, err)
	fn = h.getFn(t, "dead")
	require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
	require.Zero(t, fn.Status.Replicas)
	h.requireCondition(t, "dead", "Ready", v1.ConditionFalse, "ReconcileFailed")
}

// scenario: failed-replacement-not-ready
func TestScenarioFailedReplacementNotReady(t *testing.T) {
	t.Parallel()
	t.Run("only-replica", func(t *testing.T) {
		t.Parallel()
		cf := &createFailer{}
		h := newShimHarness(t, http.StatusOK, false, withPeriod, cf.wrap)
		h.deployReady(t, "echo")
		h.rt.exit("echo", runtime.StateFailed, time.Minute)
		cf.failing.Store(true)

		_, err := h.tryReconcile("echo")
		require.Error(t, err)
		h.requireNotServing(t, "echo", v1.PhaseDegraded, "StartFailed")
	})
	t.Run("one-of-two", func(t *testing.T) {
		t.Parallel()
		cf := &createFailer{}
		h := newShimHarness(t, http.StatusOK, false, withPeriod, cf.wrap)
		h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
		h.reconcile(t, "echo")
		require.Equal(t, 2, h.getFn(t, "echo").Status.Replicas)
		h.rt.exitRevision("echo", "echo-1", 1, runtime.StateFailed, time.Minute)
		cf.failing.Store(true)

		_, err := h.tryReconcile("echo")
		require.Error(t, err)
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseReady, fn.Status.Phase)
		require.Equal(t, 1, fn.Status.Replicas, "only replica 0 listens")
		h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "")
		h.requireCondition(t, "echo", "RevisionReady", v1.ConditionFalse, "StartFailed")
		up, ready := h.upstream(t, "echo")
		require.True(t, ready)
		require.Equal(t, h.shimURL(), up, "replica 0 answers")
	})
}

// scenario: failing-pass-writes-once
func TestScenarioFailingPassWritesOnce(t *testing.T) {
	t.Parallel()
	cf := &createFailer{}
	h := newShimHarness(t, http.StatusOK, false, withPeriod, cf.wrap)
	h.deployReady(t, "echo")
	h.rt.exit("echo", runtime.StateFailed, time.Minute)
	cf.failing.Store(true)

	rv := h.getFn(t, "echo").ResourceVersion
	versions := map[string]bool{}
	for range 10 {
		_, err := h.tryReconcile("echo")
		require.Error(t, err)
		versions[h.getFn(t, "echo").ResourceVersion] = true
	}
	require.Equal(t, int32(10), cf.n.Load(), "every pass retried the Create")
	require.Len(t, versions, 1, "ten failures with one reason write the status once")
	require.False(t, versions[rv], "the first failure is written")
	require.Contains(t, h.condition(t, "echo", "Ready").Message, "attempt 1)", "the status shows the first error")
}

// scenario: runtime-outage-keeps-routes
func TestScenarioRuntimeOutageKeepsRoutes(t *testing.T) {
	t.Parallel()
	rf := &runtimeFailer{}
	h := newShimHarness(t, http.StatusOK, false, withPeriod, rf.wrap, func(d *function.Deps) {
		d.Resolver = resolverFailing{bad: "oci://example/missing:v2"}
	})
	h.deployReady(t, "a")
	h.deployReady(t, "b")
	require.Len(t, h.routes(t), 2)
	before := h.getFn(t, "a").Status

	rf.failing.Store(true)
	_, err := h.tryReconcile("a")
	require.Error(t, err)
	fn := h.getFn(t, "a")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, before.Replicas, fn.Status.Replicas)
	h.requireCondition(t, "a", "Ready", v1.ConditionTrue, "")
	h.requireCondition(t, "a", "RevisionReady", v1.ConditionFalse, "ReconcileFailed")
	require.Len(t, h.routes(t), 2, "a runtime outage drops no route")

	// A new Function lists the runtime before its gates, so the gate fails while the outage hits the route table.
	rf.onlyFrom = ".programAllRoutes"
	h.create(t, "c", func(fn *v1.Function) { fn.Spec.Image = "oci://example/missing:v2" })
	_, err = h.tryReconcile("c")
	require.Error(t, err, "the routes could not be programmed")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "c").Status.Phase)
	h.requireCondition(t, "c", "Ready", v1.ConditionFalse, "ArtifactUnresolved")
	require.Len(t, h.routes(t), 2, "a gate failing in an outage drops no route")
}

// scenario: hung-replica-never-handed-out
func TestScenarioHungReplicaNeverHandedOut(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.rt.hold(replicaID("echo", 1, 0), true)
	h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
	h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	for range 200 {
		up, ready := h.upstream(t, "echo")
		require.True(t, ready)
		require.Equal(t, h.shimURL(), up, "every call gets replica 1, never the lower replica 0 that does not listen")
	}
	routes := h.routes(t)
	require.Len(t, routes, 1)
	require.Equal(t, h.shimURL(), routes[0].Upstream)
}

// scenario: hung-replica-replaced-with-growing-wait
func TestScenarioHungReplicaReplacedWithGrowingWait(t *testing.T) {
	t.Parallel()
	clk := clock.NewManual(time.Now())
	h := newShimHarness(t, http.StatusOK, false, withManualClock(clk, 2*time.Minute, 8*time.Minute))
	h.rt.hold(replicaID("echo", 1, 1), true)
	h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
	h.reconcile(t, "echo")
	require.Equal(t, 1, h.getFn(t, "echo").Status.Replicas)

	clk.Advance(function.BootTimeout)
	h.reconcile(t, "echo")
	require.Equal(t, runtime.StateStopped, h.rt.revisionStates("echo")["echo-1"][1], "replica 1 is stopped at the boot timeout")
	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, 1, fn.Status.Replicas)
	ready := h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "CrashLoopBackOff")
	require.Equal(t, "replicas 1 ready of 2: replica 1 did not listen within 1m0s; boot crash 1 in a row, retried 2m0s after its last start", ready.Message)

	creates, _ := h.rt.counts()
	clk.Advance(time.Minute - time.Second)
	h.reconcile(t, "echo")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "not re-created before 2 min after its creation")

	clk.Advance(time.Second)
	h.reconcile(t, "echo")
	after, _ = h.rt.counts()
	require.Equal(t, creates+1, after, "re-created 2 min after its creation")

	clk.Advance(function.BootTimeout)
	h.reconcile(t, "echo")
	require.Equal(t, "replicas 1 ready of 2: replica 1 did not listen within 1m0s; boot crash 2 in a row, retried 4m0s after its last start",
		h.condition(t, "echo", "Ready").Message)
}

// scenario: first-boot-never-listening-is-retried
func TestScenarioFirstBootNeverListeningIsRetried(t *testing.T) {
	t.Parallel()
	t.Run("first-boot", func(t *testing.T) {
		t.Parallel()
		clk := clock.NewManual(time.Now())
		h := newShimHarness(t, http.StatusOK, false, withManualClock(clk, 0, 0))
		h.rt.hold(replicaID("hang", 1, 0), true)
		h.createFn(t, "hang")
		res := h.reconcile(t, "hang")
		require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a first boot attempt is polled")

		clk.Advance(function.BootTimeout)
		h.reconcile(t, "hang")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "hang").Status.Phase, "never Failed")
		ready := h.requireCondition(t, "hang", "Ready", v1.ConditionFalse, "CrashLoopBackOff")
		require.Equal(t, "replica 0 did not listen within 1m0s; boot crash 1 in a row, retried 1m0s after its last start", ready.Message)
		h.requireCondition(t, "hang", "ShapeValid", v1.ConditionUnknown, "NotStarted")

		creates, _ := h.rt.counts()
		res = h.reconcile(t, "hang")
		after, _ := h.rt.counts()
		require.Equal(t, creates+1, after, "re-created at once")
		require.Equal(t, testPeriod, res.RequeueAfter, "the re-created replica is not polled")
		require.Equal(t, v1.PhaseDeploying, h.getFn(t, "hang").Status.Phase)
		require.NotEqual(t, v1.ConditionFalse, h.condition(t, "hang", "ShapeValid").Status)
	})
	t.Run("beside-serving", func(t *testing.T) {
		t.Parallel()
		clk := clock.NewManual(time.Now())
		h := newShimHarness(t, http.StatusOK, false, withManualClock(clk, 0, 0))
		h.deployReady(t, "echo")
		h.rt.hold(replicaID("echo", 2, 0), true)
		h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "hang" })
		h.reconcile(t, "echo")
		clk.Advance(function.BootTimeout)
		h.reconcile(t, "echo")

		require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
		h.requireCondition(t, "echo", "RevisionReady", v1.ConditionFalse, "CrashLoopBackOff")
		h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "")
		_, ready := h.upstream(t, "echo")
		require.True(t, ready, "the serving revision keeps the calls")
	})
}

// scenario: ready-replica-serves-meanwhile
func TestScenarioReadyReplicaServesMeanwhile(t *testing.T) {
	t.Parallel()
	requireServes := func(t *testing.T, h *shimHarness) {
		t.Helper()
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseReady, fn.Status.Phase)
		require.Equal(t, 1, fn.Status.Replicas)
		ready := h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "CrashLoopBackOff")
		require.Contains(t, ready.Message, "replicas 1 ready of 2: replica 1 ")
		for range 20 {
			up, ok := h.upstream(t, "echo")
			require.True(t, ok)
			require.Equal(t, h.shimURL(), up, "replica 0 answers")
		}
	}
	t.Run("hang", func(t *testing.T) {
		t.Parallel()
		clk := clock.NewManual(time.Now())
		h := newShimHarness(t, http.StatusOK, false, withManualClock(clk, 2*time.Minute, 8*time.Minute))
		h.rt.hold(replicaID("echo", 1, 1), true)
		h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
		h.reconcile(t, "echo")
		clk.Advance(function.BootTimeout)
		h.reconcile(t, "echo")
		requireServes(t, h)
	})
	t.Run("killed", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.rt.endStarts(replicaID("echo", 1, 1), sigkill(), true)
		h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
		h.reconcile(t, "echo")
		requireServes(t, h)
	})
}

// scenario: recovery-returns-to-ready
func TestScenarioRecoveryReturnsToReady(t *testing.T) {
	t.Parallel()
	t.Run("artifact-back", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withPeriod)
		h.deployReady(t, "echo")
		src := h.restartWithoutArtifact(t)
		_, err := h.tryReconcile("echo")
		require.Error(t, err)
		require.Equal(t, v1.PhaseDegraded, h.getFn(t, "echo").Status.Phase)

		require.NoError(t, os.WriteFile(h.artifact, src, 0o600))
		h.reconcile(t, "echo")
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseReady, fn.Status.Phase)
		require.Equal(t, 1, fn.Status.Replicas)
		h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "")
		h.requireCondition(t, "echo", "RevisionReady", v1.ConditionTrue, "")
		require.Len(t, h.routes(t), 1, "the route is back")
	})
	t.Run("replica-listens", func(t *testing.T) {
		t.Parallel()
		clk := clock.NewManual(time.Now())
		h := newShimHarness(t, http.StatusOK, false, withManualClock(clk, 2*time.Minute, 8*time.Minute))
		hung := replicaID("echo", 1, 1)
		h.rt.hold(hung, true)
		h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
		h.reconcile(t, "echo")
		clk.Advance(function.BootTimeout)
		h.reconcile(t, "echo")
		clk.Advance(time.Minute)
		res := h.reconcile(t, "echo")
		require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][1], "replica 1 is re-created")
		require.Equal(t, testPeriod, res.RequeueAfter)

		h.rt.hold(hung, false)
		h.reconcile(t, "echo")
		fn := h.getFn(t, "echo")
		require.Equal(t, 2, fn.Status.Replicas)
		h.requireCondition(t, "echo", "Ready", v1.ConditionTrue, "")
	})
}

// scenario: call-held-while-not-ready
func TestScenarioCallHeldWhileNotReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	h.deployReady(t, "echo")
	src := h.restartWithoutArtifact(t)
	_, err := h.tryReconcile("echo")
	require.Error(t, err)
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "echo").Status.Phase)

	const hold = 300 * time.Millisecond
	a, err := activator.New(activator.Deps{
		Store: h.st, Endpoints: h.r.Endpoints(), Scaler: storescaler.New(h.st), Clock: clock.System(), ActivationTimeout: hold,
	})
	require.NoError(t, err)
	ref := activator.FunctionRef{Namespace: "default", Name: "echo"}
	begin := time.Now()
	_, err = a.Wake(context.Background(), ref)
	require.Error(t, err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a call never served gets a 503")
	require.GreaterOrEqual(t, time.Since(begin), hold, "the call is held, not refused at once")

	type woken struct {
		up  string
		err error
	}
	done := make(chan woken, 1)
	b, err := activator.New(activator.Deps{
		Store: h.st, Endpoints: h.r.Endpoints(), Scaler: storescaler.New(h.st), Clock: clock.System(), ActivationTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	go func() {
		up, werr := b.Wake(context.Background(), ref)
		done <- woken{up, werr}
	}()
	require.NoError(t, os.WriteFile(h.artifact, src, 0o600))
	require.Eventually(t, func() bool {
		_, _ = h.tryReconcile("echo")
		return h.phaseIs("echo", v1.PhaseReady)
	}, 5*time.Second, 20*time.Millisecond)
	w := <-done
	require.NoError(t, w.err)
	require.Equal(t, h.shimURL(), w.up, "the held call is forwarded once a pass ends Ready")
}

// gateFailed writes the serving revision's listening workers as status.replicas, Degraded while one runs but none
// listens, and the gate's own outcome when none runs (ADR-0161 Decision 2).
func TestGateFailedWritesListeningCount(t *testing.T) {
	t.Parallel()
	broken := func(fn *v1.Function) { fn.Spec.Handler = "" }
	t.Run("listening", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.create(t, "echo", func(fn *v1.Function) { fn.Spec.Replicas = 2 })
		h.reconcile(t, "echo")
		h.rt.hold(replicaID("echo", 1, 1), true)
		h.apply(t, "echo", broken)
		res := h.reconcile(t, "echo")
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseReady, fn.Status.Phase)
		require.Equal(t, 1, fn.Status.Replicas)
		require.Equal(t, testPeriod, res.RequeueAfter)
	})
	t.Run("running-not-listening", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.deployReady(t, "echo")
		h.rt.hold(replicaID("echo", 1, 0), true)
		h.apply(t, "echo", broken)
		res := h.reconcile(t, "echo")
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
		require.Zero(t, fn.Status.Replicas)
		ready := h.requireCondition(t, "echo", "Ready", v1.ConditionFalse, "Restarting")
		require.Equal(t, "no worker of the serving revision listens", ready.Message)
		require.Equal(t, testPeriod, res.RequeueAfter)
	})
	t.Run("none-runs", func(t *testing.T) {
		t.Parallel()
		h := newShimHarness(t, http.StatusOK, false, withSwitch)
		h.deployReady(t, "echo")
		h.rt.exit("echo", runtime.StateFailed, time.Minute)
		h.apply(t, "echo", broken)
		h.reconcile(t, "echo")
		fn := h.getFn(t, "echo")
		require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
		require.Zero(t, fn.Status.Replicas)
		h.requireCondition(t, "echo", "Ready", v1.ConditionFalse, "ShapeInvalid")
	})
}

// A pooled member's pool worker that runs but does not listen is never handed out (ADR-0161 Decision 2).
func TestUnlistenedPoolWorkerIsNotHandedOut(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	h.create(t, "member", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)
	up, ready := h.upstream(t, "member")
	require.True(t, ready)
	require.Equal(t, h.shimURL()+"/function/member", up)

	h.rt.hold(runtime.NewInstanceID("default", poolOf("w1"), "", 0), true)
	up, ready = h.upstream(t, "member")
	require.False(t, ready)
	require.Empty(t, up)
}

// ADR-0161 Decision 2 with ADR-0158: a failed pass counts a pooled member's pool worker only while the member's own
// /health/members entry reads ready.
func TestFailedPassCountsPooledMemberOnlyWhileReady(t *testing.T) {
	t.Parallel()
	rf := &runtimeFailer{onlyFrom: ".ensurePool"}
	h := newShimHarness(t, http.StatusOK, false, withPeriod, withNodePool, rf.wrap)
	h.create(t, "member", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
	h.reconcile(t, "member")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "member").Status.Phase)

	rf.failing.Store(true)
	_, err := h.tryReconcile("member")
	require.Error(t, err)
	fn := h.getFn(t, "member")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the member reads ready, so its pool worker counts")
	require.Equal(t, 1, fn.Status.Replicas)
	h.requireCondition(t, "member", "RevisionReady", v1.ConditionFalse, "StartFailed")

	h.rt.setMember("member", "loading", "")
	_, err = h.tryReconcile("member")
	require.Error(t, err)
	h.requireNotServing(t, "member", v1.PhaseDegraded, "StartFailed")
}

// Issue #309: a serving Function's replacement that listens but never becomes ready is stopped once the Function has
// been Degraded for the boot timeout, and replaced after the backoff.
func TestIssue309_ListenedHungReplicaIsReplaced(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withPeriod)
	_, setStatus := h.rt.serveRevision(t, "stall-1", http.StatusOK)
	h.deployReady(t, "stall")
	h.rt.exit("stall", runtime.StateFailed, time.Minute)
	setStatus(http.StatusServiceUnavailable)
	h.reconcile(t, "stall")
	require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement listens but is not ready")

	h.rt.exitRevision("stall", "stall-1", 0, runtime.StateRunning, time.Hour)
	res := h.reconcile(t, "stall")
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "kept while the Function has been Degraded for less than the boot timeout")

	h.degradedSinceAnHour(t, "stall")
	res = h.reconcile(t, "stall")
	require.Equal(t, testPeriod, res.RequeueAfter, "the pass waits out the backoff instead of re-probing the hung replica")
	require.Contains(t, h.condition(t, "stall", "Ready").Message, "did not become ready")
	require.Equal(t, v1.ConditionTrue, h.shapeValid(t, "stall"))

	creates, _ := h.rt.counts()
	setStatus(http.StatusOK)
	h.reconcile(t, "stall")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "the hung replacement is replaced")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "stall").Status.Phase)
}

// ADR-0161 Decisions 1 and 2: a failed pass or gate leaves a Degraded Function Degraded with no replica even while a
// worker of its serving revision listens; only finish makes it Ready.
func TestDegradedWithListeningReplicaStaysDegraded(t *testing.T) {
	t.Parallel()
	degraded := func(t *testing.T) *shimHarness {
		t.Helper()
		h := newShimHarness(t, http.StatusOK, false, withPeriod, withPlatforms(&fakePlatforms{}))
		_, setStatus := h.rt.serveRevision(t, "stall-1", http.StatusOK)
		h.create(t, "stall", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
		h.reconcile(t, "stall")
		h.rt.exit("stall", runtime.StateFailed, time.Minute)
		setStatus(http.StatusServiceUnavailable)
		h.reconcile(t, "stall")
		require.Equal(t, v1.PhaseDegraded, h.getFn(t, "stall").Status.Phase, "the replacement listens but is not ready")
		return h
	}
	requireDegraded := func(t *testing.T, h *shimHarness) {
		t.Helper()
		fn := h.getFn(t, "stall")
		require.Equal(t, v1.PhaseDegraded, fn.Status.Phase)
		require.Zero(t, fn.Status.Replicas)
		require.Equal(t, v1.ConditionFalse, h.condition(t, "stall", "Ready").Status)
	}
	t.Run("failed-pass", func(t *testing.T) {
		t.Parallel()
		h := degraded(t)
		h.apply(t, "stall", func(fn *v1.Function) { fn.Spec.ImageDigest = digestOutage })
		_, err := h.tryReconcile("stall")
		require.Error(t, err)
		requireDegraded(t, h)
		h.requireCondition(t, "stall", "Ready", v1.ConditionFalse, "ReconcileFailed")
	})
	t.Run("failed-gate", func(t *testing.T) {
		t.Parallel()
		h := degraded(t)
		h.apply(t, "stall", func(fn *v1.Function) { fn.Spec.Handler = "" })
		h.reconcile(t, "stall")
		requireDegraded(t, h)
		h.requireCondition(t, "stall", "RevisionReady", v1.ConditionFalse, "ShapeInvalid")
	})
}
