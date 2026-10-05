package function

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// TestAsleepDesiredReplicas is ADR-0192's contract: an asleep scale-to-zero Function wants no worker, whatever its
// placement (ADR-0193); ADR-0169's max(1, replicas) and the static floor hold otherwise, and minReplicas: 1 is unchanged.
func TestAsleepDesiredReplicas(t *testing.T) {
	t.Parallel()
	r := &Reconciler{poolShimCommand: []string{"node", "pool.mjs"}}
	fnWith := func(phase v1.Phase, asleep bool, minReplicas int, worker string) *v1.Function {
		fn := &v1.Function{}
		fn.Spec.Runtime, fn.Spec.Replicas = "nodejs22", 2
		fn.Spec.Scaling.MinReplicas = minReplicas
		fn.Spec.Pooling.Worker = worker
		fn.Status.Phase = phase
		if asleep {
			fn.Status.Conditions.Set(v1.Condition{Type: condAsleep, Status: v1.ConditionTrue, Reason: "ScaledToZero"})
		}
		return fn
	}
	for _, tc := range []struct {
		name   string
		fn     *v1.Function
		asleep bool
		want   int
	}{
		{"idle", fnWith(v1.PhaseIdle, false, 0, ""), true, 0},
		{"pending-asleep", fnWith(v1.PhasePending, true, 0, ""), true, 0},
		{"failed-asleep", fnWith(v1.PhaseFailed, true, 0, ""), true, 0},
		{"failed", fnWith(v1.PhaseFailed, false, 0, ""), false, 2},
		{"pending", fnWith(v1.PhasePending, false, 0, ""), false, 2},
		{"deploying-asleep", fnWith(v1.PhaseDeploying, true, 0, ""), false, 2},
		{"min-replicas-one-failed-asleep", fnWith(v1.PhaseFailed, true, 1, ""), false, 2},
		{"min-replicas-one-idle", fnWith(v1.PhaseIdle, false, 1, ""), false, 2},
		{"pooled-failed-asleep", fnWith(v1.PhaseFailed, true, 0, "shared"), true, 0},
		{"pooled-pending-asleep", fnWith(v1.PhasePending, true, 0, "shared"), true, 0},
		{"pooled-idle", fnWith(v1.PhaseIdle, false, 0, "shared"), true, 0},
	} {
		require.Equal(t, tc.asleep, r.asleep(tc.fn), "%s: asleep", tc.name)
		require.Equal(t, tc.want, r.desiredReplicas(tc.fn), "%s: desiredReplicas", tc.name)
	}
	require.Equal(t, 1, r.desiredReplicas(func() *v1.Function {
		fn := fnWith(v1.PhaseFailed, false, 0, "")
		fn.Spec.Replicas = 0
		return fn
	}()), "a Failed Function that is not asleep keeps max(1, replicas)")
}
