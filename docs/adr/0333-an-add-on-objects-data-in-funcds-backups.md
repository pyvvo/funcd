# ADR-0333: An add-on object's data is in funcd's backups, copied from its engine's own snapshots

- **Status**: Proposed
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, backup, restore, storage
- **Realizes**: [FEAT-0018/F259](../feat/0018-feat-addons.md) (an add-on's data in funcd's backups)
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon kind),
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (workload backups),
  [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md) (member kinds),
  [ADR-0234](0234-sql-backup-and-restore.md) (a file in records), [ADR-0231](0231-durable-storage-for-add-on-engines.md)
  (engine storage), [ADR-0277](0277-a-containerd-worker-gets-one-stop-grace.md) (the stop grace),
  [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md) (held calls)

## Context & Need

An add-on object's data lives in its engine's storage (ADR-0231), which no BackupSchedule copies: an identity provider's
users and clients would be lost with the host. funcd cannot read an engine it did not build, but many engines write
their own consistent copies: rauthy's hiqlite writes one at each `HQL_BACKUP_CRON` tick and restores one named by
`HQL_BACKUP_RESTORE` at its start. This ADR copies those files into funcd's backups and puts one back for the
engine's next start, so an add-on object is a backup member as a Lake is. Its callers: BackupSchedules, Backups and
Restores (ADR-0222), and the add-on reconciler (ADR-0332).

## Scenarios

- `scenario: addon-backup-member`. *Given* the IdentityProvider `login` and a BackupSchedule with `scope.addons:
  [login]`, *When* a Backup runs, *Then* `status.members` lists `IdentityProvider/login` with its bytes: the newest
  snapshot, copied as it is.
- `scenario: addon-backup-no-snapshot`. *Given* an engine that has written no snapshot yet, *When* a Backup runs,
  *Then* it fails with reason `NoSnapshot`.
- `scenario: addon-restore-in-place`. *Given* a client registered in rauthy before the Backup and one after it, *When*
  an in-place Restore of `login` runs, *Then* it succeeds once rauthy answers again without `restore.env`, the first
  client exists, the second does not, and `restore.path` is empty.
- `scenario: addon-restore-no-rollback`. *Given* that Restore, *When* the engine crashes after loading the copy,
  *Then* no write it acknowledged after the Restore succeeded is lost at its next start.
- `scenario: addon-restore-in-place-only`. *When* a Restore member of kind `IdentityProvider` sets `as`, *Then* it is
  refused 422.
- `scenario: addon-deletion`. *Given* `deletion: delete`, *When* the object is deleted, *Then* its storage is removed;
  with the default `retain`, a new object of the same name starts on the same data.

## Scope

In: the Addon's `snapshot` and `restore`; the backup member of an add-on object; an in-place Restore of one; the
object's `deletion`. Out: an engine that writes no copies of its own (it has no member until it does), a Restore into
another object, copies of more than one file per member, and the dev runtime's file access
([ADR-0334](0334-funcdctl-dev-runs-add-on-images.md)).

## Constraints & Decision drivers

- funcd never opens an engine's files: it copies what the engine wrote, as it copies a Lake's catalog (ADR-0271).
- Backups do not stop the engine; a Restore may restart it.
- A write the engine acknowledged after a Restore must survive any later start.
- The member kind is the provider's (ADR-0311), and funcd's set of kinds stays fixed.

## Alternatives considered

- **Stop the engine and copy its storage directory**, as a volume is copied. Rejected: a Backup would take the
  engine down each time, and a directory copied under a running SQLite is torn.
- **A backup endpoint the Addon declares** (HTTP, the engine streams its copy). More general, but rauthy has none and
  each add-on would need one built; the engine's own files cover rauthy now. It stays open for a later engine.
- **Restore by writing the copy over the engine's data while it is stopped**, as the Lake does. Rejected: funcd would
  have to know the engine's data layout; the engine's own restore at its start does not.
- **Restore into another object** (`as`). Rejected for now: funcd cannot see whether another engine holds data, and
  the restore replaces it whole.

## Decision

