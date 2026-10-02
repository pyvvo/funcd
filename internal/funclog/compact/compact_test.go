package compact_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// baseTime is a fixed seal instant well inside a UTC day; window math is independent of it.
func baseTime() time.Time { return time.Date(2026, 6, 29, 10, 30, 0, 0, time.UTC) }

const testWindow = time.Hour

// logRec is one record to seed into a raw OTLP-JSONL object (mirrors the funclog sink's marshal).
type logRec struct {
	ts     int64
	sev    string
	sevNum int32
	body   string
	source string
	inv    string
	attrs  map[string]string
	trace  string // hex32, optional
	span   string // hex16, optional
}

func memBucket(t *testing.T) blob.Bucket {
	t.Helper()
	b, err := gocloud.Open(context.Background(), "mem://")
	if err != nil {
		t.Fatalf("open mem bucket: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// seedRaw writes one raw OTLP-JSON-Lines object exactly as the ADR-0081 sink would, at the real raw
// key logs/<ns>/<fn>/<date>/<sealNano>-<replica>.otlp.jsonl.
func seedRaw(t *testing.T, b blob.Bucket, ns, fn, replica string, sealNano int64, recs []logRec) {
	t.Helper()
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	ra := rl.Resource().Attributes()
	ra.PutStr("source", "function")
	ra.PutStr("namespace", ns)
	ra.PutStr("function", fn)
	if replica != "" {
		ra.PutStr("replica", replica)
	}
	ra.PutStr("tenant", ns)
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName("funcd/funclog")
	for _, r := range recs {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(pcommon.Timestamp(r.ts)) //nolint:gosec // test timestamps are positive
		lr.SetSeverityText(r.sev)
		lr.SetSeverityNumber(plog.SeverityNumber(r.sevNum))
		lr.Body().SetStr(r.body)
		la := lr.Attributes()
		la.PutStr("funcd.source", r.source)
		if r.inv != "" {
			la.PutStr("inv", r.inv)
		}
		for k, v := range r.attrs {
			la.PutStr(k, v)
		}
		if r.trace != "" {
			var tid pcommon.TraceID
			hb, _ := hex.DecodeString(r.trace)
			copy(tid[:], hb)
			lr.SetTraceID(tid)
		}
		if r.span != "" {
			var sid pcommon.SpanID
			hb, _ := hex.DecodeString(r.span)
			copy(sid[:], hb)
			lr.SetSpanID(sid)
		}
	}
	var m plog.JSONMarshaler
	data, err := m.MarshalLogs(logs)
	if err != nil {
		t.Fatalf("marshal OTLP: %v", err)
	}
	data = append(data, '\n')
	rep := replica
	if rep == "" {
		rep = "na"
	}
	date := time.Unix(0, sealNano).UTC().Format("2006-01-02")
	key := fmt.Sprintf("logs/%s/%s/%s/%d-%s.otlp.jsonl", ns, fn, date, sealNano, rep)
	if err := b.Put(context.Background(), key, data); err != nil {
		t.Fatalf("put raw: %v", err)
	}
}

// listSuffix returns every key under logs/ ending in suffix.
func listSuffix(t *testing.T, b blob.Bucket, suffix string) []string {
	t.Helper()
	attrs, err := b.List(context.Background(), "logs/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out []string
	for _, a := range attrs {
		if len(a.Key) >= len(suffix) && a.Key[len(a.Key)-len(suffix):] == suffix {
			out = append(out, a.Key)
		}
	}
	return out
}

func readCompacted(t *testing.T, b blob.Bucket, key string) []compact.Row {
	t.Helper()
	data, err := b.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get compacted %q: %v", key, err)
	}
	rows, err := parquet.Read[compact.Row](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parquet read %q: %v", key, err)
	}
	return rows
}

func newCompactor(t *testing.T, b blob.Bucket, now time.Time, retention time.Duration) *compact.Compactor {
	t.Helper()
	c, err := compact.New(compact.Deps{
		Bucket: b, Clock: clock.Fake(now), Window: testWindow, Interval: time.Minute, Retention: retention,
	})
	if err != nil {
		t.Fatalf("compact.New: %v", err)
	}
	return c
}

// scenario: compacts-closed-window — closed-window raw (two replicas) folds into one Parquet, all rows.
func TestScenarioCompactsClosedWindow(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", sealNano, []logRec{
		{ts: sealNano, sev: "INFO", sevNum: 9, body: "a", source: "console", inv: "i1"},
		{ts: sealNano + 1, sev: "WARN", sevNum: 13, body: "b", source: "console", inv: "i1"},
	})
	seedRaw(t, b, "default", "fn", "1", sealNano+2, []logRec{
		{ts: sealNano + 2, sev: "ERROR", sevNum: 17, body: "c", source: "console", inv: "i2"},
	})

	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0) // window closed
	st, err := c.CompactOnce(context.Background())
	if err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	if st.Windows != 1 || st.Rows != 3 || st.RawDeleted != 2 {
		t.Fatalf("stats = %+v, want Windows=1 Rows=3 RawDeleted=2", st)
	}
	compacted := listSuffix(t, b, ".parquet")
	if len(compacted) != 1 {
		t.Fatalf("compacted objects = %v, want exactly 1", compacted)
	}
	rows := readCompacted(t, b, compacted[0])
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
}

