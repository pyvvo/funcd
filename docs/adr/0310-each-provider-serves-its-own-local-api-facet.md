# ADR-0310: Each provider serves its own facet of a worker's local API

- **Status**: Proposed (2026-11-03; refined with the decider: every service provider serves its own facet, invoke and
  health stay built in, the name `Facet`, facets built only in `internal/workernode/local`, the first-segment rule
  checked at start, one shared segment-aware prefix test)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, local-api, architecture, refactor
- **Realizes**: [FEAT-0001/F224](../feat/0001-feat-v1.1.md) (each provider serves its own part of a worker's local API)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0310` back-link at acceptance:
  - [ADR-0069](0069-kv-data-plane.md), [ADR-0127](0127-context-blob-data-plane.md) and
    [ADR-0215](0215-built-in-health.md): the `NewHandler` and `NewManager` signatures in their Contracts, whose `kv`
    and `blob` ports become facets (Decision 3);
  - [ADR-0249](0249-ctx-secret-reads-from-funcd.md): Decision 1's "registered by `WithSecrets` like `WithSQL`" and
    `WithSecrets` in its Contracts;
  - [ADR-0309](0309-each-provider-builds-and-holds-its-own-parts.md): in Decision 2, the secret resolver and the
    secret facade as the Platform's parts; in Decision 3, the order (secrets is built first, and no step builds the
    secret facade); in Decision 4, "the kv and blob facades go to `NewManager`, sql and secrets through `WithSQL` and
    `WithSecrets`"; Decision 6 ("`NewHandler` and `NewManager` keep their signatures").
