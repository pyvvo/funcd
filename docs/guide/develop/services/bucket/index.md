---
title: Bucket
description: Declare a Bucket and its prefixes, bind a prefix to a Function, and read and write its objects over S3 or through ctx.blob.
---

## Usage

A Bucket holds objects, grouped under **prefixes**. A prefix is a separate set of objects inside the Bucket. A Function
reaches a prefix through a **binding**, and its handler reads and writes the prefix's objects in one of two ways: with
an S3 client, which sends its requests to funcd's S3 gateway, or through `ctx.blob`, the blob client in the context
that every handler receives. The [Platform page](../../../platform/services/bucket.md) explains how Buckets work inside
funcd; this page shows how to use them.

The first example, s3-roundtrip, writes an object with an S3 client and reads it back. Each call sends a tag. The
Function writes an object named after the tag into the prefix `gold`, reads the object back, and counts the objects
under `gold`. The steps below start the example on the local computer and call it twice with two different tags: the
first response contains `"list":1`, and the second contains `"list":2`, since the prefix then holds both objects.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which calls the Function.
- Node.js on the `PATH`, which runs the example's TypeScript Function.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's manifest names
an `image`, the bundle that `funcdctl push` writes for a platform to run. `funcdctl dev` does not use that image: it
runs the Function from the built file in the folder, `s3-roundtrip.mjs`, which already contains the AWS SDK for
JavaScript, so the example needs no install step. `funcdctl dev` keeps the Bucket's objects in memory and prints the
URL of each Function. The `--gport` flag sets the port of that URL, so the commands below use port 8080.

`funcdctl dev` also runs funcd's **S3 gateway**, a server that answers S3 requests for the Buckets. While the gateway
runs, funcd gives each worker of a Function that binds a prefix the gateway's address and an S3 key pair of the
Function's own, in the standard AWS environment variables `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`. On a platform, the gateway runs only when the operator sets `s3gateway.enabled: true` in
funcd's configuration.

The example's manifests connect the Function to its data:

- The Bucket `lakehouse` is a resource of its own, in `bucket.yaml`. Its prefix `gold` names the Function
  `s3-roundtrip` as its **owner**, which can write the prefix. Its prefixes `other` and `shared` have no owner, and
  its prefixes `bi` and `docs` hold the files of two Sites that this page does not use.
