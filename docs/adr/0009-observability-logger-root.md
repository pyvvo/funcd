# ADR-0009: Observability — logger root (`internal/observability` slog construction + injection)

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted 2026-06-14 after judge pass — `New`→`NewLogger` rename [Major],
  `WithGroup`-top-level + no-`Record`-retention + `LevelVar` concurrency notes [Minor] folded in;
  **Implemented 2026-06-14** — review pass, see docs/reviews/adr-0009-implementation-claude-opus-4-8.md;
  `internal/observability/logger.go` + 6 scenario tests passing, lint clean, `otel/trace` direct)
- **Deciders**: green-0-rabbit
- **Tags**: observability, logging, slog, trace-correlation, composition-root
- **Realizes**: [FEAT-0000/F17a](../feat/0000-feat-v1.md) (observability baseline — the logger root)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (`slog`-only §6, internal
  component = `New(Config)`/`Deps` struct §1, no globals §5, `api/fault` §3),
  [blueprint.md — Platform logging](../../blueprint.md). **Splits with** the follow-up **P-F2**
  (OTel metrics/traces SDK pipeline + OTLP log bridge + audit channel — F17b): this ADR is the
  pure-`slog` root; P-F2 is the OTel SDK + audit. **No cgo** (stdlib `slog` + the OTel *trace API* only).
  Refines the blueprint's illustrative snippet `observability.NewLogger(cfg.Log)` (line ~431): this ADR
  keeps the name **`NewLogger`** but takes `(Config, io.Writer)` (flat config + an injected writer for
  testable output) rather than a nested `cfg.Log` — a refinement, not a contradiction; no blueprint
  resync needed (the snippet is illustrative, and `NewLogger` is retained deliberately because this
  package will also expose `NewMeterProvider`/`NewTracerProvider`/audit constructors in P-F2).

## Context & Need

Every funcd component logs, and the blueprint fixes exactly *how* the codebase logs (distinct from
function/tenant telemetry): **one API — `log/slog`**; the root logger **built once at bootstrap from
daemon config and injected everywhere** (no package globals, so tests can assert on output); **named
child loggers** (`root.With("component", "controller")`); **runtime level switching** via a
`slog.LevelVar`; and **trace correlation** — a thin `slog.Handler` decorator that lifts
`trace_id`/`span_id` from the request context into every record so a log line links to its trace.
ADR-0002 §6 already mandates `slog`-only and "named child from `internal/observability`", but no ADR
yet *builds* `internal/observability`. Nothing can be wired with a real logger until it exists: the
composition root P-I imports `internal/observability` to construct the root `*slog.Logger` and hand
each component its named child (the tier-1 ports only take a stdlib `*slog.Logger` in their `Deps`).

**Purpose**: define and implement the **logger root** — `observability.NewLogger(cfg, w)` that builds a
root `*slog.Logger` (text in dev, JSON in prod), exposes **named child loggers**, **runtime level
switching**, and a **trace-correlation handler** — as a no-global, injectable component. Callers: the
composition root P-I (builds it, injects children), and the admin log-level endpoint P-L (calls
`SetLevel`). Conformance is mechanical: a configured logger renders at the chosen level/format,
suppresses below-level records, switches level at runtime without reconstruction, stamps
`component=` on every child record, and lifts `trace_id`/`span_id` when the context carries an OTel
span — all assertable against an in-memory writer.

## Scenarios

- `scenario: level-and-format` — **Given** `NewLogger(Config{Level: Info, Format: JSON}, buf)`, **when** a
  child logger's `InfoContext` writes a message, **then** `buf` holds a JSON record at level `INFO`;
  **and** a `DebugContext` call writes nothing (below level).
- `scenario: runtime-level-switch` — **Given** a logger built at level `Info`, **when** `SetLevel(Debug)`
  is called, **then** a subsequent `DebugContext` from an *already-constructed* child logger is emitted
  (no logger rebuild); **and** raising it back to `Info` suppresses `Debug` again.
- `scenario: named-child-component` — **Given** `Component("gateway")`, **when** it logs, **then**
  every emitted record carries `component=gateway`; constructing a second child does not mutate the
  root or the first child.
- `scenario: trace-correlation` — **Given** a `context.Context` carrying a **valid OTel span context**,
  **when** `InfoContext(ctx, …)` logs, **then** the record carries `trace_id` and `span_id` equal to
  the span's; **and** with a context that has **no** span, the same call emits the record with no
  `trace_id`/`span_id` and returns no error.
