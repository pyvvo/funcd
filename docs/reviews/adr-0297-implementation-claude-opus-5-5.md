## Verdict: pass — 0 blockers, 0 majors, 4 minors  (ADR-0297 implementation, model: claude-opus-5-5)

This review covers ADR-0297's part of b7e6798e on `fix/limits-followup`: `cmd/funcd/space.go`, its wiring in
`cmd/funcd/main.go`, `internal/platform/diskfull` and the Badger adapters. The checks ran in a worktree detached at
a7cccf39. `scripts/agent/gate.sh` runs once on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`, on darwin and with `GOOS=linux`: ok.
- `go test -race -count=1` on `cmd/funcd`, `internal/platform/diskfull` and the five Badger adapter packages
  (`internal/store/badger`, `internal/kvstore/badger`, `internal/workflow/runstate/badger`,
  `internal/eventing/eventstore`, `internal/eventing/deadletter/badger`): ok.
- `go vet` and `golangci-lint run` on the touched packages, on darwin and with `GOOS=linux`: ok, 0 issues.
- Two mutants, with `go test -overlay`. Each one fails a test:
  - The waiter retrying on another errno instead of `ENOSPC` fails `TestScenarioStartWaitsForSpace`.
  - The start skipping `RemoveLeftoverScratch` fails `TestScenarioStartFreesLeftoverScratch`.
- `go.mod` takes `github.com/dgraph-io/badger/v4` v4.9.6 and `github.com/dgraph-io/ristretto/v2` v2.2.0 from their
  releases, with no `replace`.

### Minor
- **`start-below-floor` has no named test** (`model`). The ADR's plan names `…StartBelowFloor`: `pkg/funcd` with a
  `FreeFunc` below the floor, where reads succeed and an upload gets 507. No test carries the scenario. Its substance is
  covered elsewhere:
  - `TestScenarioPlatformWritesContinue` starts a platform below the floor, applies a resource and gets 507 on a write.
  - The kv-counter lane restarts funcd with a floor above its free space, and a put gets 507.
  - Neither checks a read, and neither is tagged with the scenario.
- **The 507 test covers the classifier, not each adapter** (`model`). The plan asks for "each adapter's write path with
  an injected `ENOSPC`". `TestScenarioEnospcIs507` tests `diskfull.Wrapf` on six error shapes. Nothing checks that each
  adapter routes its write errors through it. Two background batch writes skip it:
  - The seen list's `wb.Set` (`internal/eventing/eventstore/eventstore.go:378`), which can commit a batch, still
    flattens its error to text.
  - The dead-letter sweep's eviction (`internal/eventing/deadletter/badger/badger.go:242-247`) keeps the chain but
    stays `Internal`.
  Neither answers a client, so no 507 is lost, and neither matters until upstream releases the change. Use
  `diskfull.Wrapf` in both, as at `eventstore.go:382`.
- **`need` is a constant, not taken from the adapters' sizes** (`model`). `storesOpenBytes` (`cmd/funcd/space.go:25`)
  hard-codes four databases of 16 MiB memtables and 64 MiB value logs, and the comment names the packages. The plan
  says "`need` from the adapters' sizes". If an adapter's options change, or a fifth database is added, the constant
  drifts with no test failing. Export the sizes, or add a test that compares the constant with each adapter's options.
- **Decision 2 understates the retry** (`adr`, recorded, not scored). It says that until the upstream release "its retry
  catches funcd's own writes only". `y.Wrap` does flatten Badger's errors to text (`y/error.go:55-62` in v4.9.6). But a
  new database's manifest is written by `helpRewrite`, which returns the `os` errors as they are
  (`manifest.go:242-280`), and `Open` returns those unchanged (`db.go:241-243`). Now that the adapters keep the chain,
  that step's `ENOSPC` is retried too. The behavior is better than the text says, and harmless. A later ADR that adopts
  the upstream release can correct the sentence.

### ✅ Verified correct (keep)
- **The order** (Decision 2):
  - `startSpace` removes the leftover scratch, on the containerd runtime and outside memory mode, before the first wait.
  - The safe-mode record's write is the first waited step, so a stop during that wait returns before `start.Failed`
    is deferred.
  - `buildOptions` with `funcd.New` is the second step. A failed `New` joins `ce()`, so a retry finds no store locked.
  - A stop during the second wait returns `start.Clean()` through `serve`'s named result, so no failed start is
    recorded.
- **The waiter**:
  - It waits only on free space below `need` and on `ENOSPC` (`TestSpaceWaiterStops`: another error returns at once).
  - A failed read does not wait.
  - It logs the free space and `need` every `spaceWait`.
  - Stop signals end the wait through `sleepUntilStopped`.
- **`need`'s value** is right today: the four production databases (`store/badger.go:72`, `kvstore/badger.go:120`,
  `runstate/badger.go:57`, `eventstore.go:53`) use 16 MiB memtables and 64 MiB value logs, so the need is 640 MiB.
- **The chain** (Decision 3): every adapter's `Open` now uses `fault.Wrapf(err, …)` instead of `%v`. Every write path
  a client reaches maps `ENOSPC` to `fault.InsufficientStorage`: the metastore's `Update`, the KV commit and
  `DropPrefix`, and the run state's `Put` and `Delete`. The KV facade and the store pass those errors up unchanged, so a
  client gets 507 once the chain reaches them.
- **Docs**: the "A full data disk" section of `docs/install.md` says what funcd removes by itself, what it waits for and
  logs, and how to end an earlier run's engine that still holds its scratch. The text describes behavior only.
- **Tracking**: the ADR is at `Reviewing` and its substance is unchanged since acceptance. F213 is `reviewing`. The
  blueprint names the start's wait. The upstream issue is tracked outside the repo.

### Definition of done
6 of 7. That is the three Review checklist items, plus the done-when line's per-change checks: the tests with `-race`,
build, vet and lint. The third checklist item ("each scenario has its named test", and every adapter mapping `ENOSPC`)
is short by the first two Minors. The Lima lanes passed in the integration session, and `scripts/agent/gate.sh` runs
once on the branch.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0297 (implementation) → pass, 0/0/4, 3 model-attributed, DoD 6/7. See
docs/reviews/model-scorecard.md.

### Recommendation
ADR-0297's code does what its Decisions say. Stamp it `Implemented` with F213 in the batch's stamp commit. The three
model Minors are small: a named test, two `diskfull.Wrapf` calls and a test for the constant. They can ride along with
ADR-0294's fix.
