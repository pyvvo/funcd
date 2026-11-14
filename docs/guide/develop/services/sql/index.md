---
title: SQL
description: Declare a SQL database, bind it to a Function, and run SQL statements on it from the handler through ctx.sql.
---

## Usage

A SQLDatabase is a SQL database with an engine of its own, rqlite, which keeps the data in SQLite, so the statements
are written in SQLite's dialect of SQL. A Function binds the database, and its handler sends statements to it through
[`ctx.sql`](spec.md#handler-api), the SQL client of the context that every handler receives. See
[how SQL databases work](../../../platform/services/sql.md) for the concept.

The first example creates the tables of a new database from its migrations, adds a row, and reads the rows back: in
TypeScript, sql-notes keeps notes, and in Python, sql-orders keeps orders. Each example runs a migration Function,
which applies the migrations, and the database's owner, which adds and lists the rows. The Python example also runs a
third Function, `audit`, which records each change of an order.

Install Git, curl and the [dev build of funcdctl](../../../get-started/install.md); the dev build includes the SQL
engine. For TypeScript, put Node.js on the `PATH`. For Python, put [uv](https://docs.astral.sh/uv/) on the `PATH`: the example's
Functions import packages, such as SQLModel, that uv installs before it starts `funcdctl dev`.

`funcdctl dev` starts a local platform, applies the resource files of one folder, and prints the URL of each Function,
on the port that `--gport` sets. It runs each Function from the code in the folder, not from the manifest's `image`,
the bundle that `funcdctl push` writes for a platform to run. For each SQLDatabase, it starts an engine on the local
computer and logs `dev sql engine ready`, with the database's name, once the engine answers. The banner can appear
before that: a call sent then waits until the engine and the Function's worker are ready. If they are not ready
within `invoke.activationTimeout`, 30 seconds by default, the call fails; send it again once they are ready.
`funcdctl dev` deletes the
databases' data when it stops, unless `--persist` keeps it, as [Run it locally](#run-it-locally) explains.

In each example, a **SQLDatabase** names its **owner**, the Function that can write it. Each Function declares the
database by `ref` under [`spec.sql`](../../workloads/function/spec.md#functionspecsql), since the database is a
separate resource, and binds it under [`spec.bindings.sql`](../../workloads/function/spec.md#functionspecbindingssql)
with the alias `app`, which the handler passes to `ctx.sql`. The binding lets the Function read the database. A
RolesAssignment, in `grant.yaml`, gives the migration Function the role `SQL Data Writer`, so that it can write a
database that another Function owns; [Access](#access) explains who can write. `funcdctl dev` skips the folder's
`funcdconfig.yaml`, a configuration of the funcd server.

### TypeScript

The folder holds both handlers already built, as `notes.mjs` and `notes-migrate.mjs`, so the example needs no build
step. Clone the repository and start the example from its root folder:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ cd funcd-typescript
$ funcdctl dev examples/sql-notes --gport 8080
```

In another terminal, apply the migrations, add a note, and list the notes:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/notes-migrate
{"applied":2}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"add","notebook":"shopping","title":"milk"}' http://127.0.0.1:8080/function/notes
{"note":{"id":1,"title":"milk"}}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/function/notes
{"notes":[{"id":1,"notebook":"shopping","title":"milk","done":false,"priority":0}]}
```

`"applied":2` is the number of migrations that `notes-migrate` applied, and the list contains the note that the call
before it added. To check that the database records the migrations that it holds, call `notes-migrate` again. It
applies none:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/notes-migrate
{"applied":0}
```

Press Ctrl-C in the first terminal to stop the example.

The SQLDatabase `notes-db`, in `notes-db.yaml`, names the Function `notes` as its owner:

::include[../../../../../_repos/funcd-typescript/examples/sql-notes/notes-db.yaml]

The Function `notes`, in `notes.yaml`, declares and binds `notes-db`; `notes-migrate.yaml` does the same for
`notes-migrate`:

::include[../../../../../_repos/funcd-typescript/examples/sql-notes/notes.yaml]

The handler of `notes-migrate` applies the migrations of the folder `drizzle`, which drizzle-kit generated from the
schema in `src/db/schema.ts`. `applyMigrations`, from the package `@funcd-dev/drizzle`, records each migration that it
applies in the table `_migrations`. It applies the migrations that are newer than the newest record of that table,
with their records, in one `ctx.sql.execute` call:

::include[../../../../../_repos/funcd-typescript/examples/sql-notes/src/notes-migrate/main.ts]

The handler of `notes` builds its statements with Drizzle. `funcdDrizzle`, from `@funcd-dev/drizzle`, gives Drizzle a
database that sends each statement that starts with `SELECT` or `WITH` to `ctx.sql.queryValues`, and each other
statement to `ctx.sql.execute`. In the two excerpts below, from `src/notes/main.ts`
([the complete file](https://github.com/pyvvo/funcd-typescript/blob/main/examples/sql-notes/src/notes/main.ts)), `d`
is that database, `input` is the call's data, and `notes` and `notebooks` are the tables of `src/db/schema.ts`. The
operation `add` creates the notebook on its first use, through `notebookId`, and adds the note:

::include[../../../../../_repos/funcd-typescript/examples/sql-notes/src/notes/main.ts#add]

The operation `list`, the `default` branch of the same `switch`, returns the notes with the name of their notebook,
highest `priority` first:

::include[../../../../../_repos/funcd-typescript/examples/sql-notes/src/notes/main.ts#list]

### Python

Clone the repository, go to the example's folder, and start the example with `uv run`:

```console
$ git clone https://github.com/pyvvo/funcd-python
$ cd funcd-python/examples/sql-orders
$ uv run funcdctl dev . --gport 8080
```

`uv run` installs the packages that the example's lock file, `uv.lock`, names into a virtual environment in the
folder, `.venv`. It then runs `funcdctl dev` with that environment's `python3` first on the `PATH`, and `funcdctl dev`
runs the Functions with that `python3`.

In another terminal, apply the migrations, place an order, and list the orders:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/orders-migrate
{"applied":2}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"place","email":"ada@example.org","item":"tea","quantity":2}' http://127.0.0.1:8080/function/orders
{"changed":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"list"}' http://127.0.0.1:8080/function/orders
{"orders":[{"id":1,"email":"ada@example.org","item":"tea","quantity":2,"status":"placed","note":null}]}
```

`"applied":2` is the number of migrations that `orders-migrate` applied, and the list contains the order that the
call before it placed. To check that the database records the migrations that it holds, call `orders-migrate` again.
It applies none:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{}' http://127.0.0.1:8080/function/orders-migrate
{"applied":0}
```

Press Ctrl-C in the first terminal to stop the example.

The SQLDatabase `orders-db`, in `orders-db.yaml`, names the Function `orders` as its owner:

::include[../../../../../_repos/funcd-python/examples/sql-orders/orders-db.yaml]

The Function `orders`, in `orders.yaml`, declares and binds `orders-db`; `orders-migrate.yaml` and `audit.yaml` do
the same for `orders-migrate` and `audit`:

::include[../../../../../_repos/funcd-python/examples/sql-orders/orders.yaml]

The handler of `orders-migrate` applies the migrations that the package `orders_core` holds as SQL text, which a
script of the example rendered from its Alembic revisions. It records each migration that it applies in the table
`_migrations`, and applies the migrations that the table does not record yet, with their records, in one
`context.sql.execute` call:

::include[../../../../../_repos/funcd-python/examples/sql-orders/src/orders-migrate/main.py]

The handler of `orders` builds its statements with SQLModel. The package `orders_core` compiles each statement into
SQL text and parameters, and sends it with `context.sql.query` or `context.sql.execute`. In the two excerpts below,
from `src/orders/main.py`
([the complete file](https://github.com/pyvvo/funcd-python/blob/main/examples/sql-orders/src/orders/main.py)),
`execute` and `rows` are the functions of `orders_core.sqlrun` that send the statements, `Customer` and `Order` are the
models of `orders_core.models`, and `data` is the call's data. The operation `place` adds the customer on its first
order and the order itself in one `execute` call:

::include[../../../../../_repos/funcd-python/examples/sql-orders/src/orders/main.py#place]

The operation `list`, which `handle` runs for every `op` that its `match` does not name, returns the orders with the
email of their customer; when the call gives an `email`, it returns that customer's orders only:

::include[../../../../../_repos/funcd-python/examples/sql-orders/src/orders/main.py#list]

The folder's `grant.yaml` gives the role `SQL Data Writer` to `orders-migrate` and to `audit`. `audit` records each
change of the table `orders` in the table `order_events`. The database's engine reports each row change to funcd;
that stream of row changes is the database's change feed. `changes.yaml` declares an EventSource, a resource that
emits events, with a `sql` source under its field [`spec.sql`](../events/spec.md#eventsourcespecsql), which turns each
change of the table `orders` into an event. The file also declares a [Sensor](../events/spec.md#sensor), a resource
that runs an action when an event arrives: this Sensor calls `audit` with each such event, so the order that the steps
above placed reached `audit` too.

## Call ctx.sql without a query builder

A handler can write its SQL itself and pass it to `ctx.sql`, without a library such as Drizzle or SQLModel. The
example sql-search does so, in TypeScript only: its Function `search` stores articles, keeps a full-text index of
them, and searches that index. In Python, the handler of `orders-migrate` in the first example calls `context.sql` in
the same way.

The Function `search` declares its database by `name` under `spec.sql`, in place of a `ref`. funcd then creates the
SQLDatabase `search-db`, and `search` becomes its owner, since the entry names no other owner, so the example needs no
SQLDatabase file of its own. [Changes and deletion](#changes-and-deletion) explains what happens to such a database
when the Function is deleted.

The handler's operation `init` creates the table, the full-text index and the triggers that keep the index up to
date, with one `ctx.sql.execute` call that takes a list of statements. The operation `add` inserts an article with
`execute`, which fills the placeholders `:title`, `:body` and `:tags` from an object, and the statement's
`RETURNING id` clause returns the new article's id in the result's `values`. The operation `search` reads with
`ctx.sql.query`, which returns each row as an object keyed by column name. The index is an FTS5 table, the full-text
search of SQLite, and SQLite's `json_each` turns the JSON array of each article's tags into rows.

`execute` runs its list of statements in one transaction, which applies all of them or none, and a transaction never
spans two calls. When the engine rejects a statement, such as one that names a missing table, the whole call fails,
none of its statements applies, and the message names the statement by its position in the call, counted from 0, such
as `statement 1: …` for the second statement. The first example relies on this: the operation `move` of `notes`
creates the target notebook and moves the notes in one call, the operation `place` of `orders` adds the customer and
the order in one call, and each migration Function applies its migrations with their records in one call.

`query` and `queryValues` (`query_values` in Python) only read: send a statement that writes through `execute`. A
parameter is a string, a number, a boolean or null, never a list or an object. When funcd refuses a `ctx.sql` call,
the method throws an error in TypeScript, and raises a `RuntimeError` in Python, whose message names the method, the
HTTP status and funcd's explanation, such as `context.sql.query failed: 400 …` followed by
`statement 0: no such table: orders`. The error of a `queryValues` (`query_values`) call also starts with
`context.sql.query failed`, and that of an `execute` call with `context.sql.execute failed`. A library such as Drizzle can wrap that error in one of its own. A handler that
does not catch the error makes the call to the Function fail with the HTTP status 500.

### TypeScript

The Function `search`, in `search.yaml`, declares the database `search-db` by `name` and binds it:

::include[../../../../../_repos/funcd-typescript/examples/sql-search/search.yaml]

In the root folder of the funcd-typescript repository, start the example:

```console
$ funcdctl dev examples/sql-search --gport 8080
```

In another terminal, create the tables, add an article, and search for one of its words:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"init"}' http://127.0.0.1:8080/function/search
{}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"add","title":"Raft in practice","body":"rqlite replicates SQLite with Raft","tags":["raft"]}' http://127.0.0.1:8080/function/search
{"id":1}
$ curl -s -X POST -H 'content-type: application/json' -d '{"op":"search","q":"raft"}' http://127.0.0.1:8080/function/search
{"hits":[{"id":1,"title":"Raft in practice","snippet":"rqlite replicates SQLite with [Raft]","tags":["raft"]}]}
```

The snippet puts brackets around each word that matched. Press Ctrl-C in the first terminal to stop the example.

The handler, in `src/main.ts`:

::include[../../../../../_repos/funcd-typescript/examples/sql-search/src/main.ts]

## Access

A Function needs no role for these [actions](../../security-and-networking/security/index.md#choose-a-role):

- **Read the databases that it binds** (`sql::read`). Each entry of `spec.bindings.sql` lets the Function read that
  database, never write it.
- **Write the databases that it owns** (`sql::write`). A database that a Function declares by `name` has that
  Function as its owner, unless the entry names another one.

funcd checks reading for each call of `query` or `queryValues` (`query_values` in Python), and both reading and
writing for each `execute` call, even one whose statements only read, since a statement with a `RETURNING` clause
writes and reads at once.

Any other write needs a role. A RolesAssignment gives a Function a role on one database, or on every database of its
namespace. The smallest built-in role that allows writing is `SQL Data Writer`, which allows reading and writing. Its
scope has the kind `SQLDatabase` and the database's name, or the kind `Namespace` for every database of the
namespace. The TypeScript folder of the first example holds this RolesAssignment, in `grant.yaml`, which gives
`notes-migrate` write access to `notes-db`:

::include[../../../../../api/types/v1alpha1/examples/sql/writer-role.yaml#rolesassignment]

`SQL Data Reader`, the other built-in role for databases, allows reading only, which the binding of a Function already
allows. The built-in roles `Reader`, `Contributor` and `Owner` do not cover SQL databases, so a RolesAssignment that
gives one of them on the namespace allows no write into a database. A Role that an operator defines can grant
`sql::write` too. The Security reference [lists the built-in roles](../../security-and-networking/security/spec.md#rolesassignmentspecassignmentsroleref)
and [explains how assignments pair each role with a scope](../../security-and-networking/security/spec.md#rolesassignmentspecassignments).

funcd applies these rules as a built-in policy in every namespace, so nothing needs to be written or installed. A
[Policy](../../security-and-networking/security/index.md#write-a-policy) that an operator writes applies beside it,
and cannot allow a write that the built-in policy forbids. In Cedar, the built-in policy reads:

```cedar title="Built-in SQL policy"
@id("sql-read-binding")
permit(principal, action == Action::"sql::read", resource)
  when { principal has sqlBindings && principal.sqlBindings.contains(resource) };

@id("sql-write-base")
permit(principal, action == Action::"sql::write", resource);

@id("sql-single-writer")
forbid(principal, action == Action::"sql::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
```

A binding can name only a database that its own Function declares, by `name` or by `ref`. No role and no Policy lets
a Function reach a database that it does not bind: `ctx.sql` takes only the alias of one of its Function's bindings.
A call that the policy denies fails with the HTTP status 403, and its message names the rule, such as
`cedar: forbidden by policy builtin/sql-single-writer`. A write by a Function that neither owns the database nor holds
a writer role meets that rule: give the Function a writer role, as above.

## Limits and quotas

A database's [`spec.quota`](spec.md#sqldatabasespecquota) caps its total size, the bytes of the database's pages in
use. `maxSize` or `maxBytes` sets the cap: set one of the two, not both. Without either, the database has no cap.

funcd learns the size of a write only once the write has run: it measures the database after each `execute` call, so
a call that starts below the cap can take the database past it. Once the database holds its cap or more, an `execute`
call fails, unless each of its statements is a single `DELETE` or `DROP` statement, which frees room that the next
measure counts.

[`spec.resources`](spec.md#sqldatabasespecresources) sets the memory and the CPU of the database's engine: 256 MiB of
memory when `memory` is empty, and no CPU limit when `cpu` is empty. Only the containerd runtime enforces them. This
SQLDatabase caps its size at 1 GiB and gives its engine 512 MiB of memory:

::include[../../../../../api/types/v1alpha1/examples/sql/sqldatabase.yaml#sqldatabase]

funcd also limits each `ctx.sql` call, whatever the database sets:

- One call sends at most 500 statements and at most 1 MiB of statements and parameters, and gets back at most 8 MiB
  of results. A larger call or answer fails.
- A call that runs longer than `sql.callTimeout`, 30 seconds by default, fails.

A namespace holds at most `sql.maxDatabasesPerNamespace` SQLDatabases, 20 by default; a negative value of the key
removes that cap. The operator sets both keys in [funcd's configuration](../../running-funcd/spec.md#funcdconfigyaml).
The [Spec](spec.md) tab gives the range of each field of a SQLDatabase.

## Data storage

On a funcd server, the engine of each database runs in a container of the containerd runtime, and keeps the
database's data, SQLite files, in a folder of its own under `providers/sqldatabase` in funcd's data directory,
`storage.dataDir`. The data survives a restart of funcd. With `storage.mode: memory`, funcd keeps these folders in a
temporary directory, which it removes when it stops, so the data is lost. A server that runs the process runtime keeps
no folder for an engine, so no database engine starts there, as [Status](#status) shows.

A workload backup copies a database's data: a BackupSchedule that lists the database under
[`scope.databases`](../../disaster-recovery/spec.md#backupschedulespecscope) copies it at each of its runs, and a
Restore loads a copy back, into the same database or into an empty one. The
[Disaster recovery](../../../platform/disaster-recovery.md) page explains backups and restores.

## Status

`funcdctl get sqldatabase notes-db -n default -o json` prints the status of a database, and
`funcdctl get function notes -n default -o json` that of a Function. With `funcdctl dev`, add the `--server` and
`--token` flags that its banner prints. A database's `status.bindings` counts the Functions that bind it.

| Phase or condition | Reason | Meaning |
| --- | --- | --- |
| SQLDatabase `Pending` | `EngineNotReady` | The engine is starting. |
| SQLDatabase `Pending` | `EngineConvergeFailed` | funcd could not start the engine, as on a server that runs the process runtime. funcd's log gives the cause. |
| SQLDatabase `Ready` | | The engine answers. |
| SQLDatabase `DataMissing` condition `True` | `NoDirectory` | On a server whose `storage.mode` is `file`, the default, a database that was `Ready` came back without its data folder: it starts empty and `Ready`, and keeps this condition until a Restore into it succeeds. No database shows it with `storage.mode: memory` or under `funcdctl dev`. |
| Function `Pending` | `SQLDatabaseNotFound` | A database that the Function binds does not exist. The Function starts once it exists. |
| Function `Deploying`, `Degraded` or `Ready` | `DependencyNotReady` | The engine of a database that the Function binds is not `Ready`, and each `ctx.sql` call fails. A Function that never served is `Deploying`. One that served is `Degraded` once no worker is ready, and stays `Ready`, with this reason on its `True` `Ready` condition, while some workers are. |
| Function `Pending` | `StoreNotOwned` | A database that the Function declares by `name` exists and is not the Function's, as [Changes and deletion](#changes-and-deletion) describes. |
| Any other reason | | The `Ready` condition's reason and message, read with the commands above, give the cause. |

## Changes and deletion

- A change of `spec.quota` keeps the engine running. On a server, a change of `spec.resources` replaces the engine:
  funcd stops it and starts a new one on the same data. Until the new engine is ready, the database is `Pending`, and
  each `ctx.sql` call fails.
- funcd refuses a request to delete a SQLDatabase while a binding of a Function names it, while the database holds a
  table, or while the engine of a database that has data cannot answer. Remove the bindings and drop the tables
  first. Once the database is deleted, funcd stops its engine and deletes its data.
- When a Function that declares the database by `name` is deleted, the `deletion` setting of its entry under
  [`spec.sql`](../../workloads/function/spec.md#functionspecsql) decides: `retain`, the default, keeps the database
  and its data, and `delete` deletes them, even while the database holds tables.
- A new Function of the same name does not own a database that stayed: if it declares the database by `name`, it
  waits in `Pending` with the reason `StoreNotOwned`. Declare the database by `ref` instead.

## CLI errors

`funcdctl dev` stops before it serves the example when it meets one of these local setup errors:

| Error | Action |
| --- | --- |
| `no node interpreter found for the node handler` | Put Node.js on the `PATH`, or set `FUNCD_NODE` to the path of `node`. |
| `no python interpreter found for the python handler` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. Under `uv run`, uv puts its environment's `python3` first. |
| `cannot load the runtime shim (needs Python ≥3.12; set FUNCD_PYTHON)` | Put Python 3.12 or later on the `PATH` as `python3`, or set `FUNCD_PYTHON` to its path. |
| `the folder holds a SQL database, but this funcdctl has no SQL engine` | Install the [dev build of funcdctl](../../../get-started/install.md), which includes the SQL engine, or build the engine as the message says. |

## Run it locally

`funcdctl dev` runs the engine of each database as a process on the local computer, not in a container, and does not
enforce `spec.resources`.

`funcdctl dev` keeps the data of each database in a temporary folder, which it deletes when it stops, whatever
[Data storage](#data-storage) says about a server. With `--persist`, it keeps the data between runs, in the `sql`
folder of `.funcd-dev` in the current folder, or of the folder that `--persist-to` names. To empty the databases,
delete that `sql` folder after `funcdctl dev` stops: at the next run, each database starts empty. Deleting the whole
persist folder instead clears all the state that the local platform stored: its resources and its KV, blob, SQL, lake
and volume data. Neither example repository ignores the persist folder, so `git status` lists it.

Unlike for Buckets, `funcdctl dev` gives the Functions no role on SQL databases: a write that a platform refuses, such
as a write by a Function that neither owns the database nor holds a role on it, is refused locally too. An EventSource
with a `sql` source fires under `funcdctl dev` too: the Sensor of the Python example calls `audit` with each change of
the table `orders`. The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags.

## Examples

- [sql-notes in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/sql-notes) and
  [sql-orders in Python](https://github.com/pyvvo/funcd-python/tree/main/examples/sql-orders): a database whose
  migration Function creates its tables, and whose owner adds and lists its rows, with Drizzle in TypeScript, and
  with SQLModel and Alembic in Python, where a Sensor also calls a Function with each change of a table.
- [sql-search in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/sql-search): full-text
  search with SQLite's FTS5 and JSON functions, through `ctx.sql` with no query builder.
- [The todo App](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo), whose folder `sql` holds a
  TypeScript owner and a Python reader of one database, with a backup, a restore and the change feed.
