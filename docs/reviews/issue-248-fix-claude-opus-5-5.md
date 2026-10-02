## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #248 fix, model: claude-opus-5-5)

Change: branch `fix/248-commit-msg-lint`, commit f987345 `fix(ci): stop commit-msg-lint failing on long commit messages`
(`scripts/commit-msg-lint.sh`, `scripts/commit_msg_lint_test.go`; +33/-2).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — the `q` in the new `sed` is not pinned by a test** · attribution: `model`.
  Mutant M1 dropped `q` from `sed -n '/^#/d; /^[[:space:]]*$/d; p; q'`. `go test -count=1 -run TestIssue248 ./scripts/`
  still returned `ok` (3.678s). Without `q`, the subject becomes every non-comment, non-blank line, and bash's
  `=~` lets `.+` run across the newlines. The script then accepts `fix: ` with an empty description when a body
  follows (exit 0; the fixed script exits 1), and an invalid long message prints a 20,008-line error instead of
  8 lines. The test checks only the exit status and a substring. Fix: also require that the error output quotes
  exactly the subject, for example `require.Contains(t, out, "\"not a conventional subject\"\n")`.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit HEAD` with the
  test file restored from HEAD: `--- FAIL: TestIssue248_CommitMsgLintIgnoresBodyLength` at
  `commit_msg_lint_test.go:25`, `exit status 141` (SIGPIPE) on the first valid-subject run. With `-count=20` it
  failed 20 of 20 times, which matches the issue's 20/20 repro. The worktree was then reset to f987345 and is clean.
- **It passes with the fix under `-race`.** `go test -race -count=1 -run TestIssue248 -v ./scripts/` → `PASS` (0.06s).
- **The root cause is fixed, not masked.** The issue names the cause: `grep | sed | head -n1` under
  `set -o pipefail`, where `head` exits after one line and the writer then fails on a closed pipe. The fix
  removes the pipe. One `sed` reads the file directly, prints the first non-comment, non-blank line and quits,
  so nothing can write into a closed pipe. `pipefail` was kept, no error is swallowed, and no retry was added.
- **The user-visible failure is gone, including the runner's exact mode.** On the GitHub runner SIGPIPE is
  ignored, so the writer gets EPIPE instead of being killed. With SIGPIPE ignored, the old script exits 4 with
  `sed: couldn't write … items to stdout: Broken pipe` under GNU sed (the issue's exit code 4), and exits 1 with
  `sed: stdout: Broken pipe` under BSD sed. The new script exits 0 for the long valid message and 1 with the
  usage text for the long invalid one, in both modes.
- **Behavior parity with the old script, GNU and BSD sed.** 23 message files were run through both scripts with
  GNU sed 4.9 and the macOS BSD sed, under bash 3.2 and bash 5.3, with SIGPIPE at its default and ignored
  (184 runs). Exit status and output are identical in 152 runs. The cases were leading comment lines, leading
  blank and whitespace-only lines (spaces and tabs), the `Merge`/`Revert`/`fixup!`/`squash!`/`amend!`
  exemptions, invalid subjects, a missing trailing newline, CRLF, a breaking `!`, an indented `#` line, and a
  5,000-line comment preamble. The 32 differing runs are of two kinds:
  - the long valid and long invalid messages: the old script fails with exit 141, 4 or 1; the new one returns
    the right result. This is the fix.
  - the empty file and a comments-only file: both scripts exit 1. The old one dies silently, because `grep -v`
    selects no line and `set -e` exits at the assignment. The new one prints the usage message with `""`. Same
    status, clearer output.

  A missing argument prints the same `usage:` message in both. A missing file fails in both (old exit 2; new exit
  2 under GNU sed and 1 under BSD sed).
- **lefthook and the pr-title workflow still work.** A scratch repo with a `commit-msg` hook that runs the script:
  the old script rejects a valid subject with a 20,000-line body, the new one accepts it, and it still rejects
  `not conventional`. The `merge_group` loop from `.github/workflows/pr-title.yml`, rerun with SIGPIPE ignored
  over a long-body commit, fails with the old script and passes with the new one. All 31 commit messages in
  `origin/main~30..HEAD` pass the new script. The file mode stays 100755, so both `scripts/commit-msg-lint.sh {1}`
  in `lefthook.yml` and the workflow's direct call still run it.
- **Mutants on the key lines fail a test.** M2 (drop the comment filter) → FAIL, M3 (drop the blank-line filter)
  → FAIL, M4 (drop `p`) → FAIL. M1 survived (Minor 1). The revert itself also fails the test.
- **Scope.** Two hunks: the `subject=` line with a two-line why-comment, and the regression test. No other file,
  test or ADR changed.
- **Reuse and placement.** The fix uses one `sed` in place of three processes. The test uses only the standard
  library and `testify/require`, which 197 other test files use. go.mod is unchanged. It is the first Go file under
  `scripts/`, so `go list ./...` now includes a test-only package `github.com/pyvvo/funcd/scripts`. Nothing forbids
  it: the `depguard` rules deny only mock frameworks, cross-layer imports and the bench libraries; `just check-hygiene`
  checks image placeholders, a build output and module versions; and `go build ./...` prints no warning. Putting the
  test next to the script it runs is ordinary Go practice. The test needs no `repoRoot` walk (as in
  `internal/platform/version/artifacts_test.go`), because `go test` runs in the package directory. The CI `ci` job
  runs `just ci` → `go test ./...` on every non-docs change, so the test runs in CI: `ok github.com/pyvvo/funcd/scripts`.
- **Conventions.** The `// Issue 248: …` doc comment matches `TestIssue24_…` and `TestIssue25_…`. Imports are at
  the top level, `t.Parallel()` and `t.TempDir()` are used, and there are no comments beyond the why.
- **ADRs.** No ADR governs the commit-message lint, and no ADR file was touched.
- **Checks.** `gofmt -l` clean; `go build ./...` and `GOOS=linux go build ./...` pass; `go vet ./...` passes on the
  host and on Linux; `golangci-lint run ./...` reports 0 issues on the host and on Linux (also 0 for `./scripts/`
  alone); `just check-hygiene` → `hygiene: clean`; `go test -count=1 ./...` → 80 packages `ok`, 0 failures.
  The e2e suite and the Lima lanes do not cover this path and were not run.
- **Shape.** The subject is `fix(ci): …` (and passes the new lint), the body names the cause, the fix and the
  regression test, `Fixes #248` is present, the attribution trailer is present, and the commit fixes one issue.

### Recommendation

Pass. Minor 1 is an optional one-line strengthening of the test's error-output assertion.
