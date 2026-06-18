# ADR-0014: Platform facade, lifecycle & the `InMemory()` e2e-harness (`pkg/funcd`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass, see docs/reviews/adr-0014-implementation-claude-opus-4-8.md;
  `pkg/funcd` facade/presets/lifecycle + `cmd/funcd` shell + 4 harness scenario tests passing, `just ci`
  green. Accepted 2026-06-14 after
  judge pass — `Shutdown` made concurrency-safe via
  `sync.Once` + clarified as a registered closer-list drain (store/blob via `io.Closer` assertion);
  `missing-required-dep` noted as fleshing out ADR-0002's `facade-missing-dep` skeleton [Minor]. No
  Blockers/Majors.)
- **Deciders**: green-0-rabbit
- **Tags**: facade, composition-root, lifecycle, presets, harness, library-first, platform
- **Realizes**: [FEAT-0000/F04](../feat/0000-feat-v1.md) (platform-as-a-library facade)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (the `funcd.New(opts...)`
  functional-options facade shape, `api/fault`, no globals), the substrate/port ADRs it wires —
  [ADR-0006](0006-store-database-layer-port.md) (store), [ADR-0007](0007-blob-storage-layer-port.md)
  (blob), [ADR-0008](0008-bus-messaging-port.md) (bus), [ADR-0009](0009-observability-logger-root.md)
  (logger), [ADR-0011](0011-runtime-sandbox-port.md) (runtime), [ADR-0013](0013-gateway-ingress-httputil-primary.md)
  (gateway) — and [blueprint.md — Platform as a library / Single-binary process model](../../blueprint.md).
  **The controller-driven boot reconcile is a seam here; the reconcile loops land with P-J (F08).**

## Context & Need

The blueprint's first rule is **library-first**: "the entire platform is an embeddable Go library
(`pkg/funcd`); `cmd/funcd` is a thin shell that parses configuration, selects the drivers, and calls
`funcd.New(...).Run(ctx)`. E2e tests embed the very same library with in-memory drivers — no daemon, no
root, no network." ADR-0002 fixed the *shape* (`New(opts ...Option)`, `With*`, `InMemory()`/`Production()`
presets) but left the bodies as stubs (`Run` returns `not implemented`, presets are empty). Now that the
six ports it composes are built (store/blob/bus/logger/runtime/gateway), the **composition root** can be
assembled: the one place that wires every port into a `*Platform`, owns its start/stop lifecycle, and —
crucially — provides the **`InMemory()` preset** that boots a fully-working platform with **no root, no
containerd, no network ports** so every later feature can be e2e-tested on the real code paths.

**Purpose**: implement `pkg/funcd` — `New(opts...)` (functional options + required-dep validation),
the `With*` injectors, the `InMemory()` and `Production()` presets that construct + wire the drivers,
`Run(ctx)`/`Shutdown(ctx)` lifecycle, and the **e2e-harness slice**: a booted `InMemory()` platform whose
wired ports round-trip end-to-end. Callers: `cmd/funcd` (boots `Production()`), and every e2e/integration
test (boots `InMemory()`). Conformance is mechanical: `New(InMemory())` returns a platform with all ports
wired; a missing required dependency is a typed `fault.Invalid` (never a partial platform/panic); the
booted platform's store/blob/bus/gateway round-trip; `Run` blocks until `ctx` is cancelled and returns
nil; `Shutdown` is idempotent and closes the components.

## Scenarios

- `scenario: inmemory-boots` — **Given** `funcd.New(funcd.InMemory())`, **when** it is called, **then**
  it returns a `*Platform` and no error, with every port wired (none nil).
- `scenario: inmemory-ports-roundtrip` — **Given** a booted `InMemory()` platform, **when** the harness
  drives its wired ports, **then** the **store** Put/Get round-trips, the **blob** Put/Get round-trips,
  the **bus** Publish/Subscribe delivers, and the **gateway** programs a route + proxies to a test
  upstream — proving the platform works on real code paths with no root/containerd/network.
