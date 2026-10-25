# ADR-0282: OpenAI's API from functions, through any OpenAI client

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged twice; the first round added the async Python client)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, data-plane, authorization, shims
- **Realizes**: [FEAT-0015/F195](../feat/0015-feat-ai-gateway.md) (OpenAI's API from functions)
- **Relates to**: [ADR-0281](0281-ai-gateways.md) (the gateway and its engine) ·
  [ADR-0280](0280-the-model-engine-on-bifrosts-go-library.md) (the engine's routes) ·
  [ADR-0233](0233-sql-access-from-functions.md) (the pattern of access through the facade) ·
  [ADR-0069](0069-kv-data-plane.md) (the worker-node local API) · [ADR-0074](0074-cedar-authorization-resource-access.md),
  [ADR-0116](0116-capability-authorization-framework.md) (Cedar) · [ADR-0226](0226-stuck-calls-deadline-and-stop-ladder.md)
  (a call's deadline) · [ADR-0246](0246-bindings-under-spec.md) (bindings) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md)
  (shim releases) · source: [design report](../reports/ai-gateway-design.md) · issue #1269

## Context & Need

Agent code and its frameworks already speak OpenAI's API (FEAT-0015 F195). This ADR lets a function call a gateway's
models with any OpenAI client, from TypeScript and Python, over its own Unix socket, with no key and no vendor SDK in
its sandbox. A binding names a gateway's model under an alias, the alias is the request's `model`, and the runtime's
helper hands the client its settings. The daemon is the policy point, as for SQL (ADR-0233): it resolves the alias,
asks Cedar, adds the model's system prompt and passes the call to the gateway's engine in OpenAI's format.

## Scenarios

- `scenario: function-chats-through-gateway` — Given Function `triage` binding alias `chat` to gateway `assistant`'s
  model `fast`, When its code calls `chat.completions.create({model: "chat", …})` on `new OpenAI(ctx.ai.openai())`,
  Then it gets the vendor's answer, and its sandbox holds no key.
- `scenario: function-streams-chat` — When the call sets `stream: true`, Then the chunks reach the code as the vendor
  sends them, with no `usage` field unless the call set `stream_options.include_usage`.
- `scenario: function-embeds` — Given alias `embed` on an embedding model, When the code calls `embeddings.create`,
  Then it gets the vectors.
- `scenario: python-function-calls-gateway` — Given a Python function, When it builds
  `OpenAI(**openai_settings(context))`, or `AsyncOpenAI(**openai_settings(context, asynchronous=True))` inside its own
  event loop, and chats, streams and embeds, Then it gets the same answers.
- `scenario: unbound-alias-refused` — When the code names a model that no binding of the Function names, Then the call
  fails 403 in OpenAI's error shape and no engine is called.
- `scenario: policy-forbids-model` — Given an operator Policy forbidding `ai::call` when `context.model == "deep"`,
  When the Function calls its alias bound to `deep`, Then it fails 403, while its alias bound to `fast` works.
- `scenario: system-prompt-applied` — Given model `fast` with a system prompt in ConfigMap entry `SYSTEM`, When a call
  sends no system message, Then the engine receives that prompt first; a call with its own system message keeps it.
- `scenario: abandoned-stream-stops-at-vendor` — When the code stops reading a stream and closes it, Then the vendor's
  request is closed within 2 s.
- `scenario: oversized-request-refused` — When a request body exceeds `ai.maxRequestBytes`, Then it fails 413 and no
  engine is called.
- `scenario: delete-refused-while-bound` — Given a Function that binds `assistant`, When a client deletes the gateway,
  Then the API answers 409 and the gateway stays.
- `scenario: workflow-step-binds-model` — Given a Workflow step whose bindings name alias `chat`, When the step runs,
  Then its code chats through the gateway as a Function does.

## Scope

**In**: the binding; the local API routes and streaming; the facade; the system prompt; Cedar `ai::call` and its
context; the deletion protection; the dependency check; the shim helpers.

**Out**: the caps, the token limit's value and the usage rows ([ADR-0283](0283-ai-limits-and-usage.md), which hooks
into the facade); Apps ([ADR-0284](0284-ai-gateways-in-apps.md)); callers outside funcd (FEAT-0015, out of scope).

## Constraints & Decision drivers

- OpenAI's API only; no SDK bundled; the alias is `model` (decided). Retries happen only in the engine, so the
  helper sets the client's retries to 0 (decided).
- The Python shim stays stdlib-only (ADR-0049): the helper lives in a separate module (decided).
- Prompts and answers never enter logs, telemetry or audit (decided).

## Alternatives considered

1. **funcd's own `ctx.ai.chat/stream/embed`**: one shape for both runtimes, but every agent framework would need an
   adapter. Dropped by the decider for OpenAI's API.
2. **Bundling the OpenAI SDK in the shims**: no dependency to add, but it pins a version for every function and grows
   every worker. Rejected (decided).
3. **httpx in the Python shim itself**: measured at +13 MiB and about 28 ms per Python worker against +3.9 MiB for the
   shim's stdlib imports. Rejected for a separate module only the callers import.
4. **Passing the usage chunk through always**: a function that did not ask for it would see a chunk it does not
   expect. Rejected (decided: only on `include_usage`).

## Decision

1. **The binding.** `spec.bindings.ai[]` on a Function, and on a Workflow step (copied to its Function by the
   materializer), holds `{alias, gateway, model}`: an alias unique among the Function's `ai` bindings, a gateway of its
   namespace and one of that gateway's model names. A gateway is not a store, so no root entry declares it (ADR-0248
   Decision 2 names stores, ConfigMaps and Secrets). A Function bound to a missing gateway waits Pending with
   `AIGatewayNotFound`, as SQL's `SQLDatabaseNotFound` (`internal/function/references.go`), and one bound to a model
   the gateway lacks with `AIModelNotFound`. A gateway update that drops a bound model is not refused: calls to it
   fail NotFound until the binding or the gateway changes.
2. **The routes.** The worker-node local API serves `POST /ai/v1/chat/completions` and `POST /ai/v1/embeddings`, with
   the caller's identity from the socket (ADR-0069). The body is OpenAI's request; a body over `ai.maxRequestBytes`
   (`funcdconfig.yaml`, `FUNCD_AI_MAX_REQUEST_BYTES`, default 32 MiB) fails 413.
3. **The facade.** For each call, in order:
   1. resolve `model` through the caller's `bindings.ai` (a miss is Forbidden; hits are cached 5 s, as KV and SQL);
   2. ask Cedar `ai::call` on `AIGateway::"<ns>/<gateway>"` with the socket's principal and context
      `{"model": "<the gateway's model name>"}`;
   3. find the model in the gateway, of the route's type (otherwise NotFound), and the engine in ADR-0281's `Engines`
      registry (absent ⇒ Unavailable); ADR-0283's cap check runs here;
   4. when the model has a `system` prompt and the request's `messages` hold no `system` or `developer` message, put
      `{"role": "system", "content": <prompt>}` first. A ConfigMap entry is read from the store; a blob object is read
      as the gateway's principal through `s3-read-binding` (ADR-0281 Decision 10) and kept in memory with its ETag:
      each call stats the object and reads it again only when the ETag changed. A missing source fails Unavailable naming it; a request that the prompt grows
      past `ai.maxRequestBytes` fails 413;
   5. replace `model` with the gateway's model name and post the body to the engine's route with `Authorization:
      Bearer <EngineToken>`, the caller's `traceparent`, a random `X-Funcd-Call-Id` (ADR-0280's cancel route) and
      ADR-0283's `X-Funcd-Max-Tokens`.
