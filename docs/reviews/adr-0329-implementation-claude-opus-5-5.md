## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0329 implementation, model: claude-opus-5-5)

The work describes KVStore's six operations and `invokeFunction`, adds the HTTP API section to the Spec tabs, and adds
the two-sided `TestMain` guard and the rollout list. Every check below ran on branch `docs/adr-0329-api-reference` at
`a7c1e41b`.

The `Implemented` stamp waits for the decider's ruling. Two routed `adr` findings make a Definition-of-done item and
a scenario clause impossible as written (Routed, first two items).

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: `invokeFunction`'s 401 gives one fix for two causes** · attribution: model.
  - In `internal/controlplane/spec.go`, the 401 case's When joins two causes: the call has no credential or an unknown
    one, or server.auth.edge is off. Its fix is "Send a credential of funcd's config…".
  - With server.auth.edge off, which is its default, an authenticated stance answers 401 whatever the call carries
    (`internal/dataplane/plugins.go:60-62`). For that cause the fix cannot work.
  - Fix: give server.auth.edge off a 401 case of its own. Its fix: the operator sets server.auth.edge to true. Its
    test exists already: `TestAnswersOfInvokeFunctionLimits/rate`.
- **Minor 2: the daemon links the spec builder for a test hook** · attribution: model.
  - `operationIDs` in `pkg/funcd/answers.go` calls `controlplane.SpecAPI()`. On main, only
    `internal/controlplane/cmd/specgen` calls it.
  - Built from this branch, `cmd/funcd` links `SpecAPI`, `NewStubHandlers`, the `RegisterStub*` routes and
    `addInvokeFunction`: `go tool nm` shows 4 of these symbols. With that one call removed through an overlay, it
    shows none.
  - The binary is 608,864 bytes larger: 110,202,738 against 109,593,874. A server never sets the hook.
  - `operationIDs` is also a second production global, with a nolint. The Contracts provide for one,
    `answerRecorder`.
  - Fix: `SetAnswerRecorderForTest` in `export_test.go` builds the table and stores it beside the hook, so only the
    test binary calls `SpecAPI`. Or resolve against the control plane's own registrations, as Decision 4 words it.
- **Minor 3: the HTTP API section's first sentence claims every operation** · attribution: model.
  - `httpSection` in `internal/controlplane/kindref/operations.go` writes "Each operation of these kinds, with its
    parameters, its body and its answer, and every status that it answers."
  - The section shows only described operations. That is right during the rollout, since Decision 3 builds each
    heading from a summary.
  - On the Function tab, it shows 1 of the 15 operations of Function, Revision and Invocation, so the sentence is
    false there.
  - Fix: write "Each described operation…", or say which operations are not described yet.

### Routed (not this work's)

- **Decision 2 cites ADR-0147 for `invokeFunction`'s 429** · home: elsewhere · destination: the decider (if the text
  must change, a partial supersession, ADR-0272 Decision 7) · attribution: adr.
  - ADR-0147 Decision 3 puts the nested-call cap on the worker node's local Invoker. It says "External requests never
    pass the Invoker, so are not counted". The calls.md row for nested calls cites the same decision.
  - No `POST /function/{name}` can get that 429. The work lists 429 with ADR-0112's rate case only, which
    `TestAnswersOfInvokeFunctionLimits/rate` gets.
  - The Definition-of-done item "the spec holds `invokeFunction` with every case of Decision 2" cannot hold for this
    case, unless the spec lists an answer that never happens.
- **The scenario `function-call-documented` asks for each answer's ADR on the page** · home: elsewhere · destination:
  the decider · attribution: adr.
  - Decision 7 fails a response description that names an ADR. Decision 1 writes each response's description from its
    cases.
  - The `ErrorCase` contract has no ADR field, and Decision 3's status table has no ADR column. The Spec tab cannot
    show the ADR.
  - The work shows each answer with its problem type or its body. It keeps each answer's ADR in the `adr` column of
    `TestScenarioFunctionCallDocumented`'s table, where a failure names it.
