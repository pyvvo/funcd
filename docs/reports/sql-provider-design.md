# SQL provider design: a dedicated rqlite engine for each app

- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Status**: the source of [FEAT-0011](../feat/0011-feat-sql.md). The decisions below were refined with the decider on
  2026-10-12. Code references point at commit `8f507659`; upstream rqlite facts were checked on the same day.

## Summary

funcd gets a SQL provider, named `sql`. Each database is served by its own rqlite engine. The engine runs out of the
daemon, as the catalog's DuckDB engine does, because rqlite links SQLite through cgo and funcd builds with
`CGO_ENABLED=0`. Functions reach a database through their own worker-node socket, as they reach KV today, so no database
credential ever enters a sandbox.

| Topic | Decision |
|---|---|
| Name | `sql` for the provider, its binding, its Cedar actions and its shim API. Nothing in the code uses the word today |
| Engine | rqlite. PostgreSQL is ruled out as too heavy for the target box |
| Engine image | funcd's own minimal Dockerfile, every input pinned, not the upstream image |
| Tenancy | One dedicated engine per database. An App declares its database, so it gets its own engine |
| Shared server | Kept as an idea, not built in this epoch |
| Access path | A `context.sql` helper in both shims, over the function's socket, like `context.kv` |
| Grants | A binding grants reads through rqlite's read-only endpoint. Writes need a grant from the database's side |
| Read consistency | rqlite's default, `weak`, with no setting in V1 |
| Durability | The engine's data on the host's disk, which the add-on runtime does not offer yet |
| Backup and restore | Workload backups copy each database; whether the platform backup also copies every database is open |
| Delete rules | As for KVStore |
| Schema migrations | In the App's lifecycle hooks |
| Dev mode | An embedded engine that `funcdctl dev` runs as a subprocess, like the duckdb dev engine |
| External access | Kept as an idea, not built in this epoch |
| Change feed | In scope: a row change becomes an event Sensors can react to |
| ORMs and query builders | ORMs are optional. A query builder that only generates SQL works with the helper as is, and migrations can be SQL generated from schema code |

## How providers work today

### The provider model

A provider is a shared endpoint that serves one capability to many functions. The binding on the Function is the
link, and a port with two or more drivers is the contract (`blueprint.md:229`). There are two tiers:

| Tier | Runs | Providers today | A function reaches it through |
|---|---|---|---|
| Built-in | inside the daemon, pure Go, always on | `kv`, `blob`, `secrets`, `eventing`, `invoke`; infrastructure: `ingress`, `egress`, `s3`, `log-ingest` | its worker-node socket, or the S3 gateway for `spec.blob` |
| Add-on | out of the daemon, a curated engine container per resource | `catalog-query` | a URL and a token injected into its env |

The blueprint's test is whether the provider can be embedded in pure Go in the daemon (`blueprint.md:232`). The
catalog in `pkg/funcd/providers.go:18-34` only classifies providers; it wires nothing. A descriptor is
`{Name, Kind, Port, Bindings, Summary}` (`internal/provider/provider.go:25`). Every provider is wired by hand at the
composition root, `pkg/funcd/funcd.go`.

### Built-in provider: KV

The engine is a Go library inside the daemon. The function never holds a credential: the socket it talks on is its
identity.

```text
handler: context.kv.get("cache", "user/42")
   │  HTTP over FUNCD_INVOKE_SOCKET, one per Function
   ▼
local API   GET /kv/{binding}/{key}     caller = the socket's Function
   ▼
kv Facade   alias → (store, table)       the caller's spec.kv, 5 s cache
   ▼
Cedar PDP   kv::read | kv::write         binding grants read · table writers grant write
   ▼
kvstore.KV  key = <ns>/<store>/<table>/<key>
   ▼
driver      memory | badger              kvstore.engine, own Badger dir <dataDir>/kv
```

