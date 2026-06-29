# ADR-0083: Time-windowed compaction of function-log OTLP-JSONL into partitioned Parquet

- **Status**: Implemented
- **Date**: 2026-06-29 (Accepted 2026-06-29 after one judge pass — folded M1 config surface to `pkg/funcd`
  options-only [no `internal/platform/config` YAML, mirroring ADR-0081], M2 reframed as internal substrate not an
  ADR-0082 catalog provider, and the Minor cluster: stated the seal-time window-close invariant [no grace needed,
  F51/F52 must preserve it], the UTC-midnight date-straddle clause, replica-as-column folding, and lifted `inv`/
  `funcd.source` to typed columns. No Blockers. **Reviewing → Implemented 2026-06-29** — review **pass** (DoD 10/10),
  see docs/reviews/adr-0083-implementation-claude-opus-4-8.md; `internal/funclog/compact` [Compactor + the
  raw→compacted Parquet transform via `parquet-go`] + `pkg/funcd` options/wiring, 8 scenario tests green, lint 0,
  `parquet-go` Apache-2.0/pure-Go, crash-safety verified. Terminology is **raw/compacted** [no medallion vocabulary],
  per the decider.)
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, compaction, parquet, blob, daemon-internal, otlp
- **Realizes**: [FEAT-0004/F53](../feat/0004-feat-platform-observability.md)
- **Relates to**: [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the funclog sink whose raw OTLP-JSONL
  this folds, and whose options-only config surface this mirrors) · [ADR-0007](0007-blob-storage-layer-port.md)
  (the `blob.Bucket` port it reads/writes through) · [ADR-0065](0065-metastore-badger-engine.md) (pure-Go static
  binary, no cgo) · [ADR-0082](0082-provider-model-catalog.md) (the provider model — this is **internal substrate
  serving the log-ingest capability**, not itself a function-bindable provider in that catalog) ·
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports, typed enums, `New(…)`, no global state)

## Context & Need

**Purpose**: fold the many small **raw OTLP-JSON-Lines** objects that the ADR-0081 funclog sink writes
(`logs/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl`, sealed every ~10s / 8 MiB) into far fewer **columnar
Parquet** objects, time-windowed, so the log corpus is **cheap to scan and prune** by function / namespace /
date — the input F54's reader (`funcdctl logs`) queries. This is funcd's **own** raw→compacted pipeline on its
own primitives, **lakehouse-independent** (no DuckLake, no S3 gateway, no Quack/DuckDB).

**Callers**: nobody calls it — it is an **always-on, daemon-internal pipeline** (a background goroutine on a
timer), started by the `pkg/funcd` composition root and stopped on shutdown, exactly like the funclog sink's
age-flusher. It is **internal substrate serving the log-ingest capability**, **not** a function-bindable provider:
it has no `spec.*` binding, so it is excluded from the ADR-0082 provider catalog the same way `bus` is (an internal
substrate, never function-bindable). Registering a `log-compaction` descriptor in that catalog would require a
follow-on ADR superseding the frozen ADR-0082; this ADR claims no catalog entry.

**Why now**: F50 (capture) is Implemented and produces raw continuously; without compaction the object count
grows unbounded and every read must scan thousands of tiny JSON-Lines files. F54 (the reader) needs a columnar,
partitioned, prune-friendly store to query — F53 is its prerequisite.

## Scenarios

- **scenario: compacts-closed-window** — *Given* raw OTLP-JSONL objects for function `f`/namespace `n` whose
  time window has fully closed, *When* a compaction pass runs, *Then* their records are written as **one** Parquet
  object for that `(n, f, window)` with one row per LogRecord and the typed columns + `attrs_json` populated.
- **scenario: parquet-roundtrips** — *Given* a written compacted Parquet, *When* it is read back with the parquet-go
  reader into the row struct, *Then* every typed column (ts, severity, body, identity, trace/span) **and**
  `attrs_json` survive intact.
