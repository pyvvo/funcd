---
title: Function
description: Who calls a Function, and what it binds.
---

## Callers

![External callers reach a Function through the edge. Another Function and a Job's run call it through their local API over a link. A Sensor, a Workflow step, an App hook and an add-on's kind also call it. Every call goes through the invoke chain and the activator to the Function's workers](../../assets/function-callers.drawio.svg)

A Function is called from outside funcd or from inside it. From outside, an HTTP client reaches it through the edge,
on one of its Routes or at `/function/<name>`. From inside, another Function or a Job's run calls it through its local
API, over a link that the caller declares in `spec.bindings.links`. A Sensor calls it in a function action, a Workflow
runs it as a Function step, an App calls it at a lifecycle hook, and an add-on's settings or engineless kind sends it
the objects to apply.

Every call, whoever makes it, goes through the invoke chain. Admit runs before funcd reads anything about the
Function. Before runs before the worker is called or started. After runs on the answer, before the caller sees it.
When the Function has no worker running, the activator starts one and holds the call until the worker is ready.

## Bindings

![A Function's bindings name stores (KVStores, Buckets, SQL databases and Lakes), configuration (ConfigMaps and Secrets) and what it calls: another Function over a link, a Job whose runs it starts, and an add-on object whose engine it calls. Its volumes come from spec.volumes](../../assets/function-bindings.drawio.svg)

A Function names what it uses in `spec.bindings`: KVStores, Buckets, SQL databases and Lakes, ConfigMaps and
Secrets, and what it calls: other Functions (`links`), Jobs and add-on objects. Its volumes come from `spec.volumes`.

Each store runs on an engine. KV runs in memory by default, or on Badger when the operator chooses the durable engine. A
Bucket keeps its objects in files or in S3. A SQL database runs on rqlite, which keeps its data in SQLite. A Lake runs
on DuckDB with DuckLake, and keeps its files under a prefix of a Bucket. A volume is a directory of a fixed size on the
local disk.
