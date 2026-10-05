//go:build linux

package containerd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

func TestExitOf(t *testing.T) {
	stopped := func(status uint32) containerd.Status {
		return containerd.Status{Status: containerd.Stopped, ExitStatus: status}
	}
	code := func(c int) runtime.Exit { return runtime.Exit{Cause: runtime.ExitByCode, Code: c} }
	signal := func(s int) runtime.Exit { return runtime.Exit{Cause: runtime.ExitBySignal, Signal: s} }
	cases := []struct {
		st   containerd.Status
		want runtime.Exit
	}{
		{stopped(0), code(0)},
		{stopped(3), code(3)},
		{stopped(128), code(128)},
		{stopped(129), signal(1)},
		{stopped(137), signal(9)},
		{stopped(192), signal(64)},
		{stopped(193), code(193)},
		{stopped(255), runtime.Exit{}},
		{containerd.Status{Status: containerd.Unknown}, runtime.Exit{}},
		{containerd.Status{Status: containerd.Running}, runtime.Exit{}},
	}
	for _, c := range cases {
		require.Equal(t, c.want, exitOf(c.st), "status %s, exit %d", c.st.Status, c.st.ExitStatus)
	}
}

// scenario: hostile-port-file-is-not-listened (ADR-0160) — a symlink to /dev/zero or a FIFO at the port path is not
// Listened, and reading it does not hang: the driver only calls os.Lstat on what the sandbox writes.
func TestScenarioHostilePortFileIsNotListened(t *testing.T) {
	dir := t.TempDir()
	port := filepath.Join(dir, "port")
	d := &driver{bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}}
	sb := &worker{bootDir: dir}

	require.False(t, d.listened(sb), "no port file")
	require.NoError(t, os.Symlink("/dev/zero", port))
	require.False(t, d.listened(sb), "a symlink is never followed")
	require.NoError(t, os.Remove(port))

	require.NoError(t, syscall.Mkfifo(port, 0o600))
	done := make(chan bool, 1)
	go func() { done <- d.listened(sb) }()
	select {
	case wrote := <-done:
		require.False(t, wrote, "a FIFO is not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO port path hung")
	}
	require.NoError(t, os.Remove(port))

	require.NoError(t, os.WriteFile(port, []byte("8080"), 0o600))
	require.True(t, d.listened(sb), "a regular file is the port handshake")
	require.NoError(t, os.Remove(port))
	require.True(t, d.listened(sb), "Listened stays latched")
	require.False(t, portWritten(""), "no boot dir")
}

func TestCreateBootDir(t *testing.T) {
	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "boot",
		Revision:  "boot-1",
		Image:     "funcd/boot:latest",
	}
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)
	ctrID, _ := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	newDriver := func(t *testing.T) *driver {
		cs, layer, manifest := fakeImage(t)
		snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
		client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, rejectingContainers{},
			map[string]snapshots.Snapshotter{"overlayfs": snap})
		return &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: attachedCNI{}, bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}}
	}

	t.Run("failed-create-leaves-no-dir", func(t *testing.T) {
		d := newDriver(t)
		_, err := d.Create(leases.WithLease(context.Background(), "boot"), spec)
		require.Error(t, err)
		_, serr := os.Lstat(filepath.Join(d.bootRoot, nsPrefix+"default", ctrID))
		require.ErrorIs(t, serr, os.ErrNotExist, "a failed Create removes the boot dir it made")
		fi, serr := os.Stat(filepath.Join(d.bootRoot, nsPrefix+"default"))
		require.NoError(t, serr)
		require.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "the boot dir's parents are 0700")
	})

	t.Run("live-id-conflicts", func(t *testing.T) {
		d := newDriver(t)
		bootDir := filepath.Join(d.bootRoot, nsPrefix+"default", ctrID)
		require.NoError(t, os.MkdirAll(bootDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(bootDir, "port"), []byte("8080"), 0o600))
		d.instances[id] = &worker{ctrID: ctrID, namespace: spec.Namespace, ownerKind: spec.OwnerKind, bootDir: bootDir}

		_, err := d.Create(leases.WithLease(context.Background(), "boot"), spec)
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		_, serr := os.Lstat(filepath.Join(bootDir, "port"))
		require.NoError(t, serr, "a Create of a running ID never touches its boot dir")
	})

	t.Run("no-boot-root", func(t *testing.T) {
		d := &driver{instances: map[runtime.InstanceID]*worker{}}
		_, err := d.Create(context.Background(), spec)
		require.Equal(t, fault.Internal, fault.KindOf(err))
	})
}

func TestMakeBootRoot(t *testing.T) {
	state := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(state, "boot"), 0o755))
	dir, err := makeBootRoot(state)
	require.NoError(t, err)
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "0700 is enforced on an existing boot root")

	tmp, err := makeBootRoot("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	fi, err = os.Stat(tmp)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), "an unset state dir gets a private temp boot root")
}
