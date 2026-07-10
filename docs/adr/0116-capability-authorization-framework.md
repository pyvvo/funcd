# ADR-0116: Capability authorization framework — one registry for binding-as-grant

- **Status**: Implemented
- **Date**: 2026-07-09
- **Accepted**: 2026-07-09
- **Implemented**: 2026-07-09
- **Deciders**: green-0-rabbit
- **Tags**: authorization, cedar, capability, refactor, framework
- **Acceptance note**: judge Blocker + 2 Majors folded — **B1** (the ADR-0088 provider case is a **`PrincipalSource`** — Function-first, else CatalogService — not a fourth capability; separating "capability" from "where a principal's bindings are read" keeps the unique-Action rule, the `func(*v1.Function)`→`PrincipalObject` binding, and "principal materialized once" all consistent → **three** capabilities kv/invoke/s3 + **two** sources); **M1** (`Schema() []byte` → assembled **`KnownAction`/`KnownEntityType`**, matching the driver's real `curatedActions`/`curatedEntityTypes` predicates, not a non-existent cedar-go `[]byte` schema); **M2** (resource dispatch is by the `Resource` func's **`ok`**, never a `v1.Kind`↔Cedar-entity-type match — `KindKVStore`→`KVTable`, `KindBucket`→`BlobPrefix`). Minor: empty-vs-absent binding attr pinned (always set, matching today).
- **Realizes**: [FEAT-0001/F84](../feat/0001-feat-v1.1.md) (capability authorization framework — abstract the per-capability Cedar wiring into a registry)
- **Relates to**: [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar PDP + binding-as-grant this refactors — **relates-to, not supersedes**: the Authorizer port, default-deny, and binding-as-grant semantics are unchanged), [ADR-0075](0075-cedar-invoke-authorization.md)/[ADR-0076](0076-cedar-kv-read-binding-grant.md)/[ADR-0088](0088-add-on-provider-s3-identity.md) (the invoke/kv/s3 consumers migrated onto it), [ADR-0117](0117-egress-policy-enforcement.md) (F81 — the first *new* consumer, egress — *to be written*)

## Context & Need

funcd's PDP (ADR-0074, cedar-go behind `auth.Authorizer`) has four capability consumers today — KV read/write, fn→fn `link::invoke`, S3 read/write, and add-on-provider S3 — each following the **same binding-as-grant pattern**: a `Function.spec` field (`spec.kv`/`spec.links`/`spec.blob`) is the declared capability, materialized as a Set attribute on the principal entity, and a built-in Cedar permit grants the action when `principal.<set>.contains(resource)`. But the pattern is **hand-copied across five places per capability**:

1. an `Action` constant (`internal/auth/authorizer.go`);
2. a resource entity type in the curated schema (`cedar/schema.go`);
3. **a branch inlined into the monolithic `EntitiesFor`** (`cedar/entities.go`) — the principal's binding Set + the resource's UID/owner/parent materialization;
4. a built-in permit `.cedar` file;
5. a PEP call site.

`EntitiesFor` is the smell: one 190-line method that inlines `links`, `kvBindings`, `blobBindings`, the provider fallback, and per-type resource dispatch. Every new capability edits that central hotspot. FEAT-0007's **egress** would be the fifth copy — and Volume, Secret, catalog, and other resource capabilities are coming.

