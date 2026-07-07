# ADR-0100: Per-step troubleshooting lineage in the run status (F67)

- **Status**: Implemented
- **Date**: 2026-07-07
- **Implemented**: 2026-07-07
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, lineage, troubleshooting, describe
- **Acceptance note**: judge M1 (dispatchStep must stamp the *bare* `lastErr` at the retry-loop break, before its own `step %q failed after retries` wrap and `fail()`'s run wrap; `markFailed` only fills `errMsg` if unset) folded into the Decision + Contracts; judge M2 (the `describe` pointer must be plain informational text + ADR-0106 drafted and sequenced) folded into Decision §4 + Temporary workarounds.
- **Realizes**: FEAT-0005/F67 (lineage & run observability — the troubleshooting-`describe` cut)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine + run-state + the `describe` verb this enriches), [ADR-0098](0098-typed-workflow-edges.md) (F65 — filled `status.steps[]`; this adds the per-step run record beside the cached contract), [ADR-0099](0099-sub-workflows.md) (F70 — a sub-workflow step is a step, so it gets the same record; its child run is `describe`-able on its own), [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (mints the run's `TraceID` this mirrors to `status` so `describe` shows it), [ADR-0106](0106-run-scoped-log-read.md) (the sibling: `describe` prints the `funcdctl workflow logs <run>` pointer it delivers — the full error/logs by run).

## Context & Need

`funcdctl workflow describe <run>` today shows, per step, only `name / phase / attempts / revision` — and
**`attempts` is never even populated** (the engine loops retries internally but discards the count; the
field mirrors as 0). So when a run fails you learn *that* it failed, not **which step**, **why**, **how
long** it ran, or **how many times it retried** — you fall back to grepping function logs.

This ADR records the per-step troubleshooting facts **as the step runs** and surfaces them in `describe`:
the **failure cause** (the raw step-level error, **length-capped** so it never bloats the CRD), **timings**
(start/end → duration), and the **attempt count** (fixing the latent gap). It rides `WorkflowRun.status`
(which already persists until retention), so no new store is needed — you are debugging a *recent* run.
It also mirrors the run's **`trace_id`** (minted by ADR-0102) to `status`, so `describe` shows it and can
point you at the full detail: the capped one-liner is the *summary*, and the **full** error/stack + all a
run's logs live in the funclog pipeline (F51/0102–0105) — reachable via the `funcdctl workflow logs <run>`
command that the sibling ADR-0106 delivers. `describe` prints that pointer.

Callers: whoever runs `funcdctl workflow describe` to troubleshoot a run (the operator / the agent author).

## Scenarios

Each becomes a named acceptance test.

- `failed-step-records-cause` — Given a run whose step `b` fails, Then `b`'s status records the **raw
  step-level error** (not the run-level wrap) so `describe` says *which* step failed and *why*.
- `attempts-recorded` — Given a step that retries N times, Then its status records `attempts: N` (today
  it is silently 0).
- `timings-recorded` — Given any step that runs, Then its status records `startedAt`/`endedAt`; a
  succeeded step has `endedAt ≥ startedAt` (→ a duration).
- `describe-surfaces-troubleshooting` — Given a failed run, Then `funcdctl workflow describe` renders a
  readable per-step line: `phase · attempts · duration · error`.
- `error-is-capped` — Given a step that fails with a very long error (a big stack trace), Then the mirrored
  `status` error is **capped** (≤ a fixed length; first line preferred) so the CRD never bloats — the full
  text stays in the logs/trace, not `status`.
- `run-traceid-in-status` — Given a run, Then its `trace_id` (ADR-0102) is mirrored to
  `WorkflowRun.status`, and `describe` prints it (so you can open the trace and run `funcdctl workflow logs`).
- `resume-keeps-history` — Given a crash mid-run, When the run resumes, Then already-terminal steps keep
  their recorded timings/attempts/error (recovery re-runs only the in-flight step).
- `subworkflow-step-recorded` — Given a `workflow:` step (F70), Then it records timings + (on failure)
  the child's failure cause, like any step.

## Scope

**In:** recording per-step `startedAt`/`endedAt`/`error` (**length-capped**)/`attempts` in the run record +
mirroring them to `WorkflowRun.status.steps[]`; mirroring the run's **`trace_id`** to `WorkflowRun.status`;
a readable `funcdctl workflow describe` render that shows the per-step line + the run `trace_id` + a
`funcdctl workflow logs <run>` pointer; restoring the record across `Resume`. **Out (named follow-ons):**
- **The run-scoped log read itself** — `funcdctl workflow logs <run>` (the full error/all-logs-by-run) is
  the **sibling ADR-0106** in this batch; this ADR only records the facts + *prints the pointer* to it.
- **Per-step data lineage** — recording each step's **input/output** (small inline, or a blob ref/digest
  for by-ref payloads) so you can see *what data* each step consumed/produced. The next F67 increment; it
  reuses the ADR-0094 by-reference payload model.
- **Durable audit lineage** — a record that **outlives retention** (provenance/compliance). V1 rides
  `status`, which is swept with the run.
- **The lineage graph / cross-run queries** — "which runs consumed dataset D". A V2 query layer over the
  records, not the records themselves.

## Constraints & Decision drivers

- **Record as it happens, in the engine.** Timings/attempts/error are only knowable at the step's
  terminal transition — capture them there, not reconstructed after.
- **The step-level cause, not the run-level wrap.** `describe` must name the failing step's actual error
  (`dispatch 500`), not the engine's `run "x" failed` envelope.
- **No new store.** Ride `WorkflowRun.status` (persists until retention). Troubleshooting is for a recent
  run; durable audit is a named follow-on.
- **Bounded status growth.** Only the small troubleshooting facts (timings/attempts/error string) mirror
  to `status`; full input/output payloads do **not** (that is the data-lineage follow-on, by-ref). The
  **error string is length-capped** (a fixed `maxStatusError`, e.g. 512 chars, first line preferred) so a
  pathological stack trace can never bloat the CRD — the full text lives in the logs/trace (ADR-0106).
- **Survive recovery.** A resumed run must keep the history of its already-terminal steps.

## Alternatives considered

- **A separate append-only event log (Temporal-style history).** Rejected for V1: funcd is a state
  machine, not event-sourced, and F71 replay is checkpoint-based — a full event history is scope the
  troubleshooting need doesn't justify. A rich per-step record on the existing status is enough.
- **A durable lineage store now.** Rejected for V1: troubleshooting targets a *live/recent* run within
  retention; `status` already persists that long. Durable audit is a named follow-on with its own driver.
- **Mirror full input/output into `status`.** Rejected: unbounded CRD growth. Data lineage records refs
  (by-ref/digest), and lands as the next increment — not the first cut.
- **Reconstruct timings from funclog traces.** Rejected: indirect, lossy, and couples troubleshooting to
  the log pipeline. The engine already holds the clock at each transition.

## Decision

Record the per-step troubleshooting facts in the engine at each step's terminal transition, persist them
in the run record, mirror them to `WorkflowRun.status`, and render them in `describe`.

1. **Engine (internal/workflow).** Two helpers stamp a step's terminal phase + lineage: `markSucceeded(n)`
   sets `Succeeded` + `endedAt`; `markFailed(n, cause)` sets `Failed` + `endedAt` + `errMsg =
   capErr(cause.Error())` **only if `n.errMsg` is not already set**. `startedAt` is stamped when the step
   goes `Running`. **Capturing the *raw* cause (not a wrap).** `dispatchStep` already wraps its own failure
   (`step %q failed after retries: …`) before returning, and `fail()` wraps again at the run level — so the
   `err` reaching the terminal transition is doubly-wrapped. To record the **bare** step cause, `dispatchStep`
   stamps `n.errMsg = capErr(lastErr.Error())` (the un-wrapped dispatch cause, e.g. `scorer returned 503`) at
   the retry-loop break — the same site it now records `n.attempts`. `markFailed` then only fills `errMsg`
   when the kind didn't (builtin `wait`/`pass` and sub-workflow failures pass their raw cause straight to
   `markFailed`). All three step kinds route through `markSucceeded`/`markFailed` for phase + `endedAt`.
