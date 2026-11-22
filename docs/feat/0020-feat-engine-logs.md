# FEAT-0020: An engine's logs, read as a Function's are

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-11-22
- **Deciders**: green-0-rabbit
- **Defines**: an **engine logs epoch**: the output of every engine that funcd runs for an object (an add-on
  object's engine, a Lake's, a SQLDatabase's) is kept and read as a Function's logs are, so a developer reads the
  engine of an object they declared without access to the host. Metrics come later, on the same identity.

## Initial need

funcd runs an engine for each add-on object, Lake and SQLDatabase. An engine's stdout and stderr reach only funcd's
own log on the host (ADR-0276 Decision 4), and under `funcdctl dev` an add-on's engine writes into its Docker
container (ADR-0334). `funcdctl logs` reads Functions and workflow runs only (ADR-0084, ADR-0106). A developer whose
IdentityProvider does not turn Ready, or whose Lake refuses a query, cannot read the engine's reason without the host.
ADR-0276 left the storage of engine output in funclog out of its scope.

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decider chose on 2026-11-22: build it on funcd's
OpenTelemetry mechanism, logs first, metrics prepared but not built ("we don't have metrics yet so let's just focus
on logs even though you can prepare metric"), and the agent's picks for the rest ("your picks"): the read rule of a
Function's logs, every engine kind, `funcdctl logs <kind>/<name>`, and `funcdctl dev` following an add-on's
container.

## Features

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F267 | **An engine's logs are read as a Function's**: every line an engine writes for an add-on object, a Lake or a SQLDatabase is kept with the Functions' logs, under the object's kind and name, and read with `funcdctl logs <kind>/<name>` by whoever may read the object; `funcdctl dev` keeps an add-on container's lines as well, without printing them in its terminal | FEAT-0004 (Function logs), FEAT-0018 | [ADR-0342](../adr/0342-an-engines-output-is-kept-and-read-as-a-functions-logs.md) | implemented |
| F269 | **Dev's own Lake and SQL engines' logs**: under `funcdctl dev`, the lines of the Lake and SQLDatabase engines that dev runs itself are kept in dev's store, and read with `funcdctl logs lake/<name>` and `funcdctl logs sqldatabase/<name>`, still out of dev's terminal | F267 | [ADR-0343](../adr/0343-under-funcdctl-dev-a-lakes-and-a-sqldatabases-engines-keep-their-lines.md) | accepted |
| F268 | **An engine's metrics**: an engine's metrics reach the operator's collector under the same kind, namespace, name and replica as its logs | F267 | — | idea |

## How it lands on funcd (high level)

Nothing new to run: funcd already captures every engine's output (ADR-0276) and keeps the Functions' logs as
OpenTelemetry log records (ADR-0081). The engine's lines join that store under their owner, and the logs read gains
the engine kinds.

## Out of scope

- An engine's metrics and traces (F268 is an idea).
- Sending Function or engine logs to the operator's OpenTelemetry collector.
- The logs of a settings kind's objects: they run no engine.
