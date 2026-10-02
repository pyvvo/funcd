# Fix review — issue #288 (s3 gateway tests time out when another test takes the reserved port)

- **Change**: branch `fix/i288`, commit `39fc471` `fix(s3): stop s3 gateway tests timing out when another test takes the reserved port`
- **Touched**: `pkg/funcd/s3gateway_internal_test.go` only (test-only fix)
- **Producing model**: claude-opus-5-5
- **Verdict**: **changes-requested**

## Summary

The new `startS3Gateway` harness surfaces the gateway's `Run` error, retries only on `EADDRINUSE` and fails at
once on anything else, and the regression test reproduces the collision deterministically. But moving
`TestScenarioS3GatewayEnabledOpensListener` onto the harness changed it from driving the platform's `Run` to
driving `p.s3gw.Run` directly, so the ADR-0080/0085 scenario no longer checks that the platform starts the
gateway. A mutant that deletes that wiring now passes the package's whole non-e2e suite.

## 🔴 Blocker

1. **Weakened scenario test: the platform's gateway wiring is no longer covered** — `model`.
   `TestScenarioS3GatewayEnabledOpensListener` (doc: "once Run binds the node-private addr, a TCP dial
   succeeds") ran `p.Run(ctx)` on `origin/main`. It now goes through `startS3Gateway`, which calls
   `p.s3gw.Run(runCtx)` itself (`pkg/funcd/s3gateway_internal_test.go:80`). As a result the `p.s3gw.Run` goroutine in
   `Platform.Run` (`pkg/funcd/funcd.go:1096-1103`) is no longer under test.
   Evidence (mutant M3, which replaces that goroutine's body with a no-op):
   - with the fixed test file, `go test -run 'S3Gateway|Issue109|Issue288' ./pkg/funcd/` gives `ok`. The full
     `go test ./pkg/funcd/` also has no other test that builds a gateway (`WithS3Gateway` appears only in the e2e-tagged `site_e2e_test.go`).
   - with the `origin/main` test file, `--- FAIL: TestScenarioS3GatewayEnabledOpensListener … s3 gateway did not become ready`.
   Fix direction: keep the scenario on the platform's `Run`. The scenario still needs a way to detect a
   collision, because `Platform.Run` logs and drops the gateway error. For example, inject a `WithLogger`
   handler that records the "s3 gateway stopped" error and retry on `EADDRINUSE`, or add a second, `p.Run`-driven
   assertion. Leave `TestIssue109_…` on the direct harness.

## 🟡 Major

None.

## Minor

1. **The same reserve-then-bind race remains in the e2e site test** — `model` (scope note).
   `pkg/funcd/site_e2e_test.go:103` still calls `freeLoopbackAddr` and then `funcd.New(… WithS3Gateway(s3Addr, …))`
   and `p.Run`, and has no collision handling. The issue's Test section names only the tests that wait on
   `p.s3gw.Ready()`, so this test is outside the issue's letter. However, the commit message says "both pkg/funcd
   gateway tests use it", which does not mention this third gateway user. At least record it, or file a
   follow-up.

## ✅ Verified correct

- **Pre-fix shape fails for the issue's reason.** `git revert --no-commit 39fc471` removes the regression test with
  the harness (`[no tests to run]`), because this is a test-only fix. I therefore applied an overlay that restores the
  pre-fix harness shape: `Run` error dropped, Ready-only wait, no retry. With that overlay,
  `TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken` fails after 10.07 s with
  `s3 gateway did not become ready`, which is exactly the CI symptom in the issue.
- **Passes with the fix under `-race`**: all four gateway tests pass (`TestIssue288_…` 0.24 s). The full
  `go test -race ./pkg/funcd/` gives `ok`. After `git reset --hard 39fc471` the worktree is clean at that HEAD.
- **Mutants on the fix's key lines both fail the regression test**:
  - M1 `attempts = 5 → 1`: FAIL, `bind: address already in use`.
  - M2 `syscall.EADDRINUSE → syscall.ECONNREFUSED`: FAIL, the same error.
  These failures also show that `errors.Is` sees `EADDRINUSE` through `fault.Wrapf` and versitygw's wrapping.
- **Cause, not symptom**: the change does not lengthen any timeout. It replaces the silent readiness timeout with
  the real `Run` error, and it retries only the specific collision. The reservation cannot be handed to versitygw:
  `ServeMultiPort` takes address strings (`internal/blob/s3gateway/s3gateway.go:158`, ADR-0085). Retrying on a fresh
  port is therefore the narrowest root-cause handling available without changing the gateway.
- **Cleanup**: on every failed attempt the harness cancels the run and shuts down the platform. On success it
  drains `runErr` before `Shutdown`, so no goroutine leak shows under `-race`. The regression test uses
  `os.MkdirTemp("", "funcd")` for the data dir, which follows the #41 socket-path rule.
- **Reuse**: no existing port-retry or listener-handover helper exists in `internal/testkit` or the package.
  `freeLoopbackAddr` is reused. The harness also removes the two copies of the start/Ready block, so it reduces
  duplication.
- **Conventions**: imports are at top level, comments are short and about why, and nothing is added outside the
  test. `go vet ./pkg/funcd/` is clean, `golangci-lint run ./pkg/funcd/...` reports 0 issues, and `gofmt -l` is clean.
- **ADRs**: no ADR file was touched. ADR-0080/0085 are not contradicted.
- **Shape**: the commit has the subject `fix(s3): …`, contains `Fixes #288` and the attribution trailer, and covers one issue in one commit.

## Checklist (Definition of Done)

| # | Item | Result |
|---|---|---|
| 1 | `TestIssue288_…` reproduces the issue | ✅ |
| 2 | Fails on pre-fix shape for the reported reason | ✅ (overlay; 10 s readiness timeout) |
| 3 | Passes with fix, un-skipped, `-race` | ✅ |
| 4 | Reverting/mutating key lines fails a test | ✅ (M1, M2) |
| 5 | Root cause fixed, not masked | ✅ |
| 6 | Only issue scope; no test weakened | ❌ (Blocker 1) |
| 7 | No ADR contradicted or edited | ✅ |
| 8 | Build/vet/lint/tests green (touched package, host; Linux lint + e2e left to the group gate) | ✅ |
| 9 | Conventions | ✅ |
| 10 | Reuse, no duplication | ✅ |
| 11 | Commit shape | ✅ |

**10 / 11.**

## Recommendation

Return to `/fix`. Restore platform-`Run` coverage in `TestScenarioS3GatewayEnabledOpensListener` and keep the
collision handling. Then confirm that mutant M3, which removes the gateway goroutine from `Platform.Run`, fails a
non-e2e test again. Optionally, note or follow up on the race that remains in the e2e site test.
