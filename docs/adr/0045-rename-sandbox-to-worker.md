# ADR-0045: Compute vocabulary — `sandbox` → `worker` (the process), `Worker` → `worker node` (the compute node)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Implemented 2026-06-16** — review **pass** (re-review after folding the `DeleteWorker` Major + the
  REST-path/operationId + blueprint-layout Minors), see docs/reviews/adr-0045-implementation-claude-opus-4-8.md; DoD 7/7,
  both grep gates clean, behavior-preserving (`go test ./...` green, unchanged assertions). **Reviewing 2026-06-16** —
  implemented: both axes renamed across code + OpenAPI regen + blueprint + reports + the three supersession back-links +
  F12/F03/F22/F09 re-pointed. **Accepted 2026-06-16** — judge (2 passes) folded: B1 → the **full-swap** resolution (rename both
  axes — `sandbox`→`worker` *and* `Worker`→`WorkerNode`, per the decider, so neither half reintroduces the homonym); B2 →
  Contracts re-anchored to the live `runtime.go` (`Instance` kept with `IP`+`Port`); the "tests unchanged" claim corrected
  to **behavior-preserving symbol-rename**; the file enumeration completed (all 24 axis-A files + the grep gates made
  authoritative); **F22** added to the supersession bookkeeping (ADR-0003 realizes F03 *and* F22) + F03's literal "Worker"
  prose re-pointed.)
- **Deciders**: green-0-rabbit
- **Tags**: terminology, runtime, scheduler, resource-model, naming, refactor, multi-node
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port — the
  originating rename; this ADR also refines [F03](../feat/0000-feat-v1.md) (the `Worker` resource kind) and
  [F09](../feat/0000-feat-v1.md) (scheduler placement), reconciled via the supersessions below)
