---
title: Approve a run
description: Preview what a run would do, approve exactly that preview or reject the run, and read the approval in the handler.
---

## Usage

A run of a [Job](index.md) whose [`approval.bypass`](spec.md#jobspecapproval) is not `true` does not run at once. funcd
first calls the module's `preview` export, which answers what the run would do, and keeps the answer as the run's
**preview**, with its **digest**. The run then waits in `AwaitingApproval` until a person approves that digest or
rejects the run. An approval names the digest of the preview that it approves, so the run runs exactly what the person
saw.

This example applies the Job `report-approved`, or `pyreport-approved` in Python, starts a run for the month `2026-10`,
reads its preview, approves it, and reads the result. It has the prerequisites of the
[Job page's example](index.md#usage): funcdctl, Git, and a funcd server on the same machine whose API listens at
`127.0.0.1:8080`, such as [`funcd --memory`](../../running-funcd/index.md), which accepts the built-in token
`funcd-dev-token` in the namespace `default`. funcd runs the TypeScript code with the Node.js on its `PATH`, and the
Python code with the `python3` on its `PATH`, Python 3.12 or later, or the interpreter that `FUNCD_PYTHON` names.

The Job runs the module of the Job page's example, which also exports `preview`: it answers the month and its number of
rows at once, with no work. The Job page's `report.yaml` sets `approval.bypass`, so its runs never call the preview;
`report-approved` sets none, and its [`approval.expiry`](spec.md#jobspecapproval) of `1h` is how long a preview stays
valid for its approval. [`funcdctl job approve`](../../tools/funcdctl/spec.md#funcdctl-job-approve) prints the preview's
document and digest and asks to confirm before it approves.

### TypeScript

The preview, in `src/main.ts`:

::include[../../../../../_repos/funcd-typescript/examples/job-report/src/main.ts#preview]

The Job `report-approved`, from
[`approval.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/job/approval.yaml):

::include[../../../../../api/types/v1alpha1/examples/job/approval.yaml#job]

Clone the repository and push the committed build `report.mjs` with its contract beside it, as the Job page does. Save
the Job as `approval.yaml` in the same directory and replace `image` with the reference that the push prints:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript/examples/job-report
$ funcdctl push report.mjs "oci-layout://$PWD/registry:report"
oci-layout:///home/dev/funcd-typescript/examples/job-report/registry:report@sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
```

Apply the Job and repeat `get` until `status.phase` is `Ready`. Start a run, then repeat `describe` until its phase is
`AwaitingApproval`, when it shows its preview:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f approval.yaml
applied Job/report-approved
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get job report-approved -n default -o json
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run report-approved approved-1 --input '{"month":"2026-10"}'
approved-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe approved-1
RUN approved-1   job: report-approved   phase: AwaitingApproval
reason:    message: the run waits for a person to approve or reject its preview sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51
revision: report-approved-v1   digest: sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
started by: dev
preview: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51   previewed: 2026-11-24T00:37:04.233Z
  {"month":"2026-10","rows":31}
trace: c1e32ab1c042a6acd856952064152d80
full logs: funcdctl job logs approved-1
```

Approve the preview, answering `y`, then repeat `describe` until the phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job approve approved-1
preview of approved-1, made 2026-11-24T00:37:04.233Z:
{
  "month": "2026-10",
  "rows": 31
}
digest: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51
approve this preview? [y/N] y
approved approved-1 (sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51)
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe approved-1
RUN approved-1   job: report-approved   phase: Succeeded
revision: report-approved-v1   digest: sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
started by: dev
preview: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51   previewed: 2026-11-24T00:37:04.233Z
  {"month":"2026-10","rows":31}
approved by: dev   approved: 2026-11-24T00:38:19.535Z   digest: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51
started: 2026-11-24T00:38:19.535Z   ended: 2026-11-24T00:38:22.620Z   duration: 3s85ms
result: {"month":"2026-10","rows":31}
trace: c1e32ab1c042a6acd856952064152d80
full logs: funcdctl job logs approved-1
```

The preview and the run's call share one trace, and the run keeps the Revision that it pinned at its preview.

### Python

The preview, in `src/main.py`:

::include[../../../../../_repos/funcd-python/examples/job-report/src/main.py#preview]

The Job `pyreport-approved`, from
[`approval.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/job/approval.yaml):

::include[../../../../../api/types/v1alpha1/examples/job/approval.yaml#pyjob]

Clone the repository and push the handler as one file, `pyreport.py`, beside its contract, as the Job page does. Save
the Job as `approval.yaml` in the same directory and replace `image` with the reference that the push prints:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/job-report
$ mkdir -p dist && cp src/main.py dist/pyreport.py && cp pyreport.contract.yaml dist/
$ funcdctl push dist/pyreport.py "oci-layout://$PWD/registry:pyreport"
oci-layout:///home/dev/funcd-python/examples/job-report/registry:pyreport@sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
```

Apply the Job and repeat `get` until `status.phase` is `Ready`. Start a run, repeat `describe` until its phase is
`AwaitingApproval`, approve the preview, answering `y`, and repeat `describe` until the phase is `Succeeded`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token apply -f approval.yaml
applied Job/pyreport-approved
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get job pyreport-approved -n default -o json
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job run pyreport-approved pyapproved-1 --input '{"month":"2026-10"}'
pyapproved-1
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job approve pyapproved-1
preview of pyapproved-1, made 2026-11-24T00:40:19.548Z:
{
  "month": "2026-10",
  "rows": 31
}
digest: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51
approve this preview? [y/N] y
approved pyapproved-1 (sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51)
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe pyapproved-1
RUN pyapproved-1   job: pyreport-approved   phase: Succeeded
revision: pyreport-approved-v1   digest: sha256:06645d8829109d45a1a31457612bf3e0d4bb82b15a2efeb2ea559ce74db0f31b
started by: dev
preview: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51   previewed: 2026-11-24T00:40:19.548Z
  {"month":"2026-10","rows":31}
approved by: dev   approved: 2026-11-24T00:40:19.822Z   digest: sha256:9863a692738143645a1b931167c58787669f0f636ccd44a7bb031b381cbb7d51
started: 2026-11-24T00:40:19.822Z   ended: 2026-11-24T00:40:22.955Z   duration: 3s133ms
result: {"month":"2026-10","rows":31}
trace: f93686542da9b87dc8214c19fb943c8b
full logs: funcdctl job logs pyapproved-1
```

## Approve from a script

With `--digest`, `funcdctl job approve` approves the digest that it names with no question. Without `--digest` and with
no terminal to ask on, it prints the preview and fails, naming the `--digest` that would approve it. Read the digest
from `funcdctl job describe <run> -o json`, at `status.preview.digest`:

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job approve approved-3 --digest sha256:343569053d58fd0a385b51dbc9a77e3d084df9c48bf6dbbcd4747df671c9b90e
approved approved-3 (sha256:343569053d58fd0a385b51dbc9a77e3d084df9c48bf6dbbcd4747df671c9b90e)
```

funcd refuses an approval that names another digest than the run's current preview with 409, naming the current one,
and changes nothing. Show the preview again with `funcdctl job describe` before approving its digest.

## Reject a run

[`funcdctl job reject`](../../tools/funcdctl/spec.md#funcdctl-job-reject) ends a run `Rejected`, with the text of
`--reason`, at most 512 bytes, as its message; its handler never runs. Only a run that has not started running can be
rejected: `Pending`, `Previewing` or `AwaitingApproval`. A rejection wins over an approval that funcd has not acted on
yet. A `Running` run is cancelled instead, with [`funcdctl job cancel`](../../tools/funcdctl/spec.md#funcdctl-job-cancel).

```console
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job reject approved-2 --reason "the November report waits for the audit"
rejected approved-2
$ funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token job describe approved-2
RUN approved-2   job: report-approved   phase: Rejected
reason: Rejected   message: the November report waits for the audit
revision: report-approved-v1   digest: sha256:aff19a7ace15032acd1aea667bd124dfd5e408ae0bd30bced659559cb86735e8
started by: dev
preview: sha256:f854072eb22b9a0fa4cc2cd4eff6188b97b7f9a4b6913500a11133d71ba9da49   previewed: 2026-11-24T00:37:33.919Z
  {"month":"2026-11","rows":30}
rejected by: dev
ended: 2026-11-24T00:37:34.095Z
trace: 1a74958182c259c71860f09977d3c3ae
full logs: funcdctl job logs approved-2
```

## Write a preview

The `preview` export takes the context and the event that the handler takes, and returns the preview as JSON. The
context's `run.phase` is `preview` in the preview's call and `run` in the run's call. A module that exports no `preview`
previews the run's input. funcd checks the event's data against the input schema of the module's contract, as it does
for the run's call, but not the preview against the output schema.

A preview's answer of up to [`job.payloadLimit`](../../running-funcd/spec.md#funcdconfigyaml) bytes is the preview; an
error ends the run `Failed` with `PreviewFailed`, and a larger answer with `PayloadLimitExceeded`. The preview runs in a
worker of its own, of the Revision that the run pins, and the Job's [`timeout`](spec.md#jobspec) bounds it as it bounds
the run's call. funcd may call the preview more than once for one run, after a restart or once it is old, so a preview
changes nothing: the run's call does the work.

## Read the approval in the handler

In the run's call, the handler's `ctx.approval`, `context.approval` in Python, holds the approval that the run runs
under: `approvedBy`, `approved_by` in Python, the subject of the credential that approved the run; `digest`, the digest
of the approved preview; and `preview`, its document. It is `null`, `None` in Python, in the preview's call and for a
run of a Job with `approval.bypass`. The shim reads the approval from funcd before it calls the handler: a run whose
approval cannot be read fails with `HandlerFailed`, and the handler is not called.

## When a preview is old

When a person approves a preview older than the Job's `approval.expiry`, or when the slot of an approved run frees after
its preview became older than that, funcd calls the preview again in that slot, with the reason `PreviewExpired`. A new
preview with the approved digest goes on to run in the same slot. A preview with another digest clears the approval,
and the run waits in `AwaitingApproval` again, for an approval of the new digest. funcd checks the expiry only at those
two moments, never on a timer: a run waits in `AwaitingApproval` with no deadline until a person acts.

## Status

`funcdctl job describe <run>` shows the phase, the reason and the message:

| Phase | Reason | Meaning |
|---|---|---|
| `Pending` | `JobNotFound`, `JobNotReady`, `Queued` | The run waits for its Job or for a slot, before its preview or after its approval. |
| `Previewing` | none, or `PreviewExpired` | A worker answers the preview. `PreviewExpired`: the approved preview was older than `approval.expiry`. |
| `AwaitingApproval` | none | The run waits for a person to approve or reject its preview. It holds no worker and no slot. |
| `Running` | none | The handler runs the approved preview. |
| `Succeeded` | none | The handler answered; its answer is the run's result. |
| `Failed` | `PreviewFailed`, `PayloadLimitExceeded` | The preview answered an error, or more than `job.payloadLimit` bytes. |
| `Rejected` | `Rejected` | A person rejected the run; the message is the reason. The handler never ran. |
| `Cancelled` | `Cancelled`, `JobDeleted` | The run was cancelled, or its Job was deleted or created again while the run waited. The handler never ran. |

The Job page's [Status](index.md#status) holds the reasons that every run shares, such as `HandlerFailed`.

## Access

A caller approves or rejects a run only when it may update the run's Job: with the built-in roles, an admin or a
developer of the namespace. A viewer's approval or rejection is refused with 403. The run records who acted, in
[`status.approvedBy`](spec.md#jobrunstatus) and `status.rejectedBy`: the subject of the credential, which grants
nothing.

## Changes and deletion

- A run that is `Previewing` when funcd stops goes back to `Pending` when funcd starts, keeping its approval, and calls
  the preview again once it has a slot.
- A run that waits, `AwaitingApproval` or approved and `Pending`, ends `Cancelled` with `JobDeleted` when its Job is
  deleted or created again, before funcd calls anything more for it.
- An edit of the Job leaves a run that has previewed on the Revision that it pinned. funcd reads the Job's current
  `approval.expiry` when it checks the run's preview.

## Examples

- [funcd-typescript/examples/job-report](https://github.com/pyvvo/funcd-typescript/tree/main/examples/job-report): the
  Job module with its `preview`, in TypeScript.
- [funcd-python/examples/job-report](https://github.com/pyvvo/funcd-python/tree/main/examples/job-report): the Job
  module with its `preview`, in Python.
