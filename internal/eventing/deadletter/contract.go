package deadletter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Contract exercises the deadletter.Store port against a driver. Every driver's test runs it (ADR-0002
// shared contract suite), so badger and memory are held to one behavior: roundtrip, not-found, list
// newest-first + namespace scope, delete (idempotent), no-aliasing, and the retention sweep (TTL + per-ns
// cap). It is the `retention-evicts`, `discard-removes` and `driver-independent` store-level evidence.
func Contract(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	mk := func(ns v1.NamespaceName, id string, at time.Time) DeadLetter {
		return DeadLetter{
			ID: id, Namespace: ns, Sensor: "s", Source: "git", Event: "push", Action: "build",
			Payload: json.RawMessage(`{"specversion":"1.0","id":"e1"}`), Attempts: 3, Reason: "boom", FailedAt: at,
		}
	}

	t.Run("get-missing-is-not-found", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get(ctx, "ns", "absent"); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("want NotFound, got %v", err)
		}
	})

	t.Run("put-get-roundtrip", func(t *testing.T) {
		s := newStore(t)
		dl := mk("ns", "01AAA", time.Unix(100, 0).UTC())
		if err := s.Put(ctx, dl); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.Get(ctx, "ns", "01AAA")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Sensor != "s" || got.Action != "build" || got.Attempts != 3 || got.Reason != "boom" {
			t.Fatalf("roundtrip mismatch: %+v", got)
		}
		if string(got.Payload) != `{"specversion":"1.0","id":"e1"}` {
			t.Fatalf("payload not preserved: %s", got.Payload)
		}
	})

	t.Run("no-aliasing", func(t *testing.T) {
		s := newStore(t)
		dl := mk("ns", "01BBB", time.Unix(1, 0).UTC())
		if err := s.Put(ctx, dl); err != nil {
			t.Fatal(err)
		}
		dl.Payload[0] = 'X' // mutate caller copy after Put
		got, _ := s.Get(ctx, "ns", "01BBB")
		if string(got.Payload) != `{"specversion":"1.0","id":"e1"}` {
			t.Fatal("store must not alias caller memory (Put)")
		}
	})

	t.Run("list-newest-first-and-namespace-scoped", func(t *testing.T) {
		s := newStore(t)
		_ = s.Put(ctx, mk("ns", "01A", time.Unix(1, 0).UTC()))
		_ = s.Put(ctx, mk("ns", "01C", time.Unix(3, 0).UTC()))
		_ = s.Put(ctx, mk("ns", "01B", time.Unix(2, 0).UTC()))
		_ = s.Put(ctx, mk("other", "01Z", time.Unix(9, 0).UTC()))

		list, err := s.List(ctx, "ns")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("namespace scope: want 3, got %d", len(list))
		}
		if list[0].ID != "01C" || list[1].ID != "01B" || list[2].ID != "01A" {
			t.Fatalf("want newest-first (01C,01B,01A), got %s,%s,%s", list[0].ID, list[1].ID, list[2].ID)
		}
	})

	t.Run("delete-is-idempotent", func(t *testing.T) {
		s := newStore(t)
		_ = s.Put(ctx, mk("ns", "01D", time.Unix(1, 0).UTC()))
		if err := s.Delete(ctx, "ns", "01D"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "ns", "01D"); fault.KindOf(err) != fault.NotFound {
			t.Fatal("record should be gone after Delete")
		}
		if err := s.Delete(ctx, "ns", "absent"); err != nil {
			t.Fatalf("deleting absent must not error: %v", err)
		}
	})

	t.Run("retention-evicts-by-ttl", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		_ = s.Put(ctx, mk("ns", "01OLD", now.Add(-2*time.Hour)))
		_ = s.Put(ctx, mk("ns", "01NEW", now.Add(-1*time.Minute)))
		n, err := s.SweepExpired(ctx, time.Hour, 0) // TTL 1h, no cap
		if err != nil {
			t.Fatalf("SweepExpired: %v", err)
		}
		if n != 1 {
			t.Fatalf("TTL sweep: want 1 evicted, got %d", n)
		}
		if _, err := s.Get(ctx, "ns", "01OLD"); fault.KindOf(err) != fault.NotFound {
			t.Fatal("the past-TTL entry must be evicted")
		}
		if _, err := s.Get(ctx, "ns", "01NEW"); err != nil {
			t.Fatal("the within-TTL entry must remain")
		}
	})

	t.Run("retention-evicts-by-per-namespace-cap", func(t *testing.T) {
		s := newStore(t)
		now := time.Now().UTC()
		// 3 in ns (cap 2 ⇒ evict the oldest 1), 1 in other (under cap ⇒ keep).
		_ = s.Put(ctx, mk("ns", "01A", now.Add(-3*time.Minute)))
		_ = s.Put(ctx, mk("ns", "01B", now.Add(-2*time.Minute)))
		_ = s.Put(ctx, mk("ns", "01C", now.Add(-1*time.Minute)))
		_ = s.Put(ctx, mk("other", "01A", now.Add(-1*time.Minute)))
		n, err := s.SweepExpired(ctx, 0, 2) // no TTL, cap 2 per ns
		if err != nil {
			t.Fatalf("SweepExpired: %v", err)
		}
		if n != 1 {
			t.Fatalf("cap sweep: want 1 evicted, got %d", n)
		}
		if _, err := s.Get(ctx, "ns", "01A"); fault.KindOf(err) != fault.NotFound {
			t.Fatal("the oldest over-cap entry must be evicted")
		}
		nsList, _ := s.List(ctx, "ns")
		if len(nsList) != 2 {
			t.Fatalf("ns should have 2 after cap sweep, got %d", len(nsList))
		}
		otherList, _ := s.List(ctx, "other")
		if len(otherList) != 1 {
			t.Fatalf("other (under cap) must be untouched, got %d", len(otherList))
		}
	})

	t.Run("sweep-disabled-evicts-nothing", func(t *testing.T) {
		s := newStore(t)
		_ = s.Put(ctx, mk("ns", "01A", time.Unix(1, 0).UTC()))
		n, err := s.SweepExpired(ctx, 0, 0) // both disabled
		if err != nil {
			t.Fatalf("SweepExpired: %v", err)
		}
		if n != 0 {
			t.Fatalf("disabled sweep must evict nothing, got %d", n)
		}
	})
}
