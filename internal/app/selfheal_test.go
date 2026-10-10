package app_test

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// records captures the reconciler's log lines as attribute maps, the logger's own attributes (component) included.
type records struct {
	mu    sync.Mutex
	lines []record
}

type record struct {
	msg   string
	attrs map[string]string
}

// to is a harness option that logs to l.
func (l *records) to(d *app.Deps) { d.Logger = slog.New(&recordHandler{l: l}) }

// selfHealed returns the attributes of each self-healed line, in order.
func (l *records) selfHealed() []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]string
	for _, r := range l.lines {
		if r.msg == "self-healed" {
			out = append(out, r.attrs)
		}
	}
	return out
}

type recordHandler struct {
	l     *records
	attrs []slog.Attr
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{"level": r.Level.String()}
	for _, a := range h.attrs {
		m[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.String()
		return true
	})
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	h.l.lines = append(h.l.lines, record{msg: r.Message, attrs: m})
	return nil
}

func (h *recordHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &recordHandler{l: h.l, attrs: append(slices.Clone(h.attrs), as...)}
}

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

func healLine(kind v1.Kind, name v1.ObjectName) map[string]string {
	return map[string]string{"level": "INFO", "component": "app", "kind": string(kind), "namespace": string(ns), "name": string(name), "app": "todo"}
}

// handEdit sets todo-api's image by hand, as funcdctl apply of the Function does.
func (h *harness) handEdit(image string) {
	h.t.Helper()
	fn := h.get(v1.KindFunction, "todo-api").(*v1.Function)
	fn.Spec.Image = image
	h.update(fn)
}

func (h *harness) image() string {
	h.t.Helper()
	return h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Image
}

// scenario: app-rollout-is-not-self-heal (the reconciler half) — a stamping pass, a resourceGroup-only rewrite and an
// owner-reference-only rewrite write parts without a self-healed line, and lastSelfHeal keeps the earlier record.
func TestScenarioAppRolloutIsNotSelfHeal(t *testing.T) {
	logs := &records{}
	h := newHarness(t, nil, logs.to)
	h.install(todoApp(nil))
	h.handEdit("oci-layout://hotfix:2")
	h.reconcile()
	healed := h.app().Status.LastSelfHeal
	require.NotNil(t, healed)
	h.clk.Advance(time.Second)

	h.edit(setImage("oci-layout://todo-api:2"))
	h.reconcile()
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, h.revNames())
	require.Equal(t, "oci-layout://todo-api:2", h.image())
	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)

	before := h.versionsAll()
	h.edit(func(a *v1.App) { a.ResourceGroup = "moved" })
	h.reconcile()
	after := h.versionsAll()
	require.NotEqual(t, before[v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "todo-api"}],
		after[v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "todo-api"}], "every part is rewritten")
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, h.revNames(), "a resource group is no spec change")

	fn := h.get(v1.KindFunction, "todo-api")
	fn.GetObjectMeta().OwnerReferences[0].UID = "an-earlier-uid"
	h.update(fn)
	h.reconcile()
	c, ok := v1.ControllerOf(refsOf(h.get(v1.KindFunction, "todo-api")))
	require.True(t, ok)
	require.Equal(t, h.app().UID, c.UID, "the references are rewritten")

	require.Equal(t, []map[string]string{healLine(v1.KindFunction, "todo-api")}, logs.selfHealed(), "only the hand edit is a self-heal")
	require.Equal(t, healed, h.app().Status.LastSelfHeal)
}

// ADR-0212 Decision 1: the retry of a rollout pass stopped by ChildInvalid writes the rest of the parts as
// convergence: the Deploying revision's Applied is False.
func TestAppRetryOfAStoppedRolloutIsNotSelfHeal(t *testing.T) {
	logs := &records{}
	st := &failing{Store: store.New(memory.New())}
	h := newHarness(t, st, logs.to)
	h.install(todoApp(nil))
	h.edit(func(a *v1.App) {
		setImage("oci-layout://todo-api:2")(a)
		a.Spec.Routes[0].Rules[0].Path = "/v2"
	})
	st.arm(v1.KindFunction, "todo-api", fault.Invalidf("store.Update", "Function refused"))
	h.reconcile()
	requireCond(t, revCond(t, h.rev(2), "Applied"), v1.ConditionFalse, "ChildInvalid", h.ready().Message)

	h.reconcile()
	require.Equal(t, "oci-layout://todo-api:2", h.image())
	require.Equal(t, "/v2", h.get(v1.KindRoute, "todo-api").(*v1.Route).Spec.Rules[0].Path)
	require.Empty(t, logs.selfHealed())
	require.Nil(t, h.app().Status.LastSelfHeal)
}

