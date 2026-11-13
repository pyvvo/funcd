---
title: App
description: Install an App from its template, read its status and its parts, change a value, roll back, run its hooks, and delete it.
---

## Usage

An App declares a whole app in one resource: its Functions, its Routes, its stores and the other objects that make up
the app, which funcd writes as the App's **parts**. funcd installs, upgrades, rolls back and deletes the parts as one,
and reports each of them in the App's status. The [Platform page](../../../platform/workloads/app.md) explains how
funcd rolls an App out; this page shows how to install an App from its template, follow its revisions, roll it back and
delete it.

The first example, volumes, is an App of two Functions, each with a Route. The steps below install it on the local
computer from its template and a values file, call both Functions through their Routes, and read the App's status.
funcd rolls out each version of an App's spec as a **revision**, named after the App and a number that grows by one at
each change, so the first revision of this App is `volumes-1`. At the end, the App's phase is `Ready`, `volumes-1` is
its current revision, and each part is `Ready`.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Functions.
- Node.js on the `PATH`, which runs the example's Functions.

The example installs the App from an **App template**: a folder that holds an `app.yaml` file beside a `resources`
folder. `app.yaml` names the App, its version and the images of its code, and describes the **values** that the
template takes, with their defaults. Each file of `resources` holds sections of the App's spec. funcdctl renders the
template into an App on the local computer, with the values of the files that `-f` names: `funcdctl app deploy` then
applies the App to a funcd server, and `funcdctl dev` applies it to a local platform.

The example's folder, `apps/volumes` in the funcd-functions repository, holds the template in `app`, the code of its
Functions in `functions/ts`, and values files in `values`. The template's
[`app/app.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/app/app.yaml) names the App
`volumes`, its version `1.0.0` and its one image, `files`, and takes two values: `registry`, the registry that holds
the image, and `timeout`, whose default is `30s`. The values file
[`values/redeploy.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/values/redeploy.yaml) sets
`timeout` to `31s`.

The template's `resources/files.yaml` declares the two Functions. funcdctl replaces each `${{ … }}` expression when it
renders the template: `app.name` is the App's name, `volumes`; `images.files` is the reference of the image `files`;
and `values.timeout` is the value `timeout`, from the values file:

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/files.yaml]

- Both Functions run the image `files`, whose handler, in
  [`functions/ts/src/files/main.ts`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/functions/ts/src/files/main.ts),
  takes an operation in `op`, such as `write`, which stores a text in a file of a volume, `read`, which returns it, and
  `list`, which lists the volume's files.
- Each Function declares [`volumes`](spec.md#appspecfunctionsvolumes), folders that its workers mount. `data` is a
  `local` volume, which keeps its files through a crash of a worker, a new revision of the Function and a scale to
  zero; `scratch` is an `emptyDir` volume, which each new worker gets empty. A funcd server refuses a Function with a
  volume unless its `runtime.mode` is `containerd` and `volumes.capacity` or `volumes.capacityBytes` is set;
  `funcdctl dev` keeps each volume as a folder of the local computer.

The template's `resources/routes.yaml` declares a [Route](../../security-and-networking/networking/index.md) for each
Function, at the paths `/files` and `/peer`:

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/routes.yaml]

`funcdctl dev` runs an App template from the local builds of its images. `--build` names a folder in which
`funcdctl dev` looks up the build of each image by the image's key: here `functions/ts/files.mjs`, which the repository
holds already built, for the image `files`. `funcdctl dev` pushes each build into an OCI image layout, a folder that
holds images as a registry does, in its own state folder, and uses that folder in place of the template's `registry`.
It then renders the template with the values of the `-f` files, and applies the App. Its banner shows the address of
each Route, and the address of its control plane, which a `funcdctl` command reaches with `--server` and the token
`funcd-dev-token`. The `--gport` flag sets the port of the Routes' addresses, and `--cport` the port of the control
plane, so the commands below use the ports 8080 and 8081.

