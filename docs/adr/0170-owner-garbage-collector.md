# ADR-0170: Owner garbage collector — one platform collector deletes what a deleted owner owned

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0190](0190-run-bound-to-its-revision.md) (2026-10-05) — Decisions 1 and 5 wait while an open run pins the object; Scope 72-74: deleting a Workflow cancels its open runs.
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: controller, lifecycle, owner-references, garbage-collection, workflow, identity, site, function,
  resourcegroup, admission, cli, config
- **Realizes**: [FEAT-0000/F08](../feat/0000-feat-v1.md) (owned-object cleanup deferred by ADR-0003) and
  [FEAT-0000/F22](../feat/0000-feat-v1.md) (the `ResourceGroup` delete)
- **Supersedes (in part)**, each keeping its status with a `Superseded in part by: ADR-0170` back-link at acceptance:
  - [ADR-0094](0094-workflow-engine-core.md) lines 112–113 ("in-flight runs are unaffected (pinned at start)") and
    487 ("in-flight runs immune to re-push/spec edits"): a removed step's Function is deleted at once (Decision 5).
    Line 101 "(cascade delete)": a group delete cascades only with `--force` (Decision 8), as for blueprint 380, 960.
  - [ADR-0135](0135-managed-identity.md) Decision step 3 (lines 170–171): a third trigger, a Secret controlled by a
    namesake with another UID, is re-issued in place (Decision 4). Lines 298–301: such a Secret is no longer
    "foreign", and a name collision is refused before any write with reason `SecretNotOwned`.
  - [ADR-0139](0139-site-declarative-static-web-app.md) §5 (lines 232–235), Temporary-workarounds row 293, Review
    checklist 549, Consequences 565: the collector reclaims a deleted Site's Route (contracts unchanged). Row 296:
    Revisions are collected with their Function; retention stays out. §9 and row 294 stand.
- **Refines** [ADR-0024](0024-funcdcli-and-sdk.md) Contracts lines 113, 185 (additive): `sdk.Client.Delete` gains a
  variadic `DeleteOption`; composes with Proposed ADR-0172's read-only-kind refusal (its Decision 2).
- **Made true** (unchanged): ADR-0094 lines 109–111, 481; ADR-0135 lines 74–75, 104–105, 161–163, 275.
- **Settles**: [ADR-0003](0003-resource-model-and-api-typing.md) lines 195–197, 628 (`OwnerReferences` lifecycle;
  `Finalizers`, `DeletionTime` stay unused), 495; ADR-0024 lines 50, 165, 252, 291 (cascade is a server-side force).
