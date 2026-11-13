---
title: Configuration
description: Give a Function settings that are not secret through a ConfigMap, and read them in the handler.
---

## Usage

A ConfigMap holds settings that are not secret, such as a mode or a log level, as keys with string values. A Function
binds a ConfigMap, and each of the Function's workers gets the ConfigMap's keys as environment variables when the
worker starts, except a key that starts with `FUNCD_` and a key that a bound Secret also holds. The
[Platform page](../../../platform/workloads/configuration.md) explains how each kind of workload uses ConfigMaps and
Secrets; this page shows how a Function uses a ConfigMap.

The first example, env-echo, returns a setting that a ConfigMap gives its Function. The steps below start the example
on the local computer and call the Function once: the response contains `"config":"prod"`, the value that the
ConfigMap sets for the key `APP_MODE`.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Function.
- Node.js on the `PATH`, which runs the Function's code.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's Function names
an `image`, the bundle that `funcdctl push` writes for a platform to run. `funcdctl dev` does not use that image: it
runs the Function from its build in the folder, `env-echo.mjs`, which the repository holds already built from
`src/main.ts`. `funcdctl dev` prints the URL of each Function. The `--gport` flag sets the port of that URL, so the
commands below use port 8080.

Three resource files of the example's folder give the Function its settings:

- `configmap.yaml` defines the ConfigMap `app-config`, with the keys `APP_MODE` and `SHARED`. Each key is the name of
  an environment variable.
- `secret.yaml` defines the Secret `app-secret`, with the keys `API_KEY` and `SHARED`. A Secret holds values that must
  stay private, and its values never reach the worker's environment: the handler reads them through `ctx.secret`, as
  the [Secrets](../../../platform/services/secrets.md) page explains.
- `function.yaml` defines the Function `env-echo`. The Function declares the ConfigMap by `ref` under
  `spec.configMaps`, because the ConfigMap is a separate resource, and binds it under `spec.bindings.config`. The
  binding can name only a ConfigMap that the Function declares, or, for a Function of an App, one that the App
  declares. The Function declares and binds the Secret the same way, under `spec.secrets` and `spec.bindings.secret`.

The folder's other resource files define two Routes and the configuration of a funcd server: this page does not use
them, and `funcdctl dev` ignores the server configuration.

Two kinds of key are left out of the environment. A key that a bound Secret also holds is left out, so the handler
gets `SHARED` only from the Secret. A key that starts with `FUNCD_` is left out too, because funcd reserves that prefix
for the variables that it sets itself; funcd logs a warning for each key that it leaves out for this reason.

The [Spec](spec.md) tab describes a ConfigMap's `data` field. The Function's Spec tab describes
[`spec.configMaps`](../function/spec.md#functionspecconfigmaps) and
[`spec.bindings.config`](../function/spec.md#functionspecbindings).

### TypeScript

The ConfigMap `app-config` sets `APP_MODE` to `prod`, and `SHARED` to `from-config`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/configmap.yaml]

The Function `env-echo` declares and binds the ConfigMap `app-config` and the Secret `app-secret`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/function.yaml]

The handler reads `APP_MODE` from its environment, as `process.env.APP_MODE`, and reads `API_KEY` and `SHARED`
through `ctx.secret`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/src/main.ts]

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call the Function. The Function takes no input, so the request has no body:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

The response shows where each value comes from:

- `config` is `prod`, the value of `APP_MODE` in the ConfigMap.
- `secret` is `s3cr3t`, the value of `API_KEY` in the Secret.
- `shared` is `from-secret`, the value of `SHARED` in the Secret. In this example, the ConfigMap's value of `SHARED`,
  `from-config`, does not reach the handler's environment, because the Secret also holds `SHARED`.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Read a setting through ctx.config

A handler can read its settings through `ctx.config`, the configuration client in the context that every handler
receives, instead of reading its environment:

- `ctx.config.get(key)` returns the value of `key` when a bound ConfigMap supplies that key to the environment.
  Otherwise it returns `undefined` in TypeScript and `None` in Python: for a key that a bound Secret also holds, for a
  key that starts with `FUNCD_`, and for every other environment variable, such as `PATH`.
- `ctx.config.list()` returns the keys that the bound ConfigMaps supply to the environment, sorted.

