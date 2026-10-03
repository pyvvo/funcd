# Fix review — issue #494 (claude-opus-5-5)

**Issue:** The derived workflow input drops schema defaults; a when on such a field fails.
**Change:** branch `fix/i494`, commit 75fc92d `fix(workflow): keep root input defaults in the derived workflow contract`.
**Files:** `api/types/v1alpha1/contract_check.go` (+12/-5), `internal/workflow/contract.go` (+37/-12),
`internal/workflow/engine.go` (+1), `internal/workflow/reconcile_run_test.go` (+57).
**Governing ADRs:** ADR-0095 (the defaults rule), ADR-0098 (`status.contract.input`, `RootSchemaConflict`); ADR-0002 conventions.
**Verdict:** **pass**

## Summary

`SchemaView` gains a `Defaults` map that `ParseSchemaView` fills from each property's `default`.
`deriveWorkflowContract` merges the defaults of the root steps, and `marshalObjectSchema` writes them into the
derived input schema. The reconcile-time `when:` check (`schemaResolver.Resolve`) and the run-pinned runtime schema
(`evalWhen` reads `rec.Contract.Input`) both read that schema, so one change fixes both paths the issue names. The
issue left one rule open: two roots that declare different defaults for one field. The fix reports that case as a
`RootSchemaConflict`, the same as conflicting types, and compares the defaults as compacted JSON. The change removes
the cause. It does not mask it.

## Verification run

| Check | Result |
|---|---|
| Revert of 75fc92d with the new test kept | FAIL: `Reason:WhenTypeError … optional field "input.x" must declare a default or be guarded with !== undefined`, which is the reason the issue reports |
| With the fix, `-race -run TestIssue494_` | ok (PASS) |
| Package tests `-race` (`api/types/v1alpha1`, `internal/workflow/...`) | ok |
| `go build ./...`, `go vet`, `golangci-lint` on the touched packages, `gofmt -l` | clean (0 issues) |
| Mutant M1: `ParseSchemaView` never records a default | killed (TestIssue494 fails with `WhenTypeError`) |
| Mutant M2: `marshalObjectSchema` drops `Default` | killed (TestIssue494 fails with `WhenTypeError`) |
| Mutant M3: the merge never reports a default conflict | killed (TestIssue494: "want a RootSchemaConflict") |
| Worktree after review | HEAD 75fc92d, clean |

The regression test covers the issue's own steps end to end: the Workflow reconciles Ready, `status.contract.input`
keeps `x.default = "d"`, and a run with input `{}` succeeds with step `b` dispatched once. That last check proves the
runtime binds the default, because `b` would be skipped if `input.x` were undefined.

## Blockers

None.

## Majors

None.

## Minors

1. **`sameJSONValue` copies an existing helper** (`model`). `internal/workflow/contract.go` `sameJSONValue` has the
   same body as `sameJSON` in `internal/controlplane/admission/workflowrun.go` (compact both values, then fall back
   to a byte comparison). `internal/workflow` cannot import the admission package, so the clean fix is to move the
   helper to a package that both can import (for example next to `ParseSchemaView` in `api/types/v1alpha1`) and
   call it from both places. The copy is six lines, so this is Minor.

## Observations (not scored)

- ADR-0098 names `RootSchemaConflict` only for conflicting primitive types. Reporting conflicting defaults with
  the same reason extends that rule, and the issue proposed it. It contradicts no Decision or Contract, and no ADR
  file was edited.
- When one root declares a default for a field and another root declares the same field with no default, the merged
  schema keeps the default. This is consistent with the merge of types, and the issue does not cover the case.
- The conflict error text changed from "require field … at conflicting types" to "declare field … with conflicting
  types". No test, doc or suite matches the old text.

## Verified correct

- The cause named in the issue (`contract.go` merging only `Props`, and `marshalObjectSchema` writing only `type`)
  is the code that changed. Both the reconcile check and the runtime binding read the repaired schema.
- `failureContextSchema` passes `nil` defaults, so its output does not change (`omitempty` on `Default`).
- A `default: null` survives: `json.RawMessage` keeps the literal `null`, so it is not dropped as absent.
- Scope: every hunk serves the issue. No test was weakened or deleted.
- Conventions: top-level imports, no `any` in the new signatures, comments explain why, and naming follows the
  surrounding code.
- Commit shape: `fix(workflow):`, `Fixes #494`, the attribution trailer, one issue in one commit.

## Checklist

10 of 11 items hold. Item 10 (reuse) fails on Minor 1. Item 8 was checked on the touched packages on the host. The
repo-wide checks, Linux lint and e2e run at the group gate.

## Recommendation

Pass. Moving the duplicated JSON comparison helper to a shared package can be done in this group or as a
follow-up.
