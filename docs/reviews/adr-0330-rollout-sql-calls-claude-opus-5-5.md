## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0330 rollout, SQL and the calls of another Function, model: claude-opus-5-5)

The work rolls ADR-0330 out to `POST /sql/{binding}/query`, `POST /sql/{binding}/execute`, `POST /invoke/{alias}` and
`/fetch/`. It defines 23 known causes in `internal/workernode/local/causes.go`, raises them at their sites in
`internal/services/sql` and `internal/workernode/local`, declares them on the four endpoints, maps the methods in
`kindref`, and proves each declared cause with `TestSQLCauseMatrix` (34 rows), `TestInvokeCauseMatrix` (8) and
`TestFetchCauseMatrix` (9). Every check below ran on branch `feat/rollout-0330-sql-calls` at `f1737e36`, the range
`94f1821f..f1737e36` (four commits). The tree of `94f1821f` equals the tree of `origin/main`.

The answers did not change, and the method table matches the pinned shims. One cause that a request reaches is
declared nowhere.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Major 1: invoke and fetch leave out `local.link-target-missing`, which a request reaches** · attribution: model.
  - The comment on `ErrLinkTargetMissing` (`internal/workernode/local/causes.go:106`) says that no request reaches it,
    because admission refuses a link to a missing Function and the delete of a linked one (ADR-0064).
  - Admission is not the only way that a Function goes. ADR-0170 accepts as a risk that "the collector and
    `pruneFunctions` bypass API admissions (kvstore- and link-deletion-protection; …)" (its Risks accepted). The
    collector deletes each dead-owned object through the store (`internal/gc/gc.go:422`). So does an App's prune
    (`internal/app/reconcile.go:714`), which deletes a linked Function together with the part that links it
    (ADR-0199 Decision 6). When an App whose Function parts link one another is deleted (ADR-0199 Decision 7), its
    parts go one at a time, and a target can go before the part that links it.
  - A scratch test, given to `go test -overlay` on the branch, built a platform on a store that the test holds. It
    linked a caller to a cloudevent target and to an http target, and deleted both targets through the store, as the
    collector does.
    - `POST /invoke/e` answered 404 `workernode.local.resolve: link target default/ce does not exist`, and
      `GET /fetch/w/` the same for `default/web`.
    - `TestMain` failed: `POST /invoke/{alias} answered local.link-target-missing, which it does not declare`, and the
      same for `/fetch/`.
  - The rollout declares causes of the same reach elsewhere. A Function bound to a missing KVStore or SQLDatabase
    waits Pending (`internal/function/references.go:74`, `:87`; ADR-0233 Decision 1). Still, `kvstore.store-missing`
    is declared and proven, and the when of `local.sql-engine-not-ready` names a missing database.
  - ADR-0330 Decision 4 says that a described endpoint lists the causes it answers, and Decision 7 leaves out only a
    cause that no request can reach. As built, the Function tab shows no 404 under `invoke` or `fetch`. Yet invoke's
    own comment names it (`return err // Forbidden (no link) / NotFound (unknown target)`,
    `internal/workernode/local/local.go:229`), and so do both shims' docs, "unknown target → 404"
    (`funcd-typescript@v0.29.0/shim/src/invoke.ts:11`, `funcd-python@v0.24.0/shim/src/funcd_shim/invoke.py:49`).
  - Fix: declare `ErrLinkTargetMissing` on `POST /invoke/{alias}` and `/fetch/`, and prove it with one row in each
    matrix, on a platform built with `funcd.WithStore` whose setup deletes the link's target through the store. Then
    drop the comment's claim and regenerate the Function tab.
- **Minor 1: under `execute`, the 503 row says that a missing database answers it, but an execute answers 403** ·
  attribution: model.
  - The when of `local.sql-engine-not-ready` reads "The database that the binding names does not exist, or its engine
    is not Ready yet." Both SQL endpoints declare it, so the SQL tab shows it under `execute` in both languages.
  - A query on a binding whose database does not exist answers 503 with this cause. An execute answers 403
    `local.sql-write-denied` before that: the PDP gives a missing SQLDatabase no `writers`
    (`internal/auth/cedar/capabilities.go:545`), so the `sql-single-writer` forbid fires
    (`internal/auth/cedar/builtin_sql.cedar`).
  - The wire probe below shows both answers, on the base and on the branch: `query database-missing | 503 | … is not
    Ready`, and `execute database-missing | 403 | … is not authorized to sql::write SQLDatabase "nodb"`.
  - So the execute rows say that a missing database answers 503. The 403 row's when ("The Function does not own the
    database, no RolesAssignment makes it a writer, or a Policy forbids the write") does not name this case, and its
    fix sends the reader to a RolesAssignment.
  - The case is narrow: a Function bound to a missing database waits Pending (ADR-0233 Decision 1), so it arises only
    when the database goes while a Function that binds it runs.
  - Fix: word the two causes so that each method's rows hold, for example by limiting the "does not exist" half to a
    query and naming it in the write deny. The behavior itself is routed below.
