# ADR-0281: AI gateways, one engine per AIGateway resource

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged twice; root declarations and the EgressPolicy rule for `url` hosts are the decider's choices)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, provider, resource, egress
- **Realizes**: [FEAT-0015/F194](../feat/0015-feat-ai-gateway.md) (AI gateways)
- **Supersedes (in part)**: [ADR-0175](0175-engine-is-its-own-principal.md) — Decision 1 (the owner kinds the egress
  index covers), Decision 2's sentence that an engine's external egress is denied, and Decision 5 (principal sources)
- **Relates to**: [ADR-0280](0280-the-model-engine-on-bifrosts-go-library.md) (the engine) · [ADR-0087](0087-add-on-provider-runtime.md),
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (an engine per resource) · [ADR-0248](0248-stores-at-the-root.md),
  [ADR-0251](0251-catalogservice-declares-its-stores.md), [ADR-0213](0213-app-config-and-secret-declarations.md) (root
  declarations) · [ADR-0117](0117-egress-policy-enforcement.md) (egress) · source: [design report](../reports/ai-gateway-design.md)
  · issue #1268

## Context & Need

A namespace needs one place for its model vendors' keys, its model choices and their settings (FEAT-0015 F194). This
ADR adds the `AIGateway` kind: it declares its models, the Secrets and prompts they use, and its usage database, and
applying it starts its own engine (ADR-0280) through the add-on runtime, as a SQLDatabase starts rqlite. The
reconciler hands the keys to that engine only, replaces it when its spec or a key changes, reports readiness, and,
with egress control on, lets it reach only its models' hosts.

## Scenarios

- `scenario: gateway-starts-its-engine` — Given gateway `assistant` declaring and binding Secret `vendor-keys` (key
  `OPENROUTER_API_KEY`) for model `fast`, When it is applied, Then one engine runs and the gateway turns Ready.
- `scenario: gateway-holds-without-key` — Given the declared Secret or key is missing, Then the gateway is not Ready
  with `SecretNotFound` or `SecretKeyMissing` and no engine runs; a key removed later tears its engine down.
- `scenario: gateway-key-change-replaces-engine` — When a key's value changes in its Secret, Then within one
  supervision period a new engine runs with the new key and the old one is gone.
- `scenario: gateway-declarations-checked` — When a binding names a Secret, ConfigMap or bucket prefix the gateway does
  not declare, or two declared Secrets declare one key, Then the API answers 400 naming it.
- `scenario: gateway-invalid-spec-refused` — When a model names vendor `azure`, an unknown vendor or a param outside its
  type's list, or the gateway has two `sql` entries, Then the API answers 400 naming the field.
- `scenario: gateway-usage-database` — Given `sql` entry `name: ai-usage`, Then SQLDatabase `ai-usage` exists with the
  gateway's owner marker; with `ref: shared-db`, the gateway binds that database without taking it over.
- `scenario: gateway-engine-crash-isolated` — When one engine is killed, as a panic ends it (ADR-0280 Decision 8),
  Then other gateways and workers keep serving, and it runs again after the next pass.
- `scenario: gateway-keys-stay-in-engine` — No key value appears in a status, an event, a log line or a function
  worker's environment.
- `scenario: gateway-egress-to-models-only` — Given egress on, an OpenRouter model and a `url` model at
  `10.0.0.5:11434`, Then the engine reaches `openrouter.ai:443`, the `url` only while an EgressPolicy allows it, and
  no other host.
- `scenario: gateway-delete-removes-engine` — When a gateway is deleted, Then its engine is removed, and its usage
  database follows the entry's `deletion` policy.

## Scope

**In**: the kind, its declarations and admissions; the reconciler, the engine's spec, environment, token and registry;
the usage database entry; egress; the principal; the vendor table; the catalog row; `hostengine`.

**Out**: `spec.limits` and the usage rows (ADR-0283); the binding, the delete-while-bound rule, Cedar `ai::call` and
reading the prompts (ADR-0282); Apps (ADR-0284); `funcdctl dev` (ADR-0285).

## Constraints & Decision drivers

- The keys reach the engine only, never a status, a log, an answer or a sandbox; they are named with the existing
  `spec.bindings.secret` (decided). Every binding names an entry of its resource's root (ADR-0248, ADR-0251).
- Ready means the engine runs and the keys resolve, with no vendor call; a namespace declares as many gateways as it
  needs, so there is no quota (decided). A steady pass writes nothing (ADR-0142 rule 9).

## Alternatives considered

1. **A key field of the gateway's own** (`credentialsSecret`): rejected by the decider for `bindings.secret`.
2. **Bindings to existing Secrets, ConfigMaps and buckets**, as the report's shapes showed: ADR-0251 ended that
   exception for Lakes. Rejected by the decider on 2026-10-25 for root declarations.
