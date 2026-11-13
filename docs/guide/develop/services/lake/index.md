---
title: Lake
description: Declare a Lake over a Bucket prefix, bind it to a Function, and create and query its tables with SQL from the handler.
---

## Usage

A Lake is a SQL database whose tables live in a Bucket: a DuckDB engine writes the tables' data as Parquet files under
a prefix of the Bucket, and records the tables in a catalog, which also keeps the rows of a small write. A Function
queries a Lake that it binds, through the Lake's gateway, which checks the caller when the caller opens a session,
before the engine runs any of its statements. The [Platform page](../../../platform/services/lake.md) explains how a
Lake works inside funcd; this page shows how to use one.

The first example, lake-quack, exists in Python only. It creates a table in a Lake and reads it back. Its Function,
`lake-reader`, takes a SQL statement, runs it on the Lake, and returns the rows of the result. The steps below start
the example on the local computer and call the Function twice: the first call creates the table `readings` with two
rows, and its response contains `"rows":[[2]]`, the number of rows written; the second call reads the table, and its
response contains `"rows":[[1,19],[2,21]]`.

The example needs these programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command and the DuckDB engine that serves a Lake under it.
- git, which copies the example's repository.
- curl, which calls the Function.
- Python 3.12 or later as `python3`, with its standard `venv` module, which runs the Function's code.

