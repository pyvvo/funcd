# ADR-0329: Each API operation documents its answers, and its feature's Spec tab shows them

- **Status**: Draft (2026-11-14)
- **Date**: 2026-11-14
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, codegen
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0194, ADR-0196 and ADR-0323 did)
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md) (the registrations are the source of the committed
  spec) · [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (the Spec tabs and
  `kindref`, which this extends) · [ADR-0210](0210-api-optimistic-concurrency-if-match.md) (a stale `If-Match` answers
  409) · [ADR-0220](0220-dry-run-engine.md) (`dryRun`) · [ADR-0058](0058-contract-codegen-from-code-types.md) (the
  contract check behind a call's 422 and 500) ·
  [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (behavior rows)

## Context & Need

funcd registers its HTTP API code-first with huma (ADR-0005), and `specgen` commits the spec,
`api/openapi/funcd.v1alpha1.yaml`. It holds 167 operations on 76 paths. None has a summary, a description or a security
entry, each lists only its success (200 or 204) and a `default` error, and the call of a Function, on the gateway, is
missing. A reader cannot learn from it what an operation does, how to authenticate, or which errors it returns, when,
and what to do about them. The rewrite of the Develop guide (#1463) moved 153 such error rows off the Usage pages for
this reference.

The readers are people and agents who write a client or debug a call. They read a feature's Spec tab, which `kindref`
generates (ADR-0323), or the `/docs` page that each funcd server serves, which huma renders from the same spec.

## Scenarios

- `scenario: operation-shows-its-answers`: given the KV Spec tab, when a reader looks up `replaceKVStore`, then they see
  its method, path, parameters and authentication, and one row for each status it answers, with the problem type, when
  it happens and what to do.
- `scenario: function-call-documented`: given the Function Spec tab, then `POST /function/{name}` shows its 200, its 422
  and 500 with their `{error, details}` bodies, and its 503.
- `scenario: unlisted-answer-fails`: given a described operation, when a test's call to it gets a status that the
  operation does not list, then the tests fail and name the operation and the status.
- `scenario: listed-answer-unproven-fails`: given a described operation that lists a status, when no test's call got
  that status, then the tests fail and name both.
- `scenario: undocumented-operation-refused`: given an operation with no summary, or with no answer but its success and
  `default`, when it is not on the list of operations not yet described, then `TestEveryOperationIsDescribed` fails.
- `scenario: no-adr-in-description`: given an operation, parameter or response description that names an ADR, then
  `TestEveryOperationIsDescribed` fails.

## Scope

In: the operations that the control plane registers, and the Function call on the gateway; the HTTP API section of each
Spec tab; the guard that holds the listed answers to the observed ones; the rollout list; the pilot (KVStore and the
Function call).

Out:
- Response bodies, statuses and headers: nothing changes on the wire. A machine-readable error cause is a later ADR.
- The errors of `ctx.*` calls in a handler (31 of the 153 rows): they belong in the Spec tabs' Handler API tables,
  which come from the shims' doc comments in funcd-typescript and funcd-python.
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

1. **Each operation describes its answers.** An operation carries a summary, the bearer security scheme, and one
   response for each status that it answers. Each response lists its cases, each with a problem type, when it happens
   and what the caller does, in the extension `x-funcd-cases`, and its description is written from them, so `/docs`
   shows them. `registerNamespacedCRUD`, `registerNamespacedRead` and `registerClusterCRUD` give each verb its shared
   cases; an operation adds its own, such as `replaceKVStore`'s 409 for a stale `If-Match` (ADR-0210).
2. **The Function call is documented.** `SpecAPI` registers `invokeFunction`, `POST /function/{name}`, with the gateway
   as its server. It answers 200 with the handler's result, 422 and 500 with `{error, details}` from the contract check
   (`internal/contractcheck`), and 503 with a problem from the activator when the Function failed or did not become ready
   in time. The control plane does not serve it; the spec and the Function Spec tab show it.
3. **Each Spec tab gets an HTTP API section.** `kindref` writes it after the kinds' tables and the Handler API. For each
   operation of the feature's kinds it shows a heading from the summary, the method, path, operation ID and
   authentication, a table of parameters, the body and the answer linked to their tables on the page, and a table of
   statuses with the problem type, when and what to do.
4. **The listed answers are the observed ones.** `pkg/funcd` records the operation ID and status of every answer that
   its tests' platforms give. Its `TestMain` fails when a described operation gave a status that it does not list, or
   lists a status that no test got, and names the operation and the status.
5. **Rollout.** `TestEveryOperationIsDescribed` fails on an operation with no summary, or with no answer but its success
   and `default`, that `testdata/undescribed-operations.txt` does not list, and on a listed operation that is
   described. The list starts with every operation except KVStore's and `invokeFunction`. Each later pull request
   describes one feature and removes its lines; that feature's deferred error rows are its checklist. A new operation is
   described when it is registered.
6. **Descriptions are for users.** A description of an operation, parameter or response that names an ADR fails
   `TestEveryOperationIsDescribed`. The `If-Match` and `dryRun` descriptions lose their ADR references.

## Design model

None. The decision adds no lifecycle, race, timer or delivery; it describes answers and tests them.

## Behavior rows

None. No platform behavior changes.

## Temporary workarounds

`testdata/undescribed-operations.txt` lets the rollout go one feature at a time. Exit: the list is empty, and the test
drops it.

## Contracts

```go
// package controlplane

// ErrorCase is one documented failure of an operation.
type ErrorCase struct {
	Status int    // e.g. http.StatusConflict
	Type   string // the problem type, e.g. "urn:funcd:problem:conflict"; "" for a body that is not a problem
	When   string // one sentence, e.g. "If-Match names an older version."
	Fix    string // one sentence, e.g. "Read the store again, then retry."
}

// Describe returns op with its summary, the bearer security scheme, and one response per status of cases, whose
// description is written from the cases and whose extension "x-funcd-cases" lists them.
func Describe(op huma.Operation, summary string, cases ...ErrorCase) huma.Operation
```

```go
// package kindref

// Render (ADR-0323) also writes each feature's HTTP API section, from the operations tagged with the feature's kinds.

// UndescribedOperations returns, sorted, the ID of every operation with no summary, or with no response but its
// success and default.
func UndescribedOperations(doc *huma.OpenAPI) []string
```

```go
// package funcd

// WithAnswerRecorder calls record with the operation ID and status of every answer that the platform's control plane
// and gateway give. Tests use it; a server never sets it.
func WithAnswerRecorder(record func(operationID string, status int)) Option
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| Each operation's registration | Summaries, security, responses and `x-funcd-cases` in `api/openapi/funcd.v1alpha1.yaml` | API clients, `/docs` |
| The spec | The HTTP API section of `docs/guide/develop/**/spec.md` | funcd-frontend, agents, GitHub readers |
| The answers that `pkg/funcd`'s tests get | A failing `TestMain` on a mismatch | CI |

## Implementation plan

1. `internal/controlplane/describe.go`: `ErrorCase`, `Describe`, the bearer scheme registered once, and the shared
   cases per verb, which the three helpers apply.
2. KVStore's operations, `handoverKVStore` included: summaries and their own cases, from the KV rows deferred by #1463.
3. `internal/controlplane/spec.go`: `invokeFunction` in `SpecAPI`.
4. `internal/controlplane/kindref`: the HTTP API section, `UndescribedOperations`, `testdata/undescribed-operations.txt`
   and `TestEveryOperationIsDescribed`.
5. `pkg/funcd`: `WithAnswerRecorder`; `api_answers_test.go`, which provokes each listed case of KVStore's operations
   and of `invokeFunction`; `TestMain` compares the answers with the spec.
6. The `If-Match` and `dryRun` descriptions reworded.
7. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Tests: `TestScenarioOperationShowsItsAnswers`, `TestScenarioFunctionCallDocumented`, `TestScenarioUnlistedAnswerFails`,
`TestScenarioListedAnswerUnprovenFails`, `TestScenarioUndocumentedOperationRefused` and
`TestScenarioNoADRInDescription`. The two guard scenarios run the comparison on a fixed set of answers.

Definition of done: `just ci` passes; a second `just generate` changes nothing; the KV Spec tab shows the HTTP API
section of its operations; the spec holds `invokeFunction`; the list holds every operation except the pilot's; `/docs`
shows the cases of `replaceKVStore`.

## Review checklist

- [ ] Every KVStore operation and `invokeFunction` has a summary and lists each status that its tests get, and no other.
- [ ] Each listed case has a problem type (or none for a body that is not a problem), a when and a fix.
- [ ] `invokeFunction` names the gateway as its server, and the control plane does not route it.
- [ ] No response body, status or header changed.
- [ ] No description of an operation, parameter or response names an ADR.
- [ ] The list of operations not yet described holds every other operation, and no line was added for a new one.
- [ ] The guard's tests run outside the e2e build.

## Consequences

- A reader gets each operation's answers from the Spec tab or `/docs`, and every answer is proven by a test.
- A new operation needs its cases and a test for each; a shared verb set keeps most of them to one line.
- The remaining features follow in one pull request each, about a dozen, each checked against its deferred rows.
- The guard sees only the calls that the tests make: an untested path can still return an unlisted status.

## Open questions

- Each verb's shared set is the set that the pilot's tests observe on KVStore; the pilot's pull request confirms it.
- The 31 handler API rows: a pull request in each language repository, then a version bump in funcd.
- The S3 gateway's errors: a later section of the Bucket Spec tab.

## References

- pyvvo/funcd#1463: the Develop rewrite and its deferred error rows.
- `api/fault/problem.go` (the problem types and statuses), `internal/controlplane/middleware/authn.go` (401),
  `internal/contractcheck/contractcheck.go` (422, 500), `internal/activator/activator.go` (503).
- The fumadocs OpenAPI integration, the rejected alternative:
  https://www.fumadocs.dev/docs/react-router/integrations/openapi
