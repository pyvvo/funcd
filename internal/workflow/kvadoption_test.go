package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
)

func apiKVStore(t *testing.T, s store.Store, name v1.ObjectName) *v1.KVStore {
	t.Helper()
	out, err := s.Create(context.Background(), &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}},
	})
	require.NoError(t, err)
	return out.(*v1.KVStore)
}

// previousStore is a store the materializer before markers made for wf: no marker, the controller ref only
// under delete, and a table owned by wf's first step.
func previousStore(t *testing.T, s store.Store, name v1.ObjectName, wf *v1.Workflow, controller bool) *v1.KVStore {
	t.Helper()
	st := &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t", Owner: materializedName(wf, wf.Spec.Steps[0].Name)}}},
	}
	if controller {
		st.OwnerReferences = []v1.OwnerReference{ownerRef(wf)}
	}
	out, err := s.Create(context.Background(), st)
	require.NoError(t, err)
	return out.(*v1.KVStore)
}

func bindStep(wf *v1.Workflow, step int, stores ...v1.ObjectName) {
	wf.Spec.Steps[step].Function.KV = nil
	for _, st := range stores {
		wf.Spec.Steps[step].Function.KV = append(wf.Spec.Steps[step].Function.KV, v1.FunctionKV{Alias: string(st), Store: st, Table: "t"})
	}
}

func setFunctionKV(t *testing.T, s store.Store, name v1.ObjectName, stores ...v1.ObjectName) {
	t.Helper()
	fn := getObj(t, s, v1.KindFunction, name).(*v1.Function)
	fn.Spec.KV = nil
	for _, st := range stores {
		fn.Spec.KV = append(fn.Spec.KV, v1.FunctionKV{Alias: string(st), Store: st, Table: "t"})
	}
	_, err := s.Update(context.Background(), fn)
	require.NoError(t, err)
}

func boundStores(t *testing.T, s store.Store, fn v1.ObjectName) []v1.ObjectName {
	t.Helper()
	var out []v1.ObjectName
	for _, b := range getObj(t, s, v1.KindFunction, fn).(*v1.Function).Spec.KV {
		out = append(out, b.Store)
	}
	return out
}

func rvOf(t *testing.T, s store.Store, kind v1.Kind, name v1.ObjectName) string {
	t.Helper()
	return getObj(t, s, kind, name).GetObjectMeta().ResourceVersion
}

func collectTwice(t *testing.T, s store.Store) {
	t.Helper()
	col, err := gc.New(gc.Deps{Store: s, Purger: noPurge{}})
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, col.CollectNamespace(context.Background(), "default"))
	}
}

// scenario: own-store-follows-policy-and-tables
func TestScenarioOwnStoreFollowsPolicyAndTables(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := storedWorkflow(t, s, "w", []string{"s"}, storeKV("w-kv", v1.DeletionRetain))
	require.NoError(t, m.Materialize(ctx, wf))
	uid := getObj(t, s, v1.KindKVStore, "w-kv").GetObjectMeta().UID
	check := func(controllers, tables int) {
		t.Helper()
		st := getObj(t, s, v1.KindKVStore, "w-kv").(*v1.KVStore)
		require.Equal(t, uid, st.UID, "the same store")
		require.True(t, marked(st.OwnerReferences, wf), "the marker stays")
		require.Equal(t, controllers, controllerRefs(st.OwnerReferences))
		require.Len(t, st.Spec.Tables, tables)
	}
	check(0, 1)
	wf.Spec.KV[0].Deletion = v1.DeletionDelete
	require.NoError(t, m.Materialize(ctx, wf))
	check(1, 1)
	wf.Spec.KV[0].Tables = append(wf.Spec.KV[0].Tables, v1.KVTable{Name: "t2"})
	require.NoError(t, m.Materialize(ctx, wf))
	check(1, 2)
	wf.Spec.KV[0].Deletion = v1.DeletionRetain
	require.NoError(t, m.Materialize(ctx, wf))
	check(0, 2)
}

// scenario: users-store-refused
func TestScenarioUsersStoreRefused(t *testing.T) {
	for _, policy := range []v1.DeletionPolicy{v1.DeletionRetain, v1.DeletionDelete} {
		s := newStore(t)
		m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
		ctx := context.Background()
		user := apiKVStore(t, s, "shared")
		w := storedWorkflow(t, s, "w", []string{"s"}, storeKV("shared", policy))
		require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, w)), policy)
		got := getObj(t, s, v1.KindKVStore, "shared").(*v1.KVStore)
		require.Equal(t, user.ResourceVersion, got.ResourceVersion, "no write")
		require.Empty(t, got.OwnerReferences)
		require.Equal(t, user.Spec.Tables, got.Spec.Tables)

		require.NoError(t, s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "w", ""))
		collectTwice(t, s)
		require.False(t, gone(t, s, v1.KindKVStore, "shared"), "the user's store stays")
	}
}

