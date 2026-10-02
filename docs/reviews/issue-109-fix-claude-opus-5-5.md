## Verdict: pass — 0 blockers, 0 majors, 4 minors  (issue #109 fix, model: claude-opus-5-5)

Change: branch `fix/i109`, commit `8fbd8fa fix(blob): enforce Bucket.spec.maxObjectBytes on every write`
(`internal/blob/capped.go` new, `internal/blob/s3gateway/backend.go`, `pkg/funcd/funcd.go`,
`pkg/funcd/s3gateway_internal_test.go`).

The issue: `BucketSpec.MaxObjectBytes` was declared and validated but never read, so the S3 gateway, context.blob and
the site reconciler accepted any object up to the daemon-wide `maxUploadBytes`. ADR-0080 Contracts require both caps to
be enforced, with a write past `MaxObjectBytes` returning Forbidden. The fix wraps the shared `s3BucketFor` view in a new
`blob.Capped` decorator, which refuses an over-cap `Put` with `fault.Forbidden`. It also maps a Forbidden blob error to
S3 `AccessDenied` (403) instead of `InternalError` (500).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1: the RangeReader forwarding in `blob.Capped` is untested** · attribution: `model`.
  Evidence: a mutant that deletes the `if rr, ok := inner.(RangeReader)` branch in `internal/blob/capped.go` survived
  the tests of `./pkg/funcd/`, `./internal/blob/...`, `./internal/edge/static/` and `./internal/site/` (all `ok`). If
  this forwarding is lost, the s3gateway silently drops its ranged-read fast path (ADR-0080) on every capped Bucket and
  falls back to a full Get plus a slice. The result is still correct, so this is a performance regression with no test
  that catches it. Fix: add a small unit test in `internal/blob` that asserts `Capped(rangeReaderBucket, n)` still
  implements `RangeReader` and forwards `GetRange`, mirroring how `Prefixed` is covered.
- **Minor 2: an over-cap multipart upload keeps its buffered parts in memory** · attribution: `model`.
  Evidence: `internal/blob/s3gateway/multipart.go` `CompleteMultipartUpload` calls `b.mp.abort(id)` on the
  `maxUpload` branch but not when `sub.Put` fails. Before this fix, a failing `Put` was an unusual error. Now a
  predictable 403 from the per-bucket policy reaches that branch, and the parts stay buffered until the client sends
  `AbortMultipartUpload`. The per-bucket cap is also checked only at Complete, not at `UploadPart`, and
  `PutObject` buffers up to `maxUpload` before it rejects. The ADR's effective limit,
  min(MaxObjectBytes>0, maxUploadBytes), is enforced, but late. Fix: abort the upload on a Forbidden `Put`.
  Optionally, also check the cap earlier.
- **Minor 3: a presigned PUT URL is not capped** · attribution: `adr`.
  Evidence: `internal/services/blob/blob.go` `SignedURL` returns a substrate-driver presigned URL, and a presigned
  PUT on that URL bypasses `Capped.Put` (ADR-0127). The local fileblob and mem drivers do not support signing
  (`blobcontract` "signed-url-unsupported-locally"), so this path cannot be reached on the V1 local substrate. On a
  cloud substrate, capping a presigned PUT needs a design decision: a content-length condition exists only in a POST
  policy. Because of this gap, the commit's claim of "every write" is slightly too broad. This is recorded but not
  scored.
- **Minor 4: a pre-existing data race in the s3gateway test harness** · attribution: `env`.
  Evidence: `go test -race -count=5 ./internal/blob/s3gateway/` failed `TestScenarioOwnerWrites` and
  `TestIssue30_MultipartTotalCappedAtUploadPart` with `DATA RACE` in third-party code:
  `fasthttp.(*Server).ShutdownWithContext` (from `harness_test.go` cleanup, `Server.Close`) races
  `fasthttp.(*RequestCtx).Done`. A single `-race` run of the test alone passes. The change does not touch the
  shutdown path; its only s3gateway edit adds one `mapBlobErr` case. No existing issue was found. This should be
  filed separately as a `kind/flaky-test` issue. It is not scored.

