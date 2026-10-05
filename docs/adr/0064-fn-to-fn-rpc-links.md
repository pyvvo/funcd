# ADR-0064: Declarative synchronous function-to-function RPC (links) over a worker-node local API

- **Status**: Implemented
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — Decision 3 and checklist: on a pool socket the caller is the member named in X-Funcd-Member, checked.
- **Date**: 2026-06-21 (Accepted + **Implemented 2026-06-21** — review pass, see docs/reviews/adr-0064-implementation-claude-opus-4-8.md; post-judge: moved contract validation to the **target's shim**
  (the daemon-side `Invoke` forwards via the in-process `Handler.ServeHTTP` and **propagates** the shim's
  422/500 rather than re-validating — the daemon holds no per-function validator) [Major]; named the acyclic
  admission's DFS algorithm; noted the `internal/workernode/` nesting [Minors])
- **Deciders**: green-0-rabbit
- **Tags**: rpc, links, invoke, worker-node, ipc, uds, admission, data-plane, contract, security
- **Realizes**: [FEAT-0001/F33](../feat/0001-feat-v1.1.md)
- **Relates to**: [ADR-0063](0063-admission-framework.md) (the admission pipeline this **consumes** — it
  registers the first cross-resource + Delete admissions) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md)
  (the data-plane invoke path reused to reach the target) · [ADR-0016](0016-activator-scale-to-zero.md) (cold
  target wake) · [ADR-0020](0020-function-contract-lifecycle.md) (`FunctionSpec` + the reconciler that mounts the
  local API) · [ADR-0058](0058-contract-codegen-from-code-types.md) (the eval-free input/output contract
  validator reused on each call) · [ADR-0018](0018-api-server-authn-rbac-admission.md) (the PDP — the link is
  the grant) · [ADR-0011](0011-runtime-sandbox-port.md) (per-sandbox netns — the UDS is bind-mounted across it) ·
  [ADR-0046](0046-pooling-placement-policy.md) (the single-namespace pool — the pooled-caller identity rule).

## Context & Need

A function calling another function today goes over the data-plane HTTP invoker (ADR-0033) with **nothing
declared**: no topology, no admission check that the target exists, no contract check that the call is
type-compatible, no authorization on the edge, and no first-class identity for "who is calling". That is
implicit, ungoverned, and unsafe to expose to handler authors.

**Purpose**: a **declarative, synchronous** fn-to-fn RPC — modelled on wasmCloud links. A function declares
`spec.links` (a local **alias** → a target `Function`); the handler calls `context.invoke(alias, input)`; the
platform brokers the call over a **per-sandbox worker-node local API** (HTTP-over-UDS) to the target via the
existing data-plane/activator path, validating the payload against the target's contract. The **declared link
is the capability grant** (default-deny — no link, no call); the call is attributed to the **caller sandbox's
identity from the connection**, never a client-asserted one. Caller: a handler author (`context.invoke`) and
the platform operator (`spec.links`). This completes the V1.1 "inter-function calls" track (F33); the **V2
multi-node** case — a target on a *different* worker node/network — is a **NATS-lattice transport behind the
same local-API contract**, deferred to FEAT-0002.

## Scenarios

- `scenario: handler-invokes-linked-function` — **Given** Function A with `spec.links: [{alias: payments,
  target: B}]` and a Ready B, **when** A's handler calls `context.invoke("payments", input)`, **then** it
  receives B's output (the brokered round-trip).
- `scenario: unlinked-alias-denied` — **Given** A with no link named `ghost`, **when** its handler calls
  `context.invoke("ghost", …)`, **then** the call fails **closed** with `fault.Forbidden` (default-deny: no
  link is no grant) and no target is reached.
- `scenario: link-target-must-exist` — **Given** A declaring a link whose `target` names no Function in A's
  namespace, **when** A is applied, **then** admission rejects it with `fault.Invalid` (the link-validity
  admission, cross-resource).
- `scenario: link-cycle-forbidden` — **Given** existing links B→A, **when** A is applied with a link A→B (or a
  self-link A→A), **then** admission rejects it with `fault.Invalid` (a sync cycle would deadlock).
- `scenario: linked-input-contract-validated` — **Given** B declares a `FuncInput` contract, **when** A invokes
  B with a violating payload, **then** **B's shim** returns 422 and B's handler never runs (ADR-0058), and the
  `Invoke` **propagates** that 422 to A — the daemon does not re-validate.
- `scenario: cold-target-woken` — **Given** B scaled-to-zero, **when** A invokes B, **then** the activator
  wakes B and the call returns within the link timeout — or, if B never becomes Ready, returns 503 at the
  deadline (no indefinite block).
