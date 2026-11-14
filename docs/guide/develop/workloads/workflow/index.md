---
title: Workflow
description: Run a Workflow, start runs and read their steps and logs, branch on a step's output, share a database between steps, and retry, pause, cancel or replay a run.
---

## Usage

A Workflow runs a series of **steps** for each input that it receives, and passes the output of each step on to the
steps after it. A step calls a Function, runs another Workflow, or is a **built-in step**, which funcd runs itself,
with no Function. See [how funcd drives a Workflow](../../../platform/workloads/workflow.md) for the concept.

The first example runs the Workflow `pipeline` of the TypeScript example `workflow`, starts one **run**, an execution
of the Workflow with an input, with the amount 4, and reads the result: the run ends `Succeeded`, and the step of its
child run logs 40, the score of the amount 4. Steps in Python run the same way, as
[Share a database between steps](#share-a-database-between-steps) shows.

Install the [dev build of funcdctl](../../../get-started/install.md) and Git, and put Node.js on the `PATH`.

`funcdctl dev` starts a local platform and applies the resource files at the top level of one folder to it. `--cport`
sets the port of its control plane, which a `funcdctl` command reaches with `--server` and the token
`funcd-dev-token`. The example's folder also holds `funcdconfig.yaml`, a funcd server configuration that
`funcdctl dev` skips, and `workflow.yaml`, the Workflow `orders` of
[Branch on a step's output](#branch-on-a-steps-output).

### TypeScript

Clone the repository and start the example from its root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/workflow --cport 8081
```

The Workflow `scorer`, in `examples/workflow/child.yaml`, has the single step `s`. A step that names an `image`, the
[artifact](../../../platform/workloads/artifacts.md) that a platform pulls, runs as a Function that funcd creates and
names `<workflow>-<step>`, here `scorer-s`. `funcdctl dev` runs instead the build whose name is the image's tag, the
part after the last `:`: the tag `score` names `score.mjs`, which the repository holds already built, and
`score.contract.yaml`, the step's **contract**, the JSON Schema of the input that the step accepts and of the output
that it returns:

::include[../../../../../_repos/funcd-typescript/examples/workflow/child.yaml]

The Workflow `pipeline`, in `examples/workflow/parent.yaml`, starts each run at its first step, `feed`, which runs
`ingest.mjs` as the Function `pipeline-feed` and receives the run's input. The step `sub` names `feed` under
[`dependsOn`](spec.md#workflowspecsteps), so it runs once `feed` has succeeded and receives the output of `feed`; a
step that sets no `dependsOn` follows the step listed before it. `sub` names `scorer` under
[`workflow.ref`](spec.md#workflowspecstepsworkflow), so it runs `scorer` as a **child run**, whose input is the step's
input and whose output becomes the step's output:

::include[../../../../../_repos/funcd-typescript/examples/workflow/parent.yaml]

Both Workflows set [`pooling`](spec.md#workflowspecpooling): `mode: isolated` gives the Function of each step a
[worker](../../../platform/workloads/index.md#runtime) of its own, a process that runs the Function's code, and
`minReplicas: 1` keeps that worker running while no call comes.

[`funcdctl workflow run`](../../tools/funcdctl/spec.md#funcdctl-workflow-run) creates a WorkflowRun object, with the
name that follows the Workflow's name and the input that `--input` gives as JSON. Without a name, funcd names the run
after the Workflow, with a generated suffix. Two runs that exist at the same time in a namespace cannot share a name,
but a new run can take the name of a deleted one.
[`funcdctl workflow describe`](../../tools/funcdctl/spec.md#funcdctl-workflow-describe) prints the run's phase; each
step's phase, the number of calls made to its Function (`attempts`) and its duration; and the run's **trace ID**, an
identifier that each call of the run carries and by which funcd finds the run's log records. funcd starts a run a
moment after it is created, and until then `describe` prints no phase and no step.

In another terminal, start a run of `pipeline` with the amount 4, then repeat `describe` until the phase reads
`Succeeded`, `Failed` or `Cancelled`:

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

The durations and the trace ID differ at each run. `sub` shows no attempts, because it calls no Function itself: the
step `s` of its child run calls `scorer-s`. [Status](#status) explains each phase, and the [Spec](spec.md) tab
describes every field of a Workflow and of a WorkflowRun.

Each step's handler is a Function's handler, as the [Function](../function/index.md) page explains: it receives the
step's input in `event.data`, and its return value becomes the step's output. The handler of `ingest`,
[`src/ingest/main.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/workflow/src/ingest/main.ts), logs
the amount and returns it. The handler of `score` multiplies the amount by 10, logs the result with `context.log`, and
returns both numbers:

::include[../../../../../_repos/funcd-typescript/examples/workflow/src/score/main.ts]

To check that each step received the output of the one before it, read the lines that the steps logged in the
terminal that runs `funcdctl dev`: `pipeline-feed` logged the amount 4, and `scorer-s` logged 40. The times differ at
each run:

```console
  17:35:15  pipeline-feed INFO   ingest args=["ingest",4]
  17:35:15  scorer-s     INFO   score args=["score",40]
```

Keep the example running for [Read a run's logs](#read-a-runs-logs) and
[Replay a run from a step](#replay-a-run-from-a-step), which stops it.

## Read a run's logs

[`funcdctl workflow logs`](../../tools/funcdctl/spec.md#funcdctl-workflow-logs) prints the log records of one run: those
of each step's Function, the steps of its child runs included, which funcd finds by the run's trace ID. A record shows
there only after funcd has written it to storage: a funcd server writes the records in segments up to
`funclog.segmentMaxAge` old, 10 seconds by default, and `funcdctl dev` in segments up to about a second old, so repeat
the command until the records appear. While the first example runs, print the records of `run-1` with their
attributes:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow logs run-1 -o wide
2026-11-13T16:35:15.491Z [INFO] 0 ingest source=console inv=4631a4b503407a8a trace=e5c86be4e947979338a5dac957c36c34 args=["ingest",4]
2026-11-13T16:35:15.493Z [INFO] 0 score source=console inv=babcd4d53d7f7995 trace=e5c86be4e947979338a5dac957c36c34 args=["score",40]
```

Each line shows the record's time, its severity, the number of the worker that wrote it and its message. `-o wide`
adds the record's attributes:

- `source` says how the code wrote the record: `console` for a `console` call or a `context.log` call in TypeScript,
  `logging` for the `logging` module in Python, and `stdout` or `stderr` for a line written to standard output or to
  standard error.
- `inv` identifies the call of the step's Function, and `trace` is the run's trace ID.
- `args` holds the arguments of the `console` or `context.log` call.

The times and the values of `inv` and `trace` differ at each run. `--step` keeps the records of one step's Function,
such as `--step feed`; a built-in step and a workflow step, such as `sub`, have no Function of their own, so the
command refuses them. `--severity`, `--since` and `--limit` filter the records as for `funcdctl logs`, which the
[Function](../function/index.md#read-a-functions-logs) page describes; by default, the command prints the most recent
1000 records.

## Replay a run from a step

[`funcdctl workflow replay`](../../tools/funcdctl/spec.md#funcdctl-workflow-replay) starts a new run from a run that
has ended. The step that `--from` names, and each step that depends on it, run again; the other steps keep the
outputs that they had in the source run. While the first example runs, replay `run-1` from its step `sub` under the
name `run-2`, and repeat `describe` until the phase reads `Succeeded`, `Failed` or `Cancelled`:

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

`feed` shows no attempts and no duration, because it did not run again: it kept its output of `run-1`, which `sub`
received as its input. The duration and the trace ID differ at each run. Without `--name`, the new run is named after
the source run, with `-r-` and four random hexadecimal digits.

The source run must be a run of the same Workflow that has ended and that `workflow.retention` has not removed yet,
and no step outside the steps that run again may have failed or been cancelled in it. When the image of a step that
runs again has changed since the source run, the replay fails, with the reason `DigestDrift`, unless `--allow-drift`
lets it run that step on the current image. The [`replay`](spec.md#workflowrunspecreplay) field of the new run
records its source.

Press Ctrl-C in the terminal that runs `funcdctl dev` to stop the first example.

## Branch on a step's output

By default, a step runs once each of its parents, the steps that it follows, has succeeded. A condition or a join
changes that:

- A step's [`when.condition`](spec.md#workflowspecstepswhen) is a JavaScript expression between `${{` and `}}` that
  yields `true` or `false`. It reads `input`, the run's input, and `step.<name>.output`, the output of a parent whose
  output has a schema: a parent that calls a Function, from the Function's contract, or that runs a child run, from
  the child Workflow's contract. A step whose condition is false is `Skipped`, and so are the steps after it that need
  it to succeed.
- A step's [`join`](spec.md#workflowspecsteps) sets how it waits for its parents. With `all`, the default, the step
  runs only when every parent has succeeded, and is `Skipped` otherwise. With `any`, it waits until every parent has
  ended, then runs on the outputs of those that succeeded, and is `Skipped` if none did. The object of its parents'
  outputs, which [Pass data between steps](#pass-data-between-steps) describes, then holds only those parents.

funcd checks each condition against the contracts of the steps before the Workflow becomes Ready. A condition that
reads a field that a parent's output does not have, uses a field as a value of another type, or reads the output of a
built-in step, which has no schema, leaves the Workflow `Pending`, with the reason `WhenTypeError`.

### TypeScript

The Workflow `orders`, in the example's `workflow.yaml`, takes one of two branches, depending on the score that its
step `score` computes. `hi` and `lo` both follow `score`: `hi` runs when the score is above 30, and `lo` when it is 30
or less. `report` follows both and sets `join: any`, so it receives an object with the key `hi` or the key `lo`, and
its handler, in
[`src/report/main.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/workflow/src/report/main.ts),
reads whichever is present:

::include[../../../../../_repos/funcd-typescript/examples/workflow/workflow.yaml]

On a platform, a run of `orders` with the amount 4 gets the score 40, so `hi` runs and `lo` is `Skipped`; with the
amount 2, `lo` runs and `hi` is `Skipped`. In both cases, `report`, `settle` and `summary` succeed.

## Wait or reshape data without a Function

The last two steps of `orders`, in the manifest above, are built-in steps, which run with no Function and no
worker:

- `settle` sets [`builtin.wait`](spec.md#workflowspecstepsbuiltin) to `2s`: the step waits two seconds, then succeeds
  with its input as its output. A wait can also be a `${{ }}` expression that yields a duration when the step runs.
  The wait counts toward the run's [`timeout`](spec.md#workflowspec), and a cancel ends it.
- `summary` sets [`builtin.pass`](spec.md#workflowspecstepsbuiltin) to a `${{ }}` expression whose value is the
  step's output. `{tier: step.settle.output.tier, settled: true}` keeps the field `tier` of the output of `settle` and
  adds the field `settled`. funcd checks a pass expression only when the step runs, so an error in it fails the step.

A built-in step calls no Function, so `describe` shows no attempts for it, and `funcdctl workflow logs` has no
records of its own for it.

## Pass data between steps

The steps of a run pass data to each other in their outputs:

- The first step receives the run's input. A step with one parent receives that parent's output, and a step with
  several parents receives an object of their outputs keyed by step name, as `report` does in
  [Branch on a step's output](#branch-on-a-steps-output).
- A step's [`params`](spec.md#workflowspecsteps), a JSON object, is merged over its input, and its keys win: a step
  that calls a Function gets the result as its input, and a step that runs a child Workflow passes it to the child
  run, so such a step can receive a fixed value that no parent returns. A wait step ignores `params`.
- A run's input and each step's output hold at most `workflow.payloadLimit` bytes, which
  [Limits and quotas](#limits-and-quotas) describes.

For larger data, or for data that several runs share, the steps use a store that the Workflow declares, as
[Share a database between steps](#share-a-database-between-steps) shows.

## Share a database between steps

A Workflow declares the stores that its steps use at the root of its spec:

- [`spec.kv`](spec.md#workflowspeckv), [`spec.buckets`](spec.md#workflowspecbuckets),
  [`spec.sql`](spec.md#workflowspecsql) and [`spec.lakes`](spec.md#workflowspeclakes) declare KV stores, Buckets, SQL
  databases and Lakes. [`spec.configMaps`](spec.md#workflowspecconfigmaps) and
  [`spec.secrets`](spec.md#workflowspecsecrets) declare the settings and the Secrets that the steps read.
- An entry with a `name` defines a store. Its owner, such as the `owner` of a KV table or of a SQL database, names a
  step, and the Function that funcd creates for that step is the owner, which can write the store; a SQL database's
  owner can also name a Lake that the Workflow declares. An
  entry with `ref` declares an existing store instead. A declaration gives no access by itself: a step reaches a
  store only through its own bindings.
- Each step binds what its code uses under
  [`function.bindings`](spec.md#workflowspecstepsfunctionbindings), and each store that a binding names must be
  declared at the root. A binding to a KV table, a Bucket prefix or a SQL database lets the step read it; only its
  owner and the principals that a role allows can write it, as the [KV](../../services/kv/index.md#access) page
  explains for a KV table.

The Python example sql-readings stores a batch of a sensor's values in a SQL database in one step, and summarizes
them in the next. It needs funcdctl and Git, and [uv](https://docs.astral.sh/uv/) on the `PATH`: uv installs the
Python packages that the steps import, such as SQLAlchemy, and downloads a Python interpreter when the computer has
none of version 3.12 or later. The dev build of funcdctl includes the SQL engine that `funcdctl dev` starts for each
SQL database, and deletes the database's data when it stops, unless it runs with `--persist`, as
[Run it locally](#run-it-locally) describes. The example's control plane uses the port 8081, as the first example
does, so stop the first example before it starts.

### Python

The Workflow `readings`, in `examples/sql-readings/workflow.yaml`, declares the SQL database `readings-db` and names
its step `ingest` as the database's `owner`, so the Function `readings-ingest` can write the database. Each step binds
the database with the alias `app`, which lets the step's code read it. The step `summarize` names `ingest` in
`dependsOn`, so it runs once `ingest` has succeeded:

::include[../../../../../_repos/funcd-python/examples/sql-readings/workflow.yaml]

Each step's handler receives its input in `event["data"]`. The handler of `ingest`, in `src/ingest/main.py`, creates
the table and its index when they do not exist, stores the values after the ones that the sensor already has, and
returns the sensor's name and the number of values that it
stored. `execute` and `rows`, from the example's package `readings_core`, compile each SQLAlchemy statement into SQL
text and send it through `context.sql`, which the [SQL](../../services/sql/index.md) page explains. The handler of
`summarize`, in
[`src/summarize/main.py`](https://github.com/pyvvo/funcd-python/blob/main/examples/sql-readings/src/summarize/main.py),
reads the values back and returns their count, mean, lowest and highest value, and the three latest values:

::include[../../../../../_repos/funcd-python/examples/sql-readings/src/ingest/main.py]

The folder's `pyproject.toml` names the entry of each step under `[tool.funcd-bundle.functions]`, such as
`src/ingest/main.py` for `ingest`. The folder's `run.yaml` holds a WorkflowRun, `readings-run-1`, with four values of
the sensor `greenhouse`. `funcdctl dev` applies it with the other files of the folder, so this run starts as soon as
`funcdctl dev` has started:

::include[../../../../../_repos/funcd-python/examples/sql-readings/run.yaml]

Clone the repository, go to the example's folder, and start the example with `uv run`:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/sql-readings
$ uv run funcdctl dev . --cport 8081
```

`uv run` installs the packages that the example's lock file, `uv.lock`, names into a virtual environment in the
folder, `.venv`. It then runs `funcdctl dev` with that environment's `python3` first on the `PATH`, and `funcdctl dev`
runs the steps with that `python3`.

In another terminal, read the run that `run.yaml` started, again until its phase reads `Succeeded`, `Failed` or
`Cancelled`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe readings-run-1
RUN readings-run-1   phase: Succeeded
  ingest   phase: Succeeded   attempts: 1   duration: 1s54ms
  summarize   phase: Succeeded   attempts: 1   duration: 230ms
trace: 293c42211458963a246f563da7f19a6d
full logs: funcdctl workflow logs readings-run-1
```

The first run after `funcdctl dev` has started can take a second or more, and the durations and the trace ID differ at
each run. `summarize` ran once `ingest` had succeeded, and read back the values that `ingest` stored. Another run of
`readings` for the same sensor adds its values after the ones that the database already holds: a run started with
`funcdctl workflow run` and the input of `run.yaml` stores the same four values a second time.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Retry a step and handle a failed run

A step that calls a Function fails when the Function answers with an error or does not answer in time:

- A 5xx answer, a timeout or a transport error is retried. A 4xx answer, such as the 422 of a call whose input does
  not match the step's contract, fails the step at once: `describe` shows the step `Failed`, with an error such as
  `workflow.dispatch: default/pipeline-feed rejected the step (422)` followed by the Function's answer.
- [`function.retry.maxAttempts`](spec.md#workflowspecstepsfunctionretry) sets how many calls the step may make, the
  first one included. A step that sets no `retry` makes `workflow.defaultRetry` calls, 1 by default, so it is not
  retried unless the server's configuration raises that number.
- `retry.backoff` sets the wait before the second call, which doubles at each later call, up to one hour. A step that
  sets no `backoff` waits `workflow.defaultRetryBackoff`, 0 seconds by default, so its calls follow each other at
  once.
- [`function.timeout`](spec.md#workflowspecstepsfunction) bounds each call, or `workflow.defaultStepTimeout`, 300
  seconds by default, when it is not set. A call that takes longer fails, and the step calls again while it has
  calls left.
- A failed step fails the run. The steps that depend on it are `Skipped`, and a step still running elsewhere in the
  run goes back to `Pending`. The run ends `Failed`, with the reason `StepFailed`.
- The Workflow's [`timeout`](spec.md#workflowspec) bounds a whole run, paused time excluded. A run that passes it
  ends `Failed`, with the reason `RunTimedOut`.
- [`onFailure`](spec.md#workflowspec) names a step that funcd calls once, without retry, when a run fails. Its input
  is an object with the fields `workflow`, `run`, `failedStep`, `reason` and `input`, the run's input. Its result never
  changes the run's phase, and a cancelled run does not call it.

funcd calls a step at least once, and sometimes more often: each retry calls the step's Function again, and a call
that a restart of funcd cuts runs again at its next attempt, even past `maxAttempts`. Each call carries its attempt
number in the `X-Funcd-Attempt` header. Write a step so that a second call with the same input does no harm.

## Pause, resume or cancel a run

[`funcdctl workflow pause`](../../tools/funcdctl/spec.md#funcdctl-workflow-pause) sets the run's
[`paused`](spec.md#workflowrunspec) field: no step or retry starts, the calls in flight finish, and the run's phase
becomes `Paused`. Paused time does not count toward the run's timeout.
[`funcdctl workflow resume`](../../tools/funcdctl/spec.md#funcdctl-workflow-resume) clears the field, and the run goes
on. A run whose manifest sets `paused: true` waits, in the phase `Paused`, until it is resumed.

[`funcdctl workflow cancel`](../../tools/funcdctl/spec.md#funcdctl-workflow-cancel) sets the run's `cancel` field:
nothing more starts, the calls in flight are closed and their late answers dropped, though a call may already have
done its work. The run ends `Cancelled`, without calling its `onFailure` step. A cancel wins over a pause, and a run
that has ended ignores both: `funcdctl workflow pause` still prints `paused <run>`, but the run keeps its phase.

## Start a run on a schedule

A Sensor starts runs from events. An action of a Sensor that names a
[`workflow`](../../services/events/spec.md#sensorspecdo) starts a run of that Workflow for each event, with the
action's input. With an EventSource of the kind `timer`, whose events fire at an
[`interval` or on a `cron` schedule](../../services/events/spec.md#eventsourcespectimerevents), the runs start on a
schedule. Each run is named after the Sensor and the action, with a random suffix. The action succeeds once the run
is created, and a run whose input does not match the Workflow's contract, or is larger than
`workflow.payloadLimit`, fails at its start. The [Events](../../../platform/services/events.md) page explains
EventSources and Sensors.

## Limits and quotas

- A run's input and each step's output hold at most `workflow.payloadLimit` bytes, 262144 (256 KiB) by default. A
  larger input is refused when the WorkflowRun is created, or fails the run with the reason `PayloadLimitExceeded`
  when a Sensor started it. A larger output fails its step, with no retry.
- At most `workflow.maxStepsInFlight` calls of steps, 64 by default, are in flight at once across all runs; a step
  waits for a free slot.
- A step's `timeout` lies between 0 seconds and 24 hours, and a run's `timeout` between 0 seconds and 168 hours. A
  step's `retry.maxAttempts` lies between 1 and 100, and its `retry.backoff` between 0 seconds and one hour.
- A child run can run its own child runs, at most 8 levels deep; a deeper chain fails the run, with the reason
  `SubworkflowDepthExceeded`.
- The Function of an image step is named `<workflow>-<step>`, which must fit in 63 characters; `funcdctl apply`
  refuses a Workflow whose name and step name are too long together.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each server setting above, and this
page's [Spec](spec.md) tab gives the range of each field.

## Data storage

funcd keeps the progress of each run, with the phase, the attempts and the output of each step, in its run state:
a store in the directory `workflow.dataDir`, `<storage.dataDir>/workflow` by default. With `storage.mode: memory`,
the run state lives in memory and is lost when funcd stops.

With the run state on disk, a run survives a restart of funcd. It stays `Running`; a call that the restart cut runs
again at its next attempt, a `wait` starts again from zero, and a child run starts again from its first step. funcd
deletes a run's state and its WorkflowRun once `workflow.retention`, 720 hours by default, has passed since the run
ended; a run that has not ended stays. The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes
these settings.

## Status

Read a Workflow's phase and its `Ready` condition with `funcdctl get workflow <name> -n <namespace> -o json`, and a
run's with `funcdctl workflow describe <run>`, as the first example does.
[`funcdctl workflow runs`](../../tools/funcdctl/spec.md#funcdctl-workflow-runs) lists the runs of a namespace, newest
first, and `--phase` keeps those of one phase.

Before a Workflow is Ready, funcd reads the contract of each step that calls a Function from its image and checks
every **edge**, the passage of a step's output into the input of the step that follows it: each field that a step
requires must come from its parents or its `params`, with the same type. It also checks each `when` condition. A
failed check sets the condition `SchemaMismatch` to `True`, with the reason of `Ready`, and the message names the step
and the field.

| Object and phase | Reason | Meaning |
| --- | --- | --- |
| Workflow `Ready` | `EdgesTypeChecked` | Every check passed, and runs can start. |
| Workflow `Pending` | `EdgeTypeMismatch` | An edge does not match: a step's required field is missing from its parents and `params`, or has another type. |
| Workflow `Pending` | `WhenTypeError` | A `when` condition reads a field that a parent's output does not have, uses a field as a value of another type, or reads a built-in step's output. |
| Workflow `Pending` | `WorkflowCycle` | The Workflow reaches itself through workflow steps. |
| Workflow `Pending` | `StoreNotOwned`, or `KVStoreNotOwned` for a KV store | A store that the Workflow defines by name belongs to another object. |
| Workflow `Pending` | Another reason | Such as while a step's image has not reached its registry: funcd checks it again within `workflow.artifactPollInterval`, 5 seconds by default. The message gives the cause. |
| Run `Pending` | `WorkflowNotFound` or `WorkflowNotReady` | The run waits to start, for its Workflow to exist or to become Ready. |
| Run `Running` | none | Its steps run. |
| Run `Paused` | none | The run is paused, as [Pause, resume or cancel a run](#pause-resume-or-cancel-a-run) explains. |
| Run `Succeeded` | none: `Ready` is `True` | Every step ended, and the run succeeded. |
| Run `Cancelled` | none | The run was cancelled. |
| Run `Failed` | `StepFailed` | A step failed, as [Retry a step and handle a failed run](#retry-a-step-and-handle-a-failed-run) explains. |
| Run `Failed` | `RunTimedOut` | The run passed the Workflow's `timeout`. |
| Run `Failed` | `InputSchemaMismatch` | The input does not match the Workflow's contract. |
| Run `Failed` | `PayloadLimitExceeded` | An input or an output is larger than `workflow.payloadLimit`. |
| Run `Failed` | `SubworkflowDepthExceeded` | Child runs nest more than 8 levels deep. |
| Run `Failed` | `RunRecordTooLarge` | The run's state is too large for the store of the run state. |
| Run `ReplaySeeded` condition | `False`, such as `DigestDrift` | A replay cannot start from its source run, as [Replay a run from a step](#replay-a-run-from-a-step) explains. |

A run has no phase until funcd starts it or makes it wait. Each step of a run is `Pending`, `Running`, `Succeeded`,
`Failed`, `Skipped` or `Cancelled`; a step is `Skipped` when its condition is false, its join cannot be met, or a step
that it depends on failed. `status.steps` of a WorkflowRun holds each step's attempts, start and end times, and, for a
failed step, the first line of the error, cut at 512 bytes. A run's status holds no step's output: a result that must
be read after the run goes to a store, as [Share a database between steps](#share-a-database-between-steps) shows.
`status.pins` names the **revision** of each Function that the run calls, the version of the Function's spec that
funcd runs, as [Changes and deletion](#changes-and-deletion) explains.

A Workflow's `status.contract` holds the input and output schemas that funcd derived for the runs, `status.steps` the
image and the contract of each step that calls a Function or runs a child run, and `status.runs` the names of the
runs that have not ended, with a count of the runs that ended in each final phase.

## Changes and deletion

A run pins its Workflow's spec, its child Workflows and the current revision of each Function that it calls when it
starts. An edit to the Workflow or to a Function does not change a run that has started: the run keeps the code that
it pinned, even of a step that the edit removed, and a new run takes the new spec and revisions.

A WorkflowRun's `workflow`, `input` and `replay` are fixed when the run is created, and funcd refuses an apply that
changes them: a new input takes a new run, under a new name. Deleting a run that has not ended cancels it.

Deleting a Workflow cancels each of its runs that has started; a run that has not started stays `Pending`. funcd
deletes the Functions that it created for the image steps, and the ConfigMaps and Lakes that the Workflow defines by
name. A KV store, a Bucket or a SQL database that the Workflow defines by name stays, with its data, unless its
`deletion` is `delete`.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `image "<image>" is digest-pinned and has no :tag naming a build`, or `has no :tag naming a build` | Give the step's `image` a tag that names its build, as [Run it locally](#run-it-locally) describes. |

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for a Workflow:

- It runs the Function of each image step from the build that the image's tag names, so an image with a digest or with
  no tag stops `funcdctl dev`. For Node.js, the build is `<tag>.mjs`, in the folder or in its `dist` folder, with its
  contract file beside it. For Python, it is the entry that the `[tool.funcd-bundle.functions]` table of the folder's
  `pyproject.toml` names for the tag. Without that table, it is `src/main.py` when the folder has the tag's name, else
  `<tag>.py`; the contract file is at the folder's root.
- It applies every resource file at the top level of the folder, so a WorkflowRun that such a file holds, such as
  `run.yaml` in [Share a database between steps](#share-a-database-between-steps), starts with `funcdctl dev`.
- It runs each step's Function in a worker of its own, even when `pooling.mode` is `shared`.
- It keeps one worker running for each Function of the folder, but not for the Function of a step: a step's Function
  follows the Workflow's `pooling`, so with `minReplicas` 0, the default, it stops its worker after about 5 minutes
  without a call, and the next call waits for a new worker to start.
- When a step's build or contract file changes, it copies the build to a new path and applies the Workflow again,
  within about a second, so the step's Function takes a new revision. A run that has started keeps the revision that
  it pinned. A step added after `funcdctl dev` started does not run: `funcdctl dev` logs
  `restart funcdctl dev to run a Workflow step added after it started`.

`funcdctl dev` keeps its state in memory, where it is lost when `funcdctl dev` stops. With the `--persist` flag, it
keeps the objects that it applied and the data of the stores between runs in `.funcd-dev`, or in the folder that
`--persist-to` names, as the [Function](../function/index.md#run-it-locally) page describes; the run state stays in
memory even then. To start again from nothing, delete that persist folder after `funcdctl dev` stops.

## Examples

- [workflow in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/workflow): the Workflow
  `pipeline`, which runs `scorer` as a child run, and `orders`, which branches on a step's output and ends with a
  wait and a pass.
- [sql-readings in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/sql-readings): a Workflow whose
  steps share a SQL database.
