# ADR-0135: Managed identity — a first-class principal + issued credential

- **Status**: Implemented
- **Date**: 2026-07-14 (**Implemented 2026-07-14** — review pass (claude-opus-4-8): the `Identity` CRD +
  credential-issuing reconciler (`internal/services/identity`, issue/rotate/revoke over an owned Secret) +
  store-backed `ExternalKeys` (wired into `s3gateway.Deps.External`, previously nil) + the `FUNCID` access
  key + the `Identity` Cedar principal (`principalUID`/`identityUID`/schema vocab) + `principalFor` mapping
  + CRUD land. All 7 scenarios are named passing tests (issue/rotate/revoke, external-resolves, default-deny,
  access-key-deterministic, validate). Green: `go build ./...` · `golangci-lint` (0 issues) · targeted
  `go test` · `go mod verify`; OpenAPI spec regenerated. `principalMeta`/a PrincipalSource proved
  unnecessary (an Identity carries no binding sets — simpler than the plan). No new dependency.
  **Accepted 2026-07-14** via /adr-batch self-accept — judge pass, no open
  Blockers/Majors, no default-allow (default-deny preserved: an unassigned Identity grants nothing).
  Folded 2 Minors: the per-request `store.Get` on the external S3 auth path (perf note + cache follow-on),
  and owned-Secret-only semantics (no foreign-Secret hijack). **adr-judge gate** then folded **1 Major**:
  the access-key prefix was mis-stated (`"FUNCK"` → real Function prefix is `"FUNCD"`); the Identity uses a
  distinct `"FUNCID"` prefix so it falls through the Function `decodeAccess` to `external.Lookup` (no
  Function-path edit). Grounded in the 2026-07-14 feasibility spike.)
- **Deciders**: green-0-rabbit
- **Tags**: iam, identity, authz, cedar, s3, credential, F100
- **Realizes**: [FEAT-0008/F100](../feat/0008-feat-iam.md) (managed identity — a first-class principal
  generalizing the Function-only owner/grantee, with an issued credential for external callers)
- **Relates to / refines**:
  [ADR-0085](0085-s3-gateway-request-signing-keypair.md) — the deterministic keypair derivation this
  mirrors for an Identity + the `ExternalKeys` seam it populates;
  [ADR-0088](0088-add-on-provider-s3-identity.md) — the `S3Identity` Cedar principal an external Identity
  resolves to; [ADR-0116](0116-capability-authorization-framework.md) — the principal/capability registry
  this adds a principal *kind* to; [ADR-0074](0074-cedar-authorization-resource-access.md) — the Cedar PDP
  (default-deny) an Identity is evaluated by; [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md)
  — the dev-only relaxation this is the prod-safe replacement for. Fills the reserved `Grant` IAM
  placeholder's principal side ([api/types/v1alpha1/grant.go](../../api/types/v1alpha1/grant.go)).

## Context & Need

funcd's owner/grantee principal is **hard-typed to `Function`**. `BucketPrefix.owner` materializes only as
`Function::"<ns>/<name>"` ([internal/auth/cedar/capabilities.go](../../internal/auth/cedar/capabilities.go)),
so an external caller — who authenticates via SigV4 and resolves to `S3Identity::"<ns>/<key>"`
([s3gateway `principalFor`](../../internal/blob/s3gateway/auth.go)) — can never be an owner or grantee.
Worse, the external path is **wired but dead**: the S3 gateway's `ExternalKeys` store (`Deps.External`) is
`nil` in production ([pkg/funcd/funcd.go](../../pkg/funcd/funcd.go)) — there is **no registrar, no
lifecycle, no way to issue an external credential**. The only stopgap is a dev-only relaxation
([ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md)) that drops the single-writer
forbid locally.

