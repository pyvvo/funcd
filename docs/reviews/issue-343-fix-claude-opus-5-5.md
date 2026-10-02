## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #343 fix, model: claude-opus-5-5)

Change: `fix/i343`, one commit `fc71fbe fix(config): default workflow.payloadLimit to ADR-0094's 256 KiB`.
Touched files: `internal/platform/config/config.go` (one default + its comment) and
`internal/platform/config/config_test.go` (the regression test).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

Observation (out of scope, not a finding): the doc comment on the `Workflow` config struct
(`internal/platform/config/config.go:203`) still says `PayloadLimit` is "reserved … not yet enforced",
while `internal/workflow/engine.go` and the WorkflowRun admission in `pkg/funcd/funcd.go` do enforce it.
The comment predates this change; it is worth a separate cleanup, not a change to this fix.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the `origin/main` `config.go` overlaid
  (`go test -overlay … -run TestIssue343 ./internal/platform/config/`), the test fails with
  `expected: 262144` / `actual: 1048576` — the 1 MiB default the issue reports. (An overlay was used instead of
  `git revert` of the whole commit, because that revert also removes the test.)
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/platform/config/` → `ok`.
  The test is not skipped.
- **Mutants are killed.** Default changed to `256 << 11` → FAIL (`actual: 524288`). Default changed to `0`
  (unbounded) → FAIL (`actual: 0`).
- **Root cause fixed.** The only source of the default is `defaults()` in `internal/platform/config/config.go`. The
  value flows unchanged through `cmd/funcd/main.go:327` → `funcd.WithWorkflow` → `workflow.Config.PayloadLimit`
  (engine input/step-output caps) and `admission.NewWorkflowRunPayloadAdmission`. Setting it to `256 << 10`
  fixes the user-visible default at its source. It does not mask the problem downstream.
- **Override still works.** The test's second half sets `FUNCD_WORKFLOW_PAYLOAD_LIMIT=1048576` and asserts the
  explicit value wins. This keeps the issue's "an explicit value overrides" expectation covered.
- **ADR conformance.** This matches ADR-0094's Input model (`workflow.payloadLimit` default 256 KiB). No ADR file
  was edited. No living doc states a 1 MiB payload default (grepped the docs outside `docs/adr/` and
  `docs/reviews/`), so no doc goes stale.
- **Scope.** Two hunks, both for the issue: the value and its adjacent comment. No test was weakened or deleted.
- **Reuse.** The test uses the existing `config.Load("", config.Flags{})` + `t.Setenv` pattern, as other tests
  in `config_test.go` do. It adds no new helper, type or dependency.
- **Conventions.** It uses `require` as the file does, has a one-line why-comment and no comment bloat, adds no imports, and has no YAML.
- **Checks (touched package).** `go vet ./internal/platform/config/` clean. `golangci-lint run
  ./internal/platform/config/...` → `0 issues.` The repo-wide and Linux gate runs once in the group gate.
- **Commit shape.** Subject `fix(config): …`, body with Cause/Fix/Test, `Fixes #343`, and the attribution trailer. The commit holds one issue.

### Recommendation
Pass. Hand back to `/fix` for the group PR.
