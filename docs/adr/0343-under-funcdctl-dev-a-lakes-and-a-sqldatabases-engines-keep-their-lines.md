# ADR-0343: Under funcdctl dev, a Lake's and a SQLDatabase's engines keep their lines too (amends ADR-0342)

- **Status**: Proposed (2026-11-22)
- **Date**: 2026-11-22
- **Deciders**: green-0-rabbit
- **Tags**: observability, logs, dev, lake, sql
- **Realizes**: [FEAT-0020/F269](../feat/0020-feat-engine-logs.md) (dev's own Lake and SQL engines' logs)
- **Supersedes (in part)**, with the back-link at acceptance:
  [ADR-0342](0342-an-engines-output-is-kept-and-read-as-a-functions-logs.md) Scope (dev's own Lake and SQL engines,
  left out) and Decision 5 (under dev, only an add-on container's lines are kept).
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (dev's local engines),
  [ADR-0237](0237-sql-in-funcdctl-dev.md) (dev's SQL engine)

## Context & Need

ADR-0342 keeps every engine's lines on a server, but under `funcdctl dev` only an add-on container's. Dev runs a Lake's
engine, the DuckDB CLI, and a SQLDatabase's, rqlited, as its own processes, which log each line at Debug only
(`prefixLogWriter.line` in `internal/lake/devengine`, `engineOutput.line` in `internal/sql/devengine`), so
`funcdctl logs lake/<name>` against dev answers nothing while a server's answers. The decider asked on 2026-11-22 to
cover them in the same PR ("yes, add it to this PR").

## Scenarios

- `scenario: engine-logs-dev-lake-sql` — Given `funcdctl dev` runs SQLDatabase `todo-db`, When its engine writes lines,
  Then `funcdctl logs sqldatabase/todo-db` against dev returns them, and dev's terminal shows none of them; Given dev
  runs a Lake on its bundled engine, When the engine writes a line to stderr, Then `funcdctl logs lake/<name>` returns
  it.

## Scope

Unchanged (ADR-0342), but for its Out line on dev's own Lake and SQL engines, which this ADR brings in.

## Constraints & Decision drivers

Unchanged (ADR-0342).

## Alternatives considered

- **Leave them out (ADR-0342's Scope)**: dev's read of a Lake's or a SQLDatabase's logs answers nothing while a
  server's answers. Rejected by the decider.
- **Raise their Debug lines to Info in dev's log**: every engine line would reach dev's terminal, which the decider
  refused for add-ons (ADR-0342 Decision 5). Rejected.
- **Run dev's Lake and SQL engines as runtime workers**, so the output capture keeps them: it changes dev's engine
  model (ADR-0125, ADR-0237) for one log path. Rejected.

## Decision

5. **Dev.** (replaces ADR-0342 Decision 5's first sentence; the rest of it, the add-on containers' log file and its
   one printed line, holds) Under `funcdctl dev`, the add-on container runtimes (Docker, nerdctl) and dev's own Lake and
   SQL engine runners hand each line their engines write to dev's funclog store, past dev's printing tee, and never
   to dev's terminal or dev's own log beyond the Debug line it has. Dev's Lake engine hands its stderr lines: its
   stdout carries the CLI's rows, not log lines. Dev's SQL engine writes both streams to one writer, so its lines are
   kept with the source `stderr`. Dev's Lake and SQL engines get no log file of their own.

## Design model

None: no lifecycle, race or timer; the engines' existing line writers hand each line on.

## Behavior rows

- **funcdctl-dev.md**: "What dev's Lake and SQL engines print" (dev's store, and dev's log at Debug; never the
  terminal), `decided`.

## Temporary workarounds

None.

## Contracts

```go
// internal/lake/devengine and internal/sql/devengine, on their Runtime (new): provider.LineFollower (ADR-0342)
func (r *Runtime) FollowLines(fn func(spec provider.ProviderSpec, stream, line string))
```

The Lake and SQL providers of `pkg/funcd` set `FollowLines` on such a runtime to the executor's append-only capture
(ADR-0342 Decision 5), under the kind `Lake` or `SQLDatabase`, the runtime's spec naming no `ServedKind`.

## Implementation plan

- `internal/lake/devengine`: `FollowLines`; `prefixLogWriter.line` hands each line on, after its Debug line.
- `internal/sql/devengine`: `FollowLines`; `engineOutput.line` hands each line on, as `stderr`.
- `pkg/funcd/binding_providers.go`: set `FollowLines` on a Lake or SQL runtime that is a `provider.LineFollower`;
  `pkg/funcd/engineoutput.go`: the capture takes the kind when the spec names none.
- Tests: `engine-logs-dev-lake-sql` in `cmd/funcdctl` (`-tags dev`) on dev's SQL engine when rqlited is there, and on
  its Lake engine when bundled; unit tests that each writer hands each line on.
- Done when `just ci` passes and the scenario passes.

## Review checklist

- [ ] The scenario has a passing test of its name.
- [ ] Dev's terminal shows none of these lines; dev's Debug line stays.
- [ ] Dev's Lake engine hands stderr only; dev's SQL engine's lines carry `stderr`.

## Consequences

- (+) Dev and a server read the same engines' logs with the same command.
- (−) Dev's in-memory store grows with these engines' lines too (ADR-0342 Consequences).

## References

- FEAT-0020 F269; feature issue #1519, under the epoch's tracker #1517.
- ADR-0342, ADR-0125, ADR-0237.
