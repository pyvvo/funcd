---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A KV store keeps data as keys and values, grouped in tables. A Function's handler reads and writes those keys through
`ctx.kv`, the KV client in the context that every handler receives. The
[Platform page](../../../platform/services/kv.md) explains how KV works inside funcd; this page shows how to use it.

The first example, kv-counter, counts calls by name. Each call sends a name, and the Function adds one to the count
stored under that name and returns the new count. The steps below start the example on the local computer and call it
twice with the same name: the first response contains `"count":1`, and the second contains `"count":2`.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Function.
- The runtime of the example's language on the `PATH`: Node.js for TypeScript, or Python 3.12 or later as `python3`.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's manifest names
an `image`, the bundle that `funcdctl push` writes for a platform to run. `funcdctl dev` does not use that image: it
runs the Function from the code in the folder. `funcdctl dev` keeps the KV data in memory and prints the URL of each
Function.
The `--gport` flag sets the port of that URL, so the commands below use port 8080.

The example's manifest connects the Function to its data:

- It declares a **store** under `spec.kv`, with one **table**. A table is a separate set of keys inside the store.
- The table sets no `owner`, so the Function that declares the store becomes the table's owner. The owner can write
  the table.
- A **binding** under `spec.bindings.kv` connects the table to the Function and gives the table an alias. The handler
  passes that alias to `ctx.kv`. funcd rejects a `ctx.kv` call that uses an alias that no binding declares.

The [Spec](spec.md) tab lists every field of a store and every method of `ctx.kv`, in TypeScript and in Python.

### TypeScript

The Function `counter` declares the store `counters-kv`, with the table `table-counters`, and binds the table with the
alias `counters`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]

The handler reads the current count with `getText`, adds one, and writes the new count with `put`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

Clone the repository, and start the example from the repository's root folder:

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

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

### Python

The Function `pycounter` declares the store `py-counters`, with the table `table-counters`, and binds the table with
the alias `pycounters`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/counter.yaml#manifest]

The handler reads the current count with `get_str`, adds one, and writes the new count with `put`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/src/main.py#handler]

Clone the repository, and start the example from the repository's root folder:

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

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Write a key only while a condition holds

The kv-counter handler reads the count, adds one, and writes the result. When two calls for the same name run at the
same moment, both can read the same count and write the same new count, so one increment is lost.

A conditional write lets a handler detect that another caller changed a key first. A conditional write changes the
key only while its condition holds, and its result tells the caller whether the write happened:

- `putIfAbsent` (`put_if_absent` in Python) creates a key only when the key does not exist. A key that has expired
  counts as absent. A successful `putIfAbsent` returns the key's new version; when the key exists, `putIfAbsent`
  returns `null` (`None` in Python).
- `compareAndSet` (`compare_and_set` in Python) replaces a key only when the key still has a given version. The
  caller gets that version from `getEntry` (`get_entry` in Python), which returns a key's value with its version. A
  successful `compareAndSet` returns the key's new version; when the key does not have the given version,
  `compareAndSet` returns `null` (`None`).
- `delIf` (`delete_if` in Python) deletes a key only when the key still has a given version. A successful `delIf`
  returns `true` (`True` in Python); when the key does not have the given version, `delIf` returns `false`
  (`False`).

When several callers create the same absent key at the same moment, exactly one `putIfAbsent` succeeds. When several
callers replace or delete the same key with the same version, exactly one `compareAndSet` or `delIf` succeeds. A
caller whose write failed has not changed the key. To keep its update, the caller reads the key again, with the key's
new version, and retries.

## Hold a claim on a key

The kv-claim example uses conditional writes to hold a claim: a key that one caller holds until that caller releases
the key or the key expires. Each call sends a `key`, a `holder` that names the caller, and an `op`:

- `claim` creates the key with `putIfAbsent`.
- `renew` replaces the key with `compareAndSet`, using the `version` that the holder received when it won.
- `release` deletes the key with `delIf`, using that `version`.

A successful call returns a response with `"won":true` and the key's new version; after a successful release, the
response's version and holder are empty. A failed call returns a response with `"won":false` and the key's current
version and holder, which are empty when the key no longer exists.

A claim can expire. A write with a `ttl`, a number of whole seconds, makes the key expire after that time. A write
without a `ttl` leaves the key with no expiry. A holder keeps its claim by renewing it with a new `ttl` before the
claim expires.

