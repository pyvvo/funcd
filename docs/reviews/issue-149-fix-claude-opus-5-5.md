## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #149 fix, model: claude-opus-5-5)

Change: branch `fix/i149`, commit c86a3ee `fix(workflow): re-derive an owned KVStore's owner reference when its deletion policy changes`.
Touched: `internal/workflow/reconcile_workflow.go` (+3/-1), `internal/workflow/reconcile_workflow_test.go` (+49).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** With `origin/main`'s
  `reconcile_workflow.go` restored over the fix (the test kept), `go test -race -run TestIssue149 ./internal/workflow/`
  exits FAIL on both subtests:
  - `delete-to-retain`: `owner refs = [{ObjectRef:{Kind:Workflow Namespace:default Name:wfa} UID:uid-a Controller:true BlockOwnerDeletion:true}], want 0`
  - `retain-to-delete`: `owner refs = [], want 1`

  This matches the issue's Actual behavior exactly. (A plain `git revert --no-commit` of the commit also
  removes the test, so the check was run as a fix-file-only revert.) The worktree was reset to c86a3ee and is clean.
- **Passes with the fix under `-race`**: `TestIssue149_KVDeletionPolicyChangeUpdatesOwnerRef` and the
  neighbouring `TestMaterialize*` tests PASS; `go test -race -count=1 ./internal/workflow/` → `ok`.
- **Root cause fixed, not masked**: the bug was that `ensureKVStore` copied the whole stored `ObjectMeta`
  (`st.ObjectMeta = cur.ObjectMeta`), which overwrote the `OwnerReferences` that `buildKVStore` derives from
  `spec.kv[].deletion`. The fix keeps the stored metadata (UID/RV) and restores the policy-derived owner
  references. Nothing else in the tree writes a KVStore's `OwnerReferences` (grep of non-test Go code: only
  `buildKVStore`), so re-deriving them on every materialize clobbers no other writer.
- **Mutants**, each run with `-run 'TestIssue149|TestMaterialize'` and then restored:
  1. Restore the owners only when non-empty (`if len(owners) > 0 { … }`) → FAIL (`delete-to-retain`).
  2. Drop the `ObjectMeta` copy → FAIL (the test's UID-stability assertion).
  3. Restore `cur.OwnerReferences` instead of the derived owners → FAIL.
- **Scope**: both hunks serve the issue; no test was weakened or deleted. `Status` is still not copied for the
  KVStore, which is #19's separate facet, and the fix correctly leaves it alone.
- **ADRs**: conforms to ADR-0094's materialization rule (`delete` cascades; `retain`, the default, attaches
  no owner reference, and ownership follows the workflow's current declaration) and to the
  `buildKVStore` owner-reference rule that ADR-0139 mirrors. No ADR file was edited.
- **Reuse / no duplication**: no new helper, type or harness. The test reuses the package's `newStore`,
  `fakeRuntimes` and `NewMaterializer` fixtures and follows `TestMaterializeDeletionPolicy`'s table shape.
- **Conventions**: `fault`-wrapped errors are unchanged; the two inline comments state the why (UID/RV
  preservation, ADR-0094 derivation) in the same style as the `ensureFunction` lines beside them; the code is
  gofmt-clean and its imports are unchanged.
- **Checks (touched package)**: `go vet ./internal/workflow/` clean; `golangci-lint run ./internal/workflow/`
  → `0 issues.`; `gofmt -l` empty; package tests under `-race` → `ok`. Repo-wide tests, Linux lint and e2e
  are left to the group gate.
- **Commit shape**: the subject is `fix(workflow): …`, the body names the regression test, and the commit
  carries `Fixes #149` and the attribution trailer. One issue per commit.

### Recommendation
Pass. Hand back to `/fix` Step 8 (PR with `Fixes #149`) after the group gate's repo-wide checks.
