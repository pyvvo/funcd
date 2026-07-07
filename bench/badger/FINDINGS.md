# Badger bench — findings (1M keys)

Workload: `-keys 1000000 -funcs 1000 -valsize 256`. Run on **native Linux/arm64** (colima container, not
emulated, GOMAXPROCS=4 — close to the 8-core target) **and** macOS, three profiles. The Linux numbers are
**authoritative**; macOS is shown only to expose its RSS-retention artifact. Raw: [results/](results/)
(JSON = macOS; [results/linux-1M.md](results/linux-1M.md) = Linux tables).

## The headline: RSS on Linux (the real target)

| RSS state (1M keys) | lowmem | default | what it is |
|---|--:|--:|---|
| **dormant store** (reopened, idle, post-GC) | **39 MiB** | **134 MiB** | the resting cost of *holding* 1M keys |
| serving working set (post-GC) | 638 MiB | 696 MiB | memtables + caches + buffers, **reclaimable** |
| peak during bulk-write | 797 MiB | 827 MiB | write working set |
| export peak (Backup/Stream) | ~1.6 GiB | ~1.6 GiB | **transient** — reclaimed after (→ 362–603 MiB) |
| on-disk dir | 470 MiB | 2224 MiB | default preallocates 1 GiB value-logs |

Throughput (Linux): bulk-write **0.7–0.93 M/s**, txn-write 0.8 M/s, get 0.27–0.39 M/s, scan 3–9 M/s,
backup of 1M keys in **~0.45 s**, prefix-scan of one function **sub-ms**, `DropPrefix` **90 ms** (lowmem).

## ⚠️ Why the OS matters (and corrects an earlier read)

A macOS-only run looks alarming: dormant/after-export RSS sits at **1.8–2.1 GiB** and never falls. That is a
**measurement artifact** — macOS leaves freed mmap arenas resident until memory pressure. On **Linux the same
workload reclaims them**: after close+reopen the 1M-key store idles at **39–134 MiB**, and the export spike
falls back to ~0.4–0.6 GiB. Lesson for any future engine bench here: **trust the Linux RSS, not macOS.**

## What it means for funcd

1. **RSS is affordable — the earlier "~1 GiB floor" was the macOS artifact.** On Linux a *dormant* 1M-key
   store costs **~40–130 MiB** resident; *active* serving is ~640–700 MiB of mostly-reclaimable working set.
   At funcd's real **metastore** scale (~100 functions × a few resources = thousands of keys) this is single-
   digit MiB — a non-issue. Even the 1M-key **per-function-KV aggregate ceiling** sits comfortably in the
   8-core/18 GiB budget.

2. **Throughput is a non-issue** — writes ~0.8 M/s, reads ~0.3 M/s, far above funcd's modest-write assumption.

3. **The prefix-per-function model is validated** — a single function's range scan is sub-ms and its
   `DropPrefix` wipe is ~90 ms, both **O(function), not O(all)**. The storage-ADR's "functions as key
   prefixes" layout works exactly as intended (cheap per-tenant scan + GDPR delete).

4. **The export path is a transient ~1.6 GiB spike, then reclaimed** (not the permanent doubling macOS
   suggested). So an in-process backup is a real-but-temporary memory event to schedule around, not a
   standing cost — milder than feared, but still the empirical face of the storage-ADR's "you own the
   durability subsystem": correctness + memory of the export loop is yours to get right.

5. **Profile barely moves steady RSS; its real lever is disk.** lowmem vs default differ little on resident
   memory, but default **preallocates 1 GiB value-log files** (2.2 GiB on disk for 256 MiB of data) — set
   `WithValueLogFileSize` small for funcd-sized stores (lowmem → 470 MiB).

