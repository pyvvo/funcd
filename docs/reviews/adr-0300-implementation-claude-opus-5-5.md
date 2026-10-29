## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0300 implementation, model: claude-opus-5-5)

Branch `refactor/eventstore-rename` at accc22ab, `refactor(eventing)!: the event store's directory and config key are
named eventstore (ADR-0300)`, on the accept commit 8f6112c9. Reviewed in a detached worktree at accc22ab. Every claim
below comes from a command that ran there.

### Minor
- **The default-directory test proves the derived path, not the start.** Scenario `eventstore-default-dir` says a
  started daemon opens `<storage.dataDir>/eventstore` and no `deadletter` directory exists.
  `TestScenarioEventStoreDefaultDir` (`internal/platform/config/eventstore_test.go`) asserts the value `Load` derives.
  The daemon passes that value unchanged (`cmd/funcd/main.go:560-563` → `WithDeadLetterQueue`), so the behavior
  holds, but no test starts a daemon and lists its data directory. Attribution: `model`. Not blocking.

### ✅ Verified correct (keep it)
- **Every site follows the plan.**
  - Config: `EventStore.DataDir` with `FUNCD_EVENTING_EVENTSTORE_DATA_DIR`, defaulting to `<Storage.DataDir>/eventstore`
    (`config.go:660-661`). `Deadletter` keeps `Retention` and `MaxEntries` only.
  - Readers: `cmd/funcd/main.go`, `internal/restore/run.go:113,324`, `internal/upgrade/upgrade.go:398,442` and
    `internal/safemode/safemode.go:76` read and name `eventing.eventstore.dataDir`.
  - `bench/badger` opens `eventstore`, and builds as its own module.
- **Nothing is left behind.** A search of the code, `examples/` and `docs/install.md` for the old key, its
  environment variable and a `deadletter` directory finds only the upgrade note's `mv` and two tests that name the old
  name on purpose, as Done-when allows.
- **The dead-letter tenant keeps its names**: the package `internal/eventing/deadletter`, `WithDeadLetterQueue`'s
  signature, and `eventing.deadletter.retention` and `maxEntries`. Only the comments that name the default directory
  changed.
- **Every scenario has a named test, un-skipped and passing.**
  - Config: `TestScenarioEventStoreDefaultDir`, `TestScenarioEventStoreKey` (both the file and the environment
    variable), `TestScenarioOldEventStoreKeyRefused` (``unknown field "dataDir"``) and `TestScenarioDeadLetterKeysKept`.
  - Messages, each asserting the key and the path: `TestScenarioRestoreNamesEventStoreKey`,
    `TestScenarioUpgradeProbeNamesEventStoreKey` and `TestScenarioSafeModeNamesEventStoreKey`.
- **`docs/install.md` gives Decision 5's upgrade order** (stop, `funcd upgrade --unit ""`, move, start) and states
  what a start without the move costs. The runbook's two passages and the example config name `eventstore`.
- **Tracking.** ADR-0300 is at `Reviewing`, and its only edit in accc22ab is the status line. F217 is at `reviewing`,
  and ADR-0201 has its "Superseded in part by" back-link from the accept commit.

### Checks run
- `go build ./...`: ok. `bench/badger`, as its own module, builds.
- `go vet` on the touched packages, host and `GOOS=linux`: ok.
- `golangci-lint` on the touched packages: 0 issues.
- `go test -race`, all ok:
  - `internal/platform/config`, `internal/safemode`, `internal/restore`, `internal/upgrade`, `internal/eventing/deadletter/...`;
  - `cmd/funcd -run 'Restore|Upgrade|Rollback|EventStore'`;
  - `pkg/funcd -run 'EventStore|Backup|SeenList'`.
- Not run here: the gate (`just ci-full`, e2e) and the Linux tests, which run once on the PR.

### Definition of Done
8 / 8: the 7 Review-checklist items and the Done-when.

### Model scorecard
claude-opus-5-5 on ADR-0300 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 8/8.

### Recommendation
Pass. The scenario test gap can be closed in a later change, or left as it is.