| Part | Where | What the code does |
|---|---|---|
| Resource | `api/types/v1alpha1/kvstore.go:37` | `KVStore`: tables with an optional owner; per-store caps of at most 1 MiB per value and 64000 bytes per key |
| Binding | `api/types/v1alpha1/function.go:101` | `spec.kv[]`: alias, store, table |
| Port | `internal/kvstore/kvstore.go:16` | `kvstore.KV`: Get, Put, Delete, List; drivers `memory` and `badger`, with a contract suite |
| Driver choice | `cmd/funcd/main.go:609` | `kvstore.engine` picks the driver; backup and CDC seams wrap Badger only |
| Door | `internal/workernode/local/manager.go:77`, `internal/workernode/local/local.go:131` | one Unix socket per Function, bind-mounted into its container; the Cedar principal is fixed per connection |
| Routes | `internal/workernode/local/kv.go:32` | `GET`, `PUT`, `DELETE /kv/{binding}/{key}` and `GET /kv/{binding}?prefix=` |
| Facade | `internal/services/kv/kv.go:75-166` | resolves the alias, asks Cedar, applies the caps, prefixes the key |
| Authorization | `internal/auth/cedar/capabilities.go:86`, `internal/auth/cedar/builtin_kv.cedar` | the binding is the read grant; a write needs the caller in the table's `writers` set: its owner plus writer-role RolesAssignments |
| Reconciler | `internal/services/kv/reconcile.go:71` | status follows the storage probe; drops a deleted store's or removed table's data, except while the platform is held |
| Admission | `internal/controlplane/admission/kvstore.go:34,79` | a store-count quota; a delete is refused while a Function binds the store or it holds keys |
| Function gate | `internal/function/references.go:22` | a Function bound to a missing store waits Pending with `KVStoreNotFound` |
| Health | `internal/health/prober.go:44`, `internal/health/checker.go:38` | a sentinel read on an interval; the shim's readiness asks `GET /health/dependencies` |
| Backup | `pkg/funcd/funcd.go:2157-2162`, `internal/backup/workload/workload.go:155` | the driver exports and loads one store's prefix; a store is a workload backup member |
| App, Workflow | `api/types/v1alpha1/app.go:115`, `api/types/v1alpha1/workflow.go:31` | an App declares stores with a deletion policy; a Workflow owns stores for its image steps |
| Shim | funcd-typescript `shim/src/kv.ts`, funcd-python `shim/src/funcd_shim/kv.py` | `context.kv` is plain HTTP over the socket; a pool member names itself in `X-Funcd-Member` |

### Add-on provider: CatalogService

The engine is a container that the provider runtime supervises. Functions reach it over the network with a signed
token, through a PEP proxy that runs in the daemon.

```text
CatalogService applied                     handler with its own DuckDB client
  ▼                                          │ FUNCD_CATALOG_<ALIAS>_URL and _TOKEN
catalog reconciler                           ▼
  ├─ proxy.Listen(status.proxyPort)        catalog PEP proxy, node-private
  ├─ spec.blob + spec.catalog exist?         ├─ verify HS256 JWT {ns, fn}
  ├─ engineEnv: S3 keypair, QUACK_TOKEN      ├─ Cedar catalog::query
  ├─ provider.Converge(ProviderSpec)         └─ swap token → QUACK_TOKEN
  ├─ Ready → proxy.Ensure(engine)            ▼
  └─ requeue 2 s, or the supervision       engine container ip:8080, DuckDB + Quack
     period once Ready                       ▼
                                           S3 gateway as CatalogService → blob
```

| Part | Where | What the code does |
|---|---|---|
| Resource | `api/types/v1alpha1/catalogservice.go:27` | `CatalogService`: its own `spec.blob`, the catalog location, resources, secrets, config, optional ingress; also a Cedar principal |
| Binding | `api/types/v1alpha1/function.go:117` | `spec.catalogs[]`: alias, catalog |
| Provider runtime | `internal/provider/runtime.go:99,218` | `Converge` recreates a missing or dead replica, starts a created one and probes `http://ip:port/path`; the image entrypoint is the engine, so `Command` is nil; replicas are pinned, with no scale to zero |
| Spec | `internal/provider/spec.go:63` | Ref, Image, Port, Env, Readiness, Replicas, Resources, Route; no volume |
| Reconciler | `internal/services/catalog/reconcile.go:36-184` | binds the proxy port, checks the buckets, builds the env, converges, then fronts the Ready engine with the proxy |
| Engine env | `internal/services/catalog/reconcile.go:339`, `internal/provider/env.go:33` | the S3 keypair is derived over the CatalogService identity; config, then secrets, merged with reserved `FUNCD_*` keys dropped |
| PEP proxy | `internal/catalog/gateway/manager.go:26,120,158`, `internal/catalog/gateway/proxy.go:82` | one listener per catalog in the daemon process; its port is recorded in status so the URL survives a restart; 503 while the engine is not Ready |
| Tokens | `internal/catalog/gateway/token.go:88,126` | a Function token is an HS256 JWT over `{ns, fn}` signed with the node master; an Identity token is checked against its Secret |
| Consumer env | `internal/function/catalog.go:34` | injects the URL and the token; the Function waits `CatalogNotReady` until the catalog is Ready and its proxy is bound |
| Cedar | `internal/auth/cedar/capabilities.go:365,431` | `spec.catalogs` grants `catalog::query`; a principal source resolves the engine's own identity |
| Image | `images/runtime/duckdb/Dockerfile`, `internal/runtime/embedimg` | every input pinned; `just build-runtime-images` exports it into the binary, and containerd imports it at start |
| Dev mode | `internal/catalog/devengine/devengine.go:116`, `cmd/funcdctl/dev.go:784` | `funcdctl dev` swaps in a driver that runs the engine as a host subprocess |
| Wiring | `pkg/funcd/funcd.go:996-1033` | one `provider.NewRuntime` with `OwnerKind: CatalogService` |
| Backup | `internal/backup/workload/workload.go:215` | copies the catalog's files, then its checkpoint, in up to three attempts |

