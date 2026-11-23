# ADR-0352: An EgressPolicy names an add-on object, whose engine it lets out

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: egress, add-ons, network, security, cedar
- **Realizes**: [FEAT-0007/F274](../feat/0007-feat-egress-control.md) (egress for add-on engines)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md)
  Decision 3 (an engine fenced with no endpoint "until an egress-list ADR": this is that ADR);
  [ADR-0351](0351-a-jobs-worker-reaches-the-platform-as-the-job-its-own-principal.md) Decision 6 (`appliesTo` kinds
  gain an add-on kind that runs an engine)
- **Relates to**: [ADR-0117](0117-egress-policy-enforcement.md) (the gateway) · [ADR-0175](0175-engine-is-its-own-principal.md)
  (the index by owner kind) · [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the
  engines' fence) · [ADR-0115](0115-worker-network-isolation.md) (the redirect) · ADR-0353 (the edge on the bridge
  address) · [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (a runtime that serves
  engines)

## Context & Need

An add-on engine reaches nothing outward today: ADR-0332 Decision 3 fences it with no endpoint
(`provider.EgressTo("")`, `internal/services/addon/reconcile.go:265`) until an egress-list ADR. A tunnel connector,
frpc as an add-on, must dial its server outside funcd (frps behind a Bunny pull zone, experiment 0027 path C) and hand
each visitor to funcd's edge (ADR-0353). The decider chose, on 2026-11-22/23, to reuse `EgressPolicy` with no new Addon field: an
`appliesTo` item names the object with its kind, as an owner does, and the engine's connections pass the egress
gateway as a Function's do.

## Scenarios

- `scenario: engine-egress-named`: Given `server.network.egress: true`, an Addon whose kind `Tunnel` runs an engine,
  its object `public`, and an EgressPolicy with `appliesTo: [{kind: Tunnel, name: public}]` that allows
  `example.com:443`; then the engine reaches `example.com:443`, and a Function `public` in the namespace does not.
- `scenario: engine-egress-default-deny`: Given egress on and no policy that names the object, or only a policy with
  an empty `appliesTo`; then the engine's connection is refused and audited under the object's namespace and name.
- `scenario: applies-to-kind-checked`: Given an `appliesTo` item whose kind no Addon registers, or a registered kind
  whose objects run no engine (a settings kind, ADR-0340; an engineless kind, ADR-0345); then apply answers 422 naming
  the item.
- `scenario: egress-off-keeps-the-fence`: Given `server.network.egress: false`; then the engine stays fenced with no
  endpoint, as today, whatever the policies say.

## Scope

In: an `appliesTo` item that names an add-on object; the engine's outbound path through the egress gateway; its index.
Out: an Addon field that lists egress (the decider: reuse EgressPolicy); the process runtime and `funcdctl dev`, which
control no egress (ADR-0334); an add-on kind with no engine; the path to funcd's own edge, which ADR-0353 decides: a
listener on the bridge address that a workload or an engine reaches directly, never through the gateway, which never
dials the edge's addresses.

## Constraints & Decision drivers

- Default-deny stays: an engine reaches nothing unless a policy names its object; an empty `appliesTo` stays every
  Function and no Job (ADR-0351) and reaches no engine.
- One mechanism for outbound rules, the one a Function has (ADR-0117); no second fence to keep in step.
- The engine's inbound fence (ADR-0332 Decision 3: loopback and funcd's bridge address only) is unchanged.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| `appliesTo` names the object, the engine passes the gateway | one rule shape for Functions, Jobs and engines; domains, CIDRs and audit come for free | needs egress on (containerd) | chosen by the decider (2026-11-22/23) |
| An Addon field listing what its engines may reach (the "Add-on egress list" card) | the add-on's author declares it once | an admin's list, not the namespace's; a second fence to keep in step with EgressPolicy | rejected by the decider ("why don't we just reuse the apply to") |
| ADR-0269's IP:port fence per object | no gateway | a CDN name such as `*.b-cdn.net` resolves to many addresses; no domain rules, no audit | rejected |
| An `edgeHandOff` hand-off from engine to edge | none of its own | a second path to the edge | dropped by the decider ("do we really need that?"); ADR-0353's listener is the path |

## Decision

1. **The item.** An `EgressPolicy.spec.appliesTo[]` item may be `{kind: <K>, name: <object>}`, where `K` is a kind that
   an Addon registers and whose objects run an engine: the kind of ADR-0332, and with ADR-0344 a kind whose runtime
   serves engines. Admission resolves `K` against the stored Addons and answers 422 for any other kind; `Function` and
   `Job` keep ADR-0351's rule. The object need not exist yet, as a Function named by `appliesTo` need not.
