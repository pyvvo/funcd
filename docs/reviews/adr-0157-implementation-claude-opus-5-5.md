## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0157 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0157-blob-event-seen-list`, one commit `b15d2c14` on `origin/main`
(`git diff origin/main...HEAD`: 8 files, all in `internal/eventing/`, +1178 / -125). No `go.mod`, `go.sum`,
`api/`, `pkg/` or `docs/` change.

### Verification run (in the branch worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/eventing/ ./pkg/funcd/` | exit 0 |
| `GOOS=linux go vet ./internal/eventing/` | exit 0 |
| `gofmt -l internal/eventing` | no output |
| `golangci-lint run ./internal/eventing/...` (host) | 0 issues, exit 0 |
| `golangci-lint run ./internal/eventing/...` with `GOOS=linux` (host-built binary) | 0 issues, exit 0 |
| `go test -race -count=1 -v ./internal/eventing/` | `ok` (1.6 s); every test listed below passes |
| `go test -race -count=10` of the out-of-order, purge-during-poll, start-sweep and save-failure tests | `ok`, exit 0 (no flake in 10 runs) |

Not run, by the brief: e2e, `go test ./...`, `just ci`, and `just lima-example s3` (the per-PR gate runs these).

### Mutants (overlays built from the branch files; `go test -race -overlay`)

| Mutant | Line | Result |
|---|---|---|
| m1: prune disabled (`if false && !listed[key]`) | `internal/eventing/blobwatch.go:300-301` | killed by `TestScenarioDeletedObjectPruned` |
| m2: UID dropped from the location check | `internal/eventing/blobwatch.go:258` | killed by `TestScenarioReCreateBackFills` |
| m3: live-entry check before `Publish` removed | `internal/eventing/blobwatch.go:289` | killed by `TestBlobWatcherPurgeDuringPoll` |
| m4: the NotFound branch swallows the `Purge` error | `internal/eventing/eventing.go:112` | **survives** (see Minor 2) |

### 🟡 Major

None.

### Minor

- **Minor 1 — an in-flight failing `Save` can re-add a deregistered event to the failing set** · attribution:
  `model`. `saveIfLive` (`internal/eventing/blobwatch.go:314-325`) checks `matches` under `w.mu`, releases it,
  runs `Save`, then calls `noteSave` (`:350`), which takes `w.mu` again and records the failure without checking
  that the entry is still live. `Register` (the dropped-event prune, `:119`) and `Deregister` (`:142`) take only
  `w.mu`, so one of them running during the `Save` call is undone by `noteSave`. Evidence: an overlay probe test
  whose `Save` re-registers the source without event `arrived` and then fails; after four polls the watcher still
  reports `arrived` failing (`last reported failing=map[arrived:value too large …]`, the probe test fails as
  expected). For a dropped event the stale `SeenListSaved` False names an event that is no longer watched and stays
  until the source is purged (no later `Register` prune reaches it, since the entry is already gone); after a
  `BucketNotFound` deregistration it can set False on a NotReady source until the Bucket returns. This contradicts
  Decision 9's "no stale False stays" inside a narrow window (a reconcile during a failing Save). Fix: record the
  outcome only while the live entry still matches `e`, in the same `w.mu` section (for example, pass `e` to
  `noteSave` and skip when `w.watches[k] != e`).