### Side by side

| Aspect | Built-in KV | Add-on catalog |
|---|---|---|
| Engine | Go library in the daemon: Badger | container from a curated image: DuckDB |
| Function reaches it | a shim API over its socket | its own client, to the proxy URL |
| Caller identity | the socket | a signed bearer token in the env |
| Authorization | Cedar on every call, in the Facade | Cedar on every handshake, in the proxy |
| Status | follows the storage probe | follows the engine's HTTP probe |
| Where data lives | the daemon's Badger directory | blob: the checkpointed `catalog.db` plus Parquet |
| Delete | refused while bound or holding data; the reconciler drops the prefix | engine torn down; the proxy listener stays until no Function binds it |
| Dev mode | the same code, memory or Badger | a subprocess engine |

### What a new provider touches

| Site | Files | KV | Catalog |
|---|---|---|---|
| Resource kind | `api/types/v1alpha1/<kind>.go`, `metadata.go`: `Kind.Validate`, `NewObject`, `AllKinds` | `KVStore` | `CatalogService` |
| API and CLI | `internal/controlplane` handlers, including the per-type `stampTypeMeta` switch, routes, stubs; `pkg/sdk/kinds.go`; `just generate` | yes | yes |
| Binding | `api/types/v1alpha1/function.go` and its validation | `spec.kv` | `spec.catalogs` |
| Catalog row | `pkg/funcd/providers.go` | kv, built-in | catalog-query, add-on |
| Port and drivers | a port package plus a contract suite | `kvstore.KV` | `provider.Runtime`: container and dev drivers |
| Function door | a local API route, or env injection | `/kv` routes | `FUNCD_CATALOG_*` and the proxy |
| Cedar capability | `internal/auth/cedar`: Capability, built-in `.cedar`, action in `internal/auth/authorizer.go`; registered at `pkg/funcd/funcd.go:602` | `kv::read`, `kv::write` | `catalog::query` |
| Reconciler | `internal/services/<x>/reconcile.go`, `ctrl.Register`, `ctrl.Watches` | yes | yes |
| Function gates | `internal/function/references.go`, `internal/function/catalog.go` | `KVStoreNotFound` | `CatalogNotReady` |
| Admission | `internal/controlplane/admission` | quota, deletion protection | none: reconcile-time checks (ADR-0121) |
| Health | `internal/health` probe target and checker kind | `TargetKV` | engine probe only |
| Config | `internal/platform/config/config.go`, `cmd/funcd/main.go` | `kvstore.*` | proxy host, image map |
| Workload backup | `internal/backup/workload`, `internal/services/backup`, `api/types/v1alpha1/backup.go` | member | member |
| Platform DR | backup plan components, `funcd restore`, the hold | `kv` component, restore kv | none: its data is in blob |
| App section | `api/types/v1alpha1/app.go`, the App reconciler, `workload.Resolve` | `kv`, with deletion | `catalogs` |
| Workflow | `api/types/v1alpha1/workflow.go` | owned stores, step bindings | none |
| Shim client | funcd-typescript and funcd-python: a release each, then `go get` | `context.kv` | none |
| Image and dev engine | `images/runtime/<x>`, `internal/runtime/embedimg`, `internal/catalog/embedengine` | none | duckdb |
| Lanes | `scripts/lanes.yaml` and a Venom suite | yes | the duckdb lane |

A missing `stampTypeMeta` case passes every unit test and fails only a real `funcdctl apply`.

## rqlite facts

Checked upstream on 2026-10-12, against rqlite v10.

