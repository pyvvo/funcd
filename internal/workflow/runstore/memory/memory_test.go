package memory

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/workflow/runstore"
)

// scenario: runstore-driver-conformance — the memory driver satisfies the port's
// shared contract (the same suite badger runs).
func TestMemoryRunStoreContract(t *testing.T) {
	runstore.Contract(t, func(t *testing.T) runstore.Store { return New() })
}
