# FEAT-0002: Multi-node, a control plane and worker nodes that pull their work

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-11-16
- **Deciders**: green-0-rabbit
- **Defines**: a **multi-node epoch**: the control plane keeps only the API and the metastore, and worker nodes join
  it, pull the objects of the namespaces placed on them, and run everything else for those namespaces: the edge, the
  services and stores, the reconcilers and the workers. An all-in-one funcd stays as it is today. The blueprint and
  [ADR-0064](../adr/0064-fn-to-fn-rpc-links.md) already name FEAT-0002 as multi-node's home; this document gives it one.

## Initial need

funcd is one process today: `funcd.New` builds the API, the metastore, the reconcilers, the services, the data plane
and the runtime in one call (`pkg/funcd/funcd.go`). Everything a namespace uses lives on that one machine.

The need is to run workloads on other machines than the control plane's: a home server keeps the control plane, and
Linux VMs or cloud machines run the namespaces. Those machines often sit behind NAT, or in cloud containers that
accept no inbound control connection, so a node must dial out and never be dialed. A namespace's traffic, its stores
and its Secrets stay on its node, and the node keeps serving while the control plane is down.

The blueprint's "Multi-node path" line still sketched NATS registration and a scheduler, `WorkerNode` is an empty
kind (`api/types/v1alpha1/workernode.go`), and open issue #1438 plans to remove that kind. This epoch replaces the
sketch, and it needs the kind.

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decisions were refined with the decider on 2026-11-16, before
the first ADR was drafted.

## Features

F241 comes first: every other row needs the two roles. F242 and F243 make a node join and follow its namespaces, and
F244 to F252 build on them. F253 also needs Jobs from FEAT-0012. F254 is deferred until cloud nodes are built. F255
was dropped on 2026-11-17 (see Out of scope).

### This epoch

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F241 | **Control plane and worker node roles**: one funcd binary runs all-in-one as today, as the control plane alone (the API and the metastore), or as a worker node. A single-host install changes nothing. **Why**: the control plane and the workloads can then live on different machines, while dev, the e2e suites, the lanes and small installs keep one process. | — | — | idea |
| F242 | **Join with a one-time token**: an admin, or automation that an admin set up, mints a token bound to one node name. The node checks that it reaches the real control plane before it sends the token, and joins as that node. A token works once, and a name that a running node holds is refused. **Why**: a node can join from anywhere with no long-lived secret on the machine or in its cloud user data, and no machine can take over another node's name. | F241 | — | idea |
| F243 | **A node follows its namespaces**: a node keeps a local copy of the objects of the namespaces placed on it, gets each change within moments, resumes after either side restarts, and keeps serving while the control plane is down. The control plane never opens a connection to a node. **Why**: nodes sit behind NAT or in cloud containers that accept no inbound control connection, and a control-plane outage must not stop traffic. | F241 · F242 | — | idea |
| F244 | **A node reports back**: a node writes the status of what it runs and the children it owns, while spec stays with people and CI, and the control plane validates every node write. **Why**: the control plane shows the truth of every node, and a node never overwrites what a person wrote. | F243 | — | idea |
| F245 | **Node liveness and declared nodes**: a node shows Ready while it reports and NotReady when it stops, and an admin can declare a node before its machine exists. **Why**: operators see which nodes run, and automation can declare a node and its namespaces ahead of the machine. | F242 | — | idea |
| F246 | **Namespaces placed on nodes**: an admin places each namespace on one node, and a node runs every namespace placed on it. An unplaced namespace runs on the local node of an all-in-one funcd, and a namespace placed on a node that has not joined yet waits for it. **Why**: a namespace's workers, edge and stores stay together on one machine with no scheduler, and a namespace that needs isolation gets a node of its own. | F241 · F245 | — | idea |
| F247 | **An edge on every node**: each node serves its namespaces' traffic itself. A host belongs to one node: the namespaces on that node share it under today's rules, and a claim on a host that another node holds is refused when it is applied. **Why**: a request never crosses the control plane or another node, and two nodes never answer one host. | F246 | — | idea |
| F248 | **Tokens checked on the node**: API tokens are stored only as hashes, change with no restart and expire, and each node's edge checks the callers of its namespaces itself. **Why**: tokens live in config files today and change only with a restart, and a node must check callers while the control plane is away. | F243 | — | idea |
| F249 | **Secrets and keys on nodes**: a node holds the Secrets and credential keys of its own namespaces only, encrypted at rest. Each namespace has its own key, so a leaked node exposes only the namespaces it runs, and a namespace keeps its credentials when it moves to another node. **Why**: workers read their Secrets and derived credentials on their node, and no node is trusted with every namespace. | F243 | — | idea |
| F250 | **Node commands across machines**: the commands that need a node's state or an action on it (logs, run logs, dead letters and their replay, App retries, volume deletes, holds and quarantine, backup status) and the checks before a write (delete protection, volume booking, the in-place restore guard) work for a namespace on any node as they do on one host. A node that is offline answers at once instead of hanging. **Why**: funcdctl must behave the same wherever a namespace runs. | F243 | — | idea |
| F251 | **Backups per process**: each process backs up the stores it holds: an all-in-one as today, the control plane its metastore and key escrow, and each node its own stores on its own schedule. Workload backups do not change, and a replaced node restores from its node's backup. **Why**: no backup can cut stores on different machines at one moment, and a lost node must come back without the others. | F241 · F246 | — | idea |
| F252 | **Nodes on older releases**: a node may run an older release than the control plane, within the node interface's supported versions, never a newer one, and the control plane refuses a spec that a namespace's node is too old to run. **Why**: funcd ships releases every few days, so nodes cannot all upgrade at once. | F243 | — | idea |
| F253 | **Hands-free nodes**: an admin declares a node, its namespaces and a provisioning Job once; the Job gets that node's one-time token, creates the machine with it, and the machine joins with no human step. Removing a node is declared the same way, once its namespaces have moved. **Why**: nodes on Hetzner, AWS or another cloud come and go through automation, like the rest of the platform. | F242 · F245 · F246; F137, F139, F140, F141, F257 (FEAT-0012) | — | idea |
| F254 | **Workers on cloud nodes**: how a node that is itself a cloud container (Azure Container Apps, Cloud Run, Bunny) runs its workers. Deferred: it is decided before cloud nodes are built. **Why**: those platforms give no privileged containers, so containerd cannot run there. | F241 | — | idea |
| F256 | **Packages for nodes**: funcd is released for each OS and CPU with its runtime images, and as a container image for cloud nodes. **Why**: a remote node needs something to install, and CI ships only funcdctl today. | F241; F19 (FEAT-0000) | — | idea |

