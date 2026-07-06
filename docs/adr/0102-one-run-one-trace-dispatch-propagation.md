# ADR-0102: One run = one trace — run-scoped W3C trace-context propagation on step dispatch

- **Status**: Implemented
- **Date**: 2026-07-06 (Implemented 2026-07-06 after the review-gate pass — build/lint/mod/test green, every Scenario a
  named passing test, the in-process e2e + a fresh containerd Venom lane share one trace-id, no `internal/funclog` import,
  no `go.mod` change; observed at `Accepted` on review entry as the `adr-impl → Reviewing` handoff bump was not applied.
  Accepted 2026-07-06 after the judge pass — no Blockers/Majors, the propagation chain
  verified end-to-end; folded 3 Minors: the `mintTraceContext` error path (proceed with an empty context, never
  fail the run), the `dispatchStep` signature-threading note for the retry-loop dispatch, and a not-a-security-boundary
  line; plus the RootSpanID↔ParentSpanID naming clarification)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, traces, otel, trace-context, w3c, dispatch, correlation
- **Realizes**: [FEAT-0005/F67](../feat/0005-feat-workflow-engine.md) — the **"one run = one trace"** increment
  (the complementary describe-lineage recording increment is the parked [ADR-0100](0100-run-step-lineage.md))
- **Relates to**: [ADR-0101](0101-trace-capture-invocation-span.md) (F51 — the per-invocation `SERVER` span that
  **adopts** the `traceparent` this ADR sets; the primitive this completes) · [ADR-0094](0094-workflow-engine-core.md)
  (the engine, step dispatch, run-state, Resume this extends) · [ADR-0081](0081-function-log-capture-side-channel-blob.md)
  (the funclog trace fields the correlated logs land in)

## Context & Need

**Purpose**: make **all step-function invocations of one workflow run share one OTel trace**, so an operator
troubleshooting a run gets a single-trace waterfall of its steps (and their correlated logs) instead of N
disconnected traces. **Callers**: whoever runs / debugs a workflow — the operator, the agent author.

**Why now**: F51 ([ADR-0101](0101-trace-capture-invocation-span.md)) made every function invocation emit a `SERVER`
span that **adopts** an incoming W3C `traceparent` — but the workflow engine sets **no** `traceparent` on step
dispatch ([dispatch.go:129-130](../../internal/workflow/dispatch.go) sets only `Content-Type` + `X-Funcd-Attempt`),
so each step's span mints its **own root trace**: a run's steps scatter across as many traces as it has steps. F67's
headline — "run/step IDs stamp the canonical trace fields so **one run = one trace**" — is exactly this missing half.
It is a pure add-on to F51: the engine mints one trace context per run and stamps it on every dispatch; the shim
already does the rest.

## Scenarios

Each becomes a named acceptance test.

- **scenario: run-mints-trace-context** — *Given* a fresh run starts, *Then* the run record carries a minted W3C
  **trace-id** (32 hex) and a **root span-id** (16 hex).
- **scenario: steps-share-one-trace** — *Given* a multi-step run, *When* each step dispatches, *Then* every step's
  `traceparent` carries the **same** run trace-id — the steps are one trace.
- **scenario: steps-parent-on-run-root** — *Given* a step dispatch, *Then* its `traceparent` **parent** is the run's
  root span-id (each step invocation is a child of the run).
- **scenario: retries-share-trace** — *Given* a step that retries, *When* each attempt dispatches, *Then* all attempts
  carry the same run trace-id (one trace, a span per attempt).
- **scenario: resume-keeps-trace** — *Given* a run resumes after a crash, *When* the remaining steps dispatch, *Then*
  they carry the **persisted** run trace-id (the context is reused, never re-minted) — pre- and post-crash steps stay
  in one trace.
- **scenario: no-context-no-header** — *Given* a dispatch with no trace-id (a legacy/empty run record), *Then* no
  `traceparent` header is set (unchanged dispatch) — the feature is additive, never breaking.
- **scenario: e2e-one-run-one-trace** — *Given* a real 2-step workflow run over the shim platform, *Then* both steps'
  captured spans (F51) in blob carry the **same** trace-id — the run is one trace end-to-end.

## Scope

