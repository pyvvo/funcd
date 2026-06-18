# Review — ADR-0051 implementation (gopsutil + fortio in the bench harness)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0051 implementation, model: claude-opus-4-8)

Swaps the bench's two hand-rolled measurement impls — RSS via `ps`/`pgrep`-shelling → `github.com/shirou/gopsutil/v4/process`,
and the goroutine-pool load gen + sorted-slice percentiles → `fortio.org/fortio/fhttp`+`periodic` (closed-model). The
test-only justification's load-bearing claim — confinement to `internal/bench` + `cmd/funcd-bench` — is **enforced by an
actually-firing depguard rule**, not merely asserted. Types unchanged; reports reshaped to small tables. No blocking or
major findings.

### Verification run (captured exit codes)

| Check | Command | Exit |
|---|---|---|
| build | `go build ./...` | 0 |
| full suite | `go test -p 1 ./...` | 0 (no FAIL/panic; `internal/bench` ok) |
| non-gated unit tests | `go test ./internal/bench/ -run 'TestRSSMBSelf\|TestRunLoadHTTPTest' -v` | 0 (both PASS) |
| lint | `go tool golangci-lint run ./...` | 0 (`0 issues.`) |
| mod verify | `go mod verify` | 0 (`all modules verified`) |

### Confinement is ENFORCED (the crux)

- **(a) shipped import graph clean** — `go list -deps ./pkg/funcd/... ./api/... ./cmd/funcd ./cmd/funcdcli | grep -iE 'gopsutil|fortio'`
  returns **zero matches** (grep exit 1). gopsutil/fortio never reach the shipped platform/CLI binaries.
- **(b) depguard rule fires** — temporarily planting `_ "github.com/shirou/gopsutil/v4/process"` into `cmd/funcd/main.go` and
  running `go tool golangci-lint run ./cmd/funcd/...` is **rejected**:
  `cmd/funcd/main.go:19:2: import 'github.com/shirou/gopsutil/v4/process' is not allowed from list 'bench-libs': gopsutil is bench-only (ADR-0051); must not reach the shipped platform (depguard)` (1 issue). The plant was reverted via
  `git checkout cmd/funcd/main.go`; `git diff cmd/funcd/main.go` is empty (clean revert). The `bench-libs` rule
  (`.golangci.yml`) denies `github.com/shirou/gopsutil`, `fortio.org/fortio`, **and** `fortio.org/log` from `$all` minus
  `internal/bench/**` + `cmd/funcd-bench/**`.

### ✅ Verified correct (keep it)

- **gopsutil RSS correctness** (`internal/bench/mem.go`) — `rssMB` uses `MemoryInfo().RSS` (bytes) `/ (1024*1024)` → MB (the
  ADR-flagged bytes-not-`ps`-kilobytes fix); returns 0 on error and on invalid pid (`TestRSSMBSelf` asserts both
  `rssMB(self) > 0` and `rssMB(-1) == 0`). No `os/exec`/`ps`/`pgrep` remain (`grep` over mem.go: none). `shimRSSMB` sums by
  `Cmdline()` substring, excluding `os.Getpid()`.
- **fortio load correctness** (`internal/bench/load.go`) — closed-model (`NumThreads=workers`, `QPS:-1`), keep-alive default,
  `Out=io.Discard` (verified: running `TestRunLoadHTTPTest -v` emits no `Sockets used`/`connection timing`/`Code 200`/
  `Aggregated` lines), ctx cancellation wired through a goroutine calling `opts.Stop.Abort(false)` on `ctx.Done()`, `rps` from
  `RetCodes[200] / ActualDuration.Seconds()`, percentiles from `DurationHistogram.CalcPercentile`. The hand-rolled
  `percentiles` helper is deleted (`grep 'func percentiles' internal/bench/`: none).
- **Types unchanged** — `git diff` on load.go shows `Latency{P50,P90,P99,P999,Max}` and `loadResult{rps,latency}` field lists
  identical; only internals changed. `Report` lives in `bench.go`, which is **not** in the diff (untouched).
- **Callers unchanged** — `internal/bench/pypool.go` (`runLoadSpread`/`pool path`) and the pool report still call the same
  `runLoad`/`rssMB`/`shimRSSMB` API (grep-confirmed).
- **Reports reshaped per directive** — `docs/reports/pool-report.md` (node) and `docs/reports/py-pool-report.md` (python) are
  each a small `metric | single | pooled` table (memory / density / throughput / p99) + one summary line — not the old verbose
  prose. The python report shows `Python 3.14.5` (a version, never an absolute path).
- **Licenses** — gopsutil module LICENSE = BSD-3-Clause; fortio module LICENSE = Apache-2.0 — both permissive per ADR-0002's
  gate. vegeta is fully removed from `go.mod`/`go.sum` (grep: none).
- **Hygiene** — identity/path grep (local username / `/Users/<user>/` / email tokens) over every changed + new file (including the
  reports): zero hits. ADR-0051 at `Reviewing`; F27 row links ADR-0051 and reads `implemented` (ADR-0051 refines an already-
  implemented feature, so the row stays implemented). ADR substance unchanged (only the impl gate's status bump).

### Definition of Done

6 / 6 ADR Review-checklist items hold: mem.go gopsutil + no `ps`/`pgrep` + bytes→MB ✓; runLoad fortio closed-model + unchanged
shape + `percentiles` deleted + logging quieted ✓; gopsutil(BSD-3)/fortio(Apache-2.0) pinned + `go mod verify` clean + vegeta
removed ✓; confinement (go list clean + depguard fires) ✓; suite green + sane numbers (reports regenerated) ✓; no identity/path
leak ✓. Generic phase DoD (build/lint/test/mod-verify) all exit 0. No misses.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0051 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 6/6. See
docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Stamp ADR-0051 `Reviewing → Implemented` (date 2026-06-17); F27 already links it and stays `implemented`. Nothing
loops back to the builder.
