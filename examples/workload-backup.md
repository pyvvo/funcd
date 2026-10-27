# Workload backups: an app owner's schedule, records and restores

An app owner backs up its own KVStores, Buckets and lakes with three namespaced kinds (ADR-0222): a
`BackupSchedule` defines the scope, the cron slots, the S3 target and the recipients; each slot, a hook or a
developer creates a `Backup`, one full copy; a `Restore` loads a `Backup`'s members back. funcd runs them with the
platform's rights; RBAC at admission is the boundary (developers and admins write them, viewers read them).

## The target and its credentials

The target is an `s3://` bucket the owner holds, outside the blob store's bucket (a target inside it is refused; one on
the same endpoint is accepted with the schedule's `Ready` reason `SameProvider`). Each Backup writes
`<target>/<namespace>/<backup>-<uid>/` once, every object create-if-absent, `manifest.yaml` last; funcd never deletes,
reads or overwrites an object there.

The write credential puts and lists only; the Restore's credential gets and lists. Each is a Secret of the namespace
with the keys `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and, optionally, `AWS_SESSION_TOKEN`:

```yaml
apiVersion: funcd.io/v1alpha1
kind: Secret
metadata:
  name: backup-writer
  namespace: orders
  resourceGroup: orders
spec:
  data:
    AWS_ACCESS_KEY_ID: <base64 key id>
    AWS_SECRET_ACCESS_KEY: <base64 secret>
```

## The schedule

```yaml
apiVersion: funcd.io/v1alpha1
kind: BackupSchedule
metadata:
  name: orders-nightly
  namespace: orders
  resourceGroup: orders
spec:
  schedule: 0 3 * * *
  timeZone: Europe/Paris
  ttl: 720h
  scope:
    kvStores:
      - orders-state
    buckets:
      - orders-files
  target:
    url: s3://orders-backup?region=eu-west-3
    credentialsSecret: backup-writer
  encryption:
    recipients:
      - age1owner…
      - age1recovery…
```

The scope is one form: names (`kvStores`, `buckets`, `lakes`), `app` (the App's stores) or `resourceGroup`. Every
file is sealed to the recipients, two or more of one family; keep the recovery identity outside the platform. An App
declares the same under `spec.backupSchedules`; an entry without a scope backs up the App itself.

`funcdctl get backups -n orders` lists the runs. A schedule's `status` carries `lastSuccessTime`,
`earliestRestorePoint` and `rpoRisk`, also counted by the `funcd.backup.workload_rpo_risk` gauge.

## Lifecycle rule

A Backup record is deleted at its `expireTime` (creation + `ttl`), its objects are not: set one expiry on
`<namespace>/` of ⌈`ttl` / 24h⌉ + 1 days, 31 at the default `720h`:

```json
{
  "Rules": [
    {"ID": "funcd-orders", "Status": "Enabled", "Filter": {"Prefix": "orders/"}, "Expiration": {"Days": 31}}
  ]
}
```

## A Backup by hand and a Restore

```yaml
apiVersion: funcd.io/v1alpha1
kind: Backup
metadata:
  name: before-migration
  namespace: orders
  resourceGroup: orders
spec:
  schedule: orders-nightly
---
apiVersion: funcd.io/v1alpha1
kind: Restore
metadata:
  name: orders-back
  namespace: orders
  resourceGroup: orders
spec:
  backup: before-migration
  source:
    credentialsSecret: backup-reader
    identitySecret: owner-key
  members:
    - kind: KVStore
      name: orders-state
      as: orders-state-r
```

`owner-key` holds the owner's age identity under `AGE_IDENTITY`. Each member names one target: `as` (an empty KVStore
or Bucket), `asLocation` (an empty bucket prefix, then point a Lake at it) or `inPlace: true`. An in-place
Restore needs `update` on the Namespace (an admin), and one runs per namespace at a time: a second is refused while
the first is `Pending` or `Running`, and so is a delete of the first. It first runs Backup `<restore>-before`, then holds the namespace while it
empties and loads the store; only a completed load lifts that hold. While it is held, calls to the namespace's
Functions get 503 with `Retry-After: 30`, and writes through the S3 gateway get the S3 error `ServiceUnavailable`
(ADR-0290). One whose load fails, or that funcd stopped
during the load (it ends `Failed`, `Interrupted`, at the next start), keeps the namespace held: `funcdctl hold status`
names it, and no in-place Restore of the namespace is admitted until the release: check the store, then
`funcdctl hold release --namespace orders` (refused while a Restore it names is `Pending` or `Running`).

## After a platform restore

`funcd restore run` holds each namespace that has a BackupSchedule: its stores return without their data, so its
timers, blob sources, Sensors, runs, App and slots wait (Backups by hand and Restores still run). Calls to its
Functions get 503 with `Retry-After: 30`, so that no write lands in a store that the owner's restore then replaces,
and its Sites keep serving their files (ADR-0290). Writes through the S3 gateway into its Buckets get the S3 error
`ServiceUnavailable` (503), which S3 clients retry; reads still work. Restore the data, then
`funcdctl hold release --namespace orders`; the next call and the next write are served.

The restored records stop at the platform backup's point, while the schedule kept backing up on the lost host. So
`restore run` marks each schedule `TargetSynced` `False` (reason `PlatformRestored`). Its reconciler then lists the
target, with the schedule's own put-and-list credential, and records each whole copy of a slot after the restored
`lastScheduleTime` as a `Succeeded` Backup with reason `FoundOnTarget` (ADR-0287). The schedule takes no slot until
that sync has succeeded, released or not:

```sh
funcdctl get backupschedules orders-nightly -n orders -o json  # wait for TargetSynced True: "found 3 Backups, newest …"
funcdctl get backups -n orders                                 # then restore the newest
```

While the target refuses the listing, `TargetSynced` reads `False`, reason `ListFailed`, and the sync retries; point
`spec.target.url` at a target that answers, or delete the schedule, to let it go on. Copies of Backups by hand, of
exports and of schedules the restored platform does not hold get no record. Delete no Backup before `TargetSynced`
reads `True`: a crash during the sync can bring a record deleted in the meantime back once.
