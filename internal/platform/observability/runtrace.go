package observability

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// NewTraceContext returns a fresh W3C trace context (ADR-0102): a 16-byte trace ID and an 8-byte span ID, lowercase
// hex. A workflow run mints one when it starts and a JobRun when it runs, and every call they make joins it. On the
// near-impossible crypto/rand error both are empty: the caller goes on with no traceparent, never failing the run.
func NewTraceContext() (traceID, spanID string) {
	var t [16]byte
	if _, err := rand.Read(t[:]); err != nil {
		return "", ""
	}
	s := NewSpanID()
	if s == "" {
		return "", ""
	}
	return hex.EncodeToString(t[:]), s
}

// NewSpanID returns a fresh 8-byte span ID as 16 lowercase hex characters (ADR-0105), "" on a crypto/rand error.
func NewSpanID() string {
	var s [8]byte
	if _, err := rand.Read(s[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(s[:])
}

// MaxStatusError bounds an error that a status mirrors (ADR-0100): its first line, cut at this many bytes, so a
// pathological stack trace never bloats a stored object. The whole error lives in the call's span and logs.
const MaxStatusError = 512

// StatusError returns the first line of s cut at MaxStatusError bytes, with "…" appended when it was cut.
func StatusError(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > MaxStatusError {
		return s[:MaxStatusError] + "…"
	}
	return s
}
