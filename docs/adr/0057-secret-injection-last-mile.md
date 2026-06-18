# ADR-0057: Secret injection last-mile — a Function declares its bound Secrets; the reconciler resolves them (PDP-authorized) into the worker's env

- **Status**: Implemented
- **Date**: 2026-06-19 (**Implemented 2026-06-19** — review pass (0 blockers, 0 majors; DoD 8/8, 5 scenarios race-clean),
  see docs/reviews/adr-0057-implementation-claude-opus-4-8.md. Completes the V1 exit criterion's "reads a secret" clause.
  **Reviewing 2026-06-19** — implemented: `FunctionSpec.Secrets []ObjectName` (+ OpenAPI regen);
  `internal/function` local `SecretResolver` seam + `Secrets`/`DeveloperFor` `Deps`; the resolve gate in `Reconcile`
  (fail-closed → `SecretResolveFailed`, no worker; pooled-function guard) threading a `secretEnv` map into a still-pure
  `workerSpec` with the `FUNCD_` prefix reserved-key guard; `pkg/funcd` wires `secrets.NewResolver`. 5 scenarios pass
  (`-race`), four sub-checks green, no new deps. **Accepted 2026-06-19** — judge: no Blockers; refine-not-supersede confirmed correct (`WorkerSpec.Env`
  is an open map, no ADR-0020/0022 decision reversed), fail-closed default-deny + no-plaintext-at-rest airtight. Folded 1
  **Major** — pin the resolve + fail-closed gate to `Reconcile` (mirroring the artifact-unresolved gate) and thread the
  resolved map into a still-**pure** `workerSpec(…, secretEnv)`, since `workerSpec` has no `ctx`/error — plus 4 Minors:
  `[]ObjectName`→`[]string` conversion at the call site, the reserved guard as the **`FUNCD_` prefix** (not 3 explicit keys),
  inter-secret key-collision is `ResolveEnv`'s existing last-wins, and framing the V1 namespace-scoped `DeveloperFor` default
  as intended-allow for own-namespace secrets — fail-closed path test-exercised until a V2 applier identity is recorded.)
- **Deciders**: green-0-rabbit
- **Tags**: secrets, reconciler, function, security, runtime
- **Realizes**: [FEAT-0000/F15](../feat/0000-feat-v1.md) — completes the **last mile** of the secrets feature (the row is
  already `implemented` for the at-rest encryption + delivery `Resolver` shipped by ADR-0022; this co-realizer wires that
  Resolver into the running worker, the part ADR-0022 explicitly deferred). Roadmap item **P-W** — the final V1 feature.
