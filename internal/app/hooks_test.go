package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// invoked is one call the fake invoker received.
type invoked struct {
	fn       v1.ObjectName
	ev       eventing.CloudEvent
	deadline time.Time
}

// hookInvoker is the fake Invoker: it records each call and answers with fail's error for its Function; while gate is
// set, a call waits until it closes or the call's context ends.
type hookInvoker struct {
	mu    sync.Mutex
	calls []invoked
	fail  map[v1.ObjectName]error
	gate  chan struct{}
}

func (i *hookInvoker) Invoke(ctx context.Context, _ v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	d, _ := ctx.Deadline()
	i.mu.Lock()
	i.calls = append(i.calls, invoked{fn: fn, ev: ev, deadline: d})
	gate, err := i.gate, i.fail[fn]
	i.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (i *hookInvoker) failing(fn v1.ObjectName, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err == nil {
		delete(i.fail, fn)
		return
	}
	i.fail[fn] = err
}

// hold makes each later call wait until the returned func is called.
func (i *hookInvoker) hold() (release func()) {
	i.mu.Lock()
	defer i.mu.Unlock()
	gate := make(chan struct{})
	i.gate = gate
	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			i.gate = nil
			i.mu.Unlock()
			close(gate)
		})
	}
}

func (i *hookInvoker) received() []invoked {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]invoked(nil), i.calls...)
}

// heldGate is a platform hold (ADR-0206) that is held while held is set.
type heldGate struct{ held atomic.Bool }

func (g *heldGate) Held() bool          { return g.held.Load() }
func (*heldGate) ReleasedAt() time.Time { return time.Time{} }

type hooked struct {
	*harness
	inv   *hookInvoker
	ended chan controller.Request
}

func newHooked(t *testing.T, st store.Store, opts ...func(*app.Deps)) *hooked {
	t.Helper()
	h := &hooked{inv: &hookInvoker{fail: map[v1.ObjectName]error{}}, ended: make(chan controller.Request, 16)}
	wire := func(d *app.Deps) {
		d.Invoker = h.inv
		d.Enqueue = func(r controller.Request) { h.ended <- r }
	}
	h.harness = newHarness(t, st, append([]func(*app.Deps){wire}, opts...)...)
	return h
}

// called waits until a hook call ended: recorded or dropped, the App released and requeued.
func (h *hooked) called() {
	h.t.Helper()
	select {
	case req := <-h.ended:
		require.Equal(h.t, controller.Request{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "todo"}, req)
	case <-time.After(5 * time.Second):
		h.t.Fatal("no hook call ended")
	}
}

// noCall checks that no hook call starts.
func (h *hooked) noCall() {
	h.t.Helper()
	n := len(h.inv.received())
	select {
	case <-h.ended:
		h.t.Fatal("a hook call ended")
	case <-time.After(50 * time.Millisecond):
	}
	require.Len(h.t, h.inv.received(), n, "no hook is called")
}

// step reconciles and waits for a call the pass started.
func (h *hooked) step() {
	h.t.Helper()
	h.reconcile()
	select {
	case <-h.ended:
	case <-time.After(100 * time.Millisecond):
	}
}

// converge runs passes, every part made Ready after each, until rev is current, every hook done and the App Ready.
func (h *hooked) converge(rev v1.ObjectName) {
	h.t.Helper()
	for range 12 {
		h.step()
		h.markAllReady()
		a := h.app()
		r := h.get(v1.KindAppRevision, rev).(*v1.AppRevision)
		if a.Status.CurrentRevision == rev && a.Status.Phase == v1.PhaseReady && hooksDone(r) {
			return
		}
	}
	h.t.Fatalf("%s does not become current with its hooks done: %+v", rev, h.app().Status)
}

// hooksDone reports whether every hook of r has a Ready call.
func hooksDone(r *v1.AppRevision) bool {
	if r.Spec.Spec.Hooks == nil {
		return true
	}
	for point, hooks := range map[string][]v1.AppHook{"preApply": r.Spec.Spec.Hooks.PreApply, "postApply": r.Spec.Spec.Hooks.PostApply} {
		for _, hk := range hooks {
			if !slices.ContainsFunc(r.Status.Hooks, func(c v1.AppHookCall) bool {
				return c.Point == point && c.Function == hk.Function && c.Phase == v1.PhaseReady
			}) {
				return false
			}
		}
	}
	return true
}

func (h *hooked) retry() error { return h.r.Retry(h.ctx, ns, "todo") }

