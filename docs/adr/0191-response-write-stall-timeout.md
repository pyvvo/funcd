# ADR-0191: A data-plane response write that the client does not accept for 60 s ends the connection

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: edge, data-plane, timeout, streaming, limits, invocation
- **Realizes**: [FEAT-0006/F107](../feat/0006-feat-ingress-hardening.md) (bounded external invoke; the row ADR-0151
  realizes, joined as a second entry); relates to [FEAT-0006/F75](../feat/0006-feat-ingress-hardening.md) (ADR-0112's
  in-flight ceiling, which the static, Upstream and edge-response half of Scope serves)
- **Supersedes (in part)**: [ADR-0151](0151-external-invoke-deadline.md) (Implemented), two clauses only, each gaining
  one exception, a stalled response write (no data accepted by the client for 60 s): Decision 2, line 91 ("After headers
  nothing is cut."), and Scope *Out*, lines 59–60 ("bounds after the response starts (streaming, ADR-0013)"). The
  response-start deadline, `started-response-not-cut` and every other clause stand. The `Superseded in part by ADR-0191`
  back-link on ADR-0151 is added at acceptance (the one edit it allows).
- **Refines** `blueprint.md` at acceptance: the Ingress controller / API Gateway entry (line 225) gains, after
  ADR-0181's sentence, "A data-plane response write that the client does not accept within 60 s ends the connection
  ([ADR-0191](docs/adr/0191-response-write-stall-timeout.md))."
- **Relates to**: [ADR-0112](0112-ingress-protection-limits.md) (the in-flight slot this frees; unchanged) ·
  [ADR-0013](0013-gateway-ingress-httputil-primary.md) (streaming passthrough; conforms) ·
  [ADR-0143](0143-redeploy-by-revision-switch.md) (the `CallTracker` count this frees; unchanged) ·
  [ADR-0181](0181-edge-forwards-bodiless-upgrades.md) (its idle timeout bounds upgrade tunnels, exempt here) ·
  [ADR-0114](0114-edge-observability-shaping.md) (its five layers keep their order; WriteStall is added outside Recover)
- **Publication**: held. Written from private advisory GHSA-9pxh-64h2-j9mj; nothing of it (ADR, feat row, blueprint,
  board) is committed to the public repo until the fix is released and the advisory published.

## Context & Need

Nothing bounds a data-plane response write. The data-plane `http.Server` (`pkg/funcd/funcd.go:1120`) sets only
`ReadHeaderTimeout` and `ReadTimeout`; net/http clears the read deadline once the body is read (comment at
`:1117-1119`), and the ADR-0151 deadline stops when the upstream's headers arrive. When a client stops reading, the
kernel buffers fill and the handler blocks in `Write` until the client disconnects:

- `httputil.ReverseProxy` in `Activator.forward` (`internal/activator/activator.go:409-436`, `FlushInterval = -1` at
  `:422`) blocks in its body copy, so it never closes the upstream body; the `CallTracker` count ends only on that close
  (`internal/activator/calltracker.go:151-154`), so idle reclaim treats the worker as busy (`activator.go:560-567`).
- `limit.Chain` releases the ADR-0112 slot in a `defer` after `next.ServeHTTP` returns
  (`internal/edge/limit/limit.go:82-91`), so `maxInFlight` such connections answer every other caller 503.
- Without `maxInFlight` each such connection still holds a goroutine, a file descriptor and a pooled upstream
  connection. A static Route asset (`internal/dataplane/dataplane.go:262`) passes the same chain.

No Accepted or Implemented ADR decides a bound after the response starts: ADR-0151 excludes it (Scope Out) and defers
"split timers (first byte + idle + total)" to streaming (line 81); ADR-0112 and ADR-0013 choose no write bound.

**Purpose.** Every response on the data-plane listener ends when its client accepts no data for 60 s while funcd has
data to send, which releases the slot, the goroutine, the connection, the pooled upstream connection and the busy count;
a slow but live stream (SSE, token streaming) runs as long as it keeps being read.

## Scenarios

S is the stall limit: 60 s in production, 300 ms in the `internal/dataplane` tests (passed to `gateway.WriteStall`).

- **scenario: stalled-reader-is-cut** (the advisory reproduction) — *Given* `maxInFlight` 1 and a warm Function
  whose response is 32 MiB, *when* client A sends one request to `/function/f` (and, in a second case, through a Route;
  in a third case over TLS with HTTP/2) and reads nothing, and client B calls the same path S + 2 s later, *then* B gets
  200 with the whole body, funcd has closed A's connection (on HTTP/2, reset A's stream), A's upstream handler has
  returned and the worker's `CallTracker` count is 0.
