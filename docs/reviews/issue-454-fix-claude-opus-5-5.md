## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #454 fix, model: claude-opus-5-5)

Change: branch `fix/i454`, one commit `c2e6e99` — `fix(server): route net/http server errors through the configured logger`.
Touched: `pkg/funcd/funcd.go`, `internal/workernode/local/{local,manager}.go`, `internal/catalog/gateway/manager.go`, plus three
`TestIssue454_…` tests.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The new `lockedWriter` test type duplicates `syncBuffer`** · attribution: `model` ·
  `pkg/funcd/errorlog_internal_test.go:15-31` adds a mutex-guarded `bytes.Buffer` with `Write` and `String`. The same
  type, line for line, already exists as `syncBuffer` in `internal/gateway/middleware_test.go:117-132`. A test type cannot
  be imported from another package's test file, so reuse means lifting it into `internal/testkit` and using it from both
  places. The duplication is small and confined to tests, so it is a Minor. A follow-up can do the lift.

### Out of scope (recorded, not counted)

- **A data race already present in the S3 gateway shutdown** · attribution: `env` (code this change does not touch) · One of
  eight `go test -race ./pkg/funcd/` runs failed in `TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite`. The race is
  between `fasthttp.(*Server).ShutdownWithContext` (through versitygw `S3ApiServer.ShutDown` ←
  `internal/blob/s3gateway.(*Server).Close`) and `fasthttp.(*RequestCtx).Done` read from a gocloud `NewWriter` context.
  This change does not touch the S3 gateway path. The race should be filed as its own issue if none exists yet.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** I applied `git revert --no-commit c2e6e99` and
  restored the three test files from HEAD, so only the fix code was reverted. All three tests then fail with
  `Expected value not to be nil` on `srv.ErrorLog`:
  - `TestIssue454_ServerErrorsUseTheConfiguredLogger` fails with "the data-plane server's net/http errors must go through the
    configured logger".
  - `TestIssue454_LocalAPIServerErrorsUseTheManagerLogger` fails with "the local API's net/http errors must go through the
    Manager's logger".
  - `TestIssue454_ProxyServerErrorsUseTheManagerLogger` fails with "the catalog proxy's net/http errors must go through the
    Manager's logger".

  After that run, `git reset --hard c2e6e99` left the worktree clean at that commit.
- **They pass with the fix under `-race`.** `go test -race -count=1 -run TestIssue454 -v` reports `--- PASS` for all three,
  in `pkg/funcd`, `internal/workernode/local` and `internal/catalog/gateway`.
- **Mutants** (applied with `-overlay`; the worktree was never edited). Each one fails a test:
  1. Drop `ErrorLog` from the data-plane server only (`funcd.go`). Result: FAIL, "the data-plane server's net/http errors
     must go through the configured logger". The test checks both servers, so one missing server is caught.
  2. Change the local API's `ErrorLog` level from `LevelWarn` to `LevelDebug` (`local.go`). Result: FAIL, because the
     expected `"level":"WARN"` record is missing (the default JSON handler drops Debug records).
  3. Build the catalog proxy's `ErrorLog` from `slog.Default().Handler()` instead of the Manager's logger (`manager.go`).
     Result: FAIL, because the expected record with `"component":"catalog.gateway"` is missing.
- **The root cause is fixed, not masked.** The issue says the four `http.Server` values are built without `ErrorLog`. All four
  now set `ErrorLog: slog.NewLogLogger(<component logger>.Handler(), slog.LevelWarn)`. This is the same construction as the
  existing `ReverseProxy` at `internal/catalog/gateway/proxy.go:61`, which the issue names as the expected behavior. The local
  API threads `m.logger` through `listen`, so `Serve` and `SocketFor` still share one binding path (the #357 invariant). The
  control and data planes share one `httpErrorLog` built from `p.logger`.
- **Completeness.** A search for every `http.Server{` literal outside tests finds only these four. The only other
  `ListenAndServe` is `internal/network/egress/forwarder.go`, which runs a `dns.Server`, not a `net/http` server. There is no
  `http.Serve(`, `new(http.Server)` or bare `ListenAndServe` elsewhere.
- **`local.Serve` uses `slog.Default()`.** Its only callers are in `local_test.go`, and its doc comment says that it logs
  through `slog.Default()`. The behavior is accurate and no worse than before.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted, and no ADR file was touched.
- **Conventions.**
  - The change uses only `log/slog`. `*log.Logger` comes from `slog.NewLogLogger`, so the `log` package is not imported
    (ADR-0002 §6).
  - Imports are at the top level.
  - Each comment is one short sentence that cites #454.
  - The `pkg/funcd` test uses `InMemory()`, so it has no on-disk data dir. The local API test uses `os.MkdirTemp("", "i454")`
    to stay under the socket-path limit.
- **Reuse.** The fix reuses the stdlib bridge `slog.NewLogLogger` and the existing proxy pattern, and adds no new helper.
  Repeating this one-line stdlib call in four places does not need a wrapper.
- **Checks on the touched packages.**
  - `go test -race -count=1` passes for `internal/workernode/local` and `internal/catalog/gateway`.
  - `pkg/funcd` passed 7 of 8 runs. The one failure is the unrelated S3 gateway race above.
  - `go vet` reports no issues on all three packages.
  - `golangci-lint run` on all three packages reports `0 issues.`
- **Commit shape.** The subject is `fix(server): …`, the body has `Fixes #454`, the commit carries the `Co-Authored-By`
  trailer, and it is one issue in one commit.
- **Not rerun: the issue's own TLS-handshake step.** It needs a TLS-enabled daemon. The tests assert the wiring, which is the
  cause the issue names, and `net/http`'s use of `Server.ErrorLog` is stdlib behavior.

### Definition of Done

10 of 11 items hold. The miss is item 10 (reuse), because of the duplicated `lockedWriter` (`model`, Minor). Item 8 is green
for the host scope this review ran: the touched packages' tests with `-race`, vet and lint. The Linux lint, the e2e suite and
the lanes are left to the group gate.

### Model scorecard

The ledger fields are returned to the caller; `docs/reviews/` was not edited. claude-opus-5-5 on issue #454 (fix) → pass,
0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation

The fix can ship as is. A follow-up can move the mutex-guarded log buffer into `internal/testkit` and use it from both
`pkg/funcd` and `internal/gateway`. The S3 gateway shutdown race should be filed as its own issue.
