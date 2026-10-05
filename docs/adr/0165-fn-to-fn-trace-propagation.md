# ADR-0165: Fn-to-fn trace propagation — a CLIENT span in the caller's shim, forwarded by the broker

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: observability, traces, trace-context, w3c, invoke, edge, shim
- **Realizes**: [FEAT-0004/F51](../feat/0004-feat-platform-observability.md) (Traces capture — the fn→fn stamping
  that ADR-0101 left as a workaround)
- **Supersedes (in part)**, each keeping status `Implemented` with a one-line `Superseded in part by: ADR-0165`
  back-link at acceptance:
  - [ADR-0101](0101-trace-capture-invocation-span.md) scenario `invocation-emits-span`, "exactly **one** OTLP span is
    captured for that invocation, kind `SERVER`" (lines 45–47), Decision "the shim emits **one** span record on the
    telemetry channel: kind `SERVER`" (lines 139–140), "emits one span line per invocation" (line 169) and "the
    baseline is one span per invocation" (line 348): still one `SERVER` span per invocation, plus one `CLIENT` span
    per `context.invoke` call. Its span wire `"kind":"SERVER"` (line 258) becomes `SERVER|CLIENT`, and `Span.Name`
    "the function name (FUNCD_FUNCTION) or "invoke"" (line 206) also holds `call <alias>`.
  - [ADR-0114](0114-edge-observability-shaping.md) "Callers: every data-plane request (observability wraps the whole
    hop …)" (line 31), the observability half only (shaping still wraps internal calls), and Decision 1's trace,
    RED metrics and access log (lines 112–123): they cover data-plane listener requests, not internal fn-to-fn calls.
- **Refines** (additions only, no back-link): [ADR-0064](0064-fn-to-fn-rpc-links.md) Decisions 3–5, Exposes row
  (lines 130–143, 206–211, 251): `POST /invoke/{alias}` takes an optional `traceparent`, the synthesized
  `POST /function/<target>` carries it, `invoke` stamps it; `Invoker.Invoke` (line 213) unchanged.
- **Closes**: ADR-0101's workaround "Adopt-only trace propagation" (lines 165–167), open question (lines 365–366) and
  Scope-out (lines 88–90) for fn→fn; uses the `CLIENT` kind ADR-0101 reserved (line 189).
- **Relates to**: [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (workflow half) ·
  [ADR-0105](0105-nested-dag-span-parenting.md) · [ADR-0106](0106-run-scoped-log-read.md) (unchanged) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) · [ADR-0147](0147-atomic-admission-and-nested-call-cap.md)
  (Proposed; its wrapping `Invoker` passes `ctx` through) · ADR-0158 (Proposed; lands
  first: the pooled half of `fn-to-fn-joins-one-trace` needs its Decision 1, the pool's `FUNCD_INVOKE_SOCKET` and
  `X-Funcd-Member`; its `member` joins the options object and keyword-only argument in Contracts) ·
  [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (Proposed; both edit `shim.ts`, `pool.ts`, `_poolworker.py`
  and rebuild `shim/shim.mjs`, `shim/pool.mjs`, the later PR rebases; its record bound covers the `CLIENT`
  record, Decision 5)

## Context & Need

Issue #86 (main 1193be6, which all line numbers cite; the #605 fix in Scope landed after it; funcd-typescript v0.4.4,
funcd-python v0.3.5, 3 of 3 runs): a function called through `context.invoke` always starts a new trace, and its log
lines carry that fresh trace-id. No hop carries `traceparent`: the caller shim (`invoke.ts:24`, `invoke.py:49`) sends
only content headers; `Invoker.Invoke` (`internal/workernode/local/local.go:50`) has no trace input; the broker
(`invoker.go:38-40`) sets only `Content-Type` and `X-Funcd-Namespace`. The broker cannot stamp it alone (only the caller's shim knows its span-id; a sandbox serves
concurrent calls); the callee already adopts one (`shim.ts:79-84`). With `server.observability.trace` on, the internal
hop passes edge observ (`pkg/funcd/funcd.go:978`): greeter gets an edge parent span never stored under no-op telemetry
and counts as edge traffic. `funcdctl workflow logs` misses callee lines (it filters on the run's trace-id, `logread.go:132`).

Purpose: a fn-to-fn call joins the caller's trace (caller `SERVER` → `CLIENT` `call <alias>` → callee `SERVER`)
with no user-code change; edge signals describe only edge traffic.

## Scenarios

- `scenario: fn-to-fn-joins-one-trace` — Given edge trace off (the default) and front links greeter (alias
  `greeter`), on `nodejs22` and on `python314`, When `POST /function/front` carries `traceparent: 00-<T>-<P>-01`,
  Then three spans share trace T: front's `SERVER` span (parent P), one `CLIENT` span `call greeter` whose parent is
  front's span, and greeter's `SERVER` span whose parent is that `CLIENT` span; the `CLIENT` span contains greeter's
  to within 1 ms (Decision 6). The same holds when front runs in a pool.
- `scenario: failed-call-error-client-span` — Given a call failing before greeter's handler runs (input contract
  rejects it, 422; or an undeclared alias, 403), When front is invoked, Then front's trace holds a `CLIENT` span
  `call <alias>` with status `ERROR`, a message and `http.status_code` 422 or 403, and no greeter span.
