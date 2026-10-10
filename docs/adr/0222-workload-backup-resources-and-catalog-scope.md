# ADR-0222: Workload backup resources and catalog scope

- **Status**: Accepted (2026-10-10, by an `adr-batch` run after a clean `adr-judge` gate; the defaults below were not confirmed one by one)
- **Date**: 2026-10-10
- **Deciders**: green-0-rabbit
- **Tags**: backup, restore, disaster-recovery, kvstore, blob, catalog, rbac, app
- **Realizes**: [FEAT-0009/F111](../feat/0009-feat-disaster-recovery.md) (workload data protection; DR plan item DR-10)
- **Builds on (additions only; no decision of theirs changes)**: [ADR-0199](0199-app-resource.md) (`backupSchedules`,
  reserved by its design note Decision 12); [ADR-0206](0206-restore-and-held-boot.md) (the per-namespace holds its
  Decision 6 leaves to DR-10: a `restore run` step, `hold.NamespaceGate`, a release flag); ADR-0203
  (`gocloud.OpenOptions.Static`); ADR-0204 (`envelope.Parse`); ADR-0209 (`LoadPrefix`); ADR-0170 (one pair).
- **Settles**: ADR-0208's open question on the catalog verify (Decision 5); ADR-0211 open question 1; ADR-0214 open
  question 1, its API part. **Relates to**: ADR-0018, 0074 · ADR-0063, 0147 · ADR-0086 · ADR-0194, 0196 · ADR-0205 · ADR-0223

## Context & Need

The platform and instance backups (ADR-0202 to ADR-0209) are the operator's; an app owner cannot back up or restore one
KVStore, Bucket or catalog: no kind exists (`api/types/v1alpha1`), and the App design reserves `backupSchedules` (line 636).
**Purpose**: three namespaced kinds an app owner writes through the REST API, `BackupSchedule` (definition), `Backup` (run
record, also made by hand or a hook) and `Restore` (request), run by the daemon onto an app-owned S3 target, sealed to the owner.

## Scenarios

- **scenario: schedule-runs-at-slot** — Given `orders-nightly` (`0 3 * * *`, `Europe/Paris`, KVStore and Bucket), When 03:00
  Paris passes, Then Backup `orders-nightly-<slot minutes>` is `Succeeded`, its parts then `manifest.yaml` are on the
  target, each opening with either recipient's identity, and `lastSuccessTime` is set.
- **scenario: schedule-applies-live** — Given that schedule updated to `0 4 * * *`, Then the next Backup starts at 04:00, no restart.
- **scenario: catch-up-once-no-overlap** — Given funcd down across three slots, When it starts, Then one Backup is created;
  given a Backup still `Running` at the next slot, Then that slot creates none.
- **scenario: backup-outlives-app** — Given an App with a `backupSchedules` entry and two `Succeeded` Backups, When the App
  is deleted, Then the schedule is collected and both Backups stay until `expireTime`, then are deleted.
- **scenario: api-creates-backup** — Given a developer of `orders`, When it POSTs a Backup naming the schedule, Then it runs; a viewer gets 403.
- **scenario: catalog-checkpoint-verified** — Given catalog `lake` whose C1 lists f1, When a cleanup deletes f1 mid-copy,
  Then attempt 2 runs, `catalog.db` is written after its files; three failed attempts ⇒ `Failed`, `CatalogUnstable`.
- **scenario: target-in-store-refused** — Given `blob.target` `s3://store`, When a schedule names `s3://store?prefix=b/`,
  Then 400 names `spec.target.url`; another bucket on that endpoint is stored with reason `SameProvider`.
- **scenario: restore-new-name** — Given a `Succeeded` Backup and an empty KVStore `orders-state-r`, When a developer
  restores `orders-state` as it, Then its keys equal the backup's, the source unchanged; non-empty ⇒ `Failed`, `NotEmpty`.
- **scenario: restore-in-place** — Given that Backup, When a developer creates an in-place Restore, Then 403; an admin's
  first completes Backup `<restore>-before` (platform held or not), holds the namespace during the load; a failed export ⇒ `ExportFailed`, no write.
- **scenario: platform-restore-holds-namespace** — Given G with schedules in `orders`, none in `web`, When G is restored
  and released, Then `web`'s timers and slots fire; `orders`' wait for `funcdctl hold release --namespace orders`, a Backup by API there runs.
