# ADR-0176: One collision rule for every edge source

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (judged twice by three lenses; held from publication)
- **Deciders**: green-0-rabbit
- **Tags**: edge, route, catalog, multi-tenancy
- **Realizes**: [FEAT-0008/F102](../feat/0008-feat-iam.md) (external catalog edge, ADR-0138) ·
  [FEAT-0006/F79](../feat/0006-feat-ingress-hardening.md) (declarative edge exposure, ADR-0110)
- **Supersedes in part**: [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) Decision 3 (lines
  149-155: "concatenates all sources in sorted key order" with no arbitration), Decision 4 (lines 156-160: an
  optional `Host` and no rule against Routes) and its Temporary workaround (line 166) — reason: they let a
  catalog take another namespace's path. [ADR-0110](0110-route-v2-declarative-edge-exposure.md) Decision 3
  (lines 137-143, "the reconciler enforces") and Decision 4 (lines 144-147, the Route reconciler "applies the
  host/conflict rules") for *where* the rules run: the aggregator, not the Route reconciler; and Decision 6
  (lines 154-164, the front door consults Routes first) for host-less claims under `/function/`, now refused.
  [ADR-0140](0140-path-mounted-site.md) line 108 ("`/function/…` is not reserved either (a Route may shadow it)")
  for host-less claims only. Reason for both: one arbiter over one table, and a host-less claim must not take the by-name form.
- **Extends**: ADR-0110 Decision 3's `HostRequired`/`RouteConflict` to every edge source. **Relates to**: [ADR-0139](0139-site-declarative-static-web-app.md) (a Site edges through a Route) ·
  [ADR-0162](0162-catalog-stable-proxy-url.md) (both edit `internal/services/catalog/reconcile.go`)

## Context & Need

The edge router holds one replace-all table. ADR-0110 keeps tenants apart inside it: one owner per `(host, path,
method)` and a host in every `explicit`-mode namespace. Since ADR-0138 a second source writes to the same table,
and none of those rules reach it (origin/main 5dbb7fb):

- `internal/services/catalog/reconcile.go:213-237` (`syncIngressRoute`) builds an open-auth `Entry` from
  `spec.ingress.host`/`pathPrefix` and calls `r.routes.Set(ctx, "catalog/<ns>/<name>", …)` (line 234) with no host
  check and no collision check.
- `internal/edge/router/aggregator.go:58-95` (`Set`, `unionLocked`) concatenates sources in sorted key order.
  `router.go:111` sorts rows stably, so on an exact tie the earlier source wins; `"catalog/…"` sorts before
  `"routes"`, so a CatalogService takes a user Route's path.
- `internal/route/reconcile.go:149-163`: `HostRequired` and `RouteConflict` run only inside the `"routes"` source;
  the Route that lost to a catalog stays `Ready`/`Programmed`.
- `internal/dataplane/dataplane.go:92`: `router.Resolve` runs before the `/function/<name>` form (`pathPrefix`,
  line 52), and an `Upstream` match is reverse-proxied (`serveUpstream`, line 231) with the caller's
  `Authorization` header and body.

Observable effect: a CatalogService in one namespace can take, without a host, a path that another namespace's
Route or a by-name `/function/<name>` call uses; requests then reach the catalog proxy, and the displaced Route
still reports `Ready`. The purpose of this ADR: every edge source obeys one set of claim rules, arbitrated in one
place, and the loser is told.

## Scenarios

- `scenario: catalog-loses-to-earlier-route` — Given Route `a/r` on `(h, /q, all)` and CatalogService `b/c` with
  `ingress {host: h, pathPrefix: /q}`, When both reconcile in either order, Then `/q` on `h` reaches `a/r`'s
  Function, `a/r` is `Ready`, and `b/c` has `IngressReady=False` (`RouteConflict`, naming `Route a/r`) and `Ready=True`.
- `scenario: route-loses-to-earlier-catalog` — Given CatalogService `a/c` and Route `b/r` on the same claim, Then
  `a/c` serves it and `b/r` is `NotReady` (`RouteConflict`, naming `CatalogService a/c`).
- `scenario: same-name-tie-route-first` — Given Route `a/x` and CatalogService `a/x` on the same claim, Then the
  Route wins and the CatalogService reports `RouteConflict`.
- `scenario: catalog-vs-catalog-handover` — Given CatalogServices `a/c` and `b/c` on the same claim, When `a/c` is
  deleted, Then `b/c`'s entry is programmed and `b/c` reports `IngressReady=True` without another change to it.
- `scenario: catalog-host-required-explicit` — Given an `explicit`-mode namespace and a CatalogService with
  `ingress {pathPrefix: /q}` and no host, Then no edge entry exists for it, `IngressReady=False`
  (`HostRequired`), `Ready=True`, and the engine keeps serving functions bound through `spec.catalogs`.
- `scenario: reserved-prefix-refused` — Given a host-less Route at `/function/f`, or a host-less catalog ingress
  at `/function/f` or `/` (either mode), Then it is refused (`ReservedPath`) and, with no host-less `/` Route
  present, `POST /function/f` still reaches Function `f` by name.
- `scenario: verdict-restart-stable` — Given the claims of the scenarios above, When the daemon restarts and the
  sources `Set` in the reverse order, Then every verdict is the same as before the restart, and no catalog entry
  is programmed before `"routes"` has `Set`.

## Scope

In: the claim rules (one owner per exact `(host, path, method)`, `HostRequired`, the reserved prefix) for every
source the aggregator unions; their arbitration in the aggregator; the loser's status for Routes and
CatalogServices.

Out: re-evaluation when a Namespace's `defaultExposure` changes (no Namespace watch exists today,
`pkg/funcd/funcd.go`; Routes share the gap); overlap between different prefixes, including a host-less longer
prefix that outranks a host-qualified shorter one (`router.go:111-116` sorts by path length before host); a
host-less `/` Route rule (Open questions); `CatalogService.Validate`.

