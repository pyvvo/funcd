# ADR-0205: Backup operation — runs, settings check, status, metrics and verification

- **Status**: Implemented (2026-10-10; review passed; implementation done 2026-10-10; accepted 2026-10-10 by an `adr-batch` run after a clean `adr-judge` gate; the defaults below were not confirmed one by one)
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: backup, disaster-recovery, config, observability, metrics
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-4)
- **Supersedes in part**: none.
- **Relates to**: ADR-0202 (cut) · ADR-0203 (target, pins) · ADR-0204 (`Sealer`) · ADR-0206 (restore, hold) · ADR-0207 ·
  ADR-0208, 0209 (blob, KV) · ADR-0061, 0062 (loader) · ADR-0194, 0196, 0197 (time, `_ms`) · ADR-0010, 0114, 0147 (meters)

## Context & Need

Today's only backup, the KV export (`kvbadger.RunBackup`), ticks from process start, logs failures at Error and keeps
no status or metric; package main checks its settings, out of funcdctl's reach. ADR-0202 to ADR-0204 give the cut, the
writer and the sealer, not when a run happens, what a failed or slow one does, which settings stop the start, or how
an operator learns backups stopped or cannot be read back. **Purpose**: the runner; one settings check for the daemon
and `funcdctl backup plan` (F112); one status for every backup stream, metrics, the age alert; verification.

## Scenarios

- **scenario: backup-off-by-default** — Given no `backup.target`, or `storage.mode: memory` with one, When the daemon
  starts, Then no run happens, status reads `enabled: false`, and the memory case logs one warning.
- **scenario: runs-on-interval** — Given a `file://` target and `interval: 1h` on a test clock, When 2 h pass, Then two
  complete generations exist and `lastSuccessTime` is the newest one's time.
- **scenario: restart-keeps-cadence** — Given the newest complete hourly generation written 20 min ago and a
  `pre-upgrade` pin 5 min ago, When the daemon restarts, Then the first run starts 40 min after the start.
- **scenario: failed-run-retries** — Given a target refusing puts, or unreachable at start, When a run is due, Then the
  daemon serves, `lastFailure` holds the error, `funcd.backup.runs{result=failed}` is 1, the next attempt 5 min later.
- **scenario: overrun-no-overlap** — Given a 70 min run with `interval: 1h`, When it ends, Then the next starts at once,
  no two runs overlap, and one warning carries `duration_ms` and `interval_ms`.
- **scenario: impossible-settings-refused** — Given `objectives.rpo: 30m` with `interval: 1h`, or `retention.hourly: 2`,
  `daily: 0`, `weekly: 0` with `interval: 3h`, When the daemon starts, Then it exits with `fault.Invalid` naming the key.
- **scenario: tight-settings-warn** — Given `interval: 1h`, `objectives.rpo: 90m`, When the daemon starts, Then it
  serves and logs one warning naming both keys; `config.CheckBackup` returns the same findings in both scenarios.
- **scenario: rpo-risk-follows-verified** — Given only generation 7 verified, `objectives.rpo: 2h`, When 7 is 2 h old,
  Then `rpoRisk` is true, `funcd.backup.rpo_risk` is 1, one warning is logged; once 9 is verified, a listing clears it.
- **scenario: verify-pins-generation** — Given generation 7 the newest complete, When `funcdctl backup verify` runs with
  the verify credential and an identity, Then `gen/verified/<7>-<timeline>/` holds 7; a rerun writes nothing, exit 0.
- **scenario: verify-detects-damage** — Given one changed byte in a part of 7, or its last part missing, When verify
  runs, Then it fails naming the store and part, and writes nothing under `gen/verified/`.
- **scenario: status-shows-restore-points** — Given generations 3 to 9 complete and 7 verified, When an admin runs
  `funcdctl backup status`, Then it prints the times of 9, 7 and 3 and `rpoRisk`; a developer gets 403.
- **scenario: kv-stream-without-platform-backup** — Given no `backup.target`, the KV export on and its target refusing
  puts, When a KV run fails, Then `funcdctl backup status` shows `enabled: false` and `streams.kv.lastFailure`.

## Scope

