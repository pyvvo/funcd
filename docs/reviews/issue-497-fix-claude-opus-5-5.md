# Fix review: issue #497, funcd keeps running without its S3 gateway when the gateway cannot bind its port

- **Change**: branch `fix/i497`, commit 92d01ce `fix(funcd): fail Run at startup when the S3 gateway cannot bind its port`
- **Producing model**: claude-opus-5-5
- **Files**: `pkg/funcd/funcd.go`, `pkg/funcd/s3gateway_internal_test.go`
- **Governing ADRs**: ADR-0028 (crash-only lifecycle), ADR-0080 and ADR-0085 (S3 gateway bind address and lifecycle), ADR-0002 (conventions)

## Verdict: **pass**

Blockers 0, Majors 0, Minors 0. Fix checklist: 11 of 11 items hold.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **The regression test fails without the fix.** With `git revert --no-commit 92d01ce` applied to `pkg/funcd/funcd.go` and the
  new test kept, `TestIssue497_RunFailsWhenS3GatewayCannotBind` fails after 10 s with "Run kept serving the node without its
  S3 gateway". The log shows the issue's symptom: `s3 gateway stopped ... bind: address already in use`, and then the platform
  keeps running. This is the reported reason.
- **The test passes with the fix under `-race`.** After `git reset --hard 92d01ce`, the test passes in 0.15 s. The neighbouring
  gateway tests (`TestScenarioS3GatewayDisabledByDefault`, `TestScenarioS3GatewayEnabledOpensListener`,
  `TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken`) also pass.
- **The root cause is fixed.** The issue names the cause: `Platform.Run` only logged the gateway's `Run` error, and versitygw
  binds inside `Run`. The fix starts the gateway first and blocks on the existing `s3gateway.Server.Wait`. When the gateway
  stops before it is ready, `Run` waits for the gateway goroutine, calls `Shutdown` (which closes the control-plane and
  data-plane listeners and the drivers), and returns the wrapped bind error. `cmd/funcd/main.go` returns any `Run` error, so
  the daemon now exits at startup. This is the same behavior as for a taken control-plane or data-plane port. The fix does
  not add a timeout, a retry or a swallowed error.
- **The `funcdctl dev` path still holds.** `cmd/funcdctl/dev.go` runs `Run` in a goroutine and calls `WaitS3Gateway`. Both now
  report the bind error, and the deferred `cancelRun` and `<-runErr` still complete. `TestIssue432_DevFailsWhenS3PortIsTaken`
  (build tag `dev`) passes under `-race`.
- **Mutants**: each of the three overlay mutants fails a test.
  1. The error path returns without `p.Shutdown`. The test fails because the control plane is still listening.
  2. The error is wrapped with a message instead of `fault.Wrapf`, so the cause is lost. The test fails because the error is not
     `EADDRINUSE`.
  3. The `ctx.Err() == nil` guard is inverted. The test fails because Run keeps serving the node.
- **Scope**: every hunk serves the issue. The gateway start block moved ahead of the other loops, the `Run` and
  `WaitS3Gateway` doc comments were updated, the `platformRun` helper comment was corrected, and the new test was added. No
  test was weakened or deleted.
- **Reuse**: the fix uses the existing `s3gateway.Server.Wait` (the same primitive behind `WaitS3Gateway` and #432),
  `fault.Wrapf`/`fault.KindOf`, `closeTimeout`, and the idempotent `Shutdown`. The three-line close-context idiom repeats the
  one at the end of `Run`. That is the local idiom and too small to extract. No new helper, type or dependency was added.
- **Conventions (ADR-0002, CLAUDE.md)**: errors use `api/fault` with the original kind kept. ctx comes first. Logging uses
  slog only. The comments are short and give the reasons (#497, ADR-0028). The test uses `os.MkdirTemp("", "funcd")`, not
  `t.TempDir()`, and an `InMemory()` platform.
- **ADRs**: the fix contradicts no Accepted or Implemented ADR. It applies the crash-only lifecycle of ADR-0028 and keeps the
  ADR-0085 rule that the gateway binds in `Run`. No ADR file was edited.
- **Checks (the touched package)**: `go test -race ./pkg/funcd/` passes, `go vet ./pkg/funcd/` is clean, and
  `golangci-lint run ./pkg/funcd/...` reports 0 issues. The repo-wide gate, Linux lint and e2e are left to the group gate.
- **Shape**: the commit has a conventional `fix(funcd):` subject, a body that states the cause, `Fixes #497`, and the
  attribution trailer. It is one issue per commit.
- **Worktree**: left at 92d01ce and clean.

## Recommendation

Merge with the wave's group PR after the group gate passes.