Both methods have the same names in TypeScript and in Python. They read the worker's environment and do not call
funcd, so in TypeScript `get` returns the value itself, not a Promise as `ctx.secret.get` does.

## Share settings between Functions

Several Functions can bind the same ConfigMap: each one declares it by `ref` under `spec.configMaps`, as env-echo
does, and binds it under `spec.bindings.config`. This ConfigMap holds two settings that the Functions of an
application share:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#configmap]

A Function can also define a ConfigMap of its own under `spec.configMaps`: an entry with a `name` and its `data`, in
place of a `ref`. funcd creates that ConfigMap before the Function's first worker starts, under the name
`<name>-<hash>`, where the hash comes from the data. Its `name` can have at most 52 characters. When the Function is
applied, funcd replaces the `name` in `spec.bindings.config` with that stored name, so the stored Function shows the
hashed name. The ConfigMap belongs to the Function, and funcd deletes it with the Function.

When two ConfigMaps that a Function binds hold the same key, the worker's environment gets the value of the ConfigMap
listed later in `spec.bindings.config`, unless a bound Secret also holds that key, as `SHARED` in env-echo. The
Function `report` binds the shared ConfigMap first and a ConfigMap of its own second, so its workers get `LOG_LEVEL` set
to `debug` and `TIMEZONE` set to `Europe/Paris`:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#function]

## Changes and deletion

A worker reads its settings when it starts, and keeps them until it stops. A change to a ConfigMap that a Function
names by `ref` therefore restarts nothing: the running workers keep the values that they started with, and only a
worker that starts after the change gets the new values. Such workers start, for example, when the Function's spec
changes: funcd then starts a new revision of the Function, a new set of workers for the new spec, and moves the calls
to them once they are ready.

A change to the data of a ConfigMap that the Function defines is a change to the Function's spec. funcd creates the
ConfigMap again under a new `<name>-<hash>` name, and starts a new revision of the Function, whose workers start with
the new values. Once the Function is `Ready` again, funcd deletes the old ConfigMap, unless another resource still
names it.

funcd does not refuse the deletion of a ConfigMap that a Function binds. The Function's running workers keep serving
with the values that they started with, but funcd starts no new worker while the ConfigMap is missing. A worker that
stops is not replaced, and a new revision of the Function does not start: the Function's `RevisionReady` condition
then has the reason `ConfigResolveFailed`. Once the ConfigMap exists again, funcd starts workers again.

Deleting a Function deletes the ConfigMaps that it defines. A ConfigMap that it names by `ref` stays.

## Errors

- **A key that is not an environment variable name.** Each key of a ConfigMap consists of ASCII letters, digits and
  `_`, and does not start with a digit. `funcdctl apply` refuses a ConfigMap with any other key, and its message names
  the key, such as `spec.data key "APP-MODE" is not an env-var name`.
- **A binding to a ConfigMap that the Function does not declare.** `funcdctl apply` refuses the Function, with a
  message such as `spec.bindings.config[0]: ConfigMap "app-config" is not declared in spec.configMaps`. Declare the
  ConfigMap under `spec.configMaps`, by `ref` or by `name`.
- **A ConfigMap that does not exist.** funcd starts no worker of a Function that binds a missing ConfigMap. A Function
  with no worker running is then `Failed`, and its `Ready` condition has the reason `ConfigResolveFailed` and a message
  that names the ConfigMap. A call to the Function fails with the HTTP status 503, and the message ends with
  `function default/env-echo is Failed (ConfigResolveFailed)`. funcd checks for the ConfigMap again every
  `controller.referentPollInterval`, 2 seconds by default, and starts the Function's workers once the ConfigMap
  exists.

## Run it locally

`funcdctl dev` applies each ConfigMap file of the folder when it starts, and applies a file again when the file
changes. It keeps one worker of each Function running, and that worker keeps the values that it started with, so only
a worker that starts after the edit gets the edited value. To start one, stop `funcdctl dev` and start it again, or
rebuild the Function: `funcdctl dev` starts a new revision for each new build. A ConfigMap that the Function defines
in its own resource file needs neither, because a change to its data starts a new revision at once. When a
ConfigMap's file is removed, `funcdctl dev` deletes the ConfigMap, and the Function behaves as
[Changes and deletion](#changes-and-deletion) describes.

## Examples

- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): a Function that
  returns one setting from a ConfigMap and two values from a Secret.
