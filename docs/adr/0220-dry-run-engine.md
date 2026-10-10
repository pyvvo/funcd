# ADR-0220: Dry-run engine — admit a write without storing it

- **Status**: Implemented (2026-10-10; the implementation review passed on its second round, docs/reviews/adr-0220-implementation-claude-opus-5-5-2.md; accepted 2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: api, admission, dry-run, app, sdk, funcdctl
- **Realizes**: [FEAT-0010/F123](../feat/0010-feat-apps.md) (Dry-run engine)
- **Source**: Decision 19, the decisions-table rows "A dry run" and "Where the dry run runs", the scenarios
  `apply-dry-run-refused` and `app-deploy-dry-run`, the `DryRun()` and `AppPlan` contracts and the F123 review item
  of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0064](0064-fn-to-fn-rpc-links.md) (link validity) ·
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (bucket count) · [ADR-0005](0005-api-surface-code-first-huma.md)
  (huma routes) · [ADR-0219](0219-app-requirements.md) (answers its open question 2) ·
  [ADR-0212](0212-app-self-heal-and-pause.md) (F115 pause) · [ADR-0213](0213-app-config-and-secret-declarations.md)
  (F116 Secret check) · [ADR-0214](0214-app-hooks.md) (F117 hooks) ·
  [ADR-0217](0217-app-templates-values-and-rendering.md) (F120 `app deploy`, which leaves `--dry-run` to this ADR),
  all Proposed · ADR-0206 (the platform hold), ADR-0208 (the blob store), ADR-0210 (`If-Match`), all Accepted.
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md) (Implemented): `app-parts` runs unchanged in a dry
  run; the part-write predicate of its apply becomes a shared helper, with no change to a pass.
  [ADR-0200](0200-app-revisions.md) (Implemented): the stamp's compare-and-name becomes a shared helper; `AppStatus`
  gains `plan`, set only in a dry-run answer; `app rollback` gains `--dry-run`.
- **Extends (additive)**: [ADR-0018](0018-api-server-authn-rbac-admission.md) and
  [ADR-0063](0063-admission-framework.md) (Implemented): one branch at the store call of `createObj` and
  `replaceObjIf`; authorization, `Admit` and the pipeline are unchanged.
  [ADR-0147](0147-atomic-admission-and-nested-call-cap.md) (Implemented): in a dry run the lock span ends with
  admission, since no store write follows; the App plan is computed after it.

## Context & Need

funcd has no dry run (the only one, `funcd install --print`, ADR-0056, is unrelated). `funcdctl apply` validates
each document offline (`cmd/funcdctl/cli.go:137-175`), but the refusals that read the store, such as `bucket-count`
or `link-validity`, show only at the real write.

**Purpose.** One server engine answers what a create or a replace would do: sent with `?dryRun=true`, the write runs
its whole path up to the store call and answers the object as it would be stored, or the refusal the real write
would get; for an App the answer also carries the plan. Callers: `sdk.Client.Apply` with `sdk.DryRun()`,
`funcdctl apply --dry-run`, `funcdctl app rollback --dry-run` and `funcdctl app deploy --dry-run`.

## Scenarios

Fixture: ADR-0200's App `todo` with Route `todo-legacy`, `todo-4` latest and current; ADR-0080's Bucket cap per
namespace set to 1; `alice` may create and update every kind in `default`, `bob` may only get.

