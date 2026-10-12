# ADR-0232: The SQLDatabase resource and its rqlite engine

- **Status**: Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  added `-http-adv-addr` without which rqlited refuses to start, let a database with no directory be deleted, applied a
  resources change by a single teardown, and pinned the upgrade rule; the second found one Major, fixed: the teardown
  pass records the observed generation)
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, provider, rqlite, image
- **Realizes**: [FEAT-0011/F128](../feat/0011-feat-sql.md) (SQL databases)
- **Relates to**: [ADR-0231](0231-durable-storage-for-add-on-engines.md) (the engine's directory) ·
  [ADR-0087](0087-add-on-provider-runtime.md) (the add-on runtime) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md)
  (the pattern: a resource deployed as an engine) · [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)
  (embedded images) · [ADR-0082](0082-provider-model-catalog.md) (the provider catalog) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md) (the delete rules mirrored) · [ADR-0142](0142-supervision-by-periodic-re-convergence.md)
  (supervision) · [ADR-0206](0206-restore-and-held-boot.md) (the hold) · source: [design report](../reports/sql-provider-design.md)

## Context & Need

Apps need relational data: tables, indexes, joins and writes that succeed or fail together (FEAT-0011). The decider
chose rqlite and one dedicated engine per database (2026-10-12): rqlite links SQLite through cgo, so it runs out of the
daemon as an add-on engine, from funcd's own minimal image; one engine costs 4 to 7 MB at rest on the target box
(measured, design report).

This ADR adds the `SQLDatabase` kind and its reconciler, which deploys and supervises one rqlite engine per database and
reports when it is Ready. Functions reach a database in the next ADR (FEAT-0011 F129); this one gives that ADR an engine
address and a credential to use.

## Scenarios

- `scenario: database-starts-its-engine` — Given a namespace, When a client applies `SQLDatabase` `todo-db`, Then the
  platform runs one engine container from `funcd/runtime-rqlite` with a durable directory, and the database becomes
  Ready once the engine answers its readiness probe.
- `scenario: one-engine-per-database` — Given `todo-db` is Ready, When a second database `audit-db` is applied, Then a
  second engine runs with its own directory and its own credential.
- `scenario: engine-requires-its-credential` — Given a Ready database, When a client calls the engine without the
  database's credential, Then the engine refuses it; with the credential, it answers.
- `scenario: delete-reclaims-engine-and-data` — Given an empty Ready database, When it is deleted, Then its engine is
  removed and its directory reclaimed; while the platform is held, the directory is kept and reclaimed at the first
  boot after the release.
- `scenario: delete-of-never-started-database` — Given a database whose engine never started, as on the process
  runtime, When a client deletes it through the API, Then the delete succeeds.
- `scenario: resources-change-restarts-engine` — Given a Ready database, When its `spec.resources.memory` changes, Then
  the engine is recreated once with the new bound and its data is still there.
- `scenario: delete-refused-while-holding-data` — Given a database with a table, When a client deletes it through the
  API, Then the API answers 409 and the database, its engine and its data stay.
- `scenario: database-quota` — Given a namespace that holds `sql.maxDatabasesPerNamespace` databases, When a client
  creates one more, Then the API answers 400.

## Scope

**In**: the kind; its reconciler and status; the engine image and its entrypoint; the engine credential; the delete
protection and the quota; the provider catalog row; the in-process engine registry the access ADR reads.

**Out**: bindings, the binding count, the delete-while-bound rule, the function API and authorization (F129); the App section (F130); backup and restore (F131); the
change feed (F132); `funcdctl dev` (F133).

## Constraints & Decision drivers

- The daemon stays `CGO_ENABLED=0`; rqlite's SQLite driver is a C binding.
- The image is funcd's own, minimal and pinned, like duckdb's (decider); no prebuilt rqlite binary is static.
- A function never holds the engine's credential (FEAT-0011); the engine must refuse anyone else.
- Supervision by re-convergence: the reconciler requeues within the supervision period and its steady pass writes
  nothing (ADR-0142 rule 9).

## Alternatives considered

