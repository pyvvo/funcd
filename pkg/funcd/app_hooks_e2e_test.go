//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// hookFiles are the files a hook handler reads and writes: it fails while gate is missing (waits for it when it
// blocks) and marks each call it receives with a file in calls.
type hookFiles struct {
	gate, calls string
}

func newHookFiles(t *testing.T) hookFiles {
	t.Helper()
	dir := shortDataDir(t)
	f := hookFiles{gate: filepath.Join(dir, "gate"), calls: filepath.Join(dir, "calls")}
	require.NoError(t, os.Mkdir(f.calls, 0o700))
	f.open(t)
	return f
}

func (f hookFiles) open(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.gate, nil, 0o600))
}

func (f hookFiles) shut(t *testing.T) {
	t.Helper()
	require.NoError(t, os.Remove(f.gate))
}

func (f hookFiles) count(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir(f.calls)
	require.NoError(t, err)
	return len(ents)
}

// migrateImage pushes todo-migrate's handler: it writes its input under key hook-<to> of table todos through
// context.kv, and fails (or waits, when block is set) while f's gate is missing.
func (e *gcEnv) migrateImage(t *testing.T, f hookFiles, block bool) string {
	t.Helper()
	writeStep(t, e.src, "migrate", fmt.Sprintf(`import { existsSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
export async function handle(ctx, event) {
  writeFileSync(join(%[1]q, event.id), '');
  while (%[3]t && !existsSync(%[2]q)) await new Promise((r) => setTimeout(r, 50));
  if (!existsSync(%[2]q)) throw new Error('no such column');
  await ctx.kv.put('store', 'hook-' + event.data.to, JSON.stringify(event.data));
  return { ok: true };
}`, f.calls, f.gate, block))
	return pushStepImage(t, e.layout, e.src, "migrate")
}

// hookedTodo is ADR-0214's fixture on ADR-0200's: todo at version with todo-migrate, bound to table todos of
// todo-store, as its pre-hook.
func hookedTodo(t *testing.T, e *gcEnv, img, version string) *v1.App {
	t.Helper()
	a := todoV1(t, e)
	a.Spec.Version = version
	a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-migrate", FunctionSpec: v1.FunctionSpec{
		Runtime: "nodejs22",
		Handler: "handle",
		Image:   img,
		KV:      []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}},
	}})
	a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "todo-migrate"}}}
	return a
}

// migrateWriter is the fixture's RolesAssignment beside the App: todo-migrate writes table todos, which todo-api
// owns (ADR-0136).
func migrateWriter() *v1.RolesAssignment {
	return &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: "todo-migrate-writes", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{Assignments: []v1.AssignmentEntry{{
			Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "todo-migrate"},
			RoleRef:   v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "KV Data Writer"},
			Scope:     &v1.ScopeRef{Kind: v1.ScopeKindKVStore, Name: "todo-store/todos"},
		}}},
	}
}

// installHooked installs the fixture at 3.0.0 and waits until todo-1 is current and the App Ready.
func installHooked(t *testing.T, e *gcEnv, img string) *v1.App {
	t.Helper()
	e.apply(t, migrateWriter())
	a := hookedTodo(t, e, img, "3.0.0")
	e.apply(t, a)
	e.waitCurrent(t, "todo-1")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	return a
}

// upgradeHooked gives todo-api its second image at 4.0.0 and returns that image.
func upgradeHooked(t *testing.T, e *gcEnv, a *v1.App) string {
	t.Helper()
	img := e.imageV2(t)
	a.Spec.Version = "4.0.0"
	a.Spec.Functions[0].Image = img
	e.apply(t, a)
	return img
}

// hookInput is the input todo-migrate wrote for the call that made rev current.
func (e *gcEnv) hookInput(t *testing.T, rev string) v1.AppHookInput {
	t.Helper()
	raw, found, err := e.kv.Get(context.Background(), "default/todo-store/todos/hook-"+rev)
	require.NoError(t, err)
	require.True(t, found, "todo-migrate wrote its input for %s", rev)
	var in v1.AppHookInput
	require.NoError(t, json.Unmarshal(raw, &in))
	return in
}

func hookRecorded(rev v1.ObjectName, phases ...v1.Phase) func(store.Event) bool {
	return func(ev store.Event) bool {
		r, ok := ev.Object.(*v1.AppRevision)
		if !ok || r.Name != rev || len(r.Status.Hooks) != len(phases) {
			return false
		}
		for i, c := range r.Status.Hooks {
			if c.Phase != phases[i] {
				return false
			}
		}
		return true
	}
}

