# ADR-0214: App lifecycle hooks — pre-apply and post-apply calls, recorded and retried

- **Status**: Accepted (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, hooks, lifecycle, rollout, invocation
- **Realizes**: [FEAT-0010/F117](../feat/0010-feat-apps.md) (App lifecycle hooks)
- **Source**: the [App design note](../reports/app-design.md) (decider, 2026-10-06/07): Decision 13, the decisions-table
  rows on hooks and the upgrade order, the Lifecycle table, `HookFailed`, the hook scenarios, contracts and checklist
  line, and open questions 2, 8 and 9.
- **Relates to**: [ADR-0109](0109-sensor-event-action-binder.md) (Sensor invoker) · Proposed ADR-0212, ADR-0213,
  ADR-0219, ADR-0220 (F115, F116, F122, F123) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (wake on call) ·
  [ADR-0090](0090-mandatory-single-io-schema.md) (input contract) · [ADR-0151](0151-external-invoke-deadline.md)
  (`spec.timeout`) · [ADR-0118](0118-eventing-dead-letter-queue.md) (imperative seam) ·
  [ADR-0136](0136-roles-and-role-assignments.md) (writer roles) · [ADR-0206](0206-restore-and-held-boot.md) (hold) ·
  [ADR-0210](0210-api-optimistic-concurrency-if-match.md) (`If-Match`) · the planned DR-10 workload-backup ADR
- **Builds on**: [ADR-0199](0199-app-resource.md) and [ADR-0200](0200-app-revisions.md) (Implemented); an App without
  `spec.hooks` that is not held behaves exactly as they define; Decision 7 takes the `retry` ADR-0200 Decision 6 reserved.
- **Supersedes in part (back-links at acceptance)**: ADR-0199 Decisions 4-6 and ADR-0200 Decisions 4-7, for an App with
  hooks (Decision 4: pre-hook parts first, the switch after the pre-hooks, prune after the post-hooks, the deadline's
  `endTime` term, `HookFailed`, `Applied=False` `Progressing`, a `Degraded` App, a reopened `Failed` revision). The held
  pass is ADR-0206 Decision 6's, which supersedes them in part for a held platform; this ADR supersedes nothing for it.
- **Extends (additive)**: [ADR-0170](0170-owner-garbage-collector.md) (Implemented): pair `(AppRevision, Invocation)`.

## Context & Need

**Purpose.** ADR-0200 rolls a changed spec out part by part, but a schema migration must run before the new code serves,
and a data migration or a cache warm-up after it; today that is done by hand (the design note's workaround, a one-step
`funcdctl workflow run`, which exits here). An App names its own Functions that the reconciler calls once before its
parts change and once after the new revision is current, on install, upgrade and rollback. Each call is recorded on the
AppRevision; a failure stops or degrades the rollout, and `funcdctl app retry` resumes it.

## Scenarios

Fixture: ADR-0200's App `todo` with `todo-3` current at `version: 3.0.0`; every revision also has a `functions` entry
`todo-migrate`, bound to table `todos` of KVStore `todo-store` (owner `todo-api`), and `hooks.preApply` naming it; a
RolesAssignment beside the App grants `todo-migrate` `KV Data Writer` on that table (ADR-0136). Config defaults.

- `scenario: app-pre-hook-migrates` — `todo-api` gets a new image and `version: 4.0.0` ⇒ `todo-4` is stamped; the App
  writes `todo-migrate`, waits until it is `Ready` or `NotStarted`, and calls it once with data `event: upgrade`, `app:
  todo`, `from: todo-3`, `to: todo-4`, `fromVersion: 3.0.0`, `toVersion: 4.0.0`; an Invocation owned by `todo-4` records
  the call, which `todo-4`'s `status.hooks` lists as `Ready`; only then does `todo-api` get a new `resourceVersion`. The
  handler rewrites keys through `context.kv` with the RolesAssignment; `todo-4` becomes current.
- `scenario: app-pre-hook-fails-then-retry` — the call answers 500 ⇒ no part except `todo-migrate` and the stores it
  binds gets a new `resourceVersion`; `todo-4` is `Failed` with `Applied=False` `HookFailed` naming the Invocation; the
  App is `Failed` with `Ready=False` `HookFailed`; `todo-3` stays current and serves. After the cause outside the spec
  is fixed, `funcdctl app retry todo` calls `todo-migrate` again; `todo-4` lists two calls (`Failed`, `Ready`), returns
  to `Deploying` and becomes current. A second `app retry todo` fails with "app todo has no failed hook".
- `scenario: app-post-hook-fails` — `todo-4` also has `hooks.postApply` naming `todo-warm`, drops Route `todo-legacy`,
  and `todo-warm` fails ⇒ `todo-4` is current and `Ready`; `todo-legacy` stays as a child `Pruning`; the App is
  `Degraded` with `Ready=False` `HookFailed` naming the Invocation. `funcdctl app retry todo` calls `todo-warm` again;
  once it succeeds, `todo-legacy` is gone within 5 s and the App is `Ready`.
- `scenario: app-hook-event` — a first install writes `todo-store`, then calls `todo-migrate` with `event: install` and
  no `from`; from `todo-4` current, `funcdctl app rollback todo 3` stamps `todo-5`, whose call has `event: rollback`,
  `from: todo-4`, `to: todo-5`, `toVersion: 3.0.0`.
- `scenario: app-hook-repeats-after-restart` — funcd stops while `todo-migrate` runs, longer than `app.upgradeTimeout` ⇒
  no Invocation records that call; after the restart the App calls it again, and the rollout continues.

## Scope

**In**: `spec.hooks`, the hook input, the call and its record, the gates of Decision 4, `funcdctl app retry`.
**Out**: delete, backup and restore hooks and a platform client (open questions 1 to 3); pause, requirements and the dry
run (F115, F122, F123), which keep Decisions 8 and 9; hooks through a Workflow or a goja script (feat).

## Constraints & Decision drivers

No Workflow, engine or run store in a hook's path (decider: a hook must not depend on a component that can degrade); the
reconciler keeps no state between passes (`internal/app/reconcile.go:43-44`), so what a hook did lives in the read-only
AppRevision; a call never blocks the controller worker, which every kind shares (`controller.New` without `Workers`,
`pkg/funcd/funcd.go:790`; one worker by default, `internal/controller/controller.go:85-87`); every time read through
`Deps.Clock` (ADR-0200); no shim change, no new dependency, no new config key.

## Alternatives considered

| Option | Lost because |
|---|---|
| Call inline in `Reconcile` | a hook may run up to 1 h (`v1.MaxInvokeTimeout`) and would stall every reconciler on the shared worker |
| Reuse the Sensor's invoker instance | its client has a fixed 30 s timeout (`pkg/funcd/funcd.go:848`); the workflow dispatcher's no-timeout client with a context deadline fits (`:980-986`) |
| Retry through an App spec field | any spec change stamps a new AppRevision (`internal/app/revision.go:55-70`), so `todo-4` could never become current; excluding a field changes ADR-0200 Decision 3 |
| Retry through an annotation, `tags` or the AppRevision | `ObjectMeta` has no annotations (`api/types/v1alpha1/metadata.go:135-151`); `tags` are the user's labels; the AppRevision is read-only (ADR-0200 Decision 2) |
| Rollback marker written by `app rollback` | a second writer, which ADR-0200 Decision 9 rejected; no field carries it |
| `from` read from the App status at call time | after the switch the previous revision is gone from the App status, and history may have trimmed it |
| A deterministic Invocation name, or one written before the call | an AppRevision name already reaches 63 characters (ADR-0200 Decision 1); a `Running` phase the kind does not use, for a call that is repeated anyway |

## Decision

1. **Spec and admission.** `spec.hooks.preApply` and `spec.hooks.postApply` list entries with one key, `function`.
   `App.Validate` refuses an entry that names no `functions` entry of this App, or a `ref` entry
   (`spec.hooks.preApply[0].function "x" is not a function of this App`), a name twice in one list (one name may be in
   both), and a pre-hook Function that links to a `functions` entry that is neither a `ref` nor a pre-hook (`… links to
   "todo-api", written after the pre-hooks`): admission passes it (ADR-0199 Decision 3), but its `context.invoke` would
   reach no Function on an install and the old spec on an upgrade. A hook Function is an ordinary part.
2. **The input, fixed at the stamp** (ADR-0200 open question 1). When the new spec has hooks, the stamp sets the new
   AppRevision's `spec.hookInput`, frozen with it: `app`; `to` its name and `toVersion` its `spec.version`; `from` the
   App's `status.currentRevision` and `fromVersion` its `status.version`, both empty on an install, a failed install
   included. `event` is `install` when `from` is empty; `rollback` when the new spec equals, by the stamp's
   `json.Marshal` comparison, the `spec.spec` of a retained `Ready` AppRevision of this App; else `upgrade`. Only the
   latest revision switches, so `from` is still current while its pre-hooks run; its post-hooks get the same input.
3. **Hook state.** For the latest revision and one point, each entry, in list order, is *done* when `status.hooks` holds
   a `Ready` call of that point and function, *failed* when its last call is `Failed`, else *due*.
4. **The pass.** Before it reads the revisions, a pass notes whether the runner holds a call for this App (`busy`), so no
   call recorded after that read starts again; one App's passes never overlap (`internal/controller/queue.go:17`, `:54`).
   - *Pre-hooks*, while the latest revision is not current and its pre-hooks are not all done: after ADR-0199's
     ownership check over every part and F116's Secret check (ADR-0213 Decision 8), apply writes, in section order,
     what a pre-hook Function needs, directly or through a catalog it binds: the `configMaps` its `spec.config` names
     and the `kv`, `buckets` and `catalogs` entries it binds; then the pre-hook Functions, and no other part: a
     Function waits on a missing store (`internal/function/references.go:70-78`) and fails on a missing ConfigMap
     (`ConfigResolveFailed`). This refines design-note Decision 13 and its Lifecycle "Pre-hooks" row ("before any
     other write" becomes "before any other serving part"), for the decider. While the latest is `Deploying`, a pass
     that was not stopped (a stopped pass may leave the hook Function `Ready` at the old spec; the switch requires the
     same, `revision.go:123`), wrote no part and is not `busy` starts the first not-done entry's call when it is due
     and its Function is `Ready` or `NotStarted` (`judgeFunction`, `status.go:94-118`; `NotStarted` per ADR-0200's
     decider ruling; the call wakes it, ADR-0033). A `Failed` latest calls a hook only through `retry` (Decision 7),
     as `settle` changes only a `Deploying` one (`revision.go:117`). The App is `Deploying`; both show `Progressing`.
   - *A failed pre-hook*: the first not-done entry is failed ⇒ the revision is `Failed` with `Applied=False` and
     `Current=False` `HookFailed`, and the App `Failed` with `Ready=False` `HookFailed`. Unlike a timeout, this is not
     final: once a retry records a `Ready` call, the revision is `Deploying` again with `Current=False` `Progressing`.
   - *Apply, wait and switch*: once the pre-hooks are done, ADR-0200 Decisions 4 to 6 hold. The last recorded `preApply`
     call's `endTime` of the latest revision is a start term of ADR-0212 Decision 6's deadline, beside the hold's
     `ReleasedAt` (ADR-0206 Decision 6). A pass that is `busy` or that starts a pre-hook call fails no revision by it,
     so a call cut by a restart runs again. At the deadline, a due first not-done pre-hook whose Function is neither
     `Ready` nor `NotStarted` makes the revision `Failed` `HookFailed` (`Function/<fn>: not ready`), which `retry`
     reopens. The pass that switches returns `Requeue`.
   - *Post-hooks*, once the stored App status names the latest revision current (the switch landed): a pass that is not
     `busy` starts the first not-done entry's call when it is due. A failed entry blocks the later ones and makes the
     App `Degraded` with `Ready=False` `HookFailed`, ranked after a stopped pass's reason and before a part's; the
     revision stays current and `Ready`, its conditions unchanged (ADR-0200 Decision 5). No App deadline applies.
   - *Prune* also requires every post-hook done; until then a dropped object is a child `Pruning` with no reason.
5. **The call** is one `Invoke` through an `Invoker` with the Sensor's signature (`internal/sensor/sensor.go:55-59`): a
   second `sensor.HTTPInvoker`, which wakes the target, built with `workerClient(calls, 0)`. The context deadline is the
   hook Function's `spec.timeout`, or `invoke.defaultTimeout` when unset (ADR-0151). The CloudEvent has `specversion`
   `1.0`, `id` the Invocation name (Decision 6), `source` `funcd://<ns>/app/<app>`, `type` `preApply` or `postApply`,
   `time` from `Deps.Clock`, `datacontenttype` `application/json` and `data` the revision's `spec.hookInput`, which the
   shim checks against the hook Function's contract (ADR-0090, ADR-0058). An `Invoke` error or timeout fails the call.
6. **The runner** runs each call in a goroutine, at most one per App (`Start` refuses a second). It draws the Invocation
   name `inv-<20 hex>` before the call; the goroutine drops a call whose revision is no longer the latest. When a call
   ends it creates the Invocation as `sensor.record` does (`internal/sensor/sensor.go:481-501`), with the AppRevision's
   controller reference and `Deps.Clock` times, appends an `AppHookCall` to that AppRevision's status at its
   `resourceVersion` (re-read on `Conflict`), releases the App and calls `Deps.Enqueue`. Nothing is recorded when the
   AppRevision is gone, a store write fails, or funcd's shutdown ended the call: the entry stays due and the next pass
   calls it again, so a hook must be safe to repeat. The read-only `AppHookCall` is the trusted record; the Invocation
   is writable over REST (`internal/controlplane/handlers.go:1279-1320`).
7. **Retry.** `funcdctl app retry <app> [-n ns]` posts to `/retry` under the App's item path; the control plane
   authorizes an update of the App (`auth.VerbUpdate`) and calls the reconciler's `Retry`, as the DLQ replay route calls
   its `Replayer` (`internal/controlplane/deadletters.go:17-23`, `:94-103`); a POST action, it is outside ADR-0210's
   `If-Match` (the runner writes at the version it read, Decision 6). As a pass notes `busy`, `Retry` reserves the App
   in the runner before it reads the latest revision, and releases it when it starts nothing. It starts, without
   waiting, the first not-done pre-hook of a revision `Failed` with `HookFailed`, or the current revision's first failed
   post-hook; the passes continue the rollout. Otherwise it answers the Contracts' errors or `NotFound`. A revision
   `Failed` by its deadline gets `Conflict`: `retry` re-runs failed hooks only (design note), and only a new stamp
   starts a new attempt (ADR-0200 Decision 6). A retry cut by a restart leaves the entry failed.
8. **Hold and pause.** The held pass is ADR-0206 Decision 6's: `Reconcile` first asks `Deps.Hold.Held()` (ADR-0206's
   inline `Hold` interface, which `serve` fills with the `hold.Gate` through `funcd.WithHold`; nil means never held,
   and the first of ADR-0206/0212/0214/0216 to land adds the field, ADR-0212 Decision 8) and, while held, returns `RequeueAfter: SupervisionPeriod` with status
   untouched, before F115's paused check (ADR-0212 Decision 4). The runner starts no call, for a pass or a `Retry`,
   while `Held()` or the re-read App is paused (once `spec.paused` exists; `app.go:31-33` has none). A running call
   completes within its `spec.timeout`; its goroutine re-reads both and records nothing while either stops the App
   (design note: a paused App writes nothing), so the entry keeps its state: a due one is called after the resume or
   the release, a retried one needs another `retry` (Decision 7). The hold begins only at a boot (ADR-0206), so no
   call runs as it starts; a call unrecorded at a backup's cut is due again once that generation is restored.
9. **With F122 and F123.** The requirements step comes before the pre-hooks; no hook is called for a revision waiting on
   a requirement (ADR-0219). The dry run calls no hook; `AppPlan.Hooks` (ADR-0220) lists the hook Function names in call
   order, pre-hooks then post-hooks (a name in both lists twice), empty when no revision would be stamped.
10. **No delete hook, no platform client** (open questions 1 and 3). F117 adds nothing to a hook's context (`kv`,
    `blob`, `invoke`, `log`) and does not change the shims. A binding grants read only: a hook writes a table or a
    prefix only as its owner or through a writer-role RolesAssignment (ADR-0136), which an App cannot declare yet
    (design-note open question 5), so a migration of another part's table needs one applied beside the App.

