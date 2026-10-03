## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #583 fix, model: claude-opus-5-5)

Change: branch `fix/w7-i583`, commit dfc65d7 `fix(s3gateway): return from Run when Close lands before Serve starts`
(`internal/blob/s3gateway/s3gateway.go`, new `internal/blob/s3gateway/shutdown_unit_test.go`).

### Root cause (confirmed in the module cache)

- versitygw v1.6.0 `s3api/server.go` `ServeMultiPort` binds the listener, registers the `OnListen` hook, then calls
  fiber's `app.Listener`; fiber v3 `listen.go` runs the OnListen hooks (line 299) before `app.server.Serve(ln)` (line 316).
- fasthttp v1.71.0 `server.go` `Serve` appends `ln` to `s.ln` only on entry (line 1923); `ShutdownWithContext`
  returns `nil` at once when `s.ln == nil` (line 2018).
- So a `Close` that lands between the bind and `Serve`'s registration (or before the bind) shuts nothing down;
  `Serve` then serves and the old `Run` waited on `<-s.serveCh` forever. This matches the issue's stack (Run blocked
  after Close, ShutDown returned, a fasthttp worker-pool goroutine alive). The library exposes no "Serve registered
  its listener" signal (versitygw builds the listener and offers no BeforeServe hook), so repeating `ShutDown` until
  `ServeMultiPort` returns is the remaining way to reach the late listener. It is not a bounded wait masking the
  hang: each retry is a real ShutDown, and the first one after registration closes the listener, which makes `Serve`
  return. This matches the decision recorded for this issue.

### Minor 1 — a mutant that returns from awaitServe without waiting for Serve survives  ·  attribution: model

Evidence: mutant m2 (add `return` after the `ShutDown` in the ticker case, so `awaitServe` returns after the first
retry whether or not `ServeMultiPort` has returned): `go test -race -run TestIssue583 ./internal/blob/s3gateway/`
→ `ok`. The regression test asserts that `Run` returns `context.Canceled`, but not that the gateway stopped
serving. Fix (builder, optional): after `Run` returns, assert that the gateway no longer serves, for example by
dialing the bound address and expecting a refused connection, or by checking that a second `ShutDown` finds no
listener. Not a blocker: the issue's reported defect (the hang) is pinned, and the shipped code waits on `serveCh`.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason**: overlay of `origin/main` `s3gateway.go` →
  `--- FAIL: TestIssue583_RunReturnsWhenCloseLandsBeforeServe (5.01s)` with
  "Run did not return after cancel: Close shut the gateway down before Serve registered its listener".
- **Passes with the fix**: `-race -count=20 -run TestIssue583` → 20/20 PASS; whole package `-race` → `ok`.
- **Deterministic reproduction**: the test closes the server inside the `OnListen` hook, which runs on the serving
  goroutine just before `Serve`, so the miss happens every run (no loop, no sleep). The 5 s timeout branch shuts the
  server down before failing, so a failing run does not leak the goroutine.
- **Mutants**: m1 (ticker case without the `ShutDown` call) → FAIL; m3 (`shutdownRetry = time.Hour`) → FAIL; m2
  survives (Minor 1).
- **Issue's own path**: `-race -count=3 -run TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken ./pkg/funcd/`
  → `ok`.
- **No cost on the normal path**: the ticker fires only after 10 ms, so a `ServeMultiPort` that returns after the first
  `ShutDown` returns through `serveCh` with no retry. Repeated `ShutDown` calls are safe: fiber's
  `ShutdownWithContext` and fasthttp's `ShutdownWithContext` both take their mutex; the `-race` runs are clean.
- **Scope**: two files, every hunk serves #583; no test weakened or deleted.
- **Reuse**: no existing internal-package PDP stub or server builder exists (`harness_test.go` is in the external
  `s3gateway_test` package and cannot reach `srv.api`); `denyAll` is a three-line local stub. The fix uses
  versitygw's own `WithOnListen` option and `ShutDown`, and the standard library ticker.
- **Conventions**: top-level imports, the comments state the why with the issue number, naming follows the file
  (`shutdownRetry` next to the other constants), no `any` in signatures, no new dependency.
- **ADRs**: ADR-0085 (Run blocks, graceful ShutDown on cancel, `Ready` via the OnListen hook) still holds; no ADR file
  edited.
- **Checks (touched package)**: `go vet ./internal/blob/s3gateway/` clean; `golangci-lint run
  ./internal/blob/s3gateway/...` → `0 issues.`
- **Shape**: `fix(s3gateway):` subject, cause/fix/test body, `Fixes #583`, attribution trailer, one issue in one
  commit. Worktree left clean.

### Recommendation

Pass. Merge with the group; Minor 1 can be closed in the same branch by asserting that the gateway stopped serving.
Repo-wide checks (Linux lint, e2e) are left to the group gate.
