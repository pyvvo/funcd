# Badger bench — characterizing the inherent limits for funcd

A standalone harness that measures **Badger v4** under funcd's intended use (the metastore + the
per-function KV service, the slatedb → pure-Go engine question). It answers the question that decides
adoption: **what does Badger hold resident, and how does that scale to ~1M keys**, since funcd's target
is RAM-bound. Throughput is measured too, but RSS is the headline.

It is its **own Go module** (`bench/badger/go.mod`) — Badger is **not** a dependency of funcd's main
module. This is a measurement spike to inform a superseding ADR for [ADR-0006](../../docs/adr/0006-store-database-layer-port.md);
nothing here enters funcd's build, lint, or CI until that decision lands.

## Run it

```bash
# from the repo root, through the pinned toolchain:
cd bench/badger
nix develop ../.. -c go run . -keys 1000000 -profile lowmem -json results/badger-1M-lowmem.json

# compare the three profiles on the same workload:
for p in lowmem default inmem; do
  nix develop ../.. -c go run . -keys 1000000 -profile "$p" -json "results/badger-1M-$p.json"
done
```

Key flags: `-keys` (default 1,000,000), `-funcs` (prefixes to spread keys across — models the storage
ADR's prefix-per-function layout, default 1000 → 1000 keys/function), `-valsize` (default 256B, funcd
resources are small blobs), `-profile`, `-sync` (fsync each commit), `-keep`/`-dir`, `-json`.
Concurrency/durability knobs: `-readers`/`-writers`/`-concdur` (mixed-load test), `-hotwriters`/`-hotdur`
(hot-key contention), `-synccommits` (sync-cost comparison).

## What it measures

Each scenario reports throughput **and** the OS RSS it cost (before → after, plus a 20 ms-sampled peak),
spread across `funcs` function prefixes:

| group | scenarios |
|---|---|
| **serving** | bulk-write (WriteBatch), txn-write (group commit), get (warm/cold/random), full scan (keys / keys+values), prefix-scan (one function), point-delete, merge-operator |
| **concurrency** | concurrent mixed R/W (throughput scaling), contended hot-key RMW (SSI conflict rate — the case for the single-writer gateway) |
| **durability** | commit latency, SyncWrites off vs on (the fsync-durable-ack cost) |
| **per-function** | `DropPrefix` — the per-tenant wipe (funcd's `rm file.db` equivalent) |
| **export / maintenance** | `Subscribe` (the latency trigger), full `Backup`, parallel `Stream` |
| **footprint** | steady idle RSS (clean, mid-serving) · idle RSS after the export ops · cold reopen + cold-cache gets · on-disk LSM/vlog/dir size |

### The three profiles
- **lowmem** — tuned per the [Badger memory-usage guide](https://dgraph-io.github.io/badger/quickstart.html#memory-usage):
  2 memtables, 16 MiB memtable, 32 MiB block + index cache, 64 MiB value-log files, compression off,
  small values kept **inline** in the LSM (no value log). The funcd-relevant profile.
- **default** — `badger.DefaultOptions`, the out-of-the-box footprint (1 GiB value-log files, bigger caches).
- **inmem** — `WithInMemory`, everything in RAM, nothing on disk (the upper bound, and the analogue of
  funcd's existing pure-Go memory engine).

## Reading the results (important caveats)

- **RSS, not Go heap, is the number.** Badger mmaps SSTables and the value log and keeps caches off the
  Go heap; `runtime` heap stats undercount the real footprint. The harness reads `/proc/self/status`
  (Linux) or `ps` (macOS).
- **The export path (`Backup`/`Stream`) spikes RSS via pooled off-heap `z.Buffer`s** that are not returned
  to the OS — so the *clean* steady RSS is measured **before** those run, and a second idle reading after
  them shows the high-water they leave. Weigh this if backups run in the same process as serving.
- **macOS overstates retained RSS.** Freed mmap arenas can stay resident on macOS until pressure; the
  **Linux** numbers (funcd's real target — run this in the Lima VM) are the ones to trust for a decision.
- **`db.Size()` can read 0** right after writes (metrics update on flush/compaction); the **dir total** is
  the real on-disk figure. Note the default value-log **preallocates** files, so dir-total overstates
  actual data for small datasets — lower `WithValueLogFileSize` for funcd-sized stores.
