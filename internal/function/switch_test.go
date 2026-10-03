package function_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0143 tests. A Function's revisions are named <name>-<generation>.

func withSwitch(d *function.Deps) {
	d.SupervisionPeriod = testPeriod
	d.HandOutSettle = time.Millisecond
}

// settle waits out withSwitch's hand-out settle.
func settle() { time.Sleep(5 * time.Millisecond) }

func (h *shimHarness) apply(t *testing.T, name string, change func(*v1.Function)) {
	t.Helper()
	fn := h.getFn(t, name)
	change(fn)
	_, err := h.st.Update(context.Background(), fn)
	require.NoError(t, err)
}

// create stores a Function like createFn, changed by change first.
func (h *shimHarness) create(t *testing.T, name string, change func(*v1.Function)) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "handle"
	fn.Spec.Image = "file://" + h.artifact
	change(fn)
	_, err := h.st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func (h *shimHarness) upstream(t *testing.T, name string) (string, bool) {
	t.Helper()
	up, ready, err := h.r.Endpoints().Upstream(context.Background(), activator.FunctionRef{Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
	return up, ready
}

func (h *shimHarness) condition(t *testing.T, name string, typ v1.ConditionType) v1.Condition {
	t.Helper()
	c, ok := h.getFn(t, name).Status.Conditions.Get(typ)
	require.True(t, ok, "condition %s is set", typ)
	return c
}

func (f *fakeRuntime) specOf(id runtime.InstanceID) runtime.WorkerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[id]
}

func (f *fakeRuntime) wasRemoved(id runtime.InstanceID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.removed {
		if r == id {
			return true
		}
	}
	return false
}

// serveBlockingRevision gives revision rev a ready endpoint whose calls stay in flight until release is called.
func (f *fakeRuntime) serveBlockingRevision(t *testing.T, rev v1.ObjectName, answer string) (url string, release func()) {
	t.Helper()
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/readiness" {
			w.WriteHeader(http.StatusOK)
			return
		}
		<-gate
		_, _ = io.WriteString(w, answer)
	}))
	released := false
	release = func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(srv.Close)
	t.Cleanup(release)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	f.mu.Lock()
	f.revPort[rev] = port
	f.mu.Unlock()
	return srv.URL, release
}

// callInFlight starts a call through calls to upstream and waits until the tracker counts it; the returned func waits
// for its answer.
func callInFlight(t *testing.T, calls *activator.CallTracker, upstream string) (answer func() string) {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		resp, err := (&http.Client{Transport: calls.Wrap(nil)}).Get(upstream + "/invoke")
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		done <- string(b)
	}()
	require.Eventually(t, func() bool { return !calls.Idle(upstream, 0) }, 2*time.Second, time.Millisecond, "the call is in flight")
	return func() string { return <-done }
}

// scenario: redeploy-switches-to-new-revision (ADR-0143) — a spec change boots revision 2 beside revision 1; the calls
// stay on revision 1 until every revision-2 replica is ready, then move, and revision 1's worker stops and leaves the
// runtime.
func TestScenarioRedeploySwitchesToNewRevision(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	url1, _ := h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
	up, ready := h.upstream(t, "echo")
	require.True(t, ready)
	require.Equal(t, url1, up)

	url2, setReady2 := h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	res := h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.CurrentRevision)
	require.Equal(t, "echo-1", fn.Status.ServingRevision, "revision 1 serves while revision 2 boots")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, "Progressing", h.condition(t, "echo", "RevisionReady").Reason)
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a booting revision is polled")
	up, ready = h.upstream(t, "echo")
	require.True(t, ready)
	require.Equal(t, url1, up, "calls stay on revision 1")

	setReady2(http.StatusOK)
	h.reconcile(t, "echo")
	fn = h.getFn(t, "echo")
	require.Equal(t, "echo-2", fn.Status.ServingRevision, "every revision-2 replica is ready: the switch")
	require.Equal(t, "echo-1", fn.Status.DrainingRevision)
	require.NotNil(t, fn.Status.DrainingSince)
	require.Equal(t, v1.ConditionTrue, h.condition(t, "echo", "RevisionReady").Status)
	up, _ = h.upstream(t, "echo")
	require.Equal(t, url2, up, "calls move to revision 2")
	spec := h.rt.specOf(runtime.NewInstanceID("default", "echo", "echo-2", 0))
	require.Equal(t, "handleV2", spec.Env["FUNCD_HANDLER"], "revision 2 runs the new spec")

	settle()
	h.reconcile(t, "echo")
	require.Empty(t, h.getFn(t, "echo").Status.DrainingRevision, "revision 1 drained")
	require.Nil(t, h.getFn(t, "echo").Status.DrainingSince)
	states := h.rt.revisionStates("echo")
	require.NotContains(t, states, v1.ObjectName("echo-1"), "revision 1's worker left the runtime")
	require.Equal(t, runtime.StateRunning, states["echo-2"][0])
}

