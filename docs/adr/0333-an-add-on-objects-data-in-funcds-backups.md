# ADR-0333: An add-on object's data is in funcd's backups, copied from its engine's own snapshots

- **Status**: Accepted (2026-11-19), by the decider's standing go-ahead for FEAT-0018 ("go ahead, don't wait for
  me", "until we ship entirely the new feature"), after three rounds of an independent judge (a subagent), every
  Blocker and Major folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, backup, restore, storage
- **Realizes**: [FEAT-0018/F259](../feat/0018-feat-addons.md) (an add-on's data in funcd's backups)
- **Supersedes (in part)**, back-linked at acceptance (ADR-0272 Decision 7):
  [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md)'s `Kind()` contract, Decision 3's
  dispatch by `Kind()` and Decision 6's unserved kind, for kinds funcd does not define: one member kind serves every
  add-on's kind through `Serves` (Decision 2)
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon kind) · ADR-0222 ·
  ADR-0228 · ADR-0231 · ADR-0234 · ADR-0277 · ADR-0288 · ADR-0290 · ADR-0307

## Context & Need

An add-on object's data lives in its engine's storage (ADR-0231), which no BackupSchedule copies: an identity provider's
users and clients would be lost with the host. funcd cannot read an engine it did not build, but many engines write
their own consistent copies: rauthy's hiqlite writes one at each `HQL_BACKUP_CRON` tick and restores one named by
`HQL_BACKUP_RESTORE` at its start. This ADR copies those files into funcd's backups and puts one back for the
engine's next start, so an add-on object is a backup member as a Lake is. ADR-0311 gives each member kind one kind;
an add-on's kind is added at run time (ADR-0332), so one member kind serves them all. Its callers: BackupSchedules,
Backups and Restores (ADR-0222), and the add-on reconciler (ADR-0332).

## Scenarios

- `scenario: addon-backup-member`. *Given* the IdentityProvider `login` and a BackupSchedule whose `scope.addons`
  names it, *When* a Backup runs, *Then* `status.members` lists `IdentityProvider/login` with its bytes: the newest
  snapshot, copied as it is.
- `scenario: addon-backup-no-snapshot`. *Given* an engine that has written no snapshot yet, *When* a Backup runs,
  *Then* it fails with reason `NoSnapshot`.
- `scenario: addon-backup-group-scope`. *Given* a resource group with `login` and the EchoService `echo`, whose Addon
  declares no `snapshot`, *When* a Backup of a schedule scoped to the group runs, *Then* it copies `login` only;
  *When* a schedule names `echo` in `scope.addons`, *Then* its Backup fails `NoSnapshot`, saying that the add-on
  declares no `snapshot.path`.
- `scenario: addon-restore-in-place`. *Given* a client registered in rauthy before the Backup and one after it, *When*
  an in-place Restore of `login` runs, *Then* it succeeds once rauthy answers again without `restore.env`, the first
  client exists, the second does not, and `restore.path` is empty.
- `scenario: addon-restore-no-rollback`. *Given* that Restore, *When* the engine crashes after loading the copy,
  *Then* no write it acknowledged after the Restore succeeded is lost at its next start.
- `scenario: addon-restore-timeout`. *Given* an Addon with `restore.timeout: 2s` whose engine never turns Ready with
  `restore.env`, *When* an in-place Restore runs, *Then* it fails `RestoreTimedOut`, `restore.path` is empty,
  `status.restore` is gone, the engine runs without `restore.env`, and the namespace stays held.
- `scenario: addon-restore-interrupted`. *Given* an in-place Restore whose engine runs with `restore.env`, *When*
  funcd is killed and starts again, *Then* the Restore is `Failed` (`Interrupted`), `restore.path` is empty,
  `status.restore` is gone, the engine runs without `restore.env`, and the namespace stays held.
- `scenario: addon-restore-in-place-only`. *When* a Restore member of kind `IdentityProvider` sets `as`, *Then* it is
  refused 422.
- `scenario: addon-deletion`. *Given* `deletion: delete`, *When* the object is deleted, *Then* its storage is gone.
  *Given* the default `retain`, *When* it is deleted, *Then* `funcdctl hold status` lists its storage in the
  quarantine, and a new object of that name starts empty.

