---
title: Lake
description: Declare a Lake over a Bucket prefix, bind it to a Function, and create and query its tables with SQL from the handler.
---

## Usage

A [Lake](../../../platform/services/lake.md) is a SQL database whose data files funcd keeps in a Bucket. A Function
that binds it creates and queries its tables with SQL through the Lake's gateway.

The Python example lake-quack runs the SQL of each call on a Lake and returns the rows. Install Git, curl and the
[dev build of funcdctl](../../../get-started/install.md), which includes the DuckDB engine that serves a Lake, and
Python 3.12 or later as `python3`, with `venv`. The steps install the `duckdb` package from PyPI. Under `funcdctl dev`,
the Lake's data is lost at stop, unless [`--persist`](#run-it-locally) keeps it.

### Python

The engine runs DuckDB 2.0.0, so install the same version of the package:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python
$ python3 -m venv .venv
$ .venv/bin/pip install duckdb==2.0.0
$ PATH="$PWD/.venv/bin:$PATH" funcdctl dev examples/lake-quack --gport 8080
```

Once the banner lists the service `lake`, create a table from another terminal and read it back:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"sql":"CREATE TABLE lakehouse.readings AS SELECT * FROM (VALUES (1, 19), (2, 21)) AS t(sensor, celsius)"}' http://127.0.0.1:8080/function/lake-reader
{"rows":[[2]]}
$ curl -s -X POST -H 'content-type: application/json' -d '{"sql":"SELECT sensor, celsius FROM lakehouse.readings ORDER BY sensor"}' http://127.0.0.1:8080/function/lake-reader
{"rows":[[1,19],[2,21]]}
```

Name a Lake's tables `lakehouse.<table>`, the catalog's name in every Lake: a table without that prefix lives in the
engine's memory and is lost when it stops. Press Ctrl-C in the first terminal to stop.

