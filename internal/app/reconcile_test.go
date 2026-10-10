package app_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const ns v1.NamespaceName = "default"

// todoApp is the ADR-0199 Scenarios fixture: todo-store (retain) and todo-cache (delete), todo-files (retain) and
// todo-tmp (delete), Function todo-api bound to three of them, Route todo-api and Workflow todo-plan.
func todoApp(mutate func(*v1.App)) *v1.App {
	a := &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: ns, ResourceGroup: "todo-rg"},
		Spec: v1.AppSpec{
			KV: []v1.AppKVStore{
				{Name: "todo-store", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "todos", Owner: "todo-api"}}}},
				{Name: "todo-cache", Deletion: v1.DeletionDelete, KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "entries", Owner: "todo-api"}}}},
			},
			Buckets: []v1.AppBucket{
				{Name: "todo-files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "attachments", Owner: "todo-api"}}}},
				{Name: "todo-tmp", Deletion: v1.DeletionDelete},
			},
			Functions: []v1.AppFunction{{Name: "todo-api", FunctionSpec: apiSpec(
				v1.FunctionKV{Alias: "store", Store: "todo-store", Table: "todos"},
				v1.FunctionKV{Alias: "cache", Store: "todo-cache", Table: "entries"},
			)}},
			Workflows: []v1.AppWorkflow{{Name: "todo-plan", WorkflowSpec: v1.WorkflowSpec{
				Steps: []v1.WorkflowStep{{Name: "due", Function: &v1.FunctionStep{Image: "oci-layout://todo-due:1"}}},
			}}},
			Routes: []v1.AppRoute{{Name: "todo-api", RouteSpec: v1.RouteSpec{
				Rules: []v1.RouteRule{{Path: "/api", Backend: v1.RouteBackend{Function: "todo-api"}}},
			}}},
		},
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func apiSpec(kv ...v1.FunctionKV) v1.FunctionSpec {
	return v1.FunctionSpec{
		Runtime: "nodejs22",
		Handler: "index.handler",
		Image:   "oci-layout://todo-api:1",
		Scaling: v1.Scaling{MinReplicas: 1},
		KV:      kv,
		Blob:    []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}},
	}
}

// purges records each Purge as "<ns>/<bucket>".
type purges struct {
	mu    sync.Mutex
	calls []string
}

func (p *purges) Purge(_ context.Context, ns v1.NamespaceName, bucket v1.ObjectName) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, string(ns)+"/"+string(bucket))
	return nil
}

type harness struct {
	t    *testing.T
	ctx  context.Context
	st   store.Store
	r    *app.Reconciler
	p    *purges
	clk  *clock.Manual
	deps app.Deps
}

// newHarness builds the reconciler on st (a fresh memory store when nil) and a manual clock; opts adjust its Deps.
func newHarness(t *testing.T, st store.Store, opts ...func(*app.Deps)) *harness {
	t.Helper()
	if st == nil {
		st = store.New(memory.New())
	}
	h := &harness{t: t, ctx: context.Background(), st: st, p: &purges{}, clk: clock.NewManual(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))}
	h.deps = app.Deps{Store: st, Purger: h.p, Clock: h.clk}
	for _, o := range opts {
		o(&h.deps)
	}
	h.restart()
	return h
}

// restart replaces the reconciler with a new one on the same Deps, as a funcd restart does.
func (h *harness) restart() {
	h.t.Helper()
	r, err := app.NewReconciler(h.deps)
	require.NoError(h.t, err)
	h.r = r
}

func (h *harness) create(obj v1.Object) v1.Object {
	h.t.Helper()
	out, err := h.st.Create(h.ctx, obj)
	require.NoError(h.t, err)
	return out
}

func (h *harness) update(obj v1.Object) v1.Object {
	h.t.Helper()
	out, err := h.st.Update(h.ctx, obj)
	require.NoError(h.t, err)
	return out
}

