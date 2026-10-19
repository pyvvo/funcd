## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1082 fix, model: claude-opus-5-5)

This report reviews branch `fix/1082-dependency-check-flake`, commit 2ea22aaa
`test(e2e): read the shim's readiness until it carries funcd's dependency report` (`git diff origin/main...HEAD`:
`pkg/funcd/health_e2e_test.go`). Issue: "TestScenarioAppDependencyCheck flakes: the shim's 50 ms dependency budget
expires under load". The review ran in the fixer's session, not in a separate agent, as the user asked for on
2026-10-11; the DR session's check before merge is the independent look.

### Minor
- **The first version of the regression test did not pin which reports are retried** · attribution: model ·
  evidence: two overlay mutants of `readiness`'s condition, `return rep.Kind != "socket"` and
  `return rep.Reason != "Timeout"`, passed the first test, which covered only a socket/Timeout report followed by
  funcd's kv/Forbidden. Fixed before this report: the test now also asserts that a socket report with another
  reason and a kv binding's own Timeout come back at once. All three mutants (the two above and `return true`) now
  fail it.
- **The subject is `test(e2e):`, not `fix(<scope>):`** · attribution: model · evidence: `git log -1 --format=%s`.
  The change touches only a test, so the fix belongs in no release note. The repository already types flake fixes
  this way: #1001, `test(function): the pool tests no longer read a loaded host's slow probe as an unready worker`.
  `Fixes #1082` and the attribution trailer are present.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With the `origin/main` `readiness` helper
  overlaid and the new test kept, `go test -tags e2e -overlay … -run TestIssue1082 ./pkg/funcd/` fails: expected
  kv/audit/Forbidden, actual socket/Timeout. That is the failure the issue quotes from the gate log.
- **It passes with the fix**, un-skipped, under `-race`, three runs in a row (`-count=3`).
  `TestScenarioAppDependencyCheck` passes on the real node shim.
- **Cause, not symptom.** The defect is the test's own assumption that one readiness call yields funcd's report. The
  shim's socket/Timeout report means funcd's answer missed the shim's 50 ms budget (ADR-0215 Decisions 3 and 4), and
  the reconciler's probe already retries it. `readiness` now asks again only for that report and returns every other
  one at once. The 50 ms rule is unchanged, no timeout grew, and no assertion was weakened: the test still asserts
  the exact kv/Forbidden report and its message.
- **Scope.** One file, one helper and its regression test. `readiness` has a single caller.
- **Siblings.** `TestScenarioEngineNotReadyUnavailable` (`pkg/funcd/sql_access_internal_test.go`) reads funcd's
  own `/health/dependencies`, not the shim's readiness, so the shim's budget does not apply. The
  `internal/function` health reports come from fakes. `health_internal_test.go` already polls with
  `require.Eventually`.
- **Reuse.** The polling uses `require.Eventually`, as `revisionWorker` beside it does, with the file's `appWithin`
  bound. The fake shim is a stdlib `httptest.Server`.
- **ADRs.** ADR-0215's budget and report shapes are untouched. No ADR file was edited.
- **Checks.** `go vet -tags e2e` and `golangci-lint --build-tags e2e` on `pkg/funcd` are clean.
  `scripts/agent/gate.sh` on the final commit: audit, ci-full (e2e included), linux, linux-tests and tree-unchanged
  all PASS.

### Definition of Done
12 / 12 items hold. Item 4 holds for the helper's key line, after the test was extended for the first Minor. Item 11
holds with the `test(e2e):` type explained in the second Minor.

### Model scorecard
Recorded: claude-opus-5-5 on #1082 (fix) → pass, 0/0/2, 2 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR.
