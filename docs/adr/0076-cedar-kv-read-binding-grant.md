# ADR-0076: Cedar KV read authorization — `spec.kv` binding-as-read-grant (a built-in permit)

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23 — sound read-side mirror of ADR-0075's link-as-grant, no Blockers;
  folded 1 Major (unify the Cedar expression to `principal.kvBindings.contains(resource)` everywhere — the
  `resource in …` phrasings were wrong: `kvBindings` is a `Set`, not an entity-hierarchy parent) + minors
  (state per-**table** scoping not per-store; motivate the `principal has kvBindings` guard; tighten the
  unbound-read scenario; note the examples lose the rg-scoped-`Policy` demo, still unit-covered). Verified
  against the driver: bound-only permit preserves ADR-0074 default-deny for unbound tables — NOT a return to
  default-allow; write/schema/port/facade all unchanged.)
- **Deciders**: green-0-rabbit
- **Tags**: auth, authz, cedar, kv, pdp, binding
- **Realizes**: [FEAT-0001/F45](../feat/0001-feat-v1.1.md) (Cedar KV read binding-grant)
- **Relates to**: [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar framework + KV as first
  consumer — this **refines its KV *read* posture**: a declared `spec.kv` binding now grants `kv::read` via a
  built-in permit, exactly as ADR-0075 added link-as-grant for invoke), [ADR-0073](0073-kv-bindings-and-subdomains.md)
  (`spec.kv` — the binding that becomes the read grant), [ADR-0075](0075-cedar-invoke-authorization.md) (the
  link-as-grant precedent this mirrors for reads)

## Context & Need

ADR-0074 made KV `kv::read` **default-deny, explicit-`Policy`-required**: the `spec.kv` binding is naming, and
a `Policy` must permit each read. In practice that means **every** function reading its **own** bound table
needs a hand-written read `Policy` (the kv-counter examples each ship one) — pure friction, because the KV
facade is alias-driven: its `Resolver` maps the caller's alias → `(store,table)`, and an alias *is* a
declared `spec.kv` binding, so the facade only ever asks `kv::read` on a table the caller already **bound**.

That is the same shape ADR-0075 faced for invoke and resolved with **link-as-grant**: a declared `spec.links`
target grants invoke via a built-in `permit`, governance layered by `Policy` `forbid`s. This ADR makes the
read side symmetric: a declared `spec.kv` binding grants `kv::read` on that table via a **built-in `permit`**,
so reading your own bound table needs **no `Policy`**. This is **not** a return to default-allow (the ADR-0074
judge's concern): a binding is a *declared, reviewable, scoped* capability — exactly like a link — so reads on
**un**bound tables stay default-deny. `Policy`s move from *required-to-permit* to *optional* — `forbid` to
revoke a binding's read, or `permit` a **cross-binding** read (a function reading a table it has **no** binding
to, e.g. an analytics function). Writes are unchanged: a binding never grants write; the owner-write built-in
(`forbid(kv::write) unless principal == resource.owner`) stays the single-writer rule.

## Scenarios

- **scenario: binding-grants-read** — Given function `counter` declares `spec.kv: [{alias: c, store:
  counters-kv, table: table-counters}]` and **no** read `Policy`, When it calls `context.kv.get("c", …)`, Then
  the PDP **allows** `kv::read` (the built-in `permit(kv::read) when principal.kvBindings.contains(resource)`).
- **scenario: unbound-read-denied** — Given function `reporting` whose `kvBindings` does **not** contain
  `orders/customers` (it binds no such table) and no `Policy` permits it, When a `kv::read` on `orders/customers`
  is evaluated, Then it is **Forbidden** (default-deny preserved for unbound tables — the binding is the capability).
- **scenario: cross-binding-read-via-policy** — Given `reporting` has no binding to `orders` but a `Policy`
  permits `reporting` `kv::read` on `KVStore::"default/orders"`, When the read is evaluated, Then it is
  **allowed** (a `Policy` still grants reads beyond one's own bindings).
- **scenario: policy-revokes-read** — Given `counter` has the binding **and** a `Policy`
  `forbid(principal == Function::"default/counter", action == Action::"kv::read", resource)`, When it reads,
  Then the PDP **denies** it (operator revoke, without editing `counter.spec.kv`; forbid wins).
- **scenario: write-unaffected** — Given a function holds a `spec.kv` binding to a table it does **not** own,
  When it writes, Then it is **Forbidden** (a binding grants read only; owner-write built-in unchanged).

## Scope

**In**: materialize each principal `Function` entity's **`kvBindings`** attribute (a `Set` of the `KVTable`
entity-refs from its `spec.kv`, same-namespace); a **built-in** `permit(kv::read) when
principal.kvBindings.contains(resource)` (binding-as-read-grant, default-on, beside the owner-write `forbid`);
examples drop their
now-redundant own-table read `Policy`s.

**Out**: changing `spec.kv` (still the alias→store+table naming of ADR-0073) or the facade PEP (it already
calls `Authorize(kv::read)`; the built-in now permits it — **no PEP change**); **write** authorization
(owner-write built-in unchanged — a binding never grants write); the curated schema (`kv::read` is already a
curated action — **no schema change**); **cross-namespace** reads (a binding is same-namespace, ADR-0073);
the other Cedar consumers (egress/secrets — later ADRs behind the same pattern).

