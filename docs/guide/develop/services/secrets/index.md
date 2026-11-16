---
title: Secrets
description: Create a Secret, bind it in a Function, read its values, and control which Functions can change its values.
---

## Usage

[Secrets](../../../platform/services/secrets.md) store named values such as API keys. Declare and bind a Secret to a Function, then read its values through `ctx.secret`.

Install the [dev build of funcdctl](../../../get-started/install.md), Git, curl and Node.js.

### TypeScript

Run the [env-echo example](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo):

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call the Function:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

The example returns dummy Secret values for demonstration.

Encode a new value for `API_KEY`:

```console
$ printf '%s' rotated | base64
cm90YXRlZA==
```

In `examples/env-echo/secret.yaml`, replace `API_KEY`'s value with `cm90YXRlZA==`. Save, wait about a second, and call the Function again:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"rotated","shared":"from-secret"}
```

The updated value is available without restarting the worker. Press Ctrl-C in the first terminal to stop.

The Secret (`secret.yaml`):

::include[../../../../../_repos/funcd-typescript/examples/env-echo/secret.yaml]

The Function:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/function.yaml]

The handler (`src/main.ts`):

::include[../../../../../_repos/funcd-typescript/examples/env-echo/src/main.ts]

See [Spec](spec.md#handler-api) for method details.

## Write a key of a Secret

A Function can update a bound Secret if it owns the Secret or has the `Secret Data Writer` role. The secret-writer example writes, reads and deletes `TOKEN`.

Writes change keys in an existing Secret. They do not create or delete the Secret itself.

### TypeScript

If the repository has not already been cloned:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
```

From the repository root, start the example:

```console
$ funcdctl dev examples/secret-writer --gport 8080
```

In another terminal, write `TOKEN`, then delete it:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"put","value":"token-v2"}' http://127.0.0.1:8080/function/token-owner
{"token":"token-v2"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"delete"}' http://127.0.0.1:8080/function/token-owner
{"token":null}
```

Press Ctrl-C in the first terminal to stop.

The Secret:

::include[../../../../../_repos/funcd-typescript/examples/secret-writer/secret.yaml#secret]

The Function:

::include[../../../../../_repos/funcd-typescript/examples/secret-writer/token-owner.yaml#function]

The handler:

::include[../../../../../_repos/funcd-typescript/examples/secret-writer/src/token-owner/main.ts#handler]

### Python

Put Python 3.12 or later on the `PATH` as `python3`. From the parent folder of the funcd-typescript checkout, run:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ funcdctl dev examples/secret-writer --gport 8080
```

In another terminal, write `TOKEN`, then delete it:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"put","value":"token-v2"}' http://127.0.0.1:8080/function/py-token-owner
{"token":"token-v2"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"delete"}' http://127.0.0.1:8080/function/py-token-owner
{"token":null}
```

Press Ctrl-C in the first terminal to stop.

The Secret:

::include[../../../../../_repos/funcd-python/examples/secret-writer/secret.yaml#secret]

The Function:

::include[../../../../../_repos/funcd-python/examples/secret-writer/py-token-owner.yaml#function]

The handler:

::include[../../../../../_repos/funcd-python/examples/secret-writer/src/py-token-owner/main.py#handler]

## Let another Function write a Secret

Restart the example from its repository root to restore the initial Secret. The first write is denied. Applying the writer role allows the next write without a restart.

### TypeScript

```console
$ funcdctl dev examples/secret-writer --gport 8080 --cport 8081
```

In another terminal, from the same repository folder:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"value":"token-v2"}' http://127.0.0.1:8080/function/token-user
{"token":"token-v1","error":"context.secret.put failed: 403 {\"type\":\"urn:funcd:problem:forbidden\",\"title\":\"Forbidden\",\"status\":403,\"detail\":\"secrets.CheckWrite: write secret \\\"demo-token\\\" in \\\"default\\\" denied: cedar: forbidden by policy builtin/secret-single-writer\"}\n"}
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f examples/secret-writer/fixtures/grant.yaml
applied RolesAssignment/token-user-writes
$ curl -s -X POST -H 'content-type: application/json' -d '{"value":"token-v2"}' http://127.0.0.1:8080/function/token-user
{"token":"token-v2"}
```

