# Fix review — issue #374 (presigned PUT skips maxObjectBytes) — claude-opus-5-5

- **Issue**: #374 "A presigned PUT URL skips the Bucket's maxObjectBytes cap"
- **Change**: branch `fix/i374`, commit `9af1480 fix(blob): refuse a presigned PUT on a Bucket with maxObjectBytes`
- **Files**: `internal/blob/capped.go` (+10/-2), `internal/services/blob/blob_test.go` (+17)
- **Governing ADRs**: ADR-0080 (Bucket.spec.maxObjectBytes, "a write must satisfy" the cap), ADR-0127 (context.blob.signedUrl), ADR-0002 (conventions)
- **Verdict**: **pass**

## Severity tiers

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct

- **Fails without the fix, for the issue's reason.** Reverted the fix in `internal/blob/capped.go` (test file kept at HEAD) and ran `TestIssue374_PresignedPutRefusedOnCappedBucket`: FAIL, `expected: "forbidden"`, `actual: ""` — the capped view signed the PUT, which is exactly the reported bypass. The worktree was reset to `9af1480` and left clean.
- **Passes with the fix under `-race`**: `go test -race ./internal/blob/... ./internal/services/blob/...` all `ok`; the regression test PASS, un-skipped.
- **Cause, not symptom.** The issue names `blob.Capped` overriding only `Put` while forwarding `SignedURL`. The fix adds `cappedBucket.SignedURL`, which refuses `SignPut` with `fault.Forbidden` and forwards GET/DELETE. `cappedRangeBucket` embeds `*cappedBucket`, so the override is promoted on both views. `SignOptions` carries only `Method` and `Expiry`, so binding a size limit is not expressible without a port change; refusing is the issue's first listed expected behavior and the minimal fix. The only production caller is `pkg/funcd/funcd.go` `s3BucketFor` (`Capped(Prefixed(...))`), so every capped Function-facing bucket is covered. Uncapped buckets (`maxBytes <= 0`) return `inner` unchanged and still sign PUT; the existing `TestScenarioBlobSignedURL` keeps covering that.
- **Method normalization holds**: the only producer of `SignOptions` from the wire (`internal/workernode/local/blob.go` `signOptsFromQuery`) maps to the typed `SignPut`/`SignDelete`/`SignGet` constants, so the `== SignPut` comparison cannot be bypassed by case or spelling.
- **Mutants (3/3 killed)**, run with `-run 'TestIssue374|TestScenarioBlobSignedURL'`:
  1. guard replaced by `false` → FAIL;
  2. `SignPut` → `SignDelete` in the guard → FAIL;
  3. `fault.Forbiddenf` → `fault.Invalidf` → FAIL.
- **Scope**: two hunks, both serve the issue (the override plus its doc-comment line, and the regression test). No test weakened or removed.
- **Reuse**: the override reuses the existing embedding decorator (`cappedBucket`), the `fault.Forbiddenf` helper, the typed `SignMethod` constants, and the test file's existing `newFacade`, `newMapBucket` and `s3PDP` harness. No new helper, type or dependency; nothing duplicated (`Prefixed.SignedURL` is the sibling pattern it mirrors).
- **Conventions (ADR-0002, CLAUDE.md)**: `api/fault` error with an op name matching `blob.Capped.Put`'s style, ctx-first, typed enum, no `any`, no new imports, comment explains the why only. `go vet` clean; `golangci-lint` on the touched packages: 0 issues.
- **ADRs**: no ADR file touched. ADR-0127 already requires `s3::write` for a PUT sign and does not promise signing on a capped bucket; refusing it enforces ADR-0080's "a write must satisfy" the cap. No contradiction.
- **Shape**: `fix(blob):` subject, body names the cause and the test, `Fixes #374`, attribution trailer, one issue in one commit.
- **Dev-machine hygiene**: checked; clean.

## Checklist (Definition of Done)

| # | Item | Result |
|---|---|---|
| 1 | `TestIssue374_…` reproduces the behavior | ✅ |
| 2 | Fails on pre-fix code for the reported reason | ✅ |
| 3 | Passes with the fix under `-race` | ✅ |
| 4 | Revert / mutants fail a test | ✅ (3/3) |
| 5 | Root cause fixed, not masked | ✅ |
| 6 | Scope only; no weakened test | ✅ |
| 7 | No ADR contradicted or edited | ✅ |
| 8 | Repo-wide build/lint (host + Linux), e2e | deferred to the group gate (touched packages green here) |
| 9 | Conventions | ✅ |
| 10 | Reuse, no duplication | ✅ |
| 11 | Commit shape | ✅ (PR not yet opened) |

10 of 10 applicable items pass (item 8 deferred to the group gate).

## Recommendation

Pass. Hand back to `/fix` Step 8; the group gate runs the repo-wide checks.
