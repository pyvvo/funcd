# ADR-0019: Service facade pattern + KV service — CRD + facade + reconciler + driver (`internal/kvstore`, `internal/services`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review **pass** (zero findings), see
  docs/reviews/adr-0019-implementation-claude-opus-4-8.md; DoD 5/5, 6 scenarios, OpenAPI regen, no new deps.
  **Reviewing 2026-06-14** — implemented: `internal/kvstore` (KV port + memory driver +
  contract suite) + `internal/services` (the `Dispatcher` type-router) + `internal/services/kv` (PDP+prefix
  Facade + KV `TypeHandler`) + `ServiceTypeKV`/`KVServiceSpec`; 6 scenarios pass, OpenAPI regenerated, four
  sub-checks green, no new deps. **Accepted 2026-06-14** after judge pass — no Blockers. Folded the judge's **Major**:
  the `Service` reconciler is now a **type-dispatcher** (one `controller.Reconciler` for `KindService` +
  a `TypeHandler` registry; KV is the first handler, P-O/P-P add handlers not reconcilers) — the only shape
  compatible with ADR-0015's one-reconciler-per-gvk, resolving the blob/secrets coexistence question. Minors:
  `NewFacade` naming, `List` strips the tenant prefix + the KV-op→`auth.Verb` mapping, split `FacadeDeps`. Also
  aligned the package path to the blueprint's plural **`internal/services`** (was singular — a near-homonym
  hazard). Decision: `kvstore` port + in-memory driver + the facade-PEP pattern + the Service dispatcher;
  JetStream/db-layer drivers + workload-`Grant` authz deferred. Blueprint `services/` tree synced.)
- **Deciders**: green-0-rabbit
- **Tags**: service, facade, kv, kvstore, reconciler, pattern, data-plane, control-plane
- **Realizes**: [FEAT-0000/F14](../feat/0000-feat-v1.md) (KV service: port + facade + controller; drivers; pooled buckets, namespace-scoped bindings)
- **Relates to**: [ADR-0015](0015-controller-engine.md) (the reconcile engine — the **Service reconciler**
  registers a `Reconcile` here; one reconciler per gvk, so this owns the `Service` kind), [ADR-0018](0018-api-server-authn-rbac-admission.md)
  (the `auth.Authorizer` PDP the facade calls per request — the **service-facade PEP**),
  [ADR-0006](0006-store-database-layer-port.md) (the store the reconciler reads desired `Service`s from /
  writes status to; the database layer KV is conceptually built on), [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `Service` kind — this ADR adds its `kv` type + binding spec, F14-owned),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (port+driver, `New(Deps)`, `api/fault`, ctx-first,
  no globals, no `any`, no mocks), [blueprint.md — Services / KV / Internal IAM](../../blueprint.md).
  **New deps: none** (V1 KV is the in-memory driver; JetStream/database-layer drivers are deferred).

## Context & Need

The blueprint defines a service as a **uniform four-part shape**: "**Service = CRD + facade + controller +
driver**: a service instance/binding is a `Service` resource; the controller drives the upstream through the
SDK (create/bind/teardown a bucket, KV namespace, …); the in-process **facade** is what functions actually
call, enforcing per-request authorization via the PDP." And it is emphatic that **every** service reuses this
one shape: "the same shape applies to KV, vector, secrets, config — only the driver SDK changes." KV is the
**first** instance, so F14 must both (a) establish the reusable pattern and (b) deliver KV on it.

Three blueprint contracts pin the facade: tenants are **multiplexed by key prefix** `<namespace>/<binding>/<key>`;
each access is **PDP-authorized** (the facade is a PEP — "service facades (fn → kv/blob/…)" call the one
`auth.Authorizer`); and **binding a service in `Function.spec.services` is the grant** for that instance
(default-deny otherwise). The controller half is non-negotiably the one engine (ADR-0015): the Service
reconciler contributes only `Reconcile`.