6. **Concurrency, contention & durability** (Linux 1M — [results/linux-concurrency-and-5M.md](results/linux-concurrency-and-5M.md)):
   - **Disjoint concurrent load scales** — 8R/4W sustains 400k reads/s + 56k writes/s, **0 conflicts**.
   - **Same-key contention is brutal under SSI** — 8 writers read-modify-writing **one** key hit an **~85%
     conflict-retry rate** (the *problem*). **A single-writer gateway, built and benched, is the fix**: routing
     those same writes through one serializing writer with greedy-drain group commit drove **conflicts to 0
     and useful throughput to 1.8M ops/s** (coalesced into 53k txns/s, avg batch 34) — **~25× the uncoordinated
     path's useful commits**, because serialization turns contention into batching. On spread keys the single
     writer still served 477k ops/s — **not a bottleneck**. (NB: a *timer-based* flush throttles to ~1/period
     — the bench exposed that footgun in a first cut; greedy-drain is the correct pattern. Throughput here is
     macOS; conflicts=0 is by construction/OS-independent.)
   - **A durable (fsync) ack costs ~136 µs vs 3.7 µs — ~37×** — so a single writer tops out ~7.3k durable
     commits/s. That is exactly why the ADR's **group commit** is load-bearing: amortizing one fsync over a
     16–256 batch lifts effective durable throughput to ~120k–1.8M/s. "Ack after commit" is cheap *because*
     writes are coalesced.

7. **Scaling 1M → 5M holds up where it matters** — the **dormant** store grows only 39 → 91 MiB (table
   indexes mmap'd, paged cold), prefix-scan/`DropPrefix` stay O(function). What scales is the serving working
   set (0.6 → 2.3 GiB, reclaimable) and the **export peak (~1.6 → ~5.1 GiB, ~linear)** — at 5M the in-process
   backup neared the 6 GiB VM ceiling. **The export path, not the dataset, is the RSS scaling limit** — a
   point in favor of streaming/segmented export (or doing it out-of-process) at large KV scale.

8. **Durability & CDC are now proven, not asserted** ([results/durability-and-cdc.md](results/durability-and-cdc.md)):
   - **Incremental backup ships only the delta** — after a 50k-key delta, `Backup(w, cursor)` emitted 25% of
     the full backup's bytes. (RSS nuance: it shrinks data *materialized*, but each export run still spins the
     parallel buffer pool — the full re-baseline is the one spike, capped by lowering `Stream.NumGo`.)
   - **Restore works** — `db.Load` reconstructed 250,000/250,000 keys into a fresh DB. The durable path is real.
   - **`Subscribe` is lossy** — proven: writes during a subscriber-down gap are never delivered. It's a latency
     trigger, not a durable feed.
   - **Robust CDC survives a killed consumer** — a transactional outbox (the change-log entry written in the
     same txn as the data, consumer tailing from a durable cursor) was killed mid-stream and resumed with
     **zero loss, zero dup** (100k/100k). Cost: ~2× write amplification. Design in the linked doc.

### The decision lens (unchanged in shape, softened on memory)
The RSS worry that would have argued *against* Badger is largely **resolved** by the Linux data — it fits the
RAM-bound target with room to spare, at both metastore and 1M-key KV scale. What remains is the **non-memory**
trade the storage ADR already names: Badger gives a near-perfect fit to funcd's existing pure-KV `Engine`
port (`View`/`Update`/`Txn` ≈ 1:1) plus native TTL/encryption, but its durability/export path is **code you
own**, whereas SQLite+Litestream keeps durability off-process. Badger now looks like a **viable, low-risk
drop-in for the metastore** (kills cgo + the S3 coupling, restores the pure-Go static binary); the harder
SQLite-vs-Badger call is really about *who owns durability* for the broader KV service, not about RAM.

## Caveats
- GOMAXPROCS=4 here; the target is 8-core — Stream `NumGo`/compaction scale with it.
- `db.Size()` reads 0 (metrics update on flush); the dir total is the real on-disk figure.
- 5M export peak (~5.1 GiB) neared the 6 GiB test VM ceiling; 10M would need a larger VM (or
  out-of-process export) — not yet run.
