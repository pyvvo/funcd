// Package logread is the thin pure-Go function-log reader (ADR-0084): it merges the compacted Parquet
// (ADR-0083) with the recent raw OTLP-JSONL tail (ADR-0081) over the blob.Bucket port, filters by
// time/severity/limit, and returns a tenant's function logs time-ordered. No DuckDB, no cgo,
// lakehouse-independent. Tenant scoping is the caller's concern (the control-plane PEP authorizes the
// namespace before calling Read); this package just reads the namespace it is given.
package logread

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/funclog/compact"
)

const (
	// DefaultLimit caps a read when Query.Limit is unset.
	DefaultLimit = 1000
	// MaxLimit hard-caps a read (≈ a few MB of lines — bounds a runaway scan).
	MaxLimit = 10000

	logsPrefix    = "logs/"
	compactSuffix = ".parquet"
	rawSuffix     = ".otlp.jsonl"
)

// Line is one log record returned to a caller (the wire + CLI DTO; a friendlier view of compact.Row).
type Line struct {
	Time           time.Time       `json:"time"`
	Severity       string          `json:"severity"`
	SeverityNumber int32           `json:"severityNumber"`
	Body           string          `json:"body"`
	Namespace      string          `json:"namespace"`
	Function       string          `json:"function"`
	Replica        string          `json:"replica"`
	Source         string          `json:"source"`
	Invocation     string          `json:"inv,omitempty"`
	TraceID        string          `json:"traceId,omitempty"`
	SpanID         string          `json:"spanId,omitempty"`
	Attrs          json.RawMessage `json:"attrs,omitempty"`
}

// Query selects + bounds a read. Namespace is required; Function OR TraceID must be set (a bare
// namespace scan with no trace filter is rejected — see Read's guard). The rest are optional filters.
type Query struct {
	Namespace         string
	Function          string
	Since             time.Time // zero ⇒ no lower bound
	MinSeverityNumber int32     // 0 ⇒ all severities
	Limit             int       // <= 0 ⇒ DefaultLimit; capped at MaxLimit
	// TraceID scopes the read to one run's trace (ADR-0106): "" ⇒ no trace filter (ADR-0084 per-function
	// behavior). When Function == "" && TraceID != "", Read scans the whole logs/<ns>/ prefix (namespace-wide,
	// run-scoped) — every step function's lines for that run, incl. sub-workflow children (same trace, ADR-0104).
	TraceID string
}

// Reader returns a function's logs, newest-bounded by Query.
type Reader interface {
	Read(ctx context.Context, q Query) ([]Line, error)
}

// BlobReader is the blob-backed Reader (compacted Parquet + raw OTLP-JSONL tail over blob.Bucket).
type BlobReader struct {
	bucket blob.Bucket
}

// NewBlobReader builds the reader over the funcd-system observability bucket.
func NewBlobReader(b blob.Bucket) *BlobReader { return &BlobReader{bucket: b} }