## Scope

In: the Addon's `snapshot` and `restore`; the backup member of an add-on object; an in-place Restore of one; the
object's `deletion`. Out: an engine that writes no copies of its own (its Addon declares no `snapshot`, so its objects
are no members), a Restore into another object, copies of more than one file per member, and the dev runtime's file
access ([ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 4).

## Constraints & Decision drivers

- funcd never opens an engine's files: it copies what the engine wrote, as it copies a Lake's catalog (ADR-0271).
- Backups do not stop the engine; a Restore may restart it.
- A write the engine acknowledged after a Restore must survive any later start.
- The member kind is the provider's (ADR-0311), and funcd's set of kinds stays fixed (ADR-0332).

## Alternatives considered

- **Stop the engine and copy its storage directory**, as a volume is copied. Rejected: a Backup would take the
  engine down each time, and a directory copied under a running SQLite is torn.
- **A backup endpoint the Addon declares** (HTTP, the engine streams its copy). More general, but rauthy has none and
  each add-on would need one built; the engine's own files cover rauthy now. It stays open for a later engine.
- **Restore by writing the copy over the engine's data while it is stopped**, as the Lake does. Rejected: funcd would
  have to know the engine's data layout; the engine's own restore at its start does not.
- **Restore into another object** (`as`). Rejected for now: funcd cannot see whether another engine holds data, and
  the restore replaces it whole.
- **One member kind per add-on kind.** Rejected: the backup service takes its kinds once, at start (ADR-0311
  Decision 2), and Addons come and go.
- **`retain` keeps the storage in place for a new object of the same name.** Rejected: the boot reclaim quarantines
  it (ADR-0231, ADR-0288), and one name is one add-on object across add-on kinds (ADR-0332).
- **A `funcdctl quarantine return` verb for add-on objects.** Rejected, as ADR-0288's alternative G is for SQL
  directories: a directory is swapped only while its engine is stopped, which the runbook does with funcd stopped.

## Decision

1. **The Addon declares its copies**: `snapshot.path`, the folder where the engine writes them, and `restore.path`
   with `restore.env`, the folder funcd puts a copy in and the environment of the start that loads it. Both lie under
   `storage.path`, and neither inside the other: hiqlite copies its restore file into its snapshot folder and truncates
   it when both are the same file. `restore.env` values may use `${{ restore.file }}`, the copy's path.
   `restore.timeout` (ADR-0194, 1 s to 1 h, 10 min unset) bounds a Restore's wait: an engine's start time is its own.
2. **A member per object.** `BackupScope.addons` names add-on objects; a `resourceGroup` scope takes the group's
   add-on objects whose Addon declares `snapshot`. Each member is `addon/<kind>/<name>/<file>/`: the newest regular
   file of `snapshot.path` whose name does not end in `~` and that has not changed for `addonSnapshotQuiet` (2 s;
   hiqlite rewrites a copy's metadata just after naming it), copied unopened in records of 8 MiB (ADR-0234). No such
   file fails the Backup with `NoSnapshot`, and so does a named object whose Addon declares no `snapshot`, the message
   saying so: members resolve at each run, with no link check at apply (ADR-0222 Decision 2). One member kind,
   `Addons`, whose `Kind()` is `AddonObject`, serves every add-on's kind: `Write` and a Restore's plan find a member's
   kind with `Serves`, not `Kind() ==`, and `Serves` gives `Addons` every kind funcd does not define. A Restore member
   of such a kind that names no add-on object of that kind fails `MemberNotFound`; a funcd kind that no member kind
   serves still fails by its name.
3. **Restore in place only.** A Restore member may name a kind funcd does not define that matches an Addon's kind
   pattern (ADR-0332 Decision 1); it takes `inPlace`, and the API refuses `as`, 422. The Restore's load empties
   `restore.path`, writes the copy there owned by the storage's uid and gid, and sets `status.restore` to its own
   name, the copy's file and the state `Loading`. It then waits, under the namespace hold (ADR-0222, ADR-0290), until
   the object's `status.restore` is gone, at most `restore.timeout`; past it, the Restore fails `RestoreTimedOut`, and
   the hold stays.
4. **The reconciler's part.** While `status.restore` is `Loading`, it starts the engine with `restore.env`. It removes
   the copy only once the engine started with `restore.env` is Ready, and sets `Loaded` only once the copy is gone,
   whatever the engine does then. While `Loaded`, it starts the engine without `restore.env`, and clears
   `status.restore` once that engine is Ready. It replaces an engine with ADR-0277's SIGTERM and grace, so hiqlite
   drops its crash marker first. Once the Restore that `status.restore` names has ended `Failed` (`RestoreTimedOut`,
   `Interrupted` or another reason) or is gone, it removes the copy, then `status.restore`, and starts the engine
   without `restore.env` under the kept hold (ADR-0222, ADR-0228 Decision 1.3): after funcd's crash, the boot sweep's
   kill leaves hiqlite's crash marker, which a restore start cannot pass. A failed Restore's own clear makes the same
   two removals.
5. **Deletion.** `spec.deletion` is `retain`, the default, or `delete`. An object's storage is
   `<dataDir>/providers/addonobject/<ns>/<name>`, a path named after the stored kind (ADR-0231 Decision 2, ADR-0332
   Decision 2). Once a deleted object's engine is gone, `delete` reclaims it at once (ADR-0231 Decision 3), and
   `retain` moves it into ADR-0288's quarantine in one rename, as a SQL directory is moved, to
   `<dataDir>/providers/.quarantine/addonobject/<ns>/<name>/<since>`; `hold status` lists the entry, and the sweep
   deletes it after `storage.quarantineRetention` (168 h). At boot, the add-on provider's `Reclaim` (ADR-0307)
   quarantines the storage of every add-on object no longer stored, whatever its `deletion` was, since no start-up
   path deletes data (ADR-0288 Decision 1). So a new object of that name, of any add-on, starts empty (ADR-0288's
   `same-name-starts-empty`). None of these moves runs while the platform is held (ADR-0231 Decision 4). The storage
   comes back by hand, as a SQL directory does (ADR-0288 Decision 6): apply the object again and wait for Ready, stop
   funcd, replace its storage directory with the entry, then start funcd (the runbook).
6. **File access.** The add-on runtime reads and writes an engine's files on the host, under its storage directory
   (ADR-0231). Every file operation runs inside an `os.Root` opened on that directory, as `internal/volume` does, and
   a snapshot's or a copy's name is one path segment matching `^[A-Za-z0-9][A-Za-z0-9._-]*$`. Under `funcdctl dev`,
   ADR-0334 Decision 4 decides it.

## Design model

- **Spec**: [`docs/design/addon/restore-start.fizz`](../design/addon/restore-start.fizz) · state machine
  [`restore-start.md`](../design/addon/restore-start.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoAckedWriteRolledBack` | always | `scenario: addon-restore-no-rollback` | `TestScenarioAddonRestoreNoRollback` |
  | `TheCopyIsLoaded` | always | `scenario: addon-restore-in-place` | `TestScenarioAddonRestoreInPlace` |
  | `TheRestoreEnds` | always eventually | `scenario: addon-restore-in-place` | `TestScenarioAddonRestoreInPlace` |
  | `TheEngineRunsWithoutRestoreEnv` | always eventually | `scenario: addon-restore-interrupted` | `TestScenarioAddonRestoreInterrupted` |
- **Bounds**: one engine crash, one early end of the Restore, one concurrent action (one object's passes run one at a
  time), 13 actions: the smallest bound at which the spec passes (ten restore steps, a write, a crash, its restart)
  and every mutant fails.
- **Faults**: an engine crash at any step, leaving no crash marker (an engine that restores after it could roll a
  write back); funcd's crash while the object is marked: the next start ends the Restore `Interrupted` and its boot
  sweep kills the engine, leaving hiqlite's marker; the wait passing `restore.timeout`; crash on yield off. An end
  before the mark leaves only a copy that no start reads and the next load empties.
- **Environment**: the runtime's replacement (SIGTERM, grace, a start from the current spec) is `Replace` and `Start`;
  the edge's refusal of held calls is the guard on `Write`. funcd's own calls at the engine's address (ADR-0335's
  discovery, tokens and keys) are not held, and ADR-0336's registrar does nothing while the namespace is held: a write
  such a call makes during the hold can be rolled back by a crash before the copy's removal.
- **Mutants**: `lift-early` (the hold lifts once the copy is loaded) fails `NoAckedWriteRolledBack`; `clear-first`
  (the copy goes before the engine that loads it starts) and `loaded-first` (`Loaded` before the copy goes) fail
  `TheCopyIsLoaded`; `never-clear` (the mark outlives the load) and `keep-mark` (it outlives its ended Restore) fail
  liveness.
- **Result**: pass, 69 states, 0.3 s, FizzBee v0.5.4; wider (two engine crashes, 24 actions): pass, 109 states,
  0.3 s, every mutant failing as named.
- **Left to tests**: hiqlite's restore at its start after a graceful replace, within the default `restore.timeout`,
  and its normal start over its crash marker after funcd's kill, which `TheEngineRunsWithoutRestoreEnv` assumes (the
  lane case `addon-rauthy-restore` on containerd, its two steps).

## Behavior rows

- **disaster-recovery.md**: added, `decided`: "A Backup copies an add-on object" (the newest quiet snapshot, unopened;
  a group takes only objects whose Addon declares `snapshot`; `NoSnapshot`, also for a named object without one),
  "An in-place Restore of an add-on object" (two engine starts under the hold; `RestoreTimedOut` after
  `restore.timeout`, 10 min unset; the hold kept), "An add-on object restores only in place". Changed: "A Backup fails"
  (`:43`) gains `NoSnapshot`; "funcd stops or crashes during a load" (`:53`): an add-on object's copy and
  `status.restore` go, and its engine starts without `restore.env` under the hold.
- **addons.md** (ADR-0332's chapter): "Deleting an add-on object" (`delete` reclaims its storage at once; `retain`
  quarantines it for `storage.quarantineRetention` 168 h, and it comes back by hand with funcd stopped; a start not
  held quarantines the storage of an object no longer stored; a new object of that name starts empty), `decided`;
  "A crash before a restore start" (hiqlite cannot remove its crash marker; the Restore times out and its clear
  restarts the engine), `accepted`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1

type AddonSnapshot struct{ Path string `json:"path"` }

type AddonRestore struct {
	Path    string            `json:"path"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout Duration          `json:"timeout,omitempty"` // 1s to 1h; unset is 10m
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
	Restore ObjectName `json:"restore"` // the Restore that placed the copy
	File    string     `json:"file"`
	State   string     `json:"state"` // Loading or Loaded
}
```

```go
package workload // internal/backup/workload/addons.go

// AddonFiles reads and places an add-on engine's files on the host. dir is relative to the Addon's storage.path; every
// call runs inside an os.Root opened on the engine's storage directory, and file is one path segment matching
// ^[A-Za-z0-9][A-Za-z0-9._-]*$. Newest is NotFound when dir holds no quiet file.
type AddonFiles interface {
	Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (file string, r io.ReadCloser, err error)
	Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, r io.Reader, uid, gid int) error
	Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error
}

// Addons is the one MemberKind of every add-on object, with files addon/<kind>/<name>/<file>/. Its Kind() is
// AddonObject and its members carry their add-on's kind: for Addons, Kind() is the kind Serves matches, not ADR-0311's
// "the kind its members carry".
func Addons(st store.Store, files AddonFiles) MemberKind

// Serves reports whether k serves members of kind: k.Kind() == kind, or, for Addons, any kind funcd does not define.
// Write and a Restore's plan find a member's kind through it.
func Serves(k MemberKind, kind v1.Kind) bool
```

| Consumes | Exposes |
|---|---|
| the engine's files under its storage (ADR-0231), the namespace hold (ADR-0222), the quarantine (ADR-0288) | `BackupScope.addons`, `restore.timeout`, member `addon/<kind>/<name>/<file>/`; quarantine entries in `hold status` |
| `restore.env`'s `${{ restore.file }}` (ADR-0095) | reasons `NoSnapshot`, `RestoreTimedOut`; `status.restore`; `addonSnapshotQuiet` 2 s |

## Implementation plan

- `api/types/v1alpha1` (`backup.go`: `addons`, the in-place rule; `addon.go`: `snapshot`, `restore`),
  `internal/backup/workload/addons.go`, `internal/services/backup` (`Serves` in the plan), `internal/services/addon`
  (the restore states and clears, the deletion, the boot's quarantine of objects no longer stored),
  `internal/provider` (`AddonFiles`), `pkg/funcd/addon_provider.go` (`BackupKinds`, `Reclaim` as ADR-0307's
  `lifecycle.Reclaimer`, the add-on quarantine in `quarantineStores`), `examples/restore-runbook.md` (an add-on
  object's way back, beside a SQL database's), from experiment 0025's spike.
- Tests, one per Scenario, named after it, with a fake engine that writes and restores a file and a short
  `restore.timeout`; the oracle tests carry `// oracle: docs/design/addon/restore-start.fizz#<Assertion>`. Unit tests
  for the quiet rule, the path and name rules, and the state moves. Real platform: the lane case `addon-rauthy-restore`
  on containerd backs up rauthy, registers a client, restores in place, and finds the client gone and the earlier one
  kept; then it kills funcd while the engine starts with `restore.env` and finds the Restore `Failed` (`Interrupted`),
  the hold kept and the engine running without `restore.env` after the clear, as app-todo B12 does for a load.
- Done: `just ci`, `just check-specs` and `just check-behavior` green, the lane case passes.

## Review checklist

- [ ] A Backup copies the newest quiet file unopened, fails `NoSnapshot` without one, and takes from a group only the
  objects whose Addon declares `snapshot`; a named object whose Addon declares none fails it `NoSnapshot`.
- [ ] `snapshot.path` and `restore.path` lie under `storage.path`, apart; every file operation runs in an `os.Root`.
- [ ] A Restore member with `as` is refused 422.
- [ ] The copy goes only once the engine with `restore.env` is Ready, `Loaded` comes only once the copy is gone, and
  the hold lasts until an engine without `restore.env` is Ready.
- [ ] A Restore that ended `Failed` leaves no copy and no `status.restore`, and keeps its hold.
- [ ] Each oracle marker names a passing test.
- [ ] `deletion: delete` reclaims the storage and `retain` quarantines it; a start not held quarantines the storage of
  every add-on object no longer stored.

## Consequences

- A Backup is as old as the engine's newest snapshot: it misses the writes of up to one snapshot interval, which the
  Addon sets (rauthy's 15 minutes, hiqlite's floor 60 s). So is the `<restore>-before` export: the writes between
  that snapshot and the hold are in no copy.
- An in-place Restore restarts the engine twice (experiment 0025's timing `restore-engine-back`). For the whole
  Restore the namespace refuses calls (ADR-0290), and a sign-in through this provider fails.
- Only an admin restores an add-on object: it restores in place only, which needs `update` on the Namespace
  (ADR-0228 Decision 2.4). A deleted object's storage takes disk for the quarantine's period, and it comes back only
  by hand, with funcd stopped.
- Risk accepted: an engine that crashed right before a restore start cannot restore over hiqlite's crash marker
  (`remove_dir_all` on a file, hiqlite 0.15.3, a one-line upstream fix). The Restore times out and its clear restarts
  the engine normally; the operator runs `funcdctl hold release --namespace`, then the Restore again (ADR-0228
  Decision 2.4 refuses a new in-place Restore while the marker stands).

## Open questions

- **An HTTP backup endpoint** for engines that write no files: a follow-up when such an add-on arrives.

## References

- Feature issue [#1499](https://github.com/pyvvo/funcd/issues/1499) (FEAT-0018 F259).
- Experiment 0025 (pyvvo/experiment at a0643b3c, `0025-addons-rauthy`): `backup`, `restore`, `restore-engine-back`,
  the check "the restore brings rauthy back to the backup", and the hiqlite findings.
- The decider's card 3 and answers to the judge's questions (2026-11-19).
