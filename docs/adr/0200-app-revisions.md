# ADR-0200: App revisions — safe upgrade, history and rollback

- **Status**: Implemented (2026-10-08)
- **Superseded in part by**: [ADR-0206](0206-restore-and-held-boot.md) (2026-10-10) — Decisions 3, 6, 7 and 8 for a held rollout.
- **Superseded in part by**: [ADR-0212](0212-app-self-heal-and-pause.md) (2026-10-10) — Decisions 3, 4, 6, 8 and 9: pause and the deadline.
- **Superseded in part by**: [ADR-0213](0213-app-config-and-secret-declarations.md) (2026-10-10) — Decision 5: the Secret reasons.
- **Superseded in part by**: [ADR-0214](0214-app-hooks.md) (2026-10-10) — Decisions 4-7 for an App with hooks.
- **Superseded in part by**: [ADR-0219](0219-app-requirements.md) (2026-10-10) — Decisions 3-6: startedAt waits for the requirements.
- **Date**: 2026-10-08 (self-accepted under adr-batch after a five-finder grounding, three judge rounds and two confirm
  judges; the main session made an unset `app.upgradeTimeout` default non-breaking)
- **Deciders**: green-0-rabbit
- **Tags**: app, revisions, lifecycle, rollout, rollback, config
- **Realizes**: [FEAT-0010/F114](../feat/0010-feat-apps.md) (App revisions: safe upgrade, history and rollback)
- **Source**: Decisions 1 (AppRevision), 4 (stamp), 7 (rollout record), 8 (failure), 10 (history and rollback), the
  `app.*` config keys and open question 7 of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0020](0020-function-contract-lifecycle.md), [ADR-0172](0172-revision-integrity.md) (the Revision
  model) · [ADR-0143](0143-redeploy-by-revision-switch.md) (switch) · [ADR-0190](0190-run-bound-to-its-revision.md) ·
  [ADR-0174](0174-never-booted-revision-is-unknown.md) · [ADR-0163](0163-retry-times-in-config.md),
  [ADR-0194](0194-api-duration-strings.md) (durations) · [ADR-0196](0196-utc-millisecond-timestamps.md) (timestamps)
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md) (Accepted): App phase `Failed`; `Deploying` runs from
  a stamp, made before its ownership check, to the switch or `Failed`; prune also waits for the switch and for a pass
  that wrote no part; three `AppStatus` fields; its no-timeout workaround exits. Its apply and readiness are unchanged.
- **Extends (additive)**: [ADR-0170](0170-owner-garbage-collector.md) (Implemented): the pair `(App, AppRevision)`.

## Context & Need

ADR-0199 makes an App converge to its spec, but nothing records which spec ran, a part that never becomes Ready
leaves the App `Deploying` forever, and there is no way back.

**Purpose.** `AppRevision` is the App's history and rollout record: one per changed spec, current once every part is
Ready within `app.upgradeTimeout`; `funcdctl app history` reads it and `app rollback` re-applies an earlier spec.

## Scenarios

Fixture: ADR-0199's App `todo` with `version: 1.0.0` plus `routes` `todo-legacy` (`/legacy` → `todo-api`), installed:
`todo-1` is current. Config defaults unless a scenario sets a key.

- `scenario: app-reapply-same-spec` — `todo` applied again unchanged ⇒ no AppRevision is stamped or written, no part
  gets a new `resourceVersion`, `latestRevision` stays `todo-1`.
- `scenario: app-upgrade` — `functions[0].image` and `version: 2.0.0` changed ⇒ `todo-2` stamped, App `Deploying`
  with `currentRevision` `todo-1` until `todo-api`'s new Revision serves; then current `todo-2`, `version` `2.0.0`,
  App and `todo-2` `Ready`, `todo-1` `Current=False` `Replaced` and still `Ready`; calls answer from the new image.
- `scenario: app-one-part-changes` — only `workflows[0]`'s step image changes ⇒ `todo-2` holds the whole spec; the
  App writes Workflow `todo-plan` alone, whose materializer writes `todo-plan-due`, whose new Revision switches
  (ADR-0143); no other part gets a new `resourceVersion` or restarts; a WorkflowRun started before the change
  finishes on the Revision it pinned (ADR-0190).
- `scenario: app-prune-after-current` — `todo-legacy` dropped and `todo-api`'s image changed ⇒ while `todo-2` is
  `Deploying` the Route stays as child `Pruning` `NotCurrent`; it is gone within 5 s after `todo-2` is current.
