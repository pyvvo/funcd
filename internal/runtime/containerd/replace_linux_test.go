//go:build linux

package containerd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// Issue 424: Create re-creates a worker that Stop has released (ADR-0142), so it must delete the replaced worker's
// driver-owned log file, as Remove does, or every replacement leaves one behind.
func TestIssue424_RecreateRemovesReplacedLogFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx := leases.WithLease(context.Background(), "issue424")
	cs, layer, manifest := fakeImage(t)
	spec := runtime.WorkerSpec{
		Namespace: "default",
		OwnerKind: v1alpha1.KindFunction,
		Name:      "issue424",
		Revision:  "issue424-1",
		Image:     "funcd/issue424:latest",
	}
	ctrID, _ := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: spec.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	d := &driver{client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	inst, err := d.Create(ctx, spec)
	require.NoError(t, err)
	delete(ctrs.records, ctrID) // the worker exited and its container is gone, so Stop only releases it
	require.NoError(t, d.Stop(ctx, inst.ID))
	_, err = d.Create(ctx, spec)
	require.NoError(t, err)

	logs, err := filepath.Glob(filepath.Join(tmp, "funcd-issue424-r0-*.log"))
	require.NoError(t, err)
	require.Equal(t, []string{d.instances[inst.ID].logPath}, logs, "only the new worker's log file may remain")
}