// scenario: failed-revision-keeps-old-serving (ADR-0143), boot half — revision 2's handler cannot load; revision 1
// keeps serving, RevisionReady reports ShapeInvalid, and the failed revision is checked every supervision period, never
// retried.
func TestScenarioFailedRevisionKeepsOldServing(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	h.rt.failRevision("echo-2", true)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "broken" })
	h.reconcile(t, "echo")
	res := h.reconcile(t, "echo")

	fn := h.getFn(t, "echo")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "revision 1 keeps serving")
	require.Equal(t, "echo-1", fn.Status.ServingRevision)
	rr := h.condition(t, "echo", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "ShapeInvalid", rr.Reason)
	require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "echo"))
	require.Equal(t, testPeriod, res.RequeueAfter, "a failed revision is checked every supervision period, not polled")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0])
	_, ready := h.upstream(t, "echo")
	require.True(t, ready)

	creates, _ := h.rt.counts()
	h.reconcile(t, "echo")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "the failed revision is not retried")
}

// A new revision whose handler never becomes ready is judged ShapeInvalid after the boot timeout while revision 1
// keeps serving; from then on it is checked every supervision period, not polled (issue #354).
func TestIssue354_TimedOutRevisionIsNotPolled(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	h.rt.hold(runtime.NewInstanceID("default", "echo", "echo-2", 0), true)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "hang" })
	res := h.reconcile(t, "echo")
	require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a revision that just started is polled while it boots")

	h.rt.exitRevision("echo", "echo-2", 0, runtime.StateRunning, time.Hour)
	for range 2 {
		res = h.reconcile(t, "echo")
		require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
		require.Equal(t, "ShapeInvalid", h.condition(t, "echo", "RevisionReady").Reason)
		require.Equal(t, testPeriod, res.RequeueAfter, "a timed-out revision is checked every supervision period, not polled")
	}
}

type resolverFailing struct{ bad string }

func (r resolverFailing) Resolve(_ context.Context, uri string) (string, error) {
	if uri == r.bad {
		return "", fault.NotFoundf("test.resolve", "tag %q not found", uri)
	}
	return "sha256:" + strings.Repeat("a", 64), nil
}

// scenario: failed-revision-keeps-old-serving (ADR-0143), gate half — a revision that fails any gate before converge
// leaves revision 1 serving, with RevisionReady False and the gate's reason.
func TestFailedGateKeepsOldServing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		change func(*v1.Function)
	}{
		{"artifact", "ArtifactUnresolved", func(fn *v1.Function) { fn.Spec.Image = "oci://example/missing:v2" }},
		{"shape", "ShapeInvalid", func(fn *v1.Function) { fn.Spec.Handler = "" }},
		{"secret", "SecretResolveFailed", func(fn *v1.Function) { fn.Spec.Secrets = []v1.ObjectName{"db"} }},
		{"config", "ConfigResolveFailed", func(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"missing"} }},
		{"data-reference", "KVStoreNotFound", func(fn *v1.Function) {
			fn.Spec.KV = []v1.FunctionKV{{Alias: "cache", Store: "nostore", Table: "t"}}
		}},
		{"catalog", "CatalogNotReady", func(fn *v1.Function) {
			fn.Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "nolake"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
				d.Resolver = resolverFailing{bad: "oci://example/missing:v2"}
			})
			h.deployReady(t, "echo")
			h.apply(t, "echo", tc.change)
			res := h.reconcile(t, "echo")

			fn := h.getFn(t, "echo")
			require.Equal(t, v1.PhaseReady, fn.Status.Phase, "revision 1 keeps serving")
			require.Equal(t, "echo-1", fn.Status.ServingRevision)
			rr := h.condition(t, "echo", "RevisionReady")
			require.Equal(t, v1.ConditionFalse, rr.Status)
			require.Equal(t, tc.reason, rr.Reason)
			require.Equal(t, testPeriod, res.RequeueAfter)
			require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0])
			_, ready := h.upstream(t, "echo")
			require.True(t, ready, "revision 1 is still routed")
		})
	}
}

