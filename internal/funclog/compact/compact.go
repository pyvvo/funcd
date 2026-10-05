// Package compact folds the funclog "raw" raw OTLP-JSON-Lines objects (ADR-0081) into columnar Parquet
// "compacted", time-windowed and Hive-partitioned, through the blob.Bucket port (ADR-0007). It is an always-on
// daemon-internal pipeline — pure-Go, no cgo, lakehouse-independent — internal substrate for the log-ingest
// capability, not a function-bindable provider (ADR-0082). The schema is log-specific; the timer/blob/partition
// skeleton is the seam F51/F52 reuse (windowing by SEAL time, not event time — see the close invariant in keys.go).
package compact

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// Default window / interval / retention (overridable via Deps). Interval < Window is intentional: an open
// window is re-passed harmlessly (idempotently) until it closes, then compacted once.
const (
	DefaultWindow    = time.Hour
	DefaultInterval  = 5 * time.Minute
	DefaultRetention = 720 * time.Hour // 30d
)

// LogRecord attribute keys lifted into typed Row columns; everything else goes to attrs_json.
const (
	attrSource = "funcd.source" // → Row.Source
	attrInv    = "inv"          // → Row.Invocation
)

// rawKeysMeta is the compacted Parquet key/value metadata entry listing the raw keys its rows came from, so a later
// pass merges a late raw object into the window (issue #83) and skips raw already folded in (a retried delete).
const rawKeysMeta = "funcd.raw_keys"

// Row is one LogRecord as a Parquet row: fixed typed columns + a single attrs_json column for the rest. The
// schema is STABLE (F54 reads it); new OTLP attributes land in attrs_json, never as new columns.
type Row struct {
	TimeUnixNano   int64  `parquet:"ts"`
	SeverityText   string `parquet:"severity_text"`
	SeverityNumber int32  `parquet:"severity_number"`
	Body           string `parquet:"body"`
	Namespace      string `parquet:"namespace"`
	Function       string `parquet:"function"`
	Replica        string `parquet:"replica"`
	Tenant         string `parquet:"tenant"`
	Source         string `parquet:"source"`     // lifted from the funcd.source attr (console/logging/stdout/stderr)
	Invocation     string `parquet:"inv"`        // lifted from the inv attr — the invocation id (F54 filters on it)
	TraceID        string `parquet:"trace_id"`   // hex32, "" when absent
	SpanID         string `parquet:"span_id"`    // hex16, "" when absent
	AttrsJSON      string `parquet:"attrs_json"` // remaining LogRecord attributes (minus funcd.source + inv) as JSON
}

// Deps constructs a Compactor. Bucket is the funcd-system observability bucket (the same blob the funclog sink
// writes). Window/Interval default when non-positive; Retention <= 0 ⇒ keep compacted forever (no pruning).
type Deps struct {
	Bucket    blob.Bucket
	Clock     clock.Clock
	Logger    *slog.Logger
	Window    time.Duration // bucket size AND closed-ness threshold; a window closes at windowStart+Window
	Interval  time.Duration // timer cadence between passes
	Retention time.Duration // delete compacted older than this; <= 0 ⇒ keep forever
}

// Stats is one pass's outcome (logging/tests): windows compacted, rows written, raw deleted, compacted pruned.
type Stats struct {
	Windows         int
	Rows            int
	RawDeleted      int
	CompactedPruned int
}

// Compactor is the daemon-internal compaction pipeline. Construct with New; Run owns the timer loop.
type Compactor struct {
	bucket    blob.Bucket
	clock     clock.Clock
	log       *slog.Logger
	window    time.Duration
	interval  time.Duration
	retention time.Duration
}