// migrateSpec is todo-migrate, bound to table todos of todo-store, which todo-api owns.
func migrateSpec() v1.FunctionSpec {
	return v1.FunctionSpec{
		Runtime: "nodejs22",
		Handler: "index.handler",
		Image:   "oci-layout://todo-migrate:1",
		KV:      []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}},
	}
}

// hookedTodo is ADR-0214's fixture on todoApp: todo-migrate, its pre-hook, at version.
func hookedTodo(version string, mutate func(*v1.App)) *v1.App {
	return todoApp(func(a *v1.App) {
		a.Spec.Version = version
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-migrate", FunctionSpec: migrateSpec()})
		a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "todo-migrate"}}}
		if mutate != nil {
			mutate(a)
		}
	})
}

// upgradeTo sets a new todo-api image and version.
func upgradeTo(version string) func(*v1.App) {
	return func(a *v1.App) {
		a.Spec.Version = version
		a.Spec.Functions[0].Image = "oci-layout://todo-api:" + version
	}
}

func input(t *testing.T, ev eventing.CloudEvent) v1.AppHookInput {
	t.Helper()
	var in v1.AppHookInput
	require.NoError(t, json.Unmarshal(ev.Data, &in))
	return in
}

func (h *hooked) invocation(name v1.ObjectName) *v1.Invocation {
	h.t.Helper()
	obj := h.get(v1.KindInvocation, name)
	require.NotNil(h.t, obj, "Invocation/%s", name)
	return obj.(*v1.Invocation)
}

var errHook500 = fault.Unavailablef("sensor.Invoke", "function default/todo-migrate returned status 500: no such column")

// scenario: app-pre-hook-migrates (the reconciler half) — before its pre-hook is done a pass writes only the pre-hook
// Function and the store it binds; the call carries the frozen input; an Invocation owned by the revision and an
// AppHookCall record it; only then do the other parts change, and the revision becomes current.
func TestScenarioAppPreHookMigrates(t *testing.T) {
	h := newHooked(t, nil)
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	require.NotNil(t, h.get(v1.KindKVStore, "todo-store"))
	require.NotNil(t, h.get(v1.KindFunction, "todo-migrate"))
	for _, p := range []struct {
		kind v1.Kind
		name v1.ObjectName
	}{{v1.KindKVStore, "todo-cache"}, {v1.KindBucket, "todo-files"}, {v1.KindFunction, "todo-api"}, {v1.KindWorkflow, "todo-plan"}, {v1.KindRoute, "todo-api"}} {
		require.Nil(t, h.get(p.kind, p.name), "%s/%s waits for the pre-hook", p.kind, p.name)
	}
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "Progressing", "pre-hook Function/todo-migrate")
	requireCond(t, revCond(t, h.rev(1), "Applied"), v1.ConditionFalse, "Progressing", "pre-hook Function/todo-migrate")
	requireCond(t, revCond(t, h.rev(1), "Current"), v1.ConditionFalse, "Progressing", "")
	h.noCall()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")
	require.Len(t, h.inv.received(), 1)

	before := h.versionsAll()
	api := keyOf(h.get(v1.KindFunction, "todo-api"))
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	h.called()
	require.Equal(t, before[api], h.versionsAll()[api], "todo-api waits for the pre-hook")
	got := h.inv.received()
	require.Len(t, got, 2)
	call := got[1]
	two := h.rev(2)
	require.Len(t, two.Status.Hooks, 1)
	rec := two.Status.Hooks[0]
	require.Equal(t, "preApply", rec.Point)
	require.Equal(t, v1.ObjectName("todo-migrate"), rec.Function)
	require.Equal(t, v1.PhaseReady, rec.Phase)
	require.Equal(t, v1.ObjectName("todo-migrate"), call.fn)
	require.Equal(t, eventing.CloudEvent{
		SpecVersion: "1.0", ID: string(rec.Invocation), Source: "funcd://default/app/todo", Type: "preApply",
		Time: v1.NewTimestamp(h.clk.Now()), DataContentType: "application/json", Data: call.ev.Data,
	}, call.ev)
	require.Equal(t, v1.AppHookInput{Event: "upgrade", App: "todo", From: "todo-1", To: "todo-2", FromVersion: "3.0.0", ToVersion: "4.0.0"}, input(t, call.ev))
	require.Equal(t, two.Spec.HookInput, &v1.AppHookInput{Event: "upgrade", App: "todo", From: "todo-1", To: "todo-2", FromVersion: "3.0.0", ToVersion: "4.0.0"})
	require.WithinDuration(t, time.Now().Add(v1.DefaultInvokeTimeout), call.deadline, 5*time.Second, "invoke.defaultTimeout bounds the call")
	require.Regexp(t, `^inv-[0-9a-f]{20}$`, string(rec.Invocation))
	inv := h.invocation(rec.Invocation)
	require.Equal(t, v1.PhaseReady, inv.Status.Phase)
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindAppRevision, Namespace: ns, Name: "todo-2"}, UID: two.UID, Controller: true, BlockOwnerDeletion: true}},
		inv.OwnerReferences)
	require.Equal(t, v1.ResourceGroupName("todo-rg"), inv.ResourceGroup)

	h.converge("todo-2")
	require.Equal(t, "oci-layout://todo-api:4.0.0", h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Image)
	require.Len(t, h.inv.received(), 2, "a done hook is not called again")
}