4. **Answers.** A whole answer passes through with its status; its `model` is the engine's, the vendor's model id, as
   OpenAI answers with the model it used. A stream passes through event by event, flushed as each arrives; unless the
   request set `stream_options.include_usage`, the facade removes `usage` from each event and drops an event that this
   left with no choices. The facade hands the final usage to ADR-0283 either way. When the function closes a stream
   before its end, the facade posts the engine's cancel route and reads the stream on for ADR-0283 (its Decision 7);
   when a whole call's request ends, it closes the engine connection. Either cancels the vendor's request (ADR-0280).
   The facade adds no timeout of its own: the gateway's `timeout` and idle limit bound the vendor call, and the
   client's own timeout ends the function's side.
5. **Errors.** funcd's own refusals use OpenAI's error shape, `{"error": {"message", "type", "code"}}`, with the fault's
   status (`api/fault/problem.go`): `type` is `funcd` (`funcd_cap` for a spent cap, ADR-0283) and `code` is the problem
   slug with underscores (`forbidden`, `not_found`, `unavailable`, `payload_too_large`). The engine's errors pass
   through with their status and body. An engine that refuses the connection fails Unavailable; an engine answering 401
   fails Internal, logged without the token. No log, span or audit record holds a prompt or an answer.
6. **Cedar.** A capability `ai`: action `ai::call`, entity type `AIGateway` (ADR-0281), principal attribute
   `aiBindings` from `spec.bindings.ai`, and `builtin_ai.cedar` with the `ai-call-binding` permit; it joins the root
   registry (`pkg/funcd/funcd.go`) and `defaultRegistry` (`internal/auth/cedar/schema.go`), and `AIGateway` joins
   `namespacedEntityType`. The action's context is `{model: String}`: `auth.Request` gains `Context map[string]string`,
   which the Cedar driver passes as the request context; every other action passes none.
