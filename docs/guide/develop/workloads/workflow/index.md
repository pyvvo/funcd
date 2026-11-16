---
title: Workflow
description: Run a Workflow, start runs and read their steps and logs, branch on a step's output, share a database between steps, and retry, pause, cancel or replay a run.
---

## Usage

A [Workflow](../../../platform/workloads/workflow.md) runs a series of **steps** for each input, and passes each
step's output to the steps after it. A step calls a Function, runs another Workflow, or is a **built-in step** that
funcd runs itself.

The first example runs the TypeScript Workflow `pipeline` once, a **run**, with the amount 4; its child run's step
logs the score 40. Python steps run the same way, as [Share a database between steps](#share-a-database-between-steps)
shows. Install the [dev build of funcdctl](../../../get-started/install.md) and Git, and put Node.js on the `PATH`.

### TypeScript

Clone the repository and start the example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/workflow --cport 8081
```

The Workflow `scorer`, in `child.yaml`. A step that names an `image` runs as a Function that funcd creates and names
`<workflow>-<step>`, here `scorer-s`; `funcdctl dev` runs the build named by the image's tag, `score.mjs`, with its
**contract**, `score.contract.yaml`, the JSON Schema of the step's input and output:

::include[../../../../../_repos/funcd-typescript/examples/workflow/child.yaml]

The Workflow `pipeline`, in `parent.yaml`. A run starts at the first step, `feed`. `sub` runs once the steps under
[`dependsOn`](spec.md#workflowspecsteps) have succeeded, with their output; a step without `dependsOn` follows the one
listed before it. `sub` runs `scorer` as a **child run** through [`workflow.ref`](spec.md#workflowspecstepsworkflow).
[`pooling`](spec.md#workflowspecpooling) gives each step's Function a worker of its own and keeps it running:

::include[../../../../../_repos/funcd-typescript/examples/workflow/parent.yaml]

In another terminal, start a run with [`funcdctl workflow run`](../../tools/funcdctl/spec.md#funcdctl-workflow-run),
then repeat [`describe`](../../tools/funcdctl/spec.md#funcdctl-workflow-describe) until the phase is `Succeeded`,
`Failed` or `Cancelled`. Without a name, funcd names a run after its Workflow with a suffix; two runs that exist at
once cannot share a name.

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow run pipeline run-1 --input '{"amount":4}'
started WorkflowRun/run-1
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe run-1
RUN run-1   phase: Succeeded
  feed   phase: Succeeded   attempts: 1   duration: 52ms
  sub   phase: Succeeded   duration: 2ms
trace: e5c86be4e947979338a5dac957c36c34
full logs: funcdctl workflow logs run-1
```

`attempts` counts calls of a step's Function; `sub` calls none itself. The **trace ID** is carried by each call of the
run. A step's handler is a [Function's handler](../function/index.md): its input arrives in `event.data` and its
return value is the step's output. The handler of `score`:

::include[../../../../../_repos/funcd-typescript/examples/workflow/src/score/main.ts]

The `funcdctl dev` terminal shows each step's input reaching the next:

```console
  17:35:15  pipeline-feed INFO   ingest args=["ingest",4]
  17:35:15  scorer-s     INFO   score args=["score",40]
```

Keep the example running for the next two sections.

## Read a run's logs

