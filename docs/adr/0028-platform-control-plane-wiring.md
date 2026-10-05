# ADR-0028: Platform control-plane wiring — make `funcd.Run` serve + reconcile

- **Status**: Implemented
- **Superseded in part by**: [ADR-0163](0163-retry-times-in-config.md) (2026-10-05) — Open question graceful-shutdown timeout value: now server.shutdownTimeout.
- **Superseded in part by**: [ADR-0171](0171-static-credential-list.md) (2026-10-05) — Scope Out/Consequences/Open question: multi-role tokens moved from V2 to V1.
- **Date**: 2026-06-15 (**Implemented 2026-06-15** · **Accepted 2026-06-15** after judge pass — no Blockers. The judge verified every wired
  constructor against its real signature, confirmed the bind-at-`New` decision + the honest data-plane
  deferral, and that `run-reconciles-function-to-ready` is testable in pure-Go CI (the process runtime's
  `sleep` sandbox reaches `Ready`). Folded three mechanical **Majors**: M1 — every component constructor is
  error-returning, so `New` checks each and returns the first as a typed `fault` + nil Platform; M2 —
  specified `Production()`'s `listenAddr`/`authorizer`/`localNode` defaults + the credentials-required
  validation; M3 — named the minimal valid Function (namespace=default + resourceGroup + runtime/handler/
  artifact.uri). Minors: `Run` swallows `http.ErrServerClosed` (so it returns nil on graceful stop) + waits
  on a `WaitGroup` for the loops to drain before closing ports; a bounded eventing HTTP client. Decision
  unchanged: compose the built controller + Function/Service/EventSource reconcilers + control-plane server
  into `Run`; `Addr()`/`WithDevAuth`/`DevToken`. No new dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: facade, composition-root, control-plane, controller, lifecycle, run, embed
- **Realizes**: [FEAT-0000/F04](../feat/0000-feat-v1.md) (platform facade & lifecycle — the composition root that runs the platform)
- **Relates to**: [ADR-0014](0014-platform-facade-lifecycle-harness.md) (the `pkg/funcd` facade whose `Run`
  was a documented "later seam" — this ADR fills it), [ADR-0015](0015-controller-engine.md) (the engine +
  `Register`/`Run` this wires), [ADR-0018](0018-api-server-authn-rbac-admission.md) (the control-plane
  `NewServer` this mounts), [ADR-0020](0020-function-contract-lifecycle.md) (the Function reconciler +
  `Endpoints()` provider), [ADR-0019](0019-service-facade-pattern-kv.md) (the Service dispatcher),
  [ADR-0023](0023-eventing-core.md) (the EventSource reconciler + its `Run` loop),
  [ADR-0017](0017-scheduler-placement-port.md) (the scheduler the Function reconciler needs)

## Context & Need

ADR-0014 built `pkg/funcd` as the composition root, but its `Run` is a **no-op** — it blocks on
`<-ctx.Done()` with the comment *"V1 has no long-running reconcile loops yet (the controller, P-J, registers
them into this seam later)."* So `funcd.New(funcd.InMemory()).Run(ctx)` today wires the five ports but
**serves nothing and reconciles nothing**: a downloaded `funcd` binary boots and idles. Every architectural
piece exists — the controller engine (ADR-0015), the Function/Service/EventSource reconcilers
(ADR-0020/0019/0023), the authenticated control-plane server (ADR-0018) — but nothing **starts** them.

This is the keystone gap to a live platform and to the **full embed e2e** ADR-0025's L3b harness is shaped
to consume: until `Run` serves the API + runs the controller, the SDK/CLI can't drive a running `funcd`, and
an applied resource never reconciles. This ADR fills ADR-0014's seam — one topic at one altitude: *"compose
the already-built control-plane components into `Run` so the platform serves the API and reconciles applied
resources, with a clean start/stop lifecycle."* It is on the V1 critical path (`ADR-0014 → P-U → …`).

## Scope

