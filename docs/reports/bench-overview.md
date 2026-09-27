# funcd — benchmark overview (V1 sustainability)

**Question** (the V1 target, [blueprint](../../blueprint.md)): on one Linux box (8-core / 18 GB), can funcd run **~100
lightweight agents/functions** — event-driven, bursty, scale-to-zero — with headroom?

> **Derived from the raw bench output** [`report.json`](report.json) / [`report.md`](report.md) — plus the separate
> worker-pool comparison [`pool-report.md`](pool-report.md) (ADR-0044) — the machine output of `funcd-bench`
> ([ADR-0040](../adr/0040-benchmark-sustainability-harness.md)). **Regenerate the raw reports and this overview with the
> `bench-overview` skill** (or `just bench` for the raw reports alone).
>
> **Status:** dev machine (macOS, process-driver shim, local Node 23; the runtime image pins Node 22 per ADR-0039).
> Numbers below are **process RSS**. The honest container footprint is the opt-in **containerd cgroup lane**
> ([ADR-0052](../adr/0052-bench-containerd-cgroup-footprint-lane.md), `funcd-bench --containerd`), now **measured on
> homebox** (~15.7 MB marginal cgroup/fn → ~1043-fn ceiling; see *Containerd cgroup footprint* below — and note RSS
> *over*-counts for density, so the cgroup ceiling is *higher* than RSS). Bench numbers vary run-to-run (load, machine
> state); treat them as orders of magnitude, not exact.

## Method

`funcd-bench` embeds the platform ([ADR-0014](../adr/0014-platform-facade-lifecycle-harness.md)), drives the **public
data-plane HTTP** like a real client (Go-native load — no external tool), and samples memory **externally** (process RSS,
not self-reported). It compares the **memory** and **file substrate** (blob `mem://`↔`file://` + NATS memory↔file
JetStream; the store is memory in both). Scenarios: throughput + tail latency, per-worker & idle RSS, cold-start (wake),
and a density sweep → a fits-the-budget verdict.

## Headline numbers (latest run — 8 workers × 5s, Node process-driver shim)

| metric | memory substrate | file substrate |
|---|---|---|
| throughput | **~26.8k req/s** | **~27.3k req/s** |
| latency p99 | ~0.6 ms | ~0.6 ms |
| platform baseline RSS | **~34 MB** | **~49 MB** (durable file-JetStream) |
| per-worker RSS | **~54 MB** | ~54 MB |
| idle (scaled-to-zero) RSS | **~0 MB** | ~0 MB |
| cold-start (wake) | **~230 ms** | ~230 ms |
| marginal MB / function | ~58 MB | ~58 MB |
| **pooled MB / function** (worker_threads, ADR-0044) | **~20 MB** | ~20 MB |
| **pool density gain** | **~2.9×** | ~2.9× |
| max density (16 GB budget) | **~282 fns** | ~281 fns |
| **fits ~100 agents?** | **✅ yes** | **✅ yes** |

## Verdict: sustainable

- **Density.** ~54 MB per running worker → 100 agents ≈ **5.4 GB** working set, well within 18 GB. Extrapolated ceiling
  ~282 functions before the 16 GB budget — **~2.8× headroom** over the 100-agent target.
- **Scale-to-zero pays off.** Idle RSS is **~0 MB** — reclaim *frees* a function's worker memory, it doesn't merely stop
  serving. With bursty agents, the *resident* working set is the active subset, not all 100, so the practical ceiling is
  higher still.
- **Cold-start is a genuine wake.** ~230 ms (buffer → ScaleTo(1) → fresh Node shim spawns → ready → forward) — fine for
  scale-to-zero agents. *(Was previously reported ~52 ms; that measured a not-fully-cold function before ADR-0047 fixed a
  reconcile-storm that kept idle functions churning. With true quiescence the function genuinely scales to zero, so the
  wake is an honest cold Node spawn — ~230 ms, still well within budget for bursty agents.)*
