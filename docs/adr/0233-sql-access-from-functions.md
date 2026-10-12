# ADR-0233: SQL access from functions

- **Status**: Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  made `execute` also ask `sql::read`, since `RETURNING` reads, added `sql.callTimeout`, and fixed the Cedar entity
  shape and the Namespace-scope roles; the second found no Blocker or Major)
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, authorization, local-api, shim
- **Realizes**: [FEAT-0011/F129](../feat/0011-feat-sql.md) (SQL from functions)
- **Relates to**: [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (the engine, its registry and password;
  its deletion protection gains the binding rule here) · [ADR-0069](0069-kv-data-plane.md) (the local API) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md) (binding = read, owner = write) ·
  [ADR-0074](0074-cedar-authorization-resource-access.md) / [ADR-0116](0116-capability-authorization-framework.md) (Cedar
  and its registry) · [ADR-0136](0136-roles-and-role-assignments.md) (roles) · [ADR-0215](0215-built-in-health.md) (the
  dependency check) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (shim releases) ·
  [ADR-0178](0178-a-workflow-adopts-only-its-own-kv-stores.md) (not applied to databases) · source:
  [design report](../reports/sql-provider-design.md)

## Context & Need

A `SQLDatabase` runs its own engine (ADR-0232), but no function can use it. This ADR lets a function bind a database by
name and run SQL on it from TypeScript and Python, with the KV store's access model: the binding grants reads, the
database's owner and writer roles grant writes, Cedar decides every call, and the function never holds the engine's
credential. The callers are handlers, migration hooks and Workflow steps; SQL from a query builder that only generates
SQL works as it is.

## Scenarios

- `scenario: bound-function-reads` — Given `todo-api` binds `todo-db` as `app` and the table `todos` has rows, When it
  calls `context.sql.query("app", "SELECT id, title FROM todos WHERE done = ?", [0])`, Then it gets the rows as objects.
- `scenario: owner-writes` — Given `todo-db` names `todo-api` as its owner, When `todo-api` calls
  `context.sql.execute("app", "INSERT INTO todos(title, done) VALUES(?, ?)", ["milk", 0])`, Then the row is written and
  the call returns `rowsAffected` 1 and the new row's id.
- `scenario: non-writer-write-refused` — Given `todo-report` binds `todo-db` and is neither its owner nor granted a
  writer role, When it calls `execute`, Then the call fails Forbidden and nothing is written; its `query` succeeds.
- `scenario: writer-role-grants-write` — Given `todo-migrate` binds `todo-db` and a RolesAssignment gives it
  `SQL Data Writer` on `todo-db`,
  When `todo-migrate` calls `execute`, Then the write succeeds.
- `scenario: unbound-alias-refused` — When a function calls `context.sql.query("other", …)` with no binding `other`,
  Then the call fails Forbidden.
- `scenario: query-cannot-write` — When a bound function sends `DELETE FROM todos` through `query`, Then the call fails
  Invalid and no row is deleted.
- `scenario: batch-is-all-or-nothing` — When the owner calls `execute` with three statements and the second fails,
  Then the call fails Invalid naming statement 1 and none of the three is applied.
- `scenario: named-parameters` — When a function calls `query` with `:done` in the SQL and `{"done": 0}`, as a
  compile-only query builder emits, Then it gets the same rows as with `?` and `[0]`.
- `scenario: engine-not-ready-unavailable` — Given the database's engine is not Ready, When a bound function calls
  `query`, Then the call fails Unavailable, and the function's dependency check reports the binding.
- `scenario: workflow-step-binds-database` — Given a Workflow whose image step binds `todo-db`, When the step runs, Then
  it reads the database like a bound function.
- `scenario: delete-refused-while-bound` — Given a Function binds `todo-db`, When a client deletes `todo-db` through
  the API, Then the API answers 409 naming the Function.
- `scenario: operator-policy-revokes` — Given a Policy forbids `sql::write` on `todo-db`, When the owner calls
  `execute`, Then the call fails Forbidden.
- `scenario: read-forbid-blocks-execute` — Given a Policy forbids `sql::read` on `todo-db` for its owner, When the
  owner calls `execute` with `UPDATE … RETURNING *`, Then the call fails Forbidden and nothing changes.

## Scope

**In**: the binding on Functions and image steps; the database's owner; the local API routes; the SQL facade (the
PEP); the Cedar capability, built-in policies and roles; the dependency check; the Function reconciler's existence
gate; the binding count; the shim helpers in both languages; the real-engine lane.