- **Supersedes** (naming only — each prior ADR's *design* is carried forward unchanged; this ADR changes only names):
  [ADR-0011](0011-runtime-sandbox-port.md) (`SandboxSpec` → `WorkerSpec`) · [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `Worker` resource kind → `WorkerNode`) · [ADR-0017](0017-scheduler-placement-port.md) (`Placement.Worker` →
  `Placement.WorkerNode`)
- **Relates to**: [ADR-0044](0044-worker-pooling-threads.md) (a "pool" is **a worker hosting many functions**; `pool.mjs` /
  `FUNCD_POOL_MANIFEST` untouched), [ADR-0030](0030-function-execution-runtime-shim-node.md)/[ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0020](0020-function-contract-lifecycle.md)/[ADR-0028](0028-platform-control-plane-wiring.md)
  (consume the renamed names — their *code* is renamed; their frozen *prose* keeps the old names as historical record,
  mapped old→new by this ADR), `blueprint.md`. Pooling placement (the next ADR) builds on this vocabulary.

## Context & Need

funcd's compute vocabulary is inconsistent and on a collision course. The process that runs a function is a **sandbox**
(`runtime.SandboxSpec`, ADR-0011); a compute node is a **Worker** (`v1alpha1.Worker`, ADR-0003; `scheduler.Placement.Worker`,
ADR-0017; the `worker.proto` / `worker/` node-agent in the blueprint). As worker pooling (ADR-0044) and multi-node arrive,
the natural unit names are **worker** = the process running 1+ functions, and **worker node** = a compute node that
schedules workers (**K3s-style — many worker nodes may run on one physical machine**; a worker node is *not* the machine).
But "worker" is already taken for the node, so renaming the process to "worker" while the node stays "Worker" would put
`runtime.WorkerSpec` (process) one import from `v1alpha1.Worker` (node) — the exact near-homonym the project's conventions
forbid. The fix is to swap **both axes at once**. This ADR sets the canonical vocabulary and renames it across code +
blueprint. **Behavior is unchanged.**

The vocabulary, decided:

| concept | was | now |
|---|---|---|
| the deployed unit | function | **function** (unchanged) |
| the process running 1+ functions | sandbox | **worker** |
| a compute node that schedules workers (many per machine, K3s-style; multi-node) | Worker | **worker node** |
| a worker hosting many functions (ADR-0044) | pool | **pool** — a *kind of* worker (`pool.mjs` unchanged) |

## Scenarios

- **scenario: worker-spec-renamed** — *given* the runtime port, *when* a function replica is provisioned, *then*
  `runtime.Create` takes a `runtime.WorkerSpec` (was `SandboxSpec`) and the worker runs — behavior identical to ADR-0011.
- **scenario: worker-node-kind-renamed** — *given* the resource + scheduler layer, *then* the kind is `WorkerNode` (was
  `Worker`), `KindWorkerNode = "WorkerNode"`, `scheduler.Placement.WorkerNode` (was `.Worker`), the controlplane CRUD is
  `*WorkerNode`, and the OpenAPI is regenerated — the existing resource/scheduler/controlplane tests pass after only the
  mechanical symbol rename (no assertion/logic change).
- **scenario: no-homonym-left** — *given* the renamed tree, *then* **no production Go identifier names the execution unit
  "sandbox"** AND **no production identifier names a compute node "Worker"** (the unit is `worker`, the node is
  `WorkerNode`); isolation-mechanism nouns (crun container, wasm sandbox, microVM) are excepted; both grep gates are clean.
- **scenario: blueprint-vocab** — *given* `blueprint.md`, *then* it reads **worker** (process) / **worker node** (compute
  node, many-per-machine) / **function** (unit); no stale "Worker = the node component" or "sandbox = the process" remains.

## Scope

**In:** the two-axis rename across code + the blueprint, an OpenAPI regen, and three naming supersessions.
- *Axis A (process):* `runtime.SandboxSpec` → `WorkerSpec` + the runtime port's "sandbox" identifiers/comments; the
  unit-"sandbox" sweep across **all 24 files** — `internal/runtime` (+ `runtimecontract`), `internal/function`,
  `internal/activator`, `internal/bench` (incl. `Report.PerSandboxMB`), `internal/secrets`, `internal/gateway`, `pkg/funcd`,
  `tests/e2e`, `api/types/v1alpha1/function.go` ("running sandbox count"), `cmd/**`. **The two grep gates (step 9) are
  authoritative over the file list — the named dirs are illustrative; sweep every unit-sense `sandbox` outside the
  isolation-noun exception.**
- *Axis B (node):* `v1alpha1.Worker`/`WorkerStatus` → `WorkerNode`/`WorkerNodeStatus`; `KindWorker="Worker"` →
  `KindWorkerNode="WorkerNode"`; `scheduler.Placement.Worker` → `.WorkerNode`; controlplane `*Worker` → `*WorkerNode`;
  `internal/scheduler/singlenode` + `schedulercontract` (~13 files); regenerate `api/openapi`.
- *Blueprint:* "Worker" component → "worker node"; "sandbox" (unit) → "worker"; the future `worker.proto` / `worker/`
  layout entries → `workernode.*`.

**Out:** any behavior/contract-shape change (pure rename); **isolation-mechanism** nouns (crun container, wasm sandbox,
microVM — they name *isolation*, not the unit); ADR-0044's `pool.mjs` / `FUNCD_POOL_MANIFEST`; the **multi-node worker-node
scheduling model** itself (its own V2+ ADR — this only fixes the name; `worker.proto`/`worker/` don't exist yet, only the
blueprint layout mentions them); editing the frozen *prose* of consuming ADRs (0020/0028/0030/0032 keep the old names as
historical record, mapped here).

## Constraints & Decision drivers

- **One name per concept** — the project's named cross-document defect is the two-letter near-homonym (`store`/`storage`);
  `sandbox`/`worker` *and* `Worker`(node)/`worker`(process) are both that. An LLM-implemented codebase punishes it hardest.
- **Swap both axes together** — renaming only the process reintroduces the collision (judge B1); both, or neither.
- **Freeze discipline** — the renamed types are frozen contracts (ADR-0011/0003/0017); they change via **supersession**, not edit.
- **Behavior-preserving** — purely mechanical; the unchanged runtime/scheduler/controlplane/bench tests passing (green
  `just ci`) is the proof.
- **Don't over-reach** — isolation-mechanism nouns and ADR-0044's `pool.mjs` stay; only the compute-vocabulary concepts move.

## Alternatives considered

- **Keep "sandbox" (process) + "Worker" (node)** — rejected: leaves the `sandbox`/`worker` homonym; the decider's
  vocabulary is *worker = the process*.
- **Rename only `sandbox`→`worker`, keep `Worker`=node** — rejected (judge B1): `runtime.WorkerSpec` (process) sits one
  import from `v1alpha1.Worker` (node) — the worst homonym, exactly what this ADR exists to remove.
- **A non-"worker" word for the process (e.g. `runner`), keep `Worker`=node** — rejected by the decider: *worker = the
  process* is the chosen vocabulary; the node is the *worker node* (K3s framing).
- **Forward-only (leave existing code)** — rejected: the old names stay dominant; the homonym persists — worst of both.

## Decision

1. **Vocabulary.** A `function` (unit) runs in a `worker` (process, 1+ functions); workers are scheduled onto `worker node`s
   (compute nodes — many may run on one machine, K3s-style). A **pool** (ADR-0044) is a worker hosting many functions.
2. **Axis A — process.** `runtime.SandboxSpec` → `runtime.WorkerSpec`; the `runtime.Runtime` port's "sandbox"
   identifiers/comments → "worker"; `bench.Report.PerSandboxMB` → `PerWorkerMB` (JSON `perSandboxMB` → `perWorkerMB`; report
   labels "per-sandbox" → "per-worker"); the unit-"sandbox" sweep in `internal/function`/`internal/activator`/`cmd`.
   `runtime.Instance` is **kept** (it *is* a worker instance) — only its "sandbox" comments change; **all fields, incl. `IP`
   and `Port`, are unchanged**.
3. **Axis B — node.** `v1alpha1.Worker`/`WorkerStatus` → `WorkerNode`/`WorkerNodeStatus`; `KindWorker="Worker"` →
   `KindWorkerNode="WorkerNode"` (+ the scope lists); `scheduler.Placement.Worker` → `.WorkerNode`; controlplane
   `GetWorker/CreateWorker/ListWorkers/ReplaceWorker` → `…WorkerNode`; `singlenode` + `schedulercontract`; regenerate
   `api/openapi` (the kind is public). **Meaning unchanged:** a `WorkerNode` is a compute node (many per machine possible).
4. **Blueprint.** "Worker" component → "worker node"; "sandbox" (unit) → "worker"; the `worker.proto` / `worker/`
   future-layout entries → `workernode.*`; isolation prose (crun/wasm/microVM) kept.
5. **Supersession.** ADR-0011/0003/0017 each get `Superseded by ADR-0045` (naming-only; design carried forward);
   **F12/F03/F22/F09** (ADR-0003 realizes both F03 *and* F22) add ADR-0045 and **stay `implemented`**, and F03's scope prose
   that lists the literal kind "Worker" is renamed to "WorkerNode" — all at this ADR's **Implemented** stamp (the ADR-0029
   pattern).
