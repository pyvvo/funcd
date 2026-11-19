# ADR-0338: A Function calls an add-on object's engine through funcd's local API

- **Status**: Proposed (2026-11-19), rounds 1 and 2 of an independent judge folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, local-api, shims, auth, network
- **Realizes**: [FEAT-0019/F264](../feat/0019-feat-addon-apps.md) (a Function calls an add-on object's engine)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md)
  Decision 3 (the `/addon/` route reaches an engine for a Function that binds its object: Decision 6) and
  [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md) Decision 1 (the route is a gated path: Decision 2), and
  [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) Decisions 3 and 4 (a fourth built-in route, whose
  segment `addon` no facet may take: Decision 2).
- **Relates to**: [ADR-0243](0243-http-functions.md) Decision 6 (`/fetch/`, the route's pattern),
  [ADR-0233](0233-sql-access-from-functions.md) (`ctx.sql` and its Cedar pattern),
  [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (causes),
  [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) (facets and the mux),
  [ADR-0248](0248-stores-at-the-root.md) (the root-entry rule), [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md)
  (the dependency check), ADR-0337 (an App holds the object), [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md)
  (shim releases)

## Context & Need

An add-on's engine keeps some settings behind its own API: rauthy sets its password policy only with
`PUT /auth/v1/password_policy` and an API key. An add-on's App (ADR-0337) can run a Function after each revision and on
a timer, but no workload reaches an engine: its input chain admits only funcd, and no funcd proxy that acts for a
workload connects to it (ADR-0332 Decision 3). The worker-node local API already passes a Function's request to a
target as it was written, `ctx.fetch` over `/fetch/` (ADR-0243 Decision 6), and reaches engines for workloads
(`ctx.sql`, ADR-0233). The decider chose, on 2026-11-19, the same path for add-on engines.

## Scenarios

- `scenario: addon-call` — *Given* the Function `rauthy-settings` binds the object `rauthy-login` as `idp`, *When* its
  handler calls `ctx.addon.fetch("idp", "/auth/v1/password_policy", init)` with a header of its own, *Then* rauthy gets
  the request with that header, and the handler gets rauthy's status, headers and body.
- `scenario: addon-call-refused` — *When* a Function calls an alias it does not bind, or a Policy forbids
  `addon::call`, *Then* the call fails with 403, and no engine is called.
- `scenario: addon-call-path` — *When* the path lies outside the Addon's `basePath` or under one of its `privatePaths`,
  *Then* the call fails with 404, as the same path does at the edge.
- `scenario: addon-call-held` — *Given* the caller's namespace held by a Restore, *When* it calls, *Then* the call
  fails with 503 and `Retry-After: 30`, as the edge answers (ADR-0290 Decision 2).
- `scenario: addon-call-no-engine` — *Given* the bound object is not Ready, *When* the Function calls, *Then* the call
  fails with 503, and readiness reports `DependencyNotReady` with `EngineNotReady` for `addon binding "idp"`.
- `scenario: addon-call-timeout` — *Given* an engine that does not answer within `addons.callTimeout`, *Then* the call
  fails with 504.
- `scenario: addon-call-too-large` — *Given* a request body, or an answer body, over `addons.callPayloadLimit`, *Then*
  the call fails with 413.
- `scenario: addon-call-headers` — *When* the caller sends `X-Forwarded-For`, `Forwarded`, `X-Real-IP` or an
  `X-Funcd-*` header, *Then* the engine gets none of them, and a 3xx from the engine reaches the caller unfollowed.
- `scenario: addon-binding-missing` — *Given* a Function that binds an object that does not exist, *When* it is
  applied, *Then* it waits `Pending` with `AddonObjectNotFound`.
- `scenario: addon-fence-stays` — *When* a Function dials the engine's address itself, *Then* the egress gateway (egress
  on) or the engine's input chain (egress off) refuses it.

## Scope

In: the binding, the route, its Cedar rule, limits, causes and pooling, the shims' `ctx.addon.fetch`, the fence's one
opening. Out: streaming (server-sent events, WebSocket); a list of the paths a binding may call; how funcd itself
reaches engines (ADR-0336).

## Constraints & Decision drivers

- The fence stays closed to the network: no workload learns or dials an engine's address.
- Reuse the local API's pass-through route (ADR-0243 Decision 6), Cedar's binding pattern (ADR-0233 Decision 5) and the
  declared causes (ADR-0330).
- A namespace is the trust boundary: any Function of the namespace may bind its objects.
- A route that writes to an engine refuses a held namespace, as the edge's paths to engines do (ADR-0290); `ctx.sql`
  stays outside ADR-0290's scope.

## Alternatives considered

- **Give the Function the engine's address.** Rejected: it opens the fence to the network.
- **A proxy of its own.** Rejected: the local API already authenticates the sandbox and asks Cedar.
- **Only Functions of the App that owns the object.** Rejected by the decider: the namespace is the trust boundary, and
  the engine's own credentials still guard what a caller may change.
- **The object's edge entry.** Rejected: it needs `spec.ingress` on each object, a domain rule reaches no node address
  (ADR-0317 Decision 1), and each call goes out through the edge and back.
- **Settings as the engine's environment.** Rejected: each change replaces the engine
  (`internal/provider/runtime.go:89-90`), and nothing repairs drift.

## Decision

1. **The binding.** `FunctionSpec.bindings.addon` lists entries of `alias` and `object`, an add-on object of the
   Function's namespace. An App's Function binds only an object its App declares (ADR-0248 Decision 2: `RootDeclared`
   gains `AppSpec.addons`). A standalone Function binds any add-on object of its namespace: add-on objects stand alone
   in a namespace (ADR-0332), so a root section on a Function would only repeat their names, and the namespace stays
   the boundary. A missing object holds the Function `Pending` with `AddonObjectNotFound`
   (`internal/function/references.go`); an object that is not Ready, or a call Cedar denies, fails readiness through the
   dependency check (ADR-0308 Decisions 2 and 5), with `EngineNotReady` or `Forbidden`. The pool's access document
   (`accessDoc`, `internal/pooling/pooling.go:52`) includes `bindings.addon`. A bound object counts as in use for an
   App's prune (ADR-0199 Decision 6), and a pre-hook's bound object is among what ADR-0214 writes before its pre-hooks
   run; how a change of the object's add-on version meets those hooks is FEAT-0019 F265's decision, pending.
2. **The route.** `/addon/<alias>/<path>`, any method, is a fourth built-in route of the worker-node local API beside
   `/fetch/` (ADR-0310 Decision 3), answered before the mux and through `wrap` as `/fetch/` is (ADR-0243 Decision 6);
   `CheckFacets` refuses a facet named `addon` (ADR-0310 Decision 4). It resolves `alias` in the caller's
   bindings, asks Cedar (Decision 4), and refuses while the caller's namespace is held (ADR-0290 Decision 2's 503 and
   `Retry-After`). It checks the decoded path as the edge does for that object (ADR-0332 Decision 5's `keptPath`: no
   `..` segment, under `basePath`, under no private path, so an encoded spelling of a private path is refused too) and
   sends that cleaned path from the host to the object's
   `status.address`, with `Host` that address. It forwards the method, the query, the body and the headers but the
   hop-by-hop ones, `X-Funcd-*`, `X-Forwarded-For`, `Forwarded`, `X-Real-IP`, `X-Forwarded-Host` and
   `X-Forwarded-Proto`. It follows no redirect. It returns the engine's status, headers (the same filter) and body, and
   sets `X-Funcd-Addon-Reply: 1` after that filter, so the shim tells the engine's answers from funcd's refusals.
3. **Limits.** `addons.callTimeout`, a positive duration with empty meaning 10s, as `sql.callTimeout`; the request's
   own context also bounds a call. `addons.callPayloadLimit`, in bytes, with 0 meaning 1 MiB, as
   `addons.maxObjectsPerNamespace` reads 0; a negative value is refused at load. It bounds the request's body and the
   answer's body.
4. **Cedar.** A capability `addon` with the action `addon::call` (`internal/auth`), the entity
   `AddonObject::"<ns>/<name>"` with a `namespace` attribute, and a principal attribute `addonBindings` from
   `spec.bindings.addon`. The capability joins `defaultRegistry` and the root registry, and `resourceUID` gains
   `KindAddonObject`. In the built-ins, a binding permits `addon::call`; operator Policies may forbid it.
5. **Causes** (ADR-0330), each a funcd refusal without the reply header:

   | Condition | Kind | Cause |
   |---|---|---|
   | an alias the caller does not bind | Forbidden 403 | `local.addon-no-binding` |
   | Cedar denies | Forbidden 403 | `local.addon-forbidden` |
   | no alias in the path, or a path the edge would refuse | NotFound 404 | `local.addon-path-refused` |
   | the caller's namespace is held | Unavailable 503 | `local.addon-held` |
   | the object is not Ready, has no engine address, or the engine refuses the connection | Unavailable 503 | `local.addon-engine-unavailable` |
   | no answer within the timeout | DeadlineExceeded 504 | `local.addon-timeout` |
   | a request body over the limit, or unreadable | PayloadTooLarge 413, Invalid 400 | `local.body-too-large`, `local.body-unreadable` |
   | an answer body over the limit, or cut off | PayloadTooLarge 413, Unavailable 503 | `local.addon-reply-too-large`, `local.addon-reply-cut-off` |

   A 504 is new on the local API, where `local.sql-timeout` answers 503: an engine that does not answer is a timeout,
   which `DeadlineExceeded` names. Every answer of the engine, any status, carries the reply header and passes through.
6. **The fence.** ADR-0332 Decision 3 holds but for Decision 2's route: the input chain admits only loopback and funcd's
   bridge address, and the egress gateway refuses every engine address.
7. **The shims.** funcd-typescript adds `ctx.addon: AddonClient` with `fetch(alias, path, init?)`, built as
   `makeFetch` builds `ctx.fetch`: it sends the member header and `traceparent`, resolves with a `Response` for an
   answer that carries the reply header, and rejects with the problem for a funcd refusal, its `retryAfter` taken from
   a 503's `Retry-After`. funcd-python adds
   `ctx.addon.fetch(alias, path, method="GET", headers=None, body=None)`, which returns an `AddonResponse` with
   `status: int`, `headers: dict[str, str]` (names in lower case, a repeated header's values joined with `, `) and
   `body: bytes`, and raises `RuntimeError` with the problem, and the `Retry-After` of a 503, for a funcd refusal, as
   `ctx.invoke` does. `kindref` gains `addon.fetch` in both languages (ADR-0330 Decision 8), and funcd pins
   both releases (ADR-0141).

## Design model

`docs/design/addon/restore-start.fizz` (ADR-0333) holds as it is: its guard on `Write` is that a write reaches the
engine only while the namespace is not held, and Decision 2 makes the route refuse a held namespace, so the route is
one more writer under that guard. A scratch copy of the spec with a writer that skips the guard fails
`NoAckedWriteRolledBack` in eight steps, and the unchanged spec passes (69 states, FizzBee v0.5.4). The rest of the
decision is a stateless request through funcd, with no lifecycle and no retry; who may reach an engine is a static
rule held by tests and a lane case.

## Behavior rows

- **calls.md**: "A Function calls an add-on object's engine" (the route, the causes, the two limits), `decided`.
- **addons.md**: "Who reaches an engine" (`addons.md:36`) changes: funcd, and through the local API a Function that
  binds the object.
- **workers.md**: the binding-check row (`workers.md:46`) gains `addon`.
- **limits.md**: the cap that no key changed (`limits.md:62`) gains the two keys for this route.
- **security.md**: "Principal headers a caller sends" (`security.md:42`) are dropped on `/addon/` too.
- **disaster-recovery.md**: the paths a held namespace refuses gain `/addon/`.

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
| the sandbox's socket and identity | `/addon/<alias>/<path>`, `X-Funcd-Addon-Reply` |
| the object's `status.address`, its Addon's `basePath` and `privatePaths` | the engine's answer, or a cause of Decision 5 |
| `addons.callTimeout`, `addons.callPayloadLimit` | `addon::call` in Cedar's schema |

## Implementation plan

- `api/types/v1alpha1`: `FunctionAddon`, `FunctionBindings.Addon`, `Validate`; regenerate the spec and kindref.
- `internal/auth`, `internal/auth/cedar`: the action, the capability in both registries, `resourceUID`, the built-in,
  the Secret capability's comment on add-on objects (`capabilities.go:600`).
- `internal/workernode/local`: the route before the mux, its causes, the hold check, the path check, the header
  filter, the reply header; the ADR-0330 matrix test for it.
- `internal/function`, `internal/controlplane/admission`, `internal/pooling`, `pkg/funcd`: the existence gate,
  `RootDeclared`, the access document, the dependency check, the two keys.
- funcd-typescript and funcd-python: `ctx.addon.fetch` and its tests, each released; funcd pins both.
- Docs: the Functions guide, the add-ons pages, the behavior rows, the config reference.
- Tests: one per Scenario, named after it, including encoded spellings of a private path. Real platform: a case of
  the lane `addon-rauthy-signin`, whose Function, in pyvvo/funcd-functions, released and pinned, sends a header through
  its binding to the lane's whoami Addon, which echoes it, gets 404 for a private path, and fails to dial the engine's
  address.
- Done: `just ci` green, the scenarios and the lane case passing, both shims released and pinned.

## Review checklist

- [ ] The route answers only for an alias of the caller's own bindings, Cedar decides each call, and a held namespace
      is refused.
- [ ] A path the edge refuses for the object is refused here; no redirect is followed.
- [ ] No answer or log of funcd names the engine's address, and the forwarding and `X-Funcd-*` headers never reach the
      engine.
- [ ] The pool's access document includes `bindings.addon`.
- [ ] Each cause of Decision 5 has a test; the limits are config keys; both shims are pinned at their releases.

## Consequences

- An add-on's App can apply and repair its engine's settings, with no new network path.
- funcd checks only who may reach the engine; what a caller may change stays the engine's own rule, such as rauthy's
  API key with `Secrets` update rights.
- Each call adds one local hop through funcd, and a call during a Restore fails with 503 and `Retry-After`, for the
  caller to retry.
- The route drops only the headers Decision 2 names: an Addon must not make its engine trust another header for the
  caller's address, as rauthy's `PEER_IP_HEADER_NAME` would; rauthy's Addon sets none.
- Two shim releases come before the feature ships.

## Open questions

- Streaming through the route, and a list of the paths a binding may call: follow-up ADRs when an add-on needs them.

## References

- Feature issue [#1505](https://github.com/pyvvo/funcd/issues/1505) (FEAT-0019 F264); tracker #1506.
- rauthy's password policy route and its rights: `src/api/src/generic.rs:304` in pyvvo/funcd-rauthy.