// Read merges the compacted Parquet and the raw OTLP-JSONL tail for (Namespace, Function), filters by
// Since/MinSeverityNumber, sorts ascending by time, and returns the most-recent Limit (the tail).
func (r *BlobReader) Read(ctx context.Context, q Query) ([]Line, error) {
	const op = "logread.BlobReader.Read"
	// Guard (ADR-0106): namespace is always required; Function OR TraceID must be set. A bare namespace
	// scan with no trace filter stays Invalid, so the namespace-wide mode is only ever reachable WITH a
	// trace-id — it can never become an unfiltered full-namespace dump.
	if q.Namespace == "" || (q.Function == "" && q.TraceID == "") {
		return nil, fault.Invalidf(op, "namespace is required, and one of function or traceId must be set")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	// ADR-0106: namespace-wide, run-scoped read (all functions) when Function is empty and a trace-id is
	// set; otherwise the ADR-0084 per-function prefix.
	prefix := logsPrefix + q.Namespace + "/"
	if q.Function != "" {
		prefix += q.Function + "/"
	}
	objs, err := r.bucket.List(ctx, prefix)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %q", prefix)
	}

	rows, vanished, err := r.readObjects(ctx, objs, func(string) bool { return true })
	if err != nil {
		return nil, err
	}
	if vanished {
		// A compaction pass deleted listed raw after the List; it deletes raw only once the window's Parquet
		// is written (ADR-0084), so a re-list holds that Parquet. Read the Parquet the first listing missed.
		listed := make(map[string]bool, len(objs))
		for _, o := range objs {
			listed[o.Key] = true
		}
		again, lerr := r.bucket.List(ctx, prefix)
		if lerr != nil {
			return nil, fault.Wrapf(lerr, fault.KindOf(lerr), op, "list %q", prefix)
		}
		more, _, merr := r.readObjects(ctx, again, func(key string) bool {
			return !listed[key] && strings.HasSuffix(key, compactSuffix)
		})
		if merr != nil {
			return nil, merr
		}
		rows = append(rows, more...)
	}

	lines := make([]Line, 0, len(rows))
	for _, row := range rows {
		if q.TraceID != "" && row.TraceID != q.TraceID { // ADR-0106: run-scoped trace filter
			continue
		}
		if row.SeverityNumber < q.MinSeverityNumber {
			continue
		}
		t := time.Unix(0, row.TimeUnixNano).UTC()
		if !q.Since.IsZero() && t.Before(q.Since) {
			continue
		}
		lines = append(lines, lineFromRow(row, t))
	}

	// Sort ascending by time (stable on equal timestamps), then take the TAIL — the most-recent Limit,
	// oldest-first (a log tail), never the head/oldest-N.
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

// readObjects decodes the listed Parquet and raw objects whose key passes want. An object deleted between
// the List and its Get (raw compacted, Parquet pruned) is skipped and reported as vanished.
func (r *BlobReader) readObjects(ctx context.Context, objs []blob.Attributes, want func(key string) bool) ([]compact.Row, bool, error) {
	const op = "logread.BlobReader.Read"
	var rows []compact.Row
	vanished := false
	for _, o := range objs {
		parquetObj := strings.HasSuffix(o.Key, compactSuffix)
		if (!parquetObj && !strings.HasSuffix(o.Key, rawSuffix)) || !want(o.Key) {
			continue
		}
		data, gerr := r.bucket.Get(ctx, o.Key)
		if fault.KindOf(gerr) == fault.NotFound {
			vanished = true
			continue
		}
		if gerr != nil {
			return nil, false, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get %q", o.Key)
		}
		if parquetObj {
			decoded, perr := parquet.Read[compact.Row](bytes.NewReader(data), int64(len(data)))
			if perr != nil {
				return nil, false, fault.Wrapf(perr, fault.Internal, op, "read parquet %q", o.Key)
			}
			rows = append(rows, decoded...)
			continue
		}
		decoded, derr := compact.DecodeJSONL(data)
		if derr != nil {
			continue // one undecodable object (a torn write) must not fail the read; the compactor logs its key
		}
		rows = append(rows, decoded...)
	}
	return rows, vanished, nil
}

// lineFromRow maps a compact.Row (the Parquet/raw schema) to the caller-facing Line DTO.
func lineFromRow(row compact.Row, t time.Time) Line {
	l := Line{
		Time:           t,
		Severity:       row.SeverityText,
		SeverityNumber: row.SeverityNumber,
		Body:           row.Body,
		Namespace:      row.Namespace,
		Function:       row.Function,
		Replica:        row.Replica,
		Source:         row.Source,
		Invocation:     row.Invocation,
		TraceID:        row.TraceID,
		SpanID:         row.SpanID,
	}
	if a := strings.TrimSpace(row.AttrsJSON); a != "" && a != "{}" {
		l.Attrs = json.RawMessage(row.AttrsJSON)
	}
	return l
}

// SeverityNumber maps a level name (trace|debug|info|warn|error|fatal, case-insensitive) to its OTLP
// SeverityNumber threshold (the lowest number at that level); ok=false for an unknown name.
// "" ⇒ (0, true) = no filter.
func SeverityNumber(level string) (int32, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "":
		return 0, true
	case "trace":
		return 1, true
	case "debug":
		return 5, true
	case "info":
		return 9, true
	case "warn", "warning":
		return 13, true
	case "error":
		return 17, true
	case "fatal":
		return 21, true
	default:
		return 0, false
	}
}
