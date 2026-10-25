# ADR-0283: AI limits and usage, caps per gateway and binding and one usage row per call

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged twice; the first round bounded a call's grant and added the queue's progress assertion)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, limits, usage, telemetry
- **Realizes**: [FEAT-0015/F196](../feat/0015-feat-ai-gateway.md) (limits and usage)
- **Relates to**: [ADR-0282](0282-openai-api-from-functions.md) (the facade and its `Caps` hook) ·
  [ADR-0281](0281-ai-gateways.md) (the usage database entry) · [ADR-0280](0280-the-model-engine-on-bifrosts-go-library.md)
  (the token limit, the cancel route, the telemetry ring) · [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md)
  (rqlite) · [ADR-0010](0010-observability-telemetry-and-audit.md) (telemetry) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model) · source:
  [design report](../reports/ai-gateway-design.md) · issue #1270

## Context & Need

An internal caller in a loop must not run up an unbounded bill, and an owner needs the cost of each function
(FEAT-0015 F196). This ADR adds caps on tokens and requests, per gateway and per binding, over fixed windows; a usage
row per call in the gateway's own database, from which the caps count; and each call's metrics and span. The caps are
funcd's own, kept by the daemon's AI facade ([ADR-0282](0282-openai-api-from-functions.md)), not Bifrost's.

## Scenarios

- `scenario: cap-refuses-when-spent` — Given binding `chat` with `tokens` max 1,000 per day and 1,000 used today, When
  the function calls, Then it gets 429 with `type: funcd_cap`, `code: resource_exhausted`, `X-Funcd-Cap: tokens` and
  `Retry-After` the seconds to 00:00 UTC, and the engine is not called.
- `scenario: cap-lowers-token-limit` — Given a gateway cap of 1,000 tokens, tolerance 0.05 and 900 used, When a call
  asks for 800 completion tokens, Then the engine receives `X-Funcd-Max-Tokens: 150` and the answer carries
  `X-Funcd-Cap: tokens`.
- `scenario: calls-share-the-room` — Given 100 tokens of room, When two calls asking for 80 run at once, Then one is
  granted 80 and the other 20.
- `scenario: requests-cap-counts-calls-in-flight` — Given `requests` max 2 per minute, When three calls run at once,
  Then the third gets 429 with `X-Funcd-Cap: requests`.
- `scenario: usage-row-per-call` — After a call, the gateway's database holds one `funcd_ai_usage` row with the
  namespace, function, alias, gateway, model, tokens in and out, duration, outcome `ok` and the vendor's cost.
- `scenario: caps-survive-restart` — Given a spent day cap, When the daemon restarts, Then the next call is still
  refused, and a call that arrives while the counters load waits for them.
- `scenario: abandoned-stream-recorded` — When a function stops reading a stream, Then its row reads `abandoned`, with
  the engine's usage when the engine has it, otherwise an estimate marked `estimated`.
- `scenario: window-resets` — Given a spent minute cap, When the next minute starts, Then calls are admitted again.
- `scenario: call-metrics-and-span` — After a call, `funcd.ai.requests`, `funcd.ai.tokens` and `funcd.ai.duration_ms`
  count it with its namespace, gateway, model, function and outcome, and its span holds no prompt or answer.
- `scenario: engine-telemetry-exported` — The engine's spans for a call reach funcd's trace export in the call's trace.

## Scope

**In**: `limits` on the gateway and the binding; the counters, admission and the token grant; the rows and their
table; the rebuild at start; abandoned streams; the metrics and the span; the export of the engine's telemetry.

**Out**: caps in money and price tables (FEAT-0015, out of scope); backups of the usage database, a SQLDatabase like
any other ([ADR-0222](0222-workload-backup-resources-and-catalog-scope.md)).

## Constraints & Decision drivers

- Caps per gateway and per binding, over fixed UTC windows, with a `tolerance`, counted from the usage rows; a spent cap
  refuses before the engine, and a running call stops at max × (1 + tolerance) (decided).
- The caps survive a restart and rewind with a restore; a call waits for the database at boot (decided).
- Prompts and answers never enter logs, telemetry or audit (decided).

## Alternatives considered

1. **Bifrost's governance plugin**: budgets per virtual key in its own store. Rejected by the decider for funcd's caps.
2. **Counting a call only when it ends, with no hold**: concurrent calls each get the whole room, so N calls can run a
   cap N times over. Rejected for holds (the `no-hold` mutant fails `CompletionWithinCeiling`).
