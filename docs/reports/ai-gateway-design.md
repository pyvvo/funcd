# AI gateway design: OpenAI's API over an engine built on Bifrost's Go library

- **Status**: Draft (2026-10-24): refined with the decider on 2026-10-24 and 2026-10-25 for
  [FEAT-0015](../feat/0015-feat-ai-gateway.md). The rows marked decided are the decider's choices; the ADRs decide the
  rest, and an Accepted ADR wins over this report.
- **Deciders**: green-0-rabbit

## Summary

funcd gets an AI gateway: a namespace declares an `AIGateway`, and its functions call the gateway's models with any
OpenAI client, from TypeScript and Python. The vendor or the model underneath can change without any change to a
function. Every name in this report is new and unbuilt unless it cites a file or an ADR.

- **An add-on engine per gateway, declared like a Lake or a SQL database.** Applying an `AIGateway` starts its own
  engine through the add-on provider runtime ([ADR-0087](../adr/0087-add-on-provider-runtime.md)), one per gateway, as
  many as the namespaces declare. The daemon keeps the binding checks, Cedar, the caps and the usage rows, and calls
  the engine; only the engine talks to the vendors. SQL already takes this shape: one rqlite engine per database,
  reached through the daemon's facade ([ADR-0232](../adr/0232-sql-database-resource-and-rqlite-engine.md),
  [ADR-0233](../adr/0233-sql-access-from-functions.md)).
- **An engine built on Bifrost's Go library**, funcd's own binary in its own Go module, `engines/ai/`. The daemon's
  `go.mod` takes none of Bifrost's modules and keeps its Go version. Bifrost's server, stores and plugins are not used.
- **OpenAI's API, nothing of funcd's own.** The binding's alias is the model name, and the runtime's helper hands an
  OpenAI client its settings. Tools, images, structured output, streaming and embeddings follow OpenAI's definitions,
  and Bifrost translates them for each vendor.
- **On funcd's own building blocks.** Keys come from Secrets bound to the gateway, system prompts from ConfigMaps and
  blob objects, and usage goes to the gateway's own SQL database. Cedar authorizes each call, and the egress gateway
  fences the engine's outbound calls.
- **Caps of funcd's own**, per gateway and per binding, counted from the usage rows.

## The question: where model calls run

| | A: each function calls the vendor | B: funcd's own engine per gateway (decided) | C: Bifrost inside the daemon |
|---|---|---|---|
| The vendor key | in every sandbox that calls | in the gateway's engine only | in the daemon only |
| What a function loads | a vendor SDK | an OpenAI-format client | an OpenAI-format client |
| Limits and usage per function | none in common | funcd's bindings, Cedar and caps, in the daemon | the same |
| A panic in the vendor client | ends that function's call | ends one engine | ends the daemon; its next start removes every worker ([ADR-0167](../adr/0167-process-worker-crash-recovery.md), [ADR-0186](../adr/0186-containerd-clean-slate-at-boot.md)) |
| Outbound path | each namespace allows the vendor's domain ([ADR-0117](../adr/0117-egress-policy-enforcement.md)) | the engine dials from its own sandbox, through the egress gateway when egress control is on | the daemon dials from the host, outside the workers' egress fence |
| Added footprint | an SDK in every warm worker | one engine per gateway, always on (ADR-0087 pins one replica); its idle memory is not measured | Bifrost's code and modules in the daemon |

The decider chose B on 2026-10-24:

- A puts a credential in the sandbox and leaves no central control, which funcd removed for SQL and vectors.
- C keeps a large third-party library, which parses answers from vendors and from servers that a namespace names, in
  the process that holds every Secret, where a panic on its goroutines stops the daemon.
- Another gateway's server in B's place was set aside first: it keeps its own keys, budgets and database beside
  funcd's ([below](#the-gateways-set-aside)).

Like Lake and SQL engines, the engine runs only on the containerd runtime; the process driver refuses an engine
(`internal/runtime/process/process.go`), and `funcdctl dev` runs engines as host processes. A crashed engine returns
on the next supervision pass, `runtime.supervisionPeriod` ([ADR-0142](../adr/0142-supervision-by-periodic-re-convergence.md)).

## The engine: Bifrost's Go library

The decider chose C2 over C1 on 2026-10-24: Bifrost's Go SDK lets funcd build a gateway that suits the platform, on
funcd's Secrets, rqlite and Unix sockets, while it reaches each vendor's own API.

