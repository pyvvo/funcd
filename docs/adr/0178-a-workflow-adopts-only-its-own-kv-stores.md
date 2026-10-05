# ADR-0178: A Workflow adopts only its own KV stores

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0180](0180-kv-store-adoption-corrections.md) (2026-10-05) — the `stale-step-binding-removed` scenario's last clause, the migration's event, its completion record and the Risks line naming the event.
- **Date**: 2026-10-05 (judged twice by three lenses; held from publication)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, kv, owner-references, lifecycle, garbage-collection, upgrade
- **Realizes**: [FEAT-0005/F64](../feat/0005-feat-workflow-engine.md) (Workflow engine core; the row ADR-0094 realizes)
- **Refines** Proposed [ADR-0170](0170-owner-garbage-collector.md) (held from publication, as is this ADR), its
  KVStore half only:
  - Decision 4 (lines 115–116) "(any UID); `ensureKVStore` also keeps an unowned store (#149)" and Contracts line 183
    "`ensureKVStore` when hasController && !controlledBy": replaced by Decisions 2–4 here. Decision 4's step
    Function rule stands, except that a re-stamp to another UID drops the Function's `spec.kv` (Decision 4 here).
  - Decision 1 (lines 96–97) "A child is an object … with a `Controller: true` ownerRef", line 101 "Other ownerRefs
    are ignored" and Scope line 75 "Out: … non-controller ownerRefs": for KVStores the collector also reads the
    non-controller marker (Decision 5). Decision 5 (line 126) "re-adding re-adopts it with its data": only by the
    same incarnation. Scenario `recreated-workflow-takes-new-uid` (line 50): its KVStore part becomes Scenario 4.
  - Line 22 "Made true (unchanged): ADR-0094 lines 109–111": lines 110–111 are now superseded in part (below).
  - Consequences line 276 "collects every old leftover, including a store flipped `delete` → `retain` …" and plan
    step 7, line 260 (release note "collects every leftover of an owner deleted earlier"): KVStores are excepted; an
    unmarked KVStore is never collected (Decision 5).
  - Consequences line 277, Risks accepted: only the sentence "A Workflow still adopts an unowned KVStore, so a
    principal with Workflow write but no KVStore delete can delete a user's or another Workflow's `retain` store
    with its keys." is closed and removed at acceptance; the rest of that line, including the admission bypass that
    Decision 6 relies on, stands.
  - Decision 4 "stops with no write" (the `FunctionNotOwned` and `KVStoreNotOwned` refusals): the binding strip of
    Decision 2 here runs first and unconditionally, so it is written even when the reconcile then refuses. On that
    point this ADR wins; no other write happens on a refusal.
- **Supersedes (in part)** [ADR-0094](0094-workflow-engine-core.md) (Implemented; back-link `Superseded in part by:
  ADR-0178` at acceptance): lines 110–111 "a **read-only orphan** until a workflow (or Function) re-declares
  ownership": a Workflow re-declares a kept store only as the incarnation that made it, or after a handover
  (Decision 6). A Function re-declaring ownership through a KVStore edit is unchanged. Lines 109–110 (`retain`
  default leaves the store; `delete` cascades) and 482 stand.
- **Relates to**: [ADR-0073](0073-kv-bindings-and-subdomains.md) (table owner = writer Function) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (per-kind, per-namespace authorization) · ADR-0147 (namespace
  lock) · ADR-0163 (supervision period).

## Context & Need

`buildKVStore` (`internal/workflow/reconcile_workflow.go:249-268`, cited at 915f342) sets the Workflow's controller
ref only for `deletion: delete`; a `retain` store, the default, carries no ref (:258-262). `ensureKVStore`
(:292-311) Gets the same-named store, keeps its `ObjectMeta`, then writes this Workflow's owner refs and tables
(:304-306), whoever created it. So a principal allowed to write Workflows in a namespace, but not KVStores, can name
any store there in `spec.kv`: the materializer re-points its tables to that Workflow's step Functions (and, under
`delete`, adds a controller ref, so the store and its keys go when the Workflow is deleted). Draft ADR-0170 narrows
this to unowned stores and to a controller ref by kind and name at any UID, which still covers every `retain` store
and every predecessor's store. Authorization is per kind and namespace, not per name
(`internal/controlplane/handlers.go:35-48`), so it cannot stop this. Issue #149 (a store kept its first owner ref
when the policy changed) must stay fixed.

