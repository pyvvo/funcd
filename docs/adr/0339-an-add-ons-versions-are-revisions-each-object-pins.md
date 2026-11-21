# ADR-0339: An add-on's versions are revisions, and each object pins the one it runs

- **Status**: Accepted (2026-11-22), by the decider ("I accept all ADR"), after three rounds of an independent judge, every Blocker and Major
  folded
- **Date**: 2026-11-21
- **Deciders**: green-0-rabbit
- **Tags**: addons, revisions, apps, upgrades
- **Realizes**: [FEAT-0019/F265](../feat/0019-feat-addon-apps.md) (an add-on's versions are revisions)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (an object's engine runs its pinned
  revision, not the Addon's spec of the pass: Decision 2); [ADR-0200](0200-app-revisions.md) Decision 3 (a stamp
  records each add-on kind's revision: Decision 3), Decision 5 (`ChildrenReady` waits for a part's pin: Decision 4) and
  Decision 6 (the switch waits for it too: Decision 4); [ADR-0214](0214-app-hooks.md) Decision 2 (the hook input names
  each add-on kind's move: Decision 5).
- **Relates to**: [ADR-0020](0020-function-contract-lifecycle.md) and [ADR-0172](0172-revision-integrity.md) (a
  Revision's name), ADR-0337 (the deploy that updates an Addon), [ADR-0199](0199-app-resource.md) Decision 5 (a part's
  readiness), [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)

## Context & Need

An Addon is cluster-scoped and serves every namespace. The add-on reconciler builds each engine from the Addon it reads
at each pass (`internal/services/addon/reconcile.go:48-91`), so an admin's change of the image replaces every engine
of the kind in every namespace at once, with no hook to migrate data and no way back. The decider chose, on
2026-11-19 and 2026-11-21: no version field; each change of an Addon is a revision, as a Function's is; each object
pins the revision it runs; nothing moves by itself; an object moves when it is edited or when its App's revision moves
it, with the App's hooks around the move; a rollback goes through the App; a revision nothing uses is removed. Callers:
the admin who updates an Addon, the developers whose objects run it, and the App reconciler.

## Scenarios

- `scenario: addon-revision-stamped` — *Given* the Addon `rauthy` at generation 1, *When* an admin changes its image,
  *Then* the AddonRevision `rauthy-2` holds the new spec, the Addon's `status.currentRevision` is `rauthy-2`, and
  `rauthy-1` is unchanged.
- `scenario: addon-revision-no-move` — *Given* the IdentityProvider `login` pinned to `rauthy-1`, *When* the Addon moves
  to `rauthy-2`, *Then* `login`'s engine keeps `rauthy-1`'s image and is not replaced.
- `scenario: addon-revision-edit-moves` — *When* `login`'s spec changes, *Then* `login` pins `rauthy-2`, and its engine
  runs `rauthy-2`'s image.
- `scenario: addon-revision-new-object` — *When* an object is created, *Then* it pins the current revision.
- `scenario: addon-revision-app-upgrade` — *Given* the App `web-auth`, whose part `web-auth-login` pins `rauthy-1`,
  *When* the App's spec changes after the Addon moved to `rauthy-2`, *Then* its new AppRevision records `rauthy-2`,
  its pre-hooks get `rauthy-1` to `rauthy-2` in their input and run while the part runs `rauthy-1`, and its post-hooks
  run once the part runs `rauthy-2`.
- `scenario: addon-revision-no-step-back` — *Given* that part on `rauthy-2`, *When* a later pass of the same AppRevision
  stops, or an older AppRevision is pruned, *Then* the part stays on `rauthy-2`.
- `scenario: addon-revision-superseded` — *Given* `web-auth-2` Applied and superseded by `web-auth-3`, whose pre-hooks
  run, *Then* the part keeps the engine it runs until `web-auth-3` is Applied, and then pins `web-auth-3`'s record.
- `scenario: addon-revision-app-rollback` — *When* `web-auth` is rolled back to its revision that ran `rauthy-1`, *Then*
  the new AppRevision records `rauthy-1`, and the part pins `rauthy-1` again once it is Applied.
- `scenario: addon-revision-rollback-after-failure` — *Given* `web-auth-2` failed its deadline while the part runs
  `rauthy-2`, *When* `web-auth` is rolled back to `web-auth-1`, *Then* the hook input of `web-auth-3` names
  IdentityProvider from `rauthy-2` to `rauthy-1`, and the part pins `rauthy-1` once `web-auth-3` is Applied.
- `scenario: addon-revision-retired` — *Given* `rauthy-1`, which no object pins and no kept AppRevision records,
  *Then* it is removed; the current revision is never removed.
- `scenario: addon-revision-read-only` — *When* anyone sends `POST`, `PUT` or `DELETE` to the AddonRevision routes,
  *Then* the answer is 405 with `Allow: GET`, as for a Revision; an admin lists and reads them.
- `scenario: addon-revision-dev` — *Given* `funcdctl dev` running a template, *When* a reload changes `addon.yaml`,
  *Then* the App's part runs the new revision.

## Scope

In: the revision kind and its stamp, the pin, how an App's revision moves its parts and tells its hooks, the removal,
the reads, and dev's rule. Out: the deploy that updates an Addon (ADR-0337); what an engine does with data of an older
version, which is the App's hooks' job; a revision chosen by hand on a standalone object; settings kinds, which follow
the Addon as it is now (ADR-0340).

## Constraints & Decision drivers

- No version field: a revision is named after the Addon's generation, as a Function's Revision is (ADR-0020).
- A newer revision moves no engine: an admin's change must not restart other namespaces' engines.
- Upgrade and rollback belong to the App, whose hooks own data compatibility (ADR-0214).
- Reuse: the Revision's naming and read-only API (ADR-0172), the AppRevision's stamp, conditions and switch (ADR-0200).

## Alternatives considered

- **`spec.version` on an object, naming a registered version.** Rejected by the decider: it invents a version
  vocabulary beside the generations funcd already numbers, and each object would have to name one.
- **Every engine follows the Addon, as today.** Rejected: one admin's change restarts every namespace's engines at
  once, with no hook and no rollback.
- **Keep every revision.** Rejected by the decider: an Addon changed often would keep images and specs nobody runs.
- **A part follows its App's newest Applied revision, whatever its number.** Round 1's rule, rejected: `Applied` turns
  False when a later pass stops (ADR-0200 Decision 5) and an Applied revision that is not current can be pruned
  (Decision 8), so the part would step back to an older engine with no hook.
- **A pin carried in the part's metadata.** Rejected: objects have no annotations, only `tags`; the part's own status
  records the App revision it follows.

## Decision

1. **The kind.** `AddonRevision` is cluster-scoped, with a read-only API as Revision's (ADR-0172 Decisions 1 and 2):
   list and get, for an admin. Its name is `<addon>-<generation>`, shortened as `revisionName` shortens a Revision's
   (`internal/function/function.go:2605`); its spec is `addon`, `number` (the generation) and `spec`, a frozen copy of
   the Addon's spec. A new Addon reconciler stamps one for the Addon's generation when it has none, so numbers may skip,
   and then sets the Addon's `status.currentRevision`; the pair `(Addon, AddonRevision)` collects them with the Addon.
2. **The pin.** An add-on object's `status.addonRevision` names the revision its engine runs. The add-on reconciler
   builds the engine, checks the config and renders the env from that revision's spec, not from the Addon. A
   standalone object pins the current revision at its first pass and at each new generation, and at no other time;
   while its Addon has no revision yet, it is `Pending` with `Progressing`. A pinned revision that is gone holds the
   object `Pending` with `AddonRevisionNotFound` (new), naming it.
3. **An App's revision.** An AppRevision gains `spec.addonRevisions`, one `{kind, revision}` per add-on kind of its
   parts, set at its stamp: for a spec equal to that of a kept revision that was current once (ADR-0214's rollback
   test), that revision's records; otherwise each kind's current revision. A part follows its App's revisions forward
   only: its `status.appRevision` (new) names the App revision whose record it pins, and its pass moves it to the record
   of the App's latest AppRevision once that revision's `Applied` is True and its number is higher. A superseded
   revision is never a target, and a later pass that stops, or a pruned revision, moves nothing. A revision's number
   comes from its name, `<app>-<number>` (ADR-0200 Decision 1), so a pruned one still compares. Until its first pin, a
   part is `Pending` with `Progressing`. An AppRevision's status change requeues the App's add-on parts.
4. **The App waits for its parts.** A part counts as Ready for `ChildrenReady` (ADR-0200 Decision 5, after ADR-0199
   Decision 5) only once its `status.appRevision` is the App's latest revision, so the switch (Decision 6) comes after
   the part runs the new engine. The App's pre-hooks run before `Applied` turns True (ADR-0214), so they see the old
   engine, and its post-hooks, after the switch, see the new one. A part's status change requeues its App.
5. **The hooks are told.** `AppHookInput` gains `addonRevisions`, one `{kind, object, from, to}` per add-on part whose
   revision the App's revision changes: `from` the revision the part runs at the stamp, its `status.addonRevision`,
   which a failed upgrade may have moved past the current revision's record, so parts of one kind may differ; `to`
   this revision's record for its kind.
6. **Removal.** The Addon reconciler removes a revision that is not current, that no add-on object pins, and that no
   kept AppRevision records, and requeues itself after its supervision period (ADR-0142) while a revision that is not
   current remains. Each pass, the Addon's, an object's and the App's, is one step of the controller's one worker
   (`pkg/funcd/funcd.go:767`), so a pass that reads the current revision pins it before any removal runs.
7. **Dev.** Under `funcdctl dev`, every add-on object follows its Addon's current revision, so a changed `addon.yaml`
   runs at the next reload (ADR-0337 Decision 2): dev runs one developer's loop, with no other namespace to protect.

## Design model

- **Spec**: [`docs/design/addon/revisions.fizz`](../design/addon/revisions.fizz) · state machine
  [`revisions.md`](../design/addon/revisions.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `PinsExist` | always | `scenario: addon-revision-retired`, `scenario: addon-revision-new-object` | `TestScenarioAddonRevisionRetired` |
  | `RecordsExist` | always | `scenario: addon-revision-app-rollback` | `TestScenarioAddonRevisionAppRollback` |
  | `NoMoveByItself` | transition | `scenario: addon-revision-no-move`, `scenario: addon-revision-no-step-back` | `TestScenarioAddonRevisionNoStepBack` |
  | `MovesOnlyToTheLatestApplied` | transition | `scenario: addon-revision-superseded`, `scenario: addon-revision-app-upgrade` | `TestScenarioAddonRevisionSuperseded` |
  | `HooksSeeTheMove` | transition | `scenario: addon-revision-rollback-after-failure` | `TestScenarioAddonRevisionRollbackAfterFailure` |
  | `PostHooksSeeTheNewEngine` | transition | `scenario: addon-revision-app-upgrade` | `TestScenarioAddonRevisionAppUpgrade` |
  | `UnusedAreRemoved` | always eventually | `scenario: addon-revision-retired` | `TestScenarioAddonRevisionRetired` |

- **Bounds**: one Addon, one standalone object, one App with one part; 1 update, 1 edit, 2 App stamps, 1 rollback to
  any kept revision that was current once, 1 stopped pass, the App's current revision and its newest two kept; 1
  concurrent action (each pass is one step of the one worker); 28 actions.
- **Faults**: a later pass of the latest AppRevision that stops; the history's pruning; the writes of the admin and
  the users between passes.
- **Environment**: the controller's one worker serializes the passes. The mutant `split-pass`, a pass that reads and
  pins in two steps, as two workers would, pins a removed revision: a second worker needs this decision again.
- **Mutants**: `split-pass`, `retire-current` and `retire-pinned` fail `PinsExist`; `ignore-records` fails `RecordsExist`;
  `follow-current` fails `NoMoveByItself`; `newest-applied`, `follow-superseded` and `move-early` fail
  `MovesOnlyToTheLatestApplied`; `from-current` fails `HooksSeeTheMove`; `switch-early` fails
  `PostHooksSeeTheNewEngine`; `never-retire` fails `UnusedAreRemoved`.
- **Result**: pass, 49,793 states, 31.6 s, FizzBee v0.5.4; wider (2 updates): pass, 263,287 states, 200.9 s.
- **Left to tests**: the engines the pins start and replace, several kinds per App, the hooks' calls, dev's rule.

## Behavior rows

- **addons.md**: "An Addon's change" (a new revision; no engine moves), "Which revision an object runs" (the pin and
  what moves it), "An unused revision" (removed), all `decided`.
- **apps.md**: "An App's revision with add-on parts" (records each kind's revision; the part moves forward only; a
  rollback takes back its records; the hooks' input), `decided`.
- **funcdctl-dev.md**: "An add-on object under dev" (follows the current revision), `decided`.

## Temporary workarounds

None.

## Contracts

```go
// AddonRevision is one frozen Addon spec (ADR-0339 Decision 1), named <addon>-<generation>.
type AddonRevision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonRevisionSpec `json:"spec"`
}

type AddonRevisionSpec struct {
	Addon  ObjectName `json:"addon"`
	Number int64      `json:"number"`
	Spec   AddonSpec  `json:"spec"`
}

// AddonStatus.CurrentRevision: the revision a new or moved object pins.
CurrentRevision ObjectName `json:"currentRevision,omitempty"`

// AddonObjectStatus: the revision the engine runs, and for an App's part the App revision whose record it pins.
AddonRevision ObjectName `json:"addonRevision,omitempty"`
AppRevision   ObjectName `json:"appRevision,omitempty"`

// AppRevisionSpec.AddonRevisions: each add-on kind's revision for the App's parts.
AddonRevisions []AppAddonRevision `json:"addonRevisions,omitempty"`

// AppHookInput.AddonRevisions: each add-on kind whose revision this App revision changes.
AddonRevisions []AppHookAddonRevision `json:"addonRevisions,omitempty"`

type AppAddonRevision struct {
	Kind     string     `json:"kind"`
	Revision ObjectName `json:"revision"`
}

type AppHookAddonRevision struct {
	Kind   string     `json:"kind"`
	Object ObjectName `json:"object"`
	From   ObjectName `json:"from,omitempty"`
	To     ObjectName `json:"to"`
}
```

| Consumes | Exposes |
|---|---|
| the Addon's generation; the AppRevision's stamp, `Applied` and switch (ADR-0200) | `AddonRevision`, read-only; `status.currentRevision`; `status.addonRevision`, `status.appRevision` |
| the controller's one worker; dev's option | the GC pair `(Addon, AddonRevision)`; `spec.addonRevisions`; the hook input's `addonRevisions` |

## Implementation plan

- `api/types/v1alpha1`: `AddonRevision`, the status fields, `AppAddonRevision`, `AppHookAddonRevision`; the read-only
  routes; regenerate the spec and kindref.
- `internal/services/addon`: an Addon reconciler (stamp, current, removal, requeue); the object's pass reads its pin's
  revision and moves a part forward; dev's rule as an option of the reconciler, set by `funcdctl dev`.
  `internal/app`: the stamp's records, the hook input, the parts' readiness rule, the watch on a part's status.
  `internal/gc`: the pair.
- Docs: the add-ons pages (upgrading an add-on), the App pages (rollback with add-on parts, the hook input), the
  behavior rows.
- Tests: one per Scenario, named after it, in `internal/services/addon`, `internal/app`, `cmd/funcdctl` and
  `pkg/funcd`; the oracle markers; `just check-specs`. Real platform: a case of the lane `addon-rauthy` that updates
  rauthy's Addon, sees no engine replaced, then moves the App's part with a new revision and rolls it back.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] An Addon's change stamps a revision and moves no engine on a server.
- [ ] An engine is built only from its object's pinned revision.
- [ ] A part moves only forward, to its App's latest revision once it is Applied, and a rollback takes back the older
      records.
- [ ] `ChildrenReady` and the switch wait for the part's pin; the hook input names each kind's move.
- [ ] No pinned, recorded or current revision is ever removed; an unused one is, without another change.
- [ ] AddonRevisions have no write route.

## Consequences

- An admin's change reaches a namespace only when its objects are edited or its Apps move them, so versions skew
  across namespaces by design; `funcdctl get identityproviders` shows each object's `addonRevision`.
- An App deployed from an older template moves to the newer revision at its next revision, under the older
  template's hooks.
- Any re-apply of an older App spec, a manual revert or a pipeline's included, takes back that revision's records, an
  engine downgrade; once that revision has left `app.revisionHistory`, the same spec takes the current revision.
- Removal relies on the controller's one worker; a controller with more workers, or the multi-node control plane,
  must decide it again (the `split-pass` mutant).

## Open questions

- A standalone object pinned to an older revision by hand: the decider left it to an App for now.
- Dev's rule (Decision 7) departs from "a newer revision moves no object" and from the hooks around a move: the
  decider's ruling goes into the Constraints before acceptance.

## References

- FEAT-0019 feature issue [#1514](https://github.com/pyvvo/funcd/issues/1514); tracker #1506.
- Function revisions: ADR-0020, ADR-0143, ADR-0172; App revisions: ADR-0200.
