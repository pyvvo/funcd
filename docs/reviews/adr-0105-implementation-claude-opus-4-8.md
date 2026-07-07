# ADR-0105 implementation review — nested DAG span parenting

## Verdict: pass — 0 blockers, 0 majors  (ADR-0105 implementation, model: claude-opus-4-8)

The largest F67 increment (engine + dispatch + funclog + **both** shims) lands complete and green.
Every ADR Scenario has a named, un-skipped, passing test; the Contracts match to the letter; the
extension is additive (a header-absent invoke still mints, ADR-0101 unchanged); no regression in the
ADR-0101/0102/0103/0104 trace suites. Verification was **run**, not eyeballed — exit codes and test
counts below.

> Observed at status `Accepted` (the `adr-impl` `Accepted → Reviewing` bump was not applied). Treated
> as implementation-complete per the review brief and stamped straight to `Implemented` on this pass.

### Verification evidence (all `nix develop -c`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go tool golangci-lint run ./internal/... ./pkg/funcd/...` | **0 issues**, exit 0 |
| `go mod verify` | `all modules verified`; `git diff go.mod` empty |
| `go test ./internal/workflow/... ./internal/funclog/...` | ok (all packages, incl. new DAG + funclog links tests) |
| Named engine tests (`-v`) | `TestStepParentsOnPredecessor`, `TestFanInLinksNonPrimary`, `TestRetriesShareStepSpan`, `TestResumeKeepsSpanIDs`, `TestRunMintsAndPropagatesTraceContext` — all PASS |
| `go test ./pkg/funcd/ -run 'E2EDagShapedWaterfall\|E2ECompositionOneTrace\|E2EWorkflowOneRunOneTrace\|E2EFunclogCaptures' -count=1` | ok — DAG e2e + 0101/0102/0103/0104 e2es all PASS |
| Node `npm test` | **39 pass / 0 fail** (incl. `engine-owns-span-id`, `direct-invoke-unchanged`) |
| Python `ruff` / `mypy` / `pytest` | ruff clean · mypy no issues (20 files) · **58 passed, 1 deselected** (the excluded macOS AF_UNIX flake) |
| Venom containerd workflow lane | `test_results_workflow.venom.xml` **fresh** (~90 s), **11 tests, 0 failures/0 errors**, incl. `nested-DAG-parenting-the-score-step-span-parents-on-the-ingest-step-span-ADR-0105` |

### ✅ Verified correct (keep it)

- **Contracts match exactly.** `runstate.StepState.SpanID string json:"spanId,omitempty"` (16 hex),
  `stepNode.spanID` (state.go:32), `DispatchRequest.SpanID []`/`Links` (engine.go:83-85),
  `funclog.Span.Links []string` (span.go:65), `mintSpanID()` (engine.go:38), and the two headers
  `X-Funcd-Span-Id` / `X-Funcd-Span-Links` (dispatch.go:139-144).
- **Edge computation is right** (engine.go `dispatchStep`, 466-480): primary parent = first
  `n.dependsOn`'s pre-minted span-id, run root when empty; links = the rest — read post-implicit-chaining,
  so only the first spec step is a true root. Same span-id every attempt (one span per step).
- **The M1 fix is correct** (engine.go `rebuildState`, 250): `n.spanID = s.SpanID` restores
  **unconditionally at the top of the loop, before** the `Running → Pending continue`, so the very
  in-flight step being re-dispatched keeps its id. `TestResumeKeepsSpanIDs` exercises a **mid-flight
  (`Running`)** step and asserts the persisted id is reused *and* the successor's parent edge holds.
- **Additivity holds.** `newInvContext(tp, providedSpanId?)` / `new_inv_context(tp, provided_span_id)`
  use the provided id only when it is a valid hex16, else mint — verified by `direct-invoke-unchanged`
  (Node) and the dispatch header test (`X-Funcd-Span-Id`/`Links` set only when present).
- **Fan-in is same-trace.** `marshalTraceOTLP` emits each link with the span's own `TraceID` +
  the linked `SpanID` (tracesink.go:244-255); `TestSpanLinksMarshal` asserts both links carry the
  same trace-id and the correct span-ids. The DAG e2e proves it end-to-end (b/c parent on a; merge
  parents on b, links c).
- **Both shims + both paths wired.** Direct (`shim.ts`/`shim.py`) and pooled (`pool.ts`/`pool.py`
  host→worker forward) read `x-funcd-span-id`/`x-funcd-span-links`; `parseLinks`/`parse_links`
  validate hex16.
- **e2e assertion update is a strengthening, not a weakening.** `workflow_trace_e2e_test.go` dropped
  the flat-model `all step spans parent on the run root` check (now false under DAG nesting) and
  replaced it with: one trace · a run-root span · ≥2 step spans · the root step parents on the run
  root · **and** a downstream step nests under a predecessor step. Legitimate DAG-aware upgrade.
- **`go.mod` unchanged**; `crypto/rand` stdlib; ptrace links already vendored.

### Definition of Done

6 / 6 ADR Review-checklist items hold (root→run-root & dependent→predecessor parenting; fan-in
first-edge parent + links; engine-provided id used, direct invoke mints; one span-id per step,
persisted + restored on Resume; `Span.Links` → ptrace links same-trace + NDJSON `links`; additive,
0102/0103/0104 unchanged, `go.mod` unchanged). The generic phase DoD (build/lint/test/mod-verify
green, every Scenario a passing test, in-process e2e + Venom lane green, shim bundles rebuilt) also
holds. No misses.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0105 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 6/6. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Nothing loops back to the builder. Stamp ADR-0105 `Implemented` and advance the F67 row's
ADR column `ADR-0105 accepted → ADR-0105 implemented`. The trace arc F67 (0101→0105) is complete: a
composition renders as one trace whose shape is the actual (sub-)workflow DAG.
