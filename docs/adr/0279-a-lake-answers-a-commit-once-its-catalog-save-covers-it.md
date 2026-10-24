# ADR-0279: A Lake answers a commit once its catalog save covers it

- **Status**: Proposed (2026-10-24)
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, durability, disaster-recovery
- **Realizes**: [FEAT-0003/F192](../feat/0003-feat-data-platform.md) (a Lake commit that returns is saved)
- **Answers**: #1255 (the decider's choice, 2026-10-24, on the reliability track's card 2: save before answering each
  commit by default, the interval save an opt-in)
- **Supersedes (in part)**: [ADR-0274](0274-a-lake-saves-its-catalog-at-the-interval-its-recovery-objective-sets.md)
  Decision 1's "without it the interval is 1 s" and Decisions 3 and 5, which now hold only with `lake.saveInterval`
  set · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)'s bounded checkpoint window, as amended by
  [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md), which now holds only with that key
- **Relates to**: [ADR-0137](0137-per-caller-catalog-query-rbac.md) (the Lake PEP proxy) ·
  [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (the stop) ·
  [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) (`CatalogSaved`) ·
  [ADR-0278](0278-a-file-blob-put-returns-once-its-object-is-on-disk.md) (a save's upload is on disk) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A Lake engine (ADR-0086) answers a commit once DuckLake has it in the engine's local catalog file. A watcher in the
engine's shim saves that file whole to the blob store once per `lake.saveInterval` (ADR-0274). A crash of the engine,
the daemon or the host loses the commits acknowledged since the last save, and every commit since saves began failing
when the engine dies while they fail (#1255; experiments 0009 and 0010). A client that saw a commit acknowledged
expects it in the Lake after any crash.

## Scenarios

- `scenario: acknowledged-commit-survives-crash` — Given a Lake on a platform without `lake.saveInterval`, When its
  engine is killed, the daemon is killed or the host loses power right after a commit is acknowledged, Then the commit
  is in the Lake once its engine is back.
- `scenario: waiting-commits-share-a-save` — Given several clients committing at once, When their answers wait, Then
  one save answers every commit made before it began, and no commit waits past the save after the one in progress.
- `scenario: refused-save-is-an-error` — Given S3 refusing the catalog saves, When a client commits, Then it gets an
  error, never an acknowledgement, and the Lake reports `CatalogSaved` False (ADR-0276).
- `scenario: read-waits-for-nothing-saved` — Given every commit saved, When a client reads, Then its answer goes out
  after one check and no save.
- `scenario: interval-is-the-opt-in` — Given `lake.saveInterval` set, When a client commits, Then the answer does not
  wait for a save, and the engine saves at that interval (ADR-0274).

## Scope

**In**: the save before each answer: the shim's save endpoint, the barrier in the Lake PEP proxy, the switch by
`lake.saveInterval`, and what a client gets when a save fails. **Out**: `funcdctl dev`'s engine, whose catalog is a
local file and is never uploaded (`internal/lake/devengine/devengine.go:274`); the blob store's own durability
(ADR-0278); an interval Lake while saves fail, unchanged (it answers, and `CatalogSaved` shows the failure); the stop
(ADR-0273), unchanged.

## Constraints & Decision drivers

- The proxy parses only the handshake of the Quack body (ADR-0137), so it cannot tell a commit from a read: every
  answer waits for a save that covers the catalog's latest commit, which is one check when nothing is unsaved.
- A save copies the whole catalog database and uploads it whole (ADR-0271): several MiB even for a small Lake, more as
  it grows. A save per commit adds that save to the commit's answer, so commits that wait together must share one.
- Only the shim holds the catalog and makes its saves; the daemon reaches the engine's network namespace as the proxy
  does, and lateral drop keeps workers out of it.
- A large query result must not be held in the daemon's memory.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. The proxy holds each answer until a save covers the catalog's latest commit | no acknowledged commit lost; one save for commits that wait together | every commit waits for a save; reads wait behind unsaved commits | chosen by the decider |
| B. The interval save (ADR-0274) | fast commits | a crash loses up to an interval and two saves | kept as the opt-in |
| C. A commit hook in the engine | no daemon change | DuckDB 2.0 has no hook that delays a commit's answer; Quack answers inside the engine | rejected |
| D. The catalog in a replicated database | no whole-file copy per commit | DuckLake opens PostgreSQL, MySQL, SQLite or DuckDB catalogs; PostgreSQL is ruled out and rqlite is none of them | rejected |
| E. The proxy buffers each whole answer, then saves (the prototype of experiment 0010) | simplest | daemon memory grows with a query's result | rejected for the held last bytes |
| F. Reads pass while saves fail | reads keep working | the proxy cannot tell reads from commits | rejected |

## Decision

1. **The switch.** Without `lake.saveInterval` (the default), a Lake saves before it answers. With the key set, its
   engine saves at that interval and answers at once, as ADR-0274 decided: setting the key, by hand or with
   `funcdctl backup plan lake --rpo`, is the opt-in. The reconciler sets `FUNCD_LAKE_SAVE_INTERVAL` as today when the
   key is set, and `FUNCD_LAKE_SAVE_PORT` (8081, NEW) when it is not; an engine with neither saves every second, as
   ADR-0274 decided for an engine started by hand. A change takes effect at the restart that loads it (ADR-0274).
2. **The save endpoint.** With `FUNCD_LAKE_SAVE_PORT`, the shim runs no interval watcher. It serves `POST /save` on
   that port of the engine's network namespace:
   - it reads the catalog's latest snapshot id; when the last good save covers it, it answers 200 at once;
   - otherwise it takes the save lock, which ADR-0273's stop also takes. When a save that ended while it waited covers
     the id, it answers 200; else it reads the latest id again, copies the catalog and uploads the copy (ADR-0271),
     records that id as saved, and answers 200. Every commit made before the copy began is saved with it;
   - a failed save answers 503 and the engine's report records it (ADR-0276); the next call tries again.
3. **The barrier.** For a Lake whose engine serves `/save`, the PEP proxy forwards each engine answer but its last
   bytes, and sends them once a `POST /save` to that engine answers 200; an answer with no body waits with its
   headers. When the call fails, the proxy answers 503 "lake catalog not saved" if the headers have not gone out, and
   otherwise breaks the connection, so the client never completes the answer. The handshake waits as any answer does.
4. **A failed save.** A commit answered with an error is in the engine's local catalog: the next good save keeps it,
   unless the engine dies first. Its outcome is unknown to the client, who retries an idempotent write or reads back.
   While saves fail, every answer that follows an unsaved commit is an error, reads included.
5. **What it bounds.** Without `lake.saveInterval`, a crash of the engine, the daemon or the host loses no
   acknowledged commit; ADR-0274's recovery point applies only with the key set.

## Design model

- **Spec**: [`docs/design/lake/commit-save.fizz`](../design/lake/commit-save.fizz) · state machine
  [`commit-save.md`](../design/lake/commit-save.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedCommitsAreSaved` | always | `acknowledged-commit-survives-crash`, `refused-save-is-an-error` | `TestScenarioAcknowledgedCommitSurvivesCrash` |
  | `NoAnswerStaysHeld` | always eventually | `waiting-commits-share-a-save` | `test_scenario_waiting_commits_share_a_save` |
- **Bounds**: two commits, two save slots behind one lock, one refused upload, one crash; 3 concurrent actions, 15
  actions: the whole state space (16 and 20 reach the same states; below 15 a run ends before its save does).
- **Faults**: one engine crash at any step, restarting from the uploaded catalog; one refused upload; crash on yield
  off.
- **Environment**: an upload that returns is durable (ADR-0278); a copy holds every commit made before it; every
  answer passes the proxy; a crash breaks the answers it held.
- **Mutants**: `answer-first` (the interval mode), `id-after-copy`, `no-save-lock` and `refused-marks-saved` fail
  `AckedCommitsAreSaved`; `no-save-call` fails liveness.
- **Result**: pass, 7,097 states, 3.5 s, FizzBee v0.5.4; wider (20 actions): pass, the same 7,097 states.
- **Left to tests**: that a Quack client completes no answer before its last bytes (`TestBarrierHoldsTheLastBytes`
  with a real engine on the lane); the daemon crash and the power loss (the duckdb lane, experiment 0010's failure
  phase); the stop with `/save` calls waiting (`test_stop_releases_waiting_saves`).

## Temporary workarounds

None.

## Contracts

```text
POST /save on the engine's FUNCD_LAKE_SAVE_PORT (8081), reachable from the daemon only (lateral drop)
  200 {"saved": <snapshot id>}   a good save covers the catalog's latest snapshot when the call came
  503 {"error": "<why>"}         the save failed
```

```go
// internal/lake/gateway
type EngineTarget struct {
	Lake        auth.EntityRef
	Upstream    string
	EngineToken string
	SaveURL     string // NEW: the engine's save endpoint; "" when lake.saveInterval is set (no barrier)
}

func (m *Manager) Ensure(lake auth.EntityRef, upstream, engineToken, saveURL string) (string, error) // saveURL NEW
```

| Consumes | Exposes |
|---|---|
| `lake.saveInterval` (ADR-0274); the engine's network namespace; `_Checkpointer`'s lock and report (ADR-0273, ADR-0276) | `FUNCD_LAKE_SAVE_PORT`, `POST /save`; the proxy's 503 "lake catalog not saved" |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: `FUNCD_LAKE_SAVE_PORT`, the save endpoint on a `ThreadingHTTPServer` thread, the
   saved id beside `_Checkpointer`'s lock; no watcher in that mode.
2. `internal/services/lake`: `FUNCD_LAKE_SAVE_PORT` without `lake.saveInterval`; the engine's save URL to `Ensure`.
   `internal/lake/gateway`: `SaveURL`, the barrier (a body reader that holds the last bytes, and the headers of an
   empty answer), the 503 problem.
3. Tests:
   - `shim_test.py` and `engine_test.py` (a real engine): `test_scenario_waiting_commits_share_a_save`, a covered call
     answers without a save, a refused save answers 503 and the next call saves, `test_stop_releases_waiting_saves`;
   - `internal/lake/gateway`: `TestBarrierHoldsTheLastBytes` (no byte of the last chunk before `/save` answers; an
     empty answer's headers wait), `TestScenarioRefusedSaveIsAnError` (503, or a broken connection after the
     headers), no barrier without `SaveURL`;
   - `internal/services/lake`: the engine keys in each mode, `TestScenarioIntervalIsTheOptIn`;
   - the duckdb lane: `TestScenarioAcknowledgedCommitSurvivesCrash` as a Venom case, which SIGKILLs the engine right
     after an acknowledged commit and reads the row back.
4. Done when `just ci-full`, `just test-linux`, `just check-specs` and the duckdb lane pass, and experiment 0010's
   failure phase on the branch loses no acknowledged commit.

## Review checklist

- [ ] Without `lake.saveInterval`, no engine answer completes before a save covers the catalog's latest snapshot; the
  proxy holds at most one chunk of an answer.
- [ ] `/save` reads the snapshot id before the copy, saves under the stop's lock, and never records a failed save.
- [ ] With the key set, nothing changes from ADR-0274.
- [ ] The scenario tests carry their oracle markers and pass.

## Consequences

- **(+)** No acknowledged commit is lost to a crash of the engine, the daemon or the host (with ADR-0278).
- **(−)** Every commit waits for a whole-catalog copy and upload, which grows with the catalog, and a Lake commits
  fewer times a second; a read waits behind the unsaved commits before it (experiment 0010).
- **(−)** While saves fail, the Lake answers errors to every request that follows an unsaved commit.
- **(−)** `lake.saveInterval` now chooses the mode too: `funcdctl backup plan lake --rpo` writes it, so it opts into
  interval saves.

## Open questions

- A mode per Lake rather than per platform: a later ADR, if Lakes with different needs share a platform.
- Whether `funcdctl backup plan lake --rpo` should stop writing the key, since the default loses nothing: the decider.
- An error that means "not committed" (hold until a save works, or restart the engine from its last save): a later
  ADR, if clients need it.
- Saves that upload only what changed: a later ADR, if a workload needs cheaper commits.

## References

#1255 · #1256 · #1243 · experiments 0009 and 0010 (pyvvo/experiment#19, #20) · `images/runtime/duckdb/shim.py` ·
`internal/lake/gateway/proxy.go` · `internal/lake/gateway/manager.go:111` ·
`internal/services/lake/reconcile.go:389-406` · ADR-0086 · ADR-0137 · ADR-0271 · ADR-0272 · ADR-0273 · ADR-0274 ·
ADR-0276 · ADR-0278