func (h *harness) get(kind v1.Kind, name v1.ObjectName) v1.Object {
	h.t.Helper()
	obj, err := h.st.Get(h.ctx, kind.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(h.t, err)
	return obj
}

func (h *harness) app() *v1.App { return h.get(v1.KindApp, "todo").(*v1.App) }

// edit applies mutate to the stored App's spec.
func (h *harness) edit(mutate func(*v1.App)) {
	h.t.Helper()
	a := h.app()
	mutate(a)
	h.update(a)
}

func (h *harness) reconcile() controller.Result {
	h.t.Helper()
	res, err := h.reconcileErr()
	require.NoError(h.t, err)
	return res
}

func (h *harness) reconcileErr() (controller.Result, error) {
	return h.r.Reconcile(h.ctx, controller.Request{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "todo"})
}

func (h *harness) ready() v1.Condition {
	h.t.Helper()
	c, ok := h.app().Status.Conditions.Get("Ready")
	require.True(h.t, ok)
	return c
}

func (h *harness) child(kind v1.Kind, name v1.ObjectName) v1.AppChild {
	h.t.Helper()
	for _, c := range h.app().Status.Children {
		if c.Kind == kind && c.Name == name {
			return c
		}
	}
	h.t.Fatalf("no child %s/%s", kind, name)
	return v1.AppChild{}
}

func cond(t v1.ConditionType, s v1.ConditionStatus, reason string, gen int64) v1.Condition {
	return v1.Condition{Type: t, Status: s, Reason: reason, ObservedGeneration: gen}
}

// setStatus sets obj's phase and conditions; nil conds leave them.
func setStatus(obj v1.Object, phase v1.Phase, conds ...v1.Condition) {
	st := obj.(v1.StatusObject).GetStatus()
	st.Phase = phase
	for _, c := range conds {
		st.Conditions.Set(c)
	}
}

// markReady gives a part the status its reconciler writes once it serves its generation.
func (h *harness) markReady(kind v1.Kind, name v1.ObjectName) {
	h.t.Helper()
	obj := h.get(kind, name)
	if _, ok := obj.(v1.StatusObject); !ok {
		return
	}
	gen := obj.GetObjectMeta().Generation
	if kind == v1.KindFunction {
		setStatus(obj, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", 0),
			cond("ShapeValid", v1.ConditionTrue, "", gen), cond("RevisionReady", v1.ConditionTrue, "", gen))
	} else {
		setStatus(obj, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", gen))
	}
	h.update(obj)
	if w, ok := obj.(*v1.Workflow); ok {
		for _, st := range w.Spec.Steps {
			if st.Function != nil && st.Function.Image != "" {
				h.readyStep(v1.StepFunctionName(w.Name, st.Name))
			}
		}
	}
}

// readyStep creates the step Function name, as the Workflow materializer does, and marks it Ready.
func (h *harness) readyStep(name v1.ObjectName) {
	h.t.Helper()
	if h.get(v1.KindFunction, name) == nil {
		h.create(&v1.Function{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "todo-rg"},
			Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://step:1"},
		})
	}
	h.markReady(v1.KindFunction, name)
}

func (h *harness) markAllReady() {
	h.t.Helper()
	for _, c := range h.app().Status.Children {
		if c.State != v1.AppChildPruning {
			h.markReady(c.Kind, c.Name)
		}
	}
}

// install creates the App and brings it to Ready.
func (h *harness) install(a *v1.App) {
	h.t.Helper()
	h.create(a)
	h.reconcile()
	h.markAllReady()
	h.reconcile()
	require.Equal(h.t, v1.ConditionTrue, h.ready().Status, h.ready().Message)
}

func refsOf(obj v1.Object) []v1.OwnerReference { return obj.GetObjectMeta().OwnerReferences }

func TestNewReconcilerRequiresStoreAndPurger(t *testing.T) {
	_, err := app.NewReconciler(app.Deps{Purger: &purges{}})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = app.NewReconciler(app.Deps{Store: store.New(memory.New())})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = app.NewReconciler(app.Deps{Store: store.New(memory.New()), Purger: &purges{}, UpgradeTimeout: -time.Second})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = app.NewReconciler(app.Deps{Store: store.New(memory.New()), Purger: &purges{}, RevisionHistory: -1})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = app.NewReconciler(app.Deps{Store: store.New(memory.New()), Purger: &purges{}, SupervisionPeriod: -time.Second})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// A blocked prune retries after the configured runtime.supervisionPeriod, not the package default.
func TestAppBlockedPruneRequeuesAfterConfiguredPeriod(t *testing.T) {
	h := newHarness(t, nil, func(d *app.Deps) { d.SupervisionPeriod = 3 * time.Second })
	h.install(todoApp(nil))
	h.edit(func(a *v1.App) { a.Spec.KV = a.Spec.KV[:1] })
	require.Equal(t, 3*time.Second, h.reconcile().RequeueAfter)
}

// scenario: app-install (the reconciler half) — every part is written in the App's namespace and resource group with
// the references of Decision 7; the App is Deploying until no part is Pending, then Ready with seven children Ready.
func TestScenarioAppInstall(t *testing.T) {
	h := newHarness(t, nil)
	a := h.create(todoApp(nil)).(*v1.App)
	h.reconcile()

	ctrl := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: a.UID, Controller: true, BlockOwnerDeletion: true}
	marker := v1.OwnerReference{ObjectRef: ctrl.ObjectRef, UID: a.UID}
	for _, tc := range []struct {
		kind v1.Kind
		name v1.ObjectName
		refs []v1.OwnerReference
	}{
		{v1.KindKVStore, "todo-store", []v1.OwnerReference{marker}},
		{v1.KindKVStore, "todo-cache", []v1.OwnerReference{marker, ctrl}},
		{v1.KindBucket, "todo-files", []v1.OwnerReference{marker}},
		{v1.KindBucket, "todo-tmp", []v1.OwnerReference{marker, ctrl}},
		{v1.KindFunction, "todo-api", []v1.OwnerReference{ctrl}},
		{v1.KindWorkflow, "todo-plan", []v1.OwnerReference{ctrl}},
		{v1.KindRoute, "todo-api", []v1.OwnerReference{ctrl}},
	} {
		obj := h.get(tc.kind, tc.name)
		require.NotNil(t, obj, "%s/%s", tc.kind, tc.name)
		m := obj.GetObjectMeta()
		require.Equal(t, ns, m.Namespace)
		require.Equal(t, v1.ResourceGroupName("todo-rg"), m.ResourceGroup)
		require.Equal(t, tc.refs, m.OwnerReferences, "%s/%s", tc.kind, tc.name)
	}
	require.Nil(t, h.get(v1.KindFunction, "todo-plan-due"), "a part's reconciler makes its children")

	got := h.app()
	require.Equal(t, v1.PhaseDeploying, got.Status.Phase)
	require.Len(t, got.Status.Children, 7)
	require.Equal(t, v1.AppChild{Kind: v1.KindBucket, Name: "todo-files", State: v1.AppChildPending, Reason: "Progressing"}, h.child(v1.KindBucket, "todo-files"), "a Bucket is Pending until its reconciler's first pass (ADR-0215 Decision 7)")
	require.Equal(t, v1.AppChild{Kind: v1.KindKVStore, Name: "todo-store", State: v1.AppChildPending, Reason: "Progressing"}, h.child(v1.KindKVStore, "todo-store"))
	c := h.ready()
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "Progressing", c.Reason)
	require.Equal(t, "KVStore/todo-store: Progressing", c.Message, "the first Pending part in section order")
	require.Equal(t, got.Generation, c.ObservedGeneration)

	h.markAllReady()
	require.Zero(t, h.reconcile())
	got = h.app()
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
	require.Len(t, got.Status.Children, 7)
	for _, ch := range got.Status.Children {
		require.Equal(t, v1.AppChildReady, ch.State, "%s/%s", ch.Kind, ch.Name)
	}
}

