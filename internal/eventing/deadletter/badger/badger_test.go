package badger_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/eventing/deadletter/badger"
)

// TestBadgerContract runs the shared deadletter.Store contract against the Badger driver in in-memory mode
// (hermetic — no files): put/get/list/delete + the retention sweep (TTL + per-ns cap).
func TestBadgerContract(t *testing.T) {
	deadletter.Contract(t, func(t *testing.T) deadletter.Store {
		t.Helper()
		s, err := badger.New(badger.Config{InMemory: true})
		if err != nil {
			t.Fatalf("open badger DLQ: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// TestIssue101_SweepEvictsPastTxnLimit: on the on-disk profile, one Badger txn holds about 26.5k deletes.
// A sweep over a larger backlog still evicts every past-TTL and over-cap entry.
func TestIssue101_SweepEvictsPastTxnLimit(t *testing.T) {
	const n = 30000
	for _, tc := range []struct {
		name      string
		age       time.Duration
		retention time.Duration
		maxPerNS  int
		left      int
	}{
		{name: "ttl", age: 2 * time.Hour, retention: time.Hour},
		{name: "cap", maxPerNS: 1000, left: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := badger.New(badger.Config{Dir: t.TempDir()})
			if err != nil {
				t.Fatalf("open badger DLQ: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ctx := context.Background()
			failedAt := time.Now().Add(-tc.age)
			for i := range n {
				if err := s.Put(ctx, deadletter.DeadLetter{ID: fmt.Sprintf("%08d", i), Namespace: "default", FailedAt: v1.NewTimestamp(failedAt)}); err != nil {
					t.Fatalf("Put %d: %v", i, err)
				}
			}

			evicted, err := s.SweepExpired(ctx, tc.retention, tc.maxPerNS)
			if err != nil {
				t.Fatalf("SweepExpired: %v", err)
			}
			if evicted != n-tc.left {
				t.Fatalf("evicted %d, want %d", evicted, n-tc.left)
			}
			left, err := s.List(ctx, "default")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(left) != tc.left {
				t.Fatalf("%d entries left, want %d", len(left), tc.left)
			}
			if tc.left > 0 && (left[0].ID != fmt.Sprintf("%08d", n-1) || left[tc.left-1].ID != fmt.Sprintf("%08d", n-tc.left)) {
				t.Fatalf("survivors %s..%s are not the newest %d", left[tc.left-1].ID, left[0].ID, tc.left)
			}
		})
	}
}
