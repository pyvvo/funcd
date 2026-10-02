# ADR-0144: Bundling lives in the language toolchains — `@funcd-dev/vite-plugin` and `funcd-bundle`

- **Status**: Implemented (2026-10-02)
- **Date**: 2026-10-02 (revised the same day after the judge: Vite environment names are sanitized; every manifest
  contract is checked against the schema it replaces before that schema goes, and s3-roundtrip's is fixed; the plugin
  never empties a shared output directory per function; the copy rule follows funcdctl's resolver; the runtime-provided
  packages are pruned; the hermetic build uses pip in the container; Python handlers come from the manifest's `main`;
  the release and CI steps are listed. Re-judged: the default handler follows funcdctl's rule; the copied manifest's
  `main` names the bundled handler; the output directory is replaced. Two refinements from the implementation: each
  Vite environment builds into its own staging directory, and the import check covers what the handler imports. **Accepted 2026-10-02** under `adr-batch`, with acceptance
  delegated by the decider: the re-judge found no Blocker, and its Major (the default handler) is folded in. **Reviewing 2026-10-02** — implemented by `adr-impl`:
  `@funcd-dev/vite-plugin` 0.4.0 (pyvvo/funcd-typescript#14) and `funcd-bundle` 0.3.0 (pyvvo/funcd-python#12)
  released and published; funcd pins both tags, its lanes push from the manifests, and all nine Lima lanes pass. **Implemented 2026-10-02** — review gate
  pass, see docs/reviews/adr-0144-implementation-claude-opus-5-5.md)
- **Deciders**: green-0-rabbit
- **Tags**: bundle, artifact, dx, vite, uv, python, typescript, examples
- **Realizes**: [FEAT-0001/F105](../feat/0001-feat-v1.1.md) (a function is bundled by its own language toolchain)
- **Supersedes (in part)**: [ADR-0089](0089-python-function-dependency-bundling.md) Decision 4 and its *Host-pip fast
  path* workaround — the default Python build becomes uv's locked cross-platform install, import-checked in the
  runtime's base image; the in-container build stays as an option. The rest of ADR-0089 (the bundle layer, the env, the
  contract gate) stands.
