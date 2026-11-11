---
title: Lake
description: A SQL lake on DuckDB, with its files in a Bucket and every query checked.
---

![A Function with its Lake token, or an outside client with an Identity's token through the edge, sends queries to the Lake gateway. The gateway asks Cedar for lake::query and passes the allowed queries to the Lake's DuckDB engine. The engine keeps its catalog as a DuckDB file and its Parquet files under a Bucket prefix](lake.drawio.svg)

A Lake is a SQL lake: a DuckDB engine with a DuckLake catalog, one for each Lake, which funcd runs through its add-on
runtime. Its data is Parquet files under a prefix of a Bucket, which the engine reads and writes through funcd's S3
gateway. Its catalog is a DuckDB file in funcd's catalog store, outside every Bucket.

Every query goes through the Lake gateway, which asks Cedar for `lake::query` for the caller before it passes the
query to the engine. A Function bound to the Lake in `spec.bindings.lake` uses a token that funcd signs for it. A
caller outside funcd uses an Identity's token and reaches the gateway through the edge.
