package logread_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

func baseTime() time.Time { return time.Date(2026, 6, 29, 10, 30, 0, 0, time.UTC) }

func memBucket(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	if err != nil {
		t.Fatalf("open mem bucket: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// seedCompacted writes one compacted Parquet object (as the F53 compactor would) at a real silver key.
func seedCompacted(t *testing.T, b blob.Bucket, ns, fn string, windowStart int64, rows []compact.Row) {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[compact.Row](&buf)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close parquet: %v", err)
	}
	date := time.Unix(0, windowStart).UTC().Format("2006-01-02")
	key := fmt.Sprintf("logs/%s/%s/%s/%d.parquet", ns, fn, date, windowStart)
	if err := b.Put(context.Background(), key, buf.Bytes()); err != nil {
		t.Fatalf("put compacted: %v", err)
	}
}

// seedRaw writes one raw OTLP-JSONL object (as the F50 sink would) at a real raw key.
func seedRaw(t *testing.T, b blob.Bucket, ns, fn, replica string, sealNano int64, rows []compact.Row) {
	t.Helper()
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	ra := rl.Resource().Attributes()
	ra.PutStr("source", "function")
	ra.PutStr("namespace", ns)
	ra.PutStr("function", fn)
	ra.PutStr("replica", replica)
	ra.PutStr("tenant", ns)
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("funcd/funclog")
	for _, row := range rows {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(pcommon.Timestamp(row.TimeUnixNano)) //nolint:gosec // test timestamps are positive
		lr.SetSeverityText(row.SeverityText)
		lr.SetSeverityNumber(plog.SeverityNumber(row.SeverityNumber))
		lr.Body().SetStr(row.Body)
		lr.Attributes().PutStr("funcd.source", row.Source)
	}
	var m plog.JSONMarshaler
	data, err := m.MarshalLogs(logs)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	data = append(data, '\n')
	date := time.Unix(0, sealNano).UTC().Format("2006-01-02")
	key := fmt.Sprintf("logs/%s/%s/%s/%d-%s.otlp.jsonl", ns, fn, date, sealNano, replica)
	if err := b.Put(context.Background(), key, data); err != nil {
		t.Fatalf("put raw: %v", err)
	}
}

func row(tNano int64, sev string, sevNum int32, body string) compact.Row {
	return compact.Row{
		TimeUnixNano: tNano, SeverityText: sev, SeverityNumber: sevNum, Body: body,
		Namespace: "default", Function: "fn", Replica: "0", Tenant: "default", Source: "console",
	}
}

// scenario: reads-compacted-and-tail — records from both the compacted Parquet AND the raw tail are merged.
func TestScenarioReadsCompactedAndTail(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedCompacted(t, b, "default", "fn", base, []compact.Row{
		row(base, "INFO", 9, "compacted-1"),
		row(base+1, "INFO", 9, "compacted-2"),
	})
	seedRaw(t, b, "default", "fn", "0", base+1000, []compact.Row{
		row(base+1000, "WARN", 13, "raw-tail-1"),
	})

	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (2 compacted + 1 tail)", len(lines))
	}
	if lines[len(lines)-1].Body != "raw-tail-1" {
		t.Fatalf("newest line = %q, want raw-tail-1 (time-ordered)", lines[len(lines)-1].Body)
	}
}

// scenario: filter-since — only records at/after Since are returned.
func TestScenarioFilterSince(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedCompacted(t, b, "default", "fn", base, []compact.Row{
		row(base, "INFO", 9, "old"),
		row(base+int64(time.Hour), "INFO", 9, "new"),
	})
	since := baseTime().Add(30 * time.Minute)
	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn", Since: since})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 1 || lines[0].Body != "new" {
		t.Fatalf("got %+v, want only 'new'", bodies(lines))
	}
}

// scenario: filter-severity — only records at the requested severity or above.
func TestScenarioFilterSeverity(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedCompacted(t, b, "default", "fn", base, []compact.Row{
		row(base, "INFO", 9, "info"),
		row(base+1, "WARN", 13, "warn"),
		row(base+2, "ERROR", 17, "error"),
	})
	min, ok := logread.SeverityNumber("warn")
	if !ok {
		t.Fatal("severity warn should resolve")
	}
	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn", MinSeverityNumber: min})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := bodies(lines); len(got) != 2 || got[0] != "warn" || got[1] != "error" {
		t.Fatalf("got %v, want [warn error]", got)
	}
}

