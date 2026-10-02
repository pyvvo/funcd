## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #287 fix, model: claude-opus-5-5)

Commit `bffba5c` `fix(function): give the readiness probe a transport of its own` on `fix/287-probe-transport`,
one commit on `origin/main`. It touches `internal/function/function.go` (the probe's `http.Client` in
`NewReconciler`) and `internal/function/supervision_internal_test.go` (the regression test, and the removal of
the test-side transport in `TestIssue236_…`).

### Minor
- **The new test copies the counting-server setup of `TestIssue236_…`** · attribution: `model` ·
  `TestIssue287_ProbeSurvivesDefaultTransportCloseIdle` repeats the block that the test above it already has:
  `httptest.NewUnstartedServer`, a `ConnState` counter on `http.StateNew`, `Start`, `t.Cleanup(srv.Close)` and
  the `*net.TCPAddr` assertion. Only the handler differs (an empty 200 against a JSON body). A small helper in
  the test file that takes the handler and returns the address and the counter would serve both tests. This is
  trivial test-fixture duplication and does not block sign-off.

### ✅ Verified correct (keep it)
- **It fails without the fix, for the issue's reason.** With the `origin/main` version of `function.go`
  overlaid (`go test -overlay`) and the HEAD tests, `TestIssue287_…` fails with `expected: int(1)` /
  `actual: int32(2)` and the message "closing the default transport's idle connections must not touch the
  probe's". The failure is deterministic: 20 of 20 under `-count=20`. On the same pre-fix code
  `TestIssue236_…` also fails (`expected: int(1)` / `actual: int32(3)`), because the `httptest.Server.Close`
  calls of parallel tests drop the probe's connections during its 50-probe loop. That is the issue's
  mechanism, and the removed test-side transport was the workaround that hid it.
- **It passes with the fix** under `-race`: `go test -race -count=1 ./internal/function/` gives `ok`, and
  `-race -count=50` of `TestIssue287_…`, `TestIssue236_…` and `TestSwitchWaitsForEveryReplica` (the flaky
  test that the issue reports) gives `ok`. Neither new nor changed test is skipped.
- **The user-visible failure is fixed.** The real failure is a narrow race, so a scratch overlay test (not
  committed) sent requests through the reconciler's own `httpClient` while four goroutines called
  `http.DefaultTransport.CloseIdleConnections()` in a loop. On the pre-fix code, one run of 8 × 1,000
  requests returned the issue's exact error, `http: CloseIdleConnections called`, once; three later runs did
  not hit the window, which matches the "rare" frequency in the issue. With the fix, no request failed in one
  run of 8 × 1,000 and three runs of 2 × 4,000. Two further fix-side runs with eight concurrent requesters
  failed with `can't assign requested address`: eight requesters against a pool that keeps two idle
  connections per host, on top of the TIME_WAIT backlog of the pre-fix runs, exhausted the ephemeral ports.
  That is an artifact of the scratch harness, not the probe's shape (one probe at a time per replica
  address). One package run directly after them failed for the same reason and passed when rerun.
- **The cause is fixed, not masked.** The claimed cause holds in the pinned Go 1.26.4 `net/http`: for a
  response without a body, `readLoop` calls `tryPutIdleConn` before it sends the response on the unbuffered
  channel, `CloseIdleConnections` closes the parked connection with `errCloseIdleConns`, and `roundTrip`'s
  `pcClosed` branch returns that error when the response is not yet on the channel. The fix takes the probe
  out of the process-wide pool, which removes that cause; it adds no retry, timeout or swallowed error. The
  clone keeps the default pool settings, so the keep-alive reuse of #236 and ADR-0041 still holds:
  `TestIssue236_…` now sees one connection over 50 probes through the production transport, not a
  test-injected one.
- **Mutants are killed.** m1 `Transport: http.DefaultTransport` (shared, not cloned) fails both tests (2 and
  3 connections). m2 `Transport: &http.Transport{DisableKeepAlives: true}` fails both (2 and 50 connections).
  The revert above is m0. A fresh `&http.Transport{}` would be an equivalent fix, not a survivor.
- **Scope**: two files, and every hunk serves #287. The `TestIssue236_…` change removes only its transport
  override and its comment, which makes that test check the production client. No test was weakened or
  deleted.
- **Reuse**: the fix uses the standard library's `(*http.Transport).Clone()`, as `newPooledTransport` in
  `internal/activator` and `New` in `internal/gateway/embedded` do (ADR-0041). Those builders are unexported in
  other packages and tune the pool for data-plane concurrency, which the probe does not need, so not reusing
  them is correct. The test reuses `newShimReconciler`, `fakeResolver` and `readinessPath`.
- **Conventions**: no new import, no `any`, ctx-first is unchanged, and the two-line field comment and the test
  doc comment explain why and cite the issue. The type assertion without comma-ok panics in the same case as
  the precedents' `t, _ := …` followed by `t.Clone()` (a nil receiver), so the two forms are equivalent.
- **ADRs**: ADR-0041 governs the data-plane proxies' transports, and the fix follows its clone rule.
  ADR-0143's `CallTracker` wraps the activator proxy and the `sensor.HTTPInvoker` and `workflow.DispatchDeps`
  transports, not the readiness probe, so the probe's own transport does not contradict it. No ADR file was
  edited, and no living doc names the probe's transport.
- **Checks**: `gofmt -l internal/function` is clean; `go build ./...` passes; `go vet ./internal/function/`
  passes on the host and for `GOOS=linux`; `golangci-lint run ./internal/function/...` reports `0 issues.` on
  the host and for `GOOS=linux`. No e2e suite or Lima lane was run, by review scope; the repo-wide gate runs
  once per PR.
- **Shape**: the subject is `fix(function): …`, the body names the regression test and carries `Fixes #287`
  and the attribution trailer, and the commit covers one issue.

Observation, not a finding: the issue notes that the catalog proxy (`internal/catalog/gateway/proxy.go`)
also leaves its transport nil, so it uses `http.DefaultTransport`, and that the same error string appeared
from it locally. The fix rightly leaves it alone, because #287 is the probe flake; a separate issue can cover
the proxy if it flakes.

### Definition of Done
11 / 11 items hold (the fix checklist): the regression test reproduces the issue, fails on the pre-fix code
for the reported reason, and passes under `-race`; the revert and both mutants fail a test; the cause is
removed; the scope is clean; the ADRs hold; build, vet, lint (host and Linux) and the package tests are
green; the conventions hold; the production change reuses the standard library and the ADR-0041 precedent;
and the commit shape is correct. The duplicated test setup is a Minor and does not negate item 10, because it
is trivial fixture code and the change adds no helper, type, harness or dependency.

### Model scorecard
Not recorded by this stage: claude-opus-5-5 on issue #287 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 11/11. A later stage records the ledger row.

### Recommendation
Sign off. Optionally, move the counting httptest server into a helper that both probe tests share.
