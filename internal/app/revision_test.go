package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func (h *harness) rev(n int64) *v1.AppRevision {
	h.t.Helper()
	obj := h.get(v1.KindAppRevision, v1.AppRevisionName("todo", n))
	if obj == nil {
		return nil
	}
	return obj.(*v1.AppRevision)
}

// revNames lists the stored AppRevisions by number.
func (h *harness) revNames() []v1.ObjectName {
	h.t.Helper()
	res, err := h.st.List(h.ctx, v1.KindAppRevision.GVK(), store.ListOptions{Namespace: ns})
	require.NoError(h.t, err)
	slices.SortFunc(res.Items, func(a, b v1.Object) int {
		return int(a.(*v1.AppRevision).Spec.Number - b.(*v1.AppRevision).Spec.Number)
	})
	var out []v1.ObjectName
	for _, o := range res.Items {
		out = append(out, o.GetObjectMeta().Name)
	}
	return out
}

func revCond(t *testing.T, rev *v1.AppRevision, ct v1.ConditionType) v1.Condition {
	t.Helper()
	c, ok := rev.Status.Conditions.Get(ct)
	require.True(t, ok, "%s has no %s condition", rev.Name, ct)
	return c
}

// requireCond checks a condition's status, reason and message, ignoring its times and generation.
func requireCond(t *testing.T, c v1.Condition, s v1.ConditionStatus, reason, msg string) {
	t.Helper()
	require.Equal(t, s, c.Status, "%s", c.Type)
	require.Equal(t, reason, c.Reason, "%s", c.Type)
	require.Equal(t, msg, c.Message, "%s", c.Type)
}

// versionsAll maps every stored App, AppRevision and part to its resourceVersion.
func (h *harness) versionsAll() map[v1.ObjectRef]string {
	h.t.Helper()
	out := map[v1.ObjectRef]string{}
	for _, k := range []v1.Kind{v1.KindApp, v1.KindAppRevision, v1.KindConfigMap, v1.KindKVStore, v1.KindBucket, v1.KindFunction, v1.KindWorkflow, v1.KindRoute} {
		res, err := h.st.List(h.ctx, k.GVK(), store.ListOptions{Namespace: ns})
		require.NoError(h.t, err)
		for _, o := range res.Items {
			out[keyOf(o)] = o.GetObjectMeta().ResourceVersion
		}
	}
	return out
}

func keyOf(o v1.Object) v1.ObjectRef {
	return v1.ObjectRef{Kind: o.GroupVersionKind().Kind, Namespace: o.GetObjectMeta().Namespace, Name: o.GetObjectMeta().Name}
}

// quiet checks that a repeated pass writes nothing (ADR-0047).
func (h *harness) quiet() {
	h.t.Helper()
	before := h.versionsAll()
	h.reconcile()
	require.Equal(h.t, before, h.versionsAll(), "a repeated pass writes nothing")
}

func setImage(image string) func(*v1.App) {
	return func(a *v1.App) { a.Spec.Functions[0].Image = image }
}

func versioned(v string) func(*v1.App) { return func(a *v1.App) { a.Spec.Version = v } }

// scenario: app-reapply-same-spec (the reconciler half) — an unchanged spec stamps nothing and writes nothing.
func TestScenarioAppReapplySameSpec(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(versioned("1.0.0")))
	got := h.app()
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.LatestRevision)
	require.Equal(t, "1.0.0", got.Status.Version)
	h.quiet()

	h.edit(func(a *v1.App) { a.Spec.Sensors = []v1.AppSensor{} })
	h.quiet()
	require.Equal(t, []v1.ObjectName{"todo-1"}, h.revNames(), "an omitted and an empty section are equal")
}

// Decision 3: numbers only grow, with no gap; a spec equal to an older revision's (a rollback) gets a new number and
// the switch sets version (the reconciler half of scenario app-rollback).
func TestAppRevisionNumbersHaveNoGap(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(versioned("1.0.0")))
	upgrade := func(mutate func(*v1.App)) {
		t.Helper()
		h.edit(mutate)
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-api")
		h.reconcile()
	}
	upgrade(func(a *v1.App) {
		setImage("oci-layout://todo-api:2")(a)
		a.Spec.Version = "2.0.0"
	})
	upgrade(func(a *v1.App) {
		setImage("oci-layout://todo-api:1")(a)
		a.Spec.Version = "1.0.0"
	})
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2", "todo-3"}, h.revNames())
	one, three := h.rev(1), h.rev(3)
	require.Equal(t, one.Spec.Spec, three.Spec.Spec)
	require.Equal(t, int64(3), three.Spec.Number)
	got := h.app()
	require.Equal(t, v1.ObjectName("todo-3"), got.Status.CurrentRevision)
	require.Equal(t, "1.0.0", got.Status.Version)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
}

