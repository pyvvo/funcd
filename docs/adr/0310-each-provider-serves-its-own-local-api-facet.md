# ADR-0310: Each provider serves its own facet of a worker's local API

- **Status**: Implemented (2026-11-03), review passed with no finding
  ([report](../reviews/adr-0310-implementation-claude-opus-5-5.md)); Reviewing (2026-11-03, implemented in three
  commits: the prefix test, the local API's facets, the providers' facets); Accepted (2026-11-03, by the decider,
  after refining it with them; judged twice in an independent session, every finding folded)
- **Superseded in part by**: [ADR-0321](0321-cedar-decides-each-secret-a-workload-reads.md) (2026-11-11) — Decision 6's "over the config alone": the secrets provider is built over the policy decision point too, once it exists, still before every other provider.
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, local-api, architecture, refactor
- **Realizes**: [FEAT-0001/F224](../feat/0001-feat-v1.1.md) (each provider serves its own part of a worker's local API)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0310` back-link at acceptance:
  - [ADR-0069](0069-kv-data-plane.md), [ADR-0127](0127-context-blob-data-plane.md) and
    [ADR-0215](0215-built-in-health.md): the `NewHandler` and `NewManager` signatures in their Contracts, whose `kv`
    and `blob` ports become facets, and ADR-0127's `registerBlob(mux *http.ServeMux, …)`;
  - [ADR-0249](0249-ctx-secret-reads-from-funcd.md): Decision 1's "registered by `WithSecrets` like `WithSQL`" and
    `WithSecrets` in its Contracts;
  - [ADR-0309](0309-each-provider-builds-and-holds-its-own-parts.md): in Decision 2, "the kv, blob, sql facades to the
    Executor" and the secret resolver and secret facade as the Platform's parts; in Decision 3, the order (secrets is
    built first, and no step builds the secret facade); in Decision 4, "the kv and blob facades go to `NewManager`,
    sql and secrets through `WithSQL` and `WithSecrets`"; Decision 6; the executor row of its Contracts table.