This ADR introduces the **`Identity`** resource: a declarative, namespace-scoped **user-assigned managed
identity** for a non-funcd caller. It **issues a credential** (an S3 SigV4 keypair) into an owned `Secret`
and **populates the `ExternalKeys` store in production**, so an external service can authenticate and
resolve to a first-class Cedar principal. It adds the principal *kind* to the PDP so `Identity::"<ns>/<name>"`
is a legal principal. It does **not** grant anything — an Identity with no assignment is default-deny; the
grant model (roles/assignments + the generalized single-writer forbid) is [ADR-0136](0136-roles-and-role-assignments.md),
which builds on this. The **system-assigned** case (a `Function`'s own identity, which dies with it) is the
existing implicit behavior and is unchanged; `Identity` is the **user-assigned** counterpart.

Purpose, plainly: give funcd a nameable, credential-bearing principal for external callers, so access can
later be granted to *someone who is not a Function* — the exact gap the releve-lakehouse `landing` drop
surfaced.

## Scenarios

- **scenario: identity-issues-credential** — *Given* an `Identity` `releve-dropper` in namespace `data`,
  *when* the controller reconciles it, *then* it creates an **owned `Secret`** holding a SigV4
  `accessKeyId`/`secretAccessKey`, registers the access key in the gateway's `ExternalKeys` store, and
  sets `status.phase = Ready` with the issued `accessKeyId` observable in status.
- **scenario: external-caller-resolves-to-identity** — *Given* an issued Identity credential, *when* an
  external client makes a SigV4-signed S3 request with it, *then* `principalFor` resolves the caller to
  `Identity::"data/releve-dropper"` (a first-class Cedar principal), not `Forbidden`.
- **scenario: identity-default-deny** — *Given* an `Identity` with **no** role assignment, *when* it
  attempts any blob read or write, *then* the PDP **denies** it (issuing a credential grants
  authentication, never authorization — default-deny preserved).
- **scenario: identity-credential-rotated** — *Given* a Ready `Identity`, *when* its `spec.rotate`
  generation is bumped (or the Secret is deleted), *then* the controller issues a **new** secret into the
  owned Secret and the old secret stops authenticating — the credential is a revocable/rotatable
  relationship, not a permanent attribute.
- **scenario: identity-deleted-revokes** — *Given* a Ready `Identity`, *when* it is deleted, *then* its
  owned `Secret` cascades (owner reference) and the access key is **deregistered** from `ExternalKeys`, so
  the credential no longer authenticates.
- **scenario: system-assigned-unchanged** — *Given* a `Function` (its own system-assigned identity), *when*
  it accesses a bound resource, *then* behavior is unchanged — this ADR adds the user-assigned kind only.

## Scope

- **In**: the `Identity` CRD (user-assigned/external) + `Status`; the credential-issuing **reconciler**
  (keypair → owned `Secret` → register in `ExternalKeys`); a **production `ExternalKeys` store** wired into
  the S3 gateway (`Deps.External`); the `Identity` **Cedar principal kind** (`principalUID`/`principalMeta`
  cases + `entityTypeIdentity` schema vocab so `principal == Identity::"…"` is legal in policies); CRUD
  handlers + Kind registration.
- **Out**: **roles / role assignments / the generalized single-writer forbid** — that is
  [ADR-0136](0136-roles-and-role-assignments.md) (an Identity does nothing useful until it can be granted a
  role; this ADR ships the principal + credential, default-deny). **Per-caller identity on the catalog
  serving (Quack) layer** — greenfield, a follow-on. **Assume-role / trust / vended short-lived
  credentials** — funcd identities are bound. **Federated (OIDC) identity**. **Non-S3 credential kinds**
  (only SigV4 keypair here; a catalog token is future).

## Constraints & Decision drivers

- **Default-deny preserved** — issuing a credential is authentication, never authorization. An Identity
  with no assignment must grant nothing (blueprint security model; ADR-0074).
- **Reuse the S3 identity path** — `principalFor` already resolves an external key to a principal; the only
  prod gap is that `ExternalKeys` is unpopulated. Populate it; do not build a parallel auth path.
- **Stable identity, rotatable secret** — the access key is a stable, deterministic function of
  `(ns, name)` (mirroring [ADR-0085](0085-s3-gateway-request-signing-keypair.md)'s Function derivation, so
  lookup needs no index and survives restart); the **secret** is randomly generated and **stored** so it
  can be rotated/revoked (the credential-hygiene constraint from the feasibility analysis).
- **Owned Secret, cascade on delete** — the issued Secret carries an `OwnerReference` to the Identity so it
  is garbage-collected with it (revocation).
- **No new dependency** — `crypto/rand`, the existing store/reconciler/Secret machinery, the S3 gateway
  seam, `cedar-go`.

## Alternatives considered

- **Fully deterministic keypair (derive both access AND secret, like Functions).** *Rejected*: a derived
  secret can't be rotated or revoked without changing the name; the feasibility analysis flagged the
  long-lived-secret hole. Deriving only the *access key* (stable identity) and generating+storing the
  *secret* keeps lookup index-free while making the secret rotatable — the best of both.
- **An in-memory `ExternalKeys` registry populated by the reconciler.** *Rejected*: not the source of
  truth, lost on restart, races with reconcile. A store-backed lookup (decode access → read the Identity's
  Secret) is authoritative and restart-safe.
- **Fold Identity into the existing `S3Identity` pseudo-kind.** *Rejected*: `KindS3Identity` is an
  auth-only, non-CRUD type ([metadata.go](../../api/types/v1alpha1/metadata.go)) with no lifecycle. An
  Identity needs a real reconciled resource (issue/rotate/revoke). Identity is the CRUD resource that
  *resolves to* an S3Identity principal on the wire.
- **Grant credentials to `Function`s here too.** *Rejected as scope*: Functions already have a
  system-assigned identity + derived keypair (ADR-0085); this ADR is the *user-assigned* gap only.

## Decision

Add a namespaced **`Identity`** resource — a user-assigned managed identity — and issue it a credential.

**Resource** (`api/types/v1alpha1/identity.go`):

```go
type Identity struct {
    TypeMeta   `json:",inline"`
    ObjectMeta `json:"metadata"`
    Spec       IdentitySpec   `json:"spec"`
    Status     IdentityStatus `json:"status,omitempty"`
}

type IdentitySpec struct {
    // Type is the identity flavor. V1: only "external" (a non-funcd SigV4 caller). Required.
    Type IdentityType `json:"type" enum:"external"`
    // CredentialSecretName is the owned Secret the issued keypair is written to. Defaults to the
    // Identity's own name when empty.
    CredentialSecretName ObjectName `json:"credentialSecretName,omitempty"`
    // Rotate, when incremented, forces the controller to reissue the secret (rotation). Optional.
    Rotate int64 `json:"rotate,omitempty"`
}

type IdentityStatus struct {
    Phase       Phase       `json:"phase,omitempty"`
    Conditions  Conditions  `json:"conditions,omitempty"`
    // AccessKeyID is the issued, stable SigV4 access key id (secret lives only in the Secret).
    AccessKeyID string      `json:"accessKeyId,omitempty"`
    // ObservedRotate mirrors spec.rotate the issued secret corresponds to.
    ObservedRotate int64    `json:"observedRotate,omitempty"`
}
```

`Validate()` first line is `validateMeta(...)`; structural rules: `Type` must be `external`.

**Credential issuance (the reconciler, `internal/services/identity`):** on reconcile of `Identity` id:
1. `store.Get`; a `fault.NotFound` (deleted) → deregister the access key from the `ExternalKeys` store and
   return (the owned Secret cascades via its OwnerReference).
2. Compute the **stable access key** `IdentityAccessKey(ns, name)` = `"FUNCID" + base32NoPad(ns "\x00"
   name)` — deterministic, and prefixed so it does **not** match ADR-0085's Function prefix `"FUNCD"`
   ([iam.go](../../internal/blob/s3gateway/iam.go) `accessKeyPrefix`). The Function `decodeAccess` therefore
   returns `ok=false` for it, so `iam.GetUserAccount`/`principalFor` **fall through to `external.Lookup`**
   (the existing external path) — no edit to the Function `decodeAccess`; `storeExternalKeys` owns the
   `"FUNCID"` decode back to `(ns, name)`.
3. Ensure the owned **`Secret`** idempotently (mirroring the workflow materializer's `ensure*`): if absent
   or `spec.rotate` advanced past `status.observedRotate`, generate a fresh random secret
   (`crypto/rand`, base64), write `Secret.Spec.Data = {accessKeyId, secretAccessKey}` with an
   `OwnerReference{Controller:true, BlockOwnerDeletion:true}` back to the Identity, `store.Create`/`Update`.
4. **Register** `access → (secret, namespace)` in the `ExternalKeys` store.
5. Write `status.phase=Ready`, `status.accessKeyId`, `status.observedRotate`, guarded by `retryOnConflict`.

**Production `ExternalKeys` store** (`internal/blob/s3gateway` or a small new store-backed impl): implements
`Lookup(access) (secret, namespace string, ok bool)` by decoding a `"FUNCI…"` access key → `(ns, name)`,
`store.Get` the Identity (must be `Ready`), and reading its owned Secret's `secretAccessKey`. Wired at the
compose root into `s3gateway.Deps.External` (today `nil`). A `"FUNCK…"` key still routes to the existing
Function path; an unknown/deleted key → `ok=false` → `principalFor` returns `Forbidden` (unchanged).

**Cedar principal kind:** register `Identity` as a principal type — `entityTypeIdentity` added to a
capability's `SupportingEntityTypes` (schema vocab), a `case v1.KindIdentity` in `principalUID`
(→ `Identity::"<ns>/<name>"`) and in `principalMeta`, and `identityUID(ns,name)`. `principalFor` maps an
external `Identity`-issued key to `EntityRef{Type: KindIdentity, …}`. **No grant is added** — with no
role-assignment permit and no writer-set membership, every Identity request is default-denied by the
existing policies. (The grant path is ADR-0136.)

**Kind registration:** `KindIdentity` const + `Kind.Validate`/`NewObject`/`AllKinds`; namespaced; CRUD
handlers (clone the CatalogService block); `Identity` implements `StatusObject`.

## Temporary workarounds

None. The dev-only `WithDevS3RelaxedWrites` ([ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md))
is a **separate `funcdctl dev` fidelity choice** (the same class as dev's no-sandbox / no-egress-isolation)
and is **out of scope here** — this ADR neither depends on it nor manages its future. IAM is the prod
authorization model; whether `funcdctl dev` keeps the relaxation (it does not eliminate dev's need for it —
dev auto-infers owners from bindings and cannot tell producer from consumer from external-seeder) is a
dev-focused decision for a later ADR, not this one. The two are not mixed.

## Contracts

**Reconciler** (satisfies `controller.Reconciler`):

```go
// Reconcile issues/rotates/revokes an Identity's credential. Idempotent, at-least-once (ADR-0015).
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
```

**Credential derivation** (pure, `internal/blob/s3gateway` alongside `DeriveKeypair`):

```go
// IdentityAccessKey returns the stable SigV4 access key id for a user-assigned Identity (deterministic
// in ns+name; "FUNCID" prefix does NOT match a Function's "FUNCD" prefix, so the Function decodeAccess
// skips it and the gateway falls through to external.Lookup). The SECRET is not derived here — it is
// generated + stored (rotatable). storeExternalKeys.Lookup decodes this back to (ns, name).
func IdentityAccessKey(ns v1.NamespaceName, name v1.ObjectName) string
```

**ExternalKeys store** (implements the existing `s3gateway.ExternalKeys` interface):

```go
func (s *storeExternalKeys) Lookup(access string) (secret, namespace string, ok bool)
```

**Dependencies & I/O**

| Consumes | Produces |
|---|---|
| `Identity` resources (store watch); their owned `Secret`s; `crypto/rand` | an owned `Secret` per Identity; `ExternalKeys` registrations; `Identity.status` |
| the S3 gateway `principalFor`/`GetUserAccount` seam (`Deps.External`) | a resolvable `Identity::"<ns>/<name>"` Cedar principal |

No new dependency; no wire-format change to existing resources.

## Implementation plan

1. `api/types/v1alpha1/identity.go` — the type + `IdentityType` enum + `Validate` + `GetStatus`. Register
   `KindIdentity` in `metadata.go` (const, `Validate` switch, `NewObject`, `AllKinds`). Bump the kind-count
   / namespaced / statusKinds assertions in `metadata_test.go`, `types_test.go`, `status_test.go`.
2. `internal/blob/s3gateway/iam.go` — `IdentityAccessKey` (`"FUNCID"` prefix, does not match the Function
   `"FUNCD"` prefix, so it falls through `decodeAccess` → `external.Lookup` — no Function-path edit). A
   store-backed `ExternalKeys` impl (`storeExternalKeys`) that decodes the `"FUNCID"` key → `(ns, name)`,
   `store.Get`s the (Ready) Identity, and reads its owned Secret's `secretAccessKey`.
3. `internal/services/identity/reconcile.go` — the credential-issuing reconciler (clone the *shape* of
   `internal/services/catalog/reconcile.go` + the workflow materializer's owned-`ensure` + `ownerRef`).
4. `internal/auth/cedar` — `entityTypeIdentity` in `S3Capability().SupportingEntityTypes`; `principalUID`
   + `principalMeta` cases for `KindIdentity`; `identityUID`.
5. `internal/blob/s3gateway/auth.go` — map an external `Identity`-issued key to
   `EntityRef{Type: KindIdentity}` (via the store lookup returning the identity name).
6. `internal/controlplane` — `Identity` CRUD handlers + route registration (clone CatalogService).
7. `pkg/funcd/funcd.go` — construct + `ctrl.Register(KindIdentity.GVK(), identityReconciler)`; wire
   `storeExternalKeys` into `s3gateway.Deps.External`.
8. **Test plan** (hermetic; no e2e):
   - `TestScenarioIdentityIssuesCredential` — reconcile against a fake store → owned Secret created with
     `{accessKeyId, secretAccessKey}` + OwnerReference; `ExternalKeys` registered; status Ready.
   - `TestScenarioExternalCallerResolvesToIdentity` — `principalFor` with an issued `"FUNCI…"` key +
     populated `ExternalKeys` → `EntityRef{KindIdentity, ns, name}`.
   - `TestScenarioIdentityDefaultDeny` — Cedar `Authorize` for an `Identity` principal, `s3::read` and
     `s3::write`, no assignment → `Deny` (both).
   - `TestScenarioIdentityCredentialRotated` — bump `spec.rotate` → new secret in the Secret;
     `observedRotate` advanced.
   - `TestScenarioIdentityDeletedRevokes` — reconcile a deleted Identity → `ExternalKeys.Lookup` → `ok=false`.
   - `TestIdentityValidate` — `type` must be `external`; meta rules.
   - `TestIdentityAccessKeyDeterministic` — stable + distinct-prefix from a Function key; decodes back.
9. Verify: `go build ./...` · `go tool golangci-lint run ./...` · `go test ./...` · `go mod verify`.

**Definition of done**: every scenario a named passing test; an Identity reconciles to an owned Secret +
`ExternalKeys` registration + Ready status; an issued key resolves to an `Identity` principal; an
unassigned Identity is default-denied; delete revokes; four sub-checks green.

## Review checklist

- [ ] `Identity` is a namespaced `StatusObject`; `KindIdentity` fully registered; type tests updated.
- [ ] Reconciler issues an **owned** Secret (OwnerReference → cascade) with `{accessKeyId, secretAccessKey}`
      and registers the key; NotFound path deregisters.
- [ ] Access key is **deterministic** (`FUNCI`-prefixed, decodes to ns+name); secret is **generated +
      stored** (rotatable via `spec.rotate`).
- [ ] `ExternalKeys` is store-backed + wired into `s3gateway.Deps.External` (no longer nil in prod); a
      `FUNCK…` key still routes to the Function path; unknown → Forbidden.
- [ ] `Identity` is a legal Cedar principal (`principalUID`/`principalMeta`/schema vocab) and **grants
      nothing** — an unassigned Identity is default-denied (asserted).
- [ ] No new dependency; imports at top level; no identity/path leak; block-style YAML in any doc/CRD.
- [ ] Every scenario has a named, un-skipped, passing test; four sub-checks green.

## Consequences

- **Positive**: an external service is a first-class, credential-bearing, revocable principal; the dead
  `ExternalKeys` prod seam is populated; the owner/grantee side is no longer Function-only (unblocks
  ADR-0136); default-deny intact; no new dependency.
- **Negative / risks**: a stored secret is long-lived until rotated (mitigated by `spec.rotate` +
  delete-to-revoke; short-lived vended creds are a deferred V2). The credential-issuing reconciler is
  net-new logic (no drop-in prior art) — covered by hermetic tests. The store-backed `ExternalKeys.Lookup`
  does a **`store.Get` per external S3 request** on the auth path (the Function path re-derives with no
  store hit) — acceptable for V1 external traffic volume against the fast Badger metastore; a small cache
  is a documented follow-on, not needed now.
- **Accepted**: only the S3 SigV4 credential kind is issued here (catalog token deferred); system-assigned
  Functions are unchanged. The reconciler manages **only the Secret it owns** (created with the
  Identity's OwnerReference); it never adopts or overwrites a pre-existing foreign Secret of the same name
  — a name collision with an unowned Secret surfaces as a create conflict the Identity reports in status,
  not a silent hijack.

## Open questions

- **Catalog serving per-caller identity** — a follow-on ADR (greenfield: no principal/PEP on the Quack
  path today).
- **Short-lived / vended credentials** (assume-role style) — deferred to a multi-tenant V2 story.

## References

- [ADR-0085](0085-s3-gateway-request-signing-keypair.md), [ADR-0088](0088-add-on-provider-s3-identity.md),
  [ADR-0116](0116-capability-authorization-framework.md), [ADR-0074](0074-cedar-authorization-resource-access.md),
  [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md)
- Feasibility spike (2026-07-14): generalized single-writer + Identity principal proven on cedar-go.
- Project #4 card: First-class Identity / typed principal ref (PVTI_lAHOBMTWh84BbERrzgyit0g)
