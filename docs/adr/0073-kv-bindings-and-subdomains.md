# ADR-0073: KV resource model — Function-declared bindings + KVStore sub-domains (supersedes ADR-0072)

- **Status**: Implemented (2026-06-23)
- **Superseded in part by**: [ADR-0148](0148-size-caps-answer-413.md) (2026-10-05) — Decision §4 per-op caps → Invalid; value-over-cap-rejected Then-clause.
- **Date**: 2026-06-22 (judged 2026-06-22 — sound supersede, no Blockers; folded 2 Majors: specified the
  **table-removal lifecycle** (deletion-protection on Update + reconciler `DropPrefix(<store>/<table>/)`) and
  fixed the **owner admission** (single-owner is structural via unique table names; the admission is
  owner-exists, not "≤1"); + 3 Minors (namespace = the coarse-read trust boundary; the owner writes through its
  own binding; empty-owner ⇒ read-only).)
- **Deciders**: green-0-rabbit
- **Tags**: kv, resource, binding, controller, admission, default-deny, lifecycle
- **Realizes**: [FEAT-0001/F42](../feat/0001-feat-v1.1.md) (KV bindings + sub-domains)
- **Supersedes**: [ADR-0072](0072-kv-as-a-declarative-resource.md) (its `Grant`-as-binding mechanism)
- **Relates to**: [ADR-0064](0064-fn-to-fn-rpc-links.md) (`spec.links` — the pattern KV bindings reuse),
  [ADR-0066](0066-kv-service-durable-engine.md) (the single gateway + `DropPrefix`), [ADR-0069](0069-kv-data-plane.md)
  (`context.kv` wire), [ADR-0063](0063-admission-framework.md) (admissions), [ADR-0015](0015-controller-engine.md)
  (reconciler), [ADR-0018](0018-api-server-authn-rbac-admission.md) (the PDP — coarse authz for now).
  **Fine-grained authz** is deferred to the Cedar IAM ADR (ADR-0074); the typed-record engine (backlog) attaches
  to `spec.tables[]`.

## Context & Need