- `scenario: apply-dry-run-refused` — `default` holds one Bucket; `funcdctl apply --dry-run -f bucket.yaml` with a
  new Bucket ⇒ fails with the real apply's text, `admission.bucket-count: namespace "default" already holds the
  maximum 1 Buckets`, naming the document; the Bucket list is unchanged.
- `scenario: app-apply-dry-run` — `funcdctl apply --dry-run -f todo.yaml` with a new `todo-api` image and
  `todo-legacy` dropped ⇒ prints `would apply App/todo`, `revision todo-5`, `update Function/todo-api`,
  `prune Route/todo-legacy`; no object and no AppRevision is written (every `resourceVersion` unchanged, `app
  history todo` ends at `todo-4`).
- `scenario: app-deploy-dry-run` — `funcdctl app deploy ./app --name todo -f values/prod.yaml --dry-run` in
  `default`, ADR-0217's to-do template in a version that renders only the new `todo-api` image and drops `todo-legacy`
  ⇒ the same lines, then `hook todo-migrate` once F117 is built (ADR-0214); nothing is written and nothing is awaited.
- `scenario: app-dry-run-unchanged` — the stored `todo` spec sent with `--dry-run`, and with F115 the same spec with
  `paused: true` ⇒ prints `no change`; the answer's `status.plan` has no revision, no parts and no hooks.
- `scenario: app-rollback-dry-run` — `funcdctl app rollback todo 1 --dry-run` ⇒ prints `revision todo-5` and
  `update Function/todo-api`; nothing is written.
- `scenario: dry-run-stores-nothing` — `alice` POSTs a valid new Function `f` with `?dryRun=true` ⇒ the answer is
  `f` with an empty `uid` and `resourceVersion`; `GET` answers 404. A dry-run `PUT` of a changed Function `g` with no
  `uid` ⇒ the answer has the new spec and `g`'s stored `uid` and `resourceVersion`; `GET` still returns the old spec.
- `scenario: dry-run-store-refusals` — a dry-run `POST` of Function `g`, which exists ⇒ 409 `store.Create: Function
  "g" already exists`; a dry-run `PUT` of an absent `h` ⇒ 404, as the real writes.
- `scenario: dry-run-forbidden` — `bob` sends any dry-run create or replace ⇒ 403, as the real write; no plan.
- `scenario: dry-run-unsupported` — against a test server whose OpenAPI declares no `dryRun`, `funcdctl apply
  --dry-run -f bucket.yaml` ⇒ fails with `sdk.Apply: the server does not support dryRun` and sends no write; on funcd,
  `DELETE` of Function `g` with `?dryRun=true` ⇒ 422 `unknown query parameter` and `g` remains.

## Scope

**In**: a `dryRun` query parameter on every create and replace route of a writable kind (52 routes today), namespaced
and cluster-scoped; huma's 422 for an undeclared query parameter on every write operation; the engine in `createObj` and
`replaceObjIf`; the App plan; `sdk.DryRun()` with its server check; `--dry-run` on `funcdctl apply`, `app rollback` and
`app deploy`. **Out**: a dry run of delete, of the KV handover (`internal/controlplane/kvhandover.go:58`) and of other
imperative endpoints (each answers 422 to `dryRun`); a `sdk.Create` option; showing a pause or a wait in the plan (open
question 1); a manifest whose documents see each other.

## Constraints & Decision drivers

The real write and the dry run share every step but the store call, so a refusal cannot drift (Decision 19).
Admissions are deterministic and side-effect-free (`internal/controlplane/admission/admission.go:36-67`), so running
them without a write is safe. The plan uses the reconciler's own rules (Decision 19), through helpers shared with the
pass, not a copy. Nothing is stamped, written or called, and a server that does not know the flag must not turn a dry
run into a real write. The control plane depends on a seam, not on the App package: `internal/controlplane` does not
import `internal/app`, as with the `OwnerCollector` seam (`internal/controlplane/server.go:39-44`). No new verb and no
new dependency.

## Alternatives considered

| Option | Lost because |
|---|---|
| `dryRun=All`, as Kubernetes | funcd has one mode; a bool matches `force=true` (`internal/controlplane/routes.go:152`) |
| A `/plan` endpoint per kind | one more route per writable kind and a second path that can drift from the write; Decision 19 sends a create or a replace |
| A `dryRun` argument on each create and replace `Handlers` method | every fake and `StubHandlers` change; the caller's identity already travels in the context (`internal/controlplane/middleware/authn.go:27`) |
| The plan in a response header or a dry-run-only body | a header is size-bounded and untyped; huma outputs are typed per operation (`Body v1.App`), so a body that depends on a query breaks the OpenAPI schema and the SDK decode |
| A dry-run create without a `Get` | misses the 409 the store gives for a taken name (`internal/store/store.go:318-321`) |
| Skip the ADR-0147 lock (nothing is written) | a second code path; the lock keeps admission's reads one consistent step against marked writes |
| Hold the lock through the App plan | the reconciler stamps and writes parts without it (`stamp`, `apply` in `internal/app`), so it buys no consistency and lets an App dry run stall marked writes |
| Trust the server to refuse an unknown `dryRun` | huma ignores an undeclared query parameter by default, so a server without this ADR stores the write |
| `RejectUnknownQueryParameters` on every operation | only a write turns an ignored flag into harm; reads keep accepting unknown parameters as today |
| A recording store under the reconciler's `write` | `write` mutates the stored object by reflection (`internal/app/reconcile.go:283-299`); a shared predicate is smaller |
| Apply a manifest's earlier documents in an overlay | an overlay store under every admission; Decision 19 admits each write against the store |

## Decision

1. **Wire.** Each create (`POST`) and replace (`PUT`) route of a writable kind declares `dryRun`, a bool query parameter
   (`?dryRun=true`). Every write operation, delete and the imperative `POST`s included, sets huma's
   `Operation.RejectUnknownQueryParameters`, so one that does not declare `dryRun` answers 422 `unknown query parameter`
   instead of writing (huma v2.38.0 `huma.go:803-829`; `newFaultError` keeps the status,
   `internal/controlplane/controlplane.go:328-357`), the status of ADR-0108's unknown field; reads are unchanged. The
   route puts the flag into the request context; the `Handlers` methods keep their signatures. Read-only kinds keep
   their 405. A later write route (ADR-0214's `…/retry`, ADR-0216's `…/test`, ADR-0206's `…/hold/release`, DR-10's
   backup kinds) follows the same rule, which a test checks over the registered operations and `v1.AllKinds()`.
2. **Engine.** `createObj` and `replaceObjIf` (`internal/controlplane/handlers.go:92-126`, `:192-240`) run every step
   unchanged: `authorize` with `VerbCreate` or `VerbUpdate` (no dry-run verb: a caller without the write right gets
   403), the generated name, `refuseMigrationRecord`, the path checks, the ADR-0147 lock, the `Old` fetch, ADR-0210's
   version check, the KVStore guard, `Admit` (the built-in `validate`, then the daemon's admissions with `app-parts`,
   `pkg/funcd/funcd.go:1101`), `withStatus` and `withServerMeta`. In a dry run the store call is replaced: a create
   `Get`s its name and, when found, answers the store's refusal, `fault.Conflict` `store.Create: <Kind> "<name>"
   already exists`; a replace already answered `NotFound` from its `Old` fetch. Any refusal from a step before the
   store call comes back unchanged, such as ADR-0219's `app-requires` refusal on an apply or a rollback (its open
   question 2). The platform hold (ADR-0206 Decision 6) gates the runners through `hold.Gate` (`Held()`), not the API,
   so it refuses neither the write nor its dry run. Nothing is stored or called, ADR-0208's blob store included.
3. **Answer.** The admitted object with the HTTP status of the real write. The dry-run branch sets the fields that
   only the store transaction sets, as that transaction would: a replace copies `uid`, `generation` and
   `creationTimestamp` from `Old` (`internal/store/store.go:406-408`), beside the `resourceVersion` of
   `handlers.go:238`; a create clears them and `resourceVersion` (`store.go:328-331`). So a create answers them empty,
   with a name generated from `generateName` that is not reserved, and a replace answers the stored ones, whatever
   `uid` the body carried. The answer does not predict a generation bump or a coalesced write (ADR-0047).
4. **Lock.** A dry run takes the ADR-0147 namespace lock exactly as the real write (`lockFor`, `handlers.go:47-55`)
   and releases it once admission and the answer of Decision 3 are done; an App's plan is computed after.
5. **App plan.** For an App, after the lock is released, the handler calls the injected `AppPlanner`
   (`Deps.Planner`; nil ⇒ a dry-run App write answers `fault.Unavailable`, as a nil `Collector`). `internal/app`
   implements it with the reconciler's rules and only reads the store:
   - **Revision**: the latest AppRevision of the answer's UID (none for a create); `AppRevisionName(name, n+1)` when
     none exists or the spec the stamp compares differs from its `spec.spec`, empty otherwise, by the stamp's own
     helper (`internal/app/revision.go:55-103`); with F115 that spec is `WithoutPause()` (ADR-0212 Decision 3), so a
     pause alone names no revision.
   - **Parts**, in section order (`entries`, refs skipped): `create` when absent, `update` when the predicate `write`
     uses holds (spec JSON, owner references or resource group differ); an equal part is not listed.
   - **Stop**: as the pass does, before any write (`revision.go:82-83`, `reconcile.go:250-258`). An AppRevision namesake
     held by another owner gives an empty `revision` and `parts` holding only it (`create`, ChildNotOwned). Otherwise
     `revision` is as above (the stamp runs first) and `parts` holds only the first stop: the first part in section
     order that exists and is not this App's (`update`, ChildNotOwned), else, with F116, the first declared Secret
     missing or lacking a key, by ADR-0213 Decision 8's check as a shared helper (`Secret/<name>`, no action,
     `SecretNotFound` or `SecretKeyMissing`; it fails the revision at ADR-0212 Decision 6's deadline, ADR-0206's
     `ReleasedAt` term included). No other part is listed.
   - **Prune**: every object `dropped` returns (`reconcile.go:385`, `gc.Pairs` order), as `prune`. ADR-0200 holds it
     until the switch, and `prune` keeps a `RunHeld` or `InUse` part.
   - **Hooks**: ADR-0214 Decision 9's list (pre-hooks, then post-hooks), empty without `revision` or F117.
   The plan says what the rollout does, not when or in how many passes. A pass runs, in order: the platform hold
   (ADR-0206 Decision 6, placed first by ADR-0214 Decision 8), the pause (ADR-0212 Decision 4), the requirements, the
   stamp and its wait (ADR-0219 Decision 3), ADR-0199's ownership check, ADR-0213's Secret check, the pre-hooks, apply,
   the switch, the post-hooks and prune (ADR-0214 Decision 4). The hold, the pause and the wait delay the rollout and
   are not shown (open question 1); the first passes write only the pre-hooks' parts. The plan travels in
   `status.plan`, set only by the dry-run branch, and is never stored (`withStatus` drops a client's status,
   `handlers.go:255-270`); it is advisory under concurrent writes.
6. **SDK.** `Apply(ctx, obj, opts ...ApplyOption)`; `DryRun()` appends `dryRun=true` to every request `Apply` sends:
   the `PUT`, the `POST` it falls back to on a 404, and the `POST` for a `generateName` object
   (`pkg/sdk/sdk.go:85-118`). Before its first dry-run write, the client reads the served OpenAPI document once
   (`OpenAPIPath` in `NewAPI`) and checks that the operation declares `dryRun`; when it does not, `Apply` writes
   nothing and fails, since an older server ignores the flag. `Create` and `Delete` are unchanged.
7. **CLI.** `funcdctl apply --dry-run` keeps the offline pre-flight and the document order, sends each document with
   `sdk.DryRun()` against the store as it is, stops at the first refusal naming the document, as `apply`, and prints
   `would apply <Kind>/<name>`, then for an App the plan lines (Contracts). `funcdctl app rollback --dry-run` passes
   `sdk.DryRun()`, keeps ADR-0210 Decision 4's re-read on a 409 and prints the same; an equal spec still reports no
   change. `funcdctl app deploy --dry-run` renders and fills the App as ADR-0217 Decision 8 does (the stored
   `spec.paused` included), sends it with `sdk.DryRun()` even when the spec is equal, prints the same lines and waits
   for nothing; it exits 0, or 1 on a refusal. It needs ADR-0217's `deploy`: if F120 is built after this ADR, the flag
   and `app-deploy-dry-run` land with F120.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/app.go (additive); AppStatus gains, set only in a dry-run answer and never stored:
//	Plan *AppPlan `json:"plan,omitempty"`
type AppPlan struct {
	Revision ObjectName `json:"revision,omitempty"` // empty for an unchanged spec
	Parts    []PlanPart `json:"parts,omitempty"`
	Hooks    []string   `json:"hooks,omitempty"` // ADR-0214 Decision 9
}
type PlanPart struct {
	Kind   Kind       `json:"kind"`
	Name   ObjectName `json:"name"`
	Action PlanAction `json:"action,omitempty"` // empty on a Secret stop
	Reason string     `json:"reason,omitempty"` // ChildNotOwned, SecretNotFound or SecretKeyMissing: where a pass stops
}
type PlanAction string

const (
	PlanCreate PlanAction = "create"
	PlanUpdate PlanAction = "update"
	PlanPrune  PlanAction = "prune"
)

// internal/controlplane
type AppPlanner interface {
	PlanApp(ctx context.Context, app *v1.App) (v1.AppPlan, error) // reads the store, writes nothing
}

// Deps gains Planner AppPlanner; NewStoreHandlers gains a last parameter planner AppPlanner.
func withDryRun(ctx context.Context, on bool) context.Context
func dryRunFrom(ctx context.Context) bool

// each create and replace route input of a writable kind gains:
//	DryRun bool `query:"dryRun" doc:"admit the write and store nothing (ADR-0220)"`
// every write operation sets huma.Operation.RejectUnknownQueryParameters = true.

// internal/app
func NewPlanner(st store.Store) *Planner
func (p *Planner) PlanApp(ctx context.Context, a *v1.App) (v1.AppPlan, error)

// pkg/sdk
type ApplyOption func(*applyOptions)

func DryRun() ApplyOption
func (c *Client) Apply(ctx context.Context, obj v1.Object, opts ...ApplyOption) (v1.Object, error)
```

