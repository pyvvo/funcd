# ADR-0136 Implementation Review — Roles & role assignments

**Verdict**: **pass** — the role/assignment model + the generalized single-writer forbid conform to the
Contracts; the write path is proven end-to-end on the real PDP; default-deny + owner back-compat
preserved; all existing authz tests green; build/lint/test/mod clean. One recorded ADR-attributed
deviation (the writer cache) filed as a follow-up, not a Blocker.
**Producing model**: claude-opus-4-8.
**Reviewed against**: ADR-0136 Contracts / Scenarios / Review checklist · blueprint security model
(default-deny) · ADR-0116 (compile precedent) · ADR-0080 (single-writer forbid).

## Verification (evidence)

- `go build ./...` → exit 0.
- `go tool golangci-lint run` over all changed packages → **0 issues**.
- `go test ./api/types/... ./internal/auth/... ./internal/services/roles/... ./internal/services/identity/...
  ./internal/controlplane/... ./internal/blob/...` → all `ok` (the cedar/s3/kv single-writer + dev-relax
  suites stay green after the forbid change; the write-path integration tests pass; the OpenAPI spec gate
  passes).
- `go mod verify` → verified. Spec regenerated (`roles`, `rolesassignments` routes present).
- Identity/path grep → clean.

## Conformance to the Contracts

- **`Role` / `RolesAssignment` CRDs** — pure value-types; kinds registered; enumeration tests updated;
  `Validate` enforces entry principal/role/scope resolution.
- **Built-in roles** — the fixed data-plane catalog (`Blob Data Reader/Writer`, `KV Data Reader/Writer`,
  `Function Invoker`, `Reader/Contributor/Owner`); `Owner` is data-plane full read+write (the folded
  plane-boundary Major — managing assignments is control-plane RBAC).
- **`CompileRolesAssignment`** — read/query/invoke → injection-safe EST permits (mirrors
  `CompileEgressPolicy`), scope via `== BlobPrefix` / `in KVStore` / namespace-attr `when`. Write actions
  are NOT permits.
- **Generalized single-writer forbid** — `builtin_s3.cedar` + `builtin_kv.cedar` now `unless { resource
  has writers && resource.writers.contains(principal) }`; the materializer builds `writers` = legacy
  owner (one entry, type-agnostic) + `WriterLister` role grants. The single `owner` attr is **kept** for
  user-policy back-compat. Owner-less/writer-less ⇒ no `writers` ⇒ forbid fires (default-deny).
- **Wiring** — `S3CapabilityWithWriters`/`KVCapabilityWithWriters` + the store-backed `roles.Lister` +
  a `RolesAssignment` compile loop in `policySource.Policies` (revision folds `raRV`).

## Scenario → test map (all passing, real PDP)

| Scenario | Test |
|---|---|
| external-identity-granted-write | `TestScenarioExternalIdentityGrantedWrite` |
| unassigned-write-denied | `TestScenarioExternalIdentityGrantedWrite` |
| owner-still-writes | `TestScenarioExternalIdentityGrantedWrite` |
| scope-bounds-the-grant | `TestScenarioExternalIdentityGrantedWrite` + `TestScenarioRoleGrantsReadScoped` |
| role-grants-read | `TestScenarioRoleGrantsReadScoped` |
| assignment-references-missing-role | `TestScenarioNamespaceScopeAndMissingRole` |
| (namespace scope) | `TestScenarioNamespaceScopeAndMissingRole` |
| builtin-and-custom-roles | `TestRoleValidate` + the built-in catalog exercised throughout |

## ✅ Verified correct — keep

- **Keeping `owner` alongside `writers`** — the fix that kept every existing s3/kv authz test green (user
  policies compare `resource.owner`). Keep; do not "simplify" it away.
- **Forbid-not-permit for writes** — the load-bearing correctness point; the write path gates the forbid's
  `writers`, never a permit (a permit can't beat a forbid). Proven on the real engine.
- **Type-agnostic writers** — an external Identity UID sits in `writers` beside a Function owner.

## Findings

### Blockers / Major
None (as code). 

### ADR-attributed (recorded, not a Blocker)
- The accepted ADR specifies an **invalidated writer-index cache** (O(1)); the V1 `roles.Lister` lists
  RolesAssignments **per authz call**. Correctness is equivalent; only steady-state perf differs. Filed as
  a follow-up card ("cache the RolesAssignment WriterLister"). Not fixed in place to avoid over-engineering
  the cache in this pass.

## Recommendation

Stamp **Implemented**. The IAM epoch (F100 + F101) closes the releve-lakehouse external-write gap
prod-safely. Follow-ups tracked: the writer cache; the dev-leverages-IAM refactor; catalog per-caller
identity; binding→assignment unification.
