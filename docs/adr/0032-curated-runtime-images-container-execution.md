# ADR-0032: Curated runtime images + container execution of the shim (P-V-2)

> **Superseded in part by [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)** — image
> *distribution* only (registry-pull + per-deploy bind-mount of the curated base → an **embedded** image imported into a
> funcd-managed containerd). This ADR's container **execution** model (read-only bind-mount of the *artifact*, fixed-port
> netns addressing) is **retained**.

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** — review pass: SandboxSpec.Mounts + the containerd driver
  (mounts, Instance.Port on List+Status, real task state), the shim FUNCD_PORT bind, the container-mode
  reconciler, WithContainerExecution, and the curated nodejs20 Dockerfile + recipe all land; cross-platform +
  node-gated tests green (spec-mounts-artifact, process-driver-unaffected, container-readiness-gates,
  curated-image-builds, shim-fixed-port-bind); the two Linux-only e2e (container-shim-serves,
  bind-mount-is-readonly) deferred to the integration lane (recorded); no new Go dependency. **Accepted
  2026-06-15** after judge pass — no Blockers. Folded 3 Majors + 3 Minors:
  **M2** (the load-bearing one) — the containerd driver must persist the fixed `FUNCD_PORT` and surface
  `Instance.Port` on **both `List` and `Status`** (the reconciler reads instances via `List`, and both
  `readyReplicas`/`upstreamFor` gate on `Port>0`) + correct `List`'s hardcoded `StateRunning`, else container
  mode never reaches Ready; **M1** — the shim binds `0.0.0.0:$FUNCD_PORT` (it cannot know its netns IP); the
  driver supplies `Instance.IP` (existing `extractIP`) + `Instance.Port` (the fixed port, new); **M3** — F12 row
  re-linked to this ADR for the curated-images/container-execution half; **m1** `EndpointMode` is orthogonal to
  the `Materializer!=nil` gate; **m2** container mode leaves `Command` empty (image entrypoint) + relaxes the
  ShimCommand guard; **m3** `ImageFor` keyed by `fn.Spec.Runtime` (V1: `nodejs20`). Decision: curated image +
  read-only bind-mount artifact delivery + container-mode fixed-port addressing. No new Go dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, crun, curated-image, shim, sandbox, mounts, F12
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — the **curated language runtimes**
  running the platform shim in a **containerd/crun** sandbox, the production half of F12 that the process
  driver stands in for)
- **Relates to / refines**:
  [ADR-0011](0011-runtime-sandbox-port.md) — **refines** its containerd driver (adds bind `Mounts` + the
  container-mode endpoint) and its `SandboxSpec`;
  [ADR-0030](0030-function-execution-runtime-shim-node.md) — runs *its* shim contract inside the container
  (refines the shim's bind model: fixed netns port instead of the process driver's loopback+portfile) and the
  reconciler's container-mode wiring;
  [ADR-0031](0031-oci-artifact-distribution-oras.md) — the pulled artifact is delivered into the container;
  [ADR-0003](0003-resource-model-and-runtimeclass.md) — RuntimeClass → curated image mapping.

## Context & Need

ADR-0030 made functions *execute* — but only on the **process driver** (the cross-platform dev/CI lane):
`node` + the shim + the artifact all live on the host filesystem, and the shim binds `127.0.0.1:0` with a
`FUNCD_PORTFILE` handshake. Production runs functions in a **containerd/crun sandbox** (ADR-0011: a runc/crun
container in a per-sandbox netns with default-deny lateral). Three things are missing to run the *same shim
contract* there, and this ADR (P-V-2) supplies them:

1. **A curated runtime image** — the container needs `node` **and** the funcd shim inside it. There is no
   image today; `SandboxSpec.Image` is just passed through.
2. **The artifact inside the container** — ADR-0031 pulls the bundle to a *host* path; the container can't see
   it. The host path must be delivered into the sandbox.
3. **Container-mode addressing** — inside its own netns a sandbox has a private IP (ADR-0011 already extracts
   it), so the loopback+portfile handshake is neither needed nor usable; the shim should bind a **fixed port**
   on the netns IP and the reconciler should address `netnsIP:port`.

