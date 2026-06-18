# Review — ADR-0052 implementation (containerd cgroup-footprint bench lane)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0052 implementation, model: claude-opus-4-8)

The opt-in `--containerd` lane is correctly built: a clean Linux/`!linux` build split that fences
the containerd driver + `/proc`+`/sys` cgroup read to the linux file, a skip-not-fail contract that
is unit-tested and runs in `just ci`, and a process lane left byte-for-byte unchanged. The two
*measuring* scenarios are honestly deferred to the homebox `FUNCD_IT=1` e2e (the ADR-0011/0040
precedent), and every non-deferred item has a passing test. No new module, no identity/path leak.

All verification was run on this macOS box; the linux-only files are covered by
`GOOS=linux go build`/`go vet` (golangci-lint cannot cross-execute the linux files here — noted as an
`env` limitation, not a model defect).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor (non-blocking)
- **Minor 1 — `env`: golangci-lint does not lint the linux-only files on this box.** `go tool
  golangci-lint run ./internal/bench/... ./cmd/funcd-bench/...` reported `0 issues.` (exit 0) on
  darwin, but the active GOOS excludes `containerd_linux.go` / `cgroup_linux.go` / their gated test
  from the lint pass. The only cross-platform static check that covers them here is
  `GOOS=linux go vet ./internal/bench/...` (exit 0). Full golangci-lint of the linux files will land
  when the homebox/Linux CI lane runs them. *Attribution: `env` (cross-compile linting unavailable on
  this macOS box), not a model defect.*
- **Minor 2 — feat F27 row already reads `implemented`.** `docs/feat/0000-feat-v1.md:86` shows the
  shared F27 row at `implemented` (it was stamped by ADR-0040/0051); ADR-0052 is one of three ADRs on
  that row. The ADR's own status is correctly `Reviewing`. This is a pre-existing multi-ADR-row
  artifact, not something the builder mis-set; the parent's pass-stamp need not move an already-
  `implemented` row. *Attribution: `adr`/process (shared feat row), not a model defect.*

### ✅ Verified correct (keep it)

**Build split is correct and load-bearing** (the ADR's central constraint):
- `containerd_other.go` (`//go:build !linux`) imports **only `context`** — not the containerd driver,
  not the `/proc`+`/sys` reader. Verified by reading its import block.
- `containerd_linux.go` (`//go:build linux`) is the **sole** importer of
  `internal/runtime/containerd` and `internal/runtime`; `cgroup_linux.go` + `containerd_linux.go` are
  the **only** files touching `/proc` / `/sys/fs/cgroup` (grep `internal/bench/*.go`).
- `go build ./...` green on **both** GOOS: darwin exit 0, `GOOS=linux GOARCH=amd64` exit 0.
- `GOOS=linux go vet ./internal/bench/...` exit 0 (covers the linux-only files); darwin
  `go vet ./internal/bench/... ./cmd/funcd-bench/...` exit 0.

**Skip-not-fail contract** (scenario `lane-skips-without-containerd`):
- `RunContainerd` returns `{Skipped:true, SkipReason≠""}` + `nil` error on every missing precondition:
  euid≠0 (`containerd_linux.go:35`), `containerd.New` failure (`:43`), Ready-timeout (`:94`), and
  unreadable cgroup (`PerFunctionCgroupMB<=0`, `:123`). The `!linux` stub always returns Skipped
  (`containerd_other.go:12`).
- `TestRunContainerdSkipsWithoutInfra` asserts `err==nil` and a non-empty reason; it ran on darwin
  (the `!linux` stub): `--- PASS` (exit 0).

**Cgroup measurement is sound cgroup-v2** (scenario `containerd-cgroup-footprint`, deferred-measure):
- `cgroupPath` parses the unified-hierarchy `0::<path>` line of `/proc/<pid>/cgroup`
  (`cgroup_linux.go:21`, `strings.CutPrefix(line,"0::")`); `cgroupMemMB` reads
  `/sys/fs/cgroup<path>/memory.current` → MB, 0 on any error (`:32-46`).
- `sumContainerCgroupMB` dedupes per cgroup path via `rt.List(ctx,"default")` PIDs so each container
  counts once (`containerd_linux.go:136-158`). `runtime.Instance.PID` is a real field
  (`internal/runtime/runtime.go:73`) populated by the driver's `List` (ADR confirmed at acceptance).
- `TestCgroupMemMBSelf` (linux-gated) asserts `cgroupMemMB(self)>0` on a v2 host and `cgroupMemMB(-1)==0`.

**MaxDensity / FitsTarget computed from the cgroup number; same-population RSS overhead:**
- `PerFunctionCgroupMB` = marginal `(totalCgroup-baseCgroup)/Density`; `MaxDensity`/`FitsTarget`
  derived from it (`containerd_linux.go:117,129-130`) — the honest verdict, not the RSS lower bound.
- `CgroupOverRSS = PerFunctionCgroupMB / PerFunctionRSSMB` where the RSS baseline is
  `shimRSSMB(cfg.ShimPath)` over the **same** container processes. `reportContainerd` sets
  `ShimPath:"/opt/funcd/shim.mjs"` (`cmd/funcd-bench/main.go:202`), which is exactly the curated
  image's entrypoint (`images/runtime/nodejs22/Dockerfile:13,21`
  `COPY shim/nodejs/shim.mjs /opt/funcd/shim.mjs` … `ENTRYPOINT ["node","/opt/funcd/shim.mjs"]`), so
  `shimRSSMB` matches the in-container process cmdline → a genuine same-population ratio.

