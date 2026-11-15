# ADR-0330: Each local API route declares the error causes it answers, and one interceptor writes and checks them

- **Status**: Proposed (2026-11-15). Rounds 1 and 2 of an independent judge (a subagent) were folded; the decider then
  asked that each cause be defined once, so round 3 judges this revision
- **Date**: 2026-11-15
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, errors
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0323 and ADR-0329 did)
- **Relates to**: [ADR-0329](0329-each-api-operation-documents-its-answers.md) (the HTTP API's answers and their
  guard; this answers its open question on the handler API rows another way) ·
  [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (`kindref` and the Handler API
  tables; `Render` keeps its signature) · [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) (the facets
  and `facetMux`, which stays unchanged) · [ADR-0002](0002-source-code-conventions-and-patterns.md) (`api/fault`, which
  this extends without changing a signature) · [ADR-0243](0243-http-functions.md) (fetch's dispatch around the mux) ·
  [ADR-0233](0233-sql-access-from-functions.md) (the SQL routes)

## Context & Need

A handler reaches funcd through its `ctx`: `ctx.kv`, `ctx.blob`, `ctx.sql`, `ctx.secret`, `ctx.invoke` and
`ctx.fetch` call the worker node's local API in `internal/workernode/local`, through routes such as
`PUT /kv/{binding}/{key...}`, while `ctx.config` and `ctx.lake` read the environment. A route answers a failure as a
problem of one of `api/fault`'s twelve kinds, through 38 calls of `fault.WriteProblem`, one of them the pool
dispatcher's. Each failure is created by a bare constructor where it happens, such as `fault.PayloadTooLargef` in
`internal/services/kv/kv.go:168`, so nothing names its cause: no route and no test can tell the two 413s of
`PUT /kv` apart.

The Spec tabs' Handler API tables, which `kindref` renders from each shim's `api.json` (ADR-0323), say what each
method does but not how it fails. The Develop rewrite (#1463) moved about 31 such rows off the Usage pages, and
ADR-0329 left them out of the HTTP API reference. What a method surfaces also differs from what its route answers:
`kv.get` returns `null` for a 404, and `kv.putIfAbsent` returns `null` for a 412.

## Scenarios

- `scenario: method-shows-its-errors`: given the KV Spec tab, when a reader looks up `put` in the Handler API table, in
  TypeScript or in Python, then one row appears for each cause that `put` surfaces, with the status, the problem type,
  when and what to do; and `get` shows no 404 row, since its shim returns `null` for it.
- `scenario: undeclared-cause-fails-a-test`: given a described route, when a test's call makes it answer an error
  whose cause it does not declare, or that has no cause, then `pkg/funcd`'s tests fail and name the route and the
  cause or the kind.
- `scenario: declared-cause-unproven-fails`: given a described route that declares a cause, when a full, passing run
  of `pkg/funcd`'s tests never got it, then the tests fail and name both. A filtered run skips this half, as
  ADR-0329's test of `checksAll` already pins.
- `scenario: undeclared-cause-logged`: given a running server, when a described route answers an error whose cause it
  does not declare, then the answer is unchanged and funcd logs one warning per route and cause in the process.
- `scenario: cause-keeps-the-detail`: given an error made from a cause, when funcd answers it, then the problem's
  `detail` is the text it is today, and `errors.Is` finds the cause through any wrapping.
- `scenario: one-error-writer`: given the code of `internal/workernode/local`, when a function writes a problem other
  than through the interceptor, then a test fails and names the file; the pool's member refusal is the one exemption.
- `scenario: every-method-mapped`: given the two shims' `api.json`, when a method has no entry in `kindref`'s table, or
  its entry names a pattern that `local.Endpoints` lacks, then the Spec generation fails and names it.
- `scenario: facets-match`: given a built platform, when its facet segments differ from the facet segments of
  `local.Endpoints`, then a `pkg/funcd` test fails.

## Scope

In: `fault.Cause`; the causes of the described endpoints and their raising sites; every endpoint of the local API, the
four facets' routes, `invoke`, `health` and `fetch`; the interceptor; `kindref`'s table of methods; each method's error
rows, in both languages; the guard; the rollout list.

Out:
- The HTTP API, which ADR-0329 covers.
- Answers that the local API passes through unchanged: the target's reply to `ctx.invoke` and `ctx.fetch`, whose
  gateway answers are ADR-0329's `invokeFunction` rows, and health's dependency report, a 503 that is not a problem.
- `ctx.lake`'s errors, which are the Lake proxy's, and the shims' own throws, such as a bad argument.
- Any change of status, body or header, and any change to the shims or their `api.json`.
- A static check of the causes that a route can reach on paths no test runs (see Open questions).

## Constraints & Decision drivers

- Each cause is defined once, beside the code that raises it, and every route that can answer it lists the same value.
- One place writes every error of the local API.
- A method's rows show what that method surfaces, not every answer of its route.
- Tests enforce the declarations; reviewers do not. The guard runs outside the e2e build.
- Production behaviour and the wire do not depend on the documentation.

## Alternatives considered

- **The errors in each shim's doc comments**, the route that ADR-0329's open question named. Each sentence is written
  twice, by hand, in two repositories, and no test checks it. The decider rejected it (2026-11-15).
- **Cases written inline at each route.** The same literal repeats on every route that answers it, and nothing ties
  it to the code that raises the error. The decider asked for each cause to be defined once (2026-11-15).
- **A cause wrapped with `%w`, the plain sentinel idiom.** `fault.Error`'s text includes the error it wraps, and a 4xx
  problem's `detail` carries the whole text (`problem.go`, `detailOf`), so the wire would change. A `Cause` field that
  is not printed, with an `Is` method, keeps the idiom (`errors.Is`) and the detail.
- **Kinds extracted by a static call graph.** Through an interface such as the local API's `KV`, the graph follows
  every implementation, so it lists causes that no real path returns. It stays an open question for untested paths.
- **An undeclared cause rewritten to 500 in production**, as a catch-all error handler often does. The answer would
  then change whenever the documentation lags; a warning keeps the behaviour and still reports the gap.

## Decision

1. **A cause is a sentinel.** `api/fault` gains `Cause`: an error value with a name, a kind, when it happens and what
   the caller does, defined once, as `io.EOF` or `fs.ErrNotExist` are. The failing code makes its error from it:
   `kvstore.ErrValueOverCap.Errorf(op, format, args...)` returns a `*fault.Error` of the cause's kind whose new `Cause`
   field holds it. `(*Error).Is` matches that field, so `errors.Is(err, kvstore.ErrValueOverCap)` holds through any
   wrapping; the field is not part of the error's text, so the problem's `detail` is the text it is today.
2. **Where a cause lives.** In the lowest package that both its raiser and the endpoints that answer it import: the
   KV store's causes in `internal/kvstore`, which `internal/services/kv` and `internal/workernode/local` import; the
   quota's in `internal/quota`; a cause that only the local API raises, such as its two 412s, in `local`.
3. **One interceptor.** Each endpoint registers through `handle`, or, when its dispatch bypasses the mux as fetch's
   does (ADR-0243 Decision 6), is wrapped by `wrap`. The handler returns its error before it writes any status, and the
   interceptor answers a non-nil one through `fault.WriteProblem`. Every `fault.WriteProblem` call of the package goes
   through it, except the pool's member refusal (`manager.go`), which answers before any endpoint is chosen.
4. **Declared causes.** A described endpoint lists the causes it answers; an endpoint lists the same value as every
   other that answers it, such as `kvstore.ErrNoBinding` on each KV route. Nil marks an endpoint not yet described; an
   empty list, one that answers no error.
5. **Internal.** An error of kind `internal` is implicit on every endpoint: never declared, warned or compared. Each
   Handler API table that holds a method calling the local API says once that such a method can fail with 500.
6. **Production.** When a described endpoint answers an error whose cause it does not declare, or that has no cause,
   the answer is unchanged, and funcd logs one warning per endpoint and cause (or kind) in the process.
7. **Tests.** `local.SetErrorRecorderForTest` installs a process-wide hook that receives the pattern, the cause and the
   kind of every error the interceptor answers. `pkg/funcd`'s `TestMain` adds the described endpoints to ADR-0329's
   comparison: an undeclared cause, or an error with none, always fails; a declared cause that no test got fails on a
   full run that passed, as `checksAll` decides. Only `pkg/funcd`'s tests run the real facades; `local`'s own fakes
   prove the interceptor, not a declaration. The source scan of `one-error-writer` also refuses a call of
   `SetErrorRecorderForTest` from a file that is not a test.
8. **Docs.** `local.Endpoints` builds the four facets over nil ports and the built-in endpoints on a recorder, to
   which `handle` passes its causes through a type assertion, so `facetMux` (ADR-0310) is unchanged; fetch's causes
   are one value that `NewHandler`'s `wrap` call and `Endpoints` both read. `kindref` calls it itself, so `Render`
   (ADR-0323) keeps its signature. `kindref`'s table names, for each language and method, its endpoint or none (`log`,
   `remainingMs`, `config.get`, `config.list`, `lake.get`), and the causes of that endpoint that the method does not
   surface: those its shim returns as a value (`kvstore.ErrNotFound` under `kv.get`) and those it never provokes.
   Under each method, `kindref` writes one row per remaining cause: the status and problem type that `api/fault` gives
   its kind, when and what to do.
9. **Rollout.** `internal/workernode/local/testdata/undescribed-endpoints.txt` lists the endpoints with nil causes; a
   test keeps it equal to them, and a second call of `scripts/check-list-only-shrinks.sh` in CI stops it from growing.
   The guard and the warning skip an endpoint not yet described. The first pull request describes the KV endpoints,
   and health with no cause; each later one, one client, with its raising sites moved to causes.

## Design model

None. The decision adds no lifecycle, race, timer or delivery; it names errors, moves where they are written and
tests them.

## Behavior rows

None. The local API answers the same statuses, bodies and details.

## Temporary workarounds

The list of endpoints not yet described lets the rollout go one client at a time. Exit: the list is empty, and the
test and the CI call drop it.

## Contracts

```go
// package fault (additions; no existing signature changes)

// Cause is one named reason for an error, defined once; it is an error, so errors.Is can find it.
type Cause struct {
	Name string // unique in its package, e.g. "value-over-cap"
	Kind Kind
	When string // one sentence, e.g. "The value is larger than the store's cap."
	Fix  string // one sentence, e.g. "Store a smaller value, or raise the store's cap."
}

func NewCause(k Kind, name, when, fix string) *Cause
func (c *Cause) Error() string                                // its name
func (c *Cause) Errorf(op, format string, a ...any) *Error // an Error of c's kind whose Cause is c

// Error gains Cause *Cause, which Error() does not print, and
func (e *Error) Is(target error) bool // true when target is e.Cause
```

```go
// package local
func handle(mux facetMux, logger *slog.Logger, pattern string, causes []*fault.Cause,
	h func(http.ResponseWriter, *http.Request) error)
func wrap(logger *slog.Logger, pattern string, causes []*fault.Cause,
	h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc

type Endpoint struct {
	Pattern string         // e.g. "PUT /kv/{binding}/{key...}", or "/fetch/" for fetch
	Causes  []*fault.Cause // nil: not yet described
}

func Endpoints() []Endpoint // the four facets' over nil ports, then invoke, health and fetch
func SetErrorRecorderForTest(record func(pattern string, cause *fault.Cause, kind fault.Kind))
```

```go
// package kindref
type methodEndpoint struct {
	Endpoint string         // a pattern of local.Endpoints, or "" for a method that calls no endpoint
	Except   []*fault.Cause // the endpoint's causes that the method does not surface
}

// methods maps a language ("TypeScript", "Python") and a method ("kv.put", "kv.put_if_absent") to its endpoint.
var methods map[string]map[string]methodEndpoint //nolint:gochecknoglobals // an immutable lookup table
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| The causes, defined beside their raisers | `local.Endpoints` | `kindref` |
| `methods`, the shims' `api.json` | The error rows of `docs/guide/develop/**/spec.md` | funcd-frontend, agents, GitHub readers |
| The errors that `pkg/funcd`'s tests get from the local API | A failing `TestMain` on a mismatch | CI |

## Implementation plan

1. `api/fault`: `Cause`, `NewCause`, `Errorf`, the `Cause` field and `Is`; `TestScenarioCauseKeepsTheDetail`.
2. `internal/kvstore`, `internal/quota` and `local`: the KV causes; `internal/services/kv` (`kv.go`, `resolver.go`),
   `internal/quota` (`quota.go`) and `local/kv.go` raise them.
3. `internal/workernode/local`: `handle`, `wrap`, `Endpoint`, `Endpoints`, the hook and the warning; every endpoint
   moves to `handle` or `wrap`, the KV ones with their causes, the others with nil; `TestScenarioUndeclaredCauseLogged`,
   `TestScenarioOneErrorWriter` and `TestEveryEndpointIsDescribedOrListed`.
4. `internal/controlplane/kindref`: `methods` for both languages, the rows and the 500 sentence,
   `TestScenarioMethodShowsItsErrors` and `TestScenarioEveryMethodMapped`.
5. `pkg/funcd`: the hook in `TestMain` beside ADR-0329's, `TestScenarioUndeclaredCauseFailsATest` and
   `TestScenarioDeclaredCauseUnprovenFails` on a fixed set of answers, a test that provokes each KV cause, and
   `TestScenarioFacetsMatch`. `.github/workflows/ci.yml`: the second call of `scripts/check-list-only-shrinks.sh`.
6. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Definition of done: `just ci` passes; a second `just generate` changes nothing; the KV Spec tab shows each KV
method's rows in both languages, with no 404 under `get`; every endpoint goes through `handle` or `wrap`; the list
holds every endpoint but KV's and health; no problem's `detail` changed.

## Review checklist

- [ ] Each KV cause is defined once and raised through `Errorf`; no KV raising site uses a bare constructor.
- [ ] No function of `internal/workernode/local` writes a problem outside the interceptor, but the pool's refusal.
- [ ] Each KV endpoint lists each cause its tests get, and no other; `internal` is never declared.
- [ ] Each KV method's rows leave out the causes its shim returns as a value or never provokes.
- [ ] An undeclared cause keeps its answer and logs once; only test files call the hook.
- [ ] No status, body, header or `detail` changed; `facetMux` and `Render` keep their signatures.
- [ ] The guard's tests run outside the e2e build.

## Consequences

- A reader of a Handler API table sees how each method fails, and every row is proven by a test.
- Each cause has one definition that its raiser, its endpoints and `kindref` share, and the compiler checks every
  reference. Other code can test a cause with `errors.Is`.
- A new error on a described endpoint needs a cause; one with none fails the tests. The rollout moves each client's
  raising sites to causes.
- The shims need no change, and one set of causes serves both languages. `kindref`'s table must follow a shim that
  starts or stops returning an error as a value; its test catches a renamed method or endpoint, not that change.
- The guard sees only the calls that the tests make: an untested path can still answer an undeclared cause.

## Open questions

- A static check of the causes that an endpoint can reach: a later decision, if untested paths turn out to matter.
- Whether the HTTP API's cases (ADR-0329's `ErrorCase`) move to causes: a later decision, once the local API's rollout
  shows the cost.

## References

- pyvvo/funcd#1463: the Develop rewrite and its deferred rows; ADR-0329's open question on the handler API rows.
- `api/fault/fault.go` (`Error`, `Unwrap`, `KindOf`, `Wrapf`) and `problem.go` (`detailOf`); `internal/workernode/local`
  (`facet.go`, `kv.go`, `local.go`, `manager.go`); `internal/services/kv` and `internal/quota` (the KV raising sites);
  `pkg/funcd/binding_providers.go` and `providers.go` (the facets); the shims' `kv.ts`, `blob.ts`, `secret.ts`, `kv.py`
  and `secret.py` (the errors they return as values).
