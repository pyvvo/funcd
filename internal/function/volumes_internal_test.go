package function

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/volume"
)

// withVolumes gives r a volume manager rooted in a temp dir, and returns the root.
func withVolumes(t *testing.T, r *Reconciler) string {
	t.Helper()
	root := t.TempDir()
	m, err := volume.New(volume.Options{Root: root, Holder: r.FunctionUID})
	require.NoError(t, err)
	r.volumes, r.volumesDurable = m, true
	return root
}

func volumeFn(name v1.ObjectName, vs ...v1.VolumeEntry) *v1.Function {
	fn := sampleFn()
	fn.Name, fn.Namespace, fn.ResourceGroup, fn.UID = name, "shop", "rg", v1.UID("uid-"+string(name))
	fn.Spec.Volumes = vs
	return fn
}

func stateVolume() v1.VolumeEntry {
	return v1.VolumeEntry{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}
}
func cacheVolume() v1.VolumeEntry {
	return v1.VolumeEntry{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 1 << 20}
}

func mountFor(spec runtime.WorkerSpec, target string) (runtime.Mount, bool) {
	for _, m := range spec.Mounts {
		if m.Target == target {
			return m, true
		}
	}
	return runtime.Mount{}, false
}

// TestScenarioVolumePathInEnv: a container worker reads its volume's path from FUNCD_VOLUME_STATE, the in-container
// /var/funcd/volumes/state, a writable bind; a process-mode worker reads the host directory itself.
// scenario: volume-path-in-env
func TestScenarioVolumePathInEnv(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	root := withVolumes(t, r)
	fn := volumeFn("cart", stateVolume())
	spec := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	require.Equal(t, "/var/funcd/volumes/state", spec.Env["FUNCD_VOLUME_STATE"])
	m, ok := mountFor(spec, "/var/funcd/volumes/state")
	require.True(t, ok, "the volume is mounted")
	require.False(t, m.ReadOnly)
	require.Equal(t, filepath.Join(root, "shop", "cart", "state"), m.Source)

	p := newShimReconciler(t, nil)
	proot := withVolumes(t, p)
	pspec := mustWorkerSpec(t, p, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	require.Equal(t, filepath.Join(proot, "shop", "cart", "state"), pspec.Env["FUNCD_VOLUME_STATE"])
	require.Empty(t, pspec.Mounts, "process mode mounts nothing")
}

// TestScenarioOnlyItsOwnVolume: each worker gets binds of its own volumes' leaf directories only, so walking up from a
// volume path finds no sibling volume and not the volume root, which are never mounted.
// scenario: only-its-own-volume
func TestScenarioOnlyItsOwnVolume(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	root := withVolumes(t, r)
	cart := mustWorkerSpec(t, r, volumeFn("cart", stateVolume(), cacheVolume()), "/art/app.mjs", envresolve.Resolved{}, nil)
	thumbs := mustWorkerSpec(t, r, volumeFn("thumbs", stateVolume()), "/art/app.mjs", envresolve.Resolved{}, nil)
	theirs := filepath.Join(root, "shop", "thumbs", "state")
	for _, m := range cart.Mounts {
		if !strings.HasPrefix(m.Target, containerVolumeDir) {
			continue
		}
		require.True(t, strings.HasPrefix(m.Source, filepath.Join(root, "shop", "cart")+string(filepath.Separator)), "cart mounts only its own leaves: %s", m.Source)
		require.NotEqual(t, root, m.Source)
		require.False(t, strings.HasPrefix(theirs, m.Source+string(filepath.Separator)), "no cart mount contains thumbs's volume")
	}
	m, _ := mountFor(thumbs, "/var/funcd/volumes/state")
	require.Equal(t, theirs, m.Source)
}

// TestScenarioLocalSurvives: a local volume keeps its files across a replaced worker, a new revision's worker and a
// worker created after a scale to zero, all on the same leaf.
// scenario: local-survives
func TestScenarioLocalSurvives(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	fn := volumeFn("cart", stateVolume())
	first := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	dir := first.Env["FUNCD_VOLUME_STATE"]
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.db"), []byte("rows"), 0o600))
	for _, rev := range []v1.ObjectName{"", "cart-2", "cart-3"} {
		spec, err := r.workerSpec(t.Context(), fn, rev, 0, "/art/app.mjs", envresolve.Resolved{}, nil)
		require.NoError(t, err)
		require.Equal(t, dir, spec.Env["FUNCD_VOLUME_STATE"])
		b, err := os.ReadFile(filepath.Join(spec.Env["FUNCD_VOLUME_STATE"], "orders.db"))
		require.NoError(t, err)
		require.Equal(t, "rows", string(b))
	}
}

// TestScenarioEmptyDirGoesWithWorker: a replaced worker gets a new, empty emptyDir, and the old one's leaf and space are
// released.
// scenario: emptydir-goes-with-worker
func TestScenarioEmptyDirGoesWithWorker(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	fn := volumeFn("thumbs", cacheVolume())
	first := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	old := first.Env["FUNCD_VOLUME_CACHE"]
	require.NoError(t, os.WriteFile(filepath.Join(old, "tmp"), []byte("x"), 0o600))
	second := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	fresh := second.Env["FUNCD_VOLUME_CACHE"]
	require.NotEqual(t, old, fresh)
	ents, err := os.ReadDir(fresh)
	require.NoError(t, err)
	require.Empty(t, ents)
	_, err = os.Stat(old)
	require.True(t, os.IsNotExist(err), "the replaced worker's emptyDir is released: %v", err)
	infos, err := r.volumes.List(t.Context())
	require.NoError(t, err)
	require.Len(t, infos, 1, "one emptyDir is booked")
}

// TestVolumeGates maps each volume refusal to its gate: StoreNotOwned, VolumeCapacity and VolumeBudget wait; a changed
// size fails.
func TestVolumeGates(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]string{
		fault.Wrapf(volume.ErrNotOwned, fault.Conflict, "op", "x"): reasonStoreNotOwned,
		fault.Wrapf(volume.ErrCapacity, fault.Conflict, "op", "x"): reasonVolumeCapacity,
		fault.Wrapf(volume.ErrBudget, fault.Conflict, "op", "x"):   reasonVolumeBudget,
		fault.NotFoundf("op", "gone"):                              reasonVolumeNotFound,
		fault.Conflictf("op", "held"):                              reasonVolumeHeld,
		fault.Invalidf("op", "resized"):                            reasonVolumeInvalid,
	} {
		g, gerr := volumeGate("spec.volumes[0]", err)
		require.NoError(t, gerr)
		require.Equal(t, want, g.reason)
		require.Contains(t, g.message, "spec.volumes[0]")
	}
	_, gerr := volumeGate("f", fault.Internalf("op", "disk"))
	require.Error(t, gerr, "an error that names no gate fails the pass")
}

