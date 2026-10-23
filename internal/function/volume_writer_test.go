package function_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestLocalVolumeSwitchEndsWithOneWriter: a redeploy of a Function with a local volume runs the new revision's worker
// beside the old one, both on the volume, as ADR-0262 Decision 6 allows; once the new revision serves, the old one
// stops and the volume has one writer again.
// oracle: docs/design/volume/one-writer.fizz#AtMostOneWriterOutsideASwitch
// oracle: docs/design/volume/one-writer.fizz#SwitchesEnd
func TestLocalVolumeSwitchEndsWithOneWriter(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
		m, err := volume.New(volume.Options{Root: t.TempDir(), CapacityBytes: 2 << 20})
		require.NoError(t, err)
		d.Volumes = m
	})
	running := func(rev v1.ObjectName) int {
		n := 0
		for _, s := range h.rt.revisionStates("cart")[rev] {
			if s == runtime.StateRunning {
				n++
			}
		}
		return n
	}
	h.create(t, "cart", func(fn *v1.Function) {
		fn.Spec.Volumes = []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}}
	})
	h.reconcile(t, "cart")
	h.reconcile(t, "cart")
	require.Equal(t, 1, running("cart-1"))
	h.apply(t, "cart", func(fn *v1.Function) { fn.Spec.Handler = "handleV2" })
	h.reconcile(t, "cart")
	require.Equal(t, 1, running("cart-1"), "the old revision serves during the switch")
	require.Equal(t, 1, running("cart-2"), "the new revision boots beside it")
	require.Eventually(t, func() bool {
		settle()
		h.reconcile(t, "cart")
		return running("cart-1") == 0
	}, 5*time.Second, 10*time.Millisecond, "the old revision stops once the new one serves")
	require.Equal(t, 1, running("cart-2"))
}
