# ADR-0280: The model engine, funcd's own binary on Bifrost's Go library

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged three times; the second round replaced the usage route with the cancel route)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, provider, engine, image
- **Realizes**: [FEAT-0015/F193](../feat/0015-feat-ai-gateway.md) (the model engine)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md), [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md),
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (engines and their images) · [ADR-0007](0007-blob-storage-layer-port.md)
  (a conformance suite) · [ADR-0010](0010-observability-telemetry-and-audit.md) (telemetry) ·
  [ADR-0260](0260-a-loopback-listener-owns-its-port.md) (dev ports) · source: [design report](../reports/ai-gateway-design.md) · issue #1267

## Context & Need

Functions on funcd call language and embedding models through an `AIGateway` (FEAT-0015). The decider chose, on
2026-10-24, one add-on engine per gateway, built by funcd on Bifrost's Go library (`github.com/maximhq/bifrost/core`):
the engine is the only process that holds the vendors' keys and dials the vendors, and a panic on one of Bifrost's
goroutines ends that engine, not the daemon. The daemon calls the engine in OpenAI's format.

This ADR builds that engine: its Go module, binary and image, configuration, routes, readiness, telemetry, memory
bound and conformance suite. The kind and the reconciler that start one engine per gateway are
[ADR-0281](0281-ai-gateways.md); this ADR gives that reconciler an image and a configuration to fill.

## Scenarios

The caller is the daemon; "the server" is the suite's scripted OpenAI-compatible server.

- `scenario: engine-chats-whole` — Given chat model `fast` (vendor `openai`, a key, `url` the server), When the daemon
  posts a chat completion for `fast` with the engine token, Then it gets OpenAI's chat completion with the usage.
- `scenario: engine-streams-chat` — When the call sets `stream: true`, Then the server's chunks arrive in order as
  server-sent events, the last one carries the usage, and the stream ends with `data: [DONE]`.
- `scenario: engine-embeds` — Given embedding model `embed`, When the daemon posts an embedding call, Then it gets
  OpenAI's embedding response with the server's vectors.
- `scenario: engine-applies-model-params` — Given `fast` with `temperature: 0.2` and `maxCompletionTokens: 800`, When a
  call sets `temperature: 0.9` only, Then the server receives `temperature` 0.9 and `max_completion_tokens` 800.
- `scenario: engine-lowers-token-limit` — When a call asks for 800 completion tokens with `X-Funcd-Max-Tokens: 100`,
  Then the server receives `max_completion_tokens` 100.
- `scenario: engine-refuses-without-token` — When a call carries no token or a wrong one, Then it gets 401 and the
  server receives nothing; an engine started with an empty or short token exits 1.
- `scenario: engine-readiness-needs-keys` — Given a model whose key variable is unset, Then `/readyz` answers 503
  naming it, and with the variable set, 200; the server receives nothing.
- `scenario: engine-retries-transient` — Given `maxAttempts` 3, When the server answers 429 twice and then 200, Then
  the daemon gets one answer from three requests; with `maxAttempts` 1 it gets the 429.
- `scenario: engine-cancels-vendor-call` — When the daemon posts `/funcd/cancel/<call id>` during a stream and reads
  on, Then the server sees its request closed within 2 s, and the stream ends with the usage the server had reported.
- `scenario: engine-idle-limit` — Given `streamIdleTimeout` 1s, When the server stops sending mid-stream, Then the
  stream ends with an error event within 3 s.
- `scenario: engine-keeps-content-out-of-telemetry` — After a call and after a vendor error, `/funcd/telemetry` holds
  their spans with the model and token counts, and no prompt, answer or vendor message text.
- `scenario: engine-prints-its-address` — Given `FUNCD_AI_ADDR=127.0.0.1:0`, When the binary starts, Then its first
  output line names the address it bound, and `/readyz` answers there.

## Scope

**In**: the nested module `engines/ai/`; the binary `funcd-ai` and the image `funcd/runtime-ai`; the configuration;
the routes; the token check; the vendor mapping; the telemetry ring; the memory bound; the conformance suite; the
review of Bifrost's dependency licenses.

**Out**: the kind, its reconciler, the engine token's derivation, egress and the panic's isolation lane
([ADR-0281](0281-ai-gateways.md)); the function route, the binding, Cedar and the system prompt
([ADR-0282](0282-openai-api-from-functions.md)); the caps, the usage rows and the export of the telemetry
([ADR-0283](0283-ai-limits-and-usage.md)); Apps ([ADR-0284](0284-ai-gateways-in-apps.md)); `funcdctl dev`
([ADR-0285](0285-ai-gateways-in-funcdctl-dev.md)); moving the existing engines into `engines/` (a later refactor).

