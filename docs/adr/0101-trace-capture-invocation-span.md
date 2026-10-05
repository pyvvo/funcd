# ADR-0101: Function trace capture — a per-invocation span on the signal-generic funclog pipeline

- **Status**: Implemented
- **Superseded in part by**: [ADR-0165](0165-fn-to-fn-trace-propagation.md) (2026-10-05) — one SERVER span per invocation; span kind SERVER only; span name values.
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — Decision, Trace context: a pooled span is stored under its host-checked member (funcd.member).
- **Date**: 2026-07-06 (Implemented 2026-07-06 after the review gate — pass, 0 blockers/0 majors, DoD 10/10.
  Accepted 2026-07-06 after the judge pass — no Blockers/Majors; folded 7 Minors:
  "byte-identical"→"schema-identical" wire wording, named the ALS carrier as net-new here, `funclog.traces`
  subordinated to the channel gate, documented the harness-always-emits inefficiency, moved `error-span-status`
  to the shim test tier, clarified 422-input-mismatch emits no span, and added the client-`traceparent`
  not-a-security-boundary note; plus the `OK`/`ERROR` casing nit).
  **Reviewing 2026-07-06** — implemented: `internal/funclog/{span,tracesink,route}.go` + the demux wiring in
  `pkg/funcd`; the Node shim (`tracespan.ts` + the ALS carrier in `funclog.ts`, both invoke paths) and the
  Python shim (`tracespan.py`/`invcontext.py` + contextvars, solo + pool); Go marshal/route/persist tests +
  an in-process e2e (span capture + traceparent adoption + log correlation) + the containerd Venom lane
  (12/12, both languages). No new dep. Awaiting the review gate.)
- **Deciders**: green-0-rabbit
- **Tags**: observability, traces, otel, otlp, spans, trace-context, capture, blob, side-channel
- **Realizes**: [FEAT-0004/F51](../feat/0004-feat-platform-observability.md)
- **Relates to**: [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the signal-generic pipeline + fd3/UDS
  transport + `blob.Bucket` sink this reuses; **closes its `trace_id`/`span_id` provenance open question**) ·
  [ADR-0007](0007-blob-storage-layer-port.md) (the `blob.Bucket` port) · [ADR-0011](0011-runtime-sandbox-port.md)
  (the fd3/UDS launch channel) · [ADR-0030](0030-runtime-shim-contract.md)/[ADR-0058](0058-typed-function-io-contract.md)
  (the shim `POST /` invoke contract the span wraps)

## Context & Need

**Purpose**: capture an OTel **trace span** for every function invocation, on the *same* freeze-safe side channel and
`blob.Bucket` sink that ADR-0081 built for logs, and persist it OTel-native (OTLP-trace-JSON-Lines) — so an operator
gets a per-function latency/error **span** (a waterfall when spans share a trace) without the function adopting any
SDK. **Callers**: the funcd worker path (host-side reader + sink) and, on the wire, the per-language harness.

**Why now**: FEAT-0004 makes funcd its own observability backend; logs (F50) shipped, traces (F51) is the next
signal on the *same* pipeline. Two things force it now: (1) ADR-0081 left `trace_id`/`span_id` on log records
**always empty** ([funclog.ts:94-96](../../shim/nodejs/src/funclog.ts)) with an open question deferring their
provenance to F51 — until an invocation *has* a span context, logs cannot correlate; (2) the workflow-observability
goal ("one run = one trace") needs each step-function invocation to emit a span that adopts an incoming trace — the
per-invocation span this ADR captures is that primitive.

The hard constraint is unchanged from ADR-0081: **no loss across freeze/teardown** — nothing durable buffers inside
the freezable function; funcd reads continuously and batches host-side.

## Scenarios

Each becomes a named acceptance test (the test name echoes `scenario: <name>`).

