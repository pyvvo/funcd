# ADR-0344: An add-on ships runtimes, and its kind runs on one of them

- **Status**: Proposed (2026-11-23), not judged yet
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: addons, runtime, api, functions, jobs
- **Realizes**: [FEAT-0012/F139](../feat/0012-feat-infrastack.md) (runtimes from add-ons)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 1 (the Addon's spec: its kind and its
  engine's fields move into the runtime entry that runs the kind, and `runtimes`, the runtime modes, goes: Decisions 1
  to 3) and Decision 6 (the image rule covers every runtime's image: Decision 5);
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decision 1 (`settings` moves into the engine's entry:
  Decision 3); [ADR-0149](0149-runtime-availability.md) Decision 2 (a runtime may also come from an Addon: Decision 6)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the add-on provider runtime, which runs engines: not a
  runtime an Addon ships) · [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) (an AddonRevision
  freezes the whole spec, runtimes included) · [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)
  (funcd's embedded runtimes) · [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) (dev runs engines on Docker) ·
  ADR-0345 (engineless kinds, which add the `Engineless` workload)

## Context & Need

funcd ships its runtimes inside its binary (ADR-0054): `nodejs22` and `python314` run Functions, `duckdb` and `rqlite`
run the Lake's and the SQL database's engines. A tool such as OpenTofu is too large for that (35.8 MB on amd64 and
32.1 MB on arm64 compressed, plus about 30 MB for Python), its MPL-2.0 licence would ship in funcd's release, and each
of its upgrades would wait for one of funcd's. The decider chose on 2026-11-23 that an add-on ships the runtimes it
needs, so the InfraStack add-on (F141) ships OpenTofu's and funcd carries none. An Addon is today one engine kind
(ADR-0332 Decision 1); this ADR makes it a list of runtimes, one of which may run its kind. Callers: the admin who
writes the Addon, the developers whose Functions and Jobs name a runtime, the function reconciler's runtime gate, the
containerd driver, and the add-on reconciler. "The add-on runtime" of ADR-0087 stays the provider that runs engines; a
runtime that an Addon ships is an image, as `nodejs22` is.

## Scenarios

- `scenario: addon-runtime-function` — *Given* an Addon `deno` whose runtime `deno2` lists `Function`, *When* a
  Function sets `runtime: deno2` on a containerd server, *Then* its workers run `deno2`'s image, pulled by digest, and
  it turns Ready.
- `scenario: addon-runtime-wrong-workload` — *When* a Function names a runtime that lists only `Job` or `Engine`,
  *Then* it fails with `RuntimeUnavailable` naming the runtime's workloads, before any worker starts.
- `scenario: addon-runtime-process-driver` — *When* a Function names an Addon's runtime on the process driver,
  `funcdctl dev` included, *Then* it fails with `RuntimeUnavailable`: the process driver runs no image.
- `scenario: addon-runtime-pinned` — *Given* a Function Ready on `deno2`'s image A, *When* the admin updates `deno` to
  image B, *Then* the Function's current Revision keeps A, and its next Revision runs B.
- `scenario: addon-runtime-gone` — *When* the admin deletes `deno`, *Then* at its next pass a Function on `deno2`
  fails with `RuntimeUnavailable`: no Addon ships `deno2`.
- `scenario: addon-runtime-name-refused` — *When* an Addon names a runtime `nodejs22`, *Then* it gets 400; *when* it
  names one that another Addon ships, compared without case, *Then* it gets 409; and nothing is stored.
- `scenario: addon-engine-unchanged` — *Given* rauthy's Addon with its engine entry, *When* an IdentityProvider is
  applied, *Then* its engine runs from that entry's image and attributes, as under ADR-0332 Decision 3.
- `scenario: addon-shape-refused` — *When* an Addon has two entries that run a kind, an `Engine` entry with no image,
  `Engine` beside another workload, or an attribute its workloads do not take (`port` on a Function's runtime,
  `settings` outside an engine's entry), *Then* it gets 422 naming the field.
- `scenario: addon-runtimes-only` — *Given* an Addon whose entries list only `Function` or `Job`, *Then* it adds no
  kind, and its runtimes serve Functions and Jobs.

## Scope

**In**: the Addon's shape; workloads and attributes; runtime names; the image rule for every runtime; a Function on an
Addon's runtime (the gate, the pull, the Revision's pin); rauthy's manifest reshaped in pyvvo/funcd-addons and the pin
of that module bumped; the guide, the generated API reference and the behavior rows. **Out**: the `Engineless` workload
and its kinds (ADR-0345); the gate for Jobs (F137's ADR, with the rule of Decision 8); a protocol version for runtimes
built outside funcd; image signatures; workers of an Addon's runtime under `funcdctl dev`; wasm.

## Constraints & Decision drivers

- No node is deployed, so the Addon API may break with no migration.
- Reuse what decides availability and pulls today: ADR-0149's gate (`runtimeUnavailable`, which already refuses the
  `duckdb` engine image as a Function runtime) and the containerd driver's pull of what `Config.Pullable` admits.
- An add-on and its runtimes are released together, so one AddonRevision pins them (decided on 2026-11-23).
- The decider's shape of 2026-11-23: `spec` holds only `runtimes`; the kind, its config and its settings sit with the
  runtime that runs the kind; no `modes`; the long-running workload keeps the name `Engine`.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Runtime plugins an admin registers apart, through `RuntimeClass` (F139 as first written) | one kind for runtimes | an add-on and its runtimes would be two objects to keep in step | Rejected (decider) |
| Kind, config, settings and the engine's fields at the root, beside `runtimes` | fewer levels | an engineless kind and a runtime-only add-on fit it badly; `settings` could sit with no engine | Rejected (decider) |
| An `objects` block naming its runtime (`objects.engine.runtime`) | engine and engineless side by side | one more level and a reference to keep in step | Rejected (decider) |
| A `modes` field per runtime | room for another runtime mode | containerd is the only mode an image runs on; wasm will bring what it needs | Rejected (decider) |
| OpenTofu as a runtime funcd embeds (F142) | runs under the process driver | about 62 MB more in funcd's binary, and its licence in funcd's release | Dropped (decider) |

## Decision

1. **The shape.** `Addon.spec` holds `runtimes`, a list of entries, each with `name`, `image`, `workloads` and
   `attributes`. Its `kind`, `config` and `settings` sit in the attributes of the entry that runs the kind. The
   runtime modes (`spec.runtimes` of ADR-0332) go: every runtime is an image, which runs on containerd.
2. **Workloads.** `workloads` takes `Function` and `Job`, which Functions and Jobs may name, and `Engine`, which runs
   one engine of the image for each object of the entry's kind, as ADR-0332 Decision 3 does. `Engine` stands alone in
   an entry; `Function` and `Job` may be listed together. An Addon has at most one entry that runs a kind.
3. **Attributes.** An `Engine` entry's attributes are its `kind`, `config` and `settings` (ADR-0340's kinds,
   unchanged) and ADR-0332's engine fields, unchanged: `port` and `readiness` required, `basePath`, `privatePaths`,
   `storage`, `env`, `secretKeys`, `snapshot`, `restore`. A `Function` or `Job` entry takes no attribute yet.
   Admission refuses an attribute that the entry's workloads do not take, with 422 naming it.