7. **Deletion protection.** `aigateway-deletion-protection` refuses an API delete with 409 while a Function's or a
   Workflow step's `bindings.ai` names the gateway, as ADR-0233 Decision 7 does for SQL.
8. **The dependency check** (ADR-0215). An `ai` binding is checked like a SQL one: resolve, `ai::call`, then the
   engine's presence in the registry; a missing engine reports kind `ai`, reason `EngineNotReady`; `CheckerDeps`
   gains `AI`.
9. **The shims**, each released in its language repo and pinned by a funcd bump (ADR-0141):
   - funcd-typescript: `ctx.ai.openai()` returns OpenAI's client options: `baseURL` `http://funcd/ai/v1`, `apiKey`
     `funcd` (a placeholder the facade ignores), a `fetch` that sends each request over `FUNCD_INVOKE_SOCKET` as
     `socket.ts` does and streams the body, `maxRetries` 0, `timeout` the time left before the call's deadline when the
     helper is called (unset with no deadline), and `defaultHeaders` with `X-Funcd-Member` in a pool;
   - funcd-python: `funcd_shim.ai.openai_settings(context, asynchronous=False)` returns `base_url`, `api_key`,
     `http_client` (an `httpx.Client` over the socket, `httpx.HTTPTransport(uds=…)`, or with `asynchronous=True` an
     `httpx.AsyncClient` over `httpx.AsyncHTTPTransport(uds=…)` for `AsyncOpenAI`, with the member header in a pool),
     `timeout` in seconds and `max_retries` 0. The module imports httpx, which the function's own `openai` package
     brings; nothing else in the shim imports it.

## Design model

None. The facade adds a request path with no state shared between calls; the one cached value, a blob prompt, is
read again whenever its ETag changes, and a call that races a prompt update uses either version. The races between
concurrent calls and the caps are modeled with [ADR-0283](0283-ai-limits-and-usage.md).

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: triage
  namespace: team-a
spec:
  bindings:
    ai:
      - alias: chat
        gateway: assistant
        model: fast
      - alias: embed
        gateway: assistant
        model: embed
```

```go
// api/types/v1alpha1/bindings.go: FunctionBindings and StepBindings gain
AI []FunctionAI `json:"ai,omitempty"`

type FunctionAI struct {
	Alias   string     `json:"alias"`
	Gateway ObjectName `json:"gateway"`
	Model   string     `json:"model"`
}

// internal/auth: Request gains
Context map[string]string // the Cedar request context; only ai::call sets it ({"model": …})

// internal/workernode/local/ai.go
// AI is the function-facing AI port the local API routes to; services/ai.Facade satisfies it. The caller's namespace
// and function come from the sandbox's fixed Ref, never from the request, as for SQL (ADR-0233).
type AI interface {
	// Serve answers one OpenAI request; route is "chat/completions" or "embeddings". A refusal before any byte is
	// written returns an error, which the local API writes in OpenAI's error shape.
	Serve(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, route string, body []byte, w http.ResponseWriter) error
}