- **scenario: invocation-emits-span** — *Given* a function is invoked over `POST /`, *When* the handler returns,
  *Then* exactly **one** OTLP span is captured for that invocation, kind `SERVER`, with a name, `start`/`end`
  (→ a positive duration), and status `OK`.
- **scenario: error-span-status** — *Given* a handler throws (or returns an output-contract-mismatch), *When* the
  span is captured, *Then* its status is `ERROR` and carries the error message; the span is still emitted (a failure
  is observable, not dropped). An **input**-mismatch (422, handler never runs) emits **no** span — there is no invocation.
- **scenario: adopt-traceparent** — *Given* the invoke request carries a W3C `traceparent`
  (`00-<trace32>-<span16>-<flags>`), *When* the span is captured, *Then* its `TraceID` = the header's trace-id and
  its `ParentSpanID` = the header's span-id — the invocation **joins the caller's trace**.
- **scenario: mint-root-trace** — *Given* no incoming `traceparent`, *When* the span is captured, *Then* it is a
  **root**: a fresh random 16-byte `TraceID`, a fresh 8-byte `SpanID`, empty parent.
- **scenario: logs-correlated-to-span** — *Given* the handler logs (`console.*`/`logging`) during the invocation,
  *When* those log records are captured, *Then* they carry the **same** `trace_id`/`span_id` as the invocation span
  (closing ADR-0081's provenance gap — logs↔trace correlation becomes real, OTLP-standard).
- **scenario: persisted-via-blob** — *Given* a sealed segment of spans, *When* the trace sink flushes, *Then* it is
  marshaled to **OTLP-trace-JSON-Lines** (`ptrace`) and written as **one `blob.Bucket.Put`** under
  `traces/<ns>/<fn>/<date>/<ts>-<replica>.otlp.jsonl`.
- **scenario: identity-tagged** — *Given* spans from function `f` in namespace `n`, *When* persisted, *Then* the OTLP
  **Resource** carries `namespace=n`, `function=f`, `replica`, `tenant` — the same query-time scoping key as logs.
- **scenario: signal-demux** — *Given* one telemetry channel carrying **both** log lines and span lines, *When* the
  host drains it, *Then* each line is routed by its `funcd.signal` tag to the logs `Sink` (as an `Entry`) or the
  traces `Sink` (as a `Span`); a line with **no** `funcd.signal` decodes as a log (back-compat with the F50 wire).
- **scenario: transport-fd3-and-uds** — *Given* a function launched under crun (fd 3) **or** containerd (UDS),
  *When* it emits spans, *Then* both transports route through the **same** demux → `Span`.
- **scenario: python-invocation-span** — *Given* the Python shim, *When* a function is invoked, *Then* it emits the
  **same** span wire as Node (kind/status/start/end/traceparent adoption) — the host demux is language-agnostic.

## Scope

**In**: for each function invocation — (a) **establish an invocation trace context** in the harness (adopt an incoming
W3C `traceparent` as parent+trace, else mint a root trace/span id) held in `AsyncLocalStorage` (Node) / `contextvars`
(Python); (b) emit **one auto `SERVER` span** per invocation (name/start/end/status) over the **existing** fd3/UDS
telemetry channel, **signal-discriminated** (`"funcd.signal":"traces"`); (c) the host-side **demux** routing each
channel line to the logs `Sink` or a new traces `Sink`; (d) a **`Span` record** + **`BlobTraceSink`** (the `BlobSink`
analog: per-`Resource` segments, `ptrace.JSONMarshaler`, one `blob.Bucket.Put` under a `traces/` prefix,
identity-tagged); (e) as a direct consequence of (a), log records emitted inside an invocation now carry the
invocation's `trace_id`/`span_id` (**closing ADR-0081's open question**).

**Out** (FEAT-0004 / workflow follow-ons, each its own ADR):
- **traces compaction → Parquet** (the F53-analog for spans; this ADR persists raw OTLP-JSONL only, exactly as
  ADR-0081 did for logs).
