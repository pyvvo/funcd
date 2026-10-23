package function_test

import (
	"testing"

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