// scenario: parquet-roundtrips — every typed column + attrs_json survives the Parquet round-trip.
func TestScenarioParquetRoundtrips(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	const trace = "0123456789abcdef0123456789abcdef"
	const span = "0123456789abcdef"
	seedRaw(t, b, "ns1", "svc", "2", sealNano, []logRec{{
		ts: sealNano, sev: "INFO", sevNum: 9, body: "hello", source: "logging", inv: "inv-7",
		attrs: map[string]string{"item": "42", "phase": "scan"}, trace: trace, span: span,
	}})

	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0)
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	rows := readCompacted(t, b, listSuffix(t, b, ".parquet")[0])
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.TimeUnixNano != sealNano || r.SeverityText != "INFO" || r.SeverityNumber != 9 || r.Body != "hello" {
		t.Fatalf("record columns wrong: %+v", r)
	}
	if r.Namespace != "ns1" || r.Function != "svc" || r.Replica != "2" || r.Tenant != "ns1" {
		t.Fatalf("identity columns wrong: %+v", r)
	}
	if r.Source != "logging" || r.Invocation != "inv-7" || r.TraceID != trace || r.SpanID != span {
		t.Fatalf("lifted columns wrong: %+v", r)
	}
	// attrs_json carries the remaining attrs (not funcd.source/inv).
	if r.AttrsJSON == "" || !bytes.Contains([]byte(r.AttrsJSON), []byte(`"item":"42"`)) ||
		bytes.Contains([]byte(r.AttrsJSON), []byte("funcd.source")) || bytes.Contains([]byte(r.AttrsJSON), []byte(`"inv"`)) {
		t.Fatalf("attrs_json wrong: %q", r.AttrsJSON)
	}
}

// scenario: partitioned-by-ns-fn-date — the compacted key is Hive-partitioned by ns/fn/date with the window start.
func TestScenarioPartitionedByNsFnDate(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "team-a", "worker", "0", sealNano, []logRec{{ts: sealNano, sev: "INFO", sevNum: 9, body: "x", source: "console"}})

	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0)
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	windowStart := (sealNano / int64(testWindow)) * int64(testWindow)
	date := time.Unix(0, windowStart).UTC().Format("2006-01-02")
	want := fmt.Sprintf("logs/team-a/worker/%s/%d.parquet", date, windowStart)
	got := listSuffix(t, b, ".parquet")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("compacted key = %v, want %q", got, want)
	}
}