- **scenario: partitioned-by-ns-fn-date** — *Given* a compacted window, *Then* the compacted object key is
  Hive-partitioned `logs/<ns>/<fn>/<date>/<window-start-unixnano>.parquet` (prune by ns / fn / date).
- **scenario: raw-deleted-after-durable-write** — *Given* a window is compacted, *When* the Parquet `Put`
  **succeeds**, *Then* (and only then) the consumed raw objects are deleted — raw is transient, compacted is
  the store.
- **scenario: recent-raw-preserved** — *Given* raw objects whose window is still **open** (younger than the
  window), *When* a pass runs, *Then* they are **not** compacted and **not** deleted (F54 still reads them as the
  live tail).
- **scenario: crash-safe-no-loss** — *Given* a crash (or a failed `Delete`) **after** the Parquet `Put` but
  **before** the raw delete, *When* the next pass runs, *Then* it re-writes the **same** deterministic Parquet
  key (idempotent overwrite, no duplicate object) and completes the delete — no log record is ever lost or
  double-counted.
- **scenario: retention-prunes-compacted** — *Given* compacted Parquet objects older than the configured retention,
  *When* a pass runs, *Then* they are deleted; a zero retention keeps compacted forever.
- **scenario: disabled-config** — *Given* the platform is built with `WithoutLogCompaction()`, *Then* no compactor
  goroutine is started and raw is left untouched (capture still runs).

## Scope

**In**: a new leaf sub-package **`internal/funclog/compact`** — the `Compactor` (a daemon-internal goroutine on a
timer), its Parquet **row schema** (fixed typed columns + a single `attrs_json` column), the raw→compacted
transform (read OTLP-JSONL via `plog`, write Parquet via parquet-go), **Hive-style partitioning**, the
**Parquet-before-delete** crash-safe ordering with deterministic keys, **retention** pruning of compacted, the
`funclog.compaction.*` config keys + a `pkg/funcd` option, and the composition-root lifecycle wiring (start/stop).

**Out**: F54 (the reader, `funcdctl logs`, query-time tenant scoping); F51/F52 traces/metrics compaction (this
schema is **log-specific**, though the timer/blob/partition skeleton is reusable); any query engine or DuckDB;
any change to the funclog sink (ADR-0081) or the raw wire; cross-window merging / re-compaction of compacted into
coarser compacted (a later optimization).

## Constraints & Decision drivers

- **Pure-Go, no cgo, minimal deps** (ADR-0065) — the writer must be a pure-Go Parquet library; DuckDB/cgo stays
  out of the daemon.
- **Reuse the `blob.Bucket` port** (ADR-0007) — read raw + write compacted through the existing substrate; no new
  storage path, no second client. Same memory/file/s3 drivers.
- **Lakehouse-independent** — no dependency on the FEAT-0003 services (S3 gateway, Quack/DuckLake catalog).
- **Crash-only / loss-safe** (blueprint) — a crash mid-compaction must lose **nothing** and create no duplicate;
  Parquet is written before raw is deleted, and the compacted key is deterministic so a retry overwrites in place.
- **Prune-friendly for F54** — partition by ns/fn/date and keep typed columns for time/severity so the reader can
  skip irrelevant objects cheaply.
