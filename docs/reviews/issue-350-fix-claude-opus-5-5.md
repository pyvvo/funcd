# Fix review — issue #350 (claude-opus-5-5)

- **Issue**: #350 — `funcdctl dev` and the InMemory preset run workflow steps with no step timeout
- **Change**: branch `fix/i350`, commit 3b35d05 `fix(funcd): give the workflow engine the ADR-0094 defaults without WithWorkflow`
- **Files**: `pkg/funcd/funcd.go`, `pkg/funcd/options.go`, `pkg/funcd/funcd_test.go`
- **Verdict**: **pass**
- **Checklist**: 10 of 11 (item 8 partly deferred: Linux lint and e2e run in the group gate)

## Verification run

| Check | Result |
|---|---|
| Pre-fix code (revert of 3b35d05 with the test kept; also an overlay of the `origin/main` files) | `TestIssue350_…` FAILS: `expected: 5m0s, actual: 0s` on the step timeout. This is the reason the issue gives. |
| Fixed code, `go test -race ./pkg/funcd/ ./cmd/funcdctl/` | ok (both packages) |
| Mutant 1: `defaultWorkflowStepTimeout = 0` | killed (`expected 5m0s, actual 0s`) |
| Mutant 2: drop `workflowPayloadLimit` from the `New` seed | killed (`expected 1048576, actual 0`) |
| Mutant 3: `WithWorkflow` ignores an explicit zero step timeout | killed (`Should be zero, but was 5m0s`) |
| `go build ./...`, `go vet ./pkg/funcd/`, `golangci-lint run ./pkg/funcd/...` | clean, 0 issues |
| Worktree after the review | at 3b35d05, clean |

## Blockers

None.

## Majors

None.

## Minors

1. **The default values are restated, not sourced** (attribution: `model`). The fix adds four literals
   (`300s`, `720h`, `1`, `1 << 20`) in `pkg/funcd/funcd.go`. The same values already exist in
   `internal/platform/config` `defaults()`. Importing the daemon config package from the library would
   couple the two, and `defaults()` is unexported, so restating the values is a reasonable choice. The
   regression test also pins the four values to `platformconfig.Load("", …)`, so a drift fails the test.
   No change required. A future cleanup could export one shared set of workflow defaults.

## Verified correct

- **Root cause fixed.** `New` now seeds `config` with the ADR-0094 defaults before the options run.
  The issue names this cause: only `cmd/funcd` filled `workflow.*`. `funcdctl dev`, the InMemory preset
  and any library embedding without `WithWorkflow` now pass a 300 s step timeout, a 1 MiB payload cap and
  a 720 h retention into `workflow.Config` and the retention sweep (`funcd.go`, the `workflow.New` call
  and `p.workflowRetention`). Nothing is masked: the change does not add a longer timeout or an extra retry.
- **The override semantics are kept.** `WithWorkflow` still sets every field, so explicit zeros keep their
  documented meaning (none or unbounded) in any option order. The test asserts this and mutant 3 shows it.
- **No behavior change for the daemon.** `cmd/funcd` always passes `WithWorkflow` with config values.
- **Scope.** Every hunk serves the issue. The `WithWorkflow` doc comment is updated to describe the new
  defaults, and no test was weakened.
- **ADRs.** The change aligns the library with ADR-0094's Wiring defaults and contradicts no Accepted or
  Implemented ADR. No ADR file was edited.
- **Conventions.** It follows the existing `default*` constant block, a top-level import (with an alias),
  short comments that explain the reason, and `t.Parallel()`.
- **Commit shape.** The subject is `fix(funcd): …`, the body has `Fixes #350`, and the commit carries the
  attribution trailer and touches one issue.

## Recommendation

Pass. Hand back to `/fix` for the PR. The group gate still runs the repo-wide, Linux-lint and e2e checks.
