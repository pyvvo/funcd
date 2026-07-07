# ADR-0089: Function dependency bundling — the deployment-package model

- **Status**: Implemented
- **Date**: 2026-07-01 (accepted 2026-07-01 — judge: sound, well-scoped, 0 Blockers; folded 2 Majors [pinned the
  real `--contract-*`/`ContractBlob` push surface; specified `__funcd_contract.json` + `VerifyBundleContract` as
  explicitly new build+push behavior promoting into the existing contract path] + 3 Minors; then re-pointed to
  ADR-0090 for the single mandatory schema surface)
- **Implemented**: 2026-07-01 — review **pass** (0 Blockers/0 Majors), see
  [scorecard](../reviews/adr-0089-implementation-claude-opus-4-8.md). `internal/artifact/bundle.go` (deterministic
  `PackBundle`, fail-closed traversal-safe untar, `VerifyBundleContract` [both-keys per ADR-0090 + baked-validator
  presence], `PushBundle`); `Pull`+materializer bundle-entry resolution; `funcdctl push` dir-detection + `--entry`;
  `workerSpec` `PYTHONPATH`/`FUNCD_BUNDLE_DIR` (python family). Four sub-checks green; the live containerd/venom lane
  + the F48 consumer round-trip deferred (inherently e2e).
- **Amended in place**: 2026-07-01 — **exceptionally edited after `Implemented`** (immutability waived by the
  decider for this ADR only, to fix defects the live lane surfaced): (1) **§4 build image** — the hermetic vendor
  runs in **`python:3.14-slim-bookworm`** (the base the curated image derives its Python from — same glibc, has
  pip+shell), **not** the curated image itself, which is custom distroless (no pip/shell, ADR-0049); (2) impl fixes
  (not decision changes): `Pull` **streams** the bundle layer (oras `content.FetchAll` caps at 32 MiB — a ~100MB
  vendored closure must stream via `target.Fetch` + a verifying reader); `build.py` `PYTHONPATH` export scoping; the
  example's `pydantic` build-dep. The normal rule (correct a frozen ADR via a superseding ADR) is preserved for
  every other ADR.
- **Deciders**: green-0-rabbit
- **Tags**: artifact, bundle, python, native-deps, runtime, contract, lakehouse
- **Realizes**: [FEAT-0003/F59](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0031](0031-oci-artifact-distribution-oras.md) (the single-file OCI artifact this
  **extends** with a bundle layer — ADR-0031 stays frozen) · [ADR-0035](0035-artifact-digest-resolution-at-revision.md)
  (digest resolution — a bundle resolves identically) · [ADR-0032](0032-curated-runtime-images-container-execution.md)
  (container execution + the artifact bind-mount + env this adds `PYTHONPATH`/`FUNCD_BUNDLE_DIR` to) ·
  [ADR-0030](0030-function-execution-runtime-shim-node.md) (the shim load — **unchanged**, still one entry file) ·
  [ADR-0049](0049-python-runtime-shim.md) (the stdlib-only Python shim — **preserved**: the *shim* stays
  stdlib-only; the *artifact* may now carry deps) · [ADR-0058](0058-contract-codegen-from-code-types.md) /
  [ADR-0059](0059-contract-as-oci-metadata.md) (the baked I/O validators + the OCI contract layer —
  the bundle carries its schema and push gates it) · [ADR-0090](0090-mandatory-single-io-schema.md) (the single
  **mandatory** I/O schema surface — void = `{"type":"null"}` — this bundle embeds as `__funcd_contract.json`
  and gates) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)
  (F48 — its consumer function is the first user of a native-dep bundle).

## Context & Need

**Purpose**: let a Python function's **artifact** be a *deployment package* — a directory carrying the
handler **plus its vendored non-stdlib dependencies** (a native wheel like `duckdb==1.5.4` and its
pre-installed DuckDB extensions) — so it runs **unchanged on the stock curated `python314` runtime**. This
is the Python equivalent of the JS esbuild bundle (the F47 `s3-roundtrip` example bundles
`@aws-sdk/client-s3` into its `.mjs`) and the AWS Lambda deployment-package model.