- **Throughput + tail.** ~27k req/s at p99 ≈ 0.6 ms per warm function on a dev laptop — far above lightweight-agent traffic.

## Validated on the target box (homebox — 8-core / 15 GiB Ubuntu, Node 18)

The headline table above is the dev laptop (14-core macOS). The numbers that decide V1 are the **actual deployment target**:
a bare 8-core / 15 GiB Linux box — the blueprint's "one Linux box". Cross-compiled `funcd-bench` (static linux/amd64) +
the two shim bundles, run on the box directly:

| metric | dev laptop (14-core macOS) | **homebox (8-core / 15 GiB Linux)** |
|---|---|---|
| throughput / fn | ~27k req/s | **~5.9k req/s** (slower per-core CPU; still ≫ bursty-agent traffic) |
| latency p99 | ~0.6 ms | **~3.2 ms** |
| per-worker RSS | ~54 MB | **~60 MB** |
| idle (scaled-to-zero) RSS | ~0 MB | **~0 MB** |
| cold-start (wake) | ~230 ms | **~231 ms** |
| max density (16 GB budget) | ~282 fns | **~252 fns** |
| **pool density gain** | ~2.9× | **~3.5×** (heavier Node baseline → more to amortize) |
| pooled vs single-tenant throughput | comparable (high-variance baseline) | **75% retained** (single 9.0k → pooled 6.7k — the expected hop direction) |
| **fits ~100 agents?** | ✅ | **✅ yes** |

**Verdict on real hardware: sustainable.** The 100-agent target lands with **~2.5× headroom** (252-fn ceiling), idle
functions cost **0 MB**, and it fits on both substrates. Two cross-validations worth recording: **cold-start ~231 ms
matches the laptop's ~230 ms** — confirming that figure is the honest post-[ADR-0047](../adr/0047-control-loop-quiescence-and-chaos-tests.md)
cold Node spawn, not a per-machine artifact; and the **pool throughput comparison reads cleanly here** (single > pooled =
75%), so the laptop's wild single-tenant swing was a fast-CPU measurement artifact, exactly as the pool report cautions —
density (3.5×) is the decisive, stable result on the target. (Node 18 ran the shims fine even though the image pins
Node 22 per [ADR-0039](../adr/0039-pin-node-runtime-to-node22.md).)

## Worker pooling — density vs throughput (ADR-0044)

> Full head-to-head: [`pool-report.md`](pool-report.md) / [`pool-report.json`](pool-report.json) — the separate
> worker-pool comparison `funcd-bench` emits (density **and** throughput, pooled vs per-function).

The per-worker ~54 MB is mostly the Node runtime, not the handler. Hosting K **same-namespace** handlers in one process
via `worker_threads` (one V8 isolate + a `resourceLimits` memory quota + crash isolation per handler) amortizes that
baseline. The bench measures **both** axes at K=8 — but they are **not equally reliable**. Density (RSS) is the stable,
decisive result. Throughput is measured by hitting each shim **directly on its own port** (no gateway/activator), but the
single-tenant baseline is **one short-lived single-process shim and is high-variance run-to-run** (≈15–65k req/s across
runs on this dev box — single-threaded throughput is far more sensitive to JIT/core/GC timing than the pool's long-lived
worker threads). So the throughput pair shows the two are **comparable within that noise**, not a precise ratio:

| | single-tenant (direct) | pooled (worker_threads, direct) |
|---|---|---|
| memory MB / function | ~58 MB | **~20 MB** (≈ **2.9× density**) |
| throughput at one handler | ~15–65k req/s *(high-variance baseline)* | **~47k req/s** *(stable)* |
| latency p99 | ~0.4–4 ms | **~0.4 ms** |

