package process_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
)

// scenario: transport-fd3 (process driver) — with a LogCapture hook set, Start passes the child a
// write pipe as fd 3 (FUNCD_LOG_FD=3) and hands the read end to the hook; a child writing to fd 3 is
// delivered to the hook. This is the host side of ADR-0081's Path B for the process driver.
func TestSetLogCaptureDeliversFd3(t *testing.T) {
	d := process.New()
	lc, ok := d.(runtime.LogCapturer)
	require.True(t, ok, "process driver implements runtime.LogCapturer")

	got := make(chan string, 1)
	lc.SetLogCapture(func(_ runtime.WorkerSpec, r io.ReadCloser) {
		go func() {
			b, _ := io.ReadAll(r)
			_ = r.Close()
			got <- string(b)
		}()
	})

	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "logger",
		Replica:   0,
		// write one NDJSON line to fd 3, then exit (closing fd 3 → the host reader sees EOF).
		Command: []string{"sh", "-c", `printf '%s\n' '{"ts":1,"sev":"INFO","body":"hi","funcd.source":"console"}' >&3`},
		LogPath: filepath.Join(t.TempDir(), "w.log"),
	}
	inst, err := d.Create(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, d.Start(context.Background(), inst.ID))
	t.Cleanup(func() { _ = d.Close() })

	select {
	case line := <-got:
		require.Contains(t, line, `"body":"hi"`)
		require.Contains(t, line, `"funcd.source":"console"`)
	case <-time.After(5 * time.Second):
		t.Fatal("fd 3 capture not delivered to the hook")
	}
}

// scenario: no-capture-when-unset — without a hook, Start passes no fd 3 and writing to fd 3 fails
// (the child has no inherited fd 3); the function still runs. We assert Start succeeds and no panic.
func TestNoCaptureWhenHookUnset(t *testing.T) {
	d := process.New()
	spec := runtime.WorkerSpec{
		Namespace: "default", OwnerKind: v1alpha1.KindFunction, Name: "quiet", Replica: 0,
		Command: []string{"sh", "-c", "exit 0"},
		LogPath: filepath.Join(t.TempDir(), "w.log"),
	}
	inst, err := d.Create(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, d.Start(context.Background(), inst.ID))
	t.Cleanup(func() { _ = d.Close() })
}