// Decision 3: an entry moved within a section is a change.
func TestAppRevisionReorderedEntryStamps(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(func(a *v1.App) { a.Spec.KV[0], a.Spec.KV[1] = a.Spec.KV[1], a.Spec.KV[0] })
	h.reconcile()
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, h.revNames())
	require.Equal(t, v1.ObjectName("todo-cache"), h.rev(2).Spec.Spec.KV[0].Name)
}

func foreignRevision(owner []v1.OwnerReference) *v1.AppRevision {
	return &v1.AppRevision{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindAppRevision.GVK().APIVersion(), Kind: v1.KindAppRevision},
		ObjectMeta: v1.ObjectMeta{Name: "todo-1", Namespace: ns, ResourceGroup: "rg1", OwnerReferences: owner},
		Spec:       v1.AppRevisionSpec{App: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, Number: 1},
	}
}

// Decision 3: a stored namesake a deleted incarnation controls is dropped and stamped afresh; any other owner stops
// the pass with ChildNotOwned before any part is written.
func TestAppRevisionNamesake(t *testing.T) {
	t.Run("a deleted incarnation's is dropped", func(t *testing.T) {
		h := newHarness(t, nil)
		old := h.create(foreignRevision([]v1.OwnerReference{
			{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: "an-earlier-uid", Controller: true},
		}))
		a := h.create(todoApp(nil)).(*v1.App)
		h.reconcile()
		got := h.rev(1)
		require.NotEqual(t, old.GetObjectMeta().UID, got.UID, "stamped afresh")
		require.True(t, v1.ControlledBy(got.OwnerReferences, v1.KindApp, a.UID))
		require.Equal(t, a.Spec, got.Spec.Spec)
		require.Equal(t, v1.ResourceGroupName("todo-rg"), got.ResourceGroup)
		require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.LatestRevision)
	})
	t.Run("another owner stops the pass", func(t *testing.T) {
		h := newHarness(t, nil)
		old := h.create(foreignRevision([]v1.OwnerReference{
			{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "todo"}, UID: "a-function-uid", Controller: true},
		}))
		h.create(todoApp(nil))
		require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter, "no deadline runs")
		require.Equal(t, old.GetObjectMeta().ResourceVersion, h.rev(1).ResourceVersion)
		for _, k := range []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindFunction, v1.KindWorkflow, v1.KindRoute} {
			res, err := h.st.List(h.ctx, k.GVK(), store.ListOptions{Namespace: ns})
			require.NoError(t, err)
			require.Empty(t, res.Items, "nothing is written: %s", k)
		}
		got := h.app()
		require.Equal(t, v1.PhaseDeploying, got.Status.Phase)
		require.Empty(t, got.Status.LatestRevision)
		requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotOwned", "AppRevision/todo-1: exists and is not owned by App/todo")
		h.quiet()
	})
}