2. **Run record (`runstate.StepState`).** Gains `StartedAt`, `EndedAt int64`, `Error string`; `persist`
   writes them (+ the now-populated `Attempts`). `rebuildState` (Resume/recovery) **restores** them for
   already-terminal steps, so a resumed run keeps its history; an in-flight step reset to `Pending` clears
   them (it re-runs).
3. **Status mirror (`RunStepStatus` + `WorkflowRunStatus`).** `RunStepStatus` gains `StartedAt`, `EndedAt
   int64`, `Error string`; `WorkflowRunStatus` gains `TraceID string` (the run trace, ADR-0102). `mirror`
   copies the per-step facts (+ `Attempts`) into `status.steps[]` and `rec.TraceID` into `status.traceId`.
4. **`describe` (cmd/funcdctl).** `describe`'s **default output changes** from the current raw-JSON dump to a
   **rendered** troubleshooting view — a readable per-step section (`name · phase · attempts · duration
   (endedAt−startedAt) · error`) plus the run `trace: <id>` line and a static **hint** line: `full logs:
   funcdctl workflow logs <run>`. A `-o json` flag preserves the raw object (the prior default). The hint is
   **plain informational text** — it invokes nothing, so it degrades gracefully; the `funcdctl workflow logs`
   command it names is delivered by the **sibling ADR-0106 in this same batch**, which lands with/right after
   this ADR (see *Sequencing*).

