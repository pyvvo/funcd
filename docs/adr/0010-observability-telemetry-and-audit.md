# ADR-0010: Observability — OTel telemetry pipeline + OTLP log bridge + audit channel

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted 2026-06-14 after judge pass — added missing **Decision** section
  [Blocker]; `NewFanout`→concrete `*Fanout`, `Recorder`→`AuditRecorder`, `telemetry.go` consolidation
  note, no-op `Enabled=false` [Minor] folded in; **Implemented 2026-06-14** — review pass, see
  docs/reviews/adr-0010-implementation-claude-opus-4-8.md; `telemetry.go` + `audit.go` + 6 hermetic
  scenario tests passing, lint clean, OTel SDK/exporter/otelslog deps (Apache-2.0) added)
- **Deciders**: green-0-rabbit
- **Tags**: observability, opentelemetry, otlp, metrics, traces, logs, audit
- **Realizes**: [FEAT-0000/F17b](../feat/0000-feat-v1.md) (observability baseline — OTel telemetry + audit)
- **Relates to**: [ADR-0009](0009-observability-logger-root.md) (the `slog` logger root this *complements*
  — P-F2 adds the OTLP **log** sink + telemetry providers without editing the frozen `NewLogger`),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (no globals §5, `slog`-only §6, `api/fault`
  §3, ctx-first §5), [blueprint.md — Monitoring/Platform logging/Audit](../../blueprint.md). **No cgo**
  (OTel Go SDK + gRPC are pure-Go, Apache-2.0). **Builds the building blocks; the composition root P-I
  wires them into the one root logger** (P-I is where stdout ⊕ OTLP combine).

## Context & Need

The blueprint mandates **OpenTelemetry compliance**: funcd exports its own metrics, traces, and logs
(stream-labeled `source=platform`, distinct from tenant/function telemetry) over **OTLP** to an
external collector (victoria-metrics/logs/traces + grafana). ADR-0009 built the `slog` logger root
(stdout, level switching, trace correlation) but explicitly deferred the **OTel SDK pipeline**, the
**OTLP log bridge**, and the **audit channel** to this ADR. Separately, the blueprint requires an
**audit channel** for security-relevant events (who deployed what, PDP allow/deny decisions) kept on
its *own* stream and retention, **never interleaved with operational logs**. Without this ADR funcd
emits nothing to a collector and has nowhere to send audit events — the "all observable" half of the
V1 exit criterion is unmet, and the PDP (P-N/P-L) has no audit sink to write to.

**Purpose**: define and implement (a) an **OTel telemetry pipeline** — tracer + meter + logger
providers with **OTLP gRPC exporters when configured, no-op when not** (so dev/tests need no
collector) and a clean `Shutdown`; (b) the **OTLP log bridge** — an `otelslog` `slog.Handler` that
ships platform log records over OTLP, plus a **fanout** `slog.Handler` so the composition root can
send the same records to both stdout (ADR-0009) and OTLP; (c) the **audit channel** — a typed
`AuditRecorder` writing structured security events to a dedicated `source=audit` sink. Callers: the
composition root P-I (constructs telemetry, composes the fanout logger, registers `Shutdown`), the API
server / controllers (record spans/metrics), and the PDP (records audit events). Conformance is
mechanical: providers are valid no-ops without an endpoint and SDK-backed with one; a span recorded
through an in-memory exporter carries the platform resource attrs; the log bridge emits a record to its
exporter; the fanout dispatches one record to every sink; an audit event lands on the audit sink with
its typed fields and `source=audit`.

## Scenarios

- `scenario: telemetry-disabled-noop` — **Given** `TelemetryConfig{}` with **no** OTLP endpoint,
  **when** `NewTelemetry` is called, **then** it returns usable **no-op** tracer/meter/logger providers
  and a no-op log handler (nothing dials the network), and `Shutdown` returns nil — dev and tests need
  no collector.
- `scenario: telemetry-otlp-constructs` — **Given** `TelemetryConfig{Endpoint: "localhost:4317"}`,
  **when** `NewTelemetry` is called, **then** it returns **SDK-backed** providers wired to OTLP gRPC
  exporters with no error (lazy-dial — no live collector needed to construct), and `Shutdown(ctx)` with
  a deadline returns without hanging.
