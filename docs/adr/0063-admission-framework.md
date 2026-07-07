# ADR-0063: Control-plane admission framework (a two-phase admission pipeline)

- **Status**: Implemented
- **Date**: 2026-06-21 (Accepted + **Implemented 2026-06-21** — review pass, see
  docs/reviews/adr-0063-implementation-claude-opus-4-8.md; post-judge: wired + scenario-tested `Request.Old`/`Identity` so
  the F33 consumer gets real values [Major]; `replaceObj` reuses its pre-update `Get` for `Old`; spy-admission
  proof that authz precedes admission; `v1` alias + trimmed one restatement [Minors/Nits]; aligned the package
  path to the blueprint's anticipated `internal/controlplane/admission/`)
- **Deciders**: green-0-rabbit
- **Tags**: admission, control-plane, validation, ports, extensibility
- **Realizes**: [FEAT-0001/F32](../feat/0001-feat-v1.1.md)
- **Refines**: [ADR-0018](0018-api-server-authn-rbac-admission.md) — which established the
  authn→authz→**admission** stages (401→403→400) with `obj.Validate()` as a *validate-only* admit step **folded
  into the handlers**, and **explicitly deferred mutating admission / defaulting** ("V1 admission is
  validate-only"). ADR-0063 extracts that admit step into a reusable **two-phase pipeline** (adding the deferred
  mutating phase) **without changing** the validate-only behavior that ships. Additive — ADR-0018 stays
  Implemented; its admission scenarios still hold.
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md) (the API routes + `Handlers` seam this
  modifies) · [ADR-0003](0003-resource-model-and-api-typing.md) (the `Object.Validate()` envelope +
  resourceGroup-required rules this orchestrates) · [ADR-0002](0002-source-code-conventions-and-patterns.md)
  (ports/drivers, `api/fault`, ctx-first, no `any`). **Enabled consumer**: the follow-up fn-to-fn RPC-links ADR
  (FEAT-0001/F33) registers the first cross-resource + Delete admissions on this framework.

## Context & Need

ADR-0018 established the control-plane write path as **authn → authorize (PDP) → admit → store**, with the
**admit** step a *validate-only* `obj.Validate()` call folded into six shared `v1.Object` helpers
([internal/controlplane/handlers.go](../../internal/controlplane/handlers.go)), and explicitly deferred
mutating admission. Today that admit step is a single inline call in `createObj`/`replaceObj`; `deleteObj` has
no admit step at all. That is enough for the envelope + cross-field rules (ADR-0003), but it is a **closed
seam**:
there is nowhere to add a check that must look at *other* resources (does a referenced target exist? does this
edge create a cycle?), nowhere to default/normalize an object before validation, and nothing on the delete
path (deletion-protection when a resource has dependents).

**Purpose**: a small, reusable **admission framework** — an ordered, two-phase pipeline (mutate-all, then
validate-all) of single-purpose `Admission`s that the control-plane write path runs on every Create / Update /
Delete. It **generalizes** today's inline `obj.Validate()` into one composable seam (the per-type `Validate()`
becomes *an* admission, not *the* admission) and opens the extension point that the fn-to-fn RPC-links ADR
needs (a cross-resource link-cycle/target-exists check, and a Delete deletion-protection check). Caller: the
control-plane `storeHandlers`. It is **not** the reconcile-time materialization gate (ADR-0020 shape) nor the
build-time contract gate (ADR-0058) — those are different lifecycle phases and stay where they are.

## Scenarios

- `scenario: admission-allows-valid-resource` — **Given** a well-formed resource, **when** it is created,
  **then** the pipeline admits it and it is persisted (the happy path through the validate admission).
- `scenario: admission-rejects-invalid-resource` — **Given** a resource that fails its `Validate()` (e.g. a
  `Service` with `type: kv` but no `kv` sub-spec), **when** it is created, **then** the write is rejected with
  `fault.Invalid` and **nothing is persisted**.
- `scenario: authz-precedes-admission` — **Given** a caller not authorized to create kind K in namespace N,
  **when** it attempts the create, **then** it is denied by the PDP (`fault.Forbidden`) and **no admission
  runs** and nothing is persisted (ordering: authz is the cheapest deny, first).
- `scenario: mutating-precedes-validating` — **Given** a mutating admission that defaults a field and a
  validating admission that requires that field, **when** an object missing the field is admitted, **then** the
  validator sees the **defaulted** object and admits it (the two-phase guarantee).
- `scenario: first-denial-short-circuits` — **Given** two validating admissions that would both deny, **when**
  an object is admitted, **then** the **first** registered admission's `fault` error is returned and the second
  never runs (deterministic order).
