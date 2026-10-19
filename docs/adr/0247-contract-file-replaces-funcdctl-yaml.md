# ADR-0247: A function is its resource file and a contract file; `funcdctl.yaml` goes

- **Status**: Reviewing (2026-10-20), implemented; Accepted (2026-10-19), after two judge rounds, under `adr-batch` with acceptance delegated by the
  decider. The judge added the dev contract (what dev reads, skips, stops on and rewrites; the builds; the Secrets;
  steps through the materializer), the types output rule, the full supersession list and the plan's inventory.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, contract, dev, bundle, refactor
- **Realizes**: [FEAT-0001/F160](../feat/0001-feat-v1.1.md) (#1069, tracker #1072)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0247` back-link:
  - push and types: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) Decisions 1-3,
    [ADR-0124](0124-multi-function-funcdctl-yaml.md), [ADR-0090](0090-mandatory-single-io-schema.md) Decision 3 and
    its source order, [ADR-0089](0089-python-function-dependency-bundling.md) Decision 3 and its `--entry` default,
    [ADR-0243](0243-http-functions.md) Decision 7;
  - bundling: [ADR-0144](0144-bundling-in-the-language-toolchains.md) Decisions 1-4,
    [ADR-0244](0244-vite-plugin-watch-mode.md) Decisions 2-4;
  - dev: [ADR-0125](0125-funcdctl-dev-local-run.md) Decisions 1, 3, 4, 8 and 9,
    [ADR-0126](0126-funcdctl-dev-developer-experience.md) Decision 4,
    [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) Decision 1,
    [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md) Decision 1, [ADR-0237](0237-sql-in-funcdctl-dev.md)
    Decision 1.
- **Relates to**: [ADR-0246](0246-bindings-under-spec.md) and [ADR-0248](0248-stores-at-the-root.md) (the resources
  dev applies; their dev workarounds end here) · [ADR-0123](0123-runtime-compiled-io-validators.md) ·
  [ADR-0094](0094-workflow-engine-core.md) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md)

## Context & Need

`funcdctl.yaml` repeats what a Function resource holds: runtime, handler, protocol and every binding
(`pkg/sdk/manifest.go:28`). Only its contract, `main` and `dev:` block are its own. The copies drift: the todo App's
`intake` binds `store: todo-store` in its manifest, while its App part computes the store from `values.store` and the
App's name (pyvvo/funcd-functions `apps/todo/app/resources/intake.yaml`). After ADR-0246 and ADR-0248 the resource
holds the bindings, stores, ConfigMaps and Secrets, yet dev still synthesizes them from manifests.

**Purpose**: describe a function in two files: its resource file says how it runs, its contract file what its
artifact is. `funcdctl push`, `types` and `dev`, and both bundlers, read those files and nothing else.

## Scenarios

- **scenario: contract-file-push** — *Given* `dist/counter.mjs` and `dist/counter.contract.yaml` (or `.json`), *When*
  pushed, *Then* `funcdctl inspect` prints the contract and `artifact.InspectRuntime` returns its `runtime`; with no
  contract file, or both variants, push refuses, naming the paths.
- **scenario: http-contract** — `protocol: http` with no `input`: push bakes no contract; with `input`, or with a
  python runtime, it refuses.
- **scenario: bundle-push** — `funcdctl push dist/intake <ref>` records the runtime and contract of
  `dist/intake/intake.contract.yaml`, with entry `main.py`.
- **scenario: old-manifest-refused** — with a `funcdctl.yaml` present, push, types, dev, `vite build` and funcd-bundle
  fail, naming the files that replace it.
- **scenario: dev-runs-resources** — *Given* `counter.yaml` (a Function declaring its KV store), `counter.mjs` and
  `counter.contract.yaml`, *When* `funcdctl dev .`, *Then* `counter` serves from the build, its store exists, and a
  rebuild rolls a new revision.