- `scenario: isolated-instances` — **Given** two independently constructed loggers `a` and `b`,
  **when** `a.SetLevel(Debug)` is called, **then** `b`'s level is unchanged — proving there is no
  package-level shared logger or level state.
- `scenario: invalid-format-rejected` — **Given** `NewLogger(Config{Format: "xml"}, buf)`, **when** it is
  called, **then** it returns a typed `fault.Invalid` error naming `format` and no `*Logger`.

## Scope

**In**:
- `internal/observability` package: a `Logger` facade wrapping a root `*slog.Logger` + a shared
  `*slog.LevelVar`; `Config{Level, Format}`; `Format` typed enum (`text`/`json`) with `Validate()`.
- Handler selection (`slog.NewTextHandler` dev / `slog.NewJSONHandler` prod) over a caller-supplied
  `io.Writer` (the composition root passes `os.Stdout`; tests pass a buffer).
- **Named child loggers** (`Component(name)` → `root.With("component", name)`).
- **Runtime level switching** (`SetLevel`/`Level` over the shared `*slog.LevelVar`).
- **Trace-correlation handler** decorator: lifts `trace_id`/`span_id` from the OTel **span context**
  (the OTel *trace API* only — `go.opentelemetry.io/otel/trace`, no SDK) into every record on the
  `*Context` log paths.

**Out** (→ the follow-up **P-F2 / F17b**, unless noted):
- **OTel metrics/traces SDK pipeline** (meter/tracer providers, OTLP exporters, resource attributes)
  and the **`otelslog` OTLP *log* bridge** (shipping records over OTLP to victoria-logs). P-F's
  handler only *reads* span context already in `ctx`; it neither starts spans nor exports anything.
- **The audit channel** (`internal/observability/audit.go`) — security events, separate retention.
- **The admin HTTP endpoint** `PUT /v1/admin/log-level` — owned by the API server (P-L); this ADR only
  exposes the `SetLevel` seam it calls.
- **Daemon config loading / parsing** — the composition root supplies a populated `Config`; `slog.Level`
  already implements `encoding.TextUnmarshaler` (`"info"`, `"debug"`, `"warn+2"`, …) so TOML/env binding
  is free and needs no custom parser here.
- **Child-process log re-emission** (containerd/OpenBAO stdout capture) — a supervisor concern with the
  runtime/secrets ADRs; it reuses this `slog` pipeline but does not change the root.

## Constraints & Decision drivers

- **C1 — `slog`-only, no globals (ADR-0002 §5/§6)**: one API (`log/slog`); the root is constructed and
  injected from the composition root; **no package-level logger** — enforced by `gochecknoglobals` and
  testable via `scenario: isolated-instances`.
- **C2 — blueprint "Platform logging" is the spec**: built-once/injected, named children, `LevelVar`
  runtime switching, trace correlation via a thin handler decorator, stdout (text dev / JSON prod).
- **C3 — testable output**: because there is no global, an in-memory `io.Writer` lets tests assert on
  records — the no-mocks, real-driver discipline applied to logging.
- **C4 — keep the SDK out of the root**: trace correlation needs only the **OTel trace API** (a small,
  stable, pure-Go, Apache-2.0 module already in the dep graph) — *not* the OTel SDK or any exporter.
  The heavyweight pipeline is P-F2. This keeps the logger root cheap and on no other component's path
  but P-I's.
- **D1 — minimal surface, maximal reuse**: expose a `*slog.Logger` (the stdlib type every `Deps` already
  declares) — components depend on stdlib `slog`, never on this package's concrete type.

## Alternatives considered

**Logger ownership** (driver: who constructs and holds the root logger):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`observability.NewLogger` builds a `*Logger` facade; composition root injects named children; no global** | matches ADR-0002 §5/§6 + blueprint; testable with an in-mem writer; runtime `LevelVar` lives in one place | a small facade type to learn | **chosen** |
| Package-level `slog.Default()` / global logger | zero plumbing | violates no-globals (ADR-0002 §5); untestable output; no per-component level | rejected |
| Return a bare `*slog.Logger`, no facade | least surface | nowhere to hang `SetLevel`/the `LevelVar`; runtime switching needs a shared handle | rejected (loses runtime switching) |

