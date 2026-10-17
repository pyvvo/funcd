package activator

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ReasonTargetServesHTTP names a call refused because its target serves HTTP, which only the edge calls (ADR-0243).
const ReasonTargetServesHTTP = "TargetServesHTTP"

// ProtocolFunc reads how a Function is called (ADR-0243).
type ProtocolFunc func(ctx context.Context, fn FunctionRef) (v1.FunctionProtocol, error)

// RefuseHTTP refuses a call from a Sensor, an App hook or a workflow step to a Function that serves HTTP, before any
// request reaches it (ADR-0243 Decision 5). A nil protocol, or a Function it cannot read, refuses nothing: the call
// then fails, or not, as it would.
func RefuseHTTP(ctx context.Context, protocol ProtocolFunc, fn FunctionRef, op string) error {
	if protocol == nil {
		return nil
	}
	if p, err := protocol(ctx, fn); err == nil && p == v1.ProtocolHTTP {
		return fault.Conflictf(op, "%s: %s/%s serves HTTP and only the edge calls it; call it through a Function that uses context.fetch",
			ReasonTargetServesHTTP, fn.Namespace, fn.Name)
	}
	return nil
}
