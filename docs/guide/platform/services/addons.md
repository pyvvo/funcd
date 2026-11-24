---
title: Add-ons
description: Runtimes and engines built outside funcd that an admin registers. Functions and Jobs run on an add-on's runtime, and funcd runs one engine per object, reached only through funcd.
---

![An admin's Addon, with an AddonRevision for each change of it, can add four things. A runtime for Functions and Jobs gives their workers its image. A kind with an engine, such as IdentityProvider, runs one engine per object. The objects of an engineless kind, such as InfraStack, are reconciled by their kind's Function. The objects of a settings kind, such as RauthyPasswordPolicy, are applied by a settings Function](../../assets/addons.drawio.svg)

An add-on is built outside funcd, such as rauthy, an OpenID Connect provider. An admin registers it with an Addon: the
runtimes it ships, each an image pinned by digest from a registry that `addons.registries` lists and the workloads it
serves. An entry for `Function` and `Job` is a runtime that Functions and Jobs name; one entry for `Engine` runs the
kind the add-on adds, with the port and readiness path of the engine, the storage it keeps, and the schema of its
objects' `spec.config`. Users then declare objects of that kind as they declare a Lake, at the kind's plural, and funcd
checks each object's config against the schema when it is written.

## Runtimes for Functions and Jobs

A Function's or a Job's `spec.runtime` resolves to one of funcd's own runtimes, else to an Addon's of that name, else to
the operator's image. The gate reads the Addons on each pass, and an Addon's change requeues every Function and Job on
a runtime that is none of funcd's, so one applied before its Addon recovers. A Revision records the image of the
Addon's runtime that serves it, and its workers run that image on containerd: an upgrade of the Addon moves no
Revision, and a deleted Addon fails its Functions at their next pass while their serving workers keep serving.

## One engine per object

![An add-on object pins a revision and runs one fenced engine. Only funcd reaches the engine: an HTTP client's call goes through the edge, at the object's host and under its basePath, and a call of a Function that binds the object goes through funcd's facet or proxy, where Cedar checks addon::call. Backups copy the engine's own snapshot](../../assets/addons-engine.drawio.svg)

funcd runs one engine per object on its add-on runtime, on containerd, from the image of the Addon's kind entry, with
the object's resources, as it runs a Lake's engine. A process server runs no engine. The engine admits new connections
only from loopback and funcd's bridge address. With egress control off it reaches no endpoint; with it on, its
connections pass the egress gateway, as a Function's do, so it reaches what an EgressPolicy that names its object
allows. The edge serves it at the object's `ingress.host` under the Addon's `basePath`, with the path kept, and refuses
the Addon's private paths however they are spelled. A held object has no engine and no edge entry.

## Revisions

Each change of an Addon is an AddonRevision, named after the Addon and its generation, such as `rauthy-2`, and replaces
no engine: each object runs the revision it pins in `status.addonRevision`. A standalone object pins the current
revision at its first pass and at each edit. An App's part follows its App's latest revision once that revision is
applied, and a rollback of the App takes the part back. funcd removes a revision that no object pins and no App
revision records.

## App templates

An App template can carry its add-on: its `addon.yaml` holds the Addon, which an admin's deploy registers or updates
before the App, and the App's `addons` section holds objects of the add-on's kind, so the App's Functions, hooks and
revisions run around them. A developer's deploy needs the kind registered already.

## Calls of an engine

Only funcd reaches an engine. A Function that binds an object in `spec.bindings.addon` calls its engine through funcd:
with `ctx.addon.fetch` on its local API, or with any HTTP client at `FUNCD_ADDON_<ALIAS>_URL`, the add-on proxy, with
its token `FUNCD_ADDON_<ALIAS>_TOKEN`. On each call, funcd asks Cedar whether the Function may perform `addon::call` on
the object, checks the path as the edge does, refuses a call into a held namespace, and forwards the call without the
forwarding and `X-Funcd-*` headers, following no redirect. An App's Function binds only an object its App declares.

## Settings kinds

An Addon can add settings kinds, such as rauthy's `RauthyPasswordPolicy`. An object of one runs no engine and names its
target, an object of the Addon's kind. The kind's Function applies it, and the Function's contract is the kind's schema:
funcd checks each write against that contract and the kind's constraints, then sends each target's full list of objects
to the Function: at each change of the list, after a failed call's backoff, and at the kind's resync. The Function
applies the list through its binding of the target, and its answer writes each object's status. funcd keeps what it
knows of the calls in memory, so a start sends each list again.

The Addon names the Function's image, and funcd creates the Function from it, as a Workflow creates a step's, for each
target that an object of the kind names. It binds the target and the target's Secrets, and has no address. Once the
target's last object is gone, the Function gets the empty list, puts the engine's default back, and is removed. An Addon
can name an existing Function of the namespace instead, and a target can name its own: funcd creates neither, and their
contracts judge the call, not the write.

A kind is read whole from the revision that its target pins: the image, its contract, the constraints and the resync.
funcd reads an image's contract once, from the registry, when the Addon is registered, and keeps it with the revision,
so no write and no pass waits on a registry. A change of the Addon reaches a target when the target moves.

## Engineless kinds

An Addon's kind entry may serve `Engineless`: its objects run no engine, and a Function reconciles them, called with
its group's full list as a settings kind's Function is, at each change, after a failed call's backoff, at the kind's
resync, and once a JobRun of the namespace ends. funcd creates that Function for each object from the image that the
Addon names, and it binds the Secrets and the Jobs that the object names, so a group is one object; or it is an
existing Function of the namespace. An object's first pass stores `status.workload: Engineless` before any call carries
it, and from then on an object with `deletion: delete` is held at its deletion: the API's DELETE, the owner collector
and an App's prune set its `metadata.deletionTimestamp` instead of removing it, and only its Function's ok to a call
that carried it as deleting removes it. An App's other parts outlive its engineless parts, so the Function and the Jobs
that destroy a stack are still there to do it.

## Backups

A BackupSchedule copies an add-on object when its scope names it: each Backup copies the newest consistent copy that
the engine wrote of its own data, at the Addon's `snapshot.path`. A Restore of an add-on object is in place only:
funcd holds the namespace, restarts the engine with the copy, then again without it.

The [Add-ons](../../develop/services/addons/index.md) Develop page shows how to register one, declare its objects, call
its engine and apply its settings, and [Build an add-on](../../develop/services/addons/build.md) how one is made.