ADR-0072 made KV a declarative resource but bound functions to stores via a bespoke **`Grant`** kind — which
reinvented RBAC the platform already has (the ADR-0018 PDP / cedar-go; `GrantSpec` was even reserved "for the
IAM ADR"). Two corrections: (1) **authorization** belongs to the PDP/IAM, not a hand-rolled Grant scan in the
KV facade; (2) the **binding** (which alias → which store) is naming, and naming belongs on the **consumer** —
the wrangler/Cloudflare convention funcd already follows with `spec.links`. Separately, a flat `KVStore` can't
express the **domain → sub-domain** shape the data model needs: `orders` (a store) holding `customers` and
`fulfillment` (tables), each owned by a *different* function. This ADR drops `Grant`, moves bindings to
`Function.spec.kv`, and gives `KVStore` **sub-domains (`tables`) with a per-table owner** (the single writer).

## Scenarios

- **scenario: kv-binding-resolves** — Given a Function with `spec.kv: [{alias: customers, store: orders, table:
  customers}]`, When the handler calls `context.kv.get("customers", k)`, Then it reads `<ns>/orders/customers/k`.
- **scenario: unbound-access-denied** — Given a Function with **no** `spec.kv` entry for alias `b`, When it calls
  any `context.kv` verb on `b`, Then it is **Forbidden** (default-deny — the binding is the capability).
- **scenario: owner-writes-others-read** — Given table `orders/customers` has `owner: customers-svc`, When
  `customers-svc` `put`s it succeeds; When another function bound to it `put`s, Then **Forbidden**; When that
  other function `get`s, Then it succeeds (read is coarse-allowed in-namespace; write is owner-only).
- **scenario: single-writer-per-table** — Given `orders/customers` owned by A and `orders/fulfillment` owned by
  B (same store), When A writes customers and B writes fulfillment concurrently, Then both succeed (disjoint
  prefixes; one gateway group-commits) and neither may write the other's table.
- **scenario: binding-validity** — Given a Function whose `spec.kv` names a store/table that does not exist,
  When it is applied, Then admission rejects it (`Invalid`); duplicate aliases are rejected too.
- **scenario: value-over-cap-rejected** — Given `orders` caps `maxValueBytes`, When a bound function `put`s a
  larger value (or over-long key), Then the facade rejects it (`Invalid`) before the driver.
- **scenario: deletion-protected** — Given a `KVStore` named by some `Function.spec.kv` **or** holding keys,
  When it is deleted, Then admission rejects it (`Conflict`); with no bindings and no keys, the reconciler
  reclaims it via `DropPrefix`.
- **scenario: table-removal-protected-and-reclaimed** — Given a table still named by some `Function.spec.kv`,
  When it is removed from `spec.tables[]`, Then admission rejects the update (`Conflict`); when removed with no
  binding referencing it, Then the reconciler reclaims its data via `DropPrefix(<ns>/<store>/<table>/)`.
- **scenario: store-count-quota** — Given a namespace at the max `KVStore`s, When another is created, Then
  admission rejects it (`Invalid`).

## Scope

**In**: drop the `Grant`-as-KV-binding mechanism (revert `GrantSpec` to its reserved-empty placeholder); add
`Function.spec.kv` ([]FunctionKV{alias, store, table}); add `KVStore.spec.tables[]` ({name, owner}); the
facade resolving a `context.kv` call via the caller's `spec.kv` (default-deny) with **per-table owner** writes
+ per-op caps + `<ns>/<store>/<table>/<key>` prefix; the admissions (binding-validity, table-owner single-writer,
deletion-protection on bindings **and** data, store-count quota) cloned from the `spec.links` machinery; the
reconciler (Ready, table/binding counts, Delete→`DropPrefix`); migrate the kv-counter examples (drop
`grant.yaml`, add `spec.kv` + `spec.tables`).

**Out**: **fine-grained authorization** (read/share policy beyond same-namespace + owner-writes) — the Cedar
IAM ADR (ADR-0074); the typed-record/Avro/index engine (backlog — attaches to `spec.tables[]`); per-store
backup/CDC config (ADR-0067/0068 stay global); cross-namespace bindings (same-namespace V1.1); per-key TTL;
multi-writer/cross-node (FEAT-0002); cross-table/global indexes (the eventually-consistent analytics path).

## Constraints & Decision drivers

- **Default-deny** — no `spec.kv` entry ⇒ no access (the binding *is* the capability, wrangler-style).
- **Authz is the PDP's job, not KV's** — KV does not hand-roll RBAC; fine-grained access is the IAM ADR.
- **Single-writer per table** is the real consistency invariant (disjoint table prefixes), not per-store; the
  one **gateway** stays instance-level (concurrency), ownership is per-table (facade/admission).
- **Reuse the `spec.links` machinery** — bindings, validity, and deletion-protection are clones. **Zero new deps.**
- **No usage accounting** — structural counts at admission, per-op caps at the facade.

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| Binding home | **`Function.spec.kv`** (alias → store + table), like `spec.links`/wrangler | `Grant` resource (ADR-0072) — reinvented RBAC the PDP already owns; 3 manifests; conflated authz + naming |
| Authz | **PDP** (coarse same-namespace now; fine-grained → Cedar IAM ADR) | Bespoke grant-scan in the KV facade — a second authz path beside the PDP |
| Ownership granularity | **Per-table** (`spec.tables[].owner`) | Per-store — too coarse; can't let A own `customers` and B own `fulfillment` in one domain |
| Gateway | **One, instance-level** (concurrency serializer) | One gateway per sub-domain — loses cross-table group-commit batching; disjoint prefixes never conflict anyway |
| Store ↔ table | **Store = domain (Badger prefix), table = sub-domain (`<store>/<table>/`)** | Table = its own CRD now — heavier; inline `tables[]` is simpler and graduates to the typed engine in place |

## Decision

1. **Drop `Grant` from KV.** Revert `GrantSpec` to `struct{}` (the reserved IAM-V2 placeholder); remove the
   ADR-0072 grant Binder + grant admissions. `KindGrant` stays reserved.
2. **`Function.spec.kv: []FunctionKV`** — `{alias (DNS-1123, unique), store (KVStore name), table}`. The
   binding *is* the capability: no entry ⇒ `context.kv` on that alias is `Forbidden` (default-deny). Same
   shape + admission pattern as `spec.links`.
3. **`KVStore.spec.tables: []KVTable`** — `{name (DNS-1123, unique in store), owner (a Function in this
   namespace)}`. The store is the **domain** (one Badger prefix `<ns>/<store>/`, one gateway, one
   lifecycle/`DropPrefix`); a table is a **sub-domain** (`<ns>/<store>/<table>/`).
4. **Facade** resolves a `context.kv` call by the caller function's `spec.kv[alias]` → `(store, table)` →
   prefix `<ns>/<store>/<table>/<key>`. **Writes (`put`/`del`) require `caller == table.owner`** (single-writer
   per table) else `Forbidden`; **reads (`get`/`list`)** are allowed for any bound caller. The **owner writes
   through its own `spec.kv` binding** — ownership *authorizes* the write, the binding *addresses* the table;
   an owner with no `spec.kv` entry for the table cannot reach it (no alias). A table with **no `owner`** has
   no writer (read-only through the facade). Per-op caps (`maxValueBytes`/`maxKeyBytes`) → `Invalid`. Read
   authorization is **coarse at the namespace** — the namespace is the V1.1 trust boundary (blueprint:
   *namespace = tenancy/isolation boundary*), so any function may *bind* (and thus read) a store/table in its
   own namespace; this is deliberate, not a default-allow gap (cross-namespace is denied, and per-function
   read narrowing is the Cedar IAM ADR's job — the Temporary-workaround exit).
5. **One gateway** (ADR-0066) unchanged — it serializes the instance and group-commits writes from all
   table-owners (disjoint prefixes never conflict). Single-writer-per-table is sound because each table's base
   + its table-scoped indexes have exactly one writer.
6. **Structural** (`Validate`, not admission): within a `KVStore`, **table names are unique** (so a table has
   exactly one `owner` by construction — `owner` is a single field, not a list); within a `Function`, `spec.kv`
   aliases are unique. **Admissions** (clone the `spec.links` ones):
   - **kv-binding-validity** (Function Create/Update): each `spec.kv` `(store, table)` exists in the namespace.
     (Clone `linkValidity`.)
   - **kv-owner-exists** (KVStore Create/Update): every `tables[].owner` (when set) is a real Function in the
     namespace. (Folded into the validity pass — *not* a "≤1 owner" check; single-owner is structural above.)
   - **kvstore-deletion-protection** (KVStore Delete **and** Update): on Delete, `Conflict` if any
     `Function.spec.kv` names the store **or** it holds keys. On Update, `Conflict` if a **removed** table is
     still named by some `Function.spec.kv`. (Clone `linkDeletionProtection`, scanning `spec.kv`.)
   - **kvstore-quota** (KVStore Create): reject over `kvstore.maxStoresPerNamespace`.
7. **Reconciler** (`KindKVStore`): Ready + `status.tables`/`bindings`; on a table removed from `spec.tables[]`
   (observed by diffing against the live data prefixes), reclaim it via `DropPrefix(<ns>/<store>/<table>/)`;
   on store Delete, `DropPrefix(<ns>/<store>/)`.

## Temporary workarounds

**Coarse read authz** (any bound same-namespace function may read any table it binds) is the V1.1 stand-in.
**Exit criterion**: the Cedar IAM ADR (ADR-0074) replaces it with per-principal `kv::read` policies.

## Contracts

```go
// api/types/v1alpha1 — Grant reverts; Function + KVStore gain the binding/sub-domain shape.
type GrantSpec struct{} // reverted to the reserved IAM-V2 placeholder (KindGrant stays, unused by KV)

type FunctionKV struct { // mirrors FunctionLink (ADR-0064)
	Alias string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"` // context.kv handle, unique
	Store ObjectName `json:"store"`                                                // a KVStore in this namespace
	Table string     `json:"table" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"` // a sub-domain of the store
}
// FunctionSpec gains:  KV []FunctionKV `json:"kv,omitempty"`

type KVStoreSpec struct {
	MaxValueBytes int64     `json:"maxValueBytes,omitempty"` // default 1048576
	MaxKeyBytes   int       `json:"maxKeyBytes,omitempty"`   // default 1024
	Tables        []KVTable `json:"tables,omitempty"`        // the sub-domains
}
type KVTable struct {
	Name  string     `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"` // unique within the store
	Owner ObjectName `json:"owner,omitempty"`                                    // the single writer (a Function in this ns)
	// typed-engine (backlog) attaches here: Schema, Indexes (table-scoped only).
}
type KVStoreStatus struct {
	Phase      Phase       `json:"phase,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Tables     int         `json:"tables,omitempty"`
	Bindings   int         `json:"bindings,omitempty"` // # of Function.spec.kv entries referencing this store
}
```

```go
// internal/services/kv — the facade resolves the caller's spec.kv (no Grant Binder). A read-only metastore
// view supplies the caller's bindings + the store's tables/caps, cached (low-churn).
type BindingResolver interface {
	// Resolve maps a caller function + alias to its store/table + the store caps + the table owner;
	// fault.Forbidden when the caller has no spec.kv entry for the alias (default-deny).
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error)
}
type Binding struct {
	Store v1.ObjectName; Table string; Owner v1.ObjectName; MaxValueBytes int64; MaxKeyBytes int
}
// Facade Get/List allow any bound caller; Put/Delete require caller == Owner (else Forbidden); prefix
// <ns>/<store>/<table>/<key>; per-op caps enforced.
```

| consumes | exposes |
|---|---|
| `spec.links` admission clones (ADR-0063/0064); controller engine (ADR-0015); driver `DropPrefix` (ADR-0066) | `Function.spec.kv` bindings; `KVStore.spec.tables[]` sub-domains; per-table single-writer |
| the caller function (ADR-0069 local-API `Ref`, already threaded) | the facade's binding-resolution key |
| `kvstore.maxStoresPerNamespace` config (default 100) | store-count quota |

## Implementation plan

**Files**: `api/types/v1alpha1/{function.go (add KV []FunctionKV + FunctionKV), kvstore.go (Tables[]/KVTable),
grant.go (revert GrantSpec to struct{})}` + OpenAPI regen; `internal/controlplane/admission/kvstore.go`
(replace the grant admissions with binding-validity + owner-exists + deletion-protection-over-spec.kv on
Delete **and** Update-table-removal + quota; unique table names + unique aliases are structural `Validate`)
+ wiring; `internal/services/kv/{binder.go→resolver over spec.kv, kv.go facade owner-write + table prefix,
reconcile.go}`; remove the grant Binder/types; `pkg/funcd` + `cmd/funcd` wiring; examples (drop `grant.yaml`,
add `spec.kv` + `spec.tables`) + the e2e (`pkg/funcd/kv_e2e_test.go`: unbound→Forbidden, owner writes 1→2,
non-owner write→Forbidden) + `scripts/lima-kv.yaml`/justfile. Mark ADR-0072 `Superseded by ADR-0073`.

**go.mod / deps**: none.

**Test plan**: one named test per Scenario — types roundtrip/Validate (unique table names, unique aliases);
admission (binding-validity, owner-exists, deletion-protection bindings+data on Delete, table-removal-protected
on Update, quota) cloning `links_test.go`; facade (kv-binding-resolves, unbound-access-denied,
owner-writes-others-read, value-over-cap); reconciler (provisions, delete-reclaims, table-removal-reclaims);
e2e (grant-free: unbound denied, owner 1→2).

**Definition of done**: `just ci` green; every Scenario a passing named test; default-deny (no `spec.kv` ⇒
Forbidden); single-writer per table (owner-only writes; ≤1 owner); deletion-protection on bindings+data;
OpenAPI regenerated; `Grant` no longer used by KV; ADR-0072 marked superseded; no new dep; no leak.

## Review checklist

- [ ] `GrantSpec` reverted to `struct{}`; KV no longer references `Grant`; `KindGrant` still registered.
- [ ] `Function.spec.kv` (FunctionKV) + `KVStore.spec.tables[]` (KVTable) added; OpenAPI regenerated.
- [ ] Facade is binding-gated (default-deny): no `spec.kv` ⇒ Forbidden; **writes require caller == table.owner**;
      reads allowed for any bound same-ns caller; prefix `<ns>/<store>/<table>/<key>`; per-op caps enforced.
- [ ] Structural `Validate`: unique table names per store (⇒ single owner per table), unique aliases per function.
- [ ] Admissions: binding-validity (store/table exist); owner-exists (each `tables[].owner` is a real Function);
      deletion-protection on Delete (`spec.kv` references **and** non-empty data) **and** on Update (removing a
      still-bound table); store-count quota.
- [ ] One gateway unchanged; reconciler Ready + counts; Delete → `DropPrefix(<store>/)`; table removal →
      `DropPrefix(<store>/<table>/)`.
- [ ] Examples carry `spec.kv` + `spec.tables` (no `grant.yaml`); e2e proves unbound-denied + owner 1→2.
- [ ] ADR-0072 header marked `Superseded by ADR-0073`; reuses links machinery; no new dep; no leak.

## Consequences

**Positive**: the binding model is the wrangler/`spec.links` convention (declarative, on the consumer, two
manifests); KV stops reinventing RBAC (authz → the PDP); the domain/sub-domain shape lands with per-table
single-writer; one gateway, unchanged; the typed engine attaches to `spec.tables[]` with no re-model. Zero new
deps; reuses the links admissions.
**Negative (accepted)**: **supersedes ADR-0072** — the Grant path (just built) is removed and the examples
re-migrate; **read authz is coarse** (same-namespace) until the Cedar IAM ADR — a documented temporary
workaround with an explicit exit.
**Neutral**: backup/CDC stay global; cross-namespace bindings, per-key TTL, cross-table indexes remain out.

## Open questions

- **Fine-grained read authz** (per-principal `kv::read`, cross-namespace sharing) — ADR-0074 (Cedar IAM).
- **Table as its own CRD** (`KVTable`) vs. inline `spec.tables[]` — inline now; revisit if the typed engine
  needs independent per-table lifecycle.
- **Owner transfer** — delete+recreate the table entry for now; atomic transfer is a later refinement.

## References

- [ADR-0072](0072-kv-as-a-declarative-resource.md) (superseded) · [ADR-0064](0064-fn-to-fn-rpc-links.md)
  (`spec.links`) · [ADR-0066](0066-kv-service-durable-engine.md) (gateway + DropPrefix) ·
  [ADR-0063](0063-admission-framework.md) · [ADR-0015](0015-controller-engine.md).
- Backlog: typed-record engine (`PVTI_lAHOBMTWh84BbERrzgwfcAo`) — attaches to `spec.tables[]`.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F42.
