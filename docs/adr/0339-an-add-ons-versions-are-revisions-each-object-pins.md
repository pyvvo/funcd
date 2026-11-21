# ADR-0339: An add-on's versions are revisions, and each object pins the one it runs

- **Status**: Proposed (2026-11-21)
- **Date**: 2026-11-21
- **Deciders**: green-0-rabbit
- **Tags**: addons, revisions, apps, upgrades
- **Realizes**: [FEAT-0019/F265](../feat/0019-feat-addon-apps.md) (an add-on's versions are revisions)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (an object's engine runs its pinned
  revision, not the Addon's spec of the pass: Decision 2); [ADR-0200](0200-app-revisions.md) Decision 3 (a stamp
  records each add-on kind's revision: Decision 3).
- **Relates to**: [ADR-0020](0020-function-contract-lifecycle.md) and [ADR-0172](0172-revision-integrity.md) (a
  Revision's name), [ADR-0214](0214-app-hooks.md) (hooks around the apply), ADR-0337 (the deploy that updates an Addon),
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)

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
  its pre-hooks run while the part runs `rauthy-1`, and its post-hooks run once the part runs `rauthy-2`.
- `scenario: addon-revision-app-rollback` — *When* `web-auth` is rolled back to that earlier revision, *Then* the part
  pins `rauthy-1` again.
- `scenario: addon-revision-retired` — *Given* `rauthy-1`, which no object pins and no kept AppRevision records,
  *Then* it is removed; the current revision is never removed.
- `scenario: addon-revision-read-only` — *When* anyone creates, replaces or deletes an AddonRevision through the API,
  *Then* the route does not exist; an admin lists and reads them.

## Scope

In: the revision kind and its stamp, the pin, how an App's revision moves its parts, the removal, and the reads. Out:
the deploy that updates an Addon (ADR-0337); what an engine does with data of an older version, which is the App's
hooks' job; a revision chosen by hand on a standalone object; settings kinds, which follow the Addon as it is now
(ADR-0340).

## Constraints & Decision drivers

- No version field: a revision is named after the Addon's generation, as a Function's Revision is (ADR-0020).
- A newer revision moves no engine: an admin's change must not restart other namespaces' engines.
- Upgrade and rollback belong to the App, whose hooks own data compatibility (ADR-0214).
- Reuse: the Revision's naming and read-only API (ADR-0172), the AppRevision's stamp and conditions (ADR-0200).

## Alternatives considered

- **`spec.version` on an object, naming a registered version.** Rejected by the decider: it invents a version
  vocabulary beside the generations funcd already numbers, and each object would have to name one.
- **Every engine follows the Addon, as today.** Rejected: one admin's change restarts every namespace's engines at
  once, with no hook and no rollback.
- **Keep every revision.** Rejected by the decider: an Addon changed often would keep images and specs nobody runs.
- **A pin carried in the part's metadata.** Rejected: objects have no annotations, only `tags`; the AppRevision's
  `Applied` condition already says when the App has written its parts.

## Decision

1. **The kind.** `AddonRevision` is cluster-scoped, with a read-only API as Revision's (ADR-0172 Decisions 1 and 2):
   list and get, for an admin. Its name is `<addon>-<generation>`, shortened as `revisionName` shortens a Revision's
   (`internal/function/function.go:2605`); its spec is `addon`, `number` (the generation) and `spec`, a frozen copy of
   the Addon's spec. A new Addon reconciler stamps one for each generation that has none and then sets the Addon's
   `status.currentRevision` to it; the pair `(Addon, AddonRevision)` collects them with the Addon.
2. **The pin.** An add-on object's `status.addonRevision` names the revision its engine runs. The add-on reconciler
   builds the engine, checks the config and renders the env from that revision's spec, not from the Addon. A
   standalone object pins the current revision at its first pass and at each new generation. A pinned revision that is
   gone, or an Addon with no revision yet, holds the object `Pending` with `AddonNotFound`, naming the revision.
3. **An App's revision.** An AppRevision gains `spec.addonRevisions`, one `{kind, revision}` per add-on kind of its
   parts, set at its stamp: for a spec equal to an older kept revision's (a rollback, ADR-0200 Decision 3), that
   revision's records; otherwise each kind's current revision. A part pins the record of its App's newest AppRevision
   whose `Applied` condition is True, and the current revision when that revision records none of its kind. The App's
   pre-hooks run before `Applied` turns True (ADR-0214), so they see the old engine. A part counts as Ready for
   `ChildrenReady` only once it pins its App's latest record, so the post-hooks see the new engine.
4. **Removal.** The Addon reconciler removes a revision that is not current, that no add-on object pins, and that no
   kept AppRevision records. Each pass, the Addon's, an object's and the App's, is one step of the controller's one
   worker (`pkg/funcd/funcd.go:767`), so a pass that reads the current revision pins it before any removal runs.
5. **Watches.** An AddonRevision's or an AppRevision's change requeues the objects that pin it or whose App it belongs
   to, and an object's change requeues its Addon's reconciler, which removes what nothing uses any more.

## Design model

- **Spec**: [`docs/design/addon/revisions.fizz`](../design/addon/revisions.fizz) · state machine
  [`revisions.md`](../design/addon/revisions.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `PinsExist` | always | `scenario: addon-revision-retired`, `scenario: addon-revision-new-object` | `TestScenarioAddonRevisionRetired` |
  | `RecordsExist` | always | `scenario: addon-revision-app-rollback` | `TestScenarioAddonRevisionAppRollback` |
  | `UnusedAreRemoved` | always eventually | `scenario: addon-revision-retired` | `TestScenarioAddonRevisionRetired` |

- **Bounds**: one Addon, one standalone object, one App with one part; 2 updates, 1 edit, 2 App stamps, 1 rollback, a
  kept history of two App revisions; 1 concurrent action (each pass is one step of the one worker); 24 actions.
- **Faults**: none; the writes of the admin and the users come at any point between passes.
- **Environment**: the controller's one worker serializes the passes. The mutant `split-pass`, a pass that reads and
  pins in two steps, as two workers would, pins a removed revision: a second worker needs this decision again.
- **Mutants**: `split-pass` and `retire-current` fail `PinsExist`; `ignore-records` fails `RecordsExist`;
  `never-retire` fails `UnusedAreRemoved`.
- **Result**: pass, 3,396 states, 1.3 s, FizzBee v0.5.4; wider (3 updates, 30 actions): pass, 13,743 states.
- **Left to tests**: the engines the pins start and replace, several kinds per App, the hooks' calls.

## Behavior rows

- **addons.md**: "An Addon's change" (a new revision; no engine moves), "Which revision an object runs" (the pin and
  what moves it), "An unused revision" (removed), all `decided`.
- **apps.md**: "An App's revision with add-on parts" (records each kind's revision; a rollback takes back its records),
  `decided`.

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

// AddonObjectStatus.AddonRevision: the revision the object's engine runs.
AddonRevision ObjectName `json:"addonRevision,omitempty"`

// AppRevisionSpec.AddonRevisions: each add-on kind's revision for the App's parts.
AddonRevisions []AppAddonRevision `json:"addonRevisions,omitempty"`

type AppAddonRevision struct {
	Kind     string     `json:"kind"`
	Revision ObjectName `json:"revision"`
}
```

| Consumes | Exposes |
|---|---|
| the Addon's generation; the AppRevision's stamp and `Applied` (ADR-0200) | `AddonRevision`, read-only; `status.currentRevision`; `status.addonRevision` |
| the controller's one worker | the GC pair `(Addon, AddonRevision)`; `AppRevision.spec.addonRevisions` |

## Implementation plan

- `api/types/v1alpha1`: `AddonRevision`, the status fields, `AppAddonRevision`; the read-only routes; regenerate the
  spec and kindref.
- `internal/services/addon`: an Addon reconciler (stamp, current, removal); the object's pass reads its pin's revision;
  the watches. `internal/app`: the stamp's records, the parts' readiness rule. `internal/gc`: the pair.
- Docs: the add-ons pages (upgrading an add-on), the App pages (rollback with add-on parts), the behavior rows.
- Tests: one per Scenario, named after it, in `internal/services/addon`, `internal/app` and `pkg/funcd`; the oracle
  markers; `just check-specs`. Real platform: a case of the lane `addon-rauthy` that updates rauthy's Addon, sees no
  engine replaced, then moves the App's part with a new revision and rolls it back.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] An Addon's change stamps a revision and moves no engine.
- [ ] An engine is built only from its object's pinned revision.
- [ ] A part moves only with its App's `Applied` revision, and a rollback takes back the older records.
- [ ] No pinned, recorded or current revision is ever removed; an unused one is.
- [ ] AddonRevisions have no write route.

## Consequences

- An admin's change reaches a namespace only when its objects are edited or its Apps move them, so versions skew
  across namespaces by design; `funcdctl get identityproviders` shows each object's `addonRevision`.
- An older template deployed by an admin makes an older spec current, under a new number: objects that move later
  take it.
- A rollback works while the older AppRevision is kept (`app.revisionHistory`), since its records protect its
  revisions.
- Removal relies on the controller's one worker; a controller with more workers, or the multi-node control plane,
  must decide it again (the `split-pass` mutant).

## Open questions

- A standalone object pinned to an older revision by hand: the decider left it to an App for now.

## References

- FEAT-0019 tracker #1506.
- Function revisions: ADR-0020, ADR-0143, ADR-0172; App revisions: ADR-0200.