1. **Upstream `rqlite/rqlite` image**: no build work and pinnable by digest, but it is `alpine` with a shell and ICU
   libraries, runs as uid 1000 and is not static. Rejected (decider: our own minimal image). A static build loses
   SQLite extension loading (`-extensions-path`), which funcd does not use.
2. **The upstream glibc binary on a distroless glibc base**: proven on the target box, but larger and per-architecture
   asymmetric (no arm64 musl build). Kept as the fallback if the static build fails.
3. **A stored random password per database**: needs a Secret per database and rotation handling. Rejected for a
   password derived from the node master, re-derived at every start, as the catalog token is (ADR-0137).
4. **Engine address in `status`**: visible, but a restored metastore would carry a stale address. Rejected for an
   in-process registry filled by the reconciler.

## Decision

1. **The kind.** `SQLDatabase` (namespaced) has `spec.resources` (`cpu`, `memory`, as CatalogService) and a status with
   `phase`, `conditions` and `bindings` (the count the access ADR fills). `Validate` refuses a `cpu` or `memory` value
   the runtime cannot parse. When `memory` is empty the reconciler applies `256Mi`; the spec is not rewritten.
2. **The engine.** The reconciler converges a `ProviderSpec` with image `ImageFor("rqlite")`, port 4001, readiness
   `GET /readyz` → 200, one replica, the spec's resources, and storage at `/data` owned by uid and gid 65532 (ADR-0231).
   Its env carries `FUNCD_SQL_PASSWORD`. A Ready pass requeues after the supervision period, a starting engine after
   2 s; status is written only when it changes (ADR-0142 rule 9). When `metadata.generation` differs from
   `status.observedGeneration`, the reconciler tears the engine down before converging, so a new bound takes effect;
   the data stays on disk (ADR-0231). The pass that tears the engine down records `status.observedGeneration` and the
   Ready condition's `ObservedGeneration` in the same status write, whatever the engine's state, so one change causes
   exactly one teardown. A runtime that does not implement `provider.Volumes` makes `NewReconciler` fail
   `Invalid`.
3. **The image.** `images/runtime/rqlite/Dockerfile` builds `rqlited` from the pinned rqlite source tag, statically with
   musl, and the entrypoint `cmd/funcd-rqlite` (pure Go, static), and copies both onto `gcr.io/distroless/static`
   pinned by digest, with `USER 65532:65532`, the uid and gid the storage spec names. Every input is pinned by digest or hash. `just build-runtime-images` exports it
   to `internal/runtime/embedimg/rqlite.tar`. If the static build cannot be made to work, the implementation uses the
   upstream glibc binary, checked against its sha256, on `gcr.io/distroless/base-debian12`, and says so in its review.
   The rqlite version is pinned in the Dockerfile; a funcd release that changes it reaches every engine at the next
   daemon start, when the boot sweep removes the containers and `Converge` starts the new image on the same
   directory. A version that changes rqlite's on-disk format needs its own ADR, with a test that opens a directory the
   old version wrote.