This is the production execution path for F12; it composes P-V-1 (the shim) + P-V-A (the artifact) onto the
ADR-0011 sandbox. It is **Linux-only** (containerd + crun + netns), so its end-to-end scenarios run on the
Linux lane and are **deferred on non-Linux** exactly like ADR-0011's existing `containerd_linux_test.go`.

## Scope

- **In**: a **curated runtime image** per runtime class (V1: `nodejs20`) — a Dockerfile + a `just` build recipe
  producing an image that carries `node` + the ADR-0030 shim at a fixed path and runs it as entrypoint;
  **artifact delivery into the container** via a read-only **bind mount** (`SandboxSpec.Mounts`), wiring the
  ADR-0031-materialized host path to a known container path that `FUNCD_ARTIFACT` names; **container-mode
  addressing** — the shim binds a fixed port (`FUNCD_PORT`) on `0.0.0.0` inside the netns, the containerd
  driver surfaces `Instance.IP:port`, and the reconciler picks the mode from the configured driver; the
  reconciler **container-execution wiring** (RuntimeClass → curated image; mount the artifact; fixed-port
  readiness); contract/unit tests cross-platform + Linux-gated e2e.
- **Out**: **building a per-revision base+layer image at deploy** (V1 bind-mounts the artifact onto the curated
  base instead — same logical "base + artifact", no per-deploy image build; the baked-layer build is a later
  optimization); the **Python** runtime image + shim (P-V-3, same contract); **WASM**/microVM runtime classes
  (V3); registry/layout plumbing for the curated images beyond the build recipe (operators publish them);
  multi-arch images (V1 is one arch).

## Constraints & Decision drivers

- **One shim contract, two transports** — the curated container runs the *exact* ADR-0030 HTTP contract; only
  the bind address differs (fixed netns port vs loopback+portfile). No second execution model.
- **Reuse ADR-0011's driver** — this adds `Mounts` + the container endpoint to the existing containerd driver;
  it does not write a new driver. The process driver stays the dev/CI lane unchanged.
- **Embed-first / curated, not user images** (blueprint) — functions deploy from source artifacts onto a
  *curated* base the platform controls; users never write Dockerfiles.
- **Linux-only e2e is deferred honestly** (ADR-0025 test sequencing) — the same build-tag split ADR-0011 uses;
  the cross-platform spec-construction logic is unit-tested everywhere.
- **ADR-0002** — typed surface (`Mount` is a typed struct, no `any`), `api/fault`, ctx-first, no globals.
- **Apache-2.0/MIT deps only** — **no new Go dependency** (uses containerd's existing `oci.WithMounts`).

## Scenarios

- **scenario: curated-image-builds** — *Given* the nodejs curated Dockerfile + `just build-runtime-images`,
  *when* the recipe runs, *then* it produces an image carrying `node` + the shim at the fixed path with the
  shim as entrypoint (asserted by an image-presence/structure check; the build itself is operator-run).
- **scenario: spec-mounts-artifact** *(cross-platform unit)* — *Given* a Function in container mode, *when* the
  reconciler builds the `SandboxSpec`, *then* `Image` is the curated image for the runtime class, the
  materialized artifact host path is a **read-only** `Mount` at the known container path, and `FUNCD_ARTIFACT`
  + `FUNCD_PORT` are set in `Env`.
- **scenario: container-shim-serves** *(Linux-gated e2e)* — *Given* the curated image + a bundle, *when* the
  containerd driver runs the sandbox, *then* the shim binds `FUNCD_PORT` on the netns IP, the reconciler polls
  `netnsIP:FUNCD_PORT/health/readiness` to Ready, and the handler serves over HTTP.
- **scenario: bind-mount-is-readonly** *(Linux-gated e2e)* — *Given* a running container sandbox, *then* the
  artifact mount is present and read-only (the function cannot mutate its own artifact).
- **scenario: process-driver-unaffected** *(cross-platform)* — *Given* the process driver (no container mode),
  *then* the P-V-1 loopback+portfile path is unchanged (Mounts empty, FUNCD_PORT unset → bind 127.0.0.1:0).

## Decision

### 1. Curated runtime image
A curated base image per runtime class lives under `images/runtime/<class>/Dockerfile` and is built by a `just
build-runtime-images` recipe. V1 ships `nodejs20`: `FROM node:20-bookworm-slim`, copies `shim/nodejs/shim.mjs`
to `/opt/funcd/shim.mjs`, and sets `ENTRYPOINT ["node","/opt/funcd/shim.mjs"]`. The image carries the runtime
+ the shim; the **artifact is not baked in** (it is mounted, §2). Operators publish the image to their
registry/layout (ADR-0031); the platform references it by the RuntimeClass mapping (ADR-0003).

### 2. Artifact delivery — a read-only bind mount
`SandboxSpec` gains `Mounts []Mount{Source, Target, ReadOnly}`. In container mode the reconciler materializes
the artifact (ADR-0031 → a host path) and adds a read-only `Mount{Source: <host path dir>, Target:
"/var/funcd/artifact", ReadOnly: true}`; `FUNCD_ARTIFACT` is set to the in-container path
(`/var/funcd/artifact/<file>`). The containerd driver maps `Mounts` to `oci.WithMounts` (a `bind` mount,
`ro`). This realizes the blueprint's "artifact onto the curated base at deploy" **logically** (base image +
artifact mount) without a per-deploy image build — which is deferred as an optimization.

