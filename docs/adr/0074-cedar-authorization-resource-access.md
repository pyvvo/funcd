# ADR-0074: Cedar authorization for resource access — per-function principals, Policy resources, KV first

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23, cedar-go facts WebFetch-verified — Apache-2.0/pure-Go/v1.8.0 +
  entities-with-parents/PolicySet/Authorize. No Blockers; folded 2 Majors: made the system **genuinely
  default-deny** (dropped the shipped seed read-permit; examples carry their own read `Policy`; the coarse-read
  retirement is now real, not cosmetic) and **pinned the hot-path entity strategy** (cache the compiled
  PolicySet; resolve only request-relevant entities — principal + resource + parents — never a full-store
  rebuild); + minors (owner is a `Function` entity-ref; impl sequence + `authcontract` stays green; renumber).)
- **Deciders**: green-0-rabbit
- **Tags**: auth, authz, cedar, pdp, identity, policy, kv
- **Realizes**: [FEAT-0001/F43](../feat/0001-feat-v1.1.md) (Cedar fine-grained resource authorization)
- **Relates to**: [ADR-0018](0018-api-server-authn-rbac-admission.md) (the `auth.Authorizer` PDP port + the
  V1 rbac driver — this adds the **cedar** driver the port already anticipates), [ADR-0073](0073-kv-bindings-and-subdomains.md)
  (KV — **retires its coarse-read temporary workaround**), [ADR-0069](0069-kv-data-plane.md) (the local-API
  caller `Ref` that becomes the per-function principal), [ADR-0065](0065-metastore-badger-engine.md) (the
  metastore where `Policy` resources persist), [ADR-0063](0063-admission-framework.md) (Policy validity admission)

## Context & Need

funcd's PDP (`auth.Authorizer`, ADR-0018) answers authorization for every PEP, but two gaps block
*fine-grained* access: (1) the principal for a function's data-plane calls is **namespace-scoped** (the
ADR-0069 sandbox identity is a generic per-namespace `developer`), so the PDP can't tell `customers-svc` from
`reporting`; (2) the `auth.Request` is **coarse** — `(verb, kind, namespace)`, no specific resource — so it
can't express "function F may `kv::read` `KVTable orders/customers`". ADR-0073 left KV's per-function read as
an explicit coarse-namespace **temporary workaround** pending this ADR. The platform already chose its engine:
**cedar-go** (the blueprint's policy engine; the `auth.Authorizer` doc names cedar as the deferred driver;
`EgressPolicy` is meant to compile to Cedar). Cedar is purpose-built for authz (RBAC **and** ABAC), pure-Go,
Apache-2.0, and supports an entity hierarchy (`KVTable in KVStore`) — exactly the per-resource model needed.
This ADR adds the **cedar driver** behind the existing port, a **per-function principal**, a resource/action
request model, and a **`Policy`** resource (Cedar policies in the metastore), with **KV as the first
consumer**. rbac stays the driver for control-plane CRUD; unifying all authz under Cedar is the trajectory,
not this ADR.

## Scenarios

- **scenario: per-function-principal** — Given a function `F`'s sandbox, When it calls `context.kv`, Then the
  PDP request carries `principal = Function::"<ns>/F"` (connection-scoped from the `Ref`, never client-asserted).
- **scenario: cedar-permits-read** — Given a `Policy` permitting `Function::"default/reporting"` to `kv::read`
  `KVStore::"default/orders"`, When `reporting` reads any table in `orders`, Then the PDP **allows** it (the
  `KVTable in KVStore` hierarchy covers all tables).
- **scenario: cedar-default-deny** — Given no `Policy` granting `F` `kv::read` on a table it binds, When `F`
  reads it, Then the PDP **denies** (`Forbidden`) — even though the binding resolved (binding = naming;
  authorization = the PDP).
- **scenario: owner-write-via-policy** — Given table `orders/customers` `owner: customers-svc`, When
  `customers-svc` writes it Then allowed, and another principal writing it is denied — expressed as the Cedar
  rule `forbid(kv::write) unless principal == resource.owner`.
