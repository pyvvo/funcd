# ADR-0081: Function log capture via a console-intercept side channel, persisted as OTLP-JSONL through the blob port

- **Status**: Implemented
- **Date**: 2026-06-29 (Accepted 2026-06-29 after three judge passes — signal-generic narrowed to the pipeline,
  Sink concurrency contract, loss-free qualified to freeze/teardown, `internal/platform/config` path, and the
  per-language harness capture contract added. **Reviewing → Implemented 2026-06-29** — review **pass**, see
  docs/reviews/adr-0081-implementation-claude-opus-4-8.md; `internal/funclog` pipeline + both shims (Node console /
  Python logging) + the fd3 (process) and UDS (containerd) transports + composition wiring, verified end-to-end by
  the in-process e2e **and** the Lima containerd e2e (JS + Python, all 4 testcases PASS). 3 minor follow-ons recorded)
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, otel, otlp, capture, blob, tenant-telemetry, side-channel
- **Realizes**: [FEAT-0004/F50](../feat/0004-feat-platform-observability.md)
- **Relates to**: [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket` port this persists through) ·
  [ADR-0009](0009-observability-logger-root.md)/[ADR-0010](0010-observability-telemetry-and-audit.md) (the
  **platform** lane, `source=platform`; this is the **tenant/function** lane, `source=function`, they carved out) ·
  [ADR-0011](0011-runtime-sandbox-port.md) (worker netns / sandbox launch — fd3 vs UDS) ·
  [ADR-0065](0065-metastore-badger-engine.md) (pure-Go static binary, no cgo) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md) (connection-scoped identity — the `inv`/resource tag)

## Context & Need

**Purpose**: capture the logs a function emits on the trusted runtime path (distroless Node/Python under
crun/containerd with the in-process harness), correlate them, and **persist them loss-free across freeze/teardown**
as OTel-native records on funcd's own blob substrate. **Callers**: the funcd worker path (host-side reader + pipeline) and, on
the wire, the per-language harness. This is the **tenant/function** observability lane (`source=function`) that
ADR-0009/0010 explicitly deferred while building the platform lane.

**Why now**: FEAT-0004 makes funcd its own observability backend; the producer foundation must exist before
compaction (F53), the serving function (F54), or the public endpoint (F55). The hard constraint is **loss across
freeze/teardown**: instances are suspended or killed between invocations (scale-to-zero), so any buffer held
*inside* the function process is at risk — capture must stream out continuously and batch **host-side**.

## Scenarios

- **scenario: captured-before-format** — *Given* a handler calls `console.log("user", {id: 7})`, *When* the
  harness captures it, *Then* the record preserves the **structured** arguments (object intact, severity `INFO`),
  not a `util.format`-flattened string.
- **scenario: freeze-safe-no-loss** — *Given* a function instance is frozen/killed mid-backlog, *When* the host
  reader is mid-drain, *Then* the frozen instance **pauses** the host pump (blocking `Read`) and no already-emitted
  record is lost — the core advantage over an in-process exporter.
- **scenario: path-a-crash-tail** — *Given* a function writes to stderr **before** the harness patches `console`
  (or crashes natively), *When* funcd captures fd 1/2 (Path A), *Then* those lines still become `Entry`s (coarse
  severity: fd1→`INFO`, fd2→`ERROR`, `source=stderr`).
- **scenario: correlated** — *Given* a record emitted inside an invocation, *When* it is decoded, *Then* it carries
  the invocation id and (when present) `trace_id`/`span_id`.
- **scenario: persisted-via-blob** — *Given* a sealed segment of records, *When* the sink flushes, *Then* it is
  marshaled to **OTLP-JSON-Lines** and written as **one `blob.Bucket.Put` object** under a partitioned key.
- **scenario: identity-tagged** — *Given* records from function `f` in namespace `n`, *When* persisted, *Then* the
  OTLP **Resource** carries `namespace=n`, `function=f`, `replica`, `tenant` — the query-time scoping key.
- **scenario: wazero-host-console** — *Given* the untrusted wazero+rquickjs path, *When* the guest calls `console`,
  *Then* it lands in the Go host function directly and joins the **same** `Entry` pipeline — no fd/socket channel.
- **scenario: transport-fd3-and-uds** — *Given* a function launched under crun (fd 3) **or** containerd (UDS),
  *When* it emits over its channel, *Then* both transports decode through the **same** NDJSON `Reader` → `Entry`.
- **scenario: console-no-double-capture** — *Given* the Node harness has patched `console`, *When* a handler calls
  `console.log("user", {id: 7})`, *Then* exactly **one** structured Path B record is produced (`body="user"`,
  `attrs.args` intact) and the line is **not** also re-captured by Path A — the patched `console` writes
  channel-only, with no fd 1/2 echo.
- **scenario: python-logging-structured** — *Given* the Python harness has installed its `logging.Handler`, *When*
  a handler calls `logging.info("user %s", name, extra={"id": 7})`, *Then* a structured Path B record results
  (severity `INFO`, `attrs` carrying the args + `extra`); *And* a bare `print(...)` falls to **Path A** (stdout,
  coarse `INFO`) — Python has no `console`, so `print` is not a Path B source.

## Scope

**In**: the function-log **capture** (console-intercept Path B before `util.format` + raw stdout/stderr Path A
safety net), the **transport** (fd 3 under crun / UDS bind-mount under containerd), the **host-side** decode +
batch **pipeline**, building OTLP (`plog`) **log** records on that signal-generic pipeline, and **persisting raw OTLP-JSON-Lines**
through the existing **`blob.Bucket`** port into a reserved `funcd-system` namespace, identity-tagged. The NDJSON
wire contract across shim languages.

**Out** (FEAT-0004 follow-ons): traces (F51); metrics (F52); time-windowed compaction → Parquet (F53); the
serving function + query-time tenant scoping (F54); the public OTLP/Grafana/Prometheus endpoint (F55). Also out:
any dependency on the FEAT-0003 lakehouse services (S3 gateway ADR-0080, Quack/DuckLake catalog F48) — this lane
is **lakehouse-independent**.

## Constraints & Decision drivers

- **No loss on freeze/teardown** — nothing durable may buffer *inside* the function; funcd reads continuously.
- **Pure-Go static binary, no cgo, minimal deps** (ADR-0065). No heavy framework in the funcd binary or every image.
- **Reuse, don't duplicate** — persist through the existing `blob.Bucket` port (ADR-0007), not a new blob
  abstraction or a second S3 client; reconcile with the existing OTel footprint (ADR-0010).
- **Structured + correlated** — exact severity, real event time, structured attributes, invocation/trace identity.
- **Signal-generic pipeline** — the **transport + batch + sink + identity** plumbing must extend to traces/metrics
  (F51/F52) without a rewrite; the per-signal *record type* (the log `Entry`/NDJSON here) is signal-**specific**.
- **Language-agnostic wire** across shim runtimes; OTLP built **inside funcd**, never crossing the function boundary.
- Deps **Apache-2.0/MIT** only.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **OTel SDK exporter in the function** (`@opentelemetry/sdk-logs` + `OTLPLogExporter` → funcd) | Standards-compliant, free Pino/Winston bridges — but `BatchProcessor` **buffers inside the freezable process** (loss on suspend unless `forceFlush()` every invocation), plus a dependency stack + cold-start cost in every image. Retained as a *future* option for the trusted path **as a sidecar** (the Lambda Telemetry-API pattern), not in-handler. |
| **Raw stdout only (Path A alone)** | Zero-touch, language-agnostic, catches everything — but structure lost to `util.format`, severity limited to fd1-vs-fd2, concurrent invocations interleave, no correlation. **Retained as the safety-net lane**, not the primary. |
| **OTel Collector (otelcol-contrib) sidecar** | Batteries-included (TLS, rotation, S3) — but a **config-driven sidecar** against funcd's owned-code ethos; heavy; assumes OTLP-on-the-wire we don't need internally. |
| **`otlpreceiver` embedded as a library** | Drags the collector component framework (factory/settings/host) + heavy deps. If we ever *ingest* OTLP we'd use `plogotlp` (pdata-only) instead. |
| **A new `Blob{Append/Sync}` interface + `minio-go`** (the original draft) | A second "blob" abstraction (name clash with `blob.Bucket`) and a **duplicate S3 client** — ADR-0080 standardized S3 on `blob.Bucket`/gocloud. Rejected: persist through `blob.Bucket`; solve append-vs-Put **above** the port (seal a segment, then `Put`). |
| **At-rest marshaling: `proto/otlp` + `protojson`** (no new module) | `go.opentelemetry.io/proto/otlp` is already an indirect dep, so no new module — but you **hand-build** the protobuf log structs (verbose, error-prone) and own the OTLP/JSON shaping. Rejected vs `pdata/plog`'s purpose-built `JSONMarshaler`. |
| **At-rest marshaling: the OTel SDK `sdk/log`** (already vendored) | funcd already has `otel/sdk/log` (ADR-0010) — but its log SDK is built around the push **BatchProcessor/exporter** model, not "marshal a batch to OTLP-JSONL **bytes** for a file". Awkward and against the grain. Chosen instead: `pdata/plog` (below). |

## Decision

Capture function logs via a **harness console-intercept side channel ("Path B")**, keep **raw stdout/stderr as a
safety net ("Path A")**, do **all batching host-side in funcd**, build OTLP (`plog`) **log** records on a **signal-generic pipeline**, and
persist them as **OTLP-JSON-Lines through the existing `blob.Bucket` port** into a reserved `funcd-system`
namespace. OTLP is constructed **inside funcd** and never crosses the function boundary.

Concretely:

- **Capture (Path B).** The per-language harness owns `console`, captures arguments **before** `util.format`
  flattening, tags each record with the invocation context held in **`AsyncLocalStorage`** (Node) /
  **`contextvars`** (Python), and writes **one NDJSON record per line** to a dedicated channel:
  `{"ts":<epoch_nanos>,"sev":"INFO","body":"…","attrs":{…},"inv":"…","trace_id":"<hex32>","span_id":"<hex16>","funcd.source":"console"}`.
- **Transport.** The channel is **fd 3** when funcd execs crun directly (`ExtraFiles` + `--preserve-fds`), or a
  **Unix-domain socket bind-mounted** into the sandbox under containerd (a **filesystem** bind-mount, not a network
  endpoint — ADR-0011). fd 1/2 stay untouched and are captured separately as **Path A** (coarse fd-based severity)
  to catch crashes, native-library output, and anything before the harness patches `console`.
- **Pipeline (host-side).** funcd reads each instance's channel → decodes NDJSON → `Entry` → accumulates a
  **per-instance batch** flushed on size/time → builds **`plog.Logs`** (Resource = function/tenant/replica
  identity; per-invocation fields on the `LogRecord`). A frozen instance blocks `Read`, which **pauses the pump** —
  never losing a buffered backlog held inside the function.
- **Marshal + persist.** The sink marshals the batch to **OTLP-JSON-Lines** with
  `go.opentelemetry.io/collector/pdata/plog`'s `JSONMarshaler` (Apache-2.0; a standalone data-model module, **not**
  the collector framework — verified light), seals a **segment** (size/time), and writes it as **one
  `blob.Bucket.Put` object** under a partitioned key. The append-vs-`Put` impedance lives **above** the port: the
  segment buffers in memory, then `Put`s once. `blob.Bucket`'s existing memory/file/s3 drivers give all three
  backends for free — no new `Blob` interface, no `minio-go`.
- **Identity & lane.** Every record's OTLP Resource carries `namespace`/`function`/`replica`/`tenant`
  (`tenant = namespace` today) — the query-time scoping key for F54. Stream label `source=function`, distinct from
  ADR-0010's `source=platform`. Storage is **centralized** in `funcd-system`; per-tenant isolation is a query-time
  concern later (Loki/Mimir model), not storage fan-out.
- **Untrusted path.** On wazero+rquickjs, `console` is bound to a Go host function at embed time; the call lands
  in-host and constructs an `Entry` directly — the same pipeline, no fd/socket plumbing.

## Temporary workarounds

- **Segment buffered before a single `Put`** — `blob.Bucket` has no append seam, so a segment accumulates in
  memory then `Put`s once, bounded by `funclog.segmentMaxBytes` (**fail-closed**: a segment past the cap is sealed
  early and `Put`, never grows unbounded). *Exit*: a streaming/append `blob` seam, or compaction (F53) reading many
  small segments — at which point larger segments aren't needed.
- **Coarse Path A severity** — fd 1/2 carry no structured severity, so Path A maps fd1→`INFO`, fd2→`ERROR`.
  *Exit*: none needed — Path A is the safety net; Path B carries exact severity for everything post-patch.

## Contracts

```go
// internal/funclog — capture function telemetry over a freeze-safe side channel and persist it OTel-native
// through the blob.Bucket port. THIS ADR builds LOGS (Path A/B). The PIPELINE — Reader → batch-per-Resource →
// Sink → blob.Bucket Put, plus the Resource/identity tagging — is signal-generic: traces (F51) and metrics
// (F52) reuse this seam. The per-signal RECORD type (Entry below) and the NDJSON wire are log-SPECIFIC; F51/F52
// add their own record + builder behind the same Reader/Sink (a Signal-discriminated set, not a reused Entry).

// Signal is the OTel signal kind, discriminating the record/wire a Reader+Sink carry. Logs now; F51/F52 add
// SignalTraces/SignalMetrics (each with its own record type — Entry does NOT generalize to spans/data-points).
type Signal string

const SignalLogs Signal = "logs"

// Severity is the typed OTel severity (ADR-0002: typed over magic strings).
type Severity string

const (
	SevTrace Severity = "TRACE"
	SevDebug Severity = "DEBUG"
	SevInfo  Severity = "INFO"
	SevWarn  Severity = "WARN"
	SevError Severity = "ERROR"
	SevFatal Severity = "FATAL"
)

// Source tags which capture path produced an Entry. The NDJSON wire key "funcd.source" decodes to this typed Source.
type Source string

const (
	SourceConsole Source = "console" // Path B structured (Node console.*)
	SourceLogging Source = "logging" // Path B structured (Python logging.Handler)
	SourceStdout  Source = "stdout"  // Path A coarse (INFO)
	SourceStderr  Source = "stderr"  // Path A coarse (ERROR)
)

// Entry is one decoded LOG record from a function instance, pre-OTLP (log-specific: Body + Severity). Traces
// and metrics (F51/F52) define sibling record types behind the same Reader/Sink seam — Entry is not reused.
type Entry struct {
	Time       time.Time         // real event time (epoch nanos on the wire)
	Severity   Severity          // exact for Path B; coarse for Path A
	Body       string            // the message (Path B keeps console args before util.format)
	Attrs      map[string]string // structured attributes
	Invocation string            // per-invocation id (may be empty for pre-invocation Path A lines)
	TraceID    string            // hex32, may be empty (see Open questions)
	SpanID     string            // hex16, may be empty
	Source     Source
}

// Resource is the OTLP Resource identity stamped on every record — the F54 query-time scoping key.
type Resource struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
	Replica   string
	Tenant    string // = Namespace today; explicit for later multi-tenant scoping
}