## Constraints & Decision drivers

- The daemon's `go.mod` takes none of Bifrost's modules and keeps its Go version (decided 2026-10-24).
- Bifrost's server, stores and plugins are not used; the engine keeps no state beyond a call (decided).
- Prompts and answers never enter logs, telemetry or audit (decided).
- No fake engine anywhere: tests run the real engine against a local OpenAI-compatible server (decided).
- An experiment during this ADR measures the engine's memory and sets its default limit (decided).

## Alternatives considered

1. **funcd's own OpenAI-compatible client** (C1): no new modules, but it reaches only vendors that speak OpenAI's API.
   The decider chose Bifrost's library (C2) on 2026-10-24 for the vendors' own APIs.
2. **Bifrost's HTTP gateway** (`transports`) as the engine: ready-made, but 237 requirements, its own config and log
   stores, and plugins that log content by default. Rejected: the core's OpenAI converters give the same API.
3. **Configuration in a mounted file**: the add-on runtime hands an engine environment variables (`ProviderSpec.Env`)
   and no file. Rejected: the configuration holds no prompt, so it stays far below Linux's 128 KiB per variable.
4. **The engine exports OTLP itself**: funcd's pipeline lives in the daemon (ADR-0010), and the decided Monitoring row
   hands the engine's spans and logs to the daemon. Rejected for a ring the daemon reads.

## Decision

1. **The module.** `engines/ai/go.mod` declares `module github.com/pyvvo/funcd/engines/ai` and `go 1.27.3`, requires
   `github.com/maximhq/bifrost/core v1.13.1`, carries golangci-lint as a `tool`, and imports nothing from the root
   module. Recipes run its Go commands with `GOTOOLCHAIN=auto`, which fetches go1.27.3 checked against Go's checksum
   database, as `scripts/build-sql-engine.sh` does for rqlite. Its binaries are `cmd/funcd-ai` and
   `cmd/funcd-ai-vendortest`, the suite's server for the root module's tests.
2. **The licenses.** All 80 modules that `core` v1.13.1 requires are under Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause
   or ISC (deps.dev, 2026-10-25), and Bifrost's own module is Apache-2.0. A Bifrost upgrade repeats this check and the
   log call-site check of Decision 7, and reruns the suite.
3. **The configuration**, read once at start; anything invalid exits 1 with the reason before the engine listens:
   - `FUNCD_AI_CONFIG`: the JSON of `engine.Config`: the models, `timeout` (300s), `streamIdleTimeout` (120s) and
     `retry` (`maxAttempts` 3, `backoff` 500ms), each default applied when empty;
   - `FUNCD_AI_TOKEN`: the token every route but `/readyz` requires, at least 32 bytes;
   - `FUNCD_AI_ADDR`: the listen address, `0.0.0.0:8080` when unset;
   - each model's key, in the variable its `keyEnv` names.
4. **The vendors.** `vendor` names a Bifrost provider (`schemas.ModelProvider`). The engine registers each vendor once
   with 64 workers and a queue of 64 (`ConcurrencyAndBufferSize`) and `DropExcessRequests`, so a call that finds its
   vendor's queue full gets Bifrost's 503 at once. Each model adds a Bifrost `Key` whose `Value` is its key and whose
   `Models` lists its `id`. A model's `url` becomes its vendor's `NetworkConfig.BaseURL`, or the key's URL for
   `ollama`, `vllm` (with `ModelName`, the `id`) and `sgl`; a vendor whose models name a `url` gets
   `AllowPrivateNetwork`. The configuration is invalid when:
   - a vendor is unknown, or needs a setting a model does not declare: `azure`, `bedrock`, `bedrock_mantle`,
     `vertex`, `databricks`, `cloudflare` and `github-copilot`; `ollama`, `vllm` and `sgl` require a `url`;
   - a model has no `keyEnv` and its vendor is not `ollama`, `vllm` or `sgl`, the self-hosted servers;
   - two models share a name; two models of one vendor other than those three name different `url`s; or two models
     of one vendor share an `id` without sharing their `url` and `keyEnv` (an unset value counts as one), since
     Bifrost picks a key by model;
   - a `params` name or value type is outside its type's list (Decision 6), or a type is neither `chat` nor
     `embedding`.
