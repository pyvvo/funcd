// Package eventing implements funcd eventing (ADR-0023, reshaped by ADR-0108): a typed CloudEvents v1.0
// envelope, an EventSource reconciler that registers a `timer:` source's NAMED events, and — on each
// firing — PUBLISHES a named CloudEvent onto a Publisher seam (the in-process Fanout) the F69 Sensor
// subscribes to. It owns only KindEventSource (one-reconciler-per-gvk, ADR-0015); ticking is a side Run
// loop, not the reconciler. The action side (invoke a function / start a workflow) is the Sensor
// (ADR-0109); the webhook source kind and bus-backed delivery are documented deferrals.
package eventing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const specVersion = "1.0"

// sourceURIPrefix is the canonical CloudEvent `source` form: funcd://<ns>/eventsource/<name>. NewNamedEvent
// writes it; the Fanout parses it back to route (ADR-0108) — the single canonical name↔URI mapping.
const sourceURIScheme = "funcd://"

// CloudEvent is a CloudEvents v1.0 envelope serialized in the JSON event format. Hand-defined (no
// cloudevents/sdk-go); a later SDK swap stays behind this type.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"` // funcd://<ns>/eventsource/<name>
	Type            string          `json:"type"`   // the event name (ADR-0108)
	Time            v1.Timestamp    `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

// NewNamedEvent builds a well-formed named CloudEvent for one event of an EventSource (ADR-0108): the
// `source` URI carries the namespace + source name, `type` carries the event name. A timer event has an
// empty `{}` payload. The id is a fresh random hex string (unique per call).
func NewNamedEvent(ns v1.NamespaceName, source, event v1.ObjectName) (CloudEvent, error) {
	id, err := randomID()
	if err != nil {
		return CloudEvent{}, err
	}
	return CloudEvent{
		SpecVersion:     specVersion,
		ID:              id,
		Source:          SourceURI(ns, source),
		Type:            string(event),
		Time:            v1.NewTimestamp(time.Now()),
		DataContentType: "application/json",
		Data:            json.RawMessage("{}"),
	}, nil
}

// BlobEventData is the `data` payload of a blob CloudEvent (ADR-0119, F83): what landed under a watched
// Bucket prefix, for a Sensor to project (e.g. `${{ event.data.key }}`). Version is a (ModTime,Size)
// fingerprint of the observed object version — NOT a content ETag (a `data.etag` from blob.Attributes.MD5,
// ADR-0159, is ADR-0119's follow-up).
type BlobEventData struct {
	Bucket  string       `json:"bucket"`
	Key     string       `json:"key"`
	Size    int64        `json:"size"`
	Version string       `json:"version"`
	Time    v1.Timestamp `json:"time"`
}

// NewBlobEvent builds a named CloudEvent for a landed object (ADR-0119): the same envelope as
// NewNamedEvent (source URI carries ns+source, type carries the event name), but `data` is the object
// descriptor instead of the timer's empty `{}`. It flows the unchanged Fanout → Sensor path.
func NewBlobEvent(ns v1.NamespaceName, source, event v1.ObjectName, d BlobEventData) (CloudEvent, error) {
	id, err := randomID()
	if err != nil {
		return CloudEvent{}, err
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return CloudEvent{}, fault.Internalf("eventing.NewBlobEvent", "marshal blob event data: %v", err)
	}
	return CloudEvent{
		SpecVersion:     specVersion,
		ID:              id,
		Source:          SourceURI(ns, source),
		Type:            string(event),
		Time:            v1.NewTimestamp(time.Now()),
		DataContentType: "application/json",
		Data:            json.RawMessage(payload),
	}, nil
}

// SourceURI is the canonical CloudEvent `source` for an EventSource (ADR-0108).
func SourceURI(ns v1.NamespaceName, source v1.ObjectName) string {
	return fmt.Sprintf("%s%s/eventsource/%s", sourceURIScheme, ns, source)
}

// ParseSourceURI is the inverse of SourceURI: it recovers (namespace, source) from a CloudEvent's
// `source` field so a subscriber can route by structured key (ADR-0108). ok=false for a foreign URI.
func ParseSourceURI(uri string) (ns v1.NamespaceName, source v1.ObjectName, ok bool) {
	rest, found := strings.CutPrefix(uri, sourceURIScheme)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/") // <ns>/eventsource/<name>
	if len(parts) != 3 || parts[1] != "eventsource" || parts[0] == "" || parts[2] == "" {
		return "", "", false
	}
	return v1.NamespaceName(parts[0]), v1.ObjectName(parts[2]), true
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Internalf("eventing.NewNamedEvent", "generate event id: %v", err)
	}
	return hex.EncodeToString(b[:]), nil
}