// scenario: app-pre-hook-fails-then-retry (the reconciler half) — a failed pre-hook fails the revision with HookFailed
// before any part but the hook's changes; retry calls it again, reopens the revision and the rollout completes; a
// second retry has no failed hook.
func TestScenarioAppPreHookFailsThenRetry(t *testing.T) {
	h := newHooked(t, nil)
	h.create(hookedTodo("3.0.0", nil))
	h.markReady(v1.KindFunction, "todo-migrate")
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")

	h.inv.failing("todo-migrate", errHook500)
	before := h.versionsAll()
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	h.called()
	h.reconcile()
	two := h.rev(2)
	require.Equal(t, v1.PhaseFailed, two.Status.Phase)
	inv := two.Status.Hooks[0].Invocation
	msg := "Function/todo-migrate: Invocation/" + string(inv) + ": " + errHook500.Error()
	requireCond(t, revCond(t, two, "Applied"), v1.ConditionFalse, "HookFailed", msg)
	requireCond(t, revCond(t, two, "Current"), v1.ConditionFalse, "HookFailed", msg)
	got := h.app()
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	requireCond(t, h.ready(), v1.ConditionFalse, "HookFailed", msg)
	require.Equal(t, v1.PhaseFailed, h.invocation(inv).Status.Phase)
	require.Equal(t, errHook500.Error(), h.invocation(inv).Status.Error)
	after := h.versionsAll()
	for k, rv := range before {
		if k.Kind != v1.KindApp && k.Kind != v1.KindAppRevision {
			require.Equal(t, rv, after[k], "%s/%s is not written", k.Kind, k.Name)
		}
	}
	h.quiet()
	h.noCall()

	h.inv.failing("todo-migrate", nil)
	require.NoError(t, h.retry())
	h.called()
	two = h.rev(2)
	require.Equal(t, []v1.Phase{v1.PhaseFailed, v1.PhaseReady}, []v1.Phase{two.Status.Hooks[0].Phase, two.Status.Hooks[1].Phase})
	require.Equal(t, v1.PhaseDeploying, two.Status.Phase, "a Ready retry reopens the revision")
	h.reconcile()
	requireCond(t, revCond(t, h.rev(2), "Current"), v1.ConditionFalse, "Progressing", "")
	h.converge("todo-2")
	require.Len(t, h.inv.received(), 3)

	err := h.retry()
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "app todo has no failed hook")
}

// warmTodo adds Function todo-warm as the post-hook, and Route todo-legacy.
func warmTodo(version string) *v1.App {
	return hookedTodo(version, func(a *v1.App) {
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-warm", FunctionSpec: apiSpec()})
		a.Spec.Hooks.PostApply = []v1.AppHook{{Function: "todo-warm"}}
		a.Spec.Routes = append(a.Spec.Routes, v1.AppRoute{Name: "todo-legacy", RouteSpec: v1.RouteSpec{
			Rules: []v1.RouteRule{{Path: "/legacy", Backend: v1.RouteBackend{Function: "todo-api"}}},
		}})
	})
}

