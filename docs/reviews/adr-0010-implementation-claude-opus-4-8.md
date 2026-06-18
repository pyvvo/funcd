# ADR-0010 Implementation Review — OTel telemetry + audit (model: claude-opus-4-8)

**Verdict**: **pass** — DoD met, zero Blockers/Majors. The OTel pipeline, OTLP log bridge, fanout,
and audit channel realize the Contracts; all six scenarios have named, un-skipped, hermetic, passing
tests; ADR-0009 stays frozen. One non-blocking Minor (error-path exporter cleanup).
**Reviewed against**: ADR-0010 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (§1/§5/§6) ·
blueprint "Monitoring"/"Platform logging"/"Audit" · FEAT-0000/F17b.
**Date**: 2026-06-14

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./internal/observability/...` | exit 0 |
| Lint (full tree) | `go tool golangci-lint run ./...` | **0 issues**, exit 0 |
| Tests (no cache) | `go test -count=1 -v ./internal/observability/...` | 12/12 PASS (6 for F17b), exit 0 |
| Tree vs ADR surface | `git status --porcelain` | `telemetry.go` + `audit.go` + their `_test.go` — exactly the ADR plan, nothing extra |
| Stubs/skips | `grep "not implemented\|t.Skip\|TODO"` | none |
| `any`/`panic` in sigs | `grep -E "\bany\b\|interface\{\}\|panic\("` | only in **comments** ("whether any inner handler", "no any") — none in code |
| ADR-0009 frozen | `git diff --stat docs/adr/0009-*.md` | empty (unchanged, still `Implemented`) |
| Deps | `grep otel go.mod` (direct block) | OTel SDK v1.44.0 / log v0.20.0 / otelslog v0.19.0 / 3 OTLP exporters — all Apache-2.0 |
| Identity | identity grep (username / `/Users/` paths / email) | clean |

`just ci` fails only on the uncommitted-`go.mod` commit-gate (the legitimate dep additions) — the four
substantive sub-checks all pass; not an implementation defect.

## 🔴 Blockers
None.

## 🟡 Major
None.

## Minor
- **`NewTelemetry` error path doesn't close already-constructed exporters.** Evidence:
  `telemetry.go` `NewTelemetry` — if `otlpmetricgrpc.New` (or the log exporter) returns an error, the
  earlier `traceExp` (and `metricExp`) are dropped without `Shutdown`. *Attribution: `model`.*
  Non-blocking: the OTLP gRPC exporters don't dial or spawn goroutines until first export, and the SDK
  providers (which own the background goroutines) aren't built yet on this path — so the "leak" is an
  unreferenced struct that GCs. A defensive `defer`-on-error close would tidy it. The happy path and
  the no-op path are fully correct.

## ✅ Verified correct — keep it
- **No-op-without-endpoint is real and proven** (`telemetry-disabled-noop`): empty `Endpoint` → OTel
  `noop` tracer/meter/logger providers + a `discardHandler` whose `Enabled()` returns **false**
  (asserted), and `Shutdown` returns nil. Dev/CI/`InMemory()` need no collector. Keep.
- **OTLP construction is hermetic and bounded** (`telemetry-otlp-constructs`): endpoint set → an
  `*sdktrace.TracerProvider` (type-asserted, not no-op), constructed without dialing; `Shutdown` is
  proven to *return within the deadline* (goroutine + `select`/timeout guard) rather than asserting a
  clean flush against an absent collector — a faithful, non-flaky encoding of the scenario. The 2.0s
  is the bounded metric-reader shutdown; acceptable.
- **Resource attrs verified through the export path** (`telemetry-resource-attrs`): a real span via an
  in-memory `tracetest` exporter carries `service.name=funcd` + `source=platform`. Raw
  `attribute.String("service.name", …)` (not `semconv`) is a deliberate, correct choice that avoids a
  semconv version pin — don't "fix" it.
- **OTLP log bridge proven** (`log-bridge-emits`): a record through `otelslog` reaches an in-memory
  `sdklog.Exporter` after `ForceFlush` — the bridge mechanism works, exporter-agnostic. The shared
  `logHandlerFor` helper keeps the test and `NewTelemetry` on the same construction path.
- **`*Fanout` is a concrete type** (ADR-0002 §1) implementing all four `slog.Handler` methods,
  clones the record per sink, ORs `Enabled`, and joins errors; `fanout-dispatch` proves both sinks
  receive one record.
- **Audit separated from ops logs** (`audit-record`): typed `AuditEvent` via `LogAttrs` (no `any`) to a
  dedicated `source=audit` JSON sink; `AuditDecision.Validate` → `fault.Invalid`, and a rejected event
  writes nothing (byte-length asserted). Matches the blueprint's hard audit/ops separation.
- **No OTel globals** — `otel.Set*` is never called; providers are injected (ADR-0002 §5). `Shutdown`
  is idempotent (`shutdownFuncs` niled after first call).

## Conventions spot-check
slog-only ✓ · ctx-first ✓ · no `any` in signatures ✓ · no globals/`init` ✓ · `api/fault` for typed
errors ✓ · package files split by genuine responsibility (telemetry vs audit, per blueprint, not a
premature fan-out) ✓ · import graph clean ✓ · no cgo ✓.

## DoD
ADR Review-checklist: **9/9** items hold. Scenarios: 6/6 named, un-skipped, hermetic, passing.

## Recommendation
**pass** → stamp ADR-0010 `Reviewing → Implemented`, feat F17b → `implemented`. The lone Minor
(error-path exporter cleanup) is optional hardening, not a rework trigger.
