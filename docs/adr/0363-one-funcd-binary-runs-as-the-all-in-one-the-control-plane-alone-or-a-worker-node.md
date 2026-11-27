# ADR-0363: One funcd binary runs as the all-in-one, the control plane alone or a worker node

- **Status**: Proposed (2026-11-27)
- **Date**: 2026-11-27
- **Deciders**: green-0-rabbit
- **Tags**: multi-node, platform, cli
- **Realizes**: [FEAT-0002/F241](../feat/0002-feat-multi-node.md) (control plane and worker node roles)
- **Relates to**: [ADR-0028](0028-platform-control-plane-wiring.md) (the composition root whose options pick a role) ·
  [ADR-0011](0011-runtime-sandbox-port.md) (the process runtime a control plane alone takes) ·
  [ADR-0017](0017-scheduler-placement-port.md) (placement, which the F246 ADR extends to nodes)

## Context & Need

FEAT-0002 splits funcd across machines: a control plane keeps the API and the metastore, and worker nodes run the
namespaces placed on them. The decider chose that shape on 2026-11-16 and asked on 2026-11-17 for the epoch to be built
ahead of its ADRs, on draft PR #1495. This ADR records the first decision of that build: what one funcd process is.

Its purpose is to give each machine one command that says what it runs and what it refuses, and to let the platform
library compose exactly one role per process. Operators call the commands, `cmd/funcd` maps each to library options,
and tests embed the library in every role.

## Scenarios

- `scenario: all-in-one-unchanged`: Given funcd with no `cluster` setting, When it starts, Then it runs every
  namespace, its data plane and its API as before, and no node API listens.
- `scenario: control-plane-alone`: Given `funcd server`, When an admin applies a KVStore in a namespace placed on no
  node, Then the API stores it, the KVStore gets no phase, no data plane listens, and the node API listens.
- `scenario: server-without-containerd`: Given a machine with no containerd and `runtime.mode: containerd`, When
  `funcd server` starts, Then it takes a runtime that starts no worker, and no shim.
- `scenario: node-runs-its-namespace`: Given a worker node joined as vm1 and namespace e1 placed on vm1, When a Function
  of e1 is applied on the control plane, Then the node runs it and answers on its own data plane and edge, the control
  plane reads it Ready, and the control plane starts no worker for it.
- `scenario: node-api-reads-only`: Given a worker node, When a client writes an object through the node's own API,
  Then it gets 405 `this is a worker node`, while a read and the release of the node's own hold pass.
- `scenario: all-in-one-with-nodes`: Given funcd with `cluster.listenAddr` set, When a namespace is placed on a joined
  node, Then funcd leaves it to that node and keeps running the namespaces placed on no node.
- `scenario: one-role-per-platform`: Given the library, When a platform takes both `WithCluster` and `WithNodeAgent`,
  Then `New` fails as Invalid.

## Scope

- **In**: the three roles, their commands and library options, what each role runs and what it refuses.
- **Out**, each in its own ADR: joining and a node's identity (F242), the node link and what a node follows (F243), what
  a node writes back (F244), declared nodes and liveness (F245), placement through `spec.node` (F246), edges and hosts
  (F247), API tokens (F248), Secrets and keys (F249), node commands (F250), backups (F251) and versions (F252).
  Hands-free nodes (F253), workers on cloud nodes (F254) and node packages (F256) come later.

## Constraints & Decision drivers

- One binary and one embeddable library (blueprint, *Single-binary process model*).
- A single-host install changes nothing (F241): dev, the e2e suites, the lanes and small installs keep one process.
- The control plane never dials a node (FEAT-0002), and a control plane alone starts on a machine with no container
  runtime.

## Alternatives considered

- **Separate binaries** (`funcd-server`, `funcd-node`): three release artifacts for one library whose roles differ by
  options, not code. Rejected.