// scenario: in-flight-call-finishes-on-old-revision (ADR-0143) — a call in flight on revision 1 completes after the
// switch, and holds revision 1's worker until it ends.
func TestScenarioInFlightCallFinishesOnOldRevision(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Calls = calls })
	_, release := h.rt.serveBlockingRevision(t, "echo-1", "revision 1")
	h.deployReady(t, "echo")
	up, ready := h.upstream(t, "echo")
	require.True(t, ready)
	answer := callInFlight(t, calls, up)

	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision, "revision 2 takes new calls")
	settle()
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision, "the in-flight call holds the drain")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0])

	release()
	require.Equal(t, "revision 1", answer(), "the call completes on revision 1")
	h.reconcile(t, "echo")
	require.Empty(t, h.getFn(t, "echo").Status.DrainingRevision)
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"))
}

const hungGrace = 30 * time.Millisecond

// startHungDrain switches echo to revision 2 while a call that never ends is in flight on revision 1; the drain runs on
// the returned clock with a DrainGrace of hungGrace.
func startHungDrain(t *testing.T) (*shimHarness, *clock.Manual) {
	t.Helper()
	clk := clock.NewManual(time.Unix(1_700_000_000, 0))
	calls := activator.NewCallTracker(clk)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Calls = calls
		d.Clock = clk
		d.DrainGrace = hungGrace
	})
	h.rt.serveBlockingRevision(t, "echo-1", "revision 1")
	h.deployReady(t, "echo")
	up, _ := h.upstream(t, "echo")
	callInFlight(t, calls, up)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision)
	return h, clk
}

// A call that never ends holds the drain only until DrainGrace has passed since the switch (ADR-0143 Decision 4.1).
func TestDrainGraceBoundsAHungCall(t *testing.T) {
	t.Parallel()
	h, clk := startHungDrain(t)
	clk.Advance(hungGrace - time.Millisecond)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision, "within the grace the call holds the drain")

	clk.Advance(time.Millisecond)
	h.reconcile(t, "echo")
	require.Empty(t, h.getFn(t, "echo").Status.DrainingRevision, "past the grace the worker stops anyway")
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"))
}

// A busy runner that spends longer than DrainGrace between the switch and the next pass does not cut the grace short:
// it runs on the reconciler's clock (issue #574).
func TestIssue574_WallTimeDoesNotEndTheDrainGrace(t *testing.T) {
	t.Parallel()
	h, clk := startHungDrain(t)
	clk.Advance(2 * time.Millisecond)
	time.Sleep(hungGrace + 10*time.Millisecond)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision, "within the grace the call holds the drain")
}

// A newer apply during a drain neither extends it nor switches before it ends (ADR-0143 Decisions 4.1, 4.4).
func TestNewerApplyDuringDrainDoesNotExtendIt(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Calls = calls
		d.DrainGrace = 50 * time.Millisecond
	})
	h.rt.serveBlockingRevision(t, "echo-1", "revision 1")
	h.deployReady(t, "echo")
	up, _ := h.upstream(t, "echo")
	callInFlight(t, calls, up)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	since := h.getFn(t, "echo").Status.DrainingSince
	require.NotNil(t, since)

	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV3" })
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, since.UnixNano(), fn.Status.DrainingSince.UnixNano(), "the drain keeps its start")
	require.Equal(t, "echo-2", fn.Status.ServingRevision, "no switch while a revision drains")

	time.Sleep(60 * time.Millisecond)
	h.reconcile(t, "echo")
	h.reconcile(t, "echo")
	fn = h.getFn(t, "echo")
	require.Equal(t, "echo-3", fn.Status.ServingRevision, "once the drain ended, revision 3 takes the calls")
}

// The drain runs before the gates, so it proceeds while a newer revision fails one (ADR-0143 Decision 4.1).
func TestDrainProceedsWhileAGateFails(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.Calls = calls
		d.DrainGrace = 30 * time.Millisecond
	})
	h.rt.serveBlockingRevision(t, "echo-1", "revision 1")
	h.deployReady(t, "echo")
	up, _ := h.upstream(t, "echo")
	callInFlight(t, calls, up)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision)

	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"missing"} })
	time.Sleep(40 * time.Millisecond)
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, "ConfigResolveFailed", h.condition(t, "echo", "RevisionReady").Reason)
	require.Empty(t, fn.Status.DrainingRevision, "the drain ended despite the failing gate")
	require.NotContains(t, h.rt.revisionStates("echo"), v1.ObjectName("echo-1"))
	require.Equal(t, "echo-2", fn.Status.ServingRevision)
}