// Reader decodes one instance's telemetry channel into Entries. NDJSON (Path B) or raw fd lines (Path A).
type Reader interface {
	// Read returns the next Entry, or io.EOF when the channel closes (instance gone). A frozen instance
	// blocks here — which is what pauses the pump and prevents in-function backlog loss.
	Read(ctx context.Context) (Entry, error)
}

// NewNDJSONReader decodes Path B (one JSON record per line) from the fd3/UDS channel.
func NewNDJSONReader(r io.Reader) Reader

// NewRawReader decodes Path A (raw lines) from fd 1 or fd 2, assigning coarse severity by src.
func NewRawReader(r io.Reader, src Source) Reader

// Sink accumulates per-Resource segments, marshals each sealed segment to OTLP-JSON-Lines, and persists it as
// ONE blob.Bucket object (whose own drivers give memory/file/s3). The seam is signal-generic (this logs Sink
// takes Entry; F51/F52 add sibling Sinks for their record types). CONCURRENCY-SAFE: per-Resource segments are
// independently locked, so concurrent Append/Flush from the many per-instance Pumps is supported.
type Sink interface {
	// Append adds one Entry to the open segment for res.
	Append(ctx context.Context, res Resource, e Entry) error
	// Flush seals res's current segment (also triggered by size/age) and Puts it; returns the blob key.
	Flush(ctx context.Context, res Resource) (key string, err error)
	io.Closer
}

