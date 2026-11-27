# ADR-0369: Each node serves its namespaces at its own edge, and a host belongs to one node

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, edge, hosts
- **Realizes**: [FEAT-0002/F247](../feat/0002-feat-multi-node.md) (an edge on every node)
- **Relates to**: [ADR-0368](0368-a-namespace-is-placed-whole-on-one-node-by-its-spec-node.md) (placement) ·
  [ADR-0240](0240-namespace-hosts.md) (a namespace's hosts) · [ADR-0239](0239-site-runtime-configuration.md) (how one
  edge shares a host) · [ADR-0147](0147-atomic-admission-and-nested-call-cap.md) (the per-namespace admission lock) ·
  [ADR-0366](0366-a-worker-node-writes-back-the-status-of-what-it-runs-and-the-objects-it-makes.md) (a node's
  write-back, which no admission checks)

## Context & Need

A worker node runs a whole funcd for its namespaces (ADR-0363 Decision 4), its edge included, so a request for a
namespace's Function, Site, Lake or add-on object goes to the node that runs it and never crosses the control plane or
another node (F247). On one edge, namespaces share a host under the edge's own rules (ADR-0239, ADR-0240); two edges
on two machines cannot share one host, since nothing in funcd would pick between them. The decider chose on
2026-11-16 (D6, #1484) that a host belongs to one node, and that a claim on a host another node holds is refused when
it is applied.

Its purpose: the edge of each process serves only the namespaces placed on it, and the control plane's admission keeps
each host on one node.

## Scenarios

- `scenario: served-by-its-node`: Given Function f1 of e1, placed on vm1, with `spec.ingress.host: shop.example.com`,
  When a client calls shop.example.com at vm1's edge, Then vm1's worker answers, and the control plane's edge has no
  route for it.
- `scenario: shared-on-one-node`: Given namespaces a and b both on vm1, When both claim shop.example.com, Then both
  claims pass and vm1's edge shares the host as one edge does.
- `scenario: refused-on-another-node`: Given a claims shop.example.com on vm1, When a namespace on vm2, or on the
  control plane's own node, claims it, Then the write is 409 `belongs to worker node vm1`.
- `scenario: the-namespace-host`: Given a namespace on any node, When an object claims no host, Then it takes its
  namespace's own host, which only its node answers, and no check applies.
- `scenario: a-nodes-own-route`: Given vm1's reconciler makes a Route for a parent that passed the check, When vm1
  writes it back, Then no host check runs on it again.

## Scope

- **In**: which edge serves which namespace, and the host check across nodes.
- **Out**: the names and the TLS of each node's edge, which each node's own config sets; a gateway that shares a host
  across nodes, which sits in front of funcd; DNS; tokens checked at each edge (F248).

## Constraints & Decision drivers

- A request never crosses the control plane or another node (F247).
- Two nodes never answer one host (F247).
- One edge's sharing rules stay as they are (ADR-0239, ADR-0240).

## Alternatives considered

- **The control plane's edge proxies to the nodes**: every request would cross the control plane, which never dials a
  node (ADR-0365). Rejected.
- **A host shared across nodes, each node taking its own paths**: funcd has no front that picks a node by path, and
  each edge would answer the whole host. Rejected; a gateway in front does it.
- **The check at the edge, at the first request**: a conflict would show to callers instead of to the person who
  applied the claim. Rejected for an admission at apply time.

## Decision

1. **Each node's edge.** A process's edge serves the namespaces placed on it (ADR-0368): a worker node's edge its own,
   and the control plane's edge only the namespaces it runs itself, none on `funcd server` (ADR-0363 Decision 2). A
   namespace's own host (ADR-0240) is answered by its node only.
2. **A host belongs to one node.** A write that claims a host, a Route's `spec.host` or the `spec.ingress.host` of a
   Function, a Site, a Lake or an add-on object, is refused with 409 when a namespace on another node, the control
   plane's own node counting as one, claims the same host; the host is compared in the edge's spelling (lower case, no
   port, no trailing dot). Namespaces on one node share a host under one edge's rules.
3. **Only people's writes are checked.** The check is an admission on the control plane's API (`host-on-one-node`). A
   Route that a node's reconciler makes goes back unchecked (ADR-0366), since the parent it derives from passed the
   check.

## Design model

None: the check is one admission over the claims stored when it runs. Its one race is accepted below: two writes that
claim one host at the same moment, for namespaces on two nodes, can both pass, since namespaces never wait on each other
(ADR-0147).

## Behavior rows

- **multi-node.md**: "A host on two nodes" and "Runs and Routes of a node's namespace" (its edge part) cite this ADR's
  Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: Route
metadata:
  name: r
  namespace: a
spec:
  host: shop.example.com
```

```text
409 host shop.example.com belongs to worker node vm1, which runs namespace a that claims it: a host belongs to one
    node; to share it across nodes, put a gateway in front of them
```

| Part | Reads | Writes |
|---|---|---|
| admission `host-on-one-node` | the host claims of every namespace; their placement | refuses or passes |
| each edge | the routes of the namespaces placed on its process | nothing |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `internal/multinode/hosts.go` (`hostClaim`,
`hostOnOneNode`), registered by `internal/multinode/cluster.go`; each node's edge is its own funcd's, fed by the
namespaces placed on it (ADR-0365).

| Scenario | Test |
|---|---|
| served-by-its-node | `TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn`, `TestAWorkerNodeRunsTheNamespacePlacedOnIt` |
| shared-on-one-node | `TestAHostBelongsToOneNode` |
| refused-on-another-node | `TestAHostBelongsToOneNode` |
| the-namespace-host | `TestAHostBelongsToOneNode` |
| a-nodes-own-route | `TestANodesOwnRouteGoesBackUnchecked` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] The control plane's edge has no route for a namespace placed on a node.
- [ ] Every kind that claims a host at the edge is in `hostKinds`, and `hostClaim` uses the edge's spelling.
- [ ] A claim is refused only when the other namespace runs on another node, the control plane's own node included.
- [ ] A node's write-back runs no admission.

## Consequences

- A request goes straight to the node that runs its namespace; a node's outage takes only its own hosts down.
- Sharing a host across nodes takes a gateway in front of them, with its own rules.
- Risk accepted: two claims of one host for namespaces on two nodes, applied at the same moment, can both pass; both
  nodes then answer the host where DNS sends its clients, until a person deletes one claim.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F247; feature issue #1484; tracker #1493; draft PR #1495.
