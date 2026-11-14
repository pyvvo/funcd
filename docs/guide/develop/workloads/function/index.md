---
title: Function
description: Run a Function, call it, read its status and its logs, call one Function from another, serve HTTP, and scale to zero.
---

## Usage

A Function is code that funcd runs when the Function is called: the code's **handler** answers each call. This example
runs a Function that takes events, calls it once and reads its status, whose phase is then `Ready`;
[Serve HTTP requests](#serve-http-requests) shows a Function that takes HTTP requests instead. See
[how Functions work](../../../platform/workloads/function.md) for who calls a Function and what it binds.

The example, log-burst, writes a burst of log lines at each call and returns `emitted`, the number of its `console`
calls in TypeScript or `logging` calls in Python. It also writes one line to standard output and one to standard
error, which `emitted` does not count.

Install the [dev build of funcdctl](../../../get-started/install.md), Git, curl, and the runtime of the example's
language: Node.js for TypeScript, or Python 3.12 or later for Python. Put the runtime on the `PATH`, Python as
`python3`.

`funcdctl dev` starts a local platform, applies the resource files of one folder to it, and prints the URL of each
Function. It runs each Function from the code in the folder, not from the manifest's `image`, the
[artifact](../../../platform/workloads/artifacts.md) that [`funcdctl push`](../../tools/funcdctl/spec.md#funcdctl-push)
packs from the code, its runtime and its [contract](#write-a-handler-and-its-contract) for a platform to run.
`--gport` sets the port of the URLs, and `--cport` the port of the control plane, which a `funcdctl` command reaches
with `--server` and the token `funcd-dev-token`.

### TypeScript

The manifest, `examples/log-burst/burst.yaml`, declares the Function `log-burst`:

::include[../../../../../_repos/funcd-typescript/examples/log-burst/burst.yaml]

The handler, [`src/main.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/log-burst/src/main.ts),
logs through `console`, and `funcdctl dev` runs its build, `log-burst.mjs`, which the repository already holds.

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/log-burst --gport 8080 --cport 8081
```

In another terminal, call the Function with an empty JSON object:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"emitted":115}
```

The handler made 115 `console` calls: 100 items, 10 warnings, 4 errors and the summary. The terminal that runs
`funcdctl dev` prints each line with the Function's name and the line's severity: `INFO` for `console.log` and
standard output, `WARN` for `console.warn`, and `ERROR` for `console.error` and standard error.

Read the Function's status:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default -o json
```

Under `status`, `"phase"` is `"Ready"`, and the conditions `ShapeValid`, `Ready` and `RevisionReady` have the status
`"True"`. `"replicas": 1` counts the ready [worker](../../../platform/workloads/index.md#runtime), the process that
runs the code, and `"servingRevision": "log-burst-1"` names the [revision](#changes-and-deletion) whose worker takes
the calls.

Keep the example running for [Read a Function's logs](#read-a-functions-logs), which stops it.

### Python

The manifest, `examples/log-burst/handler.yaml`, declares the Function `log-burst`:

::include[../../../../../_repos/funcd-python/examples/log-burst/handler.yaml]

The handler, [`src/main.py`](https://github.com/pyvvo/funcd-python/blob/main/examples/log-burst/src/main.py), logs
through the standard `logging` module, and `funcdctl dev` runs `src/main.py` as it is, because the example's folder
has the Function's name.

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ funcdctl dev examples/log-burst --gport 8080 --cport 8081
```

In another terminal, call the Function with an empty JSON object:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"emitted":100}
```

The handler made 100 `logging` calls: 90 with `info`, 7 with `warning` and 3 with `error`. The terminal that runs
`funcdctl dev` prints each line with the Function's name and the line's severity: `INFO` for `info` and standard
output, `WARN` for `warning`, and `ERROR` for `error` and standard error.

Read the Function's status:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default -o json
```

Under `status`, `"phase"` is `"Ready"`, and the conditions `ShapeValid`, `Ready` and `RevisionReady` have the status
`"True"`. `"replicas": 1` counts the ready [worker](../../../platform/workloads/index.md#runtime), the process that
runs the code, and `"servingRevision": "log-burst-1"` names the [revision](#changes-and-deletion) whose worker takes
the calls.

Keep the example running for [Read a Function's logs](#read-a-functions-logs), which stops it.

## Read a Function's logs

`funcdctl logs` prints the log records of one Function, the most recent 1000 by default. funcd keeps each log line of
a Function's workers as a record with a time and a severity: each `console` call in TypeScript or `logging` call in
Python, as the first example shows; each line written to standard output, as `INFO`, or to standard error, as
`ERROR`; and each call of [`ctx.log`](spec.md#handler-api), the log method of the context that every handler
receives, as `INFO`.

A record shows in `funcdctl logs` only after funcd has written it to storage: a funcd server writes the records in
segments up to `funclog.segmentMaxAge` old, 10 seconds by default, and `funcdctl dev` in segments up to about a second
old, so repeat the command until the records appear. While the first example runs, this command prints the records of
the severity `ERROR` and above. After one call of the TypeScript example, these are the line written to standard
error and the four errors; the times differ at each run:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token logs log-burst -n default --severity error
2026-11-13T15:04:11.432Z [ERROR] 0 burst stderr default
2026-11-13T15:04:11.433Z [ERROR] 0 item failed
2026-11-13T15:04:11.433Z [ERROR] 0 item failed
2026-11-13T15:04:11.433Z [ERROR] 0 item failed
2026-11-13T15:04:11.433Z [ERROR] 0 item failed
```

Each line shows the record's time, its severity, the number of the worker that wrote it, and its message. `--since`
keeps the records of a recent period, such as `15m`, and `-o wide` adds each record's attributes, such as the
arguments of a `console` call. The [reference of `funcdctl logs`](../../tools/funcdctl/spec.md#funcdctl-logs) lists
its flags.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Write a handler and its contract

A Function's code exports a handler, which answers each call, and its **contract** describes the input that the
Function accepts and the result that it returns. The manifest's [`handler`](spec.md#functionspec) names the export,
and [`runtime`](spec.md#functionspec) names the runtime that runs the code.

For each call, a worker calls the handler with two arguments. The first is the context, through which the handler
logs and reaches other Functions and its stores. The second is the **event**: the call's JSON body reaches the
handler as `event.data` (`event["data"]` in Python), unless the body is itself an event, as
[Call another Function](#call-another-function) explains. The handler's return value becomes the response. The
[Spec](spec.md) tab describes every field of a Function and of its status, and the methods of the context.

The contract names the runtime and holds the JSON Schema of the input and of the result. funcd refuses a call whose
data does not match the contract with the HTTP status 422, before the call reaches the handler, and answers with the
HTTP status 500 when the handler's result does not match. The contract of log-burst, the `log-burst.contract.yaml`
file of the example's folder
([TypeScript](https://github.com/pyvvo/funcd-typescript/blob/main/examples/log-burst/log-burst.contract.yaml),
[Python](https://github.com/pyvvo/funcd-python/blob/main/examples/log-burst/log-burst.contract.yaml)), accepts a JSON
object whose fields are all optional, so the first example's call sends an empty object, `{}`; a call with no body
gets 422. [Run it locally](#run-it-locally) says where `funcdctl dev` finds a Function's build and its contract.

### TypeScript

The first example's handler is `examples/log-burst/src/main.ts`:

::include[../../../../../_repos/funcd-typescript/examples/log-burst/src/main.ts]

To run a handler of your own, bundle it with
[`@funcd-dev/vite-plugin`](https://github.com/pyvvo/funcd-typescript#bundle-your-functions): `vite build` writes
`dist/<name>.mjs`, with the handler's dependencies inlined, and copies `<name>.contract.yaml` beside it, where
`funcdctl dev` finds them.

### Python

The first example's handler is `examples/log-burst/src/main.py`:

::include[../../../../../_repos/funcd-python/examples/log-burst/src/main.py]

## Call another Function

A Function calls another Function of its [namespace](../../../platform/tenancy-and-limits/namespaces.md) over a
**link**, which it declares under [`spec.bindings.links`](spec.md#functionspecbindingslinks): an `alias`, the name
that its code uses, and a `target`, the Function that the link calls. The handler calls
[`ctx.invoke`](spec.md#handler-api) with the alias and an input for the target, and gets the target's result.

- funcd builds the target's event from the input as from the body of a call from outside funcd: an object with a
  top-level `data` or `specversion` key is the event itself, and any other input becomes `event.data` as it is.
- funcd checks the input against the target's contract. When the call fails, `ctx.invoke` throws an error, or raises
  a `RuntimeError` in Python, whose message holds the HTTP status and the response, such as the target's 422.
- The target must exist in the namespace when the calling Function is applied, so apply the target first. A link
  cannot close a cycle, such as a link from a Function to itself.
- A target that serves HTTP, as [Serve HTTP requests](#serve-http-requests) describes, is called with
  [`ctx.fetch`](spec.md#handler-api) instead, which only TypeScript has. `ctx.invoke` refuses such a target.

In the fn-to-fn example, `front` calls `greeter` over a link and returns the greeting of `greeter`. The example's
`front.yaml` declares the link with the alias `greeter`, as this manifest of `front` does. The
[complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/function/link.yaml) also holds
the Function `greeter`, the link's target:

::include[../../../../../api/types/v1alpha1/examples/function/link.yaml#link]

The example exists in TypeScript only. In Python, the method has the same name, `invoke`.

### TypeScript

The handler of `front`, in `examples/fn-to-fn/src/front/main.ts`, calls `context.invoke` with the alias `greeter`
and the input `{ data: { name } }`, so `greeter` gets `{ name }` as its `event.data`. The
[handler of `greeter`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/fn-to-fn/src/greeter/main.ts)
returns a greeting for that name:

::include[../../../../../_repos/funcd-typescript/examples/fn-to-fn/src/front/main.ts]

The folder also holds two Functions that this task does not use, `fanout` and `hold`, and a configuration file for a
funcd server, which `funcdctl dev` skips. From the root folder of the funcd-typescript repository, cloned as in the
first example, start the example:

```console
$ funcdctl dev examples/fn-to-fn --gport 8080 --cport 8081
```

In another terminal, call `front` with a name:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/front
{"via":"front","greeting":"Hello, ada!"}
```

The contract of `greeter` requires a `name`, and the contract of `front` does not. A call to `front` without a name
therefore reaches `greeter`, which refuses it with 422. `context.invoke` throws an error that holds the 422, and
`front`, which does not catch it, answers with the HTTP status 500 and the error's message:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/front
{"error":"context.invoke(\"greeter\") failed: 422 {\"error\":\"event data does not match the input contract\",\"details\":[{\"path\":\"/name\",\"message\":\"missing property 'name'\"}]}"}
```

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Serve HTTP requests

By default, a Function's handler gets the data of each call and returns a result. A Function whose
[`protocol`](spec.md#functionspec) is `http` gets each HTTP request instead, with its method, its path, its headers
and its body, and its handler answers it as a web server does. Such a Function runs on a Node.js runtime and never
shares a worker with other Functions, and funcd checks no contract on its requests. Only a call through funcd's
[edge](../../../platform/security-and-networking/networking.md), the part of funcd that receives the calls from
outside funcd, and another Function's `ctx.fetch` reach it.

### TypeScript

The http-api example's Function `devices-api`, in `examples/http-api/devices.yaml`, serves HTTP:

::include[../../../../../_repos/funcd-typescript/examples/http-api/devices.yaml]

Its handler, in `examples/http-api/src/main.ts`, wraps a web app with `http`, from the package `@funcd-dev/shim`. The
app, in [`src/app.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/http-api/src/app.ts), uses the
Hono framework and keeps its devices in the worker's memory:

::include[../../../../../_repos/funcd-typescript/examples/http-api/src/main.ts]

The app gets the part of the path after the Function's address: a request for `/function/devices-api/devices`
reaches the app as `/devices`, and the request's `X-Forwarded-Prefix` header holds the part that funcd removed,
`/function/devices-api`. From the root folder of the funcd-typescript repository, start the example:

```console
$ funcdctl dev examples/http-api --gport 8080
```

In another terminal, create a device, then list the devices:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"truck-7"}' http://127.0.0.1:8080/function/devices-api/devices
{"id":"1","name":"truck-7"}
$ curl -s http://127.0.0.1:8080/function/devices-api/devices
[{"id":"1","name":"truck-7"}]
```

The folder's Route `api`, in `route.yaml`, which `funcdctl dev` applies with the Function, serves the same app at
`/api`, so `http://127.0.0.1:8080/api/devices` returns the same list. The
[Networking](../../../platform/security-and-networking/networking.md) page explains how a Route reaches a Function,
and the Networking [Spec](../../security-and-networking/networking/spec.md#route) tab describes a Route's fields.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Scale a Function to zero

A Function that sets neither [`replicas`](spec.md#functionspec) nor
[`scaling.minReplicas`](spec.md#functionspecscaling) runs no worker until its first call. That call starts a worker,
waits until the worker is ready, then reaches the handler: this wait is a **cold start**. With
[`scaling.idleTimeout`](spec.md#functionspecscaling), funcd also stops the Function's workers once the Function has
served no call for that long: its phase becomes `Idle`, and the next call starts a worker again. Without
`idleTimeout`, a worker that has started keeps running. The Function `pdf-render` stops its workers after 5 minutes
without a call:

::include[../../../../../api/types/v1alpha1/examples/function/function.yaml#function]

- funcd looks for idle Functions every `invoke.reclaimInterval`, 30 seconds by default, so a Function can keep its
  workers up to that long past its `idleTimeout`. A call in flight keeps the Function from going `Idle`.
- A call that waits for a worker gets the HTTP status 503 once `invoke.activationTimeout`, 30 seconds by default, has
  passed. The calls that arrive while a worker starts share that wait.
- `scaling.minReplicas` above 0 keeps that many workers running, and the Function never goes `Idle`. `replicas` above
  0 with `minReplicas` 0 starts workers before the first call, but once the Function has gone `Idle`, only a call
  starts a worker again. The first example sets both to 1, so one worker runs from the start, even while no call
  comes.
- More than one replica gives spare workers, not more capacity: every call goes to the lowest-numbered worker that
  listens.
- `funcdctl dev` sets `replicas` and `minReplicas` to 1, so a Function never goes `Idle` there.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes `invoke.reclaimInterval` and
`invoke.activationTimeout`.

## Access

A Function needs no role assignment to call the Functions that its links name. Each entry of `spec.bindings.links`
lets the Function call its target through `ctx.invoke` or `ctx.fetch`. For each call over a link, funcd asks Cedar,
the policy engine that decides what a Function may do, whether the caller may perform the action `link::invoke` on
the target. The [Security](../../../platform/security-and-networking/security.md) page explains where funcd asks
Cedar.

funcd supplies the link rule as a built-in policy, which it applies in every namespace, so nothing needs to be
written or installed. In Cedar, the built-in policy reads:

```cedar title="Built-in link policy"
@id("link-invoke-binding")
permit(principal, action == Action::"link::invoke", resource)
  when { principal has links && principal.links.contains(resource) };
```

`ctx.invoke` and `ctx.fetch` reach only the targets of the caller's own links: funcd looks the alias up in those links
before it asks Cedar, so a call whose alias no link declares fails with the HTTP status 403, whatever role the
Function has. The error that `ctx.invoke` throws then holds the response's JSON body, with a message such as
`caller default/front declares no link \"nope\" (no link is no grant)`. A
[Policy](../../security-and-networking/security/spec.md#policy) of the namespace can forbid a call that a link
allows, because a `forbid` statement overrides every `permit`. Such a call fails with the HTTP status 403 too.

A client outside funcd calls a Function through funcd's edge, at `/function/<name>` or through a Route. The
Function's [`spec.ingress`](spec.md#functionspecingress), the defaults of its Namespace and the Routes decide whether
an address answers and whether the caller must authenticate, as the
[Networking](../../../platform/security-and-networking/networking.md) page explains. An address whose
[`auth.mode`](spec.md#functionspecingressauth) is `authenticated` needs `server.auth.edge: true` on the server, and
the bearer token or `X-Api-Key` header of a credential that may get the namespace's Functions: a call without a valid
credential gets the HTTP status 401, and a call whose credential may not get them gets 403. With `server.auth.edge`
at its default, `false`, and under `funcdctl dev`, the edge has no authenticator, so every call to such an address
gets 401.

## Limits and quotas

- The body of a call through the edge holds at most 1 MiB. A larger body gets the HTTP status 413.
- A call through the edge must start its response within the Function's [`timeout`](spec.md#functionspec), or
  within `invoke.defaultTimeout`, 60 seconds by default, when `timeout` is not set. The time counts from the call's
  arrival, and a cold start counts. Past it, the caller gets the HTTP status 504, but the handler goes on, so a caller
  that retries can repeat its effects. A TypeScript handler runs until it ends, and a Python handler gets an
  exception one second after the deadline. A handler reads the time left before its deadline with
  [`ctx.remainingMs`](spec.md#handler-api), `remaining_ms` in Python, which returns `null` (`None`) when the call has
  no deadline.
- A handler that blocks its worker, such as a TypeScript loop that never awaits or a Python `time.sleep`, makes the
  worker fail its liveness check, the periodic check by which funcd finds a worker that no longer answers; in Python,
  from 11 seconds after the deadline. funcd then replaces the worker once the check has failed for
  `runtime.livenessTimeout`, 30 seconds by default. A blocked member of a TypeScript
  [pool](spec.md#functionspecpooling) is ended by the pool's host instead.
- A call over a link waits for the target's result until the link's
  [`timeout`](spec.md#functionspecbindingslinks), 30 seconds by default and at most 5 minutes, a cold start of the
  target included. Past it, the call fails with the HTTP status 503. Its input holds at most 1 MiB, and a larger one
  gets 413. funcd checks at most 1 MiB of a result, so a larger result gets 500 when the target's contract checks
  results; when the contract accepts any result, and through `ctx.fetch`, it gets 413.
- At most `invoke.maxNestedInFlight` calls over links to one Function, 10 by default, can be in flight at once. One
  more fails at once with the HTTP status 429.
- [`resources`](spec.md#functionspecresources) sets the CPU and the memory of each worker of a Function that runs in
  workers of its own. A field left empty takes the server's `functions.defaultResources`, 1 CPU and 512 MiB by
  default. Only the containerd runtime enforces them, and it kills a worker that uses more memory than its limit.
- A log record longer than `funclog.maxRecordBytes`, 64 KiB by default, is cut and marked as truncated.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each server setting above, and this
page's [Spec](spec.md) tab gives the range of each field.

## Status

Read `status.phase` and the conditions with the status command of the first example. The condition `Ready` says
whether a worker serves, `RevisionReady` whether the revision of the latest spec serves, and `ShapeValid` whether a
worker of the latest spec has loaded the handler. A condition that is not `True` names a reason, and its message,
when it has one, says more. Each phase row gives a reason that `Ready` has in that phase.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| `Pending` | What the Function waits for, such as `KVStoreNotFound` or `SecretNotFound` | The Function waits, such as for a store or a Secret that it binds and that does not exist yet. It leaves `Pending` by itself once the wait is over. |
| `Deploying` | `ShimNotReady` | Workers are starting, and none serves yet, such as for a new Function or one that a call wakes. |
| `Ready` | | A worker is ready, and calls reach it. |
| `Idle` | `NoReplicas` | No worker runs. The next call starts one. |
| `Degraded` | `Restarting` | The Function has served, but no worker is ready now, such as while funcd replaces a worker that stopped. Calls wait for a ready worker. |
| `Failed` | `ShapeInvalid` | The handler cannot load, or the first worker did not become ready within `runtime.bootTimeout`, 1 minute by default. The Function stays `Failed` until its spec changes. |
| `Failed` | `RuntimeUnavailable` | The Function's runtime is missing. |
| `Failed` | `ArtifactUnresolved` | The image cannot be resolved. |
| `Failed` | `StartFailed` | A worker cannot start. funcd tries again after a growing wait. |
| `Failed` | Another reason, such as `NoMatchingPlatform`, `ContractMissing`, `ContractInvalid` or `ConfigResolveFailed` | The Function cannot run. The message of the `Ready` condition says why. |
| `Ready` condition | `CrashLoopBackOff` | A worker stopped before it listened, and funcd waits before it starts the worker again. |
| `RevisionReady` condition | `Progressing` | The revision of the latest spec is starting beside the serving one. |
| `RevisionReady` condition | `ShapeInvalid` | The revision of the latest spec cannot load its handler. After a change, the serving revision keeps the calls and the phase. |
| `ShapeValid` condition | `ShapeInvalid` | No worker of the latest spec has loaded the handler: the code has no export that `handler` names, the code fails to load, or no worker became ready within `runtime.bootTimeout`. The message gives the load error, or `the handler did not become ready within 1m0s` in the last case. |
| `IngressReady` condition | | Reports the Function's own Route. Only a Function whose `spec.ingress` sets a host or a path has it. |

funcd refuses the calls of a `Failed` Function at once with the HTTP status 503. `status.currentRevision` names the
revision of the latest spec, and the [status reference](spec.md#functionstatus) describes the other fields.

## Changes and deletion

- funcd runs each version of a Function's spec as a **revision**, a read-only copy of the spec named after the
  Function and a number that grows by one at each change, such as `log-burst-2` after `log-burst-1`.
  `funcdctl get revisions -n <namespace>` lists the revisions.
- funcd starts the new revision's workers beside the serving ones, and moves the calls to them once every new worker
  is ready, even when only `replicas` or `scaling` changed. Each old worker stops once it is idle, but no sooner than
  `runtime.handOutSettle` after the switch, 2 seconds by default. A worker that is still busy `runtime.drainGrace`
  after the switch, 30 seconds by default, stops with its calls cut. `status.drainingRevision` names the old revision
  while its workers stop.
- A new revision that cannot load its handler takes no call: the serving revision keeps the calls and the phase.
- A revision keeps the digest that the Function's `image` named when funcd created the revision. New code pushed under
  the same tag therefore runs only after the next change to the spec: give the new code a new tag, or set
  [`imageDigest`](spec.md#functionspec).
- A delete removes the Function at once. funcd stops its workers without waiting for their calls, so the calls in
  flight are cut, and the Function's address stops answering. The Function's revisions go with it, and so do a Lake
  and a [ConfigMap](../configuration/index.md) that the Function declares by `name`. A KVStore, Bucket or SQLDatabase
  that the Function declares by `name` stays, with its data, unless its `deletion` is `delete`.
- funcd refuses to delete a Function that another Function's link targets. Remove the link first.

## CLI errors

`funcdctl dev` stops at its start, before it serves any Function, when it meets one of these local errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `no build of "<name>"` or `no contract file for "<name>"` | Build the Function, or name its build and its contract file after it, where [Run it locally](#run-it-locally) says that `funcdctl dev` looks. The message names the paths that it looked at. |
| A Function's manifest and contract file `disagree on the runtime or protocol`. | Name the same `runtime` and `protocol` in both files. |
| A resource is in another namespace: `funcdctl dev runs namespace default only`. | Set the resource's namespace to `default`. |

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for a Function:

- It runs each Function from its build in the folder, which it finds by the Function's name: `<name>.mjs`, in the
  folder or in its `dist` folder, for Node.js. For Python, it runs the entry that `[tool.funcd-bundle.functions]` of
  the folder's `pyproject.toml` names, else `src/main.py` when the folder has the Function's name, else `<name>.py`.
  It reads the contract from `<name>.contract.yaml` or `<name>.contract.json`.
- It replaces the Function's `image` with the path of that build, and its `imageDigest` with a digest of the build. A
  saved change to the build or to the contract therefore starts a new revision within about a second.
- It sets `replicas` and `scaling.minReplicas` to 1, so each Function keeps one worker running and never goes `Idle`.
- It runs each worker as a process of the local user, with no isolation and no limit on its CPU or its memory, and it
  prints each log line in its terminal.
- It runs the namespace `default` only.
- Its edge has no authenticator, as [Access](#access) explains.

`funcdctl dev` keeps its data in memory, where it is lost when `funcdctl dev` stops. It keeps only a record of the
processes that it starts, so that it can stop them after a crash, in `funcd/dev/<id>/process` under the user's cache
folder, `<id>` naming the project folder. With the `--persist` flag, it keeps its state between runs in `.funcd-dev`, a
folder that it creates in the current folder, or in the folder that `--persist-to` names: the metastore, which holds the
objects that it applied, the KV, blob, SQL, Lake and volume data, and the copies of the builds that it runs. To start
again from nothing, delete that folder after `funcdctl dev` stops. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/log-burst): a Function that writes a burst of
  log lines and returns how many `console` or `logging` calls it made.
- [fn-to-fn in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/fn-to-fn): a Function that
  calls another Function over a link.
- [http-api in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/http-api): a Function that
  serves HTTP with a web framework.
