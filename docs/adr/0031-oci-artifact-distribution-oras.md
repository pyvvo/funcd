# ADR-0031: OCI artifact distribution via oras-go — `funcdcli` push + platform pull (P-V-A)

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** — review pass, all 4 scenarios green incl the node-gated
  seam test (OrasMaterializer pulls by digest from a local OCI layout + the real shim runs it to Ready);
  funcdcli push/pull/login/logout land; one new Apache-2.0 dependency (oras-go v2.6.1); blueprint synced.
  **Accepted 2026-06-15** after judge pass — no Blockers left open. The judge verified
  oras-go v2 is Apache-2.0 with the exact API used (`oci.Store`/`oci.New`, `remote.NewRepository`,
  `oras.Copy`/`Fetch`) and that the seam composition respects ADR-0030's frozen boundary. Folded the **Blocker**
  (B1: the oras driver must *implement* `internal/function.Materializer` — renamed `OrasMaterializer` + a
  compile-time `var _` assertion, a *driver* of the seam not a redefinition) and two **Majors**: M1 — this
  **supersedes the blueprint's "users never push to registries"** UX (the user explicitly chose funcdcli as the
  OCI client: `login`/`push`/`pull`/`logout`); blueprint synced on accept; M2 — the **`Digest` is the
  authority** (pull-by-digest, empty digest rejected, tag is a locator) preserving ADR-0020's "validated =
  shipped". Expanded per the user: funcdcli gains `pull`/`login`/`logout` + validate-on-push. Decision: OCI
  artifact distribution via oras-go; one new Apache-2.0 dependency.)
- **Superseded in part by**: [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) (2026-10-02) —
  the deferral of multi-arch artifact manifests: a function ref may name an OCI image index.
- **Deciders**: green-0-rabbit
- **Tags**: artifact, oci, oras, distribution, funcdcli, materializer, registry, F13
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (the **source-artifact deploy** half — how the JS
  bundle / Python wheel reaches the platform)
- **Relates to**: [ADR-0030](0030-function-execution-runtime-shim-node.md) (this implements its `Materializer`
  **seam** — the oras driver is the production artifact source the local-file driver stands in for),
  [ADR-0024](0024-funcdcli-and-sdk.md) (the `funcdcli` this adds a `push` verb to), [ADR-0020](0020-function-contract-lifecycle.md)
  (the `Function`/`ArtifactRef` whose `URI`/`Digest` this gives meaning), [ADR-0007](0007-blob-storage-layer-port.md)
  (blob — **not** the artifact store; OCI replaces a blob-as-artifact approach), [ADR-0026](0026-packaging-and-release.md)
  (the binary this links `oras-go` into)

## Context & Need

ADR-0020's `Function.spec.artifact` is an `ArtifactRef{URI, Digest}` — but **nothing produces or consumes it**:
no path uploads a bundle, and `ArtifactRef.URI` has no defined meaning. ADR-0030 (function execution) needs an
artifact to *materialize*, and explicitly left a `Materializer` **seam** with a `file://` local-file driver as
a stand-in, naming OCI as the production source. This ADR fills that: **how a source artifact is distributed**.

The decision is **OCI, via [`oras-go`](https://github.com/oras-project/oras-go)** (`oras.land/oras-go/v2`,
Apache-2.0) — the canonical library for pushing/pulling *arbitrary* OCI artifacts (not just runnable images).
It is the right fit on every axis:
- **Content-addressed by digest** — `ArtifactRef.Digest` *is* the OCI descriptor digest (integrity + caching free).
- **Pure-Go** — works for the **process driver** (P-V-1, no containerd) *and* the container path (P-V-2), with one client.
- **No registry server needed for dev** — oras-go targets a **local OCI layout directory** (`oci.Store`) as
  well as a remote registry (`remote.Repository`), same API — fitting the embed-first / single-binary / one-Linux-box rule.
- **`funcdcli` and `funcd` share the registry, not a wire format** — the CLI pushes, the platform pulls, the
  control plane just carries the ref (how Knative/Kubernetes do source/image distribution).

