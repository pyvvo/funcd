## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #55 fix, model: claude-opus-5-5)

Change: branch `fix/i55`, commit `edc489f fix(function): retire a deleted Function's worker when its re-create reaches the same pass`.
Touched: `internal/function/function.go` (+52/-9), `internal/function/switch_test.go` (+37).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

Observations (not findings, recorded for the follow-up on #14):
- A Revision stamped before this change carries no owner reference and is still adopted by a re-created
  Function. The commit message states this on purpose; it only affects state persisted by an older daemon.
- The deleted Function's Revisions at other generations stay in the store until the re-created Function
  reaches that generation, and are then dropped by the same owner check. Their workers are already retired
  by the ADR-0143 drain, because they are neither the current nor the serving revision. This belongs to
  #14 (stale Revisions after a delete), not to #55.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit edc489f`, test file kept at HEAD,
  `go test -race -run TestIssue55_ ./internal/function/` → both subtests FAIL at
  `the deleted Function's worker left the runtime` (the old `echo-1/r0` worker is adopted, as the issue reports).
  After `git reset --hard edc489f` → `ok` under `-race`. Worktree left at `edc489f`, clean.
- **Passes with the fix**, un-skipped, `1-replicas` and `2-replicas` (the issue's mixed-replica facet), plus three
  extra passes that create no worker and leave the Function `Ready`.
- **Mutants (3/3 killed)** via `go test -overlay`:
  1. `ownedByAnother` returns `false` → FAIL (`the deleted Function's worker left the runtime`).
  2. the `rev.OwnerReferences = …` stamp removed → FAIL (same assertion).
  3. the `retire` call in `dropRevision` removed → FAIL (same assertion).
- **Cause, not symptom.** The issue names identity per incarnation as the fix; the Revision now carries a
  controller owner reference with the Function's UID, and `ensureRevision` retires the workers of, and deletes,
  a Revision that another UID stamped before stamping a fresh one. No timeout, retry or swallowed error.
  `store.Delete` tolerates only `NotFound`; every other error is wrapped with its fault kind.
- **Scope.** Every hunk serves #55. No test was weakened or deleted.
- **Reuse.** `dropRevision` reuses `namedInstances` and `retire`. The owner reference reuses `v1.OwnerReference`
  and the `Controller: true` idiom of `internal/site`, `internal/workflow` and `internal/services/identity`;
  `ownedByAnother` mirrors the per-package `ownedBy` helper in `internal/site/materialize.go` with the semantics
  this case needs (a missing owner counts as fn's). No new dependency, type or harness; the test uses the
  existing `newShimHarness` / `withSwitch` / `deployReady` helpers.
- **Conventions.** `api/fault` errors with ops, ctx-first, typed IDs, no `any`, top-level imports, comments state
  the why (the issue reference, the coalesced pass). `gofmt -l` clean.
- **ADRs.** Consistent with ADR-0020 (delete-reclaims; a Revision stays an immutable snapshot of its own
  Function — the dropped one belonged to a deleted Function) and ADR-0143 Decision 2 (teardown removes the
  workers of a deleted Function). No ADR file touched.
- **Checks (touched package).** `go build ./...` OK; `go vet ./internal/function/` OK;
  `golangci-lint run ./internal/function/...` → `0 issues.`; `go test -race -count=1 ./internal/function/` → `ok`.
  The e2e reproduction, Linux lint and the repo-wide tests are left to the group gate.
- **Shape.** `fix(function):` subject, `Fixes #55`, attribution trailer, one issue per commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 is verified for the touched package on the host; Linux lint, e2e
and lanes run at the group gate.

### Model scorecard
To record: claude-opus-5-5 on issue #55 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. Hand back to `/fix` Step 8 after the group gate is green.
