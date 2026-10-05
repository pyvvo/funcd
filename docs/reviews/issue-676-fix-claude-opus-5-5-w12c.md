## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #676 fix, model: claude-opus-5-5)

Change: branch `fix/w12c-i676`, commit 806de181 `fix(auth): type-check the entity of an is-in Policy scope`.
Decision being checked: type-check the entity of an `is ... in` scope against the schema exactly as the `==` and
`in` scopes are checked, and return 400 naming the unknown type.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **The fix removes the cause.** Before the change, `validateCedar` (`internal/auth/cedar/schema.go`) checked only
  `sc.Principal.Entity.Type` and `sc.Resource.Entity.Type`. An `is ... in` scope stores its entity under
  `In.Entity`, so that entity was never checked. The loop now iterates over `scopeEntity.named()`, which
  returns the entity of an `==`, `in` or `is ... in` scope. Each entity goes through the same
  `KnownEntityType` check and the same `fault.Invalidf` message as before, so an `is ... in` entity is checked
  exactly as the decision requires.
- **The change reuses existing code.** `named()` already existed for `ValidateCedarInNamespace`, which walks
  the same `append(sc.Principal.named(), sc.Resource.named()...)` loop. The change adds no helper and no
  duplicated logic. Removing the `et != ""` guard is safe because `named()` returns only entities whose type is
  set.
- **The admission path is covered.** `internal/controlplane/admission/policy.go:36` calls
  `ValidateCedarInNamespace`, which calls `validateCedar` first. The Policy apply path from the issue therefore
  returns `fault.Invalid` (400).
- **Revert check.** With an overlay of the `origin/main` `schema.go`, `go test -overlay … -run TestIssue676`
  fails in both subtests (`resource`, `principal`) with "An error is expected but got nil". This is the reason
  the issue reports: the Policy is admitted.
- **With the fix.** `go test -race -count=1 ./internal/auth/cedar/` → `ok`.
- **Mutants (overlay, whole package). All three were killed:**
  - The loop checked only the principal scopes: `TestIssue676…/resource` failed.
  - The loop checked only the resource scopes: `TestIssue676…` and `TestScenarioPolicyValidityCuratedSchema`
    failed.
  - The check was replaced with `e.Type == ""`: `TestIssue676…` and `TestScenarioPolicyValidityCuratedSchema`
    failed.
- **The test is sound.** `TestIssue676_IsInScopeUnknownEntityTypeRejected` first shows that a valid
  `is KVTable in KVStore::…` statement passes. It then checks both the principal and the resource `is ... in`
  scopes, and it asserts the `fault.Invalid` kind and the `unknown entity type "Foo"` message. It is parallel and
  table-driven, like the tests around it.
- **Scope.** The change has two hunks: the fix and its test. No test was weakened or deleted, and no ADR file was
  touched. The change follows ADR-0116 (assembled vocabulary through `KnownEntityType`) and ADR-0177 Decision 3
  (scope walking through `named()`).
- **Checks on the touched package.** `go vet ./internal/auth/cedar/` is clean. `golangci-lint run
  ./internal/auth/cedar/...` reports 0 issues. The worktree was left clean.
- **Commit shape.** The subject is `fix(auth): …`. The body states the cause, the fix and the test. It carries
  `Fixes #676` and the attribution trailer, and the commit fixes one issue only.

Observation, not a finding: the type after `is` (`resource is Foo in KVStore::…`) is still not checked against
the vocabulary. The issue and the decision cover only the `in` entity, so this review does not score it. File it
separately if it should be rejected as well.

### Definition of Done
11 / 11 items hold. Item 8 covers only the touched package (tests with `-race`, vet and lint). The repo-wide and
Linux checks and the e2e suite are left to the group gate.

### Model scorecard
Ledger fields (not recorded here; the batch's ledger PR records them): claude-opus-5-5 on issue #676 (fix) →
pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ready for integration. Hand back to `/fix` Step 8 (the group integrator opens the PR).