- `scenario: delete-protected-while-linked` — **Given** A links to B, **when** B is deleted, **then** admission
  rejects the delete with `fault.Conflict` (deletion-protection; the Delete admission reads `Old`).
- `scenario: caller-identity-from-connection` — **Given** the per-sandbox local API, **when** a request asks to
  invoke an alias, **then** the caller is the sandbox's own Function (fixed at provisioning) — a request body
  cannot name a *different* caller (no identity spoofing).

## Scope

**In**: the `FunctionSpec.Links` field (+ `FunctionLink` type, OpenAPI regen); the two admissions registered on
ADR-0063's pipeline — **link-validity** (Validating; Create/Update; target-exists + alias-unique + acyclic,
cross-resource) and **link-deletion-protection** (Validating; Delete; reject deleting a linked-to Function); the
**worker-node local API** (HTTP-over-UDS, one listener per sandbox, connection-scoped caller identity) exposing
`POST /invoke/{alias}`; alias→target resolution against the caller's links, **PDP-gated** (link = grant); the
synchronous call to the target via the **reused** data-plane/activator path (warm proxy / cold wake bounded by
the link timeout) with **ADR-0058 contract validation** of input (422) and output (500); the shim
`context.invoke(alias, input)` method (Node + Python); **same-namespace** only; **latest-Ready** revision.

**Out**: **V2 multi-node — the NATS-lattice transport** for a target on another worker node/network (FEAT-0002;
see Open questions for the exit criterion); **cross-namespace** links (need a workload-identity `Grant`, V2);
**typed codegen clients** (generate the caller's `invoke<I,O>` types from the target's contract — a premium
follow-up; this ADR ships the generic, runtime-validated `invoke`); **revision pinning** (latest-Ready only);
async/event delivery (the EventBus backlog item); the **vsock** transport (the V3 microVM lane — same local-API
contract).

## Constraints & Decision drivers

- **Security model is the spine**: identity is **connection-scoped** (the per-sandbox socket's caller is fixed
  at provisioning), never client-asserted; `invoke` takes an **alias only** (no URL/address → no SSRF); the
  link **is** the grant (default-deny). For a pooled caller, the listener is per-pool (one namespace, ADR-0046)
  and the trusted pool host attests the calling handler — bounded by the single-namespace pool invariant.
- **Reuse, don't rebuild**: the target is reached through the existing data-plane/activator path (ADR-0033/0016)
  and validated by the existing eval-free contract validator (ADR-0058). No new invoke transport on the
  same-node path; no new validator.
- **Sync semantics**: a call blocks the caller until the target returns or the link timeout fires; cycles
  deadlock, so the link graph must be **acyclic** (enforced at admission). Cold-start is an accepted cost — an
  operator avoids it with `minReplicas≥1` on the target; it is **not** the platform's problem to hide.
- **Transport**: HTTP-over-UDS via stdlib `net`/`net/http` (funcd already dials containerd over a UDS) — **zero
  new dependency** on the same-node path. The local API has **no published SDK** (shim-only).
- Inherited: `api/fault`→RFC 9457; ctx-first; no `any` in port surfaces; Apache-2.0/MIT-only; single binary.

## Alternatives considered

- **Caller names the target Function directly (no alias).** Pro: one fewer indirection. Con: couples the
  handler's *code* to a concrete callee — swapping the target means editing code, not the manifest; and there is
  no stable local name to grant against. Rejected: the **alias/link** (wasmCloud model) gives late binding +
  is the natural grant unit.
- **Direct sandbox-to-sandbox HTTP (no broker).** Pro: no local API to build. Con: no policy, no tracing,
  spoofable identity, and `invoke(url)` is an SSRF primitive. Rejected — the brokered local API is the security
  boundary (and the only way to attribute identity from the connection).
- **Give functions the data-plane address + a generic HTTP client.** Pro: trivial. Con: same as above plus the
  function could call *any* function (no default-deny). Rejected.
- **Async/event delivery instead of sync RPC.** Pro: decoupled, ret/DLQ. Con: it is a *different* semantic — the
  caller here needs the callee's **return value now**. Rejected for this ADR; the async path is the separate
  EventBus item.
- **NATS request/reply for the same-node call too (uniform transport).** Pro: one transport for V1.1 + V2. Con:
  pulls the bus onto the hot path for a purely local call, adds latency + a dependency where a UDS suffices, and
  coludes the local-API contract with the lattice. Rejected for same-node; **NATS is the V2 cross-node driver
  behind the same local-API contract** (deferred, FEAT-0002).