- **Minor 2: `call` puts `local.sql-timeout` on the timeouts of the two `HasTables` probes, whose deadlines are not
  `sql.callTimeout`** · attribution: model.
  - `call` (`internal/services/sql/client.go`) now raises the engine causes itself. `Backups.HasTables` calls it
    under its own `hasTablesTimeout` (`internal/services/sql/backup.go:199`), and `Prober.HasTables` with a client
    bounded by `engineProbeTimeout` (`internal/services/sql/reconcile.go:489`). A timeout there carries
    `local.sql-timeout`, whose when says "The call ran past sql.callTimeout."
  - No behavior changed (see *Verified correct*), and today only the local API's interceptor reads a known cause.
  - Decision 1 names a cause where a wrap gives the error its meaning. Fix: raise the timeout cause where the facade's
    call meets it, or word its when so that it holds on every path that `call` serves.

### Routed (not this work's)

- **The merge with the Blob and Secret item** · home: elsewhere · destination: the integration step.
  - `git merge-tree` of `f1737e36` and `8ed42148` conflicts in three files:
    `internal/controlplane/kindref/methods.go`, `internal/workernode/local/testdata/undescribed-endpoints.txt` and
    `pkg/funcd/local_causes_test.go`. `internal/workernode/local/causes.go` merges cleanly.
  - The two `withKV` helpers behave the same, and both items except `local.body-unreadable` under each method that
    sends a body.
  - Merged, the rollout list holds no endpoint. ADR-0330's Temporary workarounds then asks that the list's test and
    its CI call go ("Exit: the list is empty, and the test and the CI call drop it").
- **Behavior that the implementer marked "surprising, not checked yet"** · home: elsewhere · destination: the
  decider, as questions, and the flake tracker #1142 for the test. Each is the same on the base, and none is checked
  yet.
  - A missing caller answers 403 `local.sql-caller-missing` on SQL, but 500 on invoke and fetch
    (`workernode.local.resolve: load caller function default/ghost`). The tabs show both as they are: invoke's and
    fetch's 500 falls under the tab's 500 sentence.
  - A binding to a missing SQLDatabase answers 503 on query and 403 on execute. Minor 1 is the texts; the behavior
    stays here.
  - A PDP error keeps its own kind (`facade.go` `authorize`, `local.go` `authorizeLink`). A kind other than internal
    would answer with no known cause, and funcd would log the warning; no test reaches it.
  - A `sql.callTimeout` that ends while funcd reads the engine's answer is answered `local.sql-engine-unavailable`
    ("the engine's answer broke off"), not `local.sql-timeout`. That row's when names an answer that broke off, so
    the tab does not misstate it.
  - The named-parameters branch of `validateStatements` cannot fail: a parameter that starts with `{` is part of a
    body that `readStatements` already parsed as JSON, so it always decodes as an object. Its raise stays
    `local.sql-statement-malformed`, and no row depends on it.
  - `TestScenarioEngineNotReadyUnavailable` under load: it passed 80 of 80 runs here, alone and beside the three
    matrices, under `-race -count=20` at `GOMAXPROCS` 8 and 2. Its dependency check has the same 50 ms budget as
    the `TestDependencyCheck*` family.

### ✅ Verified correct (keep it)

- **The raise sites.**
  - Every bare constructor on the four endpoints' paths is now `Errorf` or `Wrapf` of a cause of the same kind, with
    the same format string. The sites are in `local/sql.go` (`readStatements`), `local/local.go`,
    `local/resolver.go`, `local/invoker.go`, `local/nestedcap.go`, `services/sql/client.go`,
    `services/sql/facade.go` and `services/sql/resolver.go`.
  - The bare constructors that are left are internal errors, construction or registration refusals, the PDP's errors
    with their own kind, and `call`'s encode wrap, which no request reaches because the local API passes only
    statements that it parsed as JSON.
  - A deny picks `local.sql-read-denied` or `local.sql-write-denied` by action, at run time.
  - Each status matches its cause's kind: each matrix row checks its status and problem type against
    `fault.ToProblem` of its cause.
- **Where the SQL causes live.** `local` and `internal/services/sql` share only `api/fault`, `api/types/v1alpha1`,
  `internal/auth` and `internal/quota` (`go list -f '{{.Imports}}'`). None of them is SQL's, and `local` holds the SQL
  port (`local.SQL`), so `local.sql-*` follows Decision 2 as `kvstore.*` does for KV.
