# ADR-0020: Function contract & lifecycle — the Function reconciler (`internal/function`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass (zero findings), see docs/reviews/adr-0020-implementation-claude-opus-4-8.md; DoD 7/7, 8 scenarios race-clean. **Reviewing 2026-06-14** — implemented: `internal/function` (the Function lifecycle
  reconciler: Revision stamp → effective-replica converge honoring the activator wake Phase → shape gate →
  full-table route program → status; the `activator.Endpoints` provider; `NewBasicValidator`) + F13
  `Function`/`Revision` spec fields; 8 scenarios pass under `-race`, OpenAPI regenerated, four sub-checks
  green, no new deps. **Accepted 2026-06-14** after judge pass — no Blockers. Folded the judge's two
  **Majors**: (M1) the reconciler converges to an **effective** desired-replica count that honors the
  activator's wake `Status.Phase` (`MinReplicas:0`+`Deploying`→≥1, `Idle`→0) — not blindly `spec.replicas` —
  so scale-to-zero actually wakes (the P-M half of ADR-0016's contract); (M2) route programming is **full-table**
  (`gateway.ProgramRoutes` is replace-all, so the reconciler programs **all** Ready functions' routes, never
  just its own — no clobber), with a two-function scenario. Minors: V1's shape gate is honestly the
  reconcile/materialization stage (P-L admission is frozen+envelope-only); Revision-change detected via
  `metadata.generation`; noted the `function→activator` Endpoints import. Decision: one Function lifecycle
  reconciler composing runtime/scheduler/gateway, materialization-gated, providing the production
  `activator.Endpoints`; real JS/Python shim + build + CloudEvents transport deferred to P-S/P-Q.)