// scenario: app-post-hook-fails (the reconciler half) — the revision is current and Ready, the dropped Route waits as
// a Pruning child with no reason and the App is Degraded HookFailed; once a retry succeeds the Route is pruned and the
// App is Ready.
func TestScenarioAppPostHookFails(t *testing.T) {
	h := newHooked(t, nil)
	h.create(warmTodo("3.0.0"))
	h.converge("todo-1")
	require.Len(t, h.inv.received(), 2)
	require.Equal(t, "postApply", h.rev(1).Status.Hooks[1].Point)

	errWarm := fault.Unavailablef("sensor.Invoke", "function default/todo-warm returned status 500: cache down")
	h.inv.failing("todo-warm", errWarm)
	h.edit(func(a *v1.App) {
		upgradeTo("4.0.0")(a)
		a.Spec.Routes = a.Spec.Routes[:1]
	})
	for range 4 {
		h.step()
		h.markAllReady()
	}
	h.reconcile()
	two := h.rev(2)
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.PhaseReady, two.Status.Phase)
	requireCond(t, revCond(t, two, "Current"), v1.ConditionTrue, "", "")
	require.Equal(t, v1.PhaseFailed, two.Status.Hooks[len(two.Status.Hooks)-1].Phase)
	inv := two.Status.Hooks[len(two.Status.Hooks)-1].Invocation
	require.NotNil(t, h.get(v1.KindRoute, "todo-legacy"), "prune waits for the post-hooks")
	require.Equal(t, v1.AppChild{Kind: v1.KindRoute, Name: "todo-legacy", State: v1.AppChildPruning}, h.child(v1.KindRoute, "todo-legacy"))
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "HookFailed", "Function/todo-warm: Invocation/"+string(inv)+": "+errWarm.Error())
	h.step()
	h.noCall()

	h.inv.failing("todo-warm", nil)
	require.NoError(t, h.retry())
	h.called()
	h.reconcile()
	require.Nil(t, h.get(v1.KindRoute, "todo-legacy"))
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
}

// Decision 4: postApply [A, B] with A failed calls no B until a retry of A succeeds.
func TestAppPostHooksInOrder(t *testing.T) {
	h := newHooked(t, nil)
	h.inv.failing("todo-warm", errHook500)
	h.create(hookedTodo("1.0.0", func(a *v1.App) {
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-warm", FunctionSpec: apiSpec()})
		a.Spec.Hooks = &v1.AppHooks{PostApply: []v1.AppHook{{Function: "todo-warm"}, {Function: "todo-migrate"}}}
	}))
	for range 5 {
		h.step()
		h.markAllReady()
	}
	fns := func() []v1.ObjectName {
		var out []v1.ObjectName
		for _, c := range h.inv.received() {
			out = append(out, c.fn)
		}
		return out
	}
	require.Equal(t, []v1.ObjectName{"todo-warm"}, fns(), "B waits for A")
	require.Equal(t, "HookFailed", h.ready().Reason)
	h.inv.failing("todo-warm", nil)
	require.NoError(t, h.retry())
	h.called()
	h.step()
	require.Equal(t, []v1.ObjectName{"todo-warm", "todo-warm", "todo-migrate"}, fns())
	h.reconcile()
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
}

// scenario: app-hook-event (the reconciler half) and Decision 2 — the event rule: install, a failed install,
// upgrade, rollback, and a rollback beyond the history reads upgrade.
func TestScenarioAppHookEvent(t *testing.T) {
	t.Run("install writes the store first and has no from", func(t *testing.T) {
		h := newHooked(t, nil)
		h.create(hookedTodo("3.0.0", nil))
		h.reconcile()
		require.NotNil(t, h.get(v1.KindKVStore, "todo-store"))
		h.converge("todo-1")
		require.Equal(t, v1.AppHookInput{Event: "install", App: "todo", To: "todo-1", ToVersion: "3.0.0"}, input(t, h.inv.received()[0].ev))
	})
	t.Run("a failed install", func(t *testing.T) {
		h := newHooked(t, nil, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
		h.create(hookedTodo("1.0.0", nil))
		h.reconcile()
		h.clk.Advance(time.Minute)
		h.reconcile()
		require.Equal(t, v1.PhaseFailed, h.rev(1).Status.Phase)
		h.edit(upgradeTo("1.0.1"))
		h.markReady(v1.KindFunction, "todo-migrate")
		h.converge("todo-2")
		require.Equal(t, &v1.AppHookInput{Event: "install", App: "todo", To: "todo-2", ToVersion: "1.0.1"}, h.rev(2).Spec.HookInput)
	})
	t.Run("rollback, and beyond the history upgrade", func(t *testing.T) {
		h := newHooked(t, nil, func(d *app.Deps) { d.RevisionHistory = 1 })
		h.create(hookedTodo("3.0.0", nil))
		h.converge("todo-1")
		h.edit(upgradeTo("4.0.0"))
		h.converge("todo-2")
		h.edit(func(a *v1.App) { a.Spec = hookedTodo("3.0.0", nil).Spec })
		h.converge("todo-3")
		require.Equal(t, &v1.AppHookInput{Event: "rollback", App: "todo", From: "todo-2", To: "todo-3", FromVersion: "4.0.0", ToVersion: "3.0.0"},
			h.rev(3).Spec.HookInput)
		require.Nil(t, h.rev(1), "the history keeps one revision besides the current")
		h.edit(upgradeTo("5.0.0"))
		h.converge("todo-4")
		h.edit(func(a *v1.App) { a.Spec = hookedTodo("3.0.0", upgradeTo("4.0.0")).Spec })
		h.converge("todo-5")
		require.Nil(t, h.rev(2))
		require.Equal(t, "upgrade", h.rev(5).Spec.HookInput.Event, "todo-2 was trimmed: the same spec reads as an upgrade")
	})
	t.Run("no hook, no input", func(t *testing.T) {
		h := newHooked(t, nil)
		h.install(todoApp(nil))
		require.Nil(t, h.rev(1).Spec.HookInput)
		require.Empty(t, h.inv.received())
	})
}

// cancellable gives the harness's passes a context that funcd's shutdown would end, and returns its cancel.
func (h *hooked) cancellable() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	return cancel
}

