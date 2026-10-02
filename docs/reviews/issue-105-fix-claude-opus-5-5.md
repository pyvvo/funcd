# Issue #105 Fix Review — `funcdctl dev` never notices a crashed catalog engine

**Verdict**: **pass**. With the liveness fix removed, the regression test fails for the issue's reason: after a
SIGKILL, Converge keeps reporting the dead engine Ready and never relaunches it. With the fix, the test passes under
`-race`. Three mutants of the fix's key lines each fail a test. The change removes the cause the issue names and
reuses the reaper pattern of the process runtime driver. It conforms to ADR-0125 Decision 5 and to ADR-0002. There
is one Minor finding, attributed to `env` and not scored.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #105 · ADR-0125 Decision 5 (dev extracts and supervises the catalog engine) · ADR-0002 ·
`CLAUDE.md` style rules

The fix is one commit, `29ea05c` (`fix(catalog): relaunch a crashed funcdctl dev catalog engine instead of reporting
it Ready`), on branch `fix/i105`. It touches `internal/catalog/devengine/devengine.go` and
`internal/catalog/devengine/devengine_test.go`. Both files carry the `dev` build tag.

## Verdict: pass — 0 blockers, 0 majors  (issue #105 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

- **Minor (env) — CI does not run the regression test.** The whole `devengine` package is behind `//go:build dev`.
  `just ci` runs `go test ./...` without `-tags dev`, and no workflow step adds that tag to a test run (the tag
  appears only in the release build of `funcdctl-dev`). `TestIssue105_ConvergeRelaunchesCrashedEngine` therefore never
  runs in CI, so a later regression would go unnoticed. This gap existed before the fix and covers the whole package,
  so it is not counted against the model. Suggested follow-up: add `go test -tags dev ./internal/catalog/devengine/...`
  to the fast lane. The new test needs no real engine and takes about 0.3 s.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix.** `git revert --no-commit 29ea05c` also removes the new test and the
  test seam it needs, so the plain revert runs no test (`[no tests to run]`). I therefore built a `go test -overlay`
  of `devengine.go` that keeps the two seam fields but restores the pre-fix liveness logic: `alive()` checks
  `cmd.ProcessState == nil`, no reaper goroutine runs, and `stop()` calls `cmd.Wait()`. With that overlay,
  `TestIssue105_ConvergeRelaunchesCrashedEngine` fails at `devengine_test.go:177` with "Converge still reports
  Ready=true at the crashed engine (pid …, 127.0.0.1:…): never relaunched". That is the issue's reason: no Wait runs,
  `ProcessState` stays nil, and the dead engine is reported Ready at its old address. After
  `git reset --hard 29ea05c`, the worktree was clean at that HEAD.
- **It passes with the fix.** `go test -tags dev -race -count=1 -run TestIssue105 ./internal/catalog/devengine/`
  passes (0.33 s), and `-count=3` over the whole package passes. Nothing is skipped.
- **The user-visible behavior is fixed.** The test is the issue's probe, without the overlay of the embedded archive.
  The test binary acts as a fake duckdb engine (through `TestMain`). The fake engine serves the `quack_serve` address
  from the real `-init` script until stdin closes, so the real `launch`, `waitReady`, `stop` and `Converge` paths run.
  The test SIGKILLs the engine, then calls Converge again. It passes only if a new `engineProc` replaces the crashed
  one, reports Ready, and accepts a TCP dial at the new address.
- **The cause is fixed, not the symptom.** A reaper goroutine started right after `cmd.Start` is now the only caller of
  `cmd.Wait`. It closes `done`, and `alive()` and `stop()` read that channel. A crashed engine is reaped at once, which
  also removes the zombie the issue describes. The fix adds no retry, timeout or swallowed error. I also checked that
  the fix cannot start a relaunch loop through `exec.CommandContext(ctx, …)`: `Converge` receives the controller's
  long-lived worker context (`internal/controller/controller.go`), not a per-reconcile context that is cancelled after
  each call.
- **Mutants** (each built with `go test -overlay`, each killed):
  - `alive()` always returns true for a non-nil proc: the regression test fails ("never relaunched").
  - The reaper calls `cmd.Wait()` but never closes `done`: the test cannot finish, because `stop()` blocks on `done`
    in the cleanup, and the run fails on `-timeout 40s`.
  - `Converge` reuses an existing proc without checking `alive()`: the regression test fails ("never relaunched").
- **Scope.** Every hunk serves the issue. The two function fields `bundled` and `extract` are the smallest seam that
  lets the lifecycle run when only the placeholder engine is embedded. They default to `embedengine.Bundled` and
  `embedengine.Extract`, so production behavior does not change. No existing test was changed or removed, and no ADR
  or living doc was touched.
- **Reuse.** The `done`-channel reaper copies the pattern of `internal/runtime/process/process.go` (`wait` is the only
  caller of `cmd.Wait`, and `Stop` waits on `inst.done`), as the commit body says. `internal/runtime/ctrmanager` uses
  the same idiom. The repo has no shared helper-process harness that the `TestMain` fake engine could have reused.
  The standard library offers nothing better here than this re-exec idiom.
- **Conventions.** The package is unchanged under ADR-0002: errors still go through `api/fault`, logging is `slog`,
  and no signature takes `any`. The imports in the test are at the top level. The new comments state why
  (`ProcessState` is set only by Wait; the fields are a test seam) and do not describe each line. The test follows the
  `TestIssue<N>_…` naming. There is no YAML in the change.
- **ADRs.** The fix brings the code in line with ADR-0125 Decision 5 and with the package comment "this
  re-convergence IS the supervision". No Accepted or Implemented ADR file was edited.
- **Checks** (touched package only, through the pinned dev shell): `gofmt -l internal/catalog/devengine` prints
  nothing; `go build ./...` passes; `go vet -tags dev ./internal/catalog/devengine/` passes;
  `golangci-lint run --build-tags dev ./internal/catalog/devengine/...` reports `0 issues`;
  `go test -tags dev -race -count=3 ./internal/catalog/devengine/` passes. As directed, this gate did not run the e2e
  suite, the repo-wide tests, the Linux lint or any lane; the group gate runs them.
- **Shape.** The commit has a `fix(catalog):` subject, `Fixes #105`, the `Co-Authored-By` trailer, and covers one
  issue. Its body names the cause, the fix, the test seam and the regression test.

## Recommendation

Pass. Hand back to `/fix` for the PR. Consider filing the CI gap as a separate task: `just ci` should run the
`dev`-tagged packages' tests so that this regression test runs.
