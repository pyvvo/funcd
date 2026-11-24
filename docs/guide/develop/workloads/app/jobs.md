---
title: Jobs in an App
description: Declare Jobs in an App beside its Functions, run them, and know what a change and a deletion of the App do to their runs.
---

## Usage

An App declares [Jobs](../job/index.md) in its `jobs` section as it declares Functions in `functions`: each entry is a
Job's spec under a `name`, or a `ref` to a Job that exists. funcd writes each Job that an entry declares by `name` as a
[part](../../../platform/workloads/app.md) of the App, changes it with the App and deletes it with the App. A `ref`
entry names a Job that exists already: the App waits until that Job is Ready, and never writes or deletes it. A run of
a Job is no part of the App: it is started apart from the App, as for any Job, and the App's readiness and rollouts
never wait for a run.

This example applies the App `reports`, whose one part is the Job `monthly`, to a running server, then starts runs of
the Job with the handler of the Job page's example. Its names are its own, so it also runs on a server where the
[Job page's example](../job/index.md#usage) ran. It needs funcdctl ([Install](../../../get-started/install.md)), Git,
and a funcd server on the same machine whose API listens at `127.0.0.1:8080`, such as `funcd --memory` started as the
Job page shows, which accepts the built-in token `funcd-dev-token` in the namespace `default`. funcd runs the
TypeScript handler with the Node.js on its `PATH`.

Clone the repository and push the committed build `report.mjs` with its contract beside it. If the Job page's example
cloned the repository already, push from that folder: a push of the same files gives the same digest.

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript/examples/job-report
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/funcd-typescript/examples/job-report/registry:report@sha256:d124133ffbb80a5f496f4bc9adcbd7253d9ba6c29d5642382074f3b36d6a31e0
```

The App `reports`, from
[`jobs.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/app/jobs.yaml). Save it as
`reports.yaml` in the same folder and replace `image` with the reference that the push printed:

