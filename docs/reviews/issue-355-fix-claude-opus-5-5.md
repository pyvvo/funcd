# Fix review — issue #355 (claude-opus-5-5)

**Issue**: #355, "A pooled Function whose pool host hangs at boot stays Deploying forever"
**Change**: branch `fix/i355`, commit 7a65f19 `fix(function): fail a pooled Function whose pool worker never becomes ready`
**Producing model**: claude-opus-5-5
**Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor. Checklist 11/11.

## Summary

The issue names the cause: `convergePooled` passed a boot limit of 0 to `readyReplicas`, because a pool worker was
restarted in place (`runtime.Start` on the existing instance) and kept the `CreatedAt` of its first creation. The fix
removes that cause. `restartPool` now stops the worker and creates it again through `createPool` under the same
instance id, so `CreatedAt` is the boot time. `startPoolInstance` is deleted, the `!running` branch of `ensurePool`
(wake after a reclaim, or a crashed worker) uses `restartPool`, and `convergePooled` passes `bootTimeout`. A pool
worker that runs but never serves now ends its members Failed (ShapeInvalid) and stops the 200 ms poll, as a solo
replica does since #76.

## Verification run

| Check | Result |
|---|---|
| Regression test without the fix (`git revert --no-commit 7a65f19`, test file kept) | FAIL at `pool_test.go:412`: expected `Failed`, actual `Deploying`. This is the reason the issue reports. |
| Regression test with the fix, `-race`, whole `internal/function` package | `ok github.com/pyvvo/funcd/internal/function` |
| Mutant 1: `bootTimeout` → `0` in `convergePooled` | killed (`pool_test.go:412`, the member stays Deploying) |
| Mutant 2: `restartPool` restarts in place (rewrite the manifest, then `runtime.Start`) instead of `createPool` | killed (`pool_test.go:408`, the restarted worker is judged by its old `CreatedAt` and fails at once) |
| `go vet ./internal/function/` | clean |
| `golangci-lint run ./internal/function/` | 0 issues |
| Worktree after the review | reset to 7a65f19, clean |

The issue's steps describe a code path, not a cheap command sequence. Reproducing it with a real hung pool host
would need a daemon and a blocking handler, so the unit test is the evidence. The test's fake runtime matches the
process driver on the property the fix depends on: `Create` on a terminal instance replaces it and resets its
creation time (`internal/runtime/process/process.go`, the ADR-0142 replace branch).

## Findings

### Blocker

None.

### Major

None.

### Minor

None.

## Verified correct

- **Cause, not symptom.** The boot limit was not simply switched on. The fix first makes `CreatedAt` a correct
  boot time by re-creating the worker on every restart. Switching on the limit alone would have failed a
  just-restarted worker at once (mutant 2 shows this).
- **Reuse.** `restartPool` reuses `stopPool` and `createPool` (manifest write, schedule, create, start), so the
  separate rewrite-and-start logic is deleted rather than duplicated. Stop-then-Create under the same id is the
  pattern the solo path already uses (`internal/function/function.go`, the `replace` and `launch` loop), and the
  drivers support it (ADR-0142 replace). The fix adds no new helper, type or dependency. The test reuses
  `newShimHarness`, `withNodePool`, `hold` and `exitRevision`.
- **Scope.** Every hunk serves the issue: the boot limit, the restart path, the deleted `startPoolInstance`, and
  one regression test. No test was weakened or deleted. The full package passes under `-race`.
- **ADRs.** ADR-0046's rebuild contract still holds: one worker per key, the same instance id, and a new PID on
  restart. In a pass that has started serving, ADR-0142's rule still sets `failed` to empty, so a crash of a
  serving pool is repaired rather than failed. No ADR file was edited.
- **Conventions.** Errors use `api/fault` (through `createPool` and `stopPool`). The comments are short and cite
  the ADRs. The new test follows the `TestIssue<N>_…` naming and `t.Parallel()`.
- **Requeue.** The test asserts that `RequeueAfter` is zero for the Failed member, which covers the "stops polling
  every 200 ms" part of the expected behavior.
- **Commit shape.** The subject is `fix(function): …`, the body has `Fixes #355` and the attribution trailer, and
  the commit covers one issue.

## Recommendation

Pass. Hand back to `/fix` for the PR. The group gate runs the repo-wide tests, the Linux lint and the lanes.