### ✅ Verified correct (keep it)
- **Fails without the fix.** With the non-test files reverted (`git revert --no-commit 8fbd8fa`, keeping the new test),
  `TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite` fails for the issue's reason: the gateway logs
  `200 | PUT | /lake/raw/big.parquet`, and the test reports `a 20-byte PutObject into a Bucket with maxObjectBytes=16
  must be 403, got <nil>`. After `git reset --hard 8fbd8fa`, the worktree is clean at that HEAD.
- **Passes with the fix**, un-skipped, under `-race`. `TestIssue109_…`, `TestScenarioS3GatewayDisabledByDefault` and
  `TestScenarioS3GatewayEnabledOpensListener` pass, and `go test -race ./pkg/funcd/ ./internal/blob/gocloud` is `ok`.
- **User-visible behavior.** The test runs the issue's own steps through the real gateway: a real AWS SDK client signs
  with the derived owner keypair and the real cedar PEP authorizes the request. A 20-byte PUT is 403 and nothing is
  stored, a 16-byte PUT at the cap lands, and a direct view `Put` is `fault.Forbidden`.
- **Root cause, not symptom.** The view that all four consumers share (`s3BucketFor`: S3 frontend PutObject and
  CompleteMultipartUpload, the `services/blob` facade for context.blob, the site reconciler, and static and eventing
  reads) now carries the spec. The cap is enforced once, at the seam where the issue says the spec was dropped.
- **Mutants**, each run on its own and then restored:
  - Changing `>` to `>=` in `Capped.Put` is killed: the at-cap PUT returns 403.
  - Dropping the `fault.Forbidden → accessDenied()` case is killed: the response is 500 `InternalError`.
  - Not reading `b.Spec.MaxObjectBytes` in `s3BucketFor` is killed: the PUT returns nil.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted, and no ADR file was touched.
- **Reuse.** `blob.Capped` follows the existing `blob.Prefixed` decorator pattern, including the optional
  RangeReader forwarding, and duplicates nothing. No size-policy wrapper existed in `internal/blob`,
  `internal/services/blob` or `internal/platform` before this change. Errors use the existing `fault.Forbiddenf`, and
  the 403 reuses the gateway's `accessDenied()`.
- **Conventions (ADR-0002, CLAUDE.md).** The fix uses `api/fault` errors, has no `any` in signatures, puts imports at
  module top level, keeps comments short with the ADR reference, and introduces no new dependency.
- **ADRs.** The fix matches ADR-0080 Contracts ("Two distinct caps, both enforced"; the effective limit is
  min(MaxObjectBytes>0, maxUploadBytes)). The `maxUpload` checks stay in place, and `MaxObjectBytes == 0` means
  unset, so the view is returned unchanged.
- **Checks on the touched packages.** `gofmt -l` is clean, `go build ./...` passes, `go vet` is clean, and
  `golangci-lint run ./internal/blob/... ./pkg/funcd/` reports 0 issues. Tests pass under `-race`, except for the
  pre-existing race in Minor 4. The e2e suite, Linux lint and the lanes are left to the group gate.
- **Shape.** The subject is `fix(blob): …`, the body has `Fixes #109` and the Co-Authored-By trailer, and the commit
  covers one issue.

### Definition of Done
11 / 11 items hold, with item 8 limited to the touched packages on the host; e2e, Linux lint and the lanes are left to
the group gate. Misses: none. Minor 1 is a test gap on a secondary line, not on the fix's key lines.

### Model scorecard
Not recorded here. The orchestrator records the ledger row: claude-opus-5-5 on issue #109 (fix) → pass, 0/0/4,
2 model-attributed, DoD 11/11.

### Recommendation
Ship it. Optionally fold in Minor 1 (a RangeReader forwarding test) and Minor 2 (abort the multipart upload on a
Forbidden Put) before the PR. Route Minor 3 (capping presigned PUTs) to `/adr` if a cloud substrate lands, and file
Minor 4 as a separate flaky-test issue.
