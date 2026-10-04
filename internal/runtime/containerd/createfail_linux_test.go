//go:build linux

package containerd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	gocni "github.com/containerd/go-cni"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime"
)

// Issue 492: a Create that fails after it made the worker's temp log file must delete it; the worker is never
// registered, so no Remove ever will.
func TestIssue492_FailedCreateRemovesLogFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx := leases.WithLease(context.Background(), "issue492")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "issue492",
		Revision:  "issue492-1",
		Image:     "funcd/issue492:latest",
	}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, rejectingContainers{},
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	d := &driver{client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.Error(t, err)

	logs, err := filepath.Glob(filepath.Join(tmp, "funcd-issue492-r0-*.log"))
	require.NoError(t, err)
	require.Empty(t, logs, "a failed Create must remove the log file it created")
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
		Name:      "cnisetup",
		Revision:  "cnisetup-1",
		Image:     "funcd/cnisetup:latest",
		LogPath:   filepath.Join(t.TempDir(), "worker.log"),
	}
	_, cniID := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	cni := &failingSetupCNI{}
	d := &driver{client: client, cni: cni, instances: map[runtime.InstanceID]*worker{}}

	_, err := d.Create(ctx, spec)
	require.ErrorIs(t, err, errFirewallAdd, "Create must return the Setup error")
	require.Equal(t, [][2]string{{cniID, "/proc/1/ns/net"}}, cni.removed, // the fake task's pid is 1
		"a failed Setup must be undone with a Remove of the same attachment")
	require.Empty(t, ctrs.records, "a failed Create must still delete its container")
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
