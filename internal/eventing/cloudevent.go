// Package eventing implements V1 eventing (ADR-0023): a typed CloudEvents v1.0
// envelope, an EventSource reconciler that registers timer sources, and an HTTP
// invoker that fires a CloudEvent at a function's ready upstream (via
// activator.Endpoints) and records an Invocation. It owns only KindEventSource
// (one-reconciler-per-gvk, ADR-0015); ticking is a side Run loop, not the
// reconciler. HTTP-trigger normalization (the runtime shim, P-S), cron schedules,
// and bus/async eventing are documented deferrals.
package eventing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

const (
	specVersion    = "1.0"
	timerEventType = "io.funcd.timer.tick"
	contentTypeCE  = "application/cloudevents+json"
)

// CloudEvent is a CloudEvents v1.0 envelope serialized in the JSON event format.
// Hand-defined (no cloudevents/sdk-go); a later SDK swap stays behind this type.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

// NewTimerEvent builds a well-formed timer-tick CloudEvent originating from the
// named EventSource. The id is a fresh random hex string (unique per call).
func NewTimerEvent(ns v1.NamespaceName, source v1.ObjectName) (CloudEvent, error) {
	id, err := randomID()
	if err != nil {
		return CloudEvent{}, err
	}
	return CloudEvent{
		SpecVersion: specVersion,
		ID:          id,
		Source:      fmt.Sprintf("funcd://%s/eventsource/%s", ns, source),
		Type:        timerEventType,
		Time:        time.Now().UTC(),
	}, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Internalf("eventing.NewTimerEvent", "generate event id: %v", err)
	}
	return hex.EncodeToString(b[:]), nil
}