**Callers**: any function author whose handler needs a package the curated runtime does not ship — first
and concretely the **F48 catalog consumer** ([ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md);
`examples/python/catalog-quack/src/handler.py`), whose Quack client *is* a local DuckDB with the `quack`
extension. Empirically verified this session: the stock `python314` image (which already carries the stdlib
C-extension lib closure — `libz`/`libssl`/`ffi`/`sqlite`) loads a bundled `duckdb` wheel + pre-installed
`quack`+`httpfs` extensions and completes a full Quack round-trip **with zero runtime changes**.

**The gap**: today a Python artifact is a **single `.py` file** — `funcdctl push counter.py` reads one file
([`artifact.Push`](../../internal/artifact/artifact.go)), the shim `spec_from_file_location`s that one file
([`runtime.py`](../../shim/python/src/funcd_shim/runtime.py)), and nothing puts a vendored directory on
`sys.path`. So a native dependency is un-importable. This ADR closes exactly that gap — **not** by adding a
heavier `python-duckdb` curated runtime (explicitly rejected below: the Lambda *container-image* path,
overkill here), but by letting the artifact be a bundle.

## Scenarios

- **scenario: push-directory-bundles-tar** — Given a bundle directory (handler + vendored deps), When
  `funcdctl push ./bundle ref --entry handler.py`, Then the OCI artifact carries one **tar+gzip bundle
  layer** (media type `application/vnd.funcd.bundle.tar+gzip`) with the entry recorded, and prints
  `<ref>@<digest>`.
- **scenario: push-single-file-unchanged** — Given a lone `handler.py`, When `funcdctl push handler.py ref`,
  Then the artifact is the unchanged ADR-0031 single-blob layer (no tar) — full backward-compat.
- **scenario: packbundle-deterministic** — Given the same bundle tree pushed twice, Then the tar bytes (and
  thus the digest) are identical (sorted entries, zeroed mtimes, uid/gid 0).
- **scenario: push-gates-bundle-contract** — Given a bundle declaring typed I/O, When push runs, Then it
  **refuses** (`fault.Invalid`) unless the bundle contains a valid `__funcd_contract.json` **and** the
  handler carries the matching baked validators (`__funcd_validate_input`/`_output`); a well-formed bundle
  promotes that schema to the ADR-0059 OCI contract layer.
- **scenario: pull-untars-bundle** — Given a bundle artifact, When the materializer pulls it by digest, Then
  the tree is untarred into the artifact dir (paths sanitized against traversal), the digest verified, and
  `FUNCD_ARTIFACT` resolves to `<dir>/<entry>`.
- **scenario: bundle-imports-vendored-dep** *(e2e)* — Given the F48 consumer bundle vendoring `duckdb`, When
  it runs on stock `python314`, Then `import duckdb` succeeds (via `PYTHONPATH`) and the handler returns
  rows.
- **scenario: bundle-locates-native-assets** *(e2e)* — Given the bundle ships `duckdb-ext/`, When the
  handler resolves it via `FUNCD_BUNDLE_DIR`, Then the `quack`/`httpfs` extensions load and a Quack query
  round-trips.
- **scenario: build-matches-runtime-platform** *(e2e)* — Given `build.py` vendors the wheel **inside the
  curated `python314` image**, Then the vendored `.so` closure is glibc/arch-matched to the runtime (no
  “built-on-host → glibc break”).
- **scenario: backward-compat-single-file-runs** — Given an existing single-`.py` function, Then it deploys
  and runs unchanged (no bundle, `PYTHONPATH` harmless).

## Scope

**In**: the multi-file **artifact** capability — `funcdctl push` accepting a directory → a tar+gzip OCI
bundle layer; `Pull`/materialize untarring it; the runtime env (`PYTHONPATH` + `FUNCD_BUNDLE_DIR`) that
makes vendored deps importable; the push-time bundle-contract gate; a reference `build.py` that vendors
hermetically (inside the curated image) and pre-installs extensions offline; single-file backward-compat.

