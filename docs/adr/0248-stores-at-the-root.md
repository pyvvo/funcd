# ADR-0248: Every kind declares the stores it owns at its root

- **Status**: Accepted (2026-10-19), after two judge rounds, under `adr-batch` with acceptance delegated by the
  decider, who also chose the narrower scope for the Workflow and the standalone CatalogService. The judge added one
  ownership check for every owner kind, ADR-0178's supersession for `ref` and App stores, ADR-0213's ConfigMap and
  Secret rules, the GC pairs and the Site rules.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: api, stores, ownership, refactor
- **Realizes**: [FEAT-0001/F159](../feat/0001-feat-v1.1.md), with ADR-0246 (#1068, tracker #1072)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0248` back-link:
  - [ADR-0178](0178-a-workflow-adopts-only-its-own-kv-stores.md) Decisions 2 and 4: a `ref` store, or a store the App
    declares, is bound without the marker check; the marker predicate covers every owner kind;
  - [ADR-0229](0229-undeclared-step-store-refused-at-apply.md): a step's store is declared by `name` or `ref`;
  - [ADR-0094](0094-workflow-engine-core.md): the Workflow's `spec.kv` entry type;
  - [ADR-0139](0139-site-declarative-static-web-app.md): `spec.bucket`, `spec.prefix`, and §5's take-over;
  - [ADR-0121](0121-declarative-referential-integrity-admission.md): a binding to a name its root does not declare is
    refused at apply instead of waiting.
- **Relates to**: [ADR-0246](0246-bindings-under-spec.md) (the bindings) · [ADR-0213](0213-app-config-and-secret-declarations.md)
  (its ConfigMap naming, repointing, pruning and Secret checks, reused) · [ADR-0199](0199-app-resource.md) ·
  [ADR-0241](0241-one-exposure-shape.md) (the precedent) · [ADR-0136](0136-roles-and-role-assignments.md)

## Context & Need

A Function can only name stores that exist (`api/types/v1alpha1/function.go`), so production needs a store file per
store, while `funcdctl dev` creates every store a binding names (`cmd/funcdctl/dev.go:1340`). Three kinds declare
stores already, in three shapes with two precedence rules:
- the Workflow refuses a store without its marker (`internal/workflow/reconcile_workflow.go:558`, `:573`);
- the App's entries take `name` or `ref`, under the same rule (`internal/app/reconcile.go:605`);
- the Site takes over an existing bucket (`internal/site/materialize.go:44`).

The marker predicate knows only Workflow and App markers (`IsKVMarker`, `reconcile_workflow.go:441`). No stored object
exists yet (the decider, 2026-10-19).

**Purpose**: a resource declares what it owns at the root of its spec, the App's way, so a Function's private stores need
no file, and every kind follows one precedence rule.

## Scenarios

- **scenario: function-owns-its-store** — *Given* Function `counter` with `kv: [{name: counters, tables: [{name:
  counts}]}]` and a binding to it, *Then* the stored Function shows `owner: counter`, KVStore `counters` carries
  `counter`'s marker, and `counter` writes `counts`.
- **scenario: function-refs-shared-store** — *Given* KVStore `sessions` and Function `login` with `kv: [{ref:
  sessions}]`, *Then* `login` binds it; *When* `login` is deleted, *Then* `sessions` stays, unchanged.
- **scenario: binding-needs-root-entry** — *Given* a Function binding store `ghost` that its root does not declare,
  *Then* apply refuses it, naming `spec.bindings.kv[0]` and `ghost`.
- **scenario: store-not-taken-over** — *Given* KVStore `counters` with another owner's marker, *When* `counter`
  declares `name: counters`, *Then* `counter` is `Ready=False` `StoreNotOwned`, and the store is unchanged.
- **scenario: user-cannot-replace-owned-store** — *When* a user applies a KVStore, Bucket or SQLDatabase that a
  Function owns, *Then* apply refuses it, as it refuses a KVStore a Workflow owns.
- **scenario: store-deletion** — *Given* `deletion: delete`, *When* the owner is deleted, *Then* the store goes; with
  `retain` it stays.
- **scenario: recreated-owner** — *Given* a retained store of a deleted Function, *When* the Function is applied again
  with `name`, *Then* it is `StoreNotOwned`; with `ref` it binds the store.
- **scenario: site-follows-the-rule** — *Given* Bucket `web-files` owned by an App, *When* a Site declares `name:
  web-files`, *Then* the Site is `StoreNotOwned`; with `ref` and an existing ownerless prefix `site` named by its
  `bindings.blob`, it serves from it.
- **scenario: function-config-and-secrets** — *Given* a Function declaring ConfigMap `app-settings` and Secret
  `api-keys` (key `STRIPE_KEY`), *Then* ConfigMap `app-settings-<hash>` exists, its binding is repointed, and the
  Function waits `SecretNotFound`, then `SecretKeyMissing`, until the Secret holds `STRIPE_KEY`.
- **scenario: pooled-functions-own-config** — *Given* two pooled Functions each defining `app-settings` with
  different data, *Then* they land in different pools and each reads its own values.
- **scenario: workflow-steps-bind** — *Given* a Workflow with `kv` (`name`), `buckets` (`name`) and a `ref` store,
  *Then* each step writes what it owns and reads the shared store.
- **scenario: app-parts-bind-app-stores** — *Given* an App declaring a KVStore, *Then* a function part and a Workflow
  part bind it with no root entry of their own; a part with its own root entry is refused.

## Scope

**In**: the root entries of Function, Workflow and Site; the Site's bindings; the root-entry rule; one ownership
package; the marker predicate; the owner default; a Function's ConfigMaps and Secrets; the new GC pairs.

**Out**: handover (ADR-0178 Decision 6) for owners other than a Workflow; a converter (nothing is stored).

## Constraints & Decision drivers

- One entry shape per store kind, the App's; one precedence rule, ADR-0178's; `ref` over adopting (the decider).
- A binding grants read; the owner and the writer roles write (ADR-0073, ADR-0136, ADR-0233).
- Go names of root entries differ from the removed binding fields, so no reader silently changes meaning.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. The App's entries at each root, one ownership package** | One rule; no file for a private store | New reconciler work for Function and Site | **Chosen** (the decider) |
| B. Keep the Site's take-over | No Site change | Two rules | Rejected (the decider) |
| C. A Function's catalogs, ConfigMaps and Secrets stay outside it | Less to build | Every Function with config needs files | Rejected (the decider) |

## Decision

1. **Root entries.** The App's entry types, renamed and shared: `KVStoreEntry`, `BucketEntry` and `SQLDatabaseEntry`
   (`name` or `ref`, `deletion`, the store's own spec); `CatalogEntry` and `ConfigMapEntry` (`name` or `ref`, the
   spec); `SecretDeclaration` (`name`, `keys`, `description`).

   | Kind | Root keys (JSON) | Go fields |
   |---|---|---|
   | Function | `kv`, `buckets`, `sql`, `catalogs`, `configMaps`, `secrets` | `KVStores`, `Buckets`, `SQLDatabases`, `CatalogServices`, `ConfigMaps`, `SecretDeclarations` |
   | Workflow | `kv` and `buckets`; table and prefix owners name steps, mapped to `<workflow>-<step>` | `KVStores`, `Buckets` |
   | Site | `buckets` (replaces `bucket`): exactly the one its `bindings.blob` names | `Buckets` |
   | App | unchanged | unchanged, on the shared types |

2. **The root-entry rule.** Every store a binding names (KV, bucket, database, catalog), and every ConfigMap or Secret
   a Function binds, is declared at the root of the resource that holds the binding, or at the root of the resource that
   declares it: an App's parts and catalog entries use the App's root, and a catalog entry uses its Function's.
   - Declared means by `name` or `ref` for a store or ConfigMap, and by its declaration for a Secret.
   - Two exceptions, chosen by the decider on 2026-10-19 and each tracked by a follow-up issue: a step's `sql`,
     `catalog`, `config` and `secret` bindings name existing resources, as today; a standalone CatalogService's
     bindings name existing stores, as today.
   - The `binding-declared` admission refuses any other binding, naming it. For an App's parts, the `app-parts`
     admission passes the App's declarations.
   - An App's part may not carry root entries of its own.
3. **One ownership package, `internal/ownedstore`,** used by the Function, Workflow, App and Site reconcilers:
   - `name` creates the store and stamps the owner's marker, plus a cascading owner reference under `deletion:
     delete`. An existing store without this owner's current-UID marker is refused, never taken over.
   - `ref` stores, and stores an App declares for its parts, are bound without a marker check and never rewritten; the
     Workflow's strip keeps them (`reconcile_workflow.go:334`). The materializer recognizes an App's store by the
     marker of the App that controls the Workflow.
   - One predicate, `IsMarker`, recognizes every owner kind. The replace guard (`internal/controlplane/handlers.go:957`,
     today on KVStores only) covers Buckets and SQLDatabases too, and the handover uses it
     (`internal/controlplane/kvhandover.go:95`), so no user apply replaces an owned store.
   - Reasons: `StoreNotOwned` (new) for a Function, a Site and a Workflow's buckets. The Workflow keeps
     `KVStoreNotOwned` for its KV stores and the App keeps `ChildNotOwned`; `PrefixOwned` stays for a Site.
4. **The owner default.** On a Function, an omitted `owner` in its root KV tables, bucket prefixes and databases is the
   Function, written in by the mutating `function-store-owner` admission. A Function's own store therefore always has a
   writer: a store with no in-platform writer is a store file, bound by `ref`. A prefix a catalog owns names its owner
   explicitly.
5. **A Function's ConfigMaps** follow ADR-0213 Decisions 2, 3 and 5. A defined one is stored as `<name>-<hash>`. The
   `function-store-owner` admission writes the stored name into the Function's `bindings.config`, so the environment,
   the pool key and the GC (`gc.go:582`) all read it, and two Functions with different data never share a pool. The
   old object is deleted after the switch unless something still names it.
6. **A Function's Secrets** follow ADR-0213 Decision 8. A declared Secret must exist with every declared key; the
   Function waits `SecretNotFound`, then `SecretKeyMissing`, and no value enters a status. Values come from the
   operator, or in dev from the environment (ADR-0247).
7. **A Site:**
   - Its served prefix moves from `spec.prefix` to `spec.bindings.blob`, one alias-free `{bucket, prefix}` entry
     (`SiteBindings`), naming the bucket of its one `buckets` entry.
   - `name` creates its bucket with its served prefix and no owner; `ref` needs that prefix to exist without an owner,
     and the Site never edits a referenced Bucket.
   - `deletion: delete` stays refused (ADR-0139).
   - Data-mount buckets in `ingress.rules` stay named directly.
8. **GC.** `gc.Pairs()` gains the Function to its KVStores, Buckets, SQLDatabases, CatalogServices and ConfigMaps, the
   Site to its Bucket, and the Workflow to its Bucket. A reclaimed Bucket is purged as an App's is.

## Temporary workarounds

- **`funcdctl dev` still reads `funcdctl.yaml`.** It writes `ref` entries for the stores it creates, `configMaps` refs,
  and `secrets` declarations with their keys. *Exit*: ADR-0247.

## Contracts

```go
// api/types/v1alpha1 — the App's entries, renamed and shared.
type KVStoreEntry struct {
	Name        ObjectName     `json:"name,omitempty"`
	Ref         ObjectName     `json:"ref,omitempty"`
	Deletion    DeletionPolicy `json:"deletion,omitempty"`
	KVStoreSpec `json:",inline"`
}
// BucketEntry and SQLDatabaseEntry: the same, over BucketSpec and SQLDatabaseSpec.
// CatalogEntry and ConfigMapEntry: Name, Ref and the spec inline, as AppCatalog and AppConfigMap today.
// SecretDeclaration: Name, Description and Keys, as AppSecret today.

// The Site's bindings (moved here from ADR-0246, so the Site changes once).
type SiteBindings struct {
	Blob []BlobRef `json:"blob"` // exactly one
}

type BlobRef struct {
	Bucket ObjectName `json:"bucket"`
	Prefix string     `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// internal/ownedstore — writes the stores a resource declares, under ADR-0178's marker rule, for any owner kind.
var ErrNotOwned = errors.New("ownedstore: the store exists and is not this owner's")

func IsMarker(r v1.OwnerReference) bool
func Refs(owner v1.Object, deletion v1.DeletionPolicy) []v1.OwnerReference
func Marked(refs []v1.OwnerReference, owner v1.Object) bool
func Ensure(ctx context.Context, st store.Store, owner, desired v1.Object, deletion v1.DeletionPolicy) (v1.UID, error)
func MapOwner(st store.Store, kind v1.Kind) controller.MapFunc
```

| New | Kind | Where |
|---|---|---|
| `binding-declared` (validating) | Function, Workflow, Site, App | `internal/controlplane/admission` |
| `function-store-owner` (mutating) | Function | `internal/controlplane/admission` |
| `StoreNotOwned` | Function, Site, Workflow buckets | their reconcilers |

## Implementation plan

Order:
1. A funcd PR with `internal/ownedstore`, the Workflow and App moved onto it, and `IsMarker` used by the admissions
   and the handover. No API change.
2. ADR-0246.
3. This ADR's API change in one funcd PR, with the language and example repos' YAML released first, on the decider's
   go.

- **funcd:**
  - The types, and `just generate`.
  - The Function reconciler: stores, ConfigMaps, Secrets. The Site reconciler. The two admissions. `gc.Pairs()`.
  - The handover and KV admissions on `IsMarker`.
  - Tests: one per scenario, named after it; e2e in `pkg/funcd`, with `t.Parallel()`.
- **The three repos:** a root entry for every binding: `name` for a private store, in place of its store file, and
  `ref` for a shared one.
- **Docs:** the back-links, the F159 row, the blueprint's KV and Site paragraphs.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` and `-tags dev`), `go mod verify`
  and every Lima lane pass.

## Review checklist

- [ ] Every root store entry is a shared type, under a Go name that differs from any removed binding field.
- [ ] The root-entry rule holds for every kind, with the App's root for its parts; a part's own root entry is refused.
- [ ] `name` stamps the marker, `ref` and App stores are never rewritten, an unmarked store is refused, and `IsMarker`
      covers every owner kind, in the admissions and the handover too.
- [ ] A Function's owners are written in by admission; its ConfigMaps are hashed, repointed and pruned; its Secrets
      are checked by key.
- [ ] The Site rules and the GC pairs hold.
- [ ] One test per scenario, named after it; every lane passes.

## Consequences

- **Positive**: a Function's private stores, ConfigMaps and Secrets live in its own file; one ownership rule.
- **Negative**:
  - a re-created Function cannot reclaim a store it retained, except by `ref`;
  - a Function's own stores always have a writer.
- **Risks accepted**: steps can now read shared stores through `ref`, as Functions do; the grant is still the binding.

## Open questions

None. The decider chose the narrower scope on 2026-10-19. Two follow-up issues under #1072 cover the rest:
- a Workflow declaring `sql`, `catalogs`, `configMaps` and `secrets` at its root, with owners naming steps, hashed
  ConfigMaps repointed in the step Functions, and a Secret check before the steps run;
- a standalone CatalogService declaring `buckets`, `configMaps` and `secrets` at its root, with the catalog reconciler
  as their writer.

## References

- #1068, tracker #1072.
- `internal/workflow/reconcile_workflow.go:334` (the strip), `:441` (`IsKVMarker`), `:558` (`checkKVStore`), `:573`
  (`ensureKVStore`); `internal/app/reconcile.go:503` (`checkSecrets`), `:605` (`ownerRefs`); `internal/site/materialize.go:31`;
  `internal/controlplane/kvhandover.go:95`; `internal/gc/gc.go:34` (`Pairs`); `internal/pooling/pooling.go:57`.
