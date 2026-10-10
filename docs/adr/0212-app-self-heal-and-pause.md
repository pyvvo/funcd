# ADR-0212: App self-heal record and pause

- **Status**: Accepted (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, lifecycle, drift, self-heal, pause, controller
- **Realizes**: [FEAT-0010/F115](../feat/0010-feat-apps.md) (Drift correction and pause)
- **Source**: Decision 17 (drift and pause), Decision 13's "a paused App runs no hook", the decisions-table rows on
  self-heal, its visibility, pause and the word self-heal, and the lifecycle rows Drift and Paused of the
  [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0139](0139-site-declarative-static-web-app.md) (a Site rewrites its Route) ·
  [ADR-0094](0094-workflow-engine-core.md) (a paused WorkflowRun) ·
  [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (a repeated pass writes nothing) ·
  [ADR-0196](0196-utc-millisecond-timestamps.md) · ADR-0206 (Accepted, held boot) · ADR-0210 (Accepted, `If-Match`) ·
  drafts ADR-0213, ADR-0214, ADR-0216, ADR-0217, ADR-0219, ADR-0220
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md) and [ADR-0200](0200-app-revisions.md) (Implemented):
  the Exposes column of Contracts; ADR-0199's workaround "a hand edit is written back with no record" exits.
- **Supersedes in part (back-links at acceptance)**: ADR-0199 Decisions 4 and 6 (a paused pass writes, re-creates and
  prunes nothing) and Decision 5 (`Degraded` follows the switch, not the generation); ADR-0200 Decision 3 (the stamp
  and the frozen `spec.spec` use `WithoutPause()`; a paused pass stamps nothing), Decisions 4, 6 and 8 (a paused pass
  converges no part, switches and fails nothing, and trims no history) and Decision 9 (`app rollback` keeps the App's
  stored `spec.paused` and compares with `sameAppSpec` on `WithoutPause()`); the deadline of ADR-0200 Decision 6 and
  of ADR-0206 Decision 6's App row (it also counts from `resumedAt` and, with F117, ADR-0214's last `preApply`
  `endTime`, never from these alone; ADR-0212 Decision 6 owns the formula from here).

## Context & Need

ADR-0199 Decision 4 writes a hand-edited or deleted part back on the next pass, but leaves no trace, so a hot fix
disappears and nobody sees why. An operator also has no way to stop the App while working by hand in an incident.

**Purpose.** The App records each write-back of drift (one log line and `status.lastSelfHeal`), and `spec.paused`,
set by `funcdctl app pause` and cleared by `funcdctl app resume` or an apply without it, stops every write of the App
until then. The operator reads the record in the logs and the App status; the App reconciler is the only writer.

## Scenarios

Fixture: ADR-0200's, the App `todo` installed with `todo-1` current. Config defaults unless a scenario sets a key.

- `scenario: app-drift-self-healed` — `funcdctl apply` of `todo-api` with another image ⇒ within 5 s the App writes
  the declared image back, logs one Info line `self-healed` with `kind=Function`, `name=todo-api` and `app=todo`, and
  `status.lastSelfHeal` names `Function/todo-api` with the write time; `todo-api`'s status is untouched and no
  AppRevision is stamped. `funcdctl delete function todo-api` ⇒ it is re-created the same way, with the same record.
- `scenario: app-paused-keeps-hotfix` — `funcdctl app pause todo`, then a manual image edit of `todo-api` ⇒ the edit
  stays; the App shows `Paused=True` `SpecPaused` and keeps its phase; `app history todo` lists only `todo-1`. After
  `funcdctl app resume todo` the declared spec is back within 5 s, `Paused=False` `Resumed`, `status.lastSelfHeal`
  names `Function/todo-api`, and `app history todo` still lists only `todo-1`.
- `scenario: app-rollout-is-not-self-heal` — `todo` applied with a new `todo-api` image ⇒ `todo-2` is stamped and
  `todo-api` written with no `self-healed` line and `status.lastSelfHeal` unchanged; the same holds when only the
  App's `metadata.resourceGroup` changes and every part is rewritten.
