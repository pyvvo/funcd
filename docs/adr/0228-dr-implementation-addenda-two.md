# ADR-0228: Disaster recovery implementation addenda, part two: contracts settled while implementing ADR-0222 and ADR-0223

- **Status**: Accepted (2026-10-11, by an `adr-batch` run after a clean `adr-judge` gate; it records decisions already in the code) (2026-10-11; it records only decisions already on main at ae8869a9, each cited to the code)
- **Superseded in part by**: [ADR-0286](0286-funcdctl-backup-plan-prints-an-example-and-edits-no-file.md) (2026-10-25) — Decision 7.1: the verify cadence is a comment in the printed example, not a stderr line
- **Date**: 2026-10-11
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, backup, restore, hold, funcdctl, contracts
- **Realizes**: [FEAT-0009/F111](../feat/0009-feat-disaster-recovery.md) (Decision 7: F112). No feat row changes here:
  F109 to F111 are implemented; F112's row moves `accepted` → `implemented` in the disaster-recovery session's tracking
  PR for ADR-0223 (that session owns FEAT-0009's rows, as in ADR-0227). Adds no capability, so no board card.
- **Supersedes (in part)**, each clause getting a `Superseded in part by: ADR-0228` back-link at acceptance (lines at
  origin/main ae8869a9; the quotes identify each clause once back-links shift them):
  - [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (Implemented): Decision 3's "skipped while `paused`,
    while `HeldIn(ns)` or while a Backup of the schedule is `Pending` or `Running`; else it creates" (80–81), by
    Decision 2.1; Decision 7's "a failure empties what it wrote" (121) and "loads, lifts it" (125), by Decision 1.3;
    Decision 8's "`funcdctl hold release --namespace <ns>` (NEW, repeatable) removes it" (133–134), by Decision 5.2;
    Decision 10's "developers and admins also create and delete them" (142), by Decision 2.4; the Implementation plan's
    "`cmd/funcd/restore.go`" (208) and "`TestEveryRunnerConsultsHold`, one namespace held" (213), by Decision 3.4.
  - [ADR-0223](0223-funcdctl-backup-plan.md) (Implemented): Decision 4's "For `platform` with `--rpo`, stderr also
    names ADR-0205 Decision 6's verify cadence" (142, a `--write` bullet), by Decision 7.1; Decision 6's "`main` maps an
    error with `ExitCode()` to that code, any other to 1" (154), by Decision 7.5.
- **Extends only** (no back-link): every other item; ADR-0206 Decisions 6 and 8 and ADR-0205 Decision 3 get no entry,
  and no item touches ADR-0206 Decision 5. **Relates to**: [ADR-0227](0227-disaster-recovery-implementation-addenda.md)
  (part one) · ADR-0147 (namespace admission lock) · ADR-0170 (forced ResourceGroup delete) · ADR-0000.

## Context & Need

The implementations of ADR-0222 (#949) and ADR-0223 (#954) settled contracts those ADRs omit or state otherwise; their
reviews ask an addendum to record them (`docs/reviews/adr-0222-implementation-claude-opus-5-5-2.md`, `-3.md`, `-4.md`,
the `adr` Minors; `adr-0223-implementation-claude-opus-5-5.md`, notes 1–6). Both ADRs are frozen (ADR-0000).

## Scenarios

One per theme, each proven on main by the named test; dependencies (8) have none (the Review checklist reads `go.mod`).

- `scenario: stopped-in-place-load-stays-held` (1.3) — Given an in-place Bucket load half written, When funcd stops,
  Then the namespace stays held by a marker naming the Restore, `Interrupted` at the next start, until `hold release
  --namespace`. `TestRestoreStopKeepsNamespaceHeld`.
- `scenario: overlapped-slot-not-caught-up` (2.1) — Given a Backup `Running` at the next slot, When it succeeds, Then
  that slot gets no Backup; the following one runs. `TestScenarioCatchUpOnceNoOverlap`.
- `scenario: one-in-place-restore-per-namespace` (2.4) — Given an active in-place Restore, When an admin creates another
  in place or deletes it, Then `409` naming it. `TestRestoreInPlaceOnePerNamespace`, `TestRestoreInPlaceDeleteWaitsForEnd`.
- `scenario: manifest-names-the-copy` (3.2) — Given Backup `nightly-1`, uid `uid`, When `workload.Write` runs, Then the
  manifest's `backup` is `nightly-1-uid`. `TestWriteReadBack`.
- `scenario: load-meets-standing-marker` (4) — Given Restore `first`'s marker, When another in-place Restore reaches the
  load, Then `Failed` (`NamespaceHeld`), nothing written, marker unchanged. `TestRestoreLoadRefusedWhileMarkerStands`.
- `scenario: namespace-release-waits-for-restore` (5.2) — Given a `Running` in-place Restore's marker and a `data` marker,
  When an admin releases the namespace, Then `409` naming it, both kept; once it failed, both lift. `TestReleaseNamespaceWaitsForRestore`.
- `scenario: guard-reads-store-url` (6) — Given `WithBlobStoreURL("s3://store?…")`, When a schedule targets
  `s3://store?prefix=b/`, Then `400` naming `spec.target.url`. `TestScenarioTargetInStoreRefused`.
- `scenario: plan-prints-cadence-without-write` (7.1) — When `plan platform --rpo 1h --keep 720h` runs without
  `--write`, Then stderr ends with the verify-every-15m line. `TestScenarioPlatformFromObjectives`.

## Scope

**In**: the items below. **Out**: any behaviour change; ADR-0227's items; the round-3 review's claim that a forced
ResourceGroup or Namespace delete bypasses the in-place admission (dropped: the forced delete runs each member's delete
admissions, `internal/controlplane/handlers.go:391-396`, `:450-452`, `:380`; a Namespace delete deletes no Restore, `:526-527`).

## Constraints & Decision drivers

- ADR-0000: an `Implemented` ADR takes only a back-link. ADR-0227's rule: an item contradicting a stated clause is a
  supersede entry; one adding a name or rule extends. Every item is cited at ae8869a9; nothing unbuilt is recorded.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **One addenda ADR for both, cited to the code** | One place, ADR-0227's shape | Readers follow a back-link | **Chosen** |
| Fold into ADR-0227 | One addenda ADR | ADR-0227 is Accepted, so frozen | Rejected |
| Leave the gaps to the reviews | No work | Contracts invisible to a reader of `docs/adr/` | Rejected |

## Decision

Each item states what main does. **[changes]** marks a superseded clause (header); every other item extends.

### 1. The namespace hold around an in-place Restore (ADR-0222 Decisions 7, 8; #949)

1. `hold.Marker` gains `Restore` (`internal/platform/hold/hold.go:28-32`): the in-place load's marker names its Restore
   (`internal/services/backup/restore.go:204`); `restore run`'s `data` marker names none.
2. `HoldNamespace` never replaces a marker: one standing for the reason ⇒ `fault.Conflict` naming its Restore
   (`internal/platform/hold/namespace.go:57-65`); the in-place load is its only production caller. `ReleaseNamespace` of a
   reason not held ⇒ `fault.Conflict` (`:103-105`); `NamespaceMarkers` copies the markers by namespace and reason (`:76-85`).
3. **[changes]** The load writes the marker once integrity and targets are checked, then fills each member
   (`restore.go:191-218`). Only a load that filled every member lifts it, by `ReleaseNamespaceMarker` (only while the
   marker's Restore and `Since` are unchanged, else `fault.Conflict`; `namespace.go:93-108`), never from a defer; a
   failed lift is a Warn and the marker stays (`restore.go:219-225`). A failure tries to empty what it wrote and joins
   that error to the cause (`:215-216`). A failure, a stop (the load returns before `finish`, `:88-90`) or a crash keeps
   the marker whatever the emptying did; a stop empties under the cancelled context, so a Bucket's `Purge` or a catalog's
   `ListAfter` (`:363-367`) may leave objects, and a crash empties nothing: the marker fences them until 5.2.
4. `hold.Open` reads `.hold-ns/<ns>.<reason>` for `restore` and `data` only, skipping other names (`namespace.go:175-178`);
   an undecodable marker fails `Open`, as an undecodable `.hold` does (`:184-186`; `hold.go:151-153`, `:165`).

### 2. Run lifecycle and admission (ADR-0222 Decisions 3, 4, 7, 10; #949)

1. **[changes]** A due slot while `paused` or `HeldIn(ns)` keeps `lastScheduleTime`, so missed slots collapse into one
   catch-up; while a Backup of the schedule is `Pending` or `Running` the slot is taken (`lastScheduleTime = S`) and
   creates none, so it is never caught up (`internal/services/backup/schedule.go:55-69`).
2. A `Running` Restore this process does not run is `Failed` (`Interrupted`) (`restore.go:56-60`), as for a Backup.
3. `Restore.Ended()`: `Succeeded` or `Failed`; `Pending`, `Running` and unreconciled `""` are active (`api/types/v1alpha1/backup.go:319-320`).
4. **[changes]** `NewRestoreInPlaceAdmission(authz, r, h)` (`internal/controlplane/admission/backup.go:142`) handles every
   Restore create and delete under the ADR-0147 namespace lock (`:150-154`; `handlers.go:52`). An in-place create needs
   `update` on `Namespace` (`:169-175`), then is `fault.Conflict` while another in-place Restore of the namespace is
   active (`:176-185`) or the namespace carries a restore marker, naming its Restore and `funcdctl hold release --namespace
   <ns>` (`:186-191`; a nil hold skips it; a `data` marker does not block). Deleting an active one is `fault.Conflict`
   (`:158-162`): a `Pending` one cannot be cancelled. The `update` check is kind-level (no per-object resource): built-in
   RBAC admits only `admin` (`internal/auth/rbac/rbac.go:24-40`); a per-namespace Cedar grant (ADR-0074) is not consulted.

### 3. Workload copy and members (ADR-0222 Decisions 4, 6, 8, Contracts; #949)

1. `workload.Member` gains `Catalog v1.CatalogRef`, filled by `Resolve` from the CatalogService's `spec.catalog`
   (`internal/backup/workload/workload.go:55-59`, `:146-148`): `Write` takes no store.
2. `Manifest.Backup` is the copy's directory `<backup>-<uid>` (`workload.go:86-87`, `:169`; path `<ns>/<backup>-<uid>/`,
   `internal/services/backup/run.go:146`), not the Backup's name.
3. Exported for `workload` (`workload.go:307`, `:325`): `backup.WriteRecord` (`internal/backup/layout.go:224`),
   `backup.ConditionRefused` (`internal/backup/backup.go:308`). `envelope.ParseIdentities` (`envelope.go:232-246`) applies
   `ReadIdentities`' rules to the Restore's `AGE_IDENTITY` Secret key (`restore.go:177-178`); ADR-0204's files keep `ReadIdentities`.
4. **[changes]** `internal/restore/run.go` writes `restore run`'s `data` markers (`holdNamespaces`, `:229-256`, called at
   `:193` after `holdRuns`), not `cmd/funcd/restore.go`; a failed run removes them (`undo`, `:338-340`). The per-namespace
   runner test is `TestEveryRunnerConsultsHoldInNamespace` (`pkg/funcd/hold_internal_test.go:226`; `:62` stays platform-wide).

### 4. Status reasons (ADR-0222 Contracts; #949)

Three beyond the nine (`internal/services/backup/backup.go:33-49`); the strings are the contract, two constants unexported:

| Reason | When | Code |
|---|---|---|
| `Failed` | a run failure no other reason names; the message carries the cause | `reasonFailed`; `restore.go:383-388`, `run.go:124-157` |
| `BackupNotReady` | a Restore's Backup is missing or `Failed` | `reasonBackupNotReady`; `restore.go:65-68`, `:117-131` |
| `NamespaceHeld` | an in-place load meets a standing restore marker: nothing written, the marker unchanged | `restore.go:202-210` |

### 5. Hold API and CLI (ADR-0222 Decision 8, ADR-0206 Decision 8; #949)

1. `POST /apis/funcd.io/v1alpha1/hold/release` gains body key `namespaces` (`internal/controlplane/hold.go:310-315`;
   `api/openapi/funcd.v1alpha1.yaml:2070-2071`): it authorizes `update` on `Namespace`, kind-level as in 2.4
   (`hold.go:341`, `:359`), not `WorkerNode`; `advance` beside it is `fault.Invalid` (`:337-340`).
   `sdk.ReleaseNamespaceHold` (`pkg/sdk/hold.go:42`) sends it; `funcdctl hold release --namespace` is repeatable,
   exclusive with `--advance` (`cmd/funcdctl/hold.go:55-57`).
2. **[changes]** `ReleaseNamespaces` (`internal/controlplane/hold.go:245-283`): an invalid name ⇒ `fault.Invalid` before
   any change; then per namespace, in order: no marker ⇒ `fault.Conflict`; a restore marker whose Restore is `Pending`
   or `Running` ⇒ `fault.Conflict` naming it, the `data` marker kept too; a restore marker naming an ended, deleted or no
   Restore lifts by `ReleaseNamespaceMarker`; then the `data` marker lifts. Namespaces before a refused one stay released.
3. `GET …/hold` lists `Evidence.Namespaces`, a `NamespaceHold` per marker, `Phase` the named Restore's, empty once deleted
   (`internal/platform/hold/hold.go:84`, `:89-93`; `internal/controlplane/hold.go:109-128`; `cmd/funcdctl/hold.go:23-26`).

### 6. funcd options (ADR-0222 Decision 6; #949)

`funcd.WithBlobStoreURL` (`pkg/funcd/options.go:174-182`) carries the store URL the schedule admission and controller
compare targets with (`pkg/funcd/funcd.go:1186`, `:2143`); `cmd/funcd` passes `blob.target`, else
`gocloud.FileURL(blob.dir)` (`cmd/funcd/main.go:378`, `:1127-1133`). Absent ⇒ targets are compared with nothing.

### 7. `funcdctl backup plan` (ADR-0223 Decisions 1, 3, 4, 6; #954)

1. **[changes]** For `platform` with `--rpo`, the verify-cadence line goes to stderr in print mode too, not only with
   `--write` (`cmd/funcdctl/backup.go:232-236`).
2. `-f` with `platform`, `kv` or `blob`, and `--config` with `schedule`, are `fault.Invalid` naming the flag (`:142-150`).
3. `Apply` also refuses a tagged scalar, a key with no value and a value spanning lines, `fault.Invalid` naming the key
   (`internal/backup/plan/edit.go:146-151`, `:160-161`).
4. `blob`: `--keep` plus the interval above `v1alpha1.MaxDuration` ⇒ `fault.Invalid` naming `--keep` (`derive.go:80-83`).
5. **[changes]** `exitCode` uses `ExitCode()` only above 0, else 1: an error never exits 0 (`cmd/funcdctl/main.go:22-29`).
6. Every non-error `CheckBackup` and `CheckBlob` finding is printed (`internal/backup/plan/check.go:21-26`), including
   `backup.encryption.recipients` and `.none`, which `logBackupFindings` drops when `backup.target` is set
   (`cmd/funcd/main.go:1003-1011`): the start warns both through `envelope.New` (`envelope.go:76-80`, `:93-94`).

### 8. Dependencies (ADR-0222 Implementation plan, go.mod; #949)

`modernc.org/sqlite` v1.60.1 (`go.mod:70`, cgo-free) brings, indirect, `modernc.org/libc` v1.77.1, `mathutil` v1.7.1,
`memory` v1.12.1 (`:413-415`), `github.com/ncruces/go-strftime` v1.0.0 (`:308`), `github.com/remyoudompheng/bigfft` (`:333`);
licences read in the module cache 2026-10-11: BSD-3-Clause, MIT (go-strftime, libc's musl part), public domain (SQLite's
C source). MVS raises (`go.mod:64-67`, `:230`, `:283`, `:398`, `:402-403`): `golang.org/x/crypto` v0.57.0, `x/mod` v0.41.0,
`x/net` v0.59.0, `x/sync` v0.23.0, `x/sys` v0.48.0, `x/text` v0.42.0, `x/tools` v0.50.0, `google/pprof`, `mattn/go-isatty`
v0.0.24. The deprecated `http2.ConfigureTransport`'s one use, a test, sets `http.Transport.Protocols` (`internal/edge/tls/static/static_test.go:140-143`).

## Temporary workarounds

None.

## Contracts

Copied from the code at ae8869a9 (struct fields joined on one line); all exist, none is new here.

```go
// internal/backup/workload (workload.go:55-59)
type Member struct{ Kind v1.Kind; Name v1.ObjectName; Catalog v1.CatalogRef }
// internal/platform/hold (hold.go:28-32, :84, :89-93; namespace.go:59, :77, :95)
type Marker struct{ Reason string `json:"reason"`; Since v1.Timestamp `json:"since"`; Restore v1.ObjectName `json:"restore,omitempty"` }
type NamespaceHold struct{ Namespace v1.NamespaceName `json:"namespace"`; Marker; Phase v1.RunPhase `json:"phase,omitempty"` }
// Evidence gains: Namespaces []NamespaceHold `json:"namespaces,omitempty"`
func (h *Hold) HoldNamespace(ns v1.NamespaceName, m Marker) error
func (h *Hold) NamespaceMarkers() map[v1.NamespaceName]map[string]Marker
func (h *Hold) ReleaseNamespaceMarker(ns v1.NamespaceName, m Marker) error
// api/types/v1alpha1 (backup.go:320); internal/controlplane/admission (backup.go:142)
func (r *Restore) Ended() bool
func NewRestoreInPlaceAdmission(authz auth.Authorizer, r StoreReader, h *hold.Hold) Admission
// internal/backup (layout.go:224, backup.go:308); internal/backup/envelope (envelope.go:234)
func WriteRecord(w io.Writer, r snapshot.Record) error
func ConditionRefused(err error) bool
func ParseIdentities(name string, r io.Reader) ([]age.Identity, error)
// pkg/funcd (options.go:177); pkg/sdk (hold.go:42); internal/services/backup (backup.go:44)
func WithBlobStoreURL(url string) Option
func (c *Client) ReleaseNamespaceHold(ctx context.Context, namespaces []string) error
const ReasonNamespaceHeld = "NamespaceHeld"
```

**Consumes**: ADR-0222's hold, admission and controllers; ADR-0147's lock; ADR-0223's `plan`. **Exposes**: body key
`namespaces`; `Evidence.namespaces`; reasons `Failed`, `BackupNotReady`, `NamespaceHeld`; the in-place `409`s; the names above.

## Implementation plan

None: every item is already on main. At acceptance: `Status: Accepted` and the header's back-links. With no code to
write, the citation review is its review gate. No blueprint, roadmap or board change; the feat row: see the header. Tests:

| Item | Tests that pin it |
|---|---|
| 1.1–1.3 | `TestReleaseNamespaceMarker`, `TestReleaseNamespace` (`internal/platform/hold/hold_test.go:150`, `:109`); `TestRestoreStopKeepsNamespaceHeld`, `TestRestoreInPlaceRefusedWhileMarkerStands`, `TestScenarioRestoreInPlace` (`pkg/funcd/workload_backup_internal_test.go:583`, `:626`, `:771`) |
| 2.1–2.4 | `TestScenarioCatchUpOnceNoOverlap`, `TestRestoreInterruptedHoldRelease` (`pkg/funcd/workload_backup_internal_test.go:410`, `:528`); `TestNewestDueCollapses` (`internal/services/backup/schedule_test.go:49`); `TestRestoreInPlaceOnePerNamespace`, `TestRestoreInPlaceDeleteWaitsForEnd` (`internal/controlplane/hold_test.go:185`, `:224`) |
| 3.1–3.4 | `TestResolveScopes`, `TestWriteReadBack` (`internal/backup/workload/workload_test.go:40`, `:155`); `TestParse` (`internal/backup/envelope/envelope_test.go:521`); `TestScenarioPlatformRestoreHoldsNamespace` (`cmd/funcd/workload_restore_test.go:28`); `TestEveryRunnerConsultsHoldInNamespace` (`pkg/funcd/hold_internal_test.go:226`) |
| 4 | `TestRestoreLoadRefusedWhileMarkerStands` (`pkg/funcd/workload_backup_internal_test.go:656`) |
| 5.1–5.3 | `TestReleaseNamespaceWaitsForRestore` (`internal/controlplane/hold_test.go:125`); `TestHoldReleaseNamespace` (`cmd/funcdctl/hold_test.go:72`) |
| 6 | `TestScenarioTargetInStoreRefused` (`pkg/funcd/workload_backup_internal_test.go:498`) |
| 7.1–7.6 | `TestScenarioPlatformFromObjectives`, `TestScenarioWriteKeepsTheRest`, `TestFlagRules`, `TestApplyKeepsBytes` (`cmd/funcdctl/backup_plan_test.go:142`, `:229`, `:331`, `:374`); `TestDeriveTable` (`internal/backup/plan/plan_test.go:28`) |

## Review checklist

- [ ] Every `file:line` above shows the cited text at ae8869a9 (`git show ae8869a9:<file> | sed -n <line>p`).
- [ ] Every header quote is in the cited ADR at ae8869a9; each **[changes]** item has a header entry and no other has.
- [ ] The Contracts block matches the code (struct fields joined on one line), and nothing in it is marked new.
- [ ] `scripts/agent/d go test -run 'TestReleaseNamespace|TestRestore|TestScenarioCatchUpOnceNoOverlap|TestNewestDueCollapses|TestResolveScopes|TestWriteReadBack|TestParse$|TestScenarioPlatformRestoreHoldsNamespace|TestEveryRunnerConsultsHoldInNamespace|TestHoldReleaseNamespace|TestScenarioTargetInStoreRefused|TestScenarioPlatformFromObjectives|TestScenarioWriteKeepsTheRest|TestFlagRules|TestApplyKeepsBytes|TestDeriveTable|TestScenarioRestoreInPlace' ./internal/platform/hold/ ./internal/controlplane/ ./internal/services/backup/ ./internal/backup/... ./pkg/funcd/ ./cmd/funcd/ ./cmd/funcdctl/` passes.
- [ ] `go.mod` has Decision 8's versions; acceptance edits only this file and the back-links; no username or absolute path.

## Consequences

- (+) ADR-0222 and ADR-0223 plus this ADR describe main; the `adr`-attributed review Minors of both are closed.
- (+) A half-written in-place load is never released silently: only `hold release --namespace` lifts its marker.
- (−) A reader of either ADR must follow its `Superseded in part by` link to see the current clause.
- (−) Left to a fix: after a failed lift of a complete load, the next in-place create is refused saying the load "did not
  complete" (`admission/backup.go:188-189`); no test pins 1.4, 5.1's `advance` beside `namespaces`, or 7.5's `<= 0`.
- (−) ADR-0222's "the implementation PR measures" the binary growth from SQLite was not done (#949's body has no figure).

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Where decided |
|---|---|---|
| Objects a stopped or crashed in-place load leaves in a Bucket or catalog (1.3) | keep: the marker fences them, the operator checks and empties before `hold release --namespace`; the alternative (the next start empties an `Interrupted` in-place Restore's targets) writes to a store with no operator present | a new ADR superseding 1.3 in part, if the start should empty |
| Cancelling a `Pending` in-place Restore (2.4) | no cancel verb; the round-3 review's lever is deleting the Backup it waits for, which fails it `BackupNotReady` (`restore.go:65-68`); document it in `examples/workload-backup.md` | a docs fix; a follow-up ADR if a cancel verb is wanted |

## References

- PRs [#949](https://github.com/pyvvo/funcd/pull/949) (ADR-0222), [#954](https://github.com/pyvvo/funcd/pull/954) (ADR-0223).
- ADRs [0000](0000-adr-process.md), [0147](0147-atomic-admission-and-nested-call-cap.md), [0170](0170-owner-garbage-collector.md),
  [0227](0227-disaster-recovery-implementation-addenda.md), the header's; reviews `docs/reviews/adr-022{2,3}-implementation-claude-opus-5-5*.md`.