// scenario: app-pre-hook-migrates
func TestScenarioAppPreHookMigrates(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	f := newHookFiles(t)
	a := installHooked(t, e, e.migrateImage(t, f, false))
	fns, revs := watchKind(t, st, v1.KindFunction), watchKind(t, st, v1.KindAppRevision)

	img := upgradeHooked(t, e, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	two := e.appRevision(t, "todo-2")
	require.Len(t, two.Status.Hooks, 1, "todo-migrate is called once")
	call := two.Status.Hooks[0]
	require.Equal(t, "preApply", call.Point)
	require.Equal(t, v1.ObjectName("todo-migrate"), call.Function)
	require.Equal(t, v1.PhaseReady, call.Phase)
	inv := e.object(t, v1.KindInvocation, string(call.Invocation)).(*v1.Invocation)
	require.Equal(t, v1.PhaseReady, inv.Status.Phase)
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindAppRevision, Namespace: "default", Name: "todo-2"},
		UID: two.UID, Controller: true, BlockOwnerDeletion: true}}, inv.OwnerReferences)
	want := v1.AppHookInput{Event: "upgrade", App: "todo", From: "todo-1", To: "todo-2", FromVersion: "3.0.0", ToVersion: "4.0.0"}
	require.Equal(t, &want, two.Spec.HookInput)
	require.Equal(t, want, e.hookInput(t, "todo-2"), "the handler wrote table todos through context.kv")
	require.Equal(t, 2, f.count(t), "one call per revision")

	recorded := firstRV(t, revs.events(t), "todo-2 records the call", hookRecorded("todo-2", v1.PhaseReady))
	written := firstRV(t, fns.events(t), "todo-api gets its new image", func(ev store.Event) bool {
		fn, ok := ev.Object.(*v1.Function)
		return ok && fn.Name == "todo-api" && fn.Spec.Image == img
	})
	require.Less(t, recorded, written, "todo-api changes only after the pre-hook call is recorded")
	_, body := e.routedBody(t, todoHost, "/api")
	require.Contains(t, body, todoV2Marker)
}

// scenario: app-pre-hook-fails-then-retry
func TestScenarioAppPreHookFailsThenRetry(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	cli := funcdctl(t, e)
	f := newHookFiles(t)
	a := installHooked(t, e, e.migrateImage(t, f, false))
	parts := todoV1Parts()
	before := e.generations(t, parts)

	f.shut(t)
	img := upgradeHooked(t, e, a)
	two := e.waitRevisionPhase(t, "todo-2", v1.PhaseFailed, appWithin)
	require.Len(t, two.Status.Hooks, 1)
	prefix := "Function/todo-migrate: Invocation/" + string(two.Status.Hooks[0].Invocation) + ": "
	for _, ct := range []v1.ConditionType{"Applied", "Current"} {
		c := condition(two, ct)
		require.Equal(t, v1.ConditionFalse, c.Status, "%s", ct)
		require.Equal(t, "HookFailed", c.Reason, "%s", ct)
		require.True(t, strings.HasPrefix(c.Message, prefix), "%s: %s", ct, c.Message)
		require.Contains(t, c.Message, "returned status 500")
		require.Contains(t, c.Message, "no such column")
	}
	got := e.waitApp(t, "todo", v1.ConditionFalse, "HookFailed", appWithin)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	require.True(t, strings.HasPrefix(readyCondition(got).Message, prefix), readyCondition(got).Message)
	after := e.generations(t, parts)
	for _, p := range parts {
		if p != (todoPart{v1.KindKVStore, "todo-store"}) {
			require.Equal(t, before[p], after[p], "%s/%s is not written", p.kind, p.name)
		}
	}
	require.NotEqual(t, img, e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Spec.Image)
	_, body := e.routedBody(t, todoHost, "/api")
	require.NotContains(t, body, todoV2Marker, "todo-1 still serves")

	revs := watchKind(t, st, v1.KindAppRevision)
	f.open(t)
	out, err := cli("app", "retry", "todo")
	require.NoError(t, err, out)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	two = e.appRevision(t, "todo-2")
	require.Equal(t, []v1.Phase{v1.PhaseFailed, v1.PhaseReady}, []v1.Phase{two.Status.Hooks[0].Phase, two.Status.Hooks[1].Phase})
	require.True(t, slices.ContainsFunc(revs.events(t), func(ev store.Event) bool {
		r, ok := ev.Object.(*v1.AppRevision)
		return hookRecorded("todo-2", v1.PhaseFailed, v1.PhaseReady)(ev) && ok && r.Status.Phase == v1.PhaseDeploying
	}), "the retried call returns todo-2 to Deploying")

	out, err = cli("app", "retry", "todo")
	require.Error(t, err)
	require.Contains(t, out, "app todo has no failed hook")
}