// scenario: app-hook-repeats-after-restart (the reconciler half) — a call cut by a shutdown after more than
// app.upgradeTimeout is not recorded; a busy pass fails nothing by the deadline; the restarted reconciler calls it
// again and the rollout continues.
func TestScenarioAppHookRepeatsAfterRestart(t *testing.T) {
	h := newHooked(t, nil, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
	stop := h.cancellable()
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")

	release := h.inv.hold()
	defer release()
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	require.Eventually(t, func() bool { return len(h.inv.received()) == 2 }, 5*time.Second, time.Millisecond)
	h.clk.Advance(2 * time.Minute)
	h.reconcile()
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase, "a busy pass fails no revision by the deadline")
	h.noCall()

	stop()
	h.called()
	h.ctx = context.Background()
	require.Empty(t, h.rev(2).Status.Hooks, "a call the shutdown ended is not recorded")
	require.Len(t, h.list(v1.KindInvocation), 1, "nor is its Invocation: only the install call has one")
	release()
	h.restart()
	h.reconcile()
	h.called()
	require.Len(t, h.inv.received(), 3, "the restarted reconciler calls it again")
	require.Equal(t, v1.PhaseReady, h.rev(2).Status.Hooks[0].Phase)
	h.converge("todo-2")
}

func (h *hooked) list(kind v1.Kind) []v1.Object {
	h.t.Helper()
	res, err := h.st.List(h.ctx, kind.GVK(), store.ListOptions{Namespace: ns})
	require.NoError(h.t, err)
	return res.Items
}

// failCreate fails the next Create of kind.
type failCreate struct {
	store.Store
	armed atomic.Pointer[v1.Kind]
}

func (s *failCreate) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if k := s.armed.Load(); k != nil && *k == obj.GroupVersionKind().Kind && s.armed.CompareAndSwap(k, nil) {
		return nil, errInjected
	}
	return s.Store.Create(ctx, obj)
}

