# funcd sustainability report (ADR-0040)

> **What this is:** the *sustainability* verdict — can the box run ~100 agents? — per **substrate** (`memory` vs `file`). The single-vs-pooled density/throughput comparisons live in `pool-report.md` (Node) and `py-pool-report.md` (Python).

> Per-worker memory is **process RSS**, not container cgroup memory — a lower-bound proxy, so the fits-target verdict is **optimistic** (ADR-0040). The store is memory in both substrates.

| metric | memory | file |
|---|---|---|
| throughput (req/s) | 29310 | 29541 |
| latency p50 | 549.755µs | 549.902µs |
| latency p99 | 991.133µs | 991.099µs |
| latency p99.9 | 999.24µs | 999.203µs |
| latency max | 1.591875ms | 1.349708ms |
| platform baseline (MB) | 38.3 | 53.1 |
| per-worker RSS (MB) | 54.6 | 54.4 |
| idle RSS (MB) | 0.0 | 0.0 |
| cold-start (wake) | 229ms | 229ms |
| marginal MB/function | 57.7 | 57.8 |
| pooled MB/function (worker_threads) | 20.3 | 20.3 |
| pool density gain (×) | 2.8× | 2.8× |
| max density (fns) | 283 | 282 |
| fits target? | ✅ yes | ✅ yes |
