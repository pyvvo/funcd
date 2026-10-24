# ADR-0276: A Lake shows when its catalog saves fail, and an engine's output reaches the daemon log

- **Status**: Implemented (2026-10-24), review passed with two Minors, both fixed ([report](../reviews/adr-0276-implementation-claude-opus-5-5.md)); Reviewing 2026-10-24; Accepted 2026-10-24 by an `adr-batch` run the decider delegated ("you have batch"); judged twice:
  round 1's Blocker (the report read as untrusted data, engines only) and two Majors (no false recovery across an
  engine replacement; engine output whatever funclog's setting) and round 2's (no start-up save of a fresh catalog)
  folded
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, observability, runtime, provider
- **Realizes**: [FEAT-0003/F189](../feat/0003-feat-data-platform.md) (a Lake shows when its catalog saves fail)
- **Answers**: [ADR-0168](0168-raw-output-pipes-and-record-bound.md) Open question 15 (an engine's raw output)
- **Relates to**: [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (the stop's saves) ·
  [ADR-0274](0274-a-lake-saves-its-catalog-at-the-interval-its-recovery-objective-sets.md) (its open question: a
  condition for failing saves) · [ADR-0160](0160-worker-exit-reason.md) (a worker's file in its boot dir that the
  runtime reads) · [ADR-0087](0087-add-on-provider-runtime.md) (the provider runtime) · #1233

## Context & Need

A Lake engine saves its catalog once per `lake.saveInterval`; a commit it acknowledged survives a crash only once a
save holds it (ADR-0274). When saves fail, nothing on the platform shows it (#1233): the Lake stays Ready, the engine's
error goes to its stderr, which funcd keeps only as the worker's tail (ADR-0168), and the tail goes with the worker.
#1227, #1231 and #1232 each lost acknowledged commits this way and were found only with a debug build that copied the
engine's output into the daemon's journal. The decider chose, on 2026-10-24: the Lake reports failing saves, and an
engine's output reaches the daemon log. Refusing commits while saves fail was left for later.

## Scenarios

- `scenario: failing-saves-show-on-the-lake` — Given a Lake whose catalog saves are refused, When a client commits,
  Then within one save interval plus one supervision period the Lake's `CatalogSaved` condition is `False` with reason
  `SavesFailing` and the engine's error as its message, while its phase stays `Ready` and its consumers keep querying.
- `scenario: saves-recover` — Given that Lake, When a save succeeds again, Then within one save interval plus one
  supervision period `CatalogSaved` is `True` with reason `Saved`.
- `scenario: replaced-engine-keeps-the-failure` — Given a Lake whose saves fail, When its engine is replaced and the new
  engine's start-up save fails too, Then `CatalogSaved` stays `False` throughout, with no `True` in between.
- `scenario: unsafe-report-reads-empty` — Given an engine's report path is a symlink, a FIFO or larger than 1 KiB,
  When the runtime inspects the worker, Then the report reads empty (or its first 1 KiB for a large regular file) and
  the inspection does not block.
- `scenario: engine-output-in-the-daemon-log` — Given a Lake engine writes a line to stderr, with funclog on or off,
  Then the daemon's log holds that line with the stream, the owner kind, namespace and name, and the replica.
- `scenario: failed-stop-logged` — Given a stop whose last upload fails, Then the engine writes
  `funcd duckdb shim: the stop failed: <error>` before it exits.

## Scope

**In**: an engine's report file and its reading by the containerd driver; the duckdb shim's report and start-up
save; the Lake's `CatalogSaved` condition; every engine's stdout and stderr in the daemon log; the shim's stop log.
**Out**: refusing commits while saves fail (later, the decider); a report for Function workers; a `funcdctl get` table
of conditions (`-o json` prints them); funclog storage of engine output (funclog is keyed by Function).

## Constraints & Decision drivers

- A Lake that is not Ready suspends its PEP proxy and retracts its edge entry (`internal/services/lake/reconcile.go`,
  #716), so a failing-save signal that set the phase would refuse every query: more than the deferred refusal of
  commits.
- A worker can write its boot dir, so a file there is untrusted input to the daemon; today the runtime only checks
  that the port file exists (ADR-0160 `Listened`).
- The daemon is pure Go (ADR-0065): it cannot ask the engine over Quack; the engine must tell it.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A report after each save, read as untrusted data, engines only | the port file's place; survives no restart by design, so the condition keeps its value | one open and read per engine per inspection | chosen |
| A report written on failure and removed on success | simpler | a replaced engine reads as recovered while its saves still fail | rejected |
| The phase goes not Ready | the most visible | refuses reads and commits, more than the deferred step | rejected |
| The daemon reads the engine's log lines for a save failure | no report file | state parsed from log text | rejected |
| A health HTTP server in the engine on a second port | an HTTP probe | a second listener and probe per engine | rejected |
| The S3 gateway counts refused catalog PUTs | no engine change | misses a gateway the engine cannot reach (#1227, #1232) | rejected |

## Decision

1. **An engine's report.** The containerd driver gives each engine worker, of any owner kind but Function,
   `FUNCD_REPORTFILE`, a path in its boot dir beside `FUNCD_PORTFILE`. When it inspects such a worker it reads that
   file as untrusted data: opened read-only with no symlink followed and without blocking, only when it is a regular
   file, at most 1 KiB; anything else reads as empty. `Instance.Report` holds what it read. A Function worker gets no
   report file, and the process driver, which runs no engine, reports none.
2. **The engine reports each save.** When it recovered a catalog, the duckdb shim saves it once at start, after the
   ATTACH; a fresh catalog is first saved at its first commit, as today, so a misread 404 or a read before a restore
   wrote the catalog never overwrites it with an empty one. A failed start-up save is retried at each tick until one
   succeeds; then saves follow commits as before. After each save it replaces the report whole (a temporary name in the boot dir, then a rename): `saved <RFC 3339
   time>` on success, `failing since <RFC 3339 time of the first failure>: <error>` on failure, keeping the first
   failure's time while saves keep failing.
3. **The Lake's condition.** The provider runtime returns the ready replica's report in its status; only the Lake reads
   it, and an SQL engine's is empty. On each pass the Lake reconciler sets `CatalogSaved` with `ObservedGeneration`:
   `True`, reason `Saved`, on a `saved` report; `False`, reason `SavesFailing`, the report as message, on a `failing`
   one. With no report, while an engine starts or none is Ready, or with any other content, it keeps the value it had. The phase, the `Ready`
   condition, the proxy and the edge entry do not change with it. Each change of `CatalogSaved` is logged at warn
   (`False`) or info (`True`).
4. **An engine's output in the daemon log.** Whether or not funclog runs, the output capture sends each line an engine
   worker writes to the daemon's logger at info, with the stream, owner kind, namespace, name and replica; the worker's
   tail is kept as before. Both streams log at info because engines write routine lines to stderr (rqlited's log);
   the warn line of Decision 3 is the signal that survives `log.level: warn`.
5. **A failed stop is logged.** The shim's `_on_term` writes the error that ended its stop,
   `funcd duckdb shim: the stop failed: <error>`, then exits 0 as today.

## Design model

None: a report replaced after each save and read on each pass. Across an engine replacement the boot dir and its report
go, and the condition keeps its value until the new engine's start-up save reports; no step a stop or crash cuts can
turn it `True` without a save.

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime
const ReportFileEnv = "FUNCD_REPORTFILE"
const MaxReportBytes = 1024
type Instance struct {
	// …
	Report string // an engine's report file, read as untrusted, at most MaxReportBytes; "" for none (ADR-0276)
}

// internal/provider
type ProviderStatus struct {
	// …
	Report string // the ready replica's Instance.Report
}

// internal/services/lake
const (
	condCatalogSaved   = "CatalogSaved"
	reasonSaved        = "Saved"
	reasonSavesFailing = "SavesFailing"
)
```

```python
REPORT_KEY = "FUNCD_REPORTFILE"
def _report(path: str | None, failing_since: datetime | None, error: BaseException | None) -> None: ...
```

| Input | Output |
|---|---|
| the duckdb shim's save, at start and after commits | the report file replaced: `saved …` or `failing since …: …` |
| an engine's report file | `Instance.Report` → `ProviderStatus.Report` → the Lake's `CatalogSaved` condition |
| an engine's stdout or stderr line | a daemon log record at info |

## Implementation plan

1. `internal/runtime`: `ReportFileEnv`, `MaxReportBytes`, `Instance.Report`. `internal/runtime/containerd`: the
   variable for engine owners, and the untrusted read where `listened` reads the port file (`O_NOFOLLOW`,
   `O_NONBLOCK`, a regular-file check on the open file, a bounded read).
2. `internal/provider`: `ProviderStatus.Report`.
3. `internal/services/lake`: the condition, its keep-the-value rule and its log lines.
4. `pkg/funcd`: the output capture installed for engines whatever funclog's setting, and its engine branch.
5. `images/runtime/duckdb/shim.py`: the start-up save, `_report` after every save, `_on_term`'s log.
6. Tests:
   - `internal/runtime/containerd` (Linux) `TestScenarioUnsafeReportReadsEmpty`: a regular report is read and
     bounded; a symlink, a FIFO and a directory read as empty without blocking; a Function worker has no report
     variable;
   - `internal/services/lake`: `TestScenarioFailingSavesShowOnTheLake`, `TestScenarioSavesRecover`,
     `TestScenarioReplacedEngineKeepsTheFailure`;
   - `pkg/funcd` `TestScenarioEngineOutputInTheDaemonLog`, with funclog on and off;
   - `shim_test.py`: the start-up save of a recovered catalog, none for a fresh one, its retry, both report lines;
     `TestScenarioFailedStopLogged`;
   - the duckdb lane: a case that makes the saves fail (a prefix owned by another principal), then reads the
     condition and the engine's line in the journal.
7. At acceptance, the Backlog card "A Lake reports catalog saves that keep failing as a status condition (ADR-0274
   follow-up)" moves to In Progress.
8. Done when `just ci-full`, `just test-linux`, `just test-duckdb-engine` and the duckdb lane pass.

## Review checklist

- [ ] Only engine workers get `FUNCD_REPORTFILE`; the driver reads it with no symlink followed, without blocking,
  only as a regular file, at most 1 KiB.
- [ ] The engine saves at start and reports every save; a Lake's `CatalogSaved` follows the report, keeps its value
  without one, carries `ObservedGeneration`, and never changes the phase, the proxy or the edge entry.
- [ ] Every line an engine writes reaches the daemon log, with funclog on or off.
- [ ] The scenario tests pass.

## Consequences

- **(+)** A Lake that cannot save shows it in its status and in the daemon log, so a loss like #1227's, #1231's or
  #1232's is seen before the next restart.
- **(−)** The Lake keeps acknowledging commits it cannot save until the deferred refusal lands; the condition is the
  signal to act on.
- **(−)** Each engine start uploads its catalog once, and an engine that logs heavily fills the daemon log: the worker
  pipe's queue bounds its memory, not the log's volume.

## Open questions

- Refusing commits while saves fail: the decider's later step, for its own ADR.

## References

- #1233 · #1227 · #1231 · #1232 · ADR-0168 · ADR-0160 · ADR-0273 · ADR-0274 · ADR-0087 · ADR-0065 · #716
