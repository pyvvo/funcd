---
title: Job
description: Run code once with an input, read the run's result and logs, cancel a run, and give a Job the stores and network access that its code uses.
---

## Usage

A [Job](../../../platform/workloads/job.md) is code that runs to its end, such as a report or an import. Each run of
the Job is a **JobRun**, which carries an input. funcd calls the Job's handler once for the run and keeps its answer as
the run's **result**.

This example applies the Job `report` to a running server, starts a run for the month `2026-10`, reads its result and
its logs, then cancels a second run. It needs funcdctl ([Install](../../../get-started/install.md)) and a funcd server
on the same machine whose API listens at `127.0.0.1:8080`, such as [`funcd --memory`](../../running-funcd/index.md),
which accepts the built-in token `funcd-dev-token` in the namespace `default`. To run a `nodejs22` handler, funcd
needs Node.js on its `PATH`.

The handler is a [Function's handler](../function/index.md#write-a-handler-and-its-contract): it receives the run's
input in `event.data`, and its return value, in JSON, is the run's result. Its file, here `report.mjs`, needs its
contract, `report.contract.yaml`, beside it: a Job whose artifact carries no contract is not Ready. This handler takes `{"month": "2026-10"}`, logs the
month and returns a count of rows. Push it with [`funcdctl push`](../../tools/funcdctl/spec.md#funcdctl-push) into an
OCI layout in the current directory, which the server on the same machine reads:

```console
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/jobs/registry:report@sha256:4b69fbdf58313be910dfecd0d75860646efcd10ede13015de1ecfaf5e329481f
```

The Job `report`, from
[`job.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/job/job.yaml). Save it as
`job.yaml` and replace `image` with the reference that the push printed:

::include[../../../../../api/types/v1alpha1/examples/job/job.yaml#job]

[`timeout`](spec.md#jobspec) bounds each run's call, and [`concurrency`](spec.md#jobspec) lets two runs of the Job hold
a worker at once. Apply the Job, then repeat `get` until `status.phase` is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f job.yaml
applied Job/report
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get job report -n default -o json
```

Start a run with [`funcdctl job run`](../../tools/funcdctl/spec.md#funcdctl-job-run). It prints the run's name; without
a name, funcd names the run after its Job with a suffix. Then repeat
[`funcdctl job describe`](../../tools/funcdctl/spec.md#funcdctl-job-describe) until the phase is `Succeeded`, `Failed`
or `Cancelled`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run report report-1 --input '{"month":"2026-10"}'
report-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-1
RUN report-1   job: report   phase: Running
reason:    message: the worker of revision "report-v1" runs the handler
revision: report-v1   digest: sha256:4b69fbdf58313be910dfecd0d75860646efcd10ede13015de1ecfaf5e329481f
started by: dev
started: 2026-11-23T22:47:15.024Z
trace: 61d07d62ad83fc9872a026be2e3054ae
full logs: funcdctl job logs report-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-1
RUN report-1   job: report   phase: Succeeded
revision: report-v1   digest: sha256:4b69fbdf58313be910dfecd0d75860646efcd10ede13015de1ecfaf5e329481f
started by: dev
started: 2026-11-23T22:47:15.024Z   ended: 2026-11-23T22:47:18.122Z   duration: 3s98ms
result: {"month":"2026-10","rows":42}
trace: 61d07d62ad83fc9872a026be2e3054ae
full logs: funcdctl job logs report-1
```

The run pinned the Job's current **Revision**, `report-v1`, when it started, and keeps it to its end. `started by`
is the subject of the credential that created the run, which grants nothing. The input must match the input schema
of the Job's contract: `job run` with `{"day":1}` is refused with 422 `InputSchemaMismatch`, naming
`body.spec.input.day` and `body.spec.input.month`.

[`funcdctl job logs`](../../tools/funcdctl/spec.md#funcdctl-job-logs) prints the run's records: its worker's output
and logs, and the records of the calls that the run made, found by its trace ID. Records appear within
[`funclog.segmentMaxAge`](../../running-funcd/spec.md#funcdconfigyaml), so repeat the command until they do. `-o wide`
adds each record's `source`, `inv`, `trace` and arguments:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job logs report-1 -o wide
2026-11-23T22:47:15.080Z [INFO] 0 funcd-shim: listening on 127.0.0.1:62348 source=stdout
2026-11-23T22:47:15.109Z [INFO] 0 building the report of source=console inv=703ee4cc7e7c22d1 trace=61d07d62ad83fc9872a026be2e3054ae args=["building the report of","2026-10"]
```

[`funcdctl job cancel`](../../tools/funcdctl/spec.md#funcdctl-job-cancel) cancels a run. A run that has not started
ends at once; a running run's worker is stopped first, so its handler may have done part of its work:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run report report-2 --input '{"month":"2026-11"}'
report-2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job cancel report-2
cancel requested for report-2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-2
RUN report-2   job: report   phase: Cancelled
reason: Cancelled   message: spec.cancel: the worker was stopped, and the handler may have done part of its work
revision: report-v1   digest: sha256:4b69fbdf58313be910dfecd0d75860646efcd10ede13015de1ecfaf5e329481f
started by: dev
started: 2026-11-23T22:47:18.239Z   ended: 2026-11-23T22:47:19.288Z   duration: 1s47ms
trace: a29057228e18dc0eafa09c7a1ec4b766
full logs: funcdctl job logs report-2
```

[`funcdctl job runs report`](../../tools/funcdctl/spec.md#funcdctl-job-runs) lists the Job's runs, newest first.

## Read the run in the handler

Besides a Function's context, a Job's handler gets `ctx.run`: the run's `name`, its `job`, the `revision` that it
pinned, its `phase`, which is `run` for the run's call, and `startedBy`, `started_by` in Python, the subject of the
credential that created the run. The handler calls the context's methods, such as `ctx.log`, as a Function's handler
does; the Spec tab lists them under [Handler API](spec.md#handler-api).

funcd calls the handler once for each run and never again for that run, even when the run fails or funcd stops
during it: work that must happen again takes a new run.

## Create a run from a manifest

A JobRun is also a manifest: [`spec.job`](spec.md#jobrunspec) names the Job, in the run's namespace, and
[`spec.input`](spec.md#jobrunspec) is the input, both fixed once the run exists. The run `report-2026-10`, from
[`jobrun.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/job/jobrun.yaml):

::include[../../../../../api/types/v1alpha1/examples/job/jobrun.yaml#jobrun]

Save it as `jobrun.yaml` and apply it. A run created before its Job waits in `Pending` with `JobNotFound` until the
Job exists, then starts as `report-1` did:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f jobrun.yaml
applied JobRun/report-2026-10
```

## Access

A run reaches the stores, Secrets, Lakes, add-on objects and Functions that its Job binds as the Job itself: the
principal `Job::"<namespace>/<job>"`, apart from every Function, a Function of the same name included. Each binding
grants what it grants a Function, such as `kv::read` on a table, as the [Security](../../../platform/security-and-networking/security.md)
page explains.

- A write takes the owner or a writer role. An owner names its kind, `kind: Job` with the Job's name, and a store that
  the Job declares by name at its root without an owner is the Job's own.
- A RolesAssignment grants the Job a role with a principal of kind `Job`, as in
  [Choose a role](../../security-and-networking/security/index.md#choose-a-role).
- A namespace's Policy names the Job as `Job::"<namespace>/<job>"`, so a `forbid` can revoke what a binding grants it.
- With egress on, an EgressPolicy reaches a Job only through an `appliesTo` item of kind `Job` that names it; an empty
  `appliesTo` applies to every Function of the namespace and to no Job, as in
  [Allow a Function's outbound connections](../../security-and-networking/networking/index.md#allow-a-functions-outbound-connections).

A run's worker checks its bindings before its handler is called, as a Function's worker does. A run whose binding check
fails, such as one whose bound Secret a Policy forbids, never becomes ready: it ends `Failed` with `StartFailed` after
[`runtime.bootTimeout`](../../running-funcd/spec.md#funcdconfigyaml), and the message names the binding.

The built-in roles treat a Job as they treat a Workflow and a JobRun as a WorkflowRun: a developer of the namespace
creates and changes both, and a viewer only reads them.

## Limits and quotas

- [`timeout`](spec.md#jobspec): 0s to 168h, from the worker's start; 0s or unset means none.
- [`concurrency`](spec.md#jobspec): 1 to 15, 1 when unset, and at most 1 with a local volume.
- [`job.maxRunning`](../../running-funcd/spec.md#funcdconfigyaml): the runs of all Jobs that hold a worker at once on
  the node, 4 by default. A run past it, or past its Job's `concurrency`, waits in `Pending` with `Queued`, and a free
  slot goes to the oldest waiting run whose Job is below its own `concurrency`.
- [`job.payloadLimit`](../../running-funcd/spec.md#funcdconfigyaml): 262144 bytes by default for an input and for a
  result. A larger input is refused with 413 when the run is created; a larger result ends the run `Failed` with
  `PayloadLimitExceeded`.
- [`job.retention`](../../running-funcd/spec.md#funcdconfigyaml): funcd deletes a run 720h after it ends, by default.

## Status

`funcdctl job describe <run>` shows a run's phase, reason and message:

| Phase | Reason | Meaning |
| --- | --- | --- |
| `Pending` | `JobNotFound` | The Job does not exist yet. |
| `Pending` | `JobNotReady` | The Job is not Ready at its latest spec. |
| `Pending` | `Queued` | The Job's `concurrency` or the node's `job.maxRunning` is reached. |
| `Running` | | The run's worker runs the handler. |
| `Succeeded` | | The handler answered; `result` holds its answer. |
| `Failed` | `StartFailed` | The worker did not start, or did not become ready within `runtime.bootTimeout`; the message names the last binding check that failed. |
| `Failed` | `InputSchemaMismatch` | The input does not match the contract of the Revision that the run pinned, or the handler refused it with 422. |
| `Failed` | `HandlerFailed` | The handler failed or its answer was not JSON; the message holds the first line of the cause. |
| `Failed` | `PayloadLimitExceeded` | The result is larger than `job.payloadLimit`. |
| `Failed` | `RunTimedOut` | The call ran past the Job's `timeout`, and the worker was stopped. |
| `Failed` | `Interrupted` | funcd stopped while the run was running; the run does not run again. |
| `Cancelled` | `Cancelled` | The run was cancelled. |
| `Cancelled` | `JobDeleted` | The Job was deleted or created again, or the Revision that the run pinned is gone. |
| `Cancelled` | `Restored` | A restore of the platform ended the run: the backup cannot say whether it ran since. |

The Job itself is Ready once funcd has checked it as it checks a Function before its first worker. `funcdctl get job
<job> -n <namespace> -o json` shows its `Ready` condition, with a Function's reasons and two of its own:
`ContractMissing`, when the artifact carries no contract, and `ContractInvalid`, when its contract does not compile.

## Changes and deletion

- An edit of the Job creates a new Revision, named after the Job and its version, such as `report-v2`. A running run
  keeps the Revision that it pinned, and the next run takes the new one. funcd keeps a Revision while a run that has
  not ended pins it.
- A Revision keeps the image digest of its creation, so push new code under a new tag, or set
  [`imageDigest`](spec.md#jobspec).
- Deleting a run stops its worker. Deleting a Job ends each of its runs that has not ended, `Cancelled` with
  `JobDeleted`, and removes its Revisions, with a Lake or a [ConfigMap](../configuration/index.md) that the Job
  declares by `name`. A KVStore, Bucket or SQLDatabase that it declares by `name` stays, unless its `deletion` is
  `delete`.
- When funcd stops, the runs that are running stop with it; at its next start, each ends `Failed` with `Interrupted`.

## CLI errors

| Message or cause | Action |
| --- | --- |
| `kind "Job" is namespaced; a namespace is required`, from `funcdctl get job` | Add `-n <namespace>`. The `funcdctl job` commands take `default` when `-n` is unset. |