- `scenario: delete-runs-registered-admission` — **Given** a Delete admission registered for kind K, **when** a
  K is deleted, **then** that admission runs against the stored (Old) object and can deny the delete; **and**
  when **no** admission handles Delete for K, the delete proceeds with no extra work.
- `scenario: request-carries-old-and-identity` — **Given** a spy admission registered for Update, **when** a
  resource is updated, **then** its `Admit` receives a **non-nil `Old`** (the stored object) and the populated
  caller **`Identity`** — proving the handler wires the fields the F33 cross-resource/Delete admissions depend
  on, even though no built-in F32 admission reads them.

## Scope

**In**: the `internal/controlplane/admission` package — the `Admission` port, the two-phase `Pipeline` runner, the
`Request`/`Operation`/`Phase` types; one built-in **validate admission** that wraps `obj.Validate()`
(absorbing today's inline call); wiring the pipeline into the control-plane `createObj`/`replaceObj`/`deleteObj`
helpers; the framework runs on Create / Update / Delete.

**Out**: any *concrete* cross-resource or Delete admission (the RPC-links ADR ships the first ones); the
reconcile-time shape gate (ADR-0020) and build-time contract gate (ADR-0058) — different lifecycle phases;
config-driven admission toggling (admissions are statically wired); external policy engines (Cedar/OPA) — a
later driver behind this same port.

## Constraints & Decision drivers

- **Zero new dependencies** — stdlib + existing packages only (the pipeline is plain Go).
- **fault-native** — a denial is an `api/fault` error (→ RFC 9457 on the wire), not a bool+string.
- **Import discipline** — `internal/controlplane/admission` is a near-leaf: it may import `api/types/v1alpha1`,
  `internal/auth` (Identity), `api/fault`; it must **not** import `internal/controlplane` or `internal/store`.
- **No `any`** in the port surface; `ctx` first; deterministic, side-effect-free `Admit`.
- **Behavior parity** — the existing reject/accept behavior of `obj.Validate()` is preserved exactly; this is
  a refactor-plus-extension-point, not a policy change.

## Alternatives considered

- **Keep the inline `obj.Validate()` call (status quo).** Pro: nothing to build. Con: no place for
  cross-resource checks, defaulting, or delete-time checks — the RPC-links ADR would have to bolt its
  link-cycle check directly into `createObj`, entangling two concerns. Rejected: the seam is the point.
- **K8s-style `Response{Allowed bool, Reason string}`.** Pro: familiar (mirrors `AdmissionResponse`). Con:
  funcd is fault-native — a `fault.Invalid`/`fault.Forbidden` already carries the RFC 9457 status + message, so
  a parallel bool+reason channel **duplicates** the error path and invites drift. Rejected in favor of
  returning a `fault` error as the denial.
- **Single combined pass (each admission mutates *and* validates, in one ordered chain).** Pro: simpler runner.
  Con: a late mutation can invalidate an earlier validator's assumption, making registration order load-bearing
  and fragile. Rejected for the two-phase model (mutate-all then validate-all) — validators always see the
  final object.
- **Config-driven admission registry (toggle/order via `funcdconfig.yaml`).** Pro: operator flexibility. Con:
  a new config surface + ordering semantics to own and validate, for no current need. Rejected — admissions are
  statically wired in Go (ADR-0002 "no config for internals"); a policy-engine driver can come later behind the
  port.
- **A formal cross-resource `Reader` type baked into the framework now.** Pro: explicit seam. Con: nothing in
  this ADR uses it (YAGNI / dead code). Rejected: a cross-resource admission is just an ordinary `Admission`
  that closes over whatever read-only store dependency it needs — the framework imposes no special machinery;
  the RPC-links ADR introduces the concrete one.

## Decision

Add `internal/controlplane/admission`: an `Admission` port and a **two-phase `Pipeline`** the control-plane write path runs.

1. **Port.** An `Admission` declares its `Phase` (Mutating | Validating), which `(GVK, Operation)` pairs it
   `Handles`, and an `Admit(ctx, Request) (v1.Object, error)` that returns the (possibly-mutated) object or a
   `fault` error denying the write. Pure and deterministic — no side effects beyond reading injected deps.
2. **Two-phase pipeline.** `Pipeline.Admit` runs **all** Mutating admissions first (threading the object —
   each may return a transformed object), then **all** Validating admissions (each sees the final object,
   read-only). The first `fault` error short-circuits. Admissions whose `Handles` is false for the request are
   skipped. Registration order within a phase is the tie-break (deterministic). Admissions are **statically
   wired** at server construction.
3. **One built-in admission** ships: the **validate admission** (Validating, handles Create+Update for all
   kinds) wraps `obj.Validate()` — it *is* today's inline call, now a pipeline member.
4. **Wiring.** `storeHandlers` gains an `*admission.Pipeline`. Each write helper builds a `Request` carrying the
   route's `kind.GVK()` and the caller `Identity` (from `middleware.IdentityFrom(ctx)`, already validated by
   authz), then calls `pipeline.Admit` where `obj.Validate()` used to be:
   - `createObj` (Create): `Object` = the incoming object, `Old` = nil.
   - `replaceObj` (Update): `Object` = incoming, `Old` = the current stored object — its **existing pre-update
     `Get`** (the resourceVersion read at `handlers.go:129`) is **reordered before `Admit`** and reused, so
     `Old` is real with **no second store round-trip**.
   - `deleteObj` (Delete): runs `Admit` **only when** `pipeline.Handles(gvk, Delete)` is true, fetching the
     stored object as `Old` (a no-Delete-admission build does no extra `Get`).

   Authz still runs first, unchanged. (No built-in F32 admission reads `Old`/`Identity`, but the handler
   populates them so F33's update/Delete admissions get real values — guarded by
   `scenario: request-carries-old-and-identity`.)

