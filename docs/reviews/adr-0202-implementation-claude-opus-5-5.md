# ADR-0202 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0202 implementation, model: claude-opus-5-5)

Reviewed: branch `feat/adr-0202-platform-store-snapshot-and-timeline`, commits `ccb597b7` (implementation) and
`3c1551ea` (ADR `Accepted → Reviewing`), against `origin/main`. 29 files, +1671 / −122.

### 🔴 Blocker

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

**Checks run on the touched packages** (`scripts/agent/d`, pinned toolchain):

- `go build ./...` → exit 0.
- `go vet` on `internal/snapshot/...`, `internal/store/...`, `internal/controller/...`, `internal/gc/...`,
  `internal/services/roles/...`, `internal/workflow/runstate/...`, `api/types/...`, `pkg/funcd`, `tests/chaos` → exit 0.
- `golangci-lint run` on the same set → `0 issues.`
- `go test -race -count=1` on the same set (bar `pkg/funcd` and `tests/chaos`) → every package `ok`;
  `pkg/funcd` with `-run 'Policy'` → `ok`. `gofmt -l` on the changed files → empty.
- The repo-wide `just ci`, the e2e suite and the Linux lint are left to the PR gate, as this run arranges.

**Scenarios — each has its named test, un-skipped (no `t.Skip` in any new test file) and passing under `-race`:**

| Scenario | Test |
|---|---|
| snapshot-is-one-read | `TestScenarioSnapshotIsOneRead` in `internal/store/badger`, `internal/store/memory`, `internal/workflow/runstate/badger` (on disk and in memory), through `snapshotcontract.Run` with `minSnapshots = 200` |
| snapshot-loads-back | `internal/store/timeline_test.go` `TestScenarioSnapshotLoadsBack`: memory → Badger → memory, versions kept, `<new timeline>-<revision+1>`, `fault.Conflict` on a non-empty engine with the engine unchanged |
| cut-reads-in-order | `internal/snapshot/snapshot_test.go` `TestScenarioCutReadsInOrder` |
| first-start-takes-timeline | `TestScenarioFirstStartTakesTimeline`: `^[0-9a-f]{16}-1$`, distinct timelines, a reopened Badger store keeps its own |
| legacy-store-takes-timeline | `TestScenarioLegacyStoreTakesTimeline`: seeded through `Engine.Update` at revision 120; Update → `<timeline>-121`, List matches, an untouched object keeps `"120"` |
| restore-takes-new-timeline | `TestScenarioRestoreTakesNewTimeline`: both `Store.Snapshot` and `Engine.Snapshot` loaded on both engines (B, C, D, E), each `<own>-101` with its own timeline |
| stale-update-conflicts | `TestScenarioStaleUpdateConflicts`: Update and Delete with `T1-130` → `fault.Conflict`, `x` unchanged |
| stale-watch-relists | `TestScenarioStaleWatchRelists` in `internal/store`, `internal/controller/relist_test.go`, `internal/gc/relist_test.go` |
| initial-list-spans-timelines | `TestScenarioInitialListSpansTimelines`: each object once as Added, then the new write |
| policy-cache-follows-writes | `pkg/funcd/policy_cache_internal_test.go` `TestScenarioPolicyCacheFollowsWrites`: grant, forbid, and both deletes each change the decision |

Units `TestParseVersion` and `TestResumePoint` (`internal/store/version_test.go`) pass.

**Contracts and decisions:**

- `internal/snapshot/snapshot.go` matches the Contracts exactly: `Record`, `Source`, `Loader`, `Cut(ctx, events,
  meta, runs, emit)`; `Cut` reads events, meta, then runs, returns meta's version, and stops at the first error.
- Decision 1: `internal/snapshot/badger/badger.go` `Snapshot` is one `db.View` with one iterator (no `DB.Backup`,
  `Stream` or `NumGo`); `Load` checks emptiness before the first write and writes through a `WriteBatch`. The memory
  engine (`internal/store/memory/memory.go`) copies under `RLock`, releases, sorts, then emits clones; its `Load`
  writes in batches of 1024 under the write lock.
- Decision 3: `store.New` (`loadMeta`) reads the `timeline` record beside `revision`, mints 64 bits from
  `crypto/rand` as 16 lowercase hex only when none is stored, and a failure sets `initErr`. Both `Engine.Load`
  implementations skip `IsTimelineRecord`; `Store.Snapshot` omits the timeline record and returns
  `<timeline>-<revision>` from the same stream.
- Decision 5: every site of the table follows its row — `store.go` mint sites use `s.version`; `watch.go` returns
  `fault.Unavailable` for a since-version of another timeline (plain included) and `initialEvents` (renamed from
  `snapshot`) lists objects of another timeline; controller and collector keep a `store.Version` advanced by
  `ResumePoint` and re-watch with `""` for the zero value; `maxRevision` is gone and the policy-cache key joins the
  four collection versions; `CompilePolicies` returns its List's collection version. Preconditions stay string
  equality.
- No version string is parsed or formatted outside `internal/store/version.go`: the only remaining `strconv` use on a
  revision is the counter record in `store.go` (`nextRevision`, `parseRevision`), which is not a version string.
  The chaos harness and the app-revision e2e helper use `store.ParseVersion`.
- The `ObjectMeta.ResourceVersion` doc comment says to compare for equality only.

**Tree, conventions, tracking:**

- Every file of the Implementation plan is present. One addition, `internal/snapshot/badger`, holds the shared
  one-read snapshot and batch load used by both the metastore engine and the run state; it keeps the two Badger
  drivers from duplicating the iterator and is in scope.
- ctx-first throughout; errors are `api/fault` kinds (a cancelled context is returned as is, the existing engines'
  convention); no `panic`, no `any` in the new APIs, `log/slog` untouched.
- `go.mod`/`go.sum` unchanged: no new dependency.
- The ADR diff is the status line only (`Accepted → Reviewing`); its substance is unchanged. ADR-0006 already carries
  `Superseded in part by: ADR-0202`.

### Definition of Done

20 / 20 items hold: the ADR's Definition of done (6), its Review checklist (4) and the generic implement-gate DoD
(10). The repo-wide `just ci` item is held on the touched packages here and is re-run by the PR gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0202 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 20/20.
See docs/reviews/model-scorecard.md once the tracking PR records the ledger row.

### Recommendation

Sign off: the ADR moves `Reviewing → Implemented`. The tracking PR moves the FEAT-0009 F109 row to `implemented`
and records the ledger row.