// scenario: app-post-hook-fails
func TestScenarioAppPostHookFails(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	cli := funcdctl(t, e)
	f := newHookFiles(t)
	a := installHooked(t, e, e.migrateImage(t, f, false))

	warm := shortDataDir(t)
	writeStep(t, e.src, "warm", fmt.Sprintf(`import { existsSync } from 'node:fs';
export async function handle() { if (!existsSync(%q)) throw new Error('cache down'); return { ok: true }; }`, filepath.Join(warm, "gate")))
	a.Spec.Version = "4.0.0"
	a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-warm", FunctionSpec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "handle", Image: pushStepImage(t, e.layout, e.src, "warm"),
	}})
	a.Spec.Hooks.PostApply = []v1.AppHook{{Function: "todo-warm"}}
	a.Spec.Routes = a.Spec.Routes[:1]
	e.apply(t, a)

	got := e.waitApp(t, "todo", v1.ConditionFalse, "HookFailed", appWithin)
	require.Equal(t, v1.PhaseDegraded, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.CurrentRevision)
	two := e.appRevision(t, "todo-2")
	require.Equal(t, v1.PhaseReady, two.Status.Phase)
	require.Equal(t, v1.ConditionTrue, condition(two, "Current").Status)
	last := two.Status.Hooks[len(two.Status.Hooks)-1]
	require.Equal(t, v1.AppHookCall{Point: "postApply", Function: "todo-warm", Invocation: last.Invocation, Phase: v1.PhaseFailed, EndTime: last.EndTime}, last)
	msg := readyCondition(got).Message
	require.True(t, strings.HasPrefix(msg, "Function/todo-warm: Invocation/"+string(last.Invocation)+": "), msg)
	require.Contains(t, msg, "cache down")
	require.Contains(t, got.Status.Children, v1.AppChild{Kind: v1.KindRoute, Name: "todo-legacy", State: v1.AppChildPruning})
	require.True(t, e.exists(t, v1.KindRoute, "todo-legacy"), "prune waits for the post-hooks")

	require.NoError(t, os.WriteFile(filepath.Join(warm, "gate"), nil, 0o600))
	out, err := cli("app", "retry", "todo")
	require.NoError(t, err, out)
	require.Eventually(t, func() bool {
		hooks := e.appRevision(t, "todo-2").Status.Hooks
		return hooks[len(hooks)-1].Phase == v1.PhaseReady
	}, appWithin, 20*time.Millisecond, "the retried todo-warm succeeds")
	e.waitGone(t, v1.KindRoute, "todo-legacy")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
}

