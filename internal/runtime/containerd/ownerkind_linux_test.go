//go:build linux

package containerd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0152: a worker container carries its owner kind as a label; Create refuses an empty kind, an ID another kind's
// worker holds in this driver, and a leftover container another kind's worker left behind, which it keeps.
func TestOwnerKindLabelAndConflicts(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "ownerkind")
	cs, layer, manifest := fakeImage(t)
	engine := runtime.WorkerSpec{
		Namespace: "default",
		Name:      "lake",
		OwnerKind: v1alpha1.KindCatalogService,
		Image:     "funcd/ownerkind:latest",
		LogPath:   filepath.Join(t.TempDir(), "engine.log"),
	}
	ctrID, _ := workerNames(string(engine.Namespace), string(engine.Name), "", "0")
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: engine.Image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	d := &driver{client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}

	inst, err := d.Create(ctx, engine)
	require.NoError(t, err)
	require.Equal(t, v1alpha1.KindCatalogService, inst.OwnerKind)
	require.Equal(t, "CatalogService", ctrs.records[ctrID].Labels[ownerKindLabel])

	ownerless := engine
	ownerless.OwnerKind = ""
	_, err = d.Create(ctx, ownerless)
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	fn := engine
	fn.OwnerKind = v1alpha1.KindFunction
	fn.LogPath = filepath.Join(t.TempDir(), "fn.log")
	_, err = d.Create(ctx, fn)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "this driver holds the ID for a CatalogService worker")

	restarted := &driver{client: client, cni: attachedCNI{}, instances: map[runtime.InstanceID]*worker{}}
	_, err = restarted.Create(ctx, fn)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a leftover labelled CatalogService is not reclaimed for a Function")
	require.Contains(t, ctrs.records, ctrID, "the other kind's leftover is kept")
	require.Equal(t, "CatalogService", ctrs.records[ctrID].Labels[ownerKindLabel])
}

// ADR-0152: the container IDs of the three worker shapes never meet: a solo Function worker's contains '.', a pool
// worker's '_', and an engine's neither.
func TestContainerIDsDisjointAcrossKinds(t *testing.T) {
	solo, _ := workerNames("default", "lake", "lake-2", "0")
	pool, _ := workerNames("default", "__pool__nodejs22__shared", "", "0")
	engine, _ := workerNames("default", "lake", "", "0")

	require.Contains(t, solo, ".")
	require.NotContains(t, solo, "_")
	require.Contains(t, pool, "_")
	require.NotContains(t, pool, ".")
	require.False(t, strings.ContainsAny(engine, "._"), "an engine container ID %q contains neither '.' nor '_'", engine)
	require.Len(t, map[string]bool{solo: true, pool: true, engine: true}, 3)
}
