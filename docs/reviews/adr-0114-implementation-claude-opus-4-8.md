# ADR-0114 implementation review — edge observability (F76) + shaping (F78)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0114 implementation, model: claude-opus-4-8)

The implementation realizes both families of additive, non-rejecting edge middleware exactly as the
ADR specifies: RED metrics + edge traceparent + access log (F76) and CORS + response headers + gzip
compression (F78), wired into the data-plane chain in the mandated order and proven — including the
judge's B1/M1/M2/M3 folds — by named, un-skipped, passing scenario tests. All four sub-checks are
green.

### Verification (captured exit codes)

| Check | Command | Exit |
|---|---|---|
| build | `go build ./...` | **0** |
| lint | `go tool golangci-lint run ./internal/edge/... ./internal/gateway/... ./pkg/funcd/ ./internal/platform/... ./internal/dataplane/...` → `0 issues.` | **0** |
| test | `go test ./internal/edge/... ./internal/gateway/... ./pkg/funcd/ ./internal/platform/config/... ./internal/dataplane/... -count=1` | **0** |
| mod | `go mod verify` → `all modules verified` | **0** |

(`just ci` is not run whole — its `git diff` gate fails on the intentionally-uncommitted batch tree,
a known project condition; the four sub-checks are the equivalent evidence and are all green.)

### 🔴 Blocker / 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Contracts match exactly.** `observ.Config{Metrics,AccessLog,Trace}`, `observ.Chain(cfg, *observability.Telemetry, *slog.Logger)`,
  `Target{Namespace,Function}` + `WithTarget`/`TargetFrom`, and `shape.{CORS,Headers,Config}` + `shape.Chain(cfg)`
  are byte-for-byte the ADR Contracts. No `any`/`interface{}` in any exported signature (the lone
  `any` grep hit is a doc comment in telemetry.go). Zero `Config` ⇒ pass-through in both packages
  (`TestScenarioDisabledPassthrough` in each).
