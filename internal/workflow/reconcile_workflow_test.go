package workflow

import (
	"context"
	"slices"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type fakeRuntimes struct{ rt v1.RuntimeName }

func (f fakeRuntimes) Runtime(context.Context, string) (v1.RuntimeName, error) { return f.rt, nil }

func newStore(t *testing.T) store.Store {
	t.Helper()
	s := store.New(memory.New())
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// scenario: owned-kv-materialization — the workflow materializes owned Functions and
// a KVStore (owner = the materialized function), the ADR-0073 cycle engine-internal.
func TestMaterializeOwnedFunctionsAndKV(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "default", ResourceGroup: "rg1", UID: "wf-uid"},
		Spec: v1.WorkflowSpec{
			Pooling: v1.WorkflowPooling{Mode: v1.PoolingShared, MinReplicas: 1},
			KV: []v1.WorkflowKVStore{{
				Name: "counters-kv", Deletion: v1.DeletionDelete, Tables: []v1.KVTable{{Name: "t", Owner: "ingest"}},
			}},
			Steps: []v1.WorkflowStep{
				{Name: "ingest", Function: &v1.FunctionStep{Image: "oci:ingest-v1", KV: []v1.FunctionKV{{Alias: "c", Store: "counters-kv", Table: "t"}}}},
				{Name: "solo", Function: &v1.FunctionStep{Image: "oci:solo-v1", Pooling: &v1.WorkflowPooling{Mode: v1.PoolingIsolated}}},
			},
		},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// owned Function <workflow>-<step> with runtime, artifact, pooling, scaling, owner.
	obj, err := s.Get(ctx, v1.KindFunction.GVK(), "default", "orders-ingest")
	if err != nil {
		t.Fatalf("owned function not created: %v", err)
	}
	fn := obj.(*v1.Function)
	if fn.Spec.Runtime != "nodejs22" || fn.Spec.Image != "oci:ingest-v1" {
		t.Fatalf("function spec wrong: %+v", fn.Spec)
	}
	if fn.Spec.Handler != "handle" { // materialization supplies the entrypoint so the fn passes shape validation
		t.Fatalf("materialized handler = %q, want handle", fn.Spec.Handler)
	}
	if fn.Spec.Pooling.Worker != "orders" { // shared → pool named after the workflow
		t.Fatalf("shared pooling worker = %q, want orders", fn.Spec.Pooling.Worker)
	}
	if fn.Spec.Scaling.MinReplicas != 1 {
		t.Fatalf("minReplicas = %d, want 1 (warm, no cold start)", fn.Spec.Scaling.MinReplicas)
	}
	if len(fn.OwnerReferences) != 1 || fn.OwnerReferences[0].Name != "orders" {
		t.Fatalf("owner ref missing/wrong: %+v", fn.OwnerReferences)
	}
	// kv binding patched on after the store exists (the ADR-0073 cycle).
	if len(fn.Spec.KV) != 1 || fn.Spec.KV[0].Store != "counters-kv" {
		t.Fatalf("kv binding not patched: %+v", fn.Spec.KV)
	}

	// the isolated step gets no shared worker.
	obj2, _ := s.Get(ctx, v1.KindFunction.GVK(), "default", "orders-solo")
	if w := obj2.(*v1.Function).Spec.Pooling.Worker; w != "" {
		t.Fatalf("isolated step should have no shared worker, got %q", w)
	}

	// owned KVStore with the materialized owner.
	kvObj, err := s.Get(ctx, v1.KindKVStore.GVK(), "default", "counters-kv")
	if err != nil {
		t.Fatalf("owned kvstore not created: %v", err)
	}
	kv := kvObj.(*v1.KVStore)
	if kv.Spec.Tables[0].Owner != "orders-ingest" {
		t.Fatalf("kv owner = %q, want orders-ingest (materialized)", kv.Spec.Tables[0].Owner)
	}
	if controllerRefs(kv.OwnerReferences) != 1 || !hasMarker(kv.OwnerReferences) {
		t.Fatalf("kvstore missing owner ref for cascade or marker: %+v", kv.OwnerReferences)
	}
}

// scenario core: deletion policy differentiation — a `retain` owned store carries NO cascading
// owner reference (it outlives the workflow); a `delete` one does (it cascades).
func TestMaterializeDeletionPolicy(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
		Spec: v1.WorkflowSpec{
			KV: []v1.WorkflowKVStore{
				{Name: "keep-kv", Deletion: v1.DeletionRetain, Tables: []v1.KVTable{{Name: "t"}}},
				{Name: "drop-kv", Deletion: v1.DeletionDelete, Tables: []v1.KVTable{{Name: "t"}}},
			},
			Steps: []v1.WorkflowStep{{Name: "a", Function: &v1.FunctionStep{Image: "oci:a"}}},
		},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	keep, _ := s.Get(ctx, v1.KindKVStore.GVK(), "default", "keep-kv")
	if refs := keep.(*v1.KVStore).OwnerReferences; controllerRefs(refs) != 0 || !marked(refs, wf) {
		t.Fatalf("retain store must have NO controller ref (outlives the workflow) and the marker, got %+v", refs)
	}
	drop, _ := s.Get(ctx, v1.KindKVStore.GVK(), "default", "drop-kv")
	if refs := drop.(*v1.KVStore).OwnerReferences; controllerRefs(refs) != 1 || !marked(refs, wf) {
		t.Fatalf("delete store must cascade via one controller ref and carry the marker, got %+v", refs)
	}
}

// A re-applied workflow whose kv deletion policy changed must re-derive the store's owner
// reference from the new policy, not keep the one stored at first creation (ADR-0094).
func TestIssue149_KVDeletionPolicyChangeUpdatesOwnerRef(t *testing.T) {
	cases := []struct {
		from, to   v1.DeletionPolicy
		wantOwners int
	}{
		{from: v1.DeletionDelete, to: v1.DeletionRetain, wantOwners: 0},
		{from: v1.DeletionRetain, to: v1.DeletionDelete, wantOwners: 1},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"-to-"+string(tc.to), func(t *testing.T) {
			s := newStore(t)
			m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
			ctx := context.Background()
			wf := &v1.Workflow{
				TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
				ObjectMeta: v1.ObjectMeta{Name: "wfa", Namespace: "default", ResourceGroup: "rg1", UID: "uid-a"},
				Spec: v1.WorkflowSpec{
					KV:    []v1.WorkflowKVStore{{Name: "data-kv", Deletion: tc.from, Tables: []v1.KVTable{{Name: "t"}}}},
					Steps: []v1.WorkflowStep{{Name: "a", Function: &v1.FunctionStep{Image: "oci:a"}}},
				},
			}
			if err := m.Materialize(ctx, wf); err != nil {
				t.Fatalf("first materialize: %v", err)
			}
			first, err := s.Get(ctx, v1.KindKVStore.GVK(), "default", "data-kv")
			if err != nil {
				t.Fatalf("get kvstore: %v", err)
			}
			wf.Spec.KV[0].Deletion = tc.to
			if err := m.Materialize(ctx, wf); err != nil {
				t.Fatalf("re-materialize: %v", err)
			}
			obj, err := s.Get(ctx, v1.KindKVStore.GVK(), "default", "data-kv")
			if err != nil {
				t.Fatalf("get kvstore: %v", err)
			}
			kv := obj.(*v1.KVStore)
			if kv.UID != first.(*v1.KVStore).UID {
				t.Fatalf("kvstore UID changed across re-materialize: %q -> %q", first.(*v1.KVStore).UID, kv.UID)
			}
			if !marked(kv.OwnerReferences, wf) {
				t.Fatalf("deletion %s -> %s: marker missing: %+v", tc.from, tc.to, kv.OwnerReferences)
			}
			if got := controllerRefs(kv.OwnerReferences); got != tc.wantOwners {
				t.Fatalf("deletion %s -> %s: owner refs = %+v, want %d", tc.from, tc.to, kv.OwnerReferences, tc.wantOwners)
			}
		})
	}
}

