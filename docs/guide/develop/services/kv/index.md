---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A KV store keeps keys and their values in tables, each a separate set of keys inside the store. A Function's handler
reads and writes the keys through `ctx.kv`, the KV client of the context that every handler receives. See
[how KV works](../../../platform/services/kv.md) for the concept.

The kv-counter example counts calls by name: each call sends a name, and the Function adds one to the count stored
under that name and returns the new count.

Install the [dev build of funcdctl](../../../get-started/install.md), Git and curl, and put the runtime of the
example's language on the `PATH`: Node.js for TypeScript, or Python 3.12 or later as `python3`.

The example's Function declares a store under [`spec.kv`](../../workloads/function/spec.md#functionspeckv), with one
table. The table sets no `owner`, so the Function that declares the store owns the table and can write it;
[Access](#access) explains who else can. A **binding** under
[`spec.bindings.kv`](../../workloads/function/spec.md#functionspecbindingskv) connects the table to the Function under
an alias, which the handler passes to `ctx.kv`. The [Spec](spec.md) tab lists every field of a store and every method
of [`ctx.kv`](spec.md#ctxkv).

`funcdctl dev` starts a local platform, runs the resource files of one folder on it, and prints the URL of each
Function, on the port that `--gport` sets. It runs the Function from the code in the folder, not from the manifest's
`image`, the bundle that `funcdctl push` writes for a platform to run.

### TypeScript

The Function `counter` binds its table with the alias `counters`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]

Its handler reads the count with `getText`, adds one, and writes it with `put`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

Clone the repository and start the example from its root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/kv-counter --gport 8080
```

In another terminal, call the Function twice with the same name:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/counter
{"name":"ada","count":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/counter
{"name":"ada","count":2}
```

Press Ctrl-C in the first terminal to stop the example.

### Python

The Function `pycounter` binds its table with the alias `pycounters`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/counter.yaml#manifest]

Its handler reads the count with `get_str`, adds one, and writes it with `put`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/src/main.py#handler]

Clone the repository and start the example from its root folder:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ funcdctl dev examples/kv-counter --gport 8080
```

In another terminal, call the Function twice with the same name:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/pycounter
{"name":"ada","count":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/pycounter
{"name":"ada","count":2}
```

Press Ctrl-C in the first terminal to stop the example.

## Write a key only while a condition holds

When two kv-counter calls for the same name run at the same moment, both can read the same count and write the same
new count, so one increment is lost. A conditional write lets the handler detect that another caller changed the key
first: it changes the key only while its condition holds, and its result tells the caller whether the write happened:

- `putIfAbsent` (`put_if_absent` in Python) creates a key only when the key does not exist; a key that has expired
  counts as absent. It returns the key's new version, or `null` (`None` in Python) when the key exists.
- `compareAndSet` (`compare_and_set`) replaces a key only when the key still has a given version, which `getEntry`
  (`get_entry`) returns with the key's value. It returns the key's new version, or `null` (`None`) when the key does
  not have that version.
- `delIf` (`delete_if`) deletes a key only when the key still has a given version. It returns `true` (`True`), or
  `false` (`False`) when the key does not have that version.

When several callers create the same absent key at the same moment, exactly one `putIfAbsent` succeeds. When several
callers replace or delete the same key with the same version, exactly one `compareAndSet` or `delIf` succeeds. A
caller whose write failed has not changed the key: to keep its update, it reads the key again, with the key's new
version, and retries.

## Hold a claim on a key

A claim is a key that one caller holds until that caller releases the key or the key expires. The kv-claim example
holds claims with conditional writes. Each call sends a `key`, a `holder` that names the caller, and an `op`:

- `claim` creates the key with `putIfAbsent`.
- `renew` replaces the key with `compareAndSet`, using the `version` that the holder received when it won.
- `release` deletes the key with `delIf`, using that `version`.

A call that wins returns a response with `"won":true` and the key's new version; after a release, the version and the
holder are empty. A call that loses returns a response with `"won":false` and the key's current version and holder,
which are empty when the key no longer exists.

A write with a `ttl`, a number of whole seconds, makes the key expire after that time, and a write without a `ttl`
leaves the key with no expiry. A holder keeps its claim by renewing it with a new `ttl` before the claim expires.

The TypeScript Function `claim` declares the store `claims-kv` and owns its table `claims`. The Python Function
`pyclaim` writes the same table through a writer role, which [Access](#access) shows, so both Functions compete for the
same claims. The excerpts below show only the conditional write of each handler; they do not run on their own.

### TypeScript

In this excerpt, `opts` holds the call's `ttl` as `{ ttl }`, or is `undefined` when the call sends no `ttl`. The
complete handler is
[examples/kv-claim/src/main.ts](https://github.com/pyvvo/funcd-typescript/blob/main/examples/kv-claim/src/main.ts).

::include[../../../../../_repos/funcd-typescript/examples/kv-claim/src/main.ts#claim]

### Python

In this excerpt, `kv` is `context.kv`, and `ttl` is the call's `ttl`, or `None` when the call sends none. The complete
handler is
[examples/kv-claim/src/main.py](https://github.com/pyvvo/funcd-python/blob/main/examples/kv-claim/src/main.py).

::include[../../../../../_repos/funcd-python/examples/kv-claim/src/main.py#claim]

## Share a store between Functions

Create the store as a separate KVStore, and set the `owner` of each table to the Function or Job that writes the table, by
its kind and name:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#kvstore]

Each Function that uses the store declares it by `ref` under `spec.kv` and binds the table, as this excerpt of the
Function `counter`, from the
[complete example](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/kv/kvstore.yaml), shows:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#binding]

Every Function that binds the table can read it. A Function other than the owner needs a role to write it, as
[Access](#access) explains, and a table of a KVStore that sets no `owner` is written only through such a role.

## Access

A Function needs no role for these [actions](../../security-and-networking/security/index.md#choose-a-role):

- **Read the tables that it binds** (`kv::read`). Each entry of `spec.bindings.kv` lets the Function read that table,
  never write it.
- **Write the tables that it owns** (`kv::write`).

Any other write needs a role. A RolesAssignment gives a Function a role on one table, on a store, or on the whole
namespace. The smallest built-in role that allows writing is `KV Data Writer`, which allows reading and writing
keys. Its scope names one table as `<store>/<table>`, or a store as `<store>`. The kv-claim example gives `pyclaim`
write access to the table `claims` of the store `claims-kv`:

::include[../../../../../api/types/v1alpha1/examples/kv/writer-role.yaml#rolesassignment]

The built-in roles `Contributor` and `Owner` also allow writing keys, together with reading and writing buckets, and a
Role that an operator defines can allow it too. The Security reference
[lists the built-in roles](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref) and
[explains how assignments pair each role with a scope](../../security-and-networking/security/spec.md#rolesassignmentspecassignments).

funcd applies these rules as a built-in policy in every namespace, so nothing needs to be written or installed. A
[Policy](../../security-and-networking/security/index.md#write-a-policy) that an operator writes applies beside it,
and cannot allow a write that the built-in policy forbids. In Cedar, the built-in policy reads:

```cedar title="Built-in KV policy"
@id("kv-read-binding")
permit(principal, action == Action::"kv::read", resource)
  when { principal has kvBindings && principal.kvBindings.contains(resource) };

@id("kv-write-base")
permit(principal, action == Action::"kv::write", resource);

@id("kv-single-writer")
forbid(principal, action == Action::"kv::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

A binding can name only a store that its own Function declares, by `name` or by `ref`. No role lets a Function read a
table that it does not bind: `ctx.kv` takes only a binding's alias, and no other client reaches KV keys. A call that
the policy denies fails with the HTTP status 403, and its message names the rule. A write by a Function that neither
owns the table nor holds a writer role fails with `cedar: forbidden by policy builtin/kv-single-writer`: give the
Function a writer role, as above.

## Limits and quotas

A store's [`spec.quota`](spec.md#kvstorespecquota) sets its limits:

- `maxSize` or `maxBytes` limits the total size of the store. Set one of the two, not both. Without either, the store
  has no size limit.
- `maxValueBytes` limits the size of each value. When it is not set, the limit is 1 MiB.
- `maxKeyBytes` limits the size of each key. When it is not set, the limit is 1 KiB.

A write fails when its key or its value is above its limit, or when it would take the store above its total size. The
[Spec](spec.md) tab gives the range of each field.

## Data storage

By default, funcd keeps KV data in memory, and the data is lost when funcd stops. To keep the data on disk, set
`kvstore.engine: badger` in [funcd's configuration](../../running-funcd/spec.md#funcdconfigyaml). funcd then stores
the data in the `kv` folder of its data directory, unless `kvstore.dataDir` names another folder.
`storage.mode: memory` keeps KV data in memory even when `kvstore.engine` is `badger`.

## Status

`funcdctl get kvstore counters-kv -n default -o json` prints the status of the TypeScript example's store, and
`funcdctl get function counter -n default -o json` that of its Function; in Python, they are `py-counters` and
`pycounter`. With `funcdctl dev`, add the `--server` and `--token` flags that its banner prints. A store's
`status.tables` counts the tables that it declares, and `status.bindings` the bindings that name it.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| KVStore `Ready` | | funcd's KV engine answers. |
| KVStore `Degraded` | `StorageUnreachable` | funcd's KV engine did not answer its last check, which funcd repeats every `health.storageProbeInterval`, 10 seconds by default. The message of the `Ready` condition gives the error. The store becomes `Ready` again by itself once a check passes. |
| Function `Pending` | `KVStoreNotFound` | A store or a table that the Function binds does not exist. The Function starts once it exists. |
| Function `Pending` | `StoreNotOwned` | A store that the Function declares by `name` exists and is not the Function's, as [Changes and deletion](#changes-and-deletion) describes. |
| Any other reason | | See the [Function's Status](../../workloads/function/index.md#status). |

## Changes and deletion

- funcd refuses a request to delete a KVStore while a binding names it or while it holds keys. To delete the store,
  remove its bindings and delete its keys first.
- Removing a table from a store deletes the table's keys. funcd refuses to remove a table from a KVStore while a
  binding names the table, so remove those bindings first.
- When a Function that declares a store by `name` is deleted, the
  [`deletion`](../../workloads/function/spec.md#functionspeckv) setting of its entry decides: `retain`, the default,
  keeps the store and its keys, and `delete` deletes them.
- To use a store that stayed, declare it by `ref` in the new Function. A new Function of the same name that declares
  the store by `name` waits in `Pending` with the reason `StoreNotOwned`, since the store still belongs to the deleted
  Function.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |

## Run it locally

`funcdctl dev` keeps KV data in memory, whatever [Data storage](#data-storage) says about a server, so the data is
lost when `funcdctl dev` stops. With `--persist`, it keeps the KV data between runs, in the `kv` folder of
`.funcd-dev` in the current directory, or of the folder that `--persist-to` names. Deleting `.funcd-dev`, or the
`--persist-to` folder, after `funcdctl dev` stops clears all the state that the local platform stored: its resources
and its KV, blob, SQL, lake and volume data.

## Examples

- [kv-counter in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-counter) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-counter): a count for each name, kept in a
  KV store.
- [kv-claim in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-claim) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-claim): a claim on a key, held with
  conditional writes.
