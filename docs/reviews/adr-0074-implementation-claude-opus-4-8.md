# ADR-0074 implementation review — Cedar authorization for resource access

- **ADR**: [0074](../adr/0074-cedar-authorization-resource-access.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-23 — the authz spine; verified rigorously.

## Verification (evidence — independently re-run, not trusted)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK — **proves cedar-go is pure-Go** (no cgo) |
| `go test ./...` | **50 ok, 0 fail** (`internal/auth/...` + facade `-race` clean) |
| `go tool golangci-lint run` (touched pkgs) | **0 issues** |
| `go mod verify` + new module | verified; **cedar-go v1.8.0 is the only new module**; module-cache `LICENSE` = **Apache-2.0** |
| OpenAPI regenerated | `Policy`/`PolicySpec` present; `TestSpecGeneratedFromGo` passes |
| `internal/auth/authcontract` (rbac) | **green** — rbac unaffected (runs through the routing authorizer) |

## Security model — verified (the must-haves)

- **Default-deny reads are real.** The cedar driver ships **no** built-in read permit, so a `kv::read` with no
  permitting `Policy` is denied. Proven end-to-end: the e2e logs `cedar default-deny: no permitting policy` and
  returns 403 until `policy.yaml` is applied, then serves **1 → 2** (`TestScenarioE2EKVCounterViaContextKV`),
  plus `TestScenarioCedarDefaultDeny` / `…FacadeReadDefaultDeny` / `…KVReadDefaultDeny`.
- **Owner-write is a built-in `forbid`** ([policies.go](../../internal/auth/cedar/policies.go)):
  `permit(kv::write); forbid(kv::write) unless { resource has owner && principal == resource.owner }` — a
  non-owner write is denied **even with a permissive read Policy**; the `has owner` guard keeps an owner-less
  table read-only. Not a user Policy. (`TestScenarioOwnerWriteViaPolicy` / `…KVNonOwnerReadsButCannotWrite`.)
- **Principal is connection-scoped** — `Function::"<ns>/<fn>"` is built from the `(ns, fn)` the local API
  passes from the fixed `Ref`, never the request body (`TestScenarioPerFunctionPrincipal`).

## ✅ Verified correct (keep)

- **cedar is a driver behind the existing port** — `internal/auth/cedar` implements `auth.Authorizer`; a
  **routing authorizer** sends `Action`-bearing requests to cedar, coarse `Verb/Kind/Namespace` ones to rbac.
  The `Request` additions (`Action`, `Resource`, `Identity.Principal`) are additive — rbac + `authcontract`
  untouched. The layering ADR-0018 reserved is honoured; don't collapse it.
- **Entities materialized from resources, only policies persisted** — the `EntityProvider` resolves only the
  **request-relevant** entities (principal + `KVTable` + parent `KVStore`) per call from the metastore; the
  compiled `PolicySet` is cached. No duplicate state, no full-store rebuild per op. `owner` is a `Function`
  entity-reference so `principal == resource.owner` compares entities; `KVTable in KVStore` via parents.
- **The real cedar-go API was used** (from `go doc`, not guessed): `NewPolicySetFromBytes`/`NewPolicyListFromBytes`,
  `types.EntityMap`/`Entity{UID,Parents,Attributes}`, `cedar.Authorize(policies, entities, types.Request)→(Decision,Diagnostic)`.
- **`Policy` CRD + policy-validity admission** — parses the Cedar text + curated-schema-checks actions/entity
  types (avoids cedar-go's experimental validator); invalid ⇒ `fault.Invalid`. Examples carry a read `Policy`.
- **Incremental scope** — rbac keeps control-plane CRUD; egress/secrets/invoke deferred. One new dep.

## Findings
None (Blocker/Major/Minor). One honest adaptation: `KVTable` is not a CRD (tables are inline in
`KVStore.spec.tables`), so the KV PEP addresses a table as `EntityRef{Type: KindKVStore, Name: store, Path:
table}` and the provider builds the `KVTable` cedar entity (UID `<ns>/<store>/<table>`, parent `KVStore`) — a
sound resolution of the ADR's `KVTable`-entity intent without adding a `KindKVTable`. Attribution: in-impl
refinement, not a defect.

## DoD
ADR Review-checklist: **7/7** — cedar-go (Apache-2.0/pure-Go/pinned) as a driver; per-function principal from
the `Ref`; `Request` Action+Resource (back-compat) + routing; cedar driver default-deny + request-relevant
entities + owner entity-ref; `Policy` CRD + validity admission + OpenAPI; KV PEP (reads default-deny, examples
carry a `Policy`, owner-write built-in, coarse read retired, e2e); one new dep + no leak.

## Recommendation
**pass** — `Reviewing → Implemented`. funcd has a real authorization framework: per-function principals, Cedar
`Policy` resources, entities from the metastore, genuine default-deny — KV is the first consumer, and
egress/secrets/invoke can follow behind the same port.