- `scenario: app-paused-defers-upgrade` — while `todo` is paused, a manifest with `paused: true` and a new `todo-api`
  image is applied ⇒ nothing is stamped and `todo-api` keeps its image; after `funcdctl app resume todo`, `todo-2` is
  stamped and rolls out as ADR-0200's `app-upgrade`.
- `scenario: app-apply-without-paused-resumes` — while `todo` is paused, a manifest without `paused` and with a new
  `todo-api` image is applied with `funcdctl apply` ⇒ `Paused=False` `Resumed`, and `todo-2` is stamped and rolls out.
- `scenario: app-paused-rollout-full-timeout` — with ADR-0200's `app-failed-upgrade-keeps-serving` settings
  (`app.upgradeTimeout: 20s`), `todo-api` gets an image that never starts, and `todo` is paused 5 s after the stamp
  for 30 s ⇒ the new revision stays `Deploying` while paused and turns `Failed` 20 s after the resume, not before.

## Scope

**In**: the self-heal record, `spec.paused`, `funcdctl app pause` and `resume`, the `Paused` condition, the deadline
across a pause. **Out**: the hold gate and its wiring (ADR-0206 Decision 6); hooks, which F117 adds behind the same
pause check; Secrets (F116 keeps the never-write-back rule); the conditional write of pause and resume and its retry
(ADR-0210 Decision 4); a list of every self-heal (the log holds them); pausing one part.

## Constraints & Decision drivers

The reconciler keeps no state between passes (`internal/app/reconcile.go:44-45`), so every rule reads the store;
every time goes through `Deps.Clock` (ADR-0200); a repeated pass writes nothing (ADR-0047; the store coalesces an
unchanged write, `internal/store/store.go:381-438`); stamps go through `NewTimestamp` (ADR-0196); the hold edits no
desired state (ADR-0206); no new watch (`pkg/funcd/funcd.go:1022-1038` already watches every part kind), config key or
dependency.

## Alternatives considered

| Option | Lost because |
|---|---|
| Every write in a pass that stamped nothing is a self-heal | counts an App `resourceGroup` change and the retry of a stopped rollout as drift |
| Record each part's last written spec or hash in the status | a second source of truth and a status write per part; the spec compare already says what differs |
| `paused` inside the stamped spec, as the note's contract places it | each toggle stamps a revision, starts a deadline and takes a history slot; a rollback restores an old pause |
| Pause as an annotation or an endpoint | the note and the WorkflowRun precedent put it in `spec`; an annotation is untyped; an endpoint is a second writer |
| Paused time counts toward `app.upgradeTimeout` | a pause longer than the time left fails the upgrade at the resume, the moment the operator hands back |
| Exclude the exact paused time, as a run's `PausedNanos` (ADR-0094, `internal/workflow/runstate/runstate.go:60-67`) | a persisted sum written at each toggle; a paused App writes no AppRevision |
| A paused App keeps refreshing children and phase | the parts it no longer manages report their own status; the lifecycle row keeps the phase |
| One mechanism for pause and hold | ADR-0206 rejected flags because they edit desired state; a restore must not resume an App an operator paused |

## Decision

1. **Self-heal.** A self-heal is ADR-0199 Decision 4's write of a part that is absent or whose spec differs from the
   declared one (`sameSpec`, `internal/app/reconcile.go:308-318`), in a pass whose latest AppRevision had `Applied=True`
   before the pass (the `stored` snapshot, `:187`). A pass that stamps reads a new revision with no `Applied`, and a
   rollout that has not yet written or found equal every part has `Applied` False or absent, so neither records.
   After `Failed`, `Applied` keeps its last value, so a write-back there (ADR-0200 Decision 4) is a self-heal only if
   it was True. A write that changes only owner references or the resource group (an App `metadata.resourceGroup`
   change) is convergence, not drift, and is not recorded. A self-heal writes only the spec, never a part's status, a
   `ref` object (apply skips it, `:239-242`, `:260-263`) or a Secret (not a section until F116, which keeps this rule).
