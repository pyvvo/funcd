// Package runtimecontract is the shared conformance suite for the runtime.Runtime
// port (ADR-0011). RunContract executes the same observable assertions against any
// driver: the process driver runs it in `just ci`; the containerd driver runs it on
// the Linux integration lane. The suite drives a worker through its lifecycle with
// platform-internal commands (sh/sleep/true/false) available on any POSIX host.
package runtimecontract

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// defaultImage is the runtime image the containerd lane runs the Command in; the
// process driver ignores it.
const defaultImage = "docker.io/library/busybox:1.36"

// RunContract runs every scenario assertion against the driver from newRuntime.
func RunContract(t *testing.T, newRuntime func(t *testing.T) runtime.Runtime) {
	t.Helper()

	specOf := func(t *testing.T, name string, command []string) runtime.WorkerSpec {
		t.Helper()
		return runtime.WorkerSpec{
			Namespace: "default",
			Name:      v1alpha1.ObjectName(name),
			Replica:   0,
			Image:     defaultImage,
			Command:   command,
			LogPath:   filepath.Join(t.TempDir(), "worker.log"),
		}
	}

	t.Run("worker-create-start-running", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "run", []string{"sleep", "5"}))
		require.NoError(t, err)
		require.Equal(t, runtime.StateCreated, inst.State)

		require.NoError(t, rt.Start(ctx, inst.ID))
		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, runtime.StateRunning, got.State)
		require.Positive(t, got.PID)

		require.NoError(t, rt.Stop(ctx, inst.ID))
		got, err = rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, runtime.StateStopped, got.State)
	})

	t.Run("worker-logs-captured", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "logs", []string{"sh", "-c", "echo runtimecontract-hello"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))
		waitState(t, rt, inst.ID, runtime.StateStopped)

		rc, err := rt.Logs(ctx, inst.ID)
		require.NoError(t, err)
		t.Cleanup(func() { _ = rc.Close() })
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.Contains(t, string(data), "runtimecontract-hello")
	})

	t.Run("worker-stop-idempotent", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "stop", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))

		require.NoError(t, rt.Stop(ctx, inst.ID))
		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, runtime.StateStopped, got.State)

		require.NoError(t, rt.Stop(ctx, inst.ID), "second Stop is idempotent")
	})

	t.Run("instance-not-found", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		_, err := rt.Status(ctx, runtime.InstanceID("nope"))
		require.Equal(t, fault.NotFound, fault.KindOf(err))
		require.Equal(t, fault.NotFound, fault.KindOf(rt.Stop(ctx, runtime.InstanceID("nope"))))
	})

	t.Run("worker-exec", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "exec", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))

		require.NoError(t, rt.Exec(ctx, inst.ID, []string{"true"}))
		require.Error(t, rt.Exec(ctx, inst.ID, []string{"false"}))
		require.Equal(t, fault.NotFound, fault.KindOf(rt.Exec(ctx, runtime.InstanceID("nope"), []string{"true"})))

		require.NoError(t, rt.Stop(ctx, inst.ID))
	})
}

// waitState polls Status until the instance reaches want or the deadline passes.
func waitState(t *testing.T, rt runtime.Runtime, id runtime.InstanceID, want runtime.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rt.Status(context.Background(), id)
		require.NoError(t, err)
		if got.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("instance %q did not reach state %q within deadline", id, want)
}