`funcdctl` plan lines, two-space indented under `would apply App/<name>`: `revision <name>`, then
`<action> <Kind>/<name>` per part (`stop` with no action, ` (<reason>)` when set), then `hook <name>` per hook;
`no change` when the plan has no revision and no parts.
The nil-planner refusal: `controlplane.dryRun: no App planner is wired`. The unsupported-server refusal:
`sdk.Apply: the server does not support dryRun`. No config key and no new reason.

| Consumes | Exposes |
|---|---|
| the admission pipeline · store `Get`/`List` · the ADR-0147 lock · `internal/app` planning helpers · the served OpenAPI | `dryRun` on every create and replace route (REST, OpenAPI) · 422 for an undeclared query parameter on a write · `AppStatus.plan`, `AppPlan`, `PlanPart` · `sdk.DryRun`, `ApplyOption` · `--dry-run` on `funcdctl apply`, `app rollback`, `app deploy` |

## Implementation plan

1. **API**: the types in `api/types/v1alpha1/app.go` with JSON tests; `internal/controlplane/dryrun.go` (context
   helpers); the `dryRun` field and `withDryRun` in the create and replace closures of `routes.go` and `routes_rest.go`;
   `RejectUnknownQueryParameters` on every write operation; the branch in `handlers.go`; `Deps.Planner` and
   `NewStoreHandlers` in `server.go`; `just generate`.
