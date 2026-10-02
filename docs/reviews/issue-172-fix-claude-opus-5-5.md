## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #172 fix, model: claude-opus-5-5)

Change: `fix/i172`, commit 6683f4a `fix(workernode): answer an over-cap local API body with 413, not 400`
(`git diff origin/main...HEAD`: `internal/workernode/local/{local,kv,blob}.go` and the new
`local_internal_test.go`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The per-handler cap value is not pinned at its boundary** · attribution: model · evidence: mutant M3
  (`blob.go`: `readBody(w, r, op, maxBlobBytes)` changed to `maxKVBytes`, so the blob cap drops from 64 MiB to 1 MiB)
  survives: `go test -run 'TestIssue172|TestScenarioBlob' ./internal/workernode/local/` returns `ok`. The
  regression test sends `limit+1` bytes using each handler's own constant, so any smaller cap still yields 413,
  and `TestScenarioBlobSizeCap` only checks a 2-byte within-cap body. The gap existed before the fix, which
  rewrote that line but kept its argument. Fix (optional): add a within-cap case at exactly `limit` bytes for each row
  of the table test.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `local.go`, `kv.go` and `blob.go` restored from
  `origin/main` and the new test kept, `TestIssue172_OverCapBodyIs413` fails in all three subtests:
  `expected: 413` / `actual: 400` (invoke, kv put, blob put). This matches the issue's
  `400 urn:funcd:problem:invalid … http: request body too large`. (A plain `git revert --no-commit 6683f4a` also
  deletes the test file and reports `[no tests to run]`, so the check was run with only the non-test files reverted.)
- **Passes with the fix under `-race`:** `go test -race -count=1 -run TestIssue172 -v` passes all three subtests.
  `go test -race ./internal/workernode/...` returns `ok`.
- **Root cause, not symptom.** The new `readBody` (`local.go:146-158`) maps `*http.MaxBytesError` to
  `fault.PayloadTooLargef` (413) and keeps every other read failure as `Invalid`. This is the mapping the issue names
  (`local.go:89-91`). The fix also covers the related KV-put and blob-put sites the issue lists, and ADR-0127
  ("over-cap→413") is now met for blob. No timeout, retry or swallowed error was added.
- **Mutants.** M1 (`errors.As` branch disabled): all three subtests fail. M2 (`MaxBytesReader` limit `+1`):
  `TestIssue172…` and `TestScenarioBlobSizeCap` fail. M3: survives (see the Minor finding). Every mutant was restored, and the tree is clean.
- **Scope.** Every hunk serves the issue: the three read sites, the shared helper, and a one-line doc-comment
  correction on `registerKV` that records the new 413. No test was weakened or deleted. Nothing outside the
  package asserts the old detail strings (grepped the Go, YAML and Markdown files).
- **Reuse.** The test reuses the existing `zeroReader` and `capFakeBlob` from `blob_internal_test.go` and adds no new
  fake. The helper replaces three copies of the same read with one function. It mirrors the data plane's inline
  mapping (`internal/dataplane/dataplane.go:166-172`) exactly, using the same `fault.PayloadTooLargef` and the same
  message shape. No shared HTTP-body helper exists in `internal/platform` or `api/fault`, and moving the logic into
  one across packages would go beyond the issue's scope. This is an acceptable local helper, not a reinvention.
- **Conventions (ADR-0002, CLAUDE.md).** Errors are `api/fault` and are written with `fault.WriteProblem`. The
  helper's arguments follow the handler style (`w, r`, then `op`). There is no `any`, all imports are at top
  level, and the doc comment is short and cites ADR-0134. The unused `io` import was dropped from `kv.go`.
- **ADRs.** The fix contradicts no Accepted or Implemented ADR and edits no ADR file. It brings the local API in
  line with ADR-0134 (the data plane's 413) and ADR-0127 (blob over-cap→413). ADR-0064 does not set this status.
- **Checks (touched package).** `gofmt -l` is clean, `go build ./...` passes, `go vet ./internal/workernode/local/`
  passes, and `golangci-lint run ./internal/workernode/local/` reports `0 issues.` (e2e, Linux lint and lanes are left to the group gate.)
- **Shape.** The subject is `fix(workernode): …`, the body has `Fixes #172` and the Co-Authored-By trailer, and the
  commit covers one issue.

### Definition of Done
11 / 11 items hold for the scope reviewed here (item 8 covers host build, vet, lint and tests for the touched package; Linux lint
and e2e are deferred to the group gate). No misses. The M3 survivor does not hit a key line of the 413 mapping, so
item 4 holds.

### Model scorecard
Not recorded by this gate run (the orchestrator records the ledger row): claude-opus-5-5 on issue #172 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship it. The optional follow-up is an exact-`limit` within-cap case in the table test, which pins each handler's
cap value.
