# Fix review — issue #724 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #724 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i724`, commit b511a289 `fix(function): forget a reclaimed pool worker's liveness`
(merge base 6baf945a; `origin/main` is at a394c6f1 and the branch merges into it without conflict).
Files: `internal/function/pool.go`, `internal/function/poolaccess.go`, `internal/function/pool_test.go`,
`internal/function/export_test.go`.

The orphan sweep (`reclaimOrphanPools`) now calls one `forgetPool(ns, name)` that drops the reclaimed pool
worker's signature, `poolLive` entries and member set under one `poolMu` hold. Before, it dropped the signature
and the member set only, so each pool key that stopped existing left one `poolLive` entry until restart.

### Prove-first decision (user rule)

The regression test `TestIssue724_ReclaimedPoolForgetsLiveness` has one subtest per case of the issue
("member deleted": a pool's last member is deleted; "member moved": a member changes worker id, which moves it
to a new pool key, the same mechanism as an access re-hash). With the production files overlaid by the
**current `origin/main`** versions (`pool.go`, `poolaccess.go`; the only other package files that differ
between the merge base and `origin/main` are `pool.go` and `pool_test.go`), both subtests fail for the
issue's stated reason:

```text
--- FAIL: TestIssue724_ReclaimedPoolForgetsLiveness/member_deleted
        Error: Should be zero, but was 1
--- FAIL: TestIssue724_ReclaimedPoolForgetsLiveness/member_moved
        Error: Not equal: expected: 1  actual: 2
```

So the defect still exists on current main, each case has its own failing proof, and the production change
followed that proof.

### Minor 1 — over-deletion of `poolLive` is not caught  ·  attribution: model
- Evidence: mutant M2 widened the `poolLive` predicate in `forgetPool` from `ofPool(key)` to
  `key.Namespace == ns` (drop every pool's liveness in the namespace). The package tests and both
  `TestIssue724` subtests still passed. In "member moved", the new pool's entry is dropped by the sweep and
  re-added by the next reconcile's probe, so the final count is 1 either way.
- Why it matters: `poolSilent` takes the later of `poolLastLive` and `CreatedAt`, so dropping a live pool's
  entry could make an older healthy pool look silent and restart it early. The shipped code is correct; only
  the guard is missing.
- Fix: in "member moved", assert the w1 pool's liveness survives the sweep (for example a `PoolLastLive`
  export compared before and after the reconcile that reclaims w0), or assert the count right after the
  reconcile that reclaims w0, before another probe runs.

### Minor 2 — the `poolSets` drop moved but no test covers it  ·  attribution: model
- Evidence: mutant M3 removed `delete(r.poolSets, poolSetKey(ns, name))` from `forgetPool`; the whole
  `internal/function` suite passed. The gap existed before (the deleted `forgetPoolMembers` was not covered
  either), but the issue's expected behavior names all three maps and the fix rewrote this line.
- Fix: add a `PoolSetsLen` (and optionally `PoolSigsLen`) export and assert zero in "member deleted", as the
  issue's own reproduction did.

### ✅ Verified correct (keep it)
- Revert check: overlay of `origin/main`'s `pool.go` and `poolaccess.go` → both subtests FAIL for the
  reported reason (above).
- With the fix: `go test -race -count=3 -run TestIssue724` → 3/3 PASS for both subtests, not skipped.
- Mutant M1 (the `poolLive` loop never deletes) → `TestIssue724` FAILS. M2 and M3 survive (Minors above).
- Cause, not symptom: the delete is added where the sibling maps are dropped, under the same lock, exactly the
  path the issue names; no timeout, retry or swallowed error.
- No sibling with the same cause: `poolLive` has one writer (`markPoolLive`) and one reader (`poolLastLive`).
  The `desired == 0` path only stops the pool worker (`stopPool`), so the instance stays listed and the orphan
  sweep reclaims it, together with its liveness, once its key is no longer declared.
- Scope: every hunk serves the issue. Folding `forgetPoolSigOf` and `forgetPoolMembers` into `forgetPool` is
  the shape the issue proposed and removes a second lock acquisition; both old helpers had a single caller,
  and no other reference remains.
- Reuse: the test reuses the existing shim harness (`newShimHarness`, `withSwitch`, `withNodePool`,
  `revisionStates`, `poolOf`) and the existing `export_test.go`; the predicate reuses `poolInstanceName`. No
  new dependency, type or harness.
- Conventions: ADR-0002 unchanged (no new error paths, no `any`, no import change); package-level imports
  only; comments state the why at type and function altitude, no narration; naming matches the surrounding
  `forgetPoolSig` and `setPoolMembers`.
- ADRs: ADR-0158 Decisions 4 and 5 are respected (liveness probe and access-hash keying unchanged); it says
  nothing about the lifetime of `poolLive` entries. No ADR file was edited.
- Checks (touched package only): `go test -race -count=1 ./internal/function/` ok; `go vet` clean;
  `golangci-lint run ./internal/function/` 0 issues. The worktree was left clean.
- Shape: `fix(function):` subject, `Fixes #724`, attribution trailer, one issue in one commit.

### Definition of Done
12 of 12 applicable items hold. Item 8 was checked on the host for the touched package only; Linux lint, the
repo-wide tests and e2e belong to the group gate.

### Model scorecard
claude-opus-5-5: pass; 0 blockers, 0 majors, 2 minors (both model-attributed test gaps).

### Recommendation
Ship. The two Minors are test hardening and can be folded in now or left; neither changes the production code.
