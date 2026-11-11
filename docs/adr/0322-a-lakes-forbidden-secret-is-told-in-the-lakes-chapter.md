# ADR-0322: A Lake's forbidden Secret is told in the Lake's chapter (amends ADR-0321)

- **Status**: Proposed
- **Date**: 2026-11-11
- **Deciders**: green-0-rabbit
- **Tags**: behavior, secrets, lake
- **Realizes**: [FEAT-0008/F237](../feat/0008-feat-iam.md) (each Secret-read rule is told in the chapter of what it holds)
- **Supersedes (in part)**: [ADR-0321](0321-cedar-decides-each-secret-a-workload-reads.md) — its Behavior rows' security.md
  item for a Lake: a Lake's forbidden Secret and its lift become a row of services.md, the Lake's chapter, and the
  security.md row tells a Function's.
- **Relates to**: [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (a chapter's written part
  holds at most 16 KiB and a row 400 bytes; a neighbor's step gets one row)

## Context & Need

ADR-0321's Behavior rows put one new security.md row on a forbidden Secret, for a Function and for a Lake, with its
lift. Its implementation (f474bb94) found the security chapter at the registry's cap: with that row, `just
check-behavior` refused the chapter's written part (17,024 bytes, over 16,384) and the row itself (over 400 bytes). A
Lake's other holds are told in services.md, where a reader of the Lake looks.

## Scenarios

None new: ADR-0321's scenarios stand. This changes only the chapter that tells `lake-secret-forbid-before-start` and
`lake-secret-forbid-while-running`.

## Scope

Unchanged (ADR-0321): of it, only the Behavior rows section is touched.

## Constraints & Decision drivers

- ADR-0313's caps, which `just check-behavior` enforces, and its rule that a step of a neighbor's chapter gets at most
  one row there (the behavior-registry skill).
- No behavior changes: the rows tell what ADR-0321 decided.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A Function's forbid in security.md, a Lake's in services.md** | each chapter tells what it holds; both fit their caps | a reader of security.md follows a link for the Lake | **Chosen** |
| B. Both in security.md, cutting other rows to make room | one place | decided rows of other ADRs lose content | Rejected |
| C. Split the security chapter | room for later rows | a reorganization beyond this change | Rejected |
| D. Keep ADR-0321's text and note the move in its review | no new ADR | the accepted text names a row that does not exist | Rejected (the decider asked to amend it) |

## Decision

1. **ADR-0321's Behavior rows**, replaced:
   - **security.md**: the `ctx.secret.get or list` row cites ADR-0321; a new row, "A Policy forbids a Function's read
     of a bound Secret": each next `ctx.secret` read fails, of any key, and the readiness check fails (workers.md), so
     with no ready replica the Function is `Degraded`, taking no call; at the next read, readiness within
     `runtime.supervisionPeriod`.
   - **services.md**: a new row, "A Policy forbids a Lake's read of a bound Secret": the Lake is held and its engine
     stopped, or never started with the key, within `runtime.supervisionPeriod`; once the forbid goes, the engine
     starts with it, within `controller.referentPollInterval`.
   - **workers.md**: the `A binding check fails` row names the `secret` kind; its re-check each
     `runtime.supervisionPeriod` is a Function's lift.

## Design model

None: where a row lives triggers nothing of ADR-0272.

## Behavior rows

Decision 1's rows, in place of ADR-0321's.

## Temporary workarounds

None.

## Contracts

None: no code changes.

## Implementation plan

The rows landed with ADR-0321's implementation (f474bb94), in docs/behavior/security.md, services.md and workers.md;
`just behavior-values` and `just check-behavior` pass. The review of ADR-0321's implementation checks them.

## Review checklist

- [ ] security.md holds the Function's row, services.md the Lake's, and workers.md names the `secret` kind.
- [ ] Each row cites the ADR-0321 Decision it tells, and `just check-behavior` passes.

## Consequences

- **Positive**: each chapter tells what it holds, within its caps.
- **Negative**: none beyond the link from security.md to workers.md.

## Open questions

None.

## References

- ADR-0321 and its implementation, f474bb94; `just check-behavior`'s refusal of the first rows.
