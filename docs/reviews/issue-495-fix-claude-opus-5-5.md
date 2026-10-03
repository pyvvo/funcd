# Fix review — issue #495 (builtin pass/wait bind schema defaults)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #495 fix, model: claude-opus-5-5)

Change: branch `fix/i495`, commit 248ce35 `fix(workflow): bind schema defaults in builtin pass and wait expressions`
(`internal/workflow/condition.go`, `internal/workflow/engine.go`, `internal/workflow/builtin_test.go`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With 248ce35 reverted and the test file kept,
  `TestIssue495_BuiltinBindsSchemaDefault` fails: `builtin expression for step "w": expr.check: unknown field "y"
  under "step.a.output"` — the exact error the issue reports.
- **Passes with the fix under `-race`**, un-skipped, together with the `TestIssue420_*` when/replay/child-run
  tests and the existing `TestBuiltin*` tests.
- **Root cause, not symptom.** The issue names `evalSelect` checking against a `docResolver` with no `schemas`.
  The fix gives `evalSelect` the run record and the same schema map that `evalWhen` uses, so the
  ADR-0095 default binding (`docResolver.Resolve` → `schemaResolver.Resolve` → `HasDefault`) now applies to
  `pass` and dynamic `wait`. This is what ADR-0096 requires: builtin expressions reuse the runtime path
  "exactly as `when.condition` does".
- **Mutants (overlay, `internal/workflow/condition.go`), all killed by the regression test:**
  1. `evalPass` passes an empty `runstate.Record` → fails at step `p` (`unknown field "y"`).
  2. `evalWait` passes an empty `runstate.Record` → fails at step `w`.
  3. `evalSelect` checks against `docResolver{docs: res.docs}` (no schemas) → fails.
  Both the pass branch and the wait branch are therefore covered on their own.
- **Reuse, no duplication.** The fix removes duplication: the doc-model block that `evalWhen` and `evalSelect`
  each built (input + direct-parent outputs + the input schema) is now one `runtimeResolver` helper. It reuses the
  existing `whenSchemaResolver`, `docResolver` and `schemaResolver` and adds no type or dependency. The test reuses
  `newFake`, `newTestEngine`, `passStep`, `waitStep`, `outputOf` and the shared `issue420DefaultedOutput` constant.
- **Scope.** Every hunk serves the issue: the signature changes thread `rec` from `runStep` → `runBuiltin` →
  `evalWait`/`evalPass` → `evalSelect`. No test was weakened or deleted. Reconcile-time checking of builtin
  expressions is untouched, as the issue scopes out.
- **Conventions.** `api/fault` wrapping is unchanged, ctx-first is kept, imports are at the top level, and the
  comments are short and give the why (ADR-0095/0096). gofmt is clean.
- **ADRs.** The fix conforms to ADR-0095 (defaults rule) and ADR-0096 (builtins reuse the when runtime path). No ADR
  file is touched.
- **Checks (touched packages).** `go build ./...` passes. `go test -race ./internal/workflow/...` passes
  (`internal/workflow`, `internal/workflow/runstate/badger`). `go vet` is clean. `golangci-lint` on
  `./internal/workflow/...` reports 0 issues. Repo-wide, Linux-lint and e2e checks are left to the group gate.
- **Shape.** The subject is `fix(workflow): …`, the body has `Fixes #495` and the Co-Authored-By trailer, and the
  commit covers one issue.

### 🟡 Major / Minor

None.

### Definition of Done

10 of 10 applicable items hold: 1–7, 9, 10, 11. Item 8 (build, vet, lint and tests green) holds for the touched
packages on the host. The Linux lint, e2e and lane parts of item 8 belong to the group gate and are not counted here,
so item 8 is counted as passed for the host scope.

### Model scorecard

claude-opus-5-5 — pass, 0/0/0, model-attributed 0.

### Recommendation

Merge with its group after the group gate passes. Hand back to `/fix` Step 8.
