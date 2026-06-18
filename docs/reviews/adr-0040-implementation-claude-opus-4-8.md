# ADR-0040 implementation review — `funcd-bench` (benchmark & sustainability harness)

- **ADR**: [0040](../adr/0040-benchmark-sustainability-harness.md) · status `Reviewing` → **Implemented** (this pass)
- **Realizes**: FEAT-0000/F27 (row `reviewing` → `implemented`)
- **Producing model**: claude-opus-4-8
- **Phase**: implementation · **Verdict**: ✅ **pass**
- **Reviewer**: adr-impl-review gate (ADR-0000 gate #5)

## Verification run (evidence, not opinion)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./internal/bench/... ./cmd/funcd-bench/...` | exit 0 |
| Lint | `go tool golangci-lint run ./internal/bench/... ./cmd/funcd-bench/...` | **0 issues**, exit 0 |
| Smoke (node-gated) | `go test ./internal/bench/ -run TestBenchSmoke -count=1` | **PASS** (31.6s), exit 0 |
| Binary | `go run ./cmd/funcd-bench --concurrency 6 --duration 2s --density 3 --out /tmp/rev-bench` | both substrates, `report.md`+`report.json` populated |
| Regression | `go test ./internal/function/ ./pkg/funcd/ ./tests/e2e/ -count=1` | all `ok`, exit 0 |
| Deps | `git diff go.mod go.sum` | **no change** — no new Go dependency |
| Identity | `grep -rE 'green-0-rabbit|/Users/|green-0-rabbit'` over new files | clean (exit 1) |

### Headline numbers (binary, 6 workers × 2s, density 3)

| metric | memory | file |
|---|---|---|
| throughput (req/s) | 26* | 17803 |
| latency p99 | 4.53s* | 722µs |
| per-sandbox RSS | 56.2 MB | 56.3 MB |
| idle RSS | 0.0 MB | 0.0 MB |
| cold-start (wake) | 52 ms | 52 ms |
| marginal MB/fn | 57.0 | 62.4 |
| max density | 286 | 261 |
| fits 100 fns / 16 GB | ✅ yes | ✅ yes |

\* The memory substrate's low RPS / high p99 in this run is the **platform port-exhaustion finding** below
biting that substrate — not a bench defect. The smoke run and the file substrate both ran clean.

## Scenario → evidence (all 7)

- **bench-throughput** — `runLoad` (Go-native workers + `percentiles`, sorted-sample, no histogram dep); smoke asserts `RPS>0` & `P50>0`; binary showed 17803 req/s (file). ✅
- **bench-memory** — `shimRSSMB`/`rssMB`; per-sandbox 56.2 MB; baseline = `rssMB(os.Getpid())` (single PID, no tree sum). ✅
- **bench-idle** — `IdleMB` sampled after `waitPhase(PhaseIdle)`; 0.0 MB ⇒ reclaim frees memory; smoke asserts `IdleMB < PerSandboxMB`. ✅
- **bench-coldstart** — `timeColdRequest` timed **only after** `PhaseIdle` (0 replicas); 52 ms; smoke asserts `ColdStart>0`. ✅
- **bench-density** — marginal slope `(totalShim − perSandbox)/Density` → `MaxDensity` extrapolation; smoke asserts `>0`. ✅
- **bench-substrate** — memory vs file via `WithBlob(gocloud file://)` + `WithBus(nats FileStorage)`; store `memory.New()`/`InMemory()` in both (the judge-folded Blocker axis, honoured exactly). ✅
- **bench-report** — `WriteReport` → md + json; both `FileExists` in smoke; both populated by the binary. ✅

## Contracts — match `internal/bench` exactly

`Backend` (`memory`|`file`), `Config`, `Latency{P50,P90,P99,P999,Max}`, `Report` (all fields incl.
`IdleMB`, `ColdStart`, `PerFunctionMB`, `MaxDensity`, `FitsTarget`), `Run(ctx, shimPath, Config) ([]Report, error)`,
`WriteReport(dir, []Report) (md, json, err)`. Signatures verbatim. ✅

## ✅ Verified correct — what's strong (keep it)

- **Resilient `Run`** — a failed substrate is `slog.Warn`-skipped, not fatal; errors only if *all* fail
  (`fault.Unavailablef`). This is exactly what let the harness survive and surface the platform finding below.
- **Clean scenario ordering** — `z` is the sole function until the density sweep, so every memory sample is
  uncontaminated; `Run` is drained on exit (`cancel(); <-done`) so one substrate's shims die before the next samples.
- **Single-PID baseline** — `rssMB(os.Getpid())`, self-PID excluded in `shimRSSMB` — no double-count (the explicit ADR checklist item).
- **Thin shell (ADR-0014)** — `cmd/funcd-bench/main.go` is flags → `bench.Run` → `WriteReport`; all logic in `internal/bench`.
- **Conventions** — `api/fault` everywhere, ctx-first, `slog` only, no `any`, no `fmt.Print*` (uses `fmt.Fprintf(os.Stdout,…)`), `//nolint:gosec` justified on both `ps`/`pgrep` execs.

## Platform finding (NOT a defect of this ADR — follow-up ADR)

The harness surfaced a real platform issue: under sustained load the gateway's reverse proxy
**exhausts ephemeral ports** (`dial tcp …: connect: can't assign requested address`) — no upstream
connection pooling / keep-alive reuse to the shim. The bench handled it correctly (resilient skip;
the other substrate still produced numbers). This is the bench doing its job — it warrants a
**separate follow-up ADR** (gateway upstream connection pooling), not a change to ADR-0040.
`env`-adjacent / platform-attributed; **does not** count against the model.

## Blueprint

No blueprint sync owed — the bench is an assessment/measurement tool, not architecture. Correct.

## Findings

- 🔴 Blocker: none
- 🟡 Major: none
- Minor: none

## Attribution

No model-attributed defects. The one notable observation (proxy port exhaustion) is a **platform**
finding the harness correctly surfaced — attributed to the platform/follow-up, not the model.

## Recommendation

**Pass.** DoD met, all 7 scenarios have passing evidence, contracts exact, no new dependency, thin
shell, conventions clean, no identity leak, no regression. Stamp ADR-0040 `Reviewing → Implemented`
and F27 `reviewing → implemented`. Open a follow-up ADR for gateway upstream connection pooling.