- **Refines**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (a bundler puts the function's
  `funcdctl.yaml` where `funcdctl push` resolves it, so no push needs `--schema`), [ADR-0124](0124-multi-function-funcdctl-yaml.md)
  (the `<name>.funcdctl.yaml` naming rule is how a bundler finds each function's manifest)
- **Relates to**: [ADR-0123](0123-runtime-compiled-io-validators.md) (no baked validator is needed, so a bundler only
  bundles), [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (the tools ship from the language repos; the
  lane registry), [ADR-0058](0058-contract-codegen-from-code-types.md) / [ADR-0060](0060-contract-validator-generation.md)
  (the build-time schema derivation and bake the example builds still run), [ADR-0071](0071-python-runtime-ships-fastjsonschema.md)
  (the runtime ships `fastjsonschema`), [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md)
  (uses `--platform`)

## Context & Need

Every example carries its own build script: `build.ts` in funcd-typescript (esbuild + `buildContract`), `build.py` in
funcd-python (`funcd_shim.build`, and for `catalog-quack` a `pip install --target` in Docker). The `build.ts` files
share one shape and differ in names and a few options. They also still do work that ADR-0122 and ADR-0123 made
unnecessary — they derive the schema from code types and bake a validator, while `funcdctl push` takes the contract
from `funcdctl.yaml` and the shim compiles the validator from it. A user project (a monorepo of functions) has no
build to copy at all.

**Purpose**: give each language one published, reusable bundler that turns a function's source into the artifact
`funcdctl push` expects, using that language's own toolchain:

- **TypeScript**: `@funcd-dev/vite-plugin`, a Vite plugin. `vite build` writes one self-contained `<name>.mjs` per
  function.
- **Python**: `funcd-bundle`, a command run with `uv run`. It writes a bundle directory: the handler plus the exact
  dependency versions from `uv.lock`, installed for the runtime's Linux platform.

`funcdctl` stays a single Go binary with no language toolchain (ADR-0122), like `wrangler` next to
`@cloudflare/vite-plugin`. Callers: function authors, the language repos' examples, and funcd's lanes and e2e tests,
which push those examples.

## Scenarios

- **scenario: vite-builds-one-file-per-function** — Given a TypeScript project whose Vite config uses
  `funcd({ functions: { 'env-echo': 'src/env-echo.ts', front: 'src/front.ts' } })`, When `vite build`, Then
  `env-echo.mjs` and `front.mjs` are each one self-contained ES module (dependencies inlined, `node:` builtins external,
  no shared chunk) that export the handler, and both survive in the shared output directory.
- **scenario: vite-output-pushes-from-manifest** — Given that output, When `funcdctl push <outDir>/<name>.mjs <ref>`,
  Then the push takes its contract and runtime from the manifest funcdctl resolves beside the file (the copied
  `<name>.funcdctl.yaml`, or the project's own manifest when the output directory is the project root), with no
  `--schema`.
- **scenario: vite-missing-manifest-fails** — Given a function with neither `<name>.funcdctl.yaml` nor `funcdctl.yaml`,
  When `vite build`, Then the build fails naming both paths.
- **scenario: bundle-vendors-locked-deps** — Given a uv project whose handler imports a locked third-party package,
  When `uv run funcd-bundle`, Then `dist/<name>/` holds the handler, `funcdctl.yaml`, and exactly the locked versions of
  the runtime dependencies — no dev dependencies, and nothing the runtime provides (`funcd-shim` and the packages only
  it needs, such as `fastjsonschema`).
- **scenario: bundle-cross-platform** — Given an arm64 host, When `funcd-bundle --platform linux/amd64`, Then every
  vendored native module is an x86-64 ELF from a wheel tagged manylinux no newer than glibc 2.36, and the import check
  passes on `linux/amd64`.
- **scenario: bundle-refuses-source-builds** — Given a dependency with no wheel for the target platform, When
  `funcd-bundle` runs, Then it fails naming the package rather than building it from source on the host.
- **scenario: bundle-includes-workspace-package** — Given a function that depends on a uv workspace member (e.g.
  `py-common`), Then the bundle carries that package as installed files (not an editable link) and the handler imports
  it.
- **scenario: bundle-check-catches-mismatch** — Given a vendored tree that does not import in the runtime's base image,
  Then `funcd-bundle` exits non-zero with the import error.
- **scenario: bundle-hermetic-post-install** — Given `hermetic = true` and a `post-install` command, Then the install and
  the command run inside `python:3.14-slim-bookworm` for the target platform, with `FUNCD_BUNDLE_DIR` naming the bundle
  (catalog-quack's DuckDB extensions land in `duckdb-ext/`).
- **scenario: python-source-push-needs-no-build** — Given a Python handler with no third-party runtime dependency, staged
  with its manifest beside it, When `funcdctl push <handler>.py <ref>`, Then the push takes the manifest's contract and
  the function runs (funcd's `kv` and `funclog` lanes do this).
- **scenario: examples-build-with-the-tools** — Given funcd-typescript and funcd-python at their new release tags, Then
  no example except `releve-lakehouse` carries a `build.ts`/`build.py` or a `*.schema.json`, and funcd's lanes and e2e
  tests push the examples with the contract read from their `funcdctl.yaml`.

## Scope

**In**: the two packages, their configuration and output layout, and their release and CI wiring; migrating every
example that has a build script; the funcd side of that migration (the `go.mod` pins, the lane registry, the e2e
helpers that read example contracts).

**Out**: multi-architecture artifacts and placement ([ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md)
uses `--platform` from here); Node native addons (ADR-0089's open question stays open); bundling inside `funcdctl dev`
(it still runs sources in place); removing `@funcd-dev/shim/build` and `funcd_shim.build` (public API — see Open
questions); `releve-lakehouse` (see Temporary workarounds); workspace members with native code (a member is built as a
pure-Python wheel).

## Constraints & Decision drivers

- `funcdctl` runs no language toolchain (ADR-0122).
- The contract has one source, `funcdctl.yaml` (ADR-0122); a bundler never derives or rewrites it.
- The Python artifact must match the runtime: CPython 3.14 on Debian bookworm, glibc 2.36 (ADR-0071, ADR-0089), and
  must not shadow what the runtime ships (`funcd_shim`, `fastjsonschema`).
- Versions come from the lockfile, so a bundle is reproducible from the commit.
- No new dependency in funcd. The tools' own dependencies are licensed MIT or Apache-2.0: Vite (MIT, peer), PyYAML
  (MIT).

## Alternatives considered

- **Bundle inside `funcdctl`** (embed esbuild's Go API; shell out to pip). *Rejected*: it puts a language toolchain
  back into the CLI that ADR-0122 made toolchain-free, and `funcdctl` would have to learn each ecosystem's lockfile,
  workspace and resolver rules, which the native tools already own.
- **One shared `build.ts`/`build.py` copied into each project** (today's state). *Rejected*: copies drift, and a user's
  project has no copy to start from.
- **A Vite plugin with one build and several inputs.** *Rejected*: Rolldown splits code shared by two inputs into a
  chunk, and the runtime loads one file. The plugin builds each function as its own Vite environment (Vite's
  environment API, the shape `@cloudflare/vite-plugin` uses).
- **Keep esbuild directly, behind an npm CLI.** *Rejected*: Vite is the common TypeScript build tool, with its config
  (aliases, plugins, `tsconfig` paths) in one place, and funcd-frontend already builds with it. esbuild stays usable by
  anyone who prefers it, with no funcd package needed. The cost: Vite is a heavier dev dependency than esbuild.
- **Python: always build inside the container** (ADR-0089 Decision 4). *Kept as an option, not the default*: it needs
  Docker and, for a foreign architecture, emulation for every install.
  - **Why the fast path is safe**: uv selects wheels by the target's exact tags (`--python-platform
    <arch>-manylinux_2_36`, `--only-binary :all:`), which covers the glibc and architecture the ADR-0089 rule guards.
    The import check in the runtime's base image still fails loudly on a bad wheel.
  - **ADR-0089's rejection reason does not hold**: it rejected host `pip --platform` because "pure-Python transitive
    deps aren't platform-tagged", but a `py3-none-any` wheel runs on any platform.
  - **When the container is still required**: when build-time code must run on the target (catalog-quack's
    `INSTALL quack`).
- **Python: a PEP 517 build backend or a uv plugin.** *Rejected*: a backend builds a wheel of the project, not a
  deployment directory of its closure, and uv has no plugin API. A console script run by `uv run` is what uv offers.
- **Python: name the handler in `pyproject.toml`.** *Rejected*: `funcdctl.yaml` already carries the handler path
  (`main`, used by `funcdctl dev`); a second place would drift.

## Decision

1. **`@funcd-dev/vite-plugin`** — a new `vite-plugin/` workspace in funcd-typescript, published to npm by the release
   workflow next to `@funcd-dev/shim`.
   - **Environments**: `funcd(options)` declares one Vite environment per function. Its name is `funcd_` plus the
     function name with every character outside `[A-Za-z0-9_$]` replaced by `_`; two functions that map to the same
     name fail the build. A `builder.buildApp` builds them in order, so `vite build` builds every function.
   - **Build settings**: each environment builds its single input as ESM for `node22` with:
     - dependencies bundled (`resolve.noExternal: true`) and Node builtins external;
     - `codeSplitting: false`, no minification;
     - a `createRequire` banner so bundled CommonJS can load builtins (the fix `s3-roundtrip` carries today);
     - a private staging directory under Vite's cache directory as the environment's `outDir`.
   - **Output**: after all environments are built, the plugin copies each `<name>.mjs` into `<outDir>`. A shared or
     root output directory is never emptied, and Vite's warning for a root `outDir` never fires.
   - **Manifest**: the function's manifest is `<root>/<name>.funcdctl.yaml`, else `<root>/funcdctl.yaml`; neither fails
     the build. Then the plugin copies it to `<outDir>/<name>.funcdctl.yaml`, except when
     `<outDir>` is the directory that holds it (there, funcdctl's resolver already finds it for `<name>.mjs`).
   - **Defaults**: `functions = { <root dir name>: 'src/handler.ts' }`, `outDir = 'dist'`. The plugin derives no schema
     and bakes no validator (ADR-0123).
2. **`funcd-bundle`** — a new package in funcd-python (`bundle/`, beside `shim/`), published to PyPI by the release
   workflow next to `funcd-shim`, run as `uv run funcd-bundle`. Its one dependency is PyYAML.
   - **Functions**: every `<name>.funcdctl.yaml` at the project root is a function `<name>`; with none,
     `funcdctl.yaml` is a function named after the project directory. The handler source is the manifest's `main`,
     relative to the project root, else funcdctl's default beside the manifest (`<name>.py` for a stem manifest,
     `handler.py` for the generic one, `pkg/sdk` `Manifest.Main`); a missing handler fails naming the path.
   - **Steps**: for each function and platform it:
     1. exports the project's locked runtime closure with `uv export --frozen --no-dev --no-emit-project --no-editable
        --prune funcd-shim`, keeping the hashes. `--prune` drops `funcd-shim` and every package only it needs, all of
        which the runtime ships (ADR-0071). Local path entries (workspace members) are split out, so the registry
        requirements are fully hashed.
     2. installs the registry packages with `uv pip install --target <bundle> --no-deps --only-binary :all:
        --python-version 3.14 --python-platform <x86_64|aarch64>-manylinux_2_36`, in hash-checking mode;
     3. builds each workspace member in the closure as a pure-Python wheel (`uv build --wheel`, on the host) and
        installs it with `--no-deps`;
     4. copies the handler: the file alone when it sits at the project root, else its directory tree without
        `__pycache__`, `tests` and dot-entries. Then copies the manifest to `<bundle>/funcdctl.yaml`, with a `main`
        line rewritten to the handler's file name (the path inside the bundle).
     5. **checks** the result: it imports, in `python:3.14-slim-bookworm` (the base the runtime derives its CPython
        and glibc from) for the target platform with `PYTHONPATH` set to the bundle, every vendored top-level module
        the handler's sources import. Vendored top-level modules come from each `*.dist-info/RECORD`, and the
        handler's imports from its sources' absolute `import`/`from` statements. Importing only what the handler uses
        avoids false failures from optional modules a wheel ships (duckdb's `adbc_driver_duckdb` needs a package
        duckdb does not depend on). A foreign platform runs under QEMU when the Docker host has it; `--no-check`
        skips the check, for offline iteration.
   - **Output**: the bundle directory is replaced on every run.
   - **Hermetic** (`hermetic = true` or `--hermetic`): steps 2 and 3 run inside that image with pip (`pip install
     --target /out --no-deps --only-binary :all: --require-hashes -r requirements.txt`, then the members' wheels), and
     then the project's `post-install` argv runs with `FUNCD_BUNDLE_DIR` and `PYTHONPATH` set to the bundle.
     `post-install` without hermetic is a usage error.
   - **Containers**: every container step copies its inputs in and its outputs out with `docker cp` and binds no host
     path. That works from a job container that drives another daemon (ADR-0145), and from a host directory Docker
     cannot mount.
   - **Platforms and output**: the default platform is `linux/<host arch>`; `--platform` repeats. Output goes to
     `<out>/<name>/` for one platform, `<out>/<os>-<arch>/<name>/` for several.
   - **Per-function dependencies**: all functions of one project share its dependencies; functions that need separate
     dependency sets are separate uv projects (workspace members).
3. **A Python function with no third-party runtime dependency needs no build.** Its handler file pushed with its
   manifest beside it is the artifact — funcd's lanes stage `src/counter.py` as `counter.py` next to a copy of the
   manifest named `counter.funcdctl.yaml`. An author runs `funcd-bundle` all the same; the bundle then holds the
   handler and the manifest only.
4. **The examples drop their build scripts and `*.schema.json` files.**
   - **Contracts first**: before a schema file goes, its manifest's contract is checked equal to it (descriptions
     aside), and a stale manifest is fixed. s3-roundtrip's lacks the `granted` output its handler returns and its
     Venom suite asserts.
   - **TypeScript**: each example gets a `vite.config.ts` and builds its committed `.mjs` files with the plugin,
     writing them where they are today (`outDir: '.'`), so funcd's lanes, e2e tests and `funcdctl dev` keep their
     paths. The examples' `build` scripts run after the plugin's (`yarn workspaces foreach --topological-dev`).
   - **Python**: `kv-counter` and `log-burst` drop their built handlers. Their manifests gain `main: src/<file>.py`,
     so `funcdctl dev` keeps running them. `catalog-quack` builds with `funcd-bundle` (hermetic, with a
     `post-install` that installs its DuckDB extensions) and drops its `build` dependency group.
   - **funcd**: the lane registry stages, beside each artifact, the manifest funcdctl resolves for it, and drops
     `schema:`. The e2e helpers read the contract from the manifest. `go.mod` pins the two new tags.
5. **Release order**: each language repo merges and releases first, then funcd runs `go get` on the two tags
   (ADR-0141). The two trusted publishers (npm for `@funcd-dev/vite-plugin`, a pending PyPI publisher for
   `funcd-bundle`, both naming `release-please.yml`) were registered by the decider on 2026-10-01.

## Temporary workarounds

- **`releve-lakehouse` keeps `functions/build.py`.** It has no `uv.lock` (its dependencies are still a draft), and
  `funcd-bundle` installs from the lock. *Exit*: when it gets a `uv.lock`, it moves to `funcd-bundle` and the script
  goes.

## Contracts

### `@funcd-dev/vite-plugin` (TypeScript)

```ts
import type { Plugin } from 'vite';

export interface FuncdPluginOptions {
  /** Function name → handler source, relative to the Vite root. Default: { [basename(root)]: 'src/handler.ts' }. */
  functions?: Record<string, string>;
  /** Directory for `<name>.mjs` (and the copied manifest), relative to the Vite root. Default: 'dist'. */
  outDir?: string;
}

/** One Vite environment per function; `vite build` writes `<outDir>/<name>.mjs` for each. */
export function funcd(options?: FuncdPluginOptions): Plugin[];

/** The Vite environment name for a function name (exported for tests). */
export function environmentName(name: string): string;
```

`package.json`: `name: @funcd-dev/vite-plugin`, `type: module`, `license: Apache-2.0`, `peerDependencies.vite:
^8.0.0`, `exports["."]` → `dist/index.js` + `dist/index.d.ts`, `files: ["dist"]`, `publishConfig.access: public`.

### `funcd-bundle` (Python)

```
uv run funcd-bundle [NAME ...] [--out DIR] [--platform linux/amd64|linux/arm64]... [--hermetic] [--no-check]
```

`pyproject.toml` of the function project (all keys optional):

```toml
[tool.funcd-bundle]
hermetic = false        # true: install inside python:3.14-slim-bookworm with pip
post-install = []       # argv, hermetic only; cwd = project root, FUNCD_BUNDLE_DIR = PYTHONPATH = the bundle
```

| Item | Value |
|---|---|
| Functions | each `<name>.funcdctl.yaml`, else `funcdctl.yaml` as `<project dir name>`; handler = manifest `main`, else `<name>.py` / `handler.py` beside the manifest |
| Output, one platform | `<out>/<name>/` (default `<out>` = `dist`) |
| Output, several platforms | `<out>/<os>-<arch>/<name>/` |
| Bundle contents | the handler file or directory, the vendored packages, `funcdctl.yaml` |
| Last output line per bundle | `funcdctl push <bundle> <ref> --entry <handler file name>` |
| Exit codes | `0` built and checked; `1` a step failed (the message names the package, file or command); `2` usage |
| Needs | `uv` (the one running it); Docker for the check and for `hermetic`; QEMU for a foreign-platform check |

Python API (for tests; the CLI is the public surface):

```python
def bundle(project: Path, name: str, platform: str, out: Path, *, hermetic: bool, check: bool) -> Path: ...
```

### funcd

- `go.mod`: `github.com/pyvvo/funcd-typescript` and `github.com/pyvvo/funcd-python` at their new tags.
- `scripts/lanes.yaml`: keys unchanged; `stage` lists the resolved manifest beside each artifact (the TypeScript
  examples' `funcdctl.yaml` or `<name>.funcdctl.yaml`; `from:`/`to:` for a Python file and its manifest); no
  `push[].schema`; the `duckdb` lane builds with `uv run funcd-bundle` and pushes `dist/catalog-quack` with `entry:
  handler.py`.
- `pkg/funcd` e2e: `exampleContract(t, dir, name) (input, output []byte)` reads `<name>.funcdctl.yaml`, else
  `funcdctl.yaml`, through `sdk.LoadManifest` + `ContractSides`, and gates both sides with `contract.Check`.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| a function's sources, `funcdctl.yaml`, `vite.config.ts` / `pyproject.toml` + `uv.lock` | `<name>.mjs` + its resolvable manifest; or a bundle directory |
| Vite ≥ 8 (peer, MIT); PyYAML (MIT); uv (the runner); Docker (check / hermetic) | npm `@funcd-dev/vite-plugin`, PyPI `funcd-bundle` |
| the PyPI index, for the locked wheels | the `funcdctl push` input of ADR-0089 / ADR-0122, unchanged |

## Implementation plan

**funcd-typescript** (PR → merge queue → release):
- `vite-plugin/` (`package.json`, `tsconfig.json`, `src/index.ts`, `test/plugin.test.ts`, `README.md`); add it to the
  root `workspaces`; build with `tsc`.
- `release-please-config.json`: `vite-plugin/package.json` `$.version` in `extra-files`. `release-please.yml`
  `publish-npm`: build, pack and publish the plugin after the shim.
- `justfile`: `typecheck`, `test` and `build` run `yarn workspaces foreach --all --topological-dev`, and the plugin is
  built before the examples are typechecked.
- Every example with a `build.ts`, and `hello-world`: add `vite.config.ts`, set `build` to `vite build`, drop
  `build.ts`, `*.schema.json` and the esbuild/AJV/ts-json-schema-generator dev dependencies, and rebuild the committed
  `.mjs` files. First compare each manifest contract with its schema file, and fix s3-roundtrip's manifest.
- `CLAUDE.md`: the layout row for `vite-plugin/`; the npm package list.

**funcd-python** (PR → merge queue → release):
- `bundle/` (`pyproject.toml` with `[project.scripts] funcd-bundle`, `src/funcd_bundle/{__init__,cli,bundle}.py`,
  `tests/`, `uv.lock`, `README.md`); add it to the `justfile` project lists.
- `.gitignore`: drop `bundle/` (the old output name, now the package).
- `kv-counter`, `log-burst`: drop `build.py`, the built handler, `*.schema.json` and the `build` dependency group; add
  `main` to their manifests; drop them from `built`.
- `catalog-quack`: drop `build.py` and the `build` group; add `main: src/handler.py` to its manifest,
  `[tool.funcd-bundle]` (hermetic, `post-install`),
  `scripts/install_extensions.py`, and a `funcd-bundle` dev dependency from `../../bundle`.
- `.github/workflows/ci.yml`: QEMU (`docker/setup-qemu-action`) for the foreign-platform check test.
  `release-please.yml` `publish-pypi`: build `bundle/` at the release version too, and publish both.
- `CLAUDE.md`: the layout row, the PyPI package list, and the bundle rule pointing at `funcd-bundle`.

**funcd** (this repo):
- `go get github.com/pyvvo/funcd-typescript@<tag> github.com/pyvvo/funcd-python@<tag>`.
- `scripts/lanes.yaml` as in Contracts; `pkg/funcd/invoke_e2e_test.go` `exampleContract`, used by `pushExampleFn`,
  `kv_e2e_test.go` and `redeploy_e2e_test.go` (which copies the manifest beside its edited handler).
- `blueprint.md`: the build-pipeline note and the companion-repositories table. `docs/adr/0089-*.md`: the
  `Superseded in part by ADR-0144` back-link (at acceptance).

**Test plan** (each scenario a named, passing test):
- funcd-typescript `vite-plugin/test/plugin.test.ts` (`node --test`, building fixture projects with Vite's
  `createBuilder`):
  - `vite-builds-one-file-per-function`: two functions, one hyphenated, sharing a module and one output directory →
    two files, no chunk; each imports and its handler returns.
  - `vite-output-pushes-from-manifest`: the stem and generic cases, an output directory apart from and equal to the
    root.
  - `vite-missing-manifest-fails`.
  - an environment-name collision fails.
- funcd-python `bundle/tests/` (pytest; a fixture workspace with a lock):
  - `bundle-vendors-locked-deps`: a pure-Python wheel at its locked version; `funcd_shim`, `fastjsonschema` and the
    dev dependencies absent.
  - `bundle-cross-platform`: a native wheel for the non-host architecture — the ELF machine of its `.so` and its
    `WHEEL` tag (manylinux ≤ 2.36).
  - `bundle-refuses-source-builds`, `bundle-includes-workspace-package`, `bundle-check-catches-mismatch`,
    `bundle-hermetic-post-install`.
  - Tests that need the network, Docker or QEMU skip with the reason when it is absent; CI has all three.
- funcd `pkg/funcd`: `TestScenarioExamplesBuildWithTheTools` — no `build.ts`/`build.py`/`*.schema.json` in the pinned
  examples (except `releve-lakehouse`), and every lane push resolves a manifest beside its staged artifact. The
  existing invoke, kv, redeploy and funclog e2e tests pass with contracts from the manifests.
- Live: every lane whose push changes — `env-echo`, `fn-to-fn`, `s3`, `kv`, `workflow`, `funclog`, `egress`,
  `duckdb` (`just lima-example-all`); `python-source-push-needs-no-build` is the `kv` and `funclog` Python pushes.

**Definition of done**: both language repos released with the packages published; funcd on the new pins with the four
sub-checks green (`go build ./...` · `go tool golangci-lint run ./...` · `go test ./...` · `go mod verify`); each
scenario a passing test; the lanes above pass (a lane that cannot run is named, with the reason); no identity or path
leak.

## Review checklist

- [ ] `funcd()` builds one file per function into a shared directory with no chunk, sanitizes environment names and
      fails on a collision, never empties the output directory, and copies a manifest only where funcdctl's resolver
      would not already find one.
- [ ] A missing manifest fails the Vite build with both candidate paths in the message.
- [ ] `funcd-bundle` installs only the `uv export --prune funcd-shim` closure, with hashes, `--only-binary :all:`, the
      target's manylinux 2.36 tag and Python 3.14; `funcd_shim`, `fastjsonschema` and dev dependencies are absent.
- [ ] Workspace members are installed as built wheels, not editable links.
- [ ] Functions and handlers come from the manifests (`main`), not from a second config.
- [ ] The import check runs by default in `python:3.14-slim-bookworm` for the target platform over the vendored
      modules the handler imports, and fails the build on an import error.
- [ ] `hermetic` installs with pip in the container and runs `post-install` there with `FUNCD_BUNDLE_DIR` set; no
      container step binds a host path.
- [ ] No example except `releve-lakehouse` has a `build.ts`, `build.py` or `*.schema.json`; every manifest contract
      matched its schema before the schema went.
- [ ] Both packages are versioned by release-please and published by the release workflows.
- [ ] funcd's lanes push no `--schema`; the e2e helpers read the manifest; only `go.mod`/`go.sum` name the new tags.
- [ ] Every scenario has a named, passing test; the live lanes are recorded.

## Consequences

- One bundler per language, maintained once and used by examples and user projects alike.
- A Python build without native build-time code runs on any host in seconds, and a cross-architecture build needs no
  emulation for the install (the import check still runs the base image, under QEMU when foreign).
- `funcdctl.yaml` is the only contract source in every example. The `.mjs` outputs change once (Vite's output, and no
  baked validator); the shim compiles the validator from the delivered contract (ADR-0123).
- Two more published packages: a breaking change to either is a `feat!` in its repo.
- Vite becomes a dev dependency of every TypeScript example, heavier than esbuild alone.
- Risk: uv's `--python-platform` and `--target` flags are a moving surface. The tests pin the behavior, and
  `hermetic` remains as a fallback.

## Open questions

- Remove `@funcd-dev/shim/build` and `funcd_shim.build`, now that no example uses them? A `feat!` in each language
  repo, decided there.
- Node native addons in a bundle directory (ADR-0089's open question) — a follow-up ADR when a case appears.
- Workspace members with native code — built per platform once a function needs one.

## References

- Vite environment API and `builder.buildApp` (Vite 8.3.2, MIT) — checked 2026-10-02 with a two-function prototype
  (one self-contained file each); environment names must match `/^[\w$]+$/` (Vite 8 source, checked by the judge).
- `@cloudflare/vite-plugin` and `wrangler` — the split this mirrors (a bundler plugin beside a toolchain-free CLI).
- uv `export --prune`, `pip install --target --python-platform`, `build`, `workspace dir` (uv 0.11.19) — the prune and
  the hash-checked cross-platform install were run on a scratch workspace on 2026-10-02; both architectures were
  installed and run in Linux containers during the design session.
- PyYAML (MIT).
- [ADR-0089](0089-python-function-dependency-bundling.md), [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md),
  [ADR-0123](0123-runtime-compiled-io-validators.md), [ADR-0124](0124-multi-function-funcdctl-yaml.md),
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md).
