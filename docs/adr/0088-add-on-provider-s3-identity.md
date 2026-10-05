# ADR-0088: Add-on provider identity in the F47/Cedar authorization model

- **Status**: Implemented
- **Superseded in part by**: [ADR-0175](0175-engine-is-its-own-principal.md) (2026-10-05) — Function-first precedence and the name-based principal UID: a provider engine is its own principal, looked up by kind.
- **Date**: 2026-07-01 (accepted + implemented 2026-07-01 — review `pass` ([scorecard](../reviews/adr-0088-implementation-claude-opus-4-8.md)); judge: judge: sound, verified against the real code (the in-platform keypair principal is a name-based `Function:ns/name` UID, `auth.go:43`; the policy is principal-agnostic; the prefix-owner UID is name-based) — no Blocker/Major; one documented Minor (name-collision precedence → Function-first))
- **Deciders**: green-0-rabbit
- **Tags**: add-on-provider, s3, cedar, authorization, identity, blob, lakehouse
- **Realizes**: [FEAT-0003/F58](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (F47 — **extends** its blob identity/entity
  model + the prefix-owner admission to providers; ADR-0080 stays frozen) · [ADR-0074](0074-cedar-authorization-resource-access.md)
  (the Cedar resource-authz EntityProvider this refines) · [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md)
  (the per-fn keypair the engine already carries) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)
  (the CatalogService — the first provider) · [ADR-0087](0087-add-on-provider-runtime.md) (the provider runtime that
  deploys the engine without a backing Function).

## Context & Need

**Purpose**: let an **add-on provider** engine (deployed by ADR-0087 with no backing Function) actually **use the F47
S3 surface** — read the Parquet it binds and write the prefix it owns — under the *same* Cedar binding-as-grant as any
function. **Callers**: the F48 `CatalogService` engine (and every future provider) whose injected keypair (ADR-0085)
must resolve to a real S3 identity.

**Why now**: building the F48 live e2e proved the engine is fully **authz-denied**. The Cedar S3 policy
([builtin_s3.cedar](../../internal/auth/cedar/builtin_s3.cedar)) is sound and **principal-agnostic** — it grants
`s3::read` when `principal.blobBindings.contains(resource)` and `s3::write` when `principal == resource.owner`. The
gap is in the **entity layer**, not the policy: `EntitiesFor` ([entities.go](../../internal/auth/cedar/entities.go))
materializes a principal's `blobBindings` only from a **Function's** `spec.blob`, and the `bucket-prefix-owner-exists`
admission requires a prefix `owner` to be a real **Function**. ADR-0087 removed the backing Function — so the engine's
keypair authenticates as `Function:<ns>/<name>` but has **no `blobBindings`** (read denied) and **can't be a prefix
owner** (the Bucket is rejected at apply, and `principal == owner` has nothing to match). The keypair principal and the
prefix-owner are **name-based UIDs** (`functionUID(ns, name)`), so they already *match* once both sides resolve — the
only missing pieces are sourcing the engine's bindings and admitting it as an owner.

## Scenarios

- **scenario: provider-reads-bound-prefix** — *Given* a `CatalogService` whose `spec.blob` binds `(lakehouse, gold)`,
  *When* its engine `GetObject`s under `gold/`, *Then* `s3::read` is **granted** (the engine's `blobBindings` are
  materialized from the CatalogService's `spec.blob`).
- **scenario: provider-writes-owned-prefix** — *Given* the `gold` prefix's `owner` is the `CatalogService` `lake`,
  *When* `lake`'s engine `PutObject`s under `gold/`, *Then* `s3::write` is **granted** (`principal == resource.owner`,
  both `Function:default/lake`).
- **scenario: provider-denied-unbound** — *Given* the engine, *When* it reads/writes a prefix **not** in its
  `spec.blob` / not owned, *Then* it is **denied** (default-deny holds — no binding, no ownership).
- **scenario: bucket-owner-may-be-a-provider** — *Given* a Bucket whose `prefixes[].owner` names a `CatalogService`,
  *When* it is applied, *Then* the `bucket-prefix-owner-exists` admission **admits** it (owner = a Function **or** a
  CatalogService).
