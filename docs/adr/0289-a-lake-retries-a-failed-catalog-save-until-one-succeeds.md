# ADR-0289: A Lake retries a failed catalog save until one succeeds

- **Status**: Reviewing 2026-10-25; Accepted (2026-10-25) under the decider's delegation; judged once, six Minors folded
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, durability, disaster-recovery
- **Realizes**: [FEAT-0003/F204](../feat/0003-feat-data-platform.md) (a Lake whose saves failed saves again on its own)
- **Answers**: the decider's choice 5A (2026-10-25, the DR batch's refine brief): a Lake retries a failed catalog save
  in the background until it succeeds; decision 3 on the board card "Stateful components: data loss and downtime under
  each failure (DR and platform reliability track)"
- **Relates to**: [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md) (the save endpoint, the
  save lock; nothing it decided changes) · [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) (the report,
  `CatalogSaved`, the start-up save's retry) · [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md)
  (the stop) · [ADR-0274](0274-a-lake-saves-its-catalog-at-the-interval-its-recovery-objective-sets.md) (the interval
  mode) · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

Without `lake.saveInterval`, a Lake engine saves its catalog when the Lake PEP proxy asks, before each answer
(ADR-0279). A failed save answers 503, records nothing and replaces the engine's report; the shim retries only the
start-up save of a recovered catalog, every `START_UP_RETRY_SECONDS` (`images/runtime/duckdb/shim.py` `_StartUpSave`,
ADR-0276 Decision 2). After a failed call's save, nothing saves again until the next request: an idle Lake keeps its
last commits only in its engine's local catalog, and its `CatalogSaved` stays `False` after S3 accepts again. The duckdb
lane's `issue1233-failing-saves-show` case needs a query after it gives the prefix back. An operator sees a failure
that is over, and a crash in that time loses the commits answered with an error, which the next good save would keep
(ADR-0279 Decision 4).

## Scenarios

- `scenario: idle-lake-recovers` — Given a Lake without `lake.saveInterval` whose save failed after a commit, and no
  request since, When S3 accepts saves again, Then within one retry period and one save the blob store's catalog holds
  that commit, and one supervision period later `CatalogSaved` is `True` with reason `Saved`.
- `scenario: retries-while-refused` — Given S3 refusing the saves and no request, Then the engine tries again once per
  retry period, its report keeps the first failure's time, and `CatalogSaved` stays `False` with reason `SavesFailing`.
- `scenario: no-retry-after-a-good-save` — Given the last save succeeded, When no request comes, Then the engine
  uploads nothing.
- `scenario: stop-ends-retries` — Given S3 refusing the saves, When the engine stops, Then it makes no retry once the
  stop's last save began, and the stop runs as ADR-0273 decided.

## Scope

**In**: the shim's retry of a failed save in the save-before-answering mode, its pace, its place beside the save calls
and the stop. **Out**: the interval mode, whose watcher already retries a failed save at each tick (`_CommitWatcher._tick`);
a commit whose client left before the proxy's save call, where no save failed (it waits for the next save, as ADR-0279
decided); `funcdctl dev`'s engine, whose catalog is never uploaded; the daemon and the Lake reconciler, unchanged.

## Constraints & Decision drivers

- A save copies the whole catalog and uploads it whole (ADR-0271): each retry costs one copy and one upload attempt.
- Only the shim saves, one save at a time under the lock the save calls and the stop take (ADR-0273, ADR-0279).
- The daemon is pure Go and learns of saves only from the report (ADR-0276): the engine must save again by itself.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. The shim retries every failed save at a fixed second until one succeeds | the start-up save's pace and code; `CatalogSaved` and the save follow S3's return within a second | one copy and one refused upload a second during an outage | chosen by the decider (5A) |
| B. Keep ADR-0279: the next request saves | no change | an idle Lake keeps its commits unsaved and `CatalogSaved` `False` after S3 recovers | rejected |
| C. A retry with exponential backoff to a cap | fewer copies in a long outage | the save and the condition lag S3's return by up to the cap; the interval mode and the start-up save retry at a fixed pace | rejected; a later ADR if the copy's cost matters |
| D. A watcher that saves any unsaved commit each second, beside the barrier | also saves a commit whose client left | an extra save a second under steady commits, racing the calls' saves; more than the decision | rejected |
| E. The Lake reconciler calls `/save` while `CatalogSaved` is `False` | no shim change | saves follow S3's return only at the supervision pace, and a second `/save` caller beside the proxy, whose instance tracking does not see it; the shim already retries | rejected |