**In**: Decision 1's switch and keys; the runner; the rules across all backup keys; status, its route, the stream
reports; metrics, the alert; verification. **Out**: cut (ADR-0202); format, keys (ADR-0203); envelope, escrow (ADR-0204);
restore, drills, the hold (ADR-0206); pins (ADR-0207); blob (ADR-0208); KV (ADR-0209); `BackupSchedule`; F112.

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q1 (every number a key with a default; opt-in; once on, target and recipients
  required; checked at start by the loader F112 reuses; impossible ⇒ error, tight ⇒ warning; RTO is no key); Q13 (read
  once, restart to change, no SIGHUP); Q7, Q8 (the box cannot read or decrypt a generation). Fail closed; no stored
  state to drift from the target; OTel via ADR-0010's providers, no-op without `telemetry.endpoint`; no new module.

## Alternatives considered

| Option | Outcome |
|---|---|
| **Status in the runner's memory, times from the target listing** ✅ | Chosen: the target is the truth; survives a restart; nothing written per run |
| A metastore kind or record; a file in the data directory | Rejected: a record rides into the next snapshot, so a restored platform shows an old status, and each run wakes watchers; a file is a second truth a remote funcdctl cannot read |
| **One status, a section per stream (platform, blob, KV)** ✅; platform only, the others logging | Platform only rejected: the KV export's failures stay as silent as today (Context); ADR-0203, ADR-0208, ADR-0209 report here |
| **Alert on the newest verified generation** ✅; on the newest written one | The second rejected: a written generation can be unreadable (wrong recipients, damage); report §2 pattern 6 |
| The daemon verifies its own generations; verification restores into a scratch dir | Rejected: a read credential and an identity on the box (Q7, Q8); a restore is the drill (ADR-0206), too heavy per interval |
| **Cadence anchored on the newest complete ladder manifest** ✅; a ticker from start (KV export) | Ticker rejected: a restart shifts or doubles runs; the anchor is ADR-0208's mirror rule and the #807 fix |
| A `backup.enabled` key (as `kvstore.backup.enabled`); rules in package main | Rejected: Q7 ties encryption to "a target is set" and ADR-0203, ADR-0204 key off `target`; funcdctl cannot import main |

## Decision

**1. Switch and keys.** The platform backup is on when `backup.target` is set and `storage.mode` is `file`. Durations
use ADR-0194's grammar within [1ms, `v1.MaxDuration`]; a change applies at the next start (Q13).

| Key (`backup.`) | Meaning | Default |
|---|---|---|
| `interval` (NEW) | time between run starts (Decision 2) | `1h` (Q1) |
| `objectives.rpo` (NEW) | age of the newest verified generation that sets `rpoRisk` | `2h` (proposed; accepted as default, not confirmed by the decider) |
| `retryInterval` (NEW) | wait after a failed run before the next attempt | `5m` (proposed; accepted as default, not confirmed by the decider) |

**2. Runner.** `cmd/funcd` builds it when the platform backup or the KV export is on, before the blob and KV wiring that
take its `Recorder` (backup off: no `Target`, streams only); `funcd.New` binds its stores, `Platform.Run` runs it.

```
start: CheckBackup → envelope.New → backup.Open (an error: no start) → list → due = newest ladder At + interval
loop:  wait for due → held: due = now + retryInterval, no run → else Target.Write(events, metastore, runs) → list
       ok: due = run start + interval     failed: due = min(run end + retryInterval, run start + interval)
```

- **Due time**: complete `hourly`, `daily`, `weekly` entries only (none ⇒ now), pins never (ADR-0203 Decision 6). A
  listing anchors on ModTime, which trails the run start by the run's length (seconds at metastore size), so the first
  run after a restart is that late (accepted). A failed first listing (target down at start) is a failed run.
- **Writes**: `Target.Write` with `WriteOptions{Seal: s.Seal(), Keys: s.Keys(), Parent: in.Parent}` (ADR-0203, 0206).
- **Held** (ADR-0206 Decision 6; the recheck proposed; accepted as default, not confirmed by the decider): a due time while `Hold.Held()`
  writes nothing, counts as neither a run nor a failure, sets `held: true` and rechecks after `retryInterval`.
- **Overrun** (proposed; accepted as default, not confirmed by the decider): no overlap, no queue: a late run's successor starts when it
  ends, with one Warn `backup run overran its interval` (`duration_ms`, `interval_ms`); a stuck run shows as `rpoRisk`.
