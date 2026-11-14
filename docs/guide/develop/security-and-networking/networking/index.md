---
title: Networking
description: Serve a Function at a path and a host of its own with a Route, ask its callers to authenticate, and allow a Function's outbound connections with an EgressPolicy.
---

## Usage

A Route tells funcd's edge, the part of funcd that receives the calls from outside funcd, where to answer: each rule
of a Route matches a path and sends the requests on that path to a Function, to the files of a Bucket or to a Site. An
EgressPolicy works in the other direction: on a server that controls outbound connections, it lists where the
[workers](../../../platform/workloads/index.md#runtime) of a Function, the processes that run its code, may connect.
The [Platform page](../../../platform/security-and-networking/networking.md) explains how a call reaches a worker and
how a worker reaches the rest.

The first example, http-api, serves the Function `devices-api`, a web app that keeps a list of devices, at the path
`/api` through the Route `api`, then creates a device and lists the devices through the Route. It exists in
TypeScript only, since a Function that serves HTTP runs on Node.js.

Install the [dev build of funcdctl](../../../get-started/install.md), which includes the `funcdctl dev` command, Git,
curl and Node.js, and put Node.js on the `PATH`. `funcdctl dev` starts a local platform, applies the resource files of
one folder to it and serves the edge on the local computer. Its `--gport` flag sets the port of the edge, and
`--cport` the port of the control plane, which a `funcdctl` command reaches with `--server` and the token
`funcd-dev-token`.

### TypeScript

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/http-api --gport 8080 --cport 8081
```

`funcdctl dev` prints its banner once it has applied the folder's resource files, which can be before the edge serves
the Route. Until the edge serves it, a call through the Route gets the HTTP status 404 with the message
`no route for "/api/devices"`. In another terminal, read the Route's status, and repeat the command until the phase is
`Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get route api -n default -o json
```

The command prints the Route as JSON. Under `status`, `"phase"` is `"Ready"`, and the condition `Ready` has the
status `"True"` and the reason `Programmed`: the edge serves every rule of the Route. [Status](#status) explains the
other reasons.

The folder holds the Route `api`, in `examples/http-api/route.yaml`, the Function `devices-api`, in
[`devices.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/http-api/devices.yaml), its build
`devices-api.mjs`, which the repository holds already built, and a configuration file for a funcd server, which
`funcdctl dev` skips. [Serve HTTP requests](../../workloads/function/index.md#serve-http-requests) on the Function
page explains how `devices-api` answers HTTP requests. The Route `api` has one rule:

::include[../../../../../_repos/funcd-typescript/examples/http-api/route.yaml]

- Its [`path`](spec.md#routespecrules) is `/api`. It sets no [`pathType`](spec.md#routespecrules), so the type is
  `Prefix`: the rule matches `/api` and every path under it by whole segments, such as `/api/devices`, but not
  `/apix`.
- Its [`backend.function`](spec.md#routespecrulesbackend) names the Function that the rule calls, `devices-api`,
  which must be in the Route's namespace.
- The Route sets no [`host`](spec.md#routespec), so, in the namespace `default`, it answers on every host except the
  host of another namespace while that namespace uses it, as
  [Choose the host of a Route](#choose-the-host-of-a-route) explains.

The edge removes the rule's path from the request before it calls the Function: a request for `/api/devices` reaches
the app as `/devices`, and its `X-Forwarded-Prefix` header holds the part that the edge removed, `/api`. The Route
adds an address to the Function: its own address, `/function/devices-api`, keeps answering. Create a device through
the Route, and list the devices:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"truck-7"}' http://127.0.0.1:8080/api/devices
{"id":"1","name":"truck-7"}
$ curl -s http://127.0.0.1:8080/api/devices
[{"id":"1","name":"truck-7"}]
```

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Choose the host of a Route

A request names a host, which a browser or curl takes from the URL and sends in the request's `Host` header. A Route
answers on one host, which its [`host`](spec.md#routespec) sets:

- A Route that sets a host answers on that host only. funcd compares hosts in lower case, without a port and without
  a trailing dot, so a request for `Devices.Example.com:8080` matches the host `devices.example.com`.
- A Route that sets no host answers on the host of its namespace, `<namespace>.<server.baseDomain>`. With the default
  [`server.baseDomain`](../../running-funcd/spec.md#funcdconfigyaml), `localhost`, the Routes of the namespace `shop`
  that set no host answer on `shop.localhost`.
- The namespace `default` has no host of its own. A Route of `default` that sets no host answers on every host,
  except the host of another namespace, or a name under it, while that namespace has a Route there.

funcd does not serve a Route whose host is the host of another namespace, or a name under it, while that namespace has
a Route there. It never serves a Route with a rule at or under `/function` on such a host, whether the other namespace
has a Route there or not. In both cases, the Route is not Ready, with the reason `HostReserved`.

Two more rules apply to a Route of the namespace `default` that sets no host. It may not have a rule at or under
`/function`, the path at which the edge calls Functions by name (`ReservedPath`). It is not served at all when the
Namespace `default` sets [`defaultExposure`](../../tenancy-and-limits/namespaces/spec.md#namespacespec) to `explicit`
(`HostRequired`).

The Route `devices` serves the Function `devices-api` at the root of the host `devices.example.com`: a request for
`http://devices.example.com/devices` reaches the app as `/devices`, and a request on another host does not match the
Route. The complete file,
[api/types/v1alpha1/examples/networking/route.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/networking/route.yaml),
also holds the Function `devices-api`:

::include[../../../../../api/types/v1alpha1/examples/networking/route.yaml#route]

The Route's `status.host` names the host that it claims, `devices.example.com`. On the local computer, curl sends a
host other than the URL's with its `-H` option, such as `-H 'Host: devices.example.com'`.

A Function or a Site can also have an address of its own on a host or a path: its `spec.ingress`, on the
[Function](../../workloads/function/spec.md#functionspecingress) and the
[Site](../../workloads/site/spec.md#sitespecingress) Spec tabs, sets it, with the same rules for the host, and funcd
serves it through a Route that the Function or the Site owns.

## Choose which rule serves a request

The edge compares each request with the rules of every Route that answers on the request's host:

- A rule whose [`pathType`](spec.md#routespecrules) is `Prefix`, the default, matches its path and every path under
  it, by whole segments; a trailing slash changes nothing, and `/` matches every path. A rule whose `pathType` is
  `Exact` matches its path alone, and its backend receives the path `/`.
- A rule that lists [`methods`](spec.md#routespecrules) matches only those request methods; a rule without `methods`
  matches every method.
- Among the rules that match, the rule with the longest path serves the request, whichever Route holds it. When two
  paths have the same length, the rule of a Route that sets a host wins over the rule of a Route that sets none. A
  host decides nothing else, so in the namespace `default`, a rule without a host at `/api/v2` takes a request for
  `devices.example.com/api/v2/x` from a rule of the host `devices.example.com` at `/api`.

One exception holds on a host that names a namespace, `<namespace>.<server.baseDomain>` or a name under it: a request
for a path under `/function` goes to the Function of that name in that namespace, unless the rule that matches it lies
under `/function` itself and belongs to that namespace. A rule at `/`, of that namespace or of `default`, never takes
such a request there.

A request that no rule matches, and that is not addressed to a Function's own address, gets the HTTP status 404 with
the message `no route for "<path>"`. A request whose path, after the rule's path, lies under `/health/` also gets 404
and reaches no worker: funcd keeps those paths for its own checks of a worker.

Two rules conflict when their Routes claim the same host, the one that `status.host` shows, have the same path, a
trailing slash aside, and have a method in common. Only one Route then keeps that path: the first in this order.
Routes are ordered by the name of their namespace, in alphabetical order; within a namespace, the Routes that a
Function or a Site owns come after the others, and the Routes of each of these two groups are ordered by name. The
order in which the Routes were applied does not count, so a Route applied later whose name comes first takes the path
from a Route that served it. The other Route serves none of its rules: it is not Ready, with the reason
`RouteConflict` and a message that names the Route that keeps the path, such as
`conflicts with Route default/a-conflict on (host, path, method)`. Once the Route that keeps the path is deleted, the
other Route serves again.

A Route of the namespace `default` that sets no host claims no host, so its rules conflict only with the rules of the
other Routes of `default` that set none. Its rule and a rule of a Route with a host never conflict, even on the same
path: the length of the path, then the host, decide which of them serves a request, as the list above says.

## Ask callers to authenticate

A rule lets every caller in unless its mode asks for authentication. The mode `authenticated` in a Route's
[`auth`](spec.md#routespecauth) applies to every rule of the Route; in a rule's own
[`auth`](spec.md#routespecrules), it applies to that rule and wins over the Route's. A rule and a Route that set no
mode take the default of their Namespace,
[`spec.edgeDefaults.auth`](../../tenancy-and-limits/namespaces/spec.md#namespacespecedgedefaultsauth), and are open
when the Namespace sets none. The mode `open` lets anonymous callers in, even when the Namespace's default asks for
authentication.

What a call to an `authenticated` rule needs depends on the server setting
[`server.auth.edge`](../../running-funcd/spec.md#funcdconfigyaml):

- With `server.auth.edge` at its default, `false`, the edge has no authenticator, so every call to an authenticated
  rule gets the HTTP status 401 with the message `authentication required but no authenticator is configured`, with
  a token or without one. `funcdctl dev` behaves the same way.
- With `server.auth.edge` set to `true`, a call needs the token of one of the server's credentials, in an
  `Authorization: Bearer <token>` header or an `X-Api-Key` header, and that credential's role must allow it to get
  the Functions of the rule's namespace. A call without a valid token gets 401, and a call whose credential may not
  get those Functions gets 403; [Access](#access) names the roles. The Function receives the call without these two
  headers.

The mode of a Route covers only the requests through its rules. A Function's own address, `/function/<name>`, takes
the mode of the Function's [`spec.ingress.auth`](../../workloads/function/spec.md#functionspecingressauth), else the
default of its Namespace, else open, so beside an authenticated Route it stays open unless one of these asks for
authentication. The address answers only while it is on:
[`spec.ingress.enabled`](../../workloads/function/spec.md#functionspecingress) set to `false` turns it off, and so
does a Namespace whose `defaultExposure` is `explicit`, while `enabled` is not set. A host or a path in
`spec.ingress` replaces the address with a Route that the Function owns.

The env-echo example below exists in TypeScript only.

### TypeScript

The env-echo example's Route `secure-echo`, in `examples/env-echo/secure-route.yaml`, serves the Function `env-echo`
at `/secure` and asks its callers to authenticate:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/secure-route.yaml]

`env-echo` takes no input and returns three values of its configuration, as the
[Secrets](../../services/secrets/index.md) page explains. The folder also holds the Route `env-echo`, in
[`route.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/env-echo/route.yaml), which serves the same
Function at `/echo` and sets no mode. From the root folder of the funcd-typescript repository, cloned as in the first
example, start the example:

```console
$ funcdctl dev examples/env-echo --gport 8080 --cport 8081
```

In another terminal, read the status of the Route `secure-echo`, and repeat the command until the phase is `Ready`, as
in the first example:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get route secure-echo -n default -o json
```

Then call the Function through the Route, and at its own address. The calls send no body:

```console
$ curl -s -X POST http://127.0.0.1:8080/secure
{"type":"urn:funcd:problem:unauthorized","title":"Unauthorized","status":401,"detail":"dataplane.ServeHTTP: authentication required but no authenticator is configured"}
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

`funcdctl dev` has no authenticator, so the Route refuses every call. The Function's own address stays open, since
`env-echo` sets no `spec.ingress` and the namespace `default` sets no default mode.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Allow a Function's outbound connections

A Function's code opens an outbound connection when it calls a service outside funcd, such as an HTTPS API. Egress
control, which works only on Linux with the containerd runtime, takes these server settings, which the funcd
[Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

- `server.network.egress`, set to `true`, refuses the outbound traffic of the workers by default.
- `server.network.egressGatewayPort` is the port of the egress gateway, which receives the workers' outbound TCP
  connections and lets through the allowed ones. It is required when `server.network.egress` is `true`.
- `server.network.dnsForwarderPort` is the port of the DNS forwarder, which answers the workers' lookups, and
  `server.network.dnsResolver` names the resolver that the forwarder asks. While `dnsForwarderPort` is 0, its
  default, neither the forwarder nor the gateway runs, and nothing goes out, not even a lookup.

The egress-probe example's
[`funcdconfig.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/egress-probe/funcdconfig.yaml)
sets all four. With egress control on, funcd refuses each outbound connection of a Function's workers that nothing
allows: a Function that no EgressPolicy covers, and that no Policy allows, as [Access](#access) explains, reaches
nothing outside funcd. With `server.network.egress` at its default, `false`, on another system or runtime, and in
`funcdctl dev`, workers connect to every address, and an EgressPolicy changes nothing.

An EgressPolicy holds [`rules`](spec.md#egresspolicyspec). Each rule allows outbound TCP connections to the
destinations of its [`to`](spec.md#egresspolicyspecrules), on its [`ports`](spec.md#egresspolicyspecrules), or on
every port when it lists none:

- [`domains`](spec.md#egresspolicyspecrulesto) lists host names: exact, such as `api.example.com`, or a wildcard,
  such as `*.example.com`, which matches every name under `example.com` but not `example.com` itself.
- [`cidrs`](spec.md#egresspolicyspecrulesto) lists address ranges, such as `10.0.0.0/8`.

The policy's [`appliesTo`](spec.md#egresspolicyspec) names the Functions of the namespace that its rules apply to;
without `appliesTo`, they apply to every Function of the namespace. The rules of every EgressPolicy of a namespace add
up: a connection is allowed when one rule of one of them allows it, unless a Policy forbids it.

With egress control on, a worker looks names up through funcd's DNS forwarder, which answers only the names that a
domain rule applying to the Function allows. It refuses the other names, which most libraries report as a temporary
name-resolution error. A domain rule allows a connection only to an address that the worker's own lookup of the name
returned, until that answer expires. Two consequences follow:

- A host that a CIDR rule allows needs a domain rule too, to be reached by name; reached by its address, it needs
  nothing more.
- A domain rule never allows an address that is not globally reachable, such as a private address, even when the
  lookup returns one: such an address needs a CIDR rule that holds it.

A worker that has just started can neither connect nor look a name up until funcd has registered it, within
`server.network.workerSyncInterval`, 2 seconds by default. A refused connection opens, then ends once funcd has
decided: on the ports 80 and 8080, it gets an HTTP answer with
the status 403 and the message `egress denied by funcd EgressPolicy`, and on any other port, it is closed. funcd writes
each decision to its own log, not to the Function's logs, as a record whose message is `egress decision`, with the
namespace, the Function, the domain, the address, the port, `allowed` and the reason. A refused lookup gives a record
`egress lookup refused`, at most one per worker every 10 seconds. The server's
[`log.format`](../../running-funcd/spec.md#funcdconfigyaml) sets how a record reads: with the default, `json`, a
refused connection's record holds `"allowed":false`, and with `text`, `allowed=false`.

The egress-probe example below exists in TypeScript only.

### TypeScript

The egress-probe example shows both kinds of rule. Its Function `egress-probe`, in
[`function.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/egress-probe/function.yaml), declares
nothing about outbound connections. Its handler, in
[`src/main.ts`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/egress-probe/src/main.ts), calls `fetch`
with the `url` of the call's input, such as `{"url":"https://www.example.com"}`, and returns `ok`, which is `true`
when the request completed. The results below hold on a platform with egress control on. While no EgressPolicy
applies to the Function, a call for `https://example.com` returns `"ok":false`.

The first EgressPolicy, in `examples/egress-probe/fixtures/egresspolicy-wildcard.yaml`, allows HTTPS, on the port 443,
to every name under `example.com`, for every Function of the namespace:

::include[../../../../../_repos/funcd-typescript/examples/egress-probe/fixtures/egresspolicy-wildcard.yaml]

Once `funcdctl apply -f examples/egress-probe/fixtures/egresspolicy-wildcard.yaml` has applied it on such a platform,
a call for `https://www.example.com` returns `"ok":true`, and the worker's lookup of `www.example.org` is refused. The
second EgressPolicy, in `examples/egress-probe/fixtures/egresspolicy-cidr.yaml`, allows the port 443 at the address
`1.1.1.1`:

::include[../../../../../_repos/funcd-typescript/examples/egress-probe/fixtures/egresspolicy-cidr.yaml]

With both applied, a call for a URL at `https://1.1.1.1` returns `"ok":true`, since the URL names the address and
needs no lookup, and a call for `https://1.0.0.1`, an address that no rule holds, returns `"ok":false`. Each refused
connection leaves an `egress decision` record whose `allowed` is false in funcd's log. Under `funcdctl dev`, which
controls no outbound connection, each of these calls returns `"ok":true` when the local computer can reach the
address, with or without the EgressPolicies.

## Access

A Function's outbound connections and the calls through the edge are checked by two different authorizers, as the
[Security](../../../platform/security-and-networking/security.md) page explains.

- **The outbound connections of a Function.** A Function gets no outbound connection by itself: nothing in its spec,
  no binding and no ownership allows one, and funcd supplies no built-in policy for them. With egress control on,
  funcd asks Cedar about each connection, for the action `egress::connect`. A connection needs a `permit` of that
  action: funcd turns each EgressPolicy rule that applies to the Function into one, and a
  [Policy](../security/spec.md#policy) of the namespace can hold one too. The lookup of a name still needs an
  EgressPolicy domain rule, since the DNS forwarder reads only those. A `forbid` statement in a Policy overrides every
  `permit`, so it stops a connection that an EgressPolicy allows, but not the lookup of the name.
- **The callers of an authenticated rule.** With `server.auth.edge` set to `true`, the caller's token must belong to
  one of the server's credentials, which `auth.token` and
  [`auth.credentials`](../../running-funcd/spec.md#funcdconfigyaml) set, and the credential's role decides: `admin`,
  `developer` or `viewer`. Each role may get the Functions of the namespaces in which it acts, and `admin` acts in
  every namespace, so `viewer`, which only reads, is the smallest role that lets a caller through. A caller whose
  credential does not act in the rule's namespace gets 403, with a message such as
  `not authorized to invoke default/env-echo: viewer not scoped to namespace "default"`.

## Limits and quotas

- An EgressPolicy holds 1 to 64 rules. Each rule names at most 64 domains and CIDRs together, and at most 32 ports,
  each from 1 to 65535. `funcdctl apply` refuses a policy beyond these limits.
- A call to a Function through the edge, through a Route or at the Function's own address, holds a body of at most
  1 MiB, a cap that no setting changes: a larger body gets the HTTP status 413.
- [`server.limits`](../../running-funcd/spec.md#funcdconfigyaml) sets the other limits of the edge, which apply to
  every call through a Route: a request rate, a cap on the calls in flight and a cap of its own on the size of a
  request's body, each off by default. The Function page lists the
  [limits of a call to a Function](../../workloads/function/index.md#limits-and-quotas).

This page's [Spec](spec.md) tab gives the range of each field.

## Status

`funcdctl get route <name> -n <namespace> -o json` prints a Route's status, as the first example shows. A Route serves
all its rules or none, so one rule at fault keeps the whole Route off. The condition `Ready` explains the phase:

| Phase | Reason | Meaning |
| --- | --- | --- |
| `Ready` | `Programmed` | The edge serves every rule of the Route. |
| `Pending` | `BackendNotFound` | The Function that a rule names does not exist in the Route's namespace, with a message such as `backend function "nope" not found`. funcd checks again every `controller.referentPollInterval`, 2 seconds by default, so the Route serves soon after the Function exists. |
| `Pending` | `BucketNotFound` | The Bucket that a rule names does not exist in the Route's namespace. funcd checks again every `controller.referentPollInterval`. |
| `Pending` | `SiteNotFound` | The Site that a rule names does not exist in the Route's namespace. funcd checks again every `controller.referentPollInterval`. |
| `Pending` | `SiteNotReady` | The Site that a rule names serves nothing yet. funcd checks again every `controller.referentPollInterval`. |
| `Pending` | `Disabled` | The Route's [`enabled`](spec.md#routespec) is `false`, or the Route is one that a Function or a Site owns in a namespace whose `defaultExposure` is `explicit`, and its `enabled` is not set. It leaves its paths to the other Routes. |
| `Pending` | `HostReserved`, `ReservedPath` or `HostRequired` | The Route's host or a rule's path breaks a rule of [Choose the host of a Route](#choose-the-host-of-a-route). |
| `Pending` | `RouteConflict` | Another Route keeps one of the Route's paths, as [Choose which rule serves a request](#choose-which-rule-serves-a-request) explains. |
| `Pending` | `EdgeStarting` | The edge holds a Route that a Function or a Site owns until it has loaded its table of Routes. Repeat the check. |
| `Pending` | Another reason | Read the condition's reason and message in the same output. |

`status.host` names the host that the Route claims. It is empty for a Route of the namespace `default` that sets no
host, and while a backend is missing.

An EgressPolicy has no status. `funcdctl get egresspolicy -n <namespace>` lists the EgressPolicies of a namespace, and
the `egress decision` records in funcd's log show what they allow.

## Changes and deletion

- A change to a Route reaches the edge once funcd has processed it: the condition `Ready` then shows the Route's new
  `metadata.generation` as its `observedGeneration`.
- Deleting a Route removes its rules from the edge at once: a request on its paths then gets 404, unless another rule
  matches it. A Route that lost a conflict to the deleted Route serves again.
- Deleting a Function also deletes the Route that funcd wrote for its `spec.ingress`, but not the other Routes that
  call the Function. funcd checks those Routes every `controller.routeResyncInterval`, 10 seconds by default, so
  within that time each of them reports `BackendNotFound` and serves none of its rules. Each serves again once a
  Function of that name exists.
- A change to an EgressPolicy, or its deletion, applies to the next connection and the next lookup of the namespace's
  workers, with no restart.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes `controller.routeResyncInterval` and
`controller.referentPollInterval`.

## CLI errors

These errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js for the examples' Functions. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `manifest is invalid (<kind> "<name>")`, followed by the field and the rule it breaks, such as `spec.rules[0].path "api" must begin with '/'` | `funcdctl apply` checks each Route and EgressPolicy before it sends it. Correct the field that the message names, as the [Spec](spec.md) tab describes it. |
| `manifest is invalid`, followed by `spec.rules[0].backend must set exactly one of function, static or site` | Give the rule one backend. |
| `manifest is invalid`, followed by `spec.rules[0].to must set at least one of domains/cidrs` | List a domain or a CIDR in the rule's `to`. |
| `manifest is invalid`, followed by `spec.rules[0].to.domains "*" is not a valid domain or "*."-wildcard` | Write an exact host name, or a wildcard that starts with `*.`. |
| `<file>: <kind> "<name>" is in namespace "<namespace>"; funcdctl dev runs namespace default only` | Remove `metadata.namespace` from the object, set it to `default`, or apply the file to a funcd server. |

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for Routes and EgressPolicies:

- It serves the Routes of its folder on its edge, at the port that `--gport` sets. It runs the namespace `default`
  only, and stops on a resource of another namespace, so a Route that sets no host answers on every host.
- Its edge has no authenticator, as on a server whose `server.auth.edge` is `false`, so every call to a rule whose
  mode is `authenticated` gets 401.
- It controls no outbound connection: its workers run as processes of the local user, and they connect to every
  address. It applies an EgressPolicy, which `funcdctl get egresspolicy` lists, but the policy changes nothing, and
  its banner ends with a warning that begins `⚠ egress is NOT isolated in dev`.

`funcdctl dev` keeps its state in memory, where it is lost when `funcdctl dev` stops, except a record of the processes
that it starts, so that it can stop them after a crash, in `funcd/dev/<id>/process` under the user's cache folder,
`<id>` naming the project folder. With the `--persist` flag, it keeps its state between runs in `.funcd-dev`, in the
current folder, or in the folder that `--persist-to` names: the Routes, the EgressPolicies and the other objects that
it applied, the data of its services, and the copies of the builds that it runs. To start again from nothing, delete
that folder after `funcdctl dev` stops. The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev)
lists its flags.

## Examples

- [http-api in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/http-api): a Route in front of
  a Function that serves HTTP.
- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): an open Route and a
  Route that asks its callers to authenticate, in front of the same Function.
- [egress-probe in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/egress-probe): a Function
  that reports whether an outbound connection succeeds, and two EgressPolicies in its `fixtures` folder.