// New validates deps and builds the compactor (Bucket + Clock required; Window/Interval default when zero,
// Retention <= 0 keeps compacted forever).
func New(d Deps) (*Compactor, error) {
	const op = "compact.New"
	if d.Bucket == nil {
		return nil, fault.Invalidf(op, "blob bucket is required")
	}
	if d.Clock == nil {
		return nil, fault.Invalidf(op, "clock is required")
	}
	if d.Window <= 0 {
		d.Window = DefaultWindow
	}
	if d.Interval <= 0 {
		d.Interval = DefaultInterval
	}
	if d.Retention < 0 {
		d.Retention = 0 // normalize: <= 0 ⇒ keep forever
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Compactor{
		bucket: d.Bucket, clock: d.Clock, log: d.Logger,
		window: d.Window, interval: d.Interval, retention: d.Retention,
	}, nil
}

// Run ticks every Interval, calling CompactOnce each tick, until ctx is cancelled (then returns nil). A pass
// error is logged and the loop continues (crash-only: the next pass retries idempotently).
func (c *Compactor) Run(ctx context.Context) error {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			st, err := c.CompactOnce(ctx)
			switch {
			case err != nil:
				c.log.WarnContext(ctx, "funclog compaction pass failed", "error", err)
			case st.Windows > 0 || st.CompactedPruned > 0:
				c.log.InfoContext(ctx, "funclog compaction",
					"windows", st.Windows, "rows", st.Rows,
					"rawDeleted", st.RawDeleted, "compactedPruned", st.CompactedPruned)
			}
		}
	}
}

// window groups the raw keys of one closed (ns, fn, windowStart).
type window struct {
	ns    string
	fn    string
	start int64
	raw   []string
}

// CompactOnce runs exactly one pass: discover raw → group into closed windows → merge into one Parquet per
// (ns, fn, window) → delete the consumed raw (only after the Put succeeds) → prune compacted past Retention.
// Deterministic and idempotent: the window's existing Parquet is read back and its rows kept, and raw it already
// holds is not re-added, so a re-pass rewrites the same compacted key with no loss and no duplicate. A window
// holding an undecodable raw object is logged and left raw; the rest of the pass goes on.
func (c *Compactor) CompactOnce(ctx context.Context) (Stats, error) {
	const op = "compact.Compactor.CompactOnce"
	objs, err := c.bucket.List(ctx, logsPrefix)
	if err != nil {
		return Stats{}, fault.Wrapf(err, fault.KindOf(err), op, "list raw")
	}
	nowNano := c.clock.Now().UTC().UnixNano()

	var st Stats
	for _, w := range c.groupClosedWindows(objs, nowNano) {
		rows, rawDeleted, err := c.compactWindow(ctx, w)
		st.RawDeleted += rawDeleted
		if err != nil {
			return st, err
		}
		if rows > 0 {
			st.Windows++
			st.Rows += rows
		}
	}
	st.CompactedPruned, err = c.pruneCompacted(ctx, objs, nowNano)
	return st, err
}

// groupClosedWindows groups the raw keys of closed windows, sorted by (ns, fn, start) for a deterministic pass. A
// window is closed when now >= windowStart+Window; open windows are the live tail F54 still reads as JSONL — they
// are left untouched.
func (c *Compactor) groupClosedWindows(objs []blob.Attributes, nowNano int64) []*window {
	grouped := map[string]*window{}
	for _, o := range objs {
		bk, ok := parseRawKey(o.Key)
		if !ok {
			continue
		}
		start := windowStartNano(bk.sealNano, c.window)
		if nowNano < start+int64(c.window) {
			continue
		}
		gk := bk.ns + "\x00" + bk.fn + "\x00" + strconv.FormatInt(start, 10)
		w := grouped[gk]
		if w == nil {
			w = &window{ns: bk.ns, fn: bk.fn, start: start}
			grouped[gk] = w
		}
		w.raw = append(w.raw, bk.key)
	}
	windows := make([]*window, 0, len(grouped))
	for _, w := range grouped {
		windows = append(windows, w)
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].ns != windows[j].ns {
			return windows[i].ns < windows[j].ns
		}
		if windows[i].fn != windows[j].fn {
			return windows[i].fn < windows[j].fn
		}
		return windows[i].start < windows[j].start
	})
	return windows
}

