# ADR-0273: A Lake stop cancels the queries that outlive its wait (amends ADR-0271)

- **Status**: Proposed (2026-10-24; judged twice, redesigned on round 1's Blocker)
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
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) Decision 8 (the engine's confinement)

## Context & Need

ADR-0271's stop ends Quack, copies the catalog, detaches it and waits up to `STOP_WAIT_SECONDS` for its file to close;
on timeout it uploads the copy taken at `quack_stop`. A commit acknowledged during that wait is then lost (#1217): with
a 12 s query in flight and a 2 s commit acknowledged 1.7 s after SIGTERM, the restarted engine lacked the commit in 3
runs of 3. Experiment 0007's model of the stop found the gap before the reproduction did. The stop needs a way to end
a query that outlives the wait, so that the closed file, and its copy, hold every acknowledged commit.

Probed on the engine's image (DuckDB 2.0): `FORCE CHECKPOINT` aborts nothing, and a running DuckLake insert still
commits 6 s after it. Quack can end a query: `quack_active_connections()` lists the connections it serves, with a state
(`active` while a query runs), and `quack_cancel` interrupts one; its client gets "INTERRUPT Error: Interrupted: query
was cancelled". `quack_cancel` goes through a Quack client attachment, which the engine's own DuckDB cannot make once
confined (ADR-0263 Decision 8). Probed under the shim's own confinement, the attach fails ("File system HTTPFileSystem
has been disabled by configuration") and so does a switch of the authorization function ("the configuration has been
locked"), while `quack_active_connections()` and `DETACH` work; a separate DuckDB instance of the shim's process makes
the attachment instead. A transaction holds the catalog only while its request runs: Quack ends it with the request
(a `BEGIN` and an insert in one request leave nothing open, and a later `COMMIT` with the same client id finds no
transaction), and a client that attaches the engine as a remote catalog cannot write the Lake at all, so every holder
is a query Quack lists as active.

## Scenarios

- `scenario: timed-out-stop-keeps-acknowledged-commits` (#1217): Given a query that outlives `STOP_WAIT_SECONDS` and a
  commit acknowledged during the wait, When the engine stops, Then the next engine holds the commit, the long query's
  client gets an error, and the stop ends within the runtime's grace.

- `scenario: no-transaction-after-detach`: Given a stopping engine, When a client sends a write during the stop,
  Then it fails and the next engine does not hold it.

ADR-0271's `stop-keeps-acknowledged-commits` holds unchanged.

## Scope

Unchanged (ADR-0271).

## Constraints & Decision drivers

Unchanged (ADR-0271), plus: the stop ends within the runtime's 10 s SIGTERM grace.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Detach, wait for the file to close, cancel what still holds it, copy the closed file | the closed file holds every commit, and none can follow | a query running at the deadline fails; its client must retry | chosen |
| Abort with `FORCE CHECKPOINT` (experiment 0007's candidate) | one statement | aborts nothing in DuckDB 2.0 (probed) | rejected |
| Refuse new queries and wait until none runs, without detaching | no detach | a query accepted before the refusal binds the catalog after it (the spec's `no-detach` mutant); the locked configuration refuses the switch | rejected |
| Cancel from the engine's own DuckDB | no second instance | confined: external access is off | rejected |
| Wait longer | no failed query | a query can outlive any wait; the runtime kills the engine at 10 s | rejected |
| Shut the clients' sockets from Python | no Quack dependency | reaches into Quack's sockets; Linux-only; Quack already cancels | rejected |

## Decision

3. **Stop.** Join the watcher and close its cursor; `DETACH lakehouse`, after which no new transaction can use the
   catalog; wait up to `STOP_WAIT_SECONDS` (5 s) for DuckDB to close its file, which it then attaches read-only. If the
   file is still open, list the connections `quack_active_connections()` shows `active` and cancel each with
   `quack_cancel`, through a Quack client attachment that a separate DuckDB instance of the shim's process makes with
   the served port and token; then keep waiting. Copy the closed file, upload it, then `quack_stop`. A failed cancel is
   logged and the stop keeps waiting. ADR-0263 Decision 8 governs the serving instance; the control instance is its
   one exception: opened only when the wait timed out, it attaches only the engine's own port on loopback (which
   ADR-0269's fence accepts), runs only `quack_cancel`, serves nothing, and is closed before the copy. If the runtime
   kills the engine during the stop (a commit that stalls, or uploads that overrun the grace), the stored catalog is
   the last checkpoint before SIGTERM: the commits acknowledged after it are lost, those of at most one poll interval
   before SIGTERM (#998) and any during the stop.

## Design model

- **Spec**: [`docs/design/lake/stop.fizz`](../design/lake/stop.fizz) · state machine [`stop.md`](../design/lake/stop.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedCommitsAreStored` | always | `timed-out-stop-keeps-acknowledged-commits`, `stop-keeps-acknowledged-commits` | `TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit` (engine_test.py) |
  | `StopEnds` | always eventually | the stop ends | the same test's bound on the stop's duration |

  The `no-detach` mutant protects `no-transaction-after-detach`, whose oracle is
  `TestIssue1217NoTransactionAfterDetach`.

- **Bounds**: 2 commits, one of them #1217's long query; 3 concurrent actions (two commits and the stop; `Checkpoint`
  and `StopBegin` exclude each other through the lock); 8 actions, the smallest at which every mutant fails.
- **Faults**: a graceful stop; a query accepted before the stop binds the catalog after it; a cancel that arrives after
  a commit started does not stop it. Crash on yield is off: a SIGKILL's loss is #998's trade-off.
- **Mutants**: `upload-pre-copy` (ADR-0271's timeout), `no-detach` and `copy-before-close` fail
  `AckedCommitsAreStored`; `no-cancel` fails liveness: the stop never ends.
- **Result**: pass, 3,688 states, 1.0 s, FizzBee v0.5.4; wider (3 commits, 4 concurrent actions, 16 actions): pass,
  229,201 states, 52 s.
- **Left to tests**: that `quack_cancel` interrupts a DuckLake write (the scenario test); that a transaction ends with
  its request (`test_a_transaction_ends_with_its_request`); a commit that stalls (the runtime's kill, not tested).

## Temporary workarounds

None.

## Contracts

```python
STOP_WAIT_SECONDS = 5.0
class _Checkpointer:
    def stop(self, serving: list[str], token: str) -> None: ...   # Decision 3; the served URIs and Quack token
def _closed(cur) -> bool: ...                                     # the detached file attaches read-only
def _cancel(cur, serving: list[str], token: str) -> None: ...     # cancels the active Quack connections
```

| Item | ADR-0271 | Now |
|---|---|---|
| A query running past `STOP_WAIT_SECONDS` | may commit; the stop uploads the copy taken at `quack_stop` | cancelled; its client gets an interrupt error |
| A query sent during the stop | cannot connect (Quack stopped first) | fails: the catalog is detached; Quack stops after the upload |
| `_await_close`, the copy at `quack_stop` | used | removed (`_closed` waits) |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: Decision 3 in `_Checkpointer.stop`, `_closed`, `_cancel`; `main` reads the token
   before installing the SIGTERM handler and passes it.
2. Tests: `engine_test.py` `TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit` (#1217's reproduction, with the
   oracle marker) and `TestIssue1217NoTransactionAfterDetach` (a write during the stop; a transaction ends with its
   request); `shim_test.py` `TestIssue1217AStopUploadsTheClosedCatalog` (the statement order, the cancel from the
   separate instance, a failed cancel, another attach error) and the shutdown order; ADR-0271's engine tests unchanged.
3. At acceptance, ADR-0271 gains `- **Superseded in part by**: [ADR-0273](…) (<date>) — Decision 3, the stop contract,
   Review checklist item 3`, and ADR-0263 `… — Decision 8 governs the serving instance, beside a stop's control
   instance`.
4. Done when `just test-duckdb-engine`, the duckdb lane, `just check-specs` and `just ci-full` pass; the PR closes #1217.

## Review checklist

- [ ] The stop detaches, waits for the file to close, cancels only after `STOP_WAIT_SECONDS`, copies the closed file,
  uploads it, then stops Quack; no copy is taken before the file closes.
- [ ] The #1217 test fails on the shim of v0.26.0 for the lost commit and passes with this one; its oracle marker names
  `AckedCommitsAreStored`.
- [ ] `docs/design/lake/stop.fizz` passes, and its four mutants fail.
- [ ] The control instance opens only after the wait timed out, attaches only the engine's own port on loopback, runs
  only `quack_cancel`, serves nothing, and closes before the copy.

## Consequences

- **(+)** A stop that ends keeps every acknowledged commit, also when a query outlives the wait. On the engine's image,
  3 runs of #1217's reproduction kept the commit; the stop ended in 5.23 to 5.30 s, the long query cancelled at 5.13 to
  5.19 s, which leaves about 4.7 s of the grace for the uploads (milliseconds on one node, ADR-0271's review).
- **(−)** A kill during the stop loses the commits acknowledged since the last checkpoint before SIGTERM, not only
  #998's one interval: the stop joins the watcher first. Only a stalled commit or an upload that overruns the grace
  leads there.
- **(−)** A query running 5 s after SIGTERM is cancelled; its client gets an error and retries against the next
  engine. ADR-0270 replaces each Lake engine once per key period, so this can happen daily for a long query.
- **(−)** A query sent during the stop fails on the detached catalog instead of failing to connect.
- **(−)** The stop depends on Quack's cancel, a function of DuckDB 2.0's young Quack.

## Open questions

None.

## References

- #1217 (a Lake stop loses a commit acknowledged during its wait when it times out) · #1198 · #998
- [Quack reference](https://duckdb.org/docs/2.0/quack/reference.html): `quack_active_connections`, `quack_cancel`,
  `quack_stop`
- Experiment 0007 (`pyvvo/experiment`, private): the design model that found #1217