// Decision 6: a store write that fails after a call longer than app.upgradeTimeout records nothing; the next pass calls
// the hook again and the rollout continues.
func TestAppFailedRecordWriteCallsAgain(t *testing.T) {
	for _, kind := range []v1.Kind{v1.KindInvocation, v1.KindAppRevision} {
		t.Run(string(kind), func(t *testing.T) {
			fc := &failCreate{Store: store.New(memory.New())}
			fu := &failing{Store: fc}
			h := newHooked(t, fu, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
			h.create(hookedTodo("3.0.0", nil))
			h.reconcile()
			h.markReady(v1.KindFunction, "todo-migrate")
			h.converge("todo-1")

			h.edit(upgradeTo("4.0.0"))
			release := h.inv.hold()
			defer release()
			h.reconcile()
			require.Eventually(t, func() bool { return len(h.inv.received()) == 2 }, 5*time.Second, time.Millisecond)
			h.clk.Advance(2 * time.Minute)
			if kind == v1.KindInvocation {
				fc.armed.Store(&kind)
			} else {
				fu.arm(v1.KindAppRevision, "todo-2", errInjected)
			}
			release()
			h.called()
			require.Empty(t, h.rev(2).Status.Hooks)
			h.reconcile()
			h.called()
			require.Len(t, h.inv.received(), 3)
			h.converge("todo-2")
		})
	}
}

// Decision 4: a pass stopped by ChildNotOwned or by a missing Secret starts no call though the hook's Function is
// Ready.
func TestAppStoppedPassStartsNoHookCall(t *testing.T) {
	for name, stop := range map[string]func(h *hooked){
		"ChildNotOwned": func(h *hooked) {
			h.create(&v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute},
				ObjectMeta: v1.ObjectMeta{Name: "todo-new", Namespace: ns, ResourceGroup: "rg1"},
				Spec:       v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/new", Backend: v1.RouteBackend{Function: "todo-api"}}}}})
			h.edit(func(a *v1.App) {
				a.Spec.Routes = append(a.Spec.Routes, v1.AppRoute{Name: "todo-new", RouteSpec: v1.RouteSpec{
					Rules: []v1.RouteRule{{Path: "/new", Backend: v1.RouteBackend{Function: "todo-api"}}},
				}})
			})
		},
		"SecretNotFound": func(h *hooked) {
			h.edit(func(a *v1.App) {
				a.Spec.Secrets = []v1.AppSecret{{Name: "todo-stripe-key", Keys: []string{"STRIPE_API_KEY"}}}
				a.Spec.Functions[0].Secrets = []v1.ObjectName{"todo-stripe-key"}
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHooked(t, nil)
			h.create(hookedTodo("3.0.0", nil))
			h.reconcile()
			h.markReady(v1.KindFunction, "todo-migrate")
			h.converge("todo-1")
			stop(h)
			h.reconcile()
			h.noCall()
			require.Equal(t, name, h.ready().Reason)
			require.Empty(t, h.rev(2).Status.Hooks)
		})
	}
}

// Decision 4: a due pre-hook whose Function is not ready at the deadline fails the revision with a HookFailed retry
// reopens; the retry's call wakes the Function and the rollout completes.
func TestAppHookFunctionNotReadyFailsRetryably(t *testing.T) {
	h := newHooked(t, nil, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")

	h.edit(func(a *v1.App) {
		upgradeTo("4.0.0")(a)
		a.Spec.Functions[1].Image = "oci-layout://todo-migrate:2"
	})
	require.Equal(t, time.Minute, h.reconcile().RequeueAfter)
	h.noCall()
	h.clk.Advance(time.Minute)
	h.reconcile()
	two := h.rev(2)
	require.Equal(t, v1.PhaseFailed, two.Status.Phase)
	requireCond(t, revCond(t, two, "Applied"), v1.ConditionFalse, "HookFailed", "Function/todo-migrate: not ready")
	requireCond(t, h.ready(), v1.ConditionFalse, "HookFailed", "Function/todo-migrate: not ready")
	require.Equal(t, v1.PhaseFailed, h.app().Status.Phase)
	h.reconcile()
	h.noCall()

	require.NoError(t, h.retry())
	h.called()
	require.Equal(t, v1.PhaseDeploying, h.rev(2).Status.Phase)
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-2")
}

// Decision 4 and 7: a revision failed by its deadline while a stop held it calls no hook once the stop clears, and
// retry has no failed hook for it.
func TestAppDeadlineFailedRevisionCallsNoHook(t *testing.T) {
	h := newHooked(t, nil, func(d *app.Deps) { d.UpgradeTimeout = time.Minute })
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")
	h.edit(func(a *v1.App) {
		a.Spec.Secrets = []v1.AppSecret{{Name: "todo-stripe-key", Keys: []string{"STRIPE_API_KEY"}}}
		a.Spec.Functions[0].Secrets = []v1.ObjectName{"todo-stripe-key"}
	})
	h.reconcile()
	h.clk.Advance(time.Minute)
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
	requireCond(t, revCond(t, h.rev(2), "Current"), v1.ConditionFalse, "ChildNotReady", "")

	h.putSecret(stripeKey("v1"))
	h.reconcile()
	h.noCall()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
	err := h.retry()
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "app todo has no failed hook")
}

// Decision 7: two concurrent retries start one call; the other is refused while it runs.
func TestAppConcurrentRetriesStartOneCall(t *testing.T) {
	h := newHooked(t, nil)
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")
	h.inv.failing("todo-migrate", errHook500)
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	h.called()
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)

	release := h.inv.hold()
	defer release()
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- h.retry() }()
	}
	first, second := <-errs, <-errs
	if first != nil {
		first, second = second, first
	}
	require.NoError(t, first)
	require.Equal(t, fault.Conflict, fault.KindOf(second), "%v", second)
	require.ErrorContains(t, second, "a hook call of app todo is running")
	h.reconcile()
	release()
	h.called()
	require.Len(t, h.inv.received(), 3, "one retry call; the pass started none while it ran")
}

