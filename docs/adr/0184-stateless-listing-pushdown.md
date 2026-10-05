# ADR-0184: Stateless listing push-down — the S3 gateway seeks storage from the marker instead of re-listing

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: blob, s3, listing, port, performance
- **Realizes**: [FEAT-0003/F47](../feat/0003-feat-data-platform.md) (S3-protocol frontend on the blob substrate)
- **Supersedes in part**:
  - [ADR-0159](0159-blob-content-digest-and-metadata.md) (Implemented) Contracts `Bucket` (lines 146-157, the current
    port, which replaced ADR-0007's): it gains `ListAfter`, a ranged, limited listing with its own contract scenarios,
    beside `List` (precedent: ADR-0159's header line 8). Temporary workarounds (lines 140-141): `file://` listing no
    longer goes through fileblob's `List`, so the exit criterion "a gocloud release whose fileblob `List` returns the
    same MD5" no longer applies; `MD5` stays read from `Attributes`, now only for returned keys.
  - [ADR-0007](0007-blob-storage-layer-port.md) (Implemented) Decision §4 "List + signing" (lines 123-126): `List` on
    `file://` no longer uses the gocloud iterator, and listing gains `ListAfter`.
  - Each superseded ADR gets its "Superseded in part by ADR-0184" back-link at acceptance, not before.
- **Relates to**: refines [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (Implemented) `listobjects-glob`
  (lines 53-54): listing pushes the marker down, the gateway keeps no listing state, the token stays the last key ·
  refines ADR-0159 Decision 2 (lines 95, 98-100) ·
  [ADR-0119](0119-object-store-eventsource.md) and [ADR-0120](0120-static-asset-serving-route.md) (their `List`
  calls keep the contract and gain the lost-key fix) · ADR-0188 (the multipart store, outside the listing
  constraint) · unchanged: ADR-0086, ADR-0148.
- **Issue**: [#715](https://github.com/pyvvo/funcd/issues/715) (`needs-adr`); also fixes [#768](https://github.com/pyvvo/funcd/issues/768) (`file://` `List` drops keys)

## Context & Need

The S3 gateway serves `ListObjectsV2`/`ListObjects` to DuckDB globs, `aws s3 ls` and rclone. Code lines below are at
origin/main 6b06320c. Every page calls `b.listing` (`internal/blob/s3gateway/backend.go:393`, `:421`), which lists the
whole bound prefix (`:285`); `paginate` then drops every key at or before the marker in memory (`:332`). The port cannot
do better: `Bucket.List(ctx, prefix)` has no marker and no limit (`internal/blob/blob.go:21`). A full listing of N keys
at the default 1000 per page reads about N·N/1000 entries. Reproduced (issue probe, refuter confirmed): 10,000 keys
cost 100,000 entry reads, 5.6-6.8 s on `file://`, 0.43 s on `mem://`; 100,000 keys extrapolate to about ten minutes.

`file://` adds a second defect. gocloud fileblob (v0.46.0) re-walks from the prefix directory on every 1000-entry page
(`fileblob.go:406`, `:441`), reads each sidecar before its page-token skip, and pages by key while it walks in file-name
order, so it **loses keys**: `t/events/x0000..x0999` plus `t/events.json` lists 1000 keys without `t/events.json`
(reproduced at 78219297). Every `List` caller is affected: the gateway, `internal/funclog/compact/compact.go:161`,
`internal/funclog/logread/logread.go:101`, `:117`, `internal/site/reconcile.go:351` and
`internal/services/blob/blob.go:168` (which `internal/eventing/blobwatch.go:243` reaches). funcd's own `listMD5`
(`internal/blob/gocloud/gocloud.go:200-213`) then reads every sidecar a second time.

Purpose: a flat page makes storage return about as many entries as it holds; a full listing, with or without a
delimiter, makes storage return each key about once (linear in N); listing holds no state between requests; and
every listing returns every key.

## Scenarios

- **scenario: paged-listing-linear** (the #715 reproduction) — *Given* 10,000 keys `gold/p%06d.parquet` on `file://`
  and on `mem://`, behind a bucket wrapper that counts the entries storage returns, *When* `default/analytics` pages
  `ListObjectsV2` (Bucket `lakehouse`, Prefix `gold/`, no `MaxKeys`) until `IsTruncated` is false, *Then* it receives
  each of the 10,000 keys once, in key order, and storage returned at most 2 × 10,000 entries (today 100,000).
- **scenario: continuation-is-stateless** — *Given* a truncated page whose `NextContinuationToken` is `T`, *When* the
  gateway is closed and a new one started on the same substrate, and the client sends `T`, *Then* it gets the same
  next page; `T` equals the last key or common prefix of the page, and `StartAfter: T` (V2) or `Marker: T` (V1)
  returns the same page.
- **scenario: rollup-skips-subtree** — *Given* 5,000 keys `gold/a/k%04d` and the key `gold/b.parquet` on `mem://` and
  `file://` behind the counting wrapper, *When* a client lists Prefix `gold/` with Delimiter `/`, *Then* one page holds
  the common prefix `gold/a/` and the key `gold/b.parquet`, and storage returned at most 2,000 entries.
- **scenario: delimiter-pages-many-partitions** — *Given* 3,000 partitions `gold/d=%04d/` of 10 keys each on `mem://`
  and `file://` behind the counting wrapper, *When* a client pages Prefix `gold/`, Delimiter `/` (no `MaxKeys`) until
  `IsTruncated` is false, *Then* it receives each of the 3,000 common prefixes once, in order; each token is a common
  prefix `T`; the first storage call of the page after `T` starts after `T+U+10FFFF`; storage returned at most
  2 × 30,000 entries, in at most 50 `ListAfter` calls per page (the remaining-slots rule makes 50; Risks accepted).
- **scenario: maxkeys-zero** — *Given* keys under `gold/`, *When* a client lists Prefix `gold/` with `MaxKeys: 0`,
  *Then* it gets `200` and the same empty, untruncated page as today; storage gets no call (today it lists).
- **scenario: pages-are-not-a-snapshot** — *Given* page 1 of `gold/` was returned, *When* a key sorting after its token
  is put and a key after the token is deleted, *Then* page 2 shows the new key and not the deleted one.
- **scenario: forged-token-bounded** — *Given* a principal bound to `gold` only, *When* it lists Prefix `gold/` with
  `ContinuationToken: a` (before the prefix), *Then* it gets the first page; with `zzz` (after it), an empty page with
  `IsTruncated` false; with Prefix `silver/` and any token, `403` and no storage call.
- **scenario: file-list-keeps-every-key** — *Given* `t/events/x0000..x0999` and `t/events.json` on `file://`, *When*
  `List("t/")`, *Then* it returns all 1001 keys, sorted, `t/events.json` included.
- **scenario: list-after-contract** — *Given* the memory or file driver, *When* `ListAfter(prefix, after, limit)`,
  *Then* it returns the keys under `prefix` strictly after `after`, sorted, at most `limit`, with `more` true exactly
  when a further key exists under `prefix`. `s3://` meets it in escaped-key order (Decision 4); no unit run covers it.

## Scope

**In**: the port method `ListAfter` and its forwarding by the `Prefixed`/`Capped` views; the gocloud driver's
`ListAfter` on `mem://`, `file://` and `s3://`; a funcd-owned key-ordered walk that backs both `List` and `ListAfter`
on `file://`; the gateway's V1/V2 listing path.

**Out**: other `List` callers (they keep `List`, and gain the `file://` walk); S3 `ListObjectVersions` and multipart
listings; snapshot (point-in-time) listings; any listing cache.

## Constraints & Decision drivers

- Listing keeps no state between requests or callers (decider: "I prefer not to share anything by default"); the
  multipart store (ADR-0080, ADR-0188) is outside this constraint.
- The wire format is unchanged: the token is the last emitted key or common prefix; clients (DuckDB httpfs, config
  locked by ADR-0086) need no change.
- Authorization stays per page on the request Prefix's leading segment (`backend.go:280-284`); a token grants nothing.
- One `s3://` LIST per storage call; versitygw clamps `MaxKeys` to 1000 before the backend sees it.
- No new module: `gocloud.dev` v0.46.0, `github.com/aws/aws-sdk-go-v2/service/s3` and `pgregory.net/rapid` are in `go.mod`.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| **Stateless push-down** (chosen) | No state, no TTL, no eviction; fixes `file://`'s lost keys too | A port method; a funcd-owned `file://` walk | **Chosen** |
| Gateway cursor cache (serve continuation pages from page 1's listing) | No port change; one `List` per listing | Shared state between requests and callers: ownership checks, a byte budget, eviction fairness, a staleness window that hides new keys and shows deleted ones; `file://` still loses keys | Rejected by the decider |
| Optional capability (`RangeReader`-style) | Additive | Each view must type-assert and forward it by hand (`capped.go:19-21`, `prefixed.go:15-17`); every driver can implement it anyway | Rejected: a method on `Bucket` is forwarded by embedding |
| Ask storage for `limit+1` to detect more | No `more` result | gocloud's `ListPage` loops to fill the page (`blob.go:900-925`), so S3 (1000 per call) is called twice per page | Rejected: `more` comes from storage truncation |
| Page `file://` with fileblob's page token | No own walk | Re-walks per page, reads sidecars before the skip, loses keys | Rejected |
| Client-side cache | — | The caller is DuckDB httpfs (native C++); no funcd code on that path; cannot remove the server's re-list | Rejected |
| Document the cost (hive partitioning) | No code | 10k-key glob stays ~6 s and grows quadratically; lost keys stay | Rejected |

## Decision

1. **Port.** `blob.Bucket` gains `ListAfter(ctx, prefix, after, limit)`: the keys under `prefix` sorting strictly after
   `after`, sorted, at most `limit`, and `more`, true exactly when a further key under `prefix` exists. `after` before
   the prefix range lists from its start; after it, returns nothing. `limit < 1` is `fault.Invalid`. `List` stays.
2. **Views.** `Capped` forwards it by embedding (`capped.go:25-28`, `:44-47`). `Prefixed` maps `prefix` and a non-empty
   `after` with `p.k`, strips the view prefix as `List` does (`prefixed.go:44-56`); an empty `after` stays empty.
3. **`mem://`**: `k.b.ListPage(ctx, after-or-FirstPageToken, limit, &ListOptions{Prefix})`; memblob skips keys `<=`
   the token (`memblob.go:141-145`, `:200`), a format the contract suite pins; `more` = a non-nil next token. For
   `after` = `FirstPageToken` (`"first page"`, `blob.go:837`, read as the first page at `:880-881`), `ListAfter`
   pages from the start, drops keys `<= after` and follows the next token until it has `limit` keys or none remain.
4. **`s3://`**: the same `ListPage`, asking for exactly `limit`; `more` = a non-nil next token (S3 truncation).
   `BeforeList` sets `StartAfter` on `*s3.ListObjectsV2Input` when it has no `ContinuationToken` (`ListPage` re-calls
   the driver with its token to fill a short page, `blob.go:900-925`). `StartAfter` is `s3EscapeKey(after)`, funcd's copy
   of s3blob's unexported `escapeKey` (`s3blob.go:718-731`: a rune below 0x20 and a `/` after `..` become
   `__0x<hex>__`; `gocloud.dev/internal/escape` is not importable), so seek and order agree; such a key lists at its
   escaped-form position on `s3://` only. Legacy V1 listing is unreachable (only `Options.UseLegacyList`, set by no URL
   parameter, enables it, `s3blob.go:222-225`; `gocloud.Open` opens by URL, `gocloud.go:43-44`); as a guard, an as-func
   that yields no V2 input makes `BeforeList` return a sentinel error; on it, `ListAfter` calls `List(prefix)`, keeps
   the keys strictly after `after` up to `limit`, and sets `more` when a key remains.
5. **`file://` walk** (funcd-owned, backs `List` and `ListAfter`): start at the directory of `fileWalkPrefix(prefix)`
   up to its last `/` (`gocloud.go:220-227`); per directory, `os.ReadDir`, skip `.attrs` sidecars, decode each name
   with `fileUnescapeKey` (funcd's copy of fileblob's unexported `unescapeKey`, `fileblob.go:352`), sort siblings by
   decoded key with `/` appended to directory names (a file sorts before an equal directory key); descend in that
   order; skip a directory `D` whose `D/` neither has `prefix` as prefix nor is a prefix of `prefix`, or sorts
   entirely at or before `after` (`D/ < after` and `after` lacks the prefix `D/`); stop after `limit` keys plus one
   (that one sets `more`). Only returned keys get `Size`/`ModTime` from `fs.DirEntry.Info` (a file gone by then is
   skipped) and `MD5` from `k.b.Attributes`, nil on failure (ADR-0159 Decision 2). An unreadable entry is skipped,
   as fileblob does. `listMD5` is no longer called on `file://`. funcd runs on Linux/macOS: `/` separator only.
6. **Gateway.** `ListObjectsV2`/`ListObjects` call one new `b.list`: authorize once (as `backend.go:280-284`); a
   page with `MaxKeys <= 0` is empty and untruncated, with no storage call (today it lists, then returns, `:325-327`).
   Otherwise the walk starts at `after = marker` (V2 `max(StartAfter, ContinuationToken)`, V1 `Marker`, as
   `:398`/`:426`); with a Delimiter, a marker under Prefix whose rest holds the delimiter starts it at
   `cp(marker)+string(utf8.MaxRune)` instead, skipping exactly the keys today's `cp <= marker` check drops. Each call
   is `sub.ListAfter(keyPrefix, after, r)`, `r` = `MaxKeys` minus the entries already on the page. Per returned key,
   in order: a key of another leading segment is dropped, and ends the listing when it sorts after `segment+"/"`; the
   delimiter rollup, the `cp <= marker` check, `MaxKeys` and the `maxListXML` cap work as in `paginate` (`:335-354`).
   While the page is unfilled and storage reported `more`, the next call starts after the last key read, or after
   `cp+string(utf8.MaxRune)` when that key lies under the last emitted common prefix `cp`; keys a call returns under
   `cp` are read and dropped, so the seek past `cp` takes effect at the next call. A full page is truncated when an
   entry remained or storage reported `more`; the next marker is the last emitted entry, as today.
7. **Semantics.** Pages are not a snapshot (each reflects storage at its request), as today and as S3 promises. A
   token is only a `StartAfter` any caller may send; listing stays within the authorized prefix.

## Temporary workarounds

None. The flat-directory cost on `file://` is an accepted limit (Consequences), not a stopgap.

## Contracts

```go
// internal/blob/blob.go — Bucket gains (List, Attributes … unchanged):
	// ListAfter returns at most limit objects under prefix whose key sorts strictly after `after`, sorted by key,
	// with the fields List fills; more is true exactly when a further key under prefix exists. limit < 1 is fault.Invalid.
	ListAfter(ctx context.Context, prefix, after string, limit int) (items []Attributes, more bool, err error)

// internal/blob/prefixed.go
func (p *prefixedBucket) ListAfter(ctx context.Context, prefix, after string, limit int) ([]Attributes, bool, error)

// internal/blob/gocloud/gocloud.go
func (k *bucket) ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error)
// fileWalk lists file:// keys under prefix after `after` in key order; limit < 0 means no limit (List).
func (k *bucket) fileWalk(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error)
func s3EscapeKey(key string) string      // s3blob's unexported escapeKey, copied (Decision 4)
func fileUnescapeKey(name string) string // fileblob's unexported unescapeKey, copied (Decision 5)

// internal/blob/s3gateway/backend.go — replaces listing (:277-304) and paginate (:323-365); listPage (:314-319),
// maxListXML (:309), entryLen (:369-379) and pageSize (:382-387) unchanged.
func (b *be) list(ctx context.Context, action authz.Action, bucket, prefix, delimiter, marker string, limit int32) (listPage, error)

// internal/blob/blobcontract/contract.go — RunContract (:18) gains testListAfter.
```

| | Item | Notes |
|---|---|---|
| Consumes | gocloud v0.46.0 `Bucket.ListPage`, `ListOptions.BeforeList`, `FirstPageToken` (`blob.go:837`, `:864`) | mem and s3 |
| Consumes | `*s3.ListObjectsV2Input.StartAfter` (aws-sdk-go-v2 service/s3) | s3 only |
| Consumes | `os.ReadDir`, `fs.DirEntry.Info` | file walk |
| Exposes | `Bucket.ListAfter`; `List` on `file://` backed by the walk | every driver and view |

## Implementation plan

1. **Prove first** (`internal/blob/s3gateway/issue715_test.go`, `internal/blob/gocloud/issue715_test.go`):
   `TestIssue715_PagedListingIsLinear` (mem and file; a wrapper embedding `blob.Bucket` counts entries returned by
   `List`) and `TestIssue715_FileListKeepsEveryKey`. Both fail on current main: 100,000 entries > 20,000, and
   `t/events.json` missing. The wrapper gains its `ListAfter` override in the commit that adds the port method.
2. `internal/blob/blob.go`: `ListAfter` on `Bucket`; `prefixed.go`: `prefixedBucket.ListAfter`; `capped.go`: nothing.
   `go build ./... && go vet ./...` names every other implementer (e.g. `noRangeBucket`,
   `internal/blob/s3gateway/scenarios_test.go:300`); each forwards to its inner bucket.
3. `internal/blob/gocloud/gocloud.go`: `ListAfter` (Decision 3-5), `fileWalk`, `s3EscapeKey`, `fileUnescapeKey`;
   `List` on `file://` calls `fileWalk(ctx, prefix, "", -1)`; `mem`/`s3` `List` unchanged.
4. `internal/blob/s3gateway/backend.go`: `b.list` (Decision 6); `ListObjectsV2` (`:391-416`) and `ListObjects`
   (`:419-441`) call it; delete `listing` and `paginate`.
5. **Tests** (each written to pass):
   - `blobcontract.testListAfter` (memory, file) — strictly after, sorted, at most `limit`, prefix-bounded, `after`
     before/after the range, exact `more`, `limit 0` → `fault.Invalid`, memblob's token format, `after = "first page"`.
   - `TestFileWalk_MatchesSortedReference` (rapid) — random key sets over `-`, `.`, `/`, `//`, a trailing `/` and
     control runes; `fileWalk` equals a sorted, prefix-filtered, after-cut reference for random `prefix`/`after`/`limit`.
   - `TestPrefixed_ListAfter` — view prefix mapping and an empty `after`.
   - One acceptance test per scenario, named `TestScenario` + the scenario name in CamelCase, as the
     gateway package does (e.g. `TestScenarioPagedListingLinear`); `TestScenarioListAfterContract` runs the contract suite.
   - Existing gateway listing tests (incl. `TestListObjects_PageStaysUnderXMLBodyCap`) pass unchanged.
6. Run `scripts/agent/d go test -race ./internal/blob/... ./internal/funclog/... ./internal/services/blob/...
   ./internal/site/...`, and `go vet` and `golangci-lint` on `./internal/blob/...`; the repo-wide checks run once per
   PR in `scripts/agent/gate.sh`.

**Done when** both #715 tests pass and fail with the fix reverted, every scenario test passes, the step-6 checks are
green, and the PR's single `scripts/agent/gate.sh` run passes.

## Review checklist

- [ ] `Bucket.ListAfter` has the Contracts signature; `Capped` adds no method; `Prefixed` maps an empty `after` to empty.
- [ ] The gateway holds no listing state: no map, cache or field written by `b.list`.
- [ ] `b.list` calls `authorize` once per request, before any storage call; `MaxKeys <= 0` makes none.
- [ ] Each `ListAfter` call asks for the page's remaining slots; a common-prefix marker seeks past `cp+U+10FFFF`.
- [ ] `s3://` `ListAfter` asks `ListPage` for exactly `limit`, sets `StartAfter` in escaped form only without a
  `ContinuationToken`, and on the sentinel error falls back to `List` cut after `after` (Decision 4).
- [ ] `file://` `List` and `ListAfter` both use `fileWalk`; `Attributes` is called only for returned keys.
- [ ] Tokens on the wire equal the last emitted key or common prefix.
- [ ] `TestIssue715_*` fail with the fix reverted.
- [ ] No absolute path or username in changed files.

## Consequences

- **Positive**: measured at the blob layer (macOS, 10k keys, 11 pages): `file://` flat 6.60 s → 0.45 s, nested 6.26 s
  → 0.29 s; `mem://` 0.245 s → 0.024 s. `file://` `List` returns every key to all callers, reading each sidecar once.
- **Negative**: one port method that every `Bucket` implementer and test fake must carry. funcd-owned copies of
  fileblob's and s3blob's key escapes (Decisions 4-5) must follow a gocloud upgrade that changes either format.
- **Risks accepted**: a flat directory on `file://` still reads every name per page (`ReadDir` cannot seek): about
  15× cheaper at 10k keys but O(directory) per page; hive-style partitioning is close to linear. A full page whose
  remaining keys are all dropped (another segment, or the last common prefix) is followed by one empty, untruncated
  page. On `s3://`, a key with a control rune or `../` lists at its escaped-form position. **Delimiter cost, per
  storage call** (Decision 6): up to 1000 calls returning about 500,000 entries for a page of 1000 prefixes of 1000+
  keys. On `file://` each call re-reads the start directory's names, so a page over P partitions
  reads O(P × calls) names (about 150,000 at P = 3,000), and a seek past `cp+U+10FFFF` still reads `cp`'s directory
  names (a key there may sort after it); on `mem://` each call sorts the whole bucket (`memblob.go:152-156`). A full
  listing still returns each key at most twice (linear in N).

## Open questions

- `s3://` `ListAfter` has no test run: the S3 integration lane is unbuilt (ADR-0007 line 133 defers it to P-S), and
  `s3://` is not a gateway substrate (the daemon opens `mem://` or `file://`, `cmd/funcd/main.go:751`, `:768`; `s3://`
  only as the KV backup target, `:515`; ADR-0159 line 96). The P-S lane runs `blobcontract.testListAfter` on `s3://`.
- Push the Delimiter down to storage (S3's native `Delimiter`, gocloud `ListOptions.Delimiter`, a walk that does not
  descend), so a delimiter page is one storage call and never reads a rolled-up subtree; it widens `ListAfter` to
  return common prefixes. A follow-up ADR takes it up before a substrate whose storage call is a network request (such
  as `s3://`) serves the gateway, or before the 50-call bound of `TestScenarioDelimiterPagesManyPartitions` is raised.

## References

- Issue [#715](https://github.com/pyvvo/funcd/issues/715) and its reproduction; the refuter's re-run (5.6 s at 10k on `file://`).
- gocloud.dev v0.46.0 `blob/blob.go`, `blob/s3blob/s3blob.go`, `blob/memblob/memblob.go`, `blob/fileblob/fileblob.go`.
- AWS S3 `ListObjectsV2` `StartAfter` and `ContinuationToken` semantics.