- **scenario: stalled-static-asset-is-cut** — *Given* `maxInFlight` 1 and a static Route serving a 32 MiB asset,
  *when* A requests it and reads nothing and B requests it S + 2 s later, *then* B gets 200 with the whole asset.
- **scenario: slow-live-stream-not-cut** — *Given* an upstream that sends a 1 KiB event every S/2 for 8 S, *when* the
  client reads each event as it arrives, *then* it receives all 16 events and the end of the response.
- **scenario: quiet-stream-not-cut** — *Given* an upstream that sends one event, nothing for 3 S, then a second event
  and ends, *when* the client reads (over HTTP/1.1, and over TLS with HTTP/2), *then* it receives both events.
- **scenario: upgrade-tunnel-not-cut** — *Given* a keep-alive connection that first received a 64 KiB 200 response,
  *when* the client sends an upgrade request on the same connection to a WebSocket echo upstream and sends a frame every
  S/2 for 4 S, *then* every echo arrives (the tunnel is bounded by ADR-0181's idle timeout, not by S).

## Scope

**In**: every response written by the data-plane listener's chain (`pkg/funcd/funcd.go:1111`): `/function/<name>`,
Routes to Functions, static and Upstream Routes, and the edge's own problem responses. **Out**: the response-start
deadline (ADR-0151, unchanged); upgraded or hijacked connections (ADR-0181); the internal chain (`funcd.go:1116`) and
the other funcd servers (Open questions); a limit on total stream length; a config key.

## Constraints & Decision drivers

ADR-0013 streaming passthrough must keep working for any stream length; the 60 s value is fixed, as ADR-0181's 5-minute
tunnel idle timeout is (decider, 2026-10-05: "60 s ok"); one rule in one place that every listener response passes; a
cut must free each resource in Context; standard library only (`http.ResponseController`, Go ≥ 1.20).

## Alternatives considered

| Option | Outcome |
|---|---|
| **A. Per-write stall deadline**: arm `SetWriteDeadline(now+60 s)` before each write or flush, in an edge `ResponseWriter` wrapper; every accepted write moves the deadline | **chosen**: cuts only a client that accepts nothing, keeps live streams |
| B. Flat `http.Server.WriteTimeout` on the data-plane server | rejected: one line, but cuts every response longer than N, which breaks ADR-0013 SSE and token streaming |
| C. Release the ADR-0112 slot once the response headers are sent | rejected: changes what the ceiling measures and fixes only the 503; the goroutine, file descriptor, upstream connection and busy count stay held |
| A with N as a key (`server.limits.writeStall`) | rejected by the decider: a fixed constant, as ADR-0181's idle timeout |
| A with the deadline cleared when the handler returns, left set between writes, or cleared to the zero time | rejected: the final flush goes unbounded, or HTTP/2 resets a quiet or an ended stream (see Decisions 1–2) |

## Decision

1. **The rule.** Before each `WriteHeader`, each piece of at most 32 KiB of a `Write`, and each `Flush`, the data-plane
   listener sets the connection's write deadline to now + 60 s (`http.ResponseController.SetWriteDeadline`) and moves it
   100 years ahead when that call returns, never to the zero time. A deadline is so in force only while data waits:
   net/http's HTTP/2 server resets a stream whose deadline fires even with nothing pending
   (`http2stream.onWriteTimeout`). A write the client does not accept by then fails, and net/http closes the connection
   (HTTP/1.1) or resets the stream (HTTP/2, which both listeners advertise under TLS, ADR-0111,
   `internal/edge/tls/static/static.go:56`; the deadline is per stream).
2. **After the handler, and clearing.** When the handler returns, the deadline is armed once more, also during a panic
   unwind, bounding net/http's final flush. A clear sets the deadline 100 years ahead, never the zero time, and each
   request starts with a clear: net/http's HTTP/2 server applies a deadline asynchronously on the serve loop, in no
   fixed order against END_STREAM, and a zero deadline drops the stream's timer, so a late arm would start a timer that
   nothing stops, resetting the ended stream 60 s later (RFC 9113 §5.1). With a timer from the start, `closeStream`
   stops it, and a later deadline finds it stopped and is ignored. On HTTP/1.1, net/http clears the write deadline
   itself after `finishRequest` (Go 1.26.4 `net/http/server.go:2081`), so none stays on the idle keep-alive connection.
3. **Upgrades are exempt.** net/http clears every deadline on hijack (`conn.hijackLocked`), so a deadline from an
   earlier response on the keep-alive connection never reaches a tunnel; `ReverseProxy` returns from an upgrade only
   after the tunnel ends. `stallWriter` sets no deadline after a `Hijack`, not even the post-handler arm (net/http's
   HTTP/1.1 `SetWriteDeadline` reaches the hijacked connection unchecked). HTTP/2 has no `Hijack`.
4. **Where.** One middleware, `gateway.WriteStall(gateway.WriteStallTimeout)`, outermost in the listener chain at
   `pkg/funcd/funcd.go:1111`, so it wraps the server's own writer and every byte of every response passes it.
5. **What a cut releases.** The failed write ends `ReverseProxy`'s copy, which closes the upstream body (ending the
   `CallTracker` count) and panics `http.ErrAbortHandler`; `gateway.Recover` re-panics it (`middleware.go:54-55`), the
   `limit.Chain` defer frees the slot during the unwind, and net/http closes the client connection or resets the HTTP/2
   stream. No code changes on that path; tests prove each release.
