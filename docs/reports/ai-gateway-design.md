# AI gateway design: a fixed `ctx.ai` over an engine built on Bifrost's Go library

- **Status**: Draft (2026-10-24): proposed for [FEAT-0015](../feat/0015-feat-ai-gateway.md). The engine and its
  building blocks were refined with the decider on 2026-10-24; the other decisions are proposals. The ADRs decide, and
  an Accepted ADR wins over this report.
- **Deciders**: green-0-rabbit

## Summary

funcd gets an AI gateway: a namespace declares a `ModelEndpoint`, and functions chat with its models and embed text
through `ctx.ai` from TypeScript and Python. The vendor or the model underneath can change without any change to a
function. Every name in this report is new and unbuilt unless it cites a file or an ADR.

- **A built-in provider, not an add-on engine.** The gateway runs in the daemon, as the ingress and egress gateways do
  (blueprint, *Providers*). Calling a vendor's API needs no cgo and no heavy engine, so the blueprint's dividing
  question puts it in the built-in tier, and no container runs per endpoint.
- **An engine built on Bifrost's Go library.** The decider chose it on 2026-10-24: a Go SDK lets funcd build its own
  gateway, shaped to the platform, instead of running another gateway's server. `ModelEndpoint.spec.vendor` names one
  of the vendors Bifrost supports, such as OpenRouter, OpenAI, Anthropic, Gemini, Bedrock, Ollama or vLLM, each reached
  through its own API. A second engine, `fake`, answers in the daemon for tests and `funcdctl dev`, and one conformance
  suite runs against both, as `blobcontract` does for the blob port ([ADR-0007](../adr/0007-blob-storage-layer-port.md)).
- **On funcd's own building blocks.** Bifrost's `Account` interface reads the endpoints from the metastore
  ([ADR-0065](../adr/0065-metastore-badger-engine.md)) and their keys from funcd Secrets
  ([ADR-0057](../adr/0057-secret-injection-last-mile.md), [ADR-0249](../adr/0249-ctx-secret-reads-from-funcd.md)).
  Functions reach the gateway over their sandbox's own Unix socket ([ADR-0069](../adr/0069-kv-data-plane.md)). The
  usage ledger and the durable limit counters live in rqlite, through funcd's SQL engine
  ([ADR-0232](../adr/0232-sql-database-resource-and-rqlite-engine.md)). Bifrost's config store, log store, web UI and
  HTTP server are not used, so the gateway adds no PostgreSQL, no SQLite file and no Redis.
- **A fixed API, `ctx.ai`**, reached as `ctx.sql` is ([ADR-0233](../adr/0233-sql-access-from-functions.md)): chat,
  whole or streamed, and embeddings. The function holds no vendor key and no vendor SDK.

## The question

Where should model calls run, and what does a function see? Three shapes:

