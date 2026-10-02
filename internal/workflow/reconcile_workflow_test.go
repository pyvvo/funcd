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
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil)
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
	if len(kv.OwnerReferences) != 1 {
		t.Fatal("kvstore missing owner ref for cascade")
	}
}

// scenario core: deletion policy differentiation — a `retain` owned store carries NO cascading
// owner reference (it outlives the workflow); a `delete` one does (it cascades).
func TestMaterializeDeletionPolicy(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil)
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
	if refs := keep.(*v1.KVStore).OwnerReferences; len(refs) != 0 {
		t.Fatalf("retain store must have NO owner ref (outlives the workflow), got %d", len(refs))
	}
	drop, _ := s.Get(ctx, v1.KindKVStore.GVK(), "default", "drop-kv")
	if refs := drop.(*v1.KVStore).OwnerReferences; len(refs) != 1 {
		t.Fatalf("delete store must cascade via one owner ref, got %d", len(refs))
	}
}

// materialize is idempotent (re-running updates, does not error on existing).
func TestMaterializeIdempotent(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "python314"}, nil)
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
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil)
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
			if err := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil).Materialize(ctx, wf); err != nil {
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
