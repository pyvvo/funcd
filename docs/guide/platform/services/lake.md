---
title: Lake
description: A SQL lake on DuckDB, with its files in a Bucket and each session checked.
---

![A Function with its Lake token, or an outside client with an Identity's token through the edge, sends queries to the Lake gateway. The gateway asks Cedar for lake::query when a session opens and passes the queries of an allowed session to the Lake's DuckDB engine. The engine keeps its catalog as a DuckDB file and its Parquet files under a Bucket prefix](../../assets/lake.drawio.svg)

A Lake is a SQL lake: a DuckDB engine with a DuckLake catalog, one for each Lake, which funcd runs through its add-on
runtime. Its data is Parquet files under a prefix of a Bucket, which the engine reads and writes through funcd's S3
gateway. Its catalog is a DuckDB file in funcd's catalog store, outside every Bucket. A Lake therefore needs the S3
gateway: while the gateway is off, its engine cannot start, and the Lake stays Pending.

Every query goes through the Lake gateway. When a session opens, the gateway asks Cedar for `lake::query` for the
caller, then passes the session's queries to the engine without a new check. A Function bound to the Lake in `spec.bindings.lake` uses a token that funcd signs for it. A
caller outside funcd uses an Identity's token and reaches the gateway through the edge.
