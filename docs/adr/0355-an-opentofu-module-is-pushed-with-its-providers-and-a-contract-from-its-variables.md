# ADR-0355: An OpenTofu module is pushed with its providers for each architecture and a contract from its variables

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: infrastack, artifacts, contracts, funcdctl, addons
- **Realizes**: [FEAT-0012/F143](../feat/0012-feat-infrastack.md) (infrastructure modules as artifacts)
- **Relates to**: [ADR-0059](0059-contract-as-oci-metadata.md) · ADR-0089 (bundles) · ADR-0144 (docker cp) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) ·
  [ADR-0247](0247-contract-file-replaces-funcdctl-yaml.md) · [ADR-0319](0319-funcdctl-runs-kubectl-style-plugins.md) ·
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) ·
  [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) ·
  [ADR-0348](0348-a-run-waits-for-a-persons-approval-of-its-preview-unless-its-job-says-bypass.md) · ADR-0356

## Context & Need

A stack's Job runs an OpenTofu module on the InfraStack add-on's runtime `opentofu1` (ADR-0344). Today the module is
pushed as a function bundle beside a contract written by hand, and a run takes its providers from the runtime image,
which holds three, each at one version: a module that needs another fails at `tofu init` (pyvvo/funcd-addons
`infrastack/README.md`). F143 decided that the artifact carries the module's providers, pinned by its dependency lock
file, for each architecture; that funcdctl builds it through the add-on's plugin, since OpenTofu ships with the
add-on; and that its contract comes from its variables, so a wrong config fails before any run.

**Purpose.** One command pushes a module, with no OpenTofu on the developer's machine; a run on either architecture
installs only the module's pinned providers and downloads nothing; a run's creation refuses wrong variables, naming
the path; and a run that only plans answers what would change, for F141's drift schedule.

## Scenarios

- `scenario: infra-push-builds-index`: Given a module folder whose `main.tf` calls the local child module `./sub`;
  when `funcdctl infra push <module> <ref>` runs; then it prints `<ref>@sha256:…`, an image index with a `linux/amd64`
  and a `linux/arm64` manifest, and `funcdctl inspect <ref>` prints the contract made from its variables and outputs.
- `scenario: infra-remote-module-refused`: Given a module that calls `hashicorp/consul/aws`, or `../shared`; when
  `funcdctl infra push` runs; then it pushes nothing and names the call.
- `scenario: infra-local-files-refused`: Given a module folder that holds `terraform.tfvars` or `terraform.tfstate`;
  when `funcdctl infra push` runs; then it pushes nothing and names the file; and a module pushed by hand with
  `terraform.tfvars` runs as if the file were not there.
- `scenario: infra-run-downloads-nothing`: Given a bundle that the plugin built; when the OpenTofu runtime plans and
  applies it on an internal Docker network (`docker network create --internal`) that reaches only the state's S3
  engine; then both succeed, with the providers the bundle carries.
- `scenario: infra-provider-mismatch`: Given a bundle whose provider package matches no hash of its lock file; when
  the runtime previews it; then the preview fails, naming the provider, and nothing is applied.
- `scenario: infra-variables-refused`: Given a module with the string variable `prefix`; when a stack's Function starts
  a run with `prefx: a`, or with `prefix: 3`; then the run's creation answers 422 naming the path, no run exists, and
  the stack shows `InvalidVariables`.
- `scenario: infra-sensitive-variable-refused`: Given a module whose variable `token` is sensitive; when a run's
  variables set `token`; then its creation answers 422; with `TF_VAR_token` in a Secret the Job binds, the run plans.
- `scenario: infra-plan-run`: Given a module whose one resource is `random_pet.name`, applied with `prefix: a`; when a
  run with action `plan` and `prefix: b` runs; then it applies nothing and answers `{"plan": {"add": 0, "change": 0,
  "destroy": 0, "replace": 1, "resources": ["random_pet.name"]}}`.
- `scenario: infra-module-pushed-by-hand`: Given a module pushed by `funcdctl push` with a contract of Decision 3's
  shape and no provider folder; when its run applies; then it installs the runtime image's providers and answers
  `{"outputs": …}`.