2. **Planner**: `internal/app/plan.go`; the stamp's compare-and-name, `write`'s predicate and, with F116, the Secret
   check into helpers the pass and the planner share; `pkg/funcd/funcd.go` passes `app.NewPlanner(store)` (`:1100`).
3. **Clients**: `pkg/sdk/sdk.go` (`ApplyOption`, `DryRun`, the OpenAPI check); `cmd/funcdctl/cli.go` (`--dry-run` on
   apply, the plan printer); `cmd/funcdctl/app.go` (`--dry-run` on rollback and, with F120, on `deploy`).
4. **Tests**: `internal/controlplane`: a table over every writable kind with a store wrapper that counts writes: a
   dry-run create and replace write nothing and answer the admitted object with Decision 3's store-set fields, including
   the stored `uid` on a replace whose body has none and an empty `uid` on a create whose body carries one; a taken name
   gives the store's 409 text; an absent replace 404; the KVStore guard's refusal; 403 without the write right; Decision
   1's operations test, with no fixed count; a write route answers 422 to an undeclared `dryRun`, and the existing write
   callers send no undeclared parameter; a dry run of a marked kind waits for the namespace lock and plans after
   releasing it; a nil planner answers 503. `internal/app`: for an App with no pre-hook, no unmet requirement and every
   Secret present, one `Reconcile` pass writes exactly the parts the plan lists as `create` or `update` without a
   reason; a missing Secret or key (revision named, only that line, no part written); a pre-hook (all parts and the hook
   listed, the first pass writes only the pre-hook's parts); an unchanged spec, also paused; create (`<app>-1`, all
   `create`); a part held by another owner, not first in section order (only that part is listed, the revision is still
   named, the pass writes no part); an AppRevision namesake held by another owner (no revision, only the AppRevision is
   listed) versus one of another UID; prune list; the planner writes nothing; the reconciler's tests pass unchanged.
   `pkg/sdk`: `dryRun=true` on the `PUT`, the fallback `POST` and the `generateName` `POST`; no write when the OpenAPI
   lacks `dryRun`. `cmd/funcdctl`: the printed lines, the stop at the first refusal, rollback, deploy. `pkg/funcd`:
   `TestScenarioApplyDryRunRefused`, `TestScenarioAppApplyDryRun`, `TestScenarioAppDeployDryRun`,
   `TestScenarioAppDryRunUnchanged`, `TestScenarioAppRollbackDryRun`, `TestScenarioDryRunStoresNothing`,
   `TestScenarioDryRunStoreRefusals`, `TestScenarioDryRunForbidden`, `TestScenarioDryRunUnsupported`.