**Purpose.** Extract the pattern into a **`Capability` registry**: each capability is declared **once** as a value (its actions, resource entity type, binding-field→principal-Set materializer, resource materializer, and built-in permit), and the schema, the EntityProvider, and the built-in PolicySet are **assembled from the registered set** — so a new capability *registers* instead of editing `EntitiesFor`. This ADR builds the registry and **migrates the existing four consumers onto it, behavior-preserving** (their suites are the regression guard, exactly as ADR-0092's DRY refactor). Callers: the cedar driver assembly (composition root) and every future capability (egress first). No user-facing surface changes.

## Scenarios

- `existing-behavior-preserved` — Given the four consumers migrated onto the registry, When the KV / invoke / S3 / provider authorization suites run, Then every existing assertion passes unchanged (same decisions, same entities, same built-ins).
- `new-capability-no-entitiesfor-edit` — Given a new capability registered as a `Capability` value, When it authorizes a request, Then its principal binding-Set + resource entity are materialized and its built-in permit evaluated **without any edit to the shared `EntitiesFor`/schema/built-in-assembly code** — the registry composes it.
- `binding-grant-still-self-enforces` — Given a Function with `spec.kv`/`spec.links`/`spec.blob`, When it accesses a bound resource, Then the registered capability's built-in permit grants it (as today) and an unbound resource stays default-deny.
- `assembled-schema-covers-all` — Given N registered capabilities, When the cedar driver builds its schema + built-in PolicySet, Then both are the union of the registered capabilities' contributions (no capability silently dropped).
- `unmodeled-principal-or-resource-faults` — Given a request whose principal/resource type matches no registered capability, Then it is an `Internal` fault (a wiring bug), unchanged from today.

## Scope

**In:** a `Capability` type + a `PrincipalSource` type + a `Registry` (in `internal/auth/cedar`) that assembles the composite `EntityProvider`, the `KnownAction`/`KnownEntityType` vocabulary, and the built-in `PolicySet` from the registered set; migration of the **kv-read/write, invoke, s3** consumers to **three** `Capability` values + the **two** principal sources (Function-first, CatalogService-fallback — the ADR-0088 provider case, now a source) — behavior-preserving; the composition-root wiring that registers them.

**Out (named follow-ons):**
- **Egress as a new capability + the `EgressPolicy` high-level CRD + the egress gateway PEP** — [ADR-0117](0117-egress-policy-enforcement.md) (F81), the first consumer *built* on this registry.
- **New high-level typed policy CRDs** (VolumePolicy, secret policy, …) — future ADRs, each a thin resource compiling to a registered capability.
- **The `auth.Authorizer` port, routing (Action-set⇒cedar / empty⇒rbac), default-deny, and the binding-as-grant *semantics*** — unchanged (ADR-0074); this is a mechanism refactor, not a policy-model change.
- **RBAC (the coarse control-plane authorizer)** — untouched.

## Constraints & Decision drivers

- **Behavior-preserving** — the migrated consumers must produce byte-identical decisions/entities; the existing kv/invoke/s3/provider suites are the acceptance gate (no new policy behavior).
- **ADR-0074 is Accepted/Implemented** — this **relates to** it (refactors the *implementation* of binding-as-grant); it does **not** supersede its contracts (the port, default-deny, semantics hold). No accepted-ADR contradiction.
- **One registry, assembled artifacts** — schema, EntityProvider, and built-in PolicySet are derived from the registered capabilities; no capability-specific branch survives in shared code.
- **Typed surface** — `Capability` uses typed `auth.Action` / cedar entity types / `*v1.Function`; no `any` in the exported registry API.
- **Leaf discipline** — `internal/auth/cedar` stays a near-leaf (reads the metastore via the existing `MetaReader`); the registry adds no new port dependency.
- **No new third-party dep** — reuses cedar-go; the change is internal structure.

## Alternatives considered

- **Registry of `Capability` values (assembled artifacts)** vs **leave `EntitiesFor` monolithic.** Registry wins: it removes the central hotspot every capability edits and makes egress + future capabilities declarative. Rejected status-quo: the fifth copy (egress) confirms the pattern is stable (rule-of-three long passed) — the duplication is now a real maintenance + correctness risk (a missed branch = a silent authz gap).
- **Migrate existing consumers now** vs **registry for new capabilities only.** Migrate-now wins (decider's call): one mechanism, zero duplication, and the existing suites *prove* the abstraction is faithful. Rejected new-only: leaves two mechanisms and unverified-against-real-consumers abstraction.
- **A generic reflection/tag-driven materializer** vs **explicit per-capability funcs on the `Capability` value.** Explicit funcs win: no reflection, compile-checked, each capability's materialization is readable and testable. Rejected reflection: opaque, and the binding shapes (per-table, per-prefix, entity-ref owner) differ enough that a generic reflector would be more complex than five small funcs.
- **A brand-new `Policy`-primitive CRD ("principal has capability C on resource R")** vs **reuse the existing Cedar `Policy` + binding-as-grant.** Reuse wins: the Cedar `Policy` CRD + binding-as-grant already *are* the "target a resource with a capability" primitive; the gap is the *wiring* abstraction, not a new resource. Rejected new-primitive: would duplicate what Cedar policies already express and fork the model.

## Decision

Introduce a **`Capability`** value type, a **`PrincipalSource`** list, and a **`Registry`** in `internal/auth/cedar`. Two axes are kept **orthogonal** (judge B1): a `Capability` is `{actions + resource shape + binding attribute + built-in}`; *where a principal's binding attributes are read from* is a `PrincipalSource`. The registry **assembles** the shared artifacts from the registered set:

- the **composite `EntityProvider`** — for a request it (1) resolves the principal's backing object via the **ordered `PrincipalSource` list** (Function-first, else CatalogService — the ADR-0088 provider fallback, now a *source* not a capability), (2) attaches *every* capability's `PrincipalBinding` Set (a capability that doesn't apply to that source type contributes nothing), then (3) dispatches the resource to the **first capability whose `Resource` returns `ok==true`** (the sole dispatch mechanism — never a `v1.Kind`↔Cedar-entity-type match, which never holds: `KindKVStore`→`KVTable`, `KindBucket`→`BlobPrefix`);
- the **assembled schema vocabulary** — `KnownAction`/`KnownEntityType` unioned from the capabilities' `Actions` + `EmitsEntityType` (replacing the hand-listed `curatedActions`/`curatedEntityTypes` maps in `schema.go`);
- the **built-in `PolicySet`** — the concatenation of the registered capabilities' built-in permit texts.

The **three** existing capabilities (kv, invoke, s3) become `Capability` values + the **two** principal sources (Function, CatalogService) registered at composition. `EntitiesFor`'s per-capability branches and the inline provider fallback are deleted — the binding logic moves into each `Capability`'s `PrincipalBinding.Bind`, the resource logic into its `Resource`, and the Function-then-CatalogService resolution into the sources. Adding egress (ADR-0117) is registering one more `Capability`; no shared code changes. The `auth.Authorizer` port, the routing authorizer, default-deny, and the binding-as-grant semantics are untouched.

## Temporary workarounds

None. (The migration is complete in this ADR — no bridging shim between the old monolith and the registry; the old `EntitiesFor` branches are removed, not deprecated.)

## Contracts

### The registry (`internal/auth/cedar`)

```go
package cedar

import (
	"context"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// Capability declares one authorization capability (kv, invoke, s3, egress, …) so the schema
// vocabulary, the EntityProvider, and the built-in permits are ASSEMBLED from the registered set —
// registering a new capability adds no branch to shared code. All funcs are typed (no any).
//
// Two orthogonal axes are kept separate (judge B1): a Capability is an {action + resource + built-in}
// on a PRINCIPAL; *where a principal's binding attributes come from* is a PrincipalSource (below), so the
// ADR-0088 provider fallback (Function-first, else CatalogService) is NOT a fourth capability — s3 is one
// capability with two principal sources.
type Capability struct {
	// Name is the capability's short id (e.g. "kv", "invoke", "s3", "egress") — for diagnostics/dedup.
	Name string
	// Actions are the Cedar actions this capability authorizes (e.g. auth.ActionKVRead, ActionKVWrite).
	// Unique across the registry (no two capabilities share an action).
	Actions []auth.Action
	// EmitsEntityType is the Cedar entity type this capability materializes as a resource (e.g.
	// entityTypeKVTable). Used ONLY for schema-vocabulary assembly (KnownEntityType) — NOT a dispatch key
	// (an EntityRef.Type v1.Kind never equals the Cedar entity type: KindKVStore→KVTable, KindBucket→
	// BlobPrefix). Resource dispatch is by the Resource func's ok, below.
	EmitsEntityType cedartypes.EntityType
	// Resource materializes the resource entity (UID + parents + owner attr) for a request whose resource
	// this capability owns. ok==false ⇒ not ours (try the next capability) — this ok is the SOLE resource-
	// dispatch mechanism. A dangling resource still returns ok==true with bare entities (evaluable,
	// default-deny). nil ⇒ the capability owns no resource shape (Policy-only over an existing entity).
	Resource func(ctx context.Context, r MetaReader, resource auth.EntityRef) (em cedartypes.EntityMap, ok bool, err error)
	// PrincipalBinding declares this capability's binding-as-grant attribute on the principal: Attr is the
	// principal attribute name (e.g. "kvBindings"); Bind materializes its members from a resolved
	// principal object (see PrincipalSource). The provider always sets Attr (possibly empty), matching
	// today's EntitiesFor (the built-ins also `has`-guard, so empty ≡ absent). Zero value ⇒ no binding.
	PrincipalBinding PrincipalBinding
	// Builtin is this capability's built-in permit policy text (the .cedar granting the capability when
	// principal.<attr>.contains(resource)); "" ⇒ no built-in (Policy-only).
	Builtin string
}

// PrincipalBinding is one binding-as-grant attribute a capability contributes to a principal.
type PrincipalBinding struct {
	Attr string // the Cedar principal attribute (e.g. "kvBindings", "links", "blobBindings")
	// Bind materializes the attribute's Set members from a resolved principal object. src is whatever a
	// PrincipalSource resolved (a *v1.Function or a *v1.CatalogService); a capability that doesn't apply to
	// that source type returns nil (e.g. kv/invoke return nil for a CatalogService — providers have no
	// links/kv). ns is the principal namespace (for building same-namespace resource UIDs).
	Bind func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID
}

// PrincipalObject is a resolved principal-backing object (a *v1.Function or *v1.CatalogService); a
// closed interface (only api/types implements it) so PrincipalBinding.Bind type-switches without `any`.
type PrincipalObject interface{ isPrincipalObject() }

// PrincipalSource resolves the principal's backing object for a principal EntityRef, in order. The
// default sources are Function-first then CatalogService-fallback (ADR-0088) — the provider case, now a
// principal SOURCE, not a capability. A source returns ok==false to defer to the next.
type PrincipalSource func(ctx context.Context, r MetaReader, p auth.EntityRef) (obj PrincipalObject, ok bool, err error)

// Registry is a deduped set of capabilities + an ordered principal-source list. It assembles the shared
// cedar artifacts.
type Registry struct{ /* caps []Capability; sources []PrincipalSource */ }

// NewRegistry validates (unique Name, unique Action across capabilities; ≥1 source) and returns it.
func NewRegistry(caps []Capability, sources []PrincipalSource) (*Registry, error)

// EntityProvider returns the composite provider over r: it resolves the principal via the ordered
// sources, attaches every capability's PrincipalBinding Set, then dispatches the resource to the first
// capability whose Resource returns ok==true.
func (reg *Registry) EntityProvider(r MetaReader) (EntityProvider, error)

// KnownAction / KnownEntityType are the assembled schema vocabulary (union over the capabilities'
// Actions + EmitsEntityType) — the predicates the driver's ValidateCedar uses (replacing the hand-listed
// curatedActions / curatedEntityTypes maps in schema.go).
func (reg *Registry) KnownAction(a auth.Action) bool
func (reg *Registry) KnownEntityType(t string) bool

// Builtins returns the concatenated built-in permit policy text (union of the capabilities' Builtin).
func (reg *Registry) Builtins() string

// The three capabilities + the default principal sources (moved from EntitiesFor):
func KVCapability() Capability     // kv::read (binding) + kv::write (owner-forbid); spec.kv → kvBindings
func InvokeCapability() Capability // link::invoke; spec.links → links
func S3Capability() Capability     // s3::read (binding) + s3::write (owner-forbid); spec.blob → blobBindings

func FunctionPrincipalSource() PrincipalSource        // resolve a *v1.Function (the primary source)
func CatalogServicePrincipalSource() PrincipalSource  // ADR-0088 fallback: resolve a *v1.CatalogService when no Function exists
```

### Dependencies & I/O

| Consumes | From |
|---|---|
| `MetaReader` (metastore read) | the store adapter (unchanged, ADR-0074) |
| `*v1.Function` spec fields (`kv`/`links`/`blob`) | the metastore |
| the built-in `.cedar` files (`builtin_kv*.cedar`, `builtin_invoke.cedar`, `builtin_s3.cedar`) | embedded (unchanged content, now owned by their `Capability`) |

| Exposes | To |
|---|---|
| `Registry.EntityProvider/Schema/Builtins` | the cedar driver assembly (`cedar.New` / composition root) |
| `Capability` + the four constructors | the composition root (registration) + future capabilities (egress) |

No new CRD, no new `auth.Action` semantics — the actions are the existing constants, now owned by their capabilities.

## Implementation plan

- **Files:** `internal/auth/cedar/capability.go` (the `Capability` + `PrincipalBinding` + `PrincipalSource` + `PrincipalObject` types + `Registry` + `NewRegistry`/`EntityProvider`/`KnownAction`/`KnownEntityType`/`Builtins`), `internal/auth/cedar/capabilities.go` (the **three** capability constructors `KVCapability`/`InvokeCapability`/`S3Capability` + the **two** sources `FunctionPrincipalSource`/`CatalogServicePrincipalSource`, carrying the UID builders + binding/resource materializers moved out of `entities.go`). Replace `metaEntityProvider`/`EntitiesFor` with the registry's composite provider. Wire `schema.go`'s `ValidateCedar`/`KnownAction`/`KnownEntityType` to the registry vocabulary; assemble the built-in PolicySet (`policies.go`) from the registry. Register the three capabilities + two sources at the composition root (`pkg/funcd` cedar wiring). Add `isPrincipalObject()` markers to `v1.Function`/`v1.CatalogService`.
- **Deps:** none new.
- **Test plan** (one named test per Scenario, all cross-platform):
  - `existing-behavior-preserved` — the **existing** `cedar_test.go`/`invoke_test.go`/`s3_test.go`/`schema_test.go`/`s3_provider_test.go` suites pass **unchanged** against the registry-assembled provider/schema/built-ins (this is the primary guard — do not weaken them).
  - `new-capability-no-entitiesfor-edit` — register a **test-only** dummy capability (a new action + resource type + binding + built-in) and assert its request authorizes correctly **with zero edits to capability.go's shared assembly** (the test only adds a `Capability` value).
  - `binding-grant-still-self-enforces`, `assembled-schema-covers-all`, `unmodeled-principal-or-resource-faults` — unit tests over the registry.
- **Definition of done:** `go build`/`vet`/`test`/`golangci-lint`/`go mod verify` green; the four existing consumer suites pass unchanged; the registry unit tests pass; `EntitiesFor` no longer contains per-capability branches (grep-checkable: no inline `kvBindings`/`links`/`blobBindings` literals outside a `Capability` constructor); `just ci` green after commit.

## Review checklist

- [ ] The four existing consumer suites (kv/invoke/s3/provider/schema) pass **unchanged** — no assertion weakened/deleted (behavior-preserving); the **s3_provider** suite passes with the provider modeled as a `PrincipalSource`, not a capability.
- [ ] The composite provider carries **no per-capability branch** — each capability's binding Set + resource materialization live in its constructor; the Function-then-CatalogService resolution lives in the two `PrincipalSource`s.
- [ ] Resource dispatch is by the `Resource` func's **`ok`** (never a `v1.Kind`↔entity-type match); `KnownAction`/`KnownEntityType` are **assembled** from the registry (replacing the hand-listed `curated*` maps), and the built-in PolicySet is the union of the capabilities' `Builtin`.
- [ ] A new capability registers **without editing** capability.go's assembly (proven by the dummy-capability test).
- [ ] `NewRegistry` rejects a duplicate `Name` or a duplicate `Action` across capabilities, and requires ≥1 `PrincipalSource` (`fault.Invalid`).
- [ ] `auth.Authorizer` port, routing, default-deny, and binding-as-grant **semantics** unchanged; no accepted-ADR contradiction.
- [ ] Typed surface — no `any` in the `Capability`/`Registry` API; `ctx`-first on `Resource`.
- [ ] Every Scenario has a named passing test.

## Consequences

- **Positive:** one mechanism for binding-as-grant; a new capability (egress next, then Volume/secret/catalog) is a `Capability` value, not a five-place edit — less duplication, fewer silent-authz-gap risks; the existing suites keep proving faithfulness; the "target a resource with a capability" primitive is now first-class and reusable.
- **Negative / accepted:** a sizeable behavior-preserving refactor of accepted/implemented authz code (mitigated: the existing suites are the guard, no new behavior); a small indirection cost (the composite provider iterates capabilities) — negligible on the cached hot path.
- **Risks:** a materialization moved imperfectly could change a decision — caught by the unchanged suites; the schema/built-in assembly must be deterministic (ordered) so cached compilation is stable — asserted.

## Open questions

- **Should the `Capability` registry also drive the PEP call sites** (a generic "authorize capability C on resource R" helper)? Deferred — the PEPs stay per-consumer for now; a shared PEP helper is a later refinement once egress's PEP shows the shape.
- **High-level typed policy CRDs** (VolumePolicy, …) — each its own future ADR compiling to a registered capability; out of scope here.

## References

- [ADR-0074](0074-cedar-authorization-resource-access.md) (Cedar PDP + binding-as-grant — the pattern abstracted) · [ADR-0075](0075-cedar-invoke-authorization.md)/[ADR-0076](0076-cedar-kv-read-binding-grant.md)/[ADR-0088](0088-add-on-provider-s3-identity.md) (the migrated consumers) · [ADR-0092](0092-provider-env-resolution-helper.md) (the DRY-refactor precedent — behavior-preserving, suites as guard).
- `internal/auth/cedar/entities.go` (the monolithic `EntitiesFor` this dissolves) · [FEAT-0001/F84](../feat/0001-feat-v1.1.md).
