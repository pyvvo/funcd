# ADR-0075: fn→fn invoke authorization via Cedar — `link::invoke`, link-as-grant as a built-in

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23 — sound second Cedar consumer, no Blockers; folded 1 Major (the
  `EntityProvider` must build a **Function resource** entity + the principal's **`links`** attribute — today
  `EntitiesFor` errors on a non-KVTable resource) + minors (V1.1 governance is forbid-centric; the
  `caller.links` guard is intentional **defense-in-depth**, not redundancy; `NewHandler` gains the Authorizer;
  the ADR-0064 "→ unless forbidden" refinement). cedar-go `Set`/`.contains()` + `Function`-as-resource verified
  against the implemented driver. Correctly **relates-to**, not supersedes, ADR-0064.)
- **Deciders**: green-0-rabbit
- **Tags**: auth, authz, cedar, invoke, links, pdp
- **Realizes**: [FEAT-0001/F44](../feat/0001-feat-v1.1.md) (Cedar invoke authorization)
- **Relates to**: [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar framework this **extends**
  with a second consumer + a new action), [ADR-0064](0064-fn-to-fn-rpc-links.md) (`spec.links` + the
  link-as-grant rule — **preserved as a built-in**, now Cedar-governable), [ADR-0069](0069-kv-data-plane.md)
  (the local-API invoke handler — the PEP)

## Context & Need

ADR-0074 made Cedar the PDP for **KV** resource access (the `spec.kv` binding became naming; a `Policy`
decides authorization), but left **fn→fn invoke** on ADR-0064's **link-as-grant**: the `Resolver`
([internal/workernode/local/resolver.go](../../internal/workernode/local/resolver.go)) fuses *naming*
(alias→target) and *authorization* (`fault.Forbidden` when the caller declares no such link) — exactly the
pattern Cedar just split for KV. This ADR makes invoke the **second Cedar consumer**: `spec.links` stays the
alias→target **naming**, and a `link::invoke` PDP decision authorizes — but, unlike KV's coarse read (a
temporary workaround), ADR-0064's link-as-grant is a *deliberate, reviewable, no-SSRF* capability model worth
**keeping**. So it is preserved as a **built-in `permit`** (a declared link still grants invoke by default),
with operator `Policy`s **layering governance** (revoke a link without editing the caller's spec; permit
conditional invoke) — mirroring KV's built-in owner-write `forbid`.

## Scenarios

- **scenario: declared-link-invokes** — Given function `A` declares `spec.links: [{alias: pricing, target:
  pricing}]` and no `Policy` forbids it, When `A` calls `context.invoke("pricing", …)`, Then the PDP **allows**
  it (the built-in `permit(link::invoke) when resource in caller.links`).
- **scenario: undeclared-alias-denied** — Given `A` declares no link for alias `b`, When `A` calls
  `context.invoke("b", …)`, Then it is **Forbidden** (the binding is the capability — default-deny on naming,
  the `Resolver`'s existing rule, unchanged).
- **scenario: policy-revokes-invoke** — Given `A` declares the `pricing` link **and** a `Policy`
  `forbid(principal == Function::"default/A", action == Action::"link::invoke", resource ==
  Function::"default/pricing")`, When `A` invokes `pricing`, Then the PDP **denies** it (operator revoke,
  without touching `A.spec.links`).
- **scenario: rbac-and-kv-unaffected** — Given control-plane CRUD and KV calls, When authorized, Then they
  route exactly as before (rbac for CRUD; `kv::read`/`kv::write` for KV) — adding `link::invoke` extends the
  schema only.

## Scope

**In**: extend the curated Cedar schema (ADR-0074) with the **`link::invoke`** action + `Function` as a
**resource** entity type; materialize each caller `Function` entity's **`links`** attribute (a set of target
`Function` entity-refs from `spec.links`); a **built-in** `permit(link::invoke) when resource in caller.links`
(the link-as-grant rule, default-on); the **invoke PEP** — the local-API invoke handler calls the PDP
(`Authorize(principal=caller Function, action=link::invoke, resource=target Function)`) after the `Resolver`
maps the alias to the target, before forwarding; operator `Policy`s govern (forbid/conditional).

**Out**: changing `spec.links` (still the naming + the same-namespace, acyclic, latest-Ready model of
ADR-0064); **cross-namespace invoke** (needs cross-ns naming + a Cedar permit — deferred with KV's cross-ns
sharing); the V2 multi-node NATS-lattice transport (FEAT-0002); the other Cedar consumers — **egress**
(`EgressPolicy`→Cedar), **secrets** — each a later ADR behind this same pattern; migrating the `Resolver`'s
"unknown alias ⇒ Forbidden" off the binding (it stays — naming default-deny, as KV's "no `spec.kv` entry").

## Constraints & Decision drivers

- **Keep ADR-0064's link-as-grant** — it is a sound, reviewable, no-SSRF model (the declaration *is* the
  default capability), not a temporary workaround; Cedar governs *on top*, it does not discard it.
