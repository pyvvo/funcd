## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #348 fix, model: claude-opus-5-5)

Change: `e0b51bd fix(sensor): reuse keep-alive connections across function deliveries` on `fix/i348`
(`internal/sensor/invoker.go` +9/-1, `internal/sensor/invoker_test.go` +26).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `git revert --no-commit e0b51bd` and the test file
  kept, `go test -race -run TestIssue348 ./internal/sensor/` fails: `expected: int(1)`, `actual: int32(10)`,
  so 10 deliveries open 10 connections. These are the issue's exact steps and numbers.
- **Passes with the fix.** After `git reset --hard e0b51bd`, `go test -race -count=1 ./internal/sensor/`
  reports `ok` (4.1 s). The test is not skipped. The worktree was left clean at `e0b51bd`.
- **Cause, not symptom.** The issue names the cause at `internal/sensor/invoker.go:76`: a 2xx body was
  closed without being read. The deferred close now drains up to `maxDrainBody` (4 KiB) before it closes
  the body, on every path. On the error path, the drain runs after the bounded `maxErrorBody` read, so the
  error message is unchanged. The fix adds no retry, timeout or error swallowing.
- **Production path benefits.** `pkg/funcd/funcd.go:706` builds the invoker with
  `calls.Wrap(nil)`, which wraps `http.DefaultTransport` (`internal/activator/calltracker.go:43`). That
  transport is pooled, so the drain lets real deliveries reuse their connections.
- **Mutants (overlay, `-run TestIssue348`).** Both mutants fail the test with `actual: int32(10)`:
  - M1, `maxDrainBody = 0`: killed.
  - M2, the drain line removed: killed.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The change uses `io.Copy(io.Discard, io.LimitReader(...))` from the standard library. It
  copies the idiom of the readiness probe's drain (`internal/function/function.go:1417–1437`,
  `probeBodyMax = 4 << 10`, from #236), and the commit message names that precedent. Those are two
  unexported constants in different packages, so no shared helper is needed and this is not duplication.
  No new dependency, type or test harness was added. The test reuses the package's existing
  `readyEndpoints` fake.
- **Conventions.** The imports are at the top level (`net`, `sync/atomic` in the test). The one comment
  on the constant explains why, and cites ADR-0041. The naming matches the neighbouring `maxErrorBody`.
  There is no `any`, `panic` or logging change, and the `api/fault` errors are untouched.
- **ADRs.** The fix is consistent with ADR-0041 (upstream connection pooling) and ADR-0108 (the Sensor
  function action). No ADR file was touched.
- **Checks (touched package).** `go test -race` passes, `go vet ./internal/sensor/` passes, and
  `golangci-lint run ./internal/sensor/` reports `0 issues`.
- **Shape.** The commit subject is `fix(sensor):`. The body gives the cause, the fix and the test, and
  includes `Fixes #348` and the attribution trailer. The commit fixes one issue.

### Definition of Done
11 / 11 items hold. Item 8 was verified for the touched package on the host (build, vet, lint and
`-race` tests). Linux lint, the repo-wide tests and e2e are left to the group gate, as the task instructs.

### Model scorecard
Ledger fields (not recorded here; the batch records them): claude-opus-5-5 on issue #348 (fix) → pass,
0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. Hand back to `/fix` Step 8 for the group PR.
