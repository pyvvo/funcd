package memory_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/eventing/deadletter"
	"github.com/green-0-rabbit/funcd/internal/eventing/deadletter/memory"
)

// TestMemoryContract runs the shared deadletter.Store contract against the in-memory driver, so the fake is
// provably equivalent to the Badger driver (ADR-0002 contract-suite parity).
func TestMemoryContract(t *testing.T) {
	deadletter.Contract(t, func(t *testing.T) deadletter.Store {
		t.Helper()
		return memory.New()
	})
}
