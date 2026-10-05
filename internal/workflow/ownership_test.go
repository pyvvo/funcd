package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
)

func storedWorkflow(t *testing.T, s store.Store, name v1.ObjectName, steps []string, kv ...v1.WorkflowKVStore) *v1.Workflow {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{KV: kv},
	}
	for _, st := range steps {
		wf.Spec.Steps = append(wf.Spec.Steps, v1.WorkflowStep{Name: v1.ObjectName(st), Function: &v1.FunctionStep{Image: "oci:" + st}})
	}
	out, err := s.Create(context.Background(), wf)
	require.NoError(t, err)
	return out.(*v1.Workflow)
}

func getObj(t *testing.T, s store.Store, kind v1.Kind, name v1.ObjectName) v1.Object {
	t.Helper()
	o, err := s.Get(context.Background(), kind.GVK(), "default", name)
	require.NoError(t, err, "%s/%s", kind, name)
	return o
}

func gone(t *testing.T, s store.Store, kind v1.Kind, name v1.ObjectName) bool {
	t.Helper()
	_, err := s.Get(context.Background(), kind.GVK(), "default", name)
	return fault.KindOf(err) == fault.NotFound
}

func controllerUID(o v1.Object) v1.UID {
	r, _ := v1.ControllerOf(o.GetObjectMeta().OwnerReferences)
	return r.UID
}

func reasonOf(err error) string {
	var no *notOwnedError
	if errors.As(err, &no) {
		return no.reason
	}
	return ""
}

func storeKV(name v1.ObjectName, deletion v1.DeletionPolicy) v1.WorkflowKVStore {
	return v1.WorkflowKVStore{Name: name, Deletion: deletion, Tables: []v1.KVTable{{Name: "t"}}}
}

// scenario: recreated-workflow-takes-new-uid — step Functions naming a deleted gcwf's UID are re-stamped with the
// re-applied gcwf's UID, so two collector passes delete none of them (its KVStore part is
// TestScenarioRecreatedWorkflowNeedsHandover).
func TestScenarioRecreatedWorkflowTakesNewUid(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	old := storedWorkflow(t, s, "gcwf", []string{"s1", "s2"})
	require.NoError(t, m.Materialize(ctx, old))
	require.NoError(t, s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "gcwf", ""))

	wf := storedWorkflow(t, s, "gcwf", []string{"s1", "s2"})
	require.NotEqual(t, old.UID, wf.UID)
	require.NoError(t, m.Materialize(ctx, wf))
	for _, c := range []struct {
		kind v1.Kind
		name v1.ObjectName
	}{{v1.KindFunction, "gcwf-s1"}, {v1.KindFunction, "gcwf-s2"}} {
		require.Equal(t, wf.UID, controllerUID(getObj(t, s, c.kind, c.name)), "%s carries the new UID", c.name)
	}

	col, err := gc.New(gc.Deps{Store: s})
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, col.CollectNamespace(ctx, "default"))
	}
	require.False(t, gone(t, s, v1.KindFunction, "gcwf-s1"))
	require.False(t, gone(t, s, v1.KindFunction, "gcwf-s2"))
}

func TestMaterializeRefusesAFunctionItDoesNotOwn(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()

	a := storedWorkflow(t, s, "a", []string{"b-c"})
	require.NoError(t, m.Materialize(ctx, a))
	user := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "u-s", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci:user"},
	}
	_, err := s.Create(ctx, user)
	require.NoError(t, err)
	before := map[v1.ObjectName]string{
		"a-b-c": getObj(t, s, v1.KindFunction, "a-b-c").GetObjectMeta().ResourceVersion,
		"u-s":   getObj(t, s, v1.KindFunction, "u-s").GetObjectMeta().ResourceVersion,
	}

	ab := storedWorkflow(t, s, "a-b", []string{"c"})
	require.Equal(t, "FunctionNotOwned", reasonOf(m.Materialize(ctx, ab)), "a-b's step c is a's a-b-c")
	u := storedWorkflow(t, s, "u", []string{"s"})
	require.Equal(t, "FunctionNotOwned", reasonOf(m.Materialize(ctx, u)), "u's step s is the user's u-s")
	for name, rv := range before {
		o := getObj(t, s, v1.KindFunction, name)
		require.Equal(t, rv, o.GetObjectMeta().ResourceVersion, "%s is not written", name)
	}
	require.Equal(t, a.UID, controllerUID(getObj(t, s, v1.KindFunction, "a-b-c")))
	require.Empty(t, getObj(t, s, v1.KindFunction, "u-s").GetObjectMeta().OwnerReferences)
}

