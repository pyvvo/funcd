# Contract-validation overhead (ADR-0058/0060)

Does the precompiled I/O contract validator add latency / cut throughput? Measured with the
microbench in [`bench/contract-validation/`](../../bench/contract-validation) — the **per-request
CPU cost** of the baked validator, isolated from network/handler noise (an end-to-end HTTP load
would bury a µs-scale cost under the round-trip). Two axes: the **raw validator** (ns/call) and the
**request path** `parse → [validate] → handler → serialize` with validation **ON vs OFF**.

Payload: a closed record `{ orderId: string, qty: int, tags: string[] }` — the validator generated
by the real build (Node AJV-standalone / Python fastjsonschema). N = 2M (Node) / 1M (Python).

## Results

| Runtime | Machine | validator ns/call | reqpath OFF ns | reqpath ON ns | **overhead ns** | overhead % | rps OFF | rps ON |
|---|---|--:|--:|--:|--:|--:|--:|--:|
| **Node** | mac arm64 · node 23 | 11.7 | 374.5 | 389.6 | **15.1** | 4.0% | 2.67M | 2.57M |
| **Node** | homebox amd64 · node 18 | 34.6 | 1495.8 | 1530.2 | **34.4** | 2.3% | 668k | 654k |
| **Python** | mac arm64 · 3.14 | 558.6 | 1576.3 | 2246.3 | **670.1** | 42.5% | 634k | 445k |
| **Python** | homebox amd64 · 3.12 | 1679.7 | 5061.6 | 7390.4 | **2328.9** | 46.0% | 198k | 135k |

(Lima not included — no VM provisioned at measure time; homebox is the real Linux/amd64 target, the
meaningful hardware number. The Lima arm64-VM point would fall between mac-native and homebox.)

## What it means

- **Node validation is ~free** — **15–35 ns per request** (AJV-standalone is JIT-compiled native
  code). It is **2–4 %** of the *CPU* request path, and that path is itself dominated by
  `JSON.parse`/`JSON.stringify`. Throughput drop: ~2–4 %.
- **Python validation is small but visible** — **0.7–2.3 µs per request** (fastjsonschema is
  generated Python, ~15–50× the cost of AJV's native code). It is **~42–46 %** of the *CPU* request
  path — but only because the Python baseline path is also tiny. Throughput drop in the microbench:
  ~30 %.
- **Crucial context — this is pure CPU, no network, a trivial handler.** A real request carries an
  HTTP round-trip (hundreds of µs to ms) and real handler work. Against a 1 ms real request the
  validator adds **< 0.01 %** (Node) / **< 0.3 %** (Python). Validation does **not** meaningfully
  add latency or cut throughput in any realistic workload.
- **Both are eval-free and compute-agnostic** — identical cost solo and in the pool (the validator
  is precompiled at build; AJV-standalone for Node, fastjsonschema for Python — no Rust, so it runs
  in the Python subinterpreter pool too).

## Takeaway

Opt-in I/O contracts are effectively free for Node and cheap for Python in absolute terms (single-
to low-µs per request), and negligible against real request latency. If Python micro-latency ever
mattered, fastjsonschema's generated code is already near the pure-Python ceiling; a native option
would be a future lever, not a v1.1 concern.

## Reproduce

```bash
# Node (self-contained):            node bench/contract-validation/node-bench.mjs
# Python (needs fastjsonschema):    python3 bench/contract-validation/python_bench.py
# homebox: scp the dir + the (pure-Python) fastjsonschema package; node / python3 run it directly.
```
