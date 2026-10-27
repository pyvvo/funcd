package function

import (
	"cmp"
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// The reasons of a Function whose spec.resources admission would refuse, written past admission (ADR-0291 Decision 4).
const (
	reasonResourcesInvalid     = "ResourcesInvalid"
	reasonResourcesWithPooling = "ResourcesWithPooling"
)

// resourcesGate fails a Function that admission would refuse for its spec.resources (ADR-0291 Decision 4): written
// past admission, such as by a restore, it gets no worker.
func resourcesGate(fn *v1.Function) *gateFailure {
	if err := fn.Spec.Resources.Validate("function.resourcesGate", "spec.resources"); err != nil {
		msg := withoutOp(err)
		return &gateFailure{reason: reasonResourcesInvalid, message: msg, readyMessage: msg, phase: v1.PhaseFailed}
	}
	if fn.Spec.PooledWithResources() {
		msg := "spec.resources cannot be set with spec.pooling.worker: a pool's heap cap is pool-wide"
		return &gateFailure{reason: reasonResourcesWithPooling, message: msg, readyMessage: msg, phase: v1.PhaseFailed}
	}
	return nil
}

// limitsOf is a solo worker's limits (ADR-0291 Decisions 2 and 3): spec.resources, each empty field from
// functions.defaultResources; it logs once when the runtime does not enforce them.
func (r *Reconciler) limitsOf(ctx context.Context, fn *v1.Function) runtime.Limits {
	rs := fn.Spec.Resources
	l := runtime.LimitsFrom(cmp.Or(rs.CPU, r.defaultResources.CPU), cmp.Or(rs.Memory, r.defaultResources.Memory))
	r.unenforced.Warn(ctx, r.logger, r.runtime, string(fn.Namespace)+"/"+string(fn.Name), l)
	return l
}
