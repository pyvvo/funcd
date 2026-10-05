# ADR-0180: Corrections to ADR-0178 — a refused Workflow keeps its own bindings; the migration record

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (judged twice; folded: the record is guarded at the REST handler, the audit sink is
  justified, a fourth clause is corrected, the guard scenario creates with no record present. **Accepted
  2026-10-05**, with acceptance delegated by the decider while away; no Blocker; held from publication with ADR-0178)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, kv, upgrade
- **Realizes**: [FEAT-0005/F64](../feat/0005-feat-workflow-engine.md) (Workflow engine core; the row ADR-0178 realizes)
- **Supersedes in part**: [ADR-0178](0178-a-workflow-adopts-only-its-own-kv-stores.md) (Accepted, held), four
  clauses only:
  1. Scenario `stale-step-binding-removed`, its last clause "and, in that same reconcile, `w-s` and `w-s2` carry no
     kv binding" (lines 90–91).
  2. Decision 3, "and emits one event per store it marks" (line 170), and plan step 2's "and event" (line 239).
  3. Contracts, "The migration's completion record is new; its key is set at implementation" (lines 230–231).
  4. Risks accepted, "audited by the per-store log line and event (Decision 3)" (line 288).
  Everything else in ADR-0178 stands.
- **Relates to**: [ADR-0170](0170-owner-garbage-collector.md) (Accepted, held; `KVStoreNotOwned`) ·
  [ADR-0171](0171-static-credential-list.md) (who reaches a namespace)
- **Publication**: held with ADR-0178.

## Context & Need

Implementing ADR-0178 found four points it cannot be built from as written:

1. Scenario `stale-step-binding-removed` ends with `w-s` unbound after a `KVStoreNotOwned` refusal, but `w-s` binds
   only `w-kv`, which `w` made and marked. Decision 2 strips only bindings to stores without this incarnation's
   marker and writes no `patchFunctionKV` on a refusal, and the header says "no other write happens on a refusal".
   Under the Decision `w-s` keeps `w-kv`; the Scenario says it does not.
2. Decision 3 asks the migration to emit an event per marked store. funcd has no event kind. The audit channel the
   blueprint names (`observability.AuditRecorder`, blueprint line 461) exists, but nothing in production constructs
   it or routes its stream.
3. The completion record's key was left to implementation, but `MarkKVStoresOnce(ctx, store.Store)` can write only
   API objects. Whoever deletes that object makes the migration run again, and a re-run marks every unmarked store
   that then meets Decision 3 (a)–(d) in every namespace — including an API-made store a Workflow declared after
   the upgrade (Scenario `post-upgrade-api-store-refused`), which Decision 3 says is refused.
4. Risks accepted (line 288) still names the event.

## Scenarios

- `scenario: stale-step-binding-removed` (corrected) — Given an API-created KVStore `shared`, Workflow `w` that made
  `w-kv`, and Function `w-s2` bound to `shared` by an earlier reconcile, When `w` reconciles with step `s` binding
  `w-kv` and step `s2` binding nothing, Then `w` is Ready, `w-s` binds only `w-kv`, `w-s2` carries no kv binding and
  `shared` is unchanged; When `w` then also declares `shared`, Then `w` is NotReady `KVStoreNotOwned` and, in that
  same reconcile, `w-s` still binds only `w-kv` and `w-s2` carries no kv binding.
- `scenario: migration-record-stops-rerun` — Given the migration ran and wrote its record, When funcd restarts with
  an unmarked KVStore that meets ADR-0178 Decision 3 (a)–(d), Then the store stays unmarked.
- `scenario: migration-audit-is-one-line` — Given two stores the migration marks, When it runs, Then it writes two
  log lines naming namespace, store, Workflow and UID, and nothing else.
- `scenario: migration-record-guarded` — Given an admin token, When it creates ConfigMap
  `funcd-system/kvstore-marker-migration` through the API while no record exists, Then it answers 409 and no record
  exists; and When the record exists and the admin updates or deletes it, Then each answers 409 and the record is
  unchanged.

## Scope

- **In**: the four clauses above; the REST guard on the record.
- **Out**: everything else in ADR-0178; an event kind; wiring the audit channel in production.

## Constraints & Decision drivers