// NewBlobSink builds the blob-backed sink over the funcd-system observability bucket (blob.Bucket, ADR-0007).
// Segments seal at SegmentMaxBytes or SegmentMaxAge; keys are partitioned (logs/<ns>/<fn>/<date>/<ts>-<replica>.otlp.jsonl).
func NewBlobSink(d Deps) (*BlobSink, error) // Deps: Bucket blob.Bucket, SegmentMaxBytes int, SegmentMaxAge time.Duration, Clock clock.Clock, Logger *slog.Logger

// Pump drains one instance's Reader into the Sink until the channel closes (loss-safe: blocking Read pauses it).
func Pump(ctx context.Context, r Reader, s Sink, res Resource, log *slog.Logger) error
```

**Config** (`internal/platform/config`, all defaulted ⇒ zero-config unchanged):

```yaml
funclog:
  enabled: true              # capture on for the trusted runtime path (Path A always works even if false)
  segmentMaxBytes: 8388608   # seal+Put a segment at 8 MiB
  segmentMaxAge: 10s         # ...or after 10s, whichever first
  bucket: "funcd-system"     # the reserved observability namespace's blob bucket
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | `blob.Bucket` (ADR-0007, the funcd-system observability bucket) · the sandbox telemetry channel (fd 3 / UDS, ADR-0011 launch) · the connection-scoped caller identity for the `Resource`/`inv` tag ([ADR-0064](0064-fn-to-fn-rpc-links.md)/[0069](0069-kv-data-plane.md)) · `clock.Clock` · config keys |
| Exposes | raw **OTLP-JSON-Lines** objects in `funcd-system`'s blob, identity-tagged (`source=function`) — the input to compaction (F53) |
| Config keys | `funclog.enabled`, `funclog.segmentMaxBytes`, `funclog.segmentMaxAge`, `funclog.bucket` |
| New deps | `go.opentelemetry.io/collector/pdata` (Apache-2.0; data-model module, `plog.Logs` + `JSONMarshaler`) — promoted to a direct dep |
| Wire contract | NDJSON per line: `{ts:int64 nanos, sev:string, body:string, attrs:object, inv:string, trace_id:hex32, span_id:hex16, "funcd.source":"console"}` |

