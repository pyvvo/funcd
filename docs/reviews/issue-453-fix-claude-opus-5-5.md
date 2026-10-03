## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #453 fix, model: claude-opus-5-5)

Change: `d6afdec fix(funcd): give shutdown's close phase its own bound so function logs drain`
(`pkg/funcd/funcd.go`, `pkg/funcd/logroutes_internal_test.go`).

The issue: `Run` used one 15 s `stopCtx` for the HTTP drain, `wg.Wait()` and `Platform.Shutdown`. A slow
in-flight request that used the whole bound left `Shutdown` an expired context, so `logRoutes.drain`
returned at once and the log tail and the telemetry flush were lost (ADR-0081, ADR-0028).

The fix: `Run` now calls `Shutdown` with a fresh `closeCtx` (`context.WithoutCancel(ctx)`, bounded by the
new `closeTimeout = 5 s`), created after the drain phase. The HTTP drain bound moves to a `Platform.drainTimeout`
field, set to `shutdownTimeout` in `New`, so the test can exhaust it.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The flaky `TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite` (`pkg/funcd/s3gateway_internal_test.go:205`)** · attribution: env ·
  It failed in 2 of 6 full `go test -race ./pkg/funcd/` runs on the fix branch, at 0.11 s, and passed in
  every isolated run (`-count=3`) on the fix and on an overlay of `origin/main`. The test binds the S3
  gateway on a `freeLoopbackAddr` port on a shared host. The diff touches only the shutdown path that runs
  after the context is cancelled, not the S3 gateway or its write cap. This is not a defect of this
  change. If it recurs at the group gate, file it as a flaky-test issue.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** A literal `git revert --no-commit d6afdec`
  with the new test kept fails to compile (`p.drainTimeout undefined`), because the test uses the seam the fix
  adds. A semantic overlay that keeps the seam and restores the pre-fix call (`return p.Shutdown(stopCtx)`)
  fails `TestIssue453_ShutdownDrainsLogsAfterHTTPDrainTimeout` 3 out of 3 times: "Shutdown runs on a live context of its own,
  not the one the HTTP drain used up". After `git reset --hard d6afdec`, both `TestIssue453_…` and
  `TestIssue33_…` pass under `-race`. The worktree is left clean at `d6afdec`.
- **Mutants (overlays, `-run 'TestIssue453_|TestIssue33_'`). Each mutant is killed:**
  - M0: `Shutdown(stopCtx)` instead of `closeCtx` fails `TestIssue453_…` (probe assertion).
  - M1: `closeTimeout = time.Nanosecond` fails both tests: "200 were Put after it" (the log tail is lost after
    `blob.Close`), which confirms that the log-count assertion detects an expired close context.
  - M2: `closeCtx` derived from `stopCtx` instead of `WithoutCancel(ctx)` fails `TestIssue453_…`.
- **Cause, not symptom.** The change separates the close-phase context from the drain context, which is the cause
  named in the issue (`funcd.go` single `stopCtx`). The drain bound is still 15 s. The change does not lengthen it
  and does not swallow an error. Both phases stay bounded: 15 s + 5 s fits `TimeoutStopSec=30` in
  `cmd/funcd/install.go:55`, as the constant's comment states. This matches ADR-0028's "bounded graceful
  shutdown" (no single-bound requirement; §Open questions keeps the value a fixed V1 bound) and
  ADR-0081's no-loss-on-teardown.
- **Scope.** Every hunk serves #453. The `TestIssue33_…` body is refactored into the
  `heldLogPlatform`/`runThenCancel` helpers. Its assertions are unchanged, so the test is not weakened, and it
  still passes.
- **Reuse.** The test reuses the existing `closeOrderBucket`, `captureRuntime` and held-channel harness from the
  #33 test rather than copying them. The `shutdownCtxProbe` is a three-line `network.Manager` stub that observes
  the context through the existing `WithEgressIsolation` option. No existing helper did this. `closeTimeout` sits
  beside `shutdownTimeout` in the same const block. No new dependency.
- **Conventions.** ctx-first and no `any`. The comments state the reason (an issue number and an ADR). Imports are
  at the top level. There is no YAML. The test uses `InMemory()`, the same as the #33 test it extends.
- **ADRs.** No ADR file was touched. No Accepted or Implemented Decision or Contract is contradicted.
- **Checks (touched package).** `go vet ./pkg/funcd/` passes. `golangci-lint run ./pkg/funcd/` reports "0 issues".
  `go test -race ./pkg/funcd/` is green, except for the env flake above.
- **Shape.** One commit, `fix(funcd):` subject, `Fixes #453`, attribution trailer. The body explains the cause and the bound.

Observation (not scored): `drainTimeout` is a production field that exists only so a test can shorten the
15 s drain. It is the lightest seam that avoids a 15 s test, and its comment says so.

### Definition of Done
11 / 11 items hold (host scope). Item 2 holds through the semantic overlay. The literal revert is a compile failure
because the test depends on the seam. Item 8 covers the touched package on the host only. Linux lint, e2e and
the lanes are left to the group gate, as the task scoped.

### Model scorecard
Ledger fields (not recorded by this run): issue 453, phase fix, model claude-opus-5-5 → pass, 0/0/1,
0 model-attributed, DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` Step 8. At the group gate, watch `TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite`
for the env flake.
