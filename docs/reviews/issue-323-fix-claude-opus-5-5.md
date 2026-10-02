# Fix review — issue #323 (SDK maps 413 and 429 to fault.Internal)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #323 fix, model: claude-opus-5-5)

Change: `fix/i323`, one commit `95ac700 fix(sdk): map 413 and 429 responses to PayloadTooLarge and ResourceExhausted`
(`pkg/sdk/sdk.go` +4, `pkg/sdk/sdk_test.go` +31).

### 🔴 Blocker

None.

### 🟡 Major / Minor

None.

Observation, not scored (pre-existing, outside the issue's scope): `problemToFault` in `pkg/sdk/sdk.go` is a
hand-written inverse of `kindProblem` in `api/fault/problem.go`. The two tables can drift again if a new
Kind is added. A status-to-Kind function in `api/fault` would make one table the source of both. That is a
refactor and a possible follow-up issue, not a defect of this fix.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the reason in the issue.** After `git revert --no-commit 95ac700`, with
  the test file kept, `go test -race -run TestIssue323 ./pkg/sdk/` fails all three subtests with
  `expected: "payload_too_large"` / `"resource_exhausted"`, `actual: "internal"`. This includes the
  `control plane body limit` subtest, which sends a 2 MiB ConfigMap to the real control plane and gets the
  huma 413. That subtest reproduces the issue's own steps end to end.
- **The test passes with the fix** under `-race` (all three subtests). After `git reset --hard` the worktree
  is back at the start HEAD and clean.
- **Cause, not symptom.** The issue names the missing 413 and 429 cases in the `problemToFault` switch. The fix
  adds exactly those two cases. They map to the Kinds that `api/fault/problem.go` maps to 429 and 413, so
  the switch is now the inverse of the Kind-to-status table.
- **Mutants (3 of 3 killed).** (1) 429 mapped to `Internalf`: the test fails. (2) 413 mapped to `Invalidf`:
  the test fails. (3) 413 merged into the 429 case: the test fails.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The fix uses the existing `fault.ResourceExhaustedf` and `fault.PayloadTooLargef` constructors
  and the `net/http` status constants. The test uses the package's existing `newClient` harness and
  `fault.WriteProblem` for the stub server. It adds no new helper.
- **Conventions (ADR-0002).** The errors are `api/fault` kinds with the same `"sdk"` op and `%s` message shape
  as the neighbouring cases. The imports are at the top of the file. There is one short "why" comment on the
  test and no comment bloat. The test uses no data dir, so the #41 socket-path rule does not apply.
- **ADRs.** No ADR file was touched. The change matches ADR-0002, where `api/fault/problem.go` is the single
  Kind-to-status map.
- **Checks on the touched package.** `go test -race ./pkg/sdk/` passes, `go vet ./pkg/sdk/` is clean, and
  `golangci-lint run ./pkg/sdk/` reports 0 issues. The repo-wide checks, Linux lint and e2e are left to the
  group gate.
- **Commit shape.** The subject is `fix(sdk):`, the body has Cause/Fix/Test, it carries `Fixes #323` and the
  attribution trailer, and the commit covers one issue.

### Definition of Done

11 of 11 applicable items hold. Item 8 was checked for the touched package on the host. The repo-wide and
Linux parts of item 8 are left to the group gate.

### Model scorecard

| issue | phase | model | verdict | blockers | majors | minors | model-attributed | DoD |
|---|---|---|---|---|---|---|---|---|
| 323 | fix | claude-opus-5-5 | pass | 0 | 0 | 0 | 0 | 11/11 |

### Recommendation

Pass. Hand back to `/fix` Step 8. If the group wants it, file a separate issue to derive the SDK's
status-to-Kind mapping from `api/fault` so that the two tables cannot drift.