3. **Fencing the engine (`EgressOnly`, ADR-0269)**: a fence holds IP addresses, while a hosted vendor is a domain.
4. **An egress grant from the engine's own list of hosts**: a compromised engine could widen what confines it.
5. **Granting every model `url` with no policy check**: whoever applies a gateway could reach, through its engine, any
   address egress control refuses. Rejected by the decider on 2026-10-25: a `url` host must also pass an EgressPolicy.

## Decision

1. **The kind.** `AIGateway` (namespaced) has `spec.secrets`, `spec.configMaps` and `spec.buckets` (ADR-0248's shared
   entry types), `spec.sql` (exactly one `SQLDatabaseEntry`), `spec.bindings` (`secret`, `config`, `blob`),
   `spec.timeout` (default 300s), `spec.streamIdleTimeout` (default 120s), `spec.retry` (`AIRetry`, a Workflow step
   retry's shape with its own defaults: `maxAttempts` 1 to 100, default 3; `backoff` 1ms to 1h, default 500ms),
   `spec.models` and `spec.resources`. Its status has `phase`, `conditions` and `observedGeneration`.
2. **Root declarations**, as ADR-0251 gives a standalone Lake. Every binding names an entry of the gateway's root, and
   `binding-declared` covers the kind. `name` entries are written through `internal/ownedstore` with the gateway's
   marker and `ref` entries are never rewritten (a store owned by another resource holds the gateway not Ready with
   `StoreNotOwned`); an omitted prefix `owner` is the gateway, written in by a mutating `aigateway-store-owner`
   admission that shares `catalog-store-owner`'s code. ConfigMaps follow ADR-0213 Decisions 2, 3 and 5: stored as
   `<name>-<hash>`, with `bindings.config` and each model's `system.config.name` repointed to the stored name by that
   admission. Secrets follow ADR-0213 Decision 8. `gc.Pairs()` gains the gateway to its Buckets, ConfigMaps and
   SQLDatabases. A gateway nested in an App carries no `secrets`, `configMaps` or `buckets` (ADR-0284).
3. **Models.** A model has a `name` (a DNS label, unique in the gateway), a `type` (`chat` or `embedding`), a `vendor`,
   the vendor's `id`, an optional `url`, a `secret` (`name`, a declared and bound Secret; `key`, one of its declared
   keys; required unless the vendor is `ollama`, `vllm` or `sgl`), an optional `system` (`config`: `name`, `key` of a
   declared ConfigMap; or `blob`: `bucket`, `key` under a bound prefix) and optional `params` (a JSON object).
   `Validate` applies ADR-0280 Decisions 4 and 6 and refuses more than 64 models; a `timeout` or `streamIdleTimeout`
   outside 1s to 1h; two declared Secrets that declare one key (the decided rule that no two bound Secrets share an
   entry name); and a `secret.key` that is not an environment variable name or is one of the engine's own: `FUNCD_*`,
   `GO*`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (any case), `SSL_CERT_FILE`, `SSL_CERT_DIR`. ADR-0280's
   `engines/ai/testdata/config` cases run against `Validate` too: each `keyEnv` becomes a declared key of one bound
   Secret, and the rest maps field for field.
4. **Keys.** Each pass resolves the bound Secrets through `provider.ResolveEnv`, authorized as the namespace's
   developer identity, as the Lake does, and passes only the entries the models name, under those names; a model's
   `keyEnv` is its `secret.key`. A missing Secret or key holds the gateway not Ready (`SecretNotFound`,
   `SecretKeyMissing`); the pass that finds it tears down a running engine and drops it from the registry, and no
   engine starts until every key resolves.
5. **The engine.** The reconciler converges a `ProviderSpec`: image `ImageFor("ai")`, port 8080, readiness `GET
   /readyz` → 200, one replica, no storage, `EgressOnly` nil, the spec's resources (when `memory` is empty, 32Mi plus
   48Mi per vendor of the models, ADR-0280 Decision 10), and env `FUNCD_AI_CONFIG` (the models, settings and
   `keyEnv`s), `FUNCD_AI_TOKEN`, `GOMEMLIMIT` (90% of the memory bound) and the keys. Any change to that spec, a key
   included, changes its fingerprint, so `Converge` retires the engine and starts a new one on that pass
   (`internal/provider/runtime.go`). Requeues and status writes follow ADR-0232 Decision 2.
6. **The token.** `EngineToken(master, ns, name)` is HMAC-SHA256 of the node master over `funcd-ai-engine/<ns>/<name>`,
   base64url, as ADR-0232 derives a database's password. Nothing stores it.
