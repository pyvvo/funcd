---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A KV store holds tables of keys and values that Functions read and write through `ctx.kv`; the
[Platform page](../../../platform/services/kv.md) explains the concept. A Function declares a store under `spec.kv`
and binds one of its tables under `spec.bindings.kv`. The binding's alias is the name that the handler passes to
`ctx.kv`. The [Spec](spec.md) tab lists every method of `ctx.kv`, in TypeScript and Python.

### TypeScript

The kv-counter example, which funcd's lanes run on a real platform, counts the calls for each name. Its `image` is the
bundle that `funcdctl push` wrote from the handler:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

### Python

The Python kv-counter, which the same lanes run, declares its own store, `py-counters`, and binds it as `pycounters`:

::include[../../../../../_repos/funcd-python/examples/kv-counter/counter.yaml#manifest]

::include[../../../../../_repos/funcd-python/examples/kv-counter/src/main.py#handler]

Neither table names an owner, so each table's owner is the Function that declares it: `counter` writes its table, and
`pycounter` writes its own. A Function reaches a table only through an entry of its `spec.bindings.kv`; without one,
`ctx.kv` on that alias is refused.

## Conditional writes and expiry

`putIfAbsent`, `compareAndSet` and `delIf` (`put_if_absent`, `compare_and_set` and `delete_if` in Python) write only
while their condition holds, and say who won: of racing writers, exactly one gets the new version, and the others get
`null` (`None` in Python), or `false` (`False`) for a delete. The kv-claim example uses them to hold a claim on a key.
Its TypeScript Function, `claim`, owns the claims table, and its Python Function, `pyclaim`, writes the same table
through a writer role (see [Access](#access)), so the two race on one table.

### TypeScript

::include[../../../../../_repos/funcd-typescript/examples/kv-claim/src/main.ts#claim]

### Python

::include[../../../../../_repos/funcd-python/examples/kv-claim/src/main.py#claim]

Each write sets the key's expiry: with a `ttl`, in whole seconds, the key expires after that time, and a write without
one leaves the key with no expiry. A claim with a `ttl` expires unless its holder renews it with `compareAndSet` and a
new `ttl`.

## A store applied on its own

A store can also be applied on its own, as a KVStore, for example when several Functions read it. Its table names its
owner, the Function that writes it:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#kvstore]

The Function then declares the store by `ref` and binds its table as before:

::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#binding]

## Limits and quotas

`spec.quota` caps a store: `maxSize` or `maxBytes` its total size, `maxValueBytes` each value (1 MiB when unset) and
`maxKeyBytes` each key (1 KiB when unset). A value or a key over its cap gets a 413, and a write that would take the
store past its size gets a 507. The [Spec](spec.md) tab lists the limits of each field.

## Where the data lives

funcd keeps KV data in memory unless its configuration sets `kvstore.engine: badger`, which stores the data durably in
the data directory. With `storage.mode: memory`, KV data stays in memory whatever the engine.

## Access

A table's owner writes it, and so does a principal that a RolesAssignment grants `kv::write` on the table's
namespace, its store or the table itself. The built-in roles `KV Data Writer`, `Contributor` and `Owner` hold
`kv::write`, and so can a Role. A KVStore scope names the store, `<store>`, or one of its tables, `<store>/<table>`,
such as `claims-kv/claims`. Another Function that binds the table reads it. A binding names only a store that its Function
declares, by `name` or by `ref`; funcd refuses a binding to any other store. The
[Security](../../security-and-networking/security/spec.md) Spec tab lists the fields of a RolesAssignment.

## Run it locally

`funcdctl dev` keeps KV data in memory; with `--persist`, it keeps KV, blob and metastore data in a local directory
between runs. In a clone of funcd-typescript, `funcdctl dev examples/kv-counter` runs the TypeScript quick start as it
stands: dev runs each Function from its local build, which it finds by the Function's name, in place of its `image`.

## Examples

- [kv-counter in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-counter) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-counter): a count per name in a KV store.
- [kv-claim in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-claim) and
  [in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/kv-claim): a claim on a key with conditional
  writes.
