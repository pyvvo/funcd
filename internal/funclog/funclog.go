// Package funclog captures function telemetry over a freeze-safe side channel and persists it
// OTel-native through the blob.Bucket port (ADR-0081). THIS package builds LOGS (Path A/B). The
// PIPELINE — Reader → batch-per-Resource → Sink → blob.Bucket Put, plus the Resource/identity
// tagging — is signal-generic: traces (F51) and metrics (F52) reuse this seam. The per-signal
// RECORD type (Entry) and the NDJSON wire are log-SPECIFIC; F51/F52 add their own record + builder
// behind the same Reader/Sink (a Signal-discriminated set, not a reused Entry).
//
// The freeze-safe property: nothing buffers durably inside the function — the host reads each
// instance's channel continuously, so a frozen/killed instance pauses the host Pump (a blocking
// Read) rather than losing an already-emitted backlog.
package funclog

import "time"

// Signal is the OTel signal kind, discriminating the record/wire a Reader+Sink carry. Logs now;
// F51/F52 add SignalTraces/SignalMetrics (each with its own record type — Entry does NOT
// generalize to spans/data-points).
type Signal string

// SignalLogs is the logs signal (this package).
const SignalLogs Signal = "logs"

// Severity is the typed OTel severity (ADR-0002: typed over magic strings).
type Severity string

const (
	SevTrace Severity = "TRACE"
	SevDebug Severity = "DEBUG"
	SevInfo  Severity = "INFO"
	SevWarn  Severity = "WARN"
	SevError Severity = "ERROR"
	SevFatal Severity = "FATAL"
)

// valid reports whether s is a known severity.
func (s Severity) valid() bool {
	switch s {
	case SevTrace, SevDebug, SevInfo, SevWarn, SevError, SevFatal:
		return true
	default:
		return false
	}
}

// timeFromNanos converts an epoch-nanoseconds wire timestamp to a time.Time; a zero/absent
// timestamp yields the zero time, which the sink stamps as "now" at seal.
func timeFromNanos(ns int64) time.Time {
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

// Source tags which capture path produced an Entry. The NDJSON wire key "funcd.source" decodes
// to this typed Source.
type Source string

const (
	SourceConsole Source = "console" // Path B structured (Node console.*)
	SourceLogging Source = "logging" // Path B structured (Python logging.Handler)
	SourceStdout  Source = "stdout"  // Path A coarse (INFO)
	SourceStderr  Source = "stderr"  // Path A coarse (ERROR)
)

// Entry is one decoded LOG record from a function instance, pre-OTLP (log-specific: Body +
// Severity). Traces and metrics (F51/F52) define sibling record types behind the same
// Reader/Sink seam — Entry is not reused.
type Entry struct {
	Time       time.Time         // real event time (epoch nanos on the wire)
	Severity   Severity          // exact for Path B; coarse for Path A
	Body       string            // the message (Path B keeps console args before util.format)
	Attrs      map[string]string // structured attributes
	Invocation string            // per-invocation id (may be empty for pre-invocation Path A lines)
	TraceID    string            // hex32, may be empty
	SpanID     string            // hex16, may be empty
	Source     Source
}

// Resource is the OTLP Resource identity stamped on every record — the F54 query-time scoping
// key. Tenant = Namespace today; explicit for later multi-tenant scoping.
type Resource struct {
	Namespace string
	Function  string
	Replica   string
	Tenant    string
}