- **traces read path** (`funcdctl traces`, an F54-analog) — verification here reads the blob directly, mirroring the
  ADR-0081 capture line.
- **workflow-engine trace propagation** — the engine stamping `traceparent` on each step dispatch so a whole run is
  one trace. This ADR makes each step-function invocation *adopt* an incoming `traceparent`; the *stamping* side (fn→fn
  and workflow dispatch setting the header) is the workflow-observability follow-on that **consumes** this primitive.
- **OTel-SDK in-function spans** — a function using `@opentelemetry/sdk-trace` emitting its own child spans; an
  additive future (the shim could forward SDK spans on the same channel). This ADR is the zero-touch **auto** span.
- **metrics** (F52); the public OTLP/Grafana endpoint (F55); any lakehouse dependency (this lane is
  lakehouse-independent, like ADR-0081).

## Constraints & Decision drivers

- **No loss on freeze/teardown** — same as ADR-0081: nothing durable buffers inside the function; the host reads
  continuously (a blocking `Read` pauses the pump).
- **Reuse the F50 pipeline, don't fork it** — same transport (fd3/UDS), same per-instance goroutine, same
  `blob.Bucket` port, same `Resource` identity, same segment-seal machinery. Traces add a *record type* + *sink*, not
  a second transport.
- **Zero regression on the Implemented logs path** — the F50 log-wire **schema** is unchanged: log lines gain no
  keys and no `funcd.signal`; the already-present (ADR-0081) `trace_id`/`span_id` fields simply stop being empty.
  Only span lines carry `funcd.signal:"traces"`; the demux defaults an untagged line to logs.
- **OTLP/W3C standard** — trace context is W3C `traceparent`; spans are OTLP `ptrace`; ids are OTel-format random
  bytes (16-byte trace / 8-byte span). Exportable to any OTLP backend.
- **Pure-Go, no cgo, no new module** — `ptrace` ships in the already-direct `collector/pdata v1.61.0`
  ([go.mod:34](../../go.mod)); **no `go.mod` change**.
- **Language-agnostic wire** across shims; OTLP built **inside funcd**, never crossing the function boundary.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **A dedicated traces channel** (a 2nd fd/UDS per instance, `FUNCD_TRACE_FD`/`SOCK`, a 2nd `Pump`, a `SetTraceCapture` capability) | Cleanest *separation*, but **doubles the runtime transport plumbing** (a second pipe/socket + bind-mount + env per instance) and widens the `LogCapturer` port — for no benefit over tagging lines on the one channel. Rejected: one channel, signal-discriminated. |
| **OTel-SDK spans in the function** (require `@opentelemetry/sdk-trace`) | Richer in-function spans, standards-native — but forces every function to adopt a dependency + cold-start cost, and gives no baseline for functions that don't. **Retained as an additive future** (the shim forwards SDK spans later); the auto `SERVER` span is the zero-touch primitive that always works. |
| **Reuse `Entry` for spans** (a "trace" severity log line) | Zero new types — but a span is not a log: it has start/end/parent/kind/status, not body/severity. ADR-0081's own seam says *"Entry does NOT generalize to spans"*. Rejected: a sibling `Span` record, as the seam intends. |
| **Mint the span host-side** (funcd wraps the dispatch, not the shim) | funcd already times the HTTP dispatch — but it can't see the incoming `traceparent` the *handler* received, can't attribute in-handler timing, and the workflow case needs the *function's* invocation to adopt the trace. The harness is the right altitude (it owns the invocation boundary + the context carrier that logs already read). |
| **`proto/otlp` + hand-built span structs** | Avoids leaning on `pdata` — but `pdata/ptrace` is already vendored and gives a purpose-built `JSONMarshaler`; hand-building is verbose and error-prone. Rejected, same reasoning as ADR-0081 chose `plog`. |

## Decision

