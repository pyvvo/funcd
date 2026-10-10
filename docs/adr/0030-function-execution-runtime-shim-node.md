# ADR-0030: Function execution — the runtime shim contract + a Node reference shim (P-V-1)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0160](0160-worker-exit-reason.md) (2026-10-05) — §4b: terminal shape failure (or timeout) → Failed + ShapeValid:False.
- **Superseded in part by**: [ADR-0215](0215-built-in-health.md) (2026-10-10) — §4b: readiness also covers declared bindings through `/health/dependencies`.
- **Date**: 2026-06-15 (**Implemented 2026-06-15** — review pass, all 5 scenarios green incl the node-gated
  real-shim e2e + timer-invokes-real-handler; dual-mode keeps the ADR-0020 lifecycle tests unchanged; no new
  dependency. **Accepted 2026-06-15** after judge pass — no Blockers left open. The judge confirmed
  the shim-contract-as-seam, the goja rejection, and the shape-gate-at-the-shim are sound, and caught real
  design bugs that "make the sandbox serve HTTP" forces: **B1/B2** — the process driver gives no usable address
  and replicas would collide on a fixed port → fixed with **OS-assigned per-replica loopback + a `FUNCD_PORTFILE`
  handshake**, surfacing `Instance.Port` (refines ADR-0011) and a resolved `upstreamFor`; **M1** — there is no
  artifact-upload-to-blob path → reframed: V1 uses a **local `file://` artifact**, and **production artifact
  distribution is OCI** (the user's steer; realized in P-V-2 as a per-revision base+layer image containerd
  pulls — no blob-as-artifact-store); **M2** — explicitly **refines ADR-0020's readiness gate** (Running
  necessary-not-sufficient; `/health/readiness` is Ready), composing with the activator wake; **M3** — names a
  **runtime-gated test tier** extending ADR-0025 (skips without `node`; the platform side stays pure-Go in
  `just ci`). Decision: the shim HTTP contract + a Node reference shim on the process driver. No new dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, cloudevents, function-execution, nodejs, materialization, readiness, F12, F13
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — the curated-runtime **shim**, the
  half F12's row marks "co-realized with F13") + [FEAT-0000/F13](../feat/0000-feat-v1.md) (the CloudEvents
  handler contract + function shape, now *executed*)
