---
title: Namespaces and resource groups
description: Delete the resources of a resource group as one, set the defaults of a namespace with a Namespace, and reach a namespace at its own host.
---

## Usage

A namespace holds resources apart from the resources of other namespaces. A resource group gathers the resources of a
namespace that belong together, such as the parts of one application. The
[Platform page](../../../platform/tenancy-and-limits/namespaces.md) explains what a namespace bounds; this page shows
how to delete the resources of a group as one, and how to set the defaults of a namespace.

The first example deletes the Function of the log-burst example through its resource group. The steps below start the
example on the local computer, create the ResourceGroup `rg1`, and delete it twice: funcd refuses the first delete and
names the Function `log-burst`, which belongs to the group, and the second delete, with `--force`, deletes the
Function, then the group. The next call to `log-burst` gets the HTTP status 404.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the repositories of the example and of funcd.
- curl, which calls the Function.
- Node.js on the `PATH`, which runs the example's Function.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it, in the namespace `default`. It
prints the URL of each Function, and the address of its control plane, which a `funcdctl` command reaches with
`--server` and the token `funcd-dev-token`. The `--gport` flag sets the port of each Function's URL, and `--cport` the
port of the control plane, so the commands below use the ports 8080 and 8081.

Each resource names its group in [`metadata.resourceGroup`](../../common-definitions.md#objectmeta). The Function
`log-burst`, in the example's
[`burst.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/log-burst/burst.yaml), names the group
`rg1`. funcd deletes a group only through an object of the group's name, a **ResourceGroup**. Naming a group in
`metadata.resourceGroup` creates no ResourceGroup, and the resources of a group need none, so the steps below create
the ResourceGroup `rg1`, from the file `api/types/v1alpha1/examples/namespaces/resourcegroup.yaml` of funcd's
repository. It names itself as its group, since every object of a namespace names one, and its
[`description`](spec.md#resourcegroupspec) is free text that funcd does not read:

::include[../../../../../api/types/v1alpha1/examples/namespaces/resourcegroup.yaml#resourcegroup]

The [Spec](spec.md) tab describes every field of a ResourceGroup and of a Namespace.

Clone both repositories into one folder, and start the example from the root folder of funcd-typescript:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/log-burst --gport 8080 --cport 8081
```

In another terminal, in the folder that holds both clones, call the Function with an empty JSON object. The call waits
until the Function's worker is ready, and its answer shows that the Function runs:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"emitted":115}
```

Create the ResourceGroup:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/namespaces/resourcegroup.yaml
applied ResourceGroup/rg1
```

Delete the group. funcd refuses, because the group still has a **member**, a resource that names the group and that no
other resource owns, and the refusal names it:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete resourcegroup rg1 -n default
funcdctl: sdk: admission.resourcegroup-deletion-protection: ResourceGroup "rg1" still has 1 member(s): Function/log-burst; delete them first, or delete with force
```

The Function's [revision](../../workloads/function/index.md#changes-and-deletion), `log-burst-1`, names the group too,
but the Function owns it, so it is no member: it goes with the Function. Delete the group with `--force`, which deletes
each member first, then the group:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete resourcegroup rg1 -n default --force
deleted ResourceGroup/rg1
```

Check that the Function is gone, then call it again:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default
funcdctl: sdk: store.Get: Function "log-burst" not found
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"type":"urn:funcd:problem:not-found","title":"Not Found","status":404,"detail":"dataplane.ServeHTTP: function default/log-burst is not exposed by name"}
```

`funcdctl dev` applies the folder's files again when one of them changes, or when it starts again, and the Function
comes back with them. To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Set a namespace's defaults

A **Namespace** holds the settings of the namespace that has its name: whether the namespace's Functions and Sites
answer at their own addresses, whether its edge asks callers to authenticate, and how much volume space its Functions
may reserve. A namespace needs no Namespace: without one, or with one that sets nothing, each setting keeps its default.
A Namespace belongs to no namespace, so it sets no `metadata.namespace`.

Only an admin credential creates, changes, reads or deletes a Namespace, and `funcdctl dev` has none. The tasks below
that use a Namespace therefore need a funcd server, which runs on Linux, as the
[Install](../../../get-started/install.md) page shows, with an admin credential: an entry of `auth.credentials` in
funcd's configuration file whose `role` is `admin`, and whose `tokenFile` names the file that holds its token. The
funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes these keys. The built-in token `funcd-dev-token`, and
a token that `auth.token` sets, are developer credentials, which get the HTTP status 403 for a Namespace.
`auth.credentials` replaces `auth.token`: funcd refuses to start when `auth.token`, or an `auth.namespaces` other than
its default, is set beside it, and the built-in token no longer works. Every other credential, such as a developer or
a viewer of a namespace, then needs an entry of its own, whose `namespaces` lists the namespaces where it acts.
[Access](#access) lists what each role may do.

With the admin's token in the variable `ADMIN_TOKEN`, a command such as
`funcdctl --server http://127.0.0.1:8080 --token "$ADMIN_TOKEN" apply -f namespace.yaml` applies a file that holds a
Namespace to a server whose API listens at `127.0.0.1:8080`, the default address. The files of the next two tasks, in
funcd's repository under `api/types/v1alpha1/examples/namespaces`, also hold Functions whose `image` names
`registry.example.com`. Replace each with the reference of an
[artifact](../../../platform/workloads/artifacts.md) that the server can pull: `funcdctl push <path> <ref>` packs a
Function's build and pushes it, then prints the reference with its digest, as the
[reference of `funcdctl push`](../../tools/funcdctl/spec.md#funcdctl-push) describes.

The calls below go to funcd's **edge**, the part of funcd that receives the calls from outside funcd, at the address
that `server.dataPlaneAddr` sets. Its default, `127.0.0.1:0`, takes a free port, so set it to a fixed address, such as
`127.0.0.1:8081`, before calling it. Each call names a namespace's host in its `Host` header, as curl does with
`-H 'Host: shop.localhost'`; [Reach a namespace at its own host](#reach-a-namespace-at-its-own-host) explains these
hosts.

## Turn off the Functions' own addresses

This task needs a funcd server on Linux and an admin credential, as
[Set a namespace's defaults](#set-a-namespaces-defaults) explains.

Each Function and each Site of a namespace answers at an address of its own: `/function/<name>` for a Function,
`/site/<name>` for a Site, or the host and the path that its `spec.ingress` sets. A Namespace whose
[`defaultExposure`](spec.md#namespacespec) is `explicit` turns these addresses off, for every Function and Site whose
`spec.ingress.enabled` is not set; `implicit`, or no value, keeps them on. The Namespace `shop` turns them off:

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#namespace]

The same file holds the Function `orders`, which sets no `spec.ingress`, and the Function `catalog`, which turns its
own address on again with [`spec.ingress.enabled`](../../workloads/function/spec.md#functionspecingress) set to
`true`:

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#enabled]

It also holds the Route `orders`, which serves the Function `orders` at the path `/orders`:

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#route]

With the file applied, the calls on the host `shop.localhost` get these answers:

- A call to `/function/orders` gets the HTTP status 404 with the message `function shop/orders is not exposed by name`,
  and starts no worker.
- A call to `/function/catalog` reaches `catalog`.
- A call to `/orders` reaches `orders` through the Route. Every Route that no Function or Site owns stays on, a Route
  that an App declares too.
- A Site, and a Function whose `spec.ingress` sets a host or a path, answer through a Route that they own. That Route
  is turned off too, unless their `spec.ingress.enabled` is `true`: it serves nothing, and the reason of its `Ready`
  condition is `Disabled`.
- Another Function still calls `orders` over a link: a call from a Function to a Function is never refused for the
  namespace's `defaultExposure`.

In the namespace `default`, which has no host of its own, `explicit` also turns off each Route that sets no host, with
the reason `HostRequired`, as
[Choose the host of a Route](../../security-and-networking/networking/index.md#choose-the-host-of-a-route) explains.

## Set the default authentication of a namespace

This task needs a funcd server on Linux and an admin credential, as
[Set a namespace's defaults](#set-a-namespaces-defaults) explains.

A Namespace whose [`edgeDefaults.auth.mode`](spec.md#namespacespecedgedefaultsauth) is `authenticated` asks callers for
a token on every address of the namespace that sets no mode of its own: the Routes and their rules, and the addresses
of the Functions and Sites. `open`, or no value, lets every caller in. The Namespace `billing` asks for a token:

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#namespace]

The same file holds the Function `invoices`, which sets no mode, and the Function `status`, whose
[`spec.ingress.auth`](../../workloads/function/spec.md#functionspecingressauth) keeps its address open: a mode that a
Function, a Route or a rule sets wins over the Namespace's default.

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#open]

What a call to `/function/invoices` on the host `billing.localhost` gets depends on the server setting
`server.auth.edge`, which the funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

- With `server.auth.edge` at its default, `false`, the edge has no authenticator, so every such call gets the
  HTTP status 401 with the message `authentication required but no authenticator is configured`, with a token or
  without one.
- With `server.auth.edge` set to `true`, a call without a token gets 401 with `missing bearer token or API key`, and a
  call whose token funcd does not know gets 401 with `invalid credential`. A call with the token of a credential that
  does not act in `billing` gets 403, such as
  `not authorized to invoke billing/invoices: developer not scoped to namespace "billing"` for a developer of
  `default`. A call with an admin's token, or with the token of a developer or a viewer whose entry of
  `auth.credentials` lists `billing` in its `namespaces`, reaches the Function, which receives it without its
  `Authorization` and `X-Api-Key` headers.

A call to `/function/status` reaches `status` with no token. A call to the address of a Function that does not exist,
or whose address is off, gets the same 401 before its 404, so a caller without a token cannot tell which names exist.
[Ask callers to authenticate](../../security-and-networking/networking/index.md#ask-callers-to-authenticate) on the
Networking page explains the modes of Routes and their rules.

## Reach a namespace at its own host

This task needs a funcd server on Linux, since `funcdctl dev` runs the namespace `default` only. It needs no Namespace
and no admin credential, but applying the namespace's resources needs a credential that acts in the namespace: the
built-in token, or the token of `auth.token`, acts only in the namespaces that `auth.namespaces` lists, `default` by
default, so list the namespace there, or use an entry of `auth.credentials` whose `namespaces` lists it.

Every namespace other than `default` has a host of its own, `<namespace>.<server.baseDomain>`.
[`server.baseDomain`](../../running-funcd/spec.md#funcdconfigyaml) is `localhost` by default, so the namespace `shop`
has the host `shop.localhost`. On that host, and on the names under it, such as `api.shop.localhost`:

- A Route of the namespace that sets no host, such as the Route of a Site that sets none, answers on the namespace's
  host alone, not on a name under it, and its `status.host` names that host.
- A call to `/function/<name>` reaches the Function of that name in that namespace, on the namespace's host and on
  every name under it.
- A Route of the namespace whose host is a name under the namespace's host, such as `api.shop.localhost`, answers on
  that name.

The namespace `default` has no host of its own. A call to `/function/<name>` on a host that names no namespace, such as
`localhost` or `127.0.0.1`, goes to the namespace that its `X-Funcd-Namespace` header names, else to `default`. On a
namespace's host, the header changes nothing.
[Choose the host of a Route](../../security-and-networking/networking/index.md#choose-the-host-of-a-route) gives the
other rules of a Route's host, such as when a Route of `default` answers on a namespace's host.

## Limit the volume space of a namespace

This task needs a funcd server on Linux and an admin credential, as
[Set a namespace's defaults](#set-a-namespaces-defaults) explains.

A Namespace's [`volumeBudget`](spec.md#namespacespec) limits the volume space that the namespace's Functions reserve:
each local volume's size from its apply until the volume is removed, and each emptyDir volume's size while its worker
exists. A Function declares its volumes under
[`spec.volumes`](../../workloads/function/spec.md#functionspecvolumes), which need a server whose `runtime.mode` is
`containerd` and that sets `volumes.capacity` or `volumes.capacityBytes`. The Namespace `media` gives its Functions
4 GiB:

::include[../../../../../api/types/v1alpha1/examples/namespaces/volume-budget.yaml]

`volumeBudgetBytes` sets the same budget as a number of bytes; set one of the two, not both. Without either, or with
0, the namespace has no budget, and only the server's volume capacity limits its volumes. In `media`:

- `funcdctl apply` refuses a Function whose local volume does not fit in what the namespace's other volumes leave of
  the budget, with the HTTP status 409 and a message that names the volume, its size and the budget, and ends with
  `VolumeBudget`.
- A worker whose emptyDir volume does not fit does not start, and the reason of the Function's `Ready` condition is
  `VolumeBudget`.

## Access

funcd's API decides who may create, change, read and delete Namespaces and ResourceGroups with role-based access
control (RBAC): the role of the caller's credential decides, as for every object of the API. funcd's configuration sets
the credentials, in `auth.token` and `auth.credentials`, which the funcd
[Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

- An `admin` acts on every object of every namespace. It is the only role that acts on a Namespace, which belongs to no
  namespace.
- A `developer` creates, changes, reads and deletes ResourceGroups in the namespaces of its credential, as every other
  object of these namespaces. `funcdctl dev`'s token, `funcd-dev-token`, is a developer credential of the namespace
  `default`.
- A `viewer` reads ResourceGroups in the namespaces of its credential, and changes nothing.

A developer or a viewer acts only in the namespaces of its credential: elsewhere, each request gets the HTTP status
403, with a message such as `list Function in "shop" denied: developer not scoped to namespace "shop"`. A forced
delete of a group deletes each member with the caller's own credential, so it needs the right to delete each of them.
The [Security](../../security-and-networking/security/index.md#access) page explains what a Function or an Identity
may do inside a namespace.

## Limits and quotas

- A namespace holds at most 100 KVStores, 100 Buckets and 20 SQL databases. The server settings
  `kvstore.maxStoresPerNamespace` and `sql.maxDatabasesPerNamespace` change the first and the last. A create past a
  cap gets the HTTP status 400, with a message such as `namespace "shop" already holds the maximum 100 KVStores`. The
  Platform page [Quotas](../../../platform/tenancy-and-limits/quotas.md) explains how much each store may hold.
- A Namespace's `volumeBudget` limits the volume space of its Functions, as
  [Limit the volume space of a namespace](#limit-the-volume-space-of-a-namespace) explains.
- A refused delete of a ResourceGroup names at most five members, followed by the number of the others.

The [Spec](spec.md) tab gives the range of each field.

## Changes and deletion

- A change to a Namespace's `defaultExposure` applies to the next call at a Function's address `/function/<name>`,
  since funcd reads the Namespace at each such call. The Routes that Functions and Sites own turn on or off when funcd
  checks them again, within `controller.routeResyncInterval`, 10 seconds by default.
- A change to a Namespace's `edgeDefaults` applies to the next call on every address of the namespace that sets no
  mode of its own, a Route that a Function or a Site owns included, since funcd reads the Namespace at each such call.
- funcd reads a Namespace's `volumeBudget` at each new reservation of volume space.
- Deleting a Namespace removes its settings alone: every resource of the namespace keeps running, new resources can
  still be created there, and each setting returns to its default.
- Deleting a ResourceGroup that has a member is refused. `--force` deletes the members pass by pass, Sensors first,
  each under the caller's credential and the checks of its own kind, then the group. The records that funcd writes
  with the group's name, such as the WorkflowRuns and Invocations of a Sensor, are members too. A member that funcd
  still refuses stops the delete with that member's refusal, such as a Function that a Function outside the group
  targets with a link, as the [Function](../../workloads/function/index.md#changes-and-deletion) page shows. The
  members already deleted stay deleted, and running the delete again continues where it stopped.
- A group that no ResourceGroup creates cannot be deleted: `funcdctl delete resourcegroup` gets the HTTP status 404.

## Errors

- **403** for a Namespace with a developer's or a viewer's token, such as
  `update Namespace in "" denied: developer may not act on cluster-scoped kind "Namespace"`. A developer or a viewer
  also gets 403 in a namespace that its credential does not name.
- **409** for a delete of a ResourceGroup that has members, such as
  `ResourceGroup "rg1" still has 1 member(s): Function/log-burst; delete them first, or delete with force`.
- **404** for a delete of a group that no ResourceGroup creates, such as `store.Get: ResourceGroup "dev" not found`.
- **409** ending with `VolumeBudget` for a Function whose local volume does not fit the namespace's budget.

`funcdctl apply` checks each document before it sends it, and refuses, with the message `manifest is invalid` followed
by the kind, the name and the reason:

- an object of a namespace that names no group: `resourceGroup is required for kind "ResourceGroup"`;
- a Namespace whose `defaultExposure` or `edgeDefaults.auth.mode` has another value than the Spec tab lists, such as
  `spec.defaultExposure "closed" must be implicit or explicit`;
- a Namespace that sets both budgets: `spec.volumeBudget and spec.volumeBudgetBytes are both set: want at most one`.

A call to a namespace's Functions meets the 404 of an address that is off, and the 401 and 403 of an address that asks
for a token, as [Turn off the Functions' own addresses](#turn-off-the-functions-own-addresses) and
[Set the default authentication of a namespace](#set-the-default-authentication-of-a-namespace) explain.

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for namespaces and resource groups:

- It runs the namespace `default` only, and stops on a resource of another namespace.
- Its token is a developer credential of `default`, so it cannot apply, read or list a Namespace. A Namespace in its
  folder stops it at start, with a message such as
  `funcdctl: funcdctl dev: apply Namespace "default": sdk: controlplane.authz: update Namespace in "" denied: developer may not act on cluster-scoped kind "Namespace"`
  for a Namespace named `default`. The namespace `default` keeps its defaults there: its Functions and Sites answer at
  their own addresses, unless their `spec.ingress` says otherwise, and no default asks callers to authenticate.
- It gives the group `dev` to each object of its folder that names no group, and to the objects that it writes for its
  Functions. It creates no ResourceGroup `dev`, so a delete of that group gets 404.

`funcdctl dev` keeps its data in memory, where it is lost when `funcdctl dev` stops. With the `--persist` flag, it
keeps its state between runs in `.funcd-dev`, a folder that it creates in the current folder, or in the folder that
`--persist-to` names: the metastore, which holds the objects of its folder and those that `funcdctl apply` sent to it,
such as the ResourceGroup of the first example, the data of its services, and the copies of the builds that it runs.
To start again from nothing, delete that folder after `funcdctl dev` stops. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst): the Function that
  the first example deletes through its resource group.
