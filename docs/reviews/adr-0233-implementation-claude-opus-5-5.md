## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (ADR-0233 implementation, model: claude-opus-5-5)

Scope: the uncommitted working tree of `feat/sql-provider` against `2ff2f9d1` (ADR-0232, Implemented). The new code is
`internal/services/sql/` (`facade.go`, `resolver.go`, the binding count in `reconcile.go`),
`internal/workernode/local/sql.go`, `internal/auth/cedar/builtin_sql.cedar` with the `sql` capability, the SQL roles
and `scopeHead`, `roles.Lister.SQLWriters`, the binding types in `api/types/v1alpha1`, the existence gate, the pool
access key, the dependency check, the deletion-protection binding rule, `sql.callTimeout`, the wiring in
`pkg/funcd/funcd.go`, `internal/testkit/fakesql` and the `sql` lane (`scripts/lanes.yaml`, `e2e/sql.venom.yml`). The
shim halves are the released funcd-typescript v0.12.0 and funcd-python v0.9.0, and the lane's functions are
funcd-functions v0.9.0, all pinned in `go.mod`. The scenario tests are in `pkg/funcd/sql_access_internal_test.go`.

**Not stamped.** The ADR stays `Reviewing` and the FEAT-0011 F129 row stays `reviewing` until Major 1 is fixed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| modules | `go mod verify` | `all modules verified` |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `api/types/v1alpha1`, `cmd/funcd`, `internal/auth/...`, `internal/controlplane/admission`, `internal/function`, `internal/health`, `internal/platform/config`, `internal/pooling`, `internal/services/roles`, `internal/services/sql/...`, `internal/testkit/fakesql`, `internal/workernode/local`, `internal/workflow`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| scenario tests | the 13 `TestScenario…` tests of `sql_access_internal_test.go`, `-v` | 13 × PASS, 0.03 s to 0.11 s each, all `t.Parallel()` |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | empty |
| OpenAPI | `specgen` into a scratch file, compared with `api/openapi/funcd.v1alpha1.yaml` | identical |
| lane guards | `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` | `ok` × 2 |
| real-engine lane | the `sql` lane log of the builder's run on containerd (not rerun) | `final status: PASS`, S1 to S4 |
| engine address | a scratch test added through `go test -overlay` (the tree is unchanged): a `Query` against a closed engine port | the error contains the engine's `host:port` (Major 1) |

### 🔴 Blocker
None.

### 🟡 Major