So **pooling buys ~2.9× density at no meaningful throughput cost**: the pooled handler holds a stable ~47k req/s — on par
with a solo shim within the baseline's wide variance — and the `worker_threads` structured-clone hop is sub-millisecond.
(The earlier "~73% retained / ~27% cost" read was an artifact of one high single-tenant sample; the baseline is too noisy
to support a precise ratio, so the honest statement is *comparable*, with density as the real win.) The hop only bites
when **one** handler is driven at sustained heavy concurrency (its requests serialize on a single worker event loop); at
the V1 target — ~100 bursty, low-QPS agents, ~1 in-flight each — that's a non-issue. The density is real and *with*
isolation (per-handler fault + memory quota). Other trade-offs: a *process* crash takes the whole pool down, no native
per-thread CPU cap, and same-namespace only (`worker_threads` is a fault/resource boundary, not a security one).
**Pool when memory is the binding constraint and the handlers are same-namespace; stay per-function for
isolation-critical or sustained-high-concurrency functions.** The placement policy that *decides* to pool is
ADR-0046 (`spec.pooling.worker`); this is the host + the measured trade.

## Python worker pooling — subinterpreters (ADR-0050)

Python gets its own pool host (funcd-python's `shim/.../pool.py`): N handlers in one process, each in its **own
subinterpreter** (one `InterpreterPoolExecutor(max_workers=1)` per handler, `concurrent.interpreters`, Python ≥3.14) —
per-interpreter GIL + module-state isolation, the Python analog of Node `worker_threads`.

**Measured by `funcd-bench` (the real `pool.py` vs `shim.py`, hit directly — `--python <3.14>`, K=8, dev box, HTTP/1.1
keep-alive):**

| metric | solo (`shim.py`, 1 fn) | pooled (`pool.py`, K handlers) |
|---|---|---|
| memory MB / function | ~23 | **~11 (≈2.1× density)** |
| throughput (req/s) | **~22–23k** (p99 ~1 ms) | **~11–12k** aggregate over K (p99 ~2 ms) |

**~2.1× density with isolation** is the decisive, stable result — pooling roughly halves per-function memory (the full
`funcd_shim`+JTD engine loads into each subinterpreter, so the per-handler floor is ~11 MB, not the bare-handler ~6 MB an
early isolated spike suggested). **Throughput is healthy** — the pool retains ~50% of a solo function's aggregate, the
cost being the cross-interpreter dispatch hop per request (~0.02 ms/call in isolation), *not* the HTTP layer.

> **A real bug this surfaced.** An early reading showed an alarming **~12 req/s**. The cause was **not** the
> subinterpreters: the shims' stdlib `http.server` defaulted to **HTTP/1.0**, closing the TCP connection after every
> response → a handshake per request + `TIME_WAIT` pile-up that collapsed under load (and silently defeated the ADR-0041
> upstream connection pool for Python functions in production). Setting **`protocol_version = "HTTP/1.1"`** (keep-alive;
> safe — every response carries `Content-Length`, and the request body is drained before any early return) took it from
> **~12 → ~23k req/s**. The bench load client was also pinned to one keep-alive connection per worker to measure
> steady-state honestly.

Python's stdlib `http.server` still caps absolute req/s below the Node shim's C++ HTTP server (~23k vs ~71k solo —
expected), but that is comfortably above the V1 target (~100 bursty low-QPS agents, ~1 in-flight each). The mechanism was
chosen on an earlier spike that also showed the **alternatives**: a *threaded* pool reaches ~8× density but with **zero
isolation** (rejected for the same reason ADR-0044 rejected a shared event loop), and CPU-bound co-pooled handlers
**scale across cores** via the per-GIL property (~5.9× a shared-GIL threaded pool) — a win neither solo Python nor the
Node `worker_threads` pool offers. Needs Python ≥3.14 for *pooled* Python; solo Python (ADR-0049) keeps a lower floor, and
on a sub-3.14 box Python functions run solo. Same placement policy (ADR-0046), same `FUNCD_POOL_MANIFEST` + wire contract
+ JTD validation as Node. Raw: [`py-pool-report.md`](py-pool-report.md) (`funcd-bench --python <3.14>`).

## What the bench found and fixed

1. **The data plane exhausted ephemeral ports under load** (`connect: can't assign requested address`). Cause: the
   activator's `forward` built a reverse proxy **per request** on Go's default transport (2 idle conns/host), dialing +
   discarding a connection per request → `TIME_WAIT` pile-up → port starvation, which also starved reconcile. **Fixed** by
   a shared, pooled upstream transport on the activator + gateway ([ADR-0041](../adr/0041-gateway-upstream-connection-pooling.md)).
2. **The "file substrate is 5× slower" result was a measurement artifact, not disk.** Before the fix, file measured ~3k vs
   memory ~17k req/s. But the **invoke path never touches the blob/bus/store** (the artifact is materialized + cached at
   deploy; the bus is the *eventing* path; the store is memory in both). The 5× gap was the un-pooled proxy starving
   whichever substrate ran *second* of ports. **After pooling the port exhaustion is gone (0 errors)** and the gap
   collapses — a short run measures the substrates ~equal; a longer run leaves a **modest residual (~25–30%)** that is
   *platform-side*, not the request path: the file substrate carries a higher **baseline RSS (~49 vs 30 MB)** and some
   background fsync from file-backed JetStream, plus the second-run substrate still sees mild port pressure. Disk is **not**
   a data-plane invoke bottleneck. (Pooling is tenant-safe: the pool is keyed by per-replica upstream `host:port`, so a
   connection is never reused across functions — ADR-0041 §Multi-tenancy & security.)

## Containerd cgroup footprint — the true production number (ADR-0052)

Every number above is **process RSS** from the process-driver shim — a bare `node shim.mjs` child, no container. In
production a function runs as the entrypoint of a **crun container under containerd** (ADR-0032) with its own cgroup +
netns. The opt-in **`funcd-bench --containerd`** lane boots funcd over that real path (gateway → activator → containerd
worker in a netns → shim) and reports each container's **cgroup-v2 `memory.current`** to a separate
[`footprint-report.md`](footprint-report.md). It is Linux + root only and **skips cleanly** (never fails the run) anywhere
the path is absent (macOS dev, or a Linux box missing the socket/root/image); the process lane above is unchanged.

**Measured on homebox** (8-core / 15 GiB Ubuntu 24.04, cgroup v2, containerd 2.2.1 + crun 1.28, density sweep, real crun
containers — [`footprint-report.md`](footprint-report.md)):

| metric | containerd cgroup (homebox) |
|---|---|
| **marginal cgroup memory / function** | **~15.7 MB** |
| marginal process RSS / function (same containers) | ~60.6 MB |
| cgroup ÷ RSS | **0.26×** |
| platform baseline | ~45 MB |
| throughput / fn (warm) | ~3.1k req/s, p99 ~5 ms |
| **max density (16 GB budget)** | **~1043 fns** |
| fits ~100 agents? | ✅ **yes**, with ~10× headroom |

**The headline finding — RSS *over*-counts, it isn't optimistic for density.** The marginal cgroup cost of one more
function (~16 MB) is **0.26×** its process RSS (~60 MB) — *lower*, not higher. All functions share one curated image, so
the runtime's read-only pages (the node binary, libs, the shim) are charged **once** to the first container and shared by
the rest via the page cache; per-process RSS counts them in **every** process. So the real density ceiling (**~1043**) is
*higher* than the process-RSS lane's ~250 suggested. (The *absolute* first container is still > one RSS — page cache +
kernel — but that one-time cost amortizes across the fleet.) This nuance is the whole reason the lane exists: it corrects
the "process RSS is optimistic" caveat — for **density** it's the opposite.