- `scenario: workflow-logs-include-callee` — Given a workflow step running front, When the run completes, Then
  `funcdctl workflow logs <run>` includes greeter's `greeting` log line.
- `scenario: internal-call-skips-edge-observ` — Given edge observability on (metrics, access log, trace) with
  recording telemetry, When one external `POST /function/front` makes front call greeter, Then the edge records one
  request, for front: one `funcd.edge.requests` count, one `edge request` log line, one edge span; none names greeter.
- `scenario: no-header-behaves-as-today` — Given a raw `POST /invoke/{alias}` without `traceparent`, When it calls
  greeter, Then the reply is unchanged and the forwarded call has no `traceparent`: a new trace (ADR-0101
  `mint-root-trace`).

## Scope

In: the `CLIENT` span and header in both shims (solo, pool), broker forwarding, internal calls leaving edge observ,
the releases. Out: the span-name defect (fixed by #605: `shimEnv` in `internal/function/function.go` sets
`FUNCD_FUNCTION` for solo workers; pool hosts name spans after the member); `tracestate`, baggage; KV/blob verb spans;
the edge span's unstored parent for *external* requests under no-op telemetry (ADR-0114,
unchanged); broker-minted spans.

## Constraints & Decision drivers

- OpenTelemetry: the caller creates a `CLIENT` span and injects its context; it parents the remote `SERVER` span.
- W3C `traceparent` header, not a CloudEvent field (ADR-0102 line 98); flags `01`, as the edge (`observ.go:146`) and
  the engine (`internal/workflow/dispatch.go:128`) send. An old shim sends no header; an old daemon ignores it.
- `CLIENT` is already accepted (`internal/funclog/route.go:38-41`) and marshaled (`tracesink.go:306`);
  `internal/edge/observ` cannot read `dataplane`'s internal marker (`dataplane` imports `observ`, `dataplane.go:31`).

## Alternatives considered

| Option | Outcome |
|---|---|
| **B. Caller shim mints a `CLIENT` span** (OTel pattern; failed calls and the full wait stay visible) | **chosen** |
| A. Callee parents on the caller's `SERVER` span — a call failing before the callee runs is invisible | rejected |
| C. Broker mints the `CLIENT` span — the shim must still stamp; the broker would need a funclog trace sink | rejected |
| **H2. Internal calls skip edge observ** — con: they leave the edge access log and metrics (broker log lines remain) | **chosen** |
| H1. Keep edge observ on the internal hop — edge span and metric for a call that never crossed the edge | rejected |
| **Carry the header in the request context** (wrappers such as ADR-0147 pass it through) | **chosen** |
| A new `Invoke` parameter — changes the ADR-0064 contract (lines 212–214) and every implementation | rejected |