**Trace correlation placement** (driver: keep the SDK off the critical tier-1 path):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Handler decorator here, reading span context via the OTel *trace API* only** | the blueprint files trace correlation under "Platform logging"; API module is tiny/pure-Go/Apache-2.0 and already present; makes the logger complete without the SDK | takes one extra (already-present) dep into the package | **chosen** |
| Defer all trace correlation to P-F2 (with the SDK) | P-F depends on stdlib only | the logger ships *incomplete* — every log line lacks trace linkage until P-F2; the handler is pure-stdlib-adjacent and belongs with the logger | rejected (splits a cohesive concern; the API ≠ the SDK) |
| Inject `trace_id`/`span_id` from plain `context` values set by middleware | no OTel dep at all | re-implements what `trace.SpanContextFromContext` already gives; diverges from OTel span propagation P-F2/middleware will use | rejected (reinvents the standard extractor) |

**Format default**: empty `Format` → **JSON** (blueprint: "stdout JSON by default" — 12-factor; journald/any
collector ingests it). Dev selects `text` explicitly via config/preset. Unknown values are rejected by
`Validate()`.

## Decision

### 1. `Logger` facade — root `*slog.Logger` + shared `*slog.LevelVar`, no global
`observability.NewLogger(cfg Config, w io.Writer) (*Logger, error)` validates `cfg.Format`, builds a
`*slog.LevelVar` set to `cfg.Level`, constructs the chosen base handler
(`slog.NewTextHandler`/`slog.NewJSONHandler` over `w`, `HandlerOptions{Level: levelVar}`), wraps it in
the **trace-correlation decorator**, and holds the resulting root `*slog.Logger`. It is an internal
component, so it takes a `Config` value (ADR-0002 §1) and returns `(*Logger, error)` because `Format`
can be invalid. No package-level state — two `NewLogger` calls are fully independent. The constructor
keeps the blueprint's `NewLogger` name (not a bare `New`) because the package will also hold the OTel
provider + audit constructors from P-F2; `slog.LevelVar` and the stdlib handlers are goroutine-safe, so
`SetLevel` is race-free during live logging (a runtime-admin operation).

### 2. Named children + runtime level
`(*Logger).Component(name string) *slog.Logger` returns `root.With("component", name)` — the stdlib
type every `Deps.Logger` field already declares. `(*Logger).Root() *slog.Logger` exposes the root.
`(*Logger).SetLevel(slog.Level)` and `Level() slog.Level` read/write the **shared** `*slog.LevelVar`,
so a switch takes effect on *already-constructed* children with no rebuild — the seam P-L's admin
endpoint drives.

### 3. Trace-correlation handler (OTel trace **API** only)
A `slog.Handler` decorator forwards `WithAttrs`/`WithGroup`/`Enabled` and, in `Handle`, calls
`trace.SpanContextFromContext(ctx)`; if the span context is valid it adds `trace_id` and `span_id`
string attrs to the by-value `Record` before delegating (add-then-forward only — the record is never
retained, so no `Record.Clone` is needed). No span is ever started and nothing is exported — that is
P-F2. With no span in `ctx`, it is a transparent pass-through. **`WithGroup` is not used on the root
logger** (components attach context via `.With("component", …)` = `WithAttrs`, never an open group), so
`trace_id`/`span_id` always render as **top-level** fields for victoria-logs correlation; this
invariant is recorded so the decorator stays simple.

### 4. Errors + defaults
`Format.Validate()` returns `fault.Invalidf("format", …)` on an unknown value → HTTP 400-mappable at
any edge. Empty `Format` defaults to `FormatJSON`; zero `Level` is `slog.LevelInfo` (slog's zero
value). `NewLogger` is the only constructor; there is no `init()`.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| Logs go only to the supplied `io.Writer` (stdout); **no OTLP export** | the OTLP log bridge needs the OTel SDK pipeline | **P-F2 (F17b)** adds the `otelslog` bridge as an additional composed handler — same records, OTLP stream |
| Trace correlation reads span context but funcd starts **no spans** yet | span creation is the SDK/middleware concern | **P-F2** stands up the tracer provider; middleware (P-L) starts request spans the handler then correlates |
| Runtime level switch is exposed as `SetLevel`, not an HTTP route | the route is an API-server concern | **P-L** wires `PUT /v1/admin/log-level` onto `SetLevel` |

## Contracts

