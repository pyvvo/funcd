## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1158 fix, model: claude-opus-5-5)

Commit 6d1b0dbe on `fix/flakes-1142` (main ccdf5887) adds `unowned()` to `pkg/funcd/rootentries_e2e_test.go`. The
helper strips the owner and the resourceVersion. The commit uses it in `TestScenarioUserCannotReplaceOwnedStore` and
adds the e2e test `TestIssue1158_OwnedStoreRefusedAfterTheOwnerWritesIt`.

The branch was rebuilt on ccdf5887, which adds only #1153's docs. `git range-diff` shows 6d1b0dbe's patch is the same as
edb78283's, on which the checks below ran.

### ✅ Verified correct (keep it)
- **Mutant: keep the version.** With `unowned` keeping the resourceVersion, the new test fails with CI's error: `sdk:
  controlplane.precondition: KVStore default/counters has changed since resourceVersion …`.
- **The regression is forced.** The owner rewrites its store between the read and the apply: a re-apply of
  `counter` with a second table, which the test waits for.
- **Cause, not symptom.**
  - The API checks a stale-version precondition before it checks ownership. A takeover sent with the version it read
    could therefore get the precondition error instead of the ownership refusal.
  - ADR-0210 makes the precondition optional (`If-Match` or a body `metadata.resourceVersion`, line 181). So a request
    with no version reaches the ownership check, which is what the tests assert.
- **Speed.** Both tests call `t.Parallel()` and take 0.11 to 0.12 s.
- **Checks.**
  - `go test -race -count=1 -tags e2e -run 'TestIssue1158_|TestScenarioUserCannotReplaceOwnedStore$' ./pkg/funcd/`
    passes.
  - The package's non-e2e tests pass under `-race` (26.4 s).
  - `go vet` passes with and without `-tags e2e`, and `golangci-lint` reports 0 issues with `--build-tags e2e`.
- **Shape.** The subject is `test(funcd):` (test-only), with `Fixes #1158` and the trailer. No ADR is edited.

### Definition of Done
12 / 12.

### Ledger
Batch mode: the batch's ledger PR records this review.

### Recommendation
Pass.
