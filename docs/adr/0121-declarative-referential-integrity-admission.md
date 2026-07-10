# ADR-0121: Declarative referential integrity — reconcile-time cross-resource existence

**Status**: Implemented (2026-07-10)
**Date**: 2026-07-10
**Deciders**: green-0-rabbit
**Acceptance note**: judged with no Blockers — the fail-closed single-writer claim was traced and confirmed in `internal/auth/cedar/capabilities.go` (a missing bucket ⇒ ownerless prefix ⇒ deny; a present bucket with an absent owner ⇒ owner UID no principal holds ⇒ deny until applied). Folded the Major (added ADR-0072/0073 to the refines list + References + feat row) and the Minors (softened the dangling-owner observability note; `catalog.go` deleted entirely; `nameExists` relocated to `kvstore.go:280`; blueprint sync flagged).
**Tags**: control-plane, admission, reconcile, declarative
**Realizes**: FEAT-0001/F86
**Relates to**: [ADR-0063](0063-admission-framework.md) (the admission framework this refines); refines the *admission-timing* of [ADR-0072](0072-kv-as-a-declarative-resource.md), [ADR-0073](0073-kv-bindings-and-subdomains.md), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md), [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), [ADR-0088](0088-add-on-provider-s3-identity.md), [ADR-0091](0091-function-catalog-consumer-binding.md) (not their resource contracts — no supersede)

## Context & Need

funcd's admission framework (ADR-0063) runs six **Validating** admissions that reject a write synchronously when a *cross-resource reference names a resource that does not exist yet*:

| Admission | On | Rejects when |
|---|---|---|
| `blob-binding-validity` | Function C/U | `spec.blob[].{bucket,prefix}` absent (ADR-0080) |
| `bucket-prefix-owner-exists` | Bucket C/U | `spec.prefixes[].owner` (a Function/CatalogService) absent (ADR-0080/0088) |
| `catalog-blob-validity` | CatalogService C/U | `spec.blob[]`/`spec.catalog` bucket+prefix absent (ADR-0086) |
| `catalog-binding-validity` | Function C/U | `spec.catalogs[].catalog` (a CatalogService) absent (ADR-0091) |
| `kv-binding-validity` | Function C/U | `spec.kv[]` KVStore/table absent (ADR-0072/0073) |
| `kv-owner-exists` | KVStore C/U | `spec.tables[].owner` (a Function) absent (ADR-0073) |

Because a `Bucket`'s prefix `owner` names a `CatalogService` **and** that `CatalogService`'s `spec.blob` names the `Bucket`, neither can be applied first — a **cycle**. The shipped workaround is a two-phase apply (a `bucket-base.yaml` with no owners → the CatalogService → a `bucket.yaml` that adds the owners as an Update), documented in `examples/python/catalog-quack` and `examples/python/releve-lakehouse`. This breaks declarative *apply-all-and-converge* (the Kubernetes/Helm model) and blocks a future App/Template packaging operator, which would otherwise have to bake the same sequencing in.

**Purpose of this decision**: make cross-resource *existence* an eventually-consistent, reconcile-time property instead of a write-time gate — admit a resource carrying a not-yet-resolvable reference, hold the *consumer* not-Ready with an observable Waiting condition, and converge when the referent appears. Order stops mattering; the two-phase workaround is deleted.

## Scenarios

- **scenario: consumer-before-referent** — Given no Bucket `data`, When a Function binding `spec.blob[lake→data/bronze]` is applied, Then the Apply **succeeds** (no admission reject) and the Function reports `Ready=False`, `Reason=BucketNotFound`, `Phase=Pending`; When the Bucket `data` is then applied, Then the Function converges to `Ready`.
- **scenario: catalog-cycle-any-order** — Given a `Bucket` whose `gold` prefix `owner` is `lake` and a `CatalogService lake` binding that Bucket, When both are applied **in either order in one batch** (no two-phase), Then both are admitted and `lake` converges to `Ready`; a write by `lake` into `gold` is authorized.
- **scenario: bucket-before-owner** — Given a Bucket whose prefix `owner` is a not-yet-applied Function `f`, When the Bucket is applied, Then it is admitted; a write attempt by any principal into that prefix is **Forbidden** until `f` exists; When `f` is applied, Then `f`'s writes into the prefix are authorized.
- **scenario: dangling-reference-is-observable** — Given a Function binding a Bucket that is never applied, Then the Function stays `Phase=Pending` with `Ready=False, Reason=BucketNotFound` (a typo surfaces as a durable, inspectable condition — not a silent success).
- **scenario: deletion-protection-unchanged** — Given a Bucket bound by a Function's `spec.blob`, When the Bucket is deleted, Then the delete is rejected `Conflict` (the delete-side guard is unaffected by this ADR).

## Scope

