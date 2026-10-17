# ADR-0240: Namespace hosts

- **Status**: Accepted (2026-10-17), after two judge rounds; the decider confirmed the rules of Decisions 1, 4 and 6.
- **Date**: 2026-10-17
- **Deciders**: green-0-rabbit
- **Tags**: edge, routing, namespace, multi-tenancy
- **Realizes**: [FEAT-0006/F153](../feat/0006-feat-ingress-hardening.md) (namespace hosts)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0240` back-link:
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) Decisions 1, 3, 5, 6.2 and 6.3 (the empty host,
  `HostRequired`, the order on a reserved host, a hit under `/function`, the by-name namespace) ·
  [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) Decisions 1, 2, 4 and 5 ·
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) Decision 2, discharging its namespace-addressing workaround
  and open question · [ADR-0164](0164-rate-limit-per-target.md) Decision 2 (the rate key's namespace) ·
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) Decision 4 · [ADR-0140](0140-path-mounted-site.md)
  Decisions 2 and 7 · the `SiteIngress.Host` comment of [ADR-0139](0139-site-declarative-static-web-app.md) and
  ADR-0140.
- **Relates to**: [ADR-0111](0111-tls-termination-certmagic.md) (certified hosts) ·
  [ADR-0239](0239-site-runtime-configuration.md) and #1034 (one host per SPA Site)

## Context & Need

One data-plane listener serves every namespace. An edge entry without a host (a Route, a Site's own Route, a catalog
ingress) matches every host (`internal/edge/router/router.go:35`), and `/function/<name>` takes its namespace from the
`X-Funcd-Namespace` header, `default` when absent (`internal/dataplane/dataplane.go:56-57`, `:131-134`). So:

- host-less entries of two namespaces on one path clash: the earlier (namespace, name) wins and the other is
  `RouteConflict` (ADR-0110 Decision 3(b));
- the host-less pages of every namespace share one browser origin, so one namespace's page reads another's cookies
  and storage;
- an `explicit` namespace must invent a host for every Route (`HostRequired`).

This ADR gives every namespace except `default` a host of its own, `<namespace>.<baseDomain>`, and serves its
host-less entries there: a namespace's apps get distinct URLs and their own origin, and nobody picks a host.

## Scenarios

`server.baseDomain` is `localhost` unless a scenario sets it.

- `namespace-host-function` — *Given* a Function `fleet-api` in `staging` and one in `default`, *Then*
  `staging.localhost/function/fleet-api` and `api.staging.localhost/function/fleet-api` reach staging's, and
  `localhost/function/fleet-api` reaches default's.
- `namespace-host-routes` — *Given* host-less Routes claiming `GET /api` in `staging` and in `prod`, *Then* both are
  Ready and answer on `staging.localhost` and `prod.localhost`; neither is `RouteConflict`.
- `namespace-host-site` — *Given* a host-less Site `fleet` in `staging` and nothing in `default`, *Then*
  `staging.localhost/site/fleet/` serves it and `localhost/site/fleet/` answers `404`.
- `namespace-host-default` — *Given* a host-less Route in `default`, *Then* it answers on `localhost`, `127.0.0.1` and
  `fleet.localhost`, as before; *Given* no namespace `fleet`, *Then* `fleet.localhost/function/<name>` answers `404`,
  with or without `X-Funcd-Namespace: default`.
- `namespace-host-isolated` — *Given* `staging` serving a host-less Route at `/api` and `default` host-less Routes at
  `/` and `/api/v2`, *Then* `staging.localhost/api` and `staging.localhost/api/v2` reach staging's backend, and
  `staging.localhost/` and `x.staging.localhost/` answer `404`.
- `namespace-host-function-path` — *Given* `staging` with a Function `fleet-api` and no edge entry, and a `prod` Route
  with host `staging.localhost` at `/`, *Then* that Route is Ready, `staging.localhost/` reaches prod's backend and
  `staging.localhost/function/fleet-api` reaches staging's Function; *Given* a `prod` Route with that host and a rule
  under `/function/`, *Then* it is `Ready=False` `HostReserved`.
- `namespace-host-explicit-mode` — *Given* an `explicit` namespace `staging`, *Then* its host-less Route is Ready on
  `staging.localhost` and `staging.localhost/function/<name>` answers `404` without waking anything; *Given* an
  `explicit` `default`, *Then* its host-less Route is still `HostRequired`.
- `namespace-host-reserved` — *Given* `staging` serving a host-less entry, *When* a Route in `prod` claims host
  `staging.localhost` or `api.staging.localhost`, *Then* that Route is `Ready=False` `HostReserved` and staging keeps
  serving; *When* staging's last entry there goes, *Then* the `prod` Route turns Ready. *Given* no namespace serving on
  `fleet.localhost`, *Then* a Route in `default` with that host is Ready.
- `namespace-host-status` — *Given* a host-less Route in `staging`, one in `staging` with host `fleet.example.com` and
  a host-less Route in `default`, *Then* they report `status.host` `staging.localhost`, `fleet.example.com` and none,
  and the second answers on `fleet.example.com` only.
- `namespace-host-header` — *Given* `X-Funcd-Namespace: staging`, *Then* `localhost/function/fleet-api` reaches
  staging's function and `prod.localhost/function/fleet-api` reaches prod's.
- `namespace-host-base-domain` — *Given* `server.baseDomain: apps.example.com`, *Then* staging's host-less entries
  answer on `staging.apps.example.com`; *Given* a base domain with a port, an empty label, an upper-case letter, an IP
  address or over 189 characters, or TLS enabled in `acme` mode with the default base domain, *Then* funcd refuses
  to start.

## Scope

**In**: the base-domain setting; the host of every host-less edge entry; which namespace reserves a namespace host;
the namespace of `/function/<name>`; `HostRequired` in `explicit` namespaces; `status.host` on a Route.

**Out**: a host per app; certificates for hosts that appear after start; DNS records; each kind's exposure model
(ingress shape, owned Routes, precedence), the next ADR; a Route backend that names a Site (#1033).

## Constraints & Decision drivers

- One listener and one replace-all table for every namespace (ADR-0110 Decision 5); the host is the tenant
  discriminator (Decision 3).
- `default` keeps its URLs on the base domain itself and on every host outside it.
- `localhost` names are reserved for loopback (RFC 6761 §6.3), so a local namespace host needs no DNS record.
- A wildcard certificate matches one label (RFC 9525 §6.3), so `*.<baseDomain>` can cover every namespace host.
- funcd is 0.x: a clean switch, no second code path.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A host per namespace for every host-less entry** | One origin per namespace; no clash across namespaces; one rule for every kind | Host-less URLs of non-default namespaces move; a server needs wildcard DNS for the base domain | **Chosen** |
| B. Move only `/function/<name>` and `/site/<name>` | Smaller change | Host-less Routes and catalog ingress still share one origin and still clash | Rejected |
| C. The namespace in the path (`/function/<ns>/<name>`) | No DNS | One origin for every namespace; every kind's path changes | Rejected |
| D. A host per app (`<name>.<ns>.<domain>`, Knative's default) | Every app at a root | Two labels need a wildcard certificate per namespace; a Site and a Function of one name share a host | Rejected; an explicit host or a Route does it (#1034) |
| E. Every name under the base domain belongs to the namespace of its label | Simple ownership | `fleet.localhost` unusable in `default`; a server needs a dedicated base domain | Rejected by the decider |
| F. No reservation | Simplest | Another namespace can take a namespace's host and read its cookies | Rejected |
| G. `default`'s host-less entries answer on reserved hosts too | "Every host" stays literal | `default`'s pages run in a namespace's origin: they fill the paths it does not serve, and a longer path beats its own | Rejected |
| H. A config switch keeping the old behaviour for one release | Smoother upgrade | Two code paths | Rejected |
| I. A namespace's Functions reserve its host too | Also ends co-residence beside its Functions | A new aggregator input (which namespaces have a Function); a namespace with any Function reserves its host, close to E | Rejected; Decision 6 protects `/function/` instead |

## Decision

1. **Base domain.** `server.baseDomain` (env `FUNCD_BASE_DOMAIN`), default `localhost`: lowercase DNS labels joined by
   dots, not an IP address, at most 189 characters (so `<namespace>.<base>` fits DNS's 253), with no port and no
   trailing dot. With TLS enabled in `acme` mode, `localhost` and names under it are refused too: no ACME CA
   certifies them. A refused value fails startup with `Invalid`.
2. **Namespace host.** `NamespaceHost(ns, base)` is `<ns>.<base>` for every namespace except `default`, which has none.
   A namespace name is a DNS label (`api/types/v1alpha1/ids.go:66`), so the host is valid.
3. **Host-less entries.** Before any claim rule, the edge aggregator gives every entry with an empty host its
   namespace host. This covers every source — Routes, a Site's own Route (`/site/<name>` and `ingress.path` mounts),
   catalog ingress — and changes none of them: they still send host-less entries. `default`'s entries stay host-less.
4. **Reserved hosts.** A namespace reserves its namespace host while one of its stored entries is host-less or names
   that host or a name under it; a Function reached by name does not count. While a namespace reserves its host:
   - an entry of any other namespace on that host or a name under it gets `HostReserved` and is not programmed,
     whatever the (namespace, name) order;
   - `default`'s host-less entries do not answer there.

   A namespace host that no namespace reserves is an ordinary host, except for `/function` (Decision 6).
5. **The rule sequence** (ADR-0176 Decision 2) becomes `ReservedPath`, else `HostRequired`, else `HostReserved`, else
   `EdgeStarting`, else `RouteConflict`, else programmed. `ReservedPath` and `HostRequired` apply only to entries
   still host-less after Decision 3, that is `default`'s. So an `explicit` namespace's host-less Route is served on its
   namespace host, and a namespace may claim `/function/` on its own host, as any entry with a host may today.
   `explicit` still closes `/function/<name>` for its namespace.
6. **`/function` on a namespace host.** For a public request, `NamespaceOfHost(host, base)` is the label just before
   the base domain, so `staging.localhost` and `api.staging.localhost` both name `staging`; it is empty for `default`,
   for the base domain, for a host outside it and for a label that is not a valid `NamespaceName` (`ids.go:66`). On a
   host that names a namespace, `/function` and every path under it belong to that namespace, reserved or not: another
   namespace's entry with a rule there (its cleaned path, as for `ReservedPath`) gets `HostReserved`, and the data
   plane serves the request by name in that namespace, ignoring an edge match of any other namespace (a `/` rule
   matches every path, `router.go:185`), with `X-Funcd-Namespace` ignored. On any other host, and for an internal
   fn-to-fn call, the header picks the namespace, `default` when absent. The exposure gate and the rate key
   (ADR-0164) use that namespace.
7. **`status.host`.** A verdict carries the host its entry claims, whatever its reason, and the Route reconciler
   writes it to `RouteStatus.Host`: `spec.host`, else the namespace host, else empty (any host but reserved ones).
   `Ready` tells whether the Route answers there; a Route whose backend is missing reaches no verdict and reports
   none. A Site's own Route carries it, so `funcdctl get route <site>` shows where the Site answers.
8. **TLS.** The TLS provider certifies `server.tls.hosts` and `Router.Hosts()` once, at start (ADR-0111;
   `pkg/funcd/funcd.go:1743`, after the Route table loads at `:1605`): a namespace host that a Route uses at start is
   certified, a later one at the next start, and one used only by a catalog ingress (it programs after start) or by
   Functions by name never. A `server.tls.hosts` line covers any of them, and so does a wildcard over a base of two
   or more labels: `*.<baseDomain>` in `server.tls.hosts` under `selfsigned`
   (`internal/edge/tls/static/static.go:158-167`), or a provided certificate. Many browsers reject `*.localhost`, so
   a local setup lists each host.
9. **Clean switch.** On upgrade, the host-less entries of non-default namespaces move to their namespace host and
   `default`'s stop answering on reserved hosts. On a host under the base domain, `/function/<name>` names that host's
   namespace and another namespace's rule under `/function` turns `HostReserved`, so a `default` page on
   `myapp.<base>` calls its Functions through a Route outside `/function` or the bare host. An `acme` setup sets a
   base domain. The release note says so; tests, examples and lanes move in the same change.

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config: Config.Server gains
	BaseDomain string `json:"baseDomain,omitempty" env:"FUNCD_BASE_DOMAIN"` // default "localhost" (ADR-0240)

// pkg/funcd: the base domain (Decision 1); "" means "localhost". Run fails with fault.Invalid on a refused value.
func WithBaseDomain(base string) Option

// internal/edge/router — in each, a baseDomain of "" means "localhost".
// NamespaceHost is the host a namespace's host-less entries answer on: "<ns>.<base>", or "" for default.
func NamespaceHost(ns v1.NamespaceName, base string) string

// NamespaceOfHost is the namespace a host under base names (Decision 6), compared in canonicalHost's spelling.
func NamespaceOfHost(host, base string) v1.NamespaceName
func NewAggregator(p Programmer, modes ModeFunc, notify NotifyFunc, baseDomain string, log *slog.Logger) *Aggregator

// Programmer and Router: a host-less row answers on no reserved host (Decision 4) nor on a name under it.
Program(ctx context.Context, entries []Entry, reserved []string) error
// Verdict gains the host its entry claims, in canonicalHost's spelling; "" is any host but reserved ones (ADR-0240).
	Host string
const reasonHostReserved = "HostReserved"

// internal/dataplane: Handler gains the base domain after the router.
func Handler(st store.Store, act *activator.Activator, rtr router.Router, baseDomain string, enf *authn.Enforcer,
	lim *limit.TargetLimiter, stat *static.Handler, defaultTimeout time.Duration, logger *slog.Logger) http.Handler

// api/types/v1alpha1: RouteStatus gains
	// Host is the host the Route claims: spec.host, else its namespace host, else "" (any host but reserved ones).
	Host string `json:"host,omitempty"`
```