// scenario: idle-function-starts-new-revision-on-wake (ADR-0143) — a spec change to an idle scale-to-zero Function
// boots nothing; the next wake starts revision 2.
func TestScenarioIdleFunctionStartsNewRevisionOnWake(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "nap")
	h.setPhase(t, "nap", v1.PhaseIdle)
	h.reconcile(t, "nap")
	fn := h.getFn(t, "nap")
	require.Equal(t, v1.PhaseIdle, fn.Status.Phase)
	require.Empty(t, fn.Status.ServingRevision, "nothing serves an idle Function")
	creates, _ := h.rt.counts()

	h.apply(t, "nap", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "nap")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "no worker boots while the Function is idle")
	require.Equal(t, "nap-2", h.getFn(t, "nap").Status.CurrentRevision)

	h.setPhase(t, "nap", v1.PhaseDeploying)
	h.reconcile(t, "nap")
	fn = h.getFn(t, "nap")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, "nap-2", fn.Status.ServingRevision)
	spec := h.rt.specOf(runtime.NewInstanceID("default", "nap", "nap-2", 0))
	require.Equal(t, "handleV2", spec.Env["FUNCD_HANDLER"], "the wake starts revision 2")
	require.NotContains(t, h.rt.revisionStates("nap"), v1.ObjectName("nap-1"))
}

// scenario: newer-apply-supersedes-booting-revision (ADR-0143) — revision 3 supersedes a revision 2 still booting:
// revision 2's workers stop, and revision 1 keeps serving until revision 3 is ready.
func TestScenarioNewerApplySupersedesBootingRevision(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	url1, _ := h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	_, setReady3 := h.rt.serveRevision(t, "echo-3", http.StatusServiceUnavailable)
	h.deployReady(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-2"][0], "revision 2 boots")

	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV3" })
	h.reconcile(t, "echo")
	states := h.rt.revisionStates("echo")
	require.NotContains(t, states, v1.ObjectName("echo-2"), "revision 2 is stopped and removed")
	require.Equal(t, runtime.StateRunning, states["echo-3"][0], "revision 3 boots")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
	up, ready := h.upstream(t, "echo")
	require.True(t, ready)
	require.Equal(t, url1, up, "revision 1 keeps serving")

	setReady3(http.StatusOK)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-3", h.getFn(t, "echo").Status.ServingRevision)
}

// scenario: serving-worker-crash-during-switch-is-replaced (ADR-0143) — revision 1's only worker crashes while revision
// 2 boots; it is replaced from revision 1's Revision, and revision 1 serves again.
func TestScenarioServingWorkerCrashDuringSwitchIsReplaced(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.deployReady(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")

	h.rt.exitRevision("echo", "echo-1", 0, runtime.StateFailed, time.Minute)
	creates, _ := h.rt.counts()
	h.reconcile(t, "echo")
	after, _ := h.rt.counts()
	require.Equal(t, creates+1, after, "revision 1's crashed worker is replaced")
	old := runtime.NewInstanceID("default", "echo", "echo-1", 0)
	require.False(t, h.rt.wasRemoved(old), "a crashed serving worker is replaced, never removed")
	require.Equal(t, "handle", h.rt.specOf(old).Env["FUNCD_HANDLER"], "the replacement runs revision 1's handler")
	fn := h.getFn(t, "echo")
	require.Equal(t, "echo-1", fn.Status.ServingRevision)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "revision 1 serves again")
}

// scenario: replicas-change-switches-without-dropping (ADR-0143) — a replicas change from 3 to 1 boots one revision-2
// replica while revision 1 keeps its three until the switch.
func TestScenarioReplicasChangeSwitchesWithoutDropping(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.create(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 3
		fn.Spec.Scaling.MinReplicas = 3
	})
	h.reconcile(t, "echo")
	require.Len(t, h.rt.revisionStates("echo")["echo-1"], 3)

	_, setReady2 := h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 1
		fn.Spec.Scaling.MinReplicas = 1
	})
	h.reconcile(t, "echo")
	states := h.rt.revisionStates("echo")
	for i := range 3 {
		require.Equal(t, runtime.StateRunning, states["echo-1"][i], "revision 1 keeps replica %d until the switch", i)
	}
	require.Len(t, states["echo-2"], 1)
	_, ready := h.upstream(t, "echo")
	require.True(t, ready)

	setReady2(http.StatusOK)
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	states = h.rt.revisionStates("echo")
	require.NotContains(t, states, v1.ObjectName("echo-1"), "revision 1 drained as a whole")
	require.Len(t, states["echo-2"], 1)
	require.Equal(t, 1, h.getFn(t, "echo").Status.Replicas)
}