Capture a **per-invocation auto span** in the harness and persist it through the **existing ADR-0081 pipeline** as a
sibling signal. OTLP (`ptrace`) is constructed **inside funcd**; the function boundary carries only NDJSON.

Concretely:

- **Trace context (harness).** This ADR **introduces the per-invocation context carrier** in the shim — ADR-0081
  *specified* an `AsyncLocalStorage`/`contextvars` carrier but the shipped shim never built it (`inv`/`trace_id`/`span_id`
  are hardcoded empty today, [funclog.ts:94-96](../../shim/nodejs/src/funclog.ts)). On each `POST /`, the shim reads
  the W3C **`traceparent`** request header. If present and well-formed, it adopts the header's trace-id (16-byte) as
  the invocation `TraceID` and the header's span-id as the span's **parent**; otherwise it **mints** a root
  (`crypto.randomBytes(16)`/`(8)` → hex; `secrets.token_hex` in Python). A fresh 8-byte span-id is minted for *this*
  invocation. The context `{inv, traceId, spanId, parentId}` is put in **`AsyncLocalStorage`** (Node) / **`contextvars`**
  (Python) around the handler call. The adopted trace-id is client-controllable, but trace context is **not** a security
  boundary: storage scoping (the `Resource` namespace/function/replica) is **host-stamped** from `WorkerSpec`, never
  client-asserted, so a spoofed `traceparent` only regroups a waterfall — it can never cross tenant isolation.
- **Auto span (harness).** The handler runs inside that context. When it settles, the shim emits **one** span record
  on the telemetry channel: kind `SERVER`, `name` = the function name (`FUNCD_FUNCTION` env, else `"invoke"`),
  `start`/`end` = monotonic-derived epoch nanos, status `OK` on a normal return / `ERROR` (+ message) on a throw or an
  **output**-contract mismatch (500). An **input**-mismatch (422) short-circuits *before* the handler — no invocation,
  so **no span**. The record is tagged `"funcd.signal":"traces"`.
- **Log correlation (harness, consequence of the new carrier).** `buildRecord` (logs) reads the same
  `AsyncLocalStorage`/`contextvars` context introduced above, so `inv`/`trace_id`/`span_id` on log lines are **now
  populated** during an invocation — the same ids as the span. This closes ADR-0081's provenance open question; the
  log-wire **schema** is unchanged (those fields already exist — they just stop being empty). The Go log sink already
  reads them off the wire (`sink.go` `SetTraceID`/`SetSpanID`), so correlation flows with **zero** change to the
  ADR-0081 code or contracts.
- **Demux (host).** funcd's per-instance drain (the goroutine wired at [funcd.go:598-618](../../pkg/funcd/funcd.go))
  now reads each NDJSON line and routes by `funcd.signal`: `"traces"` → a `Span` into the `BlobTraceSink`; anything
  else (including an absent tag) → an `Entry` into the logs `BlobSink` (back-compat). One channel, one blocking read
  → the freeze-safe property is unchanged.
- **Marshal + persist (host).** `BlobTraceSink` mirrors `BlobSink`: per-`Resource` in-memory segments, sealed on
  size/age, marshaled with `ptrace.JSONMarshaler` to **OTLP-trace-JSON-Lines**, written as **one `blob.Bucket.Put`**
  under `traces/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl`. Resource attrs = `source=function`,
  `namespace`/`function`/`replica`/`tenant` — identical identity tagging to logs.

## Temporary workarounds

- **Raw OTLP-JSONL, no compaction.** Spans persist as many small segments, exactly as logs did before F53. *Exit*:
  a traces-compaction ADR (the F53-analog), reusing `compact`'s timer/partition/Parquet-before-delete skeleton.
- **No read verb.** Spans are verifiable in blob but not yet queryable via `funcdctl`. *Exit*: a traces-read ADR (the
  F54-analog), mirroring `logread` + the control-plane route + the CLI.
