# Badger bench — concurrency, durability & 5M scaling (native Linux/arm64, GOMAXPROCS=4)

Follow-ups to the 1M baseline: concurrent load, hot-key contention, the fsync-durable-ack cost, and 5M-key
scaling. Authoritative (Linux reclaims RSS; macOS does not).

## Concurrency & durability (1M keys, lowmem)

| scenario | result | reading |
|---|---|---|
| concurrent mixed (8R / 4W, 3 s) | **400k reads/s + 56k writes/s**, 0 conflicts | disjoint-key load scales cleanly |
| contended hot-key RMW (8W, 2 s) | 98k commits/s, **1.2M conflict-retries = 86% of attempts** | uncoordinated same-key writers waste 86% under SSI → **the case for the single-writer gateway** |
| commit latency, sync=false | **3.7 µs/commit** (270k/s) | non-durable ack |
| commit latency, sync=true | **136 µs/commit (7.3k/s)** — ~37× | one fsync per commit; single-threaded durable-ack ceiling |

**Why group-commit matters:** at 136 µs/fsync a single writer tops out ~7.3k durable acks/s. The ADR's
server-side **group commit** amortizes one fsync over a batch (16–256), lifting effective durable throughput
to ~120k–1.8M/s — so "ack only after the commit is on disk" stays cheap *because* writes are coalesced.

## Scaling 1M → 5M (lowmem, Linux)

| metric | 1M keys | 5M keys |
|---|--:|--:|
| **dormant store** (reopened, idle) | 39 MiB | **91 MiB** — barely grows |
| serving working set (clean idle) | 638 MiB | 2284 MiB |
| bulk-write | 0.70 M/s | 0.58 M/s |
| scan (keys+values) | 3.6 M/s | 3.3 M/s |
| prefix-scan (one fn) | sub-ms | sub-ms |
| DropPrefix (per-fn wipe) | 90 ms | 78 ms |
| backup (full) | 0.46 s → 290 MiB | 1.97 s → 1464 MiB |
| **export peak RSS** | ~1.6 GiB | **~5.1 GiB** |
| on-disk dir | 470 MiB | 1607 MiB |

**The scaling shape:** the *dormant* footprint stays tiny (39 → 91 MiB) — table indexes are mmap'd and paged
cold, so holding keys at rest is cheap regardless of count. What scales is (a) the **serving working set**
(reclaimable cache/buffers, ~0.6 → 2.3 GiB) and (b) the **export peak** (~1.6 → 5.1 GiB, roughly linear) —
the latter is the real ceiling for an in-process backup, and at 5M it neared the 6 GiB VM limit.