4. **Names.** A runtime's name is a `RuntimeName`, a DNS label. It is never one of funcd's (`nodejs22`, `python314`,
   `duckdb`, `rqlite`), 400, and no two Addons ship the same name, compared without case, 409 under the Addon
   admissions' cluster-scoped lock (ADR-0340 Decision 2). A kind keeps ADR-0332 Decision 1's rules.
5. **Images.** Every entry with a workload has an image pinned by digest, from a registry that `addons.registries`
   lists, checked when the Addon is applied and pulled as an engine's is (ADR-0332 Decision 6).
6. **A Function on an Addon's runtime.** A Function names it in `spec.runtime`, as one of funcd's. The runtime gate
   (ADR-0149 Decision 2) fails it with `RuntimeUnavailable` when no Addon ships a runtime of that name that lists
   `Function`, naming what it lists, or when its node's driver runs no image (the process driver), and the message
   says which. On containerd, the Function's workers run the image its Revision records (Decision 7), which the driver
   pulls by digest from a registry that `addons.registries` lists.
7. **The pin.** A Revision records an Addon runtime's image when funcd creates it, in `spec.runtimeImage`, and its
   workers run that image: an Addon update changes no existing Revision, and the Function's next Revision records the
   Addon's image of that time. A Function whose runtime no Addon ships any more fails with `RuntimeUnavailable` at its
   next pass, as a Function whose runtime its node cannot serve does.
