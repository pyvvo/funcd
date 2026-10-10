package app_test

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

const pausedMessage = "spec.paused is set: the App writes no part until it is resumed"

func pause(on bool) func(*v1.App) { return func(a *v1.App) { a.Spec.Paused = on } }

func (h *harness) paused() v1.Condition {
	h.t.Helper()
	c, ok := h.app().Status.Conditions.Get("Paused")
	require.True(h.t, ok, "the App has no Paused condition")
	return c
}

// partVersions is versionsAll without the App, whose status a paused pass writes.
func (h *harness) partVersions() map[v1.ObjectRef]string {
	h.t.Helper()
	out := h.versionsAll()
	maps.DeleteFunc(out, func(k v1.ObjectRef, _ string) bool { return k.Kind == v1.KindApp })
	return out
}

// fakeHold is a platform hold (ADR-0206) that is never held and was released at released.
type fakeHold struct{ released time.Time }

func (*fakeHold) Held() bool              { return false }
func (h *fakeHold) ReleasedAt() time.Time { return h.released }

// ADR-0212 Decision 4: a paused pass sets Paused True SpecPaused and writes nothing else: no part created, updated or
// deleted, no stamp on a spec change, no Failed past the deadline, no AppRevision write and no history deletion; it
// returns Result{}, keeps the phase and Ready, and a repeated paused pass keeps every resourceVersion.
func TestAppPausedPassWritesNothing(t *testing.T) {
	h := newHarness(t, nil, func(d *app.Deps) { d.UpgradeTimeout = 20 * time.Second })
	h.install(todoApp(nil))
	h.edit(setImage("oci-layout://todo-api:2"))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	h.edit(setImage("oci-layout://never-starts:3"))
	h.reconcile()
	require.Equal(t, v1.PhaseDeploying, h.rev(3).Status.Phase)
	readyBefore := h.ready()

	h.edit(pause(true))
	before := h.partVersions()
	require.Equal(t, controller.Result{}, h.reconcile())
	got := h.app()
	c := h.paused()
	requireCond(t, c, v1.ConditionTrue, "SpecPaused", pausedMessage)
	require.Equal(t, got.Generation, c.ObservedGeneration)
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), c.LastTransitionTime)
	require.NotEqual(t, got.Generation, got.Status.ObservedGeneration, "status.observedGeneration keeps its stored value")
	require.Equal(t, v1.PhaseDeploying, got.Status.Phase, "the App keeps its phase")
	require.Equal(t, readyBefore, h.ready(), "Ready keeps its stored value")
	require.Equal(t, before, h.partVersions())

	h.handEdit("oci-layout://hotfix:3")
	rt := h.get(v1.KindRoute, "todo-api")
	require.NoError(t, h.st.Delete(h.ctx, v1.KindRoute.GVK(), ns, "todo-api", rt.GetObjectMeta().ResourceVersion))
	h.edit(func(a *v1.App) {
		a.Spec.Version = "4.0.0"
		a.Spec.Workflows = nil
	})
	h.clk.Advance(time.Hour)
	h.deps.RevisionHistory = 1
	h.restart()
	before = h.partVersions()
	require.Equal(t, controller.Result{}, h.reconcile())
	require.Equal(t, before, h.partVersions())
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2", "todo-3"}, h.revNames(), "no stamp and no history deletion")
	require.Equal(t, v1.PhaseDeploying, h.rev(3).Status.Phase, "no Failed past the deadline")
	require.Equal(t, "oci-layout://hotfix:3", h.image(), "the hand edit stays")
	require.Nil(t, h.get(v1.KindRoute, "todo-api"), "nothing is re-created")
	require.NotNil(t, h.get(v1.KindWorkflow, "todo-plan"), "nothing is pruned")
	require.Equal(t, h.app().Generation, h.paused().ObservedGeneration)
	h.quiet()
}

// scenario: app-paused-keeps-hotfix (the reconciler half) — pause and resume stamp nothing; the resume sets Paused False
// Resumed at the clock's time, writes the declared spec back as a self-heal, and the stored revision holds no pause.
func TestScenarioAppPausedKeepsHotfix(t *testing.T) {
	logs := &records{}
	h := newHarness(t, nil, logs.to)
	h.install(todoApp(nil))
	h.edit(pause(true))
	h.reconcile()
	h.handEdit("oci-layout://hotfix:2")
	h.reconcile()
	require.Equal(t, "oci-layout://hotfix:2", h.image())
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)

	h.clk.Advance(time.Minute)
	h.edit(pause(false))
	h.reconcile()
	c := h.paused()
	requireCond(t, c, v1.ConditionFalse, "Resumed", "")
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), c.LastTransitionTime)
	require.Equal(t, "oci-layout://todo-api:1", h.image())
	require.Equal(t, &v1.AppSelfHeal{Kind: v1.KindFunction, Name: "todo-api", At: v1.NewTimestamp(h.clk.Now())}, h.app().Status.LastSelfHeal)
	require.Equal(t, []map[string]string{healLine(v1.KindFunction, "todo-api")}, logs.selfHealed())
	require.Equal(t, []v1.ObjectName{"todo-1"}, h.revNames())
	frozen, err := json.Marshal(h.rev(1).Spec.Spec)
	require.NoError(t, err)
	require.NotContains(t, string(frozen), "paused")

	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)
	h.quiet()
	require.Equal(t, c, h.paused(), "the condition stays False")
}