**What standing up the real path surfaced.** This lane was the **first** exercise of the ADR-0032 containerd execution
path on hardware (its integration test was deferred), and it found three real funcd bugs + one environment requirement,
all now fixed:

1. **Non-absolute OCI `process.cwd`** — `WithImageConfig` leaves it empty for a WORKDIR-less image; runc tolerates it but
   **crun rejects** `"." must be absolute`. Fixed: the driver defaults cwd to `/` (`withAbsoluteCwd`).
2. **Empty container log path** — container mode never set `WorkerSpec.LogPath`, and the driver passed `""` to
   `cio.LogFile`; the shim rejects a non-absolute log URI. Fixed: the driver honors the `"" → temp file` contract.
3. **Artifact unreadable by the container user** — the materializer wrote the artifact `0600` under `0750` dirs, but the
   curated image runs as `USER node` (uid≠0), which can't read it (`Cannot find module`). Fixed: artifacts are
   container-readable `0644`/`0755` (non-secret read-only code).
4. **(env) crun too old** — Ubuntu 24.04's crun 1.14.1 only knows OCI spec 1.0.0 and rejects the 1.3.0 containerd v2
   stamps (`unknown version specified`); homebox needs **crun ≥ ~1.20** (installed 1.28 from the crun GitHub release).

