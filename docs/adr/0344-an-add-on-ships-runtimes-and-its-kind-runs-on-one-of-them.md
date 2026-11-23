# ADR-0344: An add-on ships runtimes, and its kind runs on one of them

- **Status**: Proposed (2026-11-23), judged once: round 1's Blocker, six Majors and nine Minors folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: addons, runtime, api, functions, jobs
- **Realizes**: [FEAT-0012/F139](../feat/0012-feat-infrastack.md) (runtimes from add-ons)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 1 (the Addon's spec: Decisions 1 to 3, 10),
  Decision 3 (its `RuntimeUnavailable` clause: Decision 9) and Decision 6 (the image rule covers every runtime: Decision
  5); [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decision 1 (`settings` sits in the engine's entry: Decision
  3); [ADR-0149](0149-runtime-availability.md) Decision 2 (the resolution order: Decision 6) and Decision 4 (the causes:
  Decision 6); [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 1 (`WithAddonRuntime` loses its modes:
  Decision 9); [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 2 (a template's Addon adds a kind:
  Decision 10)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the add-on provider runtime, which runs engines, is not
  a runtime an Addon ships) · [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) (an AddonRevision
  freezes the whole spec) · [ADR-0243](0243-http-functions.md) (only node-family runtimes serve HTTP) ·
  [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) · the engineless kinds ADR (F270)

## Context & Need

funcd ships its runtimes inside its binary (ADR-0054): `nodejs22` and `python314` run Functions, `duckdb` and `rqlite`
run the Lake's and the SQL database's engines; an operator may add a runtime through `runtime.containerd.imageOverride`
or `imagePrefix` (ADR-0149). A tool such as OpenTofu is too large to embed (32.1 MB on arm64 and 35.8 MB on amd64
compressed, plus about 30 MB for Python), its MPL-2.0 licence would ship in funcd's release, and its upgrades would
wait for funcd's. The decider chose on 2026-11-23 that an add-on ships the runtimes it needs, so the InfraStack add-on
(F141) ships OpenTofu's. An Addon is today one engine kind (ADR-0332 Decision 1); this ADR makes it a list of
runtimes, one of which may run its kind. Callers: the admin who writes the Addon, the developers whose Functions name a
runtime, the function reconciler's gate, and the add-on reconciler. "The add-on runtime" of ADR-0087, and Go names
such as `WithAddonRuntime`, stay the provider that runs engines; a runtime an Addon ships is an image, as `nodejs22` is.

## Scenarios

- `scenario: addon-runtime-function` — *Given* an Addon `deno` whose runtime `deno2` lists `Function`, *When* a
  Function sets `runtime: deno2` on containerd, *Then* its workers run `deno2`'s image by digest and it turns Ready.
- `scenario: addon-runtime-private-registry` — *Given* `deno2`'s image in a private registry that `addons.registries`
  lists, *Then* funcd pulls it with the server's Docker credential store, as an engine's image.
- `scenario: addon-runtime-wrong-workload` — *When* a Function names a runtime whose entry lists only `Job` or
  `Engine`, *Then* it fails with `RuntimeUnavailable` naming the Addon and the workloads, before any worker starts.
- `scenario: addon-runtime-process-driver` — *When* a Function names an Addon's runtime on the process driver,
  `funcdctl dev` included, *Then* it fails with `RuntimeUnavailable`: the process driver runs no image.
- `scenario: addon-runtime-appears` — *Given* a Function on `deno2` applied before `deno`, *When* the admin applies
  `deno`, *Then* the Function turns Ready with no change of its own.
- `scenario: addon-runtime-pinned` — *Given* a Function Ready on `deno2`'s image A, *When* the admin updates `deno` to
  image B, *Then* its Revision keeps A; only a change of the Function's spec makes a Revision that runs B.
- `scenario: addon-runtime-gone` — *When* the admin deletes `deno`, *Then* at its next pass a Function on `deno2` fails
  with `RuntimeUnavailable`, and its serving Revision keeps serving, `RevisionReady=False`, until its workers stop.
- `scenario: operator-runtime-unchanged` — *Given* an operator's `runtime.containerd.imageOverride` for `ruby3`, *When*
  a Function names `ruby3`, *Then* it runs as ADR-0149 decides, with no Addon.
- `scenario: addon-runtime-name-refused` — *When* an Addon names a runtime `nodejs22`, `node24`, `python-tofu` or a key
  of `runtime.containerd.imageOverride`, or two entries share a name, *Then* it gets 400; *when* another Addon ships the
  name, *Then* 409; and nothing is stored.
- `scenario: addon-shape-refused` — *When* an Addon has two entries that run a kind, an entry with a workload and no
  image, `Engine` beside another workload, or an attribute its workloads do not take (`port` on a Function's runtime,
  `settings` outside an engine's entry), *Then* it gets 400 naming the field.
- `scenario: addon-engine-unchanged` — *Given* rauthy's Addon with its engine entry, *When* an IdentityProvider is
  applied, *Then* its engine runs its pinned AddonRevision's image and attributes (ADR-0339 Decision 2).
- `scenario: addon-engine-process-server` — *Given* a process server, *When* an IdentityProvider is applied, *Then* it
  waits `Pending`, `RuntimeUnavailable`, naming the process driver.
- `scenario: addon-runtimes-only` — *Given* two Addons whose entries list only `Function`, *Then* both are stored and
  neither adds a kind; one may later gain an engine entry; a template whose `addon.yaml` adds no kind is refused, 400.

## Scope

**In**: the Addon's shape, workloads, attributes and names; the image rule for every runtime; a Function on an Addon's
runtime: the resolution order, the gate, the pull, the pin and the recovery; engines on a process server; runtime-only
Addons; rauthy's manifest reshaped in pyvvo/funcd-addons and its pin bumped; the guide, the API reference, the
behavior rows and the blueprint. **Out**: the `Engineless` workload (F270's ADR); the gate for Jobs (F137's ADR,
Decision 11); workers of an Addon's runtime under `funcdctl dev` and the shim protocol for runtimes built outside funcd
(follow-ups under F139); image signatures; wasm.

## Constraints & Decision drivers

- No node is deployed, so the Addon API may break with no migration.
- Reuse what decides and pulls today: ADR-0149's gate, which already refuses the `duckdb` engine image as a Function
  runtime, the operator runtimes it keeps, and the driver's pull rule, which already admits an image from
  `addons.registries` (`cmd/funcd/main.go`).
- The blueprint promises platform-owned runtime containers and no arbitrary images (`blueprint.md:44`, `:288`). An
  Addon's runtime is an admin's, pinned by digest from a listed registry, never a developer's image; the blueprint
  says so at acceptance.
- The decider's shape of 2026-11-23: `spec` holds only `runtimes`; the kind, its config and its settings sit with the
  runtime that runs the kind; no `modes`; the long-running workload is `Engine`; an add-on and its runtimes are
  released together, pinned by one AddonRevision.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Runtime plugins an admin registers apart, through `RuntimeClass` (F139 as first written) | one kind for runtimes | an add-on and its runtimes would be two objects to keep in step | Rejected (decider) |
| Kind, config, settings and the engine's fields at the root, beside `runtimes` | fewer levels | an engineless kind and a runtime-only add-on fit it badly | Rejected (decider) |
| An `objects` block naming its runtime | engine and engineless side by side | one more level and a reference to keep in step | Rejected (decider) |
| A `modes` field per runtime | room for another mode | containerd is the only mode an image runs on | Rejected (decider) |
| An Addon's runtime before funcd's own | an add-on could replace a curated runtime | a developer's `nodejs22` would depend on which Addons exist | Rejected |
| OpenTofu as a runtime funcd embeds (F142) | runs under the process driver | 62 to 66 MB more in funcd's binary, and its licence in funcd's release | Dropped (decider) |

## Decision

1. **The shape.** `Addon.spec` holds `runtimes`, a list of entries, each with `name`, `image`, `workloads` and
   `attributes`. The kind, its `config` and its `settings` sit in the attributes of the entry that runs the kind. The
   runtime modes (`spec.runtimes` of ADR-0332) go.
2. **Workloads.** `workloads` takes `Function` and `Job`, which Functions and Jobs may name, and `Engine`, which runs
   one engine of the image for each object of the entry's kind (ADR-0332 Decision 3). `Engine` stands alone in an
   entry; `Function` and `Job` may be listed together. An Addon has at most one entry that runs a kind.
3. **Attributes.** An `Engine` entry's attributes are its `kind`, `config` and `settings` (ADR-0340's kinds,
   unchanged) and ADR-0332's engine fields, unchanged: `port` and `readiness` required, `basePath`, `privatePaths`,
   `storage`, `env`, `secretKeys`, `snapshot`, `restore`. A `Function` or `Job` entry takes none yet. An attribute that
   the entry's workloads do not take is refused, 400 naming it.
4. **Names.** A runtime's name is a `RuntimeName`, a DNS label. It is refused, 400, when it is one of funcd's
   (`nodejs22`, `python314`, `duckdb`, `rqlite`), starts with `node` or `python`, whose names choose funcd's runtime
   families (`RuntimeName.IsNode`, the Python path), is a key of `runtime.containerd.imageOverride`, or repeats an
   entry of the same Addon; and 409 when another Addon ships it, under the Addon writes' cluster-scoped lock (ADR-0340
   Decision 2). A Function on an Addon's runtime is `cloudevent`: `protocol: http` is refused, as on `python314`.
5. **Images.** An entry with `Function`, `Job` or `Engine` has an image pinned by digest, from a registry that
   `addons.registries` lists, checked when the Addon is written (ADR-0332 Decision 6), and pulled as an engine's is.
6. **Resolution.** A Function's `spec.runtime` resolves first to funcd's own runtime, then to an Addon's runtime of
   that name, else as ADR-0149 Decision 2 decides (an operator's override or prefix). The gate reads the stored Addons
   on each pass, and fails the Function with `RuntimeUnavailable`, in ADR-0149 Decision 4's form, with one of three new
   causes: `Addon "<addon>" ships it for <workloads>, not for Functions`; `it is an Addon's image, which the process
   driver does not run`; `the Addon that shipped it is gone`, for a Revision that recorded its image.
7. **Recovery.** The Function controller watches Addons: a create, an update or a delete requeues each Function whose
   `spec.runtime` names a runtime the Addon ships before or after the change.
8. **The pin.** A Revision records an Addon runtime's image in `spec.runtimeImage` when funcd creates it, as it records
   `imageDigest`, and its workers run that image. An Addon update changes no Revision; only a change of the Function's
   spec makes a Revision with the Addon's image of that time. An App upgrade that moves its add-on parts to a new
   AddonRevision (ADR-0339 Decision 3) moves no Function whose spec is unchanged. A Function whose runtime's Addon is
   gone fails at its next pass, and its serving Revision keeps serving, `RevisionReady=False`, until its workers stop.
9. **Engines.** An engine runs wherever the platform has an add-on provider runtime (ADR-0087): containerd on a server,
   Docker under `funcdctl dev` (ADR-0334). A process server has none, so an object of an engine kind waits `Pending`,
   `RuntimeUnavailable`, naming the process driver. `funcd.WithAddonRuntime(r)` loses its modes argument.
10. **Runtime-only Addons.** An Addon whose entries run no kind adds none: its kinds are empty, and it is unique by its
    runtimes' names alone. An Addon may gain a kind once; a kind it has cannot change or go. An App template's
    `addon.yaml` must add a kind, since `app deploy` finds the template's Addon by its kind (ADR-0337 Decision 2).
11. **Jobs.** A Job names a runtime that lists `Job` in the same way; F137's ADR applies Decisions 6 to 8 to Jobs.
12. **Trust.** An Addon's runtime is trusted as its engine is: an admin's, pinned and allow-listed. A Function on it
    gets what that image's shim does: the egress gateway still bounds its connections (ADR-0317), while the shim's own
    capture, on by default for funcd's runtimes (blueprint), and the bindings exist only if the shim builds them in.

## Design model

None. Nothing here adds a lock, a timer or a stop path: the gate reads the stored Addons on each pass, the watch only
requeues, and the pin is written once, when a Revision is created. An object's pin of its AddonRevision stays
ADR-0339's, modeled in `docs/design/addon/revisions.fizz`, unchanged.

## Behavior rows

- **addons.md**: "An Addon's runtimes" (`addons.md:29`) becomes the runtimes an Addon ships, with Decisions 2 to 5's
  refusals; "An Addon's kind", "An Addon's image" and "An Addon's config and env" (`:26` to `:28`) read the entry
  that runs the kind; "An object on a process server" (`:46`) names the process driver, not two modes.
- **workers.md**: "A `WorkerNode` or `RuntimeClass` object" (`workers.md:39`): `spec.runtime` may pick an Addon's
  image; "A runtime with no image" (`:40`): an Addon's runtime is known at the gate and recovers when its Addon
  appears (Decision 7).
- **functions.md**: the hard-gate reasons (`functions.md:38`) keep `RuntimeUnavailable`, with Decision 6's causes.

## Temporary workarounds

None.

## Contracts

```go
// AddonSpec lists what an Addon ships; at most one entry runs the Addon's kind (ADR-0344).
type AddonSpec struct {
	Runtimes []ShippedRuntime `json:"runtimes" minItems:"1"`
}

// ShippedRuntime is one image an Addon ships, the workloads it serves and what they need.
type ShippedRuntime struct {
	Name       RuntimeName      `json:"name"`
	Image      string           `json:"image,omitempty" pattern:"^[^:@\\s]+(:[0-9]+/[^:@\\s]+)?@sha256:[a-f0-9]{64}$"`
	Workloads  []Workload       `json:"workloads" minItems:"1"`
	Attributes *AddonAttributes `json:"attributes,omitempty"`
}

// Workload is a kind of work a shipped runtime serves; F270's ADR adds Engineless.
type Workload string

const (
	WorkloadFunction Workload = "Function"
	WorkloadJob      Workload = "Job"
	WorkloadEngine   Workload = "Engine"
)

func (Workload) Schema(huma.Registry) *huma.Schema // enum Function, Job, Engine

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

// KindRuntime returns the entry that runs the Addon's kind; Kind, Kinds and AddonBasePath read through it, and
// Kinds is empty when no entry runs a kind.
func (s AddonSpec) KindRuntime() (ShippedRuntime, bool)
func (s AddonSpec) Kind() string
func (s AddonSpec) Shipped(name RuntimeName) (ShippedRuntime, bool)

// RevisionSpec gains the image of an Addon's runtime, recorded when funcd creates the Revision; empty otherwise.
type RevisionSpec struct {
	// … today's fields, unchanged
	RuntimeImage string `json:"runtimeImage,omitempty"`
}

// WithAddonRuntime sets the provider runtime that runs engines (ADR-0087, ADR-0334), with no modes.
func WithAddonRuntime(r provider.Runtime) Option
```

| Direction | What |
|---|---|
| Consumes | the stored Addons (the gate reads them each pass; the Function controller watches them); `addons.registries`; `runtime.containerd.imageOverride` (refused names); the server's Docker credential store |
| Exposes | the `Addon` shape above; `Revision.spec.runtimeImage`; the gate's causes |

## Implementation plan

1. `api/types/v1alpha1/addon.go`: the types and accessors, `Validate` for the structural rules of Decisions 2, 3 and
   5; `revision.go`: `runtimeImage`. Every reader of the old fields moves to the accessors: `internal/services/addon`,
   `internal/controlplane/addon.go`, `internal/backup/workload`, `internal/app/revision.go`, `pkg/funcd/addon_provider.go`,
   `cmd/funcdctl` (`app.go`, `dev.go`, `dev_addon.go`).
2. `internal/controlplane/addon.go`, under its lock: Decision 4's names (funcd's own from `internal/runtime/embedimg`,
   the override keys from the config), Decision 10's kinds.
3. `internal/function`: the resolution order and causes (Decision 6), the Addon watch (Decision 7), `runtimeImage`
   recorded on a Revision's creation only and passed to the driver (Decision 8). `pkg/funcd/options.go`: Decision 9.
4. Manifests: `api/types/v1alpha1/examples/addons/addon.yaml` and `settings.yaml`,
   `e2e/fixtures/rauthy-signin-whoami-addon.yaml`; pyvvo/funcd-addons: rauthy's `addon.yaml`, released; this PR bumps
   `github.com/pyvvo/funcd-addons` in `go.mod`, as a shim release is bumped (ADR-0141).
5. Docs: the guide's add-on pages, `just generate` for the API reference, the behavior rows above; at acceptance, the
   blueprint (`blueprint.md:44`, `:288`): an admin's Addon may ship a runtime image.
6. Tests: one `TestScenario…` per scenario, named after it; table tests for every refusal of Decisions 3 to 5 and 10;
   the add-on, settings and runtime-availability tests on the new shape, `TestADR0149_MissingImageContainerd` and the
   `ruby3` lane case unchanged; a new containerd lane case where a Function runs on an Addon's runtime (an Addon that
   ships a copy of `nodejs22`'s image from the lane's registry); `addon-rauthy`, `addon-rauthy-restore` and
   `addon-rauthy-signin` pass.
7. Done when `just ci-full` and the lanes above pass, and CI is green.

## Review checklist

- [ ] `Addon.spec` holds only `runtimes`; no reader uses the old fields.
- [ ] Each refusal of Decisions 3 to 5 and 10 has its code and names the field, with a test.
- [ ] Resolution is funcd's own, then an Addon's, then ADR-0149's; an operator runtime runs as before.
- [ ] A Function applied before its Addon turns Ready when the Addon arrives.
- [ ] `runtimeImage` is written only when a Revision is created; an Addon update leaves it.
- [ ] The new containerd lane case and the three rauthy lanes pass; rauthy's manifest is released and its pin bumped.
- [ ] The guide, the API reference, the behavior rows and, at acceptance, the blueprint match.

## Consequences

- **(+)** A tool funcd does not ship arrives as an add-on, on the add-on's cadence, and funcd's binary stays as it is.
- **(+)** One list covers an engine and the runtimes for Functions and Jobs; operator runtimes keep working.
- **(−)** The Addon API breaks: rauthy's manifest, the lanes and the guide change, and funcd-addons releases in step.
- **(−)** A Function or Job on an Addon's runtime is `RuntimeUnavailable` under the process driver, `funcdctl dev`
  included, until the follow-up under F139.
- **(−)** Deleting an Addon stops the Functions on its runtimes once their workers stop, and a runtime's image must
  stay pullable while a Revision records it.
- **(−)** A Function on an Addon's runtime has the egress capture and bindings only if that runtime's shim builds them.

## Open questions

- Workers of an Addon's runtime under `funcdctl dev`, on ADR-0334's Docker driver: a follow-up ADR under F139.
- The shim protocol for runtimes built outside funcd, its version and its documentation: a follow-up ADR under F139.
- Image signatures: out of scope; a board card when the decider wants one.

## References

- FEAT-0012 F139 and its issue [#1017](https://github.com/pyvvo/funcd/issues/1017); the decider's choices of
  2026-11-23 recorded in [#1526](https://github.com/pyvvo/funcd/pull/1526)
- ADR-0054, ADR-0087, ADR-0141, ADR-0149, ADR-0243, ADR-0317, ADR-0332, ADR-0334, ADR-0337, ADR-0339, ADR-0340
- `internal/function/function.go` (`runtimeUnavailable`), `cmd/funcd/main.go` (the driver's `Pullable`),
  `internal/runtime/ctrmanager/manager.go` (`ImageFor`), `pkg/funcd/options.go` (`WithAddonRuntime`)