5. **The routes.** Every route but `/readyz` requires `Authorization: Bearer <FUNCD_AI_TOKEN>`, compared in constant
   time; without it the engine answers 401 and calls nothing.
   - `POST /v1/chat/completions` and `POST /v1/embeddings` take OpenAI's request, with `model` a model name of the
     route's type (otherwise 404 `model_not_found`). The engine applies Decision 6 to the request's JSON, converts it
     with `openai.OpenAIChatRequest.ToBifrostChatRequest` or `OpenAIEmbeddingRequest.ToBifrostEmbeddingRequest`, calls
     Bifrost and writes its response without `extra_fields`. With `stream: true` it writes each chunk as a server-sent
     event as Bifrost delivers it, then `data: [DONE]`; the last chunk carries the usage, which Bifrost always asks
     the vendor for. When the daemon closes the connection, the engine cancels the Bifrost request and the vendor's.
   - A Bifrost error answers its status (502 when it has none) with `{"error":{"message","type","code"}}`; an error
     after a stream started is one last event of that shape. `SendBackRawRequest` and `SendBackRawResponse` stay
     false, and no error carries `raw_request` or `raw_response`.
   - `GET /readyz` answers 200 when every `keyEnv` has a non-empty value, else 503 with `{"missingKeys":["<model>"]}`.
   - `POST /funcd/cancel/<call id>` names a stream that carried `X-Funcd-Call-Id`. The engine cancels its Bifrost
     request and the vendor's, drains Bifrost's stream for the billed usage Bifrost reports after a cancel, else, for
     `openrouter`, asks OpenRouter's `GET /api/v1/generation?id=<the stream's id>` with the model's key (native token
     counts and `total_cost`; three tries 1 s apart, each bounded at 2 s, so the lookup ends
     within 8 s). It then ends that stream with one last event carrying the
     usage it found, or none, and `data: [DONE]`. The route answers 204, or 404 for a call it does not hold. A
     connection the daemon closes also cancels its call, with no usage event.
   - `GET /funcd/telemetry?after=<seq>` answers `{"instance","records","next","dropped"}` (Decision 7).
6. **Params and the token limit.** `params` keys are OpenAI's parameters in camelCase, checked for name and JSON
   type: chat models take `temperature`, `topP`, `presencePenalty` and `frequencyPenalty` (numbers),
   `maxCompletionTokens` and `seed` (integers), `stop` (a string or strings) and `reasoningEffort` (a string);
   embedding models take `dimensions` (an integer). The engine first renames a request's `max_tokens` to
   `max_completion_tokens` when the latter is absent, then sets each param's snake_case name (`maxCompletionTokens` →
   `max_completion_tokens`) when the request does not set it. With `X-Funcd-Max-Tokens: <n>` it then sets
   `max_completion_tokens` to the smaller of `n` and the current value, or to `n` when there is none.
7. **Network settings and telemetry.** For each vendor: `DefaultRequestTimeoutInSeconds` and
   `StreamIdleTimeoutInSeconds` from `timeout` and `streamIdleTimeout`, rounded up to whole seconds; `MaxRetries` =
   `maxAttempts` − 1; `RetryBackoffInitial` = `backoff`; `RetryBackoffMax` = the larger of `backoff` and 5 s. Bifrost
   retries what it classes as transient. The engine implements `schemas.Logger` and `schemas.Tracer` onto a ring of
   the latest 1,024 records:
   - a log record keeps its level (Info and above) and its message up to the first `: `, with arguments dropped:
     Bifrost builds some messages from a vendor's text (`Warn("Error in stream: " + …)` in `core/providers/utils`);
   - a span record keeps its name, times, status and IDs, joins the trace of the request's `traceparent`, and keeps
     only `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.response.model`,
     `gen_ai.response.finish_reasons`, `gen_ai.response.time_to_first_chunk`, `gen_ai.usage.input_tokens`,
     `gen_ai.usage.output_tokens`, `gen_ai.usage.total_tokens`, `gen_ai.usage.cost` and `gen_ai.error.code`;
   - every answer names the engine's `instance`, a random id drawn at start; a read whose `after` comes from another
     instance, or lies past the newest record, starts at the oldest record kept; records overwritten before a read
     are counted in `dropped`.
8. **Process.** On SIGTERM the engine stops listening, cancels the calls in flight and exits 0. A panic on a Bifrost
   goroutine ends the process like a kill; the add-on runtime starts a new engine on the next supervision pass
   (ADR-0142), and ADR-0281's lane proves that no other gateway or worker stops. Once it listens, the engine writes
   `funcd-ai listening on <addr>` as its first line of standard output.
9. **The image.** `engines/ai/Dockerfile` builds `funcd-ai` with `CGO_ENABLED=0` and `-trimpath` in
   `golang:1.27.3-alpine` pinned by digest, onto the `gcr.io/distroless/static-debian12` digest of rqlite's image,
   as `USER 65532:65532`. `just build-runtime-images` saves it as `funcd/runtime-ai` to
   `internal/runtime/embedimg/ai.tar`, under the runtime name `ai`.
10. **Memory.** Experiment 0011 ran Bifrost core v1.13.1 with Decision 4's settings: 16.9 MiB idle, and 88.3 MiB at
    its peak with 64 workers streaming on each of two vendors (medians of 5 samples, arm64), so about 36 MiB per busy
    vendor. The engine's default memory bound, which ADR-0281 applies, is 32 MiB plus 48 MiB per vendor of the gateway
    (128 MiB for two): the idle figure and each vendor's peak, with room for the HTTP serving and the TLS that the
    probe did not measure.

## Design model

No spec of its own. The cancel route's lookup, three tries 1 s apart, races the daemon's bound on its read; both are
modeled in ADR-0283's [`docs/design/ai/abandoned-call.fizz`](../design/ai/abandoned-call.fizz), whose
`EngineUsageIsRecorded` fails when the stream ends without the usage event or the bound is shorter than the lookup.
Otherwise the engine keeps no state between calls but the telemetry ring. Each call is one Bifrost request whose
timeout, idle limit and retries are Bifrost's mechanisms, configured (a library choice). The telemetry protocol
promises no delivery: a lost record is counted in `dropped`, and a restart shows through `instance`. The vendor rules
that ADR-0281's admission must agree with are held by the shared `testdata/config` cases and ADR-0281's vendor table.

## Temporary workarounds

None.

## Contracts

```go
// engines/ai/engine/config.go
type Config struct {
	Models            []Model `json:"models"`
	Timeout           string  `json:"timeout,omitempty"`           // a Go duration; "300s" when empty
	StreamIdleTimeout string  `json:"streamIdleTimeout,omitempty"` // "120s" when empty
	Retry             Retry   `json:"retry"`
}

