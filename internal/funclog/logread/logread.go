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

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/funclog/compact"
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

// Query selects + bounds a read. Namespace+Function are required; the rest are optional filters.
type Query struct {
	Namespace         string
	Function          string
	Since             time.Time // zero ⇒ no lower bound
	MinSeverityNumber int32     // 0 ⇒ all severities
	Limit             int       // <= 0 ⇒ DefaultLimit; capped at MaxLimit
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
	if q.Namespace == "" || q.Function == "" {
		return nil, fault.Invalidf(op, "namespace and function are required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	prefix := logsPrefix + q.Namespace + "/" + q.Function + "/"
	objs, err := r.bucket.List(ctx, prefix)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %q", prefix)
	}

	var rows []compact.Row
	for _, o := range objs {
		switch {
		case strings.HasSuffix(o.Key, compactSuffix):
			data, gerr := r.bucket.Get(ctx, o.Key)
			if gerr != nil {
				return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get %q", o.Key)
			}
			decoded, perr := parquet.Read[compact.Row](bytes.NewReader(data), int64(len(data)))
			if perr != nil {
				return nil, fault.Wrapf(perr, fault.Internal, op, "read parquet %q", o.Key)
			}
			rows = append(rows, decoded...)
		case strings.HasSuffix(o.Key, rawSuffix):
			data, gerr := r.bucket.Get(ctx, o.Key)
			if gerr != nil {
				return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get %q", o.Key)
			}
			decoded, derr := compact.DecodeJSONL(data)
			if derr != nil {
				return nil, fault.Wrapf(derr, fault.KindOf(derr), op, "decode %q", o.Key)
			}
			rows = append(rows, decoded...)
		}
	}

	lines := make([]Line, 0, len(rows))
	for _, row := range rows {
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
