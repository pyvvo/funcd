# ADR-0294: A worker's root filesystem is read-only, and everything it writes has a size limit

- **Status**: Proposed (2026-10-28)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: runtime, limits, storage
- **Realizes**: [FEAT-0016/F210](../feat/0016-feat-workload-limits.md) (a worker's writes bounded)
- **Answers**: the decider's refinement of 2026-10-28: option A (a read-only root, `/tmp` in the worker's memory,
  `HOME=/tmp`, a small boot directory, a read-only log directory) and the engines in the same ADR
- **Relates to**: [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md) (the memory limit `/tmp` counts
  in) · [ADR-0262](0262-function-owned-volumes.md) (volumes, the place for larger files) ·
  [ADR-0231](0231-durable-storage-for-add-on-engines.md) (engine directories) · [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md)
  (the Lake engine's sandbox) · [ADR-0264](0264-a-worker-sees-its-own-cgroup.md) (spilling) ·
  [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) (`lake.catalogMaxSize`) ·
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the log channel) · [ADR-0295](0295-a-volumes-quota-limits-its-inodes.md)

## Context & Need

A containerd worker, a Function's or an engine's, runs on a writable overlay snapshot with no size limit
(`internal/runtime/containerd/containerd_linux.go:501`). Two writable host directories are mounted beside it: the boot
directory `/run/funcd-boot` (`:1230`) and the log channel's directory `/run/funcd-log` (`:212`). ADR-0291 limits a
worker's memory and CPU and ADR-0262 sizes its volumes, but nothing limits what a worker writes elsewhere, and those
writes land on the node's disk. The Lake engine keeps its local catalog copy and DuckDB's temp directory under
`/var/funcd/ducklake` on its root (`images/runtime/duckdb/shim.py:61-63`). DuckDB always permits writes to its temp
directory, whatever `allowed_directories` lists (checked on duckdb 2.0.0, 2026-10-28). This ADR gives every byte a
worker writes a size limit: its memory, its volumes, or a filesystem of fixed size.

## Scenarios

- **scenario: worker-root-read-only**: Given a Function on containerd, When its code writes `/etc/x` or `/var/x`, Then
  the write fails with "read-only file system" and the worker keeps serving.
- **scenario: tmp-counts-in-memory**: Given a Function with `resources.memory: 256Mi`, When its code writes a file to
  `/tmp` or under `$HOME`, Then the write succeeds, `HOME` is `/tmp`, and a worker that fills `/tmp` past its memory
  limit is OOM-killed and restarted with an empty `/tmp`, while the node's disk is unchanged.
- **scenario: boot-and-log-dirs-bounded**: Given a running worker, When it writes more than 1 MiB to `/run/funcd-boot`,
  Then the write fails with "no space left on device"; When it writes a file to `/run/funcd-log`, Then the write fails
  with "read-only file system", and its structured logs still reach funcd through the socket.
- **scenario: lake-scratch-bounded**: Given `lake.scratchSize: 256Mi` and a Lake, When a query spills or writes files
  into the engine's temp directory past 256 MiB, Then the query fails with an out-of-space error, the engine keeps
  serving, and the node's disk holds only the 256 MiB image.
- **scenario: lake-scratch-recreated**: Given a Lake whose engine restarts, When the new engine starts, Then its
  scratch is a new, empty filesystem and its catalog is recovered from the catalog store, as today.
- **scenario: sql-engine-read-only-root**: Given a SQLDatabase, When its engine starts and serves, Then its root is
  read-only, its data stays in its ADR-0231 directory, and SQLite's temporary files go to `/tmp`.

## Scope

**In**: every containerd worker (Function, pool and engine workers): the read-only root, `/tmp`, `HOME`, the boot and
log directories; the Lake engine's scratch filesystem and `lake.scratchSize`.
**Out**: volumes and their inode limit (ADR-0262, ADR-0295); the process runtime, which isolates nothing (ADR-0262);
free space on the data filesystem (ADR-0296).

## Constraints & Decision drivers

- The kernel enforces every limit; funcd only sizes and mounts.
- No new Function field: the memory limit of ADR-0291 sizes `/tmp`, and volumes already hold larger files.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. A read-only root; `/tmp` a tmpfs within the memory limit; a 1 MiB boot directory; a read-only log directory; the Lake engine's scratch a fixed-size filesystem** | The kernel bounds every write; no host setup | Code that writes outside `/tmp` and its volumes fails | **Chosen** |
| B. A writable root with a disk limit per worker | Code is unchanged | containerd's overlay snapshotter has no size option; a project quota needs the snapshotter's filesystem mounted with `prjquota`, which funcd does not set up | Rejected |
| C. The writable layer inside the volume image | Reuses quotafs | Volumes are off until `volumes.capacity` is set (ADR-0262), so a default node would get no limit | Rejected |
| D. The Lake engine's temp directory as a tmpfs | No image | DuckDB spills there when memory runs short; a spill in memory counts in the same cgroup (ADR-0264) | Rejected |
| E. Narrow DuckDB's `allowed_directories` | No image | DuckDB always permits its temp directory | Rejected |

## Decision

1. **Read-only root.** Every containerd worker's root filesystem is read-only (`oci.WithRootFSReadonly`).
2. **`/tmp` in memory.** Every worker gets a tmpfs at `/tmp` (`nosuid`, `nodev`, mode 1777) of the worker's memory
   limit, or 512 MiB, ADR-0291's default memory, for a worker without one (a pool worker). Its pages count in the
   worker's memory cgroup, and the tmpfs ends with the worker.
