# Fix review — issue #443 (onFailure handler params accepted but never sent)

- **Change**: branch `fix/i443`, commit e21a0e3 `fix(workflow): reject params on the onFailure handler step`
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0094 (onFailure handler, FailureContext; Implemented), ADR-0096, ADR-0002
- **Verdict**: **pass**

## Summary

The engine dispatches the onFailure handler with the FailureContext alone (`internal/workflow/engine.go`,
`Engine.fail`), so `params` on the handler step were admitted and silently dropped. The fix makes
admission reject them in `validateOnFailure` (`api/types/v1alpha1/workflow.go`), next to the existing
`dependsOn`/`when` rejection, and corrects the `Params` field doc. This is the remedy the issue asks
for, and it matches ADR-0094, which defines the handler's input as the engine-defined FailureContext.

## Verification (run, not eyeballed)

| Check | Result |
|---|---|
| Revert the fix (`git revert --no-commit e21a0e3`, test file kept) | `TestIssue443_OnFailureHandlerRejectsParams` FAILS: `Validate = <nil>, want Invalid naming the handler and params` — the issue's reason (handler params admitted) |
| With the fix, `go test -race ./api/types/v1alpha1/` | ok |
| Mutant 1: guard `len(s.Params) != 0` → `> 1000` | killed (TestIssue443 fails) |
| Mutant 2: reject params on every step, not only the handler | killed (the test's DAG-step half fails) |
| Mutant 3: error kind `Invalid` → `Conflict` | killed |
| `go vet`, `golangci-lint` on `api/types/v1alpha1` | clean (0 issues) |
| `go test ./internal/workflow -run 'Fail\|Handler'` (no fixture relied on handler params) | ok |
| Worktree after review | at e21a0e3, clean |

User-visible path: `Workflow.Validate` is the admission check (`validateOnFailure` is called from
`Validate`), the same path the issue-179 fix relies on; a daemon rerun was not needed for a pure
admission rule.

## Blockers

None.

## Majors

None.

## Minors

None.

## ✅ Verified correct

- **Cause, not symptom**: the defect is a contract mismatch (admission promised an overlay the engine
  never applies); rejecting at admission is the remedy the issue names and is consistent with ADR-0094's
  "FailureContext only" handler input and with the reconcile-time contract gate, which already checks the
  handler without a params overlay.
- **Scope**: two files; one guard, two doc-comment lines, one test. Nothing unrelated.
- **Reuse**: the guard sits inside the existing `validateOnFailure` loop and uses `fault.Invalidf`, the
  same idiom as the neighbouring `dependsOn`/`when` check; `len(…Params) == 0` is the emptiness test the
  engine and reconciler already use. No new helper, type or dependency.
- **Test quality**: a positive case (handler params rejected, Invalid, names the handler and `params`)
  and a negative control (params on a DAG step still admitted), reusing the existing `newWorkflow` and
  `imgStep` helpers.
- **ADRs**: no ADR file edited; no Accepted/Implemented Decision contradicted.
- **Conventions**: comments state the why with the ADR reference, no bloat; no YAML touched.
- **Shape**: `fix(workflow):` subject, `Fixes #443`, attribution trailer, one issue in one commit.

## Not run here (by design)

Repo-wide tests, the e2e suite, Linux lint and the Lima lanes run once in the group gate and CI.

## Recommendation

Pass. Hand back to `/fix` for the group PR.

## Ledger fields

- verdict: pass · blockers 0 · majors 0 · minors 0 · model_attributed 0 · dod 11/11
- notes: clean pass; revert fails for the issue's reason; 3/3 mutants killed; no findings