## Scope

**In**: `funcdctl infra push`; the artifact's layout; the contract made from the module; the `plan` action's answer;
where the runtime installs providers from.

**Out**:
- `funcdctl apply` checking a stack's variables: funcdctl and the API check an add-on object's config only against its
  Addon's schema (ADR-0332 Decision 2), which names no module; a run's creation refuses them before anything runs, the
  row's why (decided on the decider's delegation of 2026-11-24).
- F141's drift schedule: when the add-on's Function starts a `plan` run and how it reports drift. A `plan` run of a Job
  without `approval.bypass` previews and waits for an approval, as every run does (ADR-0348 Decision 2).
- A child module from a registry, git or a URL, which a run's `init` would fetch, or a local one outside the module's
  folder, which the bundle would not hold: the push refuses both, and vendoring one into the bundle is not built
  (decided on the delegation of 2026-11-24).
- Installing the plugin: ADR-0319 has no install command, so each add-on release ships the file.
- Signatures of providers and modules: a digest pins them, as ADR-0344 pins a runtime.
- What the outputs become: ADR-0356.

## Constraints & Decision drivers

- A run downloads nothing, and every input is pinned (F143).
- The developer needs no OpenTofu: the build runs the add-on's, unmodified in its image as MPL-2.0's approval says
  (2026-11-23), and packs the providers the developer's lock file names, unmodified, into the developer's artifact.
- Reuse: bundles (ADR-0089), per-platform push and `funcdctl index` (ADR-0145), the contract in the manifest
  (ADR-0059), the run's admission (ADR-0347 Decision 3), plugins (ADR-0319), docker cp (ADR-0144).
- funcd knows no add-on kind (ADR-0332, ADR-0345 Decision 3).

## Alternatives considered

- **Providers in the runtime image only** (today): a module gets the image's three, at their versions, and a new one
  waits for an add-on release. Rejected by F143.
- **One bundle with both platforms' providers**: every node would pull the other platform's packages too, though
  `funcdctl dev` on a Mac could pull it (Consequences). Rejected: ADR-0145's index gives each node its own.
- **`tofu init` against a mirror that funcd serves**: a run would depend on a new service. Rejected: a run downloads
  nothing.
