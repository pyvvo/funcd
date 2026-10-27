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
| F206 | **Function CPU and memory limits**: a Function sets `resources.cpu` and `resources.memory`, a node default limits one that sets neither, and its worker cannot use more | — | [ADR-0291](../adr/0291-functions-get-cpu-and-memory-limits-and-stores-a-total-size.md) | adr |
| F207 | **A store's total size**: a Bucket, a KVStore or a SQLDatabase sets `maxBytes`, and a write that would pass it is refused, while reads and deletes still work | — | [ADR-0291](../adr/0291-functions-get-cpu-and-memory-limits-and-stores-a-total-size.md) | adr |
| F208 | **Per-namespace quotas**: CPU, memory and storage summed over a namespace and checked at admission (V2, ADR-0018) | F206 · F207 | — | idea |

## Exit criterion

A Function on the containerd runtime that sets `resources` cannot use more CPU or memory than it set, and one that sets
none gets the node default; a store at its `maxBytes` refuses a write with 507 and still answers reads and deletes.