3. **Holding a prompt estimate too**: four bytes per token overestimates images by far (a 1 MB image counts about
   250,000 tokens, where vendors bill around a thousand), so image calls would be refused. Rejected: prompt tokens
   count when the call ends.
4. **Writing each row before answering**: a database write in every call's latency, and a failed call when the
   database is down. Rejected for a queue.

## Decision

1. **Limits.** `AIGateway.spec.limits` and `bindings.ai[].limits` (Functions and Workflow steps) hold `tokens` and
   `requests`, each `{max, window, tolerance}`: `max` at least 1; `window` one of `minute`, `hour`, `day`, `week`,
   `month`, fixed in UTC (a week starts on Monday, a month on the 1st); `tolerance` a decimal of at least 0, 0.05 when
   unset (1 doubles `max`, 0.5 adds half). A cap's ceiling is max × (1 + tolerance), rounded down. A call meets every
   cap that applies to it: its gateway's and its binding's.
2. **Counters.** For each cap's current window the facade keeps `used` (tokens in plus out, or calls, of the window's
   ended calls) and `held` in memory. A call counts in the window it started in; a new window starts at zero.
3. **Admission** (`Caps.Admit`), under one lock per gateway:
   - a requests cap refuses when `used + held ≥ max`, and holds 1; its tolerance has no effect, since it counts a
     call when it is admitted;
   - a tokens cap refuses when `used ≥ max`. For a chat call, its room is `ceiling − used − held`, refused below 1.
     The grant is the smallest of the rooms and the call's own limit (`max_completion_tokens`, else `max_tokens`,
     else the model's `maxCompletionTokens`, else `ai.defaultMaxCompletionTokens`, 4,096 in `funcdconfig.yaml`), so a
     call never holds a whole room nor asks a vendor for more than it serves. Each tokens cap holds the
     grant, and the engine receives it as `X-Funcd-Max-Tokens` (ADR-0280 Decision 6). An embedding call is refused
     only by a spent cap and holds nothing;
   - the answer carries `X-Funcd-Cap: tokens` when the grant is below the call's effective limit (its own, else its
     model's, else `ai.defaultMaxCompletionTokens`); a vendor that reaches the grant ends with `finish_reason: "length"`;
   - a refusal is 429 in OpenAI's error shape, `type` `funcd_cap` and `code` `resource_exhausted`, with a message that
     names the binding or gateway, the cap and its window's end, `Retry-After` the seconds left in that window, and
     `X-Funcd-Cap: tokens` or `requests`.
4. **Recording** (`Caps.Record`). At a call's end its holds are released and `used` gains its tokens in and out, or 1.
   Its row is queued per gateway and written in order, retried until it is written (the design model's
   `RowsAreWritten`); at most 10,000 rows wait, and an older row dropped beyond that is logged. A daemon crash loses the queue and the calls in flight, so the rebuilt
   counters undercount them; the design model shows that nothing else breaks.
5. **The rows.** The facade creates `funcd_ai_usage` in the gateway's usage database (ADR-0281 Decision 8) when it is
   missing (Contracts), and writes one row per call that reached the engine. A database shared through `ref` holds
   several gateways' rows, told apart by `gateway`. `cost` is the vendor's `usage.cost.total_cost`, NULL when the
   vendor reports none.
6. **The rebuild.** The facade loads a gateway's counters, the sums of each cap's current window, from its rows the
   first time a call needs them after the daemon starts, and again, with the rows still queued added, when the
   gateway's or a binding's limits change. A
   call waits while they load, until they are loaded or its request ends (then Unavailable). A restore happens before
   a daemon start (ADR-0206), so the caps rewind with the database.
7. **Abandoned streams.** When the function closes a stream before its final usage, the facade posts `POST
   /funcd/cancel/<call id>` (ADR-0280 Decision 5) and reads the stream on to its end, for up to 10 s, keeping the
   usage of its last event. With no usage by then, or a stream that breaks, it estimates four characters per token,
   from the request's text for tokens in and the text streamed for tokens out, and marks the row `estimated`. A whole
   call that its function abandons is cancelled by closing its connection, and its row is estimated the same way. A
   call cut by an engine restart follows the same rule.
