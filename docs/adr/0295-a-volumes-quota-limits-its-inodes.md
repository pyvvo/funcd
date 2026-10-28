# ADR-0295: A volume's quota limits its inodes in proportion to its size

- **Status**: Reviewing (2026-10-29); Accepted (2026-10-28; adr-judge round 1 folded in; the decider kept it as drafted)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: volumes, limits, storage
- **Realizes**: [FEAT-0016/F211](../feat/0016-feat-workload-limits.md) (a volume's file count bounded)
- **Answers**: the decider's refinement of 2026-10-28: an inode limit in proportion to the volume's size, with no new
  field
- **Relates to**: [ADR-0262](0262-function-owned-volumes.md) (volumes, one ext4 image with a project quota per
  volume, no overcommit) · [ADR-0266](0266-volume-sizes-with-units.md) (sizes) ·
  [ADR-0294](0294-a-workers-root-filesystem-is-read-only.md) (the rest of a worker's writes)

## Context & Need

Every volume, local or emptyDir, is a directory of one ext4 image with a project quota (ADR-0262). `quotafs.Limit`
sets the project's block limit only (`internal/volume/quotafs/quotafs_linux.go:180`, `valid: qifBLimits`), and the
image is formatted with mke2fs's default inode ratio (`:132`). This ADR gives each volume an inode limit too: the
share of the image's inodes that its size is of the image's bytes.

## Scenarios

- **scenario: inode-limit-applied**: Given a volume of `size: 64Mi` on an image with one inode per 16 KiB, When its
  Function creates empty files, Then it creates at most 4,095 files and directories, since the limit of 4,096 counts
  the volume's own directory, and the next create fails with "disk quota exceeded".
- **scenario: existing-volume-limited**: Given a local volume created before this ADR, When its Function is next
  reconciled (at boot or at a change), Then its quota carries the inode limit too; a volume that already holds more
  files than that limit refuses new files until some are removed, and nothing is deleted.
- **scenario: emptydir-limited**: Given an emptyDir volume, When its worker starts, Then its quota carries the inode
  limit of its size.

## Scope

**In**: the inode limit of every volume's project quota.
**Out**: the image's format and inode ratio, which stay as they are; usage reporting, which stays in bytes.

## Constraints & Decision drivers

- No new field and no new setting: the size a volume already declares decides.
- ADR-0262's no-overcommit rule must cover inodes as it covers bytes.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Inodes in proportion to the size, from the image's own ratio** | No field; the sums stay within the image wherever the bytes do | A volume of many small files gets as many inodes as its share of bytes allows | **Chosen** |
| B. An `inodes` field per volume | Exact control | A new field and its booking, for a need no one has stated | Rejected |
| C. A node setting for the image's inode ratio (`mkfs -i`) | More inodes per byte | Only a new image can change it; it still needs a per-volume limit | Rejected |

## Decision

1. **The limit.** `quotafs.Limit` sets the project's inode hard and soft limits beside its block limits:
   `inodes = ⌊sizeBytes × imageInodes ÷ imageBytes⌋`, at least 1. `imageInodes` and `imageBytes` are read from the
   mounted image's `statfs` (`f_files`, `f_blocks × f_bsize`) when `quotafs.Open` mounts it, and again after it grows.
2. **Every call.** Every `Limit` call applies both limits. The volume manager calls `Limit` on each reservation pass
   (`internal/volume/volume.go:471`), so a volume created before this ADR gets its inode limit at its next pass.
3. **No overcommit.** Volume sizes never sum past the image's free space, which the booking reads from the
   filesystem (#1201, `internal/volume/volume.go:369-391`), so the inode limits never sum past its inodes. The
   exceptions are ext4's own reserved inodes and the image's directories, which the limits do not count: a nearly
   full image can refuse a file a few dozen inodes before a volume reaches its limit.

## Design model

None: the kernel enforces the limit at each create.

## Temporary workarounds

None.

## Contracts

```go
// Package quotafs (internal/volume/quotafs), Linux. The signature is unchanged; the limits it sets are new.
// Limit sets the block and the inode limits (QIF_BLIMITS|QIF_ILIMITS).
func (f *FS) Limit(dir string, project uint32, sizeBytes int64) error
func inodesFor(sizeBytes, imageBytes, imageInodes int64) uint64      // NEW: ⌊size × inodes ÷ bytes⌋, at least 1
```

| Consumes | Exposes |
|---|---|
| A volume's `size`; the image's `statfs` | The volume's inode limit; "disk quota exceeded" past it |

## Implementation plan

- `internal/volume/quotafs/quotafs_linux.go`: `qifILimits` (4), the image's inode count and size read at `Open` and
  after `grow`, `inodesFor`, and both limits in `Limit`; `quotafs_other.go` is unchanged.
- Tests, one per scenario: `TestInodesFor` (the arithmetic and its floor of 1); `TestScenarioInodeLimitApplied`
  (which also checks that another volume of the image still creates a file), `…ExistingVolumeLimited` and
  `…EmptyDirLimited` on a real image (Linux as root, run by `just test-volumes-root`); the volumes Lima lane gets a
  case where a Function fills its inodes and a second volume still creates a file.
- Docs: the volume reference (a volume holds at most its share of the image's inodes).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; the volumes lane
  passes; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] `Limit` sets `QIF_BLIMITS|QIF_ILIMITS` with the inode limit of `inodesFor`.
- [ ] The image's inode count and size are read when the image is mounted and after it grows, never assumed.
- [ ] Each scenario has its named test.

## Consequences

- **Positive**: a volume's file count is bounded, like its bytes.
- **Negative (accepted)**: a volume of many small files can run out of inodes before bytes; the remedy is a larger
  size.

## Open questions

None.

## References

- The decider's refinement of 2026-10-28; FEAT-0016.
- `internal/volume/quotafs/quotafs_linux.go:132`, `:180`; `internal/volume/volume.go:471`; quotactl(2) `Q_SETQUOTA`
  with `QIF_ILIMITS`.
