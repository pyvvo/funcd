package app_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ADR-0339 Decisions 3 to 5: an App's revision records each add-on kind's revision, its part moves once the revision
// is applied, the switch waits for it, and the hooks are told.

// runPart gives the add-on part name the status the add-on reconciler writes (ADR-0339 Decisions 2 and 3): once
// todo's latest AppRevision is Applied, the part pins its record for the part's kind, and it is Ready at its generation.
func (h *harness) runPart(name v1.ObjectName) {
	h.t.Helper()
	o, ok := h.get(v1.KindAddonObject, name).(*v1.AddonObject)
	if !ok {
		return // not written yet
	}
	if latest := h.latestRevision(); latest != nil {
		if c, ok := latest.Status.Conditions.Get("Applied"); ok && c.Status == v1.ConditionTrue {
			o.Status.AppRevision = latest.Name
			for _, rec := range latest.Spec.AddonRevisions {
				if v1.SameAddonKind(rec.Kind, o.Spec.Kind) {
					o.Status.AddonRevision = rec.Revision
				}
			}
		}
	}
	setStatus(o, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", o.Generation))
	h.update(o)
}

// latestRevision is todo's AppRevision of the highest number, nil when it has none.
func (h *harness) latestRevision() *v1.AppRevision {
	h.t.Helper()
	res, err := h.st.List(h.ctx, v1.KindAppRevision.GVK(), store.ListOptions{Namespace: ns})
	require.NoError(h.t, err)
	var latest *v1.AppRevision
	for _, o := range res.Items {
		if r, ok := o.(*v1.AppRevision); ok && r.Spec.App.Name == "todo" && (latest == nil || r.Spec.Number > latest.Spec.Number) {
			latest = r
		}
	}
	return latest
}

// addonAt stores the Addon rauthy of kind IdentityProvider, its current revision rev, as its reconciler names it.
func (h *harness) addonAt(rev v1.ObjectName) {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, v1.KindAddon.GVK(), "", "rauthy")
	if err != nil {
		a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon},
			ObjectMeta: v1.ObjectMeta{Name: "rauthy"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809", Workloads: []v1.Workload{v1.WorkloadEngine},
				Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, Readiness: v1.AddonReadiness{Path: "/"}}}}}}
		obj = h.create(a)
	}
	a := obj.(*v1.Addon)
	a.Status.CurrentRevision = rev
	h.update(a)
}

func (h *harness) part() *v1.AddonObject {
	h.t.Helper()
	return h.get(v1.KindAddonObject, "todo-login").(*v1.AddonObject)
}

// loginTodo is hookedTodo with the IdentityProvider part todo-login and the post-hook todo-warm.
func loginTodo(version string, mutate func(*v1.App)) *v1.App {
	return hookedTodo(version, func(a *v1.App) {
		a.Spec.Addons = []v1.AppAddon{{Name: "todo-login", AddonObjectSpec: v1.AddonObjectSpec{Kind: "IdentityProvider"}}}
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-warm", FunctionSpec: apiSpec()})
		a.Spec.Hooks.PostApply = []v1.AppHook{{Function: "todo-warm"}}
		if mutate != nil {
			mutate(a)
		}
	})
}

// hookSeen is one hook call: its Function, the App revision it is for, and the revision todo-login ran then.
type hookSeen struct {
	fn, to, part v1.ObjectName
}

// partWatch wraps the hook invoker and records, at each call, the AddonRevision that todo-login runs.
type partWatch struct {
	app.Invoker
	st   store.Store
	mu   sync.Mutex
	seen []hookSeen
}

func (w *partWatch) Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	var in v1.AppHookInput
	_ = json.Unmarshal(ev.Data, &in)
	var rev v1.ObjectName
	if obj, err := w.st.Get(ctx, v1.KindAddonObject.GVK(), ns, "todo-login"); err == nil {
		rev = obj.(*v1.AddonObject).Status.AddonRevision
	}
	w.mu.Lock()
	w.seen = append(w.seen, hookSeen{fn: fn, to: in.To, part: rev})
	w.mu.Unlock()
	return w.Invoker.Invoke(ctx, ns, fn, ev)
}

// at is the revision todo-login ran when fn was called for to, "" with no such call.
func (w *partWatch) at(t *testing.T, fn, to v1.ObjectName) v1.ObjectName {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.seen {
		if s.fn == fn && s.to == to {
			return s.part
		}
	}
	t.Fatalf("no call of %s for %s in %+v", fn, to, w.seen)
	return ""
}

// newWatched is newHooked whose hook calls a partWatch records.
func newWatched(t *testing.T, opts ...func(*app.Deps)) (*hooked, *partWatch) {
	t.Helper()
	st := store.New(memory.New())
	w := &partWatch{st: st}
	wrap := func(d *app.Deps) {
		w.Invoker = d.Invoker
		d.Invoker = w
	}
	return newHooked(t, st, append([]func(*app.Deps){wrap}, opts...)...), w
}

// installed is a watched harness whose todo, its part on rauthy-1, is current at version 1.0.0.
func installed(t *testing.T, opts ...func(*app.Deps)) (*hooked, *partWatch) {
	t.Helper()
	h, w := newWatched(t, opts...)
	h.addonAt("rauthy-1")
	h.create(loginTodo("1.0.0", nil))
	h.converge("todo-1")
	require.Equal(t, []v1.AppAddonRevision{{Kind: "IdentityProvider", Revision: "rauthy-1"}}, h.rev(1).Spec.AddonRevisions)
	require.Equal(t, v1.ObjectName("rauthy-1"), h.part().Status.AddonRevision)
	return h, w
}