- **Chain order (checklist #2).** `pkg/funcd/funcd.go` wires
  `gateway.Chain(dataplane.Handler(...), gateway.Recover, gateway.RequestID, observ.Chain(...), limit.Chain(...), shape.Chain(...))`
  → runtime `Recover → RequestID → observ → limit → shape → handler`: observ outer-than-limit (times
  rejects), inner-than-RequestID (reads the id), shape innermost.
- **B1 + M1 — streaming/WS through the REAL chain.** Both writer wrappers forward `http.Flusher` +
  `http.Hijacker`: observ's `recorder` (observ.go:156-167) and shape's `gzipWriter`/`headerWriter`
  (shape.go:121-122,185-191). Proven on the assembled `observ+limit+shape` chain over a live
  `httptest.NewServer` by `TestScenarioE2EFullEdgeChainStreaming` — `sse-flushes-unbuffered` reads the
  first `data: tick` frame before the handler returns, and `ws-upgrade-roundtrips` dials a real
  WebSocket upgrade and round-trips `echo:hi` end to end. Unit guards (`TestRecorderForwardsFlusherAndHijacker`,
  `TestShapingForwardsFlusherAndHijacker`) back this at the package level.
- **M2 — traceparent valid under no-op telemetry.** `injectTraceparent` mints `trace-id`/`edge-span-id`
  with `crypto/rand` independent of the OTel tracer (observ.go:112-130). `TestScenarioEdgeSpanInjectsTraceparent`
  runs with `nil` telemetry and asserts a well-formed non-zero `00-<32hex>-<16hex>-01`;
  `TestScenarioEdgeSpanAdoptsInbound` confirms a valid inbound trace-id is adopted (trace continues).
- **M3 — function label from the dataplane-filled holder.** observ seeds a `Target` holder on the way
  in; `dataplane.serveFunction` fills it with the resolved `(namespace, function)` after route/name
  resolution (dataplane.go). `TestScenarioMetricsRecorded` asserts the `funcd.edge.requests` counter +
  `funcd.edge.duration_ms` histogram carry `function=orders` from the holder, and the on-chain e2e
  re-asserts it through the full stack (`hasFunctionLabel(rm,"orders")`). No-op telemetry ⇒ no dials
  (metrics instruments only built when `cfg.Metrics && telemetry != nil`).
- **Access log (checklist #6).** One structured slog line per request with `method`, `path`,
  `function`, `namespace`, `status`, `duration_ms`, and `request_id` from `gateway.RequestIDFromContext`;
  `TestScenarioAccessLogCorrelated` wires observ under `gateway.RequestID` (as funcd does) and asserts
  the correlated id, status, function, and duration.
- **CORS / headers / gzip.** Preflight short-circuits `204` + `Access-Control-Allow-*` without waking
  the upstream (`TestScenarioCorsPreflight`, and e2e `TestScenarioE2EEdgeCorsPreflight` through a real
  `funcd.Run()`); actual request gets ACAO + `Vary: Origin`, wildcard echoes the origin; headers
  set/strip applied; gzip engages on `Accept-Encoding: gzip` and **skips** `text/event-stream` +
  upgrades + already-encoded bodies (`TestScenarioCompressionGzip`, `TestScenarioCompressionSkipsStreaming`).
- **ws-passthrough conformance (checklist #7).** `gatewaycontract.RunContract` gains a `ws-passthrough`
  case (live `x/net/websocket` echo upstream dialed through the embedded gateway proxy) — passes via
  `TestEmbeddedDriverContract/ws-passthrough`. The WS client is test-only; no new module dependency
  (`golang.org/x/net/websocket` already in the graph; `go mod verify` clean).
- **Tree vs ADR Repository surface.** All planned files present and nothing unexplained-extra:
  `internal/edge/observ/{observ,observ_test}.go`, `internal/edge/shape/{shape,shape_test}.go`, the
  `gatewaycontract` ws case, `funcd.go`/`options.go` (`WithEdgeObservability`/`WithEdgeShaping`),
  `config.go` (`server.observability` + `server.shaping`), `cmd/funcd/main.go` config→option wiring,
  the `NewFromProviders` telemetry seam, and the `pkg/funcd` on-chain e2e.
- **Conventions (ADR-0002).** slog-only, ctx-first, no `panic`/`fmt.Print*` outside main, one-file
  drivers, functional options on the facade. Package docs present on both new packages.
- **Venom lane coherence (env-deferred).** `e2e/env-echo.venom.yml` F78 testcase asserts
  `access-control-allow-origin: https://demo.example` on an OPTIONS preflight and `content-encoding: gzip`
  on a gzip-accepting invoke; `examples/js/env-echo/funcdconfig.yaml` enables `server.observability`
  (metrics+accessLog) and `server.shaping` (cors allowOrigins `https://demo.example`, allowMethods,
  compression). Config keys match `internal/platform/config/config.go`; curl assertions match
  `shape.go` behavior. The containerd VM is not bootable in this dev environment (**env**, not a model
  defect) — the Go on-chain e2e is the in-gate streaming/WS/CORS/gzip coverage and passes.

### Definition of Done

7/7 ADR Review-checklist items hold (Contracts+pass-through; chain order; both wrappers forward
Flusher/Hijacker + on-chain SSE/WS; RED metrics with holder label + no-op-safe; crypto/rand traceparent
mint/adopt/inject; access log correlated; ws-passthrough + no new dep + rows advanced). The generic
implement-gate DoD holds (build/test/lint/mod green, every scenario un-skipped and passing, real
behavior with no stubs, tree matches surface, no scope creep, no identity/path leak, feat rows at
`reviewing`, ADR at `Reviewing`). The one caveat — the Venom containerd lane cannot boot here — is
**env**-attributed; the YAML is coherent and the equivalent behavior is covered by the passing Go e2e.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0114 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Stamp ADR-0114 `Reviewing → Implemented` and advance the F76 + F78 feat rows to
`implemented`; move the Project #4 card to Done. Nothing loops back to the builder. The only
non-green surface — the containerd Venom lane — is environmental and its behavior is already proven
on-chain by the Go e2e; run it opportunistically when a Lima/colima VM is available.
