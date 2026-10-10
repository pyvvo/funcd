package app_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/store"
)

// writeCounter counts every write to the store under it.
type writeCounter struct {
	store.Store
	writes atomic.Int64
}

func (s *writeCounter) Create(ctx context.Context, o v1.Object) (v1.Object, error) {
	s.writes.Add(1)
	return s.Store.Create(ctx, o)
}

func (s *writeCounter) Update(ctx context.Context, o v1.Object) (v1.Object, error) {
	s.writes.Add(1)
	return s.Store.Update(ctx, o)
}

func (s *writeCounter) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	s.writes.Add(1)
	return s.Store.Delete(ctx, gvk, ns, name, rv)
}

// plan plans a as a dry-run write would and checks that the planner wrote nothing.
func (h *harness) plan(a *v1.App) v1.AppPlan {
	h.t.Helper()
	st := &writeCounter{Store: h.st}
	got, err := app.NewPlanner(st).PlanApp(h.ctx, a)
	require.NoError(h.t, err)
	require.Zero(h.t, st.writes.Load(), "the planner writes nothing")
	return got
}

// dryRun is the stored App with mutate applied, as a dry-run replace admits it: the stored UID and resourceVersion.
func (h *harness) dryRun(mutate func(*v1.App)) *v1.App {
	h.t.Helper()
	a := h.app()
	if mutate != nil {
		mutate(a)
	}
	return a
}

// written runs one pass and returns each part it created or updated, in no order.
func (h *harness) written() []v1.ObjectRef {
	h.t.Helper()
	before := h.versionsAll()
	h.reconcile()
	var out []v1.ObjectRef
	for k, rv := range h.versionsAll() {
		if k.Kind != v1.KindApp && k.Kind != v1.KindAppRevision && before[k] != rv {
			out = append(out, k)
		}
	}
	return out
}

// writes are the plan's create and update lines without a reason, as the parts a pass writes.
func writes(p v1.AppPlan) []v1.ObjectRef {
	var out []v1.ObjectRef
	for _, l := range p.Parts {
		if (l.Action == v1.PlanCreate || l.Action == v1.PlanUpdate) && l.Reason == "" {
			out = append(out, v1.ObjectRef{Kind: l.Kind, Namespace: ns, Name: l.Name})
		}
	}
	return out
}

func line(kind v1.Kind, name v1.ObjectName, action v1.PlanAction) v1.PlanPart {
	return v1.PlanPart{Kind: kind, Name: name, Action: action}
}

// todoCreates are the plan lines of todoApp's parts on install, in section order.
func todoCreates() []v1.PlanPart {
	return []v1.PlanPart{
		line(v1.KindKVStore, "todo-store", v1.PlanCreate), line(v1.KindKVStore, "todo-cache", v1.PlanCreate),
		line(v1.KindBucket, "todo-files", v1.PlanCreate), line(v1.KindBucket, "todo-tmp", v1.PlanCreate),
		line(v1.KindFunction, "todo-api", v1.PlanCreate), line(v1.KindWorkflow, "todo-plan", v1.PlanCreate),
		line(v1.KindRoute, "todo-api", v1.PlanCreate),
	}
}

func withLegacy(a *v1.App) {
	a.Spec.Routes = append(a.Spec.Routes, v1.AppRoute{Name: "todo-legacy", RouteSpec: v1.RouteSpec{
		Rules: []v1.RouteRule{{Path: "/legacy", Backend: v1.RouteBackend{Function: "todo-api"}}},
	}})
}

// ADR-0220 Decision 5: a create plans <app>-1 and every part as create; the first pass writes exactly those parts.
func TestPlanCreate(t *testing.T) {
	h := newHarness(t, nil)
	got := h.plan(todoApp(nil))
	require.Equal(t, v1.AppPlan{Revision: "todo-1", Parts: todoCreates()}, got)
	h.create(todoApp(nil))
	require.ElementsMatch(t, writes(got), h.written())
}

// ADR-0220 Decision 5: an upgrade that changes todo-api and drops todo-legacy plans the next revision, the update and
// the prune; one pass writes exactly the update, and prune waits for the switch.
func TestPlanUpgradeMatchesOnePass(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(withLegacy))
	upgrade := func(a *v1.App) {
		a.Spec.Functions[0].Image = "oci-layout://todo-api:2"
		a.Spec.Routes = a.Spec.Routes[:1]
	}
	got := h.plan(h.dryRun(upgrade))
	require.Equal(t, v1.AppPlan{Revision: "todo-2", Parts: []v1.PlanPart{
		line(v1.KindFunction, "todo-api", v1.PlanUpdate), line(v1.KindRoute, "todo-legacy", v1.PlanPrune),
	}}, got)
	h.edit(upgrade)
	require.ElementsMatch(t, writes(got), h.written())
	require.NotNil(t, h.get(v1.KindRoute, "todo-legacy"))
}

// ADR-0220 Decision 5: the stored spec, and the same spec paused, name no revision and no part (ADR-0212 Decision 3).
func TestPlanUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	require.Equal(t, v1.AppPlan{}, h.plan(h.dryRun(nil)))
	require.Equal(t, v1.AppPlan{}, h.plan(h.dryRun(pause(true))))
}

