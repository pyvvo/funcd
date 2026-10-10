package memory_test

import (
	"testing"

	"github.com/pyvvo/funcd/internal/snapshot/snapshotcontract"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/store/storecontract"
)

// scenario: driver-conformance-parity (memory side) — the pure-Go in-memory
// engine passes the identical store contract as the real (badger) engine.
func TestScenario_DriverConformanceParity(t *testing.T) {
	storecontract.RunContract(t, func(t *testing.T) store.Store {
		return store.New(memory.New())
	})
}

// scenario: snapshot-is-one-read (metastore, memory engine) — the snapshot contract: a writer commits a=i, then
// b=i, while 200 snapshots run, and none holds b above a; the snapshot loads back into an empty engine.
func TestScenarioSnapshotIsOneRead(t *testing.T) {
	t.Parallel()
	snapshotcontract.Run(t, func(t *testing.T) snapshotcontract.Subject {
		return storecontract.SnapshotSubject(t, memory.New())
	})
}