**Boots the production path** (mirrors `cmd/funcd` `FUNCD_RUNTIME=containerd`):
- The lane wires `containerd.New(Config{Socket,Snapshotter,CNIBinDir,CNIConfDir,SubnetCIDR})` +
  `funcd.WithRuntime(rt)` + `funcd.WithContainerExecution(imageFor)` with prefix `funcd/runtime-`
  (`containerd_linux.go:39-65`) — byte-for-byte the wiring in `cmd/funcd/main.go:158-170`.
- `WithContainerExecution` signature is `func(runtime string) string` (`pkg/funcd/options.go:126`),
  matched by the lane's `imageFor`. Facade `Addr`/`DataPlaneAddr`/`Run` exist (`pkg/funcd/funcd.go`).

**Process lane unchanged / additive-only** (scenario `process-lane-default-unchanged`):
- `git diff` shows **no change** to `internal/bench/bench.go` or `report.go`. The only modified
  tracked file is `cmd/funcd-bench/main.go`, and its diff is purely additive — a `--containerd` flag
  (default `false`) + `reportContainerd` that early-returns when the flag is off; the existing
  process/pool/python report calls are untouched.

**Separate report** (scenario `footprint-report-separate`, skip branch tested):
- `WriteFootprintReport` writes `footprint-report.{md,json}` (distinct from `report.md`), labelled
  "cgroup-2 memory.current" / honest verdict (`footprint.go:79-106`). `TestWriteFootprintReportSkipped`
  asserts a skipped report still writes a non-empty md/json: `--- PASS`.

**Hygiene & deps:**
- `go test -count=1 ./internal/bench/...` exit **0**; targeted `-run 'Containerd|Footprint|Cgroup'`
  → 2 PASS, 0 fail.
- `go tool golangci-lint run ./internal/bench/... ./cmd/funcd-bench/...` → **`0 issues.`** (exit 0).
- `gofmt -l` on all 7 touched files → empty (clean).
- `go mod verify` → `all modules verified` (exit 0). **No new module:** `go.mod`/`go.sum` have **no
  diff vs HEAD**; the containerd modules (`containerd/v2 v2.3.1`, `go-cni`, …) were already present
  via `cmd/funcd`, and `gopsutil`/`fortio` remain **absent** from the shipped `cmd/funcd`/`pkg/...`
  dep graph (`go list -deps` confirmed). No depguard confinement owed — correct per the ADR.
- Identity grep (`<username>|<email>|/Users/|/home/<user>`) over the 7 touched files → **no hits**.
- No `panic(`, `not implemented`, or `fmt.Print*` in the new bench files. Errors go through `api/fault`.
- ADR-0052 status is `Reviewing` (the `adr-impl` bump); the ADR substance is intact.

### Definition of Done
**7 / 7 ADR Review-checklist items hold**, plus the applicable generic DoD:
1. ✅ `--containerd` opt-in; bench byte-for-byte the process lane without it (`report.md` untouched).
2. ✅ Lane boots via `containerd.New` + `WithRuntime` + `WithContainerExecution` (production path).
3. ✅ Footprint = cgroup-v2 `memory.current` deduped per cgroup from `rt.List` PIDs; MaxDensity/
   FitsTarget from the cgroup number; cgroup-vs-RSS overhead recorded (same-population).
4. ✅ `{Skipped:true,SkipReason≠""}` + nil err on every missing precondition; never fails the run.
5. ✅ Build split present; `go build ./...` green on linux + darwin.
6. ✅ `footprint-report.{md,json}` separate, cgroup-labelled + honest verdict; `report.md` untouched.
7. ✅ Non-gated skip test passes in `just ci`; cgroup test linux-gated; e2e measure scenarios deferred
   FUNCD_IT=1; no new dependency; no identity/path leak.

Generic DoD: build/vet/test/lint/mod-verify all green; real behaviour, no stubs; Contracts honoured
exactly (`ContainerdConfig`, `FootprintReport`, `RunContainerd`, `WriteFootprintReport`, `cgroupMemMB`
match the ADR Contracts block); tree matches the Implementation plan's repository surface (6 new files
+ the `main.go` flag, nothing missing/extra). The two measuring scenarios are deferred e2e — an
honest deferral per the ADR's "Honest deferral" driver and the ADR-0011/0040 precedent; the in-`just
ci` items (skip path, cgroup-reader unit test, report writer) are all green.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0052 (implementation) → pass, 0/0/2, 0 model-attributed,
DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation
**Pass — sign off.** The implementation is faithful to the ADR's Contracts, the build split does its
load-bearing job, the skip-not-fail path is real and tested, and the process lane is provably
unchanged. The two Minors are `env` (cross-compile linting) and `adr`/process (shared feat row) — both
zero model-attributed; neither blocks. The deferral of the two measuring scenarios to the homebox
`FUNCD_IT=1` e2e is honest and matches the established precedent, with all non-deferred items green.
Parent may stamp ADR-0052 `Reviewing → Implemented` (the F27 feat row is already `implemented` from
ADR-0040/0051).
