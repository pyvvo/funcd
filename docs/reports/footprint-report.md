# funcd containerd footprint report (ADR-0052)

> **The true production footprint:** each function runs in a **real crun container under containerd** (gateway → activator → worker netns → shim), and the number below is the memory **actually charged to that container's cgroup** (`memory.current`) — which, for a shared curated image, differs from the per-process RSS in `report.md` in a way the note below explains.

| metric | value |
|---|---|
| **marginal** cgroup memory / fn (MB) | **15.7** |
| marginal process RSS / fn (MB) | 60.6 |
| cgroup ÷ RSS | 0.26× |
| platform baseline (MB) | 45.4 |
| throughput (req/s) | 3117 |
| latency p99 | 5ms |
| max density (fns within budget) | 1043 |
| fits target? | ✅ yes |

The **marginal** cgroup cost of one more function is **0.26×** its process RSS — *lower*, because functions share one curated image: the runtime's read-only pages (node binary, libs, shim) are charged **once** to the first container and shared by the rest via the page cache, whereas per-process RSS counts them in **every** process. So RSS *over*-counts shared pages and the real density ceiling (**1043** fns) is **higher** than the process-RSS lane suggests. (The *absolute* first-container footprint is still > one RSS — page cache + kernel — but that one-time cost amortizes across the fleet.)