- **Failure** (`Ready` not ready, a list, cut or put error, or ADR-0203 Decision 4's second-writer `fault.Conflict`
  naming `backup.target`): Error `backup run failed`, `lastFailure`, a `failed` count; the daemon serves. Shutdown
  cancelling a run is no failure (as `RunBackup`'s `ctx.Err()` guard).

**3. Settings check** (table proposed; accepted as default, not confirmed by the decider). An error is `fault.Invalid` naming the key and
no start; a warning is one Warn line at start. The daemon (`Validate`) and F112 run `config.CheckBackup` (every row
reading only the merged config but `kvstore.backup.*`, which ADR-0209 moves in) and ADR-0208's `config.CheckBlob` (`blob.*`).

| Keys | Rule | Outcome | Checked in |
|---|---|---|---|
| `backup.interval`, `.objectives.rpo`, `.retryInterval` | malformed or out of bounds; rpo below interval (Q1) | error | `CheckBackup` |
| `retention.*`, `interval` | the longest kept class (`hourly` h, `daily` × 24 h, `weekly` × 168 h; `0` unused) below interval: generations expire before the next | error | `CheckBackup` |
| `target`, `credentialsFile`, `retention.*` | scheme not `s3`/`file`; `credentialsFile` with `file://`; `hourly`, `verified`, `preUpgrade` (ADR-0207) < 1; `daily`, `weekly` < 0 | error | `CheckBackup` (ADR-0203 §5 rules) |
| `encryption.recipients`, `.none`, `secrets.encryptionKeyFile` | no recipients and not `none`; `none` with recipients; `none` without a secrets key | error | `CheckBackup` (ADR-0204 §2 rules) |
| `encryption.recipients` files | unreadable, under 2 distinct, X25519 mixed with hybrid | error | `envelope.New` (ADR-0204) |
| `kvstore.backup.*`; `blob.*` | enabled without target or engine not `badger`, malformed duration; ADR-0208 Decision 1, a target inside the store | error | `checkKVStoreConfig`, `kvBackup` until ADR-0209; `config.CheckBlob` (ADR-0208 Decision 1); a target inside the store: `gocloud.CompareDomains` in `cmd/funcd` (ADR-0208 Decision 3, needs I/O) |
| `interval`, `objectives.rpo`, `retention.verified` (ADR-0203) | interval above half the rpo: one failed run breaks it (Q1); `retention.verified` × 24 h below `rpo − interval`: a copy can expire before its original is `rpo` old (Decision 6) | warning | `CheckBackup` |
| `backup.*` without `target`; `storage.mode: memory` with `target`; `encryption.none: true`; recipients without a secrets key | a key off its default is ignored, no platform backup runs; ADR-0204 Decision 2 | warning | `CheckBackup` |
| target on the data dir's device; failed probe with `singleWriter: true`; `kvstore.backup` in memory mode; `OverlapProvider` | ADR-0203 Decision 4; today; ADR-0208 Decision 3 | warning | `Target.Ready`; as above |

**4. Status** (place proposed; accepted as default, not confirmed by the decider). The runner keeps it in memory; times are listing
`Entry.At` (the manifest's ModTime; the box cannot read `at`), so they survive a restart; `lastFailure` does not.

| Field (beside `enabled`, `held` (NEW, the gate at the read), `interval`, `rpo`) | Value |
|---|---|
| `lastSuccessTime` | newest complete ladder generation (`hourly`, `daily`, `weekly`) |
| `lastVerifiedTime` | highest-numbered generation with a complete `verified` copy, at its original's `At`; absent when none or the original expired |
| `earliestRestorePoint` | oldest complete ladder or `pre-upgrade` generation (both restorable, ADR-0206 Decision 3); a `verified` copy adds none, its ModTime being the verify time |
| `nextRunTime` | Decision 2's due time |
| `rpoRisk` | backup on, and `lastVerifiedTime` absent or older than `objectives.rpo`; besides after each run and per status read, the runner relists at `lastVerifiedTime + rpo`, so a fresh listing turns it on |
| `lastFailure` | `time`, `error` of the last failed run since start; a success clears it |

**Streams** (proposed; accepted as default, not confirmed by the decider): ADR-0208's mirror and ADR-0209's KV export report each run (a
`Ready` refusal too) to `Runner.Recorder`, kept as `streams.blob`, `streams.kv` (`lastSuccessTime`, `lastFailure`) since
start, no `rpoRisk` (never verified). `GET /apis/funcd.io/v1alpha1/platformbackup` (NEW; 503 on a failed listing)
authorizes `get` on `WorkerNode` (cluster-scoped, so admin-only); `funcdctl backup status` (NEW) prints it as YAML.

**5. Metrics and alert** (names proposed; accepted as default, not confirmed by the decider). Meter `funcd.backup` from
`observability.Telemetry.MeterProvider()`, named like `funcd.edge.*` (ADR-0114): `funcd.backup.runs` (Int64Counter,
`stream` `platform`/`blob`/`kv`, `result` `ok`/`failed`), `funcd.backup.duration_ms` (Float64Histogram, `stream`,
`result`), `funcd.backup.rpo_risk` (Int64ObservableGauge, 0/1). The alert is `rpoRisk`: Warn `backup rpo at risk`
(`rpo_ms`, `age_ms` when verified exists) on turning on and once per `objectives.rpo` while on, Info on clearing.

**6. Verified** (checks proposed; accepted as default, not confirmed by the decider). `funcdctl backup verify` (NEW; off the box, with
ADR-0203's verify credential and an ADR-0204 identity; flags `--target`, `--credentials-file`, `--identity`, `--escrow`,
`--generation`, default the newest complete ladder one) reads the manifest and each store's parts in order, matching
`parts`, `bytes`, `sha256`; decrypts each store file to its end with the `Unseal` that `envelope.Opener(ids)` returns for
the manifest's `recipients` (age authenticates each chunk, detects truncation); parses the framing (`backup.Records`) to
its end, keys strictly ascending (ADR-0202 Decision 1); with `--escrow`, finds the `secretsKey` and `masterSecret`
fingerprints (`escrow.Find`); decrypts no Secret (the drill does). All passed, it copies the parts, the manifest last, to
`gen/verified/<n>-<timeline>/` (the original's name, ADR-0203 Decision 6) with `IfNotExist` (a part already there must
match); a rerun writes nothing (`Pinned` false, exit 0). The operator runs it every `(objectives.rpo − interval)/2`
(30 min at defaults), as `rpo − interval` lets an original verified at age `interval` pass `rpo` while the next verify
runs. Copies expire by ADR-0203's `gen/verified/` rule after `retention.verified` days; no backup credential deletes.

## Temporary workarounds

None.

## Contracts

```go
package config // internal/platform/config. Config.Backup gains (NEW) strings Interval, RetryInterval,
// Objectives.RPO; JSON: Decision 1's keys; env: FUNCD_ + the key in upper snake case; omitempty.
type BackupTimes struct{ Interval, RPO, RetryInterval time.Duration }
type Finding struct{ Key, Message string; Error bool } // Key: the first key the rule names; !Error: a warning
func (c Config) BackupTimes() (BackupTimes, error)     // Decision 1 bounds; empty ⇒ default; else fault.Invalid
func (c Config) CheckBackup() []Finding                // Decision 3 rows reading only c, errors first
```

```go
package runner // internal/backup/runner (NEW). Target nil ⇒ streams only; Hold: *hold.Hold, nil ⇒ never held; Clock
// nil ⇒ system; After nil ⇒ time.After; Inputs: *eventstore.Store, store.Store, runstate.Store, restore.Parent.
type Config struct{ Target backup.Target; Sealer *envelope.Sealer; Hold interface{ Held() bool }; Times config.BackupTimes
	Meter metric.Meter; Logger *slog.Logger; Clock clock.Clock; After func(time.Duration) <-chan time.Time }
type Inputs struct{ Events, Meta, Runs snapshot.Source; Parent *backup.GenRef }
type Status struct{ Enabled, Held, RPORisk bool; Interval, RPO v1alpha1.Duration; LastFailure *Failure
	LastSuccessTime, LastVerifiedTime, EarliestRestorePoint, NextRunTime *v1alpha1.Timestamp // ADR-0196; nil omitted
	Streams map[string]Stream } // "blob", "kv": those that reported since start; JSON keys: Decision 4
type Stream struct{ LastSuccessTime *v1alpha1.Timestamp; LastFailure *Failure }
type Failure struct{ Time v1alpha1.Timestamp; Error string }; type Runner struct{ /* unexported */ }
func New(cfg Config) (*Runner, error) // cmd/funcd; registers the instruments; a Target without a Sealer ⇒ fault.Invalid
func (r *Runner) Bind(in Inputs) error // funcd.New, before Run; a Target with a nil Source ⇒ fault.Invalid
func (r *Runner) Run(ctx context.Context) // until ctx ends (at once without a Target); failures: status, log, metrics
func (r *Runner) Status(ctx context.Context) (Status, error) // lists the Target; its error ⇒ last status, fault.Unavailable
func (r *Runner) Recorder(stream string) func(start time.Time, err error) // "blob", "kv"; nil err: success, at the call
```

```go
package verify // internal/backup/verify (NEW). Bucket: the target opened with the verify credential (gocloud.OpenWith);
// Identities: envelope.ReadIdentities; EscrowDir "" ⇒ keys unchecked; Generation 0 ⇒ the newest complete ladder one.
type Options struct{ Bucket blob.Bucket; Identities []age.Identity; EscrowDir string; Generation uint64 }
type Result struct{ Generation uint64; Records int64; KeysChecked, Pinned bool } // Pinned false: copy existed
func Verify(ctx context.Context, o Options) (Result, error) // damage ⇒ fault.Invalid (store, part); none ⇒ NotFound
type BackupStatuser interface{ Status(ctx context.Context) (runner.Status, error) } // internal/controlplane (NEW)
func RegisterPlatformBackup(api huma.API, s BackupStatuser, authz auth.Authorizer) // nil s (no stream on): Enabled false
func WithPlatformBackup(r *runner.Runner) Option // pkg/funcd (NEW): New calls r.Bind, Run runs r, the route reads r
```

| consumes | exposes |
|---|---|
| ADR-0201 `eventstore.Store`; ADR-0202 `snapshot.Source`; ADR-0203 `backup.Open`, `Target`, `Entry`, `GenRef`, `Manifest`, `WriteOptions`, `ReadManifest`, `List`, `Keys`, `Records`, `Unseal`, `IfNotExist`, `gocloud.OpenWith`, keys `backup.target`, `.credentialsFile`, `.retention.*` (with `.verified`); ADR-0204 `envelope.New`, `Opener`, `ReadIdentities`, `escrow.Find`; ADR-0206 `restore.Parent`, `WithHold` (wiring only); ADR-0208 `config.CheckBlob` (`Validate` and F112 call it beside `CheckBackup`); ADR-0196 `Timestamp`; `v1alpha1.Duration`; OTel `metric` v1.44.0 (in `go.mod`); `clock.Clock` | keys `backup.interval`, `.objectives.rpo`, `.retryInterval`; `config.BackupTimes`, `CheckBackup`, `Finding`; packages `runner` (its `Recorder` for ADR-0208, ADR-0209), `verify`; `GET /apis/funcd.io/v1alpha1/platformbackup`; `funcdctl backup status`, `backup verify`; three instruments; the log lines of Decisions 2 and 5 |

## Implementation plan

**Files**: `internal/platform/config/config.go` (fields, defaults beside `c.Eventing`, `CheckBackup` in `Validate`); NEW
`internal/backup/{runner,verify}/`, `internal/controlplane/backup.go` (route, stub; mounted in `server.go` as
`RegisterDeadLetters`), `cmd/funcdctl/backup.go`; `pkg/funcd/{options,funcd}.go`; `cmd/funcd/main.go` (warnings; the
Telemetry block, then `runner.New` with its `funcd.backup` meter, no-op without `telemetry.endpoint` as `invokeMeter`,
both moved before `substrateOptions` and `buildKVStore`); `examples/funcdconfig.yaml`; ADR-0203's
`examples/backup-lifecycle.md` (verify every `(rpo − interval)/2`); the OpenAPI. **Order**: no build edge on ADR-0206
(`Hold` structural, `Parent` a `backup.GenRef`); the later wires both, adds the runner to `TestEveryRunnerConsultsHold`.
**go.mod**: none. **Blueprint** (at acceptance): the DR bullet gains "scheduled, checked and alerted platform backups".

**Test plan**: a `TestScenario<Name>` per scenario on `file://` targets (`os.Chtimes` for ModTimes), injected `Clock`,
`After`, `sdkmetric.ManualReader`. Units: `TestCheckBackupRules`, `TestBackupTimesDefaults`, `TestDueTime` (a newer pin
ignored; a failed first listing; held: nothing written or counted), `TestStatusFromEntries` (a `pre-upgrade` pin counted,
a `verified` copy not), `TestRecorderStreams`, `TestRPOWarnOncePerRPO`, `TestVerifyNoGeneration`. **Definition of done**:
`scripts/agent/d go test -race -count=1`, `scripts/agent/d just ci` green; no new module; no identity or path leak.

## Review checklist

- [ ] `CheckBackup` holds the config-only Decision 3 rows but `kvstore.backup.*`, `blob.*`; `Validate` fails on errors.
- [ ] One run at a time, no deadline; due times per Decision 2 from ladder entries; a cancelled or held run is no
      failure; a `CheckBackup`, `envelope.New` or `backup.Open` error stops the start, a failed first listing does not.
- [ ] Status times from the listing, none stored; `rpoRisk` on the verified `At`; `streams` by `Recorder`, backup off too.
- [ ] The route authorizes `get` on `WorkerNode`; instruments, log keys and the Warn cadence match Decision 5 (`_ms`).
- [ ] Verify pins only after every check passes, the manifest last; the daemon holds no read credential or identity.

## Consequences

**Positive**: one rule set for daemon and helper; a stopped, unreadable or hung backup surfaces as `rpoRisk`, a failing
mirror or KV export in `streams`; nothing stored. **Negative (accepted)**: rarer verifies (Decision 6) keep `rpoRisk` on
(a fresh install too; one Warn per rpo), none for `retention.verified` days (ADR-0203) leaves no `verified` copy; at
defaults verify reads 48 generations a day, copies 24 (48 kept); a restart loses `lastFailure`, `streams` and one run's
length of cadence; each status read lists the target. **Risk**: the verify host can read every generation.

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Why |
|---|---|---|
| Where status lives; its scope | runner memory, times from the listing; route `platformbackup`, `get` on `WorkerNode`; a `streams` section the mirror and KV export feed through `Recorder`, no `rpoRisk` (the alternative: platform only, the others logging) | no second truth; a restore cannot bring back a stale status; the KV export's failures stop being silent |
| What verification does; cost; cadence | checksums, full decrypt, framing; keys only with `--escrow`; no Secret decrypt; a read per verify, a write per generation; every `(rpo − interval)/2` (the alternative: `rpoRisk` tolerates one verify's length) | catches damage and wrong recipients without a restore; the slack absorbs a verify's own length and the alert stays exact |
| `objectives.rpo`, `retryInterval`; switch; memory mode | `2h`, `5m`; `backup.target` set; memory mode warns, runs nothing; `gen/verified/` expiry: ADR-0203's Open questions | `1h` warns at defaults under Q1's rule; Q7's wording; the `kvstore.backup` precedent; ADR-0203 owns `retention.*` and `LifecycleRules`, so one ADR holds the choice |
| Overrun; runs while held; error and warning table; metric names; answered elsewhere | Decision 2: no overlap, no queue, next run at once, one warning; held: skipped (ADR-0206 Decision 6), no run and no failure, rechecked every `retryInterval`, `held: true`; Decision 3; `funcd.backup.runs`, `.duration_ms` (both with `stream`), `.rpo_risk`; an age gauge, a `backup status` exit code are F112's | one run at a time (ADR-0203); a held drill writes no generation that turns the source's later ones `Abandoned` (ADR-0203), a quick first run after the release; Q1's two examples, generalized; the `funcd.<area>.<name>` pattern, one gauge carries the alert |

## References

- Report §2 (pattern 6), §3, §4.A, §4.E, §4.G, §4.I row 4, §5 (Q1, Q7, Q8, Q13); FEAT-0009; `dr-plan.json` DR-4. Read
  2026-10-08: `cmd/funcd/main.go`, `internal/{platform/config/config.go,kvstore/badger/backup.go,auth/rbac/rbac.go}`,
  `internal/controlplane/{deadletters,server}.go`, `internal/edge/observ/observ.go`; OTel Apache-2.0, age BSD-3.
