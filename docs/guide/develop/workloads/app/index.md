---
title: App
description: Install an App from its template, read its status and its parts, change a value, roll back, run its hooks, and delete it.
---

## Usage

An [App](../../../platform/workloads/app.md) declares a whole app in one resource: its Functions, Routes, stores and
other objects, which funcd writes as the App's **parts** and installs, upgrades, rolls back and deletes as one.

The volumes example installs from an **App template**: a folder with `app.yaml`, which names the App, its version, its
images and the **values** that it takes, and a `resources` folder of spec sections. funcdctl renders the template with
the values files that `-f` names; [`funcdctl app deploy`](../../tools/funcdctl/spec.md#funcdctl-app-deploy) applies it
to a server, and `funcdctl dev` to a local platform, with each image's build from the `--build` folder.

Install the [dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`.
Start the example:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/volumes/app -f apps/volumes/values/redeploy.yaml --build apps/volumes/functions/ts --gport 8080 --cport 8081
```

The template's `app.yaml`:

::include[../../../../../_repos/funcd-functions/apps/volumes/app/app.yaml]

A template folder holds only `app.yaml`, `app.lock`, `addon.yaml` and `resources/*.yaml`
([carry an add-on](#carry-an-add-on)); an `app.yaml` that names images sets their
`registry`, which `funcdctl dev` replaces with its own. A part declares no store of its own: a `resources` file
declares each KV store, Bucket, SQL database, Lake, ConfigMap and Secret at its top level, and the part binds it.
funcdctl replaces each `${{ … }}` expression when it renders. `resources/files.yaml` declares two Functions, each with
[`volumes`](spec.md#appspecfunctionsvolumes): `data`, a `local` volume that keeps its files through a crash, a new
revision and a scale to zero, and `scratch`, an `emptyDir` that each worker gets empty. A server accepts volumes only
with `runtime.mode: containerd` and a `volumes.capacity`.

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/files.yaml]

`resources/routes.yaml` gives each Function a [Route](../../security-and-networking/networking/index.md):

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/routes.yaml]

funcd rolls out each version of the spec as a **revision**, `volumes-1` first; it becomes current once no part is
pending. Once `funcdctl dev` prints `App volumes deployed in <duration>`, read the App in another terminal:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

`status.phase` is `Ready`, `currentRevision` is `volumes-1`, and `children` lists the Routes as `Ready` and the
Functions as `NotStarted`: a Function with no `replicas` runs no worker until its first call. A rollout does not wait
for a `NotStarted` part, so a build that cannot load shows only when a call starts it, and the App turns `Degraded`.

Write a note, read it back, and list the other Function's files:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"write","name":"note","text":"hello"}' http://127.0.0.1:8080/files
{}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"read","name":"note"}' http://127.0.0.1:8080/files
{"text":"hello"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/peer
{"names":[]}
```

Each Function has a `data` volume of its own. Read the App again until every child is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

Keep the example running for the next two sections; Ctrl-C loses the App and the note.

## Change a value and roll out a new revision

Each change of the spec, other than a pause or a resume, makes a new revision; the same spec makes none.

- `funcdctl dev` pushes every build itself, and renders and applies the App again when an input changes.
- On a server, `funcdctl app deploy` renders each image with the digest that `app.lock` pins, or that the tag names at
  that deploy when the folder has no lock. A pushed template always holds a lock, so a new build reaches the App through
  a new one, which [`funcdctl app lock`](../../tools/funcdctl/spec.md#funcdctl-app-lock) writes. `funcdctl app deploy`
  exits with 1 when the new revision fails.

Change `timeout` in `apps/volumes/values/redeploy.yaml` from `31s` to `45s` and save. `funcdctl dev` rolls out
`volumes-2`, then prints `reloaded in <duration>`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
```

Only `volumes-files` changed, so only it started a [new revision](../function/index.md#changes-and-deletion). The note
is still in its volume:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"read","name":"note"}' http://127.0.0.1:8080/files
{"text":"hello"}
```

## Roll back to an earlier revision

[`funcdctl app rollback <app> <number>`](../../tools/funcdctl/spec.md#funcdctl-app-rollback) applies an earlier
revision's spec as a new revision, so the history keeps every change. It restores the spec, not the data: stores and
volumes keep what they hold, and a dropped store whose `deletion` is `delete` comes back empty. Only the current
revision and the newest 10 others, `app.revisionHistory`, can be named. `--dry-run` prints the revision and each part
that it would `create`, `update` or `prune`, and each hook call:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1 --dry-run
would apply App/volumes
  revision volumes-3
  update Function/volumes-files
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1
applied App volumes with the spec of volumes-1
```

The command returns before the revision is current; repeat this one until revision 3 is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
3          1.0.0     Ready   2026-11-13T17:02:51.403Z
```

`funcdctl dev` keeps the rolled-back App until an input changes. Press Ctrl-C in the first terminal to stop.

## Run hooks on each new revision

A **hook** is a Function of the App that funcd calls for each new revision: [`spec.hooks`](spec.md#appspechooks)
lists `preApply` hooks, which run before the other parts change, such as a migration, and `postApply` hooks, once the
revision is current. funcd calls each list in order, with a CloudEvent whose data is the
[hook input](spec.md#apprevisionspechookinput): the event, `install`, `upgrade` or `rollback`, the App, and the current
and new revisions with their versions.

- Until every pre-hook succeeds, funcd writes only the pre-hooks and what they bind, and the current revision keeps
  serving; a failed pre-hook fails the revision with `HookFailed`.
- A failed post-hook makes the App `Degraded` with `HookFailed`, and holds the later post-hooks and the pruning.
- [`funcdctl app retry <app>`](../../tools/funcdctl/spec.md#funcdctl-app-retry) calls a failed hook again.
- A call that ends during a stop, a pause or a [hold](../../../platform/disaster-recovery.md) is made again, so a hook
  must be safe to repeat.

`funcdctl dev` calls hooks only with `--hooks`. The todo example's `resources/hooks.yaml`:

::include[../../../../../_repos/funcd-functions/apps/todo/app/resources/hooks.yaml]

Each hook records its call in a KV table that it owns, and `todo-api` returns both records. An App declares no role
assignment: a part that writes another part's table needs a RolesAssignment applied beside the App, such as
[`grants.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/grants.yaml), which version 1.1.0's hook
needs. The example also needs Python 3.12 or later as `python3`. `funcdctl dev` creates each Secret that the App
declares from the environment variable of each key's name, so the command sets `TODO_API_TOKEN`:

```console
$ TODO_API_TOKEN=dev-token funcdctl dev apps/todo/app --build apps/todo/functions/ts --build apps/todo/functions/py --build api=apps/todo/functions/ts/api-1.0.0.mjs --build migrate=apps/todo/functions/ts/migrate-1.0.0.mjs --build web=apps/todo/web --hooks --gport 8080 --cport 8081
```

Once the line of `todo-warm` ending with `warm install todo-1` shows, list the todos in another terminal:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/api
{"version":"1.0.0","todos":[],"migration":{"by":"todo-migrate 1.0.0","event":"install","app":"todo","to":"todo-1","toVersion":"1.0.0","migrated":0},"release":{"by":"todo-warm","event":"install","revision":"todo-1","version":"1.0.0"},"greeting":"hello from the todo App","tokenSet":true}
```

The revision records each hook call, with its Invocation; repeat until both are `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get apprevision todo-1 -n default -o json
```

Keep the example running: [Changes and deletion](#changes-and-deletion) deletes it.

## Carry an add-on

A template may carry the [add-on](../../services/addons/index.md) whose objects its App holds: `addon.yaml` beside
`app.yaml` holds one Addon, with no namespace, and the App's `addons` section declares the objects of its kind, as `kv`
declares a KV store: each entry has a `name`, the add-on's `kind` and the object's `config`, `bindings`, `ingress`,
`resources` and `deletion`, or a `ref` alone.

`funcdctl app deploy` applies the Addon before the App. An admin's deploy finds the Addon of the template's kind under
any name: it creates the template's when there is none (`created Addon/rauthy`), updates one whose spec differs under
its own name (`updated Addon/<name>`), and leaves one that agrees (`unchanged Addon/<name>`). A developer may not read
an Addon, so a developer's deploy only checks that the kind is served in the namespace; when it is not, it applies
nothing and exits 1: `Addon rauthy is not registered: an admin deploys this template first`. `funcdctl app render`
prints the App alone, and the Addon's `${{ }}` are its own, never the template's values. `funcdctl dev` applies
`addon.yaml` with its admin token before the App, again when the file changes, and deletes the Addon once the file
goes.

An `addons` part is admitted against its Addon: a kind no Addon registers, a config that breaks the Addon's schema, or
a Secret that the App's `secrets` do not declare is refused with 400 naming the entry. The part is Ready once its
engine is, `status.children` names it under its add-on's kind (`IdentityProvider/web-auth-login`), a revision that drops
it deletes it and its engine, and its storage follows its own `deletion`. Deleting the App deletes its parts; the
Addon stays until an admin deletes it.

## Limits and quotas

- An App's name, and the name of a ConfigMap that it defines, hold at most 52 characters.
- A new revision has [`app.upgradeTimeout`](../../running-funcd/spec.md#funcdconfigyaml), 5 minutes by default or
  twice `runtime.bootTimeout` if longer, from its start, a resume, a hold's release or its last pre-hook, to become
  current; past it, it turns `Failed`, unless a pre-hook still runs.
- A hook call fails when no answer starts within its Function's `timeout`, else `invoke.defaultTimeout`, 60 seconds by
  default. Waiting for required Apps has no time limit.
- funcd keeps the current revision and the newest `app.revisionHistory` others, 10 by default.

## Status

Read the phase and the `Ready` condition with `funcdctl get app <name> -n <namespace> -o json`. When `Ready` is
`False`, its message names the part, such as `KVStore/todo-store: exists and is not owned by App/todo`; when funcd
stops writing parts, `Ready` has the stop's reason in any phase.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| `Deploying` | `Progressing` | The latest revision is not current yet: it waits for the Apps that it requires, calls its pre-hooks or writes its parts. |
| `Ready` | none: `Ready` is `True` | The latest revision is current, and every part is ready. |
| `Ready` | `NotStarted` (`Ready` is `Unknown`) | The latest revision is current, and a Function part has not run since it changed. |
| `Degraded` | `ChildNotReady` | The latest revision is current, but a part is pending again; the message holds the part's own reason. |
| `Degraded` | `HookFailed` | A post-hook call failed. The message names the hook's Function and the Invocation that records the call. Run `funcdctl app retry`. |
| `Failed` | `HookFailed` | A pre-hook call failed the latest revision. Run `funcdctl app retry` once the hook can succeed. |
| `Failed` | `ChildNotReady` | A part was still pending when `app.upgradeTimeout` ran out. The revision stays `Failed`, and `Ready` stays `False`, even once its parts are ready: apply a new spec, such as a fix or a rollback. |
| `Ready` condition | `ChildNotOwned` | An object of a part's name exists, and the App does not own it, such as a store that a deleted App kept. funcd writes no part until the object is gone, or until the App names it with `ref`. |
| `Ready` condition | `ChildInvalid` | funcd refused the write of a part, and writes no part after it. The message holds the refusal. |
| `Ready` condition | `SecretNotFound` or `SecretKeyMissing` | A Secret that the App declares, or one of its keys, does not exist. funcd writes no part until it exists. |
| `Ready` condition | `RequirementNotMet` | An App that `spec.requires` names does not exist, is not `Ready`, or has a version out of the range. |
| `Ready` condition | `RefNotFound` | The object that a `ref` entry names does not exist. |
| `Paused` condition | `SpecPaused` | The App is paused, as [Changes and deletion](#changes-and-deletion) explains. |
| Another reason | | The message of the `Ready` condition gives the cause. |

`status.currentRevision` names the revision whose rollout last completed, and `status.latestRevision` the newest.
`status.children` lists each part, in write order, as `Ready`, `NotStarted`, `Pending` with a reason, or `Pruning`.
[`funcdctl app history`](../../tools/funcdctl/spec.md#funcdctl-app-history) lists the revisions: `Deploying` until
current, then `Ready`, or `Failed`. `funcdctl get apprevision <name> -n <namespace> -o json` prints a revision's
conditions:

| Condition | Status and reason | Meaning |
| --- | --- | --- |
| `Applied` | `True` | funcd has written each part, or found it unchanged. |
| `Applied` | `False`: `Progressing`, `HookFailed`, or the stop's reason, such as `ChildNotOwned` | A pre-hook has not succeeded or has failed, or funcd stopped writing the parts. |
| `ChildrenReady` | `True`, or `Unknown` with `NotStarted` | Every part is ready, or a Function part has not run. |
| `ChildrenReady` | `False`: `Progressing` or `RefNotFound`, then `ChildNotReady` at the deadline | A part is pending; the message names it. |
| `Current` | `True` | The revision is the App's current revision. |
| `Current` | `False`: `Progressing`, `Replaced` or `Superseded` | The revision rolls out, was current before, or was still rolling out when a newer revision was created. |
| `Current` | `False`: `ChildNotReady` or `HookFailed` | The revision failed at its deadline, or a pre-hook failed it. |

funcd updates `Applied` and `ChildrenReady` only while the revision is the newest and `Deploying`.

## Changes and deletion

A new revision rolls out in this order:

1. It waits for the Apps that `spec.requires` names, writing nothing.
2. It calls the pre-hooks.
3. It writes the parts in the order of `status.children`: ConfigMaps, KV stores, Buckets, SQL databases, Functions,
   Workflows, EventSources, Sensors, Routes, Sites, Lakes and BackupSchedules. An unchanged part stays as it is.
4. Once no part is pending, it becomes current, and funcd calls the post-hooks.
5. Then funcd deletes the dropped parts, `Pruning` until then: a dropped store whose `deletion` is `delete` goes with
   its data, and one with `retain`, the default, stays. A part still in use by a Workflow run or another object stays.

A failed revision leaves `currentRevision` on the one before, but the parts that it wrote keep the new spec. It stays
`Failed` even when its parts become ready later; apply a new spec, such as a fix or a rollback. A changed defined
ConfigMap gets a new name. funcd writes back a part that another writer changes or deletes, and
`status.lastSelfHeal` names the last one.

[`funcdctl app pause`](../../tools/funcdctl/spec.md#funcdctl-app-pause) stops revisions, writes and hooks until
[`funcdctl app resume`](../../tools/funcdctl/spec.md#funcdctl-app-resume); `deploy` and `rollback` keep the pause, but
`funcdctl apply` of an App manifest without `paused`, such as the [Spec](spec.md#app) tab's example, resumes it.

[`funcdctl app delete <app>`](../../tools/funcdctl/spec.md#funcdctl-app-delete) deletes the App and waits until its
parts, revisions and the objects that they own are gone:

- A KV store, Bucket or SQL database goes with its data when its `deletion` is `delete`, and stays when it is `retain`.
- A Lake always goes, but its files stay when its Bucket stays.
- A `local` volume whose `deletion` is `retain` stays with its files, without a printed line, until
  [`funcdctl volume delete`](../../tools/funcdctl/spec.md#funcdctl-volume-delete) or a Function of the same name takes
  it back with `ref`.
- Objects named with `ref`, Secrets and Backups stay.

Delete the todo App:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app delete todo
deleted Function/todo-api
deleted Function/todo-intake
deleted Function/todo-migrate
deleted Function/todo-planner
deleted Function/todo-warm
deleted Workflow/todo-plan
deleted EventSource/todo-plan-timer
deleted EventSource/todo-uploads
deleted Sensor/todo-plan-schedule
deleted Sensor/todo-upload-intake
deleted Route/todo-api
deleted Site/todo-web
deleted KVStore/todo-cache
deleted KVStore/todo-plans
deleted Bucket/todo-tmp
deleted Bucket/todo-web
deleted ConfigMap/todo-settings-7caf7f1644
deleted AppRevision/todo-1
deleted Revision/todo-api-1
deleted Revision/todo-intake-1
deleted Revision/todo-migrate-1
deleted Revision/todo-planner-1
deleted Revision/todo-warm-1
deleted Invocation/inv-1d12ef48d5d17449c59e
deleted Invocation/inv-688fa9ed92cbdc7b74a0
deleted Function/todo-plan-collect
deleted Function/todo-plan-rank
deleted Route/todo-web
deleted Revision/todo-plan-collect-1
deleted Revision/todo-plan-rank-2
kept KVStore/todo-store
kept Bucket/todo-files
```

The command waits with no time limit; an interrupt stops the wait, not the deletion. A kept store belongs to no App,
so an App of the same name that declares it by `name` stops with `ChildNotOwned`: declare it with `ref`, or delete it
first. The todo template does so under `when` in `app.yaml`, which selects `resources/kept-ref.yaml` when
[`values/reinstall.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/values/reinstall.yaml) sets
`keptStores`. Press Ctrl-C in the first terminal to stop.

## CLI errors

`funcdctl app deploy` and `funcdctl dev` render and check the App before they apply anything, so these errors apply
nothing. `funcdctl dev` also stops at its start, before it serves the App, on its runtime and setup errors:

| Error | Action |
| --- | --- |
| `values do not match valuesSchema`, followed by the value at fault | Give a value that the template's `valuesSchema` accepts, in a values file that `-f` names. |
| A message that names a file of the template that funcdctl cannot read | Fix `app.yaml` or that file of `resources`. |
| A message that names a file and an entry, such as `resources/files.yaml: functions[0]` | Fix that entry of the file. |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `App todo declares Secret todo-api-keys with key TODO_API_TOKEN; set it in the shell or under dev.env in funcdctl.config.yaml` | Set the key in the environment, or in a source that [Run it locally](#run-it-locally) lists. |
| `no build of images.<key> in the --build folders <dirs>; name it with --build <key>=<path>` | Write the build in one of those folders, or name it with `--build <key>=<path>`. When `funcdctl.config.yaml` names `watch` commands, `funcdctl dev` waits for the build instead of stopping. |
| `App todo requires App todo-shared ^1.0.0: funcdctl dev does not run an App that requires another yet` | Run the App on a funcd server with `funcdctl app deploy`. |

## Run it locally

`funcdctl dev` runs a template as `funcdctl app deploy` does, except:

- It pushes each image from its `--build` into a local OCI layout that replaces the `registry`, and ignores
  `app.lock`. A contract sits beside its build, such as `api.contract.yaml` beside `api.mjs`.
- It drops the hooks unless `--hooks` is set. Function parts keep their own `replicas`, so a broken build shows at the
  part's first call, with 503.
- It creates each declared Secret, unless an `--apply` file defines it, from the environment, then the `envFile` or
  `env` of [`funcdctl.config.yaml`](../../tools/funcdctl/spec.md#funcdctl-dev). `--apply` names resources, such as
  RolesAssignments, that it applies beside the App.
- It renders and applies again within about a second after an input changes, and prints `reloaded in <duration>`; a
  failed reload keeps the last App running.
- It keeps volumes as plain folders, only warning when one exceeds its size, and runs the namespace `default` only.

Without `--persist`, it loses the App, its revisions and its data when it stops; the pushed images stay in
`funcd/dev/<id>` in the user's cache folder. With `--persist`, it keeps everything in `.funcd-dev`, and a restart
with the same inputs creates no revision.

## Examples

- [volumes](https://github.com/pyvvo/funcd-functions/tree/main/apps/volumes): two Functions with volumes and Routes,
  installed with a values file.
- [todo](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): hooks, kept and deleted stores, a Workflow, a
  Site and three template versions.
