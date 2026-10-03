## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #462 fix, model: claude-opus-5-5)

Change: branch `fix/i462`, commit 2834384 `fix(s3gateway): stop blob ops from racing the gateway shutdown`
(`internal/blob/s3gateway/{backend.go,multipart.go,s3gateway.go,scenarios_test.go}`, +94/-1).

The issue: the S3 gateway backend passed fasthttp's pooled `RequestCtx` to the blob substrate. A context
derived from it (gocloud's `NewWriter` `WithCancel`) starts a `propagateCancel` goroutine that reads
`RequestCtx.Done` (`ctx.s.done`), which races the `s.done = nil` write in fasthttp's `ShutdownWithContext`.
The fix gives every backend op its own context through `be.opContext`: `WithCancel(WithoutCancel(ctx))`
keeps the request's values and drops the `RequestCtx` as a cancel parent. The context ends when the op
returns, or earlier when the gateway closes (`context.AfterFunc` on a lifetime context that `Server.Close`
cancels before `api.ShutDown`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **One graceful-shutdown stall, not reproduced** · attribution: `env` (unconfirmed) · Two of about 30 runs
  on the fix branch took about 10 s. One was a `-count=5 -run TestScenarioOwnerWrites` batch (10.05 s) and
  the other a whole-package run (10.12 s). This matches versitygw's `shutDownDuration = time.Second * 10` in
  `s3api/server.go:44`: one `Close` waited for the full graceful timeout. Neither run reported a failure or
  a race. Twelve later runs with per-test timing (`-json`) and six reruns of the same batch command found no
  test over 1 s. The stall was not tied to the fix, and it may have come from load on the shared host.
  Not scored. The group gate should watch the package's wall time. If the stall comes back, it is a separate
  issue: a shutdown that waits for a keep-alive connection.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** I ran `git revert --no-commit 2834384`, kept
  the test file from HEAD and ran `go test -race -run TestIssue462`. It failed with `FAIL
  TestIssue462_BlobContextEndsWithRequest`: `Expected error with "context canceled" in chain but got nil`
  ("the blob context ends with the request"). The substrate had received the pooled `RequestCtx`, which
  does not end with the request. A plain revert also removes the test (`[no tests to run]`), so this check
  needed the test file restored. After the check, `git reset --hard 2834384` left the worktree clean at
  that HEAD.
- **The test passes with the fix under `-race`.** `-count=3 -run 'TestIssue462|TestScenarioOwnerWrites'`
  passed, and `go test -race -count=1 ./internal/blob/s3gateway/...` passed.
- **The issue's own steps are fixed.** Main reproduced the race: one of three `-race -v` package runs
  reported `WARNING: DATA RACE`. On the fix, 9 batches of `-race -count=5 -run TestScenarioOwnerWrites` and
  13 package runs reported no race.
- **Cause, not symptom.** The `RequestCtx` is no longer an ancestor of any context the backend creates, so
  no `propagateCancel` goroutine reads `RequestCtx.Done`. There is no added timeout, retry, swallowed error
  or skipped test. Shutdown semantics are kept: `Close` still cancels an op in flight (asserted by the
  test's second half). Before the fix, that cancellation came from fasthttp closing `s.done`.
- **Mutants (all killed, by overlaying the edited `backend.go`):**
  1. `WithCancel(ctx)`, with the `WithoutCancel` detach removed: `WARNING: DATA RACE` ×7, which fails
     `TestIssue462_…`, `TestScenarioOwnerWrites`, `TestScenarioOwnerMultipartWrite` and
     `TestScenarioBindingGrantsRead`. This is the issue's race, so the scenario suite under `-race` also
     guards the fix.
  2. The `AfterFunc(b.life, cancel)` tie removed: `TestIssue462_…` fails after the 10 s shutdown timeout,
     because `Close` no longer cancels the blocked Put.
  3. `cancel()` dropped from the end func: `TestIssue462_…` fails with "the blob context ends with the
     request" ×3.
- **Coverage of the backend surface.** Every method that passes `ctx` to the substrate, the PEP or the
  metastore calls `opContext` first: GetObject, HeadObject, `listing` (ListObjects and ListObjectsV2),
  PutObject, DeleteObject, DeleteObjects, HeadBucket, ListBuckets, and the five multipart ops.
  `GetBucketAcl` only reads a context value (`accountFromCtx`), which creates no derived context and starts
  no goroutine. The other overrides ignore `ctx`. `GetObject` buffers the body before it returns, so
  cancelling the op context on return cannot truncate a read.
- **The values are preserved.** `WithoutCancel` keeps `Value`, so `caller` and `accountFromCtx` still see
  the authenticated account. The PEP scenarios pass.
- **Lifetime handling in `New`.** The `stop` function is called on both early-error returns, from
  `randomRoot` and `s3api.New`, so the lifetime context does not leak. `Close` calls `stop()` under the
  mutex before `ShutDown`, and the existing `closed` flag keeps it idempotent.
- **Scope.** All four hunks serve the issue. No test was weakened or deleted, and no doc or ADR file was
  touched.
- **Reuse.** The detach idiom `context.WithCancel(context.WithoutCancel(ctx))` matches an existing
  precedent (`internal/sensor/retry.go:156`). No shared helper exists that the fix should have used, and
  the fix adds no new dependency. The test double `ctxBucket` embeds `blob.Bucket` and reuses the package
  harness (`newGateway`, `memBucket`, `lakehouseMeta`, `fixedPolicies`, `g.client`). It hand-writes no
  server or client.
- **Conventions.** The signatures put `ctx` first, there is no `any` in a signature, the imports are at the
  top level, and the comments are short and explain why (the issue reference on `opContext`). The test is
  named `TestIssue462_…` like its neighbour `TestIssue380_…`. ADR-0085 (`Close` shuts down gracefully) and
  ADR-0080 (the PEP on every op) are honoured, and no Accepted or Implemented ADR is contradicted.
- **Checks on the touched package.** `go vet ./internal/blob/s3gateway/...` passed, `golangci-lint run
  ./internal/blob/s3gateway/...` reported `0 issues.`, `gofmt -l` was clean, and the `-race` tests passed.
- **Commit shape.** The subject is `fix(s3gateway): …`, the body has `Fixes #462` and the `Co-Authored-By`
  trailer, and the change is one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist). For item 8, the host build, vet, lint and `-race` tests of the touched
package are green. Linux lint, the repo-wide tests and e2e are left to the group gate, as this run's scope
requires.

### Model scorecard
To record: claude-opus-5-5 on issue #462 (fix) → pass, 0/0/1, 0 model-attributed, DoD 11/11. The ledger
row is returned to the caller and is not written by this review.

### Recommendation
Ready to merge after the group gate. If the group gate sees the package's wall time jump to about 10 s
again, file the shutdown stall as a separate issue. It is not a defect of this fix.