**Harness capture contract (per language).** The harness is the Path B *producer*; both languages emit the **same
NDJSON wire** (above), so the host `Reader` is identical and never sees the difference. The interception point and
field mapping differ — Node has `console`, Python does not:

| | Node (`shim/nodejs`) | Python (`shim/python`) |
|---|---|---|
| **Hook point** | patch `console.*` (`log`/`info`/`debug`/`warn`/`error`) | install a `logging.Handler` on the root logger |
| **Context carrier** | `AsyncLocalStorage` (handler runs inside `als.run(invCtx, …)`) | `contextvars` (set per invocation) |
| **Structured source** | the raw `arguments` array, captured **before** `util.format` | the `LogRecord` (`msg` + `args` + `extra`), seen **before** formatting |
| → `body` | first string arg (printf specifiers left unexpanded), else `""` | `record.getMessage()` (the human message) |
| → `attrs` | object args merged in; **all** original args preserved under `attrs.args` (lossless) | `record.args` + `extra` fields + `logger`/`funcName`/`lineno` |
| → `sev` | `debug`→`DEBUG` · `log`/`info`→`INFO` · `warn`→`WARN` · `error`→`ERROR` | `levelno`: `DEBUG`·`INFO`·`WARNING`→`WARN`·`ERROR`·`CRITICAL`→`FATAL` |
| → `funcd.source` | `console` | `logging` |
| **Unstructured path** | (`console` *is* the structured path) | **`print()` → Path A** (stdout, coarse `INFO`) — not intercepted |