- `scenario: missing-required-dep` — **Given** `funcd.New()` with no preset and a required dependency
  absent, **when** it is called, **then** it returns a typed `fault.Invalid` naming the missing dependency
  and a nil `*Platform` — never a partial platform, never a panic (ADR-0002 `facade-missing-dep`, realized).
- `scenario: run-shutdown-lifecycle` — **Given** a booted platform, **when** `Run(ctx)` is started and
  `ctx` is then cancelled, **then** `Run` returns nil (graceful); **and** `Shutdown` closes the components
  (the bus is closed) and is idempotent (a second `Shutdown` returns nil).

## Scope

**In**:
- `pkg/funcd`: `config` holding the wired ports (`store.Store`, `blob.Bucket`, `bus.Bus`,
  `runtime.Runtime`, `gateway.Gateway`, `*slog.Logger`, `*observability.Telemetry`); the `With*` options
  (`WithStore`/`WithBlob`/`WithBus`/`WithRuntime`/`WithGateway`/`WithLogger`/`WithTelemetry`, plus the
  existing `WithLogger`); `New(opts...)` with required-dep validation; `Run`/`Shutdown` lifecycle;
  `InMemory()` and `Production()` presets (drivers constructed + injected); a `components` shutdown list.
- The **e2e-harness slice**: the booted `InMemory()` platform whose ports round-trip — the foundation
  every later feature's e2e test embeds (tested here in `pkg/funcd`, white-box, since the ports are
  `internal/**` types).
- `cmd/funcd` thin shell (if absent) wiring `funcd.New(funcd.Production()...).Run(ctx)` with signal
  handling — kept minimal (config parsing + driver selection only; zero business logic).

**Out**:
- **The controller / reconcile loops** (watch→diff→act→status) — **P-J / F08**. `Run` exposes the
  lifecycle seam; the reconcile loops register into it when P-J lands. V1 `Run` blocks until `ctx` done.
- **The API server, scheduler, function lifecycle, services, eventing, CLI** — their own features
  (P-L/P-K/P-M/P-N…/P-R); the platform *builds* them internally as they land, wired with these ports.
- **The full `tests/e2e/**` lane** (importing only `pkg/**`+`api/**`) — needs pkg-level *operations*
  (deploy/invoke a function) that arrive with P-L/P-M/P-R; this ADR delivers the harness *foundation*
  (a booted, working platform) + the pkg/funcd-level round-trip proof. The full CI lane is **P-S / F20**.
- **Daemon config schema/loading** beyond a minimal `Production()` — config parsing detail is `cmd/funcd`
  + a later config concern; this ADR wires drivers, not a config-file format.

## Constraints & Decision drivers

- **C1 — library-first, single binary (blueprint)**: `pkg/funcd` is the embeddable platform; `cmd/funcd`
  is a thin shell. E2e tests embed the library with in-memory drivers — no daemon/root/network.
- **C2 — ADR-0002 facade shape (frozen)**: `func New(opts ...Option) (*Platform, error)` — **ctx-free**;
  functional options; `InMemory()`/`Production()` presets; required-dep validation → `fault.Invalid`;
  never a partial platform. So **IO-on-construction** (the in-memory NATS server start, blob open) happens
  inside the preset using `context.Background()` — the components live for the platform's lifetime and are
  released by `Shutdown`/`Close`, so a construction ctx is not the right lifetime handle.
- **C3 — no globals (ADR-0002 §5)**: everything is constructed and injected from this composition root;
  no package-level platform/driver singletons.
- **C4 — crash-only (blueprint)**: `Run` is restartable; on boot the platform rebuilds its world from the
  store + actual state and lets reconcile converge. V1 wires the seam (the reconcile body is P-J); `Run`/
  `Shutdown` own a clean, idempotent lifecycle.
- **C5 — the harness is the e2e backbone**: `InMemory()` must boot the **real** components (real memory
  store, real embedded NATS memory-storage, real gocloud mem bucket, real process runtime, real embedded
  gateway) — not fakes — so e2e tests exercise production code paths (the no-mocks discipline at the
  platform level).