## Temporary workarounds

- **History is retention-bounded.** The record lives on `status` and is swept with the run (ADR-0094
  retention). Fine for troubleshooting a recent run; durable audit is the named follow-on.
- **No input/output in V1.** `describe` shows *why/where/how-long*, not *what data*. The data-lineage
  increment (by-ref input/output) is the documented next step.
- **Sequencing with ADR-0106.** The `describe` hint points at `funcdctl workflow logs <run>`, delivered by
  the sibling **ADR-0106 in this same batch** — drafted + linked here, landing with/right after this ADR.
  The hint is plain text (invokes nothing), so it degrades if 0106 ever slips. *Exit*: none needed.
- **Full-error location.** A dispatched **function** step's full throw is captured in its span (`StatusMsg`,
  ADR-0101) + its logs (F51), reachable via the pointer. A **builtin** (`wait`/`pass`) failure has no shim
  span, so a builtin error longer than `maxStatusError` is truncated *everywhere* — acceptable, since those
  messages (wait timeout, transform error) are short. *Exit*: set the run-span `StatusMsg` from the terminal cause.
- **`attempts` counts dispatch attempts.** Only function-step dispatch retries increment it; a single-shot
  `builtin`/sub-workflow step shows its natural count (it runs once). Not a defect — the field is the retry count.

## Contracts

### Run record + status (additive)

```go
// runstate.StepState gains (ADR-0100):
StartedAt int64  `json:"startedAt,omitempty"`
EndedAt   int64  `json:"endedAt,omitempty"`
Error     string `json:"error,omitempty"` // the raw step-level failure cause, CAPPED (Failed steps)

// v1.RunStepStatus gains the same three (mirrored to WorkflowRun.status.steps[]); Attempts already
// exists and is now actually populated.
// v1.WorkflowRunStatus gains: TraceID string `json:"traceId,omitempty"` // the run's trace (ADR-0102),
// mirrored from runstate.Record.TraceID so describe shows it + funcdctl workflow logs (ADR-0106) uses it.
```

`stepNode` (engine-internal) gains `startedAt`, `endedAt int64`, `attempts int`, `errMsg string`.

### Engine (internal/workflow)

```go
const maxStatusError = 512 // cap the error string mirrored to status (first line preferred); full text in logs
func capErr(s string) string                            // first line, truncated to maxStatusError + "…"
func (e *Engine) markSucceeded(n *stepNode)              // Succeeded + endedAt
func (e *Engine) markFailed(n *stepNode, cause error)    // Failed + endedAt + errMsg = capErr(cause) (raw step cause)
// dispatchStep records n.attempts; persist writes StartedAt/EndedAt/Error/Attempts; rebuildState restores
// them for terminal steps; mirror copies them + rec.TraceID into status.
```

### CLI

`funcdctl workflow describe <run>` gains a per-step troubleshooting render (phase · attempts · duration ·
error), the run `trace: <id>` line, and a `full logs: funcdctl workflow logs <run>` pointer (ADR-0106).
`-o json` exposes the raw fields.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0094 engine (step transitions, run-state, Resume) + `describe` | per-step `startedAt`/`endedAt`/`error` + populated `attempts` in `status.steps[]` |
| the ADR-0098 `status.steps[]` seam (record beside the cached contract) | a troubleshooting `describe` render |
| `clock.Clock` (already on the engine) | no new resource kind, no new dependency, no new store |

## Implementation plan

