# ADR-0330: Each local API route declares its error kinds, and one interceptor writes and checks them

- **Status**: Draft (2026-11-15)
- **Date**: 2026-11-15
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, errors
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0323 and ADR-0329 did)
- **Relates to**: [ADR-0329](0329-each-api-operation-documents-its-answers.md) (the HTTP API's answers and their
  guard; this answers its open question on the handler API rows) ·
  [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (`kindref` and the Handler API
  tables, which it renders from each shim's `api.json`) · [ADR-0233](0233-sql-access-from-functions.md) (the local
  API's SQL routes)

## Context & Need

A handler reaches funcd's services through its `ctx`: `ctx.kv`, `ctx.blob`, `ctx.sql`, `ctx.secret` and `ctx.invoke`.
The shim sends each call to the worker node's local API in `internal/workernode/local`, through routes such as
`PUT /kv/{binding}/{key...}`. A route answers a failure as a problem of one of `api/fault`'s twelve kinds, each with
one status and one problem type, through 38 calls of `fault.WriteProblem` spread across the handlers.

The Spec tabs' Handler API tables, which `kindref` renders from each shim's `api.json` (ADR-0323), say what each
method does but not how it fails. The rewrite of the Develop guide (#1463) moved about 31 such rows off the Usage
pages, and ADR-0329 left them out of the HTTP API reference, because they are not operations of the spec. A reader
who calls `ctx.kv.put` cannot learn that it fails with 403 when no binding names the alias, 413 over the store's cap
and 507 over its quota.

## Scenarios

- `scenario: method-shows-its-errors`: given the KV Spec tab, when a reader looks up `put` in the Handler API table, in
  TypeScript or in Python, then one row appears for each kind that its route declares: the status, the problem type,
  when it happens and what to do.
- `scenario: undeclared-kind-fails-a-test`: given a route that does not declare `conflict`, when a test's call makes it
  answer `conflict`, then `pkg/funcd`'s tests fail and name the route and the kind.
- `scenario: declared-kind-unproven-fails`: given a route that declares a kind, when a full, passing run of
  `pkg/funcd`'s tests never got it, then the tests fail and name both.
- `scenario: undeclared-kind-logged`: given a running server, when a route answers a kind that it does not declare,
  then the answer is unchanged and funcd logs a warning that names the route and the kind.
- `scenario: one-error-writer`: given the code of `internal/workernode/local`, when a handler writes a problem other
  than through the interceptor, then a test fails and names the file.
- `scenario: every-method-mapped`: given the two shims' `api.json`, when a method has no entry in `kindref`'s table of
  methods and routes, then the Spec generation fails and names the method.

## Scope

In: every route of the local API, the providers' facets included; the interceptor; each route's declared cases; the
table that maps each `ctx` method to its route; the error rows of the Handler API tables, in both languages; the
guard in `pkg/funcd`'s tests; a rollout list of the routes not yet described.

Out:
- The HTTP API, which ADR-0329 covers.
- Any change of status, body or header: nothing changes on the wire.
- The shims and their `api.json`: nothing changes in the language repositories.
- A static check of the kinds that a route can reach on paths no test runs (see Open questions).

## Constraints & Decision drivers

- One place writes every error of the local API.
- A route's declaration sits beside its code, so a change to the route meets its declaration.
- Tests enforce the declarations; reviewers do not. The guard runs outside the e2e build.
- Production behaviour does not depend on the documentation.

## Alternatives considered

- **The errors in each shim's doc comments**, the route that ADR-0329's open question named. Each sentence is written
  twice, by hand, in two repositories, and no test checks it. The decider rejected it (2026-11-15).
- **A table of methods and kinds in `kindref`.** It keeps the declarations away from the code that returns the errors.
  The decider chose declarations at the route, read by one generic interceptor.
- **Kinds extracted by a static call graph.** Through an interface such as the local API's `KV`, the graph follows
  every implementation, so it lists kinds that no real path returns. It stays an open question for untested paths.
- **An undeclared kind rewritten to 500 in production**, as a catch-all error handler often does. The answer would
  then change whenever the documentation lags; a warning keeps the behaviour and still reports the gap.

## Decision

1. **One interceptor.** Each route of the local API registers through `handle(mux, pattern, cases, h)`. `h` returns an
   error; the interceptor answers a non-nil error as a problem through `fault.WriteProblem`, and a nil error leaves
   `h`'s own answer. No handler writes a problem itself.
2. **Declared cases.** Each route declares its cases: a kind, when it happens and what the caller does. Methods that
   share a route share its cases, such as `kv.get`, `kv.getText` and `kv.getJSON` on `GET /kv/{binding}/{key...}`.
3. **Production.** An answer whose kind the route does not declare is unchanged, and funcd logs a warning that names
   the route and the kind.
4. **Tests.** The interceptor reports the pattern and the kind of each error that it answers to a hook that only the
   test binary sets. `pkg/funcd`'s `TestMain` adds the local routes to ADR-0329's comparison: an undeclared kind always
   fails; a declared kind that no test got fails on a full run that passed, with no `-run` and no `-skip`.
5. **Docs.** `kindref` maps each method of the shims' `api.json` to its route, or to none for a method that calls no
   route, such as `log`, `remainingMs` and `config.get`. `specgen` passes the local routes' declarations to `kindref`,
   which writes, under each method of a Handler API table, a row for each case: the status and the problem type that
   `api/fault` gives the kind, when it happens and what to do. TypeScript and Python show the same rows.
6. **Rollout.** A route is described when the list of routes not yet described does not hold it; the list only
   shrinks, as ADR-0329's does. The first pull request describes the KV routes; each later one describes one client.

## Design model

None. The decision adds no lifecycle, race, timer or delivery; it moves where errors are written and tests them.

## Behavior rows

None. No platform behaviour changes: the local API answers the same statuses and bodies.

## Temporary workarounds

The list of routes not yet described lets the rollout go one client at a time. Exit: the list is empty, and the test
drops it.

## Contracts

```go
// package local

// Case is one documented failure of a local API route.
type Case struct {
	Kind fault.Kind // e.g. fault.PayloadTooLarge
	When string     // one sentence, e.g. "The value is larger than the store's cap."
	Fix  string     // one sentence, e.g. "Store a smaller value, or raise the store's cap."
}

// handle registers h on mux for pattern, declares the route's cases, and answers the error h returns as a problem.
func handle(mux facetMux, pattern string, cases []Case, h func(http.ResponseWriter, *http.Request) error)

// Route is one route of the local API and its declared cases.
type Route struct {
	Pattern string
	Cases   []Case
}

// Routes returns every route of the local API with its declared cases, registered on a recorder as CheckFacets does.
func Routes(facets ...Facet) []Route
```

```go
// package local: the test binary's hook; a server never sets it.
var errorRecorder atomic.Pointer[func(pattern string, kind fault.Kind)] //nolint:gochecknoglobals // set only by tests
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| Each route's declared cases | `Routes` | `specgen`, `kindref` |
| `kindref`'s table of methods and routes, the shims' `api.json` | The error rows of `docs/guide/develop/**/spec.md` | funcd-frontend, agents, GitHub readers |
| The errors that `pkg/funcd`'s tests get from the local API | A failing `TestMain` on a mismatch | CI |

## Implementation plan

1. `internal/workernode/local`: `Case`, `handle`, `Route`, `Routes`, the hook and the warning; each route moves to
   `handle`, the KV routes with their declared cases, the others on the rollout list.
2. `internal/controlplane/kindref`: the table of methods and routes, the error rows, the rollout list, and the tests
   `TestScenarioMethodShowsItsErrors` and `TestScenarioEveryMethodMapped`.
3. `internal/controlplane/cmd/specgen`: passes `local.Routes` with the platform's facets to `kindref`.
4. `pkg/funcd`: the hook in `main_test.go`'s `TestMain`, beside ADR-0329's, with `TestScenarioUndeclaredKindFailsATest`
   and `TestScenarioDeclaredKindUnprovenFails` on a fixed set of answers, and a test that provokes each KV case.
5. `internal/workernode/local`: `TestScenarioUndeclaredKindLogged` and `TestScenarioOneErrorWriter`.
6. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Definition of done: `just ci` passes; a second `just generate` changes nothing; the KV Spec tab shows each method's
error rows in both languages; every local route registers through `handle`; the list holds every route but KV's.

## Review checklist

- [ ] No handler of `internal/workernode/local` calls `fault.WriteProblem` outside the interceptor.
- [ ] Each KV route declares each kind that its tests get, and no other.
- [ ] An undeclared kind keeps its answer and logs a warning; only the test binary sets the hook.
- [ ] Every method of both shims' `api.json` has an entry in the table of methods and routes.
- [ ] No status, body or header of the local API changed.
- [ ] The guard's tests run outside the e2e build.

## Consequences

- A reader of a Handler API table sees how each method fails, and every listed kind is proven by a test.
- The 38 scattered error writes become one; a new route declares its cases when it is registered.
- The shims need no change, and the same rows serve both languages.
- The guard sees only the calls that the tests make: an untested path can still return an undeclared kind.

## Open questions

- A static check of the kinds that a route can reach: a later decision, if untested paths turn out to matter.
- Each client after KV: one pull request each, against its rows deferred by #1463.

## References

- pyvvo/funcd#1463: the Develop rewrite and its deferred rows; ADR-0329's open question on the handler API rows.
- `api/fault/fault.go` and `problem.go` (the twelve kinds, their statuses and problem types),
  `internal/workernode/local` (the routes and `facetMux`), `pkg/funcd/binding_providers.go` (the providers' facets).
