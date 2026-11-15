# ADR-0330: Each local API route declares its error kinds, and one interceptor writes and checks them

- **Status**: Proposed (2026-11-15), after two rounds of an independent judge (a subagent): round 1 asked for another
  draft (one Blocker, three Majors); round 2 found no Blocker or Major, and its six Minors are folded
- **Date**: 2026-11-15
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, errors
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0323 and ADR-0329 did)
- **Relates to**: [ADR-0329](0329-each-api-operation-documents-its-answers.md) (the HTTP API's answers and their
  guard; this answers its open question on the handler API rows another way) ·
  [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (`kindref` and the Handler API
  tables, rendered from each shim's `api.json`; `Render` keeps its signature) ·
  [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) (the facets and `facetMux`, which stays unchanged) ·
  [ADR-0243](0243-http-functions.md) (fetch's dispatch around the mux) ·
  [ADR-0233](0233-sql-access-from-functions.md) (the SQL routes)

## Context & Need

A handler reaches funcd through its `ctx`: `ctx.kv`, `ctx.blob`, `ctx.sql`, `ctx.secret`, `ctx.invoke` and
`ctx.fetch` call the worker node's local API in `internal/workernode/local`, through routes such as
`PUT /kv/{binding}/{key...}`, while `ctx.config` and `ctx.lake` read the environment. A route answers a failure as a
problem of one of `api/fault`'s twelve kinds, each with one status and one problem type, through 38 calls of
`fault.WriteProblem`, one of them the pool dispatcher's.

The Spec tabs' Handler API tables, which `kindref` renders from each shim's `api.json` (ADR-0323), say what each
method does but not how it fails. The Develop rewrite (#1463) moved about 31 such rows off the Usage pages, and
ADR-0329 left them out of the HTTP API reference. A reader of `ctx.kv.put` cannot learn that it fails with 403 when no
binding names the alias, 413 over the store's cap and 507 over its quota. What a method surfaces also differs from
what its route answers: `kv.get` returns `null` for a 404, and `kv.putIfAbsent` returns `null` for a 412.

## Scenarios

- `scenario: method-shows-its-errors`: given the KV Spec tab, when a reader looks up `put` in the Handler API table, in
  TypeScript or in Python, then one row appears for each case of a kind that `put` surfaces as an error, with the
  status, the problem type, when and what to do; and `get` shows no 404 row, since its shim returns `null` for it.
- `scenario: undeclared-kind-fails-a-test`: given a described route that does not declare `conflict`, when a test's
  call makes it answer `conflict`, then `pkg/funcd`'s tests fail and name the route and the kind.
- `scenario: declared-kind-unproven-fails`: given a described route that declares a kind, when a full, passing run of
  `pkg/funcd`'s tests never got it, then the tests fail and name both. A filtered run skips this half, as ADR-0329's
  test of `checksAll` already pins.
- `scenario: undeclared-kind-logged`: given a running server, when a described route answers a kind that it does not
  declare, then the answer is unchanged and funcd logs one warning per route and kind that names both.
- `scenario: one-error-writer`: given the code of `internal/workernode/local`, when a function writes a problem other
  than through the interceptor, then a test fails and names the file; the pool's member refusal is the one exemption.
- `scenario: every-method-mapped`: given the two shims' `api.json`, when a method has no entry in `kindref`'s table,
  or its entry names a pattern that `local.Endpoints` lacks or excepts a cause that its endpoint does not declare, then
  the Spec generation fails and names it.
- `scenario: facets-match`: given a built platform, when its facet segments differ from the facet segments of
  `local.Endpoints`, then a `pkg/funcd` test fails.

## Scope

In: every endpoint of the local API: the four facets' routes, `invoke`, `health` and `fetch`; the interceptor; each
endpoint's declared cases; `kindref`'s table of methods; the error rows of each method, in both languages; the guard;
the rollout list.

Out:
- The HTTP API, which ADR-0329 covers.
- Answers that the local API passes through unchanged: the target's reply to `ctx.invoke` and `ctx.fetch`, whose
  gateway answers are ADR-0329's `invokeFunction` rows, and health's dependency report, a 503 that is not a problem.
- `ctx.lake`'s errors, which are the Lake proxy's, and the shims' own throws, such as a bad argument.
- Any change of status, body or header, and any change to the shims or their `api.json`.
- A static check of the kinds that a route can reach on paths no test runs (see Open questions).

## Constraints & Decision drivers

- One place writes every error of the local API, and a route's declaration sits beside its code.
- A method's rows show what that method surfaces, not every answer of its route.
- Tests enforce the declarations; reviewers do not. The guard runs outside the e2e build.
- Production behaviour does not depend on the documentation.

## Alternatives considered

- **The errors in each shim's doc comments**, the route that ADR-0329's open question named. Each sentence is written
  twice, by hand, in two repositories, and no test checks it. The decider rejected it (2026-11-15).
- **A table of methods and their kinds in `kindref`.** It would keep the kinds away from the code that returns them;
  the decider chose declarations at the route. Declarations at the route cannot know what a shim does with an answer,
  so `kindref`'s table records only that: which of a route's kinds a method does not surface as an error.
- **Kinds extracted by a static call graph.** Through an interface such as the local API's `KV`, the graph follows
  every implementation, so it lists kinds that no real path returns. It stays an open question for untested paths.
- **An undeclared kind rewritten to 500 in production**, as a catch-all error handler often does. The answer would
  then change whenever the documentation lags; a warning keeps the behaviour and still reports the gap.

## Decision

1. **One interceptor.** Each endpoint registers through `handle`, or, when its dispatch bypasses the mux as fetch's
   does (ADR-0243 Decision 6), is wrapped by `wrap`. The handler returns its error before it writes any status; the
   interceptor answers a non-nil error as a problem through `fault.WriteProblem`, and a nil error leaves the handler's
   own answer. Every `fault.WriteProblem` call of the package goes through it, except the pool's member refusal
   (`manager.go`), which answers before any endpoint is chosen.
2. **Declared cases.** A described endpoint declares one case per cause: a cause name unique on the endpoint, a kind,
   when it happens and what the caller does; `PUT /kv` has two `payload-too-large` cases, the body over the API's limit and the value over the store's cap.
   Nil cases mark an endpoint not yet described; an empty list, one that answers no error.
3. **Internal.** `internal` is implicit on every endpoint: never declared, warned or compared. Each Handler API table
   that holds a method calling the local API says once that such a method can fail with 500.
4. **Production.** When a described endpoint answers a kind that it does not declare, the answer is unchanged, and
   funcd logs one warning per endpoint and kind in the process, naming both.
5. **Tests.** `local.SetErrorRecorderForTest` installs a process-wide hook that receives the pattern and the kind of
   every error the interceptor answers, the facets that tests serve directly included. `pkg/funcd`'s `TestMain` adds
   the described endpoints to ADR-0329's comparison: an undeclared kind always fails; a declared kind that no test
   got fails on a full run that passed, as `checksAll` decides. Only `pkg/funcd`'s tests run the real facades:
   `local`'s own tests use fakes that can return any kind, so they prove the interceptor, not a declaration. The
   source scan of `one-error-writer` also refuses a call of `SetErrorRecorderForTest` from a file that is not a test.
6. **Docs.** `local.Endpoints` builds the four facets over nil ports and the built-in endpoints on a recorder, to
   which `handle` passes its cases through a type assertion, so `facetMux` (ADR-0310) is unchanged; fetch's cases are
   one value that `NewHandler`'s `wrap` call and `Endpoints` both read. `kindref` calls it
   itself, so `Render` (ADR-0323) keeps its signature. `kindref`'s table names, for each language and method, its
   endpoint or none (`log`, `remainingMs`, `config.get`, `config.list`, `lake.get`), and the causes of the endpoint's cases
   that the method does not surface as an error: those its shim returns as a value (`kv.get`'s `not-found`) and those
   it never provokes (plain `kv.put` sends no precondition). Under each method, `kindref` writes one row per remaining case:
   the status and problem type that `api/fault` gives the kind, when and what to do.
7. **Rollout.** `internal/workernode/local/testdata/undescribed-endpoints.txt` lists the endpoints with nil cases, a
   test keeps it equal to them, and a second call of `scripts/check-list-only-shrinks.sh` in CI stops it from growing.
   The guard and the warning skip an endpoint not yet described. The first pull request describes the KV endpoints, and
   health with no case, since its report is out of scope; each later one, one client.

## Design model

None. The decision adds no lifecycle, race, timer or delivery; it moves where errors are written and tests them.

## Behavior rows

None. The local API answers the same statuses and bodies.

## Temporary workarounds

The list of endpoints not yet described lets the rollout go one client at a time. Exit: the list is empty, and the
test and the CI call drop it.

## Contracts

```go
// package local

// ErrorCase is one documented failure of a local API endpoint, as ADR-0329's is of an operation.
type ErrorCase struct {
	Cause string     // a short name, unique on its endpoint, e.g. "value-over-cap"
	Kind  fault.Kind // e.g. fault.PayloadTooLarge
	When  string     // one sentence, e.g. "The value is larger than the store's cap."
	Fix   string     // one sentence, e.g. "Store a smaller value, or raise the store's cap."
}

// handle registers h on mux for pattern with its cases (nil: not yet described), and answers the error that h
// returns, before writing any status, as a problem; it warns through logger on an undeclared kind.
func handle(mux facetMux, logger *slog.Logger, pattern string, cases []ErrorCase,
	h func(http.ResponseWriter, *http.Request) error)

// wrap returns h intercepted as handle does, for a dispatch that bypasses the mux.
func wrap(logger *slog.Logger, pattern string, cases []ErrorCase,
	h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc

// Endpoint is one endpoint of the local API and its cases.
type Endpoint struct {
	Pattern string      // e.g. "PUT /kv/{binding}/{key...}", or "/fetch/" for fetch
	Cases   []ErrorCase // nil: not yet described
}

// Endpoints returns every endpoint: the four facets' over nil ports, then invoke, health and fetch.
func Endpoints() []Endpoint

// SetErrorRecorderForTest installs the hook that receives the pattern and kind of every error that the interceptor
// answers. Only tests call it; the package is internal, so only this module can.
func SetErrorRecorderForTest(record func(pattern string, kind fault.Kind))

var errorRecorder atomic.Pointer[func(pattern string, kind fault.Kind)] //nolint:gochecknoglobals // set only by tests

var warned sync.Map //nolint:gochecknoglobals // one warning per endpoint and kind in the process

// fetchCases are fetch's cases, which NewHandler's wrap call and Endpoints both read.
func fetchCases() []ErrorCase
```

```go
// package kindref

// methodEndpoint is what one method of a shim's api.json takes its rows from.
type methodEndpoint struct {
	Endpoint string       // a pattern of local.Endpoints, or "" for a method that calls no endpoint
	Except   []string // the causes of the endpoint's cases that the method does not surface as an error
}

// methods maps a language ("TypeScript", "Python") and a method ("kv.put", "kv.put_if_absent") to its endpoint.
var methods map[string]map[string]methodEndpoint //nolint:gochecknoglobals // an immutable lookup table
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| Each endpoint's declared cases | `local.Endpoints` | `kindref` |
| `methods`, the shims' `api.json` | The error rows of `docs/guide/develop/**/spec.md` | funcd-frontend, agents, GitHub readers |
| The errors that `pkg/funcd`'s tests get from the local API | A failing `TestMain` on a mismatch | CI |

## Implementation plan

1. `internal/workernode/local`: `ErrorCase`, `handle`, `wrap`, `Endpoint`, `Endpoints`, the hook and the warning;
   every endpoint moves to `handle` or `wrap`, the KV ones with their cases, the others with nil cases and listed;
   `TestScenarioUndeclaredKindLogged`, `TestScenarioOneErrorWriter` and `TestEveryEndpointIsDescribedOrListed`, which
   keeps the list equal to the endpoints with nil cases.
2. `internal/controlplane/kindref`: `methods` for both languages, the error rows and the 500 sentence,
   `TestScenarioMethodShowsItsErrors` and `TestScenarioEveryMethodMapped`.
3. `pkg/funcd`: the hook in `main_test.go`'s `TestMain` beside ADR-0329's, with
   `TestScenarioUndeclaredKindFailsATest` and `TestScenarioDeclaredKindUnprovenFails` on a fixed set of answers, a test
   that provokes each KV case, and `TestScenarioFacetsMatch`.
4. `.github/workflows/ci.yml`: the second call of `scripts/check-list-only-shrinks.sh`.
5. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Definition of done: `just ci` passes; a second `just generate` changes nothing; the KV Spec tab shows each KV
method's error rows in both languages, with no 404 under `get`; every endpoint goes through `handle` or `wrap`; the
list holds every endpoint but KV's and health.

## Review checklist

- [ ] No function of `internal/workernode/local` writes a problem outside the interceptor, but the pool's refusal.
- [ ] Each KV endpoint declares each kind that its tests get, and no other; `internal` is never declared.
- [ ] Each KV method's rows leave out the kinds that its shim returns as a value or that it never provokes.
- [ ] An undeclared kind keeps its answer and logs once; only tests set the hook.
- [ ] Every method of both `api.json` files has an entry, and every entry's pattern is an endpoint.
- [ ] No status, body or header of the local API changed, and `facetMux` and `Render` keep their signatures.
- [ ] The guard's tests run outside the e2e build.

## Consequences

- A reader of a Handler API table sees how each method fails, and every listed kind is proven by a test; a kind
  with two causes is proven by either.
- The error writes of the local API meet in one interceptor, but the pool's refusal; a new endpoint declares its
  cases when it is registered.
- The shims need no change, and one set of declared cases serves both languages. `kindref`'s table must follow a shim that
  starts or stops returning a kind as a value; its test catches a renamed method or endpoint, not that change.
- The guard sees only the calls that the tests make: an untested path can still return an undeclared kind.

## Open questions

- A static check of the kinds that an endpoint can reach: a later decision, if untested paths turn out to matter.
- Each client after KV: one pull request each, against its rows deferred by #1463.

## References

- pyvvo/funcd#1463: the Develop rewrite and its deferred rows; ADR-0329's open question on the handler API rows.
- `api/fault/fault.go` and `problem.go` (the twelve kinds, their statuses and problem types);
  `internal/workernode/local` (`facet.go`, `kv.go`, `local.go`, `manager.go`); `pkg/funcd/binding_providers.go` and
  `providers.go` (the providers' facets); the shims' `kv.ts`, `blob.ts`, `secret.ts`, `kv.py` and `secret.py` (the
  kinds they return as values).