// scenario: other-workflows-store-refused
func TestScenarioOtherWorkflowsStoreRefused(t *testing.T) {
	for _, policy := range []v1.DeletionPolicy{v1.DeletionRetain, v1.DeletionDelete} {
		s := newStore(t)
		m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
		ctx := context.Background()
		a := storedWorkflow(t, s, "a", []string{"x"}, storeKV("shared", policy))
		require.NoError(t, m.Materialize(ctx, a))
		rv := rvOf(t, s, v1.KindKVStore, "shared")
		b := storedWorkflow(t, s, "b", []string{"y"}, storeKV("shared", policy))
		require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, b)), policy)
		got := getObj(t, s, v1.KindKVStore, "shared").(*v1.KVStore)
		require.Equal(t, rv, got.ResourceVersion, "no write")
		require.True(t, marked(got.OwnerReferences, a))
		require.False(t, marked(got.OwnerReferences, b))
	}
}

// scenario: upgrade-migration-marks-own-store
func TestScenarioUpgradeMigrationMarksOwnStore(t *testing.T) {
	for _, controller := range []bool{false, true} {
		s := newStore(t)
		m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
		ctx := context.Background()
		policy, flipped := v1.DeletionRetain, v1.DeletionDelete
		if controller {
			policy, flipped = flipped, policy
		}
		kv := storeKV("w-kv", policy)
		kv.Tables[0].Owner = "s"
		w := storedWorkflow(t, s, "w", []string{"s"}, kv)
		prev := previousStore(t, s, "w-kv", w, controller)

		require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
		got := getObj(t, s, v1.KindKVStore, "w-kv").(*v1.KVStore)
		require.Equal(t, append(prev.OwnerReferences, kvMarker(w)), got.OwnerReferences, "only the marker is added")
		require.Equal(t, prev.Spec, got.Spec)

		require.NoError(t, m.Materialize(ctx, w), "w is Ready")
		w.Spec.KV[0].Deletion = flipped
		require.NoError(t, m.Materialize(ctx, w))
		got = getObj(t, s, v1.KindKVStore, "w-kv").(*v1.KVStore)
		require.Equal(t, prev.UID, got.UID)
		require.True(t, marked(got.OwnerReferences, w))
		require.Equal(t, map[bool]int{false: 1, true: 0}[controller], controllerRefs(got.OwnerReferences), "the policy flip applies")
	}
}

// scenario: post-upgrade-api-store-refused
func TestScenarioPostUpgradeApiStoreRefused(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	w := storedWorkflow(t, s, "w", []string{"s"})
	require.NoError(t, m.Materialize(ctx, w))
	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))

	user := apiKVStore(t, s, "shared")
	w.Spec.KV = []v1.WorkflowKVStore{storeKV("shared", v1.DeletionRetain)}
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, w)))
	require.Equal(t, user.ResourceVersion, rvOf(t, s, v1.KindKVStore, "shared"), "no write")
}

// scenario: step-binding-to-unowned-store-refused
func TestScenarioStepBindingToUnownedStoreRefused(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	user := apiKVStore(t, s, "shared")
	w := storedWorkflow(t, s, "w", []string{"s", "s2"}, storeKV("w-kv", v1.DeletionRetain))
	require.NoError(t, m.Materialize(ctx, w))
	setFunctionKV(t, s, "w-s", "shared")

	bindStep(w, 0, "shared")
	bindStep(w, 1, "w-kv")
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, w)))
	require.Empty(t, boundStores(t, s, "w-s"))
	require.Empty(t, boundStores(t, s, "w-s2"))
	require.Equal(t, user.ResourceVersion, rvOf(t, s, v1.KindKVStore, "shared"), "no write")
}

// scenario: stale-step-binding-removed (as corrected: a refusal keeps the incarnation's own bindings)
func TestScenarioStaleStepBindingRemoved(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	user := apiKVStore(t, s, "shared")
	w := storedWorkflow(t, s, "w", []string{"s", "s2"}, storeKV("w-kv", v1.DeletionRetain))
	require.NoError(t, m.Materialize(ctx, w))
	setFunctionKV(t, s, "w-s2", "shared")

	bindStep(w, 0, "w-kv")
	require.NoError(t, m.Materialize(ctx, w), "w is Ready")
	require.Equal(t, []v1.ObjectName{"w-kv"}, boundStores(t, s, "w-s"))
	require.Empty(t, boundStores(t, s, "w-s2"))
	require.Equal(t, user.ResourceVersion, rvOf(t, s, v1.KindKVStore, "shared"))

	w.Spec.KV = append(w.Spec.KV, storeKV("shared", v1.DeletionRetain))
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, w)))
	require.Equal(t, []v1.ObjectName{"w-kv"}, boundStores(t, s, "w-s"), "its own binding stays")
	require.Empty(t, boundStores(t, s, "w-s2"))
	require.Equal(t, user.ResourceVersion, rvOf(t, s, v1.KindKVStore, "shared"))
}

