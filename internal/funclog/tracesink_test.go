package funclog_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

func newTraceSink(t *testing.T, b blob.Bucket, maxBytes int) *funclog.BlobTraceSink {
	t.Helper()
	s, err := funclog.NewBlobTraceSink(funclog.Deps{
		Bucket: b, SegmentMaxBytes: maxBytes, SegmentMaxAge: time.Hour,
		Clock: clock.Fake(time.Unix(1_700_000_000, 0)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// readBackTraces reads the single OTLP-trace-JSONL object under prefix and unmarshals it.
func readBackTraces(t *testing.T, b blob.Bucket, prefix string) ptrace.Traces {
	t.Helper()
	objs, err := b.List(context.Background(), prefix)
	require.NoError(t, err)
	require.Len(t, objs, 1, "exactly one trace segment object expected")
	data, err := b.Get(context.Background(), objs[0].Key)
	require.NoError(t, err)
	var u ptrace.JSONUnmarshaler
	tr, err := u.UnmarshalTraces([]byte(strings.TrimSpace(string(data))))
	require.NoError(t, err, "persisted bytes must be valid OTLP/JSON traces")
	return tr
}

const (
	testTraceID  = "0123456789abcdef0123456789abcdef"
	testSpanID   = "0123456789abcdef"
	testParentID = "fedcba9876543210"
)

func serverSpan() funclog.Span {
	start := time.Unix(1_700_000_000, 0)
	return funclog.Span{
		TraceID: testTraceID, SpanID: testSpanID, ParentID: testParentID,
		Name: "greeter", Kind: funclog.SpanServer,
		Start: start, End: start.Add(3 * time.Millisecond),
		Status: funclog.StatusOk, Attrs: map[string]string{"http.status_code": "200"},
		Invocation: "inv-1",
	}
}

// scenario: persisted-via-blob — a sealed segment of spans is written as exactly ONE OTLP-trace-JSONL
// object under a traces/ prefix, round-tripping every span facet (ids/name/kind/status/timing).
func TestScenarioSpanPersistedViaBlob(t *testing.T) {
	b := memBucket(t)
	s := newTraceSink(t, b, 1<<20)
	require.NoError(t, s.AppendSpan(context.Background(), defaultRes(), serverSpan()))
	require.NoError(t, s.AppendSpan(context.Background(), defaultRes(), serverSpan()))
	key, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	require.Contains(t, key, "traces/default/hello/")

	tr := readBackTraces(t, b, "traces/")
	require.Equal(t, 2, tr.SpanCount())
	sp := tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, "greeter", sp.Name())
	require.Equal(t, ptrace.SpanKindServer, sp.Kind())
	require.Equal(t, ptrace.StatusCodeOk, sp.Status().Code())
	require.Equal(t, testTraceID, sp.TraceID().String())
	require.Equal(t, testSpanID, sp.SpanID().String())
	require.Equal(t, testParentID, sp.ParentSpanID().String())
	require.Greater(t, sp.EndTimestamp().AsTime().UnixNano(), sp.StartTimestamp().AsTime().UnixNano(), "a positive duration")
	code, ok := sp.Attributes().Get("http.status_code")
	require.True(t, ok)
	require.Equal(t, "200", code.Str())
}

// scenario: identity-tagged — the OTLP Resource carries namespace/function/replica/tenant + source=function.
func TestScenarioSpanIdentityTagged(t *testing.T) {
	b := memBucket(t)
	s := newTraceSink(t, b, 1<<20)
	require.NoError(t, s.AppendSpan(context.Background(), defaultRes(), serverSpan()))
	_, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	ra := readBackTraces(t, b, "traces/").ResourceSpans().At(0).Resource().Attributes().AsRaw()
	require.Equal(t, "default", ra["namespace"])
	require.Equal(t, "hello", ra["function"])
	require.Equal(t, "r1", ra["replica"])
	require.Equal(t, "default", ra["tenant"]) // tenant defaults to namespace
	require.Equal(t, "function", ra["source"])
}

// Span{Status: ERROR} marshal round-trip — a failed invocation span persists as StatusCodeError + message.
func TestSpanErrorStatusRoundTrip(t *testing.T) {
	b := memBucket(t)
	s := newTraceSink(t, b, 1<<20)
	sp := serverSpan()
	sp.Status = funclog.StatusError
	sp.StatusMsg = "handler threw: boom"
	require.NoError(t, s.AppendSpan(context.Background(), defaultRes(), sp))
	_, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	st := readBackTraces(t, b, "traces/").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Status()
	require.Equal(t, ptrace.StatusCodeError, st.Code())
	require.Equal(t, "handler threw: boom", st.Message())
}

// scenario: signal-demux — one channel carrying BOTH log and span lines routes each by "funcd.signal":
// spans → the trace sink, logs (incl. an UNTAGGED line, back-compat) → the logs sink.
func TestScenarioSignalDemux(t *testing.T) {
	b := memBucket(t)
	logSink := newSink(t, b, 1<<20)
	traceSink := newTraceSink(t, b, 1<<20)

	logLine := `{"ts":1700000000000000000,"sev":"INFO","body":"hello","funcd.source":"console"}`
	spanLine := `{"funcd.signal":"traces","trace_id":"` + testTraceID + `","span_id":"` + testSpanID +
		`","parent_id":"","name":"greeter","kind":"SERVER","start":1700000000000000000,"end":1700000000000003000,"status":"OK","attrs":{},"inv":"inv-1"}`
	untagged := `{"ts":1700000000000000000,"sev":"WARN","body":"legacy","funcd.source":"console"}` // no funcd.signal → logs
	channel := strings.Join([]string{logLine, spanLine, untagged}, "\n") + "\n"

	err := funclog.Route(context.Background(), strings.NewReader(channel), funclog.Sinks{Logs: logSink, Traces: traceSink}, defaultRes(), nil)
	require.NoError(t, err) // EOF seals both sinks

	require.Equal(t, 2, readBackOne(t, b, "logs/").LogRecordCount(), "the two log lines (tagged + untagged) → logs sink")
	tr := readBackTraces(t, b, "traces/")
	require.Equal(t, 1, tr.SpanCount(), "the one span line → trace sink")
	require.Equal(t, "greeter", tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Name())
}

// scenario: signal-demux (traces disabled) — a nil Traces sink drops span lines but still routes logs.
func TestSignalDemuxTracesDisabled(t *testing.T) {
	b := memBucket(t)
	logSink := newSink(t, b, 1<<20)
	spanLine := `{"funcd.signal":"traces","trace_id":"` + testTraceID + `","span_id":"` + testSpanID + `","name":"x","kind":"SERVER","start":1,"end":2,"status":"OK","attrs":{},"inv":"i"}`
	logLine := `{"ts":1700000000000000000,"sev":"INFO","body":"hi","funcd.source":"console"}`
	channel := spanLine + "\n" + logLine + "\n"

	err := funclog.Route(context.Background(), strings.NewReader(channel), funclog.Sinks{Logs: logSink, Traces: nil}, defaultRes(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, readBackOne(t, b, "logs/").LogRecordCount())
	objs, err := b.List(context.Background(), "traces/")
	require.NoError(t, err)
	require.Empty(t, objs, "span lines dropped when traces are disabled")
}

// scenario: transport-fd3-and-uds — the same span NDJSON over two io.Readers (fd3/UDS both yield an
// io.Reader) routes to the same persisted span through Route.
func TestScenarioSpanTransportFd3AndUds(t *testing.T) {
	spanLine := `{"funcd.signal":"traces","trace_id":"` + testTraceID + `","span_id":"` + testSpanID +
		`","parent_id":"` + testParentID + `","name":"greeter","kind":"SERVER","start":1700000000000000000,"end":1700000000000003000,"status":"OK","attrs":{},"inv":"i"}` + "\n"

	for _, transport := range []string{"fd3", "uds"} {
		b := memBucket(t)
		traceSink := newTraceSink(t, b, 1<<20)
		err := funclog.Route(context.Background(), strings.NewReader(spanLine), funclog.Sinks{Traces: traceSink}, defaultRes(), nil)
		require.NoError(t, err, transport)
		tr := readBackTraces(t, b, "traces/")
		require.Equal(t, 1, tr.SpanCount(), transport)
		require.Equal(t, testTraceID, tr.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).TraceID().String(), transport)
	}
}

// A Span with Links marshals to a ptrace span carrying OTel span links in the SAME trace (ADR-0105 fan-in).
func TestSpanLinksMarshal(t *testing.T) {
	b := memBucket(t)
	s := newTraceSink(t, b, 1<<20)
	sp := serverSpan()
	sp.Links = []string{"fedcba9876543210", "0123456789abcdef"}
	require.NoError(t, s.AppendSpan(context.Background(), defaultRes(), sp))
	_, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)

	span := readBackTraces(t, b, "traces/").ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, 2, span.Links().Len(), "two fan-in links")
	require.Equal(t, testTraceID, span.Links().At(0).TraceID().String(), "links are in the same trace")
	require.Equal(t, "fedcba9876543210", span.Links().At(0).SpanID().String())
	require.Equal(t, "0123456789abcdef", span.Links().At(1).SpanID().String())
}

func TestNewBlobTraceSinkValidation(t *testing.T) {
	_, err := funclog.NewBlobTraceSink(funclog.Deps{Clock: clock.System()})
	require.Error(t, err) // nil bucket
	_, err = funclog.NewBlobTraceSink(funclog.Deps{Bucket: memBucket(t)})
	require.Error(t, err) // nil clock
}

// A run-root span's replica is its run record name, which grows by "-<step>" per inline sub-workflow level
// (internal/workflow/subworkflow.go), so a depth-3 chain of 63-byte names is 255 bytes. Its segment must
// still be stored on a file:// bucket, where each key segment is a file name, and two replicas that share a
// long prefix must keep distinct keys.
func TestTraceKey_LongReplicaStoredOnFileBucket(t *testing.T) {
	ctx := context.Background()
	b, err := gocloud.Open(ctx, gocloud.FileURL(t.TempDir()))
	require.NoError(t, err)
	s := newTraceSink(t, b, 1<<20)
	label := strings.Repeat("a", 63)
	run := label + strings.Repeat("-"+label, 3)
	replicas := []string{run, run[:len(run)-1] + "b"}
	for _, r := range replicas {
		res := funclog.Resource{Namespace: "default", Function: "wf", Replica: r}
		require.NoError(t, s.AppendSpan(ctx, res, serverSpan()))
		key, err := s.Flush(ctx, res)
		require.NoError(t, err)
		ra := readBackTraces(t, b, key).ResourceSpans().At(0).Resource().Attributes().AsRaw()
		require.Equal(t, r, ra["replica"], "the Resource keeps the full replica")
	}
	objs, err := b.List(ctx, "traces/")
	require.NoError(t, err)
	require.Len(t, objs, len(replicas), "replicas that share a long prefix keep distinct keys")
}
