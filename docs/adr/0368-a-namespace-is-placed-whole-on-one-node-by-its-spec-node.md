# ADR-0368: A namespace is placed whole on one node by its spec.node (amends ADR-0064)

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, placement, links
- **Realizes**: [FEAT-0002/F246](../feat/0002-feat-multi-node.md) (namespaces placed on nodes)
- **Supersedes (in part)**: [ADR-0064](0064-fn-to-fn-rpc-links.md): its Temporary workaround "Same-node only" and its
  exit criterion, the Scope's Out item "the NATS-lattice transport", the Open question "Multi-node transport (NATS
  lattice)" and the (−) Consequence "Same-node only until FEAT-0002": a link never leaves its node
- **Relates to**: [ADR-0363](0363-one-funcd-binary-runs-as-the-all-in-one-the-control-plane-alone-or-a-worker-node.md)
  (the roles) · [ADR-0365](0365-a-worker-node-follows-its-namespaces-through-the-change-log-over-a-link-it-opens.md)
  (what a node follows) · [ADR-0367](0367-a-workernode-is-declared-by-name-and-reads-ready-while-its-node-polls.md)
  (declared nodes) · [ADR-0017](0017-scheduler-placement-port.md) (worker placement on one node, unchanged)

## Context & Need

A worker node runs everything its namespaces need (ADR-0363 Decision 4), so the platform must say which node runs each
namespace. The decider chose on 2026-11-16 (#1483) a placement by hand, with no scheduler: an admin places each
namespace on one node, and a namespace stays whole there, its workers, edge and stores together (FEAT-0002, *Out of
scope*: no namespace spread over nodes). ADR-0064 left links same-node only until FEAT-0002 brought a transport to
another node; a namespace on one node makes that transport unneeded.

Its purpose: `Namespace.spec.node` and `multinode.Placement` tell each process which namespaces it runs, and the
admission keeps a namespace and its data on the node that holds them.

## Scenarios

- `scenario: place-on-a-node`: Given WorkerNode vm1 joined, When an admin applies Namespace e1 with `spec.node: vm1`
  and a Function in e1, Then vm1 runs the Function and the control plane starts no worker for it.
- `scenario: an-undeclared-node`: Given no WorkerNode vm9, When an admin applies a Namespace with `spec.node: vm9`,
  Then it is refused (400 `which is not declared`).
- `scenario: the-control-planes-own-node`: Given `funcd server`, whose own node is `local`, When an admin places a
  namespace on `local`, or declares a WorkerNode named `local`, Then each is refused (400).
- `scenario: waits-for-its-node`: Given WorkerNode vm1 declared and not joined, When a Job is applied in a namespace
  placed on it, Then the Job waits, and vm1 runs it once it joins.
- `scenario: keeps-its-node`: Given e1 holds a Function, When an admin changes its `spec.node`, Then it is refused
  (409 `holds objects`); an empty namespace moves.
- `scenario: a-create-racing-a-move`: Given an empty e1 on vm1, When an admin moves it to vm2 while another creates a
  ConfigMap in it, Then the two run one after the other: the move is 409, or the ConfigMap lands after it, on vm2.
- `scenario: a-forced-group-delete`: Given a ResourceGroup of e1, which vm1 runs, When an admin deletes it with force,
  Then each pass waits up to 10 s for vm1 to collect the children of what it deleted; while vm1 is away, the delete
  answers 409, and can be tried again.
- `scenario: a-node-in-use`: Given e1 placed on vm1, When an admin deletes WorkerNode vm1, Then it is refused (409
  `still runs namespaces`).
- `scenario: unplaced`: Given an all-in-one with nodes, When a namespace has no `spec.node`, Then the all-in-one runs
  it; on `funcd server` its objects wait.
- `scenario: a-link-stays`: Given Function f1 of e1 on vm1 links f2 of e1, When f1 calls f2, Then vm1's own local API
  and data plane serve the call.
- `scenario: a-failed-read`: Given a control plane alone whose store fails a read of a Namespace, When an event of
  that namespace comes, Then the control plane leaves it, as it leaves every namespace.

## Scope

- **In**: `spec.node`, its admission, which process acts on which namespace, and links on a node.
- **Out**: a scheduler that places namespaces or Functions on nodes (FEAT-0002, *Out of scope*); a namespace's hosts
  across nodes (F247); a moved namespace's data, which workload backups carry (F251); where a worker runs on its node
  (ADR-0017).

## Constraints & Decision drivers

- No scheduler, and no namespace spread over nodes (FEAT-0002).
- A namespace's data stays on the node that ran it: there is no replication between nodes (FEAT-0002).
- A namespace placed a moment ago never runs on two nodes.

## Alternatives considered

- **A label selector or a scheduler** that picks a node: FEAT-0002 leaves placement by a scheduler out. Rejected.
- **A move that carries the data**: no store replicates between nodes, so a move would lose it or copy it by a
  mechanism the platform does not have. Rejected for "a namespace with objects keeps its node".
- **A cross-node link transport** (ADR-0064's exit criterion, NATS between nodes): no link leaves its namespace, so no
  link leaves its node. Rejected; it would be the platform's only traffic between nodes.
- **A cached placement** in each process: a namespace placed a moment ago could run on its old node. Rejected for a
  read at each call.

## Decision

1. **spec.node.** A Namespace's `spec.node` names the WorkerNode that runs it whole: its reconcilers, edge, stores and
   workers. An empty one, or the control plane's own node by name, is the local node of an all-in-one; `funcd server`
   runs no namespace, so an unplaced one's objects wait there with no phase (ADR-0363 Decision 2).
2. **Admission.** `spec.node` names a declared WorkerNode (ADR-0367), or the write is 400 `which is not declared`; a
   node that has not joined yet is fine, and the namespace waits for it. It may name the control plane's own node only
   where that process runs namespaces, else 400 `runs no namespace`, and no WorkerNode takes that name (400). A
   namespace that holds an object keeps its node: a change is 409 `holds objects`, and an empty namespace moves. The
   change checks that under the namespace's admission lock (ADR-0147), which every create through the API in a
   namespace takes on a control plane with nodes, so a create and a move run one after the other. A node's write-back
   takes no lock: it writes only into a namespace placed on it, only children and runs, which an empty namespace does
   not get, and a late child either makes the move 409 or lands as an orphan that owner GC collects. A WorkerNode
   that a Namespace names cannot be deleted: 409 `still runs namespaces`. A namespace moves with its data by workload
   backups: back its stores up, delete its objects, change `spec.node`, apply them again and restore.
3. **Where a process acts.** A process acts only on the namespaces placed on it: the controller's enqueue, owner GC,
   the JobRun slots, the Route reconciler and the WorkflowRun sweeper ask `Placement.Skip`, which skips a namespace
   placed on another node, and on a control plane alone one placed on its own node, by name or unplaced. Placement
   reads the Namespace at each call, one store read per enqueue, collected object and route pass. A failed read skips
   the namespace on a control plane alone, which runs none, and nothing on an all-in-one, whose own work goes on. A
   forced delete of a ResourceGroup of a namespace a node runs waits, on each of its passes, up to `nodeCollects` 10 s,
   looking every `nodeCollectsPoll` 100 ms, for the node to collect the children of what the pass deleted; while the
   node is away, the delete answers 409 and can be tried again.
4. **Links stay on their node.** A link stays in its namespace (ADR-0064's Scope: no cross-namespace links), and a
   namespace on its node, so a link call goes through the node's own local API and data plane, and no call crosses
   machines. This replaces ADR-0064's same-node workaround and its exit criterion: no cross-node link transport is
   built.

## Design model

None: placement is one field, read at each call, and a namespace changes node only while it holds no object, a check
made under the namespace's admission lock (ADR-0147) that every create through the API in it takes; the lock makes the
check and the write one step, so no interleaving of a create and a move is left to model, and no object runs on two
nodes. The bounded wait of a forced group
delete is one loop with a deadline, and the node learns that it gained or lost a namespace from one record of its
feed (ADR-0365).

## Behavior rows

- **multi-node.md**: "Declaring a node" (a WorkerNode under the own node's name is refused), "Placing a namespace"
  (with 400 `runs no namespace` and `TestAFailedReadOfANamespaceSkipsItOnAControlPlaneAlone`), "Moving a namespace",
  "A forced ResourceGroup delete in a node's namespace" and "A control plane alone" (with ADR-0363) cite this ADR's
  Decisions at acceptance.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: Namespace
metadata:
  name: e1
spec:
  node: vm1
```

```go
// internal/multinode
func NewPlacement(st store.Store, local v1.ObjectName, localRuns bool) *Placement // localRuns false on funcd server
func (p *Placement) NodeOf(ctx context.Context, ns v1.NamespaceName) (v1.ObjectName, error) // "" is unplaced
func (p *Placement) Skip(ns v1.NamespaceName) bool                                         // leave ns to another node
func (p *Placement) Namespaces(ctx context.Context, node v1.ObjectName) ([]v1.NamespaceName, error)
```

| Part | Reads | Writes |
|---|---|---|
| admission | the Namespace written; WorkerNodes; the namespace's objects | refuses or passes |
| every process | the Namespace at each call (`Placement`) | nothing |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495: `api/types/v1alpha1/namespace.go` (`spec.node`),
`internal/multinode/placement.go` (`Placement`), `admission.go` (`namespaceNode`, `workerNodeInUse`),
`pkg/funcd/multinode.go` (`elsewhere`, given to the controller, owner GC, the JobRun slots, the Route reconciler and
the WorkflowRun sweeper), `internal/gc/gc.go` (`CollectNamespace`), `internal/controlplane/routes_rest.go` (the 409's
error case) and `handlers.go` (`lockNamespace`). This ADR changed the built code: a failed read skips the namespace on
a control plane alone; the own node is refused on `funcd server` and as a WorkerNode's name; a change of `spec.node`
takes its namespace's lock, which every create in it takes (`createsWaitForAMove`).

| Scenario | Test |
|---|---|
| place-on-a-node | `TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn`, `TestAWorkerNodeRunsTheNamespacePlacedOnIt` |
| an-undeclared-node | `TestANamespaceNamesADeclaredNodeAndKeepsItWhileItHoldsObjects` |
| the-control-planes-own-node | `TestAControlPlaneAloneRunsNoNamespaceOnItsOwnNode`, `TestAWorkerNodeCannotTakeTheControlPlanesOwnName` |
| waits-for-its-node | `TestAJobRunsOnTheWorkerNodeItsNamespaceIsPlacedOn` |
| keeps-its-node | `TestANamespaceNamesADeclaredNodeAndKeepsItWhileItHoldsObjects` |
| a-create-racing-a-move | `TestAMoveAndTheCreatesInItTakeTheNamespacesLock`, `TestANamespacesUpdateTakesTheLockOfTheNamespaceItNames` |
| a-forced-group-delete | `TestACollectionOfANodesNamespaceWaitsForTheNode` |
| a-node-in-use | `TestAWorkerNodeANamespaceNamesCannotBeDeleted` |
| unplaced | `TestPlacementSkipsWhatAnotherNodeRuns`, `TestEachRoleRunsItsOwnParts`, `TestAControlPlaneAloneRunsNoNamespace`, `TestJobRunsOfAnotherNodesNamespaceAreLeftToIt`, `TestSweepLeavesTheRunsOfAnotherNodesNamespace`, `TestARouteOfAnotherNodesNamespaceIsNotServedHere` |
| a-link-stays | `TestALinkCallStaysOnTheWorkerNodeOfItsNamespace` |
| a-failed-read | `TestAFailedReadOfANamespaceSkipsItOnAControlPlaneAlone` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] `spec.node` is admitted only when it names a declared WorkerNode, or the control plane's own node on a process
      that runs namespaces; no WorkerNode takes the own node's name.
- [ ] A change of `spec.node` and every create in its namespace take the namespace's admission lock.
- [ ] A Namespace holding any object keeps its `spec.node`, and a named WorkerNode cannot be deleted.
- [ ] Each loop Decision 3 names asks `Placement.Skip`.
- [ ] `Placement` reads the store at each call; a failed read skips on a control plane alone only.
- [ ] No code path sends a link call to another node.

## Consequences

- A namespace that needs isolation gets a node of its own, and a node's namespaces run with no scheduler.
- A namespace's place is fixed while it holds objects: moving it is a backup, a delete, a move and a restore.
- On a control plane with nodes, the creates in one namespace take turns under its lock.
- On an all-in-one whose store fails a read of a Namespace, the all-in-one goes on with that namespace's work, even
  one placed on a node; a store that fails reads fails the work's own reads too.
- On `funcd server`, an unplaced namespace's objects show no phase; its Namespace shows no `spec.node`.
- A newly placed namespace holds no object (Decision 2), and the API ignores the ownerReferences a person sends
  (ADR-0048), so its node has no child of another's to take (routed from the ADR-0366 judge).
- At acceptance, ADR-0064 gains its `Superseded in part by` line for its workaround, its Out item, its Open question
  and its (−) Consequence.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F246; feature issue #1483; tracker #1493; draft PR #1495.
- The ADR-0363 judge's routed findings: a failed read of a placement (Decision 3) and an unplaced namespace on
  `funcd server` (Consequences).
