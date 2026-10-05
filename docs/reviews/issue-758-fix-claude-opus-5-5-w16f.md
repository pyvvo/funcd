# Fix review — issue #758 (catalog proxy port tests lose the freed port to another test process)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #758 fix, model: claude-opus-5-5)

Change: branch `fix/w16f-i758`, one commit `8c9f1ee6 fix(test): keep the ports restart tests rebind out of the
ephemeral range`. Test-only: a new `internal/testkit/freeport` helper hands out a free 127.0.0.1 port in
[20000, 30000), below the :0 range of Linux (32768-60999) and macOS (49152-65535); every test that frees a port and
binds or dials it again takes it from there. Judged against the person's decision (prove first, then choose each
recorded port outside the ephemeral range through a shared helper; test-only).

### 🟡 Major

None.

### Minor 1 — a related freed-port dial is left on an ephemeral port  ·  attribution: model

`internal/activator/calltracker_test.go` `TestCallTrackerFailedRoundTripEndsTheCall` closes a :0 listener and dials
its port, expecting the round trip to fail. Another test process's :0 bind could take that port in between, and the
GET would connect. This is the same mechanism as the dial in `TestScenarioLastBinderClosesListener`, which the fix
covers, but the window is microseconds (close, then dial at once) and nothing is recorded or rebound, so it is outside
the decision's "frees and rebinds a recorded port" scope. The commit neither fixes nor mentions it; a one-line
`freeport.Port` would close it. Other free-then-bind sites (`internal/function/shim_test.go` runShim, the s3gateway
`freeAddr` helpers) already retry on EADDRINUSE or are covered by `TestIssue463_…`.

### Minor 2 — the proof of the window is not recorded as its own test  ·  attribution: model

The decision asked to show the window first, for example with a helper test that frees the recorded port and lets a
second listener take it before the rebind. The branch adds no such test. The proof is present in parts:
`TestIssue758_PortIsNeverEphemeral` asserts that a :0 bind draws from the OS ephemeral range (read from sysctl or
/proc) and that every `freeport.Port` lies below it, and the taken-then-moved consequence is already shown
deterministically by `TestScenarioTakenPortMovesConsumers` and `TestManagerListen…` with a holder
(`internal/catalog/gateway/manager_test.go`, the `holder` case). Cosmetic, because together these prove the cause.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `freeport.go` is new, so the revert-equivalent
  overlay replaces it with the pre-fix port source (a :0 bind, then close): `TestIssue758_PortIsNeverEphemeral`
  fails with `"50781" is not less than "49152"` / "another test process's :0 bind can be given this port".
- **It passes with the fix** under `-race` (`ok internal/testkit/freeport`).
- **Mutants, each killed:**
  1. `first = 50000` (helper range inside the ephemeral range) → fails, `"58673" is not less than "49152"`.
  2. `cursor.Add(1)` → `cursor.Load()` (no advance) → fails, "one process never gets a port twice".
  3. Drop the run-0 `mgr.Listen(…, port)` in `TestReconcileRecordsProxyPort` → fails, "the pass records the
     listener's port; a new run binds it again" (the strengthened `require.Equal(port, …)` catches it).
- **Cause, not symptom.** No retry, timeout or skip: the recorded port is simply never one a :0 bind can be given,
  so no other process's :0 bind can take it between the free and the rebind. No production code changed, as the
  decision expected (`reconcile.go` records whatever `Listen` bound; `Manager.Listen` falls back to :0 only when the
  recorded port is taken).
- **Every sibling the decision names is covered.** `readyPair` binds lake's listener on a freeport port before the
  catalog pass, which covers `TestScenarioRestartKeepsCatalogPort`, `TestScenarioTakenPortMovesConsumers` and
  `TestScenarioLastBinderClosesListener` (and the two other `readyPair` users); `TestManagerListenRebindsRecordedPort`
  and `TestReconcileRecordsProxyPort` take the port from the helper. The search found two more restart rebinds, and
  the fix covers them: the crash-recovery daemon restart (`cmd/funcd` `freeAddr`) and `TestIssue94_…`'s control
  address in `pkg/funcd`.
- **No test weakened.** `TestReconcileRecordsProxyPort` went from `NotZero` plus a run-1 equality to an equality on
  both runs. The reconcile path that records `bound` is the same line either way, and the :0 path of `Manager.Listen`
  stays covered in `manager_test.go`.
- **Stress evidence.** `-race -count=20` (the host cap) on the five sibling tests plus the regression test across
  `internal/function`, `internal/catalog/gateway`, `internal/services/catalog`, `internal/testkit/freeport`, run
  concurrently with `internal/blob/s3gateway` and `internal/activator` (`-count=3`, :0-heavy): all `ok`, no "port is
  taken" log.
- **Checks on the touched packages.** `go test -race` on the four internal packages: ok. The changed tests in
  `cmd/funcd` (`TestScenarioCrashRestartLeavesDesiredWorkers`) and `pkg/funcd` (`TestIssue94_…`) run and pass
  (not skipped). `go vet` clean; `golangci-lint` 0 issues on all six packages.
- **Reuse.** No shared free-port helper existed (`internal/testkit` had bench, langmod, loadgen, realshim; the repo
  had per-package copies of the :0-and-close idiom). The new package is the shared helper the decision asked for;
  `devengine.freePort` is production code with a different job. Standard library only.
- **Conventions.** Top-level imports; the one `//nolint:gochecknoglobals` carries its reason; comments state the why
  (#758 window) without narration; the helper takes `testing.TB`, like the other testkit packages. Linux and macOS
  ephemeral lookup in the test skips on other OSes rather than guessing.
- **ADRs.** No ADR file touched; ADR-0162's restart-keeps-port contract is unchanged (test-only change).
- **Shape.** One commit, `fix(test):` subject, `Fixes #758`, attribution trailer; the body names the regression test.

### Definition of Done

12 of 12 items apply; 12 hold. Item 8 counts the host checks run here; the Linux lint, e2e and the repo-wide run
belong to the group gate. Item 12 holds for the decision's scope (frees and rebinds a recorded port); the related
dial-only window is Minor 1.

### Model scorecard

claude-opus-5-5 — pass, 0 blockers, 0 majors, 2 minors (both model), DoD 12/12.

### Recommendation

Merge with the group. Optionally fold Minor 1 into the same PR (`freeport.Port` in
`TestCallTrackerFailedRoundTripEndsTheCall`).
