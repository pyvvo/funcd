package v1alpha1

import (
	"time"

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

// EventSourceSpec is a kind-keyed source hosting named events (ADR-0108, F72): exactly one source-kind
// pointer is non-nil (the source kind), each carrying a list of named events. A firing publishes a named
// CloudEvent that the F69 Sensor (ADR-0109) binds to actions — the source no longer binds a function.
type EventSourceSpec struct {
	// Timer is the timer source kind (V1): named events, each ticking on its own interval. The webhook
	// source kind is a named follow-on (it needs an eventing-ingress gateway decision).
	Timer *TimerSource `json:"timer,omitempty"`
	// Blob is the object-store source kind (ADR-0119, F83): named events that fire on object-created under
	// a watched Bucket prefix, detected by a poll watcher over the ADR-0007 blob.Bucket List seam.
	Blob *BlobSource `json:"blob,omitempty"`
}

// TimerSource hosts the timer kind's named events (ADR-0108).
type TimerSource struct {
	Events []TimerEvent `json:"events"` // ≥1; unique names
}

// TimerEvent is one named timer event: a DNS-1123 name + its own interval. Each fires independently and
// publishes a named CloudEvent (source=<eventsource> URI, type=<name>).
type TimerEvent struct {
	Name ObjectName `json:"name"`
	// Interval is the tick period in int64 nanoseconds, bounded 100ms ≤ ≤ 24h (ADR-0023): the floor bars a
	// μs/ns fire-storm, the ceiling bars an unbounded one. The tag literals are the sole schema source; the
	// bounds are re-checked in Validate (which also runs at store.Create, bypassing the huma edge).
	Interval time.Duration `json:"interval" minimum:"100000000" maximum:"86400000000000"` // 100ms–24h
}

// BlobSource hosts the blob kind's named events over one Bucket (ADR-0119, F83): a poll watcher lists the
// Bucket's prefixes and fires a named CloudEvent per new object.
type BlobSource struct {
	Bucket ObjectName  `json:"bucket"` // the ADR-0080 Bucket resource to watch (this namespace)
	Events []BlobEvent `json:"events"` // ≥1; unique names
}

// BlobEvent is one named object-store event: a DNS-1123 name, a key prefix, and the object lifecycle events
// it fires on (ADR-0119).
type BlobEvent struct {
	Name   ObjectName      `json:"name"`
	Prefix string          `json:"prefix,omitempty"` // only objects under this key prefix fire ("" = whole bucket)
	On     []BlobEventType `json:"on,omitempty"`     // default [Created] (set in normalize); only Created supported in V1
}

// BlobEventType is an object lifecycle event. V1: Created only (Removed/Updated are a named follow-on).
type BlobEventType string

// BlobCreated is the object-created lifecycle event — the only type supported in V1 (ADR-0119).
const BlobCreated BlobEventType = "Created"

// EventSourceStatus holds the observed state.
type EventSourceStatus struct {
	Status `json:",inline"`
}

// Normalize fills declared-but-empty defaults that JSON Schema cannot (ADR-0119): each blob event's empty
// `on` defaults to [Created]. It is a decode/normalize step — pure defaulting, NOT validation — kept out of
// Validate so Validate stays non-mutating (ADR-0108). Idempotent; a nil/timer spec is a no-op.
func (es *EventSource) Normalize() {
	if es.Spec.Blob == nil {
		return
	}
	for i := range es.Spec.Blob.Events {
		if len(es.Spec.Blob.Events[i].On) == 0 {
			es.Spec.Blob.Events[i].On = []BlobEventType{BlobCreated}
		}
	}
}

// GroupVersionKind returns the constant GVK for EventSource.
func (es *EventSource) GroupVersionKind() GroupVersionKind { return KindEventSource.GVK() }

// Validate enforces the v2 kind-union rules JSON Schema can't express (ADR-0108/ADR-0048): exactly one
// source kind set; each kind's events non-empty with unique DNS-1123 names and in-bounds intervals. The
// removed v1 `type:`/`function:` keys are rejected at the schema edge (additionalProperties:false → 422).
func (es *EventSource) Validate() error {
	if err := validateMeta(es.TypeMeta, &es.ObjectMeta, KindEventSource); err != nil {
		return err
	}
	const op = "EventSource.Validate"
	kinds := 0
	if es.Spec.Timer != nil {
		kinds++
	}
	if es.Spec.Blob != nil {
		kinds++
	}
	if kinds != 1 {
		return fault.Invalidf(op, "exactly one source kind must be set (spec.timer|spec.blob), got %d", kinds)
	}
	if es.Spec.Blob != nil {
		if err := es.Spec.Blob.validate(op); err != nil {
			return err
		}
	}
	if es.Spec.Timer != nil {
		if len(es.Spec.Timer.Events) == 0 {
			return fault.Invalidf(op, "spec.timer.events must list at least one event")
		}
		seen := make(map[ObjectName]bool, len(es.Spec.Timer.Events))
		for i := range es.Spec.Timer.Events {
			ev := &es.Spec.Timer.Events[i]
			if !dnsLabel.MatchString(string(ev.Name)) {
				return fault.Invalidf(op, "spec.timer.events[%d].name %q is not a valid DNS-1123 label", i, ev.Name)
			}
			if seen[ev.Name] {
				return fault.Invalidf(op, "duplicate event name %q under spec.timer", ev.Name)
			}
			seen[ev.Name] = true
			if ev.Interval < 100*time.Millisecond || ev.Interval > 24*time.Hour {
				return fault.Invalidf(op, "spec.timer.events[%d].interval %s is out of bounds (100ms–24h)", i, ev.Interval)
			}
		}
	}
	return nil
}

// validate enforces the blob source kind's rules JSON Schema can't express (ADR-0119): a DNS-1123 `bucket`,
// ≥1 event with unique DNS-1123 names, and each event's `on` (when set) containing only Created in V1. It is
// PURE — an empty `on` is left untouched here (Normalize defaults it to [Created]); Validate never mutates.
func (bs *BlobSource) validate(op string) error {
	if !dnsLabel.MatchString(string(bs.Bucket)) {
		return fault.Invalidf(op, "spec.blob.bucket %q is not a valid DNS-1123 label", bs.Bucket)
	}
	if len(bs.Events) == 0 {
		return fault.Invalidf(op, "spec.blob.events must list at least one event")
	}
	seen := make(map[ObjectName]bool, len(bs.Events))
	for i := range bs.Events {
		ev := &bs.Events[i]
		if !dnsLabel.MatchString(string(ev.Name)) {
			return fault.Invalidf(op, "spec.blob.events[%d].name %q is not a valid DNS-1123 label", i, ev.Name)
		}
		if seen[ev.Name] {
			return fault.Invalidf(op, "duplicate event name %q under spec.blob", ev.Name)
		}
		seen[ev.Name] = true
		for _, t := range ev.On {
			if t != BlobCreated {
				return fault.Invalidf(op, "spec.blob.events[%d].on %q is unsupported in V1 (only %q)", i, t, BlobCreated)
			}
		}
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (es *EventSource) GetStatus() *Status { return &es.Status.Status }
