# Fix review — issue #337 (claude-opus-5-5)

**Issue**: A link timeout during the target's body drops the fn-to-fn caller's connection.
**Change**: branch `fix/i337`, commit `32052a6` — `fix(workernode): return 503 when the link timeout cuts an fn-to-fn response mid-body`.
**Files**: `internal/workernode/local/invoker.go`, `internal/workernode/local/local_test.go` (+54 / −2).
**Governing ADRs**: ADR-0064 (fn-to-fn: "bounded by the link timeout (→ 503 on deadline)"), ADR-0002 (conventions).

## Verdict: **pass**

Blockers 0 · Majors 0 · Minors 1 (model 1) · DoD 11 / 11.

## Verification run

| Check | Result |
|---|---|
| `git revert --no-commit 32052a6` with the new test kept, `go test -run TestIssue337` | **FAIL**: `Post ".../invoke/b": EOF`, which is the dropped connection the issue reports |
| `git reset --hard 32052a6`, `go test -race -run TestIssue337 -v` | **PASS** (0.10 s) |
| M1: `aborted = true` removed (abort recovered but not reported) | killed: expected 503, got 200 (a truncated body returned as success) |
| M2: always re-panic in `serve` | killed: `EOF` |
| M3: `fault.Unavailablef` → `fault.Internalf` | killed: expected 503, got 500 |
| `go build ./...` | ok |
| `go test -race ./internal/workernode/local/` | ok |
| `go vet` / `golangci-lint` on the package, `gofmt -l` | clean / 0 issues / clean |

The worktree is left at `32052a6` and clean.

## Blockers

None.

## Majors

None.

## Minors

1. **The regression test checks only the status code.** (attribution: `model`)
   `TestIssue337_LinkTimeoutMidBodyIs503` asserts `503`, but not that the reply is the `application/problem+json`
   body that the issue's expected behavior names. The error path also writes the `fn-to-fn invoke failed`
   log line, and the test does not check that either. Both hold by inspection: `local.go` sends every
   non-`UpstreamError` invoke error through `logger.Warn("fn-to-fn invoke failed", …)` and
   `fault.WriteProblem`. The test is still enough to catch a regression of the defect (M1–M3 are all
   killed). One `Content-Type` assertion would close the gap.

## ✅ Verified correct

- **Root cause, not symptom.** The issue names the cause: `httputil.ReverseProxy` panics with
  `http.ErrAbortHandler` mid-body, `gateway.Recover` re-panics it (the #91 contract), and `net/http` aborts
  the local API connection. The fix recovers that one sentinel at the only place where the abort is not a
  real server abort, the in-process invoke that records into `cappedRecorder`. Nothing has reached the
  caller there, so it turns the abort into `fault.Unavailable`, which becomes a 503. It does not lengthen a
  timeout, swallow a different error, or touch `gateway.Recover`.
- **Narrow recovery.** `serve` re-panics any value that is not an error wrapping `http.ErrAbortHandler`, so
  real handler panics still reach `gateway.Recover`'s 500 path. M2 confirms that the predicate matters.
- **Cause text.** When `cctx.Err()` is set, the deadline is reported. An upstream that fails mid-body with
  no deadline gets "the upstream failed", which is still a 503 and not a dropped connection. That fits
  ADR-0064's transport-failure → 503 mapping.
- **Reuse.** No helper exists that recovers `ErrAbortHandler` into a value. The only other site,
  `internal/gateway/middleware.go` `Recover`, has the opposite job: it must re-panic. The fix reuses
  `fault.Unavailablef`, the existing `fakeResolver` and `local.NewHandler`, and stdlib `httptest`/`httputil`.
  It adds no dependency. The `const op` consolidates the existing op string literal.
- **Scope.** Every hunk serves the issue. No test is weakened or deleted.
- **Conventions.** `api/fault` errors, ctx-first, imports at the top level, and one short doc comment on
  `serve` that gives the *why* and the issue number. This matches the surrounding package's idiom.
- **ADRs.** The fix conforms to ADR-0064 (503 on the link deadline) and edits no ADR file.
- **Commit shape.** `fix(workernode):` subject, `Fixes #337`, attribution trailer, one issue per commit.

## Recommendation

Pass. Optionally assert `Content-Type: application/problem+json` in the regression test. The group gate
(repo-wide tests, Linux lint, e2e) still runs once for the PR.
