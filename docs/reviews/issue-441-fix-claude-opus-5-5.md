## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #441 fix, model: claude-opus-5-5)

Change: branch `fix/i441`, commit 99f9cd6 `fix(funcd): name the TLS storage dir the daemon really uses`
(`cmd/funcd/main.go` +2/-2, `cmd/funcd/main_test.go` +16).

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The new comment is pinned to text, not to the code it describes** · attribution: model · evidence: a
  mutant that changes `StorageDir: filepath.Join(cfg.Storage.DataDir, "tls")` to `"tls-state"` in
  `cmd/funcd/main.go` leaves the comment unchanged, and `go test -count=1 ./cmd/funcd/` still passes
  (`ok github.com/pyvvo/funcd/cmd/funcd`). The comment and the code can drift apart again, which is how
  #441 arose. · fix (optional): have `TestIssue441_…` also assert that the TLS `StorageDir` that
  `buildOptions` sets is `<dataDir>/tls`, or check the literal `"tls"` in the AST next to the comment.
  This is outside the issue's "Done when", so it does not block.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason**: with the `origin/main` version of
  `cmd/funcd/main.go` in place, `TestIssue441_TLSCommentNamesTheDaemonStorageDir` fails with
  `does not contain "<storage.dataDir>/tls"` and `should not contain "funcd-tls"`.
- **Passes with the fix** under `-race`: `--- PASS: TestIssue441_TLSCommentNamesTheDaemonStorageDir`.
- **Mutants on the fix's key lines**: (1) re-adding the `funcd-tls` wording to the comment → FAIL;
  (2) naming `<storage.dataDir>/certs` instead of `/tls` → FAIL. A third mutant on the untouched code
  line survived (see the Minor).
- **Cause, not symptom**: the issue is a wrong comment; the comment now states the fact the code sets
  (`StorageDir: filepath.Join(cfg.Storage.DataDir, "tls")`). The claim in the issue was confirmed: the
  platform default `funcd-tls` lives in `pkg/funcd/funcd.go` under a different base and never applies
  to the daemon, which always sets `StorageDir`.
- **Scope**: two hunks, both for the issue. No test was weakened or deleted.
- **Reuse**: the test reuses the existing `//go:embed main.go` `mainSource` and the `parser.ParseFile`
  comment-scan pattern of `TestIssue329_BuildFuncsCarryTheirOwnDocComment` in the same file; no new
  helper, harness or dependency.
- **Conventions**: top-level imports, no comment bloat (a two-line why-comment on the test), naming
  matches the `TestIssue<N>_…` convention. ADR-0111 is not contradicted; no ADR file was edited.
- **Checks (touched package)**: `go test -race -count=1 ./cmd/funcd/` → ok; `go vet ./cmd/funcd/` →
  exit 0; `golangci-lint run ./cmd/funcd/...` → `0 issues.` The repo-wide, Linux-lint and e2e checks
  are left to the group gate.
- **Shape**: `fix(funcd):` subject, `Fixes #441`, attribution trailer, one issue in one commit.
- The worktree was left at 99f9cd6 and clean.

### Definition of Done

11 / 11 items hold (item 8 for the touched package; the group gate runs the repo-wide and Linux checks).
Misses: none. The Minor above is a test-strength observation beyond the issue's scope.

### Model scorecard

Ledger fields (not recorded by this run): claude-opus-5-5 on issue #441 (fix) → pass, 0/0/1,
1 model-attributed, DoD 11/11.

### Recommendation

Ship as is. Optionally strengthen the regression test so it also pins the code's `StorageDir`, not
only the comment text.
