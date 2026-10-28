# ADR-0296: The data filesystem keeps a free-space floor that workload writes cannot pass

- **Status**: Proposed (2026-10-28)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: storage, limits, quota
- **Realizes**: [FEAT-0016/F212](../feat/0016-feat-workload-limits.md) (free space kept for the platform)
- **Answers**: the decider's refinement of 2026-10-28: `storage.minFreeSize`, 2GiB by default; workload writes get 507
  below it, funclog drops new records, and the platform's own stores keep writing into the margin
- **Relates to**: [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (the `quota.Checker` every
  storage write asks; per-store quotas) · [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) (the
  catalog store) · [ADR-0081](0081-function-log-capture-side-channel-blob.md) (funclog) ·
  [ADR-0294](0294-a-workers-root-filesystem-is-read-only.md) (a worker's own writes) ·
  [ADR-0297](0297-a-store-write-on-a-full-disk-fails-cleanly.md) (a full disk)

## Context & Need

Everything funcd stores lands on the filesystem under `storage.dataDir`: the metastore, the workflow and KV stores,
the blob store behind Buckets, Lakes and funclog, and the SQL engines' directories. ADR-0292's quotas are opt-in and per
store, so a store without a `quota` block grows until that filesystem is full, and the quotas' sum can exceed it. When
it is full, the platform's own writes fail with the workloads'. This ADR keeps a margin of free space for the platform:
workload writes stop at the floor, and the platform's stores keep writing into the margin below it.

## Scenarios

- **scenario: floor-refuses-workload-writes**: Given `storage.minFreeSize: 2GiB` and 1.5 GiB free on the data
  filesystem, When a Function puts a KV value, uploads an object through the S3 gateway, runs a SQL write, or a Lake
  commits, Then each gets 507 naming the free space and the floor; reads and deletes still succeed.
- **scenario: floor-lifts**: Given the floor refusing, When space is freed above it, Then writes are admitted again
  within a second, with no restart.
- **scenario: platform-writes-continue**: Given the floor refusing, When an operator applies a resource or a workflow
  run records its state, Then those writes succeed.
- **scenario: funclog-drops-below-floor**: Given the floor refusing, When a Function logs, Then its records are
  dropped, and when the floor lifts funcd logs how many it dropped.
- **scenario: floor-off**: Given `storage.minFreeSize: "0"`, or `storage.mode: memory`, When the data filesystem is
  low, Then no write is refused for its free space.

## Scope

**In**: the floor, its setting, and the writes it gates: KV, Buckets (S3 gateway and `ctx.blob`), SQL, Lakes (data and
catalog), funclog.
**Out**: the platform's own stores, backups and restores; volumes, whose image is preallocated; a worker's writes
(ADR-0294); per-namespace budgets (F208, V2).

## Constraints & Decision drivers

- One decision point: the floor sits in the `quota.Checker` that every storage write already asks (ADR-0292), not in
  each store.
- A floor the operator can set, with a default.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. A size floor in front of the quota Checker** | One decision point; a predictable margin | A size suits a disk of one size less well than a share | **Chosen** |
| B. A percentage of the filesystem | Scales with the disk | The margin the platform needs does not grow with the disk; the decider chose a size | Rejected |
| C. A check in each store | Local | Duplicates the decision ADR-0292 put in one place | Rejected |
| D. Per-namespace budgets | Fair shares | V2 (F208); it bounds a namespace, not the disk | Later |

## Decision

1. **The setting.** `storage.minFreeSize` (NEW key, a `ByteSize`, 2GiB when empty, `"0"` off) is the free space funcd
   keeps on the filesystem of `storage.dataDir` for its own stores. The floor is off in `storage.mode: memory`.
2. **The floor.** `quota.Floor` (NEW) reads the filesystem's available bytes (`statfs`: `f_bavail × f_bsize`) at most
   once a second. Below the floor it refuses every write but a delete with `fault.InsufficientStorage` (507): "the
   data filesystem has 1.4GiB free, below storage.minFreeSize (2GiB): writes are refused until space is freed". It logs
   once when it starts refusing and once when it stops.
3. **One decision point.** `Floor.Guard(book)` is the Checker that KV, SQL and every `blob.Quoted` view get. A
   `Quoted` view asks it once with the zero `Subject` when no capped subject covers a key, so an uncapped store's write
   meets the floor too; the `Book` admits the zero `Subject`, which no `Source` claims.
4. **The catalog store.** Its view (`s3gateway.Deps.Catalogs`) is a `Quoted` view with no capped subject, so a Lake's
   catalog save meets the floor; the Lake reports a refused save as ADR-0276 does.
5. **funclog.** `BlobSink.Append` asks `Floor.Check` first; below the floor it drops the record and counts it, and the
   count is logged when the floor lifts.
6. **Not gated.** The metastore, workflow run state, the dead-letter queue, backups and restores write into the
   margin. Presigned PUTs never reach the data filesystem: only an S3-backed blob store signs them
   (`internal/blob/gocloud/gocloud.go:744`; the local backend has no URL signer).

## Design model

None: one reader of the filesystem's free space decides; the stores only ask.

## Temporary workarounds

None.

## Contracts

```go
// Package quota (internal/quota), NEW.
type FreeFunc func() (int64, error)  // the available bytes of a filesystem
func DiskFree(path string) FreeFunc   // statfs(path): f_bavail × f_bsize
type Floor struct{ /* unexported */ }
func NewFloor(free FreeFunc, minFreeBytes int64, clk clock.Clock, logger *slog.Logger) *Floor
func (f *Floor) Check(ctx context.Context) error // nil at or above the floor, else fault.InsufficientStorage
func (f *Floor) Guard(next Checker) Checker     // asks Check before next.Admit for every Request but Deletes

// Package config (internal/platform/config), NEW.
//	Storage.MinFreeSize string `json:"minFreeSize,omitempty" env:"FUNCD_STORAGE_MIN_FREE_SIZE"`
func (c Config) StorageMinFreeBytes() (int64, error) // 2GiB when empty; 0 off
```

| Consumes | Exposes |
|---|---|
| `storage.minFreeSize` (NEW); the data filesystem's free space | 507 below the floor; two log lines per episode; dropped funclog records counted |

## Implementation plan

- `internal/quota/floor.go`: `Floor`, `DiskFree`, `Guard`; `internal/blob/quoted.go`: the zero-subject ask;
  `internal/funclog/sink.go`: the floor before `Append`; `internal/platform/config`: the key; `pkg/funcd`: one `Floor`
  over `storage.dataDir`, guarding `p.quotas` everywhere it is passed, and a `Quoted` catalog view.
- Tests, one per scenario: `TestScenarioFloorRefusesWorkloadWrites` (KV, a Bucket, SQL and a Lake commit through
  `pkg/funcd` with an injected `FreeFunc`), `…FloorLifts` (a fake clock), `…PlatformWritesContinue`,
  `…FunclogDropsBelowFloor`, `…FloorOff`; unit tests of `Floor` and of `Quoted`'s zero-subject ask; on real
  containerd, the kv-counter Lima lane restarts funcd with a floor above its free space, gets 507 on a put, then
  lowers it and puts.
- Docs: `funcdconfig.yaml`'s reference; the operations guide (what 507 below the floor means and how to free space).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; every Lima lane
  passes; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] Every storage write path named in Scope reaches `Floor` through the Checker or `Check`, and a delete never does.
- [ ] Free space is read at most once a second and never on a delete.
- [ ] The platform's own stores take no floor; each scenario has its named test.

## Consequences

- **Positive**: workloads can no longer fill the data filesystem through funcd; the platform keeps the margin.
- **Negative (accepted)**: with less than 2 GiB free a node refuses workload writes, so a small disk needs a smaller
  setting; funclog records are lost while the floor refuses.

## Open questions

- Per-namespace budgets: F208, V2.

## References

- The decider's refinement of 2026-10-28; FEAT-0016.
- `internal/quota/quota.go` (`Checker`, `Book`, `None`); `internal/blob/quoted.go`; `internal/funclog/sink.go:147`;
  `internal/blob/s3gateway/s3gateway.go:66`.
