---
title: Events
description: Call a Function or start a Workflow run when something happens, on a schedule, on a new object in a Bucket or on a row change in a SQL database, and handle the deliveries that fail.
---

## Usage

An [**EventSource**](../../../platform/services/events.md) turns something that happens into **events**: a timer, a
new object in a Bucket, or a row change in a SQL database. A **Sensor** subscribes to events and runs an action for
each one: it calls a Function, or starts a Workflow run.

The first example calls the Function `log-burst` every 10 seconds. Each call leaves an
[**Invocation**](../../workloads/function/spec.md#invocation), funcd's record of the action.

Install the [dev build of funcdctl](../../../get-started/install.md) and Git, and put Node.js, or Python 3.12 or later
as `python3`, on the `PATH`. Save this manifest as `ticker.yaml` in the example's folder, which `funcdctl dev` applies
in the [namespace](../../../platform/tenancy-and-limits/namespaces.md) `default`:

::include[../../../../../api/types/v1alpha1/examples/events/sensor.yaml#ticker]

The first tick comes 10 seconds after the EventSource's creation. An action calls one `function` or starts one
`workflow`, never both. The Function receives each event as a **CloudEvent** whose `data`, `{}` for a timer, funcd
checks against the Function's [contract](../../workloads/function/index.md#usage). An Invocation is `Ready` once the
Function answers without an error, or `Failed` once the delivery ends as a
[dead letter](#handle-a-delivery-that-fails), and never changes after.

### TypeScript

Clone the repository:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
```

Save the manifest as `examples/log-burst/ticker.yaml` and start the example:

```console
$ funcdctl dev examples/log-burst --cport 8081
```

About 10 seconds later, list the Invocations from another terminal until one appears:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocations -n default
NAME
inv-fc98965dbd0db0765f25
```

Its `status.phase` is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocation inv-fc98965dbd0db0765f25 -n default -o json
```

The call's `ERROR` records appear after a moment; times and order vary:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token logs log-burst -n default --severity error
2026-11-13T18:08:00.944Z [ERROR] 0 item failed
2026-11-13T18:08:00.944Z [ERROR] 0 burst stderr default
2026-11-13T18:08:00.945Z [ERROR] 0 item failed
2026-11-13T18:08:00.945Z [ERROR] 0 item failed
2026-11-13T18:08:00.946Z [ERROR] 0 item failed
```

Press Ctrl-C in the first terminal to stop.

### Python

Clone the repository:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
```

Save the manifest as `examples/log-burst/ticker.yaml` and start the example:

```console
$ funcdctl dev examples/log-burst --cport 8081
```

About 10 seconds later, list the Invocations from another terminal until one appears:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocations -n default
NAME
inv-440f82136f11ea321a07
```

Its `status.phase` is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocation inv-440f82136f11ea321a07 -n default -o json
```

The call's `ERROR` records appear after a moment; times vary:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token logs log-burst -n default --severity error
2026-11-13T18:08:30.267Z [ERROR] 0 burst stderr
2026-11-13T18:08:30.269Z [ERROR] 0 item 0 failed validation
2026-11-13T18:08:30.269Z [ERROR] 0 item 1 failed validation
2026-11-13T18:08:30.269Z [ERROR] 0 item 2 failed validation
```

Press Ctrl-C in the first terminal to stop.

## Run on a schedule

A [`timer`](spec.md#eventsourcespectimer) event sets one schedule:

- `interval`, from 100 milliseconds to 24 hours, fires at each whole interval since the EventSource's creation, so its
  times survive a restart.
- `cron`, five fields or a macro such as `@daily`, is read in the IANA time zone of `timeZone`, or in UTC.

::include[../../../../../api/types/v1alpha1/examples/events/eventsource.yaml#eventsource]

A timer does not catch up a tick missed while funcd was stopped, and carries no data: pass values through the
[action's input](#shape-the-input-of-an-action). A Sensor can also
[start a Workflow run on a schedule](../../workloads/workflow/index.md#start-a-run-on-a-schedule).

## React to a new object in a Bucket

A [`blob`](spec.md#eventsourcespecblob) event fires once for each new object under its `prefix` in a
[Bucket](../bucket/index.md) of the namespace, and again when an object is written with another size or modification
time; a deletion fires nothing. funcd lists each prefix every
[`eventing.blobPollInterval`](../../running-funcd/spec.md#funcdconfigyaml), 15 seconds by default, so an event can lag
that long. The data holds `bucket`, the full `key`, `size`, `version` and `time`. funcd records the keys that each event
has fired for; when the EventSource is created or a prefix changes, the objects already there fire too, but only to the
Sensors subscribed at that moment.

### TypeScript

From the funcd-typescript repository, start the s3-roundtrip example; the steps also need curl:

```console
$ funcdctl dev examples/s3-roundtrip --gport 8080 --cport 8081
```

In another terminal, write an object, then list the runs until one appears, about a second later:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow runs
NAME
gold-ingest-run-7634b281
```

Read the run until its phase is `Succeeded`, `Failed` or `Cancelled`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe gold-ingest-run-7634b281
RUN gold-ingest-run-7634b281   phase: Succeeded
  echo   phase: Succeeded   duration: 0s
trace: bb7489fb793dbffa27e039f4b2cf3408
full logs: funcdctl workflow logs gold-ingest-run-7634b281
```

The run's input holds the object's key:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get workflowrun gold-ingest-run-7634b281 -n default -o json
```

Each new write, the same tag again included, starts another run. Press Ctrl-C in the first terminal to stop.

The EventSource, in `drop-source.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/drop-source.yaml]

The Sensor, in `drop-sensor.yaml`, starts the Workflow
[`ingest`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/s3-roundtrip/ingest-workflow.yaml), whose one
built-in step returns its input:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/drop-sensor.yaml]

A `workflow` action succeeds once funcd creates the run; a later failure shows in the run's phase, not as a dead letter.

## React to a row change in a SQL database

A [`sql`](spec.md#eventsourcespecsql) event fires on the changes that `on` lists, `Insert`, `Update` or `Delete`, all
three by default, to one `table` of a SQLDatabase of the namespace, matched exactly. The database's engine reports them
to funcd: its **change feed**. The data holds `op`, `table`, `rowId`, `index`, the commit number that a commit's
changes share, and `before`, `after` and `commitTimestamp` when the change carries them. Past 256 KiB together, funcd
drops `before` and `after` and sets `truncated` to `true`. The [SQL](../sql/index.md#python) page runs this Python
example.

### Python

The EventSource and the Sensor, in `examples/sql-orders/changes.yaml`:

::include[../../../../../_repos/funcd-python/examples/sql-orders/changes.yaml]

The handler of `audit`:

::include[../../../../../_repos/funcd-python/examples/sql-orders/src/audit/main.py]

## Shape the input of an action

An action without an [`input`](spec.md#sensorspecdo) passes the event's data as it is. An `input` replaces it with a
JSON object whose top-level values are literals, or strings that hold one expression between `${{` and `}}` reading
`event.data` and its paths, `event.id`, `event.source`, `event.type` or `event.time`.

funcd checks each input when the Sensor is applied: an `input` that is not a JSON object, or an expression that does
not parse or reads a field that no event has, leaves the Sensor not Ready with the reason `InvalidInput`, and none of
its actions runs. The input becomes the CloudEvent's `data` for a `function` action, checked against the Function's
contract, and the run's input for a `workflow` action, checked when the run starts; a run whose input does not match,
or exceeds `workflow.payloadLimit` bytes, fails at its start.

## Read the event in the handler

The handler receives the event as its second argument, a CloudEvent with `specversion`, `id`, `source`
(`funcd://<namespace>/eventsource/<name>`), `type`, the event's name, `time`, `datacontenttype`, `data`, the action's
input, and for a `sql` event `subject`, the table.

A timer or blob event's `id` is random. A SQL event's `id` names its change, so each change has its own. A retried
delivery and a replayed dead letter keep the same `id`. A handler can receive an event more than once, since an
attempt counted as failed may still have done its work: make a repeat harmless, for example by recording each handled
`id`.

## Handle a delivery that fails

A delivery that fails, such as when the Function answers with an error, is retried, 3 attempts by default, then kept
as a **dead letter**, and its Invocation is `Failed`.
[`funcdctl eventing dlq`](../../tools/funcdctl/spec.md#funcdctl-eventing-dlq) lists, describes, replays or discards
dead letters.

To see one, stop any running example, and replace `examples/log-burst/ticker.yaml` with this manifest, whose input the
contract refuses:

::include[../../../../../api/types/v1alpha1/examples/events/dead-letter.yaml#dead-letter]

Start the example:

```console
$ funcdctl dev examples/log-burst --cport 8081
```

The first tick fails. In another terminal, list the dead letters until one appears. In TypeScript:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token eventing dlq list
01M7B1JTZ6P6WHF86MT0JFKYJV	burst-on-tick	ticker/tick	action=burst	attempts=3	reason=sensor: function default/log-burst returned status 422: {"error":"event data does not match the input contract","details":[{"path":"/items","message":"got string, want number"}]}
```

In Python, the reason reads `additional properties 'items' not allowed`. A dead letter keeps the event, not the input
built from it.

A replay delivers a dead letter once, through the Sensor as it is now. Restore the first manifest in `ticker.yaml`, and
wait until the Sensor's action has no `input`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get sensor burst-on-tick -n default -o json
```

Then replay the dead letter:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token eventing dlq replay 01M7B1JTZ6P6WHF86MT0JFKYJV
replayed 01M7B1JTZ6P6WHF86MT0JFKYJV (delivered; entry removed)
```

A replay records its own Invocation. When it fails, funcd keeps the dead letter, its attempts set back to 0, and the
command fails with `delivery failed (entry re-parked)`. `discard` deletes a dead letter without delivering it.

## Limits and quotas

- funcd attempts each delivery [`eventing.deliveryAttempts`](../../running-funcd/spec.md#funcdconfigyaml) times, 3 by
  default, waiting `eventing.deliveryBackoffInitial`, 100 milliseconds by default, then twice as long each time, up to
  `eventing.deliveryBackoffMax`, by default the larger of 10 seconds and `eventing.deliveryBackoffInitial`.
- An attempt waits up to `invoke.activationTimeout`, 30 seconds by default, for a worker, then up to 30 seconds for
  the answer.
- At most `eventing.maxDeliveriesInFlight` attempts run at once, 32 by default, and `eventing.maxInFlightPerTarget`
  per Function or Workflow, 4 by default, so a slow target holds only its own share.
- A Sensor holds at most `eventing.maxQueuedPerSensor` deliveries, 4096 by default; past that, a delivery becomes a dead
  letter at once, with a reason that contains `delivery queue of sensor <namespace>/<name> is full`.
- funcd keeps a dead letter for `eventing.deadletter.retention`, 720 hours by default, and at most
  `eventing.deadletter.maxEntries` per namespace, 1000 by default; an hourly sweep deletes the rest, oldest first.

## Data storage

funcd keeps no event except a dead letter's: an event that no Sensor subscribes to is lost. A delivery waits in memory
until its action ends. At shutdown, funcd keeps each waiting delivery as a dead letter, with a reason that contains
`daemon shut down with the delivery still queued`, for up to `server.shutdownTimeout`, 15 seconds by default; the rest
are dropped and counted in the log. A crash loses them.

The dead letters, the keys that blob events have fired for and each change feed's position live in the event store,
`<storage.dataDir>/eventstore` by default, and survive a restart; with `storage.mode: memory`, they are lost at stop.
Invocations are kept with funcd's other objects, and no sweep removes them.

## Status

`funcdctl get eventsource <name> -n <namespace> -o json` and `funcdctl get sensor <name> -n <namespace> -o json` print
the phase and the `Ready` condition; with `funcdctl dev`, add the first example's `--server` and `--token` flags.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| Timer EventSource `Ready` | | funcd has registered its events. |
| Blob or sql EventSource `Ready` | `Watching` | Its Bucket or its SQLDatabase exists, and funcd watches it. |
| Blob or sql EventSource `Pending` | `BucketNotFound` or `DatabaseNotFound` | Its Bucket or its SQLDatabase does not exist, and it watches nothing. funcd checks again every `eventing.bucketRecheckInterval`, 15 seconds by default. |
| Blob EventSource `SeenListSaved` condition `False` | `SaveFailed` | funcd cannot save the keys that one of its events has fired for. The message names each such event. |
| Sensor `Ready` | `Bound` | The Sensor has subscribed to its events. |
| Sensor `Pending` | `InvalidInput` | The input of an action is not valid, as [Shape the input of an action](#shape-the-input-of-an-action) explains, and the Sensor subscribes to nothing. The message names the action and the field, such as `action "burst" field "file": expr.check: unknown field "nope" under "event" (position 1)` for `${{ event.nope }}`. |
| Sensor `Pending` | `TargetServesHTTP` | An action names a Function whose `protocol` is `http`, which only funcd's [edge](../../../platform/security-and-networking/networking.md), the part of funcd that receives the calls from outside funcd, calls. The Sensor stays subscribed, and each event of that action ends as a dead letter. |
| Invocation `Ready` | | The action ended without an error. |
| Invocation `Failed` | | The delivery ended as a dead letter: after its attempts, or at once when the retry queue refused it. The Invocation's `error` holds the reason, as the dead letter does. |
| Any other reason | | The message of the `Ready` condition gives the cause. |

## Changes and deletion

- A changed EventSource takes effect at once. A timer event with an unchanged schedule keeps its times, and a new or
  changed `cron` first fires at its next time. A blob event with a new `prefix` fires for the objects already there.
- Deleting an EventSource stops its events and forgets its blob keys; its Sensors stay and receive nothing more.
- A changed Sensor applies to the next event. A waiting delivery whose action changed becomes a dead letter at once,
  with a reason that contains
  `sensor <namespace>/<name> changed with the delivery still queued; replay sends it to the current spec`, and one in
  flight is not attempted again. Deleting a Sensor does the same for all its deliveries; replaying its dead letters
  then fails.
- Under `funcdctl dev`, deleting a file deletes its EventSource or Sensor.

## CLI errors

`funcdctl dev` stops before it serves the first example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. With Docker or nerdctl, `funcdctl dev --containers` needs no Node.js. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. With Docker or nerdctl, `funcdctl dev --containers` needs no Python. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. With Docker or nerdctl, `funcdctl dev --containers` needs no Python. |

## Run it locally

`funcdctl dev` differs from a server:

- It lists blob prefixes every second, or at `dev.platform.eventing.blobPollInterval` in
  [`funcdctl.config.yaml`](../../tools/funcdctl/spec.md#funcdctl-dev).
- It applies EventSources and Sensors after the Functions and Workflows, applies a saved change within about a
  second, and gives the resource group `dev` to an object that names none; a server needs `metadata.resourceGroup`.
- It keeps the event store in memory, even with `--persist`, which keeps only its other state, in `.funcd-dev`.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/log-burst): the Function of the first example.
- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): a Workflow
  run for each new object.
- [sql-orders in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/sql-orders): a Function called with
  each change of a table.
