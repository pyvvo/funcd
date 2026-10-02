# Fix review (round 2) — issue #288 (s3 gateway tests time out when another test takes the reserved port)

- **Change**: branch `fix/i288`, commits `39fc471` `fix(s3): stop s3 gateway tests timing out when another test takes the reserved port` and `1897a3e` `fix(s3): address review of #288`
- **Touched**: `pkg/funcd/s3gateway_internal_test.go` only (test-only fix)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Summary

The round-1 Blocker is resolved. `TestScenarioS3GatewayEnabledOpensListener` drives `Platform.Run` again, through
`platformRun`. That helper reads the gateway's `Run` error from the platform's "s3 gateway stopped" log record by
an injected `slog` handler, so the scenario also retries a bind collision. The mutant that removes the gateway
goroutine from `Platform.Run` now fails two tests. The regression test covers both ways of serving the gateway.

## 🔴 Blocker

None.

## 🟡 Major

None.

## Minor

1. **The reserve-then-bind race remains in the e2e site test** — `model` (scope note, carried from round 1).
   `pkg/funcd/site_e2e_test.go:103` still reserves the port with `freeLoopbackAddr` and has no collision handling.
   The commit message of `1897a3e` now records this and leaves it for a follow-up. No follow-up issue is linked yet.
   The group step should file one.

## Round-1 findings

| Round-1 finding | Status | Evidence |
|---|---|---|
| Blocker 1: the scenario no longer covered the platform's gateway wiring | Resolved | Mutant M3 (`pkg/funcd/funcd.go:1100`, `p.s3gw.Run(ctx)` replaced by `error(nil)`) fails `TestScenarioS3GatewayEnabledOpensListener` and `TestIssue288_…/platform_Run` (readiness timeout) |
| Minor 1: the e2e site test race | Acknowledged | Recorded in the `1897a3e` commit message as a follow-up |

## ✅ Verified correct

- **Revert check.** `git revert --no-commit 1897a3e 39fc471` removes the regression test together with the
  harness, so `-run Issue288` gives `[no tests to run]`. This is a test-only fix. I therefore applied an overlay
  that restores the pre-fix shape (I deleted the `case err = <-runErr:` arm, so the `Run` error is dropped and only
  the Ready wait remains). With that overlay both subtests fail after 10.05 s with `s3 gateway did not become ready`,
  which is the CI symptom in the issue. After `git reset --hard 1897a3e` the worktree is clean at that HEAD.
- **Passes with the fix under `-race`.** All four gateway tests pass: `TestIssue288_…` takes 0.51 s and has
  two subtests, `gateway_Run` and `platform_Run`. The full `go test -race ./pkg/funcd/` gives `ok`.
- **Mutants** (each was restored after the run):
  - M3: remove the gateway goroutine from `Platform.Run`. `TestScenarioS3GatewayEnabledOpensListener` and `TestIssue288_…/platform_Run` FAIL.
  - M4: make the `gatewayStopped` handler match a different message. `TestIssue288_…/platform_Run` FAILS, because the collision is no longer seen.
  - M5: change `syscall.EADDRINUSE` to `ECONNREFUSED`. Both subtests FAIL on the bind error.
  M4 and M5 together show that the error reaches the retry decision through both the log record and the
  `fault.Wrapf`/versitygw wrapping.
- **Cause, not symptom.** No timeout was lengthened. The silent readiness timeout is replaced by the real `Run`
  error, and only `EADDRINUSE` is retried, up to 5 attempts. versitygw takes address strings (ADR-0085), so the
  reservation cannot be handed over without changing the gateway. Retrying on a fresh port is the narrowest fix.
- **Test-only observation of the platform.** The handler is test code. `Platform.Run` and its logging are unchanged,
  and the handler uses the public `WithLogger` option (`pkg/funcd/options.go:392`).
- **Cleanup.** A failed attempt cancels the run and shuts the platform down. On success, the cleanup cancels the
  run, drains `runErr` and then calls `Shutdown`. `platformRun` stops and drains `p.Run` before it returns, and
  `-race` shows no leak. The regression test uses `os.MkdirTemp("", "funcd")` (the #41 rule).
- **Reuse (2.7).** No port-retry or listener-handover helper exists in `internal/testkit` or in the package. The
  change reuses `freeLoopbackAddr`. One harness replaces the two copies of the start/Ready block.
- **Conventions (2.8).** Imports are at top level. The comments are short and explain why. `go vet ./pkg/funcd/` is
  clean, `golangci-lint run ./pkg/funcd/...` reports 0 issues, and `gofmt -l` is clean. The touched file contains no
  absolute path or username.
- **ADRs.** No ADR file was touched, and ADR-0080/0085 are not contradicted.
- **Shape.** `39fc471` carries `Fixes #288` and `1897a3e` carries `Refs #288`. Both commits have the attribution
  trailer.

## Checklist (Definition of Done)

| # | Item | Result |
|---|---|---|
| 1 | `TestIssue288_…` reproduces the issue | ✅ |
| 2 | Fails on the pre-fix shape for the reported reason | ✅ (overlay; 10 s readiness timeout) |
| 3 | Passes with the fix, not skipped, under `-race` | ✅ |
| 4 | Reverting or mutating the key lines fails a test | ✅ (M3, M4, M5) |
| 5 | Root cause fixed, not masked | ✅ |
| 6 | Only the issue's scope; no test weakened | ✅ (platform-`Run` coverage restored) |
| 7 | No ADR contradicted or edited | ✅ |
| 8 | Build, vet, lint and tests green (touched package, on the host; Linux lint and e2e are left to the group gate) | ✅ |
| 9 | Conventions | ✅ |
| 10 | Reuse, no duplication | ✅ |
| 11 | Commit shape | ✅ |

**11 / 11.**

## Recommendation

Pass. File a follow-up issue for the reserve-then-bind race in `pkg/funcd/site_e2e_test.go`.