The steps also install the Python package `duckdb` from PyPI, so they need access to it.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. The example's Function names
an `image`, the bundle that `funcdctl push` writes for a platform to run. `funcdctl dev` does not use that image: it
runs the Function from its code in the folder, `src/main.py`, with the `python3` that it finds first on the `PATH`,
unless the environment variable `FUNCD_PYTHON` names another interpreter. It serves the Lake with its own DuckDB
engine, a process on the local computer, and prints the URL of each Function. The `--gport` flag sets the port of that
URL, so the commands below use port 8080. `funcdctl dev` keeps the Lake's data in memory and in a temporary folder, so
the table is lost when it stops; [Run it locally](#run-it-locally) explains how to keep it.

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

The folder also holds `funcdconfig.yaml`, a configuration of the funcd server that the example uses on a platform,
which `funcdctl dev` skips.

For each Lake binding, funcd gives the Function's worker two environment variables, named after the alias:
`FUNCD_LAKE_LAKE_URL` holds the address of the Lake's gateway, and `FUNCD_LAKE_LAKE_TOKEN` holds a token of the
Function's own, which the gateway checks. The handler reads both variables. A Python handler receives its context as its
first argument, named `context` in the example, and [`context.lake.get("lake")`](spec.md#handler-api) returns the same
two values, as `url` and `token`. The handler then opens a DuckDB database in memory, loads DuckDB's `quack` extension,
and runs the call's SQL on the Lake with `quack_query`, which sends the statement and the token to the gateway and
returns the rows.

The handler needs the `duckdb` package. On a platform, the Function's bundle carries it, with the `quack` extension.
Under `funcdctl dev`, the steps below install it in a virtual environment, and `funcdctl dev` points the handler at the
extensions of its engine through `DUCKDB_EXTENSION_DIRECTORY`. That engine runs DuckDB 2.0.0, so the steps install the
same version of the package.

The engine attaches the Lake's catalog under the name `lakehouse`, which is the same for every Lake, so a statement
names a table of the Lake as `lakehouse.<table>`. That name has nothing to do with the example's Bucket, which happens
to be called `lakehouse` too. A table named without that prefix is created in the engine's own memory, outside the
Lake, and is lost when the engine stops.

The [Spec](spec.md) tab lists every field of a Lake.

### Python

The Lake `lake`, in `lake.yaml`:

::include[../../../../../_repos/funcd-python/examples/lake-quack/lake.yaml]

The Function `lake-reader`, in `consumer.yaml`, declares and binds the Lake:

::include[../../../../../_repos/funcd-python/examples/lake-quack/consumer.yaml]

The handler, in `src/main.py`, reads the binding's address and token, and runs the SQL of the call on the Lake:

::include[../../../../../_repos/funcd-python/examples/lake-quack/src/main.py]

Clone the repository, create a virtual environment in its root folder, install the `duckdb` package in it, and start
the example with the environment's `python3` first on the `PATH`:

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

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Access

A Function needs no role assignment to query the Lakes that it binds. Each entry of `spec.bindings.lake` lets the
Function query that Lake, and a binding can name only a Lake that the Function declares under `spec.lakes`. The Lake's
gateway checks the caller when a session opens: it finds the caller from the token that the session's first request
carries, refuses a caller from another namespace, and asks Cedar whether the caller may perform the action
`lake::query` on the Lake. It forwards the later requests of the session to the engine without a new check. The
[Security](../../../platform/security-and-networking/security.md) page explains where funcd asks Cedar.

Cedar decides for the Lake as a whole, not for its tables or its statements: a caller that may query a Lake can run
any statement that the engine accepts, a statement that writes included, as the first example does.

Any other caller needs a role, or a permit in a Policy that an operator writes. A client outside funcd queries a Lake
as an [Identity](../../security-and-networking/security/spec.md#identityspec), with the token that funcd writes into
the Identity's Secret under the key `lakeToken`. It reaches the Lake's gateway through the edge, at the address that
the Lake's [`spec.ingress`](spec.md#lakespecingress) exposes: `/lake/<name>` by default, or `/` when `host` is set,
and only while `enabled` is `true`. The smallest built-in role that allows a query is `Lake Query Reader`. Its scope
has the kind `Lake` and the Lake's name as its name; the scope kind `Namespace` covers every Lake of the namespace. This
RolesAssignment gives the Identity `analyst` the role on the Lake `lake` of the first example:

::include[../../../../../api/types/v1alpha1/examples/lake/reader-role.yaml#rolesassignment]

The built-in role `Lake Contributor` allows the same action, `lake::query`, and a Role that an operator defines can
grant it too. The built-in roles `Reader`, `Contributor` and `Owner` cover Bucket and KV data only, not a Lake. The
Security reference [lists the built-in
roles](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref) and [explains how
assignments pair each role with a scope](../../security-and-networking/security/spec.md#rolesassignmentspecassignments).

funcd supplies the binding rule as a built-in policy, which it applies in every namespace, so nothing needs to be
written or installed. A Policy that an operator writes applies beside it: it can permit a caller, and a `forbid`
statement in it denies a caller that a binding allows. In Cedar, the built-in policy reads:

```cedar title="Built-in Lake policy"
@id("lake-query-binding")
permit(principal, action == Action::"lake::query", resource)
  when { principal has lakeBindings && principal.lakeBindings.contains(resource) };
```

When Cedar denies the caller, the gateway answers the session's first request with the HTTP status 403, and the
engine never sees that request. In the first example's handler, such a query raises an error that reads
`IO Error: Failed to send message: Forbidden`, and the call fails with the HTTP status 500.

The Lake's engine has an identity of its own, the Lake. It reads the Bucket prefixes that the Lake binds under
`spec.bindings.blob`, and writes only a prefix that the Lake owns. funcd reads each Secret that the Lake binds as the
Lake, so a Policy that forbids that read holds the Lake in `Pending`, with the reason `BindingResolveFailed`.

## Limits and quotas

- A Lake has no quota of its own. Its files count toward the quota of the prefix that `spec.location` names, and of
  its Bucket, as the [Bucket](../bucket/index.md#limits-and-quotas) page describes. A statement that needs a new file
  past that quota fails, and its error holds the HTTP status 507. Queries, drops and catalog saves go on, because the
  catalog is kept outside the Bucket.
- The catalog can grow to `lake.catalogMaxSize` in funcd's configuration: 256 MiB by default, or
  `s3gateway.maxUploadBytes` when that is smaller. A save past that size fails, and the Lake shows the condition
  `CatalogSaved` with the status `False`, as [Status](#status) explains.
- [`spec.resources`](spec.md#lakespecresources) caps the engine's CPU and memory, on a platform whose runtime is
  containerd.
- A ConfigMap or a Secret that the Lake binds sets DuckDB's own limits with three keys: `DUCKDB_MEMORY_LIMIT`,
  `DUCKDB_THREADS` and `DUCKDB_MAX_TEMP_DIRECTORY_SIZE`, the size that the engine may spill to disk. funcd leaves out
  `DUCKDB_EXTENSION_DIRECTORY`, and the engine refuses any other key that starts with `DUCKDB_`, as [Errors](#errors)
  describes.
- On a platform, each running engine gets a scratch area on the filesystem of funcd's data directory, for a copy of
  its catalog and for its spills. Its size is `lake.scratchSize`, which defaults to 1 GiB, or to twice
  `lake.catalogMaxSize` plus 512 MiB when that is larger. A Lake whose scratch does not fit stays `Pending`, with the
  reason `ScratchSpaceLacking`.

The funcd [Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes the `lake.` settings, and the Lake's
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
where an earlier Lake was, on a prefix that it owns, finds that Lake's files and catalog.

The engine reads and writes the files through funcd's S3 gateway, which the operator turns on with
`s3gateway.enabled: true`.

The engine works on a copy of the catalog, and saves the copy to the catalog store:

- When `lake.saveInterval` is not set, the default, each answer waits until a save covers the statements committed
  before it. A statement whose answer arrived therefore survives a crash of the engine. A save copies and uploads the
  whole catalog, so the wait grows with the catalog.
- When `lake.saveInterval` is set, from 500ms to 1h, the engine answers at once and saves at that interval. A crash can
  then lose the statements committed in one interval and during the two saves around it.

A Backup copies a Lake as a member of its own, its files and its catalog together. A BackupSchedule names the Lake
under `scope.lakes`, and one that names the Lake's Bucket copies the Lake too, as a separate member. The
[Disaster recovery](../../../platform/disaster-recovery.md) page explains backups and restores, and its
[Spec](../../disaster-recovery/spec.md#backupschedulespecscope) tab describes the scope.

## Status

A Lake is `Pending` until its engine answers the readiness check, then `Ready`. `funcdctl get lake lake -n default -o
json` prints its phase and its conditions under `status`, and `status.endpoint` holds the address of its gateway while
it is `Ready`. With `funcdctl dev`, add the `--server` and `--token` flags that its banner prints.

While the Lake is `Pending`, the reason of its `Ready` condition says what it waits for:

- `EngineNotReady`: the engine starts, or does not answer the readiness check.
- `BucketNotFound`: a Bucket or a prefix that the Lake binds or names in `spec.location` does not exist.
- `PrefixNotOwned`: the prefix of `spec.location` has no owner, or another owner than the Lake.
- `StoreNotOwned`: a Bucket or a ConfigMap that the Lake declares by `name` exists, and is not the Lake's.
- `SecretNotFound` or `SecretKeyMissing`: a Secret that the Lake declares is missing, or lacks a declared key.
- `BindingResolveFailed`: a ConfigMap that the Lake binds is missing, or a Policy forbids the Lake to read a Secret
  that it binds. For a Lake that a Function declares, a missing Secret gives this reason too.
- `ScratchSpaceLacking`: the engine's scratch area does not fit, as [Limits and quotas](#limits-and-quotas) says.
- `GatewayEndpointNotAnAddress`: the S3 gateway's endpoint in funcd's configuration is a host name, not an IP address
  and a port.
- `EngineConvergeFailed`: funcd could not create the engine.
- `Restoring`: a Restore loads the Lake's files and catalog in place.

Two more conditions follow the Lake. On a platform, `CatalogSaved` is `True` with the reason `Saved` after a save of
the catalog, and `False` with the reason `SavesFailing` while saves fail; it leaves the phase `Ready`. `IngressReady`
appears when the Lake sets `spec.ingress`: it is `True`, with the reason `Programmed`, once the edge serves the Lake,
and `False` otherwise, with a reason such as `Disabled` while `enabled` is not `true`, or `LakeNotReady` while the Lake
is not `Ready`.

A Function that binds the Lake waits in `Pending`, with the reason `LakeNotReady`, until the Lake is `Ready`.

## Changes and deletion

On a platform, funcd replaces the Lake's engine when a setting that the engine starts with changes: `spec.location`,
`spec.resources`, or a value that the engine gets from the ConfigMaps and Secrets that the Lake binds. funcd sees a
changed value of a ConfigMap or a Secret within `runtime.supervisionPeriod`, 10 seconds by default. A change to
`spec.ingress`, to a quota or to a `deletion` setting keeps the running engine. funcd also replaces the engine for a
new S3 key pair, between 80% and 90% of `s3gateway.keyRotationPeriod`, 24 hours by default. The old engine lets the
running queries end for up to 5 seconds while it goes on saving, cancels the queries that still run, and saves its
catalog a last time. A cancelled query fails with `INTERRUPT Error: Interrupted: query was cancelled`. Until the new
engine answers, the Lake is `Pending` with the reason `EngineNotReady`, and its gateway answers each request with the
HTTP status 503 and the message `lake not ready`.

funcd does not refuse to delete a Lake that holds tables or that a Function binds. The Lake's engine stops. A
Function that binds the Lake keeps its worker, its `RevisionReady` condition gets the reason `LakeNotReady`, and its
queries get the HTTP status 503 and `lake not ready`. While a Function binds the Lake, its gateway keeps the address,
so a Lake applied again under the same name serves the Function at the same address.

The `deletion` setting of the Lake's entry under `spec.buckets` decides what happens to a Bucket that the Lake declares
by `name`:

- With `retain`, the default, the Bucket stays, with the Lake's files and its catalog. To use them again, apply a Lake
  of the same name at the same location, which declares the Bucket by `ref`. The owner of the prefix is the Lake's
  name, so a Lake of another name waits in `Pending` with the reason `PrefixNotOwned`. A Lake of the same name that
  declares the Bucket by `name` waits with the reason `StoreNotOwned`, since the Bucket still belongs to the deleted
  Lake. funcd refuses a request to delete the Bucket, or to remove the prefix from it, with the HTTP status 409, while
  the prefix holds objects or a Lake's catalog, such as
  `still holds its Lake catalog funcd.lake-catalogs/lakehouse/gold/_ducklake/catalog.db`. Once no Lake is located
  there, a principal that may write the prefix can delete that catalog through the S3 gateway, under that name.
- With `delete`, funcd deletes the Bucket after the Lake, with its objects and the catalog of each Lake located on its
  prefixes.

A Function can also declare a Lake by `name` under `spec.lakes`, in place of a `ref`. funcd then creates the Lake with
the Function, and deletes it with the Function.

## Errors

- `funcdctl apply` refuses a Function whose `spec.bindings.lake` names a Lake that its `spec.lakes` does not declare:
  `spec.bindings.lake[0]: Lake "lake" is not declared in spec.lakes`.
- `funcdctl apply` refuses a Lake whose `spec.location` is not one of its `spec.bindings.blob`:
  `spec.location ("lakehouse"/"gold") must be one of spec.bindings.blob …`.
- A statement that the engine cannot run raises an error in the handler. The first example's handler does not catch
  it, so the call fails with the HTTP status 500, and the response holds the engine's message, such as
  `{"error":"Catalog Error: Table with name no_such_table does not exist! …"}`.
- A caller that Cedar denies is refused with the HTTP status 403 when its session opens, as [Access](#access)
  describes.
- A query to a Lake that is not `Ready`, or that was deleted, is refused with the HTTP status 503 and `lake not ready`.
- While saves of the catalog fail, each answer that waits for a save fails with the HTTP status 503 and
  `lake catalog not saved`, or with a broken connection once the answer has begun. Each request after the unsaved
  statement fails the same way, until a save succeeds.
- On a platform, an engine that stops as it starts leaves the Lake `Pending` with the reason `EngineNotReady`, and
  funcd starts it again every `lake.enginePollInterval`, 2 seconds by default. Only funcd's log names the cause, such
  as a `DUCKDB_` key that the engine refuses, `<key> is not an engine setting a Lake may set`, or a missing
  `QUACK_TOKEN`.

## Run it locally

`funcdctl dev` serves each Lake of its folder with the DuckDB engine that the release dev build of funcdctl contains,
which it starts as a process of the local user. A funcdctl built from source without that engine leaves each Lake
`Pending`, with the reason `LakeEngineNotBundled`, and logs
`lake engine not embedded in this dev build — lake unavailable`.

That engine differs from a platform's engine:

- It writes the Lake's files through the S3 gateway of `funcdctl dev`, into the Lake's prefix, and keeps the catalog in
  a local file. It answers each statement at once, without waiting for a save.
- It applies none of the `DUCKDB_` keys, so the first example's engine runs with DuckDB's default memory limit and
  number of threads, not with the ConfigMap's.
- It does not confine queries: a query can reach the files of the local computer, which a platform's engine refuses.
  It runs without a sandbox and without a limit on its CPU or memory.

The catalog's file is in a temporary folder, so the Lake's tables are lost when `funcdctl dev` stops. With the
`--persist` flag, `funcdctl dev` keeps each Lake's catalog in the `lake` folder of its persist folder, and the Lake's
files with the blob data: `.funcd-dev` in the current folder, unless `--persist-to` names another one. The next run
with `--persist` finds the tables again. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [lake-quack in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/lake-quack): a Lake that keeps its
  files in a Bucket that it declares, and a Function that runs SQL on it.