- **Relates to**: ADR-0309's two open questions, both answered here ·
  [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md) (its shared hooks; its
  Decision 4 lets a new one join the logged list) · [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md)
  (Decision 3's "no hook" means the lifecycle hooks, so it stands) · [ADR-0064](0064-fn-to-fn-rpc-links.md),
  [ADR-0075](0075-cedar-invoke-authorization.md), [ADR-0158](0158-pool-member-identity.md) (the caller a handler
  serves) · [ADR-0243](0243-http-functions.md) (the fetch route) ·
  [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) (the edge's collision rule) ·
  [ADR-0282](0282-openai-api-from-functions.md) and FEAT-0013/F172 (the next facets) ·
  [ADR-0233](0233-sql-access-from-functions.md) · [ADR-0093](0093-function-configmap-consumption.md),
  [ADR-0245](0245-ctx-config-secret-catalog.md) (config stays in the environment) ·
  [ADR-0002](0002-source-code-conventions-and-patterns.md)

## Context & Need

A worker reaches its bindings through its local API (`internal/workernode/local`). Each binding is wired by name in
three shared places: `NewHandler` takes kv and blob as parameters and sql and secrets as options (`local.go:162`), the
Executor's inputs carry the four facades (`execDeps`), and `New` fills them (`funcd.go:778`). ADR-0282 (the AI routes,
Accepted) and FEAT-0013/F172 (vectors) add two bindings, and each would edit all three places. ADR-0309 left both
questions open: a route hook on the local API, and secrets as a provider type.

Purpose: each provider brings the routes a worker calls it on, so a binding is one facet in
`internal/workernode/local` and one method on its provider, and the start refuses a facet that would take another's
paths. Callers: the Executor's `build`, once; `NewHandler`, once per caller.

## Scenarios

- **scenario: each-provider-serves-its-facet** — *Given* a Function bound to a KVStore table, a Bucket prefix, a
  SQLDatabase and a Secret, *When* its worker calls `/kv/…`, `/blob/…`, `/sql/…`, `/secret/…`, `/invoke/{alias}`,
  `/fetch/{alias}/…` and `/health/dependencies` on its local API, *Then* each answers as today, the first four
  served by the facet of the provider that owns it. Tests: `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` (kv,
  invoke), `TestScenarioE2EBlobObjectViaContextBlob`, `TestScenarioBoundFunctionReads`, `TestScenarioSecretReadLive`,
  `TestDependencyCheck_AForbiddenLinkFails` and `TestScenarioHTTPFetch`, their assertions unchanged;
  `TestFacets_TheProvidersServeKVBlobSQLAndSecret` (new).
- **scenario: a-misplaced-facet-fails-the-start** — *Given* a facet whose segment is empty or holds `/`, `{` or `}`,
  a pattern outside its segment or with a host, two facets on one segment, or a facet on `invoke`, `fetch` or
  `health`, *When* the Executor builds, *Then* `New` fails with an Invalid error naming the facet and the pattern or
  segment before the Executor opens anything, and no worker starts. Tests: `TestCheckFacets` (new, one case each,
  plus the zero facet, ignored); `TestExecutorBuild_RefusesTwoFacetsOnOneSegment` (new: two `local.KVFacet`s).

## Scope

**In**: the facet type, its check and its option in `internal/workernode/local`; the facet hook and the kv, blob,
sql and secret facets; secrets as a provider; the constructor signatures; one segment-aware prefix test for the
facet check and its five existing copies.

**Out**: the AI facet (ADR-0282's implementation) and the vector facet (F172), which build on this; invoke, fetch and
the health check (Decision 3); ConfigMaps, which reach a worker in its environment (ADR-0093, ADR-0245); the edge's
collision rule, unchanged; out-of-process providers.

## Constraints & Decision drivers

- Behavior-preserving: the same paths, bodies, status codes and caller rules, so funcd-typescript and funcd-python
  do not change.
- A handler takes its caller from the `Ref` it was built for, in a pool the member ADR-0158 checks, never from the
  request (ADR-0064, ADR-0075).
- Go's `ServeMux` panics at registration on two patterns that match the same requests, so, with handlers built per
  sandbox (`manager.go:127`), a panic would come at the first worker start; it accepts without a panic a more
  specific or host-qualified pattern that takes another pattern's requests (References).
- ADR-0002: typed, no globals, no new dependency, no duplicated logic.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep parameters and options (today) | Each binding edits three shared places; ADR-0282 and F172 add two more. |
| Facets for new bindings only; kv and blob stay parameters | Two rules whose line is the bindings' age, not a design reason. The decider chose one rule. |
| The experiment's `local.WithRoutes`, a plain function type (commit `0d40b136`) | Any package could serve raw handlers, spreading the caller rule; it kept the `kv` and `blob` ports beside it. |
| The name `Routes`, or `Verbs` | `routes` already names the Route kind, App routes and edge routes (146 lines of Go); the decider declined `Verbs`. `Handlers`, `Mount`, `Segment` and `Surface` are taken or overloaded; `Facet` appears nowhere. |
| Turn `ServeMux`'s panic into a start error by building one handler at boot | It misses the overlaps `ServeMux` accepts without a panic. |
| ADR-0176's edge collision rule | It refuses only equal claims and lets a more specific prefix win, as a Site at `/` beside a Route at `/api` needs; facets must refuse that. Its segment-aware prefix test is reused (Decision 5). |
| One `ServeMux` per facet, picked by the first segment | Overlap is impossible by construction, but it changes how every request is routed; the start-time check gives the same guarantee and leaves the request path as it is. |
| Invoke and fetch as facets | `New` builds their invoker over the data plane after the providers (`funcd.go:770`); no provider holds it. |
| Config as a facet, read live like secrets | ConfigMaps reach a worker in its environment (ADR-0093, ADR-0245); live reads are a separate decision. |

## Decision

1. **A facet is one provider's part of a worker's local API.** `local.Facet` holds a segment and the function that
   registers its routes for a caller. Only `internal/workernode/local` builds one (`KVFacet`, `BlobFacet`,
   `SQLFacet`, `SecretFacet`; ADR-0282 adds `AIFacet`), so every route takes its caller from the `Ref` its handler
   serves. The paths and handlers are today's (`registerKV`, `registerBlob`, `registerSQL`, `registerSecrets`), and a
   facet's patterns depend on neither its port nor its caller.
2. **A provider returns its facet.** The shared hook `facetHook` (`Facet() local.Facet`) has ADR-0308's shape and
   joins its logged list. kv, blob, sql and secrets implement it. A provider's `Facet()` tests its own facade pointer
   and returns the zero `Facet` when it is nil; no facade is nil after a successful build today, so this serves a
   later optional provider. `New` passes the facets, in list order, to the Executor's `build`, which serves them
   through `WithFacets`.
3. **Invoke, fetch and health stay built in.** `NewHandler` keeps `POST /invoke/{alias}`, `/fetch/{alias}/{path...}`
   (any method, answered before the mux, ADR-0243) and `GET /health/dependencies`, and its `opaqueKeys` escaping of
   kv and blob keys (#100). It loses its `kv` and `blob` ports, and so does `NewManager`; `WithSQL` and `WithSecrets`
   go.
4. **Each facet owns its first path segment.** The segment is the facet's name (`kv`, `blob`, `sql`, `secret`, later
   `ai`): not empty, with no `/`, `{` or `}`. The Executor's `build` calls `local.CheckFacets` before it opens
   anything. It refuses, with an Invalid error naming the facet and the pattern or segment: an invalid segment; a
   pattern whose rest, after an optional method, is not a path under `/<segment>`, so a host-qualified pattern fails;
   two facets with one segment; and the segments `invoke`, `fetch` and `health`. Facets then cannot overlap each
   other or the built-in routes. Two patterns of one facet that `ServeMux` itself refuses panic in
   `TestFacets_TheProvidersServeKVBlobSQLAndSecret`, which builds one handler from every assembled facet.
5. **One segment-aware prefix test.** `/kv` covers `/kv` and `/kv/…`, not `/kv-2`. The test moves to a leaf package,
   `internal/platform/pathx` (`HasPrefix`), used by `CheckFacets` and by its five copies: the edge router's
   `matchPath` and `IsFunctionPath`, the embedded gateway's `matchPrefix`, the Site's `isRuntimeEntry` and the
   contract validator's schema-location test (`internal/contract/rules.go:235`). Their behavior is unchanged:
   `matchPath` keeps its exact and `/` cases and `IsFunctionPath` its `path.Clean`.
6. **Secrets is a provider.** `secretsProvider` builds the secret resolver and the secret facade over the config alone
   and keeps them, and serves the `secret` facet. It is built before every other provider, after the RBAC default
   (`funcd.go:577-579`), since lake, the Function reconciler and the workload backups read its resolver
   (`funcd.go:755`, `:824`, `:1018`). It takes the hand-written catalog entry's place in the list, so the catalog and
   its order are unchanged (config keeps its entry).

## Design model

None: wiring only, no state, timer or actor.

## Temporary workarounds

None.

## Contracts

```go
// internal/workernode/local

// Facet is one provider's part of a worker's local API: the routes under /<segment>. Only this package builds one;
// the zero Facet, with no register function, serves nothing.
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

// CheckFacets returns an Invalid error naming the facet and the pattern or segment for an invalid segment, a pattern
// outside its facet's segment, two facets on one segment, or a facet on invoke, fetch or health.
func CheckFacets(facets ...Facet) error

func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, deps DependencyChecker,
	logger *slog.Logger, opts ...Option) http.Handler
func NewManager(dir string, store FunctionStore, invoker Invoker, authz auth.Authorizer, deps DependencyChecker,
	logger *slog.Logger, opts ...Option) *Manager
```

```go
// pkg/funcd, unexported
type facetHook interface {
	lifecycle.Provider
	Facet() local.Facet // the zero Facet when the provider's facade is nil
}

type secretsProvider struct {
	cfg      *config
	resolver *secrets.Resolver
	facade   *secretsvc.Facade
}

func (s *secretsProvider) build() error // over the config alone: the resolver, then the facade
```

```go
// internal/platform/pathx

// HasPrefix reports whether p is prefix or lies under it: "/kv" covers "/kv" and "/kv/a", not "/kv-2". It compares
// the strings as given: it cleans nothing and needs no leading "/".
func HasPrefix(p, prefix string) bool
```

| Facet | Segment | Routes | Provider |
|---|---|---|---|
| `KVFacet` | `kv` | `GET`, `PUT`, `DELETE /kv/{binding}/{key...}`; `GET /kv/{binding}` (ADR-0069) | kv |
| `BlobFacet` | `blob` | `GET`, `PUT`, `DELETE /blob/{binding}/{key...}`; `GET /blob/{binding}` (ADR-0127) | blob |
| `SQLFacet` | `sql` | `POST /sql/{binding}/query`, `POST /sql/{binding}/execute` (ADR-0233) | sql |
| `SecretFacet` | `secret` | `GET /secret`, `GET /secret/{key}` (ADR-0249) | secrets |
| built in | `invoke`, `fetch`, `health` | `POST /invoke/{alias}` (ADR-0064); `/fetch/{alias}/{path...}`, any method, before the mux (ADR-0243); `GET /health/dependencies` (ADR-0215) | none |

| Dependencies & I/O | |
|---|---|
| Consumes | the providers' facades; the secret resolver's store and authorizer, as today |
| Exposes | no new route, path, status or header; the boot log's provider hooks gain `facets` |
| Config keys, dependencies | none new |

## Implementation plan

1. Commit 1, no behavior change: `internal/platform/pathx` with `HasPrefix` and its table test; the five copies call
   it (`internal/edge/router`, `internal/gateway/embedded`, `internal/site`, `internal/contract`), their tests
   unchanged.
2. Commit 2: in `internal/workernode/local`, `facet.go` (`Facet`, `facetMux`, `WithFacets`, `CheckFacets`) and each
   facet's constructor beside its routes; the `register*` functions take a `facetMux`; `NewHandler`, `NewManager` and
   the options lose `kv`, `blob`, `WithSQL` and `WithSecrets`. Tests: `TestCheckFacets`; the 30 constructor calls in
   15 test files and the 5 uses of `WithSQL` and `WithSecrets` change, with no assertion changed.
3. Commit 3: in `pkg/funcd`, `secretsProvider` in the secrets entry's place in `hookless()`, built first; `facetHook`
   in `sharedHooks`, so `TestPhases_ListTheProvidersWithTheirHooks` gains `, facets` on kv, blob and sql and reads
   `secrets: facets`; `Facet()` on the four, each asserting `facetHook` at compile time; `execDeps` takes the facets
   instead of the four facades; the Executor's `build` calls `local.CheckFacets` first; lake, the Function
   reconciler and the workload backups read the resolver from secrets. Tests:
   `TestFacets_TheProvidersServeKVBlobSQLAndSecret` (Decision 4), `TestExecutorBuild_RefusesTwoFacetsOnOneSegment`,
   `TestFacet_ANilFacadeServesNothing` (`(&kvProvider{}).Facet()` through `NewHandler` answers 404 on `/kv/b/k`); a
   `// scenario:` line above each Scenario's tests.
4. Mutants, each restored: a facet registering outside its segment; two facets on `kv`; a facet on `fetch`;
   `CheckFacets` not called; a provider's `Facet()` returning the zero facet; secrets built after lake. List which
   test caught each; a survivor is named in the hand-over with the checklist line that guards it.
5. Run `scripts/agent/d go test -race` on the touched packages, lint with the tags dev, e2e and integration, then
   `scripts/agent/gate.sh`, `just lima-example-all` and `just lima-example app-todo`, since every lane's functions
   call the local API.

Done when the gate, the 13 lanes and the app-todo lane pass. It lands before ADR-0282 is implemented, which then adds
`AIFacet` and the AI provider's `Facet()`.

## Review checklist

- [ ] `NewHandler` and `NewManager` take no store port, and `WithSQL` and `WithSecrets` are gone.
- [ ] `Facet`'s fields are unexported, and no package outside `internal/workernode/local` builds a non-zero one.
- [ ] The Executor's `build` calls `local.CheckFacets` before it opens anything, and its error fails `New`.
- [ ] `CheckFacets` refuses each case of Decision 4, the segments `invoke`, `fetch` and `health` included.
- [ ] `facetHook` embeds `lifecycle.Provider`; kv, blob, sql and secrets assert it; `sharedHooks` lists it.
- [ ] `New` builds no secret part; `secretsProvider` holds the resolver and the facade; `hookless()` keeps only
  config.
- [ ] The five copies call `pathx.HasPrefix`: `matchPath`, `IsFunctionPath`, `matchPrefix`, `isRuntimeEntry` and
  `internal/contract/rules.go`.
- [ ] No path, status or body changes: the existing local API tests pass with only their constructors changed.

## Consequences

- **Positive**: a new binding, ADR-0282's AI or F172's vectors, is a facet in `internal/workernode/local` and one
  method on its provider, with no line in `New`, `execDeps` or `NewHandler` that names it. Five copies of the prefix
  test become one.
- **Negative**: the constructors change in many tests; five ADRs are superseded in part; secrets gets a provider type;
  `opaqueKeys` still names kv and blob in `NewHandler`.
- **Risks**: the check guards `internal/workernode/local`'s own constructors, so a mistake there fails the start
  rather than a review; the prefix test's five callers change in packages this ADR otherwise leaves alone, and their
  own tests pin them (`hosts_test.go` pins `IsFunctionPath`'s clean).

## Open questions

None.

## References

- [ADR-0309](0309-each-provider-builds-and-holds-its-own-parts.md), its Open questions and its route-hook row in
  Alternatives · the experiment's `local.WithRoutes` (branch `keep/exp-construction`, commit `0d40b136`).
- `ServeMux` probes on Go 1.26 (2026-11-03): the same pattern twice, a renamed wildcard (`POST /sql/{db}/query`
  beside `POST /sql/{binding}/query`) and an overlap where neither is more specific (`GET /a/{x}` beside
  `GET /{y}/b`) each panic at registration; `GET /kv/{binding}/stats` and the host-qualified
  `GET local/kv/{binding}/{key...}` each register beside `GET /kv/{binding}/{key...}` and take its requests.
- Go's `net/http.ServeMux` patterns and precedence: <https://pkg.go.dev/net/http#ServeMux>.