Need: a Workflow writes a same-named KVStore only when it can prove this incarnation made it; every other case
fails closed with no write, and a step binds only a store that passes the same rule. Its own stores keep today's
behavior, and no Workflow that binds only its own stores breaks at upgrade.

## Scenarios

- `scenario: own-store-follows-policy-and-tables` — Given Workflow `w` made `w-kv` (`retain`, one table), When `w`
  is re-applied with `deletion: delete`, then with a second table, then with `retain`, Then each apply succeeds,
  `w-kv` keeps its UID and its key, its tables follow the spec, and it carries the marker always and the controller
  ref only while `delete`.
- `scenario: users-store-refused` — Given an API-created KVStore `shared` with key `k`, When Workflow `w` declares
  `shared` (`retain` or `delete`), Then `w` is NotReady `KVStoreNotOwned`, `shared`'s owner refs and tables are
  unchanged, and after `w` is deleted `shared` and `k` remain.
- `scenario: other-workflows-store-refused` — Given `a` made `shared`, When `b` declares `shared`, Then `b` is
  NotReady `KVStoreNotOwned` and `shared` is unchanged.
- `scenario: recreated-workflow-needs-handover` — Given `w` (UID `u1`) made `w-keep` (`retain`, key `k`) bound to
  step `w-s`, and `w` is deleted and re-created (UID `u2`), Then `w` is NotReady `KVStoreNotOwned`, `w-keep` is
  unchanged and `w-s` carries no kv binding; When an operator runs `funcdctl kvstore handover w-keep w`, Then `w`
  is Ready, `w-keep` carries the `u2` marker, and `k` is readable through `w-s`.
- `scenario: handover-needs-kvstore-update-and-delete` — Given a principal allowed to update Workflows but not
  KVStores, or one with KVStore `update` but not `delete`, When it calls the handover (`retain` or `delete`), Then
  403 and the store is unchanged; and while the store's marker names a Workflow UID that still exists, 409.
