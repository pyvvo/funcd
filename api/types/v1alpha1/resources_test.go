package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

func TestResourceSpecValidate(t *testing.T) {
	t.Parallel()
	for _, ok := range []v1.ResourceSpec{{}, {CPU: "0.01"}, {CPU: "2"}, {Memory: "64Mi"}, {Memory: "1GiB"}, {Memory: "1048576"}} {
		require.NoError(t, ok.Validate("op", "spec.resources"), "%+v", ok)
	}
	for _, bad := range []v1.ResourceSpec{{CPU: "0"}, {CPU: "0.001"}, {CPU: "NaN"}, {CPU: "Inf"}, {CPU: "-1"}, {CPU: "lots"},
		{Memory: "0"}, {Memory: "1G"}, {Memory: "256MB"}, {Memory: "lots"}} {
		err := bad.Validate("op", "spec.resources")
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%+v", bad)
		require.Contains(t, err.Error(), "spec.resources.", "%+v", bad)
	}
}

// scenario: invalid-resources-refused — admission refuses a Function whose resources do not parse, or that sets
// resources with pooling.worker, and names the field.
func TestScenarioInvalidResourcesRefused(t *testing.T) {
	t.Parallel()
	fn := func(change func(*v1.FunctionSpec)) error {
		f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
		f.Name, f.Namespace, f.ResourceGroup = "api", "default", "rg1"
		f.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://x:1"}
		change(&f.Spec)
		return f.Validate()
	}
	require.NoError(t, fn(func(s *v1.FunctionSpec) { s.Resources = v1.ResourceSpec{CPU: "0.5", Memory: "256Mi"} }))
	require.NoError(t, fn(func(s *v1.FunctionSpec) { s.Pooling.Worker = "w1" }), "pooling alone is fine")

	err := fn(func(s *v1.FunctionSpec) { s.Resources.CPU = "lots" })
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "spec.resources.cpu")
	err = fn(func(s *v1.FunctionSpec) { s.Resources.Memory = "256Mi"; s.Pooling.Worker = "w1" })
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), "spec.resources cannot be set with spec.pooling.worker")
}