6. **No config key, metric or log line.** ADR-0114 counts a proxied cut as a 5xx (the `ErrAbortHandler` unwind,
   `internal/edge/observ/observ.go:88-96`) and a static cut with the status it had sent; no new metric or log line.

## Temporary workarounds

None.

## Contracts

```go
// internal/gateway (new writestall.go)

// WriteStallTimeout is how long a data-plane response write may wait for the client to accept data.
const WriteStallTimeout = 60 * time.Second

// stallChunk is the largest Write piece under one deadline: the io.Copy buffer ReverseProxy and ServeContent use.
const stallChunk = 32 << 10

// clearAhead is how far ahead a clear sets the deadline: far enough that it never fires.
const clearAhead = 100 * 365 * 24 * time.Hour

// WriteStall returns a middleware that wraps w in a *stallWriter, clears the deadline, calls next, then arms it once
// more (also during a panic unwind) unless the stallWriter was hijacked. d <= 0 ⇒ next unchanged.
func WriteStall(d time.Duration) Middleware

// stallWriter sets http.NewResponseController(ResponseWriter).SetWriteDeadline(time.Now().Add(d)) before each
// WriteHeader, each stallChunk piece of Write, and Flush, and SetWriteDeadline(time.Now().Add(clearAhead)) when that
// call returns, never the zero time. A deadline error (a writer without a connection, such as
// httptest.ResponseRecorder) is ignored. Write returns the bytes written so far and the first error.
type stallWriter struct {
	http.ResponseWriter
	d        time.Duration
	hijacked bool
}

func (s *stallWriter) WriteHeader(code int)
func (s *stallWriter) Write(b []byte) (int, error)
func (s *stallWriter) Flush()                      // arm, http.NewResponseController(s.ResponseWriter).Flush(), clear
func (s *stallWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) // hijack (net/http clears deadlines); on success, set hijacked
func (s *stallWriter) Unwrap() http.ResponseWriter // for http.ResponseController, as commitWriter (middleware.go:108)
```

The method set matches `commitWriter` (`internal/gateway/middleware.go:72-108`), so `Recover`, `observ` and `shape`
reach `Flush` and `Hijack` through it unchanged. Wiring (`pkg/funcd/funcd.go:1111`):

```go
dpHandler := gateway.Chain(dpCore, gateway.WriteStall(gateway.WriteStallTimeout), gateway.Recover(p.logger),
	gateway.RequestID, edgeObserv, limit.Chain(c.limits), edgeShape)
```

