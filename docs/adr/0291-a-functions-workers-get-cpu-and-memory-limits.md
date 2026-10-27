# ADR-0291: A Function's workers get CPU and memory limits through the runtime port

- **Status**: Proposed (2026-10-27)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: runtime, limits
- **Realizes**: [FEAT-0016/F206](../feat/0016-feat-workload-limits.md) (Function CPU and memory limits)
- **Answers**: the decider's refinement of 2026-10-27: `spec.resources` on a Function, a node default
  (`functions.defaultResources`, 1 CPU and 512Mi), enforced behind the runtime port so a driver can be swapped,
  `resources` with `pooling.worker` refused at admission and blocked by the reconciler; #1313
- **Relates to**: [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (F207, the storage half, split
  from this ADR) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) and
  [ADR-0233](0233-sql-access-from-functions.md) (`ResourceSpec`) · [ADR-0044](0044-worker-pooling-threads.md),
  [ADR-0046](0046-pooling-placement-policy.md) (pools) · [ADR-0266](0266-volume-sizes-with-units.md) (`ByteSize`) ·
  [ADR-0018](0018-api-server-authn-rbac-admission.md) (V2 quotas)

## Context & Need

A Function has no CPU or memory field: `internal/function/function.go:3387` builds its worker with no `runtime.Limits`,
so one Function can take a node's CPU and memory. Lake and SQLDatabase engines get theirs from `spec.resources`
through the provider's private `limitsFrom` (`internal/provider/runtime.go:394`), which containerd applies as cgroup
limits (`internal/runtime/containerd/containerd_linux.go:1231-1236`); `SQLDatabase.Validate` checks those fields inline
(`api/types/v1alpha1/sqldatabase.go:52-60`), with another size grammar than `ByteSize`. This ADR sizes a Function's
workers, with one check and one mapping for every `ResourceSpec`, behind the runtime port so another driver can enforce
the same limits.

## Scenarios

- **scenario: function-limits-applied**: Given a Function with `resources.cpu: "0.5"` and `resources.memory: 256Mi` on
  containerd, When its worker starts, Then its cgroup allows half a CPU and 256 MiB, and a worker past the memory is
  OOM-killed and restarted.
- **scenario: function-default-limits**: Given `functions.defaultResources` of `cpu: "1"` and `memory: 512Mi`, When a
  solo Function without `resources` starts a worker, Then the worker gets those limits.
- **scenario: invalid-resources-refused**: Given `resources.cpu: "lots"`, or `resources` together with
  `pooling.worker`, When the Function is applied, Then admission refuses it with 422 and names the field.
- **scenario: past-admission-blocked**: Given a Function written past admission, such as by a restore, with an
  unparsable `resources` or with `resources` and `pooling.worker`, When the reconciler converges it, Then no worker
  starts and the Function is `Failed` with reason `ResourcesInvalid` or `ResourcesWithPooling` (see the workaround).
- **scenario: limits-not-enforced-logged**: Given the process runtime, When a Function with limits starts a worker,
  Then the worker runs and the reconciler logs once that its limits are not enforced.

## Scope

**In**: `FunctionSpec.Resources` and its default; the runtime port's limits contract and its `LimitEnforcer`
capability; one `ResourceSpec` check and one limits mapping for Function, Lake and SQLDatabase; admission and the
reconciler's block.
**Out**: storage caps (ADR-0292); per-namespace CPU and memory budgets (V2, F208, keyed `compute`); pool workers, which
keep no CPU or memory limit and a pool-wide heap cap (`FUNCD_POOL_MAX_OLD_MB`).

## Constraints & Decision drivers

