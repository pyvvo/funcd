## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the services item, round 3, model: claude-opus-5-5)

Round 3 reviews commit `1a77c301`, which follows [round 2](adr-0329-rollout-services-claude-opus-5-5-2.md). It changes:

- `internal/controlplane/routes_rest.go`: `deleteSQLDatabase` lists a third case of a check that cannot ask the
  engine, "The database's engine answers with an error", and the three cases share the fix "Retry once the
  SQLDatabase is Ready and its engine answers without an error.";
- `pkg/funcd/services_answers_test.go`: a third database, `failing`, is Ready at an engine that answers 503, and its
  row checks the detail `the engine answered 503`; the three test engines share one helper, `engineAt`;
- the regenerated spec and the SQL Spec tab.

Round 2's Minor is closed, and no finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **Whether the HTTP API section says once that an operation can fail with 500** · home: elsewhere · destination: the
  decider.
  - No described operation lists the 500 of funcd's own faults. ADR-0329 Decision 6 still says that a described
    operation's "listed answers are all it answers", and the section's first sentence promises "every status that it
    answers" (`internal/controlplane/kindref/operations.go:137`).
  - ADR-0330 Decision 5 settles this for the Handler API: an error of kind `internal` is never declared, and each table
    says once that a method can fail with 500. The HTTP API section has no such sentence.
  - Nothing of this item depends on the answer. The sentence would be shared by every described feature.

### The implementer's note: two branches of `call` stay without a case

The note holds under ADR-0329.

- **Both branches answer 500.** An engine that answers 401 fails `fault.Internalf`
  (`internal/services/sql/client.go:60`). An answer over 8 MiB fails `fault.PayloadTooLargef` (`:64`). The admission
  wraps every error but `Unavailable` as `fault.Internal`
  (`internal/controlplane/admission/sqldatabase.go:98-99`), and `fault.KindOf` reads the outermost kind
  (`api/fault/fault.go:79-88`), so a delete gets 500.
- **Both are funcd's own faults.** A 401 means that funcd's derived engine password is wrong ("a refused credential
  is funcd's fault", `internal/services/sql/sql_test.go:326`). An answer of 8 MiB to a count query means a broken
  engine. A caller can do nothing about either. Each of the three engine cases, by contrast, passes with a retry once
  the engine answers.
- **The rollout treats such faults alike.**
  - The pilot leaves them without a case: the KVStore's deletion protection answers 500 when it cannot list the
    Functions (`internal/controlplane/admission/kvstore.go:115`).
  - The pilot passed its review, and ADR-0329 is `Implemented`. Its Consequences accept that "an untested path can
    still return an unlisted status".
  - ADR-0330 Decision 5 decides the same for the local API.
- `HasTables`'s own internal faults, an unexpected answer and an undecodable count
  (`internal/services/sql/reconcile.go:497` and `:501`), are of the same class. The routed item above covers how the
  page could say so.

### ✅ Verified correct (keep it)

- **Round 2's Minor is closed.** Round 2's mutant turns `client.go:62` into `fault.Internal`. It now fails the new
  row, `deleteSQLDatabase/a database whose engine answers with an error`: 409 expected, 500 received. On the same
  filtered run, `TestMain` reports `deleteSQLDatabase answered 500 Internal Server Error, which it does not list`.
- **Round 1's mutant still fails.** It wraps the error at `internal/services/sql/reconcile.go:494`, which both
  Ready-engine rows now reach. The rows of `silent` and `failing` fail with 500, and `TestMain` reports the unlisted
  500.
- **The new case is true to its raise site.** `call` answers `Unavailable` for a status other than 200 and 401
  (`client.go:62`), and the admission answers 409 (`admission/sqldatabase.go:97`). The shared fix fits all three
  engine cases.
- **The rows.** The three engine rows check three different details: `is not Ready, so its data cannot be
  checked`, `the database's engine does not answer` and `the engine answered 503`. The `failing` engine answers at
  once. `TestSQLAnswersMatrix` takes 0.49 s.
- **No wire change.** Since round 2, the spec changes only in the 409 response of `deleteSQLDatabase`, which now
  holds 6 of the operation's 12 cases. The components are unchanged, and the Go change is the cases' text.
- **The matrices.** There are now 245 rows: Bucket 53, SQL 52, Lake 47, Secrets 45 and Configuration 48. All pass,
  and none is skipped.

### Checks run in round 3

| Check | Result |
| --- | --- |
| Round 2's mutant, through `-overlay`, on `TestSQLAnswersMatrix` | failed: 409 expected, 500 received; `TestMain` reports the unlisted 500 |
| Round 1's mutant, through `-overlay`, on `TestSQLAnswersMatrix` | failed: two rows, 409 expected, 500 received; `TestMain` reports the unlisted 500 |
| `go test -race -count=3` of the five matrices | ok, 3.6 s; once more with `-v`: 245 subtests pass, none skipped |
| `go test -race ./internal/controlplane/... ./internal/services/sql/...` | ok |
| `go vet` and `golangci-lint` on `internal/controlplane` and `pkg/funcd`; `gofmt -l` | exit 0; 0 issues; clean |
| `just generate` | exit 0; the tree was clean after it |
| The spec diff against round 2 | only `deleteSQLDatabase`'s 409 changed |
| The full `go test -race ./pkg/funcd`, `just ci`, the e2e suite | not run, as asked: the change adds no status, and the batch gate runs the rest |

### Definition of Done

The same 18 items as in rounds 1 and 2 hold. "`just ci` passes" is left to the batch gate.

### Model scorecard

To be recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0329 (implementation, the services rollout, round
3) → pass, 0/0/0, 0 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The batch gate runs next. Nothing is stamped: ADR-0329 is already `Implemented`.