8. **Jobs.** A Job names a runtime that lists `Job` in the same way; F137's ADR applies Decisions 6 and 7 to Jobs.

## Design model

None. Nothing here adds a lifecycle, a timer or a race: the gate and the pin read the stored Addon when a pass runs or
a Revision is created. An object's pin of its AddonRevision stays ADR-0339's, modeled in
`docs/design/addon/revisions.fizz`, unchanged.

## Behavior rows

- **addons.md**: "An Addon's runtimes" (`addons.md:29`) changes: the runtimes an Addon ships, their names, workloads,
  images and attributes, and the refusals of Decisions 2 to 5; "An Addon's kind", "An Addon's image" and "An Addon's
  config and env" (`addons.md:26` to `28`) change: they read the entry that runs the kind.
- **functions.md**: the `RuntimeUnavailable` causes (`functions.md:38`) gain an Addon's runtime: none ships it, it
  does not list `Function`, or the driver runs no image; "A Function's Revision" gains `runtimeImage`.

## Temporary workarounds

None.

## Contracts

```go
// AddonSpec lists the runtimes an Addon ships; at most one runs the Addon's kind (ADR-0344).
type AddonSpec struct {
	Runtimes []AddonRuntime `json:"runtimes" minItems:"1"`
}

// AddonRuntime is one image the Addon ships and the workloads it serves.
type AddonRuntime struct {
	Name       RuntimeName      `json:"name"`
	Image      string           `json:"image,omitempty" pattern:"^[^:@\\s]+(:[0-9]+/[^:@\\s]+)?@sha256:[a-f0-9]{64}$"`
	Workloads  []Workload       `json:"workloads" minItems:"1"`
	Attributes *AddonAttributes `json:"attributes,omitempty"`
}

// Workload is a kind of work a runtime serves; ADR-0345 adds WorkloadEngineless.
type Workload string

const (
	WorkloadFunction Workload = "Function"
	WorkloadJob      Workload = "Job"
	WorkloadEngine   Workload = "Engine"
)

// AddonAttributes hold what an entry's workloads need: every field for an Engine entry, none for Function or Job.
type AddonAttributes struct {
	Kind         string            `json:"kind,omitempty" pattern:"^[A-Z][A-Za-z0-9]{1,62}$"`
	Config       json.RawMessage   `json:"config,omitempty"`
	Settings     []AddonSettings   `json:"settings,omitempty"`
	Port         int               `json:"port,omitempty" minimum:"1" maximum:"65535"`
	BasePath     string            `json:"basePath,omitempty"`
	PrivatePaths []string          `json:"privatePaths,omitempty"`
	Readiness    *AddonReadiness   `json:"readiness,omitempty"`
	Storage      *AddonStorage     `json:"storage,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	SecretKeys   []string          `json:"secretKeys,omitempty"`
	Snapshot     *AddonSnapshot    `json:"snapshot,omitempty"`
	Restore      *AddonRestore     `json:"restore,omitempty"`
}

// KindRuntime returns the entry that runs the Addon's kind; Kind, Kinds and AddonBasePath read through it.
func (s AddonSpec) KindRuntime() (AddonRuntime, bool)
func (s AddonSpec) Kind() string
func (s AddonSpec) Runtime(name RuntimeName) (AddonRuntime, bool)

