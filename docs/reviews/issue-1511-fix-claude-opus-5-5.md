## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1511 fix, model: claude-opus-5-5)

This report reviews commit 0ee53621 `fix(addon): an add-on object deleted right after a restart follows its deletion
policy` on the branch that ships the FEAT-0018 review rulings in one PR, on `origin/main` 610bed78. Issue #1511: an
add-on object deleted after funcd restarts, before the reconciler's first pass over it, had its storage moved into the
quarantine even under `deletion: delete`. By the decider's standing rule of no review agents, the session that wrote
the fix ran this review, so it is not independent. Every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the reported reason.** With `origin/main`'s
  `internal/services/addon/restore.go` as an overlay, `TestIssue1511_ADeletionAfterARestartFollowsTheObjectsPolicy`
  fails on its first case: `{deletion:delete heldAtStart:false moved:false}: a quarantine entry`. With the fix it
  passes under `-race`.
- **The cause is fixed, not masked.** The reconciler learned a policy only in a pass (`remember`), and `deletionOf`
  read retain for an object no pass had seen. `ReclaimOrphans`, the start-up hook that runs before the controller, now
  calls `remember` for every stored add-on object before it reads the hold, so the policy is known before any
  deletion pass, held or not.
- **The held start is covered.** A mutant that returns while held before the objects are remembered fails the test's
  `heldAtStart` case.
- **Scope and reuse.** Two files: the hook and its comment, and the test beside the restore rig's other tests. The fix
  reuses `remember` and `store.List`; it adds no record, file or option, so no durability mechanism needs an ADR.
- **It is a bug.** The issue's Design check cites ADR-0333 Decision 5 and its scenario `addon-deletion` (a deleted
  object's storage is gone under `delete`). ADR-0288 Decision 1 is kept: an object gone before the start remembered
  it still has its storage quarantined, as the start's orphan step does.
- **No sibling.** Of the kinds that keep data on the node, only the add-on object holds its deletion policy in
  memory: a SQLDatabase has none, a Function volume keeps its own in the volume record (ADR-0262 Decision 7), and an
  App part's lives in its owner reference.
- **Checks.** `go test -race -count=1 ./internal/services/addon`, `go vet` and `golangci-lint run` on the package:
  ok, 0 issues. The repo-wide gate runs once on the final branch.
- **Shape.** A `fix(addon):` subject, `Fixes #1511` and the attribution trailer, in one commit.

### Definition of Done

12 of the 12 items that apply hold; item 13 does not apply, since no design spec models a deletion.

### Model scorecard

`claude-opus-5-5`, fix, pass: 0 Blocker, 0 Major, 0 Minor.

### Recommendation

Ship it in the batch PR with `Fixes #1511`.
