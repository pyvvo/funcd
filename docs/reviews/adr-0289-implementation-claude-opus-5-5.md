## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (ADR-0289 implementation, model: claude-opus-5-5)

Branch `feat/adr-0289-catalog-save-retry`: docs 5303d734 (Accepted), implementation 7de3ba91 (ADR → Reviewing,
F204 → reviewing), on 1cb84fe2. Reviewed in a separate detached worktree at 7de3ba91; every claim below comes from a
command that ran there.

### 🟡 Major
- **`TestScenarioIdleLakeRecovers.test_a_refused_call_save_is_made_again_with_no_call` is flaky** · attribution:
  model · evidence: it polls `bucket.bodies == [b"6"]` (`images/runtime/duckdb/shim_test.py:1190`), which
  `put_object` fills during the upload. The three assertions after the poll read state that the retry thread writes
  only later: `_saved` writes the report after the upload (`shim.py:385-396`), and `_checkpoint` then sets
  `error = None` and `covered = covers` (`shim.py:349-351`). Through `go test -count=1 -run TestIssue998
  ./internal/runtime/embedimg/` (which runs the whole stub suite, in `just ci`), the test failed in 3 of 6 runs with
  `AssertionError: 5 != 6` at `:1191`. Run alone, it failed 1 run in 30. · direction: poll the last write instead,
  `_eventually(lambda: checkpointer.saved.covered == 6)` (`covered` is set after `error` and the report), then assert
  the bodies, the error and the report.

### Minor
- **An exception that `_checkpoint` does not log is now dropped silently** · attribution: model · evidence:
  `_SaveRetry._attempt` swallows every exception (`shim.py:582-587`) because `_checkpoint` logs its failures. But the
  snapshot read before `_checkpoint`'s `try` (`shim.py:341`), and anything else raised outside it, reaches `_attempt`
  unlogged. `_StartUpSave` wrote a stderr line for every exception. · direction: in `_attempt`, write a line when the
  exception is not the one `_checkpoint` recorded (`exc is not checkpointer.saved.error`). That keeps the checklist's
  "the retry thread adds none" for save failures.

### ✅ Verified correct (keep it)
- **Decisions 1 and 2.** `_Checkpointer.retry` takes the save lock, checks again that the last save failed and that
  the stop did not begin, and runs ADR-0279's `_checkpoint`: the snapshot id is read before the copy and recorded only
  on success, and the report keeps the first failure's time (`_saved`). `_SaveRetry.run` makes the recovered catalog's
  start-up save, then retries every `SAVE_RETRY_SECONDS` until `stop`. A fresh catalog is never saved by it.
- **Decision 3.** `_stop` stops and joins the watcher, here `_SaveRetry`, after the drain and before
  `checkpointer.stop` (the last save and the detach). A retry that waited for the lock checks `stop` under it. The
  no-stop-check mutant (`and not stop.is_set()` removed) fails `test_a_retry_that_waited_for_the_lock_saves_nothing`.
- **The interval mode is unchanged**: `_CommitWatcher` is untouched, and `main` builds `_SaveRetry` only with
  `FUNCD_LAKE_SAVE_PORT`.
- **Tests.** `shim_test.py`, run directly: 53/53 OK. One test per scenario, with real timing and count assertions:
  attempts one period apart, the first failure's time kept, no upload after a good save, none after the join. The
  engine test `test_scenario_idle_lake_recovers` carries `oracle: docs/design/lake/commit-save.fizz#SavesRecover` and
  ends with a SIGKILL and a read-back. It was not rerun here (the builder ran `just test-duckdb-engine` 20/20).
- **Design model.** `specs.py check docs/design/lake/commit-save.fizz`: pass, 7,733 states; all seven mutants fail
  as named, `no-retry` on liveness.
- **The lane.** `issue1233-failing-saves-show` now reads `CatalogSaved` `True Saved` before any query, then reads the
  row back. The guard tests `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` pass. The lane itself was not
  run (the batch gate runs it).
- **Tracking.** The only edit to the ADR is the status line (`Reviewing 2026-10-25`), and F204 is `reviewing`. The old
  names survive only in the ADR's own text.

### Definition of Done
4 / 5 checklist items hold. The fifth ("the scenario tests … pass") does not hold reliably while the Major stands.

### Recommendation
Fix the Major's poll (one line) and, if you agree, the Minor. Then rerun `go test -count=5 -run TestIssue998
./internal/runtime/embedimg/` and send me the commit for a short round 2.
