## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #718 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i718`, commit 0c3df356 `fix(workflow): describe the WorkflowRun phases in the OpenAPI spec`.

The fix gives `WorkflowRunStatus` its own phase leaf, `v1.RunPhase`, which holds the six ADR-0094 run phases and has
an `enumSchema`. This leaf shadows the embedded `Status.Phase`. The engine constants (`internal/workflow/state.go`)
and `runstate.Record.Phase` are retyped to the new leaf, and the committed spec is regenerated. The wire shape does not
change. ADR-0048's seven-value `Phase` enum also stays as it is.

### Prove first (user rule)

`TestIssue718_RunPhasesRoundTrip` was run on the current `origin/main` code: an overlay of the `origin/main` version of
each changed non-test Go file (`api/types/v1alpha1/workflowrun.go`, `internal/workflow/{reconcile_run,state}.go`,
`internal/workflow/runstate/runstate.go`), with the test kept. It fails for the reason the issue gives, in all four
cases, and the two control cases pass:

```
--- FAIL: TestIssue718_RunPhasesRoundTrip/Running   ... does not contain "Running"
    {"status":422,"detail":"validation failed: expected value to be one of \"Pending, Deploying, Ready, Idle, Degraded, Failed, Terminating\" (body.status.phase)"}
--- FAIL: .../Paused     (same: spec enum + 422)
--- FAIL: .../Succeeded  (same)
--- FAIL: .../Cancelled  (same)
(Pending, Failed: pass — in-enum controls)
```

Each case of the issue has its own subtest. Each subtest checks two things: that the served `/openapi.json` enum
contains the phase, and that a GET-modify-PUT with the status echoed back returns 200.

### 🟡 Minor 1 — test idiom differs from its neighbours  ·  attribution: model

`internal/controlplane/workflowrun_phase_test.go` uses `map[string]interface{}` three times. The neighbouring tests in
the package use `map[string]any` (`api_test.go`, `server_test.go`, `status_test.go`). The test also discards the error
from `v1.NewObject` (`obj, _ :=`). Both are cosmetic, and lint is clean.

### Observation (not scored)

`WorkflowRun.GetStatus()` still returns the embedded `Status`. Its `Phase` is now shadowed and is never serialized.
A future generic write through the `StatusObject` seam would therefore set a phase that is silently dropped. No
production code does this today: the only generic `GetStatus().Phase` writer is in `internal/controller/controller_test.go`,
and it does not use a WorkflowRun. The fix documents this on `GetStatus`. The trade-off follows from the "own phase
leaf" shape that the issue proposed.

### ✅ Verified correct (keep it)

- **Revert check**: fails without the fix for the issue's reason (see above). With the fix it passes under `-race`
  (`go test -race -run TestIssue718 -v ./internal/controlplane/`: all 6 subtests PASS).
- **Cause, not symptom**: the closed 7-value enum no longer describes run phases. The run status has a leaf whose
  enum is declared once and is shared by the engine (`state.go` aliases `v1.Run*`) and the schema
  (`RunPhase.Schema`). This satisfies ADR-0048 `single-source-no-drift` and ADR-0005 `spec-consumable-by-client`.
  Nothing is masked: the request-body validation is unchanged.
- **Mutants**: all three were killed.
  1. Dropping `RunCancelled` from `RunPhase.Schema` → `TestIssue718.../Cancelled` FAIL.
  2. Renaming the shadow field's JSON tag (`phase` → `runPhase`) → `TestIssue718.../{Running,Paused,Succeeded,Cancelled}` FAIL.
  3. Dropping `v1.RunCancelled` from `runstate.Record.Terminal` → `TestRunSpanOnCancel` FAIL.
- **Spec in sync**: the committed `api/openapi/funcd.v1alpha1.yaml` matches the served spec. This is shown by the
  drift check in `internal/controlplane/api_test.go`, which passes.
- **Scope**: every hunk serves the issue. The retyped engine constants and `runstate.Record.Phase` replace the
  hard-coded `"Succeeded"`/`"Cancelled"` strings with typed constants. The test edits are only the type renames that
  the retype forces (`v1.Phase*` → `v1.Run*`), and no assertion was weakened.
- **Reuse**: `RunPhase.Schema` uses the package's existing `enumSchema` helper. It follows the `StepPhase` precedent
  in the same file. No new helper or dependency was added.
- **Siblings**: no other production code stores a value outside the enum in a `v1.Phase`. A search for out-of-enum
  `v1.Phase` literals in `internal`, `pkg` and `cmd` found nothing.
- **ADRs**: no ADR file was edited. ADR-0094's run phases match the new enum one-to-one.
- **Checks** (touched packages, through the pinned dev shell):
  - `go test -race` on `api/types/v1alpha1`, `internal/controlplane/...`, `internal/workflow/...` and `cmd/funcdctl`: all ok.
  - `go vet` on the same packages: clean.
  - `golangci-lint` on the same packages: 0 issues.
- **Shape**: the subject is `fix(workflow): …`, the commit has `Fixes #718` and the attribution trailer, and it covers
  one issue in one commit.

### Definition of Done

12/12 applicable items hold. The Linux lint, the e2e suite and `ci-full` are left to the group gate, as the review
scope requires.

### Model scorecard

| verdict | blockers | majors | minors | model-attributed | DoD |
|---|---|---|---|---|---|
| pass | 0 | 0 | 1 | 1 | 12/12 |

### Recommendation

Merge with the group. If the PR is touched again, the test idiom (`map[string]any` and checking the `NewObject`
error) can be aligned with the neighbouring tests.