2. **The path.** With `server.network.egress: true`, an add-on engine's network namespace gets the redirect of a
   Function worker's (ADR-0115, ADR-0117) in place of the fence with no endpoint, so each outbound connection reaches
   the egress gateway. Its inbound fence is unchanged. With egress off, the engine keeps the fence with no endpoint.
3. **The index.** The engine's address is in the gateway's worker index as the principal `AddonObject::"<ns>/<name>"`
   (ADR-0332 Decision 4's entity) from before the engine starts, through the add-on runtime's `BeforeStart`, which
   already indexes the engine's address (`indexAddonEngines`), and the 2 s sync reconciles it (ADR-0163). It stays in
   the engine index too, so the gateway still never dials it.
4. **The policy.** The `AddonObject` entity gains a `kind` attribute, the object's kind. An `appliesTo` item that
   names an add-on object compiles to a Cedar condition on that principal and that attribute; the rules' domains,
   CIDRs and ports are EgressPolicy's own. No policy names an engine by default.
5. **The audit.** An engine's egress decision is audited as a Function's, under its namespace and its object's name,
   with its kind.

## Design model

None. The decision adds no lifecycle, timer or racing writer. The index write precedes the engine's start in one
sequence, and the gateway's decision is ADR-0117's, unchanged.

## Behavior rows

- **egress.md**: an engine reaches out only through a policy that names its object; egress off keeps the fence; the
  index holds the engine from before its start.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1: EgressPolicySpec.AppliesTo stays []WorkloadRef. Validate checks the shape; admission checks
// a kind other than Function and Job against the Addons.

// internal/controlplane/admission
// egressAppliesTo refuses an appliesTo item whose kind is neither Function, Job nor an Addon kind whose objects run
// an engine (422, naming spec.appliesTo[i].kind).
func egressAppliesTo(ctx context.Context, addons AddonLister, ep *v1.EgressPolicy) error

// internal/auth/cedar: the AddonObject entity gains the attribute kind (string), the object's kind.

// pkg/funcd: egressPrincipal gains the add-on engine's case.
//   an engine instance of object <name> in <ns> → auth.EntityRef{Type: v1.KindAddonObject, Namespace: ns, Name: name}
```

| Item | Consumes / exposes | Notes |
|---|---|---|
| `EgressPolicy.spec.appliesTo[]` `{kind: <add-on kind>, name}` | exposes | new kinds accepted |
| `server.network.egress` | consumes | unchanged; off keeps the fence |
| the add-on runtime's `BeforeStart` | consumes | indexes the engine before it starts |

## Implementation plan

1. `api/types/v1alpha1/egresspolicy.go`: the doc of `appliesTo`; `Validate` leaves non-built-in kinds to admission.
2. `internal/controlplane/admission`: `egressAppliesTo`, reading the Addons' kinds and whether their objects run an
   engine.
3. `internal/auth/cedar` (egress compilation): an add-on object item.
4. `internal/services/addon/reconcile.go` and the containerd driver: an engine's netns redirected to the gateway when
   egress is on; the fence with no endpoint otherwise.
5. `pkg/funcd`: the engine indexed as its object's principal in the worker index, before its start and at each sync.
6. Tests, each written to pass: the admission table; the Cedar compilation; `TestScenario…` per scenario in
   `pkg/funcd`, built like `TestScenarioEngineEgressIsLake`; a case in the egress lane on containerd for
   `engine-egress-named`, with a test add-on engine.
7. Docs: egress.md, the guide's egress and add-on pages, `just generate`.
8. Done when `just ci-full` and the egress lane pass.

## Review checklist

- [ ] An `appliesTo` item may name an add-on object of a kind that runs an engine, and admission refuses every other kind.
- [ ] With egress on, an engine's connections reach the gateway; with egress off, the engine keeps its fence.
- [ ] The engine is in the worker index as its object from before it starts; the gateway still never dials it.
- [ ] An empty `appliesTo` reaches no engine.
- [ ] Every scenario has its passing `TestScenario…`, and the egress lane case passed on containerd.

## Consequences

- An add-on engine gets egress with the rule language a Function has, and its decisions are audited the same way.
- Engine egress needs containerd with egress on; elsewhere an engine stays fenced.
- The inbound fence is unchanged, so the gateway and workloads still cannot dial an engine.

## Open questions

None.

## References

- The feature issue: [#1552](https://github.com/pyvvo/funcd/issues/1552).
- The decider's answers of 2026-11-22/23, in the DR session.
- Experiment 0027 (pyvvo/experiment, private), path C.
- The Backlog card "Add-on egress list: what an add-on engine may reach outward".
