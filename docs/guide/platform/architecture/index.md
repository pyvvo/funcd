---
title: Architecture
description: The control plane that keeps the declared state, and the data plane that serves the calls.
---

![funcd's architecture: the control plane (API, authorization, admission, controller), the data plane (edge, invoke chain, activator, and the Executor with the workers and their local API sockets, then the services KV, Blob, rqlite, Lake and NATS) and the storage layer under them (metastore, event store, run state)](architecture.drawio.svg)

funcd runs a control plane and a data plane in one binary.

The control plane keeps the declared state. funcdctl sends a resource to funcd's API. The API authenticates the caller,
checks the write against RBAC and runs the admission pipeline, which mutates the resource and then validates it. The
API then writes the resource to the metastore. The controller watches the metastore and runs a reconciler for each
kind. The reconcilers make the platform match the declared state: they set the edge's routes and have the Executor
start the workers. The [control plane](control-plane.md) page describes the API and the controller in detail.

The data plane serves the calls. A call reaches the edge, which terminates TLS, matches the call to a Route and
applies its limits. The invoke chain checks the call against the Function's contract, and the activator starts a
worker when the Function has none running. The Executor runs the workers, as host processes by default or with
containerd and crun on Linux. It also gives each worker its local API socket, its volumes, its egress and the capture
of its logs. A worker reaches the services through its local API: KV, Blob and the SQL databases on rqlite, and its
secrets. A Lake answers queries through its own gateway. NATS is funcd's internal bus, and no Function reaches it. The
[data plane](data-plane.md) page describes the edge, the invoke chain, the activator and the Executor in detail.

Under both planes is funcd's storage layer, which consists of its three platform stores. The metastore holds every
resource that the API accepted. The event store holds what eventing must not lose, such as the deliveries that a
Sensor could not make. The run-state store holds the workflow engine's record of each run's steps. The services keep
their own data, apart from the storage layer. The [storage layer](storage-layer.md) page describes each store and its
backup.

## Internal bus

funcd embeds a NATS server as its internal bus (ADR-0008), and stores its JetStream streams under `<dataDir>/nats`.
No Function reaches it, and in this release no component publishes to it. KV's change feed, which would send its
changes to the bus (ADR-0068), is off by default. Events still pass through the in-process fanout, and a publisher
backed by the bus is planned for V2.