- **scenario: policy-validity** — Given a `Policy` whose Cedar text fails to parse (or references an unknown
  action/entity-type in the schema), When it is applied, Then admission rejects it (`Invalid`).
- **scenario: entities-from-resources** — Given existing `KVStore`/`KVTable`/`Function` resources, When the PDP
  evaluates, Then it builds the Cedar **entity store from those resources** (owner/resourceGroup/namespace as
  attributes; `KVTable in KVStore` parents) — no duplicate entity persistence.
- **scenario: rbac-unaffected** — Given a control-plane CRUD request (Create a Function), When authorized, Then
  the **rbac** driver still decides it (admin/developer/viewer) — Cedar governs data-plane *resource* access only.

## Scope

**In**: the **cedar driver** (`internal/auth/cedar`, cedar-go) behind `auth.Authorizer`; a **per-function
principal** (the sandbox identity carries the function from the `Ref`); a **resource/action** extension to
`auth.Request` (an optional typed resource entity + a `cedar`-namespaced action so a PEP can ask per-object
questions, back-compatible with the verb/kind/namespace coarse form); the **`Policy`** CRD (namespaced; spec =
Cedar policy text) persisted in the metastore + a validity admission; **entity materialization** from the
metastore's resources; the **KV facade** as the first PEP — replace ADR-0073's coarse same-namespace read with
a Cedar `kv::read`/`kv::write` decision (owner-write becomes a Cedar `forbid`). cedar-go added to `go.mod`
(Apache-2.0, pure-Go).

**Out**: migrating **control-plane CRUD** authz from rbac to Cedar (rbac stays; a later ADR may unify); the
other PEP consumers — **egress** (`EgressPolicy`→Cedar), **secrets**, **blob**, **invoke** (`link::invoke`) —
each a follow-on behind this same model; cross-namespace KV sharing (a Cedar policy once this lands, but the
KV *binding* stays same-namespace per ADR-0073); policy **templates** (cedar-go lacks them — roles are modeled
as group entities); a policy authoring UI/CLI beyond `funcdcli apply`.

## Constraints & Decision drivers

- **One PDP, one port** — cedar is a *driver* behind `auth.Authorizer` (ADR-0018), not a parallel path; PEPs
  call `Authorize` unchanged in shape.
- **Default-deny** — no permitting `Policy` ⇒ denied (Cedar is default-deny; a `forbid` always wins).
- **Embed-first, pure-Go, single binary** — cedar-go is pure-Go + Apache-2.0 (verified); no service, no cgo.
- **No duplicate state** — Cedar **entities** are materialized from the resources already in the metastore;
  only **policies** are new persisted state (a `Policy` resource).
- **Connection-scoped principal** — the function principal comes from the provisioned `Ref`, never the request
  body (the ADR-0064/0069 anti-spoof property).

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| Engine | **cedar-go** (RBAC+ABAC, pure-Go, Apache-2.0, entity hierarchy, formal analysis) | **OPA/Rego** — embeddable but general-purpose, no authz-specific guarantees; **OpenFGA/SpiceDB** — Zanzibar ReBAC but **separate services** (datastore + server) that break embed-first/single-binary; **hand-rolled** — that was `Grant` (ADR-0072), the mistake this corrects |
| Policy storage | **`Policy` resource in the metastore** (spec = Cedar text); entities materialized from resources | A separate policy store — second datastore, against "everything is a resource"; entities-as-CRDs — duplicates resource state |
| Scope of cedar | **Data-plane resource access (KV first); rbac keeps control-plane CRUD** | Replace rbac wholesale now — a large migration of every CP decision; incremental is safer, unify later |
| Principal | **Per-function (`Function::"<ns>/<name>"`) from the `Ref`** | Namespace-scoped — can't distinguish functions; client-asserted — spoofable |

## Decision

Add **cedar-go** as a driver behind `auth.Authorizer`; give functions a per-function principal; model resource
access as Cedar; persist policies as a `Policy` resource. **KV is the first PEP.**

1. **Per-function principal** — the local-API sandbox identity (ADR-0069) carries the function from the `Ref`
   (`Identity.Subject = "<ns>/<function>"`, a new `Identity.Principal` typed field for Cedar; `Role`/`Namespaces`
   unchanged so rbac still works). Connection-scoped, never client-asserted.
