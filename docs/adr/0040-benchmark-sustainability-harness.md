# ADR-0040: Benchmark & sustainability harness (`funcd-bench`)

> **Superseded in part by [ADR-0055](0055-funcd-bench-subcommand.md)** — **packaging only**. The harness now ships as a
> **`funcd bench`** subcommand of the funcd binary, not the standalone `cmd/funcd-bench`. ADR-0040's *harness* (`internal/bench`,
> the embed-funcd / drive-the-data-plane / sample-RSS design) is **retained unchanged**; only the entrypoint moves.

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16** · **Implemented 2026-06-16** — judge folded: the Blocker — redefined the axis to memory-vs-file
  *substrate* (blob+bus, store stays memory; file store = slatedb/cgo, out) with the real `WithBlob`/`WithBus` wiring;
  cold-start timed only after 0 replicas; single-PID baseline; marginal-slope density; optimistic-verdict note.)
- **Deciders**: green-0-rabbit
- **Tags**: benchmark, performance, sustainability, observability, testing
- **Realizes**: [FEAT-0000/F27](../feat/0000-feat-v1.md) (benchmark & sustainability harness)
- **Relates to**: [ADR-0014](0014-platform-facade-lifecycle-harness.md) (embeds the platform as the bench server),
  [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (a new perf lane beside the correctness tiers),
  [ADR-0033](0033-data-plane-serving-wake.md) (the data plane under load), [ADR-0016](0016-activator-scale-to-zero.md)
  (cold-start + idle reclaim it measures), [ADR-0006](0006-store-database-layer-port.md)/[ADR-0007](0007-blob-storage-layer-port.md)
  (the memory vs file backends compared).

## Context & Need

We have a working platform; the open question is **sustainability on the target box** (8-core / 18 GB, ~100 lightweight
agents, RAM-bound, scale-to-zero). Correctness tests (ADR-0025/0034) don't answer it. We need a **repeatable harness** that
drives the platform like a real client and measures the numbers that decide viability — throughput, tail latency, **memory
per running sandbox**, idle/scaled-to-zero footprint, cold-start (wake) latency, and how many functions fit — across the
**memory and file substrate** (the durable blob + JetStream bus, both pure-Go; the file *store* is the slatedb/cgo lane,
out of scope — the store stays memory in both). The output is a report a human reads: *does ~100 agents fit 18 GB with headroom?*

## Scenarios

- **scenario: bench-throughput** — *given* a warm deployed function, *when* N workers POST to the data plane for a
  duration, *then* the harness reports sustained **req/s** and latency **p50/p90/p99/p99.9/max**.
- **scenario: bench-memory** — *when* the function is warm, *then* the harness samples and reports **per-sandbox RSS**
  (the running shim process) and the **platform baseline RSS** (funcd idle).
- **scenario: bench-idle** — *given* a scale-to-zero function, *when* it idles past its timeout, *then* the harness
  reports its **idle RSS** (should approach the baseline — proving reclaim frees memory, not just stops serving).
- **scenario: bench-coldstart** — *given* a scaled-to-zero function, *when* a cold request arrives, *then* the harness
  reports the **wake latency** (request → first response: activator buffer → ScaleTo(1) → shim ready → forward).
- **scenario: bench-density** — *when* K functions are deployed warm, *then* the harness reports the **marginal RSS per
  function** (`(RSS_at_K − baseline) / K`) and extrapolates **max density** within a memory budget (~100 agents vs 18 GB → fits / doesn't).
- **scenario: bench-substrate** — *when* the run is repeated for the `memory` and `file` substrate (blob + bus), *then* the
  report compares throughput + memory for each; the store stays memory in both.
- **scenario: bench-report** — *when* a run completes, *then* it emits a **markdown + JSON** report with the numbers and a
  one-line sustainability verdict; a node-gated **smoke** run asserts the report is populated (RPS>0, percentiles set,
  per-sandbox RSS>0, cold-start measured).

## Scope

**In:** a Go-native harness (`internal/bench` + the `cmd/funcd-bench` shell + a `just bench` recipe) that embeds funcd
(process-driver shim, ADR-0014), drives the **public data-plane HTTP** (`POST /function/<name>`), and samples process
**RSS**; the seven scenarios above; the **memory vs file substrate** (blob `mem://`↔`file://`, NATS memory↔file storage);
a markdown + JSON report; a node-gated smoke test.

**Out:** the file **store** (slatedb is cgo / `-tags slatedb` — the store stays `memory.New()` in both substrates; a
slatedb store-comparison is a tagged Linux lane, deferred); container/cgroup per-sandbox memory (the **Linux** containerd
lane — process RSS is the dev/CI proxy, noted); reading the platform's own Prometheus metrics (external RSS is ground truth
here); CPU profiling; multi-node load; a CI perf-regression gate (this harness is its prerequisite).

## Constraints & Decision drivers

- **Reproducible, no external dependency** — the load generator is **Go-native** (goroutines against the data plane), not
  an external `k6`/`vegeta`/`wrk` binary — matching the single-binary / minimal-dep ethos and keeping the harness in-repo.
- **Measure the public surface** — load hits the data-plane HTTP exactly as a real client; memory is sampled externally
  (RSS), not self-reported — ground truth, not the platform grading itself.
- **Runs on dev and the target** — process-driver RSS is measurable on macOS + Linux; container cgroup memory is the Linux
  lane (deferred), so the harness is useful day-to-day and on the real box.
- **Answers the actual question** — the report's verdict is framed as *fits the RAM budget for ~100 agents or not*.

## Alternatives considered

- **External load tool (k6 / vegeta / wrk)** — mature, rich. Rejected: adds an external binary the bench depends on; a
  Go-native driver is reproducible, in-repo, and enough for HTTP throughput + percentiles. (Revisit if we need complex
  scripted workloads.)
- **`go test -bench`** — rejected: micro-benchmark-shaped; can't boot the platform, drive HTTP, sample sandbox RSS, and
  measure cold-start as one whole-system run. The harness is a system bench, not a function microbench.
- **Trust the platform's own metrics** — complementary, not a substitute: external RSS + client-side latency are the
  honest numbers; reading platform metrics can enrich the report later.

## Decision

Build **`funcd-bench`** — a Go-native, embeds-funcd harness:
1. **`internal/bench`** owns the logic: `Run(ctx, Config) (Report, error)` boots funcd in the chosen backend, deploys the
   scenario functions, runs the load + memory + cold-start + density scenarios, and returns a typed `Report`.
2. **Load** is a concurrent Go driver (N workers POST to the data plane for `Duration`), recording per-request latency →
   RPS + percentiles (sorted samples; no histogram dep).
3. **Memory** is sampled externally: sum RSS of the shim processes (discovered by the shim path) for per-sandbox + total,
   and the funcd process's **own** RSS (single PID — never a process-tree sum, so shim children aren't double-counted) for
   the baseline — via `/proc/<pid>` on Linux, `ps -o rss=` elsewhere.
