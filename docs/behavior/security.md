# Security

Status words and how to read this: [the index](README.md).

Kinds: Identity, Role, RolesAssignment, Policy, Grant, EgressPolicy, Secret.

```mermaid
stateDiagram-v2
    [*] --> Authn : an API call, or a call on an authenticated route
    [*] --> Principal : a worker's or an Identity's data call
    [*] --> Redirected : a worker's outbound TCP, egress on
    [*] --> Lookup : a worker's lookup on port 53, egress on
    Authn --> Refused : no token, or an unlisted one
    Authn --> rbac : the token's role and namespaces
    rbac --> Allowed : the role covers the verb, kind and namespace
    rbac --> Refused : it does not
    Principal --> cedar : principal, action, resource
    Redirected --> Refused : an unknown source address
    Redirected --> cedar : the worker, the names it resolved unless the address is restricted, the address
    Lookup --> Refused : an unknown source, not a standard query, a name no domain rule allows, or a failed read
    Lookup --> Allowed : a name a domain rule allows, sent to the resolver
    cedar --> Allowed : a permit and no forbid
    cedar --> Refused : no permit, or a forbid
    Allowed --> [*]
    Refused --> [*]
```

The API and the edge read one token list; `/openapi`, `/docs` and `/schemas/` need no token. A binding grants read, the owner or a
writer role grants write, and the built-in `Owner` covers KV and blob data only, not managing assignments (ADR-0136). An Identity's
credential authenticates and grants nothing until a RolesAssignment does. A Policy naming another namespace in its scope is refused at
apply (ADR-0177 Decision 3). With egress on, workers cannot reach each other and UDP other than DNS is dropped (ADR-0115 Decision,
ADR-0117 Consequences). A domain rule reaches public addresses only: a private, link-local or node address takes a `cidrs` rule
that names it, and a name needs a domain rule (ADR-0315). New words: Redirected, Refused, Lookup.

| Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|
| An API call | A missing or unlisted token is refused; then the token's role decides, and a viewer never reads a Secret | none | 401 `missing bearer token or API key`, 401 `invalid credential`, 403 `get Secret in "<ns>" denied: viewer may not act on kind "Secret"` | decided | ADR-0018 Decisions 2 and 3, ADR-0171 Decision 9, `TestScenarioViewerCannotReadSecrets` |
| The built-in dev token on loopback | Works in each of its forms, with one warning at the start | none | `using the built-in dev token` | decided | ADR-0314 Decisions 2 and 4, `TestScenarioBuiltInTokenOnLoopbackWarns` |
| A real token or `auth.credentials` | Starts on any address; the built-in dev token then gets 401 | none | 401 `invalid credential` | decided | ADR-0314 Decision 2, `TestScenarioRealCredentialBindsAnywhere` |
| `funcd.New` with `DevToken` off loopback, or edge auth with it and a data plane off loopback | Refused, as the daemon refuses a start | none | `is off loopback` | decided | ADR-0314 Decisions 5 and 6, `TestScenarioLibraryFollowsTheRule`, `TestScenarioEdgeAuthFollowsTheRule` |
| A change to `auth.credentials` or a token file | Files are read once at startup, so a change takes effect at the next start | a restart | a new token gets 401 `invalid credential` until then | decided | ADR-0171 Decision 6 and Temporary workarounds |
| A call allowed on an `authenticated` route | The Function receives neither `Authorization` nor `X-Api-Key`; an `open` route forwards every header | none | the headers are absent in the handler | decided | ADR-0171 Decision 5, `TestScenarioEdgeDropsTheCredential` |
| A data call by a Function or an Identity | Denied unless a binding or a grant permits it and no forbid matches; a write takes the owner or a writer role | none | 403 ending `cedar default-deny: no permitting policy`, or `cedar: forbidden by policy <id>` such as `builtin/kv-single-writer` | decided | ADR-0136 Decision, ADR-0177 Decision 4, `TestIssue675_BuiltinDenyNamesStableID` |
| A request across namespaces | Only the built-in policies decide: no Policy, RolesAssignment or EgressPolicy reaches it | none | the reason ends `(cross-namespace request: built-in policies only)` | decided | ADR-0177 Decisions 1 and 2, `TestScenario_cross_namespace_built_ins_only` |
| A write to a Policy, Role, RolesAssignment or EgressPolicy | Applies at the next decision, with no restart | none | the next call's answer | decided | ADR-0177 Decision 2, ADR-0117 Decision 4a, `TestScenarioPolicyCacheAtomicSwap` |
| A grant changes what a pooled Function may do | It moves to a pool of its own access: its calls get 404, then 503 while the new pool loads it | a pool's cold start | `status.pool` changes | decided | ADR-0158 Decision 5, `TestScenarioPoolSplitsByGrant` |
| A RolesAssignment naming a missing Role or an unknown built-in | Applies with no complaint and grants nothing; nothing shows it | none | a normal apply, then the denial | **differs** | ADR-0136 wants it observable and built-in names checked; `rolesassignment.go` checks non-empty only; `TestScenarioNamespaceScopeAndMissingRole` |
| A Grant | Stored and listed; nothing reads it: a reserved placeholder | none | a normal apply, no effect | decided | ADR-0073 Decision 1 |
| `spec.rotate` raised on an Identity | A new secret replaces the old under the same access key id; the old one stops authenticating, with no overlap | the next pass | `status.observedRotate` follows | decided | ADR-0135 scenario `identity-credential-rotated`, `TestScenarioIdentityCredentialRotated` |
| `ctx.secret.get` or `list` | Asks funcd on each read, with no cache: a changed Secret shows at the next read, at a round trip per read | none | the new value, no restart | decided | ADR-0249 Decision 2 and Consequences, `TestScenarioSecretReadLive` |
| A bound Secret is missing | No worker starts; one already serving goes on, and `ctx.secret.get` reads nothing; a Secret applied later is found at the re-check | `controller.referentPollInterval` 2 s | `Failed` or `RevisionReady=False`, `SecretResolveFailed` | decided | ADR-0249 Decisions 2 to 4, ADR-0221 Decision 3, `TestIssue77_MissingBindingRecoversWhenApplied` |
| A bound Secret value that is not UTF-8 or holds a NUL | No worker starts; a corrected Secret does not clear it until the Function changes or funcd restarts | none | `Failed`, `SecretResolveFailed`, `is not valid UTF-8; env delivery cannot carry binary data` | **uncovered** | ADR-0306 Contracts requeue this gate on NotFound only; ADR-0057 Scope leaves rotation out; a probe |
| A Secret a Lake's engine binds changes | The engine holds it in its environment, so it is replaced with the new value | within `runtime.supervisionPeriod` 10 s | the engine restarts | accepted | ADR-0249 Scope, #1195, `TestIssue1195_ConvergeReplacesAnEngineOfAnotherSpec`; no lane rotates it |
| A `secrets.encryptionKeyFile` that does not match the stored Secrets | funcd does not start | none | `stored Secrets do not decrypt with secrets.encryptionKeyFile: they were written with a different key or with none` | accepted | #93, `TestIssue93_KeyMismatchFailsAtStartup` |
| `server.network.egress`, off by default | Off, or not Linux with containerd: workers reach anything and an EgressPolicy does nothing. On: a Function no EgressPolicy covers reaches nothing external | none | on, a refused connection | decided | ADR-0117 Decisions 3 and 6, `TestScenarioDisabledPassthrough`, egress `no-egresspolicy-denies-all` |
| A refused connection | It opens, then ends once the gateway has decided: an HTTP 403 on ports 80 and 8080, a plain close on any other | up to `peekTimeout` 500 ms | 403 `egress denied by funcd EgressPolicy`, or a closed socket | decided | ADR-0117 Decision 1, `TestScenarioDecideFlow`, egress `cidr-denies-unlisted-ip` |
| A domain rule | Counts only for a name this worker looked up through funcd, also through a CNAME, until that answer's TTL: a hard-coded address or a cache kept longer is refused | the answer's TTL, at least `minRecordTTL` 1 s | a refused connection | decided | ADR-0117 Decisions 1 and 2, ADR-0315 Decision 2, `TestScenarioDomainsForCorrelationAndWildcard`, `TestScenarioRuleFiresThroughACNAME` |
| A worker that just started | Its connections and lookups are refused until the next sync finds the worker | up to `server.network.workerSyncInterval` 2 s | a refused connection, `unknown source worker <ip>` in the audit record; REFUSED | decided | ADR-0163 scenario `server-keys-pace-shutdown-and-egress-sync`, ADR-0315 Consequences, `TestScenarioServerKeysPaceShutdownAndEgressSync` |
| A domain rule at a private, link-local, loopback, shared or node address | Permits nothing there, whatever the lookup answered. A `cidrs` rule that holds the address still does, on its ports | none | a refused connection, with no `domain` in the audit record | decided | ADR-0315 Decision 1, `TestScenarioDomainRulePublicOnly`, `TestScenarioCIDRRuleNamesTheAddress` |
| A worker's lookup, egress on | Goes to the resolver only for a name a domain rule applying to the worker allows, exact or `*.`, in host-name characters | none | REFUSED, which libraries report as a temporary name-resolution error | decided | ADR-0315 Decision 2, `TestScenarioLookupAllowedName`, `TestScenarioLookupRefusals` |
| A lookup of a name outside host-name characters | Refused, under a wildcard too: a name holds only letters, digits, `-`, `_` and the dots between labels | none | REFUSED | decided | ADR-0315 Decision 2, `TestScenarioLookupNameOutsideHostCharacters` |
| A lookup from a source that is no worker, or when the EgressPolicies cannot be read | Refused, and nothing goes upstream | none | REFUSED | decided | ADR-0315 Decision 2, `TestScenarioLookupRefusals` |
| A host reached by name under a CIDR rule | Its name needs a domain rule too; by address it needs nothing more | none | REFUSED | accepted | ADR-0315 Consequences, `TestScenarioCIDRRuleNameRefused` |
| A name funcd hands a worker, such as a hostname in `s3gateway.endpoint` | Needs a domain rule like any other name; a worker holds no `/etc/hosts`, so `localhost` needs one too | none | REFUSED | accepted | ADR-0315 Consequences |
| An operator's Cedar `Policy` by domain | Its name needs an EgressPolicy domain rule, as the forwarder reads only those, and it reaches public addresses only; a `forbid` stops the connection, not the lookup | none | REFUSED | accepted | ADR-0315 Consequences |
| The DNS forwarder's listener | Binds the bridge address on UDP and TCP, retried until that address exists, so a lookup before the first worker's bridge fails | `bindRetry` 1 s | `the egress dns forwarder cannot listen yet; retrying each second`, once | decided | ADR-0315 Decision 2, ADR-0238 Decision 3, `TestBuildEgressBindsTheBridgeAddress`, `TestScenarioForwarderWaitsForTheBridge` |
| `runtime.containerd.bridgeAddress` is not the bridge's first address | The kernel's redirect picks that first address, so no lookup reaches the forwarder | none | a failed lookup | accepted | ADR-0315 Consequences |
| A refused lookup's log line | At most one per worker per interval, naming the first name, the reason and how many were held back; sources that are no worker share one | `refusedLookupLogInterval` 10 s | `egress lookup refused` | decided | ADR-0315 Decision 2, `TestScenarioRefusedLookupAudited` |
| `server.network.egress` on and `server.network.dnsForwarderPort` 0 | No forwarder and no gateway run, and a worker's DNS has no rule: nothing goes out, and `server.network.dnsResolver` is not read | none | a failed lookup, a refused connection | decided | ADR-0315 Decision B, `TestScenarioNoForwarderNothingOut` |
| A connection's decision | Logged in funcd's own log, not yet in the function log channel the ADR names | none | `egress decision`, with `allowed=false` and the reason | deferred | ADR-0117 Open questions, egress `blocked-connection-audited` |
| A Lake or SQL engine's outbound TCP, on containerd | Reaches only its one internal endpoint, with `server.network.egress` on or off | none | a failed query; `GatewayEndpointNotAnAddress` if that endpoint is a hostname | decided | ADR-0269 Decisions 1 to 4, `TestScenarioLakeSetsEgressOnly` |
| A start, stop or crash, egress on | The rules go in before any worker runs and stay until the runtime closes; after a crash they stay, so external connections fail until the next start | none | `apply worker egress isolation` if they cannot go in | decided | ADR-0115 Decision, ADR-0117 Constraints, `TestRun_AppliesTheEgressFenceBeforeAnyLoop`, `TestShutdown_KeepsEgressFenceUntilRuntimeClosed` |

<!-- generated values, do not edit: just behavior-values -->
<details>
<summary>Security: values</summary>

| Value | Default | From |
|---|---|---|
| `auth.credentials` | none | config |
| `auth.namespaces` | [default] | config |
| `auth.token` | a built-in dev token | config |
| `secrets.encryptionKeyFile` | none | config |
| `bindRetry` | 1s | `internal/network/egress/forwarder.go` |
| `dnsTimeout` | 5s | `internal/network/egress/forwarder.go` |
| `maxPairsPerSource` | 1024 | `internal/network/egress/forwarder.go` |
| `minRecordTTL` | 1s | `internal/network/egress/forwarder.go` |
| `minSweep` | 1024 | `internal/network/egress/forwarder.go` |
| `refusedLookupLogInterval` | 10s | `internal/network/egress/forwarder.go` |
| `peekTimeout` | 500ms | `internal/network/egress/gateway_linux.go` |

</details>
<!-- end of generated values -->