- **A thin node beside a central control plane**, as a kubelet or a k3s agent: the control plane runs every reconciler,
  and a node only workers. A node then stops serving its namespaces when the control plane is away. Rejected: the
  decider chose that a node runs everything its namespaces need (FEAT-0002), so it keeps serving them alone.
- **The role as a config key** (`cluster.role: server`, or a key that turns off local runs): the command line no longer
  says what a process is, and `funcd node join` has no `funcd node` beside it. Rejected; config keys keep the addresses
  and the directories.

## Decision

1. One binary has three roles, picked by its command: `funcd` the all-in-one, `funcd server` the control plane alone,
   and `funcd node` a worker node, after `funcd node join` (F242). The library has one option for each role beyond the
   all-in-one, `WithCluster` for a control plane that nodes join and `WithNodeAgent` for a worker node. A platform takes
   one of them or none, and `New` refuses both. `WithCluster` keeps the platform's own namespaces unless its `Alone` is
   true, so adding it to an all-in-one changes nothing else; only `funcd server` sets `Alone`.
2. `funcd server` runs the API, the metastore and the node API, on `cluster.listenAddr`, which is 0.0.0.0:7443 when
   unset. It runs no namespace: one placed on no node waits, with no phase, until an admin places it, and what a
   failed read of a placement does is the F246 ADR's. So it takes the process runtime with no shim, which starts no
   worker, whatever `runtime.mode` says, and it opens no data plane, no S3 gateway and no worker network.
3. `funcd` stays as today. With `cluster.listenAddr` set it also serves the node API, leaves each namespace placed on a
   node to that node, and runs every other namespace itself. Without it, its store keeps no change log for nodes.
4. `funcd node` runs a whole funcd under the name it joined under, from the identity under `cluster.dataDir`: the
   namespaces placed on it, with their reconcilers, edge, stores and workers, as F243 and F244 decide.
5. A worker node's own API answers reads (GET, HEAD and OPTIONS), the release of its own hold and the return of its
   quarantine (ADR-0206). Any other request gets the problem document the API's router answers a method it does not
   allow with: 405, `about:blank`, an `Allow` line of the methods the path takes (GET, HEAD and OPTIONS, and POST on the
   two hold paths), and the detail `this is a worker node: its namespaces are written on the control plane`. The node's
   copy follows the control plane, which would undo a write made there.

## Design model

None: a process takes its role once, at start. The node link (F243) and liveness (F245) carry their own models.

## Behavior rows

- **multi-node.md**: "A control plane alone" cites Decision 2 and "The node's own API" Decision 5, both `decided`, at
  acceptance; the row "A control plane alone" also names `TestAControlPlaneAloneTakesNoContainerRuntime`.

## Temporary workarounds

None.

## Contracts

```text
funcd                                          # the all-in-one; with cluster.listenAddr, nodes may join it
funcd server                                   # the control plane alone
funcd node                                     # a worker node
funcd node join --server <url> --token <token> # joins a control plane (F242)
```

```go
// pkg/funcd
type ClusterOptions struct {
	Dir        string   // the cluster directory, which holds the CA
	ListenAddr string   // the node API's address
	Driver     string   // the node API's driver: "https" (the default, also for "") or "nats" (F243)
	Hosts      []string // the names and addresses the node API's certificate covers
	Alone      bool     // true on a control plane alone (funcd server); the zero value keeps the own namespaces
}

func WithCluster(o ClusterOptions) Option // a control plane that worker nodes join

type NodeAgentOptions struct {
	Dir     string // the node's cluster directory, which holds the identity funcd node join wrote
	Version string // the funcd release, reported to the control plane
}

func WithNodeAgent(o NodeAgentOptions) Option // a worker node
```

| Role | Reads | Serves |
|---|---|---|
| `funcd` | its config | the API and the data plane; the node API with `cluster.listenAddr` |
| `funcd server` | its config; `cluster.listenAddr`, `cluster.dataDir`, `cluster.hosts`, `cluster.driver` | the API; the node API |
| `funcd node` | its config; the identity under `cluster.dataDir` | its own API, reads only; the data plane and the edge of its namespaces |

