# ADR-0088 implementation review — add-on provider identity in the F47/Cedar model (F58)

- **ADR**: [0088](../adr/0088-add-on-provider-s3-identity.md) · **Realizes** FEAT-0003/F58
- **Producing model**: claude-opus-4-8
- **Verdict**: **pass** (no Blockers/Majors; the one Minor — name-collision precedence — is by-design + tested)
- **Reviewed against**: the ADR's Contracts/Scenarios/Review-checklist · ADR-0080 (F47 model extended) · ADR-0074
  (Cedar EntityProvider) · ADR-0085/0086/0087 (the provider) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md).

## Verification (run, all via `nix develop -c`)

| Check | Exit |
|---|---|
| `go build ./...` | 0 |
| `CGO_ENABLED=0 go build ./...` | 0 |
| `go test ./internal/auth/cedar/... ./internal/controlplane/admission/... ./internal/services/catalog/... ./api/types/v1alpha1/...` | 0 |
| `go tool golangci-lint run` (cedar + admission) | 0 (0 issues) |
| `go mod verify` | 0 (no go.mod changes) |
| policy untouched (`builtin_s3.cedar` unchanged) | ✓ |
| identity/path leak grep | clean |

## Findings

### 🔴 Blockers / 🟡 Major
None.

### Minor — by-design (tested, not a defect)
- **Name-collision precedence.** When a Function and a CatalogService share a name, the Function's bindings win
  (Function-first). This is the documented, deterministic behavior; `TestScenarioS3ProviderFunctionTakesPrecedence`
  proves it (a same-named binding-less Function shadows the provider → read denied). Tenant-scoped; acceptable.

## ✅ Verified correct — keep
- **Two surgical changes, policy + UID scheme untouched** — exactly the ADR's Decision. `EntitiesFor`
  (`internal/auth/cedar/entities.go`) gains a **Function-first** CatalogService fallback that sources `blobBindings`
  from `cs.Spec.Blob`; the principal UID stays name-based `functionUID(ns,name)`, so the `s3::write`
  `principal == resource.owner` comparison matches by name. `bucket-prefix-owner-exists`
  (`internal/controlplane/admission/bucket.go`) admits a Function **or** CatalogService owner.
- **The fix is proven, not asserted** — 4 cedar scenarios pass: a provider **reads** its bound prefix (bindings from
  the CatalogService), **writes** its owned prefix (`principal == owner`), is **denied** on an unbound/unowned prefix
  (default-deny holds — the fallback adds a *source*, never a blanket allow), and **Function-first** precedence. Plus
  the admission scenario (a CatalogService owner is admitted; a ghost name still rejected).
- **Default-deny preserved + no privileged bypass** — a provider with no `spec.blob` is inert; its S3 reach is exactly
  its declared bindings + owned prefix, the same binding-as-grant any function gets.
- **Extends, not supersedes, ADR-0080** — the Function model is untouched; the provider is a second, lower-precedence
  binding source. Zero new deps.

## Definition of Done
The ADR's 5-item Review checklist holds: Function-first CatalogService `blobBindings` ✓; owner admission accepts a
provider, ghost rejected ✓; policy + UID scheme untouched ✓; default-deny preserved ✓; one passing test per Scenario
✓. `CGO_ENABLED=0` clean, no new deps.

## Recommendation
**Pass.** Stamp ADR-0088 `Implemented`. This lifts the gate on F48's live data path — the next step is running the
`just lima-example-duckdb` lane to confirm the engine reaches Ready + both consumers query end-to-end.