| | C1: funcd's own OpenAI-compatible client | C2: Bifrost's Go library (decided) |
|---|---|---|
| Vendors | any OpenAI-compatible API: OpenRouter, OpenAI, Ollama, vLLM | 35 provider packages, each speaking a vendor's own API, the OpenAI-compatible ones included |
| Modules it brings | none | 80, 32 of them not in funcd's `go.mod`; they go in the engine's own module |
| Go version | funcd's own (1.26.6) | requires 1.27.3, in the engine's module only |
| Code in the engine | the client's own | about 286,000 lines of non-test Go in its `core` module |
| cgo | none | none |

C2 was measured at commit `4810249` of `maximhq/bifrost` (2026-10-24). What the choice brings:

- **Its own module**: the engine's module, `engines/ai/`, is a nested module, as `bench/badger` and
  `bench/expr-engine` are; it requires Go 1.27.3 or later. Moving the existing engines' images and build scripts into
  `engines/` is a refactor of its own, after this epoch.
- **New modules**: among them the Azure and Google Cloud SDKs, an MCP and an A2A library, a Starlark interpreter and
  the sonic JSON encoder. The `core` package imports every provider package, so they come together, and the license
  of each must meet the project's Apache-2.0/MIT-compatible rule.
- **Panics on Bifrost's goroutines**: Bifrost runs each provider's calls on worker goroutines it starts
  (`requestWorker` in `core/bifrost.go`), and its OpenAI provider reads a stream in a goroutine of its own. Only reads
  and channel sends on the stream path recover a panic; a panic while parsing a chunk, in a plugin hook or in a
  provider's request code ends the process. In its own engine, that ends one engine. This is why the engine runs as an
  add-on.
- **Footprint**: the `core` module holds 514 non-test Go files, 10.9 MiB of source. Each gateway runs one engine,
  always on; its idle memory is not measured yet, and the blueprint asks for low resource consumption.

### How the engine sits on Bifrost

| Bifrost's seam | funcd's building block |
|---|---|
| `Account.GetConfiguredProviders` | the vendors of the gateway's models |
| `Account.GetKeysForProvider` | each model's key, read from the engine's environment, where the add-on runtime put the entries of the gateway's bound Secrets |
| `Account.GetConfigForProvider` | a model's address and the gateway's timeout, idle limit and retries (`NetworkConfig`) |
| OpenAI's request and answer converters (`core/providers/openai`: `ToBifrostChatRequest`, `ToBifrostEmbeddingRequest`) | the engine's OpenAI-compatible handler, which the daemon calls; Bifrost's HTTP server module (`transports/v2`, 237 requirements) is not needed |
| the plugins that `BifrostConfig` takes | not used: the daemon keeps the caps and the usage |
| Bifrost's config and log stores | not used: the resources in the metastore, the usage in the gateway's database |

- **Addresses**: an address, its network settings, one queue and a fixed number of workers belong to a provider key
  (`core/schemas/account.go`, `ConcurrencyAndBufferSize`). One engine per gateway keeps them apart between gateways.
  Bifrost refuses private addresses unless a provider sets `AllowPrivateNetwork`, which the engine sets only for a
  model whose `url` is private.
- **Cancellation**: Bifrost's OpenAI provider checks for context cancellation in its streaming paths
  (`core/providers/openai/openai.go`). A call that the function abandons relies on it: the daemon cancels its call to
  the engine, and the engine cancels the vendor's.
- **Usage**: Bifrost always asks the vendor for usage (`stream_options.include_usage`, `openai.go:607`), takes in the
  vendor's usage chunk and puts the total on its final chunk (`openai.go:868`), so the vendors' different shapes never
  reach funcd. On a cancelled stream, Bifrost bills what the vendor already reported (`attachBilledUsageFromContext`).

## What funcd has today

- **The worker-node local API** (ADR-0069): HTTP over each sandbox's own Unix socket, with the caller's identity taken
  from the connection. It serves `/kv`, `/blob`, `/sql`, `/secret`, `/invoke` and `/fetch`
  (`internal/workernode/local`), answers whole bodies of at most 1 MiB (`maxInvokeBytes`), and sets no write timeout
  (`local.go:405`).
- **SQL databases on rqlite** (ADR-0232, ADR-0233): one rqlite engine per database; `sql.callTimeout` bounds one call,
  30 s by default; a delete is refused while a Function binds the database
  (`internal/controlplane/admission/sqldatabase.go`).
