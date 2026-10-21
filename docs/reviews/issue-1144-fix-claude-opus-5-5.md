## Verdict: changes-requested — 0 blockers, 1 major, 2 minors  (issue #1144 task, model: claude-opus-5-5)

This report reviews branch `task/1144-gate-stress`, one commit 21fbb653 on main a14ce0f1:
`feat(gate): stress-run the tests a branch adds or changes`. It adds `scripts/agent/stress.py`, a stress step in
`scripts/agent/gate.sh` and `scripts/stress_test.go`. #1144 is a task, not a defect, so the bar is its "Done when",
with `/fix-review`'s discipline.

### 🟡 Major
- **A test file the branch renames and changes is never stress-run** · attribution: model · evidence: a throwaway
  repository where the branch runs `git mv a/old_test.go a/new_test.go` and edits a line of `TestOld`.
  `stress.py --base main --head renamed --list` prints nothing and exits 0, so the gate prints SKIP.
  - `changed_lines` runs `git diff -U0 --diff-filter=AM`. With rename detection on (git's default), the file is an `R`
    entry, which the filter drops.
  - A test moved to another file while it is changed is a usual refactor, and it is exactly the kind of change whose
    timing the step exists to check.
  - Fix: pass `--no-renames`, so a renamed file reads as added and all of its tests are listed, or keep the renames
    and include `R`, so only the changed lines are listed. Add the case to `TestStressRun`.

### Minor
- **No test proves that a failure after the first run fails the step** · attribution: model · evidence: mutant S4
  judges a test by its first run alone (`ok = runs[0][0] == "pass"`), and `TestStressRun` still passed. `TestRacy`
  fails about half its runs at random, so whether its first run fails is a coin flip, and the test cannot tell the
  two cases apart.
  - Fix: add a fixture test that passes its first run and fails a later one deterministically, for example with a
    counter file under `t.TempDir()`'s parent or an environment variable the run sets.
- **`gate.sh`'s header comment runs to 181 columns** (line 9) · attribution: model. Rewrap it to 120.

### Open items (not scored)
- **CLAUDE.md**: the issue's Details ask to update CLAUDE.md where it describes the gate. The fixer did not edit it,
  following the rule that a session edits no CLAUDE.md at a peer's request. The DR session or the user owns that edit.
- **Done when, "the PR shows the step's runs and timings on a real branch"**: this waits for the gate run on the final
  head (env).

### ✅ Verified correct (keep it)
- **Which tests are listed.** Each test is found from the head-side lines of the `-U0` hunks:
  - a deletion-only hunk (`+N,0`) counts as head line N;
  - each top-level `Test…(*testing.T)` spans from its doc comment's first line to its closing `}` at column 0, so
    `TestMain` and benchmarks are left out;
  - the build tags come from the file's `//go:build` line (`dev`, `e2e` and `integration`, not when negated).

  Mutants S1 (no deletion-only hunk), S2 (no doc comment) and S3 (no tags) each fail `TestStressRun`.
- **The run.**
  - The host check runs before each test, and a failed check stops the step (exit 2).
  - A test runs alone (`-run ^Name$`), first `-race -json -count=1` to time it, then up to `--count` runs in all,
    scaled by the remaining budget over the remaining tests. Each run's timeout scales with its count.
  - A test that does not build fails the step, and one its build constraint excludes on this platform is reported and
    skipped.
  - Each test gets one line with its run count, failures and timings. Exit codes: 1 for a failed run, 2 for a tool
    error.
- **The gate step.** It runs after linux-tests and before tree-unchanged. It prints SKIP when `--list` is empty and
  FAIL when `--list` fails, and `bash -n` passes on the script.
- **The test.** `TestStressRun` drives the script on a fixture repository through `audit_test.go`'s `fixtureRepo`
  (reused, not copied), and neutralizes the host check (`FUNCD_ORPHAN_TEST_SUFFIX`, `FUNCD_TIME_WAIT_COUNT`), so it
  kills nothing.
  - It covers the list (a doc-comment change, a deletion-only hunk, an e2e file; not an unchanged test or a helper),
    the no-test branch, and `TestRacy` failing the step within a 60 s budget.
  - It is `t.Parallel` and forks python and go. That is safe here: the package's tests that write and exec scripts
    (`devshell_test.go`, `guards_test.go`) are serial, and Go starts parallel top-level tests only after the serial
    ones.
- **Checks.** `go test -race -count=3 -run TestStressRun ./scripts/` passes (5.8 s), and so does
  `go test -race ./scripts/` (33.7 s). vet and `golangci-lint run ./scripts/` report 0 issues.

### Definition of Done
- The four "Done when" items: 2 / 4 hold. The step's SKIP on a no-test branch and the timing race failing it within
  the budget are proven. "The gate runs the step" holds in code and waits for a gate run, as does the PR evidence
  (env).
- The checklist items that apply: 10 / 12. The misses are item 4 (mutant S4 survives) and item 12 (a case of the
  issue's "the Test functions the branch adds or changes", a renamed file, is missed).

### Model scorecard
Recorded: claude-opus-5-5 on #1144 (task) → changes-requested, 0/1/2, 3 model-attributed, DoD 10/12.

### Recommendation
Changes requested: pass `--no-renames` (or include `R`) with a test for it, add a deterministic later-run failure to
the fixture, and rewrap the header comment. Then run the gate once on the final head; its stress step is the PR's
evidence.
