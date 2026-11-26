---
title: Namespaces and resource groups
description: Delete the resources of a resource group as one, set the defaults of a namespace with a Namespace, and reach a namespace at its own host.
---

## Usage

A [namespace](../../../platform/tenancy-and-limits/namespaces.md) keeps its resources apart from other namespaces', and
a resource group gathers the resources that belong together, such as one application's.

The first example deletes the log-burst Function through its group `rg1`. Install the
[dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`. Clone both
repositories into one folder and start the example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/log-burst --gport 8080 --cport 8081
```

In another terminal, from the folder that holds both clones, call the Function:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"emitted":115}
```

Each resource names its group in [`metadata.resourceGroup`](../../common-definitions.md#objectmeta). Naming a group
creates no **ResourceGroup**, but funcd deletes a group only through one of its name:

::include[../../../../../api/types/v1alpha1/examples/namespaces/resourcegroup.yaml#resourcegroup]

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/namespaces/resourcegroup.yaml
applied ResourceGroup/rg1
```

funcd refuses to delete a group that has a **member**, a resource that names the group and that no other resource
owns:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete resourcegroup rg1 -n default
funcdctl: sdk: admission.resourcegroup-deletion-protection: ResourceGroup "rg1" still has 1 member(s): Function/log-burst; delete them first, or delete with force
```

`--force` deletes the members first; the Function's revision is no member, since the Function owns it:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete resourcegroup rg1 -n default --force
deleted ResourceGroup/rg1
```

The Function is gone:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get function log-burst -n default
funcdctl: sdk: store.Get: Function "log-burst" not found
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/log-burst
{"type":"urn:funcd:problem:not-found","title":"Not Found","status":404,"detail":"dataplane.ServeHTTP: function default/log-burst is not exposed by name"}
```

`funcdctl dev` applies the folder again when a file changes or it restarts. Press Ctrl-C in the first terminal to stop.

## Set a namespace's defaults

A **Namespace** holds the settings of the namespace of its name: whether its Functions and Sites answer at their own
addresses, whether its edge asks callers to authenticate, and its volume budget. It belongs to no namespace.

Only an admin acts on a Namespace, so these tasks need a funcd server on Linux with an admin entry in
[`auth.credentials`](../../running-funcd/spec.md#funcdconfigyaml), a `tokenFile` with `role: admin`. `auth.credentials`
replaces `auth.token`: funcd refuses to start with both, and every other credential needs its own entry listing its
`namespaces`.

Each task applies a file of funcd's repository from a clone's root, with the admin token in `ADMIN_TOKEN`, to a server
whose API listens at `127.0.0.1:8080`. Replace each `registry.example.com` image with an
[artifact](../../../platform/workloads/artifacts.md) that the server can pull, as
[`funcdctl push`](../../tools/funcdctl/spec.md#funcdctl-push) prints. Calls go to the edge at `server.dataPlaneAddr`,
which takes a free port by default, so set one, such as `127.0.0.1:8081`, and name the namespace's host in the `Host`
header, such as `-H 'Host: shop.localhost'`.

## Turn off the Functions' own addresses

A Function answers at `/function/<name>`, and a Site at `/site/<name>`, or at its `spec.ingress` host and path. A
Namespace whose [`defaultExposure`](spec.md#namespacespec) is `explicit` turns these addresses off for each Function
and Site that does not set `spec.ingress.enabled`:

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#namespace]

The [complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/namespaces/namespace.yaml)
also holds `catalog`, which turns its address on again with
[`spec.ingress.enabled`](../../workloads/function/spec.md#functionspecingress):

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#enabled]

and the Route `orders`, which serves the Function `orders` at `/orders`:

::include[../../../../../api/types/v1alpha1/examples/namespaces/namespace.yaml#route]

Apply it with `funcdctl --server http://127.0.0.1:8080 --token "$ADMIN_TOKEN" apply -f api/types/v1alpha1/examples/namespaces/namespace.yaml`.
On `shop.localhost`:

- `/function/orders` gets 404, `function shop/orders is not exposed by name`, and starts no worker; `/function/catalog`
  answers.
- `/orders` answers through the Route: a Route that no Function or Site owns stays on.
- A Route that a Site or a Function owns turns off too, with the reason `Disabled`, unless `spec.ingress.enabled`.
- A call over a link still reaches `orders`.