- **The add-on provider runtime** (ADR-0087, `internal/provider`): one engine container per resource, with the
  resolved Secret entries in the engine's environment, a readiness probe and one pinned replica. Engine images sit
  under `images/runtime/`, and every engine so far builds another project's pinned code.
- **Store entries at the root** (ADR-0236, ADR-0248): an entry declares a store by name or references one with `ref`
  (`SQLDatabaseEntry`, `api/types/v1alpha1/app.go`); a Site binds its one bucket with an entry that has no alias
  (`BlobRef`, `api/types/v1alpha1/bindings.go`).
- **Egress** (ADR-0115, ADR-0117, ADR-0175, ADR-0269): with `server.network.egress` on, a worker reaches only what its
  namespace's `EgressPolicy` allows. An engine's fence holds addresses only and drops DNS, and no EgressPolicy can
  grant an engine today: the compiled permit names Functions (`internal/auth/cedar/egress_compile.go`), and the egress
  gateway indexes only Function and Lake workers (`pkg/funcd/funcd.go`).
- **Cedar** (ADR-0074, ADR-0116): a binding is the grant, as in `builtin_sql.cedar` and `builtin_lake.cedar`; a
  provider reads its buckets as an S3 principal through `blobBindings` (ADR-0088, `builtin_s3.cedar`). No policy uses
  the request context yet.
- **A call's deadline** ([ADR-0226](../adr/0226-stuck-calls-deadline-and-stop-ladder.md)): the shims read the time
  left from `X-Funcd-Timeout-Ms`.

## The vendors' APIs and the OpenAI clients

- OpenRouter implements OpenAI's `/chat/completions` and `/embeddings`. It returns usage with every answer, with no
  request parameter, including the `cost` charged; a streamed answer carries it in a final chunk with one empty choice.
  A finished or cancelled call's tokens and cost can be read later from `GET /api/v1/generation?id=…`.
- OpenAI sends a stream's usage only when the request sets `stream_options.include_usage`, in an extra chunk whose
  `choices` list is empty; an interrupted stream may never receive it.
- OpenAI accepts up to 512 MB of payload per request; third-party sources cite about 20 MB per image.
- Ollama serves `/v1/chat/completions` (streaming, tools, JSON mode, images), `/v1/embeddings` and `/v1/models`. vLLM
  serves `/v1/chat/completions`, `/v1/completions`, `/v1/responses` and `/v1/embeddings`.
- The OpenAI Python client times out after 600 s, retries twice on 408, 409, 429 and 5xx, obeys `x-should-retry`, and
  does not retry when `Retry-After` exceeds 120 s; it raises an error when a stream event carries one. The Node client
  takes a custom `fetch`.

## The gateways set aside

| | LiteLLM proxy | Bifrost's HTTP gateway | agentgateway |
|---|---|---|---|
| Language | Python | Go | Rust |
| Its state | PostgreSQL, required for keys, teams and spend; Redis once there is more than one instance | SQLite (one process per file) or PostgreSQL, for its config and its logs | a config file or xDS; SQLite or PostgreSQL for per-key dollar budgets |
| Another store, such as rqlite | no: Prisma on PostgreSQL, through prisma-client-py, which is no longer maintained | its config store is a Go interface, but its implementation is GORM and its transactions hand out a `*gorm.DB`, while rqlite runs a transaction within one request only | not applicable |
| Also | releases 1.82.7 and 1.82.8 on PyPI carried a credential stealer (2026-03-24) | clustering and adaptive load balancing are Enterprise features | built for MCP and A2A traffic as much as for LLM traffic |

Bifrost's own caps live in its governance plugin, a module of its own: dollar budgets and token and request limits per
virtual key, team or customer, kept in its config store (SQLite or PostgreSQL) or in memory only. funcd keeps its own
caps instead, keyed on its gateways and bindings.

## Decisions

All rows were decided with the decider on 2026-10-24 and 2026-10-25.