// scenario: addon-revision-app-upgrade — after the Addon moved to rauthy-2, todo's new revision records rauthy-2; its
// pre-hook gets rauthy-1 to rauthy-2 in its input and runs while the part runs rauthy-1, and its post-hook runs once
// the part runs rauthy-2.
//
// oracle: docs/design/addon/revisions.fizz#PostHooksSeeTheNewEngine
func TestScenarioAddonRevisionAppUpgrade(t *testing.T) {
	t.Parallel()
	h, w := installed(t)
	h.addonAt("rauthy-2")
	h.edit(upgradeTo("1.1.0"))
	h.converge("todo-2")
	rev := h.rev(2)
	require.Equal(t, []v1.AppAddonRevision{{Kind: "IdentityProvider", Revision: "rauthy-2"}}, rev.Spec.AddonRevisions)
	require.Equal(t, []v1.AppHookAddonRevision{{Kind: "IdentityProvider", Object: "todo-login", From: "rauthy-1", To: "rauthy-2"}},
		rev.Spec.HookInput.AddonRevisions)
	require.Equal(t, v1.ObjectName("rauthy-1"), w.at(t, "todo-migrate", "todo-2"), "the pre-hook sees the old engine")
	require.Equal(t, v1.ObjectName("rauthy-2"), w.at(t, "todo-warm", "todo-2"), "the post-hook sees the new engine")
	require.Equal(t, v1.ObjectName("todo-2"), h.part().Status.AppRevision)
}

// ADR-0339 Decision 4: a part Ready at its generation but not yet on the latest revision's record holds the switch.
func TestAddonRevisionSwitchWaitsForThePart(t *testing.T) {
	t.Parallel()
	h, _ := installed(t)
	h.addonAt("rauthy-2")
	h.edit(upgradeTo("1.1.0"))
	for range 4 {
		h.step()
		for _, c := range h.app().Status.Children {
			if c.Kind != "IdentityProvider" {
				h.markReady(c.Kind, c.Name)
			}
		}
	}
	require.NotEqual(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision, "the part still runs todo-1's record")
	child := h.child("IdentityProvider", "todo-login")
	require.Equal(t, v1.AppChildPending, child.State)
	require.Equal(t, "Progressing", child.Reason)
	h.converge("todo-2")
}

// scenario: addon-revision-app-rollback — rolled back to its revision that ran rauthy-1, todo's new revision records
// rauthy-1, and the part pins rauthy-1 again once it is applied.
//
// oracle: docs/design/addon/revisions.fizz#RecordsExist
func TestScenarioAddonRevisionAppRollback(t *testing.T) {
	t.Parallel()
	h, _ := installed(t)
	first := h.rev(1).Spec.Spec
	h.addonAt("rauthy-2")
	h.edit(upgradeTo("1.1.0"))
	h.converge("todo-2")
	require.Equal(t, v1.ObjectName("rauthy-2"), h.part().Status.AddonRevision)

	h.edit(func(a *v1.App) { a.Spec = first })
	h.converge("todo-3")
	rev := h.rev(3)
	require.Equal(t, []v1.AppAddonRevision{{Kind: "IdentityProvider", Revision: "rauthy-1"}}, rev.Spec.AddonRevisions,
		"the spec of todo-1, which was current once, takes back its records")
	require.Equal(t, "rollback", rev.Spec.HookInput.Event)
	require.Equal(t, v1.ObjectName("rauthy-1"), h.part().Status.AddonRevision)
}

// scenario: addon-revision-rollback-after-failure — todo-2 failed its deadline while the part ran rauthy-2; rolled
// back to todo-1, todo-3's hook input names the part from rauthy-2 to rauthy-1, and the part pins rauthy-1 once
// todo-3 is applied.
//
// oracle: docs/design/addon/revisions.fizz#HooksSeeTheMove
func TestScenarioAddonRevisionRollbackAfterFailure(t *testing.T) {
	t.Parallel()
	h, _ := installed(t, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
	first := h.rev(1).Spec.Spec
	h.addonAt("rauthy-2")
	h.edit(upgradeTo("1.1.0"))
	for range 4 { // todo-api's new generation never serves; the part runs as soon as todo-2 is applied
		h.step()
		h.markReady(v1.KindFunction, "todo-migrate")
		h.runPart("todo-login")
	}
	require.Equal(t, v1.ObjectName("rauthy-2"), h.part().Status.AddonRevision, "the part moved with todo-2")
	h.clk.Advance(time.Minute)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)

	h.edit(func(a *v1.App) { a.Spec = first })
	h.step()
	rev := h.rev(3)
	require.Equal(t, []v1.AppHookAddonRevision{{Kind: "IdentityProvider", Object: "todo-login", From: "rauthy-2", To: "rauthy-1"}},
		rev.Spec.HookInput.AddonRevisions, "from is what the part runs, not todo-1's record")
	h.converge("todo-3")
	require.Equal(t, v1.ObjectName("rauthy-1"), h.part().Status.AddonRevision)
}
