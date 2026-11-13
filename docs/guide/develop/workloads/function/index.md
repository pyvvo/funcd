---
title: Function
description: Run a Function, call it, read its status and its logs, call one Function from another, serve HTTP, and scale to zero.
---

## Usage

A Function is code that funcd runs when the Function is called. For each call, a
[worker](../../../platform/workloads/index.md#runtime) of the Function, a process that runs its code, calls the code's
**handler** with a context, through which the handler logs and reaches other Functions and its stores, and an
**event**, which holds the call's JSON body in `event.data` (`event["data"]` in Python); the handler's return value
becomes the response. The [Platform page](../../../platform/workloads/function.md) explains who calls a Function and
what it binds; this page shows how to run a Function that takes events, call it and read its state, and
[Serve HTTP requests](#serve-http-requests) shows a Function that takes HTTP requests instead.

The first example, log-burst, writes a burst of log lines at each call: its handler logs through `console` in
TypeScript or through the `logging` module in Python, and writes one more line to standard output and one to standard
error. It returns `emitted`, the number of its `console` or `logging` calls, which leaves out the two direct lines.
The steps below start the example on the local computer, call it once, and read the Function's status: the response
contains `"emitted":115` in TypeScript and `"emitted":100` in Python, and the status shows the [phase](#status)
`Ready`.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Function.
- The runtime of the example's language on the `PATH`: Node.js for TypeScript, or Python 3.12 or later as `python3`.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's manifest names
an `image`, the [artifact](../../../platform/workloads/artifacts.md) that a platform pulls: the code, its **contract**,
which describes the input that the Function accepts and the result that it returns, and its runtime, which
`funcdctl push` packs together. `funcdctl dev` does not use that image: it runs the Function from the code in the
folder, and reads the Function's contract from the folder's `log-burst.contract.yaml` file. `funcdctl dev`
prints the URL of each Function, and the address of its control plane, which a `funcdctl` command reaches with
`--server` and the token `funcd-dev-token`. The `--gport` flag sets the port of each Function's URL, and `--cport` the
port of the control plane, so the commands below use the ports 8080 and 8081.

The example's manifest and contract decide how the Function runs and what it accepts:

- [`runtime`](spec.md#functionspec) names the runtime that runs the code, `nodejs22` or `python314`, and
  [`handler`](spec.md#functionspec) names the export that the worker calls, `handle`.
- [`replicas`](spec.md#functionspec) and [`scaling.minReplicas`](spec.md#functionspecscaling), both `1`, keep one
  worker running from the start, even while no call comes, so the Function never scales to zero. `funcdctl dev`
  gives every Function these two values, whatever its manifest says.
- The contract, `log-burst.contract.yaml` in the example's folder
  ([TypeScript](https://github.com/pyvvo/funcd-typescript/blob/main/examples/log-burst/log-burst.contract.yaml),
  [Python](https://github.com/pyvvo/funcd-python/blob/main/examples/log-burst/log-burst.contract.yaml)), names the
  runtime and holds the JSON Schema of the call's data and of the handler's result. funcd
  checks the data of each call against it before a worker gets the call, and refuses data that does not match with
  the HTTP status 422. The example accepts a JSON object whose fields are all optional, so the call below sends an
  empty object, `{}`; a call with no body gets 422.

funcd runs each version of a Function's spec as a **revision**, named after the Function and a number that grows by
one at each change of the spec, such as `log-burst-1`. The status that the steps below read names the revision that
takes the calls; [Status](#status) explains the phase and the conditions, and
[Changes and deletion](#changes-and-deletion) explains how a new revision takes over. The [Spec](spec.md) tab
describes every field of a Function and of its status.

### TypeScript

The Function `log-burst`, in `examples/log-burst/burst.yaml`, runs on `nodejs22`:

::include[../../../../../_repos/funcd-typescript/examples/log-burst/burst.yaml]

The handler, in `examples/log-burst/src/main.ts`, logs 100 items with `console.log`, a warning with `console.warn` for
every tenth item and an error with `console.error` for every 25th item, then a summary with `console.log`. It also
writes one line to standard output and one to standard error, and returns the number of `console` calls that it made.
`funcdctl dev` runs its build, `log-burst.mjs`, which the repository holds already built:

::include[../../../../../_repos/funcd-typescript/examples/log-burst/src/main.ts]

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
`funcdctl dev` prints each line with the Function's name and the line's severity: `INFO` for `console.log` and for
standard output, `WARN` for `console.warn`, and `ERROR` for `console.error` and for standard error.

Read the Function's status:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default -o json
```

The command prints the Function as JSON. Under `status`, `"phase"` is `"Ready"`, and the conditions `ShapeValid`,
`Ready` and `RevisionReady` have the status `"True"`. `"replicas": 1` counts the ready worker, and
`"servingRevision": "log-burst-1"` names the revision whose worker takes the calls.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

### Python

The Function `log-burst`, in `examples/log-burst/handler.yaml`, runs on `python314`:

::include[../../../../../_repos/funcd-python/examples/log-burst/handler.yaml]

The handler, in `examples/log-burst/src/main.py`, logs through the standard `logging` module: 90 lines with `info`, 7
with `warning` and 3 with `error`. It also prints one line to standard output and one to standard error, and returns
the number of `logging` calls that it made. `funcdctl dev` runs `src/main.py` as it is, because the example's folder
has the Function's name:

::include[../../../../../_repos/funcd-python/examples/log-burst/src/main.py]

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

The terminal that runs `funcdctl dev` prints each line with the Function's name and the line's severity: `INFO` for
`info` and for standard output, `WARN` for `warning`, and `ERROR` for `error` and for standard error.

Read the Function's status:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default -o json
```

The command prints the Function as JSON. Under `status`, `"phase"` is `"Ready"`, and the conditions `ShapeValid`,
`Ready` and `RevisionReady` have the status `"True"`. `"replicas": 1` counts the ready worker, and
`"servingRevision": "log-burst-1"` names the revision whose worker takes the calls.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Read a Function's logs

funcd keeps the log lines of each Function's workers as records, each with a time and a severity:

- each call of `console.log`, `console.warn` or `console.error` in TypeScript, or of the `logging` module in Python,
  as the first example shows;
- each line that the code writes to standard output, as `INFO`, or to standard error, as `ERROR`;
- each call of [`ctx.log`](spec.md#handler-api), the log method of the context that every handler receives, as
  `INFO`: in TypeScript, `ctx.log` writes as `console.log` does, and in Python, it prints to standard output.

`funcdctl logs` prints the records of one Function, the most recent 1000 by default. A record shows there once funcd
has written it to storage: a funcd server does that within `funclog.segmentMaxAge`, 10 seconds by default, and
`funcdctl dev` within a second. While the first example runs, this command prints the records of the severity `ERROR`
and above. After one call of the TypeScript example, these are the line written to standard error and the four
errors; the times differ at each run:

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

## Call another Function

A Function calls another Function of its [namespace](../../../platform/tenancy-and-limits/namespaces.md) over a
**link**, which it declares under
[`spec.bindings.links`](spec.md#functionspecbindingslinks): an `alias`, the name that its code uses, and a `target`,
the Function that the link calls. The handler calls [`ctx.invoke`](spec.md#handler-api) with the alias and an input
for the target, and gets the target's result. funcd checks the input against the target's contract, as for a call from
outside funcd. When the call fails, `ctx.invoke` throws an error, or raises a `RuntimeError` in Python, whose message
holds the HTTP status and the answer, such as the target's 422 for an input that does not match its contract.

The target of a link must exist in the namespace when the calling Function is applied, and a link cannot close a
cycle, such as a link from a Function to itself. A target that serves HTTP, as
[Serve HTTP requests](#serve-http-requests) describes, is called with [`ctx.fetch`](spec.md#handler-api) instead,
which only TypeScript has; `ctx.invoke` refuses such a target.

The fn-to-fn example holds two Functions: `front` calls `greeter` over a link and returns greeter's greeting. The
example's `front.yaml` declares a link with the alias `greeter` to the Function `greeter`, as this manifest of
`front` does:

::include[../../../../../api/types/v1alpha1/examples/function/link.yaml#link]

The complete file,
[api/types/v1alpha1/examples/function/link.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/function/link.yaml),
also holds the Function `greeter`, the link's target. The example exists in TypeScript only; in Python, the method
has the same name, `invoke`.

### TypeScript

The handler of `front`, in `examples/fn-to-fn/src/front/main.ts`, calls `context.invoke` with the alias `greeter` and
the input `{ data: { name } }`, where `name` comes from its own input. funcd builds the target's event from that input
as from the body of an outside call: an object with a top-level `data` or `specversion` key is the event itself, so
`greeter` gets `{ name }` as its `event.data`, and any other input becomes `event.data` as it is. The handler of
`greeter`, in
[`src/greeter/main.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/fn-to-fn/src/greeter/main.ts),
returns a greeting for that name:

::include[../../../../../_repos/funcd-typescript/examples/fn-to-fn/src/front/main.ts]

The folder also holds two Functions that this section does not use, `fanout` and `hold`, and a configuration file
for a funcd server, which `funcdctl dev` skips. From the root folder of the funcd-typescript repository, cloned as in
the first example, start the example:

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
and its body, and its handler answers it as a web server does. Such a Function runs on a Node.js runtime, never shares
a worker with other Functions, and funcd checks no contract on its requests. Only a call through funcd's
[edge](../../../platform/security-and-networking/networking.md), the part of funcd that receives the calls from
outside funcd, and another Function's `ctx.fetch` reach it.

### TypeScript

The http-api example's Function `devices-api`, in `examples/http-api/devices.yaml`, serves HTTP:

::include[../../../../../_repos/funcd-typescript/examples/http-api/devices.yaml]

Its handler, in `examples/http-api/src/main.ts`, wraps a web app with `http`, from the package `@funcd-dev/shim`.
The app, in [`src/app.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/http-api/src/app.ts), uses
the Hono framework and keeps its devices in the worker's memory:

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

The folder's Route `api`, in `route.yaml`, serves the same app at `/api`, so `http://127.0.0.1:8080/api/devices`
returns the same list. The [Networking](../../../platform/security-and-networking/networking.md) page explains how a
Route reaches a Function, and the Networking [Spec](../../security-and-networking/networking/spec.md#route) tab
describes a Route's fields.

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
  starts a worker again.
- More than one replica gives spare workers, not more capacity: every call goes to the lowest-numbered worker that
  listens.
- `funcdctl dev` sets `replicas` and `minReplicas` to 1, so a Function never goes `Idle` there.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes `invoke.reclaimInterval` and
`invoke.activationTimeout`.

## Access

A Function needs no role assignment to call the Functions that its links name. Each entry of `spec.bindings.links`
lets the Function call its target through `ctx.invoke` or `ctx.fetch`. These two methods reach only the targets of
the caller's own links: funcd looks the alias up in those links before it asks Cedar, the policy engine that decides
what a Function may do, so a call whose alias no link declares fails with the HTTP status 403, whatever role the
Function has. The error that `ctx.invoke` throws then
holds the response's JSON body, with a message such as `caller default/front declares no link \"nope\" (no link is no
grant)`. For each call over a
link, funcd then asks Cedar whether the caller may perform the action `link::invoke` on the target. The
[Security](../../../platform/security-and-networking/security.md) page explains where funcd asks Cedar.

funcd supplies the link rule as a built-in policy, which it applies in every namespace, so nothing needs to be
written or installed. In Cedar, the built-in policy reads:

```cedar title="Built-in link policy"
@id("link-invoke-binding")
permit(principal, action == Action::"link::invoke", resource)
  when { principal has links && principal.links.contains(resource) };
```

A [Policy](../../security-and-networking/security/spec.md#policy) of the namespace can forbid a call that a link
allows, because a `forbid` statement overrides every `permit`. Such a call fails with the HTTP status 403.

From outside funcd, a client calls a Function through funcd's edge, at `/function/<name>` or through a Route. The
Function's [`spec.ingress`](spec.md#functionspecingress), the defaults of its Namespace and the Routes decide whether
an address answers and whether the caller must authenticate, as the
[Networking](../../../platform/security-and-networking/networking.md) page explains.

## Limits and quotas

- The body of a call through the edge holds at most 1 MiB. A larger body gets the HTTP status 413.
- A call through the edge must start its response within the Function's [`timeout`](spec.md#functionspec), or
  within `invoke.defaultTimeout`, 60 seconds by default, when `timeout` is not set. The time counts from the call's
  arrival, and a cold start counts. Past it, the caller gets the HTTP status 504, but the handler goes on, so a caller
  that retries can repeat its effects. A TypeScript handler runs until it ends. A Python handler gets an exception
  one second after the deadline. A handler that blocks its worker, such as a TypeScript loop that never awaits or a
  Python `time.sleep`, makes the worker fail its liveness check, the periodic check by which funcd finds a worker that
  no longer answers; in Python, from 11 seconds after the deadline. funcd then replaces the worker once the check has
  failed for `runtime.livenessTimeout`, 30 seconds by default. A blocked member of a TypeScript pool is ended by the
  pool's host instead. A handler reads the time left before its deadline with
  [`ctx.remainingMs`](spec.md#handler-api), `remaining_ms` in Python, which returns `null` (`None`) when the call has
  no deadline.
- A call over a link waits for the target's result until the link's `timeout`, 30 seconds by default and at most 5
  minutes, a cold start of the target included; past it, the call fails with the HTTP status 503. Its input holds at
  most 1 MiB, and a larger one gets the HTTP status 413. A result larger than 1 MiB gets 500 when the target's
  contract checks results, because funcd checks at most 1 MiB of a result; when the contract accepts any result, and
  through `ctx.fetch`, it gets 413.
- At most `invoke.maxNestedInFlight` calls over links to one Function, 10 by default, can be in flight at once. One
  more fails at once with the HTTP status 429.
- [`resources`](spec.md#functionspecresources) sets the CPU and the memory of each worker of a Function that runs in
  workers of its own. A field left empty takes the server's `functions.defaultResources`, 1 CPU and 512 MiB by
  default. Only the containerd runtime enforces them, and it kills a worker that uses more memory than its limit.
- A log record longer than `funclog.maxRecordBytes`, 64 KiB by default, is cut and marked as truncated.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each server setting above, and this
page's [Spec](spec.md) tab gives the range of each field.

## Status

`funcdctl get function <name> -n <namespace> -o json` prints a Function's status, as the first example shows. Its
phase is one of these:

- `Pending`: the Function waits, such as for a store or a Secret that it binds and that does not exist yet. The
  reason of the `Ready` condition names what it waits for, such as `KVStoreNotFound` or `SecretNotFound`, and the
  Function leaves `Pending` by itself once the wait is over.
- `Deploying`: workers are starting and none serves yet, such as for a new Function or one that a call wakes.
- `Ready`: a worker is ready, and calls reach it.
- `Idle`: no worker runs, and the next call starts one.
- `Degraded`: the Function has served, but no worker is ready now, such as while funcd replaces a worker that
  stopped. Calls wait for a ready worker.
- `Failed`: the Function cannot run, such as when its handler cannot load, or when its first worker does not become
  ready within `runtime.bootTimeout`, 1 minute by default (`ShapeInvalid`, with a message such as
  `the handler did not become ready within 1m0s` for the second case); when its runtime is missing
  (`RuntimeUnavailable`); when its image cannot be resolved (`ArtifactUnresolved`); or when a worker cannot start
  (`StartFailed`). funcd refuses its calls at once with the HTTP status 503. funcd tries a worker that cannot start
  again after a growing wait, but a `ShapeInvalid` Function stays `Failed` until its spec changes.

Three conditions explain the phase:

- `Ready` is `True` while a worker serves. Otherwise its reason and its message say why, such as `NoReplicas` for an
  `Idle` Function, `ShimNotReady` while a worker starts, or `CrashLoopBackOff` when a worker stops before it listens
  and funcd waits before it starts the worker again.
- `RevisionReady` says whether the revision of the latest spec serves. While that revision starts beside the serving
  one, it is `False` with the reason `Progressing`. [Changes and deletion](#changes-and-deletion) explains revisions.
- `ShapeValid` says whether a worker of the latest spec has loaded the handler. It is `False` with the reason
  `ShapeInvalid` when the code has no export of the name that `handler` gives, when the code fails to load, or when
  no worker became ready within `runtime.bootTimeout`, 1 minute by default.

A Function whose `spec.ingress` sets a host or a path also has the condition `IngressReady`, which reports its own
Route. `status.replicas` counts the ready workers, `status.servingRevision` names the revision whose workers take the
calls, and `status.currentRevision` names the revision of the latest spec.

## Changes and deletion

Each change to a Function's spec creates a new revision, a read-only copy of the spec, whose number is one higher,
such as `log-burst-2` after `log-burst-1`. funcd starts the new revision's workers beside the serving ones,
and moves the calls to them once every new worker is ready, even when only `replicas` or `scaling` changed. Each old
worker stops once it is idle, but no sooner than `runtime.handOutSettle` after the switch, 2 seconds by default. A
worker that is still busy `runtime.drainGrace` after the switch, 30 seconds by default, stops with its calls cut.
`status.drainingRevision` names the old revision while its workers stop, and
`funcdctl get revisions -n <namespace>` lists the revisions.

When the new revision cannot load its handler, the serving revision keeps the calls and the phase. `RevisionReady` is
then `False`, with the reason `ShapeInvalid` and the message
`the current revision could not load its handler; the serving revision keeps the calls`.

A revision keeps the digest that the Function's `image` named when funcd created the revision. New code pushed under
the same tag therefore runs only after the next change to the spec: give the new code a new tag, or set
[`imageDigest`](spec.md#functionspec).

A delete removes the Function at once. funcd stops its workers without waiting for their calls, so the calls in
flight are cut, and the Function's address stops answering. The Function's revisions go with it, and so do a Lake and
a [ConfigMap](../configuration/index.md) that the Function declares by `name`. A KVStore, Bucket or SQLDatabase that
the Function declares by `name` stays, with its data, unless its `deletion` is `delete`.

funcd refuses to delete a Function that another Function's link targets. While the fn-to-fn example runs:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete function greeter -n default
funcdctl: sdk: admission.link-deletion-protection: function "greeter" is linked by "front" (alias "greeter"); remove the link first
```

## Errors

A caller of a Function meets these HTTP statuses, each with a message in the body:

- **400** when the body of a call to a Function that takes events is not JSON: the message is
  `invalid CloudEvent JSON`. A Function that takes HTTP requests gets the body as it is.
- **401** or **403** when the Function's address asks for authentication, through
  [`spec.ingress.auth`](spec.md#functionspecingressauth) or the Namespace's defaults. On a server whose
  `server.auth.edge` is `true`, a call without a valid bearer token or `X-Api-Key` header gets 401, and a caller whose
  token does not allow it to get the namespace's Functions gets 403. With `server.auth.edge` at its default, `false`,
  the server has no authenticator, and every call to such an address gets 401 with the message
  `authentication required but no authenticator is configured`.
- **404** when no Function answers at the address, such as `function default/nope is not exposed by name` for a
  Function that does not exist.
- **413** when the body holds more than 1 MiB.
- **422** when the call's data does not match the Function's contract: the message is
  `event data does not match the input contract`, and `details` names each part at fault.
- **500** when the handler throws an error, or raises an exception in Python, with the error's message; also when its
  result does not match the contract, with the message `handler result does not match the output contract`.
- **503** when the Function is `Failed`, with a message such as `function default/log-burst is Failed (ShapeInvalid)`,
  or when no worker became ready within `invoke.activationTimeout`:
  `function default/log-burst did not become ready within 30s`.
- **504** when the response did not start within the call's deadline, with a message such as
  `default/log-burst did not start its response within 1m0s (invoke.defaultTimeout)`.

Over a link, `ctx.invoke` fails with 403 for an alias that no link of the caller declares, 409 for a target that
serves HTTP, 429 past `invoke.maxNestedInFlight`, and the target's own status, such as 422, when the target refuses
the call.

`funcdctl apply` refuses a Function:

- whose link names a Function that does not exist in the namespace, with a message such as
  `spec.bindings.links[greeter].target "greeter" does not exist in namespace "default"`. Apply the target first.
- whose links would close a cycle, with a message such as
  `spec.bindings.links would create a dependency cycle (front → greeter → front)`.
- whose `protocol` is `http` on a runtime other than Node.js, with a message such as
  `spec.protocol http needs a node runtime, not "python314"`.

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for a Function:

- It runs each Function from its build in the folder, which it finds by the Function's name: `<name>.mjs`, in the
  folder or in its `dist` folder, for Node.js. For Python, it runs the entry that `[tool.funcd-bundle.functions]` of
  the folder's `pyproject.toml` names, else `src/main.py` when the folder has the Function's name, else `<name>.py`.
  It reads the contract from `<name>.contract.yaml` or `<name>.contract.json`, and stops when the contract and the
  manifest name different runtimes.
- It replaces the Function's `image` with the path of that build, and its `imageDigest` with a digest of the build. A
  saved change to the build or to the contract therefore starts a new revision within about a second.
- It sets `replicas` and `scaling.minReplicas` to 1, so each Function keeps one worker running and never goes `Idle`.
- It runs each worker as a process of the local user, with no isolation and no limit on its CPU or its memory, and it
  prints each log line in its terminal.
- It runs the namespace `default` only, and stops on a resource of another namespace.
- Its edge has no authenticator, as on a server whose `server.auth.edge` is `false`, so it answers 401 to every call
  to a Function whose address asks for authentication, with the message
  `authentication required but no authenticator is configured`.

`funcdctl dev` keeps its state in memory, where it is lost when `funcdctl dev` stops. With the `--persist` flag, it
keeps its state between runs in `.funcd-dev`, a folder that it creates in the current folder, or in the folder that
`--persist-to` names: the metastore, which holds the objects that it applied, the KV, blob, SQL, Lake and volume data,
and the copies of the builds that it runs. To start again from nothing, delete that folder after `funcdctl dev` stops. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/log-burst): a Function that writes a burst of
  log lines and returns how many `console` or `logging` calls it made.
- [fn-to-fn in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/fn-to-fn): a Function that
  calls another Function over a link.
- [http-api in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/http-api): a Function that
  serves HTTP with a web framework.
