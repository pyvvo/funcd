# ADR-0053: `funcdcli bench` — a client-side data-plane load/latency subcommand

- **Status**: Implemented
- **Date**: 2026-06-17 (**Implemented 2026-06-18** — review `pass`: 0 Blockers/0 Majors/0 Minors, DoD 7/7. Verified:
  `go list -deps ./cmd/funcdcli/...` has 0 gopsutil/fortio, the `bench-libs` depguard rule is unchanged (a planted fortio
  import in funcdcli fails lint), `internal/loadgen` is stdlib+api/fault only, all 4 scenarios pass, `funcd-bench` untouched.
  F18 stays Implemented (additive verb). **Accepted 2026-06-17** — judge: 1 Blocker + 2 Minors, all folded. Blocker: the `Result` struct used
  invalid grouped-field json tags (`Total, OK, Errors int `+"`json:\"total\",…`"+`) that fail govet/lint and drop fields from
  `--json` — rewritten to one field + one tag per line. Minors: reconciled the "sample cap" prose (exact percentiles over all
  samples, bounded by request count — no cap) and pinned `RPS = OK / actual-elapsed-Duration`. No Major; the judge confirmed
  the ADR-0051 confinement is genuinely preserved (stdlib `internal/loadgen`, depguard unchanged). No blueprint change
  (additive CLI verb); F18 stays Implemented and now links this ADR.)
- **Deciders**: green-0-rabbit
- **Tags**: cli, bench, observability, data-plane
- **Realizes**: [FEAT-0000/F18](../feat/0000-feat-v1.md) (`funcdcli` + Go SDK). *(Homed in F18, not F27: the
  deliverable is a new **funcdcli verb**. F27 is the `funcd-bench` embed+memory harness — a different shape; this relates to
  it but does not extend it. Defensible-default choice, recorded here.)*
- **Relates to**: [ADR-0042](0042-cobra-cli-framework.md) (the cobra command tree it adds a verb to),
  [ADR-0024](0024-funcdcli-and-sdk.md) (funcdcli/SDK), [ADR-0051](0051-bench-adopt-gopsutil-fortio.md) (**preserves** its
  bench-dep confinement — see Decision 4), [ADR-0040](0040-benchmark-sustainability-harness.md) /
  [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md) (the `funcd-bench` embed + memory/cgroup harness, which stays),
  [ADR-0013](0013-gateway-httputil-primary.md)/[ADR-0029](0029-gateway-single-httputil-driver.md) (the gateway data plane it
  drives), [ADR-0002](0002-source-code-conventions-and-patterns.md) (conventions).

## Context & Need

funcd has a benchmark today — `funcd-bench` (ADR-0040) — but it is a **dev/research harness**: it *embeds* the platform
(`funcd.New`), drives it, and *samples memory* (gopsutil RSS, the ADR-0052 cgroup lane). That is the right tool for the
sustainability/density question, and it carries heavy, deliberately **test-only** deps (gopsutil + fortio, confined out of
the shipped binaries by ADR-0051).

What is missing is the everyday operator question: *"I have a function deployed on a running funcd — how fast is it, and
what's the tail latency?"* — answered against the **live data plane**, from the **shipped CLI**, with no platform embedding
and no memory probing. This is exactly the shape of `nats bench` (a client load generator against a running nats-server).
This ADR adds that as a `funcdcli bench` verb.

## Scenarios

- **scenario: bench-reports-throughput-and-latency** — *Given* a function deployed on a running funcd, *when*
  `funcdcli bench --url <data-plane-fn-endpoint> -c N -d Ds` runs, *then* it drives the endpoint with N concurrent workers
  for D seconds and prints **throughput (req/s)** + **latency p50/p90/p99/max** + **total/ok/error** counts.
- **scenario: bench-target-unreachable** — *Given* an unreachable target, *when* `funcdcli bench` runs, *then* it returns a
  clean `api/fault` error (no panic) once it sees that **no** request succeeded — never a crash or a silent success.
- **scenario: bench-no-bench-libs-in-cli** — *Given* the new verb, *when* the import graph is checked, *then* **gopsutil and
  fortio are still absent** from `go list -deps ./cmd/funcdcli/...`, and the `.golangci.yml` `bench-libs` depguard rule is
  unchanged (a planted fortio import in funcdcli fails lint) — the load generator is stdlib-only.
- **scenario: bench-json-output** — *Given* `--json`, *when* the run finishes, *then* it prints a machine-readable summary
  (the same numbers) instead of the human table.

## Scope

**In:** a `bench` cobra verb on the funcdcli root; a stdlib **client** load generator (`internal/loadgen`) — concurrent
`net/http` workers (keep-alive) firing a method/payload at a target URL, closed-model (`-c` workers for `-d` duration **or**
`-n` total requests), collecting per-request latency + ok/error counts → req/s + p50/p90/p99/max; human-table + `--json`
output; flags (`--url`, `--function/-f` + `--data-plane` convenience, `-c`, `-d`, `-n`, `--method`, `--body`,
`--content-type`, `--json`); unit tests against `httptest.Server`.

**Out:** embedding the platform (no `funcd.New`); **any** memory/RSS/cgroup/density measurement (that stays in
`funcd-bench`, ADR-0040/0052); reusing `internal/bench`'s fortio-backed `runLoad` (would pull fortio into the shipped CLI —
forbidden by ADR-0051); open-model / fixed-rate load (closed-model concurrency only); control-plane benching; retiring
`funcd-bench`; relaxing the `bench-libs` depguard rule.

## Constraints & Decision drivers

- **Shipped-binary cleanliness (ADR-0051 is load-bearing).** funcdcli is a product binary; ADR-0051 confined gopsutil +
  fortio to the bench harness and **forbids them in `funcd`/`funcdcli`**, enforced by the `bench-libs` depguard rule. So the
  load generator here is **hand-rolled on the stdlib** — net/http + sort + sync/atomic — adding **no** new dependency.
  `nats bench` is likewise hand-rolled, not a load-lib import.
- **Client tool, not a harness.** It drives a *running* funcd over HTTP like any client; it does not embed or measure the
  platform. This is what makes it a natural CLI verb (vs the `funcd-bench` dev harness).
- **cobra-native (ADR-0042).** A `benchCmd()` method on the existing `*cli`, added via `root.AddCommand`; `api/fault` errors
  printed once by `main` (the root already sets `SilenceErrors`).
- **Honest tail latency from the stdlib.** Percentiles from a sorted slice of all recorded durations (exact, no histogram
  dep). The sample set is bounded by the run's **request count** — one `time.Duration` (8 bytes) per request, which `-n`/`-d`
  keep finite — so no histogram and no sample cap are needed for a CLI run.

## Alternatives considered

- **Move `cmd/funcd-bench` into funcdcli wholesale.** The literal request. *Rejected:* it pulls gopsutil + fortio + the
  embed harness into the shipped CLI, reversing ADR-0051 and failing its depguard rule — a supply-chain regression for a
  feature (client load) that needs none of it. The embed+memory harness is also simply *not* a client command.
- **Add fortio to funcdcli (relax the depguard).** Reuse `runLoad`. *Rejected:* relaxing ADR-0051's confinement to ship a
  load library in the operator CLI is a real dependency-policy reversal; a closed-model HTTP load loop is ~80 lines of
  stdlib. Not worth a heavy transitive tree in the product.
- **`-tags bench` build of funcdcli.** Compile the verb only in a bench build. *Rejected:* then `funcdcli bench` is absent
  from the shipped CLI — defeating the "one CLI, like `nats bench`" goal; and it splits the binary.
- **Put the load loop directly in `cmd/funcdcli`.** *Not chosen:* a small `internal/loadgen` package keeps the engine
  unit-testable (httptest) independent of cobra and reusable; the cobra file stays thin.

## Decision

1. **`funcdcli bench` verb.** Add `(a *cli) benchCmd() *cobra.Command` and register it in `newRootCmdWith`'s
   `root.AddCommand(...)`. `RunE` resolves the target URL, runs the load, renders the result, and returns an `api/fault`
   error on bad flags or a fully-unreachable target.
2. **Target resolution.** `--url` is the explicit data-plane endpoint (e.g. `http://host:8081/function/hello`). As a
   convenience, `--function/-f <name>` + `--data-plane <base>` builds `<base>/function/<name>`. Exactly one path must
   resolve; otherwise a `fault.Invalid` usage error. (No control-plane discovery in V1 — the operator names the endpoint,
   as `nats bench` names the server.)
3. **`internal/loadgen` — stdlib client load engine.** `Run(ctx, Options) (Result, error)`: spin up `Options.Concurrency`
   goroutines, each looping `http.Client.Do` (keep-alive, the method/body/content-type) until the deadline
   (`Options.Duration`) or the shared request budget (`Options.Requests`) is exhausted; record each latency + classify
   2xx→ok else→error; on context cancel, stop. Compute `RPS = OK / Duration.Seconds()` where **Duration is the run's actual
   elapsed wall-clock** (so an `-n` run that finishes early, or a partially-degraded target, still reports a true
   successful-req/s), and P50/P90/P99/Max from the sorted latency slice. No fortio/gopsutil; net/http + sort + sync/atomic +
   context only.
4. **Dependency confinement preserved.** funcdcli gains **no** new module; gopsutil/fortio remain absent from its import
   graph, and the `bench-libs` depguard rule in `.golangci.yml` is **unchanged** (funcdcli is *not* added to its allowlist).
   Verified by `go list -deps ./cmd/funcdcli/...`.
5. **Output.** Default: a concise human table (req/s, p50/p90/p99/max, total/ok/errors, duration) to the command's writer.
   `--json`: the `Result` marshalled as JSON. The unreachable case (`Result.OK == 0` with transport errors) → `RunE`
   returns `fault.Unavailable`.

## Contracts

```go
// internal/loadgen — a stdlib client-side HTTP load generator (NO fortio/gopsutil).
package loadgen

type Options struct {
	URL         string        // target data-plane endpoint
	Method      string        // default "POST"
	Body        string        // request body (sent as-is)
	ContentType string        // default "application/json"
	Concurrency int           // worker goroutines (default 8)
	Duration    time.Duration // closed-model run length (used when Requests == 0)
	Requests    int           // total requests across workers (alternative to Duration); 0 ⇒ use Duration
}

type Result struct {
	Total    int           `json:"total"`
	OK       int           `json:"ok"`
	Errors   int           `json:"errors"`
	Duration time.Duration `json:"duration"` // actual elapsed wall-clock of the run
	RPS      float64       `json:"rps"`       // OK / Duration.Seconds()
	P50      time.Duration `json:"p50"`
	P90      time.Duration `json:"p90"`
	P99      time.Duration `json:"p99"`
	Max      time.Duration `json:"max"`
}

// Run drives URL under load and returns the measured Result. It honors ctx cancellation. It returns a
// non-nil error only on a usage problem (e.g. empty URL); transport failures are counted in Result.Errors,
// not returned — the caller decides whether OK==0 is fatal.
func Run(ctx context.Context, o Options) (Result, error)
```

```go
// cmd/funcdcli — the cobra verb (thin: flags → loadgen.Run → render).
func (a *cli) benchCmd() *cobra.Command
```

**Dependencies & I/O:** **no** new module. `internal/loadgen` imports only the stdlib (net/http, sort, sync, sync/atomic,
context, time) + `api/fault`. `cmd/funcdcli` imports `internal/loadgen` + cobra (already present). Consumes a target URL +
flags; produces a table/JSON on stdout + an exit code.

## Implementation plan

- `internal/loadgen/loadgen.go`: `Options`, `Result`, `Run` (worker pool + latency collection + `percentile` helper over a
  sorted `[]time.Duration`). Stdlib + `api/fault` only.
- `internal/loadgen/loadgen_test.go` (**non-gated**): `Run` against an `httptest.Server` → `Total>0`, `OK==Total`, `RPS>0`,
  `P99>0`, `P50<=Max` (**bench-reports-throughput-and-latency**); `Run` against a closed/refused address → `OK==0 &&
  Errors>0`, no panic (the engine half of **bench-target-unreachable**); a `percentile` unit check.
- `cmd/funcdcli/bench.go`: `benchCmd()` (flags `--url`, `--function/-f`, `--data-plane`, `-c/--concurrency`, `-d/--duration`,
  `-n/--requests`, `--method`, `--body`, `--content-type`, `--json`); URL resolution; render table / JSON; return
  `fault.Invalid` on no target and `fault.Unavailable` on `OK==0`. Register in `newRootCmdWith`.
- `cmd/funcdcli/bench_test.go` (**non-gated**, the cobra seam `root.SetArgs(...).Execute()`): drive `bench --url <httptest>
  -d 200ms` → exit 0 + table contains "req/s" (**bench-reports-throughput-and-latency** end-to-end); `bench` with no target →
  `fault.Invalid` (**bench-target-unreachable** usage half); `bench --url <closed> -n 3` → `fault.Unavailable`
  (**bench-target-unreachable**); `--json` → output parses as JSON with the Result fields (**bench-json-output**).
- **bench-no-bench-libs-in-cli** is verified mechanically, not by a Go test (consistent with ADR-0051): `go list -deps
  ./cmd/funcdcli/...` excludes `shirou/gopsutil` + `fortio.org`; the unchanged `bench-libs` depguard rule rejects a planted
  cross-import. The DoD lists this check.
- **DoD:** `go build ./...` · `go tool golangci-lint run` clean (the unchanged depguard still passes) · `go test ./...`
  green with every scenario test passing · `go mod verify` (no new module) · `go list -deps ./cmd/funcdcli/...` free of
  gopsutil/fortio · no identity/path leak.

## Review checklist

- [ ] `funcdcli bench` exists as a cobra verb (registered in `newRootCmdWith`), `--help` lists it.
- [ ] Drives the data plane over HTTP with `-c` workers for `-d`/`-n`; reports req/s + p50/p90/p99/max + total/ok/errors.
- [ ] `internal/loadgen` is **stdlib-only** (+ api/fault); **no** fortio/gopsutil; **no** new module in go.mod.
- [ ] `go list -deps ./cmd/funcdcli/...` excludes gopsutil + fortio; `bench-libs` depguard rule **unchanged**; a planted
      fortio import in funcdcli fails lint.
- [ ] No-target → `fault.Invalid`; fully-unreachable (OK==0) → `fault.Unavailable`; no panic.
- [ ] `--json` emits a parseable `Result`; default emits the human table.
- [ ] Every scenario has a named, passing, non-gated test; `funcd-bench` untouched; no identity/path leak.

## Consequences

- Operators get the `nats bench` experience from the shipped CLI — `funcdcli bench --url …` against a live function — with
  **zero** new dependencies and **without** disturbing ADR-0051's confinement or the `funcd-bench` harness.
- A second, intentionally-small load path now exists (stdlib `internal/loadgen`) distinct from the fortio-backed
  `internal/bench/load.go`. Accepted: the bench harness keeps fortio's richer accounting for the research lane; the CLI
  stays dependency-clean. The two never share code (the whole point).
- No memory/density answer from the CLI — that remains `funcd-bench` (ADR-0040/0052). Documented so users reach for the
  right tool.

## Open questions

- **Data-plane discovery.** V1 names the endpoint (`--url`, or `--function` + `--data-plane`). Resolving the route from the
  function's control-plane status is a future convenience — deferred to a follow-up if asked; not needed for the verb.

## References

- ADR-0042 (cobra CLI), ADR-0024 (funcdcli/SDK), ADR-0051 (the bench-dep confinement this preserves), ADR-0040/0052 (the
  embed harness that stays), ADR-0002 (conventions). `nats bench` — the client-load-tool analogue:
  https://docs.nats.io/using-nats/nats-tools/nats_cli/natsbench