- **Decision 2's 503 list misses decided 503 answers of the same call** · home: elsewhere · destination: the decider,
  in the same ruling · attribution: adr.
  - Decision 2 lists the 503 cases of ADR-0112, ADR-0155, ADR-0176 and ADR-0303, from `docs/behavior/calls.md`.
  - Other behavior pages record five more:
    - a held namespace: `disaster-recovery.md`, ADR-0290 Decisions 1 and 2, with `Retry-After: 30`;
    - no ready worker within `invoke.activationTimeout`: `functions.md`, ADR-0161 Decision 4;
    - a Failed Function: `functions.md`, ADR-0169 Decision 5;
    - a call at a stop: `platform-lifecycle.md`, ADR-0028 Decision 3;
    - a Node pool worker past its heap: `limits.md`, ADR-0044.
  - The work follows Decision 2. 503 is listed and a test gets it, so the guard and the first checklist item hold.
  - The implementer reported the held-namespace case and listed it only through the 503 status. The other four were
    not reported.
  - A caller who gets one of these 503s finds no row for it. The ruling decides whether they join now, each with a
    test, or with the Function feature's pull request.
- **The cluster-scoped shared cases are not yet proven** · home: elsewhere · destination: the first pull request that
  describes a cluster-scoped kind.
  - No cluster-scoped kind passes `cases` to `registerClusterCRUD`. The spec's responses changed only on the seven
    described operations.
  - When such a kind is described, the full `TestMain` check requires a test for each status that it lists. Nothing
    is owed here.

### ✅ Verified correct (keep it)

- **`Describe` and the verb sets.**
  - `ErrorCase`, `Describe`, `TagFeatures`, `UndescribedOperations`, `ContractViolation`, `answerRecorder` and
    `SetAnswerRecorderForTest` match the Contracts.
  - `Describe` never sets `op.Errors`. huma adds no `default`, 422 or 500 of its own
    (`TestOnlyDescribedKindsLoseTheDefault`).
  - A kind that is not described keeps today's responses.
  - The full run proved every listed status of the seven operations. This answers the first open question for
    namespaced kinds.
- **The problem types are accurate.** Each documented type matches the fault kind of the code that writes the answer:
  - the authentication middleware's 401;
  - huma's 413 (`payload-too-large`) and its other errors (`about:blank`);
  - the gateway's `invalid`, `not-found`, `forbidden`, `payload-too-large`, `resource-exhausted`, `unavailable` and
    `deadline-exceeded`;
  - `ContractViolation` as `application/json`.
- **`invokeFunction`.**
  - Only `SpecAPI` holds it, through `doc.AddOperation`. Its server is `{gateway}`.
  - Its security is `[{}, bearer, apiKey]`, and it has the `ContractViolation` component.
  - The live API neither documents it nor routes it: `POST /function/echo` answers 404 on the control plane.
- **kindref.**
  - The HTTP API section comes after the Handler API.
  - It holds an authentication table built from the schemes, the parameters (path, then header, then query), and the
    body and the answer, each linked to its table.
  - `KVStoreHandover` and `ContractViolation` get nested tables.
  - The tags of all 168 operations, 35 in all, are each a Feature kind, a `TagFeatures` entry or an Omitted kind.
- **The rollout list.**
  - Its 136 lines are the 168 operations, minus the 7 described ones and the 25 of Omitted kinds.
  - `TestEveryOperationIsDescribed` checks the list in both directions.
  - `scripts/check-list-only-shrinks.sh` is tested on a scratch repository. Its CI step runs on `pull_request` and
    `merge_group`, fetches the base and fails when it cannot read it.
- **The guard.**
  - The recorder wraps both listeners outermost: the handler of `NewServer`, and the gateway outside `WriteStall`.
  - Only `export_test.go` sets it.
  - It resolves an operation by chi's route pattern, using `RawPath` as chi does, and the gateway's call by `POST
    /function/{name}`.
  - It forwards `Flush`, `Hijack` and `Unwrap`. Nested calls do not pass it.
- **No wire change.**
  - A scratch test, given to `go test -overlay` in `internal/contractcheck`, marshals `ContractViolation` and the old
    anonymous struct, with a copy of `Detail`'s old tags. It uses four inputs, with escaped characters, nil details and
    empty details. The bytes are identical.
  - A structural diff of the old and the new spec, a Python script over both YAML files:
    - responses changed only on the seven described operations, and their success responses are unchanged;
    - on the other 161 operations, only parameter descriptions changed: namespace on 122, name on 73, dryRun and
      If-Match on 58 each, force on 1;
    - in the components, `ContractViolation` and `Detail` are new, and `KVStoreHandover` has a new description.
  - Nothing reads `op.Security` at runtime.
  - Without the hook, both servers keep their handlers. The read timeout keeps `serverReadTimeout`.
