---
title: Configuration
description: Give a Function settings that are not secret through a ConfigMap, and read them in the handler.
---

## Usage

A [ConfigMap](../../../platform/workloads/configuration.md) holds settings that are not secret as string keys. Each
[worker](../../../platform/workloads/index.md#runtime) of a Function that binds it gets its keys as environment
variables when it starts. A Function declares a ConfigMap under
[`spec.configMaps`](../function/spec.md#functionspecconfigmaps) and binds it under
[`spec.bindings.config`](../function/spec.md#functionspecbindings); a binding names only a ConfigMap that its Function,
or its App, declares.

Install the [dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`. The
env-echo example returns one ConfigMap setting and two Secret values.

### TypeScript

The ConfigMap, in `configmap.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/configmap.yaml]

The Function, in `function.yaml`, also binds the Secret
[`app-secret`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/env-echo/secret.yaml):

::include[../../../../../_repos/funcd-typescript/examples/env-echo/function.yaml]

Start the example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call it:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

The handler reads `APP_MODE` from its environment, and the Secret through
[`ctx.secret`](../../services/secrets/index.md):

::include[../../../../../_repos/funcd-typescript/examples/env-echo/src/main.ts]

`shared` comes from the Secret: a key that a bound Secret also holds does not reach the environment. Press Ctrl-C in
the first terminal to stop.

## Choose a ConfigMap's keys

A key is an environment variable name: ASCII letters, digits and `_`, not starting with a digit. A worker gets every
key of its bound ConfigMaps, except:

- a key that starts with `FUNCD_`, which funcd reserves, with a warning in its log;
- a key that a bound Secret also holds, which the handler reads through `ctx.secret`;
- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` of a Function that binds a Bucket while the
  [S3 gateway](../../services/bucket/index.md) runs, which funcd sets itself.

## Read a setting through ctx.config

[`ctx.config.get(key)`](spec.md#handler-api) returns a key that a bound ConfigMap delivers, and `undefined` or `None`
for any other key, such as `PATH`; `ctx.config.list()` returns those keys, sorted. Both read the worker's environment,
so in TypeScript `get` returns the value itself, not a Promise.

## Define the settings in the Function

An entry of `spec.configMaps` with a `name` of at most 52 characters and its `data` defines a ConfigMap of the
Function's own:

::include[../../../../../api/types/v1alpha1/examples/configuration/defined.yaml]

funcd creates it as `<name>-<hash>`, the hash of its data, before the first worker starts, and deletes it with the
Function. Two Functions that define the same `name` and `data` would share that name: the second waits in `Pending`
with `StoreNotOwned`. Share settings through a ConfigMap of their own instead.

## Share settings between Functions

Several Functions declare the same ConfigMap by `ref` and bind it:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#configmap]

## Override a shared setting

When two bound ConfigMaps hold the same key, the one listed later in `spec.bindings.config` wins:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#function]

## Status

A ConfigMap has no status; the Function that binds or defines it reports a problem.
`funcdctl get function env-echo -n default -o json` prints it; with `funcdctl dev`, add the `--server` and `--token`
flags of its banner.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| `Failed` | `ConfigResolveFailed` | No worker runs, and funcd starts none: a bound ConfigMap is missing, or holds a value that is not UTF-8 text or that holds a NUL byte. The message of the `Ready` condition names the ConfigMap, and the key for a bad value. Calls fail with the HTTP status 503. When the ConfigMap is missing, create it: funcd checks for it every `controller.referentPollInterval`, 2 seconds by default, and starts the workers once it exists. |
| `RevisionReady` condition | `ConfigResolveFailed` | The latest revision cannot start for one of the reasons of the `Failed` row, while a worker of the serving revision keeps the calls: the phase stays `Ready`. Fix or create the ConfigMap: funcd checks again every `runtime.supervisionPeriod`, 10 seconds by default. Once no worker is left, the Function is `Failed`. |
| `Pending` | `StoreNotOwned` | A ConfigMap that the Function defines exists under its stored name and does not belong to the Function, as [Define the settings in the Function](#define-the-settings-in-the-function) describes. Declare the shared settings by `ref`. |
| Another phase or reason | | The [Function's Status](../function/index.md#status) explains it. |

## Changes and deletion

- A worker keeps the settings that it started with, so a changed ConfigMap reaches only workers that start later,
  such as those of a [new revision](../function/index.md#changes-and-deletion).
- Changing a defined ConfigMap changes the Function's spec: funcd creates it under a new hash and starts a new
  revision, then deletes the old ConfigMap unless another resource names it.
- Deleting a bound ConfigMap changes nothing at once. funcd notices when the Function next needs a worker, and then
  starts none until the ConfigMap exists again.
- Deleting a Function deletes the ConfigMaps that it defines, not those that it names by `ref`.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no build of "<name>"` | Build the Function, and name its build `<name>.mjs`, after the Function, in the folder or its `dist` folder. The message names the paths that it looked at. |

## Run it locally

`funcdctl dev` applies a ConfigMap file when it starts and when the file changes, but a running worker keeps its
values: restart `funcdctl dev` after editing a ConfigMap named by `ref`. A defined ConfigMap needs no restart, since its
change starts a new revision.

## Examples

- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): one setting from a
  ConfigMap and two values from a Secret.
