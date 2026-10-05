# ADR-0159 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: docs/adr/0159-blob-content-digest-and-metadata.md (Realizes FEAT-0003/F47)
- **Work**: branch `feat/adr-0159-blob-digest-metadata`, one commit `554a14d8` on `origin/main` (41 files, +813/-158)
- **Model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./...`, `go vet -tags e2e ./pkg/funcd/...`, `go vet -tags dev ./cmd/funcdctl/` | exit 0 each |
| `GOOS=linux go vet ./...` | exit 0 |
| `go tool golangci-lint run ./...` | 0 issues, exit 0 |
| Linux lint, as `scripts/agent/gate.sh` runs it (host-built linter, `GOOS=linux … run ./internal/...`) | 0 issues, exit 0 |
| `go test -race -count=1` over `internal/blob/...`, `internal/edge/static/...`, `internal/services/blob/...`, `internal/eventing/...`, `internal/site/...`, `internal/funclog/...`, `internal/kvstore/badger/...`, `internal/workernode/local/...`, `internal/dataplane/...` | all `ok`, exit 0 |
| Named Scenario, contract and #600 tests, `-race -v` | all PASS, none SKIP (the contract `attributes-digest` runs, it does not skip, on memory and on file) |
| `go.mod` / `go.sum` | unchanged |

`just ci`, the e2e suite and `go test ./...` were not run here; the PR gate runs them once.

### Overlay mutants (`go test -overlay`; the original files were not changed)

| # | Mutation | Killed by |
|---|---|---|
| M1 | `gocloud.listMD5`: the `file://` case returns `obj.MD5` and drops the Attributes read that the Temporary workaround needs | `TestADR0159_FileListCarriesAttributesMD5`, `TestScenario_DriverConformanceParity` (file/attributes-digest) |
| M2 | `writePreconditions`: the `If-Match: *` branch is disabled, so `*` goes to versitygw's tag comparison | `TestScenarioStaleIfMatchPutRejected` |
| M3 | `multipartStore.create` drops the CreateMultipartUpload `PutOptions` | `TestScenarioContentTypeAndMetadataRoundTrip` |

All three mutants failed tests.

## Scenarios → tests (all un-skipped and passing)

| Scenario | Test |
|---|---|
| etag-on-every-read | `TestScenarioETagOnEveryRead` (memory + file: HEAD, `bytes=0-3`, ListObjectsV2) |
| stale-if-match-put-rejected | `TestScenarioStaleIfMatchPutRejected`: the stale PUT is 412 and the bytes are unchanged; the current ETag succeeds; every write row of the Contracts table (`*` on an existing object, 404 when absent, 501 ×2); a stale Complete is 412 and the same upload then completes |
| if-none-match-not-modified | `TestScenarioIfNoneMatchNotModified` (ETag and `*`, GET + HEAD → 304; another ETag → 200 + body) |
| content-type-and-metadata-roundtrip | `TestScenarioContentTypeAndMetadataRoundTrip` (PUT and multipart, HEAD + GET; an unparsable type → 400) |
| no-digest-no-etag | `TestScenarioNoDigestNoETag`: a sidecar-less file sends no ETag on HEAD/GET/LIST, and no `<ETag>` appears in the raw XML; `If-Match` → 412 on GET/HEAD/PUT and the bytes on disk are unchanged; `If-None-Match` → 200; `If-Match: *` → 200; the Get count equals the number of 200 GETs |
| conditional-ranged-read-stays-ranged | `TestScenarioConditionalRangedReadStaysRanged` (1 MiB object, 206, one `GetRange`, zero `Get`) |

## Contracts and Review checklist

- [x] **Port per Contracts, no driver import; views forward; `Capped` still refuses oversize.** `internal/blob/blob.go` matches
  the Contracts block field for field (`Bucket.Attributes`, `Put(…, PutOptions)`, `Attributes.MD5/ContentType/Metadata`,
  `PutOptions`). `Prefixed.Attributes` forwards the prefixed key and returns the caller's key (`TestPrefixedAttributesReturnsCallersKey`).
  `cappedBucket.Put` keeps the size check and forwards `opts`; it gets `Attributes` by promotion. A grep finds no other
  production wrapper that embeds `Bucket`. `TestSite_ObjectOverBucketCapIsNotReadyNotRetried` passes.
- [x] **`s3://` leaves `MD5` nil; `file://` `List` fills from `k.b.Attributes`, nil on a failed read.** `gocloud.go`
  `bucket.s3` is set from the URL scheme at `Open`. `Attributes` clears `MD5` for `s3`. `listMD5` returns nil for s3,
  the per-entry `k.b.Attributes` result (nil on error) for file, and `obj.MD5` otherwise.
- [x] **`Put` stays on `WriteAll`; a non-empty `ContentType` is checked with `mime.ParseMediaType` first.** Yes. `mapErr`
  adds `gcerrors.InvalidArgument → fault.Invalid`, and `TestADR0159_AttributesDigestAndContentType` covers both the bad
  type and the empty metadata key.
