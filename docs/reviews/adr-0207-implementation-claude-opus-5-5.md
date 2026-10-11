## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0207 implementation, model: claude-opus-5-5)

Branch `feat/adr-0207-pre-upgrade-snapshot-and-safe-mode`, 4 commits on `origin/main` (`cb810ff4`, `ce3afb26`,
`a8f6421c`, `a4cac449`); 38 files, +2591/−123.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **`requireLinuxRoot` no longer points install/uninstall at `--print`** · attribution: `model` ·
  `cmd/funcd/install.go:136-145`. To share the gate with `upgrade --unit`, the messages lost
  "use --print to preview the unit" (off Linux) and "(or use --print)" (non-root), which `funcd install` and
  `funcd uninstall` printed before. This makes the existing commands less helpful and nothing replaces the hint. Fix:
  keep the hint for the install and uninstall callers, for example with a suffix the caller passes or a wrapped error
  in `runInstall` and `runUninstall`.
- **Q13: `funcd upgrade` refuses a held platform** · attribution: `adr` · `internal/upgrade/upgrade.go` `checkHold`,
  test `TestUpgradeWhileHeld`. ADR-0207 does not say what happens when the platform is held. The DR session decided on
  2026-10-10 that the upgrade refuses with `fault.Conflict`, naming the marker and its reason, and that
  `--no-snapshot` goes on with one warning. The model implemented that decision and tested it. The gap belongs in DR's
  addendum ADR. It does not count against the model.

### ✅ Verified correct (keep it)

- **Build, vet and lint on darwin and linux**: `go build ./...` exit 0; `GOOS=linux go build ./...` exit 0;
  `go vet` on the touched packages exit 0 on both OSes; `golangci-lint run ./...` printed "0 issues." on darwin and on
  linux (the tool built for the host and run with `GOOS=linux`, as `scripts/agent/gate.sh` does); `gofmt -l` is
  empty; `go mod tidy -diff` exit 0. There is no `go.mod`/`go.sum` change, so no new module was added.
- **Tests with `-race -count=1`** on `cmd/funcd`, `internal/safemode`, `internal/upgrade`,
  `internal/platform/{config,hold,version}`, `internal/backup/...`, `internal/restore` and `pkg/funcd`: every package
  passed (`ok`).
- **Each of the 12 scenarios has a named test that passes and is not skipped** (`-v` run; this host takes the
  second-group owner path, so the owner tests ran):
  - `internal/upgrade`: `TestScenarioUpgradePinsOldState`, which checks both the `--unit ""` and the `--unit` path and
    the call order `show, stop, start`, with `start` refusing a binary that has not been swapped;
    `TestScenarioSnapshotUnavailableStopsUpgrade`; `TestScenarioWriteFailureKeepsOldBinary`, which checks owners at
    `systemctl start` and an error that names the store's part; `TestScenarioDowngradeRefused`;
    `TestScenarioPreUpgradeRetentionReported`.
  - `cmd/funcd`: `TestScenarioUpgradeKeepsDataOwner`, `TestScenarioRollbackToPreUpgrade`, `TestScenarioManualSwapWarns`,
    `TestScenarioSafeModeHolds`, `TestScenarioStableRunResetsCount`, `TestScenarioSafeModeStopsLoop`,
    `TestScenarioLeaveSafeMode`.
  - The rollback and stop-loop scenarios build funcd twice with `-ldflags -X …version.Version=` (v0.8.0 and v0.9.0),
    as the test plan asks, so the old binary really restores its own pin.
