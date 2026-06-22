# kv-counter — durable KV via `context.kv` (ADR-0069)

A **TypeScript** function that maintains a per-name counter in the platform's **KV service**. Each
invocation reads the current count via `context.kv`, increments it, writes it back, and returns it — so
two calls return `1` then `2`. It proves the function-facing KV path:

```
client ──HTTP──▶ counter ──context.kv.get/put("counters", name)──▶ [worker-node local API, UDS]
                                                                      │ PDP-authorized Facade (ADR-0019)
                                                                      ▼
                                                                   durable KV driver (ADR-0066)
```

`context.kv` dials the **same per-sandbox socket** as `context.invoke` (ADR-0064); the platform routes
`/kv/{binding}/{key}` to the Facade with the sandbox's **namespace-scoped identity** (never client-asserted).

## The API

```ts
await ctx.kv.put('counters', name, String(count)); // PUT  /kv/counters/<name>
const cur = await ctx.kv.get('counters', name);     // GET  /kv/counters/<name>  (Uint8Array | null)
await ctx.kv.del('counters', name);                 // DELETE
const keys = await ctx.kv.list('counters', 'a');    // GET  /kv/counters?prefix=a  (string[])
```

## Run it (executed by the e2e)

This example is **built and run** end-to-end by `pkg/funcd/kv_e2e_test.go`
(`TestScenarioE2EKVCounterViaContextKV`): it builds `counter.mjs`, pushes it to an OCI layout, applies
`counter.yaml`, then POSTs twice and asserts the count goes `1 → 2` (KV persisted across invocations).

```bash
nix develop -c just example-kv   # build-shim + the KV e2e (needs node on PATH)
```

KV is **namespace-scoped** (a function reaches the KV in its own namespace); fine-grained per-workload
`Grant` authz is V2. The durable Badger driver is opt-in (`kvstore.engine: badger`); the default is in-memory.