- The Function declares the Bucket by `ref` under [`spec.buckets`](../../workloads/function/spec.md#functionspecbuckets).
  funcd rejects a binding to a Bucket that its Function does not declare.
- A **binding** under [`spec.bindings.blob`](../../workloads/function/spec.md#functionspecbindingsblob) connects the
  prefix `gold` to the Function under the alias `gold`. The binding lets the Function read the prefix, and the
  ownership lets the Function write it; [Access](#access) explains which other principals can write it. Over S3, the
  handler does not use the alias: it names the Bucket and the object's key, whose first segment is the prefix, as in
  `gold/roundtrip-001.txt`. `ctx.blob` takes the alias instead.

While a binding names a Bucket or a prefix that does not exist yet, funcd holds the Function in `Pending`, with the
reason `BucketNotFound`, and starts it once the prefix exists, so the two files can be applied in either order.

The folder also holds a RolesAssignment, which [Access](#access) explains, and resources that this page does not use:
a Workflow, an EventSource, a Sensor and two Sites. `funcdctl dev` applies them all. The first example exists in
TypeScript only; [Use ctx.blob instead of an S3 client](#use-ctxblob-instead-of-an-s3-client) also gives the Python
names of the blob client's methods.

The [Spec](spec.md) tab lists every field of a Bucket.

### TypeScript

The Bucket `lakehouse`, in `bucket.yaml`:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/bucket.yaml]

The Function `s3-roundtrip`, in `function.yaml`, declares the Bucket and binds its prefix `gold`:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/function.yaml]

The handler creates an S3 client from the environment variables, then sends its requests to the gateway. It writes the
object `gold/roundtrip-<tag>.txt` and reads it back, lists the prefix `gold`, tries to write into the prefix `other`,
which it neither owns nor binds, and writes and reads an object in the prefix `shared`, which a role lets it write.
Its response reports each step: `put` is `true` once the write succeeded, `get` is `true` when the object came back
unchanged, `list` counts the objects under `gold` and is 0 when the new object is not among them, `denied` is `true`
when the gateway refused the write into `other`, and `granted` is `true` when the object written into `shared` came
back unchanged:

::include[../../../../../_repos/funcd-typescript/examples/s3-roundtrip/src/main.ts]

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/s3-roundtrip --gport 8080
```

In another terminal, call the Function twice, with the tags `001` and `002`:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"002"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":2,"denied":false,"granted":true}
```

The second response's `"list":2` shows that the prefix `gold` still holds the first call's object.

`"denied":false` shows a difference between `funcdctl dev` and a platform. `funcdctl dev` gives each Function of the
folder a role that allows writing every prefix of the namespace's Buckets, so the write into `other` succeeds. A
platform gives no such role: there, the gateway refuses that write with the S3 error `AccessDenied`, and the response
contains `"denied":true`. `"granted":true` holds on a platform as well, through the RolesAssignment that
[Access](#access) shows. [Run it locally](#run-it-locally) lists the other differences.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Use ctx.blob instead of an S3 client

A handler can reach the objects of the prefixes it binds through `ctx.blob`, with no S3 client and no S3 gateway. Each
method of `ctx.blob` takes the alias of a binding as its first argument and, except `list`, the key of an object as its
second. A key is relative to the bound prefix: under the alias `gold` of the first example, the key
`roundtrip-001.txt` names the object that the S3 gateway shows as `gold/roundtrip-001.txt`.

- `put` stores bytes under a key: a `Uint8Array` in TypeScript, `bytes` in Python. `get` returns those bytes, or
  `null` (`None` in Python) when the object does not exist.
- `del` (`delete` in Python) deletes an object.
- `list` returns the keys of the bound prefix's objects, without the prefix itself. Its optional second argument keeps
  only the keys that start with it.
- `signedUrl` (`signed_url` in Python) asks for a presigned URL, a link that allows requests of one method on one
  object, without credentials, until it expires. None of the blob stores that funcd runs on signs such URLs, so even
  a valid call fails, with the HTTP status 503. Before that, funcd refuses with 403 a URL for a request that the
  caller may not make, and a PUT URL into a Bucket or a prefix that has a size limit.

`ctx.blob` follows the same policy as the S3 gateway, and reaches only the prefixes that the Function binds: a call
whose alias no binding of the Function declares fails with the HTTP status 403. A write needs the ownership of the
prefix or a role, as [Access](#access) explains.

## Share a Bucket between Functions

The Bucket of the first example is a resource of its own, which the Function declares by `ref`. Several Functions can
share such a Bucket. Set the `owner` of each prefix to the Function that writes the prefix:

::include[../../../../../api/types/v1alpha1/examples/bucket/bucket.yaml#bucket]

Each Function that uses the Bucket declares it by `ref` under `spec.buckets`, and binds each prefix that it reaches.
The Function `resize` reads the prefix `uploads`, which the Function `ingest` owns, and writes the prefix `thumbnails`,
which `resize` owns. This excerpt shows the `spec.buckets` and `spec.bindings` of `resize`; the complete file, with
both Functions, is
[api/types/v1alpha1/examples/bucket/bucket.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/bucket/bucket.yaml).

::include[../../../../../api/types/v1alpha1/examples/bucket/bucket.yaml#binding]

Every Function that binds a prefix can read it. To write a prefix that it does not own, a Function needs the
permission that [Access](#access) describes. When a prefix of a Bucket applied on its own sets no `owner`, only that
permission lets a principal write the prefix.

A Function can also declare a Bucket by `name` under `spec.buckets`. funcd then creates the Bucket, and each of its
prefixes that sets no `owner` gets that Function as its owner. [Changes and deletion](#changes-and-deletion) explains
what happens to such a Bucket when the Function is deleted.

## Access

A Function needs no role assignment to:

- **Read the prefixes it binds.** Each entry of `spec.bindings.blob` lets the Function read that prefix. A binding
  allows reading only, never writing.
- **Write the prefixes it owns.** The owner of a prefix can write it. No other principal can write the prefix unless
  a role allows it.

Both hold through `ctx.blob` and through the S3 gateway. Any other access needs a role. A RolesAssignment gives a
principal, a Function or an Identity, a role on one prefix or on the whole namespace. An
[Identity](../../security-and-networking/security/spec.md#identityspec) stands for a client outside funcd, which
reaches the Buckets through the S3 gateway with the key pair that funcd writes into the Identity's Secret.

The smallest built-in role that allows writing is `Blob Data Writer`, which allows reading and writing objects. To
give it on one prefix, an assignment's scope has the kind `BucketPrefix` and the name `<bucket>/<prefix>`. No scope
covers one Bucket as a whole: a RolesAssignment names each prefix in an entry of its own, or covers the whole namespace
with the scope kind `Namespace`. The first example's folder holds this RolesAssignment, which gives `s3-roundtrip`
write access to the prefix `shared` of the Bucket `lakehouse`:

::include[../../../../../api/types/v1alpha1/examples/bucket/writer-role.yaml#rolesassignment]

A principal that reads a prefix without binding it needs a role as well, such as `Blob Data Reader`, which allows
reading objects. A Function makes such a read through the S3 gateway, since `ctx.blob` reaches only the prefixes that
the Function binds. The built-in roles `Reader`, `Contributor` and `Owner` also cover Bucket data: `Reader` allows
reading objects and KV keys, and `Contributor` and `Owner` allow reading and writing both. A Role that an operator
defines can grant `s3::read` or `s3::write` too. The Security reference [lists the built-in
roles](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref) and [explains how
assignments pair each role with a scope](../../security-and-networking/security/spec.md#rolesassignmentspecassignments).

funcd supplies these rules as a built-in policy, which it applies in every namespace, so nothing needs to be written
or installed. A Policy that an operator writes applies beside it, and cannot allow a write that the built-in policy
forbids. In Cedar, the built-in policy reads:

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

A request that the policy denies fails with the HTTP status 403. Through `ctx.blob`, its message names the rule, such
as `cedar: forbidden by policy builtin/s3-single-writer`; through the S3 gateway, its error code is `AccessDenied`.

## Limits and quotas

A Bucket's [`spec.quota`](spec.md#bucketspecquota) and the [`quota`](spec.md#bucketspecprefixesquota) of each of its
prefixes set its limits:

- `maxSize` or `maxBytes` limits the total size of the Bucket, or of one prefix. Set one of the two, not both. Without
  either, there is no size limit. A write into a prefix must fit both the prefix's limit and the Bucket's.
- `maxObjectBytes`, on the Bucket only, limits the size of each object. When it is 0 or not set, the Bucket sets no
  such limit of its own.

A write whose object is larger than `maxObjectBytes` fails with the HTTP status 413, or with the S3 error
`EntityTooLarge`. A write that would take the Bucket or the prefix above its total size fails with the HTTP status
507, or with the S3 error `QuotaExceeded`.

funcd sets two limits of its own, whatever the Bucket sets. A `put` through `ctx.blob` carries at most 64 MiB, and a
larger one fails with the HTTP status 413. The S3 gateway takes at most 1 GiB in one PUT or multipart upload, a limit
that the operator changes with `s3gateway.maxUploadBytes`; a larger upload fails with the S3 error `EntityTooLarge`.
The [Spec](spec.md) tab gives the range of each field.

## Data storage

funcd keeps the objects of every Bucket in its blob store. By default, the blob store is the `blob` folder of funcd's
data directory, `storage.dataDir`, and the objects survive a restart. `blob.dir` names another folder. `blob.target`
keeps the objects in a versioned S3-compatible bucket instead, and funcd refuses to start when that bucket keeps no
versions, unless `blob.allowUnversioned` is `true`. `storage.mode: memory` keeps the objects in memory, where they are
lost when funcd stops, and funcd then ignores the `blob` keys.

## Changes and deletion

funcd refuses to delete a Bucket, with the HTTP status 409, while a binding of a Function names the Bucket, or while
one of its prefixes holds objects. It refuses in the same way an update that removes a prefix that a binding names or
that holds objects. To delete a Bucket, remove its bindings and delete its objects first.

A Bucket that a Function declares by `name` stays, with its objects, when the Function is deleted. A new Function of
the same name does not own that Bucket: if it declares the Bucket by `name`, it waits in `Pending` with the reason
`StoreNotOwned`, so it declares the Bucket by `ref` instead. With `deletion: delete` on the entry under
`spec.buckets`, funcd deletes the Bucket and its objects after the Function is deleted.

## Run it locally

`funcdctl dev` keeps the objects in memory, so they are lost when `funcdctl dev` stops, whatever
[Data storage](#data-storage) says about a server. With the `--persist` flag, `funcdctl dev` keeps the KV, blob and
metastore data between runs, in the folder `.funcd-dev` of the current directory, or in the folder that `--persist-to`
names.

`funcdctl dev` differs from a platform in two more ways that matter for a Bucket:

- It always runs the S3 gateway, on a free port that it chooses, or on the port that `--s3port` sets. Its banner
  prints the gateway's address and, under S3 CREDENTIALS, the key pair of the folder's first Function, as `export`
  lines. An S3 client that runs with those variables, such as the aws CLI, reads the prefixes that this Function
  binds. `funcdctl dev <dir> --s3port <port> --print-env` prints the same lines without starting the platform, for a
  session started with the same `--s3port`.
- It gives each Function of the folder a role that allows writing every prefix of the namespace's Buckets, which a
  platform does not give. A write that succeeds locally can therefore fail with the HTTP status 403 once deployed, as
  the first example's `denied` shows. Reading still needs a binding or a role.

The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): objects
  written and read with the AWS SDK through the S3 gateway, a write that the policy refuses on a platform, and a write
  that a role allows.
