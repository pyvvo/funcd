# ADR-0103: Engine-emitted run-root span — a run span at the top of its trace

- **Status**: Implemented
- **Date**: 2026-07-06 (Implemented 2026-07-06 after the review gate — pass, 0 findings; Accepted 2026-07-06 after the judge pass — no Blockers; folded 1 Major (added the
  `run-span-on-cancel` + `run-span-on-contract-reject` scenarios/tests — the two distinct emit sites the
  single-chokepoint design rests on) and 5 Minors (emitRunSpan guards `rec == nil` + `rec.Terminal()`; the
  wiring passes a nil *interface* not a typed-nil; the exactly-once wording — no-dup via the terminal
  short-circuit, at-most-once miss-window accepted; the seam surface named honestly; sink Close ownership))
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, traces, otel, spans, trace-context, run-span
- **Realizes**: [FEAT-0005/F67](../feat/0005-feat-workflow-engine.md) — completing the **"one run = one trace"**
  increment (ADR-0102 propagated the run trace to the steps; this emits the run's own root span)
- **Relates to**: [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (mints the run's `TraceID` +
  `RootSpanID` this ADR emits as a span; opened this as its named follow-on) · [ADR-0101](0101-trace-capture-invocation-span.md)
  (the `funclog.TraceSink`/`Span`/`Resource` this reuses — the engine writes to the same sink) ·
  [ADR-0094](0094-workflow-engine-core.md) (the run reconciler + run-state this extends)

## Context & Need

**Purpose**: emit **one OTel span for the workflow run itself** at the top of the run's trace, so the run's
step spans (F51) nest under a real root that carries the run's **total duration** and **overall status**.
**Callers**: whoever opens a run's trace in an OTLP backend (operator / agent author).

**Why now**: ADR-0102 mints a `RootSpanID` per run and parents every step's `traceparent` on it — but the
engine never **emits** a span for that id, so a run's trace is N step spans pointing at a **phantom** parent.
Tracing UIs group them under the trace but show no run-level span: no total wall-clock, no run pass/fail at
the top. ADR-0102 deferred emitting it explicitly ("an engine-emitted run-root span … deferred to keep the
engine's import graph clean"); this ADR is that follow-on — it adds the `internal/workflow → internal/funclog`
seam and emits the span.

## Scenarios

Each becomes a named acceptance test.

- **scenario: run-span-on-success** — *Given* a run reaches `Succeeded`, *Then* exactly **one** span is emitted
  with `SpanID` = the run's `RootSpanID`, `TraceID` = the run's trace, **no parent** (the trace root), kind
  `INTERNAL`, status `OK`, and `End ≥ Start` (the run's wall-clock).
- **scenario: run-span-on-failure** — *Given* a run reaches `Failed` (a step fails), *Then* the run-root span is
  emitted with status `ERROR`.
- **scenario: run-span-on-cancel** — *Given* a run is cancelled (the `cancelRun` path, a **distinct** emit site
  from the drive path), *Then* one run-root span is emitted with status `ERROR`.
- **scenario: run-span-on-contract-reject** — *Given* a run fails the run-start contract gate before any step
  executes (Failed at admission), *Then* one run-root span is emitted with status `ERROR` and **no** step spans
  exist under it.
- **scenario: run-span-parents-steps** — *Given* a completed run, *Then* the run's step spans (F51) carry
  `ParentSpanID` = the run-root span's `SpanID` — the steps nest under the run (a real waterfall).
- **scenario: run-span-once** — *Given* a run that has terminated, *When* it is reconciled again, *Then* the
  run-root span is **not** re-emitted (the terminal short-circuit fires it exactly once).
- **scenario: no-sink-no-span** — *Given* traces are disabled (no trace sink), *When* a run terminates, *Then*
  no run-root span is emitted and the run completes unaffected (nil-safe, additive).
- **scenario: e2e-run-root-span** — *Given* a real workflow run over the shim platform, *Then* `blob/traces`
  contains the run-root span (kind `INTERNAL`, no parent) sharing the run's trace-id with the step spans, and
  the step spans parent on it.

## Scope

**In**: (a) the **`RunReconciler` emits one run-root span** when a run first reaches a terminal phase
(`Succeeded`/`Failed`/`Cancelled`) — `SpanID` = `rec.RootSpanID`, `TraceID` = `rec.TraceID`, no parent, kind
`INTERNAL`, name = the workflow, `Start`/`End` from the run record's timestamps, status from the phase; (b) the
span is written to the **same `funclog.TraceSink`** the F51 step spans use (one sink → one coherent trace in
blob); (c) the **seam**: `internal/workflow` imports `internal/funclog` (a `TraceSink` handle on the reconciler)
— **nil-safe** (no sink ⇒ no root span, unchanged). One shared trace sink is wired in `pkg/funcd`.

**Out** (still-deferred follow-ons):
- **Nested DAG parenting** — steps still parent on the run **root**, not on their DAG predecessor's span; a
  true per-edge waterfall needs F51 to consume a passed span-id (a V2 refinement, unchanged by this ADR).
- **Cross-sub-workflow trace linking** — a sub-workflow child run is still its own trace (ADR-0102's per-run
  scope); linking parent+child into one trace remains a follow-on.
- **Intermediate/step spans from the engine** — the engine emits only the *run* span; the per-step spans come
  from F51's shim (unchanged). No engine-emitted span per step.
- **A run-span for a never-started / admission-rejected run** — a run that fails the run-start contract gate
  before executing has a trace context but no meaningful duration; it still emits a root span (status ERROR)
  for consistency, but no step spans exist under it.

## Constraints & Decision drivers

- **Reuse F51's sink + record model** — the engine writes a `funclog.Span` to the existing `TraceSink`; no new
  span type, no new wire, no new dependency (`ptrace` already vendored via F51).
- **The seam is intentional and bounded** — `internal/workflow` now imports `internal/funclog` for the span
  construction surface (`Span`, `Resource`, the `SpanInternal`/`StatusOk`/`StatusError` enums) + the `TraceSink`
  port. This is the exact coupling ADR-0102 deferred; all value types + one behavioral call, one package,
  nil-safe, acyclic (funclog does not import workflow).
- **Exactly once** — the run-root span is emitted at the **terminal transition** only, **after** the terminal
  status is persisted; a terminal `WorkflowRun` short-circuits the reconciler at the top, so **no duplicate** can
  occur (the equal `SpanID` is idempotent belt-and-suspenders regardless). The narrow residual is the reverse — a
  crash in the sub-millisecond window between the status persist and the emit loses that one best-effort span
  (at-most-once); accepted, given the span is best-effort and the window is tiny.
- **Additive / non-breaking** — no sink ⇒ no span; the run's execution, status, and retention are unchanged.
- **One sink, one trace** — the run-root span and the step spans must land in the **same** blob trace store so
  the trace is coherent; `pkg/funcd` builds one `BlobTraceSink` and shares it.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Emit in the engine core** (at each terminal-setting site: drive-success, `fail`, `Cancel`) | The engine owns `rec.Phase`, but terminal is set in **three** exclusive places → three emit sites. The reconciler sees every terminal outcome in **one** place (after `drive`, plus `cancelRun`), so it is the cleaner single "run just terminated" chokepoint. |
| **Emit the run-root span at run START** (so it exists early) | A span needs an end time; emitting at start means updating/re-emitting at end (two writes) or a zero-duration span. The run record already holds `StartedAt`; emitting once at terminal with both timestamps is simpler and correct. |
| **A dedicated engine "tracer" abstraction** (not funclog) | Another port for one call. Rejected: `funclog.TraceSink` already exists (F51), takes a `Span`, and persists to the same blob — reuse it, don't invent a parallel seam. |
| **Keep the phantom root (ADR-0102 status quo)** | The trace groups steps but shows no run-level span (no total duration, no run status). That is exactly the gap this ADR closes. |

## Decision

The `RunReconciler` emits one run-root span, into the shared `funclog.TraceSink`, when a run first reaches a
terminal phase.

1. **The span.** At the terminal transition the reconciler builds a `funclog.Span`:
   `TraceID = rec.TraceID`, `SpanID = rec.RootSpanID`, `ParentID = ""` (root), `Name = string(rec.Workflow)`,
   `Kind = funclog.SpanInternal`, `Start = time(rec.StartedAt)`, `End = time(rec.UpdatedAt)`,
   `Status = OK` iff `Succeeded` else `ERROR`, `Attrs = {"funcd.run": <run>, "funcd.phase": <phase>}`. It
   `AppendSpan`s it with `Resource{Namespace: rec.Namespace, Function: rec.Workflow, Replica: rec.Name}` — so
   run-root spans land under `traces/<ns>/<workflow>/`, keyed by run name.
2. **When.** In `Reconcile`, after the drive + `updateRunStatus`, if `rec` is terminal → emit. In `cancelRun`,
   after the terminal status is written → emit. These two are exclusive (a run completes/fails via drive, or is
   cancelled), so the span is emitted once. A run with no trace context (`rec.TraceID == ""`) or no sink emits
   nothing.
3. **The seam + sink sharing.** `NewRunReconciler` gains a `traces funclog.TraceSink` argument (nil ⇒ no root
   span). `pkg/funcd` builds **one** `BlobTraceSink` (whenever a blob substrate is present and traces are
   enabled) and passes it to **both** the reconciler (this ADR's root span) and the F51 shim-capture demux — so
   the run-root span and the step spans persist to the same trace store.

## Temporary workarounds

- **Flat parenting persists.** Step spans parent on the run root, not their predecessor — the waterfall is
  one level deep (run → steps), not the full DAG shape. *Exit*: the nested-DAG-parenting follow-on.
- **No cross-sub-workflow root nesting.** A sub-workflow child run emits its **own** run-root span under its own
  trace; it does not nest under the parent's sub-workflow step. *Exit*: the cross-sub-workflow linking follow-on.

## Contracts

```go
// internal/workflow (reconcile_run.go) — the RunReconciler gains an optional trace sink (ADR-0103) and emits
// one run-root span at the terminal transition. funclog.Span/TraceSink/Resource are reused verbatim (F51).

// NewRunReconciler gains the trace sink (nil ⇒ no run-root span emitted; additive).
func NewRunReconciler(s store.Store, e *Engine, traces funclog.TraceSink, log *slog.Logger) *RunReconciler

// emitRunSpan writes one INTERNAL span for the terminal run into the trace sink. It is a defensive no-op
// unless ALL hold: traces != nil, rec != nil (drive can return a nil rec on a store error), rec.Terminal()
// (never emit for a still-running/paused rec), and rec.TraceID != "" (a run with no trace context). SpanID is
// the run's RootSpanID (ADR-0102), so the step spans (parented on it) nest under this root. Best-effort: a sink
// error is logged, never failing the reconcile.
func (r *RunReconciler) emitRunSpan(ctx context.Context, rec *runstate.Record)
```

**Blob layout** — the run-root span persists as OTLP-trace-JSONL under the workflow's own prefix:

```
traces/<ns>/<workflow>/<date>/<unixnano>-<run>.otlp.jsonl     # the run-root span (kind INTERNAL, no parent)
traces/<ns>/<workflow>-<step>/<date>/<unixnano>-<replica>...  # the step spans (F51), parent = the run root
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the ADR-0102 run trace context (`rec.TraceID`/`RootSpanID`) + `rec.StartedAt`/`UpdatedAt`/`Phase` · the ADR-0101 `funclog.TraceSink` (**new import**: `internal/workflow` → `internal/funclog`) |
| Exposes | one OTLP `INTERNAL` run-root span per terminal run in `blob/traces/<ns>/<workflow>/` — the real root of the run's trace |
| New deps | **none** (`funclog` is an internal package already built; `ptrace` already vendored via F51) |

## Implementation plan

- **Files**: `internal/workflow/reconcile_run.go` (`RunReconciler.traces` field + `NewRunReconciler` param;
  `emitRunSpan`; call it after `updateRunStatus` in `Reconcile` when `rec` is terminal, and in `cancelRun`);
  `pkg/funcd/funcd.go` (hoist the `BlobTraceSink` construction above the workflow wiring — guarded on
  `blob != nil && !funclogDisabled && !funclogTracesDisabled`, **not** on `LogCapturer` since the root span is
  emitted in-process; pass it to `NewRunReconciler`; reuse the same sink in the F51 capture block instead of
  building a second). **Pass a nil interface, not a typed nil**: declare `var traces funclog.TraceSink` and
  assign only when the sink is built (mirroring the existing F51 block) — passing a typed-nil `*BlobTraceSink`
  would make `traces == nil` false and panic on `AppendSpan`. **Close ownership**: `pkg/funcd` closes the shared
  sink exactly once at shutdown (before `blob.Close()`, as today); the reconciler only holds a reference and
  never closes it. No `go.mod` change.
- **Test plan** (one named test per Scenario):
  - `reconcile_run_test.go` unit, over a **capturing fake `funclog.TraceSink`** + the real engine/store: drive a
    run to `Succeeded` → `run-span-on-success` (one span, `SpanID == rec.RootSpanID`, no parent, `INTERNAL`, `OK`,
    `End ≥ Start`); a step-failing run → `run-span-on-failure` (`ERROR`); a **cancelled** run via the reconciler's
    `cancelRun` → `run-span-on-cancel` (one `ERROR` span — the distinct second emit site); a **contract-gate-
    rejected** run (a run-start `InputSchemaMismatch`) → `run-span-on-contract-reject` (one `ERROR` span, zero step
    spans); a second reconcile of the terminal run → `run-span-once` (no second AppendSpan); a nil sink →
    `no-sink-no-span` (completes, no panic).
  - **in-process e2e** (`pkg/funcd`, extending the ADR-0102 workflow-trace e2e): a real 2-step run → read
    `blob/traces`, find the run-root span (no parent) and assert the step spans' `ParentSpanID` equals its
    `SpanID` (`run-span-parents-steps` / `e2e-run-root-span`).
  - **Venom** (the containerd workflow lane): after a run succeeds, assert `traces/<ns>/orders/` contains a
    run-root span (kind `INTERNAL`) — the engine emitted it on real containerd.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario a passing test; the
  in-process e2e + the workflow Venom lane green; the ADR-0101/0102 traces e2es still pass; feat F67 note
  updated; identity/path grep clean; `just ci` green after commit. (The new `internal/workflow → internal/funclog`
  import is the sanctioned seam — the ADR-0102 no-funclog-import checklist item is intentionally superseded here.)

## Review checklist

- [ ] A terminal run emits **exactly one** `INTERNAL` span with `SpanID == rec.RootSpanID`, no parent,
      `Start`/`End` = the run's timestamps, status `OK`/`ERROR` by phase.
- [ ] The run's step spans (F51) parent on the run-root span's `SpanID` (they nest under it — a real waterfall).
- [ ] Emitted **once** (terminal transition only; a re-reconcile of a terminal run emits nothing).
- [ ] `traces == nil` (or an empty trace context) ⇒ **no** span, run unaffected (nil-safe, additive).
- [ ] The run-root span and the step spans share **one** `BlobTraceSink` (one trace store); `pkg/funcd` builds it once.
- [ ] `internal/workflow` imports `internal/funclog` only for the `TraceSink` port; no `go.mod` change; a sink
      error never fails the reconcile.
- [ ] Sub-workflow child runs still emit their own run-root span (per-run scope unchanged).

## Consequences

- **(+)** The run's trace has a **real root** — total wall-clock + overall status at the top, step spans nested
  under it: the complete run waterfall, exportable to any OTLP backend.
- **(+)** **Reuses F51's sink** — one `BlobTraceSink`, one coherent trace store; no new type, wire, or dependency.
- **(+)** **Nil-safe / additive** — traces off ⇒ nothing changes; the run's execution/status/retention untouched.
- **(−)** **The seam lands** — `internal/workflow` now imports `internal/funclog` (the `Span`/`Resource`/kind+status
  enums it builds + the `TraceSink` port). This is the deliberate coupling ADR-0102 deferred; bounded to value
  types + one call, acyclic.
- **(−)** **Flat, per-run still** — steps parent on the run root (not predecessors), and sub-workflow child runs
  are separate traces; both remain named follow-ons.

## Open questions

- **Failure cause on the run span** — V1 sets `ERROR` without the failing step's message (the reconciler holds
  the phase, not the cause). Threading the cause onto the span is a small enhancement (or it rides the
  describe-lineage record, ADR-0100).
- **Nested DAG parenting + cross-sub-workflow linking** — the remaining two follow-ons that, with this ADR,
  complete the full distributed run trace.

## References

- [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) — mints the run `TraceID`/`RootSpanID` this emits.
- [ADR-0101](0101-trace-capture-invocation-span.md) — the `funclog.TraceSink`/`Span` reused; the step spans this roots.
- [ADR-0094](0094-workflow-engine-core.md) — the run reconciler + run-state.
- [W3C Trace Context](https://www.w3.org/TR/trace-context/); OTLP `ptrace` (span kind `INTERNAL`).
- FEAT-0005/F67 (one run = one trace — this completes the trace-emission side; describe-lineage is ADR-0100).
