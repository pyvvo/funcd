# ADR-0059: Function I/O contract as OCI manifest metadata — statically inspectable without running the artifact

- **Status**: Implemented
- **Superseded in part by**: [ADR-0090](0090-mandatory-single-io-schema.md) (2026-07-01) — the *opt-in, two-flag*
  contract surface (`--contract-input`/`--contract-output`; a contract-less artifact when neither is given)
  becomes a single **mandatory** `--schema`. This ADR's OCI-metadata *mechanism* (contract blob + manifest
  annotation + `inspect`-without-pull) is **kept**, unchanged.
- **Date**: 2026-06-19
- **Deciders**: green-0-rabbit
- **Judge note (accepted 2026-06-19)**: folded the judge's Major (the keyed `{input?, output?}` blob needs labeled
  `--contract-input`/`--contract-output` at the CLI, not the prior unlabeled `--contract`) and Minor (a bare-tag
  inspect resolves the tag; the digest-pinning guarantee is for digest refs). Decision unchanged.
- **Tags**: runtime, artifact, oci, contract, registry, distribution
- **Realizes**: [FEAT-0001/F30](../feat/0001-feat-v1.1.md) (contract as OCI manifest metadata) — v1.1.
- **Refines**: [ADR-0031](0031-oci-artifact-distribution-oras.md) — **additively** extends the OCI function artifact
  with a contract blob + manifest annotation, using the `oras.PackManifest` seam ADR-0031 already exposes. It reverses
  no ADR-0031 decision (same artifact type, same digest-pinned distribution), so it refines, not supersedes.