// TestEnsureVolumesRefOwnership (Decision 7): a name volume held under another UID gates StoreNotOwned; ref adopts
// it, keeping its data; a Function with volumes and no manager gates VolumesOff.
func TestEnsureVolumesRefOwnership(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	old := volumeFn("cart", stateVolume())
	g, err := r.ensureVolumes(t.Context(), old)
	require.NoError(t, err)
	require.Nil(t, g)
	obj, err := r.store.Create(t.Context(), volumeFn("cart", stateVolume())) // cart deleted and applied again: a new UID
	require.NoError(t, err)
	again := obj.(*v1.Function)
	g, err = r.ensureVolumes(t.Context(), again)
	require.NoError(t, err)
	require.Equal(t, reasonStoreNotOwned, g.reason)
	again.Spec.Volumes = []v1.VolumeEntry{{Ref: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}}
	g, err = r.ensureVolumes(t.Context(), again)
	require.NoError(t, err)
	require.Nil(t, g, "ref adopts the retained volume")

	off := newShimReconciler(t, nil)
	g, err = off.ensureVolumes(t.Context(), volumeFn("cart", stateVolume()))
	require.NoError(t, err)
	require.Equal(t, reasonVolumesOff, g.reason)
}

// TestDataMissingAfterLoss (Decision 12): a Function stored Ready whose local volume has no leaf at this process's
// first pass gets DataMissing=True; a Succeeded Restore into the volume created since clears it.
func TestDataMissingAfterLoss(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	fn := volumeFn("cart", stateVolume())
	fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	require.NoError(t, r.dataMissing(t.Context(), fn))
	c, ok := fn.Status.Conditions.Get(condDataMissing)
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, c.Status)

	rs := &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: "back", Namespace: "shop", ResourceGroup: "rg"},
		Spec:       v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "owner-key"}, Members: []v1.RestoreMember{{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}}},
		Status:     v1.RestoreStatus{Phase: v1.RunSucceeded}}
	time.Sleep(2 * time.Millisecond) // the Restore's creation follows the condition's transition by at least a millisecond
	other := *rs
	other.Name = "other"
	other.Spec.Members = []v1.RestoreMember{{Kind: v1.KindFunction, Name: "cart", Volume: "cache", InPlace: true}}
	_, err := r.store.Create(t.Context(), &other)
	require.NoError(t, err)
	require.NoError(t, r.dataMissing(t.Context(), fn))
	c, _ = fn.Status.Conditions.Get(condDataMissing)
	require.Equal(t, v1.ConditionTrue, c.Status, "a Restore of another volume leaves DataMissing")
	_, err = r.store.Create(t.Context(), rs)
	require.NoError(t, err)
	reqs := r.MapRestore(t.Context(), rs)
	require.Len(t, reqs, 1)
	require.Equal(t, v1.ObjectName("cart"), reqs[0].Name)
	require.NoError(t, r.dataMissing(t.Context(), fn))
	c, _ = fn.Status.Conditions.Get(condDataMissing)
	require.Equal(t, v1.ConditionFalse, c.Status, "the Restore clears DataMissing")
}

// TestDeleteVolume (Decision 8): the route refuses a volume a live Function holds (Conflict) and one that does not
// exist (NotFound), and removes a retained one.
func TestDeleteVolume(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	withVolumes(t, r)
	obj, err := r.store.Create(t.Context(), volumeFn("cart", stateVolume()))
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	g, err := r.ensureVolumes(t.Context(), fn)
	require.NoError(t, err)
	require.Nil(t, g)
	require.Equal(t, fault.Conflict, fault.KindOf(r.DeleteVolume(t.Context(), "shop", "cart", "state")))
	require.Equal(t, fault.NotFound, fault.KindOf(r.DeleteVolume(t.Context(), "shop", "cart", "other")))
	require.NoError(t, r.store.Delete(t.Context(), v1.KindFunction.GVK(), "shop", "cart", ""))
	require.NoError(t, r.DeleteVolume(t.Context(), "shop", "cart", "state"))
	infos, err := r.volumes.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, infos)
}