This resolves the judge's M1 on ADR-0030 (there was no artifact-upload story) and replaces any blob-as-artifact
store (which had no upload path and a `mem://`-cross-process problem). One topic at one altitude: *artifact
distribution*. It is **P-V-A**, co-designed with **P-V-1** (they meet at ADR-0030's `Materializer` interface).

## Scope

- **In**: add `oras.land/oras-go/v2`; a **`funcdcli` artifact client** — `push <file> <ref>` (validate +
  package the bundle as an OCI artifact → push → print `ref@digest`), `pull <ref> [<dir>]` (fetch + verify an
  artifact locally, for inspection/validation), `login <registry>` / `logout <registry>` (registry credentials
  via oras-go's credential store) — so the user packages/validates/pushes once and the platform pulls later;
  the platform-side **oras driver of ADR-0030's `internal/function.Materializer`** (pull an `ArtifactRef` →
  local path, verified by `Digest`); the **`ArtifactRef` convention** (`URI` = an OCI reference; `Digest` = the
  authoritative artifact descriptor digest); two **targets** behind one resolver — a **local OCI layout** (dev,
  default under the data dir) and a **remote registry** (real); round-trip + seam-composition tests.
- **Out**: a bundled/embedded **registry server** (use a local OCI layout or an external registry); **signing**
  (cosign) + provenance — V2; **multi-arch / multi-file** artifact manifests (V1 is one bundle blob; a Python
  wheel is one file too); a `funcdcli deploy` push+apply convenience (a later thin wrapper); the **curated base
  images** (P-V-2); the **shim/execution** (P-V-1); building a per-revision base+layer **function image** (P-V-2).

## Constraints & Decision drivers

- **Implement ADR-0030's seam, don't redefine it** — the oras driver satisfies `Materializer.Materialize(ctx,
  fn) (localPath, error)`; P-V-1's local-file driver remains the test/dev stand-in. No new platform contract.
- **Embed-first / single-binary** — oras-go is a pure-Go library linked into `funcd` + `funcdcli` (no
  supervised registry process); the **local OCI layout** is the zero-infra dev store; a registry is config.
- **funcdcli stays kubectl-style + thin** (ADR-0024) — `push` is a new verb over the same shell; it talks to a
  registry/layout (not the control plane) for bytes, and the user references the result in the manifest.
- **Apache-2.0/MIT deps only** — `oras-go/v2` is Apache-2.0 ✓ (+ its OCI transitive deps).
- **ADR-0002** — typed surface, `api/fault`, ctx-first, no globals, no `any`.

## Scenarios

- **scenario: cli-pushes-artifact** — *Given* a local JS bundle, *when* `funcdcli push bundle.js <ref>` runs
  against a target (a local OCI layout), *then* it stores the bundle as an OCI artifact and prints the
  `<ref>@sha256:<digest>` the user puts in `Function.spec.artifact`.
- **scenario: platform-pulls-artifact** — *Given* an `ArtifactRef` whose `URI`/`Digest` name a pushed
  artifact, *when* the oras `Materializer` runs, *then* it pulls the artifact to a local path whose bytes
  match what was pushed, verifying the digest; a digest mismatch / missing ref → a typed `fault`.
- **scenario: push-pull-roundtrips** — *Given* a bundle pushed to a **local OCI layout**, *when* `funcdcli
  pull <ref> <dir>` fetches it, *then* the bytes written + the verified digest are identical to what was
  pushed — the user can validate locally, and the dev path needs **no registry server**.
- **scenario: materializer-satisfies-adr0030-seam** — *Given* ADR-0030's `Materializer` interface, *when* the
  oras driver is wired into the Function reconciler in place of the local-file driver, *then* the reconciler
  materializes a Function's artifact and the shim runs it (the seam composes — proven by reusing ADR-0030's
  reconciler test against the oras driver pointed at a local layout).

## Decision

### 1. The artifact is an OCI artifact (one blob, digest-addressed)
A function artifact is stored as an **OCI artifact**: a manifest with `artifactType:
application/vnd.funcd.function.artifact.v1` + a single blob layer (the bundle/wheel,
`application/vnd.funcd.function.bundle`). No runnable-image semantics — it's content. `ArtifactRef.Digest` is
the manifest (or blob) descriptor digest; `ArtifactRef.URI` is the OCI reference that locates it.