### In other epochs, needed here

These rows are tracked, with their status and ADRs, in their own epoch's table; this list only points at them.

| # | Feature | Tracked in | Needed by |
|---|---|---|---|
| F137 | **Jobs: work that runs to completion** | [FEAT-0012](0012-feat-infrastack.md) | F253 |
| F140 | **A run's own identity** | [FEAT-0012](0012-feat-infrastack.md) | F253 |
| F141 | **The InfraStack add-on**, which ships the OpenTofu runtime | [FEAT-0012](0012-feat-infrastack.md) | F253, for nodes created by an OpenTofu module |
| F257 | **A Function starts a Job run** | [FEAT-0012](0012-feat-infrastack.md) | F253, when a Function starts the provisioning |

## How it lands on funcd (high level)

The control plane keeps the API and the metastore. A node pulls the objects of the namespaces placed on it, keeps a
local copy, runs the reconcilers, the edge, the services and the workers for them, and writes status back. A command
that needs a node becomes a request the node picks up, so the control plane never dials a node. An all-in-one funcd
runs both roles in one process, as today.

The work lands in phases, each shippable, with the all-in-one working throughout: the role split inside one process,
with no behavior change; the node interface and a first node in a Linux VM; the edge on the node; data, disaster
recovery and upgrades; then cloud nodes.

## Out of scope

- A scheduler that places namespaces or Functions on nodes by itself.
- One namespace spread over several nodes.
- Replicating a namespace's stores to another node, or keeping stores on shared storage.
- Any change to an all-in-one install.
- One host shared by namespaces on different nodes (F255, dropped on 2026-11-17): a gateway in front routes the host's
  paths to each node, as Azure Container Apps' environment-level routing does for its apps.

## Exit criterion

A control plane runs on one machine and a worker node on another: the node joins with a one-time token, runs the
namespaces placed on it, serves their traffic, and keeps serving while the control plane is down. `funcdctl logs` and
the other node commands work for those namespaces as they do on one host. An all-in-one install behaves exactly as
before.
