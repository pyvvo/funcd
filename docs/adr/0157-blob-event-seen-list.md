# ADR-0157: Blob event seen list — a pruned, located record replaces the blob watermark cursor

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: eventing, eventsource, blob, watermark, dedup, kvstore
- **Realizes**: [FEAT-0003/F83](../feat/0003-feat-data-platform.md) (object-storage EventSource kind; ADR-0119's row)
- **Supersedes in part**: [ADR-0119](0119-object-store-eventsource.md):
  - Decision §3 (:182-190) and its restatement in §2 (:179-181): the new-object rule, the cursor
    `{MaxModTime, KeysAtMax}`, and a record without its location. The record key and the `data.version` fingerprint
    (:188-190) stay; the fingerprint is now the value in `Seen`.
  - Contracts `Watermark` (:266-271) and `Cursor` (:273-277); `Register` (:285) gains the source UID; the label
    "delete / kind-change" on `Deregister` (:286) becomes `BucketNotFound`, since those sites now call `Purge`.
  - Constraint *New-object soundness* (:115-117), replaced by the listing assumption below; its last sentence (an
    overwrite re-fires, :117-118) now holds by design.
  - The size claims "one small JSON record" (:85), "the watermark bounds retained state" (:120) and
    "low-volume/bounded — one small record per event, rewritten per poll" (:131-132): the record is now
    O(objects under the prefix) and is saved only when it changes.
  - Decision §5 "never drops it" (:197-199), false under out-of-order visibility (#34); restated in Decision 6.
  - Alternatives: the last sentence of "Timestamp-only high-water mark" (:143) and the rejection of "A pure unbounded
    seen-set" (:144), reversed in a pruned form; the checklist item "the cursor is bounded (`MaxModTime` + tie
    key-set)" (:355-356); the open question "Watermark GC" (:390), answered by pruning.
  - The rest of ADR-0119 stands; it keeps `Implemented` and gets a "Superseded in part by ADR-0157" back-link at
    acceptance.
- **Relates to** (no line changes): ADR-0108, ADR-0109, ADR-0118, ADR-0080, ADR-0019 (unchanged);
  [ADR-0007](0007-blob-storage-layer-port.md) (`List(prefix)` stays the poll surface; no ETag);
  [ADR-0156](0156-sensor-delivery-isolation.md) and [ADR-0159](0159-blob-content-digest-and-metadata.md) (Proposed;
  0156 saves after the enqueue; 0159 adds `MD5` to `List` on mem/file, nil on `s3://`, unused by the watcher)

## Context & Need

ADR-0119 keeps `Cursor{MaxModTime, KeysAtMax}` per named blob event under `_eventing/blobwatch/<ns>/<source>/<event>`
(`internal/eventing/watermark.go:32`); an object fires iff its ModTime is above `MaxModTime`, or equal with an unseen
key (`internal/eventing/blobwatch.go:178-216`). Two defects (reproduced at 171adac, unchanged on main 1193be6):

- **#34 — objects lost.** Concurrent writers (memblob, fileblob) make objects listable out of ModTime order; one
  listed after the cursor passed its ModTime never fires: 16–45 of 480 lost on file, 41–57 on mem, gaps 83 ns–3 ms.
- **#57 — stale state.** The record holds no bucket, prefix or identity: a prefix or bucket edit or a re-create emits
  0 events where a fresh source emits 1, and `Deregister` leaves the record behind.

Purpose: every object under a watched prefix fires at least once in any visibility order; what fires depends on the
event's current bucket, prefix and source UID (except a dropped and re-added event with the same location resumes);
a deleted source leaves no state, also after a crash; a record that cannot be saved is visible on its EventSource.

## Scenarios

- `scenario: out-of-order-visibility-never-loses` — Given Bucket `raw` prefix `drop/` over `mem://` and `file://`,
  When 8 writers put 480 objects while the watcher polls (plus a final poll), Then each fires at least once, and
  exactly once on `mem://` (a partial fileblob listing may re-fire, see Consequences); and when a
  later poll first lists `drop/a` (t1) after `drop/b` (t2 > t1) fired, Then `drop/a` fires.
- `scenario: rewritten-object-fires-again` — When a fired object gets a new ModTime or Size, Then it fires once more
  with the new `data.version`; listed unchanged next, nothing fires.
- `scenario: deleted-object-pruned` — When a fired object is deleted and polled, Then its key leaves the record; written
  again with the same ModTime and Size, it fires.
- `scenario: re-point-back-fills` — When the prefix is edited to `other/` (or `bucket` to a Bucket holding objects),
  Then the existing objects there fire.
- `scenario: re-create-back-fills` — When `drops` is deleted and re-created identically (also as one reconcile), Then
  `drop/a.parquet` fires again.
- `scenario: source-delete-deletes-record` — When a blob source is deleted, Then no key under
  `_eventing/blobwatch/<ns>/<source>/` remains.
- `scenario: kind-change-deletes-record` — When `blob:` becomes `timer:`, Then the records are deleted; back to
  `blob:`, every object fires.
- `scenario: bucket-miss-keeps-record` — When the Bucket is missing at one reconcile and then returns unchanged, Then
  the record is kept and nothing fires again.
- `scenario: dropped-event-readded-resumes` — Given event `e` fired `drop/a`, When `e` is dropped from the spec, a
  poll runs, and `e` is re-added with the same Bucket and prefix, Then the record is still present and nothing fires.
- `scenario: start-sweep-deletes-orphans` — Given records of `drops` (not in the store) and `drops2`, When the watcher
  starts, Then the `drops` keys are gone and the `drops2` keys are kept.
- `scenario: save-failure-visible` — When `Save` fails, Then the EventSource carries `SeenListSaved` False, reason
  `SaveFailed`, naming the event and error; after a successful `Save`, the condition is gone.

ADR-0119's scenarios (`dedup-no-refire`, `restart-no-replay`, and the others) stand, with their tests.

## Scope

In: the record and its encoding, the fire/prune rule, the location check, the purge epoch, `Watermark.Delete`, the
delete policy, restart, the start sweep, the save-failure condition. Out: `Removed`/`Updated` events, a content ETag,
marker-based `List`, splitting an oversized record, migration (no deployment exists).

## Constraints & Decision drivers

- Soundness rests only on what `List` returns, not on visibility order or clocks; fileblob's walk skips an unreadable
  path with no error (`fileblob.go:441-445`).
- No replay on restart (`restart-no-replay`) or after a transient `BucketNotFound` (`eventing.go:144`, requeued after `eventing.bucketRecheckInterval`, ADR-0163, default 15 s).
- The persisted format changes once, for #34 and #57; at-least-once stays (workflows must be idempotent).

## Alternatives considered

| Option | Outcome |
|---|---|
| **Seen list pruned to the current listing, (ModTime, Size) fingerprint per key, location in the record** | **chosen** |
| Lookback window on ModTime | rejected: a timing guess (gaps 83 ns–3 ms measured, S3 untested) |
| Make the substrate monotonic | rejected: fileblob `List` is not a snapshot; an external S3 is not funcd's |
| Bucket and prefix in the key | rejected: orphans every record; a re-point still needs `Delete` |
| Location check held in memory in `Register` | rejected: lost on restart |
| Delete the record on every `Deregister` | rejected: a transient `BucketNotFound` replays the prefix |
| Prune only after two consecutive misses | rejected: a counter per key, still replays; the decider settled pruning to the listing |
| Split a record over several KV values | rejected: rare, and loses one-`Put` atomicity; the failure is made visible (Decision 9) |
| ADR-0170's garbage collector removes orphan records | rejected: it follows owner references, a KV record is not an object; start sweep instead (Decision 8) |
| Store each key as base64 | rejected: `data.key` and gocloud (except `List`) cannot carry a non-UTF-8 key anyway |

## Decision

1. **Record.** Each named blob event keeps one JSON `SeenList` under the unchanged key: its location (Bucket, prefix,
   EventSource UID) and `Seen`, each fired and still-listed key mapped to the `versionOf` fingerprint it fired with
   (`<ModTime UnixNano>-<Size>`). A key that is not valid UTF-8 is skipped (one warn log per key per process), since
   JSON would write it as U+FFFD and it would never match after a `Load`.
2. **Per poll** (`pollOne`, for the watch entry `e` that `poll` snapshotted). The live entry *matches* `e` when it
   exists and its Bucket, prefix, UID and purge epoch (Decision 5) equal `e`'s.
   - `List` `e`'s prefix; on an error, return and leave the record untouched.
   - Under the record lock: if the live entry does not match `e`, skip the event (no fire, no save). Otherwise
     `Load`; if the record's location differs from `e`'s, replace it with an empty `SeenList` carrying `e`'s location,
     so the event starts like a new source.
   - **Fire** each listed object whose key is not in `Seen` or whose version differs. Before each `Publish`, stop
     without saving if the live entry no longer matches `e`; after each successful `Publish`, set `Seen[key]`.
   - **Prune** every key of `Seen` that this listing does not contain.
   - `Save` only if the record changed, under the record lock and only if the live entry still matches `e`. If
     `NewBlobEvent` or a `Publish` fails, prune, save what fired the same way, and return the error.
   - The record lock (new), one mutex on `BlobWatcher` taken before `w.mu`, covers only these checks; no `Publish`
     runs under it. Not holding it from `Load` to `Save` is safe only because one `Run` loop polls each event serially.
3. **The UID** is `ObjectMeta.UID`; `reconcileBlob` passes `es.UID` to `Register` (`eventing.go:150`). It catches a
   delete and re-create reconciled as one request, or whose delete reconcile was lost in a crash.
4. **Watermark port**: `Load`, `Save`, `Delete(ns, source)` (lists `_eventing/blobwatch/<ns>/<source>/`, trailing `/`
   so `drops2` survives a delete of `drops`, then deletes each key), `ListSources`.
5. **Delete policy per deregistration site:**

   | Site | Cause | Action |
   |---|---|---|
   | `eventing.go:101` | EventSource gone (NotFound) | deregister the timers, then `Purge`; a `Purge` error is returned so the request is retried (today the branch returns nil) |
   | `eventing.go:113` | not a blob source (kind change, timer) | `Deregister`; the branch ends with `Purge` (after `registerTimer` and the status update, or before the return at `:116`) and returns its error last, for a retry; a timer source still ticks, becomes Ready and keeps `lastFire` |
   | `eventing.go:144` | `BucketNotFound` | `Deregister` only; the record is kept; the failing set is forgotten (Decision 9) |
   | `blobwatch.go:87-91` | event dropped from the spec | registry prune only; **the record is kept until the source is purged**, and a re-added event with the same location resumes |

   At both `Purge` sites a `Source` with no watcher (`Deps.Blob` nil) skips `Purge`, as `deregisterBlob` skips
   `Deregister` today.

   **Purge epoch** (new): `purgeEpochs map[sourceKey]uint64` (`sourceKey` = `{ns, source}`, new), under `w.mu`.
   `Purge` holds the record lock while it removes the source's entries and increments the counter, then runs
   `Watermark.Delete`. `Register` stamps the counter and never increments it, so a poll in flight across a `Purge`
   (even one followed by a same-UID `Register`) no longer matches and never re-creates a purged record.
6. **Restart and at-least-once.** After a restart the same location resumes: unchanged objects stay silent, new ones
   fire, removed ones are pruned; a crash between `Publish` and `Save` fires those objects again. "Never drops it"
   holds for every valid-UTF-8 object that stays listed until a poll of its registered event publishes it; the promise
   ends when `Publish` returns.
7. **No migration.** An old `Cursor` record decodes with an empty location and never matches, so the first `Save`
   replaces it and each watched prefix back-fills once.
8. **Start sweep** (new; decided). `BlobWatcher.Run` sweeps once before its first poll: `Watermark.ListSources`, then
   per source `Exists` (a store `Get`); on NotFound, `Watermark.Delete`. Records are listed before the store is read,
   so a source created meanwhile is never swept. An error skips that step with a warn log; no hooks, no sweep.
9. **Save failure on the EventSource** (new; decided — no record splitting). The watcher keeps, per source, the set of
   events whose last `Save` failed. When that set changes it calls `SaveFailing`; the `Source` sets the condition
   `SeenListSaved` (new) False, reason `SaveFailed` (new), message `<event>: <error>` per failing event, or removes it
   when the set is empty. The set counts as reported only on a successful status write, so a conflict retries at the
   next poll. `Purge` and `Deregister` forget the source's set; the non-blob branch removes `SeenListSaved` with
   `Ready`, and the `BucketNotFound` branch removes it in its NotReady status write, so no stale False stays.

## Temporary workarounds

None.

## Contracts

```go
// SeenList is the dedup record of one named blob event (ADR-0157), JSON in kvstore.KV.
type SeenList struct {
	Bucket v1.ObjectName     `json:"bucket"`
	Prefix string            `json:"prefix"`
	UID    v1.UID            `json:"uid"`
	Seen   map[string]string `json:"seen"` // object key → the data.version it fired with (versionOf)
}

type Watermark interface {
	// Load never returns a nil Seen: a missing record, or one saved with "seen":null, loads with an empty map.
	Load(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName) (SeenList, error)
	Save(ctx context.Context, ns v1.NamespaceName, source, event v1.ObjectName, s SeenList) error
	// Delete removes every event record of one source; a source with no record is not an error.
	Delete(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error
	// ListSources returns each source that has at least one record, once (the start sweep, Decision 8).
	ListSources(ctx context.Context) ([]SourceRef, error)
}

type SourceRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// WatchHooks connect the BlobWatcher to the store; NewSource sets them before Run (Decisions 8, 9).
type WatchHooks struct {
	Exists      func(ctx context.Context, src SourceRef) (bool, error)
	SaveFailing func(ctx context.Context, src SourceRef, failing map[v1.ObjectName]error) error // empty map clears
}

// Register stamps the source's current purge epoch into each entry; Purge increments it (Decision 5).
func (w *BlobWatcher) Register(ns v1.NamespaceName, source v1.ObjectName, uid v1.UID, bs *v1.BlobSource)
func (w *BlobWatcher) Deregister(ns v1.NamespaceName, source v1.ObjectName)                        // BucketNotFound
func (w *BlobWatcher) Purge(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error // delete, kind change
func (w *BlobWatcher) SetHooks(h WatchHooks)
```

`KVWatermark` and `MemWatermark` implement all four; `KVWatermark.ListSources` is one `List` on `_eventing/blobwatch/`,
reduced to distinct `<ns>/<source>` pairs. Encoded: `{"bucket":"raw","prefix":"drop/","uid":"<uid>","seen":{"drop/a.parquet":"1759536000000000000-1234"}}`

## Implementation plan

1. `internal/eventing/watermark.go`: `SeenList` replaces `Cursor`; `Delete`, `ListSources`, `SourceRef` on both
   drivers; non-nil `Seen` on `Load`; `MemWatermark` copies the map on `Load` and `Save`.
2. `internal/eventing/blobwatch.go`: `watchEntry` gains `uid`, `purgeEpoch`; the record lock, `purgeEpochs`, the
   warned-key set, `Purge`, `WatchHooks`, `SetHooks`, the sweep in `Run` and the failing set are added; `isNew` and
   `advance` are deleted.
3. `internal/eventing/eventing.go`: `:150` passes `es.UID`; `:101`/`:113` per Decision 5; `NewSource` calls
   `d.Blob.SetHooks` when set. Every direct `BlobWatcher.Register` caller in the tree passes one constant UID,
   including any added since this ADR (today `newWatcher` and `TestBlobWatcherRegisterDeregister`, `:239`, `:241`, in
   `blobwatch_test.go`). No `go.mod`, API, OpenAPI
   or `pkg/funcd/funcd.go` change.
4. Tests, all passing with `-race`. Seam: a new file `export_test.go` adds `func (w *BlobWatcher) PollOnce(ctx context.Context)`;
   `eventing_test.go` adds `scriptLister` (per Bucket, filtered by prefix). `Watermark` contract tests on both drivers
   (round trip of keys with `<`, `&`, a control byte; non-nil empty `Seen`; `Delete` with no record and of `drops`
   leaving `drops2`; `ListSources`). `blobwatch_test.go`: `TestScenarioOutOfOrderVisibilityNeverLoses` (a
   deterministic `fakeLister` subtest plus `mem://` and `file://` concurrent writers),
   `TestScenarioRewrittenObjectFiresAgain`, `TestScenarioDeletedObjectPruned`, `TestBlobWatcherInvalidUTF8KeySkipped`,
   `TestScenarioDroppedEventReaddedResumes`, `TestBlobWatcherPurgeDuringPoll` (subtests: `Purge`; `Purge` + same-UID `Register`; new-UID `Register`).
   `run_test.go`: `TestReconcileTimerPurgeErrorKeepsTimer` (`failKV`, new, over `kvmemory.New()`). `eventing_test.go`:
   `TestScenarioRePointBackFills`, `TestScenarioReCreateBackFills`, `TestScenarioSourceDeleteDeletesRecord`,
   `TestScenarioKindChangeDeletesRecord`, `TestScenarioBucketMissKeepsRecord`, `TestScenarioStartSweepDeletesOrphans`,
   `TestScenarioSaveFailureVisible`. ADR-0119's tests and `TestBlobWatcherTieBreak` stay (drop `KeysAtMax` from its
   comment).
5. Lima: the `s3` lane's reactive case (`e2e/s3.venom.yml:88`) stays the regression check. Docs: the F83 row carries
   ADR-0157, advanced at each status move; at acceptance, the ADR-0119 back-link and a `project-summary` refresh.
6. Done: one passing test per scenario; `just ci` and `just lima-example s3` green; the PR says `Fixes #34`, `Fixes #57`.

## Review checklist

- [ ] `Cursor`, `isNew`, `advance` gone; `SeenList` matches the Contracts; `Load` never returns a nil `Seen`.
- [ ] `pollOne` follows Decisions 1-2 (fire, prune, save on change only, untouched on a `List` error, non-UTF-8 skip).
- [ ] `Purge` per Decision 5 (nil watcher skips it, `BucketNotFound` only deregisters); a stale poll fires and saves
      nothing more; no `Publish` under the record lock; `Delete` never touches another source; a `Purge` error at
      `:101` is returned; a dropped and re-added event resumes (`TestScenarioDroppedEventReaddedResumes`).
- [ ] The start sweep (Decision 8) and `SeenListSaved` (Decision 9) behave as decided.
- [ ] Each scenario has one named, passing test under `-race`; ADR-0119's tests pass; no `go.mod`, API or OpenAPI
      change; `just lima-example s3` is green.

## Consequences

- Negative: each arrival or deletion rewrites the whole O(objects) record with a synced Badger write, also exported by
  the opt-in ADR-0067 incremental backup: about 15 MB per changed poll for 100 000 objects with 100-byte keys.
- Negative: the record must fit one Badger value: `ValueLogFileSize` (64 MiB, `internal/kvstore/badger/badger.go:116`) caps a value (`txn.go:368-369`); JSON escapes
  make a key at most six times its length: about 790 000 40-byte plain keys, at least about 10 800 1024-byte keys.
- Negative: duplicate fires after a re-create, re-point, kind change back to `blob:`, the upgrade, a crash between
  `Publish` and `Save`, or a partial fileblob listing; the start sweep costs one KV `List` and one `Get` per source;
  every reconcile of a non-blob source runs `Purge`, one KV prefix `List` even when no record exists.
- Risks accepted: a same-Size rewrite within S3's one-second `LastModified` resolution is not seen (ADR-0119's
  boundary; exits only with its `data.etag` follow-up, which puts the digest into the `Seen` fingerprint, and the ADR
  that gives `s3://` a digest, 0159's Open questions); an oversized record (or any `Save` that
  keeps failing) is not split, so its objects fire every poll until drained (`SeenListSaved`). Under ADR-0156 each such
  poll is a recurring burst above the per-Sensor bound of 4096: most fires are parked, one DLQ `Put` and one Failed
  Invocation each, evicting unrelated dead letters under the 1000-per-namespace cap and overflowing the Sensor's other
  actions; the parks hold the shared blob poll loop for (N − free queue room) × the park cost: with the queue still
  full from the last poll, 10 800 objects × 2–4 ms (ADR-0156 Consequences) is about 22–43 s per poll against the 15 s interval, so
  the loop never idles.

## Open questions

- For the decider: should a failed `Save` keep the unsaved `Seen` in memory (Decision 9), so a record that cannot be
  saved stops re-firing every poll until a restart? It goes beyond the settled choices, so it is not decided here.

Settled by the decider (2026-10-04): a dropped event's record is kept until its source is purged (Decision 5); a
failed `Save` shows `SeenListSaved` False/`SaveFailed` on the EventSource, no record splitting (Decision 9); a start
sweep in `BlobWatcher.Run` deletes the records of sources the store no longer holds (Decision 8).

## References

- Issues [#34](https://github.com/pyvvo/funcd/issues/34), [#57](https://github.com/pyvvo/funcd/issues/57) (measured at 171adac)
- gocloud.dev v0.46.0 `memblob.go:366-383`, `fileblob.go:821-858`, `:441-445`, `blob.go:964`, `:1040`, `:1144`, `:1279`;
  Go `encoding/json` (U+FFFD, `\u00XX` escapes); Badger v4.9.2 `txn.go:368-369`; S3 strong `LIST` consistency, 1024-byte keys
