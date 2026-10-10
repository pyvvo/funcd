# ADR-0201 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0201 implementation, model: claude-opus-5-5)

Reviewed commit `dab46e80` (`git diff origin/main...HEAD`, 19 files, +1410/−304) against the ADR's Contracts,
Scenarios, Implementation plan, Review checklist and Definition of done, and ADR-0002.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The ADR-0119 `restart-no-replay` unit test no longer opens a fresh driver** · attribution: `model` ·
  `internal/eventing/blobwatch_test.go`: the test used to build a second `KVWatermark` over the same KV for the
  "restart"; with `KVWatermark` deleted it now hands the same `MemWatermark` instance to both watchers, so it proves
  the watcher's dedup but not persistence through a new store handle. The real restart is covered by
  `TestScenarioRestartKeepsEventingState` (`pkg/funcd`, on disk, New → Shutdown → New), so nothing is untested.
  Fix (optional): move that case to an external test package and run it over an on-disk `eventstore.Store` reopened
  between the two watchers.

### ✅ Verified correct (keep it)

- **Build, vet, lint, tests.** `go build ./...` exit 0; `go vet` on `internal/eventing/...`, `pkg/funcd`,
  `internal/platform/config` exit 0; `golangci-lint run` on the same packages: `0 issues`. `go test -race -count=1`:
  `ok` for `internal/eventing`, `deadletter`, `deadletter/badger`, `deadletter/memory`, `eventstore`,
  `internal/sensor`, `internal/platform/config`, and `pkg/funcd -run 'TestScenario|EventStore|Watermark|DeadLetter'`.
- **Every scenario has a named, un-skipped, passing test** (no `t.Skip` in any touched test file):
  `restart-keeps-eventing-state`, `memory-mode-forgets`, `memory-store-keeps-kv-keys` in
  `pkg/funcd/eventstore_internal_test.go` (data dir from `os.MkdirTemp("", "funcd")`); `tenants-stay-apart` (disk and
  memory), `rewrites-do-not-grow-disk` (400 × 2 MiB saves, disk blocks checked every 25th save),
  `upgrade-moves-seen-lists` (over a `kvbadger.Open` KV), `interrupted-move-resumes`, `move-failure-stops-start` in
  `internal/eventing/eventstore`. `TestNewRunsTheSeenListMove` also proves the move and its failure through `New`.
- **Test plan items present and passing**: `deadletter.Contract` over `New` and `NewOnDB`
  (`TestBadgerOnDBContract`); `eventing.WatermarkContract` (exported from `watermarkcontract.go`, with the 2 MiB and
  `"seen":null` cases) over `SeenLists()` on disk and in memory and over `MemWatermark`; `snapshotcontract` over the
  store in both modes plus `TestSnapshotLoadsSeenLists`; `TestSeenListParts` (headless parts ignored, removed by the
  next `Save`, over `maxRecord` → `fault.Invalid` with the previous list kept); `TestMigrateSkipsCursorRecords`.
- **Contracts match.** `Store{db, seen, stop, wg}`, `Open`, `DeadLetters`, `SeenLists`, `Close`, `Snapshot`, `Load`,
  `MigrateSeenLists`, the constants `seenPrefix`, `legacyPrefix`, `partSize`/`maxRecord`/`gcInterval`, and
  `dlbadger.Options`/`NewOnDB` with `New` built on both, exactly as specified.
- **Seen-list layout (Decision 5).** `Save` writes the next generation's parts in one `WriteBatch`, then one `Update`
  sets the head and deletes the event's other keys; `Load` is one `View`; a mutex orders writes so two saves never
  share a generation. Prefixes end in `/`, so `Delete` and `ListSources` never cross into a neighbouring source.
- **The move (Decision 6).** Runs only when the event store has a directory, after `Open` and before
  `NewBlobWatcher`; skips `Cursor` records, never overwrites a record the event store holds, deletes every legacy key
  only after every copy, and fails the start with the KV keys kept.
- **Tenant isolation and lifecycle.** `dl/` operations are unchanged (`NewOnDB` over the shared DB, its `Close` a
  no-op); the GC loop runs on disk only; `Shutdown` closes the store once, after the BlobWatcher and Sensor have
  stopped, in the slot the DLQ used. A failed `New` releases it through the existing Shutdown-on-error path.
- **Snapshot (Decision 9).** In memory the `MemWatermark` is copied under its lock (`Range`, new) before the `View`,
  then emitted as gen-1 heads and parts after `dl/`; `Load` refuses a non-empty store with `fault.Conflict` and routes
  `seen/` records into the `MemWatermark`.
- **The rule (Decision 3).** Only `eventstore/migrate.go` (and its test) imports `internal/kvstore` under
  `internal/eventing` and `internal/sensor`; `KVWatermark`, `NewKVWatermark` and the `_eventing/` key writes are gone
  from shipped code.
- **No API, config or dependency change**: `go.mod`/`go.sum` untouched; `WithDeadLetterQueue`, `deadletter.Store`,
  `eventing.Watermark` and `SeenList` keep their shapes; only comments changed in `config.go` and
  `examples/funcdconfig.yaml`.
- **ADR substance unchanged**: the only diff to `docs/adr/0201-event-store.md` is the `Accepted → Reviewing` status line.

### Definition of Done

9 / 9 hold (Review checklist 5 + ADR Definition of done 4). The repo-wide `just ci`, e2e suite and Linux lint were
not run here: by this review's scope they run once at the PR gate; the touched packages' build, vet, lint and `-race`
tests are green.

### Model scorecard

To record: claude-opus-5-5 on ADR-0201 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 9/9.

### Recommendation

Sign off: ADR-0201 moves `Reviewing → Implemented`. The minor is optional polish for the builder; the tracking PR moves
the FEAT-0009 F110 row to `implemented` and records the ledger row.
