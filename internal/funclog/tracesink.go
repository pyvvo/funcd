package funclog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// TraceSink is the traces analog of Sink (ADR-0101): it accumulates per-Resource span segments,
// marshals each sealed segment to OTLP-trace-JSON-Lines (ptrace), and persists it as ONE
// blob.Bucket object under a traces/ prefix. It mirrors BlobSink's seal machinery rather than
// touching the Implemented logs sink. CONCURRENCY-SAFE: per-Resource segments are independently
// locked, so concurrent AppendSpan/Flush from the many per-instance drains is supported.
type TraceSink interface {
	// AppendSpan adds one Span to the open segment for res.
	AppendSpan(ctx context.Context, res Resource, s Span) error
	// Flush seals res's current segment (also triggered by size/age) and Puts it; returns the blob key.
	Flush(ctx context.Context, res Resource) (key string, err error)
	// Close stops the age-flusher, then seals and Puts every open segment.
	Close() error
}

// BlobTraceSink is the blob-backed TraceSink (the only implementation; blob.Bucket's drivers
// provide memory/file/s3). Segments seal at SegmentMaxBytes or SegmentMaxAge. It reuses funclog.Deps.
type BlobTraceSink struct {
	bucket   blob.Bucket
	maxBytes int
	maxAge   time.Duration
	clock    clock.Clock
	log      *slog.Logger

	mu       sync.Mutex                 // guards the segments map (get-or-create); each segment locks itself
	segments map[Resource]*traceSegment // per-Resource open segment

	stop     chan struct{} // closes to stop the background age-flusher
	stopOnce sync.Once

	// flushing is read-held by each Flush from taking its segment until its Put returns, and write-held by
	// Close: a taken segment is no longer in segments, so Close must wait for that Put instead.
	flushing sync.RWMutex
}

type traceSegment struct {
	mu     sync.Mutex
	spans  []Span
	bytes  int
	opened time.Time
}

// NewBlobTraceSink builds the blob-backed trace sink over the funcd-system observability bucket.
// Bucket and Clock are required; segment caps default when zero. Keys are partitioned as
// traces/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl.
func NewBlobTraceSink(d Deps) (*BlobTraceSink, error) {
	const op = "funclog.NewBlobTraceSink"
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
	s := &BlobTraceSink{
		bucket: d.Bucket, maxBytes: d.SegmentMaxBytes, maxAge: d.SegmentMaxAge,
		clock: d.Clock, log: d.Logger, segments: make(map[Resource]*traceSegment),
		stop: make(chan struct{}),
	}
	go s.flushLoop()
	return s, nil
}

// flushLoop proactively seals segments older than maxAge (the "time" flush for idle segments).
func (s *BlobTraceSink) flushLoop() {
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
func (s *BlobTraceSink) flushAged() {
	now := s.clock.Now()
	s.mu.Lock()
	var aged []Resource
	for res, seg := range s.segments {
		seg.mu.Lock()
		old := len(seg.spans) > 0 && now.Sub(seg.opened) >= s.maxAge
		seg.mu.Unlock()
		if old {
			aged = append(aged, res)
		}
	}
	s.mu.Unlock()
	for _, res := range aged {
		if _, err := s.Flush(context.Background(), res); err != nil {
			s.log.Warn("funclog: trace age-flush failed", "namespace", res.Namespace, "function", res.Function, "error", err)
		}
	}
}

// AppendSpan adds sp to res's open segment, sealing+Putting it if it crosses the size/age cap.
func (s *BlobTraceSink) AppendSpan(ctx context.Context, res Resource, sp Span) error {
	s.mu.Lock()
	seg := s.segments[res]
	if seg == nil {
		seg = &traceSegment{opened: s.clock.Now()}
		s.segments[res] = seg
	}
	s.mu.Unlock()

	seg.mu.Lock()
	seg.spans = append(seg.spans, sp)
	seg.bytes += estimateSpanBytes(sp)
	full := seg.bytes >= s.maxBytes || s.clock.Now().Sub(seg.opened) >= s.maxAge
	seg.mu.Unlock()

	if full {
		_, err := s.Flush(ctx, res)
		return err
	}
	return nil
}

// Flush seals res's current segment and Puts it as one OTLP-trace-JSONL object; returns the blob
// key ("" if the segment was empty/absent).
func (s *BlobTraceSink) Flush(ctx context.Context, res Resource) (string, error) {
	s.flushing.RLock()
	defer s.flushing.RUnlock()
	return s.flush(ctx, res)
}

// flush is Flush for a caller that already holds flushing.
func (s *BlobTraceSink) flush(ctx context.Context, res Resource) (string, error) {
	s.mu.Lock()
	seg := s.segments[res]
	delete(s.segments, res)
	s.mu.Unlock()
	if seg == nil {
		return "", nil
	}
	seg.mu.Lock()
	spans := seg.spans
	seg.mu.Unlock()
	if len(spans) == 0 {
		return "", nil
	}

	now := s.clock.Now()
	data, err := marshalTraceOTLP(res, spans, now)
	if err != nil {
		return "", err
	}
	key := traceSegmentKey(res, now)
	if err := s.bucket.Put(ctx, key, data); err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "funclog.BlobTraceSink.Flush", "put trace segment %q", key)
	}
	return key, nil
}

