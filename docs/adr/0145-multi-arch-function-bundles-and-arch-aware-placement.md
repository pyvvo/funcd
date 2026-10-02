# ADR-0145: Multi-arch function bundles — an OCI image index per function, chosen per node, and arch-aware placement

- **Status**: Implemented (2026-10-02)
- **Date**: 2026-10-02 (revised the same day after the judge: ADR-0017's totality is superseded in part, not refined;
  the gate has a fixed position and literal outcome; pooled members are gated and excluded from their pool; the act
  workflow reaches Docker through its own socket and no bind mount; explicit parameters replace the internal options.
  Re-judged: the pool filter moves to `sameKeyFunctions`, which feeds both the cap and the manifest. **Accepted 2026-10-02** under `adr-batch`, with
  acceptance delegated by the decider: the re-judge found no Blocker, and its Major (the pool filter) is folded in. **Reviewing 2026-10-02** —
  implemented by `adr-impl`: `v1.OCIPlatform`, `push --platform`, `funcdctl index`, platform-aware pull and inspect, the
  scheduler's platform check, the step-2b gate and the pool filter, `WithNodePlatform`, and the act-only
  `bundle-multiarch` workflow (`just act-bundle` built and indexed both platforms). **Implemented 2026-10-02** — review gate
  pass, see docs/reviews/adr-0145-implementation-claude-opus-5-5.md)
- **Deciders**: green-0-rabbit
- **Tags**: artifact, oci, multi-arch, placement, scheduler, bundle, act
- **Realizes**: [FEAT-0001/F106](../feat/0001-feat-v1.1.md) (one function ref runs on amd64 and arm64 nodes)
- **Supersedes (in part)**: [ADR-0031](0031-oci-artifact-distribution-oras.md) — its deferral of multi-arch artifact
  manifests to V2 (the single-manifest artifact stays valid and unchanged); [ADR-0017](0017-scheduler-placement-port.md)
  Decision 2's "never fails to schedule" and the `scheduler-contract-holds` guarantee "a valid request yields a
  Placement" — a driver now refuses a request whose artifact platforms exclude its node. The port, the request/placement
  shape and the single-node driver otherwise stand.