// ADR-0212 Decision 1: after Failed, Applied keeps its last value, so a write-back records only when it was True.
func TestAppWriteBackAfterFailed(t *testing.T) {
	t.Run("Applied True", func(t *testing.T) {
		logs := &records{}
		h := newHarness(t, nil, logs.to)
		h.install(todoApp(nil))
		h.edit(setImage("oci-layout://never-starts:2"))
		h.reconcile()
		h.clk.Advance(5 * time.Minute)
		h.reconcile()
		require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
		requireCond(t, revCond(t, h.rev(2), "Applied"), v1.ConditionTrue, "", "")

		h.handEdit("oci-layout://hotfix:2")
		h.reconcile()
		require.Equal(t, "oci-layout://never-starts:2", h.image())
		require.Equal(t, []map[string]string{healLine(v1.KindFunction, "todo-api")}, logs.selfHealed())
		require.Equal(t, &v1.AppSelfHeal{Kind: v1.KindFunction, Name: "todo-api", At: v1.NewTimestamp(h.clk.Now())}, h.app().Status.LastSelfHeal)
	})
	t.Run("Applied False", func(t *testing.T) {
		logs := &records{}
		st := &failing{Store: store.New(memory.New())}
		h := newHarness(t, st, logs.to)
		h.install(todoApp(nil))
		h.edit(setImage("oci-layout://todo-api:2"))
		refuse := fault.Invalidf("store.Update", "Function refused")
		st.arm(v1.KindFunction, "todo-api", refuse)
		h.reconcile()
		h.clk.Advance(5 * time.Minute)
		st.arm(v1.KindFunction, "todo-api", refuse)
		h.reconcile()
		require.Equal(t, v1.PhaseFailed, h.rev(2).Status.Phase)
		requireCond(t, revCond(t, h.rev(2), "Applied"), v1.ConditionFalse, "ChildInvalid", "Function/todo-api: store.Update: Function refused")

		rt := h.get(v1.KindRoute, "todo-api")
		require.NoError(t, h.st.Delete(h.ctx, v1.KindRoute.GVK(), ns, "todo-api", rt.GetObjectMeta().ResourceVersion))
		h.reconcile()
		require.Equal(t, "oci-layout://todo-api:2", h.image())
		require.NotNil(t, h.get(v1.KindRoute, "todo-api"))
		require.Empty(t, logs.selfHealed())
		require.Nil(t, h.app().Status.LastSelfHeal)
	})
}

// ADR-0212 Decision 1: a ref object edited by hand stays; the App never writes it.
func TestAppNeverWritesBackARefObject(t *testing.T) {
	logs := &records{}
	h := newHarness(t, nil, logs.to)
	h.create(&v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "mailer", Namespace: ns, ResourceGroup: "rg1"}, Spec: apiSpec()})
	h.markReady(v1.KindFunction, "mailer")
	h.install(todoApp(func(a *v1.App) { a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Ref: "mailer"}) }))
	mailer := h.get(v1.KindFunction, "mailer").(*v1.Function)
	mailer.Spec.Image = "oci-layout://mailer-hotfix:2"
	rv := h.update(mailer).GetObjectMeta().ResourceVersion

	h.reconcile()
	got := h.get(v1.KindFunction, "mailer")
	require.Equal(t, rv, got.GetObjectMeta().ResourceVersion)
	require.Equal(t, "oci-layout://mailer-hotfix:2", got.(*v1.Function).Spec.Image)
	require.Empty(t, logs.selfHealed())
	require.Nil(t, h.app().Status.LastSelfHeal)
}

// ADR-0212 Decision 7: a stamp stopped by a foreign namesake on a current App reads Degraded, not Deploying.
func TestAppStampStoppedByANamesakeDegrades(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	foreign := foreignRevision([]v1.OwnerReference{
		{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: ns, Name: "todo"}, UID: "a-function-uid", Controller: true},
	})
	foreign.Name, foreign.Spec.Number = "todo-2", 2
	h.create(foreign)
	h.edit(setImage("oci-layout://todo-api:2"))
	h.reconcile()
	got := h.app()
	require.Equal(t, v1.PhaseDegraded, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotOwned", "AppRevision/todo-2: exists and is not owned by App/todo")
	require.Equal(t, "oci-layout://todo-api:1", h.image(), "no part is written")
}