// scenario: app-upgrade (the reconciler half) — a changed Function keeps the App Deploying with the old current
// revision until it serves; then the new revision is current and Ready, the old one Replaced and still Ready.
func TestScenarioAppUpgrade(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(versioned("1.0.0")))
	h.edit(func(a *v1.App) {
		setImage("oci-layout://todo-api:2")(a)
		a.Spec.Version = "2.0.0"
	})
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.PhaseDeploying, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.LatestRevision)
	require.Equal(t, "1.0.0", got.Status.Version)
	two := h.rev(2)
	require.Equal(t, v1.PhaseDeploying, two.Status.Phase)
	require.Equal(t, v1.NewTimestamp(h.clk.Now()), *two.Status.StartedAt)
	requireCond(t, revCond(t, two, "Applied"), v1.ConditionTrue, "", "")
	requireCond(t, revCond(t, two, "ChildrenReady"), v1.ConditionFalse, "Progressing", "Function/todo-api: Progressing")
	requireCond(t, revCond(t, two, "Current"), v1.ConditionFalse, "Progressing", "")
	requireCond(t, revCond(t, h.rev(1), "Current"), v1.ConditionTrue, "", "")
	h.quiet()

	h.markReady(v1.KindFunction, "todo-api")
	require.Zero(t, h.reconcile())
	got = h.app()
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.CurrentRevision)
	require.Equal(t, "2.0.0", got.Status.Version)
	two = h.rev(2)
	require.Equal(t, v1.PhaseReady, two.Status.Phase)
	requireCond(t, revCond(t, two, "ChildrenReady"), v1.ConditionTrue, "", "")
	requireCond(t, revCond(t, two, "Current"), v1.ConditionTrue, "", "")
	one := h.rev(1)
	require.Equal(t, v1.PhaseReady, one.Status.Phase)
	requireCond(t, revCond(t, one, "Current"), v1.ConditionFalse, "Replaced", "replaced by todo-2")
	h.quiet()
}

// scenario: app-one-part-changes (the reconciler half) — the new revision holds the whole spec and the pass writes
// the changed Workflow alone.
func TestScenarioAppOnePartChanges(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	before := h.versionsAll()
	h.edit(func(a *v1.App) { a.Spec.Workflows[0].Steps[0].Function.Image = "oci-layout://todo-due:2" })
	h.reconcile()
	after := h.versionsAll()
	for k, rv := range before {
		switch k.Kind {
		case v1.KindApp, v1.KindAppRevision:
		case v1.KindWorkflow:
			require.NotEqual(t, rv, after[k], "%s", k.Name)
		default:
			require.Equal(t, rv, after[k], "%s/%s keeps its resourceVersion", k.Kind, k.Name)
		}
	}
	require.Equal(t, h.app().Spec, h.rev(2).Spec.Spec)
}

// scenario: app-upgrade-superseded (the reconciler half) — a change while a revision is Deploying fails it as
// Superseded naming the newer one, which becomes current.
func TestScenarioAppUpgradeSuperseded(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(setImage("oci-layout://todo-api:2"))
	h.reconcile()
	h.edit(setImage("oci-layout://todo-api:3"))
	h.reconcile()
	two := h.rev(2)
	require.Equal(t, v1.PhaseFailed, two.Status.Phase)
	requireCond(t, revCond(t, two, "Current"), v1.ConditionFalse, "Superseded", "superseded by todo-3")
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
	h.quiet()

	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-3"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.PhaseReady, h.rev(3).Status.Phase)
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
	requireCond(t, revCond(t, h.rev(2), "Current"), v1.ConditionFalse, "Superseded", "superseded by todo-3")
	requireCond(t, revCond(t, h.rev(1), "Current"), v1.ConditionFalse, "Replaced", "replaced by todo-3")
}

// scenario: app-failed-upgrade-keeps-serving (the reconciler half) — a Function that never serves fails the revision
// at startedAt + app.upgradeTimeout; each pass before requeues at the deadline; the App is Failed and keeps its current
// revision; a part Ready later neither switches nor clears the Failed revision.
func TestScenarioAppFailedUpgradeKeepsServing(t *testing.T) {
	h := newHarness(t, nil, func(d *app.Deps) { d.UpgradeTimeout = 20 * time.Second })
	h.install(todoApp(versioned("2.0.0")))
	h.edit(func(a *v1.App) {
		setImage("oci-layout://never-starts:3")(a)
		a.Spec.Version = "3.0.0"
	})
	require.Equal(t, 20*time.Second, h.reconcile().RequeueAfter, "the time left")
	fn := h.get(v1.KindFunction, "todo-api")
	setStatus(fn, v1.PhaseFailed, cond("Ready", v1.ConditionFalse, "StartFailed", 0),
		cond("RevisionReady", v1.ConditionFalse, "StartFailed", fn.GetObjectMeta().Generation))
	h.update(fn)
	h.clk.Advance(5 * time.Second)
	require.Equal(t, 15*time.Second, h.reconcile().RequeueAfter)
	require.Equal(t, "Progressing", h.ready().Reason, "ChildNotReady is for Failed and Degraded only")
	requireCond(t, revCond(t, h.rev(2), "ChildrenReady"), v1.ConditionFalse, "Progressing", "Function/todo-api: StartFailed")
	h.clk.Advance(15*time.Second - time.Millisecond)
	require.Equal(t, time.Millisecond, h.reconcile().RequeueAfter)

	h.clk.Advance(time.Millisecond)
	require.Zero(t, h.reconcile(), "a Failed revision has no deadline")
	two := h.rev(2)
	require.Equal(t, v1.PhaseFailed, two.Status.Phase)
	requireCond(t, revCond(t, two, "ChildrenReady"), v1.ConditionFalse, "ChildNotReady", "Function/todo-api: StartFailed")
	requireCond(t, revCond(t, two, "Current"), v1.ConditionFalse, "ChildNotReady", "")
	got := h.app()
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.LatestRevision)
	require.Equal(t, "2.0.0", got.Status.Version)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotReady", "Function/todo-api: StartFailed")
	require.Equal(t, v1.PhaseReady, h.rev(1).Status.Phase)
	require.Equal(t, "oci-layout://never-starts:3", h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Image, "the failed spec stays applied")
	h.quiet()

	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase, "Failed is final")
	require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.PhaseFailed, h.app().Status.Phase)
}