- **Refines**: [ADR-0089](0089-python-function-dependency-bundling.md) (a bundle is built per platform),
  [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (the pinned digest may name an index),
  [ADR-0046](0046-pooling-placement-policy.md) (a pool admits only members that run on its node)
- **Relates to**: [ADR-0144](0144-bundling-in-the-language-toolchains.md) (`funcd-bundle --platform` builds the
  per-platform bundles; it must be implemented and pinned first), [ADR-0143](0143-redeploy-by-revision-switch.md)
  (a failing gate keeps a serving revision serving), [ADR-0059](0059-contract-as-oci-metadata.md) (the contract layer,
  identical across platforms), [ADR-0094](0094-workflow-engine-core.md) (the runtime annotation),
  [ADR-0123](0123-runtime-compiled-io-validators.md) (contract delivery, unchanged), [ADR-0026](0026-packaging-and-release.md)
  (the act-run local workflows)

## Context & Need

A Python bundle with a native dependency only runs on the CPU it was built for. A `duckdb` wheel ships
`_duckdb.cpython-314-aarch64-linux-gnu.so`; deployed to an amd64 node it fails at import with `ModuleNotFoundError:
No module named '_duckdb'` — reproduced during the design session. funcd runs on both architectures (the release
builds `funcdctl` for amd64 and arm64; Lima lanes run on the host's CPU), and a function ref names one artifact. Today
the author must push one ref per architecture and edit the `Function` for each cluster, and a mismatch shows up only as
a handler that fails to load.

**Purpose**: let one function ref carry one bundle per platform, so each node runs the bundle built for its CPU, and
make a function whose artifact has no bundle for a node fail before any worker starts, with a reason that says so. The
callers: `funcdctl push`/`funcdctl index` (authors and CI), the materializer (the node), the Function reconciler (via
the placement port).

## Scenarios

- **scenario: push-records-platform** — Given a bundle built for `linux/arm64`, When `funcdctl push <dir> <ref>
  --platform linux/arm64`, Then the manifest carries `dev.funcd.platform: linux/arm64`.
- **scenario: index-combines-platforms** — Given two such pushes for `linux/amd64` and `linux/arm64` in one repository,
  When `funcdctl index <ref> <amd64-ref> <arm64-ref>`, Then `<ref>` names an OCI image index listing both manifests
  with their platforms, and the command prints `<ref>@<digest>`.
- **scenario: index-rejects-inconsistent-sources** — Given sources that repeat a platform, lack the platform
  annotation, differ in contract, runtime or artifact kind, or live in another repository, When `funcdctl index` runs,
  Then it fails with `fault.Invalid` naming the offending source, and writes nothing.
- **scenario: pull-selects-node-platform** — Given an index, When an amd64 node and an arm64 node materialize the same
  pinned digest, Then each gets the bundle for its own platform, with the contract delivered as for a single manifest.
- **scenario: pull-no-matching-platform** — Given an index without the node's platform, When the node materializes it,
  Then it fails with `fault.NotFound` listing the platforms the index has.
- **scenario: single-manifest-unchanged** — Given an artifact pushed without `--platform`, Then every node pulls and runs
  it as today, and placement does not filter it.
- **scenario: placement-filters-by-platform** — Given a node whose platform is `linux/arm64`, When the scheduler gets a
  request whose platforms are `[linux/amd64]`, Then it returns an error matching `ErrNoMatchingPlatform`; with
  `[linux/amd64, linux/arm64]` it places on the node.
- **scenario: function-no-matching-platform** — Given a new Function whose image is an index without the node's
  platform, When it reconciles, Then it is `Failed` with reason `NoMatchingPlatform` and a message naming the
  artifact's platforms and the node's, and no worker is created.
- **scenario: redeploy-no-matching-platform-keeps-serving** — Given a Ready Function, When it is redeployed to an index
  without the node's platform, Then the serving revision keeps the calls, `Ready` stays True and `RevisionReady` is
  False with reason `NoMatchingPlatform`.
- **scenario: pooled-member-no-matching-platform** — Given two pooled Functions sharing a pool key, one of whose index
  lacks the node's platform, Then that one is `Failed` with `NoMatchingPlatform` and the pool serves the other.
- **scenario: inspect-index** — Given an index, When `funcdctl inspect <ref>@<digest>`, Then it prints the contract, read
  without a pull.
- **scenario: multiarch-workflow-builds-index** *(e2e, local act)* — Given `just act-bundle`, Then the
  `bundle-multiarch` workflow builds the catalog-quack bundle for `linux/amd64` and `linux/arm64`, pushes both and the
  index into an OCI layout, and each platform's `_duckdb` module and `duckdb-ext/` extensions name that platform.

## Scope

**In**: the platform annotation; the index command; platform-aware pull, materialize and inspect; the platform list on
the placement request and the single-node driver's check; the reconciler gate, solo and pooled; an act-only workflow
that builds a multi-arch bundle.

**Out**: building multi-arch bundles in a release workflow (the board card "move the multi-arch bundle build into the
release phase"); multi-node placement itself (V3, ADR-0017's deferred driver — it inherits the platform rule through
the contract suite); platform variants (`arm64/v8`) and non-Linux targets for artifacts; per-platform contracts; digest
source refs for `index` (tags only).

## Constraints & Decision drivers

- Standard OCI: any registry, `oras`, `crane` and `docker buildx imagetools` read an image index; oras-go v2.6.1
  (already a dependency, Apache-2.0) writes and reads one on both an OCI layout and a registry. No new dependency.
- The digest stays the authority (ADR-0035): a Revision pins one digest for every node.
- Backward compatibility: every artifact pushed today keeps working unchanged.
- Fail before start: a platform mismatch must not surface as a handler that cannot load.
- The release workflows stay fast for now (decider): the multi-arch build runs only under local `act`.

## Alternatives considered

- **One ref per architecture, chosen in the `Function`** (`image-amd64`, `image-arm64`, or a node selector).
  *Rejected*: the deploy manifest becomes cluster-specific and the author does the matching the registry can do.
- **One bundle holding every architecture's files** (both closures in one tar). *Rejected*: arch-tagged extension
  modules could coexist, but bundled shared libraries such as `duckdb.libs/` and pyarrow's `libarrow.so.*` carry no
  architecture in their names and collide; every node would also download every architecture.
- **Platform in the image config** (`os`/`architecture`, as container images do). *Rejected*: funcd artifacts have no
  image config (ADR-0031 packs an artifact manifest with an empty config); the index descriptor's `platform` is where
  OCI puts it, and a manifest annotation records it before the index exists.
- **Merge into the index as each architecture is pushed** (`push --platform` appends to whatever `<ref>` names).
  *Rejected*: two CI jobs pushing at once race on the tag, and a stale platform survives a rebuild. An explicit `index`
  step over named sources is atomic and repeatable (the shape of `docker manifest create`).
- **Match by trying to start the worker.** *Rejected*: that is today's failure, late and unclear.
- **Pool: schedule the pool with the intersection of its members' platforms.** *Rejected*: one mismatched member would
  take the whole pool down; excluding that member keeps its peers serving.

## Decision

1. **Per-platform push.** `funcdctl push <path> <ref> --platform <os>/<arch>` records the manifest annotation
   `dev.funcd.platform`; it accepts `linux/amd64` and `linux/arm64`, on every push path (single file, bundle, and the
   `funcdctl.yaml` path), and is refused with `--site`. Without the flag nothing changes.
2. **`funcdctl index <ref> <source-ref>...`** writes an OCI image index (`application/vnd.oci.image.index.v1+json`).
   - **Refs**: `<ref>` and every source use the push ref forms (`oci-layout://<dir>:<tag>` or `<registry>/<repo>:<tag>`),
     and every source names the same layout directory or repository as `<ref>`.
   - **Sources**: each resolves to a funcd function manifest carrying `dev.funcd.platform`.
   - **Consistency**: across sources the platforms are distinct, and the contract layer digest, the runtime annotation
     and the artifact kind (single file or bundle) are identical.
   - **Index content**: one descriptor per source with `platform.os`/`platform.architecture`, and the sources'
     `artifactType`. It carries no funcd annotation: readers follow its first descriptor (Decision 3).
   - **Failure**: any violation is `fault.Invalid` naming the source, before anything is written.
3. **Platform-aware pull.** `Pull` and the materializer take the node platform.
   - **Selection**: when the pinned digest names an index, they select the descriptor whose `os` and `architecture`
     equal the node's (a `variant` is ignored; `unknown/unknown` entries never match) and continue exactly as for a
     manifest. No match is `fault.NotFound` listing the index's platforms.
   - **Cache**: the materializer's cache directory is keyed by the pinned digest and the node platform.
   - **Inspect**: `Inspect`, `InspectContract` and `InspectRuntime` read an index through its first descriptor
     (identical in contract and runtime by Decision 2), and `InspectContract` returns the index digest, the pinned
     authority.
   - **`funcdctl pull --platform`**: defaults to `linux/<host arch>`, since function artifacts target Linux.
4. **Placement filters by platform.** `scheduler.Request` gains `Platforms []v1.OCIPlatform`: the platforms the
   artifact provides, empty meaning any.
   - **Driver**: a driver knows its node's platform and refuses a request whose non-empty list lacks it, with
     `fault.Invalid` wrapping `ErrNoMatchingPlatform`. The message is `artifact provides [<list>]; node <name> runs
     <platform>`. The single-node driver takes its node's platform as a constructor argument. The contract suite
     asserts the rule for every driver.
   - **Lookup**: the Function reconciler asks an optional `PlatformResolver` for the platforms of a digest. The OCI
     materializer implements it, caching per digest; an annotated manifest returns its one platform, an unannotated
     manifest none.
5. **The reconciler gate** (step 2b, right after `ensureRevision` pins the digest and before the shape gate).
   - **Check**: when a `PlatformResolver` is wired and the digest is non-empty, the reconciler resolves the platforms
     and calls `Schedule` for replica 0 with them. This covers solo and pooled Functions alike, since the gate runs
     before pooling's `assign`.
   - **No match**: `ErrNoMatchingPlatform` returns `gateFailed(ctx, fn, gateFailure{reason: "NoMatchingPlatform",
     message: err.Error(), readyMessage: err.Error(), phase: v1.PhaseFailed}, drainAfter)`. Under ADR-0143 a new
     Function goes `Failed` (`Ready` False); a Function with a serving revision keeps `Ready` True, and `RevisionReady`
     goes False with the reason.
   - **Resolver error**: an error from `Platforms` (a registry outage) is returned as a reconcile error and retried with
     backoff, not a status.
   - **Later calls**: every `Schedule` call for a solo replica carries the same list.
   - **Pools**: `sameKeyFunctions`, which feeds both pooling's cap (`assign`) and the pool manifest
     (`admittedMembers`), skips a member whose artifact lacks the node platform. The lookup uses the cached resolver on
     the digest the pool materializes (`m.Spec.ImageDigest`). So the member is neither counted, ranked nor
     materialized, and its own reconcile reports `NoMatchingPlatform`. The pool's own `Schedule` request carries no
     list, because every admitted member runs on the node.
6. **The node platform is configured once.** The composition root (`pkg/funcd`) passes one `v1.OCIPlatform` to the
   scheduler and the materializer: the option `WithNodePlatform`, defaulting to the daemon's `GOOS/GOARCH` (so a macOS
   process-mode dev daemon is `darwin/arm64` and refuses Linux-only indexes, which could not load there).
7. **Multi-arch build, local only.** `.github/workflows/bundle-multiarch.yml` (`workflow_dispatch` only) is run by
   `just act-bundle`, which passes `act` the Docker VM's socket (`--container-daemon-socket
   unix:///var/run/docker.sock`, overriding `.actrc` for this workflow only).
   - **Steps**: the job registers QEMU (`docker/setup-qemu-action`), builds `funcdctl` and installs uv, then copies
     catalog-quack out of the pinned funcd-python module (`scripts/example-copy.sh`).
   - **Per platform**: one `funcd-bundle --platform linux/amd64 --platform linux/arm64` run (hermetic, the foreign
     one under emulation) writes `dist/linux-amd64/catalog-quack` and `dist/linux-arm64/catalog-quack`; each is pushed
     with its `--platform` into one OCI layout.
   - **Finish**: it runs `funcdctl index`, checks each child's `_duckdb` file name and `duckdb-ext/` platform
     directory, and uploads the layout.
   - **Release workflows**: neither `release.yml` nor `release-please.yml` refers to it.
   - **Prerequisite**: `funcd-bundle` keeps every container step free of bind mounts (it copies in and out with
     `docker cp`, ADR-0144), so a job container driving the VM's daemon works.

## Temporary workarounds

- **The multi-arch build runs only under local `act`.** *Exit*: the board card "move the multi-arch bundle build into
  the release phase" — when releases publish function bundles, the job joins the release workflow.

## Contracts

### `api/types/v1alpha1`

```go
// OCIPlatform is an OCI platform "<os>/<architecture>", e.g. "linux/arm64" (ADR-0145).
type OCIPlatform string

const (
	PlatformLinuxAMD64 OCIPlatform = "linux/amd64"
	PlatformLinuxARM64 OCIPlatform = "linux/arm64"
)

// Validate checks the "<os>/<arch>" shape: two non-empty segments, no variant.
func (p OCIPlatform) Validate() error

// OS and Arch split a valid platform.
func (p OCIPlatform) OS() string
func (p OCIPlatform) Arch() string

// HostPlatform is the daemon's own GOOS/GOARCH.
func HostPlatform() OCIPlatform
```

### `internal/artifact`

```go
const PlatformAnnotation = "dev.funcd.platform"

// IsArtifactPlatform reports whether `funcdctl push --platform` accepts p (PlatformLinuxAMD64, PlatformLinuxARM64).
func IsArtifactPlatform(p v1.OCIPlatform) bool

// platform "" records no annotation (today's artifact).
func Push(ctx context.Context, ref, file string, contract []byte, runtime string, platform v1.OCIPlatform) (digest string, err error)
func PushBundle(ctx context.Context, ref, dir, entry, runtime string, platform v1.OCIPlatform) (digest string, err error)

// PushIndex writes an OCI image index over sources (Decision 2), tags it from ref and returns its digest.
func PushIndex(ctx context.Context, ref string, sources []string) (digest string, err error)

// node selects an index's descriptor; a manifest ignores it.
func Pull(ctx context.Context, ref, digest, dir string, node v1.OCIPlatform) (path string, err error)

// Platforms lists the platforms the artifact at digest provides: an index's, an annotated manifest's one,
// or nil for an unannotated manifest (runs anywhere).
func Platforms(ctx context.Context, ref, digest string) ([]v1.OCIPlatform, error)

func NewOrasMaterializer(artifactDir string, node v1.OCIPlatform) *OrasMaterializer

// Platforms implements function.PlatformResolver (cached per digest).
func (m *OrasMaterializer) Platforms(ctx context.Context, uri, digest string) ([]v1.OCIPlatform, error)
```

### `internal/scheduler`

```go
type Request struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Replica   int
	// Platforms the artifact provides (ADR-0145); empty means any.
	Platforms []v1.OCIPlatform
}

// ErrNoMatchingPlatform is wrapped (fault.Invalid) by Schedule when the node's platform is not in Request.Platforms.
var ErrNoMatchingPlatform = errors.New("no worker node matches the artifact's platforms")

// singlenode: node is the platform of the local worker node.
func New(local v1.ObjectName, node v1.OCIPlatform) (scheduler.Scheduler, error) // fault.Invalid on "" or an invalid node
```

`schedulercontract.Run(t, s, node v1.OCIPlatform)` adds: `Platforms: nil` places; `Platforms: [node]` places;
`Platforms: ["plan9/mips"]` returns an error matching `ErrNoMatchingPlatform` with `fault.KindOf` = `Invalid`.

### `internal/function`

```go
// PlatformResolver lists the platforms an artifact digest provides (ADR-0145); nil means any.
type PlatformResolver interface {
	Platforms(ctx context.Context, uri, digest string) ([]v1.OCIPlatform, error)
}

// Deps gains:
//	Platforms PlatformResolver // nil → no platform gate
```

Status on a gate failure: reason `NoMatchingPlatform`, message `artifact provides [<list>]; node <name> runs
<platform>`, set through `gateFailed` (ADR-0143 rules for `Ready`/`RevisionReady`).

### `funcdctl`

```
funcdctl push <path> <ref> [--platform linux/amd64|linux/arm64] [existing flags]   # not with --site
funcdctl index <ref> <source-ref>...                                               # prints <ref>@<digest>
funcdctl pull <ref> <digest> [dir] [--platform <os>/<arch>]                         # default linux/<host arch>
```

### `pkg/funcd`

```go
// WithNodePlatform sets the platform this node runs (scheduler + materializer). Default: v1.HostPlatform().
func WithNodePlatform(p v1.OCIPlatform) Option
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| per-platform manifests (`push --platform`) in one repository/layout | an OCI image index tagged `<ref>` |
| the node platform (configured once) | the selected platform's bundle, materialized; `NoMatchingPlatform` status |
| oras-go v2.6.1 (`content.NewDescriptorFromBytes`, `Target.Push`, `Target.Tag`, `oras.FetchBytes`) | — |
| `funcd-bundle --platform` (ADR-0144); Docker + QEMU through the VM socket (act workflow) | `.act-artifacts/` layout with the index |

## Implementation plan

**Files**
- `api/types/v1alpha1/platform.go` (+ `_test.go`): `OCIPlatform`, its constants and methods, `HostPlatform`.
- `internal/artifact/platform.go` (+ `_test.go`): the annotation, `IsArtifactPlatform`, `PushIndex`, `Platforms`, the
  index selection used by `Pull` and the inspect functions; `artifact.go`/`bundle.go`: the new parameters and the
  cache key. Every caller of `Push`, `PushBundle`, `Pull`, `NewOrasMaterializer` and `singlenode.New` passes the new argument (`""` /
  the node platform).
- `internal/scheduler/scheduler.go`, `singlenode/singlenode.go`, `schedulercontract/contract.go`: as in Contracts.
- `internal/function/materializer.go` (`PlatformResolver`), `function.go` (the step-2b gate, `Platforms` on the solo
  `Schedule` calls), `pool.go` (`sameKeyFunctions` skips a mismatched member).
- `pkg/funcd`: `WithNodePlatform`; pass it to `singlenode.New` and `NewOrasMaterializer`; wire the materializer as
  `Deps.Platforms`.
- `cmd/funcdctl/cli.go` + `manifest.go`: `push --platform` (all three push paths, refused with `--site`), `index`,
  `pull --platform`.
- `.github/workflows/bundle-multiarch.yml`, `justfile` `act-bundle` (its output goes to the ignored `.act-artifacts/`).
- `blueprint.md`: the artifact and placement notes. `docs/adr/0017-*.md`: the `Superseded in part by ADR-0145`
  back-link and `docs/adr/0031-*.md`: the `Superseded in part by ADR-0145` back-link (both at acceptance).

**go.mod**: none (after ADR-0144's pins).

**Test plan**
- `api/types/v1alpha1/platform_test.go`: `Validate` accepts `linux/arm64`, refuses `linux`, `/arm64`, `linux/arm64/v8`.
- `internal/artifact/platform_test.go` (temp OCI layouts): `push-records-platform`, `index-combines-platforms`,
  `index-rejects-inconsistent-sources` (cases: repeated platform, missing annotation, different contract, different
  runtime, different kind, different layout), `pull-selects-node-platform` (two node platforms, one pinned index
  digest, two cache dirs), `pull-no-matching-platform`, `single-manifest-unchanged`, `inspect-index`.
- `internal/scheduler/singlenode/singlenode_test.go`: `placement-filters-by-platform`, plus the extended contract suite.
- `internal/function`: `function-no-matching-platform` (a fake `PlatformResolver` and runtime: `Failed`,
  `NoMatchingPlatform`, zero `Create` calls), `redeploy-no-matching-platform-keeps-serving`,
  `pooled-member-no-matching-platform` (also with the pool at its cap), a resolver error requeues, and an index Function on a matching node reconciles
  Ready.
- `cmd/funcdctl`: `index`, `push --platform` (with and without `funcdctl.yaml`) and the `--site` refusal through the
  cobra commands on a temp layout.
- `multiarch-workflow-builds-index`: `just act-bundle` run locally; recorded in the review.

**Definition of done**: the four sub-checks green (`go build ./...` · `go tool golangci-lint run ./...` · `go test
./...` · `go mod verify`); every non-e2e scenario a named passing test; `just act-bundle` produces the two-platform
index; the existing lanes still pass (single-manifest path); no identity or path leak.

## Review checklist

- [ ] `push --platform` accepts only `IsArtifactPlatform` values on all three push paths and is refused with `--site`;
      without it the manifest carries no `dev.funcd.platform` and pulls as today.
- [ ] `PushIndex` validates every rule of Decision 2 before writing, and the index descriptors carry `platform`.
- [ ] `Pull` selects by the node platform (variant ignored), verifies digests as before, and reports the available
      platforms on a miss; the cache key includes the node platform.
- [ ] `Inspect*` read an index through its first descriptor without pulling a bundle.
- [ ] The step-2b gate runs before the shape gate and pooling, with the literal `gateFailure` of Decision 5; a resolver
      error requeues.
- [ ] Every solo `Schedule` call carries `Platforms`; `sameKeyFunctions` skips mismatched members (a full pool keeps
      its healthy members).
- [ ] The single-node driver honors `Platforms`; the contract suite covers it.
- [ ] One node-platform value feeds both the scheduler and the materializer.
- [ ] `bundle-multiarch.yml` is `workflow_dispatch` only and no release workflow references it.
- [ ] ADR-0017 and ADR-0031 carry the back-link.
- [ ] No new dependency; `api/fault` errors; no `any` in signatures; no package-level mutable state.
- [ ] Every non-e2e scenario has a named passing test; the act run is recorded.

## Consequences

- One ref per function serves both architectures, and a mismatch fails at reconcile with a clear reason.
- Artifacts stay standard OCI; registries and tools display the index.
- A multi-arch publish is two steps (per-platform push, then `index`); CI scripts it.
- Building the foreign architecture with `hermetic` runs under emulation and is slow; the uv fast path of ADR-0144
  avoids it for bundles without build-time code.
- `Push`, `PushBundle`, `Pull`, `NewOrasMaterializer` and `singlenode.New` gain a parameter: a one-time change at
  every call site.
- Risk: the node platform is a configured value. A misconfigured `WithNodePlatform` selects the wrong bundle; the
  default (the daemon's own `GOOS/GOARCH`) is right whenever funcd runs on the node it schedules.

## Open questions

- Platform variants (`linux/arm/v7`) — when a 32-bit ARM node is a target.
- Multi-node placement across nodes of different CPUs — the V3 multi-node driver, which inherits the contract suite.
- Signing an index (cosign) — the V2 supply-chain work ADR-0031 already defers.

## References

- OCI Image Index specification v1.1 (`application/vnd.oci.image.index.v1+json`, `platform` on descriptors).
- oras-go v2.6.1 (Apache-2.0): `oci.Store` indexes an image index on push; `remote.Repository` sends it to the
  manifests endpoint — checked in the module source by the judge, 2026-10-02.
- `docker manifest create` / `docker buildx imagetools create` — the two-step shape.
- nektos/act `--container-daemon-socket` (an empty `-` disables the socket in the job container, as `.actrc` sets).
- The `_duckdb` architecture mismatch reproduced during the design session (2026-10-01).
- [ADR-0017](0017-scheduler-placement-port.md), [ADR-0031](0031-oci-artifact-distribution-oras.md),
  [ADR-0035](0035-artifact-digest-resolution-at-revision.md), [ADR-0046](0046-pooling-placement-policy.md),
  [ADR-0089](0089-python-function-dependency-bundling.md), [ADR-0143](0143-redeploy-by-revision-switch.md),
  [ADR-0144](0144-bundling-in-the-language-toolchains.md).
