# ADR-0352: An EgressPolicy names an add-on object, whose engine it lets out

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: egress, add-ons, network, security, cedar
- **Realizes**: [FEAT-0007/F274](../feat/0007-feat-egress-control.md) (egress for add-on engines)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (an engine fenced with no endpoint
  "until an egress-list ADR": with egress on, it has no output chain and passes the gateway);
  [ADR-0175](0175-engine-is-its-own-principal.md) Decision 2 ("No EgressPolicy grant reaches an engine": one that names
  an add-on object reaches its engine); [ADR-0281](0281-ai-gateways.md) Decision 9 ("namespace EgressPolicies still
  never reach an engine directly": they reach the add-on objects' engines they name; the AIGateway's own permit is
  unchanged); [ADR-0351](0351-a-jobs-worker-reaches-the-platform-as-the-job-its-own-principal.md) Decision 6 and its
  Contracts' `EgressPolicySpec.AppliesTo` (the items become `AppliesToRef`, of kind Function, Job or an add-on kind
  whose objects run an engine)
- **Relates to**: [ADR-0117](0117-egress-policy-enforcement.md) (the gateway) · [ADR-0315](0315-what-a-domain-rule-allows.md)
  (the forwarder) · [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the engines' fence)
  · [ADR-0115](0115-worker-network-isolation.md) (the redirect) · ADR-0353 (the edge on the bridge address; its
  Decision 5 says when an index entry is written) · [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md)
  (a runtime entry that runs engines)

## Context & Need

An add-on engine reaches nothing outward today: ADR-0332 Decision 3 fences it with no endpoint
(`provider.EgressTo("")`, `internal/services/addon/reconcile.go:265`) until an egress-list ADR. A tunnel connector,
frpc as an add-on, must dial its server outside funcd (frps behind a Bunny pull zone, experiment 0027 path C) and hand
each visitor to funcd's edge (ADR-0353). The decider chose, on 2026-11-22/23, to reuse `EgressPolicy` with no new
Addon field: an `appliesTo` item names the object with its kind, as an owner does ("we give kind and another resource
name"), and the engine's connections pass the egress gateway as a Function's do.

## Scenarios

- `scenario: engine-egress-named`: Given `server.network.egress: true`, an Addon whose kind `Tunnel` runs an engine,
  its object `public`, and an EgressPolicy whose one `appliesTo` item has kind `Tunnel` and name `public` and that
  allows `example.com:443`; then the engine's lookup of `example.com` is answered and it reaches `example.com:443`,
  while a Function `public` in the namespace gets neither.
- `scenario: engine-egress-default-deny`: Given egress on and no policy that names the object, or only a policy with
  an empty `appliesTo`; then the engine's connection is refused and audited under the object's namespace and name.
- `scenario: applies-to-kind-checked`: Given an `appliesTo` item whose kind no Addon registers, a registered kind whose
  objects run no engine (a settings kind, ADR-0340; an engineless kind, ADR-0345), or `TUNNEL` for the kind `Tunnel`;
  then apply answers 400 naming the item.
- `scenario: applies-to-names-the-kind`: Given that policy, and an object `public` of another engine kind created after
  the Tunnel `public` is deleted; then that object's engine reaches nothing through the policy.
- `scenario: egress-off-keeps-the-fence`: Given `server.network.egress: false`; then the engine stays fenced with no
  endpoint, as today, whatever the policies say.

## Scope

In: an `appliesTo` item that names an add-on object; the engine's outbound path through the egress gateway and the DNS
forwarder; how they name its source. Out: an Addon field that lists egress (the decider: reuse EgressPolicy); the
process runtime and `funcdctl dev`, which control no egress (ADR-0334); an add-on kind with no engine; when an index
entry is written and removed (ADR-0353 Decision 5); the path to funcd's own edge, which ADR-0353 decides: a listener
on the bridge address that a workload or an engine reaches directly, which the gateway never dials.

## Constraints & Decision drivers

- Default-deny outside funcd: an engine reaches nothing outside funcd unless a policy names its object; an empty
  `appliesTo` stays every Function and no Job (ADR-0351) and names no engine.
- One mechanism for outbound rules, the one a Function has (ADR-0117); no second fence to keep in step.
- The engine's inbound fence (ADR-0332 Decision 3: loopback and funcd's bridge address only) is unchanged, and the
  gateway never dials an engine.
- funcd's set of kinds stays fixed: the OpenAPI spec and the Cedar schema name no add-on kind (ADR-0332 Constraints).

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| `appliesTo` names the object, the engine passes the gateway | one rule shape for Functions, Jobs and engines; domains, CIDRs and audit | needs egress on (containerd) | chosen by the decider (2026-11-22/23) |
| An Addon field listing what its engines may reach (the "Add-on egress list" card) | the add-on's author declares it once | an admin's list, not the namespace's; a second fence to keep in step with EgressPolicy | rejected by the decider ("why don't we just reuse the apply to") |
| ADR-0269's IP:port fence per object | no gateway | a CDN name such as `*.b-cdn.net` resolves to many addresses; no domain rules, no audit | rejected |
| `WorkloadRef` with add-on kinds in its enum, or with no enum | one type | the OpenAPI would name add-on kinds (ADR-0332), or every owner field's schema would change | rejected |
| The engine also written to the worker index before its start | one lookup | a second writer of an index that the 2 s sync diffs, which can restore a stale entry | rejected |
| An `edgeHandOff` hand-off from engine to edge | none of its own | a second path to the edge | dropped by the decider ("do we really need that?"); ADR-0353's listener is the path |

## Decision

1. **The item.** `EgressPolicy.spec.appliesTo[]` takes `AppliesToRef` (new): the `kind` and `name` of `WorkloadRef`,
   with a `kind` that is a kind name (`^[A-Z][A-Za-z0-9]{1,62}$`, ADR-0332 Decision 1's form) and no enum, so a stored
   policy and its manifest read as before. `Validate` takes `Function`, `Job` or a name that may be an add-on's kind
   (`IsAddonKind`). Admission takes an add-on kind only when a stored Addon registers it, spelled the same, as a kind
   whose objects run an engine: its `spec.kind` (ADR-0332), and with ADR-0344 the kind of a runtime entry whose
   `workloads` is `Engine`. Any other kind, a settings kind (ADR-0340), an engineless kind (ADR-0345) and a kind that
   differs from the Addon's only in case included, gets 400 (`fault.Invalid`, ADR-0148 Decision 1) naming
   `spec.appliesTo[i].kind`, and a kind that is not a kind name gets the schema's 422. The object need not exist yet,
   as a Function named by `appliesTo` need not.
2. **The path.** With `server.network.egress: true`, an add-on engine keeps its input chain (ADR-0332 Decision 3) and
   has no output chain, as an AIGateway engine has none (ADR-0281 Decision 5, `EgressOnly` nil). Its TCP then meets
   the host's prerouting redirect to the gateway, which covers the whole worker subnet (ADR-0115,
   `internal/network/nftables_linux.go`), its lookups meet the forwarder, and its TCP to an `InternalAllow` endpoint
   passes directly, as a Function worker's does. With egress off, it keeps the fence with no endpoint.
3. **The source.** No index gains a writer: an engine's address stays in the engine index only (ADR-0332 Decision 3),
   which holds it before the engine starts. The gateway and the forwarder read a source through one view,
   `egress.Sources` (new): the worker index's entry, else the engine index's, so engines join the gateway's
   `WorkerIndex` as the decider chose. An address that both indexes hold with different principals is refused, as an
   unknown source is. The engine index stays the gateway's list of destinations it never dials.
4. **The policy.** The `AddonObject` entity gains the attribute `kind`, its stored object's `spec.kind`. An item of kind
   `K` and name `N` compiles to `principal == AddonObject::"<ns>/N" && principal.kind == "K"`, so it does not carry over
   to a later object of another kind that takes the name (ADR-0332 Decision 2). The forwarder applies that item's
   domain rules to a source that `Sources` names `AddonObject::"<ns>/N"` whose stored object's kind is `K`, so the
   engine's lookups of those names are answered (ADR-0315). The rules' domains, CIDRs and ports are EgressPolicy's own.
5. **The audit.** An engine's egress decision is audited as a Function's, under its namespace and its object's name.

## Design model

None. Decision 3 adds no writer and no timer: the engine index keeps its one writer, and the gateway's and the
forwarder's reads of two indexes refuse a conflict, so no interleaving of writes is new. When an entry is written and
removed is ADR-0353 Decision 5's, with which this ADR lands, modeled in
[`docs/design/egress/address-index.fizz`](../design/egress/address-index.fizz); the gateway's decision is ADR-0117's,
unchanged.

## Behavior rows

- **egress.md**: with egress on, an engine's connections and lookups pass the gateway and the forwarder under the
  policies that name its object, an item of another kind reaches nothing, and an empty `appliesTo` names no engine;
  with egress off, the engine keeps its fence.
- **addons.md**: the row "An object's engine" (fenced with no endpoint, ADR-0332 Decision 3) changes: with egress on,
  no output chain and its egress through the gateway; with egress off, fenced with no endpoint.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 (new): EgressPolicySpec.AppliesTo becomes []AppliesToRef. WorkloadRef and the owner fields are
// unchanged.
type AppliesToRef struct {
	Kind string     `json:"kind" pattern:"^[A-Z][A-Za-z0-9]{1,62}$"` // Function, Job or an add-on's kind; no enum
	Name ObjectName `json:"name"`
}

// internal/controlplane/admission (new): Validating, on an EgressPolicy create or replace; reads the Addons through
// StoreReader, as addonObjectQuota does, and refuses an item of Decision 1's other kinds (400).
func NewEgressAppliesToAdmission(r StoreReader) Admission

// internal/network/egress (new): the caller of a source address for Deps.Workers and the forwarder; ok is false when
// neither index holds addr, or both hold it with different principals.
func Sources(workers, engines WorkerIndex) WorkerIndex

// internal/auth/cedar: the AddonObject entity gains kind (string). EgressDomains takes the kind of an AddonObject
// principal, which pkg/funcd's storeDomainRules reads from the stored object.
func EgressDomains(ep *v1.EgressPolicy, principal auth.EntityRef, kind string) []string

// internal/runtime/containerd: writeNetns admits a host-only worker with a nil EgressOnly while egress is on, and
// network.FenceNetns(path, nil, admit) writes the input chain alone (new). With egress off a host-only worker still
// needs an egress fence. internal/services/addon: an engine's EgressOnly is nil while egress is on.
```

| OpenAPI | Before | After |
|---|---|---|
| `EgressPolicySpec.appliesTo` items | `WorkloadRef`, `kind` an enum of `Function`, `Lake`, `Job` | `AppliesToRef` (new), `kind` a string of the kind-name pattern |
| `WorkloadRef` and the owner fields | — | unchanged |

| Item | Consumes / exposes | Notes |
|---|---|---|
| `EgressPolicy.spec.appliesTo[]` | exposes | add-on kinds whose objects run an engine |
| `server.network.egress` | consumes | unchanged; off keeps the fence |
| the worker and engine indexes | consume | read through `Sources`; written as ADR-0353 Decision 5 says |

## Implementation plan

1. `api/types/v1alpha1`: `AppliesToRef`, `EgressPolicySpec.AppliesTo`, `Validate`; `just generate` (the OpenAPI and
   the SDKs).
2. `internal/controlplane/admission`: the admission, registered in `pkg/funcd`.
3. `internal/auth/cedar`: the `kind` attribute; the item's condition in `appliesToScope`; `EgressDomains`.
4. `internal/services/addon/reconcile.go`: `EgressOnly` nil while egress is on, `provider.EgressTo("")` otherwise;
   `internal/runtime/containerd` `writeNetns` and `internal/network` `FenceNetns`: the input chain alone.
5. `internal/network/egress`: `Sources`; `pkg/funcd` `buildEgress` gives it to the gateway and the forwarder, and
   `storeDomainRules` reads an engine's kind.
6. Lands with ADR-0353 Decision 5.
7. Tests, each written to pass: the type's and the admission's tables, a case-only kind included; the Cedar
   compilation; `Sources`, a conflict refused; one `TestScenario…` per scenario in `pkg/funcd`, built like
   `TestScenarioEngineEgressIsLake`; a case in the egress lane on containerd for `engine-egress-named`, a lookup and a
   connection, with a test add-on engine.
8. Docs: the egress.md and addons.md rows, the guide's egress and add-on pages.
9. Done when `just ci-full` and the egress lane pass.

## Review checklist

- [ ] `appliesTo` items are `AppliesToRef`, the OpenAPI changes only there, and admission refuses every kind but
  Function, Job and an engine kind as spelled.
- [ ] With egress on, an engine has its input chain and no output chain, and its connections and lookups reach the
  gateway and the forwarder; with egress off, it keeps its fence (`writeNetns`, `FenceNetns`).
- [ ] No index gains a writer; the gateway and the forwarder read `Sources`, which refuses a conflict; the gateway
  still never dials an engine.
- [ ] An empty `appliesTo` reaches no engine, and an item reaches no object of another kind of its name.
- [ ] Every scenario has its passing `TestScenario…`, and the egress lane case passed on containerd.

## Consequences

- An add-on engine gets egress with a Function's rule language, lookups included, audited the same way.
- Engine egress needs containerd with egress on; elsewhere an engine stays fenced.
- With egress on, an engine reaches the `InternalAllow` endpoints directly, as a Function worker does: the operator's
  entries, the SQL change feed, which a token guards (ADR-0235), and the edge on the bridge address (ADR-0353). The
  inbound fence is unchanged, so no workload can dial an engine.
- A client generated from the OpenAPI spec reads an `appliesTo` item's kind as a string.

## Open questions

None.

## References

- The feature issue: [#1552](https://github.com/pyvvo/funcd/issues/1552).
- The decider's answers of 2026-11-22/23, in the DR session.
- Experiment 0027 (pyvvo/experiment, private), path C.
- The Backlog card "Add-on egress list: what an add-on engine may reach outward".
