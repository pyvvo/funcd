package funclog

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/platform/clock"
)

// Sink accumulates per-Resource segments, marshals each sealed segment to OTLP-JSON-Lines, and
// persists it as ONE blob.Bucket object (whose own drivers give memory/file/s3). The seam is
// signal-generic (this logs Sink takes Entry; F51/F52 add sibling Sinks for their record types).
// CONCURRENCY-SAFE: per-Resource segments are independently locked, so concurrent Append/Flush
// from the many per-instance Pumps is supported.
type Sink interface {
	// Append adds one Entry to the open segment for res.
	Append(ctx context.Context, res Resource, e Entry) error
	// Flush seals res's current segment (also triggered by size/age) and Puts it; returns the blob key.
	Flush(ctx context.Context, res Resource) (key string, err error)
	io.Closer
}

// Deps constructs a BlobSink. Bucket is the funcd-system observability bucket (blob.Bucket).
type Deps struct {
	Bucket          blob.Bucket
	SegmentMaxBytes int           // seal+Put at this many buffered bytes (default 8 MiB)
	SegmentMaxAge   time.Duration // ...or after this long (default 10s)
	Clock           clock.Clock
	Logger          *slog.Logger
}

const (
	defaultSegmentMaxBytes = 8 << 20 // 8 MiB
	defaultSegmentMaxAge   = 10 * time.Second
)

// BlobSink is the blob-backed Sink (the only implementation; blob.Bucket's drivers provide
// memory/file/s3). Segments seal at SegmentMaxBytes or SegmentMaxAge.
type BlobSink struct {
	bucket   blob.Bucket
	maxBytes int
	maxAge   time.Duration
	clock    clock.Clock
	log      *slog.Logger

	mu       sync.Mutex            // guards the segments map (get-or-create); each segment locks itself
	segments map[Resource]*segment // per-Resource open segment

	stop     chan struct{} // closes to stop the background age-flusher
	stopOnce sync.Once
}

type segment struct {
	mu      sync.Mutex
	entries []Entry
	bytes   int
	opened  time.Time
}

// NewBlobSink builds the blob-backed sink over the funcd-system observability bucket. Bucket and
// Clock are required; segment caps default when zero.
func NewBlobSink(d Deps) (*BlobSink, error) {
	const op = "funclog.NewBlobSink"
	if d.Bucket == nil {
		return nil, fault.Invalidf(op, "blob bucket is required")
	}
	if d.Clock == nil {
		return nil, fault.Invalidf(op, "clock is required")
	}
	if d.SegmentMaxBytes <= 0 {
		d.SegmentMaxBytes = defaultSegmentMaxBytes
	}
	if d.SegmentMaxAge <= 0 {
		d.SegmentMaxAge = defaultSegmentMaxAge
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &BlobSink{
		bucket: d.Bucket, maxBytes: d.SegmentMaxBytes, maxAge: d.SegmentMaxAge,
		clock: d.Clock, log: d.Logger, segments: make(map[Resource]*segment),
		stop: make(chan struct{}),
	}
	go s.flushLoop()
	return s, nil
}

// flushLoop proactively seals segments older than maxAge — the "time" flush for idle segments (one
// that received no further Append after a burst would otherwise sit in memory until Close). It ticks
// on real time; age is judged by the sink clock. Stopped by Close.
func (s *BlobSink) flushLoop() {
	interval := s.maxAge / 2
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.flushAged()
		}
	}
}

// flushAged seals every segment whose age has reached maxAge.
func (s *BlobSink) flushAged() {
	now := s.clock.Now()
	s.mu.Lock()
	var aged []Resource
	for res, seg := range s.segments {
		seg.mu.Lock()
		old := len(seg.entries) > 0 && now.Sub(seg.opened) >= s.maxAge
		seg.mu.Unlock()
		if old {
			aged = append(aged, res)
		}
	}
	s.mu.Unlock()
	for _, res := range aged {
		if _, err := s.Flush(context.Background(), res); err != nil {
			s.log.Warn("funclog: age-flush failed", "namespace", res.Namespace, "function", res.Function, "error", err)
		}
	}
}

// Append adds e to res's open segment, sealing+Putting it if it crosses the size/age cap.
func (s *BlobSink) Append(ctx context.Context, res Resource, e Entry) error {
	s.mu.Lock()
	seg := s.segments[res]
	if seg == nil {
		seg = &segment{opened: s.clock.Now()}
		s.segments[res] = seg
	}
	s.mu.Unlock()

	seg.mu.Lock()
	seg.entries = append(seg.entries, e)
	seg.bytes += estimateBytes(e)
	full := seg.bytes >= s.maxBytes || s.clock.Now().Sub(seg.opened) >= s.maxAge
	seg.mu.Unlock()

	if full {
		_, err := s.Flush(ctx, res)
		return err
	}
	return nil
}

