package v1alpha1

import "time"

// ConditionStatus is the state of a condition: True, False, or Unknown.
type ConditionStatus string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// ConditionType is an open string type identifying a condition.
// Well-known types (Ready, Scheduled, RouteConfigured, …) are defined by
// the feature ADRs that raise them.
type ConditionType string

// Condition represents a single observed condition on a resource's status.
type Condition struct {
	Type               ConditionType   `json:"type"`
	Status             ConditionStatus `json:"status"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	LastTransitionTime Timestamp       `json:"lastTransitionTime,omitzero"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
}

// Conditions is an ordered set of conditions.
type Conditions []Condition

// Set upserts a condition by Type. If a condition of the same Type already exists,
// it is replaced in place. LastTransitionTime is advanced only when the Status actually
// changes — the status-bookkeeping contract every controller relies on.
func (cs *Conditions) Set(c Condition) {
	for i, existing := range *cs {
		if existing.Type == c.Type {
			if existing.Status != c.Status {
				c.LastTransitionTime = NewTimestamp(time.Now())
			} else {
				c.LastTransitionTime = existing.LastTransitionTime
			}
			(*cs)[i] = c
			return
		}
	}
	c.LastTransitionTime = NewTimestamp(time.Now())
	*cs = append(*cs, c)
}

// Get returns the condition with the given Type, and whether it was found.
func (cs Conditions) Get(t ConditionType) (Condition, bool) {
	for _, c := range cs {
		if c.Type == t {
			return c, true
		}
	}
	return Condition{}, false
}

// Status is the shared status base embedded by kinds with observed state.
// Pure-data/policy kinds (ConfigMap, Secret, Grant, EgressPolicy) do not have status.
type Status struct {
	Phase              Phase      `json:"phase,omitempty"`
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	Conditions         Conditions `json:"conditions,omitempty"`
}
