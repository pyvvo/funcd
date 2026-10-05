# Fix review — issue #709 (S3 folder-marker key prefix/ collapses onto prefix)

- **Change**: branch `fix/w15c-i709`, commit 078a8708 `fix(s3gateway): keep the folder-marker key prefix/ apart from prefix`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 11 of 12 items hold

## Summary

The gateway now uses the S3 key itself as the substrate key for every object verb. `splitKey` only selects the
prefix to authorize. The gateway's `blobKey` is removed, and `context.blob`'s `blobKey` always returns
`prefix/object`. The folder marker `bronze/` and the object `bronze` are now two distinct keys, and writes
under a marker succeed on the file substrate. This is the fix the issue proposed, and ADR-0080 and ADR-0127
both allow it. One Minor test gap remains: the HeadObject path is not pinned by the test.

## Verification run

- **Prove first, on current origin/main.** `git diff` between the merge base and `origin/main` is empty for
  `internal/blob` and `internal/services/blob`. The touched packages are therefore identical to current main.
  The overlay of the `origin/main` versions of `backend.go`, `multipart.go` and `services/blob/blob.go` makes
  both regression tests FAIL for the issue's reasons:
  - s3gateway, file substrate: `PUT bronze/x.parquet` returns `StatusCode: 400 … InvalidRequest` after the
    marker (case 1 of the issue).
  - s3gateway, mem substrate: the substrate keys are `["bronze"]`, expected `["bronze", "bronze/"]`
    (case 2 of the issue).
  - services/blob: the substrate keys are `["p"]`, expected `["p", "p/"]`. The file substrate fails on "a write
    under the marker" (the `context.blob` case, which the issue only read and did not run).
- **With the fix**: `go test -race -count=1` passes for `./internal/blob/s3gateway/` and `./internal/services/blob/`.
- **Mutants** (overlays; only the relevant tests were run):
  1. HeadObject `blob.Stat(ctx, sub, strings.TrimSuffix(key, "/"))`: **survived**. See Minor 1.
  2. DeleteObject `sub.Delete(ctx, strings.TrimSuffix(key, "/"))`: killed by `TestIssue709_FolderMarkerKeepsItsOwnKey`.
  3. services/blob `blobKey` regains `if object == "" { return prefix }`: killed by `TestIssue709_EmptyKeyIsTheFolderMarker`,
     `TestScenarioBlobList` and `TestListExcludesSiblingPrefixSharingName`.
- **vet**: clean. **golangci-lint** (touched packages): `0 issues`.
- The worktree is clean. The overlays and mutants live only in scratch files.

## Blockers

None.

## Majors

None.

## Minors

1. **The HeadObject key is not pinned by a test** (attribution: `model`). The test sends HEAD `bronze/` after it
   stores `bronze/`=`MARKER` and `bronze`=`OBJECT`. It then checks `ContentLength == len("MARKER")`, but
   `len("OBJECT")` is also 6. A HeadObject that still collapses the key onto `bronze` therefore passes
   (mutant 1 survived). The fix itself is correct, because HeadObject uses `key`. Bodies of different lengths,
   or an ETag comparison, would close the gap.

## Verified correct

- **Cause, not symptom.** The key aliasing in `splitKey` plus `blobKey` is removed at its source in every verb
  the issue lists: Get, Head, the listings, Put, Delete, DeleteObjects and multipart Complete. Create, UploadPart,
  Abort and ListParts (`multipart.go`) already used only the prefix for authorization and the full key for the
  upload target.
- **Listing equivalence.** `sub.List(ctx, keyPrefix)` is equal to the old `blobKey(prefix, objPrefix)` for every
  input except a trailing `/` with an empty rest, which was the bug. The prefix filter at `backend.go:292` is unchanged.
- **context.blob List** `strip := blobKey(b.Prefix, "")` now yields `p/`, which is the same as the old
  `blobKey(p, "") + "/"`.
- **Sibling.** `services/blob/blob.go` had the same empty-object collapse. It is fixed and tested in the same
  commit, which keeps the two keyspaces identical as ADR-0127 requires. No other `blobKey`/`splitKey` rebuild
  remains (grep).
- **Every case of the issue has its own test**: the file-backend write block, the mem-backend aliasing for
  every verb, and the `context.blob` empty key.
- **Scope.** Every hunk serves the issue, and no test was weakened or deleted.
- **Reuse.** No new helper is added. The tests reuse the existing harness (`newGateway`, `lakehouseMeta`,
  `memBucket`, `objectKeys`, `newMapBucket`, `sortedKeys`, `newFacade`) and `gocloud.FileURL`.
- **Conventions.** Imports are at the top level. The one added doc sentence on `splitKey` states a why.
- **ADRs.** No ADR file was edited. ADR-0080 (the leading segment selects the prefix) still holds.
- **Commit shape**: `fix(s3gateway):`, Cause/Fix/Tests body, `Fixes #709`, attribution trailer, one issue.

## Observation (no finding)

A marker written before this fix is stored under the bare key `bronze`. After the fix it reads as the object
`bronze`, not as `bronze/`. The issue's workaround (delete it) still applies. No migration is needed at this
design phase.

## Recommendation

Pass. Optionally tighten the HEAD assertion (Minor 1) before the group PR. This does not block.
