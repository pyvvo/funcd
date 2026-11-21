# ADR-0338: A Function calls an add-on object's engine through funcd

- **Status**: Proposed (2026-11-19), rounds 1 and 2 of an independent judge folded, with the decider's choices of
  2026-11-21
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, local-api, shims, auth, network
- **Realizes**: [FEAT-0019/F264](../feat/0019-feat-addon-apps.md) (a Function calls an add-on object's engine)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md)
  Decision 3 (funcd reaches an engine for a Function that binds its object: Decisions 2, 3 and 7) and
  [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md) Decision 1 (both paths are gated: Decisions 2 and 3).
- **Relates to**: [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) (facets, unchanged),
  [ADR-0243](0243-http-functions.md) Decision 6 (`/fetch/`, the pass-through pattern),
  [ADR-0233](0233-sql-access-from-functions.md) (`ctx.sql` and its Cedar pattern), [ADR-0137](0137-per-caller-catalog-query-rbac.md) and
  [ADR-0306](0306-workload-hooks-check-a-functions-bindings-and-give-its-workers-their-env.md) Decision 7 (the Lake's
  proxy and its derived token), [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the S3 endpoint and key
  pair an SDK uses), [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (causes),
  [ADR-0248](0248-stores-at-the-root.md) (the root-entry rule), [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md)
  (the dependency check), ADR-0337 (an App holds the object), ADR-0341 (an operator Function uses the binding),
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (shim releases)

## Context & Need

An add-on's engine keeps some settings behind its own API: rauthy sets its password policy only with
`PUT /auth/v1/password_policy` and an API key. A Function, an App's or a settings kind's operator (ADR-0341), must
reach that API, but no workload reaches an engine: its input chain admits only funcd, and no funcd proxy that acts for a
workload connects to it (ADR-0332 Decision 3). The worker-node local API already reaches engines for workloads
(`ctx.sql`, ADR-0233) and passes a request through as written (`ctx.fetch`, ADR-0243 Decision 6). An add-on may also
ship an SDK or an OpenAPI description, which a Function should use as a blob user uses the AWS SDK. The decider chose,
on 2026-11-19 and 2026-11-21, the add-on provider's own facet with a Cedar check per call, plus a base URL and a token
per binding for any HTTP SDK.

## Scenarios

- `scenario: addon-call` — *Given* the Function `rauthy-settings` binds the object `rauthy-login` as `idp`, *When* its
  handler calls `ctx.addon.fetch("idp", "/auth/v1/password_policy", init)` with a header of its own, *Then* rauthy gets
  the request with that header, and the handler gets rauthy's status, headers and body.
- `scenario: addon-call-sdk` — *When* the handler sends the same request with a plain HTTP client to
  `FUNCD_ADDON_IDP_URL`, with `X-Funcd-Addon-Token: $FUNCD_ADDON_IDP_TOKEN` and its own `Authorization`, *Then* rauthy
  gets the request with that `Authorization` and without the token header, and the client gets rauthy's answer.
- `scenario: addon-call-refused` — *When* a Function calls an alias it does not bind, a Policy forbids `addon::call`,
  or a request to the proxy carries no token or a token of no Function, *Then* the call fails with 403, and no engine is
  called.
- `scenario: addon-call-path` — *When* the path lies outside the Addon's `basePath` or under one of its `privatePaths`,
  *Then* the call fails with 404, as the same path does at the edge.
- `scenario: addon-call-held` — *Given* the caller's namespace held by a Restore, *When* it calls, *Then* the call
  fails with 503 and `Retry-After: 30`, as the edge answers (ADR-0290 Decision 2).
- `scenario: addon-call-no-engine` — *Given* the bound object is not Ready, *When* the Function calls, *Then* the call
  fails with 503, and readiness reports `DependencyNotReady` with `EngineNotReady` for `addon binding "idp"`.
- `scenario: addon-call-timeout` — *Given* an engine that does not answer within `addons.callTimeout`, *Then* the call
  fails with 503 and the cause `local.addon-timeout`.
- `scenario: addon-call-too-large` — *Given* a request body, or an answer body, over `addons.callPayloadLimit`, *Then*
  the call fails with 413.
- `scenario: addon-call-headers` — *When* the caller sends `X-Forwarded-For`, `Forwarded`, `X-Real-IP` or an
  `X-Funcd-*` header, *Then* the engine gets none of them, and a 3xx from the engine reaches the caller unfollowed.
- `scenario: addon-binding-missing` — *Given* a Function that binds an object that does not exist, *When* it is
  applied, *Then* it waits `Pending` with `AddonObjectNotFound`.
- `scenario: addon-fence-stays` — *When* a Function dials the engine's address itself, *Then* the egress gateway (egress
  on) or the engine's input chain (egress off) refuses it.

## Scope

In: the binding, the facet, the proxy and its env, the Cedar rule, limits, causes and pooling, the shims'
`ctx.addon.fetch`, the fence's openings. Out: streaming (server-sent events, WebSocket), HTTP/2 and gRPC; a list of
the paths a binding may call; how funcd itself reaches engines (ADR-0336).

## Constraints & Decision drivers

- The fence stays closed to the network: no workload learns or dials an engine's address.
- Reuse the facets (ADR-0310), the pass-through (ADR-0243 Decision 6), the Lake's proxy and token (ADR-0137,
  ADR-0306), Cedar's binding pattern (ADR-0233 Decision 5) and the declared causes (ADR-0330).
