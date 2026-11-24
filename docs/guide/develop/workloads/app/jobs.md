---
title: Jobs in an App
description: Declare Jobs in an App beside its Functions, run them, and know what a change and a deletion of the App do to their runs.
---

## Usage

An [App](index.md) declares [Jobs](../job/index.md) in its `jobs` section as it declares Functions in `functions`: each
entry is a Job's spec under a `name`, or a `ref` to a Job that exists. funcd writes each declared Job as a part of the
App, changes it with the App and deletes it with the App. A run of the Job is no part of the App: it is started apart
from the App, as for any Job, and the App's readiness and rollouts never wait for a run.

This example applies the App `reports`, whose one part is the Job `report`, to a running server, then starts a run of
the Job. It needs funcdctl ([Install](../../../get-started/install.md)) and a funcd server on the same machine whose API
listens at `127.0.0.1:8080`, such as [`funcd --memory`](../../running-funcd/index.md), which accepts the built-in token
`funcd-dev-token` in the namespace `default`. To run a `nodejs22` handler, funcd needs Node.js on its `PATH`.

The Job runs the handler of the [Job page's example](../job/index.md#usage), `report.mjs` with its contract
`report.contract.yaml`. Push it as that example does, into an OCI layout in the current directory:

```console
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/jobs/registry:report@sha256:a3d48457442c7b24546eefda4896fa57e5fec982545f28669dae219d36330c7c
```

The App `reports`, from
[`jobs.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/app/jobs.yaml). Save it as
`reports.yaml` and replace `image` with the reference that the push printed:

::include[../../../../../api/types/v1alpha1/examples/app/jobs.yaml#app]

An entry holds the [fields of a Job's spec](spec.md#appspecjobs). It declares no store, Lake, ConfigMap or Secret of
its own: the App declares each of them in its own sections, and the entry binds it, as a `functions` entry does. Apply
the App, then repeat `get` until `status.phase` is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f reports.yaml
applied App/reports
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get app reports -n default -o json
```

`status.children` lists `Job/report` as `Ready` once the Job is Ready: funcd has stamped its first Revision,
`report-v1`, and checked it as it checks a Function. Start a run as for any Job, then repeat
[`funcdctl job describe`](../../tools/funcdctl/spec.md#funcdctl-job-describe) until the phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run report report-1 --input '{"month":"2026-10"}'
report-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-1
RUN report-1   job: report   phase: Succeeded
revision: report-v1   digest: sha256:a3d48457442c7b24546eefda4896fa57e5fec982545f28669dae219d36330c7c
started by: dev
started: 2026-11-23T23:59:18.173Z   ended: 2026-11-23T23:59:18.413Z   duration: 240ms
result: {"month":"2026-10","rows":42}
trace: dc5c745af8bcda79213cd4b3965402e0
full logs: funcdctl job logs report-1
```

## Change a Job part

A change of the entry is a change of the App's spec: funcd rolls out a new App revision, and the Job stamps a new
Revision. Change `timeout` in `reports.yaml` from `10m` to `20m`, apply the file again, and read the App's history
with [`funcdctl app history`](../../tools/funcdctl/spec.md#funcdctl-app-history):

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f reports.yaml
applied App/reports
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token app history reports
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-23T23:59:10.664Z
2          1.0.0     Ready   2026-11-23T23:59:26.757Z
```

The rollout waits for the Job to be Ready at its new spec, never for a run. A run that is running keeps the Revision
that it pinned, `report-v1`, to its end, and the next run takes `report-v2`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run report report-2 --input '{"month":"2026-11"}'
report-2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe report-2
RUN report-2   job: report   phase: Succeeded
revision: report-v2   digest: sha256:a3d48457442c7b24546eefda4896fa57e5fec982545f28669dae219d36330c7c
```

The App owns its Job parts: funcd writes an edit of the Job `report` through the API back from the App's entry, as it
does for every part, and records it in the App's `status.lastSelfHeal`.

## Delete the App

[`funcdctl app delete`](../../tools/funcdctl/spec.md#funcdctl-app-delete) deletes the App and its parts, the Job
included. Each run of the Job that has not ended then ends `Cancelled` with `JobDeleted`, a running run's worker
stopped first, so wait for a run to end before deleting its App. The runs stay listed until
[`job.retention`](../../running-funcd/spec.md#funcdconfigyaml) after their end:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token app delete reports
deleted Job/report
deleted AppRevision/reports-1
deleted AppRevision/reports-2
deleted Revision/report-v2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job runs report
NAME
report-2
report-1
```

A revision that drops a `jobs` entry deletes its Job in the same way, once that revision is current. An App's backup
copies the local volumes of its Functions, not those of its Jobs.

## In an App template

A [template](index.md#usage)'s `jobs` entry names its image as a `functions` entry does, as
`${{ images.<name> }}`: [`funcdctl app lock`](../../tools/funcdctl/spec.md#funcdctl-app-lock) pins the image's digest
in `app.lock`, and the rendered Job carries `<registry>/<repo>:<version>@<digest>`. `funcdctl dev` builds a Job
entry's image from its `--build` folder as it builds a Function's, and a saved build rolls out a new revision, whose
new Revision the next run takes.

## Status

`funcdctl get app <app> -n <namespace> -o json` shows one line per Job part in `status.children`:

| State | Reason | Meaning |
| --- | --- | --- |
| `Pending` | `Progressing` | funcd has not written the Job yet, or the Job is not checked at its latest spec yet. |
| `Pending` | the Job's own, such as `RuntimeUnavailable` or `ContractMissing` | The Job is not Ready; the [Job page](../job/index.md#status) explains its reasons. |
| `Ready` | | The Job is Ready at its latest spec. |
| `Pruning` | `NotCurrent`, or none | A new revision dropped the entry; funcd deletes the Job once that revision is current and its post-hooks are done. |
