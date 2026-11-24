---
title: App
description: A whole app in one resource, rolled out and rolled back through its revisions.
---

![funcdctl app deploy applies an App, and admission checks its parts. The App reconciler creates an AppRevision for each changed spec, waits for the Apps it requires, calls the before hooks, writes the parts and calls the after hooks. app rollback applies an earlier revision again](../../assets/app.drawio.svg)

An App declares a whole app in one resource: its Functions, Jobs, Workflows, Sites, Routes, stores (KV, Buckets, SQL
databases and Lakes), EventSources, Sensors and ConfigMaps, the Secrets its parts name, and its hooks. When it is
applied, admission checks each part as if it were written on its own. `funcdctl app deploy` renders an App template,
applies the App and waits until its new revision is current or has failed.

The App reconciler creates an AppRevision for each changed spec and rolls it out. It waits until every App that this one
requires is Ready at a matching version, and calls the App's before hooks. It then writes the parts and deletes the
parts that the new spec dropped. Once the new revision is current, it calls the after hooks. It writes nothing while the
App is paused, and it writes no part while a Secret that the App declares is missing, or lacks a key and is owned by
none of the App's Jobs. Each hook call leaves an
Invocation, a read-only record that belongs to the AppRevision and goes away with it. When a hook fails, the reason
names that record and its error.

An App never waits for its stacks. An add-on part of a kind that runs no engine, such as a stack, is Ready only once a
person has approved its change and its run has applied it. Until then its line in `status.children` is `Lagging`, with
the stack's own reason, such as `AwaitingApproval`. A part that names a Secret that one of the App's Jobs owns, such as
the Secret where a stack's Job writes its outputs, is `Lagging` with `SecretKeyMissing: Secret/<name> <key>` while that
Secret lacks a declared key. funcd does not write such a part until the Job writes the key, and a copy written earlier
keeps its spec. The Job itself and the before hooks' Functions are written as usual. A `Lagging` part holds neither the
App's Ready condition, nor the switch to its new revision, nor the rollout's deadline: the rollout ends on what funcd
runs, and the stacks and the parts that read their outputs follow. A part whose wait ends during a rollout gets a full
`app.upgradeTimeout` to turn Ready, counted from `status.lagEndedAt`. The after hooks run, and the parts that the new
spec dropped are deleted, only once no part is `Lagging`.

`funcdctl app history` lists the revisions, and `funcdctl app rollback` applies an earlier one again as a new
revision. Deleting the App deletes its parts. The runs of an App's Jobs are no parts: a rollout waits for no run, and a
run keeps the Revision of its Job that it started on, but deleting the App deletes its Jobs, which ends their runs as
deleting a Job does.
