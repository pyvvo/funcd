package memory_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/internal/store/storecontract"
)

// scenario: driver-conformance-parity (memory side) — the pure-Go in-memory
// engine passes the identical store contract as the real (slatedb) engine.
func TestScenario_DriverConformanceParity(t *testing.T) {
	storecontract.RunContract(t, func(t *testing.T) store.Store {
		return store.New(memory.New())
	})
}