func versions(t *testing.T, h *harness) map[v1.ObjectRef]string {
	t.Helper()
	out := map[v1.ObjectRef]string{}
	for _, c := range h.app().Status.Children {
		out[v1.ObjectRef{Kind: c.Kind, Name: c.Name}] = h.get(c.Kind, c.Name).GetObjectMeta().ResourceVersion
	}
	return out
}

// scenario: app-spec-change-applies (the reconciler half) — a pass writes only the part whose spec changed.
func TestScenarioAppSpecChangeApplies(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	before := versions(t, h)
	h.reconcile()
	require.Equal(t, before, versions(t, h), "a converged pass writes nothing")

	h.edit(func(a *v1.App) { a.Spec.Routes[0].Rules[0].Path = "/v2" })
	h.reconcile()
	require.Equal(t, "/v2", h.get(v1.KindRoute, "todo-api").(*v1.Route).Spec.Rules[0].Path)
	after := versions(t, h)
	route := v1.ObjectRef{Kind: v1.KindRoute, Name: "todo-api"}
	require.NotEqual(t, before[route], after[route])
	delete(before, route)
	delete(after, route)
	require.Equal(t, before, after, "no other part gets a new resourceVersion")
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase, "a spec change deploys until no part is Pending")
}

// Decision 4: every pass converges, so a part edited or deleted by hand is written back, its status kept. ADR-0212
// Decisions 1 and 2: each write-back is one self-healed line, with no spec value, and lastSelfHeal names the later
// part in section order with one time for the pass; no AppRevision is stamped.
func TestAppWritesBackAHandEdit(t *testing.T) {
	logs := &records{}
	h := newHarness(t, nil, logs.to)
	h.install(todoApp(nil))
	require.Empty(t, logs.selfHealed(), "an install is no self-heal")
	fn := h.get(v1.KindFunction, "todo-api").(*v1.Function)
	fn.Spec.Image = "oci-layout://hotfix:2"
	h.update(fn)
	rt := h.get(v1.KindRoute, "todo-api")
	require.NoError(t, h.st.Delete(h.ctx, v1.KindRoute.GVK(), ns, "todo-api", rt.GetObjectMeta().ResourceVersion))

	h.clk.Advance(time.Second)
	h.reconcile()
	fn = h.get(v1.KindFunction, "todo-api").(*v1.Function)
	require.Equal(t, "oci-layout://todo-api:1", fn.Spec.Image)
	require.Equal(t, v1.PhaseReady, fn.Status.Phase, "the App never writes a part's status")
	require.NotNil(t, h.get(v1.KindRoute, "todo-api"))
	require.Equal(t, []map[string]string{
		{"level": "INFO", "component": "app", "kind": "Function", "namespace": "default", "name": "todo-api", "app": "todo"},
		{"level": "INFO", "component": "app", "kind": "Route", "namespace": "default", "name": "todo-api", "app": "todo"},
	}, logs.selfHealed())
	require.Equal(t, &v1.AppSelfHeal{Kind: v1.KindRoute, Name: "todo-api", At: v1.NewTimestamp(h.clk.Now())}, h.app().Status.LastSelfHeal)
	require.Equal(t, []v1.ObjectName{"todo-1"}, h.revNames())
	h.quiet()
	require.Len(t, logs.selfHealed(), 2, "a repeated pass finds every part equal")
}

