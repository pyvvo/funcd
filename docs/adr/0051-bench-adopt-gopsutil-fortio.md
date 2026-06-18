# ADR-0051: Use gopsutil + fortio in the bench harness (test-only) — refines ADR-0040

> **Superseded in part by [ADR-0055](0055-funcd-bench-subcommand.md)** — the **`cmd/funcd` confinement axis only**. This ADR's
> `deps-confined-to-bench` scenario + DoD asserted `go list -deps ./cmd/funcd/...` **excludes** gopsutil/fortio; folding the
> bench into the funcd binary (the one-binary decision) means they now **do** ride in the shipped `funcd` binary via
> `internal/bench`. The **`funcdcli` confinement** and the **depguard deny rule** are **kept** (funcdcli never imports
> `internal/bench`; only the now-dead `cmd/funcd-bench` exception is dropped).

- **Status**: Implemented
- **Date**: 2026-06-17 (**Implemented 2026-06-17**; **Accepted 2026-06-17** — judge: no Blockers on the prior (vegeta) draft. Folded its 3 Majors:
  (M2) softened "numbers stay the same" → the new lib's throughput accounting differs from the old 200-only/wall-clock
  count, so numbers need be *sane, not equal*; (M3) confinement is an **enforced depguard deny rule** (Decision 3), not a
  manual checkbox; (M1) `Report` field names declared out of scope; plus a non-gated `httptest` `runLoad` test and `pool.go`
  named as an unchanged caller. **Load lib changed vegeta → fortio** on the decider's call: the bench is a *closed-model*
  max-throughput measurement (N workers firing as fast as possible), which is fortio's **native** mode (`NumThreads` +
  `QPS:-1`), whereas vegeta is open/rate-model bent into closed mode. Both permissive; fortio Apache-2.0.)
- **Deciders**: green-0-rabbit
- **Tags**: bench, tooling, dependencies, testing
- **Realizes**: [FEAT-0000/F27](../feat/0000-feat-v1.md) (benchmark & sustainability harness)
- **Refines**: [ADR-0040](0040-benchmark-sustainability-harness.md) — keeps its architecture (embed funcd, drive the public
  data plane, sample external RSS, `cmd/funcd-bench` + `internal/bench`), and **relaxes one DoD line**: ADR-0040 asserted
  "**no new Go dependency**" and rejected an external load *tool binary* (k6/vegeta/wrk), noting "**revisit if we need
  complex scenarios**." This takes that revisit: adopt two **libraries** (not tool binaries), confined to the test harness.

## Context & Need

The bench's hand-rolled measurement code has two weak spots:

1. **RSS sampling shells out to `ps -o rss=` and `pgrep -f`** (`internal/bench/mem.go`) — the real reinvented wheel, *and*
   the opposite of dependency-free: it needs `ps`/`pgrep` on `PATH` and parses their text output. Fragile, platform-variant.
2. **Load generation + latency percentiles are hand-rolled** (`internal/bench/load.go`: a goroutine pool + a sorted-slice
   quantile) — fine for a smoke, not the right tool for honest tail-latency.

ADR-0040 avoided an external load **binary** (a separate process) for the single-binary, in-repo, reproducible ethos. That
reasoning does **not** apply to a Go **library**: it compiles into the same `funcd-bench` binary, is pinned in `go.mod`,
stays in-repo. And the decider confirmed these deps are **test-only** — `funcd-bench` never ships in the platform, so the
product's minimal-dependency ethos is not at stake. Adopting `gopsutil` even *removes* the `ps`/`pgrep` external-binary
dependency.

## Scenarios

- **scenario: rss-without-shelling** — *Given* a running shim process, *when* the bench samples its memory, *then* it reads
  RSS via `gopsutil` (cross-platform, in-process) — no `ps`/`pgrep` subprocess, same MB value.
- **scenario: load-via-fortio** — *Given* a target URL, *when* the bench drives load, *then* it uses fortio's HTTP runner
  in closed-model (`NumThreads = workers`, `QPS = -1` max-speed, keep-alive) and reports successful throughput +
  P50/P90/P99/P99.9/Max from fortio's histogram — the same `loadResult`/`Latency` shape callers + reports consume.
- **scenario: deps-confined-to-bench** — *Given* the new imports, *when* the import graph is checked, *then* `gopsutil` and
  `fortio` are imported **only** by `internal/bench` (+ the `cmd/funcd-bench` shell) — never by `api/**`, `pkg/**`, or any
  platform `internal/**` package — and a **depguard** rule fails CI on any cross-import.

## Scope

