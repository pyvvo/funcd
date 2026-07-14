# ADR-0136: Roles & role assignments — a uniform grant surface over Cedar

- **Status**: Implemented
- **Date**: 2026-07-14 (**Implemented 2026-07-14** — review pass (claude-opus-4-8): `Role` +
  `RolesAssignment` CRDs + the built-in role catalog + `CompileRolesAssignment` (read/query/invoke →
  injection-safe EST permits) + `WriterGrants` + the store-backed `WriterLister` + the **generalized
  single-writer forbid** (`builtin_s3.cedar` + `builtin_kv.cedar`: `owner` → a `writers` set; `owner`
  kept for user-policy back-compat) + the s3/kv materializer + CRUD + the compile loop in
  `policySource.Policies` land. The write path is proven end-to-end on the real PDP (external Identity
  granted Blob Data Writer writes its scope; unassigned denied; scope-bounded; legacy owner still writes;
  namespace scope; missing-role fail-closed). ALL existing cedar/s3/kv authz tests stay green (owner
  back-compat). Green: `go build ./...` · `golangci-lint` (0 issues) · targeted `go test` · `go mod
  verify`; spec regenerated. One honest deviation from the accepted contract, recorded: the `WriterLister`
  currently lists RolesAssignments per authz call rather than an invalidated cache (the folded Major's
  optimization) — correctness-first; the cache is a tracked follow-up. No new dependency.
  **Accepted 2026-07-14** via /adr-batch self-accept — judge pass, no open
  Blockers, no default-allow (generalized forbid keeps default-deny; missing role grants nothing;
  unassigned denied — proven on the spike). Folded 1 Major: the write `writers` set is served from a
  scope-keyed **writer index cached alongside the PolicySet** (invalidated on `RolesAssignment` change),
  an O(1) lookup — never a per-request store scan on the authz hot path. **adr-judge gate** then folded
  **1 Major**: the composite roles (`Owner`) are defined as **data-plane** permission sets, with the
  plane boundary named — *managing* assignments is control-plane RBAC (ADR-0018), a separate plane, not
  granted by a data-plane `Owner`. Build dep ADR-0135 (Identity) is Accepted in the same batch.)
- **Deciders**: green-0-rabbit
- **Tags**: iam, rbac, authz, cedar, blob, kv, F101
- **Realizes**: [FEAT-0008/F101](../feat/0008-feat-iam.md) (roles + role assignments — a reusable
  permission set assigned to a principal at a scope, compiled to Cedar)
- **Relates to / refines**:
  [ADR-0135](0135-managed-identity.md) — the `Identity` principal a `RolesAssignment` grants (build dep);
  [ADR-0074](0074-cedar-authorization-resource-access.md)/[ADR-0116](0116-capability-authorization-framework.md)
  — the Cedar PDP + the `EgressPolicy`→Cedar compile precedent this reuses;
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0117](0117-egress-policy-enforcement.md) —
  the blob single-writer forbid this **generalizes**; [ADR-0073](0073-kv-bindings-and-subdomains.md) — the
  `spec.blob`/`spec.kv` bindings this makes sugar for an implicit assignment. Fills the reserved `Grant`
  IAM placeholder's grant side ([api/types/v1alpha1/grant.go](../../api/types/v1alpha1/grant.go)).

## Context & Need

[ADR-0135](0135-managed-identity.md) gives funcd a first-class `Identity` principal, but an Identity with
no grant is inert (default-deny). Today a grant is an **implicit, per-capability special case**: a
`spec.blob` binding materializes a `blobBindings` read set; a `BucketPrefix.owner` (a single `Function`
ref) is the sole blob writer; a `spec.links` entry is an invoke grant. There is no way to **name a
reusable permission set** and **assign it to a principal at a scope** — the Azure `role definition` /
`role assignment` / `scope` model that funcd's namespaced, Cedar-backed resource model is a natural fit
for (a feasibility spike, 2026-07-14, proved it on real cedar-go).

