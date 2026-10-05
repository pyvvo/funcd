# ADR-0159: The blob content digest and object metadata — one ETag on every S3 path, and conditional requests

- **Status**: Proposed
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged twice)
- **Deciders**: green-0-rabbit
- **Tags**: blob, s3, etag, conditional-requests, port
- **Realizes**: [FEAT-0003/F47](../feat/0003-feat-data-platform.md) (S3-protocol frontend on the blob substrate)
- **Supersedes in part**: [ADR-0007](0007-blob-storage-layer-port.md), which keeps `Implemented` and gets a
  "Superseded in part by ADR-0159" back-link at acceptance. The lines this ADR replaces:
  Contracts `Bucket` (lines 149-157) gains `Attributes`, and its `Put` (line 151) takes `PutOptions`; `Attributes`
  (lines 159-164) gains `MD5`, `ContentType` and `Metadata`; Scope *In* (line 53) and Decision §1 (lines 105-106)
  gain the `Attributes` verb; §3 (lines 116-118) and the Review checklist (line 229) also map `InvalidArgument`
  and an unparsable `ContentType` to `fault.Invalid`; §4 (lines 123-124): `List` also returns `MD5`.
- **Depends on**: PR #600 (merged, c4dab54): `datePreconditions` (`backend.go:661`) and `createOnly` (`:675`).
- **Relates to**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (`RangeReader` stands) ·
  [ADR-0119](0119-object-store-eventsource.md) / [ADR-0120](0120-static-asset-serving-route.md) (digest follow-ups)
  · [ADR-0136](0136-roles-and-role-assignments.md) (several writers per prefix) · [ADR-0148](0148-size-caps-answer-413.md)
  (also edits `cappedBucket.Put`, `mapBlobErr` and `TestIssue109`'s `view.Put` in `pkg/funcd/s3gateway_internal_test.go`;
  the second to land rebases, keeping `PutOptions{}` and the `PayloadTooLarge` assertion) ·
  [ADR-0157](0157-blob-event-seen-list.md) (Proposed; it moves `versionOf`, so the plan cites the `versionOf` comment by name;
  this `List` `MD5` is one input to its same-Size rewrite risk's exit, closed only with ADR-0119's `data.etag` follow-up and the `s3://`-digest ADR)

## Context & Need

