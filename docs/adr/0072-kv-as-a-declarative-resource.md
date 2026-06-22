# ADR-0072: KV as a declarative resource — KVStore CRD + Grant-as-binding + single-writer ownership

- **Status**: Implemented (2026-06-22)
- **Date**: 2026-06-22 (judged 2026-06-22 — folded the judge's Blocker + 2 Majors: **scoped all grants
  same-namespace for V1.1** (cross-ns `ro` grants contradicted deletion-protection under the single-namespace
  `StoreReader`; cross-ns sharing deferred); spelled out threading the **caller function** from the `Ref`
  through the local-API KV port + facade to the Binder (it is dropped today); pinned the **Grant gate replaces
  the per-call `KindService` PDP check**. No open Blockers.)
- **Deciders**: green-0-rabbit
- **Tags**: kv, resource, controller, admission, grant, default-deny, lifecycle
- **Realizes**: [FEAT-0001/F41](../feat/0001-feat-v1.1.md) (KV as a declarative, owned resource)
- **Relates to**: [ADR-0069](0069-kv-data-plane.md) (**tightens** its free-binding `context.kv` to grant-required),
  [ADR-0066](0066-kv-service-durable-engine.md) (the durable driver + `DropPrefix` teardown the reconciler uses),
  [ADR-0019](0019-service-facade-pattern-kv.md) (the PDP facade this extends), [ADR-0063](0063-admission-framework.md)
  (the admission pipeline this plugs into), [ADR-0015](0015-controller-engine.md) (the reconciler engine),
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (the PDP). **Foundation for** the typed-record/Avro/index
  engine (backlog, a separate track — out of scope here).

## Context & Need

Today KV bindings are **ad-hoc strings**: a function calls `context.kv.put("counters", …)` and the facade
([internal/services/kv/kv.go](../../internal/services/kv/kv.go)) prefixes `<ns>/<binding>/<key>` over the
shared instance — any binding works, no ownership, no lifecycle, no quota (ADR-0069). To manage KV like the
rest of the platform — prevent deleting a store still in use, enforce limits, share a store read-only across
functions, and (later) hang a typed engine off it — KV must become a **declarative resource**: a named,
namespace-scoped `KVStore` with **one writer + N readers**, reached only through an explicit **`Grant`**.
This is the control-plane/lifecycle layer over the Implemented data plane (ADR-0066 driver, ADR-0069 wire);
it builds nothing of the typed-record engine (backlog item, separate track).

## Scenarios

- **scenario: kvstore-create-provisions** — Given a `KVStore` is applied, When the reconciler runs, Then it
  reaches **Ready** with `status.grantRefs` reflecting referencing Grants.
- **scenario: grant-binds-function-to-store** — Given an `rw` Grant for function F with binding `counters` →
  store S, When F calls `context.kv.put("counters", k, v)`, Then it writes to S (prefix `<S.ns>/<S>/k`).
- **scenario: ungranted-access-denied** — Given function F has **no** Grant for binding `b`, When F calls any
  `context.kv` verb on `b`, Then it is **Forbidden** (default-deny — no implicit/default store).
- **scenario: single-writer-enforced** — Given store S already has an `rw` Grant, When a **second** `rw` Grant
  for S is applied, Then admission rejects it (`Conflict`); and a function holding only an `ro` Grant calling
  `put`/`del` is **Forbidden**.
- **scenario: value-over-cap-rejected** — Given S declares `maxValueBytes: 1048576`, When a function `put`s a
  larger value (or an over-long key), Then the facade rejects it (`Invalid`) — the write never reaches Badger.
- **scenario: store-count-quota** — Given a namespace already holds the max `KVStore`s, When another is
  created, Then admission rejects it (`Invalid`).
- **scenario: deletion-protected-by-grants** — Given S is referenced by a Grant, When S is deleted, Then
  admission rejects it (`Conflict`, "remove grants first").
- **scenario: deletion-protected-by-data** — Given S has no Grants but holds keys, When S is deleted, Then
  admission rejects it (`Conflict`, "drain first").