// scenario: raw-deleted-after-durable-write — consumed raw is gone, compacted present, after a pass.
func TestScenarioRawDeletedAfterDurableWrite(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", sealNano, []logRec{{ts: sealNano, sev: "INFO", sevNum: 9, body: "x", source: "console"}})

	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0)
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 0 {
		t.Fatalf("raw still present: %v", raw)
	}
	if compacted := listSuffix(t, b, ".parquet"); len(compacted) != 1 {
		t.Fatalf("compacted = %v, want 1", compacted)
	}
}

// scenario: recent-raw-preserved — an open-window raw object is neither compacted nor deleted.
func TestScenarioRecentRawPreserved(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", sealNano, []logRec{{ts: sealNano, sev: "INFO", sevNum: 9, body: "x", source: "console"}})

	c := newCompactor(t, b, baseTime().Add(time.Minute), 0) // window still open (< base+window)
	st, err := c.CompactOnce(context.Background())
	if err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	if st.Windows != 0 || st.RawDeleted != 0 {
		t.Fatalf("stats = %+v, want no work", st)
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 1 {
		t.Fatalf("raw should be preserved, got %v", raw)
	}
	if compacted := listSuffix(t, b, ".parquet"); len(compacted) != 0 {
		t.Fatalf("no compacted should be written, got %v", compacted)
	}
}

// failDeleteBucket fails Delete while failDelete is set — to simulate a crash after the Parquet Put but before
// the raw delete.
type failDeleteBucket struct {
	blob.Bucket
	failDelete bool
}

func (b *failDeleteBucket) Delete(ctx context.Context, key string) error {
	if b.failDelete {
		return fmt.Errorf("injected delete failure")
	}
	return b.Bucket.Delete(ctx, key)
}

// scenario: crash-safe-no-loss — Parquet-before-delete + deterministic key: a failed delete loses nothing and
// the retry overwrites the same compacted key (one object, no duplicate).
func TestScenarioCrashSafeNoLoss(t *testing.T) {
	mem := memBucket(t)
	b := &failDeleteBucket{Bucket: mem, failDelete: true}
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", sealNano, []logRec{
		{ts: sealNano, sev: "INFO", sevNum: 9, body: "a", source: "console"},
		{ts: sealNano + 1, sev: "INFO", sevNum: 9, body: "b", source: "console"},
	})

	// Pass 1: the Parquet is written, but the raw delete fails — no loss (rows are in compacted), raw remains.
	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0)
	if _, err := c.CompactOnce(context.Background()); err == nil {
		t.Fatal("expected the injected delete failure")
	}
	if compacted := listSuffix(t, b, ".parquet"); len(compacted) != 1 {
		t.Fatalf("compacted after pass1 = %v, want 1 (durable before delete)", compacted)
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 1 {
		t.Fatalf("raw should remain after failed delete, got %v", raw)
	}

	// Pass 2 (delete now works): re-writes the SAME deterministic compacted key and finishes the delete.
	b.failDelete = false
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("pass2: %v", err)
	}
	compacted := listSuffix(t, b, ".parquet")
	if len(compacted) != 1 {
		t.Fatalf("compacted after pass2 = %v, want exactly 1 (idempotent overwrite, no duplicate)", compacted)
	}
	if rows := readCompacted(t, mem, compacted[0]); len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (no loss)", len(rows))
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 0 {
		t.Fatalf("raw should be deleted after pass2, got %v", raw)
	}
}

// scenario: retention-prunes-compacted — compacted older than Retention is deleted; one within is kept.
func TestScenarioRetentionPrunesCompacted(t *testing.T) {
	b := memBucket(t)
	now := baseTime().Add(48 * time.Hour)
	oldStart := baseTime().Add(-2 * time.Hour).UnixNano() // ~50h old
	freshStart := now.Add(-30 * time.Minute).UnixNano()
	put := func(start int64) string {
		date := time.Unix(0, start).UTC().Format("2006-01-02")
		key := fmt.Sprintf("logs/default/fn/%s/%d.parquet", date, start)
		if err := b.Put(context.Background(), key, []byte("compacted")); err != nil {
			t.Fatalf("seed compacted: %v", err)
		}
		return key
	}
	oldKey, freshKey := put(oldStart), put(freshStart)

	c := newCompactor(t, b, now, time.Hour) // retention 1h
	st, err := c.CompactOnce(context.Background())
	if err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	if st.CompactedPruned != 1 {
		t.Fatalf("CompactedPruned = %d, want 1", st.CompactedPruned)
	}
	if ok, _ := b.Exists(context.Background(), oldKey); ok {
		t.Fatal("old compacted should be pruned")
	}
	if ok, _ := b.Exists(context.Background(), freshKey); !ok {
		t.Fatal("fresh compacted should be kept")
	}
}

