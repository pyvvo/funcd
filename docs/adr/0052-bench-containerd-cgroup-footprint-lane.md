# ADR-0052: Containerd cgroup-footprint bench lane (true production footprint) — refines ADR-0040

- **Status**: Implemented
- **Date**: 2026-06-17 (**Implemented 2026-06-17** — review `pass`: 0 Blockers/0 Majors, DoD 7/7, both GOOS build green, the
  skip path + report tests pass in `just ci`, process lane byte-for-byte unchanged, no new module; the two cgroup-*measuring*
  scenarios are the deferred homebox `FUNCD_IT=1` e2e per the ADR. **Accepted 2026-06-17** — judge: no Blockers. Folded the one Major (the `internal/bench`
  linux/`!linux` split's real job is fencing the `/proc`+`/sys` cgroup read + the `internal/runtime/containerd` import to
  the linux file — `containerd.New` is already cross-platform via its `!linux` stub) + three Minors (no depguard owed since
  containerd is already a shipped dep; the overhead ratio labelled "container cgroup ÷ process-lane shim RSS"; ADR-0046
  owns the per-pool axis / deferred per-pool **CPU** cgroup). Verified `runtime.Instance.PID` is populated by the driver's
  `List`, so the no-port-surface-change measurement is feasible.)
- **Deciders**: green-0-rabbit
- **Tags**: bench, containerd, footprint, observability, linux
- **Realizes**: [FEAT-0000/F27](../feat/0000-feat-v1.md) (benchmark & sustainability harness)
- **Refines**: [ADR-0040](0040-benchmark-sustainability-harness.md) — keeps its harness architecture (embed funcd, drive the
  public data plane, emit markdown+JSON). Adds a **second, opt-in measurement lane** that measures the footprint over the
  **production execution path** instead of bare processes. **Exercises** [ADR-0032](0032-curated-runtime-images.md) /
  [ADR-0011](0011-runtime-port-and-containerd-driver.md) — the containerd/crun driver + curated images — and reuses
  [ADR-0051](0051-bench-adopt-gopsutil-fortio.md)'s gopsutil/fortio.

## Context & Need

The existing bench (ADR-0040) boots funcd with the **process driver** (`funcd.WithRuntimeShim("node", …)`,
[`internal/bench/bench.go`](../../internal/bench/bench.go) L121): functions run as bare `node shim.mjs` child
processes and the footprint is each shim's **process RSS** (`shimRSSMB`). That is right for **feature validation** and
**relative** comparison (substrate, pooling density, throughput/tail-latency) — but it is **not** the production footprint.
In production a function runs as the entrypoint of a **crun container under containerd** (ADR-0032) with its own **cgroup +
netns**. The container's cgroup charges more than the shim's RSS: page cache for the image/artifact, kernel slab
(netns, mounts), and the runtime's own pages. So the process-RSS number is a **lower bound**, and the `report.md`
fits-target verdict is honestly labelled "optimistic" today.

To answer *absolute* footprint ("how many agents really fit on the box") we need to measure the thing production runs: the
**container cgroup `memory.current`** of a function executing through the real path (gateway → activator → containerd
worker in a netns → shim). This ADR adds that lane **alongside** the process lane — it does not replace it.

## Scenarios

- **scenario: containerd-cgroup-footprint** — *Given* a Linux host (root) with containerd, crun, CNI, and the curated Node
  image, *when* the bench runs the containerd lane and deploys a warm function, *then* the function executes in a **real
  crun container** and the lane reports that container's cgroup `memory.current` as the per-function footprint — a number
  **larger** than the process-RSS measurement (it also charges page cache + kernel).
- **scenario: lane-skips-without-containerd** — *Given* a host **without** the containerd path (macOS dev; or Linux missing
  the socket / root / curated image), *when* the containerd lane is requested, *then* it **skips with a logged reason and
  succeeds** (exit 0) — it never fails the run, and the process lane still emits its report.