// Decision 6: the deadline is read from startedAt, so a new Reconciler (a restart) keeps it.
func TestAppRevisionDeadlineSurvivesRestart(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(setImage("oci-layout://todo-api:2"))
	require.Equal(t, 5*time.Minute, h.reconcile().RequeueAfter, "an unset UpgradeTimeout is 5m")
	h.clk.Advance(3 * time.Minute)
	h.restart()
	require.Equal(t, 2*time.Minute, h.reconcile().RequeueAfter)
	h.clk.Advance(2 * time.Minute)
	h.restart()
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
}

// scenario: app-prune-after-current (the reconciler half) and Decision 6 — the pass that writes a part neither
// switches nor prunes, even when nothing is Pending after it; a dropped object waits as Pruning NotCurrent.
func TestScenarioAppPruneAfterCurrent(t *testing.T) {
	t.Run("a changed Function", func(t *testing.T) {
		h := newHarness(t, nil)
		h.install(todoApp(nil))
		h.edit(func(a *v1.App) {
			a.Spec.Routes = nil
			setImage("oci-layout://todo-api:2")(a)
		})
		h.reconcile()
		require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.CurrentRevision)
		require.NotNil(t, h.get(v1.KindRoute, "todo-api"))
		require.Equal(t, v1.AppChild{Kind: v1.KindRoute, Name: "todo-api", State: v1.AppChildPruning, Reason: "NotCurrent"}, h.child(v1.KindRoute, "todo-api"))

		h.markReady(v1.KindFunction, "todo-api")
		require.Zero(t, h.reconcile())
		require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
		require.Nil(t, h.get(v1.KindRoute, "todo-api"), "pruned in the switch pass")
	})
	t.Run("a Bucket, Ready after its reconciler's pass", func(t *testing.T) {
		h := newHarness(t, nil)
		h.install(todoApp(nil))
		h.edit(func(a *v1.App) {
			a.Spec.Routes = nil
			a.Spec.Buckets[0].Prefixes = append(a.Spec.Buckets[0].Prefixes, v1.BucketPrefix{Name: "exports", Owner: "todo-api"})
		})
		h.reconcile()
		require.Len(t, h.get(v1.KindBucket, "todo-files").(*v1.Bucket).Spec.Prefixes, 2)
		require.Equal(t, v1.AppChild{Kind: v1.KindBucket, Name: "todo-files", State: v1.AppChildPending, Reason: "Progressing"},
			h.child(v1.KindBucket, "todo-files"), "Pending until the Bucket reconciler writes Ready at its generation (ADR-0215)")
		require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.CurrentRevision, "the pass wrote a part")
		require.NotNil(t, h.get(v1.KindRoute, "todo-api"))

		h.markReady(v1.KindBucket, "todo-files")
		h.reconcile()
		require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
		require.Nil(t, h.get(v1.KindRoute, "todo-api"))
	})
}