// scenario: app-child-not-owned (the reconciler half) — a part that exists without this App's reference stops the
// pass before any write.
func TestScenarioAppChildNotOwned(t *testing.T) {
	t.Run("a Function created by hand", func(t *testing.T) {
		h := newHarness(t, nil)
		f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: "todo-api", Namespace: ns, ResourceGroup: "rg1"}, Spec: apiSpec()}
		f = h.create(f).(*v1.Function)
		h.create(todoApp(nil))

		require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter, "the hand-made part does not requeue the App")
		for _, k := range []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindWorkflow, v1.KindRoute} {
			res, err := h.st.List(h.ctx, k.GVK(), store.ListOptions{Namespace: ns})
			require.NoError(t, err)
			require.Empty(t, res.Items, "nothing is written: %s", k)
		}
		require.Equal(t, f.ResourceVersion, h.get(v1.KindFunction, "todo-api").GetObjectMeta().ResourceVersion)
		c := h.ready()
		require.Equal(t, v1.ConditionFalse, c.Status)
		require.Equal(t, "ChildNotOwned", c.Reason)
		require.Contains(t, c.Message, "Function/todo-api")
		require.Equal(t, v1.AppChild{Kind: v1.KindFunction, Name: "todo-api", State: v1.AppChildPending, Reason: "ChildNotOwned"}, h.child(v1.KindFunction, "todo-api"))
		require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
	})
	t.Run("a store with another incarnation's marker", func(t *testing.T) {
		h := newHarness(t, nil)
		s := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
			ObjectMeta: v1.ObjectMeta{Name: "todo-cache", Namespace: ns, ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{
				{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: "an-earlier-uid"},
			}}}
		h.create(s)
		h.create(todoApp(nil))
		h.reconcile()
		require.Equal(t, "ChildNotOwned", h.ready().Reason)
		require.Contains(t, h.ready().Message, "KVStore/todo-cache")
		require.Nil(t, h.get(v1.KindKVStore, "todo-store"), "no part is written, the ones before it included")
	})
	t.Run("a part another incarnation controls is taken back", func(t *testing.T) {
		h := newHarness(t, nil)
		f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: "todo-api", Namespace: ns, ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{
				{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: "an-earlier-uid", Controller: true},
			}}, Spec: apiSpec()}
		h.create(f)
		a := h.create(todoApp(nil)).(*v1.App)
		h.reconcile()
		c, ok := v1.ControllerOf(refsOf(h.get(v1.KindFunction, "todo-api")))
		require.True(t, ok)
		require.Equal(t, a.UID, c.UID)
	})
	t.Run("a stopped pass after Ready degrades the App", func(t *testing.T) {
		h := newHarness(t, nil)
		h.install(todoApp(nil))
		rt := h.get(v1.KindRoute, "todo-api")
		rt.GetObjectMeta().OwnerReferences = nil
		h.update(rt)
		h.reconcile()
		require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
		require.Equal(t, "ChildNotOwned", h.ready().Reason)
	})
}