8. **Telemetry** (ADR-0010). Each call has a span `ai.call` under the caller's trace, with the namespace, gateway,
   model, function, alias, outcome, token counts, cost and `estimated`, and no text; the facade sends that span's
   context to the engine as `traceparent`, so the engine's spans nest under it. Metrics, each with the namespace,
   gateway, model, function and outcome: `funcd.ai.requests` (counter), `funcd.ai.duration_ms` (histogram),
   `funcd.ai.tokens` (counter, `direction` `in` or `out`), `funcd.ai.time_to_first_token_ms` (histogram, streams),
   `funcd.ai.cost` (counter) and `funcd.ai.refused` (counter, `reason`: `tokens`, `requests` or the problem slug of an
   ADR-0282 refusal). Every 5 s the facade reads each Ready engine's `/funcd/telemetry` from its cursor (a new
   `instance` starts it over) and exports the records with their own IDs and times, spans as spans and logs as log
   records; a `dropped` count is logged at Warn. After a daemon start every engine is new (ADR-0186), so no record is
   exported twice.

## Design model

- **Specs**: [`docs/design/ai/caps.fizz`](../design/ai/caps.fizz) (state machine [`caps.md`](../design/ai/caps.md))
  and [`docs/design/ai/abandoned-call.fizz`](../design/ai/abandoned-call.fizz) ([`abandoned-call.md`](../design/ai/abandoned-call.md)).
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `CompletionWithinCeiling` | always | `scenario: calls-share-the-room`, `cap-lowers-token-limit` | `TestScenarioCallsShareTheRoom` |
  | `AdmittedOnlyBelowMax` | always | `scenario: cap-refuses-when-spent` | `TestScenarioCapRefusesWhenSpent` |
  | `CountersLoad` | eventually always | `scenario: caps-survive-restart` | `TestScenarioCapsSurviveRestart` |
  | `EngineUsageIsRecorded` | always | `scenario: abandoned-stream-recorded` | `TestScenarioAbandonedStreamRecorded` |
  | `RowsAreWritten` | eventually always | `scenario: caps-survive-restart` | `TestScenarioCapsSurviveRestart` |
  | `RowIsDecided` | eventually always | `scenario: abandoned-stream-recorded` | `TestScenarioAbandonedStreamRecorded` |

- **Bounds**: caps: two calls racing for one room, 12 actions; abandoned call: the daemon and the engine, 16 actions,
  a clock of 8 ticks. Every action is atomic; these are the smallest runs in which every mutant fails.
- **Faults**: a daemon crash that loses the counters, the queue and the calls in flight; an engine that restarts or
  hangs once; crash on yield off.
- **Environment**: one window (its rollover is arithmetic, left to tests); the database answers once loaded; the vendor
  ends a call with no completion tokens or all it was granted, bills a cancel or not, and OpenRouter's record shows on
  any try or never; a tick stands for a second, the daemon's bound is 4 ticks against the lookup's 3 tries.
- **Mutants**: caps: `no-hold`, `admit-before-load` and `leak-hold` fail `CompletionWithinCeiling`, `admit-when-spent`
  fails `AdmittedOnlyBelowMax`, `never-load` and `never-write` fail liveness. Abandoned call: `close-instead-of-cancel`,
  `bound-below-lookup` and `no-final-event` fail `EngineUsageIsRecorded`, `no-timeout` fails liveness.
- **Result**: caps: pass, 295 states, 0.4 s; wider (three calls, 16 actions): pass, 4,605 states, 1.6 s. Abandoned
  call: pass, 240 states, 0.4 s; wider (four tries, a bound of 5, 12 ticks, 22 actions): pass, 420 states, 0.5 s.
  FizzBee v0.5.4.
- **Left to tests**: the window boundaries and `Retry-After`; the rebuild's SQL; the requests cap, the same counter with
  a hold of 1; a binding's and a gateway's caps together; the estimate; the queue's order and bound; the real
  seconds of the bound against the lookup.

## Temporary workarounds

None.

## Contracts

```yaml
spec:
  limits:
    tokens:
      max: 2000000
      window: month
      tolerance: 0.05
    requests:
      max: 120
      window: minute
```