// scenario: app-hook-event
func TestScenarioAppHookEvent(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	cli := funcdctl(t, e)
	f := newHookFiles(t)
	stores, revs := watchKind(t, st, v1.KindKVStore), watchKind(t, st, v1.KindAppRevision)
	a := installHooked(t, e, e.migrateImage(t, f, false))
	require.Equal(t, v1.AppHookInput{Event: "install", App: "todo", To: "todo-1", ToVersion: "3.0.0"}, e.hookInput(t, "todo-1"))
	written := firstRV(t, stores.events(t), "todo-store is written", func(ev store.Event) bool {
		s, ok := ev.Object.(*v1.KVStore)
		return ok && s.Name == "todo-store"
	})
	require.Less(t, written, firstRV(t, revs.events(t), "todo-1 records the call", hookRecorded("todo-1", v1.PhaseReady)),
		"the install writes todo-store before it calls todo-migrate")

	upgradeHooked(t, e, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	out, err := cli("app", "rollback", "todo", "1")
	require.NoError(t, err, out)
	e.waitCurrent(t, "todo-3")
	want := v1.AppHookInput{Event: "rollback", App: "todo", From: "todo-2", To: "todo-3", FromVersion: "4.0.0", ToVersion: "3.0.0"}
	require.Equal(t, &want, e.appRevision(t, "todo-3").Spec.HookInput)
	require.Equal(t, want, e.hookInput(t, "todo-3"))
}

// scenario: app-hook-repeats-after-restart — the call runs longer than the first boot's app.upgradeTimeout, so only
// the busy rule keeps todo-2 Deploying; the restarted funcd runs with the default timeout. With the short timeout
// still in force, its first pass may find todo-migrate Degraded while the Function reconciler replaces the replica
// the stop ended, and Decision 4 then fails todo-2 HookFailed "not ready" (ADR gap, reported at hand-off); the
// internal/app scenario test covers a restart past the deadline with the Function Ready.
func TestScenarioAppHookRepeatsAfterRestart(t *testing.T) {
	t.Parallel()
	const upgradeTimeout = 6 * time.Second
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st), funcd.WithPacing(funcd.Pacing{
		AppUpgradeTimeout: upgradeTimeout, BootTimeout: 4 * time.Second, ActivationTimeout: 3 * time.Second,
	}))
	f := newHookFiles(t)
	a := installHooked(t, e, e.migrateImage(t, f, true))

	f.shut(t)
	upgradeHooked(t, e, a)
	require.Eventually(t, func() bool { return f.count(t) == 2 }, appWithin, 20*time.Millisecond, "todo-migrate is called for todo-2")
	stays(t, func() bool { return e.appRevision(t, "todo-2").Status.Phase == v1.PhaseDeploying }, upgradeTimeout+time.Second, 200*time.Millisecond,
		"a running call fails no revision by the deadline")
	e.stop()

	ctx := context.Background()
	obj, err := st.Get(ctx, v1.KindAppRevision.GVK(), "default", "todo-2")
	require.NoError(t, err)
	two := obj.(*v1.AppRevision)
	require.Empty(t, two.Status.Hooks, "the call funcd's stop ended is not recorded")
	invs, err := st.List(ctx, v1.KindInvocation.GVK(), store.ListOptions{Namespace: "default"})
	require.NoError(t, err)
	for _, o := range invs.Items {
		require.False(t, v1.ControlledBy(o.GetObjectMeta().OwnerReferences, v1.KindAppRevision, two.UID), "no Invocation records it")
	}

	f.open(t)
	e2 := startGC(t, funcd.WithStore(st), funcd.WithKVStore(e.kv))
	e2.waitCurrent(t, "todo-2")
	e2.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, 3, f.count(t), "the restarted funcd calls it again")
	two = e2.appRevision(t, "todo-2")
	require.Len(t, two.Status.Hooks, 1)
	require.Equal(t, v1.PhaseReady, two.Status.Hooks[0].Phase)
	require.Equal(t, v1.AppHookInput{Event: "upgrade", App: "todo", From: "todo-1", To: "todo-2", FromVersion: "3.0.0", ToVersion: "4.0.0"},
		e.hookInput(t, "todo-2"), "the second call wrote its input")
}

// A hook call wakes its Function within the hook Function's spec.timeout (ADR-0214 Decision 5): a pre-hook
// Function that scales to zero and takes 2 s to load fails its call at a 1 s timeout, and passes at 10 s.
func TestAppHookTimeoutCoversWake(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		timeout time.Duration
		ok      bool
	}{{time.Second, false}, {10 * time.Second, true}} {
		t.Run(tc.timeout.String(), func(t *testing.T) {
			t.Parallel()
			e := startGC(t)
			writeStep(t, e.src, "slowload", "await new Promise((r) => setTimeout(r, 2000));\nexport async function handle() { return { ok: true }; }\n")
			a := todoV1(t, e)
			a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-hook", FunctionSpec: v1.FunctionSpec{
				Runtime: "nodejs22", Handler: "handle", Image: pushStepImage(t, e.layout, e.src, "slowload"), Timeout: v1.Duration(tc.timeout),
			}})
			a.Spec.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: "todo-hook"}}}
			e.apply(t, a)
			if tc.ok {
				e.waitCurrent(t, "todo-1")
				e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
				return
			}
			c := condition(e.waitRevisionPhase(t, "todo-1", v1.PhaseFailed, appWithin), "Applied")
			require.Equal(t, "HookFailed", c.Reason)
			require.Contains(t, c.Message, "Function/todo-hook: Invocation/")
			require.Contains(t, c.Message, "wake default/todo-hook", "the call ended while it woke the Function")
			e.waitApp(t, "todo", v1.ConditionFalse, "HookFailed", appWithin)
		})
	}
}