- **Relates to**: ADR-0015 lines 122–125 (unchanged) · ADR-0064 · ADR-0080 · ADR-0142 · ADR-0143 Decision 2 (lines
  119–120: `retireStale` stops a deleted namesake's workers) · ADR-0153 Decision 4 and ADR-0154 Open question 1
  point at Decision 4 (either of 0153 and 0170 may land first) · Proposed ADR-0147 (conforms; both edit
  `storeHandlers`): its namespace admission lock lives in `deleteObjIf`, so force's member deletes take it · Proposed
  ADR-0163 (`controller` group; `runtime.supervisionPeriod` paces Decision 4's requeues) · Proposed
  [ADR-0172](0172-revision-integrity.md): (a) force's delete of an unowned Revision is its one API exemption (its
  Decision 1); (b) adopting a ref-less Revision stamps the controller ref (its Decision 5); (c) its fail-closed rule
  keys on the Function's own `status.currentRevision` (its Decision 3); (d) retention stays with ADR-0139 line 296.

## Context & Need

Four reconcilers stamp a controller `OwnerReference` (kind, name, UID) on five pairs (1193be6): Workflow → step
Function and `deletion: delete` KVStore, Identity → Secret, Site → Route, Function → Revision. Nothing deletes by it
(#66). Also: removed steps or kv entries leave their child; `ensureFunction`/`ensureKVStore` keep any same-named
object and a deleted owner's UID; `ensureSecret` reuses any same-named Secret, so a re-created Identity inherits its
namesake's credential; a delete is lost across a restart; a ResourceGroup delete removes only the group object.
Need: an owner's delete removes every object naming it by a controller ownerRef soon after, also across a crash,
never a live owner's child; a ResourceGroup is deleted only when empty, or with its members on force.

## Scenarios

- `scenario: workflow-delete-collects-steps-and-stores` — `gcwf` (warm `s1`, `s2`; `gcwf-state`, `deletion: delete`, with a key) deleted ⇒ within 5 s all three gone, `gcwf-s1` answers 404, the key reclaimed.
- `scenario: workflow-delete-keeps-retain-store` — `gcwf-keep` (`retain`) and its key remain; its table owner is a Function that no longer exists, so no call can write it.
- `scenario: removed-step-function-deleted` — re-applied without `s2` ⇒ within 5 s `gcwf-s2` gone (404), `gcwf-s1` ok.
- `scenario: removed-kv-store-kept-until-workflow-delete` — re-applied without `gcwf-extra` (`delete`) ⇒ it and its key remain until the Workflow is deleted; after that delete `gcwf-extra` no longer exists.
- `scenario: recreated-workflow-takes-new-uid` — children naming a deleted `gcwf` UID ⇒ re-applied `gcwf` stamps the new UID; two collector passes delete none.
- `scenario: workflow-refuses-anothers-child-or-users-function` — given `a` (step `b-c`, store `shared`) and API Function `u-s`: `b` declaring `shared` ⇒ `KVStoreNotOwned`; `a-b` (step `c`) or `u` (step `s`) ⇒ `FunctionNotOwned`; no ownerRef changes; after deleting `b`, `a-b`, `u`, the objects `a-b-c`, `u-s`, `shared` and its key remain.
- `scenario: identity-delete-collects-secret` — Identity `ext` deleted ⇒ within 5 s its Secret is gone.
- `scenario: identity-never-takes-anothers-secret` — `x` with `credentialSecretName` `y` (Identity `y`'s) or `u` (API) ⇒ NotReady `SecretNotOwned`; both Secrets keep ownerRefs and data.
- `scenario: recreated-identity-gets-fresh-credential` — `ext` deleted and re-applied before collection ⇒ new UID, different `secretAccessKey` and `catalogToken`; once the reconciler has re-issued the Secret the old key no longer authenticates (until then it resolves to the re-created Identity, ADR-0153 Decision 4).
- `scenario: site-delete-collects-route` — Site `web` deleted ⇒ within 5 s Route `web` gone, host 404, Bucket remains.
- `scenario: function-delete-collects-revisions` — `fn` (`fn-1`, `fn-2`) deleted ⇒ within 5 s no Revision remains.
- `scenario: recreated-function-retires-stale-workers` — `fn` (image A) deleted, its Revision collected, re-applied with image B before its reconciler sees the delete ⇒ image B serves every call; no image-A worker remains.
- `scenario: crash-before-collection-swept` — `gcwf` deleted on a file-backed platform with no collector running ⇒ the start sweep deletes `gcwf-s1`, `gcwf-s2`, `gcwf-state`.
- `scenario: live-owner-children-never-collected` — Ready Workflow, Identity, Site, Function ⇒ sweeps delete nothing.
- `scenario: resourcegroup-nonempty-delete-refused` — SDK `Delete` of `team` holding `a` ⇒ 409 naming `Function/a`, and both remain.
- `scenario: resourcegroup-force-delete-cascades` — `team` holding `gcwf` (with `gcwf-state`), Function `a`, ConfigMap `c`; `Delete` with `sdk.Force()` ⇒ succeeds; all, step Functions included, are gone.
- `scenario: resourcegroup-force-with-backlogged-sensor` — `team` holding Sensor `s` with a full queue to a stuck target; `Delete` with `sdk.Force()` ⇒ succeeds in one call; no Invocation of `s` remains in `team`.
- `scenario: resourcegroup-force-stops-at-protected-member` — `team` holding a non-empty Bucket, forced ⇒ 409 naming the Bucket; Bucket and `team` remain.
- `scenario: force-only-on-resourcegroup` — `funcdctl delete function a --force` fails before any request; `a` stays.

## Scope

In: the collector (`internal/gc`) for the five pairs with its watches, sweeps and config key; writers keeping only
their own children; pruning dropped step Functions; Revision deletion and stale-worker retirement; ResourceGroup
admission and force (API, SDK, CLI). Out: the Site `spec.bucket.deletion: delete` gate (ADR-0139 §9; its follow-on
adds a Site → Bucket pair); Revision/digest-prefix retention; finalizers and foreground deletion (`Finalizers`,
`DeletionTime`, `BlockOwnerDeletion` stay unread); a deleted Workflow's WorkflowRuns (retention reclaims closed ones;
an unstarted one waits with `WorkflowNotFound`; a running one keeps the spec pinned at start, ADR-0094 lines
112–113, and fails at its next dispatch to a collected step Function, as in Decision 5, its `onFailure` handler too
when that was a step); Namespace delete; non-controller ownerRefs; the Revision API.

## Constraints & Decision drivers

Never delete a live owner's child (UIDs are never reused; `store.Delete` takes a resourceVersion precondition;
ownerRefs are server-only). Crash-only (blueprint line 437). No ownerRef index (`List` decodes every object, and
decrypts Secrets); a full 64-event watch buffer drops a watcher. A mass delete is never reachable by a default, an env
var or a stray flag. Timers are config keys with defaults.

## Alternatives considered

- **Chosen (green-0-rabbit): B, one platform collector in background mode (event + sweep; Kubernetes, Nomad).**
  Rejected: A, per-reconciler delete on NotFound (four copies, lost across a crash); C, finalizers/foreground
  (changes every delete); sweeping every kind instead of `gc.Pairs()` (cost); skipping a live name's Revisions.
- **Chosen (green-0-rabbit): refuse a non-empty ResourceGroup delete; force deletes members.** Rejected: always
  cascade; force as a CLI loop (cannot sweep); excluding platform records (Invocations would outlive the group).
- Rejected: adopting any same-named child (two owners flip one child); using an unowned same-named Secret
  (rotation overwrites it, ADR-0135 lines 298–301).

## Decision

1. **The collector.** `internal/gc.Collector` runs beside the controller. A child is an object of a `gc.Pairs()`
   child kind with a `Controller: true` ownerRef; its owner is `Get(ref.Kind, ref.Namespace, ref.Name)` (the child's
   namespace when empty and namespaced): **live** when found with `ref.UID`, **dead** when NotFound or another UID. A
   dead-owned child is deleted with `store.Delete(…, child.ResourceVersion)`; NotFound counts as collected; a
   Conflict re-reads and re-judges once with a fresh owner Get; a second Conflict or any other error waits for the
   next sweep. Other ownerRefs are ignored. Owner judgments are memoized per pass only by the full ref (kind,
   namespace, name, UID). A failing `List` is logged, other kinds continue, errors return joined (`errors.Join`).
   Passes are stateless and may run concurrently.
2. **Triggers.** `Run` watches each owner kind on its own goroutine, which only records `Deleted` keys. A closed
   stream re-watches from its last resourceVersion; only on Unavailable does it re-watch without one and request a
   sweep. One worker sweeps once after the watches open, then drains the keys (rule 1 on their children), runs
   requested sweeps (coalesced) and sweeps every `controller.gcSweepInterval` (default `5m`); a child created after
   the drain waits for that sweep. A sweep is one pass over `store.ListOptions{}` (all namespaces). `Run` errors only
   when a start watch cannot open; funcd logs it.
3. **Cost.** A sweep: one `List` per child kind (5), at most one `Get` per distinct owner ref plus two per Conflict,
   one `Delete` per dead child; `CollectNamespace` costs the same. `pruneFunctions` adds one Function `List` per
   Workflow reconcile; a `…NotOwned` Workflow re-resolves image-step runtimes every supervision period (default
   10 s). `admission.Members` is one `List` per namespaced kind but ResourceGroup (21) per group delete and force
   pass; Invocations have no retention, so force can run long.
4. **Writers keep only their own children, with the current UID.** `ensureFunction` keeps a Function only when its
   controller ref names this Workflow's kind and name (any UID); `ensureKVStore` also keeps an unowned store (#149);
   both write the ownerRefs they built. Otherwise `Materialize` stops with no write, NotReady `FunctionNotOwned` or
   `KVStoreNotOwned`, requeued after the supervision period (`runtime.supervisionPeriod`, ADR-0163; 0 ⇒
   `controller.SupervisionPeriod`, 10 s). `ensureSecret` keeps a Secret whose controller ref names its Identity's
   kind and name: same UID as today; another UID ⇒ re-issue in place with the current ownerRef (minting the catalog
   token in the format then in force, ADR-0153's once it lands). Any other Secret, unowned included, is untouched:
   NotReady `SecretNotOwned`, requeued the same way. Site is unchanged (`RouteNotOwned` until collected; `site.MapRoute`).
5. **Workflow spec edits.** After `Materialize` steps 1–3 succeed, `pruneFunctions` deletes, with each
   resourceVersion, every Function controlled by this Workflow (kind, name, UID) that is no image step's
   materialized name; an in-flight run reaching a removed step fails it. KVStores are never pruned: a removed
   `delete` store is collected with the Workflow, a `retain` store never; re-adding re-adopts it with its data.
6. **Revisions** go with their Function by rule 1 (not a ref-less one: Temporary workarounds). `ensureRevision`, on
   its create path (no Revision of that name exists), first calls `retireStale` (retires every worker of the
   Function labelled with that Revision name), then creates; a crash between them retires again next pass. Needed
   because the collector can remove the Revision that `dropRevision` relied on to find a deleted namesake's workers
   (`internal/function/function.go:1294-1324`); invariant: no worker of a previous Revision of that name survives.
7. **ResourceGroup delete.** Validating admission `resourcegroup-deletion-protection` answers 409 while the group
   has a member: an object of a namespaced kind other than ResourceGroup, in its namespace, with
   `metadata.resourceGroup` = its name and no controller ownerRef. The message lists up to five `Kind/name` and the
   count. Platform-written records (a Sensor's WorkflowRuns and Invocations) count.
8. **Force.** `DELETE …/resourcegroups/{name}?force=true` checks in order: caller may delete the group (else 403);
   `controlplane.Deps.Collector` wired (else 503); the group exists (else 404). Then passes: list members, delete
   each via `deleteObjIf` with the listed resourceVersion, Sensors first so the Invocations their withdraw parks are
   listed by a later pass (per-member authorization and admissions, under ADR-0147's namespace lock taken and
   released per member, never across a pass; NotFound = gone), then `CollectNamespace`. On a 409 it re-Gets once
   and retries with the new resourceVersion if still a member; gone or moved is skipped; still refused waits a pass. Member protections are never bypassed. Any other
   error, `CollectNamespace`'s included, stops at once. A pass that deleted a member runs another; one that deleted
   none stops with its first 409, or else the admission's 409 (writes into the group are bounded once its Sensors
   are gone). A ref-less Revision is a member. With none left, the group is deleted normally.
   No rollback; a re-run continues. `force` exists only on this route; `sdk.Force()` is refused with `fault.Invalid`
   for any other kind before a request; `funcdctl delete <kind> <name> --force` passes it.
9. **Config.** `controller.gcSweepInterval` (`FUNCD_CONTROLLER_GC_SWEEP_INTERVAL`), positive Go duration, default
   `5m`; `0` or negative is refused naming the key. Whichever of this ADR and ADR-0163 lands first adds the group.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **A Revision with no controller ref is never collected** | `ensureRevision` adopts it without stamping a ref; force deletes it as a member; until Revisions are read-only, an API DELETE of a serving Revision makes `retireStale` stop its live workers | ADR-0172 stamps the ref on adoption and makes Revisions read-only |

## Contracts

| Consumes | Exposes |
|---|---|
| `store.Store` `Get`/`List`/`Delete`/`Watch`; config `controller.gcSweepInterval` | deletes of dead-owned children; NotReady reasons `FunctionNotOwned`, `KVStoreNotOwned`, `SecretNotOwned`; 409 on a non-empty ResourceGroup delete; `?force=true` (503 with no collector wired, 404 for a missing group); `sdk.Force()`; `funcdctl delete rg <name> --force` |

```go
// internal/gc/gc.go (new)
const DefaultInterval = 5 * time.Minute
type Pair struct{ Owner, Child v1.Kind }
// Pairs is every pair a reconciler stamps. Revision is last: one sweep collects a Function, then its Revisions.
func Pairs() []Pair {
	return []Pair{
		{Owner: v1.KindWorkflow, Child: v1.KindFunction}, {Owner: v1.KindWorkflow, Child: v1.KindKVStore},
		{Owner: v1.KindIdentity, Child: v1.KindSecret}, {Owner: v1.KindSite, Child: v1.KindRoute},
		{Owner: v1.KindFunction, Child: v1.KindRevision},
	}
}
// Store is required; Interval 0 ⇒ DefaultInterval, < 0 ⇒ fault.Invalid.
type Deps struct{ Store store.Store; Interval time.Duration; Logger *slog.Logger }
type Collector struct{ /* unexported */ }
func New(d Deps) (*Collector, error)
func (c *Collector) Run(ctx context.Context) error
// Empty ns is every namespace; List errors are returned joined; safe beside Run.
func (c *Collector) CollectNamespace(ctx context.Context, ns v1.NamespaceName) error

// internal/workflow/reconcile_workflow.go — ensureFunction (replacing line 283) refuses when !controlledBy,
// ensureKVStore when hasController && !controlledBy; both keep cur.ObjectMeta but write fn's own OwnerReferences.
func controlledBy(refs []v1.OwnerReference, wf *v1.Workflow) bool // controller ref naming wf's kind+name, any UID
func hasController(refs []v1.OwnerReference) bool
type notOwnedError struct{ reason, msg string } // Ready=False, requeue after the supervision period
// NewMaterializer and identity.ReconcilerDeps gain SupervisionPeriod time.Duration (new; 0 ⇒ controller.SupervisionPeriod);
// whichever of this ADR and ADR-0163 lands second wires runtime.supervisionPeriod to both.
func (m *Materializer) pruneFunctions(ctx context.Context, wf *v1.Workflow) error
// internal/services/identity/reconcile.go — !named ⇒ SecretNotOwned, no write; named && !sameUID ⇒ re-issue.
func secretControl(sec *v1.Secret, id *v1.Identity) (named, sameUID bool)
// internal/function/function.go — on ensureRevision's create path, before store.Create(rev).
func (r *Reconciler) retireStale(ctx context.Context, fn *v1.Function, rev v1.ObjectName) error

// internal/controlplane/admission/resourcegroup.go (new)
func Members(ctx context.Context, r StoreReader, ns v1.NamespaceName,
	group v1.ResourceGroupName) ([]v1.Object, error)
// Name "resourcegroup-deletion-protection", Validating, ResourceGroup Delete; fault.Conflict while Members non-empty.
func NewResourceGroupDeletionProtectionAdmission(r StoreReader) Admission
// internal/controlplane/handlers.go — the whole delete body: authorize → ADR-0147's namespace admission lock when
// admit.ReadsNamespace(gvk, Delete) → Get Old → Admit → store.Delete(rv) → unlock. deleteObj calls it with rv "" (no precondition; same lock rule).
func (h *storeHandlers) deleteObjIf(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName,
	rv string) error
// internal/controlplane/controlplane.go — Handlers (changed signature)
DeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, force bool) error
// internal/controlplane/routes.go — replaces namespacedDelete on deleteResourceGroup
type deleteResourceGroupInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
	Force     bool             `query:"force" doc:"delete the members first; their protections apply (ADR-0170)"`
}
// internal/controlplane/server.go — Deps gains (nil ⇒ force answers fault.Unavailable); NewStoreHandlers takes it.
Collector OwnerCollector
type OwnerCollector interface{ CollectNamespace(ctx context.Context, ns v1.NamespaceName) error }

// pkg/sdk/sdk.go (source-compatible, ADR-0024)
func (c *Client) Delete(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName,
	opts ...DeleteOption) error
type DeleteOption func(*deleteOptions)
func Force() DeleteOption // fault.Invalid for any kind but ResourceGroup
// internal/platform/config/config.go — defaults(): c.Controller.GCSweepInterval = "5m"
Controller struct {
	GCSweepInterval string `json:"gcSweepInterval,omitempty" env:"FUNCD_CONTROLLER_GC_SWEEP_INTERVAL"`
} `json:"controller,omitempty"`
// pkg/funcd/options.go — not the stores' WithValueLogGCInterval; 0 ⇒ gc.DefaultInterval
func WithGCSweepInterval(d time.Duration) Option
```

`examples/funcdconfig.yaml` documents the key, commented out, as `controller:` → `gcSweepInterval: 5m` with
"default: 5m". All new names were grepped and are unused.

## Implementation plan

1. `internal/gc/gc.go`; `pkg/funcd/funcd.go` builds it after the controller, runs `Run` beside it (error logged as
   "garbage collector stopped") and passes it to `controlplane.Deps.Collector`; `WithGCSweepInterval`;
   `cmd/funcd/main.go` `parseDuration("controller.gcSweepInterval", …, 0, false)`; config and example (covered by
   `TestIssue341_ExampleDocumentsEveryKeyWithDefault`).
2. Contracts' Workflow, Identity, Function changes. Drop "no collector exists" from
   `internal/site/reconcile.go:96` and `TestScenarioDeletedSiteReclaimsNothing` (assertion stays);
   `api/types/v1alpha1/site.go:147` says "(needs a drain-then-delete teardown, ADR-0139)".
3. Control plane: the admission, force handler, `deleteObjIf`, routes, stubs, server; `just generate`; `pkg/sdk`
   `DeleteOption`/`Force`; `funcdctl delete --force`.
4. Tests: `TestScenario` + scenario name in CamelCase, in `pkg/funcd/gc_e2e_test.go` (tag `e2e`), except
   `TestScenarioLiveOwnerChildrenNeverCollected` (`internal/gc`); `TestScenarioRecreatedWorkflowTakesNewUid`
   (`internal/workflow`, two `CollectNamespace` passes); `TestScenarioRecreatedFunctionRetiresStaleWorkers`
   (`internal/function`, fails without `retireStale`); `TestScenarioRecreatedIdentityGetsFreshCredential` and
   `TestScenarioIdentityNeverTakesAnothersSecret` (`internal/services/identity`, via `NewExternalKeys(…).Lookup`);
   `TestScenarioForceOnlyOnResourceGroup` (`cmd/funcdctl/cli_test.go`, plus `delete rg --force` reaching the
   route); a `-race` unit test in `internal/controlplane`: 40 forced deletes of a group holding Function `bN`, each
   racing a `PUT` of link `aN → bN`, store no link to a missing Function.
   `TestScenarioCrashBeforeCollectionSwept` uses a short data dir. Unit tests cover every Decision 1–2, 4, 5
   (including a step changed to a `function:` reference: its old Function is pruned) and 8 rule (`-race` for
   `CollectNamespace` beside `Run`); `TestPairsCoverEveryControllerRef` checks every controller-ref'd object over
   `v1.AllKinds()` is in `gc.Pairs()`; `BenchmarkSweep` (10 000 children, 1 000
   encrypted Secrets) records its results in the PR.
5. Done: every scenario's test passes; `just ci-full` and the Lima lanes green.
6. At acceptance: re-stamp the cited commit to the then-current main and re-check the cited lines; back-links in
   ADR-0094, ADR-0135, ADR-0139; blueprint 380, 960, F22's row and
   `docs/feat/0005-feat-workflow-engine.md:68` say a non-empty group delete is refused and `--force` deletes members.
7. Release note: the first start after upgrade collects every leftover of an owner deleted earlier, and re-issues
   the Secret of an Identity re-created over its namesake's Secret (new credential).

## Review checklist

- [ ] Decisions 1–9 hold as written; each scenario has its passing test.
- [ ] `TestPairsCoverEveryControllerRef` passes and `gc.Pairs()` matches every non-test `OwnerReferences` writer.
- [ ] Decision 4 refusals (`FunctionNotOwned`, `KVStoreNotOwned`, `SecretNotOwned`) perform no write.
- [ ] Force never bypasses a member's protections (per-member authorization and admissions apply).
- [ ] A forced member delete runs under ADR-0147's namespace lock (via `deleteObjIf`).
- [ ] `controller.gcSweepInterval` of `0` or a negative value is refused naming the key.
- [ ] No identity or absolute-path leak.

## Consequences

- Positive: #66 fixed for all five pairs (tracker #196 closes); no collection lost to a crash.
- Negative: children answer until collected; Workflows or Identities that took foreign children turn NotReady; a non-forced delete of a used group fails. The first start after upgrade collects every old leftover, including a store flipped `delete` → `retain` before 58c0296 (#275) under a Workflow deleted earlier; kept old-UID step Functions re-materialize (one cold start each); an Identity re-created over its namesake's Secret gets that Secret re-issued (old credential stops working).
- Risks accepted: the collector and `pruneFunctions` bypass API admissions (kvstore- and link-deletion-protection; never bucket protection). A `delete` store re-applied after collection may keep or lose its data (timing). A Workflow still adopts an unowned KVStore, so a principal with Workflow write but no KVStore delete can delete a user's or another Workflow's `retain` store with its keys. Sweep cost unmeasured until `BenchmarkSweep`. A re-created Identity's old credentials resolve to it until its Secret is re-issued. A stale in-flight Sensor attempt cut after a group delete can still park an Invocation naming the deleted group (as today).

## Open questions

None.

## References

Issues #66, #196, #149, #55, #328 (github.com/pyvvo/funcd); Kubernetes garbage collection (read 2026-10-04); Nomad
`job stop` and GC (read 2026-10-04). Code cited at 1193be6 (spot-checked unchanged at 5dbb7fb); re-stamp at acceptance.
