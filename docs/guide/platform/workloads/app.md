---
title: App
description: A whole app in one resource, rolled out and rolled back through its revisions.
---

![funcdctl app deploy applies an App, whose parts admission checks; the App reconciler stamps an AppRevision per changed spec, waits for the Apps it requires, calls the before hooks, writes the parts and calls the after hooks; app rollback applies an earlier revision again](app.drawio.svg)

An App declares a whole app in one resource: its Functions, Workflows, Sites, Routes, stores (KV, Buckets, SQL
databases and Lakes), EventSources, Sensors and ConfigMaps, the Secrets its parts name, and its hooks. When it is
applied, admission checks each part as if it were written on its own. `funcdctl app deploy` renders an App template,
applies the App and waits until its new revision is current or has failed.

The App reconciler stamps an AppRevision for each changed spec and rolls it out. It waits until every App this one
requires is Ready at a matching version, calls the App's before hooks, writes the parts and prunes the ones the new
spec dropped, then calls the after hooks once the new revision is current. It writes nothing while the App is paused,
and no part while a Secret the App declares is missing. `funcdctl app history` lists the revisions, `funcdctl app
rollback` applies an earlier one again as a new revision, and deleting the App deletes its parts.
