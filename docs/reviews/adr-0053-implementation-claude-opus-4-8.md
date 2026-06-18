# Review — ADR-0053 `funcdcli bench` (implementation)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0053 implementation, model: claude-opus-4-8)

The implementation realizes ADR-0053 faithfully: a stdlib-only `internal/loadgen` engine and a thin
cobra `bench` verb that preserves ADR-0051's bench-dep confinement (funcdcli ships **zero** gopsutil /
fortio), with all four scenarios covered by named, un-skipped, passing tests. Every Review-checklist
item and the generic phase DoD hold against captured evidence. No Blockers, no Majors, no Minors.

All commands below were run through the pinned dev shell (`nix develop -c …`, go1.26.4).

---

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

None.

---

### ✅ Verified correct (keep it)

**Build / format / lint / tests — all green (evidence):**
- `gofmt -l` on the 5 touched files → no output, `gofmt-exit=0`.
- `go build ./...` → `build-exit=0` (whole repo, not just touched pkgs).
- `go test ./internal/loadgen/... ./cmd/funcdcli/...` → `ok` both packages, `test-exit=0`.
- `go tool golangci-lint run ./internal/loadgen/... ./cmd/funcdcli/...` → `0 issues.`, `lint-exit=0`
  (govet, depguard, gochecknoglobals/inits, forbidigo all in the active set and clean).
- `go mod verify` → `all modules verified`, `modverify-exit=0`.

**The crux scenario `bench-no-bench-libs-in-cli` — confinement genuinely preserved:**
- `go list -deps ./cmd/funcdcli/...` → `shirou/gopsutil` count **0**, `fortio` count **0**.
- `git diff go.mod go.sum` → empty (no new module).
- `git diff .golangci.yml` → empty; the `bench-libs` depguard rule (`.golangci.yml:72-84`) is
  unchanged and funcdcli is **not** added to its file-allowlist (it exempts only `internal/bench/**`
  and `cmd/funcd-bench/**`).
- Planted-import proof: a temporary `import _ "fortio.org/fortio/fhttp"` in `cmd/funcdcli` was
  rejected by lint — `import 'fortio.org/fortio/fhttp' is not allowed from list 'bench-libs' …
  (depguard)`. Probe file removed; `git status` confirms no probe artifacts remain.

**Scenario coverage (named, un-skipped, passing):**
- `bench-reports-throughput-and-latency` — engine: `loadgen_test.go:13`
  `TestRunReportsThroughputAndLatency` (Total>0, OK==Total, RPS>0, P99>0, P50<=Max); end-to-end:
  `bench_test.go:16` `TestScenarioBenchReportsThroughputAndLatency` (output contains `req/s`).
- `bench-target-unreachable` — both halves: usage (`bench_test.go:34` `TestScenarioBenchNoTarget`
  → `fault.Invalid`); fully-unreachable OK==0 (`bench_test.go:43` `TestScenarioBenchUnreachable` →
  `fault.Unavailable`); engine half (`loadgen_test.go:39` `TestRunUnreachableCountsErrors` —
  OK==0, Errors>0, no error returned, no panic).
- `bench-json-output` — `bench_test.go:52` `TestScenarioBenchJSONOutput` unmarshals the output and
  asserts numeric fields.

**Contracts honoured (`internal/loadgen/loadgen.go`):**
- `Options` / `Result` / `Run(ctx, Options) (Result, error)` signatures match the ADR Contracts
  exactly. `Run` is ctx-first; returns a non-nil error **only** on the usage case (empty URL,
  `loadgen.go:62-64`), counting transport failures in `Result.Errors` rather than returning them
  (`doOne`, `loadgen.go:130-145`).
- **The pre-accept Blocker is resolved**: the `Result` struct now carries **one json tag per field**
  (`loadgen.go:44-54`). Captured real marshal output: `{"total":5,"ok":5,"errors":0,"duration":…,
  "rps":…,"p50":…,"p90":…,"p99":…,"max":…}` — all 9 fields present, govet/lint clean.
