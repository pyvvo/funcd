package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// TestIssue1317_StatusWriteOnStoredInvalidObject writes a Function past admission, as a restore does, then
// writes its status: the status write must land, because only a spec change is validated.
func TestIssue1317_StatusWriteOnStoredInvalidObject(t *testing.T) {
	ctx := context.Background()
	eng := newFakeEngine()
	s := New(eng)

	in, _ := v1.NewObject(v1.KindFunction)
	fn := in.(*v1.Function)
	fn.Name = "f1"
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	created, err := s.Create(ctx, fn)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gvk := created.GroupVersionKind()

	created.(*v1.Function).Spec.Timeout = v1.Duration(v1.MaxInvokeTimeout + time.Second)
	raw, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := eng.Update(ctx, func(tx Txn) error { return tx.Put(gvk.String(), keyFor("default", "f1"), raw) }); err != nil {
		t.Fatalf("restore write: %v", err)
	}

	stored, err := s.Get(ctx, gvk, "default", "f1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored.(*v1.Function).Status.Phase = "Failed"
	if _, err := s.Update(ctx, stored); err != nil {
		t.Fatalf("status write on a stored object that fails Validate: %v", err)
	}

	got, err := s.Get(ctx, gvk, "default", "f1")
	if err != nil {
		t.Fatalf("Get after status write: %v", err)
	}
	if phase := got.(*v1.Function).Status.Phase; phase != "Failed" {
		t.Fatalf("stored status phase = %q, want %q: the status write was dropped", phase, "Failed")
	}
}
