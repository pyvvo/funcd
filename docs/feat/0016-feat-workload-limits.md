# FEAT-0016: Workload limits, CPU and memory for Functions and a total size for stores

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Defines**: a **workload limits epoch**: a Function's worker gets CPU and memory limits, and a Bucket, a KVStore or a
  SQLDatabase gets a cap on its total size. Positioned alongside the other additive epochs (FEAT-0003 to FEAT-0015).

## Initial need

One workload can take what a node has, and nothing stops it:

- a Function has no CPU or memory field, and its worker runs with no limit; only Lake and SQLDatabase engines carry
  `spec.resources` today;
- a Bucket, a KVStore and a SQLDatabase cap one object, key, value or database count, but not what they hold in total;
- per-namespace quotas are planned for V2 (ADR-0018), and only Function volumes are sized today (FEAT-0014).

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decisions were refined with the decider on 2026-10-27 before
the first ADR was drafted.

## Features

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F206 | **Function CPU and memory limits**: an owner sizes a Function's CPU and memory, a node default sizes one the owner left unsized, and the Function's workers cannot use more | — | [ADR-0291](../adr/0291-a-functions-workers-get-cpu-and-memory-limits.md) | implemented |
| F207 | **A store's total size**: an owner caps what a Bucket, a Lake, a KVStore or a SQLDatabase holds, and a write that would pass the cap is refused, while reads still work and deletes can bring the store back under it | — | [ADR-0292](../adr/0292-stores-get-a-quota-block-and-a-quota-framework.md) | implemented |
| F208 | **Per-namespace quotas**: a namespace's budget per consumed resource, compute for every worker (Functions, Jobs, engines), volumes, and each kind of store, checked at admission (V2, ADR-0018) | F206 · F207 | — | idea |
| F209 | **A Lake's catalog outside its Bucket**: a Lake's catalog is kept in a platform store that only its engine reaches, capped by one platform setting, so a Lake at its prefix's quota still answers queries and drops | F207 | [ADR-0293](../adr/0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) | implemented |
| F210 | **A worker's writes bounded**: a Function's or an engine's worker writes only into its memory, its volumes or a scratch of fixed size, so its writes cannot grow the node's disk past those sizes | F206 | [ADR-0294](../adr/0294-a-workers-root-filesystem-is-read-only.md) | accepted |
| F211 | **A volume's file count bounded**: a volume holds at most its share of the volume image's inodes, as it holds at most its size in bytes | — | [ADR-0295](../adr/0295-a-volumes-quota-limits-its-inodes.md) | accepted |
| F212 | **Free space kept for the platform**: workload writes stop at a free-space floor on the data filesystem, while reads, deletes and the platform's own writes go on | F207 | [ADR-0296](../adr/0296-the-data-filesystem-keeps-a-free-space-floor.md) | accepted |
| F213 | **A start on a full disk waits**: a start on a full data filesystem waits for space instead of exiting, and finishes once space is freed | F212 | [ADR-0297](../adr/0297-a-start-waits-for-disk-space.md) | accepted |

## Exit criterion

A Function sized by its owner cannot use more CPU or memory than it was given, and an unsized one gets the node
default; a store at its cap refuses a write, still answers reads, and takes writes again once deletes free room. A worker
writes only into its memory, its volumes or a scratch of fixed size; workload writes stop before the data filesystem
is full, and a start on a full one waits for space instead of exiting.
