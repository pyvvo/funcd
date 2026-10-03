## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #452 fix, model: claude-opus-5-5)

Change: branch `fix/i452`, commit c823eab `fix(function): let the node-gated shim tests wait out a slow shim boot`.
Touched: `internal/function/shim_test.go`, new `internal/function/export_test.go`. Test-only change: the defect is the
tests' own fixed 5 s deadline, so no production file changes.

### 🟡 Minor 1 — the fixed-port helper's "shim exited" early return is untested  ·  attribution: model

`shimReadyOnFixedPort` (`internal/function/shim_test.go`) now watches `cmd.Wait()` and returns `false` at once when the
shim exits. A mutant that returns `true` on that branch passes every test in the package
(`TestIssue452_…`, `TestScenarioShimEndToEndNode`, `TestScenarioShimFixedPortBindNode` all PASS). No test starts a
shim that exits on the fixed-port path, so a regression there would make a crashing shim report success. Fix
(builder, optional): a sub-test that starts the shim on a handler that exits at load and asserts `false`.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** The fix is test-only, so a literal `git revert --no-commit c823eab`
  removes the regression test too (`[no tests to run]`). The equivalent check was run as overlay mutants that restore
  the old 5 s deadlines while keeping the new test:
  - bringUpRealShim back to a 5 s deadline → `TestIssue452_RealShimWaitsOutASlowBoot/reconciled` FAIL at 5.01 s,
    "the real Node shim booted and reported readiness" (phase not Ready).
  - fixed-port wait back to 5 s → `…/fixed_port` FAIL at 5.02 s, "a shim that boots in 6 s bound the fixed
    FUNCD_PORT…".
  Together these are the pre-fix behavior, and each half fails its own sub-test, so both suspected-cause sites of the
  issue are covered.
- **Passes with the fix**, un-skipped (node present), under `-race`: `TestIssue452_…` (both sub-tests, parallel, ~6 s),
  `TestScenarioShimEndToEndNode`, `TestScenarioShimFixedPortBindNode` — `ok internal/function 7.9s`.
- **Cause, not symptom.** The issue's cause is a test deadline (5 s) shorter than funcd's own boot budget
  (`bootTimeout = time.Minute`, `internal/function/function.go:694`). The fix ties both waits to that budget instead of
  picking a larger magic number: bringUpRealShim reconciles until the Function leaves `Deploying` and asserts `Ready`;
  the fixed-port wait is bounded by `function.BootTimeout`.
- **The reconciler really bounds the unbounded loop.** `Deploying` is set only while replicas run unready or wait out a
  backoff (`function.go:600-605`); a never-ready replica fails at `bootTimeout` (`TestIssue76_NeverReadyHandlerFailsAfterBootTimeout`).
  A scratch probe (overlay, removed) ran bringUpRealShim on a handler that calls `process.exit(1)` at load: it ended in
  0.11 s with phase `Failed`, a clear assertion and no hang.
- **Scope.** Every hunk serves the issue: the `src` parameter lets the regression test feed a slow handler, and
  `TestScenarioShimFixedPortBindNode` keeps its assertion through the extracted helper. No test was weakened.
- **Reuse.** `export_test.go` exposing an internal constant follows the existing idiom
  (`internal/activator/export_test.go`); the constant is reused, not duplicated. `internal/testkit` has no readiness
  wait helper to reuse; the polling loop keeps the shape of the code it replaces.
- **Conventions.** Top-level imports, no comment bloat (each new comment states a why: the ADR-0030 §4b bound),
  surrounding naming kept. No ADR edited or contradicted.
- **Checks** (touched package): `go vet ./internal/function/` clean; `golangci-lint run ./internal/function/` 0 issues;
  tests above green with `-race`.
- **Shape.** `fix(function):` subject, `Fixes #452`, attribution trailer, one issue in one commit.
- Worktree left at c823eab and clean.

### Definition of Done

10 / 10 applicable items hold (item 8's host/Linux lint, repo-wide tests and e2e belong to the group gate; the touched
package's build, vet, lint and `-race` tests are green). Item 4 holds for the key lines (both deadlines); the Minor
notes one surviving mutant on a secondary branch.

### Model scorecard

Not recorded here (the group step writes the ledger): claude-opus-5-5 on issue #452 (fix) → pass, 0/0/1,
1 model-attributed, DoD 10/10.

### Recommendation

Sign off. Optionally add a fixed-port sub-test for a shim that exits at load, so the early-return branch is pinned.