func TestMaterializeRefusesAKVStoreAnotherOwnerControls(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	a := storedWorkflow(t, s, "a", []string{"x"}, storeKV("shared", v1.DeletionDelete))
	require.NoError(t, m.Materialize(ctx, a))
	rv := getObj(t, s, v1.KindKVStore, "shared").GetObjectMeta().ResourceVersion

	b := storedWorkflow(t, s, "b", []string{"y"}, storeKV("shared", v1.DeletionDelete))
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, b)))
	require.Equal(t, rv, getObj(t, s, v1.KindKVStore, "shared").GetObjectMeta().ResourceVersion, "no write")
	require.Equal(t, a.UID, controllerUID(getObj(t, s, v1.KindKVStore, "shared")))

	free := &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: "free", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}},
	}
	_, err := s.Create(ctx, free)
	require.NoError(t, err)
	c := storedWorkflow(t, s, "c", []string{"z"}, storeKV("free", v1.DeletionDelete))
	require.Equal(t, "KVStoreNotOwned", reasonOf(m.Materialize(ctx, c)), "an unowned store is not adopted")
	require.Empty(t, getObj(t, s, v1.KindKVStore, "free").GetObjectMeta().OwnerReferences)
}

func TestReconcileNotOwnedIsNotReadyAndRequeues(t *testing.T) {
	for _, period := range []time.Duration{0, 3 * time.Second} {
		s := newStore(t)
		m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, period)
		r := NewWorkflowReconciler(s, m, nil, nil)
		ctx := context.Background()
		storedWorkflow(t, s, "a", []string{"x"}, storeKV("shared", v1.DeletionDelete))
		_, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: "a"})
		require.NoError(t, err)
		storedWorkflow(t, s, "b", []string{"y"}, storeKV("shared", v1.DeletionRetain))

		res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: "b"})
		require.NoError(t, err)
		want := period
		if want == 0 {
			want = controller.SupervisionPeriod
		}
		require.Equal(t, want, res.RequeueAfter)
		b := getObj(t, s, v1.KindWorkflow, "b").(*v1.Workflow)
		ready, ok := b.Status.Conditions.Get(condReady)
		require.True(t, ok)
		require.Equal(t, v1.ConditionFalse, ready.Status)
		require.Equal(t, "KVStoreNotOwned", ready.Reason)
		require.Equal(t, getObj(t, s, v1.KindWorkflow, "a").GetObjectMeta().UID, controllerUID(getObj(t, s, v1.KindKVStore, "shared")))
	}
}

func TestPruneFunctionsDeletesRemovedStepsOnly(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := storedWorkflow(t, s, "gcwf", []string{"s1", "s2", "s3"}, storeKV("gcwf-state", v1.DeletionDelete), storeKV("gcwf-extra", v1.DeletionDelete))
	require.NoError(t, m.Materialize(ctx, wf))

	wf = getObj(t, s, v1.KindWorkflow, "gcwf").(*v1.Workflow)
	wf.Spec.Steps = wf.Spec.Steps[:2]
	wf.Spec.Steps[1].Function = &v1.FunctionStep{Ref: "elsewhere"}
	wf.Spec.KV = wf.Spec.KV[:1]
	require.NoError(t, m.Materialize(ctx, wf))

	require.False(t, gone(t, s, v1.KindFunction, "gcwf-s1"), "a kept image step stays")
	require.True(t, gone(t, s, v1.KindFunction, "gcwf-s2"), "a step changed to a function reference is pruned")
	require.True(t, gone(t, s, v1.KindFunction, "gcwf-s3"), "a removed step is pruned")
	require.False(t, gone(t, s, v1.KindKVStore, "gcwf-extra"), "KVStores are never pruned")
}

func TestPruneFunctionsLeavesAnotherUIDsFunction(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	old := storedWorkflow(t, s, "gcwf", []string{"s1", "s2"})
	require.NoError(t, m.Materialize(ctx, old))
	require.NoError(t, s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "gcwf", ""))
	wf := storedWorkflow(t, s, "gcwf", []string{"s1"})
	require.NoError(t, m.Materialize(ctx, wf))
	require.Equal(t, old.UID, controllerUID(getObj(t, s, v1.KindFunction, "gcwf-s2")), "the collector, not the prune, reclaims a deleted namesake's step")
}
