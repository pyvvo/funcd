# Fix review — issue #321 (two doc comments in `cmd/funcdctl/dev.go` sit above the wrong function)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #321 fix, model: claude-opus-5-5)

Change: branch `fix/i321`, one commit `a8900f2 fix(funcdctl): put the bootDev and devShimOptions doc comments above their functions`
(`cmd/funcdctl/dev.go`, new `cmd/funcdctl/dev_doc_test.go`).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit a8900f2` with the test file kept
  from HEAD: `TestIssue321_DevDocCommentsSitAboveTheirFunctions` fails with "the doc comment of catalogAliases
  contains the doc comment of bootDev", the same for resolveInterpreter / devShimOptions, and "bootDev has no
  doc comment". These are the two drifts the issue names. The same failure appears with `origin/main`'s
  `dev.go` supplied through `go test -overlay`, so the test's claim that the embedded file honors `-overlay` holds.
- **Passes with the fix**: `go test -race -run TestIssue321 ./cmd/funcdctl` → ok after `git reset --hard a8900f2`;
  the worktree is back at `a8900f2`, clean.
- **Cause, not symptom.** The two comment blocks are moved verbatim, each directly above its function; the
  stray `//` separator line that tied the devShimOptions text to resolveInterpreter is gone. No comment text
  changed, no code changed.
- **The test runs in the default build.** `dev.go` is `//go:build dev`; the test has no build tag and reads the
  file through `//go:embed`, so it runs in `just ci` without `-tags dev`.
- **Mutants** (in place, restored after each):
  - a blank line between the bootDev comment and `func (a *cli) bootDev` (detaches the doc) → fails "bootDev has no doc comment";
  - the devShimOptions comment moved back above resolveInterpreter → fails "the doc comment of resolveInterpreter contains the doc comment of devShimOptions";
  - the bootDev comment reworded so it no longer starts with its name → fails "the doc comment of bootDev does not describe it".
- **Scope**: two moved comment blocks plus the new test; nothing else in the diff. No test weakened or deleted.
- **Reuse**: no existing doc-comment check exists in the repo (no other test parses source with
  `parser.ParseComments`, and no doc-comment linter is enabled in the golangci config); the test uses
  `go/parser`/`go/ast` from the standard library and the repo's `testify/require`.
- **Conventions**: imports at top level; one short why-comment per declaration; naming follows `TestIssue<N>_…`.
- **ADRs**: comment-only change to ADR-0125's `funcdctl dev` code; no Decision or Contract touched, no ADR file edited.
- **Checks** (touched package): `go test -race ./cmd/funcdctl` ok; `go vet` clean with and without `-tags dev`;
  `golangci-lint run ./cmd/funcdctl/` → 0 issues with and without `--build-tags dev`. Linux lint, the repo-wide
  suite and e2e are left to the group gate.
- **Shape**: `fix(funcdctl):` subject, `Fixes #321`, the attribution trailer, one issue in one commit.

### Observation (not a finding)

The test's first-word rule (no doc-comment line in `dev.go` may start with another function's name) is broader
than the issue. It could flag a future prose line that happens to begin with a function name; that is a cheap,
visible failure, and the rule is what catches the drift pattern in general rather than only these two sites.

### Definition of Done

11 of 11 items hold (item 8 at the host level for the touched package; Linux lint and e2e are run by the group gate).

### Model scorecard

claude-opus-5-5 — fix phase, issue #321: pass, 0/0/0, 0 model-attributed findings, DoD 11/11.
