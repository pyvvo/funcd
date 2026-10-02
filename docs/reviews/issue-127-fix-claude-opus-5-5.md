## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #127 fix, model: claude-opus-5-5)

Commit under review: `474064f` `fix(workernode): cap the fn-to-fn response the daemon buffers`, which touches
`internal/workernode/local/invoker.go`, `internal/workernode/local/local.go` (one comment) and a new
`internal/workernode/local/invoker_internal_test.go`.

The issue: the fn-to-fn `Invoker` captured the target's response in an unbounded `httptest.ResponseRecorder`
and copied it again with `io.ReadAll`. Only the input was capped at `maxInvokeBytes` (1 MiB). The fix wraps the
recorder in a `cappedRecorder`. The wrapper keeps at most `maxInvokeBytes` and drops the rest without failing the
write. `Invoke` then returns `fault.PayloadTooLarge` (413) for an over-cap response, and it returns the buffer
without a second copy.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The drop-instead-of-fail branch is not covered by any committed test** · attribution: `model`.
  The `cappedRecorder` comment explains that a failed write makes `httputil.ReverseProxy` abort the whole
  handler. That claim is correct, but the regression test calls `Invoke` with a plain `http.HandlerFunc`, so
  nothing tests it. Mutant m3 changes `Write` past the cap to `return 0, <error>`. The package tests pass with
  that mutant (`ok internal/workernode/local`). A scratch probe sent the response through a real
  `httputil.NewSingleHostReverseProxy` (with `FlushInterval -1`, as `internal/dataplane/dataplane.go:249` does)
  under a server context. With the committed fix, the probe passed: an at-cap response came back whole and a
  64 MiB response returned 413. With m3, the probe panicked (`should not panic`). The `WriteString` override is
  also never exercised. Fix: route the regression test (or a second case) through a `ReverseProxy` with a server
  context, so that a regression to a failing write is caught.
- **The `Invoker` port doc does not name the new outcome** · attribution: `model`.
  `internal/workernode/local/local.go:43-46` lists what `Invoke` returns: the output, an `*UpstreamError`
  for a non-2xx response, or a fault error on a transport or cold-wake failure. The doc does not mention the new
  `PayloadTooLarge` result for an over-cap response. The `maxInvokeBytes` comment was updated, so the behavior is
  recorded there, but not on the interface that other drivers implement. Fix: add one clause to the interface
  doc.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the issue's reason.** In the review worktree, I ran
  `git revert --no-commit 474064f` and restored the test file. `TestIssue127_InvokeCapsTheTargetResponse` then
  failed with `invoking a target that answers 33554432 bytes allocated 144376408 bytes` (a limit of 8388608).
  That is a 32 MiB response held about 4.3 times over, which matches the issue's +395 to +476 MB for 100 MB.
- **The test passes with the fix under `-race`.** After `git reset --hard 474064f`, I ran
  `go test -race -count=3 -run TestIssue127 ./internal/workernode/local/`, and all 3 runs passed.
  `go test -race -count=1 ./internal/workernode/...` also passed.
- **The behavior is fixed on the real proxy path.** The scratch `ReverseProxy` probe above passed with the fix:
  an at-cap response returned exactly `maxInvokeBytes` bytes, and a 64 MiB response returned
  `fault.PayloadTooLarge` without a panic. With the pre-fix `invoker.go` overlaid, the probe failed.
- **The root cause is fixed, not masked.** The unbounded buffer and the second `io.ReadAll` copy are both
  removed. Memory is now bounded by the cap and does not depend on a timeout or a retry. Dropping the bytes past
  the cap, instead of failing the write, is the correct choice: the probe shows that a failing write panics the
  local API handler through `ReverseProxy`. Over-cap reads still drain the target until it ends or the link
  timeout expires, but they use constant memory.
- **Mutants.** m1 changes `>` to `>=` at the cap, and the at-cap assertion fails. m2 removes the
  `if rec.over { return … PayloadTooLarge }` check, and `require.Error` fails. m3 is the survivor described
  in the first Minor.
- **Scope.** All three hunks serve the issue. No test was weakened or deleted, and no doc or ADR file was
  touched.
- **Reuse.** I found no capped `ResponseWriter` or limited-writer helper in `internal/`, `api/fault` or
  `internal/testkit`. The nearest bounds are `io.LimitReader` and `http.MaxBytesReader`, which act on the reader
  side. This invoker writes into a recorder behind `ReverseProxy`, so it needs a small writer-side wrapper. The
  wrapper embeds `*httptest.ResponseRecorder`, so `Header`, `WriteHeader` and `Flush` still work.
  `fault.PayloadTooLargef` is the existing constructor, which maps to 413 through `fault.WriteProblem`
  (`local.go:135`).
- **Conventions.** The change uses the `api/fault` error, and the op string `workernode.local.invoke` matches
  `local.go`. Imports stay at the top level, the comments explain only why, and no `any` was added to a
  signature. `gofmt -l` reports nothing.
- **ADRs.** ADR-0064 (Implemented) specifies a "capturing `ResponseWriter`" and an `Invoke` that returns
  `[]byte`, and it sets no output size. The fix keeps that design and adds the bound that the issue asks for,
  using the same value as the existing input guard. The target's 2xx and non-2xx statuses are still propagated
  verbatim below the cap. A non-2xx body over 1 MiB now becomes a 413, which is the intended effect of the bound.
  No ADR file was edited.
- **Checks.** The following commands passed:
  - `go build ./...` and `GOOS=linux go build ./...`.
  - `go vet` on host and Linux.
  - `golangci-lint run ./internal/workernode/...` on host and Linux, with `0 issues.` each.
  - `go test -count=1 ./internal/...`, with no failures.
  - `go test -tags e2e ./pkg/funcd/...`, which returned `ok` in 123.6 s.
  - The five fn-to-fn e2e scenarios under `-race`, which all passed.
- **Commit shape.** The subject is `fix(workernode): …`. The body contains `Fixes #127`, names the regression
  test, and ends with the attribution trailer. The commit covers one issue.

### Recorded, not scored

- **`env`.** `go test -race -tags e2e ./pkg/funcd/...` fails in `TestScenarioE2ETLSSelfSignedServesHTTPS` with
  a data race inside `(*Platform).Run`, in `pkg/funcd`. This fix does not touch `pkg/funcd`. The race reproduces
  with the pre-fix `internal/workernode/local` files overlaid, so it was already present. The project's e2e lane
  (`just test-e2e`) runs without `-race` and passes. A separate issue should be filed if one does not exist.

### Definition of Done

11 / 11 items hold. Item 4 (revert and mutants fail a test) holds: the revert and m1/m2 are caught, and the
surviving m3 is recorded as the first Minor.

### Model scorecard

Ledger fields (to be recorded by a later stage): issue 127, phase fix, model claude-opus-5-5 → pass,
0 blockers / 0 majors / 2 minors, 2 model-attributed, DoD 11/11.

### Recommendation

Ship as is. The two Minors can be folded in before the PR: add a test case that goes through `ReverseProxy`, and
add one clause to the `Invoker` doc. The `-race` data race in the TLS e2e test is a separate issue that was
already present.
