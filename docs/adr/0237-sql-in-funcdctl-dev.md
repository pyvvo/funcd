# ADR-0237: SQL databases under funcdctl dev

- **Status**: Implemented (2026-10-12, review passed). Reviewing (2026-10-12, implementation done). Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  replaced the `zig cc` cross-build by a native macOS build, piloted on darwin/arm64, restored the crash reap and bound
  the engine to loopback; the second found one Major, fixed: pruning keeps a database that holds a table)
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, dev, funcdctl
- **Realizes**: [FEAT-0011/F133](../feat/0011-feat-sql.md) (SQL in funcdctl dev)
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (funcdctl dev, its process-mode catalog engine) ·
  [ADR-0167](0167-process-worker-crash-recovery.md) (the reap of a crashed run's processes) ·
  [ADR-0231](0231-durable-storage-for-add-on-engines.md) (process runtimes refuse storage) ·
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) (the engine, its arguments, auth file and password) ·
  [ADR-0233](0233-sql-access-from-functions.md) (bindings, owner and roles) · source:
  [design report](../reports/sql-provider-design.md)

## Context & Need

`funcdctl dev` runs a function or a workflow from source with no containers (ADR-0125). A function that declares a SQL
binding must get a working database there, served by the same rqlite version as in production, so its queries and
migrations behave the same before deploying. On the process runtime the add-on runtime refuses engine storage
(ADR-0231), so dev needs its own engine driver, as the catalog has (`internal/catalog/devengine`).

## Scenarios

- `scenario: dev-function-binds-database` — Given a function whose `funcdctl.yaml` binds database `todo-db` as `app`,
  When the developer runs `funcdctl dev`, Then `todo-db` is created and Ready, and the function's `context.sql` calls
  work, writes included.
- `scenario: dev-data-persists-with-persist` — Given `--persist`, When the developer stops and restarts `funcdctl dev`,
  Then the rows written before are there; without `--persist` each run starts empty.
- `scenario: dev-crashed-engine-reaped` — Given a `funcdctl dev --persist` run that was killed with SIGKILL, When the
  developer starts it again, Then the old engine process is gone and the new run serves the same data.
- `scenario: dev-engine-unavailable` — Given a `funcdctl` built without the engine (a placeholder build), When a
  function binds a database, Then `funcdctl dev` fails at start with a message naming the build recipe.
- `scenario: dev-workflow-step-binds-database` — Given a workflow whose step binds `todo-db`, When it runs under
  `funcdctl dev`, Then the step reads and writes the database.

## Scope

**In**: the `bindings.sql` key of `funcdctl.yaml`; the dev engine driver and its reap; the engine's arguments on a host;
the embedded engine binary and its build; the dev databases' owner and roles.