In the namespace `default`, which has no host, `explicit` also turns off each Route without a host, with
[`HostRequired`](../../security-and-networking/networking/index.md#choose-the-host-of-a-route).

## Set the default authentication of a namespace

A Namespace whose [`edgeDefaults.auth.mode`](spec.md#namespacespecedgedefaultsauth) is `authenticated` asks for a token
on every address of the namespace that sets no mode of its own; a mode set by a Function, a Route or a rule wins:

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#namespace]

The [complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/namespaces/edge-defaults.yaml)
also holds `status`, which keeps its address open:

::include[../../../../../api/types/v1alpha1/examples/namespaces/edge-defaults.yaml#open]

Apply it with `funcdctl --server http://127.0.0.1:8080 --token "$ADMIN_TOKEN" apply -f api/types/v1alpha1/examples/namespaces/edge-defaults.yaml`.
A call to `/function/invoices` on `billing.localhost` gets:

| Call | Answer | Action |
| --- | --- | --- |
| Any call while `server.auth.edge` is `false`, its default, with a token or without one | 401, `authentication required but no authenticator is configured` | Set `server.auth.edge` to `true`. |
| A call without a token | 401, `missing bearer token or API key` | Send a token in the `Authorization: Bearer` or the `X-Api-Key` header. |
| A call whose token funcd does not know | 401, `invalid credential` | Send a token that funcd's configuration sets. |
| A call with the token of a credential that does not act in `billing`, such as a developer of `default` | 403, `not authorized to invoke billing/invoices: developer not scoped to namespace "billing"` | List `billing` in the `namespaces` of the credential's entry of `auth.credentials`. |
| A call with an admin's token, or with the token of a developer or a viewer whose entry lists `billing` in its `namespaces` | The Function's answer: the Function receives the call without its `Authorization` and `X-Api-Key` headers | None. |

`/function/status` answers with no token. A missing or turned-off address gets the same 401 before its 404, so a caller
without a token cannot learn which names exist. See also
[Ask callers to authenticate](../../security-and-networking/networking/index.md#ask-callers-to-authenticate).

## Reach a namespace at its own host

This needs a funcd server with a credential that acts in the namespace, but no Namespace. Every namespace other than
`default` has the host `<namespace>.<server.baseDomain>`, such as `shop.localhost` with the default
[`server.baseDomain`](../../running-funcd/spec.md#funcdconfigyaml). On it:

- A Route of the namespace with no host answers on that host alone, and its `status.host` names it.
- `/function/<name>` reaches the namespace's Function, on the host and every name under it.
- A Route whose host is a name under it, such as `api.shop.localhost`, answers there.

On a host that names no namespace, such as `localhost`, `/function/<name>` goes to the namespace of the
`X-Funcd-Namespace` header, else to `default`. [Choose the host of a Route](../../security-and-networking/networking/index.md#choose-the-host-of-a-route)
gives the other rules.

## Limit the volume space of a namespace

A Namespace's [`volumeBudget`](spec.md#namespacespec), or `volumeBudgetBytes`, limits the volume space that its
Functions reserve: each local volume from its apply until its removal, and each emptyDir while its worker exists.
[Volumes](../../workloads/function/spec.md#functionspecvolumes) need `runtime.mode: containerd` and a
`volumes.capacity`:

::include[../../../../../api/types/v1alpha1/examples/namespaces/volume-budget.yaml]

Apply it with `funcdctl --server http://127.0.0.1:8080 --token "$ADMIN_TOKEN" apply -f api/types/v1alpha1/examples/namespaces/volume-budget.yaml`.
A local volume that does not fit is refused with 409 and a message ending in `VolumeBudget`; a worker whose emptyDir
does not fit does not start, with `VolumeBudget` on the Function's `Ready` condition.

## Access

The credential's role decides, as for every object of the API:

- An `admin` acts everywhere, and is the only role that acts on a Namespace.
- A `developer` manages ResourceGroups, as every object, in its credential's namespaces. `funcd-dev-token`, which
  `funcdctl dev` uses, and the token of `auth.token` are developers of `auth.namespaces`, `default` by default.
- A `viewer` reads ResourceGroups in its namespaces.

Elsewhere, a request gets 403, such as `list Function in "shop" denied: developer not scoped to namespace "shop"`. A
forced delete deletes each member with the caller's credential. What a Function or an Identity may do inside a
namespace is on the [Security](../../security-and-networking/security/index.md#access) page.

## Limits and quotas

- A namespace holds at most 100 KVStores, 100 Buckets and 20 SQL databases;
  [`kvstore.maxStoresPerNamespace`](../../running-funcd/spec.md#funcdconfigyaml) and `sql.maxDatabasesPerNamespace`
  change two of them. A create past a cap gets 400.
- A refused group delete names at most five members, and counts the rest.

## Changes and deletion

- funcd reads a Namespace's `defaultExposure` and `edgeDefaults` at each call; the Routes that Functions and Sites own
  follow within `controller.routeResyncInterval`, 10 seconds by default. It reads `volumeBudget` at each reservation.
- Deleting a Namespace only resets its settings; the namespace's resources keep running.
- `--force` deletes a group's members pass by pass, Sensors first, each under its own kind's checks, records such as a
  Sensor's WorkflowRuns included. A member that funcd refuses, such as a link's target, stops it; the members already
  deleted stay deleted, and a new delete continues.
- A group without a ResourceGroup cannot be deleted: the delete gets 404.

## CLI errors

These errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js for the example's Function. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. With Docker or nerdctl, `funcdctl dev --containers` needs no Node.js. |
| `<file>: <kind> "<name>" is in namespace "<namespace>"; funcdctl dev runs namespace default only` | Remove `metadata.namespace` from the object, set it to `default`, or apply the file to a funcd server. |
| `manifest is invalid (<kind> "<name>")`, followed by `resourceGroup is required for kind "<kind>"` | `funcdctl apply` checks each document before it sends it. Set `metadata.resourceGroup` on every object of a namespace. |
| `manifest is invalid (Namespace "<name>")`, followed by a value that `must be implicit or explicit` or `must be authenticated or open` | Set `defaultExposure` or `edgeDefaults.auth.mode` to a value that the [Spec](spec.md#namespacespec) tab lists. |
| `manifest is invalid (Namespace "<name>")`, followed by `spec.volumeBudget and spec.volumeBudgetBytes are both set: want at most one` | Keep one of the two budgets. |
| `manifest is invalid (Namespace "<name>")`, followed by `spec.volumeBudget` and the reason the size does not parse | Write `spec.volumeBudget` as a size that the [Spec](spec.md#namespacespec) tab describes. |

## Run it locally

- `funcdctl dev` runs the namespace `default` only, and stops on another.
- Its token is a developer's, so a Namespace in its folder stops it; `default` keeps its defaults there.
- It gives the group `dev` to each object that names none, without a ResourceGroup, so deleting `dev` gets 404.
- Its state stays in memory unless [`--persist`](../../tools/funcdctl/spec.md#funcdctl-dev) keeps it in `.funcd-dev`.

## Examples

- [log-burst in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/log-burst): the Function that
  the first example deletes through its group.