Shared rules (both languages, also the wazero host path):

- **Channel-only — no double-capture.** funcd's injected hook writes the record to the side channel **only**; it
  does **not** echo to fd 1/2, so Path A never re-captures a Path B line. Output that still reaches fd 1/2 (pre-hook,
  native libraries, or a user-added stdout handler) is Path A's domain — overlap there is the user's choice, not
  funcd double-emitting.
- **Framing.** One NDJSON object + `\n` per record, written with a **synchronous** write to fd 3 / the UDS (the
  crash-tail trade — see Open questions); the host `Reader` splits on `\n`.
- **Context.** `inv`/`trace_id`/`span_id` come from the per-invocation context carrier; absent (a pre-invocation
  line) ⇒ emitted empty, which the host tolerates.
- **wazero/rquickjs.** No wire — `console` binds to a Go host function that builds the `Entry` **in-process** with
  the same field mapping; no fd/socket, no NDJSON.

## Implementation plan

- **Files**: `internal/funclog/{funclog.go (Entry/Signal/Severity/Source/Resource types),reader.go (NDJSON +
  raw Readers),sink.go (Sink iface + BlobSink over blob.Bucket, pdata/plog JSONMarshaler, segment seal/Put),pump.go
  (Pump)}`; config keys in `internal/platform/config`; the per-instance wiring (a `Pump` per launched sandbox, the fd3/UDS
  channel) in the worker/runtime launch path (ADR-0011 sandbox start passes the channel); the **harness** Path B
  producers per the *Harness capture contract* above — `shim/nodejs` patches `console.*` (AsyncLocalStorage context,
  capture-before-`util.format`) and `shim/python` installs a `logging.Handler` (contextvars context; `print()` left
  to Path A) — both emitting the **same** NDJSON on fd 3 / UDS, channel-only.