- **Relates to / refines**: [ADR-0011](0011-runtime-sandbox-port.md) — **refines** its `Instance` to surface a
  resolved endpoint (`Instance.IP`/`Instance.Port`), and its process driver to bind loopback + report the port
  (today `Instance.IP` is empty → the upstream is unreachable; this ADR fixes that for the process driver);
  [ADR-0020](0020-function-contract-lifecycle.md) — **refines** its readiness gate (C3): `StateRunning` is now
  *necessary but not sufficient* — `GET /health/readiness == 200` is the Ready condition — and `upstreamFor`
  now uses the resolved per-replica endpoint (ADR-0020's other contracts stand unchanged);
  [ADR-0023](0023-eventing-core.md) (the eventing invoker that POSTs CloudEvents to that upstream — so a timer
  reaches a real handler once addressing is fixed); [ADR-0007](0007-blob-storage-layer-port.md) (blob — the
  *remote*-artifact source, deferred; V1 uses a local `file://` artifact, see Decision §3)

## Context & Need

Everything to *deploy* a function is built — the reconciler (ADR-0020) provisions sandboxes, marks Ready, and
programs routes; the activator wakes; eventing invokes. But the sandbox runs **`["sleep","86400"]`** (the
ADR-0020 placeholder): **no user handler executes.** The exit criterion's core — *"deploy a JS/Python
function from a source artifact whose handler consumes CloudEvents… invoked"* — is not met because nothing
runs the artifact.

The platform side is already shaped for real execution: `SandboxSpec` carries `Command`/`Env`
(`internal/runtime/runtime.go`); the reconciler builds it per replica (`function.go` `sandboxSpec`); and the
reconciler **already resolves the upstream as `http://<ip>:<port>`** for a Running instance
(`function.go` `upstreamFor`) — i.e. the platform already expects the sandbox to **serve HTTP** on a known
port, which the gateway/activator/eventing proxy to. The only missing piece is a program *inside* the
sandbox that serves that HTTP contract and runs the handler: the **runtime shim**.

This ADR is **P-V-1**, the first, CI-testable vertical slice of P-V: define the shim↔platform contract and
ship a **Node reference shim** the **process driver** runs — proving a real JS handler executes and is
invocable, without a container. The shim is a *seam*: the same contract is run by the process driver here
(dev/e2e) and by the curated-image + crun driver in **P-V-2** (production, Linux). One topic at one altitude:
*how a function's artifact is executed and invoked.*

## Scope

- **In**: the **runtime-shim HTTP contract** (invoke a CloudEvent → handler → response; `/health/readiness` +
  `/liveness`); a **Node reference shim** (`shim/nodejs/`) that loads the artifact, resolves the Knative-func
  `handle(context, event)` export (the authoritative materialization shape-gate), and serves the contract; an
  **artifact materializer** seam (P-V-1: resolve a `file://` local artifact to a path) wired into the
  Function reconciler; **per-replica loopback addressing** (the shim self-assigns a port + reports it; refines
  `Instance`); the reconciler's **`sandboxSpec` change** (build the shim `Command`/`Env` instead of `sleep`)
  and **readiness change** (poll `/health/readiness` → `Ready` / `ShapeValid:False`→`Failed`); the
  **decision that production artifact distribution is OCI** (realized in P-V-2); a **node-gated integration
  test** proving deploy→execute→invoke→response (+ a bad artifact → `Failed`).
- **Out**: the **curated OCI images + crun/containerd materialization** (mount, netns, egress) — **P-V-2**
  (Linux/L4); the **Python** shim — **P-V-3** (same contract); the **in-handler SDK** (KV/blob/secrets/events
  callbacks via a local worker API) — a follow-up (meets P-W); **HTTP invocation through the gateway
  data-plane** (route→activator→shim) — **P-X** (this ADR's test invokes the shim's upstream directly / via
  eventing); **response streaming/WebSocket** from the handler, **egress interception** (undici/sitecustomize),
  and per-replica autoscaling — later.

## Constraints & Decision drivers

- **The platform already expects an HTTP-serving sandbox** (`upstreamFor`) — so the shim conforms to that
  (serve on the upstream port); no platform-protocol redesign.
- **Materialization is the authoritative shape-gate** (blueprint §"Shape enforcement"): the shim — not the
  reconciler — loads the artifact and resolves `handle`; on failure the function never becomes Ready and the
  reconciler records `ShapeValid:False`. This ADR moves readiness from "process is running" to "the shim
  reports ready".
- **One shim contract, two sandbox drivers** (ports-and-drivers): the process driver runs the shim directly
  (dev/e2e, this ADR); crun runs it inside the curated image (prod, P-V-2). The contract is the seam.
- **CI-honest**: real JS execution needs `node` on the runner, so the execution test is **node-gated**
  (skips without node) — the containerized proof stays in the L4 lane (ADR-0025). No fake "executed".
- **ADR-0002**: typed surface, `api/fault`, ctx-first, no globals, no `any`; the Go side adds no new
  dependency (stdlib `net/http`/`os/exec`); the shim is plain Node (no npm deps).

## Scenarios

- **scenario: shim-executes-handler** *(node-gated)* — *Given* a JS artifact exporting `handle(context, event)`,
  *when* the shim is started on it and receives `POST /` with a CloudEvent, *then* it invokes the handler with
  the event and returns the handler's result as the HTTP response.
- **scenario: shim-readiness-gate** *(node-gated)* — *Given* a **valid** artifact, *when* the shim starts,
  *then* `GET /health/readiness` returns 200 once `handle` is resolved; *given* an artifact with **no**
  `handle` export, *then* readiness never goes 200 and the shim reports the shape error.
- **scenario: reconciler-materializes-and-runs** *(node-gated)* — *Given* a Function whose `artifact.uri`
  is a local `file://` bundle, *when* the reconciler converges it, *then* it resolves the artifact to a local
  path, launches the Node shim via the process driver, reads the port handshake, polls the resolved
  `/health/readiness`, and marks the Function `Ready` — and a function whose artifact lacks `handle` reaches
  `Failed` with `ShapeValid:False`.
- **scenario: timer-invokes-real-handler** *(node-gated)* — *Given* a Ready function + a timer EventSource,
  *when* the timer fires, *then* the eventing invoker (ADR-0023) POSTs the CloudEvent to the shim and the real
  handler runs (the existing invoke path now reaches real code) — proving the exit criterion's "invoked by a
  timer" end-to-end without the gateway data-plane.
- **scenario: platform-side-without-node** — *Given* no `node` on the runner, *when* the node-gated tests are
  skipped, *then* the Go materializer + `sandboxSpec` + readiness-polling logic are still unit-tested against a
  **fake shim** (a Go `httptest` server speaking the contract) — so the platform side is proven in pure-Go CI.

## Decision

### 1. The runtime-shim HTTP contract
The shim serves, on the function's upstream port:
- `POST /` — body is a CloudEvent (`application/cloudevents+json`, the ADR-0023 envelope). The shim invokes
  `handle(context, event)` and maps the return to the HTTP response (Knative-func convention: a returned
  object → JSON 200; `undefined`/no return → 204; a thrown error → 500 + a problem-shaped body). Headers carry
  the CloudEvent attributes back where applicable.
- `GET /health/readiness` — 200 once the artifact loaded and `handle` resolved; 503 (with the shape error in
  the body) until/if it cannot. **This is the authoritative shape-gate.**
- `GET /health/liveness` — 200 while the process is up.

The contract is HTTP + the existing CloudEvents envelope (no proto, no new dependency); a `funcd/v1` proto is
a later option if a non-HTTP transport is ever needed.

### 2. The Node reference shim (`shim/nodejs/shim.mjs`)
A dependency-free Node ESM program. Inputs (env): `FUNCD_ARTIFACT` (local path), `FUNCD_HANDLER` (export name,
default `handle`), `FUNCD_PORTFILE` (it binds `127.0.0.1:0` and writes the OS-assigned port here once
listening — the §4a handshake). On start it `import()`s the artifact, resolves the handler export (→
readiness), and serves §1 with Node's built-in `http`. The handler shape is Knative-func
`handle(context, event)` (the `event` is the CloudEvent); `context` is a minimal object for V1 (logging +
the event metadata; the KV/blob/secrets SDK is the deferred follow-up). It is shipped as a repo asset (and,
in P-V-2, baked into the curated base image).

**Runtime-shape note** (so P-V-3 fills a hole, not amends the contract): the *handler resolution* + the
*readiness/liveness delegation* are runtime-shape-specific. Node resolves a single `handle(context, event)`
export; Python (P-V-3) resolves a `new()` factory → instance with `handle(...)` + optional `start(cfg)`/`stop()`
+ health `alive()`/`ready()` (blueprint §"Function shape"), the shim delegating `/health/*` to those hooks.
The §1 HTTP contract is identical; only the in-shim resolution differs per runtime.

### 3. Artifact materialization — local path now, **OCI** for production
The production artifact-distribution model is **OCI** (decided here, realized in P-V-2): a function artifact
is content-addressed by digest in an OCI registry — the same machinery containerd already pulls, matching
`ArtifactRef.Digest`, with caching/signing/replication for free, and **no blob-as-artifact-store + no
upload-to-blob path** (the gap a blob source would have left, since nothing uploads artifacts to blob today).
The cleanest shape — built in P-V-2 — is a **per-revision function image = curated base (with the shim) + a
thin artifact layer**, which containerd pulls and runs directly (a layer-append, *not* a source build).

The `Materializer` is a **seam with two drivers**, mirroring the runtime port:
```go
// internal/function — resolve a Function's artifact to a local path the shim reads.
type Materializer interface {
    Materialize(ctx context.Context, fn *v1.Function) (localPath string, err error)
}
```
- **P-V-1 (process driver, this ADR)**: a **local-file** materializer — `ArtifactRef.URI` is a `file://` path
  (the dev/CI artifact is a local bundle); `Materialize` resolves it to a local path (no registry, no
  containerd, CI-testable). It caches per Revision digest (the Revision pins the artifact, immutable) and the
  reconciler cleans it on teardown.
- **P-V-2 (containerd/crun)**: an **OCI** materializer — pull by `ArtifactRef` (registry ref @ digest) into
  the content store / function image; containerd runs it. (`ArtifactRef.URI` carries the OCI reference there.)

The reconciler gains a `Materializer` (+ the shim path) in its `Deps`; `sandboxSpec(fn, i)` builds
`Command = ["node", shimPath]` + `Env = {FUNCD_ARTIFACT: localPath, FUNCD_HANDLER: fn.Spec.Handler,
FUNCD_PORTFILE: <handshake file>}` (see §4a) instead of `["sleep","86400"]`. No `blob`/registry dependency
enters the Go side in P-V-1 (the OCI client lands with P-V-2).

### 4a. Per-replica loopback addressing (refines ADR-0011 `Instance`)
Once the sandbox *actually serves HTTP*, two things break that `sleep` hid: the process driver leaves
`Instance.IP` empty (so `upstreamFor` falls back to a synthetic `name.ns` host that resolves to nothing), and
every replica gets the *same* fixed `upstreamPort` (so replica 1 fails `bind: address already in use`). This
ADR fixes both with an **OS-assigned per-replica loopback port + a ready handshake**:
- The shim binds `127.0.0.1:0` (the OS picks a free port) and writes the chosen port to the file named by
  `FUNCD_PORTFILE` once listening — a tiny, race-free handshake.
- The **process driver** reads that file after Start and surfaces the resolved endpoint on the instance:
  `Instance.IP = "127.0.0.1"`, **`Instance.Port = <n>`** (a new field — this *refines* ADR-0011's `Instance`;
  the containerd driver fills it from the container's netns IP + the known port in P-V-2).
- `upstreamFor` (ADR-0020) now returns `http://<Instance.IP>:<Instance.Port>` per replica — no synthetic host,
  no fixed `8080`, no cross-replica collision. (`upstreamPort` the constant is removed.)

### 4b. Readiness = the shim's shape-gate (refines ADR-0020's C3)
This **refines ADR-0020's readiness gate**: `StateRunning` is now *necessary but not sufficient*. After Start +
the port handshake, the reconciler polls the resolved `…/health/readiness`: 200 → `Status.Phase=Ready` +
`ShapeValid:True`; a terminal shape failure (or timeout) → `Phase=Failed` + a `ShapeValid:False` condition
carrying the shim's error — making materialization the authoritative gate (blueprint). It composes with the
activator wake Phase (`desiredReplicas`): a `Deploying` wake provisions ≥1 replica, which then passes through
this readiness gate to `Ready` exactly as a fresh deploy does (the poll doesn't block the wake — it runs after
the replica is started, and an un-ready replica simply isn't counted toward `Ready`).

### 5. The test lane — a named "runtime-gated" tier (extends ADR-0025)
ADR-0025's taxonomy has pure-Go `just ci` (L1–L3) and the Linux+container L4 lane. Real JS execution needs a
*language runtime* but **not** a container, so it doesn't fit either — this ADR **adds a named tier to ADR-0025**:
**L3-runtime** — tests gated on a language runtime (`exec.LookPath("node")`), `t.Skip` when absent, run by
`just ci` when present and always in CI's runtime-enabled job. (It is *not* an unclassified skip: it's a
recorded tier between L3b and L4 — runtime present, container absent.)
- **L3-runtime (node-gated)**: real Node shim + process runtime + the reconciler → deploy a local JS artifact →
  Ready → POST a CloudEvent → assert the handler ran; bad artifact → Failed. Skips without node.
- **pure-Go L3 (always in `just ci`)**: the materializer (file→path), `sandboxSpec` construction, the port
  handshake, and readiness-polling against a **fake shim** (a Go `httptest` server serving §1) — the *platform
  side* proven with no node, so `just ci` stays green-without-node. The full containerized walk stays at L4.

## Contracts

### Shim HTTP contract (language-agnostic)
```
POST /                      CloudEvent (application/cloudevents+json) → handler → response
                            (object→200 JSON · none→204 · throw→500 problem+json)
GET  /health/readiness      200 when handle resolved (shape-gate); 503 + error otherwise
GET  /health/liveness       200 while up
```
### Go (`internal/function`, `internal/runtime`)
```go
type Materializer interface { Materialize(ctx context.Context, fn *v1.Function) (string, error) } // local file path (P-V-1)
// reconciler Deps gains: Materializer (+ shim path); sandboxSpec builds the shim Command/Env (FUNCD_PORTFILE);
// readiness polls the RESOLVED endpoint's /health/readiness.
// runtime.Instance gains Port int (refines ADR-0011); the process driver fills IP="127.0.0.1" + Port via the handshake.
```
### Node (`shim/nodejs/shim.mjs`)
```
env: FUNCD_ARTIFACT (local path) · FUNCD_HANDLER (export, default "handle") · FUNCD_PORTFILE (write OS-assigned port)
```
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/runtime` (process driver), `api/types`, stdlib `net/http`/`os/exec`/`os`; Node stdlib (`node:http`) | **no new Go or npm dependency** (no blob/registry client in P-V-1 — local artifact) |
| Adds | `shim/nodejs/shim.mjs` (repo asset); a `Materializer` + reconciler wiring; `Instance.Port` | OCI artifact pull + curated images = **P-V-2** |
| Exposes | the shim HTTP contract; real function execution on the process driver | the contract every language shim + P-V-2's container implement |

## Implementation plan

1. **`shim/nodejs/shim.mjs`** — load `FUNCD_ARTIFACT`, resolve `FUNCD_HANDLER`, serve §1 (`node:http`), readiness gate.
2. **`internal/function/materializer.go`** — the `Materializer` seam + a local-file driver (resolve a `file://`
   `ArtifactRef.URI` to a path, cached per Revision digest, cleaned on teardown). (The OCI driver lands in P-V-2.)
3. **`internal/runtime`** — `Instance.Port int` (refines ADR-0011); the **process driver** binds via the shim's
   `FUNCD_PORTFILE` handshake and fills `Instance.IP="127.0.0.1"`/`Port`.
4. **`internal/function/function.go`** — reconciler `Deps` += `Materializer`; `sandboxSpec` builds the shim
   Command/Env; `upstreamFor` uses `Instance.IP:Port`; `converge`/readiness polls `/health/readiness`
   (Ready/Failed + `ShapeValid`).
4. **`pkg/funcd` wiring** — build the `Materializer` from the platform blob + the shim path; `InMemory()`’s execution path uses a `file://` blob.
5. **Tests**: node-gated integration (`shim-executes-handler`, `shim-readiness-gate`, `reconciler-materializes-and-runs`, `timer-invokes-real-handler`) + pure-Go (`platform-side-without-node` against a fake shim) + a `shim/nodejs` self-test (node `--test`) if node is present.
6. **Definition of done**: `just ci` green (pure-Go side always; node-gated tests run when node is present); a real JS handler executes + is invocable on the process driver; bad artifact → `Failed`/`ShapeValid:False`; no new Go/npm dependency; no identity/path leak.

## Review checklist

- [ ] **Real execution** (`shim-executes-handler`, `reconciler-materializes-and-runs`): a deployed JS artifact
      runs and returns its handler's result; the reconciler materializes from blob, launches the shim, and
      marks Ready via `/health/readiness`.
- [ ] **Shape-gate is authoritative** (`shim-readiness-gate`): a missing `handle` → never Ready →
      `ShapeValid:False`/`Failed`; no route for a bad function.
- [ ] **Timer reaches real code** (`timer-invokes-real-handler`): the existing eventing invoke now hits a real handler.
- [ ] **Platform side proven without node** (`platform-side-without-node`): materializer + sandboxSpec +
      readiness-poll unit-tested against a fake shim in pure-Go `just ci`; the node-gated tests `t.Skip` cleanly.
- [ ] One shim contract; process driver only (crun=P-V-2, Node-only=P-V-3, in-handler-SDK + gateway-invoke
      deferred); **no new dependency**; ADR-0002 conventions; no identity/path leak; every Scenario a named test.

## Consequences

- (+) **Functions actually run**: a real JS handler executes and is invocable (timer path end-to-end) on the
  dev/e2e process driver, in CI when node is present — the biggest unbuilt chunk of the exit criterion, and the
  contract every language shim + the production sandbox (P-V-2) implement.
- (+) **The shape-gate becomes real** (readiness via the shim), so a bad artifact never serves.
- (+) **No new dependency** (stdlib Go + plain Node); the shim contract is HTTP + the existing CloudEvents envelope.
- (−) **Process driver only, Node only, node-gated** — production sandboxing (crun + curated images) is P-V-2
  (Linux/L4), Python is P-V-3, and the execution test needs node; the platform side is the pure-Go fallback.
- (−) **No in-handler SDK / no gateway HTTP-invoke yet** — the handler can't call KV/blob/secrets (follow-up,
  meets P-W) and HTTP invocation through the gateway is P-X; timer-invoke + direct-upstream are the V1 proofs.
