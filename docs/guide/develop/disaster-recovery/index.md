---
title: Disaster recovery
description: Back up the stores of a namespace to an S3 bucket on a schedule, and restore a Backup into other stores or into the stores that it came from.
---

## Usage

A **BackupSchedule** copies a namespace's stores to an S3 bucket, its **target**, on a cron schedule. Each copy is a
**Backup**; a **Restore** loads one into empty stores or back into its own
([how it works](../../platform/disaster-recovery.md)).

The example backs up the Bucket `lakehouse` of [s3-roundtrip](../services/bucket/index.md) into an empty Bucket,
`lakehouse-copy`. Install the
[dev build of funcdctl](../../get-started/install.md), Git and curl, and put Node.js on the `PATH`. `--s3port` starts
funcd's S3 gateway, which serves the Buckets on port 8082.

Its target, `backups`, is a Bucket of the same platform; a real target is a bucket of another S3 provider, since a
copy on the platform it protects is lost with it.

Clone both repositories into one folder, and start the example from the root folder of funcd-typescript:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/s3-roundtrip --gport 8080 --cport 8081 --s3port 8082
```

In another terminal, in the folder that holds both clones, call the Function; it writes `gold/roundtrip-001.txt`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
```

Apply the schedule and a Backup, from
[`backup.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/disaster-recovery/backup.yaml):

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/disaster-recovery/backup.yaml
applied Bucket/backups
applied RolesAssignment/s3-roundtrip-reads-backups
applied Secret/backups-key
applied BackupSchedule/lakehouse-nightly
applied Backup/lakehouse-now
```

Repeat until the phase is `Succeeded` or `Failed`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get backup lakehouse-now -n default -o json
```

`status.members` lists the stores copied, its **members**, and `path` the copy's folder on the target. A `Failed`
Backup is never retried ([Status](#status)): fix the cause, delete it, and apply again.

The schedule:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/backup.yaml#backupschedule]

- [`schedule`](spec.md#backupschedulespec) is a cron expression, read in `timeZone`, else UTC; each of its times is a
  **slot**, at which the schedule creates a Backup.
- [`scope`](spec.md#backupschedulespecscope) names the stores to copy.
- [`target.url`](spec.md#backupschedulespectarget) is an `s3://` URL with the S3 client's options as its query.
- [`target.credentialsSecret`](spec.md#backupschedulespectarget) holds the S3 key pair under `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY`; here, the dev key pair of `s3-roundtrip`, which protects nothing.
- [`encryption.recipients`](spec.md#backupschedulespecencryption) lists [age](https://github.com/FiloSottile/age/blob/main/README.md)
  public keys, **recipients**; the private key of any one, its **identity**, opens the copy. These are test keys whose
  identity is published: for real copies, create new ones with `age-keygen`.

A Backup made by hand runs at once:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/backup.yaml#backup]

Apply the Restore, from
[`restore.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/disaster-recovery/restore.yaml),
which repeats the objects of `backup.yaml`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/disaster-recovery/restore.yaml
applied Bucket/backups
applied RolesAssignment/s3-roundtrip-reads-backups
applied Secret/backups-key
applied BackupSchedule/lakehouse-nightly
applied Backup/lakehouse-now
applied Secret/backups-identity
applied Bucket/lakehouse-copy
applied RolesAssignment/s3-roundtrip-reads-copy
applied Restore/lakehouse-restore
```

Repeat until the phase is `Succeeded` or `Failed`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get restore lakehouse-restore -n default -o json
```

After `NotEmpty` or `Interrupted`, `lakehouse-copy` holds data: restart `funcdctl dev` and repeat from the call.
After another failure, fix the cause, delete the Restore and apply again.

Read the restored object with the key pair of `s3-roundtrip`, which `--print-env` prints as `export` lines:

```console
$ eval "$(funcdctl dev funcd-typescript/examples/s3-roundtrip --s3port 8082 --print-env)"
$ curl -s -w '\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" http://127.0.0.1:8082/lakehouse-copy/gold/roundtrip-001.txt
roundtrip-001
```

Press Ctrl-C in the first terminal to stop. The Restore:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/restore.yaml#restore]

- [`backup`](spec.md#restorespec) names the Backup; the Restore waits while it is `Pending` or `Running`.
- [`source.credentialsSecret`](spec.md#restorespecsource) holds the key pair that reads the copy from the Backup's
  `status.target`.
- [`source.identitySecret`](spec.md#restorespecsource) holds an identity under `AGE_IDENTITY`.
- Each [`members`](spec.md#restorespecmembers) entry names a member by `kind` and `name`, and `as` names an existing,
  empty store of the same kind.

## Choose what a Backup copies

A [`scope`](spec.md#backupschedulespecscope) takes one of three forms: name lists under `kvStores`, `buckets`,
`lakes`, `databases` and `volumes` (a Function's local volume, as `<function>/<name>`); `app`, an App's stores and
volumes; or `resourceGroup`, a resource group's stores, without volumes. funcd resolves it when each Backup starts:

- A named store that does not exist fails the Backup with `MemberNotFound`.
- A Bucket brings in each Lake on its prefixes as a separate member. Restoring the Bucket leaves those Lakes alone:
  name them as members too.
- A Lake that deletes files during the copy is copied again, and fails the Backup with `LakeUnstable` after the third
  try. A SQL database whose engine is not `Ready` fails it with `EngineNotReady`.
- A volume is read file by file while its Function runs, so its copy is not a single point in time.

A Lake member can load at a new location with [`asLocation`](spec.md#restorespecmembersaslocation) instead of `as`:
an empty prefix of a Bucket of the namespace where no Lake is located. Then create a
[Lake](../services/lake/index.md) there, which reads the restored files and catalog.

An App declares its own schedules under [`spec.backupSchedules`](../workloads/app/spec.md#appspecbackupschedules);
an entry with an empty `scope` backs up that App.

## Plan a schedule from recovery objectives

`funcdctl backup plan schedule` sets a BackupSchedule's `schedule` from a recovery point objective (RPO), the most data
a loss may take, and its `ttl` from how far back a restore must reach. It prints the file with those fields set, and
needs no platform:

```console
$ funcdctl backup plan schedule -f funcd/api/types/v1alpha1/examples/disaster-recovery/backupschedule.yaml --rpo 1h --keep 168h
```

The result holds `schedule: "*/30 * * * *"` and `ttl: 168h`:

- `--rpo` runs a Backup once in each period that divides the hour or the day and is at most half the RPO, and at
  least daily. An RPO under 2 minutes is refused.
- `--keep` sets the [`ttl`](spec.md#backupschedulespec): how long each Backup's record, which a Restore needs, is
  kept.

Pipe the result to `funcdctl apply -f -`. The [reference](../tools/funcdctl/spec.md#funcdctl-backup-plan) lists the
other flags, such as `--rto`, `--size` and `--speed`.

## Restore stores in place

A member with [`inPlace: true`](spec.md#restorespecmembers) instead of `as` replaces its store's data; a volume only
when empty or named by its Function's `DataMissing`. It needs an admin, and runs one at a time per namespace:

1. `Pending`: it waits for its Backup, then for an **export**, a Backup of the schedule named `<restore>-before`
   (`status.export`), which copies the schedule's current `scope`. A failed export fails it with `ExportFailed`.
2. `Running`: while the namespace keeps serving, it checks the whole copy and each destination. A failed check writes
   nothing.
3. It **holds** the namespace: Functions answer 503 with `Retry-After: 30`, Sites keep serving, and the workers of a
   loaded volume and the engines of a Lake loaded in place stop until the loads end.
4. It loads each member in order, lifts the hold, and ends `Succeeded`.

A failure after the hold keeps the hold. A failed load clears what the loads began, so the old data is only in the
export; a non-empty volume fails with `NotEmpty`. A stop during a load ends it `Interrupted`, clearing nothing. Check
the stores, then lift the hold with [`funcdctl hold release --namespace`](../tools/funcdctl/spec.md#funcdctl-hold-release)
(admin only, refused while the Restore runs). No new in-place Restore runs while it stands.

## Bring a namespace back after a host loss

After the operator's `funcd restore run` ([Platform page](../../platform/disaster-recovery.md#after-a-host-loss)),
each namespace with a BackupSchedule stays held: its Functions refuse calls. Each schedule lists its target and
records every whole slot copy made after the platform backup as a `Succeeded` Backup; Backups made by hand and exports
get no record. Until that listing succeeds, the schedule's `TargetSynced` condition is `False` (`PlatformRestored`, or
`ListFailed`) and it creates no Backup.

An admin then applies an in-place Restore of the newest Backup and, once it has `Succeeded`, runs
`funcdctl hold release --namespace`. A namespace without a BackupSchedule is not held.

## Access

- A namespace viewer reads BackupSchedules, Backups and Restores; a developer also creates and deletes them and
  changes a schedule; an admin does this everywhere
  ([`auth.credentials`](../running-funcd/spec.md#funcdconfigyaml)). An in-place Restore and
  `funcdctl hold release --namespace` need the permission to update the Namespace.
- A Backup's or a Restore's spec cannot change: create another one.
- Backups and Restores ignore the namespace's RolesAssignments and Policies. The target checks the key pairs: the
  schedule's needs put and list, the Restore's get and list. In the example, `s3-roundtrip` owns the prefix `default`
  of `backups`, and reads it through `Blob Data Reader` ([Security](../security-and-networking/security/index.md)).

## Limits and quotas

- At least two distinct recipients, all X25519 (`age1…`) or all hybrid (`age1pq1…`).
- The target is an `s3://` URL, on another bucket than the server's `blob.target`.
- A BackupSchedule's name has at most 54 characters and a Restore's at most 56, since funcd names Backups
  `<schedule>-<slot>` (minutes since 1970) and exports `<restore>-before`.
- A Backup's `ttl` is at most its schedule's.
- A Backup copies at most `workloadBackup.maxCopyMembers` members (1024); a target listing reads at most
  `workloadBackup.maxListedKeys` keys (1048576) and `workloadBackup.maxListedKeySize` bytes of names (256 MiB).

See the server's [Spec](../running-funcd/spec.md#funcdconfigyaml) for `workloadBackup`, and this page's
[Spec](spec.md) for field ranges.

## Data storage

A copy lives under `status.path`, `<namespace>/<backup>-<uid>/`: each member's encrypted files in parts, then an
unencrypted `manifest.yaml`, written last, with the members, sizes, checksums and recipient fingerprints. funcd writes
only where no object exists yet; a target that refuses such writes fails each Backup with `TargetUnconditional`.

funcd deletes nothing on a target. At `status.expireTime` (creation plus `ttl`: the Backup's, else the schedule's,
else 720 hours) it deletes the record only; use a lifecycle rule of the target to delete the copies.

## Status

`funcdctl get backup|backupschedule|restore <name> -n <namespace> -o json` prints the status. A Backup goes `Pending`,
`Running`, then `Succeeded` or `Failed`, in one attempt. A Restore is `Pending` while it waits for its Backup (and, in
place, its export), `Running` while it checks and loads, then `Succeeded` or `Failed`, with the reason on `Ready`.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| Backup `Failed` | `ScheduleNotFound` | The schedule did not exist when the run started. |
| Backup `Failed` | `MemberNotFound` | A store of the scope does not exist. The message names it. |
| Backup `Failed` | `LakeUnstable` | A Lake deleted files during each of three copies, as [Choose what a Backup copies](#choose-what-a-backup-copies) explains. |
| Backup `Failed` | `EngineNotReady` | The engine of a SQL database of the scope is not `Ready`. |
| Backup `Failed` | `TargetUnconditional` | The target refuses a write only where no object exists yet, as [Data storage](#data-storage) explains. |
| Backup `Succeeded` | `FoundOnTarget` | The Backup was `Running` when funcd stopped, and its copy is whole on the target. |
| Backup `Failed` | `Interrupted` | The Backup was `Running` when funcd stopped, and its copy is not whole. |
| Backup `Failed` | `Failed` | Any other cause, which the message names. |
| BackupSchedule `Ready` | none, or `SameProvider` | The schedule runs. `SameProvider`, on a `True` condition, only warns: the URL names another bucket on the endpoint of `blob.target`, the funcd server's own blob store, so one failure of that provider takes both. |
| BackupSchedule `TargetSynced` condition `False` | `PlatformRestored` or `ListFailed` | After a platform restore, the schedule has not yet listed its target, or the listing fails. It creates no Backup until the condition is `True`, as [Bring a namespace back after a host loss](#bring-a-namespace-back-after-a-host-loss) explains. |
| Restore `Failed` | `BackupNotReady` | The Backup does not exist, or is `Failed`. |
| Restore `Failed` | `MemberNotFound` | The copy holds no such member, or the destination does not exist. |
| Restore `Failed` | `NotEmpty` | The destination holds data: a Restore loads into an empty one. |
| Restore `Failed` | `Integrity` | A part of the copy does not match the manifest. |
| Restore `Failed` | `EngineNotReady` | The engine of a destination database is not `Ready`. |
| Restore `Failed` | `ExportFailed` or `ScheduleNotFound` | In place: the export failed, or the schedule that it needs does not exist. |
| Restore `Failed` | `NamespaceHeld` | In place: an earlier Restore's load did not complete, and its hold stands. |
| Restore `Failed` | `Interrupted` | funcd stopped while the Restore was `Running`. |
| Restore `Failed` | `Failed` | Any other cause, such as an identity that matches none of the recipients, whose message names the fingerprints of both. |

A BackupSchedule's status also gives `lastScheduleTime`, `lastSuccessTime`, `earliestRestorePoint` (the oldest
point a Restore can reach) and `active`. `rpoRisk` turns `true` when the schedule falls behind
([Spec](spec.md#backupschedulestatus)), never while paused, and funcd logs `workload backup rpo at risk`.

## Changes and deletion

- A BackupSchedule change applies at once: each Backup reads the scope, target and recipients when it starts.
- [`paused: true`](spec.md#backupschedulespec) stops the slots; a Backup made by hand still runs. Slots missed while
  paused, stopped or held collapse into one Backup, and a slot that comes while a Backup is active creates none.
- Deleting a Backup deletes its record, not its copy, and no Restore can name it after. A Backup outlives its schedule
  and App until `expireTime`, but an in-place Restore of it fails with `ScheduleNotFound`.
- funcd refuses to delete an in-place Restore that has not ended.

## CLI errors

These local errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `--rpo <duration>: a cron fires at most once a minute, so want at least 2m` | Give `funcdctl backup plan schedule` an RPO of 2 minutes or more. |
| `-f <file> holds <n> BackupSchedule documents: want one` | Give `funcdctl backup plan schedule` a file that holds one BackupSchedule. |

## Run it locally

- `funcdctl dev` derives each Function's S3 key pair from a constant and prints the first one in its banner; a server
  derives its own, so the example's Secret works only under `funcdctl dev`.
- Its token, `funcd-dev-token`, is a developer of `default`: in-place Restores and `hold release` are refused.
- Everything stays in memory unless [`--persist`](../tools/funcdctl/spec.md#funcdctl-dev) keeps it in `.funcd-dev`
  (or `--persist-to`); delete that folder to start over.

## Examples

- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): the Bucket
  backed up above.
- [todo](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): an App drill with Backups of a KV store, a
  Bucket, a Lake and a SQL database, Restores under new names and in place, and a platform restore.
- [volumes](https://github.com/pyvvo/funcd-functions/tree/main/apps/volumes): a lost local volume restored in place.