**Purpose**: ship (1) the **`kvstore` port** — `Get/Put/Delete/List` over namespace-scoped keys — with an
**in-memory driver** and a contract suite; (2) the **service facade pattern** — a `kv.Facade` that wraps the
port with **per-request PDP authorization** + **`<namespace>/<binding>/<key>` prefixing** (the shape every
service copies); (3) the **`Service` reconciler** (registered on the ADR-0015 engine) that provisions a KV
binding and writes `Status.Phase=Ready`; and (4) the `Service` **`kv` type + binding spec** (F14-owned).
Callers: a function (via the facade, in-process for V1) reads/writes KV; the composition root (ADR-0014)
constructs the driver + facade + registers the reconciler. Conformance is mechanical: a `Service{type:kv}`
reconciles to Ready; a facade Put/Get round-trips within a binding; a cross-namespace/unbound access is
denied by the PDP; keys are prefixed so two namespaces never collide.

## Scenarios

- `scenario: kv-roundtrips` — **Given** the in-memory `kvstore` driver, **when** a value is Put under a key
  then Got, **then** the same bytes return; a Delete makes it absent; List returns the keys under a prefix.
- `scenario: kvstore-contract-holds` — **Given** any `kvstore` driver, **when** the shared contract suite
  runs (put/get/overwrite/delete/list/missing-key), **then** every assertion holds (the contract the
  JetStream/database-layer V2 drivers will also satisfy).
- `scenario: facade-prefixes-by-namespace-and-binding` — **Given** the KV facade, **when** namespace `team-a`
  binding `cache` Puts key `k` and namespace `team-b` binding `cache` Puts key `k`, **then** the two values
  are independent (keys are stored as `team-a/cache/k` vs `team-b/cache/k` — no cross-tenant collision).
- `scenario: facade-authorizes-each-access` — **Given** the KV facade wired to the PDP, **when** an identity
  not permitted in the target namespace calls Get/Put, **then** the facade denies it (`fault.Forbidden`)
  before touching the store — the facade is a PEP.
- `scenario: service-reconciles-to-ready` — **Given** a `Service{type:kv}` created in the store, **when** the
  Service reconciler runs under the engine, **then** it provisions the binding and writes
  `Status.Phase=Ready` (the CRD→reconciler→driver half of the pattern).
- `scenario: service-reconciler-ignores-other-types` — **Given** a `Service` whose `type` is not `kv`, **when**
  the KV reconciler runs, **then** it is a no-op (each service type owns its own reconciler slice).

## Scope

**In**:
- **`internal/kvstore`**: the **`KV` port** (`Get`/`Put`/`Delete`/`List`, ctx-first, `api/fault`); the
  **in-memory driver** (`internal/kvstore/memory`); a **contract suite** (`kvstorecontract`).
- **`internal/services`**: the **`Dispatcher`** — the single `controller.Reconciler` for `KindService` that
  routes by `spec.type` to a registered **`TypeHandler`** (no-op for unregistered types) + the shared status
  write-back. The seam P-O/P-P extend by registering a handler.
- **`internal/services/kv`**: the **`Facade`** (the function-facing API: `Get`/`Put`/`Delete`/`List` scoped to
  `(namespace, binding)`, PDP-authorized, key-prefixed, results prefix-stripped) and the **KV `TypeHandler`**
  (`NewHandler`) the dispatcher routes `type:kv` to.
- **`Service` spec**: add `ServiceTypeKV` + a `KVServiceSpec` (the bucket/binding config) to `ServiceSpec`
  (F14-owned, as the type reserves).
- **The reusable pattern, documented**: facade-wraps-driver-with-PDP-and-prefix + Service-reconciler-per-type
  — the shape P-O (blob), P-P (secrets), and later services copy.

**Out (deferred, blueprint-sanctioned)**:
- **JetStream-backed + database-layer KV drivers** — the production drivers (the blueprint lists
  cloud/JetStream/memory); V1 ships the **in-memory** driver + the contract suite the others inherit. Bus
  (ADR-0008) integration for durable KV is a follow-up.