- **Relates to**: [ADR-0058](0058-contract-codegen-from-code-types.md) (**produces** the JSON Schema this ADR
  **surfaces** — the direct dependency), [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (digest pinning —
  the inspected contract is exactly what was deployed), [ADR-0034](0034-end-user-journey-acceptance-e2e.md) (the push
  journey). The registry / AI-matching / admission-policy layer that *consumes* this metadata is **out of scope**
  (Project #4 backlog, toward V2).

## Context & Need

[ADR-0058](0058-contract-codegen-from-code-types.md) generates a function's input/output contract as **JSON Schema**
and ships it digest-pinned with the artifact — but, like ADR-0038 before it, the schema travels *inside the bundle*.
To read a function's contract you must fetch and parse the bundle blob (or run it). That blocks the whole reason for a
static contract: a registry, a deploy-time **admission policy**, or an **agent** browsing artifacts wants to read a
function's I/O shape **without pulling the (large) bundle layer and without executing untrusted code**.

The funcd artifact is already an **OCI artifact** (ADR-0031): a `oras.PackManifest` manifest (`artifactType:
application/vnd.funcd.function.artifact.v1`) with a single bundle-blob layer, and `PackManifest` exposes a manifest
**annotation** seam (today only a layer title is set). The fix is to attach the generated JSON Schema as **first-class
OCI metadata** — a small, dedicated, content-addressed **contract blob** plus a **manifest annotation** flagging it —
so any OCI client can read a function's contract straight from the manifest + one tiny blob, never touching the bundle.

**Purpose**: make a function's I/O contract **statically inspectable from its OCI artifact** — no bundle pull, no
execution — and add `funcdcli inspect` to surface it. This is the substrate the V2 registry/policy layer builds on.

## Scenarios

- **scenario: contract-embedded-on-push** — Given an artifact whose build produced a contract ([ADR-0058](0058-contract-codegen-from-code-types.md)),
  When `funcdcli push`, Then the manifest carries a **contract annotation** (`dev.funcd.contract.v1`) and a dedicated
  **contract blob layer** (`application/vnd.funcd.contract.v1+json`) holding the input/output JSON Schemas.
- **scenario: inspect-without-pull** — Given a pushed contracted artifact, When a client reads its contract (e.g.
  `funcdcli inspect <ref>`), Then it fetches the **manifest + the small contract blob only** — **never the bundle
  layer**, never executing code — and renders the input/output schemas.
- **scenario: no-contract-no-metadata** — Given an artifact with no declared types (no ADR-0058 contract), When
  pushed, Then **no** contract blob or annotation is added — the manifest is exactly the ADR-0031 shape (backward
  compatible).
- **scenario: bundle-selected-by-mediatype** — Given a contracted artifact now has two layers (bundle + contract),
  When the platform `Pull`s it, Then it selects the **bundle** layer by its media type (not by index) and materializes
  the correct bytes — the contract layer never breaks materialization.
- **scenario: contract-digest-pinned** — Given the manifest is fetched **by digest** (ADR-0031/0035) and the contract
  blob is content-addressed, When a tag is later moved, Then the inspected contract is still exactly the one that was
  deployed — no tag-swap can change a function's advertised contract.

## Scope

**In:** embedding the [ADR-0058](0058-contract-codegen-from-code-types.md) JSON Schema(s) as a dedicated, content-addressed
**contract blob layer** + a **manifest annotation** on push; selecting the bundle layer by media type on pull;
`funcdcli inspect` to read the contract **without pulling the bundle or running**; opt-in (only when a contract exists).

**Out:** **generating** the schema (that is [ADR-0058](0058-contract-codegen-from-code-types.md)). The **OCI referrers
artifact** path for cross-artifact registry discovery (noted as the V2 path, not built here). The registry index /
AI-matching / deploy-time admission policy that *consume* this metadata (Project #4, toward V2). Schema versioning /
evolution.

## Constraints & Decision drivers

- **No bundle pull, no execution** — the contract must be readable from the manifest + a *small* blob; the large
  bundle layer and the code stay untouched. (The core F30 driver.)
- **Reuse ADR-0031's seam** — `oras.PackManifest` already takes layers + manifest annotations; add to it, don't fork
  the artifact model. One artifact, one manifest.
- **Digest-pinned & tamper-evident** — the contract is content-addressed and the manifest is pulled by digest
  (ADR-0035), so the advertised contract == the deployed one.
- **Backward compatible** — a contract-less artifact is byte-for-byte the ADR-0031 shape; `Pull` of an old artifact is
  unchanged.
- **Standard OCI** — a generic OCI client (not just funcd) can read the annotation + blob; no funcd-only manifest tricks.

## Alternatives considered

- **Inline the schema as a manifest annotation string** — simplest to read (zero blob pull), but registries cap
  annotation size (commonly a few KB–tens of KB) and a non-trivial schema bloats every manifest list/response.
  Rejected as the carrier; the annotation instead **flags** the contract (presence + the blob digest) and the schema
  lives in a dedicated blob. Best of both: the manifest stays small, the contract is one tiny addressed fetch.
- **Keep the schema only inside the bundle (ADR-0058 status quo)** — no new OCI surface, but you must pull + parse the
  bundle (or run it) to read the contract — exactly the gap F30 exists to close. Rejected.
- **OCI referrers artifact (a separate manifest referring to the function artifact)** — the *right* mechanism for
  cross-artifact, registry-wide discovery ("find all functions whose input matches this output"), but it needs OCI 1.1
  referrers support and is the **registry/AI-matching** concern (Project #4, V2). For v1.1 — "read *this* artifact's
  own contract" — an in-manifest blob is simpler and works on the local OCI layout + any registry. Noted as the V2 path.
- **A separate sidecar file / platform schema field** — splits the contract from its digest-pinned artifact (drift,
  no single source of truth). Rejected — the contract must travel with + be addressed by the artifact (ADR-0031/0035).

## Decision

1. **Contract blob + annotation on push.** When the build produced a contract ([ADR-0058](0058-contract-codegen-from-code-types.md)),
   `artifact.Push` adds a second, content-addressed layer — media type **`application/vnd.funcd.contract.v1+json`** —
   holding `{ "input": <JSON Schema>?, "output": <JSON Schema>?, "dialect": "https://json-schema.org/draft/2020-12/schema" }`,
   and sets a manifest **annotation `dev.funcd.contract.v1`** to the contract blob's digest. No contract ⇒ neither is
   added (the ADR-0031 manifest, unchanged).

2. **Static inspection — manifest + contract blob only.** A reader fetches the manifest (by digest), reads
   `annotations["dev.funcd.contract.v1"]` (or finds the layer by media type), and fetches **just that small blob** —
   **never the bundle layer, never running code**. `funcdcli inspect <ref>` renders the input/output schemas from it.

3. **Bundle selected by media type on pull.** `artifact.Pull` selects the **bundle** layer by `bundleMediaType`
   (not `Layers[0]`), so the added contract layer never changes which bytes are materialized. (A focused refinement of
   ADR-0031's pull.)

4. **Digest-pinned.** The contract blob is content-addressed; the manifest is fetched by digest (ADR-0031/0035). The
   inspected contract is provably the deployed one; a moved tag cannot change it.

5. **Referrers deferred.** Cross-artifact, registry-wide discovery (the OCI referrers API) is the V2 registry's job
   (Project #4); v1.1 embeds the contract in the artifact's own manifest, which every target (local layout + registry)
   supports.

## Temporary workarounds

- **In-manifest blob, not referrers**: v1.1 reads *one artifact's own* contract; the registry-wide "match output→input
  across artifacts" needs the referrers API + an index. **Exit:** the V2 contract-registry ADR (Project #4) adds the
  referrers artifact + index, consuming the same `application/vnd.funcd.contract.v1+json` payload defined here.

## Contracts

### Media type & annotation (new OCI surface)

```
layer media type : application/vnd.funcd.contract.v1+json
   blob payload   : { "input": <JSON Schema>?, "output": <JSON Schema>?,
                      "dialect": "https://json-schema.org/draft/2020-12/schema" }
manifest annotation : dev.funcd.contract.v1 = <contract blob digest>   (absent ⇒ no contract)
```

### `internal/artifact` (refines ADR-0031)

```go
// Push gains the generated contract (nil ⇒ unchanged ADR-0031 artifact). When non-nil, Push adds
// the contract blob layer + the dev.funcd.contract.v1 annotation alongside the bundle layer.
func Push(ctx context.Context, ref, file string, contract []byte) (digest string, err error)

// Inspect fetches the manifest (by digest) + the contract blob ONLY — never the bundle layer,
// never executing code — and returns the raw contract JSON ({input?, output?, dialect}).
// fault.NotFound if the artifact carries no contract.
func Inspect(ctx context.Context, ref, digest string) (contract []byte, err error)

// Pull selects the bundle layer by bundleMediaType (not Layers[0]) so a contract layer never
// changes which bytes materialize (unchanged signature).
func Pull(ctx context.Context, ref, digest, dir string) (path string, err error)
```

### `funcdcli` (push + inspect)

```
funcdcli push <file> <ref> [--contract-input <schema.json>] [--contract-output <schema.json>]
    # each schema is gated against the funcd profile (contract.Check, ADR-0058/0060) BEFORE
    # packaging, then the two are assembled into the {input?, output?, dialect} contract blob passed
    # to artifact.Push. (Replaces the prior unlabeled repeatable --contract: the blob payload is
    # KEYED by input/output, so the two schemas must be labeled at the CLI.)

funcdcli inspect <ref>[@<digest>]
    # prints the input/output JSON Schemas; fetches manifest + contract blob only (never the bundle,
    # never running). A bare tag is resolved to its current manifest; the digest-pinning guarantee
    # (inspected contract == deployed contract) holds when a <digest> is supplied.
```

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| the generated contract JSON | [ADR-0058](0058-contract-codegen-from-code-types.md) build output | passed to `Push`; `nil` ⇒ no contract |
| `oras.PackManifest` (layers + annotations) | oras-go (ADR-0031) | the existing seam — add a layer + annotation |
| manifest by digest, contract blob | the OCI target | inspection fetches these two only, never the bundle |
| Exposes: `dev.funcd.contract.v1` + the contract blob | → registry / policy / agents (V2, Project #4) | the static-inspection substrate |

## Implementation plan

- **`internal/artifact/artifact.go`** — `Push` takes `contract []byte`; when non-nil, push a
  `application/vnd.funcd.contract.v1+json` blob layer + set the `dev.funcd.contract.v1` manifest annotation (via
  `PackManifestOptions{Layers, ManifestAnnotations}`). Change `Pull` to select the bundle layer by `bundleMediaType`.
  Add `Inspect` (fetch manifest by digest → read the annotation/contract layer → fetch only that blob).
- **`cmd/funcdcli`** — replace the unlabeled repeatable `--contract` with `--contract-input` / `--contract-output`
  (each optional, each gated via `contract.Check`); assemble `{input?, output?, dialect}` and pass it to
  `artifact.Push`. Add `funcdcli inspect` (manifest + contract blob only). A bare-tag inspect resolves the tag; the
  pinning guarantee is for digest refs.
- **Tests (non-gated, `just ci`)** — one per scenario: `contract-embedded-on-push` (annotation + contract layer
  present), `inspect-without-pull` (Inspect fetches manifest + contract blob, asserts the bundle blob is **not**
  fetched — a counting/fake target), `no-contract-no-metadata` (nil contract ⇒ ADR-0031 manifest unchanged),
  `bundle-selected-by-mediatype` (Pull returns the bundle with a contract layer present), `contract-digest-pinned`
  (Inspect by digest; a moved tag doesn't change the result). The push→registry→inspect e2e is the end-user-journey
  lane (ADR-0034).
- **Verify green** via the four sub-checks (`go build` · `go tool golangci-lint run` · `go test` · `go mod verify`).

**Definition of done:** a contracted push adds the contract blob + annotation; `Inspect` / `funcdcli inspect` reads
the contract fetching **only** the manifest + contract blob (never the bundle, never running); a contract-less push is
the unchanged ADR-0031 artifact; `Pull` selects the bundle by media type; all scenario tests pass; no new dependency
(reuses oras-go); `just ci` green.

## Review checklist

- [ ] Contract is a dedicated `application/vnd.funcd.contract.v1+json` blob layer + a `dev.funcd.contract.v1` manifest annotation (not inlined into an annotation string).
- [ ] `Inspect` fetches the **manifest + contract blob only** — a test asserts the **bundle blob is never fetched**.
- [ ] `Pull` selects the bundle layer by `bundleMediaType`, robust to the added contract layer.
- [ ] `push` takes labeled `--contract-input` / `--contract-output`, gates each, and assembles the `{input?, output?, dialect}` blob; a bare-tag inspect resolves the tag (the pinning guarantee is for digest refs).
- [ ] No-contract push is byte-compatible with the ADR-0031 manifest (backward compatible).
- [ ] Contract is content-addressed + read by manifest digest (tamper-evident; tag-swap-proof).
- [ ] `funcdcli inspect` renders the input/output schemas; no new dependency; no `any` in the new surface; no identity/path leak.

## Consequences

- A function's I/O contract is readable straight from its OCI artifact — no bundle pull, no execution — the substrate
  the V2 registry / admission-policy / AI-matching layer (Project #4) is built on.
- The artifact gains one small content-addressed layer + one annotation; a contract-less artifact is unchanged. Any
  generic OCI client can read the contract (standard media type + annotation), not just funcd.
- `Pull` now selects the bundle by media type — a small hardening that also tolerates future extra layers.
- The contract is tamper-evident and tag-swap-proof (content-addressed + digest-pinned), so a policy that trusts the
  advertised contract is trusting exactly what runs.

## Open questions

- **Annotation key / media-type versioning** — `dev.funcd.contract.v1` / `…contract.v1+json` carry a `v1`; the
  evolution rule (when the payload shape changes) is deferred to the V2 registry ADR. Default: additive fields only
  within `v1`.
- **Referrers vs in-manifest** at registry scale — v1.1 uses in-manifest; the V2 registry adds referrers for
  cross-artifact discovery (consuming the same payload). Answered there.

## References

- [ADR-0058](0058-contract-codegen-from-code-types.md) — generates the JSON Schema this ADR embeds.
- [ADR-0031](0031-oci-artifact-distribution-oras.md) — the OCI function artifact + `PackManifest` seam this refines.
- [ADR-0035](0035-artifact-digest-resolution-at-revision.md) — digest pinning (the inspected contract == the deployed one).
- [OCI image-spec — annotations & artifact manifests](https://github.com/opencontainers/image-spec/blob/main/annotations.md) · [oras-go](https://github.com/oras-project/oras-go) (the push/pull library, ADR-0031).
- [FEAT-0001/F30](../feat/0001-feat-v1.1.md) · [blueprint.md](../../blueprint.md) (artifact distribution).
