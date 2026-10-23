# ADR-0264: A worker sees its own cgroup

- **Status**: Proposed
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, isolation, cgroups, lake
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port) (#1185)
- **Supersedes (in part)**: [ADR-0011](0011-runtime-sandbox-port.md), only its conservative OCI list (lines 108-109, 205-207
  and the checklist's 374-375): a worker also gets a cgroup namespace and a read-only cgroup mount. The rest stands.
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the managed
  containerd and crun) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md),
  [ADR-0087](0087-add-on-provider-runtime.md) (`spec.resources` sizes an engine) ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) (the duckdb lane's spill step) ·
  [ADR-0265](0265-the-lake-engine-takes-tuning-keys-from-an-allow-list.md) (the Lake's tuning keys)

## Context & Need

A containerd worker (ADR-0011) runs with its memory and CPU limits applied (`WithMemoryLimit`, `WithCPUCFS`), but it
cannot see them. funcd's spec is built on containerd's defaults, which have no cgroup namespace and mount no cgroup
filesystem, so `/sys/fs/cgroup` is empty in the worker (`ociOpts`, `internal/runtime/containerd/containerd_linux.go:1181-1206`).
A runtime that sizes itself from its limits then sizes itself to the host.

Today only provider engines carry limits, from a Lake's or a SQLDatabase's `spec.resources`
(`internal/provider/runtime.go:237`). In the duckdb lane, a Lake at `memory: 256Mi` and `cpu: "2"` ran DuckDB with
`memory_limit` 3.0 GiB and 4 threads, the VM's totals. A query larger than its memory then got the engine killed
instead of spilling (#1185). Under Docker, which mounts a cgroup view, the same DuckDB 2.0.0 reports 204.7 MiB and
2 threads, with or without a cgroup namespace.

## Scenarios

- `scenario: engine-sizes-to-its-limit`: Given a Lake with `spec.resources` `memory: 256Mi` and `cpu: "2"` and no tuning
  keys, When its engine starts, Then DuckDB's `memory_limit` is 204.7 MiB (80% of 256 MiB) and its `threads` is 2.
- `scenario: engine-spills-under-its-limit`: Given that Lake, When a query needs more memory than its engine has, Then
  the engine spills to its temp directory, returns the rows and keeps serving.
- `scenario: worker-sees-only-its-cgroup`: Given any containerd worker, Then `/proc/self/cgroup` in it reads `0::/`,
  `/sys/fs/cgroup/memory.max` reads its own limit (`max` without one), and a write under `/sys/fs/cgroup` fails.

## Scope

**In**: the containerd driver's OCI spec; its Linux test; the duckdb lane's spill step, which runs on the engine again.

**Out**: resources on a Function (`FunctionSpec` has none); the process driver, which has no cgroups; a worker
writing its own cgroup knobs.

## Constraints & Decision drivers

- Isolation must not widen (ADR-0011): a worker must not see or change another worker's cgroup or the host's.
- No added capability; the view is read-only.
- One mechanism for every runtime; no runtime-specific code.
- funcd runs on cgroup v2 hosts: ADR-0230's lane cleanup already relies on v2's `cgroup.kill`.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A cgroup namespace and a read-only cgroup mount for every worker | every runtime sizes itself; the worker sees only its own cgroup; Docker's and containerd CRI's default on cgroup v2 | changes ADR-0011's OCI list | chosen by the decider |
| The mount without a cgroup namespace | one spec entry fewer | the worker sees the host's whole cgroup tree | rejected |
| Only the Lake engine learns its limit, from a variable the provider runtime sets | no driver change | every other runtime stays blind; a new environment contract | rejected by the decider |
| The Lake's author sets `DUCKDB_MEMORY_LIMIT` | already in ADR-0087 | manual, easy to get wrong, DuckDB only | rejected by the decider; kept as tuning (ADR-0265) |

## Decision

1. **Every containerd worker gets a cgroup namespace and its own cgroup, read-only, at `/sys/fs/cgroup`.** Inside the
   worker, `memory.max` and `cpu.max` read its limits, or `max` when it has none.
2. **One `SpecOpt`, `withCgroupView`, in `ociOpts`.** No other driver change; the process driver is unchanged.
3. **The duckdb lane's spill step runs on the engine again.** The 256Mi Lake binds no tuning keys; the lane reads
   its engine's `memory_limit` and `threads`, and a query larger than its memory, sent through `lake-reader`, returns
   its rows. ADR-0263's in-container spill probe goes.

## Temporary workarounds

None.

## Contracts

**`internal/runtime/containerd`** (`//go:build linux`):

```go
// withCgroupView gives a worker a cgroup namespace and its own cgroup, read-only, at /sys/fs/cgroup, so a runtime in it
// reads its memory and CPU limits (ADR-0264). It adds each entry once.
func withCgroupView(ctx context.Context, client oci.Client, c *containers.Container, s *oci.Spec) error
```

The spec gains these entries:

```yaml
linux:
  namespaces:
    - type: cgroup
mounts:
  - destination: /sys/fs/cgroup
    type: cgroup
    source: cgroup
    options:
      - ro
      - nosuid
      - noexec
      - nodev
      - relatime
```

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| `runtime.WorkerSpec.Limits`, unchanged | `/sys/fs/cgroup` in the worker, read-only, rooted at its own cgroup |
| crun 1.28 (`internal/runtime/provision/provision.go:36`), which supports the cgroup namespace | |

## Implementation plan

1. `internal/runtime/containerd/containerd_linux.go`: `withCgroupView`, applied in `ociOpts`.
2. `internal/runtime/containerd/cgroupview_linux_test.go`: `TestScenarioWorkerSeesOnlyItsCgroup`.
3. The duckdb lane: `e2e/fixtures/lake-256mi.yaml` binds no ConfigMap; the `spill-under-256mi` case asserts the
   engine's settings, sends the query through `lake-reader`, and checks the worker's cgroup view;
   `e2e/fixtures/spill-probe.py` and its `scripts/lanes.yaml` entry go.
4. At acceptance, the `Superseded in part by ADR-0264` back-link on ADR-0011 and the F12 row's entry.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `engine-sizes-to-its-limit` | the duckdb lane: the 256Mi engine reports `memory_limit` 204.7 MiB and `threads` 2 |
| `engine-spills-under-its-limit` | the duckdb lane: the 20M-row window query through `lake-reader` returns `[[19999999]]`, and the Lake stays Ready |
| `worker-sees-only-its-cgroup` | `TestScenarioWorkerSeesOnlyItsCgroup`: on a bare spec, the namespace and the read-only mount, each once; the duckdb lane: in the engine, `/proc/self/cgroup` is `0::/`, `memory.max` is 268435456, and a write to it fails |

**Definition of done**: `scripts/agent/gate.sh` passes, its Linux tests included; `just lima-example-all` passes, so
every runtime runs under the new spec.

## Review checklist

- [ ] `ociOpts` applies `withCgroupView` to every worker: a `cgroup` namespace, and a `cgroup` mount at `/sys/fs/cgroup`
  with `ro`, `nosuid`, `noexec`, `nodev` and `relatime`.
- [ ] No other OCI change, and the process driver is unchanged.
- [ ] `TestScenarioWorkerSeesOnlyItsCgroup` passes on Linux; the duckdb lane asserts the engine's `memory_limit`,
  `threads` and cgroup view, and spills on the engine; every lane passes.
- [ ] The spill probe fixture and its stage entry are gone.

## Consequences

- **(+)** A provider engine sizes itself to its `spec.resources`; a Lake spills instead of being killed; a future limit
  on a Function is visible to its runtime; a worker sees no other cgroup, as before.
- **(−)** One more namespace per worker; ADR-0011's OCI list grows by two entries.
- **Risk**: a runtime that read the host's totals now reads its limit; only provider engines have one today.

## Open questions

None.

## References

- Issue #1185, under tracker #1032.
- The duckdb lane, 2026-10-23: at 256Mi and `cpu: "2"`, DuckDB reported 3.0 GiB and 4 threads, and the engine's cgroup
  held `memory.max` 268435456.
- Docker with `--memory 256m --cpus 2`: DuckDB 2.0.0 reported 204.7 MiB and 2 threads, under `--cgroupns=private` and
  `--cgroupns=host` alike.
- containerd v2.4.2 `pkg/oci/mounts.go` (the default mounts) and `pkg/oci/spec.go` (the default namespaces).
- The OCI runtime specification: the `cgroup` namespace and mount.
