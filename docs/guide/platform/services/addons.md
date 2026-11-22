---
title: Add-ons
description: Engines built outside funcd that an admin registers, one engine per object, reached only through funcd.
---

![An admin's Addon registers a kind, and each change of it is an AddonRevision. A developer's add-on object, such as an IdentityProvider, pins a revision and runs one fenced engine, which the edge serves at its host and a Function that binds it reaches through funcd's facet or proxy, Cedar's addon::call checked on each call. A settings object runs no engine: its target's list goes to the kind's Function. Backups copy the engine's own snapshot](../../assets/addons.drawio.svg)

An add-on is an engine built outside funcd, such as rauthy, an OpenID Connect provider. An admin registers it with an
Addon: the kind it adds, its engine's image pinned by digest from a registry that `addons.registries` lists, the port
and readiness path of the engine, the storage it keeps, and the schema of its objects' `spec.config`. Users then declare
objects of that kind as they declare a Lake, at the kind's plural, and funcd checks each object's config against the
schema when it is written.

## One engine per object

funcd runs one engine per object on its add-on runtime, on containerd, with the object's resources, as it runs a Lake's
engine. The engine is fenced: it reaches no endpoint, and it admits new connections only from loopback and funcd's
bridge address. The edge serves it at the object's `ingress.host` under the Addon's `basePath`, with the path kept, and
refuses the Addon's private paths however they are spelled. A held object has no engine and no edge entry.

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
target, an object of the Addon's kind. funcd checks each write against the kind's schema and its constraints, then
sends each target's full list of objects to the kind's Function, or to one that the target names in `spec.functions`:
at each change of the list, after a failed call's backoff, and at the kind's resync. The Function applies the list
through its binding of the target, and its answer writes each object's status. funcd keeps what it knows of the calls in
memory, so a start sends each list again.

## Backups

A BackupSchedule copies an add-on object when its scope names it: each Backup copies the newest consistent copy that
the engine wrote of its own data, at the Addon's `snapshot.path`. A Restore of an add-on object is in place only:
funcd holds the namespace, restarts the engine with the copy, then again without it.

The [Add-ons](../../develop/services/addons/index.md) Develop page shows how to register one, declare its objects, call
its engine and apply its settings, and [Build an add-on](../../develop/services/addons/build.md) how one is made.