- **Relates to**: ADR-0309's two open questions, both answered here ·
  [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md) (its shared hooks) ·
  [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) · [ADR-0064](0064-fn-to-fn-rpc-links.md),
  [ADR-0075](0075-cedar-invoke-authorization.md), [ADR-0158](0158-pool-member-identity.md) (the caller a handler
  serves) · [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) (the edge's collision rule) ·
  [ADR-0282](0282-openai-api-from-functions.md) and FEAT-0013/F172 (the next facets) ·
  [ADR-0233](0233-sql-access-from-functions.md) · [ADR-0093](0093-function-configmap-consumption.md),
  [ADR-0245](0245-ctx-config-secret-catalog.md) (config stays in the environment) ·
  [ADR-0002](0002-source-code-conventions-and-patterns.md)

## Context & Need

A worker reaches its bindings through its local API (`internal/workernode/local`). Each binding is wired by hand:
`NewHandler` takes kv and blob as parameters and sql and secrets as options (`local.go:162`), the Executor's inputs
carry the four facades (`execDeps`), and `New` hands them over (`funcd.go:778`). A new binding edits these three
shared places, and kv and blob still pass their facades through the Platform, the hand-off ADR-0309 removed
everywhere else. ADR-0282 (the AI routes, Accepted) and FEAT-0013/F172 (vectors) add two bindings. ADR-0309 left both
questions open: a route hook on the local API, and secrets as a provider type.

Purpose: each provider brings the routes a worker calls it on, so a binding is one facet in
`internal/workernode/local` and one method on its provider, and the start refuses a facet that would take another's
paths. Callers: the Executor's `build`, once; `NewHandler`, once per caller.

## Scenarios

- **scenario: each-provider-serves-its-facet** — *Given* a Function bound to a KVStore table, a Bucket prefix, a
  SQLDatabase and a Secret, *When* its worker calls `/kv/…`, `/blob/…`, `/sql/…`, `/secret/…`, `/invoke/{alias}`
  and `/health/dependencies` on its local API, *Then* each answers as today, the first four served by the facet of
  the provider that owns it. Tests: `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` (kv, invoke),
  `TestScenarioE2EBlobObjectViaContextBlob`, `TestScenarioBoundFunctionReads`, `TestScenarioSecretReadLive`,
  `TestDependencyCheck_AForbiddenLinkFails`, their assertions unchanged;
  `TestFacets_TheProvidersServeKVBlobSQLAndSecret` (new).
- **scenario: a-misplaced-facet-fails-the-start** — *Given* a facet that registers a pattern outside its segment,
  such as `GET /kv/{binding}/stats` from a facet other than `kv` (Go's `ServeMux` accepts it and sends
  `/kv/cache/stats` to it, so a key named `stats` would become unreadable), two facets on one segment, or a facet
  on `invoke` or `health`, *When* the Executor builds, *Then* `New` fails with an Invalid error naming the facet and
  the pattern or segment, and no worker starts. Test: `TestCheckFacets` (new, one case each, plus a zero facet that
  is ignored).

## Scope

**In**: the facet type, its check and its option in `internal/workernode/local`; the facet hook and the kv, blob,
sql and secret facets; secrets as a provider; the constructor signatures; one segment-aware prefix test for the
facet check and its four existing copies.

**Out**: the AI facet (ADR-0282's implementation) and the vector facet (F172), which build on this; invoke and the
health check (Decision 3); ConfigMaps, which reach a worker in its environment (ADR-0093, ADR-0245); the edge's
collision rule, unchanged; out-of-process providers.

## Constraints & Decision drivers

- Behavior-preserving: the same paths, bodies, status codes and caller rules, so funcd-typescript and funcd-python
  do not change.
- A handler takes its caller from the `Ref` it was built for, in a pool the member ADR-0158 checks, never from the
  request (ADR-0064, ADR-0075).
- Go's `ServeMux` (probed on Go 1.26.3): a pattern that matches the same requests as another panics at registration,
  and handlers are built per sandbox (`manager.go:127`), so a clash would surface at the first worker start; a more
  specific pattern under another's prefix registers without a panic and takes the overlap.
- ADR-0002: typed, no globals, no new dependency, no duplicated logic.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep parameters and options (today) | A binding edits three shared places; kv and blob keep passing through the Platform; ADR-0282 and F172 add two more. |
| Facets for new bindings only; kv and blob stay parameters | Two rules whose line is the bindings' age, not a design reason; kv and blob keep the hand-off. The decider chose one rule. |
| The experiment's `local.WithRoutes`, a plain function type (commit `0d40b136`) | Any package could serve raw handlers, spreading the caller rule; it kept the `kv` and `blob` ports beside it. |
| The name `Routes`, or `Verbs` | `routes` already names the Route kind, App routes and edge routes (146 lines of Go); the decider declined `Verbs`. `Handlers`, `Mount`, `Segment` and `Surface` are taken or overloaded; `Facet` appears nowhere. |
| Turn `ServeMux`'s panic into a start error by building one handler at boot | It misses the overlap a more specific pattern makes without a panic. |
| ADR-0176's edge collision rule | It refuses only equal claims and lets a more specific prefix win, as a Site at `/` beside a Route at `/api` needs; reusing it would accept the overlap facets must refuse. Its segment-aware prefix test is reused (Decision 5). |
| Invoke as a facet | `New` builds its invoker over the data plane after the providers (`funcd.go:770`); no provider holds it. |
| Config as a facet, read live like secrets | ConfigMaps reach a worker in its environment (ADR-0093, ADR-0245); live reads are a separate decision. |

## Decision

1. **A facet is one provider's part of a worker's local API.** `local.Facet` holds a segment and the function that
   registers its routes for a caller. Only `internal/workernode/local` builds one (`KVFacet`, `BlobFacet`,
   `SQLFacet`, `SecretFacet`; ADR-0282 adds `AIFacet`), so every route takes its caller from the `Ref` its handler
   serves. The paths and handlers are today's (`registerKV`, `registerBlob`, `registerSQL`, `registerSecrets`).
2. **A provider returns its facet.** The shared hook `facetHook` (`Facet() local.Facet`) joins ADR-0308's and is
   listed with them in the boot log. kv, blob, sql and secrets implement it; a provider whose facade is nil returns
   the zero `Facet`, which serves nothing (404, as today). `New` passes the providers' facets, in list order, to the
   Executor, which serves them through `WithFacets`.
3. **Invoke and health stay built in.** `NewHandler` keeps `POST /invoke/{alias}` and `GET /health/dependencies` and
   loses its `kv` and `blob` ports; so does `NewManager`. `WithSQL` and `WithSecrets` go.
4. **Each facet owns its first path segment.** The segment is the facet's name: `kv`, `blob`, `sql`, `secret` (and
   `ai`). The Executor's `build` calls `local.CheckFacets` before `NewManager`. It refuses, with an Invalid error
   naming the facet and the pattern or segment, a pattern whose path is not under `/<segment>`, two facets with one
   segment, and the segments `invoke` and `health`. Facets then cannot overlap, and the start fails before any
   worker does.
5. **One segment-aware prefix test.** `/kv` covers `/kv` and `/kv/…`, not `/kv-2`. The test moves to a leaf package,
   `internal/platform/pathx` (`HasPrefix`), used by `CheckFacets` and by its four copies: the edge router's
   `matchPath` and `IsFunctionPath`, the embedded gateway's `matchPrefix` and the Site's `isRuntimeEntry`. Their
   behavior is unchanged; `matchPath` keeps its exact and `/` cases.
6. **Secrets is a provider.** `secretsProvider` builds the secret resolver and the secret facade and keeps them,
   serves the `secret` facet, and names its catalog entry in place of the hand-written one (config keeps its entry).
   It is built before every other provider, since lake, the Function reconciler and the workload backups read its
   resolver, and it takes the hand-written entry's place in the list, so the catalog's order is unchanged.

## Design model

None: wiring only, no state, timer or actor.

## Temporary workarounds

None.

## Contracts

```go
// internal/workernode/local

// Facet is one provider's part of a worker's local API: the routes under /<segment>. Only this package builds one.
type Facet struct {
	segment  string
	register func(mux facetMux, caller Ref, logger *slog.Logger)
}

// facetMux is what a facet registers on: the handler's *http.ServeMux, or CheckFacets' recorder.
type facetMux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

func KVFacet(kv KV) Facet
func BlobFacet(b Blob) Facet
func SQLFacet(s SQL) Facet
func SecretFacet(s Secrets) Facet

// WithFacets serves each non-zero facet on every caller's local API.
func WithFacets(facets ...Facet) Option

// CheckFacets returns an Invalid error naming the facet and the pattern or segment when a pattern leaves its
// facet's segment, two facets share a segment, or a facet takes invoke or health.
func CheckFacets(facets ...Facet) error

func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, deps DependencyChecker,
	logger *slog.Logger, opts ...Option) http.Handler
func NewManager(dir string, store FunctionStore, invoker Invoker, authz auth.Authorizer, deps DependencyChecker,
	logger *slog.Logger, opts ...Option) *Manager
```

```go
// pkg/funcd, unexported
type facetHook interface {
	Facet() local.Facet // the zero Facet when the provider's facade is nil
}

type secretsProvider struct {
	cfg      *config
	resolver *secrets.Resolver
	facade   *secretsvc.Facade
}

func (s *secretsProvider) build() error // the resolver over the store and the authorizer, then the facade
```

```go
// internal/platform/pathx

// HasPrefix reports whether the slash-separated path p is prefix or lies under it: "/kv" covers "/kv" and "/kv/a",
// not "/kv-2".
func HasPrefix(p, prefix string) bool
```

| Facet | Segment | Routes | Provider |
|---|---|---|---|
| `KVFacet` | `kv` | `GET`, `PUT`, `DELETE /kv/{binding}/{key...}`; `GET /kv/{binding}` (ADR-0069) | kv |
| `BlobFacet` | `blob` | `GET`, `PUT`, `DELETE /blob/{binding}/{key...}`; `GET /blob/{binding}` (ADR-0127) | blob |
| `SQLFacet` | `sql` | `POST /sql/{binding}/query`, `POST /sql/{binding}/execute` (ADR-0233) | sql |
| `SecretFacet` | `secret` | `GET /secret`, `GET /secret/{key}` (ADR-0249) | secrets |
| built in | `invoke`, `health` | `POST /invoke/{alias}` (ADR-0064), `GET /health/dependencies` (ADR-0215) | none |

| Dependencies & I/O | |
|---|---|
| Consumes | the providers' facades; the secret resolver's store and authorizer, as today |
| Exposes | no new route, path, status or header; the boot log's provider hooks gain `facets` |
| Config keys, dependencies | none new |

## Implementation plan

1. Commit 1, no behavior change: `internal/platform/pathx` with `HasPrefix` and its table test; `matchPath` and
   `IsFunctionPath` (`internal/edge/router`), `matchPrefix` (`internal/gateway/embedded`) and `isRuntimeEntry`
   (`internal/site`) call it.
2. Commit 2: in `internal/workernode/local`, `facet.go` (`Facet`, `facetMux`, `WithFacets`, `CheckFacets`) and each
   facet's constructor beside its routes; the `register*` functions take a `facetMux`; `NewHandler`, `NewManager` and
   the options lose `kv`, `blob`, `WithSQL` and `WithSecrets`. Tests: `TestCheckFacets`; the 30 constructor calls in
   15 test files and the 5 uses of `WithSQL` and `WithSecrets` change, with no assertion changed.
3. Commit 3: in `pkg/funcd`, `secretsProvider` in the secrets entry's place in `hookless()`, built first; `facetHook`
   in `sharedHooks`, and `Facet()` on kv, blob, sql and secrets; `execDeps` takes the facets instead of the four
   facades; the Executor's `build` calls `local.CheckFacets` before `NewManager`; lake, the Function reconciler and
   the workload backups read the resolver from secrets. Test: `TestFacets_TheProvidersServeKVBlobSQLAndSecret` (the
   assembled list's facets, in order, pass `CheckFacets`); a `// scenario:` line above each Scenario's tests.
4. Mutants, each restored: a facet registering outside its segment; two facets on `kv`; a facet on `health`;
   `CheckFacets` not called; a provider's `Facet()` returning the zero facet; secrets built after lake. List which
   test caught each; a survivor is named in the hand-over with the checklist line that guards it.
5. Run `scripts/agent/d go test -race` on the touched packages, lint with the tags dev, e2e and integration, then
   `scripts/agent/gate.sh`, `just lima-example-all` and `just lima-example app-todo`, since every lane's functions
   call the local API.

Done when the gate, the 13 lanes and the app-todo lane pass. It lands before ADR-0282 is implemented, which then adds
`AIFacet` and the AI provider's `Facet()`.

## Review checklist

- [ ] `NewHandler` and `NewManager` take no store port, and `WithSQL` and `WithSecrets` are gone.
- [ ] `Facet`'s fields are unexported, and no package outside `internal/workernode/local` builds one.
- [ ] The Executor's `build` calls `local.CheckFacets` before `NewManager`, and its error fails `New`.
- [ ] kv, blob, sql and secrets implement `facetHook`, `sharedHooks` lists it, and a nil facade gives the zero
  `Facet`.
- [ ] `New` builds no secret part; `secretsProvider` holds the resolver and the facade; `hookless()` keeps only
  config.
- [ ] The four copies of the segment-aware prefix test call `pathx.HasPrefix`, and no copy is left.
- [ ] No path, status or body changes: the existing local API tests pass with only their constructors changed.

## Consequences

- **Positive**: a new binding, ADR-0282's AI or F172's vectors, is a facet in `internal/workernode/local` and one
  method on its provider, with no line in `New`, `execDeps` or `NewHandler`. kv and blob no longer pass through the
  Platform, so ADR-0309's rule holds for every provider. A facet that would take another's paths fails the start,
  including the overlap `ServeMux` accepts silently. Three copies of the prefix test go.
- **Negative**: the constructors change in 30 test calls in 15 files and 5 option uses; four ADRs are superseded in
  part; secrets gets a provider type.
- **Risks**: the check guards `internal/workernode/local`'s own constructors, so a mistake there fails the start
  rather than a review; the four callers of the prefix test change in packages this ADR otherwise leaves alone, and
  their own tests pin them.

## Open questions

None.

## References

- [ADR-0309](0309-each-provider-builds-and-holds-its-own-parts.md), its Open questions and its route-hook row in
  Alternatives · the experiment's `local.WithRoutes` (branch `keep/exp-construction`, commit `0d40b136`).
- A `ServeMux` probe on Go 1.26.3 (2026-11-03): the same pattern twice, a renamed wildcard
  (`POST /sql/{db}/query` beside `POST /sql/{binding}/query`) and an overlap where neither is more specific
  (`GET /a/{x}` beside `GET /{y}/b`) each panic at registration; `GET /kv/{binding}/stats` beside
  `GET /kv/{binding}/{key...}` registers and serves `/kv/cache/stats`.
- Go's `net/http.ServeMux` patterns and precedence: <https://pkg.go.dev/net/http#ServeMux>.
