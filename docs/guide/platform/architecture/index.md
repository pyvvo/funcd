---
title: Architecture
description: The control plane that keeps what you declared, and the data plane that serves the calls.
---

![funcd's architecture: the control plane (API, authorization, admission, controller), the data plane (edge, invoke chain, activator, and the Executor with the workers and their local API sockets, then the services KV, Blob, rqlite, Lake and NATS) and the storage layer under them (metastore, event store, run state)](architecture.drawio.svg)

funcd runs a control plane and a data plane in one binary.

The control plane keeps what you declare. funcdctl sends a resource to funcd's API, which authenticates the caller,
checks the write against RBAC and runs the admission pipeline, which mutates the resource and then validates it. The
resource is then written to the metastore. The controller watches the metastore and runs a reconciler per kind, which
makes the platform match what you declared: it sets the edge's routes and has the Executor start the workers.

The data plane serves the calls. A call reaches the edge, which terminates TLS, matches the call to a Route and
applies its limits. The invoke chain checks the call against the Function's contract, and the activator wakes a worker
when the Function has none running. The Executor runs the workers, with containerd and runc on a funcd server, and
gives each worker what surrounds it: its local API socket, its volumes, its egress and the capture of its logs. A
worker reaches the services through its local API: KV, Blob and the SQL databases on rqlite, and its secrets. A Lake
answers queries through its own gateway, and NATS is funcd's internal bus, which no Function reaches.

Under both planes sits funcd's storage layer, its three platform stores. The metastore holds every resource the API
accepted, the event store holds the deliveries a Sensor could not make, and the run-state store holds the workflow
engine's record of each run's steps. The services keep their own data, apart from the storage layer.

## Internal bus

funcd embeds a NATS server as its internal bus (ADR-0008), with its JetStream streams stored under `<dataDir>/nats`.
No Function reaches it, and in this release no component publishes to it: KV's change feed, which would drain into it
(ADR-0068), is off by default, and events still pass through the in-process fanout, with a bus-backed publisher
planned for V2.