2. **Record.** Right after each self-heal write the reconciler logs at Info the message `self-healed` with keys
   `kind`, `namespace`, `name` and `app`, as `pruned` (`internal/app/reconcile.go:456`); it carries no spec value. The
   pass sets `status.lastSelfHeal` to the last self-healed part in section order, with `at` one
   `NewTimestamp(clock.Now())` per pass, and writes it with the App status. It is never cleared. When that App status
   write meets a Conflict the record is lost; the log line stays, and the next pass finds the part equal.
3. **Pause is not a revision.** `spec.paused` lives in `AppSpec` (the note's contract) but is not part of the declared
   app: the stamp compares, and the AppRevision freezes, `a.Spec.WithoutPause()`. A pause or resume therefore stamps
   nothing, starts no deadline and takes no history slot; stored revisions have no `paused` key and compare equal.
   `funcdctl app rollback` copies `spec.spec` and keeps the App's `paused`; `sameAppSpec` compares `WithoutPause()`.
4. **A paused pass** (`spec.paused` true) reads only the App, sets `Paused=True` with reason `SpecPaused`, message
   "spec.paused is set: the App writes no part until it is resumed" and the condition's own `observedGeneration` (not
   `status.observedGeneration`) the generation, and writes the App status (an unchanged one coalesces). It stamps
   nothing; writes, re-creates or prunes no part; runs no hook (F117: a call running when the pause lands records
   nothing, ADR-0214 Decision 8) and no Secret check (ADR-0213 Decision 8: its reasons keep their stored values until
   the resume pass checks again); switches nothing; turns no revision `Failed`; writes no AppRevision status; deletes no
   history. `app retry` and `app test` answer 409 `app <app> is paused` (ADR-0214, ADR-0216 Decision 4). Phase,
   `Ready`, children, revisions, `version` and `lastSelfHeal` keep their stored values, so an App created paused
   installs nothing and has no phase until resumed. It returns no requeue: a resume is an App update, which the App's
   watch brings; part events run passes that write nothing. The owner GC is not the reconciler, so a deleted paused
   App's tree is collected (`internal/app/reconcile.go:171-174`).
5. **Resume.** The first pass not paused that finds `Paused=True` sets it False with reason `Resumed`, its
   `lastTransitionTime` from `Deps.Clock` (`internal/app/status.go:221-227`), before the stamp, then runs the whole
   pass: a spec changed while paused stamps now, and a part edited while paused is written back, a self-heal when
   Decision 1 holds. The condition stays False afterwards and is absent only on an App never paused.
6. **Deadline.** Paused and held time do not count. Once `startedAt` is set, the deadline is max(`startedAt`,
   `ReleasedAt`, `resumedAt`, F117's last `preApply` `endTime`) + `app.upgradeTimeout`: ADR-0206 Decision 6's
   `ReleasedAt` (`app.Deps.Hold.ReleasedAt()`), this ADR's `resumedAt` (a False `Paused` condition's
   `lastTransitionTime`) and ADR-0214 Decision 4's call end. `resumedAt` and `ReleasedAt` are zero when absent and
   never start one alone; ADR-0200 fills a missing `startedAt` until ADR-0219 Decision 4 drops the fill. All terms are
   read again each pass, from the store and the gate; `setCondition` stamps `resumedAt` from `Deps.Clock`, not the
   wall clock (`api/types/v1alpha1/status.go:35-49`). A revision stamped after the resume or the release keeps its
   `startedAt`; one `Failed` before the pause or the hold stays `Failed`.
