# ADR-0311 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors (ADR-0311 implementation, model: claude-opus-5-5)

Reviewed `32169c14`: six commits on top of the acceptance commit `a9b8d524`, built by a session other than this one.
Commit 1 (`1a9fdd4e`, #1383's fix) passed its own review first (`docs/reviews/issue-1383-fix-claude-opus-5-5.md`). The
ADR's substance is unchanged: only its status line and the F225 row moved.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Build and checks, run once on this branch by `scripts/agent/gate.sh`.**
  `GATE PASS`: audit 3 s (no hard flag; its one retry-loop note is the Lake's three attempts, ported from
  `workload.go`), `ci-full` 834 s (build, lint, tests, e2e), the Linux build, vet and lint 19 s, the Linux tests
  52 s, stress 258 s, and the run changed no file.
- **Every Scenario's tests pass, none skipped.** No assertion line of a pre-existing test was removed; only the setups
  the plan names changed (the four `Write` tests, two listing tests, `copyOn` and commit 1's Bucket pin).
  - `each-kind-round-trips-as-today`: every test of `internal/backup/workload` and of `pkg/funcd`'s `workload_backup`,
    `sql_backup`, `volume_backup` and `target_sync` internal test files, and the seven pins:
    `TestScenarioRestoreBucketAsAndInPlace`, `TestScenarioVolumeRestoreAsAnotherFunction`,
    `TestRestoreReason_MemberNotFound`, `TestRestoreReason_TornObjectIsIntegrity`,
    `TestRestoreReason_EngineNotReady`, `TestRestoreReason_VolumeWithoutLeafIsFailed` and
    `TestRestoreLake_PutsFilesBeforeCatalog`, which passed on commit 1's services before the refactor.
  - `every-kind-round-trips-the-whole-store`: `TestKinds_RoundTripLeavesTheStoresAsTheyWere`, which from commit 4 also
    asserts that its members cover every provider's `BackupKinds`, `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote`
    and the three `TestIssue1383_` tests.
  - `an-unserved-kind-fails-by-name`: `TestWrite_AnUnservedKindFailsByName`, `TestRestore_AnUnservedKindFailsByName`
    and `TestListCopy_SkipsAKeyNoKindParses`.
  - `a-new-kind-is-one-type`: `TestKinds_ATestKindNeedsNoServiceCode`, over a test-only kind.
  - `the-dr-lanes-pass-unchanged`: the 13 lanes and app-todo, run once on the PR's tip.
- **Contracts.**
  - `internal/backup/workload/kinds.go` holds `MemberKind`, `Destination`, `FileWriter`, `RecordOpener`, `ErrNotEmpty`
    and `ErrTornObject` as written. The five constructors `KVStores`, `Buckets`, `Lakes`, `Databases` and `Volumes`
    sit one per file, with the loader interfaces. `Write`, `ListCopy` and `SlotCopies` take the kinds, and `Sources`
    is gone with `CatalogKey`.
  - `Deps.Kinds` replaces `Sources`, `KV`, `SQL`, `Volumes` and `Purger`. `New` refuses a nil kind and a kind served
    twice (`TestNew_RefusesAKindServedTwice`).
  - `backupHook` embeds `lifecycle.Provider`; kv, blob, sql, lake and the Executor assert it; `sharedHooks` names it
    `backups`; `New` hands the Function reconciler over with `useFunctions`.
- **Decision 3, the order.** `load` plans every member before the hold, holds the namespace when in place, runs every
  `Prepare` (a failed one releases the earlier ones: `TestRestore_AFailedPrepareReleasesTheEarlierOnes`), loads in spec
  order, clears what each load began except a load refused `NotEmpty`, and lifts the hold only after a completed load.
- **Decision 4, each kind's rules.** I read the Bucket and volume kinds line by line against the table: the copy (the
  catalogs read before the walk and written after the objects), the destination, the emptiness (a catalog counts; a
  lost volume is empty), the volume's `Prepare` that stops the workers, the in-place `Clear` first (a lost volume's
  too), and the purge as the Bucket's `Clear`. A nil part follows Decision 2: `Write`, `IsEmpty` and `Load` answer
  `Unavailable`; `Destination`, `Prepare` and `Clear` do nothing.
- **Decision 5, one mapping.** `reasonOf` maps every member call's error, and the service's own steps keep their
  reasons. The sentinels wrap an error without changing its fault kind or message.
- **Decision 6.** `Write` looks every member's kind up before its first put, `plan` refuses an unserved kind by name
  before the hold, and the listing skips a key that no kind parses.
- **The checklist, by grep.** `internal/services/backup` names no member kind, and its `Deps` has none of the five old
  fields. In `internal/backup/workload` only the five kind files and `Resolve` with `memberKinds` name one.
  `BackupKinds` runs in production only in `registerWorkloadBackups`. `Format` is 1, `api/types/v1alpha1` changes in
  two doc comments only, and `go.mod` is unchanged.
- **Mutants.**
  - The builder ran 26, each caught. The one that first survived, a failed `Prepare` that keeps the earlier stops, is
    now caught by `TestRestore_AFailedPrepareReleasesTheEarlierOnes`.
  - I ran four of my own through `go test -overlay`. A `Destination`'s `NotFound` mapped to `Failed` fails
    `TestRestoreReason_MemberNotFound`. A load refused `NotEmpty` and then cleared fails
    `TestKinds_ATestKindNeedsNoServiceCode` and `TestRefusedVolumeRestoreKeepsItsData` ("the refused Restore removed
    orders.sqlite"). A Lake `Parse` that returns attempt 0 fails `TestListCopyMembers` and
    `TestKinds_ParseReadsWhatWriteWrote`. `backups` left out of `sharedHooks` fails
    `TestPhases_ListTheProvidersWithTheirHooks`.
- **The builder's two choices conform.** Commit 3 keeps the four `Deps` fields unread and commit 4 removes them, as
  the plan splits the work. `TestKinds_ATestKindNeedsNoServiceCode` clears the second load too, since that load began:
  Decision 3 clears what each load began, and only a load refused `NotEmpty` stays uncleared.
- **Conventions (ADR-0002).** No `any` in the new signatures, `api/fault` errors (`Invalid` for an unserved kind or a
  kind served twice), no `panic`, `log/slog` only. The lint, with its import-graph rules, is clean.
- **Tracking.** The ADR was at Reviewing and F225 at reviewing, with the substance unchanged.

### Definition of Done

13 of 13 hold: the ADR's 4 checklist lines and 9 applicable generic items (no port has drivers here, so the contract
suite item does not apply). The 13 lanes and app-todo run once, on the PR's tip.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0311 (implementation): pass, 0 blockers, 0 majors, 0 minors, 0 model-attributed,
DoD 13/13. See docs/reviews/model-scorecard.md.