// scenario: app-store-deletion-flip (the reconciler half) and Decision 7 — a change of deletion or resource group
// alone rewrites the part's references and group.
func TestScenarioAppStoreDeletionFlip(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	a := h.app()
	marker := v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: a.UID}

	h.edit(func(a *v1.App) { a.Spec.KV[1].Deletion = v1.DeletionRetain })
	h.reconcile()
	require.Equal(t, []v1.OwnerReference{marker}, refsOf(h.get(v1.KindKVStore, "todo-cache")), "a retained store keeps only the marker")

	h.edit(func(a *v1.App) { a.ResourceGroup = "moved" })
	h.reconcile()
	for _, c := range h.app().Status.Children {
		require.Equal(t, v1.ResourceGroupName("moved"), h.get(c.Kind, c.Name).GetObjectMeta().ResourceGroup, "%s/%s", c.Kind, c.Name)
	}
}

// refusing is a store that refuses every Create of one kind as invalid.
type refusing struct {
	store.Store
	kind v1.Kind
}

func (s refusing) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if obj.GroupVersionKind().Kind == s.kind {
		return nil, fault.Invalidf("store.Create", "%s refused", s.kind)
	}
	return s.Store.Create(ctx, obj)
}

// Decision 4: a write the store refuses gives ChildInvalid and stops the pass.
func TestAppChildInvalid(t *testing.T) {
	h := newHarness(t, refusing{Store: store.New(memory.New()), kind: v1.KindFunction})
	h.create(todoApp(nil))
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter)
	c := h.ready()
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "ChildInvalid", c.Reason)
	require.Contains(t, c.Message, "Function/todo-api: ")
	require.Contains(t, c.Message, "Function refused")
	require.NotNil(t, h.get(v1.KindBucket, "todo-tmp"), "the parts before it are written")
	require.Nil(t, h.get(v1.KindWorkflow, "todo-plan"), "no part after it is written")
	require.Equal(t, "ChildInvalid", h.child(v1.KindFunction, "todo-api").Reason)
}

