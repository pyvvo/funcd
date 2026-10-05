# ADR-0184 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: docs/adr/0184-stateless-listing-pushdown.md (Accepted 2026-10-05; status stamping is left to the wave's docs PR)
- **Work**: branch `feat/adr-0184-stateless-listing-pushdown`, one commit `865ab2c4` on origin/main, 14 files, +978/-97
- **Verdict**: **pass** — 0 Blocker, 0 Major, 3 Minor (2 model, 1 adr)

## Verification run (in the worktree, via `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on internal/blob/..., services/blob, workernode/local, kvstore/badger | exit 0; Linux `go vet ./internal/blob/...` exit 0 |
| `golangci-lint run` on the same packages | `0 issues.` (darwin); `GOOS=linux` run of the host lint binary: `0 issues.` |
| `go test -race -count=1` internal/blob/..., funclog/..., services/blob/..., site/..., workernode/local/..., kvstore/badger/... | all `ok` (s3gateway 34.9 s, gocloud 3.4 s, blob 1.5 s) |
| `-race -v` on every Scenario / Issue715 / FileWalk / Prefixed / ListObjects test | all `--- PASS`, none skipped |
| Changed files scanned for dev-machine references | clean |

### Overlay mutants (each must fail a test)

| # | Mutation | Test | Result |
|---|---|---|---|
| m1 | `internal/blob/s3gateway/backend.go` replaced by `git show origin/main:` (the revert of the gateway fix) | `TestIssue715_PagedListingIsLinear` | **FAIL** (mem and file): `storage returned 100000 entries for 10000 keys` — the #715 reason |
| m2 | `gocloud.go` `List`: `if k.file {` → `if false {` (file:// back on fileblob's iterator) | `TestIssue715_FileListKeepsEveryKey`, `TestScenarioFileListKeepsEveryKey` | **FAIL**: `t/events.json` missing — the #768 reason |
| m3 | `backend.go:369` in-page seek past the last common prefix disabled | `TestScenarioRollupSkipsSubtree` | **FAIL** (mem and file): `"5001" is not less than or equal to "2000"` |

3/3 killed. m1 and m2 are the ADR's "fail with the fix reverted" check for both `TestIssue715_*` tests.

## Findings

### Blocker
None.

### Major
None.

### Minor

- **n1 [model]** — The three map-backed test fakes satisfy the widened port by embedding a **nil** `blob.Bucket`
  (`internal/kvstore/badger/backup_test.go:22`, `internal/services/blob/blob_test.go:23`,
  `internal/workernode/local/blob_test.go:23`). Plan step 2 says each implementer forwards to its inner bucket; these
  fakes have none, so the embed compiles, but a later `ListAfter` call on them (for example, once a facade under test
  starts paging) panics with a nil dereference instead of failing at compile time. The comment on each states the
  reason, so this is a fragility, not a defect today. An explicit `ListAfter` that returns `fault.Unsupported` (or one
  that pages the map) would fail loudly and clearly.
- **n2 [model]** — `fileWalk` wraps its walk error as op `blob.List` (`internal/blob/gocloud/gocloud.go:398`) also when
  `ListAfter` calls it, so a cancelled `ListAfter` on file:// reports the wrong operation. Only the context error
  reaches this path (unreadable directories are skipped).
- **n3 [adr]** — The ADR asks for both `TestIssue715_PagedListingIsLinear` and `TestScenarioPagedListingLinear`, and
  for `TestScenarioListAfterContract` beside the existing `TestScenario_DriverConformanceParity`. The implementation
  aliases each pair to one helper (correct), but every pair runs the full body twice: the 10,000-key seed on mem and
  file twice, and the whole contract suite twice. The s3gateway package takes about 35 s under `-race`. This is a
  test-time cost the ADR text causes; a later ADR could let one name cover both.

## Contracts and Decisions vs code

- **Decision 1 / Contracts** — `Bucket.ListAfter(ctx, prefix, after string, limit int) (items []Attributes, more bool, err error)`
  (`internal/blob/blob.go`) matches the Contracts signature; `limit < 1` → `fault.Invalidf` (`gocloud.go:237-240`),
  pinned by the contract case for `limit` 0 and -1.
- **Decision 2** — `capped.go` untouched; `cappedBucket` embeds `Bucket`, so `ListAfter` is forwarded.
  `prefixedBucket.ListAfter` (`prefixed.go:53`) maps `prefix` and a non-empty `after`, keeps an empty `after` empty, and
  strips the view prefix through the shared `strip` helper that `List` now uses too. `TestPrefixed_ListAfter` proves the
  view's root key `""` is listed.
- **Decision 3 (mem)** — `memListAfter` (`gocloud.go:254`) calls `ListPage(token, limit, Prefix)` with `after` as the
  token; memblob skips keys `<= token` and sets `NextPageToken` only when a further matching key exists, so `more` is
  exact. For `after == "first page"` it pages with the iterator and drops keys `<= after`, equivalent to the Decision's
  "follow the next token" and pinned by two contract cases.
- **Decision 4 (s3)** — `s3ListAfter` (`gocloud.go:287`) asks `ListPage` for exactly `limit`; `BeforeList` sets
  `StartAfter = s3EscapeKey(after)` only when `ContinuationToken` is nil; an as-func that yields no
  `*ListObjectsV2Input` returns `errNoV2Input` (`:47`), and `ListAfter` then falls back to `List` cut after `after` with
  `more` set when a key remains. s3blob v0.46.0's as-func accepts `**s3.ListObjectsV2Input` (s3blob.go:501-516), and
  gocloud's `gcerr` wrapper unwraps, so `errors.Is` reaches the sentinel. `s3EscapeKey` matches s3blob's `escapeKey`
  rules (rune < 0x20, `/` after `..`, `__0x<hex>__` format, unchanged string when nothing escapes). No unit test runs
  s3://, as the ADR's Open question records.
- **Decision 5 (file walk)** — `fileWalk` (`:393`) and `fileWalker.walk` (`:434`) start at `fileWalkPrefix(prefix)` cut
  at its last `/`, skip `.attrs` sidecars, decode names with `fileUnescapeKey`, sort siblings by decoded key with `/`
  appended to directories (a file before an equal directory key), prune a directory outside the prefix or wholly at or
  before `after`, and stop at `limit + 1`. Only returned keys get `Info()` and `Attributes` (MD5, nil on failure).
  `listMD5` is deleted. The root directory comes from the new `fileDir` (`:74`), which mirrors fileblob's URL opener
  (path, leading `/` dropped for host `.`, `filepath.Abs`). `checkKey` already rejects keys containing `__0x`, `.`
  segments and a leading `/`, so the decoding is unambiguous. The rapid property test
  `TestFileWalk_MatchesSortedReference` checks the walk against a sorted, prefix-filtered, after-cut reference over
  `a b - . / \x01` keys with random `prefix`, `after` and `limit` (-1, 1, 2, 3, 5).
- **Decision 6 (gateway)** — `b.list` (`backend.go:293`) matches the Contracts signature and replaces `listing` and
  `paginate`: it authorizes once (`:297`) before any storage call, returns an empty untruncated page for `MaxKeys <= 0`
  (`:302`), starts a delimiter marker under its common prefix at `cp + U+10FFFF` (`:308`), asks each `ListAfter` for the
  remaining slots, drops another segment's key and ends the listing past `segment+"/"` (`:321`), keeps the
  `cp <= marker`, `MaxKeys` and `maxListXML` rules, and seeks past the last emitted common prefix between calls
  (`:369`). The next marker stays the last emitted key or common prefix. `listPage`, `maxListXML`, `entryLen` and
  `pageSize` are unchanged.
- **Decision 7** — no field, map or cache is written; `be` is unchanged. `TestScenarioPagesAreNotASnapshot` and
  `TestScenarioForgedTokenBounded` pin the semantics.

## Review checklist (ADR) — 9/9

| Item | Holds | Evidence |
|---|---|---|
| `ListAfter` signature; `Capped` adds no method; `Prefixed` keeps an empty `after` empty | yes | blob.go, capped.go untouched, prefixed.go:53, `TestPrefixed_ListAfter` |
| Gateway holds no listing state | yes | `be` unchanged; `b.list` writes only locals |
| `authorize` once, before storage; `MaxKeys <= 0` makes no call | yes | backend.go:297, :302; `TestScenarioMaxkeysZero`, `TestScenarioForgedTokenBounded` (denied listing: no storage call) |
| Remaining slots per call; common-prefix marker seeks past `cp+U+10FFFF` | yes | backend.go:308, :369; `TestScenarioDelimiterPagesManyPartitions` asserts the first seek; m3 |
| s3 `ListAfter`: exactly `limit`, escaped `StartAfter` only without a token, sentinel fallback | yes (by reading; no s3 run, per the ADR) | gocloud.go:287-326 |
| file `List` and `ListAfter` both use `fileWalk`; `Attributes` only for returned keys | yes | gocloud.go `List` file branch, :393 |
| Tokens equal the last emitted key or common prefix | yes | `TestScenarioContinuationIsStateless`, `TestScenarioDelimiterPagesManyPartitions` |
| `TestIssue715_*` fail with the fix reverted | yes | m1, m2 |
| No absolute path or username in changed files | yes | scan clean |

Definition of done (3 items in reach of this gate): both #715 tests pass and fail when reverted; all nine scenario tests
pass; the step-6 checks (race tests on blob, funclog, services/blob, site; vet and lint on internal/blob, also Linux)
are green. The PR's single `scripts/agent/gate.sh` run is outside this review.

## Scenarios — 9/9 named, un-skipped, passing

`TestScenarioPagedListingLinear` (mem and file, at most 2 × 10,000 entries) · `TestScenarioContinuationIsStateless`
(first gateway closed, a new one on the same file:// directory; V2 token, V2 StartAfter and V1 Marker agree) ·
`TestScenarioRollupSkipsSubtree` (mem and file, at most 2,000 entries) · `TestScenarioDelimiterPagesManyPartitions`
(3,000 prefixes in order, at most 50 calls per page, first seek `T+U+10FFFF`, at most 60,000 entries) ·
`TestScenarioMaxkeysZero` · `TestScenarioPagesAreNotASnapshot` · `TestScenarioForgedTokenBounded` (`a` → first page,
`zzz` → empty and untruncated, `silver/` with four tokens → 403 and no storage call) ·
`TestScenarioFileListKeepsEveryKey` · `TestScenarioListAfterContract` (memory and file). The existing gateway listing
tests, `TestListObjects_PageStaysUnderXMLBodyCap` included, pass unchanged.

## Departures from the ADR text, judged against the preflight brief

- The ADR status was not bumped to `Reviewing` and no feat row moved: the batch defers doc edits to one docs PR per
  wave. Expected; the ADR file is untouched.
- Files outside the ADR's named set: three test fakes (n1) and `scenarios_test.go`'s `noRangeBucket`, which plan step 2
  covers ("every other implementer"). The `noRangeBucket` forwards to its inner bucket as the plan asks. The changes
  stay small and local, so the integrator's rebase stays mechanical.
- `fileDir` and the `dir` field are not named in the Contracts. They are the minimum the walk needs, because fileblob
  does not expose its root.

## Verified correct — keep

- The gateway loop's termination logic: a key of another segment past `segment+"/"` ends the page untruncated (no
  later key can belong to the segment); a full page is truncated only when an entry remained or storage reported
  `more`. A call that returns no entries while reporting `more` (file entries removed between `ReadDir` and `Info`)
  repeats once and then advances, because the removed files are not listed again.
- `more` comes from storage truncation, never from asking for `limit + 1` on s3 (the ADR's rejected alternative).
- The rapid property test for the walk is the right tool for the escape and sort edge cases (`-` and `.` sort before
  `/`; a trailing `/`; `//`; control runes).
- The counting wrapper records every seek, so the delimiter test asserts the first-seek rule directly instead of
  inferring it from entry counts.

## Recommendation

Pass. n1 and n2 are optional cleanups for a later change; n3 goes to a future ADR if test time matters.

```json
{
  "date": "2026-10-05",
  "adr": "0184",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 2,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0184-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (865ab2c4); all 7 Decisions + Contracts hold; build/vet/lint clean also Linux; touched pkgs -race ok; 9/9 scenario tests pass; mutants 3/3 killed (backend.go reverted -> 100000 entries; file List on fileblob iterator -> t/events.json lost; in-page cp seek off -> 5001 > 2000). n1 [model] map-backed test fakes embed a nil blob.Bucket instead of implementing ListAfter; n2 [model] fileWalk labels its error blob.List when ListAfter calls it; n3 [adr] Issue715/Scenario and contract alias pairs run the same heavy bodies twice (~35 s s3gateway under -race)."
}
```