**Out**: the App section and migrations (F130); backup (F131); the change feed (F132); `funcdctl dev` (F133); external
clients (F134); ORM adapters; transactions across calls.

## Constraints & Decision drivers

- One access model for KV, blob and SQL (FEAT-0011): binding = read, owner and roles = write, Cedar on every call.
- No database credential in the sandbox; the caller comes from its socket (`internal/workernode/local/local.go:136-140`).
- rqlite runs `/db/query` on a read-only connection and a transaction within one request only (design report).
- Shim changes ship as releases of funcd-typescript and funcd-python, then `go get` (ADR-0141).

## Alternatives considered

1. **A URL and a token in the env, through a PEP proxy, as the catalog does**: any rqlite client would work, but a
   bearer token sits in the sandbox. Rejected (decider, 2026-10-12); ORMs are optional.
2. **Checking the tables of each statement**: not needed with one engine per database; the read/write split rides on
   rqlite's read-only endpoint. Kept for a shared server (F135).
3. **Workflow-owned databases only, as ADR-0178 does for KV stores**: ADR-0178 exists because a table's owner names a
   step; a database's owner is a Function and shared data is the point. Rejected: an image step binds any database of
   its namespace, as it binds a catalog.
4. **A writers list on the database**: simpler for an App than a RolesAssignment, but a second way to grant writes.
   Rejected for the KV model: one `owner` plus writer roles.

## Decision

1. **The binding.** `Function.spec.sql[]` and `steps[].function.sql[]` hold `{alias, database}`; the materializer copies
   a step's bindings to its Function (`internal/workflow/reconcile_workflow.go:477`). A Function bound to a missing
   database waits Pending with `SQLDatabaseNotFound` (`internal/function/references.go`). The database's
   `status.bindings` counts the Functions that bind it.
2. **The owner.** `SQLDatabase.spec.owner` (optional) names the Function that may write, as a KV table's owner does.
3. **The local API.** `POST /sql/{binding}/query` and `POST /sql/{binding}/execute` take
   `{"statements": [...]}`, each statement `["SQL", p1, p2, …]` or `["SQL", {"name": value}]`, values being JSON
   strings, numbers, booleans or null. A body over 1 MiB or more than 500 statements fails PayloadTooLarge.
4. **The facade (the PEP).** For each call: resolve the alias through the caller's `spec.sql` (a miss is Forbidden, hits
   are cached 5 s, as KV); ask Cedar `sql::read` for `query`, and both `sql::read` and `sql::write` for `execute`, since
   `RETURNING` reads, on `SQLDatabase::"<ns>/<name>"` with the socket's principal; look up the engine in the `Engines` registry (absent ⇒
   Unavailable); call the engine with user `funcd` and the database's `EnginePassword`: `query` → `/db/query`,
   `execute` → `/db/execute?transaction`, both in rqlite's array format, which keeps duplicate column names of a join.
   An `execute` statement with `RETURNING` returns its rows (checked on rqlite 10.5.3). An engine error on statement *i* fails Invalid naming
   *i*; a response over 8 MiB fails PayloadTooLarge; a parameter that is an array or an object, other than one named-
   parameter map, fails Invalid in the facade. Each call is bounded by its request context and by `sql.callTimeout`
   (new config key, default 30 s), passed to rqlite as its timeout too. An engine that refuses the connection or
   times out fails Unavailable; an engine that answers 401 fails Internal, logged without the password. The engine's
   credential and address never reach the sandbox. rqlite returns BLOB columns as base64 strings; a parameter is never
   a BLOB.
5. **Cedar.** A capability `sql` with actions `sql::read` and `sql::write`, entity type `SQLDatabase`, principal
   attribute `sqlBindings` from `spec.sql`, and `builtin_sql.cedar`. The entity `SQLDatabase::"<ns>/<name>"` carries
   `namespace`, `resourceGroup`, `owner` (a Function reference, when set) and `writers`. The capability joins both the
   root registry (`pkg/funcd/funcd.go:602`) and `defaultRegistry` (`internal/auth/cedar/schema.go:55`), so an
   operator's Policy naming `sql::write` validates, and `SQLDatabase` joins `namespacedEntityType` (`schema.go:108`).
   In the built-ins, a binding permits `sql::read`; `sql::write` is
   permitted only to the database's `writers` (its owner plus writer-role grants). Operator Policies may forbid either.