- **Refines**: [ADR-0022](0022-secrets-service.md) — **implements its deferred last mile**. ADR-0022 shipped the PDP-authorized
  `Resolver.ResolveEnv` but stated it "does not by itself complete 'reads a secret' end-to-end … the worker injects them"
  (a deferred V1 follow-up). This ADR is that injection. It does **not supersede** ADR-0022: every ADR-0022 decision stands.
  Also refines [ADR-0020](0020-function-contract-lifecycle.md) — **additively** extends the reconciler's worker-spec
  building (a new resolve-and-merge step + the `spec.secrets` field). It reverses no ADR-0020 decision, so it refines, not
  supersedes (ADR-0022's header mused a "P-M-superseding" owner — nothing is reversed, so refine is the correct relation).
- **Relates to**: [ADR-0018](0018-api-server-authn-rbac-admission.md)/[ADR-0028](0028-platform-control-plane-wiring.md) (the PDP + developer identity that
  authorize the read — **default-deny**, unchanged), [ADR-0030](0030-function-execution-runtime-shim-node.md)/[ADR-0032](0032-curated-runtime-images-container-execution.md)
  (`WorkerSpec.Env` → the sandbox; the delivery path is **unchanged** — this only fills the map), [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `Function`/`Secret` resource model the `spec.secrets` field extends), [ADR-0048](0048-dto-validation-reference.md) (the
  spec field regenerates the schema), [ADR-0034](0034-end-user-journey-acceptance-e2e.md) (the end-user journey that proves the exit
  criterion — a deployed handler **reads a secret**).

## Context & Need

The V1 exit criterion requires a deployed handler that, among other things, **reads a secret**. Everything the secret path
needs is built except the final hop: ADR-0022 shipped `internal/secrets.Resolver.ResolveEnv(ctx, id, ns, names) →
map[string]string` — it reads the named `Secret` resources (auto-decrypted by the store's at-rest AES-256-GCM encryptor),
PDP-authorizes the identity, and returns an env-var map — but **nothing calls it**. A `Function` cannot even *name* the secrets
it wants: `FunctionSpec` has no secrets field, and the reconciler's `workerSpec` builds a **fixed** `WorkerSpec.Env`
(`FUNCD_ARTIFACT`/`FUNCD_HANDLER`/`FUNCD_PORT`). The secret values never reach the worker, so a handler cannot read them.

This ADR is the last mile: let a `Function` declare its bound `Secret` names, have the reconciler resolve them through the
existing `Resolver` at materialization, and merge the result into `WorkerSpec.Env` — which the process and containerd drivers
already deliver to the sandbox. It is the 34th and final V1 item; on `Implemented` the V1 exit criterion is met.

## Scenarios

- **scenario: handler-reads-injected-secret** — Given a `Function` with `spec.secrets: [api-creds]` and a `Secret api-creds`
  (`Data: {API_KEY: "s3kr3t"}`) the developer is authorized to read, When the reconciler materializes the function, Then the
  resulting `WorkerSpec.Env` carries `API_KEY=s3kr3t` (alongside the reserved `FUNCD_*` keys) and the function reaches `Ready`.
- **scenario: unauthorized-secret-fails-materialization** — Given a `Function` whose developer is **not** PDP-authorized to read
  its declared secret, When reconciled, Then `ResolveEnv` denies, the function is **not Ready** with `Reason=SecretResolveFailed`,
  and **no worker is started** (never a worker running with the secret absent).
- **scenario: missing-secret-fails** — Given `spec.secrets: [nope]` with no `Secret nope` in the namespace, When reconciled,
  Then materialization fails clearly (not Ready, `Reason=SecretResolveFailed`) — never a silent empty env.
- **scenario: reserved-env-not-overridable** — Given a `Secret` keyed `FUNCD_PORT`, When its function is reconciled, Then the
  injected value does **not** override the reserved shim port (reserved `FUNCD_*` wins; the secret key is dropped).
- **scenario: secret-value-not-persisted** — Given a reconciled function with injected secrets, When the stored `Function` is
  read back, Then it holds only the secret **names** (`spec.secrets`) — the resolved **value** appears only in `WorkerSpec.Env`,
  never in the persisted spec or status.

## Scope

**In:** the `FunctionSpec.Secrets []ObjectName` field (+ OpenAPI regen); a `SecretResolver` seam on the function reconciler and
a developer-identity provider; the resolve-and-merge step at materialization (reserved-key precedence); the fail-closed failure
model (unauthorized/missing/not-configured → not Ready, no worker); env-var delivery (ADR-0022's V1 mechanism).

**Out:** **tmpfs** delivery (V2 — for large/file secrets; the blueprint's "env/tmpfs" → V1 ships env); live secret hot-reload /
rotation (V2); the OpenBAO + S3-envelope external drivers (V2, ADR-0022 deferred); the at-rest encryption + `Secret` CRUD
(done — ADR-0022/0003); 1→N autoscaling. One altitude: this ADR wires an existing Resolver into an existing worker-spec
builder; it decides no new secrets mechanism.

## Constraints & Decision drivers

- **Default-deny, never silent** (blueprint security model): a secret a deployer can't read, or that doesn't exist, must fail
  the function closed — never a worker started with an empty/absent value masquerading as success.
- **No new plaintext at rest**: the `Function` resource records only secret **references** (names); resolved values live only
  transiently in the in-memory `WorkerSpec.Env`. The `Secret` itself stays encrypted at rest (ADR-0022).
- **Reuse, don't rebuild**: the Resolver, the PDP, and the `WorkerSpec.Env` delivery path all exist — this ADR only connects
  them. No new dependency.
- **Import discipline** (ADR-0002): the reconciler must not import the sibling `internal/secrets` feature; it depends on a
  **local interface** (satisfied by `*secrets.Resolver`, wired in `pkg/funcd`), mirroring `Materializer`/`ArtifactResolver`.
- **Reserved keys are load-bearing**: `FUNCD_*` configure the shim (port, artifact, handler). A secret must never shadow them.

## Alternatives considered

- **Inline `secretRef` env mapping in the spec** (`env: [{name: API_KEY, secretRef: {name, key}}]`, K8s-style) — rejected for
  V1: more spec surface and a second mapping layer, when `ResolveEnv` already maps a `Secret`'s `Data` keys → env names. A
  simple name list is the smallest field that satisfies the exit criterion; per-key remapping is a clean V2 extension.
- **Resolve in `pkg/funcd` and pass a static env into the reconciler** — rejected: secrets are per-function and change as the
  function's `spec.secrets` changes; resolution must happen per-reconcile with the function in hand, not once at boot.
- **Secret wins on key collision with `FUNCD_*`** — rejected on security/correctness: a secret named `FUNCD_PORT` would hijack
  the shim's contract. Reserved keys win; the colliding secret key is dropped (and logged).
- **Soft-fail (start the worker, omit the unresolved secret)** — rejected: violates default-deny and produces a silently
  mis-configured handler. Fail the function closed instead.
- **Persist the resolved env on the Revision/Status for caching** — rejected: writes plaintext secret values at rest, defeating
  ADR-0022. Resolve fresh each materialization.

## Decision

1. **Spec field.** Add `Secrets []ObjectName` to `FunctionSpec` (`json:"secrets,omitempty"`) — the names of `Secret` resources
   **in the function's own namespace** to inject. Regenerate the OpenAPI schema (ADR-0048). Empty ⇒ no secrets (status quo).

2. **Reconciler seam.** The function reconciler gains a local `SecretResolver` interface (one method, `ResolveEnv`, satisfied by
   `*secrets.Resolver`) and a `DeveloperFor func(ns) auth.Identity` provider, both on `function.Deps`. `pkg/funcd` builds the
   concrete `secrets.Resolver` from the already-present store + authorizer and injects it.

3. **Resolve in `Reconcile` (a dedicated gate); merge in `workerSpec` (pure).** `workerSpec` is a **pure, error-less**
   function — resolution needs `ctx` and can fail, so it cannot live there. Instead, **`Reconcile` resolves once per reconcile**
   at a dedicated gate (a new step after the shape gate, mirroring the `errArtifactUnresolved` gate): if
   `len(fn.Spec.Secrets) > 0` it calls `ResolveEnv(ctx, DeveloperFor(fn.Namespace), fn.Namespace, names(fn.Spec.Secrets))`,
   fails closed on error (Decision 5), and otherwise threads the resolved `secretEnv map[string]string` down through
   `converge`/`convergeFor` into `workerSpec`. **`workerSpec` gains a `secretEnv map[string]string` parameter** and merges it
   into `WorkerSpec.Env` for every replica with **reserved `FUNCD_*` keys winning** — a resolved key matching the reserved
   guard is dropped (and logged at warn). `workerSpec` stays pure (no `ctx`, no error); all resolution + the fail-closed gate
   live in `Reconcile`. (`names(...)` converts the `[]ObjectName` field to the `[]string` the Resolver takes.)

4. **Identity for the PDP.** Resolution uses the **function's developer identity** — `DeveloperFor(ns)` returns the developer
   principal scoped to the function's namespace (ADR-0018/0028). The PDP then enforces that deployer's authorization to read
   each `Secret` (default-deny). V1 derives the identity from the namespace (the single-tenant developer model); the default
   provider (when secrets are configured but no provider is given) returns `{Role: developer, Namespaces: [ns]}`. **In V1 this
   means the PDP read is intended-allow for a function's own-namespace secrets** (the developer owns its namespace — the
   correct V1 grant), so the `unauthorized-secret-fails-materialization` path is exercised by test (a denying authorizer) and
   becomes production-reachable once the real applier identity is recorded on the `Function` (a V2 refinement — Open questions).

5. **Fail-closed failure model.** Any resolution failure — PDP deny, a missing `Secret`, or `spec.secrets` set while no
   `SecretResolver` is configured — fails materialization: the function goes **not Ready**, `Phase=Failed`,
   condition `Ready=False, Reason=SecretResolveFailed` (message carries the cause), and **no worker is created** for that
   reconcile. This mirrors ADR-0020's shape-failure → not-Ready shape and the artifact-unresolved gate.

6. **No new plaintext at rest.** Only `spec.secrets` (names) is persisted. The resolved values exist only in the in-memory
   `WorkerSpec.Env` handed to the driver; nothing writes them back to the `Function` spec/status or the Revision.

## Temporary workarounds

- **Namespace-derived developer identity** (Decision 4): V1 has one developer principal per namespace, so `DeveloperFor`
  synthesizes it rather than reading a recorded applier. **Exit:** when the `Function` records its applier identity (a V2
  authn refinement), `DeveloperFor` reads that instead — the seam already isolates the change to one provider func.
- **Env-only delivery** (no tmpfs): the blueprint allows env **or** tmpfs; V1 ships env. **Exit:** a V2 ADR adds a tmpfs
  delivery driver for large/file secrets behind the same `spec.secrets` field (or a typed delivery selector).

## Contracts

### Resource — `FunctionSpec` (additive field, ADR-0003/0048)

```go
// FunctionSpec … (existing fields unchanged)
type FunctionSpec struct {
    // … Scaling, Runtime, Handler, Artifact, Replicas, Pooling (unchanged) …

    // Secrets names the Secret resources in this function's namespace whose Data is injected
    // into the worker as env vars at materialization (ADR-0057, F15 last mile). Each named
    // Secret's Data keys become env-var names; reserved FUNCD_* keys are never overridable.
    // Empty ⇒ no secret injection. Only the NAMES are persisted; resolved values are never
    // stored on the Function (they live only in the worker's env). Resolution is PDP-authorized
    // (ADR-0022/0018); an unauthorized or missing Secret fails the function closed (not Ready).
    Secrets []ObjectName `json:"secrets,omitempty"`
}
```

### Reconciler seam — `internal/function` (local interface; satisfied by `*secrets.Resolver`)

```go
// SecretResolver resolves a function's bound Secret names → an env-var map for worker
// injection, PDP-authorized for id (ADR-0022). The reconciler depends on this local seam,
// never on the internal/secrets feature directly (ADR-0002 import discipline). Satisfied by
// *secrets.Resolver. nil on Deps ⇒ secret injection disabled (a function declaring
// spec.secrets then fails closed — SecretResolveFailed).
type SecretResolver interface {
    ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// Deps additions (function.Deps):
//   Secrets      SecretResolver                         // nil ⇒ injection disabled (fail-closed on declared secrets)
//   DeveloperFor func(ns v1.NamespaceName) auth.Identity // identity for the PDP read; defaulted when Secrets != nil
```

`NewReconciler` validation: if `Secrets != nil` and `DeveloperFor == nil`, default `DeveloperFor` to the namespace-scoped
developer identity (Decision 4). `Secrets == nil` is valid (injection off).

### Behavior — resolve (in `Reconcile`) then merge (in `workerSpec`)

```
// In Reconcile, a dedicated gate AFTER the shape gate (mirrors the errArtifactUnresolved gate):
given fn with spec.secrets = names (len > 0):
  if r.secrets == nil:                      → fail closed: SecretResolveFailed ("secret injection not configured")
  secretEnv, err = r.secrets.ResolveEnv(ctx, r.developerFor(fn.Namespace), fn.Namespace, asStrings(names))
  if err != nil:                            → fail closed: SecretResolveFailed (err cause); no worker
  // else thread secretEnv down: convergeFor → converge → workerSpec(fn, replica, artifactPath, secretEnv)

// In workerSpec (pure — receives the already-resolved map):
  for k, v in secretEnv:
     if isReservedFuncdKey(k): log warn, skip   // strings.HasPrefix(k, "FUNCD_") wins (reserved-env-not-overridable)
     else: spec.Env[k] = v
```

`asStrings` converts the `[]ObjectName` field to the `[]string` `ResolveEnv` takes. `isReservedFuncdKey(k)` is
`strings.HasPrefix(k, "FUNCD_")` — a **prefix** guard, so every current and future reserved key (`FUNCD_ARTIFACT`/`HANDLER`/
`PORT`/`POOL_MANIFEST`/`PORTFILE`, …) stays protected without editing the guard. Inter-secret key collisions follow
`ResolveEnv`'s existing last-in-`names`-wins merge (unchanged here). The failure path sets `Ready=False,
Reason=SecretResolveFailed`, `Phase=Failed`, persists status, programs routes without the function (no worker) — identical
control flow to the existing artifact-unresolved / shape-invalid gates in `Reconcile`.

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| `SecretResolver.ResolveEnv` | `internal/secrets.Resolver` (via local seam) | reads + decrypts Secrets, PDP-authorizes — unchanged |
| `auth.Identity` | `internal/auth` | the developer principal for the PDP read |
| `fn.Spec.Secrets` | the `Function` resource | the secret names to inject |
| Exposes: merged `WorkerSpec.Env` | → process/containerd drivers (ADR-0030/0032) | delivery path unchanged |

## Implementation plan

- **`api/types/v1alpha1/function.go`** — add `Secrets []ObjectName` to `FunctionSpec`; keep the spec roundtrip test green.
- **OpenAPI** — regenerate the schema (ADR-0048 generator); commit the regenerated artifact (staleness check stays green).
- **`internal/function`** — add the `SecretResolver` interface + `Secrets`/`DeveloperFor` to `Deps` (mirror `materializer.go`'s
  seam style); `NewReconciler` stores them and defaults `DeveloperFor`; add `isReservedFuncdKey` (the `FUNCD_` prefix guard) +
  `asStrings([]ObjectName)`; add the **resolve gate to `Reconcile`** (after the shape gate, mirroring `errArtifactUnresolved`):
  resolve once per reconcile, fail closed on error, else thread `secretEnv` through `convergeFor`/`converge` into `workerSpec`;
  give `workerSpec` a `secretEnv map[string]string` parameter and do the reserved-precedence merge there (keeping it pure).
- **`pkg/funcd/funcd.go`** — build `secrets.NewResolver(secrets.Deps{Store: c.store, Authorizer: c.authorizer, Logger})` and
  inject it (+ the default `DeveloperFor`) into `function.Deps`.
- **Tests (non-gated, `just ci`)** — one per scenario, using a fake/real `SecretResolver` over an in-memory secrets store:
  `handler-reads-injected-secret` (secret env reaches `WorkerSpec.Env`, function Ready), `unauthorized-secret-fails-materialization`
  (deny → `SecretResolveFailed`, no worker), `missing-secret-fails` (missing → `SecretResolveFailed`),
  `reserved-env-not-overridable` (a `FUNCD_PORT` secret does not override the reserved value), `secret-value-not-persisted`
  (resolved value in env, absent from the stored `Function`). The full container e2e (a real handler reading the env over the
  journey) is the **deferred** exit-criterion proof — the end-user-journey lane (ADR-0034) / the Linux integration lane.
- **Verify green** via the four sub-checks (`go build` · `go tool golangci-lint run` · `go test` · `go mod verify`) + OpenAPI
  staleness.

**Definition of done:** the five scenario tests pass un-skipped; a function with `spec.secrets` and an authorized developer
gets the secret in `WorkerSpec.Env` and reaches Ready; unauthorized/missing/not-configured fail closed with
`SecretResolveFailed` and no worker; reserved `FUNCD_*` keys are never overridden; no resolved value is persisted on the
`Function`; OpenAPI regenerated; no new dependency; `just ci` green after commit.

## Review checklist

- [ ] `FunctionSpec.Secrets []ObjectName` added (`json:"secrets,omitempty"`); spec roundtrip test green; OpenAPI regenerated (not stale).
- [ ] The reconciler depends on a **local** `SecretResolver` interface, **not** an import of `internal/secrets`.
- [ ] Resolution + the fail-closed gate live in `Reconcile`; `workerSpec` stays pure, taking a `secretEnv` parameter.
- [ ] Secret env merges into `WorkerSpec.Env`; the reserved guard is the **`FUNCD_` prefix** (a colliding secret key dropped, logged).
- [ ] Resolution uses the function's namespace-scoped developer identity via `DeveloperFor`; PDP enforces default-deny.
- [ ] Unauthorized / missing / not-configured → `Ready=False, Reason=SecretResolveFailed`, `Phase=Failed`, **no worker created**.
- [ ] The resolved value appears only in `WorkerSpec.Env`, never in the persisted `Function` spec/status.
- [ ] All five scenario tests pass un-skipped; no new dependency; no `any` in the new surface; no identity/path leak.

## Consequences

- The V1 exit criterion's "reads a secret" clause is satisfiable: a deployed handler can read a bound secret from its env.
  On `Implemented`, V1 is feature-complete (34/34 roadmap items).
- The secret-injection failure mode is **fail-closed**, consistent with every other materialization gate — a mis-authorized or
  missing secret never yields a silently-degraded worker.
- `WorkerSpec.Env` now carries developer-supplied keys; the reserved-key guard makes the shim contract robust to collisions.
- Resolution runs once per reconcile (not cached), so a rotated `Secret` is picked up on the next reconcile — at the cost of a
  decrypt per reconcile (negligible at V1 scale; caching would mean plaintext at rest, rejected).

## Open questions

- **Applier identity on the Function** — V1 synthesizes the developer identity from the namespace; recording the real applier
  (so the PDP authorizes the *actual* deployer, not a namespace-default developer) is a V2 authn refinement, isolated to
  `DeveloperFor`. Answered by a future authn ADR.
- **Per-key env remapping** (`secretRef` style) and **tmpfs delivery** for large/file secrets — both V2, behind the same field
  or a typed delivery selector. Answered by a V2 secrets-delivery ADR.

## References

- [ADR-0022](0022-secrets-service.md) — the secrets service + the deferred-last-mile note this ADR discharges.
- [ADR-0020](0020-function-contract-lifecycle.md) — the reconciler + worker-spec building this ADR extends.
- [ADR-0018](0018-api-server-authn-rbac-admission.md) / [ADR-0028](0028-platform-control-plane-wiring.md) — the PDP + developer identity.
- [ADR-0030](0030-function-execution-runtime-shim-node.md) / [ADR-0032](0032-curated-runtime-images-container-execution.md) — the `WorkerSpec.Env` → sandbox delivery path (unchanged).
- [ADR-0034](0034-end-user-journey-acceptance-e2e.md) — the end-user journey that proves the exit criterion.
- [blueprint.md](../../blueprint.md) §secrets — "delivered to workers via env/tmpfs" (the model this conforms to).