2. **Request model** — `auth.Request` gains an optional `Resource` (a typed entity ref: kind + `<ns>/<name>` +
   optional sub-path like a table) and an optional `Action` (`cedar`-namespaced, e.g. `kv::read`). A PEP that
   sets them asks Cedar a per-object question; a PEP that leaves them (the existing `Verb`/`Kind`/`Namespace`
   form) routes to rbac as today. Back-compatible.
3. **cedar driver** (`internal/auth/cedar`) — implements `Authorizer`: builds the Cedar **request**
   (principal, action, resource, context) and evaluates `Authorize(policySet, entities, request)` →
   `Decision`. **Default-deny** (no permit ⇒ deny; a `forbid` always wins). **Hot-path strategy**: the
   **compiled PolicySet is cached** (recompiled on a `Policy` change) and the **entities resolved per call are
   only the request-relevant set** — the principal `Function`, the resource `KVTable`, and its parent
   `KVStore` — fetched from the metastore (small, cached), **never a full-store rebuild per op**. Entity
   attributes: `KVStore`/`KVTable` carry `namespace`/`resourceGroup`; `KVTable.owner` is a **`Function`
   entity-reference** (so `principal == resource.owner` compares entities); `KVTable in KVStore` via parents.
4. **`Policy` resource** — namespaced CRD, `spec.cedar` = the policy text (one or more `permit`/`forbid`
   statements). A **policy-validity** admission (ADR-0063) parses it + checks actions/entity-types against the
   curated schema; invalid ⇒ `fault.Invalid`. Stored in the metastore (ADR-0065); the cedar driver loads +
   compiles all `Policy` resources (cached, recompiled on change).
5. **KV consumer** — the facade (ADR-0073) keeps the `spec.kv` **binding** (naming) but replaces its coarse
   same-namespace read + owner-write check with a **PDP call**: `Authorize(principal=Function, action=kv::read|
   kv::write, resource=KVTable in KVStore)`. **Reads require a permitting `Policy`** (default-deny — the
   examples carry one); **writes** are governed by the built-in `forbid(action == kv::write) unless principal
   == resource.owner;` (single-writer). ADR-0073's coarse-read workaround is **genuinely retired** (binding
   alone no longer reads).
6. **Curated action/entity schema** — a fixed Cedar schema (actions `kv::read`,`kv::write`; entity types
   `Function`,`KVStore`,`KVTable`; `KVTable in KVStore`) ships with the driver; `Policy` text is validated
   against it. Future actions (`link::invoke`,`egress::send`,…) extend the schema in their consumer ADRs.

## Temporary workarounds

None. The system is **genuinely default-deny**: with no permitting `Policy`, a `kv::read` is denied (Cedar
default-deny). This **truly retires** ADR-0073's coarse-read workaround rather than preserving it under a new
mechanism — a function that read by binding alone now needs a `Policy`. The kv-counter **examples carry their
own read `Policy`** (demonstrating the model), and the owner-write `forbid(kv::write) unless principal ==
resource.owner` is a **built-in** rule (the consistency model, always on — not a policy operators write). A
broad "every function may read stores in its own namespace" compat policy is available as an **explicit,
opt-in** `Policy` an operator may apply (e.g. to ease migration) — it is **never shipped enabled**, so the
out-of-the-box posture is default-deny.

## Contracts

```go
// internal/auth — Identity + Request gain the per-function principal + per-object fields (back-compatible).
type Identity struct {
	Subject    string                // unchanged (now "<ns>/<function>" for sandbox principals)
	Role       Role                  // unchanged (rbac)
	Namespaces []v1.NamespaceName    // unchanged
	Principal  *EntityRef            // NEW: the Cedar principal (e.g. Function::"<ns>/<name>"); nil ⇒ rbac-only
}
type EntityRef struct { Type v1.Kind; Namespace v1.NamespaceName; Name v1.ObjectName; Path string } // Path = sub-resource, e.g. a table
type Action string // cedar-namespaced, e.g. "kv::read", "kv::write"
type Request struct {
	Identity  Identity
	Verb      Verb                   // unchanged coarse form (→ rbac)
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Action    Action                 // NEW: set ⇒ Cedar per-object decision
	Resource  *EntityRef             // NEW: the target entity (with optional sub-path)
}
// Authorizer unchanged. A routing authorizer dispatches Action-bearing requests to the cedar driver,
// others to rbac.
```

