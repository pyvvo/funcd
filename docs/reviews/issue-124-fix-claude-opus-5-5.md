## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #124 fix, model: claude-opus-5-5)

Reviewed commit `f88e580` ("fix(workflow): persist each step and dispatch attempt before it runs") on
branch `fix/198-workflow`, at the group head `fbcb9ff`. The other commits of the group are out of scope.

The issue: the engine wrote the run record once per ready batch, and `rebuildState` dropped the attempt
count of a `Running` step. After a crash, recovery re-ran siblings that had already succeeded and
re-dispatched the in-flight step from attempt 1, with the attempt ID of the lost dispatch. This
contradicts ADR-0094 ("a write-ahead intent precedes every dispatch"; scenario
`crash-recovery-resumes-run`: "a fresh attempt ID").

The fix:

- `dispatchStep` sets `n.attempts` and persists the record before each attempt (the write-ahead intent).
- The engine persists the record before a builtin or sub-workflow step starts.
- `rebuildState` keeps the attempt count of a `Running` step.
- The retry loop starts at `n.attempts + 1` and runs to `max(maxAttempts, first)`, so a recovered step
  always gets one re-dispatch with a fresh attempt ID.
- A write-ahead failure is wrapped in `writeAheadError`. It ends the drive through the new
  `recordFailed`, which the batch-end write now shares: `PayloadTooLarge` fails the run, and any other
  error goes back to the reconciler.

### 🔴 Blocker

None.

### 🟡 Major / Minor

None.

### Observation for the group (not scored, not part of #124)

- At `fbcb9ff`, a mutant that deletes the pre-start persist for a builtin or sub-workflow step
  (`internal/workflow/engine.go` `runStep`, the `functionOf(st) == nil` block) **survives**
  `TestIssue124_…`. At `f88e580` the same mutant fails the `sub-workflow sibling` case:
  `recovery dispatched map[c:[2] e:[1] x:[1]] … want map[e:[1] x:[1]]`. The later commit `64310c9`
  (concurrent fan-out) persists after every settled success, and that persist now covers what the
  test observes. The pre-start persist is still correct, but no test pins it down any more. This
  belongs to the review of `64310c9`, not to this fix.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the issue's reason.** `git revert --no-commit f88e580` on
  `fbcb9ff` conflicts in `engine.go` and `engine_test.go`, because later commits rewrote `drive` for
  concurrent dispatch. The revert check therefore ran in two ways:
  1. At the fix's own commit `f88e580`, with the pre-fix `engine.go` (`f88e580^`) applied as an
     overlay, `go test -race -run TestIssue124` → FAIL. All three cases fail:
     - `fan-out`: `recovery dispatched map[c:[1] d:[1] e:[1]] … want map[d:[2] e:[1]]`. This is
       exactly the issue's "Actual behavior".
     - `retry`: `map[b:[1 2 3]] … want map[b:[3]]`. Recovery restarts with the full retry budget.
     - `sub-workflow sibling`: `map[c:[1] e:[1] x:[1]] … want map[e:[1] x:[1]]`.
  2. At `fbcb9ff`, all four of the fix's key edits were removed by hand as an overlay: the
     `rebuildState` attempts restore, `first := 1`, the per-attempt persist, and the pre-start
     persist. FAIL, in all three cases, for the same reason (attempts restart at 1, and a succeeded
     sibling is re-run).
- **It passes with the fix**, un-skipped, under `-race`: at `f88e580` (PASS) and at `fbcb9ff` with
  `-count=3` (PASS, 3/3).
- **The mutants fail at `fbcb9ff`:**
  - Dropping `n.attempts = s.Attempts` in `rebuildState` → FAIL.
  - Dropping the per-attempt write-ahead persist → FAIL.
  - `first := 1` → FAIL.
  - The pre-start persist mutant: see the observation above. It fails at `f88e580`.
- **The root cause is fixed, not masked.** Both causes that the issue names are removed. The record is
  durable before every attempt and every step start, and the attempt count survives a restart. There
  is no timeout, retry or swallowed error. Execution stays at-least-once (ADR-0094).
- **Scope.** Every hunk serves the issue:
  - The rename `max` → `maxAttempts` frees the `max` builtin for the new loop bound.
  - `recordFailed` merges the existing batch-end `PayloadTooLarge` handling with the new write-ahead
    path.
  - Only comments that describe the changed behavior were edited.
  - No test was weakened or deleted. `TestCrashRecoveryResumesRun` still passes.
- **ADRs.** The fix conforms to ADR-0094 (write-ahead intent, fresh attempt ID on recovery,
  at-least-once) and ADR-0100 (the attempt count is the lineage field). It does not affect the
  ADR-0107 replay, because re-run steps are built from `newRunState` with zero attempts. No ADR file
  was edited.
- **Reuse.**
  - Persistence goes through the existing `persist`, and errors go through `api/fault`.
    `writeAheadError` is a small unexported wrapper with `Unwrap`, so `fault.KindOf` still sees the
    store's kind. It is needed to tell a store refusal apart from a dispatch failure that has the
    same fault kind.
  - The test reuses the package's `capturingDispatcher`, `fakeChildren`, `step`, `subwfStep` and the
    in-memory Badger store.
- **Conventions.** The code takes `ctx` first and uses `api/fault` errors, with no `any` in its
  signatures and no new imports beyond `fmt` in the test. Comments are short and cite ADRs. The test
  is table-driven, like its neighbours.
- **Checks** (at `fbcb9ff`, through `nix develop -c`):
  - `gofmt -l internal/workflow`: clean.
  - `go build ./...` and `GOOS=linux go build ./...`: OK.
  - `go vet ./internal/workflow/...` on the host and on Linux: OK.
  - `golangci-lint run ./internal/workflow/...` on the host and on Linux: `0 issues.`
  - `go test -race -count=1 ./internal/workflow/...`: ok.
  - `go test -tags e2e -count=1 ./pkg/funcd/...`: ok (148 s). `pkg/funcd` is the only package that
    imports `internal/workflow`.
  - At `f88e580` alone, build, vet, lint and the `-race` tests are also green.
  - No Lima lane covers `internal/workflow`.
- **Shape.** The subject is `fix(workflow): …`, the body has `Fixes #124` and the `Co-Authored-By`
  trailer, and the commit covers one issue.

### Definition of Done

11 / 11 items hold:

1. A regression test reproduces the issue.
2. It fails on the pre-fix code, for the reported reason.
3. It passes with the fix, under `-race`.
4. Reverting or mutating the key lines fails a test, at the fix's commit.
5. The root cause is fixed.
6. Only the issue's scope changed.
7. No ADR is contradicted or edited.
8. The checks and e2e are green.
9. The conventions hold.
10. The change reuses what exists.
11. The commit shape is correct.

There are no misses.

### Model scorecard

Not recorded here (a later stage records it). Fields: claude-opus-5-5 on issue #124 (fix) → pass,
0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Sign off. The group review should add a test that pins the builtin and sub-workflow pre-start persist,
which `64310c9` made unobservable (see the observation above).