// scenario: disabled-config (compact half) — an all-open-windows corpus yields a no-op pass; and Retention<=0
// keeps compacted forever (the daemon-disable half is a pkg/funcd internal test).
func TestScenarioDisabledConfigNoOp(t *testing.T) {
	b := memBucket(t)
	// A future-dated raw object (window not yet closed) + an old compacted kept because retention is 0.
	sealNano := baseTime().UnixNano()
	seedRaw(t, b, "default", "fn", "0", sealNano, []logRec{{ts: sealNano, sev: "INFO", sevNum: 9, body: "x", source: "console"}})
	oldStart := baseTime().Add(-1000 * time.Hour).UnixNano()
	date := time.Unix(0, oldStart).UTC().Format("2006-01-02")
	oldCompacted := fmt.Sprintf("logs/default/fn/%s/%d.parquet", date, oldStart)
	if err := b.Put(context.Background(), oldCompacted, []byte("compacted")); err != nil {
		t.Fatalf("seed compacted: %v", err)
	}

	c := newCompactor(t, b, baseTime().Add(time.Minute), 0) // window open + retention 0 (keep forever)
	st, err := c.CompactOnce(context.Background())
	if err != nil {
		t.Fatalf("CompactOnce: %v", err)
	}
	if (st != compact.Stats{}) {
		t.Fatalf("stats = %+v, want empty (no-op)", st)
	}
	if ok, _ := b.Exists(context.Background(), oldCompacted); !ok {
		t.Fatal("retention 0 must keep compacted forever")
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 1 {
		t.Fatalf("open-window raw must be untouched, got %v", raw)
	}
}

// Issue #83: a raw segment that lands in a window a pass already compacted (a Put in flight across the window
// boundary, or a wall clock stepped back) is merged into that window's Parquet instead of replacing it.
func TestIssue83_LateSegmentKeepsCompactedRows(t *testing.T) {
	b := memBucket(t)
	sealNano := baseTime().UnixNano()
	early := make([]logRec, 100)
	for i := range early {
		early[i] = logRec{ts: sealNano + int64(i), sev: "INFO", sevNum: 9, body: fmt.Sprintf("early-%d", i), source: "console"}
	}
	seedRaw(t, b, "default", "fn", "0", sealNano, early)

	c := newCompactor(t, b, baseTime().Add(testWindow+time.Minute), 0)
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("pass1: %v", err)
	}
	lateNano := baseTime().Add(30*time.Minute - 10*time.Millisecond).UnixNano()
	seedRaw(t, b, "default", "fn", "0", lateNano, []logRec{{ts: lateNano, sev: "INFO", sevNum: 9, body: "late", source: "console"}})
	if _, err := c.CompactOnce(context.Background()); err != nil {
		t.Fatalf("pass2: %v", err)
	}

	compacted := listSuffix(t, b, ".parquet")
	if len(compacted) != 1 {
		t.Fatalf("compacted = %v, want exactly 1", compacted)
	}
	if rows := readCompacted(t, b, compacted[0]); len(rows) != len(early)+1 {
		t.Fatalf("rows after the late segment = %d, want %d (earlier compacted rows + the late one)", len(rows), len(early)+1)
	}
	if raw := listSuffix(t, b, ".otlp.jsonl"); len(raw) != 0 {
		t.Fatalf("raw should be deleted, got %v", raw)
	}
}
