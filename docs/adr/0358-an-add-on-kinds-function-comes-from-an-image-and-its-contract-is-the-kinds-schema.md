# ADR-0358: An add-on kind's Function comes from an image, and its contract is the kind's schema

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: addons, functions, contracts, admission
- **Realizes**: [FEAT-0019/F275](../feat/0019-feat-addon-apps.md) (an add-on's kinds bring their Function)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decisions 1 (`schema` goes, `function` gains `image`, `target` is
  fixed), 2 and 5 (a kind's checks: Decision 4 here), 3 and 4 (which revision a write is checked against: Decision 5);
  [ADR-0341](0341-a-function-applies-a-targets-settings.md) Decisions 1 (a kind is read from a pin: Decision 5), 3 (an
  image's Function: Decision 2) and 4 (two more reasons);
  [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) Decisions 1
  (`config` goes), 2 (an image's group is one object), 3 (an image's Function, an object's bindings) and 5 (two more
  reasons); [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) Decision 1 (what uses an object);
  [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 3 (the schema that a part's config fits);
  [ADR-0356](0356-a-stacks-outputs-reach-its-readers-through-the-secret-its-job-owns.md) Decision 8 (which Job a stack's
  outputs come from: Decision 2).
- **Relates to**: [ADR-0096](0096-engine-native-builtin-steps.md) (a step's `image` and `ref`) ·
  [ADR-0094](0094-workflow-engine-core.md) (the Function a Workflow creates) ·
  [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) ·
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) (a pin) ·
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (an Addon's images) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (a Job's contract)

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

- `scenario: addon-function-image` — *When* the policy `strict` names `login`, *Then* the Function
  `login-rauthypasswordpolicy` exists, controlled by `login`, runs the image's `handle` on the runtime its artifact
  declares, binds `login` and `rauthy-keys`, and has no address; and rauthy enforces `strict`.
- `scenario: addon-function-on-demand` — *Given* no policy ever named `login`, *Then* it has no such Function, and a
  policy set at rauthy's own API stays; *when* `strict` is created and deleted, *Then* rauthy's default is back, and the
  Function goes.
- `scenario: addon-function-follows-the-pin` — *When* the Addon names a newer image, *Then* `login`'s Function keeps its
  image until an edit of `login` moves its pin, and its next call has cause `change`.
- `scenario: addon-function-repaired` — *When* that Function is deleted by hand while `strict` names `login`, *Then* it
  is back; *when* `login` is deleted, *Then* it goes; *given* a user's Function of that name, *Then* it is left as it
  is, and `strict` is `Pending` with `FunctionNotOwned`.
- `scenario: addon-function-own` — *When* `login` names its own Function for the kind in `spec.functions`, *Then* that
  one is called, funcd's own goes, and a write is checked as a `ref`'s.
- `scenario: addon-function-ref` — *Given* `function.ref`, *Then* funcd creates nothing, a write of `strict` is checked
  against no schema, and a call off that Function's contract fails, `ReconcileFailed` naming the path.
- `scenario: addon-contract-is-the-schema` — *When* `strict` sets a property that the contract's `config` lacks, or a
  value of another type, *Then* it gets 422 naming the property, as today.
- `scenario: addon-contract-of-the-pin` — *Given* a newer revision whose contract adds `maxAge`, *When* `strict` sets
  it, *Then* 422 while `login` pins the former one, and stored once an edit of `login` moved it; an App's upgrade whose
  parts are both is admitted.
- `scenario: addon-kind-not-in-the-pin` — *Given* an admin adds the kind to a live Addon, *When* `strict` names `login`,
  which pins an older revision, *Then* it is `Pending` with `FunctionNotFound` and nothing is called, until an edit of
  `login` moves its pin.
- `scenario: addon-image-unread` — *When* the image cannot be read, *Then* the Addon is `Ready=False` with
  `ArtifactNotReady`, `login`'s engine runs, and `strict` is stored and `Pending` with `ArtifactNotReady`, until it can.
- `scenario: addon-constraint-unfit` — *When* a rule or a constraint names a field that the contract lacks, *Then* the
  Addon is `Ready=False` with `SettingsInvalid`, naming it, and a write that the rule judges gets 400; *when* a replace
  changes the kind's `target`, *Then* 400.
- `scenario: addon-config-no-longer-fits` — *When* `login` moves to a revision whose contract refuses `strict`, *Then*
  the call is refused before the Function runs, `strict` is `ReconcileFailed` naming the path, and rauthy keeps the
  policy; *when* an edit makes it fit, *Then* the next call applies it.
- `scenario: addon-part-with-function` — *Given* `login` as an App's part, *When* a revision drops it, *Then* it goes,
  and its Function with it.
- `scenario: addon-function-dev` — *When* a folder under `funcdctl dev` registers that Addon, *Then* the image's
  Function runs there, and `strict` turns Ready.
- `scenario: engineless-function-image` — *Given* InfraStack with `function.image`, *When* the stack `bunny-edge` binds
  the Job `bunny-edge` as `stack`, *Then* the Function `bunny-edge-infrastack` starts that Job's run; *when* the stack
  is deleted with `deletion: delete`, *Then* its Function stays until its ok removes it.
- `scenario: addon-function-refused` — *When* a `function` names none or both of `image` and `ref`, an image with no
  digest or from an unlisted registry, or a `ref` with rules; or an object sets `bindings.job` and is no engineless one
  whose kind has an image, *Then* 400 naming the field.

## Scope

In: the two sources of a kind's Function, its creation, the contract as the schema, what a write and a call are checked
against. Out: an engine kind's `config`, which funcd reads itself for `env`; other bindings of an image's Function (a
kind that needs KV, a Bucket or SQL names a `ref`); publishing an add-on's images; migration (no node is deployed).

## Constraints & Decision drivers

- One mechanism per job (`.claude/rules/reuse-before-design.md`): a Workflow step's `image` and `ref`, a Job's contract
  read in its pass, `internal/ownedstore`, the contract check of every call, an object's pin.
- An Addon is registered with no registry call, as today; its images stay pinned by digest, from a listed registry.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep `schema` in the Addon and fill the contract too | two copies of one schema, kept in step by hand |
| A new `kinds` section in the contract file | a second way to describe what code takes; an App hook's Function already types its event in `input` |
| `ref` only, its Function's contract as the schema | two namespaces on different versions accept different fields, and nothing is checked before the Function is there |
| Read the contract when the Addon is written, and refuse it there | a write that waits on a registry; a Workflow and a Job are stored first and read their images in their pass |
| Stamp a revision only once its images are read and fit | an unreachable registry would keep back the engine's change too, and a deploy's wait for the revision would need an end of its own |
| Read a kind from the Addon, or from its current revision | a write and its call would be checked against two contracts, and one edit by the admin would reach every namespace at once |
| A Function per target from the start | it would get an empty list, and put each engine's default back where nobody declared a thing (mutant `create-always`) |
| One Function per namespace and kind | two objects may pin two revisions, so one Function could not run both images |

## Decision

1. **The source.** A settings kind's `function`, and an Engineless entry's, names exactly one of `image` and `ref`, as a
   Workflow step does (ADR-0096 Decision 1), else 400. `image` is a Function's artifact, pinned by digest, from a
   registry that `addons.registries` lists, by the rule of an Addon's other images (ADR-0344 Decision 5); its kind has
   at most 40 characters, so that Decision 2's name fits (400). A `ref` is as today, and an object's own Function in
   `spec.functions` wins over both, as today (ADR-0341 Decision 3, ADR-0345 Decision 2).
2. **The Function of an image.** The add-on reconciler creates one for an object and a settings kind of it once an
   object of that kind names it, and one for an engineless object at its first pass. It is kept while such an object
   exists: what differs is put back, and a deleted one is back, at the next pass. With the last object gone it gets the
   empty list, and goes once the call that sent it ended well; it goes with its owner too, or once `spec.functions` or
   the pinned revision names another or none. Its name is `<object>-<kind in lower case>`, shortened with a hash past 63
   characters as an AddonRevision's. It is written as a Workflow's step Function (ADR-0094): in the object's namespace
   and resource group, with the object's marker and controller reference (`internal/ownedstore`, whose owners gain
   AddonObject, and the owner collector's new pair `(AddonObject, Function)`), the runtime the artifact declares, the
   export `handle`, no minimum replica and the step's idle timeout. It runs in a worker of its own, and its address is
   off (`ingress.enabled: false`), since its caller is funcd. It binds:
   - the object itself in `addon`, as `self`, when the object runs an engine; that alias goes in the call;
   - the object's `bindings.secret`;
   - an engineless object's `bindings.job` (new), as a Function's (ADR-0349 Decision 1). An InfraStack names its Job
     there, under the alias `stack`, in place of `config.job`, and `funcdctl infra outputs` reads that Job.

   An engineless object may set these two bindings only when its kind's Function is an image. A Function that an object
   controls is no user of it, so an App's prune can drop the object. A Function of that name without the object's marker
   is left as it is, and the objects it would serve wait with `FunctionNotOwned`, the Workflow's reason, where a missing
   `ref` reads `FunctionNotFound` (ADR-0341 Decision 4). An image's group, for an engineless kind's call, is its one
   object.
3. **The schema.** A settings kind has no `schema`, and an Engineless entry no `config`. A kind's schema is what its
   image's contract says of one object's `config`: in `input`, the schema at `objects[].config`, or that of the member
   whose `kind` is this kind when `input` is a union discriminated by `kind`; a contract that says nothing of it, such
   as `{}`, checks nothing. The call's data keeps its shape (ADR-0341 Decision 2, ADR-0345 Decision 2): a closed
   contract refuses a new field, so a change of that shape is a new event type.
4. **Reading an image.** An Addon is stored and its revision stamped as today. Its pass reads the contract and the
   runtime of each image, by digest and from registry metadata as a Job's contract (ADR-0347 Decision 2), into
   `AddonRevision.status.contracts`: once before it names a new revision current, whatever comes of it, so that a
   deploy's App is checked against them, and again each `runtime.supervisionPeriod` for what it could not read, in each
   of its revisions. The Addon then reports `Ready`, a condition it lacks today: True once the images of its current
   revision are read and its constraints fit their schemas, else False with `ArtifactNotReady` or `SettingsInvalid`
   (new), naming the kind. Nothing waits for it: engines start and objects are stored, and only an object whose image is
   not read waits, with `ArtifactNotReady`, after `TargetNotReady`. A contract that does not compile checks nothing at a
   write, and fails its Function, as any (ADR-0303 Decision 3). A kind with a `ref` takes no rules (400), which need a
   schema to type them.
5. **One revision per target.** A settings kind that the Addon declares is read whole, its Function, contract,
   constraints and resync, from the revision its target pins, or from the current one while the target does not exist or
   pins none, no longer from the Addon as it is now (ADR-0341 Decision 1). A replace of the Addon may not change a
   kind's `target` (400), so one field finds an object's target in every revision. A write of an object, alone or as an
   App's part, is checked against that contract, 422, or an App's 400, naming the property, and against those
   constraints; a target that is a part of the same App counts as pinning none, and an engineless object's write is
   checked against the current revision. While the pinned revision lacks the kind, an object has no Function and no
   call, and waits with `FunctionNotFound`, or with `Progressing` before the first revision; its write is checked
   against the current revision, or against the Addon's other constraints while none has the kind. A contract that is
   not read checks nothing, nor do its rules, as a JobRun's input before its Job's contract is read (ADR-0347 Decision
   3), and a write for a Function that `spec.functions` names is checked as a `ref`'s. At the call, the contract is
   checked as any Function's (ADR-0303): a list with an object that does not fit, as when its target moved, is refused
   before the Function runs, and each of its objects is `ReconcileFailed`, naming the path.

## Design model

- **Spec**: [`docs/design/addon/settings.fizz`](../design/addon/settings.fizz) · state machine
  [`settings.md`](../design/addon/settings.md); ADR-0341's, with the Function that the first object brings
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoObjectNoCall` (new) | always | `scenario: addon-function-on-demand` | `TestScenarioAddonFunctionOnDemand` |
  | `NothingLeftBehind` (new) | always | the same: the Function goes after the default is back | the same |
  | `Converges` | always eventually | the same, and ADR-0341's, while the Function is there | `TestScenarioSettingsDrift` |

- **Bounds, faults**: ADR-0341's. A list that its contract refuses is a failed call, sent again until an edit.
- **Environment**: a kind read from a frozen revision changes only when the pin moves, and a new image then changes the
  Function's revision, which the digest names. `engineless-delete.fizz` is unchanged: the Function is the stack's own.
- **Mutants** added: `create-always` fails `NoObjectNoCall`; `gone-with-the-last` (the Function goes before its empty
  list is sent) and `gone-after-any-answer` (after a failed call) fail `NothingLeftBehind`.
- **Result**: pass, 25,944 states, 9.0 s, FizzBee v0.5.4; the other mutants fail as ADR-0341 says.
- **Left to tests**: the Function's repair and removal, the in-use rule, targets on two revisions.

## Behavior rows

- **addons.md**: "An entry's attributes", "An object's config", "A settings object's write" and "An Addon's config and
  env" (the schema's source, the pin's contract), "An Addon changed or deleted" (when a kind's change applies), "A
  settings object that cannot be applied" (`FunctionNotOwned`, `ArtifactNotReady`, a kind that the pin lacks) and "A
  target's own Function for a settings kind"; new rows "An add-on kind's Function from an
  image", "An Addon's images and constraints" (`Ready`) and "A list off its Function's contract".
- **engineless-kinds.md**: "An engineless object's Function", "An engineless object's engine fields", "An engineless
  object that cannot be called" and "An engineless kind's call". **apps.md**: "A dropped part" and "An `addons` entry
  refused". **funcdctl-dev.md**: the folder mode's images.

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

// AddonSettings loses Schema; AddonAttributes.Config is an Engine entry's only, and its Function a *AddonFunction.

// AddonObjectBindings gains an engineless object's Jobs (Decision 2).
Job []FunctionJob `json:"job,omitempty"`

// AddonRevision gains a status: what funcd read of each kind's image (Decision 4).
type AddonRevisionStatus struct {
	Contracts []AddonKindContract `json:"contracts,omitempty"`
}

type AddonKindContract struct {
	Kind     string           `json:"kind"`
	Runtime  RuntimeName      `json:"runtime"`
	Contract WorkflowContract `json:"contract"`
}

// ImageReader reads an artifact's runtime and contract from its manifest, never its bundle (internal/services/addon).
// An error of any kind is tried again.
type ImageReader interface {
	Read(ctx context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error)
}

// KindSchema is what contract says of one object's config of kind, nil when it says nothing.
func KindSchema(contract v1.WorkflowContract, kind string) json.RawMessage
```

| Consumes | Exposes |
|---|---|
| an image's contract and runtime; `runtime.supervisionPeriod` | `function.image`; the Function `<object>-<kind>`; `AddonRevision.status.contracts` |
| the revision a target pins; the object's bindings | `bindings.job`; the Addon's `Ready`; `SettingsInvalid`; `FunctionNotOwned`, `ArtifactNotReady` on an object |

## Implementation plan

- `api/types/v1alpha1`: the types, `Validate`; regenerate the spec and the Spec pages.
- `internal/services/addon`: `functions.go` (the created Functions, over `ownedstore.Ensure`, and their watch),
  `revisions.go` (the read), `settings.go`, `objects.go` (the pin's kinds, `self`), `settings_kinds.go` (`KindSchema`).
  `internal/controlplane`: the checks of a write, the App's part included. `internal/ownedstore`, `internal/gc`:
  AddonObject, the pair, the in-use rule. `pkg/funcd`: the registry's reader, in dev too, whose images are no builds of
  the session. `cmd/funcdctl`: in folder mode, an image that is no build is pulled as a server's; `app delete`'s walk
  notes the Functions that an object controls.
- pyvvo/funcd-addons: rauthy-settings and infrastack-reconciler export `handle`, throw when an empty list cannot be
  applied, and carry their contracts; their images are published first, beside the old `addon.yaml`, which then names
  them once funcd's release has this. InfraStack's `config.job` goes: its handler and `funcdctl infra` read the binding
  `stack`, so a stack names its Job once. pyvvo/funcd-functions' frp-bunny drops its Function for the image.
- Docs: the add-ons pages, `api/types/v1alpha1/examples/addons`, the behavior rows, the blueprint's add-on paragraph.
- Tests: one per Scenario, named after it, and the oracle markers. Done: `just ci`, `just check-specs` and
  `just check-behavior` green; funcd-addons' `addon-rauthy` and `addon-infrastack` lanes pass with the images.

## Review checklist

- [ ] No field of an Addon describes an object that only a Function reads.
- [ ] No write and no object's pass calls a registry; an engine's start waits for no read to succeed.
- [ ] A list, its constraints and its call come from one revision, the target's.
- [ ] A created Function has its object's marker and bindings and no address; another's is never written.
- [ ] No Function and no call for a target that no object of the kind names, once its engine's default is back.
- [ ] An App drops an add-on part whose kind has an image.

## Consequences

- An add-on's kinds work once its Addon is registered: no Function manifest and no push in a template.
- An Addon's change reaches a target when its pin moves, its settings kinds included; a bad image shows in the Addon's
  status and on the objects that wait, no longer as a 400.
- An object written before its image is read meets the schema and the rules at its next write, and its call at once.
- While an object does not fit, its target's other objects of the kind wait too, and a drift is not repaired.
- Each target with such an object has one Function per kind, and each engineless object one, where one `ref` served a
  namespace: a JobRun's end calls each, and a resync under the idle timeout (5 min), as InfraStack's, keeps each up.
- A Function reads every key of the Secrets its object binds, where an engine gets its `secretKeys` alone.
- While a registry hangs, a read keeps the controller's worker up to 10 s, as a Job's and a Workflow's do.

## Open questions

None.

## References

- FEAT-0019 (the feature issue is filed at acceptance); #1516 and #1558, which added the two schemas; #1574, the rule.
- The decider's choices of 2026-11-24: the contract is the schema (A); `image`, for settings and engineless kinds alike;
  a stack names its Job in `bindings.job` alone.
