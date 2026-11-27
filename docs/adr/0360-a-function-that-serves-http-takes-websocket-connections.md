# ADR-0360: A Function that serves HTTP takes WebSocket connections with Hono's upgradeWebSocket

- **Status**: Reviewing (2026-11-27, implemented); Accepted (2026-11-27), by the DR session for the decider, who sent the brief there ("your picks but make
  it fast"), with the four choices it named confirmed, after two rounds of an independent judge, every finding folded
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: function, shim, edge, http, websocket
- **Realizes**: [FEAT-0006/F277](../feat/0006-feat-ingress-hardening.md) (WebSocket in Functions that serve HTTP)
- **Supersedes (in part)**, with a back-link at acceptance: [ADR-0243](0243-http-functions.md) Decision 4 for an
  upgrade request whose route takes the socket (the shim answers `101` and passes the socket to the route instead of
  answering the route's `Response`), and the WebSocket item of its Scope (Out)
- **Relates to**: [ADR-0181](0181-edge-forwards-bodiless-upgrades.md) · [ADR-0143](0143-redeploy-by-revision-switch.md) ·
  [ADR-0151](0151-external-invoke-deadline.md) · [ADR-0316](0316-a-functions-health-paths-are-funcds-own.md) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md)

## Context & Need

A Function with `spec.protocol: http` serves a Hono app through the shim's `http(app)` adapter (ADR-0243). An app whose
two ends both send at any time, such as a TV page and a phone remote linked by a relay, needs a WebSocket.

The edge already carries one. It forwards a bodiless upgrade untouched, with the path remainder and
`X-Forwarded-Prefix` (ADR-0181 Decision 1; `envelopePlugin` and `prefixPlugin` in `internal/dataplane/plugins.go`), and
the activator tunnels the worker's `101` and closes a tunnel that carries no byte either way for `tunnelIdleTimeout`, 5
minutes (ADR-0181 Decision 5, `internal/activator/tunnel.go`). The shim does not: it listens to no `upgrade` event
(`shim/src/shim.ts`), so Node hands the request to the app as a plain `GET` (checked on Node 23: a `request` event with
`upgrade: websocket`), and the client's handshake fails on the app's answer.

Hono's API for it is `upgradeWebSocket`, from each runtime's adapter. On Node, `@hono/node-server` 2.1.11 serves it
itself: `serve()` takes a `ws` `WebSocketServer` (`noServer: true`) as its `websocket` option, and completes the
handshake for a route that called its `upgradeWebSocket`; `@hono/node-ws`, the earlier package, is deprecated. The shim
already serves with `@hono/node-server`. A route in a Function's bundle cannot call that `upgradeWebSocket`, though: it
finds its server through the request env of the `serve()` that runs it, and the route's env is `{ funcd: ctx }`
(ADR-0243 Decision 4).

Purpose: a Function that serves HTTP takes WebSocket connections at its addresses, with the route code that Hono uses
on Node, while the edge and the rest of funcd stay as they are.

## Scenarios

The namespace is `default`. Function `devices-api` is the http-api example; its route `/relay` is made with
`upgradeWebSocket` and sends each message to every other socket of the route.

- `ws-relay` — *Given* `spec.timeout: 1s` and a Prefix Route `/api` → `devices-api`, *when* client A connects to
  `/function/devices-api/relay`, client B to `/api/relay`, and A sends `left` 2 s later, *then* B receives `left` and A
  receives nothing.
- `ws-not-taken` — *When* an upgrade request reaches `/devices`, a route that takes no socket, *then* the client gets
  the app's status `200` and its headers with an empty body; a `GET /relay` with no `Upgrade` header reaches the next
  handler, here the app's `404`.
- `ws-message-cap` — *When* a client sends a message over 1 MiB, *then* its socket closes, and a new socket still
  relays.
- `ws-keeps-worker` — *Given* `devices-api` with `replicas: 0`, `scaling.minReplicas: 0` and `scaling.idleTimeout: 1s`,
  *when* a socket stays open 3 s, *then* the Function stays `Ready` and the socket open; after it closes, idle reclaim
  makes the Function `Idle`.
- `ws-at-switch` — *Given* an open socket on revision 1, *when* revision 2 is applied, *then* a new socket reaches
  revision 2, the open one keeps relaying on revision 1, and it ends when revision 1's worker stops,
  `runtime.drainGrace` after the switch.

## Scope

- **In**: `upgradeWebSocket` in `@funcd-dev/shim`; the shim's handling of an upgrade request in a Function with
  `protocol: http`; the message cap; the http-api example's `/relay`; the guide, the behavior rows, the e2e tests.
- **Out**: Python, with its http adapter in its own ADR (ADR-0243 Open questions); a pooled http Function (ADR-0243
  Decision 1 forbids it); a socket over a link (`ctx.fetch` answers whole, ADR-0243 Decision 6); a cloudevent Function
  (its shim keeps no `upgrade` listener); message compression (`ws`'s server leaves permessage-deflate off); a close
  frame when a worker stops (the shim exits at once on `SIGTERM`, `shim/src/runtime.ts`). No funcd Go code changes.

## Constraints & Decision drivers

- The decider, 2026-11-27 ("your picks but make it fast"): Hono's own API; `@funcd-dev/shim` exports
  `upgradeWebSocket`, served by the `http(app)` adapter as Hono serves it on Node; Node first, Python in a later ADR, a
  pooled http Function out; a connected socket keeps its worker up, ADR-0181's idle close and ADR-0143's drain apply,
  and clients reconnect.
- A Function's bundle carries its own copy of `@funcd-dev/shim` and of `hono`. It and the shim agree only through what
  the shim passes to the app and through global symbols, as `http()`'s tag does (`Symbol.for('funcd.http')`,
  `shim/src/types.ts`).
- Licenses: `ws` 8.23.0 (MIT) and `@hono/node-server` (MIT, already bundled) go into `shim.mjs`; `hono` (MIT).
- No new funcd config key, and the edge stays as it is.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. `upgradeWebSocket` in `@funcd-dev/shim/ws` hands the route's events to the shim, which upgrades with `@hono/node-server`'s own code** | Hono's API and its Node handshake; one owner of the socket | the shim bundles `ws` | **Chosen** |
| B. `upgradeWebSocket` from `@hono/node-server` or `@hono/node-ws` in the Function's bundle | the stock import | it finds its server through the env of the `serve()` that runs it, the shim's | Rejected: cannot work |
| C. The shim's own handshake on `ws`, as `@hono/node-ws` did | no reliance on `@hono/node-server`'s WebSocket code | a second copy of the event and message handling Hono ships | Rejected: reuse |
| D. Export it from the package's main entry | one import path | that entry would import `hono/ws`, so every `http()` user needs Hono, a plain fetch app's too | Rejected: a `ws` entry |
| E. A funcd socket API (`ctx.socket`) | no Hono at all | not Hono's API | Rejected by the decider |
| F. `ws`'s default message cap, 100 MiB | nothing to document | one message can fill most of a worker's 512 MiB | Rejected: 1 MiB, an http Function's body ceiling |
| G. The app's whole answer, body included, when no route takes the socket | the body reaches the client | a writer of its own beside `@hono/node-server`'s | Rejected: status and headers, as `@hono/node-server` writes |

## Decision

1. **`upgradeWebSocket`.** `@funcd-dev/shim/ws`, a new entry of the package with `hono` as its optional peer
   dependency, exports `upgradeWebSocket`, made with `hono/ws`'s `defineWebSocketHelper`:
   `app.get('/relay', upgradeWebSocket((c) => events))`. For a request whose `Upgrade` header is `websocket`, in any
   case, it passes the route's events to the hook that the shim's context carries under `Symbol.for('funcd.ws')`, and
   answers the hook's response; any other request goes to the next handler. The context is one object for every call
   (`shim/src/invcontext.ts`), so the hook acts on the call that runs it, and only when the shim's server received
   that call as an upgrade; otherwise it fails. On a shim that predates this ADR there is no hook, and the helper
   throws. Either way the app answers its error.
2. **The shim takes the socket with `@hono/node-server`** (replaces ADR-0243 Decision 4 for an upgrade). In a
   Function with `protocol: http`, the shim serves with
   `serve()`'s `websocket` option, a `ws` `WebSocketServer` with `noServer: true`. An upgrade request reaches the shim's
   routes as any request does: `/health/*` gets the shim's answer, and any other path the app (ADR-0243 Decision 4),
   with the hook on the context. The hook calls `@hono/node-server`'s `upgradeWebSocket` with the shim's own request
   context and the route's events. Once the app has answered, `serve()` completes the handshake and runs the events as
   on Node: `onOpen` first, messages that arrive before it is called delivered after it, text as a string and binary as
   an `ArrayBuffer`, each event with Hono's `WSContext`, whose `raw` is `ws`'s `WebSocket`. The shim runs each event in
   the upgrade call's invocation context (`invStore.run`), its deadline dropped: its log records carry that call's ids,
   `ctx.principal` is who opened the socket, and `ctx.remainingMs()` is null. A request that no route takes gets the app's status and
   headers with an empty body, and an upgrade other than `websocket` gets `400`. The shim ends the call's span itself
   on this path, at the handshake or the refusal, since nothing reads the body of an upgrade's answer.
3. **The message cap.** The server's `maxPayload` is 1 MiB, the ceiling of an http Function's request body (ADR-0243
   Constraints): a larger message closes its socket with code 1009.
4. **What funcd does, unchanged.** An open socket is a call in flight until it closes (`countedConn` in
   `internal/activator/calltracker.go`; ADR-0143 Decision 6). Idle reclaim therefore spares its worker, and at a
   revision switch the socket stays on the old worker, which stops `runtime.drainGrace` after the switch: the client
   sees the connection end with no close frame (1006 in a browser) and must reconnect. `spec.timeout` bounds the wait
   for the `101` (ADR-0151 Decision 2), a socket that carries no byte either way for 5 minutes is closed (ADR-0181
   Decision 5), every edge step applies to the upgrade request (ADR-0181 Decision 3), and the socket holds a
   `server.limits.maxInFlight` slot while open (`internal/edge/limit/limit.go`).

## Design model

None: funcd gains no lifecycle, timer or race. A socket is a call in flight of ADR-0143's drain
([`revision-drain`](../design/function/revision-drain.md)), and its idle close is ADR-0181's timer, both unchanged.

## Behavior rows

- **calls.md**: a new row "An upgrade to a Function that serves HTTP" (`101` for a route that takes it, else the app's
  status and headers; the 1009 cap; `tunnelIdleTimeout`).
- **functions.md**: the row on an open stream at a switch gains the socket, which the client must reopen.

## Temporary workarounds

None.

## Contracts

```ts
// @funcd-dev/shim/ws: a new entry; hono ^4.12 is the package's optional peer dependency
import type { UpgradeWebSocket, WSEvents } from 'hono/ws';
import type { WebSocket } from 'ws';

export const upgradeWebSocket: UpgradeWebSocket<WebSocket>;

// shim/src/types.ts, beside HTTP_HANDLER: a global symbol, so the copy in a Function's bundle and the shim's agree.
// The shim's FunctionContext carries the hook under it in a Function with spec.protocol: http; the hook takes the
// socket of the call that runs it when the server received that call as an upgrade, and rejects otherwise.
export const WS_UPGRADE: unique symbol = Symbol.for('funcd.ws');
export type TakeWebSocket = (events: WSEvents<WebSocket>) => Promise<Response>;

// shim/src/shim.ts: the WebSocketServer's maxPayload
const maxMessageBytes = 1 << 20;
```

| Consumes | Writes | Exposes |
|---|---|---|
| an upgrade request that the edge forwards (ADR-0181); the route's events | the `101` and the socket's frames; else the app's status and headers | `upgradeWebSocket` in `@funcd-dev/shim/ws` |

## Implementation plan

- **funcd-typescript**, its own PR and release (ADR-0141):
  - `shim/src/ws.ts` and `shim/package.json`: the `./ws` entry, with `hono` as an optional peer dependency;
    `tsconfig.lib.json` builds it. `WS_UPGRADE` and `TakeWebSocket` in `shim/src/types.ts`.
  - `shim/src/shim.ts`: under `protocol: http`, `serve()`'s `websocket` option with `maxMessageBytes`, and the hook on
    the context of a request that `serve()` dispatched as an upgrade. `@hono/node-server` at a version that has the
    option (2.1.11 does) and `ws` ^8.23 as devDependencies, bundled into `shim.mjs`; `ws`'s optional `bufferutil` and
    `utf-8-validate` stay external.
  - Unit tests (`node --test`): the helper passes a `websocket` upgrade to the hook and any other request to the next
    handler, and throws with no hook; through the shim, a taken upgrade answers `101` and relays, an untaken one
    answers its status with an empty body, a message over the cap closes with code 1009, `onOpen` runs before the
    first `onMessage`, an event's `ctx.principal` is the upgrade's, and the call's span ends at the handshake or the
    refusal.
  - `examples/http-api`: the `/relay` route, its test and README. Then a release, and
    `go get github.com/pyvvo/funcd-typescript@<tag>` in funcd.
- **funcd**:
  - `pkg/funcd`: one e2e test per scenario, named after it, with `t.Parallel()` and `golang.org/x/net/websocket`
    (already a dependency), at short pacing: `spec.timeout` 1 s; `scaling.idleTimeout` 1 s with `Pacing.ReclaimInterval`
    200 ms; `runtime.drainGrace` 2 s. That client reads any close as `io.EOF`, so the e2e tests assert when a socket
    ends, and the close codes stay with the shim's unit tests.
  - `e2e/http-api.venom.yml`: on containerd, an upgrade through the Route `/api/relay` answers `101`, so the runtime
    image's shim is checked: an `exec` step runs `curl -i -N --max-time 2` and asserts on its output, not its exit
    code, since curl then waits until the time runs out.
  - Docs: the guide's "Serve HTTP requests" gains WebSocket, the example's route by include; the behavior rows; the
    F277 row; ADR-0243's back-link at acceptance.
- **Done when** the shim's tests pass, `go test -tags e2e ./pkg/funcd/` passes the five scenarios, the http-api lane
  passes, and `just ci` is green.

## Review checklist

- [ ] `upgradeWebSocket` from `@funcd-dev/shim/ws` takes only a `websocket` upgrade, passes any other request to the
      next handler, and throws with no hook; the package's main entry imports no `hono`.
- [ ] The shim upgrades only under `protocol: http`, through `serve()`'s `websocket` option and `@hono/node-server`'s
      `upgradeWebSocket`, with no handshake code of its own.
- [ ] `maxPayload` is the named 1 MiB; a larger message closes 1009.
- [ ] The call's span ends at the handshake or the refusal; an event runs in the upgrade call's context, its deadline
      dropped.
- [ ] One e2e test per scenario, named after it; the http-api lane's `101` case passes on containerd.
- [ ] No funcd Go code outside tests changes; the guide and the behavior rows name the idle close, the drain's cut and
      the reconnect.

## Consequences

- **Positive**: a Function serves WebSocket with Hono's route code for Node; the edge and the Function's lifecycle do
  not change; a Function with one replica gets every socket on its one worker, so a relay can keep its sockets in
  memory; `funcdctl dev` runs the same shim, so the dev loop serves sockets too.
- **Negative**: the shim grows by `ws`; `@funcd-dev/shim/ws` needs Hono; an open socket holds a
  `server.limits.maxInFlight` slot, as a stream does.
- **Risks accepted**: a deploy cuts every open socket `runtime.drainGrace` after the switch with no close frame, and
  meanwhile a relay's sockets sit on two workers, so a message may miss a socket on the other one; a socket silent
  both ways for 5 minutes closes, so an app that idles sends pings.

## Open questions

- Python's WebSocket: in the ADR of its http adapter (ADR-0243 Open questions).

## References

- #1589, the feature issue (tracker #1039)
- Hono's WebSocket helper, read 2026-11-27 (`@hono/node-ws` deprecated; Node support in `@hono/node-server`):
  https://hono.dev/docs/helpers/websocket
- `@hono/node-server` 2.1.11 (MIT): https://github.com/honojs/node-server
- `ws` 8.23.0 (MIT): https://github.com/websockets/ws
- RFC 6455 §7.4.1, close codes 1006 and 1009: https://www.rfc-editor.org/rfc/rfc6455#section-7.4.1
