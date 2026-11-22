# ADR-0343: Under funcdctl dev, a Lake's and a SQLDatabase's engines keep their lines too (amends ADR-0342)

- **Status**: Reviewing (2026-11-22, implemented); Accepted (2026-11-22), by the decider's go-ahead ("yes, add it to this PR"), after one round of an
  independent judge, its five Minors folded
- **Date**: 2026-11-22
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, dev, lake, sql
- **Realizes**: [FEAT-0020/F269](../feat/0020-feat-engine-logs.md) (dev's own Lake and SQL engines' logs)
- **Supersedes (in part)**, with the back-link at acceptance:
  [ADR-0342](0342-an-engines-output-is-kept-and-read-as-a-functions-logs.md) Scope, its Out line "dev's own Lake and
  SQL engines, subprocesses that log at Debug, which store nothing", and Decision 5, which gains a paragraph; its
  sentences stand.
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (dev's local engines),
  [ADR-0237](0237-sql-in-funcdctl-dev.md) (dev's SQL engine),
  [ADR-0260](0260-a-loopback-listener-owns-its-port.md) Decision 3 (the SQL engine's listen check)

## Context & Need

ADR-0342 keeps every engine's lines on a server, but under `funcdctl dev` only an add-on container's. Dev runs a Lake's
engine, the DuckDB CLI, and a SQLDatabase's, rqlited, as its own processes, which log each line at Debug only
(`prefixLogWriter.line` in `internal/lake/devengine`, `engineOutput.line` in `internal/sql/devengine`), so
`funcdctl logs lake/<name>` against dev answers nothing while a server's answers. The decider asked on 2026-11-22 to
cover them in the same PR ("yes, add it to this PR").

## Scenarios

- `scenario: engine-logs-dev-lake-sql` — Given `funcdctl dev` runs SQLDatabase `todo-db`, When its engine writes lines,
  Then `funcdctl logs sqldatabase/todo-db` against dev returns them, each with the source `stderr`, and dev's terminal
  shows none of them; Given dev's Lake engine writes a line to stderr, as its init error, When the runner hands it on,
  Then the line is kept under `Lake` and the Lake's name, and dev's terminal shows none of it.

## Scope

Unchanged (ADR-0342), but for its Out line on dev's own Lake and SQL engines, which this ADR brings in.

## Constraints & Decision drivers

Unchanged (ADR-0342).

## Alternatives considered

- **Leave them out (ADR-0342's Scope)**: dev's read of a Lake's or a SQLDatabase's logs answers nothing while a
  server's answers. Rejected by the decider.
- **Raise their Debug lines to Info in dev's log**: dev's log is its terminal, so every engine line would reach it,
  which the decider refused for add-ons (ADR-0342 Decision 5). Rejected.
- **Run dev's Lake and SQL engines as runtime workers**, so the output capture keeps them: it changes dev's engine
  model (ADR-0125, ADR-0237) for one log path. Rejected.
- **A log file per engine, as an add-on container's**: dev runs these engines itself and already reports a failed
  start's last lines; the store and `funcdctl logs` serve the rest. Rejected as a second copy.
- **Each stream's own source for dev's SQL engine**: os/exec hands both streams to one writer (`cmd.Stdout` and
  `cmd.Stderr` are the same `engineOutput`), which cannot tell them apart, and two writers would split the listen
  check of ADR-0260 Decision 3. Rejected: rqlited writes its logs to stderr.

## Decision

5. **Dev.** ADR-0342 Decision 5 stands, and gains: dev's own Lake and SQL engine runners hand each line their engines
   write to dev's funclog store, past dev's printing tee, never to dev's terminal; the Debug line of dev's log stays,
   which dev's log, at Info, shows nowhere. Dev's Lake engine hands its stderr lines: its stdout carries the CLI's rows,
   not log lines. Dev's SQL engine writes both streams to one writer, so its lines are kept with the source `stderr`.
   These engines get no log file. The writers bound no line, as their Debug line is not bounded: these are dev's
   engines, on the developer's machine.

## Design model

None: no lifecycle, race or timer; the engines' existing line writers hand each line on.

## Behavior rows

- **funcdctl-dev.md**: "What dev's Lake and SQL engines print" (dev's store; the terminal shows none; lines written
  while dev stops are lost), `decided`.

## Temporary workarounds

None.

## Contracts

```go
// internal/provider (new): embedded by a runtime that reads its engines' output itself, it makes it a LineFollower
type LineHook struct{ /* the func FollowLines sets, under a lock of its own */ }
func (h *LineHook) FollowLines(fn func(spec ProviderSpec, stream, line string))
func (h *LineHook) LinesOf(spec ProviderSpec, stream string) func(line string) // nil when FollowLines set none

// internal/lake/devengine and internal/sql/devengine: their Runtime embeds provider.LineHook
```

The Lake and SQL providers of `pkg/funcd` set `FollowLines` on such a runtime to the executor's append-only capture
(ADR-0342 Decision 5), under the kind `Lake` or `SQLDatabase`, the runtime's spec naming no `ServedKind`. Dev's
add-on runtimes embed the same `LineHook`.

| Dependency or output | What |
|---|---|
| Consumes | dev's funclog store, past its printing tee |
| Exposes | segments under `logs/<ns>/lakes.<name>/` and `logs/<ns>/sqldatabases.<name>/` in dev's store |
| Config keys | none new |

## Implementation plan

- `internal/provider`: `LineHook`; dev's Docker runtime embeds it in place of its own func.
- `internal/lake/devengine`: the runtime embeds `LineHook`; `prefixLogWriter.line` hands each line on, after its
  Debug line.
- `internal/sql/devengine`: the runtime embeds `LineHook`; `engineOutput.line` hands each line on, as `stderr`.
- `pkg/funcd`: `followDevLines` sets `FollowLines` under a kind when the spec names none; `binding_providers.go` calls
  it for the Lake and SQL runtimes, whose `lakeDeps` and `sqlDeps` carry the capture from `funcd.go`, as `addonDeps`
  does.
- Tests: `engine-logs-dev-lake-sql` in `cmd/funcdctl` (`-tags dev`) on dev's SQL engine, which runs only when the SQL
  engine is bundled (`just build-sql-engine`; `just ci`'s placeholder skips it); the Lake half on the runner's fake
  engine in its `fail` mode, whose init error goes to stderr, and on the capture in `pkg/funcd`; each writer's
  hand-on.
- Done when `just ci` passes and the scenario passes on a bundled SQL engine.

## Review checklist

- [ ] The scenario has a passing test of its name, on a bundled SQL engine.
- [ ] Dev's terminal shows none of these lines.
- [ ] Dev's Lake engine hands stderr only; dev's SQL engine's lines carry `stderr`.

## Consequences

- (+) Dev and a server read an engine's logs with the same command.
- (−) Dev's lines differ from a server's: dev's Lake engine is the DuckDB CLI, not the server's shim, and every dev SQL
  line carries `stderr`.
- (−) Lines written while dev stops are lost: dev stops these engines after funclog has closed, as its add-on
  containers (ADR-0342 Decision 5).
- (−) Dev's in-memory store grows with these engines' lines too (ADR-0342 Consequences).

## Open questions

None.

## References

- FEAT-0020 F269; feature issue #1519, under the epoch's tracker #1517.
- ADR-0342, ADR-0125, ADR-0237, ADR-0260.