## Constraints & Decision drivers

- **Symmetry with invoke** — a `spec.kv` binding is a declared capability exactly like a `spec.links` target;
  read authorization should mirror ADR-0075's link-as-grant (built-in permit + `Policy` governance), not a
  bespoke rule.
- **Still default-deny for the unbound** — binding-as-read-grant permits **only** bound tables; an unbound
  `kv::read` stays Forbidden. This honors ADR-0074's judge-reinforced default-deny (no blanket seed permit).
- **One PDP, one port, zero new deps** — reuses the ADR-0074 cedar driver, `EntityProvider`, and built-in
  PolicySet; no `auth.Authorizer` port change, no facade change.
- **`Policy`s stay load-bearing** — for `forbid` (revoke) and **cross-binding** `permit` (read a table you did
  not bind); only the *own-binding read* `Policy` becomes redundant.

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| Own-table read | **Built-in `permit(kv::read) when principal.kvBindings.contains(resource)`** (binding grants read) | **Keep ADR-0074 default-deny** (every read needs a `Policy`) — pure friction; the facade only ever reads bound tables, so the `Policy` restates the binding; asymmetric with invoke's link-as-grant |
| Where the built-in reads "bound" | **The principal `Function` entity's `kvBindings` Set** (the `spec.kv` tables as `KVTable` refs) | A built-in re-reading `spec.kv` outside Cedar — splits the rule across Go + Cedar; the entity attribute keeps it one Cedar expression (as `links` for invoke) |
| Default-allow seed | **Rejected — bound-only** | A blanket `permit(kv::read)` — the exact default-allow the ADR-0074 judge removed; a binding-scoped permit is a *declared* capability, not a blanket one |
| Binding grants write too | **No — read only** | Writes are single-writer by ownership (`tables[].owner`); a consumer binding must not grant write or the owner-write invariant breaks |

## Decision

A declared `spec.kv` binding grants `kv::read` on that table, mirroring ADR-0075's link-as-grant.

1. **Entity** — the cedar `EntityProvider` (ADR-0074/0075) materializes the principal `Function` entity's
   **`kvBindings`** attribute: a `types.Set` of the **exact** `KVTable` entity-refs `kvTableUID(ns, b.Store,
   b.Table)` for each `b` in the caller's `spec.kv` (same-namespace) — per **table**, not per store, so a
   binding grants read on **that one table only** (tighter than a `resource in KVStore::"…"` Policy).
   Request-relevant (built from the principal already read for `links`); no full-store rebuild.
2. **Built-in policy** (ships in the driver, beside the KV owner-write rule and the link::invoke permit):
   `permit(principal, action == Action::"kv::read", resource) when { principal has kvBindings &&
   principal.kvBindings.contains(resource) };` — a declared binding grants read by default. The
   `principal has kvBindings` guard makes a function with **no** `spec.kv` inert here (the attribute is absent,
   so default-deny holds), exactly as `principal has links` does for invoke.
3. **No PEP / schema / port change** — the facade already calls `Authorize(kv::read, resource=resolved table)`
   on an alias the caller bound, so the built-in permits it; `kv::read` is already a curated action; the
   `auth.Authorizer` surface is untouched.
4. **Governance** — `forbid` `Policy`s **revoke** a binding's read (or condition it); `permit` `Policy`s grant
   **cross-binding** reads (a table the principal did not bind — e.g. analytics). Own-binding reads need no
   `Policy`; the examples drop theirs.
5. **Default-deny preserved** — a `kv::read` on a table **not** in `principal.kvBindings` has **no** permitting
   built-in, so it is Forbidden unless a `Policy` permits it (the cross-binding case). Defense-in-depth: the
   PDP self-enforces "bound" without trusting the facade to have resolved only a bound alias.

## Temporary workarounds

None.

## Contracts

```cedar
// built-in (driver-shipped), added beside the ADR-0074 owner-write + ADR-0075 link::invoke rules:
permit(principal, action == Action::"kv::read", resource)
  when { principal has kvBindings && principal.kvBindings.contains(resource) };
```

```go
// internal/auth/cedar/entities.go — EntitiesFor (ADR-0074/0075) gains, on the PRINCIPAL Function entity, a
// "kvBindings" attribute alongside "links": a types.Set of KVTable EntityUIDs from the caller's spec.kv —
//   kvBindings = { kvTableUID(principal.Namespace, b.Store, b.Table) | b ∈ fn.Spec.KV }
// Request-relevant (the principal Function is already fetched for links); no new MetaReader call, no port change.
// internal/auth/cedar/policies.go — embed builtin_kv_read.cedar and concatenate it into the built-in PolicySet
//   (alongside builtin_kv.cedar + builtin_invoke.cedar).
// schema.go, the facade (internal/services/kv), and the auth.Authorizer port are UNCHANGED.
```

