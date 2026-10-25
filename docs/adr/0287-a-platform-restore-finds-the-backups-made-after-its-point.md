# ADR-0287: A platform restore finds the Backups its schedules made after the restored point

- **Status**: Reviewing (2026-10-25); Accepted 2026-10-25 under the decider's delegation, after two judge rounds (cowork); Proposed 2026-10-25
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: backup, restore, disaster-recovery, workload
- **Realizes**: [FEAT-0009/F202](../feat/0009-feat-disaster-recovery.md) (a workload store's recovery point is its
  schedule's alone)
- **Answers**: the decider's choice 3A (refine brief, 2026-10-25): after a platform restore, funcd re-creates the
  Backups found on each BackupSchedule's target that are newer than the restored point, as Velero's backup sync does
- **Supersedes (in part)**: [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 4's "`Running`
  at start ⇒ `Failed` (`Interrupted`)": now only when the copy's manifest is not on the target (Decision 4)
- **Relates to**: [ADR-0206](0206-restore-and-held-boot.md) (`funcd restore run`, the held boot) ·
  [ADR-0184](0184-stateless-listing-pushdown.md) (`ListAfter`) · [ADR-0234](0234-sql-backup-and-restore.md) (SQL
  members) · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A Restore names a Backup record (`RestoreSpec.Backup`, `api/types/v1alpha1/backup.go:137`) and reads the copy at the
record's `status.target` and `status.path` (`internal/services/backup/restore.go:189-194`). `funcd restore run`
brings the metastore back to the platform generation's cut, so every Backup a schedule made after the cut is whole on
its target but has no record: the owner can only restore an older one. Experiment 0011 (a host loss rebuilt from its
backups) measured it: SQL's effective recovery point is its schedule's cadence plus the platform generation interval.

**Purpose**: after a platform restore, and before the owner restores, each BackupSchedule's records again show every
whole copy its target holds that is newer than the restored point, so a workload store's recovery point is its
schedule's alone. The callers are the app owner (and the runbook) creating a Restore while the namespace is held.

## Scenarios

- `scenario: restore-finds-newer-backups` — Given schedule `orders-sql` (`* * * * *`) whose Backups kept running on
  the lost host after the platform generation's cut, When the operator restores that generation and funcd boots held,
  Then each newer whole copy is a `Succeeded` Backup `orders-sql-<slot>` with Ready reason `FoundOnTarget`, the
  schedule's `TargetSynced` is True, and a Restore naming the newest loads it.
- `scenario: incomplete-copy-not-recorded` — Given a newer directory with no `manifest.yaml` (the host died mid-copy),
  When the schedule syncs, Then no record names it.
- `scenario: gone-backup-stays-gone` — Given a Backup record the owner deleted before the cut, one deleted after the
  sync, and a newer copy whose slot plus the schedule's ttl has passed, When the schedule syncs again, Then none of the
  three has a record.
- `scenario: only-own-slots` — Given a target shared by schedules `a` and `a-1` that also holds a Backup by hand and an
  export `<restore>-before`, When `a` syncs, Then only `a-<slot>` copies get records.
- `scenario: unreachable-target-retried` — Given the target refusing the listing after the restore, Then `TargetSynced`
  is False with reason `ListFailed` and the error, and the schedule takes no slot, even once `funcdctl hold release
  --namespace` has run; When the target is back, Then the next pass creates the records and the slots resume.
- `scenario: finished-run-kept` — Given a Backup `Running` when funcd stopped whose `manifest.yaml` is on the target
  (the cut fell during its run, or funcd died after the manifest), When funcd starts, Then it is `Succeeded` with
  reason `FoundOnTarget`, also when the target answers only after the restore's first listing; without the manifest
  it is `Failed` (`Interrupted`), as before.

## Scope

**In**: the sync after a platform restore (its trigger, what it lists, the records it makes, the schedule's status and
slots), and the start-up rule for a run whose copy finished. **Out**: Backups by hand or hook and exports, whose names
carry no slot; copies of schedules the restored metastore does not hold; a sync outside a platform restore; the
Restore itself (ADR-0222 Decision 7, unchanged); the platform backup (ADR-0202 to ADR-0206).

## Constraints & Decision drivers

- The schedule's credential puts and lists only (ADR-0222 Q8, `examples/workload-backup.md`): the sync gets nothing.
- A record must never claim a copy that is not whole. The manifest is written last (ADR-0222 Decision 4), so its key is
  the proof.
- A record that was deleted, by the owner or at its `expireTime`, must not come back: a delete drops the record only
  (ADR-0222 Decision 10), and the objects stay until the owner's lifecycle rule.
- The owner restores during the namespace's `data` hold (ADR-0222 Decision 8), and nothing stops an early release of
  that hold, so the sync must not depend on it.
- `funcd restore run` is offline and reads only the platform target (ADR-0206).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. `restore run` marks each schedule; its reconciler syncs it and takes no slot until a sync succeeds | in the daemon, which holds the Secrets and the targets' clients; survives an early release and a restart | one listing per pass until the target answers | chosen |
| B. `funcd restore run` syncs offline | records ready at the first boot | an operator tool would read each namespace's Secrets and reach every owner's target; one unreachable target fails the platform restore | rejected |
| C. The controller syncs after the namespace's release | no hold logic | the owner restores during the hold, before it | rejected |
| D. Every pass syncs every target (Velero's controller) | also heals records lost otherwise | re-creates records deleted or expired, since funcd keeps the objects on a delete (Velero deletes them); lists targets forever | rejected |
| E. Read each manifest for the record | recipients and the schedule from the copy | the schedule's credential cannot get (Q8); the slot name already gives schedule and slot | rejected |
| F. A Restore names a target path, not a record | no sync | the decider chose records (3A); the owner could not list what exists | rejected |
| G. Sync while the namespace's `data` marker stands (round 1) | no new state | an early `hold release --namespace` removes the marker before the sync and loses it for good, silently | rejected |
| H. Refuse the namespace release until every schedule there synced | keeps G | an operator step that a gone target blocks, so it needs an override flag; the schedule's own state already holds its slots | rejected |
| I. Every directory of the namespace, slot-named or not | Backups by hand and exports come back too | without a get, a listing cannot tell their schedule or time, and two schedules may share a target | rejected; open question |
| J. Keep a `Running` record `Interrupted` at start (ADR-0222) | no listing at start | when the cut falls during a run, the newest whole copy has no usable record, and the recovery point grows by one cadence | rejected |
| K. Take `startTime` and `recipients` from the manifest | exact values | needs a get (Q8) | rejected for the slot and empty recipients |

## Decision

1. **The mark.** `funcd restore run`, where it lists the BackupSchedules to hold their namespaces (ADR-0222 Decision 8),
   sets on each restored schedule `TargetSynced` False with reason `PlatformRestored`. The schedule's own status then
   carries the pending sync across restarts and releases (alternatives G, H). A schedule never so marked never syncs.
2. **When.** On each pass of a schedule whose `TargetSynced` is False with reason `PlatformRestored` or `ListFailed`,
   held platform or namespace or not, the reconciler syncs before its slot logic, and takes no slot until a sync
   succeeds: `lastScheduleTime` never passes a copy the sync has not seen.
3. **What it finds.** With the schedule's credential it lists the keys under `<ns>/<schedule>-` on `spec.target.url`
   after those of the slot `L`, the restored `status.lastScheduleTime` (unset ⇒ the schedule's creation), with
   `ListAfter` (ADR-0184). A directory `<backup>-<uid>/` counts when `<backup>` is `<schedule>-<slot in Unix minutes>`
   (`v1.BackupSlotName`), its slot is after `L`, and it holds `manifest.yaml`. Nothing is read but the listing: a slot
   name alone carries its schedule and its time, while a Backup by hand or an export carries neither (alternative I).
4. **The record.** For each one, oldest first, it creates Backup `<backup>` (spec `schedule`, the schedule's
   `resourceGroup`, no ownerReference) with status `Succeeded`, `startTime` the slot (never later than the run's
   start), `completionTime` the manifest's modification time, `expireTime` the slot plus the schedule's ttl (never later
   than the original's, so the record does not outlive the owner's lifecycle rule), `target` the URL, `path` the
   directory, `members` from the listing (kind directory, name, volume, bytes; a Lake's last attempt; for display, as a
   Restore matches the manifest's members, `restore.go:275-286`) and Ready True with reason `FoundOnTarget`. A copy
   whose `expireTime` has passed gets no record, a name already stored is left as it is (Conflict counts as done), and
   `recipients` stay empty: the manifest has them (alternative K). Then, in one status write, `lastScheduleTime`
   becomes the newest whole copy's slot found after `L` (stored names included) and `TargetSynced` True.
5. **A run the records lost.** Before the Backup reconciler fails a `Running` record it does not run as `Interrupted`,
   and before it copies a `Pending` one, it lists the record's own directory `<ns>/<backup>-<uid>/` on its schedule's
   target, whatever the record's name. With `manifest.yaml` there, the record becomes `Succeeded`, reason
   `FoundOnTarget`: `target` the schedule's URL, `path` that directory, `completionTime` the manifest's modification
   time, `members` as in Decision 4, `startTime` kept (its creation time when unset) and `expireTime` kept. A failed
   listing of a `Running` record requeues it while its schedule's `TargetSynced` is False (its slots wait anyway);
   otherwise, without the manifest, or when the schedule or its credential fails, the record goes on as before. The
   manifest, written last, is the proof that the copy is whole, whichever process wrote it (alternative J).
6. **Status.** `TargetSynced` (NEW condition on the schedule): False `PlatformRestored` from `restore run`; True after a
   sync, message "found <n> Backups, newest <name>"; False with reason `ListFailed` and the error, retried at the next
   pass (at most `Period`). Each created record logs Info `workload backup found on target` (`namespace`, `schedule`,
   `backup`); a failed listing logs Warn on turning False. The runbook waits for `TargetSynced` True before the owner
   restores; `rpoRisk` turns on as slots pass while the sync waits for the target (ADR-0222 Decision 9).

## Design model

- **Spec**: [`docs/design/backup/target-sync.fizz`](../design/backup/target-sync.fizz) · state machine
  [`target-sync.md`](../design/backup/target-sync.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoRecordWithoutCopy` | always | `incomplete-copy-not-recorded`, `finished-run-kept` | `TestScenarioIncompleteCopyNotRecorded` |
  | `GoneStaysGone` | always | `gone-backup-stays-gone` | `TestScenarioGoneBackupStaysGone` |
  | `SlotsAfterSync` | always | `unreachable-target-retried` | `TestScenarioUnreachableTargetRetried` |
  | `FinishedRunKept` | always | `finished-run-kept` | `TestScenarioFinishedRunKept` |
  | `NewerCopiesFound` | eventually always | `restore-finds-newer-backups` | `TestScenarioRestoreFindsNewerBackups` |
- **Bounds**: two newer slots, each copy whole, partial or absent; one older slot whose record was deleted; one run
  cut at the cut; one owner delete; one daemon crash; one slot after the restore; a release at any time; 2 concurrent
  actions, 13 actions, the whole state space (every action atomic and guarded: 11 reaches the same states).
- **Faults**: a daemon crash at any step (a pass restarts from the stored status); a target that refuses listings
  until it is back (fair); an early namespace release; crash on yield off.
- **Environment**: the owner deletes a record only once `TargetSynced` is True (a delete inside a pass a crash cuts
  could come back); a listing shows every key written before it; the lost host writes nothing after the restore.
- **Mutants**: `no-manifest-check` fails `NoRecordWithoutCopy`; `no-after-guard` fails `GoneStaysGone`;
  `slots-while-pending` (round 1's marker, released early) and `advance-first` fail `SlotsAfterSync`;
  `interrupted-always` and `interrupted-on-fail` fail `FinishedRunKept`.
- **Result**: pass, 1,848 states, 1.0 s, FizzBee v0.5.4; wider (three newer slots, 15 actions): pass, 6,712 states,
  2.7 s.
- **Left to tests**: the expireTime rule; the advanced `L`, which keeps the slot logic's catch-up off the copies'
  slots, so a record deleted after the sync does not run again under its name (`TestScenarioGoneBackupStaysGone`); the
  listing's key order and slot parse (`TestParseBackupSlotName`, `TestFindSlotsSkipsOtherSchedules`); a real S3
  listing (the S3 stub of ADR-0203).

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1
const ConditionTargetSynced = ConditionType("TargetSynced")
const ReasonPlatformRestored, ReasonListFailed, ReasonFoundOnTarget = "PlatformRestored", "ListFailed", "FoundOnTarget"

func ParseBackupSlotName(schedule, name ObjectName) (time.Time, bool) // the inverse of BackupSlotName

// internal/backup/workload (NEW)
// Copy is one Backup directory as a listing shows it (list rights only).
type Copy struct {
	Name      v1.ObjectName // <backup>
	UID       v1.UID
	Path      string // <ns>/<backup>-<uid>/
	Whole     bool   // manifest.yaml is there
	Completed time.Time
	Members   []v1.BackupMember
}

func ListCopy(ctx context.Context, target blob.Bucket, path string) (Copy, error)
// SlotCopies lists schedule's slot copies under ns whose slot is after `after`, oldest first; whole ones and not.
func SlotCopies(ctx context.Context, target blob.Bucket, ns v1.NamespaceName, schedule v1.ObjectName, after time.Time) ([]Copy, error)
```

| Consumes | Exposes |
|---|---|
| `restore run`'s list of BackupSchedules (`internal/restore/run.go`, `holdNamespaces`); the schedule's target credential; `blob.Bucket.ListAfter` (ADR-0184); the layout of ADR-0222 Decision 4 | condition `TargetSynced`, reasons `PlatformRestored`, `ListFailed`, `FoundOnTarget`; log lines above |

## Implementation plan

1. `api/types/v1alpha1/backup.go`: the condition and reasons; `ParseBackupSlotName` (8 digits of Unix minutes, as
   `maxBackupScheduleNameLength`).
2. `internal/restore/run.go`: `holdNamespaces` sets `TargetSynced` False `PlatformRestored` on each schedule it lists.
3. `internal/backup/workload/copies.go` (NEW): `ListCopy`, `SlotCopies`; the directory parse (`<name>-<32 hex>`) and
   the members from the kind directories of ADR-0222 Decision 4 (`kv/`, `blob/`, `lake/<name>/<attempt>/`, `sql/`,
   `volume/<fn>/<name>/`).
4. `internal/services/backup/schedule.go`: the sync in `Reconcile` before the slot logic, the slots held while it is
   pending; `run.go`: Decision 5 in `Reconcile` (`Running` not run here) and `copy` (`Pending`).
5. `examples/restore-runbook.md` and `examples/workload-backup.md`: wait for `TargetSynced` True, then restore.
6. Tests:
   - `pkg/funcd/workload_backup_internal_test.go` (the S3 stub, a manual clock, `shortDataDir`): a test per scenario,
     `TestScenarioRestoreFindsNewerBackups` through `TestScenarioFinishedRunKept`, each with its oracle marker; the
     existing `TestScenarioRunInterrupted` keeps passing;
   - units: `TestParseBackupSlotName` (round trip; `a-1-<n>` is `a-1`'s, not `a`'s), `TestFindSlotsSkipsOtherSchedules`,
     `TestListCopyMembers`, and in `internal/restore` the mark on each schedule.
7. At acceptance (done): ADR-0222's `Superseded in part by` back-link; FEAT-0009 F202 → accepted; the blueprint's DR
   bullet.
8. Done when the touched packages' tests (`-race`), vet, lint and `just check-specs` pass.

## Review checklist

- [ ] `restore run` marks every restored schedule; only a marked schedule syncs, with list calls only, before its slot
  logic, and it takes no slot until a sync succeeds.
- [ ] A record is made only for a `<schedule>-<slot>` directory after `L` that holds `manifest.yaml`, never for an
  expired one; `lastScheduleTime` and `TargetSynced` are written after the records, in one status write.
- [ ] A `Running` record not run here, or a `Pending` one, whose directory holds `manifest.yaml` becomes `Succeeded`,
  keeping its `expireTime`; a failed listing of a `Running` one requeues while its schedule's `TargetSynced` is False.
- [ ] No object is read; recipients stay empty; no Secret in status or logs.
- [ ] The scenario tests carry their oracle markers and pass; `just check-specs` passes with every mutant failing.

## Consequences

- **(+)** After a platform restore, a workload store's recovery point is its schedule's cadence alone, whenever the
  namespace is released.
- **(+)** A run that finished its copy but not its record is no longer reported `Interrupted`.
- **(−)** Backups by hand or hook, exports and copies of schedules the restored metastore lacks stay on the target
  without a record until the owner's lifecycle rule removes them: a Restore cannot name them.
- **(−)** A re-created record's ttl is its schedule's, so a Backup by API with a shorter `spec.ttl` is not one of them,
  and its `recipients` are empty until a Restore reads the manifest.
- **(−)** A schedule whose target never answers again takes no slot until its owner points `spec.target.url` at a target
  that answers or deletes the schedule; `rpoRisk` says so. Each pass meanwhile lists the target once.
- **(−)** A record the owner deletes during a sync, before its status write, comes back once if the daemon crashes in
  between: the runbook deletes nothing before `TargetSynced` is True.
- **Risk**: a Backup by API named like another schedule's slot, on a shared target, would be taken for that slot.

## Open questions

- Copies of a schedule the restored metastore does not hold, and Backups by hand: a later ADR, if owners need them
  (a manifest that names its schedule, read with the Restore's credential).

## References

[#1278](https://github.com/pyvvo/funcd/issues/1278) (F202, under #1276) · experiment 0011 (pyvvo/experiment, branch
exp/host-loss, private) · Velero, "How Velero works", backup sync (velero.io/docs/v1.15, read 2026-10-25) ·
`api/types/v1alpha1/backup.go` · `internal/services/backup/{schedule,run,restore}.go` ·
`internal/backup/workload/workload.go` · `internal/platform/hold/namespace.go` · `internal/restore/run.go`
(`holdNamespaces`) · `internal/controlplane/hold.go` (`ReleaseNamespaces`) · ADR-0184 · ADR-0206 · ADR-0222 · ADR-0234 ·
ADR-0272