- An Accepted ADR is changed only by a superseding ADR (ADR-0000).
- ADR-0178's own rule, "No binding to a store this incarnation does not own survives a reconcile", governs.
- Keep `MarkKVStoresOnce(ctx, store.Store)` as ADR-0178's Contracts give it.
- After the upgrade, only a metastore restore may re-run the migration (ADR-0178 Decision 3 already accepts that).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Scenario follows the Decision: a refusal keeps owned bindings | matches Decision 2, the header and the "never kept live" rule | none | **chosen** |
| Decision follows the Scenario: a refusal strips every binding | the Scenario stands | a refusal cuts the Workflow off its own store; contradicts "no other write on a refusal" | rejected |
| Audit by one operational log line per mark | exists today; each mark also persists as the store's marker ownerRef | not on the dedicated audit stream | **chosen**, a stated departure from blueprint line 461 until the audit channel is wired |
| Record through `AuditRecorder` | the blueprint's channel | nothing in production constructs it or routes its stream; wiring it is its own decision | rejected for now |
| Add an event kind | listable audit | a new subsystem for one migration | rejected |
| Record as ConfigMap `funcd-system/kvstore-marker-migration`, refused at the REST handler | fits the Contracts signature; no API principal can delete or pre-create it | one REST guard | **chosen** |
| The same ConfigMap, unguarded | no guard | any principal with `funcd-system` in scope forces a re-run that marks post-upgrade API stores everywhere | rejected (judge) |
| A raw metastore key outside the API | unreachable by the API | changes the frozen Contracts signature; a new store seam | rejected |

## Decision

1. **A refusal keeps the incarnation's own bindings.** The corrected Scenario replaces ADR-0178's. Decision 2's
   strip, its "no `patchFunctionKV` on a refusal" and the header's "no other write happens on a refusal" stand.
2. **Audit is one log line per marked store** (namespace, store, Workflow, UID) at Info, on the operational log. No
   event is emitted. This departs from blueprint line 461 until an ADR wires the audit channel in production.
3. **The completion record** is ConfigMap `kvstore-marker-migration` in namespace `funcd-system`, written with
   `store.Create` after the last mark and after the binding strip (ADR-0178 Decision 3's order), so a crash before
   either finishes re-runs the migration. `funcd-system` is a new namespace name for this record only; no Namespace
   object is created for it (today the name exists only as the observability bucket).
4. **The record is guarded.** The REST handler for ConfigMaps refuses create, update and delete of
   `funcd-system/kvstore-marker-migration` with 409 (`fault.Conflict`), as ADR-0178's no-pre-claim guard does for a
   live-marked KVStore: in the handler, not admission, so the in-process `store.Create` of the migration is unaffected.

## Temporary workarounds

None.

## Contracts

```go
// internal/workflow/kvmigration.go
const (
	KVMigrationNamespace v1.NamespaceName = "funcd-system"
	KVMigrationRecord    v1.ObjectName    = "kvstore-marker-migration"
)
```

The ConfigMap REST handler (`internal/controlplane`) answers 409 to POST, PUT and DELETE on that name.
`MarkKVStoresOnce(ctx context.Context, s store.Store) error` is unchanged (ADR-0178 Contracts).

## Implementation plan

1. Implement ADR-0178 with these corrections; no other change to its plan.
2. The ConfigMap handler guard in `internal/controlplane`, importing the two names from `internal/workflow` (the
   depguard graph allows it; `internal/workflow` does not import `internal/controlplane`).
3. Tests: `TestScenarioStaleStepBindingRemoved`, `TestScenarioMigrationRecordStopsRerun` and
   `TestScenarioMigrationAuditIsOneLine` in `internal/workflow`; `TestScenarioMigrationRecordGuarded` in
   `internal/controlplane`.
4. At acceptance, add `Superseded in part by: ADR-0180` to ADR-0178's header.

## Review checklist

- [ ] After a `KVStoreNotOwned` refusal, a step bound only to a store this incarnation marked keeps that binding.
- [ ] The migration writes one log line per marked store and no event.
- [ ] The record is written after the last mark and the binding strip; with it present, a restart marks nothing.
- [ ] API create, update and delete of `funcd-system/kvstore-marker-migration` answer 409.

## Consequences

- Positive: ADR-0178 can be built as written; a refused Workflow keeps serving its own stores; on this version and
  later no API principal can re-run or skip the migration.
- Negative: the migration's audit is on the operational log, not the audit stream.
- Risks accepted: a metastore restored without the record re-runs the migration (ADR-0178 Decision 3). On a daemon
  older than this change the record is not guarded, so a principal with `funcd-system` in scope (an admin, or a
  credential an operator scopes there; the default scope is `default`) can pre-create it before the upgrade; the
  migration then marks nothing and every Workflow's own `retain` store turns `KVStoreNotOwned` (fail closed; the
  operator hands each store over, ADR-0178 Decision 6).

## Open questions

- Moving the migration's log line to the audit stream — answered by the ADR that wires `AuditRecorder` in production.

## References

- ADR-0178; ADR-0170; ADR-0171; ADR-0000 (supersession); blueprint line 461.