type failingRuntimes struct{ bad string }

func (f failingRuntimes) Runtime(_ context.Context, image string) (v1.RuntimeName, error) {
	if image == f.bad {
		return "", errors.New("no runtime for image")
	}
	return "nodejs22", nil
}

// reconcileReady stores w as Ready, reconciles it with runtimes rt, and returns w's Ready condition after.
func reconcileReady(t *testing.T, s store.Store, rt RuntimeResolver, w *v1.Workflow) (v1.Condition, error) {
	t.Helper()
	ctx := context.Background()
	w.Status.Phase = v1.PhaseReady
	w.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "EdgesTypeChecked"})
	_, err := s.Update(ctx, w)
	require.NoError(t, err)
	r := NewWorkflowReconciler(s, NewMaterializer(s, rt, nil, 0), nil, nil, 0)
	_, rerr := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: w.Name})
	ready, ok := getObj(t, s, v1.KindWorkflow, w.Name).(*v1.Workflow).Status.Conditions.Get(condReady)
	require.True(t, ok)
	return ready, rerr
}

// scenario: strip-survives-removed-step-and-early-failure
func TestScenarioStripSurvivesRemovedStepAndEarlyFailure(t *testing.T) {
	ctx := context.Background()
	t.Run("removed step and refusal", func(t *testing.T) {
		s := newStore(t)
		apiKVStore(t, s, "shared")
		w := storedWorkflow(t, s, "w", []string{"s0", "s"})
		require.NoError(t, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0).Materialize(ctx, w))
		setFunctionKV(t, s, "w-s", "shared")

		w.Spec.Steps = w.Spec.Steps[:1]
		w.Spec.KV = []v1.WorkflowKVStore{storeKV("shared", v1.DeletionRetain)}
		ready, err := reconcileReady(t, s, fakeRuntimes{rt: "nodejs22"}, w)
		require.NoError(t, err)
		require.Equal(t, v1.ConditionFalse, ready.Status, "w is NotReady")
		require.Equal(t, "KVStoreNotOwned", ready.Reason)
		require.Empty(t, boundStores(t, s, "w-s"))
	})
	t.Run("earlier step runtime unresolved", func(t *testing.T) {
		s := newStore(t)
		apiKVStore(t, s, "shared")
		w := storedWorkflow(t, s, "w", []string{"s0", "s"})
		require.NoError(t, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0).Materialize(ctx, w))
		setFunctionKV(t, s, "w-s", "shared")

		ready, err := reconcileReady(t, s, failingRuntimes{bad: "oci:s0"}, w)
		require.Error(t, err, "the failure requeues")
		require.Empty(t, reasonOf(err), "the runtime failure, not a refusal")
		require.Equal(t, v1.ConditionFalse, ready.Status, "w is NotReady")
		require.Equal(t, "MaterializeFailed", ready.Reason)
		require.Empty(t, boundStores(t, s, "w-s"))
	})
}

// A re-created Workflow has a new UID, so the marker its predecessor wrote does not make the store its own.
func TestRecreatedWorkflowRefusesPredecessorsStore(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	w1 := storedWorkflow(t, s, "w", []string{"s"}, storeKV("w-keep", v1.DeletionRetain))
	require.NoError(t, m.Materialize(ctx, w1))
	require.NoError(t, s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "w", ""))
	collectTwice(t, s)
	rv := rvOf(t, s, v1.KindKVStore, "w-keep")

	w2 := storedWorkflow(t, s, "w", []string{"s"}, storeKV("w-keep", v1.DeletionRetain))
	require.NotEqual(t, w1.UID, w2.UID)
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, w2)))
	require.Equal(t, rv, rvOf(t, s, v1.KindKVStore, "w-keep"), "no write")
	require.True(t, marked(getObj(t, s, v1.KindKVStore, "w-keep").GetObjectMeta().OwnerReferences, w1))
}

// The migration strips a step's binding to a store it leaves unmarked and keeps the one to a store it marks.
func TestMigrationStripsBindingsToStoresLeftUnmarked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	kv := storeKV("w-kv", v1.DeletionRetain)
	kv.Tables[0].Owner = "s"
	w := storedWorkflow(t, s, "w", []string{"s"}, kv)
	steps := *w
	steps.Spec.KV = nil
	require.NoError(t, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0).Materialize(ctx, &steps))
	previousStore(t, s, "w-kv", w, false)
	apiKVStore(t, s, "shared")
	setFunctionKV(t, s, "w-s", "w-kv", "shared")

	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
	require.True(t, marked(getObj(t, s, v1.KindKVStore, "w-kv").GetObjectMeta().OwnerReferences, w))
	require.Empty(t, getObj(t, s, v1.KindKVStore, "shared").GetObjectMeta().OwnerReferences)
	require.Equal(t, []v1.ObjectName{"w-kv"}, boundStores(t, s, "w-s"))
}