// compactWindow merges the window's pending raw into its compacted Parquet, then deletes the window's raw — only
// after the Put succeeds (crash-safe: a crash between them re-Puts the same deterministic compacted key next pass,
// no loss, no duplicate). It returns the rows written (0 when the window is skipped) and the raw deleted so far.
func (c *Compactor) compactWindow(ctx context.Context, w *window) (rows, rawDeleted int, err error) {
	const op = "compact.Compactor.compactWindow"
	key := compactedKey(w.ns, w.fn, w.start)
	kept, folded, err := c.readCompacted(ctx, key)
	if err != nil {
		return 0, 0, err
	}
	var pending []string
	for _, bkey := range w.raw {
		if _, ok := folded[bkey]; !ok {
			pending = append(pending, bkey)
			folded[bkey] = struct{}{}
		}
	}
	fresh, err := c.readRaw(ctx, pending)
	if fault.KindOf(err) == fault.Invalid {
		// An undecodable object never heals on retry, so it must not stall the pass. The whole window stays
		// raw: compacting only its good objects would later be overwritten at the same deterministic key.
		c.log.WarnContext(ctx, "funclog compaction skipped a window with an undecodable raw object", "error", err)
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	kept = append(kept, fresh...)
	if len(kept) == 0 {
		return 0, 0, nil
	}
	if err := c.writeCompacted(ctx, key, kept, folded); err != nil {
		return 0, 0, err
	}
	for _, bkey := range w.raw {
		if err := c.bucket.Delete(ctx, bkey); err != nil {
			return 0, rawDeleted, fault.Wrapf(err, fault.KindOf(err), op, "delete raw %q", bkey)
		}
		rawDeleted++
	}
	return len(kept), rawDeleted, nil
}

// pruneCompacted deletes compacted objects older than Retention (0 ⇒ keep forever) and returns how many it deleted.
// It works on the pre-write listing, so the objects this pass just wrote (current windows) are never candidates.
func (c *Compactor) pruneCompacted(ctx context.Context, objs []blob.Attributes, nowNano int64) (int, error) {
	const op = "compact.Compactor.pruneCompacted"
	if c.retention <= 0 {
		return 0, nil
	}
	cutoff := nowNano - int64(c.retention)
	pruned := 0
	for _, o := range objs {
		ws, ok := parseCompactedWindowStart(o.Key)
		if !ok || ws >= cutoff {
			continue
		}
		if err := c.bucket.Delete(ctx, o.Key); err != nil {
			return pruned, fault.Wrapf(err, fault.KindOf(err), op, "prune compacted %q", o.Key)
		}
		pruned++
	}
	return pruned, nil
}

// readCompacted reads the window's existing Parquet at key, if any: its rows and the raw keys already folded into it.
func (c *Compactor) readCompacted(ctx context.Context, key string) ([]Row, map[string]struct{}, error) {
	const op = "compact.Compactor.readCompacted"
	folded := map[string]struct{}{}
	data, err := c.bucket.Get(ctx, key)
	if fault.KindOf(err) == fault.NotFound {
		return nil, folded, nil
	}
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), op, "get compacted %q", key)
	}
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "open compacted %q", key)
	}
	if v, ok := f.Lookup(rawKeysMeta); ok {
		for _, bkey := range strings.Split(v, "\n") {
			folded[bkey] = struct{}{}
		}
	}
	rows, err := parquet.Read[Row](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "read compacted %q", key)
	}
	return rows, folded, nil
}

// readRaw reads + decodes every raw object of a window into Rows (one Row per LogRecord).
func (c *Compactor) readRaw(ctx context.Context, keys []string) ([]Row, error) {
	const op = "compact.Compactor.readRaw"
	var rows []Row
	for _, key := range keys {
		data, err := c.bucket.Get(ctx, key)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "get raw %q", key)
		}
		decoded, err := DecodeJSONL(data)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "decode raw %q", key)
		}
		rows = append(rows, decoded...)
	}
	return rows, nil
}

