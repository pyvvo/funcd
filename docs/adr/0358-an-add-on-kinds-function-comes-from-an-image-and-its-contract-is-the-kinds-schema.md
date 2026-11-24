# ADR-0358: An add-on kind's Function comes from an image, and its contract is the kind's schema

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: addons, functions, contracts, admission
- **Realizes**: [FEAT-0019/F275](../feat/0019-feat-addon-apps.md) (an add-on's kinds bring their Function)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decision 1 (`schema` goes, `function` gains `image`), Decision 2
  (a settings kind's checks: Decision 4 here), Decision 3 (what a write is checked against: Decision 5) and Decision 5
  (what types a rule: Decision 4);
  [ADR-0341](0341-a-function-applies-a-targets-settings.md) Decision 1 (an object that does not fit blocks its call:
  Decision 5) and Decision 3 (funcd creates an image's Function: Decisions 1 and 2);
  [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) Decision 1 (`config`
  goes), Decision 2 (an image's group is one object) and Decision 3 (an engineless object's bindings);
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) Decision 1 (when a revision is stamped).
- **Relates to**: [ADR-0096](0096-engine-native-builtin-steps.md) (a step's `image` and `ref`) ·
  [ADR-0094](0094-workflow-engine-core.md) (the Function a Workflow creates) ·
  [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) Decisions 2 and 3 (a Job's
  `status.contract`, and a run checked against it) ·
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) Decision 5 (an Addon's images)

## Context & Need

An Addon declares the schema of its settings kinds, and of an engineless kind, for objects that only a Function reads.
That Function's contract, which funcd checks on every call (ADR-0303), is `{}`. So one job has two mechanisms, and they
drift: the Addon may add a field that an older Function ignores, and the object is `Ready` with the field never applied
(reproduced on 2026-11-24 with rauthy's settings Function). The decider chose on 2026-11-24 that the contract is the
schema, and that a kind's Function may come from an image, as a Workflow step's does, so that every namespace runs the
same code and contract. Callers: the admin who writes the Addon, the developers who write its objects, the reconciler.

## Scenarios

Given rauthy's Addon, whose `RauthyPasswordPolicy` names `function.image` with `secretKeys: [BOOTSTRAP_API_KEY_SECRET]`,
and the IdentityProvider `login`, which binds the Secret `rauthy-keys`, unless a scenario says otherwise.

- `scenario: addon-function-image` — *When* `login` is applied, *Then* the Function `login-rauthypasswordpolicy` exists,
  controlled by `login`, runs the image's `handle` on the runtime the artifact declares, binds `login`, and reads of
  `rauthy-keys` only `BOOTSTRAP_API_KEY_SECRET`; *when* the policy `strict` is created, *Then* rauthy enforces it.
- `scenario: addon-function-follows-the-pin` — *When* the Addon names a newer image, *Then* `login`'s Function keeps its
  image until an edit of `login` moves its pin, and its next call has cause `change`.
- `scenario: addon-function-repaired` — *When* `login-rauthypasswordpolicy` is edited or deleted by hand, *Then* it is
  back as it was; *when* `login` is deleted, *Then* it goes.
- `scenario: addon-function-not-owned` — *Given* a user's Function `login-rauthypasswordpolicy`, *Then* it is left as it
  is, and `strict` is `Pending` with `FunctionNotOwned`.
- `scenario: addon-function-ref` — *Given* `function.ref`, *Then* funcd creates nothing, a write of `strict` passes with
  any `config`, and a call whose data breaks that Function's contract fails, `ReconcileFailed` naming the path.
- `scenario: addon-contract-is-the-schema` — *When* `strict` sets a property that the contract's `config` lacks, or a
  value of another type, *Then* it gets 422 naming the property, as today.
- `scenario: addon-contract-missing` — *When* the Addon names an image whose contract describes no `config` for the
  kind, such as `input: {}`, *Then* the Addon is `Ready=False` with `ContractInvalid`, naming the kind, and
  `status.currentRevision` stays the former.
- `scenario: addon-contract-unread` — *When* the image cannot be read, *Then* the Addon is `Ready=False` with
  `ArtifactNotReady`, and becomes Ready once it can.
- `scenario: addon-constraint-unfit` — *When* a rule, `target`, `unique` or `immutable` names a field that the contract
  lacks, *Then* the Addon is `Ready=False` with `SettingsInvalid`, naming it, and the former revision stays current.
- `scenario: addon-config-no-longer-fits` — *Given* `strict` applied, *When* `login` moves to a revision whose contract
  refuses `strict`, *Then* `strict` is `Pending` with `ConfigInvalid` naming the property, no call is made for `login`
  and the kind, and rauthy keeps the policy; *when* an edit makes it fit, *Then* the next call carries it.
- `scenario: engineless-function-image` — *Given* InfraStack with `function.image`, *When* the stack `bunny-edge` binds
  the Job `bunny-edge` as `stack`, *Then* the Function `bunny-edge-infrastack` binds that Job, and starts its run.
- `scenario: engineless-function-outlives` — *When* `bunny-edge` is deleted with `deletion: delete`, *Then* its Function
  stays until its ok removes `bunny-edge`.
- `scenario: addon-object-bindings-refused` — *When* an object whose kind's Function is a `ref`, or a settings object,
  sets `bindings.job`, or one whose kind has an image sets `spec.functions` for it, *Then* it gets 400 naming the entry.

## Scope

In: the two sources of a kind's Function, its creation, the contract as the schema, what a write and a call are checked
against. Out: an engine kind's `config`, which funcd reads itself for `env`; publishing an add-on's Function images
(pyvvo/funcd-addons); a kind's Function in Python beyond what any artifact allows; migration (no node is deployed).

## Constraints & Decision drivers

- One mechanism per job (`.claude/rules/reuse-before-design.md`): a Workflow step's `image` and `ref`, a Job's
  `status.contract`, `internal/ownedstore`, the engine's `ConfigInvalid`.
- An Addon is registered with no registry call, as today; its images stay pinned by digest, from a listed registry.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep `schema` in the Addon and fill the contract too | two copies of one schema, kept in step by hand |
| A new `kinds` section in the contract file, each kind's schema apart | a second way to describe what code takes; an App hook's Function already types its event in `input` |
| `ref` only, its Function's contract as the schema | two namespaces on different versions accept different fields, and nothing checks before the Function is deployed |
| Read the contract when the Addon is applied, and refuse it there with 400 | an admission that waits on a registry under the cluster-scoped lock; a Workflow and a Job are stored first and read their images in their pass |
| Leave an object that does not fit out of the list | the Function would put the engine's default back (`leave-out`, Design model) |
| One Function per namespace and kind | two objects may pin two revisions, so one Function could not run both images |

## Decision

1. **The source.** A settings kind's `function`, and an Engineless entry's, names exactly one of `image` and `ref`, as a
   Workflow step does (ADR-0096 Decision 1), else 400. `image` is pinned by digest, from a registry that
   `addons.registries` lists, by the rule of an Addon's other images (ADR-0344 Decision 5), and `secretKeys`, which only
   an image takes, lists keys. A `ref`'s Function is as ADR-0341 Decision 3 and ADR-0345 Decision 3 say, and so is an
   object's own in `spec.functions`, which is refused with 400 for a kind whose `function` is an image.
2. **The Function of an image.** For each add-on object, the add-on reconciler keeps one Function per settings kind of
   the revision it pins, and one for an engineless object, from that revision's images. Its name is
   `<object>-<kind in lower case>`; an object whose name makes one longer than 63 characters is refused at its create
   with 400. It is written as a Workflow's step Function is (ADR-0094): in the object's namespace and resource group,
   with the object's marker and controller reference (`internal/ownedstore`, whose owners gain AddonObject), the runtime
   the artifact declares, the export `handle`, no minimum replica and the step's idle timeout. It binds:
   - the object itself in `addon`, as `self`, when the object runs an engine; that alias goes in the call;
   - the object's `bindings.secret`, each declared with those keys of `secretKeys` that it has;
   - an engineless object's `bindings.job` (new), which only an object whose kind's Function is an image may set, and
     which in an App's part names a Job the App declares, as `bindings.secret` does (ADR-0337 Decision 3).

   A change by hand is put back at the next pass, and a Function whose kind the pinned revision no longer names with an
   image is deleted. A Function of that name without the object's marker is left as it is, and the objects it would serve
   wait with `FunctionNotOwned`, the Workflow's reason. An image's group, for an engineless kind's call, is its one
   object.
3. **The schema.** A settings kind has no `schema`, and an Engineless entry no `config`. A kind's schema is what its
   Function's contract says of one object's `config`: in `input`, the schema at `objects[].config`, or that of the
   member whose `kind` is this kind when `input` is a union discriminated by `kind`. The call's data keeps its shape
   (ADR-0341 Decision 2, ADR-0345 Decision 2): a closed contract refuses a new field, so a change of that shape is a new
   event type.
4. **Reading it.** The Addon's pass stamps a revision (ADR-0339 Decision 1) once it has read, by digest and from
   registry metadata as a Job's is (ADR-0347 Decision 2), the contract and the runtime of each image the spec names; it
   keeps each in `AddonRevision.spec.contracts`, so no object's pass calls a registry. `target`, `unique`, `immutable`
   and the rules of a kind with an image are checked against its schema there, not when the Addon is written. Until all
   of it holds, the Addon is `Ready=False`: `ArtifactNotReady` (tried again each `runtime.supervisionPeriod`),
   `ContractInvalid` (it does not compile, or describes no `config` for the kind) or `SettingsInvalid` (new), each
   naming the kind; `status.currentRevision` stays the former, and the Addon is `Ready` once it is its generation's. A
   kind with a `ref` has no schema: its `target`, `unique` and `immutable` are read from each object as it is, and it
   takes no rules (400).
5. **Checking against it.** A write of an object is checked against the contract in the Addon's current revision: 422
   naming the property, as today. A pass checks it again against the revision that serves it, its own pin or its
   target's: one that does not fit is `Pending` with `ConfigInvalid`, as an engine's object is (ADR-0332 Decision 3),
   naming each property, and no call carries its list until it fits or goes. At the call, the contract is checked as any
   Function's (ADR-0303).

## Design model

- **Spec**: [`docs/design/addon/settings.fizz`](../design/addon/settings.fizz) · state machine
  [`settings.md`](../design/addon/settings.md); ADR-0341's, with the object that stops fitting
- **Assertions** added:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `UnfitKeepsItsSetting` | always | `scenario: addon-config-no-longer-fits` | `TestScenarioAddonConfigNoLongerFits` |
  | `UnfitIsNeverSent` | always | `scenario: addon-config-no-longer-fits` | `TestScenarioAddonConfigNoLongerFits` |
  | `Converges` | always eventually | the same, once an edit makes it fit | `TestScenarioSettingsDrift` |

- **Bounds**: ADR-0341's, and 1 object that stops fitting; 1 concurrent action, 44 actions: every mutant fails at them.
- **Faults**: ADR-0341's; the object stops fitting at any step, a call in flight included.
- **Environment**: a write is checked, so it fits; only a move of the target's pin makes a stored object unfit. A
  revision stamped later is `revisions.fizz`'s `Stamp`, which may already run at any later step, so that spec is
  unchanged, and so is `engineless-delete.fizz`: the Function is the stack's own, collected after it.
- **Mutants**: `leave-out` fails `UnfitKeepsItsSetting`; `send-unfit` fails `UnfitIsNeverSent`; `stay-blocked` fails
  `Converges`.
- **Result**: pass, 73,585 states, 25.2 s, FizzBee v0.5.4.
- **Left to tests**: several objects of one target, where one that does not fit stops its siblings' calls; the created
  Function's repair and removal; the registry's answers.

## Behavior rows

- **addons.md**: "An Addon's config and env" and "A settings object's write" (the schema's source); new rows "An add-on
  kind's Function from an image" (its name, owner, bindings, `FunctionNotOwned`), "An Addon whose image's contract
  cannot serve" (the three reasons; the former revision stays current) and "A settings object that no longer fits"
  (`ConfigInvalid`, no call); "A target's own Function for a settings kind" (a `ref` kind only).
- **engineless-kinds.md**: "An engineless object's Function", "An engineless object's engine fields" (`bindings.job`)
  and "An engineless kind's call" (one object for an image).

## Temporary workarounds

None.

## Contracts

```go
// AddonFunction is the Function of a settings or an engineless kind, as a Workflow step's is (ADR-0096); it replaces
// AddonSettingsFunction.
type AddonFunction struct {
	Image      string     `json:"image,omitempty"`      // an artifact pinned by digest; exactly one of image and ref
	Ref        ObjectName `json:"ref,omitempty"`
	SecretKeys []string   `json:"secretKeys,omitempty"` // with image
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
# rauthy's settings kind
- kind: RauthyPasswordPolicy
  target: spec.config.provider
  resync: 10m
  function:
    image: ghcr.io/pyvvo/rauthy-settings@sha256:0000000000000000000000000000000000000000000000000000000000000000
    secretKeys:
      - BOOTSTRAP_API_KEY_SECRET
  constraints:
    count:
      perTarget: 1
    immutable:
      - spec.config.provider
    rules:
      - rule: ${{ object.spec.config.lengthMin <= object.spec.config.lengthMax }}
        message: lengthMin must not exceed lengthMax
```

| Consumes | Exposes |
|---|---|
| an image's contract and runtime from its registry; `runtime.supervisionPeriod` | `function.image`, `function.secretKeys`; the Function `<object>-<kind>` |
| the pinned AddonRevision; the object's bindings | `AddonRevision.spec.contracts`; `bindings.job`; the Addon's `Ready`; `SettingsInvalid`, `FunctionNotOwned`, `ConfigInvalid` |

## Implementation plan

- `api/types/v1alpha1`: the types, `Validate`; regenerate the spec and the Spec pages.
- `internal/services/addon`: `functions.go` (the created Functions, over `ownedstore.Ensure`, and their watch),
  `revisions.go` (the read and the checks before a stamp), `settings.go` and `objects.go` (the block, the alias `self`),
  `settings_kinds.go` (`KindSchema`). `internal/controlplane/addon.go` and `internal/app/admission.go`: the checks of a
  write. `pkg/funcd`: the contract and runtime readers the Workflow and the Job use. `internal/ownedstore`: AddonObject.
- pyvvo/funcd-addons, after funcd's release: rauthy-settings and infrastack-reconciler export `handle`, carry their
  contracts, are published by digest, and are named by `image`; their templates lose the Function and its push.
- Docs: the add-ons pages, `api/types/v1alpha1/examples/addons`, the behavior rows, the blueprint's add-on paragraph.
- Tests: one per Scenario, named after it, and the oracle markers. Real platform: the cases of pyvvo/funcd-addons'
  `addon-rauthy` and `addon-infrastack` lanes with the images, on containerd.
- Done: `just ci`, `just check-specs` and `just check-behavior` green; both lanes pass.

## Review checklist

- [ ] No field of an Addon describes an object that only a Function reads.
- [ ] Registering an Addon calls no registry; a revision is stamped only once each image's contract is read and fits.
- [ ] A created Function has its object's marker, its bindings and the listed keys only; another's is never written.
- [ ] Its image is that of the revision its object pins.
- [ ] An object that does not fit is never sent, and its list is never sent without it.
- [ ] A `ref` kind creates nothing and is checked at its call alone.

## Consequences

- An add-on's kinds work once its Addon is registered: no Function manifest and no push in a template.
- Each target has one more Function per settings kind, and each engineless object one, stopped when idle; a short resync
  keeps one awake.
- A bad image or constraint shows in the Addon's status, after its apply, no longer as a 400.
- While an object does not fit, its target's other objects of the kind wait too, and a drift is not repaired.
- A settings Function's contract repeats the call's envelope, which funcd cannot change without a new event type.
- An image runs the admin's code in each namespace with the listed keys, as an engine's does (ADR-0332 Consequences).

## Open questions

None.

## References

- FEAT-0019 (the feature issue is filed at acceptance); #1516 and #1558, which added the two schemas; #1574, the rule.
- The decider's choices of 2026-11-24: the contract is the schema (A); `image`, for settings and engineless kinds alike.