The Lake, in `lake.yaml` ([complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/lake.yaml)),
declares the Bucket `lakehouse` by `name`, so funcd creates it and the Lake owns its prefix `gold`.
[`spec.location`](spec.md#lakespeclocation) names that prefix, which the Lake must bind and own. It also binds a
ConfigMap of engine settings and the Secret `lake-quack-token`, whose `QUACK_TOKEN` the engine needs on a platform; the
gateway puts that token in place of an allowed caller's, so no caller holds it:

::include[../../../../../_repos/funcd-python/examples/lake-quack/lake.yaml#lake]

The Function, in `consumer.yaml`
([complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/consumer.yaml)), declares the Lake
under [`spec.lakes`](../../workloads/function/spec.md#functionspeclakes) and binds it under
[`spec.bindings.lake`](../../workloads/function/spec.md#functionspecbindingslake); funcd starts its worker once the Lake
is `Ready`:

::include[../../../../../_repos/funcd-python/examples/lake-quack/consumer.yaml#manifest]

The worker gets the gateway's address and a token of its own in `FUNCD_LAKE_<ALIAS>_URL` and
`FUNCD_LAKE_<ALIAS>_TOKEN`, which [`context.lake.get("lake")`](spec.md#handler-api) also returns. The handler sends the
SQL through DuckDB's `quack` extension; on a platform the Function's bundle carries `duckdb` with it, and under
`funcdctl dev` the virtual environment does. An excerpt of
[`src/main.py`](https://github.com/pyvvo/funcd-python/blob/main/examples/lake-quack/src/main.py):

::include[../../../../../_repos/funcd-python/examples/lake-quack/src/main.py#handler]

A statement that the engine rejects raises an error; uncaught, it fails the call with the engine's message.

## Access

- A Function queries the Lakes that it binds (`lake::query`) with no role; a binding names only a Lake that the
  Function declares.
- The gateway checks a caller when a session opens: it reads the caller's token, refuses another namespace, and asks
  Cedar for `lake::query`. It passes the session's later requests to the engine without a new check.
- Cedar decides for the whole Lake: an allowed caller can run any statement, writes included.
- A client outside funcd queries as an [Identity](../../security-and-networking/security/spec.md#identityspec), with the
  token in its Secret's `lakeToken`, through the edge at the address of
  [`spec.ingress`](spec.md#lakespecingress), `/lake/<name>` by default or `/` with a `host`, while `enabled` is `true`.
  It needs a role:
  [`Lake Query Reader`](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref) or
  `Lake Contributor`, scoped to a `Lake` or the `Namespace`. `Reader`, `Contributor` and `Owner` do not cover a Lake.

::include[../../../../../api/types/v1alpha1/examples/lake/reader-role.yaml#rolesassignment]

funcd applies the binding rule as a built-in policy in every namespace. An operator's
[Policy](../../security-and-networking/security/index.md#write-a-policy) can permit a caller, and its `forbid` denies
even a bound one:

```cedar title="Built-in Lake policy"
@id("lake-query-binding")
permit(principal, action == Action::"lake::query", resource)
  when { principal has lakeBindings && principal.lakeBindings.contains(resource) };
```

A denied session's first request gets 403 and never reaches the engine; in the example's handler, it raises
`IO Error: Failed to send message: Forbidden`.

The Lake's engine acts as the Lake: it reads the prefixes that the Lake binds, and writes only the one that it owns.
funcd reads the Lake's Secrets as the Lake, so a Policy that forbids that read holds the Lake in `Pending` with
`BindingResolveFailed`.

## Limits and quotas

- A Lake's files count toward the quota of its location's prefix and Bucket; a statement that needs a new file past it
  fails, while queries, drops and catalog saves go on.
- The catalog grows up to [`lake.catalogMaxSize`](../../running-funcd/spec.md#funcdconfigyaml), 256 MiB by default or
  `s3gateway.maxUploadBytes` if smaller; a save past it fails and sets `CatalogSaved` to `False`.
- [`spec.resources`](spec.md#lakespecresources) caps the engine's CPU and memory on containerd. A bound ConfigMap or
  Secret sets `DUCKDB_MEMORY_LIMIT`, `DUCKDB_THREADS` and `DUCKDB_MAX_TEMP_DIRECTORY_SIZE`; the engine refuses any
  other `DUCKDB_` key.
- On a platform, each engine gets a scratch area of `lake.scratchSize`, by default 1 GiB or twice
  `lake.catalogMaxSize` plus 512 MiB, whichever is larger; when it does not fit, the Lake waits with
  `ScratchSpaceLacking`.

## Data storage

A Lake keeps its tables in Parquet files under its location's prefix, and its catalog in a DuckDB file of funcd's
catalog store, outside every Bucket, where no listing shows it and no quota counts it. DuckLake keeps the rows of a
small write in the catalog, as in the example. Both live in funcd's [blob store](../bucket/index.md#data-storage), and
the engine reaches the files through the S3 gateway. A Lake located where an earlier one was, on a prefix that it owns,
finds its files and catalog.

The engine saves its copy of the catalog to the store:

- By default, each answer waits for a save that covers it, so an answered statement survives a crash; the wait grows
  with the catalog.
- With `lake.saveInterval`, from 500ms to 1h, answers come at once and saves at that interval; a crash can lose the
  statements committed in one interval and during the two saves around it.

While saves fail, the answers that wait for one fail, and so does each later request, until a save succeeds. A Backup
copies a Lake, files and catalog together; a BackupSchedule names it under
[`scope.lakes`](../../disaster-recovery/spec.md#backupschedulespecscope).

## Status

`funcdctl get lake lake -n default -o json` and `funcdctl get function lake-reader -n default -o json` print the
status; with `funcdctl dev`, add the `--server` and `--token` flags of its banner. While `Ready`, `status.endpoint`
holds the gateway's address. `funcdctl logs lake/lake -n default` prints what its engine wrote, an `EngineNotReady`'s
cause among it.

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

- On a platform, funcd replaces the engine when `spec.location`, `spec.resources` or a value from a bound ConfigMap or
  Secret changes, seen within `runtime.supervisionPeriod`, and for a new S3 key pair, between 80% and 90% of
  `s3gateway.keyRotationPeriod`. Other changes keep the engine.
- During a replacement, running queries get up to 5 seconds, the rest are cancelled, and the catalog is saved; the
  Lake is `Pending` and its gateway refuses requests. Once it is `Ready`, send a refused query or a cancelled read
  again, but check first whether a cancelled write applied.
- Deleting a Lake is not refused, even with tables. Its engine stops, and the Functions that bind it keep their worker
  with `LakeNotReady` on `RevisionReady`; a Lake applied again under the same name serves them at the same address.
- A Function that declares a Lake by `name` creates and deletes it with itself.

For a Bucket that the Lake declares by `name`, the entry's `deletion` decides:

- `retain`, the default, keeps the Bucket, the files and the catalog. Reuse them with a Lake of the same name at the
  same location that declares the Bucket by `ref`; another name waits with `PrefixNotOwned`, and a `name` declaration
  with `StoreNotOwned`. funcd refuses to delete the Bucket or the prefix while it holds objects or a catalog, the S3
  object `<bucket>/<prefix>/_ducklake/catalog.db` of `funcd.lake-catalogs`, which a writer of the prefix can delete
  once no Lake is there.
- `delete` deletes the Bucket after the Lake, with its objects and catalogs.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. With Docker or nerdctl, `funcdctl dev --containers` needs no Python. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. With Docker or nerdctl, `funcdctl dev --containers` needs no Python. |

## Run it locally

`funcdctl dev` serves each Lake with the DuckDB engine of the release dev build, as a local process. A funcdctl built
from source without it leaves each Lake `Pending` with `LakeEngineNotBundled`. Its engine differs from a platform's:

- It keeps the catalog in a local file and answers each statement at once, without waiting for a save.
- It applies none of the `DUCKDB_` keys.
- It does not confine queries, which can reach the local computer's files, and runs with no sandbox or limits.

The catalog is lost at stop. With [`--persist`](../../tools/funcdctl/spec.md#funcdctl-dev), it is kept in
`.funcd-dev/lake` and the files with the blob data, so the next run finds the tables.

## Examples

- [lake-quack in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/lake-quack): a Lake over a Bucket
  that it declares, and a Function that runs SQL on it.
