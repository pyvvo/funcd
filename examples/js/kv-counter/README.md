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
await ctx.kv.put('counters', name, String(count));  // PUT  /kv/counters/<name>
const s = await ctx.kv.getText('counters', name);   // GET  → string | null   (ADR-0070)
const o = await ctx.kv.getJSON('counters', name);   // GET  → parsed JSON | null
const b = await ctx.kv.get('counters', name);        // GET  → Uint8Array | null (raw bytes)
await ctx.kv.del('counters', name);                  // DELETE
const keys = await ctx.kv.list('counters', 'a');     // GET  /kv/counters?prefix=a  (string[])
```

## Contract (ADR-0058/0060)

`build.ts` is contract-aware (like `examples/js/fn-to-fn`): from the handler's `FuncInput` /
`FuncOutput` types it generates the closed JSON Schema, **bakes an eval-free validator** into
`counter.mjs`, and writes `counter-{input,output}.schema.json`. Those schemas are pushed as OCI
metadata (`funcdctl push --contract-input/--contract-output`), so a malformed call is rejected (422)
before the handler runs — KV functions are contract-validated, not just KV-enabled.

## Run it locally (`funcdctl dev`)

`funcdctl dev` runs the function from source — no hand-written CRDs — printing a colored services
banner + live logs (it builds the `-tags dev` funcdctl for you):

```bash
just dev-example js/kv-counter      # gateway :3005 · S3 :3006 — override: just dev-example js/kv-counter 4000 4001
```

Invoke the gateway the banner prints (default `http://127.0.0.1:3005`). The single generic
`funcdctl.yaml` names the function after its directory (`kv-counter`); the invoke is a **CloudEvent
envelope** — `{"data": <input>}` matching the manifest's `contract.input` (`{name: string}`). POST
**twice** and the durable `context.kv` counter increments `1 → 2`:

```bash
curl -sS -XPOST http://127.0.0.1:3005/function/kv-counter \
  -H 'Content-Type: application/json' -d '{"data":{"name":"alice"}}'
# → {"name":"alice","count":1}   then, on the second POST,   {"name":"alice","count":2}
```

## Run it (executed by the e2e)

This example is **built and run** end-to-end by `pkg/funcd/kv_e2e_test.go`
(`TestScenarioE2EKVCounterViaContextKV`): it builds `counter.mjs` + its schemas, pushes them to an OCI
layout (with the contract), applies `counter.yaml`, then POSTs twice and asserts the count goes
`1 → 2` (KV persisted across invocations).

```bash
nix develop -c just example-kv   # build-shim + the KV e2e (needs node on PATH)
```

KV is **namespace-scoped** (a function reaches the KV in its own namespace); fine-grained per-workload
`Grant` authz is V2. The durable Badger driver is opt-in (`kvstore.engine: badger`); the default is in-memory.