1. **The Addon declares its copies**: `snapshot.path`, the folder where the engine writes them, and `restore.path`
   with `restore.env`, the folder funcd puts a copy in and the environment of the start that loads it. Both lie under
   `storage.path`, and neither inside the other: hiqlite copies its restore file into its snapshot folder and truncates
   it when both are the same file. `restore.env` values may use `${{ restore.file }}`, the copy's path.
2. **A member per object.** `BackupScope.addons` names add-on objects, and a `resourceGroup` scope takes its add-on
   objects too. Each member is `addon/<kind>/<name>/<file>/`: the newest regular file of `snapshot.path` whose name does
   not end in `~` and that has not changed for `addonSnapshotQuiet` (2 s; hiqlite rewrites a copy's metadata just after
   naming it), copied unopened in records of 8 MiB (ADR-0234). No such file fails the Backup with `NoSnapshot`. One
   member kind serves every add-on's kind; the backup service falls back to it for a kind funcd does not define.
3. **Restore in place only.** A member of an add-on's kind takes `inPlace`, never `as`. The Restore's load empties
   `restore.path`, writes the copy there owned by the storage's uid and gid, and sets `status.restore` to `{file,
   state: Loading}`. It then waits, under the namespace hold (ADR-0222, ADR-0290), until the object's `status.restore`
   is gone, at most `addonRestoreTimeout` (10 min); past it the Restore fails `RestoreTimedOut` and the hold stays.
4. **The reconciler's part.** While `status.restore` is `Loading`, it starts the engine with `restore.env`. Once that
   engine is Ready, it removes the copy, then sets `Loaded`. While `Loaded`, it starts the engine without
   `restore.env`, and clears `status.restore` once that engine is Ready. It replaces an engine with ADR-0277's SIGTERM
   and grace, so hiqlite drops its crash marker first. A failed Restore's clear removes the copy and `status.restore`.
5. **Deletion.** `spec.deletion` is `retain` (the default: the storage stays for a new object of the same name) or
   `delete` (the storage is reclaimed once the engine is gone, ADR-0231).
6. **File access.** The add-on runtime reads and writes an engine's files under its storage directory: on the host
   for containerd (ADR-0231's directory), through Docker's archive API under `funcdctl dev` (ADR-0334).

## Design model

- **Spec**: [`docs/design/addon/restore-start.fizz`](../design/addon/restore-start.fizz) · state machine
  [`restore-start.md`](../design/addon/restore-start.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoAckedWriteRolledBack` | always | `scenario: addon-restore-no-rollback` | `TestScenarioAddonRestoreNoRollback` |
  | `TheCopyIsLoaded` | always | `scenario: addon-restore-in-place` | `TestScenarioAddonRestoreInPlace` |
  | `TheHoldLifts` | always eventually | `scenario: addon-restore-in-place` | `TestScenarioAddonRestoreInPlace` |
- **Bounds**: one crash, 16 actions, one concurrent action: one object's passes run one at a time, and one crash
  after the load is the smallest run in which `lift-early` rolls a write back.
- **Faults**: an engine crash at any step; crash on yield off (every step is atomic).
- **Environment**: the runtime's replacement (SIGTERM, grace, then a start from the current spec) is modeled as
  `Replace` and `Start`; the edge's refusal of held calls is the guard on `Write`.
- **Mutants**: `lift-early` (the hold lifts once the copy is loaded) fails `NoAckedWriteRolledBack`; `clear-first`
  (the copy is removed before the engine that loads it started) fails `TheCopyIsLoaded`; `never-clear` fails
  liveness.
