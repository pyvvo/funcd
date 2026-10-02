## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #115 fix, model: claude-opus-5-5)

Commit under review: `50b5141` `fix(workflow): reject a dependsOn cycle that closes through list order`
(branch `fix/198-workflow`, reviewed at `fbcb9ff`). Touched: `api/types/v1alpha1/workflow.go`,
`api/types/v1alpha1/workflow_test.go`, `internal/workflow/state.go`.

The issue: `Workflow.Validate` checked cycles over the explicit `dependsOn` edges only, while the engine
(`newRunState`) adds an implicit edge from each step without `dependsOn` to the previous DAG step
(ADR-0094 Control flow). Shapes `[a dependsOn b, b]` and `[a dependsOn c, b, c]` were admitted, and their
runs stayed `Running` forever with nothing dispatched. The fix moves the implicit-chaining rule into one
place, `WorkflowSpec.EffectiveDependsOn`, which both `validateAcyclic` and `newRunState` now read, so
admission checks the graph the engine schedules.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The onFailure-exclusion rule in `EffectiveDependsOn` is not pinned by any test** · attribution: `model`
  · Evidence: overlay mutant M2 deletes `if st.Name == s.OnFailure { continue }` from
  `api/types/v1alpha1/workflow.go`; `go test ./api/types/v1alpha1/ ./internal/workflow/` both stay `ok`.
  Under that mutant, in `[a, notify, b]` with `onFailure: notify`, `b` gets the implicit parent `notify`,
  which the engine never schedules on success, so `b` never runs — the same stuck-run class as #115. The
  regression test's acyclic case "handler between chained steps" looks meant to cover this, but a
  `Validate`-level check cannot see it (the mutant adds no cycle). The gap predates the fix: the same mutant
  on the pre-fix `state.go` also survives (`ok internal/workflow`). The fix moved the line into a new
  exported function, which is the natural place to pin it.
  Fix (builder): add a direct assertion on `EffectiveDependsOn` for that spec (`b` → `[a]`, `notify` → none),
  or an engine test where a handler sits between two chained steps and the run succeeds.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 50b5141`
  applied cleanly on `fbcb9ff`; with the test file restored to HEAD,
  `go test -race ./api/types/v1alpha1/ -run TestIssue115`:
  `a dependsOn b, b: want an Invalid cycle error, got <nil>` and
  `a dependsOn c, b, c: want an Invalid cycle error, got <nil>` → `FAIL`. These are the two shapes in the issue.
- **Passes with the fix**, un-skipped, under `-race`: `--- PASS: TestIssue115_ValidateRejectsListOrderCycle`.
  Both acyclic controls (an explicit edge to a later step, a handler between chained steps) are accepted.
- **Mutants.** M1 (drop the implicit edge `deps[st.Name] = []ObjectName{prev}`) and M3 (drop `prev = st.Name`)
  each fail `TestIssue115_…` plus `TestImplicitChaining`, `TestSequentialRunSucceeds`,
  `TestRunMintsAndPropagatesTraceContext`, and `TestIssue116_…` in `internal/workflow`. The shared function
  means the engine's tests now guard the admission graph too. M2 survives (Minor above).
- **Cause, not symptom.** The divergence between the admitted graph and the scheduled graph is removed at
  its source: one function computes the graph and both sides read it. There is no new timeout, requeue, or
  swallowed error. User-visible path: `internal/controlplane/admission/validate.go` runs `obj.Validate()` on
  every Create/Update, so such a Workflow is now rejected at apply time with `fault.Invalid`.
- **Behavior preserved in the engine.** `newRunState` produces the same `dependsOn` per node as before: the
  handler keeps its explicit deps and is never `prev`, and the steps keep spec order in `rs.order`.
  `go test -race ./internal/workflow/...` is `ok`, and the later group commits that touch `state.go`
  (`64310c9`, `d3ad379`, `3ff305d`) compose with it.
- **Scope.** All three hunks serve #115. No test was weakened or deleted. The cycle error message now names
  the implicit rule, which helps the user.
- **Reuse and conventions.** The name and the value receiver follow the existing
  `KVStoreSpec.EffectiveMaxValueBytes`/`EffectiveMaxKeyBytes` precedent in the same package. Errors use
  `fault.Invalidf`, there is no `any` in the signature, and no new dependency. The comments are short and
  cite ADR-0094. No logic is duplicated: the engine's own chaining code was deleted, not copied.
- **ADRs.** The fix matches ADR-0094 Control flow ("no `dependsOn` ⇒ previous step"; the handler is outside
  the DAG). No ADR file was touched.
- **Checks** (all via `nix develop -c`): `gofmt -l` clean; `go build ./...` and `GOOS=linux go build ./...`
  ok; `go vet` host and Linux ok for `./api/...` and `./internal/workflow/...`; `golangci-lint` host and
  Linux `0 issues.`; `go test -race -count=1 ./api/... ./internal/workflow/...` all `ok`;
  `go test -tags e2e -count=1 ./pkg/funcd/...` `ok` (148s, includes the shipped-examples test, so no shipped
  workflow is newly rejected); `just check-hygiene` `hygiene: clean`; `just generate` left the OpenAPI spec
  unchanged (adding a method changes no schema). No Lima lane covers the path.
- **Shape.** `fix(workflow):` subject, cause plus fix plus the named regression test in the body,
  `Fixes #115`, the attribution trailer, one issue in the commit.

### Definition of Done
11 / 11 items hold. Item 4 holds because the key lines (the implicit edge and the `prev` update) fail
tests when mutated. The surviving mutant is on a moved, previously untested rule, recorded as a Minor.

### Model scorecard
Not recorded by this stage. Ledger fields: claude-opus-5-5 on issue #115 (fix) → pass, 0/0/1,
1 model-attributed, DoD 11/11.

### Recommendation
Ship. As a follow-up in the same branch (optional), pin the onFailure exclusion in `EffectiveDependsOn`
with one direct assertion, so the next refactor of that function cannot reintroduce a stuck run.
