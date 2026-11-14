---
title: App
description: Install an App from its template, read its status and its parts, change a value, roll back, run its hooks, and delete it.
---

## Usage

An App declares a whole app in one resource: its Functions, its Routes, its stores and its other objects, which funcd
writes as the App's **parts**. funcd installs, upgrades, rolls back and deletes the parts as one, and reports each part
in the App's status. See [how funcd rolls an App out](../../../platform/workloads/app.md) for the concept.

The volumes example, an App of two Functions with a Route each, installs from an **App template**: a folder that
holds `app.yaml`, which names the App, its version, its images and the **values** that the template takes, beside a
`resources` folder, whose files hold sections of the App's spec. funcdctl renders the template into an App with the
values of the files that `-f` names:
[`funcdctl app deploy`](../../tools/funcdctl/spec.md#funcdctl-app-deploy) applies it to a funcd server, and
`funcdctl dev` to a local platform.

Install the [dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`.

`--build` names a folder in which `funcdctl dev` looks up each image's build by the image's key, here the prebuilt
`functions/ts/files.mjs` for the image `files`; [Run it locally](#run-it-locally) says where it puts the builds. `--gport` sets the port of the Routes' addresses,
and `--cport` the port of the control plane, which a `funcdctl` command reaches with `--server` and the token
`funcd-dev-token`.

Clone the repository and start the example from its root folder:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/volumes/app -f apps/volumes/values/redeploy.yaml --build apps/volumes/functions/ts --gport 8080 --cport 8081
```

[`app/app.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/app/app.yaml) names the App
`volumes`, its version `1.0.0` and its image `files`, and takes two values: `registry`, and `timeout`, whose default
is `30s`. [`values/redeploy.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/values/redeploy.yaml)
sets `timeout` to `31s`. `resources/files.yaml` declares the two Functions; funcdctl replaces each `${{ … }}`
expression when it renders the template, with the App's name, the reference of the image `files` and the value
`timeout`:

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/files.yaml]

Both Functions run the image `files`, whose handler,
[`functions/ts/src/files/main.ts`](https://github.com/pyvvo/funcd-functions/blob/main/apps/volumes/functions/ts/src/files/main.ts),
takes an operation in `op`: `write` stores a text in a file of a volume, `read` returns it, and `list` lists the
volume's files. Each Function declares [`volumes`](spec.md#appspecfunctionsvolumes), folders that its workers mount:
`data` is a `local` volume, which keeps its files through a crash of a worker, a new revision of the Function and a
scale to zero, and `scratch` is an `emptyDir` volume, which each new worker gets empty. A funcd server refuses a
Function with a volume unless its `runtime.mode` is `containerd` and `volumes.capacity` or `volumes.capacityBytes` is
set; `funcdctl dev` keeps each volume as a folder of the local computer.

`resources/routes.yaml` declares a [Route](../../security-and-networking/networking/index.md) for each Function, at
`/files` and `/peer`:

::include[../../../../../_repos/funcd-functions/apps/volumes/app/resources/routes.yaml]

funcd rolls out each version of an App's spec as a **revision**, named after the App and a number that grows by one
at each change, so the first one is `volumes-1`. A rollout writes the parts, and once no part is still pending, the
revision becomes the App's current revision. `funcdctl dev` prints its banner, the revision's name, a line for each
part whose state changes and for the App's `Ready` condition, such as `App/volumes Ready=Unknown NotStarted`, and
`App volumes deployed in <duration>` once `volumes-1` is current; the banner can come before or after the first of
these lines. Once that last line shows, read the App in another terminal:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

`spec` holds the rendered App, in which `volumes-files` has `"timeout": "31s"`. Under `status`, `"phase"` is
`"Ready"`, `"currentRevision"` is `"volumes-1"`, and `"children"` lists the Routes as `"Ready"` and the Functions as
`"NotStarted"`. A Function that sets neither `replicas` nor
`scaling.minReplicas` runs no worker until its first call, as
[Scale a Function to zero](../function/index.md#scale-a-function-to-zero) explains, and its part is `NotStarted` until
then. A rollout does not wait for a `NotStarted` part, and funcd does not start it to check it: a part whose new build
cannot load shows the fault only when a call starts it, and the App then turns `Degraded`.

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

`volumes-peer` lists no file, because each Function has a volume `data` of its own. Read the App again, and repeat the
command until each child is `"Ready"`; the `Ready` condition then has the status `"True"`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get app volumes -n default -o json
```

Keep the example running for the next two sections. Ctrl-C in the first terminal stops it, and `funcdctl dev` then
loses the App and the note, as [Run it locally](#run-it-locally) explains. The [Spec](spec.md) tab describes every
field of an App and of an AppRevision, the object that records a revision.

## Change a value and roll out a new revision

Each change of an App's spec, other than a pause or a resume, makes a new revision; an apply of the same spec makes
none. A values file or a template version changes the rendered spec. A new build of an image changes it as follows:

- `funcdctl dev` pushes every build itself, and renders and applies the App again each time one of its inputs
  changes, which [Run it locally](#run-it-locally) lists.
- On a funcd server, `funcdctl app deploy` renders the template with a digest for each image: the digest that the
  template's `app.lock` file pins, or, for a template folder without `app.lock`, the digest that the image's tag names
  at that deploy. A template that `funcdctl push --template` pushed always holds a lock, so a new build reaches its App
  through a new lock, which [`funcdctl app lock`](../../tools/funcdctl/spec.md#funcdctl-app-lock) writes, such as for
  a new tag. For a template folder that holds `app.lock`, `funcdctl app deploy` only warns when a locked tag now names
  another digest. `funcdctl app deploy` exits with the status 1 when the new revision fails, with a message that names
  the revision and its reason.

While the first example runs, change `timeout` in `apps/volumes/values/redeploy.yaml` from `31s` to `45s`, and save
the file. `funcdctl dev` applies the new spec and follows the revision `volumes-2`: while `volumes-files` starts its
new revision, its part is `Pending` with the reason `Progressing`, and the App's `Ready` condition is `False` with
the same reason.
Once `volumes-2` is current, `funcdctl dev` prints `reloaded in <duration>`. List the App's revisions:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
```

The times differ at each run. `VERSION` is the `version` of `app.yaml`. Only the spec of `volumes-files` changed, so
funcd started a new revision of that Function, `volumes-files-2`, and left `volumes-peer` as it was; the Function
page's [Changes and deletion](../function/index.md#changes-and-deletion) explains how a new revision of a Function
takes over its calls. The note is still in the volume `data`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"read","name":"note"}' http://127.0.0.1:8080/files
{"text":"hello"}
```

Keep the example running for the next section.

## Roll back to an earlier revision

[`funcdctl app rollback <app> <number>`](../../tools/funcdctl/spec.md#funcdctl-app-rollback) applies the spec of an
earlier revision again, as a new revision with the next number, so the history keeps every change. A rollback
restores the spec, not the data: the stores and the volumes keep what they hold, and a store whose `deletion` is
`delete` and that a later revision dropped comes back empty. A rollback can name only a revision that funcd keeps:
the current one and the 10 newest others, a number that the server setting `app.revisionHistory` sets. For a revision
whose spec the App already has, the command prints a line such as
`no change: App volumes already has the spec of volumes-3` and changes nothing.

With `--dry-run`, the command changes nothing and prints what the rollback would do: the revision that it would
create, a line for each part that it would `create`, `update` or `prune`, and a `hook <name>` line for each hook call.
When the rollout would stop at a part, such as at an object of another owner, that part's line is the only part line,
with the reason in parentheses. While the first example runs, check the rollback to revision 1, then run it:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1 --dry-run
would apply App/volumes
  revision volumes-3
  update Function/volumes-files
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app rollback volumes 1
applied App volumes with the spec of volumes-1
```

The command returns once the App is applied, before the new revision is current. Repeat this command until revision
3 is `Ready`; while it rolls out, its phase is `Deploying`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token app history volumes
REVISION   VERSION   PHASE   STAMPED
1          1.0.0     Ready   2026-11-13T17:02:49.697Z
2          1.0.0     Ready   2026-11-13T17:02:50.912Z
3          1.0.0     Ready   2026-11-13T17:02:51.403Z
```

Revision 3 holds the spec of revision 1, so `volumes-files` runs with the `timeout` `31s` again. `funcdctl dev` keeps
the App as the rollback set it until one of its inputs changes; it then applies the App that it renders, with the
`timeout` of the values file, as a new revision.

Press Ctrl-C in the first terminal to stop the example.

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
  rollout goes on once the call succeeds. An App with no failed hook has nothing to retry, and the command fails.
- The revision's [`status.hooks`](spec.md#apprevisionstatushooks) records each call, with the Invocation that holds
  its start, its end and its error. A call that ends during a stop of funcd, a pause of the App or a
  [hold](../../../platform/disaster-recovery.md), the state in which a restore leaves the platform or a namespace, is
  not recorded, and funcd makes it again. A hook must therefore be safe to call more than once.

`funcdctl dev` calls an App's hooks only with the `--hooks` flag. Without it, it drops the hooks from the App, so a
migration that a hook runs does not run.

The todo example is the App of a to-do list, with a TypeScript API, Python Functions, a Workflow, a Site and stores.
Its template's `resources/hooks.yaml` declares the Function `todo-warm`, the pre-hook `todo-migrate`, a Function that
another file of the template declares, and the post-hook `todo-warm`:

::include[../../../../../_repos/funcd-functions/apps/todo/app/resources/hooks.yaml]

Each hook records its call under the key `last` of a KV table that it owns: `todo-migrate` in the table `meta`, and
`todo-warm` in the table `warm`. The `list` operation of the Function `todo-api`, which the Route `/api` serves,
returns both records, as `migration` and `release`.

An App declares no role assignment: a part that writes a table that another part owns needs a role from a
RolesAssignment applied beside the App, as the KV page's [Access](../../services/kv/index.md#access) section
explains. The todo example's [`grants.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/grants.yaml)
lets `todo-migrate` write the table `todos` of `todo-store`, which `todo-api` owns and the hook of version 1.1.0
rewrites. The hooks of version 1.0.0 write only their own tables, so the steps below need no role.

The example also needs Python 3.12 or later as `python3` on the `PATH`, which runs its Python Functions, such as
`todo-warm`. They import only Python's standard library and funcd's shim, which `funcdctl dev` provides, so the
example installs no package. The command's `--build` flags name the TypeScript builds in `apps/todo/functions/ts`,
such as `collect.mjs` for the image `collect`; the Python Functions in `apps/todo/functions/py`, whose
`pyproject.toml` names each entry under `[tool.funcd-bundle.functions]`, such as `src/warm/main.py` for the image
`warm`; the builds of `api` and `migrate`, whose names carry their version, as `api=…` and `migrate=…`; and the
Site's files, as `web=apps/todo/web`.

The App declares the Secret `todo-api-keys`, with the key `TODO_API_TOKEN`, which `todo-api` reads to check the token
of a call that adds a todo. `funcdctl dev` creates each Secret that the App declares, with the value of each key from
the environment variable of the key's name, so the command sets `TODO_API_TOKEN`; [Run it locally](#run-it-locally)
gives the other sources of a key.

From the root folder of the funcd-functions repository, start the todo example with its hooks:

```console
$ TODO_API_TOKEN=dev-token funcdctl dev apps/todo/app --build apps/todo/functions/ts --build apps/todo/functions/py --build api=apps/todo/functions/ts/api-1.0.0.mjs --build migrate=apps/todo/functions/ts/migrate-1.0.0.mjs --build web=apps/todo/web --hooks --gport 8080 --cport 8081
```

`funcdctl dev` prints `App todo deployed in <duration>` once `todo-1` is current, and funcd calls the post-hook after
that: the line of `todo-warm` that ends with `warm install todo-1` shows that the call has run. Once that line shows,
list the todos in another terminal:

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

Its `spec` holds the App's spec when the revision was created, and `spec.hookInput` the input of its hook calls.
Under `status`, `"hooks"` lists the call `"preApply"` to `todo-migrate` and the call `"postApply"` to `todo-warm`,
each with the phase `"Ready"` and the name of the Invocation that records it. Each later revision calls the hooks
again, with the event `upgrade`, or `rollback` for a revision whose spec is that of an earlier revision that was
current.

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

Read the App's phase and its `Ready` condition with `funcdctl get app <name> -n <namespace> -o json`, as the first
example does. When `Ready` is `False`, its message names the part, such as
`KVStore/todo-store: exists and is not owned by App/todo`. Each phase row gives a reason that `Ready` has in that phase.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| `Deploying` | `Progressing` | The latest revision is not current yet: it waits for the Apps that it requires, calls its pre-hooks or writes its parts. |
| `Ready` | none: `Ready` is `True` | The latest revision is current, and every part is ready. |
| `Ready` | `NotStarted` (`Ready` is `Unknown`) | The latest revision is current, and a Function part has not run since it changed. |
| `Degraded` | `ChildNotReady` | The latest revision is current, but a part is pending again; the message holds the part's own reason. funcd also reports `Degraded` when it stopped writing the parts, with the stop's reason, such as `SecretNotFound`. |
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

`status.currentRevision` names the revision whose rollout last completed, and `status.latestRevision` the newest one;
they differ while a rollout runs and after one fails. `status.children` holds a line for each part, in the order in
which funcd writes them: `Ready`; `NotStarted` for a Function that has not run since it changed; `Pending`, with a
reason, for a part that is missing or not ready; and `Pruning` for a part that the spec dropped and that funcd has not
deleted yet.

[`funcdctl app history <app>`](../../tools/funcdctl/spec.md#funcdctl-app-history) lists the revisions with their
`version`, phase and creation time. A revision is `Deploying` until it becomes current, `Ready` once it is or has been
current, and `Failed` when it did not become current. `funcdctl get apprevision <name> -n <namespace> -o json` prints
a revision and its three conditions:

| Condition | Status and reason | Meaning |
| --- | --- | --- |
| `Applied` | `True` | funcd has written each part, or found it unchanged. |
| `Applied` | `False`: `Progressing`, `HookFailed`, or the stop's reason, such as `ChildNotOwned` | A pre-hook has not succeeded or has failed, or funcd stopped writing the parts. |
| `ChildrenReady` | `True`, or `Unknown` with `NotStarted` | Every part is ready, or a Function part has not run. |
| `ChildrenReady` | `False`: `Progressing` or `RefNotFound`, then `ChildNotReady` at the deadline | A part is pending; the message names it. |
| `Current` | `True` | The revision is the App's current revision. |
| `Current` | `False`: `Progressing`, `Replaced` or `Superseded` | The revision rolls out, was current before, or was still rolling out when a newer revision was created. |
| `Current` | `False`: `ChildNotReady` or `HookFailed` | The revision failed at its deadline, or a pre-hook failed it. |

funcd updates `Applied` and `ChildrenReady` only while the revision is the newest one and `Deploying`; after that only
`Current` changes. `todo-1` of the hooks example therefore keeps `ChildrenReady` at `Unknown`, with the reason
`NotStarted`, even after calls start its parts.

## Changes and deletion

An apply of a spec that differs from the latest revision's spec, other than in `paused`, creates a new revision, which
funcd rolls out in this order:

1. It waits for the Apps that `spec.requires` names, with no time limit, and writes nothing meanwhile.
2. It calls the pre-hooks, as [Run hooks on each new revision](#run-hooks-on-each-new-revision) explains.
3. It writes the parts in the order of `status.children`: the ConfigMaps, the KV stores, the Buckets, the SQL
   databases, the Functions, the Workflows, the EventSources, the Sensors, the Routes, the Sites, the Lakes and the
   BackupSchedules. A part whose spec did not change stays as it is, and a changed Function starts a new revision
   beside the one that serves.
4. Once no part is pending, the revision becomes current, and funcd calls the post-hooks.
5. Once the post-hooks have succeeded, funcd deletes the parts that the new spec dropped, which are `Pruning` until
   then. A dropped KV store, Bucket or SQL database whose `deletion` is `delete` goes with its data; one whose
   `deletion` is `retain`, the default, stays with its data. A dropped part also stays while a run of a Workflow holds
   it or another object uses it.

When a revision fails, `status.currentRevision` keeps naming the revision before it, but the parts that the rollout
wrote keep the new spec: a Route, a Site, a Sensor or an EventSource follows it at once, and a changed Function
switches to its new revision whenever that revision becomes ready. A revision that failed at its deadline stays
`Failed`, even when its parts become ready later, and an apply of the same spec creates no revision: a new spec, such
as a fix or a rollback, starts a new one. A revision that a pre-hook failed goes on once `funcdctl app retry` has
called the hook again and the call has succeeded.

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
  `retain` stays with its data.
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
and the Bucket `todo-files` with `deletion: retain`, so both stay with their data, and the stores whose `deletion` is
`delete`, such as `todo-cache`, went with theirs. The command waits with no time limit, such as while another object
still uses a store that it deletes; an interrupt stops the wait, not the deletion.

A kept store belongs to no App. An App of the same name that declares it by `name` stops at it, with the reason
`ChildNotOwned` and the message `KVStore/todo-store: exists and is not owned by App/todo`; `funcdctl dev` shows it at
the next change of one of its inputs. To take a kept store back, declare it with `ref`, or delete the store first.
Under `when`, `app.yaml` can make a file of `resources` depend on a condition over the values: the todo template
declares its kept stores by `name` in `resources/kept.yaml` and by `ref` in `resources/kept-ref.yaml`, and
[`values/reinstall.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/todo/values/reinstall.yaml) sets the
value `keptStores` that selects the second file.

Press Ctrl-C in the first terminal to stop the example.

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
| A message that names an image that no `--build` holds | Name the image's build with `--build`, such as `--build api=<path>`. |
| `App todo requires App todo-shared ^1.0.0: funcdctl dev does not run an App that requires another yet` | Run the App on a funcd server with `funcdctl app deploy`. |

## Run it locally

`funcdctl dev` runs an App template on a local platform as `funcdctl app deploy` runs it on a funcd server, with these
differences:

- It takes each image from a local build that `--build` names, and pushes it into an OCI image layout, a folder that
  holds images as a registry does, in its state folder, which replaces the template's `registry`. It neither reads nor writes `app.lock`.
- It changes nothing else in the App but its hooks, which it drops unless `--hooks` is set. Unlike the Functions of a
  folder of resource files, which `funcdctl dev` keeps running, an App's Function parts keep their own `replicas`, so a
  part that sets none runs no worker until its first call. A save of a build that cannot load therefore still prints
  `reloaded in <duration>` when its part has not run: the fault shows at the part's first call, which fails with the
  HTTP status 503.
- It creates each Secret that the App declares, unless a file that `--apply` names defines it, with each key from the
  environment variable of the key's name, else from the `envFile` or the `env` of its `funcdctl.config.yaml` file.
- `--apply` names a file of resource documents that it applies before the App, again when the file changes, and
  deletes once they leave the file, such as the RolesAssignments that an App needs beside it.
- It renders and applies the template again within about a second after one of its inputs changes: a file of the
  template folder, a values file, an `--apply` file, a `--build` folder or path, or the `build`, `values`, `apply` or
  `env` keys of `funcdctl.config.yaml` and the file that its `envFile` names. It prints `reloaded in <duration>` once
  the new revision is current. A reload that fails is printed, and the last App keeps running. For a revision that
  does not become current, it prints
  `the new revision did not become current; save a fix, or run funcdctl app retry` with the reason, and keeps running.
- It keeps each volume of a Function as a plain folder and does not limit its size: it only warns when a volume holds
  more than its size.
- It runs the namespace `default` only.

Without `--persist`, `funcdctl dev` keeps the App, its revisions and the data of its stores in memory, and its volumes
in a temporary folder, and loses them when it stops. It keeps the images that it pushed and a record of the processes
that it starts in `funcd/dev/<id>` under the user's cache folder, such as `~/.cache` on Linux or `~/Library/Caches` on
macOS, where `<id>` stands for the template folder; that folder holds no platform data, and the next run reuses it.
With `--persist`, it keeps all its state between runs in `.funcd-dev` in the current folder, or in the folder that
`--persist-to` names: the objects that it applied, the App's revisions included, the stores' data, the volumes and the
pushed images. A restart with the same inputs creates no revision. To start again from nothing, delete that folder
after `funcdctl dev` stops. The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its
flags and the keys of `funcdctl.config.yaml`.

## Examples

- [volumes](https://github.com/pyvvo/funcd-functions/tree/main/apps/volumes): an App of two Functions, each with a
  volume and a Route, installed from its template with a values file.
- [todo](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): the App of a to-do list, with its hooks, the
  stores that it keeps or deletes with the App, a Workflow, a Site and three versions of its template.
