# ADR-0151: External invoke deadline — a per-Function limit on the response start, 504 on expiry

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, invoke, timeout, activator, errors, rfc9457, shim, pool
- **Realizes**: [FEAT-0006/F107](../feat/0006-feat-ingress-hardening.md) (bounded external invoke; a new row)
- **Supersedes**: None
- **Relates to**: ADR-0002, [ADR-0013](0013-gateway-ingress-httputil-primary.md), ADR-0016, ADR-0044, ADR-0050,
  ADR-0061, ADR-0064, ADR-0094, ADR-0109, ADR-0112, ADR-0113, ADR-0114, ADR-0134, ADR-0138, ADR-0141, ADR-0143,
  ADR-0147 and ADR-0163 (Proposed; siblings in the `invoke` config group), [ADR-0155](0155-worker-http-pools.md)
  (Proposed; replaces the activator's pooled transport with `httpx.NodeTransport()`, which `DeadlineTransport` wraps
  unchanged, and changes the `calls.Wrap(nil)` transport inside `workerClient`), ADR-0158 (Proposed; its shim
  release orders this ADR's pin bump), ADR-0164 (Proposed; also adds a `dataplane.Handler` parameter; whichever
  lands second rebases)

## Context & Need

Issue #187 (reproduced on `1193be6`): an external invoke of a never-settling handler gets no response until the
client gives up (a link with a 1 s timeout returns 503 after 1.001 s).

- Nothing on the path (server timeouts, edge chain, `dataplane.Handler`, `activator.forward`'s `ReverseProxy` without
  `ResponseHeaderTimeout`) bounds a handler; `ActivationTimeout` (30 s) bounds only the cold wake. A hung call holds
  the connection and any ADR-0112 in-flight slot.
- The only shim bound is the funcd-typescript pool's fixed 30 s (v0.4.4 `shim/src/pool.ts:56`): it drops the pending
  entry and answers 503 `timed out`, the worker keeps running, and links (up to 5 m) and steps (300 s default) are cut
  at 30 s too. The solo Node shim and both Python shims have no timer.
- `api/fault` has no 504 Kind. No feat row owns the "timeouts" concern (`blueprint.md:224`, ADR-0013).

**Purpose.** Every external invoke (`/function/<name>` or a Route to a Function) ends within the Function's limit,
else the daemon default, with 504 distinct from 503; each path to a pooled Node function is bounded by its own limit.

## Scenarios

A call is an HTTP request to the data-plane listener; D is the daemon default (60 s unless `invoke.defaultTimeout`).

- `scenario: hung-handler-cut-at-default` — no `spec.timeout`, never-settling handler, invoked by name or Route ⇒ 504
  after D (not before), type `urn:funcd:problem:deadline-exceeded`, title `Gateway Timeout`, detail starting
  `activator.response-deadline:` naming the Function, D and `invoke.defaultTimeout`.
- `scenario: spec-timeout-raises-limit` — D = 1 s, `spec.timeout` 3 s, answer after 2 s ⇒ 200 with output; without ⇒ 504
  after 1 s.
- `scenario: spec-timeout-bounds` — `spec.timeout` 2 h or −1 s ⇒ 422 at apply, nothing stored; 1 h accepted.
- `scenario: cold-wake-counts` — scaled to zero, `spec.timeout` 2 s, ready 1.5 s after the wake, handler 1 s ⇒ 504
  after 2 s; warm ⇒ 200 after 1 s.
- `scenario: cold-wake-cut-at-limit` — `spec.timeout` 2 s, ready 5 s after the wake ⇒ 504 after 2 s with the
  `activator.response-deadline:` detail, not the wake's 503; a call after 5 s is served warm (activation went on).
- `scenario: started-response-not-cut` — `spec.timeout` 1 s, headers at 0.5 s, body ends at 2 s ⇒ the status and the whole
  body after 2 s.
- `scenario: link-keeps-own-limit` — D = 1 s; A→B, `links[].timeout` 5 s, B answers after 2 s ⇒ A gets the output;
  link 1 s, B never settles ⇒ `context.invoke` fails 503 after 1 s, as today; pooled `nodejs22` B, link 40 s, answer
  after 32 s ⇒ output (today 503 at 30 s).
