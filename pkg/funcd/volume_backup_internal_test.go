package funcd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestScenarioVolumeBackupRestore: a schedule whose scope names cart/state copies the volume as member Function/cart
// volume state; after the host loses the volume, cart's empty volume takes a Restore of that member back.
// scenario: volume-backup-restore
func TestScenarioVolumeBackupRestore(t *testing.T) {
	t.Parallel()
	cart := &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta("cart"), Spec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
		Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}},
	}}
	r := newWBRig(t, wbOptions{
		opts:  []Option{WithFunctionVolumes(volume.Options{Root: t.TempDir()}, false)},
		setup: func(r *wbRig) { r.seed(cart) },
	})
	stored, err := r.st.Get(r.ctx, v1.KindFunction.GVK(), "orders", "cart")
	require.NoError(t, err)
	id := volume.ID{Namespace: "orders", Function: "cart", Name: "state"}
	reserve := func() string {
		dir, err := r.p.volumes.Reserve(r.ctx, id, stored.GetObjectMeta().UID, 1<<20, v1.DeletionRetain)
		require.NoError(t, err)
		return dir
	}
	dir := reserve()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "orders.sqlite"), []byte("rows"), 0o600))

	r.create(r.dev, r.schedule("orders-vol", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Volumes: []string{"cart/state"}}
	}))
	r.create(r.dev, r.backupObj("b1", "orders-vol"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Len(t, b.Status.Members, 1)
	require.Equal(t, v1.BackupMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", Bytes: b.Status.Members[0].Bytes}, b.Status.Members[0])

	require.NoError(t, r.p.volumes.Release(r.ctx, id)) // the host is lost: the volume goes, cart's next pass makes it empty
	dir = reserve()
	r.create(r.admin, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}))
	r.waitRestore("back", v1.RunSucceeded)
	got, err := os.ReadFile(filepath.Join(dir, "db", "orders.sqlite"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(got))
}

// TestRefusedVolumeRestoreKeepsItsData: an in-place Restore into a volume that holds data is refused NotEmpty (ADR-0262
// Decision 11), and the refusal leaves the volume as it was: the cleanup after a failed load empties only a target the
// load wrote.
func TestRefusedVolumeRestoreKeepsItsData(t *testing.T) {
	t.Parallel()
	cart := &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta("cart"), Spec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
		Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}},
	}}
	r := newWBRig(t, wbOptions{
		opts:  []Option{WithFunctionVolumes(volume.Options{Root: t.TempDir()}, false)},
		setup: func(r *wbRig) { r.seed(cart) },
	})
	stored, err := r.st.Get(r.ctx, v1.KindFunction.GVK(), "orders", "cart")
	require.NoError(t, err)
	dir, err := r.p.volumes.Reserve(r.ctx, volume.ID{Namespace: "orders", Function: "cart", Name: "state"}, stored.GetObjectMeta().UID, 1<<20, v1.DeletionRetain)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.sqlite"), []byte("rows"), 0o600))
	r.create(r.dev, r.schedule("orders-vol", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Volumes: []string{"cart/state"}}
	}))
	r.create(r.dev, r.backupObj("b1", "orders-vol"))
	r.waitBackup("b1", v1.RunSucceeded)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "newer.txt"), []byte("written after the backup"), 0o600))

	r.create(r.admin, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}))
	rs := r.waitRestore("back", v1.RunFailed)
	c, ok := rs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, "NotEmpty", c.Reason)
	for name, want := range map[string]string{"orders.sqlite": "rows", "newer.txt": "written after the backup"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, "the refused Restore removed %s", name)
		require.Equal(t, want, string(got))
	}
}

// TestIssue1206_RestoreReplacesWhatALostVolumeGot: after a loss the Function's worker starts on its empty volume and
// writes there; a Restore into the volume its DataMissing names replaces what the volume got since the loss with the
// backup (#1206).
func TestIssue1206_RestoreReplacesWhatALostVolumeGot(t *testing.T) {
	t.Parallel()
	cart := &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta("cart"), Spec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
		Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}},
	}}
	r := newWBRig(t, wbOptions{
		opts:  []Option{WithFunctionVolumes(volume.Options{Root: t.TempDir()}, false)},
		setup: func(r *wbRig) { r.seed(cart) },
	})
	stored, err := r.st.Get(r.ctx, v1.KindFunction.GVK(), "orders", "cart")
	require.NoError(t, err)
	id := volume.ID{Namespace: "orders", Function: "cart", Name: "state"}
	reserve := func() string {
		dir, err := r.p.volumes.Reserve(r.ctx, id, stored.GetObjectMeta().UID, 1<<20, v1.DeletionRetain)
		require.NoError(t, err)
		return dir
	}
	dir := reserve()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.sqlite"), []byte("rows"), 0o600))
	r.create(r.dev, r.schedule("orders-vol", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Volumes: []string{"cart/state"}}
	}))
	r.create(r.dev, r.backupObj("b1", "orders-vol"))
	r.waitBackup("b1", v1.RunSucceeded)

	require.NoError(t, r.p.volumes.Release(r.ctx, id)) // the host is lost
	dir = reserve()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.sqlite"), []byte("schema only"), 0o600)) // the worker's start
	require.NoError(t, os.WriteFile(filepath.Join(dir, "since.log"), []byte("a write since the loss"), 0o600))
	require.Eventually(t, func() bool { // the first pass's DataMissing, as the Function reconciler sets it
		obj, err := r.st.Get(r.ctx, v1.KindFunction.GVK(), "orders", "cart")
		require.NoError(t, err)
		fn := obj.(*v1.Function)
		fn.Status.Conditions.Set(v1.Condition{Type: "DataMissing", Status: v1.ConditionTrue, Reason: "NoVolume",
			Message: "lost volumes: state; the Function was Ready before and these local volumes are gone, so they start empty: run a workload Restore into each"})
		_, err = r.st.Update(r.ctx, fn)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	r.create(r.admin, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}))
	r.waitRestore("back", v1.RunSucceeded)
	got, err := os.ReadFile(filepath.Join(dir, "orders.sqlite"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(got), "the backup's file replaces the one written since the loss")
	_, err = os.Stat(filepath.Join(dir, "since.log"))
	require.True(t, os.IsNotExist(err), "what the volume got since the loss is gone: %v", err)
}
