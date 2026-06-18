# ADR-0013 Implementation Review — Gateway ingress slice (middleware seam + streaming) (model: claude-opus-4-8)

**Verdict**: **pass** — the ADR's own implementable slice (the `Middleware`/`Chain` seam + `Recover`/
`RequestID` + embedded-driver streaming) realizes the Contracts; zero Blockers/Majors. The deferred
pieces (certmagic TLS, default-selection, auth/LB middleware) are correctly sequenced to P-I/P-L/P-H2.
One non-blocking Minor (WebSocket passthrough is httputil-native, not separately tested).
**Reviewed against**: ADR-0013 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (§3/§5/§6) · FEAT-0000/F10.
**Date**: 2026-06-14

## Verification (evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./internal/gateway/...` | exit 0 |
| `go tool golangci-lint run ./...` | **0 issues** |
| `go test -count=1 -v ./internal/gateway/... ` | `middleware-chain-order` ✓ · `streaming-passthrough` ✓ · Recover ✓ · RequestID ✓ · embedded+lura contracts still ✓ |
| Tree | `middleware.go` + `middleware_test.go` + `embedded/streaming_test.go` + the `embedded.go` `FlushInterval` edit — exactly the slice |
| No new dep | `grep certmagic go.mod` = 0 (TLS correctly deferred) |
| Stubs/skips | none |
| Identity | clean |

## 🔴 Blockers / 🟡 Major
None.

## Minor
- **WebSocket-upgrade passthrough is asserted but not separately tested.** Evidence: Decision §3 /
  checklist item 2 claim native `Upgrade` passthrough; the test covers SSE/chunked streaming
  (`streaming-passthrough`) but not a WebSocket handshake. *Attribution: `model`.* Non-blocking —
  `httputil.ReverseProxy` handles `Upgrade` natively (Go 1.12+), so it works by construction; a WS
  round-trip test could be added when an MCP WebSocket transport is wired.

## ✅ Verified correct — keep it
- **`Chain` composes outermost-first and short-circuits**: `middleware-chain-order` proves `[a, b, handler]`
  order and that a 429-returning outer middleware skips the inner chain + handler — the exact ingress seam
  the composition root will use.
- **Streaming is real, proven without a flake**: `streaming-passthrough` blocks the upstream after chunk1
  and reads chunk1 through the gateway *before* releasing chunk2 (with a 3s fail-fast guard) — so a
  buffering regression fails fast instead of passing. `FlushInterval=-1` on the per-route `ReverseProxy`.
- **`Recover` → problem+json 500** (deliberate nil-map panic, `//nolint` documented) and **`RequestID`**
  (mint/echo/context) are concrete, dependency-free middlewares — `net/http`-only, `api/fault` for the
  500, struct context key (no global).
- **certmagic correctly deferred** (no dep added); Lura still passes the shared contract and is *not*
  asked to stream (exempt, per the ADR); ADR-0012 carries the `Superseded by` back-link and its code is
  untouched (not re-implemented).

## DoD
ADR Review-checklist: the **6** items hold for the implementable slice; `httputil-is-default` +
`tls-config-embeds` are explicitly deferred to P-I (recorded, not dropped). Scenarios: `middleware-chain-order`
+ `streaming-passthrough` named + passing; deferred scenarios traced.

## Recommendation
**pass** → stamp ADR-0013 `Reviewing → Implemented`; feat F10 stays `implemented` (the gateway port +
drivers + now the ingress seam). The remaining ingress build-out (certmagic/auth/LB) is sequenced to
P-I/P-L/P-H2, not a gap.
