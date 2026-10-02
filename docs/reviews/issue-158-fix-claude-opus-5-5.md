## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #158 fix, model: claude-opus-5-5)

Change: `fix/i158`, commit b67dde4 `fix(s3gateway): honor the client's part list and ETags on CompleteMultipartUpload`.
Files: `internal/blob/s3gateway/multipart.go` (+29/-15), `internal/blob/s3gateway/scenarios_test.go` (+48).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Pre-existing data race in the package's gateway shutdown flakes `-race` runs** · attribution: `env` (third-party / harness, predates this change).
  Evidence: `go test -race -count=20 -run TestIssue158 ./internal/blob/s3gateway/` failed once in about 60 runs with
  `race detected during execution of test`. The race is between `fasthttp.(*Server).ShutdownWithContext` (from
  `Server.Close`, `s3gateway.go:182`, in the `newGateway` cleanup, `harness_test.go:116`) and
  `fasthttp.(*RequestCtx).Done`, read by a `context.propagateCancel` goroutine that `gocloud.dev/blob.(*Bucket).WriteAll`
  starts under the request context in `(*be).CompleteMultipartUpload` → `sub.Put`. The same race reproduces with the
  new test excluded: `go test -race -count=15 -skip TestIssue158 ./internal/blob/s3gateway/` → exit 1,
  `--- FAIL: TestScenarioOwnerWrites`. Any test that Puts through the gateway and then closes it can hit it; the
  fix does not add or widen it. No existing issue covers it (searched titles for race / fasthttp / s3gateway / shutdown).
  Fix: file a `kind/flaky-test` issue (area s3gateway): detach the blob write from the fasthttp request context, or
  drain in-flight handlers before shutdown. Not owned by this fix.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the `multipart.go` hunk reverted (`git revert --no-commit b67dde4`,
  test file kept): `--- FAIL: TestIssue158_CompleteHonorsPartList` at `scenarios_test.go:249`,
  `An error is expected but got nil` / `a listed ETag that does not match the stored part`. Complete with a bogus ETag
  succeeded, as the issue reports. The worktree was reset to b67dde4 afterwards and is clean.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue158_CompleteHonorsPartList`, `ok`. The whole package
  under `-race -count=1`: `ok`. The test is not skipped.
- **The issue's behavior is covered end to end** through a real SigV4 client against the running gateway: a bogus
  ETag → 400 InvalidPart; listed part 9 that was never uploaded → 400 InvalidPart; out-of-order list → 400
  InvalidPartOrder; Complete with [1,2] of {1,2,3} → object `AAABBB`. These are the issue's three effects (a)–(c)
  plus the ordering rule, and the rejected Completes leave the upload in place, so the final Complete still works.
- **Cause, not symptom**: `assemble` now iterates `in.MultipartUpload.Parts` instead of every buffered part, which
  removes the cause named in the issue. No timeout, retry or swallowed error is involved.
- **Mutants** (overlay, `-run TestIssue158`), all three killed:
  1. ETag check neutralised (`AreEtagsSame(*p.ETag, *p.ETag)`) → FAIL, "a listed ETag that does not match the stored part".
  2. Order check disabled (`false && num <= prev`) → FAIL, "parts listed out of ascending order".
  3. Missing part skipped (`ok && !AreEtagsSame`) → FAIL, "a listed part that was never uploaded".
- **Scope**: both hunks serve the issue. `TestScenarioOwnerMultipartWrite` and the other scenarios are unchanged and pass.
- **Reuse**: the change uses the library instead of writing its own. It uses `backend.AreEtagsSame` from versitygw for
  quote-insensitive ETag comparison (the package already imports `versitygw/backend` in `backend.go`), the
  `s3err.GetInvalidPartErr`, `ErrInvalidPartOrder`, `ErrMalformedXML` and `ErrNoSuchUpload` API errors, and the
  package's own `etag()` helper. The test reuses the existing harness (`newGateway`, `lakehouseMeta`, `g.client`,
  `mustGet`, `statusCode`, `ptrS`). Nothing is duplicated.
- **Conventions**: errors are S3 API errors at the S3 wire boundary, consistent with the rest of the backend; ctx-first
  call sites are unchanged; imports are at the top level; no `any` in signatures; the comment on `assemble` states the
  contract and is not bloated. `gofmt -l` is clean.
- **ADRs**: consistent with ADR-0080. The Temporary workaround "multipart assembled before a single `Put`, bounded by
  `maxUploadBytes`" is preserved (the cap check still follows `assemble`). No ADR file was touched.
- **Checks (touched package)**: `go build ./...` ok; `go vet ./internal/blob/s3gateway/` ok;
  `golangci-lint run ./internal/blob/s3gateway/...` → `0 issues.`; `go test -race ./internal/blob/s3gateway/` ok.
  Linux lint, e2e and lanes are left to the group gate.
- **Shape**: the subject is `fix(s3gateway): …`, the body has `Fixes #158` and the attribution trailer, one issue per commit.

Observation (not counted): a listed part number below 1 returns InvalidPartOrder (because `prev` starts at 0). AWS
returns InvalidArgument for it. It is still a 400 rejection and outside the issue's scope.

### Definition of Done
11 / 11 items hold. Item 8 was verified for the touched package on the host only, as this gate is scoped; Linux lint,
e2e and the lane are left to the group gate. Item 3 holds, with the env-attributed `-race` flake above noted.

### Model scorecard
Ledger fields (not recorded by this gate): claude-opus-5-5 on issue #158 (fix) → pass, 0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Pass. The fix can go to the PR. Separately, file a flaky-test issue for the pre-existing fasthttp shutdown race in the
s3gateway test harness.
