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
			OwnerKind: v1alpha1.KindFunction,
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

	// ADR-0142: a stopped replica can be created again under the same ID (the Function reconciler replaces a
	// dead replica with Stop → Create → Start).
	t.Run("worker-recreate-after-stop", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		first, err := rt.Create(ctx, specOf(t, "recreate", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, first.ID))
		require.NoError(t, rt.Stop(ctx, first.ID))

		time.Sleep(10 * time.Millisecond)
		second, err := rt.Create(ctx, specOf(t, "recreate", []string{"sleep", "30"}))
		require.NoError(t, err, "an exited instance's ID can be created again")
		require.Equal(t, first.ID, second.ID)
		require.True(t, second.CreatedAt.After(first.CreatedAt), "the new instance has a new CreatedAt")
		require.NoError(t, rt.Start(ctx, second.ID))
		got, err := rt.Status(ctx, second.ID)
		require.NoError(t, err)
		require.Equal(t, runtime.StateRunning, got.State)
		require.NoError(t, rt.Stop(ctx, second.ID))
	})

	// ADR-0142: a worker that exits with a non-zero status is Failed on every driver.
	t.Run("worker-exit-nonzero-failed", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "exit3", []string{"sh", "-c", "exit 3"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))
		waitState(t, rt, inst.ID, runtime.StateFailed)

		require.NoError(t, rt.Stop(ctx, inst.ID))
		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, runtime.StateStopped, got.State, "after Stop an exited instance is Stopped")
	})

	// ADR-0143: a worker records the Revision it was created from.
	t.Run("worker-revision-round-trips", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		spec := specOf(t, "rev", []string{"sleep", "30"})
		spec.Revision = "rev-2"
		inst, err := rt.Create(ctx, spec)
		require.NoError(t, err)
		require.Equal(t, runtime.NewInstanceID("default", "rev", "rev-2", 0), inst.ID)
		require.Equal(t, v1alpha1.ObjectName("rev-2"), inst.Revision)
		require.NoError(t, rt.Start(ctx, inst.ID))

		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.ObjectName("rev-2"), got.Revision)
		list, err := rt.List(ctx, "default")
		require.NoError(t, err)
		found := false
		for _, in := range list {
			if in.ID == inst.ID {
				found = true
				require.Equal(t, v1alpha1.ObjectName("rev-2"), in.Revision)
			}
		}
		require.True(t, found, "List reports the instance")
		require.NoError(t, rt.Stop(ctx, inst.ID))
	})

	// ADR-0143: two revisions of one replica run at once, under distinct IDs.
	t.Run("two-revisions-of-a-replica-coexist", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		var ids []runtime.InstanceID
		for _, rev := range []v1alpha1.ObjectName{"pair-1", "pair-2"} {
			spec := specOf(t, "pair", []string{"sleep", "30"})
			spec.Revision = rev
			inst, err := rt.Create(ctx, spec)
			require.NoError(t, err)
			require.NoError(t, rt.Start(ctx, inst.ID))
			ids = append(ids, inst.ID)
		}
		require.NotEqual(t, ids[0], ids[1])
		for _, id := range ids {
			got, err := rt.Status(ctx, id)
			require.NoError(t, err)
			require.Equal(t, runtime.StateRunning, got.State, "both revisions run")
			require.NoError(t, rt.Stop(ctx, id))
		}
	})

	// ADR-0143: Remove forgets an instance once Stop has released it, and only then.
	t.Run("worker-remove-after-stop", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "remove", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.Equal(t, fault.Conflict, fault.KindOf(rt.Remove(ctx, inst.ID)), "a created instance is not removed")
		require.NoError(t, rt.Start(ctx, inst.ID))
		require.Equal(t, fault.Conflict, fault.KindOf(rt.Remove(ctx, inst.ID)), "a running instance is not removed")

		require.NoError(t, rt.Stop(ctx, inst.ID))
		require.NoError(t, rt.Remove(ctx, inst.ID))
		_, err = rt.Status(ctx, inst.ID)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "a removed instance is unknown")
		list, err := rt.List(ctx, "default")
		require.NoError(t, err)
		for _, in := range list {
			require.NotEqual(t, inst.ID, in.ID, "List omits a removed instance")
		}
		require.NoError(t, rt.Remove(ctx, inst.ID), "removing an unknown instance is a no-op")

		exited, err := rt.Create(ctx, specOf(t, "exited", []string{"sh", "-c", "exit 0"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, exited.ID))
		waitState(t, rt, exited.ID, runtime.StateStopped)
		require.Equal(t, fault.Conflict, fault.KindOf(rt.Remove(ctx, exited.ID)), "an instance that exited without a Stop is kept")
		require.NoError(t, rt.Stop(ctx, exited.ID))
		require.NoError(t, rt.Remove(ctx, exited.ID))
	})

	// ADR-0143: an instance started again after Stop runs, so Remove waits for its next Stop. A driver that cannot
	// restart a stopped instance fails that Start instead, and the instance stays released.
	t.Run("worker-remove-after-restart", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "restart", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))
		require.NoError(t, rt.Stop(ctx, inst.ID))
		if err := rt.Start(ctx, inst.ID); err != nil {
			require.NoError(t, rt.Remove(ctx, inst.ID), "an instance that did not start again stays released")
			return
		}
		require.Equal(t, fault.Conflict, fault.KindOf(rt.Remove(ctx, inst.ID)), "a restarted instance is running")
		require.NoError(t, rt.Stop(ctx, inst.ID))
		require.NoError(t, rt.Remove(ctx, inst.ID))
	})

	// ADR-0152: a worker reports the kind that created it from Create, Status and List.
	t.Run("worker-owner-kind-round-trips", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		spec := specOf(t, "owner", []string{"sleep", "30"})
		spec.OwnerKind = v1alpha1.KindCatalogService
		inst, err := rt.Create(ctx, spec)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.KindCatalogService, inst.OwnerKind)
		require.NoError(t, rt.Start(ctx, inst.ID))

		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.KindCatalogService, got.OwnerKind)
		list, err := rt.List(ctx, "default")
		require.NoError(t, err)
		found := false
		for _, in := range list {
			if in.ID == inst.ID {
				found = true
				require.Equal(t, v1alpha1.KindCatalogService, in.OwnerKind)
			}
		}
		require.True(t, found, "List reports the instance")
		require.NoError(t, rt.Stop(ctx, inst.ID))
	})

	// ADR-0152: every creator names the kind it creates a worker for.
	t.Run("worker-owner-kind-required", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		spec := specOf(t, "ownerless", []string{"sleep", "30"})
		spec.OwnerKind = ""
		_, err := rt.Create(ctx, spec)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		_, err = rt.Status(ctx, runtime.NewInstanceID("default", "ownerless", "", 0))
		require.Equal(t, fault.NotFound, fault.KindOf(err), "a refused Create keeps no instance")
	})

	// ADR-0152: an ID held by another kind's worker, live or exited, is never replaced; the same kind still replaces
	// its own exited worker (ADR-0142).
	t.Run("worker-id-of-another-kind-conflicts", func(t *testing.T) {
		ctx := context.Background()
		rt := newRuntime(t)
		t.Cleanup(func() { _ = rt.Close() })

		inst, err := rt.Create(ctx, specOf(t, "shared", []string{"sleep", "30"}))
		require.NoError(t, err)
		require.NoError(t, rt.Start(ctx, inst.ID))
		other := specOf(t, "shared", []string{"sleep", "30"})
		other.OwnerKind = v1alpha1.KindCatalogService
		_, err = rt.Create(ctx, other)
		require.Equal(t, fault.Conflict, fault.KindOf(err), "a live worker of another kind holds the ID")
		got, err := rt.Status(ctx, inst.ID)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.KindFunction, got.OwnerKind)
		require.Equal(t, runtime.StateRunning, got.State, "the refused Create leaves the worker running")

		require.NoError(t, rt.Stop(ctx, inst.ID))
		_, err = rt.Create(ctx, other)
		require.Equal(t, fault.Conflict, fault.KindOf(err), "an exited worker of another kind still holds the ID")
		again, err := rt.Create(ctx, specOf(t, "shared", []string{"sleep", "30"}))
		require.NoError(t, err, "the same kind replaces its exited worker")
		require.Equal(t, inst.ID, again.ID)
		require.Equal(t, v1alpha1.KindFunction, again.OwnerKind)
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