| Fact | What it means for funcd |
|---|---|
| The module is `github.com/rqlite/rqlite/v10`. SQLite comes from `github.com/rqlite/go-sqlite3`, a fork of `mattn/go-sqlite3` that ships C sources; the Linux build needs glibc 2.34 or later | the engine needs cgo, so it runs out of the daemon |
| Raft is `hashicorp/raft`, with its log in bbolt | the engine keeps its Raft log, snapshots and SQLite file on local disk |
| The data API is `/db/execute`, `/db/query` and `/db/request`, with parameterized statements; `transaction` makes one request all or nothing; explicit `BEGIN` and `COMMIT` are undefined | every request stands alone, so a check per request needs no session state |
| Read levels: `none`, `weak` as the default, `linearizable`, `strong`, `auto` | on one node every level reads the same local data |
| Auth is HTTP Basic, with users in a JSON file per node; permissions are per endpoint, never per table | a function must never hold the engine's credentials |
| `/db/query` runs on a read-only SQLite connection | a read binding limited to `/db/query` cannot write, with no SQL parsing |
| The docs describe one SQLite database per node | a database is one engine |
| Backup: `GET /db/backup` returns the SQLite file or a SQL dump, optionally vacuumed and compressed; `-auto-backup` uploads to S3, GCS or a file on an interval, without encryption | funcd's own backup rules and sealer apply |
| Restore: `POST /db/load` replaces all data, `/boot` loads a single node, `-auto-restore` runs only on an empty node; no incremental backup, no point in time | matches funcd's offline restore model (ADR-0206) |
| CDC, since 9.0: changes stream to an HTTP endpoint, at least once, sent by the leader | the source of the change feed |
| Client libraries include `knex-rqlite` and `sqlalchemy-rqlite` | ORMs can reach rqlite's HTTP API; not a V1 requirement |
| `github.com/rqlite/sql` parses SQLite syntax in pure Go, under MIT | a daemon-side statement check is possible for a later shared server |
| License MIT; images `rqlite/rqlite` and `ghcr.io/rqlite/rqlite`; HTTP port 4001 by default | funcd still builds its own pinned image, as it does for duckdb |
| The prebuilt Linux binaries are dynamically linked: the glibc build needs glibc 2.34 or later, and the amd64 musl build needs musl's loader, so it does not start on Ubuntu. There is no musl build for arm64. `rqlited` v10.5.3 is 36 MB, 12.1 MB with `gzip -9` | a minimal image either builds `rqlited` statically from source, or carries the glibc build on a distroless base; with a static base, the embedded image adds about 12 MB per architecture to funcd |

## Cost of a dedicated engine, measured

Measured on homebox, the 8-core, 15.8 GB V1 target, on 2026-10-12: rqlite v10.5.3 (go1.27.1, SQLite 3.53.4), the
official linux-amd64 release checked against its published sha256. One engine started alone, idle, then after a
workload of 10,000 inserts in 20 transactions, 1,000 point queries and 200 one-row updates; then 10 more engines side by
side. Memory is read from `/proc/<pid>/status` and `/proc/<pid>/smaps_rollup`, and from `MemAvailable` before and
after the 10 engines.

| Measure | Result |
|---|---|
| One engine, empty and idle | 28.6 MB RSS, 4.6 MB of it anonymous, 10 threads |
| Each extra idle engine, 10 started | 4 to 7 MB: 6.8 MB PSS, 4.5 MB anonymous, `MemAvailable` down 4.1 MB per engine; the 36 MB binary is shared in memory |
| One engine after the workload | 42.2 MB RSS, 16.3 MB anonymous |
| Start to ready | 1.5 s |
| CPU at idle | 0.13 % of one core |
| Disk | 76 KB empty, 3.9 MB with 10,000 rows |
| 10,000 inserts in 20 transactions | 0.39 s |
| Point query | 0.44 ms median, 1.19 ms p99 |
| One-row update, one request each | 6.5 ms, mostly the disk sync |

For 100 apps, idle engines take about 0.5 to 0.7 GB, and engines as busy as the test about 1.6 GB, roughly a tenth of
the box. A shared server would save only the fixed cost of about 5 MB per engine, since caches grow with the data
either way. Not measured: the containerd shim that each engine runs under.

## Decisions

**Engine placement: out of the daemon.** rqlite cannot be linked into a `CGO_ENABLED=0` binary. The engine runs as an
add-on engine container in production and as a subprocess under `funcdctl dev`, like DuckDB. Only the client side,
the facade and the authorization stay in the daemon, in pure Go, over rqlite's HTTP API.

