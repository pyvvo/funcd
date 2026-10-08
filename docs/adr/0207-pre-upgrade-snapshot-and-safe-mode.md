# ADR-0207: Pre-upgrade snapshot and safe mode

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, upgrade, backup, hold, crash
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-7)
- **Supersedes in part**: none; the units of ADR-0026 §4 and ADR-0054 gain one directive (Decision 5).
- **Relates to**: ADR-0203 (pin class `pre-upgrade`, `Target`) · ADR-0204 (`Sealer`) · ADR-0205 (switch,
  `CheckBackup`, `verified`) · ADR-0206 (`.hold`, `restore run`, version rule, `Parent`) · ADR-0202 (cut) · ADR-0201
  (event store) · ADR-0028 (crash-only lifecycle) · ADR-0160 (`CrashLoopBackOff` is a replica's, not this)

## Context & Need

An upgrade is a binary swap with forward-only migrations (blueprint line 700): `docs/install.md` installs the binary
by hand, `funcd install` (`runInstall`) writes a unit, no package exists (`release.yml` builds funcdctl only), and the
new binary migrates at start (`workflow.MarkKVStoresOnce` in `(*Platform).Run`). No old state is kept and ADR-0206
Decision 4 refuses a newer minor's generation: only a copy the old binary wrote is a way back. Both units restart funcd
2 s after any failure (`Restart=on-failure`, `RestartSec=2`): a crash at boot or in a runner loops, re-firing timers.
**Purpose**: `funcd upgrade`, run by the installed binary, pins a `pre-upgrade` generation before the swap; safe mode
counts unclean starts, starts held, then stops naming the operator's restore (Q7, Q8). Callers: the operator; `serve`.

## Scenarios

- **scenario: upgrade-pins-old-state** — Given 0.m.0 installed with a `file://` target, When `funcd upgrade
  ./funcd-0.(m+1).0` runs with `--unit ""` and the daemon stopped, or with `--unit` and the daemon running (holding
  the target's lock), Then `gen/pre-upgrade/<n>/manifest.yaml` exists with `funcd: 0.m.0`, the installed path prints
  0.(m+1).0 and `<path>.previous` prints 0.m.0; with `--unit` the stop came before the lock, the start after the swap.
- **scenario: upgrade-keeps-data-owner** — Given the repo unit (`User=funcd`), its data and `file://` target owned by
  `funcd`, When root runs `funcd upgrade`, Then every entry there and `safemode.json` is `funcd`'s; the daemon serves;
  with the target root another uid's, the pin's new entries are that uid's and no older entry changes owner.
- **scenario: rollback-to-pre-upgrade** — Given that upgrade and objects 0.(m+1).0 wrote after it, When the runbook
  of Decision 2 runs, Then 0.m.0 starts held with the objects as at the pin and none of 0.(m+1).0's.
- **scenario: snapshot-unavailable-stops-upgrade** — Given no `backup.target`, a failing `s3://` probe or an empty
  metastore directory, When `funcd upgrade` runs, Then it exits non-zero naming the cause and `--no-snapshot`, binary
  and unit untouched; `--unit ""` and a running daemon ⇒ `fault.Conflict`; `--no-snapshot` swaps and warns once.
- **scenario: write-failure-keeps-old-binary** — Given upgrade-keeps-data-owner's data and a target refusing a part
  after the unit stopped, When the write fails, Then no binary is swapped, every entry under the data and `file://`
  root is `funcd`'s when `systemctl start` runs, the old daemon serves, and the error names the store.
- **scenario: downgrade-refused** — Given 0.(m+1).0 installed, When `funcd upgrade ./funcd-0.m.0` runs, Then
  `fault.Invalid` names both versions and nothing is stopped, written or swapped.
- **scenario: pre-upgrade-retention-reported** — Given four complete pins and `preUpgrade: 3`, When a fifth is
  written, Then the two oldest print as past `backup.retention.preUpgrade` and no object is deleted.
- **scenario: manual-swap-warns** — Given `safemode.json` naming 0.m.0 and no upgrade to 0.(m+1).0, When 0.(m+1).0
  starts, Then one Warn names both versions and the missing pre-upgrade generation, and funcd serves.
- **scenario: safe-mode-holds** — Given `afterCrashes: 3` and three starts killed before `stableAfter`, When funcd
  starts, Then `hold status` shows reason `safe-mode`, a 1 s timer does not fire, and an Error names 3 and `lastError`.
- **scenario: stable-run-resets-count** — Given two unclean starts, When the third runs `stableAfter`, or stops on
  SIGTERM, Then the count is 0 and two more killed starts do not enter safe mode.
- **scenario: safe-mode-stops-loop** — Given safe mode and three more killed starts, When funcd starts, Then it exits
  70 opening no store, naming count, last error and `<path>.previous restore run <timeline>/<n>` of `upgrade.generation`
  if its `to` is this version; else, or after a `--no-snapshot` upgrade after a pin ("no way back"), `verified`.
- **scenario: leave-safe-mode** — Given safe mode held, When `funcdctl hold release` runs and funcd runs
  `stableAfter`, Then no marker and count 0; given status 70, `funcd safe-mode reset` makes the next start held.

## Scope

**In**: `funcd upgrade`, the pre-upgrade pin, its count, the rollback runbook; the start counter, safe mode's two
steps, leaving them; their keys and the unit directive. **Out**: restore, hold, release, points (ADR-0206); layout,
pins, fencing (ADR-0203); verification (ADR-0205); cut (ADR-0202); a migration framework; online upgrade.

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q10 (same or older minor restores; the last 3 pre-upgrade snapshots kept, a key),
  Q7 (the box holds no identity), Q8 (the box puts and lists, never reads or deletes; the operator holds the restore
  credential), Q4 (one hold, an explicit release), Q1 (numbers are keys with defaults), Q13 (read at start).
- Fail closed: an upgrade without its way back is the operator's explicit choice. No new module.

## Alternatives considered

| Option | Outcome |
|---|---|
| **The installed binary runs `funcd upgrade`: checks, stop, offline cut, swap, start** ✅ | Chosen: the old binary writes the pin, so Q10 lets it restore it; the pin holds the three stores the new binary migrates (`MarkKVStoresOnce` is a metastore step); KV and blob data are outside it (Risk) |
| The new binary snapshots before its first migration | Rejected: its manifest names the newer minor, which the old binary refuses (ADR-0206 Decision 4) |
| An API call to the running daemon before the swap | Rejected: an admin token on the host and a route; writes between the cut and the stop miss the way back |
| A snapshot at every graceful stop; a deb or rpm hook | Rejected: every restart pays it and no stop marks an upgrade; no package exists (Context) |
| **Root's writes take `<storage.dataDir>`'s owner, a new pin its target directory's (ADR-0206 `hold.Own`)** ✅ | Chosen: one rule for both units, `--unit ""` and `restore run`; older target entries keep theirs (Q8) |
| Store steps as the unit's `User=` (`systemctl show`); stores opened read-only (Badger `ReadOnly`) | Rejected: a second process and a privilege drop, no user for `--unit ""`; a read-only open in ADR-0201's and ADR-0202's stores, and `safemode.json` and the target still need an owner |
| **Unclean starts counted in a file in `storage.dataDir`** ✅ | Chosen: any supervisor, survives restarts, travels with the data it guards |
| systemd `StartLimitBurst` plus `OnFailure=`; a count in the metastore | Rejected: systemd only and counts starts, not crashes; the store may be what crashes and the count would ride into snapshots |
| **Held on the current data, then stop naming the restore** ✅ | Chosen: within Q7 and Q8; a loop in a runner ends at the hold |
| The daemon restores the newest verified generation itself | Rejected under Q7, Q8: the box can neither read nor decrypt a generation (ADR-0203 Decision 2, ADR-0205 Alternatives); Open questions |
| A local plaintext last-good copy in the data dir | Rejected: a second backup path with its own "good" rule, on the same disk |

## Decision

**1. `funcd upgrade <new-binary>`** (NEW; flags `--config` as the root's, `--unit` default `funcd.service`, `""`
when the operator stops and starts the daemon, `--no-snapshot`). Steps in order; a failure stops at once:

| # | Step | On failure |
|---|---|---|
| 1 | `config.Load`; `<new-binary> version` parsed (`version.Info.String`: second field); older than `version.Version` (`golang.org/x/mod/semver`) ⇒ `fault.Invalid` naming both; `dev` on either side warns. `--unit` set: `requireLinuxRoot`; `<self>` not the `path=` of `systemctl show -p ExecStart <unit>` ⇒ `fault.Invalid` naming both | nothing changed |
| 2 | unless `--no-snapshot`, the checks that take no lock: platform backup on (ADR-0205 Decision 1), `config.CheckBackup` without errors, `envelope.New`; the metastore directory present and not empty, else `fault.Invalid` naming it and `--config` (the unit sets `FUNCD_DATA_DIR`, install.go; the shell may not); `s3://`: `backup.Open`, `Target.Ready` (the probe) | nothing changed; the error names the key, the directory or the probe, and `--no-snapshot` |
| 3 | `--unit` set: `systemctl stop <unit>` (install.go `systemctl`) | nothing changed |
| 4 | `file://`: `backup.Open`, `Target.Ready` (the `<dir>/lock` a running daemon holds, ADR-0203 Decision 4); open the event store, metastore and run state (a live Badger directory lock ⇒ `fault.Conflict`); `Target.Write` with ADR-0205 Decision 2's `WriteOptions` (`Seal`, `Keys: Sealer.Keys()`, `Parent: restore.Parent(…)`) and `Pin: backup.PreUpgrade` | the binary stays; the stores closed and owned (**Owner**), then `systemctl start <unit>` |
| 5 | swap: the new binary copied to `<self>.new` beside `<self>` (`os.Executable`, symlinks resolved), `fsync`, 0755; `<self>` hard-linked to `<self>.previous.tmp` (a stale one removed first), renamed onto `<self>.previous` (`link(2)` cannot replace); `<self>.new` renamed onto `<self>`; the directory `fsync`ed | `<self>.new`, `<self>.previous.tmp` removed; start as in 4 |
| 6 | `safemode.RecordUpgrade` (Decision 3; it and `Reset` end with `hold.Own` on `safemode.json`); `systemctl start <unit>`; print the pin as `<timeline>/<n>` and the pins past `preUpgrade` | report; the swap stands |

`--no-snapshot` skips 2 and 4 and warns once; the record carries no generation. **Owner**: the repo unit runs
`User=funcd` (ADR-0026 §4), install.go's `User=root`. Every exit from step 4 closes what it opened, then re-owns with
ADR-0206's `hold.Own` (Badger creates files as root): the store directories, and `lock` if new (the daemon opens it),
take `<storage.dataDir>`'s owner; under the target only this run's new entries (`gen/pre-upgrade/<n>/`, new parents)
take the nearest pre-existing directory's (Q8, ADR-0203 Decision 5); deferred in `Snapshot`; an error fails step 4.

**2. Pre-upgrade pins** (Q10, Q8). funcd deletes no pin: step 6 lists `gen/pre-upgrade/` (`Target.List`) and prints
the complete ones older than the newest `backup.retention.preUpgrade` with the prune command of
`examples/backup-lifecycle.md` (ADR-0203); no lifecycle rule covers the prefix. Rollback runbook: stop the unit; move
the three store directories aside; `<self>.previous restore run <timeline>/<n>` of `upgrade.generation` (step 6 prints
it; not the class `pre-upgrade`, whose newest may be an older upgrade's; ADR-0206, the operator's credential,
identity); install `<self>.previous` at `<self>`; `funcd safe-mode reset` if stopped; start held; verify; `hold release`.

**3. Counting starts** (proposed; decider confirms at acceptance). `serve` (`cmd/funcd/main.go`) calls
`safemode.Begin` after `config.Load` and the data directory exist, before `buildOptions`, so store opens and
migrations count. `Begin` records the start as unclean (`unclean` + 1, `version`) in `<storage.dataDir>/safemode.json`
(NEW, 0600, file and directory `fsync`ed; Decision 1's owner from `RecordUpgrade`, `Reset`). A start is clean when it
runs `stableAfter` (a timer started before `platform.Run`) or `Run` returns nil after a stop signal: `unclean` = 0. A
returned error is kept as `lastError`; a panic, a kill or a power loss leaves it unclean. A `version` newer than the
last start's that `upgrade.to` does not name logs one Warn (manual-swap-warns). `storage.mode: memory` skips all this.

**4. Safe mode** (proposed; decider confirms at acceptance). With N = `recovery.safeMode.afterCrashes`, `Begin`
reads `unclean` before counting this start:

```
unclean < N        normal start
N ≤ unclean < 2N   held: no marker (hold.Open) ⇒ serve calls hold.Write(Reason "safe-mode") (ADR-0206); one stays
unclean ≥ 2N       stopped: hold.Write as held; safemode.json unchanged, no store opened; *StoppedError ⇒ exit 70
```

Held stops a loop born in a runner (ADR-0206 Decision 6); a loop in the API, a controller or a store open goes on to
stopped. It prints the next step: Decision 2's rollback to `upgrade.generation` when `upgrade.to` is this version and
`generation` is set; else `funcd restore run verified` (ADR-0206) into directories moved aside (nil: "no way back").

**5. Leaving safe mode.** Held: `funcdctl hold release` (ADR-0206 Decision 8); the count clears after `stableAfter`.
Stopped: `funcd safe-mode reset` (NEW, offline) sets `unclean` and `lastError` to zero and keeps `upgrade`; the
`safe-mode` marker stays, so the next start is held. Both units gain `RestartPreventExitStatus=70` (no restart).

| Key | Meaning | Default |
|---|---|---|
| `backup.retention.preUpgrade` (NEW) | complete pre-upgrade generations kept; at least 1 | 3 (Q10) |
| `recovery.safeMode.afterCrashes` (NEW) | unclean starts before held; twice it, stopped; at least 1 | 3 (report §4.C `crashLoopThreshold`, renamed: `CrashLoopBackOff` is a replica's, ADR-0160) |
| `recovery.safeMode.stableAfter` (NEW) | continuous run that makes a start clean; ADR-0194 grammar, at least 1ms | `10m` |

## Temporary workarounds

None.

## Contracts

```go
package safemode // internal/platform/safemode (NEW)

const StateFile = "safemode.json" // JSON keys: the lowerCamel field names; Upgrade.At is ADR-0196's form
const ExitStopped = 70             // the exit status of Stopped; the units' RestartPreventExitStatus

type Upgrade struct{ From, To string; Generation *backup.GenRef; At v1alpha1.Timestamp } // Generation nil: --no-snapshot
type State struct{ Unclean int; Version, LastError string; Upgrade *Upgrade }
type Config struct{ AfterCrashes int; StableAfter time.Duration }
type Mode int
const Normal, Held, Stopped Mode = 0, 1, 2

type StoppedError struct{ State State } // Error() names the count, the last error and the next command
type Start struct{ /* unexported */ }

// Begin returns the state read (before this start) and the mode; Stopped ⇒ nil *Start and *StoppedError, nothing
// written to StateFile; else the start is recorded unclean. Unreadable state ⇒ fault.Internal, no start.
func Begin(dataDir, version string, cfg Config) (*Start, State, Mode, error)
func (s *Start) Clean() error           // unclean = 0
func (s *Start) Failed(err error) error // lastError
func Reset(dataDir string) error
func RecordUpgrade(dataDir string, u Upgrade) error // also unclean = 0; it and Reset end with hold.Own (Decision 1)
```

```go
package upgrade // internal/platform/upgrade (NEW); Self: os.Executable, symlinks resolved; Unit "" ⇒ no systemctl
type Options struct{ Config config.Config; NewBinary, Self, Unit string; NoSnapshot bool
	Systemctl func(args ...string) ([]byte, error); Out io.Writer } // as install.go systemctl, plus stdout (show)
type Result struct{ From, To string; Pin *backup.GenRef; PastRetention []backup.Entry }
func Run(ctx context.Context, o Options) (Result, error) // Decision 1
// Snapshot: steps 2 and 4 alone; an empty metastore directory ⇒ fault.Invalid; a store or the file:// lock held by a
// running daemon ⇒ fault.Conflict naming its directory; every return after step 4 began re-owns (Decision 1, Owner).
func Snapshot(ctx context.Context, cfg config.Config) (backup.Manifest, error)
```

`config.Config` gains (NEW) `Backup.Retention.PreUpgrade int` `json:"preUpgrade,omitempty"`
(`FUNCD_BACKUP_RETENTION_PRE_UPGRADE`) and `Recovery.SafeMode.{AfterCrashes int, StableAfter string}` under
`json:"recovery"`/`"safeMode"` (`FUNCD_RECOVERY_SAFE_MODE_AFTER_CRASHES`, `…_STABLE_AFTER`); defaults in `defaults()`
as `c.Eventing.DeliveryAttempts`. `preUpgrade` below 1 is an error row of ADR-0205's `config.CheckBackup`; the
`recovery.safeMode` bounds are checked in `cmd/funcd` with `parseDuration`, as `bootBackoff`.

| consumes | exposes |
|---|---|
| ADR-0201 `eventstore.Open`; ADR-0202 `snapshot.Source` of the three stores; ADR-0203 `backup.Open`, `Target.Ready`, `Write`, `List`, `WriteOptions.Pin`, `Keys`, `PreUpgrade`, `GenRef`; ADR-0204 `envelope.New`, `Sealer`; ADR-0205 `CheckBackup`, Decisions 1 and 2; ADR-0206 `hold.Open`, `hold.Write`, `hold.Own`, `Marker`, `restore.Parent`, `restore run`; `golang.org/x/mod/semver` (direct via ADR-0206) | `funcd upgrade`, `funcd safe-mode reset`; `<self>.previous`; `<dataDir>/safemode.json`; exit status 70; the three keys; marker reason `safe-mode` |

## Implementation plan

**Files**: NEW `internal/platform/safemode/safemode.go`, `internal/platform/upgrade/upgrade.go`, `cmd/funcd/upgrade.go`
(`upgrade`, `safe-mode reset`); `cmd/funcd/main.go` (`serve`: `Begin`, `hold.Write`, the timer, `Clean`/`Failed`;
`main`: `ExitStopped`); `cmd/funcd/install.go`, `configs/systemd/funcd.service` (the directive); `config/config.go`;
`examples/funcdconfig.yaml`; `docs/install.md` (Upgrade); ADR-0206's `examples/restore-runbook.md` (rollback, crash
loop); ADR-0203's `examples/backup-lifecycle.md` (pin prune). **go.mod**: none. **At acceptance**: blueprint line 700
gains "`funcd upgrade` writes a pre-upgrade generation; repeated crashes start held, then stop"; in F109, "starts on the
last good copy, held" becomes "starts held, then stops and names the restore of the last good copy".

**Test plan**: one `TestScenario<Name>` per scenario: funcd built twice with `-ldflags -X …version.Version=` (as
`cmd/funcd/version_test.go`), v0.m.0 and v0.(m+1).0, so the rollback really restores; a fake `Systemctl` (`stop`
releases a `Target` the test holds, `show` returns `ExecStart`); a `file://` target; crashes by a child `funcd` killed
with SIGKILL; all on `shortDataDir`; owner assertions as root on Linux (a second uid owns the data, runs the child),
else skipped. Units: `TestBeginModes` (N−1, N, 2N−1, 2N), `TestStateRoundTrip`, `TestParseVersionOutput`,
`TestExecStartMismatch`, `TestSwapLeavesBinaryOnFailure`, `TestUnitsPreventRestartOn70` (both units). **Definition of
done**: `scripts/agent/d go test -race -count=1`, `scripts/agent/d just ci` green; no new module; no identity/path leak.

## Review checklist

- [ ] Steps 1 and 2 check before the stop; the `file://` lock, the store opens and the cut follow it; the swap follows
      a complete pin; a failure after the stop restarts the old binary; `.previous` is replaced by link and rename.
- [ ] Root's writes are re-owned by `hold.Own` on every exit, before `systemctl start`: stores, `safemode.json`, a new
      `lock` to `<storage.dataDir>`'s owner; new target entries to the nearest pre-existing directory's; no older one.
- [ ] `funcd` deletes no backup object; the pin is written with `PreUpgrade` by the installed binary.
- [ ] `Begin` precedes `buildOptions`; Stopped opens nothing; clean only at `stableAfter` or a nil `Run`; memory skips.
- [ ] Held and stopped write a marker only when none exists; release is ADR-0206's; both units carry the directive.

## Consequences

**Positive**: every `funcd upgrade` but `--no-snapshot` has a way back the old binary restores; a crash loop ends held
or stopped, not repeating side effects. **Negative (accepted)**: the snapshot lengthens the upgrade's downtime (a cut and
an upload); a manual swap only warns; an environment fault (a taken port) counts like a crash; rollback loses every
write after the pin; the first protected upgrade starts from the release shipping this ADR. **Risk**: a loop in the API
or a controller needs the operator's restore; a box without a target has no way back; the pin holds only the three
stores: a release migrating KV or blob data must say how it rolls back (`restore kv` or a later ADR).

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| How the old binary learns of the upgrade | the operator runs `funcd upgrade` with the installed binary (Decision 1); a manual swap warns | the repo's only path is a binary swap; the old binary must write the pin (Q10) |
| Pre-upgrade snapshot fails | the upgrade stops before the swap; `--no-snapshot` is the explicit opt-out | fail closed |
| Crash counter storage | `<dataDir>/safemode.json`; clean at `stableAfter` (10m) or a signal stop | any supervisor; survives restarts |
| Leaving safe mode | held: `hold release`; stopped: `funcd safe-mode reset`, next start held | one release path (Q4) |
| F109 (and report §4.C) say "starts on the last good copy": automatic restore of the newest verified generation | not in v1: held, then stopped with the command; F109 reworded at acceptance (Implementation plan) | automatic needs a read credential and an identity on the box, which Q7 and Q8 (decided after §4.C) forbid |
| Unit directive | additive, no partial supersession of ADR-0026 §4 or ADR-0054 | nothing either decides changes |

## References

- Report §3, §4.A (Retention, Versions), §4.C (Pre-upgrade snapshot, Restore point, Hold; its Safe mode row, "boot the
  last good generation held", is superseded by Q7 and Q8: Decision 4), §4.I row 7, §5 (Q1, Q4, Q7, Q8, Q10, Q13);
  FEAT-0009; `docs/roadmap/dr-plan.json` (DR-7). Code read 2026-10-08: `cmd/funcd/{main,install}.go` (`User=root`),
  `configs/systemd/funcd.service` (`User=funcd`), `docs/install.md`, `.github/workflows/release.yml`,
  `internal/platform/{version,config}`, `pkg/funcd/funcd.go` `Run`, `internal/workflow/kvmigration.go`.
- Licences checked 2026-10-08: no new module; `golang.org/x/mod` BSD-3-Clause (ADR-0206); Badger v4.9.2 Apache-2.0: its
  directory lock (`dir_unix.go`) refuses a second process; a read-write open creates a memtable (`memtable.go:138`).
