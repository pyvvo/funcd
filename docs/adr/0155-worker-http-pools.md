# ADR-0155: Worker HTTP pools — one constructor for node-local calls, one pool per caller, no proxy

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: data-plane, workflow, eventing, catalog, http, sustainability
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (the activator) + [F10](../feat/0000-feat-v1.md) (the
  embedded gateway), the two rows that carry ADR-0041; no row covers daemon HTTP pools, so the other members ride on F11
- **Supersedes (in part)**: [ADR-0041](0041-gateway-upstream-connection-pooling.md) — its transport settings only:
  `Proxy` becomes nil (ADR-0041 inherits `ProxyFromEnvironment` from `http.DefaultTransport`), the constructor moves to
  `httpx.NodeTransport` and its `upstream-pooled` test to `TestNodeTransportSettings`; its values and per-proxy pooling
  carry forward. ADR-0041 stays `Implemented` and gets a `Superseded in part by: ADR-0155` back-link at acceptance.
- **Relates to**: [ADR-0143](0143-redeploy-by-revision-switch.md) (`CallTracker.Wrap` keeps wrapping the activator,
  dispatch and Sensor pools) · [ADR-0151](0151-external-invoke-deadline.md) (Proposed; its `workerClient` wraps the
  dispatch and Sensor pools in `DeadlineTransport`) · [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md)
  (the Upstream route) · [ADR-0137](0137-per-caller-catalog-query-rbac.md) (the catalog PEP proxy) ·
  [ADR-0163](0163-retry-times-in-config.md) (Proposed; the engine probe's 2 s becomes a key) · #564 / PR #570
  (`internal/platform/httpx`) · builds on #598 (merged, 0b8335c; the fourth drain site)

## Context & Need

The daemon calls processes on its own node over HTTP: function and pool workers, provider engines, its own
node-private servers. Each caller has its own pool (#570), but only the activator and the embedded gateway (no
production traffic, ADR-0033) apply ADR-0041's tuning. Workflow dispatch, Sensor invocation, the data-plane Upstream
route (`internal/dataplane/dataplane.go:79`) and the catalog proxy's engine pool
(`internal/catalog/gateway/manager.go:95`) keep Go's 2 idle connections per host, so more than 2 concurrent calls to
one host each dial and drop a connection, leaving a TIME_WAIT on the daemon's ephemeral ports. All of them also
inherit `Proxy = ProxyFromEnvironment`: Go exempts loopback, not a netns address, so under containerd a daemon started
with `HTTP_PROXY` sends calls to `10.x` workers through that proxy (verified).

Measured for #573 on `1193be6` (darwin, loopback httptest worker, 3 ms handler, one host, 3 interleaved runs,
reproduced by a second harness; neither is in the repository, and this table is the record #573 asks for):

| Load | Today: 2 default pools | 2 dedicated ADR-0041 pools | 1 shared ADR-0041 pool | 1 shared default pool |
|---|---|---|---|---|
| 15 bursts of 32 steps + 4 Sensor loops, 600 calls: new connections | 485–496 | 36 | 36 | 496–508 |
| same: client-side TIME_WAIT | 481–492 | 0 | 0 | 494–506 |
| 16-wide bursts and 4 Sensor calls, never overlapping, 480 calls: new connections | 339–340 | 20 | 16 | — |

`MaxIdleConnsPerHost = 256` alone on today's pools gives 36 / 0: the per-host idle limit is the whole gain; sharing
saves only the Sensor's ≤ 4 connections, only when bursts never overlap. The Upstream route, in bursts of 32 (640
calls), makes 602 new connections and 600 TIME_WAIT with defaults (94 % dial), 32 and 0 tuned; at ≤ 2 concurrent
calls both make 2 and 0. Limits of the evidence:

- Churn needs **more than 2 concurrent calls to one host**: `upstreamOf` (`internal/function/function.go:1398-1409`)
  returns one worker per Function, so one Function or one pool worker; a fan-out over distinct Functions does not
  churn today (8 hosts, 8 wide: 12 new connections on every config).