6. **Pool naming untouched** (ADR-0044). **Behavior-preserving rename** — no signature shape, behavior, or driver change
   beyond the name. Test files that reference a renamed symbol get **only that symbol rename** (no assertion/logic change);
   green `just ci` is the acceptance proof.

## Temporary workarounds

- **Frozen ADRs keep the old names in prose** (ADR-0011 "sandbox"; ADR-0003/0017 "Worker"); they are immutable historical
  record. This ADR is the standing map: `sandbox` ≡ `worker`, `Worker`(node) ≡ `WorkerNode`. **Exit criterion:** none — the map stands.

## Contracts

**Source of truth is the *current* code** (`internal/runtime/runtime.go`, `api/types/v1alpha1`, `internal/scheduler`), **not**
the ADR-0011/0003/0017 snapshots. Every field/behavior is unchanged — only names move.

```go
// Axis A — internal/runtime (was ADR-0011; renamed here)
type WorkerSpec struct { // was SandboxSpec — fields unchanged
	Namespace v1alpha1.NamespaceName; Name v1alpha1.ObjectName; Replica int
	Image string; Command []string; Env map[string]string; Mounts []Mount; Limits Limits; LogPath string
}
type Instance struct { // KEPT (a worker instance) — fields unchanged, incl. IP + Port
	ID InstanceID; Namespace v1alpha1.NamespaceName; Name v1alpha1.ObjectName; Replica int
	PID int; State State; IP string; Port int; CreatedAt time.Time
}
// Runtime.Create(ctx, WorkerSpec) (Instance, error) — was Create(…, SandboxSpec); the rest of the port is unchanged.

// Axis B — api/types/v1alpha1 + internal/scheduler (was ADR-0003 / ADR-0017)
type WorkerNode struct { TypeMeta; ObjectMeta; Status WorkerNodeStatus } // was Worker/WorkerStatus — a compute node
const KindWorkerNode Kind = "WorkerNode"                                  // was KindWorker = "Worker"
type Placement struct { WorkerNode v1.ObjectName }                       // was .Worker — the node a function lands on
// controlplane: GetWorkerNode/CreateWorkerNode/ListWorkerNodes/ReplaceWorkerNode — was *Worker
```