- **Minor 2 — the `:101` Purge-error return is not pinned by a test** · attribution: `model`. The code is
  correct (`internal/eventing/eventing.go:112` returns `s.purgeBlob(...)`), and the checklist names it ("a `Purge`
  error at `:101` is returned"), but mutant m4, which drops that error, passes the whole package.
  `TestReconcileTimerPurgeErrorKeepsTimer` covers only the non-blob branch. Fix: one more case with `failKV` that
  reconciles a deleted source and asserts an error. The ADR's test list did not name such a test, so this is a
  coverage gap, not a missing scenario.

### ✅ Verified correct (keep it)

- **Contracts.** `SeenList`, `SourceRef`, the four-method `Watermark`, `WatchHooks`, and the
  `Register(ns, source, uid, bs)` / `Deregister` / `Purge(ctx, …) error` / `SetHooks` signatures match the ADR
  exactly (`internal/eventing/watermark.go`, `internal/eventing/blobwatch.go`). `Cursor`, `isNew` and `advance` are
  gone (grep for `KeysAtMax`, `MaxModTime`, `Cursor{` finds nothing in the tree).
- **Watermark drivers.** `Load` never returns a nil `Seen` on both drivers (missing record, `"seen":null`);
  `MemWatermark` copies the map on `Load` and `Save`; `Delete` lists `sourcePrefix` with its trailing `/`
  (`watermark.go:124`), so `drops2` survives a delete of `drops`; `KVWatermark.ListSources` is one `List` reduced
  to distinct pairs. An old `Cursor` record decodes with an empty location (`TestKVWatermarkLoadsNullSeenAndOldCursor`),
  which gives the Decision 7 back-fill.
- **Decision 2 (`pollOne`).** A `List` error returns before the record is touched; the match check and `Load` run
  under `recordMu`; a location change replaces the record and marks it changed; each unseen or re-versioned key
  fires, and `Seen[key]` is set only after a successful `Publish`; the live-entry check runs before every
  `Publish`; the prune uses the full listing even after a fire error; `Save` runs only when changed, under
  `recordMu`, and only while the entry still matches. No `Publish` runs under `recordMu`.
- **Decision 1.** A non-UTF-8 key is skipped with one warn per key per process
  (`TestBlobWatcherInvalidUTF8KeySkipped` asserts exactly one log line over two polls).
- **Decisions 3 and 5.** `reconcileBlob` passes `es.UID` (`eventing.go:163`); the NotFound branch deregisters the
  timers and returns the `Purge` error; the non-blob branch deregisters, updates status, and returns the `Purge`
  error last (`:126`, `:140`; `TestReconcileTimerPurgeErrorKeepsTimer` shows the timer still ticks, the source is
  Ready and `lastFire` is kept across the retry); `BucketNotFound` only deregisters; a dropped event keeps its
  record; a nil watcher skips `Purge`. `Purge` holds `recordMu` while it drops the entries and increments the epoch,
  then calls `Delete`; `Register` stamps the epoch and never increments it.
- **Decision 8.** `Run` sweeps before the first tick (`blobwatch.go:180`), lists the records before the store
  lookup, logs and continues on an error, and does nothing without hooks. `NewSource` installs the hooks
  (`eventing.go:99`) before `pkg/funcd` starts `BlobWatcher.Run`.
- **Decision 9.** The failing set is per source, reported only when it changes, and counts as reported only after a
  successful status write; `Purge` and `Deregister` forget it; the non-blob branch removes `SeenListSaved` with
  `Ready`, and `setBlobNotReady` removes it in the NotReady write (`eventing.go:187`). The message is
  `<event>: <error>`, sorted by event.
- **Scenarios.** Each of the 11 scenarios has one named test, all passing under `-race`:
  `TestScenarioOutOfOrderVisibilityNeverLoses` (deterministic subtest plus `mem://` and `file://` with 8 writers ×
  60 objects; exactly once on `mem://`), `TestScenarioRewrittenObjectFiresAgain`, `TestScenarioDeletedObjectPruned`,
  `TestScenarioRePointBackFills` (prefix and bucket), `TestScenarioReCreateBackFills` (two reconciles and one),
  `TestScenarioSourceDeleteDeletesRecord`, `TestScenarioKindChangeDeletesRecord`, `TestScenarioBucketMissKeepsRecord`,
  `TestScenarioDroppedEventReaddedResumes`, `TestScenarioStartSweepDeletesOrphans`, `TestScenarioSaveFailureVisible`.
  `TestBlobWatcherPurgeDuringPoll` covers the three in-flight cases the plan names. The ADR-0119 tests and
  `TestBlobWatcherTieBreak` (its `KeysAtMax` comment dropped) still pass; no existing assertion was weakened.
- **Implementation plan seams.** `export_test.go` adds only `PollOnce`; `scriptLister` filters per Bucket and
  prefix; the `Watermark` contract test runs both drivers with keys containing `<`, `&` and a control byte.
- **Scope and conventions.** The change stays inside `internal/eventing/`; errors go through `api/fault`; logging
  is `slog` only; no `panic`; `ctx` comes first; imports are at the top of each file. The commit message lists the
  scenario tests and carries `Fixes #34` and `Fixes #57`.

### Definition of Done

6 / 8 items hold (5 Review-checklist items + 3 Implementation-plan "Done" items: one test per scenario, `just ci` and
`just lima-example s3` green, the `Fixes` lines).

- Checklist 1-4 hold (items 3 and 4 with Minors 2 and 1 as caveats on test pinning and a narrow race).
- Checklist 5 holds except `just lima-example s3`, which this review did not run (`env`: excluded by the brief;
  the PR gate runs it).
- Done: one passing test per scenario holds; the `Fixes #34` / `Fixes #57` lines hold (commit message; the PR body
  is not open yet); `just ci` and the Lima lane are not verified here (`env`, the per-PR gate).
- Tracking: the ADR still reads `Accepted` and the F83 row is unchanged. In this campaign a docs PR per wave makes
  those moves, so this is not counted against the model. The review did not stamp the ADR or edit any doc.

### Model scorecard

To record: claude-opus-5-5 on ADR-0157 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 6/8 (the 2 misses
are unrun `env` items). The ledger row is below; the wave's ledger PR records it.

### Recommendation

Pass. Both Minors are small follow-ups for the builder: gate `noteSave` on the live entry (Minor 1) and add a
NotFound-branch `Purge`-error test (Minor 2). The per-PR gate still owes `just ci` and `just lima-example s3`.

```json
{
  "date": "2026-10-05",
  "adr": "0157",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 6,
  "dod_total": 8,
  "report": "docs/reviews/adr-0157-implementation-claude-opus-5-5.md",
  "notes": "pass; contracts match exactly, all 11 scenario tests plus ADR-0119 tests pass under -race (10x repeat clean), host and Linux build/vet/lint green, 3/4 overlay mutants killed (prune, UID location check, live check before Publish); minor(model): noteSave records a failed Save for an event deregistered or dropped during that Save, leaving a stale SeenListSaved False (reproduced with an overlay probe); minor(model): the :101 Purge-error return is untested (mutant m4 survives); just ci and the s3 Lima lane not run here (env, per-PR gate); ADR/feat stamping deferred to the wave docs PR."
}
```
