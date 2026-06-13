package v1alpha1

import "github.com/green-0-rabbit/funcd/api/fault"

// Phase represents the lifecycle phase of a resource.
type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseReady       Phase = "Ready"
	PhaseFailed      Phase = "Failed"
	PhaseDeleted     Phase = "Deleted"
	PhaseScaling     Phase = "Scaling"
	PhaseReconciling Phase = "Reconciling"
)

// String returns the string representation of the Phase.
func (p Phase) String() string { return string(p) }

// Validate checks that the Phase is a known value.
func (p Phase) Validate() error {
	switch p {
	case PhasePending, PhaseReady, PhaseFailed, PhaseDeleted, PhaseScaling, PhaseReconciling:
		return nil
	default:
		return fault.Invalidf("Phase.Validate", "unknown phase %q", p)
	}
}

// IsTerminal reports whether the phase is terminal (no further transitions expected).
func (p Phase) IsTerminal() bool {
	return p == PhaseFailed || p == PhaseDeleted
}
