## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1118 fix, model: claude-opus-5-5)

This report reviews branch `fix/1118-dev-settle` at 8c5cebb3, which is two commits on main 42897d43:

- 820a0e88 `fix(funcdctl): dev reloads a build only once its tool stops writing it`;
- 8c5cebb3 `fix(funcdctl): dev reads a new resource file again once it is saved fixed`, a defect the fixer found on
  the way.

The diff (`git diff origin/main...HEAD`) touches `cmd/funcdctl/dev.go`, `dev_app.go`, `dev_reload.go`,
`dev_settle_test.go` and `dev_test.go`. The issue is "`funcdctl dev` reloads a build its tool is still writing".

The review added two commits, f273e91a and 64183528 (below). It closed both Minors and fixed one defect that was
already on main.

### Minor
- **devSettle's `pending` guard in folder mode's reload had no test** · attribution: model · evidence: mutant M4
  (`if !forced && !s.settle.pending && !s.changed()` → `if !forced && !s.changed()` in `dev_reload.go`) left every
  test of the fix passing (`ok`, 7.0 s).
  - The guard holds a reload that `reloadFile` reported (an envFile change) while `devSettle` waits. It is needed
    once a reload has read a new Function: the next poll's stamp then differs from the one devSettle holds, so the
    envFile reload waits one poll, and on that poll `reloadFile` reports nothing.
  - A `funcdctl.config.yaml` change does not show the gap, because the file is one of the folder's stamped files,
    so `changed()` sees it anyway.
  - Fixed in 64183528: `TestDevSettleKeepsAConfigReload` changes `.env` after a reload that read a new Function. It
    passes 3 of 3 under `-race`, and fails against M4 (the envFile value never served, 20.8 s).
- **reload's doc comment ran past 120 columns** (`dev_reload.go`, the line ending "retried on the next change; an
  apply that") · attribution: model · evidence: a line width of 165. Rewrapped in 64183528.

### Found defect, already on main (not the fix's)
- **Dev reloaded the folder at every poll once a Function was added after it started** · attribution: none (on main
  42897d43 too) · evidence:
  - `prepare` (`dev.go`) runs no Function or step added after boot and warns "restart funcdctl dev to run a Function
    added after it started".
  - But `changed()` compared that build's fingerprint with `s.seen`, which dev never sets for it. Every 300 ms poll
    therefore reloaded the folder and printed the warning again.
  - On the branch before the fix, `TestDevReloadLeavesAFunctionAddedAfterStartAlone` sees the warning 6 times in
    6 polls, where 1 is expected.
  - Fixed in f273e91a: `changed()` leaves out the builds dev did not start. With the fix the test passes 3 of 3
    under `-race`.
  - App mode has no such case: its watch loop stamps the whole input tree (`inputsStamp`).

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reason.** With main's `dev.go`, `dev_app.go` and
  `dev_reload.go` overlaid (and `TestDevSettle`, which needs `devSettle`, left out of the overlay), both
  `TestIssue1118_*` tests fail with "expected: 2, actual: 6". That is a revision or generation per poll while the
  tool writes: dev reloads a build in progress.
  - `TestDevReloadSeesANewFileFixedAfterAFailedLoad` fails on main after its 20 s wait. The fixed file is never read
    again.
- **They pass with the fix.** All four of the fixer's tests pass 3 of 3 under `-race` (the two #1118 tests take
  2.3-3.7 s each, the new-file test 1.5 s).
- **Mutants.**
  - M5, which drops the listing's `*.yaml` stamps in `currentStamps`, fails the new-file test.
  - M6, which bypasses devSettle in App mode's watch loop, fails `TestIssue1118_AppDevWaitsForABuildToSettle`.
  - The fixer reports that `TestDevSettle` kills 3 mutants of `devSettle.ready`. Its table checks each branch of
    `ready`: a change at a settled poll, a change while the inputs still move, and no change.
  - M4 is the Minor above.
- **The fix addresses the cause.** Both loops acted on the first poll whose inputs differed (`dev_app.go` watch,
  `dev_reload.go` reload). They now act only once a change was seen and the inputs read the same on two polls in a
  row, which is the decider's rule recorded in the issue.
  - The fix adds no timeout and no retry, and it swallows no error.
  - A forced reload with inputs at rest still runs at once: `ready` sees the same stamp as at the last poll.
- **Scope.** Every hunk serves #1118. The one exception is 8c5cebb3, a separate defect with its own commit and
  test, as the found-defect rule asks. No test was weakened or deleted.
- **Reuse.** `pollStamp` digests `currentStamps` (already there) and `devBuild.fingerprint`. `devSettle` is one small
  type that both loops share, with no copy in either.
- **ADRs.** ADR-0247 Decision 8 and ADR-0253 Decision 9 fix the 300 ms poll. Neither says a change reloads on its
  first poll, and the settle rule is the decider's choice recorded in the issue. No ADR file is edited.
- **Conventions.** The `fix(funcdctl):` subjects carry the attribution trailer, and `Fixes #1118` is in 820a0e88. The
  imports are at the top, and the why-comments name #1118. vet and lint report 0 issues with and without
  `-tags dev`.
- **Checks.** `go test -tags dev -race ./cmd/funcdctl/` passes (102.6 s) and `go test ./cmd/funcdctl/` passes
  (7.8 s), both on 64183528. The repo-wide gate runs once on the final head, before the PR.

### Definition of Done
11 / 12 items hold. The miss is item 4: mutant M4 survived the fixer's tests (model, closed in 64183528).

### Model scorecard
Recorded: claude-opus-5-5 on #1118 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/12.

### Recommendation
Ready for the PR, with the review's two commits. Record the found defects (8c5cebb3 and f273e91a) in a PR comment.