## Constraints & Decision drivers

- ADR-0110 Decision 3: a namespace can never take another's exact claim. That holds only if it holds for every source.
- One arbiter (two over one table disagree), deterministic and restart-stable: a verdict depends on the claims, not on reconcile order.
- ADR-0138 Decision 1: no user Route can set `Upstream`; this ADR does not change that.

## Alternatives considered

| Option | Outcome |
|---|---|
| **(1) Arbitrate in the aggregator across sources, return per-source verdicts, ADR-0110 order across kinds** | **chosen**: one arbiter over the one table; no source reads another kind |
| (2) The catalog reconciler reads the Route set, or a shared claim index, before `Set` | rejected: two arbiters that race; every new source must copy the rule |
| (3) An existing `Ready` claim wins (first come) | rejected: the winner depends on reconcile order and changes across restarts |
| (4) Separate host spaces per source | rejected: one listener and one host namespace; a reserved host per source still needs arbitration |

## Decision

1. **One rule set for every source.** Each `Entry` names its owner `(kind, namespace, name)`. Each claim
   `(host, path, method)` has one owner. Empty `methods` claims every method, including methods outside
   `allHTTPMethods` (a nil-method row matches any method, `router.go:132`): two entries collide when their
   method sets intersect. `HostRequired` (host empty in an `explicit`-mode namespace) applies to every source as ADR-0110
   applies it to Routes. The reserved prefix (Decision 5) is a **new** rule for Routes too: it replaces ADR-0110
   Decision 6's Routes-first order and ADR-0140's "not reserved" for host-less claims. A Route's host is per
   Route, so `HostRequired` and `ReservedPath` refuse the whole Route, not one rule.
2. **The aggregator arbitrates.** `Set` validates first (non-empty `Owner`, ranked `Kind`, `Owner.Namespace` equal
   to `Entry.Namespace`; else `fault.Invalid`, nothing committed), then stores the entries and evaluates the
   union in the order `(namespace, name, kind rank)`, kind rank `Route` = 0, `CatalogService` = 1 (a new source
   takes the next rank). For each entry: `ReservedPath`, else `HostRequired`, else `RouteConflict` if any claim
   is held by an earlier entry, else the entry is programmed and its claims are held. Only programmed entries
   reach `Router.Program`. The verdict is a function of the stored entries and the namespace modes; `ModeFunc`
   is read once per namespace per `Set`, under the aggregator lock (acceptable: `Set` is already serialized and
   the read is a local store get). A `ModeFunc` error other than NotFound (which maps to `implicit`) fails the
   `Set` before anything is programmed.