### The package (`internal/observability/logger.go`)
```go
package observability

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Format selects the slog handler rendering. Empty defaults to FormatJSON.
type Format string

const (
	FormatText Format = "text" // human-readable, dev
	FormatJSON Format = "json" // structured, production (blueprint default)
)

// Validate reports whether f is a known format. Unknown → fault.Invalid.
func (f Format) Validate() error // "" treated as valid (default; NewLogger applies JSON)

// Config configures the root logger (internal component → value config, ADR-0002 §1).
// Level is a slog.Level (implements encoding.TextUnmarshaler, so "info"/"debug"/… bind
// directly from TOML/env at the composition root — no parser needed here).
type Config struct {
	Level  slog.Level // initial level; switchable at runtime via Logger.SetLevel
	Format Format     // text (dev) | json (prod); empty → json
}

// Logger is the platform's root logging facade: a root *slog.Logger whose level is
// switchable at runtime, plus named child loggers. No package-level global.
type Logger struct {
	root  *slog.Logger
	level *slog.LevelVar
}

// NewLogger builds the root Logger from cfg, writing to w (the composition root
// passes os.Stdout; tests pass a buffer). Returns fault.Invalid if cfg.Format is
// unknown. Name matches blueprint "Platform logging"; the package later also exposes
// NewMeterProvider/NewTracerProvider/audit constructors (P-F2), so it is NewLogger,
// not a bare New. slog.LevelVar + the handlers are goroutine-safe (SetLevel is safe
// during live logging).
func NewLogger(cfg Config, w io.Writer) (*Logger, error)

func (l *Logger) Root() *slog.Logger                 // the root logger
func (l *Logger) Component(name string) *slog.Logger // root.With("component", name)
func (l *Logger) SetLevel(level slog.Level)          // runtime switch via the shared LevelVar
func (l *Logger) Level() slog.Level                  // current level
```