type Model struct {
	Name   string                     `json:"name"`
	Type   string                     `json:"type"` // "chat" or "embedding"
	Vendor string                     `json:"vendor"`
	ID     string                     `json:"id"`
	URL    string                     `json:"url,omitempty"`
	KeyEnv string                     `json:"keyEnv,omitempty"`
	Params map[string]json.RawMessage `json:"params,omitempty"`
}

type Retry struct {
	MaxAttempts int    `json:"maxAttempts,omitempty"` // 3 when 0
	Backoff     string `json:"backoff,omitempty"`     // "500ms" when empty
}

// ParseConfig decodes and validates a configuration with the defaults applied (Decisions 3, 4 and 6).
func ParseConfig(raw []byte) (Config, error)

// engines/ai/engine/engine.go
type Engine struct{ /* unexported */ }

// New starts Bifrost for cfg, reading each model's key through getenv; it refuses a token shorter than 32 bytes.
func New(cfg Config, token string, getenv func(string) string, rec *Recorder) (*Engine, error)
func (e *Engine) Handler() http.Handler
func (e *Engine) Close()

// engines/ai/engine/telemetry.go
type Record struct {
	Seq        uint64         `json:"seq"`
	Kind       string         `json:"kind"` // "log" or "span"
	Time       time.Time      `json:"time"`
	End        time.Time      `json:"end,omitzero"`
	Level      string         `json:"level,omitempty"`
	Message    string         `json:"message,omitempty"`
	Name       string         `json:"name,omitempty"`
	TraceID    string         `json:"traceId,omitempty"`
	SpanID     string         `json:"spanId,omitempty"`
	ParentID   string         `json:"parentId,omitempty"`
	Status     string         `json:"status,omitempty"` // "ok" or "error"
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Recorder struct{ /* a ring of records with an instance id; safe for concurrent use */ }

func NewRecorder(size int) *Recorder
func (r *Recorder) Logger() schemas.Logger
func (r *Recorder) Tracer() schemas.Tracer
func (r *Recorder) After(instance string, seq uint64) (records []Record, next, dropped uint64)
func (r *Recorder) Instance() string
```

| Consumes | Exposes |
|---|---|
| `FUNCD_AI_CONFIG`, `FUNCD_AI_TOKEN`, `FUNCD_AI_ADDR`, the key variables | the routes of Decision 5; `X-Funcd-Max-Tokens`, `X-Funcd-Call-Id` |
| `github.com/maximhq/bifrost/core` v1.13.1 | the image `funcd/runtime-ai`, runtime name `ai`; `funcd-ai-vendortest` |

## Implementation plan

1. `engines/ai/go.mod`, `go.sum`; `cmd/funcd-ai`; `engine/` (`config.go`, `vendors.go`, `params.go`, `engine.go`,
   `cancel.go`, `telemetry.go`); `internal/vendortest/` and `cmd/funcd-ai-vendortest`, the scripted server (whole,
   stream, embeddings, N failures with a status, a stall, a closed request, a generation record).
2. `engines/ai/testdata/config/`: valid and invalid configurations, one per Decision 4 and 6 rule, each with its
   expected error; ADR-0281's `Validate` runs the same files.
3. Tests: `ParseConfig` over `testdata/config`; the params and token-limit merge; the telemetry allow-list, the
   message cut and the instance rule; one `TestScenario<Name>` per scenario in `engine/conformance_test.go` against
   `vendortest` with vendors `openai`, `openrouter`, `ollama` and `vllm`, except `engine-applies-model-params` and
   `engine-lowers-token-limit`, which assert `max_completion_tokens` with `openai` and `openrouter` only (Bifrost sends
   Ollama `max_tokens`); `engine-prints-its-address` builds and runs the binary.
4. `engines/ai/Dockerfile`; `justfile`: `test-ai-engine` (vet, the module's golangci-lint tool and `go test -race
   ./...` in `engines/ai`, part of `just ci`), the image in `build-runtime-images`, `embedimg` (`ai.tar` placeholder,
   `tarFile` entry), and `smoke-ai-image` (`/readyz` with and without a key, a 401 without the token, the image's uid).

**Definition of done**: the tests pass with `-race`; `just smoke-ai-image` passes once on a host with docker; `just ci`
passes; no username or absolute path in a changed file.

## Review checklist

- [ ] `engines/ai/go.mod` requires Bifrost core v1.13.1, and the root `go.mod` gained no requirement.
- [ ] Every route but `/readyz` refuses a missing or wrong token before any Bifrost call; an empty or short
      `FUNCD_AI_TOKEN` exits 1.
- [ ] Each Decision 4 and 6 rule has a `testdata/config` case, and the engine exits 1 on each invalid one.
- [ ] No key value in an answer, a record or the engine's output; no `extra_fields`, `raw_request` or `raw_response`.
- [ ] Records keep only Decision 7's attributes; log records keep no arguments and no text after the first `: `.
- [ ] Vendors run with 64 workers, a queue of 64 and `DropExcessRequests`.
- [ ] The image runs as 65532, pins every input by digest, and holds `funcd-ai` and nothing to run a shell.
- [ ] One test per scenario, named for it.

## Consequences

- Vendor clients run in their own process: a panic, or a vendor answer Bifrost cannot parse, ends one engine.
- A key change recreates the engine (ADR-0281), and SIGTERM cuts every stream of that gateway in flight.
- More than 128 calls in flight to one vendor of a gateway get 503 until workers free up.
- A second Go toolchain, go1.27.3, builds one module; the embedded image grows funcd's binary by its compressed size.
- Vendors whose keys need more than a key and a URL (Azure, Bedrock, Vertex and others) are not reachable yet.

## Open questions

- Vendors whose keys need more settings, such as a region or a project: a later ADR, tracked on the board.

## References

- Bifrost core [v1.13.1](https://github.com/maximhq/bifrost/tree/core/v1.13.1) (tag commit `df95547`, 2026-10-20); the
  source was read at `4810249`, whose `core/go.mod` is identical: `core/schemas/account.go`, `provider.go`,
  `logger.go`, `tracer.go`, `trace.go`, `core/providers/openai`, `core/providers/utils/utils.go`, `core/utils.go`.
- [deps.dev](https://deps.dev) license data for the 80 required modules, read on 2026-10-25; issue #1267.
- Experiment `0011-ai-engine-memory`, pyvvo/experiment#22 (private), commit `5265e69`: the engine's memory, idle and
  busy.
