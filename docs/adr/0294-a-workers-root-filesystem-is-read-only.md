# ADR-0294: A worker's root filesystem is read-only, and everything it writes has a size limit

- **Status**: Proposed (2026-10-28; adr-judge round 1 folded in; revised after the decider's review: a 1GiB scratch
  and a 256MiB catalog cap; round 4 passed, its Minors folded in)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: runtime, limits, storage
- **Realizes**: [FEAT-0016/F210](../feat/0016-feat-workload-limits.md) (a worker's writes bounded)
- **Answers**: the decider's refinement of 2026-10-28: option A (a read-only root, `/tmp` in the worker's memory,
  `HOME=/tmp`, a small boot directory, a read-only log directory) and the engines in the same ADR; the decider's
  review of the same day: the Lake engine's scratch stays preallocated, 1GiB by default, with a lower catalog cap
- **Supersedes (in part)**: [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) Decision 3's
  default for `lake.catalogMaxSize`: 256MiB instead of 1GiB
- **Relates to**: [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md) (the memory limit `/tmp` counts
  in) · [ADR-0262](0262-function-owned-volumes.md) (volumes, the place for larger files) ·
  [ADR-0231](0231-durable-storage-for-add-on-engines.md) (engine directories) ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) (the Lake engine's sandbox) ·
  [ADR-0264](0264-a-worker-sees-its-own-cgroup.md) (spilling) ·
  [ADR-0265](0265-the-lake-engine-takes-tuning-keys-from-an-allow-list.md) (`DUCKDB_MAX_TEMP_DIRECTORY_SIZE`) ·
  [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) (`lake.catalogMaxSize`) ·
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) (the log channel) ·
  [ADR-0295](0295-a-volumes-quota-limits-its-inodes.md) ·
  [ADR-0296](0296-the-data-filesystem-keeps-a-free-space-floor.md)

## Context & Need

A containerd worker, a Function's or an engine's, writes beyond its volumes: to its root filesystem, to its boot
directory and to its log channel's directory. This ADR gives every byte a worker writes a fixed limit: its memory, its
volumes, or a filesystem of fixed size. The Lake engine keeps three things in one directory, `/var/funcd/ducklake`: its
catalog copy, a checkpoint copy of the catalog made before each save, and DuckDB's spill files
(`images/runtime/duckdb/shim.py:61-66`, `:445-452`). A Lake's Parquet files live in its Bucket, not there, and its
catalog holds the Lake's metadata, small next to them. That directory gets a filesystem of its own, sized for all three.

## Scenarios

- **scenario: worker-root-read-only**: Given a Function on containerd, When its code writes `/etc/x` or `/var/x`, Then
  the write fails with "read-only file system" and the worker keeps serving.
- **scenario: tmp-counts-in-memory**: Given a Function with `resources.memory: 256Mi`, When its code writes a file to
  `/tmp` or under `$HOME`, Then the write succeeds, `HOME` is `/tmp`, and `/tmp` is a tmpfs of 256 MiB whose pages
  count in the worker's memory.
- **scenario: boot-and-log-dirs-bounded**: Given a running worker, When it writes more than 1 MiB to `/run/funcd-boot`,
  Then the write fails with "no space left on device"; When it writes a file to `/run/funcd-log`, Then the write fails
  with "read-only file system", and its structured logs still reach funcd through the socket.
- **scenario: lake-scratch-bounded**: Given `lake.scratchSize: 512Mi` and `lake.catalogMaxSize: 128Mi`, When a query
  spills more than 256 MiB, Then that query fails with an out-of-space error, and the Lake's next commit and its next
  save succeed.
- **scenario: lake-scratch-refused-below-floor**: Given less free space on the data filesystem than
  `storage.minFreeSize` plus `lake.scratchSize`, When a Lake's engine starts, Then the start is refused before
  anything is allocated, and the Lake reports that the data filesystem lacks the space for its scratch.
- **scenario: lake-scratch-recreated**: Given a Lake whose engine restarts, When the new engine starts, Then its
  scratch is a new, empty filesystem and its catalog is recovered from the catalog store, as today.
- **scenario: sql-engine-read-only-root**: Given a SQLDatabase, When its engine runs, Then a write to its `/etc` fails
  with "read-only file system", its data stays in its ADR-0231 directory, and its SQL calls succeed.

## Scope

