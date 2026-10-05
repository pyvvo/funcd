# Fix review — issue #710 (model: claude-opus-5-5, wave 15c)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #710 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i710`, commit 9598e273 `fix(workflow): refuse an image step whose function name exceeds 63 bytes`.
The branch base (6baf945a) is behind `origin/main` (a394c6f1), but `git diff --stat 6baf945a origin/main` over
`api/types/v1alpha1`, `internal/workflow` and `internal/controlplane` is empty, so the proof below holds on current main.

### Prove-first decision (the person's rule)

The regression test was run with the `origin/main` version of all four changed non-test files overlaid
(`api/types/v1alpha1/workflow.go`, `internal/controlplane/logs.go`, `internal/workflow/engine.go`,
`internal/workflow/reconcile_workflow.go`; `go test -overlay`), so the test is current main with no fix:

```
--- FAIL: TestIssue710_AdmittedWorkflowMaterializesStepFunctions
    --- PASS: .../wf31-step31-reffalse
    --- FAIL: .../wf40-step40-reffalse   an admitted workflow must materialize: workflow.materialize: create function "www…w-sss…s": ObjectMeta.Validate: invalid name: ObjectName.Validate: … is not a valid DNS label
    --- FAIL: .../wf63-step63-reffalse   (same)
    --- PASS: .../wf40-step40-reftrue
```

That is the issue's exact reason: `Validate` admits the Workflow and `Materialize` fails at Function creation.
The 63-byte boundary (31+1+31) still materializes. The production change came after a failing proof, as decided.

### ✅ Verified correct (keep it)

- **Passes with the fix under `-race`**: all four subtests PASS (`ok internal/workflow`).
- **Cause, not symptom**: `Workflow.Validate` (`api/types/v1alpha1/workflow.go:272`) now refuses an image step
  whose `<workflow>-<step>` name fails `dnsLabel`, with `fault.Invalid` naming the step; admission relies on
  `obj.Validate()` (store `Create`/`Update`), so the apply is refused up front as the issue expects.
- **One source of the name**: the new `v1alpha1.StepFunctionName` replaces `materializedStepName` and the inline
  concatenation in `internal/controlplane/logs.go:158`, so the validated name is the created, dispatched and
  log-resolved name. All remaining callers go through `materializedName` → `StepFunctionName` (grep: no other
  `<workflow>-<step>` concatenation in `internal`, `api`, `pkg`, `cmd`).
- **Ref steps untouched**: the check is gated on `s.Function.Image != ""`; ref steps materialize nothing
  (`pruneFunctions` keeps only image steps), and the `ref=true` 40/40 case proves they stay admitted.
- **Mutants** (overlay on `workflow.go`):
  - M1 drop the length check (`false && …`) → wf40/wf63 subtests FAIL.
  - M2 apply the check to ref steps too → the `ref=true` subtest FAILS.
  - M3 drop the `-` separator in `StepFunctionName` → TestIssue710 passes alone, but 10 existing
    `internal/workflow` tests fail (`TestMaterializeOwnedFunctionsAndKV`, `TestFunctionRefDispatches`, …): killed.
- **Reuse**: reuses the package's existing `dnsLabel` regexp and `fault.Invalidf`; the test reuses `newStore`,
  `fakeRuntimes` and `NewMaterializer` from the package's harness. The new helper replaces, rather than duplicates,
  `materializedStepName`.
- **Scope**: every hunk serves the issue (the check, the shared name helper, the Validate doc comment, the test);
  no test weakened or deleted.
- **Conventions**: ctx-first and `api/fault` kept, typed `ObjectName`, top-level imports, a one-line doc comment
  citing ADR-0094, no comment bloat.
- **ADRs**: enforces the length that ADR-0094's `<workflow>-<step>` naming already implies; no hashing/truncation
  scheme (which would need an ADR); no ADR file edited.
- **Checks (touched packages)**: `go test -race` on `api/types/v1alpha1`, `internal/workflow`,
  `internal/controlplane` all `ok`; `go vet` clean; `golangci-lint` 0 issues. Worktree left clean.
- **Shape**: `fix(workflow):` subject, `Fixes #710`, attribution trailer, one issue in one commit.
- **Siblings**: KV table owners (`buildKVStore`) use the same name, but `KVStore` does not validate `Owner` as a
  DNS label and an owner that names an image step is now bounded by the same check; no other site builds this name.

### Note (not a finding)

A Workflow already stored with an over-long image step before this fix now fails `Validate` on its status
`Update` too; it was already stuck and stays deletable (`Delete` does not validate, Workflows carry no
finalizer), and the issue's workaround (shorten and re-apply) is unchanged.

### Definition of Done

12 of 12 applicable items hold. Item 8 is verified at the touched-package scope; the Linux lint, e2e and
repo-wide tests belong to the group gate.

### Model scorecard

claude-opus-5-5 · fix · pass · 0 blockers · 0 majors · 0 minors · 0 model-attributed · DoD 12/12.

### Recommendation

Pass. Hand back to `/fix` for integration; the group gate runs the repo-wide checks.