## Decision

1. **Caller shim — the `CLIENT` span.** Inside an invocation, `context.invoke(alias, input)` mints span-id S, sends
   `traceparent: 00-<T>-<S>-01` (T = the invocation's trace-id) on `POST /invoke/{alias}`, and when the call settles
   emits one record: trace T, span S, parent = the invocation's `SERVER` span, name `call <alias>`, kind `CLIENT`,
   bracketing the whole call, `OK` or `ERROR` + message, `http.status_code` as a decimal string when a reply arrived
   (wire `map[string]string`, `route.go:32`), `inv` = the caller's, `links` empty — through the `SERVER` record's emit
   helper, so a pool member's record carries `funcd.member` (ADR-0158 Decision 3). Outside an invocation: no header,
   no span; with no telemetry channel: header sent, no span line.
2. **`POST /invoke/{alias}`** accepts an optional `traceparent`. Absent, the call behaves as today.
3. **Broker.** The local API handler puts the request's `traceparent` into the context it passes to
   `Invoker.Invoke`; the data-plane invoker sets it verbatim on the synthesized `POST /function/<target>`.
   The callee's shim validates and adopts it (ADR-0101; `shim.ts:79-84`, `shim.py:165-168`; `tracespan.ts:36-45`,
   `tracespan.py:31-46`): a malformed value yields a root.
4. **Internal calls skip edge observ.** The internal-only chain (`pkg/funcd/funcd.go:978`, used only by
   `local.NewInvoker`) drops `edgeObserv` (not `observ` reading `WithInternal`: import cycle): no edge span,
   `funcd.edge.*` metric or `edge request` line; `edgeShape` and the broker's invoke log lines stay. The listener's
   edge observ is unchanged and still parents the entry function's `SERVER` span.
5. **`ERROR` and attributes.** `ERROR` covers every `context.invoke` failure (4xx/5xx, transport error, unset socket,
   non-JSON 2xx); `status_msg` is the error message the handler receives, as for the `SERVER` span.
   Attributes: `http.status_code` only (ADR-0101 line 212); the alias is in the span name.
   The shared emit helper cuts `status_msg` so the span line stays within ADR-0168's record bound
   (`FUNCD_FUNCLOG_MAX_RECORD_BYTES`, its Open question 12 taking the cut), so a large reply cannot drop the span.
6. **Timing.** The `CLIENT` span reads its own epoch base plus a monotonic delta, as the `SERVER` span does, so
   `InvContext` is unchanged; Node's ms base (`Date.now()`) lets it end up to 1 ms after its parent (no sub-ms base).
7. **Rollout** (ADR-0141): funcd forwarding and chain change first (harmless while shims send no header); ADR-0158
   lands first (Relates to); then funcd-typescript and funcd-python releases cut after ADR-0158's shim releases;
   then the funcd `go.mod` bump. A tag holding ADR-0158's shim half is pinned only with or after ADR-0158's funcd PR
   (ADR-0158 Decision 7).

## Temporary workarounds

None.

## Contracts

| Direction | What |
|---|---|
| Consumes | the caller's invocation context (ADR-0101); an optional `traceparent` request header on `POST /invoke/{alias}` |
| Exposes | `traceparent` on the synthesized `POST /function/<target>`; one `CLIENT` span line per `context.invoke` call; edge signals (trace, RED metrics, access log) for requests on the data-plane listener only |

funcd (`internal/workernode/local`, `pkg/funcd`):

