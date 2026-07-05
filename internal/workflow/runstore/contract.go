package runstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Contract exercises the runstore.Store port against a driver. Every driver's test
// runs it (ADR-0002 shared contract suite), so memory and badger are held to one
// behavior: roundtrip, not-found, overwrite, delete, no-aliasing, and List filters.
func Contract(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("get-missing-is-not-found", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Get(ctx, "ns", "absent")
		if fault.KindOf(err) != fault.NotFound {
			t.Fatalf("want NotFound, got %v", err)
		}
	})

	t.Run("put-get-roundtrip", func(t *testing.T) {
		s := newStore(t)
		rec := &Record{
			Namespace: "ns", Name: "run-1", Workflow: "wf", Phase: "Running",
			Input: json.RawMessage(`{"day":"2026-07-05"}`),
			Steps: []StepState{{Name: "a", Phase: v1.StepSucceeded, Attempts: 1, Revision: "sha256:x"}},
		}
		if err := s.Put(ctx, rec); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.Get(ctx, "ns", "run-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Workflow != "wf" || got.Phase != "Running" || len(got.Steps) != 1 || got.Steps[0].Phase != v1.StepSucceeded {
			t.Fatalf("roundtrip mismatch: %+v", got)
		}
	})

	t.Run("no-aliasing", func(t *testing.T) {
		s := newStore(t)
		rec := &Record{Namespace: "ns", Name: "run-2", Phase: "Running", Steps: []StepState{{Name: "a", Phase: v1.StepPending}}}
		if err := s.Put(ctx, rec); err != nil {
			t.Fatal(err)
		}
		rec.Steps[0].Phase = v1.StepFailed // mutate caller copy after Put
		got, _ := s.Get(ctx, "ns", "run-2")
		if got.Steps[0].Phase != v1.StepPending {
			t.Fatal("store must not alias caller memory (Put)")
		}
		got.Steps[0].Phase = v1.StepFailed // mutate returned copy
		again, _ := s.Get(ctx, "ns", "run-2")
		if again.Steps[0].Phase != v1.StepPending {
			t.Fatal("store must not alias its own memory (Get)")
		}
	})

	t.Run("overwrite-and-delete", func(t *testing.T) {
		s := newStore(t)
		_ = s.Put(ctx, &Record{Namespace: "ns", Name: "r", Phase: "Running"})
		_ = s.Put(ctx, &Record{Namespace: "ns", Name: "r", Phase: "Succeeded"})
		got, _ := s.Get(ctx, "ns", "r")
		if got.Phase != "Succeeded" {
			t.Fatalf("overwrite failed: %s", got.Phase)
		}
		if err := s.Delete(ctx, "ns", "r"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "ns", "r"); fault.KindOf(err) != fault.NotFound {
			t.Fatal("record should be gone after Delete")
		}
		if err := s.Delete(ctx, "ns", "absent"); err != nil {
			t.Fatalf("deleting absent must not error: %v", err)
		}
	})

	t.Run("list-filters", func(t *testing.T) {
		s := newStore(t)
		_ = s.Put(ctx, &Record{Namespace: "ns", Name: "a1", Workflow: "wf-a", Phase: "Running"})
		_ = s.Put(ctx, &Record{Namespace: "ns", Name: "a2", Workflow: "wf-a", Phase: "Succeeded"})
		_ = s.Put(ctx, &Record{Namespace: "ns", Name: "b1", Workflow: "wf-b", Phase: "Running"})
		_ = s.Put(ctx, &Record{Namespace: "other", Name: "c1", Workflow: "wf-a", Phase: "Running"})

		byWf, _ := s.List(ctx, ListOptions{Namespace: "ns", Workflow: "wf-a"})
		if len(byWf) != 2 {
			t.Fatalf("workflow filter: want 2, got %d", len(byWf))
		}
		open, _ := s.List(ctx, ListOptions{Namespace: "ns", OpenOnly: true})
		if len(open) != 2 { // a1 + b1 running; a2 terminal
			t.Fatalf("open filter: want 2, got %d", len(open))
		}
		nsAll, _ := s.List(ctx, ListOptions{Namespace: "ns"})
		if len(nsAll) != 3 {
			t.Fatalf("namespace filter: want 3, got %d", len(nsAll))
		}
	})
}