// Decision 6: a failed install is Failed without currentRevision or version, naming the first Pending part.
func TestAppRevisionFailedInstall(t *testing.T) {
	h := newHarness(t, nil)
	h.create(todoApp(versioned("1.0.0")))
	h.reconcile()
	h.clk.Advance(5 * time.Minute)
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Empty(t, got.Status.CurrentRevision)
	require.Empty(t, got.Status.Version)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.LatestRevision)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotReady", "KVStore/todo-store: Progressing")
	requireCond(t, revCond(t, h.rev(1), "ChildrenReady"), v1.ConditionFalse, "ChildNotReady", "KVStore/todo-store: Progressing")
	h.quiet()
}

// Decision 3 and 6: an upgrade stopped by ChildNotOwned stamps and writes no part, then fails with the stop reason;
// the App's Ready keeps the stop reason.
func TestAppRevisionStoppedUpgrade(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.create(&v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "todo-mail", Namespace: ns, ResourceGroup: "rg1"}, Spec: apiSpec()})
	before := h.versionsAll()
	h.edit(func(a *v1.App) {
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-mail", FunctionSpec: apiSpec()})
		a.Spec.Routes[0].Rules[0].Path = "/v2"
	})
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter, "the supervision period comes before the deadline")
	route := v1.ObjectRef{Kind: v1.KindRoute, Namespace: ns, Name: "todo-api"}
	require.Equal(t, before[route], h.versionsAll()[route], "no part is written")
	two := h.rev(2)
	require.NotNil(t, two, "the stamp precedes the ownership check")
	requireCond(t, revCond(t, two, "Applied"), v1.ConditionFalse, "ChildNotOwned", "Function/todo-mail: exists and is not owned by App/todo")
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
	require.Equal(t, "ChildNotOwned", h.ready().Reason)

	h.clk.Advance(5 * time.Minute)
	h.reconcile()
	two = h.rev(2)
	require.Equal(t, v1.PhaseFailed, two.Status.Phase)
	requireCond(t, revCond(t, two, "ChildrenReady"), v1.ConditionFalse, "ChildNotReady", "Function/todo-mail: ChildNotOwned: exists and is not owned by App/todo")
	got := h.app()
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotOwned", "Function/todo-mail: exists and is not owned by App/todo")
}

// scenario: app-history-kept (the reconciler half) and Decision 8 — the current revision and the newest
// RevisionHistory others are kept, Failed ones counting as others.
func TestScenarioAppHistoryKept(t *testing.T) {
	t.Run("five changes", func(t *testing.T) {
		h := newHarness(t, nil, func(d *app.Deps) { d.RevisionHistory = 2 })
		h.install(todoApp(versioned("1")))
		for _, v := range []string{"2", "3", "4", "5", "6"} {
			h.edit(versioned(v))
			h.reconcile()
		}
		require.Equal(t, v1.ObjectName("todo-6"), h.app().Status.CurrentRevision)
		require.Equal(t, []v1.ObjectName{"todo-4", "todo-5", "todo-6"}, h.revNames())
	})
	t.Run("with Failed revisions", func(t *testing.T) {
		h := newHarness(t, nil, func(d *app.Deps) { d.RevisionHistory = 2 })
		h.install(todoApp(nil))
		for _, image := range []string{"oci-layout://todo-api:2", "oci-layout://todo-api:3", "oci-layout://todo-api:4"} {
			h.edit(setImage(image))
			h.reconcile()
			h.clk.Advance(5 * time.Minute)
			h.reconcile()
		}
		require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.CurrentRevision)
		require.Equal(t, []v1.ObjectName{"todo-1", "todo-3", "todo-4"}, h.revNames())
		require.Equal(t, v1.PhaseFailed, h.rev(4).Status.Phase)
	})
}

// failing is a store whose next Update of one object fails once with err.
type failing struct {
	store.Store
	mu   sync.Mutex
	kind v1.Kind
	name v1.ObjectName
	err  error
}

func (s *failing) arm(kind v1.Kind, name v1.ObjectName, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kind, s.name, s.err = kind, name, err
}

func (s *failing) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	s.mu.Lock()
	hit := obj.GroupVersionKind().Kind == s.kind && obj.GetObjectMeta().Name == s.name
	err := s.err
	if hit {
		s.kind = ""
	}
	s.mu.Unlock()
	if hit {
		return nil, err
	}
	return s.Store.Update(ctx, obj)
}

