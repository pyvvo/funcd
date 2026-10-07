package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

func openSaving(t *testing.T) (*driver, string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "process")
	rt, err := Open(context.Background(), state, time.Second, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	return rt.(*driver), state
}

func saved(t *testing.T, state string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "workers.json"))
	require.NoError(t, err)
	var all []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &all))
	return all
}

// A worker that exits before Start reads its start time is saved and its exit recorded: Start must read the start
// time before the wait goroutine can reap the worker, or Start fails with a NotFound "save worker" error.
func TestStartSavesAWorkerThatExitsAtOnce(t *testing.T) {
	ctx := context.Background()
	d, state := openSaving(t)
	d.startTime = func(pid int) (uint64, error) {
		// An unreaped worker keeps its pid, so after the bound the read goes ahead.
		for deadline := time.Now().Add(time.Second); syscall.Kill(pid, 0) == nil && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		return procreg.StartTime(pid)
	}
	inst, err := d.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", Name: "brief", OwnerKind: v1alpha1.KindFunction, Command: []string{"sh", "-c", "exit 3"},
	})
	require.NoError(t, err)
	require.NoError(t, d.Start(ctx, inst.ID))
	require.Eventually(t, func() bool {
		got, serr := d.Status(ctx, inst.ID)
		return serr == nil && got.State == runtime.StateFailed
	}, 10*time.Second, 10*time.Millisecond, "the worker's exit is recorded")
	got, err := d.Status(ctx, inst.ID)
	require.NoError(t, err)
	require.Equal(t, runtime.Exit{Cause: runtime.ExitByCode, Code: 3}, got.Exit)
	require.Empty(t, saved(t, state), "the exited worker is deleted from the registry")
}

// Start saves the file the driver owns with the worker, so the reap after a crash deletes it (issue #44): the port
// file, the only file a worker has once its output goes through pipes (ADR-0168).
func TestStartSavesTheDriverOwnedFiles(t *testing.T) {
	ctx := context.Background()
	d, state := openSaving(t)
	inst, err := d.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", Name: "files", OwnerKind: v1alpha1.KindFunction,
		Command: []string{"sh", "-c", "exec sleep 30"},
	})
	require.NoError(t, err)
	require.NoError(t, d.Start(ctx, inst.ID))
	d.mu.Lock()
	want := []string{d.instances[inst.ID].portFile}
	d.mu.Unlock()
	require.Regexp(t, `funcd-worker-.*\.port$`, want[0])
	entries := saved(t, state)
	require.Len(t, entries, 1)
	require.Equal(t, want, entries[0].Files)
}
