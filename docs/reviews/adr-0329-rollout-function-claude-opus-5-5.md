## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0329 rollout, the Function feature, model: claude-opus-5-5)

The work is the Function feature's pull request of ADR-0329's rollout, commit `e5497f4d` on `edcafaf3`. It describes
the 14 operations tagged Function, Revision or Invocation and registers Function and Revision through the generic
route helpers. It adds four of the five 503 cases that the decider ruled on 2026-11-15 to `invokeFunction`, and it
proves the cases with `TestFunctionAnswersMatrix`. Every check below ran on branch `feat/api-ref-function` at
`e5497f4d`. ADR-0329 is already `Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: `invokeFunction`'s cases sit in three tests, so the matrix exempts it from its check** · attribution:
  model.
  - The matrix has rows for the five new 503 cases of `invokeFunction`. The pilot's `TestAnswersOfInvokeFunction`
    and `TestAnswersOfInvokeFunctionLimits` still provoke its other cases.
  - Because of that split, `runMatrix(t, rows, "invokeFunction")` leaves `invokeFunction` out of the check that
    gives each case one row.
  - The effect, shown by a mutant: a sixth 503 case with no test passes the full `pkg/funcd` suite and the kindref
    tests. The matrix leaves `invokeFunction` out, and `TestMain` compares statuses only, while other cases already
    prove 503. A new status is caught: a 418 case fails `TestMain` on the full run.
  - The decider's layout rule gives each service one matrix test. Fix: fold the two invoke tests into the Function
    matrix, on answer environments of their own, and drop the exemption. The slow-body 400 and the in-flight 503
    open raw connections, so their rows need a hook of their own.

### Routed (not this work's)

- **The fifth 503 case, a Node pool worker past its heap (ADR-0044)** · home: elsewhere · destination: the decider ·
  attribution: adr.
  - funcd-typescript v0.29.0 writes this answer inside the worker. In `shim/src/pool.ts:370`, `fault()` answers each
    pending call 503 `function <name> worker faulted`.
  - A fake worker can only imitate that answer, so no non-e2e funcd test can prove it. No funcd test provokes it,
    e2e included: `git grep 'worker faulted'` finds none. The gap's reason holds.
  - The ruling asks for each case "with its test". The decider chooses: the case joins with its proof in
    funcd-typescript's tests or in an e2e test, or it stays out.
- **The shim's other answers of the call** · home: above · destination: the decider.
  - The gateway passes a body that is not JSON to the worker unchanged, for the shim to refuse
    (`internal/dataplane/normalize.go:48-61`, ADR-0134). The shim then answers 400 `invalid CloudEvent JSON`
    (`shim/src/shim.ts:103`, `shim/src/pool.ts:482`).
  - A handler that throws answers 500 with `{error}` (`shim/src/shim.ts:130` and `:164`, `shim/src/pool.ts:183` and
    `:499`).
  - The work treats both as worker answers outside the scope, which matches Decision 2's list. No behavior row lists
    them.
  - The ruling took in the heap 503, which is a shim answer too. Whether these two join the call's cases is the
    decider's choice. Their statuses, 400 and 500, are listed already.
- **Where the Function matrix sits** · home: elsewhere · destination: the decider.
  - The matrix and its helpers sit in `api_answers_test.go` (1,209 lines), with the KV pilot tests and the shared
    helpers.
  - The layout rule's text is not in the repository. If only small matrices share a file, the Function matrix takes
    a file of its own. Under any other reading, it conforms.

### ✅ Verified correct (keep it)

- **No wire change.**
  - **The spec.** A Python diff of the old and the new spec (`edcafaf3` against `e5497f4d`) finds 168 operations on
    both sides and 15 that changed: the 14 operations of the feature and `invokeFunction`.
    - Unchanged: operation IDs, tags, request bodies and success responses, and each parameter's name, place,
      required flag, schema, explode and style (descriptions aside).
    - In the components, no schema is new or gone. `Line` and `LogsOutputBody` gain descriptions only.
    - Besides summaries, descriptions and responses, `Describe` adds `security`, which is documentation only: no
      code reads it at runtime.
  - **The live behavior.** A scratch test sent the same 36 requests to `edcafaf3`, built from a `git archive`, and to
    `e5497f4d`, through `go test -overlay`.
    - The requests: a Function's create, read, list, replace and delete with their refusals, `dryRun` and PATCH; a
      Revision's list and get, with POST and DELETE answering 405 and `Allow: GET`; the logs, with a bad `since`,
      `severity` and `limit`; a volume's delete and its refusals.
    - The statuses, the `Content-Type` and `Allow` headers and the bodies are identical, with each object's status
      and volatile metadata removed.
    - On the branch, `TestMain` found none of the 36 answers unlisted.
  - **The generic helpers** do what the hand-registered routes did:
    - create checks the path's namespace, then applies `dryRun`;
    - replace checks If-Match against the metadata;
    - delete passes If-Match;
    - Revision registers list and get only.
- **The four new 503 rows are deterministic.**
  - **The two hold cases.** The rows set the hold markers before they run. The gateway refuses after the stance and
    the lookup, before any wake.
  - **No ready worker.** `never` has no fake worker that listens, so no worker becomes ready.
    `WithPacing(ActivationTimeout: 500ms)` bounds the wait. The gateway reads the store (`serveByName`), so the new
    Function is found at once.
  - **A Failed Function.** The row waits for the phase `Failed` before it calls. A wake that already holds a call
    ends at its next poll with the same 503.
  - **A call at a stop.**
    - `stopOnWake` stops the platform only once the call's wake has created a worker. The call is then held in the
      activator, and the stop releases it.
    - Only that row uses this platform. `stoppablePlatform`'s stop is a `sync.OnceFunc` that the cleanup shares.
  - **20 runs under `-race`, all ok:** 10 alone (30.2 s), and 10 beside the full race run (26.7 s).
- **The matrix.**
  - It has 133 rows. Each row's When matches one case, and each case of the rows' operations has one row, apart from
    `invokeFunction`.
  - Each row takes its status and its problem type from the document, so the problem types are now checked against
    the answers. The pilot did not check them.
- **The cases match the answers.**
  - The full non-e2e `go test -race ./pkg/funcd` passed with both guards, in 98.2 s.
  - Four mutants failed a test:
    - removing the `deleteFunction` link row fails the matrix: `cases with no row`;
    - an untested 418 on `invokeFunction` fails `TestMain` on the full run: `invokeFunction lists 418 I'm a teapot,
      which no test got`;
    - rewording the Failed 503 fails the matrix: `one case holds "is Failed"`;
    - the activator answering a Failed Function with 500 fails its row on the status.
  - A fifth mutant survives, and Minor 1 describes it.
- **Conventions.**
  - The bloat audit (`scripts/agent/audit.py --report-only`, against `edcafaf3`) passes with no hard flag.
  - Its one soft flag is the 10 ms sleep in `stopOnWake`'s loop. The loop polls the condition and has a 20 s
    deadline, as pitfall 5 asks. `require.Eventually` cannot run there, because the loop runs outside the test
    goroutine.
  - Comment ratios: 34 comment lines to 498 code lines in tests, 4 to 90 in production. No `nolint` is new.
  - No helper is duplicated within the build. `phaseOf` shares its name with a helper of another signature in the e2e
    build (`dataplane_e2e_test.go:77`), which never compiles with it.
- **The rollout list and the page.**
  - The list goes from 136 to 122 lines, and `TestEveryOperationIsDescribed` passes.
  - The Function Spec tab shows all 15 operations, and `TestScenarioFunctionCallDocumented` checks the five new rows.

### Checks run

| Check | Result |
| --- | --- |
| `scripts/agent/d just generate`, twice | exit 0 both times; the tree was clean after each |
| `go build ./...`; `go vet` and `golangci-lint` on the touched packages; `go vet -tags e2e ./pkg/funcd` | exit 0; 0 issues |
| `just ci` | exit 0, 585 s, with `pkg/funcd` ok in 66.9 s; the tree was clean after it |
| `go test -race -count=1 ./pkg/funcd`, no filter: the two-sided guard | ok, 98.2 s |
| `go test -race -count=10 -run '^TestFunctionAnswersMatrix$'`, alone, then beside the full race run | ok, 30.2 s and 26.7 s |
| The spec diff and the live probe of 36 requests on both commits | identical where it must be |
| `scripts/agent/audit.py --base edcafaf3 --head HEAD --report-only` | PASS, 0 hard flags |
| The e2e suite, `scripts/agent/gate.sh` | not run: the parent's gate runs them |

### Definition of Done

16 of the 17 items hold: ADR-0329's 7 Definition-of-done items and 9 checklist items, applied to this feature, and
the ruling's item 3. The ruling's item 3 holds for four of its five cases. The fifth, the heap 503, is routed above
(adr).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0329 (implementation, the Function rollout) → pass, 0/0/1, 1 model-attributed,
DoD 16/17. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can fold the invoke tests into the matrix (Minor 1) in this pull request or in the next feature's.
The heap 503, the shim's 400 and 500, and the matrix's file go to the decider. Nothing is stamped: ADR-0329 is
already `Implemented`.
