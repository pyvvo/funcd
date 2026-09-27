package process_test

import (
	"testing"

	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/runtimecontract"
)

// scenario: driver-conformance-parity (and the lifecycle/logs/stop/not-found/exec
// scenarios it drives) — the process driver satisfies the shared runtime contract.
func TestProcessDriverContract(t *testing.T) {
	t.Parallel()
	runtimecontract.RunContract(t, func(t *testing.T) runtime.Runtime {
		t.Helper()
		return process.New()
	})
}
