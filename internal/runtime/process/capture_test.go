package process_test

import (
	"context"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
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
	}
	inst, err := d.Create(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, d.Start(context.Background(), inst.ID))
	t.Cleanup(func() { _ = d.Close() })
}

// A Start whose cmd.Start fails has already handed the run's output to the hook: the Pumps reading it must still end,
// or each failed start leaks two readers (ADR-0168).
func TestFailedStartLeavesNoPump(t *testing.T) {
	d := process.New()
	t.Cleanup(func() { _ = d.Close() })
	var outs []*workerpipe.Output
	pumps := make(chan struct{}, 2)
	d.(runtime.OutputCapturer).SetOutputCapture(func(_ runtime.WorkerSpec, out *workerpipe.Output) {
		outs = append(outs, out)
		for _, s := range []workerpipe.Stream{workerpipe.Stdout, workerpipe.Stderr} {
			r := out.Reader(s)
			go func() {
				_, _ = io.Copy(io.Discard, r)
				pumps <- struct{}{}
			}()
		}
	})

	inst, err := d.Create(context.Background(), runtime.WorkerSpec{
		Namespace: "default", OwnerKind: v1alpha1.KindFunction, Name: "nostart",
		Command: []string{"funcd-no-such-worker-binary"},
	})
	require.NoError(t, err)
	require.Error(t, d.Start(context.Background(), inst.ID))
	require.Len(t, outs, 1, "the hook runs once, before cmd.Start")
	for range 2 {
		select {
		case <-pumps:
		case <-time.After(5 * time.Second):
			t.Fatal("a Pump still reads the output of a run that never started")
		}
	}
	select {
	case <-outs[0].Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the output of a failed start is not closed")
	}
}

// The terminal state waits for the run's last output, so Logs read as soon as the state is terminal ends with the line a
// load error ends with (ADR-0168). A Drain the hook holds back past the worker's exit stands in for a slow one.
func TestTerminalStateWaitsForTheLastOutput(t *testing.T) {
	d := process.New()
	t.Cleanup(func() { _ = d.Close() })
	held, release := io.Pipe()
	d.(runtime.OutputCapturer).SetOutputCapture(func(_ runtime.WorkerSpec, out *workerpipe.Output) {
		out.Drain(workerpipe.Stderr, held)
	})

	ctx := context.Background()
	inst, err := d.Create(ctx, runtime.WorkerSpec{
		Namespace: "default", OwnerKind: v1alpha1.KindFunction, Name: "loaderr",
		Command: []string{"sh", "-c", "echo loading >&2; exit 1"},
	})
	require.NoError(t, err)
	require.NoError(t, d.Start(ctx, inst.ID))
	inst, err = d.Status(ctx, inst.ID)
	require.NoError(t, err)
	go func() {
		for syscall.Kill(inst.PID, 0) == nil {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = release.Write([]byte("load error\n"))
		_ = release.Close()
	}()

	require.Eventually(t, func() bool {
		st, serr := d.Status(ctx, inst.ID)
		return serr == nil && st.State.Terminal()
	}, 5*time.Second, time.Millisecond)
	logs, err := d.Logs(ctx, inst.ID)
	require.NoError(t, err)
	b, err := io.ReadAll(logs)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(b), "load error\n"), "Logs at the terminal state: %q", b)
}
