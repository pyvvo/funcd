package badger

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/workflow/runstore"
)

// scenario: runstore-driver-conformance — the Badger driver in in-memory mode
// satisfies the port's shared contract (the same behavior a file backend gives).
func TestBadgerInMemoryContract(t *testing.T) {
	runstore.Contract(t, func(t *testing.T) runstore.Store {
		s, err := New(Config{InMemory: true})
		if err != nil {
			t.Fatalf("New(inMemory): %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// The file backend opens at a temp dir and satisfies the same contract (durability
// path), proving memory↔file parity.
func TestBadgerFileContract(t *testing.T) {
	runstore.Contract(t, func(t *testing.T) runstore.Store {
		s, err := New(Config{Dir: t.TempDir()})
		if err != nil {
			t.Fatalf("New(file): %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