## Decision

1. **Retry every failed save.** With `FUNCD_LAKE_SAVE_PORT`, one thread of the shim retries a failed save every
   `SAVE_RETRY_SECONDS` (1 s, the renamed `START_UP_RETRY_SECONDS`) until a save succeeds or the stop joins it,
   whichever save failed: the start-up save of a recovered catalog (ADR-0276 Decision 2, as today), a save call's, or its own
   retry. The thread runs from the start until the stop, for a fresh catalog too; while the last save succeeded it
   saves nothing, so it is not ADR-0274's interval watcher.
2. **A retry is a save.** A retry takes the save lock, checks again that the last save failed (a call's save may have
   succeeded meanwhile), and runs ADR-0279 Decision 2's save: the latest snapshot id read before the copy, recorded only
   once the upload succeeded, the report replaced with the outcome, the first failure's time kept while saves fail
   (ADR-0276 Decision 2). A save call that waits behind a retry answers as it does behind any save.
3. **The stop.** The stop stops and joins the retry thread where it joins the start-up save today, after its drain and
   before its last save and the detach (ADR-0273); a retry under way finishes first, as any save under the lock does,
   and a retry that was waiting for the lock when the stop began checks the stop again under it and saves nothing.
4. **What the Lake shows.** A good retry reports `saved`; the Lake's `CatalogSaved` turns `True` at the reconciler's next
   pass (ADR-0276 Decision 3), with no request.

## Design model

