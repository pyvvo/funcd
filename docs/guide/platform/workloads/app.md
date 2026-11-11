---
title: App
description: A whole app in one resource, rolled out and rolled back through its revisions.
---

![funcdctl app deploy applies an App, and admission checks its parts. The App reconciler creates an AppRevision for each changed spec, waits for the Apps it requires, calls the before hooks, writes the parts and calls the after hooks. app rollback applies an earlier revision again](app.drawio.svg)

An App declares a whole app in one resource: its Functions, Workflows, Sites, Routes, stores (KV, Buckets, SQL
databases and Lakes), EventSources, Sensors and ConfigMaps, the Secrets its parts name, and its hooks. When it is
applied, admission checks each part as if it were written on its own. `funcdctl app deploy` renders an App template,
applies the App and waits until its new revision is current or has failed.

The App reconciler creates an AppRevision for each changed spec and rolls it out. It waits until every App that this
one requires is Ready at a matching version, and calls the App's before hooks. It then writes the parts and deletes
the parts that the new spec dropped. Once the new revision is current, it calls the after hooks. It writes nothing
while the App is paused, and it writes no part while a Secret that the App declares is missing.
`funcdctl app history` lists the revisions, and `funcdctl app rollback` applies an earlier one again as a new
revision. Deleting the App deletes its parts.
