## Verdict: pass — 0 blockers, 0 majors  (issue #512 fix, model: claude-opus-5-5)

Change: branch `fix/i512`, commit 8366b89 `fix(agents): drop the stale lint-lock failure from the shared-host note`.
It removes the lint-lock clause from the "host is shared" note in `.claude/CLAUDE.md` and in its mirror,
`.github/copilot-instructions.md`. It also adds `TestIssue512_AgentNotesDropTheLintLockFailure` in
`tests/lint-fixtures/lintrules_test.go`.

### 🟡 Minor
- **The regression test checks only the exact error text** · attribution: model · evidence: a mutant that adds
  the claim to `.claude/CLAUDE.md` in other words ("The tests/lint-fixtures tests fail while another lint holds
  the lock") passes the test (`ok tests/lint-fixtures`). Two mutants that use the quoted error text both fail
  the test. · Fix (optional): also match a second phrase, such as `lint holds the lock`. The present check
  catches the exact sentence from the issue, so this is not a blocker for a docs fix.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** `git revert --no-commit 8366b89` also removes the
  new test (`[no tests to run]`). So the run kept the test file and reverted only the two docs. With that
  setup, the test FAILS at `lintrules_test.go:174` for both files: "still says the lint-fixtures tests fail
  while another golangci-lint holds the lock". The worktree was then reset to 8366b89 and is clean.
- **The test passes with the fix**, un-skipped, under `-race`
  (`--- PASS: TestIssue512_AgentNotesDropTheLintLockFailure`).
- **Mutants.** M1: put the quoted claim back into `.claude/CLAUDE.md` → FAIL. M2: append the claim to
  `.github/copilot-instructions.md` → FAIL. M3: reword the claim → survives (see Minor).
- **Cause, not symptom.** The issue's claim holds. `lintFixture` passes `--allow-parallel-runners`
  (`lintrules_test.go:44`, #289). `.golangci.yml:121` sets `run.allow-parallel-runners: true` (#415). The
  regression tests for #289 and #415 are still in the file. The stale sentence is removed, not hidden.
- **Scope.** Three files changed, and every hunk serves the issue. The ephemeral-port advice stays, word for
  word, as "Done when" requires. Only line wrapping changed. The two agent files stay byte-identical (`cmp`).
  No test was weakened or deleted.
- **Reuse.** The test reuses the package's `repoRoot` helper and only standard-library calls. The repo has no
  existing check that reads the agent docs or keeps the two files in sync, so nothing is duplicated. The
  test sits next to the #289 and #415 lint-lock tests, which suits it.
- **Conventions.** Imports are at the top level. The test has one doc comment that gives the *why* (#289, #415).
  The name follows `TestIssue<N>_…`. The test needs no YAML and no `funcd.New` platform.
- **ADRs.** No ADR file was touched, and no Accepted or Implemented decision is contradicted. The living
  agent docs are now true again.
- **Checks (touched package).** `go test -race ./tests/lint-fixtures/` ok (9.9 s). `go vet` clean.
  `golangci-lint run ./tests/lint-fixtures/...` reports 0 issues. `gofmt -l` is clean. The Linux lint and the
  repo-wide gate are left to the group gate.
- **Shape.** The subject is a conventional `fix(agents):`. The body states the cause, the fix and the test. It
  has `Fixes #512` and the attribution trailer, and the commit covers one issue.

### Definition of Done
11 / 11 items hold. Item 4 holds: the revert and two of the three mutants fail the test. The one surviving
mutant is the Minor above. Item 8 holds for the host checks of the touched package. The Linux lint and the
repo-wide run are deferred to the group gate by design. The PR shape is checked when `/fix` opens the PR.

### Model scorecard
Recorded: claude-opus-5-5 on issue #512 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.
The orchestrator writes the ledger row. This review did not write it.

### Recommendation
Pass. Hand back to `/fix` Step 8 to open the PR. Matching a second phrase in the test is optional and does not
block this fix.