// DecodeJSONL decodes one raw OTLP-JSON-Lines object's bytes into Rows (one per LogRecord) — the per-line decode
// the compactor and the ADR-0084 reader share: split on '\n', skip blank lines, plog.JSONUnmarshaler.UnmarshalLogs
// each line (a bad line ⇒ fault.Invalid), appendRows. An additive export of the compactor's existing loop.
func DecodeJSONL(data []byte) ([]Row, error) {
	const op = "compact.DecodeJSONL"
	var um plog.JSONUnmarshaler
	var rows []Row
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		logs, err := um.UnmarshalLogs(line)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "decode OTLP-JSONL line")
		}
		rows = appendRows(rows, logs)
	}
	return rows, nil
}

// writeCompacted marshals rows to Parquet, recording the raw keys they came from, and Puts them at the window's
// deterministic compacted key.
func (c *Compactor) writeCompacted(ctx context.Context, key string, rows []Row, rawKeys map[string]struct{}) error {
	const op = "compact.Compactor.writeCompacted"
	var buf bytes.Buffer
	pw := parquet.NewGenericWriter[Row](&buf,
		parquet.KeyValueMetadata(rawKeysMeta, strings.Join(slices.Sorted(maps.Keys(rawKeys)), "\n")))
	if _, err := pw.Write(rows); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "write parquet rows")
	}
	if err := pw.Close(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "close parquet writer")
	}
	if err := c.bucket.Put(ctx, key, buf.Bytes(), blob.PutOptions{}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "put compacted %q", key)
	}
	return nil
}

// appendRows flattens one plog.Logs document into Rows (resource attrs → identity columns; record fields +
// attrs → the rest, with funcd.source/inv lifted to columns and the remainder to attrs_json).
func appendRows(rows []Row, logs plog.Logs) []Row {
	rls := logs.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		rl := rls.At(i)
		ra := rl.Resource().Attributes()
		ns := attrStr(ra, "namespace")
		fn := attrStr(ra, "function")
		replica := attrStr(ra, "replica")
		tenant := attrStr(ra, "tenant")
		sls := rl.ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			lrs := sls.At(j).LogRecords()
			for k := 0; k < lrs.Len(); k++ {
				lr := lrs.At(k)
				row := Row{
					TimeUnixNano:   int64(lr.Timestamp()),
					SeverityText:   lr.SeverityText(),
					SeverityNumber: int32(lr.SeverityNumber()),
					Body:           lr.Body().AsString(),
					Namespace:      ns,
					Function:       fn,
					Replica:        replica,
					Tenant:         tenant,
				}
				la := lr.Attributes()
				row.Source = attrStr(la, attrSource)
				row.Invocation = attrStr(la, attrInv)
				row.AttrsJSON = attrsJSON(la, attrSource, attrInv)
				if tid := lr.TraceID(); !tid.IsEmpty() {
					row.TraceID = hex.EncodeToString(tid[:])
				}
				if sid := lr.SpanID(); !sid.IsEmpty() {
					row.SpanID = hex.EncodeToString(sid[:])
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// attrStr reads a string attribute (empty when absent).
func attrStr(m pcommon.Map, key string) string {
	if v, ok := m.Get(key); ok {
		return v.AsString()
	}
	return ""
}

// attrsJSON marshals the LogRecord attributes minus the lifted keys to a JSON object string ("{}" when empty).
func attrsJSON(m pcommon.Map, exclude ...string) string {
	raw := m.AsRaw()
	for _, k := range exclude {
		delete(raw, k)
	}
	if len(raw) == 0 {
		return "{}"
	}
	b, err := json.Marshal(raw)
	if err != nil { // attrs are scalar strings — marshal does not fail in practice
		return "{}"
	}
	return string(b)
}