7. **The registry.** The reconciler records each Ready engine's `ProviderStatus.Address` in an in-process `Engines`
   registry and drops it when the engine is not Ready or the gateway is deleted; ADR-0282 reads it.
8. **The usage database.** The `sql` entry goes through `internal/ownedstore` like the other root entries (Decision
   2); the database's own readiness does not gate the gateway's.
9. **Egress.** With `server.network.egress` off, nothing more applies. With it on:
   - the egress index covers namespaces with an AIGateway and indexes its engine as `AIGateway::"<ns>/<name>"`, with
     a principal source of its own (replacing ADR-0175 Decisions 1 and 5's two kinds with three);
   - funcd compiles one permit per gateway, as a Cedar policy structure (ADR-0117 4(a)): `egress::connect` for that
     principal to each hosted vendor's default API host on 443, and to each model `url`'s host and port that a
     whole-namespace EgressPolicy (one with no `appliesTo`) allows, checked when the permits compile, which happens again when a gateway or
     an EgressPolicy changes. A `url` that no policy allows gets no grant, and the reconciler records an event
     `EgressNotAllowed` naming the model. This permit is the one exception to ADR-0175 Decision 2's denial of an
     engine's external egress; namespace EgressPolicies still never reach an engine directly;
   - the vendor table, `internal/services/ai/vendors.json`, maps each vendor ADR-0280 accepts to its default API host,
     empty for `ollama`, `vllm` and `sgl`; a test in `engines/ai` checks it against Bifrost's defaults.
10. **The principal.** Cedar gains the entity type `AIGateway` with `namespace` and `blobBindings` (its
    `bindings.blob`), so ADR-0282 reads long system prompts through the existing `s3-read-binding` rule.
11. **Delete.** Deleting a gateway tears its engine down and drops it from the registry; the gateway holds no data.
    The catalog in `pkg/funcd/providers.go` lists `ai` as an add-on.

## Design model

None. The engine's lifecycle is the add-on runtime's `Converge`, reused unchanged (its fingerprint replaces an engine
whose spec or key changed); the key rules are static checks of one gateway's declarations; the permits recompile
from the stored gateways and EgressPolicies as ADR-0117's compiled policies do.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: AIGateway
metadata:
  name: assistant
spec:
  secrets:
    - name: vendor-keys
      keys:
        - OPENROUTER_API_KEY
  sql:
    - name: ai-usage
  bindings:
    secret:
      - vendor-keys
  models:
    - name: fast
      type: chat
      vendor: openrouter
      id: example/chat-model
      secret:
        name: vendor-keys
        key: OPENROUTER_API_KEY
```

```go
// api/types/v1alpha1/aigateway.go
const KindAIGateway Kind = "AIGateway"

// AIGateway has the usual TypeMeta, ObjectMeta, Spec AIGatewaySpec and Status (the shared Status type).

type AIGatewaySpec struct {
	SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
	ConfigMaps         []ConfigMapEntry    `json:"configMaps,omitempty"`
	Buckets            []BucketEntry       `json:"buckets,omitempty"`
	SQLDatabases       []SQLDatabaseEntry  `json:"sql"` // exactly one
	Bindings           AIGatewayBindings   `json:"bindings,omitempty"`
	Timeout            Duration            `json:"timeout,omitempty"`
	StreamIdleTimeout  Duration            `json:"streamIdleTimeout,omitempty"`
	Retry              *AIRetry            `json:"retry,omitempty"`
	Models             []AIModel           `json:"models"`
	Resources          ResourceSpec        `json:"resources,omitempty"`
}

type AIGatewayBindings struct {
	Secret []ObjectName `json:"secret,omitempty"`
	Config []ObjectName `json:"config,omitempty"`
	Blob   []BlobRef    `json:"blob,omitempty"`
}

type AIRetry struct {
	MaxAttempts int      `json:"maxAttempts,omitempty"` // 1 to 100; 3 when unset
	Backoff     Duration `json:"backoff,omitempty"`     // 1ms to 1h; 500ms when unset
}

type AIModel struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"` // "chat" or "embedding"
	Vendor string          `json:"vendor"`
	ID     string          `json:"id"`
	URL    string          `json:"url,omitempty"`
	Secret *KeyRef         `json:"secret,omitempty"`
	System *AISystemPrompt `json:"system,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type KeyRef struct { // one key of a declared Secret or ConfigMap
	Name ObjectName `json:"name"`
	Key  string     `json:"key"`
}

type AISystemPrompt struct {
	Config *KeyRef        `json:"config,omitempty"` // exactly one of the two
	Blob   *BlobObjectRef `json:"blob,omitempty"`
}

type BlobObjectRef struct {
	Bucket ObjectName `json:"bucket"`
	Key    string     `json:"key"`
}

// internal/services/ai (package ai, imported as aisvc)
func EngineToken(master []byte, ns v1.NamespaceName, name v1.ObjectName) string

// Engines has the methods of ADR-0232's registry: NewEngines, Set, Delete, Address.
type Engines struct{ /* unexported; safe for concurrent use */ }

// VendorHost is a vendor's default API host:port from vendors.json; ok is false for a vendor ADR-0280 refuses.
func VendorHost(vendor string) (host string, ok bool)

type ReconcilerDeps struct {
	Store             store.Store
	Provider          provider.Runtime // OwnerKind AIGateway
	Engines           *Engines
	Secrets           provider.SecretResolver
	Master            []byte
	ImageFor          func(runtime string) string
	Logger            *slog.Logger
	SupervisionPeriod time.Duration
}

func NewReconciler(d ReconcilerDeps) (*Reconciler, error) // and Reconcile, as ADR-0232's
```

| Status reason (Ready False) | When |
|---|---|
| `SecretNotFound`, `SecretKeyMissing` | a declared Secret, or one of its declared keys, is missing |
| `StoreNotOwned` | a `name` entry's store exists without the gateway's marker |
| `EngineNotReady`, `EngineConvergeFailed` | as for SQLDatabase (ADR-0232) |

| Consumes | Exposes |
|---|---|
| `provider.Runtime`, `provider.ResolveEnv`, the node master, `ImageFor`, `internal/ownedstore` | the kind, its routes and SDK plural `aigateways` |
| the image `funcd/runtime-ai`; EgressPolicies; `server.network.egress` | `Engines`, `EngineToken`, `VendorHost`, the principal, the egress permits, `EgressNotAllowed`, the `ai` row |

## Implementation plan

1. Kind wiring (`api/types/v1alpha1/aigateway.go` with `Validate`, every new-kind site, `stampTypeMeta` included,
   `pkg/sdk/kinds.go`, `just generate`); `binding-declared`, `aigateway-store-owner`, `internal/ownedstore`,
   `IsMarker` and `gc.Pairs()` gain the kind.
2. `internal/services/ai`: `engine.go`, `vendors.go` with `vendors.json`, `reconcile.go`. Egress: the index's kinds
   (`pkg/funcd/funcd.go`), the principal source, the Cedar entity and the permit compiler
   (`internal/auth/cedar/egress_compile.go`) with the EgressPolicy check for `url` hosts.
3. `pkg/funcd`: the runtime with `OwnerKind: AIGateway`, the reconciler, the admissions, the catalog row and
   `WithAIProviderRuntime`; `internal/ai/hostengine`, a `provider.Runtime` that runs `funcd-ai` on `127.0.0.1:0` and
   reads its address line (ADR-0285 reuses it); `internal/testkit/aiengine`, which builds `funcd-ai` and
   `funcd-ai-vendortest` once per test binary with `GOTOOLCHAIN=auto`.
4. Tests: `Validate` over `engines/ai/testdata/config` and its own cases; key resolution; the permit compiler with and
   without an allowing EgressPolicy; `engines/ai/engine/vendors_test.go`; one `TestScenario<Name>` per scenario on
   `hostengine` with the real engine, except the crash and egress scenarios, which run in a containerd lane.

**Definition of done**: the tests pass; the lane runs the two containerd scenarios once; `just ci` passes; no username
or absolute path in a changed file.

## Review checklist

- [ ] `funcdctl apply` creates an AIGateway (`stampTypeMeta` included); `binding-declared` refuses an undeclared binding.
- [ ] Only the entries the models name reach the engine; no key value in status, events or logs.
- [ ] A key change replaces the engine through `Converge`'s fingerprint; a missing key tears it down.
- [ ] The permit is a policy structure naming only vendor hosts and EgressPolicy-allowed `url` hosts; a test checks
      `vendors.json` against Bifrost.
- [ ] One test per scenario, named for it.

## Consequences

- Each gateway costs one always-on engine, of at most 32 MiB plus 48 MiB per vendor unless its spec says otherwise; a
  key change cuts the calls in flight on the old engine.
- With egress on, a vendor whose API moves to a new host needs a funcd release with an updated table. With egress
  off, a model's `url` reaches any address, as a function itself could.

## Open questions

None.

## References

- Issue #1268; `internal/provider/runtime.go` (`Converge`), `internal/services/lake/reconcile.go` (`engineEnv`),
  `internal/controlplane/admission/bindingdeclared.go`.