- The Node.js shim closes idle connections after 5 s (`node:http` default `keepAliveTimeout`; the shim sets none).
  After longer gaps a tuned pool dials as often as today, but TIME_WAIT lands in the worker's netns, not on the
  daemon's ports (a 1 s stand-in, 3 gapped cycles: 60 dials either way, daemon-side TIME_WAIT 48 → 0).
- The Python shim's `ThreadingHTTPServer` (HTTP/1.1, no handler timeout) parks one thread per open connection until
  the client closes it.

The purpose: every daemon call to a node-local process reuses its connections under concurrency and never takes a
proxy, through one rule that a new caller can apply without measuring again.

## Scenarios

- `scenario: workflow-burst-reuses-connections` — Given a Function with one ready worker and a workflow that dispatches
  32 steps to it at once, 15 times, When the steps run, Then the worker accepts 32 connections, all in the first burst.
- `scenario: sensor-burst-reuses-connections` — Given four Sensor deliveries in flight to one Function at once, 25
  times, When they are invoked, Then the worker accepts 4 connections, all in the first round.
- `scenario: edge-upstream-burst-reuses-connections` — Given an Upstream edge route (ADR-0138) and 32 concurrent
  external requests to it, 20 times, When they are served, Then the upstream accepts 32 connections, all in the
  first burst.
- `scenario: proxy-env-ignored` — Given the daemon runs with `HTTP_PROXY` set and no `NO_PROXY`, When it calls a worker
  at a netns address (`10.63.0.5:8080`), Then the connection goes to that address, never to the proxy.