// listHook runs onList right after a List of AppRevisions read the store.
type listHook struct {
	store.Store
	onList atomic.Pointer[func()]
}

func (s *listHook) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	res, err := s.Store.List(ctx, gvk, opts)
	if f := s.onList.Swap(nil); f != nil && gvk.Kind == v1.KindAppRevision {
		(*f)()
	}
	return res, err
}

// Decision 4: a pass notes busy before it reads the revisions, so a call recorded after that read is not started
// again by it.
func TestAppCallRecordedAfterBusyReadNotRepeated(t *testing.T) {
	st := &listHook{Store: store.New(memory.New())}
	h := newHooked(t, st)
	h.create(hookedTodo("3.0.0", nil))
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")

	release := h.inv.hold()
	h.edit(upgradeTo("4.0.0"))
	h.reconcile()
	require.Eventually(t, func() bool { return len(h.inv.received()) == 2 }, 5*time.Second, time.Millisecond)
	finish := func() {
		release()
		h.called()
	}
	st.onList.Store(&finish)
	h.reconcile()
	require.Len(t, h.inv.received(), 2, "the pass read busy before the call ended")
	h.noCall()
	require.Len(t, h.rev(2).Status.Hooks, 1)
	h.converge("todo-2")
	require.Len(t, h.inv.received(), 2)
}

// Decision 8: a held pass writes nothing, a paused App's too, and retry is Unavailable; a call ending while the App
// is paused records nothing, and the next pass after the resume calls it again.
func TestAppHeldOrPausedRecordsNothing(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		gate := &heldGate{}
		h := newHooked(t, nil, func(d *app.Deps) { d.Hold = gate })
		h.create(hookedTodo("3.0.0", nil))
		gate.held.Store(true)
		before := h.versionsAll()
		require.Equal(t, controller.Result{RequeueAfter: controller.SupervisionPeriod}, h.reconcile())
		require.Equal(t, before, h.versionsAll(), "a held pass writes nothing")
		h.edit(pause(true))
		before = h.versionsAll()
		require.Equal(t, controller.Result{RequeueAfter: controller.SupervisionPeriod}, h.reconcile())
		require.Equal(t, before, h.versionsAll(), "a held pass of a paused App writes nothing")
		require.Empty(t, h.revNames())
		err := h.retry()
		require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	})
	t.Run("a call ending while held", func(t *testing.T) {
		gate := &heldGate{}
		h := newHooked(t, nil, func(d *app.Deps) { d.Hold = gate })
		h.create(hookedTodo("3.0.0", nil))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-migrate")
		release := h.inv.hold()
		h.reconcile()
		require.Eventually(t, func() bool { return len(h.inv.received()) == 1 }, 5*time.Second, time.Millisecond)
		gate.held.Store(true)
		release()
		h.called()
		require.Empty(t, h.rev(1).Status.Hooks)
		require.Empty(t, h.list(v1.KindInvocation))
		gate.held.Store(false)
		h.converge("todo-1")
		require.Len(t, h.inv.received(), 2)
	})
	t.Run("a call ending while paused", func(t *testing.T) {
		h := newHooked(t, nil)
		h.create(hookedTodo("3.0.0", nil))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-migrate")
		release := h.inv.hold()
		h.reconcile()
		require.Eventually(t, func() bool { return len(h.inv.received()) == 1 }, 5*time.Second, time.Millisecond)
		h.edit(pause(true))
		release()
		h.called()
		require.Empty(t, h.rev(1).Status.Hooks)
		require.Empty(t, h.list(v1.KindInvocation))
		h.reconcile()
		h.noCall()
		err := h.retry()
		require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, "app todo is paused")

		h.edit(pause(false))
		h.converge("todo-1")
		require.Len(t, h.inv.received(), 2)
	})
}

// Decision 7: retry answers NotFound without the App and Conflict without a failed hook.
func TestAppRetryRefusals(t *testing.T) {
	h := newHooked(t, nil)
	require.Equal(t, fault.NotFound, fault.KindOf(h.retry()))
	h.install(todoApp(nil))
	err := h.retry()
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "app todo has no failed hook")
	require.Empty(t, h.inv.received())
}