- **Revision pinning on the link.** Pro: stability. Con: complexity + stale-target footguns for a first cut.
  Rejected — **latest-Ready** only; pinning is a later option.

## Decision

1. **`spec.links`.** Add `Links []FunctionLink` to `FunctionSpec`; `FunctionLink{Alias, Target, Timeout}`. The
   alias is the local name the handler passes to `invoke`; the target is a `Function` name in the same
   namespace; the timeout bounds the sync wait (default applied when zero). Intra-object structural rules (alias
   format + uniqueness, target format) live in `Function.Validate()`; **cross-resource** rules are admissions.
2. **Two admissions on ADR-0063's pipeline** (both Validating; Function kind):
   - **link-validity** (Create/Update) — every link `target` resolves to an existing Function in the namespace,
     and the namespace link graph stays **acyclic** with this object applied (build the namespace link digraph,
     DFS from the applied object, reject on a back-edge; a self-link is a degenerate cycle). Cross-resource:
     constructed with a read-only store reader.
   - **link-deletion-protection** (Delete) — reject deleting a Function that is another Function's link `target`
     (reads `Request.Old` + lists dependents). Returns `fault.Conflict`.
3. **Worker-node local API.** The reconciler provisions, per sandbox, a **UDS** bind-mounted into the sandbox
   (per-sandbox netns, ADR-0011) whose **caller `FunctionRef` is fixed at provisioning** — connection-scoped
   identity. It serves `POST /invoke/{alias}` (HTTP-over-UDS): resolve the caller's link for `alias`
   (default-deny if none), **PDP-check** the caller→target edge (ADR-0018), then `Invoke` the target.
4. **The call.** `Invoke` **forwards** through the **in-process** data-plane/activator `Handler.ServeHTTP`
   (ADR-0033/0016) with a synthesized `POST /function/<target>` request + a capturing `ResponseWriter` (no
   network hop): warm → proxy; cold → wake, bounded by the link timeout (→ 503 on deadline). The **target's
   shim** validates input (422) and output (500) against its baked ADR-0058 contract — the daemon-side `Invoke`
   does **not** re-validate (it holds no per-function validator) and **propagates** the target's status.
   Resolution is **latest-Ready** revision.
5. **SDK.** `FunctionContext` gains `invoke` — Node `invoke<I, O>(alias: string, input: I): Promise<O>`, Python
   `invoke(alias, input)`. It dials the per-sandbox UDS and POSTs to `/invoke/{alias}`. The generics are
   compile-time ergonomics; **runtime contract validation is the enforcement** (typed codegen clients are a
   later premium).

The link is the capability: with no matching link, `invoke` fails closed; the caller can reach **only** its
declared targets, in its own namespace, identified by the connection.

## Temporary workarounds

- **Same-node only (no multi-node transport).** V1.1 brokers `invoke` over a same-node UDS; a target on another
  worker node is unreachable. **Exit criterion**: FEAT-0002 adds the NATS-lattice transport behind the same
  `Invoker` contract (the local API and `context.invoke` are unchanged; only the same-node-vs-remote routing
  behind `Invoke` gains a driver).
- **Generic (untyped) `invoke` + runtime validation.** Types are author-declared/erased; safety is the runtime
  contract check. **Exit criterion**: a later ADR generates typed `invoke<I,O>` clients from the target's
  published contract (ADR-0058/0059 metadata), filling the generics from the real schema.

## Contracts

```go
// api/types/v1alpha1/function.go — additive.

// FunctionLink declares a synchronous RPC dependency: a local alias bound to a target Function in
// the same namespace, callable from the handler as context.invoke(alias, input) (ADR-0064).
type FunctionLink struct {
	// Alias is the local name the handler passes to invoke; unique within a function's links.
	Alias string `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Target is the name of a Function in this function's namespace (latest-Ready revision is called).
	Target ObjectName `json:"target"`
	// Timeout bounds the synchronous wait (incl. a cold-start wake); zero ⇒ the platform default.
	Timeout time.Duration `json:"timeout,omitempty" minimum:"0" maximum:"300000000000"` // ≤5m
}

// FunctionSpec gains:
//   Links []FunctionLink `json:"links,omitempty"`
```

```go
// internal/workernode/local — the per-sandbox worker-node local API (ADR-0064), HTTP-over-UDS.
// One listener per sandbox; the caller is fixed at provisioning (connection-scoped identity). No
// published SDK — reached only through the built-in runtime shim.
package local

