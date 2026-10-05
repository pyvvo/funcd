# Fix review — issue #719 (ConfigMap create and update return 409 before authorizing the caller)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #719 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i719`, commit 1614a40d `fix(controlplane): authorize ConfigMap create and update before the
migration-record guard`. Files: `internal/controlplane/handlers.go`, `internal/controlplane/kvhandover.go`,
`internal/controlplane/kvhandover_test.go`. Branch base 6baf945a; `origin/main` (a394c6f1) has no change under
`internal/controlplane/` since that base, so the proof below on the current `origin/main` code is exact.

### Proof first (the person's decision for this issue)

`TestIssue719_MigrationRecordGuardRunsAfterAuthorization` run against the **current `origin/main`** production code
(overlay of `git show origin/main:` for `handlers.go` and `kvhandover.go`, test file kept):

```
--- FAIL: TestIssue719_MigrationRecordGuardRunsAfterAuthorization
    --- FAIL: .../dev-token/POST        expected: 403  actual: 409  "... kvstore-marker-migration is reserved for the platform"
    --- FAIL: .../dev-token/PUT         expected: 403  actual: 409
    --- FAIL: .../view-token/POST       expected: 403  actual: 409
    --- FAIL: .../view-token/PUT        expected: 403  actual: 409
    --- FAIL: .../sys-viewer-token/POST expected: 403  actual: 409
    --- FAIL: .../sys-viewer-token/PUT  expected: 403  actual: 409
```

Each case of the issue has its own failing subtest, for the stated reason (the guard's 409 wins over the PDP's
403): POST and PUT for an out-of-scope developer, an out-of-scope viewer, and a viewer scoped to `funcd-system`
(the issue's third case). DELETE subtests pass on `origin/main`, as the issue says (control). The fix followed a
failing proof, as decided.

### 🟡 Minor 1 — the new `kind == v1.KindConfigMap` condition is not pinned by a test  ·  attribution: model

`refuseMigrationRecord` now takes the kind and is called from the generic `createObj`/`replaceObjIf`/`deleteObjIf`,
so the kind check is what keeps a Secret (or any other kind) named `kvstore-marker-migration` in `funcd-system`
writable. No test covers a non-ConfigMap kind at that name; a mutant dropping the kind condition would survive.
Low risk (the condition is one line and obvious), so not a Major.

### ✅ Verified correct (keep it)

- **Passes with the fix under `-race`**: all 9 subtests and `TestScenarioMigrationRecordGuarded` (admin still gets
  409 on POST, PUT, DELETE and on the forced ResourceGroup delete) — `ok internal/controlplane`.
- **Cause, not symptom**: the guard moves to right after `h.authorize` in `createObj` (after the name is final,
  `GenerateName` resolved) and `replaceObjIf`, matching the existing order in `deleteObjIf`; the two early calls in
  `CreateConfigMap`/`ReplaceConfigMap` are removed. The order now follows the package's authorize → admit → store
  path and ADR-0018 C3; the 409 for an authorized caller required by ADR-0180 Decision 4 is unchanged.
- **Mutants** (overlay, `-run 'TestIssue719|TestScenarioMigrationRecordGuarded'`), all killed:
  - M1 guard moved before `authorize` in `createObj` → `TestIssue719…/{dev,view,sys-viewer}-token/POST` fail.
  - M2 guard removed from `replaceObjIf` → `TestScenarioMigrationRecordGuarded` fails.
  - M3 guard removed from `createObj` → `TestScenarioMigrationRecordGuarded` fails.
- **Scope**: every hunk serves the issue; no test weakened or deleted. Folding the ConfigMap check into the
  function (kind parameter) replaces the `if kind == v1.KindConfigMap` wrapper `deleteObjIf` already had, so all
  three call sites read the same.
- **Reuse**: reuses `refuseMigrationRecord`, `h.authorize`, the package's `do()` test helper,
  `middleware.NewStaticCredentials`, `rbac.New` and `workflow.MarkKVStoresOnce`; no new helper or dependency. The
  inline call mirrors `deleteObjIf`; the `replaceObjIf` guard-func parameter (used by ADR-0178's `refuseLiveMarked`)
  could not serve create or delete, so the inline form is the consistent choice.
- **Siblings**: no other guard runs before authorization in the ConfigMap wrappers or the generic helpers; the
  platform writes the record through the store directly (`workflow.MarkKVStoresOnce`), not through the API, so the
  migration is unaffected.
- **Conventions**: `api/fault` Conflict kept, ctx-first, test-only imports at top level, doc comment updated to
  name the ADRs (no narration).
- **ADRs**: no ADR file edited; consistent with ADR-0018 C3, ADR-0180 Decision 4 and the ADR-0178 precedent.
- **Checks** (touched package): `go test -race ./internal/controlplane/...` ok (controlplane, admission);
  `go vet` ok; `golangci-lint` 0 issues. Worktree clean. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(controlplane):` subject, `Fixes #719`, attribution trailer, one issue in one commit.

### Definition of Done

11 of 11 applicable items hold (item 8 is checked here for the touched package only; the host-wide, Linux and e2e
parts run in the group gate and are not counted).

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 1 minor (model: untested kind condition).

### Recommendation

Pass; integrate. Optionally add one assertion that a non-ConfigMap kind at the reserved name is not refused.
