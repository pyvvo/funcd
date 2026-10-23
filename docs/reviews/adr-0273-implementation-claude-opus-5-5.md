## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0273 implementation, model: claude-opus-5-5)

This report reviews the branch `fix/1217-lake-stop-cancel` against ADR-0273's Contracts, Scenarios, Review checklist
and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review agents), by
running every check below. The ADR was judged five rounds before acceptance; the shim was built beside it on the
engine's image, and its design model, `docs/design/lake/stop.fizz`, drove two of the redesigns.

### Verified correct (keep)
- **Decision 3, the order of the stop:** `_on_term` drains for up to `STOP_WAIT_SECONDS` while the watcher saves
  (`images/runtime/duckdb/shim.py:395`), joins the watcher and closes its cursor (396-399), saves once more, logging a
  failed save (400-403), then calls `_Checkpointer.stop`: `DETACH lakehouse` (242), a cancel only while the file is
  still open (243-246), a copy of the closed file (248), its upload (251), then `quack_stop` (252-253). No copy is
  taken before the file closes: `_closed` attaches the file read-only and waits out only the open-file conflict (281-290).
- **The control instance:** `_cancel` opens it only when a query still holds the file after `DETACH` and Quack lists an
  active connection (297-300); it attaches only `127.0.0.1` at the engine's served port with the engine's token
  (305-308), runs only `quack_cancel` (310), serves nothing, and closes in `finally` (314), before the copy. A failed
  cancel is logged and the stop keeps waiting (311-312, 245-246).
- **Scenarios:** `timed-out-stop-keeps-acknowledged-commits` and `drain-keeps-the-recovery-point` are
  `TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit`, with the oracle markers of `AckedCommitsAreStored`,
  `StopEnds` and `KillLosesAtMostOneInterval`; `writes-during-the-drain-are-kept` is `TestIssue1217TheDrain`, which
  also shows that a transaction ends with its request. ADR-0271's `stop-keeps-acknowledged-commits` test runs unchanged.
- **Revert checks, on the final tests:** with the shim of v0.26.0 (the shim on main), the #1217 test fails with
  `Lists differ: [] != [(42,)]` and the drain test fails; with the shim of 7f3fa9f7 (the cancel without the drain
  while the watcher saves), the #1217 test passes and the drain test fails ("no checkpoint before the stop's own
  upload held the commit acknowledged during the drain").
- **The design model:** `stop.fizz` passes at 10,453 states; its six mutants each fail the assertion they name
  (`upload-pre-copy`, `no-detach` and `copy-before-close` fail `AckedCommitsAreStored`; `join-at-sigterm` and
  `no-final-save` fail `KillLosesAtMostOneInterval`; `no-cancel` fails liveness); `stop.md` is current.
- **Tracking:** ADR-0271 and ADR-0263 carry their `Superseded in part by` back-links; F186 moved with the ADR. The
  ADR's substance is unchanged since its acceptance: only its status line moved.

### Checks
- `scripts/agent/gate.sh` at 5fe0e62e: PASS (audit, `ci-full` 674 s, linux, linux-tests, tree-unchanged; stress
  skipped, the branch changes no Go test).
- `just test-duckdb-engine`: 10 tests, OK, 64.7 s.
- `shim_test.py`: 21 tests, OK.
- `just lima-example duckdb`: PASS, 19 of 19 cases, among them #998's killed engine and the engine restarts.
- `just check-specs`: 2 specs, 0 failing.
