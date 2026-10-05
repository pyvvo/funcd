//go:build linux

package containerd

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	gocni "github.com/containerd/go-cni"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// scenario: unrevisioned-cni-ids-distinct-across-namespaces (ADR-0179)
func TestScenario_UnrevisionedCNIIDsDistinctAcrossNamespaces(t *testing.T) {
	_, abc := workerNames("a-b", "c", "", "0")
	_, abC := workerNames("a", "b-c", "", "0")
	require.Equal(t, "a-b_c-r0", abc)
	require.Equal(t, "a_b-c-r0", abC)

	f := newCNIIDFixture(t)
	ctx := leases.WithLease(context.Background(), "cniid")
	first, err := f.d.Create(ctx, f.engine(t, "a-b", "c"))
	require.NoError(t, err)
	_, err = f.d.Create(ctx, f.engine(t, "a", "b-c"))
	require.NoError(t, err)
	require.Equal(t, []string{abc, abC}, f.cni.setups, "each worker must get its own CNI attachment")

	require.NoError(t, f.d.Stop(ctx, first.ID))
	require.Equal(t, [][2]string{{abc, "/proc/1/ns/net"}}, f.cni.removed, "Stop must remove only the stopped worker's attachment")
}

// Issue 706: the bridge plugin frees a worker's masquerade rules only in a live netns, and the netns goes with the task.
func TestIssue706_StopSendsCNIDelBeforeTaskStops(t *testing.T) {
	f := newCNIIDFixture(t)
	tasks, ok := f.d.client.TaskService().(*createdTasks)
	require.True(t, ok)
	cni := &taskStateCNI{recordingCNI: f.cni, tasks: tasks}
	f.d.cni = cni
	ctx := leases.WithLease(context.Background(), "issue706")
	inst, err := f.d.Create(ctx, f.engine(t, "a", "b"))
	require.NoError(t, err)

	require.NoError(t, f.d.Stop(ctx, inst.ID))
	require.Equal(t, [][2]string{{"a_b-r0", "/proc/1/ns/net"}}, f.cni.removed)
	require.Equal(t, [][2]bool{{false, false}}, cni.killedDeleted, "Stop must send the CNI DEL before it kills and deletes the task")
	require.Equal(t, []string{"b-r0"}, tasks.deleted)
}

// scenario: leftover-old-form-attachment-released (ADR-0179)
func TestScenario_LeftoverOldFormAttachmentReleased(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "leftover")
	unrevisioned := map[string]string{"funcd/namespace": "a-b", "funcd/name": "c", "funcd/replica": "0"}

	t.Run("reclaim", func(t *testing.T) {
		f := newCNIIDFixture(t)
		f.plant("c-r0", unrevisioned)
		_, err := f.d.Create(ctx, f.engine(t, "a-b", "c"))
		require.NoError(t, err)
		require.Equal(t, [][2]string{{"a-b_c-r0", ""}, {"a-b-c-r0", ""}}, f.cni.removed,
			"reclaim must release the leftover's current-form and old-form attachments")
		require.Equal(t, []string{"a-b_c-r0"}, f.cni.setups)
	})

	t.Run("sweep", func(t *testing.T) {
		f := newCNIIDFixture(t)
		f.plant("c-r0", unrevisioned)
		_, err := f.d.Sweep(ctx, "a-b")
		require.NoError(t, err)
		require.Equal(t, [][2]string{{"a-b_c-r0", ""}, {"a-b-c-r0", ""}}, f.cni.removed)
	})

	t.Run("revisioned", func(t *testing.T) {
		f := newCNIIDFixture(t)
		f.plant("lake-2.r0", map[string]string{
			"funcd/namespace": "default", "funcd/name": "lake", "funcd/revision": "lake-2", "funcd/replica": "0",
		})
		_, err := f.d.Sweep(ctx, "default")
		require.NoError(t, err)
		require.Equal(t, [][2]string{{"default.lake-2.r0", ""}}, f.cni.removed, "a revisioned worker has no old-form attachment")
	})

	t.Run("empty label", func(t *testing.T) {
		f := newCNIIDFixture(t)
		f.plant("c-r0", map[string]string{"funcd/namespace": "a-b", "funcd/name": "c"})
		_, err := f.d.Sweep(ctx, "a-b")
		require.NoError(t, err)
		require.Equal(t, [][2]string{{"a-b_c-r", ""}}, f.cni.removed, "no old-form DEL without every label")
	})
}