// Decision 5: a hook Function's spec.timeout bounds its call; without an Invoker every call fails Unavailable.
func TestAppHookCallBounds(t *testing.T) {
	t.Run("spec.timeout", func(t *testing.T) {
		h := newHooked(t, nil, func(d *app.Deps) { d.InvokeTimeout = time.Hour })
		h.create(hookedTodo("3.0.0", func(a *v1.App) { a.Spec.Functions[1].Timeout = v1.Duration(90 * time.Second) }))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-migrate")
		h.converge("todo-1")
		require.WithinDuration(t, time.Now().Add(90*time.Second), h.inv.received()[0].deadline, 5*time.Second)
	})
	t.Run("invoke.defaultTimeout", func(t *testing.T) {
		h := newHooked(t, nil, func(d *app.Deps) { d.InvokeTimeout = time.Hour })
		h.create(hookedTodo("3.0.0", nil))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-migrate")
		h.converge("todo-1")
		require.WithinDuration(t, time.Now().Add(time.Hour), h.inv.received()[0].deadline, 5*time.Second)
	})
	t.Run("no invoker", func(t *testing.T) {
		h := newHooked(t, nil, func(d *app.Deps) { d.Invoker = nil })
		h.create(hookedTodo("3.0.0", nil))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-migrate")
		h.reconcile()
		h.called()
		h.reconcile()
		require.Equal(t, "HookFailed", h.ready().Reason)
		require.Contains(t, h.ready().Message, "no invoker")
	})
	_, err := app.NewReconciler(app.Deps{Store: store.New(memory.New()), Purger: &purges{}, InvokeTimeout: -time.Second})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// Decision 4: an install writes, before its pre-hook, the ConfigMap its config names and the stores it binds, directly
// or through a catalog, and no other part; an upgrade adding a table it binds writes the store and the hook first.
func TestAppPreHookPartsFirst(t *testing.T) {
	h := newHooked(t, nil)
	h.create(hookedTodo("3.0.0", func(a *v1.App) {
		a.Spec.ConfigMaps = []v1.AppConfigMap{{Name: "todo-settings", ConfigMapSpec: paris()}}
		a.Spec.Catalogs = []v1.AppCatalog{{Name: "todo-lake", CatalogServiceSpec: v1.CatalogServiceSpec{
			Blob:    []v1.FunctionBlob{{Alias: "lake", Bucket: "todo-files", Prefix: "lake"}},
			Catalog: v1.CatalogRef{Bucket: "todo-files", Prefix: "lake"},
		}}}
		m := &a.Spec.Functions[1]
		m.Config = []v1.ObjectName{"todo-settings"}
		m.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "todo-lake"}}
	}))
	h.reconcile()
	for _, k := range []v1.ObjectRef{
		{Kind: v1.KindConfigMap, Name: storedSettings(paris())}, {Kind: v1.KindKVStore, Name: "todo-store"},
		{Kind: v1.KindBucket, Name: "todo-files"}, {Kind: v1.KindCatalogService, Name: "todo-lake"}, {Kind: v1.KindFunction, Name: "todo-migrate"},
	} {
		require.NotNil(t, h.get(k.Kind, k.Name), "%s/%s is written before the pre-hook", k.Kind, k.Name)
	}
	require.Equal(t, []v1.ObjectName{storedSettings(paris())}, h.get(v1.KindFunction, "todo-migrate").(*v1.Function).Spec.Config)
	for _, k := range []v1.ObjectRef{{Kind: v1.KindKVStore, Name: "todo-cache"}, {Kind: v1.KindBucket, Name: "todo-tmp"}, {Kind: v1.KindFunction, Name: "todo-api"}} {
		require.Nil(t, h.get(k.Kind, k.Name), "%s/%s waits for the pre-hook", k.Kind, k.Name)
	}
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-1")

	h.edit(func(a *v1.App) {
		a.Spec.Version = "4.0.0"
		a.Spec.KV[0].Tables = append(a.Spec.KV[0].Tables, v1.KVTable{Name: "migrations", Owner: "todo-migrate"})
		a.Spec.Functions[1].KV = append(a.Spec.Functions[1].KV, v1.FunctionKV{Alias: "log", Store: "todo-store", Table: "migrations"})
		a.Spec.Functions[0].Image = "oci-layout://todo-api:4"
	})
	h.reconcile()
	h.noCall()
	require.Len(t, h.get(v1.KindKVStore, "todo-store").(*v1.KVStore).Spec.Tables, 2)
	require.Len(t, h.get(v1.KindFunction, "todo-migrate").(*v1.Function).Spec.KV, 2)
	require.Equal(t, "oci-layout://todo-api:1", h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Image)
	h.markReady(v1.KindFunction, "todo-migrate")
	h.converge("todo-2")
	require.Len(t, h.inv.received(), 2)
}