### 3. Container-mode addressing — fixed netns port
The shim refines its bind: if `FUNCD_PORT` is set it binds **`0.0.0.0:$FUNCD_PORT`** — it binds *all*
interfaces inside its own netns because it **cannot know its own netns IP** (only the port it was given) — and
skips the portfile; otherwise it keeps the P-V-1 behavior (`127.0.0.1:0` + `FUNCD_PORTFILE`). Addressing is
then split cleanly: the containerd driver supplies **`Instance.IP` from its existing `extractIP`** (ADR-0011 —
the netns IP, routable from the host over the CNI veth/bridge, the same upstream the gateway/activator already
proxy to) and **`Instance.Port` from the fixed `FUNCD_PORT`** it set in the spec (a *new* surfacing — ADR-0011
extracts no port). The reconciler is told its mode (`EndpointMode`: `EndpointLoopback` for the process driver,
`EndpointNetnsFixedPort` for containerd); in container mode it sets `FUNCD_PORT` (a fixed constant, `8080`) in
`Env` and polls `IP:8080/health/readiness`. The readiness/route logic (ADR-0030) is otherwise identical.

### 4. Reconciler container-execution wiring
The Function reconciler, when configured for container mode, resolves the curated `Image` via `ImageFor(fn.Spec.
Runtime)` (keyed by the per-function curated-runtime id — V1 ships exactly the `nodejs20 → <curated image>`
mapping), leaves **`Command` empty so the image ENTRYPOINT (the shim) runs** (the containerd `ociOpts` only
overrides args when `Command` is non-empty), mounts the artifact (§2), and sets `FUNCD_PORT` (§3). `EndpointMode`
is **orthogonal to the existing `Materializer != nil` gate**: the reconciler is in the shim path whenever a
Materializer is set (unchanged), and `EndpointMode` selects only *how that path is addressed* — process
(`FUNCD_PORTFILE`, `Command = shimCommand`, loopback poll) vs container (`FUNCD_PORT`, empty `Command`,
`IP:8080` poll). Because container mode runs the shim as the image entrypoint, `NewReconciler`'s
"ShimCommand-required-when-Materializer-set" guard is **relaxed for `EndpointNetnsFixedPort`** (the shim command
lives in the image, not the spec). Process mode is unchanged (P-V-1). Mode is a construction-time choice in
`pkg/funcd` (which driver + which addressing), not per-Function.

## Contracts

### `internal/runtime` (refines ADR-0011 `SandboxSpec` + `Instance`)
```go
// Mount is a host→container bind (container drivers only; ignored by the process driver).
type Mount struct {
    Source   string // host path
    Target   string // container path
    ReadOnly bool
}
// SandboxSpec gains:
//   Mounts []Mount // bind mounts (e.g. the read-only artifact); empty for the process driver
```
### `internal/runtime/containerd` (refines the Linux driver)
`ociOpts` appends `oci.WithMounts(<bind, ro/rw from spec.Mounts>)`. The driver persists the fixed `FUNCD_PORT`
in its per-sandbox bookkeeping at `Create`, and returns it as **`Instance.Port` on BOTH `List` and `Status`**
(not `Status` alone) — because the reconciler reads instances via `runtime.List` and **both `readyReplicas` and
`upstreamFor` gate on `in.Port > 0`**; surfacing the port on `Status` only would leave container-mode functions
never Ready and unrouted. `List` must also return each sandbox's **real state** (today it hardcodes
`StateRunning`) so a crashed/exited container is not counted Running — otherwise the ADR-0030 `shapeFailed`
detection never fires for the containerd driver. No new dependency.