## Alternatives considered

**Where ctx-dependent drivers are constructed** (driver: ADR-0002's ctx-free `New`):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Preset constructs all drivers (ctx-needing ones with `context.Background()`); `Run`/`Shutdown` own lifecycle** | keeps `New(opts...)` ctx-free per ADR-0002; one place builds the world; `Shutdown` releases | `InMemory()`/`Production()` do IO (start NATS) during `New` | **chosen** (honors the frozen facade shape) |
| Change `New` to `New(ctx, opts...)` | drivers get a real ctx | **contradicts ADR-0002's frozen facade signature** — would need to supersede it | rejected |
| Defer all driver construction to `Run(ctx)` via factories in the config | drivers get `Run`'s ctx | a factory indirection for every port; `New` can't validate a real dep, only a factory | rejected (more machinery; weaker `New` validation) |

**Harness placement**: the round-trip harness test lives in **`pkg/funcd`** (white-box), not `tests/e2e/**`
— because the ports are `internal/**` types the `tests/e2e` depguard boundary forbids; the full pkg-only
e2e lane arrives with pkg-level operations (P-L/P-M) and the F20 testing strategy. **Chosen**; putting raw
internal-typed accessors on the public `Platform` (so `tests/e2e` could drive them) is rejected — it would
leak internal types into the SDK surface.

**Required deps**: `store`, `blob`, `bus`, `runtime`, `gateway` are **required** (the platform cannot
function without them); `logger` defaults to a stdout text logger; `telemetry` defaults to the no-op
pipeline (ADR-0010). Requiring the five core ports (vs. making all optional) is chosen so a misconfigured
embedder fails fast at `New` with a precise `fault.Invalid`, never a half-built platform.

## Decision

### 1. `config` + `With*` options — the injected world
`config` holds `store.Store`, `blob.Bucket`, `bus.Bus`, `runtime.Runtime`, `gateway.Gateway`,
`*slog.Logger`, `*observability.Telemetry`. Each `With*` option injects one already-constructed dependency
(the embedder, or a preset, builds it). `New(opts...)` applies options, then `validate()` returns
`fault.Invalidf("funcd.New", "<dep> is required")` for the first missing required dep (store/blob/bus/
runtime/gateway); `logger` defaults to a stdout text `*slog.Logger`, `telemetry` to the no-op pipeline.
On success it returns a `*Platform` holding the config + the root logger + a `components` list (the
closers to run on `Shutdown`).

### 2. Presets construct + wire the real drivers
`InMemory()` constructs and injects: `store.New(memory.New())`, `gocloud.Open(context.Background(),
"mem://")`, `nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})`, `process.New()`,
`embedded.New()`, a text `observability.NewLogger` to `os.Stdout`, and a no-op `observability.NewTelemetry`.
`Production()` constructs the production drivers (file/S3 blob, file-storage NATS, slatedb store via its
build tag, containerd runtime, JSON logger, OTLP telemetry when configured) — the production wiring, with
the same shape. Both return a single `Option` that sets every port on the config. IO-on-construction
(NATS start) uses `context.Background()` (C2).

### 3. `Run`/`Shutdown` lifecycle (crash-only seam)
`Run(ctx)` starts the platform and **blocks until `ctx` is cancelled**, then returns nil (graceful) — in
V1 there are no long-running reconcile loops yet (P-J registers them into this seam later); `Run` logs the
boot, waits on `ctx.Done()`, and triggers `Shutdown`. `Shutdown(ctx)` drains a **registered closer list**
that the preset/`New` populates at construction: each driver that exposes a closer is registered (bus,
gateway, runtime, telemetry definitely; store/blob via an `interface{ Close() error }` type-assertion, and
simply skipped if their port interface has none — `Shutdown` does **not** assume every port declares
`Close`). It is **concurrency-safe and idempotent** — guarded by a `sync.Once` so `Run`'s shutdown-on-ctx
and a caller's explicit `Shutdown` cannot double-close or race — and best-effort (`errors.Join`, closing all
even if one fails).