Files: `internal/workflow/state.go` (`stepNode` lineage fields), `internal/workflow/engine.go`
(`maxStatusError`/`capErr`, `markSucceeded`/`markFailed`, `startedAt` at Running, `n.attempts` in
`dispatchStep`, `persist` writes them), `internal/workflow/engine.go`/`state.go` (`rebuildState` restores
terminal-step lineage), `internal/workflow/runstate/runstate.go` (`StepState` fields),
`api/types/v1alpha1/workflowrun.go` (`RunStepStatus` fields + `WorkflowRunStatus.TraceID`),
`internal/workflow/reconcile_run.go` (`mirror` copies the per-step facts + `rec.TraceID`),
`cmd/funcdctl/workflow.go` (the `describe` render + the `trace:` line + the `funcdctl workflow logs <run>`
pointer). No `go.mod` change.

Test plan — one named test per Scenario (engine records over the fake dispatcher: `failed-step-records-
cause`, `attempts-recorded`, `timings-recorded`, `error-is-capped` (a long cause → `status` error ≤
`maxStatusError`), `run-traceid-in-status` (`mirror` copies `rec.TraceID` to `status.traceId`),
`resume-keeps-history`, `subworkflow-step-recorded`; a `describe`-render unit for
`describe-surfaces-troubleshooting` incl. the trace line + logs pointer). Definition of done: all scenario
tests green; `go build/lint/test/mod` green; the existing engine/e2e suites still pass; feat F67 row
advanced; no leak. (OpenAPI regenerates — additive `status` fields.)

## Review checklist

- [ ] A failed step records the **raw step-level** cause (not the run-level wrap); `describe` names the
      step + reason.
- [ ] `attempts` is populated (was silently 0); a retried step shows its count.
- [ ] Every step (dispatch / builtin / sub-workflow) records `startedAt`/`endedAt`; duration is derivable.
- [ ] `Resume` restores terminal steps' timings/attempts/error; an in-flight step re-runs with a fresh
      record.
- [ ] `funcdctl workflow describe` renders a readable per-step troubleshooting line + the run `trace:` +
      the `funcdctl workflow logs <run>` hint; `-o json` returns the raw object (the prior default).
- [ ] The mirrored `error` is **capped** (≤ `maxStatusError`); only the small facts mirror to `status` (no
      full payloads); the run `trace_id` is mirrored to `status`.
- [ ] Additive on the **status schema**: `StepState`/`RunStepStatus`/`WorkflowRunStatus` gain `omitempty`
      fields; OpenAPI regen. (Note: `describe`'s **default CLI output** changes from raw JSON to a rendered
      view + a new `-o json` flag — a deliberate CLI change, not a status-contract change.)

## Consequences

- **`describe` becomes a real troubleshooting tool** — which step failed, why, how long, how many tries —
  without grepping logs. Immediate, standalone value.
- **A latent bug is fixed** — `attempts` is finally populated.
- **`describe` bridges to the trace** — it shows the run `trace_id` and points at `funcdctl workflow logs
  <run>` (ADR-0106), so the capped one-liner leads to the full error + all a run's logs (one-run-one-trace,
  ADR-0102–0105, is built). The per-step record is also the substrate the remaining follow-ons (data
  lineage, durable audit, F71 replay) build on; V1 keeps only the troubleshooting facts.
- **Bounded + no new store** — small fields on the existing `status`; history is retention-scoped by
  design (durable audit is the named next step).

## Open questions

- **Duration formatting** — `describe` renders `endedAt−startedAt`; human units (ms/s) is a display
  detail.
- **Skipped/cancelled timings** — a Skipped step has no run; it records phase only (no start/end). A
  cancelled in-flight step records `startedAt` + the cancel as its terminal note.

## References

- [ADR-0094](0094-workflow-engine-core.md) — the engine, run-state, `describe`, retention.
- [ADR-0098](0098-typed-workflow-edges.md) — the `status.steps[]` seam this records beside.
- [ADR-0099](0099-sub-workflows.md) — sub-workflow steps get the same record.
- FEAT-0005/F67 (lineage & observability); F71 (replay) and the data-lineage / durable-audit
  follow-ons build on these records.
- [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)–[ADR-0105](0105-nested-dag-span-parenting.md) — one-run-one-trace (now **built**): the run `trace_id` this mirrors + the full DAG-shaped trace `describe` points at.
- [ADR-0081](0081-function-log-capture-side-channel-blob.md)/[ADR-0084](0084-funclog-read-funcdctl-logs.md) — funclog capture + read; [ADR-0106](0106-run-scoped-log-read.md) extends the read to run-scoped (the `funcdctl workflow logs` this points to).
