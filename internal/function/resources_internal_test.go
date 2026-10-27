package function

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/envresolve"
	"github.com/pyvvo/funcd/internal/runtime"
)

// scenario: function-limits-applied — a Function's spec.resources becomes its solo worker's limits, in container and
// process mode alike; the containerd driver turns them into the worker's cgroup (just test-linux and the lane).
func TestScenarioFunctionLimitsApplied(t *testing.T) {
	t.Parallel()
	fn := sampleFn()
	fn.Spec.Resources = v1.ResourceSpec{CPU: "0.5", Memory: "256Mi"}
	for _, r := range []*Reconciler{newContainerReconciler(t), newShimReconciler(t, nil)} {
		spec := mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
		require.Equal(t, runtime.Limits{CPUs: 0.5, MemoryBytes: 256 << 20}, spec.Limits)
	}
}

// scenario: function-default-limits — functions.defaultResources fills each field a solo Function leaves empty; with
// no default, the worker has no limit.
func TestScenarioFunctionDefaultLimits(t *testing.T) {
	t.Parallel()
	r := newContainerReconciler(t)
	r.defaultResources = v1.ResourceSpec{CPU: "1", Memory: "512Mi"}
	fn := sampleFn()
	limits := func() runtime.Limits {
		return mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil).Limits
	}
	require.Equal(t, runtime.Limits{CPUs: 1, MemoryBytes: 512 << 20}, limits())
	fn.Spec.Resources.CPU = "0.25"
	require.Equal(t, runtime.Limits{CPUs: 0.25, MemoryBytes: 512 << 20}, limits(), "the spec's cpu, the default's memory")
	r.defaultResources = v1.ResourceSpec{}
	fn.Spec.Resources = v1.ResourceSpec{}
	require.Equal(t, runtime.Limits{}, limits())
}

// scenario: limits-not-enforced-logged — under the process runtime, which is no LimitEnforcer, the worker runs and the
// reconciler logs once per Function that its limits are not enforced.
func TestScenarioLimitsNotEnforcedLogged(t *testing.T) {
	t.Parallel()
	r := newShimReconciler(t, nil)
	var buf bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&buf, nil))
	fn := sampleFn()
	fn.Spec.Resources.Memory = "256Mi"
	for range 3 {
		mustWorkerSpec(t, r, fn, "/art/app.mjs", envresolve.Resolved{}, nil)
	}
	require.Equal(t, 1, strings.Count(buf.String(), "does not enforce"))
}