**Engine image: our own minimal Dockerfile.** As for duckdb, funcd builds the engine image itself, pins every input
and embeds it, instead of pulling the upstream image. No prebuilt binary is static, so the smallest image builds
`rqlited` statically from source in a pinned builder stage and ships it alone on a distroless static base, the same
for amd64 and arm64. The fallback is the glibc build on a distroless base pinned by digest, as the duckdb image uses
distroless cc. A static build of rqlite with SQLite has not been tried yet.

**Tenancy: one dedicated engine per database.** The options were a dedicated engine per database, one platform-run
shared engine, and an external cluster given by URL, as `blob.target` gives an S3 store. rqlite has one database per
node and no per-table permissions, so a shared engine needs funcd to check the tables of every statement, and every
app's writes share one Raft log. Dedicated engines need no such check, isolate load and failures, and make the
backup, the restore and the delete of one app simple. At about 5 MB per idle engine, that isolation is cheap. An App
declares its database and gets its own engine; a database outside an App works the same way, like a KVStore.

**Access path: a helper over the socket.** The options were a `context.sql` helper in both shims, over the function's
worker-node socket, and a proxy URL with a token in the env, for any rqlite client, as the catalog does. The helper
keeps every credential out of the sandbox and follows the KV and blob pattern, at the cost of a release of each shim.
ORMs are optional: with the helper, an ORM needs a small adapter onto `context.sql`, which can come later.

**Grants: read by binding, write by grant.** On a dedicated engine, a read goes to `/db/query`, which rqlite runs on a
read-only connection, and a write goes to `/db/execute` only when the caller holds a write grant on the database. This
mirrors KV, where the binding grants read and the table's writers grant write. No SQL is parsed.

**Shared server, kept as an idea.** The decider proposed a check by table and action for a shared server, made in the
shim. The policy is sound, but the shim runs next to the handler's code and can be skipped or given a false table
list, so the check has to run in the daemon, which can read each statement with `github.com/rqlite/sql`. Views,
triggers, `ATTACH`, `PRAGMA` and `sqlite_schema` make that check hard to get airtight, and one app's writes would still
slow the others. Not built in this epoch.

**Read consistency: `weak`, with no setting.** A single node is always the leader, so every level reads the same local
database and sees every finished write. The levels differ only with several nodes, a V2 topic.

**Durability and backup.** The engine keeps its data on the host's disk, which the add-on runtime needs to offer
first: `ProviderSpec` has no volume today. Workload backups copy a database through `/db/backup` and a restore loads it
through `/db/load`, under funcd's own encryption, since rqlite's automatic backups have none. Whether the platform
backup also copies every database, as it copies the KV instance, is left to the backup ADR; the proposal is yes, so a
host loss never loses an app that set no BackupSchedule.

**Delete rules, migrations, dev mode, external access, change feed.** A database is protected like a KVStore: its
delete is refused while a function binds it or it holds data, and the App's deletion policy decides whether it
outlives the App. Schema migrations run in the App's lifecycle hooks (ADR-0214). `funcdctl dev` embeds the engine and
runs it as a subprocess. External access is kept as an idea. The change feed is in scope: rqlite pushes each change to
an HTTP endpoint, and funcd turns it into events, following the KV change feed (ADR-0068).

## Illustrative shapes

These shapes illustrate the decisions above; the ADRs fix the final names and contracts. Each shape mirrors one that
ships today. The text before each block names its new keys; every other key exists.

| Piece | Proposed shape | Mirrors |
|---|---|---|
| Kind | `SQLDatabase` | `KVStore` and `CatalogService` |
| Binding | `spec.sql[{alias, database}]` | `spec.catalogs` |
| Function API | `context.sql.query()` and `context.sql.execute()` | `context.kv` |
| Local API route | `POST /sql/{binding}/query` and `POST /sql/{binding}/execute` | `/kv/{binding}/{key}` |
| Cedar actions | `sql::read`, `sql::write` | `kv::read`, `kv::write` |
| Built-in policy | the binding grants read; the writers set grants write | `builtin_kv.cedar` |
| Built-in roles | `SQL Data Reader`, `SQL Data Writer`, with scope kind `SQLDatabase` | `KV Data Reader`, `KV Data Writer`, scope kind `KVStore` |
| App section | `sql`, with `deletion` | `kv` |
| Backup | an App's backup covers its databases; restore member kind `SQLDatabase` | the `KVStore` member |
| Change feed | EventSource kind `sql` | EventSource kind `blob` |

### The kind