- **The unit tests the plan names all exist and pass**: `TestBeginModes` (N−1, N, 2N−1, 2N; a stopped start leaves
  the file byte-identical), `TestStateRoundTrip` (lowerCamel keys, `at` in ADR-0196's form), `TestParseVersionOutput`,
  `TestCompareVersions` (describe counts, `-dirty`, `-rc.1`, hash and `dev` unordered), `TestExecStartMismatch`,
  `TestSwapLeavesBinaryOnFailure` and `TestUnitsPreventRestartOn70` (both units). Further tests:
  `TestStartCleanFailedRace`, `TestStoppedErrorNamesNextCommand` and `TestStoreHeldConflict` (Q5's flock probe).
- **Mutants (applied with `go test -overlay`; the work was not edited)**, all killed:
  1. `safemode.Begin`: `n >= cfg.AfterCrashes` → `n > …`. `TestBeginModes` fails ("after 3 unclean starts").
  2. `snapshotRun.write`: `Pin: backup.PreUpgrade` dropped. `TestScenarioUpgradePinsOldState` fails.
  3. `snapshotRun.own`: the store directories are not re-owned. `TestScenarioWriteFailureKeepsOldBinary` fails
     ("…/store is owned by …, want …").
- **Decision 1 step order** (checklist 1). Version, `checkUnit` and `checkHold` run first, then `prepare`
  (`CheckBackup`, the metastore check, `envelope`, the s3 probe), all before `systemctl stop`. After the stop come the
  `file://` lock (`Target.Ready`), Q5's flock probe of each store, the opens, and `Target.Write` with `Seal`, `Keys`,
  `Parent: restore.Parent(…)` and `Pin: backup.PreUpgrade`. The swap runs only after a complete pin. A failure after the
  stop calls `restart`. `.previous` is replaced through a hard link and a rename, and both the new file and the
  directory are fsynced (`upgrade.go` `swap`).
- **Owner** (checklist 2). The deferred `own` runs on every return from step 4, before `restart` or `systemctl start`.
  The store directories and a new `lock` take `<storage.dataDir>`'s owner. Under the target, only names that were not
  in the listing taken before the run are re-owned, each to its parent's owner (`listTarget`/`own`).
  `RecordUpgrade` and `Reset` end with `hold.Own` (`writeOwned`).
- **Nothing is deleted** (checklist 3). `internal/upgrade` deletes no object. `pastRetention` only reports, and the
  retention scenario reads back all four older manifests.
- **Counting starts** (checklist 4). `serve` calls `hold.Open`, builds the logger, then calls `safeModeStart`
  (`Begin`) before `buildOptions`. A stopped start returns before any store opens, and the scenario checks that
  `<dataDir>/store` is not recreated. `Clean` runs only from the `stableAfter` timer or after `Run` returns nil.
  `storage.mode: memory` skips the counter. The `*StoppedError` reaches `exitCode` through `loggedError.Unwrap`, which
  gives exit status 70.
- **Marker and release** (checklist 5). The held and stopped paths write the `safe-mode` marker only when
  `!held.Held()`. Release goes through ADR-0206's `funcdctl hold release`. Both units carry
  `RestartPreventExitStatus=70`, and `artifacts_test.go` also asserts it.
- **Package placement** (checklist 6). `internal/safemode` and `internal/upgrade` sit outside `internal/platform/`
  (lint is clean). `version.Compare` orders versions in step 1 and in the manual-swap Warn.
- **Contracts**. The signatures of `Begin`, `Clean`, `Failed`, `Reset`, `RecordUpgrade`, `Run`, `Snapshot` and
  `Compare` match the ADR. So do the types `State`, `Upgrade`, `Config`, `Mode`, `StoppedError`, `Options` and
  `Result`, with one unexported test seam, `Options.version`. The three config keys use the specified JSON and env
  names and the defaults 3, 3 and `10m`. `preUpgrade < 1` is an error row of `CheckBackup`, tested. The
  `recovery.safeMode` bounds are checked in `cmd/funcd` with `parseDuration`.
- **The DR session's plan decisions are followed**. Q2: `buildOptions(…, held)` and `rc.Hold`, with mechanical updates
  at the test call sites. Q3: `Inputs.Parent` through `WithBackupParent(restore.Parent)`, plus the platform-backup case
  in `TestEveryRunnerConsultsHold` and `TestBackupParentWired`. This carries out ADR-0205's obligation to wire the two
  later; it is not scope creep by the model. Q6: `envelope.LoadMaster`, `FromConfig` and `ReadSecretsKey` moved out of
  `package main`. Q10: `hold.WriteFile` exported. Q11: `backup.GenDir` exported, and the prune lines added to
  `examples/backup-lifecycle.md`.
- **Docs**. `docs/install.md` has an Upgrade section. `examples/restore-runbook.md` has "Roll back an upgrade" and
  "Crash loop". `examples/funcdconfig.yaml` gains its keys as block-style YAML comments.
- **Tracking**. The ADR diff is the `Accepted → Reviewing` status line alone, so its substance is unchanged. `docs/feat`
  and `blueprint.md` are untouched, as instructed (DR owns the feat row). The module path is `github.com/pyvvo/funcd`.

### Definition of Done

9 of 9 items hold: the 6 Review-checklist items, plus the ADR's own Definition of done (`go test -race -count=1`
green on the touched packages, no new module, no identity or path leak). The repo-wide parts of `scripts/agent/d just
ci` (the whole-repo `go test` and the e2e suite) were not run here, as instructed; the PR gate runs them. Every
component of `just ci` that this review could run passed: build, vet, lint on both OSes, fmt, tidy, and the touched
packages' tests.

### Model scorecard

Not recorded by this run (instructed: do not edit `docs/reviews` or the ledger). Row to record:

```json
{"date": "2026-10-11", "adr": "0207", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 2, "model_attributed": 1, "dod_passed": 9, "dod_total": 9, "report": "docs/reviews/adr-0207-implementation-claude-opus-5-5.md", "notes": "model: requireLinuxRoot dropped the --print hint for install/uninstall (minor); adr: Q13 upgrade refuses a held platform (DR decision, addendum ADR); Q3 Parent/Hold wiring is ADR-0205's carried obligation, not creep; mutants M1 Begin threshold, M2 PreUpgrade pin, M3 store re-own killed; race ok on 13 packages, lint 0 darwin+linux"}
```

### Recommendation

Pass. Stamp ADR-0207 `Implemented` through the orchestrator's ledger and stamp step. Restoring the `--print` hint is
an optional follow-up for the builder. Q13 goes to DR's addendum ADR.
