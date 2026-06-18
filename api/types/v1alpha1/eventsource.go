package v1alpha1

import (
	"time"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// EventSource is a namespaced resource that configures an event trigger source.
// Status-bearing. Behavioral spec fields (source config) owned by F16.
type EventSource struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       EventSourceSpec   `json:"spec"`
	Status     EventSourceStatus `json:"status,omitempty"`
}

// EventSourceSpec holds the desired state. Behavioral fields owned by F16 (ADR-0023).
type EventSourceSpec struct {
	Type     EventSourceType `json:"type,omitempty"`
	Timer    *TimerSpec      `json:"timer,omitempty"`    // set when Type == EventSourceTypeTimer
	Function ObjectName      `json:"function,omitempty"` // the bound function (same namespace)
}

// EventSourceType is the trigger transport (ADR-0023, F16).
type EventSourceType string

const (
	// EventSourceTypeHTTP triggers via the gateway route; normalization is the runtime shim's (P-S).
	EventSourceTypeHTTP EventSourceType = "http"
	// EventSourceTypeTimer fires the bound function on an interval (V1; cron is a follow-up).
	EventSourceTypeTimer EventSourceType = "timer"
)

// Schema carries EventSourceType's enum constraint into the generated OpenAPI (ADR-0048).
func (EventSourceType) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(EventSourceTypeHTTP), string(EventSourceTypeTimer))
}

// TimerSpec configures a timer EventSource (ADR-0023): V1 uses Interval; cron is a follow-up.
type TimerSpec struct {
	// Interval is the timer period in int64 nanoseconds, bounded 100ms ≤ ≤ 24h: the floor bars
	// a pathological μs/ns fire-storm while still allowing sub-second timers; the ceiling bars an
	// unbounded one. The tag literals are the sole source (a struct tag can't reference a const);
	// the human values live here in the comment — ADR-0048.
	Interval time.Duration `json:"interval,omitempty" minimum:"100000000" maximum:"86400000000000"` // 100ms–24h
}

// EventSourceStatus holds the observed state.
type EventSourceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for EventSource.
func (es *EventSource) GroupVersionKind() GroupVersionKind { return KindEventSource.GVK() }

// Validate performs envelope validation, then the cross-field rules JSON Schema can't express
// (ADR-0048): the timer sub-spec present iff type==timer, and the target function set. The
// `type` enum and interval bounds are schema-enforced at the edge (not re-checked here).
func (es *EventSource) Validate() error {
	if err := validateMeta(es.TypeMeta, &es.ObjectMeta, KindEventSource); err != nil {
		return err
	}
	const op = "EventSource.Validate"
	switch es.Spec.Type {
	case EventSourceTypeTimer:
		if es.Spec.Timer == nil {
			return fault.Invalidf(op, "spec.timer is required when spec.type is %q", EventSourceTypeTimer)
		}
	case EventSourceTypeHTTP:
		if es.Spec.Timer != nil {
			return fault.Invalidf(op, "spec.timer must be empty when spec.type is %q", EventSourceTypeHTTP)
		}
	default:
		return fault.Invalidf(op, "unknown event source type %q", es.Spec.Type)
	}
	if es.Spec.Function == "" {
		return fault.Invalidf(op, "spec.function (the target) must be set")
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (es *EventSource) GetStatus() *Status { return &es.Status.Status }
