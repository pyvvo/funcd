//go:build linux

package containerd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
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