4. **Substrate (memory vs file)**: both runs use `InMemory()` (memory store + process runtime); the **file** run overrides
   the durable substrate — `WithBlob(gocloud.Open(ctx, "file://"+dir))` + `WithBus(nats.Open(ctx, nats.Options{Storage:
   nats.FileStorage, StoreDir: dir}))` (both pure-Go; `internal/bench` imports the `internal/blob/gocloud` + `internal/bus/nats`
   drivers directly). The report compares the two; the store is `memory` in both (the file store = slatedb/cgo, out).
5. **`cmd/funcd-bench`** is the thin shell (flags: backend(s), concurrency, duration, density, mem-budget, out-dir) that
   calls `bench.Run` and writes the **markdown + JSON** report. **`just bench`** runs both backends.

## Temporary workarounds

- **Per-sandbox memory is process RSS, not container cgroup memory**, until the Linux containerd lane is wired. **Exit
  criterion:** a follow-up adds cgroup `memory.current` sampling under the containerd driver (Linux), reported alongside
  process RSS. Process RSS is a sound lower-bound proxy for the dev/CI numbers.

## Contracts

### `internal/bench`
```go
type Backend string // "memory" | "file" — the durable substrate (blob + JetStream bus); the store is memory in both

type Config struct {
	Backends    []Backend     // which backends to run (default: memory, file)
	Concurrency int           // load workers
	Duration    time.Duration // load phase per backend
	Density     int           // functions deployed for the density sweep
	MemBudgetMB int           // function-memory budget for the verdict (e.g. 16384)
	TargetFns   int           // agents to size for (e.g. 100)
}

type Latency struct{ P50, P90, P99, P999, Max time.Duration }

type Report struct {
	Backend            Backend
	RPS                float64
	Latency            Latency
	PlatformBaselineMB float64
	PerSandboxMB       float64
	IdleMB             float64       // RSS scaled-to-zero (≈ baseline ⇒ reclaim works)
	ColdStart          time.Duration // wake → first response (timed only after the fn reached 0 replicas)
	PerFunctionMB      float64       // marginal slope: (RSS_at_K − baseline) / K
	MaxDensity         int           // functions within MemBudgetMB
	FitsTarget         bool          // TargetFns within MemBudgetMB?
}

func Run(ctx context.Context, shimPath string, cfg Config) ([]Report, error) // one Report per Backend
func WriteReport(dir string, reports []Report) (mdPath, jsonPath string, err error)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `pkg/funcd` (embedded platform), the data-plane HTTP, `shim/nodejs/shim.mjs`, `/proc` or `ps` for RSS | node-gated (the shim runs JS) |
| Adds | `cmd/funcd-bench` + `internal/bench` + a `just bench` recipe + a `bench/` output dir | **no new Go dependency** (stdlib load + RSS) |
| Exposes | a markdown + JSON sustainability report (throughput, tail latency, per-sandbox & idle RSS, cold-start, density, verdict) | the artifact a human reads |

## Implementation plan

1. **`internal/bench/load.go`** — the concurrent load driver + latency percentiles (sorted samples).
2. **`internal/bench/mem.go`** — RSS sampler: shim-PID discovery (by shim path) + per-pid RSS (`/proc` on Linux, `ps`
   elsewhere); platform-baseline + per-sandbox + idle sampling.
3. **`internal/bench/bench.go`** — `Config`/`Report`/`Run`: boot funcd per backend (`InMemory()` vs a file-backed config),
   deploy a warm function + a scale-to-zero function, run throughput → memory → idle → cold-start → density, return Reports.
4. **`internal/bench/report.go`** — `WriteReport` (markdown + JSON) with the verdict (`FitsTarget`).
5. **`cmd/funcd-bench/main.go`** — thin shell (flags → `bench.Run` → `WriteReport`), ADR-0014 style.
6. **`justfile`** — a `bench` recipe (runs both backends; node-gated note).
7. **Tests** (node-gated): `internal/bench/bench_test.go` — a short **smoke** run asserting the scenario *properties*, not
   just populated fields: RPS>0 + `Latency.P50>0` (`bench-throughput`); `PerSandboxMB>0` (`bench-memory`); `IdleMB ≤
   PerSandboxMB`-ish, near baseline (`bench-idle`); **`ColdStart>0` timed only after the scale-to-zero fn reached 0
   replicas / `Idle`** (`bench-coldstart` — mirror the journey test's `Eventually(ph=="Idle")` precondition); both
   `memory` + `file` Reports present (`bench-substrate`); `PerFunctionMB ≥ 0` + `MaxDensity > 0`; `WriteReport` writes both files.
8. **Definition of done**: `funcd-bench` runs `memory` + `file` node-gated and writes a report; the smoke test green; no new
   Go dependency; ADR-0002 conventions (thin `cmd`, no globals, ctx-first, slog); no identity/path leak.

## Review checklist

- [ ] `funcd-bench` boots both substrates, drives the data plane, and writes markdown + JSON (`bench-report`).
- [ ] Report carries throughput + p50/p99/p99.9 (`bench-throughput`), per-sandbox + baseline RSS (`bench-memory`), idle RSS
      (`bench-idle`), cold-start measured **after 0 replicas** (`bench-coldstart`), and a marginal-slope density extrapolation + verdict (`bench-density`).
- [ ] Memory vs file **substrate** both run and are compared (`bench-substrate`); store is memory in both.
- [ ] Baseline RSS = funcd's own single PID (no process-tree double-count); Go-native load + RSS — **no new Go dependency**;
      `cmd/funcd-bench` is a thin shell (ADR-0014); node-gated smoke green.
- [ ] No identity/path leak.

## Consequences

- (+) An honest, repeatable answer to "is it sustainable" — the numbers that matter on a RAM-bound box, on dev and the target.
- (+) The substrate for a later CI perf-regression gate and for comparing runtimes (the python shim re-benches with this).
- (−) Per-sandbox memory is **process RSS**, not container cgroup memory (which also counts page cache + kernel accounting),
  until the Linux lane lands — a lower-bound proxy, so the fits/doesn't-fit **verdict is optimistic** (the report says so).
- (−) The **store is memory in both substrates** (the file store = slatedb/cgo lane); the substrate axis exercises the
  durable blob + bus, not a persistent metastore.
- (−) Node-gated (the shim runs JS); without node the bench skips, like the other execution lanes.

## Open questions

- **Container/cgroup memory (Linux)** — sample `memory.current` under containerd/crun for true per-container footprint (and
  a truer verdict than process RSS).
- **File store comparison (slatedb)** — a `-tags slatedb` (cgo) lane comparing the memory vs persistent metastore, once that lane is stood up.
- **CI perf-regression gate** — thresholds on throughput/RSS/cold-start once baselines exist.
- **Reading platform metrics** — enrich the report with the platform's own Prometheus counters.

## References

- [ADR-0014](0014-platform-facade-lifecycle-harness.md) · [ADR-0025](0025-testing-strategy-and-e2e-harness.md) · [ADR-0033](0033-data-plane-serving-wake.md) · [ADR-0016](0016-activator-scale-to-zero.md).