The TypeScript Function `claim` declares the store `claims-kv` and owns its table `claims`. The Python Function
`pyclaim` writes the same table through a writer role, which [Access](#access) explains, so both Functions compete
for the same claims.

The excerpts below show only the conditional write of each handler; they do not run on their own.

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

To share a store between Functions, create it as a separate KVStore, and set the `owner` of each table to the
Function that writes the table:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#kvstore]

Each Function that uses the store declares it by `ref` under `spec.kv`, and binds the table as in the first example:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#binding]

Every Function that binds the table can read it. To write the table, a Function other than the owner needs the write
permission that [Access](#access) describes. When a table of a KVStore sets no `owner`, only that permission lets a
principal write the table.

## Limits and quotas

A store's `spec.quota` sets its limits:

- `maxSize` or `maxBytes` limits the total size of the store. Set one of the two, not both. Without either, the store
  has no size limit.
- `maxValueBytes` limits the size of each value. When it is not set, the limit is 1 MiB.
- `maxKeyBytes` limits the size of each key. When it is not set, the limit is 1 KiB.

A write whose value or key is above its limit fails with the HTTP status 413. A write that would take the store above
its total size fails with the HTTP status 507. The [Spec](spec.md) tab gives the range of each field.

## Data storage

By default, funcd keeps KV data in memory, and the data is lost when funcd stops. To keep the data on disk, set
`kvstore.engine: badger` in funcd's configuration. funcd then stores the data in the `kv` folder of its data
directory, unless `kvstore.dataDir` names another folder. `storage.mode: memory` keeps KV data in memory even when
`kvstore.engine` is `badger`.

## Access

A Function gets two grants from its own manifest, with no role to assign. funcd checks both with Cedar, as the
[Security](../../../platform/security-and-networking/security.md) page explains:

- **Read.** A binding works as a read grant. Each entry of `spec.bindings.kv` adds its table to the Function's
  `kvBindings`, which allows the Cedar action `kv::read` on that table. A binding grants reading only, never writing.
- **Write.** Ownership works as a write grant. The table's owner is in the table's `writers`, which allows the Cedar
  action `kv::write`. A principal outside `writers` cannot write the table.

Any other write needs a role. A RolesAssignment adds a principal, a Function or an Identity, to a table's `writers`.
The smallest built-in role for it is `KV Data Writer`, which holds `kv::read` and `kv::write`. A grant's scope is one
table, written as `<store>/<table>`, a store, written as `<store>`, or the whole namespace. The kv-claim example
gives `pyclaim` write access to the table `claims` of the store `claims-kv`:

::include[../../../../../api/types/v1alpha1/examples/kv/writer-role.yaml#rolesassignment]

The built-in roles `Contributor` and `Owner` also hold `kv::write`, with read and write access to buckets, and a Role
that an operator defines can hold it too. A read of a table that the principal does not bind needs a grant as well,
such as the built-in role `KV Data Reader`, which holds `kv::read`. The
[Security](../../security-and-networking/security/spec.md) Spec tab lists the fields of a RolesAssignment.

funcd enforces these rules with a built-in Cedar policy, which it ships as `builtin_kv.cedar`. The policy is part of
funcd and applies in every namespace, so nothing needs to be written or installed. A Policy that an operator writes
applies beside it and cannot lift its `forbid` rule:

```cedar title="builtin_kv.cedar"
@id("kv-read-binding")
permit(principal, action == Action::"kv::read", resource)
  when { principal has kvBindings && principal.kvBindings.contains(resource) };

@id("kv-write-base")
permit(principal, action == Action::"kv::write", resource);

@id("kv-single-writer")
forbid(principal, action == Action::"kv::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

`kv-read-binding` is the read grant of a binding. `kv-write-base` allows every write, and `kv-single-writer` forbids a
write by any principal outside the table's `writers`.

A binding can name only a store that its own Function declares, by `name` or by `ref`. funcd rejects a binding to any
other store. A call that the policy denies fails with the HTTP status 403, and its message names the rule, such as
`cedar: forbidden by policy builtin/kv-single-writer`.

## Run it locally

`funcdctl dev` keeps the KV data in memory, so the data is lost when `funcdctl dev` stops. With the `--persist` flag,
`funcdctl dev` keeps the KV, blob and metastore data in a local folder between runs.

## Examples

- [kv-counter in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-counter) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-counter): a count for each name, kept in a
  KV store.
- [kv-claim in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-claim) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-claim): a claim on a key, held with
  conditional writes.
