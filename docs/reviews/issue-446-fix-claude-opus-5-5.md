## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #446 fix, model: claude-opus-5-5)

Change: branch `fix/i446`, commit 80bceca `fix(funcd): bound the eventing DLQ by default without WithDeadLetterQueue`
(`pkg/funcd/funcd.go`, `pkg/funcd/options.go`, `pkg/funcd/funcd_test.go`; +34/−5).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observation (not scored): the regression test asserts the platform fields (`deadletterRetention`,
`deadletterMaxEntries`), not an actual eviction. The pre-existing sweep guard at `pkg/funcd/funcd.go:1111`
starts `runDeadLetterRetention` whenever either value is > 0, so the fields are the whole cause; this mirrors
the accepted shape of `TestIssue350_WorkflowDefaultsWithoutWithWorkflow`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 80bceca` with the fix's test file
  kept: `TestIssue446_DeadLetterDefaultsWithoutWithDeadLetterQueue` FAILS with
  `expected: 720h0m0s / actual: 0s` (retention 0 ⇒ "never", so no sweep). Worktree reset to 80bceca, clean.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue446_…` (`ok pkg/funcd`).
- **Mutants (3/3 killed)**: retention default 720h → 24h; the `deadletterMaxEntries: defaultDeadletterMaxEntries`
  line deleted; `WithDeadLetterQueue` no longer assigning `maxEntries` (explicit-zero override). Each fails
  `TestIssue446_…`; all files restored.
- **Cause, not symptom**: `New` now seeds `deadletterRetention`/`deadletterMaxEntries` alongside the
  `workflow*` defaults (`funcd.go:340-341`), exactly the cause the issue names (`funcd.go:330-336`). An explicit
  `WithDeadLetterQueue(…, 0, 0)` still means no TTL / no cap, and the test pins that.
- **Defaults match the daemon**: the test loads `platformconfig.Load("", Flags{})` and compares against
  `Eventing.Deadletter.Retention`/`MaxEntries` (720h / 1000, `internal/platform/config/config.go:281-282`), so a
  later change to the daemon default without the library default fails the test.
- **Option doc claim checked**: "three delivery attempts" without the option holds — `internal/sensor/sensor.go:33`
  `defaultDeliveryAttempts = 3`.
- **Scope**: every hunk serves #446; no test weakened or deleted. `cmd/funcd/main.go:333` still passes the
  config values explicitly, so daemon behavior is unchanged.
- **Reuse**: the two constants follow the #350 precedent (`defaultWorkflowRetention` etc. in the same const
  block); `pkg/funcd` does not import `internal/platform/config` in production code, so a const block is the
  established way; no helper or dependency added.
- **Conventions (ADR-0002, CLAUDE.md)**: no new exported API, no `any`, no new imports, comments state the why
  (ADR-0118 parity) without bloat; naming matches `defaultWorkflow*`.
- **ADRs**: consistent with ADR-0118 Decision 5 (TTL + per-namespace cap, swept periodically) and ADR-0125;
  no ADR file touched.
- **Checks (touched package)**: `go vet ./pkg/funcd/` ok; `golangci-lint run ./pkg/funcd/...` 0 issues;
  `go test -race -count=1 ./pkg/funcd/` ok. Repo-wide, Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(funcd):` subject, cause/fix/test body, `Fixes #446`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 covers the host checks for the touched package; the Linux lint,
repo-wide tests and e2e run in the group gate.

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #446 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11
(not recorded here; the orchestrator writes the ledger).

### Recommendation
Ship as is in the eventing group PR.