A rollout writes the parts, and once no part is still pending, the revision becomes the App's current revision. The
[Spec](spec.md) tab describes every field of an App and of an AppRevision, the object that records a revision.

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/volumes/app -f apps/volumes/values/redeploy.yaml --build apps/volumes/functions/ts --gport 8080 --cport 8081
```

`funcdctl dev` applies the App, prints its banner, and follows the rollout: it prints the revision's name, a line for
each part whose state changes, a line with the App's `Ready` condition, such as `App/volumes Ready=Unknown NotStarted`,
and `App volumes deployed in <duration>` once the revision `volumes-1` is current. The banner can come before or after
the first of these lines. Once the line `App volumes deployed in <duration>` shows, read the App in another terminal:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

The command prints the App as JSON. `spec` holds the App that the template rendered: under `functions`, the Function
`volumes-files` has `"timeout": "31s"`, from the values file. Under `status`, `"phase"` is `"Ready"` and
`"currentRevision"` is `"volumes-1"`. `"children"` lists each part with its state: the Routes are `"Ready"`, and the
Functions are `"NotStarted"`. A Function that sets neither `replicas` nor `scaling.minReplicas` runs no worker until
its first call, as [Scale a Function to zero](../function/index.md#scale-a-function-to-zero) explains, and its part is
`NotStarted` until then. A rollout does not wait for a `NotStarted` part, and while one remains, the App's `Ready`
condition has the status `"Unknown"` and the reason `NotStarted`. funcd does not start such a part to check it: a part
whose new build cannot load shows the fault only when a call starts the part. Calls to it then get the HTTP status 503,
and the App turns `Degraded`.

Call each Function through its Route: write a note with `volumes-files`, read it back, and list the files of
`volumes-peer`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"write","name":"note","text":"hello"}' http://127.0.0.1:8080/files
{}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"read","name":"note"}' http://127.0.0.1:8080/files
{"text":"hello"}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/peer
{"names":[]}
```

`volumes-peer` lists no file: each Function has a volume `data` of its own. Read the App again, and repeat the command
until each child is `"Ready"`: the `Ready` condition then has the status `"True"`.

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