- **scenario: function-takes-precedence** — *Given* a Function **and** a CatalogService share a name in a namespace,
  *When* the principal's bindings are resolved, *Then* the **Function** wins (deterministic; no ambiguity).

## Scope

**In**: the Cedar **EntityProvider** sources a principal's `blobBindings` from a **CatalogService** when no Function of
that name exists (Function-first); the **`bucket-prefix-owner-exists` admission** accepts a CatalogService as a prefix
owner. That is all — the **policy is unchanged**, the principal/owner **UID scheme is unchanged** (name-based), and the
keypair/auth path (ADR-0085) is unchanged.

**Out**: a distinct Cedar **provider principal type** (kept Function-shaped — name-based UIDs already match; a new type
is unneeded churn); **non-blob** provider authz (kv/invoke for providers — no consumer yet); **cross-namespace**
provider ownership; generalizing beyond `CatalogService` to a hypothetical provider kind (done when the second
provider CRD lands — the resolution is a small switch then). A provider still has **no** Function lifecycle (ADR-0087).

## Constraints & Decision drivers

- **Policy stays principal-agnostic** — `builtin_s3.cedar` already grants read/write on bindings/ownership for *any*
  principal; do not touch it. The fix is purely entity resolution + admission.
- **Name-based identity already matches** — the keypair principal and the prefix owner are both `functionUID(ns,
  name)`; a provider need not be a new principal *type*, only a new *source* of bindings.
- **Function-first, deterministic** — if a name is both a Function and a CatalogService, the Function's bindings win
  (it is the execution identity; the provider is the fallback). No ambiguous grant.
- **Default-deny preserved** — a provider with no `spec.blob` gets no `blobBindings` (inert); an unowned prefix stays
  read-only. The change only *adds* a binding source, never a blanket allow.
- **Extends, not supersedes, ADR-0080** — ADR-0080's Function model is untouched; this adds the provider as a second,
  lower-precedence binding source + owner kind. ADR-0080 stays frozen.

## Alternatives considered