| Topic | Decision |
|---|---|
| Placement | one add-on engine per `AIGateway`, started when the gateway is applied, as for a Lake or a SQL database; the daemon keeps the binding checks, Cedar, the caps and the usage, and calls the engine |
| Engine | funcd's own binary on Bifrost's core library, in the nested module `engines/ai/`; no fake engine anywhere: tests run the real engine against a local OpenAI-compatible server |
| API | OpenAI's API only; the helper returns an OpenAI client's settings (base URL, a transport over the function's socket, a placeholder key, a timeout of the time the function has left, no retries) and no SDK is bundled: `ctx.ai.openai()` in TypeScript, and `openai_settings(context)` from a separate module, `funcd_shim.ai`, in Python, so that only the functions that use it load httpx (the shim is stdlib-only, ADR-0049); the alias is the `model` field; the daemon talks to the engine in OpenAI's format too |
| Resource | `AIGateway`: its models, its usage database, its limits, timeout, idle limit and retries |
| Models | a short `name`, a `type` (`chat` or `embedding`), a `vendor`, the vendor's `id`, a `url` for a self-hosted server, the `secret` entry of its key, a default `system` prompt and default `params` |
| Keys | the gateway binds its Secrets (`bindings.secret`); each model names its entry; the entries reach the engine's environment at start; a changed Secret recreates the engine; two bound Secrets may not share an entry name |
| System prompt | a ConfigMap entry for a short text, a blob object for a long one, bound through `bindings.config` and `bindings.blob`; the daemon reads it, sends it with each call, and keeps a copy until it changes; it applies when a call sends no system message |
| Settings | OpenAI's parameters, written in camelCase on the spec and converted by the engine; only portable names, no pass-through of vendor-specific ones |
| Network settings | `timeout` (300 s), `streamIdleTimeout` (120 s) and `retry` (`maxAttempts` 3, `backoff` 500 ms) on the gateway, each optional; retries happen only in the engine, so they do not count as new calls |
| Binding | `spec.bindings.ai`: an `alias`, a `gateway`, a `model` and optional `limits`; Workflow steps bind the same way |
| Grants | Cedar `ai::call` on `AIGateway::"<ns>/<name>"`, granted by the binding; the model travels in the request context, so a Policy can forbid one model |
| Egress | with egress control on, funcd registers the engine with the egress gateway and grants it the hosts its models name; nothing changes with egress control off |
| Usage database | exactly one root `sql` entry on the gateway, declared by name or referenced with `ref`; no binding and no alias |
| Caps | `limits.tokens` and `limits.requests`, each with a `max`, a fixed `window` in UTC (`minute`, `hour`, `day`, `week`, `month`) and a `tolerance`, a decimal fraction of `max` (0.05 when unset); a spent cap refuses new calls with OpenAI's 429 shape and a `Retry-After`; a running call is cut at `max` × (1 + `tolerance`) by lowering its token limit, and its answer reads `finish_reason: "length"` with the header `X-Funcd-Cap: tokens` or `X-Funcd-Cap: requests`; a spent cap's error has `type: "funcd_cap"` and `code: "resource_exhausted"`; no cap in money |
| Counting | the caps count from the usage rows: the daemon keeps them in memory and rebuilds them at start; a call waits for the database at boot; after a restore, the caps rewind with the database |
| Usage rows | namespace, function, gateway, model, tokens in and out, duration, outcome, and the cost when the vendor reports it, empty otherwise |
| Abandoned streams | Bifrost's billed usage first, then OpenRouter's generation lookup, then an estimate of about 4 characters per token, for the prompt and for what was streamed, marked as estimated |
| Streaming | the daemon passes OpenAI's events through as they arrive; it keeps the final usage for itself and passes it on only to a call that asked for `include_usage` |
| Size | `ai.maxRequestBytes` in `funcdconfig.yaml`, 32 MiB by default, which fits one 20 MB image sent inline |
| Errors | funcd writes its own refusals in OpenAI's error shape with funcd's statuses (`api/fault/problem.go`); vendor errors pass through unchanged, as the local API does for `ctx.invoke` and `ctx.fetch` (`UpstreamError`) |
| Logs | prompts and answers never enter logs, telemetry or audit; the engine drops the raw request that Bifrost attaches to some errors |
| Readiness | Ready when the engine runs and the keys resolve; no vendor call |
| Delete | refused while a Function binds the gateway, as for SQL |
| Apps | an App declares gateways under `spec.aiGateways`, next to its stores; a gateway's `sql` entry references the App's database, and a gateway entry has no deletion policy |
| Dev | `funcdctl dev` runs the real engine as a host process, with keys from the shell, `.env` or `env:` ([ADR-0254](../adr/0254-funcdctl-dev-config-file.md)) |
| License | Bifrost's own module is plain Apache-2.0, checked on 2026-10-25; the licenses of its dependencies are reviewed in the engine ADR |
| Engine memory | an experiment during the engine ADR measures an idle engine's memory, which sets the engine's default memory limit |

## Shapes

The decided shapes, with placeholder model ids.

### The gateway and its inputs

```yaml
apiVersion: funcd.io/v1alpha1
kind: Secret
metadata:
  name: vendor-keys
  namespace: team-a
spec:
  data:
    OPENROUTER_API_KEY: <base64 of the key>
    OPENAI_API_KEY: <base64 of the key>
---
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: support-prompts
  namespace: team-a
spec:
  data:
    SYSTEM: You are team-a's support assistant. Answer in two sentences.
---
apiVersion: funcd.io/v1alpha1
kind: AIGateway
metadata:
  name: assistant
  namespace: team-a
spec:
  bindings:
    secret:
      - vendor-keys
    config:
      - support-prompts
    blob:
      - bucket: prompts
        prefix: support
  sql:
    - name: ai-usage
      resources:
        memory: 64Mi
  timeout: 300s
  streamIdleTimeout: 120s
  retry:
    maxAttempts: 3
    backoff: 500ms
  models:
    - name: fast
      type: chat
      vendor: openrouter
      id: example/chat-model
      secret:
        name: vendor-keys
        key: OPENROUTER_API_KEY
      system:
        config:
          name: support-prompts
          key: SYSTEM
      params:
        temperature: 0.2
        maxCompletionTokens: 800
        reasoningEffort: low
    - name: deep
      type: chat
      vendor: openrouter
      id: example/large-model
      secret:
        name: vendor-keys
        key: OPENROUTER_API_KEY
      system:
        blob:
          bucket: prompts
          key: support/system-long.md
    - name: embed
      type: embedding
      vendor: openai
      id: example/embedding-model
      secret:
        name: vendor-keys
        key: OPENAI_API_KEY
      params:
        dimensions: 1024
    - name: local
      type: chat
      vendor: ollama
      url: http://10.0.0.5:11434
      id: example/local-model
  limits:
    tokens:
      max: 2000000
      window: month
      tolerance: 0.05
    requests:
      max: 120
      window: minute
```

- `timeout` uses the duration strings of `Function.spec.timeout` ([ADR-0194](../adr/0194-api-duration-strings.md)),
  and `retry` has the shape of a Workflow step's `retry` (`api/types/v1alpha1/workflow.go`).
- In an App, the same spec sits under `spec.aiGateways`, and its `sql` entry references the App's database:
  `- ref: ai-usage`.

### A function binds it and calls it

```yaml
spec:
  bindings:
    ai:
      - alias: chat
        gateway: assistant
        model: fast
        limits:
          tokens:
            max: 200000
            window: day
      - alias: embed
        gateway: assistant
        model: embed
```

```ts
import OpenAI from "openai";

export async function handle(ctx: FunctionContext, event: CloudEvent<FuncInput>) {
  const openai = new OpenAI(ctx.ai.openai());

  const answer = await openai.chat.completions.create({
    model: "chat",
    messages: [{ role: "user", content: "Where is my order 42?" }],
  });
  const text = answer.choices[0].message.content;

  const parts: string[] = [];
  const stream = await openai.chat.completions.create({
    model: "chat",
    messages: [{ role: "user", content: "Summarize this ticket." }],
    stream: true,
  });
  for await (const chunk of stream) {
    parts.push(chunk.choices[0]?.delta?.content ?? "");
  }

  const { data } = await openai.embeddings.create({ model: "embed", input: ["my sink is leaking"] });
  const vector = data[0].embedding;
}
```

```python
from funcd_shim import CloudEvent, FunctionContext
from funcd_shim.ai import openai_settings
from openai import OpenAI

def handle(context: FunctionContext, event: CloudEvent[FuncInput]):
    client = OpenAI(**openai_settings(context))

    answer = client.chat.completions.create(
        model="chat",
        messages=[{"role": "user", "content": "Where is my order 42?"}],
    )
    text = answer.choices[0].message.content

    vector = client.embeddings.create(model="embed", input=["my sink is leaking"]).data[0].embedding
```

The handler signatures are the ones the language repos' examples use today. A spent cap answers with OpenAI's error
shape, which the client raises as its rate-limit error:

```json
{
  "error": {
    "message": "ai binding chat: tokens max 200000 spent; the window ends at 2026-10-25T00:00:00Z",
    "type": "funcd_cap",
    "code": "resource_exhausted"
  }
}
```

### Grants

```cedar
@id("ai-call-binding")
permit(principal, action == Action::"ai::call", resource)
  when { principal has aiBindings && principal.aiBindings.contains(resource) };
```

For the engine, funcd generates one permit per gateway from its models' hosts, as it compiles `EgressPolicy` rules:

```cedar
@id("aigateway-egress:team-a/assistant")
permit(principal, action == Action::"egress::connect", resource)
  when { principal == AIGateway::"team-a/assistant"
         && ((resource.port == 443
              && (resource.domains.contains("openrouter.ai") || resource.domains.contains("api.openai.com")))
             || (resource.port == 11434 && resource.ip.isInRange(ip("10.0.0.5/32")))) };
```

The gateway reads its long prompts through the existing `s3-read-binding` rule, with its `blobBindings`. An operator
Policy can still forbid a call, for one function, one gateway or, through the request context, one model.

### A usage row

```
time      function  gateway    model  tokens_in  tokens_out  duration_ms  outcome  cost
10:02:11  triage    assistant  fast   812        203         1840         ok       0.0012
```

## Gaps in today's code

- No AI vocabulary: no kind, binding, Cedar action, role or shim helper names `ai` or a gateway (grep of `api`,
  `internal`, `pkg`, `cmd` and `docs`, 2026-10-24).
- No streaming to a function: every local API route answers a whole body of at most 1 MiB.
- No way out for an engine that calls a hosted vendor with egress control on.
- No engine of funcd's own source, no AI engine image, and no funcd module depends on Bifrost's Go library.
- No policy reads the Cedar request context.

## Open questions

| Question | Decided in |
|---|---|
| How agents' skills are stored and loaded | later, with the decider |
| The AI gateway's monitoring: what the engine reports to funcd's telemetry; Bifrost offers a `Tracer` interface and plugins (`otel`, `telemetry`, `logging`, `maxim`) that depend on its `framework` module | later, with the decider |
| The licenses of the modules Bifrost brings; Bifrost's own module is Apache-2.0 | the engine ADR |

## Sources

- funcd: the ADRs and paths cited above, and `go.mod`.
- Bifrost: [maximhq/bifrost](https://github.com/maximhq/bifrost) at commit `4810249` (2026-10-24): `core/go.mod`,
  `core/bifrost.go`, `core/schemas/account.go`, `core/providers/openai/openai.go`, `core/providers/utils/utils.go`,
  `transports/bifrost-http/integrations/openai.go`, `plugins/governance`, `framework/modelcatalog`;
  [Go SDK setup](https://docs.getbifrost.ai/quickstart/go-sdk/setting-up),
  [config store](https://docs.getbifrost.ai/architecture/framework/config-store).
- OpenAI: [images and vision](https://developers.openai.com/docs/guides/images-vision);
  [openai-python](https://github.com/openai/openai-python) `src/openai/_constants.py`, `_base_client.py`,
  `_streaming.py`, `types/chat/chat_completion_stream_options_param.py`;
  [openai-node](https://github.com/openai/openai-node) `src/client.ts`.
- OpenRouter: [usage accounting](https://openrouter.ai/docs/guides/administration/usage-accounting),
  [streaming](https://openrouter.ai/docs/api-reference/streaming),
  [get a generation](https://openrouter.ai/docs/api-reference/get-a-generation.md),
  [embeddings](https://openrouter.ai/docs/api/reference/embeddings).
- Go: [the specification on handling panics](https://go.dev/ref/spec#Handling_panics).
- LiteLLM: [deployment](https://docs.litellm.ai/docs/proxy/deploy_cloud),
  [the PyPI incident](https://github.com/BerriAI/litellm/issues/24518),
  [prisma-client-py](https://github.com/RobertCraigie/prisma-client-py).
- agentgateway: [per-key budgets](https://agentgateway.dev/docs/standalone/main/llm/cost-controls/budget-limits/per-key/).
- rqlite: [FAQ, transactions](https://rqlite.io/docs/faq/).
- Ollama: [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility).
- vLLM: [online serving](https://docs.vllm.ai/en/latest/serving/online_serving/).