| Consumes | Exposes |
|---|---|
| the server's `http.ResponseWriter` (`http.ResponseController`) | `gateway.WriteStall`, `gateway.WriteStallTimeout` |
| no config key, no port, no new dependency | no new metric, log line, problem type or header |

## Implementation plan

**At acceptance (held until the advisory is published).** ADR-0151 gets the back-link; the F107 row of
`docs/feat/0006-feat-ingress-hardening.md` links ADR-0191 as a second entry, its status cell reads `invoke deadline:
implemented · write stall: accepted` (later moves update only the write-stall half), and its sentence "A response that
has started is never cut" and the exit criterion "a started response is never cut (F107)" gain "while its client keeps
accepting data"; the blueprint sentence (header). Commits, comments and test names describe the behavior only.

1. **Prove first.** Add `internal/dataplane/writestall_test.go` (package `dataplane_test`, reusing `readyEndpoints`,
   `spyScaler` and `seedFn` from `frontdoor_test.go`) with a helper `listenerChain(core http.Handler, lim limit.Config,
   outer ...gateway.Middleware) http.Handler` composing `outer`, `Recover`, `RequestID` and `limit.Chain(lim)` in the
   order of `funcd.go:1111`, served by `httptest` with the listener's read timeouts. `TestScenarioStalledReaderIsCut`
   (A on a raw `net.Conn` with a 64 KiB read buffer, the test server's send buffer fixed at 64 KiB by the new
   test helper `internal/testkit/sendbuf`) with no `outer` must fail on main: B gets 503 `edge.limit: in-flight
   concurrency ceiling reached` (reproduced on `6b06320c`). Add `pkg/funcd/writestall_e2e_test.go` (tag `e2e`):
   `TestScenarioStalledReaderIsCutRealChain` through `funcd.New` with `WithLimits(limit.Config{MaxInFlight: 1})` and
   a Function answering 32 MiB (`nodeFn`, as ADR-0151's e2e tests); B is sent 62 s after A and resent on a 503 for up
   to 30 s (the kernel takes more of A's response after A stops reading, and the 60 s count from there), and A's
   connection ends before the whole body. It fails on main with the same 503. Data dir: `os.MkdirTemp("", "funcd")`.
2. Add `internal/gateway/writestall.go` (Contracts) and `internal/gateway/writestall_test.go`:
   `TestWriteStallArmSequence` (a fake writer with `SetWriteDeadline` records the calls: one arm per `WriteHeader`, 32
   for one 1 MiB `Write`, one per `Flush`, each moved 100 years ahead when its call returns, never the zero time, no
   write or flush without a deadline, one arm after the handler returns, nothing after `Hijack`); over a real `httptest`
   server, `TestWriteStallCutsStalledWrite` (32 MiB to a client that reads nothing: `Write` errors within S + 1 s),
   `TestWriteStallLiveClientNotCut` (one 4 MiB `Write`, server send buffer fixed at 64 KiB, to a client with a 64 KiB read buffer reading at most 64 KiB per
   S/10 completes and takes over 3 S) and `TestWriteStallEndedStreamNotReset` (100 sequential write-flush-end responses
   on one HTTP/2 connection, read with a raw `golang.org/x/net/http2` Framer, already in `go.mod`: any RST_STREAM up to
   3 S after the last END_STREAM fails it); `TestWriteStallNoDeadlineSupport` (`httptest.ResponseRecorder` gets the
   body unchanged); `TestWriteStallNonPositiveReturnsNext` (`d <= 0` returns `next` itself).
3. Wire `funcd.go:1111` (Contracts); pass `gateway.WriteStall(300 * time.Millisecond)` as `outer` in step 1's helper.
4. Scenario tests in `internal/dataplane/writestall_test.go`: `TestScenarioStalledReaderIsCut` (by name and by
   Route; and over `httptest` `EnableHTTP2` + `StartTLS`; asserts B's 200 and full body, after draining A's read returns
   EOF or a reset, the upstream handler returned, and `CallTracker.Idle(upstream, 0)` is true for an activator built
   with `Deps.Calls`), `TestScenarioStalledStaticAssetIsCut`, `TestScenarioSlowLiveStreamNotCut`,
   `TestScenarioQuietStreamNotCut` (HTTP/1.1, and `EnableHTTP2` + `StartTLS`; the HTTP/2 case fails when the deadline
   is left set between writes), `TestScenarioUpgradeTunnelNotCut` (a regression scenario: the ADR-0181
   `internal/testkit/wsupstream` echo upstream; if ADR-0181 has not landed, a raw 101 echo upstream in the test file).
5. Revert check: with `WriteStall` returning `next`, `TestScenarioStalledReaderIsCut`,
   `TestScenarioStalledStaticAssetIsCut`, `TestWriteStallCutsStalledWrite` and `TestWriteStallArmSequence` fail;
   with `Hijack` not setting `hijacked`, `TestWriteStallArmSequence` fails (the tunnel scenario passes either way).
6. Checks through `scripts/agent/d`: `go vet`, `go tool golangci-lint run`, `go test -race` on `internal/gateway`,
   `internal/dataplane`, `pkg/funcd`; the e2e test runs once in the gate (`just ci-full`).

**Definition of done**: every scenario has a passing test of the same name; the step 1 tests failed on unfixed main for
the 503 reason and pass now; `go.mod` and `go.sum` unchanged; the at-acceptance edits done when published.

## Review checklist

- [ ] `gateway.WriteStall` is applied once, outermost in the listener chain at `funcd.go:1111`, with
      `gateway.WriteStallTimeout`; the internal chain (`funcd.go:1116`) is unchanged.
- [ ] `stallWriter` arms before `WriteHeader`, each ≤ 32 KiB piece of `Write`, and `Flush`, and clears after each; a
      clear sets the deadline `clearAhead` (100 years) ahead, never the zero time; each request starts cleared; arms
      after the handler returns (deferred); nothing is set after `Hijack`; it has `Unwrap`.
- [ ] No `WriteTimeout` on any `http.Server`; no config key; no change to `limit.go`, `activator.go` or `calltracker.go`.
- [ ] Each scenario test exists by name; the step 1 tests were shown failing on main; both revert checks were run.
- [ ] Commit messages, comments and test names describe the behavior only; no absolute path or username in the diff.

## Consequences

- **Positive**: a client that stops reading no longer holds an in-flight slot, goroutine, connection, pooled upstream
  connection or busy count past 60 s; SSE and token streams of any length keep working.
- **Negative**: a client that accepts no data for 60 s while funcd has data to send is cut (the slowest client not cut
  depends on socket buffers: Linux wakes a blocked writer only after about a third of the send buffer drains, so a
  client must free that much per 60 s; on a slow real link the buffer stays near 100 KB, but a client that slows down
  sharply after a fast start can hold a buffer of a few MiB); a client that pauses
  reading over 60 s mid-response (a suspended mobile app) must retry; each `WriteHeader`, 32 KiB piece and `Flush` costs
  two `SetWriteDeadline` calls (on HTTP/2, each a serve-loop message and a timer reset): four per flushed SSE event.
- **Risks accepted**: the value is fixed, so another one needs a superseding ADR; only the e2e test exercises the real
  60 s; the sibling servers below stay unbounded until their follow-up; the first clear is also asynchronous on HTTP/2,
  so at worst one late RST_STREAM within 60 s hits an ended stream, never a 100-year timer (probe: 0 in 12,000 streams).

## Open questions

- **Sibling servers** (none shares the data-plane chain, so this rule does not cover them): the control plane
  (`pkg/funcd/funcd.go:1072`, authenticated), the catalog proxies (`internal/catalog/gateway/manager.go:293-294`), the
  worker local API (`internal/workernode/local/local.go:224-225`, a per-sandbox Unix socket serving the internal chain
  at `funcd.go:1116`) and the S3 frontend (`s3gateway.Server.Run`, started at `funcd.go:1271`). Each is checked for the
  same pattern after this fix is released; a reachable, affected one gets its own ADR reusing `gateway.WriteStall`.

## References

- Private advisory GHSA-9pxh-64h2-j9mj (published with the fix); issue #90 (the request-side `ReadTimeout`).
- Go `net/http` (`ResponseController.SetWriteDeadline`, Go 1.20; `ErrAbortHandler`); `net/http/httputil` `ReverseProxy`.