// scenario: steady-function-stays-quiescent (ADR-0143) — once the old revision has drained, supervision passes write
// nothing and call only runtime.Status.
func TestScenarioSteadyFunctionStaysQuiescent(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	settle()
	h.reconcile(t, "echo")
	require.Empty(t, h.getFn(t, "echo").Status.DrainingRevision)

	rv := h.getFn(t, "echo").ResourceVersion
	_, lists := h.rt.counts()
	for range 3 {
		res := h.reconcile(t, "echo")
		require.Equal(t, testPeriod, res.RequeueAfter)
	}
	require.Equal(t, rv, h.getFn(t, "echo").ResourceVersion, "steady passes write nothing")
	_, after := h.rt.counts()
	require.Equal(t, lists, after, "steady passes only call Status")
}

func (h *shimHarness) configMap(t *testing.T, name string) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindConfigMap)
	require.True(t, ok)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cm.Spec.Data = map[string]string{"APP_MODE": "test"}
	_, err := h.st.Create(context.Background(), cm)
	require.NoError(t, err)
}

func (h *shimHarness) deleteConfigMap(t *testing.T, name string) {
	t.Helper()
	require.NoError(t, h.st.Delete(context.Background(), v1.KindConfigMap.GVK(), "default", v1.ObjectName(name), ""))
}

// The calls move only once every replica of the current revision is ready (ADR-0143 Decision 4.4).
func TestSwitchWaitsForEveryReplica(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.create(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 2
		fn.Spec.Scaling.MinReplicas = 2
	})
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)

	held := runtime.NewInstanceID("default", "echo", "echo-2", 1)
	h.rt.hold(held, true)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision, "one of two replicas is ready: no switch")

	h.rt.hold(held, false)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision)
}

// A demoted revision's idle workers outlive the switch by HandOutSettle, and a pass comes back within a second while
// one drains (ADR-0143 Decisions 4.1, 4.7).
func TestDrainWaitsOutTheHandOutSettle(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) {
		d.SupervisionPeriod = time.Hour
		d.HandOutSettle = 3 * time.Second
	})
	h.createFn(t, "echo")
	h.reconcile(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	res := h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision)
	require.Equal(t, 3*time.Second, res.RequeueAfter, "the switch comes back when the hand-out settle ends")

	res = h.reconcile(t, "echo")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.DrainingRevision, "an idle worker is kept through the settle")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][0])
	require.Equal(t, time.Second, res.RequeueAfter, "a drain is checked at least every second")
}

// The resolver records each hand-out of a ready upstream, so the drain waits out a call resolved just before a switch
// (ADR-0143 Decision 4.2).
func TestResolverRecordsReadyHandOuts(t *testing.T) {
	t.Parallel()
	calls := activator.NewCallTracker(nil)
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) { d.Calls = calls })
	_, setStatus := h.rt.serveRevision(t, "echo-1", http.StatusServiceUnavailable)
	h.createFn(t, "echo")
	h.reconcile(t, "echo")
	up, ready := h.upstream(t, "echo")
	require.False(t, ready)
	require.NotEmpty(t, up)
	require.True(t, calls.Idle(up, time.Minute), "an upstream that is not ready is not recorded")

	setStatus(http.StatusOK)
	h.reconcile(t, "echo")
	up, ready = h.upstream(t, "echo")
	require.True(t, ready)
	require.False(t, calls.Idle(up, time.Minute), "a ready hand-out is recorded")
}

