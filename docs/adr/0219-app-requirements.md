# ADR-0219: App requirements — a shared App before its dependents

- **Status**: Implemented (2026-10-10; accepted 2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, requires, dependencies, admission, semver, lifecycle
- **Realizes**: [FEAT-0010/F122](../feat/0010-feat-apps.md) (App dependencies)
- **Source**: Decision 20, the 2026-10-07 rows of the decisions table on dependencies, the lifecycle step
  Requirements, the reason `RequirementNotMet`, the `AppRequirement` contracts and the six `app-requires-*` scenarios
  of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0064](0064-fn-to-fn-rpc-links.md) (link-validity, link-deletion-protection) ·
  [ADR-0121](0121-declarative-referential-integrity-admission.md) (apply in any order) ·
  [ADR-0147](0147-atomic-admission-and-nested-call-cap.md) (namespace lock) · [ADR-0170](0170-owner-garbage-collector.md)
  (forced group delete) · [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (a repeated pass writes nothing) ·
  [ADR-0212](0212-app-self-heal-and-pause.md) (Proposed, F115: a pause stamps nothing and keeps the stored phase) ·
  [ADR-0214](0214-app-hooks.md) (Proposed, F117: the held-pass rule, Decision 8; pre-hooks after this step, Decision
  9) · [ADR-0220](0220-dry-run-engine.md) (Proposed, F123: the dry run returns these refusals) ·
  ADR-0206 (Accepted, the disaster-recovery platform hold read through its inline `Hold` interface, the held rollout deadline in Decision 6) ·
  ADR-0210 (Accepted, `If-Match` on replace and delete)
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md) (Implemented): `AppSpec.requires`, two `AppStatus`
  fields, a second App admission and a watch of Apps; `spec.version` stays free but a range now reads it.
  [ADR-0200](0200-app-revisions.md) (Implemented): answers its open question 2, which it left to F122 with `requires`
  (its Scope and Alternatives); its switch, history and rollback are unchanged.
