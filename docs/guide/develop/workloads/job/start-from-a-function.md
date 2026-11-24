---
title: Start a run from a Function
description: Start a Job's run from a Function's code, read the run back, and start one run for an event that arrives more than once.
---

## Usage

A Function starts a run of a [Job](index.md) when an event or a schedule calls for work that runs to its end, such as
a monthly report. The Function's [`spec.bindings.job`](../function/spec.md#functionspecbindingsjob) names the Job under an
alias. Its code calls `ctx.job.start`, `context.job.start` in Python, with that alias and the run's input, and gets the
run's name back at once, without waiting for the run. It reads the run back with `ctx.job.get`: its phase and, once
the run Succeeded, its result.

This example applies the Job `report` of the [Job page's example](index.md#usage), or `pyreport` in Python, and the
Function `planner`, or `pyplanner`, which binds it under the alias `report`. A call of the Function with a month starts
that month's run of the Job and answers the run's name and phase. It needs funcdctl
([Install](../../../get-started/install.md)), Git, curl, and a funcd server on the same machine whose API listens at
`127.0.0.1:8080` and whose data plane listens at `127.0.0.1:8081`, such as
`FUNCD_DATA_PLANE_ADDR=127.0.0.1:8081 funcd --memory` ([funcd](../../running-funcd/index.md)), which accepts the
built-in token `funcd-dev-token` in the namespace `default`. funcd runs the TypeScript code with the Node.js on its
`PATH`, and the Python code with the `python3` on its `PATH`, Python 3.12 or later, or the interpreter that
`FUNCD_PYTHON` names.

The handler names the run after the month, `report-<month>`. A start under a name that a run of the bound Job holds,
with the same input, answers that run's name and starts nothing, so a second call for one month starts no second run.
The run passes the checks of a run that a person starts with
[`funcdctl job run`](../../tools/funcdctl/spec.md#funcdctl-job-run): its input must fit
[`job.payloadLimit`](../../running-funcd/spec.md#funcdconfigyaml) and the Job's contract.

### TypeScript

The handler, in `src/main.ts`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/src/main.ts#handler]

Its contract, in `planner.contract.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/planner.contract.yaml#contract]

The Function `planner`, in `planner.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/job-planner/planner.yaml#function]

Clone the repository. In `examples/job-report`, push the Job's build and apply `report.yaml`, as the
[Job page](index.md#typescript) does. Then push the Function's committed build `planner.mjs` with its contract beside
it, and replace `image` in `planner.yaml` with the reference that the push prints:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript/examples/job-report
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/funcd-typescript/examples/job-report/registry:report@sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f report.yaml
applied Job/report
$ cd ../job-planner
$ funcdctl push planner.mjs "oci-layout://$PWD/registry:planner"
oci-layout:///home/dev/funcd-typescript/examples/job-planner/registry:planner@sha256:8696a1f3e12024256b8f21f7281c1e552353f621ee9e031da31dbb0b4de328b4
```

Apply the Function, repeat `get` until its `status.phase` is `Idle` or `Ready`, and call it twice for one month on the
data plane. Then repeat `funcdctl job describe` until the run's phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f planner.yaml
applied Function/planner
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get function planner -n default -o json
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-10"}' http://127.0.0.1:8081/function/planner
{"run":"report-2026-10","phase":"Running"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-10"}' http://127.0.0.1:8081/function/planner
{"run":"report-2026-10","phase":"Running"}
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-2026-10
RUN report-2026-10   job: report   phase: Succeeded
revision: report-v1   digest: sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
started by: Function::"default/planner"
started: 2026-11-24T01:00:01.085Z   ended: 2026-11-24T01:00:04.333Z   duration: 3s248ms
result: {"month":"2026-10","rows":31}
trace: ec51e222f7f78d99ff8b32a82139cc01
full logs: funcdctl job logs report-2026-10
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job runs report
NAME
report-2026-10
```

The two calls started one run. The phase that a call answers is the run's phase when the handler read it, such as
`Pending` or `Running`. The run's `started by` is the Function, `Function::"default/planner"`.

### Python

The handler, in `src/main.py`:

::include[../../../../../_repos/funcd-python/examples/job-planner/src/main.py#handler]

Its contract, in `pyplanner.contract.yaml`:

::include[../../../../../_repos/funcd-python/examples/job-planner/pyplanner.contract.yaml#contract]

The Function `pyplanner`, in `planner.yaml`:

::include[../../../../../_repos/funcd-python/examples/job-planner/planner.yaml#function]

Clone the repository. In `examples/job-report`, push the Job's handler and apply `report.yaml`, as the
[Job page](index.md#python) does. The Function's handler needs no third-party package either, so it is pushed as one
file, `pyplanner.py`, beside its contract; replace `image` in `planner.yaml` with the reference that the push prints:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/job-report
$ mkdir -p dist && cp src/main.py dist/pyreport.py && cp pyreport.contract.yaml dist/
$ funcdctl push dist/pyreport.py "oci-layout://$PWD/registry:pyreport"
oci-layout:///home/dev/funcd-python/examples/job-report/registry:pyreport@sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f report.yaml
applied Job/pyreport
$ cd ../job-planner
$ mkdir -p dist && cp src/main.py dist/pyplanner.py && cp pyplanner.contract.yaml dist/
$ funcdctl push dist/pyplanner.py "oci-layout://$PWD/registry:pyplanner"
oci-layout:///home/dev/funcd-python/examples/job-planner/registry:pyplanner@sha256:27a4fcc1523225d93cb69db203e0dede758d6399022c6e5ad5bf3a2129132cb6
```

Apply the Function, repeat `get` until its `status.phase` is `Idle` or `Ready`, call it on the data plane, and repeat
`funcdctl job describe` until the run's phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f planner.yaml
applied Function/pyplanner
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get function pyplanner -n default -o json
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-10"}' http://127.0.0.1:8081/function/pyplanner
{"run":"pyreport-2026-10","phase":"Running"}
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe pyreport-2026-10
RUN pyreport-2026-10   job: pyreport   phase: Succeeded
revision: pyreport-v1   digest: sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
started by: Function::"default/pyplanner"
started: 2026-11-24T01:00:28.021Z   ended: 2026-11-24T01:00:31.219Z   duration: 3s198ms
result: {"month":"2026-10","rows":31}
trace: 675e6acb56b78bab29d6671630c94583
full logs: funcdctl job logs pyreport-2026-10
```

## Read a run back

`ctx.job.get` answers the run's `name`, `phase`, `reason`, `startedAt` and `endedAt`, `started_at` and `ended_at` in
Python, and, once the phase is `Succeeded`, its `result`. A call never waits for a run: a Function's call has a
deadline, and a run may take hours. A Function that needs a run's end reads the run again in a later call, such as
one that a schedule or a later event makes. The Spec tab lists both methods under [Handler API](spec.md#handler-api).

A Function reads only the runs of the Jobs that it binds: a run of another Job reads as not found, and so does a run
that [`job.retention`](../../running-funcd/spec.md#funcdconfigyaml) removed.

## Start one run when an event arrives more than once

A Function that funcd calls at least once, as a Sensor's action, can be called twice for one event, and each call
that runs `ctx.job.start` without a name starts a run of its own. With a name as the third argument, made from the
event, the second call answers the first call's run and starts nothing. The name is a run's name: a DNS-1123 label of
at most 63 characters. funcd refuses a start under a name that another run of the namespace holds, of another Job or
with another input, with 409.

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

A namespace [Policy](../../security-and-networking/security/index.md) that forbids one of the actions revokes it, and
the call is refused with 403. With this rule in a Policy of the namespace `default`, the planner's start fails, and so
does its call:

```cedar title="A Policy rule that stops planner from starting runs"
forbid(principal == Function::"default/planner", action == Action::"job::start", resource);
```

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"month":"2026-11"}' http://127.0.0.1:8081/function/planner
{"error":"context.job.start(\"report\") failed: 403 funcd.jobGrants.Grant: default/planner may not job::start on Job \"report\": cedar: forbidden by policy default/no-planner-start#0"}
```

## Errors in the handler

`ctx.job.start` and `ctx.job.get` reject, or raise in Python, a `JobError` that carries the HTTP status and funcd's
problem. The Spec tab's [Handler API](spec.md#handler-api) lists each status with its cause; the ones a handler meets
most:

| Status | When | What to do |
| --- | --- | --- |
| 403 | The Function binds no Job under the alias, or a Policy forbids the action. | Add the binding, or change the Policy. |
| 404 | The bound Job does not exist, or the run is not one of that Job's. | Create the Job, or read a run of the bound Job. |
| 409 | Another run of the namespace holds the name. | Start the run under another name. |
| 413 | The input passes `job.payloadLimit`. | Pass large data by reference, such as the key of an object in a Bucket. |
| 422 | The input does not match the Job's input schema; the detail names each path, as a Function's call does. | Send an input that the Job's contract takes. |

## Changes and deletion

- Deleting the Job leaves the binding in place: a start on its alias is refused with 404 until a Job of that name
  exists again, and the runs it already had stay readable until `job.retention` removes them.
- A run of a Job without [`approval.bypass`](spec.md#jobspecapproval) that a Function starts waits for a person's
  approval as any other run does, as [Approve a run](approval.md) shows.

## Examples

- [funcd-typescript/examples/job-planner](https://github.com/pyvvo/funcd-typescript/tree/main/examples/job-planner):
  the Function `planner`, which starts and reads runs of the Job `report`, in TypeScript.
- [funcd-python/examples/job-planner](https://github.com/pyvvo/funcd-python/tree/main/examples/job-planner): the
  Function `pyplanner`, which starts and reads runs of the Job `pyreport`, in Python.
