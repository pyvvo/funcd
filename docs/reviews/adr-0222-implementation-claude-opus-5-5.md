## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0222 implementation, model: claude-opus-5-5)

Reviewed commit `cf6795c2` (`feat(backup)!: implement ADR-0222`) on `feat/adr-0222-workload-backup-resources`,
62 files, against the ADR's Contracts, Scenarios, Implementation plan, Review checklist and Definition of done.
The repo-wide gate (`just ci-full`, e2e, Linux lint, lanes) runs before the PR; this review ran the checks of the
touched packages.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **An interrupted in-place Restore leaves its namespace held** · attribution: `model` ·
  `internal/services/backup/restore.go:60` fails a `Running` Restore that this process does not run as `Interrupted`,
  but it does not lift the `<ns>.restore` marker that `load` wrote (`restore.go:203`; the deferred release at
  `restore.go:207` dies with the process). `hold release --namespace` releases only the `data` reason
  (`internal/controlplane/hold.go:218`), so after a crash during an in-place load every per-namespace runner of
  that namespace stays held until another in-place Restore completes and lifts the marker. Keeping the namespace
  held over a half-loaded store is defensible (fail closed), but the way out is undocumented. Fix: lift the marker
  on the `Interrupted` path, or document that a new in-place Restore is the release (`examples/workload-backup.md`).

### ✅ Verified correct (keep it)

- **Build, vet, lint, format**: `scripts/agent/d go build ./...` exit 0; `go vet` over the 22 touched packages
  exit 0; `go tool golangci-lint run` over them: `0 issues.`, exit 0; `gofmt -l` on the changed Go files prints
  nothing; `go mod tidy -diff` exit 0.
- **Tests**: `go test -race -count=1` over the touched packages: every package `ok` (`pkg/funcd` 22.5 s,
  `cmd/funcd` 40.0 s, `internal/services/backup` 4.0 s, `internal/backup/workload` 6.9 s, `internal/kvstore/badger`
  33.5 s, …), no `FAIL`, no panic.
- **One test per Scenario, un-skipped and passing** (times from `-race -v`): schedule-runs-at-slot
  `TestScenarioScheduleRunsAtSlot` 0.57 s; schedule-applies-live `TestScenarioScheduleAppliesLive` 0.61 s;
  catch-up-once-no-overlap `TestScenarioCatchUpOnceNoOverlap` 0.96 s; backup-outlives-app
  `TestScenarioBackupOutlivesApp` 0.64 s; api-creates-backup `TestScenarioAPICreatesBackup` 0.24 s;
  catalog-checkpoint-verified `TestScenarioCatalogCheckpointVerified` 0.37 s; target-in-store-refused
  `TestScenarioTargetInStoreRefused` 0.16 s; restore-new-name `TestScenarioRestoreNewName` 0.27 s;
  restore-in-place `TestScenarioRestoreInPlace` 0.32 s; platform-restore-holds-namespace
  `TestScenarioPlatformRestoreHoldsNamespace` (cmd/funcd) 4.13 s; rpo-risk `TestScenarioRPORisk` 0.55 s;
  run-interrupted `TestScenarioRunInterrupted` 0.23 s. Plus the CLI `apply` per kind
  (`TestScenarioCLIApplyWorkloadBackup` 0.35 s, which exercises the `stampTypeMeta` switch through
  `controlplane.NewServer`). No `t.Skip`; the new platform tests call `t.Parallel()`, drive a manual clock, poll with
  `require.Eventually`, and take a short data dir (`os.MkdirTemp("", "funcd")`); no timeout raised.
- **Planned unit tests present and passing**: `TestResolveScopes`, `TestObjectFrame`, `TestCatalogFilesRelative`,
  `TestLoadPrefixRefusesNonEmpty` (badger and memory), `TestBackupNameFromSlot`, `TestRPORiskRule`,
  `TestReleaseNamespace`, and `TestEveryRunnerConsultsHoldInNamespace` (timers, Sensors, runs, blob sources, App,
  schedule slots, dead-letter replay act in the free namespace only); the existing `TestEveryRunnerConsultsHold`
  still passes for the platform-wide runners.
- **Contracts**: `api/types/v1alpha1/backup.go` matches the shapes and JSON keys (required keys without
  `omitempty`); `workload.{Format, Member, Sources, Options, MemberFile, Manifest, Resolve, Write, ReadManifest,
  Integrity, Records, EncodeObject, DecodeObject, CatalogFiles}` have the ADR's signatures; `LoadPrefix` on the
  badger and memory drivers (non-empty ⇒ `fault.Conflict`), `gocloud.StaticKey` / `OpenOptions.Static`,
  `envelope.Parse`, `hold.NamespaceGate`, `NeverNamespace`, `HoldNamespace`, `ReleaseNamespace` (release time
  written before the marker is removed), `MarkNamespaces`, `app.Deps.NamespaceHold`; the nine reasons and the gauge
  `funcd.backup.workload_rpo_risk` are exposed.
- **Decision 3**: `newestDue` takes the newest due slot from `cron.Next`; paused or `HeldIn` keeps
  `lastScheduleTime` so missed slots collapse into one catch-up; an active Backup takes the slot without creating
  one (no overlap); `Conflict` on create counts as done (`schedule.go:57-71`). Names capped at 54 and 56
  (`backup.go:14-17`); `backup-ttl` admission refuses a Backup ttl over its schedule's. A Backup carries the
  schedule's `resourceGroup` and no ownerReference, and `admission.Members` (all namespaced kinds) makes a
  ResourceGroup with Backups refuse a delete unless forced.
- **Decisions 4-5**: every object is put `IfNotExist` (`workload.go:303`), a refused condition is
  `TargetUnconditional`; catalog files before `catalog.db`, the manifest last; `Running` at start ⇒ `Interrupted`;
  a `Pending` Backup waits on the platform hold except a Restore's export.
- **Decision 6**: `s3://` only (`checkS3URL`), recipients through `envelope.Parse` at admission, the store guard
  through `gocloud.CompareDomains` (`OverlapStore` ⇒ 400 naming `spec.target.url`, `OverlapProvider` ⇒ Ready
  reason `SameProvider`); Secrets read with a developer identity of the namespace only; no Secret value in status
  or logs.
- **Decisions 7-8, 10**: in place needs `update` on `Namespace` at admission, runs `<restore>-before` first
  (gone ⇒ `ScheduleNotFound`, failed ⇒ `ExportFailed`, no write), holds the namespace during the load;
  integrity is checked before any write; a failure empties what it wrote. `restore run` writes a `data` marker
  per namespace holding a schedule; `funcdctl hold release --namespace` (repeatable, exclusive with `--advance`)
  authorizes `update` on `Namespace`. Backup and Restore specs are immutable at admission.
- **Dependencies**: `modernc.org/sqlite v1.60.1` as the ADR names; the `golang.org/x/*` bumps are its minimum
  requirements (MVS), `go.sum` tidy.
- **Tracking**: the ADR diff is the one `Accepted → Reviewing` status line; its substance is unchanged.

### Definition of Done

15 / 15 hold (ADR Review checklist 4, ADR Definition of done 1, generic implement-gate DoD 10). `just ci` itself
runs in the PR gate; every check it runs held on the touched packages. The feat row move is left to the tier's
tracking PR by design of this run.

### Model scorecard

To record: claude-opus-5-5 on ADR-0222 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 15/15.

### Recommendation

Sign off: stamp ADR-0222 `Implemented`. The Minor goes back to the builder as a follow-up (lift the restore marker
on `Interrupted`, or document the release); it does not block.
