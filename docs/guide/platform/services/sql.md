---
title: SQL
description: A SQL database per SQLDatabase, on its own rqlite engine.
---

![A Function's query and execute calls go through its local API to the SQL facade, which asks Cedar for sql::read or sql::write and passes them to the database's rqlite engine; the engine keeps SQLite files and posts row changes to an EventSource](sql.drawio.svg)

A SQLDatabase is a SQL database with its own rqlite engine. funcd runs one engine per database through its add-on
runtime, as a container on a funcd server and as a host process under `funcdctl dev`, and keeps its data, SQLite
files, under `<dataDir>/providers`.

A Function reaches a database through a binding in `spec.bindings.sql`: its local API takes `query` and `execute`
calls, and the SQL facade asks Cedar for `sql::read` or `sql::write` before it passes them to the engine. The engine
also posts each row change to funcd, which an EventSource of the `sql` kind turns into events.