```go
// api/types/v1alpha1: AIGatewaySpec and FunctionAI gain
Limits *AILimits `json:"limits,omitempty"`

type AILimits struct {
	Tokens   *AILimit `json:"tokens,omitempty"`
	Requests *AILimit `json:"requests,omitempty"`
}

type AILimit struct {
	Max       int64    `json:"max"`
	Window    string   `json:"window"`              // minute, hour, day, week or month, in UTC
	Tolerance *float64 `json:"tolerance,omitempty"` // a fraction of max; 0.05 when unset
}

// internal/services/ai/limiter.go: implements ADR-0282's Caps
type LimiterDeps struct {
	Usage  UsageStore
	Store  store.Store
	Clock  func() time.Time
	Meter  metric.Meter
	Logger *slog.Logger
}

func NewLimiter(d LimiterDeps) (*Limiter, error)

// UsageStore reaches a gateway's usage database as rqlite user funcd (ADR-0232's EnginePassword and Engines).
type UsageStore interface {
	Ensure(ctx context.Context, db v1.ObjectRef) error
	Insert(ctx context.Context, db v1.ObjectRef, rows []UsageRow) error
	Sums(ctx context.Context, db v1.ObjectRef, q UsageQuery) (tokens, calls int64, err error)
}
```

```sql
CREATE TABLE IF NOT EXISTS funcd_ai_usage (
  time INTEGER NOT NULL, namespace TEXT NOT NULL, function TEXT NOT NULL, alias TEXT NOT NULL,
  gateway TEXT NOT NULL, model TEXT NOT NULL, tokens_in INTEGER NOT NULL, tokens_out INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL, outcome TEXT NOT NULL, cost REAL, estimated INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS funcd_ai_usage_gateway_time ON funcd_ai_usage (gateway, time);
```

| Consumes | Exposes |
|---|---|
| ADR-0282's `Caps` hook; ADR-0280's `X-Funcd-Max-Tokens`, cancel route and telemetry ring | `limits` on gateways and bindings; `X-Funcd-Cap` |
| the gateway's SQLDatabase (ADR-0281), ADR-0232's engine access | the `funcd_ai_usage` table; the `funcd.ai.*` metrics and the `ai.call` span |
| config `ai.defaultMaxCompletionTokens` (`funcdconfig.yaml`, default 4096) | |

## Implementation plan

1. `api/types/v1alpha1`: `AILimits`, `AILimit` and their `Validate`; `just generate`; config
   `ai.defaultMaxCompletionTokens` in `internal/platform/config`.
2. `internal/services/ai/limiter.go` (counters, admission, grants, windows), `usage.go` (the table, the queue, the
   rebuild), `telemetry.go` (the span, the metrics, the engine pull and export); the facade wires the `Caps` hook.
3. Tests: unit tests of the windows and `Retry-After`, the grant (rooms, own limits, both caps), the requests hold,
   the estimate, the queue bound; one `TestScenario<Name>` per scenario in `pkg/funcd`, on ADR-0281's `hostengine`
   with the real engine, `funcd-ai-vendortest` and a real rqlite engine; each Design model assertion's oracle test
   carries `oracle: docs/design/ai/<spec>.fizz#<Assertion>` for the spec that holds it.

**Definition of done**: the tests pass; `just check-specs` passes; `just ci` passes; no username or absolute path in a
changed file.

## Review checklist

- [ ] A spent cap refuses before any engine call; grants are held at admission and released at the end.
- [ ] The rebuild reads only the rows of each cap's current window; calls wait for it.
- [ ] Rows hold no prompt or answer text; the span and the metrics hold none either.
- [ ] `X-Funcd-Cap` is set exactly when a cap lowered or refused the call.
- [ ] Each assertion in the Design model has its oracle marker; `just check-specs` passes.
- [ ] One test per scenario, named for it.

## Consequences

- Under a tokens cap, a call with no limit of its own and none from its model ends at 4,096 completion tokens
  (`ai.defaultMaxCompletionTokens`), reasoning tokens included, with `finish_reason: "length"`.
- Prompt tokens count when a call ends, so a cap can pass its ceiling by the prompts of the calls in flight.
- A daemon crash or a hard stop loses the queued rows and the calls in flight; the caps then count less than was used.
- An abandoned stream's estimate can be off, and its row says so.
- A function that writes to the usage database (one shared through `ref`, or an App's) can change the rows the caps
  count from; the change takes effect at the next rebuild.

## Open questions

None.

## References

- [ADR-0282](0282-openai-api-from-functions.md), [ADR-0206](0206-restore-and-held-boot.md),
  [ADR-0186](0186-containerd-clean-slate-at-boot.md); issue #1270.
- OpenRouter's usage accounting and generation lookup; Bifrost core v1.13.1 `BifrostCost.TotalCost` (design report).
