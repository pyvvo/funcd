# Badger bench — store topology: one instance vs many

**Question.** funcd runs several logical stores on Badger — the metastore (control-plane desired
state, ADR-0065), the workflow run-state (ADR-0094), the eventing DLQ (ADR-0118), and the KV
service (ADR-0066). The DLQ shipped as its *own* dedicated instance, raising the design question:
should each concern be its **own Badger instance**, or should they share **one instance with key
prefixes as tables** (`store/`, `run/`, `dl/`)? Backup argues for co-location (one instance = one
whole-DB `db.Backup` = one *consistent* recovery point — the Postgres "back up the database, not the
table" model); the counter-argument is that a shared instance couples the near-static metastore to
the high-churn ephemeral stores' compaction. This bench measures that coupling instead of guessing.

**Harness.** [`topology.go`](../topology.go), `-topology` mode. Models the churn asymmetry: a
near-static, read-heavy metastore (`-metakeys` keys) next to high-churn workflow/DLQ writes (DLQ
append + bounded sweep; run-state overwrite of a bounded working set → LSM garbage → compaction),
with a per-instance value-log-GC ticker. In **shared** all three handles point at one `*badger.DB`
(three prefixes); in **isolated** they are three independent instances at the funcd-tuned `lowmem`
profile. Reproduce:

```
go run . -topology -topo shared   -metakeys 50000 -readers 8 -writers 4 -churnrate 500 -concdur 5s
go run . -topology -topo isolated -metakeys 50000 -readers 8 -writers 4 -churnrate 500 -concdur 5s
```

Run `shared` and `isolated` as **separate processes** for clean RSS (RSS is process-global; the
`-topo both` convenience mode contaminates the second run's *idle* RSS — its throughput/latency and
per-mode RSS *delta* stay valid). Numbers below are macOS (Darwin, GOMAXPROCS=14); per the other
results docs macOS **overstates** retained RSS, so treat the absolute MiB as inflated and the
**shared-vs-isolated ratio** as the signal.

## Result 1 — realistic funcd churn (`-churnrate 500`, ~500 txn/sec)

The funcd-representative case: the DLQ and workflow engine commit at a modest rate, not a firehose.

| metric | shared (1 instance) | isolated (3 instances) | read |
|---|--:|--:|---|
| RSS delta on populate | 56 MiB | 56 MiB | identical |
| run peak RSS | **312 MiB** | 368 MiB | shared saves ~56 MiB |
| metastore read p50 | **1.7 µs** | **1.7 µs** | **identical — no penalty** |
| metastore read p99 | 134 µs | 98 µs | both sub-ms |
| metastore read throughput | 915k ops/sec | 1,074k ops/sec | both ≫ funcd need |
| churn write | 496 txn/sec | 494 txn/sec | rate-capped, equal |
| on disk | **161 MiB** | 483 MiB | 3× fixed baseline for 3 instances |
| backup | **31 ms · 1 consistent snapshot** | 35 ms · 3 snapshots, no cross-store point-in-time | shared wins |

**At funcd's real load the co-tenancy read penalty is zero** — metastore p50 is identical (1.7 µs).
One instance is at least as RAM-frugal (same idle delta, ~56 MiB lower run-peak), uses **⅓ the
on-disk baseline** (three instances each reserve their own value-log/WAL/SST preallocation), and
gives a single **consistent** backup where three instances cannot.

## Result 2 — saturated worst-case (`-churnrate 0`, writers flat-out)

The only regime where isolation helps — churn hammering the shared instance at 85k+ txn/sec, far
beyond anything funcd generates.

| metric | shared (1) | isolated (3) | read |
|---|--:|--:|---|
| RSS delta on populate | 55 MiB | 78 MiB | shared saves ~23 MiB |
| run peak RSS | 312 MiB | 415 MiB | shared saves ~100 MiB |
| metastore read p50 | 20.1 µs | **2.7 µs** | isolated **7.5× faster** |
| metastore read p99 | 135 µs | 140 µs | equal |
| metastore read throughput | 294k ops/sec | 648k ops/sec | isolated 2.2× |
| churn write | 85k txn/sec | 160k txn/sec | isolated 1.9× |
| backup | 59 ms · 1 snapshot | 94 ms · 3 snapshots | shared faster + consistent |

Under a write firehose the shared metastore reads *do* pay: p50 20 µs vs 2.7 µs (isolated's metastore
instance has no writers touching it). But even the penalized p50 is **20 µs**, p99 stays sub-ms, and
throughput is still ~300k reads/sec — orders of magnitude past funcd's control-plane read rate. And
funcd never reaches this churn regime; the penalty is a saturation artifact, not an operating point.

## Verdict — one instance is enough

- **RAM (funcd's binding constraint): one instance wins** in every regime — same idle floor, lower
  run-peak, and ⅓ the on-disk baseline (each extra instance multiplies Badger's fixed
  memtable/cache/compactor/vlog overhead).
- **Metastore read latency: no penalty at funcd's load.** The co-tenancy cost only appears when
  churn *saturates* (≥85k txn/sec) — a regime funcd doesn't reach — and even then p50 is 20 µs.
- **Backup: one instance wins decisively** — a single whole-DB `db.Backup` is one consistent
  recovery point across metastore + run-state + DLQ; three instances give three snapshots with no
  cross-store point-in-time.

The dedicated DLQ instance (ADR-0118) is not justified by these numbers; the disciplined layout is
**one funcd-internal instance** (metastore + workflow + DLQ as prefixes, backed up whole), with the
**KV service** separate only for its independent changefeed/restore lifecycle (ADR-0066/67/68) — not
for backup or isolation. Revisit only if a *measured* churn regime approaches saturation.

Artifacts: [`topology-realistic-500tps.json`](topology-realistic-500tps.json),
[`topology-saturated.json`](topology-saturated.json).