**In**: every containerd worker (Function, pool and engine workers): the read-only root, `/tmp`, `HOME`, the boot and
log directories; the Lake engine's scratch filesystem, `lake.scratchSize` and DuckDB's spill limit.
**Out**: volumes and their inode limit (ADR-0262, ADR-0295); the process runtime, which isolates nothing (ADR-0262);
the floor itself (ADR-0296). Files a Lake's own queries write into its scratch count against that scratch: they are
bounded, and they are the Lake's own concern.

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
| E. Narrow DuckDB's `allowed_directories` | No image | The engine's temp directory must stay writable for its spills | Rejected |
| F. A sparse scratch image | Takes disk only as the Lake writes | Lakes spilling at once can use up the free-space margin; the decider chose a preallocated, smaller one | Rejected |

## Decision

1. **Read-only root.** Every containerd worker's root filesystem is read-only (`oci.WithRootFSReadonly()`).
2. **`/tmp` in memory.** Every worker gets a tmpfs at `/tmp` (`nosuid`, `nodev`, mode 1777) of its memory limit, whose
   pages count in its memory cgroup. A pool worker has no memory limit (ADR-0291), so its `/tmp` is 512 MiB, ADR-0291's
   default memory, bounded by the tmpfs's size in the node's memory. The tmpfs ends with the worker. containerd's own
   `/run` and `/dev/shm` stay tmpfs of 64 MiB each (`pkg/oci/mounts.go`), in memory too.
3. **`HOME`.** A worker's environment gets `HOME=/tmp` unless its spec sets `HOME`.
4. **Boot directory.** The driver mounts a 1 MiB tmpfs (`nosuid`, `nodev`, `noexec`, mode 0777) on the host's boot
   directory before it creates the container, and unmounts it before it removes the directory: at teardown and at a
   failed create. At boot, the driver unmounts every mount under the boot root that `/proc/self/mountinfo` lists, since
   an unclean stop or a failed create can leave one that containerd's container list does not name. The directory
   holds the port and report files only.
5. **Log directory.** The log channel's directory is bind-mounted read-only. A socket on a read-only mount still
   connects: Linux returns "read-only file system" only for regular files, directories and symbolic links.
6. **The Lake engine's scratch.** `/var/funcd/ducklake` is a filesystem of its own:
   - `lake.scratchSize` (NEW key, a `ByteSize`) sizes it. Empty, it is the larger of 1GiB and twice
     `lake.catalogMaxSize` plus 512MiB, so a node that sets a large catalog cap still starts; a value that is set is
     refused unless it is more than twice `lake.catalogMaxSize`, which leaves room for the catalog, its checkpoint
     copy and the spills.
   - `lake.catalogMaxSize` defaults to 256MiB (ADR-0293 Decision 3 said 1GiB), so the default scratch is 1GiB:
     512MiB for the catalog and its checkpoint copy, 512MiB for spills.
   - Before it allocates anything, the provider runtime asks the free-space floor (ADR-0296) for `lake.scratchSize`; a
     refusal fails the start, and the Lake reports that the data filesystem lacks the space for its scratch.
   - At each engine start the provider runtime creates `<dataDir>/scratch/<ns>/<lake>.img` of `lake.scratchSize`,
     preallocated and formatted, loop-mounts it (`nosuid`, `nodev`) at `<stateDir>/scratch/<ns>/<lake>` and
     bind-mounts it into the engine. It reuses quotafs's image code (`internal/volume/quotafs/quotafs_linux.go:104`,
     `:127`, `:132`), moved to a package both use. The image is unmounted and deleted after the engine's teardown.
     Before a start waits for space (ADR-0297), funcd unmounts and removes the images an earlier run left that no
     engine holds (`provider.RemoveLeftoverScratch`), and the boot sweep removes the rest.
   - The spill limit is the scratch's free space before the engine writes to it, less twice `lake.catalogMaxSize`, so
     ext4's own overhead never comes out of the catalog's room: the Lake reconciler passes the cap as
     `FUNCD_LAKE_CATALOG_MAX_BYTES` (NEW), and the shim reads the free space with `statvfs` and sets DuckDB's
     `max_temp_directory_size` to the result, or to `DUCKDB_MAX_TEMP_DIRECTORY_SIZE` (ADR-0265) when that is smaller.
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
// The engine runtime's deps gain the floor (ADR-0296):
//	Floor interface{ Check(ctx context.Context, bytes int64) error }

// RemoveLeftoverScratch unmounts and removes the scratch images an earlier run left under root, mounted under
// mountRoot; held is the size of those an engine still holds, which the boot sweep frees (ADR-0297).
func RemoveLeftoverScratch(root, mountRoot string) (held int64, err error)