**In**: relocate the six *pure cross-resource existence* checks above from Validating admission to reconcile-time; the consumer reconcilers (Function, CatalogService) surface a Waiting condition + requeue on an unresolved referent; delete the six admissions and their unit tests; collapse the two-phase bucket workaround in the examples.

**Out**: (1) a **Bucket reconciler / `BucketStatus`** — Bucket stays a pure data-model resource (ADR-0080); a dangling `prefixes[].owner` needs no reconcile handling because write-authz is fail-closed on it (below). Correctness holds, but a *typo'd* owner is **not** separately surfaced (no owner resource exists to be not-Ready) — bucket/KVStore-level owner observability is a cheap follow-up (Open questions), not this ADR. (2) The **structural** `Validate()` checks (unique owner per prefix, exactly-one-of unions), the **quota** admissions, **all deletion-protection** admissions, and the `link-validity` **cycle-detection** — all stay in admission (none is a pure-existence gate). (3) Any change to the Cedar authorization model or the resource contracts of ADR-0080/0086/0088/0091.

## Constraints & Decision drivers

- **Fail-closed security invariant is preserved, not weakened** (hard constraint). The single-writer rule is `write ⟺ caller == prefix.owner` evaluated by the Cedar PEP at S3-request time against the owner's connection-scoped UID. Until the owner resource exists, **no principal holds that UID**, so the prefix is un-writable; until a bound bucket exists, the binding grants nothing. The removed admissions were *early-typo convenience*, never the security boundary — default-deny already covers the window.
- **Reuse existing machinery, invent nothing**: the `Condition`/`Phase` convention (`api/types/v1alpha1/status.go`), the `controller.Result{RequeueAfter}` requeue (`internal/controller/controller.go`), and the **existing precedent** — the Function reconciler already does accept-and-requeue-with-Waiting for catalog bindings (`internal/function/function.go:358-379`, `Reason=CatalogNotReady`). This ADR generalizes that one precedent to the blob/kv/catalog binding families.
- Library-first, single binary, no new deps.

## Alternatives considered

- **Keep synchronous admission, document apply order** (status quo). Rejected: it *is* the smell — non-declarative, forces the two-phase bucket and blocks apply-all; every new owner/binding relationship risks a new cycle.
- **An App/Template operator that sequences the applies.** Rejected as the *fix* (kept as a future feature): if admission still rejects synchronously, the operator must internally order/retry the members — it **moves** the workaround into the operator, it doesn't remove it. The declarative core must come first.
- **Add a Bucket reconciler + `BucketStatus` for full symmetry** (so a dangling `owner` shows a Waiting condition on the Bucket itself). Deferred: it adds a new controller + status surface for pure observability of a fail-closed state that is already correct and already observable via the owner's readiness. Out of altitude for this ADR; a clean follow-up.
- **Accept-and-requeue (chosen).** Existence becomes eventually-consistent: admit the dangling reference, consumer waits, converge on the referent's arrival. The Kubernetes model (a Pod may reference a not-yet-existent ConfigMap; it stays Pending).

## Decision

1. **Remove the six existence admissions** from the chain (`pkg/funcd/funcd.go`) and delete their implementations + unit tests: `blob-binding-validity`, `bucket-prefix-owner-exists` (`internal/controlplane/admission/bucket.go`), `catalog-blob-validity`, `catalog-binding-validity` (`.../catalog.go`), `kv-binding-validity`, `kv-owner-exists` (`.../kvstore.go`). The `StoreReader` port stays (other admissions use it).
2. **Consumer reconcilers surface Waiting on an unresolved referent** (the ADR-0088/0091 precedent, generalized): when the Function or CatalogService reconciler resolves a `spec.blob`/`spec.kv`/`spec.catalogs`/`spec.catalog` reference and the referent (or its prefix/table) does not exist, it sets `Conditions.Set(Ready=False, Reason=<kind>NotFound, Message=…)`, `Phase=Pending`, persists status, and returns `controller.Result{RequeueAfter: 2s}` — it does **not** deploy/serve until the reference resolves (fail-closed). `Reason` values: `BucketNotFound`, `KVStoreNotFound`, `CatalogNotFound` (existing `CatalogNotReady` for the *not-Ready* case is unchanged).
3. **Container-side owner references need no reconcile handling**: `Bucket.prefixes[].owner` and `KVStore.tables[].owner` referencing a not-yet-existent Function/CatalogService are simply admitted; the fail-closed write-authz (above) makes the prefix/table un-writable until the owner exists, then writable — no status, no requeue. (KVStore has a reconciler; it MAY later surface a Waiting condition for a dangling table owner, but that is not required and not in scope.)
4. **Unchanged in admission**: `validate` (structural `obj.Validate()`), `bucket-count`/`kvstore-count` quotas, `bucket-deletion-protection`/`kvstore-deletion-protection`/`link-deletion-protection`, and `link-validity` (its cycle-detection is graph-structural, not existence).
5. **Collapse the examples**: replace the two-phase `bucket-base.yaml`→`bucket.yaml` with a single `bucket.yaml` (owners inline) in `examples/python/catalog-quack`, `examples/python/releve-lakehouse`, and `examples/js/s3-roundtrip`; update their apply-order prose to "apply in any order."