## Implementation plan

Built ahead of this ADR on the branch of draft PR #1495:

- `cmd/funcd/cluster.go`: `role`, its default node API address, `runsWorkers`, `executionOptions`, `option`,
  `clusterOptions`, and the `server` and `node` commands. `cmd/funcd/main.go` gives the S3 gateway, the worker network
  and the bridge address only to a role that runs workers.
- `pkg/funcd/options.go`: `ClusterOptions`, `WithCluster`, `NodeAgentOptions`, `WithNodeAgent`. `pkg/funcd/funcd.go`:
  `validate` refuses both, and a control plane alone opens no data plane. `pkg/funcd/multinode.go`: `controlPlaneAlone`
  and the node's read-only API, `readOnly`, which writes the router's 405 problem document.
- `internal/multinode/placement.go`: `Skip` leaves a namespace placed on no node on a control plane alone.

| Scenario | Test |
|---|---|
| all-in-one-unchanged | `TestEachRoleRunsItsOwnParts`, and the existing suites, which set no `cluster` option |
| control-plane-alone | `TestAControlPlaneAloneRunsNoNamespace` |
| server-without-containerd | `TestAControlPlaneAloneTakesNoContainerRuntime` |
| node-runs-its-namespace | `TestAFunctionRunsOnTheWorkerNodeItsNamespaceIsPlacedOn`, `TestAWorkerNodeRunsTheNamespacePlacedOnIt`, `TestAWorkerNodeRunsTheNamespacePlacedOnItOverNATS` |
| node-api-reads-only | `TestAWorkerNodesOwnAPIAnswersReadsAndItsHoldOnly`, `TestAWorkerNodeRunsTheNamespacePlacedOnIt` |
| all-in-one-with-nodes | `TestEachRoleRunsItsOwnParts`, `TestPlacementSkipsWhatAnotherNodeRuns`, `TestAWorkerNodeRunsTheNamespacePlacedOnIt` |
| one-role-per-platform | `TestAPlatformIsAControlPlaneOrAWorkerNodeNotBoth` |

Done when every test above passes and `just ci` is green.

## Review checklist

- [ ] `funcd server` takes the process runtime with no shim whatever `runtime.mode` says, and opens no data plane, no
      S3 gateway, no worker network and no bridge address.
- [ ] `funcd server` listens for nodes on 0.0.0.0:7443 when `cluster.listenAddr` is unset; `funcd` serves no node API
      without it, and its store then keeps no change log.
- [ ] `New` refuses `WithCluster` together with `WithNodeAgent`, and `WithCluster` runs the own namespaces unless
      `Alone` is true, which `funcd server` alone sets.
- [ ] A worker node's own API passes GET, HEAD, OPTIONS, `POST /apis/funcd.io/v1alpha1/hold/release` and
      `POST /apis/funcd.io/v1alpha1/quarantine/return`, and answers any other request with the router's 405 problem
      document and an `Allow` line of the methods its path takes.
- [ ] `Placement.Skip` skips a namespace placed on another node, and one placed on no node on a control plane alone
      only.
- [ ] Each scenario's test exists and passes.

## Consequences

- One binary and one library serve every role, and a single host changes nothing.
- A control plane alone runs on a small machine with no container runtime.
- On `funcd server`, nothing runs until an admin places a namespace (F246), and an object of an unplaced namespace
  shows no phase, which `funcdctl get` makes visible.
- A worker node cut off from its control plane cannot be changed through its own API; it keeps running what it holds.

## Open questions

None.

## References

- [FEAT-0002](../feat/0002-feat-multi-node.md) F241; feature issue #1478; tracker #1493; draft PR #1495.
- [blueprint.md](../../blueprint.md), *Multi-node path* and *Single-binary process model*.
- [ADR-0206](0206-restore-and-held-boot.md), the hold and the quarantine a node releases on itself.