- A namespace is the trust boundary: any Function of the namespace may bind its objects.
- A path that writes to an engine refuses a held namespace, as the edge's paths to engines do (ADR-0290).

## Alternatives considered

- **Give the Function the engine's address.** Rejected: it opens the fence to the network.
- **A fourth built-in route, answered before the mux.** Round 2's draft, rejected by the decider: the add-on provider
  serves its own facet, as kv, blob and sql do, and ADR-0310 stays as it is.
- **The SDK over the worker's Unix socket.** Rejected: most HTTP SDKs speak TCP only; the Lake's proxy and the S3
  gateway already give a TCP endpoint and a credential.
- **funcd's token in `Authorization`.** Rejected: the engine keeps that header for its own credentials, such as
  rauthy's `API-Key`.
- **Only Functions of the App that owns the object.** Rejected by the decider: the namespace is the trust boundary, and
  the engine's own credentials still guard what a caller may change.
- **The object's edge entry.** Rejected: it needs `spec.ingress` on each object, a domain rule reaches no node address
  (ADR-0317 Decision 1), and each call goes out through the edge and back.

## Decision

1. **The binding.** `FunctionSpec.bindings.addon` lists entries of `alias` and `object`, an add-on object of the
   Function's namespace. An App's Function binds only an object its App declares (ADR-0248 Decision 2: `RootDeclared`
   gains `AppSpec.addons`). A standalone Function binds any add-on object of its namespace. A missing object holds the
   Function `Pending` with `AddonObjectNotFound` (`internal/function/references.go`); an object that is not Ready, or a
   call Cedar denies, fails readiness through the dependency check (ADR-0308 Decisions 2 and 5), with `EngineNotReady`
   or `Forbidden`. The pool's access document (`accessDoc`, `internal/pooling/pooling.go:52`) includes
   `bindings.addon`. A bound object counts as in use for an App's prune (ADR-0199 Decision 6), and how its revision
   moves around an App's hooks is ADR-0339 Decision 3's.
2. **The facet.** The add-on provider returns the facet `addon` (ADR-0310 Decisions 2 and 4), serving
   `/addon/{alias}/{path...}` for any method. It resolves `alias` in the caller's bindings, asks Cedar (Decision 5), and
   refuses while the caller's namespace is held (ADR-0290 Decision 2's 503 and `Retry-After`) or while the object is not
   Ready. It checks the decoded path as the edge does for that object (ADR-0332 Decision 5's `keptPath`: no `..`
   segment, under `basePath`, under no private path, so an encoded spelling of a private path is refused too), and sends
   that cleaned path from the host to the object's `status.address`, with `Host` that address. It forwards the method,
   the query, the body and the headers but the hop-by-hop ones, `X-Funcd-*`, `X-Forwarded-For`, `Forwarded`,
   `X-Real-IP`, `X-Forwarded-Host` and `X-Forwarded-Proto`. It follows no redirect. It returns the engine's status,
   headers (the same filter) and body, and sets `X-Funcd-Addon-Reply: 1` after that filter, so the shim tells the
   engine's answers from funcd's refusals.
3. **The proxy, for any HTTP SDK.** A node-private add-on proxy, as the Lake's (ADR-0137), listens where a worker
   reaches funcd's gateways. Each `bindings.addon` entry gives the worker `FUNCD_ADDON_<ALIAS>_URL`, the proxy's base
   URL for that alias, and `FUNCD_ADDON_<ALIAS>_TOKEN`, derived per Function as its lake token is (ADR-0306 Decision 7).
   A request carries the token in `X-Funcd-Addon-Token`; the proxy resolves it to the Function, then makes Decision 2's
   checks and call, and the token header, an `X-Funcd-*` one, never reaches the engine.