Press Ctrl-C in the first terminal to stop.

The RolesAssignment:

::include[../../../../../_repos/funcd-typescript/examples/secret-writer/fixtures/grant.yaml#grant]

The handler:

::include[../../../../../_repos/funcd-typescript/examples/secret-writer/src/token-user/main.ts#handler]

### Python

```console
$ funcdctl dev examples/secret-writer --gport 8080 --cport 8081
```

In another terminal, from the same repository folder:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"value":"token-v2"}' http://127.0.0.1:8080/function/py-token-user
{"token":"token-v1","error":"context.secret.put failed: 403 {\"type\":\"urn:funcd:problem:forbidden\",\"title\":\"Forbidden\",\"status\":403,\"detail\":\"secrets.CheckWrite: write secret \\\"py-demo-token\\\" in \\\"default\\\" denied: cedar: forbidden by policy builtin/secret-single-writer\"}\n"}
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f examples/secret-writer/fixtures/grant.yaml
applied RolesAssignment/py-token-user-writes
$ curl -s -X POST -H 'content-type: application/json' -d '{"value":"token-v2"}' http://127.0.0.1:8080/function/py-token-user
{"token":"token-v2"}
```

Press Ctrl-C in the first terminal to stop.

The RolesAssignment:

::include[../../../../../_repos/funcd-python/examples/secret-writer/fixtures/grant.yaml#grant]

The handler:

::include[../../../../../_repos/funcd-python/examples/secret-writer/src/py-token-user/main.py#handler]

## Access

- A Function can read the Secrets it binds (`secret::read`). The declared keys check readiness; they do not restrict which keys the binding exposes.
- A Function can write a bound Secret whose `spec.owner` names it (`secret::write`). Other writers need `Secret Data Writer`, scoped to the Secret or its namespace.
- `Secret Data Reader` adds no access beyond a binding. These data roles apply to Functions. The general roles `Reader`, `Contributor` and `Owner` include no Secret actions.

See the complete RolesAssignment in [Let another Function write](#let-another-function-write-a-secret).

funcd supplies this Cedar policy in every namespace:

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

A namespace Policy can forbid a read or write even when ownership, a binding or a role allows it. A forbidden read affects the Function's readiness. A forbidden write does not. See [Security](../../security-and-networking/security/index.md#write-a-policy).

This Policy forbids `env-echo` to read `app-secret`:

::include[../../../../../api/types/v1alpha1/examples/secrets/forbid-read.yaml#policy]

A Function binds Secrets in its own namespace. For a Workflow step, ownership names the invoked Function: `<workflow>-<step>` for an inline step, or the existing Function for a referenced step.

An [App](../../workloads/app/spec.md#appspecsecrets) or [Workflow](../../workloads/workflow/spec.md#workflowspecsecrets) declares Secrets on the parent resource. Its Functions bind them.

People access Secrets through the control-plane API using their credential's role. An admin or a developer of the namespace can read and write them. A viewer cannot read them. Cedar does not decide these API requests.

## Limits and quotas

A write's JSON body, including the value, can hold at most 1 MiB. A larger write changes nothing. Deletes send no body.

## Data storage

Secrets live in funcd's metastore. They survive a restart with `storage.mode: file`, the default, and are lost with `storage.mode: memory`.

Secrets are stored unencrypted by default. Set `secrets.encryptionKeyFile` to a file containing exactly 32 bytes to encrypt them with AES-256. funcd refuses to start if the stored Secrets do not match the configured encryption mode or key. See [server configuration](../../running-funcd/spec.md#funcdconfigyaml).

## Status

A Secret has no status. Check the Function that uses it:

```console
$ funcdctl get function env-echo -n default -o json
```

For a local platform, add the `--server` and `--token` flags printed by `funcdctl dev`.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| `Pending` | `SecretNotFound` | While no worker serves: a declared Secret does not exist. The message names it. |
| `Pending` | `SecretKeyMissing` | While no worker serves: a declared Secret lacks a declared key, which the message names. |
| `Failed` | `SecretResolveFailed` | While no worker serves: a bound Secret holds a value that is not UTF-8 text, or that contains a NUL byte. |
| `Degraded` | `DependencyNotReady` | A Policy forbids a read, and the Function has served. The message names the binding, such as `secret binding "app-secret": …`. |
| `Deploying` | `DependencyNotReady` | A Policy forbids a read, and the Function has not served yet. |
| `RevisionReady` condition | `SecretNotFound`, `SecretKeyMissing` or `SecretResolveFailed` | While a worker serves, the phase stays `Ready`, and the reason is on this condition once funcd has checked the Secrets again. |
| Any other reason | | See the [Function's Status](../../workloads/function/index.md#status). |

funcd checks a Pending Function again every `controller.referentPollInterval`, 2 seconds by default. A Function with dedicated workers may show the updated Secret condition only after another change, such as a Function update or a worker stopping.

For binary data, base64-encode the data, then encode that text again for `spec.data`. Decode it once in the handler. Values written through `ctx.secret.put` or provided by the shell need only the first encoding.

## Changes and deletion

- Each read returns the current values. When bound Secrets share a key, the last Secret in `spec.bindings.secret` wins. Keys starting with `FUNCD_` are not returned.
- Applying a Secret manifest replaces its data, including keys written by a Function.
- Removing a declared key prevents new workers from starting until the key is restored. Existing workers keep serving and the Function stays Ready.
- Deleting a bound Secret prevents reads from all of the Function's bound Secrets: `get` returns no value and `list` fails. New workers wait until the Secret and its declared keys exist again. Existing workers keep serving and the Function stays `Ready`.
- A namespace Policy that forbids reading a bound Secret makes the Function's workers unready within `runtime.supervisionPeriod`, 10 seconds by default. Removing the Policy restores readiness without changing the Function.
- Changing a Lake's bound Secret replaces its engine. See [Lake](../lake/index.md#changes-and-deletion).
- A Function that owns Secrets shares workers only with Functions that own the same Secrets. Changing ownership can move it to other workers.
- Deleting a Function, Workflow or App does not delete its Secrets.

## CLI errors

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `… declares Secret <secret> with key <key>; set it in the shell … or under dev.env in funcdctl.config.yaml` | No file of the folder defines a Secret that a Function declares, and no source holds the key. Set the key as [Run it locally](#run-it-locally) explains, or add the Secret's file. |

## Run it locally

`funcdctl dev` keeps Secrets in memory. With `--persist`, it stores them unencrypted under `.funcd-dev/metastore`, or under the folder set by `--persist-to`. Keep that folder out of version control. Delete the persist folder after stopping the platform to clear all local state.

Each startup or file reload reapplies the folder's Secret manifests and replaces values written by Functions. Ownership and writer roles are enforced locally too.

For a declared Secret without a manifest, funcdctl reads each declared key from the shell, then `dev.envFile`, then `dev.env` in `funcdctl.config.yaml`. A missing key stops startup. See [funcdctl dev](../../tools/funcdctl/spec.md#funcdctl-dev).

From the funcd-typescript repository root, run the example with values from the shell:

```console
$ rm examples/env-echo/secret.yaml
$ API_KEY=from-shell SHARED=also-from-shell funcdctl dev examples/env-echo --gport 8080
```

In another terminal, call the Function:

```console
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"from-shell","shared":"also-from-shell"}
```

Press Ctrl-C to stop, then run `git checkout examples/env-echo/secret.yaml` to restore the file.

## Examples

- [env-echo](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): read Secret values and a ConfigMap setting.
- secret-writer: [TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/secret-writer) and [Python](https://github.com/pyvvo/funcd-python/tree/main/examples/secret-writer). Write and delete a key, then grant another Function access.
