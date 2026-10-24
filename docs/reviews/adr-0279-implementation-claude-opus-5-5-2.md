## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0279 implementation, round 2, model: claude-opus-5-5)

This second round reviews f3e90260 on `feat/adr-0279-lake-save-before-answer`. That commit holds the round-1 work at
5aca61c8, plus 56bc59bf (a lane case) and f52a5cae (the fixes for the round-1 findings,
[report](adr-0279-implementation-claude-opus-5-5.md)). The checks below ran in a worktree detached at f3e90260. The
gate, the Lima lanes and experiment 0010's failure phase are the builder's runs and did not run here.

### The round-1 findings
- **Blocker 1 (`funcdctl dev` Lakes got 503): fixed.** `pkg/funcd` sets `ReconcilerDeps.LocalCatalog` when the Lake
  runtime is overridden, as `funcdctl dev` does (`pkg/funcd/funcd.go:1169`). `saves()`
  (`internal/services/lake/reconcile.go:394`) then gives the engine no save port and the proxy no save URL. The
  round-1 overlay test, which assembles the platform as `funcdctl dev` does with an engine on the IPv6 loopback, now
  gets 200 without a save interval. With `LocalCatalog` forced false it gets 503 again, and the committed
  `TestLakeWithALocalCatalogAnswersAtOnce` also fails with 503.
- **Minor 1 (the release order): partly fixed.** See Minor 1 below.
- **Minor 2 (three barrier rules): fixed.** Each mutant now fails a test. An empty answer sent through `heldTail` fails
  `TestScenarioRefusedSaveIsAnError`, which now requires a whole 503. A retried save call fails that test's exact call
  count. A save call made on `context.Background()` fails `TestBarrierSaveCallEndsWithTheRequest`.
- **Minor 3 (the wiring): fixed.** `TestScenarioIntervalIsTheOptIn` reads the save URL that `Reconcile` gave the
  Manager (`Manager.SaveURL`, `internal/lake/gateway/manager.go:238`), in three modes: interval, save before
  answering, and local catalog. A `Reconcile` that passes `""` fails it.
- **Minor 4 (a real engine for the stop and the refused saves): partly fixed.** The refused-save test is real and
  fails two mutants (see Verified). See Minor 2 below for the stop test.
- **Minor 5 (`saveURL` failed open): fixed.** An address that `saveURL` cannot read is `fault.Internal`, and
  `Reconcile` fails the pass. A mutant that fails open fails `TestSaveURLFailsClosed`.
- **Minor 6 (stale comments): fixed** in `config.go` and `shim.py`.

### Minor
- **The release-order test still misses sometimes** (`model`): with the 200 KiB answer,
  `TestBarrierHoldsTheLastBytes` (`internal/lake/gateway/barrier_test.go:107`) kills a mutant that returns the last
  bytes with nil and makes the save call on the next read in 15 of 20 runs. In the other 5 runs, the proxy server's
  write buffer still holds the tail when the save call starts. The refusal tests do not kill that mutant, because their
  answers are small and the handler's abort discards a buffered tail. A unit test of `heldTail` over a reader that
  returns its last bytes and `io.EOF` on separate reads would kill it in every run. It would also kill a `heldTail`
  that holds no byte, which every current test passes. The real barrier passed 20 of 20 runs.
- **The real-engine stop test does not see a call answered by the stop's upload** (`model`):
  `test_stop_answers_waiting_saves` (`images/runtime/duckdb/engine_test.py:609`) passes with a stop that answers no
  waiting call. A mutant without the wait for the calls at the stop passes it, and so does a mutant whose stop does
  not record its snapshot id. In three runs of the real shim and three of each mutant, all 5 acknowledged commits came
  after SIGTERM, from saves made during the drain, and the counts were the same. The test shows that a stop under
  concurrent commits loses no acknowledged commit and exits 0. The stop's 200s rest on the stub test of the same name
  in `shim_test.py`, which fails both mutants.
- **`WithLakeProviderRuntime` now also turns off the save barrier** (`model`): `LocalCatalog` follows the runtime
  override (`funcd.go:1169`), but the option's doc (`pkg/funcd/options.go:544-547`) does not say so. A caller that
  overrides the runtime with engines that serve `/save` would get no barrier, with no warning.

