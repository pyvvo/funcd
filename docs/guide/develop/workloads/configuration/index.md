---
title: Configuration
description: Give a Function settings that are not secret through a ConfigMap, and read them in the handler.
---

## Usage

A ConfigMap holds settings that are not secret, such as a mode or a log level, as keys with string values. A Function
binds a ConfigMap, and each [worker](../../../platform/workloads/index.md#runtime) of the Function, a process that runs
its code, gets the ConfigMap's keys as environment variables when it starts, except the few keys that
[Choose a ConfigMap's keys](#choose-a-configmaps-keys) lists.

The [Platform page](../../../platform/workloads/configuration.md) explains how each kind of workload uses ConfigMaps
and Secrets; this page shows how a Function uses a ConfigMap.

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
  stay private.
- `function.yaml` defines the Function `env-echo`. The Function declares the ConfigMap by `ref` under
  `spec.configMaps`, because the ConfigMap is a separate resource, and binds it under `spec.bindings.config`. The
  binding can name only a ConfigMap that the Function declares, or, for a Function of an App, one that the App
  declares. The Function declares the Secret under `spec.secrets`, by its `name` and the keys that its code reads, and
  binds it under `spec.bindings.secret`.

The folder's other resource files define two Routes and the configuration of a funcd server: this page does not use
them, and `funcdctl dev` ignores the server configuration.

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

## Choose a ConfigMap's keys

Each key of a ConfigMap is the name of an environment variable, as [Errors](#errors) details. A worker gets every key
of the ConfigMaps that its Function binds, with three exceptions:

- A key that starts with `FUNCD_` is left out, because funcd reserves that prefix for the variables that it sets
  itself. funcd logs a warning for each key that it leaves out this way.
- A key that a bound Secret also holds is left out. The handler reads the Secret's value through
  [`ctx.secret`](../../services/secrets/index.md) instead, as the first example does with `SHARED`.
- While funcd's [S3 gateway](../../services/bucket/index.md) runs, a Function that binds a Bucket gets the
  `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` that funcd issues it, in place of a ConfigMap's values for these
  two keys.

## Read a setting through ctx.config

A handler can read its settings through `ctx.config`, the configuration client in the context that every handler
receives, instead of reading its environment:

- `ctx.config.get(key)` returns the value that `key` has in the worker's environment, when a bound ConfigMap holds the
  key and no exception of [Choose a ConfigMap's keys](#choose-a-configmaps-keys) leaves it out. For any other key,
  such as `PATH`, it returns `undefined` in TypeScript and `None` in Python.
- `ctx.config.list()` returns the keys that `get` returns a value for, sorted.

Both methods have the same names in TypeScript and in Python. They read the worker's environment and do not call
funcd, so in TypeScript `get` returns the value itself, not a Promise as `ctx.secret.get` does. The
[Spec](spec.md#handler-api) tab lists both methods in each language.

## Define the settings in the Function

A Function can define a ConfigMap of its own under `spec.configMaps`: an entry with a `name` and its `data`, in place
of a `ref`. Its `name` can have at most 52 characters. The Function `billing` defines the ConfigMap
`billing-settings` and binds it, so its workers get `CURRENCY` set to `EUR` and `INVOICE_DAY` set to `1`:

::include[../../../../../api/types/v1alpha1/examples/configuration/defined.yaml]

funcd creates that ConfigMap before the Function's first worker starts, under the name `<name>-<hash>`, where the hash
comes from the data, such as `billing-settings-<hash>`. When the Function is applied, funcd replaces the `name` in
`spec.bindings.config` with that stored name, so the stored Function shows the hashed name. The ConfigMap belongs to
the Function, and funcd deletes it with the Function.

Two Functions that define a ConfigMap with the same `name` and the same `data` would share one stored name. funcd
creates that ConfigMap for the first Function only. The second one waits in `Pending`, with the reason
`StoreNotOwned` and a message such as
`spec.configMaps[0]: ConfigMap "billing-settings-<hash>" exists and is not this Function's; bind it with ref`. To share
settings, define them in one ConfigMap of their own, as the next section shows.

## Share settings between Functions

Several Functions can bind the same ConfigMap: each one declares it by `ref` under `spec.configMaps`, as env-echo
does, and binds it under `spec.bindings.config`. This ConfigMap holds two settings that the Functions of an
application share:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#configmap]

## Override a shared setting

When two ConfigMaps that a Function binds hold the same key, the worker gets the value of the ConfigMap listed later in
`spec.bindings.config`, within the exceptions of [Choose a ConfigMap's keys](#choose-a-configmaps-keys). The Function
`report` binds the shared ConfigMap first and a ConfigMap of its own second, so its workers get `LOG_LEVEL` set to
`debug` and `TIMEZONE` set to `Europe/Paris`:

::include[../../../../../api/types/v1alpha1/examples/configuration/configmap.yaml#function]

## Status

A ConfigMap has no status. A Function reports a problem with a ConfigMap that it binds or defines in its phase and its
conditions: `Ready`, and `RevisionReady` for its latest revision. `funcdctl get function env-echo -n default -o json`
prints them under `status`. With `funcdctl dev`, add the `--server` and `--token` flags that its banner prints.

- `ConfigResolveFailed`: a bound ConfigMap is missing, or holds a value that a worker cannot get, as
  [Errors](#errors) describes.
- `StoreNotOwned`: a ConfigMap that the Function defines already exists under its stored name, and does not belong to
  the Function, as [Define the settings in the Function](#define-the-settings-in-the-function) describes.

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

funcd does not refuse the deletion of a ConfigMap that a Function binds, and the deletion changes nothing at once: the
Function stays `Ready`, and its running workers keep serving with the values that they started with. funcd finds the
ConfigMap missing only when the Function next needs a new worker, such as when a worker stops or the Function's spec
changes. A Function that shares its workers with other Functions, through
[`spec.pooling`](../function/spec.md#functionspecpooling), is the exception: funcd finds the missing ConfigMap at the
Function's next check. funcd then starts no new worker while the ConfigMap is missing:

- While a worker still serves, the Function stays `Ready`, and its `RevisionReady` condition has the reason
  `ConfigResolveFailed`.
- Once no worker is left, the Function is `Failed`, as [Errors](#errors) describes.

Once the ConfigMap exists again, funcd starts workers again. It checks for the ConfigMap every
`runtime.supervisionPeriod`, 10 seconds by default, while a worker still serves, and every
`controller.referentPollInterval`, 2 seconds by default, once none is left.

Deleting a Function deletes the ConfigMaps that it defines. A ConfigMap that it names by `ref` stays.

## Errors

- **A key that is not an environment variable name.** Each key of a ConfigMap consists of ASCII letters, digits and
  `_`, and does not start with a digit. `funcdctl apply` refuses a ConfigMap with any other key, and its message names
  the key, such as `spec.data key "APP-MODE" is not an env-var name`.
- **A binding to a ConfigMap that the Function does not declare.** `funcdctl apply` refuses the Function, with a
  message such as `spec.bindings.config[0]: ConfigMap "app-config" is not declared in spec.configMaps`. Declare the
  ConfigMap under `spec.configMaps`, by `ref` or by `name`.
- **A missing ConfigMap.** funcd starts no worker of a Function that binds a missing ConfigMap. A Function with no
  worker running is then `Failed`, and its `Ready` condition has the reason `ConfigResolveFailed` and a message that
  names the ConfigMap. A call to the Function fails with the HTTP status 503, and the message ends with
  `function default/env-echo is Failed (ConfigResolveFailed)`. funcd checks for the ConfigMap again every
  `controller.referentPollInterval`, 2 seconds by default, and starts the Function's workers once the ConfigMap
  exists.
- **A value that is not UTF-8 text, or that holds a NUL byte.** A worker cannot get such a value in its environment,
  so funcd starts no worker of a Function that binds the ConfigMap: the Function is `Failed`, with the reason
  `ConfigResolveFailed` and a message that names the ConfigMap and the key.
- **A defined ConfigMap that another Function already defines.** The Function waits in `Pending` with the reason
  `StoreNotOwned`, as [Define the settings in the Function](#define-the-settings-in-the-function) describes.

## Run it locally

`funcdctl dev` applies each ConfigMap file of the folder when it starts, and applies a file again when the file
changes. It keeps one worker of each Function running, and that worker keeps the values that it started with, so it
does not see an edit of a ConfigMap that the Function names by `ref`. To load the edited values, stop `funcdctl dev`
and start it again. A ConfigMap that the Function defines in its own resource file needs no restart, because a change
to its data starts a new revision at once. When a ConfigMap's file is removed, `funcdctl dev` deletes the ConfigMap,
and the Function keeps serving, as [Changes and deletion](#changes-and-deletion) describes.

## Examples

- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): a Function that
  returns one setting from a ConfigMap and two values from a Secret.
