# ADR-0096 implementation review — Workflow step model + engine-native built-ins (blocking `wait`/`pass`)

**Verdict**: **pass** — the reshaped three-kind step model and the two engine-native built-ins are
implemented conformantly, every scenario has a named passing test, and the four sub-checks are green.
**Model**: claude-fable-5 · **Phase**: implementation · **ADR status**: Reviewing → Implemented
**Reviewed against**: ADR-0096 Contracts / Scenarios / Review checklist / DoD · [ADR-0094](../adr/0094-workflow-engine-core.md)
(orchestration it reshapes) · [ADR-0095](../adr/0095-reference-engine-typed-paths-predicates.md) (the goja evaluator it extends) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md) conventions.

> **Design correction mid-implementation (recorded).** The ADR was self-accepted in this batch with a
> **yield/park** execution model for `wait` (a special `StepWaiting` phase, a persisted `wakeAt`, a
> `RequeueAfter` timer). The decider then directed that a `wait` be a **plain blocking step — no special
> workflow state**. The ADR was revised in place (still pre-`Implemented`; the acceptance note records the
> revision) and the yield-model code was fully reverted. This review is of the **blocking** implementation.
> The one honest nuance surfaced to the decider: Temporal's own timers are durable/non-blocking (the yield
> model); blocking is the simpler V1 choice and the ADR's *Out of scope* documents the durable-timer model
> as the exit for long/scheduled waits.

## Verification (run, not eyeballed)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Lint | `go tool golangci-lint run ./...` | **0 issues** |
| Tests | `go test ./internal/workflow/... ./internal/expr/ ./api/... ./pkg/funcd/ -count=1` | exit 0 (7 packages ok) |
| Modules | `go mod verify` | all modules verified |

`TestPythonPoolSmoke` (internal/testkit/bench) fails on Python-shim readiness — a pre-existing
**environmental** failure unrelated to this change (no workflow/expr/type code path); not attributed.

## Scenario → test (all named, un-skipped, passing)

| Scenario | Test |
|---|---|
| `function-image-materializes` | `TestMaterializeOwnedFunctionsAndKV` (materializes `orders-ingest` w/ runtime+artifact) + `TestScenarioWorkflowEndToEnd` |
| `function-ref-dispatches` | `TestFunctionRefDispatches` (`stepTarget` → the ref; image → `<workflow>-<step>`) |
| `dispatch-knobs-on-function-only` | shape-enforced (`BuiltinStep` has no retry/timeout/bindings fields — won't compile) + `retryStep` sets `s.Function.Retry` |
| `step-kind-union-validated` | `TestWorkflowKindUnion` (top-level union + `function{image\|ref}` + `builtin{wait\|pass}` + `workflow` reserved) |
| `wait-blocks-then-continues` | `TestWaitBlocksThenContinues` (blocks ~80ms, no dispatch, output = flowing input) |
| `wait-duration-from-expression` | `TestWaitDurationFromExpression` (`${{ input.secs }}` → ~50ms) |
| `wait-counts-toward-run-timeout` | `TestWaitCountsTowardRunTimeout` (`timeout 60ms` + `wait 5s` → RunTimedOut at ~60ms) |
| `pass-transforms-in-engine` | `TestPassTransformsInEngine` (object literal, no dispatch) + `TestSelectObjectLiteral` (expr) |
| `pass-selects-parent-output` | `TestPassSelectsParentOutput` |
| `builtin-reconcile-check-rejects-bad-expression` | `TestBuiltinRejectsBadExpression` |

Plus `internal/expr` `TestSelectObjectLiteral` — Select admits object literals (checked values, unknown
field rejected, empty rejected), **Condition rejects** — the additive ADR-0095 case.

## Review checklist (ADR-0096)

- [x] `WorkflowStep` is three kind-keyed structs; exactly one of `function|builtin|workflow`; `function`
      exactly one of `image|ref`; `builtin` exactly one of `wait|pass`; `workflow` rejected. (`validateKind`, `TestWorkflowKindUnion`)
- [x] Dispatch knobs live on `FunctionStep` only; a `builtin` cannot express them (shape-enforced).
- [x] Function-image materialization + function-ref dispatch behave as ADR-0094, new field paths; the
      materializer reads `st.Function.Image` (`reconcile_workflow.go`, `TestMaterialize*`).
- [x] `wait` blocks in-engine then Succeeds (output = flowing input); no container dispatched; the
      post-wait step doesn't run before the delay (`runBuiltin`, `TestWaitBlocksThenContinues`).
- [x] `wait` accepts a duration string and a Select expression (→ seconds); it blocks on the run context
      so the paused-excluding run-timeout interrupts it (`TestWaitCountsTowardRunTimeout`).
- [x] **No new step phase or run-state field** — `StepPhase`, `RunStepStatus`, `StepState` are byte-for-byte
      back to their ADR-0094 committed state (verified: `git diff` shows no change to `workflowrun.go`/
      `runstate.go`); a `wait` is `Running` then `Succeeded`; pause/cancel are ADR-0094's declarative path.
- [x] `pass` evaluates a Select expression to its output, no dispatch; object literals Select-only.
- [x] `wait`/`pass` bad expression fails the run (`TestBuiltinRejectsBadExpression`).
- [x] ADR-0094's step contract back-linked as superseded, orchestration untouched; example + venom lane
      reworked to the new shape with a `wait`/`pass` case.

## Strengths — keep as-is

- **The yield-model revert is clean.** `workflowrun.go`, `runstate.go`, `state.go`, `reconcile_run.go`
  return to their exact committed ADR-0094 bytes — no orphaned `StepWaiting`/`wakeAt`/`requeueForWait`
  residue. The blocking model touches only what it must.
- **`wait` blocks on `ctx`**, so the run-timeout genuinely interrupts a long wait (the test asserts it
  fails at ~60ms, not after 5s) — the "wait counts toward the timeout" guarantee is real, not documented.
- **Type-enforced kind rules**: dispatch knobs live only on `FunctionStep`, so "a builtin has no retry"
  is a compile fact. The `functionOf(st)` helper localizes the field-path migration cleanly.
- **The object-literal extension is genuinely additive**: `Parse` wraps a leading-`{` in parens (the
  `() => ({…})` idiom) so a bare `${{ {a: 1} }}` parses; `checkObjectLiteral` mirrors `checkArrayLiteral`
  and is Select-only. ADR-0095's Condition behavior is untouched (test asserts Condition still rejects).

## Findings

### Blockers
None.

### Major
None.

### Minor
- `dispatch-knobs-on-function-only` has no single dedicated test — it is enforced structurally (the field
  simply isn't on `BuiltinStep`) and exercised via `retryStep`/`TestWorkflowKindUnion`. Adequate, since a
  compile-time guarantee can't regress; noted for completeness. (attribution: **model**, non-blocking)

### Nits
- The blocking `wait` holds a driver goroutine for its duration and can't be paused/cancelled mid-wait —
  correctly recorded in the ADR's *Temporary workarounds* + *Open questions* with the durable-timer exit.
  Taste, not a defect (the decider chose this).

## Recommendation

**pass** → stamp ADR-0096 `Reviewing → Implemented`; the F64 row is already `implemented` (ADR-0094) and
ADR-0096 is tracked by its own lifecycle (parenthetical → implemented). No `adr`-attributed defects, no
Blockers/Majors; the one Minor is a compile-time guarantee that needs no extra test.