| Consumes | Writes | Exposes |
|---|---|---|
| `server.baseDomain`; each entry's host and namespace; the request's `Host` header | the edge table with namespace hosts and the reserved set; the verdict `HostReserved`; `status.host` | `<namespace>.<baseDomain>` for the host-less entries and Functions of every non-default namespace |

## Implementation plan

- `internal/platform/config`, `pkg/funcd` (`WithBaseDomain`) and `cmd/funcd`: the setting and its validation, unit
  tests; `examples/funcdconfig.yaml`.
- `internal/edge/router`: the two helpers; in `arbitrate` the host step, the reserved set, both `HostReserved` rules
  and `Verdict.Host`; `Program`'s `reserved` and `Resolve`; unit tests per rule, including a reservation that starts
  and ends.
- `internal/route`: `status.host`; `HostReserved` reaches `Ready` as other reasons do (`internal/route/reconcile.go:107`)
  and the catalog reconciler reports any reason (`internal/services/catalog/reconcile.go:334`).
- `internal/dataplane`: Decision 6 in the front door and the by-name path; unit tests.
- e2e tests in `pkg/funcd`, one per scenario, named after it, with `t.Parallel()`. Tests of host-less entries in a
  non-default namespace move to its host: `internal/edge/router/aggregator_test.go:309`,
  `internal/services/catalog/reconcile_test.go:781`, `internal/route/reconcile_test.go:200-206`, the `team-guarded`
  Route (`e2e/fixtures/env-echo-edge-defaults.yaml:26-36`, `e2e/env-echo.venom.yml:575-596`) and app-todo C6
  (staging's `preview` Site, `e2e/app-todo.venom.yml:2593-2598`). OpenAPI regenerated.
- Docs: the feat row, the back-links, `blueprint.md:670`, `:684-685` and `:688-690`, the `Entry.Host` and
  `SiteIngress.Host` comments.
- Done when `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, and the Lima lanes pass on containerd.

## Review checklist

- [ ] A non-default namespace's host-less entries answer only on its host; `default`'s on every host but reserved ones.
- [ ] The host step runs once, in the aggregator, before every claim rule; no source changes what it sends.
- [ ] A namespace reserves its host only while one of its stored entries is there; `HostReserved` covers the host and
  every name under it and beats the (namespace, name) order.
- [ ] On a host that names a namespace, a public `/function/…` request reaches that namespace by name unless its own
  entry matches; another namespace's rule there is `HostReserved`; internal calls keep the header.
- [ ] `ReservedPath` and `HostRequired` apply only to entries still host-less.
- [ ] `status.host` is the claimed host of every Route with a verdict; the base domain is validated at startup.
- [ ] One e2e test per scenario, named after it.

## Consequences

- **Positive**: the three problems of the Context are gone, and a local namespace host needs no DNS.
- **Negative**: host-less URLs of non-default namespaces move, and `/function/<name>` on a host under the base domain
  changes namespace; a server needs wildcard DNS and certificates for its namespace hosts; a namespace that starts
  serving on its host turns off other namespaces' claims and `default`'s host-less entries there.
- **Risks accepted**: a turn-off shows only as `HostReserved` in the losing object's status. On a host its namespace
  does not reserve, another namespace's page (`default`'s host-less one or an explicit claim) runs in its origin: it
  can call the namespace's Functions with the namespace's cookies and read the answers, and a URL naming the namespace
  helps phishing; only `/function` is protected. `default`'s host-less entries still shadow, or share an origin with, a
  namespace's explicit hosts (ADR-0176's residual). A reservation lapses while a namespace's only entry is a catalog
  that is not Ready or a Route whose backend is missing. A page on `<ns>.<base>` can set a cookie for `<base>`
  (RFC 6265 §4.1.2.3) that every namespace host receives, unless a public base domain is on the Public Suffix List.

## Open questions

- Certificates for hosts that appear after start, without a restart: a follow-up to ADR-0111.
- The exposure model of each kind and the precedence of Routes: the next ADR.

## References

- #1038, the feature issue (tracker #1039)
- RFC 6761 §6.3, `localhost` names: https://www.rfc-editor.org/rfc/rfc6761#section-6.3
- RFC 9525 §6.3, a wildcard matches one label: https://www.rfc-editor.org/rfc/rfc9525#section-6.3
- RFC 6265 §4.1.2.3, the cookie `Domain` attribute: https://www.rfc-editor.org/rfc/rfc6265#section-4.1.2.3
- Knative's default host `{{.Name}}.{{.Namespace}}.{{.Domain}}`: https://pkg.go.dev/knative.dev/networking/pkg/config
