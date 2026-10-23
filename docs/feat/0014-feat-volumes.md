# FEAT-0014: Function volumes, a size-limited directory that belongs to one Function

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Defines**: a **volumes epoch**: a Function declares a directory on its spec, gets it on its worker's filesystem
  with a hard size limit, and no other Function can reach it. Positioned alongside the other additive epochs
  (FEAT-0003 to FEAT-0013).

## Initial need

Code that insists on file paths (a model cache, a SQLite file, a library's work directory) has no place to put
them. Today:

- the Function spec has no volume, mount or storage field;
- a worker's writable root filesystem is deleted with its container, so a crash, a redeploy or a scale to zero
  loses everything written there, and each replica has its own copy;
- durable host directories exist only for add-on engines (ADR-0231), set by a provider's reconciler, never by a
  Function;
- the data kinds (KVStore, Bucket, SQLDatabase, Lake) keep state, but only through their APIs.

Container platforms answer this with per-replica scratch (Azure Container Apps `EmptyDir`, Kubernetes `emptyDir`) and
mounted shares (Azure Files). This epoch covers the first, plus a durable directory owned by one Function; a Bucket
prefix seen as a directory is an idea for later.

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decisions were refined with the decider on 2026-10-23, card
by card, before the first ADR was drafted.

## Features

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F181 | **Function-owned volumes**: a Function declares `emptyDir` (one per worker, gone with it) and `local` (durable, owned like a store) volumes with a required size; its code finds each one through an env var; the size is a hard limit, the platform never overcommits its volume capacity, and a namespace can carry a budget; no Function reaches another's volume; a `local` volume can be backed up and restored | — | [ADR-0262](../adr/0262-function-owned-volumes.md) | reviewing |
| F182 | **A Bucket prefix as a directory**: a blob binding mounted read-only for every binder and read-write for the prefix owner, first as a snapshot taken at worker start, later live | F181 | — | idea |

## Exit criterion

A Function on the containerd runtime declares `local` and `emptyDir` volumes with sizes, writes up to each size and
fails past it, keeps its `local` data across a crash, a redeploy and a scale to zero, cannot see any other volume,
and gets its `local` data back from a workload backup after the host is lost.