**Rename map (load-bearing surfaces; the implementer greps the rest, disambiguating by axis):**

| old | new | axis |
|---|---|---|
| `runtime.SandboxSpec` · `Create(…, SandboxSpec)` | `runtime.WorkerSpec` · `Create(…, WorkerSpec)` | process |
| `bench.Report.PerSandboxMB` · `perSandboxMB` · "per-sandbox" | `PerWorkerMB` · `perWorkerMB` · "per-worker" | process |
| `v1alpha1.Worker` · `WorkerStatus` | `v1alpha1.WorkerNode` · `WorkerNodeStatus` | node |
| `KindWorker = "Worker"` | `KindWorkerNode = "WorkerNode"` | node |
| `scheduler.Placement.Worker` | `.WorkerNode` | node |
| controlplane `*Worker(...)` | `*WorkerNode(...)` (+ OpenAPI regen) | node |
| blueprint "Worker" component · `worker.proto` · `worker/` | "worker node" · `workernode.proto` · `workernode/` | node |

`PerWorkerMB` = **one single-tenant worker's RSS** (today's `PerSandboxMB`, `bench.go:184`) — distinct from `PerFunctionMB`
(the marginal slope) and ADR-0044's `PooledPerFunctionMB` (a pool worker's RSS ÷ K). **Dependencies & I/O:** none new — a
rename + an OpenAPI regen; same behavior under new names.

## Implementation plan

1. **Axis A · runtime** — `SandboxSpec`→`WorkerSpec` (type + every `Create` call + process & containerd/crun drivers + the
   `containerd_other.go` non-Linux stub + `runtimecontract/contract.go` + doc-comments); `Instance` "sandbox" comments →
   "worker" (fields untouched).
2. **Axis A · sweep** — `internal/function`, `internal/activator`, `internal/bench` (`PerSandboxMB`→`PerWorkerMB` + JSON key
   + markdown labels), `cmd/**`: unit-"sandbox" → "worker"; **leave** isolation-mechanism nouns.
3. **Axis B · resource** — `v1alpha1.Worker`/`WorkerStatus`→`WorkerNode`/`WorkerNodeStatus`, `KindWorker`→`KindWorkerNode`
   (+ `metadata.go` scope lists), the `status_test`/`types_test`/`rbac_test` references.
4. **Axis B · scheduler + controlplane** — `Placement.Worker`→`.WorkerNode`, `schedulercontract`, `singlenode`(+test);
   controlplane `handlers`/`routes_rest`/`stubs`/`controlplane.go` `*Worker`→`*WorkerNode`.
5. **OpenAPI** — `just generate` (the `Worker` kind → `WorkerNode`, and the `WorkerStatus` schema → `WorkerNodeStatus`, in `api/openapi/funcd.v1alpha1.yaml`).
6. **Blueprint** — "Worker" component → "worker node"; "sandbox" (unit) → "worker"; layout `worker.proto`/`worker/` →
   `workernode.*`; keep crun/wasm/microVM prose.