The whole kind is new. `owner` mirrors a KV table's owner, `resources` mirrors the CatalogService's, and `bindings`
mirrors the KVStore status.

```yaml
apiVersion: funcd.io/v1alpha1
kind: SQLDatabase
metadata:
  name: todo-db
  namespace: default
spec:
  owner: todo-api
  resources:
    memory: 64Mi
status:
  phase: Ready
  conditions:
    - type: Ready
      status: "True"
  bindings: 2
```

### A function binds it and uses it

New key: `spec.sql`. The TypeScript helper is async and the Python helper is sync, as `context.kv` is. A list of
statements passed to `execute` succeeds or fails as a whole, through rqlite's one-request transaction.

```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: todo-api
  namespace: default
spec:
  runtime: nodejs22
  handler: handle
  sql:
    - alias: app
      database: todo-db
```

```ts
export async function handle(context, event) {
  await context.sql.execute('app', 'INSERT INTO todos(title, done) VALUES(?, ?)', [event.data.title, 0]);
  return context.sql.query('app', 'SELECT id, title FROM todos WHERE done = ? ORDER BY id', [0]);
}
```

```python
def handle(context, event):
    context.sql.execute("app", "INSERT INTO todos(title, done) VALUES(?, ?)", [event["data"]["title"], 0])
    return context.sql.query("app", "SELECT id, title FROM todos WHERE done = ?", [0])
```

### An App with its database and its migration hook

New keys: `spec.sql` and `functions[].sql`. The migration runs in the existing `preApply` hook, which already receives
the version being installed.

```yaml
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: default
spec:
  sql:
    - name: todo-db
      deletion: retain
      owner: todo-api
  functions:
    - name: todo-api
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-api:1.0.0
      sql:
        - alias: app
          database: todo-db
    - name: todo-migrate
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-migrate:1.0.0
      sql:
        - alias: app
          database: todo-db
  hooks:
    preApply:
      - function: todo-migrate
```

```ts
export async function handle(context, event) {
  await context.sql.execute('app', [
    ['CREATE TABLE IF NOT EXISTS todos (id INTEGER PRIMARY KEY, title TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0)'],
    ['CREATE INDEX IF NOT EXISTS todos_done ON todos(done)'],
  ]);
}
```

### Write grants and roles

The todo App of funcd-functions grants its migration function `KV Data Writer` on a table owned by its API function.
The SQL grant is the same object. New values: the role name `SQL Data Writer` and the scope kind `SQLDatabase`.

```yaml
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: todo-migrate-writes-db
  namespace: default
spec:
  assignments:
    - principal:
        kind: Function
        name: todo-migrate
      roleRef:
        kind: BuiltinRole
        name: SQL Data Writer
      scope:
        kind: SQLDatabase
        name: todo-db
```

| Built-in role | Actions | Mirrors |
|---|---|---|
| `SQL Data Reader` | `sql::read` | `KV Data Reader` |
| `SQL Data Writer` | `sql::read`, `sql::write` | `KV Data Writer` |
| `Reader`, `Contributor`, `Owner` | unchanged, with no SQL action | the catalog's choice: `catalog::query` is in none of them |

A write grant compiles into the database's `writers` set, as `WriterGrants` does for KV
(`internal/auth/cedar/roles_compile.go:212`).

### Built-in policies, entities and an operator Policy

A new `builtin_sql.cedar`, written like `builtin_kv.cedar`:

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

The entities Cedar sees for the App above:

```text
Function::"default/todo-api"     sqlBindings = [SQLDatabase::"default/todo-db"]
SQLDatabase::"default/todo-db"   owner = Function::"default/todo-api"
                                 writers = [Function::"default/todo-api", Function::"default/todo-migrate"]
```

An operator's Policy still overrides the built-ins, for example to freeze writes:

```yaml
apiVersion: funcd.io/v1alpha1
kind: Policy
metadata:
  name: freeze-todo-db
  namespace: default
spec:
  cedar: |
    forbid(principal, action == Action::"sql::write", resource == SQLDatabase::"default/todo-db");
```

### The PEP

The SQL facade makes the call the KV facade makes at `internal/services/kv/kv.go:79`. An unknown alias is Forbidden,
as for KV, and the caller always comes from the socket.

```go
resource := &auth.EntityRef{Type: v1.KindSQLDatabase, Namespace: ns, Name: b.Database}
dec, err := f.authorizer.Authorize(ctx, auth.Request{
	Identity: auth.Identity{Subject: ns + "/" + fn, Principal: principal},
	Action:   auth.ActionSQLWrite,
	Resource: resource,
})
```

