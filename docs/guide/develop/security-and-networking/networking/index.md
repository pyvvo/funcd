---
title: Networking
description: Serve a Function at a path and a host of its own with a Route, ask its callers to authenticate, and allow a Function's outbound connections with an EgressPolicy.
---

## Usage

A **Route** tells funcd's [edge](../../../platform/security-and-networking/networking.md), which receives the calls
from outside funcd, where to answer: each rule sends the requests on a path to a Function, a Bucket's files or a Site.
An **EgressPolicy** lists where a Function's workers may connect, on a server that controls outbound connections.

The TypeScript example http-api serves the Function `devices-api` at `/api`. Install the
[dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`.

### TypeScript

Clone the repository and start the example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/http-api --gport 8080 --cport 8081
```

Until the edge serves the Route, a call gets 404 `no route for "/api/devices"`. In another terminal, repeat until the
Route's phase is `Ready`, with the reason `Programmed`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get route api -n default -o json
```

The Route, in `route.yaml`; [Serve HTTP requests](../../workloads/function/index.md#serve-http-requests) explains the
Function:

::include[../../../../../_repos/funcd-typescript/examples/http-api/route.yaml]

A rule without a [`pathType`](spec.md#routespecrules) is a `Prefix`: `/api` matches `/api/devices` but not `/apix`.
[`backend.function`](spec.md#routespecrulesbackend) names a Function of the Route's namespace. The edge removes the
rule's path, so the app gets `/devices`, with `/api` in `X-Forwarded-Prefix`; the Function's own address,
`/function/devices-api`, keeps answering. Create a device and list them:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"name":"truck-7"}' http://127.0.0.1:8080/api/devices
{"id":"1","name":"truck-7"}
$ curl -s http://127.0.0.1:8080/api/devices
[{"id":"1","name":"truck-7"}]
```

Press Ctrl-C in the first terminal to stop.

## Choose the host of a Route

A Route answers on the host that its [`host`](spec.md#routespec) sets, compared in lower case without a port or a
trailing dot. Without a host, it answers on its namespace's host, `<namespace>.<server.baseDomain>`, such as
`shop.localhost`; a Route of `default`, which has no host, answers on every host except another namespace's while that
namespace has a Route there.

A Route is not served, and not Ready:

- on another namespace's host, or a name under it, while that namespace has a Route there, or with a rule under
  `/function` on such a host (`HostReserved`);
- in `default` without a host, with a rule under `/function` (`ReservedPath`), or when the Namespace `default` sets
  [`defaultExposure: explicit`](../../tenancy-and-limits/namespaces/spec.md#namespacespec) (`HostRequired`).

This Route serves `devices-api` at the root of `devices.example.com`
([complete file](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/networking/route.yaml)); locally,
send that host with `-H 'Host: devices.example.com'`:

::include[../../../../../api/types/v1alpha1/examples/networking/route.yaml#route]

`status.host` names the host that a Route claims. A [Function's](../../workloads/function/spec.md#functionspecingress) or a
[Site's](../../workloads/site/spec.md#sitespecingress) `spec.ingress` host or path makes a Route that it owns, by the
same rules.

## Choose which rule serves a request

- A `Prefix` rule matches its path and every path under it by whole segments, `/` matching all; an `Exact` rule only
  its path, and its backend gets `/`. A rule with [`methods`](spec.md#routespecrules) matches only those.
- Among the matching rules, the longest path wins, whatever Route holds it; at equal length, a Route with a host wins.
- On a namespace's host, a path under `/function` goes to that namespace's Function, unless a rule of that namespace
  under `/function` matches.
- No match gets 404 `no route for "<path>"`. A path under `/health/` after the rule's path gets 404, reserved for
  funcd's worker checks.

Two rules conflict on the same claimed host, the same path and a common method. The first Route by namespace name, then
by name, with owned Routes after the others, keeps the path, whatever the apply order; the other serves nothing, with
`RouteConflict` and a message such as `conflicts with Route default/a-conflict on (host, path, method)`, until the
winner is deleted. A hostless Route of `default` conflicts only with other hostless Routes of `default`.

## Ask callers to authenticate

A Route's [`auth`](spec.md#routespecauth) mode applies to its rules, and a rule's own [`auth`](spec.md#routespecrules)
wins; without either, the Namespace's
[`edgeDefaults.auth`](../../tenancy-and-limits/namespaces/spec.md#namespacespecedgedefaultsauth) applies, else `open`.
An `authenticated` rule:

- answers 401 `authentication required but no authenticator is configured` while
  [`server.auth.edge`](../../running-funcd/spec.md#funcdconfigyaml) is `false`, its default, and under `funcdctl dev`;
- otherwise needs a server credential's token in `Authorization: Bearer` or `X-Api-Key`, whose role may get the
  namespace's Functions: else 401, or 403. The Function gets the call without those headers.

The `oidc` mode signs callers in with an OpenID Connect provider instead: [OIDC sign-in](../sign-in/index.md).

A Route's mode covers only its rules: a Function's own address takes its
[`spec.ingress.auth`](../../workloads/function/spec.md#functionspecingressauth), else the Namespace's default, else
`open`, and answers only while its address is on.

### TypeScript

The env-echo example's Route `secure-echo`, in `secure-route.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/env-echo/secure-route.yaml]

From the funcd-typescript repository, start the example:

```console
$ funcdctl dev examples/env-echo --gport 8080 --cport 8081
```

In another terminal, wait until the Route is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get route secure-echo -n default -o json
```

Then call through the Route, and at the Function's own address:

```console
$ curl -s -X POST http://127.0.0.1:8080/secure
{"type":"urn:funcd:problem:unauthorized","title":"Unauthorized","status":401,"detail":"dataplane.ServeHTTP: authentication required but no authenticator is configured"}
$ curl -s -X POST http://127.0.0.1:8080/function/env-echo
{"config":"prod","secret":"s3cr3t","shared":"from-secret"}
```

Press Ctrl-C in the first terminal to stop.

## Allow a Function's outbound connections

Egress control works on Linux with the containerd runtime. Turn it on with these
[server settings](../../running-funcd/spec.md#funcdconfigyaml), as the egress-probe example's
[`funcdconfig.yaml`](https://github.com/pyvvo/funcd-typescript/blob/main/examples/egress-probe/funcdconfig.yaml) does:
`server.network.egress: true`, `server.network.egressGatewayPort`, `server.network.dnsForwarderPort` and
`server.network.dnsResolver`. With it on, a Function or an add-on engine reaches nothing outside funcd unless an
EgressPolicy or a Policy allows it. Without it on containerd, a Function's workers connect anywhere and an add-on
engine nowhere. Under `funcdctl dev`, workers and engines both connect anywhere.

An EgressPolicy's [`rules`](spec.md#egresspolicyspec) allow TCP connections to their [`to`](spec.md#egresspolicyspecrules)
on their `ports`, or on every port:

- [`domains`](spec.md#egresspolicyspecrulesto): exact names, or wildcards such as `*.example.com`, which do not match
  `example.com` itself;
- [`cidrs`](spec.md#egresspolicyspecrulesto): address ranges, such as `10.0.0.0/8`.

[`appliesTo`](spec.md#egresspolicyspec) names the Functions, Jobs and add-on objects it applies to, each by its kind and
name, such as `{kind: Job, name: tofu}`, or `{kind: Tunnel, name: public}` for the engine of an add-on object, its kind
spelled as its Addon spells it. funcd refuses with 400 a kind that no Addon registers for objects that run an engine,
such as a settings kind. Without `appliesTo`, the rules apply to all of the namespace's Functions and to no Job or
engine. The rules of
all its EgressPolicies add up. A worker can look up only the names that a domain rule allows, and a domain rule allows only the
addresses that the lookup returned, never a private one. So a CIDR host reached by name also needs a domain rule, and a
private address needs a CIDR rule.

A new worker can connect about 2 seconds after it starts. A refused connection on the ports 80 or 8080 gets 403
`egress denied by funcd EgressPolicy`, and is closed on others. funcd's own log records each decision as
`egress decision`, with `allowed`.

### TypeScript

The egress-probe Function fetches the `url` of its input and returns `ok`. With egress control on and no
EgressPolicy, `https://example.com` returns `"ok":false`. This EgressPolicy allows port 443 to every name under
`example.com`:

::include[../../../../../_repos/funcd-typescript/examples/egress-probe/fixtures/egresspolicy-wildcard.yaml]

Applied, `https://www.example.com` returns `"ok":true`, and a lookup of `www.example.org` is refused. This one allows
port 443 at `1.1.1.1`, which a URL can name without a lookup; `https://1.0.0.1` stays refused:

::include[../../../../../_repos/funcd-typescript/examples/egress-probe/fixtures/egresspolicy-cidr.yaml]

## Access

- **Outbound connections.** A Function or an add-on engine gets none by itself. Each connection needs a `permit` of
  `egress::connect`:
  funcd makes one from each EgressPolicy rule, and a [Policy](../security/spec.md#policy) can hold one, but a lookup
  still needs a domain rule. A Policy's `forbid` stops a connection, not the lookup.
- **A workload's calls to the edge.** With egress control on, a worker or an add-on engine reaches funcd's edge
  directly at funcd's bridge address, `10.63.0.1` by default, on the data plane's port; a configured address needs a
  fixed port in `server.dataPlaneAddr`. There it gets only its own namespace's hosts, Functions by name and sign-in
  callback: another namespace's, or an `X-Funcd-Namespace` header that names one, gets 403
  `outside the caller's namespace`. Any other way to it, such as a node address at that port, is refused whatever an
  EgressPolicy allows.
- **Callers of an authenticated rule.** With `server.auth.edge: true`, the token's credential decides, from
  [`auth.credentials`](../../running-funcd/spec.md#funcdconfigyaml): `viewer` is the smallest role that passes in its
  namespaces, and another namespace gets 403, such as
  `not authorized to invoke default/env-echo: viewer not scoped to namespace "default"`.

## Limits and quotas

- An EgressPolicy holds 1 to 64 rules, each with at most 64 domains and CIDRs and 32 ports.
- A call through the edge carries at most 1 MiB, or gets 413.
- [`server.limits`](../../running-funcd/spec.md#funcdconfigyaml) sets the edge's rate, in-flight and body limits, off
  by default; see also the [Function's limits](../../workloads/function/index.md#limits-and-quotas).

## Status

`funcdctl get route <name> -n <namespace> -o json` prints a Route's status. A Route serves all its rules or none:

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
| `Pending` | Another reason | Read the condition's reason and message in the same output. |

An EgressPolicy has no status; `funcdctl get egresspolicy -n <namespace>` lists them.

## Changes and deletion

- A Route change is live once `Ready`'s `observedGeneration` shows the new generation.
- Deleting a Route removes its rules at once, and a Route that lost a conflict to it serves again.
- Deleting a Function deletes its `spec.ingress` Route; other Routes to it turn `BackendNotFound` within
  `controller.routeResyncInterval`, 10 seconds by default, and serve again when the Function returns.
- An EgressPolicy change applies to the next connection and lookup.

## CLI errors

These errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js for the examples' Functions. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. With Docker or nerdctl, `funcdctl dev --containers` needs no Node.js. |
| `manifest is invalid (<kind> "<name>")`, followed by the field and the rule it breaks, such as `spec.rules[0].path "api" must begin with '/'` | `funcdctl apply` checks each Route and EgressPolicy before it sends it. Correct the field that the message names, as the [Spec](spec.md) tab describes it. |
| `manifest is invalid`, followed by `spec.rules[0].backend must set exactly one of function, static or site` | Give the rule one backend. |
| `manifest is invalid`, followed by `spec.rules[0].to must set at least one of domains/cidrs` | List a domain or a CIDR in the rule's `to`. |
| `manifest is invalid`, followed by `spec.rules[0].to.domains "*" is not a valid domain or "*."-wildcard` | Write an exact host name, or a wildcard that starts with `*.`. |
| `<file>: <kind> "<name>" is in namespace "<namespace>"; funcdctl dev runs namespace default only` | Remove `metadata.namespace` from the object, set it to `default`, or apply the file to a funcd server. |

## Run it locally

- `funcdctl dev` serves its folder's Routes on `--gport`, in the namespace `default` only, so a hostless Route answers
  on every host. It listens on 127.0.0.1, unless [`--lan`](../../tools/funcdctl/index.md) puts the gateway on the
  machine's network addresses as well.
- Its edge has no authenticator, so `authenticated` rules answer 401.
- It controls no outbound connection: an EgressPolicy is applied but changes nothing, and the banner warns
  `⚠ egress is NOT isolated in dev`.
- Its state stays in memory unless [`--persist`](../../tools/funcdctl/spec.md#funcdctl-dev) keeps it in `.funcd-dev`.

## Examples

- [http-api in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/http-api): a Route in front of
  a Function that serves HTTP.
- [env-echo in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/env-echo): an open and an
  authenticated Route to one Function.
- [egress-probe in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/egress-probe): a Function
  that tests outbound connections, with two EgressPolicies.