5. **Done**: `just ci` and `just ci-full` green; a passing test per scenario (`app-deploy-dry-run` with F120, Decision
   7); no `go.mod` change.

## Review checklist

- [ ] Every write operation sets `RejectUnknownQueryParameters` and only the create and replace operations of writable
      kinds declare `dryRun`, by a test with no fixed count; a `DELETE` with `?dryRun=true` gets 422.
- [ ] `createObj` and `replaceObjIf` branch on the dry run only at the store call; the steps before it are shared.
- [ ] The write-counting test covers every writable kind and sees zero store writes, App and AppRevision included.
- [ ] A dry-run create of a taken name answers the store's 409 text; a replace of an absent name answers 404.
- [ ] `authorize` uses `VerbCreate`/`VerbUpdate` in a dry run; no new `auth.Verb` exists.
- [ ] `internal/controlplane` does not import `internal/app`; a nil `Deps.Planner` answers 503 for an App only.
- [ ] Only the dry-run branch sets `status.plan`, after the lock is released; the pass and `PlanApp` call the same
      helpers.
- [ ] `sdk.Apply` with `DryRun()` sends `dryRun=true` on the `PUT`, the fallback `POST` and the `generateName`
      `POST`, and writes nothing when the served OpenAPI lacks `dryRun`; `Create` is unchanged.