// Decision 5's readiness of each kind: a Function by its own rule (idle and waking included), a Bucket once it
// exists, any other part by its Ready condition at its generation. The App is Deploying until its first switch, so a
// part that is not ready gives Progressing: ChildNotReady is for Failed and Degraded only (ADR-0200 Decision 6).
func TestAppReadiness(t *testing.T) {
	fn := func(a *v1.App) { a.Spec.Functions = []v1.AppFunction{{Name: "todo-api", FunctionSpec: apiSpec()}} }
	kv := func(a *v1.App) { a.Spec.KV = []v1.AppKVStore{{Name: "todo-store"}} }
	timer := func(a *v1.App) {
		a.Spec.EventSources = []v1.AppEventSource{{Name: "todo-tick", EventSourceSpec: v1.EventSourceSpec{
			Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "hourly", Interval: v1.Duration(time.Hour)}}},
		}}}
	}
	catalog := func(a *v1.App) {
		a.Spec.Catalogs = []v1.AppCatalog{{Name: "todo-lake", CatalogServiceSpec: v1.CatalogServiceSpec{
			Blob:    []v1.FunctionBlob{{Alias: "lake", Bucket: "lake", Prefix: "lake"}},
			Catalog: v1.CatalogRef{Bucket: "lake", Prefix: "lake"},
		}}}
	}
	bucket := func(a *v1.App) { a.Spec.Buckets = []v1.AppBucket{{Name: "todo-files"}} }
	const yes, no, unk = v1.ConditionTrue, v1.ConditionFalse, v1.ConditionUnknown
	for _, tc := range []struct {
		name      string
		section   func(*v1.App)
		kind      v1.Kind
		part      v1.ObjectName
		status    func(gen int64) (v1.Phase, []v1.Condition)
		state     v1.AppChildState
		reason    string
		ready     v1.ConditionStatus
		appReason string
	}{
		{"a serving Function", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", 0), cond("ShapeValid", yes, "", g), cond("RevisionReady", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"an idle Function that served", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseIdle, []v1.Condition{cond("Ready", no, "NoReplicas", 0), cond("ShapeValid", yes, "", g), cond("RevisionReady", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"right after the activator's wake write", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseDeploying, []v1.Condition{cond("Ready", no, "NoReplicas", 0), cond("ShapeValid", yes, "", g), cond("RevisionReady", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a waking Function", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseDeploying, []v1.Condition{cond("Ready", no, "ShimNotReady", 0), cond("ShapeValid", yes, "", g), cond("RevisionReady", no, "Progressing", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a Function never called", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseIdle, []v1.Condition{cond("Ready", no, "NoReplicas", 0), cond("ShapeValid", unk, "NotStarted", g), cond("RevisionReady", unk, "NotStarted", g)}
		}, v1.AppChildNotStarted, "NotStarted", unk, "NotStarted"},
		{"a Function whose replica is replaced", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseDegraded, []v1.Condition{cond("Ready", no, "Restarting", 0), cond("ShapeValid", yes, "", g), cond("RevisionReady", yes, "", g)}
		}, v1.AppChildPending, "Restarting", no, "Progressing"},
		{"a Function whose new generation has not served", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseDeploying, []v1.Condition{cond("Ready", yes, "", 0), cond("ShapeValid", yes, "", g-1), cond("RevisionReady", no, "Progressing", g)}
		}, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a Function that failed to start", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseFailed, []v1.Condition{cond("Ready", no, "StartFailed", 0), cond("ShapeValid", unk, "NotStarted", g), cond("RevisionReady", no, "StartFailed", g)}
		}, v1.AppChildPending, "StartFailed", no, "Progressing"},
		{"a Function with a status of an earlier generation", fn, v1.KindFunction, "todo-api", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", 0), cond("ShapeValid", yes, "", g-1), cond("RevisionReady", yes, "", g-1)}
		}, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a ready KVStore", kv, v1.KindKVStore, "todo-store", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a KVStore Ready without observedGeneration", kv, v1.KindKVStore, "todo-store", func(int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", 0)}
		}, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a KVStore not ready at its generation", kv, v1.KindKVStore, "todo-store", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhasePending, []v1.Condition{cond("Ready", no, "QuotaExceeded", g)}
		}, v1.AppChildPending, "QuotaExceeded", no, "Progressing"},
		{"a timer EventSource at a new generation", timer, v1.KindEventSource, "todo-tick", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", g-1)}
		}, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a ready timer EventSource", timer, v1.KindEventSource, "todo-tick", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a CatalogService with no condition", catalog, v1.KindCatalogService, "todo-lake", nil, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a ready CatalogService", catalog, v1.KindCatalogService, "todo-lake", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a Bucket before its first pass", bucket, v1.KindBucket, "todo-files", nil, v1.AppChildPending, "Progressing", no, "Progressing"},
		{"a ready Bucket", bucket, v1.KindBucket, "todo-files", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseReady, []v1.Condition{cond("Ready", yes, "", g)}
		}, v1.AppChildReady, "", yes, ""},
		{"a Bucket whose storage is unreachable", bucket, v1.KindBucket, "todo-files", func(g int64) (v1.Phase, []v1.Condition) {
			return v1.PhaseDegraded, []v1.Condition{cond("Ready", no, "StorageUnreachable", g)}
		}, v1.AppChildPending, "StorageUnreachable", no, "Progressing"},
	} {
		tt := tc
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.create(todoApp(func(a *v1.App) {
				a.Spec = v1.AppSpec{}
				tt.section(a)
			}))
			h.reconcile()
			if tt.status != nil {
				obj := h.get(tt.kind, tt.part)
				phase, conds := tt.status(obj.GetObjectMeta().Generation)
				setStatus(obj, phase, conds...)
				h.update(obj)
				h.reconcile()
			}
			require.Equal(t, v1.AppChild{Kind: tt.kind, Name: tt.part, State: tt.state, Reason: tt.reason}, h.child(tt.kind, tt.part))
			c := h.ready()
			require.Equal(t, tt.ready, c.Status, c.Message)
			require.Equal(t, tt.appReason, c.Reason)
			if tt.ready != v1.ConditionTrue {
				require.Contains(t, c.Message, string(tt.kind)+"/"+string(tt.part)+": ", "the message names the part")
			}
		})
	}
}

// scenario: app-degraded-recovers (the reconciler half) — a part that turns Pending after the App was Ready degrades
// it, naming the part's reason and message, until the cause clears.
func TestScenarioAppDegradedRecovers(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	fn := h.get(v1.KindFunction, "todo-api")
	setStatus(fn, v1.PhaseDegraded, v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "Restarting", Message: "a replica exited and is being replaced"})
	h.update(fn)
	h.reconcile()
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	c := h.ready()
	require.Equal(t, "ChildNotReady", c.Reason)
	require.Equal(t, "Function/todo-api: Restarting: a replica exited and is being replaced", c.Message)

	h.markReady(v1.KindFunction, "todo-api")
	h.reconcile()
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
}