- **Deciders**: green-0-rabbit
- **Tags**: function, revision, lifecycle, reconciler, shape, cloudevents, artifact, endpoints, control-plane, data-plane
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (function contract & lifecycle: CloudEvents handler, function shape, source-artifact deploys, shape-validation pipeline, apply→Revision→deploy→invoke→logs, manual replicas)
- **Relates to**: [ADR-0015](0015-controller-engine.md) (the engine — the **Function reconciler** is the one
  `Reconcile` for `KindFunction`), [ADR-0011](0011-runtime-sandbox-port.md) (the `runtime.Runtime` port the
  reconciler drives to provision sandboxes), [ADR-0017](0017-scheduler-placement-port.md) (the scheduler the
  reconciler asks for placement), [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the gateway the
  reconciler programs a route on once Ready), [ADR-0016](0016-activator-scale-to-zero.md) (**this ADR provides
  the production `activator.Endpoints`** — a Function's ready upstream — that ADR-0016 deferred to P-M),
  [ADR-0006](0006-store-database-layer-port.md) (the store: Function/Revision read + status write-back),
  [ADR-0003](0003-resource-model-and-api-typing.md) (the `Function`/`Revision` kinds — this ADR adds their F13
  spec fields), [ADR-0002](0002-source-code-conventions-and-patterns.md) (`New(Deps)`, `api/fault`, ctx-first,
  no globals, no `any`, no mocks), [blueprint.md — Function / Revision / Function runtime](../../blueprint.md).
  **New deps: none** (the control-plane lifecycle; the real JS/Python runtime shim is deferred to the e2e lane).

## Context & Need

The V1 exit criterion is "`funcdcli apply` a JS/Python function from a source artifact … deployed → invokable
→ logs … scales to zero." Every port for that exists — runtime (ADR-0011), gateway (ADR-0013), scheduler
(ADR-0017), controller engine (ADR-0015), store (ADR-0006), activator (ADR-0016) — but **nothing ties them
together**: there is no reconcile loop that turns an applied `Function` into a running, routed, ready
workload. That loop is F13, the **re-rooted critical-path keystone** (`P-J → P-M → P-Q`): it feeds eventing
(P-Q) and supplies the **`Endpoints` resolver the activator (ADR-0016) explicitly deferred to P-M**.

The blueprint pins the lifecycle: apply a `Function` → **stamp an immutable `Revision`** (the artifact pinned
by digest — "what was validated is exactly what ships") → the scheduler places it → the worker boots a
sandbox whose **runtime shim materializes the shape** (loads the artifact, resolves `handle`/`new()`, wires
health hooks) → **on success a route is programmed and the function becomes Ready; on shape failure no route
is programmed and `ShapeValid: False` is written to status**. The handler contract is **CloudEvents-only**
(the shim normalizes every trigger; for sync HTTP the return maps to the response). The **shape** is
Knative-func-compatible (Node `handle(context, event)`; Python `new()`+`handle`/`start`/`stop`/`alive`/`ready`).

**Purpose**: implement `internal/function` — the **Function lifecycle reconciler** (one `controller.Reconciler`
for `KindFunction`) that stamps Revisions, schedules + provisions sandboxes via the runtime port for the
desired replica count, validates the shape, programs the gateway route once Ready, and writes status
(`Phase`, `Replicas`, `CurrentRevision`, `Ready`/`ShapeValid` conditions); plus the **`Endpoints` provider**
the activator consumes; plus the **`Function`/`Revision` F13 spec fields** and a **shape-validator seam**.
Callers: the composition root registers the reconciler + wires the `Endpoints` into the activator. Conformance
is mechanical with the **process runtime driver** (no real JS/Python): an applied Function reconciles to Ready
with N sandboxes + a programmed route + a resolvable endpoint; a shape-invalid artifact reconciles to
`ShapeValid: False` with no route.

## Scenarios

- `scenario: apply-stamps-revision` — **Given** a `Function` applied to the store, **when** the reconciler
  runs, **then** it stamps an immutable `Revision` (number 1, the artifact/handler/runtime snapshot pinned)
  and records it as `Status.CurrentRevision`.
- `scenario: reconcile-provisions-and-routes` — **Given** a valid `Function` (replicas N) with the process
  runtime driver, **when** the reconciler runs, **then** it schedules + creates/starts **N** sandboxes via the
  runtime port, programs a gateway route to the function, and writes `Status.Phase=Ready` + `Replicas=N` +
  `Ready: True`.
- `scenario: shape-invalid-blocks-ready` — **Given** a `Function` whose artifact fails shape validation, **when**
  the reconciler runs, **then** it writes `ShapeValid: False` (with the reason), does **not** program a route,
  and the function does **not** become Ready (the materialization gate).
- `scenario: endpoints-resolves-ready-upstream` — **Given** a Ready function with a running sandbox, **when**
  the activator's `Endpoints.Upstream(fn)` is called, **then** it returns the sandbox's upstream URL +
  `ready=true`; for a scaled-to-zero / not-ready function it returns `ready=false` (the seam ADR-0016 deferred).
- `scenario: scale-changes-replicas` — **Given** a Ready function, **when** its `spec.replicas` changes, **then**
  the reconciler converges the running sandbox count to match (create or stop) and updates `Status.Replicas`.
- `scenario: delete-reclaims` — **Given** a Ready function, **when** it is deleted (the reconcile sees it gone
  from the store), **then** the reconciler stops its sandboxes and removes its route (no orphan sandboxes/routes).
- `scenario: wake-provisions-scaled-to-zero` — **Given** a `MinReplicas:0` function with no running sandboxes
  whose `Status.Phase` is `Deploying` (the activator's wake signal, ADR-0016), **when** the reconciler runs,
  **then** it provisions ≥1 sandbox (honoring the wake) so the upstream can go ready — *not* 0 (which would
  drop the wake). With `Phase=Idle` (reclaimed) it converges to 0.
- `scenario: routes-preserved-across-functions` — **Given** two Ready functions in a namespace, **when** the
  reconciler programs routes for one, **then** the **other's route is still present** (full-table replace-all
  programming — no clobber).

## Scope

**In**:
- **`internal/function`**: the **Function reconciler** (one `controller.Reconciler` for `KindFunction`):
  Revision stamping → scheduler placement → runtime provision/converge (N replicas) → shape validation →
  gateway route program → status write-back (`Phase`/`Replicas`/`CurrentRevision`/`Ready`/`ShapeValid`); plus
  the **`Endpoints` provider** (implements `activator.Endpoints`) and a **`ShapeValidator` seam** + a basic V1
  validator.
- **`Function`/`Revision` F13 spec fields**: `FunctionSpec += { Runtime, Handler, Artifact, Replicas }` and the
  frozen snapshot on `RevisionSpec`; `FunctionStatus += { Replicas, CurrentRevision }` (+ the `Ready`/`ShapeValid`
  conditions on the shared `Status.Conditions`).
- **Manual replica scaling** (`spec.replicas`); the reconciler converges actual→desired sandbox count.

**Out (deferred, blueprint-sanctioned / sequenced)**:
- **The real JS/Python runtime shim + curated runtime images + source-artifact layering** — the shim that
  loads the artifact, resolves `handle`/`new()`, serves health, and speaks CloudEvents runs on the Linux
  container lane; V1 ships the lifecycle against the **process runtime driver** and **defers the shim/image +
  the load-time materialization test to the e2e/Linux lane (P-S)**, per the roadmap's test-sequencing note.
- **CloudEvents normalization wire path + HTTP/timer trigger capture** — the **eventing core (P-Q)**; this ADR
  fixes the **handler/shape *contract*** + the route, not the normalization transport.
- **Source-artifact build/registry layering, digest signing (cosign)** — V1 pins the artifact ref in the
  Revision; build/layer/verify is a follow-up (V2 build pipeline).
- **Concurrency/RPS autoscaling** — V3; V1 is manual `spec.replicas` (+ scale-to-zero via the activator ADR-0016).
- **Response streaming escape hatch** — the blueprint's open contract point; recorded, decided in a follow-up
  (the gateway already streams via `FlushInterval`).

## Constraints & Decision drivers

- **C1 — one reconcile engine (ADR-0015)**: the Function lifecycle is the one `Reconcile` for `KindFunction`;
  it composes the ports (runtime/scheduler/gateway) — it does not re-implement watch/retry.
- **C2 — Revision is immutable, artifact pinned (blueprint)**: a spec change stamps a **new** Revision; "what
  was validated is exactly what ships." The reconciler never mutates a stamped Revision.
- **C3 — materialization gates Ready (blueprint)**: a route is programmed **only after** the shape validates +
  the sandbox is running; a shape failure writes `ShapeValid: False` and blocks Ready (no route to a broken fn).
- **C4 — the reconciler provides the activator's `Endpoints`**: ADR-0016 deferred the production `Endpoints`
  to P-M; this ADR supplies it (a Function's ready sandbox upstream), closing the scale-to-zero loop.
- **C5 — ADR-0002 conventions**: `New(Deps)` (the reconciler composes store/runtime/scheduler/gateway/validator);
  ctx-first; `api/fault`; `slog`; no globals; no `any`; no mocks (real ports + the process runtime driver).
- **C6 — idempotent + convergent**: re-reconciling a Ready function is a no-op; the reconciler converges
  actual→desired (replicas, route, status) — the controller-runtime contract.

## Alternatives considered

**Where the lifecycle lives** (driver: the blueprint's lifecycle + one-engine rule):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **One Function reconciler composing runtime+scheduler+gateway, materialization-gated, providing Endpoints** | the blueprint's pick; one engine slice; closes the activator loop; testable with the process driver | a big reconciler (the integrative keystone) — unavoidable, it *is* F13 | **chosen** |
| Per-concern controllers (a deploy controller, a route controller, …) | smaller units | fragments one lifecycle across reconcilers racing on `Function.status`; the blueprint models it as one function lifecycle | rejected (fragmentation, status races) |
| Skip the Revision (reconcile the Function spec directly) | less state | loses immutability/pinning ("what shipped"); rollouts/rollback impossible | rejected (blueprint: Revision is the shipped unit) |

**V1 runtime for the lifecycle test**: the **process driver** (ADR-0011) — chosen (cross-platform, in `just
ci`); the containerd driver + the real JS/Python shim materialization run on the Linux lane (P-S), deferred.
**Shape validation**: a **`ShapeValidator` seam** with a basic V1 validator (validates the spec/artifact ref
is well-formed); the authoritative **load-time** materialization (resolve `handle`/`new()`) is the shim's, on
the Linux lane.

## Decision

### 1. The `Function`/`Revision` spec (F13-owned)
```go
type FunctionSpec struct {
	Scaling  Scaling      `json:"scaling,omitempty"`  // ADR-0016
	Runtime  string       `json:"runtime,omitempty"`  // runtime class / language (e.g. "nodejs20", "python312")
	Handler  string       `json:"handler,omitempty"`  // e.g. "app.handler" — resolved by the shim at materialization
	Artifact ArtifactRef  `json:"artifact,omitempty"` // the source artifact (JS bundle / Python wheel), pinned by digest
	Replicas int          `json:"replicas,omitempty"` // manual replica count (>=0); 0 + Scaling.MinReplicas==0 = scaled-to-zero
}
type ArtifactRef struct {
	URI    string `json:"uri"`              // where the artifact lives (blob ref)
	Digest string `json:"digest,omitempty"` // content digest pinned into the Revision
}
// RevisionSpec gains the frozen snapshot (immutable):
type RevisionSpec struct {
	Function ObjectRef   `json:"function"`
	Number   int64       `json:"number"`
	Runtime  string      `json:"runtime,omitempty"`
	Handler  string      `json:"handler,omitempty"`
	Artifact ArtifactRef `json:"artifact,omitempty"`
}
// FunctionStatus gains observed fields:
type FunctionStatus struct {
	Status          `json:",inline"`
	Replicas        int    `json:"replicas,omitempty"`
	CurrentRevision string `json:"currentRevision,omitempty"`
}
```
Conditions `Ready` and `ShapeValid` live on the shared `Status.Conditions` (ADR-0003).

### 2. The Function reconciler (`internal/function`)
`Reconcile(ctx, Request)` for `KindFunction` (idempotent, convergent):
1. **read** the Function (gone → **delete path**: stop its sandboxes, remove its route, return).
2. **stamp Revision** if the spec changed since `CurrentRevision` (detected by comparing the Function's
   `metadata.generation` to the Revision's recorded generation — the store bumps generation on spec change,
   ADR-0006; the activator's status-only `Phase` writes do **not** bump it, so a wake never stamps a spurious
   Revision); the Revision is an immutable snapshot (bump `Number`); set `CurrentRevision`.
3. **validate shape** via the `ShapeValidator`; on failure set `ShapeValid: False` (+reason), **no route**,
   `Phase=Failed/Pending`, return (Ready blocked — C3).
4. **place + provision**: `scheduler.Schedule` → build `runtime.SandboxSpec` from Function/Revision → converge
   the running sandbox set to the **effective desired replica count** (create/start missing, stop extra) via
   the runtime port. The effective count is **not** simply `spec.replicas` — it honors the activator's wake
   signal (ADR-0016's partitioned `Status.Phase`):
   - for a **scaled-to-zero** function (`Scaling.MinReplicas == 0`): desired = `0` when `Phase==Idle`
     (reclaimed); desired = `max(1, spec.replicas)` when `Phase==Deploying` (the activator **woke** it) — so a
     cold request's wake actually provisions a sandbox and `Endpoints` can go ready (this is the P-M half of
     ADR-0016's contract; without it scale-to-zero never wakes);
   - otherwise (`MinReplicas >= 1` or no scaling): desired = `max(spec.replicas, MinReplicas)`.
5. **route (full table) + ready**: once ≥1 sandbox is Running and shape-valid, set `Phase=Ready`, `Ready: True`,
   `ShapeValid: True`, `Replicas=<running>`, then **program the gateway with the FULL desired route table** —
   `gateway.ProgramRoutes` is **replace-all** (ADR-0013), so the reconciler lists **every** Ready function and
   programs **all** their routes in one call; it must never call `ProgramRoutes` with only its own route (that
   would clobber every other function's route). A small in-reconciler **route set** (all Ready functions →
   their routes) is recomputed and re-programmed each reconcile; a deleted/not-ready function is dropped from
   the set.
The reconciler tracks its provisioned instances (per function) so `Endpoints` can resolve them and convergence
is stable.

### 3. The `Endpoints` provider (closes the ADR-0016 activator loop)
`internal/function` exposes an `Endpoints` (implements `activator.Endpoints`): `Upstream(ctx, FunctionRef)`
returns the running sandbox's upstream URL + `ready=true` when the function has ≥1 Running instance, else
`ready=false`. The composition root wires it into the activator — so a scaled-to-zero function's cold request
buffers until this reconciler provisions a sandbox and the endpoint goes ready.

### 4. The shape-validator seam + where in the pipeline V1 gates
`ShapeValidator { Validate(ctx, *v1.Function) error }` — the V1 driver validates the spec/artifact is
well-formed (runtime set, handler set, artifact URI present). **Where V1 gates, honestly:** the blueprint's
3-stage pipeline is CLI pre-flight (P-R) → admission (P-L) → materialization (shim load-time). ADR-0018's
admission is **frozen + envelope-only** (it does not run the `ShapeValidator`), so **V1's shape gate is the
reconcile/materialization stage here** — an invalid-shape Function is accepted by the API (2xx) and fails at
reconcile with `ShapeValid:False` (not rejected at admission with 400). Admission-stage shape validation (the
"one shared validator" at the API edge) is a **deferred refinement** (a P-L successor); the authoritative
**load-time** materialization (the shim resolving `handle`/`new()`) is deferred to the Linux lane (P-S). The
seam is the shared validator both stages will adopt; V1 wires it at the reconcile gate.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Process runtime driver for the V1 lifecycle test** (not the real shim/containers) | cross-platform, in `just ci`; the shim needs node/python + Linux | the e2e/Linux lane (P-S) runs the containerd driver + the real shim materialization (`handle`/`new()` resolution) |
| **Basic `ShapeValidator` (spec well-formedness), not load-time materialization** | the authoritative check needs the shim + the artifact loaded | P-S/the shim does load-time `handle`/`new()` resolution; the seam stays, its driver upgrades |
| **Artifact pinned by ref, not built/layered/cosign-verified** | build/registry layering + signing is a pipeline | a V2 build pipeline layers the artifact onto the runtime base + verifies the digest |
| **CloudEvents is a contract, normalization transport is P-Q** | the eventing core owns trigger capture → CloudEvents | P-Q wires HTTP/timer triggers → CloudEvents to the handler; this ADR fixes the shape/route |

## Contracts

### The reconciler + Endpoints (`internal/function/function.go`)
```go
package function

// Deps configures the Function lifecycle reconciler (internal component, ADR-0002 §1).
type Deps struct {
	Store     store.Store
	Runtime   runtime.Runtime
	Scheduler scheduler.Scheduler
	Gateway   gateway.Gateway
	Validator ShapeValidator
	Logger    *slog.Logger
}

// ShapeValidator validates a Function's artifact conforms to its runtime shape.
type ShapeValidator interface {
	Validate(ctx context.Context, fn *v1.Function) error
}

// Reconciler is the one controller.Reconciler for KindFunction (register on the engine).
type Reconciler struct { /* unexported */ }
func NewReconciler(d Deps) (*Reconciler, error)
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)

// Endpoints resolves a function's ready upstream — the production activator.Endpoints
// (ADR-0016 deferred this to P-M). Backed by the reconciler's provisioned instances.
func (r *Reconciler) Endpoints() activator.Endpoints

// NewBasicValidator returns the V1 spec-well-formedness ShapeValidator.
func NewBasicValidator() ShapeValidator
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/store`, `internal/runtime`, `internal/scheduler`, `internal/gateway`, `internal/controller`, `internal/activator` (the `Endpoints`/`FunctionRef` types), `api/types`, `api/fault` | composes the ports |
| Adds (lib) | none | the process driver tests it |
| Exposes | `function.Reconciler` (+ `Reconcile`, `Endpoints()`), `ShapeValidator` + `NewBasicValidator`; `Function`/`Revision` F13 spec fields | reconciler registered by **P-I**; `Endpoints` wired into the activator by **P-I** |

## Implementation plan

1. **`api/types/v1alpha1/function.go` + `revision.go`** — add the F13 spec/status fields (`Runtime`/`Handler`/
   `Artifact`/`Replicas`, `ArtifactRef`, the Revision snapshot, `FunctionStatus.Replicas`/`CurrentRevision`).
   Keep roundtrip green; regenerate the OpenAPI.
2. **`internal/function/function.go`** — `Deps`, `Reconciler` (`NewReconciler`, `Reconcile` — the steps in
   Decision §2), the instance tracker, `Endpoints()` (implements `activator.Endpoints`), `ShapeValidator` +
   `NewBasicValidator`.
3. **Test plan** (one named test per Scenario; real store + real process runtime driver + real scheduler +
   real embedded gateway + the basic validator, no mocks):
   - `internal/function/function_test.go` → `apply-stamps-revision`, `reconcile-provisions-and-routes`
     (assert N sandboxes via `runtime.List` + a programmed route via `gateway.Routes` + status Ready),
     `shape-invalid-blocks-ready` (a bad-shape Function via a failing validator → `ShapeValid:False`, no route),
     `endpoints-resolves-ready-upstream`, `scale-changes-replicas`, `delete-reclaims`,
     `wake-provisions-scaled-to-zero` (MinReplicas:0 + Phase=Deploying → ≥1 sandbox; Phase=Idle → 0),
     `routes-preserved-across-functions` (two functions; programming one keeps the other's route — full-table).
4. **Definition of done**: `just ci` green (four sub-checks); apply→Revision→provision(N)→route→Ready; shape
   failure blocks Ready + route; `Endpoints` resolves a ready upstream; replica changes converge; delete
   reclaims; OpenAPI regenerated; no new dependency; no globals; no `any`; no identity/path leak. Linux-shim
   materialization deferred to P-S (recorded).

## Review checklist

- [ ] One **Function reconciler** for `KindFunction` (composes runtime/scheduler/gateway; no per-concern
      controllers); idempotent + convergent; registered on the ADR-0015 engine.
- [ ] **Revision stamped immutably** on spec change, artifact pinned, recorded as `CurrentRevision`
      (`apply-stamps-revision`); never mutated.
- [ ] **Materialization gate** (C3): a route is programmed only after shape-valid + running; a shape failure →
      `ShapeValid:False` + **no route** + not Ready (`shape-invalid-blocks-ready`).
- [ ] **Provision + converge** to the **effective** desired count (honoring the activator's wake `Phase`, not
      just `spec.replicas`): `wake-provisions-scaled-to-zero` proves a `MinReplicas:0`+`Phase=Deploying` function
      provisions ≥1 (and `Phase=Idle`→0); `reconcile-provisions-and-routes`/`scale-changes-replicas` converge;
      `delete-reclaims` stops sandboxes + drops the route.
- [ ] **Route programming is full-table** (`gateway.ProgramRoutes` is replace-all): the reconciler programs
      **all** Ready functions' routes, never just its own — `routes-preserved-across-functions` proves no clobber.
- [ ] **`Endpoints` provider** implements `activator.Endpoints` and resolves a ready function's upstream
      (`endpoints-resolves-ready-upstream`) — the ADR-0016 seam closed (and the wake path works end-to-end with
      the effective-replica fix).
- [ ] F13 `Function`/`Revision` fields added; roundtrip + OpenAPI green. `New(Deps)`, ctx-first, `api/fault`,
      `slog`, no globals, **no `any`**, **no new dep**; shim/materialization deferral recorded; no identity
      leak; every Scenario a named passing test.

## Consequences

- (+) The V1 **lifecycle spine exists**: apply→Revision→schedule→provision→shape-gate→route→Ready, the
  exit-criterion's "deployed → invokable," and the **critical-path keystone** that unblocks eventing (P-Q).
- (+) **The activator loop closes**: this reconciler is the production `activator.Endpoints`, so scale-to-zero
  wake→forward now has a real upstream resolver (ADR-0016's deferred seam).
- (+) **No new dependency**; the process driver makes the whole lifecycle testable in pure-Go `just ci`.
- (−) The **real JS/Python shim + materialization + source-artifact build** are deferred to the Linux/e2e lane
  (P-S) — V1 proves the control loop, not the language runtimes end-to-end. Bounded, sequenced, recorded.
- (−) A **big reconciler** (the integrative keystone) — mitigated by composing the existing ports (it owns
  orchestration, not their internals) and by the scenario tests pinning each step.
- (note) **Roadmap build edges**: P-M's real edges are `ADR-0003`/`ADR-0006`/`ADR-0015`/`ADR-0011`/`ADR-0013`/
  `ADR-0017`/`ADR-0016` (it imports all those ports). The Step-6 reconcile records these.

## Open questions

| Question | Where it gets answered |
|---|---|
| Real shim materialization (`handle`/`new()` load-time resolution) + curated runtime images | **P-S** (Linux/e2e lane) + the runtime-images follow-up |
| CloudEvents normalization transport (HTTP/timer trigger capture → handler) | **P-Q** (eventing core) |
| Source-artifact build/layer/cosign-verify pipeline | a **V2** build pipeline |
| Response streaming escape hatch to the request→event→response model | a follow-up (the gateway already streams) |
| Concurrency/RPS autoscaling (beyond manual replicas + scale-to-zero) | **V3** |

## References

- [blueprint.md](../../blueprint.md) — "Function" (CloudEvents handler, curated runtimes, source-artifact
  deploys, the shape, the 3-stage shape validation, materialization gates Ready), "Revision" (immutable,
  artifact pinned), "Function runtime".
- [ADR-0015](0015-controller-engine.md) · [ADR-0011](0011-runtime-sandbox-port.md) ·
  [ADR-0017](0017-scheduler-placement-port.md) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) ·
  [ADR-0016](0016-activator-scale-to-zero.md) — the ports this reconciler composes (+ the `Endpoints` it provides).
