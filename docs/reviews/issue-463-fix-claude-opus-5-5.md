## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #463 fix, model: claude-opus-5-5)

Change: `fix/i463`, commit 71eee7d `fix(s3gateway): start the test gateway again when its reserved port is taken`.
Touched files: `internal/blob/s3gateway/harness_test.go`, `internal/blob/s3gateway/scenarios_test.go` (test code only).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — no test pins "only EADDRINUSE is retried"** · attribution: model.
  Mutant M3 replaced `if !errors.Is(err, syscall.EADDRINUSE) || i == attempts` with `if i == attempts`
  (retry on every Run error). The full package still passed (`ok internal/blob/s3gateway 0.733s`). The
  impact is small: a non-bind error still fails the test, only after up to 5 attempts instead of at once.
  A fix: a harness case whose Run fails with a non-bind error (for example an unparsable `Listen` set by an
  `opts` func) and that asserts a single attempt.

- **Minor 2 — the retry loop duplicates the #288 helper** · attribution: model.
  The new loop in `newGateway` (harness_test.go:117-150) has the same structure as `startS3Gateway` in
  `pkg/funcd/s3gateway_internal_test.go:70-104`: reserve an address, start Run in a goroutine with a
  buffered error channel, select on Ready / Run error / timeout, shut down, retry only on
  `syscall.EADDRINUSE`, up to 5 attempts. The commit message states this on purpose. The two copies live in
  different test packages and wrap different types (`*s3gateway.Server` vs `*Platform`), so no shared helper
  exists to call today. A small generic helper in `internal/testkit` (start func returning ready/run/stop)
  would remove both copies. Non-blocking; a follow-up is enough.

### Recorded, not scored

- **Pre-existing race under `-race`, tracked as #462.** The package run with `-race` fails intermittently
  with a data race inside fasthttp (`(*Server).ShutdownWithContext` writes a field that
  `(*RequestCtx).Done` reads), reported on `TestIssue30_MultipartTotalCappedAtUploadPart` or
  `TestScenarioOwnerWrites`. With the fix: 3 of 6 package runs failed. With the pre-fix harness
  (origin/main `harness_test.go`, `-skip TestIssue463_`): 2 of 8 runs failed with the same trace. It is not
  caused by this change; open issue #462 ("TestScenarioOwnerWrites fails under -race: fasthttp shutdown
  races a request ctx") covers it. The new cleanup waits for Run to return before the test ends
  (`cancel(); <-runErr; srv.Close()`), which may surface the race slightly more often inside a test, but the
  racing code is in the dependency and in `Server.Close`, not in the harness.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** With the origin/main `harness_test.go`
  and the new test kept:
  `--- FAIL: TestIssue463_GatewayStartsWhenItsReservedPortIsTaken (5.00s)` /
  `scenarios_test.go:568: gateway did not become ready` — exactly the issue's symptom (bind error dropped,
  5 s readiness timeout).
- **It passes with the fix under `-race`:** `go test -race -count=5 -run TestIssue463_` → `ok` (1.5 s).
  The test also performs a real GetObject through the restarted gateway, so it checks a working gateway,
  not only a different port.
- **Mutants on the key lines:**
  - M1 never retry (`if true || …`) → FAIL at once with the real bind error
    (`bind: address already in use`) — also shows the error now reaches the test unmasked.
  - M2 drop the `case err = <-runErr` arm → FAIL after 5 s with `gateway did not become ready`.
  - M3 retry on any error → survived (Minor 1).
- **Cause, not symptom.** The issue names two causes: the dropped Run error and the reserve-release-bind
  race. The fix keeps Run's error and selects on it, so a bind failure is seen immediately, and it retries
  only the bind collision on a fresh address. No timeout was raised and no error is swallowed: every other
  Run error and the 5th collision fail the test with the error itself.
- **Cleanup is correct.** The successful attempt registers `cancel(); <-runErr; srv.Close()`, so the Run
  goroutine has finished before the test returns. Failed attempts cancel and close their server before the
  next one.
- **Scope.** Two test files only; every hunk serves #463. No test was weakened or deleted; the existing
  scenarios still use `newGateway` unchanged in signature.
- **ADRs.** ADR-0085's `Ready()` contract is used as designed. No ADR file is touched.
- **Conventions.** Top-level imports (`syscall`, `net`), the `errors.Is` check matches the #288
  precedent, no comment bloat (one doc-comment sentence explains why the retry exists), no YAML.
- **Checks (touched package).** `go vet` → ok; `golangci-lint run ./internal/blob/s3gateway/` →
  `0 issues.`; `gofmt -l` → empty; package tests under `-race` pass apart from the pre-existing #462 race.
- **Shape.** Subject `fix(s3gateway): …`, body with Cause / Fix / Test, `Fixes #463`, attribution trailer,
  one issue in one commit.
- Worktree left at 71eee7d and clean after every revert and mutant.

### Definition of Done

10 / 11 items hold. Miss: item 10 (reuse — the retry loop copies the #288 helper; Minor, model).
Item 8 holds for the assigned scope (touched package: build, vet, lint, `-race` tests; the Linux lint,
repo-wide tests and e2e run in the group gate); the intermittent `-race` failure is the pre-existing #462.

### Model scorecard

Not recorded here (the batch records it): claude-opus-5-5 on issue #463 (fix) → pass, 0/0/2,
2 model-attributed, DoD 10/11.

### Recommendation

Pass. The fix removes the cause the issue names and the regression test proves it. Optional follow-ups:
a test that pins single-attempt failure on non-bind errors, and a shared `internal/testkit` helper for
the two EADDRINUSE retry loops. The `-race` flake in this package is #462, not this change.