- **No wire change.**
  - A scratch test, given to `go test -overlay`, sent 69 requests to the matrices' platforms. The base run put the
    base versions of the 15 changed Go files back and left the two new test files out.
  - The requests were the 51 rows' requests and 18 more. On each SQL endpoint, they were a NaN parameter, `{}`, an
    empty SQL text, a named parameter that is a list, a missing database and a plain `SELECT 1`. The others were a
    missing caller on invoke and fetch, an empty fetch alias, and the dependency reports of three Functions.
  - The status, every header but `Date`, and the body of each answer are byte-identical on the base and the branch.
- **`ErrEngineNotReady`.**
  - It is now a known cause raised with `Wrapf` around `errNoEngine`, whose text is the old sentinel's. The error's
    text, the problem's detail (`sql.call: the engine of SQLDatabase "starting" is not Ready`) and the dependency
    report did not change.
  - The reports are byte-identical: `{"kind":"sql","binding":"starting","reason":"EngineNotReady","message":"the
    engine of SQLDatabase \"starting\" is not Ready: the database's engine is not ready"}`, the same for a missing
    database, and a Forbidden report for a read deny.
  - `errors.Is(err, local.ErrEngineNotReady)` holds through `(*Error).Is`, so `reasonOf` still answers
    `EngineNotReady`.
  - `TestFacadeRefusals`' new `EqualError` pins the text (mutant M4).
  - No production code wraps `local.ErrEngineNotReady` with `fault.Wrapf`, which would now print its name.
    `checker_test.go`'s fake is the one site that did, and it now builds the same error that the facade builds.
- **`call`'s other callers behave as before.**
  - `KnownCause.Errorf` sets the same `Kind`, `Op` and `Msg` as the bare constructor, so `KindOf`, `Error()` and every
    `errors.Is` against an earlier target behave as before.
  - The readers are the SQLDatabase delete admission (`KindOf(err) == Unavailable`, then `%v` in a Conflict), the
    backup workload, which returns the error, and the restore and run paths, which test `workload.ErrEngineNotReady`,
    a sentinel that `call` never raises.
  - The only production call of `fault.KnownCauseOf` is in the interceptor's `wrap`.
  - `internal/controlplane/admission`, `internal/services/backup`, `internal/backup/...` and `internal/quota` pass
    under `-race`.
- **The declarations.**
  - query declares 15 causes and execute the same 15 and 4 more: the write deny, the floor, the quota and its unread
    measure.
  - invoke declares 8: `callCauses()` and the target that serves HTTP. fetch declares 9: the malformed path,
    `callCauses()` and the target that does not serve HTTP. Neither declares the target's reply, as ADR-0330's Scope
    leaves it out.
  - `undescribed-endpoints.txt` loses its 4 lines and keeps Blob's and Secret's 8, and
    `scripts/check-list-only-shrinks.sh origin/main` passes.
  - The full `pkg/funcd` run, with no filter, passed both halves of `compareCauses`.
- **The matrices.**
  - Each row is one pair of endpoint and declared cause: 15 + 19 SQL, 8 invoke and 9 fetch.
    `TestEveryDeclaredCauseHasARow` holds each matrix to that.
  - Each row checks its status and problem type, and that the recorder saw its endpoint answer its own cause.
  - The parents and their rows run in parallel. The tests take 0.32 s (SQL) and 0.24 s (invoke, fetch) under `-race`.
    Both files carry `//go:build !e2e`.
  - The fake engines are named by what they do. The held call keeps the nested cap's one slot, so the 429 rows are
    deterministic.
- **The method table matches the pinned shims** (funcd-typescript v0.29.0, funcd-python v0.24.0).
  - TypeScript's SQL body is `JSON.stringify({ statements })` (`sql.ts:64`), which writes NaN and Infinity as null.
    Python's is `json.dumps({"statements": statements})` with its defaults (`sql.py:61`), which writes `NaN`. The
    probe's NaN body answers 400 `local.sql-body-malformed`. So only Python's SQL methods show that row, as the fourth
    commit has it.
  - Both shims' query methods send one statement, so they never meet no statement or more than 500. Both execute
    methods send a list, so they show both.
  - Every shim body goes whole with its length. TypeScript sets `content-length` (`socket.ts:31`, `invoke.ts`), and
    fetch reads the whole body first (`invoke.ts:124`). Python's `http.client` sets it for a bytes body. So no method
    shows `local.body-unreadable`, as Decision 7 asks for a cause that only a raw request provokes.
  - TypeScript's fetch refuses a path that does not start with `/` and encodes the alias (`invoke.ts:120`, `:137`),
    so its 404 row comes only from an empty alias. The probe's `/fetch//x` answers it.
  - Each shim throws on funcd's refusal, with its status and body. TypeScript's fetch tells a refusal by the missing
    `X-Funcd-Fetch-Reply` header.
  - The brief's narrower `Except` rule gave way to Decisions 7 and 8, as it should.