- (note) **Roadmap**: this is P-V-1 (splits P-V); P-V-2 (images + crun) and P-V-3 (Python) follow, then P-W/P-X.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| `context` passed to the handler is minimal (no KV/blob/secrets SDK) | the in-handler SDK needs a local worker API + secret injection | the SDK-in-shim follow-up (meets P-W) |
| execution proven on the **process driver**, not a container | crun/image materialization is a separate, Linux-only slice | **P-V-2** (curated images + crun, the L4 walk) |

## Alternatives considered

- **Embed a pure-Go JS engine (goja) as the shim** — would run "JS" in pure-Go CI with zero external runtime.
  Rejected: goja ≠ Node (no `node:` builtins, npm, streams) — a non-production toy that would prove the wrong
  thing; the exit criterion is *real* nodejs/python. A node-gated lane proves the real runtime honestly.
- **Reconciler loads/validates the artifact itself (shape-gate in Go)** — rejected: only the *runtime* can
  authoritatively resolve a JS/Python handler; the blueprint puts the authoritative gate at materialization in
  the shim. Go-side validation stays the cheap pre-flight (the existing `ShapeValidator`), not the authority.
- **A `funcd/v1` gRPC/proto shim transport now** — rejected for V1: the platform already speaks HTTP to the
  upstream (`upstreamFor`) and CloudEvents is HTTP-shaped; a proto adds a dependency + codegen for no V1 gain
  (kept as a later option behind the HTTP contract).