## Temporary workarounds

- A backup before an upgrade is taken by hand, not by a hook. Exit: the DR-10 workload-backup ADR (open question 1).

## Contracts

```go
// api/types/v1alpha1/app.go (additive)
type AppSpec struct {
	Hooks *AppHooks `json:"hooks,omitempty"` // after ADR-0199's sections
}
type AppHooks struct {
	PreApply  []AppHook `json:"preApply,omitempty"`
	PostApply []AppHook `json:"postApply,omitempty"`
}
type AppHook struct {
	Function ObjectName `json:"function"` // a functions entry of this App, not a ref
}
type AppHookInput struct {
	Event       string     `json:"event"` // install | upgrade | rollback
	App         ObjectName `json:"app"`
	From        ObjectName `json:"from,omitempty"`
	To          ObjectName `json:"to"`
	FromVersion string     `json:"fromVersion,omitempty"`
	ToVersion   string     `json:"toVersion,omitempty"`
}

// api/types/v1alpha1/apprevision.go (additive, after ADR-0200's fields)
type AppRevisionSpec struct {
	HookInput *AppHookInput `json:"hookInput,omitempty"` // set by the stamp when spec.hooks is set, frozen (Decision 2)
}
type AppRevisionStatus struct {
	Hooks []AppHookCall `json:"hooks,omitempty"` // every call, retries included, in call order
}
type AppHookCall struct {
	Point      string     `json:"point"` // preApply | postApply
	Function   ObjectName `json:"function"`
	Invocation ObjectName `json:"invocation"`
	Phase      Phase      `json:"phase"`   // Ready | Failed
	EndTime    Timestamp  `json:"endTime"` // ADR-0196; a start term of the deadline (Decision 4)
}

// internal/app (additive)
type Invoker interface { // *sensor.HTTPInvoker satisfies it
	Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error
}

// Deps gains Invoker (nil ⇒ every call fails Unavailable "no invoker"), InvokeTimeout time.Duration
// (invoke.defaultTimeout; 0 ⇒ v1.DefaultInvokeTimeout) and Enqueue func(controller.Request); Deps.Hold: ADR-0206's inline Hold (nil ⇒ never held).
func (r *Reconciler) Retry(ctx context.Context, ns v1.NamespaceName, app v1.ObjectName) error

// internal/controlplane (new)
type AppRetrier interface{ Retry(ctx context.Context, ns v1.NamespaceName, app v1.ObjectName) error }
// POST …/apps/{name}/retry, retryApp; sets RejectUnknownQueryParameters, as ADR-0220 asks of every write operation
func RegisterAppRetry(api huma.API, retrier AppRetrier, authz auth.Authorizer)

// pkg/sdk (new)
func (c *Client) RetryApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error

// internal/gc: Pairs() gains (AppRevision, Invocation) right after (App, AppRevision).
```

