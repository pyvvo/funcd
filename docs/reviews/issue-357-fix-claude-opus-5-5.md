# Fix review — issue #357 (`workernode/local.Serve has no callers`) — claude-opus-5-5

- **Change**: branch `fix/i357`, one commit `9ce3e04 fix(workernode): remove the uncalled local.Serve`
  (`internal/workernode/local/local.go` −22, `internal/workernode/local/manager_internal_test.go` +55).
- **Governing ADRs**: ADR-0064 (fn-to-fn links, per-sandbox local API — **Implemented**), ADR-0127
  (context.blob data plane, `NewManager` contract — Implemented), ADR-0002 (conventions).
- **Verdict**: **changes-requested** — the code change is correct and well tested, but it deletes a
  function that an Implemented ADR's Contracts declare, when the issue offered a conforming option.

## Verification run

| Check | Result |
|---|---|
| Regression test, fix reverted (`git revert --no-commit 9ce3e04`, test file restored from HEAD) | **FAIL** for the issue's reason: `expected: []string{"SocketFor"}`, `actual: []string{"Serve", "SocketFor"}` |
| Regression test, fix in place, `-race` | `ok internal/workernode/local` (whole package, `-race -count=1`) |
| Mutant 1: `SocketFor` binds via `(&net.ListenConfig{}).Listen` instead of `net.Listen` | test FAILs (killed — but see Minor 1) |
| Mutant 2: a second `net.Listen("unix", …)` binder added in `manager.go` | test FAILs (killed) |
| `go vet ./internal/workernode/local/` | clean |
| `golangci-lint run ./internal/workernode/local/` | `0 issues.` |
| Callers of `local.Serve` in Go code | none (only historical review docs mention it) |
| Worktree after review | at `9ce3e04`, clean |

The full `git revert` of the commit also removes the new test (`no tests to run`), so the revert check
was rerun with only `local.go` reverted.

## Blockers

None.

## Majors

1. **The change deletes a symbol that ADR-0064's Contracts declare** — attribution: `model`.
   `docs/adr/0064-fn-to-fn-rpc-links.md` (Status: Implemented) lists, in its Contracts block,
   `func Serve(ctx context.Context, path string, h http.Handler) error` ("One call per sandbox
   provisioning"), and its file list names `Serve` in `internal/workernode/local/local.go`. No later ADR
   supersedes that line: ADR-0127 contracts `NewManager` but does not retire `Serve`. After this commit
   an Implemented ADR's Contracts name a function that does not exist, and the frozen ADR cannot be
   corrected in place. The fix skill's rule is that a fix contradicts no Accepted/Implemented ADR's
   Contracts, and that a fix which must do so needs an ADR. This fix did not have to: the issue's
   "Done when" also accepts keeping `Serve` "with a caller and a test if it is meant to stay as a seam".
   The commit message cites ADR-0127 as the contracted surface but does not mention ADR-0064's `Serve`
   contract. The issue's own "Done when" suggested removal without flagging the contract, which
   partly explains the choice.
   **Rework**, pick one:
   - keep `Serve`, have it share the listen and serve path with `Manager.SocketFor` so the two cannot
     drift (the issue's real concern), and give it a test. This conforms to ADR-0064 and needs no ADR; or
   - route the removal through `/adr`: a small ADR that supersedes ADR-0064's `Serve` contract line
     (pointing at the ADR-0127 `NewManager`/`SocketFor` surface), then land this commit unchanged.

## Minors

1. **The regression test is a source-shape guard that only recognizes `net.Listen`** — attribution:
   `model`. `TestIssue357_LocalAPIHasOneListenerPath` parses the package's `.go` files and collects
   functions that call the selector `net.Listen`. A binder written with `net.ListenUnix` or
   `net.ListenConfig.Listen` passes unnoticed. Mutant 1 shows the reverse: a harmless refactor of
   `SocketFor` to `ListenConfig` fails the test. For removing dead code, a structural guard is a
   reasonable choice and no duplicate helper exists (no other `go/parser` test in the repo). It is
   still brittle in both directions. Matching the `net` listen family, or asserting on the exported
   API surface, would make it sturdier.

## ✅ Verified correct

- The root cause is removed, not masked: the uncalled second binder is gone, and `newServer` now has
  one caller, `Manager.SocketFor`.
- The scope is tight: only `Serve` and its two imports that became unused (`net`, `os`) are removed;
  `errors` and `fault` remain in use. The test is additive, and no test was weakened.
- Conventions: imports at top level, the `//go:embed *.go` FS is documented by one comment saying why,
  and the test name follows `TestIssue<N>_…`.
- Reuse: nothing duplicated; the test uses the standard library (`go/ast`, `go/parser`, `embed`).
- Commit shape: `fix(workernode):` subject, `Fixes #357`, the attribution trailer, one issue per commit.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue357_…` reproduces the issue | yes |
| 2 | Fails on pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Revert or mutation fails a test | yes |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no test weakened | yes |
| 7 | No Accepted/Implemented ADR contradicted | **no** (Major 1) |
| 8 | Build, vet, lint, tests green (touched package; the repo-wide and Linux checks are left to the group gate) | yes |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |

**10 of 11.**

## Recommendation

Return to `/fix`. Prefer the conforming rework: keep `Serve` as a thin wrapper over the shared
`SocketFor` listen and serve path, and add a test. Otherwise move the removal to `/adr` to supersede
ADR-0064's `Serve` contract. In either case, broaden the regression guard (Minor 1).
