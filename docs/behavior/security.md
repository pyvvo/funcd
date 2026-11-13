# Security

Status words and how to read this: [the index](README.md).

Kinds: Identity, Role, RolesAssignment, Policy, Grant, Secret.

```mermaid
stateDiagram-v2
    [*] --> Authn : an API call, or a call on an authenticated route
    [*] --> Principal : a worker's or an Identity's data call
    Authn --> Refused : no token, or an unlisted one
    Authn --> rbac : the token's role and namespaces
    rbac --> Allowed : the role covers the verb, kind and namespace
    rbac --> Refused : it does not
    Principal --> cedar : principal, action, resource
    cedar --> Allowed : a permit and no forbid
    cedar --> Refused : no permit, or a forbid
    Allowed --> [*]
    Refused --> [*]
```

The API and the edge read one token list; `/openapi`, `/docs` and `/schemas/` need no token. A binding grants read, the owner or a
writer role grants write, and the built-in `Owner` covers KV and blob data only, not managing assignments (ADR-0136). An
Identity's credential authenticates and grants nothing until a RolesAssignment does. A Policy naming another namespace in its
scope is refused at apply (ADR-0177 Decision 3). A worker's outbound traffic and lookups: [Egress](egress.md). New words: Refused.

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
| `ctx.secret.get` or `list` | Asks funcd on each read, with no cache, and Cedar decides each bound Secret as the Function: a changed Secret shows at the next read, at a round trip per read | none | the new value, no restart | decided | ADR-0249 Decision 2 and Consequences, ADR-0321 Decision 3, `TestScenarioSecretReadLive`, `TestScenarioSecretBoundReads` |
| A Policy forbids a Function's read of a bound Secret | Each next `ctx.secret` read fails, of any key, and readiness fails ([Workers](workers.md)): with no ready replica it is `Degraded`, its calls held | the next read; `runtime.supervisionPeriod` 10 s | 403 `cedar: forbidden by policy <id>`; `DependencyNotReady` | decided | ADR-0321 Decisions 3 and 4, `TestScenarioSecretForbidReadiness` |
| A bound Secret is missing | No worker starts; one already serving goes on, and `ctx.secret.get` reads nothing; a Secret applied later is found at the re-check | `controller.referentPollInterval` 2 s | `Failed` or `RevisionReady=False`, `SecretResolveFailed` | decided | ADR-0249 Decisions 2 to 4, ADR-0221 Decision 3, `TestIssue77_MissingBindingRecoversWhenApplied` |
| A bound Secret value that is not UTF-8 or holds a NUL | No worker starts; a corrected Secret does not clear it until the Function changes or funcd restarts | none | `Failed`, `SecretResolveFailed`, `is not valid UTF-8; env delivery cannot carry binary data` | **uncovered** | ADR-0306 Contracts requeue this gate on NotFound only; ADR-0057 Scope leaves rotation out; a probe |
| A Secret a Lake's engine binds changes | The engine holds it in its environment, so it is replaced with the new value | within `runtime.supervisionPeriod` 10 s | the engine restarts | accepted | ADR-0249 Scope, #1195, `TestIssue1195_ConvergeReplacesAnEngineOfAnotherSpec`; no lane rotates it |
| A `secrets.encryptionKeyFile` that does not match the stored Secrets | funcd does not start | none | `stored Secrets do not decrypt with secrets.encryptionKeyFile: they were written with a different key or with none` | accepted | #93, `TestIssue93_KeyMismatchFailsAtStartup` |

<!-- generated values, do not edit: just behavior-values -->
<details>
<summary>Security: values</summary>

| Value | Default | From |
|---|---|---|
| `auth.credentials` | none | config |
| `auth.namespaces` | [default] | config |
| `auth.token` | a built-in dev token | config |
| `secrets.encryptionKeyFile` | none | config |

</details>
<!-- end of generated values -->