[`funcdctl workflow logs`](../../tools/funcdctl/spec.md#funcdctl-workflow-logs) prints a run's records, child runs
included, found by its trace ID. Records appear within `funclog.segmentMaxAge`, or about a second under
`funcdctl dev`, so repeat the command:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow logs run-1 -o wide
2026-11-13T16:35:15.491Z [INFO] 0 ingest source=console inv=4631a4b503407a8a trace=e5c86be4e947979338a5dac957c36c34 args=["ingest",4]
2026-11-13T16:35:15.493Z [INFO] 0 score source=console inv=babcd4d53d7f7995 trace=e5c86be4e947979338a5dac957c36c34 args=["score",40]
```

`-o wide` adds `source` (`console`, `logging`, `stdout` or `stderr`), `inv`, the call, and `trace`. `--step feed`
keeps one step's records; a built-in or workflow step has none. `--severity`, `--since` and `--limit` filter as for
[`funcdctl logs`](../function/index.md#read-a-functions-logs).

## Replay a run from a step

[`funcdctl workflow replay`](../../tools/funcdctl/spec.md#funcdctl-workflow-replay) starts a new run from an ended one:
the step that `--from` names and the steps after it run again, and the others keep their outputs:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow replay run-1 --from sub --name run-2
replay run-2 created (of run-1 from sub)
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe run-2
RUN run-2   phase: Succeeded
  feed   phase: Succeeded
  sub   phase: Succeeded   duration: 1ms
replay of: run-1 (from sub)
trace: 682e64a6e91d67d9847a012df0fe9eb2
full logs: funcdctl workflow logs run-2
```

The source must be an ended run of the same Workflow, not yet removed by `workflow.retention`, in which no step outside
the replayed ones failed or was cancelled. When a replayed step's image changed, the replay fails with `DigestDrift`,
unless `--allow-drift`. Press Ctrl-C in the first terminal to stop.

## Branch on a step's output

By default, a step runs once all its parents have succeeded.

- [`when.condition`](spec.md#workflowspecstepswhen) is a JavaScript expression between `${{` and `}}` over `input` and
  `step.<name>.output` of a parent whose output has a schema. A false condition makes the step `Skipped`, with the
  steps that need it.
- [`join`](spec.md#workflowspecsteps) `all`, the default, skips the step unless every parent succeeded; `any` waits for
  every parent, then runs on the outputs of those that succeeded.

funcd checks each condition against the contracts before the Workflow is Ready: one that reads a missing field, uses
another type, or reads a built-in step's output leaves it `Pending` with `WhenTypeError`.

### TypeScript

The Workflow `orders`, in `workflow.yaml`, runs `hi` for a score above 30 and `lo` otherwise; `report` joins both with
`any`:

::include[../../../../../_repos/funcd-typescript/examples/workflow/workflow.yaml]

## Wait or reshape data without a Function

The last two steps of `orders` are built-in steps, with no Function:

- [`builtin.wait`](spec.md#workflowspecstepsbuiltin) waits a duration, or a `${{ }}` expression's, then passes its
  input on. It counts toward the run's [`timeout`](spec.md#workflowspec), and a cancel ends it.
- `builtin.pass` outputs the value of a `${{ }}` expression, checked only when the step runs, so an error fails it.

## Pass data between steps

- The first step receives the run's input. A step with one parent receives its output; with several, an object of
  their outputs keyed by step name.
- A step's [`params`](spec.md#workflowspecsteps) are merged over its input, and win. A wait step ignores them.
- An input or output holds at most `workflow.payloadLimit` bytes; larger or shared data goes in a
  [store](#share-a-database-between-steps).

## Share a database between steps

A Workflow declares its stores at the root of its spec, under [`spec.kv`](spec.md#workflowspeckv),
[`spec.buckets`](spec.md#workflowspecbuckets), [`spec.sql`](spec.md#workflowspecsql),
[`spec.lakes`](spec.md#workflowspeclakes), [`spec.configMaps`](spec.md#workflowspecconfigmaps) and
[`spec.secrets`](spec.md#workflowspecsecrets): a `name` defines a store, whose `owner` names a step that can write it,
and a `ref` declares an existing one. A step reaches a store only through its
[`function.bindings`](spec.md#workflowspecstepsfunctionbindings), which allow reading; a `ref` step keeps its
Function's own bindings.

The Python example sql-readings stores a sensor's values in one step and summarizes them in the next. It needs
[uv](https://docs.astral.sh/uv/), which installs the steps' packages and, if needed, Python 3.12. Stop the first
example first, since both use the port 8081.

### Python

The Workflow, in `workflow.yaml`, makes `ingest` the owner of `readings-db`:

::include[../../../../../_repos/funcd-python/examples/sql-readings/workflow.yaml]

The handler of `ingest` creates the table, stores the values after the sensor's existing ones, and returns their
count; [`summarize`](https://github.com/pyvvo/funcd-python/blob/main/examples/sql-readings/src/summarize/main.py) reads
them back:

::include[../../../../../_repos/funcd-python/examples/sql-readings/src/ingest/main.py]

`run.yaml` holds a WorkflowRun, which `funcdctl dev` applies with the folder, so the run starts with it:

::include[../../../../../_repos/funcd-python/examples/sql-readings/run.yaml]

Start the example with `uv run`, which installs the packages into `.venv` and runs `funcdctl dev` with its `python3`:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/sql-readings
$ uv run funcdctl dev . --cport 8081
```

In another terminal, read the run until its phase is final:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe readings-run-1
RUN readings-run-1   phase: Succeeded
  ingest   phase: Succeeded   attempts: 1   duration: 1s54ms
  summarize   phase: Succeeded   attempts: 1   duration: 230ms
trace: 293c42211458963a246f563da7f19a6d
full logs: funcdctl workflow logs readings-run-1
```

Another run for the same sensor adds its values after the stored ones. Press Ctrl-C in the first terminal to stop.

## Retry a step and handle a failed run

- A 5xx answer, a timeout or a transport error is retried; a 4xx answer, such as a contract's 422, fails the step at
  once, with an error such as `workflow.dispatch: default/pipeline-feed rejected the step (422)`.
- [`function.retry.maxAttempts`](spec.md#workflowspecstepsfunctionretry) counts the calls, the first included, by
  default `workflow.defaultRetry`, 1. `retry.backoff`, by default `workflow.defaultRetryBackoff`, 0 seconds, doubles at
  each call, up to an hour.
- [`function.timeout`](spec.md#workflowspecstepsfunction), by default `workflow.defaultStepTimeout`, 300 seconds,
  bounds each call.
- A failed step fails the run with `StepFailed`: its dependents are `Skipped`, and steps still running elsewhere go back
  to `Pending`. The Workflow's [`timeout`](spec.md#workflowspec), paused time excluded, fails a run with
  `RunTimedOut`.
- [`onFailure`](spec.md#workflowspec) names a step called once when a run fails, with `workflow`, `run`, `failedStep`,
  `reason` and `input`; it never changes the phase, and a cancel does not call it.

A step can be called more than once, even past `maxAttempts` when a restart cuts a call; the `X-Funcd-Attempt` header
carries the attempt number. Make a repeated call harmless.

## Pause, resume or cancel a run

[`funcdctl workflow pause`](../../tools/funcdctl/spec.md#funcdctl-workflow-pause) sets the run's
[`paused`](spec.md#workflowrunspec): nothing new starts, calls in flight finish, and the phase is `Paused`, which the
timeout does not count. [`resume`](../../tools/funcdctl/spec.md#funcdctl-workflow-resume) clears it.
[`cancel`](../../tools/funcdctl/spec.md#funcdctl-workflow-cancel) starts nothing more and drops the calls in flight,
which may still have done their work; the run ends `Cancelled`, without `onFailure`. A cancel wins over a pause, and an
ended run ignores both.

## Start a run on a schedule

A [Sensor](../../services/events/index.md) action that names a
[`workflow`](../../services/events/spec.md#sensorspecdo) starts a run for each event, with the action's input; with a
`timer` EventSource, on a [schedule](../../services/events/spec.md#eventsourcespectimerevents). The run is named after
the Sensor and the action; the action succeeds once the run exists, and a run whose input does not fit the contract or
`workflow.payloadLimit` fails at its start.

## Limits and quotas

- An input or output holds at most [`workflow.payloadLimit`](../../running-funcd/spec.md#funcdconfigyaml), 256 KiB by
  default: a larger input is refused, or fails a Sensor's run with `PayloadLimitExceeded`; a larger output fails its
  step, with no retry.
- At most `workflow.maxStepsInFlight` step calls run at once, 64 by default.
- A step's `timeout` lies up to 24 hours, a run's up to 168 hours; `retry.maxAttempts` from 1 to 100.
- Child runs nest at most 8 levels deep, or the run fails with `SubworkflowDepthExceeded`.
- `<workflow>-<step>` must fit in 63 characters.

## Data storage

funcd keeps each run's progress in its run state, `<storage.dataDir>/workflow` by default, lost with
`storage.mode: memory`. A run survives a restart: a cut call runs again, a `wait` starts over, and a child run restarts
from its first step. funcd deletes an ended run after `workflow.retention`, 720 hours by default.

## Status

Read a Workflow with `funcdctl get workflow <name> -n <namespace> -o json`, and a run with
`funcdctl workflow describe <run>`; [`funcdctl workflow runs`](../../tools/funcdctl/spec.md#funcdctl-workflow-runs)
lists a namespace's runs, newest first. Before a Workflow is Ready, funcd checks every **edge**, from a step's output
to the next step's input: each required field must come from its parents or `params`, with the same type. A failed
check sets `SchemaMismatch` to `True`, and the message names the step and the field.

| Object and phase | Reason | Meaning |
| --- | --- | --- |
| Workflow `Ready` | `EdgesTypeChecked` | Every check passed, and runs can start. |
| Workflow `Pending` | `EdgeTypeMismatch` | An edge does not match: a step's required field is missing from its parents and `params`, or has another type. |
| Workflow `Pending` | `WhenTypeError` | A `when` condition reads a field that a parent's output does not have, uses a field as a value of another type, or reads a built-in step's output. |
| Workflow `Pending` | `WorkflowCycle` | The Workflow reaches itself through workflow steps. |
| Workflow `Pending` | `StoreNotOwned`, or `KVStoreNotOwned` for a KV store | A store that the Workflow defines by name belongs to another object. |
| Workflow with no phase, or `Pending` | `ArtifactNotReady`, after an edit | A step's image has not reached its registry, or a child Workflow is missing or not Ready. On a first apply the Workflow has no phase and no `Ready` condition while it waits. A Workflow that was Ready and is then edited is `Pending` with this reason. funcd checks again every `workflow.artifactPollInterval`, 5 seconds by default, and the runs wait. |
| Workflow `Pending` | Another reason, such as `MaterializeFailed` | The message gives the cause. |
| Run `Pending` | `WorkflowNotFound` or `WorkflowNotReady` | The run waits to start, for its Workflow to exist or to become Ready. |
| Run `Running` | none | Its steps run. |
| Run `Paused` | none | The run is paused, as [Pause, resume or cancel a run](#pause-resume-or-cancel-a-run) explains. |
| Run `Succeeded` | none | Every step ended, and the run succeeded. |
| Run `Cancelled` | none | The run was cancelled. |
| Run `Failed` | `StepFailed` | A step failed, as [Retry a step and handle a failed run](#retry-a-step-and-handle-a-failed-run) explains. |
| Run `Failed` | `RunTimedOut` | The run passed the Workflow's `timeout`. |
| Run `Failed` | `InputSchemaMismatch` | The input does not match the Workflow's contract. |
| Run `Failed` | `PayloadLimitExceeded` | The run's input is larger than `workflow.payloadLimit` when the run starts. A step output that is too large fails its step with no retry, and the run with `StepFailed`. |
| Run `Failed` | `SubworkflowDepthExceeded` | Child runs nest more than 8 levels deep. |
| Run `Failed` | `RunRecordTooLarge` | The run's state is too large for the store of the run state. |
| Run `Failed`, with the condition `ReplaySeeded` `False` | `DigestDrift` or `SeedInvalid` | A replay cannot start from its source run, as [Replay a run from a step](#replay-a-run-from-a-step) explains. |

A step is `Pending`, `Running`, `Succeeded`, `Failed`, `Skipped` or `Cancelled`. A WorkflowRun's `status.steps` holds
each step's attempts, times and, for a failure, the error's first line; no step output, so a result needed later goes
to a store. `status.pins` names the Function revisions that the run calls.

## Changes and deletion

- A run pins its Workflow's spec, its child Workflows and the Functions' current revisions when it starts; later edits
  reach only new runs.
- A WorkflowRun's `workflow`, `input` and `replay` are fixed: a new input takes a new run. Deleting an unfinished run
  cancels it.
- Deleting a Workflow cancels its started runs, and deletes the Functions of its image steps and the ConfigMaps and
  Lakes that it defines; a KV store, Bucket or SQL database that it defines stays unless its `deletion` is `delete`.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `image "<image>" is digest-pinned and has no :tag naming a build`, or `has no :tag naming a build` | Give the step's `image` a tag that names its build, as [Run it locally](#run-it-locally) describes. |

## Run it locally

- `funcdctl dev` runs an image step from the build that its tag names: `<tag>.mjs`, in the folder or `dist`, with its
  contract beside it, or for Python the entry of `[tool.funcd-bundle.functions]` in `pyproject.toml`, else
  `src/main.py` in a folder of the tag's name, else `<tag>.py`.
- It applies every file of the folder, a WorkflowRun such as `run.yaml` included.
- It gives each step's Function its own worker; with `minReplicas` 0, the worker stops after about 5 minutes idle.
- A changed build or contract gives the step's Function a new revision within a second; a step added after the start
  needs a restart.
- The run state stays in memory, even with [`--persist`](../function/index.md#run-it-locally).

## Examples

- [workflow in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/workflow): a child run, a
  branch, a wait and a pass.
- [sql-readings in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/sql-readings): steps that share a
  SQL database.
