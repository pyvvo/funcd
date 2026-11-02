package function_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/store"
)

// scenario: past-admission-blocked — a Function written past admission, such as by a restore, with a resources value
// that does not parse, or with resources and pooling.worker, gets no worker; the reconcile fails naming the field.
func TestScenarioPastAdmissionBlocked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*v1.Function)
	}{
		{"lots", func(fn *v1.Function) { fn.Spec.Resources.CPU = "lots" }},
		{"pooled", func(fn *v1.Function) {
			fn.Spec.Resources.Memory = "256Mi"
			fn.Spec.Pooling.Worker = "w1"
		}},
	} {
		h := newShimHarness(t, http.StatusOK, false, withNodePool)
		h.create(t, tc.name, tc.change)
		fn := h.getFn(t, tc.name)
		tc.change(fn)
		raw, err := json.Marshal(fn)
		require.NoError(t, err)
		err = h.eng.Update(context.Background(), func(tx store.Txn) error {
			return tx.Put(fn.GroupVersionKind().String(), "default/"+tc.name, raw)
		})
		require.NoError(t, err)
		h.reconcile(t, tc.name)

		fn = h.getFn(t, tc.name)
		require.Equal(t, v1.PhaseFailed, fn.Status.Phase, "%s", tc.name)
		ready, ok := fn.Status.Conditions.Get("Ready")
		require.True(t, ok, "%s: Ready condition is set", tc.name)
		require.Equal(t, "ShapeInvalid", ready.Reason, "%s", tc.name)
		shape, ok := fn.Status.Conditions.Get("ShapeValid")
		require.True(t, ok, "%s: ShapeValid condition is set", tc.name)
		require.Contains(t, shape.Message, "spec.resources", "%s", tc.name)
		require.Zero(t, h.rt.createAttempts(), "%s: no worker is created", tc.name)
	}
}