// materialize is idempotent (re-running updates, does not error on existing).
func TestMaterializeIdempotent(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "python314"}, nil, 0)
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
		Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "a", Function: &v1.FunctionStep{Image: "oci:a"}}}},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("second materialize (idempotent) failed: %v", err)
	}
}

// A re-materialize keeps the status the Function reconciler wrote, which tracks a redeploy (ADR-0143).
func TestMaterializeKeepsFunctionStatus(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
		Spec:       v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "a", Function: &v1.FunctionStep{Image: "oci:a"}}}},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	obj, err := s.Get(ctx, v1.KindFunction.GVK(), "default", "wf-a")
	if err != nil {
		t.Fatalf("owned function not created: %v", err)
	}
	fn := obj.(*v1.Function)
	fn.Status.Phase = v1.PhaseReady
	fn.Status.CurrentRevision, fn.Status.ServingRevision = "wf-a-1", "wf-a-1"
	if _, err := s.Update(ctx, fn); err != nil {
		t.Fatalf("write status: %v", err)
	}

	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	obj, err = s.Get(ctx, v1.KindFunction.GVK(), "default", "wf-a")
	if err != nil {
		t.Fatal(err)
	}
	if got := obj.(*v1.Function).Status; got.Phase != v1.PhaseReady || got.ServingRevision != "wf-a-1" {
		t.Fatalf("a re-materialize wiped the status: %+v", got)
	}
}

