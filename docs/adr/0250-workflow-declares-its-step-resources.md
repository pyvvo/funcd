# ADR-0250: A Workflow declares at its root the databases, catalogs, ConfigMaps and Secrets its steps bind

- **Status**: Reviewing (2026-10-19), implemented; Accepted (2026-10-19), after one judge round, under `adr-batch` with acceptance delegated by the
  decider: the judge gave defined ConfigMaps and catalogs the controller reference and settled prefix owners.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: api, workflow, stores, config, secrets
- **Realizes**: [FEAT-0001/F162](../feat/0001-feat-v1.1.md) (#1076, tracker #1072)
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0250` back-link:
  [ADR-0248](0248-stores-at-the-root.md) Decision 2's first exception (a step's `sql`, `catalog`, `config` and
  `secret` bindings named existing resources) and the Workflow row of Decision 1.
- **Relates to**: [ADR-0213](0213-app-config-and-secret-declarations.md) (its ConfigMap and Secret rules, reused) ·
  [ADR-0094](0094-workflow-engine-core.md) (the materializer) · [ADR-0246](0246-bindings-under-spec.md)
  (`StepBindings`)

## Context & Need

After ADR-0248 a Workflow declares only `kv` and `buckets` at its root. Its steps still bind databases, catalogs,
ConfigMaps and Secrets that some other file creates, so the root-entry rule has an exception for steps. The decider
kept that exception to keep ADR-0248 small and asked for this follow-up.

**Purpose**: a Workflow file holds everything its steps use, as a Function file and an App file do.

## Scenarios

- **scenario: workflow-owns-database** — *Given* Workflow `orders` with `sql: [{name: orders-db, owner: ingest}]` and
  step `ingest` binding it, *Then* SQLDatabase `orders-db` carries `orders`' marker and owner `orders-ingest`, and the
  step writes it.
- **scenario: workflow-config-hashed** — *Given* the Workflow defines ConfigMap `settings`, *Then*
  `settings-<hash>` exists and each step Function binding `settings` reads it; *when* its data changes, *Then* the
  steps roll to the new object, and the old one is pruned after the switch.
- **scenario: workflow-secret-check** — *Given* the Workflow declares Secret `api-keys` with key `STRIPE_KEY`, *Then*
  it waits `SecretNotFound`, then `SecretKeyMissing`, and materializes no step until the Secret holds the key.
- **scenario: workflow-catalog** — *Given* the Workflow declares catalog `lake` over its root bucket, *Then* the
  CatalogService carries the Workflow's marker, and a step binding `lake` queries it; *when* the Workflow is
  deleted, *Then* `lake` and its defined ConfigMaps go with it.
- **scenario: workflow-ref-kept** — *Given* `ref` entries for a database, a catalog and a ConfigMap, *Then* the steps
  bind them, and deleting the Workflow leaves them unchanged.
- **scenario: step-binding-needs-root-entry** — *Given* a step binding database `ghost`, which the root does not
  declare, *Then* apply refuses it, naming `spec.steps[0].function.bindings.sql[0]` and `ghost`.

## Scope

**In**: the four Workflow root keys; ownership, hashing, the Secret check and GC for them; the end of the step
exception. **Out**: an App's Workflow parts, which keep using the App's root (ADR-0248 Decision 2); a standalone
CatalogService (ADR-0251).

## Constraints & Decision drivers

- One entry shape per kind, ADR-0248's; one ownership rule, `internal/ownedstore`.
- ConfigMaps and Secrets behave as an App's do (ADR-0213), so the two declarers never differ.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. The four keys at the Workflow's root** | One rule for every kind | More materializer work | **Chosen** (the decider, #1076) |
| B. Keep the step exception | No change | Two rules; a Workflow file is not self-contained | Rejected (the decider) |

## Decision

1. **Root keys.** `WorkflowSpec` gains `sql`, `catalogs`, `configMaps` and `secrets`, with ADR-0248's shared entry
   types. A database's or a prefix's `owner` names either a step, mapped to `<workflow>-<step>` as a KV table owner
   is, or a catalog the root declares, kept as written. Apply refuses any other owner, and a step named like a
   declared catalog.
2. **The root-entry rule covers steps.** Every `sql`, `catalog`, `config` and `secret` binding of a step names an entry
   of its Workflow's root, or of the App's root for an App's part. The `binding-declared` admission refuses any other,
   naming the binding. ADR-0248 Decision 2's first exception ends.
3. **Ownership.** The materializer writes `name` entries through `ownedstore.Ensure`, with the Workflow's marker, and
   never rewrites `ref` entries. A defined ConfigMap or catalog, whose entry has no `deletion`, also carries the
   Workflow's controller reference (ADR-0213 Decision 2), so it goes with the Workflow. A database, catalog or
   ConfigMap it does not own is `StoreNotOwned`.
4. **ConfigMaps** follow ADR-0213 Decisions 2, 3 and 5: a defined one is stored as `<name>-<hash>`, the materializer
   repoints each step Function's `config` binding to it, and the old object is deleted after the switch unless
   something still names it.
5. **Secrets** follow ADR-0213 Decision 8: before it materializes a step, the Workflow checks each declared Secret
   and every declared key, and waits `SecretNotFound` or `SecretKeyMissing`, naming it. No value enters a status.
6. **GC.** `gc.Pairs()` gains the Workflow to its SQLDatabases, CatalogServices and ConfigMaps.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 — WorkflowSpec, beside ADR-0248's KVStores and Buckets.
SQLDatabases       []SQLDatabaseEntry  `json:"sql,omitempty"`
CatalogServices    []CatalogEntry      `json:"catalogs,omitempty"`
ConfigMaps         []ConfigMapEntry    `json:"configMaps,omitempty"`
SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
```

| Reason | On | When |
|---|---|---|
| `StoreNotOwned` (ADR-0248) | the Workflow | a `name` entry exists without its marker |
| `SecretNotFound`, `SecretKeyMissing` (ADR-0213) | the Workflow | a declared Secret or key is missing |

## Implementation plan

Order: after ADR-0246 and ADR-0248's round.

- **funcd:** the types and `just generate`; the materializer (`internal/workflow`: entries through `ownedstore`, the
  ConfigMap hash and repoint, the Secret check before any step); `binding-declared` for steps; `gc.Pairs()`. Tests:
  one per scenario, named after it (`TestScenarioWorkflowOwnsDatabase`, …), e2e in `pkg/funcd` with `t.Parallel()`.
- **The three repos:** each example Workflow declares what its steps bind (funcd-python's `sql-readings`, the todo
  App keeps its App-root entries).
- **Docs:** the back-link on ADR-0248, the F162 row.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e`), `go mod verify` and every Lima
  lane pass.

## Review checklist

- [ ] The four keys use ADR-0248's entry types; a database owner maps to `<workflow>-<step>`.
- [ ] Every step binding is checked against its declarer's root; the exception is gone.
- [ ] ConfigMaps are hashed, repointed and pruned; Secrets are checked by key before any step.
- [ ] `name` entries carry the marker, `ref` entries are never rewritten; the GC pairs hold.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: a Workflow file is self-contained; one rule for every kind.
- **Negative**: a Workflow that used an existing database must now declare it, by `ref`.
- **Risks accepted**: none.

## Open questions

None.

## References

- #1076, tracker #1072; ADR-0248's open questions.
- `internal/workflow/reconcile_workflow.go` (`materialize`, `buildFunction`, `stripBindings` at :335,
  `checkStepStores` at :362); `internal/app/reconcile.go:507` (`checkSecrets`).