- **In**: build + start, in `pkg/funcd`, the **controller** (ADR-0015) with the **Function / Service /
  EventSource** reconcilers registered; mount the **control-plane HTTP server** (ADR-0018) on a listener;
  start the **eventing `Run`** timer loop; a clean `Run` (start → block on ctx → graceful shutdown) and an
  `Addr()` accessor; the config + options to support it (`WithListenAddr`, `WithDevAuth`, `WithAuthorizer`);
  `InMemory()` defaults (ephemeral addr, a dev token, RBAC, single-node scheduler, basic validator); the
  **embed e2e** (apply→reconcile→Ready through the public SDK).
- **Out**: the **data-plane** gateway listener serving function invocations over HTTP (the route→activator→
  sandbox path) — **P-X** (it needs real function execution); real function execution / curated runtime
  images — **P-V**; secret injection into the sandbox — **P-W**; TLS/certmagic; multi-node (controlplane.proto
  registration); a richer auth surface (multiple roles, token issuance — `WithDevAuth` is the V1 developer
  credential, the rest is V2).

## Constraints & Decision drivers

- **`cmd/funcd` stays a thin shell** (ADR-0014) — all wiring lives in `pkg/funcd`; `cmd/funcd` keeps just
  preset + driver selection (+ the ADR-0026 `version` shell).
- **Crash-only lifecycle** (blueprint) — `Run` starts the loops + server, blocks on ctx, and on cancel does a
  bounded graceful shutdown (stop the HTTP server, let the ctx-bound loops drain, then close the ports). No
  panics; errors are `api/fault`/joined.
- **Embed-testable on the public surface** — the e2e (`tests/e2e`, depguard-bounded to `pkg/**`+`api/**`)
  must drive a running platform through `pkg/sdk`; so `Addr()` exposes the bound address and `WithDevAuth`
  lets the test supply a token **without** importing `internal/auth` (whose `Identity` is internal).
- **Compose, don't re-decide** — every component (controller, reconcilers, server, scheduler, validator) is
  already built + accepted; this ADR only constructs + starts them. No new architecture, no new dependency.

## Scenarios

- **scenario: addr-available-after-new** — *Given* `funcd.New(funcd.InMemory())`, *when* `Addr()` is read
  (before `Run`), *then* it returns the non-empty bound control-plane address (the listener is bound at `New`).
- **scenario: run-serves-control-plane** — *Given* a platform `Run`-ing in the background, *when* the public
  SDK (authenticated with the dev token) `Apply`s and `Get`s a resource against `Addr()`, *then* it
  round-trips (the API is served).
- **scenario: run-reconciles-function-to-ready** — *Given* a running platform, *when* the SDK `Apply`s a
  **valid** `Function` (the shape gate + admission require `metadata.namespace="default"` (matching the dev
  credential) + `metadata.resourceGroup` non-empty + `spec.runtime` + `spec.handler` + `spec.artifact.uri`
  all set; no pre-existing Namespace/ResourceGroup parent is needed — admission only calls `obj.Validate()`),
  *then* polling its status converges to `Phase==Ready` (the Function reconciler provisions a `sleep` sandbox
  replica via the process runtime and marks Ready when ≥1 is Running) within a bound — proving the controller
  + reconcilers are running.
- **scenario: run-shutdown-graceful** — *Given* a running platform, *when* its context is cancelled, *then*
  `Run` returns nil, the server stops accepting, and a second `Shutdown` is idempotent (no panic/double-close).

## Decision

Wire the control plane in `pkg/funcd`: `New` constructs every component and **binds** the listener (so
`Addr()` is ready); `Run` starts the loops + server, blocks on ctx, and shuts down gracefully.