- **An HCL parser in the plugin**: a new dependency for what `tofu show -json -module=<dir>` prints (checked with the
  runtime's OpenTofu 1.13.9 on 2026-11-24). Rejected.
- **A contract written by hand** (today): a gap between the module and its contract goes unseen. Rejected by F143.
- **The drift finding in the preview** (`status.preview`): `ctx.job.get` reads no preview (ADR-0349 Decision 3).
  Rejected: the `plan` run's result carries it.

## Decision

1. **The plugin.** The InfraStack add-on ships `funcdctl-infra`, a script for Python 3.9 or later that uses only its
   standard library, in each release beside its Addon; on PATH, funcdctl runs it as `funcdctl infra` (ADR-0319). It
   runs OpenTofu in the add-on's runtime image, by the digest that each release pins in the script as in its Addon's
   `opentofu1` runtime, or in the image that `INFRASTACK_OPENTOFU_IMAGE` names, as the add-on's CI and lanes do for one
   not yet released. It uses the first of `docker` and `nerdctl` on PATH, and copies files in and out with
   `<engine> cp`, with no bind mount, as funcd-bundle's container steps do (ADR-0144). It calls funcd only through
   `$FUNCDCTL` (ADR-0319 Decision 4). Decided on the decider's delegation of 2026-11-24: a script needs no build of its
   own, and the image gives it the add-on's OpenTofu.
2. **`funcdctl infra push <module> <ref>`.** `<ref>` names a tag, `<registry>/<repo>:<tag>` or
   `oci-layout://<dir>:<tag>`. In order, stopping at the first failure with OpenTofu's or funcdctl's message:
   1. `<module>` must be a folder that holds a `.tf` file and, outside `.terraform/`, no `terraform.tfstate`,
      `terraform.tfstate.backup`, `terraform.tfvars`, `terraform.tfvars.json`, `*.auto.tfvars` or `*.auto.tfvars.json`,
      which OpenTofu would load over a run's variables and which carry values in plain text, else the plugin names the
      path or the file; `tofu show -json -module=<module>` reads its variables, outputs and module calls, and refuses,
      naming it, a call whose `source` is not a relative path that stays inside the folder; each local child is read
      and checked the same way;
   2. `tofu get` installs the local child modules, and `tofu providers lock -platform=linux_amd64
      -platform=linux_arm64` records both platforms' hashes in the module's `.terraform.lock.hcl`, created when absent
      and copied back into the module's folder, to commit;
   3. for each of `linux/amd64` and `linux/arm64`, `tofu providers mirror -platform=linux_<arch> .funcd-providers`
      fills a fresh `.funcd-providers/` in the engine's copy of the module, and a build folder named as the module's
      folder, holding its files without `.terraform/` and without the contract file the module holds
      (`<folder>.contract.yaml` or `.json`, ADR-0247 Decision 1), takes it with Decision 3's contract as
      `<folder>.contract.yaml`. Then `$FUNCDCTL push <build> <ref>-linux-<arch> --entry <first> --platform
      linux/<arch>` pushes it, `<first>` being its first `.tf` file by name, since the runtime reads the folder, not the
      entry;
   4. `$FUNCDCTL index <ref> <ref>-linux-amd64 <ref>-linux-arm64` writes the index, and the plugin prints its
      `<ref>@<digest>`.
   The build reaches the providers' registries; a run reaches none.
3. **The contract.** Its runtime is `opentofu1`. Its input is the run's `{action, variables}`: `action` one of `apply`,
   `destroy` and `plan`; `variables` a closed record of the module's variables, typed by Contracts' table, the required
   ones those with no default. A sensitive variable is left out, so no run's input holds it: its value comes from a
   bound Secret's `TF_VAR_<name>` key, which the runtime gives OpenTofu already. The runtime's work copy of a module
   leaves out the files that step 1 of Decision 2 refuses, so a run's variables come only from its input, the
   `TF_VAR_<name>` keys of its bound ConfigMaps and Secrets, and the module's defaults, a module pushed by hand
   included. Its output is a closed record of two
   optional members: `outputs`, a closed record of the module's outputs that are not sensitive, each any value, none
   required; and `plan`, Decision 5's finding. A record that would have no member is the empty schema `{}`. The
   runtime answers in this shape: an apply `{"outputs": {…}}`, a destroy `{}`, a `plan` run `{"plan": {…}}`; so a
   contract written by hand for this runtime takes it too.
4. **The run's providers.** The runtime runs `tofu init -plugin-dir=<module>/.funcd-providers` when the module holds
   that folder, else with its image's folder, as today. OpenTofu then installs each provider from there only, and only
   when it matches a hash of the lock file: with no network, a packed mirror installs and a package off the lock fails
   (checked with OpenTofu 1.13.9 on 2026-11-24). A failed `init` fails the call it runs in: the preview, with
   `PreviewFailed`, or a `bypass` run's one call, with `HandlerFailed` (ADR-0347 Decision 5, ADR-0348 Decision 2).
5. **The `plan` action.** A run whose action is `plan` keeps no saved plan, applies nothing and writes no output. Its
   preview answers a preview's summary, as for any action; its run plans the variables it gets against the stack's
   state and answers `{"plan": {"add", "change", "destroy", "replace", "resources"}}`: the counts of the resources the
   plan would add, change, destroy and replace, counted as a preview's summary counts them, and the addresses of the
   first 100 of them at most. F141's drift schedule starts such runs and reads the finding through `ctx.job.get`
   (ADR-0349 Decision 3).
6. **The check before a run.** A run's creation checks its input against the contract of the Job's current revision
   and refuses an undeclared variable, a value of another type and a missing required one with 422 naming the path
   (ADR-0347 Decision 3, ADR-0349 Decision 4), which the add-on's Function shows as `InvalidVariables`. So a stack's
   variables are checked at each run's creation, which answers FEAT-0012's open question on when.

## Design model

None: an artifact layout, a build command, a type mapping and one more action of the runtime's call, with no
lifecycle, racing actors, timer or retry (ADR-0272 Decision 1). A `plan` run takes the run model of ADR-0347 and
ADR-0348 unchanged (`docs/design/job/run.fizz`, `docs/design/job/approval.fizz`).

## Behavior rows

None: funcd's behavior does not change. The plugin, the artifact, the contract and the runtime's actions are the
InfraStack add-on's, and its README records them.

## Temporary workarounds

None.

## Contracts

Each platform's bundle under the index (new) holds the module's files with its local child modules, its
`.terraform.lock.hcl`, the contract files (`<folder>.contract.yaml`, and the `__funcd_contract.json` that push writes)
and `.funcd-providers/`: that platform's packages in OpenTofu's packed mirror layout,
`<host>/<namespace>/<type>/terraform-provider-<type>_<version>_linux_<arch>.zip`, beside the JSON index files that
`providers mirror` writes and `init` ignores.

The contract of a module with the string variable `prefix` and the plain output `name` (new):

```yaml
runtime: opentofu1
input:
  type: object
  additionalProperties: false
  properties:
    action:
      type: string
      enum:
        - apply
        - destroy
        - plan
    variables:
      type: object
      additionalProperties: false
      properties:
        prefix:
          type: string
      required:
        - prefix
  required:
    - action
    - variables
output:
  type: object
  additionalProperties: false
  properties:
    outputs:
      type: object
      additionalProperties: false
      properties:
        name: {}
    plan:
      type: object
      additionalProperties: false
      properties:
        add:
          type: integer
        change:
          type: integer
        destroy:
          type: integer
        replace:
          type: integer
        resources:
          type: array
          items:
            type: string
      required:
        - add
        - change
        - destroy
        - replace
        - resources
```

| A variable's type in `tofu show -json -module` | Its schema (new) |
|---|---|
| `"string"`, `"number"`, `"bool"` | `type` `string`, `number` or `boolean`; no null |
| `["list", T]`, `["set", T]` | `type: array`, `items` T's schema |
| `["tuple", [T, …]]` | `type: array`, `items: {}` |
| `["map", T]` | `type: object`, `additionalProperties` T's schema |
| `["object", {a: T, …}, [optional, …]]` | a closed record of each attribute, required unless listed optional |
| `"dynamic"`, or no `type` | `{}` |

No default and no description: OpenTofu applies its own defaults.

```python
# infrastack/plugin/funcdctl-infra (new)
IMAGE = "ghcr.io/pyvvo/funcd-opentofu@sha256:<the release's>"  # the Addon's opentofu1 image (Decision 1)
IMAGE_ENV = "INFRASTACK_OPENTOFU_IMAGE"       # names another image (Decision 1)
def module_calls(shown: dict) -> list[str]: ...  # Decision 2's step 1: the calls it refuses, by name
def contract(shown: dict) -> dict: ...        # Decision 3, from `tofu show -json -module`
def schema(tofu_type: object) -> dict: ...    # the table above
def push(module: Path, ref: str) -> str: ...  # Decision 2; returns "<ref>@<digest>"

# infrastack/runtime/handler.py (delta)
ACTIONS = ("apply", "destroy", "plan")        # plan is new (Decision 5)
PROVIDERS = ".funcd-providers"                # new: the module's own provider folder (Decision 4)
LOCAL_FILES = ("terraform.tfstate", "terraform.tfstate.backup", "terraform.tfvars", "terraform.tfvars.json")
# new: with *.auto.tfvars and *.auto.tfvars.json, left out of the work copy (Decision 3)
def plan_finding(shown: dict) -> dict: ...    # new: Decision 5's record, from `tofu show -json` of a plan
```

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `tofu show -json -module`, `providers lock`, `providers mirror`, `init -plugin-dir` | consumes | the runtime image's OpenTofu 1.13.9 |
| `$FUNCDCTL push --platform`, `$FUNCDCTL index` | consumes | ADR-0145 Decisions 1 and 2 |
| `docker` or `nerdctl` | consumes | `create`, `cp`, `start`, `rm` |
| `INFRASTACK_OPENTOFU_IMAGE` | consumes | an image reference in place of the pinned one (new) |
| the input's `action: plan`; the result's `outputs` and `plan` | exposes | Decision 3's contract |

## Implementation plan

In pyvvo/funcd-addons, on the InfraStack add-on; funcd's code does not change:
1. `infrastack/plugin/funcdctl-infra` and `plugin_test.py`, with a fake engine and a fake funcdctl that record their
   argv; the release workflow attaches the script to each release and pins its `IMAGE` with the Addon's digest.
2. `infrastack/runtime/handler.py`: Decision 3's answers, so an apply answers `{"outputs": …}`, and its work copy
   without the local files; Decisions 4 and 5; with `handler_test.py`. `image_check.py` gains a module that carries
   `.funcd-providers`, made from the image's own providers, run on an internal Docker network that reaches only
   versitygw, and a package off its lock file.
3. Decision 3's shape in the README's contract and run sections and in
   `infrastack/examples/random-stack/random-stack.contract.yaml`; in pyvvo/funcd-functions, in
   `apps/frp-bunny/infra/bunny/bunny.contract.yaml` and `apps/frp-bunny/infra/fake/fake.contract.yaml`.
4. A test that the plugin's `IMAGE` is its Addon's `opentofu1` image.

Tests, each written to pass: the type table, row by row, `infra-remote-module-refused` and the push's half of
`infra-local-files-refused` in `plugin_test.py`, its run's half in `handler_test.py`;
`infra-push-builds-index` with the fakes, and in the add-on's CI against an OCI layout with real OpenTofu;
`infra-run-downloads-nothing`, `infra-provider-mismatch`, `infra-plan-run` and `infra-module-pushed-by-hand` in
`image_check.py`; `infra-variables-refused` and `infra-sensitive-variable-refused` as cases of the InfraStack lane that
F141's work adds to funcd's `e2e/` (`addon-infrastack.venom.yml`), on a module the plugin pushed. Done when
`just test-infrastack`, `just opentofu-image-check` and that lane pass.

## Review checklist

- [ ] Every scenario has its named, passing test.
- [ ] The plugin calls funcd only through `$FUNCDCTL`, and runs OpenTofu only in its Addon's image, by digest, or in
  the image `INFRASTACK_OPENTOFU_IMAGE` names.
- [ ] The push refuses a child module outside the folder, and each platform's bundle holds only its own packages.
- [ ] Each row of the type table has a test, and no generated input schema holds a sensitive variable.
- [ ] A `plan` run writes no output and keeps no saved plan.

## Consequences

- A module may use any provider its lock file pins, with no add-on release; the image's providers serve only a module
  pushed without the plugin.
- A bundle grows by its platform's provider packages; a node pulls only its own.
- A provider with no build for one of the two platforms fails the push at its lock step.
- Under `funcdctl dev` on a Mac, which pulls for its own platform (ADR-0145 Decision 6), the index has no darwin entry,
  so a pushed module does not materialize there.
- A typo in a stack's variables fails at its run's creation, naming the path, and nothing reaches the cloud.
- A module's own `terraform.tfvars`, `*.auto.tfvars` and local state never reach a run: a stack sets each variable
  through its config, or through a `TF_VAR_<name>` key of a ConfigMap or Secret that its Job binds.
- A `plan` run of a Job without `bypass` asks a person to approve a run that changes nothing.

## Open questions

None.

## References

- Feature issue #1021 (FEAT-0012 F143), under tracker #1028.
- pyvvo/funcd-addons `infrastack/README.md` and `infrastack/runtime/handler.py`.
- OpenTofu's `providers lock`, `providers mirror` and `show` commands (opentofu.org/docs/cli/commands), read
  2026-11-24.
- ADR-0059, ADR-0089, ADR-0144, ADR-0145, ADR-0247, ADR-0319, ADR-0332, ADR-0344, ADR-0345, ADR-0347, ADR-0348,
  ADR-0349, ADR-0356.