### 4. `cmd/funcd` thin shell
`cmd/funcd/main.go` parses minimal config, builds `funcd.New(funcd.Production(), …)`, installs SIGINT/
SIGTERM → ctx cancel, and calls `Run(ctx)` — zero business logic (blueprint: "`cmd/funcd` only holds the
configuration of external components and driver selection").

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **`Run` only blocks on `ctx`** (no reconcile loops) | the controller is **P-J**; nothing to reconcile until it exists | **P-J / F08** registers its watch→diff→act→status loops into the `Run` lifecycle seam |
| **Presets construct ctx-needing drivers with `context.Background()`** | ADR-0002's `New` is ctx-free and frozen | a future config/lifecycle ADR may add a `Run`-time driver-open phase if a driver needs `Run`'s ctx/cancellation at open |
| **Harness round-trip tested in `pkg/funcd`, not `tests/e2e/**`** | ports are `internal/**` types; no pkg-level operations yet | **P-L/P-M** add pkg-level deploy/invoke ops; **P-S/F20** stands up the full `tests/e2e` + CI lanes |
| **`Production()` runtime/store use Linux/cgo drivers** (containerd, slatedb) | those are the real production drivers (Linux/cgo-gated) | `Production()` is exercised on the Linux lane (P-S); `InMemory()` is the cross-platform CI path |

## Contracts

### The facade (`pkg/funcd`)
```go
package funcd

import (
	"context"
	"log/slog"

	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/observability"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// Option configures the platform (functional options, ADR-0002).
type Option func(*config) error

func WithStore(s store.Store) Option
func WithBlob(b blob.Bucket) Option
func WithBus(b bus.Bus) Option
func WithRuntime(r runtime.Runtime) Option
func WithGateway(g gateway.Gateway) Option
func WithLogger(l *slog.Logger) Option
func WithTelemetry(t *observability.Telemetry) Option

// InMemory wires real in-memory drivers for every port (no root/containerd/network) —
// the e2e-harness preset. Production wires the production drivers.
func InMemory() Option
func Production() Option

// New assembles the platform from opts and validates required deps (store, blob,
// bus, runtime, gateway). Returns fault.Invalid (and a nil *Platform) if one is
// missing — never a partial platform, never a panic.
func New(opts ...Option) (*Platform, error)

// Platform is the assembled composition root.
type Platform struct { /* config + root logger + components, unexported */ }

// Run starts the platform and blocks until ctx is cancelled, then returns nil.
func (p *Platform) Run(ctx context.Context) error

// Shutdown closes the components; idempotent and best-effort.
func (p *Platform) Shutdown(ctx context.Context) error
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | the six ports + `api/fault` + `observability` (ADR-0006/07/08/09/11/13, ADR-0002/0010) | constructed + injected here |
| Adds (lib) | none | wires existing deps |
| Exposes | `pkg/funcd` (`New`/`With*`/`InMemory`/`Production`/`Run`/`Shutdown`) + the `InMemory()` harness | consumed by `cmd/funcd` and every e2e test |

## Implementation plan

1. **`pkg/funcd/options.go`** — add `WithStore`/`WithBlob`/`WithBus`/`WithRuntime`/`WithGateway`/
   `WithTelemetry` (keep `WithLogger`).
2. **`pkg/funcd/funcd.go`** — `config` with the seven fields + a `components []io.Closer`-style shutdown
   list; `validate()` (required: store/blob/bus/runtime/gateway → `fault.Invalid`); `New` (defaults
   logger→stdout text, telemetry→no-op; build `*Platform`); `Run` (log boot, block on `ctx.Done()`,
   `Shutdown`); `Shutdown` (close components, idempotent, `errors.Join`).
3. **`pkg/funcd/presets.go`** — `InMemory()` (construct + inject the real in-memory drivers; register
   closers) and `Production()` (production drivers).
4. **`cmd/funcd/main.go`** — thin shell: `Production()` + signal→ctx + `Run` (if not already present).
5. **`pkg/funcd/funcd_test.go`** — one named test per Scenario (white-box `package funcd` where it needs
   the unexported ports): `inmemory-boots`, `inmemory-ports-roundtrip` (store/blob/bus/gateway round-trip
   via the booted platform), `missing-required-dep` (`fault.Invalid` — this **fleshes out ADR-0002's
   existing `facade-missing-dep` skeleton**, not a duplicate), `run-shutdown-lifecycle` (Run returns on
   ctx cancel; concurrent `Shutdown` idempotent via `sync.Once` + bus closed).
6. **Definition of done**: `just ci` green; `New(InMemory())` boots with all ports wired; the four
   scenarios pass on real in-memory drivers (no root/containerd/network); `New()` → `fault.Invalid`;
   `Run`/`Shutdown` lifecycle is clean + idempotent; no new dependency; no globals.

## Review checklist

- [ ] `New(opts...)` is ctx-free (ADR-0002), validates required deps (store/blob/bus/runtime/gateway) →
      `fault.Invalid` naming the missing one, returns a nil `*Platform` on failure (never partial/panic).
- [ ] `InMemory()` constructs the **real** in-memory drivers (memory store, gocloud `mem://`, memory-storage
      NATS, process runtime, embedded gateway, text logger, no-op telemetry) — no fakes; ctx-needing ones
      use `context.Background()`.
