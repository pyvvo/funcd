# Badger bench — durability, robust CDC & the single-writer gateway (proven, not asserted)

## Single-writer gateway (the contention fix, measured)

The contended-hot-key test shows the *problem*; this is the *solution*, built and run on the same load
(`singleWriterGateway` — one serializing writer, greedy-drain group commit):

| same hot-key load | conflicts | useful throughput |
|---|--:|--:|
| uncoordinated `db.Update` per client (the problem) | **~85% retries** | 72k commits/s |
| **single-writer gateway** (hot key) | **0** | **1.8M ops/s** → 53k txns/s (avg batch 34) |
| single-writer gateway (spread keys, realistic) | **0** | 477k ops/s → 15k txns/s |

The gateway eliminates conflicts *and* is ~25× more useful throughput on the hot key — serialization turns
contention into batching. The single writer is not a bottleneck (477k ops/s spread). A first cut used a
2 ms flush *timer* and throttled to ~7k ops/s — the bench caught that footgun; **greedy drain** (commit
immediately, coalescing whatever queued during the last commit — no fixed timer) is the correct pattern.
Conflicts=0 is by construction (no concurrent txns); throughput numbers are macOS.

---



Run: `go run . -durability -keys 200000 -cdcn 100000`. These are **correctness proofs** (verified outcomes),
which are OS-independent; absolute RSS is macOS here (see the perf caveats). Each row is a `VERIFIED ✓`/`LOST`
outcome, not a claim.

## What was proven

| proof | result | meaning |
|---|---|---|
| **incremental backup** | full = 56.3 MiB; after a 50k-key delta, incremental = **14.3 MiB (25% of full)** | `Backup(w, cursor)` ships **only the delta**, not the whole DB |
| **restore round-trip** | backup → `db.Load` into a fresh DB → **250,000 / 250,000 keys VERIFIED ✓** | the durable path actually restores |
| **Subscribe lossiness** | #1 saw A=5000, #2 saw C=5000, **gap B=5000 LOST** | `Subscribe` has **no durable resume** — useless alone for robust CDC |
| **transactional-outbox CDC** | 100k changes, consumer **killed after 50k**, resumed from cursor → **100,000 / 100,000 distinct, 0 dup, VERIFIED ✓** | a change-feed that **survives consumer death with zero loss** |

Costs measured: the outbox write path (data + `_cdc/` entry in one txn) ran at **136k writes/s** vs ~700k for a
plain write — i.e. roughly **2× write amplification**, the price of an atomic change log. (The proof consumer
persists its cursor per-entry, which is slow — ~5 s/100k; production batches the cursor.)

## The robust CDC design (what survives a killed subscriber)

`Subscribe` is in-memory and post-commit with no durable resume — proven lossy above. The robust answer is a
**transactional outbox**:

1. **Atomic change log.** Every write does, in **one** `db.Update` txn: `txn.Set(dataKey, value)` **and**
   `txn.Set("_cdc/"+seq, changeRecord)`. Same transaction ⇒ the log entry is atomic with the data — no
   dual-write skew, ever. `seq` is a durable monotonic counter (`db.GetSequence`, leased in bands).
2. **Durable cursor.** The consumer tails `_cdc/` in order from a cursor it **persists in the store**
   (`_cdc_cursor/<id>` → next seq). On restart it re-reads the cursor and resumes — so a kill at any point
   loses nothing (proven: killed mid-stream, resumed, 0 loss / 0 dup).
3. **Delivery semantics.** At-least-once + durable cursor + an idempotent consumer = effectively exactly-once.
   (Per-entry cursor persist → 0 dup as shown; a batched cursor trades that for bounded re-delivery of the
   last batch on crash.)
4. **Retention / GC.** Trim `_cdc/` below the min of all consumer cursors, **or** give entries a native Badger
   **per-key TTL** for a bounded window (a consumer down longer than the window does a full re-sync: snapshot
   + resume). TTL is simpler and bounds storage; min-cursor GC is exact.
5. **Subscribe stays — as a *trigger*.** Use `Subscribe` (debounced) only to wake the tailer for low latency;
   correctness lives entirely in the outbox tail, never in Subscribe.

**Trade-offs:** ~2× write amplification + log storage + a GC policy. So build it **only where a durable feed is
actually needed** — not for funcd's internal metastore watch (a restarted controller just re-lists current
state from the store, no gap), but for **multi-node replication** (the V2 NATS-lattice) and any **external
change consumer**. The point of this proof: when that need lands, the mechanism is shown to work on Badger.

## Corrected note on incremental backup & RSS

Incremental backup **provably ships only the delta** (25% bytes here) — so frequent small incrementals move
little data. But each `Backup`/`Stream` run still spins up the parallel export machinery (pooled `z.Buffer`s),
so there is a **fixed per-run buffer-pool cost** independent of delta size; incremental shrinks the *data
materialized*, not that floor. The expensive event remains the periodic **full re-baseline** — bound its RSS
by lowering `Stream.NumGo` + batch size (background job; trade speed for footprint). Net: incremental backup
keeps the steady durability cost small; the re-baseline is the one spike to schedule and cap.