// Issue 19: a re-materialize keeps the status the KVStore reconciler wrote.
func TestIssue19_RematerializeKeepsKVStoreStatus(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "default", ResourceGroup: "rg1", UID: "wf-uid"},
		Spec: v1.WorkflowSpec{
			KV: []v1.WorkflowKVStore{{Name: "counters-kv", Tables: []v1.KVTable{{Name: "t", Owner: "ingest"}}}},
			Steps: []v1.WorkflowStep{
				{Name: "ingest", Function: &v1.FunctionStep{Image: "oci:ingest-v1", KV: []v1.FunctionKV{{Alias: "c", Store: "counters-kv", Table: "t"}}}},
			},
		},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	obj, err := s.Get(ctx, v1.KindKVStore.GVK(), "default", "counters-kv")
	if err != nil {
		t.Fatalf("owned kvstore not created: %v", err)
	}
	kv := obj.(*v1.KVStore)
	kv.Status.Phase, kv.Status.Tables, kv.Status.Bindings = v1.PhaseReady, 1, 1
	if _, err := s.Update(ctx, kv); err != nil {
		t.Fatalf("write status: %v", err)
	}

	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	obj, err = s.Get(ctx, v1.KindKVStore.GVK(), "default", "counters-kv")
	if err != nil {
		t.Fatal(err)
	}
	if got := obj.(*v1.KVStore).Status; got.Phase != v1.PhaseReady || got.Tables != 1 || got.Bindings != 1 {
		t.Fatalf("a re-materialize wiped the kvstore status: phase=%q tables=%d bindings=%d", got.Phase, got.Tables, got.Bindings)
	}
}

// Issue 50: a minReplicas-0 step Function, shared or isolated, is reclaimed once idle — ADR-0094's
// materialized Functions scale to zero.
func TestIssue50_IdleStepFunctionScalesToZero(t *testing.T) {
	for _, mode := range []v1.PoolingMode{v1.PoolingShared, v1.PoolingIsolated} {
		t.Run(string(mode), func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			wf := &v1.Workflow{
				TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
				ObjectMeta: v1.ObjectMeta{Name: "wfz", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
				Spec: v1.WorkflowSpec{
					Pooling: v1.WorkflowPooling{Mode: mode},
					Steps:   []v1.WorkflowStep{{Name: "ingest", Function: &v1.FunctionStep{Image: "oci:ingest"}}},
				},
			}
			if err := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0).Materialize(ctx, wf); err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			obj, err := s.Get(ctx, v1.KindFunction.GVK(), "default", "wfz-ingest")
			if err != nil {
				t.Fatalf("owned function not created: %v", err)
			}
			scaling := obj.(*v1.Function).Spec.Scaling
			clk := &manualClock{t: time.Unix(1000, 0)}
			sc := &zeroScaler{}
			act, err := activator.New(activator.Deps{Store: s, Endpoints: fakeEndpoints{}, Scaler: sc, Clock: clk})
			if err != nil {
				t.Fatal(err)
			}
			if err := act.ReclaimIdle(ctx); err != nil { // seeds the grace window
				t.Fatalf("ReclaimIdle: %v", err)
			}
			clk.advance(scaling.IdleTimeout + time.Second)
			if err := act.ReclaimIdle(ctx); err != nil {
				t.Fatalf("ReclaimIdle: %v", err)
			}
			want := []activator.FunctionRef{{Namespace: "default", Name: "wfz-ingest"}}
			if got := sc.reclaimed(); !slices.Equal(got, want) {
				t.Fatalf("reclaimed %v, want %v: an idle minReplicas-0 step function must scale to zero (scaling %+v)", got, want, scaling)
			}
		})
	}
}

func controllerRefs(refs []v1.OwnerReference) int {
	n := 0
	for _, r := range refs {
		if r.Controller {
			n++
		}
	}
	return n
}

// #722: a Workflow moved to another ResourceGroup takes its step Functions and its retain KVStore along, so
// the store, which no controller owns, stops being a member of the old group (ADR-0170 Decision 7).
func TestIssue722_GroupMoveRestampsChildren(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: "default", ResourceGroup: "team", UID: "uid-w"},
		Spec: v1.WorkflowSpec{
			KV: []v1.WorkflowKVStore{{Name: "keep", Tables: []v1.KVTable{{Name: "t", Owner: "s1"}}}},
			Steps: []v1.WorkflowStep{{Name: "s1", Function: &v1.FunctionStep{
				Image: "oci:x", KV: []v1.FunctionKV{{Alias: "c", Store: "keep", Table: "t"}},
			}}},
		},
	}
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	wf.ResourceGroup = "other"
	if err := m.Materialize(ctx, wf); err != nil {
		t.Fatalf("re-materialize: %v", err)
	}
	fn, err := s.Get(ctx, v1.KindFunction.GVK(), "default", "w-s1")
	if err != nil {
		t.Fatalf("get function: %v", err)
	}
	kv, err := s.Get(ctx, v1.KindKVStore.GVK(), "default", "keep")
	if err != nil {
		t.Fatalf("get kvstore: %v", err)
	}
	if g := fn.GetObjectMeta().ResourceGroup; g != "other" {
		t.Errorf("step function group = %q, want other", g)
	}
	if g := kv.GetObjectMeta().ResourceGroup; g != "other" {
		t.Errorf("retain kvstore group = %q, want other (with no controller ref it stays a member of the old group)", g)
	}
	if b := fn.(*v1.Function).Spec.KV; len(b) != 1 || b[0].Store != "keep" {
		t.Errorf("kv binding after the move = %+v, want the keep binding", b)
	}
}
