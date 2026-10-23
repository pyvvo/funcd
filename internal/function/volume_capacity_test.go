package function_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestScenarioEmptyDirOverCapacityIsNotReady: a worker whose emptyDir does not fit the capacity is not created, and
// the Function is Ready=False with reason VolumeCapacity (ADR-0262 Decision 5, scenario capacity-not-overcommitted).
func TestScenarioEmptyDirOverCapacityIsNotReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, 200, false, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir(), CapacityBytes: 1 << 20})
		require.NoError(t, err)
		d.Volumes = m
	})
	h.create(t, "thumbs", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 2 << 20}}
	})
	h.reconcile(t, "thumbs")
	h.requireCondition(t, "thumbs", "Ready", v1.ConditionFalse, "VolumeCapacity")
	_, created := h.rt.specFor("thumbs")
	require.False(t, created, "no worker is created without its emptyDir")
}

// TestIssue1206_RestoredVolumeReplacesItsWorkers: a Succeeded Restore into a lost volume clears DataMissing and replaces
// the worker that ran on the empty volume, which keeps the files it opened there, so the next one opens the restored
// files (#1206).
func TestIssue1206_RestoredVolumeReplacesItsWorkers(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, 200, false, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir()})
		require.NoError(t, err)
		d.Volumes, d.VolumesDurable = m, true
	})
	h.create(t, "cart", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}}
	})
	h.reconcile(t, "cart")
	h.reconcile(t, "cart")
	before := h.creates()
	require.NotZero(t, before, "cart runs a worker")
	fn := h.getFn(t, "cart")
	fn.Status.Conditions.Set(v1.Condition{Type: "DataMissing", Status: v1.ConditionTrue, Reason: "NoVolume", // as the first pass after a loss sets it
		Message: "lost volumes: state; the Function was Ready before and these local volumes are gone, so they start empty: run a workload Restore into each"})
	_, err := h.st.Update(t.Context(), fn)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond) // the Restore's creation follows the condition's transition
	_, err = h.st.Create(t.Context(), &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: "back", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "owner-key"},
			Members: []v1.RestoreMember{{Kind: v1.KindFunction, Name: "cart", Volume: "state", InPlace: true}}},
		Status: v1.RestoreStatus{Phase: v1.RunSucceeded}})
	require.NoError(t, err)
	h.reconcile(t, "cart")
	h.requireCondition(t, "cart", "DataMissing", v1.ConditionFalse, "Restored")
	require.Equal(t, before+1, h.creates(), "the worker that ran on the empty volume is replaced")
}
