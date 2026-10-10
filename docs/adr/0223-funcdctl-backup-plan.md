# ADR-0223: funcdctl backup plan — recovery objectives into backup settings

- **Status**: Accepted (2026-10-10, by an `adr-batch` run after a clean `adr-judge` gate; the defaults below were not confirmed one by one)
- **Date**: 2026-10-10
- **Deciders**: green-0-rabbit
- **Tags**: backup, disaster-recovery, funcdctl, config, cli
- **Realizes**: [FEAT-0009/F112](../feat/0009-feat-disaster-recovery.md) (backup settings helper; DR plan item DR-L1)
- **Supersedes in part**: none.
- **Relates to**: ADR-0205 (`config.CheckBackup`, the `funcdctl backup` group) · ADR-0203 (retention ladder) · ADR-0208
  (`blob.backup.*`, `config.CheckBlob`) · ADR-0209 (`kvstore.backup.*`) · ADR-0222 (Proposed: `BackupSchedule`) ·
  ADR-0211 (cron) · ADR-0206 (drill) · ADR-0194 (durations) · ADR-0061, 0062 (loader) · ADR-0042 (cobra)

## Context & Need

The backup settings are intervals and retention counts: `backup.*` (ADR-0203, ADR-0205), `kvstore.backup.*` (ADR-0209),
`blob.backup.*` (ADR-0208) in `funcdconfig.yaml`, `schedule` and `ttl` in a `BackupSchedule` (ADR-0222). The objectives
(how much data a loss may take, how long a recovery may take, how far back a restore must reach) are the architect's,
outside funcd (report §5 Q1); converting them by hand is error-prone, as one value can break a rule tying it to another.
ADR-0205 made the start-up rules functions of the merged config (`config.CheckBackup`, beside ADR-0208's `CheckBlob`)
for this helper. **Purpose**: `funcdctl backup plan` turns objectives into settings, prints or writes them once the
daemon's own loader accepts them, and says when a recovery time cannot be met. Callers: the operator (config keys), an
app owner (a `BackupSchedule`), an agent (exit codes). Like `types` (`newRootCmdWith`, `cmd/funcdctl/cli.go`), no server.

## Scenarios

- **scenario: platform-from-objectives** — Given no config file and no server, When `funcdctl backup plan platform
  --rpo 1h --keep 720h` runs, Then stdout is `backup.interval: 30m`, `objectives.rpo: 1h`, `retention.hourly: 48`,
  `daily: 30`, `weekly: 0` in block style, stderr carries the daemon's warning that no `backup.target` is set, exit 0.
- **scenario: workload-keys-from-objectives** — Given a config with the KV backup on, When `plan kv --rpo 1m --keep
  168h`, `plan kv --keep 12h` and `plan blob --rpo 2h --keep 720h` run, Then stdout is `kvstore.backup.interval: 30s`,
  `retention.hourly: 48`, `daily: 7`, `weekly: 0`; then `retention.hourly: 25`, `daily: 0`, `weekly: 0` (ADR-0209's
  `rebaseline` rule at its 24h default); then `blob.backup.interval: 1h`, `blob.backup.retention: 721h`; exit 0 each.
- **scenario: schedule-from-objectives** — Given `orders-nightly.yaml` holding one `BackupSchedule` (`0 3 * * *`,
  `ttl: 720h`), When `plan schedule -f orders-nightly.yaml --rpo 2h --keep 336h` runs, Then stdout is that manifest
  with `schedule: 0 * * * *` and `ttl: 336h`, every other byte unchanged, and `funcdctl apply -f -` accepts it; with
  `--rpo 90s` it exits 1 naming `--rpo`, stdout empty.
- **scenario: write-keeps-the-rest** — Given a commented `funcdconfig.yaml` with `backup.target`, two recipients and
  `interval: 1h`, When `plan platform --rpo 30m --write` runs, Then the file holds `interval: 15m` and
  `objectives.rpo: 30m`, every other byte (comments, blank lines) unchanged, and stdout lists
  `backup.interval: 1h → 15m` and `backup.objectives.rpo: (absent) → 30m`.
- **scenario: refused-as-the-daemon-refuses** — Given `--rpo 4h --keep 1h` (interval 2h, `hourly: 1`), or a file
  hand-edited to `interval: 1h`, `objectives.rpo: 30m` and `--keep 720h`, When `plan platform --write` runs, Then it
  exits 1 with the error `funcd` prints at start for the same bytes, and the file is unchanged.
- **scenario: rto-impossible** — Given a config with the KV backup on, When `plan kv --rpo 1m --rto 15m --size 200Gi
  --speed 100Mi --write` runs, Then stderr names a `34m8s` download and the knobs, nothing is printed or written, exit 3.
- **scenario: rto-lower-bound-only** — When `plan platform --rpo 1h --rto 4h --size 2Gi --speed 50Mi` runs, Then
  stderr says the download takes `41s` and only a restore drill (ADR-0206) proves 4h, exit 0.

## Scope

**In**: the verb, flags, derivation, check, print and `--write`, RTO bound, exit codes; `config.Parse`. **Out**: daemon
rules and defaults (ADR-0203, 0205, 0208, 0209); the drill (ADR-0206); the kinds (ADR-0222); a running daemon; rqlite.

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q1 (numbers are keys with defaults; the daemon checks them at start through one
  loader, impossible ⇒ error, tight ⇒ warning; the helper reuses that loader and adds no rule; RTO is no key; app-level
  consistency is found later); Q13 (the config is read at start, a restart applies it; a `BackupSchedule` applies live).
- Fail closed: nothing is written that the daemon would refuse. Never write credentials or recipients (report §4.G).
- `.golangci.yml` depguard: no rule stops `cmd/` importing `internal/platform/config` (`platform-leaf` binds the leaf
  itself). No new module: `go.yaml.in/yaml/v3` v3.0.4 is direct in `go.mod` (used by `pkg/sdk`), MIT and Apache-2.0
  (its LICENSE, read in the module cache 2026-10-10).

## Alternatives considered

| Option | Outcome |
|---|---|
| **`funcdctl backup plan <component>`, offline** ✅; a subcommand of the `funcd` binary; a server route checking a posted config | the objectives' owner is rarely on the box; the daemon reads its config only at start (Q13), so a route would check a file it does not load, and needs an admin token |
| **Flags, one component a call** ✅; a plan file of objectives per component | a file is one more schema to version for three to five values |
| **The daemon's loader on the merged bytes** ✅; the rules re-coded in funcdctl; `CheckBackup` alone on a hand-built `Config` | a copy drifts from the daemon (Q1: one place); without the loader's defaults, env overlay and `Validate`, a value the start refuses could pass |
| **Splice at yaml.v3 node positions** ✅; yaml.v3 decode and re-encode; a `sigs.k8s.io/yaml` rewrite; refuse a commented file | re-encoding drops blank lines and re-indents; `sigs.k8s.io/yaml` drops every comment; `examples/funcdconfig.yaml` is mostly comments |
| **Interval = RPO ÷ 2** ✅; interval = RPO (ADR-0067's reading for the KV export) | one failed run then breaks the RPO, and ADR-0205 Decision 3 warns above half |
| **KV: floor `hourly`** ✅; derive a shorter `kvstore.backup.rebaseline` | `rebaseline` maps to no objective and sets how often a full base is written; the floor only keeps a second day of increments |
| **RTO lower bound = size ÷ speed** ✅; a model of load and boot rates; an `rto` key | the rates are unknown until a drill measures them; Q1: RTO is no key |

## Decision

**1. Command.** `plan` joins ADR-0205's `funcdctl backup` group (`status`, `verify`; the `kvstore` group of
`cmd/funcdctl/kvstore.go` is the shape) and never builds an SDK client (`sdkClient`): `--server`, `--token` are unused.

| Flag (NEW) | Value | Components |
|---|---|---|
| `<component>` | `platform`, `kv`, `blob`, `schedule` | — |
| `--rpo` | the most data a loss may take; ADR-0194 duration | all |
| `--keep` | how far back a restore point must reach; ADR-0194 duration (`720h`, no `d` unit) | all |
| `--rto` | the target recovery time; ADR-0194 duration; needs `--size` and `--speed` | all |
| `--size`, `--speed` | bytes a restore downloads; link bytes per second: an integer with an optional `Ki`, `Mi`, `Gi`, `Ti` suffix (the `parseMemoryBytes` grammar, `internal/provider/runtime.go`) | with `--rto` |
| `--config` | the config file, found as the daemon finds it: `config.Locate` (this flag, `$FUNCD_CONFIG`, `./funcdconfig.yaml`, `/etc/funcd/funcdconfig.yaml`; the daemon's flag of the same name, `cmd/funcd/main.go`) | `platform`, `kv`, `blob` |
| `-f`, `--file` | a manifest holding exactly one `BackupSchedule` document (others pass through); required | `schedule` |
| `--write` | edit the file in place instead of printing | all |

At least one of `--rpo`, `--keep`; `--rto`, `--size`, `--speed` all or none; every given value above zero; no `--write`
with `-f -` (stdin, `readManifest`); else `fault.Invalid` naming the flag. An absent objective sets no key.

**2. Derivation.** Values are written in ADR-0194's normalized form (`v1alpha1.Duration.String`), truncated to the ms.

| Component | From `--rpo R` | From `--keep K` |
|---|---|---|
| `platform` | `backup.interval` = R ÷ 2; `backup.objectives.rpo` = R | `backup.retention.hourly`, `.daily`, `.weekly` = ladder(K) |
| `kv` | `kvstore.backup.interval` = R ÷ 2 | `kvstore.backup.retention.hourly`, `.daily`, `.weekly` = ladder(K), `hourly` floored |
| `blob` | `blob.backup.interval` = R ÷ 2 | `blob.backup.retention` = K + one interval |
| `schedule` | `spec.schedule` = cron(R) | `spec.ttl` = K |

ladder(K) gives (`hourly`, `daily`, `weekly`) in ADR-0203 Decision 5's units, `0` unused, so the longest kept class
covers K: K ≤ 48h ⇒ (⌈K ÷ 1h⌉, 0, 0); K ≤ 720h ⇒ (48, ⌈K ÷ 24h⌉, 0); else (48, 30, ⌈K ÷ 168h⌉). `Derive`'s `cur` is
`config.Parse(path, data, config.Flags{})` (Decision 3) of the located file, or of nil if none; its error is plan's: a
file the start refuses fails before deriving. kv: `hourly` ≥ 24 × ⌊b ÷ 24h⌋ + 1, b = `cur.Kvstore.Backup.Rebaseline`
(`internal/platform/config/config.go`; empty ⇒ 24h, as in `kvBackup`, `cmd/funcd/main.go`), as ADR-0209 Decision 5
refuses a `rebaseline` of ⌈hourly ÷ 24⌉ days or more (default: K ≤ 24h ⇒ 25). blob adds one interval (R ÷ 2, else
`cur`'s, else `1h`): a generation stays restorable for `retention` less one run (ADR-0208 Decisions 1, 4). cron(R): the
largest period P ≤ R ÷ 2 that divides the hour or the day, so slots stay even across boundaries (ADR-0211 Decision 1
grammar): P of 1, 2, 3, 4, 5, 6, 10, 12, 15, 20 or 30 min ⇒ `*/P * * * *` (`* * * * *` for 1 min); 1, 2, 3, 4, 6, 8 or
12 h ⇒ `0 */P * * *` (`0 * * * *` for 1 h); 24 h, also the cap ⇒ `0 0 * * *`. R ÷ 2 under 1 min ⇒ `fault.Invalid` naming
`--rpo` (a cron fires at most once a minute). `spec.timeZone`, `scope`, `target`, `encryption` are never touched.
`rebaseline`, `retryInterval`, `retention.verified`, `retention.preUpgrade` map to no objective and are never set.

**3. Check** (one loader). Config components: plan sets the keys in the located file's bytes (Decision 4; none found ⇒
the derived keys alone) and calls `config.Parse(path, merged, config.Flags{})` (NEW, Contracts): defaults, strict
decode, the `FUNCD_*` overlay of funcdctl's environment, `Validate` with ADR-0205's `CheckBackup` (ADR-0209's
`kvstore.backup.*` rows in it) and ADR-0208's `CheckBlob`. Its error is plan's, unchanged: exit 1, nothing printed or
written. Then each finding of `CheckBackup()` and `CheckBlob()` with `Error` false goes to stderr as
`warning: <key>: <message>`. Not run: ADR-0205 Decision 3's I/O rows (`envelope.New`, `CompareDomains`, `Target.Ready`):
they read targets and recipient files plan never sets, and the start still runs them; nor `--memory`, under which no
backup runs. `schedule`: `apply`'s offline pre-flight (`sdk.DecodeManifestDocuments`, then each document's
`sdk.ReadOnlyKind` refusal and `Validate()`, `applyCmd` in `cmd/funcdctl/cli.go`), moved into `preflight`, which both
call; the server's admission (ADR-0222 Decision 6 guard, Secrets) runs at `funcdctl apply`.

**4. Print and write.** By default stdout carries, for a config component, the derived keys as a block-style fragment
of `funcdconfig.yaml`, nested as the file nests them, and for `schedule` the whole edited manifest, for
`funcdctl apply -f -`. With `--write`:

- **File**: the one `config.Locate` returns (none ⇒ `fault.NotFound` naming `--config`), or the `-f` manifest.
- **Edit**: `go.yaml.in/yaml/v3` parses the bytes into nodes to find each derived key; a present scalar's text is
  replaced in place; a missing key (or parent) is inserted after its parent mapping's last line at the indentation of
  that mapping's first child (top level: at the end). Every other byte, comments and blank lines included, stays. A
  value is double-quoted when a plain scalar would read back differently (`*/30 * * * *`). A key under an alias, an
  anchor, a block scalar or a flow collection, or a config file of several documents ⇒ `fault.Invalid` naming the key.
- **Commit**: the edited bytes pass Decision 3; then the target's symlinks are resolved (`filepath.EvalSymlinks`), a
  temp file beside the resolved file, `fsync`, rename over it, mode kept, owner and group kept where `chown` is allowed.
  stdout lists `<key>: <old> → <new>` per change (`(absent)` for a new key); stderr ends with `restart funcd to apply`
  (Q13), or for `schedule` with `funcdctl apply -f <file>` (applies live).
- For `platform` with `--rpo`, stderr also names ADR-0205 Decision 6's verify cadence, (rpo − interval) ÷ 2.

**5. RTO, a lower bound only.** B = ⌈size ÷ speed⌉ seconds, the download alone; loading, the held boot and the release
are not counted, and only a restore drill (ADR-0206 scenario first-drill, which logs its time) measures a recovery. RTO
below B ⇒ `*plan.RTOError`: stderr `RTO <rto> is impossible: the download alone takes <B>. Knobs: a faster or closer
target (--speed), a smaller scope (--size).`, nothing on stdout, nothing written. Else stderr `RTO <rto>: the download
takes <B>; only a restore drill (ADR-0206) proves the target.` plan reads no size from a target or the daemon; the
operator gives it (a generation's `stores[].bytes`, ADR-0203 Decision 3, is one source).

**6. Exit codes.** 0: settings accepted, printed or written (warnings and notes on stderr). 1: a bad flag or file, a
key plan cannot edit, the loader's or `Validate()`'s error, a failed write; nothing written (every funcdctl error today,
`cmd/funcdctl/main.go`). 3 (NEW): settings accepted, `--rto` below B; nothing printed or written. The RTO is checked
after the settings, so 1 wins over 3; `main` maps an error with `ExitCode()` to that code, any other to 1.

## Temporary workarounds

None.

## Contracts

```go
package plan // internal/backup/plan (NEW): derivation, check, edit; cmd/funcdctl/backup.go wires flags and files

type Component string; const Platform, KV, Blob, Schedule Component = "platform", "kv", "blob", "schedule"
type Objectives struct{ RPO, Keep, RTO time.Duration; Size, Speed int64 } // 0: not given (Decision 1); bytes, bytes/s
type Setting struct{ Key, Value string }   // Key: dotted path from the document root; Value: the scalar's text
type Change struct{ Key, Old, New string } // Old "" ⇒ the key was absent
type RTOError struct{ Target, Bound time.Duration }
func (e *RTOError) Error() string // Decision 5's impossible line
func (e *RTOError) ExitCode() int // 3
func Derive(c Component, o Objectives, cur config.Config) ([]Setting, error) // Decision 2; cur: Parse of the located file
func Ladder(keep time.Duration) (hourly, daily, weekly int)
func Cron(rpo time.Duration) (string, error)
func Apply(doc []byte, kind v1alpha1.Kind, s []Setting) ([]byte, []Change, error) // Decision 4; kind "" ⇒ the only document
func CheckConfig(path string, merged []byte) ([]config.Finding, error) // Decision 3; warnings only, errors as config.Parse's
func RTOBound(size, speed int64) time.Duration                       // ⌈size ÷ speed⌉ s, capped at MaxDuration; both > 0
func CheckRTO(o Objectives) (bound time.Duration, err error)         // no RTO ⇒ 0, nil; below the bound ⇒ *RTOError
// internal/platform/config, NEW: Load(path, flags) = os.ReadFile(path) + Parse(path, data, flags), messages unchanged
func Parse(name string, data []byte, flags Flags) (Config, error) // data nil ⇒ defaults; name only in messages
// internal/platform/units (NEW leaf): parseMemoryBytes moved here; internal/provider calls it
func ParseBytes(s string) (int64, bool) // also false on an overflow, which parseMemoryBytes let wrap
func preflight(docs []sdk.ManifestDocument) error // cmd/funcdctl (NEW): applyCmd's ReadOnlyKind + Validate loop
```

| consumes | exposes |
|---|---|
| `config.Locate`, `Flags`, `Finding`; ADR-0205 `CheckBackup`, keys `backup.interval`, `.objectives.rpo`, Decision 6's cadence, the `backup` group; ADR-0203 keys `backup.retention.{hourly,daily,weekly}`; ADR-0209 `kvstore.backup.retention.*`, `.rebaseline` (read) and its `CheckBackup` rows; `kvstore.backup.interval` (ADR-0067); ADR-0208 `CheckBlob`, `blob.backup.interval` (default `1h`), `.retention`; ADR-0222 `BackupSchedule` `spec.schedule`, `spec.ttl`, `Validate()`; ADR-0211 grammar; `sdk.DecodeManifestDocuments`, `ReadOnlyKind`; `v1alpha1.ParseDuration`, `Duration.String`; `go.yaml.in/yaml/v3` v3.0.4 | `funcdctl backup plan` and its flags; exit code 3; `internal/backup/plan`; `config.Parse`; `units.ParseBytes` |

## Implementation plan

**Files**: NEW `internal/backup/plan/{derive,edit,check}.go`, `internal/platform/units/units.go`;
`cmd/funcdctl/backup.go` (ADR-0205's group gains `planCmd`, which hands `Derive` the located file's `config.Parse`),
`cmd/funcdctl/cli.go` (the pre-flight out of `applyCmd`), `cmd/funcdctl/main.go` (exit code via `ExitCode()`);
`internal/platform/config/config.go` (`Parse`, `Load` calls it); `internal/provider/runtime.go` (calls
`units.ParseBytes`); `examples/backup-lifecycle.md` (ADR-0203's) gains the plan commands. **Order**: after ADR-0205
(`CheckBackup`, the group), ADR-0208 (`CheckBlob`), ADR-0209 (its rows in `CheckBackup`), ADR-0211 (`CheckCron`) and
ADR-0222 (`BackupSchedule`). **go.mod**: none. **Blueprint** (at acceptance): the "Backup & disaster recovery" bullet
gains "a funcdctl helper turns recovery objectives into these settings (ADR-0223)".

**Test plan**: one `TestScenario<Name>` per scenario in `cmd/funcdctl`, driving `newRootCmdWith` with `t.TempDir()`
files, `t.Setenv("FUNCD_CONFIG", "")` and no server. Units: `TestFlagRules` (each Decision 1 refusal), `TestDeriveTable`
(each Decision 2 row, `cur` by `config.Parse`; the kv floor at `rebaseline` unset, 12h, 24h, 48h, and 48h by
`FUNCD_KVSTORE_BACKUP_REBASELINE`; blob's added interval), `TestLadder` (24h, 25h, 48h, 49h, 720h, 721h bounds),
`TestCron` (each period, the 1 min floor, the 24 h cap, each output passing `CheckCron`), `TestApplyKeepsBytes`
(comments, blank lines, a missing parent, a quoted cron, an alias refused, a symlinked file), `TestCheckMatchesParse`
(each `CheckBackup` and `CheckBlob` error row: plan's error equals `config.Parse`'s), `TestRTOBound` (the cap),
`TestParseBytesMoved` (`parseMemoryBytes`'s cases, `0`, an overflow). **Definition of done**:
`scripts/agent/d go test -race -count=1`, `scripts/agent/d just ci` green; no new module; no identity or path leak.

## Review checklist

- [ ] `plan` builds no SDK client; flags and their pairing match Decision 1.
- [ ] `Derive` matches Decision 2's table, ladder and cron rule, and sets no other key (no target, credential, recipient, timeZone, scope).
- [ ] Config components pass through `config.Parse` (the code `Load` runs) before `Derive` and any output; schedule through
      `preflight`; plan holds no rule of its own beyond the flag rules.
- [ ] `--write` changes only the derived keys' bytes and refuses Decision 4's forms; its commit matches Decision 4.
- [ ] The RTO bound and exit codes match Decisions 5 and 6; a test per scenario.

## Consequences

**Positive**: objectives map to settings the same way every time, through the start's rules; a hand edit that breaks a
rule fails plan and the start with one message; an agent reads the outcome from the exit code. **Negative (accepted)**:
plan sees funcdctl's `FUNCD_*` environment and working directory (relative paths, the loader's `filepath.Abs`), not the
daemon's; the I/O checks wait for the start; one component per call; KV and blob objectives have no rule in the daemon
(no `objectives.rpo` there), so nothing alerts on them beyond ADR-0205's streams; the RTO bound ignores load and boot;
at fall-back a DST `spec.timeZone` stretches one gap a year by an hour (ADR-0211), so a schedule may pass R by that
hour. **Risk**: an RPO above 96h at the default `retention.verified` (2 days) draws ADR-0205's verify warning, which
plan reports but does not fix.

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Why |
|---|---|---|
| Flag names; input format; the impossible-RTO exit code | Decision 1's flags; no plan file; 3 | the report's names; one call holds three to five values; apart from 1 (every error) and 2 (often a usage error) |
| `--write` on a commented file | the splice of Decision 4; refuse aliases, anchors, block scalars, flow parents | keeps the operator's comments; a guess in a rare form could corrupt the file |
| RTO estimate inputs | `--size`, `--speed` only; a drill-time input after ADR-0206's first drill has measured one | load and boot rates are unknown before it |
| Seed current values from ADR-0205's status route | no: the located file is the current value | the route carries only `interval` and `rpo`, needs a server and an admin token, and the daemon applies a file only at start |
| Which environment plan checks | funcdctl's own `FUNCD_*`, as `config.Parse` reads it | the same loader; the alternative ignores the environment, a second code path |
| Daemon rules this helper lacks (Q1: none added here) | no `objectives.rpo` or rpo rule for `kvstore.backup`, `blob.backup` or a `BackupSchedule`; a rule needs an ADR superseding ADR-0209 or ADR-0208 in part, or Q1's app-level guarantee | the helper derives these intervals but nothing checks them against an RPO |
| ADR-0205's hand-off to F112: an age gauge, a `backup status` exit code | not taken here: both read the running daemon; a follow-up ADR, noted on FEAT-0009's F112 row | one topic per ADR |
| `retention.verified` for a large RPO; App `backupSchedules` entries; rqlite | not derived; not edited; added by the ADR that adds the rqlite keys | no objective maps to the first; the second is a section of another document |

## References

- Report §4.F step 7, §4.G, §5 (Q1, Q13); FEAT-0009 F112 and its exit criterion; `docs/roadmap/dr-plan.json` (DR-L1);
  the board card "funcdctl backup plan". Code read 2026-10-10: `cmd/funcdctl/{main,cli,kvstore,manifest}.go`,
  `cmd/funcd/main.go` (`--config`, `checkKVStoreConfig`, `kvBackup`), `internal/platform/config/config.go` (`Locate`,
  `Load`, `Validate`), `internal/provider/runtime.go` (`parseMemoryBytes`), `api/types/v1alpha1/duration.go`,
  `pkg/sdk/sdk.go` (`DecodeManifestDocuments`), `.golangci.yml`, `go.mod`.