4. **The entrypoint.** `funcd-rqlite` writes `/tmp/auth.json` (0600, in the container's own filesystem, never on the
   host's disk) on every start: user `funcd` with permission `all` and the password from `FUNCD_SQL_PASSWORD`, and the
   anonymous user `*` with `ready` only. It then execs `rqlited -node-id <name> -http-addr 0.0.0.0:4001
   -http-adv-addr 127.0.0.1:4001 -raft-addr 127.0.0.1:4002 -auth /tmp/auth.json /data/db`; rqlited refuses an
   unspecified advertised address, and Raft stays on loopback so a recreated engine with a new IP keeps its stored
   address. Later ADRs add flags through env the entrypoint reads (ADR-0235: the change feed).
5. **The credential.** `EnginePassword(master, ns, name)` is HMAC-SHA256 of the node master over
   `funcd-sql-engine/<ns>/<name>`, base64url. Nothing stores it; a master rotation changes it at the next engine start.
6. **The registry.** The reconciler records each Ready engine's `ip:4001` in an in-process `Engines` registry and drops
   it when the engine is not Ready or deleted. Its readers are the deletion-protection prober here and the access ADR.
7. **Delete.** On delete the reconciler tears the engine down and, unless the platform is held, reclaims its directory
   (ADR-0231). At boot, before the controller runs, it reclaims the directory of every engine with no `SQLDatabase`
   left, unless held; while held, those directories appear in `hold status` as `sqlOrphans` (`<ns>/<name>`), beside
   ADR-0206's `bucketOrphans`. A database deleted and recreated under the same name while held keeps the old
   directory, so its data comes back; deleting it again after the release starts it empty. The admission
   `sqldatabase-deletion-protection` refuses an API delete with 409 while the engine reports a user table (`SELECT
   count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`), or while it cannot answer and
   `Volumes.List` names its directory (fail closed, the message says why); with no directory, there is no data and the
   delete passes. The access ADR adds the delete-while-bound rule. A controller's delete through the store is not checked, as for
   KVStore.
8. **Quota.** The admission `sqldatabase-quota` refuses a create beyond `sql.maxDatabasesPerNamespace`
   (`FUNCD_SQL_MAX_DATABASES_PER_NAMESPACE`; 0 ⇒ the default 20, negative disables it), as KV and Bucket do.
9. **Catalog row.** `pkg/funcd/providers.go` lists `sql` as an add-on; the access ADR adds its binding `spec.sql`.

## Temporary workarounds

- **No real engine in the e2e suite.** The in-process platform runs no containers, so the scenario tests use a fake
  `provider.Runtime` and a fake engine over HTTP; the real image is checked by `just smoke-rqlite-image`. *Exit:* the
  SQL lane of the access ADR (F129) runs the real engine.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: SQLDatabase
metadata:
  name: todo-db
  namespace: default
spec:
  resources:
    memory: 64Mi
status:
  phase: Ready
  conditions:
    - type: Ready
      status: "True"
```

```go
// api/types/v1alpha1/sqldatabase.go
const KindSQLDatabase Kind = "SQLDatabase"

type SQLDatabase struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SQLDatabaseSpec   `json:"spec"`
	Status     SQLDatabaseStatus `json:"status,omitempty"`
}

type SQLDatabaseSpec struct {
	Resources ResourceSpec `json:"resources,omitempty"`
}

type SQLDatabaseStatus struct {
	Status   `json:",inline"`
	Bindings int `json:"bindings,omitempty"`
}

// internal/services/sql/engine.go (package sql, imported as sqlsvc)

// EnginePassword derives a database's engine password from the node master (ADR-0232).
func EnginePassword(master []byte, ns v1.NamespaceName, name v1.ObjectName) string

// Engines is the in-process registry of Ready engines' addresses (ADR-0232); safe for concurrent use.
type Engines struct{ /* unexported */ }

func NewEngines() *Engines
func (e *Engines) Set(ns v1.NamespaceName, name v1.ObjectName, addr string)
func (e *Engines) Delete(ns v1.NamespaceName, name v1.ObjectName)
func (e *Engines) Address(ns v1.NamespaceName, name v1.ObjectName) (string, bool)

// internal/services/sql/reconcile.go

type ReconcilerDeps struct {
	Store             store.Store
	Provider          provider.Runtime // the runtime with OwnerKind SQLDatabase; implements provider.Volumes
	Engines           *Engines
	Master            []byte
	ImageFor          func(runtime string) string
	Hold              hold.Gate
	Logger            *slog.Logger
	SupervisionPeriod time.Duration
}

func NewReconciler(d ReconcilerDeps) (*Reconciler, error)
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
func (r *Reconciler) ReclaimOrphans(ctx context.Context) error // at boot, before the controller runs; no-op while held
func (r *Reconciler) Orphans(ctx context.Context) ([]v1.ObjectRef, error) // directories with no SQLDatabase, for hold status

// internal/controlplane/admission/sqldatabase.go

// SQLProber reports whether a database's engine holds a user table (ADR-0232).
type SQLProber interface {
	HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
}