- **Materialize via a host-path mount / bake the artifact into an image** — rejected for the *process-driver*
  slice (no image): fetch-from-blob-to-local-path is driver-agnostic (P-V-2 mounts the same local path into the
  container). Baking is a build-time path that doesn't fit dynamic deploys.

## Open questions

| Question | Where it gets answered |
|---|---|
| The in-handler SDK (KV/blob/secrets/events) + the local worker API the shim calls | a follow-up (meets P-W); the worker-API contract is its own ADR |
| Response **streaming** (LLM tokens / SSE) from the handler through the shim | later — the gateway already streams; the shim adds `FlushInterval`-style passthrough when P-X wires the data plane |
| Egress interception (undici global dispatcher / Python `sitecustomize`) by default | P-V-2's netns + the curated image (egress tier-2); V2 for policy |
| Concurrency model inside the shim (one handler invocation at a time vs. N) | V1: serve concurrently via Node's event loop; a per-instance concurrency limit is a later knob |

## References

- [blueprint.md](../../blueprint.md) — "Curated language runtimes… that embed the runtime shim"; the
  CloudEvents-only handler contract; "Shape enforcement (three gates, one validator)" — materialization is the authority.
- [ADR-0011](0011-runtime-sandbox-port.md) — the `runtime.Runtime` port + process driver the shim runs in.
- [ADR-0020](0020-function-contract-lifecycle.md) — the reconciler/`sandboxSpec`/readiness this changes.
- [ADR-0023](0023-eventing-core.md) — the eventing invoker that now reaches a real handler.
