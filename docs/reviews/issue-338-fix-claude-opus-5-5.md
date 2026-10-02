## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #338 fix, model: claude-opus-5-5)

Change: branch `fix/i338`, commit 49db472 `fix(gateway): abort a committed response on a handler panic instead of appending a problem`.
Touched: `internal/gateway/middleware.go`, `internal/gateway/middleware_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Single-setter mutants survive · attribution: model.** The regression test both writes and flushes before
  panicking, so each commit setter is covered only by the other. Mutants run with
  `go test -run 'Recover|Issue91|Issue338' ./internal/gateway/`:
  - M2 `Write` no longer sets `committed` → `ok` (survives)
  - M3 `Flush` no longer sets `committed` → `ok` (survives)
  - M4 `WriteHeader(>=200)` no longer sets `committed` → `ok` (survives)

  A table case per commit trigger (write only, explicit `WriteHeader(200)` only, flush only) would kill all
  three. Non-blocking: the key line (the `cw.committed` check) is killed (M1, below).

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 49db472` with the new test kept:
  `TestIssue338_RecoverAbortsCommittedResponseOnPanic` FAILs with
  `Expected error with "unexpected EOF" in chain but got nil`, which is the clean 200 with the appended
  problem that the issue reports. The worktree was then reset to 49db472 and left clean.
- **Passes with the fix**: `go test -race -count=1 ./internal/gateway/` → `ok`. The test is not skipped.
- **The user-visible behavior is fixed.** The test is the issue's own reproduction on a real
  `httptest` server: write `partial-`, flush, then a plain panic. It asserts a truncated body
  (`io.ErrUnexpectedEOF`), a body of exactly `partial-` with no problem appended, and that net/http still
  logs the panic through `ErrorLog`.
- **Cause, not symptom.** The cause named in the issue (`middleware.go`: Recover wrote a problem without
  knowing whether the response had committed) is removed. Recover now tracks the commit and re-panics once it
  happens, which is the same path the #91 `ErrAbortHandler` fix uses. Nothing is swallowed, retried or skipped.
- **Mutants**: M1 (drop `cw.committed ||`) → `TestIssue338` FAILs. M5 (`committed` always true) →
  `TestRecoverMiddleware` FAILs, so a panic before the commit still returns a problem+json 500.
- **Commit semantics are right.** 1xx informational statuses do not commit, while 101 and every final status
  do. A hijack commits only when it succeeds. `Flusher` and `Hijacker` are implemented directly, because
  inner wrappers (`edge/observ.recorder`, `edge/shape`) type-assert them, and `Unwrap` keeps
  `http.ResponseController` working (read and write deadlines, full duplex).
- **Scope**: both hunks serve #338. No test was weakened or deleted, and #91's test still passes.
- **Reuse.** The only similar wrapper is `internal/edge/observ.recorder`. It is unexported, it sits in a
  package that wraps the gateway (importing it would invert the import graph), and it counts 1xx as written.
  A local wrapper is therefore justified. `syncBuffer` is a test-only helper; no equivalent exists in
  `internal/testkit`. The fix uses the standard `http.NewResponseController` instead of hand-written type
  assertions.
- **Conventions**: imports are at the top level, the doc comments state the why and cite #338, and there is
  no `any` in signatures. The panic is reported by net/http, as for #91, which is consistent with
  ADR-0013's Recover and its first-class streaming. No ADR was edited and none is contradicted (ADR-0002
  problem+json still applies to every panic before the commit).
- **Checks on the touched package**: `go vet ./internal/gateway/` is clean, and
  `golangci-lint run ./internal/gateway/...` reports `0 issues`. The repo-wide checks, the Linux lint and the
  e2e suite are left to the group gate.
- **Shape**: the subject is `fix(gateway): …`, the body has `Fixes #338` and the attribution trailer, and the
  commit covers one issue.

### Recommendation
Pass. Optionally add table cases for each commit trigger (Minor). Hand back to `/fix` Step 8.
