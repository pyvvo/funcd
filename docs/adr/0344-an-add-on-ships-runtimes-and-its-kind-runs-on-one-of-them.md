# ADR-0344: An add-on ships runtimes, and its kind runs on one of them

- **Status**: Accepted (2026-11-23), by the decider ("accept"), after three rounds of an independent judge, every
  Blocker and Major folded
- **Amended in place**: 2026-11-23, by the decider: the Jobs ADRs (F137) are implemented first, so this ADR gates Jobs
  as Functions (Decision 11), and under `funcdctl dev` a worker on an Addon's runtime runs on dev's container engine
  (Decision 13)
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: addons, runtime, api, functions, jobs
- **Realizes**: [FEAT-0012/F139](../feat/0012-feat-infrastack.md) (runtimes from add-ons)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decisions 1 (the spec: Decisions 1 to 3, 10),
  3 (its `RuntimeUnavailable` clause: Decision 9) and 6 (the image rule: Decision 5);
  [ADR-0340](0340-an-addon-declares-settings-kinds.md) Decision 1 (`settings`: Decision 3);
  [ADR-0149](0149-runtime-availability.md) Decisions 2 and 4 (the order and the causes: Decision 6);
  [ADR-0020](0020-function-contract-lifecycle.md) C2 as [ADR-0172](0172-revision-integrity.md) narrowed it (a stamped
  Revision's spec never changes: Decision 8); [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 1
  (`WithAddonRuntime`'s modes: Decision 9); [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 2 (a
  template's Addon adds a kind: Decision 10)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the provider runtime that runs engines) ·
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) · [ADR-0243](0243-http-functions.md) ·
  [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) · the engineless kinds ADR (F270)

## Context & Need

funcd ships its runtimes inside its binary (ADR-0054): `nodejs22` and `python314` run Functions, `duckdb` and `rqlite`
run the Lake's and the SQL database's engines; an operator may add one through `runtime.containerd.imageOverride` or
`imagePrefix` (ADR-0149). OpenTofu is too large to embed, and its licence and its upgrades would ride on funcd's
release (Alternatives), so the decider chose on 2026-11-23 that an add-on ships the runtimes it needs: the InfraStack
add-on (F141) ships OpenTofu's. An Addon is today one engine kind (ADR-0332 Decision 1); this ADR makes it a list of
runtimes, one of which may run its kind. Callers: the admin who writes the Addon, the developers whose Functions name
a runtime, the function reconciler's gate, and the add-on reconciler. ADR-0087's "add-on runtime" and Go names such
as `WithAddonRuntime` stay the provider that runs engines; a runtime an Addon ships is an image, as `nodejs22` is.

## Scenarios

- `scenario: addon-runtime-function` — *Given* an Addon `deno` whose runtime `deno2` lists `Function`, its image in a
  private registry that `addons.registries` lists, *When* a Function sets `runtime: deno2` on containerd, *Then* funcd
  pulls the image by digest with the server's Docker credential store, as an engine's, and the Function turns Ready.
- `scenario: addon-runtime-wrong-workload` — *When* a Function names a runtime whose entry lists only `Job` or
  `Engine`, *Then* it fails with `RuntimeUnavailable` naming the Addon and the workloads, before any worker starts.
- `scenario: addon-runtime-process-driver` (amended 2026-11-23) — *When* a Function names an Addon's runtime on a process
  server, *Then* it fails with `RuntimeUnavailable`: no driver of that server runs an image.
- `scenario: addon-runtime-job` (amended 2026-11-23) — *Given* `deno2` lists `Job`, *When* a Job names it, *Then* its runs run
  `deno2`'s image; *when* a Job names a runtime that lists only `Function`, *Then* `RuntimeUnavailable`.
- `scenario: addon-runtime-dev` (amended 2026-11-23) — *Given* `funcdctl dev` with Docker or nerdctl, *When* a Function or a
  Job names `deno2`, *Then* its workers run `deno2`'s image there; *with* neither, *then* `RuntimeUnavailable` naming
  what dev tried.
- `scenario: addon-runtime-appears` — *Given* a Function on `deno2` applied before `deno`, or while `deno2` listed only
  `Job`, *When* the admin applies `deno` with `Function`, *Then* the Function turns Ready with no change of its own.
- `scenario: addon-runtime-pinned` — *Given* a Function Ready on `deno2`'s image A, *When* the admin updates `deno` to
  image B, *Then* its Revision keeps A; only a change of the Function's spec makes a Revision that runs B.
- `scenario: addon-runtime-gone` — *When* the admin deletes `deno`, *Then* at its next pass a Function on `deno2` fails
  with `RuntimeUnavailable`, and its serving Revision keeps serving, `RevisionReady=False`, until its workers stop.
- `scenario: operator-runtime-unchanged` — *Given* an operator's `runtime.containerd.imageOverride` for `ruby3`, *When*
  a Function names `ruby3`, *Then* it runs as ADR-0149 decides, with no Addon.
- `scenario: addon-runtime-name-refused` — *When* an Addon names a runtime `nodejs22`, `node24`, `python-tofu` or a key
  of `runtime.containerd.imageOverride`, or two entries share a name, *Then* it gets 400; *when* another Addon ships the
  name, *Then* 409; and nothing is stored.
- `scenario: addon-shape-refused` — *When* an Addon has two entries that run a kind, an entry with no image, `Engine`
  beside another workload, or an attribute its workloads do not take (`port` on a Function's runtime, `settings`
  outside an engine's entry), *Then* it gets 400 naming the field.
- `scenario: addon-engine-unchanged` — *Given* rauthy's Addon with its engine entry, *When* an IdentityProvider is
  applied, *Then* its engine runs its pinned AddonRevision's image and attributes (ADR-0339 Decision 2).
- `scenario: addon-engine-process-server` — *Given* a process server, *When* an IdentityProvider is applied, *Then* it
  waits `Pending`, `RuntimeUnavailable`, naming the process driver.
- `scenario: addon-runtimes-only` — *Given* two Addons whose entries list only `Function`, *Then* both are stored,
  neither adds a kind, and one may later gain an engine entry.
- `scenario: addon-template-needs-kind` — *When* an App template's `addon.yaml` adds no kind, *Then* funcdctl refuses
  the template, exit 1 naming the file, and applies nothing.

## Scope

**In** (amended 2026-11-23): Decisions 1 to 13; rauthy's manifest reshaped in pyvvo/funcd-addons and its pin bumped; the
guide, the API reference, the behavior rows and the blueprint. **Out**: the `Engineless` workload (F270's ADR); the
shim protocol for runtimes built outside funcd (a follow-up under F139); image signatures; wasm.

## Constraints & Decision drivers

- No node is deployed, so the Addon API may break with no migration.
- The blueprint promises platform-owned runtime containers and no arbitrary images (`blueprint.md:44`, `:288`): an
  Addon's runtime is an admin's, pinned by digest from a listed registry; the blueprint says so at acceptance.
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
| Drop a Revision created before its runtime served Functions and stamp it afresh, as ADR-0172 does a namesake | no stamped spec ever changes | the create path resolves the artifact's tag again, so a moved tag drifts it, and retires workers by the Revision's name | Rejected |
| OpenTofu as a runtime funcd embeds (F142) | runs under the process driver | 62 to 66 MB more in funcd's binary, and its licence in funcd's release | Dropped (decider) |

## Decision

1. **The shape.** `Addon.spec` holds `runtimes`, entries of `name`, `image`, `workloads` and `attributes`. The kind,
   its `config` and its `settings` sit in the attributes of the entry that runs the kind; ADR-0332's runtime modes go.
2. **Workloads.** `workloads` takes `Function` and `Job`, which Functions and Jobs may name, and `Engine`, which runs
   one engine of the image per object of the entry's kind (ADR-0332 Decision 3). `Engine` stands alone in an entry,
   and at most one entry runs a kind.
3. **Attributes.** An `Engine` entry's are its `kind`, `config` and `settings` (ADR-0340's kinds) and ADR-0332's
   engine fields, `port` and `readiness` required, all unchanged; a `Function` or `Job` entry takes none yet. Any other
   attribute is refused, 400 naming it.
4. **Names.** A runtime's name is a `RuntimeName`, a DNS label, checked when an Addon is written. It is refused, 400,
   when it is one of funcd's (`nodejs22`, `python314`, `duckdb`, `rqlite`), starts with `node` or `python`, whose names
   choose funcd's runtime families (`RuntimeName.IsNode`, the Python path), is a key of
   `runtime.containerd.imageOverride`, or repeats an entry; and 409 when another Addon ships it, under the Addon
   writes' cluster-scoped lock (ADR-0340 Decision 2). A Function on it is `cloudevent`: `protocol: http` is refused.
5. **Images.** An entry with `Function`, `Job` or `Engine` has an image pinned by digest, from a registry that
   `addons.registries` lists, checked when the Addon is written (ADR-0332 Decision 6), and pulled as an engine's is.
6. **Resolution.** A Function's `spec.runtime` resolves to funcd's own runtime, else to an Addon's of that name, else
   as ADR-0149 Decision 2 decides, so an Addon's runtime shadows an operator's of the same name, an override key added
   later included, though running workers keep the operator's image until they stop (Decision 8). The gate reads the
   stored Addons on each pass and fails the Function with `RuntimeUnavailable`, in ADR-0149 Decision 4's form, with
   one of three new causes: `Addon "<addon>" ships it for <workloads>, not for Functions`; `it is an Addon's image,
   which no driver of this server runs` (amended 2026-11-23); `the Addon that shipped it is gone`, for a Revision that
   recorded its image.
7. **Recovery.** The Function controller watches Addons. A watch event carries no prior state (`controller.MapFunc`),
   so each change requeues every Function whose `spec.runtime` is none of funcd's own.
8. **The pin.** A Revision records the image of the Addon runtime that serves it in `spec.runtimeImage`, as it records
   `imageDigest`, and its workers run that image. funcd writes it when it creates the Revision, or, for one created
   before its runtime served Functions, once, at the first pass that resolves it while no worker of the Revision runs;
   it never changes after. Only a change of the Function's spec makes a Revision with a newer image: an Addon update,
   or an App upgrade that moves its add-on parts (ADR-0339 Decision 3), moves none whose spec is unchanged.
9. **Engines.** An engine runs where the add-on provider runtime (ADR-0087) runs images: containerd on a server, Docker
   under `funcdctl dev` (ADR-0334). On a process server, whose provider runtime runs no image, an object of an engine
   kind waits `Pending`, `RuntimeUnavailable`, naming the process driver. `WithAddonRuntime(r)` loses its modes.
10. **Runtime-only Addons.** An Addon whose entries run no kind adds none, and is unique by its runtimes' names alone.
    An Addon may gain a kind once; a kind it has cannot change or go. An App template's `addon.yaml` must add a kind,
    since `app deploy` finds the template's Addon by it (ADR-0337 Decision 2): funcdctl's template loader refuses one
    that adds none, exit 1 naming the file.
11. **Jobs** (amended 2026-11-23). The Jobs ADRs (F137) are implemented before this ADR, so it applies Decisions 6 to 8
    and 13 to a Job as to a Function: a Job names a runtime that lists `Job`, the gate refuses one that does not, the
    Job controller watches Addons, and what F137 stamps for a Job's definition records the image.
12. **Trust.** An Addon's runtime is trusted as its engine is: an admin's, pinned and allow-listed. Its workers' egress
    is governed as funcd's own runtimes' is: by the gateway when `server.network.egress` is on, by nothing when it is
    off (ADR-0117 Decisions 3 and 6).
13. **Workers under `funcdctl dev`** (amended 2026-11-23). Dev runs a worker whose runtime is an Addon's image on the
    container engine it runs add-on engines on (ADR-0334 Decision 1, Docker or nerdctl), and any other worker on the
    process driver: `runtime.Pair` sends a worker whose spec names an image to that engine's worker driver, and any
    other to the process driver. The driver publishes the shim's port on 127.0.0.1 and copies the worker's bundle into
    a volume through a helper, as ADR-0334 copies an engine's files. With no engine, the worker fails
    `RuntimeUnavailable` naming what dev tried, as an engine waits.

## Design model

None. Nothing here adds a lock, a timer or a stop path: the gate reads the stored Addons on each pass, the watch only
requeues, and Decision 8's one write fills an empty field in the Function's own pass before it starts a worker
(workers start only there, in `internal/function`). ADR-0339's AddonRevision pin and `revisions.fizz` are unchanged.

## Behavior rows

- **addons.md**: "An Addon's runtimes" (`:29`) becomes the runtimes an Addon ships, with Decisions 2 to 5's refusals;
  "An Addon's kind", "image" and "config and env" (`:26` to `:28`) read the entry that runs the kind, and an Addon
  gains a kind once, never changed or dropped; "An Addon changed or deleted" (`:30`): an update requeues the Functions
  on its runtimes and moves no image, a delete fails them at their next pass; "An object on a process server" (`:46`)
  names the process driver.
- **workers.md**: "A `WorkerNode` or `RuntimeClass` object" (`:39`): `spec.runtime` may pick an Addon's image; "A
  runtime with no image" (`:40`): an Addon's runtime is known at the gate and recovers when its Addon appears.
- **functions.md**: beside "A tag that moved" (`:66`), a row: an Addon's new image moves no Revision.
- **funcdctl.md** (`:30`) and **funcdctl-dev.md** (`:53`): a template's `addon.yaml` that adds no kind, exit 1.
- **funcdctl-dev.md** (amended 2026-11-23): a new row "A worker on an Addon's runtime" (Decision 13).

## Temporary workarounds

None.

## Contracts

```go
// AddonSpec lists what an Addon ships; at most one entry runs the Addon's kind (ADR-0344).
type AddonSpec struct {
	Runtimes []ShippedRuntime `json:"runtimes" minItems:"1"`
}

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

// AddonAttributes hold an Engine entry's fields, moved from ADR-0332's AddonSpec with their tags unchanged: Kind,
// Config, Settings (ADR-0340), Port, BasePath, PrivatePaths, Readiness, Storage, Env, SecretKeys, Snapshot, Restore.
type AddonAttributes struct {
	Kind     string          `json:"kind,omitempty" pattern:"^[A-Z][A-Za-z0-9]{1,62}$"`
	Config   json.RawMessage `json:"config,omitempty"`
	Settings []AddonSettings `json:"settings,omitempty"`
	// … Port to Restore as listed above, unchanged
}

// KindRuntime returns the entry that runs the kind; Kind, Kinds and AddonBasePath read through it.
func (s AddonSpec) KindRuntime() (ShippedRuntime, bool)
func (s AddonSpec) Kind() string
func (s AddonSpec) Shipped(name RuntimeName) (ShippedRuntime, bool)

// RevisionSpec gains the image of the Addon runtime that serves it (Decision 8); empty for any other runtime.
type RevisionSpec struct {
	// … today's fields, unchanged
	RuntimeImage string `json:"runtimeImage,omitempty"`
}

func WithAddonRuntime(r provider.Runtime) Option // the provider runtime that runs engines (ADR-0087), no modes

// Pair runs a worker whose spec has an Image on images, any other on process (Decision 13) (amended 2026-11-23).
func Pair(process, images Runtime) Runtime // internal/runtime

// NewWorkers is the worker driver over the container engine of dev's add-on engines (Decision 13) (amended 2026-11-23).
func NewWorkers(ctx context.Context, eng Engine, key string, logger *slog.Logger) (runtime.Runtime, error)
```

| Direction | What |
|---|---|
| Consumes | the stored Addons; `addons.registries`; `runtime.containerd.imageOverride`; the server's Docker credential store |
| Exposes | the `Addon` shape above; `Revision.spec.runtimeImage`; the gate's causes |

## Implementation plan

1. `api/types/v1alpha1/addon.go`: the types and accessors, `Validate` for the structural rules of Decisions 2, 3 and
   5; `revision.go`: `runtimeImage`. Every reader of the old fields moves to the accessors: `internal/services/addon`,
   `internal/controlplane/addon.go`, `internal/backup/workload`, `internal/app/revision.go`,
   `pkg/funcd/addon_provider.go`, `cmd/funcdctl` (`app.go`, `dev.go`, `dev_addon.go`).
2. `internal/controlplane/addon.go`, under its lock: Decision 4's names (funcd's own from `internal/runtime/embedimg`,
   the override keys from the config) and Decision 10's kinds; `internal/app/template/load.go` (`readAddon`): the
   template rule.
3. `internal/function`: Decision 6's order and causes, Decision 7's watch, and `runtimeImage` written by
   `ensureRevision` as Decision 8 says and passed to the driver; `pkg/funcd/options.go`: Decision 9. (amended 2026-11-23):
   the Job controller F137 built takes Decisions 6 to 8; `internal/runtime`: `Pair`; `internal/provider/docker`:
   `NewWorkers`, nerdctl's through the same `Engine`; `cmd/funcdctl/dev.go`: `WithRuntime(Pair(process, workers))`.
4. Manifests: `api/types/v1alpha1/examples/addons/addon.yaml` and `settings.yaml`,
   `e2e/fixtures/rauthy-signin-whoami-addon.yaml`; rauthy's `addon.yaml` released in pyvvo/funcd-addons, and its pin
   bumped in `go.mod` as a shim release is (ADR-0141).
5. Docs: the guide's add-on pages, `just generate` for the API reference, the behavior rows above; the F139 row splits
   its status, as ADR-0149 did for F12: `runtimes and dev workers: <status> · shim protocol: idea` (amended 2026-11-23); at acceptance,
   the blueprint (`blueprint.md:44`, `:288`): an admin's Addon may ship a runtime image.
6. Tests: one `TestScenario…` per scenario, named after it; table tests for every refusal of Decisions 3 to 5 and 10;
   the add-on, settings and runtime-availability tests on the new shape, `TestADR0149_MissingImageContainerd` and the
   `ruby3` lane case unchanged; a new containerd lane case, a Function on an Addon's runtime that ships a copy of
   `nodejs22`'s image from the lane's registry; `addon-rauthy`, `addon-rauthy-restore` and `addon-rauthy-signin` pass.
7. Done when `just ci-full` and the lanes above pass and CI is green, after the Jobs ADRs are implemented (amended 2026-11-23).
   The PR references #1017 and does not close it: the last of F139's ADRs does.

## Review checklist

- [ ] `Addon.spec` holds only `runtimes`; no reader uses the old fields.
- [ ] Each refusal of Decisions 3 to 5 and 10 has its code and names the field or the file, with a test.
- [ ] Resolution is funcd's own, then an Addon's, then ADR-0149's; an operator runtime runs as before.
- [ ] A Function applied before its Addon, or before its entry listed `Function`, turns Ready once it does.
- [ ] `runtimeImage` is written at a Revision's creation, or once while no worker of it runs; an Addon update leaves it.
- [ ] The new containerd lane case and the three rauthy lanes pass; rauthy's manifest is released and its pin bumped.
- [ ] The guide, the API reference, the behavior rows, the F139 row and, at acceptance, the blueprint match.
- [ ] A Job takes Decisions 6 to 8 as a Function does; under dev, a worker on an Addon's runtime runs on Docker or
      nerdctl and any other on the process driver (amended 2026-11-23).

## Consequences

- **(+)** A tool funcd does not ship arrives as an add-on, on the add-on's cadence, and funcd's binary stays as it is.
- **(−)** The Addon API breaks: rauthy's manifest, the lanes and the guide change, and funcd-addons releases in step.
- **(−)** Deleting an Addon stops the Functions on its runtimes once their workers stop, and a runtime's image must
  stay pullable while a Revision records it.
- **(−)** A stamped Revision's spec may change once, from no runtime image to one, before any worker of it runs.
- **(−)** A Function on an Addon's runtime has the bindings its image's shim builds in, and no vendored Python
  dependency: funcdctl bundles it as a non-Python Function, and `addBundleEnv` sets `PYTHONPATH` only for the Python
  family. F141's runtime carries its handler in its image; the shim-protocol follow-up decides the rest.
- **(−)** Under `funcdctl dev` a worker on an Addon's runtime needs Docker or nerdctl, as an add-on engine does (amended 2026-11-23).

## Open questions

- The shim protocol for runtimes built outside funcd, its version and its documentation: a follow-up ADR under F139;
  dev workers are Decision 13 (amended 2026-11-23).
- Image signatures: out of scope; a board card when the decider wants one.

## References

- FEAT-0012 F139 and its issue [#1017](https://github.com/pyvvo/funcd/issues/1017), which the last of F139's ADRs
  closes; the decider's shape of 2026-11-23, recorded in [#1526](https://github.com/pyvvo/funcd/pull/1526) (its
  fields; the other choices are in Constraints)
- ADR-0020, ADR-0054, ADR-0087, ADR-0117, ADR-0141, ADR-0149, ADR-0172, ADR-0243, ADR-0332, ADR-0334, ADR-0337,
  ADR-0339, ADR-0340
- `internal/function/function.go` (`runtimeUnavailable`, `ensureRevision`, `addBundleEnv`), `cmd/funcd/main.go` (the
  driver's `Pullable`), `internal/runtime/ctrmanager/manager.go` (`ImageFor`), `internal/controller/controller.go`
  (`MapFunc`), `pkg/funcd/options.go` (`WithAddonRuntime`)