1. **The engine's address reaches the sandbox and the logs** (`model`). When the engine refuses the connection or a
   read fails, `call` wraps the `net/http` error (`internal/services/sql/client.go:44`, `:49`). That error carries the
   request URL, so the message is `… Post "http://127.0.0.1:<port>/db/query?db_timeout=30s": dial tcp …: connection
   refused`. `writeSQL` logs `err.Error()` and writes it as the problem's `detail` (`internal/workernode/local/sql.go:110-111`;
   `api/fault/problem.go:51`), so the function receives the engine's address in its 503 body. A timeout of the HTTP
   client produces the same message. This breaks the first Review-checklist item ("the engine's address and password
   never appear in env, logs, status or responses") and Decision 4 ("the engine's credential and address never reach
   the sandbox"). The scratch test confirmed it; `TestFacadeEngineFailures` checks that the password is absent but not
   the address. Fix: return a `fault.Unavailablef` without the cause in the message (log the cause at the facade, under
   the database's name, without the URL), and assert in `TestFacadeEngineFailures` that neither the response nor the log
   contains the address.

### Minor

1. **The Contract's `[][]any` rows cannot be written as specified** (`adr`). The ADR's `QueryResult.Values` and
   `ExecResult.Values` are `[][]any`, which the repo's forbidigo rule and ADR-0002 forbid. The code uses
   `[][]json.RawMessage` (`internal/workernode/local/sql.go:40`, `:48`); the JSON on the wire is identical and the
   rows pass through without a decode. The Contract's `FacadeDeps` also omits the `CallTimeout` field that Decision 4's
   `sql.callTimeout` needs (`facade.go:32`). Both deviations are right; a superseding ADR, or the next ADR that cites
   these types, should use the shipped shapes.

### ✅ Verified correct (keep)

- The caller comes only from the socket: `registerSQL` takes the namespace and function from the sandbox's fixed
  `Ref`; the pool member path goes through `handlerFor`, and both shims send `X-Funcd-Member` in a pool
  (`pool.ts` `makeSQL(spec.name)`, `_poolworker.py` `SQLClient(_member)`).
- `query` goes only to `/db/query` and `execute` only to `/db/execute?transaction`, both with `db_timeout` set to
  `sql.callTimeout`; the call context and the HTTP client are bounded by the same timeout. A per-statement
  `query timeout` maps to Unavailable, any other statement error to Invalid naming the statement, a 401 to Internal
  with a log line that does not contain the password (`TestFacadeEngineFailures`).
- Every call asks Cedar; `execute` asks `sql::read` and then `sql::write` (`TestScenarioReadForbidBlocksExecute`). The
  resolver reads the caller's `spec.sql`, caches hits 5 s through the shared `services.BindingCache`, never caches a
  miss, and an unknown alias is Forbidden before any engine call.
- The size caps: a 1 MiB body, 500 statements and an 8 MiB engine answer each fail PayloadTooLarge; an array or object
  parameter, other than one named map, fails Invalid before any engine call.
- The Cedar capability: `SQLDatabase::"<ns>/<name>"` carries `namespace`, `resourceGroup`, `owner` and `writers`; the
  capability joins `defaultRegistry` and the root registry; `SQLDatabase` is a namespaced entity type, so an operator
  Policy naming it validates in its own namespace only (`TestSQLPolicyRevokes`). The built-ins match the ADR's Cedar
  block exactly.
- The roles: `SQL Data Reader` and `SQL Data Writer` at `SQLDatabase` and Namespace scope; `Reader`, `Contributor` and
  `Owner` give no SQL read or write (`TestSQLRoles`). `SQLWriters` counts only grants that hold `sql::write`, through
  the new `WriterGrant.Writes` (`TestSQLWriters`).
- The binding: `spec.sql` on Functions and image steps, validated as DNS-1123 aliases unique within `sql`; the
  materializer copies a step's bindings (`TestScenarioWorkflowStepBindsDatabase`); a missing database holds the
  Function with `SQLDatabaseNotFound` (`TestSQLBindingWaitsForDatabase`); `status.bindings` is recounted through the
  `MapFunction` watch.
- The pool access key: `spec.sql` and owned databases (`sql/<db>`) join the document with `omitempty`, so existing
  keys are unchanged; the SQLDatabase kind joins the `MapAccess` watches.
- Deletion protection refuses an API delete with 409 naming the Function and alias (`TestScenarioDeleteRefusedWhileBound`).
- The dependency check reports kind `sql`, reason `EngineNotReady` without an engine call
  (`TestScenarioEngineNotReadyUnavailable`).
- `local.WithSQL` as a variadic option keeps the existing callers of `NewHandler` and `NewManager` unchanged, which
  ADR-0002's functional options allow. Loading the node master earlier in `buildControlPlane` changes only its order;
  the S3 gateway and the catalog still receive the same key.
- The shims expose `query`, `queryValues`/`query_values` and `execute` with the single-statement and batch forms in
  both languages. The lane's functions use Drizzle's `sqlite-proxy` driver and SQLModel, as Implementation plan step 7
  says, and the lane runs all four required cases on containerd.
- No scope creep: nothing of F130 to F135.

### Definition of Done

13 / 14 items hold (the ADR's 10 Review-checklist items and 4 Definition-of-done items). Miss: "the engine's address
and password never appear in env, logs, status or responses" (Major 1, `model`). The repo-wide gate runs once per PR;
the touched packages' tests, vet, lint, the Linux build, `go mod verify` and `just check-hygiene` are green.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0233 (implementation) → changes-requested, 0/1/1, 1 model-attributed, DoD 13/14.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `adr-impl` for Major 1: a message without the wrapped URL and a test that asserts the address is absent from
the response and the log. The re-review is that test, the `internal/services/sql` and `internal/workernode/local`
tests and lint; then stamp ADR-0233 `Implemented` and F129 `implemented`.
