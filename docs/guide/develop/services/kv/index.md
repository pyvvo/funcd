---
title: KV
description: Declare a key-value store, bind its tables to a Function, and read and write keys from the handler.
---

## Usage

A Function declares a KV store under `spec.kv` and binds one of its tables under `spec.bindings.kv`. The binding's
alias is the name that the handler passes to `ctx.kv`:

```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: counter
  namespace: default
  resourceGroup: rg1
spec:
  runtime: nodejs22
  handler: handle
  image: registry.example.com/counter:1.0.0
  kv:
    - name: counters-kv
      tables:
        - name: table-counters
  bindings:
    kv:
      - alias: counters
        store: counters-kv
        table: table-counters
```

```ts
import type { CloudEvent, FunctionContext } from '@funcd-dev/shim';

export interface FuncInput {
  name?: string;
}
export interface FuncOutput {
  name: string;
  count: number;
}

export async function handle(ctx: FunctionContext, event: CloudEvent<FuncInput>): Promise<FuncOutput> {
  const name = event.data?.name ?? 'world';
  const cur = await ctx.kv.getText('counters', name);
  const count = Number(cur ?? 0) + 1;
  await ctx.kv.put('counters', name, String(count));
  ctx.log(`kv-counter: ${name} → ${count}`);
  return { name, count };
}
```

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

```ts
const v =
  op === 'renew'
    ? await ctx.kv.compareAndSet('claims', key, holder, version, opts)
    : await ctx.kv.putIfAbsent('claims', key, holder, opts);
if (v !== null) return { key, won: true, version: v, holder };
```

With a `ttl` in `opts`, the claim expires unless its holder renews it with `compareAndSet` and a new `ttl`.

## A store applied on its own

A store can also be applied on its own, as a KVStore, for example when several Functions read it. Its table names the
one Function that writes it:

```yaml
apiVersion: funcd.io/v1alpha1
kind: KVStore
metadata:
  name: counters-kv
  namespace: default
  resourceGroup: rg1
spec:
  quota:
    maxSize: 64Mi
  tables:
    - name: table-counters
      owner: counter
```

The Function then declares the store by `ref` and binds its table as before. A binding names only a store that its
Function declares, by `name` or by `ref`; funcd refuses a binding to any other store.

```yaml
spec:
  kv:
    - ref: counters-kv
  bindings:
    kv:
      - alias: counters
        store: counters-kv
        table: table-counters
```

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