### 2. `ArtifactRef.URI` convention + the two targets
`URI` is an OCI reference resolved by a single `internal/artifact` helper to one of:
- **local OCI layout** — `oci-layout://<dir>` (or a bare path) → oras-go `oci.Store(dir)`; the **dev default**
  is a layout under the platform/CLI data dir. Zero infra; works on one box (funcdcli + funcd co-located).
- **remote registry** — `<registry>/<repo>:<tag>` (or `@<digest>`) → oras-go `remote.Repository`; the **real**
  path (any OCI registry — GHCR/ECR/zot/Harbor), and the multi-node story.
The same `oras.Copy`/`oras.Fetch` API drives both; the ref scheme picks the target.

### 3. The `funcdcli` artifact client (the producer)
`funcdcli` gains an artifact verb set over ADR-0024's thin-shell `run()` + `flag` dispatch (each talks to a
registry/layout via oras-go, **not** the control plane — bytes go to the store, the API carries only the ref):
- **`push <file> <ref>`** — *validate* the bundle (a light pre-flight: non-empty, packs into the §1 artifact;
  the authoritative shape-gate is the shim, ADR-0030 — deep JS/Python export-analysis is the deferred runtime
  pre-flight), *package* it as the §1 OCI artifact, *push* to `<ref>`'s target, print `<ref>@<digest>`.
- **`pull <ref> [<dir>]`** — fetch the artifact, *verify its digest*, write it to `<dir>` (default a temp dir)
  for local inspection/validation — the same pull the platform does, in the user's hands.
- **`login <registry> [-u user]`** / **`logout <registry>`** — store / remove registry credentials via
  oras-go's credential store (so `push`/`pull` reach private registries). The dev **local OCI layout** needs no login.

The user `push`es once, sets `Function.spec.artifact.{uri,digest}` from the printed `ref@digest`, and
`funcdcli apply`s the manifest (push and apply stay separate verbs — a `deploy` push+apply wrapper is a later
convenience). `cmd/funcdcli` may import `internal/artifact` (not under the `e2e-boundary`).

> **Blueprint reconciliation (newest-accepted-wins).** The blueprint currently says *"users never … push to
> registries; the platform uses its OCI registry internally."* This ADR **supersedes that UX decision**: in
> V1 the user is the artifact client (`funcdcli login/push/pull`), distributing via OCI; the platform pulls.
> The blueprint's "platform layers the artifact onto the curated base at deploy" stays the *execution
> packaging* model (P-V-2 builds the per-revision base+layer image *from* the pushed artifact) — but the
> artifact gets *to* the platform via this push/pull. **The blueprint is synced to this on acceptance.**