// Flush seals res's current segment and Puts it as one OTLP-JSONL object; returns the blob key
// ("" if the segment was empty/absent).
func (s *BlobSink) Flush(ctx context.Context, res Resource) (string, error) {
	s.mu.Lock()
	seg := s.segments[res]
	delete(s.segments, res)
	s.mu.Unlock()
	if seg == nil {
		return "", nil
	}
	seg.mu.Lock()
	entries := seg.entries
	seg.mu.Unlock()
	if len(entries) == 0 {
		return "", nil
	}

	now := s.clock.Now()
	data, err := marshalOTLP(res, entries, now)
	if err != nil {
		return "", err
	}
	key := segmentKey(res, now)
	if err := s.bucket.Put(ctx, key, data); err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "funclog.BlobSink.Flush", "put segment %q", key)
	}
	return key, nil
}

// Close stops the age-flusher, then seals and Puts every open segment (the loss-safe shutdown boundary).
func (s *BlobSink) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	res := make([]Resource, 0, len(s.segments))
	for r := range s.segments {
		res = append(res, r)
	}
	s.mu.Unlock()
	var firstErr error
	for _, r := range res {
		if _, err := s.Flush(context.Background(), r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// marshalOTLP builds one plog.Logs (Resource = the function identity; per-invocation fields on
// each LogRecord) from a segment's entries and marshals it to OTLP/JSON (one document = one line).
func marshalOTLP(res Resource, entries []Entry, now time.Time) ([]byte, error) {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	ra := rl.Resource().Attributes()
	ra.PutStr("source", "function") // the tenant/function lane label, distinct from ADR-0010's source=platform
	ra.PutStr("namespace", res.Namespace)
	ra.PutStr("function", res.Function)
	if res.Replica != "" {
		ra.PutStr("replica", res.Replica)
	}
	ra.PutStr("tenant", tenantOf(res))

	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("funcd/funclog")
	for _, e := range entries {
		lr := sl.LogRecords().AppendEmpty()
		ts := e.Time
		if ts.IsZero() {
			ts = now
		}
		lr.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		lr.SetSeverityNumber(severityNumber(e.Severity))
		lr.SetSeverityText(string(e.Severity))
		lr.Body().SetStr(e.Body)
		la := lr.Attributes()
		if e.Invocation != "" {
			la.PutStr("inv", e.Invocation)
		}
		la.PutStr("funcd.source", string(e.Source))
		for k, v := range e.Attrs {
			la.PutStr(k, v)
		}
		if tid, ok := parseTraceID(e.TraceID); ok {
			lr.SetTraceID(tid)
		}
		if sid, ok := parseSpanID(e.SpanID); ok {
			lr.SetSpanID(sid)
		}
	}

	var m plog.JSONMarshaler
	b, err := m.MarshalLogs(logs)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "funclog.marshalOTLP", "marshal OTLP logs")
	}
	return append(b, '\n'), nil // JSON-Lines: one OTLP document per line
}

func tenantOf(r Resource) string {
	if r.Tenant != "" {
		return r.Tenant
	}
	return r.Namespace
}

// segmentKey partitions objects as logs/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl.
func segmentKey(res Resource, now time.Time) string {
	replica := res.Replica
	if replica == "" {
		replica = "na"
	}
	return fmt.Sprintf("logs/%s/%s/%s/%d-%s.otlp.jsonl",
		res.Namespace, res.Function, now.UTC().Format("2006-01-02"), now.UTC().UnixNano(), replica)
}

func estimateBytes(e Entry) int {
	n := len(e.Body) + len(e.Invocation) + len(e.TraceID) + len(e.SpanID) + 64
	for k, v := range e.Attrs {
		n += len(k) + len(v) + 8
	}
	return n
}

func severityNumber(s Severity) plog.SeverityNumber {
	switch s {
	case SevTrace:
		return plog.SeverityNumberTrace
	case SevDebug:
		return plog.SeverityNumberDebug
	case SevInfo:
		return plog.SeverityNumberInfo
	case SevWarn:
		return plog.SeverityNumberWarn
	case SevError:
		return plog.SeverityNumberError
	case SevFatal:
		return plog.SeverityNumberFatal
	default:
		return plog.SeverityNumberUnspecified
	}
}

func parseTraceID(s string) (pcommon.TraceID, bool) {
	var id pcommon.TraceID
	if len(s) != 32 {
		return id, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

func parseSpanID(s string) (pcommon.SpanID, bool) {
	var id pcommon.SpanID
	if len(s) != 16 {
		return id, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}