```go
// local.go — added. The W3C header (ADR-0165), carried in ctx so Invoker.Invoke (ADR-0064) keeps its signature.
const traceparentHeader = "traceparent"
type traceparentKey struct{}
func withTraceparent(ctx context.Context, tp string) context.Context // empty tp: ctx unchanged
func traceparentFrom(ctx context.Context) string                     // "" when absent

// NewHandler, POST /invoke/{alias} (was: inv.Invoke(r.Context(), …)):
//	out, err := inv.Invoke(withTraceparent(r.Context(), r.Header.Get(traceparentHeader)), target, input, timeout)

// invoker.go, proxyInvoker.Invoke, after the X-Funcd-Namespace header:
//	if tp := traceparentFrom(cctx); tp != "" { req.Header.Set(traceparentHeader, tp) }

// pkg/funcd/funcd.go — the internal chain (was: …, gateway.RequestID, edgeObserv, edgeShape):
//	dpHolder.Set(gateway.Chain(dpCore, gateway.Recover(p.logger), gateway.RequestID, edgeShape))
```

funcd-typescript (`shim/src/`):

```ts
// tracespan.ts — added; SpanRecord.kind widens to 'SERVER' | 'CLIENT'.
export interface ClientSpan {
  /** 00-<caller trace>-<this span>-01, stamped on POST /invoke/{alias}. */
  readonly traceparent: string;
  /** Emits the span once; httpStatus is the local API reply status, when one arrived. */
  end(status: 'OK' | 'ERROR', statusMsg?: string, httpStatus?: number): void;
}
/** Opens the CLIENT span "call <alias>" under the active invocation (currentInv()); null outside one. */
export function startClientSpan(sink: Sink | null, alias: string): ClientSpan | null;

// invoke.ts — was makeInvoke(); shim.ts passes { sink: traceSink } (declared before ctx), pool.ts { sink: channel }.
// An options object: ADR-0158 (lands first) adds `member`, this ADR adds `sink`.
export function makeInvoke(opts: { member?: string; sink?: Sink | null } = {}): <I = unknown, O = unknown>(alias: string, input: I) => Promise<O>;
```

funcd-python (`shim/src/funcd_shim/`):

```python
# tracespan.py — added.
class ClientSpan:
    """CLIENT span "call <alias>" of one context.invoke call, under current_inv();
    outside an invocation traceparent is None, nothing emitted."""
    def __init__(self, channel: Channel | None, alias: str) -> None: ...
    @property
    def traceparent(self) -> str | None: ...
    def reply(self, status: int) -> None:
        """Record the local API reply status as http.status_code, a decimal string;
        sets no status (invoke.py raises on non-2xx)."""
    def __enter__(self) -> ClientSpan: ...
    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        """Emit the span once; any exception sets ERROR with str(exc), even if already ERROR."""

# invoke.py — was invoke(alias, payload); shim.py builds _Context(channel), _poolworker.py passes _channel.
# Keyword-only: ADR-0158 (lands first) adds `member`, this ADR adds `channel`.
def invoke(alias: str, payload: Any, *, member: str | None = None, channel: Channel | None = None) -> Any: ...
```

The span line on the existing channel (ADR-0101 wire, `kind` widened):

```json
{"funcd.signal":"traces","trace_id":"<T>","span_id":"<S>","parent_id":"<caller SERVER span>","name":"call greeter","kind":"CLIENT","start":1759550000000000000,"end":1759550000180000000,"status":"ERROR","status_msg":"context.invoke(\"greeter\") failed: 422 …","attrs":{"http.status_code":"422"},"inv":"<caller inv>","links":[]}
```

No config key, no new Go dependency, no change to `internal/funclog` decoding.

## Implementation plan

1. **funcd PR** (`Refs #86`): `local.go`, `invoker.go` and the `funcd.go:978` chain as in Contracts; fix the comments
   this ADR makes wrong (`pkg/funcd/funcd.go:975-977`: the chain now also lacks edge observ;
   `internal/funclog/span.go:18`, `:57`, `:58`, `route.go:40`). Tests in `internal/workernode/local/local_internal_test.go`
   over a recording data-plane handler: `TestScenarioNoHeaderBehavesAsToday`, contract test
   `TestBrokerForwardsTraceparent`; `TestScenarioInternalCallSkipsEdgeObserv` (`pkg/funcd`, e2e tag) over
   `shimPlatformOCI` and the TS `fn-to-fn` example with `WithEdgeObservability` all on, recording telemetry
   (`ManualReader`, `tracetest.SpanRecorder`) and a capturing `WithLogger`.