7. **Phase across a pause.** A pause or resume bumps the App's generation (`internal/store/store.go:620-645`)
   without a stamp, and `appPhase` gives `Degraded` only when `observedGeneration` equals the generation
   (`internal/app/status.go:178-181`), so a resumed `Degraded` App would read `Deploying`. Since ADR-0200 every other
   spec change stamps or stops the pass, and the switch pass sets `observedGeneration`; so once the latest revision is
   current, a Pending part or a stopped pass gives `Degraded` whatever the generation. That includes a stamp stopped by
   a foreign namesake (`ChildNotOwned`, `internal/app/revision.go:78-83`), which today reads `Deploying`.
8. **The platform hold is separate.** ADR-0206 Decision 6 holds every runner through one `hold.Gate`, which `serve`
   passes through `funcd.WithHold` to the App reconciler as `app.Deps.Hold`; a held pass writes nothing and requeues.
   The first of ADR-0206, 0212, 0214 and 0216 to land adds that field as ADR-0206's inline
   `Hold interface{ Held() bool; ReleasedAt() time.Time }` (nil means never held), not `hold.Gate`, so `internal/app`
   never imports `internal/platform/hold`. ADR-0214 Decision 8 places the `Held()` check; this ADR adds none. The hold
   never reads or writes `spec.paused`; the pause code never reads the gate, only `deadline` reads `ReleasedAt`.
9. **CLI.** `funcdctl app pause <app>` and `funcdctl app resume <app>`, built as `workflowPauseCmd`
   (`cmd/funcdctl/workflow.go:221-247`) on ADR-0210 Decision 4's read-change-apply helper (a conflict re-reads and
   retries, at most 5 attempts): get the App, set `spec.paused`, `sdk.Client.Apply`, print `paused <app>` or
   `resumed <app>`; `-n/--namespace` defaults to `default`. The apply runs the App admission again with the caller's
   rights (`internal/app/admission.go:49-56`), as a rollback. `pause|resume` join the group's `Short`
   (`cmd/funcdctl/app.go:26`) beside the subcommands other ADRs add. `spec.paused` is an ordinary spec field that a
   PUT replaces (`pkg/sdk/sdk.go:85-87`), and a manifest has no version (ADR-0210 Decision 3), so a plain
   `funcdctl apply` whose manifest does not set `paused: true` resumes the App. `app deploy` copies the stored
   `spec.paused` into the applied spec and never resumes one (ADR-0217 Decision 8).
10. **Dry run (F123, ADR-0220).** The plan of a paused App is what the pass after the resume would do: it compares
    `WithoutPause()` (Decision 3), so a pause or resume alone plans no revision, and it does not show the pause, which
    the stored `Paused` condition reports (ADR-0220 open question 1; a client note is ADR-0217 open question 1).

## Temporary workarounds

- Until ADR-0206's implementation sets `app.Deps.Hold` (the exit), the field (Decision 8) is nil: nothing is held.

## Contracts

```go
// api/types/v1alpha1/app.go (additive)
type AppSpec struct {
	// Version unchanged; Paused follows it and precedes ADR-0219's Requires (the design note's order)
	// Paused stops every write of the App (ADR-0212); it is not part of an AppRevision.
	Paused bool `json:"paused,omitempty"`
	// KV ... Catalogs and the fields of sibling ADRs unchanged
}

// WithoutPause returns s with Paused cleared: what the stamp compares, an AppRevision freezes and rollback compares.
func (s AppSpec) WithoutPause() AppSpec

type AppStatus struct {
	// Status, CurrentRevision, LatestRevision, Version, Children unchanged
	LastSelfHeal *AppSelfHeal `json:"lastSelfHeal,omitempty"`
}

// AppSelfHeal is the last part the App wrote back (ADR-0212 Decision 2).
type AppSelfHeal struct {
	Kind Kind       `json:"kind"`
	Name ObjectName `json:"name"`
	At   Timestamp  `json:"at"` // NewTimestamp, ADR-0196
}

// internal/app; Deps.Hold is ADR-0206's inline interface, added by the first of ADR-0206/0212/0214/0216 to land
const (
	condPaused    = "Paused"
	reasonPaused  = "SpecPaused"
	reasonResumed = "Resumed"
)

// apply gains heal, true when the latest revision's stored Applied is True, and returns the parts it self-healed in
// section order besides the first part written.
func (r *Reconciler) apply(ctx context.Context, a *v1.App, ents []entry, objs map[v1.ObjectRef]v1.Object, halt *stop,
	heal bool) (*stop, *v1.ObjectRef, []v1.ObjectRef, error)

// deadline is the latest of startedAt, resumedAt(a), Deps.Hold.ReleasedAt() (ADR-0206; zero for a nil Hold) and,
// with F117, the last preApply endTime (ADR-0214 Decision 4), plus UpgradeTimeout; neither replaces startedAt.
func (r *Reconciler) deadline(a *v1.App, rev *v1.AppRevision) time.Time

// resumedAt is the lastTransitionTime of a False Paused condition, the zero time when there is none.
func resumedAt(a *v1.App) time.Time

// cmd/funcdctl
func (a *cli) appPauseCmd(verb string, paused bool) *cobra.Command
```

