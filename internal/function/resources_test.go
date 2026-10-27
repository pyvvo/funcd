package function_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/store"
)

// pastAdmission is a store whose reads return each Function with change applied: a record a restore wrote past
// admission reads this way, since the store validates only what it writes.
type pastAdmission struct {
	store.Store
	change func(*v1.Function)
}

func (s pastAdmission) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	obj, err := s.Store.Get(ctx, gvk, ns, name)
	if fn, ok := obj.(*v1.Function); ok && err == nil {
		s.change(fn)
	}
	return obj, err
}

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
		h := newShimHarness(t, http.StatusOK, false, withNodePool, func(d *function.Deps) {
			d.Store = pastAdmission{Store: d.Store, change: tc.change}
		})
		h.create(t, tc.name, func(*v1.Function) {})
		_, err := h.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindFunction.GVK(), Namespace: "default",
			Name: v1.ObjectName(tc.name)})

		require.ErrorContains(t, err, "spec.resources", tc.name)
		require.Zero(t, h.rt.createAttempts(), "%s: no worker is created", tc.name)
	}
}