func NewSQLDatabaseQuotaAdmission(r StoreReader, maxPerNamespace int) Admission
func NewSQLDatabaseDeletionProtectionAdmission(r StoreReader, p SQLProber) Admission
```

| Status reason (Ready False) | When |
|---|---|
| `EngineNotReady` | the engine runs but does not answer `/readyz` yet |
| `EngineConvergeFailed` | the runtime could not create or start the engine, or storage is not configured |

| Consumes | Exposes |
|---|---|
| `provider.Runtime` + `provider.Volumes` (ADR-0231), the node master, `ImageFor`, the hold | the kind, its API routes and SDK plural `sqldatabases` |
| config `sql.maxDatabasesPerNamespace` | the `Engines` registry, `EnginePassword` |
| | the image `funcd/runtime-rqlite`, the `sql` catalog row |

## Implementation plan

1. Kind wiring: `api/types/v1alpha1/sqldatabase.go` (with `Validate`), `metadata.go` (`Kind`, `NewObject`,
   `AllKinds`, the count test), `internal/controlplane` handlers (including the `stampTypeMeta` case), routes, stubs,
   `pkg/sdk/kinds.go`, `just generate`. `internal/platform/hold` and `internal/controlplane/hold.go`: `sqlOrphans`.
2. `internal/services/sql`: `engine.go`, `reconcile.go`; the prober for the admission (`/db/query` with the password).
3. `cmd/funcd-rqlite/main.go`; `images/runtime/rqlite/Dockerfile` and a sha/digest pin file; `embedimg` (`rqlite.tar`
   placeholder, `tarFile` entry); `justfile`: the image in `build-runtime-images`, and `smoke-rqlite-image`, which runs
   the image with docker and checks `/readyz`, a refused call without the password and an accepted one with it, a
   write that survives killing the container and starting a new one on the same directory (the real-container proof of
   ADR-0231), and the image's uid.
4. `pkg/funcd`: a second `provider.NewRuntime` with `OwnerKind: SQLDatabase` and the storage root; the reconciler,
   `ReclaimOrphans` before the controller runs, the admissions in `partAdmissions`, the catalog row;
   `WithSQLProviderRuntime` to inject a runtime in tests. Config `sql.maxDatabasesPerNamespace`.
5. Tests: unit tests of `EnginePassword`, `Engines`, the entrypoint's auth file and arguments, the reconciler with a fake
   runtime, both admissions with a fake prober; one `TestScenario<Name>` per scenario in `pkg/funcd`, with a fake
   runtime and an `httptest` engine; `engine-requires-its-credential` is checked by the smoke recipe and recorded in the
   review.

**Definition of done**: the tests pass; the catalog's tests pass unchanged; `just smoke-rqlite-image` passes once on a
host with docker; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] The kind is threaded through every site of a new kind, `stampTypeMeta` included, and `funcdctl apply` creates it.
- [ ] The engine spec: image, port 4001, `/readyz`, one replica, storage `/data` as 65532, bounds with the default
      memory.
- [ ] The password never appears in status, logs or events; the auth file is 0600 and rewritten at every start.
- [ ] The Dockerfile pins every input; the final image holds `rqlited`, `funcd-rqlite` and nothing to run a shell.
- [ ] Delete tears the engine down and reclaims its directory unless held; boot reclaims orphans unless held, and
      `hold status` lists them as `sqlOrphans` while held.
- [ ] The image's user equals the storage spec's uid and gid (65532), checked by the smoke recipe.
- [ ] The deletion protection fails closed when the engine cannot answer.
- [ ] One test per scenario, named for it, except `engine-requires-its-credential`, which the smoke recipe checks.
- [ ] The entrypoint's arguments include `-http-adv-addr` and the auth file lives outside `/data`.

## Consequences

- Each database costs one engine, about 5 MB at rest and up to its memory bound under load.
- funcd's binary grows by about 12 MB per architecture for the embedded image.
- A database whose engine is broken cannot be deleted through the API until it answers; an App with deletion `delete`
  still removes it.

## Open questions

- The containerd shim's memory per engine: measured on the target box during implementation, recorded in the review
  (taken over from ADR-0231).

## References

- rqlite security, data API and install docs; `github.com/rqlite/rqlite/v10` v10.5.3 (design report, Sources).
- [ADR-0231](0231-durable-storage-for-add-on-engines.md), [ADR-0087](0087-add-on-provider-runtime.md),
  [ADR-0137](0137-per-caller-catalog-query-rbac.md) (a credential derived from the node master).