```go
// internal/auth/cedar — the driver (cedar-go behind the port).
func New(deps Deps) (auth.Authorizer, error) // Deps: an EntityProvider, a Policy source, logger
// EntityProvider resolves ONLY the request-relevant entities (principal + resource + parents) from the
// metastore — not the whole store — so a per-op Authorize is cheap. The compiled cedar.PolicySet is cached
// (recompiled on a Policy change). Authorize builds the cedar request + evaluates Authorize(ps, entities, req).
type EntityProvider interface {
	EntitiesFor(ctx context.Context, principal, resource EntityRef) (cedartypes.EntityMap, error)
}
```

```go
// api/types/v1alpha1 — the Policy resource.
type Policy struct { TypeMeta; ObjectMeta; Spec PolicySpec }
type PolicySpec struct { Cedar string `json:"cedar"` } // one or more permit/forbid statements (validated)
func (p *Policy) Validate() error // structural; the Cedar parse + schema check is the policy-validity admission
// register KindPolicy.
```

| consumes | exposes |
|---|---|
| **cedar-go** (Apache-2.0, pure-Go) — policy compile + evaluate + entities | the `cedar` `Authorizer` driver behind ADR-0018's port |
| the metastore (ADR-0065) — `Function`/`KVStore`/`KVTable` (entities) + `Policy` (policies) | per-function, per-resource authorization decisions |
| the ADR-0069 local-API `Ref` | the connection-scoped per-function principal |

## Implementation plan

**Sequence** (build in this order): per-function identity → the `Request` model (`Action`/`Resource`/`Principal`,
confirm `internal/auth/authcontract` stays green for rbac + add a routing-authorizer contract case) → the cedar
driver + curated schema + `EntityProvider` → the `Policy` CRD + validity admission → the KV consumer + examples.

**Files**: `go get github.com/cedar-policy/cedar-go` (Apache-2.0; pin). `internal/auth/{authorizer.go (add
EntityRef/Action + Request.Action/Resource + Identity.Principal), routing.go (dispatch Action→cedar, else
rbac)}`; `internal/auth/cedar/{cedar.go (driver), schema.go (curated schema), entities.go (materialize from a
store reader), policies.go (load+compile Policy resources, cached)}`; `api/types/v1alpha1/policy.go` +
`KindPolicy` registration + OpenAPI regen; `internal/controlplane/admission/policy.go` (policy-validity:
parse + schema-check) + wiring + CRUD wiring for `Policy`; `internal/workernode/local/kv.go` +
`internal/services/kv` (set the per-function principal + the `kv::read`/`kv::write` action + the `KVTable`
resource on the PDP call, replacing the coarse read/owner check); `pkg/funcd`/`cmd/funcd` (build the routing
authorizer with the cedar driver over the metastore; ship the seed policy); the kv-counter examples gain a
`Policy` (or rely on the seed) + the e2e asserts deny-without-policy then allow-with-policy.

**go.mod / deps**: **cedar-go** (`github.com/cedar-policy/cedar-go`, Apache-2.0, pure-Go — the one new dep).

**Test plan** — one named test per Scenario: cedar driver (permits-read, default-deny, owner-write forbid,
entities-from-resources) over a fake metastore reader + in-line policies; routing authorizer (Action→cedar,
coarse→rbac, rbac-unaffected); Policy type roundtrip/Validate + policy-validity admission (bad Cedar →
Invalid); per-function principal (the local-API sets `Function::"<ns>/F"`); KV facade (deny-without-policy,
allow-with-policy, owner-write); e2e (kv-counter: read denied without a Policy/seed, allowed with).