- [ ] Each Scenario has a test of the same name, and none of them sees a `resourceVersion` move.

## Consequences

**Positive**: a store-reading refusal shows before the write, through one path, so it matches the real one; a deploy's
changes show before it starts; every kind gains it, with or without an App; a write never ignores `dryRun`.
**Negative**: `apply --dry-run` admits each document against the store as it is, so a link to a Function created earlier
in the same manifest is refused in the dry run, and a quota counts only stored objects; a dry run of a marked kind holds
the namespace lock through admission, so many dry runs slow marked writes in that namespace; a client's first dry run
reads the OpenAPI document; a dry-run create shows no store-set field and an unreserved generated name.
**Risks accepted**: the plan is advisory: a concurrent write can change the revision number or the parts, and a listed
prune can be held or kept at prune time; a dry run that passes does not reserve anything.

## Open questions

1. Whether the plan should show that a pass would wait (`SpecPaused`, `RequirementNotMet`, ADR-0206's platform hold)
   → the decider, at acceptance (ADR-0212 Decision 10 and ADR-0219 Scope leave it here).

## References

- [App design note](../reports/app-design.md) (Decision 19, scenarios, contracts) · [FEAT-0010](../feat/0010-feat-apps.md)
  · `internal/controlplane/handlers.go:22-55`, `:92-126`, `:192-296` · `internal/controlplane/admission/pipeline.go:30-89`
  · `internal/app/admission.go:56-96` · `internal/app/revision.go:55-103` · `internal/app/reconcile.go:178-299`,
  `:385-466` · `internal/store/store.go:314-334`, `:398-412` · `pkg/sdk/sdk.go:85-231`.
