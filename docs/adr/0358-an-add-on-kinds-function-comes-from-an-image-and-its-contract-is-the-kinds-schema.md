# ADR-0358: An add-on kind's Function comes from an image, and its contract is the kind's schema

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: addons, functions, contracts, admission
- **Realizes**: [FEAT-0019/F275](../feat/0019-feat-addon-apps.md) (an add-on's kinds bring their Function)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decisions 1 (`schema` goes, `function` gains `image`), 2 and 5 (a
  kind's checks: Decision 4 here) and 3 (what a write is checked against: Decision 5);
  [ADR-0341](0341-a-function-applies-a-targets-settings.md) Decisions 1 (where a kind is read: Decision 4) and 3 (an
  image's Function: Decisions 1 and 2);
  [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) Decisions 1
  (`config` goes), 2 (an image's group is one object) and 3 (an image's Function, an engineless object's bindings);
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) Decision 1 (when a revision is stamped);
  [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 2 (when deploy's wait ends).
- **Relates to**: [ADR-0096](0096-engine-native-builtin-steps.md) (a step's `image` and `ref`) ·
  [ADR-0094](0094-workflow-engine-core.md) (the Function a Workflow creates) ·
  [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (a Job's contract) ·
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (an Addon's images)

## Context & Need

An Addon declares the schema of its settings kinds, and of an engineless kind, for objects that only a Function reads.
That Function's contract, which funcd checks on every call (ADR-0303), is `{}`. So one job has two mechanisms, and they
drift: the Addon may add a field that an older Function ignores, and the object is `Ready` with the field never applied
(reproduced on 2026-11-24 with rauthy's settings Function). The decider chose on 2026-11-24 that the contract is the
schema, and that a kind's Function may come from an image, as a Workflow step's does, so that every namespace runs the
same code and contract. Callers: the admin who writes the Addon, the developers who write its objects, the reconciler.

## Scenarios

Given rauthy's Addon, whose `RauthyPasswordPolicy` names `function.image`, and the IdentityProvider `login`, which binds
the Secret `rauthy-keys`, unless a scenario says otherwise.

- `scenario: addon-function-image` — *When* `login` is applied, *Then* the Function `login-rauthypasswordpolicy` exists,
  controlled by `login`, runs the image's `handle` on the runtime its artifact declares, binds `login` and
  `rauthy-keys`, and has no address: `/function/login-rauthypasswordpolicy` answers 404. *When* the policy `strict` is
  created, *Then* rauthy enforces it.
- `scenario: addon-function-follows-the-pin` — *When* the Addon names a newer image, *Then* `login`'s Function keeps its
  image until an edit of `login` moves its pin, and its next call has cause `change`.
- `scenario: addon-function-repaired` — *When* `login-rauthypasswordpolicy` is edited or deleted by hand, *Then* it is
  back as it was; *when* `login` is deleted, *Then* it goes; *given* a user's Function of that name, *Then* it is left
  as it is, and `strict` is `Pending` with `FunctionNotOwned`.
- `scenario: addon-function-ref` — *Given* `function.ref`, *Then* funcd creates nothing, a write of `strict` is checked
  against no schema, and a call off that Function's contract fails, `ReconcileFailed` naming the path.
- `scenario: addon-contract-is-the-schema` — *When* `strict` sets a property that the contract's `config` lacks, or a
  value of another type, *Then* it gets 422 naming the property, as today.
- `scenario: addon-contract-missing` — *When* the Addon names an image with no contract, or one that describes no
  `config` for the kind, such as `input: {}`, *Then* the Addon is `Ready=False` with `ContractMissing` or
  `ContractInvalid`, and its current revision stays the former. *When* the image cannot be read, *Then* the reason is
  `ArtifactNotReady`, until it can.
- `scenario: addon-constraint-unfit` — *Given* `strict` applied, *When* an edit of the Addon gives the kind a rule, a
  `target`, a `unique` or an `immutable` field that the contract lacks, *Then* the Addon is `Ready=False` with
  `SettingsInvalid`, naming it, and rauthy keeps `strict`; *when* an admin's `app deploy` made that edit, *Then* it
  prints the reason, exits 1 and applies no App.
- `scenario: addon-object-first` — *When* `strict` is created while the Addon has no revision, *Then* it is stored and
  `Pending` with `Progressing`, and turns Ready once the revision is there.
- `scenario: addon-config-no-longer-fits` — *Given* `strict` applied, *When* `login` moves to a revision whose contract
  refuses `strict`, *Then* its call is refused before the Function runs, `strict` is `ReconcileFailed` naming the path,
  and rauthy keeps the policy; *when* an edit makes it fit, *Then* the next call applies it.
- `scenario: engineless-function-image` — *Given* InfraStack with `function.image`, *When* the stack `bunny-edge` binds
  the Job `bunny-edge` as `stack`, *Then* the Function `bunny-edge-infrastack` binds that Job, and starts its run.
- `scenario: engineless-function-outlives` — *When* `bunny-edge` is deleted with `deletion: delete`, *Then* its Function
  stays until its ok removes `bunny-edge`.
- `scenario: addon-object-bindings-refused` — *When* a settings object, an object that runs an engine, or one whose
  kind's Function is a `ref` sets `bindings.job`, or one sets `spec.functions` for a kind with an image, *Then* 400
  naming the entry.

## Scope

In: the two sources of a kind's Function, its creation, the contract as the schema, and what a write and a call are
checked against. Out: an engine kind's `config`, which funcd reads itself for `env`; publishing an add-on's Function
images (pyvvo/funcd-addons); migration (no node is deployed).

## Constraints & Decision drivers

- One mechanism per job (`.claude/rules/reuse-before-design.md`): a Workflow step's `image` and `ref`, a Job's contract
  read in its pass, `internal/ownedstore`, the contract check of every call.
- An Addon is registered with no registry call, as today; its images stay pinned by digest, from a listed registry.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep `schema` in the Addon and fill the contract too | two copies of one schema, kept in step by hand |
| A new `kinds` section in the contract file, each kind's schema apart | a second way to describe what code takes; an App hook's Function already types its event in `input` |
| `ref` only, its Function's contract as the schema | two namespaces on different versions accept different fields, and nothing checks before the Function is deployed |
| Read the contract when the Addon is written, and refuse it there with 400 | a write that waits on a registry; a Workflow and a Job are stored first and read their images in their pass |
| Stamp the revision at once and report its checks after | a `target` that names no field would empty each list, and the Function would put each engine's default back (mutant `read-the-spec`) |
| Check each object again in its target's pass | the call's contract already refuses its list; the gain is a status that names the object, not its place in the list |
| One Function per namespace and kind | two objects may pin two revisions, so one Function could not run both images |

## Decision

1. **The source.** A settings kind's `function`, and an Engineless entry's, names exactly one of `image` and `ref`, as a
   Workflow step does (ADR-0096 Decision 1), else 400. `image` is a Function's artifact, pinned by digest, from a
   registry that `addons.registries` lists, by the rule of an Addon's other images (ADR-0344 Decision 5); its kind has
   at most 40 characters (400). A `ref`'s Function is as ADR-0341 Decision 3 and ADR-0345 Decision 3 say, and so is an
   object's own in `spec.functions`, which is refused with 400 for a kind whose `function` is an image.
2. **The Function of an image.** For each add-on object, the add-on reconciler keeps one Function per settings kind of
   the revision it pins, and one for an engineless object, from that revision's images. Its name is
   `<object>-<kind in lower case>`, shortened with a hash past 63 characters as an AddonRevision's is. It is written as
   a Workflow's step Function is (ADR-0094): in the object's namespace and resource group, with the object's marker and
   controller reference (`internal/ownedstore`, whose owners gain AddonObject, and the owner collector's new pair
   `(AddonObject, Function)`), the runtime the artifact declares, the export `handle`, no minimum replica and the step's
   idle timeout. Its own address is off (`ingress.enabled: false`), since its caller is funcd. It binds:
   - the object itself in `addon`, as `self`, when the object runs an engine; that alias goes in the call;
   - the object's `bindings.secret`;
   - an engineless object's `bindings.job` (new), as a Function's (ADR-0349 Decision 1).

   An engineless object may set these two bindings only when its kind's Function is an image. A change by hand is put
   back at the next pass, and a Function whose kind the pinned revision no longer names with an image is deleted. A
   Function of that name without the object's marker is left as it is, and the objects it would serve wait with
   `FunctionNotOwned`, the Workflow's reason. An image's group, for an engineless kind's call, is its one object.
3. **The schema.** A settings kind has no `schema`, and an Engineless entry no `config`. A kind's schema is what its
   Function's contract says of one object's `config`: in `input`, the schema at `objects[].config`, or that of the
   member whose `kind` is this kind when `input` is a union discriminated by `kind`. The call's data keeps its shape
   (ADR-0341 Decision 2, ADR-0345 Decision 2): a closed contract refuses a new field, so a change of that shape is a new
   event type.
4. **Checking the Addon.** The Addon's pass stamps a revision (ADR-0339 Decision 1) once the images of its spec fit. It
   reads each one's contract and runtime by digest from registry metadata, as a Job's contract (ADR-0347 Decision 2),
   and keeps them in `AddonRevision.spec.contracts`, so no object's pass calls a registry. `target`, `unique`,
   `immutable` and the rules of a kind with an image are checked against its schema there, no longer at the Addon's
   write. Until each check passes, the Addon is `Ready=False` (it reports no condition today), naming the kind, with
   `ArtifactNotReady` (tried again each `runtime.supervisionPeriod`), `ContractMissing`, `ContractInvalid` (it does not
   compile, or describes no `config` for the kind) or `SettingsInvalid` (new); `status.currentRevision` stays the
   former. The Addon is `Ready` once that is its generation's. `funcdctl app deploy`'s wait (ADR-0337 Decision 2) prints
   the reason, and on each but `ArtifactNotReady` ends with exit 1, applying no App.

   A settings kind is read from the current revision, no longer from the Addon as it is now (ADR-0341 Decision 1), so an
   Addon that fails its checks changes no list; its `function` is that of the revision the target pins, and with none
   there its objects wait with `FunctionNotFound`. A kind with a `ref` has no schema: its `target`, `unique` and
   `immutable` are read from each object as it is, and it takes no rules (400), which need a schema to type them.
5. **Checking an object.** A write of an object, alone or as an App's part, is checked against the contract and the
   constraints in the Addon's current revision: 422, or an App's 400, naming the property, as today. Until a revision
   that declares the kind is current, a write is checked against no contract and no rule, only the Addon's other
   constraints, as a JobRun's input is before its Job's contract is read (ADR-0347 Decision 3), and the object waits
   with `Progressing`. At the call, the contract is checked as any Function's (ADR-0303): a list with an object that the
   revision its target pins does not take is refused before the Function runs, and each of its objects is
   `ReconcileFailed`, naming the path.

## Design model

- **Spec**: [`docs/design/addon/settings.fizz`](../design/addon/settings.fizz) · state machine
  [`settings.md`](../design/addon/settings.md); ADR-0341's, with an edit of the Addon that fails its checks
- **Assertions** added:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `StoredIsNeverLeftOut` | always | `scenario: addon-constraint-unfit` | `TestScenarioAddonConstraintUnfit` |

- **Bounds**: ADR-0341's, and 1 edit that fails its checks, at any step; 1 concurrent action, 44 actions.
- **Faults**: ADR-0341's. A list that its contract refuses is a failed call, which the model has.
- **Environment**: a revision stamped late, or never, is `revisions.fizz`'s `Stamp`: that spec is unchanged, and passes
  with `Stamp` unfair too (49,793 states). `engineless-delete.fizz` is unchanged: the Function is the stack's own.
- **Mutant**: `read-the-spec` (the pass reads the kind from the Addon as it is now) fails `StoredIsNeverLeftOut`.
- **Result**: pass, 59,244 states, 18.5 s, FizzBee v0.5.4; the other mutants fail as ADR-0341 says.
- **Left to tests**: the created Function's repair and removal; a list whose one object does not fit; the registry.

## Behavior rows

- **addons.md**: "An Addon's config and env", "An object's config" and "A settings object's write" (the schema's
  source), "An Addon changed or deleted" (when a revision is stamped), "A settings object that cannot be applied"
  (`FunctionNotOwned`) and "A target's own Function for a settings kind" (a `ref` kind only); new rows "An add-on kind's
  Function from an image" (name, owner, bindings, no address), "An Addon whose images do not fit" (the four reasons; the
  former revision stays current) and "A list off its Function's contract" (refused before the Function runs).
- **engineless-kinds.md**: "An engineless object's Function", "An engineless object's engine fields" (its bindings) and
  "An engineless kind's call" (one object for an image). **funcdctl.md**: deploy's wait.

## Temporary workarounds

None.

## Contracts

```go
// AddonFunction is the Function of a settings or an engineless kind, as a Workflow step's is (ADR-0096); it replaces
// AddonSettingsFunction.
type AddonFunction struct {
	Image string     `json:"image,omitempty"` // an artifact pinned by digest; exactly one of image and ref
	Ref   ObjectName `json:"ref,omitempty"`
}

// AddonSettings loses Schema; AddonAttributes.Config is an Engine entry's only. Both Function fields are AddonFunction.

// AddonObjectBindings gains an engineless object's Jobs (Decision 2).
Job []FunctionJob `json:"job,omitempty"`

// AddonRevisionSpec gains the contract of each kind whose Function is an image (Decision 4).
Contracts []AddonKindContract `json:"contracts,omitempty"`

type AddonKindContract struct {
	Kind     string           `json:"kind"`
	Runtime  RuntimeName      `json:"runtime"` // the artifact's
	Contract WorkflowContract `json:"contract"`
}

// KindSchema is what contract says of one object's config of kind, nil when it says nothing (internal/services/addon).
func KindSchema(contract v1.WorkflowContract, kind string) json.RawMessage
```

```yaml
# rauthy's settings kind, in its Addon
- kind: RauthyPasswordPolicy
  target: spec.config.provider
  resync: 10m
  function:
    image: ghcr.io/pyvvo/rauthy-settings@sha256:0000000000000000000000000000000000000000000000000000000000000000
```

| Consumes | Exposes |
|---|---|
| an image's contract and runtime from its registry; `runtime.supervisionPeriod` | `function.image`; the Function `<object>-<kind>` |
| the Addon's current revision and the pinned one; the object's bindings | `AddonRevision.spec.contracts`; `bindings.job`; the Addon's `Ready`; `SettingsInvalid`; `FunctionNotOwned` on an object |

## Implementation plan

- `api/types/v1alpha1`: the types, `Validate`; regenerate the spec and the Spec pages.
- `internal/services/addon`: `functions.go` (the created Functions, over `ownedstore.Ensure`, and their watch),
  `revisions.go` (the read and the checks before a stamp), `settings.go` and `objects.go` (the current revision's kinds,
  the alias `self`), `settings_kinds.go` (`KindSchema`). `internal/controlplane/addon.go`: the checks of a write, the
  App's part included. `pkg/funcd`: the contract and runtime readers of the Workflow and the Job. `internal/ownedstore`
  and `internal/gc`: AddonObject. `cmd/funcdctl`: deploy's wait, and the Addon's reason in dev's status lines.
- pyvvo/funcd-addons, after funcd's release: rauthy-settings and infrastack-reconciler export `handle`, carry their
  contracts, are published by digest, and are named by `image`; their templates lose the Function and its push.
- Docs: the add-ons pages, `api/types/v1alpha1/examples/addons`, the behavior rows, the blueprint's add-on paragraph.
- Tests: one per Scenario, named after it, and the oracle markers. Done: `just ci`, `just check-specs` and
  `just check-behavior` green, and the `addon-rauthy` and `addon-infrastack` lanes of pyvvo/funcd-addons pass with the
  images, on containerd.

## Review checklist

- [ ] No field of an Addon describes an object that only a Function reads.
- [ ] Registering an Addon calls no registry; a revision is stamped only once each image's contract is read and fits.
- [ ] No list and no write reads a settings kind from a spec that failed its checks, once a revision declares it.
- [ ] A created Function has its object's marker and bindings and no address; another's is never written.
- [ ] Its image is that of the revision its object pins.
- [ ] A `ref` kind creates nothing, and its Function's contract is checked at its call alone.
- [ ] A deploy never waits on an Addon that only a new spec can make Ready.

## Consequences

- An add-on's kinds work once its Addon is registered: no Function manifest and no push in a template.
- Each target has one more Function per settings kind, and each engineless object one. A resync shorter than the step's
  idle timeout (5 min), such as InfraStack's 1 min, keeps its worker up.
- A bad image or constraint shows in the Addon's status, after its apply, no longer as a 400, and its revision waits,
  its engine's change included.
- While an object does not fit, its target's other objects of the kind wait too, and a drift is not repaired.
- A settings Function's contract repeats the call's envelope, which funcd cannot change without a new event type.
- An image runs the admin's code in each namespace, and reads the Secrets its object binds, as an engine's does
  (ADR-0332 Consequences).
- An object written before its Addon's first revision meets the kind's rules at its next write only.
- An author who edits a kind's Function under `funcdctl dev` names it by `ref` there, with no schema and no rules.

## Open questions

None.

## References

- FEAT-0019 (the feature issue is filed at acceptance); #1516 and #1558, which added the two schemas; #1574, the rule.
- The decider's choices of 2026-11-24: the contract is the schema (A); `image`, for settings and engineless kinds alike.