// ADR-0220 Decision 5: a declared Secret missing, then lacking its key, is the plan's only line, with the revision
// named; the pass writes no part.
func TestPlanSecretStop(t *testing.T) {
	h := newHarness(t, nil)
	got := h.plan(configApp(nil))
	require.Equal(t, v1.AppPlan{Revision: "todo-1", Parts: []v1.PlanPart{
		{Kind: v1.KindSecret, Name: "todo-stripe-key", Reason: "SecretNotFound"},
	}}, got)
	h.create(configApp(nil))
	require.Empty(t, h.written())
	h.putSecret(map[string][]byte{"OTHER": []byte("x")})
	require.Equal(t, []v1.PlanPart{{Kind: v1.KindSecret, Name: "todo-stripe-key", Reason: "SecretKeyMissing"}},
		h.plan(h.dryRun(nil)).Parts)
	require.Empty(t, h.written())
}

// ADR-0220 Decision 5: with a pre-hook every part and the hook are listed; the first pass writes only what the
// pre-hook needs (ADR-0214 Decision 4).
func TestPlanPreHook(t *testing.T) {
	h := newHooked(t, nil)
	a := hookedTodo("1.0.0", nil)
	got := h.plan(a)
	parts := append(todoCreates()[:5], line(v1.KindFunction, "todo-migrate", v1.PlanCreate))
	parts = append(parts, todoCreates()[5:]...)
	require.Equal(t, v1.AppPlan{Revision: "todo-1", Parts: parts, Hooks: []string{"todo-migrate"}}, got)
	h.create(a)
	require.ElementsMatch(t, []v1.ObjectRef{
		{Kind: v1.KindKVStore, Namespace: ns, Name: "todo-store"}, {Kind: v1.KindFunction, Namespace: ns, Name: "todo-migrate"},
	}, h.written())
}

// ADR-0214 Decision 9: hooks are listed in call order, pre-hooks then post-hooks, a name in both lists twice.
func TestPlanHookOrder(t *testing.T) {
	h := newHarness(t, nil)
	a := hookedTodo("1.0.0", func(a *v1.App) {
		a.Spec.Hooks.PostApply = []v1.AppHook{{Function: "todo-api"}, {Function: "todo-migrate"}}
	})
	require.Equal(t, []string{"todo-migrate", "todo-api", "todo-migrate"}, h.plan(a).Hooks)
}

// ADR-0220 Decision 5: a part another owner holds, not first in section order, is the only line, with the revision
// named; the pass writes no part.
func TestPlanPartOfAnotherOwner(t *testing.T) {
	h := newHarness(t, nil)
	h.create(&v1.Route{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute},
		ObjectMeta: v1.ObjectMeta{Name: "todo-api", Namespace: ns, ResourceGroup: "todo-rg", OwnerReferences: []v1.OwnerReference{{
			ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "other"}, UID: "other-uid", Controller: true,
		}}},
		Spec: v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/x", Backend: v1.RouteBackend{Function: "x"}}}},
	})
	got := h.plan(todoApp(nil))
	require.Equal(t, v1.AppPlan{Revision: "todo-1", Parts: []v1.PlanPart{
		{Kind: v1.KindRoute, Name: "todo-api", Action: v1.PlanUpdate, Reason: "ChildNotOwned"},
	}}, got)
	h.create(todoApp(nil))
	require.Empty(t, h.written())
}

// ADR-0220 Decision 5: an AppRevision namesake another owner holds is the only line and no revision is named; a
// deleted incarnation's is dropped by the stamp, so the plan is the install's.
func TestPlanRevisionNamesake(t *testing.T) {
	t.Run("another owner", func(t *testing.T) {
		h := newHarness(t, nil)
		h.create(foreignRevision([]v1.OwnerReference{
			{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "todo"}, UID: "a-function-uid", Controller: true},
		}))
		require.Equal(t, v1.AppPlan{Parts: []v1.PlanPart{
			{Kind: v1.KindAppRevision, Name: "todo-1", Action: v1.PlanCreate, Reason: "ChildNotOwned"},
		}}, h.plan(todoApp(nil)))
		h.create(todoApp(nil))
		require.Empty(t, h.written())
	})
	t.Run("a deleted incarnation", func(t *testing.T) {
		h := newHarness(t, nil)
		h.create(foreignRevision([]v1.OwnerReference{
			{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: "an-earlier-uid", Controller: true},
		}))
		require.Equal(t, v1.AppPlan{Revision: "todo-1", Parts: todoCreates()}, h.plan(todoApp(nil)))
	})
}

// ADR-0220 Decision 5: every object the App controls that no entry names is listed as prune, users before stores
// (gc.Pairs order).
func TestPlanPrune(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(withLegacy))
	got := h.plan(h.dryRun(func(a *v1.App) {
		a.Spec.Routes = a.Spec.Routes[:1]
		a.Spec.KV = a.Spec.KV[:1]
		a.Spec.Functions[0].KV = a.Spec.Functions[0].KV[:1]
	}))
	require.Equal(t, "todo-2", string(got.Revision))
	require.Equal(t, []v1.PlanPart{
		line(v1.KindFunction, "todo-api", v1.PlanUpdate),
		line(v1.KindRoute, "todo-legacy", v1.PlanPrune), line(v1.KindKVStore, "todo-cache", v1.PlanPrune),
	}, got.Parts)
}