```yaml
spec:
  paused: true
status:
  phase: Ready
  conditions:
    - type: Paused
      status: "True"
      reason: SpecPaused
  lastSelfHeal:
    kind: Function
    name: todo-api
    at: "2026-10-08T09:12:03.123Z"
```

| Item | Value |
|---|---|
| Condition `Paused` | True `SpecPaused` while `spec.paused`; False `Resumed` after a resume; absent on an App never paused; no effect on `Ready` |
| Log line | Info `self-healed`, keys `kind`, `namespace`, `name`, `app` (component `app`) |
| Config | none new |

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, the section kinds · `Deps.Clock` · `app.Deps.Hold.ReleasedAt()` (ADR-0206) · `app.upgradeTimeout` · ADR-0210's read-change-apply helper | `spec.paused` · `status.lastSelfHeal` · condition `Paused` · log `self-healed` · `funcdctl app pause`, `funcdctl app resume` |

## Implementation plan

1. **Types**: `api/types/v1alpha1/app.go` (`Paused`, `WithoutPause`, `LastSelfHeal`, `AppSelfHeal`) with tests:
   `WithoutPause` clears only `Paused`; `paused: false` is absent from the JSON; `at` marshals as UTC milliseconds;
   `just generate` (OpenAPI, SDK).
2. **Server**: `internal/app/reconcile.go` (the paused pass before `revisions`, the resume, `heal` from `stored`,
   apply's self-heal list, the log line, `lastSelfHeal`); `revision.go` (stamp and frozen copy on `WithoutPause`,
   `deadline`, `resumedAt`); `status.go` (`Paused`, the `Degraded` case of `appPhase`).
3. **CLI**: `cmd/funcdctl/app.go`: `appPauseCmd` wired as `pause` and `resume` on ADR-0210's helper; rollback
   keeps `paused`; `sameAppSpec` on `WithoutPause`.
4. **Tests**: `internal/app` unit tests on `clock.NewManual` with a captured `slog` handler:
   `TestAppWritesBackAHandEdit` also asserts the line and `lastSelfHeal`; a stamping pass and, on a `Deploying`
   revision, the retry of a pass stopped by `ChildInvalid` record nothing; a `resourceGroup`-only and an
   owner-reference-only rewrite record nothing; a write-back after `Failed` records only when `Applied` was True; two
   parts healed in one pass give two lines, one `at` and the later part in section order; a hand-edited `ref` object
   stays; no line holds the image; a stamp stopped by a namesake on a current App reads `Degraded`. Paused: no part
   created, updated or deleted, no stamp on a spec change, no `Failed` past the deadline, no AppRevision write, no
   history deletion, `Result{}`, a repeated paused pass keeps every `resourceVersion`; pause and resume stamp nothing;
   an apply without `paused` resumes; a resume sets the deadline from `resumedAt`, a later release (a fake `Hold`)
   from `ReleasedAt`; a `Degraded` App stays `Degraded` across a pause; an App created paused installs nothing.
   `internal/gc`: a deleted paused App's tree is collected. `cmd/funcdctl`: pause and resume set the flag, print, honor
   `-n` and retry a conflict; rollback keeps `paused`; `sameAppSpec` ignores it. `pkg/funcd`: one e2e per scenario.