3. **`HOME`.** A worker's environment gets `HOME=/tmp` unless its spec sets `HOME`.
4. **Boot directory.** The driver mounts a 1 MiB tmpfs (`nosuid`, `nodev`, `noexec`, mode 0777) on the host's boot
   directory before it creates the container, and unmounts it before it removes the directory: at teardown, at a
   failed create and in the boot sweep. It holds the port and report files only.
5. **Log directory.** The log channel's directory is bind-mounted read-only. A socket on a read-only mount still
   connects: Linux returns "read-only file system" only for regular files, directories and symbolic links.
6. **The Lake engine's scratch.** `/var/funcd/ducklake`, which holds the catalog copy and DuckDB's temp directory, is
   a filesystem of its own:
   - At each engine start, the provider runtime creates `<dataDir>/scratch/<ns>/<lake>.img` of `lake.scratchSize`
     (NEW key, a `ByteSize`, 2GiB when empty, more than `lake.catalogMaxSize`).
   - The image is preallocated with `fallocate` and formatted with `mkfs.ext4 -q -F -m 0`. It is loop-mounted
     (`nosuid`, `nodev`) at `<stateDir>/scratch/<ns>/<lake>` and bind-mounted into the engine.
   - It is unmounted and deleted after the engine's teardown; the boot sweep removes leftovers.
   - A start that cannot allocate the image fails like any engine start, and the Lake reports the error.
   - DuckDB's own spill limit, `max_temp_directory_size`, now defaults to 90% of this filesystem's free space.
7. **The SQL engine.** It keeps its data in its ADR-0231 directory and writes nothing else outside `/tmp`.

## Design model

None: no protocol runs between actors. The kernel enforces each limit at the write.

## Temporary workarounds

None.

## Contracts

```go
// Package containerd (internal/runtime/containerd), Linux, NEW: what ociOpts and Create add for every worker.
const tmpDefaultBytes = 512 << 20 // /tmp of a worker without a memory limit (ADR-0291's default)
const bootDirBytes = 1 << 20      // the host tmpfs of a worker's boot directory

// Package provider (internal/provider), NEW: a fixed-size filesystem an engine gets at Path.
type ScratchSpec struct {
	Path      string // the mount point in the engine, e.g. /var/funcd/ducklake
	SizeBytes int64
}
// ProviderSpec.Scratch *ScratchSpec // nil: none

// Package config (internal/platform/config), NEW.
//	Lake.ScratchSize string `json:"scratchSize,omitempty" env:"FUNCD_LAKE_SCRATCH_SIZE"`
func (c Config) LakeScratchBytes() (int64, error) // 2GiB when empty; Validate refuses one not above catalogMaxSize
```

| Consumes | Exposes |
|---|---|
| A worker's memory limit (ADR-0291); `lake.scratchSize` (NEW) | A read-only root, `/tmp`, `HOME=/tmp`; a 1 MiB boot directory; the Lake engine's scratch filesystem |

## Implementation plan

- `internal/runtime/containerd`: `ociOpts` adds the read-only root, the `/tmp` tmpfs and `HOME`; `makeBootDir` mounts
  the boot tmpfs, and teardown and the boot sweep unmount it; `setupLogChannel`'s mount is read-only.
- `internal/provider`: `ProviderSpec.Scratch` and the image's lifecycle (create before `Create`, remove after
  teardown, sweep at boot), Linux only; `internal/services/lake/reconcile.go:117` sets
  `Scratch{Path: "/var/funcd/ducklake", SizeBytes: lake.scratchSize}`; `internal/platform/config`: the key.
- Tests, one per scenario: `TestScenarioWorkerRootReadOnly`, `…TmpCountsInMemory` (the OCI spec carries the root, the
  tmpfs size and `HOME`), `…BootAndLogDirsBounded` and `…LakeScratchRecreated` (Linux, run by `just test-linux`),
  `…LakeScratchBounded` and `…SqlEngineReadOnlyRoot` (the duckdb and sql Lima lanes: a COPY past a 256 MiB scratch
  fails and the next query succeeds; a write to `/etc` from a Function fails).
- Docs: the Function reference (where code may write), `funcdconfig.yaml`'s reference, a changelog note.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; every Lima lane
  passes; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] Every containerd worker's spec has a read-only root, a `/tmp` tmpfs of its memory limit or 512 MiB, and
      `HOME=/tmp` unless set.
- [ ] Every path that removes a boot directory unmounts its tmpfs first; the log directory's mount is read-only.
- [ ] The Lake engine's scratch is preallocated, mounted before the engine starts, removed after it stops, and swept
      at boot; `lake.scratchSize` must exceed `lake.catalogMaxSize`.

## Consequences

- **Positive**: a worker's writes are bounded by its memory, its volumes or its scratch, and none of them can grow the
  node's disk beyond those sizes.
- **Negative (accepted)**: code that writes outside `/tmp`, `$HOME` and its volumes now fails (the changelog says so);
  `/tmp` uses the worker's memory; each Lake engine holds `lake.scratchSize` of disk while it runs, and its start
  formats an image.

## Open questions

- Engine directories on the volume image (ADR-0262's open question): its own ADR, if engines need a limit there.

## References

- The decider's refinement of 2026-10-28; FEAT-0016.
- `internal/runtime/containerd/containerd_linux.go:212`, `:501`, `:1215-1240`; `images/runtime/duckdb/shim.py:61-63`,
  `:225`, `:238`.