- **Adopt-only trace propagation.** The invocation *adopts* an incoming `traceparent` but funcd does not yet *set* one
  on fn→fn / workflow dispatch. *Exit*: the workflow-observability follow-on stamps `traceparent` on step dispatch —
  at which point a whole run is one trace with no change here.
- **Harness emits a span even when traces are off.** The shim always establishes the invocation context (log
  correlation needs `trace_id`/`span_id` regardless of the traces toggle) and emits one span line per invocation; when
  `funclog.traces:false` the host drops it (nil `Traces` sink) — one wasted NDJSON line/invocation. *Exit*: a
  `FUNCD_TRACES=0` env the harness reads to skip the span emit, added only if that waste ever proves material.

## Contracts

```go
// internal/funclog — ADR-0101 adds the TRACES signal as a sibling record + sink behind ADR-0081's seam.
// The transport (fd3/UDS), the per-instance drain, Resource identity, and segment-seal machinery are reused;
// only the record type (Span, not Entry), the marshaler (ptrace, not plog), and the blob prefix (traces/) differ.

// SignalTraces is the traces signal (this ADR); joins SignalLogs on the wire's "funcd.signal" discriminator.
const SignalTraces Signal = "traces"

// SpanKind / SpanStatus are the typed OTel span facets carried on the wire (ADR-0002: typed over strings).
type SpanKind string

const (
	SpanServer   SpanKind = "SERVER"   // the auto invocation span (this ADR)
	SpanInternal SpanKind = "INTERNAL" // reserved for later SDK-forwarded spans
	SpanClient   SpanKind = "CLIENT"   // reserved (fn→fn call span, a follow-on)
)

type SpanStatus string

const (
	StatusUnset SpanStatus = "UNSET"
	StatusOk    SpanStatus = "OK"
	StatusError SpanStatus = "ERROR"
)

// Span is one decoded TRACE record from a function instance, pre-OTLP (trace-specific: start/end/parent/kind/
// status, NOT body/severity). The sibling to Entry behind the same Reader/Sink seam — neither reuses the other.
type Span struct {
	TraceID    string            // hex32; adopted from traceparent or minted (root)
	SpanID     string            // hex16; minted per invocation
	ParentID   string            // hex16; the traceparent span-id, empty for a root
	Name       string            // the function name (FUNCD_FUNCTION) or "invoke"
	Kind       SpanKind          // SERVER for the auto invocation span
	Start      time.Time         // invocation start (epoch nanos on the wire)
	End        time.Time         // invocation end
	Status     SpanStatus        // Ok on return, Error on throw/contract-mismatch
	StatusMsg  string            // the error message when Status == Error
	Attrs      map[string]string // structured attributes (e.g. http.status_code)
	Invocation string            // the per-invocation id (== the log lines' inv for correlation)
}

// TraceSink is the traces analog of Sink: per-Resource span segments marshaled to OTLP-trace-JSON-Lines and
// persisted as ONE blob.Bucket object. CONCURRENCY-SAFE per Resource (the many per-instance drains Append freely).
type TraceSink interface {
	// AppendSpan adds one Span to the open segment for res.
	AppendSpan(ctx context.Context, res Resource, s Span) error
	// Flush seals res's current segment (also triggered by size/age) and Puts it; returns the blob key.
	Flush(ctx context.Context, res Resource) (key string, err error)
	io.Closer
}

// NewBlobTraceSink builds the blob-backed trace sink over the funcd-system observability bucket. Segments seal at
// SegmentMaxBytes or SegmentMaxAge; keys are traces/<ns>/<fn>/<date>/<ts>-<replica>.otlp.jsonl. Reuses funclog.Deps.
func NewBlobTraceSink(d Deps) (*BlobTraceSink, error)

// Sinks bundles the per-signal sinks the demux routes into; a nil sink drops that signal (e.g. traces disabled).
type Sinks struct {
	Logs   Sink
	Traces TraceSink
}

// Route drains one instance's telemetry channel, decoding each NDJSON line and dispatching by "funcd.signal":
// "traces" → Sinks.Traces.AppendSpan(Span); absent/other → Sinks.Logs.Append(Entry). Loss-safe (blocking Read
// pauses it); a malformed line is logged and skipped. Replaces the single-Sink Pump at the composition-root wiring;
// Pump/NewNDJSONReader remain for the logs-only contract tests.
func Route(ctx context.Context, r io.Reader, sinks Sinks, res Resource, log *slog.Logger) error
```