**Out** (own follow-ups): the `spec.catalogs` **consumer binding** (endpoint/token injection + the egress
grant — Project #4 *“Add-on provider consumption binding”*); a Node bundle beyond today's esbuild `.mjs`
(the model generalizes but this ADR's contract-gate + `build.py` are Python); any new curated runtime
(explicitly rejected); a general artifact-size/layer-caching policy.

## Constraints & Decision drivers

- **Stock runtime, unchanged** — the curated `python314` image ([ADR-0049](0049-python-runtime-shim.md))
  and the shim stay exactly as they are; the capability lives in the *artifact + push/materialize* layer.
- **Single-file backward-compat is non-negotiable** — the common case (`push handler.py`) must not change.
- **Pure-Go daemon** — packing/unpacking uses `archive/tar` + `compress/gzip` (stdlib); no new dependency.
- **Reproducible artifact** — same tree ⇒ same digest (deterministic tar), so ADR-0035 digest-pinning holds.
- **Platform-match or fail loudly** — the wheel closure must match the runtime's glibc/arch; the build
  guarantees it hermetically rather than hoping host `pip` picked a compatible wheel.
- **The contract travels with the code** — the I/O schema lives *in* the bundle and is gated at push
  (composes with [ADR-0058](0058-contract-codegen-from-code-types.md)/[ADR-0059](0059-contract-as-oci-metadata.md)).

## Alternatives considered

- **A `python-duckdb` curated runtime** (bake duckdb + extensions into a new image; the function selects it
  via `spec.runtime`). *Rejected*: this is the Lambda *container-image* path — a new curated image per
  native-dep combination, an image build/publish/pin burden, and it special-cases duckdb into the platform.
  The bundle model carries the dep in the *artifact*, so **one** stock runtime serves every native-dep
  function; duckdb is just the first. (The empirical test this session proved the stock runtime suffices.)
- **zip layer (Lambda-literal format)**. *Rejected*: zip is the exact AWS deployment-package format but is
  less native to OCI/Go tooling and buys nothing over tar here; `archive/tar`+`gzip` is stdlib and streams.
- **Shim prepends the bundle dir to `sys.path`**. *Rejected*: pushes bundle-awareness into the shim
  (erodes the ADR-0049 stdlib-only/dumb-shim line) and is Python-specific; setting `PYTHONPATH` in the
  worker env is language-generic and leaves the shim untouched. Python honors `PYTHONPATH` at interpreter
  startup, before the shim runs.
- **Host `pip install --platform manylinux_2_36 --only-binary=:all:`**. *Rejected as the guarantee*:
  `--platform` can silently resolve a subtly-wrong wheel and pure-Python transitive deps aren't
  platform-tagged — the “built-on-Mac → glibc break” footgun survives. Building *inside* the curated image
  is byte-for-byte hermetic. (Kept as a documented fast-path caveat, not the contract.)
- **A per-native-lib platform env** (the platform injects `DUCKDB_EXTENSION_DIRECTORY`, …). *Rejected*: does
  not scale — the platform would learn every library. `PYTHONPATH` covers *all* Python deps generically;
  `FUNCD_BUNDLE_DIR` lets the *handler* locate its own asset dirs. The platform stays library-agnostic.

## Decision

1. **Bundle = a directory pushed as one deterministic tar+gzip OCI layer.** A new media type
   `application/vnd.funcd.bundle.tar+gzip` sits **alongside** the ADR-0031 single-blob layer (additive).
   `funcdctl push <path> <ref>`: if `<path>` is a **directory** → bundle; if a **file** → the unchanged
   single-blob push. The handler entry (relative to the bundle root, default `handler.py`) is recorded in a
   manifest/layer annotation `dev.funcd.bundle.entry` via `--entry`. The tar is deterministic (entries
   sorted; mtime/uid/gid zeroed) so identical trees yield an identical digest (ADR-0035 holds).

2. **The runtime finds deps via generic env — no per-library knowledge.** The bundle untars into the
   artifact tree the materializer already produces: in **container mode** that dir is bind-mounted
   (`Dir(artifactPath)` → `/var/funcd/artifact`, ADR-0032 — *unchanged*); in **process mode** (ADR-0030)
   there is no mount — `Pull` untars into the materializer's per-digest cache dir and `bundleRoot` *is*
   that dir. The two modes converge on the *env*, not a mount. The materializer sets, in the worker env:
   - `FUNCD_BUNDLE_DIR = <bundle-root>` — generic, any runtime; a handler resolves its own asset dirs from
     it (e.g. `os.path.join(FUNCD_BUNDLE_DIR, "duckdb-ext")`).
   - `PYTHONPATH = <bundle-root>` — for the python runtime family; makes **every** vendored dependency
     importable. This is the *one* mechanism; the platform never names a specific library.

   `FUNCD_ARTIFACT` points at `<bundle-root>/<entry>`; the shim loads that single entry file **unchanged**.
   (`--entry` names the entry *file* → `FUNCD_ARTIFACT`; the handler *export* stays `fn.Spec.Handler` →
   `FUNCD_HANDLER`, default `handle` — the two are orthogonal, unchanged.) A single-file artifact sets the
   same env (harmless — the dir holds only the one file), so bundle vs single-file is transparent to the
   runtime.

3. **The contract travels in the bundle and push gates it — new build+push behavior over the existing
   contract path.** This is *not* already present; it is added. `build.py` gains a step that emits
   `<bundle>/__funcd_contract.json` (the ADR-0059 `{input?, output?, dialect}` shape) **in addition to**
   baking the ADR-0058 precompiled validators (`__funcd_validate_input`/`_output`) into the entry file
   (which its AST baker already does). `funcdctl push` calls the **new** `VerifyBundleContract`, which
   (a) validates the JSON parses as the ADR-0059 shape and (b) confirms the entry file *defines* the baked
   validator symbol for each side the schema declares (a static source check of the symbols the AST baker
   injects); otherwise the push fails with `fault.Invalid`. A verified contract is promoted to the OCI
   contract layer through the **existing** `artifact.ContractBlob`/contract-layer path (ADR-0059), so
   `funcdctl inspect` reads it without a pull. A bundle thus reaches the *same* contract blob as a
   single-file function, only by a different source — the bundle-embedded JSON. Per
   [ADR-0090](0090-mandatory-single-io-schema.md) both `input` and `output` are **mandatory** (a void side is
   `{"type":"null"}`), so `VerifyBundleContract` also fails a bundle carrying only one side. This makes the
   deployed artifact self-describing and tamper-evident.

4. **The build is hermetic.** The reference `build.py` runs `pip install --target <bundle>` **inside
   `python:3.14-slim-bookworm`** — the exact base the curated runtime derives its Python from
   (`images/runtime/python314/Dockerfile` `FROM python:3.14-slim-bookworm AS py`), same glibc/arch and it *has*
   pip+shell — and pre-installs the DuckDB extensions **offline** into `<bundle>/duckdb-ext/`, so the vendored
   native closure is byte-for-byte the runtime's glibc/arch. (It does **not** vendor in the curated image itself:
   that image is custom distroless — no pip, no shell, ADR-0049 — so `pip` cannot run there. Amended from the
   original "inside the curated image" wording; see the header.)

*Language-agnostic by design.* The transport machinery — the `BundleTarMediaType` layer, the traversal-safe
untar, `FUNCD_BUNDLE_DIR`, and the digest-pinned bundle — knows nothing about Python; only the `PYTHONPATH`
env is python-family-gated (Node resolves `node_modules` from the entry file's own directory natively, so it
needs no equivalent). A **Node native-addon bundle** (a `.mjs` entry + `.node` N-API binaries esbuild cannot
inline — the JS peer of a native wheel) **reuses this same machinery**; only its build (`npm ci` inside the
curated `nodejs22` image) and its contract emission are Node-specific and **deferred** (Scope / Open
questions). For *pure-JS* deps, esbuild's single `.mjs` stays the simpler single-file path — the bundle is
for the native case, in either language.

## Temporary workarounds

- **Until the `spec.catalogs` consumer binding lands** (Project #4), the F48 consumer receives its catalog
  URL/token via a bound `Secret`/`ConfigMap` rather than injected `FUNCD_CATALOG_*` env. *Exit*: the
  consumer-binding ADR — this bundling ADR does **not** depend on it (bundling is orthogonal to endpoint
  injection).
- **Host-`pip` fast path** for iteration: `build.py --no-hermetic` may vendor via host `pip --platform`;
  documented as unsafe for release. *Exit*: none needed — the hermetic path is the default and the only
  release-blessed one.

## Contracts

### `internal/artifact` — bundle layer (additive to ADR-0031)

```go
// Media type + entry annotation for a multi-file function bundle (a deployment package).
// The single-file layer (ADR-0031) is unchanged; this is additive.
const (
	BundleTarMediaType    = "application/vnd.funcd.bundle.tar+gzip"
	BundleEntryAnnotation = "dev.funcd.bundle.entry" // handler file, relative to the bundle root
)

// PackBundle writes dir as a DETERMINISTIC tar+gzip stream (entries sorted; mtime/uid/gid
// zeroed) so identical trees yield an identical digest. entry must name an existing regular
// file relative to dir (the handler).
func PackBundle(dir, entry string) (data []byte, err error)

// VerifyBundleContract enforces the push-time contract gate: if dir/__funcd_contract.json is
// present it must be valid JSON Schema, and for each declared side (input/output) the entry
// file must carry the matching baked validator symbol (ADR-0058). Returns the contract bytes
// to promote to the OCI layer (nil if the bundle declares no typed I/O). fault.Invalid on a
// missing/inconsistent contract.
func VerifyBundleContract(dir, entry string) (contract []byte, err error)

// PushBundle packs dir (PackBundle) as a BundleTarMediaType layer + entry annotation, adds the
// VerifyBundleContract result as the ADR-0059 contract layer, and pushes it. Returns the
// manifest digest.
func PushBundle(ctx context.Context, ref, dir, entry string) (digest string, err error)

// Push (UNCHANGED, ADR-0031): a single file → the single-blob BundleMediaType layer.
// func Push(ctx context.Context, ref, file string, contract []byte) (digest string, err error)

// Pull (EXTENDED): a BundleTarMediaType layer untars into dir (paths sanitized against
// traversal) and returns dir/<entry-from-annotation>; a single-blob layer keeps ADR-0031
// behavior. Digest verified either way.
// func Pull(ctx context.Context, ref, digest, dir string) (path string, err error)
```

### `internal/function` — worker env (ADR-0032 container mode + ADR-0030 process mode)

```go
// workerSpec gains, for both execution modes, generic bundle env (the artifact dir is already
// bind-mounted; a bundle just populates it with more files):
env["FUNCD_BUNDLE_DIR"] = bundleRoot                 // == containerArtifactDir (container) / Dir(artifactPath) (process)
if isPythonFamily(fn.Spec.Runtime) {                 // vendored deps import; ADR-0049 shim unchanged
	env["PYTHONPATH"] = bundleRoot
}
// FUNCD_ARTIFACT already resolves to <bundleRoot>/<entry>; the shim loads that one file unchanged.
```

### `funcdctl push` (CLI)

`push <path> <ref> [--entry <relpath>]` — `<path>` a **directory** ⇒ bundle (`--entry` default
`handler.py`); a **file** ⇒ the single-blob push (unchanged). The **contract surface** — a single **mandatory**
`--schema` / code-derived document, both `input` and `output` required, void = `{"type":"null"}` — is defined
by **[ADR-0090](0090-mandatory-single-io-schema.md)** (which supersedes the old `--contract-input`/`--contract-output`
flags). A **bundle** sources its contract from the embedded `<dir>/__funcd_contract.json` (Decision §3), which
`push` gates per ADR-0090's mandatory both-keys rule. (Node bundles are out of scope here; `--entry` defaults
to `handler.py`.)

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| a bundle directory (build output: entry + vendored deps + `__funcd_contract.json` + `duckdb-ext/`) | a `BundleTarMediaType` OCI layer + `dev.funcd.bundle.entry` annotation |
| the curated `python314` image (build-time, hermetic `pip --target`) | the untarred tree at the bind-mounted artifact dir |
| the OCI target (ADR-0031) | `FUNCD_BUNDLE_DIR` (generic) + `PYTHONPATH` (python family) worker env |
| `__funcd_contract.json` (ADR-0059 shape) | the promoted OCI contract layer (`funcdctl inspect` reads it) |

## Implementation plan

**Files**
- `internal/artifact/bundle.go` — `PackBundle` (deterministic tar+gzip), untar-in-`Pull` (traversal-safe),
  `VerifyBundleContract`, `PushBundle`; extend `Push`/`Pull` media-type selection; add `BundleTarMediaType`
  + `BundleEntryAnnotation`.
- `internal/artifact/artifact.go` — `OrasMaterializer.Materialize` **cache-hit fast-path** must be taught
  the entry: today a hit returns `filepath.Join(cacheDir, entries[0].Name())`, which for an untarred
  multi-file bundle is non-deterministic and likely not the entry. Read the entry back from the manifest
  annotation (or a sidecar written on the miss path) so a cached bundle returns `<cacheDir>/<entry>`.
- `cmd/funcdctl/cli.go` — `pushCmd` detects dir vs file, adds `--entry`, routes to `PushBundle`.
- `internal/function/function.go` — `workerSpec` sets `FUNCD_BUNDLE_DIR` (both modes) + `PYTHONPATH`
  (python family); add `isPythonFamily`.
- `examples/python/catalog-quack/build.py` — hermetic `pip install --target` inside the curated image +
  offline extension pre-install into `duckdb-ext/` + write `__funcd_contract.json` + bake ADR-0058
  validators into the entry; emit `bundle/`.
- `examples/python/catalog-quack/` — rewire `consumer.yaml` to `runtime: python314` + a deployable bundle;
  README update.

**go.mod**: none (stdlib `archive/tar`, `compress/gzip`).

**Test plan** (one test per scenario; contract/unit now, e2e deferred to the Lima lane per roadmap
test-sequencing):
- `internal/artifact`: `push-directory-bundles-tar`, `push-single-file-unchanged`, `packbundle-deterministic`
  (same tree → same digest), `push-gates-bundle-contract` (missing/inconsistent → `fault.Invalid`; good →
  promoted layer), `pull-untars-bundle` (+ a path-traversal-rejection assertion), `pull-single-file-unchanged`.
- `internal/function`: `bundle-sets-pythonpath-and-bundledir` (workerSpec env, both modes),
  `single-file-no-env-regression`.
- **e2e** (`just lima-example-duckdb`, deferred): `bundle-imports-vendored-dep`, `bundle-locates-native-assets`,
  `build-matches-runtime-platform`, `backward-compat-single-file-runs` — a third venom testcase invoking the
  deployed `catalog-reader` function over Quack.

**Definition of done**: the four sub-checks green (`go build ./...` · `go tool golangci-lint run ./...` ·
`go test ./...` · `go mod verify`); every non-e2e scenario a named passing test; the F48 consumer bundle
builds hermetically and (e2e) round-trips; single-file functions unchanged; no identity/path leak.

## Review checklist

- [ ] `push <dir>` produces a `BundleTarMediaType` layer with `dev.funcd.bundle.entry`; `push <file>`
      is the unchanged single-blob artifact.
- [ ] `PackBundle` is deterministic (same tree ⇒ identical bytes/digest).
- [ ] `Pull` untars a bundle traversal-safely and verifies the digest; single-file `Pull` unchanged.
- [ ] `VerifyBundleContract` fails a bundle whose declared I/O schema lacks the matching baked validator or
      whose schema is invalid; a good contract is promoted to the OCI contract layer.
- [ ] `workerSpec` sets `FUNCD_BUNDLE_DIR` (both modes) + `PYTHONPATH` (python family); single-file sets the
      same env harmlessly; the shim is unchanged.
- [ ] No new dependency; `archive/tar`+`compress/gzip` only.
- [ ] Every non-e2e scenario has a named passing test; e2e ones are recorded as deferred.
- [ ] No `any` in APIs; `api/fault` for errors; no identity/path leak.

## Consequences

- **One stock runtime serves every native-dep function** — duckdb is just the first; no per-dep curated
  image. The F48 consumer becomes a deployable, invokable Function.
- **Artifacts get bigger** (a vendored wheel + extensions ≈ tens of MB) — acceptable; the digest-pinned OCI
  layer is cached by containerd. A size/eviction policy is a future concern (noted out of scope).
- **The build now needs Docker** (already required to build the curated images) and is slower (hermetic
  vendoring) — the correctness win (no glibc footgun) is worth it; the host fast-path exists for iteration.
- **The contract is self-describing** — the schema travels in the bundle and is gated at push, so a
  deployed bundle can't silently drift from its declared I/O.

## Open questions

- Node bundles beyond esbuild `.mjs` (a `node_modules` bundle with the same contract gate) — deferred to a
  follow-up ADR when a Node native-dep case appears.
- A `.funcdignore` / bundle-size cap — deferred; `build.py` curates the tree for now.

## References

- [ADR-0031](0031-oci-artifact-distribution-oras.md), [ADR-0032](0032-curated-runtime-images-container-execution.md),
  [ADR-0030](0030-function-execution-runtime-shim-node.md), [ADR-0049](0049-python-runtime-shim.md),
  [ADR-0058](0058-contract-codegen-from-code-types.md), [ADR-0059](0059-contract-as-oci-metadata.md),
  [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md).
- AWS Lambda deployment packages & layers (the model this mirrors) — verified 2026-07-01.
- Empirical validation (this session): stock `python314` + bundled `duckdb==1.5.4` wheel + offline
  `quack`/`httpfs` extensions → full Quack round-trip, zero runtime changes.