- **Result**: pass, 32 states, 0.3 s, FizzBee v0.5.4.
- **Left to tests**: hiqlite's restore at its start and its crash marker (the lane case `addon-rauthy-restore` on
  containerd); funcd stopping during the Restore (ADR-0222's `Interrupted`, unchanged).

## Behavior rows

- **disaster-recovery.md**: "A Backup copies an add-on object" (the newest quiet snapshot, unopened; `NoSnapshot`),
  `decided`; "An in-place Restore of an add-on object" (two engine starts under the hold, `RestoreTimedOut` after
  `addonRestoreTimeout`, the hold kept), `decided`; "An add-on object restores only in place", `decided`.
- **addons.md** (ADR-0332's chapter): "Deleting an add-on object" (`retain` keeps its storage, `delete` reclaims it),
  `decided`; "A crash before a restore start" (hiqlite cannot remove its crash marker; the Restore times out and its
  clear restarts the engine), `accepted`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1

type AddonSnapshot struct {
	Path string `json:"path"`
}

type AddonRestore struct {
	Path string            `json:"path"`
	Env  map[string]string `json:"env,omitempty"`
}

type BackupScope struct {
	// ...the existing fields
	Addons []ObjectName `json:"addons,omitempty"`
}

type AddonObjectStatus struct {
	Status  `json:",inline"`
	Address string             `json:"address,omitempty"`
	Restore *AddonRestoreState `json:"restore,omitempty"`
}

type AddonRestoreState struct {
	File  string `json:"file"`
	State string `json:"state"` // Loading or Loaded
}
```

```go
package workload // internal/backup/workload/addons.go

// AddonFiles reads and places an add-on engine's files under its storage. Newest is NotFound when dir holds no
// quiet file.
type AddonFiles interface {
	Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (file string, r io.ReadCloser, err error)
	Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, r io.Reader, uid, gid int) error
	Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error
}

func Addons(st store.Store, files AddonFiles) MemberKind
func Serves(k MemberKind, kind v1.Kind) bool
```

| Consumes | Exposes |
|---|---|
| the engine's files under its storage (ADR-0231), the namespace hold (ADR-0222) | `BackupScope.addons`, member `addon/<kind>/<name>/<file>/` |
| `restore.env`'s `${{ restore.file }}` (ADR-0095) | reasons `NoSnapshot`, `RestoreTimedOut`; `status.restore`; `addonSnapshotQuiet` 2 s, `addonRestoreTimeout` 10 min |

## Implementation plan

- `api/types/v1alpha1/backup.go` (`addons`, the in-place rule), `addon.go` (`snapshot`, `restore`, their paths),
  `internal/backup/workload/addons.go`, `internal/services/backup` (the fallback), `internal/services/addon` (the
  restore states), `internal/provider` (`AddonFiles` on the engine runtime's storage directory),
  `pkg/funcd/addon_provider.go` (`BackupKinds`), from experiment 0025's spike.
- Tests, one per Scenario, named after it, with a fake engine that writes and restores a file; the oracle tests carry
  `// oracle: docs/design/addon/restore-start.fizz#<Assertion>`. Unit tests for the quiet rule, the path rules and the
  state moves.
- Real platform: the lane case `addon-rauthy-restore` on containerd backs up rauthy, registers a client, restores in
  place, and finds the client gone and the earlier one kept.
- Done: `just ci`, `just check-specs` and `just check-behavior` green, the lane case passes.

## Review checklist

- [ ] A Backup copies the newest quiet file unopened, and fails `NoSnapshot` without one.
- [ ] `snapshot.path` and `restore.path` lie under `storage.path`, apart.
- [ ] A Restore member with `as` is refused 422.
- [ ] The hold lasts until an engine without `restore.env` is Ready; the copy is removed before `Loaded`.
- [ ] Each oracle marker names a passing test.
- [ ] `deletion: delete` reclaims the storage; `retain` keeps it.

## Consequences

- A Backup is as old as the engine's newest snapshot: it misses the writes of up to one snapshot interval, which the
  Addon sets (rauthy's 15 minutes, hiqlite's floor 60 s).
- An in-place Restore restarts the engine twice, about 9 s for rauthy (experiment 0025, `restore-engine-back`).
- Risk accepted: an engine that crashed right before a restore start cannot restore over hiqlite's crash marker
  (`remove_dir_all` on a file, hiqlite 0.15.3); the Restore times out, its clear restarts the engine normally, and
  the operator runs it again. An upstream fix is a one-line change in hiqlite.

## Open questions

- **An HTTP backup endpoint** for engines that write no files: a follow-up when such an add-on arrives.

## References

- Feature issue [#1499](https://github.com/pyvvo/funcd/issues/1499) (FEAT-0018 F259).
- Experiment 0025 (pyvvo/experiment at a0643b3c, `0025-addons-rauthy`): `backup`, `restore`, `restore-engine-back`,
  the check "the restore brings rauthy back to the backup", and the hiqlite findings.
- The decider's card 3 (2026-11-19).
