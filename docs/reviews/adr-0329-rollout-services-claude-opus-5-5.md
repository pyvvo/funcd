## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0329 rollout, the Bucket, SQL, Lake, Secrets and Configuration features, model: claude-opus-5-5)

The work is one item of ADR-0329's rollout: commit `54500695` on `94f1821f`, branch `feat/rollout-0329-services`. It
describes the 25 operations of Bucket, SQLDatabase, Lake, Secret and ConfigMap, adds each kind's own cases, and proves
every case in five matrix tests in `pkg/funcd/services_answers_test.go`. Every check below ran on `54500695`.
ADR-0329 is already `Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: one of the two triggers of a SQLDatabase delete's "cannot ask" case has no test** · attribution: model.
  - The third 409 case of `deleteSQLDatabase` (`internal/controlplane/routes_rest.go:254`) names two triggers: the
    engine does not answer, or it is not Ready and the database has data.
  - Both reach one refusal (`internal/controlplane/admission/sqldatabase.go:97`) through two branches of
    `Prober.HasTables` (`internal/services/sql/reconcile.go:472`). Line 482 refuses an engine that is not Ready. Line
    494 passes on the error of `call`, which is `Unavailable` when a Ready engine does not answer
    (`internal/services/sql/client.go:51`).
  - The matrix's row proves the first branch. No test provokes the second: `git grep` for the refusal's text finds
    only the admission and the matrix.
  - A mutant shows the gap. When line 494 wraps the error as `fault.Internal`, a delete whose Ready engine does not
    answer gets 500, which `deleteSQLDatabase` does not list. The mutant survives the full non-e2e
    `go test ./pkg/funcd`, with both halves of the guard, and the tests of `internal/services/sql/...` and
    `internal/controlplane/admission`.
  - The work meets the bar of one row per case. Fix: split the case in two, each with its own row, as the work does
    for the Bucket's objects and catalog. `sqlFixtures` can make a second database Ready at an address that does not
    answer, for the second row. A silent Ready engine in `TestProberHasTables`
    (`internal/services/sql/sql_test.go:291`) would also pin the branch, outside the matrix.

### Routed (not this work's)

None.

### The implementer's choices that the ADR does not state

- **Tests named after their Spec tab** (`TestSQLAnswersMatrix`, `TestSecretsAnswersMatrix`,
  `TestConfigurationAnswersMatrix`): sound. The rollout gives each feature's Spec tab one matrix, and each of the five
  tabs holds one kind (`internal/controlplane/kindref/kindref.go:43-48`). `TestFunctionAnswersMatrix` is the
  precedent.
- **The cap cases inside each matrix, on a second platform**: sound. `answerRow.on` exists for this, and it puts each
  cap case under `runMatrix`'s check instead of a test of its own. The two matrices with a second platform took 0.50 s
  each, against 0.33 s for the other three.
- **Two Bucket cases, for a prefix that holds objects and for one that holds a Lake's catalog**: sound.
  - They are the two values that `blobProber.Remaining` reports (`pkg/funcd/funcd.go:2099`), at one refusal of each
    verb (`internal/controlplane/admission/bucket.go:120` and `:181`).
  - The fixes differ. Objects are deleted. A catalog is deleted through the S3 gateway, by a writer of the prefix, once
    no Lake is located there (`internal/blob/s3gateway/catalog.go:80`).
  - Each case has a row whose detail names its value.
- **One SQL "cannot ask the engine" case for two code branches**: acceptable under the bar, since both branches give
  the same status, problem type, message and fix. Minor 1 asks for the second branch's proof.
- **`madeByLive(kind, makers)` in `routes_rest.go`, which `madeByLiveOwner` now calls**: sound.
  - `ownedstore.IsMarker` (`internal/ownedstore/ownedstore.go:26`) counts a Lake and a Site as makers.
  - Both make Buckets (`internal/services/lake/rootentries.go:64`, `internal/site/materialize.go:37`). Neither makes a
    KVStore or a SQLDatabase.
  - `madeByLiveOwner` keeps its text, and the spec diff shows that no KVStore operation changed.
- **A fake SQL engine runtime in the test**: sound. The external test package cannot reach the fakes of the internal
  tests. This one is small: only `tables` becomes Ready, at a test server that counts one table. Its
  `NewDirQuarantine("")` is the documented store with no durable storage (`internal/provider/quarantine.go:33`).

### ✅ Verified correct (keep it)

- **No wire change.**
  - **The spec.** A Python diff of the spec at `94f1821f` and at `54500695` finds 168 operations on both sides, and
    exactly the 25 operations of the item changed.
    - Unchanged in each: the path, method, operation ID and tags, the parameters and the request body (descriptions
      aside), and the success response (its description aside).
    - Each has a summary and `security`, and none keeps a `default` response.
    - No component and no top-level key changed.
  - **The live answers.** A scratch probe sent the matrices' 243 requests, in order, to the head and to the head with
    `routes_rest.go` from `94f1821f`, through `go test -overlay`. The statuses, the `Content-Type` and `Allow` headers
    and the bodies, with volatile metadata removed, are identical. The only differences are two owner references'
    random UIDs.
  - huma reads `Responses`, `Summary` and `Security` only to write the document. Only `kindref` and `describe.go` read
    `x-funcd-cases`.
- **Each case is true to the code that answers it.** No admission other than these handles the five kinds: the
  pipeline's (`pkg/funcd/funcd.go:1052-1086`), the providers' (`pkg/funcd/binding_providers.go:240` and `:429`) and
  `validate` (`internal/controlplane/server.go:88`).
  - **Bucket.**
    - The cap: `countQuota` (`internal/controlplane/admission/kvstore.go:69`) with `defaultBucketsPerNamespace`, 100
      (`pkg/funcd/funcd.go:119`), which only a test hook changes. "100 Buckets, the most that funcd allows" holds.
    - A live owner: `refuseLiveMarked` (`internal/controlplane/kvhandover.go:89`). The makers are a Function, a
      Workflow, an App, a Lake and a Site.
    - A binding: only a Function's `spec.bindings.blob` is read (`admission/bucket.go:108` and `:171`).
    - Objects and a catalog: described above. The detail names the S3 bucket `funcd.lake-catalogs` and the key, as the
      fix says.
  - **SQLDatabase.**
    - The cap names `sql.maxDatabasesPerNamespace` (`internal/platform/config/config.go:444`).
    - A live owner: a Function, a Workflow or an App makes a SQLDatabase (`internal/function/rootentries.go:52`,
      `internal/workflow/rootentries.go:51`, `internal/app/reconcile.go:173`).
    - A binding, a table and the fail-closed check: `admission/sqldatabase.go:86`, `:101` and `:97`.
  - **Lake.** `lakeUndeclared` (`admission/bindingdeclared.go:252`) checks the blob bindings against `spec.buckets`,
    and a standalone Lake's config and secret bindings against `spec.configMaps` and `spec.secrets`. It answers 400.
    No admission handles a Lake's delete.
  - **Secret.** Only `validate` handles a Secret, and its handlers are the generic ones
    (`internal/controlplane/handlers.go:900-917`), so its verbs' cases are all of its answers.
  - **ConfigMap.** `refuseMigrationRecord` (`kvhandover.go:117`) runs after authorization in create, replace and
    delete (`handlers.go:107`, `:216` and `:362`), for `funcd-system/kvstore-marker-migration`
    (`internal/workflow/kvmigration.go:21-22`).
- **The matrices.**
  - 243 rows: 25 successes and 218 cases. Bucket has 53, SQL 50, Lake 47, Secrets 45 and Configuration 48.
  - Each row's detail names its refusal's message. `runMatrix` holds each row to one case and each case to one row,
    with no exemption.
  - The waits poll (`require.Eventually`, 10 s). The SQL reconciler records an engine's address before it writes
    `Ready` (`internal/services/sql/reconcile.go:172-173`), so the row of a database that holds a table never meets a
    Ready database without an address.
- **Mutants.** Five of six failed a test:
  - dropping the Bucket delete's catalog case (`routes_rest.go:219`) fails the matrix: `deleteBucket: one case holds
    "still holds a Lake's catalog"`;
  - the reserved ConfigMap answering 403 (`kvhandover.go:119`) fails its three rows: 409 expected, 403 received;
  - dropping the row of `replaceLake`'s undeclared binding fails the matrix: `cases with no row`, naming the case;
  - the fail-closed check answering 500 (`admission/sqldatabase.go:97`) fails its row, and on that filtered run
    `TestMain` reports `deleteSQLDatabase answered 500 Internal Server Error, which it does not list`;
  - `ReplaceBucket` without its live-owner guard (`handlers.go:979`) fails its row: 409 expected, 200 received.
  - The sixth survives, and Minor 1 describes it.
- **The pages.**
  - The five Spec tabs show the HTTP API section of their operations, and `just generate` changes nothing.
  - A scratch test on `controlplane.NewAPI` finds `/docs` answering 200 and the new cases in the `/openapi.json` that
    it reads.
  - No summary, description or case names an ADR (`TestScenarioNoADRInDescription`, and a script over the 218 cases),
    and each case has a problem type, a when and a fix.
- **The rollout list** loses exactly these 25 operations. `TestEveryOperationIsDescribed` passes, and so does
  `scripts/check-list-only-shrinks.sh` against `94f1821f`.
- **Conventions.**
  - The bloat audit passes with no hard flag. Its one soft flag is `_, _ = io.WriteString` in the fake engine's test
    handler.
  - Comment ratios: 4 comment lines to 39 code lines in production, 21 to 280 in tests. The comments are the helpers'
    doc comments and short reasons; none narrates the code.
  - The new helpers stay in the item's file, and `api_answers_test.go` is unchanged. The tests call `t.Parallel()`.

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the five matrices alone | ok, 3.8 s; once more with `-v`: 243 subtests pass, none skipped |
| `go test -race ./internal/controlplane/...` | ok: `controlplane` 17.3 s, `admission` 1.6 s, `specgen` 3.4 s, `kindref` 4.8 s |
| `go vet` on `./internal/controlplane/...` and `./pkg/funcd/`, also with `-tags e2e` | exit 0 |
| `golangci-lint run ./internal/controlplane/... ./pkg/funcd/...` | exit 0, 0 issues |
| `GOOS=linux go build ./...`, then `GOOS=linux go vet` on the touched packages | exit 0, exit 0 |
| `go test -race -count=1 ./pkg/funcd`, no filter: both halves of the guard | ok, 92.0 s |
| `just generate`, twice | exit 0 both times; the tree was clean after each |
| `just check-hygiene`; `gofmt -l` on the two Go files | clean; clean |
| The spec diff, and the live probe of 243 requests on the head and on the base routes | identical where it must be |
| Six overlay mutants | five failed a test; one survives (Minor 1) |
| `scripts/agent/audit.py --base 94f1821f --head HEAD --report-only` | PASS, 0 hard flags |
| `just ci`, `just ci-full`, the e2e suite, `go test ./...` | not run, as asked: the batch gate runs them |

### Definition of Done

18 of the 18 items checked here hold:

- ADR-0329's 7 Definition-of-done items and 9 Review-checklist items, applied to this item;
- three rulings of the decider that the ADR does not state: one row per case with no exemption, answers that only a
  shim writes left out, and the item's tests in a file of their own.

The items about `invokeFunction` and the recorder hold because this work leaves them unchanged. The 19th item,
"`just ci` passes", is left to the batch gate, as asked. The parts of it that this change reaches pass.

### Model scorecard

To be recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0329 (implementation, the services rollout) →
pass, 0/0/1, 1 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can close Minor 1 in this pull request or in the next item's. Nothing is stamped: ADR-0329 is already
`Implemented`.