## Temporary workarounds

- The two-phase `bucket-base.yaml`/`bucket.yaml` apply in the examples is the workaround this ADR removes. **Exit criterion**: this ADR reaching `Implemented` — the examples collapse to a single `bucket.yaml` in the same change.

## Contracts

**Admission chain (`pkg/funcd/funcd.go`)** — remove these six entries (constructors + registration); everything else in the slice is unchanged:

```go
// DELETED from the Admissions slice:
admission.NewBlobBindingValidityAdmission(storeReader{c.store})
admission.NewBucketPrefixOwnerExistsAdmission(storeReader{c.store})
admission.NewCatalogBlobValidityAdmission(storeReader{c.store})
admission.NewCatalogBindingValidityAdmission(storeReader{c.store})
admission.NewKVBindingValidityAdmission(storeReader{c.store})
admission.NewKVOwnerExistsAdmission(storeReader{c.store})
```

**Consumer reconcile behavior** — the resolution seam already returns a requeue signal; extend it to the blob/kv/catalog families. Reusing the existing types (no new API):

```go
// api/types/v1alpha1/status.go (UNCHANGED — reused):
type Condition struct {
    Type ConditionType; Status ConditionStatus; ObservedGeneration int64
    LastTransitionTime time.Time; Reason string; Message string
}
func (c *Conditions) Set(cond Condition) // upsert-by-Type

// internal/controller/controller.go (UNCHANGED — reused):
type Result struct { Requeue bool; RequeueAfter time.Duration }

// The reconcile pattern each consumer applies on an unresolved reference (generalizing
// internal/function/function.go:358-379):
//   fn.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse,
//       Reason: "BucketNotFound", Message: `spec.blob "lake" → bucket "data" not found; waiting`})
//   fn.Status.Phase = v1.PhasePending
//   _, _ = r.store.Update(ctx, fn)
//   return controller.Result{RequeueAfter: 2 * time.Second}, nil
```

**Resource YAML** — after this ADR, a single Bucket with owners applies in any order:

```yaml
apiVersion: funcd.io/v1alpha1
kind: Bucket
metadata: { name: lakehouse, namespace: default, resourceGroup: rg1 }
spec:
  prefixes:
    - { name: gold, owner: lake }   # `lake` (a CatalogService) may not exist yet — admitted; wired on arrival
```

**Dependencies & I/O**: consumes the store (`StoreReader.List`) at reconcile (already used); produces resource `Status.Conditions` + `Phase` writes; no config keys, events, files, or deps added or removed.

## Implementation plan

**Files**:
- `pkg/funcd/funcd.go` — remove the six `admission.New*` entries from the `Admissions` slice.
- `internal/controlplane/admission/bucket.go` — delete `blobBindingValidity` + `bucketPrefixOwnerExists` (types + constructors). Keep `bucketQuota`, `bucketDeletionProtection`, `BlobProber`, `bucketPrefix`.
- `internal/controlplane/admission/catalog.go` — **delete the file entirely** (it holds only `catalogBlobValidity` + `catalogBindingValidity`; an emptied file would leave unused imports → build/lint failure).
- `internal/controlplane/admission/kvstore.go` — delete `kvBindingValidity` + `kvOwnerExists` **and the now-unused `nameExists` helper (defined here, `:280`, used only by the two deleted owner-exists admissions)**; keep the quota + deletion-protection.
- `blueprint.md` — sync the admission mentions at acceptance (the KV line "admissions enforce … binding-validity, owner-exists" ~L102, and the ADR-0080/0086 S3 admission mentions) so they read as reconcile-time existence, not write-time gates.
- `internal/controlplane/admission/{bucket,catalog,kvstore}_test.go` — delete the removed admissions' unit tests.
- `internal/function/function.go` — extend the binding-resolution path: on a missing bound Bucket/prefix (`spec.blob`) or KVStore/table (`spec.kv`) or CatalogService (`spec.catalogs`), set `Ready=False` + `Reason=BucketNotFound`/`KVStoreNotFound`/`CatalogNotFound` + `Phase=Pending` + `RequeueAfter`, mirroring the existing `CatalogNotReady` block.
- `internal/services/catalog/reconcile.go` — same for `spec.blob`/`spec.catalog` → `Reason=BucketNotFound`.
- `examples/python/catalog-quack`, `examples/python/releve-lakehouse`, `examples/js/s3-roundtrip` — collapse `bucket-base.yaml`+`bucket.yaml` → one `bucket.yaml`; update READMEs/apply-order + `scripts/lanes.yaml` apply lists.

