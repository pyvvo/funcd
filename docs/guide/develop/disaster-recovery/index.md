---
title: Disaster recovery
description: Back up the stores of a namespace to an S3 bucket on a schedule, and restore a Backup into other stores or into the stores that it came from.
---

## Usage

A **BackupSchedule** copies stores of a namespace, such as KV stores, Buckets and SQL databases, to a **backup
target**, a bucket of an S3 service, at the times of a cron schedule. Each copy is a **Backup**, and a **Restore**
loads stores from a Backup, into other, empty stores or back into the stores that the copy came from. See
[how disaster recovery works](../../platform/disaster-recovery.md) for what funcd copies and how a platform comes back
after a host loss.

The first example backs up the Bucket `lakehouse` of the s3-roundtrip example and restores it into an empty Bucket,
`lakehouse-copy`: the Backup and the Restore end in the phase `Succeeded`, and the object read back from
`lakehouse-copy` holds `roundtrip-001`, the content that the example wrote.

Install the [dev build of funcdctl](../../get-started/install.md), Git and curl, which calls the Function and signs
the S3 requests with its `--aws-sigv4` option, and put Node.js on the `PATH`.

`funcdctl dev` starts a local platform and applies the resource files of one folder, here `examples/s3-roundtrip` of
the funcd-typescript repository: the Bucket `lakehouse` and the Function `s3-roundtrip`, which the
[Bucket](../services/bucket/index.md) page explains. At each call, the Function writes an object named after the
call's tag into the prefix `gold` of `lakehouse`. `funcdctl dev` also runs funcd's **S3 gateway**, a server that
answers S3 requests for the Buckets. A `funcdctl` command reaches the local control plane with `--server` and the
token `funcd-dev-token`. The `--gport`, `--cport` and `--s3port` flags set the ports of the Functions' URLs, of the
control plane and of the S3 gateway, so the commands use the ports 8080, 8081 and 8082. `funcdctl dev` keeps its
objects and its Buckets in memory, so all that the steps create is lost when it stops, unless it runs with
`--persist`, as [Run it locally](#run-it-locally) explains.

The example's target is a Bucket of the same platform, `backups`, which the S3 gateway serves on port 8082. Such a
target only shows how the kinds work: a copy kept on the platform that it protects is lost with that platform, so a
real target is a bucket of another S3 provider.

Clone both repositories into one folder, and start the example from the root folder of funcd-typescript:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/s3-roundtrip --gport 8080 --cport 8081 --s3port 8082
```

Once `funcdctl dev` has printed its banner, call the Function once with the tag `001`, in another terminal, in the
folder that holds both clones. The Function writes the object `gold/roundtrip-001.txt`, whose content is
`roundtrip-001`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
```

Apply the schedule and a Backup, from the file
[`backup.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/disaster-recovery/backup.yaml)
of the funcd clone:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/disaster-recovery/backup.yaml
applied Bucket/backups
applied RolesAssignment/s3-roundtrip-reads-backups
applied Secret/backups-key
applied BackupSchedule/lakehouse-nightly
applied Backup/lakehouse-now
```

The Backup runs after the apply has returned. Read its status, and repeat the command until the phase is `Succeeded`
or `Failed`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get backup lakehouse-now -n default -o json
```

The command prints the Backup as JSON. Under `status`, `"phase"` is `"Succeeded"`. `"members"` lists the stores that
the copy holds, its **members**: here the Bucket `lakehouse`, with the size of its encrypted copy in bytes. `"path"`
names the copy's folder on the target, `default/lakehouse-now-<uid>/`, where `<uid>` is the Backup's unique ID;
`"recipients"` holds a fingerprint of each recipient; and `"expireTime"`, 30 days after the Backup's creation, is when
funcd deletes this record.

A Backup that ends in the phase `Failed` stays `Failed`: funcd does not try it again, and applying the unchanged file
again changes nothing. The reason of its condition `Ready` says why it failed, as [Status](#status) explains. The
steps need a Backup that `Succeeded`: once the cause is fixed, delete the Backup with
`funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete backup lakehouse-now -n default`, then apply
`backup.yaml` again, which creates a new Backup of the same name.

`backup.yaml` holds the BackupSchedule `lakehouse-nightly`:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/backup.yaml#backupschedule]

- [`schedule`](spec.md#backupschedulespec) holds a cron expression, whose times are the schedule's **slots**: at each
  slot, here every day at 03:00, the schedule creates a Backup. funcd reads the expression in the time zone that
  `timeZone` names, and in UTC when `timeZone` is empty. The example does not wait for a slot, since it creates a
  Backup by hand.
- [`scope`](spec.md#backupschedulespecscope) names the stores that each Backup copies, here the Bucket `lakehouse`.
- [`target.url`](spec.md#backupschedulespectarget) names the bucket that receives the copies, `backups`, as an
  `s3://` URL with the options of the S3 client as query parameters: `endpoint`, the address of the S3 service, here
  the S3 gateway at port 8082, which `funcdctl dev` therefore runs with `--s3port 8082`; `region`, here `us-east-1`,
  the one region of the S3 gateway; and `use_path_style=true`, which puts the bucket's name in the path of each
  request rather than in its host name.
- [`target.credentialsSecret`](spec.md#backupschedulespectarget) names the Secret `backups-key`, which holds the S3
  key pair that each Backup writes with, under the keys `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. Here it
  holds, in base64, the key pair that `funcdctl dev` gives the Function `s3-roundtrip` and prints in its banner.
  `funcdctl dev` derives that key pair from a constant published in its code, so the key pair protects nothing. The
  Function owns the prefix `default` of `backups`, where each Backup of the namespace `default` writes its copy.
- [`encryption.recipients`](spec.md#backupschedulespecencryption) lists two **recipients** of
  [age](https://github.com/FiloSottile/age/blob/main/README.md), a file encryption format: a recipient is a public key,
  and the matching **identity**, a private key, opens what is sealed to it. funcd seals the files of each store in a
  Backup's copy to every recipient, so the identity of any one of them opens the copy. These two are test keys: the
  example publishes the identity of the first one, so a copy sealed to them protects nothing. For real copies, replace
  them with recipients of new keys, which the `age-keygen` command of age creates, as age's README explains.

The same file holds the Bucket `backups`, the Secret `backups-key`, a RolesAssignment that lets `s3-roundtrip` list and
read the prefix `default` of `backups`, as a Restore needs, and the Backup `lakehouse-now`, which names the schedule. A
Backup made by hand runs without waiting for a slot, and copies what the schedule's `scope` names to the schedule's
target:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/backup.yaml#backup]

Apply the Restore, from the file
[`restore.yaml`](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/disaster-recovery/restore.yaml),
which also holds the objects of `backup.yaml`, unchanged, so that it can be applied on its own:

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

The Restore runs after the apply has returned. Read its status, and repeat the command until the phase is
`Succeeded` or `Failed`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get restore lakehouse-restore -n default -o json
```

Under `status`, `"phase"` is `"Succeeded"`, and the condition `Ready` has the status `"True"`. A Restore that ends in
the phase `Failed` stays `Failed`, as a Backup does, and the reason of its condition `Ready` says why, as
[Status](#status) explains. The last step needs a Restore that `Succeeded`, and a new Restore needs an empty
`lakehouse-copy`:

- A Restore that failed with the reason `NotEmpty` found data in `lakehouse-copy`, and one that failed with the reason
  `Interrupted` clears nothing that it loaded, so a new Restore would fail with `NotEmpty` too. To start again with an
  empty `lakehouse-copy`, stop `funcdctl dev` with Ctrl-C, start it again with the same command, and repeat every step
  from the call of the Function: without `--persist`, `funcdctl dev` keeps no data, so the restart also empties
  `lakehouse`, and the Backup and its copy are gone.
- After any other failure, `lakehouse-copy` is still empty. Once the cause is fixed, delete the Restore with
  `funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete restore lakehouse-restore -n default`,
  then apply `restore.yaml` again.

Read the restored object through the S3 gateway, with the key pair of `s3-roundtrip`. The `--print-env` flag of
`funcdctl dev` prints that key pair, the region and the address of the S3 gateway as `export` lines, without starting
a platform, and `eval` runs them in the shell:

```console
$ eval "$(funcdctl dev funcd-typescript/examples/s3-roundtrip --s3port 8082 --print-env)"
$ curl -s -w '\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" http://127.0.0.1:8082/lakehouse-copy/gold/roundtrip-001.txt
roundtrip-001
```

The URL names the Bucket, then the object's key. The option `--aws-sigv4 aws:amz:us-east-1:s3` signs the request as
an S3 client does, and `-w '\n'` ends the answer with a line break.

Press Ctrl-C in the first terminal to stop the example.

The Restore `lakehouse-restore` of `restore.yaml`:

::include[../../../../api/types/v1alpha1/examples/disaster-recovery/restore.yaml#restore]

- [`backup`](spec.md#restorespec) names the Backup to load. A Restore waits while its Backup is `Pending` or
  `Running`.
- [`source.credentialsSecret`](spec.md#restorespecsource) names the Secret of the S3 key pair that reads the copy,
  from the target that the Backup's `status.target` names. Here it is the same key pair as the schedule's, which the
  RolesAssignment lets read.
- [`source.identitySecret`](spec.md#restorespecsource) names the Secret `backups-identity`, whose key `AGE_IDENTITY`
  holds the identity of the first recipient.
- Each entry of [`members`](spec.md#restorespecmembers) names a store of the copy by its `kind` and its `name`, as
  the Backup's `status.members` lists it, and `as` names the store that receives it: an existing, empty store of the
  same kind.

The same file holds the Secret `backups-identity`, the empty Bucket `lakehouse-copy`, and a RolesAssignment that lets
`s3-roundtrip` read the prefix `gold` of `lakehouse-copy`, which the last step uses. The [Spec](spec.md) tab describes
every field of a BackupSchedule, a Backup and a Restore.

## Choose what a Backup copies

A schedule's [`scope`](spec.md#backupschedulespecscope) names the stores in exactly one of three forms: lists of names, under
`kvStores`, `buckets`, `lakes`, `databases` and `volumes`, where a volume is a Function's local volume written as
`<function>/<name>`; `app`, the stores and the volumes of an App; or `resourceGroup`, the stores of a resource group,
without volumes. funcd resolves the scope when each Backup starts, and the Backup's `status.members` lists each store
that it copied:

- A store that the scope names and that does not exist fails the Backup with the reason `MemberNotFound`.
- A Bucket brings in each Lake located on one of its prefixes as a member of its own, and the Bucket's copy holds none
  of that Lake's data. A Restore of the Bucket leaves those Lakes as they are: to restore them, name them as members
  too.
- funcd copies a Lake again when the Lake deletes files during the copy, as a busy Lake can, and fails the Backup
  with the reason `LakeUnstable` after the third attempt. A SQL database whose engine, the process that serves it, is
  not `Ready` fails the Backup with the reason `EngineNotReady`.
- funcd reads a volume file by file while its Function runs, so the volume's copy is not taken at a single point in
  time.

A Restore loads a Lake member either in place or, with
[`asLocation`](spec.md#restorespecmembersaslocation) instead of `as`, at a new location: a prefix of a Bucket of the
namespace, where nothing is stored yet and no Lake is located. funcd loads the Lake's files under that prefix, and its
catalog under the same location. Then create a Lake at that location, which owns the prefix as the
[Lake](../services/lake/index.md) page explains: the Lake reads the restored files and catalog. A Lake located there
before the Restore makes the Restore fail with the reason `NotEmpty`.

An App can declare its own schedules under
[`spec.backupSchedules`](../workloads/app/spec.md#appspecbackupschedules), where an entry with an empty `scope` backs
up that App.

## Plan a schedule from recovery objectives

The recovery point objective, or RPO, is the most data that a loss may take, given as a duration.
`funcdctl backup plan schedule` sets a BackupSchedule's `schedule` from an RPO, and its `ttl` from how far back a
restore point must reach. It reads a file that holds one BackupSchedule, and prints the file again with these fields
set: it writes no file and needs no platform. In the folder that holds the funcd clone of the first example:

```console
$ funcdctl backup plan schedule -f funcd/api/types/v1alpha1/examples/disaster-recovery/backupschedule.yaml --rpo 1h --keep 168h
```

The command prints the documents of the file, and the BackupSchedule now holds `schedule: "*/30 * * * *"`, a Backup
every 30 minutes, and `ttl: 168h`:

- With `--rpo`, the Backups run once in each period P, the longest period that divides the hour or the day and is at
  most half the RPO, and at least once a day. funcdctl refuses an RPO under 2 minutes, since a cron schedule fires at
  most once a minute.
- With `--keep`, the [`ttl`](spec.md#backupschedulespec) is that duration: funcd keeps the record of each Backup for
  that long after its creation, and a Restore can only name a Backup whose record exists.

To apply the result, send it to `funcdctl apply -f -`. The
[reference of `funcdctl backup plan`](../tools/funcdctl/spec.md#funcdctl-backup-plan) lists its flags, such as `--rto`,
which, with `--size` and `--speed`, checks a recovery time objective against the time that the download of a copy
takes.

## Restore stores in place

A member that sets [`inPlace: true`](spec.md#restorespecmembers) instead of `as` is loaded back into its own store,
whose data it replaces. A Function's local volume is the exception: funcd loads it in place only when it is empty, or
when the Function's condition `DataMissing` names it, after a loss of the volume while funcd was stopped. Such a
Restore needs the permission to update the Namespace, which only an admin has, and funcd runs one at a time in each
namespace: it refuses a second one while the first has not ended. The Restore goes through these steps:

1. While its Backup is `Pending` or `Running`, the Restore is `Pending`. It then creates an **export**, a Backup of
   the restored Backup's schedule named `<restore>-before`, which its `status.export` names, and waits for it. The
   export copies what the schedule's `scope` names when the export runs, which can differ from the members that the
   Restore replaces. A failed export fails the Restore with the reason `ExportFailed`, and nothing is written.
2. The Restore turns `Running`. While the namespace keeps serving, it reads the whole copy and checks every part, then
   checks each member and its destination. A failed check fails the Restore, and nothing is written. For a member in
   place, this check finds a member that the copy does not hold and a store that does not exist, but not a volume that
   is not empty, which is found at its load.
3. It **holds** the namespace: the namespace's Functions refuse calls with the HTTP status 503 and the header
   `Retry-After: 30`, and its Sites keep serving their files. Until its loads end, it also stops the workers, the
   processes that run a Function's code, of each Function whose volume it loads, and the engine, the process that
   serves a Lake, of each Lake that it loads in place. Other workers and engines keep running.
4. It loads each member, in the order of `members`, then lifts the hold, and ends `Succeeded`.

When a load fails, the Restore clears what the loads began and ends `Failed`, and the hold stays: a store loaded in
place before the failing member is then empty, and its former data remains only in the export, when the export's scope
named that store. A volume that is not empty fails its load that way, with the reason `NotEmpty`. When funcd stops
during a load, the next start ends the Restore `Failed` with the reason `Interrupted`, clears nothing, and keeps the
hold. Check the stores, then lift the hold with
[`funcdctl hold release --namespace`](../tools/funcdctl/spec.md#funcdctl-hold-release) and the namespace's name, which
also needs the permission to update the Namespace, and which funcd refuses while the Restore is `Pending` or
`Running`. While that hold stands, funcd refuses a new in-place Restore of the namespace: check the stores, then lift
the hold first.

## Bring a namespace back after a host loss

After a host loss, the operator brings the platform back with `funcd restore run`, as the
[Platform page](../../platform/disaster-recovery.md#after-a-host-loss) explains: the platform comes back as it was at
the time of the platform backup that the operator restores. Each namespace that has a BackupSchedule then stays held
for its data: its Functions refuse calls from the first start. The schedule first lists its target, and records each
whole copy of its slots made after that time as a Backup in the phase `Succeeded`. A Backup made by hand, such as the
first example's, and an export get no record, so no Restore can name their copies. The schedule's condition
`TargetSynced` turns `True` once that listing has succeeded, and the schedule creates no Backup before; the condition
stays `False` with the reason `PlatformRestored` until then, or `ListFailed` while the listing fails.
`funcdctl get backupschedule <name> -n <namespace> -o json` prints the condition under `status.conditions`.

An admin then applies an in-place Restore of the newest Backup, as
[Restore stores in place](#restore-stores-in-place) explains, and once it has `Succeeded`, lifts the namespace's hold
with `funcdctl hold release --namespace`: both need the permission to update the Namespace. A namespace without a
BackupSchedule is not held: its Functions answer calls from the first start, with the data that the operator's restore
of KV and Blob brought back.

## Access

People create and read BackupSchedules, Backups and Restores through funcd's API, where the role of the caller's
credential decides. A viewer of the namespace reads them; a developer of the namespace also creates and deletes them,
and changes a BackupSchedule; an admin does all of this in every namespace. funcd refuses a change to the spec of a
Backup or a Restore: create another one instead. An in-place Restore and `funcdctl hold release --namespace` need the
permission to update the Namespace, which only an admin has. The funcd
[Spec](../running-funcd/spec.md#funcdconfigyaml) tab describes the roles under `auth.credentials`.

Once funcd has accepted a Backup or a Restore, it copies and loads the stores itself, without asking Cedar, the policy
engine that decides what Functions and Identities may do: no RolesAssignment or Policy of the namespace limits what a
Backup copies or a Restore loads. The S3 service of the target checks the key pairs instead. The key of a schedule's
`target.credentialsSecret` needs to put and list objects in the bucket, and the key of a Restore's
`source.credentialsSecret` to get and list them. In the first example, that service is funcd's S3 gateway, so the
key's Function, `s3-roundtrip`, writes the prefix `default` of `backups` as its owner, and lists and reads it through
the role `Blob Data Reader`, which the [Security](../security-and-networking/security/index.md) page explains.

## Limits and quotas

- A BackupSchedule needs at least two distinct recipients, all X25519 keys, which start with `age1`, or all hybrid
  keys, which start with `age1pq1`.
- A BackupSchedule's target is an `s3://` URL, the only target scheme, and names another bucket than the one of
  `blob.target`, the funcd server's own blob store, on its endpoint.
- A BackupSchedule's name has at most 54 characters, and a Restore's at most 56, so that the names that funcd builds
  from them stay valid: `<schedule>-<slot>`, where `<slot>` is the slot's time in minutes since 1970, for the Backups of
  a schedule, and `<restore>-before` for an export.
- A Backup's `ttl` is at most its schedule's.
- A Backup copies at most `workloadBackup.maxCopyMembers` members, 1024 by default: a scope that resolves to more fails
  the Backup with no copy written. One listing of a schedule's target reads at most `workloadBackup.maxListedKeys`
  keys, 1048576 by default, and `workloadBackup.maxListedKeySize` bytes of key names, 256 MiB by default; a listing
  past either fails.

The funcd [Spec](../running-funcd/spec.md#funcdconfigyaml) tab describes the `workloadBackup` settings, and this
page's [Spec](spec.md) tab gives the range of each field.

## Data storage

Each Backup writes its copy into the folder that its `status.path` names on the target, `<namespace>/<backup>-<uid>/`:
the encrypted files of each member, cut into parts, then `manifest.yaml`, written last and not encrypted. The manifest
names the members, the size and the checksum of each of their files, and the fingerprints of the recipients. funcd
writes each object only where none exists yet, which the target must support: a bucket that refuses such a write
fails each Backup with the reason `TargetUnconditional`.

funcd deletes nothing on a target. At a Backup's `status.expireTime`, its creation plus its `ttl`, funcd deletes the
Backup's record, and the copy stays on the target until a lifecycle rule of the target deletes it. Without such a rule,
the copies stay on the target. A Backup's `ttl` is its schedule's, unless the Backup sets a shorter one, and 720 hours,
30 days, when neither sets one.

## Status

`funcdctl get backup <name> -n <namespace> -o json` prints a Backup's status, as the first example shows, and
`get backupschedule` and `get restore` print those of a schedule and a Restore. With `funcdctl dev`, add the
`--server` and `--token` flags of the first example. A Backup is `Pending`, then `Running`, then `Succeeded` or
`Failed`, in one attempt. A Restore is `Pending` while it waits for its Backup and, in place, for its export, `Running`
while it checks the copy and loads, then `Succeeded` or `Failed`. A `Failed` one's condition `Ready` has the status
`False`, and its reason says why.

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
| BackupSchedule `Ready` | `SameProvider` | The URL names another bucket on the endpoint of `blob.target`, the funcd server's own blob store, so one failure of that provider takes both. |
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

A BackupSchedule's status also gives the newest slot that it took, `lastScheduleTime`; the completion of its newest
Backup that `Succeeded`, `lastSuccessTime`; the start of the oldest one, `earliestRestorePoint`, the oldest point that a
Restore can go back to; and the Backup that is `Pending` or `Running`, `active`. `rpoRisk` is `true` when the schedule
has fallen behind, as the [Spec](spec.md#backupschedulestatus) tab defines, and stays `false` while the schedule is
paused: when it turns `true`, funcd logs `workload backup rpo at risk`.

## Changes and deletion

A change to a BackupSchedule applies with no restart. Each Backup reads the scope, the target and the recipients of
its schedule when it starts, and a new `schedule` moves the next slot. [`paused: true`](spec.md#backupschedulespec)
stops the slots until it is `false` again, while a Backup created by hand still runs. The slots missed during a pause,
while funcd was stopped or while the namespace was held collapse into one Backup, and a slot that comes while one of
the schedule's Backups is `Pending` or `Running` creates none.

The spec of a Backup or a Restore cannot change. A delete of a Backup deletes its record only: its copy stays on the
target, but no Restore can name it any more. A Backup outlives its schedule and its App until its `expireTime`: a
Restore into another store still loads it, while an in-place Restore fails with the reason `ScheduleNotFound`, since
its export needs the schedule. funcd refuses to delete an in-place Restore that has not ended.

## CLI errors

These local errors stop a command before any server answers:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | `funcdctl dev` found no Node.js. Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `--rpo <duration>: a cron fires at most once a minute, so want at least 2m` | Give `funcdctl backup plan schedule` an RPO of 2 minutes or more. |
| `-f <file> holds <n> BackupSchedule documents: want one` | Give `funcdctl backup plan schedule` a file that holds one BackupSchedule. |

## Run it locally

`funcdctl dev` runs BackupSchedules, Backups and Restores as a funcd server does, with these differences:

- It derives the S3 key pair of each Function from a constant, so a key pair is the same at each run, and its banner
  prints the key pair of the folder's first Function. A funcd server derives its key pairs from a secret of its own,
  so the key pair in the first example's Secret works only with `funcdctl dev`.
- Its token, `funcd-dev-token`, is a developer of the namespace `default`, so it cannot update the Namespace: an
  in-place Restore and `funcdctl hold release --namespace` are refused.
- It keeps its objects and its Buckets in memory, so the schedules, the Backups, the Restores and the copies kept in
  its Buckets are lost when it stops. With the `--persist` flag, it keeps them between runs in `.funcd-dev`, a folder
  that it creates in the current folder, or in the folder that `--persist-to` names. To start again from nothing,
  delete that folder after `funcdctl dev` stops.

The [reference of `funcdctl dev`](../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): the Bucket
  that the first example backs up and restores.
- [todo](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): an App whose test runs a disaster-recovery
  drill on a funcd server, with an S3 server beside it: Backups of the App's KV store, Bucket, Lake and SQL database,
  their Restores under new names and in place, and a platform restore after the loss of the data folder.
- [volumes](https://github.com/pyvvo/funcd-functions/tree/main/apps/volumes): an App whose local volume, lost while
  funcd was stopped, is restored in place from a Backup.