### `shim/nodejs/shim.mjs` (refines the ADR-0030 shim bind)
```
if FUNCD_PORT set → server.listen(FUNCD_PORT, '0.0.0.0')   // container mode (netns IP)
else             → server.listen(0, '127.0.0.1') + write FUNCD_PORTFILE   // process mode (P-V-1)
```
### `internal/function` (container-mode reconciler)
```go
// EndpointMode selects how a sandbox is ADDRESSED (set at construction from the driver). It is
// orthogonal to the Materializer-presence gate that selects the shim path — it only changes how
// FUNCD_PORT/portfile env is set and how readiness is polled, within the existing shim path.
type EndpointMode int
const ( EndpointLoopback EndpointMode = iota; EndpointNetnsFixedPort )
// Deps gains:
//   EndpointMode EndpointMode                  // default EndpointLoopback (process driver)
//   ImageFor     func(runtime string) string   // fn.Spec.Runtime → curated image ref; V1: nodejs20
// NewReconciler relaxes the "ShimCommand required when Materializer set" guard for
// EndpointNetnsFixedPort (the shim is the image ENTRYPOINT, so Command stays empty).
```
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Adds (lib) | none | uses containerd's existing `oci.WithMounts` |
| Adds (artifact) | `images/runtime/nodejs20/Dockerfile` + `just build-runtime-images` | operator-built curated image |
| Consumes | ADR-0011 driver, ADR-0030 shim, ADR-0031 materializer, ADR-0003 RuntimeClass | composes them |
| Exposes | container execution of the shim; `SandboxSpec.Mounts`; the curated image | production F12 lane |

## Implementation plan

1. **`internal/runtime/runtime.go`** — add the `Mount` type + `SandboxSpec.Mounts`.
2. **`internal/runtime/containerd/containerd_linux.go`** — map `spec.Mounts` → `oci.WithMounts` (bind, ro/rw);
   persist the spec's `FUNCD_PORT` in the `sandbox` struct at `Create` and return it as `Instance.Port` on
   **both `List` and `Status`**; correct `List`'s hardcoded `StateRunning` to the real task status (via the
   existing `mapState`). (`containerd_other.go` stays the non-Linux stub.)
3. **`shim/nodejs/shim.mjs`** — the `FUNCD_PORT` bind branch (§3), keeping the P-V-1 portfile branch.
4. **`internal/function/function.go`** — `EndpointMode` + `ImageFor` in `Deps` (default `EndpointLoopback`);
   relax the `NewReconciler` ShimCommand guard for `EndpointNetnsFixedPort`; in container mode build the
   `SandboxSpec` with `ImageFor(fn.Spec.Runtime)`, an **empty `Command`** (image entrypoint), the read-only
   artifact `Mount`, and `FUNCD_PORT=8080` in `Env`; poll `IP:8080` readiness. Process mode (loopback +
   `shimCommand` + portfile) unchanged.
5. **`pkg/funcd`** — a `WithContainerExecution(imageFor)` option that selects container mode (used with the
   containerd driver); `WithRuntimeShim` stays the process-mode option.
6. **`images/runtime/nodejs20/Dockerfile`** + **`justfile`** `build-runtime-images` recipe.
7. **Tests** — cross-platform unit: `spec-mounts-artifact` (SandboxSpec construction), `process-driver-unaffected`
   (loopback branch intact), a shim unit for the `FUNCD_PORT` branch (node-gated); **Linux-gated** e2e
   (`//go:build linux`): `container-shim-serves`, `bind-mount-is-readonly` — **deferred on non-Linux** (recorded),
   matching ADR-0011's split. `curated-image-builds` is a structure check over the Dockerfile.
8. **Definition of done**: `just ci` green (cross-platform unit + shim node-gated; Linux e2e gated/deferred off
   Linux); `SandboxSpec.Mounts` honored by the containerd driver; the shim binds `FUNCD_PORT` in container
   mode and loopback+portfile in process mode; the curated Dockerfile builds an image with the shim as
   entrypoint; no new Go dependency; no identity/path leak.