- **Spec**: [`docs/design/lake/commit-save.fizz`](../design/lake/commit-save.fizz) (ADR-0279's, updated) · state machine
  [`commit-save.md`](../design/lake/commit-save.md) (unchanged: the retry moves no answer)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `SavesRecover` (NEW) | always eventually | `idle-lake-recovers` | `test_scenario_idle_lake_recovers` |
  | `AckedCommitsAreSaved` | always | ADR-0279's, with a retry saving beside the calls | ADR-0279's |
  | `NoAnswerStaysHeld` | always eventually | ADR-0279's | ADR-0279's |
- **Bounds**: ADR-0279's: two commits, two save slots behind one lock, one refused upload, one crash; 15 actions, the
  smallest at which the spec passes (14 cuts a held answer), and 16 reaches the same states.
- **Faults**: ADR-0279's; the refused upload now also leaves `failing` set, which only a good save clears.
- **Mutants**: NEW `no-retry` (nothing saves after a failed save until an answer waits) fails liveness; ADR-0279's six
  still fail.
- **Result**: pass, 7,733 states, 5.1 s, FizzBee v0.5.4; wider (a second crash, 21 actions): pass, 19,397 states, 12.3 s.
- **Left to tests**: the retry against a real engine and S3 (`test_scenario_idle_lake_recovers`); the stop's join of
  the retry (`TestScenarioStopEndsRetries`): [`stop.fizz`](../design/lake/stop.fizz) is reused unchanged, as its
  watcher may save at any step until the stop joins it, and the retry's saves, made only while saves fail, are among
  those runs; `no-retry-after-a-good-save` (`TestScenarioNoRetryAfterAGoodSave`), a cost property no assertion
  protects.

## Temporary workarounds

None.

## Contracts

```python
# images/runtime/duckdb/shim.py
SAVE_RETRY_SECONDS = 1.0  # RENAMED from START_UP_RETRY_SECONDS: the pace of every failed save's retry

class _Checkpointer:
    def retry(self, stop: threading.Event) -> None: ...  # NEW: under the save lock, saves when the last save
                                                         # failed and stop is not set; else returns

class _SaveRetry(threading.Thread):  # RENAMED from _StartUpSave: the start-up save, then each failed save's retry
    def __init__(self, checkpointer: _Checkpointer, recovered: bool) -> None: ...
    def stop(self) -> None: ...
```

| Consumes | Exposes |
|---|---|
| `_Checkpointer`'s lock, its `_Saved` outcome and the report (ADR-0276, ADR-0279) | the report's `saved` line after a good retry, so `CatalogSaved` turns `True`; no new key, port or field |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: the renamed constant; `_Checkpointer.retry`; `_SaveRetry` in `_StartUpSave`'s place
   in `main` and `_stop`; the docstrings that name the start-up save.
2. Tests:
   - `shim_test.py` (stub S3, in `just ci`): `TestScenarioIdleLakeRecovers` (a call's save refused, then S3 accepts:
     the retry uploads with no call and the report reads `saved`), `TestScenarioRetriesWhileRefused` (attempts one
     period apart, the first failure's time kept), `TestScenarioNoRetryAfterAGoodSave`, `TestScenarioStopEndsRetries`
     (no attempt after `stop`, none by a retry that waited for the lock), the start-up save tests on `_SaveRetry`;
   - `engine_test.py` (`just test-duckdb-engine`): `test_scenario_idle_lake_recovers`, with the oracle marker
     `oracle: docs/design/lake/commit-save.fizz#SavesRecover` (the `refuse` file on, a commit's `/save` answers 503,
     the file off, no call: within 3 s the uploaded catalog holds the commit and the report reads `saved`; then SIGKILL,
     restart and read back);
   - the duckdb lane's `issue1233-failing-saves-show`: after the prefix is given back, `CatalogSaved` reads `True Saved`
     before any query, then the row reads back.
3. Done when `just ci` (the stub tests, `just check-specs`), `just test-duckdb-engine` and the duckdb lane pass.

## Review checklist

- [ ] With the save endpoint, a failed save of any kind is retried every `SAVE_RETRY_SECONDS` until one succeeds or the
  stop joins the retry; nothing is uploaded while the last save succeeded.
- [ ] A retry saves under the save lock, reads the snapshot id before its copy, records it only on success, and keeps
  the report's first failure time.
- [ ] The stop joins the retry before its last save, and a retry that waited for the lock saves nothing once the stop
  began; the interval mode is unchanged.
- [ ] Each failed attempt logs one line, `_checkpoint`'s `catalog checkpoint failed`; the retry thread adds none.
- [ ] `SavesRecover`'s oracle test carries its marker; the scenario tests and `just check-specs` pass.

## Consequences

- **(+)** An idle Lake whose saves failed saves again within a second of S3 accepting: `CatalogSaved` returns to
  `True`, and the commits answered with an error are kept without a request.
- **(−)** While S3 refuses, the engine copies its catalog and tries an upload once a second, and logs one line per failed
  attempt, `catalog checkpoint failed` (ADR-0276 Decision 4), as the interval mode does each tick. A refusal that never
  ends, such as a prefix the Lake does not own (the lane's issue1233 case), costs that copy and line each second for as
  long as it lasts.
- **(−)** A call that comes during a retry that fails answers 503 without an attempt of its own (ADR-0279 Decision 2).

## Open questions

- A backoff for long outages, if a large catalog's copy each second costs too much: a later ADR.
- Saving a commit whose client left before its save call: a later ADR, if a workload needs it.

## References

ADR-0273 · ADR-0274 · ADR-0276 · ADR-0279 · ADR-0272 · #1243 · #1256 · experiments 0009 and 0011 ·
`images/runtime/duckdb/shim.py` (`_StartUpSave`, `_Checkpointer.save_covering`, `_CommitWatcher._tick`, `_stop`) ·
`e2e/duckdb.venom.yml` (`issue1233-failing-saves-show`) · #1280 (F204)
