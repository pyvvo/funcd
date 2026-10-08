# Platform disaster recovery design

> **Status:** research input for ADRs, not a decision record. Written 2026-10-06 from research gathered on 2026-10-05.
> Three labels: **Sources** is what other systems do, **funcd today** is verified in the code on main (e362eabd to
> 2a327dd2), and **PROPOSAL** is input for the owner's ADRs, never a decision.
> **Feature rows:** [FEAT-0009](../feat/0009-feat-disaster-recovery.md), F109 to F112. **Build order:** the
> [delivery plan](../roadmap/dr-delivery-plan.md), computed from section J.
> **ADRs (2026-10-08):** this report led to ADR-0201 to ADR-0210. Where an ADR differs from the report, the ADR wins.

## Bottom line

- **Two layers is the norm.** A platform backup (control-plane state) and a separate workload backup (the apps' data). Kubernetes keeps etcd snapshots apart from Velero, and Cloud Foundry's BBR leaves service data out. No source offers one mechanism for both.
- **The restore is the hard part, not the backup.** Mature systems start a new timeline on restore, push counters past anything already issued, keep keys outside the snapshot, and hold side effects until an operator verifies.
- **funcd today** has DR for one store only, the KV instance, and its restore function is never called. Two verified hazards: deleted keys can come back after a restore ([#798](https://github.com/pyvvo/funcd/issues/798)), and a restored metastore re-issues resourceVersions that a stale client can then overwrite.
- **PROPOSAL.** A platform backup (metastore, platform records, run state and the event store (dead-letter queue and blob seen lists) as held evidence, keys in a separate escrow set), each store read in one transaction, with an offline restore that starts a new timeline and boots held. A workload backup scoped by namespace, KVStore, Bucket or SQL database that restores into a new name. rqlite is a workload service, so it sits in the second layer.
- **PROPOSAL, order.** Fix #798, settle the backup format, ship metastore backup, restore, the new timeline and the held boot together, then add run state, DLQ, KV, retention, a pre-upgrade snapshot, alerts and drills, then decide the blob target, then the workload backup, then rqlite, and later a `funcdctl` helper that turns RPO and RTO into these settings (G).

## 1. What depends on what

It is a graph, not a strict tree: the metastore, KV and blob are shared by many components. So there is one small tree per component, shared parts are shown once (↗), and each node is tagged so the backup scope can be read off it. Built from the composition root (`pkg/funcd/funcd.go`) and [ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md) for the catalog. The rqlite branch is not in the code; its shape follows the owner's description (2026-10-05) and the KV and Catalog patterns.

```
[S] durable state   [K] key material   [X] outside funcd   ↗ shown once, reused   (planned) not built

SHARED FOUNDATION
  Metastore [S store/] ....... every resource, spec + status, Secret values included (encrypted only if the secrets key [K] is set)
  Cedar PDP .................. reads Policy/Roles/bindings from the metastore; checks every service call below
  Host runtime ............... process or containerd driver, scheduler, activator, ingress gateway
  Function code .............. OCI registry [X], pulled by digest into a cache dir

FUNCTION
  ├─ Function/Revision ........ metastore
  ├─ code ..................... function code ↗
  ├─ run ...................... host runtime ↗ + per-sandbox local API socket
  ├─ bindings ................. KV ↗ · Blob ↗ · Catalog ↗ · Secrets ↗ · other Functions (links)
  └─ logs ..................... blob ↗ (funcd-system bucket)

WORKFLOW
  ├─ Workflow/WorkflowRun ..... metastore
  ├─ run records [S] .......... run-state store (Badger workflow/), its own restore class
  ├─ steps .................... Function ↗ (each step is a Function)
  │   └─ step stores .......... KV ↗ (the workflow's own KVStores)
  ├─ dispatch ................. activator ↗ (host runtime)
  └─ traces ................... blob ↗

KV SERVICE
  ├─ KVStore .................. metastore
  ├─ authz + binding .......... Function.spec.kv · Cedar PDP ↗
  ├─ data [S] ................. KV instance (Badger kv/), key prefix per store/table
  │   └─ also today ........... blob seen lists until they move to the event store (Q2), CDC outbox, backup cursor
  ├─ reached through .......... per-sandbox local API socket
  ├─ optional change feed ..... NATS [S nats/]
  └─ optional DR export ....... any bucket URL

BLOB SERVICE (Buckets, S3 frontend, Sites, logs)
  ├─ Bucket ................... metastore
  ├─ data [S] ................. blob substrate: one shared bucket, a prefix per Bucket
  │                             (local dir today, memory in memory mode, no S3 option yet)
  ├─ authz + binding .......... Function.spec.blob · Cedar PDP ↗
  ├─ S3 gateway (opt-in) ...... keypairs derived from the node master secret [K]; Identity → Secret
  └─ built on it .............. Site, static handler, funclog + compactor, blob EventSource

SECRETS (and Identity)
  ├─ Secret resource [S] ...... metastore, like any resource: spec.data maps env-style names to bytes
  │     at rest: the whole object is AES-256-GCM encrypted with the secrets key [K] when
  │     secrets.encryptionKeyFile is set; plaintext in Badger when it is not (the daemon warns).
  │     The name (namespace/name) is the storage key and is never encrypted.
  ├─ secrets key [K] .......... a 32-byte key file named in the operator config; KMS or OpenBAO drivers
  │                             are meant to replace it later behind the same encryptor seam
  ├─ delivery ................. secrets resolver, authorized by the control-plane RBAC; the Function reconciler
  │     reads the decrypted Secret and merges it into the worker's environment before the worker starts
  │     (env only; tmpfs is V2). Runtime copies: the worker's process env; under containerd the container
  │     spec in containerd's own state dir under the data dir (inference)
  ├─ users .................... Function.spec.secrets · CatalogService.spec.secrets (Quack token, config)
  │                             · Identity → credential Secret → S3 external keys
  └─ not Secrets, but keys .... node master secret [K] (a file; derives S3 + catalog credentials, stores nothing),
                                TLS keys [K] (files under the data dir), control-plane tokens [K] (operator config)

CATALOG SERVICE (DuckLake + DuckDB + Quack)
  ├─ CatalogService ........... metastore
  ├─ engine ................... add-on provider runtime → curated DuckDB image, one replica (host runtime ↗)
  ├─ data + catalog [S] ....... Blob ↗ through the S3 gateway (it must be enabled)
  │     Parquet under the Bucket prefix; catalog.db checkpointed there as one object;
  │     the live SQLite copy is local to the engine and is recovered from blob at start
  ├─ credentials .............. Secrets ↗ (Quack token, config) · per-engine S3 keypair from master secret [K]
  ├─ query access ............. catalog PEP proxy · tokens from master secret [K] · Cedar catalog::query
  └─ opt-in exposure .......... edge routes (route aggregator)

SQL SERVICE ON rqlite (planned; a built-in-style provider, per the owner)
  ├─ default instance ......... one shared cluster for all apps, one database (rqlite serves one per cluster);
  │                             functions and workflows in any namespace can share it
  ├─ dedicated instance ....... on request an app declares its own cluster, as a CatalogService declares its engine
  ├─ declaration .............. metastore (a binding for apps; a new kind for dedicated instances)
  ├─ authz + binding .......... Cedar PDP ↗ · a new binding
  ├─ credentials .............. Secrets ↗ (platform admin credential + per-app credentials; derivation undecided)
  ├─ engine ................... run by the platform (host runtime ↗), never the metastore's cluster
  └─ data [S] ................. each cluster's own data dir

EVENTING
  ├─ EventSource/Sensor ....... metastore
  ├─ timers ................... in-process, no stored state
  ├─ blob events .............. Blob ↗ (list poll) + seen list [S]: in the KV instance today ← mixed; moves to the event store (Q2)
  ├─ delivery ................. Function ↗ via activator, or start a WorkflowRun (Workflow ↗)
  └─ event store [S] .......... today the DLQ (Badger deadletter/); the blob seen lists move in (Q2)
```

| Store | Holds | Depended on by | Backup layer |
|---|---|---|---|
| Metastore | every resource, including Secret values (ciphertext only if the secrets key is set) | everything | platform |
| KV instance | workload KV data and the KV service's own change-feed and backup records; the blob seen lists until they move (Q2) | KV | workload |
| Blob store | Buckets, Sites, logs, catalog Parquet and `catalog.db` | Blob, Catalog, Site, logs, blob events | workload |
| Run state | run records | Workflow | platform, held |
| Event store (today the DLQ) | failed deliveries, and the blob seen lists after the move (Q2) | Eventing | platform, held |
| rqlite (planned) | app SQL data: one shared cluster by default, dedicated clusters on request | SQL service | workload |
| Keys | secrets key, master secret, TLS, config | Secrets, S3, catalog, API | escrow set |
| Registry | function bundles | Function, Workflow | external dependency |
| NATS | KV change feed only | KV CDC | nothing authoritative |

**How to read it for DR**
- **Restore order falls out bottom-up:** keys, metastore, blob, KV and rqlite, then the catalog engine (it recovers `catalog.db` from blob), then Function, Workflow and Eventing held, then release.
- **Where the layers cross today:** the blob seen list sits in the KV instance (it moves to the event store, Q2), and the catalog's durable state lives in blob. The engine writes Parquet before its `catalog.db` checkpoint, so a live copy must go the other way: take `catalog.db` first, then copy the files, then verify that every file the catalog lists is in the copy, or copy from a frozen snapshot (inference; the catalog lists its files in `ducklake_data_file`).
- **Single points:** the metastore (everything), blob (catalog, sites, logs, events) and the master secret (S3 and catalog credentials).
- **Built-in providers versus add-ons:** KV, blob and the default rqlite are one shared instance each, with per-app separation inside, so the backup unit is the instance. Add-ons (the catalog engine, a dedicated rqlite) are declared per app and are each their own unit.

## 2. What other platforms do, in a few lines

| System | Backs up | Point in time | Restore goes to | Keys |
|---|---|---|---|---|
| [etcd and K3s](https://etcd.io/docs/v3.6/op-guide/recovery/) | whole control-plane database | snapshot cadence (K3s: 12-hourly, 5 kept) | a new data dir and cluster ID; a bump-revision option pushes revisions past what clients saw | encryption config and CA keys kept apart; K3s encrypts bootstrap data under the server token |
| [Velero](https://velero.io/docs/main/how-velero-works/) (workloads) | API objects plus volumes, by namespace or label | discrete backups, 30-day TTL | existing objects skipped by default; namespace mapping | no immutable buckets |
| [rqlite](https://rqlite.io/docs/guides/backup/) | SQLite content, full file | none: no incremental, no point-in-time, no pruning | replaces all data in a fresh cluster | no encryption setting; no as-of marker |
| [PostgreSQL](https://www.postgresql.org/docs/current/continuous-archiving.html) | base backup plus WAL | any moment in the archive window | forward replay on a new timeline | per tool (AES-256, GPG, KMS) |
| [Vault, Consul, Nomad](https://developer.hashicorp.com/vault/docs/sysadmin/snapshots/restore) | Raft snapshot | cadence (hourly, 30 kept for the agents) | a fresh cluster | unseal keys never in the snapshot; Nomad's default key sits in cleartext |
| [Cloud Foundry BBR](https://docs.cloudfoundry.org/bbr/bbr-devguide.html) | platform databases and blobstore, not service data | none | destructive overwrite, same topology | a wrong DB key lets a restore appear to succeed |
| [Workflow engines](https://docs.temporal.io/self-hosted-guide/multi-cluster-replication) | history | no procedure for an older copy | failover; progress can roll back and completed activities can re-run | idempotency keys are the defence |

Six patterns recur:
1. Control-plane and workload backups are separate mechanisms with separate owners.
2. A restore must not let clients see the version go back. Two families: bump the counter on the server, or tag the lineage so an old number never matches a new one. [etcd](https://etcd.io/docs/v3.6/op-guide/recovery/) restores with a bumped revision and ends all watches, because Kubernetes controllers assume revisions never decrease ([kubernetes#118501](https://github.com/kubernetes/kubernetes/issues/118501)). [PostgreSQL](https://www.postgresql.org/docs/current/continuous-archiving.html) starts a new timeline at every recovery, and [ZooKeeper](https://zookeeper.apache.org/doc/current/zookeeperInternals.html) builds its transaction id from an epoch and a counter. [Consul](https://developer.hashicorp.com/consul/api-docs/features/blocking) leaves it to the client: an index that goes backwards after a snapshot restore must be reset to 0.
3. Root keys live outside the snapshot, and losing them ends recovery.
4. Moving to an arbitrary moment needs a log; control planes only offer snapshot cadence, so they keep a ladder of snapshots and one before every upgrade.
5. After a restore the old world keeps running, so systems hold side effects until an operator verifies. Most use one switch: [Vault](https://developer.hashicorp.com/vault/docs/sysadmin/snapshots/restore) restores into an isolated cluster and connects it after testing, [PostgreSQL](https://www.postgresql.org/docs/current/runtime-config-wal.html) pauses recovery at the target until the operator resumes, and [Cloud Foundry BBR](https://docs.cloudfoundry.org/bbr/bbr-devguide.html) locks the components before a restore and unlocks them after. [Strimzi](https://strimzi.io/docs/operators/latest/deploying) pauses per resource with an annotation. [Temporal](https://docs.temporal.io/self-hosted-guide/multi-cluster-replication) holds nothing: after a failover progress can roll back and finished activities can run again, so its docs ask for idempotent activities. I found no workflow engine that restarts a run from step 1 on its own.
6. Replication is not a backup: keep immutable copies outside the box, test restores, and alert on backup age ([Google SRE](https://sre.google/sre-book/data-integrity/), [GitLab 2017](https://about.gitlab.com/blog/postmortem-of-database-outage-of-january-31/)).

**Where other systems set the schedule**

| System | Where |
|---|---|
| [K3s](https://docs.k3s.io/cli/etcd-snapshot) | server flags or config file: cron (default 00:00 and 12:00), retention (5 per node), S3 target |
| [rqlite](https://rqlite.io/docs/guides/backup/) | its own JSON file passed to `-auto-backup`: type, interval, timestamp, vacuum, bucket; uploads only when the database changed |
| Consul, Nomad, Vault | snapshot agent config or an API call (Enterprise): interval (hourly) and retain (30; Vault keeps 1) |
| [Velero](https://velero.io/docs/main/how-velero-works/) | a `Schedule` resource: cron, TTL, namespace and label scope, paused flag |
| CloudNativePG | a `ScheduledBackup` resource: cron with seconds, `immediate`, `suspend` |
| Backup for GKE | a `BackupPlan` resource: cron or a target RPO, retention, scope; its status shows last success and RPO risk |
| Cloud Foundry BBR, upstream etcd | no scheduler: the operator runs the tool from an external cron |
| Workflow engines | not in the engine: Temporal Cloud backs up every namespace every 4 hours itself; self-hosted engines leave it to the database |

Daemons and agents keep the schedule in their own config, because it must exist before the system is up. Control planes with workloads keep it in resources.

## 3. funcd today (verified 2026-10-05)

- **Only the KV instance has DR.** It is opt-in ([ADR-0067](../adr/0067-kv-opt-in-dr-backup.md)), and `Restore` has no caller: no command, no boot hook, no runbook. The metastore, run state, dead-letter queue, blob and keys have none.
- **Deleted keys can come back** ([#798](https://github.com/pyvvo/funcd/issues/798), open, needs an ADR). Badger drops a delete marker once compaction reaches it, so an incremental export cannot carry the delete. Heavy writes, a restart or a target outage trigger it (5 of 5, 8 of 10, 5 of 5 in my tests); light traffic is safe; a re-baseline cures it. The idle-segment bug [#790](https://github.com/pyvvo/funcd/issues/790) is already fixed. A fix for #798 is being prepared; the recommended shape is delete records shipped with the incrementals plus a one-time re-baseline.
- **A restored metastore re-issues revisions.** Badger's backup and load restore it faithfully, but the counter goes back, and a client holding a revision from the lost timeline overwrote newer content with no conflict.
- **A non-terminal WorkflowRun with no run record executes again from step 1.** Restoring the metastore alone re-runs steps that already ran.
- **The KV instance also holds platform records** (blob seen lists, change feed, backup cursor). The seen lists move to the event store (Q2).
- **Blob is hard-wired to a local directory.** JetStream is durable only for the opt-in KV change feed.
- **Secret values sit in the metastore** (the whole object AES-256-GCM encrypted when the secrets key file is set, plaintext in Badger otherwise) and reach workers as environment variables. **The keys sit outside the databases and in no backup:** the secrets key, the node master secret, TLS files and the operator config.
- **The KV backup format is thin:** one manifest rewritten with plain puts (no fencing), no version marker, no encryption, only the latest generation kept, failures only logged. ADR-0067 promises low export concurrency, but the code used Badger's default ([#805](https://github.com/pyvvo/funcd/issues/805), fixed in [#809](https://github.com/pyvvo/funcd/pull/809)); a 1M-key export peaks near 1.5 GiB of memory, against 0.6 to 0.7 GiB at one or two goroutines (macOS).
- **The KV backup chain has three more defects, filed with reproducing tests (not rerun here):** a write committed during an export can be missing from every later backup, because each Badger producer opens its own read transaction ([#806](https://github.com/pyvvo/funcd/issues/806), fixed in [#809](https://github.com/pyvvo/funcd/pull/809) with one export producer); the full re-baseline never runs when funcd restarts within its period ([#807](https://github.com/pyvvo/funcd/issues/807)); and a key deleted while backup is off comes back after a restore ([#808](https://github.com/pyvvo/funcd/issues/808)). #807 and #808 are fixed in [#812](https://github.com/pyvvo/funcd/pull/812): each backup segment now records its time (`at`), so the full re-baseline runs on schedule across restarts, and opening the store without backup deletes the backup's cursor, so the next backup re-baselines.
- **The database part of recovery is seconds** (the repo's bench: a 1M-key full backup takes 0.46 s on Linux). Download time and function cold starts are not measured.

## 4. Proposals (all PROPOSAL)

Names are placeholders. Three table shapes, one job each. **Grid** (`Aspect | Proposal | Why`): every proposal opens with the same eight rows, then a second grid with the same columns holds the details. **Compare** (`Option | Fits when | Costs | Pick`) is for alternatives. **Steps** (`# | Step | Why`) is for the order of work. An aspect that does not apply says n/a. Each proposal also has a small flow, a tree of file locations and the YAML config.

### A. Platform backup and restore

| Aspect | Proposal | Why |
|---|---|---|
| Covers | metastore, platform records, run state + event store (DLQ and seen lists, held evidence) | desired state plus history, one cut |
| Where it lives | `funcdconfig.yaml` `backup:`; target `gen/<n>/` | needed before the metastore exists |
| Owner | platform operator | apps never see it |
| Schedule | hourly; ladder 48 h / 30 d / 12 w (placeholders); pins | RPO 1 h; full snapshots are cheap at metastore size |
| Format | full snapshot + manifest; encrypted; immutable | avoids the delete hazard of incrementals |
| Restore | offline, empty data dir, a new random timeline, held boot | a stale client must not overwrite newer data |
| Not covered | keys (escrow set), blob data, workload data | keys never travel with the data |
| Open | Q1: where the numbers come from (config defaults now, the funcdctl helper later) | decided with Q1 |

| Aspect | Proposal | Why |
|---|---|---|
| Port | one backup capability on the metastore port | the engine can change |
| Manifests | numbered, immutable, create-if-absent; needs `IfNotExist` exposed in funcd's blob interface (Go CDK v0.46.0 has it) | no overwrite; every generation is a restore point |
| Lineage | each manifest records the timeline its snapshot was taken on; the restore listing shows the branches | a restore from an older generation leaves the later ones on an abandoned branch |
| Cut | each store is read in one transaction, because Badger's `Stream.Backup` opens one per worker and a commit between their starts can tear the snapshot ([#806](https://github.com/pyvvo/funcd/issues/806) reproduced it for the KV backup, and PR #809 fixed it with one export producer); the three stores are read back to back in the order event store, metastore, run state | the seen list is never ahead of the runs, and a run the metastore holds keeps its record |
| Fencing | startup probe: two concurrent creates, exactly one wins; failure is an error unless `singleWriter: true`; a local dir gets a process lock | fail closed |
| RPO targets | metastore, platform records, run state, DLQ 1 h · KV 30 s · rqlite 15 min · blob provider-side (remote) or hourly mirror (local) | one objective per component, all config keys with defaults |
| Retention | 30-day window: hourly 48, daily 30, weekly 12 (placeholders); pins: pre-upgrade (last 3), newest verified | a bad write surfaces late |
| Encryption | on whenever a target is set; at least 2 recipients; `none` only if explicit; refuse to back up if Secrets would leave in plaintext | secure by default |
| Credential | put and list on the box, no read, no delete; the restore credential is separate, held by the operator | least privilege |
| Versions | restore accepts the same or an older minor, refuses newer; boot migrations as for an upgrade | forward-only migrations |
| Master secret | restore needs the escrowed one; `--new-master-secret` accepts a new one and lists what changes | derived credentials depend on it |
| ConfigMaps, Secrets | inside the metastore snapshot as stored: ConfigMaps plaintext, Secrets ciphertext when the secrets key is set; GitOps re-applies ConfigMaps but not Secret values | they are desired state |
| Key rotation | the manifest records the secrets key fingerprint; escrow keeps every key a retained generation used; restore says which key it needs | old snapshots stay readable |

```
backup (hourly)                          restore (operator, offline)
metastore + platform records             1 escrow keys in; optional read-only list of missing registry digests (Q9)
+ run state / event store (evidence)     2 non-empty data dir? refuse. Decrypt; canary Secret decrypts
   │ full snapshot                       3 load stores; new random timeline in the version
   ▼                                     4 boot HELD; old watchers get "too old, re-list"
encrypt ──▶ external target              5 verify, then release
```
```
/etc/funcd/
  funcdconfig.yaml              # backup: section
  backup-credentials            # write-only
  operator.pub  recovery.pub    # public recipients only
/var/lib/funcd/                 # data dir
  store/ kv/ workflow/ deadletter/ blob/    # deadletter/ is the event store
  s3gateway/master.key          # escrow, never in a backup
escrow/                         # offline or a separate store, never in a backup (placeholder location)
  secrets.key                   # the secrets key in use
  keys-history/                 # every key a retained generation used
s3://funcd-backups/prod/        # external target
  gen/000042/manifest.yaml      # immutable, create-if-absent
  gen/000042/metastore.age
  pins/pre-upgrade-0.7.2        # outlives the ladder
  pins/verified-000041          # newest verified
```
```yaml
# funcdconfig.yaml, read at start; restart to change (values are placeholders)
backup:
  target: s3://funcd-backups/prod
  credentialsFile: /etc/funcd/backup-credentials
  interval: 1h                    # RPO
  singleWriter: false             # true = accept a target without create-if-absent
  objectives:
    rpo: 1h                       # alert when the newest verified backup is older than this
  retention:
    hourly: 48
    daily: 30
    weekly: 12
    preUpgrade: 3
  encryption:
    recipients:
      - /etc/funcd/operator.pub
      - /etc/funcd/recovery.pub   # private half stays offline
```
```yaml
# manifest.yaml
format: 3                         # older => one re-baseline
timeline: 9c41d2e07b3a5f10        # recorded here, defined by the store; shows branches
funcd: 0.7.2                      # newer is refused
secretsKey: 4c91                  # fingerprint of the key that encrypted this snapshot's Secrets
```

### B1. Workload backup

| Aspect | Proposal | Why |
|---|---|---|
| Covers | KVStores and Buckets of a namespace (catalogs: B2); narrow by resource group or name | the app's own data |
| Where it lives | `BackupSchedule` in the metastore; its own external target | apps own their schedule |
| Owner | app owner; namespace RBAC (default-deny) | the namespace is the tenancy unit |
| Schedule | per schedule: cron + `ttl` | app-specific RPO |
| Format | prefix exports, encrypted; KV adds delete records | restore must apply deletes |
| Restore | new name by default; in place needs an admin and an automatic export | no silent overwrite |
| Not covered | platform state (A), the blob store as a whole (D) | other layers |
| Open | consistency across an app's stores and the settings check for `BackupSchedule`: to be discussed later (owner, 2026-10-06); rqlite per-app restore and the `resources` scope come later (Q12, Q14) | out of scope for this report |

| Aspect | Proposal | Why |
|---|---|---|
| Instance rule | shared providers (KV, blob, default rqlite): the unit is the instance, per-app slices by prefix; dedicated instances (catalog engine, dedicated rqlite): their own unit | follows the provider design |
| Consistency | placeholder: one cut per app with the owner functions paused (rqlite has no as-of marker); the real mechanism is to be found later (owner, 2026-10-06); the App design in PR #815 names the App as the unit of this cut (section L) | an app's stores are not cut together otherwise |
| Target | its own external bucket; a target inside the blob store is warned or refused | same failure domain |
| Resources | `BackupSchedule` (definition), `Backup` (run record), `Restore` (request) | the usual split |
| Status | lastSuccess, earliestRestorePoint, rpoRisk; alert when the age exceeds the interval | silent failures last for days |
| Restore source | resolved live keys, not raw segments | works across a rename |
| Platform restore | recreates resources without data; namespaces with a pending data restore stay held | no running-but-empty database |
| ConfigMaps, Secrets | platform layer in v1 (A); per-object restore is operator-run (C); a later optional `resources` scope gives app owners self-service, with backup encryption | desired state, not data |
| KV fix (#798) | delete records `\x00del/<full key>` (the export streams the data prefix + the record prefix); manifest `format: 2` is the migration hook; restore applies records by version; restore to a segment boundary | deletes must survive compaction |
| Seen lists | the blob seen lists move into the event store, the DLQ store under its new name (decided, Q2); a separate ADR (H) | KV holds workload data only |
| rqlite | whole-instance restore in v1; one app out of the shared database = an SQL import of its tables, later, with a table naming convention | isolation first |

```
BackupSchedule ──▶ pause owners ──▶ export prefixes ──▶ target ──▶ Backup (run record) ──▶ status
                                    KV data + delete records · bucket prefix
restore request ──▶ new name ──▶ app owner swaps

full recovery order:  keys → platform → per-scope data → verification → release
```
```
s3://orders-backups/nightly/        # app-owned target, outside the blob store (placeholder layout)
  2026-10-06T03:00/
    manifest.yaml
    kv/orders-state.age             # data prefix + delete records
    blob/orders-files.age
```
```yaml
apiVersion: funcd.io/v1alpha1
kind: BackupSchedule
metadata:
  name: orders-nightly
  namespace: orders             # the permission boundary
  resourceGroup: orders
spec:
  schedule: "0 3 * * *"         # syntax and time zone from the Cron ADR (X1)
  ttl: 720h                     # retention for this schedule
  scope:
    kvStores:
      - orders-state
    buckets:
      - orders-files
  target:
    url: s3://orders-backups/nightly
    credentialsSecret: orders-backup-credentials
  paused: false
status:                         # written by the platform
  lastSuccessTime: "2026-10-06T03:00:41.000Z"
  earliestRestorePoint: "2026-09-06T03:00:41.000Z"
  rpoRisk: false                # true when the age exceeds the interval
```

### B2. Catalog service

The catalog's durable state is already in blob, so the blob backup covers it. Only the copy order needs care, and restore needs no special service.

| Aspect | Proposal | Why |
|---|---|---|
| Covers | CatalogService, `catalog.db`, Parquet files | all durable catalog state |
| Where it lives | resource in the metastore; `<prefix>/_ducklake/catalog.db` + files in blob | the engine recovers from blob |
| Owner | app owner (`catalogs` scope) | namespace-scoped |
| Schedule | nightly | a lake changes slowly |
| Format | object copy: `catalog.db` first, copy files, verify, manifest last | consistent cut without staging |
| Restore | copy objects back; the engine recovers `catalog.db` at start | no special service |
| Not covered | live SQLite copy (ephemeral), engine keypair (re-derived) | rebuilt at start |
| Open | does DuckLake cleanup run automatically? | the docs are silent |

| Aspect | Proposal | Why |
|---|---|---|
| CatalogService resource | platform backup | metastore |
| Quack token and config | platform backup (ciphertext) | Secrets, in the metastore |
| Engine S3 keypair | nothing: re-derived; the master secret is in the escrow set | derived from the master secret |
| `catalog.db` checkpoint | blob backup, taken first | blob, `<prefix>/_ducklake/catalog.db` |
| Parquet files | blob backup, incremental | blob, under the Bucket prefix |
| Live SQLite copy | nothing: recovered from blob at start | the engine's ephemeral sandbox |

DuckLake facts: the catalog lists every data file in `ducklake_data_file`, with a path that can be relative to the table path, so the verify step must resolve relative paths. Files are physically removed only by explicit cleanup functions. The docs do not say whether data files are immutable or whether cleanup runs automatically. If a cleanup deletes a file the snapshot lists mid-backup, the verify step catches it and the run starts over. The engine writes Parquet before `catalog.db`, so a live copy goes the other way: a newer checkpoint could name a file the copy missed.

```
backup (catalog scope)                       restore
1 read catalog.db -> C1 (kept even if        1 blob restore: copy objects back to the same keys
  a newer checkpoint overwrites the key)     2 platform restore recreates the CatalogService
2 copy the prefix's objects, skipping        3 engine starts -> GetObject catalog.db -> ATTACH
  ones already in the target                    (no special service)
3 verify: every file C1 lists is in the copy; if one is missing, start over
4 write catalog.db (C1), then the manifest, last
```
```
blob: Bucket lake, prefix gold/
  gold/_ducklake/catalog.db      # SQLite checkpoint, atomic whole-object Put
  gold/<...>.parquet             # files the catalog lists (ducklake_data_file)
s3://analytics-backups/lake/
  2026-10-06T03:00/
    gold/<...>.parquet           # copied second, incremental
    gold/_ducklake/catalog.db    # C1, written second to last
    manifest.yaml                # every key + sha256, written last
```
```yaml
apiVersion: funcd.io/v1alpha1
kind: BackupSchedule
metadata:
  name: lake-nightly
  namespace: analytics
  resourceGroup: analytics
spec:
  schedule: "0 3 * * *"
  scope:
    catalogs:
      - lake                     # placeholder scope: catalog.db first, copy, verify
  target:
    url: s3://analytics-backups/lake
    credentialsSecret: analytics-backup-credentials
```

### C. Recovery after a bug-induced crash

| Aspect | Proposal | Why |
|---|---|---|
| Covers | restore after a crash or bad release, to a point before the bad writes | the third requirement |
| Where it lives | `.hold` marker in the data dir; `recovery:` keys (placeholders) | survives restarts |
| Owner | platform operator | restore is operator-run |
| Schedule | on demand; drills on a schedule; a snapshot before every upgrade | rehearsed restores work |
| Format | a restore report and a generation marker | shows what was lost |
| Restore | inspect read-only, restore offline, held boot, verify, release | side effects stay off until verified |
| Not covered | app-level mistakes (B1); one object has its own row below | different tools |
| Open | hold switch details, safe-mode threshold (Q4) | tested before relying on it |

| Aspect | Proposal | Why |
|---|---|---|
| Restore point | latest, time, resourceVersion or named marker; read-only inspect and diff first | choose before acting |
| Pre-upgrade snapshot | automatic, by the old binary; last 3 kept; rollback = old binary + snapshot | forward-only migrations |
| Hold | one platform-wide hold in the daemon, at the side-effect runners, lifted by an explicit release; not built on the WorkflowRun pause flag (untested for a run with no record) | restores desired state faithfully |
| Held boot, OFF | timers, sensors, event sources, workflow runners, DLQ delivery, namespaces with a pending data restore | no re-fired side effects |
| Non-terminal runs | restored as evidence, paused; step idempotency keys from run ID + step ID, not an ID regenerated on restart | no re-execution from step 1 |
| DLQ | restored held, keyed by the original event ID, no automatic redelivery | handled items stay handled |
| Blob events | replay by default; "advance to now" per source at release | at-least-once beats silent loss |
| Safe mode | after N crashes boot the last good generation held | breaks crash loops |
| Single object | inspect any generation read-only (Secret values redacted), then apply the chosen ConfigMaps or Secrets through the API (new uid and resourceVersion); operator-run in v1; Secrets need the escrowed key and an explicit flag | whole-instance restore is too heavy for one object |
| Drills | scratch dir, outbound effects off, integrity check, counts, smoke test, timed; alert when the newest verified backup is older than the objective | untested backups fail |

```
crash / bad release
  │ after N crashes: safe mode boots the last good generation HELD
  ▼
inspect (read-only) ──▶ restore offline ──▶ new timeline
  ▼
HELD BOOT: API up, resources readable, side effects OFF
  ▼
verify ──▶ release (blob events per source: replay | advance)
```
```
single object (operator, v1)
inspect a generation (read-only, Secret values redacted) ──▶ pick the objects
  ──▶ decrypt Secrets with the escrowed key ──▶ apply through the API
  ──▶ new uid + resourceVersion ──▶ the live key re-encrypts ──▶ workers read it at their next start
```
```
/var/lib/funcd/
  .hold                         # written by restore, release deletes it (placeholder)
```
```yaml
recovery:                       # placeholders
  crashLoopThreshold: 3         # then boot the last good generation held
  releaseBlobEvents: replay     # replay | advance
```

### D. Blob target

| Aspect | Proposal | Why |
|---|---|---|
| Covers | the whole blob store: all Buckets, Sites, logs, catalog data | one substrate shared by every app |
| Where it lives | `blob:` in `funcdconfig.yaml` | needed before the metastore exists |
| Owner | platform operator | whole-store protection |
| Schedule | remote: provider-side; local: hourly mirror | RPO about 0 remote, 1 h local |
| Format | remote: versioning + Object Lock; local: mirror from a frozen image, manifest last | funcd needs no lock API |
| Restore | whole instance, by the operator | disaster recovery |
| Not covered | per-app slices (B1) | app owners restore their own |
| Open | verify versioning and Object Lock at startup, refuse or warn if missing | fail closed or not |

| Aspect | Proposal | Why |
|---|---|---|
| Stores | S3-compatible stores (AWS S3, Ceph, SeaweedFS, R2 and the like) and the local file system, each through the startup probe; MinIO as a dependency excluded (archived, AGPL) | the owner's choice (Q8); create-if-absent support |
| Local dir | allowed, not an independent copy | same disk, same failure domain |
| Catalog prefix | take `catalog.db` first, copy the files, verify every listed file is in the copy; manifest last (B2) | no dangling references |
| Guard | a `BackupSchedule` target inside the same blob store is warned or refused | same failure domain |

| Option | Fits when | Costs | Pick |
|---|---|---|---|
| Remote target | a provider bucket with versioning and Object Lock | a configuration surface; per-provider quirks | first |
| Local files + mirror | local-file mode | funcd mirror code; a frozen image; not an independent copy | fallback |
| `blob:` instance backup | the operator wants whole-store recovery after losing the box, disk or provider | operator config; whole instance only | yes |
| `BackupSchedule` scope | app owners want their own restore points, per-app rollback and an independent copy | per-app schedules and targets | yes, alongside it |

```
plain buckets:   frozen image ──▶ copy objects ──▶ write manifest last
catalog prefix:  read catalog.db ──▶ copy objects ──▶ verify listed files ──▶ write catalog.db + manifest last
```
```
/var/lib/funcd/blob/            # local mode
s3://funcd-backups/blob/        # mirror target
  manifest.yaml                 # written last
```
```yaml
blob:                           # remote: protection is provider-side
  target: s3://funcd-data/prod
  credentialsFile: /etc/funcd/blob-credentials
```
```yaml
blob:                           # local: funcd mirrors; target, encryption, retention from backup:
  dir: /var/lib/funcd/blob
  backup:
    interval: 1h
```

### E. KV and rqlite providers

| Aspect | Proposal | Why |
|---|---|---|
| Covers | the KV instance and the default rqlite cluster | built-in providers: one shared instance each |
| Where it lives | `kvstore.backup` and `sql.backup` in `funcdconfig.yaml` | next to each provider's settings |
| Owner | platform operator | instance-level |
| Schedule | KV 30 s + re-baseline 24 h; rqlite 15 min | KV exists today |
| Format | KV: incrementals + delete records; rqlite: a full file pulled over HTTP | rqlite has no incremental |
| Restore | the whole instance; per-app slices come from B1 | the instance is the unit |
| Not covered | per-app SQL restore (later) | needs a table ownership convention |
| Open | pull or forward; rqlite isolation (Q12) | decided in the rqlite ADR |

| Aspect | Proposal | Why |
|---|---|---|
| Settings apply | platform and provider backup: restart; `BackupSchedule`: live | the operator config is read once |
| Rule | what must exist before the metastore lives in the daemon config; what apps own is a resource | K3s cannot read its S3 Secret while the API is down |
| Credentials | files referenced from the config, not Secrets | needed during a restore |
| KV inheritance | target, credentials and encryption inherited from `backup:` unless set in `kvstore.backup` | one place for the target |

| Option | Fits when | Costs | Pick |
|---|---|---|---|
| pull | one scheme for every store | funcd needs its own change check | yes |
| forward | least code; rqlite uploads only when the database changed, only from the leader | no client-side encryption (`encryption` is refused); no pruning (a bucket lifecycle rule must prune; `timestamp: true` writes one object per upload); no funcd manifest or backup age | no |

```yaml
kvstore:                          # exists today
  engine: badger
  backup:
    enabled: true
    target: s3://funcd-backups/kv
    interval: 30s
    rebaseline: 24h
    chunkBytes: 67108864
# proposed: target, credentials and encryption inherited from backup: unless set here
```
```yaml
sql:
  engine: rqlite
  backup:
    mode: pull                    # funcd pulls: one scheme, encryption and manifests
    interval: 15m
```
```yaml
sql:
  engine: rqlite
  backup:
    mode: forward                 # funcd renders rqlite's -auto-backup file; encryption refused
    interval: 15m
    target: s3://funcd-backups/sql
    credentialsFile: /etc/funcd/backup-credentials
```
```json
{
  "version": 1,
  "type": "s3",
  "interval": "15m",
  "timestamp": true,
  "sub": {
    "access_key_id": "$BACKUP_ACCESS_KEY_ID",
    "secret_access_key": "$BACKUP_SECRET_ACCESS_KEY",
    "region": "$BACKUP_REGION",
    "bucket": "funcd-backups",
    "path": "sql/db.sqlite3.gz"
  }
}
```

### F. Order of work

| # | Step | Why |
|---|---|---|
| 1 | #798 fix (P1): ADR-0195 is accepted, the implementation follows | the only DR code in use |
| 2 | The ADRs of section I: 1 to 6 first (P1 is ADR-0195, accepted) | the first release needs these six; 7 to 10 follow at steps 5 and 6 |
| 3 | Metastore backup, offline restore, conformance test with deletes, first drill | proves restore |
| 4 | Held boot + run pause, same release | else a restore re-runs runs and re-fires events |
| 5 | Run state, event store (DLQ and seen lists), retention, pre-upgrade snapshot, status, metrics, backup-age alert | completes the platform layer |
| 6 | Blob target, then workload backup (KVStore first), then the SQL service | needs the target decision first |
| 7 | Later: the `funcdctl backup plan` helper (G); a first cut can use the `kvstore.backup` keys that exist today | turns objectives into settings; the drill from step 3 measures the RTO |

### G. funcdctl backup helper (later)

Objectives (RPO, RTO, how long to keep) are the architect's or data admin's input, outside funcd's config. The helper turns them into the settings of A, B1, B2 and E, and checks them. Names are placeholders and nothing is built.

| Aspect | Proposal | Why |
|---|---|---|
| Covers | RPO, RTO and keep per component in, backup settings out | the objectives live outside the config |
| Where it lives | `funcdctl backup plan`; needs no server | no `backup` verb exists today; like `types` and the artifact verbs |
| Owner | platform operator (platform, providers); app owner (`BackupSchedule`) | follows who owns the setting |
| Schedule | n/a: on demand | a planning aid |
| Format | prints YAML (default); `--write` edits the local config file; a `BackupSchedule` goes through `funcdctl apply` | proposing is safe, writing is opt-in |
| Restore | n/a: it plans backups, never restores | restore is operator-run (C) |
| Not covered | proving an RTO is met; changing a running daemon | only a restore drill proves an RTO; the config is read at start |
| Open | verb name, flags or a plan file, a `drill` verb | decided when it is scoped |

| Aspect | Proposal | Why |
|---|---|---|
| Input | RPO, RTO, keep, data size, link speed, per component | what the rules need |
| Output | config keys, or a `BackupSchedule` manifest | the two places settings live (E) |
| RPO rule | `interval` = RPO ÷ 2; `objectives.rpo` = RPO | one failed run still meets it; [ADR-0067](../adr/0067-kv-opt-in-dr-backup.md) says RPO = interval for the KV export |
| Keep rule | `keep` sets the retention ladder | one input, no arithmetic by hand |
| RTO rule | lower bound only: size ÷ speed; a target below it is impossible and the helper names the knobs (a closer target, a smaller scope) | load and boot time are known only from a drill |
| Safety | checks the result with the daemon's config loader and the validation rules the daemon runs at start, shows a diff, never writes credentials | a bad config must not stop the boot |
| First cut | only keys that exist today: `kvstore.backup.interval` and `rebaseline` | useful before the platform backup exists |

```
RPO, RTO, keep ───┐
size, link speed ─┴─▶ funcdctl backup plan ─┬─▶ print YAML
                                            ├─▶ edit funcdconfig.yaml (--write, then restart)
                                            └─▶ BackupSchedule ─▶ funcdctl apply
```
```
funcdconfig.yaml                # --write edits it in place; restart to apply
orders-backup.yaml              # a printed BackupSchedule (placeholder name), then funcdctl apply -f
```
```
$ funcdctl backup plan platform --rpo 1h --rto 4h --keep 30d --size 2GiB --speed 50MB/s
```
```yaml
backup:
  interval: 30m                 # RPO 1h ÷ 2
  objectives:
    rpo: 1h                     # alert when the newest verified backup is older than this
  retention:
    daily: 30                   # from --keep 30d
# RTO 4h: the download takes 43 s, so the target is possible
```
```
$ funcdctl backup plan kv --rpo 1m --rto 15m --size 200GiB --speed 100MB/s
RTO 15m is impossible: the download alone takes 36 min.
Knobs: a closer target, or a smaller scope.
```

### H. Event store (separate ADR)

The DLQ store becomes the event store: the one durable home for what eventing must remember across a restart. A separate ADR defines it and folds the eventing decisions into it. Names are placeholders and nothing is built.

| Aspect | Proposal | Why |
|---|---|---|
| Covers | the dead-letter queue and the blob seen lists; the rule for any other state eventing must remember | one store for one service |
| Where it lives | the DLQ Badger instance, renamed in the ADR; on disk by default, in memory with `storage.mode: memory` | already wired and always on |
| Owner | the eventing service | one instance per service |
| Schedule | with the platform backup | same cut as run state |
| Format | records by key prefix: `dl/` for dead letters, a new prefix for seen lists | the TTL and max-entries sweeps touch only `dl/` |
| Restore | with the platform backup, held; the seen list drives replay at release (Q5) | restore class: held evidence |
| Not covered | the KV change-feed outbox and cursors (the KV service's); definitions and Invocation records (metastore) | not eventing's durable state |
| Open | the final name and directory; the migration of existing seen lists; how it supersedes ADR-0119 and ADR-0157; which feat row it realizes; whether in-flight Sensor firings and the retry queue become durable here | decided in the ADR |

| Aspect | Proposal | Why |
|---|---|---|
| Durability | follows the engine: memory is ephemeral by design; a file-based or remote engine must keep its state across a restart | a restart is not a restore |
| Backup | what funcd holds on disk; whose backup covers a remote engine is open (no remote engine exists today) | the platform backup copies funcd's own files |
| Folds ADR-0118 | the DLQ becomes the first tenant of the event store | its Badger store already mirrors the run-state driver |
| Folds ADR-0119, ADR-0157 | the seen list moves from the KV substrate into the event store | both are Implemented, so a new ADR supersedes the placement decision |
| Folds this report | Q2 (seen list moves), Q3 (DLQ held evidence), Q5 (replay at release uses the seen list) | the ADR carries the decided answers |
| Rule | whatever eventing must remember across a restart lives here | prevents the next mixed store |

```
today:  BlobWatcher ──▶ seen list in the KV instance ◀── KV backup (opt-in, its own cut)
after:  BlobWatcher ──▶ seen list in the event store ◀── platform backup (same cut as runs and DLQ)
```
```
/var/lib/funcd/
  deadletter/                   # the event store (directory name is a placeholder)
  kv/                           # workload data only after the move
```
Config: nothing new here. The DLQ keys (`eventing.deadletter.dataDir`, `retention`, `maxEntries`) stay until the ADR renames them.

### I. ADRs needed

One topic each, merged only where the topics change together. The first release needs 1 to 6; 7 to 10 follow at steps 5 and 6. P1 is ADR-0195, accepted on 2026-10-07, and W5, L1 and L2 are later or on another track. The draft order and the proposed feat rows are in section K. A number is taken when an ADR is drafted (main ended at ADR-0198 on 2026-10-07). FEAT-0009 holds the feature rows (F109 to F112), and each ADR needs its `Realizes:` row.

| # | Step | Why |
|---|---|---|
| 1 | ADR: store layer for backup (was P2 + P6): a one-transaction snapshot with one producer, the cut order event store, metastore, run state, and the version timeline `<timeline>-<n>` | the Port, Cut and timeline rows of A; Q6; #806 as evidence; touches ADR-0006, ADR-0065, ADR-0094 |
| 2 | ADR: backup format, targets and fencing (was P3): numbered immutable manifests, `IfNotExist` in the blob port, startup probe, `singleWriter`, retention ladder, put-and-list credential, timeline field | Q8 and the format part of Q10; touches ADR-0007 |
| 3 | ADR: backup encryption and key escrow (was P5): recipients, `none`, refusal of plaintext Secrets, escrow set, key history, master secret on restore | Q7 |
| 4 | ADR: backup operation (was P4): the objectives key, validation across keys at start, status, metrics, backup-age alert; each ADR defines the keys of its own behavior | Q1 and Q13 |
| 5 | ADR: restore and held boot (was P7 + P8): offline restore command, restore points, version rule, single-object restore; one platform-wide hold, the runners it stops, runs and DLQ held as evidence, blob replay or advance at release; registry out of scope; the conformance test and the first drill | Q3, Q4, Q5, Q9, Q10, Q14; the hold also stops the App reconciler, App hooks and App rollouts in progress (section L); the biggest merge, so split it if it passes 250 lines; touches ADR-0094, ADR-0108, ADR-0109, ADR-0118, ADR-0119, ADR-0157 |
| 6 | ADR: event store (H) (was P9) | Q2; supersedes the placement in ADR-0119 and ADR-0157, builds on ADR-0118 |
| 7 | ADR: pre-upgrade snapshot and safe mode (was P10) | C; the bug-induced crash requirement |
| 8 | ADR: blob store backend and backup target (was W1): S3-compatible or local, remote protection or a local mirror, same-failure-domain guard | D and Q8; touches ADR-0007 |
| 9 | ADR: KV backup on the common format (was W3): encryption, retention, fencing, target inheritance, per-store export | E and B1; supersedes what P1 leaves of ADR-0067 |
| 10 | ADR: workload backup resources and catalog scope (was W2 + W4): `BackupSchedule`, `Backup`, `Restore`, scopes including `catalogs` and its copy order, targets, status, namespace permissions, restore into a new name | B1, B2, Q11, Q13; the App scope, `Backup` records that outlive the App and a backup API for hooks (section L); needs the Cron ADR; app-level consistency deferred by the owner; touches ADR-0086 |
| P1 | ADR-0195: KV backup delete records (#798) | step 1; Accepted on 2026-10-07 and its card is In Progress; supersedes clauses of ADR-0067 and records #807 and #808, fixed in PR #812; #805 and #806 were fixed in PR #809 with no ADR; it leaves the production Restore entry point and the metastore to this plan |
| W5 | ADR: SQL service on rqlite, backup and restore parts (pull mode, whole-instance restore, isolation and table ownership) | E and Q12; belongs to the rqlite service ADRs; touches ADR-0082, ADR-0087 |
| L1 | ADR: `funcdctl backup plan` helper | G; Backlog card filed |
| L2 | ADR, optional and not decided: step idempotency keys from run ID and step ID | C; touches ADR-0094, ADR-0146 |

Drafting order: wave 1 is 1 and 6 (P1 is done: ADR-0195), then 2 and 3, then 4 and 5, then 7 to 10. 5 needs 1, 2, 3 and 6. App-level consistency, deferred by the owner, adds one ADR when it is discussed, or folds into 10.

### J. Dependencies

Fixes first, then the ADRs of section I. A dependency means the other item is decided or landed first; a fix row never blocks an ADR draft, but the ADR records its behavior.

| # | ADR or fix | Carries, and existing ADRs it touches | Depends on |
|---|---|---|---|
| #805 | fix, done | the KV export ran at Badger's default concurrency, not the low one ADR-0067 promises; fixed in PR #809 | none |
| #806 | fix, done | a write committed during an export could be missing from every later backup; fixed in PR #809 with one export producer; evidence for 1 | none |
| #807 | fix, done | each backup segment records its time, so the full re-baseline runs on schedule across restarts; fixed in PR #812; changes ADR-0067's re-baseline rule | none; P1 records it |
| #808 | fix, done | opening the store without backup deletes the cursor, so the next backup re-baselines; fixed in PR #812; changes ADR-0067 | none; P1 records it |
| P1 (#798) | ADR-0195, accepted 2026-10-07: KV backup delete records | option A, the one-time re-baseline, the manifest `format`; supersedes clauses of ADR-0067; its card is In Progress | #807 and #808, whose behavior it records |
| 1 | ADR: store layer for backup (was P2 + P6) | one-transaction snapshot, cut order, version timeline `<timeline>-<n>`; Q6; ADR-0006, ADR-0065, ADR-0094 | none |
| 2 | ADR: backup format, targets and fencing (was P3) | manifests, `IfNotExist` in the blob port, startup probe, `singleWriter`, retention, put-and-list credential, timeline field; Q8; ADR-0007 | 1 |
| 3 | ADR: backup encryption and key escrow (was P5) | recipients, `none`, plaintext refusal, escrow set, key history; Q7 | 2 |
| 4 | ADR: backup operation (was P4) | the objectives key, validation across keys at start, status, alerts; each ADR defines its own keys; Q1, Q13 | 2, 3 |
| 5 | ADR: restore and held boot (was P7 + P8) | offline restore, restore points, version rule, one hold, the first drill, runs and DLQ held, blob replay or advance; Q3, Q4, Q5, Q9, Q10, Q14; the App reconciler, hooks and rollouts held (L); ADR-0094, ADR-0108, ADR-0109, ADR-0118, ADR-0119, ADR-0157 | 1, 2, 3, 6; P1 for the KV part |
| 6 | ADR: event store (was P9) | Q2; supersedes the placement in ADR-0119 and ADR-0157, builds on ADR-0118 | none |
| 7 | ADR: pre-upgrade snapshot and safe mode (was P10) | automatic snapshot, last 3 kept, crash-loop boot held | 2, 5 |
| 8 | ADR: blob store backend and backup target (was W1) | S3-compatible or local, remote protection or a mirror; D, Q8; ADR-0007 | 2 |
| 9 | ADR: KV backup on the common format (was W3) | encryption, retention, fencing, inheritance, per-store export; E, B1; supersedes what P1 leaves of ADR-0067 | P1, 2, 3 |
| 10 | ADR: workload backup resources and catalog scope (was W2 + W4) | `BackupSchedule`, `Backup`, `Restore`, scopes including `catalogs`, targets, status, permissions; B1, B2, Q11, Q13; the App scope, Backups outliving the App, a backup API for hooks (L); ADR-0086 | 2, 3, 8, 9, X1 |
| W5 | ADR: SQL service on rqlite, backup and restore parts | pull mode, whole-instance restore, isolation; E, Q12; ADR-0082, ADR-0087 | the rqlite service ADRs, 2, 10 |
| L1 | ADR: `funcdctl backup plan` | G; reuses the validation of 4 | 4; 10 for the `BackupSchedule` output |
| L2 | ADR, optional and not decided: step idempotency keys | C; ADR-0094, ADR-0146 | none |
| X1 | other initiative: the Cron ADR | one cron implementation for EventSource timers and `BackupSchedule`, UTC by default; from the App design (PR #815) | none; 10 depends on it |

```
fixes   #805 and #806 done (PR #809) | #807 and #808 done (PR #812)
done    P1 = ADR-0195, accepted 2026-10-07 (records #807 and #808)
wave 1  1 Store layer | 6 Event store
wave 2  2 Format and fencing (1) | 3 Encryption and escrow (2)
wave 3  4 Operation (2, 3) | 5 Restore and held boot (1, 2, 3, 6)      the first release is 1 to 6
wave 4  7 Pre-upgrade and safe mode (2, 5) | 8 Blob backend (2) | 9 KV on the common format (P1, 2, 3) | 10 Workload resources (2, 3, 8, 9, X1)
later   W5 (the rqlite ADRs, 2, 10) | L1 helper (4, 10) | L2 idempotency keys
other   X1 Cron ADR (App initiative), needed by 10
```

### K. Draft order and feat rows

FEAT-0009 ([docs/feat/0009-feat-disaster-recovery.md](../feat/0009-feat-disaster-recovery.md)) holds the feature rows
F109 to F112, all at idea, and the [delivery plan](../roadmap/dr-delivery-plan.md) computes the build order from section
J. No ADR is registered yet. A number is taken when an ADR is drafted. Main ended at ADR-0198 on 2026-10-07, and five
numbers (0194 to 0198) went to other work in one day, so no number is reserved here; the table gives the draft order.
The highest feature code before this plan was F123 (FEAT-0010, Apps), which left FEAT-0009 and F109 to F112 free on
purpose. FEAT-0002 is reserved for V2 hardening.

| # | ADR | Draft order | Realizes |
|---|---|---|---|
| P1 (#798) | KV backup delete records | done: ADR-0195, accepted 2026-10-07 | FEAT-0001/F36 |
| 6 | Event store | 1st | FEAT-0009/F110 |
| 1 | Store layer for backup | 2nd | FEAT-0009/F109 |
| 2 | Backup format, targets and fencing | 3rd | FEAT-0009/F109 |
| 3 | Backup encryption and key escrow | 4th | FEAT-0009/F109 |
| 4 | Backup operation | 5th | FEAT-0009/F109 |
| 5 | Restore and held boot | 6th | FEAT-0009/F109 |
| 7 | Pre-upgrade snapshot and safe mode | 7th | FEAT-0009/F109 |
| 8 | Blob store backend and backup target | 8th | FEAT-0009/F111 |
| 9 | KV backup on the common format | 9th | FEAT-0009/F111 |
| 10 | Workload backup resources and catalog scope | 10th, after X1 | FEAT-0009/F111 |
| X1 | the Cron ADR (Apps epoch) | before 10 | set by that ADR |
| W5, L1, L2 | rqlite ADRs, funcdctl helper, idempotency keys | not scheduled | W5 in the rqlite feature, L1 FEAT-0009/F112, L2 in FEAT-0005 |

### L. Impact of the App design (PR #815)

PR #815 is merged (64bd360b) and added FEAT-0010, Apps (F113 to F123, all at idea, no ADR) and its design note. It handed several items to this plan, and the follow-up PR #831 (merged 2026-10-07) records them in the App design. Nothing below changes a decision of section 5; it adds inputs and one dependency.

| # | The App design says | Impact on the DR plan | ADR |
|---|---|---|---|
| 1 | An App gets a `backupSchedules` section, and a `BackupSchedule` can take an App as its scope: its KV stores, Buckets and catalogs, "the unit of the deferred one cut per app" | ADR 10 defines the App scope. The App is the candidate carrier for the app-level consistency that the owner deferred; PR #831 keeps the section and the scope as a placeholder for ADR 10 | 10 |
| 2 | `Backup` records outlive their schedule and the App, until their `ttl` or a manual delete | a `Backup` has its own `ttl` and no owner cascade from the schedule or the App | 10 |
| 3 | A pre-apply hook can take "a backup before an upgrade", and a platform client in a hook's context (trigger a Backup, pause a Sensor) waits for "the DR backup API" | ADR 10 exposes an API a Function can call to create a `Backup`, with namespace permissions (Q11); PR #831 records that a hook needs a platform client to call it | 10 |
| 4 | Cron: one implementation for EventSource timers and `BackupSchedule`, UTC by default, in its own ADR | a new ADR outside this plan; the `schedule` syntax of ADR 10 depends on it (X1 in section J); PR #831 records that the Cron ADR is decided first | 10 |
| 5 | Drift correction: the App reconciler writes, prunes and restores parts at once; a paused App writes nothing and runs no hook | the hold of ADR 5 stops the App reconciler and the hooks of every App, without editing each `spec.paused` | 5 |
| 6 | A rollout is durable: the AppRevision records how it went, a failed hook is retried with `funcdctl app retry`, and a hook must be safe to repeat | a restored App can be mid-rollout, so ADR 5 treats it like a run: held as evidence, continued by the operator, never by itself | 5 |
| 7 | Secrets: the App declares them and never creates, writes or restores one; the values are managed by the platform | no conflict: Secrets stay in the metastore snapshot, under the escrow rules and the operator-run restore (Q14) | 3, 5 |
| 8 | "The definition rides the metastore backup"; AppRevisions are immutable resources, `app.revisionHistory` defaults to 10 | nothing new to back up; the snapshot holds up to 11 copies of each App spec, a few KB each | 1 |
| 9 | Templates are rendered on the client and pushed to a registry; the server never pulls a template; images are pinned by digest in `app.lock` | no new registry dependency at restore beyond the image digests of Q9 | 5 |
| 10 | "Restore" meant self-heal in the App design (`status.lastRestore`, as a Site restores its Route) | resolved in PR #831: the App says self-heal (`status.lastSelfHeal`), so "restore" is the DR word; grep the vocabulary again when ADR 10 is drafted | 10 |
| 11 | FEAT-0010 takes F113 to F123 and leaves FEAT-0009 and F109 to F112 free for DR; no ADR number is claimed | no clash in feature codes; ADR numbers come from one pool, and main already holds ADR-0194 to 0198 from other work (section K) | K |
| 12 | F123, the dry-run engine: a write checked by admission without being stored | a possible restore check: run the restored objects through admission under the new binary before the release (an idea, not a decision) | 5 |

To swap a restored store into an App, the owner changes the App to `ref` the restored store. That is an App upgrade, so it is recorded as a new AppRevision. Hooks have only `preApply` and `postApply` today, with no point for a backup or a restore; the deferred app-level consistency would add one, in the App's F117.

## 5. Open questions, with a recommended answer for each

Recommended answers are my choices from the platform's philosophy (fail closed, opt-in and default off, least privilege, restore classes, one instance per service, numbers as config keys with defaults) and from the choices made so far. The owner decides; **Decided** marks the owner's answer (2026-10-06).

1. **Which recovery point and recovery time apply to each component, and how long can a bad write go unnoticed?** **Decided (owner, 2026-10-06): the daemon enforces consistency for the platform-wide backup, in a transaction; the same guarantee for app-level backups is to be found later.** The ADRs do not fix the numbers. The objectives are the owner's plan, outside funcd. Every number is a config key with a documented default; the first defaults are the ones below, and they are placeholders. The platform backup is opt-in like the KV one: once it is on, `target` and the encryption recipients are required. The daemon validates the settings at start, with the same loader that `funcdctl backup plan` (G) reuses later: an impossible combination is an error and the daemon does not start, as for the KV backup today (for example `objectives.rpo` smaller than `interval`); a tight one is a warning (for example `interval` above half of the RPO). The helper turns objectives into settings later and adds no rule the daemon does not already check. First defaults: metastore, platform records, run state and event store hourly; KV 30 s (today's interval); rqlite 15 min; blob provider-side when remote, an hourly mirror when local. Retention hourly 48, daily 30, weekly 12. RTO is not a config key: aim for one hour from a working host plus the escrow set, and publish it only after the first drill measures it. Why: opt-in and default off, fail closed, one place for the rules, and the helper is step 7 while the metastore backup ships at step 3, so the first release needs defaults. On the data side each store is read in one transaction and the stores in a fixed order (section 4.A, Cut).
2. **Do the blob seen lists move out of the KV instance (B1)?** **Decided (owner, 2026-10-06): yes.** A seen list (the `Watermark` in the code) records, per EventSource event, which objects already fired. The DLQ store becomes the event store, the seen lists move into it, and everything eventing must remember across a restart lives there. A separate ADR defines the event store and folds the eventing decisions into it (section 4, H). Why: one instance per service by restore class and ownership, and the seen list then shares a backup cut with the run state and the DLQ. Assessment (2026-10-06, from the code): feasible and small. `Watermark` is a port with a shared contract test, so a new driver plugs in at `pkg/funcd/funcd.go`, where the DLQ store is built later than the watermark. The DLQ `Store` interface is dead-letter specific, so the seen list gets its own key prefix on the same Badger handle, and the TTL and max-entries sweeps must not touch it. Durability follows the engine: with a memory engine, losing the list at a restart is normal (the KV instance is memory by default); with a file-based or remote engine it must survive a restart, and the event store is file-based by default. Risk: a missing record makes the first poll fire every listed object (`pollOne`), so the move needs a one-time copy of the `_eventing/blobwatch/` records, or an explicit no-migration call as ADR-0157 made. ADR-0119 and ADR-0157 are Implemented and frozen, so the new ADR supersedes their placement decision. Size: one JSON record per source and event, as today.
3. **Are run state and the DLQ kept as held evidence, or dropped?** **Decided (owner, 2026-10-06).** Kept as held evidence, restored read-only and never resumed by themselves. Why: a restart resumes a run from its record by design ([ADR-0094](../adr/0094-workflow-engine-core.md)), but after a restore the record is older than the real world; the restore class says history of side effects must not resume, and replay and audit need the records. The stores are small. Example: run invoice-42 finished reserve-stock at the 10:00 backup, charged the customer at 10:20, and the box died at 10:25. Resuming the restored record would charge again, and dropping the record would restart at step 1. Held, the operator sees the last known step and chooses `funcdctl workflow resume`, `replay --from` or `cancel`.
4. **One platform-wide hold, or per-kind flags?** **Decided (owner, 2026-10-06).** One platform-wide hold, implemented in the daemon at the side-effect runners and lifted by an explicit release command. Keep per-kind pause flags for operators, but do not build the hold on the WorkflowRun flag until a test shows it blocks a run with no run record. Why: it restores desired state faithfully and fails closed, and most systems use one switch (section 2, pattern 5). Example: a restore brings back 3 cron Sensors, 1 blob EventSource and 20 runs. With one hold nothing fires until the release command. With per-kind flags the restore sets 24 objects to paused, the operator flips each back by hand, a kind that forgets keeps firing, and the old paused values are lost.
5. **After a restore, do blob events replay or skip ahead?** **Decided (owner, 2026-10-06): replay by default.** The owner's goals: no conflict between the seen list and the runs, a seen list that catches up by itself, and every event that was not fired gets fired. That holds when the seen list is in the same backup cut as the runs (Q2). "Advance to now" is an explicit per-source choice at release. Why: at-least-once is the contract, and silent loss is worse than duplicates. A Sensor keeps no dedup state ([ADR-0109](../adr/0109-sensor-event-action-binder.md)) and each firing starts a new run, so a replayed object starts a second run and the workflow's steps must tolerate it. Example: the seen list is restored to a, b while the bucket holds a, b, c, d. Replay fires c and d on the next poll and records them; advance marks them seen without firing.
6. **resourceVersion: how does a restore keep a stale client from overwriting newer data?** **Decided (owner, 2026-10-06): a timeline in the version, `<timeline>-<n>`.** The timeline is a random ID (at least 64 bits, placeholder), chosen at the store's first start and at every restore, so two instances never share one: not two restores of the same backup on two hosts, and not a fresh cluster that replaces an old one at the same address (today every new store starts its plain counter at 1). `n` is today's counter. A store from before this change keeps its plain numbers as a legacy timeline and takes its first timeline at its next start; old objects keep their plain versions until rewritten. A version from another timeline never equals a current one: a stale update conflicts and a stale watch gets "too old, re-list". No published mark, no jump, no margin, so `markInterval` and `resourceVersionMark` left the plan. Clients do not parse the version (the SDK and funcdctl pass it through). Six places parse it as a number today (`internal/store/watch.go` twice, `internal/controller/controller.go`, `internal/gc/gc.go`, `pkg/funcd/funcd.go` and `internal/services/roles/writers.go`; ADR-0202 lists them) and change to compare inside one timeline. Others: etcd bumps the revision and ends watches, PostgreSQL and ZooKeeper tag the lineage with a timeline or an epoch, Consul has the client reset (section 2, pattern 2). A random UUID per write is equally safe but has no order. The separator is `-`, so a version reads `9c41d2e07b3a5f10-120` and a legacy plain number stays `120`. Open: the key name; the current timeline would live next to the revision counter in the store's meta bucket (placeholder). A data directory copied by hand skips the restore command and keeps its timeline, so it is not a supported path.
7. **Encryption and keys.** **Decided (owner, 2026-10-06).** Backup encryption is on whenever a target is set, recipients are required, and `none` needs an explicit setting. Refuse to back up if Secrets would leave the box in plaintext (no secrets key and encryption off). The offline recovery key is the operator's, never funcd's. Restore requires the escrowed master secret; an explicit flag accepts a new one and prints which derived credentials change. Why: fail closed, secure by default, least privilege.
8. **Which object stores, probe failure, and a delete credential?** **Decided (owner, 2026-10-06).** The targets are S3-compatible stores and the local file system. Each target goes through the startup probe; a local directory gets a process lock and is an independent copy only on another disk or host. A failed probe is an error unless `singleWriter: true` is set. funcd's backup credential is write-only on S3 (it can put and list objects, to find the next generation number, but cannot read or delete them) and a directory relies on file permissions; retention comes from bucket lifecycle and Object Lock; the restore credential is separate and held by the operator. Why: fail closed, least privilege. Example, probe: a store that accepts two simultaneous create-if-absent writes of the same test file cannot stop a second funcd that boots by mistake from overwriting generation 42, so funcd refuses to start backups; `singleWriter: true` is the operator's promise that only one funcd writes there. Example, credential: a bug in a new release deletes everything under the backup bucket; with a normal key the backups vanish with the live data, with the write-only key each delete gets 403.
9. **Is the OCI registry inside the DR scope?** **Decided (owner, 2026-10-06): no, it is outside the DR scope and funcd does not manage it.** It stays an external dependency with no backup and no restore logic. Left from my recommendation, not decided: restore lists the digests it cannot find (read-only), and Functions stay NotReady with a clear status until the artifact is there. Why: do not absorb external systems; reconcilers converge once the artifact is there.
10. **Version rule.** **Decided (owner, 2026-10-06).** Restore from the same or an older minor, refuse a newer one. Older backups go through the same boot-time migrations as an upgrade, and the ADR lists the restorable versions until a migration framework exists. Keep the last 3 pre-upgrade snapshots (a config key). Why: forward-only migrations (blueprint), and a rebuilt host runs the latest funcd.
11. **Who may trigger workload backups and restores, and what is the app boundary?** **Decided (owner, 2026-10-06).** The namespace is the permission boundary (RBAC and Cedar, default-deny). A scope can narrow by resource group or by name. Developers in the namespace create schedules, trigger backups and restore into a new name; in-place restore needs a namespace admin and takes an automatic export first. Why: the namespace is the tenancy unit; a ResourceGroup is only a logical grouping.
12. **rqlite: isolation inside the shared database, and per-app restore.** **Decided (owner, 2026-10-06).** Whole-instance restore only in the first version, per-app restore later. Isolation is enforced by a platform PEP in front of the shared rqlite, with table ownership by prefix, decided in the rqlite ADR. Why: sharing state needs ownership checks, and DR should not promise what isolation cannot yet enforce.
13. **Backup settings: confirm the split, and apply changes without a restart?** **Decided (owner, 2026-10-06).** The split is confirmed. A platform backup interval needs a restart (the operator config is read once; there is no reload path today). A `BackupSchedule` applies live. No SIGHUP reload for now.
14. **ConfigMap and Secret restore: operator-run only, or app self-service?** **Decided (owner, 2026-10-06).** Operator-run in v1 (inspect, then apply through the API; Secret values redacted; Secrets need the escrowed key and an explicit flag). A later optional `resources` scope in `BackupSchedule` gives app owners self-service, with backup encryption required. Why: they are desired state in the platform layer, and Secrets need the escrowed key.

## Key sources

- etcd and Kubernetes: [etcd recovery](https://etcd.io/docs/v3.6/op-guide/recovery/), [Kubernetes etcd page](https://kubernetes.io/docs/tasks/administer-cluster/configure-upgrade-etcd/), [bump-revision PR](https://github.com/etcd-io/etcd/pull/16029), [K3s snapshots](https://docs.k3s.io/cli/etcd-snapshot)
- Velero: [how it works](https://velero.io/docs/main/how-velero-works/), [restore reference](https://velero.io/docs/main/restore-reference/)
- rqlite: [backup guide](https://rqlite.io/docs/guides/backup/), [multiple databases request](https://github.com/rqlite/rqlite/issues/927)
- PostgreSQL: [continuous archiving](https://www.postgresql.org/docs/current/continuous-archiving.html)
- HashiCorp: [Vault restore](https://developer.hashicorp.com/vault/docs/sysadmin/snapshots/restore)
- Cloud Foundry: [BBR developer guide](https://docs.cloudfoundry.org/bbr/bbr-devguide.html), [platform backup scope](https://docs.cloudfoundry.org/bbr/cf-backup.html)
- Workflow engines and restores: [Temporal multi-cluster](https://docs.temporal.io/self-hosted-guide/multi-cluster-replication), [Strimzi recovery](https://strimzi.io/docs/operators/latest/deploying)
- Object stores: [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html), [Go CDK blob](https://pkg.go.dev/gocloud.dev/blob), [MinIO repository](https://github.com/minio/minio)
- Badger: [compaction code](https://github.com/dgraph-io/badger/blob/main/levels.go)
- Practice: [Google SRE data integrity](https://sre.google/sre-book/data-integrity/), [AWS DR options](https://docs.aws.amazon.com/whitepapers/latest/disaster-recovery-workloads-on-aws/disaster-recovery-options-in-the-cloud.html), [GitLab 2017 postmortem](https://about.gitlab.com/blog/postmortem-of-database-outage-of-january-31/)
