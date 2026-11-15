## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0330 rollout, SQL and the calls of another Function, round 2, model: claude-opus-5-5)

Round 2 reviews `52be45ab`, two commits on top of [round 1](adr-0330-rollout-sql-calls-claude-opus-5-5.md)
(`455f304b..52be45ab`). They change `internal/workernode/local/causes.go`, `internal/workernode/local/local.go`,
`pkg/funcd/calls_causes_test.go` and the two regenerated Spec tabs:

- `dc65adfa` declares `local.link-target-missing` on invoke and fetch, proves it with one row in each call matrix, and
  rewrites the comment that called it unreachable;
- `52be45ab` rewords `local.sql-engine-not-ready`, `local.sql-write-denied` and `local.sql-timeout`.

Round 1's Major and both Minors are closed, and no answer changed. No finding is open.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **The merge with the Blob and Secret item** · home: elsewhere · destination: the integration step. This is
  unchanged from round 1.
  - `git merge-tree` of `52be45ab` and the item's head `1ffc3607` conflicts in the same three files:
    `internal/controlplane/kindref/methods.go`, `internal/workernode/local/testdata/undescribed-endpoints.txt` and
    `pkg/funcd/local_causes_test.go`. `internal/workernode/local/causes.go` merges cleanly.
  - Merged, the rollout list holds no endpoint, so ADR-0330's Temporary workarounds asks that the list's test and its
    CI call go.
- **The six behaviors that round 1 routed as "not checked yet"** stay with the decider and the flake tracker #1142.
  Round 2 changes none of them, and the texts now describe the missing database's two answers correctly (round 1's
  Minor 1).

### ✅ Verified correct (keep it)

- **Round 1's Major is closed: invoke and fetch declare `local.link-target-missing`.**
  - `callCauses()` holds it, so both endpoints declare it, and `local.Endpoints` lists it on both.
  - The call matrices build their platform with `funcd.WithStore(store.New(memory.New()))`, the store that
    `funcd.InMemory` uses too. The setup creates `gone`, links it from `caller` as `g`, and deletes it through the
    store, as the collector does.
  - The delete passes no version, and the store's `Delete` then has no precondition (`internal/store/store.go`). A
    later status write cannot bring the Function back, because `Update` answers NotFound for an object that is gone.
  - Invoke has 9 rows and fetch 10, and `TestEveryDeclaredCauseHasARow` holds each matrix to one row per declared
    cause.
  - Round 1's scratch reproduction gets the same answers. Before the delete, invoke and fetch answer 200. After it,
    they answer 404 `workernode.local.resolve: link target default/ce does not exist`, and the same for
    `default/web`. `TestMain` now passes.
  - The new comment gives the real reason, ADR-0170's accepted risk that the collector and an App's prune delete
    through the store.
  - The new rows are deterministic: the call matrices passed `-race -count=20` at `GOMAXPROCS` 8 and 2, so the
    `local.link-target-missing` rows passed 40 of 40.
- **The new texts hold at their raise sites.**
  - `local.link-target-missing` (404). The when is "The Function that the link targets no longer exists, as when its
    App deleted it before the Function that links it." `protocolOf` raises it when the store no longer holds the
    target. Admission refuses a link to a Function that does not exist, so "no longer" is right.
  - Its fix is "Create the Function again, or link one that exists." It needs no `Except`:
    - TypeScript's invoke throws on any status that is not 2xx;
    - TypeScript's fetch throws when the answer lacks `X-Funcd-Fetch-Reply`, as funcd's refusal does;
    - Python's invoke raises `RuntimeError`;
    - both shims' docs already name "unknown target → 404".
  - `local.sql-engine-not-ready` (503) now reads "The database's engine is not Ready yet; a query also gets this answer
    when the database that its binding names does not exist." Round 1's probe shows this: a query on a missing
    database answers 503 with this cause. An execute on one never does, because a missing SQLDatabase has no `writers`
    and the `sql-single-writer` forbid fires first. The new comment says so.
  - `local.sql-write-denied` (403) now names a missing database, and its fix says to create the database. It is
    declared on execute only, where that case arises.
  - `local.sql-timeout` (503) now reads "The database's engine did not answer in time, as when a statement runs past
    sql.callTimeout." That holds at both raise sites, and for the deadlines of `Backups.HasTables` and
    `Prober.HasTables`. With the pinned Go 1.26.6, a client's own timeout also matches `context.DeadlineExceeded`, so
    `call` raises this cause for the prober too. The fix still names `sql.callTimeout`, the one deadline a caller of
    the local API can change.