// A gate that starts failing while the current revision boots stops that revision's workers; the serving revision
// keeps the calls (ADR-0143 Decision 4.6).
func TestGateFailureStopsTheBootingRevision(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.configMap(t, "app")
	h.deployReady(t, "echo")
	h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"app"} })
	h.reconcile(t, "echo")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-2"][0], "revision 2 boots")

	h.deleteConfigMap(t, "app")
	h.reconcile(t, "echo")
	require.Equal(t, "ConfigResolveFailed", h.condition(t, "echo", "RevisionReady").Reason)
	states := h.rt.revisionStates("echo")
	require.Equal(t, runtime.StateStopped, states["echo-2"][0], "the failing revision's worker stops")
	require.Equal(t, runtime.StateRunning, states["echo-1"][0])
	up, ready := h.upstream(t, "echo")
	require.True(t, ready, "revision 1 keeps serving")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
	require.NotEmpty(t, up)
}

// A failed gate keeps the Function out of the steady state, so the next pass sees the gate pass again (ADR-0143
// Decisions 4.6, 6).
func TestFailedGateIsRecheckedNextPass(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.configMap(t, "app")
	h.deployReady(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Config = []v1.ObjectName{"app"} })
	h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision)

	h.deleteConfigMap(t, "app")
	settle()
	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Empty(t, fn.Status.DrainingRevision)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "revision 2 keeps serving")
	require.Equal(t, "ConfigResolveFailed", h.condition(t, "echo", "RevisionReady").Reason)

	h.configMap(t, "app")
	h.reconcile(t, "echo")
	require.Equal(t, v1.ConditionTrue, h.condition(t, "echo", "RevisionReady").Status)
}

// During a switch the serving revision keeps the replicas it runs, whatever the current revision's count (ADR-0143
// Decision 4.3).
func TestServingRevisionKeepsItsReplicasDuringASwitch(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.create(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 3
		fn.Spec.Scaling.MinReplicas = 3
	})
	h.reconcile(t, "echo")
	h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 1
		fn.Spec.Scaling.MinReplicas = 1
	})
	h.reconcile(t, "echo")

	h.rt.exitRevision("echo", "echo-1", 2, runtime.StateFailed, time.Minute)
	h.reconcile(t, "echo")
	require.Equal(t, runtime.StateRunning, h.rt.revisionStates("echo")["echo-1"][2], "revision 1's replica 2 is replaced")
}

// After a daemon restart the runtime lists no worker, so the serving revision comes back with status.replicas
// replicas (ADR-0143 Decision 4.3).
func TestServingRevisionComesBackAfterARestart(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.create(t, "echo", func(fn *v1.Function) {
		fn.Spec.Replicas = 2
		fn.Spec.Scaling.MinReplicas = 2
	})
	h.reconcile(t, "echo")
	h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.Equal(t, 2, h.getFn(t, "echo").Status.Replicas)

	h.rt.forget()
	h.reconcile(t, "echo")
	states := h.rt.revisionStates("echo")
	require.Len(t, states["echo-1"], 2, "revision 1 comes back with its two replicas")
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
}

// Deleting a Function stops and removes its workers (ADR-0143).
func TestDeleteRemovesTheWorkers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "echo", ""))
	h.reconcile(t, "echo")
	require.Empty(t, h.rt.revisionStates("echo"), "the deleted Function's worker left the runtime")
	require.True(t, h.rt.wasRemoved(runtime.NewInstanceID("default", "echo", "echo-1", 0)))
}

// A pooled member's servingRevision follows its current revision once the pool worker serves (ADR-0143 Decision 8).
func TestPooledMemberServesItsCurrentRevision(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		d.PoolShimCommand = []string{"node", "/opt/funcd/pool.mjs"}
	})
	h.create(t, "member", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
	h.reconcile(t, "member")
	fn := h.getFn(t, "member")
	require.Equal(t, v1.PhaseReady, fn.Status.Phase)
	require.Equal(t, "member-1", fn.Status.ServingRevision)

	h.apply(t, "member", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "member")
	require.Equal(t, "member-2", h.getFn(t, "member").Status.ServingRevision)
}

// A status with no current revision tells no worker apart, so the pass drains only once it has stamped one: a worker
// of that revision keeps running.
func TestStatusWithoutRevisionKeepsItsWorker(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	h.deployReady(t, "echo")
	h.apply(t, "echo", func(fn *v1.Function) { fn.Status = v1.FunctionStatus{} })
	creates, _ := h.rt.counts()
	h.reconcile(t, "echo")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "the running worker is kept, not replaced")
	require.False(t, h.rt.wasRemoved(runtime.NewInstanceID("default", "echo", "echo-1", 0)))
	require.Equal(t, "echo-1", h.getFn(t, "echo").Status.ServingRevision)
}

