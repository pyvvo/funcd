## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #146 fix, model: claude-opus-5-5)

Change: branch `fix/i146`, commit `1e3150b` "fix(activator): give a re-created function a full idle grace
window and forget deleted ones". Files: `internal/activator/activator.go`, `internal/activator/activator_test.go`,
`internal/activator/export_test.go` (new).

### 🟡 Major / Minor

- **Minor — mutant survivor: `touch` keeping the attributed UID is untested** · attribution: model.
  Mutant: in `touch` (`internal/activator/activator.go:214`), replace `e := a.lastActive[fn]` with
  `e := activity{}`, so every request clears the UID. Result: `go test -run 'TestIssue146|TestScenario|Reclaim'
  ./internal/activator/` → `ok`. Effect of the mutant: each served request makes the next reclaim pass re-seed
  the entry at "now", so idle reclaim fires up to one reclaim interval late. The impact is small, and the delay
  only grants extra grace. Fix: add an assertion that a function served after a reclaim pass is reclaimed exactly
  `idleTimeout` after its last request, not after the next pass.
- **Minor — `forgetAllBut` hand-rolls `maps.DeleteFunc`** · attribution: model.
  `internal/activator/activator.go:364-372` loops and deletes from `a.lastActive`. Go 1.26 (`go.mod`) ships
  `maps.DeleteFunc(a.lastActive, func(fn FunctionRef, _ activity) bool { _, ok := live[fn]; return !ok })`.
  The loop is 4 lines, so this is a trivial polish, not a defect.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 1e3150b` on the code only (the new
  test and `export_test.go` kept), then `go test -race -run TestIssue146 ./internal/activator/` → `FAIL`: both
  `recreated-gets-full-grace-window/observed=false|true` report `Should be empty, but was [0]` ("the re-created
  function must not inherit its predecessor's activity"). The issue reports the same `ScaleTo` result,
  `[0]` on the first pass. `deleted-is-forgotten` fails "only the live function stays tracked", which is the
  leak. Then `git reset --hard 1e3150b`: the worktree is clean at that HEAD.
- **Passes with the fix:** `go test -race -count=1 ./internal/activator/` → `ok` (1.7s). Nothing is skipped.
- **Mutants:** M1, which drops `|| e.uid != uid` in `seenAt`, fails `TestIssue146`. M2, which removes the
  `a.forgetAllBut(live)` call, fails `TestIssue146`. M3 survived (see the first Minor). The file was restored
  after each mutant.
- **Cause, not symptom.** The tracker entry is now tied to the Function's UID (`activity{at, uid}`), so a re-created
  name is re-seeded with a full grace window, as ADR-0016 §4 requires ("seeded at first observation"). Every
  pass drops entries for functions that no longer exist, which closes the leak. The prune also removes entries
  that `touch` creates for names that never existed. The prune covers every listed Function, including those
  with `minReplicas != 0`, so live non-reclaimable functions keep their entries. A `List` error returns before
  the prune, so a failed pass does not wipe the tracker.
- **Race and edge cases reasoned through.** If a `touch` runs during a pass for a function created after `List`,
  the prune can drop that entry. The next pass then re-seeds it, which grants grace and never reclaims early.
  An entry seeded by `touch` (UID `""`) is re-seeded on its first pass. This only extends grace.
- **Scope.** All three files serve the issue. No existing test was weakened or deleted.
- **Reuse.** The fix uses the existing `stepClock`, `fakeScaler`, `createFunction`, `serve` and `newActivator`
  test helpers, plus the memory store. It adds no new dependency. `export_test.go` is the standard Go
  pattern for a test-only accessor, and it keeps the production API unchanged.
- **Conventions.** The UID type is `v1.UID`. The new code takes no `any`, logs nothing outside slog, and
  imports only at the top level. Comments are short and say why. Naming follows the file (`seenAt`,
  `touch`). `gofmt -l` is clean.
- **ADRs.** The change matches ADR-0016 §4 and edits no ADR file (`git diff --name-only` shows no `docs/adr` path).
- **Checks for the touched package:** `go vet ./internal/activator/` passes, and `golangci-lint run
  ./internal/activator/` reports `0 issues.` The repository-wide tests, the Linux lint and the e2e suite are
  left to the group gate.
- **Commit shape.** The subject is `fix(activator): …`, the body contains `Fixes #146`, and the
  `Co-Authored-By` trailer is present. The commit fixes one issue.

### Definition of Done

10 / 11 items hold. Item 4 (reverting or mutating the key lines fails a test) holds only in part: the revert,
M1 and M2 are killed, and M3 (the UID kept in `touch`) survives. This miss is the first Minor and is
`model`-attributed. Item 8 holds for the touched package. The Linux lint, the e2e suite and the lanes are
deferred to the group gate. Item 11 holds for the commit. The PR is not open yet.

### Model scorecard

Not recorded here: the orchestrator records the ledger row. Fields: claude-opus-5-5 on issue #146 (fix) →
pass, 0/0/2, 2 model-attributed, DoD 10/11.

### Recommendation

Ship it. The two Minors are optional polish for `/fix`: an assertion that `touch` keeps the UID, and
`maps.DeleteFunc` in `forgetAllBut`. Neither blocks the PR.