### Trace-correlation handler (same file; unexported)
```go
// traceHandler lifts trace_id/span_id from the OTel span context in ctx into every
// record. It starts no spans and exports nothing (that is P-F2). Pass-through when
// ctx carries no valid span.
type traceHandler struct{ inner slog.Handler }

func (h traceHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }
func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()))
	}
	return h.inner.Handle(ctx, r)
}
func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler { return traceHandler{h.inner.WithAttrs(as)} }
func (h traceHandler) WithGroup(name string) slog.Handler    { return traceHandler{h.inner.WithGroup(name)} }
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002 §3), stdlib `log/slog`/`context`/`io` | `Format.Validate` → `fault.Invalid` |
| Adds (lib) | `go.opentelemetry.io/otel/trace` (OTel **trace API** only) | **Apache-2.0**, pure-Go, already transitively present; **not** the SDK |
| Exposes | `observability.Logger` (`Root`/`Component`/`SetLevel`/`Level`), `Config`, `Format` | consumed by **P-I** (composition root builds it, injects children into every `Deps.Logger *slog.Logger`) and **P-L** (admin endpoint → `SetLevel`) |

## Implementation plan

No business logic — slog construction + a one-method handler decorator.

1. **`internal/observability/logger.go`** — `Format`+`Validate`, `Config`, `Logger`, `NewLogger`
   (validate format, `*slog.LevelVar`, base text/JSON handler over `w`, wrap in `traceHandler`, build
   root), `Root`/`Component`/`SetLevel`/`Level`, and the unexported `traceHandler` (add-then-forward,
   no `Record` retention; `WithGroup` unused on the root).
2. **Deps** — `go get go.opentelemetry.io/otel/trace` (promote the existing indirect to direct);
   `go mod tidy`. No SDK, no cgo, no build tag.
3. **`internal/observability/logger_test.go`** — one named test per Scenario, all passing:
   - `level-and-format` (JSON handler over a `bytes.Buffer`; assert level + `component`; Debug suppressed),
   - `runtime-level-switch` (`SetLevel(Debug)` then a child `DebugContext` appears; raise → suppressed),
   - `named-child-component` (`component=` on every child record; second child independent),
   - `trace-correlation` (build a valid `trace.SpanContext` via `trace.ContextWithSpanContext`; assert
     `trace_id`/`span_id`; empty ctx → absent, no error),
   - `isolated-instances` (two `NewLogger`s; `SetLevel` on one doesn't move the other),
   - `invalid-format-rejected` (`Format: "xml"` → `fault.KindOf == Invalid`, nil `*Logger`).
4. **Definition of done** (= Scenarios executed):
   - `just ci` exits 0 (build, lint — no globals/`any`/non-`slog` logger, test, mod verify).
   - All six scenario tests pass against an in-memory writer; `SetLevel` affects pre-built children;
     trace fields appear iff a valid span is in `ctx`; `Format` validation maps to `fault.Invalid`.
   - Only `go.opentelemetry.io/otel/trace` promoted to direct (Apache-2.0); `go.mod`/`go.sum` tidy;
     no package-level logger/global; no cgo.

## Review checklist

- [ ] `internal/observability/logger.go` defines `Logger`, `Config`, `Format`(+`Validate`), `NewLogger`,
      `Root`/`Component`/`SetLevel`/`Level`, and an unexported `traceHandler`; **no package-level logger
      or global** (`gochecknoglobals` clean).
- [ ] `NewLogger` selects text/JSON by `Format`, writes to the injected `io.Writer`, and shares one
      `*slog.LevelVar` across the root and all children; empty `Format` defaults to JSON.
- [ ] `traceHandler` adds correlation attrs top-level (no `WithGroup` on the root) and does not retain
      the `Record` (add-then-forward, no `Clone`).
- [ ] `Component(name)` stamps `component=name`; children are independent; `Root()` returns the
      decorated root `*slog.Logger`.
- [ ] `SetLevel` changes the level of **already-constructed** children (no rebuild); `Level()` reflects it.
- [ ] `traceHandler` adds `trace_id`/`span_id` **iff** `ctx` carries a valid OTel span context, forwards
      `WithAttrs`/`WithGroup`/`Enabled`, and starts/exports nothing.
- [ ] `Format.Validate()` returns `fault.Invalid` on unknown; `NewLogger` returns it and a nil `*Logger`.
- [ ] Only the OTel **trace API** added (Apache-2.0); **no SDK/exporter, no audit, no OTLP bridge** here.
- [ ] Every Scenario has a named, passing test; `slog`-only; ctx-first on the `*Context` paths; no
      identity/path leak.

## Consequences

- (+) The platform gets its **single, injectable, no-global logger root** — built once, named children,
  runtime level switch, trace correlation — exactly the blueprint "Platform logging" spec, testable
  against an in-memory writer.
- (+) **P-I is unblocked**: the composition root can construct the root logger and fill every
  `Deps.Logger`; tier-1 ports keep depending only on stdlib `*slog.Logger`.
- (+) Pulling in only the **OTel trace API** (not the SDK) keeps this off every other component's
  dependency and build path — the SDK weight lands in P-F2 where it belongs.
- (−) The logger **does not export to OTLP** and funcd **starts no spans** yet — both are P-F2; until
  then `trace_id`/`span_id` only appear once spans exist (documented workarounds).
- (risk) Trace correlation depends on middleware actually putting span context in `ctx` (P-F2/P-L); a
  missing span silently yields no fields — acceptable and tested (`trace-correlation` empty-ctx case).

## Open questions

| Question | Where it gets answered |
|---|---|
| OTel SDK pipeline (meter/tracer providers, OTLP exporters), the `otelslog` OTLP log bridge, audit channel | **P-F2 / F17b** (the next observability ADR) |
| The admin `PUT /v1/admin/log-level` route wiring onto `SetLevel` | API server **P-L / F07** |
| Per-component level overrides (not just a global level) | revisit with P-L if operators need it; the `LevelVar`-per-component seam is a later extension |
| Daemon config schema/loading that populates `Config` | the facade/config wiring in **P-I / F04** |

## References

- Go stdlib [`log/slog`](https://pkg.go.dev/log/slog) — `Handler`, `LevelVar`, `Text`/`JSON` handlers,
  `*Context` methods; `slog.Level` implements `encoding.TextUnmarshaler`.
- [`go.opentelemetry.io/otel/trace`](https://pkg.go.dev/go.opentelemetry.io/otel/trace) (Apache-2.0) —
  `SpanContextFromContext`, `SpanContext.TraceID/SpanID/IsValid` (the **API**, not the SDK).
- [ADR-0002](0002-source-code-conventions-and-patterns.md) — `slog`-only (§6), no globals (§5),
  internal-component construction (§1), `api/fault` (§3).
- [blueprint.md](../../blueprint.md) — "Platform logging" (built-once/injected, named children,
  `LevelVar` runtime switching, trace correlation, stdout text/JSON).