| consumes | exposes |
|---|---|
| the ADR-0074 cedar driver (`auth.Authorizer`) + `EntityProvider` + built-in PolicySet | binding-implied `kv::read`; operator-governable KV reads |
| `Function.spec.kv` (ADR-0073) — naming + the caller entity's `kvBindings` Set | the built-in permit's "bound" set |

## Implementation plan

**Files**: `internal/auth/cedar/builtin_kv_read.cedar` (new — the binding-as-read-grant permit);
`internal/auth/cedar/policies.go` (embed + concat it into the built-in set); `internal/auth/cedar/entities.go`
(the principal Function's `kvBindings` Set from `spec.kv`); their tests. `examples/{js,python}/kv-counter/`:
delete `policy.yaml` (own-table reads are now implicit) and drop the `funcdcli apply -f …/policy.yaml` lines
from `scripts/lima-kv.yaml` (the lane now proves reads work with **no** read `Policy`). Note: this removes the
examples' only end-to-end demo of a `resourceGroup`-scoped `Policy` (the Python `policy.yaml`); that path stays
covered by the unit test `TestScenarioScopedPolicies` (and the cross-binding `Policy` path by
`TestScenarioCedarPermitsRead`), so no coverage is lost — only example boilerplate.

**go.mod / deps**: none (reuses ADR-0074's cedar-go).

**Test plan** — one named test per Scenario, cedar package: `binding-grants-read` (a function with a `spec.kv`
binding reads its table with no `Policy` → allowed); `unbound-read-denied` (a principal with no binding to the
table, no `Policy` → Forbidden); `cross-binding-read-via-policy` (the existing reporting-reads-orders permit
stays — reporting has no binding); `policy-revokes-read` (a `forbid` overrides the binding grant);
`write-unaffected` (a bound non-owner write is still Forbidden by the owner-write built-in). The facade KV
tests get a bound-read-with-no-Policy case. The Lima KV lane (`just lima-example-kv`) is the e2e: counter +
pycounter read 1→2 with **no** read `Policy`.

**Definition of done**: `just ci` green; every Scenario a passing named test; a bound read is allowed with no
`Policy`; an unbound read is Forbidden (default-deny preserved); a `forbid` `Policy` revokes a bound read; a
`permit` `Policy` still grants a cross-binding read; writes unaffected; examples carry no read `Policy`; no new
dep; no identity/path leak.

## Review checklist

- [ ] `EntitiesFor` sets the principal Function's `kvBindings` Set (the `spec.kv` tables as `KVTable` refs);
      no new `MetaReader` call, no port/schema change.
- [ ] Built-in `permit(kv::read) when … principal.kvBindings.contains(resource)` ships in the driver; a bound
      read is allowed with **no** `Policy`.
- [ ] An **un**bound read is Forbidden (default-deny preserved); a `forbid` `Policy` revokes a bound read; a
      `permit` `Policy` grants a cross-binding read (reporting reads orders).
- [ ] Owner-write (`kv::write`) unchanged — a binding grants **read only**; `link::invoke` + rbac unchanged.
- [ ] Example `policy.yaml`s removed; `scripts/lima-kv.yaml` no longer applies a read `Policy`; the lane is green.
- [ ] No new dep; no identity/path leak.

## Consequences

**Positive**: reading your own bound KV table needs **no `Policy`** — the binding is the capability, symmetric
with invoke (`spec.links`→`link::invoke`) and ownership (`tables[].owner`→`kv::write`). `Policy`s become
*optional governance* (revoke via `forbid`; grant cross-binding reads via `permit`), not a per-read tax. The
kv-counter examples lose their boilerplate read `Policy`.
**Refines** ADR-0074: KV `kv::read` moves from "default-deny, `Policy` required" to "a declared binding grants
read **unless** a `Policy` forbids" — an added permit path scoped to declared bindings, not a return to
default-allow (unbound reads stay Forbidden), so this **relates to** (does not supersede) ADR-0074, exactly as
ADR-0075 relates to ADR-0064.
**Negative (accepted)**: two layers express "bound" — the facade `Resolver`'s alias→table mapping **and** the
built-in's `kvBindings` guard — intentional defense-in-depth (the PDP self-enforces "bound" without trusting
the facade), and they cannot diverge (both read the live `spec.kv`).
**Neutral**: writes, the schema, the port, and cross-namespace reads are unchanged; egress/secrets consumers
still deferred.

## Open questions

- **Raw (store,table) reads** — if a future API reads KV without an alias (no binding), the built-in denies it
  and a `Policy` must permit it (the cross-binding path) — the intended behavior; revisit if such an API lands.
- **Per-read vs per-connection authz** — unchanged from ADR-0074 (cached PolicySet + request-relevant
  entities); a per-(principal,table) decision cache is a possible implementation-PR optimization.

## References

- [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar framework + KV first consumer, read
  posture refined here) · [ADR-0075](0075-cedar-invoke-authorization.md) (link-as-grant — the precedent
  mirrored) · [ADR-0073](0073-kv-bindings-and-subdomains.md) (`spec.kv` bindings).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F45. Backlog IAM track (`PVTI_lAHOBMTWh84BbERrzgwhbqc`).