- [x] **`Content-Type` and `x-amz-meta-*` round-trip through PUT and multipart.** `PutObject` passes
  `deref(in.ContentType)`/`in.Metadata`. The `upload` keeps cloned options, because fiber reuses its buffers and the
  upload outlives the request. `assemble` returns them to `Put`.
- [x] **Every read path carries `objectETag` or a nil `ETag`; ranged GET makes no whole-object `Get`; no full read computes a
  digest; preconditions per Decision 5 with a nil output; no-digest semantics per Decision 6.** GetObject (`backend.go:206`),
  HeadObject (`:277`) and `listing` (`:308`, shared by V1 and V2) all set `GetPtrFromString(objectETag(…))`; no other
  backend method emits an object ETag. The order in `readPreconditions` matches the versitygw v1.6.0 source that was read
  (`backend/common.go`): the If-Match-only evaluation is a match for `*`, then `datePreconditions` gets the full set, then
  If-None-Match only. Both GET and HEAD return `nil, cerr`. `writePreconditions` handles a lone `If-Match: *` itself and
  sends everything else to `EvaluateObjectPutPreconditions`. Writes Stat only when a header is set, after buffering,
  immediately before `Put`; `createOnly` is gone.
- [x] **#600's tests pass unchanged; no existing test weakened.** `TestIssue111_*` pass; the one added case is the one the
  plan asks for. `TestIssue425` is tightened, not weakened: it now requires the object's ETag. `pinnedModTime` moves
  its override from `List` to `Attributes`, because `Stat` reads that now, and `TestIssue161` still pins `ModTime`.
- Plan step 1/3 comment rewording is done: `stat.go`, the `versionOf` doc, `cloudevent.go`, the `backend.go`
  GetObject/HeadObject docs, the PutObject/Complete docs that name `writePreconditions`, and the `datePreconditions` doc
  that cites §13.2.2.
- Plan step 4: the Decision 8 production callers pass `blob.PutOptions{}`. All the named fakes are updated: the
  embedding fakes forward `opts`; `mapBucket`, `blobMapBucket`, `fakeBucket` and `noRangeBucket` implement `Attributes`
  with `fault.NotFound` when the key is absent. `tooLarge` no longer exists on main (ADR-0148 landed), so `recorder` is
  the only one affected, as the ADR expects.

## Findings

### Minor

1. **Two names that are easy to confuse** (`model`). `backend.go` adds the I/O wrapper `writeConditions(ctx, sub, key, …)`
   beside the Contracts' pure `writePreconditions(attrs, found, …)` in `multipart.go`. The two names differ by four
   letters, and the PutObject/Complete docs name the inner one. The wrapper is correct and replaces #600's `createOnly`
   as the plan intends. A more distinct name (for example `statWritePreconditions`) would read better. Not required.

### Not scored (process)

- The ADR still reads `Accepted`, F47's `content digest` segment is not yet advanced, and ADR-0007 does not yet have its
  "Superseded in part by ADR-0159" back-link. This campaign defers every doc edit to the wave's docs PR, so these are
  not model findings. That PR owes the `Accepted → Reviewing → Implemented` moves, the F47 segment, and the ADR-0007
  back-link.

## Verified correct — keep it

- The port change follows the Contracts exactly. `Stat` now does one keyed `Attributes` call instead of a prefix `List`,
  and maps `NotFound` and `Invalid` to `found=false`. A directory key still reports not found, because fileblob's
  `forKey` returns `ErrNotExist` for a directory, so the SPA fallback of the static route keeps its behavior.
- The precondition logic is built from the real versitygw evaluators rather than reimplemented. Only the two gaps the
  ADR names are handled locally: `*` on writes, and the date skips.
- The error ordering on Complete is right. Assembly errors and the maxUpload bound come before the 412. The 412, 404 and
  501 answers leave the upload open, and the test proves a retry on the same upload. `putPart` already bounds the total
  at `maxUpload`, so the EntityTooLarge branch at Complete cannot be reached before the 412.
- The tests count what matters: Get calls for the no-digest case, and range calls against Get calls for the ranged
  conditional read. They also assert the raw ListObjectsV2 XML has no `<ETag>`, rather than trusting the SDK's nil.
- Metadata is safe from aliasing: versitygw's `GetUserMetaData` copies header bytes (`string(value)`), and the multipart
  store clones the strings because the upload outlives the request.

## Recommendation

Pass. The ADR is ready for `Reviewing → Implemented` in the wave's docs PR, together with the F47 segment, the ADR-0007
back-link and the board card move to Done. The Minor is optional.

## Ledger row

```json
{
  "adr": "0159",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 9,
  "dod_total": 9,
  "report": "docs/reviews/adr-0159-implementation-claude-opus-5-5.md",
  "notes": "pass; 6/6 checklist + 3/3 DoD (just ci deferred to the PR gate, not counted); build/vet/lint darwin+linux, race tests green; 3/3 overlay mutants killed; Minor(model): writeConditions vs writePreconditions naming"
}
```
