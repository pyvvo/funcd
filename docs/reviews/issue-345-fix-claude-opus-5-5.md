## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #345 fix, model: claude-opus-5-5)

Change: `fix/i345`, one commit `407ce60 fix(config): describe workflow retention and payloadLimit as enforced`.
Touched files: `internal/platform/config/config.go` (the `Config.Workflow` doc comment),
`internal/platform/config/config_test.go` (the regression test) and `pkg/funcd/funcd.go` (the same stale
claim on the Platform's workflow config fields).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### 🟢 Minors
- **The test's `reserved` ban is broad** (`internal/platform/config/config_test.go`, `TestIssue345_…`). The
  test fails if the word "reserved" appears anywhere in the `Config.Workflow` doc comment, in any sense. A later,
  correct comment such as "a reserved key name" would fail it. The precise stale phrase is "not yet enforced"
  (already checked) or "reserved for the run-gc". This is cosmetic and does not block. Attribution: `model`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the `origin/main` `config.go` overlaid
  (`go test -overlay … -run TestIssue345 ./internal/platform/config/`), the test fails with
  `… are reserved for the run-gc / admission gates, not yet enforced. … should not contain "not yet enforced"`.
  This is the exact stale sentence that the issue reports. An overlay was used because `git revert --no-commit 407ce60`
  also removes the test (with the full revert the run reports `[no tests to run]`). The worktree was then reset to
  `407ce60` and left clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/platform/config/` → `ok`. The test is not skipped.
- **Mutants are killed.** (1) "not yet enforced" re-inserted into the new comment → FAIL. (2) The
  `PayloadLimit` key name removed from the comment → FAIL (the `Contains` check).
- **The new comment is true.** It was checked against the code:
  - Retention: `pkg/funcd/funcd.go:1076` starts the sweep only when `workflowRetention > 0` ("0 ⇒ no sweep").
    `runWorkflowRetention` (`funcd.go:1333`) calls `SweepExpired`, and its own doc says the sweep reclaims
    "engine records and WorkflowRun objects alike".
  - PayloadLimit: `internal/workflow/engine.go:254` checks the run input at run start,
    `engine.go:787` checks each step output, and `funcd.go:898` wires `admission.NewWorkflowRunPayloadAdmission`.
    Each check is guarded by `> 0` ("0 ⇒ unbounded"). This matches `workflow.Config.PayloadLimit`'s own doc
    (`engine.go:150`).
- **Root cause fixed.** The defect is the stale text, and the text is replaced. The duplicate stale claim in
  `pkg/funcd/funcd.go` is corrected in the same commit, so no copy is left behind. No other living doc
  (blueprint, docs outside `docs/adr/`) carries the claim.
- **Scope.** Three hunks, all for the issue. No behavior change, and no test was weakened or deleted.
- **ADR conformance.** This matches ADR-0094 (run retention and payload limit). No ADR file was edited.
- **Reuse.** The test uses only the standard library (`go/parser`, `go/ast`, `embed`) and `require`, as the
  file already does. The repository has no existing doc-comment check helper to reuse. It adds no new helper,
  type or dependency.
- **Conventions.** Imports are at the top level, the test has one why-comment, and the new comments are concise. No YAML.
- **Checks (touched packages).** `go vet ./internal/platform/config/ ./pkg/funcd/` clean,
  `golangci-lint run ./internal/platform/config/ ./pkg/funcd/` → `0 issues.`, `go build ./pkg/funcd/` ok.
  Repo-wide tests, Linux lint and lanes are left to the group gate.
- **Shape.** Subject `fix(config): …`, body `Fixes #345`, attribution trailer, one issue in one commit.

### Fix checklist
11 of 11 hold (item 8 at the touched-package scope; the group gate runs the rest).

### Recommendation
Pass. Narrowing the `reserved` check to the exact stale phrase is optional and can be done with this
change or later.