- **scenario: dev-skips-and-refuses** — dev skips a `FuncdConfig` file and stops on an App document (naming #1067) or
  on two Functions named `counter` (naming both files).
- **scenario: dev-build-checks** — a Function with no build, or with `runtime: nodejs22` beside a contract file with
  `python314`: dev stops, naming the paths it looked at or both files; types stops on the mismatch too.
- **scenario: dev-secret-from-env** — a Function declares Secret `api-keys` with key `STRIPE_KEY` and no document
  defines it: `STRIPE_KEY=sk funcdctl dev .` serves `sk`; unset, dev stops, naming `STRIPE_KEY`.
- **scenario: dev-python-in-place** — with `intake = "src/intake/main.py"` in `[tool.funcd-bundle.functions]`,
  `intake` runs from source and a save rolls a new revision; without the table, `src/main.py` runs the Function
  named after the folder.
- **scenario: dev-workflow-steps** — a Workflow with an image step tagged `ingest` and a ref step to a Function of the
  folder: `<workflow>-<step>` runs `ingest.mjs` with the step's bindings, the ref step runs that Function, and a
  rebuild of `ingest.mjs` rolls the step.
- **scenario: dev-reload-resources** — a table added to `counter.yaml` is applied; a removed ConfigMap file prunes it.
- **scenario: dev-writer-grant** — the developer writes an ownerless prefix through the S3 frontend, as today.
- **scenario: types-from-contract** — `funcdctl types -f counter.contract.yaml -o src` writes `src/funcd.d.ts` with
  `FuncInput`, `FuncOutput` and `counter`'s binding types; with no Function document, the I/O types only.
- **scenario: bundlers-copy-the-contract** — `vite build` (and `--watch`) and `uv run funcd-bundle` put each contract
  file beside its build; a function without one fails the build, naming the path.

## Scope

**In**: the contract file; push, types and dev on resource and contract files; both bundlers; the examples' entries;
every `funcdctl.yaml` in the four repos. **Out**: an App in dev (#1067); `vite dev` (#1066); the resource shape
(ADR-0246, ADR-0248); a Site served from a local build.

## Constraints & Decision drivers

- The contract keeps its gate (`contract.Check` at push) and its enforcement (ADR-0123), and the artifact format
  stays (`internal/artifact/artifact.go:58`). Only its file moves.
- A Workflow image step takes its runtime from the artifact (ADR-0094), so push records it.
- No flag repeats what a file says. Dev applies the files production applies and lists what it changes.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A contract file with `runtime` and `protocol`; the resource for the rest** | One file per artifact | Another file per function | **Chosen** (the decider, 2026-10-19) |
| B. The contract inside the Function resource | One file | A second copy of the artifact's contract for apply to compare | Rejected |
| C. Runtime from the build's file type | No key | A rule to remember; wrong for future runtimes | Rejected (the decider) |
| D. Keep `funcdctl.yaml` for the contract only | No new file | A second name for one function's data | Rejected (the decider) |
| E. Dev matches every image by its tag stem (#1069) | One rule | A tag such as `:1.0.0` names no build | Rejected: a Function matches by name |
| F. Dev synthesizes a step's Function behind a ref, as today | No new option | Duplicates the materializer; a ref'd Function cannot own the Workflow's stores (ADR-0248) | Rejected |

## Decision

1. **The contract file** is `<name>.contract.yaml` or `<name>.contract.json` at the project root, beside the resource
   file, and copied beside each build. `runtime` is required. `protocol` is `cloudevent` (the default) or `http`,
   which needs a node runtime. `input` and `output` are required for `cloudevent` (a void side is `{"type":"null"}`)
   and refused for `http`. It decodes strictly, as `funcdctl.yaml` does (`pkg/sdk/manifest.go:142`). Two variants of
   one name are an error.
2. **Push** reads `<dir>/<stem>.contract.*` for a file `<dir>/<stem>.<ext>`, and `<dir>/<base>.contract.*` for a
   bundle directory `<dir>` named `<base>`; a build without one is refused, naming the path. It gates and bakes
   `input` and `output`, or none for `http`, and records `runtime`, as `pushFromManifest` does today; a bundle's
   `__funcd_contract.json` stays funcdctl's own file, written from the contract file. `--schema` and `--runtime` go;
   `--entry` (bundle only) defaults to `main.py` for python, else `main.mjs`.
3. **Types.** `funcdctl types -f <name>.contract.yaml [-o <dir>]` writes `funcd_types.py` or `funcd.d.ts`
   (ADR-0129): `FuncInput` and `FuncOutput` from the contract file, and the binding types of the Function document
   `<name>` in the contract file's folder; with no such document (a step, an App part), the I/O types only. It stops on
   a mismatch as dev does (Decision 6). `-o` defaults to a python function's source entry directory (Decision 6), so
   `funcd_types.py` sits beside the handler, else to `.`.
4. **Dev runs a folder.** `funcdctl dev [dir]` (default `.`) reads the `*.yaml` files at its top level, as an App
   template reads `resources/` (`internal/app/template/load.go:52`). A file or `<stem>` is refused.
   - A document whose `apiVersion` is funcd's is a resource. Contract files, other documents and `FuncdConfig`
     documents (a daemon's config) are skipped.
   - Dev stops on an App (#1067), on two documents of one kind and name (naming both files), on a namespace other
     than `default`, and on a `*.yaml` file that does not parse.
   - It applies the rest in today's order (`applyBoot`, `cmd/funcdctl/dev.go:883`), changed only as Decision 5 says.
     A Site serves from its pushed image.
5. **What dev changes:**

   | Object | Dev writes |
   |---|---|
   | Function `spec.image`, `spec.imageDigest` | `file://` its build (Decision 6); the fingerprint of its files |
   | Function `spec.replicas`, `spec.scaling.minReplicas` | 1 |
   | Workflow image step `function.image` | `file://` a copy of the build its tag stem names (ADR-0125 Decision 8; Decision 7) |
   | Every object | namespace `default`; group `dev` if empty; tag `managed-by: funcdctl-dev`, which `--persist` prunes by |

   It creates each Secret a Function declares (ADR-0248) that no document defines, each key from the environment
   variable of that name (a missing one stops dev, naming it), and, as today, the write-only Role and one
   RolesAssignment per Function it runs, steps included (ADR-0128 Decision 2). It synthesizes nothing else.
6. **Builds.**
   - A node Function runs `<name>.mjs` from the folder or from `dist/` (both is an error), with the contract file
     beside it.
   - A python Function runs in place from its entry in the folder's `pyproject.toml` `[tool.funcd-bundle.functions]`;
     without the table, from `src/main.py` if it is named after the folder; else from `<name>.py`. Its contract file is
     at the folder root. funcdctl reads TOML with `github.com/BurntSushi/toml` (MIT, an indirect dependency today).
   - Dev stops on a Function with no build, naming the paths it looked at, and on a Function whose `runtime` or
     `protocol` differs from its contract file, naming both files.
7. **Workflow steps run as the materializer builds them**: `<workflow>-<step>` with the step's bindings and the
   `handle` handler (`buildFunction`, `internal/workflow/reconcile_workflow.go:51`), solo since dev registers no pool
   shim (`pkg/funcd/options.go:436`). Dev resolves its runtime from the build's contract file through
   `funcd.WithWorkflowRuntimeResolver`, the twin of `WithWorkflowContractResolver` (`pkg/funcd/options.go:490`). A
   rebuild is copied to a new path, since a run compares the image reference (`internal/workflow/reconcile_run.go:333`),
   so the re-applied Workflow rolls the step; old paths go once the new revision serves, but under `--persist` the
   last one stays (#1053). A ref step names a Function of the folder.
8. **Reload.** Dev polls the folder: when a file it read or a build changes, or a file is added or removed, it
   re-reads the documents, re-applies them and prunes what is gone, as a manifest edit does today
   (`cmd/funcdctl/dev_reload.go:184`). A Function or step added after boot is not started: dev warns that it needs a
   restart, as today.
9. **Interpreters** come from `FUNCD_NODE` and `FUNCD_PYTHON`, else `node` and `python3` on `PATH`;
   `uv run funcdctl dev .` puts the project's virtualenv first. `--persist` stays.
10. **Bundlers.**
    - The Vite plugin reads `<root>/<name>.contract.*` where it read the manifest; none or two fail the build. It
      copies the file beside `<name>.mjs` unless `outDir` is the root, and in watch mode watches it and replaces it
      atomically (ADR-0244 Decisions 3 and 5). Its default entry becomes `src/main.ts`.
    - funcd-bundle builds the functions `[tool.funcd-bundle.functions]` lists (name → entry from the project root);
      without the table, one function named after the project directory, from `src/main.py`. It copies each
      contract file to its bundle's root and writes no manifest.
11. **`funcdctl.yaml` is read nowhere.** Push, types, dev and both bundlers fail when they find `funcdctl.yaml` or
    `<name>.funcdctl.yaml` where they look, naming the files that replace it.
12. **Examples**, App parts included, name each entry `main.ts` or `main.py`: `src/main.*` for one function,
    `src/<name>/main.*` for several. A single-file push names its contract after the file, so their Python functions
    are pushed as bundles, or a lane stages a dependency-free `src/main.py` as `<name>.py` beside its contract file.
    Each keeps what dev runs at its root and what only its lane applies in `fixtures/`. Developers name entries freely.

## Temporary workarounds

None.

## Contracts

```yaml
# counter.contract.yaml
runtime: nodejs22
protocol: cloudevent
input:
  type: object
output:
  type: object
```

```go
// pkg/sdk — replaces Manifest, LoadManifest and the manifest-based GenerateTypes.
type ContractFile struct {
	Runtime  v1.RuntimeName      `json:"runtime"`
	Protocol v1.FunctionProtocol `json:"protocol,omitempty"`
	Input    json.RawMessage     `json:"input,omitempty"`
	Output   json.RawMessage     `json:"output,omitempty"`
}

// LoadContract reads <dir>/<name>.contract.yaml or .json (exactly one), decodes it strictly, validates it
// (Decision 1) and returns its path.
func LoadContract(dir, name string) (*ContractFile, string, error)

// GenerateTypes returns funcd_types.py or funcd.d.ts for c's runtime family.
func GenerateTypes(c *ContractFile, b v1.FunctionBindings) (map[string][]byte, error)

// pkg/funcd
func WithWorkflowRuntimeResolver(r workflow.RuntimeResolver) Option
```

| Command | Reads | Removed |
|---|---|---|
| `funcdctl push <build> <ref>` | the contract file | `--schema`, `--runtime` |
| `funcdctl types -f <contract> [-o <dir>]` | the contract file and its Function document | the manifest |
| `funcdctl dev [dir]` | the folder's resources, contract files, builds, `pyproject.toml` | file and stem targets, `--entry`, `--name` |

## Implementation plan

Order: ADR-0246, ADR-0248, then this ADR. On the decider's go, funcd-typescript and funcd-python release first, then
funcd-functions (it pins the released bundlers), then one funcd PR pins all three (ADR-0141). Between those releases
the released bundlers write contract files the released funcdctl does not read: a project upgrades both together.

- **funcd:**
  - `pkg/sdk` as in Contracts; `cmd/funcdctl` push, types and dev (`manifest.go` and `synthesizeResources` go);
    `pkg/funcd` `WithWorkflowRuntimeResolver`.
  - Lanes: `scripts/lane-provision.sh` drops `--schema` and `--runtime` (:59). `scripts/lanes.yaml` stages contract
    files, drops `runtime:` (332-344) and `entry:` (175, 500, 591-603), stages the kv and funclog lanes' Python
    `src/main.py` as `py/<name>.py` (268, 379), builds the todo App's Python parts with funcd-bundle, and moves
    fixtures. `e2e/sql.venom.yml:383`, `e2e/app-todo.venom.yml:81` and `:2152`, and
    `.github/workflows/bundle-multiarch.yml:53` push bundles with no `--entry`. In `justfile`, `dev-example` (:541),
    `seed-releves` (:612) and `run-releve` (:637) pass a folder, and SQL is found in resource files (:570).
  - Tests: one per scenario, named after it (dev ones `-tags dev`, e2e with `t.Parallel()`). Tests on manifest
    fixtures move to resource files and keep covering what stays (`--persist`, the crash reap, the SQL and catalog
    engines): `cmd/funcdctl/dev*_test.go`, `manifest_test.go`, `pkg/sdk/manifest*_test.go`, and `pkg/funcd`'s
    `examples_test.go` (a contract file beside every lane push), `kv_e2e_test.go`, `invoke_e2e_test.go` and
    `redeploy_e2e_test.go`.
- **funcd-typescript:** the plugin and its tests. Examples: `src/main.ts` or `src/<name>/main.ts`, contract files, a
  Function resource for hello-world, Vite keys equal to Function names (blob-object, egress-probe, http-api, log-burst,
  s3-roundtrip), and `fixtures/` for kv-counter's `counter-unbound.yaml`, the workflow example's runs and timer, and
  egress-probe's two policies.
- **funcd-python:** funcd-bundle and its tests. Examples: `src/main.py` or `src/<name>/main.py`, contract files, a
  list entry for each Function not named after its folder (catalog-quack's `catalog-reader`, kv-counter's
  `pycounter`), and releve-lakehouse's dev Functions beside its dev Workflow.
- **funcd-functions:** contract files; the todo App's parts in `src/<name>/main.ts` and `src/<name>/main.py`, the
  Python ones listed in `pyproject.toml`.
- **Docs:** the back-links, F160, `blueprint.md` (:45, :745, :1051), `.github/copilot-instructions.md`,
  `docs/PROJECT-SUMMARY.md`.
- **Done when** `just ci-full` (it lints `-tags dev`), every Lima lane and the three repos' CI pass, and no
  `funcdctl.yaml` remains in the four repos.

## Review checklist

- [ ] The contract file is `.yaml` or `.json`, strict and required, with the `http` rules; push reads it as decided,
      with no `--schema` or `--runtime`; types reads it and the Function document, and stops on a mismatch.
- [ ] Dev reads one level, skips and stops as in Decision 4, changes only what Decision 5 lists, creates only the
      declared Secrets and the writer grant, and finds builds as in Decision 6.
- [ ] Steps go through the materializer with `WithWorkflowRuntimeResolver`; a rebuild or a folder change reloads.
- [ ] Both bundlers read, copy and watch contract files; `funcdctl.yaml` is read nowhere and fails with the hint;
      one test per scenario, named after it; every lane passes.

## Consequences

- **Positive**: one place per binding and store; dev applies production's files and runs steps as production does.
- **Negative**:
  - a breaking change for every project, ours included;
  - `runtime` and `protocol` stay in both files, as they were in the resource and `funcdctl.yaml`; dev and types
    compare them, apply and push cannot;
  - dev needs a resource file for every Function (ADR-0125's "zero CRDs" ends) and runs only a folder;
  - `dev.backends` and the interpreter pins go; a bundle whose entry is not `main.*` needs `--entry`.
- **Risks accepted**: a contract file that says `http` for a `cloudevent` Function pushes no contract, so in
  production the Function runs unvalidated; dev catches the mismatch, and #1084 has the reconciler refuse it.

## Open questions

None.

## References

- #1069, tracker #1072. `cmd/funcdctl/manifest.go`, `cmd/funcdctl/cli.go:230`, `cmd/funcdctl/dev.go`
  (`resolveDevFunctions`, `synthesizeResources`, `tagStem`, `setMeta`), `cmd/funcdctl/dev_reload.go`,
  `pkg/sdk/manifest.go`, `internal/workflow/reconcile_workflow.go:193`; funcd-typescript `vite-plugin/src/index.ts`;
  funcd-python `bundle/src/funcd_bundle/bundle.py:80`.
- ADR-0145 Decision 1, ADR-0217 Decision 2 and ADR-0218 Decision 6 name the removed file or flags; they are otherwise
  unaffected.