**Config** (`internal/platform/config`, defaulted ⇒ zero-config unchanged):

```yaml
funclog:
  traces: true    # capture the traces signal (per-invocation spans); default on, like logs.
                  # SUBORDINATE to the funclog channel: with capture disabled there is no channel, so this is moot;
                  # with capture on but traces off, the host's Traces sink is nil (Sinks.Traces == nil) and span
                  # lines are dropped. segmentMaxBytes / segmentMaxAge (ADR-0081) are shared by both signals' sinks.
```

**Wire contract** — one NDJSON object per line on the fd3/UDS channel; the `funcd.signal` key discriminates
(logs omit it, back-compat):

```json
{"funcd.signal":"traces","trace_id":"<hex32>","span_id":"<hex16>","parent_id":"<hex16>",
 "name":"<fn>","kind":"SERVER","start":<int64 nanos>,"end":<int64 nanos>,
 "status":"OK|ERROR|UNSET","status_msg":"<msg>","attrs":{...},"inv":"<id>"}
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | `blob.Bucket` (ADR-0007) · the fd3/UDS telemetry channel (ADR-0011, **reused** from ADR-0081) · the incoming W3C `traceparent` header on `POST /` · `clock.Clock` · config keys |
| Exposes | raw **OTLP-trace-JSON-Lines** objects under `traces/…` in `funcd-system`'s blob, identity-tagged (`source=function`) — the input to a future traces-compaction lane; **and** populated `trace_id`/`span_id` on log records (correlation) |
| Config keys | `funclog.traces` (new); reuses `funclog.segmentMaxBytes`/`segmentMaxAge` |
| New deps | **none** — `ptrace` is in the already-direct `go.opentelemetry.io/collector/pdata v1.61.0` |

**Harness capture contract (per language).** Both shims emit the **same** span wire; the interception point is the
`POST /` invoke handler in each (Node `createApp`, [shim.ts:44-73](../../shim/nodejs/src/shim.ts); the Python
equivalent). Per language:

| | Node (`shim/nodejs`) | Python (`shim/python`) |
|---|---|---|
| **Context carrier** | `AsyncLocalStorage` around the handler call | `contextvars` set per invocation |
| **traceparent read** | `c.req.header('traceparent')` (Hono) | the ASGI/WSGI request headers |
| **id minting** | `crypto.randomBytes(16/8).toString('hex')` | `secrets.token_hex(16/8)` |
| **span emitted** | after the handler settles, on the funclog channel (`funcd.signal:"traces"`) | same |
| **log correlation** | `buildRecord` reads the ALS context → `inv`/`trace_id`/`span_id` populated | the `logging.Handler` reads the contextvars |

Shared rules: one NDJSON object + `\n` per span, synchronous write to the channel (the ADR-0081 framing); a broken
channel drops the span, never crashes the function; a malformed/absent `traceparent` ⇒ mint a root (never reject the
invocation).

## Implementation plan

- **Files (Go)**: `internal/funclog/span.go` (`Span`, `SpanKind`, `SpanStatus`, `SignalTraces`); `internal/funclog/tracesink.go`
  (`TraceSink`, `BlobTraceSink` — factor the segment-seal core out of `sink.go` or mirror it; `ptrace.JSONMarshaler`;
  `traces/` key builder); `internal/funclog/route.go` (`Sinks`, `Route` — the signal demux; peek `funcd.signal`, decode
  `Entry` or `Span`); config key `funclog.traces` in `internal/platform/config`; wire the traces sink + swap the
  per-instance `Pump` for `Route` at [pkg/funcd/funcd.go:598-618](../../pkg/funcd/funcd.go) (+ close the trace sink at
  shutdown). No `go.mod` change.
- **Files (shims)**: `shim/nodejs/src/tracespan.ts` (parse `traceparent`, mint ids, the ALS context, build+emit the
  span record) + wire it into `createApp`'s `POST /` and make `funclog.ts:buildRecord` read the ALS context;
  `shim/python/src/funcd_shim/tracespan.py` + the contextvars wiring in the Python invoke handler + its logging handler.
  Rebuild `shim.mjs`/`pool.mjs` bundles.
- **Test plan** (one acceptance test per Scenario, names echoing `scenario: <name>`):
  - `funclog` unit/contract tests over a **real in-memory `blob.Bucket`** (no mocks) — the marshal/route/persist tier:
    `persisted-via-blob` (sealed segment → exactly one `Put`, body round-trips via `ptrace.JSONUnmarshaler`),
    `identity-tagged`, `signal-demux` (a mixed channel of log+span lines routes correctly; an untagged line → logs),
    `transport-fd3-and-uds` (both readers feed the same `Route`), plus a `Span{Status:ERROR}` marshal round-trip.
  - shim tests (runtime-gated) — the behavioral-span tier, over `createApp` with a stub channel: Node
    `invocation-emits-span` (one `SERVER` span, `OK`, name/start/end), `error-span-status` (handler throw /
    output-mismatch → `ERROR` + message; input-mismatch 422 → **no** span), `adopt-traceparent`, `mint-root-trace`,
    `logs-correlated-to-span`; Python `python-invocation-span`. (The throw→`ERROR` mapping is a shim behavior — the
    funclog package never sees a handler throw — so it lives here, not in the funclog tier.)
  - an **in-process e2e** (`pkg/funcd`, hermetic — the ADR-0081 `funclog_e2e_test.go` template): boot funcd with an
    in-memory blob, push a Node function, invoke it (once plain, once with a `traceparent` header), then read the blob
    directly — `List("traces/")`, `ptrace.JSONUnmarshaler`, assert `SpanCount() >= 1`, the Resource attrs, and (for
    the header case) `TraceID`/`ParentSpanID` match. A `countSpans` helper mirrors `countLogRecords`.
  - a **Venom containerd lane** (`e2e/traces.venom.yml`, the `funclog.venom.yml` template): invoke a function in-VM,
    assert span objects land under `.../blob/traces/<ns>/<fn>/*/*.otlp.jsonl` (count `spanId`/`traceId`). No read CLI
    (deferred).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario has one passing test (none
  skipped); the ADR-0081 logs e2e + venom lane **still pass** (no logs regression); shim bundles rebuilt; identity/path
  grep clean; `just ci` green after commit. Linux-only/containerd scenarios (the Venom lane) follow the roadmap
  test-sequencing note — ship the in-process Go e2e in-gate; the containerd Venom lane runs on the Lima lane.

## Review checklist

- [ ] Each invocation (that reaches the handler) emits **exactly one** `SERVER` span with name/start/end (→ duration)
      and `OK`/`ERROR` status; an input-mismatch (422, handler never runs) emits no span.
- [ ] `traceparent` present ⇒ span adopts its trace-id + parent span-id; absent/malformed ⇒ a minted **root** (16/8
      random bytes), invocation never rejected.
- [ ] Log records emitted in an invocation carry the **same** `trace_id`/`span_id` as the span (ADR-0081 gap closed);
      the log **wire is unchanged** (fields already existed).
- [ ] Persist is through **`blob.Bucket`** (`Put`), **one object per sealed segment**, OTLP-trace-JSON-Lines via
      `ptrace`, under a `traces/` prefix; Resource carries `namespace`/`function`/`replica`/`tenant`.
- [ ] The host **demux** routes by `funcd.signal`; an untagged line → logs (**no F50 regression** — the logs e2e +
      venom lane still pass).
- [ ] Reuses the fd3/UDS transport + the per-instance drain + `clock.Clock`; **no** second channel, **no** `go.mod`
      change (`ptrace` from the vendored `pdata`).
- [ ] Both shims (Node ALS, Python contextvars) emit the **same** span wire; both languages' logs correlate.
- [ ] Pure-Go (no cgo); one passing acceptance test per Scenario; freeze-safe property preserved (one blocking read).

## Consequences

- **(+)** Every function invocation yields an OTLP span — latency + error, a waterfall when spans share a trace —
  with **zero function code change** (the auto `SERVER` span).
- **(+)** **Closes ADR-0081's correlation gap**: logs emitted in an invocation now carry the invocation's
  trace/span id — logs↔trace correlation, OTLP/W3C-standard, exportable to any backend.
- **(+)** **Reuses the whole F50 pipeline** — transport, drain, `blob.Bucket`, identity, seal machinery — adding only
  a record type + sink + a demux; no new dep, no second channel.
- **(+)** The **primitive for workflow "one run = one trace"**: each step-function invocation already adopts an
  incoming `traceparent`; the follow-on only has to *stamp* it on dispatch.
- **(−)** **Auto span only** — in-function (SDK) spans are a later additive lane; the baseline is one span per
  invocation, not a rich internal tree.
- **(−)** **Raw OTLP-JSONL, no compaction/read** here — traces compaction + a `funcdctl traces` verb are named
  follow-ons (mirroring the F50 → F53/F54 split).
- **(−)** **Private NDJSON wire** extended with `funcd.signal` — funcd owns the contract across shim languages (same
  trade ADR-0081 accepted).
- **Risk**: the demux touches the proven logs drain. Mitigated: log lines stay **untagged** (schema-identical wire —
  the existing `trace_id`/`span_id` fields just populate), the demux defaults untagged → logs, `json.Unmarshal` ignores
  unknown keys, and the ADR-0081 logs e2e + venom lane are re-run in-gate as the regression guard.

## Open questions

- **Monotonic vs wall clock for span duration** — start/end use the harness clock; wall-clock skew across a freeze is
  a display detail (duration is derived from a single process's clock). Resolved at implementation (use a monotonic
  base, stamp epoch nanos at emit).
- **Sampling** — every invocation is captured (no sampling) in V1, matching logs (capture-all). A sampling policy is a
  later concern if span volume proves material (measured by the in-process e2e + a homebox bench lane).
- **fn→fn / workflow `traceparent` stamping** — who *sets* the header on dispatch (so a run is one trace) is the
  workflow-observability follow-on; this ADR only *adopts* it.

## References

- [ADR-0081](0081-function-log-capture-side-channel-blob.md) — the logs signal + the signal-generic pipeline this
  reuses; its `trace_id`/`span_id` open question is closed here.
- `go.opentelemetry.io/collector/pdata/ptrace` — `ptrace.Traces`, `JSONMarshaler`/`JSONUnmarshaler` (Apache-2.0;
  same vendored data-model module as `plog`, `v1.61.0`).
- [W3C Trace Context](https://www.w3.org/TR/trace-context/) — the `traceparent` header format (`00-<trace>-<span>-<flags>`).
- [ADR-0007](0007-blob-storage-layer-port.md), [ADR-0011](0011-runtime-sandbox-port.md),
  [ADR-0030](0030-runtime-shim-contract.md), [ADR-0058](0058-typed-function-io-contract.md).
- FEAT-0004/F51 (Traces capture); F53/F54-analogs (traces compaction / read) and the workflow-observability
  follow-on (dispatch-side `traceparent` stamping) build on this.