3. **The loser is told.** `Set` returns the caller's verdicts. The Route reconciler sets `NotReady` with the
   reason (as today); the message names the winning owner. The CatalogService reconciler withdraws only its
   ingress (the entry is not programmed) and sets a new condition `IngressReady` (`True`/`Programmed` or
   `False` with the same reason); `Ready` and the engine are untouched. When `spec.ingress` is set but the catalog
   is not Ready or has no proxy URL, it `Set`s nil and reports `IngressReady=False`, reason `CatalogNotReady`
   (new). After each `Set`, with `a.mu` released, the aggregator calls `NotifyFunc` for every *other* owner whose
   verdict changed, so that owner's reconciler re-runs (also catalog vs catalog). `NotifyFunc` is a
   non-blocking enqueue, so it cannot stall or deadlock `Set`. The re-run happens only on a verdict change.
4. **`HostRequired` covers catalog ingress** in an `explicit`-mode namespace (supersedes ADR-0138's optional host there; it stays optional in `implicit` mode).
5. **Reserved prefix.** A claim with an empty host whose path is `/function` or starts with `/function/`
   (`dataplane.pathPrefix`) is refused for every source, `ReservedPath`; the check applies to the `path.Clean`ed
   path, so a trailing slash or `//` does not evade it. A host-less catalog ingress at `/` is refused the same
   way (a new source, no existing users). With a host it is allowed.
6. **Fail closed at start.** The aggregator programs no entry of a kind rank above 0 until the `"routes"` source
   has `Set` once. A start-up call in `pkg/funcd/funcd.go` lists every Route (the full list that
   `internal/route/reconcile.go:136` iterates) and `Set`s `"routes"`, empty only when no Route exists. Held
   entries get the new reason `EdgeStarting` ("held until the Route table loads").

## Temporary workarounds

None.

## Contracts

New names (grepped at 5dbb7fb, none exists): `router.Owner`, `router.Verdict`, `router.ModeFunc`,
`router.NotifyFunc`, `Entry.Owner`, the reasons `ReservedPath`, `CatalogNotReady`, `EdgeStarting`, the CatalogService
condition `IngressReady`. Reused: `HostRequired`, `RouteConflict`, `Programmed`.

```go
// internal/edge/router
type Owner struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Entry gains: Owner Owner — required; Owner.Namespace must equal Entry.Namespace.

type Verdict struct {
	Owner   Owner
	Reason  string // "" = programmed; "ReservedPath" | "HostRequired" | "RouteConflict" | "CatalogNotReady" | "EdgeStarting"
	Message string // for RouteConflict: "conflicts with <Kind> <ns>/<name> on (host, path, method)"
}

type ModeFunc func(ctx context.Context, ns v1.NamespaceName) (v1.ExposureMode, error) // NotFound → implicit
type NotifyFunc func(Owner) // non-blocking enqueue; called after the aggregator lock is released

type EntrySetter interface {
	// Validation or ModeFunc error: (nil, err), nothing committed. Program error: (nil, err), the source map stays
	// committed as today and the next Set re-programs it; the caller returns the error and is requeued.
	Set(ctx context.Context, source string, entries []Entry) ([]Verdict, error)
}

func NewAggregator(p Programmer, modes ModeFunc, notify NotifyFunc, log *slog.Logger) *Aggregator
```

| Consumes | Exposes |
|---|---|
| each source's entries with owners; the namespace mode (`ModeFunc`) | one arbitrated table to `Router.Program`; per-owner verdicts; `NotifyFunc` calls; Route `Ready`; CatalogService `IngressReady` |

## Implementation plan

1. `internal/edge/router/aggregator.go`: `Owner`, `Verdict`, `ModeFunc`, `NotifyFunc`, validate-then-arbitrate in
   `Set` (Decision 2), the start gate (Decision 6); `router.go`: `Entry.Owner`.
2. `internal/route/reconcile.go`: keep `BackendNotFound`; move `HostRequired`/`RouteConflict` to the aggregator;
   `Set` every backend-valid Route and apply the returned verdicts.
3. `internal/services/catalog/reconcile.go`: `syncIngressRoute` sets `Owner`, applies its verdict to
   `IngressReady` (`CatalogNotReady` when it `Set`s nil), removes the condition when `spec.ingress` is unset.
   ADR-0162 edits the same file; merge after it and rebase.
4. `pkg/funcd/funcd.go`: `ModeFunc` from the namespace store with an error return; `NotifyFunc` enqueues a request
   on the owner's kind controller (new wiring; `site.MapRoute` at `pkg/funcd/funcd.go:753` is the precedent);
   the start-up full-list `Set` of `"routes"` (Decision 6).
5. Tests, one per scenario, named `TestScenario_<name>`: `internal/edge/router/aggregator_test.go`
   (`same-name-tie-route-first`, `reserved-prefix-refused`, `verdict-restart-stable`,
   `catalog-vs-catalog-handover`); `internal/route/reconcile_test.go` (`route-loses-to-earlier-catalog`);
   `internal/services/catalog/reconcile_test.go` (`catalog-loses-to-earlier-route`,
   `catalog-host-required-explicit`). Update ADR-0138's aggregator tests for the new `Set` signature.
6. Propagation: at Proposed, add "(+ ADR-0176 — one collision rule)" with the sub-status "collision rule: adr"
   to the F102 and F79 rows (as F102 tracks ADR-0153); advance that sub-status at each move (accepted →
   reviewing → implemented); the rows' other parts stay `implemented`. At Accepted, add the back-link "Superseded in part by ADR-0176" to ADR-0138, ADR-0110 and
   ADR-0140 (the only permitted touch), and sync `blueprint.md:640-652`: collision rules cover catalog ingress,
   and a host-less claim under `/function/` is refused for every source.
7. Definition of done: every scenario test passes with `-race`; `just ci` green; step 6 done.

## Review checklist

- [ ] The aggregator is the only place that decides a collision; `internal/route` no longer builds a claim map.
- [ ] Order is `(namespace, name, kind rank)`, `Route` before `CatalogService`; an unranked kind is refused.
- [ ] `Set` validates before it commits; a `ModeFunc` error programs nothing.
- [ ] A losing Route is `NotReady` with `RouteConflict`/`HostRequired`/`ReservedPath`; the message names the winner.
- [ ] A losing CatalogService has no edge entry, `IngressReady=False` with the reason, and `Ready=True`.
- [ ] A changed verdict reaches its owner's status through `NotifyFunc`.
- [ ] No catalog entry is programmed before `"routes"` has `Set`.
- [ ] Reversing the `Set` order yields the same verdicts and the same programmed table; each scenario has a passing test.

## Consequences

- Positive: one claim rule set for every source, now and for later sources; no source takes another namespace's
  exact `(host, path, method)` claim; the loser's status reports the loss.
- Negative: a host-less catalog ingress in an `explicit`-mode namespace stops being exposed until
  `spec.ingress.host` is set; a host-less Route under `/function/` that works today is refused.
- Residual, still open for every source: a host-less longer prefix outranks a host-qualified shorter one (e.g. a
  host-less catalog at `/orders/admin` in an `implicit` namespace takes `/orders/admin/…` from a Route on
  `(h, /orders)`); a host-less `/` Route precedes the by-name form; a host-qualified `/function/` claim on the
  platform's shared public host takes by-name calls of other namespaces on that host.
- Risk accepted: at start, a catalog entry that will lose to another namespace's catalog can serve that path
  until the other catalog reconciles (cross-namespace, catalog to catalog, start window only).

## Open questions

- Refuse a host-less `/` Route, and sort host-qualified rows first? A follow-up ADR (changes host-less Sites).

## References

- Code at 5dbb7fb: `internal/edge/router/{aggregator,router}.go`, `internal/route/reconcile.go`,
  `internal/services/catalog/reconcile.go`, `internal/dataplane/dataplane.go`.
- Prior art: Envoy xDS (one snapshot from many sources); Gateway API route conflicts (oldest, then namespace/name).
