# ADR-0177: A Policy governs only its own namespace

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (judged twice by three lenses; held from publication)
- **Deciders**: green-0-rabbit
- **Tags**: auth, authz, cedar, policy, namespace, admission
- **Realizes**: [FEAT-0001/F43](../feat/0001-feat-v1.1.md) (Cedar fine-grained resource authorization)
- **Supersedes in part**: [ADR-0074](0074-cedar-authorization-resource-access.md) Decision 4, the clause "the cedar
  driver loads + compiles all `Policy` resources (cached, recompiled on change)" (lines 115–116), and Decision 3's
  single cached compiled PolicySet (lines 107–108): one cache and one revision remain, now holding one set per
  namespace plus a built-ins-only set. The rest of
  Decision 4 (namespaced CRD, `spec.cedar`, policy-validity admission, metastore persistence, cache on change) stands.
  It also settles ADR-0074's open question "Policy scoping … namespaced for V1" (lines 256–257) for namespaced
  Policies; cluster Policies stay open.
- **Relates to**: [ADR-0116](0116-capability-authorization-framework.md) (entity types and built-ins) ·
  [ADR-0117](0117-egress-policy-enforcement.md) and [ADR-0136](0136-roles-and-role-assignments.md) (synthetic
  Policies) · [ADR-0158](0158-pool-member-identity.md) (Proposed; reads Policies for its AccessHash) ·
  [ADR-0075](0075-cedar-invoke-authorization.md): its cross-namespace invoke "needs cross-ns naming + a Cedar
  permit" (lines 57, 103, 197), and ADR-0074's Out list expects cross-namespace KV sharing through a Cedar policy;
  a namespaced Policy can no longer grant either, so the later sharing ADR replaces that path

## Context & Need

A `Policy` is a namespaced object (ADR-0074 Scope), so its author expects it to govern that namespace. The driver
does not hold it there:

- `pkg/funcd/funcd.go:1499-1510`: `policySource.Policies` lists `KindPolicy` with `store.ListOptions{}`, so it
  returns the Policies of every namespace.
- `internal/auth/cedar/policies.go:77-95`: `compile` adds every statement of every Policy to one `PolicySet` with no
  namespace guard; the namespace appears only in the `PolicyID` string (`<ns>/<name>#<j>`, line 90).
- `internal/auth/cedar/schema.go:81-119`: `ValidateCedar` checks parse, actions and entity types, not the namespace
  of a named principal or resource entity. The admission (`internal/controlplane/admission/policy.go:35`) calls only it.

Observable effect: a Policy created in namespace `team-a` can permit or forbid requests between principals and
resources of `team-b`, and an unscoped statement (`permit(principal, action == …, resource);`) applies to every
namespace. A deny also reads "no permitting policy" when a forbid decided it (`internal/auth/cedar/cedar.go:109-114`).

The purpose: a namespaced Policy decides only requests inside its namespace, admission says so to the author at
apply time, and the deny reason tells a forbid from a missing permit.

## Scenarios

- `scenario: policy-foreign-principal-refused` — Given namespace `team-a`, When a Policy in `team-a` is applied with
  `permit(principal == Function::"team-b/g", action == Action::"kv::read", resource in KVStore::"team-a/orders");`,
  Then the apply fails with 400 naming `Function::"team-b/g"`.
- `scenario: policy-foreign-resource-refused` — Given the same, When the statement's resource is
  `resource in KVStore::"team-b/orders"`, Then the apply fails with 400 naming `KVStore::"team-b/orders"`.
- `scenario: unscoped-policy-stays-in-namespace` — Given a Policy in `team-a` with
  `permit(principal, action == Action::"kv::read", resource);` and no binding, When `team-a/f` reads a table of
  `team-a`, Then it is allowed; When `team-b/g` reads a table of `team-b`, or `team-a/f` reads a table of `team-b`,
  Then it is denied with a reason starting `cedar default-deny: no permitting policy` (the cross-namespace case
  carries the Decision 4 suffix).