- **Default-on, zero-config** — sane window/interval/retention defaults; fully overridable.
- Deps **Apache-2.0/MIT** only.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Daemon-internal pure-Go pipeline** (a goroutine on a timer) — **chosen** | Always-on, no dependency on function execution / a scheduler / bootstrapping; matches the FEAT-0004 "funcd's **own** compaction pipeline" intent and the funclog sink's own age-flusher precedent. Cost: it runs in-daemon (CPU/memory of the compaction pass) — bounded by the window/interval and acceptable for the ~100-agent target. |
| **A scheduled funcd function** doing the compaction (dogfooding) | Elegant self-hosting — but needs a **cron/scheduler primitive** (funcd has none yet), function execution on the critical observability path, and `funcd-system` bootstrapping. Heavier and more fragile than a goroutine for a core substrate. Revisit if a general scheduler lands. |
| **`github.com/parquet-go/parquet-go`** as the writer — **chosen** | Pure-Go (no cgo), Apache-2.0, actively maintained (v0.30.1, 2026-05), generic `GenericWriter[T]`/`WriteFile[T]` from Go structs + `GenericReader[T]`/`Read[T]` back, row-group/bloom pruning for the F54 read path. The mature pure-Go Parquet option. |
| **`github.com/apache/arrow-go` (`pqarrow`)** | Also pure-Go Parquet, Apache-2.0 — but pulls the **whole Arrow** memory/array framework for what is a struct→Parquet write; far heavier than parquet-go's struct-tag writer. Rejected on dependency weight; revisitable if Arrow is needed elsewhere. |
| **Flatten OTLP attrs into typed Parquet columns** (one column per attr key) | Richer per-attr predicate pushdown — but attrs are **unbounded and sparse** across functions, so the schema would be unstable/wide. Rejected: a single `attrs_json` string column keeps the schema **fixed** and is enough for log-tail (filter on the typed columns; render attrs as JSON). |
| **Keep raw, compact lazily at read time (F54)** | No background pipeline — but every read re-scans thousands of tiny files; the cost just moves to the hot path and never shrinks the object count. Rejected: compaction is the point. |
| **Delete raw on a fixed age, decoupled from the Parquet write** | Simpler — but risks deleting raw a window never compacted (loss) or keeping it forever (no reclaim). Rejected: delete is **gated on the durable Parquet write** of that exact window. |

## Decision

A **daemon-internal, pure-Go compaction pipeline** — `internal/funclog/compact.Compactor`, a background goroutine
on a timer (internal substrate serving the log-ingest capability, not a function-bindable ADR-0082 provider) —
folds raw OTLP-JSONL into partitioned Parquet through the `blob.Bucket` port. Concretely:

- **Discover.** List `logs/` on the bucket; keep the `*.otlp.jsonl` (raw) keys, parse each into
  `(ns, fn, sealNano, replica)` from the key the funclog sink wrote. (Compacted `*.parquet` keys are skipped.)
