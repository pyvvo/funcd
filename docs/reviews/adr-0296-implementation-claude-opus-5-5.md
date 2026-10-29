## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0296 implementation, model: claude-opus-5-5)

This review covers ADR-0296's work on `fix/limits-followup` at a7cccf39: 15f4fc87, plus its wiring in b7e6798e (the floor
handed to the Lake engine runtime, and `cmd/funcd`). The checks ran in a worktree detached at a7cccf39.
`scripts/agent/gate.sh` runs once on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`, on darwin and with `GOOS=linux`: ok.
- `go test -race -count=1` on the packages the branch touches: ok. That includes `internal/quota`, `internal/blob`,
  `internal/funclog`, `internal/services/kv`, `internal/services/sql`, `internal/platform/config` and `pkg/funcd`.
- `go vet` on the touched packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux`: ok. `golangci-lint run`, on darwin
  and with `GOOS=linux`: 0 issues.
- Four mutants, with `go test -overlay`. Each one fails a test:
  - `Floor.Check` ignoring the write's size fails `TestScenarioFloorCountsTheWrite`.
  - `Quoted` dropping the zero `Subject` fails `TestQuotedAsksOnlyCappedSubjects` in `internal/blob`.
  - `Guard` also asking a prefixed subject, which would count a write twice, fails `TestFloorGuard`.
  - funclog keeping a refused record fails `TestScenarioFunclogDropsBelowFloor`.
- The kv-counter lane's floor case (`e2e/kv-counter.venom.yml:131`) passed in the integration session on b7e6798e. In
  it, funcd restarts with a floor above its free space, a KV put gets 507 naming `storage.minFreeSize`, and the put is
  stored once the floor is lowered.

### Minor
- **`platform-writes-continue` tests half its scenario** (`model`). The scenario says that below the floor both "an
  operator applies a resource" and "a workflow run records its state" succeed. `TestScenarioPlatformWritesContinue`
  (`pkg/funcd/floor_internal_test.go:68`) checks only the apply. Run state never reaches the floor, so this is a test
  gap, not a defect. A run recorded below the floor would close it.
- **`storage.minFreeSize` is not checked at load** (`model`). `Config.validate` runs `CheckLake` and the other key
  checks (`internal/platform/config/config.go:704`), but nothing calls `StorageMinFreeBytes`. A value that does not
  parse passes `config.Load` and fails in `buildOptions`. That runs after the safe-mode count, so a typo there counts
  as a failed start, while a bad `lake.catalogMaxSize` is refused before the start counts. A `CheckStorage` finding in
  that list would fix it.

### ✅ Verified correct (keep)
- **The floor** (Decision 2, `internal/quota/floor.go`):
  - It reads `f_bavail × f_bsize` at most once a second.
  - The bytes admitted since a reading count against it.
  - An `Unknown` size is admitted only while the room is not negative.
  - A failed reading admits writes, logged once.
  - The refusal names no number (`TestScenarioFloorRefusesWorkloadWrites` asserts it). The free space and the floor go
    to the log once when a reading falls below and once when one is back above.
- **One decision point** (Decision 3): `p.checker` is `Floor.Guard(p.quotas)`, and every place that passed `p.quotas`
  as a Checker now passes it: the KV and SQL facades, and each `s3BucketFor` (S3 gateway, `ctx.blob`, Sites, static,
  watcher, admission). `Quoted` asks the zero `Subject` for an uncapped Bucket, so each write meets the floor once.
- **The catalog store** (Decision 4): the `Catalogs` view is a `Quoted` view with no capped subject. A Lake's catalog
  save gets 507 below the floor (`TestScenarioFloorRefusesWorkloadWrites` in `pkg/funcd`).
- **funclog** (Decision 5): a refused record is dropped and counted, and the count is logged at the first record
  admitted again.
- **Off** (Decision 1): `"0"` sets no floor (`TestScenarioFloorOff`), and `cmd/funcd` adds none in `storage.mode:
  memory`. `StorageMinFreeBytes` defaults to 2GiB.
- **Docs**: `examples/funcdconfig.yaml` documents `minFreeSize`. The "A full data disk" section of `docs/install.md`
  says what 507 below the floor means and that writes resume once space is freed.
- **Tracking**: the ADR is at `Reviewing` and its substance is unchanged since acceptance. F212 is `reviewing`, and the
  blueprint names the floor.

### Definition of done
7 of 7. That is the three Review checklist items, plus the done-when line's per-change checks: the tests with `-race`,
build, vet and lint. The Lima lanes passed in the integration session, and `scripts/agent/gate.sh` runs once on the
branch.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0296 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 7/7. See
docs/reviews/model-scorecard.md.

### Recommendation
ADR-0296 meets its bar. Stamp it `Implemented` with F212 in the batch's stamp commit. Both Minors are small and can
ride along with ADR-0294's fix.