- `scenario: retried-step-reuses-connection` — Given a step function that answers 503 with a 1 KiB body, When the step
  is dispatched 5 times, Then the worker accepts one connection (holds on `main` through #598's test; a guard).

## Scope

In: the rule that makes a daemon HTTP pool node-local; its constructor; each member's pool; one body-drain helper.

Out:
- external callers' pools: the per-operation OCI pools (`internal/artifact/artifact.go:416-436`), and the
  `http.DefaultClient` uses inside containerd's Pull authorizer and certmagic's OCSP stapling (one issue each);
- calls to workers on another node: decided with the multi-node scheduler (ADR-0017's driver swap); until then every
  worker runs on the daemon's node, so the activator, dispatch and Sensor pools stay members;
- a per-host connection cap or admission (ADR-0112's limits); HTTP/2 to workers (ADR-0041's open question);
- forwarding `CloseIdleConnections` through `countingTransport`: no caller closes a counted client's pool;
- clients outside the daemon: `pkg/sdk`, `internal/testkit/bench`, `internal/testkit/loadgen`
  (a raw `&http.Transport{}`, `loadgen.go:67-70`).

## Constraints & Decision drivers

- No globals (ADR-0002): a pool is a field built by its owner. Never the process-global pool (#564, forbidigo).
- A caller must survive another caller closing or retuning connections (#290, #531): pools stay per caller.
- Every worker invocation (activator, dispatch, Sensor) is counted through `CallTracker.Wrap` (ADR-0143); readiness
  probes are not invocations and stay uncounted.
- Isolation holds by the pool key: one `host:port` per worker (ADR-0041 §Multi-tenancy).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **One constructor; each caller builds its own tuned, proxy-free pool** | The whole measured gain; no new seams; callers stay isolated | Idle sockets up to each pool's own peak for 90 s; a Python worker keeps one thread per idle connection for up to 90 s | **chosen** |
| One daemon-owned worker pool injected into callers (B) | One idle cap; one place to close | Saves ≤ 4 connections, only for non-overlapping bursts; new seams in `activator.Deps` and `dataplane.Handler`; one caller's `CloseIdleConnections` hits all (#290); an untuned shared pool is worse (496–508) | rejected |
| Tune only workflow and Sensor (the issue's first candidates) | Smallest diff | The data-plane Upstream route churns at 94 %; the proxy hole stays in every pool | rejected |
| Copy the settings into each caller | No new name | Two copies exist already, and the data plane and engines were missed | rejected |
| Keep `ProxyFromEnvironment`; operators add the CNI subnet to `NO_PROXY` | No code change | A proxy is never right for traffic that stays on the node; fails silently when forgotten | rejected |

## Decision

1. **Membership.** A daemon HTTP pool is **node-local** when every destination it can reach runs on this node: a
   worker or engine funcd started (a loopback port, or a netns address on the node's bridge) or a server the daemon
   itself runs. Any other pool is **external** and keeps `httpx.Transport()` (or its library's transport) with the
   environment proxy: OCI pulls, runtime downloads (`funcd install`), ACME, the S3 backup target.
2. **One constructor.** Every node-local pool is built by `httpx.NodeTransport()` (or `httpx.NodeClient`, which
   wraps it). Its settings, over `httpx.Transport()`, are the activator's `newPooledTransport` values
   (`internal/activator/activator.go:112-119`), moved, plus `Proxy` nil:

   | Setting | Value | Why |
   |---|---|---|
   | `MaxIdleConnsPerHost` | 256 | The lever: keeps an idle connection for each concurrent call to a host (ADR-0041) |
   | `MaxIdleConns` | 512 | Overall idle cap (ADR-0041) |
   | `IdleConnTimeout` | 90 s | ADR-0041. Reuse across a gap holds only where the peer keeps the connection open: not on the Node.js shim (5 s) |
   | `Proxy` | nil | The traffic never leaves the node |
   | Dialer | 30 s timeout, 30 s TCP keep-alive | ADR-0041 |
   | Everything else | from `httpx.Transport()` | No `MaxConnsPerHost`, no `ResponseHeaderTimeout`: callers bound calls by context (a step's timeout, ADR-0094), and streams stay open |

3. **Members, each with a pool of its own.** A caller is the component that owns the pool field: it shares the pool
   across everything it serves (the activator across Functions, the catalog Manager across its proxies) and never
   passes it to another caller. The members: the activator forward, the embedded gateway, workflow dispatch and Sensor
   invocation (each `calls.Wrap(nil)` builds one), the data-plane Upstream route, the catalog proxy's engine pool, and
   the readiness probes of Functions and provider engines (their own timeouts; dedicated per #290). The worker-node
   local API invoker has no pool: it serves through the data-plane handler, whose worker hop is the activator's.
4. **One drain.** `httpx.CloseBody` replaces the four inline drains and their three constant names.
5. **Shutdown adds no close.** Every member's destinations stop with the platform (the runtime stops workers and
   engines; the catalog Manager closes its proxy servers), and Go's transport drops an idle connection whose peer
   closed. `Manager.Shutdown`'s existing `engines.CloseIdleConnections()` stays.

## Temporary workarounds

None.

## Contracts

New in `internal/platform/httpx/httpx.go` (all four names are new; the file gains the `io` import):

```go
// NodeTransport returns a new transport, with a connection pool of its own, for calls that never leave this node:
// function and pool workers, provider engines and the daemon's own node-private servers (ADR-0155). Concurrent calls
// to one host reuse keep-alive connections (ADR-0041), and no proxy is used.
func NodeTransport() *http.Transport {
	tr := Transport()
	tr.Proxy = nil
	tr.MaxIdleConns = 512
	tr.MaxIdleConnsPerHost = 256
	tr.IdleConnTimeout = 90 * time.Second
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return tr
}

// NodeClient returns a client over a new NodeTransport; a zero timeout means none.
func NodeClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: NodeTransport()}
}

// DrainLimit is the most of an unread response body that CloseBody discards.
const DrainLimit = 4 << 10

// CloseBody discards at most DrainLimit bytes of body and closes it. A body read to EOF returns its keep-alive
// connection to the pool; a longer one closes the connection, so an unbounded answer is never read in full.
func CloseBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, DrainLimit))
	_ = body.Close()
}
```

| Consumes | Exposes |
|---|---|
| Nothing new; node-local pools stop reading `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` | `httpx.NodeTransport`, `httpx.NodeClient`, `httpx.DrainLimit`, `httpx.CloseBody` |

## Implementation plan

1. `internal/platform/httpx/httpx.go`: the four names above. `Transport`'s doc comment becomes "Transport returns a
   new transport with http.DefaultTransport's settings, the environment proxy included, and a connection pool of its
   own, for destinations off this node. A pool whose every destination runs on this node uses NodeTransport
   (ADR-0155)." `httpx_test.go`:
   - `TestNodeTransportSettings` (the Decision table) replaces `TestPooledTransport`, the activator's `upstream-pooled`
     test (ADR-0041), deleted with `internal/activator/pooling_test.go`; the embedded gateway's `TestUpstreamPooled` stays.
   - `TestCloseBodyDrainsAtMostDrainLimit`.
   - `TestScenarioProxyEnvIgnored`. net/http reads the proxy environment once per process and
     `TestIssue564_ClientSurvivesDefaultTransportCloseIdle` sends a request first, so `t.Setenv` is too late: a new
     `TestMain` sets `HTTP_PROXY=http://192.0.2.1:3128` and unsets `NO_PROXY` and `no_proxy` before `m.Run`, so no
     host exemption reaches the test (the package's other tests call 127.0.0.1, which the proxy exempts). Each
     transport's `DialContext` records its address and fails. A POST to `http://10.63.0.5:8080/` through
     `httpx.Transport()` records `192.0.2.1:3128` (the control: the environment was read); through `NodeTransport` it
     records `10.63.0.5:8080`.
2. Callers (drop the then-unused imports: `net` in `activator.go`; `net` and `time` in `embedded.go`):

   | Site | Change |
   |---|---|
   | `internal/activator/activator.go:107-119`, `:158` | delete `newPooledTransport`; `httpx.NodeTransport()` |
   | `internal/activator/calltracker.go:43-47` | nil → `httpx.NodeTransport()`, which the `:43` doc comment names |
   | `pkg/funcd/funcd.go:730`, `:847` | unchanged, or inside ADR-0151's `workerClient` if it lands first: each `calls.Wrap(nil)` now builds its own `NodeTransport` |
   | `internal/workflow/dispatch.go:92`, `internal/sensor/invoker.go:100` | `httpx.NodeClient(0)` |
   | `internal/dataplane/dataplane.go:79` | `httpx.NodeTransport()` |
   | `internal/catalog/gateway/manager.go:95`, `proxy.go:53` | `httpx.NodeTransport()` |
   | `internal/gateway/embedded/embedded.go:39-45` | `return &driver{transport: httpx.NodeTransport()}` |
   | `internal/function/function.go:324` | `httpx.NodeClient(probeTimeout)` |
   | `internal/provider/runtime.go:64` | `httpx.NodeClient(2 * time.Second)`; with ADR-0163 the 2 s becomes `provider.Deps.ProbeTimeout` (0 means 2 s), set from `catalog.engineProbeTimeout` |

3. Drains → `defer httpx.CloseBody(resp.Body)`, constant deleted: `internal/sensor/invoker.go:23-25, 81-84`
   (`maxDrainBody`), `internal/function/function.go:1501-1503, 1520-1523` and `internal/provider/probe.go:10-12, 37-40`
   (`probeBodyMax`; both files drop their then-unused `io` import), `internal/workflow/dispatch.go` (`drainBodyMax`,
   #598).
4. Tests, each counting `StateNew` through the worker's `ConnState` hook (as `TestIssue348_InvokeReusesConnection`).
   In each burst test the worker's handler is a barrier holding a wave's calls until all W arrive (under a test
   deadline); without it a call that ends while another's dial is pending hands that call its connection and the
   dialed one stays idle, so a fast handler under `-race` can open W+1. The first wave opens exactly W, later waves
   none.
   - `TestScenarioWorkflowBurstReusesConnections` (`internal/workflow/dispatch_test.go`): `NewHTTPDispatcher` with
     `&http.Client{Transport: activator.NewCallTracker(nil).Wrap(nil)}` (as `funcd.go:847`), 15 waves of 32, each
     joined before the next; = 32.
   - `TestScenarioSensorBurstReusesConnections` (`internal/sensor/invoker_test.go`): the client as `funcd.go:730`, 25
     rounds of 4; = 4.
   - `TestScenarioEdgeUpstreamBurstReusesConnections` (`internal/dataplane/upstream_test.go`): the Upstream route as in
     `TestScenarioDataPlaneServesUpstreamBackend`, 20 bursts of 32; = 32.
   - `TestScenarioRetriedStepReusesConnection`: #598's `TestDispatch_ReusesConnectionAfterUnreadBody`, renamed;
     `TestIssue126_DispatchReadsOnlyWhatTheStepNeeds` bounds use `httpx.DrainLimit`.
   - `TestIssue312_ProxyBoundsStalledAndIdleConns` (`internal/catalog/gateway/manager_test.go:153-156`, `:170`): its
     doc comment and comparison name `httpx.NodeTransport()` instead of `http.DefaultTransport` (both 90 s).
5. Documents (the `adr` gates): at Draft, the F10 and F11 rows in `docs/feat/0000-feat-v1.md` add `(+ [ADR-0155] —
   node-local pools)` to their ADR cell and split their status, labelling both halves as F13 does: `gateway: implemented
   · node-local pools: adr` (F10), `scale-to-zero: implemented · node-local pools: adr` (F11); each later gate advances
   its own `node-local pools` label. At acceptance, ADR-0041 gets the `Superseded in part by: ADR-0155` back-link
   (the one edit an Implemented ADR permits). No blueprint change. The PR carries `Fixes #573`.
6. Done when: the touched packages pass with `-race`; `just ci` is green; outside tests,
   `grep -rn 'httpx\.Transport\|httpx\.Client' --include='*.go'` (code and comments) matches only `internal/artifact`,
   `internal/runtime/provision` and `internal/testkit/bench`.

## Review checklist

- [ ] `NodeTransport` sets exactly the Decision table's values over `httpx.Transport()`, `Proxy` nil included.
- [ ] `newPooledTransport` and the embedded gateway's inline copy are gone; every member in Decision 3 builds its pool
      with `NodeTransport`/`NodeClient`, and no pool value reaches a second owning component.
- [ ] `Transport`'s doc comment names it for destinations off this node, with the environment proxy, and points a
      node-local pool to `NodeTransport` (ADR-0155).
- [ ] The grep in step 6 finds only external callers; the `calltracker.go:43` comment and `TestIssue312` name
      `NodeTransport`.
- [ ] `maxDrainBody`, both `probeBodyMax` and `drainBodyMax` are gone; the four sites call `httpx.CloseBody`.
- [ ] `CallTracker.Wrap(nil)` builds a `NodeTransport`.
- [ ] Each scenario's test passes with `-race`; `TestScenarioProxyEnvIgnored` fails with `ProxyFromEnvironment` restored.
- [ ] ADR-0041 carries the back-link; the F10 and F11 rows list ADR-0155; no identity or absolute path in the diff.

## Consequences

- Positive: bursts to one Function, pool worker or catalog proxy no longer churn the daemon's ephemeral ports (481–492
  → 0 TIME_WAIT per 600 calls measured); a daemon `HTTP_PROXY` can no longer reroute worker calls; one place holds the
  class's settings, and two setting copies and four drains go.
- Negative: each pool holds up to its peak concurrency of idle sockets per host for 90 s; a connection to a Node.js
  worker idle over 5 s is reopened.
- Risks accepted: the limits of the evidence in Context. A Python worker's `ThreadingHTTPServer` keeps one thread per
  idle connection for as long as the daemon keeps the connection (up to the 90 s idle timeout). This ADR releases no
  funcd-python version. A pool that outlives the Node.js shim's 5 s can write a POST onto a connection as the shim
  closes it; the call fails, and net/http does not retry it (not replayable, `shouldRetryRequest`). The activator has
  carried this since ADR-0041; dispatch (ADR-0094) and Sensor delivery (ADR-0118) retry a failed call.

## Open questions

None.

## References

- Issue [#573](https://github.com/pyvvo/funcd/issues/573) (the measurement is recorded here); PR
  [#598](https://github.com/pyvvo/funcd/pull/598); #564 / PR #570; #290, #287, #531, #348
- ADR-0002, ADR-0016, ADR-0017, ADR-0033, ADR-0041, ADR-0094, ADR-0112, ADR-0118, ADR-0137, ADR-0138, ADR-0143,
  ADR-0151, ADR-0163
- Go `net/http`: `Transport` (`DefaultMaxIdleConnsPerHost` = 2), `ProxyFromEnvironment` (loopback exempt; `NO_PROXY`
  and `no_proxy` included, read once per process), `persistConn.shouldRetryRequest`; Node.js `server.keepAliveTimeout`
  (default 5000 ms); Python `ThreadingHTTPServer`
