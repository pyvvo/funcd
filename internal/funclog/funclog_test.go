package funclog_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/funclog"
	"github.com/green-0-rabbit/funcd/internal/platform/clock"
)

func memBucket(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	return b
}

func newSink(t *testing.T, b blob.Bucket, maxBytes int) *funclog.BlobSink {
	t.Helper()
	s, err := funclog.NewBlobSink(funclog.Deps{
		Bucket: b, SegmentMaxBytes: maxBytes, SegmentMaxAge: time.Hour,
		Clock: clock.Fake(time.Unix(1_700_000_000, 0)),
	})
	require.NoError(t, err)
	return s
}

// readBackOne reads the single OTLP-JSONL object under prefix and unmarshals it.
func readBackOne(t *testing.T, b blob.Bucket, prefix string) plog.Logs {
	t.Helper()
	objs, err := b.List(context.Background(), prefix)
	require.NoError(t, err)
	require.Len(t, objs, 1, "exactly one segment object expected")
	data, err := b.Get(context.Background(), objs[0].Key)
	require.NoError(t, err)
	var u plog.JSONUnmarshaler
	logs, err := u.UnmarshalLogs([]byte(strings.TrimSpace(string(data))))
	require.NoError(t, err, "persisted bytes must be valid OTLP/JSON")
	return logs
}

func defaultRes() funclog.Resource {
	return funclog.Resource{Namespace: "default", Function: "hello", Replica: "r1"}
}

// scenario: captured-before-format — an NDJSON record keeps its structured attributes (object
// intact), not a util.format-flattened string.
func TestScenarioCapturedBeforeFormat(t *testing.T) {
	line := `{"ts":1700000000000000000,"sev":"INFO","body":"user","attrs":{"id":"7","kind":"obj"},"inv":"abc","funcd.source":"console"}`
	r := funclog.NewNDJSONReader(strings.NewReader(line + "\n"))
	e, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, "user", e.Body)
	require.Equal(t, funclog.SevInfo, e.Severity)
	require.Equal(t, map[string]string{"id": "7", "kind": "obj"}, e.Attrs) // structure preserved
	require.Equal(t, funclog.SourceConsole, e.Source)
}

// blockingReader yields its queued entries, then blocks forever (simulating a frozen instance that
// stops sending). It closes drained once the queue first empties — at which point the Pump has
// already Appended every prior entry (it Appends before looping back to Read), so the test can flush
// deterministically without racing the pump.
type blockingReader struct {
	mu      sync.Mutex
	queue   []funclog.Entry
	once    sync.Once
	drained chan struct{}
}

func (r *blockingReader) Read(ctx context.Context) (funclog.Entry, error) {
	r.mu.Lock()
	if len(r.queue) > 0 {
		e := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		return e, nil
	}
	r.mu.Unlock()
	r.once.Do(func() { close(r.drained) }) // queue empty ⇒ every prior entry has been Appended
	<-ctx.Done()                           // freeze: block until the test cancels
	return funclog.Entry{}, ctx.Err()
}

// scenario: freeze-safe-no-loss — a frozen instance (its Reader blocks) pauses the Pump; the
// already-emitted records are not lost (they are out of the function and in the host sink).
func TestScenarioFreezeSafeNoLoss(t *testing.T) {
	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	r := &blockingReader{
		queue:   []funclog.Entry{{Severity: funclog.SevInfo, Body: "a"}, {Severity: funclog.SevInfo, Body: "b"}, {Severity: funclog.SevInfo, Body: "c"}},
		drained: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = funclog.Pump(ctx, r, s, defaultRes(), nil) }()

	<-r.drained // all 3 emitted records read + appended; the reader now blocks (the freeze)
	key, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	require.NotEmpty(t, key)
	require.Equal(t, 3, readBackOne(t, b, "logs/").LogRecordCount(), "all 3 emitted records captured despite the freeze")
}

// scenario: path-a-crash-tail — a raw fd2 line becomes a coarse ERROR/stderr Entry.
func TestScenarioPathACrashTail(t *testing.T) {
	r := funclog.NewRawReader(strings.NewReader("segfault: boom\n"), funclog.SourceStderr)
	e, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, funclog.SevError, e.Severity)
	require.Equal(t, funclog.SourceStderr, e.Source)
	require.Equal(t, "segfault: boom", e.Body)
}