// RevisionSpec gains the image of an Addon's runtime, recorded when funcd creates the Revision; empty for funcd's own.
type RevisionSpec struct {
	// … the fields of today, unchanged
	RuntimeImage string `json:"runtimeImage,omitempty"`
}
```

```go
// Package addonruntime indexes the runtimes that registered Addons ship, for the function reconciler (ADR-0344).
type Index interface {
	// Lookup returns the runtime named name, compared without case, and the Addon that ships it.
	Lookup(name v1.RuntimeName) (rt v1.AddonRuntime, addon v1.ObjectName, ok bool)
}
```

| Direction | What |
|---|---|
| Consumes | the Addons in the store; `addons.registries` (ADR-0332); the server's Docker credential store for private images |
| Exposes | the `Addon` shape above; `Revision.spec.runtimeImage`; the index to the function reconciler's gate |

## Implementation plan

1. `api/types/v1alpha1/addon.go`: the types and accessors above, `Validate` for Decisions 2 to 4;
   `revision.go`: `runtimeImage`. Every reader of the old fields moves to the accessors:
   `internal/services/addon` (reconcile, settings, settings_kinds, calls, revisions, restore, addon),
   `internal/controlplane` (addon, admission/addon), `internal/backup/workload`, `cmd/funcdctl/app.go`.
2. `internal/controlplane/admission/addon.go`: the shape, names and images of Decisions 2 to 5, under the existing
   lock. A new `internal/services/addonruntime` keeps the index from the watched Addons.
3. `internal/function`: the gate's new causes (Decision 6), and `runtimeImage` recorded at a Revision's creation and
   passed to the driver, whose `Config.Pullable` admits a ref from `addons.registries`.
4. pyvvo/funcd-addons: rauthy's `addon.yaml` in the new shape, released; this PR bumps `github.com/pyvvo/funcd-addons`
   in `go.mod`, as a shim release is bumped (ADR-0141).
5. Docs: the guide's add-on pages, `just generate` for the API reference, the behavior rows above.
6. Tests: one `TestScenario…` per scenario, named after it; admission table tests for every refusal; the existing
   add-on and settings tests on the new shape; a lane case on containerd where a Function runs on an Addon's runtime
   (an Addon that ships a copy of `nodejs22`'s image from the lane's registry); `addon-rauthy` and
   `addon-rauthy-restore` pass.
7. Done when `just ci-full` and the lanes above pass, and CI is green.

## Review checklist

- [ ] `Addon.spec` holds only `runtimes`; no reader uses the old fields.
- [ ] Each refusal of Decisions 2 to 5 has its status code and names the field, with a test.
- [ ] A Function on an Addon's runtime runs its Revision's `runtimeImage`; an Addon update leaves that Revision as is.
- [ ] The gate's messages name the cause: no such runtime, its workloads, or a driver that runs no image.
- [ ] rauthy's manifest is reshaped in a funcd-addons release, the pin is bumped, and both rauthy lanes pass.
- [ ] The guide, the API reference and the behavior rows match.

## Consequences

- **(+)** A tool funcd does not ship arrives as an add-on, on the add-on's cadence, and funcd's binary stays as it is.
- **(+)** One list covers an engine and the runtimes for Functions and Jobs.
- **(−)** The Addon API breaks: rauthy's manifest, the lanes and the guide change, and funcd-addons releases in step.
- **(−)** A Function or Job on an Addon's runtime is `RuntimeUnavailable` under the process driver, `funcdctl dev`
  included, until dev runs such workers.
- **(−)** A runtime's image must stay pullable while a Revision records it.

## Open questions

- Workers of an Addon's runtime under `funcdctl dev`, on its Docker driver as engines run (ADR-0334): a follow-up ADR.
- A protocol version on a `Function` or `Job` entry, for runtimes built outside funcd (FEAT-0012's open question): the
  ADR that documents the shim protocol.
- Whether a Function moves to an Addon's new runtime image only at its next Revision, or on a command too: decided
  with the decider at this ADR's acceptance.
- Image signatures: FEAT-0012's open question on runtime trust.

## References

- FEAT-0012 F139 and its issue [#1017](https://github.com/pyvvo/funcd/issues/1017); the decider's choices of
  2026-11-23 recorded in [#1526](https://github.com/pyvvo/funcd/pull/1526)
- ADR-0054, ADR-0087, ADR-0141, ADR-0149, ADR-0332, ADR-0334, ADR-0339, ADR-0340
- `internal/function/function.go` (`runtimeUnavailable`), `internal/runtime/containerd/containerd_linux.go`
  (`resolveImage`), `internal/runtime/ctrmanager/manager.go` (`ImageFor`)
