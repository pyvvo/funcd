## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #515 fix, model: claude-opus-5-5)

Change: branch `fix/i515`, commit `bca5f53` `fix(funcd): state the 256 KiB default payload cap in the WithWorkflow doc`.
Touched: `pkg/funcd/options.go` (doc comment only) and the new `pkg/funcd/options_doc_internal_test.go`.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit bca5f53` with the test file
  kept: `TestIssue515_WithWorkflowDocStatesDefaultPayloadCap` FAILS, and the message shows the doc text
  `... one attempt, 1 MiB payload cap ...` that `does not contain "256 KiB payload cap"`. After
  `git reset --hard bca5f53`, the test PASSES under `-race`. The worktree is clean at `bca5f53`.
- **Mutants (overlay, `-run TestIssue515`), 3 of 3 killed:**
  1. doc says `128 KiB payload cap` → FAIL (the doc no longer matches the constant).
  2. doc keeps 256 KiB but adds a stray `1 MiB` elsewhere → FAIL (the `NotContains` check catches it).
  3. `defaultWorkflowPayloadLimit = 1 << 20` in `pkg/funcd/funcd.go`, doc unchanged → FAIL. The test
     derives the expected text from the constant, so the doc and the code can't drift apart again.
- **Cause, not symptom.** The issue names a stale comment as the cause (`options.go:136`, left over
  from #343). The fix corrects that comment to the value of `defaultWorkflowPayloadLimit = 256 << 10`
  (`pkg/funcd/funcd.go:103`), which matches ADR-0094 ("default 256 KiB"). No behavior change, and none is needed.
- **Scope.** Two hunks: the comment rewrap and the regression test. Nothing unrelated changed, and no test
  was weakened. A grep for other stale `1 MiB` payload-cap mentions in Go files and living docs found none.
  The one hit, `internal/workflow/reconcile_run_test.go:307`, is about the in-memory run store's value
  limit, not the payload cap.
- **Reuse.** The test embeds `options.go` with `//go:embed` and parses its doc with `go/parser`. That is
  the idiom the repo already uses for doc-drift tests: `cmd/funcd/main_test.go`,
  `cmd/funcdctl/dev_doc_test.go`, `cmd/funcdctl/workflow_test.go`, and the whitespace-normalised
  comparison in `internal/secrets/secrets_test.go`. `internal/testkit` has no shared helper for this.
  Each such test has to embed its own package's source, so a per-package helper is the right shape.
  The expected value comes from the existing constant, not a duplicated literal.
- **Conventions.** Imports are at the top level. The embed comment states a real *why* (the overlay revert
  check). The test name follows `TestIssue<N>_…`, and testify `require` matches the package's tests. ADR-0002
  is not affected (test-only code plus a comment).
- **ADRs.** No ADR file was edited. The comment now agrees with ADR-0094.
- **Checks (touched package):** `go vet ./pkg/funcd/` ok; `golangci-lint run ./pkg/funcd/` 0 issues;
  `go test -race -count=1 ./pkg/funcd/` ok. The repo-wide set, Linux lint and e2e are left to the group gate.
- **Shape.** The subject is `fix(funcd): …`, the body has `Fixes #515`, and the commit carries the
  attribution trailer. One issue per commit.

### Recommendation

Pass. Hand back to `/fix` Step 8. The group gate runs the repo-wide checks.