4. **Limits.** `addons.callTimeout`, a positive duration with empty meaning 10s, as `sql.callTimeout`; the request's
   own context also bounds a call. `addons.callPayloadLimit`, in bytes, with 0 meaning 1 MiB, as
   `addons.maxObjectsPerNamespace` reads 0; a negative value is refused at load. It bounds the request's body and the
   answer's body, on both paths.
5. **Cedar.** A capability `addon` with the action `addon::call` (`internal/auth`), the entity
   `AddonObject::"<ns>/<name>"` with a `namespace` attribute, and a principal attribute `addonBindings` from
   `spec.bindings.addon`. The capability joins `defaultRegistry` and the root registry, and `resourceUID` gains
   `KindAddonObject`. In the built-ins, a binding permits `addon::call`; operator Policies may forbid it. Both paths ask
   it on every call.
6. **Causes** (ADR-0330), each a funcd refusal without the reply header, on both paths:

   | Condition | Kind | Cause |
   |---|---|---|
   | an alias the caller does not bind | Forbidden 403 | `local.addon-no-binding` |
   | Cedar denies | Forbidden 403 | `local.addon-forbidden` |
   | the proxy gets no token, or one of no Function (new) | Forbidden 403 | `local.addon-token-refused` |
   | no alias in the path, or a path the edge would refuse | NotFound 404 | `local.addon-path-refused` |
   | the caller's namespace is held | Unavailable 503 | `local.addon-held` |
   | the object is not Ready, has no engine address, or the engine refuses the connection | Unavailable 503 | `local.addon-engine-unavailable` |
   | no answer within the timeout | Unavailable 503 | `local.addon-timeout` |
   | a request body over the limit, or unreadable | PayloadTooLarge 413, Invalid 400 | `local.body-too-large`, `local.body-unreadable` |
   | an answer body over the limit, or cut off | PayloadTooLarge 413, Unavailable 503 | `local.addon-reply-too-large`, `local.addon-reply-cut-off` |

   A timeout is 503, as `local.sql-timeout` is. Every answer of the engine, any status, carries the reply header and
   passes through.
7. **The fence.** ADR-0332 Decision 3 holds but for the facet and the proxy, which are funcd's: the input chain admits
   only loopback and funcd's bridge address, and the egress gateway refuses every engine address.
8. **The shims.** funcd-typescript adds `ctx.addon: AddonClient` with `fetch(alias, path, init?)`, built as
   `makeFetch` builds `ctx.fetch`: it sends the member header and `traceparent`, resolves with a `Response` for an
   answer that carries the reply header, and rejects with the problem for a funcd refusal, its `retryAfter` taken from
   a 503's `Retry-After`. funcd-python adds
   `ctx.addon.fetch(alias, path, method="GET", headers=None, body=None)`, which returns an `AddonResponse` with
   `status: int`, `headers: dict[str, str]` (names in lower case, a repeated header's values joined with `, `) and
   `body: bytes`, and raises `RuntimeError` with the problem, and the `Retry-After` of a 503, for a funcd refusal, as
   `ctx.invoke` does. `kindref` gains `addon.fetch` in both languages (ADR-0330 Decision 8), and funcd pins both
   releases (ADR-0141).

## Design model

`docs/design/addon/restore-start.fizz` (ADR-0333) holds as it is: its guard on `Write` is that a write reaches the
engine only while the namespace is not held, and Decisions 2 and 3 make both paths refuse a held namespace, so each is
one more writer under that guard. A scratch copy of the spec with a writer that skips the guard fails
`NoAckedWriteRolledBack` in eight steps, and the unchanged spec passes (69 states, FizzBee v0.5.4). The rest of the
decision is a stateless request through funcd, with no lifecycle and no retry; who may reach an engine is a static
rule held by tests and a lane case.

## Behavior rows

- **calls.md**: "A Function calls an add-on object's engine" (the facet, the proxy, the causes, the two limits),
  `decided`.
- **addons.md**: "Who reaches an engine" (`addons.md:36`) changes: funcd, and through funcd a Function that binds the
  object.
- **workers.md**: the binding-check row (`workers.md:46`) gains `addon`, and the env rows gain the two variables.
- **limits.md**: the cap that no key changed (`limits.md:62`) gains the two keys for these paths.
- **security.md**: "Principal headers a caller sends" (`security.md:42`) are dropped on both paths, and so is the token.
- **disaster-recovery.md**: the paths a held namespace refuses gain both.

## Temporary workarounds

None.

## Contracts

```go
// FunctionAddon is an entry of FunctionBindings.Addon (ADR-0338 Decision 1).
type FunctionAddon struct {
	Alias  string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" doc:"The name the handler calls the object by."`
	Object ObjectName `json:"object" doc:"An add-on object of the Function's namespace, declared by its App when an App holds the Function."`
}
```

```ts
interface AddonClient {
  fetch(alias: string, path: string, init?: RequestInit): Promise<Response>;
}
```

```python
def fetch(self, alias: str, path: str, method: str = "GET", headers: dict[str, str] | None = None,
          body: bytes | None = None) -> AddonResponse: ...
