# ADR-0329: Each API operation documents its answers, and its feature's Spec tab shows them

- **Status**: Reviewing (2026-11-15, implemented); Accepted (2026-11-14), on the decider's go, after two rounds of an
  independent judge (a subagent): round 1 asked for another draft (one Blocker, three Majors); round 2 found no Blocker
  or Major, and its seven Minors are folded
- **Date**: 2026-11-14
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, codegen
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0194, ADR-0196 and ADR-0323 did)
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md) (the registrations are the source of the committed
  spec) · [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (the Spec tabs and
  `kindref`, which this extends) · [ADR-0210](0210-api-optimistic-concurrency-if-match.md) (a stale `If-Match` answers
  409) · [ADR-0303](0303-funcd-checks-every-call-against-its-contract.md) (a call's 422 and 500) ·
  [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (the behavior rows that list a call's
  answers) · [ADR-0113](0113-edge-authn-pep.md) (a call's 401 and 403)

## Context & Need

funcd registers its HTTP API code-first with huma (ADR-0005), and `specgen` commits the spec,
`api/openapi/funcd.v1alpha1.yaml`. It holds 167 operations on 76 paths. None has a summary, a description or a security
entry, each lists only its success (200 or 204) and a `default` error, and the call of a Function, on the gateway, is
missing. A reader cannot learn from it what an operation does, how to authenticate, or which errors it returns, when,
and what to do about them. The rewrite of the Develop guide (#1463) moved 153 such error rows off the Usage pages for
this reference.

The readers are people and agents who write a client or debug a call. They read a feature's Spec tab, which `kindref`
generates (ADR-0323), or the `/docs` page that each funcd server serves, which huma renders from the live control
plane's document: the same operations, without the Function call.

## Scenarios

- `scenario: operation-shows-its-answers`: given the KV Spec tab, when a reader looks up `replaceKVStore`, then they see
  its method, path, parameters and authentication, and one row for each status it answers, with the problem type, when
  it happens and what to do.
- `scenario: function-call-documented`: given the Function Spec tab, when a reader looks up `invokeFunction`, then each
  answer of Decision 2 appears, with its body and its ADR.
- `scenario: unlisted-answer-fails`: given a described operation, when a test's call to it gets a status that the
  operation does not list, then `TestMain` fails and names the operation and the status.
- `scenario: listed-answer-unproven-fails`: given a described operation that lists a status, when a full, passing run of
  `pkg/funcd`'s tests never got it, then `TestMain` fails and names both.
- `scenario: filtered-run-checks-only-unlisted`: given a run filtered with `-run` or `-skip`, or a failing run, when
  `TestMain` ends, then it checks only that no answer was unlisted.
- `scenario: undocumented-operation-refused`: given an operation with no summary, or with no answer but its success and
  `default`, when it is not on the list of operations not yet described, then `TestEveryOperationIsDescribed` fails.
- `scenario: undescribed-list-never-grows`: given a pull request that adds a line to that list, when CI runs, then the
  check fails.
- `scenario: no-adr-in-description`: given an operation, parameter or response description that names an ADR, when
  `TestEveryOperationIsDescribed` runs, then it fails and names the description.

## Scope

In: the operations that the control plane registers; `invokeFunction`, `POST /function/{name}` on the gateway; the HTTP
API section of each Spec tab; the guard; the rollout list; the pilot (KVStore and `invokeFunction`).

Out:
- Response bodies, statuses and headers: nothing changes on the wire. A machine-readable error cause is a later ADR.
- The errors of `ctx.*` calls in a handler (31 of the 153 rows): they belong in the Spec tabs' Handler API tables,
  which come from the shims' doc comments in funcd-typescript and funcd-python.
- The gateway's other calls: an http Function's other methods and paths (ADR-0243), calls through a Route's rules
  (ADR-0241), and `/health/` (ADR-0316).
- The S3 gateway's errors, which follow S3, and funcdctl's local errors, which the pages' CLI errors tables hold.
- An interactive page on the docs site (see Alternatives).

## Constraints & Decision drivers

- One source: an operation's registration says what it answers, and every page derives from it (ADR-0005).
- The pages live in funcd as plain Markdown, readable on GitHub and by agents (the docs decision of 2026-11-11).
- Tests enforce the reference; reviewers do not.
- No wire change.
- The guard runs on the in-memory platform, outside the e2e build, which is close to its time limit.

## Alternatives considered

- **Render the spec on the docs site with fumadocs-openapi.** It gives interactive pages, but it is a second renderer
  outside funcd, its pages are not in funcd or on GitHub, agents need its Markdown export, and a proof (static
  pre-rendering, search, the `llms` export) must come first. The design discussion preferred it, with the Go generator
  as a fallback; the decider chose the Go generator (2026-11-14). funcd's `/docs` page already gives an interactive
  view.
- **Shared status sets only**, as a utoipa API declares one response enum for every endpoint. Such sets list statuses
  that an endpoint never returns and miss others. They are kept as each verb's base, with each operation's own cases
  and the guard.
- **Error tables written by hand on the Usage pages.** #1463 moved them off because they drift.
- **A machine-readable cause in every error body now.** It changes the wire; the decider left it to a later ADR.
- **A separate Reference section for the API.** A kind's operations sit on its own page in the Kubernetes reference,
  and a reader of the KV Spec tab looks for KV's operations there.

## Decision

1. **Each operation describes its answers.** A described operation carries a summary, the security schemes `bearer`
   and `apiKey` (`X-Api-Key`), either of which authenticates, and one response for each status that it answers. Each
   response lists its cases, each with a problem type, when it happens and what the caller does, in the extension
   `x-funcd-cases`, and its description is written from them, so `/docs` shows them. `registerNamespacedCRUD`,
   `registerNamespacedRead` and `registerClusterCRUD` give each verb its shared cases, such as the 409 of a stale
   `If-Match` (ADR-0210) on replace and delete, but only for a kind whose operations are described; the others keep
   today's responses until their pull request. An operation adds its own cases, such as `handoverKVStore`'s 409 when
   the Workflow does not declare the store in `spec.kv`.
2. **The Function call is documented.** `SpecAPI` registers `invokeFunction`, `POST /function/{name}`, with the
   namespace from the `X-Funcd-Namespace` header or the namespace's host (ADR-0240), and a server whose URL is the
   variable `{gateway}`, the address that `server.dataPlaneAddr` sets. Its security is optional, since a namespace's
   stance decides it (ADR-0113). Its cases, each citing its ADR, are 200 with the handler's result, 400 (#90), 401 and
   403 (ADR-0113), 404 (ADR-0241), 413 (ADR-0134), 422 and 500 (ADR-0303), 429 (ADR-0112, ADR-0147), 503 (ADR-0112,
   ADR-0155, ADR-0176, ADR-0303) and 504 (ADR-0151); all but the 401 and 403 are rows of `docs/behavior/calls.md`. The
   422 and 500 bodies are `contractcheck.ContractViolation`, `{error, details}` as `application/json`: contractcheck
   marshals this named type instead of today's anonymous struct, with the same JSON. The control plane does not serve
   the call.
3. **Each Spec tab gets an HTTP API section.** `kindref` writes it after the kinds' tables and the Handler API. For each
   operation it shows a heading from the summary, the method, path, operation ID and authentication, a table of
   parameters, the body and the answer linked to their tables on the page, and a table of statuses with the problem
   type, when and what to do. The operations tagged `Hold` and `PlatformBackup` go on the disaster recovery tab, and
   those tagged `DeadLetter` on the events tab. The operations of `kindref.Omitted` kinds get no section and stay off the
   rollout list.
4. **The listed answers are the observed ones.** The test binary of `pkg/funcd` installs a recorder through a test-only
   hook, and `New` wraps the control plane's and the gateway's handlers outermost with it, so the 401 that the
   authentication middleware writes is seen; the wrapper forwards `Flush` and `Hijack`, so streams and upgrades still
   work. Each server resolves an answer by method and path against its own operations: the control plane against its
   registrations, the gateway against `invokeFunction`, which is only `POST /function/{name}`. An answer that resolves
   to no operation is not recorded. `TestMain` compares the answers with `SpecAPI().OpenAPI()`. It always fails when a described
   operation gave a status that it does not list. On a full run that passed, with no `-run` and no `-skip`, it also
   fails when a described operation lists a status that no test got. Each failure names the operation and the status.
5. **Rollout.** An operation is described when `testdata/undescribed-operations.txt` does not list it.
   `TestEveryOperationIsDescribed` fails when the list differs from `UndescribedOperations`, so a described operation
   leaves the list in the pull request that describes it. A CI step fails a pull request or a merge group whose list
   gains a line against its base commit. The list starts with every operation except KVStore's and `invokeFunction`.
   Each later pull request describes one feature; its deferred error rows are its checklist.
6. **Responses under huma.** `Describe` writes `op.Responses` and never `op.Errors`, which would make huma add 422 and
   500 and overwrite their descriptions. A problem case refers to `FaultError`, which huma registers on every
   operation; another case names its body schema, such as `ContractViolation`. A described operation lists no
   `default`: its listed answers are all it answers.
7. **Descriptions are for users.** A description of an operation, parameter or response that names an ADR fails
   `TestEveryOperationIsDescribed`. The `If-Match`, `dryRun` and ResourceGroup `force` descriptions lose theirs.

## Design model

None. The decision adds no lifecycle, race, timer or delivery; it describes answers and tests them.

## Behavior rows

None. No platform behavior changes; `invokeFunction`'s cases come from the rows of `calls.md`.

## Temporary workarounds

`testdata/undescribed-operations.txt` lets the rollout go one feature at a time. Exit: the list is empty, and the test
and the CI step drop it.

## Contracts

```go
// package controlplane

// ErrorCase is one documented failure of an operation.
type ErrorCase struct {
	Status int    // e.g. http.StatusConflict
	Type   string // the problem type, e.g. "urn:funcd:problem:conflict"; "" when Body names another schema
	Body   string // "" for a problem (FaultError); else the component schema of the body, e.g. "ContractViolation"
	When   string // one sentence, e.g. "The Workflow does not declare the store in spec.kv."
	Fix    string // one sentence, e.g. "Declare the store in the Workflow's spec.kv, then retry."
}

// Describe returns op with its summary, the bearer and apiKey schemes, and one response per status of cases, whose
// description is written from the cases and whose extension "x-funcd-cases" lists them. It never sets op.Errors.
func Describe(op huma.Operation, summary string, cases ...ErrorCase) huma.Operation
```

```go
// package kindref

// Render (ADR-0323) also writes each feature's HTTP API section.

// TagFeatures places the operations whose tag is not a kind: Hold and PlatformBackup on "disaster-recovery",
// DeadLetter on "services/events".
var TagFeatures map[string]string

// UndescribedOperations returns, sorted, the ID of every operation whose tag is not an Omitted kind and that has no
// summary, or no response but its success and default.
func UndescribedOperations(doc *huma.OpenAPI) []string
```

```go
// package contractcheck: the body of a call that breaks its contract, 422 for the input and 500 for the result.
type ContractViolation struct {
	Error   string            `json:"error"`
	Details []contract.Detail `json:"details"`
}
```

```go
// package funcd: the production side of the hook, read by New, which wraps both servers when it is set.
var answerRecorder atomic.Pointer[func(operationID string, status int)] //nolint:gochecknoglobals // set only by SetAnswerRecorderForTest

// In export_test.go: the test binary's hook; a server never sets it.
func SetAnswerRecorderForTest(record func(operationID string, status int))
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| Each operation's registration | Summaries, security, responses and `x-funcd-cases` in `api/openapi/funcd.v1alpha1.yaml` | API clients, `/docs` |
| The spec | The HTTP API section of `docs/guide/develop/**/spec.md` | funcd-frontend, agents, GitHub readers |
| The answers that `pkg/funcd`'s tests get | A failing `TestMain` on a mismatch | CI |

## Implementation plan

1. `internal/controlplane/describe.go`: `ErrorCase`, `Describe`, the two schemes registered once, and each verb's
   shared cases, which the three helpers apply to described kinds.
2. KVStore's operations, `handoverKVStore` included: summaries and their own cases, from the KV rows deferred by #1463.
3. `internal/controlplane/spec.go`: `invokeFunction` in `SpecAPI`, with `ContractViolation` and the gateway server;
   `internal/contractcheck`: `check` marshals `ContractViolation`.
4. `internal/controlplane/kindref`: the HTTP API section, `UndescribedOperations`, `testdata/undescribed-operations.txt`
   and `TestEveryOperationIsDescribed`. `scripts/check-list-only-shrinks.sh <base> <file>` and its ci.yml step, which
   fetches the base commit and fails when it cannot read it.
5. `pkg/funcd`: the recorder in `New`, `SetAnswerRecorderForTest` in `export_test.go`, `TestMain`'s comparison in
   `main_test.go`, and `api_answers_test.go`, which provokes each case of KVStore's operations and of `invokeFunction`
   outside the e2e build and never skips, with `WithRuntime` for the cases that need a worker and a short read timeout,
   set through a test-only option as `WithBucketQuotaForTest` does, for the 400 of #90.
6. The `If-Match`, `dryRun` and `force` descriptions reworded.
7. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Tests: `TestScenarioOperationShowsItsAnswers`, `TestScenarioFunctionCallDocumented`, `TestScenarioUnlistedAnswerFails`,
`TestScenarioListedAnswerUnprovenFails`, `TestScenarioFilteredRunChecksOnlyUnlisted`,
`TestScenarioUndocumentedOperationRefused`, `TestScenarioUndescribedListNeverGrows` (in `scripts/generated_test.go`, on
a scratch repository) and `TestScenarioNoADRInDescription`. The three guard scenarios run the comparison on a fixed set
of answers.

Definition of done: `just ci` passes; a second `just generate` changes nothing; the KV Spec tab shows the HTTP API
section of its operations; the spec holds `invokeFunction` with every case of Decision 2; the list holds every other
operation of a kind not omitted; `/docs` shows the cases of `replaceKVStore`; a run of one test passes.

## Review checklist

- [ ] Every KVStore operation and `invokeFunction` has a summary and lists each status that its tests get, and no other.
- [ ] Each listed case has a problem type or a body schema, a when and a fix.
- [ ] `invokeFunction` names the gateway server and optional security, and the control plane does not route it.
- [ ] No described operation has `op.Errors` or a `default` response.
- [ ] No response body, status or header changed.
- [ ] No description of an operation, parameter or response names an ADR.
- [ ] The recorder wraps both servers outermost and is set only by the test binary.
- [ ] A run filtered with `-run` passes when its tests pass.
- [ ] The guard's tests run outside the e2e build.

## Consequences

- A reader gets each described operation's answers from the Spec tab or `/docs`, and every listed answer is proven by
  a test. Operations not yet described keep today's responses.
- A new operation needs its cases and a test for each; a shared verb set keeps most of them to one line.
- The remaining features follow in one pull request each, about a dozen, each checked against its deferred rows.
- The guard sees only the calls that the tests make: an untested path can still return an unlisted status.
- A pull request that takes a kind out of `Omitted`, such as RuntimeClass (FEAT-0012 F139), or renames an operation
  not yet described, adds lines to the list, so it describes those operations at once.

## Open questions

- Each verb's shared set is the set that the pilot's tests observe on KVStore; the pilot's pull request confirms it.
- The 31 handler API rows: a pull request in each language repository, then a version bump in funcd.
- The S3 gateway's errors and the gateway's other calls: later sections of the Bucket, Function and Networking tabs.

## References

- pyvvo/funcd#1463: the Develop rewrite and its deferred error rows.
- `docs/behavior/calls.md`: the rows that list a call's answers and the ADR behind each.
- `api/fault/problem.go` (the problem types and statuses), `internal/controlplane/middleware/authn.go` (401),
  `internal/contractcheck/contractcheck.go` (422, 500).
- The fumadocs OpenAPI integration, the rejected alternative:
  https://www.fumadocs.dev/docs/react-router/integrations/openapi
