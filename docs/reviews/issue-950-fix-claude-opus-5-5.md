## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #950 fix, model: claude-opus-5-5)

Change: branch `fix/950-never-checks-in-test-goroutine`, commit 8b8d20ab
`test(funcd): check a Never condition that uses the test in the test's goroutine`, on base f49b82b4
(16 files, +161/-38). The change adds `internal/testkit/poll` (`Never`, which checks in the caller's goroutine) and
moves the 26 `require.Never`/`assert.Never` calls in `pkg/funcd` whose condition uses `t` onto it. It also builds
`stays` (`pkg/funcd/app_config_e2e_test.go:98`) on `poll.Never` and adds a source guard in `tests/e2e`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1: the guard does not catch a `Never` whose condition is a named variable** · attribution: `model`.
  `TestIssue950_NeverChecksDoNotUseTheTest` flags a call only when its condition argument is a function literal
  (`tests/e2e/neverhygiene_test.go:38`). In a scratch worktree of the fix, I made two mutants of the same site
  (`pkg/funcd/app_ready_e2e_test.go:76`). G1 put back the original `require.Never(t, func() bool { … e.app(t, "todo") … }, …)`,
  and the guard failed with `[pkg/funcd/app_ready_e2e_test.go:76]`. G2 moved the same condition into
  `notReady := func() bool { … e.app(t, "todo") … }` and called `require.Never(t, notReady, …)`. The guard passed
  (`ok … tests/e2e 0.663s`), although the race is the same. This package already passes named conditions to the
  sibling helper (`stays(t, waiting, …)`, `pkg/funcd/app_requires_e2e_test.go:132`), so a future regression of this
  shape is plausible. Fix: also flag a `require.Never`/`assert.Never` whose condition is not a function literal, or
  resolve a local identifier to the literal it is assigned. None of the 12 remaining `Never` calls in the tree would
  trip the stricter rule, because all of them take a literal that does not use `t`.

### ✅ Verified correct (keep it)

- **The cause is real and the fix removes it.** testify v1.11.1 `assert/assertions.go:2135-2168`: `Never` starts
  each check with `go checkCond()` and returns `true` on `timer.C` without waiting for a check that is still running.
  This matches the issue. `poll.Never` (`internal/testkit/poll/poll.go:10-19`) calls `cond()` in a plain loop, so no
  check outlives the call. No timeout was lengthened and no error was swallowed. `Eventually` and `EventuallyWithT`
  are not siblings: they return success only from `case v := <-ch` after a check has finished, and their timer path
  fails the test anyway.
- **The user-visible failure is reproduced and fixed.** The out-of-tree reproduction (which uses the real
  `startGC` harness and holds the second check until `e.ctx` is cancelled) was added to `pkg/funcd` as a
  `go test -overlay` file, so nothing was written to the worktree. `-tags e2e -race -count=3`:
  `TestZZRepro950RequireNever` failed 3/3 with the CI error (`app_fixture_e2e_test.go:106 … get App/todo …
  context canceled`), and `TestZZRepro950PollNever` passed 3/3.
- **The guard fails on the base for the issue's reason.** The guard reads the sources at run time, so an overlay
  does not reach them. It ran in a scratch worktree of f49b82b4 with the guard file copied in, and failed with exactly
  26 hits: `app_config` ×3, `app_e2e` ×2, `app_pause` ×3, `app_ready` ×2, `app_revision`, `app_rollout`,
  `app_store` ×3, `gc` ×3, `health`, `issue769` ×2, `issue796` ×3, `pool_member`, `pooling`. On the fix it passes
  under `-race` (`--- PASS … (0.79s)`).
- **The poll mutants are killed** (overlays on `internal/testkit/poll/poll.go`). M1 is testify's goroutine shape and
  fails `TestIssue950_NoCheckRunsAfterNeverReturns` with "a check still runs after Never returned", 5/5 runs. M2,
  which returns after the first check, fails `TestNeverHoldsForItsWindow` and `TestNeverStopsAtTheFirstTrue`. M3,
  which ignores the condition's result, fails `TestNeverStopsAtTheFirstTrue`. Unmutated, the package passes under
  `-race -count=20`.
- **Every rewrite keeps its semantics.** A `go/ast` comparison of each base call against its rewrite (whitespace
  normalised) gave `compared 26 sites, 0 mismatches`. The condition, window, tick, message arguments and the `t`
  are identical. All 20 `require.Never` sites became an expression statement `require.True(t, poll.Never(…), msgs…)`.
  The 6 `assert.Never` sites keep their assignment (`held :=` ×5, `stayed :=` ×1), and the `require.True(t, held, …)`
  lines after them are unchanged. The only behavioural differences are stricter than before. `poll.Never` makes one
  check after the window ends. When a window is already past (the `time.Until(…)` windows), testify could return
  without checking at all, but `poll.Never` checks once. A failing `assert.Never` site now reports one failure
  instead of two. `stays` keeps its message and adds the same final check.
- **No site still needs the rewrite, and none was rewritten needlessly.** Each of the 26 rewritten conditions
  calls a helper with `t` (`e.app`, `e.exists`, `r.fn`, `r.get`, `h.poolPID`, `h.poolWorkerPID`, `e.versions`,
  `e.apiImage`, `e.appRevision`, `e.anyPartExists`). The 12 `Never` calls left in the tree, in `cmd/funcd`,
  `internal/activator`, `internal/controlplane`, `internal/kvstore/badger`, `internal/sensor` and three in
  `pkg/funcd`, read only channels, atomics, mutex-guarded fakes (`asleepRig`, `poolHost`, `fakeBucket`,
  `s3stub.Stub.Keys`) or `os.Stat`. None of them calls `require` or a helper that takes `t`, and no test harness keeps
  a `*testing.T` field. No call goes through `require.New`/`assert.New`.
- **Scope.** Every hunk serves #950: the new package and its tests, the 26 rewrites with their imports (one
  `assert` import dropped from `issue796_internal_test.go`), `stays` built on the new helper, and the guard. No test
  was weakened or deleted, and no `go.mod`/`go.sum` change was made.