import (
	"context"
	"net/http"
	"time"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Ref identifies a function replica's caller identity (namespace + function); the local API is
// constructed with the sandbox's Ref, so the caller is the connection, never the request body.
type Ref struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
}

// Resolver maps (caller, alias) → the link target, applying the PDP (the link is the grant).
// fault.Forbidden when the caller declares no such link (default-deny); fault.NotFound for an
// unknown target. Constructed with a read-only store view + the auth.Authorizer.
type Resolver interface {
	Resolve(ctx context.Context, caller Ref, alias string) (target Ref, timeout time.Duration, err error)
}

// Invoker performs the synchronous call by FORWARDING through the target's normal invoke path — the
// in-process data-plane/activator Handler.ServeHTTP (ADR-0033/0016) with a synthesized
// POST /function/<target> request + a capturing ResponseWriter (no network hop): warm → proxy; cold →
// wake, bounded by timeout (never-Ready → fault.Unavailable → 503). The TARGET's shim validates input
// (422) and output (500) against its baked ADR-0058 contract; the Invoker does NOT re-validate (the
// daemon holds no per-function validator) — it PROPAGATES the target's status. Returns the output bytes.
type Invoker interface {
	Invoke(ctx context.Context, target Ref, input []byte, timeout time.Duration) ([]byte, error)
}

// NewHandler builds the per-sandbox local API handler: POST /invoke/{alias} → Resolve(caller, alias)
// → Invoke(target). caller is the fixed sandbox identity (connection-scoped).
func NewHandler(caller Ref, res Resolver, inv Invoker) http.Handler

