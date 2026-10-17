# ADR-0243: Functions that serve HTTP

- **Status**: Implemented (2026-10-18, review passed on its second round). Accepted (2026-10-17) under the decider's
  delegation ("go ahead until you finish"), after two judge rounds.
- **Date**: 2026-10-17
- **Deciders**: green-0-rabbit
- **Tags**: function, shim, edge, http, streaming
- **Realizes**: [FEAT-0006/F156](../feat/0006-feat-ingress-hardening.md) (Functions that serve HTTP)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0243` back-link:
  [ADR-0030](0030-function-execution-runtime-shim-node.md) §1 and [ADR-0037](0037-typescript-hono-runtime-shim.md) §2
  (`POST /` is the only call) · [ADR-0046](0046-pooling-placement-policy.md) Decision 1 (an `http` Function is never
  pooled) · [ADR-0058](0058-contract-codegen-from-code-types.md) Decisions 3 and 4 and
  [ADR-0123](0123-runtime-compiled-io-validators.md) Decisions 1 and 4 (no I/O contract for an `http` Function) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md) Decisions 3, 4 and 5 (the local API gains `fetch`; `invoke` refuses an `http`
  target) · [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) Decisions 1 and 2 (`funcdctl.yaml` gains
  `protocol`) · [ADR-0134](0134-gateway-normalized-invoke-body.md)'s passthrough branch (for an `http` Function every
  JSON body is wrapped).
- **Relates to**: [ADR-0121](0121-declarative-referential-integrity-admission.md) ·
  [ADR-0147](0147-atomic-admission-and-nested-call-cap.md) · [ADR-0214](0214-app-hooks.md) ·
  [ADR-0241](0241-one-exposure-shape.md) · [ADR-0181](0181-edge-forwards-bodiless-upgrades.md)

## Context & Need

A Function answers one call: `POST /` with a CloudEvent, returning JSON. The edge already forwards a Prefix Route's
method, path remainder, query and headers (`internal/dataplane/dataplane.go:207-226`), but the TypeScript shim
registers only `POST /` (`shim/src/shim.ts:76`) and a handler is `(ctx, event) => JSON` (`shim/src/types.ts:60`). So
`GET /devices/42` answers `404` before user code runs, and a handler cannot set a status or headers, or stream.

This ADR lets a Function serve an HTTP app (Hono, or any `fetch` app) — a REST API with its OpenAPI spec, or a
streamed answer such as SSE or MCP's Streamable HTTP — while CloudEvent stays the only wire format and the default.
App authors use it to put an API in one Function and call it from the edge.

## Scenarios

The namespace is `staging`; Function `devices-api` has `protocol: http` and exports `http(app)`, a Hono app, unless a
scenario says otherwise.

- `http-rest` — *Then* `GET staging.localhost/function/devices-api/devices/42` answers the app's `200` JSON,
  `POST …/devices` its `201` with its headers, `PUT` and `DELETE` theirs, a bad body the app's `400`, and a body with
  a top-level `data` key reaches the app as sent.
- `http-route` — *Given* a Prefix Route `/api` → `devices-api`, *Then* `GET /api/devices/42` reaches the app as
  `GET /devices/42` with the query, the headers and `X-Forwarded-Prefix: /api`.
- `http-stream` — *Given* a route that writes one line per second with `streamText`, *Then* the client receives each
  line as it is written.
- `http-validate` — *Given* `protocol: http` with `pooling.worker` or a Python runtime, *Then* apply refuses it.
- `http-mismatch` — *Given* `protocol: http` with an event handler export or a delivered contract, or `cloudevent`
  with `http(app)`, *Then* the worker exits before listening and the Function is `ShapeValid=False` `ShapeInvalid`.
- `http-no-events` — *Given* a Sensor action naming `devices-api`, *Then* the Sensor is `Ready=False`
  `TargetServesHTTP`, and an event it matches fails with that reason without reaching the worker; a workflow step and
  an App hook naming it fail the same way.
- `http-fetch` — *Given* Function `purge-bridge` with a link `devices` → `devices-api`, *When* it calls
  `ctx.fetch('devices', '/devices/purge', {method: 'POST'})`, *Then* it receives the app's status, headers and body;
  `ctx.invoke` on that link fails naming `ctx.fetch`, and `ctx.fetch` to a `cloudevent` Function names `ctx.invoke`.
- `http-limits` — *Given* a request body over 1 MiB, *Then* the edge answers `413`; a non-JSON body reaches the app
  byte for byte.

## Scope

**In**: `spec.protocol`; the TypeScript shim's HTTP adapter; the edge's encoding for an `http` Function; who may call
one; `ctx.fetch`; `funcdctl` for an `http` Function.

**Out**: Python (its own ADR); pooling an `http` Function (it needs a streamed host-member protocol); WebSocket (an
upgrade reaches the edge untouched, ADR-0181, but the adapter does not pass it to the app); request streaming (the
body stays buffered); events delivered to an `http` Function (a bridge Function handles them).

## Constraints & Decision drivers

- CloudEvent stays the default and the only wire format (the decider).
- The edge buffers a request body to wrap it, at most 1 MiB (`internal/dataplane/normalize.go:15-19`); the decider
  keeps that ceiling.
- A cross-resource referent is checked by the consumer's reconciler, not by admission (ADR-0121).
- A `Function` manifest is written by hand; `funcdctl` builds and pushes the artifact (ADR-0122 Decision 4).
- Functions call each other through the worker-node local API (ADR-0064), not through the public edge.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. An HTTP adapter in the shim; the envelope stays on the wire; edge-only callers; `ctx.fetch` over links** | One wire format; response streaming | Request bodies buffered at 1 MiB; JSON bodies arrive re-encoded | **Chosen** |
| B. A second wire format: the request passed through unwrapped | Request streaming; raw bodies | Two formats in the edge and both shims | Rejected by the decider |
| C. CloudEvents binary mode for every call | One format with streaming both ways | Every Function's wire changes; the decider keeps today's envelope | Rejected by the decider |
| D. An `onEvent` export beside the app for events | Events reach the same code | A second entry point; events need a content-type rule | Rejected by the decider; a bridge Function calls `ctx.fetch` |
| E. Learn the protocol from the export alone | Nothing to declare | Unknown until the worker runs; a wrong target fails late | Rejected; the field is checked against the export |

## Decision

1. **`spec.protocol`**: `cloudevent` (default) or `http`. `Validate` refuses `http` with `pooling.worker` (ADR-0046)
   or with a runtime whose name does not start with `node`, the rule of `isNodeFamily`
   (`internal/function/function.go:3052`), which moves to `api/types`. The reconciler passes the protocol to the shim
   as `FUNCD_PROTOCOL`, beside `FUNCD_HANDLER` (`internal/function/function.go:3105`).
2. **The start check.** The shim's handler resolution accepts an `http(app)` export only under `http` and refuses it
   under `cloudevent`, and an `http` Function with a delivered contract (`FUNCD_CONTRACT_PATH`, ADR-0123) fails closed.
   Each exits 3 before listening, which the Function reports as `ShapeValid=False`, `ShapeInvalid`
   (`internal/function/bootbackoff.go:33`), at its first boot.
3. **The edge, for an `http` Function.** It forwards the method, the decoded path remainder, the query and the headers
   (`dataplane.go:226`), sets `X-Forwarded-Prefix` to the prefix it stripped (the Route's, or `/function/<name>`), and
   wraps every JSON body in the CloudEvent envelope, also one with a top-level `data` or `specversion` key that
   ADR-0134 passes through for event callers. Any other body passes raw; at most 1 MiB.
4. **The adapter.** `@funcd-dev/shim` gains `http(app)`. For every request but the shim's own `/health/*`, which shadows
   the app's, the shim builds a `Request` from the method, path, query and headers, `x-funcd-*` removed, with the
   envelope's `data` re-encoded as its body (none when `data` is null, for every method) or the raw bytes, and
   `Content-Length` recomputed. It calls `app.fetch(request, {funcd: ctx})` and answers the `Response`: status, the
   app's headers under those the edge sets (`internal/activator/activator.go:510-520`), and a body streamed as it comes
   (ADR-0013). `ctx` and tracing are the event handler's; the span ends with the body.
5. **Only the edge calls it.** The Sensor and App-hook invoker (`sensor.HTTPInvoker`) and the workflow dispatcher
   refuse an `http` target before any request; the failure is recorded where each records a failed call (the Sensor's
   Invocation and dead letter, the run's step, the App's hook), reason `TargetServesHTTP`. The Sensor also reports it
   as `Ready=False`, `TargetServesHTTP`: a Function event re-runs the Sensors of its namespace that name it, as
   `MapConfigMap` re-runs Sites (`pkg/funcd/funcd.go:1050`).
6. **`ctx.fetch(alias, path, init)`** reaches a new local API route, `/fetch/{alias}/{path...}`, any method. It checks
   the link, Cedar's `auth.ActionLinkInvoke` (`internal/workernode/local/local.go:188-192`) and the nested call cap
   (ADR-0147) as `invoke` does. The Resolver also returns the target's protocol: `fetch` to a `cloudevent` Function and
   `invoke` to an `http` one fail closed, naming the other call. The request carries the caller's method, path,
   headers and body, encoded as in Decision 3, plus `X-Funcd-Member` from a pool member; the response returns whole,
   with its status and headers, at most 1 MiB (`local.go:31`). `ctx.fetch(alias, '/')` sends `/fetch/<alias>/`.
7. **No contract.** `funcdctl.yaml` gains `protocol` beside `handler`: `funcdctl push` generates no contract for an
   `http` Function, and `funcdctl dev` serves its app.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 — FunctionSpec gains
	// Protocol is how the shim calls the code: "cloudevent" (default) or "http" (ADR-0243).
	Protocol FunctionProtocol `json:"protocol,omitempty"`

type FunctionProtocol string

const (
	ProtocolCloudEvent FunctionProtocol = "cloudevent"
	ProtocolHTTP       FunctionProtocol = "http"
)

// internal/workernode/local — Resolver.Resolve also returns the target's protocol
	Resolve(ctx context.Context, caller Ref, alias string) (target Ref, protocol v1.FunctionProtocol, timeout time.Duration, err error)

// internal/sensor.HTTPInvoker and internal/workflow.HTTPDispatcher gain
	Protocol func(ctx context.Context, fn activator.FunctionRef) (v1.FunctionProtocol, error) // refuses an http target
```

```ts
// @funcd-dev/shim
export function http(app: { fetch(req: Request, env: { funcd: FunctionContext }): Response | Promise<Response> }): HttpHandler;
// FunctionContext gains
fetch(alias: string, path: string, init?: RequestInit): Promise<Response>;
```

| Consumes | Writes | Exposes |
|---|---|---|
| `spec.protocol`; the export; the forwarded request | the app's `Response`; `TargetServesHTTP` | an HTTP app at the Function's addresses; `ctx.fetch` to it |

## Implementation plan

- funcd: `FunctionProtocol` and `Validate`; `FUNCD_PROTOCOL`; the edge's encoding and `X-Forwarded-Prefix`; the local
  API's `fetch` route and Resolver; the invoker and dispatcher guards; the Sensor condition and its Function map;
  `funcdctl.yaml`'s `protocol`; OpenAPI regenerated.
- funcd-typescript: `http(app)`, the adapter, the start check, `ctx.fetch`, with unit tests; an example `http-api` (a
  Hono REST API with a streamed route, built with the Vite plugin); a release, then `go get` here.
- e2e tests in `pkg/funcd`, one per scenario and named after it, with `t.Parallel()`; a lane `http-api` on containerd
  (`scripts/lanes.yaml`, `e2e/http-api.venom.yml`) that deploys the example behind a Route and checks REST and
  streaming.
- Docs: the feat row, the back-links, the blueprint's Functions section, the shim README.
- Done when `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, the shim's tests pass, and the lane passes.

## Review checklist

- [ ] An `http` Function receives the method, path, query, headers, `X-Forwarded-Prefix`, and every JSON body wrapped.
- [ ] The adapter rebuilds the body without ambiguity and streams the app's `Response`; `/health/*` stays the shim's.
- [ ] A mismatch or a delivered contract exits 3 before listening; `Validate` refuses pooling and a non-Node runtime.
- [ ] No Sensor, workflow step or App hook ever reaches an `http` worker; the Sensor reports `TargetServesHTTP`.
- [ ] `ctx.fetch` makes the link, Cedar and nested-cap checks; `invoke` and `fetch` each refuse the other kind.
- [ ] One e2e test per scenario, named after it; the `http-api` lane passes.

## Consequences

- **Positive**: one Function serves a REST API or a stream, framework-native; the wire format and every event Function
  stay as they are.
- **Negative**: one more field beside the export; bodies over 1 MiB are refused; an `http` Function runs unpooled; a
  bridge Function sits between events and an API.
- **Risks accepted**: a JSON body, whatever its `Content-Type`, reaches the app re-encoded from the envelope's `data`,
  so a signature over the raw body does not verify and large integers may lose precision; an encoded `/` (`%2F`)
  inside a path segment reaches the app as a separator.

## Open questions

- Python's adapter (WSGI or ASGI): its own ADR.
- Pooling and WebSocket for an `http` Function: follow-ups.

## References

- #1045, the feature issue (tracker #1039)
- Hono on AWS Lambda (`@hono/aws-lambda`, `streamHandle`): https://hono.dev/docs/getting-started/aws-lambda
- Lambda response streaming: https://docs.aws.amazon.com/lambda/latest/dg/configuration-response-streaming.html
- Cloudflare service bindings, the model of `ctx.fetch`: https://developers.cloudflare.com/workers/runtime-apis/bindings/service-bindings/