7. **Reports** — regenerate `docs/reports/report.{md,json}` + refresh `bench-overview.md` so "per-sandbox" → "per-worker".
8. **Supersede** — add `Superseded by ADR-0045` to ADR-0011/0003/0017; add ADR-0045 to F12/F03/F22/F09 and rename the literal
   "Worker" in F03's scope prose → "WorkerNode" (all at the Implemented stamp).
9. **Verify** — `go build ./...` · `go test ./...` (the existing runtime/scheduler/controlplane/bench tests pass after
   **only the mechanical symbol rename** — the symbol-referencing test files are `internal/bench/bench_test.go`,
   `internal/function/shim_test.go`, `internal/runtime/runtimecontract/contract.go`, `internal/scheduler/singlenode/singlenode_test.go`,
   `internal/auth/rbac/rbac_test.go`, `api/types/v1alpha1/{status_test,types_test}.go`; no assertion/logic change) ·
   `golangci-lint` · `go mod verify`; **two grep gates** — no production identifier names the unit "sandbox", none names a
   compute node "Worker" (now "WorkerNode"); identity grep; `just ci` green.
10. **Definition of done** — both axes renamed; OpenAPI regenerated; the blueprint vocab is consistent; ADR-0011/0003/0017
    superseded + F12/F03/F09/F22 re-pointed; **behavior identical** (green ci; tests carry only the symbol rename).

## Review checklist

- [ ] Axis A: `SandboxSpec`→`WorkerSpec`; `Create` takes `WorkerSpec`; `Instance` kept with `IP`+`Port`; drivers + stub compile (`worker-spec-renamed`).
- [ ] Axis B: `v1alpha1.Worker`→`WorkerNode`, `KindWorker`→`KindWorkerNode="WorkerNode"`, `Placement.Worker`→`.WorkerNode`, controlplane `*WorkerNode`, OpenAPI regenerated (`worker-node-kind-renamed`).
- [ ] No production identifier names the unit "sandbox" **or** a node "Worker"; both grep gates clean (`no-homonym-left`); isolation nouns excepted.
- [ ] `bench.PerSandboxMB`→`PerWorkerMB` + JSON `perWorkerMB` + committed `report`/`bench-overview` labels read "per-worker".
- [ ] Blueprint: worker (process) / worker node (compute node, many-per-machine) / function (unit); no stale Worker-component or sandbox-process (`blueprint-vocab`).
- [ ] ADR-0011/0003/0017 carry `Superseded by ADR-0045`; F12/F03/F09 list ADR-0045 and stay `implemented`.
- [ ] Behavior unchanged — pre-existing tests pass after **only the mechanical symbol rename** (no assertion/logic change); `just ci` green; no new dep; no identity leak.

## Consequences

- (+) **Coherent compute vocabulary** (`function` ⊂ `worker` ⊂ `worker node`) for pooling placement (the next ADR) and
  multi-node; **both** near-homonyms killed at once.
- (+) **Matches the K3s-style model** the decider intends — many worker nodes per machine, workers scheduled onto them.
- (−) **A two-axis ~37-file rename**, a regenerated **public OpenAPI** (the `Worker` kind → `WorkerNode`), and **three
  superseded ADRs** — a one-time, behavior-preserving cost; cheap **now** (the `WorkerNode` kind is a V2 multi-node stub,
  funcd is pre-release).
- (−) **"sandbox" loses its FaaS-isolation connotation** in prose; mitigated by keeping isolation-mechanism names and
  stating worker isolation where it matters.
- (−) **Frozen ADRs keep the old names** in their text; a reader maps via this ADR (`sandbox`≡`worker`, `Worker`≡`WorkerNode`).

## Open questions

- **`worker.proto` / `worker/`** don't exist yet (blueprint future-layout only) — this ADR renames the *layout entries*; the
  actual proto/package land with the multi-node ADR under the new name (`workernode.*`).

## References

- [ADR-0011](0011-runtime-sandbox-port.md) / [ADR-0003](0003-resource-model-and-api-typing.md) /
  [ADR-0017](0017-scheduler-placement-port.md) (superseded for naming) · [ADR-0044](0044-worker-pooling-threads.md) (a pool
  = a worker hosting many functions) · `blueprint.md` · the project near-homonym invariant ("never a two-letter near-homonym
  for two concepts").
