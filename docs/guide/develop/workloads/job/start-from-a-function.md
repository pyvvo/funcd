---
title: Start a run from a Function
description: Start a Job's run from a Function's code, read the run back, and start one run for an event that arrives more than once.
---

## Usage

A Function starts a run of a [Job](index.md) when an event or a schedule calls for work that runs to its end, such as
a monthly report. The Function's [`spec.bindings.job`](../function/spec.md#functionspecbindingsjob) names the Job under an
alias. Its code calls [`ctx.job.start`](spec.md#handler-api), `context.job.start` in Python, with that alias and the
run's input, and gets the run's name back at once, without waiting for the run. It reads the run back with
[`ctx.job.get`](spec.md#handler-api): its phase and, once the run Succeeded, its result.

This example applies the Job `report` of the [Job page's example](index.md#usage), or `pyreport` in Python, and the
Function `planner`, or `pyplanner`, which binds it under the alias `report`. A call of the Function with a month starts
that month's run of the Job and answers the run's name and phase. The Job's `report.yaml` sets
[`approval.bypass`](spec.md#jobspecapproval), so each run runs at once; a run of a Job without it waits for a person's
approval, whoever starts it, as [Approve a run](approval.md) shows.

It needs funcdctl ([Install](../../../get-started/install.md)), Git, curl, Node.js for the TypeScript section, Python
3.14 for the Python section, and a funcd server on the same machine, started as the [Job page](index.md#usage) starts
it: [`funcd --memory`](../../running-funcd/index.md), with a data directory that it can write. This page also calls the
Function on funcd's data plane, at `127.0.0.1:8081`. The data plane listens on a free port of funcd's choosing unless
`FUNCD_DATA_PLANE_ADDR` sets one, since [`server.dataPlaneAddr`](../../running-funcd/spec.md#funcdconfigyaml) defaults
to `127.0.0.1:0`, so a server started with the Job page's command must start again with this one before the calls
below:

```console
$ FUNCD_DATA_DIR="$(mktemp -d)" FUNCD_DATA_PLANE_ADDR=127.0.0.1:8081 FUNCD_PYTHON="$(command -v python3.14)" funcd --memory
```

Its API listens at `127.0.0.1:8080` and accepts the built-in token `funcd-dev-token` in the namespace `default`.
funcd runs the TypeScript code with the Node.js on its `PATH`. `funcd --memory` keeps nothing when it stops, so the
steps below apply the Job's `report.yaml` again.

The handler names the run after the month, `report-<month>`, or `pyreport-<month>` in Python. A start under a name that
a run of the bound Job holds, with the same input, answers that run's name and starts nothing, so a second call for one
month starts no second run. The run passes the checks of a run that a person starts with
[`funcdctl job run`](../../tools/funcdctl/spec.md#funcdctl-job-run): its input must fit
[`job.payloadLimit`](../../running-funcd/spec.md#funcdconfigyaml) and the Job's contract, as
[Handle a refused start or read](#handle-a-refused-start-or-read) explains.

### TypeScript

The handler, `src/main.ts`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/src/main.ts]

Its contract, in `planner.contract.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/planner.contract.yaml#contract]

The Function `planner`, in `planner.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/planner.yaml#function]

Clone the repository, or reuse the clone of the Job page's example. In `examples/job-report`, push the Job's build, as
the [Job page](index.md#typescript) does:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript/examples/job-report
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/funcd-typescript/examples/job-report/registry:report@sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
```

Replace `image` in `report.yaml` with the reference that the push prints, and apply it. Then push the Function's
committed build `planner.mjs` with its contract beside it:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f report.yaml
applied Job/report
$ cd ../job-planner
$ funcdctl push planner.mjs "oci-layout://$PWD/registry:planner"
oci-layout:///home/dev/funcd-typescript/examples/job-planner/registry:planner@sha256:8696a1f3e12024256b8f21f7281c1e552353f621ee9e031da31dbb0b4de328b4
```

Replace `image` in `planner.yaml` with the reference that the push prints. Apply the Function, repeat `get` until its
`status.phase` is `Idle` or `Ready`, and call it twice for one month on the data plane. Then repeat
`funcdctl job describe` until the run's phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f planner.yaml
applied Function/planner
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get function planner -n default -o json
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-09"}' http://127.0.0.1:8081/function/planner
{"run":"report-2026-09","phase":"Running"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-09"}' http://127.0.0.1:8081/function/planner
{"run":"report-2026-09","phase":"Running"}
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-2026-09
RUN report-2026-09   job: report   phase: Succeeded
revision: report-v1   digest: sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
started by: Function::"default/planner"
started: 2026-11-24T02:53:01.326Z   ended: 2026-11-24T02:53:04.443Z   duration: 3s117ms
result: {"month":"2026-09","rows":30}
trace: 7a092419a7543ad3562f4c2f7db1511f
full logs: funcdctl job logs report-2026-09
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job runs report
NAME
report-2026-09
```

The two calls started one run. The phase that a call answers is the run's phase when the handler read it, such as
`Pending` or `Running`. The run's `started by` is the Function, `Function::"default/planner"`.

### Python

The handler, `src/main.py`:

::include[../../../../../_repos/funcd-python/examples/job-planner/src/main.py]

Its contract, in `pyplanner.contract.yaml`:

::include[../../../../../_repos/funcd-python/examples/job-planner/pyplanner.contract.yaml#contract]

The Function `pyplanner`, in `planner.yaml`:

::include[../../../../../_repos/funcd-python/examples/job-planner/planner.yaml#function]

Clone the repository, or reuse the clone of the Job page's example. In `examples/job-report`, push the Job's handler,
as the [Job page](index.md#python) does:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/job-report
$ mkdir -p dist && cp src/main.py dist/pyreport.py && cp pyreport.contract.yaml dist/
$ funcdctl push dist/pyreport.py "oci-layout://$PWD/registry:pyreport"
oci-layout:///home/dev/funcd-python/examples/job-report/registry:pyreport@sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
```

Replace `image` in `report.yaml` with the reference that the push prints, and apply it. The Function's handler needs no
third-party package either, so it is pushed as one file, `pyplanner.py`, beside its contract:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f report.yaml
applied Job/pyreport
$ cd ../job-planner
$ mkdir -p dist && cp src/main.py dist/pyplanner.py && cp pyplanner.contract.yaml dist/
$ funcdctl push dist/pyplanner.py "oci-layout://$PWD/registry:pyplanner"
oci-layout:///home/dev/funcd-python/examples/job-planner/registry:pyplanner@sha256:27a4fcc1523225d93cb69db203e0dede758d6399022c6e5ad5bf3a2129132cb6
```

Replace `image` in `planner.yaml` with the reference that the push prints. Apply the Function, repeat `get` until its
`status.phase` is `Idle` or `Ready`, and call it twice for one month on the data plane. Then repeat
`funcdctl job describe` until the run's phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f planner.yaml
applied Function/pyplanner
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get function pyplanner -n default -o json
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-09"}' http://127.0.0.1:8081/function/pyplanner
{"run":"pyreport-2026-09","phase":"Running"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-09"}' http://127.0.0.1:8081/function/pyplanner
{"run":"pyreport-2026-09","phase":"Running"}
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe pyreport-2026-09
RUN pyreport-2026-09   job: pyreport   phase: Succeeded
revision: pyreport-v1   digest: sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
started by: Function::"default/pyplanner"
started: 2026-11-24T02:53:05.223Z   ended: 2026-11-24T02:53:08.378Z   duration: 3s155ms
result: {"month":"2026-09","rows":30}
trace: 80e5c40647c9eb74888072322baa306a
full logs: funcdctl job logs pyreport-2026-09
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job runs pyreport
NAME
pyreport-2026-09
```

## Read a run back

`ctx.job.get` answers a `JobRunState`, a type that `@funcd-dev/shim` exports: the run's `name`, `phase` and `reason`,
its `startedAt` and `endedAt`, and, once the phase is `Succeeded`, its `result`, each of the last three `null` until
set. In Python, `context.job.get` answers a dict, `JobRunState` in `funcd_shim`, with the keys `name`, `phase`,
`reason`, `started_at`, `ended_at` and `result`, each of the last three `None` until set. `ctx.job.start` and
`ctx.job.get` never wait for a run: a Function's call has a deadline, and a run may take hours. A Function that needs a
run's end reads the run again in a later call, such as one that a schedule or a later event makes.

A Function reads only the runs of the Jobs that it binds: a run of another Job reads as not found, and so does a run
that [`job.retention`](../../running-funcd/spec.md#funcdconfigyaml) removed.

## Start one run when an event arrives more than once

A Function that funcd calls at least once, as a [Sensor](../../services/events/index.md)'s action, can be called twice
for one event, as [Read the event in the handler](../../services/events/index.md#read-the-event-in-the-handler) says,
and each call that runs `ctx.job.start` without a name starts a run of its own. With a name as the third argument, made
from the event, the second call answers the first call's run and starts nothing. The name is a run's name: a DNS-1123
label of at most 63 characters, else the start is refused with 400. funcd refuses a start under a name that another run
of the namespace holds, of another Job or with another input, with 409.

A start answers the run of its name whatever the run's phase: once that run has ended, `Failed` or `Cancelled`
included, the start answers it, with its phase, and starts nothing. A new run under that name can start only once
[`job.retention`](../../running-funcd/spec.md#funcdconfigyaml) removes the old one, so a start that must run the work
again takes another name.

## Handle a refused start or read

When funcd refuses a start or a read, `ctx.job.start` and `ctx.job.get` reject with a `JobError`, and raise one in
Python. In TypeScript it is an `Error` whose `name` is `JobError`, a type that `@funcd-dev/shim` exports, with
`status`, the HTTP status, and `problem`, funcd's problem, whose `detail` says why. In Python, `JobError` is a
`RuntimeError` that `funcd_shim` exports, with `status` and `problem`, a dict with the key `detail`. A 409 from a
start, for example, means that another run holds the name; the [Handler API](spec.md#handler-api) lists each status
with its cause.

A start whose input does not match the Job's input schema is refused with 422 and the body
`{"error": …, "details": […]}` that a Function's call answers for an input off its contract; its `JobError` keeps
that body as its `detail`. funcd checks a start's input only when the Job holds the contract of its current spec. A
start whose input is off the contract therefore succeeds before funcd first checks the Job, or after an edit until
funcd checks it again, as the Job page's [Status](index.md#status) says, and its run ends `Failed` with
`InputSchemaMismatch` when it starts.

## Access

A Function needs no role to start the runs of a Job that it binds: each entry of `spec.bindings.job` grants it
`job::start` and `job::read` on its Job, and nothing on any other Job. A call on an alias that the Function does not
bind is refused with 403, and a Job's own spec cannot bind a Job. The binding grants through this built-in policy:

```cedar title="Built-in Job binding policy"
@id("job-start-binding")
permit(principal, action == Action::"job::start", resource)
  when { principal has jobBindings && principal.jobBindings.contains(resource) };

@id("job-read-binding")
permit(principal, action == Action::"job::read", resource)
  when { principal has jobBindings && principal.jobBindings.contains(resource) };
```

A namespace [Policy](../../security-and-networking/security/index.md#write-a-policy) that forbids one of the actions
revokes it, and the start or the read is refused with 403. This Policy forbids `planner` to start runs of `report`:

::include[../../../../../api/types/v1alpha1/examples/job/forbid-start.yaml#policy]

Save it as `forbid-start.yaml`, apply it, and call `planner` again. Its start is refused with 403, and `planner`, which
does not catch the `JobError`, answers its own call with 500 and the error:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f forbid-start.yaml
applied Policy/no-planner-start
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-11"}' http://127.0.0.1:8081/function/planner
{"error":"context.job.start(\"report\") failed: 403 funcd.jobGrants.Grant: default/planner may not job::start on Job \"report\": cedar: forbidden by policy default/no-planner-start#0"}
```

## Changes and deletion

- Deleting the Job leaves the binding in place: a start on its alias is refused with 404 until a Job of that name
  exists again, and the runs it already had stay readable until `job.retention` removes them.

## Examples

- [funcd-typescript/examples/job-planner](https://github.com/pyvvo/funcd-typescript/tree/main/examples/job-planner):
  the Function `planner`, which starts and reads runs of the Job `report`, in TypeScript.
- [funcd-python/examples/job-planner](https://github.com/pyvvo/funcd-python/tree/main/examples/job-planner): the
  Function `pyplanner`, which starts and reads runs of the Job `pyreport`, in Python.