- **The Spec tabs.**
  - The Function tab gains three 404 rows: under `invoke` in TypeScript and in Python, and under `fetch`.
  - The SQL tab rewords 14 rows, 7 in each language: the 503s of `query`, `queryValues` and `execute`, and the 403 of
    `execute`.
  - `just generate` exits 0 and leaves the tree clean.
- **No wire change.** Round 1's scratch probe, given to `go test -overlay` on `52be45ab`, got 69 answers that are
  byte-identical to the base's: the status, every header but `Date`, and the body.
- **Round 1's other checks still hold.** The method table and its `Except` lists did not change, and
  `TestScenarioEveryMethodMapped` passes.

### Checks run

| Check | Result |
| --- | --- |
| Round 1's scratch reproduction (`TestLinkTargetGoneScratch`, `go test -overlay`) | PASS; `TestMain` passes; 404 on both endpoints, details unchanged |
| `TestSQLCauseMatrix`, `TestInvokeCauseMatrix`, `TestFetchCauseMatrix` and `TestEveryDeclaredCauseHasARow`, `-race -count=3` | 171 PASS, 3 × (4 parents + 34 + 9 + 10 rows) |
| `TestInvokeCauseMatrix` and `TestFetchCauseMatrix`, `-race -count=20`, `GOMAXPROCS` 8 and 2 | ok; 40 of 40 runs of each new row passed |
| `go test -race -count=1` on `internal/workernode/local/...`, `internal/controlplane/kindref/...`, `internal/services/sql/...` and `internal/health/...` | 5 packages ok |
| `go vet`, host and `GOOS=linux`, on `internal/workernode/local/...`, `pkg/funcd/...` and `internal/controlplane/kindref/...` | exit 0 |
| `golangci-lint run` on the same packages, host and with `GOOS=linux` analysis | 0 issues |
| `GOOS=linux go build ./...` | exit 0 |
| `just generate` | exit 0; the tree was clean after it |
| Round 1's wire probe on `52be45ab` | 69 answers, byte-identical to the base |
| The full non-e2e `go test -race ./pkg/funcd`, `just ci`, e2e, `scripts/agent/gate.sh` | not rerun: round 2 asked for light runs, and the batch gate runs the rest. The implementer reports a full `pkg/funcd` pass in 93.5 s |

| Mutant (`go test -overlay`) | Failed |
| --- | --- |
| `callCauses()` drops `ErrLinkTargetMissing` | `TestEveryDeclaredCauseHasARow`: `the invoke matrix has a row for POST /invoke/{alias} and local.link-target-missing, which the endpoint does not declare`, and the same for fetch; `TestMain`: `POST /invoke/{alias} answered local.link-target-missing, which it does not declare`, and the same for `/fetch/` |

### Definition of Done

12 of the 13 items hold: the ADR's 6 Definition-of-done items and its 7 checklist items, read for these endpoints.

- "`just ci` passes": every part that this review ran passes. The recipe as a whole runs in the gate after this
  review.
- Round 1's miss is closed: the Function tab now shows the reachable 404 under `invoke` and `fetch`.

### Model scorecard

To be recorded with the batch's ledger rows: claude-opus-5-5 on ADR-0330 (implementation) → pass, 0/0/0, 0
model-attributed, DoD 12/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The rollout item is ready for the integrator. ADR-0330 is already Implemented and FEAT-0000/F02's handler
errors are `implemented`, so this review stamps nothing. FEAT-0000/F02 is an infra row, so no feature issue is
closed.