- **scenario: footprint-report-separate** — *Given* a completed containerd lane, *when* reports are written, *then* a
  **distinct** `footprint-report.{md,json}` records the cgroup-measured per-function MB, an **honest** (non-optimistic)
  fits-target verdict computed from the cgroup number, and the cgroup-vs-RSS overhead — leaving the process `report.md`
  untouched.
- **scenario: process-lane-default-unchanged** — *Given* the default `just bench` (no `--containerd`), *when* it runs on any
  platform, *then* it behaves exactly as before — the containerd lane is purely **additive and opt-in**.

## Scope

**In:** an opt-in `--containerd` lane in `cmd/funcd-bench` that boots funcd with the **containerd driver + container
execution** (the exact wiring `cmd/funcd` uses for `FUNCD_RUNTIME=containerd`), deploys the footprint scenarios so they run
in real crun containers, and measures each container's **cgroup-v2 `memory.current`**; a separate `footprint-report.{md,json}`;
a Linux/non-Linux build split so `go build ./...` + `just ci` stay green on macOS (the lane is a clean skip off-Linux); the
homebox infrastructure runbook (containerd + crun + CNI + curated-image import).

**Out:** **replacing** the process lane (it stays, always runs); CI-gating the containerd lane (it is manual / homebox, like
the ADR-0011 `FUNCD_IT=1` integration tests); the **file** substrate under containerd (the store stays memory; the
substrate comparison stays in the process lane); a **Python** curated image (Node only — matches the one curated image that
exists today); microVM/gVisor/wasm isolation; the **pool** cgroup axis (this lane measures per-**function** containers; ADR-0046 owns
per-pool, and deferred its per-pool **CPU** cgroup); automating homebox provisioning beyond a documented runbook.

## Constraints & Decision drivers

- **Production-honest footprint** — the only faithful absolute number is the container's **cgroup `memory.current`**, not
  the shim RSS. That is the lane's reason to exist.
- **Additive, never regressive** — the process lane (ADR-0040/0051) is unchanged and remains the cross-platform default; the
  new lane is opt-in (`--containerd`) and isolated in its own files.
- **Cross-platform build** — `containerd.New` is already cross-platform (its `//go:build !linux` stub returns
  `fault.Unavailable`, [`containerd_other.go`](../../internal/runtime/containerd/containerd_other.go)), so the bench
  compiles off-Linux regardless. The `internal/bench` linux/`!linux` split exists for a narrower reason: only the **linux**
  file may do the `/proc`+`/sys` cgroup read (`cgroupMemMB`) and construct the `internal/runtime/containerd` runtime; the
  `!linux` file just returns `Skipped`; the **shared** types live in a cross-platform `footprint.go`. So `go build ./...`
  stays green on both and the lane **skips** (never errors) where the path is absent.
- **Reuse, don't reinvent** — boot via the existing `containerd.New` + `funcd.WithRuntime`/`WithContainerExecution`; read
  PIDs via the `runtime.Runtime.List` the bench already holds; reuse fortio/gopsutil (ADR-0051). No new dependency.
- **Honest deferral** — the real cgroup numbers need Linux+root+containerd+crun+CNI+curated image, none of which exist on
  the macOS dev box or (yet) on homebox. The measuring scenarios are **e2e, deferred to the homebox lane** behind
  `FUNCD_IT=1` (the roadmap test-sequencing rule + the ADR-0011/0040 precedent); the **skip path is unit-tested and runs in
  `just ci`**.

## Alternatives considered

- **Probe crun directly (no containerd).** Run `crun` on a hand-built OCI bundle and read its cgroup — lighter infra. *Rejected:
  it measures crun, not the **production** path; it would miss the containerd shim + CNI netns + snapshot overhead that the
  real footprint includes. The decider chose the full production E2E.*