| Option | Why it lost / won |
|---|---|
| **Source provider bindings in `EntitiesFor` + admit a provider owner — chosen** | Two surgical changes, policy + UID scheme untouched, name-based identity already matches. Minimal blast radius; generalizes to future providers with a small switch. |
| **A distinct Cedar `Provider` principal type** (new UID type + schema + `principalFor` change) | Cleaner separation on paper — but the keypair principal is already `Function:ns/name` and the owner UID is name-based, so a new type buys nothing and forces changes to `principalFor`, `principalUID`, the schema, and the policy. Rejected as churn. |
| **Re-introduce an identity-only Function** (the CatalogService reconciler creates a non-executed Function) | Gives the engine a real Function entity — but a Function the controller reconciles to `ShapeInvalid`, contradicting ADR-0087's "no backing Function / bypass the Function controller". Rejected: the identity belongs in the authz layer, not a ghost resource. |
| **Relax the S3 PEP to allow any authenticated keypair** | Trivial — but a default-allow that destroys the binding-as-grant security model (the blueprint's default-deny). Rejected outright. |

## Decision

Teach the F47/Cedar authorization layer that an **add-on provider (a `CatalogService`) is an S3 principal and a valid
prefix owner** — two surgical changes; the policy and the UID scheme are untouched.

- **Provider bindings in `EntitiesFor`** ([entities.go](../../internal/auth/cedar/entities.go)). When building the
  principal entity, after the existing Function lookup, **fall back** to a `CatalogService` of the same `(namespace,
  name)`: if found (and no Function was), materialize `blobBindings` from `cs.Spec.Blob` (the same `blobPrefixUID`
  set), plus the `namespace`/`resourceGroup` attrs. A CatalogService has no `links`/`kv`, so those Sets stay empty
  (inert). The principal UID is **unchanged** (`functionUID(ns, name)`, name-based) — so the `s3::write`
  `principal == resource.owner` comparison still matches when the owner names the same provider.
- **Provider owner in the admission** ([admission/bucket.go](../../internal/controlplane/admission/bucket.go)). The
  `bucket-prefix-owner-exists` admission accepts a prefix `owner` that is a real **Function OR CatalogService** in the
  namespace (list both; a name present in either is admitted). The single-writer guarantee is unchanged — one owner
  name per prefix.
- **Function-first precedence** — the Function lookup runs first; the CatalogService is only the fallback. A name that
  is both resolves to the Function's bindings (deterministic).

Net: the F48 engine, deployed by ADR-0087 with its ADR-0085 keypair, now reads its bound Parquet and writes/checkpoints
its owned catalog prefix through the F47 PEP — the live path turns green. The policy, the keypair, and ADR-0080's
Function model are all untouched.

## Temporary workarounds

- **Provider kind hard-coded to `CatalogService`.** `EntitiesFor` + the admission resolve a `CatalogService`
  specifically (the only provider CRD today), not a generic provider abstraction. *Exit*: when a second provider CRD
  lands, replace the single lookup with a small per-provider-kind switch (or a shared "provider principal" interface).

## Contracts

```go
// internal/auth/cedar/entities.go — EntitiesFor principal resolution gains a CatalogService fallback.
// Sketch of the added branch (after the existing Function lookup, replacing the lone `else if NotFound`):
//
//   if fobj, ferr := p.r.Get(ctx, v1.KindFunction.GVK(), ns, name); ferr == nil {
//       /* existing: links + kvBindings + blobBindings from the Function */
//   } else if fault.KindOf(ferr) == fault.NotFound {
//       // Provider fallback (ADR-0088): an add-on provider (CatalogService) is an S3 principal; its
//       // spec.blob is its blobBindings. Function-first — only reached when no Function of this name exists.
//       if csobj, cserr := p.r.Get(ctx, v1.KindCatalogService.GVK(), ns, name); cserr == nil {
//           if cs, ok := csobj.(*v1.CatalogService); ok {
//               attrs := cedartypes.RecordMap{
//                   "namespace":     cedartypes.String(cs.Namespace),
//                   "resourceGroup": cedartypes.String(cs.ResourceGroup),
//               }
//               blob := make([]cedartypes.Value, 0, len(cs.Spec.Blob))
//               for _, b := range cs.Spec.Blob {
//                   blob = append(blob, blobPrefixUID(ns, b.Bucket, b.Prefix))
//               }
//               attrs["blobBindings"] = cedartypes.NewSet(blob...)
//               em[pUID] = cedartypes.Entity{UID: pUID, Attributes: cedartypes.NewRecord(attrs)}
//           }
//       } else if fault.KindOf(cserr) != fault.NotFound {
//           return nil, fault.Wrapf(cserr, fault.Internal, op, "get catalogservice %q", name)
//       }
//   } else {
//       return nil, fault.Wrapf(ferr, fault.Internal, op, "get function %q", name)
//   }
```

```go
// internal/controlplane/admission/bucket.go — bucket-prefix-owner-exists admits a provider owner.
// The owner may be a Function OR a CatalogService in the namespace:
//   fns, _ := a.r.List(ctx, v1.KindFunction.GVK(), ns)
//   css, _ := a.r.List(ctx, v1.KindCatalogService.GVK(), ns)
//   for _, p := range b.Spec.Prefixes {
//       if p.Owner != "" && !nameExists(fns, p.Owner) && !nameExists(css, p.Owner) {
//           return nil, fault.Invalidf(op, "spec.prefixes[%s].owner %q is not a Function or CatalogService in %q", p.Name, p.Owner, ns)
//       }
//   }
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the Cedar EntityProvider's `StoreReader` (already injected) — now also reads `KindCatalogService`; the existing `blobPrefixUID`/`functionUID` helpers |
| Exposes | a provider (CatalogService) as a resolvable S3 principal (`blobBindings`) + a valid Bucket-prefix `owner` |
| Config keys | none |
| New deps | **none** — pure entity-resolution + admission logic over existing types |

## Implementation plan

- **`internal/auth/cedar/entities.go`** — add the CatalogService fallback in `EntitiesFor` principal resolution (the
  contract above); a CatalogService's `blobBindings` from `cs.Spec.Blob`, Function-first.
- **`internal/controlplane/admission/bucket.go`** — `bucket-prefix-owner-exists` lists CatalogServices too and admits
  an owner present in either kind.
- **go.mod**: none. **Test plan** (one per Scenario, in-process over a memory store):
  - cedar entity tests: `provider-reads-bound-prefix` (a CatalogService principal resolves `blobBindings` from its
    spec.blob → `s3::read` permitted on a bound prefix), `provider-denied-unbound` (unbound prefix → denied),
    `function-takes-precedence` (a same-named Function's bindings win), and `provider-writes-owned-prefix` (a
    BlobPrefix `owner` naming the CatalogService → `principal == owner` → `s3::write` permitted) via the Authorizer.
  - admission test: `bucket-owner-may-be-a-provider` (a Bucket prefix owner = a CatalogService is admitted; a
    nonexistent name is still rejected).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; `CGO_ENABLED=0 go build ./...` clean; the
  policy file is unchanged (grep: no edit to `builtin_s3.cedar`); identity/path grep clean; the **live
  `just lima-example-duckdb` lane reaches Ready + both consumers query** (the gate this ADR lifts). `just ci` green
  after commit.

## Review checklist

- [ ] `EntitiesFor` sources a CatalogService principal's `blobBindings` from `cs.Spec.Blob`, **Function-first** (a
      same-named Function wins); the principal UID stays name-based `functionUID(ns, name)`.
- [ ] `bucket-prefix-owner-exists` admits an owner that is a Function **or** a CatalogService; a nonexistent name is
      still rejected (single-writer unchanged).
- [ ] The Cedar **policy is untouched** (`builtin_s3.cedar` unchanged) and so is the UID scheme — the change is
      entity-resolution + admission only.
- [ ] Default-deny preserved: a provider with no `spec.blob` is inert; an unowned/unbound prefix stays denied.
- [ ] One passing test per Scenario; `CGO_ENABLED=0` clean; no new deps.

## Consequences

- **(+)** The F48 catalog engine (and every future add-on provider) is a **first-class S3 principal** — it reads its
  bound Parquet and owns/writes its catalog prefix under the same Cedar binding-as-grant as a function. The ADR-0087
  live path turns green.
- **(+)** **Tiny blast radius** — two functions changed, policy + UID scheme + keypair untouched; the binding-as-grant
  security model is preserved exactly (only a binding *source* is added).
- **(+)** **Generalizes** — a second provider CRD becomes a one-line switch in `EntitiesFor` + the admission.
- **(−)** **Provider kind hard-coded** to `CatalogService` for now (the one provider) — a documented exit.
- **(−)** **Two read paths** in `EntitiesFor` principal resolution (Function then CatalogService) — a small, bounded
  branch.
- **Extends ADR-0080** (the F47 identity/owner model) without superseding it; ADR-0080 stays frozen.

## Open questions

- **A generic provider-principal abstraction** — when the second provider CRD lands, factor the Function-then-provider
  resolution into a small registry/switch (or a `ProviderPrincipal` interface). Resolved when that CRD is scoped.
- **Provider kv/invoke identity** — providers only need blob today; if a provider ever binds KV or links, extend
  `EntitiesFor` the same way. Deferred until a consumer exists.

## References

- [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (F47 — the blob identity/owner model extended here) ·
  [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar EntityProvider) ·
  [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the keypair) ·
  [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)/[ADR-0087](0087-add-on-provider-runtime.md) (the
  provider) · [builtin_s3.cedar](../../internal/auth/cedar/builtin_s3.cedar) (the unchanged, principal-agnostic policy)
  · [FEAT-0003](../feat/0003-feat-data-platform.md).
- Tracking: Project #4 card *"Provider F47/Cedar identity …"* (this ADR realizes it).