// scenario: limit-returns-most-recent — the N most-recent records (tail), oldest-first.
func TestScenarioLimitReturnsMostRecent(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	var rows []compact.Row
	for i := 0; i < 10; i++ {
		rows = append(rows, row(base+int64(i), "INFO", 9, fmt.Sprintf("m%d", i)))
	}
	seedCompacted(t, b, "default", "fn", base, rows)

	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn", Limit: 3})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := bodies(lines)
	if len(got) != 3 || got[0] != "m7" || got[2] != "m9" {
		t.Fatalf("got %v, want the 3 most-recent oldest-first [m7 m8 m9]", got)
	}
}

// scenario: empty-when-none — a function with no logs returns an empty slice, not an error.
func TestScenarioEmptyWhenNone(t *testing.T) {
	b := memBucket(t)
	lines, err := logread.NewBlobReader(b).Read(context.Background(), logread.Query{Namespace: "default", Function: "ghost"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("got %d lines, want 0", len(lines))
	}
}

// getHookBucket runs hook once, before the first Get: after the reader's List, before it reads an object.
type getHookBucket struct {
	blob.Bucket
	once sync.Once
	hook func()
}

func (b *getHookBucket) Get(ctx context.Context, key string) ([]byte, error) {
	b.once.Do(b.hook)
	return b.Bucket.Get(ctx, key)
}

// A compaction pass that folds the listed raw tail into Parquet between the reader's List and its Gets
// must not fail the read or lose the window: its lines are in the new Parquet.
func TestIssue151_ReadDuringCompactionReturnsCompactedLines(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	for i := int64(0); i < 3; i++ {
		seedRaw(t, b, "default", "fn", "0", base+i, []compact.Row{row(base+i, "INFO", 9, fmt.Sprintf("m%d", i))})
	}
	c, err := compact.New(compact.Deps{Bucket: b, Clock: clock.Fake(baseTime().Add(2 * compact.DefaultWindow))})
	if err != nil {
		t.Fatalf("compact.New: %v", err)
	}
	hooked := &getHookBucket{Bucket: b, hook: func() {
		st, cerr := c.CompactOnce(context.Background())
		if cerr != nil || st.RawDeleted != 3 {
			t.Errorf("CompactOnce = %+v, %v; want 3 raw deleted", st, cerr)
		}
	}}

	lines, err := logread.NewBlobReader(hooked).Read(context.Background(), logread.Query{Namespace: "default", Function: "fn"})
	if err != nil {
		t.Fatalf("Read during compaction: %v", err)
	}
	if got := bodies(lines); len(got) != 3 || got[0] != "m0" || got[2] != "m2" {
		t.Fatalf("got %v, want [m0 m1 m2] from the new Parquet", got)
	}
}

func bodies(lines []logread.Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Body
	}
	return out
}

// Issue #84: one undecodable raw object (a torn write) must fail neither the function's read nor a
// namespace-wide run read; every readable object is still returned.
func TestIssue84_UndecodableRawObjectDoesNotFailReads(t *testing.T) {
	ctx := context.Background()
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedRaw(t, b, "default", "mmm", "0", base, []compact.Row{row(base, "INFO", 9, "good")})
	date := time.Unix(0, base).UTC().Format("2006-01-02")
	badKey := fmt.Sprintf("logs/default/mmm/%s/%d-1.otlp.jsonl", date, base+1)
	if err := b.Put(ctx, badKey, []byte(`{"resourceLogs":[{"resource":{"attributes":[`)); err != nil {
		t.Fatalf("put truncated raw: %v", err)
	}
	seedCompacted(t, b, "default", "step", base, []compact.Row{rowTrace(base, "run-A line", "step", traceA)})

	r := logread.NewBlobReader(b)
	lines, err := r.Read(ctx, logread.Query{Namespace: "default", Function: "mmm"})
	if err != nil {
		t.Fatalf("function read: %v", err)
	}
	if got := bodies(lines); len(got) != 1 || got[0] != "good" {
		t.Fatalf("function read = %v, want [good]", got)
	}
	lines, err = r.Read(ctx, logread.Query{Namespace: "default", TraceID: traceA})
	if err != nil {
		t.Fatalf("namespace-wide run read: %v", err)
	}
	if got := bodies(lines); len(got) != 1 || got[0] != "run-A line" {
		t.Fatalf("namespace-wide run read = %v, want [run-A line]", got)
	}
}