## Review checklist

- [ ] **Artifact reaches the container** (`spec-mounts-artifact`, `bind-mount-is-readonly`): the materialized
      artifact is a **read-only** bind mount at the known path; `FUNCD_ARTIFACT` names the in-container path.
- [ ] **Same shim, container transport** (`container-shim-serves`): the shim binds `FUNCD_PORT` on the netns IP;
      the reconciler polls `netnsIP:port/health/readiness` to Ready and the handler serves — the ADR-0030
      contract unchanged but for the bind address.
- [ ] **Process driver unaffected** (`process-driver-unaffected`): P-V-1's loopback+portfile path is intact
      (Mounts empty, FUNCD_PORT unset).
- [ ] **Curated image** (`curated-image-builds`): the Dockerfile carries `node` + the shim as entrypoint; built
      by `just build-runtime-images`. No new Go dependency; ADR-0002 conventions; Linux-only e2e gated +
      deferral recorded; no leak; every Scenario a named test (Linux e2e gated).

## Consequences

- (+) **Production execution path complete**: the same shim runs in a crun sandbox with default-deny lateral
      (ADR-0011), the artifact delivered by mount, addressed on the netns IP. P-V-1 + P-V-A + ADR-0011 compose.
- (+) **No per-deploy image build in V1** — bind-mounting the artifact onto the curated base is simpler and
      avoids an image-build dependency on the hot path; the baked-layer build stays available as an optimization.
- (+) **No new dependency** — reuses containerd's mount support and the existing shim.
- (−) **Linux-only e2e** — the container scenarios can't run on the dev (darwin) host; they are build-tagged and
      deferred there, validated on the Linux lane (the same constraint ADR-0011 already lives with).
- (−) **Curated images are operator-published** — V1 ships the Dockerfile + recipe, not a registry; publishing
      + a per-revision image build are follow-ups.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| artifact delivered by **bind mount**, not a baked per-revision image layer | avoids a per-deploy image build on the hot path; same logical base+artifact | a per-revision base+layer image build (P-V-2 follow-up / V2) |
| container e2e **deferred off Linux** (build-tagged) | crun/containerd/netns need Linux | run on the Linux CI lane (ADR-0025) |
| curated images **operator-built** from the shipped Dockerfile | no image registry/publish pipeline in V1 | a publish pipeline + per-revision build (follow-up) |

## Alternatives considered

- **Bake the artifact into a per-revision image at deploy** — deferred: it needs an image-build step (base +
  artifact layer + push) on every deploy; the bind mount gives the same base+artifact result with no build.
  Kept as a documented optimization (the blueprint's eventual model).
- **Copy the artifact into the container via `Exec`/cp after start** — rejected: racy (must precede the shim
  load), and a mount is atomic + read-only (the function can't mutate its artifact).
- **Keep the loopback+portfile handshake in the container** — rejected: a portfile would need a shared host
  mount and the loopback bind isn't reachable from the reconciler; the netns IP + a fixed port is exactly what
  ADR-0011 already exposes.
- **A second "container shim" image with a different contract** — rejected: one shim contract (ADR-0030); only
  the bind address differs, a one-branch refinement of the same shim.

## Open questions

| Question | Where it gets answered |
|---|---|
| Per-revision base+layer image build (vs bind mount) | a P-V-2 follow-up / V2 optimization |
| Curated-image publish pipeline + signing | release/packaging follow-up (ADR-0026 successor) |
| Python curated image + shim | P-V-3 (same contract) |
| Multiple replicas sharing one netns vs one-netns-each (port reuse) | ADR-0011 already gives one netns per sandbox → fixed port is safe |

## References

- [ADR-0011](0011-runtime-sandbox-port.md) — the containerd/crun driver this refines (mounts + endpoint).
- [ADR-0030](0030-function-execution-runtime-shim-node.md) — the shim contract run inside the container.
- [ADR-0031](0031-oci-artifact-distribution-oras.md) — the artifact pulled, then mounted in.
- [blueprint.md](../../blueprint.md) — "layers the artifact onto the matching runtime base image at deploy".