- **go.mod**: `go get go.opentelemetry.io/collector/pdata` (Apache-2.0); record the `go list -deps` delta.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`):
  - `funclog` unit/contract tests over a **real in-memory `blob.Bucket`** (no mocks): `captured-before-format`
    (NDJSON decode keeps structure), `freeze-safe-no-loss` (a blocking `Reader` pauses `Pump`; no Entry dropped),
    `path-a-crash-tail` (raw fd2 line → `ERROR`/`stderr` Entry), `correlated` (inv/trace/span carried),
    `persisted-via-blob` (sealed segment → exactly one `Put`, body is valid OTLP-JSON readable by
    `plog.JSONUnmarshaler`), `identity-tagged` (Resource attrs present), `transport-fd3-and-uds` (both Readers feed
    the same decode), and `concurrent-pumps` (**N pumps `Append` to one `BlobSink` concurrently under `-race`; no
    data race; every instance's segment is `Put`** — exercises the per-Resource-lock contract).
  - shim tests (per-language, runtime-gated) asserting the *Harness capture contract*: Node
    `console-no-double-capture` (`console.log("user",{id:7})` → exactly one structured NDJSON line, `body="user"`,
    `attrs.args` intact, **no** fd 1/2 echo) and Python `python-logging-structured` (`logging.info(... extra=…)` → a
    structured Path B record; `print()` → Path A coarse `INFO`). (`wazero-host-console` covered by the in-host
    `Entry` path test.)
  - an **in-process e2e** (`pkg/funcd`) booting funcd with `funclog` over an in-memory blob, invoking a function
    that logs, and asserting an OTLP-JSONL object lands in `funcd-system` with the right Resource.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; `go list -deps` delta recorded; no new
  `Blob` type, no `minio-go`; pure-Go (no cgo); identity/path grep clean; `just ci` green after commit.

## Review checklist

- [ ] Path B captures `console` args **before** `util.format` (structure preserved; exact severity).
- [ ] **Harness capture contract** honored per language: Node patches `console.*`; Python installs a
      `logging.Handler` (`print()` → Path A); both map args/level → `body`/`attrs`/`sev` per the contract and write
      **channel-only** (no double-capture); identical NDJSON wire; the host `Reader` is language-agnostic.
- [ ] Path A (raw fd 1/2) captured independently, coarse severity, catches pre-patch/crash output.
- [ ] **No durable buffer inside the function** — across **freeze/teardown** a frozen/suspended instance pauses the host `Pump` and loses no emitted record (a hard crash may lose the last unflushed segment/fd-tail; Path A best-effort).
- [ ] Both transports (fd 3 under crun, UDS bind-mount under containerd) decode through the **same** NDJSON `Reader`.
- [ ] Records carry real event time, exact severity, structured attrs, invocation id, and trace/span when present.
- [ ] Persist is through **`blob.Bucket`** (`Put`), **one object per sealed segment**, OTLP-JSON-Lines marshaled via `pdata/plog`; **no** new `Blob` interface, **no** `minio-go`.
- [ ] Every record's OTLP **Resource** carries `namespace`/`function`/`replica`/`tenant`; stream `source=function`.
- [ ] Storage centralized in `funcd-system`; no per-tenant storage fan-out.
- [ ] wazero+rquickjs `console` joins the **same** `Entry` pipeline in-host (no fd/socket).
- [ ] Pure-Go (no cgo); `pdata` is Apache-2.0; `go list -deps` delta recorded.
- [ ] One passing acceptance test per Scenario.

## Consequences

- **(+)** **Loss-safe across freeze/teardown** — nothing buffers in the function; funcd reads continuously, so a
  frozen or suspended instance pauses the writer rather than losing a backlog (the core advantage over an in-process
  exporter). A *hard crash* can still lose the last unflushed host segment + the in-function fd-tail — caught
  best-effort by Path A, and bounded (not eliminated) per the negative items below.
- **(+)** Records carry exact severity, real event time, structured attributes, and invocation/trace correlation;
  OTLP-native (round-trips via `plog.JSONUnmarshaler` / `otlpjsonfilereceiver` / DuckDB).
- **(+)** **Reuses `blob.Bucket`** — memory/file/s3 backends for free, governed by the same substrate; no new blob
  abstraction, no second S3 client; the S3 quirk (no append) is isolated above the port.
- **(+)** **Signal-generic pipeline** — the `Reader` → batch → `Sink` → `blob.Bucket` seam + Resource/identity
  tagging is reused by traces (F51) and metrics (F52); each adds its own record type + wire (the log `Entry`/NDJSON
  here is log-specific, not reused verbatim). The reuse is the *plumbing*, honestly — not the record model.
- **(+)** NDJSON wire is language-agnostic across shims; OTLP exists only inside funcd.
- **(−)** **Private wire protocol** — funcd owns the NDJSON contract, decoder, and versioning across every shim
  language; no OTel-ecosystem bridges (Pino/Winston, semantic conventions) for free on the function side.
- **(−)** **Logs only** here — traces/metrics are separate follow-on lanes (F51/F52).
- **(−)** `console` capture is the harness patch (manual) — not automatic in any approach.
- **(−)** **Crash tail** — an async fd-3 write can drop the last lines on a hard crash unless switched to
  synchronous writes (throughput cost); Path A independently catches crash output. (Open question.)
- **(−)** **Durability window** — in-flight buffered records aren't durable until the segment `Put`; a crash loses
  the last partial segment. Acceptable for logs; a small WAL only if at-least-once to blob is later required.
- **Risk**: a new direct dep (`collector/pdata`) beside the existing otel SDK — mitigated: `pdata` is a
  data-model-only module (Apache-2.0, ~7 imports), not the collector framework, and serves a distinct need
  (at-rest OTLP-JSON marshaling vs the SDK's live gRPC push).

## Open questions

- **trace_id/span_id provenance** — the harness emits them, but a function's span context must be **propagated into
  the sandbox** (the activation/invoke path carrying an OTel span context the harness reads). Where that context is
  established is resolved in the **F54/serving** work or a focused trace-propagation ADR (F51); until then the
  fields are best-effort (may be empty), which the pipeline tolerates.
- **Crash-tail durability** — async fd-3 writes can drop the last lines on a hard crash. Sync writes fix it at a
  throughput cost; resolved with measurement during implementation (Path A is the interim catch).
- **Host-side pump cost at high fan-out** — capture is **decided** default-on (`funclog.enabled: true`); the
  remaining unknown is the per-instance pump's CPU/memory under high concurrency, **measured by the in-process e2e
  + a homebox bench lane**, with the default revisited only if that cost proves material.
- **`funcd-system` provisioning** — the reserved namespace + its observability bucket must exist; the bootstrap
  that creates them is named in the **F54/serving** ADR (this ADR degrades to Path A stdout if the bucket is absent).

## References

- `go.opentelemetry.io/collector/pdata/plog` — `plog.Logs`, `JSONMarshaler`/`JSONUnmarshaler`, `ProtoMarshaler`
  (pkg.go.dev, v1.61.0, Apache-2.0; standalone data-model module, verified independent of the collector framework).
- `otlpjsonfilereceiver` / DuckDB for reading back OTLP-JSON-Lines.
- AWS Lambda Telemetry API (the sidecar pattern, the retained-future OTel-SDK alternative).
- [ADR-0007](0007-blob-storage-layer-port.md), [ADR-0009](0009-observability-logger-root.md),
  [ADR-0010](0010-observability-telemetry-and-audit.md), [ADR-0011](0011-runtime-sandbox-port.md),
  [ADR-0065](0065-metastore-badger-engine.md).
- Tracking: [Project #4 card](https://github.com/users/green-0-rabbit/projects/4/views/1?pane=issue&itemId=206170299)
  *"Function-log capture (Path A/B side channel) — ADR-0081 / FEAT-0004 F50"* — status follows this ADR's lifecycle.
