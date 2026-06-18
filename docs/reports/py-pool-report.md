# funcd Python worker-pool comparison (ADR-0050)

K=8 handlers in one subinterpreter pool host (`pool.py`) vs a solo `shim.py` — both hit directly (no data plane), HTTP/1.1 keep-alive. Process RSS, dev box; Python 3.14.5.

| metric | single fn | pooled (×8) |
|---|---|---|
| memory MB / function | 23.4 | 11.3 |
| density (functions / GB) | 1× | 2.1× |
| throughput (req/s, ~1 in-flight/fn) | 19667 | 1582 *(avg/handler)* |
| latency p99 | 236µs | 1.94ms |

~2.1× density *with* per-interpreter isolation + per-GIL CPU parallelism; needs Python ≥3.14. Pool when memory is the binding constraint.