### 1. Config + options (`pkg/funcd/options.go`, `presets.go`)
```go
// added config fields: listenAddr string; credentials middleware.CredentialStore;
// authorizer auth.Authorizer; localNode v1.ObjectName

func WithListenAddr(addr string) Option   // control-plane bind addr; default "127.0.0.1:0" (ephemeral)
func WithAuthorizer(a auth.Authorizer) Option // default rbac.New()
// WithDevAuth wires a single developer-role credential (V1 dev/test convenience; not a
// production identity story — that's V2). It maps token → Identity{developer, namespaces}
// WITHOUT exposing internal/auth, so a public-surface e2e can authenticate.
func WithDevAuth(token string, namespaces ...string) Option
```
`InMemory()` defaults: `WithListenAddr("127.0.0.1:0")`, `WithDevAuth(DevToken, "default")`, `rbac.New()`,
`singlenode` scheduler (`localNode="local"`), `function.NewBasicValidator()`. `Production()` defaults
`listenAddr="0.0.0.0:8080"`, `authorizer=rbac.New()`, `localNode="local"`, and the basic validator — but
deliberately sets **no** credentials (no default token in production): `config.validate` requires
`credentials != nil`, so `New` returns `fault.Invalid` unless the operator supplies `WithDevAuth`/credentials.
(`InMemory` always has credentials via its `WithDevAuth` default, so the existing `New(InMemory())` tests are
unaffected; `New()` with no preset still fails on the *store* check first, as today.)

`DevToken` is an exported const (the InMemory default token) so tests/tools can authenticate against an
`InMemory()` platform without configuring auth.

### 2. `New` builds the control plane + binds the listener (`pkg/funcd/funcd.go`)
After the existing port validation, `New` additionally:
1. builds the **scheduler** (`singlenode.New(localNode)`) and **validator** (`function.NewBasicValidator()`);
2. builds the **Function reconciler** (`function.NewReconciler(Deps{store, runtime, scheduler, gateway,
   validator, logger})`);
3. builds the **Service dispatcher** (`services.NewDispatcher(store, logger, kv.NewHandler(),
   blob.NewHandler())`);
