## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #143 fix, model: claude-opus-5-5)

Commit 01b2a16 `fix(runtime): stop process-driver workers in parallel on Close`, on branch
`fix/202-graceful-shutdown` (reviewed at d1614a1; only #143's commit is in scope). Touched files:
`internal/runtime/process/process.go` (Close) and the new `internal/runtime/process/close_internal_test.go`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1: the regression test does not check that Close waits for the workers · attribution: model.**
  Overlay mutant: replace `wg.Wait()` in `Close` with `_ = &wg`. The mutant makes Close return before any worker is
  stopped. `go test -race -count=1 -overlay … ./internal/runtime/process/` → `ok` (1.4 s). The mutant
  survives. `TestIssue143_CloseStopsInstancesInParallel` only checks an upper bound (`elapsed < 2*stopGrace`).
  The pre-fix code had no test for this property either, so this is a gap that already existed and that the fix
  did not close. It is not a regression. Fix: after `d.Close()`, also assert that every instance's `Status` reads
  `runtime.StateStopped`, or that `elapsed >= stopGrace` for the workers that ignore SIGTERM. Either assertion
  kills the mutant.
- **Minor 2: daemon shutdown is still the HTTP drain plus the runtime stop, not one shutdownTimeout · attribution: adr (not scored).**
  `pkg/funcd/funcd.go:1151-1159` spends up to `shutdownTimeout` (15 s) on `dataPlaneServer`/`httpServer.Shutdown`.
  It then calls `Platform.Shutdown`, which calls `p.cfg.runtime.Close()` (`:1202`), and `runtime.Runtime.Close()`
  (`internal/runtime/runtime.go:108`) takes no context. After the fix, the worst case is about 15 s + 3 s. That
  bound is fixed and no longer grows with the worker count. The issue's suggested fix was "parallel and/or
  bounded by the shutdown context", and the fix delivers the parallel part, which removes the 3 s × N growth that
  was reported. To bound shutdown strictly by `shutdownTimeout`, the port's `Close` signature must change
  (`Close(ctx)`). That change is in the contract of the runtime port, so it needs an ADR if it is wanted.

### ✅ Verified correct (keep it)

- **It fails without the fix, for the issue's reason.** `git revert --no-commit 01b2a16`, with the new test file
  restored (the revert applied cleanly), then `go test -race -count=1 -run TestIssue143 ./internal/runtime/process/` →
  `FAIL`: `"9.006664209s" is not less than "6s"`, `Close took 9.006664209s for 3 workers that ignore SIGTERM`.
  That is exactly 3 s per worker, the cause the issue reports.
- **It passes with the fix, under `-race`.** `go test -race -count=3 -v -run TestIssue143 ./internal/runtime/process/` →
  3 × `PASS` (3.04 s each). The test is not skipped.
- **The user-visible behavior is fixed.** A driver-level probe of the issue's scenario (2 normal workers + 6 that
  ignore SIGTERM, then `Close`) gave `Close … 3.002656625s`. Before the fix, the same scenario took 18 s. All 8
  PIDs were gone after Close (`kill -0` fails), and every instance read `state=stopped`. No worker was left
  running. The real-daemon steps need a registry-backed bundle, so they were not rerun. The probe drives the same
  `Close` path that `Platform.Shutdown` calls.
- **The cause is fixed, not masked.** The fix removes the sequential loop. It keeps the 3 s grace, adds no
  timeout and adds no retry, and it ignores no new errors (the old loop also ignored the Signal/Kill errors).
- **Mutant on the key line.** Replacing `wg.Go(func() { … d.Stop … })` with a sequential `d.Stop(...)` call →
  `FAIL` (`9.005599167s is not less than 6s`). The test catches it.
- **Reuse.** Close now calls the driver's own `Stop` instead of keeping its own copy of the SIGTERM → grace →
  SIGKILL sequence. This removes duplicated logic, and Close now also sets `released` the same way Stop does
  (ADR-0143). The fix uses `sync.WaitGroup.Go` from the standard library (the module is on go 1.26.4). It adds no
  new helper, type or dependency.
- **Concurrency.** `Stop` takes `d.mu` for each state transition, and it handles an instance that is not running
  without error. An instance that exits between the snapshot and the call, or a `Stop` from the controller that
  runs at the same time, is therefore safe. The race detector is clean on the package and on the e2e suite.
- **Scope.** Two files, and every hunk serves #143. No test was weakened or deleted. No ADR file was touched.
- **ADRs.** The fix is consistent with ADR-0011 (process driver), ADR-0143 (Stop/Remove release semantics) and
  ADR-0028 (bounded graceful shutdown). The driver bound is now one stopGrace and no longer grows with N.
- **Conventions.** The doc comment on Close is one line that states why. The test is an `_internal_test.go` file,
  which this repo already uses, so that it can reference `stopGrace`. The code uses `fault` and is ctx-first as
  before, adds no new logging, and uses only top-level imports.
- **Checks.** `gofmt -l` is clean. `go build ./...` (host and `GOOS=linux`), `go vet ./internal/runtime/...` (host
  and Linux) and `golangci-lint run ./internal/runtime/...` (host and Linux) → `0 issues`.
  `go test -race ./internal/runtime/... ./internal/function/... ./cmd/...` → all `ok`. `just check-hygiene` →
  clean. E2E: `go test -race -tags e2e ./pkg/funcd/...` → `ok` (116 s) when the one test below is skipped.
  - **Environment note (attribution: env, not a finding against this fix):** `TestScenarioE2ETLSSelfSignedServesHTTPS`
    fails under `-race` with a DATA RACE in `net/http.http2ConfigureServer`, which is reached from two
    `Platform.Run` serve goroutines (`pkg/funcd/funcd.go:1135`). This failure reproduces on `origin/main` (b5f28fc),
    so it already existed and is unrelated to #143.
- **Commit shape.** `fix(runtime):` subject, `Fixes #143`, the Co-Authored-By trailer, and one issue in one commit.

### Definition of Done

11 / 11 items hold. Item 4 holds because the revert and the key-line parallelism mutant both fail the test.
The surviving `wg.Wait` mutant is recorded as Minor 1 (a test gap).

### Model scorecard

Not recorded here: the batch's later stage records the ledger row. The fields are: claude-opus-5-5 on
issue #143 (fix) → pass, 0/0/2, 1 model-attributed, DoD 11/11.

### Recommendation

Pass. Optional follow-up for `/fix`: make the regression test also assert that every instance reads Stopped
after Close, so that the `wg.Wait` mutant fails. The ctx-bounded `Runtime.Close` is a matter for an ADR if it is
wanted. The pre-existing TLS e2e race deserves its own issue.