- **Window.** Bucket each raw object by `(ns, fn, windowStart)` where
  `windowStart = floor(sealNano / Window) * Window` and `sealNano` is parsed from the raw key. **`sealNano` is
  the funclog sink's *seal time*** (`segmentKey` stamps `now.UTC().UnixNano()` at flush — `sink.go`), **not** event
  time: it is monotonic w.r.t. real time and at most `funclog.segmentMaxAge` (~10s) behind the data. A bucket is
  **closed** when `now ≥ windowStart + Window`; only closed buckets are compacted (open ones are the live tail F54
  still reads as JSONL — *recent-raw-preserved*). Because seals are near-real-time, **no** segment can still
  seal *into* an already-closed window, so the close needs **no grace period** and there is no late-arrival loss/dup
  (and the deterministic compacted key below would absorb one anyway). *F51/F52 must preserve this invariant — window
  by seal time, never event time.* Grouping uses `sealNano` **alone** (the raw key's `<date>` dir is ignored), so
  a window straddling UTC midnight is grouped intact; the full-`logs/` list (above) guarantees both date dirs are seen.
- **Transform.** For each closed bucket, read every raw object, unmarshal each line with
  `plog.JSONUnmarshaler`, and emit **one `Row` per LogRecord** with this fixed extraction:
  - from the OTLP **Resource** attrs → `namespace`, `function`, `replica`, `tenant`;
  - from the **LogRecord** → `ts` (timestamp), `severity_text`/`severity_number`, `body`, `trace_id`, `span_id`;
  - from the LogRecord **attributes** → `funcd.source`→`source`, `inv`→`inv` (the invocation id — a typed column,
    as query-relevant for F54 as `trace_id`), and **all remaining** attributes → the single **`attrs_json`** string
    column (`pcommon.Map.AsRaw()` minus the two lifted keys → `json.Marshal`).

  `replica` is a **column**, not a partition key, so every replica's segments in a window coalesce into the one
  compacted object.
- **Write compacted.** Marshal the rows to Parquet with `parquet.GenericWriter[Row]` into a buffer and
  `blob.Bucket.Put` it at the **deterministic, Hive-partitioned** key
  `logs/<ns>/<fn>/<date>/<windowStart-unixnano>.parquet` (`date` = the UTC date of `windowStart`).
- **Reclaim (Parquet-before-delete).** Only **after** the `Put` succeeds, `blob.Bucket.Delete` the consumed raw
  objects. A crash between `Put` and `Delete` is safe: the compacted key is a pure function of `(ns, fn, windowStart)`,
  so the next pass re-`Put`s the **same** key (idempotent overwrite, no duplicate) and finishes the delete —
  *crash-safe-no-loss*.
- **Retention.** Each pass also deletes compacted `*.parquet` whose `windowStart` is older than `Retention`
  (`0` ⇒ keep forever).
- **Lifecycle.** `New(Deps)` validates and returns the compactor; `Run(ctx)` ticks every `Interval`, calling
  `CompactOnce` each tick and on `ctx.Done()` returns. `CompactOnce(ctx)` is the deterministic single pass
  (the unit of test). Wired in `buildControlPlane` next to the funclog sink (gated on a blob substrate + not
  disabled), run as a goroutine in `Platform.Run`, stopped by `ctx` cancel on shutdown. **Configured through
  `pkg/funcd` options only** — `WithLogCompaction(window, interval, retention)` / `WithoutLogCompaction()` —
  mirroring how ADR-0081's funclog is wired (`WithFunclog`/`WithoutFunclog`); there is **no `funclog:` YAML block**
  in the daemon config today and this ADR adds none.

The pipeline is **log-specific in its schema** but its timer/blob/partition skeleton is the reusable seam F51/F52
will copy for traces/metrics (their own row types) — consistent with ADR-0081's signal-generic principle.

## Temporary workarounds

- **Full-`logs/` list per pass.** Each pass `List`s the whole `logs/` prefix to group windows; there is no cursor.
  *Exit*: a persisted compaction cursor / per-fn list (`logs/<ns>/<fn>/`) if the object count makes the full list
  costly — measured by the homebox bench, not pre-optimized.
- **One Parquet per `(fn, window)`, no compacted re-compaction.** Compacted is never merged into coarser compacted.
  *Exit*: a second-tier compaction (compacted→compacted over a larger window) if per-window Parquet stays too granular
  for F54 — a follow-on ADR, out of scope here.

## Contracts

```go
// Package compact folds the funclog "raw" raw OTLP-JSON-Lines objects (ADR-0081) into columnar Parquet
// "compacted", time-windowed and Hive-partitioned, through the blob.Bucket port (ADR-0007). It is an always-on
// daemon-internal pipeline — pure-Go, no cgo, lakehouse-independent — internal substrate for the log-ingest
// capability, not a function-bindable provider (ADR-0082). The schema is log-specific; the timer/blob/partition
// skeleton is the seam F51/F52 reuse (windowing by SEAL time, not event time — see the close invariant).
package compact

// Row is one LogRecord as a Parquet row: fixed typed columns + a single attrs_json column for the rest. The
// schema is STABLE (F54 reads it); new OTLP attributes land in attrs_json, never as new columns.
type Row struct {
	TimeUnixNano   int64  `parquet:"ts"`              // LogRecord timestamp (epoch nanos)
	SeverityText   string `parquet:"severity_text"`   // e.g. "INFO"
	SeverityNumber int32  `parquet:"severity_number"` // OTLP SeverityNumber
	Body           string `parquet:"body"`            // the message
	Namespace      string `parquet:"namespace"`       // resource identity (partition key)
	Function       string `parquet:"function"`        // resource identity (partition key)
	Replica        string `parquet:"replica"`
	Tenant         string `parquet:"tenant"`
	Source         string `parquet:"source"`     // lifted from the funcd.source attr (console/logging/stdout/stderr)
	Invocation     string `parquet:"inv"`        // lifted from the inv attr — the invocation id (F54 filters on it)
	TraceID        string `parquet:"trace_id"`   // hex32, "" when absent
	SpanID         string `parquet:"span_id"`    // hex16, "" when absent
	AttrsJSON      string `parquet:"attrs_json"` // remaining LogRecord attributes (minus funcd.source + inv) as a JSON object
}

// Deps constructs a Compactor. Bucket is the funcd-system observability bucket (the same blob the funclog sink
// writes). Window/Interval/Retention default when zero (see defaults below).
type Deps struct {
	Bucket    blob.Bucket
	Clock     clock.Clock
	Logger    *slog.Logger
	Window    time.Duration // bucket size AND closed-ness threshold; a window closes at windowStart+Window
	Interval  time.Duration // timer cadence between passes
	Retention time.Duration // delete compacted older than this; 0 ⇒ keep forever
}

const (
	defaultWindow    = time.Hour        // one compacted Parquet per (fn, hour)
	defaultInterval  = 5 * time.Minute  // run a pass every 5m
	defaultRetention = 720 * time.Hour  // keep compacted 30d
)

// Stats is one pass's outcome (for logging/tests): windows compacted, rows written, raw objects deleted,
// compacted objects pruned by retention.
type Stats struct {
	Windows       int
	Rows          int
	RawDeleted int
	CompactedPruned  int
}

// Compactor is the daemon-internal compaction pipeline. Construct with New; Run owns the timer loop.
type Compactor struct { /* bucket, clock, log, window, interval, retention */ }

// New validates deps and builds the compactor (Bucket + Clock required; durations default when zero).
func New(d Deps) (*Compactor, error)

// Run ticks every Interval, calling CompactOnce each tick, until ctx is cancelled (then returns nil). A pass
// error is logged and the loop continues (crash-only: the next pass retries, idempotently).
func (c *Compactor) Run(ctx context.Context) error

// CompactOnce runs exactly one pass: discover raw → group into closed windows → write one Parquet per
// (ns, fn, window) → delete the consumed raw (only after the Put) → prune compacted past Retention. Deterministic
// and idempotent (re-running over the same raw overwrites the same compacted key). The unit of test.
func (c *Compactor) CompactOnce(ctx context.Context) (Stats, error)
```

**Config** — `pkg/funcd` **options only** (mirroring ADR-0081's funclog, which has no `internal/platform/config`
YAML block; this ADR adds none either):

```go
// WithLogCompaction tunes compacted compaction (ADR-0083): the window (bucket size + close threshold), the pass
// interval, and the compacted retention. Any zero keeps the default (1h / 5m / 30d). Default-on when a blob
// substrate is present.
func WithLogCompaction(window, interval, retention time.Duration) Option

// WithoutLogCompaction disables compaction entirely: no compactor goroutine is started, raw is left untouched.
func WithoutLogCompaction() Option
```

All defaults live in `compact.New` (`Deps` zero-values default), so an unconfigured platform compacts with the
1h / 5m / 30d defaults; a disabled platform runs no compactor.

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | `blob.Bucket` (ADR-0007: `List`/`Get`/`Put`/`Delete` over the funcd-system bucket) · the funclog **raw** OTLP-JSONL objects (ADR-0081) · `clock.Clock` · the `pkg/funcd` compaction options |
| Exposes | **compacted** Parquet objects at `logs/<ns>/<fn>/<date>/<windowStart>.parquet` — the input F54's reader queries |
| Config surface | `pkg/funcd` options `WithLogCompaction(window, interval, retention)` / `WithoutLogCompaction()` (no YAML; mirrors ADR-0081) |
| New deps | `github.com/parquet-go/parquet-go` (Apache-2.0, pure-Go, no cgo; `GenericWriter[T]`/`Read[T]`) — promoted to a direct dep. `pdata/plog` already direct (ADR-0081). |
| Lifecycle | constructed in `buildControlPlane` (gated on blob present + `compaction.enabled`); `Run` as a `Platform.Run` goroutine; stopped by `ctx` cancel on shutdown |

## Implementation plan

- **Files**: `internal/funclog/compact/{compact.go (Row, Deps, Stats, Compactor, New, Run, CompactOnce),
  keys.go (raw key parse + compacted key build + window math),compact_test.go (scenario tests)}`; the
  `WithLogCompaction`/`WithoutLogCompaction` options in `pkg/funcd/options.go`; the wiring in
  `pkg/funcd/funcd.go` (`config` fields for window/interval/retention/disabled, `buildControlPlane` construction
  next to the funclog sink, a `Platform.Run` goroutine, `Shutdown` left to ctx-cancel). **No `internal/platform/config`
  or `cmd/funcd` change** — the surface is `pkg/funcd` options, as ADR-0081's funclog is.
- **go.mod**: `go get github.com/parquet-go/parquet-go` (Apache-2.0); record the `go list -deps` delta.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`, over a **real in-memory
  `blob.Bucket`** — `internal/blob/gocloud` mem://, no mocks; seed raw by writing OTLP-JSONL the same way the
  funclog sink does, via `plog.JSONMarshaler`, at the real raw keys):
  - `compacts-closed-window` — seed closed-window raw → `CompactOnce` → exactly one Parquet for the window,
    `Stats.Rows` = total LogRecords, values correct.
  - `parquet-roundtrips` — read the compacted back with `parquet.Read[Row]` → typed columns + `attrs_json` intact.
  - `partitioned-by-ns-fn-date` — the compacted key equals `logs/<ns>/<fn>/<date>/<windowStart>.parquet`.
  - `raw-deleted-after-durable-write` — after `CompactOnce` the consumed raw keys are gone and the Parquet
    exists.
  - `recent-raw-preserved` — an open-window raw object is neither compacted nor deleted.
  - `crash-safe-no-loss` — a `blob.Bucket` wrapper that fails `Delete`: first pass writes the Parquet but cannot
    delete raw (no loss — rows are in compacted); a second pass over the same raw re-writes the **same** compacted
    key (one object, not two) and deletes the raw. Proves Parquet-before-delete + deterministic-key idempotency.
  - `retention-prunes-compacted` — a compacted object older than `Retention` is deleted; one within is kept (asserting
    `Stats.CompactedPruned`).
  - `disabled-config` — a `package funcd` internal test: a `config` built with `WithoutLogCompaction()` wires **no**
    compactor (`Platform.compactor == nil`) so raw is untouched; plus a `compact` assertion that an
    all-open-windows corpus yields a no-op pass (`Stats{}`).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; one passing test per Scenario; `go list
  -deps` delta recorded; pure-Go (no cgo); reads/writes only through `blob.Bucket` (no new storage path); identity/
  path grep clean; `just ci` green after commit.

## Review checklist

- [ ] Raw discovered via `blob.Bucket.List("logs/")`, `*.otlp.jsonl` only; `*.parquet` skipped.
- [ ] Windowing is `floor(sealNano/Window)*Window`; a window is compacted **only** when `now ≥ windowStart+Window`
      (open windows preserved — *recent-raw-preserved*).
- [ ] One `Row` per LogRecord; fixed typed columns populated; remaining attributes in `attrs_json` (valid JSON).
- [ ] Compacted written with `parquet.GenericWriter[Row]`, one object per `(ns, fn, window)`, at the deterministic
      Hive key `logs/<ns>/<fn>/<date>/<windowStart>.parquet`.
- [ ] Raw deleted **only after** the Parquet `Put` succeeds; the compacted key is deterministic so a retry
      overwrites in place (no duplicate, no loss).
- [ ] Retention deletes compacted older than `Retention`; `0` keeps forever.
- [ ] Default-on; `WithoutLogCompaction()` ⇒ no goroutine, raw untouched. Config surface is `pkg/funcd`
      options only (no `internal/platform/config` YAML), mirroring ADR-0081's funclog.
- [ ] One `Row` per LogRecord with `funcd.source`→`source`, `inv`→`inv` lifted to columns; window by **seal time**.
- [ ] Reads/writes only through `blob.Bucket`; no DuckDB, no cgo; `parquet-go` is Apache-2.0; `go list -deps`
      delta recorded.
- [ ] `Run` stops on `ctx` cancel; wired into `pkg/funcd` start/stop like the funclog sink.
- [ ] One passing acceptance test per Scenario.

## Consequences

- **(+)** **Object count collapses** — thousands of ~10s segments per function per hour become one Parquet per
  window; reads (F54) scan and prune columnar data instead of parsing tiny JSON-Lines files.
- **(+)** **Loss-safe + idempotent** — Parquet-before-delete with a deterministic compacted key means a crash or a
  failed delete never loses or duplicates a record; the next pass converges.
- **(+)** **Lakehouse-independent, pure-Go** — funcd owns the whole raw→compacted path on `blob.Bucket` with one
  pure-Go dep; no DuckLake/S3-gateway/DuckDB/cgo in the daemon.
- **(+)** **Prune-friendly for F54** — Hive ns/fn/date partitioning + typed time/severity columns give the reader
  cheap predicate pushdown; `attrs_json` keeps the schema stable as attributes evolve.
- **(−)** **`attrs_json` isn't column-pruned** — per-attribute predicates scan the JSON blob, not a column. Accepted
  for log-tail (filters are on the typed columns); revisit only if attribute-level analytics are needed.
- **(−)** **Compaction latency** — a record isn't in compacted until its window closes (≤ `Window + Interval`); F54
  covers the gap by also reading the open-window JSONL tail. Logs-only here (F51/F52 separate).
- **(−)** **In-daemon cost** — the pass runs in the funcd process (list + read + Parquet encode); bounded by
  window/interval and measured on the homebox bench. A full-`logs/` list per pass is the first thing to optimize
  if the object count grows (see Temporary workarounds).
- **Risk**: a new direct dep (`parquet-go`) — mitigated: Apache-2.0, pure-Go (no cgo), actively maintained
  (v0.30.1), generic struct-tag writer/reader, no Arrow framework pulled in.

## Open questions

- **Window/interval/retention defaults** — `1h`/`5m`/`30d` are first estimates for the ~100-agent target;
  revisited with the homebox bench once F54 exercises the read path (the values are options, so tuning is not a
  code change). `Interval (5m) < Window (1h)` is **intentional**: a window is re-passed harmlessly (idempotently)
  several times while still open and compacted once it closes — not a misconfiguration to "optimize" away.
- **Compaction cursor** — the full-`logs/` list per pass is simplest; whether a persisted cursor / per-fn list is
  needed is a measured optimization (Temporary workarounds), resolved in a follow-on if the bench shows it.
- **Compacted re-compaction** — whether per-window Parquet is granular enough for F54 or needs a compacted→compacted tier is
  deferred until F54 measures read cost.

## References

- `github.com/parquet-go/parquet-go` — pure-Go Parquet (pkg.go.dev, **v0.30.1**, 2026-05-18, **Apache-2.0**, no
  cgo; `GenericWriter[T]`/`WriteFile[T]`, `GenericReader[T]`/`Read[T]`, struct-tag schema, row-group/bloom pruning).
- `github.com/apache/arrow-go` `pqarrow` — the considered heavier pure-Go Parquet alternative (Arrow framework).
- `go.opentelemetry.io/collector/pdata/plog` — `JSONUnmarshaler` to read raw OTLP-JSONL (already a direct dep,
  ADR-0081).
- [ADR-0081](0081-function-log-capture-side-channel-blob.md) (raw producer), [ADR-0007](0007-blob-storage-layer-port.md)
  (blob port), [ADR-0065](0065-metastore-badger-engine.md) (pure-Go), [ADR-0082](0082-provider-model-catalog.md)
  (built-in provider), [FEAT-0004](../feat/0004-feat-platform-observability.md) (the observability epoch).
- Tracking: Project #4 card *"Function-log compaction → Parquet (F53) — ADR-0083 / FEAT-0004"* — status follows
  this ADR's lifecycle.
