## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #927 fix, model: claude-opus-5-5)

Change: commit 33f75219 `fix(workflow): refuse a Workflow that names one kv store twice` on `fix/w12-i927`.
Decision under review: refuse a repeated `spec.kv` name in `Workflow.Validate` (`validateOwnedStores`), naming
`spec.kv[i].name`, as `App.Validate` refuses a name repeated within a section. The change implements exactly that.

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** Overlay of the `origin/main` `api/types/v1alpha1/workflow.go`:
  `TestIssue927_ValidateRefusesRepeatedKVName` FAILS with `Validate() = <nil>, want an Invalid error containing
  "spec.kv[1].name repeats the name \"cache\" of spec.kv[0]"` — the issue's Actual behavior.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./api/types/v1alpha1/` → `ok`. The test also
  checks that two distinct names are accepted, so the rule does not over-refuse.
- **Mutants (3/3 killed)**, each an overlay of `workflow.go` run against `-run TestIssue927`:
  1. duplicate branch disabled (`&& false`) → FAIL (`<nil>`);
  2. recorded index off by one (`seen[st.Name] = i + 1`) → FAIL (message names `spec.kv[1]` as the earlier entry);
  3. the `seen` record removed → FAIL (`<nil>`).
- **Cause, not symptom.** The issue's root cause is the missing name-distinctness check in `validateOwnedStores`
  (`api/types/v1alpha1/workflow.go`); the fix adds it there, before any write. The materializer in
  `internal/workflow/reconcile_workflow.go` (step 2) builds one KVStore per `spec.kv` entry and calls
  `ensureKVStore` for each in turn, confirming the inferred overwrite path that the check now closes at validation.
- **Matches the decision and the precedent.** Error shape `spec.kv[%d].name repeats the name %q of spec.kv[%d]`
  with `fault.Invalidf` mirrors `App.Validate`'s `"%s repeats the name %q of %s"` (`api/types/v1alpha1/app.go`,
  the section-name and secrets checks); the `seen`/`prev, dup` idiom is the same.
- **Reuse.** No existing shared helper does a uniqueness check in `api/types/v1alpha1` — every validator
  (`app.go`, `kvstore.go`, `bucket.go`, `eventsource.go`, `function.go`, `route.go`) inlines a `seen` map.
  The inline map is the package idiom; no duplication of a helper.
- **Scope.** Two hunks in `workflow.go` (the check and the two doc comments it updates) and one new test; no
  test weakened or deleted.
- **Conventions.** `api/fault` Invalid error, typed `ObjectName` key, no comment bloat (the doc comment states
  the why: each entry materializes as the KVStore of that name), imports unchanged.
- **ADRs.** No ADR file touched; ADR-0094's `Validate` contract (rules JSON Schema cannot express) is extended,
  not contradicted.
- **Siblings.** `WorkflowSpec` has two name-keyed lists: `steps` (already unique-checked) and `kv` (now). Within
  an App, #924's fix already refuses two Workflows that declare one store.
- **Checks (touched package only)**: tests `-race` ok; `go vet` clean; `golangci-lint run ./api/types/v1alpha1/...`
  → `0 issues.` Repo-wide, Linux and e2e checks are left to the group gate.
- **Shape.** `fix(workflow):` subject, `Fixes #927`, regression test named in the body, attribution trailer, one
  issue in one commit. Worktree left clean.

### Definition of Done
12 / 12 items hold (item 8 for the touched package; the repo-wide and Linux part runs in the group gate).

### Model scorecard
Not recorded here (ledger rows are written by the batch ledger PR): claude-opus-5-5 on issue #927 (fix) → pass,
0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Pass. Hand back to `/fix` for integration into the wave-12 group PR.