var errInjected = fault.Unavailablef("store.Update", "injected failure")

// Decision 6: a pass that fails after any of its writes converges on the next.
func TestAppRevisionConvergesAfterAFailedWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind v1.Kind
		obj  v1.ObjectName
	}{
		{"the App status", v1.KindApp, "todo"},
		{"the previous revision", v1.KindAppRevision, "todo-1"},
		{"the new revision", v1.KindAppRevision, "todo-2"},
	} {
		t.Run("the switch, failing on "+tc.name, func(t *testing.T) {
			st := &failing{Store: store.New(memory.New())}
			h := newHarness(t, st)
			h.install(todoApp(nil))
			h.edit(setImage("oci-layout://todo-api:2"))
			h.reconcile()
			h.markReady(v1.KindFunction, "todo-api")
			st.arm(tc.kind, tc.obj, errInjected)
			_, err := h.reconcileErr()
			require.Error(t, err)

			h.reconcile()
			require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
			require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
			require.Equal(t, v1.PhaseReady, h.rev(2).Status.Phase)
			requireCond(t, revCond(t, h.rev(2), "Current"), v1.ConditionTrue, "", "")
			requireCond(t, revCond(t, h.rev(1), "Current"), v1.ConditionFalse, "Replaced", "replaced by todo-2")
			h.quiet()
		})
	}
	t.Run("the switch, a Conflict on the App status", func(t *testing.T) {
		st := &failing{Store: store.New(memory.New())}
		h := newHarness(t, st)
		h.install(todoApp(nil))
		h.edit(setImage("oci-layout://todo-api:2"))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-api")
		st.arm(v1.KindApp, "todo", fault.Conflictf("store.Update", "App %q resourceVersion mismatch", "todo"))
		h.reconcile()
		require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase, "the App changed: its next pass writes the record")
		requireCond(t, revCond(t, h.rev(1), "Current"), v1.ConditionTrue, "", "")

		h.reconcile()
		require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
		require.Equal(t, v1.PhaseReady, h.rev(2).Status.Phase)
	})
	t.Run("the supersede, failing on the superseded revision", func(t *testing.T) {
		st := &failing{Store: store.New(memory.New())}
		h := newHarness(t, st)
		h.install(todoApp(nil))
		h.edit(setImage("oci-layout://todo-api:2"))
		h.reconcile()
		h.edit(setImage("oci-layout://todo-api:3"))
		st.arm(v1.KindAppRevision, "todo-2", errInjected)
		_, err := h.reconcileErr()
		require.Error(t, err)
		require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)

		h.reconcile()
		require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
		requireCond(t, revCond(t, h.rev(2), "Current"), v1.ConditionFalse, "Superseded", "superseded by todo-3")
		h.quiet()
	})
	t.Run("the deadline, failing on the App status", func(t *testing.T) {
		st := &failing{Store: store.New(memory.New())}
		h := newHarness(t, st)
		h.install(todoApp(nil))
		h.edit(setImage("oci-layout://todo-api:2"))
		h.reconcile()
		h.clk.Advance(5 * time.Minute)
		st.arm(v1.KindApp, "todo", errInjected)
		_, err := h.reconcileErr()
		require.Error(t, err)
		require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase, "no revision status is written before the App's")

		h.reconcile()
		require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
		require.Equal(t, v1.PhaseFailed, h.app().Status.Phase)
		h.quiet()
	})
}

// Decision 1: the stamped revision carries the App's controller reference, namespace and resource group, and a
// frozen copy of the spec equal to the App's by json.Marshal.
func TestAppRevisionStamp(t *testing.T) {
	h := newHarness(t, nil)
	a := h.create(todoApp(nil)).(*v1.App)
	h.reconcile()
	rev := h.rev(1)
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: a.UID, Controller: true, BlockOwnerDeletion: true}}, rev.OwnerReferences)
	require.Equal(t, v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, rev.Spec.App)
	require.Equal(t, v1.ResourceGroupName("todo-rg"), rev.ResourceGroup)
	want, err := json.Marshal(a.Spec)
	require.NoError(t, err)
	got, err := json.Marshal(rev.Spec.Spec)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
	require.Equal(t, int64(1), rev.Generation, "only the status is written after create")
}