## Temporary workarounds

None.

## Contracts

```go
// Package admission is the control-plane admission framework (ADR-0063): a two-phase pipeline
// (mutate-all → validate-all) of single-purpose Admissions run on every resource write, after
// authz and before the store. It is a near-leaf — it imports api/types, internal/auth, api/fault.
package admission

import (
	"context"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// Operation is the resource write being admitted.
type Operation string

const (
	Create Operation = "create"
	Update Operation = "update"
	Delete Operation = "delete"
)

// Phase orders an admission: all Mutating run (threading the object), then all Validating.
type Phase int

const (
	Mutating Phase = iota
	Validating
)

// Request is one admission review. Object is the incoming desired object (nil on Delete); Old is
// the stored object (nil on Create; set on Update/Delete). GVK is the route's kind (authoritative,
// not body-derived). Identity is the authenticated caller (authz already passed).
type Request struct {
	Operation Operation
	GVK       v1.GroupVersionKind
	Object    v1.Object
	Old       v1.Object
	Identity  auth.Identity
}

// Admission reviews a write before it is persisted. Deterministic and side-effect-free.
// A Validating admission returns req.Object on allow, or a fault error (Invalid/Forbidden/Conflict)
// on deny. A Mutating admission returns the object to carry forward, or a fault error.
type Admission interface {
	Name() string                                                  // stable, kebab-case
	Phase() Phase                                                  // Mutating | Validating
	Handles(gvk v1.GroupVersionKind, op Operation) bool      // applicability filter
	Admit(ctx context.Context, req Request) (v1.Object, error)
}

// Pipeline runs the registered admissions for a write. Construct once at server start.
type Pipeline struct {
	mutating   []Admission
	validating []Admission
}

// NewPipeline registers admissions, partitioning by Phase; order within a phase is preserved.
func NewPipeline(admissions ...Admission) *Pipeline

// Handles reports whether any registered admission applies to (gvk, op) — lets the caller skip an
// Old-object fetch on Delete when nothing admits deletes.
func (p *Pipeline) Handles(gvk v1.GroupVersionKind, op Operation) bool

// Admit runs all Mutating admissions (threading req.Object), then all Validating ones, skipping any
// whose Handles is false. The first fault error short-circuits. Returns the final (mutated) object.
func (p *Pipeline) Admit(ctx context.Context, req Request) (v1.Object, error)

// NewValidateAdmission returns the built-in Validating admission that runs obj.Validate()
// (the ADR-0003 envelope + per-type cross-field rules) for every kind on Create and Update.
func NewValidateAdmission() Admission
```

Control-plane integration (edits, not new contracts): `NewStoreHandlers(st store.Store, authz auth.Authorizer,
admit *admission.Pipeline) Handlers`; `createObj`/`replaceObj`/`deleteObj` call `h.admit.Admit(...)` in place
of (resp. in addition to) the inline `obj.Validate()`. The default pipeline is
`admission.NewPipeline(admission.NewValidateAdmission())`, built in the server constructor.

**Dependencies & I/O**

| Direction | What |
|---|---|
| Consumes | `v1.Object` (+ `Validate()`, `GroupVersionKind()`), `auth.Identity`, the control-plane write path (`createObj`/`replaceObj`/`deleteObj`) |
| Exposes | `Admission` port, `Pipeline`, `Request`/`Operation`/`Phase`, `NewValidateAdmission` |
| Config keys | none |
| Events / files | none |
| New deps | none |

## Implementation plan

