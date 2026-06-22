# ADR-0069: KV data-plane — function-facing KV over the worker-node local API

- **Status**: Implemented
- **Date**: 2026-06-22 (judged 2026-06-22 — reuses the verified worker-node local API (ADR-0064 `NewHandler`),
  the Facade's exact signatures (ADR-0019), and the ADR-0066 driver; folded the identity-granularity point —
  namespace-scoped sandbox identity for v1.1, workload-`Grant` authz deferred to V2 per ADR-0018/0019. No
  Blockers. **Implemented 2026-06-22** — local-API `/kv` routes + `sandboxIdentity`; the platform constructs +
  wires + closes the Facade (resolving ADR-0066's facade-selection); `kvstore.engine`/`dataDir` config + the
  in-memory/Badger driver selection; the local-API socket now provisioned for every function. `context.kv` in
  BOTH shims (Node + Python). `examples/js/kv-counter` + an in-process KV e2e (count 1→2). 4 scenario tests +
  e2e green; lint clean; see docs/reviews/adr-0069-implementation-claude-opus-4-8.md.)
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, kv, data-plane, worker-node, local-api, sdk, facade, identity
- **Realizes**: [FEAT-0001/F38](../feat/0001-feat-v1.1.md) (KV data-plane — functions can call KV)
- **Relates to**: [ADR-0019](0019-service-facade-pattern-kv.md) (the KV `Facade` this finally wires + exposes),
  [ADR-0064](0064-fn-to-fn-rpc-links.md) (**reuses** its per-sandbox worker-node local API — HTTP-over-UDS +
  connection-scoped caller identity), [ADR-0066](0066-kv-service-durable-engine.md) (the durable driver the
  Facade is backed by; **resolves** its deferred facade-selection wiring), [ADR-0028](0028-platform-control-plane-wiring.md)
  / [ADR-0018](0018-api-server-authn-rbac-admission.md) (the PDP the Facade authorizes against)

## Context & Need

ADR-0019 shipped the KV `Facade` (PDP-authorized, `<namespace>/<binding>/<key>`-tenanted) over the
`kvstore.KV` port — but it is **constructed nowhere in the running platform**: there is no function-facing
path to it, so a function cannot call KV today. ADR-0066 added the durable Badger driver and noted the facade
"wiring" but had no insertion point. This ADR adds the missing **function-facing KV data-plane** and, in doing
so, wires the Facade (with the config-selected driver) into the platform.

The mechanism already exists: ADR-0064 built a **per-sandbox worker-node local API** (HTTP-over-UDS) for
`context.invoke`, where the caller identity is **fixed at provisioning** (connection-scoped — a request body
can never name a different caller). KV is another verb namespace on that same local API: a function calls
`context.kv.get/put/del/list`, the SDK dials the per-sandbox socket, the handler routes `/kv/...` to the
Facade with the sandbox's own identity. No new transport, no new identity model.

## Scenarios

- **scenario: kv-put-get-roundtrip** — Given a function with a KV binding, When its handler calls
  `context.kv.put("b","k",v)` then `context.kv.get("b","k")`, Then it reads back `v` (over the worker-node
  local API → Facade → durable driver).
- **scenario: kv-tenancy-isolation** — Given two functions in different namespaces both writing key `"k"` to
  binding `"b"`, When each lists, Then each sees only its own value (the Facade's `<namespace>/<binding>/`
  tenant prefix; identity is connection-scoped, not client-asserted).
- **scenario: kv-authz-denied** — Given an identity the PDP denies KV access in a namespace, When it calls
  `context.kv.get`, Then the handler returns **403** (RFC 9457), the Facade's `authorize` having refused.
- **scenario: kv-list-prefix** — Given several keys under a binding, When the handler calls
  `context.kv.list("b","p")`, Then it gets exactly the binding's keys under `p` with the tenant prefix
  stripped.

## Scope

**In**: KV verbs on the worker-node local API (`GET/PUT/DELETE /kv/{binding}/{key}`, `GET /kv/{binding}` list);
constructing the KV `Facade` in the platform with the config-selected `kvstore.KV` driver (ADR-0066) + the
PDP authorizer; generalizing the per-sandbox local-API socket provisioning to "function uses the local API"
(declares links **or** KV bindings); the shim `context.kv` client (Node + Python) + its types.

**Out**: a *public* KV SDK/HTTP API outside the sandbox (the local API is sandbox-only, ADR-0064); the opt-in
DR/CDC (ADR-0067/0068, behind the driver); cross-namespace KV (needs `Grant`, V2); vector/secrets/config
data-planes (same pattern, later); changing the Facade's authz/tenancy logic (reused as-is).

## Constraints & Decision drivers

- **Reuse, don't reinvent** — the worker-node local API (ADR-0064), the Facade (ADR-0019), the durable driver
  (ADR-0066), and the PDP (ADR-0018) are all reused. **Zero new deps.**
- **Connection-scoped identity** — the caller `Ref` is the sandbox's fixed identity; the request never names a
  caller (the ADR-0064 security spine — no SSRF, no client-asserted identity).
- RFC 9457 on the wire; ctx-first; `api/fault`; pure-Go.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| A **public control-plane KV HTTP API** (functions call the daemon's API server) | One API surface | Functions are sandboxed clients with no control-plane credential; identity would be client-asserted (SSRF/spoofing); contradicts ADR-0064's connection-scoped model |
| A **separate KV UDS** per sandbox | Isolation | A second socket per sandbox to provision/mount for no benefit — the worker-node local API already is the per-sandbox, identity-bound channel |
| **KV verbs on the existing worker-node local API** ✅ | Reuses the transport + identity + Facade | One socket, one identity model, the Facade unchanged — **chosen** |

## Decision

Expose KV to functions as **verbs on the per-sandbox worker-node local API** (ADR-0064), routed to the
ADR-0019 `Facade`.

1. **Local-API KV routes** — extend `internal/workernode/local`'s handler with `GET/PUT/DELETE /kv/{binding}/{key}`
   and `GET /kv/{binding}?prefix=…` (list). The handler holds a **KV port** (a thin interface the `Facade`
   satisfies) and calls it with the **fixed caller `Ref`** as the identity + namespace — never read from the
   request. Errors map to RFC 9457 (`Forbidden→403`, `NotFound→404`, `Invalid→422`, `Internal→500`).
2. **Facade wiring (resolves ADR-0066's gap)** — the platform constructs `kv.NewFacade{KV: <driver>, Authorizer:
   <PDP>}` where `<driver>` is the `kvstore.engine`-selected driver (`memory` | `badger` at `<dataDir>/kv`,
   ADR-0066), closes it on shutdown, and passes the Facade into the worker-node local-API handler.
   - **Identity granularity (V1.1)** — the sandbox identity passed to the Facade is a **namespace-scoped**
     identity derived from the caller `Ref`'s namespace; the Facade authorizes `KindService` KV access at
     **namespace** granularity (a function reaches the KV in its own namespace). Fine-grained **per-workload /
     `Grant`-based** authz is **V2** — ADR-0018 deferred `Grant`/workload tokens and ADR-0019 deferred
     workload-`Grant` KV authz; this ADR stays consistent with that and does **not** invent a workload-identity
     model. The connection-scoped *namespace* is still un-spoofable (fixed at provisioning).
3. **Socket provisioning** — the per-sandbox local-API socket (today the invoke socket, `FUNCD_INVOKE_SOCKET`)
   is provisioned when a function **declares links OR has KV bindings** (generalized from links-only). One
   socket serves `/invoke/` and `/kv/`.
4. **Shim `context.kv`** — the Node + Python shims add `context.kv.{get,put,del,list}(binding, key[, value])`
   that dials the local-API socket (`GET/PUT/DELETE/GET /kv/...`), mirroring `context.invoke`. Typed; no
   client-asserted identity.

## Temporary workarounds

None.

## Contracts

```go
// internal/workernode/local — the handler gains KV routes, backed by a thin KV port the Facade satisfies.
type KV interface {
    Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, bool, error)
    Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, value []byte) error
    Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error
    List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error)
}
// NewHandler gains a kv param; caller.Namespace + the sandbox identity are passed to every KV call.
func NewHandler(caller Ref, res Resolver, inv Invoker, kv KV, identity auth.Identity, logger *slog.Logger) http.Handler
```

```ts
// shim: context.kv mirrors context.invoke (dials the worker-node local-API UDS).
interface KVClient {
  get(binding: string, key: string): Promise<Uint8Array | null>;
  put(binding: string, key: string, value: Uint8Array): Promise<void>;
  del(binding: string, key: string): Promise<void>;
  list(binding: string, prefix?: string): Promise<string[]>;
}
```

| consumes | exposes |
|---|---|
| the worker-node local API + socket (ADR-0064), the KV `Facade` (ADR-0019), the durable driver (ADR-0066), the PDP (ADR-0018) | `GET/PUT/DELETE /kv/{binding}/{key}` + list on the per-sandbox UDS; `context.kv` in the shims |
| config `kvstore.engine`/`dataDir` (ADR-0066) | the Facade wired to the selected driver; closed on shutdown |

## Implementation plan

**Files**
- `internal/workernode/local/kv.go` — the `/kv/...` routes + the `KV` port (one file; the handler in
  `local.go` gains the routes + the `kv`/`identity` params).
- `pkg/funcd` — construct the KV driver by `kvstore.engine`, build `kv.NewFacade`, pass it into the
  local-API handler, close the driver on shutdown; generalize socket provisioning to links-or-KV-bindings.
- `shim/nodejs/src/kv.ts` + `shim/python/src/funcd_shim/kv.py` — `context.kv`; regen `shim.mjs`/`pool.mjs`.
- `examples/js/kv-counter/` — a real example: a function that `context.kv.put/get/list`s a counter (the KV
  analogue of `examples/js/fn-to-fn`).

**Test plan** (one acceptance test per scenario, grep-able names)
- `TestScenarioKVPutGetRoundtrip`, `TestScenarioKVTenancyIsolation`, `TestScenarioKVAuthzDenied`,
  `TestScenarioKVListPrefix` — at the local-API handler level (Facade + memory driver), plus an in-process
  `pkg/funcd` KV e2e (a function drives `context.kv` end-to-end) mirroring `invoke_e2e_test.go`.

**Definition of done**: `just ci` green; the scenarios pass; the example runs in the e2e; the Facade is
constructed + closed by the platform; identity is connection-scoped (a request body cannot name a caller);
no new dep; blueprint KV-storage synced.

## Review checklist

- [ ] `/kv/{binding}/{key}` GET/PUT/DELETE + list on the worker-node local API; RFC 9457 errors.
- [ ] Identity is the fixed caller `Ref` (connection-scoped) — never read from the request body/query.
- [ ] The platform constructs `kv.NewFacade` with the `kvstore.engine`-selected driver and closes it on shutdown.
- [ ] Socket provisioned for links **or** KV bindings; one socket serves `/invoke/` + `/kv/`.
- [ ] `context.kv` in Node + Python shims; typed; regenerated bundles.
- [ ] The `examples/js/kv-counter` example interacts with KV and is exercised by the e2e.
- [ ] Facade authz/tenancy logic unchanged (ADR-0019 reused); no new dep; ctx-first; `api/fault`.

## Consequences

**Positive**: functions can finally use durable KV; the path reuses the ADR-0064 local API + identity model
(no new transport/identity); ADR-0066's driver is now actually wired + exercised; the Facade pattern proves
out for blob/secrets/config to follow.
**Negative (accepted)**: the worker-node local API grows a second verb namespace (more surface to keep
consistent); KV is sandbox-only (no public API — by design).
**Neutral**: the opt-in DR/CDC (ADR-0067/0068) sit behind the driver, unaffected by this data-plane.

## Open questions

- **Value size limits / streaming** for large KV values — a later refinement (small values for now).
- **Per-binding quotas** — V2 (with `Grant`/quota policy).
- **vector/secrets/config data-planes** — the same pattern, separate ADRs.

## References

- [ADR-0019](0019-service-facade-pattern-kv.md) — the KV Facade. · [ADR-0064](0064-fn-to-fn-rpc-links.md) — the
  worker-node local API + connection-scoped identity. · [ADR-0066](0066-kv-service-durable-engine.md) — the
  durable driver.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F38.