- **scenario: delete-reclaims** — Given S has no Grants and no keys, When S is deleted, Then the reconciler
  reclaims its prefix via `DropPrefix(<S.ns>/<S>/)`.
- **scenario: reader-grant-allows-get-not-put** — Given a function has an `ro` Grant for store S, When it
  `get`s/`list`s S Then it succeeds, and When it `put`s/`del`s Then it is **Forbidden** (read sharing without
  write).

## Scope

**In**: a `KVStore` namespaced CRD (name + per-op caps + status); the `GrantSpec` defined KV-scoped
(subject→binding→store→mode); the facade resolving a binding via a Grant (default-deny) + enforcing
mode + per-op caps + store-scoped prefix; a `KVStore` reconciler (Ready, grant count, Delete→`DropPrefix`);
the admissions (store-count quota; grant-validity incl. single-writer; KVStore deletion-protection on Grants
**and** non-empty data); wiring; migrating the kv-counter examples to a `KVStore`+`Grant`.

**Out**: the typed-record/Avro/index/scan engine (backlog `PVTI_…fcAo`); per-store backup/CDC config
(ADR-0067/0068 stay **global/instance-level**); usage/byte accounting + quotas (only structural counts +
per-op caps here); generalizing `Grant` beyond KV (KV-scoped now); **cross-namespace grants / read sharing**
(same-namespace KV for V1.1 — deletion-protection needs a cross-ns grants-by-store index first); per-key TTL;
multi-writer / cross-node (FEAT-0002).

## Constraints & Decision drivers

- **Default-deny** (blueprint security posture; ADR-0064 link=grant) — a function gets **no** KV implicitly
  (only config + secrets are implicit); access requires an explicit Grant.
- **Reuse, don't reinvent** — the admission pipeline (ADR-0063), the controller engine (ADR-0015), the PDP
  facade (ADR-0019), and the byte driver + `DropPrefix` (ADR-0066). **Zero new deps.**
- **One writer + N readers** — the consistency lock the future typed engine relies on; cheap to enforce.
- **No usage accounting** — counts at admission, per-op caps at the facade; no running counters, no scans.

## Alternatives considered

| Decision | Chosen | Rejected alternative (why) |
|---|---|---|
| Binding home | **Grant carries it fully** (`{subject, binding, store, mode}`); facade resolves `(caller, binding)→Grant` | `Function.spec.kv` field + Grant authorizes — two places to sync; less able to express cross-namespace read sharing per-binding |
| Owner model | **Owner = the single `rw` Grant holder** (admission: ≤1 `rw` per store) | Explicit `KVStore.spec.owner` — redundant with the `rw` Grant; two sources of truth for one fact |
| Delete guard | **Block on referencing Grants AND non-empty data** | Grants-only — a no-Grant delete silently wipes data; data-only — leaves dangling Grants |
| Ownership-enforcement layer | **Facade + admission** (identity-aware) | The byte gateway (ADR-0066) — it is identity-/store-agnostic by design; threading grants into it breaks the port layering. The gateway provides the *concurrency* single-writer; the facade provides *ownership* |
| Grant locality | **All grants (`ro` + `rw`) same-namespace as the store** (V1.1) | Cross-namespace grants — the deletion-protection guarantee ("block if **any** Grant references the store") needs to enumerate grants across namespaces, but the admission `StoreReader.List` is single-namespace; cross-ns sharing needs a grants-by-store index — deferred (a dangling cross-ns grant would otherwise survive a store delete) |

## Decision

Make KV a declarative resource reached only through a Grant; keep the byte wire (ADR-0069) but **tighten the
facade to grant-required**.

1. **`KVStore`** (namespaced CRD): `spec.maxValueBytes` (default `1048576`), `spec.maxKeyBytes` (default
   `1024`); `status` = `Phase` + conditions + `grantRefs` (count). No owner field — ownership is the `rw` Grant.
