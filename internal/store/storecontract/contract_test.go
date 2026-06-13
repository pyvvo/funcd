package storecontract

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/store"
)

// scenario: driver-conformance-parity (ADR-0002)
// This skeleton compiles and is skipped until the real store port + drivers
// exist (store ADR). The real assertions will prove the in-memory driver
// behaves identically to the real one.
func TestScenario_DriverConformanceParity(t *testing.T) {
	t.Skip("store port not yet implemented — real assertions land with the store ADR")

	// The contract suite shape (to be filled in):
	_ = func(t *testing.T) store.Store {
		// return memory.New()
		return nil
	}
	// storecontract.RunContract(t, newStore)
}