// Close stops the age-flusher, waits for any Flush still Putting a segment, then seals and Puts every open
// segment (the loss-safe shutdown boundary).
func (s *BlobTraceSink) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	s.flushing.Lock()
	defer s.flushing.Unlock()
	s.mu.Lock()
	res := make([]Resource, 0, len(s.segments))
	for r := range s.segments {
		res = append(res, r)
	}
	s.mu.Unlock()
	var firstErr error
	for _, r := range res {
		if _, err := s.flush(context.Background(), r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// marshalTraceOTLP builds one ptrace.Traces (Resource = the function identity; one Span per record)
// from a segment's spans and marshals it to OTLP/JSON (one document = one line).
func marshalTraceOTLP(res Resource, spans []Span, now time.Time) ([]byte, error) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	ra := rs.Resource().Attributes()
	ra.PutStr("source", "function") // the tenant/function lane label, distinct from ADR-0010's source=platform
	ra.PutStr("namespace", res.Namespace)
	ra.PutStr("function", res.Function)
	if res.Replica != "" {
		ra.PutStr("replica", res.Replica)
	}
	ra.PutStr("tenant", tenantOf(res))

	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("funcd/funclog")
	for _, sp := range spans {
		s := ss.Spans().AppendEmpty()
		if tid, ok := parseTraceID(sp.TraceID); ok {
			s.SetTraceID(tid)
		}
		if sid, ok := parseSpanID(sp.SpanID); ok {
			s.SetSpanID(sid)
		}
		if pid, ok := parseSpanID(sp.ParentID); ok {
			s.SetParentSpanID(pid)
		}
		s.SetName(sp.Name)
		s.SetKind(spanKindNumber(sp.Kind))
		start := sp.Start
		if start.IsZero() {
			start = now
		}
		end := sp.End
		if end.IsZero() {
			end = now
		}
		s.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		s.SetEndTimestamp(pcommon.NewTimestampFromTime(end))
		st := s.Status()
		st.SetCode(statusCode(sp.Status))
		if sp.StatusMsg != "" {
			st.SetMessage(sp.StatusMsg)
		}
		a := s.Attributes()
		if sp.Invocation != "" {
			a.PutStr("inv", sp.Invocation)
		}
		for k, v := range sp.Attrs {
			a.PutStr(k, v)
		}
		// ADR-0105: fan-in edges are OTel span links to the non-primary predecessors, in the SAME trace.
		for _, linkID := range sp.Links {
			lsid, ok := parseSpanID(linkID)
			if !ok {
				continue
			}
			l := s.Links().AppendEmpty()
			if tid, ok := parseTraceID(sp.TraceID); ok {
				l.SetTraceID(tid)
			}
			l.SetSpanID(lsid)
		}
	}

	var m ptrace.JSONMarshaler
	b, err := m.MarshalTraces(traces)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "funclog.marshalTraceOTLP", "marshal OTLP traces")
	}
	return append(b, '\n'), nil // JSON-Lines: one OTLP document per line
}

// traceSegmentKey partitions objects as traces/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl.
func traceSegmentKey(res Resource, now time.Time) string {
	replica := res.Replica
	if replica == "" {
		replica = "na"
	}
	return fmt.Sprintf("traces/%s/%s/%s/%d-%s.otlp.jsonl",
		res.Namespace, res.Function, now.UTC().Format("2006-01-02"), now.UTC().UnixNano(), replica)
}

func estimateSpanBytes(sp Span) int {
	n := len(sp.TraceID) + len(sp.SpanID) + len(sp.ParentID) + len(sp.Name) + len(sp.StatusMsg) + len(sp.Invocation) + 96
	for k, v := range sp.Attrs {
		n += len(k) + len(v) + 8
	}
	return n
}

func spanKindNumber(k SpanKind) ptrace.SpanKind {
	switch k {
	case SpanServer:
		return ptrace.SpanKindServer
	case SpanInternal:
		return ptrace.SpanKindInternal
	case SpanClient:
		return ptrace.SpanKindClient
	default:
		return ptrace.SpanKindUnspecified
	}
}

func statusCode(s SpanStatus) ptrace.StatusCode {
	switch s {
	case StatusOk:
		return ptrace.StatusCodeOk
	case StatusError:
		return ptrace.StatusCodeError
	default:
		return ptrace.StatusCodeUnset
	}
}