**In**: (a) mint a **run trace context** — `TraceID` (W3C trace-id, hex32) + `RootSpanID` (hex16) — at run start,
persist it in `runstate.Record`, and **reuse** it on Resume/recovery (never re-mint); (b) carry it on
`DispatchRequest`; (c) set a W3C **`traceparent: 00-<TraceID>-<RootSpanID>-01`** header on every **function**-step
dispatch. F51's shim adopts it → each step's `SERVER` span joins the run's trace under the run root.

**Out** (named follow-ons):
- **An engine-emitted run-root span.** V1 propagates a run root span-**id** that the engine does **not** itself emit
  as a span — so the trace *groups* the step spans under a common (synthetic) root, but there is no explicit root
  span carrying the run's total duration/status. Emitting one would couple `internal/workflow` to `internal/funclog`
  (a trace sink handle in the engine); deferred to keep the engine's import graph clean. The named follow-on adds the
  engine→funclog seam.
- **Nested DAG parenting.** V1 parents every step on the **run root** (flat — all steps are siblings of one trace).
  A true nested waterfall (step B parented on predecessor A's *actual* span) needs A's span-id, which F51 mints
  **inside the sandbox** and does not return; a V2 refinement (the engine pre-mints per-step span-ids the shim
  *uses* rather than mints).
- **Cross-sub-workflow trace linking.** A sub-workflow child run (ADR-0099, `execute` at depth>0) mints its **own**
  trace — V1 is one trace per `WorkflowRun`. Linking a parent run and its child runs into one distributed trace is a
  follow-on.
- **Builtin steps** (`wait`/`pass`) invoke no function → no span → no `traceparent` (only the function-dispatch path
  carries it). Traces read/compaction (F51's follow-ons) and the describe-lineage recording ([ADR-0100](0100-run-step-lineage.md)).

## Constraints & Decision drivers

- **Reuse F51 + the W3C standard** — the engine sets a *standard* `traceparent`; F51's shim already adopts it. No new
  mechanism, no new wire.
- **Keep the engine's import graph clean** — the engine sets a header **string**; it does **not** import
  `internal/funclog` (no root-span emission in V1). Propagation only.
- **Stable across retries + resume** — the context is minted **once** at run start, persisted, never re-minted; every
  attempt and every resumed step reuse it.
- **Additive / non-breaking** — a run with no trace context dispatches exactly as before (no header).
- **Not a security boundary** — the trace-id is **engine-minted** (never client-supplied), and spec-as-grant
  (declared-target authorization, `dispatch.go` `grant.Allow`) + the host-stamped `Resource` identity are unchanged;
  a trace context only groups a waterfall, it grants nothing.
- **Zero new dependency** — `crypto/rand` mints the ids; no `go.mod` change.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Engine emits the run-root span** (into the funclog trace sink) | A complete waterfall with a real root span carrying the run's total duration/status — but couples `internal/workflow` → `internal/funclog` (a trace-sink handle wired into the engine), widening the import graph for polish. **Retained as the named follow-on**; V1 delivers "one run = one trace" by propagation alone. |
| **Nested DAG parenting** (step B parented on A's span) | A true per-edge waterfall — but the engine can't know A's span-id (F51 mints it *inside* the sandbox and never returns it). Would need F51 to return the id, or the engine to pre-mint per-step ids the shim *uses*. A V2 refinement; flat-under-root is already a correct single trace. |
| **Per-invocation trace only** (status quo — what F51 does alone) | Each invocation is its own root trace; a run's steps scatter across N traces. That is precisely the gap this ADR closes. |
| **Propagate via the CloudEvent body** (a `traceparent` field in `stepEvent`) instead of an HTTP header | Non-standard — the shim reads the W3C **header** (F51), and OTel tooling expects `traceparent` on the transport. Rejected: use the standard header. |

## Decision

The engine mints one W3C trace context per run and stamps it on every function-step dispatch; F51's shim adopts it.

1. **Run trace context (`runstate.Record`).** `execute` mints `TraceID` (16 random bytes → hex32) + `RootSpanID`
   (8 random bytes → hex16) via `crypto/rand` when it creates a fresh record, and persists them. `Resume`/recovery
   load the record and **reuse** them (they are already on the persisted record — never re-minted). A sub-workflow
   child run (`execute` at depth>0) mints its **own** context (per-run trace; cross-boundary linking deferred). If
   minting fails (a near-impossible `crypto/rand` error), the run proceeds with an **empty** context — no
   `traceparent`, additive — and **never fails on it** (consistent with the additive/non-breaking driver).
2. **Dispatch carries it (`DispatchRequest`).** The struct gains `TraceID` + `ParentSpanID`; the engine's dispatch
   loop passes `rec.TraceID` + `rec.RootSpanID`.
3. **The header (`HTTPDispatcher.Dispatch`).** When `TraceID != ""`, set
   `httpReq.Header.Set("traceparent", "00-"+TraceID+"-"+ParentSpanID+"-01")` (W3C Trace Context, version 00, sampled
   flag 01). An empty `TraceID` sets no header (additive/non-breaking). Only the function-dispatch path sets it;
   builtins don't dispatch.
4. **The shim closes the loop (F51, unchanged).** Each step-function invocation reads the `traceparent`, adopts its
   trace-id + parent, and emits its `SERVER` span accordingly → every step span of the run shares the run's trace,
   parented on the run root. The step's console logs (F51 correlation) carry the same trace-id too.

## Temporary workarounds

- **Synthetic (un-emitted) run root.** The `RootSpanID` names a root the engine does not itself emit as a span; the
  trace *groups* the step spans under it, but there is no root span with the run's duration/status. *Exit*: the
  engine→funclog root-span follow-on.
- **Flat parenting.** Every step parents on the run root, not its DAG predecessor. *Exit*: the nested-parenting V2
  refinement (needs F51 to consume a passed span-id).

## Contracts

```go
// runstate.Record gains (ADR-0102): the run's W3C trace context, minted at run start and persisted so every step
// (and every retry, and every resumed step) shares one trace. Reused by Resume, never re-minted.
TraceID    string `json:"traceId,omitempty"`    // W3C trace-id, 32 lowercase hex (16 bytes)
RootSpanID string `json:"rootSpanId,omitempty"` // the run's root span-id, 16 hex (8 bytes); surfaces as each
//                                              // dispatch's ParentSpanID — every step parents on the run root

// DispatchRequest gains (engine.go): the trace context a step dispatch propagates.
TraceID      string // the run's trace-id (hex32); "" ⇒ no traceparent header (additive)
ParentSpanID string // the parent span-id (hex16) the step joins — the run root

// mintTraceContext returns a fresh (traceID hex32, spanID hex16) via crypto/rand. Used once per run at start.
func mintTraceContext() (traceID, rootSpanID string, err error)
```

**Wire** — a standard W3C Trace Context header on every function-step dispatch:

```
traceparent: 00-<traceId(hex32)>-<rootSpanId(hex16)>-01
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the ADR-0094 engine (step dispatch, run-state, Resume) · `crypto/rand` · the ADR-0101 shim (which adopts the header) |
| Exposes | a W3C `traceparent` on every function-step dispatch → **one trace per run** (the run's step spans + their correlated logs share `traceId`) |
| New deps | **none** (`crypto/rand`, stdlib); **no** `internal/funclog` import in `internal/workflow` |

## Implementation plan

- **Files**: `internal/workflow/runstate/runstate.go` (`Record.TraceID`/`RootSpanID`); `internal/workflow/engine.go`
  (`mintTraceContext` helper; mint + set on the record in `execute`; `DispatchRequest.TraceID`/`ParentSpanID`; pass
  `rec.TraceID`/`rec.RootSpanID` at the dispatch construction — both the retry loop and the onFailure dispatch. The
  retry-loop `DispatchRequest` is built inside `dispatchStep`, whose signature carries neither `rec` nor the trace
  fields today, so thread `traceID`/`rootSpanID` (or `rec`) into it — else the header lands only on the onFailure path);
  `internal/workflow/dispatch.go` (set the `traceparent` header when `TraceID != ""`). No `go.mod` change.
- **Test plan** (one named test per Scenario):
  - `dispatch` unit (`dispatch_test.go` pattern — a stub HTTP server capturing headers): `steps-parent-on-run-root`
    (a `DispatchRequest` with TraceID+ParentSpanID → the exact `traceparent` header), `no-context-no-header` (empty
    TraceID → no header).
  - engine unit (over the fake dispatcher, capturing every DispatchRequest): `run-mints-trace-context` (execute mints
    hex32/hex16 on the record), `steps-share-one-trace` (a 2-step run → both dispatches share the run trace-id, parent
    = root), `retries-share-trace` (a retried step's attempts share it), `resume-keeps-trace` (a resumed run reuses the
    persisted context, not re-minted).
  - **in-process e2e** (`pkg/funcd`, the ADR-0101 traces-e2e pattern): a real 2-step Node workflow run → read
    `blob/traces/`, assert both steps' spans carry the **same** trace-id (= the run's). "One run = one trace" end to end.
  - **Venom** (the containerd `workflow` lane): run a workflow, assert its step spans in `blob/traces/` share one
    trace-id (an in-VM grep that the distinct traceIds count is 1).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario a passing test; the in-process
  e2e + the workflow Venom lane green; **no `internal/funclog` import in `internal/workflow`** (grep); feat F67 row
  advanced; identity/path grep clean; `just ci` green after commit.

## Review checklist

- [ ] A fresh run mints `TraceID` (hex32) + `RootSpanID` (hex16) on the record; Resume **reuses** them (not re-minted).
- [ ] Every function-step dispatch sets `traceparent: 00-<traceId>-<rootSpanId>-01`; an empty context sets **no** header.
- [ ] All steps of a run (and all retry attempts) share the **same** trace-id; each parents on the run root.
- [ ] The engine sets a header **string** only — **no** `internal/funclog` import in `internal/workflow`.
- [ ] Only the function-dispatch path carries the header; builtin (`wait`/`pass`) steps are untouched.
- [ ] Additive: `Record`/`DispatchRequest` gain fields; no behavior change when the context is absent; `go.mod` unchanged.
- [ ] End-to-end (Go + Venom): a run's step spans in blob share one trace-id.

## Consequences

- **(+)** **One run = one trace** — a run's step-function invocations (and their correlated logs) collapse into a
  single trace: the troubleshooting waterfall F67 promised, exportable to any OTLP backend.
- **(+)** **Pure add-on to F51 + W3C** — the engine sets one standard header; the shim already adopts it. No new
  mechanism, no new dependency, no new wire.
- **(+)** **Import-graph clean** — the engine stays free of `internal/funclog`; it emits no span, only a header string.
- **(+)** **Stable under retries + resume** — the context is minted once and persisted, so the trace survives crashes
  and re-dispatch.
- **(−)** **No engine-emitted root span** in V1 — the run root is a synthetic (un-emitted) parent; the trace groups the
  steps but carries no explicit run-duration span (named follow-on).
- **(−)** **Flat parenting** — steps are siblings under the run root, not a nested DAG waterfall (V2).
- **(−)** **Per-run scope** — a sub-workflow child run is a separate trace; parent↔child trace linking is deferred.

## Open questions

- **Root-span emission** — emitting the run-root span (run duration/status) needs an `internal/workflow` →
  `internal/funclog` seam (a trace-sink handle on the engine). A focused follow-on; deliberately out of V1 to keep the
  import graph clean.
- **Nested DAG parenting** — parenting a step on its predecessor's span needs F51 to *consume* a passed span-id rather
  than mint its own; a V2 refinement of both the engine and the shim.
- **Cross-sub-workflow linking** — sharing one trace across a parent run and its sub-workflow child runs (ADR-0099).

## References

- [ADR-0101](0101-trace-capture-invocation-span.md) — F51, the per-invocation `SERVER` span that adopts this header.
- [ADR-0094](0094-workflow-engine-core.md) — the engine, dispatch, run-state, Resume.
- [ADR-0081](0081-function-log-capture-side-channel-blob.md) — the funclog pipeline the correlated logs/spans land in.
- [W3C Trace Context](https://www.w3.org/TR/trace-context/) — the `traceparent` header (`00-<trace>-<span>-<flags>`).
- FEAT-0005/F67 (lineage & run observability — the "one run = one trace" increment); [ADR-0100](0100-run-step-lineage.md)
  (the parked describe-lineage increment); F71 (replay) builds on the run's recorded context.