- **The Spec tabs.**
  - The SQL tab gains 39 rows in TypeScript (11, 11 and 17) and 42 in Python (12, 12 and 18). The Function tab gains
    15 (invoke 7, fetch 8) and 7.
  - Each row's when and fix match its raise site, but for Minor 1 and the missing 404 of Major 1. The names that the
    texts cite exist: `invoke.maxNestedInFlight`, `sql.callTimeout`, the SQL Data Writer role, the `cloudevent` and
    `http` protocols.
- **The shared files.**
  - `TestScenarioUndeclaredCauseFailsATest` now names a synthetic `GET /undescribed`, so the scenario does not depend
    on how far the rollout has gone. Its assertions are unchanged.
  - `withKV` changes only the kv matrix and keeps every other matrix.
  - `causeMatrices`, `fetchCauses()` and `causes.go` change only by additions.
- **Conventions.** There is no new dependency, no `any` in an API, no panic and no new global but the error
  sentinels. The comments say why, and the commit subjects are conventional.

### Checks run

| Check | Result |
| --- | --- |
| `go build ./...`, host and `GOOS=linux` | exit 0 |
| `go vet` on `internal/services/sql/...`, `internal/workernode/local/...`, `internal/controlplane/kindref/...`, `internal/health/...` and `pkg/funcd/...`, host and `GOOS=linux`; `go vet -tags e2e ./pkg/funcd` | exit 0 |
| `golangci-lint run` on the same packages, host and with `GOOS=linux` analysis | 0 issues |
| `go test -race -count=1` on the touched packages but `pkg/funcd`, and `api/fault` | 6 packages ok |
| `go test -race -count=1` on the readers of `call`'s errors | 11 packages ok |
| `TestSQLCauseMatrix`, `TestInvokeCauseMatrix`, `TestFetchCauseMatrix`, `-race -count=3` | 162 PASS, 3 × (3 parents + 51 rows) |
| `go test -race -count=1 ./pkg/funcd`, no filter | ok, 93.3 s |
| `just generate` | exit 0; the tree was clean after it |
| `just check-hygiene`; `gofmt -l` on the changed Go files; `scripts/check-list-only-shrinks.sh origin/main` on the list | clean; none; exit 0 |
| `just ci` as a whole, the e2e suite, `scripts/agent/gate.sh` | not run: the batch gate runs them |

Each mutant ran through `go test -overlay` or `go run -overlay`, so the tree was never changed:

| Mutant | Failed |
| --- | --- |
| M1: a statement past its timeout raises `local.sql-engine-unavailable` (same status and type) | `TestSQLCauseMatrix`, both `local.sql-timeout` rows: the recorder never saw the cause |
| M2: `callCauses()` drops `ErrLinkDenied` | `TestEveryDeclaredCauseHasARow`: `the invoke matrix has a row for POST /invoke/{alias} and local.link-denied, which the endpoint does not declare`, and the same for fetch; `TestMain`: `POST /invoke/{alias} answered local.link-denied, which it does not declare`, and the same for `/fetch/` |
| M3: TypeScript's `sql.execute` stops excepting `local.sql-body-malformed` | no Go test; the generated SQL tab gains one row under `execute`, which `scripts/check-generated.sh` refuses in `just ci` |
| M4: `CheckRead` raises `ErrEngineNotReady` with `Errorf`, around no error | `TestFacadeRefusals`: the text loses `: the database's engine is not ready` |

M3 shows that the SQL, invoke and fetch rows are pinned by the committed tab and its CI check, not by a Go test. The
ADR's scenario names only the KV tab.

### Definition of Done

11 of the 13 items hold: the ADR's 6 Definition-of-done items and its 7 checklist items, read for these endpoints.

- "`just ci` passes": every part that this review ran passes. The recipe as a whole runs in the gate after this
  review.
- "the Spec tab shows each method's rows": the invoke and fetch rows lack the reachable 404 of Major 1 (model).

### Model scorecard

To be recorded with the batch's ledger rows: claude-opus-5-5 on ADR-0330 (implementation) → changes-requested,
0/1/2, 3 model-attributed, DoD 11/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Changes requested, for Major 1: declare `local.link-target-missing` on invoke and fetch, prove it with a row in each
matrix, and regenerate the Function tab. The two Minors change a cause's text or the place where it is raised. The
ADR stays Implemented, and this review stamps nothing.
