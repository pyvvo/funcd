## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #19 fix, model: claude-opus-5-5)

Change: branch `fix/19-kvstore-status-wipe`, commit 26ff7d3 `fix(workflow): keep an owned KVStore's status when a
Workflow is re-materialized` on `origin/main` 556e684 (`internal/workflow/reconcile_workflow.go`,
`internal/workflow/reconcile_workflow_test.go`; 41 insertions).

The issue: each time the Workflow materializer reconciles, `ensureKVStore` copies only the stored `ObjectMeta`
into the KVStore it built from the Workflow spec and calls `store.Update`, so the update replaces the status the
KVStore reconciler wrote with an empty one (`phase: ""`, `tables: 0`, `bindings: 0`) until that reconciler runs
again.

The fix: one line in `ensureKVStore`, `st.Status = cur.Status`, which carries the stored status into the update,
the same way `ensureFunction` already carries the Function status (`reconcile_workflow.go:285`).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With the `origin/main` version of
  `reconcile_workflow.go` overlaid (`go test -overlay`), `TestIssue19_RematerializeKeepsKVStoreStatus` fails:
  `a re-materialize wiped the kvstore status: phase="" tables=0 bindings=0`. This is the issue's own "after the
  second Materialize" line.
- **It passes with the fix under `-race`.** `go test -race -run TestIssue19_ -v ./internal/workflow/` gives
  `--- PASS: TestIssue19_RematerializeKeepsKVStoreStatus`. The whole package passes under `-race`
  (`ok … internal/workflow`).
- **The test is the issue's reproduction.** It runs the issue's three steps as written: it materializes a Workflow
  with the owned KVStore `counters-kv` (table `t` owned by step `ingest`), writes `Ready`/`tables: 1`/`bindings: 1`
  as the KVStore reconciler does, materializes again, and asserts all three fields. The rerun of the issue's
  steps is this test, so the user-visible behavior is checked directly.
- **Mutants: 3 of 3 killed**, each by the new test.
  - M1: only the phase is carried (`st.Status.Phase = cur.Status.Phase`): fails with `tables=0 bindings=0`.
  - M2: the assignment is reversed (`cur.Status = st.Status`): fails with the full wipe.
  - M3: only the embedded common status is carried (`st.Status.Status = cur.Status.Status`): fails with
    `tables=0 bindings=0`.
- **The root cause is fixed, not masked.** The cause the issue names (the update replaces the stored status) is
  removed at the write. No retry, no extra reconcile, no relaxed assertion. Because `ObjectMeta` (and so the
  resource version) comes from the same `Get`, a status write by the KVStore reconciler between the `Get` and the
  `Update` produces a conflict, not a stale overwrite. The whole-struct copy also keeps `Conditions` and
  `ObservedGeneration`, so a spec change still reads as not yet observed until the reconciler runs.
- **The ownership claim is grounded.** ADR-0073 (Implemented) makes the `KindKVStore` reconciler the writer of
  Ready and `status.tables`/`bindings` (item 7 of its decision), and `internal/services/kv/reconcile.go` writes
  exactly those fields. `ensureKVStore` is the only KVStore write in `internal/workflow`.
- **Scope.** Both hunks serve the issue. The owner-reference re-derivation (ADR-0094 deletion policy) is untouched,
  and no existing test was weakened or deleted.
- **Reuse.** The fix mirrors the existing `ensureFunction` line, and no shared status-preservation helper exists
  in the codebase to use instead. The test reuses the package's `newStore` and `fakeRuntimes` harnesses and
  follows the structure of the neighbouring `TestMaterializeKeepsFunctionStatus`.
- **Conventions.** The inline comment states the why and names the ADR, in the same form as the `ensureFunction`
  line. No new imports, no `any`, `api/fault` errors unchanged. The test name follows `TestIssue<N>_…`.
- **ADRs.** No ADR file was touched. The fix conforms to ADR-0073 (the reconciler owns the status), ADR-0094
  (materialization and deletion policy unchanged) and ADR-0002.
- **Checks on the touched package.** `go vet ./internal/workflow/` exits 0, `gofmt -l` is empty, and
  `golangci-lint run --allow-parallel-runners ./internal/workflow/` reports `0 issues.` on the host and with
  `GOOS=linux` (exit 0). As instructed, the repo-wide gate and CI run once per PR, not per review.
- **Commit shape.** The subject is `fix(workflow): …`, the body has `Fixes #19` and names the regression test, the
  attribution trailer is present, and the commit covers one issue.

### Definition of Done

11 / 11 items of the fix checklist hold. Item 8 holds for the touched package; the repo-wide set runs in the
PR's gate.

### Model scorecard

To record: claude-opus-5-5 on issue #19 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Pass. Hand back to `/fix` Step 8. The worktree is left at 26ff7d3 plus this report.
