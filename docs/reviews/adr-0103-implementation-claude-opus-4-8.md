# Review — ADR-0103 implementation (engine-emitted run-root span, F67)

- **Gate**: ADR-0000 review gate (#5) — implementation review
- **ADR**: [0103](../adr/0103-engine-emitted-run-root-span.md) · **Realizes** FEAT-0005/F67
- **Model under review**: claude-opus-4-8
- **Verdict**: **pass** — Definition of Done met, no Blockers, no Majors.
- **Observed at**: ADR status `Accepted` (the `adr-impl` `Accepted → Reviewing` handoff bump was
  not applied). Treated as implementation-complete per the review brief; stamped straight to
  `Implemented` on this pass, noting the missed intermediate bump.

## Verification (evidence, all `nix develop -c`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go tool golangci-lint run ./internal/workflow/... ./pkg/funcd/...` | **0 issues** (macOS `ld: warning` filtered) |
| `go mod verify` | `all modules verified` · `git diff go.mod` empty (funclog is internal — no module) |
| `go test ./internal/workflow/...` | ok — workflow + runstate/badger pass |
| `TestRunSpan*` (the 6 new tests, `-v`) | OnSuccess / OnFailure / OnCancel / OnContractReject / Once / NoSink — **all PASS** |
| `go test ./pkg/funcd/ -run 'E2EWorkflowOneRunOneTrace\|E2EFunclogCaptures'` | ok 5.2s — `TestScenarioE2EWorkflowOneRunOneTrace` + the two ADR-0101 funclog e2es all PASS |
| Venom containerd workflow lane XML | fresh (`test_results_workflow.venom.xml`, today) — includes `run-root-span-…-INTERNAL-run-span-…-ADR-0103` testcase, no failure/error tags |

## Seam is intentional + acyclic (checklist item 6)

- `grep internal/funclog internal/workflow/*.go` → only `reconcile_run.go` (+ the test). The single
  bounded seam ADR-0102 deferred.
- `internal/funclog` does **not** import `internal/workflow` — no cycle.
- The ADR's Consequences (−) and DoD line both openly state this import supersedes ADR-0102's
  "no-funclog-import" checklist item. Intentional and documented, not a regression.

## Exactly-once correctness (the design crux — checklist item 3)

- **No double-emit**: `Reconcile` short-circuits at the top on `isRunTerminal(run.Status.Phase)`
  (reconcile_run.go:77), so a run whose terminal status is persisted never re-drives → `emitRunSpan`
  is never reached again. `TestRunSpanOnce` proves it (two reconciles → 1 span).
- **Both emit sites covered**: the after-drive site (`Reconcile`, :117) by Success/Failure/
  ContractReject; the distinct `cancelRun` site (:141) by OnCancel.
- **Defensive no-op**: `emitRunSpan` guards `traces == nil || rec == nil || !rec.Terminal() ||
  rec.TraceID == ""` (:44) — nil sink (`TestRunSpanNoSink`), typed-nil avoided at the wiring (nil
  *interface*, funcd.go:490/500), non-terminal, and no-trace-context all handled.
- The at-most-once miss-window (crash between status-persist and emit) is openly accepted in the ADR;
  the equal `SpanID` is idempotent belt-and-suspenders regardless.

## Contracts & conventions

- `NewRunReconciler(store, engine, funclog.TraceSink, logger)` matches the ADR contract; the four
  existing `reconcile_run_test.go` call sites updated to the new 4-arg signature.
- `emitRunSpan(ctx, rec)` builds the span exactly as specified: `SpanID = rec.RootSpanID`,
  `TraceID = rec.TraceID`, `ParentID = ""`, `Name = workflow`, `Kind = SpanInternal`, Start/End from
  `StartedAt`/`UpdatedAt`, `Status = OK` iff `Succeeded` else `ERROR`,
  `Resource{Function: workflow, Replica: run}`.
- ADR-0002: ctx-first (`emitRunSpan(ctx, rec)`), typed (no `any`), `api/fault` wrapping preserved,
  slog via `WarnContext` (best-effort, never fails the reconcile).
- **One shared sink** (checklist item 5): `pkg/funcd` builds one `BlobTraceSink`, hoisted above the
  workflow wiring, passed to `NewRunReconciler` and **reused** in the F51 capture block
  (`Sinks{Logs, Traces: traceSink}`) — no second sink. Close ownership unchanged (one owner, sealed
  once before `blob.Close()` at shutdown, funcd.go:781).

## Scenario → test coverage (8/8)

| Scenario | Named test |
|---|---|
| run-span-on-success | `TestRunSpanOnSuccess` ✅ |
| run-span-on-failure | `TestRunSpanOnFailure` ✅ |
| run-span-on-cancel | `TestRunSpanOnCancel` ✅ |
| run-span-on-contract-reject | `TestRunSpanOnContractReject` ✅ (zero step dispatch asserted) |
| run-span-parents-steps | `TestScenarioE2EWorkflowOneRunOneTrace` (step SERVER spans parent on the INTERNAL root's SpanID) ✅ |
| run-span-once | `TestRunSpanOnce` ✅ |
| no-sink-no-span | `TestRunSpanNoSink` ✅ |
| e2e-run-root-span | in-process e2e + the Venom `…run-span…ADR-0103` testcase (kind 1 under `traces/default/orders/`) ✅ |

## Review-checklist (ADR-0103) — 7/7 hold

All seven items verified: exactly-one INTERNAL span with the right fields; steps parent on the root;
once-only; nil-safe/additive; one shared sink; bounded funclog import + no go.mod change + best-effort
(never fails reconcile); sub-workflow child runs keep per-run scope (each run's reconcile emits its own
root — unchanged).

## ✅ Verified correct — keep it

- The nil-*interface* discipline (`var traceSink funclog.TraceSink`, assigned only when built) — the
  exact trap the ADR flagged; done right, so `traces == nil` holds and no `AppendSpan` panic.
- Sink reuse instead of a second construction — one coherent trace store, one Close owner.
- The e2e assertion genuinely proves the new invariant: exactly one trace, one INTERNAL no-parent
  root, ≥2 SERVER step spans, and **all** of them parent on the root's SpanID.
- ADR substance untouched (only the header status line), so the immutability invariant holds.

## Findings

None scored. **Blockers: 0 · Majors: 0 · Minors: 0.**

Non-scored observation (**env**, not a code defect, not attributed to the model): the working tree
diff also touches `internal/runtime/embedimg/python314.tar` (a ~200-byte binary delta) — a byproduct
of the embedded-runtime image rebuild during the Venom containerd lane, unrelated to the ADR-0103
source change. No action for the builder.

## Recommendation

**Pass.** Advance ADR-0103 `Accepted → Implemented` (noting the skipped `Reviewing` bump) and set
F67's ADR column to `ADR-0103 implemented`.