- **One PDP, one port** — invoke is a PEP calling the same `auth.Authorizer` (the ADR-0074 cedar driver); no
  new engine, no new dep.
- **Default-deny on naming preserved** — an undeclared alias is `Forbidden` at the `Resolver` (the binding is
  the capability), as KV's missing `spec.kv`.
- **Connection-scoped principal** — the caller `Function` principal is the provisioned sandbox `Ref` (ADR-0064/
  0069/0074), never client-asserted.

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| link-as-grant | **Keep as a built-in `permit`; `Policy`s layer governance** | **Full default-deny** (a `Policy` must permit every invoke) — retires ADR-0064's *deliberate, sound* link-as-grant + breaks every existing fn→fn; KV's coarse read was a workaround, the declared link is not |
| Where the built-in reads "declared" | **The caller `Function` entity's `links` attribute** (set of target refs from `spec.links`) | A built-in that re-reads `spec.links` outside Cedar — splits the rule across Go + Cedar; the entity attribute keeps it one Cedar expression |
| Resource | **The target `Function`** (`Function::"<ns>/<target>"`) | A synthetic "link" entity — `spec.links` is naming, not a resource; the callee Function *is* the resource |

## Decision

Invoke becomes the second Cedar consumer; `spec.links` stays naming; link-as-grant survives as a built-in.

1. **Schema** (extends ADR-0074's curated schema): add action **`link::invoke`**; `Function` is now both a
   principal and a **resource** type. Future actions (`egress::send`,`secret::read`) extend it likewise.
2. **Entity** — the cedar `EntityProvider` (ADR-0074) is extended on **both** sides: the **principal**
   `Function` entity gains a **`links`** attribute (a `Set` of target `Function` entity-refs from the caller's
   `spec.links`, same-namespace), and the **resource** path builds a **`Function` resource entity** when the
   request's resource is a Function (today it only builds a `KVTable`). Both stay request-relevant.
3. **Built-in policy** (ships in the cedar driver, alongside the KV owner-write rule):
   `permit(principal, action == Action::"link::invoke", resource) when { principal has links &&
   principal.links.contains(resource) };` — a declared link grants invoke by default (ADR-0064 preserved).
4. **PEP** — the local-API invoke handler ([local.go](../../internal/workernode/local/local.go)) keeps
   `Resolver.Resolve(caller, alias) → target` (naming; **unknown alias ⇒ `Forbidden`**, unchanged), then calls
   `Authorize(principal=Function::"<ns>/<caller>", action=link::invoke, resource=Function::"<ns>/<target>")`;
   a deny ⇒ `fault.Forbidden`, else forward to the `Invoker`. So: no link ⇒ Forbidden (naming); declared link
   ⇒ allowed by the built-in **unless** a `Policy` forbids it.
5. **Governance** — in V1.1 it is **forbid-centric**: operators write `Policy` resources (ADR-0074) to
   **revoke** a declared invoke (`forbid`) or **conditionally deny** it (`forbid … unless { context… }`),
   without editing the caller's `spec.links`. (User *permits* are largely **inert** in V1.1 — the built-in
   already permits declared links and the `Resolver` gates undeclared aliases, so a permit cannot grant an
   un-addressable target; permits become load-bearing only when **cross-namespace** naming lands.)

## Temporary workarounds

None. (Cross-namespace invoke is a deliberate scope line — `spec.links` is same-namespace per ADR-0064 — not
a workaround.)

## Contracts

```cedar
// built-in (driver-shipped), added beside the ADR-0074 KV rules:
permit(principal, action == Action::"link::invoke", resource)
  when { principal has links && principal.links.contains(resource) };
```

```go
// internal/auth/cedar — schema + entity additions (no new types on the auth.Authorizer port).
// schema.go: register Action "link::invoke"; Function as a resource entity type (it is already the
//   principal type, entityTypeFunction).
// entities.go: EntitiesFor (ADR-0074) is EXTENDED on BOTH sides for invoke —
//   (a) the principal Function entity gains a "links" attribute = a types.Set of Function EntityUIDs
//       resolved from the caller Function's spec.links targets (same namespace); and
//   (b) when resource.Type == KindFunction (not only KVStore-with-Path/KVTable), it builds the TARGET
//       Function resource entity (today resourceTableUID errors on a non-KVTable resource — that branch
//       must accept a Function resource). Both stay request-relevant (no full-store rebuild).
// policies.go: append the link::invoke built-in to the built-in PolicySet (beside the KV owner-write rule).
```