The S3 gateway (ADR-0080) gets ETags and conditional requests wrong (#111). On main 5dbb7fb: GET/HEAD do not
evaluate `If-Match`/`If-None-Match` (they only make a date condition skip); PUT and CompleteMultipartUpload evaluate
only `If-None-Match: *` (`createOnly`), so a stale `If-Match` PUT overwrites; HEAD and a ranged GET send no ETag,
LIST sends `ETag: ""` (`internal/blob/s3gateway/backend.go:165-279`, `:300`); only PUT and a full GET hash the bytes
in hand (`etag()`, `multipart.go:239-242`); PUT drops `Content-Type` and `x-amz-meta-*`. The cause is the port:
`blob.Attributes` is `{Key, Size, ModTime}` (`internal/blob/blob.go:27-31`) and the gocloud driver drops `List`'s MD5
(`internal/blob/gocloud/gocloud.go:149`), so a strong ETag needs a full read, which a ranged Parquet read (ADR-0080)
cannot afford. PR #600 (Refs #111) evaluates the date headers (`datePreconditions`) and a write's `If-None-Match: *`
(`createOnly`); this ADR adds only the ETag conditions.

## Scenarios

- `scenario: etag-on-every-read` — Given an object PUT through the gateway whose response carried ETag `E`, When
  a client HEADs it, GETs `bytes=0-3` of it and lists its prefix, Then each response carries `E`, on the memory and
  the file substrate.
- `scenario: stale-if-match-put-rejected` — Given an object with ETag `E1` that replaced `E0`, When a PUT with
  `If-Match: E0` arrives, Then it fails with 412 and a GET still returns the `E1` bytes; with `If-Match: E1` it
  succeeds. A CompleteMultipartUpload with `If-Match: E0` fails the same way, and its upload stays open for a retry.
- `scenario: if-none-match-not-modified` — Given an object with ETag `E`, When a client GETs or HEADs it with
  `If-None-Match: E`, Then the answer is 304 with no body; with another ETag, 200 and the object.
- `scenario: content-type-and-metadata-roundtrip` — Given a PUT with `Content-Type: application/json` and
  `x-amz-meta-owner: etl`, When a client HEADs and GETs the object, Then both answers carry both; the same holds for
  an object written by a multipart upload.
- `scenario: no-digest-no-etag` — Given an object in the file substrate without gocloud's sidecar, When a client
  HEADs, GETs and lists it, Then no response carries an ETag; a GET or HEAD with `If-Match: "<any>"`, and a PUT
  with `If-Match: "<any>"`, fail with 412 and the object is unchanged; a GET or HEAD with `If-None-Match: "<any>"`
  gets 200 (a HEAD with no body); and no whole-object read was made to compute an ETag.
- `scenario: conditional-ranged-read-stays-ranged` — Given a 1 MiB object with ETag `E` on a driver with
  `RangeReader`, When a client GETs `bytes=0-1023` with `If-Match: E`, Then it gets 206 with those 1024 bytes and
  ETag `E`, served by one range read and no whole-object read.

## Scope

In: the port (`Attributes.MD5`/`ContentType`/`Metadata`, `Bucket.Attributes`, `PutOptions`); the gocloud driver;
the `Prefixed`/`Capped` views; the ETag on HEAD, GET, ranged GET and LIST; ETag preconditions on GET, HEAD, PUT and
CompleteMultipartUpload; `Content-Type` and user metadata on PUT, CreateMultipartUpload → CompleteMultipartUpload,
GET and HEAD. Out: date preconditions and a write's `If-None-Match: *` (PR #600); an atomic compare-and-write;
`DeleteObject` preconditions; S3's multipart ETag form `"<md5>-<n>"` (one MD5 of the assembled object); the digest
in ADR-0119/0120; metadata in `context.blob` and Site; other gocloud attributes (`Cache-Control`, …).

## Constraints & Decision drivers

ADR-0080's ranged read through `blob.RangeReader`; RFC 9110 strong `If-Match` (§13.1.1), 304/412 and §13.2.2
precedence; one ETag form, the quoted lowercase hex MD5 (`etag()`; `TestIssue380_ETagsAreQuoted`); ADR-0007's port
rules (no driver import, `api/fault`, ctx-first, no `any`; `SignOptions` as options precedent). The gateway
substrate is memblob or fileblob (`cmd/funcd/main.go:626`, `:643`); s3blob is only the KV backup target (`:496`).

## Alternatives considered

Chosen: content MD5 on `blob.Attributes`, metadata through `Bucket.Attributes` and `PutOptions`. Rejected (pro / con):
- full read on a conditional ranged GET/HEAD: no port change / breaks ADR-0080's `RangeReader`; LIST has no ETag;
- weak `(Size, ModTime)` validator: free from today's port / does not equal PUT's MD5 ETag; S3 `If-Match` is strong;
- a gateway side table of PUT MD5s: no port change / misses `context.blob`, Site and funclog writes; a second truth;
- a second write method: no caller changes / `Capped` embeds `Bucket`, so it is promoted unchecked past
  `maxObjectBytes`;
- functional options on `Put`: no caller changes / ADR-0002 forbids `Option` types in internal packages;
- an optional capability like `RangeReader`: `Bucket` unchanged / multiplies each view's forwarding types;
- `Get` returning attributes: one call per GET / breaks every caller; HEAD still needs a body-free read;
- all four headers through `EvaluatePreconditions`: one evaluator / fails `If-Unmodified-Since` equal to
  `Last-Modified` and compares sub-second `ModTime` with a whole-second header (`common.go:664-667`).

## Decision

1. **The port.** `Bucket` gains `Attributes(ctx, key)`, a body-free read; `Attributes` gains `MD5`, `ContentType`,
   `Metadata`. `Put` takes `PutOptions`, whose zero value keeps today's behavior. `Get` is unchanged.
2. **Driver fill rule** (gocloud v0.46.0):

   | Backend | Use | `MD5` (List and Attributes) | `ContentType`, `Metadata` |
   |---|---|---|---|
   | memblob `mem://` | memory substrate, tests | MD5 of the written bytes (`memblob.go:363`) | as Put |
   | fileblob `file://` | durable substrate | from the `.attrs` sidecar (`fileblob.go:838`), per entry on `List`; nil without one | from the sidecar; none: `application/octet-stream` (`attrs.go:58-66`) |
   | s3blob `s3://` | KV backup only | nil: `eTagToMD5` (`s3blob.go:679-698`) decodes an SSE-KMS/SSE-C ETag that is not the MD5 | S3 object metadata |

   The driver tells `s3://` from the URL at `Open`. On `file://`, `List` ignores `ListObject.MD5` and fills each
   entry's `MD5` from `k.b.Attributes(ctx, obj.Key)`, nil when that read fails (as fileblob does), so `List` fails no more often than
   today. `Put` stays on `WriteAll` (memblob's `Upload` never feeds its hash, `memblob.go:350`) with
   `WriterOptions`; an empty `ContentType` lets gocloud detect one. A non-empty one is checked with
   `mime.ParseMediaType` first (gocloud returns that error unwrapped, `blob.go:1211-1215`); it and gocloud's
   metadata `InvalidArgument` (`blob.go:1162-1180`) map to `fault.Invalid`. gocloud lowercases metadata keys and
   re-formats the content type (`blob.go:1216`).
3. **Views.** `Prefixed` forwards `Attributes` with the caller's key; `Capped` checks size and forwards options.
   `blob.Stat` reads `Bucket.Attributes` instead of a prefix `List`; `fault.NotFound`/`fault.Invalid` → `found=false`.
4. **One ETag.** HEAD, full GET, ranged GET and every LIST entry carry `objectETag(attrs.MD5)`; a ranged GET still
   reads only its range. PUT and CompleteMultipartUpload keep `etag(data)`, equal to the stored digest.
5. **Preconditions**, on PR #600:
   - GET/HEAD: `readPreconditions` after `blob.Stat`, before any body read, in §13.2.2 order: versitygw v1.6.0
     `backend.EvaluatePreconditions` (`common.go:648`) with only `IfMatch`, then `datePreconditions` (given the full
     `PreConditions`: it skips `If-Unmodified-Since` under `If-Match` and `If-Modified-Since` under
     `If-None-Match`), then `EvaluatePreconditions` with only `IfNoneMatch`. **Decided**: `If-Match: *` on GET/HEAD
     with no digest is an existence test.
   - PUT and CompleteMultipartUpload (**decided**: in scope): `blob.Stat` and `writePreconditions` only when
     `IfMatch` or `IfNoneMatch` is set, after buffering, immediately before `Put` (narrowest window; a failing body
     is read first, bounded by `maxUpload`); replaces #600's `createOnly`; wraps
     `backend.EvaluateObjectPutPreconditions` (`:741`). **Decided**: `writePreconditions` answers an `If-Match: *`
     alone (proceed if the object exists, else 404), since the evaluator compares `*` as a tag (`:764`).
     CompleteMultipartUpload keeps the upload after a 412/404/501 (`multipart.go:194-201`).
   - versitygw strips both headers' quotes and drops an empty one (`s3api/utils/precondition.go:71-73`). Callers
     return a nil output with the error, so no output headers (`object-get.go:498-511`, `object-head.go:148-161`);
     a non-nil one would add `x-amz-delete-marker: true`. A 304 carries no ETag (known divergence, RFC 9110 §15.4.5, S3).
6. **No digest.** `objectETag` returns "" for nil or zero-length `MD5`; the gateway sets a nil `ETag`, so no header
   and no `<ETag>` LIST element (`s3response.go:200` lacks `omitempty`). An `If-Match` other than `*` is 412 on
   GET/HEAD (the evaluator compares it with the empty ETag, `common.go:658`) and on a write. On GET/HEAD an
   `If-None-Match` with entity tags matches nothing, so the read proceeds, 200/206 (RFC 9110 §13.1.2; the evaluator
   answers so, `:661`, `:698-707`). No full read computes a digest.
7. **Content-Type and metadata.** PUT passes `in.ContentType` (versitygw default `binary/octet-stream`,
   `s3api/controllers/base.go:58`) and `in.Metadata` (lowercased, 2 KB limit, `s3api/utils/utils.go:60`, `:85-86`);
   CreateMultipartUpload keeps both, CompleteMultipartUpload passes them to `Put`; GET/HEAD return them. A type
   `mime.ParseMediaType` rejects is 400 `InvalidRequest` (`backend.go:150-151`); others round-trip parsed.
8. **Other consumers.** `context.blob` (`internal/services/blob/blob.go:149`), Site (`internal/site/reconcile.go:264`),
   KV backup (`internal/kvstore/badger/backup.go:257`, `:294`), funclog (`sink.go:197`, `tracesink.go:181`,
   `compact/compact.go:365`) pass `blob.PutOptions{}`. The blob EventSource and static route
   (`internal/edge/static/static.go:97`, `:110`) receive `MD5` unused.

## Temporary workarounds

- **`List` on `file://` reads each entry's attributes** (Decision 2). **Exit criterion**: a gocloud release whose
  fileblob `List` returns the same MD5 as `Attributes`; then `List` keeps `ListObject.MD5`.

## Contracts

```go
// internal/blob/blob.go — replaces Bucket and Attributes; adds PutOptions. SignMethod/SignOptions unchanged.
type Bucket interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, opts PutOptions) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix string) ([]Attributes, error)
	// Attributes reads one object's attributes without its content; fault.NotFound if absent.
	Attributes(ctx context.Context, key string) (Attributes, error)
	SignedURL(ctx context.Context, key string, opts SignOptions) (string, error)
	Close() error
}

// Attributes describes one object. List fills Key, Size, ModTime and MD5; Bucket.Attributes fills all.
type Attributes struct {
	Key     string
	Size    int64
	ModTime time.Time
	// MD5 is the MD5 of the content; empty when the driver has no digest for the object (ADR-0159).
	MD5         []byte
	ContentType string
	Metadata    map[string]string // user metadata, lowercase keys
}

// PutOptions configures a Put. The zero value stores no metadata and lets the driver pick the content type.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

// internal/blob/s3gateway (multipart.go, beside etag).

// objectETag is the quoted lowercase hex ETag of md5, or "" when md5 is nil or empty (caller sets a nil ETag).
func objectETag(md5 []byte) string

// readPreconditions: Decision 5 order; nil, NotModified (304) or PreconditionFailed (412), returned with a nil output.
func readPreconditions(attrs blob.Attributes, c backend.PreConditions) error

// writePreconditions: nil, PreconditionFailed (412), NoSuchKey (404) or NotImplemented (501); "*" alone answered here.
func writePreconditions(attrs blob.Attributes, found bool, ifMatch, ifNoneMatch *string) error
```

| Request | Header | Object | Answer |
|---|---|---|---|
| GET, HEAD | `If-Match: <etag>` | ETag differs, or no digest | 412 |
| GET, HEAD | `If-Match: *` | exists (digest or not) | 200/206 |
| GET, HEAD | `If-None-Match: <etag>` | ETag equal | 304 |
| GET, HEAD | `If-None-Match: <etag>` | no digest | 200/206 (matches nothing) |
| GET, HEAD | `If-None-Match: *` | exists | 304 |
| PUT, CompleteMultipartUpload | `If-Match: <etag>` | ETag differs, or no digest | 412, object unchanged |
| PUT, CompleteMultipartUpload | `If-Match: *` | exists | the write proceeds |
| PUT, CompleteMultipartUpload | `If-Match` | absent | 404 `NoSuchKey` |
| PUT, CompleteMultipartUpload | `If-None-Match: <etag>` | any | 501 `NotImplemented` |
| PUT, CompleteMultipartUpload | `If-Match` and `If-None-Match: *` | any | 501 `NotImplemented` (`common.go:750-751`) |

A write's `If-None-Match: *` alone keeps #600's answers, except that a request error found while buffering now
precedes the 412: `EntityTooLarge` on a PUT body over `maxUpload`; `NoSuchUpload`, `MalformedXML`,
`InvalidPartOrder` or `InvalidPart` on Complete (`maxObjectBytes` stays after the 412). No new module.

| Consumes | Exposes |
|---|---|
| gocloud.dev v0.46.0 `Bucket.Attributes`, `ListObject.MD5`, `WriterOptions` | port: `Bucket.Attributes`, `Attributes.MD5`/`ContentType`/`Metadata`, `PutOptions` |
| versitygw v1.6.0 `EvaluatePreconditions`, `EvaluateObjectPutPreconditions`, `s3err.GetPreconditionFailedErr` | gateway: `ETag` on HEAD, GET, ranged GET and LIST; `Content-Type` and `x-amz-meta-*` on GET/HEAD |
| PR #600 `datePreconditions` | gateway: 304/412/404/501 per the table above |

## Implementation plan

1. `internal/blob/blob.go`: the port; `stat.go`: `Stat` over `Attributes`; `prefixed.go`, `capped.go`: forward.
   Reword the no-digest / no-per-key-read comments: `stat.go:5-6`, the `versionOf` doc comment in
   `internal/eventing/blobwatch.go` (:218-219 at 1193be6), `internal/eventing/cloudevent.go:60`.
2. `internal/blob/gocloud/gocloud.go`: `Put` per Decision 2; `Attributes` via `k.b.Attributes` after `checkKey`;
   `List` keeps `obj.MD5` (line 149) except `file://` and `s3://`; `mapErr`: `gcerrors.InvalidArgument` →
   `fault.Invalid`.
3. From main after PR #600, `internal/blob/s3gateway/backend.go`, `multipart.go`: Decisions 4-7, replacing #600's
   `datePreconditions` and `createOnly` calls (`createOnly` goes); reword #600's comments: `backend.go:161-164`,
   `:251-252`; the PutObject (`:446-448`) and CompleteMultipartUpload (`multipart.go:178-180`) docs name
   `writePreconditions` for both headers; the `datePreconditions` doc (`:656-660`) cites §13.2.2 precedence.
   `upload` keeps `ContentType`/`Metadata`.
4. Callers pass `blob.PutOptions{}`: Decision 8, `blobcontract/contract.go`, every test caller (the `e2e`-tagged
   `pkg/funcd` tests and `cmd/funcdctl/dev_phase2_test.go:189`, `:515` included). Port fakes: (a) those
   embedding `blob.Bucket` get `Attributes` by promotion, and their `Put` overrides take and forward `PutOptions`:
   `recorder` and `tooLarge` (`internal/site/reconcile_test.go`; only `recorder` if ADR-0148 lands first),
   `gatedBucket` (`internal/funclog/shutdown_test.go`), `ctxBucket` (`internal/blob/s3gateway/scenarios_test.go`),
   `closeOrderBucket` (`pkg/funcd/logroutes_internal_test.go`); (b) full implementations gain `Attributes` and the
   new `Put`: `blobMapBucket` (`internal/workernode/local/blob_test.go`), `mapBucket`
   (`internal/services/blob/blob_test.go`), `fakeBucket` (`internal/kvstore/badger/backup_test.go`), and
   `noRangeBucket` (`scenarios_test.go:272-290`), forwarding `Attributes` to `inner` for `TestIssue425`'s `fallback`
   case; `pinnedModTime` (`internal/edge/static/static_test.go:176-189`) overrides `Attributes`, so
   `TestIssue161_SameSecondRedeployIsNotStale304` still pins `ModTime`.
5. Tests:
   - `blobcontract.RunContract` (memory, file): `attributes-digest` (skipped for a driver with no digest, e.g.
     `s3://`), `put-options-roundtrip`, `attributes-not-found`.
   - `gocloud_test.go`: digests on memory and file; a sidecar-less file has `MD5 == nil` and
     `application/octet-stream`; an unparsable `ContentType` is `fault.Invalid`; `file://` `List` carries the same
     MD5 as `Attributes` for every entry. `internal/blob`: `Prefixed.Attributes` returns the caller's key.
     `multipart_unit_test.go`: `objectETag` "" for nil and empty.
   - `internal/blob/s3gateway/scenarios_test.go`: `TestScenarioETagOnEveryRead`; `TestScenarioStaleIfMatchPutRejected`
     (plus every write row of the table, and a stale multipart `If-Match` 412 then the same upload completes);
     `TestScenarioIfNoneMatchNotModified`; `TestScenarioContentTypeAndMetadataRoundTrip` (plus a rejected type →
     400); `TestScenarioNoDigestNoETag` on file wrapped in `getCountingBucket` (`scenarios_test.go:311-319`): the
     `If-Match` 412s, bytes unchanged, the `If-None-Match` 200s, Get count equals the number of 200 GETs (one body read
     each; HEADs and the 412s make none), no `<ETag>` in the raw ListObjectsV2 XML;
     `TestScenarioConditionalRangedReadStaysRanged` (a counting view that forwards `RangeReader`).
   - #600's `TestIssue111_DatePreconditionsAndCreateOnlyPut` (plus: stale `If-Match` with `If-Unmodified-Since` =
     `Last-Modified` is 412) and `TestIssue111_CreateOnlyMultipartComplete` pass;
     `TestIssue425_RangedGetKeepsObjectETag` now requires the object's ETag.
6. Commands: `scripts/agent/d go build ./...`; `scripts/agent/d go vet ./...`, `-tags e2e ./pkg/funcd/...`,
   `-tags dev ./cmd/funcdctl/`; `scripts/agent/d go test -race ./internal/blob/... ./internal/edge/static/...
   ./internal/services/blob/...`; `scripts/agent/d go tool golangci-lint run ./...`; then `just ci`.
7. F47's row links `(+ [ADR-0159](../adr/0159-blob-content-digest-and-metadata.md) — content digest)` from Draft,
   status `S3 frontend: implemented · content digest: adr` (precedent F83); each status move advances that segment.
8. Done: each scenario has one named, passing test; contract cases pass on memory and file; `just ci` green; no
   `go.mod` change.

## Review checklist

- [ ] Port per Contracts, no driver import; views forward; `Capped` still refuses oversize.
- [ ] `s3://` leaves `MD5` nil; `file://` `List` fills each entry's `MD5` from `k.b.Attributes`, nil on a failed read.
- [ ] `Put` stays on `WriteAll`; a non-empty `ContentType` is checked with `mime.ParseMediaType` first.
- [ ] `Content-Type` and `x-amz-meta-*` round-trip through PUT and multipart.
- [ ] Every read path carries `objectETag` or a nil `ETag`; ranged GET makes no whole-object `Get`; no full read
      computes a digest; preconditions per Decision 5 with a nil output on error; on an object without a digest an
      `If-Match` other than `*` is 412 and an entity-tag `If-None-Match` on GET/HEAD proceeds (Decision 6).
- [ ] #600's tests pass unchanged; no existing test weakened.

## Consequences

- Positive: one ETag on every path, so DuckDB's per-read ETag check holds from the first HEAD; conditional
  requests need no body read; HEAD and GET look up one key instead of listing a prefix.
- Negative: seven production `Put` call sites and eight test files change; until the workaround's exit, each
  `file://` `List` (every S3 LIST, and every blob EventSource poll of a watched prefix, 15 s by default, on its
  serial poll loop) costs one more stat and sidecar read per entry (after it, gocloud's `List` reads the sidecar,
  `fileblob.go:487`); a PUT whose `Content-Type` or metadata gocloud rejects is 400 where today it succeeds, on
  multipart at CompleteMultipartUpload, leaving the upload open until abort or `multipartIdleExpiry` (one hour).
- Risks accepted: check and `Put` are two calls, so racing writers with one `If-Match` can both pass (ADR-0136);
  an overwrite between `Attributes` and the read serves new bytes under the old ETag. fileblob writes the sidecar
  before renaming the data (`fileblob.go:841`, `:854`): (a) a read in that window gets the new ETag with old bytes;
  (b) a crash leaves the new MD5 over old bytes; (c) a failed rename removes the sidecar (`:855`), losing ETag and
  `Content-Type`; (d) the truncated sidecar (`attrs.go:43`) fails reads — GET already does (`:398`, `:627-628`),
  now HEAD and conditional writes too, and a `List` entry has no `MD5`. memblob writes under its lock (`:383`).

## Open questions

| Question | Where it gets answered |
|---|---|
| An atomic conditional write: a per-key gateway lock (no port change) or a conditional `Put` (gocloud offers only `IfNotExist`) | a follow-up ADR, when the first S3 client commits with `If-Match` or `If-None-Match: *` |
| ADR-0119's `data.etag` and ADR-0120's strong ETag from `Attributes.MD5` | each one's own follow-up ADR |
| A digest on `s3://` (the ETag is the MD5 only for single-part, non-SSE-KMS/C uploads) | the ADR that makes `s3://` a gateway substrate |

## References

- Issue [#111](https://github.com/pyvvo/funcd/issues/111); PR #600, the base (parked 90c3490 on `fix/i111` is not:
  its ranged GET reads the whole object). RFC 9110 §8.8.3, §13.1–13.2.2, §15.4.5, §15.5.13; AWS S3 conditional
  requests and writes; gocloud.dev v0.46.0, google/go-cloud#3794 (`aa1fa83`); versitygw v1.6.0
  `backend/common.go:632-764`, `s3response.go:200`, `s3api/utils`, `s3api/controllers`.
