---
title: Lake
description: Declare a Lake over a Bucket prefix, bind it to a Function, and create and query its tables with SQL from the handler.
---

## Usage

A Lake is a SQL database whose tables a Function creates and queries with SQL, and whose data files funcd keeps in a
Bucket. A Function queries a Lake that it binds through the Lake's gateway. See
[how a Lake works](../../../platform/services/lake.md) for the concept.

The lake-quack example, in Python only, creates a table in a Lake and reads it back. Its Function, `lake-reader`,
takes a SQL statement, runs it on the Lake, and returns the rows of the result.

Install Git, curl and the [dev build of funcdctl](../../../get-started/install.md); the dev build includes the DuckDB
engine that serves a Lake under `funcdctl dev`. Put Python 3.12 or later on the `PATH` as `python3`, with its standard
`venv` module. The steps install the Python package `duckdb` from PyPI, so they need access to it.

`funcdctl dev` starts a local platform, applies the resource files of one folder, and prints the URL of each Function,
on the port that `--gport` sets. It runs the Function from its code in the folder, `src/main.py`, not from the
manifest's `image`, the bundle that `funcdctl push` writes for a platform to run. It uses the `python3` that it finds
first on the `PATH`, unless the environment variable `FUNCD_PYTHON` names another interpreter. It serves the Lake with
its own DuckDB engine, a process on the local computer, and keeps the Lake's data in memory and in a temporary folder,
so the table is lost when it stops; [Run it locally](#run-it-locally) explains how to keep it.

### Python

Clone the repository, create a virtual environment in its root folder, install the `duckdb` package in it, and start
the example with the environment's `python3` first on the `PATH`. The engine of `funcdctl dev` runs DuckDB 2.0.0, so
the steps install the same version of the package:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ python3 -m venv .venv
$ .venv/bin/pip install duckdb==2.0.0
$ PATH="$PWD/.venv/bin:$PATH" funcdctl dev examples/lake-quack --gport 8080
```

The banner lists the service `lake` under SERVICES, with the Lake's name after `serves:`, and the Function's URL under
FUNCTIONS. In another terminal, create the table `readings` in the Lake, then read it back:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"sql":"CREATE TABLE lakehouse.readings AS SELECT * FROM (VALUES (1, 19), (2, 21)) AS t(sensor, celsius)"}' http://127.0.0.1:8080/function/lake-reader
{"rows":[[2]]}
$ curl -s -X POST -H 'content-type: application/json' -d '{"sql":"SELECT sensor, celsius FROM lakehouse.readings ORDER BY sensor"}' http://127.0.0.1:8080/function/lake-reader
{"rows":[[1,19],[2,21]]}
```

The first response holds the number of rows that the statement wrote, and the second holds the table's rows, each as a
list of its columns' values: the Lake keeps the table between the two calls.

The engine attaches the Lake's catalog under the name `lakehouse`, which is the same for every Lake, so a statement
names a table of the Lake as `lakehouse.<table>`. That name has nothing to do with the example's Bucket, which happens
to be called `lakehouse` too. A table named without that prefix is created in the engine's own memory, outside the
Lake, and is lost when the engine stops.

Press Ctrl-C in the first terminal to stop the example.

The example's folder holds four resource files, which `funcdctl dev` applies:

- `secret.yaml` defines the Secret `lake-quack-token`. Its key `QUACK_TOKEN` holds the token that the Lake's engine
  accepts; on a platform, the engine does not start without it. The Lake's gateway puts that token in place of the
  caller's own token once it allows the caller, so no caller holds it.
- `configmap.yaml` defines the ConfigMap `lake-engine-config`, which sets the engine's memory limit and its number of
  threads with the keys `DUCKDB_MEMORY_LIMIT` and `DUCKDB_THREADS`. The engine of `funcdctl dev` does not apply them,
  as [Run it locally](#run-it-locally) says.
- `lake.yaml` defines the Lake `lake`. It declares the Bucket `lakehouse` by `name` under `spec.buckets`, so funcd
  creates the Bucket, and the Lake becomes the owner of the Bucket's prefix `gold`. It declares the ConfigMap by `ref`
  and the Secret by `name`, with the key that the engine reads, and binds all three under
  [`spec.bindings`](spec.md#lakespecbindings). [`spec.location`](spec.md#lakespeclocation) names the prefix that
  holds the Lake's files: the Lake must bind that prefix under `spec.bindings.blob`, and must own it. `spec.ingress`
  exposes the Lake to clients outside funcd, as [Access](#access) explains; the first example does not use it.
- `consumer.yaml` defines the Function `lake-reader`. It declares the Lake by `ref` under
  [`spec.lakes`](../../workloads/function/spec.md#functionspeclakes), and binds it under
  [`spec.bindings.lake`](../../workloads/function/spec.md#functionspecbindingslake) with the alias `lake`. The binding
  lets the Function query the Lake, and funcd starts the Function's worker only once the Lake is `Ready`.

`funcdctl dev` skips the folder's `funcdconfig.yaml`, a configuration of the funcd server that the example uses on a
platform.

The Lake `lake`, in `lake.yaml`, without the file's opening comment
([the complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/lake.yaml)):

::include[../../../../../_repos/funcd-python/examples/lake-quack/lake.yaml#lake]

The Function `lake-reader`, in `consumer.yaml`, without the file's opening comment
([the complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/consumer.yaml)):

::include[../../../../../_repos/funcd-python/examples/lake-quack/consumer.yaml#manifest]

For each Lake binding, funcd gives the Function's worker two environment variables, named after the alias:
`FUNCD_LAKE_LAKE_URL` holds the address of the Lake's gateway, and `FUNCD_LAKE_LAKE_TOKEN` holds a token of the
Function's own, which the gateway checks. A Python handler receives its context as its first argument, named `context`
in the example, and [`context.lake.get("lake")`](spec.md#handler-api) returns the same two values, as `url` and
`token`. The handler opens a DuckDB database in memory, loads DuckDB's `quack` extension, and runs the call's SQL on
the Lake with `quack_query`, which sends the statement and the token to the gateway and returns the rows.

The handler needs the `duckdb` package. On a platform, the Function's bundle carries it, with the `quack` extension.
Under `funcdctl dev`, the virtual environment holds it, and `funcdctl dev` points the handler at the extensions of its
engine through `DUCKDB_EXTENSION_DIRECTORY`.

In this excerpt of `src/main.py`
([the complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/src/main.py)), `FuncInput`
and `FuncOutput` describe the call's data and its response, and `_quack_uri` adds the `quack://` scheme to the
gateway's address:

::include[../../../../../_repos/funcd-python/examples/lake-quack/src/main.py#handler]

A statement that the engine cannot run raises an error in the handler. The example's handler does not catch it, so
the call to the Function fails, and its response holds the engine's message.

## Access

A Function needs no role assignment to query the Lakes that it binds (`lake::query`). Each entry of
`spec.bindings.lake` lets the Function query that Lake, and a binding can name only a Lake that the Function declares
under `spec.lakes`. The Lake's gateway checks the caller when a session opens: it finds the caller from the token that
the session's first request carries, refuses a caller from another namespace, and asks Cedar whether the caller may
perform `lake::query` on the Lake. It forwards the later requests of the session to the engine without a new check.
The [Security](../../../platform/security-and-networking/security.md) page explains where funcd asks Cedar.

Cedar decides for the Lake as a whole, not for its tables or its statements: a caller that may query a Lake can run
any statement that the engine accepts, a statement that writes included, as the first example does.

Any other caller needs a role, or a permit in a Policy that an operator writes. A client outside funcd queries a Lake
as an [Identity](../../security-and-networking/security/spec.md#identityspec), with the token that funcd writes into
the Identity's Secret under the key `lakeToken`. It reaches the Lake's gateway through the edge, at the address that
the Lake's [`spec.ingress`](spec.md#lakespecingress) exposes: `/lake/<name>` by default, or `/` when `host` is set,
and only while `enabled` is `true`. The smallest built-in role that allows a query is `Lake Query Reader`. Its scope
has the kind `Lake` and the Lake's name, or the kind `Namespace` for every Lake of the namespace. This RolesAssignment
gives the Identity `analyst` the role on the Lake `lake` of the first example:

::include[../../../../../api/types/v1alpha1/examples/lake/reader-role.yaml#rolesassignment]

The built-in role `Lake Contributor` allows the same action, `lake::query`, and a Role that an operator defines can
grant it too. The built-in roles `Reader`, `Contributor` and `Owner` cover Bucket and KV data only, not a Lake. The
Security reference [lists the built-in
roles](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref) and [explains how
assignments pair each role with a scope](../../security-and-networking/security/spec.md#rolesassignmentspecassignments).

funcd applies the binding rule as a built-in policy in every namespace, so nothing needs to be written or installed.
A [Policy](../../security-and-networking/security/index.md#write-a-policy) that an operator writes applies beside it:
it can permit a caller, and a `forbid` statement in it denies a caller that a binding allows. In Cedar, the built-in
policy reads:

```cedar title="Built-in Lake policy"
@id("lake-query-binding")
permit(principal, action == Action::"lake::query", resource)
  when { principal has lakeBindings && principal.lakeBindings.contains(resource) };
```

When Cedar denies the caller, the gateway answers the session's first request with the HTTP status 403, and the
engine never sees that request. In the first example's handler, such a query raises an error that reads
`IO Error: Failed to send message: Forbidden`. Bind the Lake, or give the caller a role, as above.

The Lake's engine has an identity of its own, the Lake. It reads the Bucket prefixes that the Lake binds under
`spec.bindings.blob`, and writes only a prefix that the Lake owns. funcd reads each Secret that the Lake binds as the
Lake, so a Policy that forbids that read holds the Lake in `Pending`, with the reason `BindingResolveFailed`.

## Limits and quotas

- A Lake has no quota of its own. Its files count toward the quota of the prefix that `spec.location` names, and of
  its Bucket, as the [Bucket](../bucket/index.md#limits-and-quotas) page describes. A statement that needs a new file
  past that quota fails. Queries, drops and catalog saves go on, because the catalog is kept outside the Bucket.
- The catalog can grow to `lake.catalogMaxSize` in funcd's configuration: 256 MiB by default, or
  `s3gateway.maxUploadBytes` when that is smaller. A save past that size fails, and the Lake shows the condition
  `CatalogSaved` with the status `False`, as [Status](#status) explains.
- [`spec.resources`](spec.md#lakespecresources) caps the engine's CPU and memory, on a platform whose runtime is
  containerd.
- A ConfigMap or a Secret that the Lake binds sets DuckDB's own limits with three keys: `DUCKDB_MEMORY_LIMIT`,
  `DUCKDB_THREADS` and `DUCKDB_MAX_TEMP_DIRECTORY_SIZE`, the size that the engine may spill to disk. funcd leaves out
  `DUCKDB_EXTENSION_DIRECTORY`, and the engine refuses any other key that starts with `DUCKDB_`, as [Status](#status)
  describes.
- On a platform, each running engine gets a scratch area on the filesystem of funcd's data directory, for a copy of
  its catalog and for its spills. Its size is `lake.scratchSize`, which defaults to 1 GiB, or to twice
  `lake.catalogMaxSize` plus 512 MiB when that is larger. A Lake whose scratch does not fit stays `Pending`, with the
  reason `ScratchSpaceLacking`.

[funcd's configuration](../../running-funcd/spec.md#funcdconfigyaml) describes the `lake.` settings, and the Lake's
[Spec](spec.md) tab gives the range of each field.

## Data storage

A Lake keeps its tables in two parts:

- Its files, Parquet files that the engine writes, are objects under the prefix that `spec.location` names.
- Its catalog, which records the tables and their files, is a DuckDB file. funcd keeps it in its catalog store, outside
  every Bucket, under the same location: a listing of the Bucket does not show it, and the prefix's quota does not
  count it.

DuckLake, the table format that the engine uses, keeps the rows of a small write in the catalog instead of a file, as
it does with the two rows of the first example. Such rows count toward `lake.catalogMaxSize`, not toward the prefix's
quota.

funcd keeps both parts in its blob store, with the objects of the Buckets, so the Bucket page's
[Data storage](../bucket/index.md#data-storage) says where they are and whether they survive a restart. A Lake located
where an earlier Lake was, on a prefix that it owns, finds that Lake's files and catalog. The engine reads and writes
the files through funcd's S3 gateway, which the operator turns on with `s3gateway.enabled: true`.

The engine works on a copy of the catalog, and saves the copy to the catalog store:

- When `lake.saveInterval` is not set, the default, each answer waits until a save covers the statements committed
  before it. A statement whose answer arrived therefore survives a crash of the engine. A save copies and uploads the
  whole catalog, so the wait grows with the catalog.
- When `lake.saveInterval` is set, from 500ms to 1h, the engine answers at once and saves at that interval. A crash can
  then lose the statements committed in one interval and during the two saves around it.

While saves fail, each answer that waits for a save fails, and so does each request after the unsaved statement,
until a save succeeds.

A Backup copies a Lake as a member of its own, its files and its catalog together. A BackupSchedule names the Lake
under `scope.lakes`, and one that names the Lake's Bucket copies the Lake too, as a separate member. The
[Disaster recovery](../../../platform/disaster-recovery.md) page explains backups and restores, and its
[Spec](../../disaster-recovery/spec.md#backupschedulespecscope) tab describes the scope.

## Status

`funcdctl get lake lake -n default -o json` prints the phase and the conditions of the first example's Lake under
`status`, and `funcdctl get function lake-reader -n default -o json` those of its Function. With `funcdctl dev`, add
the `--server` and `--token` flags that its banner prints. A Lake's `status.endpoint` holds the address of its gateway
while it is `Ready`.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| Lake `Ready` | | The engine answers the readiness check. |
| Lake `Pending` | `EngineNotReady` | The engine starts, does not answer the readiness check, or is being replaced. On a platform, funcd starts an engine that stops as it starts again every `lake.enginePollInterval`, 2 seconds by default. Only funcd's log names the cause, such as a `DUCKDB_` key that the engine refuses, `<key> is not an engine setting a Lake may set`, or a missing `QUACK_TOKEN`. |
| Lake `Pending` | `BucketNotFound` | A Bucket or a prefix that the Lake binds or names in `spec.location` does not exist. |
| Lake `Pending` | `PrefixNotOwned` | The prefix of `spec.location` has no owner, or another owner than the Lake. |
| Lake `Pending` | `StoreNotOwned` | A Bucket or a ConfigMap that the Lake declares by `name` exists, and is not the Lake's. |
| Lake `Pending` | `SecretNotFound` or `SecretKeyMissing` | A Secret that the Lake declares is missing, or lacks a declared key. |
| Lake `Pending` | `BindingResolveFailed` | A ConfigMap that the Lake binds is missing, or a Policy forbids the Lake to read a Secret that it binds. For a Lake that a Function declares, a missing Secret gives this reason too. |
| Lake `Pending` | `ScratchSpaceLacking` | The engine's scratch area does not fit, as [Limits and quotas](#limits-and-quotas) says. |
| Lake `Pending` | `GatewayEndpointNotAnAddress` | The S3 gateway's endpoint in funcd's configuration is a host name, not an IP address and a port. |
| Lake `Pending` | `EngineConvergeFailed` | funcd could not create the engine. |
| Lake `Pending` | `Restoring` | A Restore loads the Lake's files and catalog in place. |
| Lake `Pending` | `LakeEngineNotBundled` | Under `funcdctl dev` only: this funcdctl has no Lake engine, as [Run it locally](#run-it-locally) says. |
| `CatalogSaved` condition | `Saved` or `SavesFailing` | On a platform, `True` with `Saved` after a save of the catalog, and `False` with `SavesFailing` while saves fail. It leaves the phase `Ready`. |
| `IngressReady` condition | `Programmed`, `Disabled` or `LakeNotReady` | Only for a Lake that sets `spec.ingress`: `True` with `Programmed` once the edge serves the Lake, and `False` otherwise, such as with `Disabled` while `enabled` is not `true`, or `LakeNotReady` while the Lake is not `Ready`. |
| Function `Pending` | `LakeNotReady` | A Lake that the Function binds is not `Ready`. The Function starts its worker once it is. |
| Any other reason | | The message of the `Ready` condition gives the cause. See also the [Function's Status](../../workloads/function/index.md#status). |

## Changes and deletion

- On a platform, funcd replaces the Lake's engine when a setting that the engine starts with changes:
  `spec.location`, `spec.resources`, or a value that the engine gets from the ConfigMaps and Secrets that the Lake
  binds. funcd sees a changed value of a ConfigMap or a Secret within `runtime.supervisionPeriod`, 10 seconds by
  default. It also replaces the engine for a new S3 key pair, between 80% and 90% of `s3gateway.keyRotationPeriod`,
  24 hours by default. A change to `spec.ingress`, to a quota or to a `deletion` setting keeps the running engine.
- During a replacement, the old engine lets the running queries end for up to 5 seconds while it goes on saving,
  cancels the queries that still run, and saves its catalog a last time. Until the new engine answers, the Lake is
  `Pending` with the reason `EngineNotReady`, and its gateway refuses each request. Once the Lake is `Ready`, send a
  refused query or a cancelled read again. Before you send a cancelled write again, check whether it applied: the last
  save can keep a write whose answer never arrived.
- funcd does not refuse to delete a Lake that holds tables or that a Function binds. The Lake's engine stops. A
  Function that binds the Lake keeps its worker, its `RevisionReady` condition gets the reason `LakeNotReady`, and
  the gateway refuses its queries. While a Function binds the Lake, its gateway keeps the address, so a Lake applied
  again under the same name serves the Function at the same address.
- A Function can also declare a Lake by `name` under `spec.lakes`, in place of a `ref`. funcd then creates the Lake
  with the Function, and deletes it with the Function.

The `deletion` setting of the Lake's entry under `spec.buckets` decides what happens to a Bucket that the Lake declares
by `name`:

- With `retain`, the default, the Bucket stays, with the Lake's files and its catalog. To use them again, apply a Lake
  of the same name at the same location, which declares the Bucket by `ref`. The owner of the prefix is the Lake's
  name, so a Lake of another name waits in `Pending` with the reason `PrefixNotOwned`. A Lake of the same name that
  declares the Bucket by `name` waits with the reason `StoreNotOwned`, since the Bucket still belongs to the deleted
  Lake. funcd refuses a request to delete the Bucket, or to remove the prefix from it, while the prefix holds objects
  or a Lake's catalog. Through the S3 gateway, that catalog is the object `<bucket>/<prefix>/_ducklake/catalog.db` of
  the S3 bucket `funcd.lake-catalogs`, such as `lakehouse/gold/_ducklake/catalog.db`. Once no Lake is located there, a
  principal that may write the prefix can delete that object.
- With `delete`, funcd deletes the Bucket after the Lake, with its objects and the catalog of each Lake located on its
  prefixes.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |

## Run it locally

`funcdctl dev` serves each Lake of its folder with the DuckDB engine that the release dev build of funcdctl contains,
which it starts as a process of the local user. A funcdctl built from source without that engine leaves each Lake
`Pending`, with the reason `LakeEngineNotBundled`, and logs
`lake engine not embedded in this dev build — lake unavailable`: install the
[dev build of funcdctl](../../../get-started/install.md) instead.

That engine differs from a platform's engine:

- It writes the Lake's files through the S3 gateway of `funcdctl dev`, into the Lake's prefix, and keeps the catalog in
  a local file. It answers each statement at once, without waiting for a save.
- It applies none of the `DUCKDB_` keys, so the first example's engine runs with DuckDB's default memory limit and
  number of threads, not with the ConfigMap's.
- It does not confine queries: a query can reach the files of the local computer, which a platform's engine refuses.
  It runs without a sandbox and without a limit on its CPU or memory.

The catalog's file is in a temporary folder, so the Lake's tables are lost when `funcdctl dev` stops. With
`--persist`, `funcdctl dev` keeps each Lake's catalog in the `lake` folder of `.funcd-dev` in the current folder, or of
the folder that `--persist-to` names, and the Lake's files with the blob data there. The next run with `--persist`
finds the tables again. Deleting that folder after `funcdctl dev` stops clears all the state that the local platform
stored: its resources and its KV, blob, SQL, lake and volume data. The [reference of
`funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [lake-quack in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/lake-quack): a Lake that keeps its
  files in a Bucket that it declares, and a Function that runs SQL on it.