::include[../../../../../api/types/v1alpha1/examples/app/jobs.yaml#app]

An entry holds the [fields of a Job's spec](spec.md#appspecjobs). It declares no store, Lake, ConfigMap or Secret of
its own: the App declares each of them in its own sections, and the entry binds it, as a `functions` entry does. A Job
part writes a table or a prefix of such a store only as its [`owner`](spec.md#appspecbucketsprefixesowner), with
`kind: Job` and the Job's name, or through a writer role, as the Job page's [Access](../job/index.md#access) section
says. In Python, push the handler as the Job page's [Python](../job/index.md#python) section does, and set the entry's
`runtime` to `python314` and its `image` to the reference that that push prints.

Apply the App, then repeat `get` until `status.phase` is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f reports.yaml
applied App/reports
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get app reports -n default -o json
```

[`status.children`](spec.md#appstatuschildren) lists `Job/monthly` as `Ready` once the Job is Ready: funcd has
created its first Revision, `monthly-v1`, and the Job passed the checks that a Function passes before its first
worker. Start a run as for any Job, then repeat
[`funcdctl job describe`](../../tools/funcdctl/spec.md#funcdctl-job-describe) until the phase is `Succeeded`; the
handler works for three seconds:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run monthly monthly-1 --input '{"month":"2026-10"}'
monthly-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe monthly-1
RUN monthly-1   job: monthly   phase: Succeeded
revision: monthly-v1   digest: sha256:d124133ffbb80a5f496f4bc9adcbd7253d9ba6c29d5642382074f3b36d6a31e0
started by: dev
started: 2026-11-24T01:39:29.016Z   ended: 2026-11-24T01:39:32.250Z   duration: 3s234ms
result: {"month":"2026-10","rows":31}
trace: 4b85e0f60c8546228b0cb2dfd4fa4ff6
full logs: funcdctl job logs monthly-1
```

## Change a Job part

A change of the entry is a change of the App's spec: funcd rolls out a new App revision and creates a new Revision of
the Job. Change [`timeout`](spec.md#appspecjobs) in `reports.yaml` from `10m` to `20m` and apply the file again.
`apply` returns once the App is stored, so repeat
[`funcdctl app history`](../../tools/funcdctl/spec.md#funcdctl-app-history) until revision 2 is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f reports.yaml
applied App/reports
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token app history reports
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-24T01:39:22.540Z
2          1.0.0     Ready   2026-11-24T01:39:40.077Z
```

The rollout waits for the Job to be Ready at its new spec, never for a run. A run that is running keeps the Revision
that it pinned, `monthly-v1`, to its end, and a run that starts once revision 2 is `Ready` takes `monthly-v2`. Start
one, then repeat `job describe` until the phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run monthly monthly-2 --input '{"month":"2026-11"}'
monthly-2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe monthly-2
RUN monthly-2   job: monthly   phase: Succeeded
revision: monthly-v2   digest: sha256:d124133ffbb80a5f496f4bc9adcbd7253d9ba6c29d5642382074f3b36d6a31e0
started by: dev
started: 2026-11-24T01:39:40.138Z   ended: 2026-11-24T01:39:43.354Z   duration: 3s216ms
result: {"month":"2026-11","rows":30}
trace: e8158e18f3648e7a7f4a926a06e0635b
full logs: funcdctl job logs monthly-2
```

When another writer changes or deletes the Job `monthly`, funcd writes the App's entry back and records it in
[`status.lastSelfHeal`](spec.md#appstatuslastselfheal), so change a Job part through the App.

## Delete the App

[`funcdctl app delete`](../../tools/funcdctl/spec.md#funcdctl-app-delete) deletes the App and its parts, the Job
included. Each run of the Job that has not ended and holds the Job's UID then ends `Cancelled` with `JobDeleted`, as
the Job page's [Changes and deletion](../job/index.md#changes-and-deletion) section says. funcd stops a running run's
worker first, so its handler may have done part of its work: wait for a run to end before deleting its App. The runs
stay listed until [`job.retention`](../../running-funcd/spec.md#funcdconfigyaml) after their end, 720h by default:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token app delete reports
deleted Job/monthly
deleted AppRevision/reports-1
deleted AppRevision/reports-2
deleted Revision/monthly-v2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job runs monthly
NAME
monthly-2
monthly-1
```

A revision that drops a `jobs` entry deletes its Job in the same way, once that revision is current and its post-hooks
are done. A Job part's local volumes follow their `deletion`, as a Function part's do, and an App's backup copies the
local volumes of its Functions, not those of its Jobs.

## Declare a Job in an App template

A [template](index.md#usage)'s `jobs` entry names its image as a `functions` entry does, as `${{ images.<name> }}`:
[`funcdctl app lock`](../../tools/funcdctl/spec.md#funcdctl-app-lock) pins the image's digest in `app.lock`, and the
rendered Job carries `<registry>/<repo>:<version>@<digest>`. `funcdctl dev` pushes the build that it finds under the
image's name in its `--build` folders, as it does for a `functions` entry. A changed build makes `funcdctl dev` apply
the App again; funcd creates a new Revision of the Job, which the next run takes.

## Status

`funcdctl get app <app> -n <namespace> -o json` shows one line per Job part in
[`status.children`](spec.md#appstatuschildren):

| State | Reason | Meaning |
| --- | --- | --- |
| `Pending` | `Progressing` | funcd has not written the Job yet, or has not checked it at its latest spec yet. |
| `Pending` | the Job's own, such as `RuntimeUnavailable` or `ContractMissing` | The Job is not Ready; the [Job page](../job/index.md#status) explains its reasons. |
| `Pending` | the App's own, such as `ChildNotOwned` or `RefNotFound` | The App's [Status](index.md#status) explains it. |
| `Ready` | | The Job is Ready at its latest spec. |
| `Pruning` | `NotCurrent`, or none | A new revision dropped the entry; funcd deletes the Job once that revision is current and its post-hooks are done. |