**Test plan**:
- *Contract/unit*: the removed admissions' tests are deleted; add reconcile tests (`internal/function/function_test.go`, `internal/services/catalog/reconcile_test.go`) — a consumer with a missing referent sets `Ready=False`/`Phase=Pending`/the right `Reason` and requeues, then goes `Ready` once the referent is stored.
- *scenario: consumer-before-referent* → `pkg/funcd` Go e2e: `funcd.New(funcd.InMemory())`, Apply a Function binding a not-yet-applied Bucket, assert Apply succeeds + `phaseOf == Pending` + the condition; Apply the Bucket; `require.Eventually(phaseOf == Ready)`.
- *scenario: catalog-cycle-any-order* → Go e2e: Apply `Bucket(owner=lake)` + `CatalogService lake` in the "wrong" order in one batch; assert both admitted and converge to Ready.
- *scenario: bucket-before-owner* + *dangling-reference-is-observable* + *deletion-protection-unchanged* → Go e2e assertions (Forbidden-then-authorized; durable Pending; delete still Conflict).
- *Venom lane* `apply-any-order` (`scripts/lanes.yaml` + `e2e/apply-any-order.venom.yml`) on real containerd: apply Bucket+CatalogService+Function deliberately out of order, gate `ready` on all reaching Ready, assert an invocation/write works. Needs colima up (CLAUDE.md pitfall #3); if the daemon env is unavailable, ship the Go e2e and record the Venom lane as authored-but-deferred per the roadmap test-sequencing note.

**Definition of done**: the six admissions are gone from the chain; `go build ./... · go tool golangci-lint run ./... · go test ./... · go mod verify` all green; apply-out-of-order Apply succeeds and converges; a dangling reference is a durable Waiting condition; deletion-protection still Conflicts; a write before the owner exists is Forbidden then authorized; examples collapsed to a single `bucket.yaml`; Go e2e green + Venom lane green (or recorded deferral).

## Review checklist

- [ ] The six existence admissions are removed from the `Admissions` slice and their code + unit tests deleted; quotas, deletion-protection, `link-validity` cycle-detection, and `validate` remain.
- [ ] Applying a Function/CatalogService whose reference is absent **succeeds** (no admission reject) and yields `Ready=False` + the correct `Reason` + `Phase=Pending`.
- [ ] The referent's later Apply drives the consumer to `Ready` (converges, bounded requeue).
- [ ] `Bucket(owner=lake)` + `CatalogService lake` apply in any order and both reach Ready; the two-phase files are gone from the examples.
- [ ] A write into a prefix whose owner does not exist is `Forbidden`; once the owner exists it is authorized (fail-closed preserved).
- [ ] `bucket-deletion-protection` still rejects deleting a bound/non-empty bucket with `Conflict`.
- [ ] Four sub-checks green; Go e2e passes; Venom `apply-any-order` lane passes or is recorded-deferred.

## Consequences

- **Positive**: apply-all-and-converge in any order (declarative parity with Helm/K8s); the two-phase bucket workaround is deleted; unblocks a future App/Template lifecycle operator; one coherent rule replaces six synchronous gates.
- **Negative / accepted**: a typo'd reference is now a durable `Pending`/Waiting state rather than an instant reject — mitigated by a clear `Reason`/`Message` (observable, the K8s trade). Slightly more reconcile churn (bounded 2s requeue while a reference is unresolved).
- **Risks accepted**: consumer reconcilers must tolerate an absent referent without compiling a broken grant — covered by the fail-closed authz model and the added not-found handling; verified by the Go e2e.

## Open questions

- A **Bucket/KVStore-level Waiting condition** for a dangling `owner` (a Bucket reconciler + `BucketStatus`) — deferred; answered by a follow-up ADR only if bucket-level observability is wanted (correctness + owner-readiness observability already hold).

## References

- [ADR-0063](0063-admission-framework.md) — the admission framework refined here.
- [ADR-0072](0072-kv-as-a-declarative-resource.md), [ADR-0073](0073-kv-bindings-and-subdomains.md), [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md), [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), [ADR-0088](0088-add-on-provider-s3-identity.md), [ADR-0091](0091-function-catalog-consumer-binding.md) — the admission-timing of their existence checks is refined (contracts unchanged).
- `api/types/v1alpha1/status.go` (Condition/Phase), `internal/controller/controller.go` (Result), `internal/function/function.go:358-379` (the accept-and-requeue precedent).
- Kubernetes eventually-consistent references (a Pod Pending on a missing ConfigMap) — the prior art for reconcile-time existence.