Keep the example running: the next two sections change this App and roll it back, and the first of them reads the
note again. Pressing Ctrl-C in the terminal that runs `funcdctl dev` stops the example, and `funcdctl dev` then loses
the App and the note, as [Run it locally](#run-it-locally) explains.

## Change a value and roll out a new revision

Each change of an App's spec, other than a pause or a resume, makes a new revision, and an apply of the same spec makes
none. A new values file or a new version of the template changes the spec that the template renders. A new build of
an image changes it under these conditions:

- `funcdctl dev` pushes every build itself, and renders the template and applies the App again each time one of its
  inputs changes, which [Run it locally](#run-it-locally) lists.
- On a funcd server, [`funcdctl app deploy`](../../tools/funcdctl/spec.md#funcdctl-app-deploy) renders the template
  with a digest for each image: the digest that the template's `app.lock` file pins, or, for a template folder without
  `app.lock`, the digest that the image's tag names at that deploy. A template that `funcdctl push --template` pushed
  always holds such a lock, so a new build reaches its App through a new lock, which
  [`funcdctl app lock`](../../tools/funcdctl/spec.md#funcdctl-app-lock) writes, such as for a new tag of the image.
  For a template folder that holds `app.lock`, `funcdctl app deploy` only warns when a locked tag now names another
  digest.

While the first example runs, change `timeout` in `apps/volumes/values/redeploy.yaml` from `31s` to `45s`, and save
the file. `funcdctl dev` applies the new spec and follows the revision `volumes-2`: while the changed Function
`volumes-files` starts its new revision, its part is `Pending` with the reason `Progressing`, and the App's `Ready`
condition is `False` with the same reason. Once `volumes-2` is current, `funcdctl dev` prints
`reloaded in <duration>`. Then list the App's revisions:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
```

The times differ at each run. `VERSION` is the `version` of `app.yaml`, the same in both revisions. Only the spec of
`volumes-files` changed, so funcd started a new revision of that Function, `volumes-files-2`, and left `volumes-peer`
as it was; the Function page's [Changes and deletion](../function/index.md#changes-and-deletion) section explains how
a new revision of a Function takes over its calls. The note that the first example wrote is still in the volume
`data`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"read","name":"note"}' http://127.0.0.1:8080/files
{"text":"hello"}
```

Keep the example running for the next section.

## Roll back to an earlier revision

[`funcdctl app rollback <app> <number>`](../../tools/funcdctl/spec.md#funcdctl-app-rollback) applies the spec of an
earlier revision to the App again. funcd rolls it out as a new revision, with the next number, so the history keeps
every change. A rollback restores the spec, not the data: the stores and the volumes keep what they hold, and a store
whose `deletion` is `delete` and that a later revision dropped comes back empty. funcd keeps an App's current revision
and its 10 newest other revisions, a number that the server setting `app.revisionHistory` sets, and deletes the older
ones, so a rollback can name only a revision that funcd keeps.

With `--dry-run`, the command changes nothing and prints what the rollback would do: the revision that it would create,
then a line for each part that it would `create`, `update` or `prune`, then a `hook <name>` line for each hook call of
the new revision. When the rollout would stop at a part, such as at an object of another owner, that part's line is
the only part line, with the reason in parentheses. While the first example runs, check the rollback to revision 1,
then run it:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1 --dry-run
would apply App/volumes
  revision volumes-3
  update Function/volumes-files
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1
applied App volumes with the spec of volumes-1
```

The command returns once the App is applied, before the new revision is current. Read the App's revisions, and repeat
the command until revision 3 is `Ready`; while it rolls out, its phase is `Deploying`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
3          1.0.0     Ready   2026-11-13T17:02:51.403Z
```

Revision 3 holds the spec of revision 1, so `volumes-files` runs with the `timeout` `31s` again. `funcdctl dev` leaves
the App as the rollback set it until one of its inputs changes; it then applies the App that it renders, with the
`timeout` of the values file, as a new revision.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Run hooks on each new revision

A **hook** is a Function of the App that funcd calls for each new revision whose spec has hooks: a `preApply` hook
before the revision changes the other parts, such as to migrate a database, and a `postApply` hook once the revision
is current. funcd calls the hooks of each list in order, one at a time, with a CloudEvent whose type is the list's name
and whose data is the revision's **hook input**: the event, `install`, `upgrade` or `rollback`, the App's name, the
current revision and the new one, and their versions. [`spec.hooks`](spec.md#appspechooks) declares the hooks, and
[`hookInput`](spec.md#apprevisionspechookinput) describes the input.

- Until every pre-hook has succeeded, funcd writes only the pre-hook Functions and what they bind, and the current
  revision keeps serving. A failed pre-hook call fails the new revision, with the reason `HookFailed`.
- The post-hooks run once the revision is current, and the revision's deadline, which
  [Limits and quotas](#limits-and-quotas) gives, does not bound them. A failed post-hook call makes the App
  `Degraded`, with the reason `HookFailed`, and holds the later post-hooks and the deletion of the parts that the
  revision dropped.
- [`funcdctl app retry <app>`](../../tools/funcdctl/spec.md#funcdctl-app-retry) calls a failed hook again, and the
  rollout goes on once the call succeeds.
- Each call is recorded in the revision's [`status.hooks`](spec.md#apprevisionstatushooks), with the Invocation that
  holds its start, its end and its error. A call that ends during a stop of funcd, a pause of the App or a
  [hold](../../../platform/disaster-recovery.md), the state in which a restore leaves the platform or a namespace, is
  not recorded, and funcd makes it again. A hook must therefore be safe to call more than once.

`funcdctl dev` calls an App's hooks only with the `--hooks` flag. Without it, it drops the hooks from the App, so a
migration that a hook runs does not run.

The todo example is the App of a to-do list, with a TypeScript API, Python Functions, a Workflow, a Site and stores.
Its template's `resources/hooks.yaml` declares the Function `todo-warm` and the two hooks: the pre-hook
`todo-migrate`, a Function that another file of the template declares, and the post-hook `todo-warm`:

::include[../../../../../_repos/funcd-functions/apps/todo/app/resources/hooks.yaml]

Each hook records its call in a table of a KV store that it owns: `todo-migrate` under the key `last` of the table
`meta`, and `todo-warm` under the key `last` of the table `warm`. The `list` operation of the Function `todo-api`,
which the Route `/api` serves, returns both records, as `migration` and `release`.

An App declares no role assignment. A part that writes a table that another part owns needs a role that allows it,
from a RolesAssignment applied beside the App, as the KV page's [Access](../../services/kv/index.md#access) section
explains. The todo example's
[`grants.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/grants.yaml), a file beside its
template, gives `todo-migrate` the
right to write the table `todos` of the store `todo-store`, which `todo-api` owns and the hook of version 1.1.0
rewrites. The hooks of version 1.0.0 write only their own tables, so the steps below need no role.

The example needs the programs of the first example, and Python 3.12 or later as `python3` on the `PATH`, which runs
its Python Functions, such as `todo-warm`. The Python Functions of its version 1.0.0 import only Python's standard
library and funcd's shim, which `funcdctl dev` provides, so the example installs no package. The command names the
builds of the template's images with `--build`:

- `apps/todo/functions/ts` holds the TypeScript builds, such as `collect.mjs` for the image `collect`.
- `apps/todo/functions/py` holds the Python Functions. Its `pyproject.toml` file names the entry of each one under
  `[tool.funcd-bundle.functions]`, such as `src/warm/main.py` for the image `warm`.
- The builds of the images `api` and `migrate` carry their version in their names, so `--build api=…` and
  `--build migrate=…` name them.
- `--build web=apps/todo/web` names the folder of the files that the App's Site serves.

The App declares the Secret `todo-api-keys`, with the key `TODO_API_TOKEN`, which `todo-api` reads to check the token
of a call that adds a todo. `funcdctl dev` creates each Secret that the App declares, and takes the value of each key
from the environment variable of the key's name, so the command sets `TODO_API_TOKEN`;
[Run it locally](#run-it-locally) gives the other sources of a key.

From the root folder of the funcd-functions repository, cloned as in the first example, start the todo example with
its hooks:

```console
$ TODO_API_TOKEN=dev-token funcdctl dev apps/todo/app --build apps/todo/functions/ts --build apps/todo/functions/py --build api=apps/todo/functions/ts/api-1.0.0.mjs --build migrate=apps/todo/functions/ts/migrate-1.0.0.mjs --build web=apps/todo/web --hooks --gport 8080 --cport 8081
```

`funcdctl dev` prints `App todo deployed in <duration>` once the revision `todo-1` is current, and funcd calls the
post-hook after that: the line of `todo-warm` that ends with `warm install todo-1` shows that the call has run. Once
that line shows, list the todos in another terminal:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/api
{"version":"1.0.0","todos":[],"migration":{"by":"todo-migrate 1.0.0","event":"install","app":"todo","to":"todo-1","toVersion":"1.0.0","migrated":0},"release":{"by":"todo-warm","event":"install","revision":"todo-1","version":"1.0.0"},"greeting":"hello from the todo App","tokenSet":true}
```

`migration` and `release` hold what the two hooks received: the event `install` and the revision `todo-1`.
`greeting` is the default of the template's value `greeting`, which a ConfigMap of the App passes to `todo-api`. Read
the revision's record of the calls, and repeat the command until both calls show the phase `"Ready"`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get apprevision todo-1 -n default -o json
```

The command prints the revision as JSON. Its `spec` holds the App's spec when the revision was created, and
`spec.hookInput` the input of its hook calls. Under `status`, `"hooks"` lists the two calls, `"preApply"` to
`todo-migrate` and `"postApply"` to `todo-warm`, each with the phase `"Ready"` and the name of the Invocation that
records it. Each later revision calls the hooks again, with the event `upgrade`, or `rollback` for a revision whose
spec is that of an earlier revision that was current.

Keep the example running: [Changes and deletion](#changes-and-deletion) deletes this App.

## Limits and quotas

- An App's name holds at most 52 characters, so that each revision's name, `<app>-<number>`, stays a valid name. A
  ConfigMap that an App defines has a name of at most 52 characters too: funcd stores it under that name, followed by
  a dash and 10 hexadecimal characters computed from its data.
- A new revision has `app.upgradeTimeout` to make every part ready and become current: 5 minutes by default, or twice
  `runtime.bootTimeout` when that is longer. The time counts from the latest of the revision's start, a resume of the
  App, the release of a hold and the end of its last pre-hook call. A revision that runs out turns `Failed`, unless a
  pre-hook call is still running.
- A hook call fails when no answer starts within the hook Function's `timeout`, else `invoke.defaultTimeout`, 60
  seconds by default, a cold start included.
- A revision that waits for the Apps that it requires waits with no time limit.
- funcd keeps an App's current revision and its newest other revisions, 10 by default; `app.revisionHistory` sets that
  number, from 1 to 100.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes each server setting above, and this
page's [Spec](spec.md) tab gives the range of each field.

## Status

`funcdctl get app <name> -n <namespace> -o json` prints an App's status, as the first example shows. Its phase is one
of these:

- `Deploying`: the latest revision is not current yet: it waits for the Apps that it requires, calls its pre-hooks or
  writes its parts.
- `Ready`: the latest revision is current, and no part is pending.
- `Degraded`: the latest revision is current, but a part is pending again, a post-hook failed, or funcd stopped
  writing the parts, such as for a missing Secret.
- `Failed`: the latest revision failed: a pre-hook failed, or a part was still pending when `app.upgradeTimeout` ran
  out.

The condition `Ready` explains the phase. It is `False` while funcd has stopped writing the parts, a part is pending,
a pre-hook has not succeeded or a post-hook has failed, and while the latest revision is `Failed`, even once its parts
are ready. Otherwise, it is `Unknown`, with the reason `NotStarted`, while a Function part has not run since it
changed, and `True` once every part is ready. When it is `False`, its reason says why, with a message that names the
part, such as `KVStore/todo-store: exists and is not owned by App/todo`:

- `Progressing`: a rollout writes the parts, or waits for a part or a pre-hook.
- `ChildNotReady`: a part is not ready; the message holds the part's own reason.
- `ChildNotOwned`: an object of a part's name exists, and the App does not own it, such as a store that a deleted App
  kept. funcd writes no part until the object is gone, or until the App names it with `ref` instead.
- `ChildInvalid`: funcd refused the write of a part. It writes no part after that one, and the message holds the
  refusal.
- `SecretNotFound` or `SecretKeyMissing`: a Secret that the App declares, or one of its keys, does not exist. funcd
  writes no part until it exists.
- `RequirementNotMet`: an App that `spec.requires` names does not exist, is not `Ready`, or has a version out of the
  range.
- `HookFailed`: a hook call failed. The message names the hook's Function and the Invocation that records the call.
- `RefNotFound`: the object that a `ref` entry names does not exist.

The condition `Paused` is `True`, with the reason `SpecPaused`, while the App is paused, as
[Changes and deletion](#changes-and-deletion) explains.

`status.currentRevision` names the revision whose rollout last completed, and `status.latestRevision` the newest one;
they differ while a rollout runs and after one fails. `status.children` holds a line for each part, in the order in
which funcd writes them, with its state: `Ready`; `NotStarted` for a Function that has not run since it changed;
`Pending`, with a reason, for a part that is missing or not ready; and `Pruning` for a part that the spec dropped and
that funcd has not deleted yet.

[`funcdctl app history <app>`](../../tools/funcdctl/spec.md#funcdctl-app-history) lists an App's revisions by number,
with the `version` of each, its phase and the time of its creation. A revision is `Deploying` until it becomes
current, `Ready` once it is or has been current, and `Failed` when it did not become current.
`funcdctl get apprevision <name> -n <namespace> -o json` prints a revision, whose three conditions explain its phase.
`Applied` and `ChildrenReady` record the rollout: funcd updates them only while the revision is the newest one and its
phase is `Deploying`, and after that only `Current` changes. `todo-1` of the hooks example therefore keeps
`ChildrenReady` at `Unknown`, with the reason `NotStarted`, even after calls start its parts.

- `Applied` is `True` once funcd has written each part of the revision, or found it unchanged. It is `False` with the
  reason `Progressing` while a pre-hook has not succeeded, with `HookFailed` when a pre-hook failed, and otherwise
  with the reason and the message of the stop, such as `ChildNotOwned`.
- `ChildrenReady` is `True` when every part is ready, and `Unknown`, with the reason `NotStarted`, while a Function
  part has not run. It is `False` with `Progressing`, or `RefNotFound`, while a part is pending, and with
  `ChildNotReady` once the revision has failed at its deadline; the message names the part.
- `Current` is `True` on the App's current revision. Otherwise it is `False`: with the reason `Progressing` while the
  revision rolls out; `Replaced` on a revision that was current before; `Superseded` on a revision that was still
  rolling out when a newer revision was created; `ChildNotReady` on a revision that failed at its deadline; and
  `HookFailed` on a revision that a pre-hook failed.

## Changes and deletion

An apply of a spec that differs from the latest revision's spec, other than in `paused`, creates a new revision, and
funcd rolls it out in this order:

1. It waits for the Apps that `spec.requires` names, with no time limit, and writes nothing meanwhile.
2. It calls the pre-hooks, as [Run hooks on each new revision](#run-hooks-on-each-new-revision) explains.
3. It writes the parts in the order of `status.children`: the ConfigMaps first, then the KV stores, the Buckets, the
   SQL databases, the Functions, the Workflows, the EventSources, the Sensors, the Routes, the Sites, the Lakes and the
   BackupSchedules. A part whose spec did not change stays as it is, and a changed Function starts a new revision
   beside the one that serves.
4. Once no part is pending, the revision becomes current, and funcd calls the post-hooks.
5. Once the post-hooks have succeeded, funcd deletes the parts that the new spec dropped, which are `Pruning` until
   then. A dropped KV store, Bucket or SQL database whose `deletion` is `delete` goes with its data; one whose
   `deletion` is `retain`, the default, stays, with its data. A dropped part also stays while a run of a Workflow holds
   it or another object uses it.

When a revision fails, at the end of `app.upgradeTimeout` or on a failed pre-hook, `status.currentRevision` keeps
naming the revision before it, but the parts that the rollout wrote keep the new spec: a Route, a Site, a Sensor or an
EventSource follows it at once, and a changed Function switches to its new revision whenever that revision becomes
ready. A revision that failed at its deadline stays `Failed`, even when its parts become ready later, and an apply of
the same spec creates no revision: a new spec, such as a fix or a rollback, starts a new one. A revision that a
pre-hook failed goes on once `funcdctl app retry` has called the hook again and the call has succeeded.

A change to the data of a ConfigMap that the App defines writes a new ConfigMap, under a new name, points each part
that names it at the new one, and deletes the old one with the other dropped parts. When another writer changes or
deletes a part, funcd writes the part back as soon as it sees the change, and `status.lastSelfHeal` names the last
part written back.

[`funcdctl app pause <app>`](../../tools/funcdctl/spec.md#funcdctl-app-pause) sets the App's `spec.paused`: funcd then
creates no revision, writes or deletes no part and calls no hook, until
[`funcdctl app resume <app>`](../../tools/funcdctl/spec.md#funcdctl-app-resume) clears it. `funcdctl app deploy` and
`funcdctl app rollback` keep the pause. An App is also an ordinary resource, so `funcdctl apply` applies an App
manifest with no template, such as the example of the [Spec](spec.md#app) tab; such an apply of a manifest without
`paused` resumes the App.

[`funcdctl app delete <app>`](../../tools/funcdctl/spec.md#funcdctl-app-delete) deletes an App, waits until funcd has
deleted its parts, its revisions and the objects that they own, and prints a line for each object that went and for
each store that stayed:

- A KV store, Bucket or SQL database whose `deletion` is `delete` goes with its data. One whose `deletion` is
  `retain` stays, with its data.
- A Lake of the App always goes, but its files stay in its Bucket when that Bucket stays.
- A `local` volume of a Function whose `deletion` is `retain`, the default, stays with its files and no owner, until
  [`funcdctl volume delete`](../../tools/funcdctl/spec.md#funcdctl-volume-delete) deletes it or a Function of the same
  name takes it back with `ref`. `funcdctl app delete` prints no line for such a volume, such as each volume `data` of
  the first example.
- An object that the App names with `ref`, a Secret and a Backup stay.

While the todo example of [Run hooks on each new revision](#run-hooks-on-each-new-revision) runs, delete the App:

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

The names of the Invocations differ at each run. The template's `resources/kept.yaml` declares the store `todo-store`
and the Bucket `todo-files` with `deletion: retain`, so both stay, with their data. The stores whose `deletion` is
`delete`, such as `todo-cache`, went with their data. The command waits with no time limit, such as while another
object still uses a store that it deletes; an interrupt stops the wait, not the deletion.

A kept store belongs to no App. An App of the same name that declares it by `name` stops at it: its `Ready` condition
is `False`, with the reason `ChildNotOwned` and the message `KVStore/todo-store: exists and is not owned by App/todo`.
`funcdctl dev` shows it at the next change of one of its inputs, when it applies the App again. To take a kept store
back, declare it with `ref`, or delete the store first. Under `when`, `app.yaml` can make a file of `resources` depend
on a condition over the values: the todo template declares its kept stores by `name` in `resources/kept.yaml` and by
`ref` in `resources/kept-ref.yaml`, and the values file
[`values/reinstall.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/values/reinstall.yaml) sets
the value `keptStores` that selects the second file.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Errors

- When `funcdctl app deploy` or `funcdctl dev` renders a template, it checks the App before it applies anything, and a
  refusal applies nothing. A value that the template's `valuesSchema` refuses gives a message that contains
  `values do not match valuesSchema`, followed by the value at fault. An `app.yaml` or a file of `resources` that
  funcdctl cannot read gives a message that names the file. An entry that breaks a rule gives a message that names the
  file and the entry, such as `resources/files.yaml: functions[0]`.
- The server refuses an App, and stores nothing, when its spec breaks a rule, with a message that names the entry,
  such as `spec.functions[0]` for an App manifest that `funcdctl apply` sends. It also refuses a part that names a
  Secret that `spec.secrets` does not declare, with a message that names the entry, such as
  `spec.functions[0].bindings.secret[0]`.
- A rollout that funcd cannot complete leaves the App `Deploying`, `Degraded` or `Failed`, and the reason and the
  message of its `Ready` condition name the part, as [Status](#status) lists.
- `funcdctl app rollback` refuses a revision that funcd does not keep, with a message such as
  `AppRevision volumes-9 does not exist: it was never stamped, or app.revisionHistory pruned it`. For a revision whose
  spec the App has already, it prints a line such as `no change: App volumes already has the spec of volumes-3` and
  changes nothing.
- `funcdctl app retry` refuses an App that has no failed hook, with the HTTP status 409 and the message
  `app <app> has no failed hook`.
- `funcdctl app deploy` exits with the status 1 when the new revision fails, with a message that names the revision
  and its reason.

## Run it locally

`funcdctl dev` runs an App template on a local platform as `funcdctl app deploy` runs it on a funcd server, with these
differences:

- It takes each image from a local build, which `--build` names, and pushes it into an OCI image layout in its state
  folder, which replaces the template's `registry`. It neither reads nor writes the template's `app.lock`.
- It changes nothing else in the App but its hooks, which it drops unless `--hooks` is set. Unlike a folder of
  resource files, whose Functions `funcdctl dev` keeps running, an App's Function parts keep their own `replicas`, so a
  part that sets none runs no worker until its first call. A save of a build that cannot load therefore still prints
  `reloaded in <duration>` when its part has not run: the fault shows at the part's first call, as the HTTP status
  503 with a message such as `function default/<part> is Failed (ShapeInvalid)`.
- It creates each Secret that the App declares, unless a file that `--apply` names defines it, with each key from the
  environment variable of the key's name, else from the `envFile` or the `env` of its `funcdctl.config.yaml` file.
  When no source holds a key, it stops with a message such as
  `App todo declares Secret todo-api-keys with key TODO_API_TOKEN; set it in the shell or under dev.env in funcdctl.config.yaml`.
- `--apply` names a file of resource documents that it applies before the App, again when the file changes, and
  deletes once they leave the file, such as the RolesAssignments that an App needs beside it.
- It renders and applies the template again within about a second after one of its inputs changes: a file of the
  template folder, a values file, an `--apply` file, a `--build` folder or path, or the `build`, `values`, `apply` or
  `env` keys of its `funcdctl.config.yaml` file and the file that its `envFile` names. It prints
  `reloaded in <duration>` once the new revision is current. A reload that fails is printed, and the last App keeps
  running. For a revision that does not become current, it prints
  `the new revision did not become current; save a fix, or run funcdctl app retry` with the reason, and keeps running.
- It refuses an App that requires another App, with a message such as
  `App todo requires App todo-shared ^1.0.0: funcdctl dev does not run an App that requires another yet`, and an image
  that no `--build` holds, with a message that names the image.
- It keeps each volume of a Function as a plain folder and does not limit its size: it only warns when a volume holds
  more than its size.
- It runs the namespace `default` only.

Without the `--persist` flag, `funcdctl dev` keeps the App, its revisions and the data of its stores in memory, and its
volumes in a temporary folder, and loses them when it stops. It keeps the images that it pushed, and a record of the
processes that it starts, in a folder of its own, `funcd/dev/<id>` in the user's cache folder, such as `~/.cache` on
Linux or `~/Library/Caches` on macOS, where `<id>` stands for the template folder. That folder holds no data of the
platform, and `funcdctl dev` reuses it at its next run, so nothing needs to be deleted there. With `--persist`, it
keeps all of its state between runs in `.funcd-dev`, a folder that it creates in the current folder, or in the folder
that `--persist-to` names: the objects that it applied, the App's revisions included, the data of the stores, the
volumes and the images that it pushed. A restart with the same inputs creates no revision. To start again from
nothing, delete that folder after `funcdctl dev` stops. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags and the keys of
`funcdctl.config.yaml`.

## Examples

- [volumes](https://github.com/pyvvo/funcd-functions/tree/main/apps/volumes): an App of two Functions, each with a
  volume and a Route, installed from its template with a values file.
- [todo](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): the App of a to-do list, with its hooks, the
  stores that it keeps or deletes with the App, a Workflow, a Site and three versions of its template.
