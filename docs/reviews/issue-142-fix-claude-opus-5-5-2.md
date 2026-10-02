# Fix review (round 2) — issue #142 (a call to a Failed Function waits the 30 s activation timeout, then a 503)

- **Change**: branch `fix/i142`, commits `ea29232` `fix(activator): answer a call to a Failed function at once, not after the activation timeout` and `2f5406b` `fix(activator): address review of #142`
- **Files**: `internal/activator/activator.go` (+51/-9), `internal/activator/storescaler/storescaler.go` (+6), `internal/activator/storescaler/storescaler_test.go` (+91)
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0016 (activator C2/C3, storescaler edges), ADR-0142 (a ShapeInvalid worker is never restarted), ADR-0002
- **Previous report**: `issue-142-fix-claude-opus-5-5.md` (changes-requested: 1 major, 3 minors)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #142 fix, model: claude-opus-5-5)

Both halves of the issue's root cause are now fixed. `storescaler.ScaleTo` refuses a wake of a Failed
function with `fault.Unavailable` that names the state. `drive` now reads the Function on every poll and
answers all waiters at once when the Function turns Failed during the activation. The round-1 Major
(a function that becomes Failed mid-activation still stalled the whole timeout) is resolved and pinned
by a regression test. One mutant survives, because the `ScaleTo` guard is now covered by the poll check
as well, so no test pins the guard itself. This is a Minor test gap.

### Round-1 findings — resolution

| Round-1 finding | Status | Evidence |
|---|---|---|
| Major 1 — became-Failed-mid-activation still stalls the timeout (model) | **resolved** | `drive` calls `a.failed(ctx, fn)` on each poll; `TestIssue142_FunctionFailedDuringActivationIsAnsweredAtOnce` passes with the fix, fails on reverted code ("did not become ready within 2s"), and fails under mutant M2 |
| Minor 1 — the guard's `target == v1.PhaseDeploying` clause is not pinned (model) | **resolved** | `TestIssue142_ReclaimOfAFailedFunctionStillSucceeds`; mutant M3 (guard on every target) fails it |
| Minor 2 — the literal `"Ready"` in production code (model) | **resolved** | `const condReady v1.ConditionType = "Ready"` in `internal/activator/activator.go`, the same as the neighbouring packages (`internal/function`, `internal/sensor`, `internal/route`, `internal/site`, `internal/eventing`) |
| Minor 3 — ADR-0016 C3 wording (adr) | unchanged, not scored | no ADR edit is needed; the 503 is a genuine activation failure |

### 🟡 Minor 1 — mutant survivor: removing the `ScaleTo` wake guard fails no test  ·  attribution: model

Mutant M1 (delete the whole `if target == v1.PhaseDeploying { … FailedFault … }` block in
`storescaler.go`) passes all three `TestIssue142_…` tests. The reason: production (`pkg/funcd/funcd.go`)
and the test both give the activator a `Store`, so for an already-Failed function `ScaleTo` is a no-op and
the first poll's `a.failed` answers. The only difference is the `op` prefix in the detail, and
`require.Contains` does not check it. The guard still has a purpose: it is the only check when `Deps.Store`
is nil, and it keeps `ScaleTo` from returning success for a wake that cannot happen. Either pin it with a
direct `ScaleTo(ctx, ref, 1)` assertion (it returns `fault.Unavailable` and the phase stays Failed), or drop
it and keep the one check in `drive`.

### 🟡 Minor 2 — the test helper `failShape` uses the literal `"Ready"`  ·  attribution: model

`fn.Status.Conditions.Set(v1.Condition{Type: "Ready", …})` in `storescaler_test.go`. The constant is
unexported in the `activator` package, so the external test cannot use it; a literal in a test is cosmetic.
Recorded for completeness only.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** `git revert --no-commit 2f5406b ea29232`,
  then restore only the HEAD test file. Results: `TestIssue142_FailedFunctionIsAnsweredWithItsState` FAIL
  (`"detail":"activator.activate: function default/broken did not become ready within 2s"`, after 2.00 s);
  `TestIssue142_FunctionFailedDuringActivationIsAnsweredAtOnce` FAIL (the same detail for `late`, after 2.00 s).
  `TestIssue142_ReclaimOfAFailedFunctionStillSucceeds` passes on the old code, as expected: it pins behavior
  that already existed. The worktree was then reset to `2f5406b` and is clean.
