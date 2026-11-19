# ADR-0338: A Function calls an add-on object's engine through funcd's local API

- **Status**: Proposed (2026-11-19), awaiting the judge
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, local-api, shims, auth, network
- **Realizes**: [FEAT-0019/F264](../feat/0019-feat-addon-apps.md) (a Function calls an add-on object's engine)
- **Supersedes (in part)**, with a back-link at acceptance: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md)
  Decision 3: the local API's `/addon/` route, acting for a Function that binds the object, connects to the engine's
  address; every other funcd proxy that acts for a workload still does not.
- **Relates to**: [ADR-0233](0233-sql-access-from-functions.md) (`ctx.sql` over the local API, and its Cedar pattern),
  [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (a route's causes), ADR-0337 (an App holds the
  object), [ADR-0214](0214-app-hooks.md) (hooks), [ADR-0317](0317-a-domain-rule-reaches-globally-reachable-addresses-only.md) (the egress gateway),
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (shim releases)

## Context & Need

An add-on's engine keeps some settings behind its own API: rauthy sets its password policy only with
`PUT /auth/v1/password_policy` and an API key. An add-on's App (ADR-0337) can run a Function after each revision and on
a timer, but no workload reaches an engine: its input chain admits only funcd, and no funcd proxy that acts for a
workload connects to it (ADR-0332 Decision 3). funcd already calls engines for workloads elsewhere: `ctx.sql` reaches
the `sql` provider's rqlite engines through the worker-node local API (ADR-0233), and the catalog proxy fronts the
Lake's engines (ADR-0137). The decider chose, on 2026-11-19, the same path for add-on engines.

## Scenarios

- `scenario: addon-call` — *Given* the Function `rauthy-settings` binds the object `rauthy-login` as `idp`, *When*
  its handler calls `ctx.addon.fetch("idp", "/auth/v1/password_policy", init)`, *Then* the request reaches rauthy's
  engine, and the handler gets rauthy's status, headers and body.
- `scenario: addon-call-unbound` — *When* a Function calls an alias it does not bind, *Then* it gets 404, and no
  engine is called.
- `scenario: addon-call-forbidden` — *Given* an operator Policy that forbids `addon::call` for the Function, *When* it
  calls, *Then* it gets 403.
- `scenario: addon-call-no-engine` — *Given* the bound object is not Ready, *When* the Function calls, *Then* it gets
  503 naming the object, and the Function's readiness reports `DependencyNotReady` naming `addon binding "idp"`.
- `scenario: addon-call-limits` — *Given* an engine that does not answer within `addons.callTimeout`, or a body over
  `addons.callPayloadLimit`, *When* the Function calls, *Then* it gets 504, or 413.
- `scenario: addon-fence-stays` — *Given* egress on, *When* a Function dials the engine's address itself, *Then* the
  egress gateway refuses it, as before.

## Scope

In: the binding, the route, its Cedar rule, limits and causes, the shims' `ctx.addon.fetch`, the fence's one opening.
Out: streaming (server-sent events, WebSocket) through the route; a list of the paths a binding may call; any change to
how funcd itself reaches engines (ADR-0336).

## Constraints & Decision drivers

- The fence stays closed to the network: no workload learns or dials an engine's address.
- Reuse the local API and its Cedar pattern (ADR-0233), and declare the route's causes (ADR-0330).
- A namespace is the trust boundary: any Function of the namespace may bind its objects, as with every other binding.
- A shim change is a release in its language repository first (ADR-0141).

## Alternatives considered

- **Give the Function the engine's address**, admitted by the input chain. Rejected: it opens the fence to the network,
  and every workload of the subnet could then try the engine.
- **A proxy of its own**, beside the local API. Rejected: the local API already authenticates the sandbox and asks
  Cedar; a second path would repeat both.
- **Only Functions of the App that owns the object.** Rejected by the decider: the namespace is the trust boundary, and
  the engine's own API key still guards what a caller may change.
- **Settings as the engine's environment.** Rejected: each change restarts the engine (`internal/provider/runtime.go:89`),
  and nothing repairs drift.

## Decision

1. **The binding.** `FunctionSpec.bindings.addon` lists entries of `alias` and `object`, the name of an add-on object
   of the Function's namespace: names are namespace-local, so no binding names another namespace's object.
   `Function.Validate` refuses an invalid alias or name and a repeated alias. The binding joins the dependency check
   (ADR-0308 Decision 3): while the object is missing or not Ready, readiness reports `DependencyNotReady` naming
   `addon binding "<alias>"`.
2. **The route.** The worker-node local API serves `/addon/<alias>/<path>` for any method over the sandbox's socket,
   beside `/sql/…` (ADR-0233). funcd resolves `alias` in the caller's bindings, asks Cedar (Decision 4), and sends the
   request from the host to `http://<object's status.address><path>`, which the engine's input chain admits. It
   forwards the method, the query, the body and the headers, without hop-by-hop headers and `X-Funcd-*`, and returns the
   engine's status, headers, with the same filter, and body.
3. **Limits** (new config keys, chosen numbers): `addons.callTimeout`, default 10 s, bounds a call;
   `addons.callPayloadLimit`, default 1 MiB, bounds the request's body and the answer's body.
4. **Cedar.** A capability `addon` with the action `addon::call`, the entity `AddonObject::"<ns>/<name>"`, and a
   principal attribute `addonBindings` from `spec.bindings.addon`. In the built-ins, a binding permits `addon::call`;
   operator Policies may forbid it, as ADR-0233 Decision 5 does for `sql::read`.
5. **Causes** (ADR-0330): `NotFound` for an alias the Function does not bind; `Forbidden` when Cedar denies;
   `Unavailable` when the object has no engine address or the engine refuses the connection; `DeadlineExceeded` past
   the timeout; `PayloadTooLarge` for a body over the limit. Any answer of the engine, whatever its status, is the
   engine's and passes through.
6. **The fence.** ADR-0332 Decision 3 holds but for Decision 2's route: the engine's input chain still admits only
   loopback and funcd's bridge address, and the egress gateway still refuses every engine address.
7. **The shims.** funcd-typescript gains `ctx.addon.fetch(alias, path, init?)`, which returns the Fetch API's
   `Response`; funcd-python gains `ctx.addon.fetch(alias, path, method="GET", headers=None, body=None)`, which returns
   an object with `status`, `headers` and `body`. Both call Decision 2's route over the socket, as `ctx.sql` does, and
   funcd pins their releases (ADR-0141).

## Design model

None. A call is one request through funcd with no state of its own, no lifecycle and no retry; who may reach an engine
is a static rule (Decisions 4 and 6), held by tests and a lane case.

## Behavior rows

- **calls.md**: "A Function calls an add-on object's engine" (the route, the causes, the two limits), `decided`.
- **security.md**: "Who reaches an engine" changes: funcd, and through the local API a Function that binds the object,
  `decided`.

## Temporary workarounds

None.

## Contracts

```go
// AddonBinding is an entry of FunctionBindings.Addon (ADR-0338 Decision 1).
type AddonBinding struct {
	Alias  string     `json:"alias"`
	Object ObjectName `json:"object"`
}
```

```ts
// funcd-typescript
interface AddonContext {
  fetch(alias: string, path: string, init?: RequestInit): Promise<Response>;
}
```

```yaml
functions:
  - name: rauthy-settings
    bindings:
      addon:
        - alias: idp
          object: rauthy-login
```

| Consumes | Exposes |
|---|---|
| the sandbox's socket and identity (ADR-0233) | `/addon/<alias>/<path>` |
| the object's `status.address` | the engine's answer, or a cause of Decision 5 |
| `addons.callTimeout` (10 s), `addons.callPayloadLimit` (1 MiB) | `addon::call` in Cedar's schema |

## Implementation plan

- `api/types/v1alpha1`: `AddonBinding`, `FunctionBindings.Addon`, `Validate`; regenerate the spec and kindref.
- `internal/auth/cedar`: the capability, the action, the attribute and the built-in permit.
- `pkg/funcd`: the route beside `/sql/`, its causes, the dependency-check entry, the two config keys.
- funcd-typescript and funcd-python: `ctx.addon.fetch` with its tests, each released; funcd pins both.
- Docs: the Functions guide, the add-ons pages, the behavior rows, the config reference.
- Tests: one per Scenario, named after it, in `pkg/funcd` and `internal/auth/cedar`; the shims' own tests. Real
  platform: a case of the lane `addon-rauthy` in which a Function reads rauthy's `/auth/v1/ping` through its binding
  and fails to dial the engine's address itself.
- Done: `just ci` green, the scenarios and the lane case passing, both shims released and pinned.

## Review checklist

- [ ] The route answers only for an alias of the caller's own bindings, and Cedar decides each call.
- [ ] No answer or log names the engine's address to the Function.
- [ ] The egress gateway and the input chain still refuse a direct dial.
- [ ] Each cause of Decision 5 has a test, and the limits are config keys.
- [ ] Both shims expose `ctx.addon.fetch` and are pinned at their releases.

## Consequences

- An add-on's App can apply and repair its engine's settings, with no new network path.
- funcd checks only who may reach the engine; what a caller may change stays the engine's own rule, such as rauthy's
  API key with `Secrets` update rights.
- Each call adds one local hop through funcd.
- Two shim releases come before the feature ships.

## Open questions

- Streaming through the route, and a list of the paths a binding may call: follow-up ADRs when an add-on needs them.

## References

- Feature issue [#1505](https://github.com/pyvvo/funcd/issues/1505) (FEAT-0019 F264); tracker #1506.
- rauthy's password policy route and its rights: `src/api/src/generic.rs:304` in pyvvo/funcd-rauthy.