- **Supersedes in part (back-links at acceptance)**: ADR-0199 Decision 4 (a third pre-write stop; a waiting revision
  writes no part back) and Decision 5 (a stopped pass's `Ready` also carries `RequirementNotMet`); ADR-0200 Decision 3
  (the stamp sets `startedAt` only when every requirement is met, else nil), Decision 4 (a waiting pass converges no
  part), Decision 5 (`Applied=False` also carries `RequirementNotMet`, naming no part) and the `startedAt` part of
  Decision 6 (no deadline, switch or failure without `startedAt`; `deadline()` no longer fills it). ADR-0206
  supersedes other parts of these decisions for a held platform; Decisions 3 and 4 below state how the two meet.
- **Extends (refactor only)**: ADR-0064 (Implemented): `findCycle` is exported as `admission.FindCycle` for reuse.

## Context & Need

A shared App (`lakehouse`) serves objects that other Apps of its namespace use through `ref` (ADR-0199). Nothing says
which version a dependent needs, so a dependent can roll out against the wrong version, and an upgrade or a delete of
the shared App breaks a dependent that works today.

**Purpose.** `spec.requires` lets an App name the shared Apps it needs and a version range for each. The App
reconciler holds a dependent's new revision until every requirement is met, and the App admission refuses the
upgrade, rollback, delete or cycle that would break a working dependent. Callers are App authors (`funcdctl apply`,
`funcdctl app rollback`) and the App reconciler.

## Scenarios

Fixture: in one namespace, App `lakehouse` declares the catalog `lake`; App `billing` has `version: 1.3.0`,
`requires` `lakehouse` `^2.0.0`, the Function `invoice` and `catalogs: - ref: lake`; `app.upgradeTimeout: 2m` (more
than the 1m default `runtime.bootTimeout`, as `cmd/funcd/main.go:632-633` requires), other config at defaults.

- `scenario: app-requires-waits` — `billing` applied before `lakehouse` exists ⇒ `billing-1` stays `Deploying` with
  `Applied=False` `RequirementNotMet` and no `startedAt`; App `billing` is `Deploying`, `Ready=False`
  `RequirementNotMet` `App/lakehouse does not exist; billing needs ^2.0.0`; Function `invoice` is not written, and
  3 min later it is still so, not `Failed`. Then `lakehouse` 2.1.0 is applied and becomes Ready ⇒ `billing-1` gets
  `startedAt`, `invoice` is written and `billing-1` becomes current; `billing` `status.requires` is `lakehouse`,
  `2.1.0`, `met: true`; `lakehouse` `status.requiredBy` is `billing`.
- `scenario: app-requires-version` — `lakehouse` is Ready at 1.9.0 ⇒ `billing` waits with the message
  `App/lakehouse is 1.9.0; billing needs ^2.0.0` and `status.requires` `1.9.0`, `met: false`; after `lakehouse`
  2.1.0 is current and Ready, `billing` rolls out.
- `scenario: app-requires-any-version` — `billing` requires `lakehouse` with no range, and `lakehouse` has no
  `spec.version` ⇒ `billing` rolls out once `lakehouse` is Ready; an apply of `lakehouse` at `3.0.0` or at `beta` is
  accepted; a `DELETE` of App `lakehouse` answers 409 naming `billing`.
- `scenario: app-requires-upgrade-refused` — `billing` current on `lakehouse` 2.1.0, and App `audit` waiting on
  `lakehouse` `^3.0.0` ⇒ an apply of `lakehouse` 3.0.0 answers 409 naming `billing` and `^2.0.0`, not `audit`, and
  stores nothing; `funcdctl app rollback lakehouse 1` (1.9.0) fails the same way; an apply of 2.2.0 is accepted.
  With `lakehouse` 2.1.0 not Ready and a new `billing` waiting at `^2.0.0` instead, an apply of 3.0.0 is accepted.
- `scenario: app-requires-delete-refused` — a `DELETE` of App `lakehouse` while `billing` requires it ⇒ 409 naming
  `billing`, nothing deleted; after `billing` is deleted, the delete succeeds.
- `scenario: app-requires-cycle-refused` — `lakehouse` updated to require `billing` ⇒ 400 naming the cycle
  `lakehouse → billing → lakehouse`, nothing stored; an App that requires itself is refused the same way.

## Scope

**In**: `spec.requires`, the wait before the rollout, the move of `startedAt`, `status.requires` and
`status.requiredBy`, the admission `app-requires`, the App watch. **Out**: requirements across namespaces (the
namespace is the tenancy boundary); template includes and nested Apps (decider); which objects a dependent uses (still
`ref`, Decision 5); pre-hooks, which F117 places after this step (ADR-0214 Decision 9); `funcdctl app deploy` and
`app delete` (F120, ADR-0217), so the scenarios use apply and `DELETE`; the dry run that shows the refusals (F123,
ADR-0220 Decision 2); the platform hold (ADR-0206: the `hold.Gate` that `funcd.WithHold` passes, read as `app.Deps.Hold`, its inline `Hold` interface)
and the held-pass check (ADR-0214 Decision 8); conditional writes (ADR-0210).

## Constraints & Decision drivers

Carry Decision 20 as decided; Apps still apply in any order (ADR-0121), so a missing required App waits and is never
refused; a refusal names the dependents; reuse ADR-0064's precedents and ADR-0200's halt and deadline paths rather than
new mechanisms; every time read through the injected clock; a repeated pass writes nothing (ADR-0047); MIT
`Masterminds/semver/v3` v3.5.0 only, already in `go.mod` (`go.mod:98`); no new config key.

## Alternatives considered

| Option | Lost because |
|---|---|
| Refuse a missing required App at admission, as link-validity refuses a missing link target (`links.go:66-69`) | Decision 20: Apps apply in any order and a dependent waits |
| A separate `requirementsMetAt` beside `startedAt` | two start times for one deadline; a nil `startedAt` already marks a revision whose clock has not started |
| A new AppRevision condition `RequirementsMet` | `Applied=False` already means "no part written" (ADR-0200 Decision 5); one reason more is enough |
| Match `status.version` (the current revision's) | Decision 20 names `spec.version`; with the Ready rule (Decision 2) the two agree whenever a requirement is met |
| Protect a dependent by its current revision's `requires` too | deadlock: `billing-2` waits for `^3.0.0` while `billing-1`'s `^2.0.0` refuses the upgrade it waits for |
| Refuse an upgrade with 422 | the fault table has no such kind (`api/fault/problem.go:18-34`); Conflict, as link-deletion-protection |
| Lenient `semver.NewVersion` | it coerces `1.2` and `v1.2.3`; Decision 20 matches only a semver version |

## Decision

1. **The field.** `spec.requires` lists entries of `app` and `version`; `app` names an App of the same namespace,
   `version` is an npm-style range; an empty range accepts any version, even none. `App.Validate` refuses an invalid
   `app` name, a range `semver.NewConstraint` refuses, and an `app` repeated in the list; a self-requirement is the
   admission's cycle. `AppRequirement.Matches(v)` is true for an empty range; otherwise `v` must parse with
   `semver.StrictNewVersion` (SemVer 2.0.0) and pass `Constraints.Check`, whose prerelease rule stays the library's.
   `spec.version` stays free: no value is refused, and one that is not SemVer never matches a range.
2. **Met.** A requirement is met when the required App exists, `Matches` its `spec.version`, and serves that version
   Ready: phase `Ready` (a `NotStarted` Function counts, `status.go:176-177`), `status.currentRevision` equal to
   `status.latestRevision` and `status.version` equal to `spec.version`. So a stored Ready from an older version never
   counts, and a pause alone (ADR-0212) leaves a met requirement met; `Degraded`, `Failed` and `Deploying` do not
   count. The message of the first unmet requirement, in list order, is `App/<app> <what>; <dependent> needs <range>`,
   with `any version` for an empty range and `<what>` one of `does not exist`, `has no SemVer version` (a range only),
   `is <version>` (outside the range), `is not Ready (<phase>)` (`none` when unset) or
   `has not rolled out its spec yet`, checked in this order.
3. **The wait.** Each pass neither paused nor held lists the namespace's Apps once. The stamp sets `startedAt` only
   when every requirement is met; otherwise it leaves it nil (ADR-0200 Decision 3, else unchanged). A latest revision
   that is `Deploying`, not current and without `startedAt` waits. A held pass ends at ADR-0214 Decision 8's check of
   `app.Deps.Hold` (ADR-0206's inline `Hold` interface) at the top of `Reconcile`, so it never reaches this step and leaves
   `startedAt` nil; this ADR adds no hold check, and a revision started before the hold keeps ADR-0212 Decision 6's
   deadline (Decision 4). In a later pass, if every requirement is met now, the pass stores `startedAt` from the
   clock with an AppRevision status update before `apply` writes any part and continues with the returned revision;
   if that update fails or meets a Conflict, the pass writes no part and requeues, so the deadline survives a restart
   (ADR-0200 Constraints). If not, the pass is stopped with reason `RequirementNotMet` and that message, verbatim: the
   stop names no part, and `stop.msg()` then returns its detail alone (Contracts). The stop is the `halt` that `apply`
   receives, so it comes after the stamp and before ADR-0199's ownership check of the parts and ADR-0213's Secret check
   (its Decision 8), which a waiting pass never reaches. A stopped pass already reads the parts and writes none
   (`reconcile.go:250-252`), shows `Applied=False` with the stop's reason (`revision.go:174-179`), keeps the App
   `Deploying` with `Ready=False` and the stop's message (`status.go:131-136`, `171-175`), prunes nothing
   (`reconcile.go:209`) and requeues after the supervision period (`status.go:204-206`). A self-heal of the current
   revision's parts waits too: no part is written. A waiting revision that a newer stamp supersedes turns `Failed`
   `Superseded` by ADR-0200's rule.
4. **The clock.** `settle` neither switches nor fails a revision without `startedAt` and returns no time left;
   `deadline()` stops filling a nil `startedAt` (`revision.go:165-171`). ADR-0212 Decision 6's deadline, with
   ADR-0214 Decision 4, exists only once `startedAt` is set; `resumedAt` or the hold's `ReleasedAt` alone never
   starts one (ADR-0206 assumed the stamp set `startedAt`).
   Requirements gate a revision only until its `startedAt` is set: one that stops being met later neither stops the
   rollout nor changes the phase or Ready of a started or current revision; `status.requires` shows it.
5. **Status.** Every pass neither paused nor held, a stopped one too, writes `status.requires`, one line per entry
   in order with the required App's `spec.version` (empty when missing or unset) and `met`, and `status.requiredBy`,
   every App of the namespace whose `spec.requires` names this one, sorted by name. The store skips an identical write
   (`store.go:410-420`). A paused pass (ADR-0212 Decision 4) lists no Apps and keeps both at their stored values until
   the App is resumed; a held pass writes neither (ADR-0214 Decision 8).
6. **The watch.** `pkg/funcd` adds `ctrl.Watches(KindApp, MapRequires)`, as Function watches Function
   (`pkg/funcd/funcd.go:829`). `MapRequires` requeues, for a changed or deleted App
   (`internal/controller/controller.go:221-227`), each App of its namespace whose `spec.requires` names it, each App
   that its own `spec.requires` names, and each App whose `status.requiredBy` names it, so a removed or renamed
   requirement reaches the formerly required App (an event carries only the new object).
7. **The admission** `app-requires` is Validating on App Create, Update and Delete, reads the namespace's Apps, and
   is `NamespaceReading` (true), so the namespace lock covers it to the store write (ADR-0147). It is registered after
   app-parts (`pkg/funcd/funcd.go:1101`) and runs after ADR-0210's version check (its Decision 2), so a stale
   `If-Match` or body version gets that 409 first. It refuses, naming every dependent sorted by name:
   - **an upgrade or a rollback** (Update): each other App with an entry for this App whose range matches the stored
     `spec.version` and not the incoming one, except a dependent that has not started, as the design note decides:
     no `status.currentRevision` and either `Ready` reason `RequirementNotMet` (waiting) or no phase (created paused,
     ADR-0212 Decision 4, or not reconciled yet). A started first revision still blocks; a dependent waiting on
     another version never matches. A rollback is an apply (ADR-0200 Decision 9), so it is covered. `fault.Conflict`
     (409); `funcdctl app rollback` retries a `fault.Conflict` at most 5 times (ADR-0210), then fails with it.
   - **a delete**: any other App with an entry for it, with or without a range, as link-deletion-protection
     (`links.go:128-151`). `fault.Conflict`, so a forced group delete removes the dependent first, when it is a member
     of the group, and the required App on its next pass (`handlers.go:346-360`).
   - **a cycle** (Create or Update with `requires`): `admission.FindCycle` over the stored Apps' edges, the incoming
     App's own from the request, as link-validity (`links.go:49-75`); a self-requirement is a cycle. `fault.Invalid`
     (400).

## Temporary workarounds

None: the hold check is ADR-0214 Decision 8's, at the top of `Reconcile`, and this ADR adds none.

## Contracts

```go
// api/types/v1alpha1/app.go (additive)
type AppSpec struct {
	// Version is a free label; a requirement's range matches it only when it is a SemVer 2.0.0 version (ADR-0219).
	Version string `json:"version,omitempty"`
	// Paused (ADR-0212) unchanged; Requires follows it
	Requires []AppRequirement `json:"requires,omitempty"` // shared Apps of this namespace (ADR-0219)
	// KV ... Catalogs unchanged
}
type AppRequirement struct {
	App     ObjectName `json:"app"`
	Version string     `json:"version,omitempty"` // an npm-style range; empty accepts any version, even none
}
type AppStatus struct {
	// other fields unchanged
	Requires   []AppRequirementState `json:"requires,omitempty"`
	RequiredBy []ObjectName          `json:"requiredBy,omitempty"` // sorted by name
}
type AppRequirementState struct {
	App     ObjectName `json:"app"`
	Version string     `json:"version,omitempty"` // the required App's spec.version
	Met     bool       `json:"met"`
}

func (r AppRequirement) Matches(version string) bool // Decision 1

// api/types/v1alpha1/apprevision.go: the StartedAt comment becomes "when the requirements were met, the stamp time
// when none waited; nil while the revision waits; the rollout deadline (ADR-0212 Decision 6) needs it".

// internal/controlplane/admission/links.go: findCycle renamed, links.go calls it, behaviour unchanged.
func FindCycle(start string, adj map[string][]string) string

// internal/app/reconcile.go: stop.msg() returns detail alone when part is unset (a RequirementNotMet stop names no
// part); the ChildNotOwned and ChildInvalid messages keep their "<Kind>/<Name>: " prefix.

// internal/app/requires.go
func NewRequiresAdmission(r admission.StoreReader) admission.Admission // "app-requires"
func (r *Reconciler) MapRequires(ctx context.Context, obj v1.Object) []controller.Request
```

| Refusal | Kind | Message |
|---|---|---|
| upgrade or rollback | Conflict (409) | `app "lakehouse" version "3.0.0" is outside the range its dependents require: billing (^2.0.0)` |
| delete | Conflict (409) | `app "lakehouse" is required by billing; remove the requirement first` |
| cycle | Invalid (400) | `spec.requires would create a requirement cycle (lakehouse → billing → lakehouse)` |

New reason: `RequirementNotMet` (App `Ready=False` in phase `Deploying`; AppRevision `Applied=False`). No config key.

| Consumes | Exposes |
|---|---|
| store: App, AppRevision · `admission.StoreReader` · `internal/platform/clock` · `controller.Watches` · `Masterminds/semver/v3` (MIT, v3.5.0) | `spec.requires`, `status.requires`, `status.requiredBy` (REST, SDK, OpenAPI) · admission `app-requires` · `admission.FindCycle` · reason `RequirementNotMet` |

## Implementation plan

1. **Types**: `api/types/v1alpha1/app.go` (fields, `Matches`, the `Validate` rules, the `Version` comment) and
   `apprevision.go` (comment); `go mod tidy` makes `Masterminds/semver/v3` direct; `just generate`.
2. **Admission**: export `FindCycle` in `internal/controlplane/admission/links.go`; `internal/app/requires.go`
   (`NewRequiresAdmission`); register it in `pkg/funcd/funcd.go` after `app.NewAdmission`.
3. **Reconciler**: `internal/app/requires.go` (evaluate, message, wait, `MapRequires`); `revision.go` (stamp
   `startedAt`, the met pass's `startedAt` update, `settle`, `deadline`); `reconcile.go` (requirements before the
   stamp, the wait after it as `apply`'s `halt`, `stop.msg()`, status lines);
   `pkg/funcd/funcd.go` (`ctrl.Watches(v1.KindApp.GVK(), appReconciler.MapRequires)`).
4. **Tests**: `Matches` table (empty range with `""` and `beta`; `^2.0.0` against `2.1.0`, `1.9.0`, `3.0.0`, `1.2`,
   `v2.1.0`, `2.1.0-rc.1`); `Validate` (bad range, empty or invalid `app`, repeated `app`); admission unit tests:
   each refusal and its kind, a dependent waiting on a version or for readiness at a matching version, or created
   paused (no phase), not blocking, one in its first rollout blocking, a no-range dependent never blocking an
   upgrade, a Create closing a cycle, a self-requirement, delete with and without dependents, `ReadsNamespace`,
   `Handles`;
   `internal/app` on `clock.NewManual`: a wait writes no part and never fails, `startedAt` stored on the met pass
   before any part write and the deadline counted from it, no part written when that update conflicts, a new
   Reconciler after the met pass keeping the stored `startedAt`, a hold's `ReleasedAt` (ADR-0206's `app.Deps.Hold`,
   once built) starting no deadline for a waiting revision, an App without `requires` keeps `startedAt` at the
   stamp and marshals as before, each message verbatim, a required App Ready at an older `status.version`, a revision
   superseded while waiting, a requirement lost after the start changes nothing, `MapRequires` both ways, on delete
   and on an Update that removes or renames a requirement (`requiredBy` drops the dependent), `requiredBy` order, a
   repeated pass writes nothing, a waiting pass reaches neither the ownership check nor the Secret check, a paused
   pass (once ADR-0212 is built) lists no Apps and keeps `status.requires` and `status.requiredBy`;
   `TestScenarioApp…` per scenario in `pkg/funcd`.
5. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; the only `go.mod` change drops
   `// indirect` from `Masterminds/semver/v3`.

## Review checklist

- [ ] `go.mod` lists `github.com/Masterminds/semver/v3 v3.5.0` without `// indirect`; no other module changes.
- [ ] `Matches` uses `StrictNewVersion` and `NewConstraint`; an empty range returns true for any input.
- [ ] A waiting revision has no `startedAt`, no part write in its passes, `Applied=False` `RequirementNotMet`, and
      `settle` never switches or fails it, after a hold's release too; `deadline()` never assigns `StartedAt`; a met
      pass stores `startedAt` before any part write.
- [ ] The `app-requires-waits` and `app-requires-version` messages match the scenario text, with no `<part>: ` prefix.
- [ ] An App without `requires` gets `startedAt` at the stamp, and its `json.Marshal` spec is unchanged.
- [ ] `app-requires` handles App Create, Update and Delete only; `ReadsNamespace` returns true; upgrade and delete
      refusals are `fault.Conflict`, a cycle `fault.Invalid`; a missing required App is never refused.
- [ ] `grep -rn 'func findCycle\|func FindCycle'` finds one definition.
- [ ] `pkg/funcd` registers `Watches(KindApp, MapRequires)`; `MapRequires` maps both directions and also requeues
      the Apps whose `status.requiredBy` names the changed App.
- [ ] `status.requiredBy` is sorted; `status.requires` follows `spec.requires` order.
- [ ] One test named after each `app-requires-*` scenario.

## Consequences

**Positive**: shared Apps and dependents apply in any order and still roll out in the right one; an upgrade, rollback
or delete that breaks a working dependent is refused before anything is stored, naming it.
**Negative**: `spec.version` is now read; each App pass lists the namespace's Apps, and an App change requeues its
dependents and the Apps it requires; while an upgrade waits, a hand edit to a current part is not written back; a
dependent waits without a limit, visible as `RequirementNotMet`, after any stamp, a tests-only one (ADR-0216) too;
a required App that degrades after a dependent's start does not show on the dependent's phase.
**Risks accepted**: a required App can still drop or change the objects a dependent uses at a matching version, since
requirements do not name objects (the `ref` waits again); a Namespace delete runs no App admission
(`handlers.go:476-477`), and a requirement never leaves its namespace.

## Open questions

1. Pre-hooks run after the requirements step and never for a waiting revision → answered by ADR-0214 Decision 9.
2. The dry run returns the `app-requires` refusals → answered by ADR-0220 Decision 2 (a refusal before the store call
   comes back unchanged).

## References

- [App design note](../reports/app-design.md) (Decision 20, scenarios, contracts, reasons) ·
  [FEAT-0010](../feat/0010-feat-apps.md) · `internal/controlplane/admission/links.go:18-151` ·
  `internal/app/revision.go:55-171` · `internal/app/reconcile.go:166-281` · `internal/app/status.go:129-217` ·
  `internal/controlplane/handlers.go:315-360` · [Masterminds/semver](https://github.com/Masterminds/semver) v3.5.0.