// ADR-0221: a Function that serves while a gate fails (phase Ready, Ready=True, RevisionReady=False with the gate's
// reason at its generation) is Pending, and the App is Degraded ChildNotReady naming the gate's reason.
func TestAppFunctionServingUnderGateIsChildNotReady(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	fn := h.get(v1.KindFunction, "todo-api")
	gen := fn.GetObjectMeta().Generation
	gate := cond("RevisionReady", v1.ConditionFalse, "ConfigResolveFailed", gen)
	gate.Message = `configmap "app" not found`
	setStatus(fn, v1.PhaseReady, cond("Ready", v1.ConditionTrue, "", 0), cond("ShapeValid", v1.ConditionTrue, "", gen), gate)
	h.update(fn)
	h.reconcile()
	require.Equal(t, v1.AppChild{Kind: v1.KindFunction, Name: "todo-api", State: v1.AppChildPending, Reason: "ConfigResolveFailed"},
		h.child(v1.KindFunction, "todo-api"))
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotReady", `Function/todo-api: ConfigResolveFailed: configmap "app" not found`)
}

// scenario: app-ref-waits (the reconciler half) — a missing ref target is RefNotFound; once it exists and serves the
// App is Ready; the App never writes or owns it.
func TestScenarioAppRefWaits(t *testing.T) {
	h := newHarness(t, nil)
	h.create(todoApp(func(a *v1.App) { a.Spec = v1.AppSpec{Functions: []v1.AppFunction{{Ref: "mailer"}}} }))
	h.reconcile()
	c := h.ready()
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "RefNotFound", c.Reason)
	require.Contains(t, c.Message, "Function/mailer")
	require.Nil(t, h.get(v1.KindFunction, "mailer"), "a ref object is never created")

	mailer := h.create(&v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "mailer", Namespace: ns, ResourceGroup: "rg1"}, Spec: apiSpec()})
	require.Equal(t, []controller.Request{{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "todo"}}, h.r.MapPart(h.ctx, mailer))
	h.markReady(v1.KindFunction, "mailer")
	rv := h.get(v1.KindFunction, "mailer").GetObjectMeta().ResourceVersion
	h.reconcile()
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
	got := h.get(v1.KindFunction, "mailer")
	require.Equal(t, rv, got.GetObjectMeta().ResourceVersion, "a ref object is never written")
	require.Empty(t, refsOf(got), "a ref object is never owned")
}

// scenario: app-prune-dropped-part (the reconciler half) — a dropped part goes once no part is Pending and the new
// revision is current (ADR-0200 Decision 7); a retained store has no controller reference and stays with its marker.
func TestScenarioAppPruneDroppedPart(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(func(a *v1.App) {
		a.Spec.Routes = nil
		a.Spec.KV = a.Spec.KV[1:]
		a.Spec.Functions[0].KV = a.Spec.Functions[0].KV[1:]
	})
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter, "the edited Function is Pending: prune waits")
	require.NotNil(t, h.get(v1.KindRoute, "todo-api"))
	require.Equal(t, v1.AppChild{Kind: v1.KindRoute, Name: "todo-api", State: v1.AppChildPruning, Reason: "NotCurrent"}, h.child(v1.KindRoute, "todo-api"))
	require.Equal(t, v1.ConditionFalse, h.ready().Status, "the Pending Function decides Ready, not the Pruning Route")

	h.markReady(v1.KindFunction, "todo-api")
	require.Zero(t, h.reconcile())
	require.Nil(t, h.get(v1.KindRoute, "todo-api"))
	kept := h.get(v1.KindKVStore, "todo-store")
	require.NotNil(t, kept)
	require.Len(t, refsOf(kept), 1, "it keeps its marker")
	require.Len(t, h.app().Status.Children, 5)
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
}

// scenario: app-store-in-use-kept (the reconciler half) — a dropped store a declared Function binds stays Pruning
// InUse naming it, without changing Ready; once unbound it goes.
func TestScenarioAppStoreInUseKept(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	h.edit(func(a *v1.App) { a.Spec.KV = a.Spec.KV[:1] })
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter, "its blockers do not requeue the App")
	require.NotNil(t, h.get(v1.KindKVStore, "todo-cache"))
	require.Equal(t, v1.AppChild{Kind: v1.KindKVStore, Name: "todo-cache", State: v1.AppChildPruning, Reason: "InUse: Function/todo-api"}, h.child(v1.KindKVStore, "todo-cache"))
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)

	h.edit(func(a *v1.App) { a.Spec.Functions[0].KV = a.Spec.Functions[0].KV[:1] })
	h.reconcile()
	h.markReady(v1.KindFunction, "todo-api")
	require.Zero(t, h.reconcile())
	require.Nil(t, h.get(v1.KindKVStore, "todo-cache"))
}

