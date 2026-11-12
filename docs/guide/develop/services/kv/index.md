---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A Function declares a KV store under `spec.kv` and binds one of its tables under `spec.bindings.kv`. The binding's
alias is the name that the handler passes to `ctx.kv`. The kv-counter example, which funcd's lanes run on a real
platform, shows both; its `image` is the bundle that `funcdctl push` wrote from the handler:

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]

::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

The table names no owner, so its owner is the Function: `counter` is the one Function that may write it. A Function
reaches a table only through an entry of its `spec.bindings.kv`; without one, `ctx.kv` on that alias is refused.

## Read and write

| Method | What it does |
| --- | --- |
| `get(alias, key)` | Returns the value as bytes, or `null` when the key does not exist |
| `getText(alias, key)` | Returns the value decoded as UTF-8 text |
| `getJSON(alias, key)` | Returns the value parsed as JSON |
| `getEntry(alias, key)` | Returns the value with its version |
| `put(alias, key, value, { ttl })` | Writes the value; `ttl`, in whole seconds, sets the key's expiry |
| `del(alias, key)` | Deletes the key |
| `list(alias, prefix)` | Returns the keys that start with the prefix |

Each write sets its own expiry: a `put` without `ttl` leaves the key with none. Only the table's owner can write;
another Function that binds the table reads it.

## Conditional writes

`putIfAbsent`, `compareAndSet` and `delIf` write only while their condition holds, and say who won: of racing writers,
exactly one gets the new version, and the others get `null` (`false` for `delIf`). The kv-claim example uses them to
hold a claim on a key:

::include[../../../../../_repos/funcd-typescript/examples/kv-claim/src/main.ts#claim]

With a `ttl` in `opts`, the claim expires unless its holder renews it with `compareAndSet` and a new `ttl`.

## A store applied on its own

A store can also be applied on its own, as a KVStore, for example when several Functions read it. Its table names the
one Function that writes it:

::include[../../../../../api/types/v1alpha1/examples/kv/own-store.yaml#kvstore]

The Function then declares the store by `ref` and binds its table as before. A binding names only a store that its
Function declares, by `name` or by `ref`; funcd refuses a binding to any other store.

::include[../../../../../api/types/v1alpha1/examples/kv/own-store.yaml#binding]

## Quotas

`spec.quota` caps a store: `maxSize` or `maxBytes` its total size, `maxValueBytes` each value (1 MiB when unset) and
`maxKeyBytes` each key (1 KiB when unset). A write that would take the store past its size gets a 507. The
[Spec](spec.md) tab lists the limits of each field.

## Where the data lives

funcd keeps KV data in memory unless its configuration sets `kvstore.engine: badger`, which stores the data durably in
the data directory. `funcdctl dev` keeps it in memory; with `--persist`, it keeps KV, blob and metastore data in a local
directory between runs.

## Examples

- [kv-counter](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-counter): a count per name in a KV store.
- [kv-claim](https://github.com/pyvvo/funcd-typescript/tree/main/examples/kv-claim): a claim on a key with conditional
  writes.
