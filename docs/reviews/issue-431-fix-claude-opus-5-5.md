# Fix review — issue #431 (claude-opus-5-5)

- **Issue**: pyvvo/funcd#431 — dev-tag persist and S3 tests fail, not skip, when python3 cannot load the shim
- **Change**: branch `fix/i431`, commit 7d8486d `fix(funcdctl): skip the dev persist and S3 tests when no runtime can load the shim`
- **Touched**: `cmd/funcdctl/dev_phase2_test.go` only (+39 / −9)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Summary

`requireRuntime` now asks `devShimOptions` — the same check `startDev` runs at boot (`cmd/funcdctl/dev.go`, the
`process.PythonShimLoadError` gate and the final NotFound) — and skips on its NotFound fault. A python3 that
cannot load the shim therefore counts as "no runtime", exactly as at startup. The cause named in the issue
(a PATH-only `LookPath` check that diverged from the #322 load check) is removed, not masked. Every check ran
green; no finding.

## Blockers

None.

## Majors

None.

## Minors

None.

## Notes (not scored)

- **env** — the prescribed revert check (`git revert --no-commit 7d8486d`) cannot fail the regression test:
  the fix and the test live in the same test file, so the revert removes the test too (`[no tests to run]`).
  The pre-fix behavior was instead checked by restoring only the old `requireRuntime` body while keeping the
  new test (row 1 below). This is a property of a test-only fix, not a defect of the change.

## Verification evidence

| Check | Result |
|---|---|
| Full revert of 7d8486d, run `TestIssue431` | `[no tests to run]` (test removed with the fix — see Notes) |
| Pre-fix `requireRuntime` body + new test | **FAIL** — `ran` is true: "Should be false" (the gated subtest ran on a host with only a broken python3) |
| HEAD, `-race`, `TestIssue431` + the four gated tests | PASS; the `gated` subtest is SKIPped; the four gated tests run (node present here) |
| Mutant: skip on `fault.Invalid` instead of `fault.NotFound` | FAIL — `require.NoError` gets "no runtime found on PATH" |
| Mutant: `needPython=true` in the `devShimOptions` call | FAIL — Invalid "cannot load the runtime shim" instead of a skip |
| Issue's own steps: the four gated tests from a compiled test binary, env cleared, PATH = only a python3 that cannot import fastjsonschema | pre-fix: all four **FAIL**; fixed: all four **SKIP**, package PASS |
| `go test -tags dev -race ./cmd/funcdctl/` | ok |
| `go test -race ./cmd/funcdctl/` | ok |
| `go vet -tags dev ./cmd/funcdctl/` | ok |
| `golangci-lint run --build-tags dev ./cmd/funcdctl/...` | 0 issues |
| Worktree after review | at 7d8486d, clean |

## ✅ Verified correct

- **Cause, not symptom**: the skip is now driven by the real boot predicate, so it cannot drift from
  `devShimOptions` again (including future changes to its interpreter precedence or load gate).
- **Reuse**: no new helper; `os/exec` import dropped. The test reuses `devProject`, `permissiveContract`,
  `cli.startDev` and `fault.KindOf`. No other test helper in the repo carries the old PATH-only check.
- **Regression test is faithful**: it first proves the precondition (`startDev` returns NotFound on that host),
  then asserts `requireRuntime` skips — it ties the skip to real startup behavior, not to an assumption.
- **Cleanup**: `cleanup()` is called on success, removing the shim temp dir `devShimOptions` creates; on error
  `devShimOptions` removes it itself.
- **Hygiene in the test**: `t.Setenv` for PATH/FUNCD_NODE/FUNCD_PYTHON; `startDev` fails before `funcd.New`,
  so the `t.TempDir()` for the fake interpreter does not hit the socket-path limit; the dev instance is
  cancelled and stopped in `t.Cleanup`.
- **Conventions**: `context.Background()` matches the file's idiom; comments state the why; no YAML; imports
  at top level.
- **Scope**: one file, every hunk serves the issue; no test weakened or deleted (the four gated tests still
  run wherever a runtime can boot).
- **ADRs**: no ADR touched or contradicted (ADR-0125 dev-shim behavior unchanged; test-only change).
- **Shape**: `fix(funcdctl):` subject, Cause/Fix/Test body, `Fixes #431`, attribution trailer, one commit.

## Checklist (Definition of Done)

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue431_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes (targeted revert) |
| 3 | Passes with the fix, un-skipped, `-race` | yes |
| 4 | Revert/mutants fail a test | yes (2/2 mutants killed) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no test weakened | yes |
| 7 | No ADR contradicted/edited | yes |
| 8 | Build, vet, lint, tests green (host; Linux lint and e2e left to the group gate) | yes |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes (PR not yet opened) |

**11 / 11.**

## Recommendation

Pass — hand back to `/fix` Step 8 for the PR.
