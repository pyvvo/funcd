# ADR-0264: A worker sees its own cgroup

- **Status**: Accepted (2026-10-23), under the decider's "don't wait for me" and "go ahead" of that day, on option A of
  #1185. Judged twice: round 1 found one Blocker (nothing proved the spill at 256Mi), cleared by measuring the spike in
  the duckdb lane, which moved the fixture to 1Gi (References), and three Minors, folded; round 2: accept.
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, isolation, cgroups, lake
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port) (#1185)
- **Supersedes (in part)**: [ADR-0011](0011-runtime-sandbox-port.md), only its conservative OCI list (lines 108-109, 205-207
  and the checklist's 374-375): on a cgroup v2 host, a worker also gets a cgroup namespace and a read-only cgroup mount.
  The rest stands.
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the managed
  containerd and crun) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md),
  [ADR-0087](0087-add-on-provider-runtime.md) (`spec.resources` sizes an engine) ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) (the duckdb lane's spill step) ·
  [ADR-0265](0265-the-lake-engine-takes-tuning-keys-from-an-allow-list.md) (the Lake's tuning keys)

## Context & Need

A containerd worker (ADR-0011) runs with its memory and CPU limits applied (`WithMemoryLimit`, `WithCPUCFS`), but it
cannot see them. funcd's spec is built on containerd's defaults, which have no cgroup namespace and mount no cgroup
filesystem, so `/sys/fs/cgroup` is empty in the worker (`ociOpts`, `internal/runtime/containerd/containerd_linux.go`).
A runtime that sizes itself from its limits then sizes itself to the host.

Today only provider engines carry limits, from a Lake's or a SQLDatabase's `spec.resources`
(`internal/provider/runtime.go:237`). In the duckdb lane, a Lake at `memory: 256Mi` and `cpu: "2"` ran DuckDB with
`memory_limit` 3.0 GiB and 4 threads, the VM's totals. A query larger than its memory then got the engine killed
instead of spilling (#1185). Under Docker, which mounts a cgroup view, the same DuckDB 2.0.0 reports 204.7 MiB and
2 threads.

## Scenarios

- `scenario: engine-sizes-to-its-limit`: Given a Lake with `spec.resources` `memory: 1Gi` and `cpu: "2"` and no tuning
  keys, When its engine starts, Then DuckDB's `memory_limit` is 819.1 MiB (80% of 1 GiB) and its `threads` is 2.
- `scenario: engine-spills-under-its-limit`: Given that Lake, When a query needs more memory than its engine has, Then
  the engine spills to its temp directory, returns the rows and keeps serving, with no OOM kill in its cgroup.
- `scenario: worker-sees-only-its-cgroup`: Given a containerd worker on a cgroup v2 host, Then `/proc/self/cgroup` in it
  reads `0::/`, `/sys/fs/cgroup/memory.max` and `cpu.max` read its own limits (`max` and `max 100000` without one), and
  a write under `/sys/fs/cgroup` fails.

## Scope

**In**: the containerd driver's OCI spec; its Linux test; the duckdb lane's spill step, which runs on the engine again.

**Out**: resources on a Function (`FunctionSpec` has none); the process driver, which has no cgroups; a worker
writing its own cgroup knobs; cgroup v1 hosts, whose workers stay as they are.

## Constraints & Decision drivers

- Isolation must not widen (ADR-0011): a worker must not see or change another worker's cgroup or the host's.
- No added capability; the view is read-only.
- One mechanism for every runtime; no runtime-specific code.
- As containerd's CRI does, the namespace is for cgroup v2 hosts only (containerd v2.4.2
  `internal/cri/server/container_create.go:765-771`, `helpers_linux.go:190-192`).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A cgroup namespace and a read-only cgroup mount for every worker on cgroup v2 | every runtime sizes itself; the worker sees only its own cgroup; Docker's and containerd CRI's default on v2 | changes ADR-0011's OCI list | chosen by the decider |
| The mount without a cgroup namespace | one spec entry fewer | the worker sees the host's whole cgroup tree, and a cgroup2 mount made from the host's cgroup namespace changes the host's single cgroup2 superblock (containerd v2.4.2 `internal/cri/opts/spec_linux_opts.go:85-97`) | rejected |
| Only the Lake engine learns its limit, from a variable the provider runtime sets | no driver change | every other runtime stays blind; a new environment contract | rejected by the decider |
| The Lake's author sets `DUCKDB_MEMORY_LIMIT` | already in ADR-0087 | manual, easy to get wrong, DuckDB only | rejected by the decider; kept as tuning (ADR-0265) |

## Decision

1. **On a cgroup v2 host, every containerd worker gets a cgroup namespace and its own cgroup, read-only, at
   `/sys/fs/cgroup`.** Inside the worker, `memory.max` and `cpu.max` read its limits, or `max` when it has none. On a v1
   host the spec is unchanged.
2. **One `oci.SpecOpts`, `withCgroupView`, in `ociOpts`**, applied when `cgroups.Mode()` is `cgroups.Unified`. No other
   driver change; the process driver is unchanged.
3. **The duckdb lane's spill step runs on the engine again**, on a Lake re-created at `memory: 1Gi` that binds no tuning
   keys. The lane reads its engine's `memory_limit` and `threads`, and a query larger than its memory, sent through
   `lake-reader`, returns its rows. At 256Mi and 512Mi the idle engine's own memory leaves DuckDB's 80% too little
   room (References), so the fixture is sized past that. ADR-0263's in-container spill probe goes.

## Temporary workarounds

None.

## Contracts

**`internal/runtime/containerd`** (`//go:build linux`):

```go
// withCgroupView gives a worker a cgroup namespace and its own cgroup, read-only, at /sys/fs/cgroup, so a runtime in it
// reads its memory and CPU limits (ADR-0264). It adds each entry once.
func withCgroupView(ctx context.Context, client oci.Client, c *containers.Container, s *oci.Spec) error
```

The spec gains these entries ([OCI runtime specification](https://github.com/opencontainers/runtime-spec/blob/main/config-linux.md#namespaces):
the `cgroup` namespace; the `cgroup` filesystem mount):

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
| crun 1.28 (`crunVersion`, `internal/runtime/provision/provision.go:38`), which supports the cgroup namespace | |
| `github.com/containerd/cgroups/v3`'s `Mode()`, already in the module graph | |

## Implementation plan

1. `internal/runtime/containerd/containerd_linux.go`: `withCgroupView`, applied in `ociOpts` on a cgroup v2 host.
2. `internal/runtime/containerd/cgroupview_linux_test.go`: `TestScenarioWorkerSeesOnlyItsCgroup`.
3. The duckdb lane: `e2e/fixtures/lake-1gi.yaml` (renamed from `lake-256mi.yaml`) at 1Gi binds no ConfigMap; three
   cases named after the scenarios; `e2e/fixtures/cgroup-view.py` probes a worker's view;
   `e2e/fixtures/spill-probe.py` and its `scripts/lanes.yaml` entry go.
4. At acceptance, the `Superseded in part by ADR-0264` back-link on ADR-0011 and the F12 row's entry.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `engine-sizes-to-its-limit` | the duckdb lane: the 1Gi engine reports `memory_limit` 819.1 MiB and `threads` 2 |
| `engine-spills-under-its-limit` | the duckdb lane: the 40M-row window query through `lake-reader` returns `[[39999999]]`; the Lake stays Ready and its cgroup's `oom_kill` is 0 |
| `worker-sees-only-its-cgroup` | `TestScenarioWorkerSeesOnlyItsCgroup`: on a bare spec, the namespace and the read-only mount, each once; the duckdb lane: in the engine, `/proc/self/cgroup` is `0::/`, `memory.max` 1073741824, `cpu.max` `200000 100000`, and a write fails; in `lake-reader`, with no limit, `max` and `max 100000` |

**Definition of done**: `scripts/agent/gate.sh` passes, its Linux tests included; `just lima-example-all` passes, so
every runtime runs under the new spec.

## Review checklist

- [ ] On a cgroup v2 host, `ociOpts` applies `withCgroupView` to every worker: a `cgroup` namespace, and a `cgroup`
  mount at `/sys/fs/cgroup` with `ro`, `nosuid`, `noexec`, `nodev` and `relatime`.
- [ ] No other OCI change, and the process driver is unchanged.
- [ ] `TestScenarioWorkerSeesOnlyItsCgroup` passes on Linux; the duckdb lane asserts the engine's `memory_limit`,
  `threads` and cgroup view, `lake-reader`'s `max`, and spills on the engine; every lane passes.
- [ ] The spill probe fixture and its stage entry are gone.

## Consequences

- **(+)** A provider engine sizes itself to its `spec.resources`; a Lake spills instead of being killed; a future limit
  on a Function is visible to its runtime; a worker sees no other cgroup, as before.
- **(−)** One more namespace per worker; ADR-0011's OCI list grows by two entries.
- **Risk**: a runtime that read the host's totals now reads its limit; only provider engines have one today, and the
  SQL engine's Go runtime now sets `GOMAXPROCS` from its `cpu.max`. DuckDB's 80% of a small limit can leave too little
  memory for the rest of the engine: the idle Lake engine holds about 175 MiB of RSS, so a Lake below about 1Gi still
  needs an explicit memory setting (ADR-0265) to spill a large query.

## Open questions

None.

## References

- Issue #1185, under tracker #1032.
- The duckdb lane, 2026-10-23, before this ADR: at 256Mi and `cpu: "2"`, DuckDB reported 3.0 GiB and 4 threads, and the
  engine's cgroup held `memory.max` 268435456.
- The duckdb lane, 2026-10-23, with `withCgroupView`, each Lake binding no tuning keys:
  - at 256Mi: DuckDB reported 204.7 MiB and 2 threads; the idle engine held 177 MB RSS and `memory.current` 259596288;
    the 20M-row window query got the engine killed (Quack answered 503 and the task stopped);
  - at 512Mi: 409.5 MiB and 2 threads; idle 179 MB RSS; the same query got the engine killed;
  - at 1Gi: 819.1 MiB and 2 threads; idle 180 MB RSS, `memory.current` 260558848; the 40M-row window query
    returned `[[39999999]]`, the Lake stayed Ready, `oom_kill` read 0 with `memory.peak` at the limit, and the kernel
    logged no OOM kill. In the engine, `/proc/self/cgroup` read `0::/`, `cpu.max` `200000 100000`, and a write under
    `/sys/fs/cgroup` was refused (EROFS); in `lake-reader`, `max` and `max 100000`.
- Docker with `--memory 256m --cpus 2`: DuckDB 2.0.0 reported 204.7 MiB and 2 threads, under `--cgroupns=private` and
  `--cgroupns=host` alike.
- containerd v2.4.2: `pkg/oci/mounts.go` (the default mounts), `pkg/oci/spec.go` (the default namespaces),
  `internal/cri/server/container_create.go:765-771` and `internal/cri/opts/spec_linux_opts.go:85-97`.
- [The OCI runtime specification](https://github.com/opencontainers/runtime-spec): the `cgroup` namespace and mount.
