//go:build linux

package containerd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	gocni "github.com/containerd/go-cni"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// Issue 492: a Create that fails leaves nothing behind: the worker is never registered, so no Remove ever would
// delete it. Its output would have gone through FIFOs under the driver's fifoDir (ADR-0168).
func TestIssue492_FailedCreateLeavesNoFifoDir(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "issue492")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "issue492",
		Revision:  "issue492-1",
		Image:     "funcd/issue492:latest",
	}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, rejectingContainers{},
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	fifoDir := t.TempDir()
	d := &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: attachedCNI{}, bootRoot: t.TempDir(), fifoDir: fifoDir, instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.Error(t, err)
	dirs, err := os.ReadDir(fifoDir)
	require.NoError(t, err)
	require.Empty(t, dirs, "a failed Create must leave no FIFO dir")
}

type rejectingContainers struct{ containers.Store }

func (rejectingContainers) Get(_ context.Context, id string) (containers.Container, error) {
	return containers.Container{}, fmt.Errorf("container %q: %w", id, errdefs.ErrNotFound)
}

func (rejectingContainers) Create(context.Context, containers.Container) (containers.Container, error) {
	return containers.Container{}, fmt.Errorf("create container: %w", errdefs.ErrInvalidArgument)
}

// A Create whose network setup fails must send the CNI DEL itself: Setup does not undo the plugins that succeeded, so
// the worker's host-local lease would outlive the failed Create.
func TestCreate_FailedNetworkSetupRemovesCNIAttachment(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "cnisetup")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "cnisetup",
		Revision:  "cnisetup-1",
		Image:     "funcd/cnisetup:latest",
	}
	_, cniID := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	cni := &failingSetupCNI{}
	d := &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: cni, bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.ErrorIs(t, err, errFirewallAdd, "Create must return the Setup error")
	require.Equal(t, [][2]string{{cniID, "/proc/1/ns/net"}}, cni.removed, // the fake task's pid is 1
		"a failed Setup must be undone with a Remove of the same attachment")
	require.Empty(t, ctrs.records, "a failed Create must still delete its container")
}

// A Create whose network setup fails must kill its task before it deletes it: containerd refuses to delete a created
// task that has a pid, and then refuses to delete its container, so the init process, its netns, the container and its
// snapshot would outlive the failed Create.
func TestCreate_FailedNetworkSetupKillsTask(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "killtask")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "killtask",
		Revision:  "killtask-1",
		Image:     "funcd/killtask:latest",
	}
	ctrID, _ := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	fifoDir := t.TempDir()
	d := &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: &failingSetupCNI{}, bootRoot: t.TempDir(), fifoDir: fifoDir, instances: map[runtime.InstanceID]*worker{}}
	hooked := false
	d.outputs = func(runtime.WorkerSpec, *workerpipe.Output) { hooked = true }

	_, err := d.Create(ctx, spec)
	require.ErrorIs(t, err, errFirewallAdd, "Create must return the Setup error")
	require.False(t, hooked, "a failed Create must start no Pump on its output")
	dirs, err := os.ReadDir(fifoDir)
	require.NoError(t, err)
	require.Empty(t, dirs, "a failed Create must close its task IO and remove the run's FIFO dir")
	tasks := client.TaskService().(*createdTasks)
	require.Equal(t, []string{ctrID}, tasks.killed, "a failed Create must kill its created task")
	require.Equal(t, []string{ctrID}, tasks.deleted, "a failed Create must delete its task")
	require.Empty(t, ctrs.records, "a failed Create must delete its container")
	require.NotContains(t, snap.keys, ctrID+"-snap", "a failed Create must remove its snapshot")
}

var errFirewallAdd = errors.New("plugin type=\"firewall\" failed (add)")

// failingSetupCNI fails Setup as a plugin after the bridge does (the IP is allocated by then) and records each Remove.
type failingSetupCNI struct {
	gocni.CNI
	removed [][2]string
}

func (*failingSetupCNI) Setup(context.Context, string, string, ...gocni.NamespaceOpts) (*gocni.Result, error) {
	return nil, errFirewallAdd
}

func (c *failingSetupCNI) Remove(_ context.Context, id, path string, _ ...gocni.NamespaceOpts) error {
	c.removed = append(c.removed, [2]string{id, path})
	return nil
}
