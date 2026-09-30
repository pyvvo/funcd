package process_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
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

// Create keeps rejecting an ID whose instance is live (Created or Running); only an exited one is replaced
// (ADR-0142).
func TestCreateRejectsLiveInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	spec := runtime.WorkerSpec{Namespace: "default", Name: "live", Command: []string{"sleep", "30"}, LogPath: filepath.Join(t.TempDir(), "w.log")}

	inst, err := rt.Create(ctx, spec)
	require.NoError(t, err)
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a Created instance is live")

	require.NoError(t, rt.Start(ctx, inst.ID))
	_, err = rt.Create(ctx, spec)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a Running instance is live")
	require.NoError(t, rt.Stop(ctx, inst.ID))
}
