---
title: Events
description: Call a Function or start a Workflow run when something happens, on a schedule, on a new object in a Bucket or on a row change in a SQL database, and handle the deliveries that fail.
---

## Usage

An **EventSource** turns something that happens into **events**: a timer that fires on a schedule, a new object in a
Bucket, or a row change in a SQL database. A **Sensor** subscribes to some of these events and runs an action for each
one: it calls a Function, or it starts a run of a Workflow. See [how funcd carries events](../../../platform/services/events.md)
for the concept.

The first example calls a Function every 10 seconds: the EventSource `ticker` fires its event `tick` every 10 seconds,
and the Sensor `burst-on-tick` calls the Function `log-burst`, the first example of the
[Function](../../workloads/function/index.md) page, which writes a burst of log lines at each call. Each tick leaves
an **Invocation**, the record that funcd keeps each time a Sensor runs an action, and the lines that `log-burst` wrote.

Install the [dev build of funcdctl](../../../get-started/install.md) and Git, and put the runtime of the example's
language on the `PATH`: Node.js for TypeScript, or Python 3.12 or later as `python3`.

`funcdctl dev` starts a local platform and applies every resource file at the top level of one folder, here
`examples/log-burst` of the language repository, which declares only the Function `log-burst`. A `funcdctl` command
reaches the local control plane with `--server` and the token `funcd-dev-token`; the `--cport` flag sets its port, so
the commands use the port 8081. `funcdctl dev` runs the
[namespace](../../../platform/tenancy-and-limits/namespaces.md) `default` only, which the manifest names.

The steps save this manifest, which holds the EventSource and the Sensor, into the folder as `ticker.yaml`:

::include[../../../../../api/types/v1alpha1/examples/events/sensor.yaml#ticker]

- The EventSource `ticker` has a [`timer`](spec.md#eventsourcespectimer) source with one event, `tick`, which fires
  once per [`interval`](spec.md#eventsourcespectimerevents), counted from the moment funcd created the EventSource, so
  the first tick comes 10 seconds after it.
- The Sensor `burst-on-tick` subscribes to that event under [`on`](spec.md#sensorspecon). Each entry of `on` is a
  **dependency**: a name, here `tick`, for the event that `event` names in the EventSource that `source` names.
- Under [`do`](spec.md#sensorspecdo), the action `burst` runs for each event of the dependency that its `on` names,
  and calls the Function that `function` names; an action names either a `function` or a `workflow`, never both.
  funcd starts a [worker](../../../platform/workloads/index.md#runtime) of the Function, a process that runs its code,
  first when none runs.

The Function receives each event as a **CloudEvent**, the envelope that the CloudEvents specification defines, whose
`data` holds the event's data: for a timer, an empty object, `{}`. funcd checks that data against the Function's
[contract](../../workloads/function/index.md#usage), the JSON Schema of the input that the Function accepts, as for a
call from outside funcd; `log-burst` accepts it, since every field of its input is optional.
[Read the event in the handler](#read-the-event-in-the-handler) describes the CloudEvent. An Invocation is `Ready`
once the Function has answered without an error, and `Failed` once the delivery ends as a dead letter, as
[Handle a delivery that fails](#handle-a-delivery-that-fails) explains. The [Spec](spec.md) tab describes every field
of an EventSource and of a Sensor, and the Function's Spec tab describes the
[Invocation](../../workloads/function/spec.md#invocation).

### TypeScript

Clone the repository:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
```

Save the manifest above as `examples/log-burst/ticker.yaml`, beside the Function's manifest, `burst.yaml`. Then start
the example from the repository's root folder:

```console
$ funcdctl dev examples/log-burst --cport 8081
```

`funcdctl dev` prints its banner once it has applied the files of the folder, and the first tick comes about 10
seconds later. Wait 10 seconds, then, in another terminal, list the Invocations of the namespace `default`, again until
the list holds a name:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocations -n default
NAME
inv-fc98965dbd0db0765f25
```

The names are random, so they differ at each run, and the list, sorted by name, grows by one at each tick. A name in
the list tells that an action has ended, not how: read the Invocation, with a name that the list printed, and check its
phase:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocation inv-fc98965dbd0db0765f25 -n default -o json
```

The command prints the Invocation as JSON. Under `status`, `"phase"` is `"Ready"`, since `log-burst` answered the call,
and `startTime` and `endTime` give when the action started and ended; the `startTime` of the next tick's Invocation
comes 10 seconds later. funcd writes an Invocation only once its action has ended, so its phase does not change
afterwards.

To check that each tick reached `log-burst`, read the records of the severity `ERROR` and above that the Function
wrote. Each call writes five of them: the line written to standard error, and four errors. A record appears after
funcd has written it to storage, so repeat the command until the records appear. The times, and the order of the lines that one call writes, differ at each run:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token logs log-burst -n default --severity error
2026-11-13T18:08:00.944Z [ERROR] 0 item failed
2026-11-13T18:08:00.944Z [ERROR] 0 burst stderr default
2026-11-13T18:08:00.945Z [ERROR] 0 item failed
2026-11-13T18:08:00.945Z [ERROR] 0 item failed
2026-11-13T18:08:00.946Z [ERROR] 0 item failed
```

The terminal that runs `funcdctl dev` prints every line of each call too. To stop the example, press Ctrl-C in that
terminal.

### Python

Clone the repository:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
```

Save the manifest above as `examples/log-burst/ticker.yaml`, beside the Function's manifest, `handler.yaml`. Then
start the example from the repository's root folder:

```console
$ funcdctl dev examples/log-burst --cport 8081
```

`funcdctl dev` prints its banner once it has applied the files of the folder, and the first tick comes about 10
seconds later. Wait 10 seconds, then, in another terminal, list the Invocations of the namespace `default`, again until
the list holds a name:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocations -n default
NAME
inv-440f82136f11ea321a07
```

The names are random, so they differ at each run, and the list, sorted by name, grows by one at each tick. A name in
the list tells that an action has ended, not how: read the Invocation, with a name that the list printed, and check its
phase:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get invocation inv-440f82136f11ea321a07 -n default -o json
```

The command prints the Invocation as JSON. Under `status`, `"phase"` is `"Ready"`, since `log-burst` answered the call,
and `startTime` and `endTime` give when the action started and ended; the `startTime` of the next tick's Invocation
comes 10 seconds later. funcd writes an Invocation only once its action has ended, so its phase does not change
afterwards.

To check that each tick reached `log-burst`, read the records of the severity `ERROR` and above that the Function
wrote. Each call writes four of them: the line written to standard error, and three errors. A record appears after
funcd has written it to storage, so repeat the command until the records appear. The times differ at each run:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token logs log-burst -n default --severity error
2026-11-13T18:08:30.267Z [ERROR] 0 burst stderr
2026-11-13T18:08:30.269Z [ERROR] 0 item 0 failed validation
2026-11-13T18:08:30.269Z [ERROR] 0 item 1 failed validation
2026-11-13T18:08:30.269Z [ERROR] 0 item 2 failed validation
```

The terminal that runs `funcdctl dev` prints every line of each call too. To stop the example, press Ctrl-C in that
terminal.

## Run on a schedule

A [`timer`](spec.md#eventsourcespectimer) source fires each of its events on a schedule of its own, which one of two
fields of the [event](spec.md#eventsourcespectimerevents) sets:

- `interval`, a duration from 100 milliseconds to 24 hours, such as `10s` or `1h30m`. The event fires each time a
  whole number of intervals has passed since funcd created the EventSource, so its times stay the same across a
  restart of funcd.
- `cron`, a cron expression of five fields, the minute, the hour, the day of the month, the month and the day of the
  week, or a macro such as `@daily` or `@hourly`. funcd reads it in the IANA time zone that `timeZone` names, such as
  `Europe/Paris`, and in UTC when `timeZone` is empty.

An event sets exactly one of `interval` and `cron`, and sets `timeZone` only with `cron`. This EventSource fires its
event `every-minute` once a minute, and its event `nightly` at 02:30 in Paris:

::include[../../../../../api/types/v1alpha1/examples/events/eventsource.yaml#eventsource]

A timer does not catch up: a tick that falls due while funcd is stopped is skipped, and the event fires next at the
next whole interval or the next time that its cron expression names. A timer's event carries no data of its own, so a
Sensor that needs some passes it in the [input of its action](#shape-the-input-of-an-action). The Workflow page explains
how a Sensor [starts a Workflow run on a schedule](../../workloads/workflow/index.md#start-a-run-on-a-schedule).

## React to a new object in a Bucket

A [`blob`](spec.md#eventsourcespecblob) source watches the objects of a [Bucket](../bucket/index.md) of its namespace.
Each of its [events](spec.md#eventsourcespecblobevents) watches the objects whose key starts with its `prefix`, or every
object of the Bucket when the prefix is empty. The event fires once for each new object, and once more each time an
object is written again with another size or another modification time. `on` lists the changes that fire the event:
`Created`, the only one, covers both cases, and an empty `on` means `Created`. A deleted object fires nothing.

funcd finds the new objects by listing each prefix every `eventing.blobPollInterval`, 15 seconds by default, so an
event fires up to that long after the write; `funcdctl dev` lists them every second. The event's data describes the
object: `bucket`, the Bucket's name; `key`, the object's full key, prefix included; `size`, in bytes; `version`, which
changes each time the object is written again; and `time`, the object's modification time. funcd keeps the keys that
each event has fired for, so an object fires once for each version. When the EventSource is created, or the prefix of
an event changes, the objects already under the prefix fire too, but only to the Sensors subscribed at that moment: an
object that no Sensor received still counts as fired, and a Sensor applied later does not receive it.

The example s3-roundtrip, whose Function the [Bucket](../bucket/index.md) page calls, also holds an EventSource and a
Sensor, which start a Workflow run for each object written under the prefix `gold`. The example exists in TypeScript
only.

### TypeScript

The EventSource `gold-drops`, in `examples/s3-roundtrip/drop-source.yaml`, fires its event `arrived` for each object
under the prefix `gold/` of the Bucket `lakehouse`:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/drop-source.yaml]

The Sensor `gold-ingest`, in `drop-sensor.yaml`, starts a run of the Workflow `ingest` for each such event. The
[`input`](spec.md#sensorspecdo) of its action sets the run's input to an object with one field, `file`, whose value the
expression `${{ event.data.key }}` reads from the event: the key of the object. [Shape the input of an
action](#shape-the-input-of-an-action) explains such expressions. The Workflow `ingest`, in
[`ingest-workflow.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/s3-roundtrip/ingest-workflow.yaml),
has a single built-in step, `echo`, which ends with its input as its output; the
[Workflow](../../workloads/workflow/index.md) page explains runs and built-in steps.

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/drop-sensor.yaml]

From the root folder of the funcd-typescript repository, cloned as in the first example, start the example.
The steps also need curl, which calls the Function `s3-roundtrip`:

```console
$ funcdctl dev examples/s3-roundtrip --gport 8080 --cport 8081
```

In another terminal, call the Function `s3-roundtrip` with the tag `001`, which makes it write the object
`gold/roundtrip-001.txt`. Then list the runs of the namespace, again until the list holds a run of `gold-ingest`, which
takes about a second:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow runs
NAME
gold-ingest-run-7634b281
```

The run is named after the Sensor and its action, with a random suffix that differs at each run. A name in the list
does not tell whether the run has ended: read the run's phase, with the name that the list printed, again until it
reads `Succeeded`, `Failed` or `Cancelled`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token workflow describe gold-ingest-run-7634b281
RUN gold-ingest-run-7634b281   phase: Succeeded
  echo   phase: Succeeded   duration: 0s
trace: bb7489fb793dbffa27e039f4b2cf3408
full logs: funcdctl workflow logs gold-ingest-run-7634b281
```

The trace ID differs at each run. To check that the run received the key of the object, print the run as JSON:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get workflowrun gold-ingest-run-7634b281 -n default -o json
```

Under `spec`, `"input"` is `{"file": "gold/roundtrip-001.txt"}`, the key that the event carried. A call with the tag
`002` writes another object and starts another run. A call with the tag `001` again writes `gold/roundtrip-001.txt`
anew, with a new modification time, and starts another run too. To stop the example, press Ctrl-C in the terminal that
runs `funcdctl dev`.

The action succeeds once funcd has created the run: what the run does afterwards, a failure included, shows in the
run's own phase, and leaves no dead letter, which [Handle a delivery that fails](#handle-a-delivery-that-fails)
describes.

## React to a row change in a SQL database

A [`sql`](spec.md#eventsourcespecsql) source fires on the row changes of a SQLDatabase of its namespace. The database's
engine reports each change to funcd, and that stream of changes is the database's **change feed**. Each of its
[events](spec.md#eventsourcespecsqlevents) fires on the changes of one `table`, matched by its exact name, case
included, that its `on` lists: `Insert`, `Update` or `Delete`, and all three when `on` is empty. The event's data holds
`op`, the change; `table`; `rowId`, the identifier of the row; and `index`, the number of the commit that made the
change, which all the changes of one commit share. It also holds `before` and `after`, the row before and after the
change, and `commitTimestamp`, when the change carries them. When `before` and `after` together exceed 256 KiB, funcd
leaves both out and sets `truncated` to `true`.

The [SQL](../sql/index.md#python) page starts the Python example sql-orders, which uses such a source: each order that
the example places reaches its Function `audit`, which records the change in another table.

### Python

In `examples/sql-orders/changes.yaml`, the EventSource `orders-changes` fires its event `order-changed` on every change
of the table `orders` of the database `orders-db`, and the Sensor `orders-audit` calls the Function `audit` with each
one:

::include[../../../../../_repos/funcd-python/examples/sql-orders/changes.yaml]

The handler of `audit`, in `src/audit/main.py`, reads the change from the event's `data`: it takes the row from
`after`, or from `before` for a deleted row, and inserts the change's `op`, its `rowId` and its `index`, with the
order's status, into the table `order_events`:

::include[../../../../../_repos/funcd-python/examples/sql-orders/src/audit/main.py]

## Shape the input of an action

An action without an [`input`](spec.md#sensorspecdo) passes the event's data on as it is: the first example's Function
receives the timer's empty object. An `input` replaces that data with a JSON object of its own, whose top-level values
are either literals, passed as they are, or strings that hold a single expression between `${{` and `}}`. An
expression reads the event, as `event.data` and the paths under it, `event.id`, `event.source`, `event.type` or
`event.time`. The Sensor of [React to a new object in a Bucket](#react-to-a-new-object-in-a-bucket) sets
`file: "${{ event.data.key }}"`, so each run receives the key of its object.

funcd checks the input of each action once the Sensor is applied, before any event comes. An `input` that is not a
JSON object, or an expression that does not parse or reads a field that no event has, makes the Sensor not Ready, with
the reason `InvalidInput`, and none of its actions runs until the Sensor is fixed, as [Status](#status) shows.

The input becomes:

- for a [`function`](spec.md#sensorspecdo) action, the `data` of the CloudEvent that the Function receives, which
  funcd checks against the Function's contract, as for any call;
- for a [`workflow`](spec.md#sensorspecdo) action, the input of the WorkflowRun, which funcd checks against the
  Workflow's contract when the run starts. A run whose input does not match it, or holds more than
  `workflow.payloadLimit` bytes, fails at its start.

## Read the event in the handler

A Function that a Sensor calls receives the event as its handler's second argument, as a CloudEvent with these fields:
`specversion`, `1.0`; `id`, the identifier of the event; `source`, the EventSource as
`funcd://<namespace>/eventsource/<name>`; `type`, the name of the event; `time`, when the event fired;
`datacontenttype`, `application/json`; `data`, the action's input; and, for an event of a `sql` source, `subject`, the
table's name. In TypeScript, the handler reads them as `event.data` or `event.id`, and in Python as `event["data"]` or
`event["id"]`.

The `id` of a timer or blob event is a random value, new at each firing. The `id` of a SQL event names the change
that it reports: funcd builds it from the namespace and the database, the commit and the position of the change in that
commit, and the event, so each change has an `id` of its own. A delivery that funcd attempts again, and a dead letter
that it replays, carry the same CloudEvent, `id` included.

A handler may receive the same event more than once: funcd attempts a delivery again after any failure, and an attempt
that funcd counted as failed, such as one whose answer came too late, may still have done its work. Write the handler
so that a second call with the same event does no harm, for example by recording the `id` of each event that it has
handled.

## Handle a delivery that fails

A delivery fails when funcd cannot build the action's input from the event, or while the platform or the namespace is
held, as the [Disaster recovery](../../../platform/disaster-recovery.md) page explains. A delivery to a Function also
fails when the Function answers with an HTTP status of 400 or above, such as 422 for data that its contract refuses, or
when funcd cannot call the Function, for example because no worker became ready or no answer came in time. funcd then
attempts it again, after 100 milliseconds, then after twice as long at each attempt, up to `eventing.deliveryAttempts`
attempts in all, 3 by default. When the last attempt fails, funcd keeps the delivery as a **dead letter**, with the
CloudEvent, the Sensor, the action, the number of attempts and the reason, and records the action's Invocation as
`Failed`, with the reason as its `error`. Nothing delivers a dead letter again by itself:
[`funcdctl eventing dlq`](../../tools/funcdctl/spec.md#funcdctl-eventing-dlq) lists, describes, replays or discards the
dead letters of a namespace. Its `list` and `describe` need the right to read the Sensors of the namespace, and
`replay` and `discard` the right to delete them, since a dead letter is a failed action of a Sensor.

To see one, run the first example again, in either language, with an action that fails. The first example and the
one of [React to a new object in a Bucket](#react-to-a-new-object-in-a-bucket) end with Ctrl-C; stop the one that
still runs, if any, since both use the same ports. In the root folder of the repository cloned in [Usage](#usage),
replace the content of `examples/log-burst/ticker.yaml` with this manifest, whose action passes the input
`{"items": "many"}`. `log-burst`'s contract refuses that input: the TypeScript Function wants a number in `items`, and
the Python Function accepts no field named `items`:

::include[../../../../../api/types/v1alpha1/examples/events/dead-letter.yaml#dead-letter]

Then start the example as in [Usage](#usage):

```console
$ funcdctl dev examples/log-burst --cport 8081
```

The first tick, about 10 seconds after the banner, fails. Wait 10 seconds, then, in another terminal, list the dead
letters, newest first, again until the list holds one. Each line shows the dead letter's identifier, its Sensor,
its EventSource and event, its action, the number of attempts and the reason. In TypeScript, the reason reads:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token eventing dlq list
01M7B1JTZ6P6WHF86MT0JFKYJV	burst-on-tick	ticker/tick	action=burst	attempts=3	reason=sensor: function default/log-burst returned status 422: {"error":"event data does not match the input contract","details":[{"path":"/items","message":"got string, want number"}]}
```

In Python, the message of the reason is `additional properties 'items' not allowed`. The identifiers differ at each
run, and each tick adds a dead letter. `funcdctl eventing dlq describe` with an identifier prints the dead letter in
full, with the CloudEvent under `payload`; its `data` is the event's empty object, since funcd keeps the event, not the
input that the action built from it.

A replay delivers a dead letter once, through the Sensor as it is now. Save the first manifest in `ticker.yaml` again.
`funcdctl dev` applies it within about a second, and prints nothing when it does. To check that it did, print the
Sensor, again until its action `burst` has no `input`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get sensor burst-on-tick -n default -o json
```

Under `spec`, the entry of `do` then holds only `"name": "burst"`, `"on": "tick"` and `"function": "log-burst"`. Replay
the dead letter with the identifier that the list printed:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token eventing dlq replay 01M7B1JTZ6P6WHF86MT0JFKYJV
replayed 01M7B1JTZ6P6WHF86MT0JFKYJV (delivered; entry removed)
```

The action no longer sets an input, so `log-burst` receives the event's empty object and answers, and funcd removes
the dead letter. A replay records an Invocation of its own. When the delivery fails again, funcd keeps the dead letter,
with its count of attempts set back to 0 and the new reason, and the command fails with a message that contains
`delivery failed (entry re-parked)`. `funcdctl eventing dlq discard` with an identifier deletes a dead letter without
delivering it. To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`: `ticker.yaml` holds the
first manifest again, as at the end of [Usage](#usage).

A `workflow` action fails for the first two causes above, or when funcd cannot create the run. Once the run exists, the
action has succeeded, so a run that fails later leaves no dead letter: the run's phase reports it, as the Workflow
page's [Status](../../workloads/workflow/index.md#status) explains.

## Limits and quotas

- An `interval` lies between 100 milliseconds and 24 hours.
- funcd attempts each delivery `eventing.deliveryAttempts` times in all, 3 by default. The first wait between two
  attempts is `eventing.deliveryBackoffInitial`, 100 milliseconds by default; each later wait is twice as long, up to
  `eventing.deliveryBackoffMax`, 10 seconds by default.
- An attempt to call a Function waits up to `invoke.activationTimeout`, 30 seconds by default, for a worker of the
  Function, then up to 30 seconds for its answer.
- funcd makes at most `eventing.maxDeliveriesInFlight` attempts at once, 32 by default, across all Sensors, and at most
  `eventing.maxInFlightPerTarget` at once to one Function or Workflow, 4 by default. A slow target holds only its own
  share, so the other targets go on.
- One Sensor holds at most `eventing.maxQueuedPerSensor` deliveries at once, 4096 by default, whether they wait, are in
  flight or wait between two attempts. A delivery past that number becomes a dead letter at once, with no attempt,
  and a reason that contains `delivery queue of sensor <namespace>/<name> is full`.
- funcd keeps a dead letter for `eventing.deadletter.retention` after its last failure, 720 hours by default, and keeps
  at most `eventing.deadletter.maxEntries` dead letters in each namespace, 1000 by default: a sweep, every hour or
  sooner, deletes the older ones and the oldest past that number, whatever their Sensor.
- The data of a SQL event holds `before` and `after` only while together they hold at most 256 KiB.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each server setting above, and this page's
[Spec](spec.md) tab gives the range of each field.

## Data storage

funcd keeps no event, except the CloudEvent inside a dead letter: an event that fires while no Sensor subscribes to it
is lost, with no Invocation. A delivery waits in memory until its action ends. When funcd stops, it keeps each delivery that still waits
as a dead letter, with a reason that contains `daemon shut down with the delivery still queued`, until
`server.shutdownTimeout` has passed, 15 seconds by default: the deliveries that still wait then are dropped, and a line
of funcd's log counts them. When funcd crashes, the deliveries that still wait are lost.

funcd keeps the dead letters, the keys that each blob source has fired for, and the position that the change feed of
each database has reached in its event store: a store in the directory `eventing.eventstore.dataDir`,
`<storage.dataDir>/eventstore` by default. These survive a restart of funcd, so an object that a blob source fired for
does not fire again. With `storage.mode: memory`, the event store lives in memory, and funcd forgets them when it
stops. funcd keeps the Invocations with its other objects, and no sweep removes them.

## Status

`funcdctl get eventsource <name> -n <namespace> -o json` prints an EventSource's status, and
`funcdctl get sensor <name> -n <namespace> -o json` a Sensor's, each with its phase and a `Ready` condition that
explains it. With `funcdctl dev`, add the `--server` and `--token` flags of the first example. The outcome of each
action shows in its Invocation, as the first example shows, and the dead letters in `funcdctl eventing dlq list`.

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

A change to an EventSource takes effect at once:

- A timer event whose schedule did not change keeps its times. A changed `interval` fires again on the times counted
  from the creation of the EventSource, and a new or changed `cron` first fires at its next time after the change. A
  removed event stops firing.
- When the `prefix` of a blob event changes, the objects already under the new prefix fire, as for a new EventSource.

Deleting an EventSource stops its events, and funcd deletes the keys that its blob events have fired for. A Sensor
that subscribes to its events stays, and receives nothing more.

A change to a Sensor applies to the next event. A delivery that waits when the Sensor changes, and whose action
changed, becomes a dead letter with no more attempt and a reason that contains
`sensor <namespace>/<name> changed with the delivery still queued; replay sends it to the current spec`; a delivery in
flight whose action changed is not attempted again when it fails. Deleting a Sensor does the same with all its
deliveries, and stops its actions. Its dead letters stay, but a replay of one fails, since the Sensor no longer exists.

Under `funcdctl dev`, deleting the file that holds an EventSource or a Sensor deletes it: removing `ticker.yaml` from
the folder of the first example stops the ticks.

## CLI errors

`funcdctl dev` stops before it serves the first example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for events:

- It lists the prefixes of each blob source every second, where a server waits `eventing.blobPollInterval`, 15
  seconds by default. `dev.platform.eventing.blobPollInterval` in the file `funcdctl.config.yaml` of the current folder
  sets another interval; the [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) describes that
  file.
- It applies the EventSources and the Sensors of its folder after the Functions and the Workflows, deletes those whose
  file leaves the folder, and applies a saved change within about a second.
- It gives the resource group `dev` to an object of its folder that names none. On a server, an EventSource or a
  Sensor must set `metadata.resourceGroup`.
- It keeps the event store in memory, even with `--persist`: the dead letters, and the keys that each blob source has
  fired for, are lost when `funcdctl dev` stops.

`funcdctl dev` keeps its other state in memory as well, unless the `--persist` flag keeps it between runs in
`.funcd-dev`, a folder that it creates in the current folder, as the
[Function](../../workloads/function/index.md#run-it-locally) page describes; the EventSources, the Sensors and the
Invocations are among the objects that it keeps then. To start again from nothing, delete that folder once
`funcdctl dev` has stopped.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/log-burst): the Function that the first
  example's Sensor calls at each tick.
- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): a blob
  EventSource and a Sensor that starts a Workflow run for each new object.
- [sql-orders in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/sql-orders): a sql EventSource and a
  Sensor that calls a Function with each change of a table.