- `scenario: telemetry-resource-attrs` — **Given** a tracer provider built with an **in-memory span
  exporter** (the test seam), **when** a span is recorded and flushed, **then** the exported span
  carries the platform resource attributes `service.name=funcd` and `source=platform`.
- `scenario: log-bridge-emits` — **Given** a logger provider built with an **in-memory log exporter**,
  **when** a record is written through the `otelslog` bridge handler, **then** that record (body +
  attrs) reaches the exporter — proving platform logs flow to the OTLP path.
- `scenario: fanout-dispatch` — **Given** a fanout handler over two recording sink handlers, **when**
  one record is logged, **then** **both** sinks receive it — the seam P-I uses to send a record to
  stdout *and* OTLP.
- `scenario: audit-record` — **Given** an `AuditRecorder` over an in-memory sink, **when**
  `Record(ctx, AuditEvent{Actor, Action, Resource, Decision})` is called, **then** the sink holds one
  structured event with those typed fields and `source=audit`, separate from any ops-log stream.

## Scope

**In**:
- `internal/observability/telemetry.go`: `TelemetryConfig`, `Telemetry` holding tracer/meter/logger
  providers; `NewTelemetry(ctx, cfg) (*Telemetry, error)` (OTLP gRPC exporters when `Endpoint != ""`,
  else no-op); accessors `TracerProvider()`/`MeterProvider()`/`LoggerProvider()`/`LogHandler()`; a
  platform `resource.Resource` (`service.name=funcd`, `source=platform`); `Shutdown(ctx)` flushing/
  closing every provider; and a **fanout** handler — concrete `*Fanout` implementing `slog.Handler` (`NewFanout(...slog.Handler) *Fanout`).
- The **OTLP log bridge**: `LogHandler()` returns an `otelslog` `slog.Handler` over the logger
  provider (a no-op handler when telemetry is disabled). OTel log records carry trace context natively.
- `internal/observability/audit.go`: typed `AuditEvent` (+ a typed `AuditDecision` enum), and
  `AuditRecorder` with `NewAuditRecorder(w io.Writer) *AuditRecorder` + `Record(ctx, AuditEvent) error`,
  writing a structured event labeled `source=audit` to a **dedicated** sink.

**Out**:
- **Editing ADR-0009's `NewLogger`** — frozen; P-F2 adds the OTLP sink as a *composable* handler, it
  does not change the stdout logger. **Final stdout ⊕ OTLP composition is P-I's** (it owns the root).
- **Daemon config loading** — P-I supplies a populated `TelemetryConfig` (the `OTEL_EXPORTER_OTLP_*`
  env conventions bind at the composition root; slog/otel parsing lives there).
- **Instrumentation of specific components** (HTTP middleware spans, controller metrics) — each owning
  ADR (P-L, P-J) adds its own spans/metrics *using* these providers; this ADR ships the pipeline only.
- **Audit retention/rotation, query, tamper-evidence, and PDP policy** — V1 ships the **typed audit
  *channel* (stub)**: the structured event + dedicated sink. Durable retention and an audit API are V2.
- **Setting OTel global providers** (`otel.SetTracerProvider`) — providers are **injected** (no
  globals, ADR-0002 §5); whether to also register globals for third-party libs is P-I's call.

## Constraints & Decision drivers

- **C1 — OTel-compliant, OTLP, embed-first (blueprint Monitoring)**: the export protocol is **OTLP**
  to an external collector; the OTel Go SDK is a pure-Go, Apache-2.0 library — embed it, no sidecar.
- **C2 — no collector in dev/CI**: without a configured endpoint the providers are **no-ops** (the OTel
  `noop` packages), so `funcd.InMemory()`, unit tests, and a laptop run need nothing listening on 4317.
- **C3 — ADR-0009 is frozen; no globals (ADR-0002 §5)**: P-F2 must *complement* the logger root via a
  composable handler, never edit it, and never install a package-level provider — everything is
  constructed and returned for injection.
- **C4 — audit ≠ ops logs (blueprint)**: security events go to a **separate** sink (`source=audit`)
  with their own (later) retention — never interleaved with operational `slog` output.