- `scenario: stored-foreign-policy-inert` — Given a Policy in `team-a` written to the metastore before this ADR that
  permits `Function::"team-b/g"` `kv::read` on `KVStore::"team-b/orders"`, When `team-b/g` reads `team-b/orders`
  without a binding, Then it is denied.
- `scenario: cross-namespace-built-ins-only` — Given a Policy in `team-a` permitting every `kv::read`, When a
  principal of `team-a` reads a table of `team-b`, Then only the built-in policies decide, and the reason says the
  request crossed namespaces; a cross-namespace `kv::write` or `s3::write` is denied.
- `scenario: synthetic-stays-in-namespace` — Given a RolesAssignment in `team-a` granting `team-a/f` a role, When
  `team-b/g` makes the request that role allows on a `team-b` resource, Then it is denied; the same holds for an
  EgressPolicy synthetic.
- `scenario: deny-reason-names-forbid` — Given `team-a/f` binds `team-a/orders` and a Policy `revoke` in `team-a`
  forbids that read, When `f` reads it, Then it is denied with `cedar: forbidden by policy team-a/revoke#0`.

## Scope

In: which Policies evaluate a request (the cedar driver's compiled sets), the namespace check of the policy-validity
admission, and the deny reason.

Out:
- cross-namespace sharing (a grant from one namespace to another): impossible through a namespaced Policy; a later
  ADR (a board card for the decider);
- cluster-scoped Policies and their precedence (ADR-0074 open question, still open);
- the built-in policies, entity materialization, `auth.Request`, the PEPs, rbac (all unchanged);
- the content of EgressPolicy and RolesAssignment synthetics (already namespace-local, see Decision 2).

## Constraints & Decision drivers

- Default-deny and forbid-wins (ADR-0074) hold in every set.
- One PDP behind `auth.Authorizer` (ADR-0018); no new PEP input: the namespaces come from the `auth.EntityRef`
  already on the request (`internal/auth/authorizer.go:50-53`); an egress resource carries its caller's namespace
  (`internal/auth/netdest.go:51-57`).
- The decision cache stays lock-free on the hot path (ADR-0117 §4a, `policies.go:48-72`).
- Every namespaced entity UID is `<ns>/…` (`internal/auth/cedar/capabilities.go:36-73`); `NetDestination` is not
  namespaced (`cedar.go`, `netDestUID`).

## Alternatives considered

| Option | Outcome |
|---|---|
| **Both: admission refuses a statement naming an entity outside the Policy's namespace, and evaluation compiles one PolicySet per namespace** — containment does not depend on parsing every statement shape, and the author learns at apply time | **chosen** |
| Admission only — unscoped heads, `when` conditions and Policies already stored keep reaching every namespace; closing them means refusing the unscoped shapes authors use | rejected |
| Evaluation only — contained, but a statement naming another namespace is accepted and silently never matches | rejected |
| Inject `when { principal.namespace == "<ns>" && resource.namespace == "<ns>" }` into every statement at compile time — needs a `namespace` attribute on every entity type (`NetDestination` lacks one); a missing attribute errors and cedar-go skips the statement without a signal; every namespace's statements still evaluate on every request | rejected |

## Decision

1. **Scope rule.** A namespaced Policy applies only to a request whose principal and resource are both in the
   Policy's namespace. Request namespace = `req.Identity.Principal.Namespace` when it equals
   `req.Resource.Namespace` and is non-empty; otherwise the request is cross-namespace.
2. **Evaluation: one compiled set per namespace.** On a revision change, `compile` groups `PolicySource` output by
   `Policy.Namespace` and builds, per namespace, the built-ins plus that namespace's user Policies plus its
   EgressPolicy and RolesAssignment synthetics (each carries its source's namespace: `egress_compile.go:114`,
   `roles_compile.go:168`), and one built-ins-only set. A same-namespace request evaluates against its namespace's set
   (the built-ins-only set when the namespace has no Policy); a cross-namespace request evaluates against the
   built-ins-only set. `PolicySource` and `policySource` are unchanged (they still list all namespaces once per
   revision); one revision rebuilds every set. The built-ins are parsed once and their parsed `*cedar.Policy` values
   are added to each set, since any store write (a Function write included, `pkg/funcd/funcd.go:1495-1496, 1548-1550`) changes the revision.
   Sending a cross-namespace request to the built-ins only also drops the resource namespace's forbids; that is safe
   because no built-in permits a cross-namespace request: links, kvBindings, blobBindings and the writers set are
   built in the principal's or the RolesAssignment's own namespace (`capabilities.go:196-197`,
   `roles_compile.go:119`), and the kv/s3 write permit is cut off by the writers forbid.
3. **Admission.** Policy-validity refuses (`fault.Invalid`, HTTP 400 like every policy-validity rejection today,
   `api/fault/problem.go:25`) a statement whose principal or resource scope (`==`, `in`, `is … in`) names an entity of
   a namespaced type (`Function`, `KVStore`, `KVTable`, `Bucket`, `BlobPrefix`, `S3Identity`, `Identity`,
   `CatalogService`) whose id does not start with `<policy namespace>/`. Unconstrained scopes (`principal`,
   `resource`, `is T`) and entity literals inside `when`/`unless` are **not** refused: Decision 2 alone contains them,
   since no other namespace's request reaches this set. `NetDestination` and `Action` are not namespaced and pass.
   The settled refusal status "422" is met by the platform's existing invalid-request status, 400: no `fault` Kind
   maps to 422, and adding one is out of scope; the decider confirms this deviation at acceptance.
4. **Deny reason.** On deny, `Decision.Reason` is, in order: `cedar: forbidden by policy <PolicyID>` when cedar-go's
   `Diagnostic.Reasons` lists a matched forbid (on Deny it holds the forbids; with several, the lowest `PolicyID`
   in byte order, since cedar-go collects them in set-iteration order); the existing `cedar: denied (<error>)`
   when evaluation errors; the existing `cedar default-deny: no permitting policy`, with
   ` (cross-namespace request: built-in policies only)` appended for a cross-namespace request. The forbid and
   cross-namespace strings are new (grep: no match in the code or the ADRs).
5. **Stored Policies.** No migration. A stored Policy naming another namespace stays stored and compiles into its own
   namespace's set, where the foreign entity never matches; re-applying it is refused by Decision 3.
6. **Impact on ADR-0158 (Proposed; its owner applies it).** Decision 5 "Reads" lists "the user Policies the PDP
   reads (all, as `policySource`'s first List)": it becomes one List of the Function's namespace
   (`store.ListOptions{Namespace: ns}`), and only statements of that namespace count toward `AccessHash`; a Policy of
   another namespace grants nothing and must not split a pool. Its re-keying "a Policy change queues every pooled
   Function" narrows to the pooled Functions of the Policy's namespace.

## Temporary workarounds

None.

## Contracts

New (grep: none of these names exists in `internal/auth`):

```go
// internal/auth/cedar/policies.go
type compiledPolicies struct {
	revision string
	builtin  *cedar.PolicySet                      // built-ins only
	byNS     map[v1.NamespaceName]*cedar.PolicySet // built-ins + one namespace's user and synthetic Policies
}

// For returns the set a request evaluates against (Decision 2); replaces Get. Lock-free when the revision is unchanged.
func (c *policyCache) For(ctx context.Context, principalNS, resourceNS v1.NamespaceName) (*cedar.PolicySet, error)

func compile(builtins string, policies []v1.Policy) (builtin *cedar.PolicySet, byNS map[v1.NamespaceName]*cedar.PolicySet, err error)

// internal/auth/cedar/schema.go
// ValidateCedarInNamespace runs ValidateCedar, then applies Decision 3; fault.Invalid names the statement index and entity.
func ValidateCedarInNamespace(ns v1.NamespaceName, text string) error
```

`scopeEntity` (`schema.go`) gains the entity `id` and the `in` entity of an `is … in` scope. `auth.Decision`,
`auth.Request` and `PolicySource` are unchanged.

| Consumes | Exposes |
|---|---|
| `PolicySource.Policies` (all namespaces, one revision) · `auth.EntityRef.Namespace` of principal and resource | Per-namespace evaluation · HTTP 400 on a foreign-namespace scope entity · the deny reasons of Decision 4 |

## Implementation plan

1. `internal/auth/cedar/policies.go`: `compile` groups by namespace; `compiledPolicies` and `policyCache.For` per
   Contracts. `internal/auth/cedar/cedar.go`: `Authorize` calls `For` with the two EntityRef namespaces and builds the
   reason per Decision 4. ADR-0175 (Proposed) also edits `egress_compile.go` and this grouping; whichever of the
   two lands second rebases onto the other.
2. `internal/auth/cedar/schema.go`: `ValidateCedarInNamespace`; `internal/controlplane/admission/policy.go:35` calls it
   with `pol.Namespace`.
3. Audit every PEP's `auth.EntityRef` construction (grep `auth.EntityRef{` and `.Ref(`) for a set `Namespace`; an
   empty one now falls to built-ins only.
4. Tests, one per scenario, named `TestScenario_<scenario_with_underscores>`: the two `*-refused` in
   `internal/controlplane/admission/policy_test.go`; five in `internal/auth/cedar/policies_test.go` (driver built with
   a fake `PolicySource`): `unscoped-policy-stays-in-namespace`, `stored-foreign-policy-inert` (feeds the foreign
   Policy directly, bypassing admission), `cross-namespace-built-ins-only`, `synthetic-stays-in-namespace` (also
   checks that `compile` puts each synthetic in its source's namespace set) and `deny-reason-names-forbid`.
5. Propagation on acceptance: ADR-0074 gets the single back-link "Superseded in part by ADR-0177 (Decisions 3–4)"; the
   F43 row reads `[ADR-0074] (+ [ADR-0177] per-namespace Policy scope)` with status
   `implemented · per-namespace scope: <status>` (precedent F88, ADR-0150); `blueprint.md:505` (the `Policy`
   paragraph) gains one sentence: a Policy governs only its namespace through a per-namespace PolicySet, and
   cross-namespace sharing, including ADR-0075's deferred cross-namespace invoke, waits for a later ADR; ADR-0158's
   owner applies Decision 6.
6. Definition of done: every scenario test passes; the existing cedar, egress, roles and admission suites stay green
   unchanged; `just ci` green.

## Review checklist

- [ ] `Authorize` evaluates a same-namespace request against that namespace's set, any other against built-ins only.
- [ ] Each namespace set holds the built-ins, the namespace's user Policies and its synthetics, and nothing else.
- [ ] The hot path stays one atomic load when the revision is unchanged.
- [ ] Admission refuses a foreign-namespace scope entity (`==`, `in`, `is … in`), not unconstrained scopes.
- [ ] A deny by forbid names the PolicyID; a cross-namespace deny says so.
- [ ] Each scenario has one named, passing test; no PEP, `auth.Request` or built-in policy text changed.

## Consequences

- Positive: a Policy's namespace is its boundary, by evaluation and at apply time; authors see which policy denied.
- Negative: a stored Policy that relied on reaching another namespace stops granting or revoking there, without an
  error; memory holds the parsed built-ins once per namespace with any Policy, EgressPolicy or RolesAssignment.
- Risks accepted: a PEP that leaves an `EntityRef.Namespace` empty is decided by the built-ins only: closed for user
  permits, but its namespace's user forbids no longer revoke a built-in permit (step 3 audits every PEP).

## Open questions

- Cross-namespace sharing — a later ADR; the decider opens a board card.
- Cluster-scoped Policies and precedence — still ADR-0074's open question; a later ADR.

## References

- ADR-0074 Decision 4 (lines 113–116), Out list (lines 67–71), Open questions (lines 256–257)
- `pkg/funcd/funcd.go:1499-1510`; `internal/auth/cedar/policies.go:77-95`; `internal/auth/cedar/schema.go:81-119`;
  `internal/auth/cedar/cedar.go:107-117`; `internal/controlplane/admission/policy.go`
- cedar-go v1.8.0 (`go.mod`) `authorize.go:48-54`: on Deny, `Diagnostic.Reasons` lists the matched forbid policies
