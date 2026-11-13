---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A KV store keeps data as keys and values, grouped in tables. A Function's handler reads and writes those keys through
`ctx.kv`, the KV client in the context that every handler receives. The [Platform page](../../../platform/services/kv.md)
explains how KV works inside funcd; this page shows how to use it from a Function.

Four parts connect a Function to its data. The example below uses each of them:

- A **store** holds one or more tables. A Function declares the store in its manifest, under `spec.kv`.
- A **table** is a separate set of keys inside the store.
- A table's **owner** is the Function that writes the table. When a Function declares a store and a table of that
  store sets no `owner`, the declaring Function owns the table.
- A **binding**, under `spec.bindings.kv`, connects one table to the Function. The binding's `alias` is the name that
  the handler passes to `ctx.kv`. A Function reaches a table only through a binding: funcd refuses a `ctx.kv` call
  whose alias no binding defines.

The [Spec](spec.md) tab lists every field of a store and every method of `ctx.kv`, in TypeScript and in Python.

The first example, kv-counter, counts calls by name. Each call adds one to the count stored under the name that the
call sends, and answers with the new count. Running the example and calling it twice shows the count go from 1 to 2.

The example needs three programs on the computer:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- The runtime of the example's language on the `PATH`: Node.js for TypeScript, or Python 3.12 or later as `python3`.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's manifest names
an `image`, the bundle that `funcdctl push` writes for a platform to run; `funcdctl dev` ignores the image and runs
the Function from the code in the folder instead. It keeps the KV data in memory, and it prints the URL of each Function.
The `--gport` flag sets the port of that URL, so the commands below use port 8080.

### TypeScript

The manifest of the Function `counter` declares the store `counters-kv`, with one table, `table-counters`. The table
sets no `owner`, so `counter` owns it. The binding gives the table the alias `counters`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]

The handler reads the current count with `getText`, adds one, and writes the new count with `put`:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

Clone the repository, and run the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/kv-counter --gport 8080
```

From another terminal, call the Function twice with the same name. The first answer holds the count 1, and the second
holds the count 2:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/counter
{"name":"ada","count":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/counter
{"name":"ada","count":2}
```

### Python

The manifest of the Function `pycounter` declares the store `py-counters`, with one table, `table-counters`. The table
sets no `owner`, so `pycounter` owns it. The binding gives the table the alias `pycounters`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/counter.yaml#manifest]

The handler reads the current count with `get_str`, adds one, and writes the new count with `put`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/src/main.py#handler]

Clone the repository, and run the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ funcdctl dev examples/kv-counter --gport 8080
```

From another terminal, call the Function twice with the same name. The first answer holds the count 1, and the second
holds the count 2:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/pycounter
{"name":"ada","count":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"ada"}' http://127.0.0.1:8080/function/pycounter
{"name":"ada","count":2}
```

## Write without losing an update

The kv-counter handler reads the count, adds one, and writes the result. When two calls for the same name run at the
same moment, both can read the same count and write the same new count. One increment is then lost.

A conditional write prevents this loss. It writes only while a condition holds, and its result tells the caller
whether it wrote:

- `putIfAbsent` (`put_if_absent` in Python) creates a key only when the key does not exist. A key that has expired
  counts as absent.
- `compareAndSet` (`compare_and_set` in Python) replaces a key only when the key still has a given version. The
  caller gets that version from `getEntry` (`get_entry` in Python), which returns a key's value with its version.
- `delIf` (`delete_if` in Python) deletes a key only when the key still has a given version.

When several callers race, exactly one of them succeeds. A successful `putIfAbsent` or `compareAndSet` returns the
key's new version; the other callers get `null` (`None` in Python). A successful `delIf` returns `true`, and the other
callers get `false` (`False` in Python). A caller that lost can read the key again and retry.

## Hold a claim on a key

The kv-claim example uses conditional writes to hold a claim: a key that only one caller holds at a time. Each call
sends a `key`, a `holder` that names the caller, and an `op`:

- `claim` creates the key with `putIfAbsent`.
- `renew` replaces the key with `compareAndSet`, using the `version` that the holder received.
- `release` deletes the key with `delIf`, using that `version`.

When the conditional write fails, the call answers `won: false`, with the key's current version and holder.

A claim can expire. A write with a `ttl`, in whole seconds, makes the key expire after that time. A write without a
`ttl` leaves the key with no expiry. A holder keeps its claim by renewing it with `compareAndSet` and a new `ttl`
before the claim expires.

The TypeScript Function `claim` declares the store `claims-kv`, whose table `claims` it owns. The Python Function
`pyclaim` writes the same table through a writer role, which [Access](#access) explains, so both Functions compete for
the same claims. The core of each handler follows:

### TypeScript

::include[../../../../../_repos/funcd-typescript/examples/kv-claim/src/main.ts#claim]

### Python

::include[../../../../../_repos/funcd-python/examples/kv-claim/src/main.py#claim]

## Share a store between Functions

A store can also be a resource of its own, a KVStore, that several Functions use. In a KVStore, each table's `owner`
field names the one Function that writes the table:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#kvstore]

Each Function that uses the store declares it by `ref` under `spec.kv`, and binds the table as in the first example:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#binding]

A table of a KVStore that sets no `owner` has no owner. Only a Function that a writer role allows can write it.

## Limits and quotas

A store's `spec.quota` sets its limits:

- `maxSize` or `maxBytes` limits the total size of the store. Set one of the two, not both; without either, the
  store has no size limit.
- `maxValueBytes` limits the size of each value. When it is not set, the limit is 1 MiB.
- `maxKeyBytes` limits the size of each key. When it is not set, the limit is 1 KiB.

A write of a value or a key above its limit gets the HTTP status 413. A write that would take the store above its
total size gets 507. The [Spec](spec.md) tab gives the range of each field.

## Where the data lives

funcd keeps KV data in memory, where the data is lost when funcd stops, unless funcd's configuration sets
`kvstore.engine: badger`. With `badger`, funcd keeps the data on disk, by default in the `kv` folder of its data
directory. When the configuration sets `storage.mode: memory`, the KV data stays in memory, whatever the engine.

## Access

A table's owner can write the table. Another Function can write it when a RolesAssignment grants that Function
`kv::write` on the table, on its store, or on the whole namespace. The built-in roles `KV Data Writer`, `Contributor`
and `Owner` include `kv::write`, and a Role that an operator defines can include it too. A grant names a store as
`<store>`, or one table as `<store>/<table>`, such as `claims-kv/claims`. The
[Security](../../security-and-networking/security/spec.md) Spec tab lists the fields of a RolesAssignment.

A Function that binds a table without a right to write it can read the table but not write it.

A binding can name only a store that its own Function declares, by `name` or by `ref`. funcd refuses a binding to any
other store.

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