- `scenario: app-failed-upgrade-keeps-serving` — from `todo-2` current, with `app.upgradeTimeout: 20s`,
  `runtime.bootTimeout: 10s` and `invoke.activationTimeout: 5s`, `todo-api` gets an image that never starts ⇒ 20 s
  after the stamp `todo-3` is phase `Failed` with `ChildrenReady=False` `ChildNotReady` naming `Function/todo-api`;
  App phase `Failed`, `Ready=False` `ChildNotReady`; `currentRevision` stays `todo-2`; calls answer from its image.
- `scenario: app-rollback` — then `funcdctl app rollback todo 2` ⇒ `todo-4` is stamped with `todo-2`'s spec and
  becomes current; `app history todo` lists 1 to 4: version, phase (`Ready`, `Ready`, `Failed`, `Ready`), stamp time.
- `scenario: app-upgrade-superseded` — a second image change while `todo-2` is `Deploying` ⇒ `todo-3` stamped;
  `todo-2` `Failed` with `Current=False` `Superseded` naming `todo-3`; `todo-3` becomes current.
- `scenario: app-history-kept` — `app.revisionHistory: 2`, five changes until `todo-6` is current ⇒ only `todo-4`,
  `todo-5` and `todo-6` remain; `funcdctl app rollback todo 1` fails naming `todo-1` and writes nothing.
- `scenario: app-revision-read-only` — `POST`, `PUT` or `DELETE` of an AppRevision answers 405 with `Allow: GET`;
  `funcdctl apply -f` of a file holding one, or `funcdctl delete apprevision todo-1`, fails with "AppRevision is
  read-only: the App reconciler writes it"; nothing changes.
- `scenario: app-upgrade-timeout-config` — `app.upgradeTimeout: 1m` with `runtime.bootTimeout` at its `1m` default ⇒
  funcd does not start, naming `app.upgradeTimeout` and `runtime.bootTimeout`.

## Scope

**In**: the kind `AppRevision` and all that Decisions 1 to 10 add. **Out**: hooks (F117); pause, the self-heal record
and `retry` (F115, F117); `configMaps` (F116); tests (F119); templates, `app deploy` and `app delete` (F120, F121);
`requires`, which moves the timeout start (F122); dry run (F123); retention of Function Revisions and Sites (left to
a platform retention ADR, as ADR-0139 deferred it); automatic rollback (decider).

## Constraints & Decision drivers

Follow Function → Revision (ADR-0020, ADR-0172): platform-stamped, frozen, read-only, a namesake dropped; keep
ADR-0199's apply and readiness; a deadline that survives a restart (`AddAfter` lives in memory,
`internal/controller/controller.go:286-288`); every time read through an injected clock; a repeated pass writes
nothing (ADR-0047); config durations through `parseDuration` (ADR-0194); no new dependency.

## Alternatives considered

| Option | Lost because |
|---|---|
| Number = `metadata.generation`, as a Revision's (`internal/function/function.go:2209`) | two applies before one pass leave a gap; the note numbers from the latest AppRevision |
| Rollback as a server endpoint or a `spec.rollbackTo` field | a second writer of the user's App spec; a client re-apply runs the App admission again with the caller's rights, as `workflow pause` writes a run (`cmd/funcdctl/workflow.go:221-277`) |
| A part Ready after the timeout makes the `Failed` revision current | the feat row says a timed-out upgrade "stops"; the outcome would depend on when a part recovers; a spec change starts a new attempt (decider) |
| Stop writing parts after `Failed` | the failed spec is still the App's spec; ADR-0199 Decision 4 converges every pass, and a hand edit would stay unseen |
| Record each Function's resolved digest in the AppRevision | a second source of truth beside the spec; a template pins digests (F120) |
| Deadline from `creationTimestamp` or a condition's `lastTransitionTime` | both read the wall clock (`internal/store/store.go:331`, `api/types/v1alpha1/status.go:35-49`); F122 must move the start |

## Decision

1. **The kind.** `AppRevision` is namespaced and has a status. `spec.app` names the App, `spec.number` its number and
   `spec.spec` is a frozen copy of the App spec. Its name is `<app>-<number>`: with ADR-0199's 52-character App name
   it fits a DNS label up to 9999999999, the schema maximum, so no hashed form exists. It carries the App's
   controller reference (kind, name, UID), namespace and resource group. Only the App reconciler writes it; spec and
   metadata never change after create, and its status goes through `store.Update`, skipping an unchanged write, as
   `writeHeld` (`internal/function/function.go:2076`). `Validate` checks the envelope, name and number, not the spec.
