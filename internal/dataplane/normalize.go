package dataplane

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// maxNormalizeBytes bounds the buffered read of an external invoke body during CloudEvent
// normalization (ADR-0134). Normalization buffers the body (the proxy path otherwise streams), and
// the optional edge body cap (internal/edge/limit) may be off, so this is an independent ceiling —
// invoke `data` is small JSON; a body over this is rejected 413 by the caller.
const maxNormalizeBytes = 1 << 20 // 1 MiB

// bodilessUpgrade reports whether r is an HTTP/1.1 upgrade by httputil.ReverseProxy's rule (Connection
// carries the "upgrade" token and Upgrade is non-empty) that carries no body (ContentLength 0; chunked is -1).
// Such a request skips ADR-0134 normalization (ADR-0181).
func bodilessUpgrade(r *http.Request) bool {
	return r.ContentLength == 0 &&
		httpguts.HeaderValuesContainsToken(r.Header["Connection"], "upgrade") &&
		r.Header.Get("Upgrade") != ""
}

// cloudEventEnvelope is the CloudEvents v1.0 envelope the edge builds around a plain invoke body so
// the shim always receives a well-formed envelope for an external JSON invoke.
type cloudEventEnvelope struct {
	SpecVersion string          `json:"specversion"`
	Type        string          `json:"type"`
	Source      string          `json:"source"`
	ID          string          `json:"id"`
	Data        json.RawMessage `json:"data"`
}

// newInvokeID returns a random 16-hex event id (crypto/rand).
func newInvokeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// normalizeInvokeBody returns the CloudEvents v1.0 envelope bytes to forward to the shim for an
// EXTERNAL invoke of ns/name, given the raw request body (ADR-0134). It wraps plain data, passes an
// already-formed envelope through unchanged, and returns ok=false when the body is not valid JSON so
// the caller forwards it verbatim for the shim to reject with a clean 400. newID supplies the event
// id (injected so tests are deterministic).
func normalizeInvokeBody(ns v1.NamespaceName, name v1.ObjectName, body []byte, newID func() string) (envelope []byte, ok bool) {
	// Empty body → an envelope carrying data:null; a no-input function is invoked with no body.
	if len(bytes.TrimSpace(body)) == 0 {
		return wrapEnvelope(ns, name, json.RawMessage("null"), newID), true
	}
	// Must be valid JSON to normalize; otherwise forward as-is (the shim returns 400).
	if !json.Valid(body) {
		return nil, false
	}
	// Already an envelope? A JSON object with a top-level "specversion" or "data" key → passthrough.
	// Unmarshalling into a map succeeds (and yields non-nil) only for a JSON object; `null`, arrays,
	// and scalars fall through to the wrap branch.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err == nil && obj != nil {
		if _, has := obj["specversion"]; has {
			return body, true
		}
		if _, has := obj["data"]; has {
			return body, true
		}
	}
	// Any other valid JSON value → wrap it as the event data.
	return wrapEnvelope(ns, name, json.RawMessage(body), newID), true
}

// HTTPEnvelope returns body as the CloudEvents v1.0 envelope for a call to an http Function (ADR-0243 Decision 3): it
// wraps every JSON body, even one with a top-level "specversion" or "data" key that ADR-0134 passes through for event
// callers, so the shim's adapter unwraps it without ambiguity. An empty body carries data:null, and a body that is not
// JSON, or not UTF-8, which json.Valid lets through and the adapter would not decode, passes unchanged.
func HTTPEnvelope(ns v1.NamespaceName, name v1.ObjectName, body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return wrapEnvelope(ns, name, json.RawMessage("null"), newInvokeID)
	}
	if !json.Valid(body) || !utf8.Valid(body) {
		return body
	}
	return wrapEnvelope(ns, name, json.RawMessage(body), newInvokeID)
}

// InvokeEnvelope returns body as the CloudEvents v1.0 envelope for an invoke of ns/name under the
// ADR-0134 rule, or body unchanged when it is not valid JSON. The edge normalizes only external traffic,
// so an internal producer (the fn-to-fn broker, ADR-0064) uses this to emit its envelope.
func InvokeEnvelope(ns v1.NamespaceName, name v1.ObjectName, body []byte) []byte {
	if env, ok := normalizeInvokeBody(ns, name, body, newInvokeID); ok {
		return env
	}
	return body
}

// wrapEnvelope builds the v1.0 envelope around data. data must already be valid JSON.
func wrapEnvelope(ns v1.NamespaceName, name v1.ObjectName, data json.RawMessage, newID func() string) []byte {
	env := cloudEventEnvelope{
		SpecVersion: "1.0",
		Type:        "io.funcd.invoke",
		Source:      "funcd://" + string(ns) + "/function/" + string(name),
		ID:          newID(),
		Data:        data,
	}
	b, _ := json.Marshal(env)
	return b
}