// holdPins stores an open WorkflowRun whose status pins a revision of fn (ADR-0190).
func holdPins(h *harness, fn v1.Object) *v1.WorkflowRun {
	h.t.Helper()
	run := &v1.WorkflowRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "r1", Namespace: ns, ResourceGroup: "rg1"}}
	run.Spec.Workflow = "wf"
	run = h.create(run).(*v1.WorkflowRun)
	run.Status.Phase = v1.RunRunning
	m := fn.GetObjectMeta()
	run.Status.Pins = []v1.RevisionPin{{Function: m.Name, FunctionUID: m.UID, Revision: m.Name + "-1"}}
	return h.update(run).(*v1.WorkflowRun)
}

// Decision 6: prune keeps a Function an open run holds and what it uses, then deletes both in one pass once the run
// ends; a dropped Bucket goes through gc.DeleteBucket; an object another incarnation controls is not pruned.
func TestAppPruneSkips(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	other := h.create(&v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute},
		ObjectMeta: v1.ObjectMeta{Name: "old-route", Namespace: ns, ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{
			{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: "an-earlier-uid", Controller: true},
		}},
		Spec: v1.RouteSpec{Rules: []v1.RouteRule{{Path: "/old", Backend: v1.RouteBackend{Function: "todo-api"}}}}})
	run := holdPins(h, h.get(v1.KindFunction, "todo-api"))
	h.edit(func(a *v1.App) {
		a.Spec.KV = a.Spec.KV[:1]
		a.Spec.Buckets = a.Spec.Buckets[:1]
		a.Spec.Functions = nil
		a.Spec.Routes = nil
	})
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter)
	require.Equal(t, "RunHeld", h.child(v1.KindFunction, "todo-api").Reason)
	require.Equal(t, "InUse: Function/todo-api", h.child(v1.KindKVStore, "todo-cache").Reason, "a kept Function counts as not deleted")
	require.Nil(t, h.get(v1.KindRoute, "todo-api"))
	require.Nil(t, h.get(v1.KindBucket, "todo-tmp"), "nothing it does not delete uses it")
	require.Equal(t, []string{"default/todo-tmp", "default/todo-tmp"}, h.p.calls, "a Bucket goes through DeleteBucket")

	run.Status.Phase = v1.RunCancelled
	h.update(run)
	require.Zero(t, h.reconcile())
	require.Nil(t, h.get(v1.KindFunction, "todo-api"))
	require.Nil(t, h.get(v1.KindKVStore, "todo-cache"), "its only user is deleted in the same pass")
	require.Equal(t, other.GetObjectMeta().ResourceVersion, h.get(v1.KindRoute, "old-route").GetObjectMeta().ResourceVersion,
		"prune deletes only what this App's UID controls")
	require.NotNil(t, h.get(v1.KindKVStore, "todo-store"))
	require.NotNil(t, h.get(v1.KindBucket, "todo-files"))
	require.False(t, slices.ContainsFunc(h.app().Status.Children, func(c v1.AppChild) bool { return c.State == v1.AppChildPruning }))
}

// Decision 4: MapPart requeues the App a controller reference or marker names, and each App whose ref entry names obj.
func TestMapPart(t *testing.T) {
	h := newHarness(t, nil)
	a := h.create(todoApp(func(a *v1.App) { a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Ref: "mailer"}) })).(*v1.App)
	h.create(todoApp(func(b *v1.App) {
		b.Name = "other"
		b.Spec = v1.AppSpec{KV: []v1.AppKVStore{{Ref: "todo-store"}}}
	}))
	h.reconcile()
	req := func(name v1.ObjectName) controller.Request {
		return controller.Request{GVK: v1.KindApp.GVK(), Namespace: ns, Name: name}
	}
	require.Equal(t, []controller.Request{req("todo")}, h.r.MapPart(h.ctx, h.get(v1.KindFunction, "todo-api")), "a controller reference")
	require.Equal(t, []controller.Request{req("todo"), req("other")}, h.r.MapPart(h.ctx, h.get(v1.KindKVStore, "todo-store")), "a marker and a ref entry")
	mailer := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "mailer", Namespace: ns, ResourceGroup: "rg1"}}
	require.Equal(t, []controller.Request{req("todo")}, h.r.MapPart(h.ctx, mailer), "a ref entry")
	unrelated := &v1.Function{TypeMeta: mailer.TypeMeta, ObjectMeta: v1.ObjectMeta{Name: "audit", Namespace: ns, ResourceGroup: "rg1"}}
	require.Empty(t, h.r.MapPart(h.ctx, unrelated))
	require.NotEmpty(t, a.UID)
}
