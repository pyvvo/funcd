# ADR-0206 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0206 implementation, model: claude-opus-5-5)

Reviewed commit `429c8b09` (`feat(restore)!: implement ADR-0206 — Restore and held boot`) against the ADR's
Contracts, Scenarios, Implementation plan, Review checklist and Definition of done, the blueprint and ADR-0002.
Repo-wide checks (`just ci-full`, the e2e suite, the Linux lint, the lanes) run once at the PR gate and are not
repeated here.

### Verification run

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| vet | `go vet` on the 12 touched packages | exit 0 |
| lint | `go tool golangci-lint run` on the touched packages | exit 0, `0 issues.` |
| tests | `go test -race -count=1` on `cmd/funcd`, `cmd/funcdctl`, `internal/{app,controlplane/...,eventing,platform/hold,restore,sensor,services/kv,workflow}`, `pkg/funcd`, `pkg/sdk` | exit 0, every package `ok` |
| skips | `go test -v -run 'Scenario\|Hold\|…'` filtered for `SKIP`/`FAIL` | none (the owner scenario ran its group case) |
| fmt | `gofmt -l` on the changed Go files | empty |
| deps | `go mod verify` | `all modules verified`; `golang.org/x/mod` moved indirect → direct, as the plan names |
| ADR diff | `git diff origin/main...HEAD -- docs/adr` | only the status line `Accepted → Reviewing` |

### Minor

- **The first-drill test is not in `tests/e2e`, and it does not set `server.network.egress: true`** · attribution:
  model · the test plan places `TestScenarioFirstDrill` in `tests/e2e` and Decision 1 runs the drill with
  `server.network.egress: true`; the test is `cmd/funcd/restore_test.go:605` on the default config. It does
  exercise the drill end to end (two age recipients, escrowed keys, restore, held boot, counts per kind equal,
  a read of each kind, time logged), and the runbook (`examples/restore-runbook.md`) states the egress setting.
  Fix: move or mirror it under `tests/e2e`, with the egress setting.
- **`restore run` spools parts into `storage.dataDir`** · attribution: model · `internal/restore/run.go`, `load(…,
  r.dataDir, …)` creates `restore-<store>-*` temporaries there. Each is removed after its load, but a kill during a
  fetch leaves one beside `restore.inprogress`; the emptiness check covers only the three store directories, so a
  rerun still works and the file stays as litter that no step removes. Fix: spool to the metastore directory the
  undo empties, or remove matching temporaries in `undo` and at `Begin` of a rerun.

### ✅ Verified correct (keep it)

- **Contracts match**: `internal/platform/hold` has the `MarkerFile`/`BusyFile`/`ReleasedFile` names, `Marker`,
  `Gate`, `Never`, `Hold`, `Write`, `Begin`, `End`, `Open`, `Own`, `Marker()`, `Release(now)` with the signatures
  and error kinds of the Contracts block; `internal/restore` has `Point`, `Generation`, `Report`, `Options`, `View`,
  `ParsePoint`, `List`, `Resolve`, `Run`, `CheckVersion`, `Inspect`, `Diff`, `Export`, `Parent`; `BlobWatcher.Advance`
  and `Pending`, `kv.(*Reconciler).Orphans`, `bucketOrphans` and `app.Deps.Hold` are present.
- **`restore run` order (checklist 1)**: memory mode, the three directories, point, readable (format, Decision 4),
  recipient match once, `CheckSecretsKey`, `PlanMaster`, all before `hold.Begin` and the marker; each store's parts
  checked against the manifest's bytes and `sha256` before its `Load`; `store.New` then the canary on the new
  timeline; runs paused; master installed; `restore.json`; stores closed; `Own`; `End`. Any error after `Begin` runs
  `undo` (stores emptied, report, its own marker, the master restored or removed, `End`); a left `restore.inprogress`
  makes `hold.Open` refuse the start with `fault.Conflict`.
- **Every Decision 6 runner asks the gate (checklist 2)**: timer tick, blob watcher sweep and poll (the sweep is
  deferred to the first un-held tick), Sensor `deliver` and `Replay` (`fault.Unavailable`), run reconciler (requeue,
  status untouched), both retention loops, the boot reclaims in `(*Platform).Run` and `reclaimOrphanTables`, and the
  App reconciler (requeue at `SupervisionPeriod`, deadline from `ReleasedAt`). The backup loops of ADR-0205/0208/0209
  are not built yet; the conformance test says they add their cases, as the plan orders. No gate reads
  `spec.paused`; the release validates every `--advance` and the held state before any change, and a failed
  `Advance` keeps the marker.
- **Inspect (checklist 3)** loads into memory engines and spools only to the temp directory; Secrets list by key
  without `--secrets-key`, and `Export` redacts values unless `--reveal-secrets`.
- **Every scenario has a named, passing test**: restore-refuses-non-empty, restore-crash-refuses-start,
  restore-keeps-data-owner, restore-boots-held (5 s across a restart), held-rollout-deadline, restore-integrity,
  version-rule, restore-points, single-secret-restore, runs-held-as-evidence, dead-letters-held,
  blob-replay-or-advance, first-drill; plus the units and the two conformance tests the plan names
  (`TestPausedRunWithoutRecordStaysStill`, `TestEveryRunnerConsultsHold`). Platform tests use `shortDataDir`.
- **Routes**: `GET …/hold` and `POST …/hold/release` authorize `get`/`update` on `WorkerNode`, are in the committed
  OpenAPI spec through `RegisterStubHold`, and `funcdctl hold status|release [--advance]` and the SDK reach them.
- **Conventions**: `api/fault` kinds throughout, ctx-first, no `panic` or `fmt.Print*` outside `main`, the one
  global (`hold.Never`) is the contract's and carries its `nolint` reason, imports at top level, refactors of
  `ReclaimDeleted`/`reclaimDeletedBuckets` keep the old behavior and only split out the listing.
- **Tree**: every file of the Implementation plan exists; `internal/app/revision.go` needed no change because its
  `deadline` already reads `ReleasedAt` (ADR-0212). The extras (`hold.Evidence`, `WriteReport`,
  `controlplane.HoldService`, `restore.Counts`) serve Decision 1's status and the report.

### Definition of Done

13 / 14 items hold (ADR Review checklist 3, ADR DoD 2 checked here: race tests green and no identity leak; generic
DoD 9 applicable here). Miss: the first-drill test's level and config (model, Minor). `just ci`, the e2e lane and
the Linux checks run at the PR gate. The feat row F109 still reads `accepted`: the tier's tracking PR records it.

### Model scorecard

Not recorded here: the tier's tracking PR adds the ledger row (claude-opus-5-5 on ADR-0206 implementation → pass,
0/0/2, 2 model-attributed, DoD 13/14).

### Recommendation

Pass: the ADR moves to `Implemented`. The two minors can follow as a small cleanup; neither changes behavior on the
shipped path.