```go
// internal/workernode/local — NewHandler gains the auth.Authorizer + the caller auth.Identity (the
// per-function principal). The invoke handler, AFTER Resolve(caller, alias)→target (naming; unknown
// alias ⇒ Forbidden, unchanged), calls:
//   dec, _ := authorizer.Authorize(ctx, auth.Request{
//       Identity: callerIdentity, Action: "link::invoke",
//       Resource: &auth.EntityRef{Type: v1.KindFunction, Namespace: target.Namespace, Name: target.Function},
//   })
//   if !dec.Allowed { → fault.Forbidden }  // a forbid Policy revokes a declared invoke
// else forward to the Invoker. The principal entity's links attr is the built-in's defense-in-depth.
```

| consumes | exposes |
|---|---|
| the ADR-0074 cedar driver (`auth.Authorizer`) + `EntityProvider` + built-in PolicySet | `link::invoke` authorization; operator-governable fn→fn invoke |
| `Function.spec.links` (ADR-0064) — naming + the caller entity's `links` attribute | the built-in permit's "declared" set |
| the ADR-0069 local-API caller `Ref` | the connection-scoped caller principal |

## Implementation plan

**Files**: `internal/auth/cedar/{schema.go (+ link::invoke action, Function as a resource type), entities.go
(+ the principal Function `links` Set attribute from spec.links **and** a Function-resource branch in
EntitiesFor — today it errors on a non-KVTable resource), policies.go (+ the link::invoke built-in permit)}`
+ their tests; `internal/workernode/local/local.go` — **NewHandler gains an auth.Authorizer + the caller
auth.Identity**; the invoke handler calls Authorize after Resolve; + the `pkg/funcd` wiring that passes the
PDP + builds the caller principal Identity. Tests + the fn-to-fn e2e (a declared link invokes; a `forbid`
`Policy` revokes it).

**go.mod / deps**: none (reuses ADR-0074's cedar-go).

**Test plan** — one named test per Scenario: cedar (`declared-link-invokes` via the built-in;
`policy-revokes-invoke` via a forbid; the caller entity carries `links`); the invoke PEP
(`undeclared-alias-denied` stays the Resolver's Forbidden; declared+permitted forwards; declared+forbidden ⇒
Forbidden); `rbac-and-kv-unaffected` (routing unchanged); e2e on `examples/js/fn-to-fn` (greeter link invokes;
a forbid Policy denies).

**Definition of done**: `just ci` green; every Scenario a passing named test; a declared link invokes via the
built-in (ADR-0064 preserved); an undeclared alias is Forbidden (naming default-deny, unchanged); a `Policy`
`forbid` revokes a declared invoke; KV + rbac unaffected; no new dep; no identity/path leak.

## Review checklist

- [ ] Schema gains `link::invoke` + `Function` as a resource type; `EntitiesFor` sets the principal Function's
      `links` Set attribute **and** builds a Function resource entity (no longer errors on a non-KVTable resource).
- [ ] Built-in `permit(link::invoke) when … caller.links.contains(resource)` ships in the driver (ADR-0064
      link-as-grant preserved, defense-in-depth); a declared link invokes with no `Policy`.
- [ ] `NewHandler` gains the `auth.Authorizer` + caller `Identity`; the invoke handler calls the PDP after
      `Resolver.Resolve` (unknown alias ⇒ Forbidden unchanged); a deny ⇒ `fault.Forbidden`; a `forbid` `Policy`
      revokes a declared invoke.
- [ ] KV (`kv::read`/`kv::write`) + rbac (CP CRUD) decisions unchanged; no new dep; no identity/path leak.

## Consequences

**Positive**: fn→fn invoke joins KV under one PDP — operator-governable (revoke/condition a link without
editing the caller), while ADR-0064's reviewable link-as-grant is preserved as the default (no churn for
existing functions). **Refines** ADR-0064: "declared link → invoke" becomes "→ invoke **unless** a `Policy`
forbids" — an added deny path, not a contradiction, so this **relates to** (does not supersede) ADR-0064. The
pattern now repeats for egress/secrets behind the same schema-extension seam.
**Negative (accepted)**: a per-invoke PDP call (cached PolicySet + request-relevant entities, as ADR-0074).
Two layers express "declared" — the `Resolver`'s alias→target gate **and** the built-in's `caller.links`
guard — but this is **intentional defense-in-depth**: the PDP self-enforces "declared" *without trusting* the
`Resolver` to have run, and they cannot diverge (both read the live `spec.links`).
**Neutral**: cross-namespace invoke + egress/secrets consumers deferred; `spec.links` semantics unchanged.

## Open questions

- **Cross-namespace invoke** — needs cross-ns naming + a Cedar permit; deferred with KV's cross-ns sharing.
- **Per-invoke vs per-connection authz** — the PDP is called per invoke; caching a decision per (caller,
  target) for the sandbox's life is a possible optimization (implementation-PR detail).

## References

- [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar framework) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md) (`spec.links` + link-as-grant, preserved) ·
  [ADR-0069](0069-kv-data-plane.md) (the local-API invoke handler).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F44. Backlog IAM track (`PVTI_lAHOBMTWh84BbERrzgwhbqc`).
