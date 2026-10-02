## Verdict: pass — 0 blockers, 0 majors  (issue #236 fix, model: claude-opus-5-5)

Change: branch `fix/i236`, commit 0c85911 `fix(function): reuse the keep-alive connection across readiness probes`.
Files: `internal/function/function.go` (+9/-1), `internal/function/supervision_internal_test.go` (+33).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the fix reverted in `function.go` and the new test kept,
  `go test -race -run TestIssue236 ./internal/function/` fails:
  `expected: int(1) actual: int32(50)` — "50 probes must share one keep-alive connection". This matches the
  issue's reproduction exactly (50 probes, 50 connections).
- **Passes with the fix under `-race`**: `--- PASS: TestIssue236_ReadinessProbeReusesConnection`, and the whole
  `./internal/function/...` package is `ok` with `-race -count=1`.
- **Root cause, not symptom.** `probeReady` (`internal/function/function.go`) now drains the body through
  `io.LimitReader(resp.Body, probeBodyMax)` before `Close`, which is the documented net/http condition for
  returning a connection to the keep-alive pool. No timeout, retry or error swallowing was added. The bound
  (4 KiB) keeps a misbehaving shim from making the probe read an unbounded body; the readiness body is a few
  bytes, so the bound never prevents reuse in practice.
- **Mutants (3/3 killed)**, each run as a `go test -overlay` on `function.go`:
  1. drain line removed → FAIL (50 connections);
  2. `probeBodyMax = 0` → FAIL (50 connections);
  3. `probeBodyMax = 8` (below the 18-byte body, so EOF is never reached) → FAIL (50 connections).
- **Test quality.** The test counts `http.StateNew` through the server's `ConnState` hook (the issue's own
  method), uses `newShimReconciler` (the existing package harness) rather than a hand-built Reconciler, and
  gives the client a private `http.Transport` so a parallel test's `httptest.Server.Close` cannot drop the
  shared idle pool and flake the count. The one test comment states why, not what.
- **Scope.** Two hunks, both serving the issue; no test weakened or deleted; no ADR or living doc touched.
- **Reuse, no duplication.** The drain uses the standard library (`io.Copy(io.Discard, io.LimitReader(...))`);
  there is no shared drain helper in the repo to reuse (the only other drains are in `internal/testkit/bench`
  and `internal/testkit/loadgen`, test-only and unbounded). The test reuses `newShimReconciler`.
- **Conventions.** Top-level imports only; ctx-first signature unchanged; the constant follows the package's
  naming idiom and its doc comment cites ADR-0041; no comment bloat; no YAML touched.
- **ADRs.** Consistent with ADR-0030 §4b (the probe still reports a 200 as ready) and ADR-0041 (pooled
  connections so ephemeral ports do not churn). No Accepted/Implemented ADR file edited.
- **Checks (touched package).** `gofmt -l internal/function` empty; `go build ./...` ok; `go vet ./internal/function/` ok;
  `golangci-lint run ./internal/function/...` → `0 issues`; `go test -race -count=1 ./internal/function/...` → ok.
  Linux lint, e2e and lanes are left to the group gate.
- **Shape.** Subject `fix(function): …`, body names the regression test, `Fixes #236`, attribution trailer, one
  issue in one commit.
- The worktree was left at 0c85911, clean.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 is verified for the touched package on the host; Linux lint, e2e and
the lane are delegated to the group gate.

### Model scorecard
Ledger fields (not recorded here): claude-opus-5-5 on issue #236 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. Hand back to `/fix` Step 8 (open the PR that closes #236) once the group gate is green.