**Out**: Apps under `funcdctl dev` (dev runs functions and workflows only); running migration hooks (a dev function runs
its migrations itself, for example with Drizzle's migrator).

## Constraints & Decision drivers

- Same engine version as production (ADR-0232's pin); no containers, no root.
- rqlite publishes no macOS binary (checked 2026-10-12: v10.5.3 has Linux, Windows and package assets only), and its
  SQLite driver needs cgo. rqlite v10.5.3 builds natively on macOS from source (checked 2026-10-12 on darwin/arm64:
  a 50 MB binary linking only system libraries, serving queries).
- The thin release client never embeds the engine; only the `-tags dev` build does, like the catalog engine.
- A killed dev run must not leave an engine holding its data directory (ADR-0167).
- On a developer's host the engine must not listen beyond loopback.

## Alternatives considered

1. **Use an `rqlited` found on `PATH`**: no build work, but the version is whatever the developer installed. Rejected.
2. **Run the production image with Docker**: needs a container runtime, against ADR-0125. Rejected.
3. **Cross-build darwin from Linux with `zig cc`**: one Linux job, but Go's darwin build links CoreFoundation and
   Security, which zig does not ship without Apple's SDK; reports of such builds failing exist. Rejected for a native
   build on macOS.

## Decision

1. **The binding.** `funcdctl.yaml` `bindings.sql[]` holds `{alias, database}`. `funcdctl dev` creates one
   `SQLDatabase` per named database, as it does for KV stores and buckets: the last function that binds it is its
   `owner`, and every other dev function or step that binds it gets a `SQL Data Writer` RolesAssignment. A database no
   longer named is pruned at the next `--persist` start like the other synthesized objects (`KindSQLDatabase` joins
   `pruneStale`'s kinds), but ADR-0232's deletion protection keeps it while it holds a table, so its rows survive and
   the prune's warning names it.
2. **The driver.** `internal/sql/devengine` implements `provider.Runtime` and `provider.Volumes` for `OwnerKind`
   `SQLDatabase` and runs the embedded `rqlited` as a host subprocess with `-http-addr` and `-http-adv-addr`
   `127.0.0.1:<free port>`, `-raft-addr 127.0.0.1:<second free port>`, the auth file at 0600 in the engine's own
   temporary directory, and its data under `<data root>/<ns>/<name>`. The arguments and the auth file come from the
   builder that ADR-0232's entrypoint `cmd/funcd-rqlite` uses, shared so the two cannot drift. The driver ignores the
   storage spec's uid and gid (no chown). Its `Converge` reports `Address` `127.0.0.1:<port>`, which the SQL reconciler
   records as it is. `WithStateDir` registers its processes with `procreg` for the ADR-0167 reap, as the catalog
   engine does; `WithDataDir` is the data root, `<persist-to>/sql/` under `--persist`, else a temporary root removed at
   exit. `funcd.WithSQLProviderRuntime` injects the driver, as `WithCatalogProviderRuntime` does.
3. **The binary.** `internal/sql/embedengine` (`-tags dev`) embeds a per-OS/arch `rqlited` built with cgo from the
   rqlite source version that ADR-0232's Dockerfile pins, read from that one pin, through the Go module checksum
   database: linux in the same Docker builder stage as the image, darwin natively on macOS, both by
   `scripts/build-sql-engine.sh`, shared by `just build-sql-engine <os> <arch>` and the release workflow, which gains a
   macOS job for the darwin builds, run on the macOS host through act's self-hosted platform mapping (an `.actrc`
   entry) while releases are built locally. darwin/amd64 builds on an arm64 Mac with `GOARCH=amd64 CGO_ENABLED=1
   CC="clang -arch x86_64"`. The committed file is a placeholder, so `just ci` stays green.
4. **A placeholder build fails at start.** `cmd/funcdctl/dev.go` checks `embedengine.Bundled()` before starting the
   platform when a function binds a database: a database that can never serve would only fail later, on the first call.

## Temporary workarounds

None.

## Contracts

```yaml
name: todo-api
runtime: nodejs22
handler: handle
bindings:
  sql:
    - alias: app
      database: todo-db
```

```go
// pkg/sdk/manifest.go: Bindings.SQL []v1.FunctionSQL `json:"sql,omitempty"`

// internal/sql/devengine (build tag dev)
func New(logger *slog.Logger, opts ...Option) (*Runtime, error) // implements provider.Runtime and provider.Volumes
func WithStateDir(dir string) Option                             // the procreg registry reaped at New (ADR-0167)
func WithDataDir(dir string) Option                              // the data root; "" ⇒ a temporary root

// internal/sql/embedengine (build tag dev)
func Bundled() bool
func Extract(dir string) (path string, err error)

// internal/services/sql/engineargs.go: the argument and auth-file builder shared with cmd/funcd-rqlite.

// pkg/funcd/options.go
func WithSQLProviderRuntime(r provider.Runtime) Option
```

| Consumes | Exposes |
|---|---|
| ADR-0232's pin, argument builder and password; ADR-0233's facade, owner and roles; `procreg` | `bindings.sql` in `funcdctl.yaml`, the dev engine, `just build-sql-engine` |

## Implementation plan

1. `pkg/sdk`: the binding; `cmd/funcdctl/dev.go`: synthesize the databases and roles, wire the driver, the start check,
   `KindSQLDatabase` in `pruneStale`'s kinds.
2. `internal/services/sql/engineargs.go`, used by `cmd/funcd-rqlite` and the driver; `internal/sql/devengine`;
   `internal/sql/embedengine` with a placeholder; `scripts/build-sql-engine.sh`; `justfile`: `build-sql-engine`, the
   placeholder's skip-worktree pin and unpin recipes, its `check-hygiene` size entry, and `dev-run` building the host
   engine; `.github/workflows/release.yml`: the macOS job and its header comment.
3. Tests: the driver's unit tests with a fake engine (a small script that serves `/readyz`), the reap included; one
   `TestScenario<Name>` per scenario in `cmd/funcdctl`, run against a real engine only when the build has one, else
   skipped with the reason; `just build-sql-engine` checked once on darwin/arm64 and linux/amd64 during implementation.

**Definition of done**: the tests pass; the catalog's dev tests pass unchanged; a darwin/arm64 build runs a bound
function's query once, recorded in the review; darwin/amd64 is built once too; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] The dev engine uses ADR-0232's version and the shared argument builder, bound on loopback only.
- [ ] A killed run's engine is reaped at the next start.
- [ ] The thin `funcdctl` embeds nothing; the placeholder keeps `just ci` green.
- [ ] Without `--persist` nothing survives the run; with it, data does.
- [ ] One test per scenario, named for it.

## Consequences

- A developer tests SQL locally against the production engine version.
- The fat `funcdctl` grows by about 12 MB compressed per platform and extracts about 36 to 50 MB to a temporary directory at each run.
- The release workflow needs a macOS job.

## Open questions

None.

## References

- [ADR-0125](0125-funcdctl-dev-local-run.md) Decision 5 (the catalog dev engine); rqlite v10.5.3 release assets;
  the native darwin/arm64 build checked on 2026-10-12.