**Homebox runbook** (the one-time provisioning that produced the numbers above):

```bash
# on the target box (Linux, root):
sudo apt-get install -y containerd crun containernetworking-plugins buildah nodejs
# crun from apt (1.14) is too old for containerd v2's OCI 1.3 spec — install a recent static build:
curl -fsSL -o /usr/bin/crun https://github.com/containers/crun/releases/download/1.28/crun-1.28-linux-amd64 && chmod +x /usr/bin/crun
# CNI: bridge+firewall conflist funcd loads (default-deny lateral) + the plugin dir it expects
sudo ln -s /usr/lib/cni /opt/cni/bin   # ubuntu installs plugins under /usr/lib/cni
sudo install -d /var/lib/funcd/cni/conf && sudo tee /var/lib/funcd/cni/conf/10-funcd.conflist >/dev/null <<'JSON'
{ "cniVersion":"1.0.0","name":"funcd","plugins":[
  {"type":"bridge","bridge":"funcd0","isGateway":true,"ipMasq":true,
   "ipam":{"type":"host-local","ranges":[[{"subnet":"10.63.0.0/16"}]],"routes":[{"dst":"0.0.0.0/0"}]}},
  {"type":"firewall"} ] }
JSON
# curated image: build (no docker on the box → buildah) and serve it from a local registry, because the
# driver client.Pull resolves the ref (containerd's default resolver uses plain-HTTP for localhost):
sudo ctr -n default run -d --null-io --net-host docker.io/library/registry:2 funcd-reg /entrypoint.sh /etc/docker/registry/config.yml
sudo buildah bud -f images/runtime/nodejs22/Dockerfile -t localhost:5000/funcd/runtime-nodejs22:latest .
sudo buildah push --tls-verify=false localhost:5000/funcd/runtime-nodejs22:latest
# run the lane (cross-compiled static binary: CGO_ENABLED=0 GOOS=linux go build ./cmd/funcd-bench):
sudo ./funcd-bench --containerd --image-prefix localhost:5000/funcd/runtime- \
  --cni-conf-dir /var/lib/funcd/cni/conf --cni-bin-dir /opt/cni/bin --density 4
```

## Caveats & next

- **Process RSS vs cgroup — now measured.** The headline tables are process RSS; the **true** container footprint is the
  opt-in `--containerd` cgroup lane ([ADR-0052](../adr/0052-bench-containerd-cgroup-footprint-lane.md)), measured on homebox
  (section above): **~15.7 MB marginal cgroup/fn, ~1043-fn ceiling**. The surprise — for *density*, RSS *over*-counts
  (shared image pages), so the cgroup ceiling is *higher* than the RSS proxy, not lower. The higher-density sweep is
  somewhat flaky on a hand-provisioned box (per-container registry pulls + CNI; leaked shims from killed runs need a
  `systemctl restart containerd` between sweeps); `--density 4` reproduces cleanly.
- **Bench fairness** — substrates run sequentially in one process, so the second sees mild residual port/TIME_WAIT
  pressure; a future harness tweak (warm-up discard / per-substrate isolation) would tighten the comparison.
- **Node-only** — Python runtime is pending; re-bench both runtimes once the Python shim ships.
- **Dev machine** — re-run on the actual 8-core/18 GB target for the authoritative numbers.