An allowed read goes to the engine's `/db/query`, an allowed write to `/db/execute`.

### Workflow steps, restore and the change feed

New keys: `steps[].function.sql`, the restore member kind `SQLDatabase`, the EventSource kind `sql` and its event type
`Insert`. An App's existing `backupSchedules` entry needs no change: an empty scope backs up the App, which then
includes its databases.

```yaml
workflows:
  - name: todo-report
    steps:
      - name: summarize
        function:
          image: registry.example/todo-summarize:1.0.0
          sql:
            - alias: app
              database: todo-db
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: Restore
metadata:
  name: todo-db-yesterday
  namespace: default
spec:
  backup: todo-backup-20261011-0600
  members:
    - kind: SQLDatabase
      name: todo-db
      as: todo-db-restored
```

```yaml
eventSources:
  - name: todo-changes
    sql:
      database: todo-db
      events:
        - name: todo-added
          table: todos
          on:
            - Insert
sensors:
  - name: todo-notify
    "on":
      - name: added
        source: todo-changes
        event: todo-added
    do:
      - name: notify
        "on": added
        function: todo-notify
```

### Query builders and migrations

A query builder that only generates SQL produces what `context.sql` takes: SQL text and its parameters. The decider
picked the tools on 2026-10-12, code first: Drizzle in TypeScript (`drizzle-orm` Apache-2.0, `drizzle-kit` MIT) and
SQLModel with Alembic in Python. Checked on 2026-10-12 with `drizzle-orm` 0.45.3 and `drizzle-kit` 0.31.11:
`drizzle-kit generate` writes `CREATE TABLE` and later `ALTER TABLE` migrations from `schema.ts` with no database; the
`sqlite-proxy` driver sends every query to a callback, so a 20-line adapter maps reads to `queryValues`, writes to
`execute` and `db.batch` to one all-or-nothing `execute`; Drizzle's migrator applies pending files in one `execute` and
skips applied ones. Joins need array rows: rows as objects lose a duplicate column name. Kysely and `kysely-codegen`
were considered first; `kysely-codegen` only generates types from an existing database. Drizzle does it
in TypeScript through its `sqlite-proxy` driver, and SQLAlchemy Core or SQLModel in Python with `compile`. The schema is
written once in code; handlers build their queries from it, and the migrations are SQL generated from it.

```ts
import { eq } from 'drizzle-orm';
import { drizzle } from 'drizzle-orm/sqlite-proxy';
import { todos } from './schema';

const isRead = (sql: string) => /^\s*(select|with)\b/i.test(sql);

export function db(context) {
  return drizzle(
    async (sql, params, method) => {
      if (isRead(sql)) {
        const r = await context.sql.queryValues('app', sql, params);
        return { rows: method === 'get' ? r.values[0] : r.values };
      }
      const r = await context.sql.execute('app', sql, params);
      return { rows: r.values ?? [] };
    },
    async (queries) => {
      const rs = await context.sql.execute('app', queries.map((q) => [q.sql, ...q.params]));
      return rs.map((r) => ({ rows: r.values ?? [] }));
    },
  );
}

export async function handle(context, event) {
  return db(context).select({ id: todos.id, title: todos.title }).from(todos).where(eq(todos.done, false));
}
```

```python
from sqlalchemy import Column, Integer, MetaData, String, Table, select
from sqlalchemy.dialects import sqlite
from sqlalchemy.schema import CreateTable

metadata = MetaData()
todos = Table(
    "todos",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("title", String, nullable=False),
    Column("done", Integer, nullable=False, server_default="0"),
)
dialect = sqlite.dialect(paramstyle="named")


def migrate(context, event):
    context.sql.execute("app", str(CreateTable(todos, if_not_exists=True).compile(dialect=dialect)))


def handle(context, event):
    q = select(todos.c.id, todos.c.title).where(todos.c.done == 0).compile(dialect=dialect)
    return context.sql.query("app", str(q), q.params)
```

Versioned migrations are generated at build time, by `drizzle-kit generate` in TypeScript or Alembic in Python, and
shipped inside the migration function. The hook applies the pending ones in order:

```text
build      schema code ──▶ migrations/0001_init.sql, 0002_add_due.sql, …
preApply   todo-migrate reads the applied versions from a _migrations table, then for each pending file:
           execute [the file's statements, INSERT INTO _migrations], all or nothing
runtime    handlers build queries from the same schema and call query or execute
```

