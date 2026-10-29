# ADR-0298: A function artifact carries an SBOM, one per platform manifest

- **Status**: Accepted (2026-10-29; judged twice by the author under the adr-judge skill, no Blocker; the decider
  confirmed the five choices the draft made: the file beside the build, a warning when there is none, a refused push
  for a broken one, always written, and Python's from the vendored `*.dist-info`)
- **Date**: 2026-10-29
- **Deciders**: green-0-rabbit
- **Tags**: artifact, sbom, supply-chain, funcdctl, bundling
- **Realizes**: [FEAT-0017/F214](../feat/0017-feat-artifact-supply-chain.md) (an SBOM for every function artifact)
- **Answers**: the decider's refinement of 2026-10-29: the bundlers write each function's SBOM and `funcdctl push`
  attaches it (option B); functions are built both in GitHub Actions and with funcdctl on a dev machine or homebox,
  so signing stays optional; verifying provenance before a worker starts is its own ADR later (F216)
- **Relates to**: [ADR-0031](0031-oci-artifact-distribution-oras.md) (push and pull) ·
  [ADR-0089](0089-python-function-dependency-bundling.md) (the bundle layer) ·
  [ADR-0144](0144-bundling-in-the-language-toolchains.md) (the bundlers) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) (one manifest per platform) ·
  [ADR-0247](0247-contract-file-replaces-funcdctl-yaml.md) (a build's files) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (language-repo releases) ·
  [ADR-0299](0299-funcdctl-scan-matches-sboms-in-an-extension.md) (the scan that reads these SBOMs)

## Context & Need

A function artifact is an OCI manifest of artifactType `application/vnd.funcd.function.artifact.v1` with a bundle
layer and a contract layer (`internal/artifact/artifact.go:50-57`, `bundle.go:43`), and nothing a scanner reads.
Experiment 0015 ([pyvvo/experiment#27](https://github.com/pyvvo/experiment/pull/27)) measured it:

- `trivy image`, syft, grype and zot v2.1.22's scanner refuse a function manifest; through a multi-arch index,
  `trivy image` exits 0 with no package and zot answers no CVE and no error;
- the Vite plugin inlines every dependency (`noExternal: true`, funcd-typescript `vite-plugin/src/index.ts:65`), so
  every scanner reads 0 packages from a `.mjs`;
- funcd-bundle keeps each `*.dist-info` (funcd-python `bundle/src/funcd_bundle/bundle.py:259`), so `trivy rootfs`
  reads a Python bundle, but only once it is pulled and unpacked: 1.05 s for a 76 MiB bundle;
- a CycloneDX SBOM attached to each platform manifest as an OCI referrer works with Trivy, Grype and OSV-Scanner,
  gives Trivy the same 33 advisories as the unpacked bundle, and rescans in about 65 ms at any bundle size; the
  manifest's digest and tags, `funcdctl pull` and `funcdctl inspect` stay as they were, in zot and in GHCR.

**Purpose**: every function artifact funcdctl pushes carries, for each platform, an SBOM of exactly what its bundle
ships, so a scanner (ADR-0299, or any tool that reads CycloneDX) checks a function by its digest without its bundle.
The bundlers write the SBOM, `funcdctl push` attaches it, and a scanner finds it by the manifest's digest.

## Scenarios

- **scenario: ts-sbom-names-what-ships**: Given a TS function that inlines lodash 4.17.20 and whose project also
  declares minimist 1.2.5, which no source imports, When `vite build` runs with `@funcd-dev/vite-plugin`, Then
  `render.cdx.json` beside `render.mjs` names exactly `pkg:npm/lodash@4.17.20`.
- **scenario: python-sbom-per-platform**: Given a Python function locked to requests 2.25.1 and cryptography
  41.0.0, When `uv run funcd-bundle --platform linux/amd64 --platform linux/arm64` runs, Then each platform's
  `fetch.cdx.json` beside its bundle directory names every vendored distribution with its version, and not
  funcd-shim, which the runtime provides.
- **scenario: push-attaches-sbom**: Given `dist/fetch` with `dist/fetch.cdx.json` beside it, When
  `funcdctl push dist/fetch <ref>` runs, Then the pushed manifest has one referrer of artifactType
  `application/vnd.cyclonedx+json` whose layer is that file, and the manifest's digest equals that of a push without
  the SBOM.
- **scenario: index-keeps-platform-sboms**: Given two per-platform pushes, each with its SBOM, When `funcdctl index`
  combines them, Then each platform manifest keeps its own referrer and the index has none.
- **scenario: repush-is-idempotent**: Given a function pushed with its SBOM, When it is pushed again unchanged, Then
  its manifest still has exactly one SBOM referrer.
- **scenario: push-without-sbom**: Given a build with no `<name>.cdx.json` beside it, When `funcdctl push` runs, Then
  the artifact is pushed as before and stderr says that it carries no SBOM.
- **scenario: push-refuses-broken-sbom**: Given a `<name>.cdx.json` that is not CycloneDX JSON, When `funcdctl push`
  runs, Then nothing is pushed and the error names the file.

## Scope

**In**: the SBOM file each bundler writes; `funcdctl push` attaching it as a referrer of each manifest it pushes; the
file's format and check; an example of signing it in GitHub Actions.
**Out**: scanning (ADR-0299); checking a signature or provenance before a worker starts (F216); SBOMs for Site
bundles and App templates; the runtime images and the shim, which are ordinary images that image scanners read;
funcdctl signing anything.

## Constraints & Decision drivers

- The artifact's digest does not change: a Revision pins it (ADR-0035), and `funcdctl pull` reads it as before. A
  referrer leaves its subject as it is.
- The SBOM names what ships, not what a lockfile names: in experiment 0015 the lockfile SBOM also named the unused
  minimist, and 65 packages with dev dependencies.
- funcdctl stays toolchain-free (ADR-0144): it reads the file a bundler wrote and runs no language tool.
- CycloneDX JSON: Trivy, Grype and OSV-Scanner read it, and `actions/attest` signs it only when it carries
  `bomFormat`, `serialNumber` and `specVersion` (`src/sbom.ts` at v4.2.2).
- Reproducible: the same bundle gives the same SBOM and the same referrer digest, as its own manifest does
  (`reproducible`, `internal/artifact/artifact.go:115`).

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| A. Docs only: each user makes and attaches an SBOM in CI | No code | A TS bundle gets no exact SBOM without its bundler; every user rebuilds the recipe | Rejected (decider) |
| **B. The bundlers write it, `funcdctl push` attaches it** | Exact: the module graph, the vendored distributions; every push carries it, local or CI | Two language-repo releases and a funcdctl change | **Chosen** |
| C. Signed only: an attestation, no plain referrer | Trusted by construction | GitHub Actions only: a push from a dev machine or homebox gets no SBOM | Rejected (decider): signing stays optional |
| D. The SBOM inside the bundle layer | One blob | A scanner reads it only after pulling the bundle; a better SBOM changes the bundle's digest | Rejected |
| E. funcdctl makes the SBOM at push | One tool | funcdctl would learn each ecosystem, which ADR-0144 moved to the bundlers; a `.mjs` has no metadata left to read | Rejected |
| F. SPDX | Equal support in the scanners and `actions/attest` | Two formats to write and check; experiment 0015 validated CycloneDX with all three scanners | Rejected: one format |

## Decision

1. **The file.** A build's SBOM is `<name>.cdx.json` beside its push path, `<name>` as `sdk.ContractName` gives it:
   beside `render.mjs`, or beside the directory `dist/fetch`, as `dist/fetch.cdx.json`, outside the bundle. It is
   CycloneDX 1.5 JSON with `bomFormat`, `specVersion`, a `serialNumber` derived from its content (a name-based UUID,
   so the same packages give the same file), `metadata.component` naming the function, and one `library` component
   per package with its `purl`.
2. **The TS bundler.** `@funcd-dev/vite-plugin` writes it for each function, on every build, from its chunk: each
   module under `node_modules` gives its package's `name` and `version` from that package's `package.json`, as
   `pkg:npm/<name>@<version>`, unless its rendered length is 0 (tree-shaken to nothing). Experiment 0015's prototype
   (`fn/ts/sbom-plugin.ts`) is the reference.
3. **The Python bundler.** `funcd-bundle` writes one per platform on every build, beside each platform's bundle
   directory, from each vendored `*.dist-info/METADATA` (`Name`, `Version`), as `pkg:pypi/<name>@<version>` with the
   name normalized as the purl spec does for PyPI. What it prunes (funcd-shim and its dependencies, which the runtime
   provides) is not in the bundle, so not in the SBOM.
4. **The push.** A function push (`--site` and `--template` are unchanged) checks the file before it pushes anything,
   then attaches it as an OCI 1.1 referrer of the manifest it pushed: a reproducible manifest of artifactType
   `application/vnd.cyclonedx+json`, the file as its one layer of the same media type, `subject` that manifest, in the
   same target, a registry or an OCI layout. oras-go falls back to the referrers tag schema on a registry without the
   referrers API. stdout stays `<ref>@<digest>`, and a failed attach fails the command; a rerun attaches it, since both
   pushes are idempotent. Without the file it pushes as today and prints `no SBOM beside <path>; the artifact carries
   none` on stderr. A file that is not JSON with `bomFormat: CycloneDX`, a `specVersion` and a `serialNumber` stops the
   push. `funcdctl index` attaches nothing: each platform manifest keeps its own. `artifact.SBOMs` reads a manifest's
   SBOMs back, for the scan (ADR-0299) and the tests.
5. **Signing, optional.** A GitHub Actions build may sign the same file with `actions/attest` (`sbom-path`,
   `subject-digest` each platform manifest's digest, `push-to-registry: true`), as `examples/artifact-sboms.md` (NEW)
   shows, after experiment 0015's workflow. funcdctl signs nothing and checks no signature (F216).

## Design model

None: no protocol runs between actors. A push writes its manifest, then the referrer that names it; a crash between
the two leaves an artifact without an SBOM, which a rerun completes.

## Temporary workarounds

None.

## Contracts

```go
// Package artifact (internal/artifact), NEW.
const SBOMMediaType = "application/vnd.cyclonedx+json" // the referrer's artifactType and its layer's media type

// SBOMPath is the SBOM file of a push of path: <name>.cdx.json in path's parent directory for a directory, in
// path's directory for a file, name as sdk.ContractName(path, isDir) gives it.
func SBOMPath(path string, isDir bool) string

// CheckSBOM refuses data that is not CycloneDX JSON with bomFormat "CycloneDX", a specVersion and a serialNumber.
func CheckSBOM(data []byte) error

// AttachSBOM pushes sbom to ref's target as a reproducible referrer of the manifest digest and returns the
// referrer's digest. It is idempotent: the same sbom and digest give the same referrer.
func AttachSBOM(ctx context.Context, ref, digest string, sbom []byte) (string, error)

// SBOMs returns the layer of each CycloneDX referrer of the manifest digest in ref's target, digest-verified; none
// is an empty slice, not an error.
func SBOMs(ctx context.Context, ref, digest string) ([][]byte, error)
```

`pushFromContract` (`cmd/funcdctl/contract.go:77`) reads `SBOMPath`, runs `CheckSBOM` before the push and
`AttachSBOM` after it. The bundlers' options do not change: both always write the SBOM.

| Consumes | Exposes |
|---|---|
| `<name>.cdx.json` beside a push path | One CycloneDX referrer per pushed manifest; a stderr notice when there is none; a refused push for a broken file |

## Implementation plan

- funcd-typescript: `vite-plugin/src/index.ts` writes each function's SBOM (from the prototype), with a test for
  `ts-sbom-names-what-ships`; an npm release.
- funcd-python: `bundle/src/funcd_bundle/bundle.py` writes each platform's SBOM, with a test for
  `python-sbom-per-platform`; a PyPI release.
- funcd: `internal/artifact` (`SBOMMediaType`, `SBOMPath`, `CheckSBOM`, `AttachSBOM`, `SBOMs`) and
  `pushFromContract`; `TestScenarioPushAttachesSBOM`, `…IndexKeepsPlatformSBOMs`, `…RepushIsIdempotent`,
  `…PushWithoutSBOM` and `…PushRefusesBrokenSBOM` on an OCI layout; `examples/artifact-sboms.md`; the changelog; at
  acceptance, `blueprint.md`'s artifact-trust bullet gains the SBOM referrer.
- funcd-functions: the examples' lockfiles move to the new bundler releases, so the lanes push SBOMs.
- **Done when**: the tests pass with `-race`; build, vet and lint are clean; `scripts/agent/gate.sh` passes; an
  example a lane pushes carries its referrer (`oras discover` on the lane's layout).

## Review checklist

- [ ] A manifest's digest is the same with and without its SBOM; `funcdctl pull` and `funcdctl inspect` are unchanged.
- [ ] The referrer manifest is reproducible: a repush leaves one referrer.
- [ ] funcdctl reads the file only; it runs no language tool and writes no SBOM.
- [ ] The TS SBOM names only packages whose modules ship code; the Python SBOM names only vendored distributions.
- [ ] Each scenario has its named test.

## Consequences

- **Positive**: any CycloneDX scanner checks a function by its manifest's digest, without its bundle; a CI can sign
  the same file.
- **Negative (accepted)**: a push writes one more blob (8.9 KiB for experiment 0015's Python function, 0.5 KiB for
  its TS one) and one referrer per platform manifest; an artifact pushed before this ADR or by another tool has no
  SBOM, which ADR-0299 reports; funcdctl trusts the file beside the build, so a stale one another tool left behind is
  attached as it is (the bundlers rewrite theirs on every build); native code inside a wheel counts only as its Python
  package; a uv workspace member gets a `pkg:pypi` purl, so a public package of the same name could lend it false
  advisories.

## Open questions

- SBOMs for Site bundles and App templates: a later row, if a Site's libraries need scanning.
- Checking a signed SBOM or provenance before a worker starts: F216's ADR.

## References

- Experiment 0015, [pyvvo/experiment#27](https://github.com/pyvvo/experiment/pull/27): the scanners on funcd
  artifacts, the module-graph prototype, and the attestation run on pyvvo/funcd-functions branch
  `exp/0015-attestation`.
- The decider's refinement of 2026-10-29; FEAT-0017.
- `actions/attest` v4.2.2 `src/sbom.ts` (CycloneDX needs a `serialNumber`); OCI image-spec v1.1 `subject` (go.mod
  pins image-spec v1.1.1 and oras-go v2.7.0).
- `internal/artifact/artifact.go:50-57,115,130`, `bundle.go:43,223`, `cmd/funcdctl/contract.go:33,77`.