- Load loop is sound (closed-model): `Options.Concurrency` workers; `-n` mode uses an atomic
  claim-before-issue (`atomic.AddInt64(&consumed,1) > budget` → return) so the request budget is
  **exact** — a probe across concurrency 1/4/16 with `Requests:100` produced `Total==100` every time
  (no over- or under-fire). `-d` mode stops on deadline; `ctx.Err()` aborts both. Per-worker latency
  slices are merged then sorted once; `RPS = OK / actual-elapsed.Seconds()` (`loadgen.go:120-121`),
  matching Decision 3. `percentile` is nearest-rank over the sorted slice, 0 on empty
  (`loadgen.go:148-161`), with a unit test (`loadgen_test.go:56`).

**CLI wiring (`cmd/funcdcli/bench.go`, `cli.go`):**
- `a.benchCmd()` registered in `newRootCmdWith`'s `root.AddCommand` (`cli.go:48`); `funcdcli --help`
  lists it: `bench   Load-test a deployed function's data-plane endpoint …`.
- Target resolution (`benchTarget`, `bench.go:66-75`): `--url`, or `--function` + `--data-plane` →
  `<base>/function/<name>`; neither → `fault.Invalid`. OK==0 → `fault.Unavailable` (`bench.go:42-44`).
  `--json` path marshals `Result`; default renders the human table with a `req/s` line.

**Conventions (ADR-0002):**
- `internal/loadgen` imports only stdlib (bytes, context, io, net/http, sort, sync, sync/atomic,
  time) + `api/fault` — no fortio/gopsutil. Errors via `api/fault` (`Invalidf`, `Unavailablef`,
  `Wrapf`). No `panic`, no `any` in hand-written signatures, no package-level globals/inits (the
  `const` block is fine; gochecknoglobals/inits clean). The `...any` in `cli.writef` is the
  sanctioned printf variadic, pre-existing.

**Tree:**
- `cmd/funcd-bench` and `internal/bench` do not appear in `git status` — the fortio-backed harness
  is untouched (the two load paths never share code, as the ADR intends).
- Tree matches the ADR's Implementation plan / Repository surface exactly: `internal/loadgen/
  loadgen.go` + `loadgen_test.go`, `cmd/funcdcli/bench.go` + `bench_test.go`, and the `cli.go`
  registration edit — nothing missing, nothing unexplained-extra.

---

### Definition of Done

**7 / 7** ADR Review-checklist items hold:

1. ✅ `funcdcli bench` registered in `newRootCmdWith`; `--help` lists it.
2. ✅ Drives the data plane over HTTP with `-c` workers for `-d`/`-n`; reports req/s + p50/p90/p99/max
   + total/ok/errors.
3. ✅ `internal/loadgen` stdlib-only (+ api/fault); no fortio/gopsutil; no new module in go.mod.
4. ✅ `go list -deps ./cmd/funcdcli/...` excludes gopsutil + fortio; `bench-libs` rule unchanged; a
   planted fortio import in funcdcli fails lint.
5. ✅ No-target → `fault.Invalid`; fully-unreachable (OK==0) → `fault.Unavailable`; no panic.
6. ✅ `--json` emits a parseable `Result`; default emits the human table.
7. ✅ Every scenario has a named, passing, non-gated test; `funcd-bench` untouched; no identity/path
   leak.

Generic phase DoD: full suite green, every scenario passing, real behaviour (no stubs), Contracts
honoured, tree matches, ADR-0002 conventions, deps sanctioned (none added), no scope creep, hygiene
+ tracking — all hold.

Note (tracking, not a defect): F18's feat row reads `implemented` (`docs/feat/0000-feat-v1.md:83`)
because F18 was already an Implemented feature (ADR-0024/0042); ADR-0053 is an **additive verb** to
that row and correctly links itself into the row's ADR list rather than walking the row backward to
`reviewing`. The ADR file itself is at `Reviewing` as required, and is a new (untracked) file, so its
substance has no committed baseline to mutate.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0053 (implementation) → pass, 0 blockers / 0 majors / 0 minors,
0 model-attributed, DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation

**Sign off.** The work is done and verified — stamp ADR-0053 `Reviewing → Implemented` (and keep the
F18 row at `implemented` with the ADR-0053 link, which the builder already added). Nothing loops back
to the builder; no superseding ADR is needed. The crux confinement (stdlib-only load engine,
depguard unchanged, zero gopsutil/fortio in the funcdcli dep graph, planted import rejected) is the
load-bearing property and it is solid — do not regress it.
