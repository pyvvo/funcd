---
title: Add-ons
description: Register an add-on built outside funcd, such as rauthy, declare its objects, run Functions and Jobs on the runtimes it ships, call their engines from a Function, apply their settings, reconcile an engineless kind's objects, back them up and sign users in through them.
---

## Usage

An add-on is built outside funcd, and an admin registers it on a running funcd. It ships runtimes: images that
Functions and Jobs run on, or that run one engine per object of the kind it adds. Its users then declare objects of
that kind, as they declare a Lake: funcd runs one engine per object, serves it at the object's host, and copies its
data into the namespace's backups. The [Platform page](../../../platform/services/addons.md) explains how.
The add-ons funcd ships live in [pyvvo/funcd-addons](https://github.com/pyvvo/funcd-addons). This page uses rauthy, an
OpenID Connect provider of kind `IdentityProvider`.

The server's `addons.registries` must list the registry of the add-on's image, `ghcr.io` for rauthy. An admin then
registers the add-on from a release, piping its manifest to `funcdctl apply -f -`:

```sh
curl -fsSL https://github.com/pyvvo/funcd-addons/releases/latest/download/rauthy.yaml | funcdctl apply -f -
```

The Addon lists the runtimes the add-on ships, each with its image pinned by digest. rauthy's one entry serves the
workload `Engine`: one engine of its image runs per object of the kind its attributes name, with its port and readiness
path, its storage, and the schema of its objects' `spec.config`. This one is shortened from rauthy's
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/addons/addon.yaml)):

::include[../../../../../api/types/v1alpha1/examples/addons/addon.yaml#addon]

A user writes the Secret its objects bind with rauthy's plugin, then declares an identity provider
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/addons/addonobject.yaml)):

::include[../../../../../api/types/v1alpha1/examples/addons/addonobject.yaml#addonobject]

```sh
funcdctl rauthy keys --admin-email admin@example.com -n default
funcdctl apply -f identityprovider.yaml
funcdctl get identityproviders -n default
```

funcd checks `spec.config` against the add-on's schema when the object is applied. The object turns `Ready` once its
engine answers on its readiness path, and the edge then serves it at `login.example.com` under `/auth/v1`.

## Run Functions and Jobs on its runtime

An entry whose workloads hold `Function` or `Job` is a runtime that Functions or Jobs name in `spec.runtime`, as they
name `nodejs22`. Its name is none of funcd's runtimes, does not start with `node` or `python`, and is shipped by no
other Addon. The Addon `deno` ships `deno2` for both, and the Function `greeter` runs on it
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/addons/runtimes.yaml)):

