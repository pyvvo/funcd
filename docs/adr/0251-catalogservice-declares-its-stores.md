# ADR-0251: A standalone CatalogService declares at its root the buckets, ConfigMaps and Secrets its engine binds

- **Status**: Reviewing (2026-10-19), implemented; Accepted (2026-10-19), after one judge round, under `adr-batch` with acceptance delegated by the
  decider: the judge gave defined ConfigMaps the controller reference and corrected the supersession.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: api, catalog, stores, config, secrets
- **Realizes**: [FEAT-0001/F163](../feat/0001-feat-v1.1.md) (#1077, tracker #1072)
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0251` back-link:
  [ADR-0248](0248-stores-at-the-root.md) Decision 2's second exception (a standalone CatalogService's bindings named
  existing stores) and the matching line of its Open questions.
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) and
  [ADR-0087](0087-add-on-provider-runtime.md) (the CatalogService and its engine) ·
  [ADR-0213](0213-app-config-and-secret-declarations.md) (ConfigMap and Secret rules, reused) ·
  [ADR-0250](0250-workflow-declares-its-step-resources.md) (the same step for Workflows)

## Context & Need

After ADR-0248 a catalog declared inside a Function or an App uses that resource's root, but a standalone
CatalogService still binds buckets, ConfigMaps and Secrets that another file creates
(`internal/services/catalog/reconcile.go:392`, `resolveBucketRefs`). The decider kept that exception to keep ADR-0248
small and asked for this follow-up.

**Purpose**: a standalone CatalogService file holds its own stores, as every other kind's file does.

## Scenarios

- **scenario: catalog-owns-its-bucket** — *Given* CatalogService `lake` with `buckets: [{name: lakehouse, prefixes:
  [{name: lake}]}]` and `catalog: {bucket: lakehouse, prefix: lake}`, *Then* Bucket `lakehouse` carries `lake`'s
  marker, prefix `lake` is owned by `lake`, and the engine serves.
- **scenario: catalog-ref-bucket** — *Given* `buckets: [{ref: landing}]`, *Then* the engine reads `landing`, and
  deleting `lake` leaves it unchanged.
- **scenario: catalog-secret-check** — *Given* `secrets: [{name: quack, keys: [QUACK_TOKEN]}]`, *Then* `lake` waits
  `SecretNotFound`, then `SecretKeyMissing`, and starts its engine once the Secret holds the key.
- **scenario: catalog-config-hashed** — *Given* a defined ConfigMap `tuning`, *Then* `tuning-<hash>` exists and the
  engine reads it; *when* its data changes, *Then* the engine restarts on the new object.
- **scenario: catalog-binding-needs-root-entry** — *Given* a binding to bucket `ghost` that the root does not declare,
  *Then* apply refuses it, naming `spec.bindings.blob[0]`.
- **scenario: nested-catalog-has-no-root** — *Given* a catalog entry inside a Function or an App that carries its own
  root entries, *Then* apply refuses it: its declarer's root holds them.

## Scope

**In**: the three root keys of a standalone CatalogService, their ownership, hashing, Secret check and GC. **Out**: a
catalog entry inside a Function, a Workflow or an App, which keeps its declarer's root (ADR-0248 Decision 2,
ADR-0250).

## Constraints & Decision drivers

- One entry shape per kind (ADR-0248), one ownership rule (`internal/ownedstore`).
- The catalog's DuckLake prefix stays owned by the CatalogService itself (ADR-0086, single-writer).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. `buckets`, `configMaps` and `secrets` at the standalone root** | One rule for every kind | Catalog reconciler work | **Chosen** (the decider, #1077) |
| B. Keep the exception | No change | Two rules | Rejected (the decider) |

## Decision

1. **Root keys.** `CatalogServiceSpec` gains `buckets`, `configMaps` and `secrets`, with ADR-0248's shared entry
   types. On a standalone CatalogService, an omitted prefix `owner` is the CatalogService, written in by a new
   mutating `catalog-store-owner` admission, the twin of ADR-0248's `function-store-owner`, sharing its code. Cedar
   counts the catalog as a writer only on a prefix it binds.
2. **The root-entry rule.** Every binding of a standalone CatalogService (`blob`, `config`, `secret`) names an entry
   of its root; `binding-declared` refuses any other. A catalog entry nested in another resource may not carry root
   entries. ADR-0248 Decision 2's second exception ends.
3. **Ownership.** The catalog reconciler writes `name` entries through `ownedstore.Ensure`, with the CatalogService's
   marker, before `resolveBucketRefs`, and never rewrites `ref` entries. A store it does not own is `StoreNotOwned`.
4. **ConfigMaps** follow ADR-0213 Decisions 2, 3 and 5, as a Function's do (ADR-0248 Decision 5): stored as
   `<name>-<hash>` with the CatalogService's controller reference (ADR-0213 Decision 2), the binding repointed by
   `catalog-store-owner`, and the old object deleted once the engine runs on the new one.
5. **Secrets** follow ADR-0213 Decision 8: the reconciler checks each declared Secret and key before it starts the
   engine, and waits `SecretNotFound` or `SecretKeyMissing`.
6. **GC.** `gc.Pairs()` gains the CatalogService to its Buckets and ConfigMaps.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 — CatalogServiceSpec, used only on a standalone CatalogService.
Buckets            []BucketEntry       `json:"buckets,omitempty"`
ConfigMaps         []ConfigMapEntry    `json:"configMaps,omitempty"`
SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
```

| New | Kind | Where |
|---|---|---|
| `catalog-store-owner` (mutating) | CatalogService | `internal/controlplane/admission` |
| `StoreNotOwned`, `SecretNotFound`, `SecretKeyMissing` | CatalogService | `internal/services/catalog` |

## Implementation plan

Order: after ADR-0246 and ADR-0248's round.

- **funcd:** the types and `just generate`; the `catalog-store-owner` admission; the catalog reconciler
  (entries through `ownedstore`, the Secret check before the engine); `binding-declared` for CatalogServices and
  nested entries; `gc.Pairs()`. Tests: one per scenario, named after it (`TestScenarioCatalogOwnsItsBucket`, …), e2e
  in `pkg/funcd` with `t.Parallel()`.
- **The three repos:** each standalone CatalogService declares its stores (funcd-python's `catalog-quack` and
  `releve-lakehouse`; its Bucket and Secret files become root entries or `ref`s).
- **Docs:** the back-link on ADR-0248, the F163 row.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e`), `go mod verify` and every Lima
  lane, the duckdb lane included, pass.

## Review checklist

- [ ] The three keys use ADR-0248's entry types; `catalog-store-owner` writes the CatalogService as an omitted owner.
- [ ] Every binding is checked against the root; a nested catalog entry has no root entries.
- [ ] `name` entries carry the marker before `resolveBucketRefs`; `ref` entries are never rewritten.
- [ ] ConfigMaps are hashed, repointed and pruned; Secrets are checked before the engine starts; the GC pairs hold.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: a standalone catalog file is self-contained.
- **Negative**: a CatalogService that used an existing bucket must now declare it, by `ref`.
- **Risks accepted**: none.

## Open questions

None.

## References

- #1077, tracker #1072; ADR-0248's open questions.
- `internal/services/catalog/reconcile.go` (`engineEnv` at :351, `resolveBucketRefs` at :392);
  `api/types/v1alpha1/catalog*.go` (`CatalogServiceSpec`).