What this asks of `context.sql`:

- SQL text with positional parameters, `?` with a list, or named ones, `:name` with an object, as rqlite's JSON form
  takes them;
- a list of statements in one `execute`, all or nothing, for a migration and its record;
- values that JSON carries: strings, numbers, booleans and null; a date or bytes is converted first;
- a `SELECT` goes to `query` and everything else to `execute`; a write sent to `query` fails on the read-only
  connection;
- no transaction spans two calls, an rqlite limit, so a builder's interactive transactions do not apply.

Because rqlite runs SQLite, a developer generates and tests migrations against a local SQLite file or `funcdctl dev`.
Which call returns the rows of `INSERT … RETURNING` is still to check.

## Gaps in today's code

| Gap | Where | Why the SQL provider hits it |
|---|---|---|
| No durable volume for an engine | `internal/provider/spec.go:63`; the container port has `Mounts` (`internal/runtime/runtime.go:95`), but the provider runtime never sets them (`internal/provider/runtime.go:218`) | the engine's data lives on disk; ADR-0087 is implemented, so this needs a new ADR |
| A recreated engine gets a new IP | `internal/provider/runtime.go:159-164` addresses `ip:port`; the catalog hides it behind the proxy's recorded port (ADR-0162) | the facade must follow the engine's current address |
| One owner kind per runtime | `internal/provider/runtime.go:36-38`; `pkg/funcd/funcd.go:1002` builds only the catalog's | a second `provider.NewRuntime` for the new kind |
| The dependency check knows kv, blob and links | `internal/health/checker.go:38` | a SQL binding needs its own kind there |
| Fixed backup member kinds | `internal/backup/workload/workload.go:155`, the `Write` switch, `RestoreMember` validation at `api/types/v1alpha1/backup.go:372` | a new member kind, a source and a loader |
| Fixed backup plan components | `internal/backup/plan/derive.go:98`; `funcdctl backup plan rqlite` is refused (`cmd/funcdctl/backup_plan_test.go:357`) | ADR-0223 leaves the rqlite keys to the ADR that adds rqlite |
| Fixed App sections | `api/types/v1alpha1/app.go:38-62` and `workload.Resolve` | a new section, with a deletion policy like `kv` |

Two documents already reserve room: ADR-0142 rule 9 names a future rqlite provider (requeue within the supervision
period, a steady pass writes nothing), and FEAT-0009 leaves the rqlite backup mode and the per-app restore to the
rqlite ADRs.

## Open questions

| Question | Decided in |
|---|---|
| The resource kind's name, around the `sql` vocabulary | the SQL database ADR |
| Who holds a write grant: an owner plus roles, as for KV, or a writers list on the database | the access ADR |
| Whether a Workflow step may bind an App's database, or only one its Workflow owns, as for KV stores (ADR-0178) | the access ADR |
| Result size and statement limits per call; how dates and bytes are passed; which call returns `RETURNING` rows | the access ADR |
| Whether the platform backup also copies every database | the backup ADR |
| The engine's memory and CPU limits, and a per-namespace database quota | the SQL database ADR |
| The containerd shim cost per engine | the SQL database ADR, measured |
| The shape of a change event, how it is named and delivered, and how a duplicate delivery is dropped | the change-feed ADR |
| The engine image: a static build from source or the glibc build on distroless; its version pin; how an engine is upgraded | the SQL database ADR |

## Sources

- [rqlite go.mod](https://github.com/rqlite/rqlite/blob/master/go.mod) · [rqlite/go-sqlite3](https://github.com/rqlite/go-sqlite3)
- [FAQ](https://rqlite.io/docs/faq/) · [security](https://rqlite.io/docs/guides/security/) · [data API](https://rqlite.io/docs/api/api/) · [read consistency](https://rqlite.io/docs/api/read-consistency/)
- [backup and restore](https://rqlite.io/docs/guides/backup/) · [CDC](https://rqlite.io/docs/guides/cdc/) · [client libraries](https://rqlite.io/docs/api/client-libraries/) · [install](https://rqlite.io/docs/install-rqlite/)
- [rqlite/sql](https://github.com/rqlite/sql) · [LICENSE](https://github.com/rqlite/rqlite/blob/master/LICENSE)
- [drizzle-kit generate](https://orm.drizzle.team/docs/drizzle-kit-generate) · [SQLAlchemy DDL constructs](https://docs.sqlalchemy.org/en/20/core/ddl.html) · [Alembic offline mode](https://alembic.sqlalchemy.org/en/latest/offline.html)
