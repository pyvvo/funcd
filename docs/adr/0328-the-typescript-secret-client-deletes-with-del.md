# ADR-0328: The TypeScript Secret client deletes with del, as its KV and Blob clients do (amends ADR-0327)

- **Status**: Accepted (2026-11-13), by the decider, after one round of an independent judge (a subagent): accept,
  its Minor and its Nit folded
- **Date**: 2026-11-13
- **Deciders**: green-0-rabbit
- **Tags**: secrets, shims, typescript
- **Realizes**: [FEAT-0008/F240](../feat/0008-feat-iam.md) (each language's Secret client names its calls as its KV and
  Blob clients do)
- **Supersedes (in part)**: [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md): Decision 5's TypeScript
  `delete(binding, key): Promise<void>`, and every other mention of the TypeScript delete: its Contracts row for
  funcd-typescript and its Exposes cell, its Scope's "both shims' `put` and `delete`", its secrets.md row title,
  `scenario: secret-owner-writes` for a TypeScript handler, and its review checklist's "The shims' `put` and `delete`
  are released and pinned". Python's `delete` and the rest stand.
- **Relates to**: [ADR-0249](0249-ctx-secret-reads-from-funcd.md) Decision 4 (the shims' Secret clients)

## Context & Need

ADR-0327 Decision 5 names TypeScript's write of a Secret key `put` and its delete `delete`, as a copy of KV. In
TypeScript, KV and Blob name the delete `del` (pyvvo/funcd-typescript v0.27.0, `shim/src/kv.ts:54`,
`shim/src/blob.ts:25`); in Python, both name it `delete` (pyvvo/funcd-python v0.22.0, `shim/src/funcd_shim/kv.py:159`,
`blob.py:82`). The implementation of ADR-0327 found it before any shim release, and the decider chose `del` on
2026-11-13.

## Scenarios

- `scenario: secret-owner-writes` (changed): a TypeScript handler deletes with `ctx.secret.del("oauth", "TOKEN")`; a
  Python handler keeps `ctx.secret.delete("oauth", "TOKEN")`.

## Scope

Unchanged (ADR-0327); this ADR changes only the TypeScript delete's name.

## Constraints & Decision drivers

Unchanged (ADR-0327): copy what KV does, in each language.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. `del` in TypeScript and `delete` in Python, as each language's KV and Blob clients** | a client reads like its siblings | the languages differ, as they do for KV and Blob | **Chosen** (the decider) |
| B. `delete` in both, as ADR-0327 says | one name in both languages | TypeScript's Secret client differs from its KV and Blob clients | Rejected (the decider) |

## Decision

5. **The shims.** TypeScript `context.secret.put(binding, key, value): Promise<void>` and
   `context.secret.del(binding, key): Promise<void>`, as `context.kv.del` and `context.blob.del`. Python is unchanged:
   `ctx.secret.put(binding, key, value) -> None` and `ctx.secret.delete(binding, key) -> None`. The rest of ADR-0327
   Decision 5 stands.

## Design model

None: a name triggers nothing of ADR-0272.

## Behavior rows

ADR-0327's secrets.md row is titled "`ctx.secret.put`, `del` or `delete`".

## Temporary workarounds

None.

## Contracts

| Package | Addition |
|---|---|
| funcd-typescript `shim` | `SecretClient.del(binding: string, key: string): Promise<void>`, in place of ADR-0327's `delete` |

## Implementation plan

It lands with ADR-0327's implementation, in the same funcd-typescript change and release. ADR-0328 moves to Reviewing
with ADR-0327, and the review of that implementation checks it and stamps it Implemented with it.

## Review checklist

- [ ] funcd-typescript's `SecretClient` has `put` and `del`, and no `delete`; funcd-python's has `put` and `delete`.

## Consequences

- **Positive**: each language's Secret client reads like its KV and Blob clients.
- **Negative**: the call differs between TypeScript and Python, as KV's and Blob's do.

## Open questions

None.

## References

- The decider's choice of 2026-11-13, made at ADR-0327's implementation.
- pyvvo/funcd-typescript v0.27.0 `shim/src/kv.ts:54`, `shim/src/blob.ts:25`; pyvvo/funcd-python v0.22.0
  `shim/src/funcd_shim/kv.py:159`, `shim/src/funcd_shim/blob.py:82`.