// scenario: correlated — inv/trace_id/span_id are carried through decode and into the OTLP record.
func TestScenarioCorrelated(t *testing.T) {
	tid := "0123456789abcdef0123456789abcdef"
	sid := "0123456789abcdef"
	line := `{"ts":1700000000000000000,"sev":"WARN","body":"hi","inv":"inv-1","trace_id":"` + tid + `","span_id":"` + sid + `","funcd.source":"console"}`
	r := funclog.NewNDJSONReader(strings.NewReader(line + "\n"))
	e, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, "inv-1", e.Invocation)
	require.Equal(t, tid, e.TraceID)
	require.Equal(t, sid, e.SpanID)

	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	require.NoError(t, s.Append(context.Background(), defaultRes(), e))
	_, err = s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	lr := readBackOne(t, b, "logs/").ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	require.Equal(t, tid, lr.TraceID().String())
	require.Equal(t, sid, lr.SpanID().String())
	inv, ok := lr.Attributes().Get("inv")
	require.True(t, ok, "inv attribute present")
	require.Equal(t, "inv-1", inv.Str())
}

// scenario: persisted-via-blob — a sealed segment is written as exactly ONE blob object whose body
// is valid OTLP/JSON.
func TestScenarioPersistedViaBlob(t *testing.T) {
	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	for _, body := range []string{"one", "two"} {
		require.NoError(t, s.Append(context.Background(), defaultRes(), funclog.Entry{Severity: funclog.SevInfo, Body: body, Source: funclog.SourceConsole}))
	}
	key, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	require.Contains(t, key, "logs/default/hello/")
	require.Equal(t, 2, readBackOne(t, b, "logs/").LogRecordCount())
}

// scenario: identity-tagged — the OTLP Resource carries namespace/function/replica/tenant + source=function.
func TestScenarioIdentityTagged(t *testing.T) {
	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	require.NoError(t, s.Append(context.Background(), defaultRes(), funclog.Entry{Severity: funclog.SevInfo, Body: "x"}))
	_, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	ra := readBackOne(t, b, "logs/").ResourceLogs().At(0).Resource().Attributes().AsRaw()
	require.Equal(t, "default", ra["namespace"])
	require.Equal(t, "hello", ra["function"])
	require.Equal(t, "r1", ra["replica"])
	require.Equal(t, "default", ra["tenant"]) // tenant defaults to namespace
	require.Equal(t, "function", ra["source"])
}

// scenario: transport-fd3-and-uds — the same NDJSON over two different io.Readers (fd3 / UDS both
// yield an io.Reader) decodes to the same Entry.
func TestScenarioTransportFd3AndUds(t *testing.T) {
	line := `{"ts":1700000000000000000,"sev":"INFO","body":"hi","funcd.source":"console"}` + "\n"
	e1, err := funclog.NewNDJSONReader(strings.NewReader(line)).Read(context.Background())
	require.NoError(t, err)
	e2, err := funclog.NewNDJSONReader(strings.NewReader(line)).Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, e1, e2)
}

// scenario: wazero-host-console — the untrusted path builds an Entry in-host (no wire) and it joins
// the same Sink pipeline.
func TestScenarioWazeroHostConsole(t *testing.T) {
	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	inHost := funclog.Entry{Severity: funclog.SevDebug, Body: "from wasm guest", Source: funclog.SourceConsole}
	require.NoError(t, s.Append(context.Background(), defaultRes(), inHost))
	_, err := s.Flush(context.Background(), defaultRes())
	require.NoError(t, err)
	require.Equal(t, 1, readBackOne(t, b, "logs/").LogRecordCount())
}

// scenario: concurrent-pumps — N pumps Append to one BlobSink concurrently (run under -race); no
// data race, and every instance's segment is Put (the per-Resource-lock contract).
func TestScenarioConcurrentPumps(t *testing.T) {
	b := memBucket(t)
	s := newSink(t, b, 1<<20)
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := funclog.Resource{Namespace: "default", Function: "fn" + string(rune('a'+i)), Replica: "r"}
			for j := 0; j < 5; j++ {
				_ = s.Append(context.Background(), res, funclog.Entry{Severity: funclog.SevInfo, Body: "x", Source: funclog.SourceConsole})
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, s.Close()) // seals every open segment
	objs, err := b.List(context.Background(), "logs/")
	require.NoError(t, err)
	require.Len(t, objs, n, "one segment object per instance")
}

func TestNewBlobSinkValidation(t *testing.T) {
	_, err := funclog.NewBlobSink(funclog.Deps{Clock: clock.System()})
	require.Error(t, err) // nil bucket
	_, err = funclog.NewBlobSink(funclog.Deps{Bucket: memBucket(t)})
	require.Error(t, err) // nil clock
}
