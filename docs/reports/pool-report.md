# funcd Node worker-pool comparison (ADR-0044)

K=8 handlers in one `worker_threads` pool vs a single-tenant shim — both hit directly on the shim port (no data plane), HTTP/1.1 keep-alive. Process RSS, dev box (ADR-0040 caveats).

| metric | single fn | pooled (×8) |
|---|---|---|
| memory MB / function | 57.7 | 20.3 |
| density (functions / GB) | 1× | 2.8× |
| throughput (req/s, ~1 in-flight/fn) | 34484 | 6425 *(avg/handler)* |
| latency p99 | 253µs | 991µs |

~2.8× density (the win). Per-handler throughput is lower than a dedicated shim (the pool's shared dispatch event loop) but ample for bursty low-QPS agents. Same-namespace only.
