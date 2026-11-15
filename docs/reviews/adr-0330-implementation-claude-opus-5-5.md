## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0330 implementation, model: claude-opus-5-5)

The work is the KV pilot, plan steps 1 to 6. It adds `fault.KnownCause`, 25 known causes on the KV path and one for a
full disk, the interceptor, `local.Endpoints`, the rollout list, the two-sided `TestMain` guard, `TestKVCauseMatrix`
and the error rows of the Handler API tables. Every check below ran on branch `docs/adr-0330-local-api-errors` at
`26238650`, the range `22140229..26238650` (six commits). Blob, SQL, Secret, invoke and fetch get their causes in later
pull requests under this ADR, so their absence is no finding.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **Commit `8f4207ff` edits Decision 7, plan step 5 and the Review checklist after acceptance** · home: elsewhere ·
  destination: the decider · attribution: adr.
  - `git diff 22140229 8f4207ff` changes three things. Decision 7 goes from one matrix test to one matrix test per
    service. Plan step 5 renames `TestLocalAPICauseMatrix` to `TestKVCauseMatrix`. The last checklist item reads "its
    service's matrix test". The Status line records the decider's ask.
  - CLAUDE.md freezes an Accepted ADR's Decision. The edit records the decider's own instruction, given in chat after
    acceptance, and no reader has seen the earlier text: the ADR is not on main yet, and the queue squashes the pull
    request into one commit.
  - The pilot meets both texts. KV is the only described service, so its matrix is the one matrix. The texts differ
    for the work only in the test's name.
  - Ruling: keep the edit as part of the acceptance, or revert its three hunks and record the per-service matrix as a
    partial supersession (ADR-0272 Decision 7) before the Blob pull request. The work does not change either way.