::include[../../../../../api/types/v1alpha1/examples/addons/runtimes.yaml#addon]

::include[../../../../../api/types/v1alpha1/examples/addons/runtimes.yaml#function]

funcd resolves `spec.runtime` to one of its own runtimes first, then to an Addon's, then to the operator's
`runtime.containerd.imageOverride`. A Revision records the image of the Addon's runtime that serves it in
`spec.runtimeImage`, and its workers run that image, pulled by digest as an engine's is: an upgrade of the Addon moves
no Revision, and only a change of the Function's spec makes a Revision that runs the new image. A Function applied
before its Addon turns `Ready` once the Addon ships its runtime for Functions. Such a worker runs on containerd, and
under `funcdctl dev` on the Docker or nerdctl that runs dev's add-on engines; on a process server it is
`RuntimeUnavailable`. A Function on such a runtime is a cloudevent one, with the bindings its image's shim builds in.

Under `funcdctl dev`, the worker's bundle is copied into a volume of its own, and the worker reaches funcd's local API
at `FUNCD_INVOKE_URL`, sending `FUNCD_INVOKE_TOKEN` in the `X-Funcd-Invoke-Token` header, as funcd's shims do. On a
Linux host it shares the host's network; on an engine in a VM, as on macOS, it reaches funcd at `funcd.host`. With no
container engine, or a rootless one, the Function or Job is `RuntimeUnavailable`, naming why.

## Call its engine from a Function

A Function reaches an add-on object's engine through funcd only, under an alias of
[`spec.bindings.addon`](../../workloads/function/spec.md#functionspecbindings). The binding names an object of the
Function's namespace; a Function of an App binds only an object its App declares, by name or by ref. The Function
waits in `Pending` with `AddonObjectNotFound` while the object does not exist.

[`ctx.addon.fetch`](spec.md#handler-api) sends a request to the engine and returns its answer, whatever its status, as a
`Response` in TypeScript and an `AddonResponse` in Python. funcd follows no redirect, and drops the forwarding headers
and every `X-Funcd-*` header both ways. A call funcd refuses throws, or raises a `RuntimeError` in Python, with its
status and its problem. Any HTTP client can call the engine too, at `FUNCD_ADDON_<ALIAS>_URL`, the add-on proxy, with
the header `X-Funcd-Addon-Token` set to `FUNCD_ADDON_<ALIAS>_TOKEN`; the proxy drops the token before the engine.

The Function `addon-caller` of the lane example binds the object `echo`, a whoami engine that answers the headers it
gets, and calls it both ways
([complete file](https://github.com/pyvvo/funcd-functions/blob/main/apps/addon-caller/fixtures/addon-caller.yaml)):

::include[../../../../../_repos/funcd-functions/apps/addon-caller/fixtures/addon-caller.yaml]

::include[../../../../../_repos/funcd-functions/apps/addon-caller/caller/addon-caller.mjs]

A path that the object's Addon keeps private is refused with 404, and so is a path outside its `basePath`, as at the
edge. The Function's own dial of the engine's address is refused: only funcd reaches an engine.

## Apply its settings

An Addon can add settings kinds. An object of one runs no engine and names its target, an object of the Addon's kind:
rauthy's `RauthyPasswordPolicy` holds the password policy of one IdentityProvider. The kind's Function applies it, and
funcd checks each write against that Function's contract and the kind's constraints, here one policy per
IdentityProvider
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/addons/settings.yaml)):

::include[../../../../../api/types/v1alpha1/examples/addons/settings.yaml#policy]

Once a policy names the IdentityProvider `login`, funcd creates the Function `login-rauthypasswordpolicy` from the
image that the Addon names. The Function binds `login` and its Secrets, and has no address: only funcd calls it. funcd
sends it the IdentityProvider's full list of policies, and each policy turns `Ready` on its answer.

funcd calls the Function at each change of a target's list, after a failed call's backoff, and at the kind's `resync`,
so a setting changed at the engine's own API is put back. Each call is a CloudEvent of type `funcd.addon.settings`,
whose data holds the kind, the cause, the target with the alias that binds it, and each object's name, uid, generation
and config. The answer holds a result per object: `ok`, or a reason and a message. With no object left, the list is
empty: the Function puts the engine's default back, and funcd then removes it. While no policy has named an
IdentityProvider, funcd creates no Function for it and leaves its engine's own settings as they are. rauthy's Function
puts the policy with rauthy's API key `settings`, which `funcdctl rauthy keys` has rauthy create at its first start:

::include[../../../../../_repos/funcd-addons/rauthy/settings/rauthy-settings.mjs]

A target may name its own Function for a kind in `spec.functions`, in place of the kind's, so two copies of an App
template in one namespace each apply their own, and an Addon may name an existing Function of the namespace with
`function.ref` instead of an image. Such a Function binds the target itself, and no schema checks a write of its
objects: its contract refuses the call.

## Reconcile objects with a Function

An Addon whose kind entry serves `Engineless` adds a kind whose objects run no engine, such as stacks that a Job
applies outside funcd: a Function reconciles them. funcd creates one for each object from the entry's `function.image`,
and it binds the Secrets and the Jobs of the object's `bindings`. With `function.ref`, it is an existing Function of the
namespace, and an object may name its own for its kind in `spec.functions`
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/addons/engineless.yaml)):

::include[../../../../../api/types/v1alpha1/examples/addons/engineless.yaml#addon]

::include[../../../../../api/types/v1alpha1/examples/addons/engineless.yaml#stack]

funcd sends the Function the list of the objects it reconciles, deleting ones included, at each change of the list,
after a failed call's backoff, at the kind's `resync`, 10 minutes when unset, and once a JobRun of the namespace ends.
A Function from an image gets its one object. An object is `Pending` with `Progressing` until the Function answers for
its generation, then `Ready` on an ok, or with the Function's reason, such as `AwaitingApproval`. [Build an
add-on](build.md#engineless-kinds) shows the call and its answer.

An object with `deletion: delete` stays at its deletion until its Function has destroyed what it holds. The DELETE
answers 202 with the object, which keeps `metadata.deletionTimestamp`, the reason `Deleting` until the Function's first
answer, and its later writes. Only the Function's ok to a call that carried it as deleting removes it. With `retain`,
the default, the object goes at once and the Function forgets it, leaving what it manages in place; setting `retain`
on a deleting object lets it go at its next pass. A deleting object whose Addon or Function is gone waits until they
are back or `retain` is set:

```sh
funcdctl delete InfraStack bunny-edge -n default
funcdctl get infrastacks bunny-edge -n default -o json
```

`funcdctl delete` prints `deleting InfraStack/bunny-edge` for an object it holds. An App's deletion and a revision that
drops such an object wait the same way: the App's other parts, its Function and its Jobs among them, stay until its
engineless parts are gone.

## Ship it with an App

An App template can carry the add-on with the Functions, values and hooks that run beside it: its `addon.yaml` holds the
Addon, which an admin's `funcdctl app deploy` registers or updates before the App, and the App's `addons` section holds
the objects of its kind. rauthy's template holds an IdentityProvider `<app>-login`, whose policies funcd applies with
the Function that the Addon names:

::include[../../../../../_repos/funcd-addons/rauthy/app/resources/rauthy.yaml]

```sh
funcdctl push --template app <registry>/rauthy:0.2.0
funcdctl app deploy <registry>/rauthy:0.2.0 -f values.yaml --name web-auth
```

A developer's deploy of the template needs the kind registered already; [Carry an
add-on](../../workloads/app/index.md#carry-an-add-on) shows the rest.

## Upgrade it

An admin's change of an Addon stamps a revision, an AddonRevision named after the Addon and its generation, such as
`rauthy-2`, and replaces no engine: each object keeps running the revision that `status.addonRevision` names. An edit
of the object moves it to the Addon's current revision, and an App's next revision moves its parts, its hooks around
the move. A settings kind follows its target: its Function's image and contract and its constraints are those of the
target's revision, so a field that a newer image takes gets a 422 until the target moves. A settings object that the
newer contract refuses waits with `ReconcileFailed`, naming the path, and the target's other objects of the kind wait
with it. An admin lists the revisions with `funcdctl get addonrevisions`; funcd removes one that no object pins and no
App's revision records. Under `funcdctl dev`, every object follows the current revision at once.

## Back it up

A BackupSchedule copies an add-on object when its scope names it under `addons`, and an App's scope copies its `addons`
parts: each Backup copies the newest copy that the engine wrote of its own data. A Restore of an add-on object is in
place only. funcd restarts the engine with the copy, then again without it, and holds the namespace meanwhile.

## Sign users in through it

An `OIDCProvider` with `client.registration` has funcd register the edge's sign-in client at the provider, and its
`addon`, naming the IdentityProvider, has funcd reach rauthy at the engine's own address. A Route whose `auth.mode` is
`oidc` then signs its callers in, and the code behind it reads who signed in:
[OIDC sign-in](../../security-and-networking/sign-in/index.md).

## Build one

[pyvvo/funcd-addons' rauthy](https://github.com/pyvvo/funcd-addons/tree/main/rauthy) is a complete add-on to build
from: its Addon and App template, its settings Function, its examples and its funcdctl plugin. [Build an
add-on](build.md) shows what an engine must do and how rauthy's parts are written.

## Access

Only an admin registers, changes or deletes an Addon, and only an admin reads its AddonRevisions. An Addon's runtime is
trusted as its engine is: an admin's, pinned by digest and from a listed registry. Its workers' egress is governed as
funcd's own runtimes' is. A developer of a
namespace declares the objects of an add-on's kind there.

A Function calls the engines of the objects it binds with no role. For each call, funcd asks Cedar whether the Function
may perform `addon::call` on the object, which this built-in policy allows:

```cedar title="Built-in add-on call policy"
@id("addon-call-binding")
permit(principal, action == Action::"addon::call", resource)
  when { principal has addonBindings && principal.addonBindings.contains(resource) };
```

A namespace [Policy](../../security-and-networking/security/spec.md#policy) can forbid a call that a binding allows,
with 403. An alias the Function does not bind, and a proxy request with no token or a token of no Function, are 403 too.

A Function that funcd creates from an image reads every key of the Secrets that its object binds, while an engine gets
the keys that its Addon lists in `secretKeys`.

## Limits and quotas

- A namespace holds at most `addons.maxObjectsPerNamespace` objects of add-on kinds, settings kinds apart, 20 by
  default, all add-ons together, an engineless kind's objects too, and at most `addons.maxSettingsPerNamespace`
  settings objects, 200 by default.
- A call of an engine waits `addons.callTimeout` for the answer, 10 seconds by default, then fails with 503. Its request
  body and its answer body hold at most `addons.callPayloadLimit` bytes each, 1 MiB by default, or the call fails with
  413.
- A call of a settings or an engineless kind's Function waits `addons.settingsTimeout`, 30 seconds by default, then
  fails, and funcd calls again after 1 second, then twice as long after each failure, at most 5 minutes or the kind's
  resync.

## Status

```sh
funcdctl get addons rauthy -o json
funcdctl get identityproviders login -n default -o json
funcdctl get rauthypasswordpolicys strict -n default -o json
funcdctl logs identityprovider/login -n default
```

`funcdctl logs` prints what the object's engine wrote, both streams at INFO, to whoever may read the object. Under
`funcdctl dev`, the lines also go to the log file that dev prints as the container starts, and never to its terminal.

| Object | Reason | Meaning |
| --- | --- | --- |
| An Addon | `ArtifactNotReady` | The image of a kind's Function cannot be read. funcd tries again every `runtime.supervisionPeriod`; engines run meanwhile |
| | `SettingsInvalid` | A settings kind's constraint names a field that its Function's contract lacks |
| An object that runs an engine | `EngineNotReady` | The engine does not answer its readiness path yet |
| | `AddonNotFound` | No Addon registers its kind |
| | `ConfigInvalid`, `EnvInvalid` | Its config no longer fits the Addon's schema, or its env does not evaluate |
| | `SecretNotFound`, `BindingResolveFailed` | A bound Secret is missing or forbidden |
| | `RuntimeUnavailable` | The server's add-on runtime runs no image, as on a process server |
| A settings object | `Progressing` | No answer of its Function for its generation yet |
| | `ReconcileFailed`, `NoResult` | The call failed, or its answer left the object out; the Function's own reason when it gives one |
| | `TargetNotFound`, `TargetNotReady` | Its target is missing or not Ready |
| | `ArtifactNotReady` | The image of its kind's Function is not read yet |
| | `FunctionNotFound`, `FunctionNotBound` | Its Function is missing, its target's revision lacks its kind, or its Function does not bind its target |
| | `FunctionNotOwned` | Another Function has the name of the one that funcd would create |
| An engineless object | `Progressing` | No answer of its Function for its generation yet |
| | `ReconcileFailed`, `NoResult` | The call failed, or its answer left the object out; the Function's own reason when it gives one |
| | `AddonNotFound`, `AddonRevisionNotFound`, `FunctionNotFound`, `ArtifactNotReady`, `FunctionNotOwned` | Its Addon, its pinned revision, its Function or its image's contract is missing, or another Function has its Function's name, and no call carries it |
| | `Deleting` | Held at its deletion: its Function has not answered a call that carried it as deleting yet |
| A Function or a Job on its runtime | `RuntimeUnavailable` | The entry does not serve the workload, no worker driver of the server runs an image, or the Addon that shipped the Revision's image is gone |
| A Function that binds an object | `AddonObjectNotFound` | The object does not exist |
| | `DependencyNotReady` | A worker is not ready: the object's engine is not Ready |

## Examples

- [rauthy](https://github.com/pyvvo/funcd-addons/tree/main/rauthy): the add-on, its template and its settings Function,
  which pyvvo/funcd-addons' `addon-rauthy` lanes register, deploy and apply.
- [addon-caller](https://github.com/pyvvo/funcd-functions/tree/main/apps/addon-caller): a Function that calls an add-on
  object's engine, which pyvvo/funcd-addons' `addon-rauthy-signin` lane runs.