6. **Roles.** Built-in `SQL Data Reader` (`sql::read`) and `SQL Data Writer` (`sql::read`, `sql::write`); scope kind
   `SQLDatabase`, name `<database>` (`ScopeKindSQLDatabase` in `api/types/v1alpha1/rolesassignment.go`); `sql::write`
   is a write action, so it feeds the `writers` set (`WriterLister.SQLWriters`, covering Namespace and `SQLDatabase`
   scopes, as `KVWriters` does). `Reader`, `Contributor` and `Owner` are unchanged, so existing namespace grants do not
   silently gain database access.
7. **Deletion protection.** ADR-0232's `sqldatabase-deletion-protection` also refuses an API delete with 409 while a
   Function's `spec.sql` names the database.
8. **The dependency check.** A `spec.sql` binding is checked like a KV one (ADR-0215): resolve, `sql::read`, then the
   engine's presence in the registry (no prober target); a missing engine reports kind `sql`, reason `EngineNotReady`,
   which extends ADR-0215's reason vocabulary (`internal/workernode/local/local.go:102-108`); `CheckerDeps` gains `SQL`.
9. **The shims.** TypeScript: `context.sql.query(alias, sql, params?) → Promise<Row[]>` (objects; with a duplicate
   column name the last one wins), `context.sql.queryValues(alias, sql, params?) → Promise<{columns, values}>` (arrays,
   for query builders), `context.sql.execute(alias, sql, params?) → Promise<ExecResult>` and
   `context.sql.execute(alias, statements) → Promise<ExecResult[]>`. Python: the same names in snake case
   (`query_values`), synchronous, rows as dicts; `execute(alias, sql_or_statements, params=None)` dispatches on whether
   its second argument is a string or a list. Integers past 2^53 lose precision in TypeScript. A pool member names itself with `X-Funcd-Member`, as for KV.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1
type FunctionSQL struct {
	Alias    string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	Database ObjectName `json:"database"`
}
// FunctionSpec.SQL []FunctionSQL `json:"sql,omitempty"`; FunctionStep.SQL []FunctionSQL `json:"sql,omitempty"`
// SQLDatabaseSpec.Owner ObjectName `json:"owner,omitempty"`

// internal/workernode/local/sql.go
type SQL interface {
	Query(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]QueryResult, error)
	Execute(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]ExecResult, error)
}
type QueryResult struct {
	Columns []string `json:"columns"`
	Values  [][]any  `json:"values"`
}
type ExecResult struct {
	RowsAffected int64    `json:"rowsAffected"`
	LastInsertID int64    `json:"lastInsertId"`
	Columns      []string `json:"columns,omitempty"` // a RETURNING statement's rows
	Values       [][]any  `json:"values,omitempty"`
}
const DependencySQL = "sql"

// internal/services/sql/facade.go
type FacadeDeps struct {
	Resolver   Resolver // alias → database, from the caller's spec.sql, cached 5 s
	Authorizer auth.Authorizer
	Engines    *Engines
	Master     []byte
	HTTPClient *http.Client
	Logger     *slog.Logger
}
func NewFacade(d FacadeDeps) (*Facade, error) // implements local.SQL and health.ReadChecker

// internal/auth
const ActionSQLRead Action = "sql::read"
const ActionSQLWrite Action = "sql::write"

// internal/auth/cedar
func SQLCapabilityWithWriters(w WriterLister) Capability
// WriterLister gains: SQLWriters(ctx context.Context, ns v1.NamespaceName, database string) []cedartypes.EntityUID
```

```cedar
@id("sql-read-binding")
permit(principal, action == Action::"sql::read", resource)
  when { principal has sqlBindings && principal.sqlBindings.contains(resource) };

@id("sql-write-base")
permit(principal, action == Action::"sql::write", resource);

