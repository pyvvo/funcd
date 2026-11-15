## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0329 rollout, the services item, round 2, model: claude-opus-5-5)

Round 2 reviews commit `8b287ba6`, which follows [round 1](adr-0329-rollout-services-claude-opus-5-5.md). It changes:

- `internal/controlplane/routes_rest.go`: `deleteSQLDatabase` lists two 409 cases where it listed one, one per branch of
  the check that cannot ask the engine, with one fix;
- `pkg/funcd/services_answers_test.go`: `sqlFixtures` takes a map of Ready engines, a second database `silent` is
  Ready at an engine that closes each connection with no answer, and each of the two rows checks its own branch's
  detail;
- the regenerated spec and the SQL Spec tab.

Round 1's Minor is closed. One gap of the second case's text remains, which round 1 missed in the same words.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: the 409 of a Ready engine that answers with an error status has no case of its own and no test** ·
  attribution: model.
  - The second case says "The database's engine does not answer" (`internal/controlplane/routes_rest.go:257`). Its
    branch passes on the error of `call` (`internal/services/sql/reconcile.go:492`), which is `Unavailable` in four
    places of `internal/services/sql/client.go`:
    - no answer (`:51`), the branch that the new row provokes;
    - no answer in time (`:49`);
    - an answer that breaks off (`:56`);
    - an answer with a status other than 200 or 401 (`:62`).
  - The text holds for the first three. For the fourth, the engine does answer, with an error status, and the detail
    names it, such as `the engine answered 503`.
  - No test provokes the fourth. A mutant that makes `:62` `fault.Internal` survives the SQL matrix and the tests of
    `internal/services/sql/...` and `internal/controlplane/admission`. A delete would then answer 500, which the
    operation does not list.
  - Round 1 passed the same words in the case that this commit splits.
  - Fix: a third case, such as "The database's engine answers with an error, so funcd cannot check whether it holds a
    table.", with the same fix and a row of its own. `sqlFixtures` can make a third database Ready at an engine that
    answers 503.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Round 1's Minor is closed.** Round 1's mutant wraps the error at `internal/services/sql/reconcile.go:494` as
  `fault.Internal`. It now fails the new row, `deleteSQLDatabase/a database whose engine does not answer`: 409
  expected, 500 received. On the same filtered run, `TestMain` reports `deleteSQLDatabase answered 500 Internal Server
  Error, which it does not list`.
- **The two new cases are true to their raise sites.**
  - "The database's engine is not Ready and the database has data": `HasTables` finds no Ready engine
    (`reconcile.go:474`) and lists the database among the volumes with data, so it fails `Unavailable` (`:482`). The
    admission answers 409 (`internal/controlplane/admission/sqldatabase.go:97`).
  - "The database's engine does not answer": true for a connection that the engine closes, the new row's trigger, and
    for no answer in time. Minor 1 covers the error status.
  - "Retry once the SQLDatabase is Ready and its engine answers" fits both cases.
- **The rows.** Each row now checks its own branch's message: `the engine of default/pending is not Ready, so its
  data cannot be checked` and `the database's engine does not answer`. The two rows cannot pass for each other's
  case.
- **The fixtures.**
  - The matrix waits until both `tables` and `silent` are Ready. The reconciler records an engine's address before it
    writes `Ready` (`reconcile.go:172-173`).
  - The `ready` map is written once, before the platform starts, so `Converge` reads it with no lock. The capped
    platform's `sqlFixtures{}` has a nil map, which reads as no Ready engine.
  - The silent engine hijacks each connection and closes it at once, so no call waits for a timeout.
    `TestSQLAnswersMatrix` takes 0.46 s.
- **No wire change.** Since round 1, the spec changes only in the 409 response of `deleteSQLDatabase`. The other 167
  operations and the components are unchanged, and the Go change is the cases' text.
- **The matrices.** There are now 244 rows: Bucket 53, SQL 51, Lake 47, Secrets 45 and Configuration 48. All pass,
  and none is skipped.

### Checks run in round 2

| Check | Result |
| --- | --- |
| Round 1's mutant, through `-overlay`, on `TestSQLAnswersMatrix` | failed: 409 expected, 500 received; `TestMain` reports the unlisted 500 |
| A mutant on the error status (`client.go:62` as `fault.Internal`) | survives the SQL matrix, `internal/services/sql/...` and `internal/controlplane/admission` (Minor 1) |
| `go test -race -count=3` of the five matrices | ok, 3.5 s; once more with `-v`: 244 subtests pass, none skipped |
| `go test -race ./internal/controlplane/... ./internal/services/sql/...` | ok |
| `go vet` and `golangci-lint` on `internal/controlplane` and `pkg/funcd`; `GOOS=linux go vet` on both; `gofmt -l` | exit 0; 0 issues; exit 0; clean |
| `just generate` | exit 0; the tree was clean after it |
| The spec diff against round 1 | only `deleteSQLDatabase`'s 409 changed |
| The full `go test -race ./pkg/funcd`, `just ci`, the e2e suite | not run, as asked: the change adds no status, and the batch gate runs the rest |

### Definition of Done

The same 18 items as in round 1 hold. "`just ci` passes" is left to the batch gate, as in round 1.

### Model scorecard

To be recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0329 (implementation, the services rollout, round
2) → pass, 0/0/1, 1 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can close Minor 1 in this pull request with a third case and its row, or in the next item's. Nothing
is stamped: ADR-0329 is already `Implemented`.