- **scenario: rpo-risk** — Given a daily schedule whose 03:00 Backup fails, Then `rpoRisk`, the gauge and one Warn are on; a success clears them.
- **scenario: run-interrupted** — Given a Backup `Running` when funcd is killed, Then at start it is `Failed` (`Interrupted`).

## Scope

**In**: the three kinds, their wiring, scopes, the App section, layout, sealing, target, catalog order, guard, restores,
holds, status, authorization. **Out**: ADR-0202 to 0209, 0211, 0223; Q14's `resources` scope; rqlite; the app-level cut.

## Constraints & Decision drivers

Decided 2026-10-06 (report §5): Q11, Q13, Q14, Q7 (sealed whenever a target is set), Q8 (the write credential puts and
lists only). App design (2026-10-07): App scope and section; Backups outlive schedule and App (ttl or manual delete, no
cascade); one creation path, a REST create; ADR-0211's grammar; at most one catch-up, no overlap. Fail closed; reuse.

## Alternatives considered

| Option | Outcome |
|---|---|
| **Three kinds** ✅; `Restore` as a verb on `Backup` | a verb leaves no record or status of a restore |
| **A Backup names its schedule** ✅; a Backup with its own scope and target | one definition of scope, target, keys; a hook writes one field |
| **Full copy per Backup, uid path** ✅; ADR-0208's epochs and object ids | a Backup expires whole by object age, no delete (Q8); epochs need a rebaseline per schedule |
| **Inline recipients, restore credentials in Secrets** ✅; the platform's recipients; restore in funcdctl | the owner decrypts its own; no API loads a store from a client |
| **SQLite reader in the daemon** ✅; ask the engine; skip verify | the engine holds the live catalog, not C1; skipping keeps ADR-0208's Risk |
| **RBAC at admission, the run acts as the platform** ✅; a Cedar action (`backup::read` on each store) at admission | Cedar's principals are Functions (ADR-0074 Decision 1), and a developer already writes its namespace's Policies (`internal/auth/rbac/rbac.go`): the action adds a check, not a boundary |

## Decision