- **The judgment calls hold.**
  - `-list` counts as a filtered run. Such a run runs no test, so a full check would fail every listed status. This
    fits Decision 4's "a full run that passed".
  - `WithGatewayReadTimeoutForTest` is what Implementation plan step 5 asks for: it sits in `export_test.go`, as
    `WithBucketQuotaForTest` does.
  - The doc tags on path parameters fill the parameter table. Those on `contract.Detail` fill the
    `ContractViolation` tables, which `TestEveryFieldIsDescribed` checks.
  - The HTTP API section shows only described operations (Minor 3 is about its first sentence).
- **The tests.**
  - The `TestAnswers*` tests run in parallel, each on its own platform, through the real servers.
  - Their times: KVStore operations 0.07 s, quota 0.06 s, `invokeFunction` 1.48 s, the two limit subtests 0.06 s
    each.
- **Tracking.** After acceptance, the ADR's only edit is its status line. The F02 row reads "operation answers:
  reviewing".

### Checks run

| Check | Result |
| --- | --- |
| `scripts/agent/d just generate`, twice | exit 0 both times (7 s, 5 s); `git status` empty after each |
| `go build ./...`; `go vet` and `golangci-lint` on the touched packages | exit 0; 0 issues |
| `go vet -tags e2e ./pkg/funcd` | exit 0 (the e2e suite did not run) |
| `go test -race -count=1` on `internal/controlplane/...`, `internal/contractcheck/...`, `internal/contract/...`, `scripts/...` | 8 packages ok |
| `go test -race -count=1 ./pkg/funcd`, no filter: the two-sided guard | ok, 79.7 s |
| `-run '^TestAnswersOfKVStoreQuota$'`; the three guard scenarios alone; `-list TestAnswers` | ok each |
| The eight `TestScenario*` tests, `-v` | PASS each |
| `go test ./api/... ./pkg/sdk/...`, which read the spec | ok |
| `just check-hygiene`; `scripts/check-generated.sh`; `gofmt -l` on the changed Go files | clean; exit 0; none |
| `just ci` as a whole, the e2e suite, `scripts/agent/gate.sh` | not run: the parent's gate runs them |

Each mutant ran through `go test -overlay`, so the tree was never changed. Each failed a test:

| Mutant | Failed |
| --- | --- |
| `compareAnswers` never flags an unlisted status | `TestScenarioUnlistedAnswerFails`, `TestScenarioFilteredRunChecksOnlyUnlisted` |
| `checksAll` ignores `-skip` | `TestScenarioFilteredRunChecksOnlyUnlisted` |
| `compareAnswers` skips its full side | `TestScenarioListedAnswerUnprovenFails` |
| The recorder reports the status plus 1 | `TestMain` on `TestAnswersOfKVStoreQuota` alone: `createKVStore answered 201 Created, which it does not list` |
| `listKVStores` lists a 409 that no test gets | `TestMain` on the full run: `listKVStores lists 409 Conflict, which no test got` |
| `Describe` sets `op.Errors` | `TestDescribeWritesTheResponsesOnly`, `TestOnlyDescribedKindsLoseTheDefault`, `TestDocsShowTheCasesOfReplaceKVStore`, `TestSpecGeneratedFromGo`, `TestScenarioOperationShowsItsAnswers` |
| `Describe` keeps one case per status | `TestDescribeWritesTheResponsesOnly`, `TestDocsShowTheCasesOfReplaceKVStore`, `TestSpecGeneratedFromGo`, `TestScenarioOperationShowsItsAnswers`, `TestScenarioFunctionCallDocumented` |

### Definition of Done

14 of the 16 items hold (the ADR's 7 Definition-of-done items and its 9 checklist items).

- "`just ci` passes": every part that this review ran passes. The recipe as a whole runs in the parent's gate.
- "The spec holds `invokeFunction` with every case of Decision 2": it holds every case that can happen. ADR-0147's
  429 cannot happen (Routed, adr).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0329 (implementation) → pass, 0/0/3, 3 model-attributed, DoD 14/16. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Pass. Do not stamp `Implemented` yet: the stamp waits for the decider's ruling on the three `adr` items. The builder
can fix the three Minors before the stamp. Minor 1 matters most, because later features copy the pilot's cases.

No feature issue exists. FEAT-0000/F02 has none, and the ADR's References cite #1463, a merged pull request. The pull
request closes no issue.
