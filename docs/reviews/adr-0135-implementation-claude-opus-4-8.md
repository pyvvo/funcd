# ADR-0135 Implementation Review — Managed identity

**Verdict**: **pass** — the `Identity` principal + credential issuance conform to the Contracts; all 7
scenarios are named passing tests; default-deny preserved; build/lint/test/mod green; no new dependency.
**Producing model**: claude-opus-4-8.
**Reviewed against**: ADR-0135 Contracts / Scenarios / Review checklist · blueprint security model
(default-deny) · ADR-0116 (capability registry) · ADR-0085 (keypair path).

## Verification (evidence)

- `go build ./...` → exit 0.
- `go tool golangci-lint run` over all changed packages → **0 issues**.
- `go test ./internal/services/identity/... ./internal/blob/s3gateway/... ./internal/auth/cedar/...
  ./api/types/... ./internal/controlplane/...` → `ok` (all scenario tests + the OpenAPI spec gate pass).
- `go mod verify` → all modules verified. OpenAPI spec regenerated (`just generate`, `identities` routes present).
- Identity/path grep over all changed files → clean.

## Conformance to the Contracts

- **`Identity` CRD** — namespaced `StatusObject`; `KindIdentity` registered (const, Validate switch,
  NewObject, AllKinds); the count/enumeration tests updated. `Validate` enforces `type == external`.
- **Credential reconciler** (`internal/services/identity/reconcile.go`) — issues a stable `FUNCID` access
  key + a random (`crypto/rand`) secret into an **owned** Secret (`OwnerReference{Controller:true}` →
  cascade); rotates on `spec.rotate`; a deleted Identity needs no teardown (Secret cascades, lookup
  fails). Status Ready + AccessKeyID + ObservedRotate.
- **`storeExternalKeys`** (`externalkeys.go`) — decodes a `FUNCID` key → Identity → owned Secret's
  `secretAccessKey`; wired into `s3gateway.Deps.External` (the previously-nil prod seam) at the compose root.
- **Access key** — `IdentityAccessKey` = `FUNCID` + base32(ns\x00name); does not match the Function
  `FUNCD` prefix, so `decodeAccess` skips it and the gateway falls through to `external.Lookup` (the folded
  judge fix). `DecodeIdentityAccess` round-trips.
- **Cedar principal** — `entityTypeIdentity` in the schema vocab (S3Capability SupportingEntityTypes),
  `principalUID` case → `identityUID` (`Identity::"<ns>/<name>"`). `principalFor` maps a `FUNCID` key to the
  `Identity` principal; a Function key is unchanged.
- **Default-deny** — an unassigned Identity is denied s3::read AND s3::write (asserted in cedar).

## Scenario → test map (all passing)

| Scenario | Test |
|---|---|
| identity-issues-credential | `TestScenarioIdentityIssuesCredential` |
| identity-credential-rotated | `TestScenarioIdentityCredentialRotated` |
| identity-deleted-revokes | `TestScenarioIdentityDeletedRevokes` |
| external-caller-resolves-to-identity | `TestPrincipalForMapsIdentityKey` |
| identity-default-deny | `TestScenarioIdentityDefaultDeny` |
| (access-key deterministic/distinct) | `TestIdentityAccessKeyDeterministicAndDistinct` |
| identity-validate | `TestIdentityValidate` |

## ✅ Verified correct — keep

- The `FUNCID`-falls-through-to-external design (no edit to the Function `decodeAccess`) — clean and the
  folded judge fix; keep.
- The simplification (no `principalMeta`/PrincipalSource for Identity — it carries no binding sets) is
  correct: grants come from ADR-0136's compiled permits, so the bare principal + default-deny is right.
- Owned-Secret cascade + store-backed lookup → delete-to-revoke with no in-memory registry to desync.

## Findings

### Blockers / Major
None.

### Minor
- `Identity` reads on the external S3 auth path do a `store.Get` per request (already noted in the ADR
  Consequences as an accepted V1 trade-off; a cache is a documented follow-on). No change needed.

## Recommendation

Stamp **Implemented**. Next: ADR-0136 (the roles/assignments that make an Identity grantable) — the build
dependency is satisfied.