2. **`Grant`** (define `GrantSpec`, KV-scoped): `function` (subject), `binding` (the alias the function uses),
   `store` (the `KVStore`), `mode` (`ro`|`rw`). A Grant, its subject function, and its store all live in **one
   namespace** (the Grant's) — V1.1 is same-namespace KV; cross-namespace sharing is deferred (Scope/Out).
3. **Facade resolution** (default-deny): a `context.kv` call carries the caller (function + namespace) — the
   function comes from the ADR-0069 local-API `Ref`, which is **connection-scoped/provisioned at sandbox
   creation, never client-asserted** (so a function-keyed grant is trustworthy despite the namespace-scoped
   PDP identity). The facade finds the Grant `(function=caller, binding=alias)` in the caller's namespace →
   `(store, mode)`; **no Grant ⇒ `Forbidden`**. `put`/`del` require `mode=rw`; `get`/`list` accept either.
   On-disk prefix is `<ns>/<store>/<key>` (store-scoped). Per-op caps: `len(value) > maxValueBytes` or
   `len(key) > maxKeyBytes` ⇒ `Invalid`, before the write reaches the driver. **This Grant gate is the KV PEP
   — it replaces the per-call `KindService` PDP check the facade does today** (ADR-0019); the PDP still gates
   control-plane CRUD of `KVStore`/`Grant` (ADR-0018), and the local-API `KV` port + facade verbs gain the
   caller **function** (today they thread only `id, ns, binding` — [kv.go:22-25](../../internal/workernode/local/kv.go)).
4. **Ownership**: a store has **one** `rw` Grant (its writer/owner); admission rejects a second `rw` Grant for
   the same store (`Conflict`). The byte gateway (ADR-0066) already serializes all writes; the facade enforces
   that only the `rw`-holder writes a given store (a non-`rw` caller's `put`/`del` ⇒ `Forbidden`).
5. **Admissions** (ADR-0063, Validating):
   - **store-count quota** — on `KVStore` Create, reject if the namespace already holds
     `kvstore.maxStoresPerNamespace` (config, default `100`).
   - **grant-validity** — on `Grant` Create/Update: the referenced `KVStore` (and subject `Function`) exist;
     `rw` ⇒ same-namespace as the store **and** no other `rw` Grant for that store (single-writer).
   - **kvstore-deletion-protection** — on `KVStore` Delete: reject if any Grant references it (scan Grants) or
     the store holds keys (a `KVProber.HasAny(<store prefix>)` probe).
6. **Reconciler** (`KindKVStore`): set `Ready`, compute `grantRefs`; on delete (store `NotFound`), reclaim via
   the driver's `DropPrefix(<ns>/<store>/)` (ADR-0066).
7. **Migration**: the kv-counter examples gain a `KVStore` + an `rw` `Grant`; with no Grant, `context.kv` is
   denied. This tightens ADR-0069's free bindings (it stays the wire; the facade gains the gate).

## Temporary workarounds

None. (The cross-namespace **writer** restriction is a deliberate scope line, not a workaround — multi-writer
is FEAT-0002.)

## Contracts

```go
// api/types/v1alpha1 — new kind + Grant spec (register KindKVStore in metadata.go + NewObject).
type KVStore struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       KVStoreSpec   `json:"spec"`
	Status     KVStoreStatus `json:"status,omitempty"`
}
type KVStoreSpec struct {
	MaxValueBytes int64 `json:"maxValueBytes,omitempty"` // default 1048576 (1 MiB); 0 ⇒ default
	MaxKeyBytes   int   `json:"maxKeyBytes,omitempty"`   // default 1024;       0 ⇒ default
}
type KVStoreStatus struct {
	Phase      Phase       `json:"phase,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	GrantRefs  int         `json:"grantRefs,omitempty"` // # of Grants referencing this store
}

type KVMode string // "ro" | "rw"
const (KVModeRO KVMode = "ro"; KVModeRW KVMode = "rw")

type GrantSpec struct { // currently struct{} — defined here, KV-scoped; subject/store same namespace (V1.1)
	Function ObjectName `json:"function"` // subject function, in this Grant's namespace
	Binding  string     `json:"binding"`  // the alias the function uses
	Store    ObjectName `json:"store"`    // target KVStore, in this Grant's namespace
	Mode     KVMode     `json:"mode"`     // ro | rw
}
func (g *Grant) Validate() error // mode oneof (ro|rw); binding a DNS-1123 label; function/store non-empty
```

```go
// internal/services/kv — the facade gains grant resolution + caps. Binder resolves the Grant + the store's
// caps in one shot, a read-only metastore view, cached (low-churn).
type Binder interface {
	// Resolve maps a caller (namespace + function) + binding alias to its granted store + mode + caps;
	// fault.Forbidden when no Grant matches (default-deny).
	Resolve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string) (Binding, error)
}
type Binding struct{ Store v1.ObjectName; Mode KVMode; MaxValueBytes int64; MaxKeyBytes int }