- `scenario: step-keeps-own-limit` — D = 1 s; step timeout 5 s, F answers after 2 s ⇒ succeeds; pooled `nodejs22`
  step Function, step timeout 40 s, answer after 32 s ⇒ succeeds (today 503 at 30 s).
- `scenario: pooled-node-follows-limit` — pooled `nodejs22`, `spec.timeout` 40 s, answer after 32 s ⇒ 200 (today 503
  at 30 s); `spec.timeout` 2 s, never settling ⇒ 504 after 2 s, not the pool's 503.

## Scope

**Out**: link, step and Sensor limits (header only); static and Upstream (ADR-0138) Routes; bounds after the response
starts (streaming, ADR-0013); stopping a cut handler (future shim-contract ADR); per-Route timeouts; the solo Node
and both Python shims.

## Constraints & Decision drivers

Settled for #187 (Decision 1–3, 7): a 60 s daemon default; a per-Function `spec.timeout`; a limit on the
response start that counts the cold wake; external invokes only; the pool following the forwarded limit. 504 via a
new Kind (ADR-0002); links keep 503 (ADR-0064); a shim change is a release then `go get` (ADR-0141); no new dep.

## Alternatives considered

| Option | Outcome |
|---|---|
| **Set in `serveFunction`, enforced by the activator** — knows Function and external caller; stops at headers | **chosen** |
| Timeout middleware in the public chain — before routing, cannot read `spec.timeout`; `http.TimeoutHandler` buffers | rejected |
| Knative-style timeout writer around `s.activator.ServeHTTP` — goroutine per call; late writes race the 504 | rejected |
| Context deadline on the proxied request — cuts a started response; proxy answers 503 | rejected |
| `ResponseHeaderTimeout` on the transport — per transport, misses the wake, would cut links | rejected |
| Per-Route timeout — `/function/<name>` has no Route; per-Route policy is V2 (ADR-0112) | rejected |
| 503 (`Unavailable`) — hung handler indistinguishable from failed wake; peers answer 504 | rejected |
| Default 300 s — holds a hung connection 5 min; decider chose 60 s plus `spec.timeout` | rejected |
| Split timers (first byte + idle + total) — nothing streams yet | deferred to streaming |
| Pool: no timer (entry lives until the worker answers) or the header's exact value (503 races the 504) | rejected |
| Absolute deadline in the header — needs funcd and worker clocks to agree | rejected |

## Decision

1. **The limit.** An external invoke waits `spec.timeout` for its response to start, or `invoke.defaultTimeout`
   (60 s) when 0. Both capped at 1 h (`v1.MaxInvokeTimeout`): `spec.timeout` by the OpenAPI schema (422 at apply),
   the key and option by `funcd.New`. Read from the Function loaded per call, so an edit applies next call.
2. **What it measures.** From receipt in `dataplane.Server.ServeHTTP` until the upstream's headers reach the
   activator; body read (ADR-0134), store reads and cold wake count. After headers nothing is cut.
3. **Where.** `serveFunction`, for a request not marked internal, after `store.Get`. Links enter marked internal
   (`dataplane.WithInternal`, in-process only); steps and Sensors call `Wake` and POST with their own clients;
   static and Upstream Routes are served earlier — none reach it.
4. **The 504, always before the first byte.** The activator enforces the `ResponseDeadline`: past ⇒ 504, no wake;
   `Wake` runs under a context ending at the deadline, and a deadline end ⇒ 504 while the shared activation goes on
   (ADR-0016), an earlier `ActivationTimeout` ⇒ 503 (`activation-timeout` unchanged); before headers ⇒
   `DeadlineTransport` cancels the inner context (freeing the connection and the `CallTracker` count), closes a late
   response and returns the fault, which `forward`'s `ErrorHandler` writes as is. Every other proxy error, a link
   deadline included, stays 503. A caller disconnect still cancels the upstream.
5. **The Kind.** `fault.DeadlineExceeded` (after gRPC) ⇒ 504; only the activator raises it. ADR-0114 metrics and
   access log already record it; no new metric or log line.
