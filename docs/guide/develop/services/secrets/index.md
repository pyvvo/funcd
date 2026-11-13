---
title: Secrets
description: Create a Secret, bind it in a Function, read its values, and write its keys from the Functions that may.
---

## Usage

A Secret holds named values, such as an API key, that a Function reads but that stay out of its code and its
configuration. A Function declares and binds each Secret that it reads, and its handler reads the values through
`ctx.secret`, the Secret client in the context that every handler receives. The Function that a Secret names as its
owner, and a Function that a role allows, can also write the keys of a Secret that they bind. funcd puts no value of a
Secret in the environment of a [worker](../../../platform/workloads/index.md#runtime), where the Function's code
runs. The [Platform page](../../../platform/services/secrets.md) explains how funcd stores and checks Secrets; this
page shows how to use them.

The first example, env-echo, reads two keys of a Secret and returns their values. The steps below start the example
on the local computer and call it: the response contains `"secret":"s3cr3t"`, the value of the key `API_KEY`. Then
the steps change that value in the Secret and call the Function again: the second response contains the new value,
from the same worker, which did not restart.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Function.
- Node.js on the `PATH`, which runs the example's handler.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's Function names
an `image`, the bundle that `funcdctl push` writes for a platform to run. `funcdctl dev` does not use that image: it
runs the Function from the code in the folder, and prints the URL of each Function. The `--gport` flag sets the port
of that URL, so the commands below use port 8080.

Besides the handler's code and its build, the example's folder holds a Secret, a ConfigMap and the Function that reads
them. It also holds two Routes, which `funcdctl dev` applies but which the steps below do not use, and a server
configuration file, `funcdconfig.yaml`, which `funcdctl dev` skips. The steps below use the Secret, the ConfigMap and
the Function:

- The Secret `app-secret` holds two keys, `API_KEY` and `SHARED`, under `spec.data`. Each value is written in base64,
  and each key is an environment variable name: a letter or an underscore, then letters, digits and underscores.
- The ConfigMap `app-config` holds two settings that are not secret, `APP_MODE` and `SHARED`. funcd gives the keys of
  a bound ConfigMap to each worker as environment variables, with the exceptions that the Configuration page lists
  under [Choose a ConfigMap's keys](../../workloads/configuration/index.md#choose-a-configmaps-keys). One of them
  applies here: a key that a bound Secret also holds, such as `SHARED`, is left out.
- The Function `env-echo` **declares** the Secret under `spec.secrets`, with the keys that its code reads. The
  Function waits in `Pending` until the Secret exists and holds every declared key.
- The Function **binds** the Secret under `spec.bindings.secret`. A binding can name only a Secret that `spec.secrets`
  declares, and the binding is what allows the Function to read the Secret, as [Access](#access) explains.

The handler reads the values with two methods of `ctx.secret`:

- `get` returns the value of one key, or `undefined` (`None` in Python) when no bound Secret holds the key or when the
  key starts with `FUNCD_`.
- `list` returns every key that the bound Secrets hold, except a key that starts with `FUNCD_`, in sorted order.

The declared keys are a check, not a filter: the handler can read every key of a bound Secret, except a key that
starts with `FUNCD_`. Each call of `get` or `list` asks funcd, with no cache, so each call returns the current values.
When two bound Secrets hold the same key, the handler reads the value of the Secret that comes later in
`spec.bindings.secret`. Two other methods write keys, as [Write a key of a Secret](#write-a-key-of-a-secret) explains.

The [Spec](spec.md) tab lists the fields of a Secret, and the Function's
[Spec tab](../../workloads/function/spec.md#functionspecsecrets) lists those of `spec.secrets`. An App declares the
Secrets of its parts under its own [`spec.secrets`](../../workloads/app/spec.md#appspecsecrets), and a Workflow
declares those of its steps under its [`spec.secrets`](../../workloads/workflow/spec.md#workflowspecsecrets); each part
or step binds them as a Function does.

### TypeScript

The Secret `app-secret` is in `examples/env-echo/secret.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/secret.yaml]

The Function `env-echo`, in `examples/env-echo/function.yaml`, declares and binds both the Secret and the ConfigMap:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/function.yaml]

The handler, in `examples/env-echo/src/main.ts`, returns `APP_MODE` from its environment, and `API_KEY` and `SHARED`
from `ctx.secret.get`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/src/main.ts]

The example returns the Secret's values only to show them: a handler that serves real callers does not return a
Secret's value.

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call the Function. The Function takes no input, so the call sends no body:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

`config` comes from the ConfigMap, through the environment; `secret` and `shared` come from the Secret, through
`ctx.secret`.

To check that the handler reads the Secret on each call, change the value of `API_KEY` while `funcdctl dev` runs.
Encode the new value, `rotated`, in base64:

```console
$ printf '%s' rotated | base64
cm90YXRlZA==
```

In `examples/env-echo/secret.yaml`, replace the value of `API_KEY`, `czNjcjN0`, with `cm90YXRlZA==`, and save the
file. `funcdctl dev` applies the saved file within about a second. Call the Function again:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"rotated","shared":"from-secret"}
```

The response contains the new value, and no worker restarted: the terminal that runs `funcdctl dev` prints a
`funcd-shim: listening on` line each time a worker starts, and it printed no new one.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Write a key of a Secret

A Function writes the keys of a Secret whose `spec.owner` names it. The owner is one Function of the Secret's
namespace, and it writes only while it binds the Secret. In this example, the Function `refresher` refreshes an OAuth
token and keeps it in the Secret `oauth`, which another Function, `api`, binds and reads:

::include[../../../../../api/types/v1alpha1/examples/secrets/writers.yaml#owner]

`refresher` declares and binds `oauth` as `env-echo` declares and binds `app-secret` in the first example. Its handler
writes with two methods of `ctx.secret`, whose first argument names the Secret, one of the Function's
`spec.bindings.secret`:

- `put(binding, key, value)` sets a key to a text value, such as `ctx.secret.put("oauth", "TOKEN", token)`.
- `del(binding, key)` in TypeScript, and `delete(binding, key)` in Python, removes a key. Removing a key that the Secret
  does not hold is no error.

Both methods return nothing, and they throw an error, or raise a `RuntimeError` in Python, when funcd refuses the
write, as [Errors](#errors) lists. The next read of each Function that binds the Secret returns the change, with no
restart. A write changes keys only: it never creates or deletes the Secret itself. funcd checks a written key as it
checks the keys of an applied Secret, and a written value as a read does: the key must be an environment variable name,
and the value must be UTF-8 text without a NUL byte. A key that starts with `FUNCD_` is stored, but no read returns it.

## Let another Function write a Secret

A Secret has at most one owner. To let another Function write it too, grant that Function the built-in role
`Secret Data Writer` with a RolesAssignment. Its scope names one Secret, with the kind `Secret` and the Secret's name,
or the whole namespace, with the kind `Namespace`. This RolesAssignment lets `api` write `oauth`:

::include[../../../../../api/types/v1alpha1/examples/secrets/writers.yaml#rolesassignment]

Like the owner, `api` writes only a Secret that it binds, with the same methods. The built-in role
`Secret Data Reader` allows no write, and it lets a Function read nothing more than its bindings do: `ctx.secret` reads
only the Secrets that the Function binds, and each binding already allows the read. Both roles act only for a
Function, because funcd reads and writes a Secret for no Identity. The Security page explains how to
[choose a role](../../security-and-networking/security/index.md#choose-a-role).

## Access

A Function needs no role assignment to:

- **Read the Secrets it binds.** Each entry of `spec.bindings.secret` lets the Function read that Secret through
  `ctx.secret`. At each read, funcd asks Cedar whether the Function may perform the action `secret::read` on each
  Secret that it binds.
- **Write the Secret it owns.** The Function that `spec.owner` names can write the Secret's keys while it binds the
  Secret. At each write, funcd asks Cedar whether the Function may perform the action `secret::write` on that Secret.

Any other write needs a role. The smallest built-in role that allows writing is `Secret Data Writer`, which
[Let another Function write a Secret](#let-another-function-write-a-secret) grants. The built-in roles `Reader`,
`Contributor` and `Owner` include no Secret action. The
[Security](../../../platform/security-and-networking/security.md) page explains where funcd asks Cedar.

funcd supplies these rules as a built-in policy, which it applies in every namespace, so nothing needs to be written
or installed. In Cedar, the built-in policy reads:

```cedar title="Built-in Secrets policy"
@id("secret-read-binding")
permit(principal, action == Action::"secret::read", resource)
  when { principal has secretBindings && principal.secretBindings.contains(resource) };

@id("secret-write-base")
permit(principal, action == Action::"secret::write", resource);

@id("secret-single-writer")
forbid(principal, action == Action::"secret::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

A [Policy](../../security-and-networking/security/index.md#write-a-policy) of the namespace can deny a read or a
write that these rules allow, because a `forbid` statement overrides every `permit`. This Policy forbids the Function
`env-echo` to read the Secret `app-secret`:

::include[../../../../../api/types/v1alpha1/examples/secrets/forbid-read.yaml#policy]

While the Policy exists:

- Each `ctx.secret` read of `env-echo` fails with the HTTP status 403, and its error names the rule:
  `cedar: forbidden by policy default/forbid-app-secret#0`. A read fails even for a key that another bound Secret
  holds, because funcd reads every bound Secret for each read. Another Function that binds `app-secret` still reads it.
- Within `runtime.supervisionPeriod`, 10 seconds by default, the workers of `env-echo` stop being ready, and they do
  not restart. The Function is then `Degraded`, and each call waits for a ready worker until
  `invoke.activationTimeout`, 30 seconds by default, then fails with the HTTP status 503.

Once the Policy is deleted, the next read succeeds, and the workers are ready again within
`runtime.supervisionPeriod`, with no change to the Function.

A `forbid` of the action `secret::write` denies writes in the same way, those of the owner included: each write fails
with the HTTP status 403 and names the Policy. It does not change the Function's readiness, which checks only reads.

A Function binds only Secrets of its own namespace, and a RolesAssignment or a Policy decides only the reads and the
writes of its own namespace's Functions and Secrets.

People read and write Secrets through funcd's API, where the role of the caller's credential decides, not Cedar: an
admin, or a developer of the Secret's namespace, can read and write Secrets, and a viewer can never read one. A Policy
does not change these reads and writes. The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes
the roles under `auth.credentials`.

## Data storage

funcd stores each Secret in its metastore, the store that holds every object of the API. With `storage.mode: file`,
the default, the metastore is on disk, in the `store` folder of `storage.dataDir` unless `storage.metastoreDir` names
another folder, and the Secrets survive a restart. With `storage.mode: memory`, the Secrets are lost when funcd stops.
A key that a Function writes is stored in the same Secret, in the same way.

By default, the metastore holds the Secrets unencrypted, and with `storage.mode: file`, funcd logs a warning at start.
To encrypt them, set `secrets.encryptionKeyFile` to a file that holds a key of exactly 32 bytes: funcd then encrypts
each Secret that it stores with AES-256. funcd refuses to start when the stored Secrets were written in another way:
with another key, with a key while none is set, or without a key while one is set. The funcd
[Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each of these settings.

## Status

A Secret has no status. The Function that declares it reports a Secret that is missing, or that it cannot read, with
one of these reasons:

- `SecretNotFound`: a declared Secret does not exist. The message names it, such as
  `spec.secrets[0]: Secret "app-secret" does not exist in the namespace; waiting`.
- `SecretKeyMissing`: a declared Secret lacks a declared key, which the message names.
- `SecretResolveFailed`: a bound Secret holds a value that is not UTF-8 text, or that contains a NUL byte. Binary data
  therefore takes two base64 encodings in a manifest: the first turns it into text, and the second is the encoding
  that every value of `spec.data` has. The handler reads the text of the first encoding, and decodes it once. A value
  that a Function writes, or that `funcdctl dev` takes from the shell or from an envFile, as
  [Run it locally](#run-it-locally) explains, is stored as it is, so binary data in it is encoded once.
- `DependencyNotReady`: a Policy forbids a read, as [Access](#access) explains. The message names the binding, such as
  `secret binding "app-secret": …`.

While no worker serves, `SecretNotFound` and `SecretKeyMissing` hold the Function in `Pending`, and
`SecretResolveFailed` holds it in `Failed`, with the reason on the `Ready` condition. funcd checks a `Pending` Function
again every `controller.referentPollInterval`, 2 seconds by default, and the Function leaves `Pending` once each
declared Secret exists with every declared key.

While a worker serves, the phase stays `Ready`. A Function that runs in workers of its own can show no reason for a
while: funcd checks its Secrets again only after another change, such as an update of the Function or a worker that
stops. Once funcd has checked them, the reason is on the `RevisionReady` condition while a worker still serves.

A forbid shows on the `Ready` condition: a Function that has served becomes `Degraded` within
`runtime.supervisionPeriod`, and a Function that has not served yet stays `Deploying`.

`funcdctl get function env-echo -n default -o json` prints the phase and the conditions under `status`. With
`funcdctl dev`, add the `--server` and `--token` flags that its banner prints.

## Changes and deletion

- The Function's next read returns a changed value, with no restart, whether a person or a Function changed it, as the
  first example shows. A Lake is the exception: its engine reads its Secrets only when it starts, so a changed value
  replaces the engine, as the Lake's [Spec](../lake/spec.md#lakespecbindings) tab says.
- A person's apply of a Secret replaces its data, so a key that a Function wrote is gone after the apply unless the
  applied manifest holds it.
- When a bound Secret loses a key, `get` returns `undefined` (`None` in Python) for that key, and `list` leaves it
  out. The other keys read as before. When `spec.secrets` declares the lost key, funcd starts no new worker for the
  Function until the key is back.
- When a bound Secret is deleted, the Function's reads return no value at all, because funcd reads every bound Secret
  for each read: `get` returns `undefined` (`None`) for every key, even a key that another bound Secret still holds, and
  `list` throws an error (raises one in Python) with the HTTP status 404. env-echo then returns empty strings. funcd
  starts no new worker for the Function until the Secret exists again with every declared key.
- In both cases, a worker that serves goes on serving, and the Function stays `Ready`, as [Status](#status) explains.
- When a person names as a Secret's owner a Function that binds the Secret and shares its workers with other
  Functions, through [`spec.pooling`](../../workloads/function/spec.md#functionspecpooling), funcd moves the Function to
  other workers: an owner never shares a worker with a Function that does not own the same Secrets.
- Deleting a Function, a Workflow or an App never deletes a Secret that it declares or binds.

## Errors

- `funcdctl apply` refuses a Function whose `spec.bindings.secret` names a Secret that its `spec.secrets` does not
  declare: `spec.bindings.secret[0]: Secret "app-secret" is not declared in spec.secrets`.
- `funcdctl apply` refuses a Secret whose key is not an environment variable name, such as `api-key`:
  `spec.data key "api-key" is not an env-var name`.
- `ctx.secret.get` returns `undefined` (`None` in Python) for a key that no bound Secret holds, or that starts with
  `FUNCD_`, without an error.
- A read that a Policy forbids throws an error in TypeScript, and raises a `RuntimeError` in Python. The error's
  message holds the status 403 and the rule, such as `context.secret.get failed: 403 …` and
  `cedar: forbidden by policy default/forbid-app-secret#0`. A handler that does not catch the error, such as
  env-echo's, makes the call fail with the HTTP status 500.
- A write that funcd refuses throws an error, or raises a `RuntimeError` in Python, whose message, such as
  `context.secret.put failed: 403 …`, holds one of these HTTP statuses:
  - 403: the first argument names a Secret that the Function does not bind, such as
    `function "refresher" in "default" does not bind Secret "app-secret"`; or the Function is neither the owner nor a
    writer that a role names, `cedar: forbidden by policy builtin/secret-single-writer`; or a Policy forbids the
    write.
  - 404: the bound Secret does not exist.
  - 400: the key is not an environment variable name, or the value is not UTF-8 text or contains a NUL byte. Nothing
    is written.
  - 413: the request is over 1 MiB, the value with the JSON that carries it.
  - 409: other writes changed the Secret during each of 5 attempts. Nothing is written, and the call can be made
    again.

## Run it locally

`funcdctl dev` keeps its Secrets in memory, so they are gone when it stops. With the `--persist` flag, it keeps them
between runs in the `metastore` folder of its persist folder: `.funcd-dev`, which it creates in the current folder,
unless `--persist-to` names another folder. The Secrets' values are stored there unencrypted, because `funcdctl dev`
sets no encryption key. To remove them, delete the persist folder after `funcdctl dev` stops: `.funcd-dev`, or the
folder that `--persist-to` names. Deleting it clears all the state that the local platform stored, its metastore, KV,
blob, SQL, lake and volume data, Secrets included. Keep the persist folder out of version control, for example with a
`.funcd-dev/` line in `.gitignore`: the example's repository does not ignore it, so `git status` lists it.

`funcdctl dev` also creates each Secret that a Function of the folder declares and that no file of the folder
defines. It takes the value of each declared key from the shell's environment, else from the file that `dev.envFile`
names in `funcdctl.config.yaml`, in the current folder, else from `dev.env` in that file. A key that none of them
holds stops `funcdctl dev` before the platform starts, with an error that names the Secret and the key. A value can
thus stay out of the folder and out of its repository. For example, the first example also runs without its
`secret.yaml`, with values from the shell. In the repository's root folder, run:

```console
$ rm examples/env-echo/secret.yaml
$ API_KEY=from-shell SHARED=also-from-shell funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call the Function:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"from-shell","shared":"also-from-shell"}
```

The worker does not get these values in its environment either: it inherits only a few variables of the shell, such
as `PATH` and `HOME`. After the example stops, `git checkout examples/env-echo/secret.yaml` brings the file back.

Writes work under `funcdctl dev` as on a platform: the owner and the writers that a RolesAssignment names write, and
`funcdctl dev` grants no Secret role of its own. When it applies the folder again after a saved file, it applies each
Secret of the folder again too, which replaces the keys that a Function wrote.

## Examples

- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): a Function that
  reads two keys of a Secret through `ctx.secret`, and a setting of a ConfigMap from its environment.