**1. Kinds** (NEW, namespaced, `funcd.io/v1alpha1`; plurals `backupschedules`, `backups`, `restores`; shapes in Contracts,
the report's sample extended). A schedule's `schedule` and `timeZone` pass `CheckCron` (ADR-0211 Decision 5); its `ttl` is a
`Duration` (ADR-0194; default `720h`). Backup and Restore specs are immutable (admission, as `admission/site.go`); controllers own status.

**2. Scope.** One form: names (`kvStores`, `buckets`, `catalogs`), `app` (the App's `kv`, `buckets`, `catalogs` entries,
`name` and `ref`) or `resourceGroup` (its KVStores, Buckets, CatalogServices). Resolved per run (`workload.Resolve`), recorded
in `status.members`; a named member missing ⇒ `MemberNotFound`; no link-validity admission (ADR-0121): an App writes schedule
and stores together. An empty-scoped `AppBackupSchedule` entry (`name` or `ref` plus the spec, as `AppFunction`) gets `app: <App>`.

**3. Runs.** The controller keeps `status.lastScheduleTime` (unset ⇒ creation time). When `Next(lastScheduleTime) ≤ now` it
takes the newest due slot S: skipped while `paused`, while `HeldIn(ns)` or while a Backup of the schedule is `Pending` or
`Running`; else it creates Backup `<schedule>-<S in Unix minutes>` (`Conflict` counts as done), then sets `lastScheduleTime = S`.
Missed slots collapse into S: one catch-up after downtime, a hold or a pause; a spec change applies at the next pass (Q13).
Admission caps schedule names at 54 characters, Restore names at 56 (63, `ids.go:19`, less `-` and 8 digits of Unix minutes
to 2160, or `-before`). A Backup by API names `spec.schedule`; its optional `spec.ttl` overrides, at most the schedule's
(admission). A Backup carries the schedule's `resourceGroup` and no ownerReference (no GC pair, ADR-0170); the controller
deletes it at `expireTime` (creation + ttl); a ResourceGroup with Backups refuses deletion unless forced (ADR-0170 Decision 7).

**4. Backup run.** `Pending → Running → Succeeded | Failed` (`RunPhase`, `workflowrun.go:81-88`), one attempt; `Running` at
start ⇒ `Failed` (`Interrupted`); `Pending` waits while the platform is held, but for a Restore's export (`status.export`). It
resolves schedule (gone ⇒ `ScheduleNotFound`), credential, members, and writes `<target>/<ns>/<backup>-<uid>/` (proposed):

```
kv/<store>/part-00000 …                ADR-0209 SnapshotPrefix records, ADR-0203 framing, one Seal stream, 8 MiB parts
blob/<bucket>/part-00000 …             every object of s3/<ns>/<bucket>/: key, EncodeObject(type, metadata, bytes)
catalog/<name>/<attempt>/files/…       the catalog prefix but catalog.db; then catalog/<name>/<attempt>/catalog.db/…
manifest.yaml                          workload.Manifest, plain, created last
```

Every object goes `IfNotExist` (a refused condition ⇒ `TargetUnconditional`; uid paths are unique, so no probe or lock).
Each file is one `Seal` stream of `envelope.Parse(recipients)`. Status records `target`, `path`, `members`, recipient
fingerprints: a Backup restores after its schedule is gone. No owner is paused; the cut is per member.

**5. Catalog copy order** (owned here; ADR-0208's mirror keeps Decision 5, no verify). CatalogService c, its `spec.catalog`
(bucket b, prefix p, ADR-0086): (1) read `<p>/_ducklake/catalog.db` into C1; (2) copy every other object under
`s3/<ns>/<b>/<p>/`; (3) `CatalogFiles(C1)`: each `ducklake_data_file` and `ducklake_delete_file` path, resolved against
table, schema and data path, must be a key of (2), else the next attempt (3 at most, then `CatalogUnstable`); (4) write C1;
(5) the manifest after all members. A restore writes the files first and `catalog.db` last.

**6. Target and keys.** `target.url` is `s3://` only (ADR-0203 §5 syntax; a `file://` path is outside the namespace), narrowing
Q8 (proposed; accepted as default, not confirmed by the decider). Secrets resolve through `secrets.Resolver.ResolveEnv` (`secrets.go:53`) as the
catalog's injector (`defaultDeveloperFor`, `internal/services/catalog/catalog.go:184`), a developer of `<ns>` only: keys
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional `AWS_SESSION_TOKEN` (schedule: put and list; Restore: get and list);
`identitySecret` key `AGE_IDENTITY` (ADR-0204 Decision 5 types). Recipients: 2 or more, distinct, one family, `age.ParseRecipients`
in admission; no `none`. The owner sets a lifecycle rule on `<ns>/` of ⌈ttl/24h⌉ + 1 days (`examples/workload-backup.md`).
**Guard** at admission: `gocloud.CompareDomains(store, target.url)` (ADR-0208 Decision 3; store `blob.target` or `FileURL(blob.dir)`):
`OverlapStore` ⇒ 400 naming `spec.target.url`; `OverlapProvider` ⇒ stored, `Ready` reason `SameProvider`, one Warn.

**7. Restore.** Each member has `as` (an empty KVStore or Bucket), `asCatalog` (an empty bucket prefix; the owner then
points a CatalogService at it) or `inPlace: true`. The controller reads the manifest with the Restore's credential, checks
every `sha256` before writing (`Integrity`), opens with `envelope.Opener`, loads KV by `LoadPrefix` and blob by `Put` into
`blob.Prefixed(shared, Sources.BucketPrefix(ns, as))`; non-empty ⇒ `NotEmpty`; a failure empties what it wrote. Restores
and exports run while the platform is held (recovery order). **In place**: admission also authorizes `update` on
`Namespace` `<ns>` (built-in RBAC: `admin` only); the controller sets `status.export`, runs Backup `<restore>-before` of
its schedule (gone ⇒ `ScheduleNotFound`; failed ⇒ `ExportFailed`, no write), holds the namespace (reason `restore`),
empties the target (`DropPrefix`, `BucketPurger.Purge`), loads, lifts it.

**8. Per-namespace hold.** `<storage.dataDir>/.hold-ns/<ns>.<reason>` holds an ADR-0206 `hold.Marker`; `HeldIn(ns)`: the platform
is held or `<ns>.restore` or `<ns>.data` exists (only these reasons are read). ADR-0206 Decision 6's per-namespace runners ask the
daemon's `*hold.Hold`, as a `hold.NamespaceGate`, `HeldIn` of each object's namespace: timers, blob sources, Sensors, dead-letter
replay, runs and the App reconciler (NEW `app.Deps.NamespaceHold`; its deadline reads `ReleasedAtIn(ns)` for `ReleasedAt`), as do
Decision 3's slots; the platform-wide backup and retention runners and data reclaims keep `Held()`; Backups by API, exports and
Restores skip `HeldIn` (Decisions 4, 7). `funcd restore run` adds, after its step 4, a `data` marker per namespace holding a
`BackupSchedule` (its resources return without their data) and prints them; `funcdctl hold release --namespace <ns>` (NEW,
repeatable) removes it, authorizing `update` on that `Namespace`, once its time is in `.hold-ns-released/<ns>`.

**9. Status and alert** (ADR-0205 pattern). `earliestRestorePoint` is the oldest stored `Succeeded` Backup; condition `Ready`.
`rpoRisk`: not paused, and the slot after the newest `Succeeded` Backup's passed with its Backup failed or absent, or a second
slot passed. Meters: `funcd.backup.runs`, `.duration_ms` with `stream=workload`; NEW `funcd.backup.workload_rpo_risk` (gauge,
schedules at risk); Warn `workload backup rpo at risk` (`namespace`, `schedule`) on turning on, Info on clearing.

**10. Authorization** (`storeHandlers.authorize`, `internal/auth/routing.go`). Viewers get and list the three kinds;
developers and admins also create and delete them (a hook creates Backups) and update BackupSchedules; Backups and Restores
refuse updates (immutable); an `inPlace` Restore also needs `update` on `Namespace`; a Backup's delete drops its record only.
A run acts as the platform: RBAC at admission is the whole boundary; Cedar's principals are Functions (ADR-0074 Decision 1),
so no Cedar action is asked (proposed; accepted as default, not confirmed by the decider).

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/backup.go (NEW): each kind has TypeMeta, ObjectMeta, Spec, Status, GroupVersionKind, Validate; JSON
// keys lowerCamel, omitempty but for schedule, scope, target, url, credentialsSecret, recipients, backup, source, members, kind, name.
type BackupScheduleSpec struct{ Schedule, TimeZone string; TTL Duration; Paused bool; Scope BackupScope
	Target BackupTarget; Encryption BackupEncryption }
type BackupScope struct{ KVStores, Buckets, Catalogs []ObjectName; App ObjectName; ResourceGroup ResourceGroupName }
type BackupTarget struct{ URL string; CredentialsSecret ObjectName }; type BackupEncryption struct{ Recipients []string }
type BackupScheduleStatus struct{ Status `json:",inline"`; LastScheduleTime, LastSuccessTime, EarliestRestorePoint *Timestamp
	Active ObjectName; RPORisk bool `json:"rpoRisk,omitempty"` }
type BackupStatus struct{ Status `json:",inline"`; Phase RunPhase // shadows Status.Phase, as WorkflowRunStatus
	StartTime, CompletionTime, ExpireTime *Timestamp; Target, Path string; Members []BackupMember; Recipients []string }
type BackupSpec struct{ Schedule ObjectName; TTL Duration }; type BackupMember struct{ Kind Kind; Name ObjectName; Bytes int64 }
type RestoreSpec struct{ Backup ObjectName; Source RestoreSource; Members []RestoreMember }; type RestoreSource struct{ CredentialsSecret, IdentitySecret ObjectName }
type RestoreMember struct{ Kind Kind; Name, As ObjectName; AsCatalog *CatalogRef; InPlace bool } // exactly one target
type RestoreStatus struct{ Status `json:",inline"`; Phase RunPhase; StartTime, CompletionTime *Timestamp; Export ObjectName }
type AppBackupSchedule struct{ Name, Ref ObjectName; BackupScheduleSpec `json:",inline"` } // AppSpec.BackupSchedules

package workload // internal/backup/workload (NEW)
const Format = 1; type Member struct{ Kind v1.Kind; Name v1.ObjectName }
type Sources struct{ KV func(prefix string) snapshot.Source; Blob blob.Bucket // ADR-0209 SnapshotPrefix; the shared store
	BucketPrefix func(ns v1.NamespaceName, b v1.ObjectName) string } // pkg/funcd passes its bucketPrefix (funcd.go:1986)
type Options struct{ Target blob.Bucket; Path string; Seal backup.Seal; Recipients []string }
type MemberFile struct{ Kind v1.Kind; Name v1.ObjectName; Attempt int; Files []backup.StoreFile } // catalog: files, catalog.db
type Manifest struct{ Format int; Namespace, Backup, Funcd string; At v1alpha1.Timestamp; Members []MemberFile; Recipients []string }
func Resolve(ctx context.Context, st store.Store, ns v1.NamespaceName, s v1.BackupScope) ([]Member, error) // Decision 2
func Write(ctx context.Context, ns v1.NamespaceName, ms []Member, src Sources, o Options) (Manifest, error) // Decisions 4, 5
func ReadManifest(ctx context.Context, src blob.Bucket, path string) (Manifest, error)
func Integrity(ctx context.Context, src blob.Bucket, path string, m Manifest) error // every part's sha256; fault.Invalid
func Records(ctx context.Context, src blob.Bucket, path string, f backup.StoreFile, open backup.Unseal) func() (snapshot.Record, error)
func EncodeObject(contentType string, md map[string]string, body []byte) []byte // uvarint-framed, as ADR-0203; DecodeObject reverses it
func DecodeObject(v []byte) (contentType string, md map[string]string, body []byte, err error)
func CatalogFiles(db []byte) ([]string, error) // modernc.org/sqlite on a read-only temp copy; keys relative to the prefix
// NEW additions to accepted packages
func (d *driver) LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error // kvstore badger, memory; non-empty ⇒ fault.Conflict
type StaticKey struct{ ID, Secret, Session string } // gocloud.OpenOptions.Static *StaticKey: s3:// only, not with CredentialsFile
func Parse(recipients []string) (*Sealer, error)   // internal/backup/envelope: ADR-0204 Decision 2 recipient rules, NoSecrets
type NamespaceGate interface{ HeldIn(ns v1.NamespaceName) bool; ReleasedAtIn(ns v1.NamespaceName) time.Time } // internal/platform/hold; HeldIn: <ns>.restore, <ns>.data only
var NeverNamespace NamespaceGate = never{} // *Hold and never implement it; ReleasedAtIn: max(ReleasedAt, .hold-ns-released/<ns>)
func (h *Hold) HoldNamespace(ns v1.NamespaceName, m Marker) error; func (h *Hold) ReleaseNamespace(ns v1.NamespaceName, reason string) error // writes .hold-ns-released/<ns> first, as Release
func MarkNamespaces(dataDir string, nss []v1.NamespaceName, m Marker) error // offline, by restore run
```

| consumes | exposes |
|---|---|
| ADR-0211 `CheckCron`, `cron.Parse`, `Next`; ADR-0203 `IfNotExist`, framing, `StoreFile`, `Seal`, `Unseal`; ADR-0204 `Opener`, `Fingerprint`; ADR-0208 `CompareDomains`, `FileURL`; ADR-0209 `SnapshotPrefix`; ADR-0206 `hold`, `app.Deps`; `secrets.Resolver.ResolveEnv`, the catalog's `defaultDeveloperFor`; `bucketPrefix` (`pkg/funcd`); `PrefixManager.DropPrefix`; `gc.BucketPurger`; `blob.Prefixed`; aws-sdk-go-v2 `credentials` v1.19.25 (in `go.mod`); `modernc.org/sqlite` (NEW) | the three kinds and routes; `AppSpec.backupSchedules`; packages `workload` and `internal/services/backup`; the additions above, `app.Deps.NamespaceHold` (`NamespaceGate`'s methods); reasons `MemberNotFound`, `ScheduleNotFound`, `TargetUnconditional`, `CatalogUnstable`, `Interrupted`, `NotEmpty`, `Integrity`, `ExportFailed`, `SameProvider`; gauge `funcd.backup.workload_rpo_risk`; `hold release --namespace` |

## Implementation plan

**Wiring per kind**: `api/types/v1alpha1/metadata.go` (constants, `Kind.Validate`, `NewObject`, `AllKinds`,
`metadata_test.go` count); `internal/controlplane/{controlplane,handlers,routes,routes_rest,stubs}.go` (`Handlers`, `crudKind`,
the `stampTypeMeta` switch, `registerNamespacedCRUD`); `pkg/sdk/kinds.go`; `internal/gc/gc.go` (App → BackupSchedule);
`api/types/v1alpha1/app.go` and `internal/app/reconcile.go` (`entries`); `pkg/funcd/funcd.go` (admissions, controllers,
`Sources.BucketPrefix`); `just generate`. **Files**: NEW `api/types/v1alpha1/backup.go`, `internal/backup/workload/`,
`internal/services/backup/` (reconcilers), `internal/controlplane/admission/backup.go`, `examples/workload-backup.md`;
edits in `internal/kvstore/{badger,memory}`, `internal/blob/gocloud`, `internal/backup/envelope`, `internal/platform/hold`,
`cmd/funcd/restore.go`, `cmd/funcdctl/hold.go`. **go.mod**: `modernc.org/sqlite`. **Order**: after ADR-0203 to 0206, 0208,
0209, 0211. **Blueprint** (at acceptance): the DR bullet gains "app-owned backup schedules, records and restore requests".
**Test plan**: a `TestScenario<Name>` per scenario on ADR-0203's S3 stub, a manual clock, `shortDataDir`; a CLI `apply` per kind through
`controlplane.NewServer`; units `TestResolveScopes`, `TestObjectFrame`, `TestCatalogFilesRelative`, `TestLoadPrefixRefusesNonEmpty`,
`TestBackupNameFromSlot` (54 characters give 63; 55 refused), `TestRPORiskRule`, `TestReleaseNamespace` (after it, `HeldIn` false and
`ReleasedAtIn` the release time); `TestEveryRunnerConsultsHold`, one namespace held: per-namespace runners act outside it only.
**Definition of done**: `scripts/agent/d go test -race -count=1`, `just ci` green; no identity or path leak.

## Review checklist

- [ ] Every wiring site per kind, `stampTypeMeta` included; Backup and Restore specs immutable; no ownerReference on a Backup.
- [ ] Slots from `cron.Next`; one catch-up, no overlap; objects `IfNotExist`, manifest last; catalog files before `catalog.db`.
- [ ] `s3://` only, guard at admission; names at most 54 and 56; a Backup's `ttl` at most its schedule's; no Secret in status or logs.
- [ ] In place: `update` on `Namespace`, export first, even held; `restore run` writes `data` markers; `HeldIn` per namespace, `Held` platform-wide; a test per scenario.

## Consequences

**Positive**: owners protect and restore their own data, sealed to their keys, beyond schedule, App and platform restore. **Negative (accepted)**: a full copy per Backup; a Backup by hand needs a schedule; a Restore puts a
read credential and an identity in namespace Secrets; in place needs a cluster admin for now; a deleted record leaves its
objects to the lifecycle rule; restored objects pass through memory; a larger binary (SQLite in Go; the implementation PR
measures it). **Risk**: absolute catalog paths, or a DuckLake data path not overridden at ATTACH, break a restore into another prefix.

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Why |
|---|---|---|
| Field names | `schedule`, `timeZone`, `ttl`, `scope`, `target.url`, `target.credentialsSecret`, `encryption.recipients` | report sample; ADR-0211 open question 1; `retain`, `artifact` unused |
| App scope on change; `Restore` a kind; ttl | resolved per run, recorded; a kind; `720h` | a Backup states what it holds; a request needs a record; ADR-0208 `retention` |
| Recipients; `none`; restore credentials | inline, 2 or more; no `none`; Secrets named by the Restore | Q7; the owner opens its own backups |
| Per-namespace hold | Decision 8; held after `restore run` when the namespace holds a schedule | travels with the data like ADR-0206's marker |
| In-place export; namespace admin | `<restore>-before`, the schedule's ttl; `update` on `Namespace` | no namespace-scoped admin role exists (`internal/auth/rbac`) |
| Hook client | a follow-up ADR: a Function credential for the API and a shim context member (ADR-0141) | ADR-0214 open question 1; the API is a plain create |
| `file://` targets (Q8 names the local file system); Cedar (Q11 names it) | `s3://` only (Decision 6), FEAT-0009's Targets row then reads "F111: S3-compatible only"; no Cedar action (Decision 10) | a `file://` URL in a namespaced object names a host path outside the namespace (alternative: `file://` under an operator-set per-namespace root, a NEW config key); Cedar: Alternatives, last row (alternative: `backup::read` at admission) |
| SQLite reader; incremental; Backups without a schedule | `modernc.org/sqlite` (cgo-free); full copies; none | static build (ADR-0211); retention by age (Q8) |

## References

- Report §4.B1, §4.B2, §4.I row 10, §4.J row 10, §4.L rows 1-4, 10, §5; `docs/reports/app-design.md` Decision 12, open
  questions 4, 8; FEAT-0009, FEAT-0010. Code read 2026-10-10: `api/types/v1alpha1/{metadata,app,catalogservice,workflowrun,ids}.go`,
  `internal/controlplane/{handlers,routes_rest}.go`, `pkg/sdk/kinds.go`, `internal/gc/gc.go`, `internal/auth/{routing,rbac/rbac,cedar/schema}.go`,
  `internal/secrets/secrets.go`, `internal/services/catalog/catalog.go`, `pkg/funcd/funcd.go`.
- `modernc.org/sqlite`: v1.60.1 of 2026-09-29 (pkg.go.dev, read 2026-10-10); BSD-3-Clause read in the module cache at
  v1.26.0 (2026-10-10); the newest version's licence and binary size to confirm at implementation.
