# ADR-0279: A Lake answers a commit once its catalog save covers it

- **Status**: Reviewing (2026-10-24; accepted that day under the decider's delegation; judged twice)
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, durability, disaster-recovery
- **Realizes**: [FEAT-0003/F192](../feat/0003-feat-data-platform.md) (a Lake commit that returns is saved)
- **Answers**: #1255 (the decider's choice, 2026-10-24, on the reliability track's card 2: save before answering each
  commit by default, the interval save an opt-in); for a Lake without the key, ADR-0276's open question on refusing
  commits while saves fail
- **Supersedes (in part)**: [ADR-0274](0274-a-lake-saves-its-catalog-at-the-interval-its-recovery-objective-sets.md)
  Decision 1's "without it the interval is 1 s" and its config contract (an empty key is 1 s), and Decisions 3 and 5,
  which now hold only with `lake.saveInterval` set · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)'s
  bounded checkpoint window, as amended by [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md), now only with the key
- **Relates to**: [ADR-0137](0137-per-caller-catalog-query-rbac.md) (the Lake PEP proxy) ·
  [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (the stop) ·
  [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) (`CatalogSaved`, the start-up save) ·
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
`lake.saveInterval` through the config chain, and what a client gets when a save fails. **Out**: `funcdctl dev`'s
engine, whose catalog is a local file and is never uploaded (`internal/lake/devengine/devengine.go:270-282`); the blob
store's own durability (ADR-0278); an interval Lake while saves fail, unchanged; the stop's order (ADR-0273).

## Constraints & Decision drivers

- The proxy parses only the handshake of the Quack body (ADR-0137), so it cannot tell a commit from a read: every
  answer waits for a save that covers the catalog's latest commit, which is one check when nothing is unsaved.
- A save copies the whole catalog database and uploads it whole (ADR-0271): several MiB even for a small Lake, more as
  it grows. Commits that wait together must share one save.
- An engine can restart on the same address (`internal/lake/gateway/proxy.go:139-140`, #634), and a restarted engine
  hands out again the snapshot ids of the commits it lost: a save report must come from the instance that answered.
- Only the shim holds the catalog and makes its saves; the daemon reaches the engine's network namespace as the proxy
  does, and lateral drop keeps workers out of it. A large query result must not be held in the daemon's memory.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. The proxy holds each answer until a save covers the catalog's latest commit | no acknowledged commit lost; one save for commits that wait together | every request waits; reads wait behind unsaved commits | chosen by the decider |
| B. The interval save (ADR-0274) | fast commits | a crash loses up to an interval and two saves | kept as the opt-in |
| C. A commit hook in the engine | no daemon change | DuckDB 2.0 has no hook that delays a commit's answer; Quack answers inside the engine | rejected |
| D. The catalog in a database server | no whole-file copy per commit | DuckLake's server catalogs are PostgreSQL and MySQL; funcd runs neither, PostgreSQL is ruled out, and rqlite is not one | rejected |
| E. The proxy buffers each whole answer, then saves (experiment 0010's prototype) | simplest | Quack sends batches of up to 32 MiB per message: daemon memory grows with a result | rejected for the held last bytes |
| F. Reads pass while saves fail | reads keep working | the proxy cannot tell reads from commits, and a read could show a commit a crash takes back | rejected |
| G. A mode key of its own beside `lake.saveInterval` | names the mode | one more key, while the interval key and `plan lake --rpo` already say "accept a loss" | rejected |
| H. The save endpoint as a socket in the engine's boot dir | no port | the boot dir is worker-writable, so the daemon would connect where a worker points it | rejected |

## Decision

1. **The switch.** Without `lake.saveInterval` (the default), a Lake saves before it answers. `Config.LakeSaveInterval`
   reports an empty key as 0, `cmd/funcd` passes it on, and `WithLakeSaveInterval(0)` means "save before answering".
   With the key set, the engine saves at that interval and answers at once, as ADR-0274 decided: setting it, by hand or
   with `funcdctl backup plan lake --rpo`, is the opt-in. The reconciler sets `FUNCD_LAKE_SAVE_INTERVAL` when the key is
   set and `FUNCD_LAKE_SAVE_PORT` (8081, NEW) when it is not; an engine with neither saves every second, as ADR-0274
   decided for an engine started by hand. A change takes effect at the restart that loads it (ADR-0274).
2. **The save endpoint.** With `FUNCD_LAKE_SAVE_PORT`, the shim runs no interval watcher; it draws an instance id at
   start and serves `POST /save` on that port of the engine's network namespace, listening before `quack_serve`:
   - the saved id starts at the catalog's latest snapshot id at start, fresh or recovered. A recovered catalog is saved
     once at start, and that save is retried every second until one succeeds (ADR-0276 Decision 2);
   - a call reads the latest snapshot id on its own cursor; when the last good save covers it, it answers 200 at once;
   - otherwise it takes the save lock, which ADR-0273's stop also takes. When a save that ended while it waited covers
     the id, it answers 200; when that save failed, 503. Else it reads the latest id again, copies the catalog and
     uploads the copy (ADR-0271), records that id as saved, and answers 200; a failed save answers 503, records nothing
     and updates the engine's report (ADR-0276). The S3 client's own timeouts bound a save (`_s3_client`, botocore's);
   - at a stop, the stop records the id of its last upload: the waiting calls it covers answer 200 before
     `quack_stop`, the others 503.
3. **The barrier.** For a Lake whose engine serves `/save`, the PEP proxy learns the engine's instance id with a `/save`
   call before it forwards its first request, which gets 503 when that call fails. It forwards each engine answer but
   its last bytes; once the answer's body ends, it makes one `/save` call to that engine, never retried, and sends the
   last bytes only when the call answers 200 with the instance it knew when it forwarded the request. An answer with no
   body waits with its headers. Any other outcome fails the answer, with 503 "lake catalog not saved" before its headers
   went out and a broken connection after; another instance id also teaches the proxy the new one. The wait ends with
   the client's request.
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
  | `AckedCommitsAreSaved` | always | `acknowledged-commit-survives-crash`, `refused-save-is-an-error` | `test_scenario_acknowledged_commit_survives_crash` |
  | `NoAnswerStaysHeld` | always eventually | `waiting-commits-share-a-save` | `test_scenario_waiting_commits_share_a_save` |
- **Bounds**: two commits, two save slots behind one lock, one refused upload, one crash; every action atomic, so 3
  concurrent actions; 15 actions, the whole state space (16 reaches the same states).
- **Faults**: one engine crash at any step, restarting as a new instance from the uploaded catalog; one refused
  upload; crash on yield off.
- **Environment**: an upload that returns is durable (ADR-0278); a copy holds every commit made before it; an engine's
  answer ends only after its commit is visible to another connection; every answer passes the proxy.
- **Mutants**: `answer-first` (the interval mode), `id-after-copy`, `no-save-lock`, `refused-marks-saved` and
  `release-across-restart` fail `AckedCommitsAreSaved`; `no-save-call` fails liveness.
- **Result**: pass, 7,277 states, 4.1 s, FizzBee v0.5.4; wider (a second crash, 21 actions): pass, 18,413 states, 9.8 s.
- **Left to tests**: the two Quack facts the barrier rests on, on a real engine: an answer ends only after its commit
  is visible (`test_answer_ends_after_its_commit_is_visible`), and a client completes no answer before its last bytes
  (`test_client_needs_the_last_bytes`); the daemon kill and the power loss (experiment 0010's failure phase on the
  branch); the stop with calls waiting (`test_stop_answers_waiting_saves`).

## Temporary workarounds

None.

## Contracts

```text
POST /save on the engine's FUNCD_LAKE_SAVE_PORT (8081), reachable from the daemon only (lateral drop), no parameters
  200 {"engine": "<instance id>", "saved": <snapshot id>}   a good save covers the latest snapshot when the call came
  503 {"engine": "<instance id>", "error": "<why>"}         the save failed
```

```go
// internal/platform/config
func (c Config) LakeSaveInterval() (time.Duration, error) // CHANGED: 0 when the key is empty (save before answering)

// pkg/funcd
func WithLakeSaveInterval(d time.Duration) Option // CHANGED: 0 ⇒ save before answering

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

1. `internal/platform/config/lake.go`, `pkg/funcd/options.go` and `examples/funcdconfig.yaml`: an empty key is 0, "save
   before answering". `images/runtime/duckdb/shim.py`: the instance id, the save endpoint on a `ThreadingHTTPServer`
   thread, the saved id beside `_Checkpointer`'s lock, the retried start-up save, the stop's answer; no watcher.
2. `internal/services/lake`: `FUNCD_LAKE_SAVE_PORT` without the key; the save URL to `Ensure`. `internal/lake/gateway`:
   `SaveURL`, the instance learned before the first request, the barrier (a body reader that holds the last bytes, and
   an empty answer's headers), the single call, the 503.
3. Tests:
   - `engine_test.py` (a real engine, with a small proxy): `test_scenario_acknowledged_commit_survives_crash` (an
     acknowledged commit through `/save`, then SIGKILL, restart and read back), the two Quack-fact tests,
     `test_scenario_waiting_commits_share_a_save`, `test_scenario_read_waits_for_nothing_saved`, a refused save and the
     calls behind it answer 503, `test_stop_answers_waiting_saves`;
   - `internal/lake/gateway`: `TestBarrierHoldsTheLastBytes`, `TestScenarioRefusedSaveIsAnError`, an answer from
     another instance fails, no second call, no barrier without `SaveURL`;
   - `internal/platform/config` and `internal/services/lake`: an empty key is 0, the engine keys in each mode,
     `TestScenarioIntervalIsTheOptIn`;
   - the duckdb lane: a Venom case that SIGKILLs the engine right after an acknowledged commit and reads the row back.
4. At acceptance: the `Superseded in part by` back-links on ADR-0274 and ADR-0086.
5. Done when `just ci-full`, `just test-linux`, `just check-specs` and the duckdb lane pass, and experiment 0010's
   failure phase on the branch loses no acknowledged commit.

## Review checklist

- [ ] Without `lake.saveInterval`, no engine answer completes before a save covers the catalog's latest snapshot; the
  proxy holds at most one chunk of an answer.
- [ ] An answer is released only on a 200 from the instance the proxy knew when it forwarded the request; no `/save`
  call is retried, and any failure fails the answer.
- [ ] `/save` reads the snapshot id before the copy, saves under the stop's lock, and never records a failed save.
- [ ] An empty key reaches the reconciler as 0; with the key set, nothing changes from ADR-0274.
- [ ] The scenario tests carry their oracle markers and pass.

## Consequences

- **(+)** No acknowledged commit is lost to a crash of the engine, the daemon or the host (with ADR-0278).
- **(−)** Every request waits, not every query: a large result arrives over several requests, each held. A commit
  waits for a whole-catalog copy and upload that grows with the catalog, and a Lake commits fewer times a second; a
  read waits behind the unsaved commits before it (experiment 0010).
- **(−)** A busy Lake rewrites its whole catalog on the blob store's disk up to once per save, back to back: ADR-0274's
  500 ms floor no longer bounds those uploads.
- **(−)** While saves fail, the Lake answers errors to every request that follows an unsaved commit, and its phase
  stays Ready (ADR-0276). A hung upload holds every answer until the S3 client's timeouts (botocore's: 60 s to connect
  and to read, with retries) or the client's own deadline end it.
- **(−)** `lake.saveInterval` now chooses the mode too: `funcdctl backup plan lake --rpo` writes it, so it opts into
  interval saves.

## Open questions

- A mode per Lake rather than per platform: a later ADR, if Lakes with different needs share a platform.
- Whether `funcdctl backup plan lake --rpo` should stop writing the key, since the default loses nothing: the decider.
- An error that means "not committed" (hold until a save works, or restart the engine from its last save): a later
  ADR, if clients need it.
- Saves that upload only what changed: a later ADR, if a workload needs cheaper commits.

## References

#1255 · #1256 · #1243 · #634 · experiments 0009 and 0010 (pyvvo/experiment#19, #20) · `images/runtime/duckdb/shim.py` ·
`internal/lake/gateway/proxy.go`, `manager.go:65-70`, `:111` · `internal/services/lake/reconcile.go:389-406` ·
`internal/platform/config/lake.go:20-25` · `pkg/funcd/options.go:561-563` · `internal/network/nftables_linux.go:143` ·
ADR-0086 · ADR-0137 · ADR-0271 · ADR-0272 · ADR-0273 · ADR-0274 · ADR-0276 · ADR-0278
