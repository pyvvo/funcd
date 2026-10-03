## Verdict: pass — 0 blockers, 0 majors  (issue #576 fix, model: claude-opus-5-5)

Change: branch `fix/w7-i576`, commit 4089443 `fix(agents): polish the stash hook and lane lock edge cases`, touching
`scripts/agent/no-stash.py`, `scripts/lane-lock.sh` and `scripts/guards_test.go`. Issue kind: task; the target is its
"Done when" section, with the decision that every case gets a test case in `scripts/guards_test.go` and the guards'
core behavior stays as it is.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observation, not scored (pre-existing, outside the issue): `time -p git stash pop` is not blocked. `time` is in
`RESERVED`, so its `-p` option ends the command-position scan. The `origin/main` hook behaves the same way, so this is
not a regression. A follow-up could treat `time` as a wrapper whose options are skipped.

### ✅ Verified correct (keep it)
- **Revert check.** The tests read the scripts at runtime, so `go test -overlay` cannot reach them. The check ran in
  a scratch worktree of `origin/main` with the branch's `scripts/guards_test.go` copied in, running
  `go test -run TestIssue576 -v ./scripts/`. Both new tests fail, each for the issue's reason:
  - `TestIssue576_StashHookReadsHeredocsAndWrappers`: `"function f { git stash pop; }" must be blocked`, exit 0.
  - `TestIssue576_LaneLockReportsItsGuard`: all four subtests fail. With a directory at the lock path, a stray link
    is left inside it. When the lock dir vanishes during a takeover, the script exits 1 instead of 0. The held-guard
    subtest fails because the old script has no bounded guard wait. When the guard cannot be created, the old script
    prints "has been held for two minutes" instead of "could not take the guard".
- **With the fix.** `go test -race -count=1 ./scripts/` passed in 25.8 s, with the #560/#561/#562 guard tests
  unchanged and passing.
- **Mutants** (in a scratch worktree of the branch; each one killed):
  1. Dropping `return 0` after `rmdir "$guard"` in `remove_if` makes the `lock dir vanishes during a takeover`
     subtest fail.
  2. Disabling the `function` branch in `blocked_segment` fails the hook test on `function f { git stash pop; }`.
  3. Forcing `shell = False` (heredoc bodies always read as data) fails the hook test on
     `bash <<'EOF' … git stash pop … EOF`.
- **Done when, read and probed directly.**
  - Commands that only mention `git stash` now pass. These include the `git commit -m "$(cat <<'EOF' … EOF)"` form,
    `git commit -F - <<'EOF'`, a heredoc containing an apostrophe, and `timeout 5 echo git stash`.
  - The `function`, `watch '…'`, `watch -n 5 …`, shell-fed heredoc (`bash <<'EOF'`, `cat <<'EOF' | sh`) and
    unquoted-heredoc `$(…)` forms are blocked.
  - An extra probe of 18 forms found no regression against `origin/main`. Commands still blocked include
    `env -i`, `sudo --`, `nix develop -c`, `timeout -s KILL 5`, `xargs -I{}`, `exec`, `f() { …; }`, an unterminated
    heredoc, a multi-line quote followed by `&& git stash`, and a `<<-` heredoc with tabs. Commands that still run
    include `sh <<EOF` with only `git stash list`, and `watch -d 'git stash list'`.
  - The lock checks for a directory before `ln -s` and creates the lock dir on every try. `remove_if` returns 0
    after `rmdir`. The two guard messages are told apart by `[ -d "$guard" ]`. `until` is renamed to `give_up`.
- **Core behavior unchanged.** The block message, the read-only allow-list, the unbalanced-quote regex fallback (it
  now applies only when a quote never closes) and the lock's take/wait/nested logic are as before.
  `FUNCD_LANE_GUARD_WAIT` defaults to 120, so behavior is the same in production. It is documented next to the other
  test-only knobs.
- **Reuse.** The test refactor moves the per-test `run` and `bash` closures into the shared `stashHook` and `bash`
  helpers instead of duplicating them. The new tests use the existing `laneEnv`, `newLaneEnv`, `staleLock`, `holder`,
  `readLog` and `exitCode` helpers. The hook uses only the standard library (`shlex`, `re`). The new `options_end`
  replaces the hand-rolled git-option loop instead of adding a second one.
- **Conventions.** Top-level imports, no comment narration (the comments state the why: the `WRAPPERS` value
  semantics and the fallback direction), no YAML touched, and no ADR touched.
- **Checks on the touched package.** `go vet ./scripts/` was clean, and `golangci-lint run ./scripts/...` reported
  0 issues.
- **Shape.** The commit has the subject `fix(agents):`, includes `Fixes #576` and the attribution trailer, and
  covers one issue.

### Definition of Done
11 of 11 applicable items hold. The repo-wide and Linux checks belong to the group gate.

### Model scorecard
Not recorded by this review, as instructed. The ledger fields are: claude-opus-5-5 on issue #576 (fix), verdict
pass, 0/0/0 findings, 0 model-attributed, DoD 11/11.

### Recommendation
Pass. Hand the change back to `/fix` for integration. The `time -p` gap is optional follow-up material.
