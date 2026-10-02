## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (issue #30 fix, model: claude-opus-5-5)

Commit reviewed: `f40d987` `fix(blob): bound each S3 multipart upload by maxUploadBytes and drop abandoned uploads`
(branch `fix/199-unbounded-memory`, group HEAD `474064f`). Only #30's commit is in scope. It touches
`internal/blob/s3gateway/multipart.go` plus three test files.

The fix closes both halves of the issue at the root. `putPart` now keeps a running size per upload and rejects the
part that would push the upload past `maxUploadBytes` with `EntityTooLarge`. `create` now drops uploads that have
received no part for `multipartIdleExpiry` (1h). One key line is not pinned by any test: a part arriving is
supposed to refresh the idle window, and nothing checks it. If that line regressed, a healthy, slow upload would
be dropped in the middle of the transfer, and the tests would still pass. That gap is the one change requested.

### 🟡 Major 1 — the idle-window refresh on UploadPart is untested (surviving mutant)  ·  attribution: model
Evidence: an overlay mutant that deletes `u.touched = m.now()` from `putPart` (`internal/blob/s3gateway/multipart.go`)
leaves the whole package green:

```
== notouch
ok  	github.com/pyvvo/funcd/internal/blob/s3gateway	0.789s
```

`TestIssue30_AbandonedMultipartUploadExpires` creates `live` at T+h/2, puts a part at T+h, and sweeps at T+h+1s.
`live` survives that sweep because it was *created* inside the window, so the test never proves that a part
extends the window. The assertion message, "an upload that received a part within the window is kept", claims a
property the test does not check. The unguarded regression causes data loss for the client: an active upload that
has run longer than an hour since `Create` would be swept, and its `Complete` would return `NoSuchUpload`. DoD item 4
does not hold.
Fix (builder): arrange the clock so the live upload's `Create` is outside the window and its last part is inside
it. For example, create both uploads at T, put a part on `live` at T+h−1s, and sweep at T+h+1s. Then assert that
`live` is kept and `abandoned` is dropped.

### Minor
- **The clock seam duplicates the ADR-0002 clock port** · attribution: model · `multipartStore.now func() time.Time`
  (`internal/blob/s3gateway/multipart.go`) hand-rolls what `internal/platform/clock.Clock` already provides.
  ADR-0002 names that port as the canonical injected time dependency, and `internal/activator`, `internal/funclog`
  and `internal/workflow` use it. There is one precedent for the bare-func form (`internal/network/egress/forwarder.go`),
  and `clock.Fake` cannot advance, so the test would need a small settable fake. That keeps this a Minor. Fix:
  hold a `clock.Clock`, default it to `clock.System()`, and give the test a settable fake.
- **No daemon-wide bound across concurrent uploads** · attribution: adr · each upload is now capped at
  `maxUploadBytes` and dropped after 1h idle. A principal can still open N uploads within the hour, and each can
  hold up to the cap. The issue mentions this ("it can open any number of uploads"). ADR-0080 caps "a single
  buffered multipart object" and sets no aggregate budget, so a cap on concurrent uploads or total bytes is a
  decision, not a fix. Concurrent `PutObject` bodies share the same per-request shape. Recorded, not scored.
  Route to `/adr`, or to the streaming-blob-seam exit that ADR-0080 already names.
- **A data race in the package's `-race` runs predates this fix** · attribution: env (third-party) · fasthttp
  `(*Server).ShutdownWithContext` (reached from `s3gateway.(*Server).Close` in `newGateway`'s cleanup) races with
  `(*RequestCtx).Done`. The request context of a completed `blob.Put` is the parent of the gocloud writer context.
  Interleaved loop of the race-built test binary: head **3/20** failing, pre-fix base (`f40d987^`) **6/20**. The
  race hits `TestScenarioOwnerWrites` on the base, and the new `TestIssue30_MultipartTotalCappedAtUploadPart`
  (through `CompleteMultipartUpload`'s `Put`), so the fix did not introduce it. No open issue covers it. It
  should be filed as a flaky-test issue for `area/blob`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit f40d987` kept the new
  test files, and the external scenario test then failed at the over-cap part:
  `TestIssue30_MultipartTotalCappedAtUploadPart … An error is expected but got nil … a part past the upload's cap
  must be rejected`. This is the reported behavior: per-part cap only. The white-box
  `TestIssue30_AbandonedMultipartUploadExpires` does not compile against the pre-fix API (`putPart` returned
  `bool`, and there was no `now` or `multipartIdleExpiry`). Its behavioral revert was therefore checked with an
  overlay that removes only the sweep loop, and the test failed (`--- FAIL: TestIssue30_AbandonedMultipartUploadExpires`).
- **Passes with the fix under `-race`.** `go test -race -count=3 -run TestIssue30_` ran 6/6 PASS. Both tests are
  un-skipped.
- **Mutants on the cap line are killed.** Removing the size check, dropping the replaced-part subtraction
  (`u.size + len(data)`), and changing `>` to `>=` each fail `TestIssue30_MultipartTotalCappedAtUploadPart`.
  The re-send-replaces-a-part assertion is the one that kills the subtraction mutant.
- **Root cause, not symptom.** The total is enforced at `UploadPart` under the store mutex, so concurrent parts
  cannot overshoot. Abandoned uploads no longer live until a restart. A sweep only on `create` keeps memory
  bounded, because `create` is the only way the number of uploads grows. The existing check at Complete
  stays as a backstop.
- **Fail-closed per ADR-0080.** An over-cap part returns `EntityTooLarge` (HTTP 400), which is the error that
  `readCapped` and the Complete check already use. `NoSuchUpload` is unchanged for an unknown id. The ADR-0080
  Decision and Contracts are not contradicted, and no ADR file was edited.
- **Scope.** Every hunk serves #30. The variadic `opts ...func(*s3gateway.Deps)` on `newGateway` follows the
  harness idiom in `internal/function` (`newHarness`, `newShimHarness`). Existing callers are unchanged, and no
  test was weakened or deleted.
- **Checks.** `gofmt -l` is clean. `go build ./...` passes on the host and with `GOOS=linux`. `go vet` passes on
  the host and Linux. `golangci-lint` reports 0 issues on the host and Linux. The package tests are green apart
  from the pre-existing race above. `go test -race` on the gateway tests in `pkg/funcd` is ok. The e2e suite
  `go test -tags e2e ./pkg/funcd/...` is ok (111s), though no e2e test exercises multipart. `just check-hygiene`
  is clean. The Lima lanes were not run, as instructed.
- **Shape.** The subject is `fix(blob):`, the body has `Fixes #30` and the attribution trailer, and the commit
  covers one issue. The commit message names both regression tests.

### Recommendation
Return to `/fix` for one small rework: make `TestIssue30_AbandonedMultipartUploadExpires` prove that a part
refreshes the idle window (Major 1), and optionally switch the seam to `clock.Clock`. File the pre-existing
s3gateway shutdown race as its own flaky-test issue. Route the aggregate multipart budget to `/adr` if a
daemon-wide bound is wanted before the streaming blob seam lands.