- **Replace the process lane with the containerd lane.** One honest number. *Rejected: the process lane is the
  cross-platform, dependency-free path that validates features + relative density on any dev box; the containerd lane can
  only run on a provisioned Linux box. Keep both — each answers a different question.*
- **Add a `MemoryBytes` method to the `runtime.Runtime` port.** Measure via the port. *Rejected: cgroup reading is a
  bench/observability concern, not a platform contract; the bench already holds the constructed runtime and can read the
  cgroup of the PIDs `List` reports — no port surface change, and the platform stays container-lib-free.*
- **Read cgroup stats via the containerd client (`task.Metrics`).** Use containerd's typed metrics. *Not chosen — it couples
  the bench to containerd's metrics types/versioning; reading `memory.current` from the PID's cgroup-v2 path is a few lines
  of stdlib and is runtime-agnostic. (Recorded as a possible future swap.)*

## Decision

1. **A second lane, opt-in, in its own files.** `cmd/funcd-bench` gains a `--containerd` flag (default off). When set, it
   calls `bench.RunContainerd`; otherwise behaviour is byte-for-byte the existing process lane. `internal/bench/containerd_linux.go`
   (`//go:build linux`) holds the real lane (it alone imports `internal/runtime/containerd` and reads `/proc`+`/sys`);
   `internal/bench/containerd_other.go` (`//go:build !linux`) returns `Skipped` without touching the cgroup path. The shared
   `FootprintReport`/`ContainerdConfig` types + `WriteFootprintReport` live in cross-platform `footprint.go`. containerd is
   **already** in the shipped import graph (via `cmd/funcd`), so the bench's new use of it needs **no** depguard confinement
   (unlike ADR-0051's gopsutil/fortio, which were genuinely new modules).
2. **Boot the production path.** The Linux lane builds the runtime exactly as `cmd/funcd` does for `FUNCD_RUNTIME=containerd`
   — `containerd.New(containerd.Config{Socket, Snapshotter, CNIBinDir, CNIConfDir, SubnetCIDR})` + `funcd.WithRuntime(rt)` +
   `funcd.WithContainerExecution(imageFor)` (image prefix `funcd/runtime-`, runtime `nodejs22`) — then drives the **same**
   deploy/settle/load/density scenarios as `runBackend`, over the real data plane. The store stays memory (substrate is the
   process lane's axis).
3. **Measure the cgroup, not the RSS.** The lane keeps the constructed `rt runtime.Runtime`; after a function is Ready it
   calls `rt.List(ctx, "default")`, and for each instance reads its container's **cgroup-v2 `memory.current`** via the
   instance `PID`: parse `/proc/<pid>/cgroup` (the `0::<path>` line) → read `/sys/fs/cgroup<path>/memory.current` → MB,
   **deduped per cgroup path** so each container counts once. Per-function footprint = the marginal cgroup MB over the
   density sweep (same model as the process lane, cgroup-sourced). `MaxDensity`/`FitsTarget` are computed from this **real**
   number — the verdict is **honest**, not optimistic. The lane also records an overhead ratio = **container cgroup MB ÷
   process-lane shim RSS MB** — labelled as exactly that (the two are different runs on different substrates, so it is an
   indicative "how much does process RSS under-count," not the same container measured twice).
4. **Skip cleanly, never fail.** The lane runs only when **Linux + `containerd.New` succeeds (socket present) + euid 0 +
   the curated image is pullable**. Any miss → `FootprintReport{Skipped: true, SkipReason: …}` with `nil` error; the CLI
   logs the reason and exits 0. The non-Linux stub always returns `Skipped`.
5. **Separate report.** `footprint-report.{md,json}` (cgroup footprint, ADR-0052) is written distinct from `report.md`
   (process-RSS sustainability, ADR-0040). It states plainly it measures **container cgroup `memory.current`** over the
   production path, gives the honest fits-target verdict, and shows the cgroup-vs-RSS overhead. `report.md` is unchanged.

## Contracts

```go
// footprint.go (cross-platform) — shared types + report writer
type ContainerdConfig struct {
	Socket, Snapshotter, CNIBinDir, CNIConfDir, SubnetCIDR string
	ImagePrefix string                          // "funcd/runtime-"; image = prefix+runtime+":latest"
	ShimPath    string                          // process-lane shim, for the cgroup-vs-RSS overhead baseline
	Density     int                             // density sweep size
	MemBudgetMB int; TargetFns int              // verdict inputs (same as Config)
	Concurrency int; Duration time.Duration     // load phase
}

type FootprintReport struct {
	Skipped      bool    `json:"skipped"`
	SkipReason   string  `json:"skipReason,omitempty"`
	PerFunctionCgroupMB float64 `json:"perFunctionCgroupMB"` // marginal cgroup memory.current / fn (the true footprint)
	PerFunctionRSSMB    float64 `json:"perFunctionRSSMB"`    // process-RSS baseline, for the overhead ratio
	CgroupOverRSS       float64 `json:"cgroupOverRSS"`       // container cgroup ÷ process-lane shim RSS — indicative under-count
	PlatformBaselineMB  float64 `json:"platformBaselineMB"`
	MaxDensity   int     `json:"maxDensity"`                 // from the cgroup number (honest)
	FitsTarget   bool    `json:"fitsTarget"`                 // honest verdict, not optimistic
	RPS          float64 `json:"rps"`; Latency Latency `json:"latency"`
}

// RunContainerd boots funcd over the containerd/crun production path, runs the footprint
// scenarios in real containers, and returns a cgroup-measured report. On a host without the
// path it returns FootprintReport{Skipped:true,…} and a nil error — never fails the run.
//   containerd_linux.go  (//go:build linux):  the real lane
//   containerd_other.go  (//go:build !linux): returns {Skipped:true, SkipReason:"requires linux"}
func RunContainerd(ctx context.Context, cfg ContainerdConfig) (FootprintReport, error)

func WriteFootprintReport(dir string, r FootprintReport) (mdPath, jsonPath string, err error)

// cgroup_linux.go (//go:build linux) — referenced only by the linux lane
func cgroupMemMB(pid int) float64 // cgroup-v2 memory.current of pid's cgroup → MB; 0 on error
```

**Dependencies & I/O:** no new module. Consumes the containerd socket, CNI dirs, the curated image, and (read-only)
`/proc/<pid>/cgroup` + `/sys/fs/cgroup/**`. Produces `footprint-report.{md,json}`. Requires Linux + root at run time.

## Implementation plan

- `internal/bench/footprint.go` (cross-platform): `ContainerdConfig`, `FootprintReport`, `WriteFootprintReport`
  (`footprint-report.{md,json}`, mirroring `report.go`'s writer + a small markdown table that labels it cgroup-measured).
- `internal/bench/containerd_linux.go` (`//go:build linux`): `RunContainerd` — precondition checks (euid 0, `containerd.New`,
  image pullable) → on miss return `{Skipped:true,SkipReason}`; else build `funcd.WithRuntime`+`WithContainerExecution`, run
  the deploy/settle/load/density scenarios (factor the shared scenario walk out of `bench.go` where clean, else mirror it),
  measure via `rt.List` + `cgroupMemMB`, compute the report.
- `internal/bench/cgroup_linux.go` (`//go:build linux`): `cgroupMemMB` — parse `/proc/<pid>/cgroup` → read
  `/sys/fs/cgroup<path>/memory.current`.
- `internal/bench/containerd_other.go` (`//go:build !linux`): `RunContainerd` → `{Skipped:true,SkipReason:"requires linux"}`.
- `cmd/funcd-bench/main.go`: `--containerd` flag; when set, call `bench.RunContainerd`, `WriteFootprintReport`, and `logf` the
  skip reason when skipped. Default path unchanged.
- **Test plan** — *(non-gated, runs in `just ci`)* `TestRunContainerdSkipsWithoutInfra`: on macOS the `!linux` stub, and on a
  Linux CI box without containerd/root the `linux` precondition check, both return `Skipped==true` with a non-empty reason
  and a `nil` error (covers `lane-skips-without-containerd`; `process-lane-default-unchanged` holds by construction — the
  flag defaults off and the existing bench files are untouched). *(linux-gated)* a `cgroupMemMB(os.Getpid())>0` unit test
  (`//go:build linux`). *(e2e, deferred to homebox, `FUNCD_IT=1`)* `containerd-cgroup-footprint` + `footprint-report-separate`
  — run `funcd-bench --containerd` as root on a provisioned box; recorded as a deferred scenario (ADR-0011/0040 precedent),
  not run in `just ci`.
- **Homebox runbook** (documented in the report header + a `docs/` note): install `containerd`, `crun`, CNI plugins; write a
  CNI conflist; `just build-runtime-images` then import the image into containerd's store for namespace `funcd-default`
  (`ctr -n funcd-default images import …`); run `sudo funcd-bench --containerd`.
- **DoD:** `go build ./...` on linux **and** darwin · `golangci-lint` clean · `go test ./...` green (skip test passes) ·
  `go mod verify` · process lane byte-for-byte unchanged · the containerd lane skips cleanly off-Linux · homebox runbook
  documented · no identity/path leak. The real cgroup numbers are verified on homebox and recorded as a deferred e2e.

## Review checklist

- [ ] `--containerd` is opt-in; without it the bench is byte-for-byte the ADR-0040/0051 process lane (`report.md` unchanged).
- [ ] The lane boots funcd via `containerd.New` + `WithRuntime` + `WithContainerExecution` — the production path, not the
      process shim.
- [ ] Footprint = container **cgroup-v2 `memory.current`** (deduped per cgroup), read from `rt.List` PIDs; `MaxDensity`/
      `FitsTarget` computed from the cgroup number; cgroup-vs-RSS overhead recorded.
- [ ] `RunContainerd` returns `{Skipped:true, SkipReason≠""}` + `nil` err on any missing precondition (non-Linux, no socket,
      non-root, image absent); never fails the run.
- [ ] Build split: `containerd_linux.go` / `containerd_other.go` / `cgroup_linux.go`; `go build ./...` green on linux + darwin.
- [ ] `footprint-report.{md,json}` is separate, labelled cgroup-measured + honest verdict; `report.md` untouched.
- [ ] Non-gated skip test passes in `just ci`; cgroup + e2e scenarios gated/deferred per the test plan; no new dependency;
      no identity/path leak.

## Consequences

- funcd gains an **honest absolute footprint** number measured over the real crun/containerd path — closing the
  "optimistic, process-RSS lower bound" caveat the `report.md` carries, without losing the cross-platform process lane.
- The real numbers require provisioning homebox (containerd + crun + CNI + curated image) — infrastructure that does not yet
  exist there; until then the lane skips and the measuring scenarios are deferred e2e. This is the same Linux-lane deferral
  ADR-0011/0040 already carry.
- ADR-0040's architecture is intact; this adds a lane + a report and exercises ADR-0032/0011 end to end for the first time
  in the bench.

## Open questions

- **cgroup driver path layout** (cgroupfs vs systemd slice) is read generically from `/proc/<pid>/cgroup`, so it is
  driver-agnostic; confirmed against the real homebox containerd config during the deferred e2e.

## References

- ADR-0040 (the harness this refines), ADR-0051 (gopsutil/fortio it reuses), ADR-0032 (curated images), ADR-0011 (containerd
  driver + the `FUNCD_IT=1` integration-lane precedent), ADR-0046 (owns the per-pool axis; deferred per-pool CPU cgroup).
- cgroup-v2 `memory.current` (kernel `Documentation/admin-guide/cgroup-v2.rst`) — the live total charged to a cgroup.