**In:** replacing `internal/bench/mem.go`'s RSS sampling with **`github.com/shirou/gopsutil/v4/process`**; replacing
`internal/bench/load.go`'s `runLoad` + `percentiles` with **`fortio.org/fortio/fhttp` + `periodic`**; the `go.mod`/`go.sum`
changes; a depguard rule confining both to the bench; keeping the `loadResult`/`Latency`/`Report` Go **types** unchanged
(report *rendering* is reshaped separately).

**Out:** fortio/vegeta as an external **binary** (we use fortio as a library); changing the harness *architecture*
(ADR-0040 stands); using these libs **anywhere outside the bench**; the pooled comparisons' structure (ADR-0044/0050) —
they keep calling the same `runLoad`/`rssMB`, now lib-backed.

## Constraints & Decision drivers

- **Closed-model fit** — the bench measures max throughput with N concurrent workers; fortio's native runner *is* that
  (`NumThreads` + `QPS:-1`). vegeta is open/rate-model (a worse fit for this question), so it was reconsidered out.
- **Library, not binary** — keeps ADR-0040's single-binary, in-repo, reproducible properties; only adds a pinned module.
- **Test-only blast radius** — the deps live in the bench, which never ships; enforced by the import graph (depguard).
- **Apache-2.0/MIT/BSD only** — **`gopsutil` is BSD-3-Clause**, **`fortio` is Apache-2.0** (both verified). Permissive.
- **No type/shape change** — `loadResult`/`Latency`/`Report` Go types are unchanged (an internal measurement-impl swap).
  Absolute throughput may shift *slightly* (fortio's successful-QPS accounting ≠ the old 200-only-count ÷ wall-clock), so
  numbers need be **sane, not equal**.

## Alternatives considered

- **Keep stdlib (status quo).** Zero deps, but the `ps`/`pgrep` shelling stays fragile and the percentile is naive.
  *Rejected by the decider for this revisit; ADR-0040 itself invited it.*
- **vegeta (MIT) for load.** The canonical Go load library — but fundamentally **open-model** (you set a target rate and it
  fires at that rate); the bench's closed-model max-throughput is a secondary `Rate{Freq:0}` mode. *Rejected: a worse fit
  for the bench's actual question; fortio's native mode is the closed loop the bench runs.*
- **gopsutil only (RSS), keep the stdlib load driver.** Smaller change. *Not chosen — the decider wanted a maintained load
  + histogram lib too.*
- **hdrhistogram-only for percentiles + keep the goroutine driver.** Lighter. *Not chosen — fortio bundles the closed-model
  runner + the histogram in one Apache-2.0 library.*

## Decision

1. **RSS via gopsutil.** `internal/bench/mem.go` uses `github.com/shirou/gopsutil/v4/process`: `rssMB(pid)` →
   `process.NewProcess(int32(pid)).MemoryInfo().RSS / MiB` (note: gopsutil RSS is **bytes**, not the old `ps` kilobytes);
   `shimRSSMB(shimPath)` → `process.Processes()`, summing the RSS of those whose `Cmdline()` contains the shim path,
   excluding `os.Getpid()`. 0 on any error (unchanged contract). No `ps`/`pgrep`.
2. **Load + percentiles via fortio.** `internal/bench/load.go`'s `runLoad(ctx, url, body, workers, dur)` runs
   `fhttp.RunHTTPTest` with `periodic.RunnerOptions{NumThreads: workers, QPS: -1, Duration: dur}` (closed-model max speed)
   + keep-alive, a POST `HTTPOptions` (JSON `Payload`), and `Percentiles: [50,90,99,99.9]`. It returns
   `loadResult{ rps: successful-QPS (from RetCodes[200] over the run), latency: {P50,P90,P99,P99.9,Max} from the
   DurationHistogram }`. fortio's logger is quieted to errors so it doesn't spam the bench output. The hand-rolled
   `percentiles` helper is deleted. `ctx` cancellation is honored via fortio's `Stop` channel / a bounded `Duration`.
3. **Confinement, enforced by depguard.** A `.golangci.yml` depguard rule **denies** importing `github.com/shirou/gopsutil`
   and `fortio.org/fortio` from everywhere except `internal/bench/**` and `cmd/funcd-bench/**` — an accidental future
   cross-import into a shipped package fails CI (reusing the repo's existing file-scoped depguard infrastructure). The
   shipped `funcd`/`funcdcli` binaries never pull them — also checked by `go list -deps`.

## Contracts

```go
// mem.go
func rssMB(pid int) float64             // gopsutil MemoryInfo().RSS (bytes) → MB; 0 on error
func shimRSSMB(shimPath string) float64 // sum RSS of processes whose Cmdline contains shimPath (excl. self)

// load.go — signature + returned shape UNCHANGED; implementation now fortio-backed
func runLoad(ctx context.Context, url, body string, workers int, dur time.Duration) loadResult
// loadResult{ rps float64; latency Latency{P50,P90,P99,P999,Max time.Duration} } — unchanged
```

**Dependencies & I/O:** adds `github.com/shirou/gopsutil/v4` (BSD-3) + `fortio.org/fortio` (Apache-2.0) to `go.mod`
(test-only, bench-confined). Consumes a PID / a target URL. Exposes the same `rssMB`/`shimRSSMB`/`runLoad` API.

## Implementation plan

- `go get github.com/shirou/gopsutil/v4@latest fortio.org/fortio@latest`; `go mod tidy` (drops the removed vegeta).
- Rewrite `internal/bench/mem.go` (gopsutil) — drop `os/exec` + text parsing.
- Rewrite `internal/bench/load.go` `runLoad` (fortio) + delete `percentiles`; keep `loadResult`/`Latency`. Quiet fortio's
  logger.
- `internal/bench/pypool.go` `runLoadSpread` **and** `internal/bench/pool.go` (ADR-0044) are unchanged — both call the
  same `runLoad`/`rssMB`.
- `.golangci.yml` — depguard deny rule confining gopsutil/fortio to `internal/bench/**` + `cmd/funcd-bench/**`.
- `Report` field names (e.g. `PerWorkerMB`) are **out of scope** — only `rssMB`/`shimRSSMB`/`runLoad`/`loadResult`/`Latency`
  internals change.
- **Test plan:** existing `internal/bench` node-gated + `pypool_test.go` py-gated smokes cover the swap end to end; add a
  focused `rssMB(os.Getpid()) > 0` unit test **and** an `httptest.Server`-backed `runLoad` unit test (throughput > 0,
  p99 > 0) so `load-via-fortio` has a **non-gated** assertion. **Verify** the import graph:
  `go list -deps ./pkg/... ./api/... ./cmd/funcd/... ./cmd/funcdcli/...` excludes gopsutil/fortio, and the depguard rule
  rejects a planted cross-import.
- **DoD:** `go build ./...` · `golangci-lint` clean · `go test ./...` green · `go mod verify` · `just bench` runs with
  **sane** numbers · gopsutil/fortio absent from the platform import graph · no identity/path leak.

## Review checklist

- [ ] `mem.go` uses gopsutil; no `ps`/`pgrep`/`os/exec` left in the bench RSS path; RSS unit is bytes→MB.
- [ ] `runLoad` uses fortio closed-model (`NumThreads`, `QPS:-1`, keep-alive); returns the unchanged `loadResult`/`Latency`;
      `percentiles` deleted; fortio logging quieted.
- [ ] gopsutil (BSD-3) + fortio (Apache-2.0) pinned in `go.mod`; `go mod verify` clean; vegeta removed.
- [ ] **gopsutil/fortio imported only by `internal/bench` (+ `cmd/funcd-bench`)** — absent from `go list -deps` of the
      platform/CLI; a depguard deny rule enforces it (a planted cross-import fails lint).
- [ ] `just bench` runs; throughput + RSS numbers **sane** (not necessarily equal — fortio throughput accounting differs).
- [ ] Go suite green; no identity/path leak.

## Consequences

- The bench loses fragile `ps`/`pgrep` text-parsing and a naive percentile; gains cross-platform RSS + a maintained
  closed-model load/histogram library — more correct, more portable.
- `go.mod`/`go.sum` grow (fortio pulls a transitive set). Accepted: **test-only**, never in the shipped platform binaries
  (enforced by depguard), so it doesn't touch the single-binary product or its supply chain.
- ADR-0040's architecture is intact; only its "no new Go dependency" DoD line is superseded for the harness, along the
  "revisit if…" door it left open.

## Open questions

None. (The confinement the test-only argument rests on is enforced by a depguard deny rule, Decision 3.)

## References

- ADR-0040 (the harness this refines), ADR-0044/0050 (pooled comparisons that call the same `runLoad`/`rssMB`),
  ADR-0002 (§7 depguard + the Apache/MIT license gate).
- `github.com/shirou/gopsutil/v4` — **BSD-3-Clause** (verified). `fortio.org/fortio` — **Apache-2.0** (verified);
  `periodic.RunnerOptions{NumThreads, QPS:-1 (max speed), Duration, Percentiles}` + `fhttp.RunHTTPTest` →
  `ActualQPS`/`RetCodes`/`DurationHistogram.CalcPercentile`.