- **Reuse.** `stays` was the only synchronous never-helper in the tree. It lives in an e2e-tagged `funcd_test` file,
  which the untagged internal `issue769`/`issue796` tests cannot import, and it fails the test instead of returning
  the bool that those sites use. Moving its loop into `internal/testkit/poll` and building `stays` on it removes the
  duplicate. Neither `internal/testkit` nor the module's dependencies offer a synchronous `Never`. The guard reuses
  the existing `repoRoot` and `trackedFiles` helpers in `tests/e2e`, in the same way as the YAML-style guard.
- **Conventions.** The package doc names its issue, as `internal/testkit/freeport` does (#758). Comments say why,
  without narration. Every new import is in the project group in sorted order, and `gofmt -l` on all touched files
  is clean. The guard imports only the standard library and testify, so it stays inside the `tests/e2e` depguard
  boundary. The commit uses `test(funcd):` and not `fix(<scope>):`. For a test-only fix this follows the merged
  precedents `c514b151` (#919, `Fixes #904`) and `1bcd9cb4` (#921, `Fixes #916`), and it cuts no release-please
  patch for a change that ships no code. It also has `Fixes #950`, the attribution trailer, and one issue in one
  commit.
- **ADRs.** No ADR or living doc is touched. The change is test-only and contradicts no Decision or Contract.
- **Checks** (all through `scripts/agent/d`):
  - `go test -tags e2e -race -count=1` on every top-level test in the 13 touched `pkg/funcd` files plus the other
    two `stays` callers (`TestScenarioAppHookRepeatsAfterRestart`, `TestScenarioAppRequiresWaits`): `ok
    pkg/funcd 106.111s`, 74 passed, 0 skipped, 0 failed, no data race. The pool tests' `nodejs22`/`python314`
    subtests ran their full windows (about 3.6 s each).
  - `./internal/testkit/poll/` and the guard both pass under `-race`.
  - `go vet -tags e2e` passes on darwin and on `GOOS=linux`.
  - `golangci-lint run --build-tags e2e ./internal/testkit/poll/ ./pkg/funcd/ ./tests/e2e/` reports `0 issues.` on
    darwin and on `GOOS=linux` (host-built linter binary).
  - The repo-wide gate was not run, by instruction.

### Definition of Done

12 / 12 items hold.

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue950_…` regression tests reproduce the issue | yes: the guard covers the call sites, and the unit test covers the mechanism |
| 2 | Fails on the pre-fix code, for the reason | yes: 26 hits on base, and testify's shape fails the unit test |
| 3 | Passes with the fix, un-skipped, `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes: M1–M3 and G1 killed (G2 is Minor 1, a new shape of regression rather than a mutation of the fix) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Scope only, no test weakened | yes: 26/26 arguments identical |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint (host and Linux), tests green | yes |
| 9 | Conventions | yes |
| 10 | Reuse | yes |
| 11 | Commit shape | yes: `test(funcd):` per the test-only precedent, `Fixes #950`, trailer |
| 12 | Every case fixed and no sibling left | yes: 26/26 sites, and the 12 remaining calls are safe |

### Model scorecard

To record: claude-opus-5-5 on issue #950 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.
See docs/reviews/model-scorecard.md.

### Recommendation

Ship it. Minor 1 can be folded in before the PR: make the guard flag a `Never` whose condition is not a function
literal. It is optional and does not block.

## Follow-up

Minor 1 was fixed in the same commit (be635aa8, after the rebase onto main): the guard now also refuses a `Never` whose condition is not a function literal. A scratch edit that moved the app_ready condition into a variable makes the guard fail at `pkg/funcd/app_ready_e2e_test.go:77`; the fixed tree passes.