5. **Done**: `just ci` and `just ci-full` green; a passing test per scenario name; no `go.mod` change.

## Review checklist

- [ ] The stamp, the frozen `spec.spec` and `sameAppSpec` all use `WithoutPause()`; no AppRevision holds `paused`.
- [ ] A write is recorded only when the latest revision's stored `Applied` is True and the part was absent or its
      spec differed.
- [ ] One `self-healed` Info line per self-healed part, with keys `kind`, `namespace`, `name`, `app` and no spec value.
- [ ] `AppSelfHeal.At` is a `Timestamp` set through `v1.NewTimestamp(r.clock.Now())`.
- [ ] With `spec.paused`, `Reconcile` makes no store write but the App status update and returns `Result{}`.
- [ ] `Paused` is True `SpecPaused` while paused, False `Resumed` after, absent on an App never paused.
- [ ] `deadline` takes the latest of its start terms, `resumedAt` and `ReleasedAt` among them, and never either alone.
- [ ] With the latest revision current, a Pending part gives `Degraded` whatever `observedGeneration` says.
- [ ] No pause code reads the hold gate (only `deadline` reads `ReleasedAt()`); this ADR adds no hold check, and an
      `app.Deps.Hold` it adds is ADR-0206's inline interface; the gate never reads or writes `spec.paused`.
- [ ] `funcdctl app pause` and `resume` change only `spec.paused`, through ADR-0210's helper; rollback keeps it.
- [ ] No pass writes a `ref` object or a Secret.

## Consequences

**Positive**: a hand edit is undone and visible in the logs and the App status; an operator can hot-fix a part
safely and hand control back; a pause leaves the history, the revisions and the deadlines alone.
**Negative**: `lastSelfHeal` holds only the last write, and a write whose App status update conflicts shows only in
the log; while the latest revision's `Applied` is not True (a rollout's first pass, or a rollout that failed or stays
stopped before every part was applied), a hand edit is undone without a record, as is a part moved by hand to another
resource group; while paused, the App's children and `Ready` are stale; each resume and each hold release (ADR-0206)
gives a running rollout a full `app.upgradeTimeout` again.
**Risks accepted**: a pause or resume runs the part admissions again with the caller's rights, so a lowered quota or a
lost right can refuse a pause in an incident, as it refuses a rollback; a plain `funcdctl apply` of a manifest without
`paused`, from CI for example, resumes a paused App and overwrites a hot fix (to keep the pause, the manifest sets
`paused: true`; `app deploy` keeps it, ADR-0217 Decision 8).

## Open questions

1. Should an update that changes only `spec.paused` skip the part admissions? → the decider, at acceptance.
2. Answered by ADR-0210 Decision 4: `app pause` and `resume` write conditionally and retry a conflict (Decision 9).
3. Should a plain `funcdctl apply` that omits `paused` also keep the stored value, as rollback and `app deploy`
   (ADR-0217 Decision 8) do (a `*bool` or an admission rule: a `bool` cannot tell omitted from false)? → the decider,
   at acceptance.
4. Answered by ADR-0206 Decision 6: held time does not count; `deadline` takes the hold's `ReleasedAt` (Decision 6).

## References

- [App design note](../reports/app-design.md) (Decision 17, scenarios, lifecycle) ·
  [FEAT-0010](../feat/0010-feat-apps.md) (F115, its state and sequence diagrams) ·
  `internal/app/reconcile.go:170-228`, `:234-318` · `internal/app/revision.go:55-103`, `:117-171` ·
  `internal/site/reconcile.go:317-341` (`ensureRoute`) · `api/types/v1alpha1/workflowrun.go:28-31`.