- **A full disk on the KV write path, once Badger keeps the error's chain** · home: elsewhere · destination: the
  Badger version bump that ADR-0297 waits for (its Decision 3 and Open questions) · attribution: none, no defect today.
  - The work defines `diskfull.ErrDiskFull` (`internal/platform/diskfull/causes.go`), and `diskfull.Wrapf` raises it.
    `kvbadger`'s `submit` calls `diskfull.Wrapf` for every commit (`internal/kvstore/badger/badger.go:466`). No KV
    endpoint declares the cause.
  - That is right today. In the pinned Badger v4.9.6, `y.Wrap` formats its error with `%+v`, so a full disk reaches
    funcd as text and answers 500. ADR-0297 Decision 3 says the 507 takes effect only with the upstream release, and
    ADR-0330 Decision 7 does not declare a cause that no request can reach.
  - A scratch test, given to `go test -overlay`, ran the matrix's platform on a KV driver whose writes fail with
    `diskfull.Wrapf(syscall.ENOSPC, …)`. `PUT` and `DELETE /kv/b/k` answered 507. funcd logged "the local API
    answered a known cause that its endpoint does not declare" for each. `TestMain` failed: `PUT
    /kv/{binding}/{key...} answered diskfull.disk-full, which it does not declare`, and the same for `DELETE`.
  - The bump should then declare `diskfull.ErrDiskFull` on `PUT` and `DELETE /kv/{binding}/{key...}`, with a matrix
    row on such a driver, so that the five write methods show the 507. The guard cannot report it before that,
    because no test fills a disk. The ADR accepts that cost in its Consequences: "an untested path can still answer
    an undeclared cause".
- **`TestDependencyCheckReadsThePolicies` and `TestDependencyCheck_AForbiddenLinkFails` fail under host load** ·
  home: elsewhere · destination: the flake tracker #1142 · attribution: none.
  - Both tests wait for any 503 and then require the reason `Forbidden` (`pkg/funcd/health_internal_test.go`).
    When a check misses `DependencyCheckBudget` (50 ms, ADR-0215 Decision 4), the 503 says `Timeout` and ends the
    wait first. #1082, now closed, was the same kind of failure in an e2e test. No issue names these two tests. Whether
    this is a defect of the tests is not checked yet.
  - This branch does not make them fail more often. The runs are in *Flake runs* below. The KV matrix slows them less
    than two of main's own ADR-0329 tests do, and no run failed, even with every test on one CPU.

### ✅ Verified correct (keep it)

- **`api/fault`.**
  - `KnownCause`, `NewKnownCause`, the accessors, `Errorf`, `Wrapf`, `KnownCauseOf`, the `KnownCause` field and
    `(*Error).Is` match the Contracts.
  - `Error()` does not print the field. `Is` matches only that pointer, so an `errors.Is` on any other target behaves
    as before.
  - `TestScenarioCauseKeepsTheDetail` compares the answer byte for byte with the bare constructor's for eight kinds,
    and checks `errors.Is` through `%w`, `Wrapf`, another cause and `errors.Join`.
- **The causes and their sites.**
  - There are 25 causes on the KV path: 10 in `internal/kvstore`, 3 in `internal/quota` and 12 in `local`. Each is
    defined once.
  - Every bare constructor that the KV routes reached is now `Errorf` or `Wrapf` of a cause of the same kind, with the
    same format string. The constructors left in `internal/services/kv` run at construction, in `Reconcile`, or wrap
    the PDP's error with its own kind.
  - A deny picks `ErrReadDenied` or `ErrWriteDenied` by action, at run time, as Decision 1 says.
- **No wire change.**
  - A scratch test, given to `go test -overlay` on main (`edcafaf3`, the merge base) and on the branch, sent the
    matrix's 46 requests to the same platforms. It recorded the status, `Content-Type`, `ETag`, `Content-Length` and
    body of each answer.
  - The two outputs are identical once the opaque versions are masked. Those versions also differ between two runs on
    main.
- **The interceptor.**
  - Every endpoint registers through `handle`, and fetch through `wrap`. The only `fault.WriteProblem` calls left in
    the package are `wrap`'s and the pool's refusal (`manager.go:179`).
  - The 412 keeps its `ETag`, and the matrix checks it.
  - The warning fires once per endpoint and cause, or per endpoint and kind for an error with no cause, in the process.
    `TestScenarioUndeclaredCauseLogged` checks this across two handlers.
  - Internal errors and undescribed endpoints are never warned.
- **`Endpoints`, the list and the guard.**
  - `local.Endpoints` lists 17 endpoints. The 5 described ones are KV's four and health, which declares an empty
    list. The rollout list holds the other 12.
  - `TestEveryEndpointIsDescribedOrListed` checks the list in both directions. It also refuses a declared cause that
    is internal, declared twice, or that lacks a name, a when or a fix.
  - `scripts/check-list-only-shrinks.sh` passes against main, where the list does not exist yet, and against
    `cd49407d`. It fails on a list that grew, which a scratch worktree showed.
  - The full `pkg/funcd` run under `-race` passed with both halves of `compareCauses`.
  - `TestErrorRecorderIsSetOnlyByTests` finds no file outside the tests that calls the hook, and its fixture shows that
    it names such a file.
- **The matrix.**
  - `TestKVCauseMatrix` has 46 rows, one for each pair of KV endpoint and declared cause.
    `TestEveryDeclaredCauseHasARow` holds each declared cause to exactly one row, and refuses a row whose endpoint does
    not declare its cause.
  - Each row checks its status and problem type, and that the recorder saw its endpoint answer its cause under its own
    kind.
  - The parent and its rows run in parallel. The matrix takes 0.41 s alone and 2.51 s in the full run.
- **kindref.**
  - The method table matches the shims that `go.mod` pins, funcd-typescript v0.29.0 and funcd-python v0.24.0.
  - The get methods return null or None for a 404, the conditional methods return a value for a 412, and `put` sends
    `?ttl=` on the conditional path, so `put` shows the 503 for a driver with no conditional writes.
  - Both shims accept a ttl past `maxTTLSeconds`: up to 2^53−1 in TypeScript, with no bound in Python. So `put` shows
    `ttl-invalid`.
  - `del` leaves out the conditional causes, and `compareAndSet` and `delIf` show the malformed `If-Match`.
  - The KV Spec tab has 87 rows in each language, with no 404 under the get methods. The 500 sentence appears once
    per table on the five pages whose methods call funcd, and not on Lake's or Configuration's.
  - `Render` and `facetMux` keep their signatures.
- **Tracking.** After `8f4207ff`, the ADR's only edit is its status line, which is now at Reviewing. The F02 row reads
  "handler errors: reviewing".
- **The judgment calls hold.**
  - The table of methods excepts only what the shims return as a value or never send.
  - `maxTTLSeconds` names the bound that the ttl cause's text quotes. The generated values block of
    `docs/behavior/workers.md` gains its row, and `just check-behavior` passes. No behavior changed.
  - Two fake drivers reach `quota.unreadable` and a driver with no conditional writes. The raw socket reaches the
    broken chunked body.

### Checks run

| Check | Result |
| --- | --- |
| `go build ./...`, host and `GOOS=linux` | exit 0 |
| `go vet` on the 8 touched packages, host and `GOOS=linux`; `go vet -tags e2e ./pkg/funcd` | exit 0 |
| `golangci-lint run` on the touched packages, host and `GOOS=linux` | 0 issues |
| `go test -race -count=1` on `api/fault`, `internal/kvstore`, `internal/quota`, `internal/platform/diskfull`, `internal/services/kv`, `internal/workernode/local`, `internal/controlplane/kindref` | 7 packages ok |
| `go test -race -count=1 ./pkg/funcd`, no filter, so `TestMain` checks both halves | ok, 84 s |
| `TestKVCauseMatrix`, `TestEveryDeclaredCauseHasARow`, `TestScenarioFacetsMatch` and the two guard scenarios, `-race -count=5` | PASS each, 46 rows each run |
| The scenario tests of `fault`, `local`, `kindref` and `diskfull`, `-race -count=5` | PASS each |
| `just generate` | exit 0; the tree was clean after it |
| `just check-hygiene`; `just check-specs`; `just check-behavior`; `scripts/check-generated.sh`; `gofmt -l` on the changed Go files | clean; 19 specs, 0 failing; exit 0; exit 0; none |
| `just ci` as a whole, the e2e suite, `scripts/agent/gate.sh` | not run: the gate runs them after this review |

Each mutant ran through `go test -overlay`, so the tree was never changed. Each failed a test:

| Mutant | Failed |
| --- | --- |
| `PUT /kv` no longer declares `quota.ErrUnreadable` | `TestEveryDeclaredCauseHasARow`; `TestMain`: `PUT /kv/{binding}/{key...} answered quota.unreadable, which it does not declare`; `TestScenarioMethodShowsItsErrors` (12 rows, not 13) |
| A repeated `If-None-Match` raises `ErrIfNoneMatchNotStar` (same status and type) | `TestKVCauseMatrix/PUT_/kv/{binding}/{key...}_local.if-none-match-repeated` |
| The matrix loses its `ttl-invalid` row | `TestEveryDeclaredCauseHasARow`: `PUT /kv/{binding}/{key...} declares local.ttl-invalid, which no row of the kv matrix proves` |
| `wrap` passes a nil cause to the recorder | all 46 matrix rows; `TestMain`; `TestScenarioUndeclaredCauseLogged` |
| The get methods no longer except `local.ErrKeyNotFound` | `TestScenarioMethodShowsItsErrors`: "get returns null for a missing key" |
| `(*Error).Is` returns false | `TestScenarioCauseKeepsTheDetail`, in all five wrappings |

### Flake runs

The runs used one `-race` test binary for each tree. Each count ran its tests together, all in parallel:

| Tree, setting | Tests | Dependency tests passed | Their median time |
| --- | --- | --- | --- |
| branch | the two dependency tests, `-count=30` | 60 of 60 | 0.14 s |
| branch | the same and `TestKVCauseMatrix`, `-count=60` | 120 of 120 | 0.15 s |
| branch and main, `GOMAXPROCS=2`, then `GOMAXPROCS=1` | alone; branch with the matrix; main with `TestAnswersOfKVStoreOperations` and `TestAnswersOfInvokeFunction`; `-count=30` each | 60 of 60 in each of the 8 runs | 0.14 to 0.16 s |
| `GOMAXPROCS=1 -test.parallel=64`, every test on one CPU | branch alone | 60 of 60 | 0.28 s |
| same | branch with the matrix | 60 of 60 | 0.42 s |
| same | main with the two ADR-0329 tests | 60 of 60 | 0.55 s |
| same | main alone | 60 of 60 | 0.28 s |

- One matrix run costs 0.62 CPU-seconds. The two dependency tests together cost 0.34, and one
  `TestAnswersOfKVStoreOperations` costs 0.24.
- On the dependency check's own path, the branch adds one closure around `GET /health/dependencies`, which answers
  no error. Alone, the tests take the same time on both trees.
- The runs took about six minutes in all.

### Definition of Done

12 of the 13 items hold (the ADR's 6 Definition-of-done items and its 7 checklist items).

- "`just ci` passes": every part that this review ran passes. The recipe as a whole runs in the gate after this
  review.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0330 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 12/13. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Pass. This gate stamps the ADR `Implemented (2026-11-15)` and moves the F02 row's "handler errors" to `implemented`.

- The decider rules on `8f4207ff` before the pull request merges. Keeping it is the light option.
- The Badger bump that brings ADR-0297's upstream change declares the full-disk cause on the two KV write endpoints.

FEAT-0000/F02 is an infra row, so no feature issue exists, and the pull request closes none.