// Issue 24: a pass over a broken redeploy beside a serving revision writes nothing, so its write cannot
// retrigger the pass through the watch (ADR-0143 Decision 4.6, ADR-0047 Decision 1).
func TestIssue24_BrokenRedeployPassWritesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		crash bool
		phase v1.Phase
	}{
		{"serving-ready", false, v1.PhaseReady},
		{"serving-degraded", true, v1.PhaseDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) {
				d.SupervisionPeriod = time.Hour // holds a crashed serving worker in its backoff
			})
			h.createFn(t, "echo")
			h.reconcile(t, "echo")
			h.rt.failRevision("echo-2", true)
			h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "broken" })
			h.reconcile(t, "echo")
			if tc.crash {
				h.rt.exitRevision("echo", "echo-1", 0, runtime.StateFailed, 0)
			}
			h.reconcile(t, "echo")
			fn := h.getFn(t, "echo")
			require.Equal(t, tc.phase, fn.Status.Phase)
			require.Equal(t, "ShapeInvalid", h.condition(t, "echo", "RevisionReady").Reason)
			require.Equal(t, v1.ConditionFalse, h.shapeValid(t, "echo"))

			for range 3 {
				h.reconcile(t, "echo")
			}
			require.Equal(t, fn.ResourceVersion, h.getFn(t, "echo").ResourceVersion, "a pass that observes no change writes nothing")
		})
	}
}

// Issue 53: deleting the serving Revision during a switch leaves its running worker serving, and the calls still move to
// the current revision once it is ready (ADR-0143 Decisions 4.3, 4.4).
func TestIssue53_DeletedServingRevisionStillSwitches(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch)
	url1, _ := h.rt.serveRevision(t, "echo-1", http.StatusOK)
	h.deployReady(t, "echo")
	url2, setReady2 := h.rt.serveRevision(t, "echo-2", http.StatusServiceUnavailable)
	h.apply(t, "echo", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "echo")
	require.NoError(t, h.st.Delete(context.Background(), v1.KindRevision.GVK(), "default", "echo-1", ""))

	h.reconcile(t, "echo")
	fn := h.getFn(t, "echo")
	require.Equal(t, fn.Generation, fn.Status.ObservedGeneration, "the pass writes its status")
	require.Equal(t, "echo-1", fn.Status.ServingRevision, "revision 1's running worker keeps serving")
	up, ready := h.upstream(t, "echo")
	require.True(t, ready)
	require.Equal(t, url1, up)

	setReady2(http.StatusOK)
	h.reconcile(t, "echo")
	require.Equal(t, "echo-2", h.getFn(t, "echo").Status.ServingRevision, "the switch completes")
	up, _ = h.upstream(t, "echo")
	require.Equal(t, url2, up)
}

// Issue 55: a delete and a re-create reach the reconciler as one pass, so teardown never runs and the re-created
// Function's first revision has the deleted one's name. The pass replaces the deleted Function's worker with one of the
// new spec on every replica, and the next passes keep it.
func TestIssue55_DeleteRecreateInOnePassReplacesTheWorker(t *testing.T) {
	t.Parallel()
	for _, replicas := range []int{1, 2} {
		t.Run(strconv.Itoa(replicas)+"-replicas", func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch)
			h.deployReady(t, "echo")
			require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "echo", ""))
			h.create(t, "echo", func(fn *v1.Function) {
				fn.Spec.Handler = "handleNEW"
				fn.Spec.Replicas = replicas
			})
			h.reconcile(t, "echo")

			require.True(t, h.rt.wasRemoved(runtime.NewInstanceID("default", "echo", "echo-1", 0)), "the deleted Function's worker left the runtime")
			for i := range replicas {
				spec := h.rt.specOf(runtime.NewInstanceID("default", "echo", "echo-1", i))
				require.Equal(t, "handleNEW", spec.Env["FUNCD_HANDLER"], "replica %d runs the re-created spec", i)
			}
			obj, err := h.st.Get(context.Background(), v1.KindRevision.GVK(), "default", "echo-1")
			require.NoError(t, err)
			require.Equal(t, "handleNEW", obj.(*v1.Revision).Spec.Handler, "revision 1 is the re-created Function's")

			creates, _ := h.rt.counts()
			for range 3 {
				h.reconcile(t, "echo")
			}
			after, _ := h.rt.counts()
			require.Equal(t, creates, after, "the next passes keep the new workers")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "echo").Status.Phase)
		})
	}
}