6. **The deadline reaches the worker.** `activator.DeadlineTransport` sets `X-Funcd-Timeout-Ms` (ms, rounded up, ≥ 1)
   to the earlier of the context deadline (link, step, Sensor client's 30 s) and the `ResponseDeadline`; deletes it
   when neither, so a caller cannot set it. Wraps the activator's transport and the dispatcher's and Sensor
   invoker's clients. Relative, like `grpc-timeout`: no shared clock.
7. **The funcd-typescript pool follows it.** Same cleanup; delay = header + 1 s, so the platform answers first;
   without a valid header 30 s (from funcd only an unbounded step sends none). Other shims unchanged.

## Temporary workarounds

None.

## Contracts

```go
// api/fault
DeadlineExceeded Kind = "deadline_exceeded" // 504 Gateway Timeout
func DeadlineExceededf(op, format string, a ...any) *Error
// kindProblem: DeadlineExceeded: {typeURI: "urn:funcd:problem:deadline-exceeded", title: "Gateway Timeout",
//   status: http.StatusGatewayTimeout},

// api/types/v1alpha1 (`just generate` regenerates api/openapi/funcd.v1alpha1.yaml)
const (
	MaxInvokeTimeout     = time.Hour
	DefaultInvokeTimeout = 60 * time.Second
)
type FunctionSpec struct {
	// ...
	// Timeout: 0 ⇒ invoke.defaultTimeout; external invokes only (ADR-0151).
	Timeout time.Duration `json:"timeout,omitempty" minimum:"0" maximum:"3600000000000"`
}

// internal/activator (new deadline.go; New, ServeHTTP, forward changed)
type ResponseDeadline struct {
	At     time.Time
	Limit  time.Duration
	Source string // "spec.timeout" or "invoke.defaultTimeout"
}
func WithResponseDeadline(r *http.Request, d ResponseDeadline) *http.Request
// fault.DeadlineExceededf(DeadlineOp, "%s/%s did not start its response within %s (%s)", fn.Namespace, fn.Name, d.Limit, d.Source)
const DeadlineOp = "activator.response-deadline"
const TimeoutHeader = "X-Funcd-Timeout-Ms"
// Sets/deletes TimeoutHeader on a request copy; with a ResponseDeadline cancels the inner context when d.At passes
// before headers, closes a later response, returns the DeadlineOp fault; never cancels a response returned before
// d.At; returns the inner response unwrapped (a 101 body stays an io.ReadWriteCloser).
func DeadlineTransport(rt http.RoundTripper) http.RoundTripper

// internal/dataplane — defaultTimeout ≤ 0 ⇒ v1.DefaultInvokeTimeout. With ADR-0164 the signature becomes
// (…, enf, lim, stat, defaultTimeout, logger), whichever lands first.
func Handler(st store.Store, act *activator.Activator, rtr router.Router, enf *authn.Enforcer, stat *static.Handler,
	defaultTimeout time.Duration, logger *slog.Logger) http.Handler

// pkg/funcd — 0 ⇒ v1.DefaultInvokeTimeout; negative or > v1.MaxInvokeTimeout ⇒ fault.Invalid from New
func WithDefaultInvokeTimeout(d time.Duration) Option
// dispatcher (timeout 0) and Sensor invoker (30 s):
// &http.Client{Transport: activator.DeadlineTransport(calls.Wrap(nil)), Timeout: timeout}
func workerClient(calls *activator.CallTracker, timeout time.Duration) *http.Client

// internal/platform/config — this ADR adds only DefaultTimeout to the `invoke` group (ADR-0147 and ADR-0163 own its
// other keys; the group is created here if neither ADR-0147 nor ADR-0163 has landed)
Invoke struct {
	// DefaultTimeout bounds external invokes only; a link keeps links[].timeout (30 s default).
	DefaultTimeout string `json:"defaultTimeout,omitempty" env:"FUNCD_INVOKE_DEFAULT_TIMEOUT"`
} `json:"invoke,omitempty"`
```

`New` wraps its pooled transport, after `Calls.Wrap`, in `DeadlineTransport`. `ServeHTTP` with a deadline: past ⇒
write the DeadlineOp fault, no `Wake`; else `Wake` under `context.WithDeadlineCause(r.Context(), d.At, <the
DeadlineOp fault>)`, and on that cause with `r.Context()` live, write it. `forward`'s `ErrorHandler` writes a
`DeadlineExceeded` error as is, without its `upstream call failed` Warn; every other error keeps the Warn and stays
`fault.Wrapf(perr, fault.Unavailable, op, "upstream call failed")`. `cmd/funcd` parses the key with
`parseDuration("invoke.defaultTimeout", …, 0, true)` (unset or 0 ⇒ default; negative ⇒ `Invalid`) and passes
`funcd.WithDefaultInvokeTimeout`; `pkg/funcd` passes it to `dataplane.Handler` and builds the Sensor and dispatcher
clients (today inline at `pkg/funcd/funcd.go:730`, `:847`) with `workerClient`. Example detail:
`activator.response-deadline: default/agent did not start its response within 10m0s (spec.timeout)`.

```yaml
apiVersion: funcd.io/v1alpha1
kind: FuncdConfig
invoke:
  defaultTimeout: 60s
```

funcd-typescript `shim/src/pool.ts`:

```ts
const requestTimeoutMs = 30_000; // without a valid x-funcd-timeout-ms header (unchanged)
const timeoutMarginMs = 1_000;
/** header + timeoutMarginMs when a decimal integer in 1..2_147_482_647, else requestTimeoutMs (funcd ADR-0151). */
export function callTimeoutMs(header: string | undefined): number;
```

`PooledHandler.invoke(event, timeoutMs, traceparent?, spanId?, links?)` arms its timer with `timeoutMs`;
`POST /function/:name` passes `callTimeoutMs(c.req.header('x-funcd-timeout-ms'))`; expiry as today (delete the
entry, resolve 503 `function <name> timed out`).

## Implementation plan

**At acceptance.** This ADR refines ADR-0013's seam (supersedes nothing; ADR-0013 is not edited). `blueprint.md:224`
(timeouts as composable `net/http` middleware) names the owner (FEAT-0006/F107, ADR-0151) and, in one sentence, the
handler-step exceptions: the invoke deadline is set in `serveFunction` and enforced by the activator; the ADR-0113
auth PEP is a `serveFunction` step (existing drift, corrected here). Whichever of this ADR and ADR-0164 (its
`key: function` rate step) is accepted second extends that sentence as the first left it, at `:224` and at the `:938`
ingress sentence ADR-0164 also edits.

**funcd-typescript (own PR referencing pyvvo/funcd#187, then a release; safe before ADR-0158's).** The Contracts;
`just build`, commit `shim/pool.mjs`. `shim/test/pool.test.ts` under `// scenario: pooled-node-follows-limit — …`: 1500
for `'500'`; 30 000 for `undefined`, `'0'`, `'abc'`, over the cap; never settling with header `500` ⇒ 503 after ~1.5 s.

**funcd (one PR, `Fixes #187`, breaking: `!` title, `BREAKING CHANGE:` footer — a call over 60 s now gets 504 unless raised).**
`api/fault/{fault,problem}.go`; `api/types/v1alpha1/function.go` (+ a test that the tag equals `MaxInvokeTimeout`),
`just generate`; `internal/activator/deadline.go`, `activator.go`; `internal/dataplane/dataplane.go` and every `Handler`
call site; `internal/platform/config/config.go`, `cmd/funcd/main.go`, `pkg/funcd/{options,funcd}.go`;
`examples/funcdconfig.yaml` gets a commented `defaultTimeout: 60s` under a commented `# invoke:` block (added here if no
sibling has); `go get github.com/pyvvo/funcd-typescript@<tag>` — a tag cut before ADR-0158's shim half unless
ADR-0158's funcd PR has merged (its Decision 7), and one holding ADR-0147's example change only after ADR-0147's funcd
PR; the F107 row advances with each status change.

**Test plan** (each scenario is `TestScenario<Name>` in funcd)

| Scenario | `internal/dataplane` (real Handler + activator, `httptest` upstream) | `pkg/funcd` e2e (real shims) |
|---|---|---|
| hung-handler-cut-at-default | D = 1 s; by name and Route | never-settling `nodeFn`, `WithDefaultInvokeTimeout(2s)` |
| spec-timeout-raises-limit | yes | — |
| cold-wake-counts | fake `Endpoints` ready after 1.5 s | — |
| cold-wake-cut-at-limit | ready after 5 s; call after 5 s ⇒ 200, no second scale-up | — |
| started-response-not-cut | headers 0.5 s, body ends 2 s | — |
| link-keeps-own-limit | `local.NewInvoker` over the fn-to-fn chain | pooled callee, link 40 s, 32 s handler ⇒ output |
| step-keeps-own-limit | — | step 5 s, D = 1 s; `shared`-pool step 40 s, 32 s ⇒ `Succeeded` (pooled `TestIssue28_…`) |
| pooled-node-follows-limit | — | `nodeFn(...).pooled(...)`: 32 s at 40 s ⇒ 200; never settling at 2 s ⇒ 504 |

`TestScenarioSpecTimeoutBounds`: raw HTTP in `internal/controlplane/api_test.go` (like `replicas`): 2 h, −1 s ⇒ 422,
GET ⇒ 404; 1 h ⇒ stored. Contract tests: `DeadlineTransport` (header from context, `ResponseDeadline`, earlier of both,
rounded up; deleted when neither; caller value replaced/removed; inner context cancelled at `d.At`; late response
closed, `CallTracker` 0; body after `d.At` not cut; a 101 over `Calls.Wrap` tunnels, as
`TestCallTrackerCountsUpgradedConnUntilClosed`); `workerClient(calls, 0)` with a 5 s context and `(calls, 30*time.Second)`
send the header within 1 s of their bound; `ServeHTTP` 504 without `Wake` when past, 503 at 0.5 s with
`ActivationTimeout` 0.5 s and a 2 s deadline; `ErrorHandler` plain `context.DeadlineExceeded` ⇒ 503 + Warn, deadline
fault ⇒ 504 without; `upstream_test.go` D = 1 s, Upstream answering after 2 s ⇒ 200; `problem.go` maps the Kind;
config file/env set the key; `cmd/funcd` refuses negative; `New` refuses negative or > 1 h, maps 0 to 60 s.

**Definition of done**: build, lint, test, `go mod verify` and `just ci-full` green; Lima lanes green (a lane
answering after > 60 s sets `spec.timeout`); every scenario a named passing test in its repo; no identity/path leak.

## Review checklist

- [ ] Contracts match (schema tag = `v1.MaxInvokeTimeout`, no other OpenAPI change); only the activator raises the
      Kind; only non-internal `serveFunction` sets a `ResponseDeadline`; no context deadline on the proxied request.
- [ ] Every scenario has a same-name test; PR has `Fixes #187`, `!`, `BREAKING CHANGE:`; new funcd-typescript tag pinned.

## Consequences

- (+) Every external invoke ends, freeing its connection and any ADR-0112 slot; 504 and 503 are distinguishable.
- (−) An unbounded step (no step timeout, `workflow.defaultStepTimeout: 0`) to pooled Node still ends at 30 s.
- (−) A cut handler keeps running (side effects may repeat on retry; a hung Python pool member blocks its interpreter;
  idle reclaim may stop its worker, ADR-0143); tuning `spec.timeout` stamps a Revision (ADR-0143 decisions 4, 8, 9).

## Open questions

None. Settled by the decider: margin + 1 s; pool fallback 30 s without a valid header (not the 24 h per-step cap,
`api/types/v1alpha1/workflow.go:113`); `spec.timeout: 0` = daemon default (no opt-out); 1 h cap in the OpenAPI schema
and `funcd.New`; relative header in whole milliseconds.

## References

- [pyvvo/funcd#187](https://github.com/pyvvo/funcd/issues/187); funcd-typescript v0.4.4 and funcd-python v0.3.5 pools. Prior art (read 2026-10-04): [Knative](https://raw.githubusercontent.com/knative/serving/main/pkg/http/handler/timeout.go) · [Cloud Run](https://docs.cloud.google.com/run/docs/configuring/request-timeout) · [of-watchdog](https://raw.githubusercontent.com/openfaas/of-watchdog/master/executor/http_runner.go) · [Fission](https://raw.githubusercontent.com/fission/fission/main/pkg/router/functionHandler.go) · [OpenWhisk](https://raw.githubusercontent.com/apache/openwhisk/master/docs/reference.md) · [google.rpc.Code](https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto) · [gRPC HTTP/2](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md)