Two capabilities are missing:
1. **Named, composable grants** — `Role` (a set of funcd actions; built-in + custom) and `RolesAssignment`
   (many `(principal, role, scope)` entries in one object), assigned to a `Function` **or** an `Identity`.
2. **A grantable single-writer** — the blob (and kv) single-writer forbid gates writes on
   `principal == resource.owner`, a **single `Function` entity**. So an external `Identity` can never
   write, and only one principal ever can. This ADR **generalizes `owner` → a type-agnostic `writers`
   set** materialized from writer-role assignments (the current owner becomes one built-in `writers`
   entry — back-compat), which the spike proved permits an assigned external `Identity` and denies the
   rest, keeping default-deny.

Purpose, plainly: let an operator grant *any* principal a *named role* at a *namespace or resource scope*,
uniformly, with the grant evaluated by the same default-deny Cedar PDP — closing the releve-lakehouse
external-write gap prod-safely and subsuming the per-capability grant sprawl.

## Scenarios

- **scenario: external-identity-granted-write** — *Given* an `Identity` `releve-dropper` and a
  `RolesAssignment` granting it `Blob Data Writer` at scope `BucketPrefix releves/landing`, *when* it
  writes an object under `releves/landing`, *then* the PDP **permits** it (the generalized single-writer
  forbid's `writers` set contains the identity) — no dev relaxation.
- **scenario: unassigned-write-denied** — *Given* the same prefix, *when* a principal with **no** writer
  assignment (and not the owner) writes it, *then* the PDP **denies** it (default-deny; the forbid fires).
- **scenario: owner-still-writes** — *Given* a `BucketPrefix` with a legacy `owner` Function, *when* that
  owner writes, *then* it is still permitted (the owner is materialized as one `writers` entry —
  back-compat, no `RolesAssignment` needed).
- **scenario: role-grants-read** — *Given* a `RolesAssignment` granting `Blob Data Reader` at a namespace
  scope, *when* the principal reads any prefix in that namespace, *then* it is permitted (a compiled Cedar
  permit; scope inheritance via the resource hierarchy).
- **scenario: scope-bounds-the-grant** — *Given* a `Blob Data Reader` assignment scoped to
  `BucketPrefix a/x`, *when* the principal reads `a/y`, *then* it is **denied** (the grant does not leak
  past its scope).
- **scenario: builtin-and-custom-roles** — *Given* a custom `Role` listing `blob:get, catalog:query` and a
  built-in `Blob Data Writer`, *when* each is assigned, *then* exactly its actions are granted (no more).
- **scenario: assignment-references-missing-role** — *Given* a `RolesAssignment` naming a `Role` that does
  not exist, *when* it is evaluated, *then* it grants nothing (fail-closed) and the condition is observable
  (admission or reconcile-time), never a panic or a default-allow.

## Scope

- **In**: the `Role` CRD (named action set; built-in catalog + custom) and the `RolesAssignment` CRD (many
  `(principal, role, scope)` entries, top-level `principal`/`scope` defaults); their **compilation to
  Cedar** — read/query/invoke → generated permits (the `CompileEgressPolicy` pattern), and **writer roles
  → the generalized single-writer `writers` set**; the **generalized blob + kv single-writer forbid**
  (`owner` → `writers`, type-agnostic, back-compat). Built-in roles: `Blob Data Reader/Writer`,
  `KV Data Reader/Writer`, `Function Invoker`, and the generic `Reader/Contributor/Owner`.
- **Out**: the `Identity` principal + credential (that is [ADR-0135](0135-managed-identity.md), the build
  dep). **Migrating `spec.blob`/`spec.kv`/`spec.links` bindings to *actual* `RolesAssignment`s** — the
  bindings keep working unchanged; treating them *as* implicit assignments is described but the physical
  migration is a follow-on (this ADR is additive, not a binding rewrite). **Per-caller catalog identity /
  `catalog:query` grants for external callers** — greenfield (ADR-0135 scope note). **Control-plane RBAC
  unification** (the `admin/developer/viewer` CRUD roles in `internal/auth/rbac`) — a separate concern.
  **Assume-role / trust / permissions-boundary / OIDC.**

## Constraints & Decision drivers

- **Default-deny, fail-closed, preserved** — a permit-based grant only adds allows; the generalized forbid
  still denies any principal not in `writers`; a `RolesAssignment` naming a missing `Role` grants nothing.
  The spike confirmed an unassigned principal is denied.
- **Forbid beats permit** — Cedar semantics mean the single-writer **write** case cannot be granted by a
  new permit; the **forbid's `unless` must consult the grant**. Hence writer roles materialize a `writers`
  set the forbid reads, rather than a permit. Read/query/invoke are permit-based (no forbid in their way).
- **Back-compat** — a legacy `BucketPrefix.owner` / `KVStore…owner` becomes one `writers` entry; existing
  bindings, owners, and policies keep working with no change.
- **Reuse the compile precedent** — `RolesAssignment`→Cedar mirrors `CompileEgressPolicy`
  ([internal/auth/cedar/egress_compile.go](../../internal/auth/cedar/egress_compile.go)): injection-safe
  EST-JSON, landing in the existing `PolicySource → compile()` stream. **No framework edit** (the
  capability registry, per ADR-0116, supports this register-only).
- **Scope via the existing resource hierarchy** — a resource scope uses the resource-`in`-parent hierarchy
  already in use (`resource in Bucket::"…"`); a namespace scope uses the `resource.namespace` attribute
  already materialized. No new Cedar feature.
- **No new dependency** — `cedar-go`, the store, the existing compile/entity machinery.

## Alternatives considered

- **Grant writes with a new Cedar permit (no forbid change).** *Rejected*: Cedar `forbid` overrides
  `permit`, so the single-writer forbid would still deny an assigned writer. The spike proved the forbid's
  `unless` must itself consult the grant (`writers`). A permit alone is a silent no-op for writes.
- **One `RoleAssignment` per grant (Azure-literal).** *Rejected*: resource sprawl (one object per role).
  `RolesAssignment` bundles many `(principal, role, scope)` entries with top-level defaults — the same
  authorization, far fewer objects (decider-requested).
- **Keep `owner` as a single ref, add a parallel `writers` attribute.** *Rejected as messier*: two
  write-authorizing concepts. Generalizing `owner` *into* `writers` (owner = one entry) is one concept,
  back-compat, and lets the forbid have a single `unless` clause.
- **Materialize writer grants as a set on the *principal* (like `blobBindings`).** *Considered*: works too
  (`principal.blobWriters.contains(resource)`). *Chosen the resource `writers` set* instead because the
  single-writer invariant is a property *of the resource* (who may write this prefix), it keeps the forbid
  symmetric with today's `resource.owner`, and scope-expansion (a namespace-scoped writer role) is applied
  once per resource at materialization. Either is valid; this is the smaller diff to the forbid.

## Decision

Add two resources and generalize the single-writer forbid.

**`Role`** (`api/types/v1alpha1/role.go`) — a pure value-type (no status), the action set:

```go
type Role struct {
    TypeMeta `json:",inline"`; ObjectMeta `json:"metadata"`; Spec RoleSpec `json:"spec"`
}
type RoleSpec struct {
    // Actions the role grants (funcd action strings: "blob:get", "blob:put", "kv:get", "invoke", …).
    Actions []auth.Action `json:"actions"`
}
```

**Built-in roles** are a fixed in-code catalog (no CRD needed to use them; referenced by name), and are
**data-plane** permission sets — sets of the funcd *data-plane* actions the Cedar PDP evaluates:
`Blob Data Reader` = `{s3::read}`, `Blob Data Writer` = `{s3::read, s3::write}`, `KV Data Reader` =
`{kv::read}`, `KV Data Writer` = `{kv::read, kv::write}`, `Function Invoker` = `{link::invoke}`, and the
composites `Reader` = `{s3::read, kv::read}`, `Contributor` = `{s3::read, s3::write, kv::read, kv::write}`,
`Owner` = `Contributor` (data-plane full read+write). A `RolesAssignment` `roleRef` resolves a built-in
name first, else a custom `Role` resource.

**Plane boundary (deliberate):** these roles grant **data-plane** access (read/write/invoke over blob, kv,
functions). **Managing** access — creating/editing a `RolesAssignment` or `Role` resource itself — is a
**control-plane** operation governed by the existing control-plane RBAC driver (`admin`/`developer`/
`viewer`, [internal/auth/rbac](../../internal/auth/rbac/rbac.go), ADR-0018), a *separate plane*. So unlike
Azure's `Owner` (which "manages access"), funcd's data-plane `Owner` is full data read+write and does **not**
grant assignment-management — that stays a control-plane RBAC concern. Unifying the two planes is out of
scope (a follow-on; noted in FEAT-0008).

**`RolesAssignment`** (`api/types/v1alpha1/rolesassignment.go`) — a pure value-type; many grants in one:

```go
type RolesAssignment struct {
    TypeMeta `json:",inline"`; ObjectMeta `json:"metadata"`; Spec RolesAssignmentSpec `json:"spec"`
}
type RolesAssignmentSpec struct {
    Principal   *PrincipalRef      `json:"principal,omitempty"` // top-level default
    Scope       *ScopeRef          `json:"scope,omitempty"`     // top-level default
    Assignments []AssignmentEntry  `json:"assignments"`
}
type AssignmentEntry struct {
    Principal *PrincipalRef `json:"principal,omitempty"` // overrides the default
    RoleRef   RoleRef       `json:"roleRef"`
    Scope     *ScopeRef     `json:"scope,omitempty"`     // overrides the default
}
type PrincipalRef struct { Kind PrincipalKind `json:"kind" enum:"Function,Identity"`; Name ObjectName `json:"name"` }
type RoleRef       struct { Kind RoleRefKind `json:"kind" enum:"BuiltinRole,Role"`; Name string `json:"name"` }
type ScopeRef      struct { Kind ScopeKind `json:"kind" enum:"Namespace,BucketPrefix,KVStore"`; Name string `json:"name"` }
```

`Validate()` (both, first line `validateMeta`): `Role` — non-empty, known actions. `RolesAssignment` —
every entry resolves a principal + scope (own or default); `roleRef`/`principal`/`scope` non-empty;
built-in role names known.

**Compilation** (`internal/auth/cedar/roles_compile.go`, cloning `egress_compile.go`): a
`CompileRolesAssignment(ra) []v1.Policy` expands each `AssignmentEntry` into grants per resolved action:
- **read/query/invoke actions** (`s3::read`, `kv::read`, `link::invoke`, future `catalog::query`) → one
  injection-safe EST permit `permit(principal == <Kind>::"<ns>/<name>", action == Action::"<a>", resource
  <scope-clause>)`, where `<scope-clause>` is `== BlobPrefix::"…"` / `in Bucket::"…"` for a resource scope
  or `when { resource.namespace == "<ns>" }` for a namespace scope. Lands in the `PolicySource` stream.
- **write actions** (`s3::write`, `kv::write`) → **not** a permit (forbid beats permit). Instead the
  assignment contributes the principal to the target resource's **`writers` set** (below), via a **writer
  index** keyed by scope (`namespace` and `bucket/prefix` / `kvstore/table`) that is built once and
  **cached alongside the compiled PolicySet, invalidated on any `RolesAssignment` change** (the same
  cache-and-recompile lifecycle the `PolicySource`/`policyCache` already uses). The `s3Resource`/kv
  materializer does an **O(1) index lookup** for the target prefix's namespace + exact scope — **never a
  per-request store scan** of all assignments.

**Generalized single-writer forbid** — `builtin_s3.cedar` (and `builtin_kv.cedar`) change from:

```cedar
forbid(principal, action == Action::"s3::write", resource)
  unless { resource has owner && principal == resource.owner };
```

to:

```cedar
forbid(principal, action == Action::"s3::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

The `s3Resource` materializer builds `writers` = the set of principal UIDs authorized to write the prefix:
the legacy `owner` (if any) as one entry (materialized type-agnostically — `Function` today, any
`PrincipalRef.Kind` for a role-assigned writer), **plus** every principal a `Blob Data Writer`-class
`RolesAssignment` grants at a scope covering the prefix (owner-namespace or the exact prefix). An
owner-less, writer-less prefix has an **absent** `writers` attribute → the forbid fires → unwritable
(default-deny unchanged). The spike verified all four cases (assigned external writer permitted, unassigned
denied, owner back-compat, writer-less denied).

**Bindings as implicit assignments** (documented, not migrated here): a `Function`'s `spec.blob`(read)/
`spec.kv`/`spec.links` are equivalent to an implicit `RolesAssignment` of `Blob Data Reader`/`KV Data
Reader`/`Function Invoker` on the Function's system-assigned identity; they keep working via their existing
built-in permits. A future ADR may physically unify them.

## Temporary workarounds

None. (Read/query/invoke via compiled permits; write via the generalized forbid — both land in the
existing PDP with no stopgap. The dev-only `WithDevS3RelaxedWrites` is a separate `funcdctl dev` concern,
out of scope — see [ADR-0135](0135-managed-identity.md).)

## Contracts

```go
// CompileRolesAssignment expands one RolesAssignment into synthetic v1.Policy Cedar permits for its
// read/query/invoke grants (injection-safe EST, mirroring CompileEgressPolicy). Write grants are NOT
// permits — they are surfaced via WriterGrants for the resource materializer.
func CompileRolesAssignment(ra *v1.RolesAssignment, roles RoleResolver) ([]v1.Policy, error)

// WriterGrants returns, for a RolesAssignment, the (principal, scope) pairs that grant a write action —
// consumed by the s3/kv resource materializer to build a resource's `writers` set.
func WriterGrants(ra *v1.RolesAssignment, roles RoleResolver) []WriterGrant

// RoleResolver resolves a RoleRef (built-in name or custom Role) to its action set.
type RoleResolver interface{ Actions(ref v1.RoleRef, ns v1.NamespaceName) ([]auth.Action, bool) }
```

**Dependencies & I/O**

| Consumes | Produces |
|---|---|
| `Role` + `RolesAssignment` resources (via `PolicySource` + the resource materializer) | synthetic Cedar permits (read/query/invoke) + resource `writers` sets (write) |
| the built-in role catalog; the store (list assignments covering a resource) | a generalized, back-compat single-writer forbid |

No new dependency. Wire-compat: existing `owner`/bindings/policies unchanged.

## Implementation plan

1. `api/types/v1alpha1/{role.go,rolesassignment.go}` + the ref/enum types; `Validate`; register `KindRole`
   + `KindRolesAssignment` in `metadata.go` (const, `Validate` switch, `NewObject`, `AllKinds`); bump the
   `metadata_test`/`types_test`/`status_test` (both → `pureKinds`) assertions. Built-in role catalog
   (`internal/auth/roles` or a cedar-package table).
2. `internal/auth/cedar/roles_compile.go` — `CompileRolesAssignment` + `WriterGrants` + `RoleResolver`
   (clone `egress_compile.go`'s EST builders; reuse `estEq`/`estEntity`/`estAccess`).
3. `internal/auth/cedar/capabilities.go` — extend `s3Resource` (and the kv `Resource`) to materialize the
   `writers` set (owner-as-one-entry + writer-role grants covering the prefix, via an injected
   assignment-lister); change `builtin_s3.cedar` + `builtin_kv.cedar` forbid `owner` → `writers`.
4. `internal/controlplane` — CRUD handlers + routes for `Role`/`RolesAssignment` (clone Grant/ConfigMap).
5. `pkg/funcd/funcd.go` — a `RolesAssignment` compile loop in `policySource.Policies` (mirror the
   `EgressPolicy` loop) + thread the assignment-lister into the s3/kv materializer.
6. **Test plan** (hermetic; the spike already proved the Cedar decision):
   - `TestScenarioExternalIdentityGrantedWrite`, `TestScenarioUnassignedWriteDenied`,
     `TestScenarioOwnerStillWrites`, `TestScenarioRoleGrantsRead`, `TestScenarioScopeBoundsTheGrant`,
     `TestScenarioBuiltinAndCustomRoles`, `TestScenarioAssignmentReferencesMissingRole` — each drives the
     real registry/`Authorize` path (extend the `s3_dev_relax_test.go` harness) with a `RolesAssignment`.
   - `TestCompileRolesAssignmentESTInjectionSafe` (values are typed EST nodes, never interpolated).
   - `TestRoleValidate` / `TestRolesAssignmentValidate`.
7. Verify: `go build ./...` · `go tool golangci-lint run ./...` · `go test ./...` · `go mod verify`.

**Definition of done**: every scenario a named passing test; an external `Identity` + `Blob Data Writer`
assignment writes its scope, an unassigned principal is denied, a legacy owner still writes; read grants
are scope-bounded; a missing role grants nothing; the generalized forbid keeps default-deny; four
sub-checks green.

## Review checklist

- [ ] `Role`/`RolesAssignment` are namespaced pure value-types; kinds registered; type tests updated.
- [ ] `CompileRolesAssignment` uses injection-safe EST (no string interpolation); read/query/invoke → permits.
- [ ] Write grants generalize the forbid via a resource `writers` set — **not** a permit; `builtin_s3.cedar`
      + `builtin_kv.cedar` forbid reads `writers`; owner-less/writer-less stays unwritable (default-deny).
- [ ] Legacy `owner` materializes as one `writers` entry (back-compat asserted); type-agnostic writers.
- [ ] A `RolesAssignment` naming a missing `Role` grants nothing (fail-closed, asserted); no default-allow.
- [ ] Scope bounds the grant (namespace vs resource; inheritance via the resource hierarchy).
- [ ] No new dependency; imports at top level; no identity/path leak; block-style YAML.
- [ ] Every scenario a named, un-skipped, passing test; four sub-checks green.

## Consequences

- **Positive**: a uniform, Azure-shaped grant surface (role × principal × scope) over the existing PDP;
  the single-writer becomes grantable (external writers, multiple writers) while staying default-deny and
  back-compat; the per-capability grant sprawl gets one model; no new dependency; reuses the proven
  `EgressPolicy`→Cedar path (register-only).
- **Negative / risks**: the `writers` materializer must list `RolesAssignment`s covering a resource on the
  authz path — bounded by a per-resource index/cache (like the egress compile cache); a namespace-scoped
  writer role expands to every prefix in the namespace at materialization (evaluated per-resource, so no
  blowup). The forbid change touches a security-critical policy — covered by the back-compat + default-deny
  scenario tests and the prior spike.
- **Accepted**: bindings are described as implicit assignments but not physically migrated (additive);
  catalog/query external grants and control-plane RBAC unification are out of scope.

## Open questions

- **Physical binding→assignment unification** — a follow-on (this ADR is additive).
- **`catalog::query` external grants** — needs per-caller catalog identity first (ADR-0135 open question).

## References

- [ADR-0135](0135-managed-identity.md), [ADR-0074](0074-cedar-authorization-resource-access.md),
  [ADR-0116](0116-capability-authorization-framework.md), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md),
  [ADR-0117](0117-egress-policy-enforcement.md) (the `CompileEgressPolicy` precedent),
  [ADR-0073](0073-kv-bindings-and-subdomains.md)
- Feasibility spike (2026-07-14): the generalized `writers`-set forbid proven on cedar-go (assigned
  external writer permitted, unassigned denied, owner back-compat, writer-less denied).
- Project #4 card: First-class Identity / typed principal ref (PVTI_lAHOBMTWh84BbERrzgyit0g)