| | A: each function calls the vendor | B: an engine container per endpoint | C: a built-in provider in the daemon |
|---|---|---|---|
| The vendor key | in every sandbox that calls | in the engine's environment | in the daemon only |
| What a function loads | a vendor SDK | a thin client | the shim's client only |
| Limits and usage per function | none in common | the engine's own keys, budgets and store | funcd's bindings, Cedar and counters |
| Outbound path | each namespace allows the vendor's domain ([ADR-0117](../adr/0117-egress-policy-enforcement.md)) | the engine dials the vendor's hostname, which ADR-0269's address-only fence cannot express | the daemon dials the vendor, outside the workers' egress fence ([below](#what-funcd-has-today)) |
| Added footprint | an SDK in every warm worker | one engine per endpoint | Bifrost's code and modules in the daemon, and the ledger's rqlite engine (4 to 7 MB at rest, ADR-0232) |
| Platform work | none | an add-on engine, its image and its config | an epoch, as SQL's |

The proposal is C:

- A puts a credential in the sandbox and leaves no central control, which funcd removed for SQL and vectors.
- B adds a second governance layer, the engine's own keys and budgets, beside funcd's bindings and Cedar. A gateway
  server also brings its own database ([below](#the-gateways-set-aside)). And
  [ADR-0269](../adr/0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) fences an engine by IP address
  only, so an engine that dials a vendor's hostname would run unfenced or need a new kind of fence.

## The engine: Bifrost's Go library

The decider chose C2 over C1 on 2026-10-24: Bifrost's Go SDK lets funcd build a gateway that suits the platform's
serverless approach, on funcd's Secrets, rqlite and Unix sockets, while it reaches each vendor's own API.

| | C1: funcd's own OpenAI-compatible client | C2: Bifrost's Go library (chosen) |
|---|---|---|
| Vendors | any OpenAI-compatible API: OpenRouter, OpenAI, Ollama, vLLM | 35 provider packages, each speaking a vendor's own API, the OpenAI-compatible ones included |
| New modules in `go.mod` | none | 32 of the 80 it requires |
| Go version | funcd's own (1.26.6) | requires 1.27.3 |
| Code in the daemon | the client's own | about 286,000 lines of non-test Go in its `core` module |
| cgo | none | none, so the no-cgo daemon rule (ADR-0065) holds |
| Extras | none | fallbacks, weighted keys, vendor-specific features |

C2 was measured on a shallow clone of `maximhq/bifrost` at commit `4810249` (2026-10-24), against funcd's `go.mod`.
What the choice brings, for the engine ADR to plan:

- **A Go bump**: funcd moves from Go 1.26.6 to 1.27.3 or later, through `go.mod` and the flake's pin.
- **New modules**: among them the Azure and Google Cloud SDKs, an MCP and an A2A library, a Starlark interpreter and
  the sonic JSON encoder. The `core` package imports every provider package, so they come together, and the license
  of each must meet the project's Apache-2.0/MIT-compatible rule.
- **Panics on Bifrost's goroutines**: Bifrost runs each provider's calls on worker goroutines it starts
  (`requestWorker` in `core/bifrost.go`), and its OpenAI provider reads a stream in a goroutine of its own. Neither
  stops a panic in flight: the recovers in its stream helpers sit one defer deeper, where `recover` does not catch a
  panic already under way, as Bifrost's own comment on them says. A `recover` at funcd's port runs on the caller's
  goroutine and cannot reach them. A panic there stops the daemon, and the next start's boot sweep removes every
  worker ([ADR-0167](../adr/0167-process-worker-crash-recovery.md),
  [ADR-0186](../adr/0186-containerd-clean-slate-at-boot.md)).
- **Footprint**: the `core` module holds 514 non-test Go files, 10.9 MiB of source, at the same commit. The daemon's
  binary size and idle memory with it are not measured yet, and the blueprint asks for low resource consumption.

### How the engine sits on Bifrost

| Bifrost's seam | funcd's building block |
|---|---|
| `Account.GetConfiguredProviders` | the vendors of the namespace's `ModelEndpoint` resources, in the metastore |
| `Account.GetKeysForProvider` | the endpoint's key, which the daemon reads from its Secret |
| `Account.GetConfigForProvider` | the endpoint's address and network settings |
| the plugins that `BifrostConfig` takes | funcd's limit checks and usage records |
| Bifrost's HTTP server | not used: the worker-node local API over each sandbox's Unix socket |
| Bifrost's config and log stores | not used: the resources in the metastore, the ledger in rqlite |

`GetKeysForProvider` receives the request's context, so one client can pick each endpoint's key per call, but
`GetConfiguredProviders` and `GetConfigForProvider` receive none (`core/schemas/account.go`): an address and its
network settings belong to a provider key, not to a call. One client per daemon therefore needs a provider key for each
endpoint address, and each provider key gets one queue and a fixed number of workers (`ConcurrencyAndBufferSize`),
which every namespace that calls it shares.

Bifrost's OpenAI provider checks for context cancellation in its streaming paths (`core/providers/openai/openai.go`).
A call that the function abandons relies on it, and the conformance suite checks it end to end. A cancelled stream also
never receives its last event, which is where OpenRouter puts the usage ([below](#the-vendors-apis)).

## What funcd has today

- **The worker-node local API** (ADR-0069): HTTP over each sandbox's own Unix socket, with the caller's identity taken
  from the connection. It serves `/kv`, `/blob`, `/sql`, `/secret`, `/invoke` and `/fetch`
  (`internal/workernode/local`).
- **SQL databases on rqlite** (ADR-0232, ADR-0233): one rqlite engine per database, which the daemon reaches with the
  engine's credential; functions never hold it.
- **Secrets that the daemon resolves** (ADR-0057, ADR-0249): a bound Secret is resolved under a Cedar check, and
  `ctx.secret` reads it from funcd, not from the environment.
- **Egress control** ([ADR-0115](../adr/0115-worker-network-isolation.md), ADR-0117): with `server.network.egress` on,
  a worker reaches only the domains its namespace's `EgressPolicy` allows. The switch is off by default, and the fence
  holds the workers' bridge (`internal/platform/config/config.go`), so the daemon's calls to a vendor are outside it.
- **Token buckets at the edge** ([ADR-0164](../adr/0164-rate-limit-per-target.md)): `internal/edge/limit` keeps a
  bounded table of buckets per resolved target.
- **Telemetry and an audit channel** ([ADR-0010](../adr/0010-observability-telemetry-and-audit.md)).

## The vendors' APIs

- OpenRouter implements OpenAI's `/completions` and `/chat/completions` and presents itself as a drop-in replacement
  for OpenAI's SDKs. It also serves `/embeddings`.
- OpenRouter returns usage with every answer, with no request parameter: prompt and completion tokens, cached tokens
  and the `cost` charged. A streamed answer carries it in its last event, so a stream cancelled before that event gets
  none.
- Ollama serves `/v1/chat/completions` (streaming, tools, JSON mode, images), `/v1/embeddings` and `/v1/models`.
- vLLM's server implements `/v1/chat/completions`, `/v1/completions`, `/v1/responses` and `/v1/embeddings`.

## The gateways set aside

| | LiteLLM proxy | Bifrost's HTTP gateway | agentgateway |
|---|---|---|---|
| Language | Python | Go | Rust |
| Its state | PostgreSQL, required for keys, teams and spend; Redis once there is more than one instance | SQLite (one process per file) or PostgreSQL, for its config and its logs | a config file or xDS; SQLite or PostgreSQL for per-key dollar budgets |
| Another store, such as rqlite | no: Prisma on PostgreSQL, through prisma-client-py, which is no longer maintained; S3 only as a response cache and a log sink | its config store is a Go interface, but its implementation is GORM and its transactions hand out a `*gorm.DB`, while rqlite runs a transaction within one request only | not applicable |
| Also | releases 1.82.7 and 1.82.8 on PyPI carried a credential stealer (2026-03-24) | clustering and adaptive load balancing are Enterprise features | built for MCP and A2A traffic as much as for LLM traffic |

Each keeps its own keys, budgets and store beside funcd's. C replaces them with funcd's resources, bindings and Cedar;
the chosen engine takes Bifrost's library and leaves its server, which is the part that needs those stores.

## Decisions and proposals

The engine and the building blocks were decided with the decider on 2026-10-24; the other rows are proposals.

| Topic | Decision or proposal |
|---|---|
| Shape | C: a built-in provider; the facade calls the vendor from the daemon |
| Engine (decided) | built on Bifrost's Go library, behind funcd's own port, with `fake` for tests and the dev loop |
| Building blocks (decided) | keys in funcd Secrets, access over each sandbox's Unix socket, durable data in rqlite |
| API | a fixed `ctx.ai` with `chat`, `stream` and `embed`, not a mirror of a vendor's API |
| Resource | `ModelEndpoint`: its vendor, an address for a self-hosted server, the Secret that holds its key, its models (a short name, the vendor's model id, chat or embedding) and its limits |
| Binding | `spec.bindings.ai`: an alias, an endpoint and one of its models; the binding is the grant |
| Grants | Cedar `ai::call` on `ModelEndpoint::"<ns>/<name>"`; a binding permits it, and an operator Policy may forbid it; no writer role, since a call changes no shared data |
| Key | read by the daemon from the bound Secret; never in a sandbox, a status, a log or an answer |
| Streaming | the local API passes the vendor's chunks to the function as they arrive; a call that the function abandons cancels the vendor call |
| Limits | requests and tokens per window, on an endpoint and on a binding; once a window's cap is spent, the next call is refused before the vendor sees it. A call's tokens are known only from its answer, so calls in flight can overshoot a token cap ([open](#open-questions)). The counters are kept in the daemon and flushed to rqlite in batches, so a restart keeps them |
| Usage | one row per call in the rqlite ledger: namespace, function, endpoint, model, tokens in and out, duration, outcome, and the cost when the vendor reports it; the same record goes to telemetry and audit |
| Egress | unchanged for functions: with egress on, a function reaches a vendor directly only if its namespace allows the vendor's domain. The daemon's calls to an endpoint's address follow the endpoint ADR's rule ([open](#open-questions)) |

## Illustrative shapes

Illustrative and non-normative: every name here is new, the ADRs decide, and the model ids are placeholders.

### The kind

```yaml
apiVersion: funcd.io/v1alpha1
kind: ModelEndpoint
metadata:
  name: openrouter
  namespace: team-a
spec:
  vendor: openrouter
  credentialsSecret: openrouter-key
  models:
    - name: fast
      id: example/chat-model
      kind: chat
    - name: embed
      id: example/embedding-model
      kind: embedding
  limits:
    requestsPerMinute: 120
    tokensPerDay: 2000000
```

- The engine comes from the platform's configuration, `bifrost` by default, and `vendor` names one of the vendors it
  supports.
- The Secret `openrouter-key` holds the key in one entry, `API_KEY`. The daemon reads it; nothing else does. The field
  mirrors `credentialsSecret` on a `BackupTarget` (`api/types/v1alpha1/backup.go`), since `secret` already names the
  Secrets a Function reads (`spec.bindings.secret`).
- A self-hosted server names its vendor and its address, such as `vendor: ollama` with `url: http://10.0.0.5:11434`,
  and names a Secret only when the server asks for a key. Who may set `url`, and which addresses the daemon refuses,
  is open.
- A delete is refused while a Function binds the endpoint, as for SQL
  (`internal/controlplane/admission/sqldatabase.go`).

### A function binds it and uses it

```yaml
spec:
  bindings:
    ai:
      - alias: chat
        endpoint: openrouter
        model: fast
        limits:
          tokensPerDay: 200000
      - alias: embed
        endpoint: openrouter
        model: embed
```

```ts
const answer = await ctx.ai.chat("chat", {
  messages: [{ role: "user", content: "Summarize this ticket." }],
  maxTokens: 400,
});

for await (const chunk of ctx.ai.stream("chat", { messages })) {
  send(chunk.text);
}

const text = "my sink is leaking";
const { vectors } = await ctx.ai.embed("embed", { input: [text] });
await ctx.vector.upsert("mem", "notes", [{ id: "n1", text, embedding: vectors[0] }]);
```

Python has the same calls in snake case, and its `stream` returns an iterator. A chat request carries `messages` and
`maxTokens`, plus `temperature`, `tools` and `responseFormat` when the engine declares those capabilities. An answer
carries the text, any tool calls and its usage. The last line uses the vector API of
[FEAT-0013](../feat/0013-feat-vector.md), which leaves embedding to the function.

### Grants

```cedar
@id("ai-call-binding")
permit(principal, action == Action::"ai::call", resource)
  when { principal has aiBindings && principal.aiBindings.contains(resource) };
```

As for `sql::read` (ADR-0233), the facade resolves the alias from the caller's bindings, checks that the endpoint
offers the model, asks Cedar and checks the limits, all before any vendor call.

### The engine port

```go
type Engine interface {
	Capabilities() Capabilities
	Schema() []byte
	Check(ctx context.Context, e Endpoint) error
	Chat(ctx context.Context, e Endpoint, model string, r ChatRequest) (ChatAnswer, error)
	Stream(ctx context.Context, e Endpoint, model string, r ChatRequest, emit func(Chunk) error) (Usage, error)
	Embed(ctx context.Context, e Endpoint, model string, r EmbedRequest) (EmbedAnswer, error)
}
```

The `bifrost` engine implements the port with Bifrost's client, through the `Account` and the plugins above. The
`fake` engine answers in the daemon with no network: the same answer for the same request, and vectors derived from
the input text.

### A usage record

```json
{
  "namespace": "team-a",
  "function": "triage",
  "endpoint": "openrouter",
  "model": "fast",
  "tokensIn": 812,
  "tokensOut": 203,
  "durationMs": 1840,
  "outcome": "ok"
}
```

The ledger keeps the same fields, one row per call.

## Gaps in today's code

- No AI vocabulary: no kind, binding, Cedar action, role or shim helper names `ai` or a model endpoint (grep of `api`,
  `internal`, `pkg`, `cmd` and `docs`, 2026-10-24).
- No streaming to a function: every local API route answers a whole body, and `ctx.fetch` returns the target's whole
  reply, at most the invoke limit (`internal/workernode/local/local.go`,
  [ADR-0243](../adr/0243-http-functions.md) Decision 6).
- No limit on a function's tokens: the edge's buckets limit requests per target only.
- No model client in the daemon: Bifrost's Go library is not yet a dependency.

## Open questions

| Question | Decided in |
|---|---|
| The kind's name, the `ai` vocabulary, and the binding's `endpoint` field, a word that names a URL elsewhere (`api/types/v1alpha1/lake.go`) | the endpoint ADR |
| Who may set an endpoint's address, and which addresses the daemon refuses to dial for it, such as loopback, link-local and funcd's own listeners | the endpoint ADR |
| Whether an endpoint's readiness calls the vendor, or only checks that its key resolves | the endpoint ADR |
| One Bifrost client per endpoint, or one per daemon whose `Account` picks an endpoint's key from the request's context, given that an address and a queue belong to a provider key ([above](#how-the-engine-sits-on-bifrost)) | the engine port ADR |
| The Go bump to 1.27, the license review of the modules Bifrost brings, and the daemon's binary size and idle memory with them | the engine port ADR |
| How a panic on Bifrost's goroutines is kept from stopping the daemon ([above](#the-engine-bifrosts-go-library)) | the engine port ADR |
| How a stream crosses the function's socket, and how it ends when the function's call ends | the access ADR |
| Size caps, the call timeout and the stream's idle timeout, under the function's own deadline ([ADR-0151](../adr/0151-external-invoke-deadline.md)) | the access ADR |
| How a token cap holds when a call's tokens come back only with its answer: reserve `maxTokens` and settle after, require `maxTokens` under a token cap, or accept a stated overshoot by the calls in flight | the limits ADR |
| How a stream that the function abandons is counted, since some vendors report usage only in its last event: count the chunks received, or charge a reservation | the limits ADR |
| The ledger's database: one platform database, or one that the namespace names on its endpoint so its functions query their usage with `ctx.sql`; and how often the counters are flushed | the limits ADR |
| What a call does while the ledger's rqlite engine is not up (it starts after the daemon, ADR-0232), and what a daemon crash loses of the counters; one option rebuilds the window counters from the ledger rows at start | the limits ADR |
| Whether usage carries a price from a table when the vendor reports none | the limits ADR |
| Whether OpenAI's wire format for SDKs (F194) joins this epoch | the decider |

## Sources

- funcd: the ADRs and paths cited above, and `go.mod`.
- Bifrost: [maximhq/bifrost](https://github.com/maximhq/bifrost), measured at commit `4810249` (2026-10-24);
  [Go SDK setup and the `Account` interface](https://docs.getbifrost.ai/quickstart/go-sdk/setting-up),
  [`core/schemas`](https://pkg.go.dev/github.com/maximhq/bifrost/core/schemas),
  [config store](https://docs.getbifrost.ai/architecture/framework/config-store),
  [storage](https://docs.getbifrost.ai/deployment-guides/config-json/storage).
- Go: [the specification on handling panics](https://go.dev/ref/spec#Handling_panics).
- LiteLLM: [deployment](https://docs.litellm.ai/docs/proxy/deploy_cloud),
  [the PyPI incident](https://github.com/BerriAI/litellm/issues/24518),
  [prisma-client-py](https://github.com/RobertCraigie/prisma-client-py).
- agentgateway: [per-key budgets](https://agentgateway.dev/docs/standalone/main/llm/cost-controls/budget-limits/per-key/).
- rqlite: [FAQ, transactions](https://rqlite.io/docs/faq/).
- OpenRouter: [FAQ](https://openrouter.ai/docs/faq),
  [usage accounting](https://openrouter.ai/docs/guides/administration/usage-accounting),
  [embeddings](https://openrouter.ai/docs/api/reference/embeddings).
- Ollama: [OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility).
- vLLM: [online serving](https://docs.vllm.ai/en/latest/serving/online_serving/).