// scenario: other-worker-names-unchanged (ADR-0179)
func TestScenario_OtherWorkerNamesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		ns, name, revision, replica string
		ctrID, cniID                string
	}{
		{ns: "default", name: "lake", revision: "lake-2", replica: "0", ctrID: "lake-2.r0", cniID: "default.lake-2.r0"},
		{ns: "a-b", name: "c", revision: "c-7", replica: "3", ctrID: "c-7.r3", cniID: "a-b.c-7.r3"},
		{ns: "a-b", name: "c", replica: "0", ctrID: "c-r0"},
		{ns: "default", name: "__pool__nodejs22__shared", replica: "1", ctrID: "__pool__nodejs22__shared-r1"},
	} {
		ctrID, cniID := workerNames(tc.ns, tc.name, tc.revision, tc.replica)
		require.Equal(t, tc.ctrID, ctrID)
		if tc.revision != "" {
			require.Equal(t, tc.cniID, cniID)
		}
	}

	f := newCNIIDFixture(t)
	ctx := leases.WithLease(context.Background(), "names")
	fn := f.engine(t, "default", "lake")
	fn.OwnerKind, fn.Revision = v1alpha1.KindFunction, "lake-2"
	solo, err := f.d.Create(ctx, fn)
	require.NoError(t, err)
	engine, err := f.d.Create(ctx, f.engine(t, "a-b", "c"))
	require.NoError(t, err)

	require.Equal(t, runtime.InstanceID("default/lake/lake-2/r0"), solo.ID)
	require.Equal(t, runtime.InstanceID("a-b/c/r0"), engine.ID)
	require.Contains(t, f.ctrs.records, "lake-2.r0")
	require.Contains(t, f.ctrs.records, "c-r0")
	require.Equal(t, "default.lake-2.r0", f.cni.setups[0])
}

// cniIDFixture is a fake containerd holding one image, with a CNI that records every attachment it sets up and removes.
type cniIDFixture struct {
	d     *driver
	ctrs  *memContainers
	cni   *recordingCNI
	image string
}

func newCNIIDFixture(t *testing.T) *cniIDFixture {
	cs, layer, manifest := fakeImage(t)
	const image = "funcd/cniid:latest"
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: image, Target: manifest}, ctrs,
		map[string]snapshots.Snapshotter{"overlayfs": snap})
	cni := &recordingCNI{}
	return &cniIDFixture{
		d:    &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: cni, bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}},
		ctrs: ctrs, cni: cni, image: image,
	}
}

// engine is the spec of an unrevisioned CatalogService engine.
func (f *cniIDFixture) engine(t *testing.T, ns v1alpha1.NamespaceName, name v1alpha1.ObjectName) runtime.WorkerSpec {
	return runtime.WorkerSpec{
		Namespace: ns,
		Name:      name,
		OwnerKind: v1alpha1.KindCatalogService,
		Image:     f.image,
	}
}

// plant leaves a container with no task behind, as a worker of an earlier run.
func (f *cniIDFixture) plant(id string, labels map[string]string) {
	f.ctrs.records[id] = containers.Container{ID: id, Labels: labels}
}

type recordingCNI struct {
	gocni.CNI
	mu      sync.Mutex
	setups  []string
	removed [][2]string
}

func (c *recordingCNI) Setup(_ context.Context, id, _ string, _ ...gocni.NamespaceOpts) (*gocni.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setups = append(c.setups, id)
	return &gocni.Result{}, nil
}

func (c *recordingCNI) Remove(_ context.Context, id, path string, _ ...gocni.NamespaceOpts) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed = append(c.removed, [2]string{id, path})
	return nil
}

// taskStateCNI records, at each Remove, whether the task was already killed and deleted.
type taskStateCNI struct {
	*recordingCNI
	tasks         *createdTasks
	killedDeleted [][2]bool
}

func (c *taskStateCNI) Remove(ctx context.Context, id, path string, opts ...gocni.NamespaceOpts) error {
	c.tasks.mu.Lock()
	c.killedDeleted = append(c.killedDeleted, [2]bool{len(c.tasks.killed) > 0, len(c.tasks.deleted) > 0})
	c.tasks.mu.Unlock()
	return c.recordingCNI.Remove(ctx, id, path, opts...)
}