4. builds the **EventSource source** (`eventing.NewSource(Deps{store, Endpoints: fnReconciler.Endpoints(),
   logger, HTTPClient: &http.Client{Timeout: 30s}})` — a bounded client, not the timeout-less `http.DefaultClient`,
   so a hung upstream can't block a dispatch goroutine);
5. builds the **controller** (`controller.New(Deps{store, logger, workers})`) and **registers** the three:
   `Register(KindFunction.GVK(), fnReconciler)`, `Register(KindService.GVK(), dispatcher)`,
   `Register(KindEventSource.GVK(), source)`;
6. builds the **control-plane handler** (`controlplane.NewServer(Deps{store, authorizer, credentials,
   logger})`) and an `&http.Server{Handler: …}`;
7. binds `net.Listen("tcp", listenAddr)` and stores the listener + its `Addr().String()` on the Platform.

**Every wired constructor is error-returning** — `singlenode.New(local) (Scheduler, error)`,
`function.NewReconciler(Deps) (*Reconciler, error)`, `services.NewDispatcher(...) (*Dispatcher, error)`,
`eventing.NewSource(Deps) (*Source, error)`, `controller.New(Deps) (*Controller, error)`,
`controlplane.NewServer(Deps) (http.Handler, error)` — so `New` checks each and returns the first failure
wrapped as a typed `fault` + nil Platform (never a partial platform, never a panic). The controller uses the
engine's worker default. Validation failures (a missing credential in `Production`, a bind error) likewise
return a typed `fault` + nil Platform.

### 3. `Run` starts + blocks + graceful shutdown
```go
func (p *Platform) Run(ctx context.Context) error
// starts (goroutines, tracked by a sync.WaitGroup): p.controller.Run(ctx); p.eventing.Run(ctx);
//   p.httpServer.Serve(p.listener)
// blocks: <-ctx.Done()
// graceful: p.httpServer.Shutdown(stopCtx) (stop accepting); wg.Wait() (let the ctx-bound loops drain);
//           then p.Shutdown(stopCtx) (closes the five ports + telemetry + the listener)
```
`Run` treats **both** `context.Canceled` (the loops) **and** `http.ErrServerClosed` (`Serve` always returns
it after `Shutdown`) as a *clean* stop — neither is an error — so `Run` returns nil on a graceful cancel.
It **waits for the controller/eventing goroutines to drain** (a `sync.WaitGroup`) **before** `p.Shutdown`
closes the ports, so a late in-flight reconcile can't hit a closed store. `Run` returns the joined shutdown
error (nil on a clean stop). `Shutdown` stays `sync.Once`-guarded + idempotent and now also closes the bound
listener.

### 4. `Addr()` accessor
```go
func (p *Platform) Addr() string  // the bound control-plane address (host:port), set at New
```

## Contracts

### `pkg/funcd` (`funcd.go`, `options.go`, `presets.go`)
```go
const DevToken = "funcd-dev-token"   // the InMemory() default credential token

func WithListenAddr(addr string) Option
func WithAuthorizer(a auth.Authorizer) Option
func WithDevAuth(token string, namespaces ...string) Option

func New(opts ...Option) (*Platform, error)   // now also builds the control plane + binds the listener
func (p *Platform) Run(ctx context.Context) error      // serves + reconciles; graceful shutdown on ctx
func (p *Platform) Shutdown(ctx context.Context) error // + closes the listener (idempotent)
func (p *Platform) Addr() string                        // bound control-plane addr
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/controller`, `internal/function`, `internal/services`(+`kv`,`blob`), `internal/eventing`, `internal/controlplane`(+`middleware`), `internal/auth`(+`rbac`), `internal/scheduler/singlenode`, stdlib `net`/`net/http` | all already-built; `pkg/**`→`internal/**` is allowed |
| Adds (lib) | none | composition only |
| Exposes | `Run` (serves+reconciles), `Addr()`, `DevToken`, `WithListenAddr`/`WithAuthorizer`/`WithDevAuth` | the embed e2e drives it via `pkg/sdk` |

## Implementation plan

1. **`pkg/funcd/options.go`** — `WithListenAddr`, `WithAuthorizer`, `WithDevAuth` (builds a one-token
   `middleware.NewStaticCredentials` developer `Identity`); config fields.
2. **`pkg/funcd/presets.go`** — `InMemory()` adds the control-plane defaults (ephemeral addr, `WithDevAuth(DevToken,"default")`,
   `rbac.New()`, scheduler/validator); `Production()` leaves credentials unset (operator-provided).
3. **`pkg/funcd/funcd.go`** — extend `config.validate` (Production needs credentials); `New` builds the
   components + binds the listener; `DevToken` const; `Run` start/block/graceful-shutdown; `Shutdown` closes
   the listener; `Addr()`.
4. **`tests/e2e/controlplane_test.go`** (package `e2e_test`, pkg+api only) — the four scenarios via
   `funcd` + `pkg/sdk`: `addr-available-after-new`, `run-serves-control-plane`,
   `run-reconciles-function-to-ready`, `run-shutdown-graceful`.
5. **Verify**: four sub-checks green; the e2e drives a running platform end-to-end (apply→reconcile→Ready)
   through the public SDK with `DevToken`; `tests/e2e` imports no `internal/` (depguard).
6. **Definition of done**: `just ci` green; `funcd.New(InMemory()).Run` serves the API + reconciles a
   Function to Ready, drivable by the SDK; graceful shutdown; no new dependency; no globals beyond the
   sanctioned; no identity/path leak.

## Review checklist

- [ ] **Run serves + reconciles** (`run-serves-control-plane`, `run-reconciles-function-to-ready`): the SDK
      round-trips a resource and a `Function` reaches `Ready` against a backgrounded `Run` — the controller +
      Function/Service/EventSource reconcilers are registered and running.
- [ ] **Addr + auth on the public surface** (`addr-available-after-new`): `Addr()` returns the bound addr
      after `New`; `WithDevAuth`/`DevToken` let the e2e authenticate without importing `internal/auth`.
- [ ] **Graceful, crash-only shutdown** (`run-shutdown-graceful`): `Run` returns nil on ctx cancel, the
      server stops accepting, `Shutdown` is idempotent and closes the listener; no panic.
- [ ] `cmd/funcd` stays a thin shell; the wiring is `pkg/funcd`-only; **no new dependency**; deferrals
      (data-plane serving=P-X, real execution=P-V, secret injection=P-W) recorded; no leak; every Scenario a
      named passing test (`tests/e2e` imports only `pkg/**`+`api/**`).

## Consequences

- (+) **A downloaded `funcd` actually runs**: `Run` serves the authenticated REST API and reconciles applied
  resources (Function→Ready, Service, EventSource) in one process — the platform is live and SDK/CLI-drivable.
  The critical-path keystone `ADR-0014 → P-U` is done; the **full embed e2e** ADR-0025 shaped is now real.
- (+) **Compose-only, no new dependency** — every piece was already built; `Run` just constructs + starts
  them with a clean lifecycle, and `cmd/funcd` stays a thin shell.
- (+) **`InMemory()` is a complete embeddable platform** (ephemeral addr + dev auth + the reconcile loops) —
  the "platform as a library, zero infra" promise, now serving.
- (−) **No data-plane invocation yet**: applied functions reconcile to Ready and routes are programmed, but
  serving an invocation over HTTP (route→activator→sandbox) and **real** function execution are **P-X**/**P-V**;
  `Run` mounts the control plane, not the data-plane listener. Honest: the control loop runs; the physical
  invoke needs the runtime lane.
- (−) **Dev-grade auth** (`WithDevAuth`, a single developer token) — sufficient for V1's "drivable via API/CLI"
  + the e2e; multi-role/issuance is V2.
- (note) **Roadmap**: P-U's edges are `ADR-0014` (facade), `ADR-0015` (engine), `ADR-0018` (server),
  `ADR-0020` (function), `ADR-0023` (eventing) — all built. It unblocks P-V/P-W/P-X/P-Z. Step-6 records this.

## Temporary workarounds

None. The wiring is the permanent shape; the deferrals (data-plane serving, real execution, secret injection)
are bounded follow-ups with their own items (P-X/P-V/P-W), not stopgaps inside this ADR.

## Alternatives considered

- **Build the components lazily in `Run` (not `New`)** — keeps `New` pure (no socket bind). Rejected: the
  embed e2e needs `Addr()` *before* `Run` (which blocks), so the listener must bind at `New`; building the
  rest there too gives fail-fast validation + a ready address. The leak risk (bind without `Run`) is closed by
  `Shutdown` closing the listener.
- **Mount the gateway data-plane listener here too** (`gateway.Handler()` on a second port) — tempting (it's
  one `http.Server`), but serving a route proxies to the function's sandbox upstream, which on `InMemory` is a
  `sleep` process with no HTTP server → a connection error, not a working invoke. Real invocation needs the
  curated runtime shim (P-V) + the activator-as-upstream wiring (P-X), so the data plane is honestly deferred.
- **A full auth/credential subsystem (roles, multiple tokens, issuance) in this ADR** — rejected as scope:
  ADR-0018 already decided the authn mechanism; P-U only needs to *supply* a credential to run + be driven.
  `WithDevAuth` is the minimal V1 surface; richer credential management is a V2 decision.
- **Expose the ports (`Store()` etc.) on `Platform` instead of mounting the server** — rejected: the public
  contract is the REST API + SDK (the exit criterion is "all via API/CLI"), not direct port access; mounting
  the server is what makes the platform drivable the way the exit criterion specifies.

## Open questions

| Question | Where it gets answered |
|---|---|
| Data-plane gateway listener (serving invocations) + activator-as-route-upstream | P-X (needs real execution P-V) |
| Production credential management (roles, issuance, rotation) | V2 (`WithDevAuth` is the V1 developer credential) |
| Per-reconciler worker tuning / backpressure | a controller follow-up if load demands it (the engine already rate-limits) |
| Graceful-shutdown timeout value | a config knob if needed; V1 uses a sane fixed bound |

## References

- [ADR-0014](0014-platform-facade-lifecycle-harness.md) — the facade + the `Run` "later seam" this fills.
- [ADR-0015](0015-controller-engine.md)/[ADR-0020](0020-function-contract-lifecycle.md)/[ADR-0019](0019-service-facade-pattern-kv.md)/[ADR-0023](0023-eventing-core.md) — the engine + reconcilers wired.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) — the control-plane server mounted.
- [ADR-0025](0025-testing-strategy-and-e2e-harness.md) — the L3b embed harness this makes a full deploy→reconcile walk.
