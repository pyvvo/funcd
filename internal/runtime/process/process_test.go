package process_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/runtime/runtimecontract"
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