@id("sql-single-writer")
forbid(principal, action == Action::"sql::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

| Local API call | Outcome |
|---|---|
| unknown alias, or Cedar denies | 403 |
| the engine is not in the registry, refuses the connection or times out | 503 |
| the engine answers 401 | 500, logged without the password |
| statement *i* fails (rqlite error, a write through `query`) | 400 naming *i*; an `execute` applies nothing |
| body over 1 MiB, over 500 statements, or a response over 8 MiB | 413 |
| success | 200 `{"results": [QueryResult…]}` or `{"results": [ExecResult…]}` |

| Consumes | Exposes |
|---|---|
| the `Engines` registry and `EnginePassword` (ADR-0232), the metastore, the PDP, the role compiler | `spec.sql` on Functions and steps, `SQLDatabase.spec.owner` |
| the per-sandbox socket (ADR-0069) | `/sql/{binding}/query` and `/execute`, `context.sql` in both shims |
| | Cedar `sql::read`, `sql::write`, `SQLDatabase`, `sqlBindings`; roles `SQL Data Reader`, `SQL Data Writer` |

## Implementation plan

1. Types and validation (`function.go`, `workflow.go`, `sqldatabase.go`), `just generate`; the materializer copy; the
   existence gate; the binding count (`MapFunction` watch).
2. `internal/services/sql`: resolver, facade; `internal/workernode/local/sql.go`: routes, registered when the facade
   is wired, through `NewHandler`, `NewManager` (`manager.go:64`) and the pool members' `handlerFor`;
   `internal/health`: the `sql` check; config `sql.callTimeout`.
3. `internal/auth`: actions; `internal/auth/cedar`: capability, `builtin_sql.cedar`, roles, `scopeHead`, write action,
   `schema.go`; `api/types/v1alpha1/rolesassignment.go`: the scope kind (enum and `Validate`);
   `internal/services/roles`: `SQLWriters`; registration at `pkg/funcd/funcd.go:602`.
4. The deletion-protection binding rule.
5. funcd-typescript `shim/src/sql.ts` and funcd-python `shim/src/funcd_shim/sql.py`, with tests against a fake local
   API; a release of each; `go get` both here.
6. Tests: facade units with an `httptest` engine that checks the user and password and honors read-only and
   `transaction`; capability and role-compiler units; one `TestScenario<Name>` per scenario in `pkg/funcd` with that
   fake engine registered in `Engines`.
7. The real-engine lane: a TypeScript and a Python function in funcd-functions (a release, then `go get`), and an `sql`
   lane. The examples follow the decider's choice of tools (2026-10-12), code first: TypeScript defines its schema with
   Drizzle (`drizzle-orm` Apache-2.0, `drizzle-kit` MIT) and queries through Drizzle's `sqlite-proxy` driver, whose
   callback routes by statement, not by Drizzle's method: a `SELECT` or `WITH` goes to `queryValues`, any other
   statement to `execute`, since Drizzle sends `insert … returning()` as a read method. Python uses SQLModel (MIT) models
   compiled with the SQLite dialect. The lane, in `scripts/lanes.yaml` with a Venom suite, runs
   `bound-function-reads`, `owner-writes`, `non-writer-write-refused`, `batch-is-all-or-nothing` and a write that
   survives an engine kill and a daemon restart.

**Definition of done**: the tests pass in funcd and both shims; the `sql` lane passes once; the gate passes; no username
or absolute path in a changed file.

## Review checklist

- [ ] The caller comes only from the socket; the engine's address and password never appear in env, logs, status or
      responses.
- [ ] `query` goes only to `/db/query`; `execute` only to `/db/execute?transaction`.
- [ ] Every call asks Cedar; an unknown alias is Forbidden before any engine call.
- [ ] `sql::write` feeds the `writers` set; `Reader`, `Contributor` and `Owner` are unchanged.
- [ ] Size caps and `sql.callTimeout` are enforced; an array or object parameter, other than one named map, is refused.
- [ ] `execute` asks both `sql::read` and `sql::write`.
- [ ] `SQLDatabase` entities carry `namespace`; a Namespace-scoped role grants reads and writes as for KV.
- [ ] Both shims expose the same names and shapes; pool members send `X-Funcd-Member`.
- [ ] `queryValues` keeps every column of a join with duplicate names; `execute` returns `RETURNING` rows.
- [ ] One test per scenario, named for it; the lane passes.

## Consequences

- Handlers use SQL with the same grants and audit as KV and blob, and any compile-only query builder works.
- Every call crosses the daemon, which bounds sizes and adds a hop; measured in the lane.
- An ORM that needs its own driver needs an adapter onto `context.sql`.

## Open questions

None. (`RETURNING` rows from `/db/execute`, array rows with duplicate join columns, named parameters and the refusal of
a write through `/db/query` were checked on rqlite 10.5.3 on 2026-10-12.)

## References

- rqlite data API and security docs (design report, Sources); [ADR-0069](0069-kv-data-plane.md),
  [ADR-0073](0073-kv-bindings-and-subdomains.md), [ADR-0136](0136-roles-and-role-assignments.md),
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md).