2. **funcd-typescript**: `startClientSpan`; `emitSpan` takes the kind, ids and `attrs` (today hardcoded,
   `tracespan.ts:62-86`) so both records go through it; `makeInvoke({ sink })`; `shim.ts`, `pool.ts` wiring.
   Contract tests in `shim/test/invoke.test.ts`: "client span stamps traceparent and emits CLIENT", "failed call
   emits an ERROR client span" (stub local API and sink inside `invStore.run`; the span covers the stub's delay),
   "outside an invocation, no traceparent and no span", "no telemetry channel: traceparent sent, no span line".
   `just build`, commit `shim/shim.mjs`, `shim/pool.mjs`. Release.
3. **funcd-python**: `ClientSpan` and a shared `_emit_span` (new name) used by `InvocationSpan.__exit__` and
   `ClientSpan.__exit__`; `invoke.py`, `shim.py`, `_poolworker.py` wiring. Contract tests in `shim/tests/test_invoke.py`:
   `test_client_span_stamps_traceparent_and_emits_client`, `test_failed_call_emits_error_client_span` (the span covers
   the stub's delay), the outside-an-invocation and no-channel (header sent, no span line) cases. Release.
4. **funcd PR** (`Fixes #86`), after ADR-0158's funcd pin PR (its pool socket carries the pooled subtests): `go get`
   both tags. `pkg/funcd` e2e over `newShimRig` (`readSpans`): `TestScenarioFnToFnJoinsOneTrace` (subtests `nodejs22`,
   `python314`, `nodejs22-pooled`, `python314-pooled`, via `shimFn.pooled(worker)`, Python pool gated by
   `requirePython(t, true)`); `TestScenarioFailedCallErrorClientSpan`; `TestScenarioWorkflowLogsIncludeCallee`.
   Advance the F51 row. `just ci-full` and the `fn-to-fn`, `funclog` Lima lanes green.
5. Done when each scenario has its named passing funcd test, each shim its contract tests, and funcd pins both releases.

## Review checklist

- [ ] Both shims, solo and pool, follow Decisions 1, 5 and 6 through the shared emit helper.
- [ ] Broker forwards `traceparent` verbatim via ctx (absent ⇒ none); `Invoker.Invoke`, other headers unchanged.
- [ ] No new exported Go name; internal chain has no `edgeObserv`; listener chain and `internal/funclog` decoding
      unchanged; `go.mod` changes only the two language pins.
- [ ] Each scenario has one named, passing test; ADR-0101 and ADR-0114 receive only the back-link, ADR-0064 none;
      the F51 row's status matches this ADR's.

## Consequences

- Positive: a fn→fn call is one waterfall with its full wait (cold start included) visible, failed calls included.
- Negative: one more span line per call, written and dropped when `funclog.traces:false` (ADR-0101 lines 168–171);
  three repos to release in order; internal calls leave the edge access log and
  `funcd.edge.*` metrics (the broker's invoke log lines remain; Decision 4).
- Risks accepted: span volume is unmeasured; a caller-chosen trace-id only regroups a waterfall: it feeds no storage
  scoping, and the namespace stays host-stamped (ADR-0101 lines 136–138; pool: ADR-0158 Decision 3); a Python
  handler's own threads do not inherit contextvars, so their calls carry no header, as today.

## Open questions

None.

## References

- Issue [#86](https://github.com/pyvvo/funcd/issues/86) and its re-verification on `1193be6`
- OpenTelemetry trace API: https://opentelemetry.io/docs/specs/otel/trace/api/ (read 2026-10-04)
- Dapr tracing overview: https://docs.dapr.io/operations/observability/tracing/tracing-overview/ (read 2026-10-04)
- W3C Trace Context, `traceparent`: https://www.w3.org/TR/trace-context/