### 4. The platform oras driver of ADR-0030's `Materializer` (the consumer)
`internal/artifact` provides an **`OrasMaterializer` that implements `internal/function.Materializer`** (the
ADR-0030 seam — `var _ function.Materializer = (*artifact.OrasMaterializer)(nil)`; it is a *new driver of* that
frozen interface, not a redefinition). Given a Function, it resolves `spec.artifact.uri` → target and **pulls
by `spec.artifact.digest`**, which is the **authority**: the `URI`'s tag (if any) is only a locator, the
artifact is fetched + content-verified against the digest, and an **empty `Digest` is rejected**
(`fault.Invalid`) — so a mutable tag can never swap what was deployed (preserving ADR-0020's "validated =
shipped" pinning). It writes to a path under the platform's artifact dir (cached per digest — immutable) and
returns it. A digest mismatch / missing ref / pull error → typed `fault.Invalid`/`NotFound`. `pkg/funcd` wires
the `OrasMaterializer` (over the configured target) into the reconciler's `Materializer` dep; the P-V-1
local-file driver remains the no-dep test stand-in.

## Contracts

### `internal/artifact`
```go
// resolveTarget maps an ArtifactRef.URI to an oras Target (oci.Store local layout | remote.Repository).
func Push(ctx context.Context, ref, file string) (digest string, err error) // package + push; returns the descriptor digest
func Pull(ctx context.Context, ref, digest, dir string) (path string, err error) // fetch + verify by digest
func Login(ctx context.Context, registry, user, pass string) error
func Logout(ctx context.Context, registry string) error

// OrasMaterializer is a DRIVER of ADR-0030's internal/function.Materializer (verified at compile time).
type OrasMaterializer struct{ /* artifactDir; target resolver */ }
func NewOrasMaterializer(artifactDir string) *OrasMaterializer
func (m *OrasMaterializer) Materialize(ctx context.Context, fn *v1.Function) (localPath string, err error)
var _ function.Materializer = (*OrasMaterializer)(nil) // implements the ADR-0030 seam
```
### `cmd/funcdcli` (verbs over ADR-0024's run()/flag shell)
```
funcdcli push  <file> <ref>      → prints "<ref>@sha256:<digest>"
funcdcli pull  <ref> [<dir>]     → fetch + verify by digest into <dir>
funcdcli login <registry> [-u user] [-p pass]   ·   funcdcli logout <registry>
```
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Adds (lib) | `oras.land/oras-go/v2` (Apache-2.0) + its OCI transitive deps | the one new dependency |
| Consumes | `api/types` (`ArtifactRef`), `api/fault`, stdlib | linked into `funcd` + `funcdcli` |
| Exposes | `funcdcli push`; the oras `Materializer` (ADR-0030's seam); the `ArtifactRef` convention | a local OCI layout (dev) or a registry (prod) |

## Implementation plan

1. **`go get oras.land/oras-go/v2`** (record the version).
2. **`internal/artifact/artifact.go`** — the target resolver (`oci-layout://`/path → `oci.Store`;
   `registry/repo[:tag|@digest]` → `remote.NewRepository`), `Push`/`Pull`/`Login`/`Logout`, and the
   **`OrasMaterializer`** (`Materialize` = resolve → pull **by digest** → verify → local path, cached per
   digest) with `var _ function.Materializer = (*OrasMaterializer)(nil)`.
3. **`cmd/funcdcli/cli.go`** — `push`/`pull`/`login`/`logout` verbs over `internal/artifact` (funcdcli may
   import `internal/artifact` — not under the `e2e-boundary`).
4. **`pkg/funcd`** — wire the `OrasMaterializer` (over a configured artifact dir / local layout) into the
   Function reconciler's `Materializer` dep; `InMemory()` uses a temp local layout.
5. **`blueprint.md`** — sync the "users never push to registries" line to the funcdcli-as-OCI-client model
   (the user pushes; the platform pulls; P-V-2 still layers the artifact onto the curated base at deploy).
6. **Tests** — `internal/artifact`: `cli-pushes-artifact` / `push-pull-roundtrips` (push to a temp local layout
   → pull back → bytes+digest match) + `platform-pulls-artifact` (digest verify + mismatch/empty → fault);
   `materializer-satisfies-adr0030-seam` (the `OrasMaterializer` drives ADR-0030's reconciler to Ready against
   a local layout). All pure-Go (a local OCI layout needs no registry, no node).
7. **Definition of done**: `just ci` green; `funcdcli push/pull/login/logout` work against a local layout; the
   platform pulls + digest-verifies to a local path; `OrasMaterializer` satisfies ADR-0030's `Materializer`;
   one new Apache-2.0 dependency (`oras-go`); blueprint synced; no identity/path leak.

## Review checklist

- [ ] **Push + pull round-trip** (`cli-pushes-artifact`, `push-pull-roundtrips`): `funcdcli push` stores the
      bundle as an OCI artifact; pulling it back yields identical bytes + digest — **with no registry server**
      (a local OCI layout).
- [ ] **Digest-verified pull** (`platform-pulls-artifact`): the Materializer verifies `ArtifactRef.Digest`; a
      mismatch / missing ref → typed `fault`.
- [ ] **Composes with ADR-0030** (`materializer-satisfies-adr0030-seam`): the oras driver satisfies the
      `Materializer` interface and drives the reconciler to Ready (the seam is real, not just asserted).
- [ ] `funcdcli push` is a thin verb over ADR-0024's shell; the `ArtifactRef` convention (URI/Digest) is
      documented; **exactly one new Apache-2.0 dependency** (`oras-go`); ADR-0002 conventions; no leak; every
      Scenario a named passing test.

## Consequences

- (+) **The artifact story is complete + OCI-native**: `funcdcli push` → registry/layout → the platform pulls
      by digest → the shim runs it. Content-addressed, cacheable, signable later — and **dev needs no registry**.
- (+) **One pure-Go client for both sandbox drivers** (process now, containerd later) — the same oras pull
      feeds P-V-1 and P-V-2; no blob-as-artifact-store, no upload-to-blob path.
- (+) **Implements ADR-0030's seam** — P-V-1 + P-V-A compose at the `Materializer` interface; the local-file
      driver stays as a no-dep test stand-in.
- (−) **One new dependency** (`oras-go` + OCI transitive deps) — justified: it is *the* OCI-artifact library,
      Apache-2.0, and replaces hand-rolling registry/distribution + a blob upload path.
- (−) **No embedded registry** — multi-host needs an external registry; single-host dev uses a local layout.
      An embedded registry (zot-as-library) is a possible V2 add behind the same `Ref` resolver.
- (note) **Roadmap**: P-V-A is wave-1 (deps ADR-0024 + ADR-0020), parallel to P-V-1; P-V-2 pulls via this driver.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| `push` and `apply` are separate `funcdcli` verbs (no auto-push on apply) | keeps each verb thin + composable; auto-push couples apply to a registry | a `funcdcli deploy` (push+apply) convenience later |
| dev uses a **local OCI layout** (shared filesystem, single host) | no embedded registry in V1; multi-host needs a real registry | an embedded registry (zot-as-library) behind the `Ref` resolver — V2 |

## Alternatives considered

- **Blob service (`gocloud.dev/blob`) as the artifact store** — rejected (the judge's M1 on ADR-0030): nothing
  uploads to blob, `mem://` isn't cross-process, and blob has no digest-addressing/caching/distribution. OCI is
  purpose-built for content distribution and matches `ArtifactRef.Digest`.
- **Push artifact bytes through the control-plane API** (the platform stores them) — rejected: it makes the API
  server an artifact registry (large bodies, storage, GC) and reinvents OCI distribution; the registry/layout
  is the right store, the control plane carries only the ref.
- **A per-revision base+layer function *image* (build at apply)** — deferred to P-V-2: that's the *execution*
  packaging (curated base + artifact layer), which needs the base images; P-V-A is the artifact *distribution*
  primitive both the process driver and that image build consume.
- **Hand-roll the OCI distribution client** — rejected: oras-go is the maintained, canonical implementation
  (the OCI project's own); hand-rolling registry auth/manifest/blob upload is error-prone for zero gain.
- **Bundle an embedded registry (zot) now** — rejected for V1: a local OCI layout gives zero-infra dev without
  a registry process; an embedded registry is a clean V2 add behind the same `Ref` resolver if wanted.

## Open questions

| Question | Where it gets answered |
|---|---|
| Artifact signing / provenance (cosign) | V2 (the OCI artifact is sign-ready) |
| Artifact GC / retention in the local layout + registry | a follow-up (cache eviction by digest/age) |
| An embedded registry (zot-as-library) for multi-host without external infra | V2, behind the `Ref` resolver |
| `funcdcli deploy` (push + apply in one) | a later convenience wrapper over `push`+`apply` |

## References

- [`oras-project/oras-go`](https://github.com/oras-project/oras-go) (Apache-2.0) — push/pull arbitrary OCI
  artifacts; `oci.Store` (local layout) + `remote.Repository` (registry) targets.
- [ADR-0030](0030-function-execution-runtime-shim-node.md) — the `Materializer` seam this implements.
- [ADR-0024](0024-funcdcli-and-sdk.md) — the `funcdcli` shell this adds `push` to.
- [blueprint.md](../../blueprint.md) — "source-artifact deploys (JS bundle, Python wheel)".