### Routed (not this work's)
- **An idle Lake keeps a failed save until its next request** · home: elsewhere · destination: the decider (ADR-0279
  Decisions 2 and 4, ADR-0276). Without `lake.saveInterval` nothing retries a failed save on its own. Once S3 accepts
  saves again, an idle Lake still reports `CatalogSaved` False, and the commit answered with an error stays unsaved
  until the next request saves it. 56bc59bf had to add a query to the lane's `issue1233` case for this reason. The ADR
  does not say it, and the code follows the ADR.
- `docs/PROJECT-SUMMARY.md` still says that ADR-0274's default is 1 s · home: elsewhere · destination: the next
  `/project-summary` run.

### ✅ Verified correct (keep)
- **The round-1 verification still holds.** `barrier.go` and `proxy.go` are unchanged since 5aca61c8, and `shim.py`
  changed only in comments. Decisions 1 to 3, the Contracts and the five Scenarios hold as the round-1 report records.
- **The local-catalog switch:** `saves()` is false with `lake.saveInterval` set or with a local catalog. In both cases
  `engineEnv` sets no `FUNCD_LAKE_SAVE_PORT` and `Reconcile` passes no save URL. Only `cmd/funcdctl/dev.go` overrides the
  Lake runtime outside tests, so production Lakes keep the barrier.
- **The lane case (56bc59bf):** once the prefix is given back, a query's save keeps the commit that was answered with
  an error, as Decision 4 says. The case reads `[[1233]]` again before it checks that `CatalogSaved` is True.
- **The new real-engine refused-save test** fails a mutant that records a refused save as saved and a mutant whose
  `/save` never saves. All three concurrent commits fail while S3 refuses, and the next read's save stores them.
- **Mutants killed in this round:** the round-1 Go mutants that were killed are still killed (a release that ignores
  the instance, a request sent before the instance is learned, a save status that is ignored), and so are the six
  new ones: the empty answer through `heldTail`, the retried call, the save call on `context.Background()`, the
  `""` save URL from `Reconcile`, the fail-open `saveURL`, and `LocalCatalog` forced false.
- **Tracking:** the ADR's substance is unchanged since its acceptance; this gate stamps it `Implemented` and F192
  `implemented`.

### Checks
- `go build ./...` on the host and with `GOOS=linux`: exit 0. `go vet` on the touched packages, `pkg/funcd` included,
  for both systems: exit 0.
- `golangci-lint run` on `internal/lake/...`, `internal/services/lake/...`, `internal/platform/config/...`,
  `pkg/funcd/...` and `cmd/funcd/...`: 0 issues.
- `go test -race -count=1` on `./internal/lake/... ./internal/services/lake/... ./internal/platform/config/...
  ./internal/runtime/embedimg/... ./cmd/funcdctl/`: ok. `pkg/funcd` with `-race`, the Lake tests on a fake engine
  (`TestLakeWithALocalCatalogAnswersAtOnce`, `TestLakeWatchesFollowLakeAndBinders`, `TestIssue769_*`): ok.
- `just check-specs`: 8 specs, 0 failing; `commit-save.fizz` passes at 7,277 states, and each of its six mutants fails
  what it names.
- `shim_test.py`: 40 tests, OK.
- `engine_test.py` in `funcd/runtime-duckdb`, the five ADR-0279 classes: 7 tests, OK, 26.0 s.

### Definition of Done
8 of 8 items hold: the five Review-checklist items, the tree, the tracking, and no change outside the ADR's scope.
The ADR's "Done when" runs (`just ci-full`, `just test-linux`, the duckdb lane and experiment 0010's failure phase)
are the builder's. The builder reports that the duckdb lane's `issue1233` case passes with 56bc59bf.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0279 (implementation, round 2) → pass, 0/0/3, 3 model-attributed, DoD 8/8. See
`docs/reviews/model-scorecard.md`.

### Recommendation
Pass. ADR-0279 is stamped `Implemented` and F192 `implemented`. The three Minors are a `heldTail` unit test, a real
stop test with a call waiting, and one line of option doc; they can follow in this PR or later. The PR that implements
the ADR closes #1255.