// FacadeDeps gains Binder; Get/Put/Delete/List gain the caller function (today only id, ns, binding reach
// the facade — internal/workernode/local/kv.go threads only those), resolve the Grant (default-deny),
// enforce mode (rw for put/del) + per-op caps, and prefix by <ns>/<store>/<key>.
```

```go
// internal/controlplane/admission — three Validating admissions. StoreReader is the existing read view;
// KVProber is a NEW narrow interface defined HERE (keeps the package a near-leaf, like StoreReader), wired
// at control-plane assembly to the kvstore driver's List.
type KVProber interface { HasAny(ctx context.Context, prefix string) (bool, error) }
func NewKVStoreQuotaAdmission(r StoreReader, maxPerNamespace int) Admission           // Create KVStore
func NewGrantValidityAdmission(r StoreReader) Admission                               // Create/Update Grant (incl. single-writer ≤1 rw)
func NewKVStoreDeletionProtectionAdmission(r StoreReader, p KVProber) Admission       // Delete KVStore (grants + data)
```

```go
// internal/services/kv — the KindKVStore reconciler (controller.Reconciler). PrefixDropper is a NEW narrow
// interface (the driver's DropPrefix is a concrete method beyond the kvstore.KV port — type-asserted from
// the driver at wiring; the memory driver gets a no-op or real impl).
type PrefixDropper interface { DropPrefix(prefix string) error }
type ReconcilerDeps struct { Store store.Store; KV PrefixDropper; Logger *slog.Logger }
func NewReconciler(d ReconcilerDeps) (*Reconciler, error)
// Reconcile: KVStore present ⇒ Ready + grantRefs; absent (deleted) ⇒ DropPrefix(<ns>/<name>/).
```

| consumes | exposes |
|---|---|
| ADR-0063 admission pipeline; ADR-0015 controller engine; ADR-0019 facade; ADR-0066 driver `DropPrefix` + `List` (the `KVProber`) | `KVStore` + KV `Grant` resources; grant-gated, owned, lifecycle-managed KV |
| `kvstore.maxStoresPerNamespace` config (default 100) | store-count quota at admission |
| the ADR-0069 local-API caller `Ref` (function + namespace) | the facade's grant resolution key |

## Implementation plan

**Files**: `api/types/v1alpha1/kvstore.go` (+ `KindKVStore` in `metadata.go`/`NewObject`, OpenAPI regen),
`grant.go` (fill `GrantSpec` + `Validate`); `internal/controlplane/admission/kvstore.go` (the three
admissions + `KVProber`) + wire them in the control-plane assembly; `internal/services/kv/binder.go`
(Binder over the metastore, cached) + facade changes (resolve/mode/caps/prefix) + `internal/services/kv/reconcile.go`
(the reconciler) + register it with the controller engine; **`internal/workernode/local/kv.go` + the facade
verb signatures — thread `caller.Function` from the `Ref` through to the Binder** (today only `id, ns,
binding` reach the facade); `cmd/funcd` + `pkg/funcd` wiring (the `KVProber`/`PrefixDropper` adapters over the
kvstore driver, the `kvstore.maxStoresPerNamespace` config key); the kv-counter example manifests (a `KVStore`
+ `Grant`) + the e2e/lima updates.

**go.mod / deps**: none.

**Test plan** — one named test per Scenario:
- types: KVStore/Grant roundtrip + `Validate` (mode oneof, rw-locality).
- admission: `store-count-quota`, `grant-validity` (+ single-writer `Conflict`), `kvstore-deletion-protection`
  (grants-present `Conflict`; non-empty `Conflict` via a fake `KVProber`; clean ⇒ allow) — clone the
  `links_test.go` shape.
- facade: `grant-binds-function-to-store`, `ungranted-access-denied` (Forbidden), `ro-cannot-write`
  (Forbidden), `value-over-cap-rejected` (Invalid), `cross-namespace-reader`.
- reconciler: `kvstore-create-provisions` (Ready + grantRefs), `delete-reclaims` (DropPrefix called).
- e2e/lima: the kv-counter functions deploy a `KVStore` + `Grant` and still serve 1→2 (now grant-gated);
  an ungranted call is denied.

**Definition of done**: `just ci` green; every Scenario a passing named test; default-deny holds (no Grant ⇒
Forbidden); single-writer enforced (≤1 rw Grant; ro cannot write); per-op caps enforced; delete blocked by
Grants or data, else reclaims; OpenAPI regenerated; no new dep; no identity/path leak.

## Review checklist

- [ ] `KVStore` + KV `GrantSpec` types + `Validate` + `KindKVStore` registered + OpenAPI regenerated.
- [ ] Facade is **grant-required** (default-deny): no Grant ⇒ Forbidden; `ro` cannot `put`/`del`; the caller
      function is threaded from the `Ref` to the Binder; prefix is `<ns>/<store>/<key>`; the per-call
      `KindService` PDP check is replaced by the Grant gate.
- [ ] Per-op caps (`maxValueBytes` default 1 MiB, `maxKeyBytes`) enforced in the facade before the driver.
- [ ] Admissions: store-count quota (Create); grant-validity incl. single-writer ≤1 `rw` (Create/Update);
      deletion-protection on referencing Grants **and** non-empty data (Delete).
- [ ] Reconciler: Ready + `grantRefs`; delete ⇒ `DropPrefix`.
- [ ] kv-counter examples carry a `KVStore` + `Grant`; the e2e proves grant-gated 1→2 + ungranted denial.
- [ ] Reuses ADR-0063/0015/0019/0066; no new dep; no identity/path leak.

## Consequences

**Positive**: KV is a first-class, owned, lifecycle-managed resource — deletion-protected, quota-bounded,
read-shareable across functions (same namespace), default-deny; the single-writer ownership is the lock the
future typed engine inherits; everything composes from existing machinery (zero new deps).
**Negative (accepted)**: **tightens ADR-0069** — functions now need a `KVStore` + `Grant` (the examples
migrate; a no-Grant call is denied). This *adds* a grant precondition, it does not supersede ADR-0069: that
ADR made no grant decision (grants did not exist), and its wire + facade contracts stand — so ADR-0072
relates-to, not supersedes. The on-disk prefix changes to store-scoped (dev/example data re-deployed, no
production data exists); the facade gains a cached metastore read on the KV hot path (grants + caps are
low-churn — cache, don't re-read).
**Neutral**: backup/CDC stay global; the typed engine is unbuilt (backlog); cross-namespace grants/sharing,
cross-namespace writers, and per-key TTL remain out.

## Open questions

- **Generalizing `Grant`** beyond KV (verbs/resources for blob/secrets/events) — a later ADR; KV-scoped now.
- **Grant-resolution cache invalidation** — the facade caches grants/specs; the exact invalidation (watch vs
  TTL) is an implementation-PR detail bounded by "low-churn, eventually-consistent within seconds".
- **Owner transfer** (moving the `rw` Grant) — delete-then-create for now; an atomic transfer is a later refinement.

## References

- [ADR-0069](0069-kv-data-plane.md) · [ADR-0066](0066-kv-service-durable-engine.md) ·
  [ADR-0019](0019-service-facade-pattern-kv.md) · [ADR-0063](0063-admission-framework.md) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md) (link=grant + deletion-protection template) ·
  [ADR-0015](0015-controller-engine.md) · [ADR-0018](0018-api-server-authn-rbac-admission.md).
- Backlog: the typed-record/Avro/index engine (`PVTI_lAHOBMTWh84BbERrzgwfcAo`) this is the foundation for.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F41.
