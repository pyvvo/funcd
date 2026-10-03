## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #505 fix, model: claude-opus-5-5)

Change: `371ac7e fix(function): restart the fixed-port shim test helper when its reserved port is taken`, one file
(`internal/function/shim_test.go`, +50/-11). Issue #505: `shimReadyOnFixedPort` reserves a free port, releases it,
then starts the Node shim on it, so another process can take the port first and the shim exits on EADDRINUSE.

### Minor 1 — the regression test's failure message says the opposite of what failed  ·  attribution: model
`internal/function/shim_test.go:677`: `require.True(t, shimReadyOnFixedPort(...), "the shim reported readiness although
another listener took its first port")`. The message prints when the shim did **not** become ready (mutants m1 and m2
below print exactly this), so it misleads whoever reads the failure. Suggested wording: "the shim did not start after
another listener took its first port". Builder: `/fix`, optional.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** The change is test-only, so a literal
  `git revert --no-commit 371ac7e` removes the regression test with the fix (`ok … [no tests to run]`); the worktree
  was then reset to `371ac7e` and is clean. The pre-fix behavior was reproduced with an overlay that keeps the hook
  and the new test but restores the single attempt of `origin/main` (`const attempts = 1`):
  `--- FAIL: TestIssue505_FixedPortShimStartsWhenItsReservedPortIsTaken` at `require.True` — the shim exits on the
  taken port and the helper returns false, as the issue's failure path describes.
- **Passes with the fix, un-skipped, under `-race`.** `go test -race -count=1 -run
  'TestIssue505|TestScenarioShimFixedPortBindNode|TestIssue452_RealShimWaitsOutASlowBoot' -v ./internal/function/`:
  all PASS (TestIssue505 1.14s, so the node lane ran; not skipped). Whole package under `-race`: `ok`.
- **Mutants — all killed.**
  - m1 `const attempts = 5` → `1` (no restart): TestIssue505 FAIL.
  - m2 drop `cmd.Stderr = &stderr` (EADDRINUSE never detected): TestIssue505 FAIL.
  - m3 `http.Client{Timeout: time.Second}` → `http.Client{}` (unbounded probe): the package hangs on the
    non-answering listener until `-timeout 90s` panics — confirms the second defect the commit names (a probe that
    could hang) is real and now bounded. No stray shim process was left afterwards.
- **Cause, not symptom.** The shim must bind `FUNCD_PORT` itself (ADR-0030), so the helper cannot hand it an open
  listener; restarting only on an EADDRINUSE exit (not on any failure) is the targeted remedy, and a shim that fails
  for any other reason still fails the test at once. The retry is bounded (5 attempts) and does not lengthen any
  timeout.
- **Scope.** Every hunk serves #505: the retry loop, the extracted `runShimOnPort`, stderr capture, the bounded probe
  and the regression test. `TestScenarioShimFixedPortBindNode` and `TestIssue452…/fixed_port` keep their assertions;
  the variadic `beforeStart` hook leaves their call sites unchanged. No test weakened or deleted.
- **Reuse.** The loop mirrors the existing EADDRINUSE restart idiom of #463/#464
  (`internal/blob/s3gateway/harness_test.go:147`, `pkg/funcd/s3gateway_internal_test.go:100`): a bounded local
  `attempts` loop. Those detect the collision with `errors.Is(err, syscall.EADDRINUSE)` on an in-process bind; here
  the bind happens in a child Node process, so matching its stderr is the only available signal. No shared helper in
  `internal/testkit` covers either case, so nothing is duplicated.
- **Conventions.** Top-level imports, no comment bloat (doc comments state the why and cite #505 / ADR-0030 §4b),
  the removed `//nolint:gosec` is no longer needed (lint clean), naming follows the file. No YAML touched.
- **ADRs.** No ADR file touched; the boot budget is still `function.BootTimeout` (ADR-0030 §4b); nothing contradicts
  ADR-0030/0032.
- **Checks (touched package).** `go vet ./internal/function/` ok; `golangci-lint run ./internal/function/` 0 issues;
  `go test -race ./internal/function/` ok. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape.** `fix(function):` subject, `Fixes #505`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Optionally reword the failure message (Minor 1) before the PR; it does not block.
