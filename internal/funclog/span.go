package funclog

import "time"

// SignalTraces is the traces signal (ADR-0101). It joins SignalLogs on the wire's "funcd.signal"
// discriminator: a line tagged "traces" decodes to a Span, anything else (including an absent tag)
// to an Entry. The transport, per-instance drain, Resource identity, and segment-seal machinery are
// reused from ADR-0081; only the record type (Span), the marshaler (ptrace), and the blob prefix
// (traces/) differ.
const SignalTraces Signal = "traces"

// SpanKind is the typed OTel span kind carried on the wire (ADR-0002: typed over magic strings).
type SpanKind string

const (
	SpanServer   SpanKind = "SERVER"   // the auto invocation span (ADR-0101)
	SpanInternal SpanKind = "INTERNAL" // reserved for later SDK-forwarded spans
	SpanClient   SpanKind = "CLIENT"   // reserved (fn→fn call span, a follow-on)
)

// valid reports whether k is a known span kind.
func (k SpanKind) valid() bool {
	switch k {
	case SpanServer, SpanInternal, SpanClient:
		return true
	default:
		return false
	}
}

// SpanStatus is the typed OTel span status.
type SpanStatus string

const (
	StatusUnset SpanStatus = "UNSET"
	StatusOk    SpanStatus = "OK"
	StatusError SpanStatus = "ERROR"
)

// valid reports whether s is a known span status.
func (s SpanStatus) valid() bool {
	switch s {
	case StatusUnset, StatusOk, StatusError:
		return true
	default:
		return false
	}
}

// Span is one decoded TRACE record from a function instance, pre-OTLP (trace-specific: start/end/
// parent/kind/status, NOT body/severity). The sibling to Entry behind the same Reader/Sink seam —
// neither reuses the other (ADR-0081's seam: "Entry does NOT generalize to spans").
type Span struct {
	TraceID    string            // hex32; adopted from traceparent or minted (root)
	SpanID     string            // hex16; minted per invocation
	ParentID   string            // hex16; the traceparent span-id, empty for a root
	Name       string            // the function name (FUNCD_FUNCTION) or "invoke"
	Kind       SpanKind          // SERVER for the auto invocation span
	Start      time.Time         // invocation start (epoch nanos on the wire)
	End        time.Time         // invocation end
	Status     SpanStatus        // OK on return, ERROR on throw/output-mismatch
	StatusMsg  string            // the error message when Status == ERROR
	Attrs      map[string]string // structured attributes
	Invocation string            // the per-invocation id (== the log lines' inv for correlation)
}
