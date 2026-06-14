package v1alpha1

import "github.com/green-0-rabbit/funcd/api/fault"

// Phase represents the lifecycle phase of a resource per the blueprint resource state
// machine. Reconciled by ADR-0003 from the ADR-0002 scaffold placeholders.
type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseDeploying   Phase = "Deploying"
	PhaseReady       Phase = "Ready"
	PhaseIdle        Phase = "Idle"
	PhaseDegraded    Phase = "Degraded"
	PhaseFailed      Phase = "Failed"
	PhaseTerminating Phase = "Terminating"
)

// String returns the string representation of the Phase.
func (p Phase) String() string { return string(p) }

// Validate checks that the Phase is a known value.
func (p Phase) Validate() error {
	switch p {
	case PhasePending, PhaseDeploying, PhaseReady, PhaseIdle, PhaseDegraded, PhaseFailed, PhaseTerminating:
		return nil
	default:
		return fault.Invalidf("Phase.Validate", "unknown phase %q", p)
	}
}

// IsTerminal reports whether the phase is terminal (no further transitions expected).
// Only PhaseTerminating is terminal — it is the sole state with no live successor.
func (p Phase) IsTerminal() bool {
	return p == PhaseTerminating
}