- **C5 — hermetic tests**: behavior is proven with OTel's **in-memory exporters** (spans/logs) and
  in-memory writers (fanout/audit); the OTLP path is proven by *construction + clean shutdown* (lazy
  dial), not by standing up a collector.

## Alternatives considered

**Telemetry export** (driver: blueprint compliance vs operational weight):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **OTel SDK + OTLP gRPC exporters, no-op when unconfigured** | the blueprint's mandate; pure-Go embed; one protocol for metrics/traces/logs; collector-agnostic (victoria/grafana); zero-cost when off | OTLP/SDK API surface + a few exporter modules | **chosen** |
| Prometheus pull + direct Jaeger/Loki clients | familiar | three different client libs/protocols; contradicts "OTel-only" (blueprint) | rejected (not OTel-compliant) |
| stdout/file only, no OTLP | trivial | no metrics/traces to a collector — fails "all observable" at scale | rejected (insufficient for V1 exit) |

**OTLP transport** (driver: collector compatibility): **gRPC** (`otlp*grpc`, port 4317) **chosen** —
the OTel-collector default and victoria's ingestion path; HTTP/protobuf (4318) is a later config swap
behind the same `Endpoint`. Honest cost: gRPC pulls `google.golang.org/grpc` (already in the graph).

**Log bridge integration** (driver: respect ADR-0009's frozen contract):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`otelslog` handler + a fanout handler; P-I composes stdout ⊕ OTLP** | ADR-0009 stays frozen; each piece tested in isolation; OTel log records carry trace context natively | the live wiring lands at P-I (tier 2), not here | **chosen** |
| Supersede ADR-0009 to add a `WithOTLP` option to `NewLogger` | one constructor | rewrites a just-frozen ADR for a sink that composes fine externally; couples the stdout root to OTLP | rejected (needless supersede; violates immutability spirit) |
| Route logs only via the LoggerProvider, drop stdout | one path | loses 12-factor stdout (journald) — blueprint wants **both** | rejected |

**Audit sink for V1** (driver: separation now, durability later): a typed `Recorder` over a dedicated
`source=audit` `slog` JSON sink — **chosen** (stub per the feat row; real retention/query is V2). A
full append-only audit store is rejected for V1 (scope); interleaving audit into ops logs is rejected
(blueprint forbids it).

## Decision

### 1. Telemetry providers — OTLP when configured, no-op otherwise
`NewTelemetry(ctx, cfg)` builds a `Telemetry` holding an OTel **tracer + meter + logger** provider.
When `cfg.Endpoint != ""` it constructs OTLP **gRPC** exporters (`otlptracegrpc`/`otlpmetricgrpc`/
`otlploggrpc`, `Insecure` honored) and SDK providers (`sdktrace`/`sdkmetric`/`sdklog`) over a shared
`resource.Resource` carrying `service.name` (default `funcd`) and `source=platform`. When `Endpoint`
is empty it returns the OTel **`noop`** providers and a no-op log handler. Construction is **lazy** —
no exporter dials the network until first export — so both paths are safe in dev/CI. The
`metrics.go`/`tracing.go` files the blueprint sketches are **consolidated into one `telemetry.go`**
per ADR-0002 §8 (don't pre-split); this is deliberate, not an omission.

### 2. OTLP log bridge + fanout — composed at P-I, never editing ADR-0009
`LogHandler()` returns an `otelslog` `slog.Handler` over the logger provider (OTel log records carry
trace context natively), or a **no-op handler whose `Enabled` is `false`** when telemetry is disabled
(so a disabled fanout short-circuits and pays nothing). `NewFanout(...slog.Handler)` returns a
**concrete `*Fanout`** (implementing `slog.Handler`, ADR-0002 §1 — not an interface return) that
dispatches every record to all inner handlers, `Enabled` being their OR. ADR-0009's `NewLogger` is
**not touched**; the composition root **P-I** builds the final root logger as
`slog.New(NewFanout(stdoutHandler, telemetry.LogHandler()))`.

### 3. Audit channel — typed recorder to a dedicated `source=audit` sink
`AuditRecorder` (via `NewAuditRecorder(w)`) writes typed `AuditEvent`s (with a typed `AuditDecision`
enum) as structured JSON labeled `source=audit` to a **dedicated** writer — never interleaved with
operational logs (blueprint). `Record` returns `fault.Invalid` for an unknown `Decision`. V1 is the
typed *channel* (stub); durable/queryable retention is a V2 audit ADR.

### 4. Injection, no globals, idempotent shutdown
Everything is constructed and returned for injection — **no** `otel.SetTracerProvider`/global state
here (ADR-0002 §5; whether to register globals for third-party libs is P-I's call). `Shutdown(ctx)`
flushes and closes every provider, is **deadline-bounded and idempotent**, and leaks no goroutine.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Audit channel is a typed `Recorder` over a `source=audit` slog sink**, no durable store | V1 needs separation + a stable API, not retention machinery | a V2 audit ADR adds durable, queryable, tamper-evident retention + an audit API |
| **Live stdout ⊕ OTLP logger composition lands in P-I**, not here | ADR-0009's `NewLogger` is frozen; the composition root owns the final chain | P-I wires `NewFanout(stdoutHandler, telemetry.LogHandler())` into the root logger (tier 2) |
| **OTLP path proven by construct + clean shutdown**, not a live export round-trip | no collector in CI (C5) | the F20 e2e/integration lane may add a collector-backed export assertion later |
| **gRPC transport only** (port 4317) | the collector default | an HTTP/protobuf driver is a config swap behind the same `Endpoint` if needed |

## Contracts

### Telemetry (`internal/observability/telemetry.go`)
```go
package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// TelemetryConfig configures the OTel pipeline. An empty Endpoint disables export
// (no-op providers) — dev/tests need no collector. ServiceName defaults to "funcd".
type TelemetryConfig struct {
	Endpoint    string // OTLP gRPC target, e.g. "localhost:4317"; "" → no-op
	ServiceName string // resource service.name; "" → "funcd"
	Insecure    bool   // gRPC without TLS (dev/local collector)
}

// Telemetry holds the platform's OTel providers and the OTLP log handler. It is
// injected (no globals); Shutdown flushes and closes every provider.
type Telemetry struct { /* providers + shutdown funcs, unexported */ }

// NewTelemetry builds the pipeline from cfg: OTLP gRPC exporters + SDK providers
// when cfg.Endpoint != "", else no-op providers. Never dials at construction
// (lazy). Returns fault errors on misconfiguration.
func NewTelemetry(ctx context.Context, cfg TelemetryConfig) (*Telemetry, error)

func (t *Telemetry) TracerProvider() trace.TracerProvider   // SDK or noop
func (t *Telemetry) MeterProvider() metric.MeterProvider    // SDK or noop
func (t *Telemetry) LoggerProvider() log.LoggerProvider     // SDK or noop
func (t *Telemetry) LogHandler() slog.Handler               // otelslog bridge, or a no-op (Enabled=false) handler
func (t *Telemetry) Shutdown(ctx context.Context) error     // flush+close all; idempotent

// Fanout is a slog.Handler that dispatches every record to all inner handlers
// (P-I composes stdout ⊕ OTLP). Enabled is the OR of the inner handlers. Concrete
// return type (ADR-0002 §1: constructors return concrete structs, not interfaces).
type Fanout struct { /* handlers, unexported */ }

func NewFanout(handlers ...slog.Handler) *Fanout
func (f *Fanout) Enabled(ctx context.Context, l slog.Level) bool // OR of inner handlers
func (f *Fanout) Handle(ctx context.Context, r slog.Record) error
func (f *Fanout) WithAttrs(as []slog.Attr) slog.Handler
func (f *Fanout) WithGroup(name string) slog.Handler
```

### Audit (`internal/observability/audit.go`)
```go
package observability

import (
	"context"
	"io"
)

// AuditDecision is the outcome recorded for a security-relevant action.
type AuditDecision string

const (
	AuditAllow AuditDecision = "allow"
	AuditDeny  AuditDecision = "deny"
)

func (d AuditDecision) Validate() error // fault.Invalid on unknown

// AuditEvent is one security-relevant event — who did what to which resource and
// the decision. Typed (no any); the dedicated audit stream, not ops logs.
type AuditEvent struct {
	Actor    string        // authenticated principal / API key id
	Action   string        // e.g. "function.apply", "egress.connect"
	Resource string        // namespaced resource ref
	Decision AuditDecision // allow | deny
	Reason   string        // optional human reason (e.g. PDP rule)
}

// AuditRecorder writes audit events to a dedicated sink (source=audit), separate
// from operational logs. V1 sink is a structured JSON writer; durable retention is V2.
type AuditRecorder struct { /* dedicated *slog.Logger over w, unexported */ }

// NewAuditRecorder builds an AuditRecorder writing to w (composition root passes
// the audit stream; tests pass a buffer).
func NewAuditRecorder(w io.Writer) *AuditRecorder

// Record writes one event; returns fault.Invalid if Decision is unknown.
func (r *AuditRecorder) Record(ctx context.Context, e AuditEvent) error
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002), ADR-0009 logger root (complements it) | `Validate`/misconfig → `fault.Invalid` |
| Adds (lib) | `go.opentelemetry.io/otel/sdk` (trace+resource), `.../sdk/metric`, `.../sdk/log`, `.../log`, `contrib/bridges/otelslog`, `.../exporters/otlp/otlptrace/otlptracegrpc`, `.../exporters/otlp/otlpmetric/otlpmetricgrpc`, `.../exporters/otlp/otlplog/otlploggrpc` | **all Apache-2.0, pure-Go**; most already transitive |
| Exposes | `Telemetry` (providers + `LogHandler` + `Shutdown`), `*Fanout`, `AuditRecorder`/`AuditEvent` | consumed by **P-I** (compose + shutdown), **P-L/P-J** (spans/metrics), **P-N/P-L** (audit) |

## Implementation plan

Real wiring of the OTel SDK + the audit recorder; no cgo.

1. **`internal/observability/telemetry.go`** — `TelemetryConfig`, `Telemetry`, `NewTelemetry`
   (endpoint set → `otlptracegrpc`/`otlpmetricgrpc`/`otlploggrpc` exporters + `sdktrace`/`sdkmetric`/
   `sdklog` providers with a `resource.NewWithAttributes(service.name, source=platform)`; else the
   `noop` providers), accessors, `Shutdown` (flush+close, deadline-bounded, idempotent), `LogHandler`
   (`otelslog.NewHandler` or a no-op `slog.Handler`), and `NewFanout`.
2. **`internal/observability/audit.go`** — `AuditDecision`(+`Validate`), `AuditEvent`, `AuditRecorder`,
   `NewAuditRecorder`, `Record` (a dedicated JSON `*slog.Logger` with `source=audit`).
3. **Deps** — `go get` the OTel SDK/exporter/bridge modules above; `go mod tidy`. No cgo, no build tag.
4. **Test plan** (one named test per Scenario, all passing, hermetic):
   - `telemetry_test.go` → `telemetry-disabled-noop` (no endpoint → noop providers, no-op handler,
     `Shutdown` nil), `telemetry-otlp-constructs` (endpoint set → no error, `Shutdown` with a 1s ctx
     returns), `telemetry-resource-attrs` (an **in-memory span exporter** via `tracetest` proves
     `service.name`/`source` on the span), `log-bridge-emits` (an **in-memory log exporter** via
     `sdk/log/logtest` proves a bridged record arrives), `fanout-dispatch` (two recording handlers
     both receive one record).
   - `audit_test.go` → `audit-record` (in-mem buffer holds the typed event with `source=audit`;
     unknown `Decision` → `fault.Invalid`).
5. **Definition of done** (= Scenarios executed):
   - `just ci` green (build, lint — no `any`/globals/non-`slog` logger, test, mod verify).
   - All six scenarios pass hermetically (no network/collector); `Shutdown` is deadline-bounded and
     idempotent and leaks no goroutine; no-op path dials nothing.
   - Only Apache-2.0 OTel modules added; `go.mod`/`go.sum` tidy; no cgo; ADR-0009 **unchanged**.

## Review checklist

- [ ] `NewTelemetry` returns **no-op** providers + a no-op `LogHandler` when `Endpoint == ""` (nothing
      dials); **SDK-backed** providers + `otelslog` handler when set; construction never dials.
- [ ] The platform `resource` carries `service.name` (default `funcd`) and `source=platform`; proven by
      an in-memory span exporter test.
- [ ] `LogHandler()` is the `otelslog` bridge over the logger provider; `log-bridge-emits` proves a
      record reaches an in-memory log exporter.
- [ ] `NewFanout` returns a concrete `*Fanout` dispatching to **every** inner handler; `Enabled` is the OR;
      no record dropped. The disabled-telemetry `LogHandler()` reports `Enabled()==false` (short-circuits).
- [ ] `Shutdown(ctx)` flushes+closes all providers, is **deadline-bounded and idempotent**, and leaks no
      goroutine; the no-op path’s `Shutdown` is nil.
- [ ] `AuditRecorder` writes typed `AuditEvent`s to a **dedicated** `source=audit` sink (not ops logs);
      `AuditDecision.Validate` → `fault.Invalid`; `Record` rejects an unknown decision.
- [ ] No globals (`otel.Set*` not called here); `slog`-only; ctx-first; no `any` in signatures.
- [ ] Only Apache-2.0 OTel modules added; **ADR-0009 file unchanged**; no identity/path leak.
- [ ] Every Scenario has a named, passing, hermetic test (no collector needed).

## Consequences

- (+) funcd becomes **OTel-compliant**: metrics, traces, and logs export over OTLP to any collector
  (victoria/grafana), `source=platform` — the blueprint Monitoring mandate, embed-first, pure-Go.
- (+) **Zero-cost when off**: no endpoint → no-op providers, so dev/CI/`InMemory()` need no collector;
  ADR-0009's stdout logging keeps working unchanged.
- (+) ADR-0009 stays **frozen**; the OTLP sink composes via a fanout handler — the building blocks are
  testable in isolation and P-I owns the one-line final wiring.
- (+) The **audit channel** exists and is separated from ops logs from day one, so the PDP (P-N/P-L)
  has a typed sink the moment it needs one.
- (−) **Several OTel modules** join the dep graph (SDK + 3 exporters + the bridge) — accepted: all
  Apache-2.0/pure-Go, and OTel is the blueprint's chosen standard.
- (−) The **live stdout ⊕ OTLP logger** isn't wired until P-I, and the OTLP export round-trip isn't
  asserted against a real collector until the F20 lane — documented workarounds, off the critical path.
- (risk) OTLP/SDK API churn across minor versions — mitigated by pinning and the contract tests at the
  in-memory-exporter seam (provider behavior, not exporter wire format).

## Open questions

| Question | Where it gets answered |
|---|---|
| Per-component spans/metrics instrumentation (HTTP middleware, controller workqueue depth) | each owning ADR (P-L, P-J) — they *use* these providers |
| Whether to also register OTel **global** providers for third-party libs | P-I (composition root) — it decides global registration policy |
| Durable, queryable, tamper-evident **audit retention** + an audit API | a V2 audit/security ADR (this V1 channel is the stub) |
| HTTP/protobuf OTLP transport (4318) vs gRPC (4317) | a config/driver swap behind `Endpoint` if a deployment needs it |
| Live collector-backed export assertion | the F20 testing-strategy lane (P-S) |

## References

- [OpenTelemetry Go SDK](https://pkg.go.dev/go.opentelemetry.io/otel/sdk) (Apache-2.0) — trace/metric/
  log providers, `resource`, `noop` providers, `tracetest`/`sdk/log/logtest` in-memory exporters.
- [`contrib/bridges/otelslog`](https://pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelslog)
  (Apache-2.0) — the `slog.Handler` → OTel log bridge.
- OTLP gRPC exporters: `otlptracegrpc`, `otlpmetricgrpc`, `otlploggrpc` (Apache-2.0).
- [ADR-0009](0009-observability-logger-root.md) — the `slog` logger root this complements (frozen).
- [blueprint.md](../../blueprint.md) — "Monitoring and Logging" (OTel-only, OTLP, victoria/grafana),
  "Platform logging" (the `otelslog` OTLP bridge, `source=platform`), and "Audit is not ops logging".