```

```cedar
@id("addon-call-binding")
permit (principal, action == Action::"addon::call", resource)
when { principal has addonBindings && principal.addonBindings.contains(resource) };
```

| Consumes | Exposes |
|---|---|
| the sandbox's socket and identity; the proxy's token | `/addon/<alias>/<path>`; the proxy; `X-Funcd-Addon-Reply` |
| the object's `status.address`, its Addon's `basePath` and `privatePaths` | `FUNCD_ADDON_<ALIAS>_URL`, `FUNCD_ADDON_<ALIAS>_TOKEN`, `X-Funcd-Addon-Token` |
| `addons.callTimeout`, `addons.callPayloadLimit` | `addon::call` in Cedar's schema; the causes of Decision 6 |

## Implementation plan

- `api/types/v1alpha1`: `FunctionAddon`, `FunctionBindings.Addon`, `Validate`; regenerate the spec and kindref.
- `internal/auth`, `internal/auth/cedar`: the action, the capability in both registries, `resourceUID`, the built-in,
  the Secret capability's comment on add-on objects (`capabilities.go:600`).
- `internal/workernode/local`: `AddonFacet`, its causes, the hold check, the path check, the header filter, the reply
  header; the ADR-0330 matrix test for it. The proxy beside the Lake's, sharing the facet's checks and call.
- `internal/function`, `internal/controlplane/admission`, `internal/pooling`, `pkg/funcd`: the existence gate,
  `RootDeclared`, the access document, the dependency check, the env hook, the provider's facet, the two keys.
- funcd-typescript and funcd-python: `ctx.addon.fetch` and its tests, each released; funcd pins both.
- Docs: the Functions guide (the shim's client and an SDK through the proxy), the add-ons pages, the behavior rows,
  the config reference.
- Tests: one per Scenario, named after it, including encoded spellings of a private path. Real platform: a case of
  the lane `addon-rauthy-signin`, whose Function, in pyvvo/funcd-functions, released and pinned, sends a header through
  its binding to the lane's whoami Addon, which echoes it, once through `ctx.addon.fetch` and once through the proxy,
  gets 404 for a private path, and fails to dial the engine's address.
- Done: `just ci` green, the scenarios and the lane case passing, both shims released and pinned.

## Review checklist

- [ ] Both paths answer only for an alias of the caller's own bindings, Cedar decides each call, and a held namespace
      is refused.
- [ ] A path the edge refuses for the object is refused here; no redirect is followed.
- [ ] No answer or log of funcd names the engine's address, and the forwarding, `X-Funcd-*` and token headers never
      reach the engine.
- [ ] The pool's access document includes `bindings.addon`; the env carries one URL and one token per alias.
- [ ] Each cause of Decision 6 has a test; the limits are config keys; both shims are pinned at their releases.

## Consequences

- An add-on's App or operator applies and repairs its engine's settings, with no new network path to the engine.
- funcd checks only who may reach the engine; what a caller may change stays the engine's own rule, such as rauthy's
  API key with `Secrets` update rights.
- Each call adds one local hop through funcd, and a call during a Restore fails with 503 and `Retry-After`, for the
  caller to retry.
- An SDK works when it can send one more header; one that speaks HTTP/2 or gRPC waits for a follow-up ADR.
- The paths drop only the headers Decision 2 names: an Addon must not make its engine trust another header for the
  caller's address, as rauthy's `PEER_IP_HEADER_NAME` would; rauthy's Addon sets none.
- Two shim releases come before the feature ships.

## Open questions

- Streaming, HTTP/2 and gRPC through the paths, and a list of the paths a binding may call: follow-up ADRs when an
  add-on needs them.

## References

- Feature issue [#1505](https://github.com/pyvvo/funcd/issues/1505) (FEAT-0019 F264); tracker #1506.
- rauthy's password policy route and its rights: `src/api/src/generic.rs:304` in pyvvo/funcd-rauthy.