// scenario: migration-record-stops-rerun
func TestScenarioMigrationRecordStopsRerun(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
	_, err := s.Get(ctx, v1.KindConfigMap.GVK(), KVMigrationNamespace, KVMigrationRecord)
	require.NoError(t, err, "the record is written")

	kv := storeKV("w-kv", v1.DeletionRetain)
	kv.Tables[0].Owner = "s"
	w := storedWorkflow(t, s, "w", []string{"s"}, kv)
	prev := previousStore(t, s, "w-kv", w, false)
	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
	got := getObj(t, s, v1.KindKVStore, "w-kv")
	require.Equal(t, prev.ResourceVersion, got.GetObjectMeta().ResourceVersion, "the store stays unmarked")
	require.Empty(t, got.GetObjectMeta().OwnerReferences)
}

// Issue 830: the KVStore migration logs through the logger funcd gives it, not through slog's default.
func TestIssue830_KVMigrationLogsThroughItsLogger(t *testing.T) {
	var leaked, logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&leaked, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s := newStore(t)
	kv := storeKV("w-kv", v1.DeletionRetain)
	kv.Tables[0].Owner = "s"
	w := storedWorkflow(t, s, "w", []string{"s"}, kv)
	previousStore(t, s, "w-kv", w, false)

	require.NoError(t, MarkKVStoresOnce(context.Background(), s, slog.New(slog.NewTextHandler(&logs, nil))))
	require.Contains(t, logs.String(), "kvstore marked for its workflow")
	require.Contains(t, logs.String(), "component=workflow.kvstore-migration")
	require.NotContains(t, leaked.String(), "kvstore marked for its workflow")
}

// scenario: migration-audit-is-one-line
func TestScenarioMigrationAuditIsOneLine(t *testing.T) {
	var buf bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLog) })
	s := newStore(t)
	ctx := context.Background()
	want := map[string]*v1.Workflow{}
	for _, name := range []v1.ObjectName{"w1", "w2"} {
		kv := storeKV(name+"-kv", v1.DeletionRetain)
		kv.Tables[0].Owner = "s"
		w := storedWorkflow(t, s, name, []string{"s"}, kv)
		previousStore(t, s, name+"-kv", w, false)
		want[string(name)+"-kv"] = w
	}

	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2, buf.String())
	for _, line := range lines {
		var rec struct {
			Level     string `json:"level"`
			Namespace string `json:"namespace"`
			Store     string `json:"store"`
			Workflow  string `json:"workflow"`
			UID       string `json:"uid"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		w := want[rec.Store]
		require.NotNil(t, w, line)
		require.Equal(t, "default", rec.Namespace)
		require.Equal(t, string(w.Name), rec.Workflow)
		require.Equal(t, string(w.UID), rec.UID)
		require.Equal(t, "INFO", rec.Level)
		delete(want, rec.Store)
	}
	require.Empty(t, want, "one line per marked store")
}

// ADR-0199 Decision 8: a non-controller App ref is a store marker too, so the migration leaves a store an App made
// to that App even when a Workflow declares it.
func TestKVMarkerCoversAnApp(t *testing.T) {
	ref := func(kind v1.Kind, controller bool) v1.OwnerReference {
		return v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: kind, Namespace: "default", Name: "todo"}, UID: "u1", Controller: controller}
	}
	require.True(t, IsKVMarker(ref(v1.KindWorkflow, false)))
	require.True(t, IsKVMarker(ref(v1.KindApp, false)))
	require.False(t, IsKVMarker(ref(v1.KindApp, true)))
	require.False(t, IsKVMarker(ref(v1.KindSite, false)))

	s := newStore(t)
	ctx := context.Background()
	kv := storeKV("w-kv", v1.DeletionRetain)
	kv.Tables[0].Owner = "s"
	w := storedWorkflow(t, s, "w", []string{"s"}, kv)
	prev := previousStore(t, s, "w-kv", w, false)
	prev.OwnerReferences = []v1.OwnerReference{ref(v1.KindApp, false)}
	_, err := s.Update(ctx, prev)
	require.NoError(t, err)

	require.NoError(t, MarkKVStoresOnce(ctx, s, nil))
	require.Equal(t, []v1.OwnerReference{ref(v1.KindApp, false)}, getObj(t, s, v1.KindKVStore, "w-kv").GetObjectMeta().OwnerReferences)
}