- `scenario: upgrade-migration-marks-own-store` — Given a store the previous materializer made for `w` after `w`
  was created (no refs, or `w`'s controller ref), When the upgraded funcd boots, Then the migration marks it with
  no other change, `w` is Ready, and a later policy flip works as in Scenario 1.
- `scenario: upgrade-refuses-older-store-and-collector-keeps-it` — Given a user's store created before `w`, which
  the previous materializer gave `w`'s controller ref (`delete`), When the upgraded funcd boots and reconciles `w`,
  Then the store stays unmarked and `w` is NotReady `KVStoreNotOwned` with no write; When `w` is deleted and two
  collector passes run, Then the store and its keys remain.
- `scenario: post-upgrade-api-store-refused` — Given Workflow `w` created before the upgrade and declaring nothing,
  When after the upgrade a user creates KVStore `shared` through the API with owner-less tables and `w` then
  declares `shared`, Then `w` is NotReady `KVStoreNotOwned` and `shared` is unchanged.
- `scenario: step-binding-to-unowned-store-refused` — Given an API-created KVStore `shared` with table `t`, Workflow
  `w` that made `w-kv`, and Function `w-s` already bound to `shared`/`t` by the previous materializer, When after
  the upgrade `w` binds `shared`/`t` in step `s` (`function.kv`) without declaring it in `spec.kv`, and step `s2`
  binds `w-kv`, Then `w` is NotReady `KVStoreNotOwned`, Functions `w-s` and `w-s2` carry no kv binding, and
  `shared` is unchanged.
- `scenario: stale-step-binding-removed` — Given an API-created KVStore `shared`, Workflow `w` that made `w-kv`,
  and Function `w-s2` bound to `shared` by an earlier reconcile, When `w` reconciles with step `s` binding `w-kv`
  and step `s2` binding nothing, Then `w` is Ready, `w-s` binds only `w-kv`, `w-s2` carries no kv binding and
  `shared` is unchanged; When `w` then also declares `shared`, Then `w` is NotReady `KVStoreNotOwned` and, in that
  same reconcile, `w-s` and `w-s2` carry no kv binding.
- `scenario: strip-survives-removed-step-and-early-failure` — Given an API-created KVStore `shared` and Function
  `w-s`, controlled by `w`, bound to `shared` by the previous materializer, When `w` removes step `s` and declares
  `shared` (refused `KVStoreNotOwned`), and separately When `w` keeps `s` but an earlier step's image runtime cannot
  be resolved, Then in each case `w` is NotReady and, in that reconcile, `w-s` carries no kv binding.

## Scope

In: when `ensureKVStore` may write a same-named KVStore; which stores a step's kv binding may name; the creation
marker; the one-shot upgrade migration; which KVStores ADR-0170's collector may delete; the handover. Out: Function
adoption (ADR-0170 Decision 4) beyond the `spec.kv` drop; KV write authorization by table-owner Function name (ADR-0073,
`internal/auth/cedar/capabilities.go:155-156`), an accepted risk with a follow-up; KVStores made through the API
(they keep no Workflow ref); `function.ref` steps (ADR-0096, `workflow.go:303-318`), which still let a Workflow
writer dispatch any Function of the namespace, a user's Function bound to the user's store included, reaching its
data through that Function's own logic. Unchanged here; this ADR removes only Workflow-controlled bindings.

## Constraints & Decision drivers

- Fail closed: an ambiguous case is a refusal with no write, never a guess.
- Proof must be unforgeable through the API: the control plane clears owner refs on create and copies the stored
  ones on update (`withServerMeta`, `handlers.go:92`, `:182`, `:231-241`). Labels and annotations do not exist on
  `ObjectMeta` (`api/types/v1alpha1/metadata.go:130-149`); `Tags` are written by API callers. The store sets
  `CreationTime` on create (`internal/store/store.go:331`) and keeps it on update (`:408`), so the migration's
  creation-time test cannot be forged through the API either.
- ADR-0170 Decision 1 ignores ownerRefs that are not `Controller: true`, so a non-controller ref collects nothing.
- `retain` is the default and existing `retain` stores carry nothing, so the upgrade must mark some unmarked stores.

## Alternatives considered

1. **A creation marker with the Workflow's UID on every store the materializer creates, a one-shot boot migration
   that marks existing qualifying stores, and exact-UID matching** — **chosen**.
2. Adopt an unowned store without changing it — rejected: it breaks a Workflow's own `retain` store, since a
   `retain` → `delete` flip and table edits stop applying (ADR-0094, #149).
3. Refuse every unowned store — rejected: an outage at upgrade, since every existing `retain` store is unowned.
4. Keep ADR-0170's rule — rejected: any same-named Workflow, at any UID, takes every `retain` store.
5. Adopt unmarked stores in `ensureKVStore` on every reconcile — rejected: unbounded in time, so a Workflow writer
   can declare an API-created store later and take it.

## Decision

1. **The marker.** A non-controller `OwnerReference` `{Kind: Workflow, Namespace, Name, UID, Controller: false,
   BlockOwnerDeletion: false}` naming the Workflow incarnation. `buildKVStore` writes it under both policies; under
   `delete` it adds today's controller ref (`ownerRef`, :197-203), so a `delete` store carries two refs, a `retain`
   store one. The previous materializer never wrote a non-controller ref, so the marker is distinguishable from
   anything it left. Only in-process writers using `store.Create`/`store.Update` set it: the materializer, the
   migration (Decision 3) and the handover handler (Decision 6). An API caller cannot.
2. **`ensureKVStore`.** NotFound ⇒ create with the built refs; if a concurrent create wins, `store.Create` fails as
   a Conflict (`store.go:321`) and the retry finds the store and applies this check. Found ⇒ the store is this
   Workflow's iff it carries a marker naming this Workflow's kind, name and UID; then it writes as today (keeps
   `ObjectMeta`, sets the built refs and tables), so #149 and table edits behave as before. That write replaces
   every ref, as today; only the materializer, the migration and the handover write KVStore refs, so no other ref
   exists to lose. Keeping `cur.ObjectMeta` (:304-305) makes `store.Update` conditional on the read
   resourceVersion (`store.go:394-395` compares it unconditionally, also when empty), so a store deleted and
   re-created between the check and the write fails the update and the retry re-checks. Otherwise: NotReady
   `KVStoreNotOwned`, requeued after the supervision period (the reason and requeue of ADR-0170 Decision 4), with
   no KVStore write and no `patchFunctionKV` (phase 3, :172-181, the binding that is the capability).
   **Step bindings.** Phase 3 copies `steps[].function.kv` into the step Function (:322) with no ownership check,
   and `validateOwnedStores` (`api/types/v1alpha1/workflow.go:436-452`) checks only `spec.kv`. So every store a
   step binds (`function.kv[].store`) must be declared in `spec.kv` and pass the check above in the same reconcile
   before phase 3 writes any binding; otherwise the same refusal, with no `patchFunctionKV` for any step. The rule
   lives in the materializer, where the marker is read; admission is unchanged.
   **No binding to a store this incarnation does not own survives a reconcile.** Every reconcile starts, before
   runtime resolution, phase 1 (:150-163) and any check that can refuse, with an unconditional strip: every Function
   whose controller ref names this Workflow's kind, name and UID (removed steps included) loses each `spec.kv` entry
   whose store lacks this incarnation's marker, written at its read resourceVersion; a failed strip write ends the
   reconcile NotReady, requeued, with no later phase. `ensureFunction` then keeps from `cur.Spec.KV` (:284) only
   marked-store entries. On a pass, phase 3 writes every owned step's `spec.kv` from its `function.kv`, an empty one
   included (the `len(st.Function.KV) == 0` skip at :174 goes). So an earlier binding (the previous materializer's,
   or one whose store stopped passing) is never kept live, Ready or NotReady.
   **Binding tied to the checked UID.** The check records each passed store's UID; right before `patchFunctionKV`,
   phase 3 re-reads every store the step binds and refuses (`KVStoreNotOwned`, no write) on another UID or no
   marker. The residual window between that re-read and the Function write needs a second principal with KVStore
   delete and create; the next reconcile's strip removes such a binding.
3. **One-shot upgrade migration.** At boot, before the controllers start, before ADR-0170's collector (which runs
   beside them) first sweeps, and before the control plane serves, funcd marks
   each KVStore that has no marker, once, when all hold: (a) a Workflow of the store's namespace has
   `CreationTime` not later than the store's; (b) every controller ref on the store names that Workflow's kind,
   name and UID; (c) every table owner is empty or exactly equals `<workflow>-<step>` for a step in that Workflow's
   current spec (the names `buildKVStore` :253-255 writes); (d) exactly one Workflow satisfies (a)–(c) and declares
   the store in `spec.kv`. It logs one line and emits one event per store it marks (namespace, store, Workflow,
   UID) for audit, and strips from every Workflow-controlled Function each `spec.kv` entry whose store it left
   unmarked. It then records completion in a new metastore record and never runs again; a metastore restored without
   that record re-runs it, bounded to the restored stores. After it, `ensureKVStore` has no path for an unmarked
   store: a store made through the API later is refused.
4. **Exact UID.** A store whose marker or controller ref names another UID is never re-stamped: a re-created
   Workflow does not take its predecessor's store, `retain` or `delete`. `ensureFunction` keeps `cur.Spec.KV`
   (:284) only when the found Function's controller ref names this Workflow's UID; a re-stamp from another UID
   writes an empty `spec.kv`, so a predecessor's step Function binding does not survive into the new incarnation.
5. **Collector (ADR-0170 Decision 1, KVStore only).** A KVStore is a child only when its controller ref and a
   marker name the same kind, name and UID. An unmarked KVStore is never collected, so a ref the previous
   materializer wrote onto a user's store deletes nothing. A predecessor's marked `delete` store is collected.
6. **Handover.** `POST /apis/funcd.io/v1alpha1/namespaces/{ns}/kvstores/{name}/handover` with body
   `{"workflow": "<name>"}` (new). No subresource mapping exists in the ADR-0018 authorizer today, so the handler
   asks it for each pair explicitly: `update` and `delete` on KVStore and `get` on Workflow in `{ns}`. `delete` is
   asked whatever the current policy: the Workflow's writers can later flip it to `delete`, which makes the store
   collectable, and the collector bypasses kvstore-deletion-protection (ADR-0170 line 277). 404 when the store or
   Workflow is missing; 409 when the Workflow's `spec.kv` does not declare the store, or when the store's marker
   names a Workflow UID that still exists (no silent re-assignment; that owner must be deleted first). Effect: the
   store's refs become the marker of the named Workflow at the UID the handler read; spec and data are
   untouched. The handler runs the KVStore Update admission chain the API runs (`admission/kvstore.go:84`, ADR-0073
   kv-owner-exists, the ADR-0147 namespace lock) on the new object, then writes with `store.Update` at the read
   resourceVersion. If the Workflow is re-created in between, the marker names a stale UID and the new Workflow is
   refused (fail closed); the operator re-runs the handover. The Workflow's next reconcile adds the controller ref
   if its policy is `delete`. CLI `funcdctl kvstore handover <store> <workflow>` (new command group), under
   `kvstore` because the authorization is on the KVStore.
   **No pre-claim.** The KVStore API update handler refuses with 409 a write to a store whose marker names a live
   Workflow UID, so a user's apply never lands keys under a Workflow's lifecycle. It lives in the REST handler, not
   admission, so the materializer and handover are unaffected; a dead-UID marker stays editable (ADR-0094).
7. **Publication.** Held with ADR-0170; at acceptance ADR-0170's Decision 4 points its KVStore half here.

## Temporary workarounds

| Gap | Effect | Closed by |
|---|---|---|
| KV writes authorize by table-owner Function name (ADR-0073) | A principal that can create a Function named like a table owner writes that table | Follow-up issue at acceptance |

## Contracts

| Consumes | Exposes |
|---|---|
| `store.Store` `Get`/`List`/`Create`/`Update`; ADR-0170 `KVStoreNotOwned` and requeue; the ADR-0018 authorizer | the marker; the migration; the handover route, `sdk.Client.HandoverKVStore`, `funcdctl kvstore handover` |

```go
// internal/workflow/reconcile_workflow.go — all new; ownerRef (:197) unchanged.
func kvMarker(wf *v1.Workflow) v1.OwnerReference // ownerRef(wf) with Controller and BlockOwnerDeletion false
func marked(refs []v1.OwnerReference, wf *v1.Workflow) bool // a non-controller ref with wf's kind, name, UID

// internal/workflow — new; the Decision 3 migration, called once at boot before serving.
func MarkKVStoresOnce(ctx context.Context, s store.Store) error
func adoptableAtUpgrade(cur *v1.KVStore, wf *v1.Workflow) bool // Decision 3 (a)-(c); table owners match exactly

// internal/gc (ADR-0170) — KVStore children only.
func kvStoreCollectable(refs []v1.OwnerReference) bool // controller ref and marker name the same kind, name, UID

// pkg/sdk — new.
func (c *Client) HandoverKVStore(ctx context.Context, ns v1.NamespaceName, store, workflow v1.ObjectName) error
```

All names above, the `handover` route and the `funcdctl kvstore` command group were grepped on main and are
unused; `KVStoreNotOwned` exists only in draft ADR-0170. The migration's completion record is new; its key is set
at implementation.

## Implementation plan

1. `internal/workflow/reconcile_workflow.go`: `kvMarker` in `buildKVStore` (update the :258-259 comment);
   `ensureKVStore` per Decisions 2 and 4; phase 3 checks every step-bound store first (Decision 2);
   the first-step strip; `ensureFunction` keeps only marked-store entries, none on a UID re-stamp; phase 3 patches
   empty `function.kv` too and re-checks the UID before `patchFunctionKV`.
2. `internal/workflow`: `MarkKVStoresOnce` (log line and event per store, binding strip, completion record), called
   by `pkg/funcd` boot before the controllers and control plane; the live-marker 409 in the KVStore update handler.
3. `internal/gc`: `kvStoreCollectable` in the KVStore pair (with ADR-0170, whichever lands second wires it).
4. `internal/controlplane`: the handover route in `routes_rest.go` (beside :557) and its handler; `pkg/sdk`;
   `cmd/funcdctl/kvstore.go`; `just generate`.
5. Tests, `TestScenario` + scenario name in CamelCase: Scenarios 1–3, 6 and 8–11 in `internal/workflow`; 7 in
   `internal/gc` with two `CollectNamespace` passes, written by whichever of ADR-0170 and this ADR lands second; 4
   in `pkg/funcd` (tag `e2e`, short data dir); 5 in `internal/controlplane`.
   `TestMaterializeDeletionPolicy` (:118, :122) and `TestIssue149_KVDeletionPolicyChangeUpdatesOwnerRef`
   (:134-135, :169) count controller refs instead of all refs and also assert the marker; their
   behavior expectations are unchanged.
6. Done: every scenario's test passes; `just ci-full` and the Lima lanes green.
7. At acceptance: the ADR-0094 back-link; ADR-0170 Decision 4 points here; the follow-up issue for ADR-0073; the
   FEAT-0005 F64 row links ADR-0170 and ADR-0178 when the held drafts are published; re-stamp this ADR and ADR-0170
   to the same commit.

## Review checklist

- [ ] No `KVStoreNotOwned` path performs a KVStore write or `patchFunctionKV`.
- [ ] Phase 3 writes a step's kv binding only after every store it binds is declared in `spec.kv` and passed the
      Decision 2 check.
- [ ] The strip runs first over every controlled Function (removed steps included), before runtime resolution or
      any refusal; the migration strips bindings to stores it leaves unmarked (Scenario 11).
- [ ] Phase 3 binds only the store UID it checked; the KVStore API refuses an update of a live-marked store.
- [ ] After any reconcile no step Function binds a store without this incarnation's marker, and a step whose
      `function.kv` is empty carries no `spec.kv` (Scenario 10).
- [ ] Only the materializer, the migration and the handover handler write a marker; `withServerMeta` is unchanged.
- [ ] The migration runs once, before serving, and `ensureKVStore` has no unmarked-store path.
- [ ] The collector never deletes an unmarked KVStore.
- [ ] A step Function re-stamped from another UID carries no `spec.kv`.
- [ ] ADR-0094's deletion-policy and #149 tests pass with only the ref-count change of plan step 5.
- [ ] No identity or absolute-path leak.

## Consequences

- Positive: a Workflow writer can no longer take, re-point, bind a step to or schedule the deletion of a store it
  did not make.
- Negative: a Workflow re-created over a predecessor's kept store, or one that declared a user's older store, turns
  NotReady `KVStoreNotOwned` at upgrade until an operator runs the handover. A predecessor's `delete` store left
  unmarked at upgrade is no longer collected; the handover or a KVStore delete clears it. Stores carry one more ref.
  A re-created Workflow's step loses its kv binding until the handover. A store a live Workflow made and then
  dropped from `spec.kv` (kept under `retain`) stays unmarked at upgrade; re-adding it is refused until a handover.
  Upgrade break: an existing Workflow whose step binds a store not declared in `spec.kv` (an API-made store,
  including the step writer's own store) turns NotReady `KVStoreNotOwned` at upgrade and loses all step bindings.
  It can bind such a store again only by declaring it in `spec.kv` and running the handover, which hands the
  Workflow's writers full lifecycle control of the store (a handover requires KVStore `delete`).
- Risks accepted: migration laundering. The previous materializer rewrote the tables of every store a Workflow
  declared (:304-306), so criterion (c) holds for practically every declared store; any pre-upgrade takeover of a
  store created after one of the taker's Workflows becomes permanent, marked ownership with full lifecycle control.
  This is bounded to stores present at upgrade and audited by the per-store log line and event (Decision 3). The
  binding re-check leaves a small window between the store re-read and the Function write (Decision 2), closed by
  the next reconcile. KV write authorization stays by Function name (ADR-0073) until its follow-up, including a
  table whose owner still names a re-created Workflow's step Function.

## Open questions

None.

## References

Issue #149 (github.com/pyvvo/funcd); draft ADR-0170; ADR-0094; ADR-0073; ADR-0147. Code cited at 915f342 (ADR-0170
cites 1193be6); both are re-stamped to one commit at acceptance.