**Definition of done**: `just ci` green; every Scenario a passing named test; cedar-go added (Apache-2.0,
pinned, pure-Go, `go mod verify`); the cedar driver is **default-deny** and resolves only request-relevant
entities from the metastore (no duplicate state, no full-store rebuild per op); KV uses the PDP for read+write
(coarse read **genuinely retired** — binding alone no longer reads; examples carry a read `Policy`; owner-write
is the built-in `forbid`); rbac still decides control-plane CRUD (`authcontract` green); OpenAPI regenerated;
no identity/path leak.

## Review checklist

- [ ] cedar-go in `go.mod` (Apache-2.0, pure-Go, pinned); `go mod verify` clean; it is a **driver** behind
      `auth.Authorizer`, not a parallel path.
- [ ] Per-function principal set from the `Ref` (connection-scoped, not client-asserted); `Role`/`Namespaces`
      preserved so rbac is unaffected for CP CRUD.
- [ ] `Request` gains `Action`+`Resource` (back-compatible); the routing authorizer sends Action-bearing
      requests to cedar, coarse ones to rbac.
- [ ] cedar driver: **default-deny**; resolves **only request-relevant entities** (principal + resource +
      parents) per call (compiled PolicySet cached) — no full-store rebuild; `KVTable in KVStore` parents,
      `KVTable.owner` a `Function` entity-ref, `resourceGroup`/`namespace` attrs; policies from `Policy`
      resources (cached, recompiled on change).
- [ ] `Policy` CRD + `KindPolicy` + OpenAPI; policy-validity admission rejects un-parseable / off-schema Cedar.
- [ ] KV facade calls the PDP for `kv::read`/`kv::write` (resource = `KVTable in KVStore`); reads **default-deny**
      (examples carry a read `Policy`); owner-write is the built-in `forbid unless principal == resource.owner`;
      ADR-0073 coarse read **genuinely retired**; e2e proves read-denied-without-Policy then allowed-with.
- [ ] One new dep only (cedar-go); no identity/path leak.

## Consequences

**Positive**: funcd gets a real authorization framework (Cedar: principals/actions/resources, RBAC+ABAC,
hierarchy, formal analysis) instead of hand-rolled per-service ACLs; KV per-function access is a `Policy`, not
a coarse namespace allow; the same model extends to egress/secrets/invoke; policies live as resources in the
metastore (one home, GitOps-able), entities derive from existing resources (no drift). cedar-go is pure-Go +
Apache-2.0 (single static binary preserved).
**Negative (accepted)**: one new dependency (cedar-go); two authorizers during the transition (cedar for
data-plane resources, rbac for CP CRUD) until a later unify ADR; cedar-go lacks policy templates (roles via
group entities) and ships an experimental validator (mitigated by the curated, fixed schema); a per-resource
PDP call on the KV hot path — bounded by the cached compiled PolicySet + resolving **only the request-relevant
entities** (principal + resource + parents), not a full-store rebuild; **functions that read now need a
`Policy`** (the examples carry one) — the honest cost of genuine default-deny.
**Neutral**: the KV *binding* (`spec.kv`) is unchanged (naming); the typed engine is unaffected; egress/secrets
consumers are deferred.

## Open questions

- **Unifying control-plane CRUD under Cedar** (retire rbac) — a later ADR once the data-plane model proves out.
- **Policy scoping** (namespaced vs cluster `Policy`, and precedence) — namespaced for V1; cluster policies +
  precedence are an implementation-PR/refinement detail.
- **Entity-store freshness** (watch vs TTL recompile) — bounded "eventually-consistent within seconds"; the
  exact mechanism is an implementation detail.

## References

- [ADR-0018](0018-api-server-authn-rbac-admission.md) (the PDP port + rbac driver) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md) (KV — coarse read retired here) ·
  [ADR-0065](0065-metastore-badger-engine.md) (Policy persistence) · [ADR-0063](0063-admission-framework.md).
- **cedar-go** — <https://github.com/cedar-policy/cedar-go> (Apache-2.0, pure-Go, v1.8.0); Cedar docs
  <https://docs.cedarpolicy.com/>. Backlog: the IAM track (`PVTI_lAHOBMTWh84BbERrzgwhbqc`).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F43.