**Files**
- `internal/controlplane/admission/admission.go` — `Operation`, `Phase`, `Request`, the `Admission` port.
- `internal/controlplane/admission/pipeline.go` — `Pipeline`, `NewPipeline`, `Handles`, `Admit`.
- `internal/controlplane/admission/validate.go` — the built-in validate admission (`NewValidateAdmission`).
- `internal/controlplane/handlers.go` — add the `*admission.Pipeline` field; replace the inline
  `obj.Validate()` in `createObj`/`replaceObj` with `h.admit.Admit(...)`; gate `deleteObj` on
  `h.admit.Handles(gvk, Delete)`.
- `internal/controlplane/server.go` — build the default pipeline and pass it to `NewStoreHandlers`.

**Deps**: none.

**Test plan** (one named test per scenario, all written to pass):
- `internal/controlplane/admission/pipeline_test.go` — `TestScenarioMutatingPrecedesValidating` (fake mutating + validating
  admissions), `TestScenarioFirstDenialShortCircuits` (two denying validators, assert first error + second not
  run via a spy), `TestScenarioDeleteRunsRegisteredAdmission` (a fake Delete admission runs; with none,
  `Handles(_, Delete)` is false), plus a `Handles`-filter unit test.
- `internal/controlplane/admission/validate_test.go` — `TestValidateAdmission` (valid → passes; invalid `Service`/`Function`
  → `fault.Invalid`), reusing the matrix style.
- `internal/controlplane/*_test.go` — `TestScenarioAdmissionAllowsValidResource`,
  `TestScenarioAdmissionRejectsInvalidResource`, `TestScenarioAuthzPrecedesAdmission` (register a **spy
  admission** and assert it is **not called** when authz denies — `fault.Forbidden`, store untouched), and
  `TestScenarioRequestCarriesOldAndIdentity` (a spy admission registered for Update asserts `req.Old != nil`
  and `req.Identity` is populated), against the real `storeHandlers` + a fake store/authorizer.

**Definition of done**: `go build ./...`, `go vet`, `go tool golangci-lint run`, `go test ./...`, `go mod
verify` all green; every scenario has a named passing test; behavior parity (the existing
controlplane/admission tests still pass unchanged); `internal/controlplane/admission` imports nothing from
`internal/controlplane`/`internal/store`; no `any` in the port; no identity/path leak.

## Review checklist

- [ ] `internal/controlplane/admission` exists with the `Admission` port + two-phase `Pipeline` exactly as in Contracts;
      no `any`; ctx-first; near-leaf import graph (no controlplane/store import).
- [ ] Two-phase order proven: a mutating admission's change is visible to a validating admission
      (`TestScenarioMutatingPrecedesValidating`).
- [ ] First fault error short-circuits deterministically (`TestScenarioFirstDenialShortCircuits`).
- [ ] The validate admission reproduces `obj.Validate()` behavior for every kind on Create+Update; invalid
      resources are `fault.Invalid` and not persisted.
- [ ] Authz still runs before admission, proven by a spy admission not called on deny (`TestScenarioAuthzPrecedesAdmission`).
- [ ] `Request.Old` (Update/Delete) and `Request.Identity` are populated by the handler (`TestScenarioRequestCarriesOldAndIdentity`); `replaceObj` reuses its pre-update `Get` for `Old` (no second round-trip).
- [ ] Delete runs the pipeline only when an admission handles Delete, fetching `Old`; no extra store fetch otherwise.
- [ ] Pre-existing control-plane behavior unchanged (all prior tests green); zero new deps; `just ci` green.

## Consequences

- (+) One composable admission seam replaces a closed inline call; the RPC-links ADR adds its link-cycle +
  deletion-protection checks by registering admissions, touching no handler logic.
- (+) Mutating/defaulting now has a home (none today); validators always see the final object.
- (+) Each admission is a pure, single-purpose unit — matrix-testable in isolation.
- (+) Extensible behind the port (a future Cedar/OPA policy admission) without changing the write path —
  mirrors ADR-0018's driver-behind-a-port pattern.
- (−) `NewStoreHandlers`/`NewServer` gain a parameter (a small, contained ripple) and every write now goes
  through one more indirection (negligible; the pipeline is a slice walk).
- (Risk accepted) The framework ships with only the validate admission — its extension value is realized by the
  RPC-links ADR. Acceptable: it is the strictly-smaller refactor that unblocks that ADR cleanly.

## Open questions

- Mutating-admission *conflict* policy (two mutators touching the same field) — deferred until a second mutating
  admission exists; answered by whichever ADR introduces it.
- Whether reconcile-time gates (ADR-0020 shape) should eventually share this port — out of scope here;
  revisit if a reconcile-admission need arises (its own ADR).

## References

- Kubernetes admission control (mutating → validating phases) — model inspiration.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) (the authn/authz/admission stages this refines),
  [ADR-0005](0005-api-surface-code-first-huma.md), [ADR-0003](0003-resource-model-and-api-typing.md).
