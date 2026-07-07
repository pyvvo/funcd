# ADR-0104 implementation review — cross-sub-workflow trace linking (one composition = one trace)

- **ADR**: [ADR-0104](../adr/0104-cross-subworkflow-trace-linking.md) — F67 (extending one-run-one-trace to
  one-composition-one-trace across inline sub-workflow boundaries)
- **Phase**: implementation · **Model**: claude-opus-4-8 · **Reviewer gate**: ADR-0000 gate #5
- **Verdict**: **pass** — DoD met, no Blockers/Majors/Minors.
- **Observed at**: ADR status `Accepted` (the `adr-impl → Reviewing` bump was not applied); treated as
  implementation-complete and stamped straight to `Implemented` on this pass.

## Verification (evidence, not opinion)

All commands via `nix develop -c`. macOS `ld: warning … newer 'macOS' version` lines ignored per the run book.

| Check | Result |
|---|---|
| `go build ./...` | exit **0** |
| `go tool golangci-lint run ./internal/workflow/... ./pkg/funcd/...` | **0 issues** |
| `go mod verify` | `all modules verified`; `git diff go.mod` **empty** (funclog is internal — no module added) |
| `go test ./internal/workflow/...` (fresh, `-count=1`) | **ok** — main pkg 0.755s, badger 0.528s |
| 4 new ADR-0104 tests (`-v -count=1`) | `TestCompositionIsOneTrace`, `TestFailedChildEmitsErrorSpan`, `TestNestedDepthOneTrace`, `TestTopLevelHasNoRootParent` — all **PASS** |
| ADR-0103 run-root regression (6) | `TestRunSpanOn{Success,Failure,Cancel,ContractReject,Once}`, `TestRunSpanNoSink` — all **PASS** (exercise the refactored shared `buildRunSpan`/`emitRunSpan`) |
| `go test ./pkg/funcd/ -run 'E2ECompositionOneTrace\|E2EWorkflowOneRunOneTrace\|E2EFunclogCaptures' -count=1` | **ok** 6.214s — `TestScenarioE2ECompositionOneTrace` PASS + the ADR-0101/0102/0103 e2es still PASS |
| Venom workflow lane (`test_results_workflow.venom.xml`, fresh) | **10 tests, 0 failures, 0 errors** — includes `one-composition-one-trace … pipeline parent and scorer child runs share one traceId (ADR-0104)` |

## Scenario → test coverage (every scenario has a named passing test)

| ADR scenario | Test | Result |
|---|---|---|
| child-inherits-parent-trace | `TestCompositionIsOneTrace` (child `score` dispatch `TraceID == parent`) | PASS |
| child-run-nests-under-parent | `TestCompositionIsOneTrace` (child span `ParentID == parent RootSpanID`, own SpanID) | PASS |
| child-run-root-emitted | `TestCompositionIsOneTrace` (1 INTERNAL child run span from the engine) | PASS |
| failed-child-emits-error-span | `TestFailedChildEmitsErrorSpan` (failing child → 1 INTERNAL **ERROR** span) | PASS |
| top-level-unchanged | `TestTopLevelHasNoRootParent` + `TestRunSpanOnSuccess` (`RootParentID==""`, span `ParentID==""`) | PASS |
| nested-depth | `TestNestedDepthOneTrace` (grandchild inherits top trace; 2 child spans one trace) | PASS |
| e2e-one-composition-one-trace | `TestScenarioE2ECompositionOneTrace` + Venom lane | PASS |

## Refactor safety (the crux) — verified

- **`emitRunSpan` method → package func is behavior-preserving.** `reconcile_run.go` now exposes a pure
  `buildRunSpan(rec) (Resource, Span)` + a package `emitRunSpan(ctx, sink, rec, log)`; both reconciler sites
  (`Reconcile` and `cancelRun`) call the package func. `TestRunSpanOnSuccess` asserts a top-level run's root span
  keeps `ParentID == ""` (span builder sets `ParentID = rec.RootParentID`, which is `""` for a top-level run) —
  the ADR-0103 contract is intact.
- **No double-emit.** A sub-workflow child run is a `runstate.Record` produced inline by `runChild`, never a
  `WorkflowRun` CRD — the reconciler never sees it, so top-level ⇒ reconciler, child ⇒ engine, one shared builder.
  Confirmed by architecture + `TestCompositionIsOneTrace` emitting exactly one (child) span in the engine path.
- **No import cycle.** `internal/funclog` is imported by `engine.go` + `reconcile_run.go` (and the tests); a
  reverse grep confirms `funclog` does not import `internal/workflow`. `go.mod` unchanged (funclog is internal).
- **Failed-child emission ordering.** In `subworkflow.go`, `emitRunSpan(ctx, e.traces, rec, e.log)` is placed
  **before** the `if err != nil { return }`, so a business-failed child (terminal `rec`, non-nil `err`) still
  emits its ERROR span; an infra error (`rec == nil`) is skipped by the guard. `TestFailedChildEmitsErrorSpan`
  proves the ERROR span survives the failing return.

## Contracts — match

- `runstate.Record.RootParentID string` (`omitempty`, "" ⇒ trace root) — present.
- `execute(…, depth int, inheritTraceID, inheritRootParent string)`; public `Execute` passes `"",""`;
  `runChild` passes `parent.TraceID`/`parent.RootSpanID` — present and correct (inherit rule at engine.go:155-160).
- `Deps.Traces funclog.TraceSink` + `Engine.traces`; `New` wires it; nil-safe — present.
- `buildRunSpan(rec) (funclog.Resource, funclog.Span)` with `ParentID = rec.RootParentID` — present.
- Wiring: `pkg/funcd` passes the shared `traceSink` into both `workflow.New(Deps{Traces: …})` and
  `NewRunReconciler(…, traceSink, …)` — present (funcd.go:527-542).

## Review checklist / DoD — 6/6 hold

Child dispatch carries the parent's `TraceID`; child run span `ParentID` = parent `RootSpanID` (top-level `""`);
engine emits the child INTERNAL run span; no double-emit (one shared builder); `RootParentID` flows to arbitrary
depth; top-level + no-sub-workflow runs unchanged, additive, `go.mod` unchanged, nil sink ⇒ no span, sink error
never fails the run (best-effort, logged). ADR-0002 conventions respected (ctx-first, `api/fault`, slog-only, no
`any` in the surface, one-file shape). ADR substance is unchanged from the accepted document.

## ✅ Verified correct — keep it

- The inline/reconciler emission split maps cleanly onto engine-vs-reconciler; correct-by-construction, no flag.
- `buildRunSpan` extracted as a pure builder so both reconciler call sites are untouched — minimal, low-risk refactor.
- The failed-child emit ordering (the folded Major) is exactly right and has a dedicated proving test.
- Coverage is thorough: unit (capturing fake sink) + in-process shim e2e + a real containerd Venom testcase.

## Findings

None. No Blocker / Major / Minor. No identity or absolute-path leak in any tracked changed file.

## Recommendation

**Pass.** Stamp ADR-0104 `Implemented`; advance the F67 ADR-column `ADR-0104 accepted → implemented`.
</content>
