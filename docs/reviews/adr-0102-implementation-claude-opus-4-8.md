# ADR-0102 implementation review — one run = one trace (dispatch trace-context propagation)

- **ADR**: [0102](../adr/0102-one-run-one-trace-dispatch-propagation.md) — "One run = one trace — run-scoped W3C trace-context propagation on step dispatch" (Realizes FEAT-0005/F67, the one-run-one-trace increment)
- **Phase**: implementation · **Gate**: ADR-0000 #5 (review the *work*)
- **Model reviewed**: claude-opus-4-8
- **Verdict**: **pass** (DoD met; no Blockers, no Majors; 1 Minor)
- **Observed status on entry**: ADR at `Accepted` (the `adr-impl` `Accepted → Reviewing` handoff bump was not applied); treated as implementation-complete and stamped straight to `Implemented` on this pass.

## Verification (run, not eyeballed)

All via `nix develop -c` (ld "newer macOS" linker warnings ignored per house rule).

| Check | Result |
|---|---|
| `go build ./...` | exit **0** |
| `go tool golangci-lint run ./internal/workflow/... ./pkg/funcd/...` | **0 issues**, exit 0 |
| `go mod verify` | `all modules verified` |
| `git diff go.mod` | **empty** — no new dependency (crypto/rand is stdlib), as the ADR requires |
| `go test ./internal/workflow/...` | **ok** (engine + runstate/badger) — 4 new trace tests pass, no regression |
| 4 named trace tests (`-v`) | `TestRunMintsAndPropagatesTraceContext`, `TestRetriesShareTrace`, `TestResumeKeepsTrace`, `TestDispatchSetsTraceparentHeader` — all **PASS** |
| `go test ./pkg/funcd/ -run 'E2EWorkflowOneRunOneTrace\|E2EFunclogCaptures' -count=1` | **ok** (5.36s): `TestScenarioE2EWorkflowOneRunOneTrace` PASS, plus `…FunclogCapturesBurst`/`…FunclogCapturesSpans` (ADR-0101) PASS — no regression from the `dispatchStep` signature change |
| Import-graph guard: `grep -rn "internal/funclog" internal/workflow/` | **empty** — the engine sets a header string, never importing funclog |
| Identity / abs-path leak grep on changed tracked files | clean |

**Venom containerd lane** (evidence, not run by this gate): `test_results_workflow.venom.xml` is **fresh** (today) — `testsuite tests="8"`, **no `<failure>`/`<error>` elements**. It includes the new testcase
`one-run-one-trace-a-runs-ingest-and-score-step-spans-share-one-traceId-ADR-0102` (12.5s). The in-process `TestScenarioE2EWorkflowOneRunOneTrace` is the same assertion over the shim platform; both green.

## Scenarios → tests (every Scenario has a named passing test)

| Scenario | Test | Status |
|---|---|---|
| run-mints-trace-context | `TestRunMintsAndPropagatesTraceContext` (asserts hex32 trace + hex16 root on the record) | ✅ |
| steps-share-one-trace | `TestRunMintsAndPropagatesTraceContext` (all 3 dispatches carry `rec.TraceID`) | ✅ |
| steps-parent-on-run-root | `TestRunMintsAndPropagatesTraceContext` + `TestDispatchSetsTraceparentHeader` (ParentSpanID = run root; wire `00-…-…-01`) | ✅ |
| retries-share-trace | `TestRetriesShareTrace` (attempt 1 + 2 share the run trace) | ✅ |
| resume-keeps-trace | `TestResumeKeepsTrace` (persisted context reused, never re-minted) | ✅ |
| no-context-no-header | `TestDispatchSetsTraceparentHeader` (empty TraceID → no header) | ✅ |
| e2e-one-run-one-trace | `TestScenarioE2EWorkflowOneRunOneTrace` (real 2-step run → all step spans in blob one trace-id, one root) + Venom lane | ✅ |

## Contracts — match

- `runstate.Record` gains `TraceID string`/`RootSpanID string` (`json:"...,omitempty"`), documented as minted-at-start / reused-by-Resume. ✅
- `DispatchRequest` gains `TraceID`/`ParentSpanID`; `dispatchStep` now takes `rec` and threads `rec.TraceID`/`rec.RootSpanID` into the retry-loop `DispatchRequest`, **and** the onFailure dispatch in `fail()` carries them. Both dispatch paths covered. ✅
- Wire: `dispatch.go` sets `traceparent: 00-<TraceID>-<ParentSpanID>-01` only when `TraceID != ""`. ✅
- Minted via `crypto/rand` → hex32/hex16; empty on rand error (run proceeds, never fails). ✅

## Review checklist — 7/7 hold

Fresh-run mint + Resume-reuse ✅ · traceparent set / empty→none ✅ · all steps + retries share trace-id, parent on root ✅ · header-string only, no funclog import ✅ · only the function-dispatch path (builtins `wait`/`pass` run in-engine, untouched) ✅ · additive, no go.mod change ✅ · e2e (Go + Venom) share one trace-id ✅.

## Findings

### Minor — `mintTraceContext` signature omits the declared `err` return · attribution: model

The ADR Contracts block declares:

```go
func mintTraceContext() (traceID, rootSpanID string, err error)
```

The implementation is `func mintTraceContext() (traceID, rootSpanID string)` — no `err` return; on a `crypto/rand` failure it returns `"", ""` internally (`engine.go:22`). This **exactly satisfies** the ADR's folded error-path decision ("proceed with an empty context, never fail the run") and is arguably cleaner — the caller has no error to thread and unconditionally proceeds — but it is a literal deviation from the signature written in the Contracts section. Non-blocking: behavior conforms to the Decision text and the empty-context path is exercised by `TestDispatchSetsTraceparentHeader`. No action required to ship; noted for signature fidelity.

## ✅ Verified correct — keep it

- **Both dispatch paths carry the context** — the retry-loop `DispatchRequest` *and* the onFailure handler dispatch in `fail()`. A common miss (header only on the happy path) is avoided; the onFailure handler joins the run's trace too.
- **Resume reuses, never re-mints** — `execute` mints only on the fresh record; `Resume` loads `rec` and drives, leaving `TraceID`/`RootSpanID` untouched. `TestResumeKeepsTrace` pins this with a known context and asserts the re-dispatched step carries it.
- **Import graph stays clean** — the engine emits a header string; `grep` confirms no `internal/funclog` import in `internal/workflow`. The V1 "no engine-emitted root span" scope line is honored.
- **Truly additive** — `omitempty` fields, empty-context → unchanged dispatch, no `go.mod` change; a legacy record without a context dispatches exactly as before.
- **Real end-to-end proof** — the in-process e2e runs a genuine 2-step Node workflow and reads the F51 spans back out of blob, asserting `len(traceIDs) == 1 && len(parents) == 1` over ≥2 spans; the containerd Venom lane asserts the same on real containerd.

## Recommendation

**Pass.** The implementation realizes ADR-0102 faithfully: one W3C trace context minted per run, persisted, reused across retries and Resume, propagated as a standard `traceparent` on every function-step dispatch (and the onFailure handler), additive and import-graph-clean, with a named passing test per Scenario and a green real e2e. The single Minor (a helper signature that drops the declared `err` in favor of the ADR's own empty-context error behavior) is cosmetic and does not warrant a rework loop. Stamped `Implemented`.
