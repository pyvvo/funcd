# ADR-0286: funcdctl backup plan prints an example and edits no file

- **Status**: Reviewing 2026-10-25; Accepted (2026-10-25, under the decider's delegation; judged once)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: backup, disaster-recovery, funcdctl, config, lake
- **Realizes**: [FEAT-0009/F200](../feat/0009-feat-disaster-recovery.md) (the backup plan prints an example and edits
  nothing)
- **Answers**: the decider's choices of 2026-10-25 (a refine brief): "the plan should just give an example config not
  mutate the real config", and 2A, the verify job shown in the example as a comment ·
  [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md)'s open question whether
  `plan lake --rpo` should stop writing `lake.saveInterval`: it stops
- **Supersedes (in part)**: [ADR-0223](0223-funcdctl-backup-plan.md) Decision 1's `--write` row and its "no `--write`
  with `-f -`" rule, and Decision 4's `--write` (its file, edit and commit bullets), with the scenario
  write-keeps-the-rest · [ADR-0228](0228-dr-implementation-addenda-two.md) Decision 7.1 (the verify cadence on stderr),
  with the scenario plan-prints-cadence-without-write ·
  [ADR-0274](0274-a-lake-saves-its-catalog-at-the-interval-its-recovery-objective-sets.md) Decision 4's derivation of
  `lake.saveInterval` from `--rpo` and its 500 ms refusal, with the scenarios plan-lake-rpo and
  plan-lake-rpo-too-small and the plan row of its Contracts ·
  [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md) Decision 1's "or with
  `funcdctl backup plan lake --rpo`"
- **Relates to**: [ADR-0205](0205-backup-operation.md) (Decision 6, the verify cadence; `rpoRisk`) ·
  [ADR-0208](0208-blob-store-backend-and-backup-target.md) (the blob mirror) ·
  [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md) (a Lake's catalog is a blob object) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model rule)

## Context & Need

`funcdctl backup plan` (ADR-0223) turns recovery objectives into backup settings, offline. With `--write` it edits the
located `funcdconfig.yaml` in place: a splice guided by the YAML nodes, then a temp file, an fsync and a rename
(`cmd/funcdctl/backup.go:313-383`). One helper run thus changes what funcd does at its next start, with no review of the
file. Since ADR-0279, the one key `plan lake --rpo` derives, `lake.saveInterval` (`internal/backup/plan/derive.go:98-106`),
is the opt-in that makes a Lake answer a commit before its catalog save: planning a Lake's objective weakens the Lake
(ADR-0279's Consequences). And the job that keeps `rpoRisk` off, since `rpoRisk` follows the newest *verified*
generation (`internal/backup/runner/runner.go:406-409`), is ADR-0205 Decision 6's off-the-box `funcdctl backup verify`,
whose cadence is only a stderr line (ADR-0228 Decision 7.1) that a saved example loses.

**Purpose**: the plan prints an example the operator reads and copies. It edits no file, sets no Lake key, and carries
the verify job in the example. Callers: the operator (config keys), an app owner (a `BackupSchedule`), an agent (exit
codes).

## Scenarios

- `scenario: plan-edits-nothing` — Given a commented `funcdconfig.yaml` with `backup.target`, two recipients and
  `interval: 1h`, When `funcdctl backup plan platform --rpo 30m` runs, Then stdout is `backup.interval: 15m` and
  `backup.objectives.rpo: 30m` in block style with the verify comment, and the file is unchanged byte for byte; with
  `--write` added, it exits 1 on an unknown flag and prints nothing. Replaces ADR-0223's write-keeps-the-rest.
- `scenario: plan-shows-verify-job` — Given no config file, When `plan platform --rpo 1h --keep 720h` runs, Then the
  example's last line is `# run funcdctl backup verify every 15m, off the funcd host: (backup.objectives.rpo −
  backup.interval) / 2 (ADR-0205)`, and stderr holds only the daemon's no-target warning. Replaces ADR-0228's
  plan-prints-cadence-without-write.
- `scenario: plan-lake-sets-nothing` — Given a `funcdconfig.yaml` without `lake.saveInterval`, When
  `plan lake --rpo 10s` runs, Then stdout holds only comments: no key to set, a Lake saves its catalog before it answers
  a commit, if the blob store is lost a Lake comes back from the blob mirror, `funcdctl backup plan blob --rpo 10s`, and
  an App's own backups of its Lakes are a BackupSchedule's, `funcdctl backup plan schedule -f <manifest> --rpo 10s`; the
  file is unchanged, exit 0, and `--rpo 800ms` exits 0 too. Replaces ADR-0274's plan-lake-rpo and
  plan-lake-rpo-too-small.
- `scenario: plan-lake-names-the-opt-in` — Given `lake.saveInterval: 1s` in the file, When `plan lake --rpo 10s` runs,
  Then the comments say that `1s` makes a Lake answer a commit before its save, so a crash can lose up to that interval
  and the two saves around it, and that without the key a Lake saves before it answers; exit 0.
- ADR-0223's refused-as-the-daemon-refuses and rto-impossible hold without `--write`: the same exit codes (1 and 3),
  nothing on stdout, the file unchanged.

## Scope

**In**: what the plan prints and its write path; the `lake` component's derivation; where the verify cadence appears.
**Out**: the keys and their rules (ADR-0203, 0205, 0208, 0209, 0274, 0279): `lake.saveInterval` stays an opt-in, set by
hand; `funcdctl backup verify`; the daemon.

## Constraints & Decision drivers

- The decider, 2026-10-25: "the plan should just give an example config not mutate the real config"; 2A, the printed
  example shows the verify job as a comment.
- ADR-0223 Q1 holds: the helper reuses the daemon's loader and adds no rule. Q13 holds: a config key applies at the next
  start, a `BackupSchedule` at `funcdctl apply`.
- ADR-0279: without `lake.saveInterval` a crash loses no answered commit, so no objective should set the key.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Print only; `lake` sets no key and says why; the verify job as a comment** ✅ | the decider's call; the real config changes only by the operator's hand | the operator copies the keys into the file | chosen |
| B. Keep `--write` behind a confirmation prompt | one step for the operator | it still edits the real config, and an agent answers the prompt | rejected: the decider asked for an example only |
| C. Print the whole merged file | one file to drop in | it repeats every unrelated key and hides the change in a large diff; replacing the file loses a hand edit made meanwhile | rejected |
| D. Remove the `lake` component | less code | `plan lake` would answer "unknown component", not what bounds a Lake's loss | rejected |
| E. Keep the cadence on stderr beside the comment | a script that reads stderr keeps it | two copies of one fact; stderr is gone once the example is saved | rejected (2A) |

## Decision

1. **Print only** (replaces ADR-0223 Decision 1's `--write` row and its `-f -` rule, and Decision 4's `--write`).
   `funcdctl backup plan` writes no file. The `--write` flag is removed: cobra refuses it as an unknown flag, exit 1.
   stdout stays ADR-0223 Decision 4's print: the derived keys as a block-style fragment of `funcdconfig.yaml` for
   `platform`, `kv` and `blob`, Decision 3's comments for `lake`, and the whole edited manifest for `schedule`, for
   `funcdctl apply -f -`. Plan still reads the located file and runs ADR-0223 Decision 3's check on it with the keys
   spliced in memory by `plan.Apply`: a merge the start refuses exits 1 with the start's error and prints nothing.
   `Apply` returns the edited bytes only; its `Change` list, which only `--write` printed, goes.
2. **The verify job in the example** (replaces ADR-0228 Decision 7.1). For `platform` with `--rpo R`, the fragment ends
   with one comment line, the job ADR-0205 Decision 6 gives the operator:
   `# run funcdctl backup verify every V, off the funcd host: (backup.objectives.rpo − backup.interval) / 2 (ADR-0205)`,
   where V = (R − R ÷ 2) ÷ 2, truncated to the ms: the value of today's stderr line, which goes. Without `--rpo`, no line,
   as before. stderr keeps the loader's warnings and the RTO lines (ADR-0223 Decisions 3 and 5).
3. **A Lake sets no key** (replaces ADR-0274 Decision 4's derivation and its 500 ms refusal). `plan lake --rpo R`
   derives nothing: without `lake.saveInterval` a Lake saves its catalog before it answers a commit (ADR-0279
   Decision 1), and the key is the opt-in to answering first. A Lake's catalog and data files are blob objects
   (ADR-0271), so after a loss of the blob store a Lake comes back with it from the blob mirror (ADR-0208, `funcd restore
   blob`), whose interval bounds that loss. The example is comments only:

   ```text
   # lake: no key to set: a Lake saves its catalog before it answers a commit (ADR-0279).
   # If the blob store is lost, a Lake comes back from the blob mirror: funcdctl backup plan blob --rpo <R>
   # An App's own backups of its Lakes are a BackupSchedule's (scope.lakes): funcdctl backup plan schedule -f <manifest> --rpo <R>
   ```

   When the loaded config sets the key (in the file or as `FUNCD_LAKE_SAVE_INTERVAL`, the loader's overlay), the first
   line is replaced by two:

   ```text
   # lake: lake.saveInterval <v> answers a commit before its save: a crash can lose up to <v> and the two saves around
   # it (ADR-0274). Without the key a Lake saves its catalog before it answers a commit (ADR-0279).
   ```

   `<R>` is `--rpo` in ADR-0194's normalized form and `<v>` the key's text. `--rpo` stays required for `lake`, with no
   floor; `--keep` and `-f` stay refused (ADR-0274 Decision 4).

## Design model

None: the Decision changes a command's output and removes its file write. It adds no stop, race, time rule, retry or
conditional write (ADR-0272 Decision 1).

## Temporary workarounds

None.

## Contracts

The delta; everything else is ADR-0223's.

```go
// package plan (internal/backup/plan)
func Apply(doc []byte, kind v1.Kind, s []Setting) ([]byte, error)            // was ([]byte, []Change, error); Change goes
func Derive(c Component, o Objectives, cur config.Config) ([]Setting, error) // Lake: no Setting and no floor

// package main (cmd/funcdctl): planFlags loses write; commitFile and planResult go
func (a *cli) planFinish(stderr io.Writer, o plan.Objectives, notes []string, printed []byte) error
```

| Command | stdout | stderr | Exit |
|---|---|---|---|
| `plan platform\|kv\|blob …` | the derived keys; `platform` with `--rpo` adds Decision 2's comment | the loader's warnings, the RTO line | 0, 1, 3 |
| `plan lake --rpo R …` | Decision 3's comments | the loader's warnings, the RTO line | 0, 1, 3 |
| `plan schedule -f F …` | the edited manifest | the RTO line | 0, 1, 3 |
| any, with `--write` | nothing | cobra's `unknown flag: --write` | 1 |

## Implementation plan

1. `internal/backup/plan`: `derive.go`'s `Lake` case returns no setting; `edit.go`'s `Apply` and `set` drop the change
   list and `Change`; the doc comments that name `--write`.
2. `cmd/funcdctl/backup.go`: remove the flag, the `--write` refusals (`-f -`, no config file), `commitFile` and
   `planResult`; `planFinish` prints; Decision 2's comment; Decision 3's comments; the `Short` and `Long` help.
3. Tests, one per scenario: `TestScenarioPlanEditsNothing`, `TestScenarioPlanShowsVerifyJob` (today's
   `TestScenarioPlatformFromObjectives`, updated), `TestScenarioPlanLakeSetsNothing`, `TestScenarioPlanLakeNamesTheOptIn`
   in `cmd/funcdctl/backup_plan_test.go`. `TestScenarioWriteKeepsTheRest`, `TestScenarioPlanLakeRPO`,
   `TestScenarioPlanLakeRPOTooSmall` and the symlink subtest go. The refusal, RTO and pre-flight tests run without
   `--write`, each still asserting the file unchanged; `TestFlagRules` loses its `--write` rows and gains `--write` as
   an unknown flag; `TestApplyKeepsBytes` checks bytes only. `internal/backup/plan/plan_test.go`: the Lake derive cases.
4. Docs: the plan section of `examples/backup-lifecycle.md`; the `lake.saveInterval` comment in
   `examples/funcdconfig.yaml` and the `SaveInterval` doc in `internal/platform/config/config.go` (set by hand);
   the F200 row and F112's "or writes them"; the back-links in ADR-0223, ADR-0228, ADR-0274 and ADR-0279 at
   acceptance. `docs/PROJECT-SUMMARY.md` (its ADR-0223 and ADR-0274 rows, an ADR-0286 row) is logged once with the
   batch that ships this ADR, through the project-summary skill.
5. Checks: `scripts/agent/d go test -race ./cmd/funcdctl/ ./internal/backup/plan/`, `go vet` and `golangci-lint` on
   both packages, also with `-tags dev` for `cmd/funcdctl`, and `go build ./...`.

## Review checklist

- [ ] No path of `funcdctl backup plan` creates, renames or writes a file; `--write` is an unknown flag; `commitFile` is
  gone.
- [ ] Each component prints Decision 1's output only after the merged bytes pass `CheckConfig`; a refusal prints nothing.
- [ ] `platform --rpo R` ends its example with Decision 2's line and V as specified; stderr has no cadence line.
- [ ] `lake` derives no key and has no floor; its comments match Decision 3, the key-set variant included; `--keep` and
  `-f` stay refused.
- [ ] `Apply` returns bytes and an error only; no `Change` remains.
- [ ] The four scenario tests pass; every refusal test asserts the file unchanged.

## Consequences

- **(+)** A plan run changes nothing funcd reads: the operator reviews the example and edits the file, and the start's
  loader still refuses a bad one.
- **(+)** Planning a Lake no longer opts it into interval saves.
- **(+)** The verify job stays with the saved example.
- **(−)** The operator copies the keys by hand; ADR-0223 Decision 4's comment-keeping splice now serves only the check
  and `schedule`.
- **(−)** A script that passes `--write` exits 1.

## Open questions

None.

## References

#1277, the feature issue · the decider's refine brief, 2026-10-25 · ADR-0223 · ADR-0228 · ADR-0274 · ADR-0279 ·
ADR-0205 Decision 6 · ADR-0208 ·
ADR-0271 · ADR-0272 · `cmd/funcdctl/backup.go:98-383` · `internal/backup/plan/derive.go:98-106`, `edit.go:19-85` ·
`internal/backup/runner/runner.go:406-409` · experiment 0011 (pyvvo/experiment, branch exp/host-loss): a host loss
rebuilt from the backups, whose duckdb lane brings a Lake back from the blob mirror