// scenario: app-paused-defers-upgrade (the reconciler half) — a spec change made while paused stamps at the resume,
// and the frozen spec has no pause.
func TestScenarioAppPausedDefersUpgrade(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(pause(true))
	h.reconcile()
	h.edit(func(a *v1.App) {
		setImage("oci-layout://todo-api:2")(a)
		a.Spec.Version = "2.0.0"
	})
	h.reconcile()
	require.Equal(t, []v1.ObjectName{"todo-1"}, h.revNames())
	require.Equal(t, "oci-layout://todo-api:1", h.image())

	h.edit(pause(false))
	h.reconcile()
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, h.revNames())
	require.False(t, h.rev(2).Spec.Spec.Paused)
	require.Equal(t, h.app().Spec, h.rev(2).Spec.Spec)
	require.Equal(t, "oci-layout://todo-api:2", h.image())
	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
}

// scenario: app-apply-without-paused-resumes (the reconciler half) — a spec without paused resumes the App and stamps.
func TestScenarioAppApplyWithoutPausedResumes(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(pause(true))
	h.reconcile()
	h.edit(func(a *v1.App) { a.Spec = todoApp(setImage("oci-layout://todo-api:2")).Spec })
	h.reconcile()
	requireCond(t, h.paused(), v1.ConditionFalse, "Resumed", "")
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, h.revNames())
	require.Equal(t, "oci-layout://todo-api:2", h.image())
}

// scenario: app-paused-rollout-full-timeout (the reconciler half) and Decision 6 — paused time does not count: a resume
// gives the Deploying revision a full timeout from resumedAt, a later release one from ReleasedAt; neither alone
// starts a deadline, and a revision stamped after them keeps its startedAt.
func TestScenarioAppPausedRolloutFullTimeout(t *testing.T) {
	hold := &fakeHold{}
	h := newHarness(t, nil, func(d *app.Deps) {
		d.UpgradeTimeout = 20 * time.Second
		d.Hold = hold
	})
	h.install(todoApp(nil))
	hold.released = h.clk.Now()
	h.clk.Advance(time.Second)
	h.edit(setImage("oci-layout://never-starts:2"))
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "a release before the stamp does not count")
	h.clk.Advance(5 * time.Second)
	h.edit(pause(true))
	require.Equal(t, controller.Result{}, h.reconcile())
	h.clk.Advance(30 * time.Second)
	require.Equal(t, controller.Result{}, h.reconcile(), "no Failed while paused")
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)

	h.edit(pause(false))
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "a full timeout from the resume")
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)
	h.clk.Advance(10 * time.Second)
	require.Equal(t, 10*time.Second, h.reconcile().RequeueAfter)
	hold.released = h.clk.Now()
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "a full timeout from the release")
	h.clk.Advance(20*time.Second - time.Millisecond)
	require.Equal(t, time.Millisecond, h.reconcile().RequeueAfter)
	h.clk.Advance(time.Millisecond)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)

	h.clk.Advance(time.Minute)
	h.edit(setImage("oci-layout://never-starts:3"))
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "a revision stamped later keeps its startedAt")
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase, "a Failed revision stays Failed")
}

// ADR-0212 Decision 7: a Degraded App stays Degraded across a pause and a resume, though the generation moved.
func TestAppDegradedStaysDegradedAcrossAPause(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	fn := h.get(v1.KindFunction, "todo-api")
	setStatus(fn, v1.PhaseDegraded, v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "Restarting"})
	h.update(fn)
	h.reconcile()
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)

	h.edit(pause(true))
	h.reconcile()
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	h.edit(pause(false))
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.PhaseDegraded, got.Status.Phase)
	require.NotEqual(t, got.Generation, got.Status.ObservedGeneration)

	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
}

// ADR-0212 Decision 4: an App created paused installs nothing and has no phase until resumed.
func TestAppCreatedPausedInstallsNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.create(todoApp(pause(true)))
	require.Equal(t, controller.Result{}, h.reconcile())
	for _, k := range []v1.Kind{v1.KindAppRevision, v1.KindKVStore, v1.KindBucket, v1.KindFunction, v1.KindWorkflow, v1.KindRoute} {
		res, err := h.st.List(h.ctx, k.GVK(), store.ListOptions{Namespace: ns})
		require.NoError(t, err)
		require.Empty(t, res.Items, "%s", k)
	}
	got := h.app()
	require.Empty(t, got.Status.Phase)
	_, ok := got.Status.Conditions.Get("Ready")
	require.False(t, ok)
	requireCond(t, h.paused(), v1.ConditionTrue, "SpecPaused", pausedMessage)

	h.edit(pause(false))
	h.reconcile()
	require.Equal(t, []v1.ObjectName{"todo-1"}, h.revNames())
	require.NotNil(t, h.get(v1.KindFunction, "todo-api"))
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
}

// ADR-0212 Decision 5: the Paused condition is absent on an App never paused.
func TestAppNeverPausedHasNoPausedCondition(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	_, ok := h.app().Status.Conditions.Get("Paused")
	require.False(t, ok)
}
