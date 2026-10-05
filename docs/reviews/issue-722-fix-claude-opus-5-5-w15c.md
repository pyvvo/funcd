## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #722 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i722`, commit bf419684 `fix(workflow): carry a moved owner's ResourceGroup to its existing children`.
Files: `internal/workflow/reconcile_workflow.go` (+4/-3), `internal/site/reconcile.go` (+3/-3),
`internal/services/identity/reconcile.go` (+12/-1), and one `TestIssue722_…` test in each package's test file.

The fix re-stamps `metadata.resourceGroup` from the owner on the update paths that the issue names. In
`ensureFunction` and `ensureKVStore` it keeps the group from the newly built child after copying the stored meta.
In `ensureRoute` it treats a group difference as drift. In `ensureSecret` it moves an issued Secret to the
Identity's group without rotating it (`restampSecret`), and a rotation also writes the group.

### Proof first (the decision for this issue)

Each regression test was run against the current `origin/main` (a394c6f1) version of the three changed
non-test files through `go test -overlay`, with the test files kept. The branch base (6baf945a) and
`origin/main` have identical copies of these files.

```
--- FAIL: TestIssue722_GroupMoveRestampsChildren
    step function group = "team", want other
    retain kvstore group = "team", want other (with no controller ref it stays a member of the old group)
--- FAIL: TestIssue722_GroupMoveRestampsRoute        expected: "other"  actual: "rg1"
--- FAIL: TestIssue722_GroupMoveRestampsSecret       expected: "other"  actual: "rg1"
```

All three tests fail on current main for the reason the issue states: the children keep the old group after the
owner moves. The Workflow test reproduces the issue's unit probe: a retain KVStore (marker ref only) and a step
Function that binds it. With the fix, all three pass under `-race`.

The issue also asks whether a metadata-only change re-runs the reconcilers, because generation stays at 1. The
controller does not filter on generation (`internal/controller` has no generation check), and a group change is a
content change that `store.Update` writes and emits as a watch event (`internal/store/store.go`, `equalContent`).
The Site test drives that exact case: `h.apply` changes only the group, and `h.reconcile` re-stamps the Route.

### 🟡 Major / Minor

- **Minor — test gap on the rotation path** (`internal/services/identity/reconcile.go:173`, attribution: model).
  Mutant M5 removes `sec.ResourceGroup = id.ResourceGroup` from the rotate/re-issue branch, and the identity
  package still passes. No test moves an Identity and rotates it in the same reconcile. The impact is low: the
  next reconcile takes the `!rotate` branch and `restampSecret` corrects the group.
- **Minor — stale comments in `ensureSecret`** (`internal/services/identity/reconcile.go:137-145`, attribution:
  model). The doc comment still says the reconciler "leaves the existing secret in place" when no rotation is due,
  and the trailing comment "secret already issued for this rotation generation" now sits on a line that writes
  the Secret. Both should mention the group re-stamp.

### ✅ Verified correct (keep it)

- **The cause is fixed, not masked.** The group is now derived from the owner on every update, the same way the
  owner refs already were (`owners, group := …` mirrors the existing `owners := …` idiom). No retry, timeout or
  admission workaround was added.
- **Mutants on the key lines fail a test.** M1 (drop the KVStore re-stamp) fails
  `TestIssue722_GroupMoveRestampsChildren` at the retain-store assertion. M2 (drop the Function re-stamp) fails it
  at the step-function assertion. M3 (ignore the group in the Route drift check) fails
  `TestIssue722_GroupMoveRestampsRoute`. M4 (drop both Secret group writes) fails
  `TestIssue722_GroupMoveRestampsSecret`.
- **The Secret test also checks that a move does not rotate the credential** (`Spec.Data` is unchanged). This is
  the right guard for the new `restampSecret` path.
- **Scope.** Every hunk serves #722. The four update paths that the issue's root-cause section lists are all
  fixed and tested. No test was weakened or deleted.
- **Siblings.** The search covered every site that copies an owner's `ResourceGroup` into a child.
  `Revision` (`internal/function/function.go:1795`) is an immutable, controller-owned snapshot, and Cedar does not
  read its group, so it is not the same defect. The synthesized Cedar Policies (`egress_compile.go`,
  `roles_compile.go`) are compiled from the source object each time. The Site's Bucket (`newBucket`) is
  deliberately ownerless and adoptable, and several Sites can share it, so moving it with a Site would be a
  design decision, not this fix. That case is noted here for the batch and not scored.
- **Reuse.** No new helper duplicates existing code. `restampSecret` is a five-line, single-purpose method next to
  `ensureSecret`. The tests reuse each package's harness (`newStore`/`fakeRuntimes`, `newHarness`,
  `setup`/`reconcile`/`getSecret`).
- **ADRs.** The fix does not decide whether `resourceGroup` is mutable, so it leaves that open as the issue
  required. It agrees with ADR-0170 Decisions 7 and 8 (membership by group, with no controller ref), and with
  ADR-0094 and ADR-0178 (the marker and the deletion policy are untouched). No ADR file was edited.
- **Checks (touched packages).** `go build ./...` is green. `go vet` passes, and `go test -race -count=1` passes
  for `internal/workflow`, `internal/site` and `internal/services/identity`. golangci-lint reports 0 issues. The
  tree is clean after the review. The Linux lint, the e2e suite and the repo-wide checks are left to the group
  gate.
- **Shape.** The subject is `fix(workflow): …`, the body has a cause, fix and test section, `Fixes #722`, and the
  attribution trailer. The commit is one issue per commit.

### Recommendation

Pass. You can fold the two Minors into a follow-up commit: a move-plus-rotate case in
`TestIssue722_GroupMoveRestampsSecret` and the `ensureSecret` comment wording. Neither blocks the merge.
