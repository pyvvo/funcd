---
title: Bucket
description: Declare a Bucket and its prefixes, bind a prefix to a Function, and read and write its objects over S3 or through ctx.blob.
---

## Usage

A [Bucket](../../../platform/services/bucket.md) holds objects under **prefixes**. A Function reaches a prefix through
a **binding**, and reads and writes its objects with an S3 client, through funcd's **S3 gateway**, or through
[`ctx.blob`](#use-ctxblob-instead-of-an-s3-client).

Install the [dev build of funcdctl](../../../get-started/install.md), Git and curl, and put Node.js on the `PATH`. The
TypeScript example s3-roundtrip writes, reads and lists objects with the AWS SDK, already bundled in its build.

While the S3 gateway runs, each worker of a Function that binds a prefix gets the gateway's address and a key pair of
its own in the standard AWS variables. `funcdctl dev` always runs the gateway; a platform runs it only with
[`s3gateway.enabled: true`](../../running-funcd/spec.md#funcdconfigyaml).

### TypeScript

Clone the repository and start the example:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/s3-roundtrip --gport 8080
```

In another terminal, call the Function twice:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"002"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":2,"denied":false,"granted":true}
```

`denied` reports a write into a prefix that the Function neither owns nor binds, and `granted` a write that a role
allows. On a platform, the first one gets `AccessDenied` and `"denied":true`; under `funcdctl dev` it succeeds, since
[every Function may write every prefix there](#run-it-locally). Press Ctrl-C in the first terminal to stop.

The Bucket, in `bucket.yaml`. A prefix's **owner**, here `s3-roundtrip` for `gold`, can write it:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/bucket.yaml]

The Function, in `function.yaml`, declares the Bucket under
[`spec.buckets`](../../workloads/function/spec.md#functionspecbuckets) and binds `gold` under
[`spec.bindings.blob`](../../workloads/function/spec.md#functionspecbindingsblob): the binding allows reading, the
ownership writing. Over S3, the handler names the Bucket and the full key, such as `gold/roundtrip-001.txt`:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/function.yaml]

The handler:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/src/main.ts]

## Use ctx.blob instead of an S3 client

[`ctx.blob`](spec.md#handler-api), in TypeScript and Python, reaches the prefixes that the Function binds, with no S3
client, key pair or gateway. Its methods take the binding's alias and keys relative to the prefix: under the alias
`gold`, `roundtrip-001.txt` is the S3 object `gold/roundtrip-001.txt`. Use an S3 client for code that already speaks
S3, for a read through a role, or for an object over the `ctx.blob` [limit](#limits-and-quotas).

## Share a Bucket between Functions

Create the Bucket on its own, with the `owner` of each prefix:

::include[../../../../../api/types/v1alpha1/examples/bucket/bucket.yaml#bucket]

Each Function declares it by `ref` and binds the prefixes that it reaches, as in this excerpt of the
[complete example](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/bucket/bucket.yaml):

::include[../../../../../api/types/v1alpha1/examples/bucket/bucket.yaml#binding]

A Function that binds a missing Bucket or prefix waits in `Pending` with `BucketNotFound`, so the resources can be
applied in any order. When a Function declares a Bucket by `name`, funcd creates the Bucket, and the Function owns
each prefix that names no owner.

## Access

- A Function reads the prefixes that it binds (`s3::read`) and writes those that it owns (`s3::write`) with no
  [role](../../security-and-networking/security/index.md#choose-a-role). Any other write needs a role, and a read of an
  unbound prefix a role or a Policy permit.
- The principal is a Function or an [Identity](../../security-and-networking/security/spec.md#identityspec), a client
  outside funcd with the key pair that funcd writes into the Identity's Secret.
- The smallest writer role is
  [`Blob Data Writer`](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref), scoped to
  `BucketPrefix` `<bucket>/<prefix>` or to the `Namespace`; no scope covers one whole Bucket. `Blob Data Reader`
  allows a read without a binding, through the S3 gateway with the key pair of a Function that binds some prefix.
  `Reader` also reads objects, and `Contributor` and `Owner` read and write them; all three cover KV keys too.

The first example's RolesAssignment lets `s3-roundtrip` write the prefix `shared`:

::include[../../../../../api/types/v1alpha1/examples/bucket/writer-role.yaml#rolesassignment]

funcd applies these rules as a built-in policy in every namespace. An operator's
[Policy](../../security-and-networking/security/index.md#write-a-policy) can permit a read, but never a write that it
forbids:

```cedar title="Built-in Bucket policy"
@id("s3-read-binding")
permit(principal, action == Action::"s3::read", resource)
  when { principal has blobBindings && principal.blobBindings.contains(resource) };

@id("s3-write-base")
permit(principal, action == Action::"s3::write", resource);

@id("s3-single-writer")
forbid(principal, action == Action::"s3::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

A denied request fails with the HTTP status 403: through `ctx.blob`, its message names the rule, such as
`cedar: forbidden by policy builtin/s3-single-writer`; through the S3 gateway, its code is `AccessDenied`.

## Limits and quotas

- A Bucket's [`spec.quota`](spec.md#bucketspecquota) and each prefix's [`quota`](spec.md#bucketspecprefixesquota) cap
  its size with `maxSize` or `maxBytes`, and the Bucket's `maxObjectBytes` caps each object. A write must fit both the
  prefix's cap and the Bucket's.
- A `ctx.blob` `put` carries at most 64 MiB. The S3 gateway takes at most
  [`s3gateway.maxUploadBytes`](../../running-funcd/spec.md#funcdconfigyaml), 1 GiB by default, per upload; the
  multipart uploads in flight hold at most that for one principal and twice that in all, and past that a part gets
  `SlowDown`, which asks the client to retry.
- A write fails when it would leave less than `storage.minFreeSize`, 2 GiB by default, free on the data directory's
  filesystem.
- A namespace holds at most 100 Buckets.

## Data storage

funcd keeps the objects in its blob store, by default the `blob` folder of `storage.dataDir`, where they survive a
restart; [`blob.target`](../../running-funcd/spec.md#funcdconfigyaml) keeps them in a versioned S3-compatible bucket
instead. With `storage.mode: memory`, they are lost when funcd stops.

## Status

`funcdctl get bucket lakehouse -n default -o json` and `funcdctl get function s3-roundtrip -n default -o json` print
the status; with `funcdctl dev`, add the `--server` and `--token` flags of its banner.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| Bucket `Ready` | | funcd's blob store answers. |
| Bucket `Degraded` | `StorageUnreachable` | funcd's blob store did not answer its last check, which funcd repeats every `health.storageProbeInterval`, 10 seconds by default. The message of the `Ready` condition gives the error. The Bucket becomes `Ready` again by itself once a check passes. |
| Function `Pending` | `BucketNotFound` | A Bucket or a prefix that the Function binds does not exist. The Function starts once it exists. |
| Function `Pending` | `StoreNotOwned` | A Bucket that the Function declares by `name` exists and is not the Function's, as [Changes and deletion](#changes-and-deletion) describes. |
| Any other reason | | See the [Function's Status](../../workloads/function/index.md#status). |

## Changes and deletion

- funcd refuses to delete a Bucket, or to remove a prefix, while a binding names it or it holds objects.
- When a Function that declares the Bucket by `name` is deleted, its entry's `deletion` decides: `retain`, the
  default, keeps the Bucket and its objects, and `delete` deletes them.
- A Bucket that stayed belongs to the deleted Function: a new Function of the same name declares it by `ref`, or waits
  with `StoreNotOwned`. A prefix's owner is a name, so that new Function can write its prefixes again.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no build of "<name>"` | Build the Function, or name its build after it, where the [Function page](../../workloads/function/index.md#run-it-locally) says that `funcdctl dev` looks. The message names the paths that it looked at. |

## Run it locally

- `funcdctl dev` keeps the objects in memory, or with
  [`--persist`](../../tools/funcdctl/spec.md#funcdctl-dev) in `.funcd-dev/blob`.
- It runs the S3 gateway on a free port, or on `--s3port`, and its banner prints the first Function's key pair as
  `export` lines, so a client such as the aws CLI acts as that Function; `--print-env` prints them without starting.
- It lets every Function write every prefix, so a write that succeeds locally can be denied once deployed. Reads still
  need a binding, a role or a Policy permit.

## Examples

- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): objects
  through the S3 gateway, a refused write and a write that a role allows.
