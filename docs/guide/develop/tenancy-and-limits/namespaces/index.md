---
title: Namespaces and resource groups
description: Delete the resources of a resource group as one, set the defaults of a namespace with a Namespace, and reach a namespace at its own host.
---

## Usage

A namespace keeps its resources apart from the resources of other namespaces, and a resource group gathers the
resources of a namespace that belong together, such as the parts of one application. This page deletes the resources
of a group as one and sets the defaults of a namespace. The
[Platform page](../../../platform/tenancy-and-limits/namespaces.md) explains what a namespace bounds.

The first example starts the log-burst example on the local computer, creates the ResourceGroup `rg1` and deletes it
twice: funcd refuses the first delete, and the second, with `--force`, deletes the Function `log-burst`, then the
group.

Install the [dev build of funcdctl](../../../get-started/install.md), which includes the `funcdctl dev` command, Git,
curl and Node.js, and put Node.js on the `PATH`. `funcdctl dev` runs the resource files of one folder in the namespace
`default`. Its `--gport` flag sets the port of each Function's URL, and `--cport` the port of its control plane, which
a `funcdctl` command reaches with `--server` and the token `funcd-dev-token`.

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

Each resource names its group in [`metadata.resourceGroup`](../../common-definitions.md#objectmeta): the Function
`log-burst`, in the example's
[`burst.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/log-burst/burst.yaml), names the group
`rg1`. funcd deletes a group only through a **ResourceGroup** of the group's name. Naming a group creates no
ResourceGroup, and the resources of a group need none, so create the ResourceGroup `rg1` from funcd's
`api/types/v1alpha1/examples/namespaces/resourcegroup.yaml`. It names itself as its group, as every object of a
namespace names one, and its [`description`](spec.md#resourcegroupspec) is free text that funcd does not read:

::include[../../../../../api/types/v1alpha1/examples/namespaces/resourcegroup.yaml#resourcegroup]

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/namespaces/resourcegroup.yaml
applied ResourceGroup/rg1
```

Delete the group. funcd refuses while the group has a **member**, a resource that names the group and that no other
resource owns, and the refusal names the members:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete resourcegroup rg1 -n default
funcdctl: sdk: admission.resourcegroup-deletion-protection: ResourceGroup "rg1" still has 1 member(s): Function/log-burst; delete them first, or delete with force
```

The Function's [revision](../../workloads/function/index.md#changes-and-deletion), `log-burst-1`, names the group too,
but the Function owns it, so it is no member and goes with the Function. Delete the group with `--force`, which
deletes each member first, then the group:

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

`funcdctl dev` applies the folder's files again when one of them changes or when it starts again, and the Function
comes back with them. Press Ctrl-C in the terminal that runs `funcdctl dev` to stop the example.

## Set a namespace's defaults

A **Namespace** holds the settings of the namespace of its name: whether the namespace's Functions and Sites answer at
their own addresses, whether its edge asks callers to authenticate, and how much volume space its Functions may
reserve. Without a Namespace, or with one that sets nothing, each setting keeps its default. A Namespace belongs to no
namespace, so it sets no `metadata.namespace`. The [Spec](spec.md) tab describes every field of a Namespace and of a
ResourceGroup.

Only an admin credential acts on a Namespace, as [Access](#access) explains, and `funcdctl dev` has none. The tasks
that use a Namespace therefore need a funcd server, which runs on Linux, as the
[Install](../../../get-started/install.md) page shows, with an admin credential: an entry of `auth.credentials` in
funcd's configuration file whose `role` is `admin` and whose `tokenFile` names the file that holds its token, as the
funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes. `auth.credentials` replaces `auth.token`:
funcd refuses to start when `auth.token`, or an `auth.namespaces` other than its default, is set beside it, and the
built-in token stops working. Every other credential, such as a developer or a viewer of a namespace, then needs an
entry of its own, whose `namespaces` lists the namespaces where it acts.

With the admin's token in the variable `ADMIN_TOKEN`, a command such as
`funcdctl --server http://127.0.0.1:8080 --token "$ADMIN_TOKEN" apply -f namespace.yaml` applies a file that holds a
Namespace to a server whose API listens at its default address, `127.0.0.1:8080`. The files of the next tasks, in
funcd's repository under `api/types/v1alpha1/examples/namespaces`, also hold Functions whose `image` names
`registry.example.com`. Replace each with the reference of an
[artifact](../../../platform/workloads/artifacts.md) that the server can pull, which
[`funcdctl push <path> <ref>`](../../tools/funcdctl/spec.md#funcdctl-push) prints with its digest after it packs and
pushes a Function's build.

The calls of these tasks go to funcd's **edge**, which receives the calls from outside funcd at the address that
`server.dataPlaneAddr` sets. Its default, `127.0.0.1:0`, takes a free port, so set a fixed address, such as
`127.0.0.1:8081`, before calling it. Each call names a namespace's host in its `Host` header, as curl does with
`-H 'Host: shop.localhost'`; [Reach a namespace at its own host](#reach-a-namespace-at-its-own-host) explains these
hosts.

## Turn off the Functions' own addresses

This task needs the server and the admin credential of [Set a namespace's defaults](#set-a-namespaces-defaults).

Each Function and each Site answers at an address of its own: `/function/<name>` for a Function, `/site/<name>` for a
Site, or the host and the path that its `spec.ingress` sets. A Namespace whose
[`defaultExposure`](spec.md#namespacespec) is `explicit` turns these addresses off for every Function and Site of the
namespace whose `spec.ingress.enabled` is not set; `implicit`, or no value, keeps them on. The Namespace `shop` turns
them off:

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
  that an App declares included.
- A Site, and a Function whose `spec.ingress` sets a host or a path, answer through a Route that they own. That Route
  is turned off too, unless their `spec.ingress.enabled` is `true`: it serves nothing, and the reason of its `Ready`
  condition is `Disabled`.
- A call from another Function to `orders` over a link still reaches it: `defaultExposure` never refuses a call from a
  Function to a Function.

In the namespace `default`, which has no host of its own, `explicit` also turns off each Route that sets no host, with
the reason `HostRequired`, as
[Choose the host of a Route](../../security-and-networking/networking/index.md#choose-the-host-of-a-route) explains.

## Set the default authentication of a namespace

This task needs the server and the admin credential of [Set a namespace's defaults](#set-a-namespaces-defaults).

A Namespace whose [`edgeDefaults.auth.mode`](spec.md#namespacespecedgedefaultsauth) is `authenticated` asks callers for
a token on every address of the namespace that sets no mode of its own: the Routes and their rules, and the addresses
of the Functions and Sites. `open`, or no value, lets every caller in. The Namespace `billing` asks for a token:

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#namespace]

The same file holds the Function `invoices`, which sets no mode, and the Function `status`, whose
[`spec.ingress.auth`](../../workloads/function/spec.md#functionspecingressauth) keeps its address open: a mode that a
Function, a Route or a rule sets wins over the Namespace's default.

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#open]

A call to `/function/invoices` on the host `billing.localhost` gets one of these answers, which depend on the server
setting `server.auth.edge` that the funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

| Call | Answer | Action |
| --- | --- | --- |
| Any call while `server.auth.edge` is `false`, its default, with a token or without one | 401, `authentication required but no authenticator is configured` | Set `server.auth.edge` to `true`. |
| A call without a token | 401, `missing bearer token or API key` | Send a token in the `Authorization: Bearer` or the `X-Api-Key` header. |
| A call whose token funcd does not know | 401, `invalid credential` | Send a token that funcd's configuration sets. |
| A call with the token of a credential that does not act in `billing`, such as a developer of `default` | 403, `not authorized to invoke billing/invoices: developer not scoped to namespace "billing"` | List `billing` in the `namespaces` of the credential's entry of `auth.credentials`. |
| A call with an admin's token, or with the token of a developer or a viewer whose entry lists `billing` in its `namespaces` | The Function's answer: the Function receives the call without its `Authorization` and `X-Api-Key` headers | None. |

A call to `/function/status` reaches `status` with no token. A call to the address of a Function that does not exist,
or whose address is off, gets the same 401 before its 404, so a caller without a token cannot tell which names exist.
[Ask callers to authenticate](../../security-and-networking/networking/index.md#ask-callers-to-authenticate) on the
Networking page explains the modes of Routes and their rules.

## Reach a namespace at its own host

This task needs a funcd server on Linux, since `funcdctl dev` runs the namespace `default` only. It needs no Namespace
and no admin credential, but applying the namespace's resources needs a credential that acts in the namespace, as
[Access](#access) explains: list the namespace in `auth.namespaces` for the built-in token or the token of
`auth.token`, or in the `namespaces` of an entry of `auth.credentials`.

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

This task needs the server and the admin credential of [Set a namespace's defaults](#set-a-namespaces-defaults).

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

funcd's API decides who may create, change, read and delete Namespaces and ResourceGroups by the role of the caller's
credential, as for every object of the API. funcd's configuration sets the credentials, in `auth.token` and
`auth.credentials`, as the funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

- An `admin` acts on every object of every namespace. It is the only role that acts on a Namespace, which belongs to no
  namespace.
- A `developer` creates, changes, reads and deletes ResourceGroups in the namespaces of its credential, as it does
  every other object of these namespaces. The built-in token
  `funcd-dev-token`, which `funcdctl dev` uses, and a token that `auth.token` sets are developer credentials, which
  act in the namespaces that `auth.namespaces` lists, `default` by default.
- A `viewer` reads ResourceGroups in the namespaces of its credential, and changes nothing.

A developer or a viewer gets the HTTP status 403 for any request on a Namespace.

It acts only in the namespaces of its credential. Elsewhere, each request gets the HTTP status
403, with a message such as `list Function in "shop" denied: developer not scoped to namespace "shop"`: list the
namespace in the `namespaces` of the credential's entry, or use a credential that acts there. A forced delete of a
group deletes each member with the caller's own credential, so it needs the right to delete each of them. The
[Security](../../security-and-networking/security/index.md#access) page explains what a Function or an Identity may do
inside a namespace.

## Limits and quotas

- A namespace holds at most 100 KVStores, 100 Buckets and 20 SQL databases. The server settings
  `kvstore.maxStoresPerNamespace` and `sql.maxDatabasesPerNamespace` change the first and the last. funcd refuses a
  create past a cap with the HTTP status 400. The Platform page
  [Quotas](../../../platform/tenancy-and-limits/quotas.md) explains how much each store may hold.
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
- funcd refuses to delete a ResourceGroup that has a member. `--force` deletes the members pass by pass, Sensors
  first, each under the caller's credential and the checks of its own kind, then the group. The records that funcd
  writes with the group's name, such as the WorkflowRuns and Invocations of a Sensor, are members too.
- A member that funcd still refuses stops a forced delete with that member's refusal, such as a Function that a
  Function outside the group targets with a link, as the
  [Function](../../workloads/function/index.md#changes-and-deletion) page shows. The members already deleted stay
  deleted, and running the delete again continues where it stopped.
- A group that no ResourceGroup creates cannot be deleted: `funcdctl delete resourcegroup` gets the HTTP status 404.
  Create a ResourceGroup of the group's name first, as the first example does.

## CLI errors

These errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js for the example's Function. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `<file>: <kind> "<name>" is in namespace "<namespace>"; funcdctl dev runs namespace default only` | Remove `metadata.namespace` from the object, set it to `default`, or apply the file to a funcd server. |
| `manifest is invalid (<kind> "<name>")`, followed by `resourceGroup is required for kind "<kind>"` | `funcdctl apply` checks each document before it sends it. Set `metadata.resourceGroup` on every object of a namespace. |
| `manifest is invalid (Namespace "<name>")`, followed by a value that `must be implicit or explicit` or `must be authenticated or open` | Set `defaultExposure` or `edgeDefaults.auth.mode` to a value that the [Spec](spec.md#namespacespec) tab lists. |
| `manifest is invalid (Namespace "<name>")`, followed by `spec.volumeBudget and spec.volumeBudgetBytes are both set: want at most one` | Keep one of the two budgets. |

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for namespaces and resource groups:

- It runs the namespace `default` only, and stops on a resource of another namespace.
- Its token is a developer credential, so it cannot apply, read or list a Namespace, and a Namespace in its folder
  stops it at start. The namespace `default` keeps its defaults there: its Functions and Sites answer at
  their own addresses, unless their `spec.ingress` says otherwise, and no default asks callers to authenticate.
- It gives the group `dev` to each object of its folder that names no group, and to the objects that it writes for its
  Functions. It creates no ResourceGroup `dev`, so a delete of that group gets 404.

`funcdctl dev` keeps its state in memory, where it is lost when `funcdctl dev` stops. With the `--persist` flag, it
keeps its state between runs in `.funcd-dev`, in the current folder, or in the folder that `--persist-to` names: the
objects of its folder and those that `funcdctl apply` sent to it, such as the ResourceGroup of the first example, the
data of its services, and the copies of the builds that it runs. To start again from nothing, delete that folder after
`funcdctl dev` stops. The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst): the Function that
  the first example deletes through its resource group.