2. **Read-only API**, as Revision (ADR-0172 Decisions 1, 2): only `listAppRevisions` and `getAppRevision` (plural
   `apprevisions`); `POST`, `PUT` and `DELETE` answer 405 problem+json with `Allow: GET`; no `PATCH`.
   `sdk.ReadOnlyKind` covers both kinds; the new `sdk.ReadOnlyWriter` names each kind's writer in the refusal
   ("AppRevision is read-only: the App reconciler writes it"). The `funcdctl apply -f` pre-flight, which hard-codes
   the Function reconciler today (`cmd/funcdctl/cli.go:155-157`), takes it from there; the Revision text is unchanged.
3. **Stamp.** The latest AppRevision is the stored one with the highest `spec.number` whose controller reference names
   this App's UID. Every pass first compares `json.Marshal` of the App's typed `spec` byte for byte with the latest's
   `spec.spec`, as `specChanged` does (`internal/store/store.go:620-645`): an omitted and an empty section are equal,
   and an entry moved within a section is a change. When they differ, or none exists, it creates `<app>-<n+1>` (`n`
   the latest number, 0 for none) with phase `Deploying` and `status.startedAt` = `NewTimestamp(clock.Now())`
   (ADR-0196) and sets `status.latestRevision`. ADR-0199's ownership check runs only then: an AppRevision is not a
   part, so the check still precedes every part write, and a pass it stops has stamped (ADR-0199's
   `app-child-not-owned` writes no part, only `todo-1`). Numbers only grow, with no gap; a spec equal to an older
   revision's (a rollback) gets a new number. A stored `<app>-<n+1>` whose controller reference names this App's kind
   and name with another UID (a deleted App's, not yet collected) is deleted at its `resourceVersion` and stamped
   afresh, as `dropRevision` (ADR-0172 Decision 5); any other owner stops the pass with `ChildNotOwned` naming it.
4. **Apply** stays ADR-0199 Decision 4: every pass converges the parts to the App spec, the latest revision's, and
   writes only the parts that differ, so an unchanged part keeps its generation, Revision and workers, and a changed
   Function switches by itself (ADR-0143). Parts switch one by one; the App does not judge which versions work
   together (decider). This holds after `Failed`: its parts stay written and a hand edit is written back.
5. **The rollout record** is the AppRevision status: phase `Deploying`, then `Ready` or `Failed`; a replaced one keeps
   `Ready`. `Applied` and `ChildrenReady` are written only while it is the latest and `Deploying`; then, of the
   conditions this ADR defines, only `Current` changes. A True condition has no reason, as `RevisionReady`
   (`internal/function/function.go:1067`).
   - `Applied`: True once a pass wrote or found equal every owned part; False with `ChildNotOwned` or `ChildInvalid`
     naming the part when a pass stopped.
   - `ChildrenReady`: ADR-0199 Decision 5's `Ready` value over the parts: True, Unknown `NotStarted`, or False naming
     the first Pending part with `Progressing` or `RefNotFound`; False `ChildNotReady` once the timeout passed.
   - `Current`: True while it is `status.currentRevision`; else False with `Progressing` (deploying), `ChildNotReady`
     (failed, kept when a newer one is stamped), `Superseded` (it was `Deploying` when a newer one existed; the
     message names it) or `Replaced` (a newer one became current).
6. **Switch, timeout and App phase.** The App's `status.currentRevision` is the source of truth. Every pass, a stopped
   one too, derives the record from it and the highest number, over the AppRevisions Decision 8 lists: `latestRevision`
   is the highest number; an older `Deploying` revision turns `Failed` with `Current=False` `Superseded`; in a pass that
   was not stopped and wrote no part, the latest switches when it is neither current nor `Failed` and no declared part
   is Pending (`NotStarted` counts as settled, decider). A pass that wrote a part read it at its old generation, before
   the write, so it does not switch; the part's watch requeues the App (ADR-0199 Decision 4). The current one is `Ready`
   with `Current=True`; any other with `Current=True` turns `Current=False` `Replaced`. A switch sets `currentRevision`
   and `status.version` (its `spec.version`) and writes the App status before any AppRevision status, so a pass that
   fails between writes converges on the next one. Short of a switch, once the clock reads `time.Time(startedAt)` +
   `app.upgradeTimeout`, the latest turns `Failed` with `ChildrenReady=False` `ChildNotReady`, its message the first
   Pending part and its reason, or the stop reason of a stopped pass; a pass that sees both switches. Short of the
   deadline the pass returns `RequeueAfter` the time left (at least 1 ms), or a prune requeue if earlier; after a
   restart the deadline is read again from `startedAt`. A part Ready later neither switches nor clears a `Failed`
   revision; only a new stamp (a spec change or a rollback), or F117's `retry`, starts a new attempt; nothing rolls back
   by itself. The App phase is `Deploying` while the latest revision is `Deploying`, or while none exists (the stamp
   stopped: ADR-0199's stop reason on `Ready`, no deadline); `Failed` while it is `Failed`, with `Ready=False`
   `ChildNotReady` and its message unless the pass stopped (then ADR-0199's stop reason); once it is current, ADR-0199
   Decision 5 decides (`Ready`, `Degraded`). `ChildNotReady` is for `Failed` and `Degraded` only. `currentRevision` and
   `version` stay empty until the first switch, so a failed install is `Failed` without them.
7. **Prune** (ADR-0199 Decision 6) also requires that the latest revision is current and the pass wrote no part.
   Before that a dropped object is a child `Pruning` with reason `NotCurrent`; a `Failed` revision never prunes. Prune
   lists only section kinds, so it never deletes an AppRevision.
8. **History.** Each pass, a stopped one too, deletes, at its `resourceVersion`, every AppRevision of this App's UID
   that is neither current nor among the newest `app.revisionHistory` others by number. Revisions of every phase
   count, and the latest is always kept, since the minimum is 1. On App delete the GC collects them through the
   pair `(App, AppRevision)` (Contracts).
9. **CLI.** F114 creates the `funcdctl app` group with only `history` and `rollback`, each with `-n/--namespace`
   defaulting to `default` (`nsOrDefault`, `cmd/funcdctl/workflow.go:373`). `app history <app> [-o json]` lists the
   AppRevisions whose `spec.app.name` is `<app>` (and, while the App exists, whose controller UID is its UID) by
   number: `REVISION`, `VERSION`, `PHASE`, `STAMPED` (`creationTimestamp`, ADR-0196). `app rollback <app> <n>` takes
   a positive integer, gets `<app>-<n>` (absent: fails naming it and `app.revisionHistory`), refuses one whose
   controller UID is not the App's, and applies the App with `spec` set to its `spec.spec` (`sdk.Client.Apply`), so
   the admission runs again with the caller's rights; the last writer wins, as for any apply
   (`internal/controlplane/handlers.go:234`). An equal spec is reported as no change. No rollback marker is recorded.
10. **Config.** `app.upgradeTimeout` (a duration string, ADR-0194) and `app.revisionHistory` (an integer). A set
    `app.upgradeTimeout` must be more than `runtime.bootTimeout` (open question 7 of the note), or funcd refuses to
    start, checked in `pacing()` beside the other orderings (`cmd/funcd/main.go:611-617`) and in `funcd.WithPacing`.
    Unset, it is the larger of `5m` and twice `runtime.bootTimeout`, so one boot retry fits, as an unset
    `eventing.deliveryBackoffMax` follows its initial value (`:607-609`); a config without Apps never fails on it.

## Temporary workarounds

- A rollback re-resolves image tags, so a moved tag runs other bytes than the revision ran. Exit: F120's template
  lock, which writes digests (`@sha256:`) into the spec.

## Contracts

```go
// api/types/v1alpha1/apprevision.go
type AppRevision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AppRevisionSpec   `json:"spec"`
	Status     AppRevisionStatus `json:"status,omitempty"`
}
type AppRevisionSpec struct {
	App    ObjectRef `json:"app"`
	Number int64     `json:"number" minimum:"1" maximum:"9999999999"`
	Spec   AppSpec   `json:"spec"` // the frozen copy (Decision 1)
}
type AppRevisionStatus struct { // phase Deploying | Ready | Failed; conditions Applied, ChildrenReady, Current
	Status    `json:",inline"`
	StartedAt *Timestamp `json:"startedAt,omitempty"` // ADR-0196: app.upgradeTimeout starts here
}

func (r *AppRevision) GroupVersionKind() GroupVersionKind
func (r *AppRevision) Validate() error // envelope, name == AppRevisionName(spec.app.name, spec.number), number bounds
func (r *AppRevision) GetStatus() *Status
func AppRevisionName(app ObjectName, n int64) ObjectName // "<app>-<n>"

// api/types/v1alpha1/app.go (additive; phase Deploying | Ready | Degraded | Failed)
type AppStatus struct {
	Status          `json:",inline"`
	CurrentRevision ObjectName `json:"currentRevision,omitempty"`
	LatestRevision  ObjectName `json:"latestRevision,omitempty"`
	Version         string     `json:"version,omitempty"` // spec.version of the current revision
	Children        []AppChild `json:"children,omitempty"`
}

// pkg/sdk
func ReadOnlyKind(k v1.Kind) bool    // Revision, AppRevision
func ReadOnlyWriter(k v1.Kind) string // new: "the Function reconciler", "the App reconciler", "" if writable

// pkg/funcd (additive)
// Pacing gains AppUpgradeTimeout time.Duration (zero ⇒ max(5m, 2×BootTimeout)); WithPacing refuses a non-zero
// value at or below the effective BootTimeout: "Pacing.AppUpgradeTimeout %s must be more than BootTimeout %s".
func WithAppRevisionHistory(n int) Option // fault.Invalid unless 1 <= n <= 100

// internal/gc: Pairs() gains (App, AppRevision) after ADR-0199's App pairs, before (Function, Revision).
// internal/controlplane: Handlers gains GetAppRevision and ListAppRevisions only.
// internal/app: Deps gains Clock clock.Clock (nil ⇒ System), UpgradeTimeout (0 ⇒ 5m), RevisionHistory (0 ⇒ 10).

// internal/platform/config (additive field; defaults() sets only RevisionHistory 10, leaving UpgradeTimeout empty
// so pacing() sees it unset, as eventing.deliveryBackoffMax)
type Config struct {
	App struct {
		UpgradeTimeout  string `json:"upgradeTimeout,omitempty" env:"FUNCD_APP_UPGRADE_TIMEOUT"`
		RevisionHistory int    `json:"revisionHistory,omitempty" env:"FUNCD_APP_REVISION_HISTORY" validate:"min=1,max=100"`
	} `json:"app,omitempty"`
}
```

| Config key | Env | Default | Bounds |
|---|---|---|---|
| `app.upgradeTimeout` | `FUNCD_APP_UPGRADE_TIMEOUT` | `5m`, or twice `runtime.bootTimeout` if larger | 1ms to `v1.MaxDuration`; when set, more than `runtime.bootTimeout` |
| `app.revisionHistory` | `FUNCD_APP_REVISION_HISTORY` | `10` | 1 to 100: AppRevisions kept besides the current one |

The start refusal uses `pacing()`'s helper: `config key "app.upgradeTimeout" has invalid value "1m0s" (want more than
runtime.bootTimeout, 1m0s)`. New reasons: `Superseded` and `Replaced` (AppRevision `Current`) and `NotCurrent` (child
state `Pruning`); the AppRevision conditions reuse ADR-0199's other reasons.

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, the section kinds · `internal/platform/clock` · config `app.*`, `runtime.bootTimeout` · `controller.Result.RequeueAfter` | kind `AppRevision` (GET only: REST, SDK, OpenAPI) · `AppStatus` fields · `sdk.ReadOnlyWriter` · `funcdctl app history`, `funcdctl app rollback` · GC pair · `Pacing.AppUpgradeTimeout`, `WithAppRevisionHistory` |

## Implementation plan

1. **Types and API** (after ADR-0199's implementation): `api/types/v1alpha1/apprevision.go` with tests; `app.go`
   status fields; `metadata.go` (`KindAppRevision`, `NewObject`, `AllKinds` and its count test); `pkg/sdk/kinds.go`
   (`ReadOnlyWriter`) and the `cmd/funcdctl/cli.go` pre-flight; `internal/controlplane` Handlers, `storeHandlers`,
   `StubHandlers`, `registerAppRevision` (GET only), `stampTypeMeta`; `just generate`.
2. **Server**: `internal/app/revision.go` (stamp, namesake, record, switch, timeout, history), the phase rule in
   `status.go`, the prune gate in `reconcile.go`; the `internal/gc/gc.go` pair; the config group, the
   `revisionHistory` default and `examples/funcdconfig.yaml` (`upgradeTimeout` commented out, its default noted);
   `cmd/funcd/main.go` (`pacing()`, `WithAppRevisionHistory`); `pkg/funcd` (Pacing, option, `app.Deps`);
   `cmd/funcdctl/app.go` (the group).
3. **Tests**: `internal/app` unit tests on `clock.NewManual`: equal spec stamps nothing; no gap across A→B→A; a
   reordered entry stamps; namesake dropped, other owner stops; superseded; `Failed` at the deadline and final after;
   `RequeueAfter` the time left; a new Reconciler keeps the deadline; the pass that writes a changed Function neither
   switches nor prunes; failed install; a stopped upgrade stamps, writes no part, then fails with the stop reason;
   history with `Failed` revisions; prune held `NotCurrent`; a pass failing after any of its writes (App status, new,
   previous or superseded revision) converges on the next; a repeated pass writes nothing. Config refusals, bounds and the unset default
   (`cmd/funcd`, `pkg/funcd`); `TestSDKKindPaths_MatchServerRoutes`; a 405 test as
   `internal/controlplane/revision_test.go`; `cmd/funcdctl` `history` (order, columns, `-o json`) and `rollback`
   (absent, other UID, equal spec, bad `n`, read-only text); `TestScenarioApp…` (e2e) in `pkg/funcd`.
4. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; no `go.mod` change.

## Review checklist

- [ ] AppRevision: GET routes only, else 405 `Allow: GET`; `ReadOnlyKind` true; spec and metadata never change.
- [ ] A stamp precedes ADR-0199's ownership check and needs a `json.Marshal` change; numbered latest+1 per App UID.
- [ ] Every time read in `internal/app` goes through `Deps.Clock`; the deadline is `startedAt` + `UpgradeTimeout`.
- [ ] `currentRevision` moves only in a pass not stopped, with no part written or Pending, App status first; every
      pass re-derives the records; a part Ready after the deadline never makes a `Failed` revision current.
- [ ] Prune runs only after the switch, in a pass that wrote no part; history keeps current + newest N per App UID.
- [ ] `gc.Pairs()` holds `(App, AppRevision)` before `(Function, Revision)`.
- [ ] `pacing()` and `WithPacing` refuse a set `app.upgradeTimeout <= runtime.bootTimeout`, default it to
      max(5m, 2 × `runtime.bootTimeout`) when unset; `revisionHistory` is 1 to 100.
- [ ] `funcdctl app rollback` writes only the App, and nothing when the revision is absent or foreign.

## Consequences

**Positive**: what was deployed, and how its rollout went, is recorded per version; a broken upgrade neither stops
the changed Functions' previous Revisions nor prunes dropped parts; rollback reuses apply and admission.
**Negative**: each AppRevision copies the spec (at most 1 MiB,
the API body cap), up to 101 copies per App; after a failure the kinds without revisions run the new spec, so only a
change inside one Function keeps its old version serving: a Route, Site, Sensor or EventSource moved to a new or
renamed Function points at it in the same pass (a Route checks only that its backend exists,
`internal/route/reconcile.go:165-185`) and stays broken until a fix or a rollback; a Function ready late switches,
so `currentRevision` names the last complete rollout, not what every part runs; a rollback after a failure boots a
fresh Revision of code that already serves; each change still adds Function Revisions (ADR-0170 scope); data is
not rolled back: KV keys and Bucket objects stay, and a `deletion: delete` store pruned later comes back empty.
**Risks accepted**: a changed scale-to-zero Function that is not running counts as settled (decider), so its revision
becomes current unbooted and a broken image shows as `Degraded` at the first call; a Workflow part is Ready once its
edges type-check (`internal/workflow/reconcile_workflow.go:141-145`), before its step Functions' new Revisions
serve, so the timeout does not cover a step image that fails at boot; image pull and boot share the timeout; funcd
downtime counts toward the deadline; the same spec is retried only by changing it (for example `spec.version`);
`app.revisionHistory` is the platform's first count-based retention.

## Open questions

1. Where a hook reads `event: rollback` and `from` (an AppRevision field or annotation) → the F117 ADR.
2. How F122 moves `startedAt` to the moment the requirements are met → the F122 ADR.

## References

- [App design note](../reports/app-design.md) · [FEAT-0010](../feat/0010-feat-apps.md) · `function.go:2076`, `:2283-2295` ·
  `store.go:398-420`, `:620-645` · `pkg/sdk/kinds.go:15-22` · `cmd/funcd/main.go:575-617` · `internal/gc/gc.go:30-37`.