// Package config (internal/platform/config), NEW.
//	Lake.ScratchSize string `json:"scratchSize,omitempty" env:"FUNCD_LAKE_SCRATCH_SIZE"`
func (c Config) LakeScratchBytes() (int64, error) // empty: max(1GiB, 2 × catalog cap + 512MiB); set: above 2 × the cap
// CHANGED: DefaultLakeCatalogMaxSize = "256MiB" (was "1GiB", ADR-0293 Decision 3)
```

| Consumes | Exposes |
|---|---|
| A worker's memory limit (ADR-0291); `lake.scratchSize` (NEW); the floor (ADR-0296) | A read-only root, `/tmp`, `HOME=/tmp`; a 1 MiB boot directory; the Lake engine's scratch and `FUNCD_LAKE_CATALOG_MAX_BYTES` (NEW) |

## Implementation plan

- `internal/runtime/containerd`: `ociOpts` adds the read-only root, the `/tmp` tmpfs and `HOME`; `makeBootDir` mounts
  the boot tmpfs; teardown, a failed create and the boot sweep unmount it (the sweep from `/proc/self/mountinfo`);
  `setupLogChannel`'s mount is read-only.
- quotafs's image creation, format and loop mount move to a package both quotafs and the scratch use.
- `internal/provider`: `ProviderSpec.Scratch`, the floor check, and the image's lifecycle (create before `Create`,
  remove after teardown, `RemoveLeftoverScratch`, sweep at boot), Linux only. `internal/services/lake/reconcile.go:117`
  sets `Scratch{Path: "/var/funcd/ducklake", SizeBytes: lake.scratchSize}` and `FUNCD_LAKE_CATALOG_MAX_BYTES`.
  `images/runtime/duckdb/shim.py` computes and applies the spill limit. `internal/platform/config`: the key and its
  check, and `DefaultLakeCatalogMaxSize` lowered to 256MiB.
- Tests, one per scenario: `TestScenarioWorkerRootReadOnly` and `…TmpCountsInMemory` (the OCI spec carries the
  read-only root, the tmpfs and its size, and `HOME`); `…LakeScratchRefusedBelowFloor` and `…LakeScratchRecreated`
  (the provider runtime with a fake floor and a fake image); the image package's own test and
  `RemoveLeftoverScratch`'s as root (`just test-volumes-root`); the shim's spill limit from a fake `statvfs`
  (`shim_test.py`). On real containerd: the kv-counter Lima lane execs into a Function's worker (a write to
  `/etc` fails, `/tmp` is a tmpfs) and, as `…BootAndLogDirsBounded`, a 2 MiB write to `/run/funcd-boot` fails, a
  write to `/run/funcd-log` fails, and the Function's next log line still reaches funcd;
  the duckdb lane, with a 512Mi scratch and a 128Mi catalog cap, runs a query that spills past 256 MiB and fails, then
  a commit and a save that succeed (`…LakeScratchBounded`); the sql lane execs a write to `/etc` in the engine, which
  fails, and its SQL cases pass (`…SqlEngineReadOnlyRoot`).
- Docs: the Function reference (where code may write), `funcdconfig.yaml`'s reference, a changelog note (where code
  may write; a Lake whose catalog is over 256 MiB needs `lake.catalogMaxSize` set, or it cannot save, and cannot start
  once its catalog no longer fits its scratch).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; the root tests
  and every Lima lane pass; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] Every containerd worker's spec has a read-only root, a `/tmp` tmpfs of its memory limit or 512 MiB, and
      `HOME=/tmp` unless set.
- [ ] Every path that removes a boot directory unmounts its tmpfs first, and the boot sweep unmounts what
      `/proc/self/mountinfo` lists under the boot root; the log directory's mount is read-only.
- [ ] The Lake engine's scratch is asked of the floor, preallocated, mounted before the engine starts, removed after
      it stops, removed before the start's wait when no engine holds it, and swept at boot; `lake.scratchSize` must
      exceed twice `lake.catalogMaxSize`, whose default is 256MiB; the spill limit comes from the scratch's free space.

## Consequences

- **Positive**: a worker's writes are bounded by its memory, its volumes or its scratch.
- **Negative (accepted)**: code that writes outside `/tmp`, `$HOME` and its volumes now fails (the changelog says so);
  `/tmp` uses the worker's memory; each running Lake holds `lake.scratchSize` of disk, 1 GiB by default, and its start
  formats an image; a Lake whose catalog is over 256 MiB has its saves refused after the upgrade (ADR-0276's
  `SavesFailing`), and one whose catalog does not fit in its scratch does not start, until `lake.catalogMaxSize`, and
  with it the scratch, is raised.

## Open questions

- Engine directories on the volume image (ADR-0262's open question): its own ADR, if engines need a limit there.

## References

- The decider's refinement and review of 2026-10-28; FEAT-0016; adr-judge rounds 1 and 4 (coworker-3, 2026-10-28).
- `images/runtime/duckdb/shim.py:61-66`, `:107-111`, `:225`, `:445-452`; containerd v2.4.2 `pkg/oci/spec_opts.go:500`.
