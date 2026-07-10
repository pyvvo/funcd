package badger_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/eventing/deadletter"
	"github.com/green-0-rabbit/funcd/internal/eventing/deadletter/badger"
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