// Serve runs the handler on a Unix domain socket at path (bind-mounted into the sandbox), until ctx
// is done. One call per sandbox provisioning.
func Serve(ctx context.Context, path string, h http.Handler) error
```

```go
// internal/controlplane/admission — the two admissions this ADR registers on ADR-0063's pipeline.
// Both are Validating, Function-kind, constructed with a read-only store reader (cross-resource).
func NewLinkValidityAdmission(r StoreReader) admission.Admission        // Create/Update: target-exists + acyclic
func NewLinkDeletionProtectionAdmission(r StoreReader) admission.Admission // Delete: reject if linked-to
// StoreReader is the read-only store subset a cross-resource admission needs (Get + List).
type StoreReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
	List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error)
}
```

SDK (TypeScript `FunctionContext`, Python parallel):
```ts
export interface FunctionContext {
  log(...args: unknown[]): void;
  /** Synchronously invoke a linked function by its spec.links alias; validated against its contract. */
  invoke<I = unknown, O = unknown>(alias: string, input: I): Promise<O>;
}
```

**Dependencies & I/O**

| Direction | What |
|---|---|
| Consumes | ADR-0063 `admission.Pipeline` (registers 2 admissions) · the data-plane/activator invoke path (ADR-0033/0016) · the ADR-0058 contract validator · the store (read, for resolution + admissions) · `auth.Authorizer` (PDP) · a per-sandbox UDS path (from the reconciler/runtime drivers, ADR-0011/0030/0032) |
| Exposes | `FunctionSpec.Links` (wire) · `POST /invoke/{alias}` on the per-sandbox local API (shim-only) · `context.invoke` (shim SDK) · the 2 admissions |
| Config keys | none new (the UDS path is derived from the sandbox root) |
| New deps | none (stdlib `net`/`net/http` for the UDS) |

## Implementation plan

**Files**
- `api/types/v1alpha1/function.go` — add `Links`/`FunctionLink`; intra-object link rules in `Function.Validate()`; OpenAPI regen (ADR-0048).
- `internal/controlplane/admission/links.go` — `NewLinkValidityAdmission`, `NewLinkDeletionProtectionAdmission`, `StoreReader`; register both in the server's pipeline construction.
- `internal/workernode/local/local.go` — `Ref`, `Resolver`, `Invoker` ports, `NewHandler`, `Serve` (HTTP-over-UDS); nests under the blueprint's `internal/workernode/` node-agent component (blueprint.md §repository shape).
- `internal/workernode/local/resolver.go` — store+PDP-backed `Resolver` (one file).
- `internal/workernode/local/invoker.go` — data-plane/activator+contract-backed `Invoker` (one file).
- reconciler wiring (ADR-0020 owner) — provision the per-sandbox UDS + `Serve` the local API with the sandbox's `Ref`; bind-mount the socket (process + containerd drivers).
- `shim/nodejs/src/*` + `shim/python/src/*` — add `context.invoke` dialing the UDS.

**Deps**: none.

**Test plan** (named test per scenario; cross-process e2e deferred to the Linux integration lane per the
roadmap's test-sequencing, recorded):
- Non-gated (unit/contract): `TestScenarioUnlinkedAliasDenied`, `TestScenarioLinkTargetMustExist`,
  `TestScenarioLinkCycleForbidden`, `TestScenarioDeleteProtectedWhileLinked` (admissions over a fake store);
  `TestScenarioCallerIdentityFromConnection` (the handler attributes to the fixed `Ref`, ignoring body-claimed
  caller); `TestScenarioLinkedInputContractValidated` (Invoker over a fake target asserts 422); resolver
  default-deny + PDP unit tests; `Function.Validate()` link-structural matrix (alias format/uniqueness).
- Deferred to the integration lane (real shim + sandbox + UDS): `handler-invokes-linked-function`,
  `cold-target-woken` — the full brokered round-trip + activator wake.

**Definition of done**: `go build`/`vet`/`golangci-lint`/`go test`/`go mod verify` green; every non-gated
scenario a named passing test; OpenAPI regenerated (no staleness); admissions registered on the ADR-0063
pipeline; no `any` in the new ports; no identity/path leak; `just ci` green.

## Review checklist

- [ ] `FunctionSpec.Links`/`FunctionLink` added with the documented tags; OpenAPI regenerated; `Function.Validate()` enforces alias format + uniqueness.
- [ ] The link-validity admission rejects a missing target and any cycle (incl. self-link); the deletion-protection admission rejects deleting a linked-to Function (`fault.Conflict`, reads `Old`). Both registered on the ADR-0063 pipeline.
- [ ] The local API attributes the caller from the fixed sandbox `Ref` — a request cannot name a different caller (`TestScenarioCallerIdentityFromConnection`).
- [ ] `invoke` is default-deny: no matching link ⇒ `fault.Forbidden`, no target reached.
- [ ] The **target's shim** validates input (422) / output (500) per ADR-0058; the daemon-side `Invoke` **propagates** those statuses (no re-validation) and forwards via the **in-process** `Handler.ServeHTTP`; cold target woken via the activator, bounded by the link timeout (503 on deadline).
- [ ] `context.invoke` exists in both shims, dials the UDS, returns the target output. Same-namespace only.
- [ ] Zero new deps; UDS via stdlib; cross-process e2e scenarios deferred + recorded; `just ci` green.

## Consequences

- (+) fn-to-fn calls become declared, validated, authorized, and identity-attributed — the call graph is
  explicit (admission-checked) instead of implicit.
- (+) The link doubles as the authorization grant (default-deny) and the contract anchor — one declaration.
- (+) First real consumer of ADR-0063 (two cross-resource + a Delete admission) and of the worker-node local
  API seam the blueprint anticipated.
- (+) Reuses the data-plane/activator/contract machinery — small new surface (the local API + 2 admissions).
- (−) A sync call holds the caller while the target runs/cold-starts — a pooled caller blocking on `invoke` is
  a back-pressure risk; the link timeout bounds it, but operators must size pools/min-replicas accordingly.
- (−) Same-node only until FEAT-0002 — a target on another node is unreachable (documented workaround).
- (Risk accepted) Connection-scoped identity for a **pooled** caller delegates handler attribution to the
  trusted pool host, bounded by the single-namespace pool invariant (ADR-0046) — acceptable; a lying pool host
  can only forge identities within its own namespace.

## Open questions

- **Multi-node transport (NATS lattice).** When the target is on another worker node/network, `Invoke` routes
  over NATS request/reply instead of the local data-plane. **Answered in**: FEAT-0002 (a transport driver behind
  the `Invoker` contract; the local API + `context.invoke` are unchanged).
- **Typed codegen clients.** Filling `invoke<I,O>` generics from the target's published contract. **Answered
  in**: a later v1.x/V2 ADR (builds on ADR-0058/0059 contract metadata).
- **Link-timeout defaulting as a mutating admission.** The zero→default could move from the `Invoker` into a
  Mutating admission (the first use of ADR-0063's mutating phase). **Answered in**: the implementation PR or a
  follow-up — out of this ADR's critical path.

## References

- wasmCloud links (component-to-component, link-as-capability) — model inspiration.
- [ADR-0063](0063-admission-framework.md) (admission pipeline consumed), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md), [ADR-0016](0016-activator-scale-to-zero.md), [ADR-0058](0058-contract-codegen-from-code-types.md), [ADR-0018](0018-api-server-authn-rbac-admission.md), [ADR-0011](0011-runtime-sandbox-port.md), [ADR-0046](0046-pooling-placement-policy.md).
- Dapr service-invocation building block (brokered local API over UDS) — prior art.