// internal/services/ai/facade.go
type FacadeDeps struct {
	Resolver        Resolver // the caller's bindings.ai; hits cached 5 s
	Authorizer      auth.Authorizer
	Store           store.Store  // gateways and ConfigMap prompts
	Prompts         PromptReader // blob prompts, read as the gateway's principal
	Engines         *Engines
	Master          []byte
	Caps            Caps // ADR-0283; nil ⇒ no caps
	MaxRequestBytes int64
	HTTPClient      *http.Client
	Logger          *slog.Logger
}

func NewFacade(d FacadeDeps) (*Facade, error)

// Caps is the hook ADR-0283 implements: Admit runs before the engine call, Record after it.
type Caps interface {
	Admit(ctx context.Context, call Call) (maxTokens int, err error) // 0: no token limit
	Record(ctx context.Context, call Call, usage Usage)
}
```

```ts
// funcd-typescript: FunctionContext gains
ai: { openai(): ClientOptions }; // OpenAI's ClientOptions type, structurally: baseURL, apiKey, fetch, timeout, maxRetries, defaultHeaders
```

```python
# funcd-python: funcd_shim/ai.py
def openai_settings(context: FunctionContext, asynchronous: bool = False) -> dict: ...  # for OpenAI or AsyncOpenAI
```

| Consumes | Exposes |
|---|---|
| ADR-0281's `Engines`, `EngineToken` and principal; the PDP; the blob store | `POST /ai/v1/chat/completions`, `POST /ai/v1/embeddings` |
| config `ai.maxRequestBytes` | the binding, `ai::call` with context `model`, `AIGatewayNotFound` |
| | `ctx.ai.openai()`, `funcd_shim.ai.openai_settings` |

`Call` (namespace, function, gateway, model, type, start time) and `Usage` (tokens in and out, cost, outcome,
duration, estimated) are plain structs that ADR-0283 completes.

## Implementation plan

1. `api/types/v1alpha1/bindings.go` (with `Validate`), the materializer copy, `internal/function/references.go`
   (`AIGatewayNotFound`), `just generate`.
2. `internal/workernode/local/ai.go`: the routes and the `AI` port; `internal/services/ai/facade.go`: the facade, the
   stream filter, the prompt reader; config `ai.maxRequestBytes` in `internal/platform/config`.
3. `internal/auth`: `Request.Context`, the driver's context; `internal/auth/cedar`: the capability, `builtin_ai.cedar`,
   the schema's action context; the deletion admission; the dependency check.
4. The shims: funcd-typescript and funcd-python PRs and releases, then `go get` here (ADR-0141); an example function in
   funcd-functions for the lane.
5. Tests: unit tests of the facade's order, the stream filter (with and without `include_usage`) and the error shape;
   one `TestScenario<Name>` per scenario in `pkg/funcd`, against the real engine binary and the suite's
   OpenAI-compatible server from ADR-0280; `python-function-calls-gateway` and the TypeScript scenarios through the
   real shims in an e2e lane.

**Definition of done**: the tests pass; both shims are released and pinned; the lane runs the language scenarios once;
`just ci` passes; no username or absolute path in a changed file.

## Review checklist

- [ ] A missing binding, a Cedar deny or an oversized body never reaches the engine.
- [ ] The engine token and address never reach the sandbox; the function's `Authorization` header is ignored.
- [ ] `usage` reaches a stream's code only with `include_usage`; ADR-0283's hook receives it either way.
- [ ] The Cedar context is set only for `ai::call`, and an operator Policy on `context.model` validates.
- [ ] No prompt or answer text in a log, a span or an audit record.
- [ ] One test per scenario, named for it.

## Consequences

- An agent framework that takes an OpenAI client, sync or async, runs on funcd with the client built from the helper.
- A Python function that calls a gateway needs the `openai` package, and with it httpx.
- The helper's `timeout` is fixed when it is called: a client built early in a call and used late keeps the earlier
  bound, which the function's deadline still caps.

## Open questions

None.

## References

- [ADR-0233](0233-sql-access-from-functions.md), [ADR-0215](0215-built-in-health.md); openai-python and openai-node
  client options (design report, Sources); issue #1269.
