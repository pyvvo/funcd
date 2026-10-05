# ADR-0181: The edge forwards a bodiless upgrade request untouched, and an idle tunnel closes

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: edge, data-plane, websocket, invocation, cloudevent, size-cap, idle-timeout
- **Realizes**: [FEAT-0006/F78](../feat/0006-feat-ingress-hardening.md) (the WebSocket passthrough; the row ADR-0114
  realizes, joined as a second entry); relates to [FEAT-0001/F99](../feat/0001-feat-v1.1.md) (ADR-0134's row)
- **Supersedes (in part)**: [ADR-0134](0134-gateway-normalized-invoke-body.md) (Implemented), four clauses only,
  each gaining one exception, a bodiless upgrade request: Decision lines 113–114 (every external body is normalized
  into a CloudEvents envelope), Contracts *Wiring* lines 176–177 (a `GET`/no-body normalizes to `data:null`), the
  Definition of done's "`serveFunction` normalizes external invokes" (lines 211-212) and the Review checklist item
  "Empty/absent body → `data:null`" (line 220). Everything else stands, including `normalizeInvokeBody` and the
  `maxNormalizeBytes` bound. The `Superseded in part by ADR-0181` back-link on ADR-0134 is added at acceptance (the
  one edit it allows).
- **Refines** `blueprint.md` at acceptance: the Ingress controller / API Gateway entry (line 225) gains, after
  "dynamic route programming as a map swap.", the sentence "An external upgrade request with no body is forwarded
  without the ADR-0134 invoke envelope, and the activator closes any upgrade tunnel it proxies that carries no byte in
  either direction for 5 minutes ([ADR-0181](docs/adr/0181-edge-forwards-bodiless-upgrades.md))."
- **Relates to**: [ADR-0013](0013-gateway-ingress-httputil-primary.md) §3 (conforms) ·
  [ADR-0114](0114-edge-observability-shaping.md) (conforms; adds the real-handler case) ·
  [ADR-0148](0148-size-caps-answer-413.md), [ADR-0151](0151-external-invoke-deadline.md) (unchanged) ·
  [ADR-0171](0171-static-credential-list.md) Decision 5, [ADR-0164](0164-rate-limit-per-target.md) (still apply) ·
  [ADR-0030](0030-function-execution-runtime-shim-node.md) (WebSocket from the handler stays out of the shim)

## Context & Need

The external branch of `serveFunction` normalizes every request into an ADR-0134 envelope, an upgrade GET included:
it reads the body (`internal/dataplane/dataplane.go:194`), wraps it (`dataplane.go:204`; an empty body becomes
`data:null` at `internal/dataplane/normalize.go:41-43`) and sets `Body`, `ContentLength` and `Content-Length`
(`dataplane.go:205-207`). `httputil.ReverseProxy` forwards that 120-byte body with the upgrade GET; after the 101 the
worker reads the bytes as WebSocket frames and the connection closes with EOF. The internal path skips
normalization and works. Issue #727 reproduces both on `dataplane.Handler`.

This contradicts ADR-0013 §3, lines 151–153 (WebSocket through httputil's native `Upgrade`) and FEAT-0006 line 262
(ws-passthrough "proven"). The ADR-0114 case (scenario `ws-passthrough`, line 58; note M1, line 9, asked for the
real chain) missed it: its terminal "stands in for dataplane.Handler" (`pkg/funcd/edge_shaping_e2e_test.go:86-87`).

Once a tunnel is open, nothing closes an idle one: `Activator.forward`
(`internal/activator/activator.go:408-435`) hands the 101 to `httputil.ReverseProxy`, which tunnels until one side
closes. Purpose: an external caller that opens a WebSocket (or any HTTP/1.1 upgrade) to a Function gets
the tunnel an internal caller gets, every external body stays bounded by `maxNormalizeBytes`, and no tunnel stays
open without traffic for longer than the idle timeout. Impact today is low (the pinned shims serve only POST).

## Scenarios

- **scenario: external-websocket-roundtrips** (issue #727) — *Given* a warm Function `f` with a WebSocket echo
  upstream, *when* an external client dials `/function/f` and sends `hi`, *then* it receives `echo:hi`, and the
  upstream saw the upgrade GET with `Content-Length` 0.
- **scenario: upgrade-tunnel-outlives-deadline** — *Given* `invoke.defaultTimeout` 300 ms, *when* the client sends
  a frame 700 ms after the 101, *then* it receives the echo.
- **scenario: upgrade-with-body-still-capped** — *Given* no edge body cap, *when* an external client POSTs 3 MiB with
  `Connection: Upgrade` and `Upgrade: x`, sized and chunked, *then* each gets 413 `payload-too-large` and the
  upstream receives nothing.
- **scenario: bodiless-get-still-normalized** — *When* an external client sends a GET with no body and no upgrade
  headers, *then* the upstream receives an envelope whose `data` is `null` (ADR-0134).
- **scenario: edge-chain-websocket-real-handler** — *Given* the real `pkg/funcd` edge chain (Recover, RequestID,
  observ, limit, shape) in front of the real `dataplane.Handler`, *when* a client dials `/function/ws` and sends
  `hi`, *then* it receives `echo:hi`.
- **scenario: idle-tunnel-closed** — *Given* the activator's transport with a 300 ms idle timeout, *when* no byte
  flows either way, *then* funcd closes the tunnel 300 ms to 1.3 s after the last byte: both ends' pending reads fail.
- **scenario: active-tunnel-stays-open** — *When* the client sends a frame every 100 ms for 1.5 s, *then* the
  tunnel is open at 1.5 s and every echo matched.
- **scenario: one-direction-is-traffic** — *When* for 1.5 s only the client writes on one tunnel (the upstream
  replies to none of it) and only the upstream writes on another, *then* both are open at 1.5 s: a final `probe:`
  frame on each gets its echo.

## Scope

- **In**: the normalization condition in `serveFunction`; one unexported predicate; a fixed idle timeout on every
  tunnel `Activator.forward` proxies; one shared test upstream; the tests above, including the `pkg/funcd`
  WebSocket subtest on the real `dataplane.Handler`; a soak test outside CI.
- **Out**: a WebSocket-capable runtime or shim (ADR-0030); HTTP/2 extended CONNECT (RFC 8441), which
  `httputil.ReverseProxy` does not proxy; the `pkg/funcd` SSE and normal subtests (stay on the stand-in); the
  ADR-0114 `denyUpgrade` opt-out (deferred); a tunnel lifetime or byte limit; the ADR-0138 node-private `Upstream`
  backend (`dataplane.go:286`), which runs neither `serveFunction` nor the activator.

## Constraints & Decision drivers

- Every external body stays bounded: `maxNormalizeBytes` is the independent ceiling because the edge cap
  (`internal/edge/limit/limit.go:73-80`, `MaxBodyBytes`) may be 0 (ADR-0134 lines 168–172; ADR-0148 line 161).
- The edge and the proxy must agree on what an upgrade is, so the edge reuses `httputil.ReverseProxy`'s rule.
- No new config key, dependency, exported signature or shim change. `golang.org/x/net` v0.56.0 is a direct
  dependency (`go.mod:59`); `httpguts` is already imported at `internal/platform/config/config.go:26`.
- One idle rule at one site both external and internal tunnels cross; only silence in both directions ends one.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A, narrowed: skip normalization for a bodiless upgrade** (the ReverseProxy upgrade rule and `ContentLength == 0`) | covers every RFC 6455 handshake (a bodiless GET); keeps the ADR-0134 GET rule and the ADR-0148 413; one condition | an upgrade request that carries a body still gets the envelope (no RFC 6455 handshake has one) | **chosen** |
| A unnarrowed: skip for any upgrade, forward a body unchanged | same tunnel fix | measured bypass: a 3 MiB POST with `Connection: Upgrade` and `Upgrade: x` got 200 with all bytes delivered (413 on main), so any caller skips `maxNormalizeBytes` when the edge cap is off | rejected: breaks ADR-0134's bound and ADR-0148's 413 row |
| B: skip normalization for every GET and HEAD | also drops the envelope from SSE GETs | reverses the explicit ADR-0134 GET rule; a GET that carries a body streams with no bound (the same hole as above) | rejected: broader change and the same bypass |
| C: reject an external upgrade with a 501 until a WebSocket runtime exists | smallest code change | removes a capability ADR-0013 §3 requires and ADR-0114 F78 declares proven; supersedes both | rejected: callers get an error instead of a tunnel |
| Detect an upgrade by a non-empty `Upgrade` header alone | simpler | can disagree with `httputil.ReverseProxy`, which tunnels only when `Connection` carries `upgrade` | rejected: edge and proxy must agree |
| **Idle timeout: a fixed 5 minutes, in the activator's transport** | one rule for external and internal tunnels; no config key; an active tunnel is never cut | a caller that keeps a tunnel silent must send traffic (a WebSocket ping counts) within 5 minutes | **chosen** (decider) |
| Document the unbounded tunnel as an accepted risk | no code | a tunnel without traffic stays open indefinitely | rejected by the decider: bound it now |
| Add a tunnel lifetime or a byte limit | also bounds an active tunnel | cuts a tunnel that still carries traffic | rejected: an active tunnel stays open while traffic flows |
| Make the idle timeout a config key | tunable per install | a new key against this ADR's constraint; no caller needs another value | rejected |
| Wrap the hijacked client connection | sees the client side directly | `httputil.ReverseProxy` hijacks inside `forward`; reaching that conn needs a `Hijacker` wrapper on every writer, while the transport already sees each 101 body | rejected: the transport is the one site both paths cross |

## Decision

1. **A bodiless upgrade is forwarded untouched.** An external request skips ADR-0134 normalization when all three
   hold: its `Connection` header contains the token `upgrade` (case-insensitive,
   `httpguts.HeaderValuesContainsToken`), its `Upgrade` header is non-empty (the rule `httputil.ReverseProxy` uses
   to tunnel), and `r.ContentLength == 0`. It is streamed as internal traffic is.
2. **Any other request is normalized as today.** An upgrade request with a body, or chunked (`ContentLength == -1`),
   is read through `http.MaxBytesReader(..., maxNormalizeBytes)`, answers 413 over the bound and is wrapped. A plain
   GET with no body still gets `data:null`.
3. **Every other external step still applies** to a bodiless upgrade: the PEP (`dataplane.go:157-168`), the
   throttle (`dataplane.go:180`), the credential strip (`dataplane.go:185-188`) and the response deadline
   (`dataplane.go:213-215`).
4. **The WebSocket conformance runs through the real `dataplane.Handler`**: a new external round-trip test in
   `internal/dataplane`, and the `pkg/funcd` on-chain WebSocket subtest served by the real handler.
5. **An idle tunnel is closed.** funcd closes every upgrade tunnel `Activator.forward` proxies, external or
   internal, once no byte has flowed through it in either direction for 5 minutes; a byte in either direction
   restarts the clock. The timeout is an unexported constant; there is no lifetime and no byte limit.

## Temporary workarounds

None.

## Contracts

No exported signature changes. One unexported predicate in `internal/dataplane/normalize.go`:

```go
// bodilessUpgrade reports whether r is an HTTP/1.1 upgrade by httputil.ReverseProxy's rule (Connection
// carries the "upgrade" token and Upgrade is non-empty) that carries no body (ContentLength 0; chunked is -1).
// Such a request skips ADR-0134 normalization (ADR-0181).
func bodilessUpgrade(r *http.Request) bool {
	return r.ContentLength == 0 &&
		httpguts.HeaderValuesContainsToken(r.Header["Connection"], "upgrade") &&
		r.Header.Get("Upgrade") != ""
}
```

`serveFunction` (`dataplane.go:193`): `if !internal {` becomes `if !internal && !bodilessUpgrade(r) {`; the comment
at `dataplane.go:189-192` names the exception. Nothing else in `serveFunction` changes.

| Request (external) | `bodilessUpgrade` | Outcome |
|---|---|---|
| `Connection: Upgrade`, `Upgrade: websocket`, no body | true | streamed untouched; upstream sees `Content-Length` 0 |
| upgrade headers, `Content-Length` > 0, or chunked (`ContentLength` -1) | false | normalized; 413 over `maxNormalizeBytes` |
| `Upgrade` set, `Connection` without `upgrade` | false | normalized (the proxy does not tunnel it either) |
| GET, no body, no upgrade headers | false | normalized to `data:null` (ADR-0134) |

A new file `internal/activator/tunnel.go`:

```go
// tunnelIdleTimeout is how long a 101 tunnel may carry no byte either way before funcd closes it (ADR-0181).
const tunnelIdleTimeout = 5 * time.Minute

// idleTunnel returns rt with the io.ReadWriteCloser body of a 101 response wrapped to close once idle passes
// with no byte read or written; every other response is returned unchanged.
func idleTunnel(rt http.RoundTripper, idle time.Duration) http.RoundTripper
```

The wrapper is an unexported struct holding the inner body in a field (never embedded) with exactly `Read`, `Write`
and `Close`: it stays an `io.ReadWriteCloser`, so `httputil.ReverseProxy` tunnels it, and `io.Copy` cannot reach the
inner `io.WriterTo` or `io.ReaderFrom` and bypass the clock. `Read` and `Write` run on the proxy's two copy
goroutines; a call that moves more than 0 bytes stores the last-activity time in an `atomic.Int64` of monotonic
nanoseconds (elapsed since the wrapper was made). A `time.AfterFunc` timer fires at last activity plus `idle`,
closes the body if `idle` has passed and otherwise re-arms for the remainder. `Close` is idempotent (`sync.Once`)
and stops the timer: `handleUpgradeResponse` (go1.26.4, `backConnCloseCh`) closes the back connection again after
the tunnel ends. Closing the upstream side ends the tunnel, and the proxy then closes the client connection.

Wiring, in `activator.New` (`internal/activator/activator.go:148`): after `transport = DeadlineTransport(transport)`,
add `transport = idleTunnel(transport, tunnelIdleTimeout)`. That is `a.transport`, which `forward` sets on its proxy
(`activator.go:422`) for every request `ServeHTTP` forwards (`activator.go:219`), external and internal. The
workflow and Sensor client (`pkg/funcd/funcd.go:1983`) does not tunnel and is unchanged.

**Dependencies & I/O**: consumes the request's `Connection`, `Upgrade` and `ContentLength` and every 101 body
`forward` proxies.

**Shared test upstream (new, test-only).** A new package `internal/testkit/wsupstream` (beside `freeport` and
`realshim`): `New(tb testing.TB)` starts an `httptest` server over `x/net/websocket`, closed by `tb.Cleanup`, that
records every request's `ContentLength` and serves the query parameter `mode`: `echo` replies `echo:<frame>`;
`sink` replies only to a frame starting `probe:`; `push` echoes and also sends `push:<n>` every `every` (a duration
query parameter). The dataplane, activator and `pkg/funcd` tests use it; it replaces the stand-in's echo; no copy.

## Implementation plan

1. **Prove first.** Add `internal/testkit/wsupstream` and `internal/dataplane/upgrade_test.go` (package
   `dataplane_test`), reusing `readyEndpoints`, `spyScaler` and `seedFn` (`internal/dataplane/frontdoor_test.go:25`,
   `:38`, `:62`) and `dataplane.Handler(st, act, rtr, nil, nil, nil, timeout, nil)` (`dataplane.go:81-82`):
   - `TestScenarioExternalWebSocketRoundtrips` — asserts upstream `ContentLength` 0. **Fails on main** (`CL=120`).
   - `TestScenarioUpgradeTunnelOutlivesDeadline` — `timeout` 300 ms, a frame at 700 ms gets its echo. **Fails on
     main** (same cause).
   - `TestScenarioUpgradeWithBodyStillCapped` — sized and chunked: 413 `payload-too-large`, no upstream request.
     Passes on main; fails under the unnarrowed option A.
   - `TestScenarioBodilessGetStillNormalized` — the upstream decodes `"data":null`. Passes on main; fails under B.

   In `pkg/funcd/edge_shaping_e2e_test.go` (tag `e2e`), the terminal (lines 97–117) routes `/function/ws` to a real
   `dataplane.Handler`: a memory store with Function `ws` in `default`, `activator.New` over a test-local
   `activator.Endpoints` returning a `wsupstream` server as ready and a test-local `activator.Scaler` that fails,
   `router.New()` with no entries; the comment at lines 86–87 says only SSE and normal use the stand-in. Extract the
   chain assembly (lines 119–131) and this terminal into one helper the soak test also calls. Rename the subtest
   `ws-upgrade-roundtrips` (lines 171–180) to `edge-chain-websocket-real-handler`; it **fails on main**. Run
   `scripts/agent/d go test -race -count=1 -run 'TestScenario(ExternalWebSocket|UpgradeTunnel|UpgradeWithBody|BodilessGet)' ./internal/dataplane/`
   and `-tags e2e -run TestScenarioE2EFullEdgeChainStreaming ./pkg/funcd/`; record the failures.
2. Add `bodilessUpgrade` and the condition (Contracts); `TestBodilessUpgrade` in `internal/dataplane/normalize_test.go`
   covers the Contracts rows plus `Connection: keep-alive, upgrade` in any case.
3. Add `internal/activator/tunnel.go` and its wiring. In `internal/activator/tunnel_test.go` (package `activator`),
   an `httputil.ReverseProxy` set up as `forward` sets it (`FlushInterval` -1, `Transport:
   idleTunnel(httpx.NodeTransport(), 300*time.Millisecond)`) fronts `wsupstream` (`sink` for the idle case and the
   first one-direction tunnel, `echo` for the active case, `push` with `every=100ms` for the second):
   `TestScenarioIdleTunnelClosed`, `TestScenarioActiveTunnelStaysOpen`, `TestScenarioOneDirectionIsTraffic`,
   `TestIdleTunnelPassesNon101` (a 200 body comes back as the same value). They cannot fail on main for their
   reason; their revert check makes `idleTunnel` return `rt`, and `TestScenarioIdleTunnelClosed` must then fail.
4. Rerun step 1's commands and `-race -run 'TestScenario|TestIdleTunnel' ./internal/activator/`: all pass. Revert
   check: with the condition at `dataplane.go:193` restored, the three main-failing tests fail again.
5. **Soak (decider; never in `just ci`, `just ci-full` or CI).** `pkg/funcd/tunnel_soak_test.go`, tag
   `e2e && soak`, `TestSoakUpgradeTunnel`; the stage length is `FUNCD_SOAK_DURATION` (skip when unset). Through step
   1's helper (real edge chain, `dataplane.Handler` and `activator.New`, so the real 5-minute constant), it records
   goroutines (`runtime.NumGoroutine`) and open files (entries of `/dev/fd`) after the chain, the `wsupstream`
   server and the activator are up and before either tunnel opens, then asserts: (1) an active tunnel (`push` with
   `every=60s`, plus a client frame every 60 s offset by 30 s) stays open for the whole stage and every echo
   matches; (2) an idle tunnel (`sink`) opened at the stage start is closed by funcd 5 min to 5 min 10 s later, both
   ends seeing the close; (3) after both close and a 2 s settle, goroutines are at most baseline + 5 and open files
   at most baseline + 2. A stage lasts its duration or until (2) resolves, whichever is later. Run `5m`, `15m`,
   `1h`, `2h` in order, each only after the previous passed, detached on the developer host, e.g.
   `mkdir -p .cache/soak && FUNCD_SOAK_DURATION=5m nohup scripts/agent/d go test -tags 'e2e soak' -count=1 -timeout 3h -run TestSoakUpgradeTunnel ./pkg/funcd/ > .cache/soak/5m.log 2>&1 &`.
6. Checks: `go vet`, `go tool golangci-lint run` (also `--build-tags e2e,soak`) and `go test` through
   `scripts/agent/d` on `internal/dataplane`, `internal/activator`, `internal/testkit/wsupstream` and `pkg/funcd`.

**Definition of done**: each scenario has a passing test of the same name; the three main-failing tests fail on the
unfixed code, and `TestScenarioIdleTunnelClosed` fails with `idleTunnel` as a pass-through; the PR records the four
soak stages (pass or fail, idle close time, goroutines and open files at baseline and end); no new dependency or
config key; ADR-0134 carries the back-link; the FEAT-0006 F78 row reads `[ADR-0114](…) (…) (+ [ADR-0181](…) —
bodiless upgrades + idle tunnel)` with the status `shaping: implemented · upgrade passthrough: <status>`, `<status>`
following this ADR at every move (`adr` at Proposed through `implemented`); FEAT-0006 line 262 cites ADR-0181's
real-handler tests (`TestScenarioExternalWebSocketRoundtrips`, the `edge-chain-websocket-real-handler` subtest).

## Review checklist

- [ ] The diff to `serveFunction` is the one condition at `dataplane.go:193` and its comment (Decision 3 holds).
- [ ] `bodilessUpgrade` requires the `upgrade` token (`httpguts.HeaderValuesContainsToken`), a non-empty `Upgrade`
      and `ContentLength == 0`; `TestScenarioUpgradeWithBodyStillCapped` covers a sized and a chunked body.
- [ ] The `pkg/funcd` WebSocket subtest goes through a real `dataplane.Handler`, not the stand-in.
- [ ] `idleTunnel` is applied once, in `activator.New` after `DeadlineTransport`, wraps only a 101 with an
      `io.ReadWriteCloser` body, has only `Read`, `Write` and `Close`, an atomic clock and an idempotent `Close`.
- [ ] `internal/testkit/wsupstream` is the only WebSocket test upstream; no copy.
- [ ] The soak test has the tag `e2e && soak`, takes its baseline after setup; the PR lists all four stages.
- [ ] Both revert checks were run; `go.mod` and `go.sum` are unchanged; no absolute path or username in the diff.

## Consequences

- **Positive**: external WebSocket to a Function works as ADR-0013 §3 requires; the F78 claim is tested on the real
  handler; `maxNormalizeBytes` has a regression test for upgrade-header bodies; no tunnel stays open without
  traffic for longer than 5 minutes.
- **Negative**: `serveFunction`'s normalization has one more branch; a caller that keeps a tunnel silent must send
  traffic, such as a WebSocket ping, within every 5 minutes.
- **Risks accepted**: an SSE GET still receives the 120-byte `data:null` envelope (ADR-0134's GET rule, kept); a
  bodiless upgrade to a shipped shim still fails at the shim (POST only, ADR-0030); a longer silent tunnel needs a
  superseding ADR; only the soak test, which no CI lane runs, exercises the real 5-minute timeout.

## Open questions

- None beyond Scope Out (HTTP/2 extended CONNECT is left to the ADR that adds a WebSocket-capable runtime).

## References

- Issue [#727](https://github.com/pyvvo/funcd/issues/727) (external fails with upstream `CL=120` and `EOF`).
- Go `net/http/httputil` (`upgradeType`, `handleUpgradeResponse`); RFC 6455 §4.1; RFC 9110 §7.8.