| Reason | On | When |
|---|---|---|
| `HookFailed` (new) | App `Ready=False` (phase `Failed` or `Degraded`); AppRevision `Applied=False`, `Current=False` (pre-hook only) | the last call of a hook failed or timed out, or a pre-hook Function was not ready at the deadline; message `Function/<fn>: Invocation/<inv>: <error>`, or `Function/<fn>: not ready` |
| `Progressing` (reused) | App `Ready=False`, AppRevision `Applied=False` | a pre-hook is not done; message `pre-hook Function/<name>` |

`Retry` errors: `Conflict` ("app <app> has no failed hook", "a hook call of app <app> is running", "app <app> is
paused"), `Unavailable` (held). No new config key: hooks read `invoke.defaultTimeout` and `app.upgradeTimeout`.

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, Function, Invocation · `Invoker` (activator wake, ADR-0033) · `Deps.Clock`, `Deps.Enqueue` · `Deps.Hold` (ADR-0206's inline `Hold` interface; `serve` passes the `hold.Gate`) · the config above | `spec.hooks`, `spec.hookInput`, `status.hooks` · hook CloudEvents · Invocations owned by AppRevisions · `POST …/apps/{name}/retry`, `sdk.Client.RetryApp`, `funcdctl app retry` · the GC pair |

## Implementation plan

1. **Types**: the Contracts in `api/types/v1alpha1/app.go` (with Decision 1's `Validate` refusals) and `apprevision.go`,
   with tests; `just generate`.
2. **Server**: `internal/app/hooks.go` (hook state, runner, CloudEvent, record, `Retry`), `revision.go`, `status.go` and
   `reconcile.go`; `internal/gc/gc.go`; `internal/controlplane/appretry.go` (as `RegisterDeadLetters`); `pkg/funcd` (the
   second `sensor.HTTPInvoker`, `app.Deps` with `Enqueue: ctrl.Enqueue` and `Hold` when not yet added, the route); `pkg/sdk`; `cmd/funcdctl/app.go`
   (`retry`, added to the group's `Short` beside the other ADRs' subcommands, not replacing them).
3. **Tests**: `internal/app` unit tests on `clock.NewManual` and a fake `Invoker`, per rule of Decisions 2 to 8, among
   them: the event rule (install, failed install, upgrade, rollback, a rollback beyond history reads `upgrade`); an
   install whose pre-hook binds an App-declared store or names an App ConfigMap, and an upgrade adding a table it binds,
   all call it and switch; a stopped pass (`ChildNotOwned`, a missing Secret) starts no call; a restart or a failed
   record write after a call longer than `upgradeTimeout` calls it again and the rollout continues; a hook Function not
   ready past the deadline gives a retryable `HookFailed`; a revision failed by its deadline whose stop is later cleared
   calls no hook; postApply [A, B] with A failed calls no B until a retry of A succeeds; two concurrent `Retry`s start
   one call; a call recorded after the `busy` read is not started again; a held pass, a paused App's too, writes
   nothing, and a call ending while paused records nothing and runs after the resume; ADR-0200's tests pass
   unchanged without hooks. `Validate`, a pre-hook linking to `todo-api` refused; the `gc` pair order; the route; the
   CLI; one `TestScenarioApp…` per scenario in `pkg/funcd`, the restart one on a restarted platform.
4. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; no `go.mod` or language-module change.

## Review checklist

- [ ] `App.Validate` refuses a hook naming no `functions` entry, a `ref` entry, or a name twice in one list, and a
      pre-hook linking to a non-`ref` entry other than a pre-hook.
- [ ] Before its pre-hooks are done a pass writes only the pre-hook Functions and the ConfigMaps and stores they need,
      never switches, and calls a hook only for a `Deploying` latest in a pass not stopped, writing no part, not `busy`.
- [ ] A failed pre-hook leaves the revision `Failed` `HookFailed` and `currentRevision` unchanged; a failed post-hook
      keeps the revision current, skips prune, makes the App `Degraded` `HookFailed` and blocks the later post-hooks.
- [ ] Every recorded hook call is an Invocation with the AppRevision's controller reference and an `AppHookCall` in its
      status, a `Conflict` retried, not dropped; a shutdown-ended call writes nothing.
- [ ] `internal/app` imports no Workflow, engine or run-store package and reads time only through `Deps.Clock`; no
      `Invoke` runs on the controller worker; at most one call per App; only the stamp sets `spec.hookInput`.
- [ ] `Retry` starts only a failed hook of the latest revision and answers `Conflict` otherwise.
- [ ] `Reconcile` asks `Deps.Hold.Held()` first, before the paused check, and a held pass writes nothing (ADR-0206);
      no hook call starts or is recorded while held or paused; `gc.Pairs()` holds `(AppRevision, Invocation)` after
      `(App, AppRevision)`; no shim, `go.mod` or config key changes.

## Consequences

**Positive**: migrations run inside the platform, ordered against the parts they need, debugged as any Function call;
the reconciler stays stateless; a retry resumes the same revision instead of stamping a new one.
**Negative**: a second invoker and a goroutine per running call; an imperative route beside the CRUD routes; the hook
Functions run the new spec while the revision is `Failed`; a hand edit of another part waits for the pre-hooks.
**Risks accepted**: an interrupted call runs twice; a rollback beyond `app.revisionHistory` reads as `upgrade`, an older
Ready spec re-applied by hand as `rollback`; `status.hooks` grows per retry; a fixed hook image is a new stamp; a
revision failed by its deadline needs a spec change or a rollback; a store a pre-hook binds takes the new spec first.

## Open questions

1. A hook that creates a DR `Backup`: a context member (a language-module release, ADR-0141) or a namespace-scoped
   credential (the API takes only config-file bearer tokens, `internal/controlplane/middleware/authn.go:60-100`), and
   its identity → the DR-10 workload-backup ADR (design-note open question 8); ADR-0205 and ADR-0208 leave it out.
2. A hook point around a backup or a restore → the DR work (open question 9); a later ADR may extend F117 with it.
3. A hook before a delete (open question 2) → later, with finalizers (ADR-0170), if a case needs it.

## References

- `internal/app/reconcile.go:170-228` · `revision.go:110-171`, `:222-242` · `internal/sensor/invoker.go:28-86`.