- **They pass with the fix under `-race`**: all three `--- PASS` (0.00 s each); `go test -race ./internal/activator/...` ok for both packages.
- **Mutants**: M2 (disable the poll's `failed` check) → the mid-activation test fails; M3 (apply the guard to the reclaim target too) → the reclaim test fails; M1 survives (Minor 1).
- **Cause, not symptom**: the activation now reads the Function's phase, which is the missing input the issue
  names. No timeout was changed, no retry was added and no error is swallowed. A store read error during the
  poll is logged at debug level and the poll continues, so the activation keeps its earlier behavior and is never worse.
- **The tests use the real path**: a real `activator.New`, the real `storescaler` and a memory store, called
  through `ServeHTTP`. `failsAfterWake` adds the reconciler's write to the real scaler and does not replace the scaler.
- **The activator writes nothing for a Failed function**: the guard returns before `transition`/`Update`,
  and `failed` only reads. This keeps ADR-0016 C2 and ADR-0142.
- **Reuse**: one `FailedFault` builds the fault for both call sites (the round-1 duplication risk was avoided).
  The change also uses `fault.Unavailablef`, `Conditions.Get`, the existing `Deps.Store` (already used for
  idle reclaim), the existing Scaler-error branch of `drive`, and the package's test helpers (`putFunction`,
  `phaseOf`, `ref`). No existing never-ready Endpoints fake was found in `internal/activator` or `internal/testkit`.
- **Conventions**: `fault` errors, ctx-first, slog only, a typed condition constant, top-level imports, short
  doc comments that name the ADR or issue.
- **Scope**: every hunk serves the issue; no test weakened or deleted; no ADR file touched.
- **Observation (not a finding)**: some Failed reasons are requeued by the Function reconciler
  (`gateFailed` returns `RequeueAfter`, for example `ArtifactUnresolved`). A call during such a Failed spell
  now gets an immediate 503 instead of a wait that might succeed. This matches the issue's expected behavior,
  and a 503 is retryable.
- **Checks (touched packages)**: `gofmt -l` clean; `go vet ./internal/activator/...` ok;
  `golangci-lint run ./internal/activator/...` 0 issues; `go test -race ./internal/activator/...` ok.
  The e2e suite, Linux lint and the lanes are left to the group gate.
- **Shape**: both subjects are `fix(activator):`; `Fixes #142` on `ea29232`, `Refs #142` on the rework commit;
  both commits carry the attribution trailer. The two commits should be squashed into one for the one-commit-per-issue PR.

### Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue142_…` reproduces the issue | yes |
| 2 | fails on pre-fix code for the reported reason | yes |
| 3 | passes with the fix, un-skipped, `-race` | yes |
| 4 | reverting / mutating key lines fails a test | yes: the revert fails and M2/M3 fail. M1 survives on a redundant guard (Minor 1) |
| 5 | root cause fixed, not masked | yes |
| 6 | only the issue's scope; no test weakened | yes |
| 7 | no Accepted/Implemented ADR contradicted or edited | yes |
| 8 | build, vet, lint, tests green (touched packages) | yes |
| 9 | conventions | yes |
| 10 | reuse, no duplication | yes |
| 11 | commit shape | yes (squash at PR time) |

**11 / 11.**

### Model scorecard

claude-opus-5-5 · issue #142 · fix (round 2) · pass · 0 blockers · 0 majors · 2 minors · 2 model-attributed · DoD 11/11.

### Recommendation

Pass. Optionally, add a direct `ScaleTo(…, 1)` assertion for a Failed function to pin the guard, or remove
the guard. Squash the two commits into one `Fixes #142` commit for the PR.