- **`Grant`-based cross-namespace / workload-identity authz** — V2 (ADR-0018 deferred `Grant`/workload tokens);
  V1 facade authz uses the `auth.Authorizer` PDP with the **caller's `Identity`** (namespace-scoped RBAC). The
  `Function.spec.services` binding-as-grant is **recorded** (the binding exists) but workload-token enforcement
  is V2.
- **Atomic / CAS KV operations, TTL, watch** — V1 is get/put/delete/list; atomicity is a driver-capability
  follow-up.
- **The worker/sandbox transport** (how a sandboxed function reaches the in-process facade — netns, the
  worker's sandbox-local API) — V1 wires the facade **in-process**; the sandbox transport is P-M/worker.
- **Vector / graph / config services** — V2/V3; they reuse this pattern.

## Constraints & Decision drivers

- **C1 — Service = CRD + facade + controller + driver (blueprint, the uniform shape)**: KV must be built as
  this shape, not a bespoke KV subsystem — so P-O/P-P/etc. copy it. The pattern *is* the deliverable.
- **C2 — the facade is a PEP (blueprint security model)**: every facade access calls the one `auth.Authorizer`
  PDP (ADR-0018) before touching the driver. Default-deny.
- **C3 — tenant isolation by key prefix**: `<namespace>/<binding>/<key>` — pooled storage, no cross-tenant
  bleed; a driver never sees an un-prefixed key.
- **C4 — one reconciler per gvk (ADR-0015) → a type-dispatcher**: there is exactly **one** `Service`
  reconciler (a `Dispatcher`) registered for `KindService`; it routes by `spec.type` to a registered
  `TypeHandler`. KV registers the first handler; P-O/P-P register theirs on the **same** dispatcher. Each
  service must **never** register its own `Service` reconciler — that would collide on the gvk (the engine's
  `Register` is a map; last-write-wins). This is the only shape compatible with ADR-0015.
- **C5 — ADR-0002 conventions**: port + one-file driver in its own subpackage + contract suite; `New(Deps)`;
  typed enums; ctx-first; `api/fault`; no globals; no `any`; no mocks (real in-memory driver + a real PDP).

## Alternatives considered

**Service architecture** (driver: the blueprint's uniform four-part shape):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **CRD + facade + reconciler + driver, KV as the first instance defining the pattern** | the blueprint's pick; every later service copies it; facade is the PDP PEP; the reconciler is one engine slice | a fair bit of structure for one service — but that structure is reused by P-O/P-P/… | **chosen** |
| A bespoke KV subsystem (no Service CRD / no facade) | less code for KV alone | breaks the uniform shape; blob/secrets would each reinvent; no PDP PEP, no declarative binding | rejected (blueprint-violating, non-reusable) |
| Functions call the KV **driver** directly (no facade) | one fewer layer | no per-request authz, no tenant prefixing, no pooling — the exact footguns the facade exists to remove | rejected (security + isolation) |

**KV driver for V1**: **in-memory** + the contract suite — chosen (testable now, zero-dep); JetStream/
database-layer are the deferred production drivers behind the same port (the contract suite pins their
behavior). **Authz model**: the facade calls the **`auth.Authorizer` PDP** with the caller's `Identity`
(ADR-0018 RBAC) — chosen; `Grant`/workload-token binding-as-grant is V2 (recorded, not enforced yet).

## Decision

### 1. The `kvstore` port + in-memory driver (`internal/kvstore`)
```
KV { Get(ctx, key) ([]byte, bool, error); Put(ctx, key, val) error; Delete(ctx, key) error; List(ctx, prefix) ([]string, error) }
```
Keys are opaque strings (the facade supplies the prefixed key); values are `[]byte`. The **in-memory** driver
(`internal/kvstore/memory`) is a mutex-guarded `map[string][]byte`; a missing Get returns `(nil, false, nil)`.
A `kvstorecontract` suite asserts the port guarantee against any driver.

### 2. The KV facade (`internal/services/kv`) — the reusable PEP shape
`Facade` is what a function calls. Constructed with `NewFacade(Deps{KV, Authorizer, Logger})`. Every method
takes the caller's `auth.Identity` + the `(namespace, binding, key)`:
1. **authorize** — `Authorizer.Authorize(ctx, {Identity, verb, KindService, namespace})`, where the KV op maps
   to the verb (`Get`/`List` → `VerbGet`/`VerbList`, `Put` → `VerbUpdate`, `Delete` → `VerbDelete`); deny →
   `fault.Forbidden`.
2. **prefix** — the driver key is `<namespace>/<binding>/<key>` (tenant isolation); `List` prepends the prefix
   to the caller's `prefix` arg and **strips `<namespace>/<binding>/` from returned keys** so the function only
   ever sees its own key space (never the pooling layout).
3. **delegate** — call the `KV` driver with the prefixed key.
This is the shape P-O (blob) and P-P (secrets) copy: wrap a driver with PDP + prefix.

### 3. The `Service` reconciler — ONE type-dispatcher for `KindService` (`internal/services`)
ADR-0015 is **one `Reconciler` per gvk**, so there can be exactly **one** `Service` reconciler. It is a
**type-dispatcher**: `internal/services` exposes a `Dispatcher` (a `controller.Reconciler`) holding a
`map[v1.ServiceType]TypeHandler`. On reconcile it reads the `Service` from the store and dispatches to the
handler registered for `spec.type`; an **unregistered type is a no-op** (so partially-built platforms don't
fail). A `TypeHandler` is the per-service-type logic — `Reconcile(ctx, *v1.Service) (controller.Result, error)`
— that provisions the instance/binding and returns; the dispatcher does the shared status write-back
(`Status.Phase=Ready` on success, idempotent — the ADR-0015 contract).

**KV is the first handler** (`internal/services/kv.NewHandler`): for `type:kv` it validates the binding spec
(the in-memory driver needs no external provisioning). **P-O (blob) and P-P (secrets) register their own
`TypeHandler`s on the same `Dispatcher`** — they add handlers, never new `Service` reconcilers, so they never
collide on the gvk. The composition root builds the `Dispatcher` with all available handlers and registers it
once on the engine.

### 4. The `Service` `kv` spec (F14-owned)
```go
const ServiceTypeKV ServiceType = "kv"
type ServiceSpec struct {
	Type ServiceType   `json:"type,omitempty"`
	KV   *KVServiceSpec `json:"kv,omitempty"` // set when Type == kv
}
type KVServiceSpec struct {
	Binding string `json:"binding"` // the binding name functions reference (the key prefix segment)
}
```

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **In-memory KV driver only** | testable, zero-dep; proves the port + facade + reconciler | JetStream + database-layer drivers (the blueprint's production set) land behind the port + contract suite |
| **Facade authz is RBAC (caller Identity), not workload `Grant`** | `Grant`/workload tokens are V2 (ADR-0018) | V2 internal-IAM makes `Function.spec.services` the enforced grant via short-lived workload tokens through the same PDP |
| **Facade is in-process; no sandbox transport** | functions/sandboxes are P-M; V1 wires the facade in-process | P-M/worker exposes the sandbox-local API that proxies to the facade |
| **No atomic/CAS/TTL/watch** | V1 KV is get/put/delete/list | a KV-capability follow-up adds CAS/TTL/watch where a driver supports them |

## Contracts

### The KV port (`internal/kvstore/kvstore.go`)
```go
package kvstore

import "context"

// KV is the key/value port: namespace-agnostic get/put/delete/list over opaque
// string keys (the facade supplies the tenant-prefixed key) and []byte values.
type KV interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) (keys []string, err error)
}
```

### The in-memory driver (`internal/kvstore/memory/memory.go`)
```go
// New returns an in-memory KV driver (mutex-guarded map). For dev/e2e/tests.
func New() kvstore.KV
```

### The Service dispatcher (`internal/services`) — one reconciler for KindService
```go
package services

// TypeHandler is the per-service-type reconcile logic (KV, blob, secrets, …). It
// provisions the instance/binding for one Service of its type; the Dispatcher does
// the shared status write-back.
type TypeHandler interface {
	Type() v1.ServiceType
	Reconcile(ctx context.Context, svc *v1.Service) (controller.Result, error)
}

// NewDispatcher builds the single Service reconciler (register once on the engine for
// KindService). It routes each Service to the handler for spec.type; an unregistered
// type is a no-op. Store is used for the read + the idempotent status write-back.
func NewDispatcher(st store.Store, logger *slog.Logger, handlers ...TypeHandler) (controller.Reconciler, error)
```

### The KV facade + handler (`internal/services/kv`)
```go
// FacadeDeps configures the KV facade (the PEP). ReconcilerDeps/NewHandler take what the
// reconcile half needs — kept separate so neither carries the other's unused deps.
type FacadeDeps struct {
	KV         kvstore.KV
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Facade is what a function calls (PDP-authorized, namespace/binding-prefixed).
type Facade struct { /* unexported */ }
func NewFacade(d FacadeDeps) (*Facade, error)
func (f *Facade) Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, bool, error)
func (f *Facade) Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, value []byte) error
func (f *Facade) Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error
func (f *Facade) List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error)

// NewHandler returns the KV services.TypeHandler (Type() == ServiceTypeKV) for the dispatcher.
func NewHandler() services.TypeHandler
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/store` (reconciler), `internal/auth` (PDP), `internal/controller` (Reconciler seam), `api/types`, `api/fault`, stdlib `sync` | the facade + reconciler are the pattern |
| Adds (lib) | none | in-memory driver; JetStream/database-layer deferred |
| Exposes | `kvstore.KV` + memory driver; `kv.Facade` + `kv.NewReconciler`; `ServiceTypeKV` + `KVServiceSpec` | facade called by functions (P-M wires the transport); reconciler registered by P-I |

## Implementation plan

1. **`api/types/v1alpha1/service.go`** — add `ServiceTypeKV` + `KVServiceSpec` + `ServiceSpec.KV` (keep the
   roundtrip + spec-gen green; regenerate the OpenAPI for the new spec field).
2. **`internal/kvstore/kvstore.go`** — the `KV` port.
3. **`internal/kvstore/memory/memory.go`** — the in-memory driver (one file, its own subpackage).
4. **`internal/kvstore/kvstorecontract/contract.go`** — the shared contract suite.
5. **`internal/services/dispatcher.go`** — `TypeHandler` + `NewDispatcher` (one `controller.Reconciler` for
   `KindService`: read → route by `spec.type` to the handler / no-op if unregistered → idempotent status Ready).
6. **`internal/services/kv/kv.go`** — `FacadeDeps`, `Facade` (authorize→prefix→delegate, `List` strips the
   prefix), `NewHandler` (the KV `TypeHandler`: validate the binding spec).
7. **Test plan** (one named test per Scenario; real in-memory KV + real rbac PDP + real store, no mocks):
   - `internal/kvstore/memory/memory_test.go` → `kv-roundtrips`, `kvstore-contract-holds` (runs the suite).
   - `internal/services/kv/kv_test.go` → `facade-prefixes-by-namespace-and-binding`,
     `facade-authorizes-each-access` (a viewer/unscoped identity → Forbidden).
   - `internal/services/dispatcher_test.go` → `service-reconciles-to-ready` (a `Service{type:kv}` → Ready via
     the dispatcher+KV handler), `service-reconciler-ignores-other-types` (an unregistered type → no-op).
8. **Definition of done**: `just ci` green (four sub-checks); KV round-trips + contract passes; the facade
   prefixes + authorizes; the reconciler drives `Service{type:kv}` to Ready and no-ops other types; OpenAPI
   regenerated for the spec field; no new dependency; no globals; no `any`; no identity/path leak.

## Review checklist

- [ ] `kvstore.KV` port + **in-memory driver** (one file, own subpackage) + **contract suite** the driver runs
      (`kvstore-contract-holds`, `kv-roundtrips`).
- [ ] The **facade** authorizes every access via the `auth.Authorizer` PDP **before** the driver
      (`facade-authorizes-each-access`, `fault.Forbidden`) and **prefixes** keys `<ns>/<binding>/<key>`
      (`facade-prefixes-by-namespace-and-binding` — no cross-tenant collision).
- [ ] The **Service reconciler** is an ADR-0015 `Reconciler` for `KindService` that drives `type:kv` to
      `Status.Phase=Ready` (`service-reconciles-to-ready`) and **no-ops** other types
      (`service-reconciler-ignores-other-types`); idempotent status write-back.
- [ ] `ServiceTypeKV` + `KVServiceSpec` added; roundtrip + OpenAPI staleness green.
- [ ] `New(Deps)`, ctx-first, `api/fault`, `slog`, no globals, **no `any`**, **no new dependency**; the
      JetStream/database-layer drivers + workload-`Grant` authz are documented deferrals; no identity/path leak;
      every Scenario a named passing test.

## Consequences

- (+) The platform gets its **reusable service shape** (CRD + facade + reconciler + driver) *and* its first
  service (KV) on it — P-O (blob) and P-P (secrets) now copy a proven pattern instead of reinventing.
- (+) The **facade is a real PEP**: every KV access is PDP-authorized + tenant-prefixed — the blueprint's
  security/isolation model, in-process and audited, not hand-rolled NATS ACLs.
- (+) **No new dependency**; the in-memory driver + contract suite keep the production (JetStream/database-layer)
  drivers a clean swap.
- (−) V1 ships **one KV driver** (in-memory) — durable KV waits on the production drivers; mitigated by the
  contract suite pinning the guarantee.
- (−) Facade authz is **RBAC, not workload `Grant`** — fn→service identity is V2; V1 uses the caller's
  `Identity`. Bounded, documented.
- (note) **Roadmap build edges**: P-N's real build deps are `ADR-0003` (Service kind), `ADR-0006` (store),
  `ADR-0008`?—**no**: V1 KV is in-memory, so the bus edge is *not* a build edge (JetStream KV is the deferred
  driver, like P-J's dropped bus edge). Real edges: `ADR-0003`, `ADR-0006`, `ADR-0015` (the engine), `ADR-0018`
  (the PDP). The Step-6 reconcile sets these and drops `ADR-0008`.

## Open questions

| Question | Where it gets answered |
|---|---|
| JetStream-backed + database-layer KV drivers | a follow-up (the production drivers behind the port) |
| Workload-identity `Grant` enforcement (`Function.spec.services` as the enforced grant) | **V2 internal IAM** |
| Atomic/CAS/TTL/watch KV operations | a KV-capability follow-up |
| The sandbox→facade transport (worker sandbox-local API) | **P-M / worker** |
| Do blob/secrets reconcilers compose into one `Service` reconciler or register per-type? | **resolved here**: one `Dispatcher` for `KindService` + a `TypeHandler` registry — P-O/P-P register handlers, not reconcilers (the only shape compatible with ADR-0015's one-reconciler-per-gvk) |

## References

- [blueprint.md](../../blueprint.md) — "Services / Service architecture" (CRD + facade + controller + driver;
  the same shape for KV/blob/secrets), "KV storage" (get/put/delete/list, drivers), "Internal IAM" (service
  facades are PEPs; key-prefix tenant isolation; binding-as-grant).
- [ADR-0015](0015-controller-engine.md) — the engine the Service reconciler registers on.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) — the `auth.Authorizer` PDP the facade calls.
- [ADR-0006](0006-store-database-layer-port.md) — the store / database layer.
- [ADR-0003](0003-resource-model-and-api-typing.md) — the `Service` kind (this ADR adds its `kv` type).