- One check and one mapping for every `ResourceSpec`; the kernel enforces, funcd only parses and validates.
- Every limit sits behind a port with a swappable driver, as `StopGracer` reports a driver's grace.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. `resources` enforced by the runtime driver, behind an optional `LimitEnforcer` capability** | Reuses `ResourceSpec` and containerd's limits; a new runtime is a new driver | The process driver enforces nothing | **Chosen** |
| B. A limits port of its own, apart from the runtime | Limits swap without the runtime | Each runtime applies limits at creation its own way (an OCI spec, a VM's size); a second port would mirror the first | Rejected |
| C. `resources` on a pooled Function too | One rule for all | The pool's heap cap is pool-wide (`FUNCD_POOL_MAX_OLD_MB`); a per-Function cap needs a shim change | Rejected for now |

## Decision

1. **The port.** `runtime.WorkerSpec.Limits` is the contract and the driver enforces it. A driver that does
   implements the NEW capability `runtime.LimitEnforcer`, as `StopGracer` reports a grace
   (`internal/runtime/runtime.go:183`): containerd does, by cgroups. Under a driver without it, the Function
   reconciler and the provider runtime log once per Function or engine that its limits are not enforced. Another
   driver, a microVM's sizes or a cgroup for the process driver, plugs in by implementing it.
2. **One check, one mapping.** `FunctionSpec.Resources` is the `ResourceSpec` of Lake and SQLDatabase.
   `ResourceSpec.Validate` (NEW) is the one check, a finite `cpu` of at least 0.01 and a `memory` in `ByteSize`'s
   grammar (ADR-0266), which SQLDatabase, Lake and Function call. `runtime.LimitsFrom` (NEW, the provider's
   `limitsFrom` moved, on `ByteSize`) is the one mapping, for the provider runtime and the Function reconciler.
3. **The default.** `functions.defaultResources` (NEW key, `cpu: "1"`, `memory: 512Mi`) fills each field a solo
   Function leaves empty; cleared, it leaves them unlimited.
4. **Pools and the second layer.** Admission refuses `resources` with `pooling.worker` (422), through the one predicate
   `FunctionSpec.PooledWithResources` (NEW) that the reconciler asks too. The reconciler blocks a Function written past
   admission, such as by a restore, before any placement: no worker, phase `Failed`, reason
   `ResourcesWithPooling` or, for a value that does not parse, `ResourcesInvalid`, as `VolumeInvalid` does
   (`internal/function/volumes.go:105`).

## Design model

None: no protocol runs between actors. Admission and the reconciler agree by construction, both calling
`ResourceSpec.Validate` and `FunctionSpec.PooledWithResources`; the kernel enforces the limits.

## Temporary workarounds

- The store refuses a status write for an object that fails `Validate` (#1317), so a Function blocked by Decision 4
  keeps its last phase: the reconciler returns the error, which names the field, and starts no worker. Exit: #1317
  fixed, then `TestScenarioPastAdmissionBlocked` asserts the phase and the reason.

## Contracts

```go
// Package runtime (internal/runtime), NEW: the capability of a driver that enforces WorkerSpec.Limits.
type LimitEnforcer interface{ EnforcesLimits() bool }
func LimitsFrom(cpu, memory string) Limits // the provider's limitsFrom, moved

// Package v1alpha1 (api/types/v1alpha1), NEW.
func (r ResourceSpec) Validate(op, path string) error
func (s *FunctionSpec) PooledWithResources() bool // pooling.worker together with resources
// FunctionSpec.Resources ResourceSpec `json:"resources,omitzero"` (an unsized Function renders no resources: {})

// Package config (internal/platform/config), NEW: functions.defaultResources; each key has an env override, as every
// key does.
//	Functions.DefaultResources.CPU    string `json:"cpu" env:"FUNCD_FUNCTIONS_DEFAULT_CPU"`
//	Functions.DefaultResources.Memory string `json:"memory" env:"FUNCD_FUNCTIONS_DEFAULT_MEMORY"`
func (c Config) FunctionDefaultResources() (v1.ResourceSpec, error) // checked with ResourceSpec.Validate
```

| Consumes | Exposes |
|---|---|
| `functions.defaultResources` (NEW); each Function's `resources` | A worker's limits by its driver; 422 at admission; `ResourcesInvalid`, `ResourcesWithPooling` |

## Implementation plan

- `api/types/v1alpha1`: `FunctionSpec.Resources`, `ResourceSpec.Validate` (SQLDatabase's inline checks go; the
  `ResourceSpec` comment, `lake.go:84-86`, is rewritten); the OpenAPI spec and SDK types regenerated.
- `internal/runtime`: `LimitEnforcer` (containerd implements it; a `runtimecontract` case), `LimitsFrom` (the provider
  calls it); `internal/platform/config`: `Functions.DefaultResources`; `internal/function`: the worker's limits, the
  default, admission and the two reasons.
- Tests, one per scenario: `TestScenarioFunctionLimitsApplied` (the containerd spec carries the limits),
  `…FunctionDefaultLimits`, `…InvalidResourcesRefused`, `…PastAdmissionBlocked`, `…LimitsNotEnforcedLogged`; on
  real containerd, the duckdb Lima lane reads an unsized worker's cgroup (the default) and OOM-kills a process in it
  that passes 512 MiB while the worker keeps serving.
- Docs: the Function reference, `funcdconfig.yaml`'s reference, the changelog's note on the default.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean;
  `scripts/agent/gate.sh` passes; `just check-hygiene check-specs` passes.

## Review checklist

- [ ] No `ResourceSpec` check outside `Validate`, no limits mapping outside `LimitsFrom`.
- [ ] A solo worker's limits come from `resources`, else the default; only a `LimitEnforcer` driver is trusted, and a
      driver without it is logged once per Function or engine.
- [ ] Admission refuses a bad `cpu` or `memory`, and `resources` with `pooling.worker`; the reconciler blocks either
      one written past admission; every scenario has its named test.

## Consequences

- **Positive**: a solo Function can no longer take a node's CPU or memory; the next runtime enforces the same limits by
  implementing `LimitEnforcer`.
- **Negative (accepted)**: existing solo Functions get the default 1 CPU and 512 MiB at upgrade (the changelog says
  so); pool workers stay unlimited; a Lake or SQLDatabase whose `resources` was ignored until now gets 422 at its next
  apply if it does not parse, and a stored one takes no status write until it is applied again (#1317); `funcdctl dev`
  enforces no CPU or memory limit.

## Open questions

- A pool's per-Function heap cap: a TypeScript shim change; its own ADR.

## References

- The decider's refinement of 2026-10-27; FEAT-0016; #1313, tracker #1315.
- `internal/runtime/runtime.go:183`, `internal/provider/runtime.go:394`, `api/types/v1alpha1/sqldatabase.go:52-60`,
  `internal/pooling/pooling.go:124` (opt-in pools).
