# ADR-0273: A Lake stop cancels the queries that outlive its wait (amends ADR-0271)

- **Status**: Reviewing (2026-10-24); Accepted 2026-10-24, the acceptance delegated by the decider; Proposed
  2026-10-24, judged five rounds with no Blocker or Major left, redesigned in between to keep the Lake's recovery
  point, on the decider's call
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, quack, durability, stop, amendment
- **Realizes**: [FEAT-0003/F186](../feat/0003-feat-data-platform.md) (a Lake stop that times out keeps every
  acknowledged commit)
- **Supersedes (in part)**: [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md) — Decision 3 (the stop), the
  `stop` contract, and the third Review checklist item (the fallback to the copy taken at `quack_stop`) ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) Decision 8 — it now governs the serving instance, beside the
  bounded control instance a stop may open
- **Relates to**: [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the amendment form
  and the design model) · [ADR-0270](0270-derived-s3-key-pairs-rotate.md) (a Lake engine replaced once per key period) ·
  [FEAT-0009/F112](../feat/0009-feat-disaster-recovery.md) (recovery objectives)

## Context & Need

A Lake engine saves its DuckLake catalog to S3 after each commit, at most once per `COMMIT_POLL_SECONDS` (1 s, #998):
a crash loses at most the commits of one save interval, the Lake's recovery point today. ADR-0271's stop stops that
watcher at SIGTERM, ends Quack, detaches the catalog and waits up to `STOP_WAIT_SECONDS` for its file to close; on
timeout it uploads the copy taken at `quack_stop`. A commit acknowledged during that wait is then lost (#1217): with a
12 s query in flight and a 2 s commit acknowledged 1.7 s after SIGTERM, the restarted engine lacked the commit in 3
runs of 3. Experiment 0007's model of the stop found the gap first. A kill during that wait also loses every commit
since SIGTERM, more than the recovery point.

Probed on the engine's image (DuckDB 2.0): `FORCE CHECKPOINT` aborts nothing, and a running DuckLake insert still
commits 6 s after it. Quack can end a query: `quack_active_connections()` lists the connections it serves, with a state
(`active` while a query runs), and `quack_cancel` interrupts one; its client gets "INTERRUPT Error: Interrupted: query
was cancelled". `quack_cancel` goes through a Quack client attachment, which the engine's own DuckDB cannot make once
confined (ADR-0263 Decision 8): under the shim's own confinement the attach fails ("File system HTTPFileSystem has been
disabled by configuration") and so does a switch of the authorization function ("the configuration has been locked"),
while `quack_active_connections()` and `DETACH` work; a separate DuckDB instance of the shim's process makes the
attachment instead. A transaction holds the catalog only while its request runs: Quack ends it with the request (a
`BEGIN` and an insert in one request leave nothing open, and a later `COMMIT` with the same client id finds no
transaction), and a client that attaches the engine as a remote catalog cannot write the Lake at all, so every holder
is a connection Quack lists as active.

## Scenarios

- `scenario: timed-out-stop-keeps-acknowledged-commits` (#1217): Given a query that outlives `STOP_WAIT_SECONDS` and a
  commit acknowledged during the wait, When the engine stops, Then the next engine holds the commit, the long query's
  client gets an error, and the stop ends within the runtime's grace.
- `scenario: drain-keeps-the-recovery-point`: Given a stop that waits for a long query, When a commit is acknowledged
  during the wait, Then a catalog saved before the stop's own upload holds it, so a kill during the drain loses no more
  than a crash.
- `scenario: writes-during-the-drain-are-kept`: Given a stopping engine whose queries drain, When a client writes, Then
  the write is acknowledged and the next engine holds it.

ADR-0271's `stop-keeps-acknowledged-commits` holds unchanged.

## Scope

Unchanged (ADR-0271).

## Constraints & Decision drivers

Unchanged (ADR-0271), plus: the stop ends within the runtime's 10 s SIGTERM grace, and a kill during its drain loses
no more than a crash does (the decider, 2026-10-24).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Drain while the watcher saves, then detach, cancel what still holds the file, copy the closed file | every acknowledged commit in the copy; a kill during the drain loses no more than a crash | under steady traffic the stop takes the full 5 s, and the next engine waits | chosen |
| Stop saving at SIGTERM, detach at once (ADR-0271, and this ADR's first drafts) | the stop ends as soon as the queries in flight end | a kill during the stop loses every commit since SIGTERM | rejected by the decider |
| Abort with `FORCE CHECKPOINT` (experiment 0007's candidate) | one statement | aborts nothing in DuckDB 2.0 (probed) | rejected |
| Refuse new queries and wait until none runs, without detaching | no detach | a query accepted before the refusal binds the catalog after it (the spec's `no-detach` mutant); the locked configuration refuses the switch | rejected |
| Cancel from the engine's own DuckDB | no second instance | confined: external access is off | rejected |
| Shut the clients' sockets from Python | no Quack dependency | reaches into Quack's sockets; Linux-only; Quack already cancels | rejected |

## Decision

3. **Stop.** At SIGTERM the engine keeps serving, and the commit watcher keeps saving the catalog, while the running
   queries drain: up to `STOP_WAIT_SECONDS` (5 s), until `quack_active_connections()` shows no `active` connection.
   Then the stop joins the watcher and closes its cursor, saves the catalog once more (a failed save is logged; the
   stop's own upload covers the same commits), and runs `DETACH lakehouse`, after which no new transaction can use the
   catalog. If the file is still open, it cancels each `active` connection with `quack_cancel`, through a
   Quack client attachment that a separate DuckDB instance of the shim's process makes with the served port and token,
   and waits for the file to close. It copies the closed file, uploads it, then runs `quack_stop`. A failed cancel is
   logged and the stop keeps waiting. ADR-0263 Decision 8 governs the serving instance; the control instance is its one
   exception: opened only when a query still holds the file after `DETACH`, it attaches only the engine's own port on
   loopback (which ADR-0269's fence accepts), runs only `quack_cancel`, serves nothing, and is closed before the copy.
   A kill during the drain loses at most what a crash loses, the commits of one save interval; a kill later in the
   stop loses only the commits that complete after its last save, those of queries running after the drain ended.

## Design model

- **Spec**: [`docs/design/lake/stop.fizz`](../design/lake/stop.fizz) · state machine [`stop.md`](../design/lake/stop.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedCommitsAreStored` | always | `timed-out-stop-keeps-acknowledged-commits`, `writes-during-the-drain-are-kept`, `stop-keeps-acknowledged-commits` | `test_a_commit_acknowledged_during_the_wait_survives_and_the_long_query_is_cancelled` |
  | `KillLosesAtMostOneInterval` | always | `drain-keeps-the-recovery-point` and the save at the join | `test_the_watcher_keeps_saving_while_the_queries_drain` |
  | `StopEnds` | always eventually | the stop ends | the #1217 test's bound on the stop's duration |

- **Bounds**: 2 commits, one of them #1217's long query; 3 concurrent actions (the two commits and the watcher's
  checkpoint race through yield points; every stop action is atomic); 9 actions, the smallest at which the spec passes
  and every mutant fails (8 cuts the run before the stop can end).
- **Faults**: a graceful stop; a kill at any point of the stop; a query accepted before `DETACH` binds the catalog after
  it; a cancel that arrives after a commit started does not stop it. Time is one save interval per `Tick`, in every
  phase: while the watcher runs it saves within an interval; after the join, time moves freely (a stalled commit, a
  failed cancel). Crash on yield is off.
- **Mutants**: `upload-pre-copy` (ADR-0271's timeout), `no-detach` and `copy-before-close` fail
  `AckedCommitsAreStored`; `join-at-sigterm` (the watcher stops at SIGTERM) and `no-final-save` (no save at the join)
  fail `KillLosesAtMostOneInterval`; `no-cancel` fails liveness: the stop never ends.
- **Result**: pass, 10,453 states, 2.7 s, FizzBee v0.5.4; wider (3 commits, 3 concurrent actions, 10 actions): pass,
  402,463 states, 103 s and 34 GB of memory (4 concurrent actions ran out of memory).
- **Left to tests**: that `quack_cancel` interrupts a DuckLake write and that a transaction ends with its request (the
  engine tests); a commit that stalls (the runtime's kill, not tested).

## Temporary workarounds

None.

## Contracts

```python
STOP_WAIT_SECONDS = 5.0
def _drain(cur, seconds: float) -> None: ...                      # waits for no active Quack connection, at most seconds
class _Checkpointer:
    def stop(self, serving: list[str], token: str) -> None: ...   # after the drain and the watcher's join: Decision 3
def _closed(cur) -> bool: ...                                     # the detached file attaches read-only
def _cancel(cur, serving: list[str], token: str) -> None: ...     # cancels the active Quack connections
```

| Item | ADR-0271 | Now |
|---|---|---|
| The watcher at SIGTERM | stops | saves until the drain ends |
| A query running past `STOP_WAIT_SECONDS` | may commit; the stop uploads the copy taken at `quack_stop` | cancelled; its client gets an interrupt error |
| A write during the stop | cannot connect (Quack stopped first) | served and kept while the queries drain; after `DETACH` a new query fails to bind (the spec's `no-detach` mutant) |
| `_await_close`, the copy at `quack_stop` | used | removed (`_closed` waits) |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: Decision 3 in `_drain`, `_Checkpointer.stop`, `_closed` and `_cancel`; `main`
   reads the token before installing the SIGTERM handler, which drains, stops the watcher, saves once more, then
   calls the stop.
2. Tests: `engine_test.py` `TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit` (#1217's reproduction; the watcher
   saving during the drain; both with oracle markers) and `TestIssue1217TheDrain` (a write during the drain is kept; a
   transaction ends with its request); `shim_test.py` `TestIssue1217AStopUploadsTheClosedCatalog` (the statement
   order, the cancel from the separate instance, a failed cancel, another attach error),
   `TestADrainLetsTheRunningQueriesEnd` and the shutdown order; ADR-0271's engine tests unchanged.
3. At acceptance, ADR-0271 gains `- **Superseded in part by**: [ADR-0273](…) (<date>) — Decision 3, the stop contract,
   Review checklist item 3`, and ADR-0263 `… — Decision 8 governs the serving instance, beside a stop's control
   instance`.
4. Done when `just test-duckdb-engine`, the duckdb lane, `just check-specs` and `just ci-full` pass; the PR closes #1217.

## Review checklist

- [ ] The stop drains while the watcher saves, then joins the watcher, saves once more, detaches, cancels only what
  still holds the file, copies the closed file, uploads it, then stops Quack; no copy is taken before the file closes.
- [ ] The #1217 test fails on the shim of v0.26.0 for the lost commit, and the drain test fails on a stop that joins
  the watcher at SIGTERM; both pass with this shim and carry their oracle markers.
- [ ] `docs/design/lake/stop.fizz` passes, and its six mutants fail.
- [ ] The control instance opens only when a query still holds the file after `DETACH`, attaches only the engine's own
  port on loopback, runs only `quack_cancel`, serves nothing, and closes before the copy.

## Consequences

- **(+)** A stop that ends keeps every acknowledged commit, also when a query outlives the wait. A kill during the
  drain loses no more than a crash; later in the stop, only the commits that complete after the last save. On the
  engine's image, 3 runs of #1217's reproduction kept the commit, saved by the watcher during the drain; the stop ended
  in 5.25 to 5.38 s and the long query was cancelled at 5.23 to 5.28 s, which leaves about 4.6 s of the grace.
- **(−)** Under steady traffic the stop takes the full 5 s, and the Lake's next engine starts only after it.
- **(−)** A query still running when the drain ends, also one started during it, is cancelled; its client gets an
  error and retries against the next engine. ADR-0270 replaces each Lake engine once per key period.
- **(−)** The stop depends on Quack's cancel, a function of DuckDB 2.0's young Quack.

## Open questions

- The save interval as a Lake setting derived from a recovery objective (`funcdctl backup plan --rpo`, FEAT-0009 F112):
  a follow-up ADR, decided 2026-10-24. It also answers two questions this stop leaves to the objective: whether the
  commits that complete after the stop's last save count against the recovery point when the stop is killed, and
  which loss the recovery point bounds while saves fail (the watcher retries a failed save on its next interval).

## References

- #1217 (a Lake stop loses a commit acknowledged during its wait when it times out) · #1198 · #998
- [Quack reference](https://duckdb.org/docs/2.0/quack/reference.html): `quack_active_connections`, `quack_cancel`,
  `quack_stop`
- Experiment 0007 (`pyvvo/experiment`, private): the design model that found #1217