- [ ] `inmemory-ports-roundtrip` drives store + blob + bus + gateway through the booted platform on real
      code paths (no root/containerd/network).
- [ ] `Run(ctx)` blocks until `ctx` cancelled then returns nil; `Shutdown` drains a **registered closer
      list** (store/blob via `interface{ Close() error }` assertion, skipped if absent), is **idempotent
      and concurrency-safe** (`sync.Once`), joins errors; the bus is closed after `Shutdown`.
- [ ] No globals; `slog` via the wired logger; ctx-first; no `any`; `cmd/funcd` holds no business logic.
- [ ] No new dependency; `go.mod` tidy; no identity/path leak; the controller-reconcile seam is documented
      as P-J (not implemented here).

## Consequences

- (+) funcd is now a **bootable, embeddable platform**: `New(InMemory()).Run(ctx)` stands up every port in
  one process with no root/containerd/network — the library-first promise, and the e2e-harness backbone
  every later feature tests against on real code paths.
- (+) **Required-dep validation** fails a misconfigured embedder fast and precisely; **no globals** — the
  whole world is one injected composition root.
- (+) `Run`/`Shutdown` give a clean, idempotent, crash-only lifecycle with the reconcile seam ready for P-J.
- (−) Presets do **IO on construction** (NATS start) to honor ADR-0002's ctx-free `New` — a small surprise,
  documented; the alternative was superseding the frozen facade signature.
- (−) The **full `tests/e2e` lane is deferred** to P-S (needs pkg-level operations from P-L/P-M) — V1 proves
  the harness in `pkg/funcd`.

## Open questions

| Question | Where it gets answered |
|---|---|
| The controller watch→diff→act→status loops registered into `Run` | **P-J / F08** |
| pkg-level operations (deploy/invoke a function) for the `tests/e2e` lane | **P-L/P-M** + the F20 testing strategy (P-S) |
| Daemon config schema/loading (file/env) feeding `Production()` | `cmd/funcd` + a later config concern |
| A `Run`-time driver-open phase if a driver needs `Run`'s ctx at open | a future lifecycle ADR if a driver requires it |

## References

- [blueprint.md](../../blueprint.md) — "Platform as a library" (`pkg/funcd`, `cmd/funcd` thin shell,
  `funcd.New(...).Run(ctx)`, e2e on `InMemory()`), "Single-binary process model", "Crash-only design".
- [ADR-0002](0002-source-code-conventions-and-patterns.md) — the facade shape + `facade-missing-dep`
  scenario this realizes.
- ADR-0006/0007/0008/0009/0011/0013 — the ports this composition root wires.
