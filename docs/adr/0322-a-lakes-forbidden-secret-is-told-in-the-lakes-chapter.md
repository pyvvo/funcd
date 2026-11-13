# ADR-0322: A Lake's forbidden Secret is told in the Lake's chapter (amends ADR-0321)

- **Status**: Implemented (2026-11-11), review passed with no finding ([report](../reviews/adr-0322-implementation-claude-opus-5-5.md)); Reviewing (2026-11-11, implemented with ADR-0321); Accepted (2026-11-11), under the decider's delegation
  ("Don't need acceptance brief run it till completion"), after two rounds of an independent judge (a subagent), every
  finding folded
- **Superseded in part by**: [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) (2026-11-13) — its Decision's security.md row: the new Secrets chapter tells a Function's forbidden Secret, with the other Secret rows.
- **Date**: 2026-11-11
- **Deciders**: green-0-rabbit
- **Tags**: behavior, secrets, lake
- **Realizes**: [FEAT-0008/F237](../feat/0008-feat-iam.md) (each Secret-read rule is told in the chapter of what it holds)
- **Supersedes (in part)**: [ADR-0321](0321-cedar-decides-each-secret-a-workload-reads.md) — its Behavior rows section, whole:
  a Lake's forbidden Secret and its lift become rows of services.md, the Lake's chapter; security.md tells a Function's;
  workers.md's binding row names the `secret` kind and cites ADR-0321.
- **Relates to**: [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (a chapter's written part
  holds at most 16 KiB and a row 400 bytes) · the behavior-registry skill (a neighbor's step gets one row)

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
| **A. A Function's forbid in security.md, a Lake's in services.md** | each chapter tells what it holds; both fit their caps | security.md tells only the Function: a reader looking for the Lake finds it in services.md | **Chosen** |
| B. Both in security.md, cutting other rows to make room | one place | decided rows of other ADRs lose content | Rejected |
| C. Split the security chapter | room for later rows | a reorganization beyond this change | Rejected |
| D. Keep ADR-0321's text and note the move in its review | no new ADR | the accepted text names a row that does not exist | Rejected (the decider asked to amend it) |

## Decision

**ADR-0321's Behavior rows**, replaced:

- **security.md**: the `ctx.secret.get or list` row adds ADR-0321's per-Secret decision and keeps ADR-0249's round
  trip per read; a new row, "A Policy forbids a Function's read of a bound Secret": each next `ctx.secret` read fails,
  of any key, and the readiness check fails (workers.md), so with no ready replica the Function is `Degraded`, its
  calls held; at the next read, readiness within `runtime.supervisionPeriod`.
- **services.md**: a new row, "A Policy forbids a Lake's read of a bound Secret": the Lake is held, `Pending`, and its
  engine stopped, or never started with the key, within `runtime.supervisionPeriod`; once the forbid goes, the engine
  starts with it, within `controller.referentPollInterval`. And an `accepted` row: a forbid applied between a pass's
  check and its Converge starts the engine once with the key, and the next pass stops it (ADR-0321 Risks accepted).
- **workers.md**: the `A binding check fails` row names the `secret` kind and cites ADR-0321 Decision 4; its re-check
  each `runtime.supervisionPeriod` is a Function's lift.

## Design model

None: where a row lives triggers nothing of ADR-0272.

## Behavior rows

The Decision's rows, in place of ADR-0321's.

## Temporary workarounds

None.

## Contracts

None: no code changes.

## Implementation plan

The rows land with ADR-0321's implementation, in docs/behavior/security.md, services.md and workers.md;
`just behavior-values` and `just check-behavior` pass. ADR-0322 moves to Reviewing with ADR-0321, and the independent
review of that implementation checks these rows and stamps ADR-0322 and F237 Implemented with it.

## Review checklist

- [ ] security.md holds the Function's row, services.md the Lake's two rows, and workers.md names the `secret` kind.
- [ ] Each row cites the ADR-0321 part it tells, and `just check-behavior` passes.

## Consequences

- **Positive**: each chapter tells what it holds, within its caps.
- **Negative**: a reader of security.md finds the Lake's rows in services.md, not through a link; and the security
  chapter's written part is now about 20 bytes under its 16 KiB cap, so the next ADR that adds a row there splits
  the chapter (option C, postponed to it).

## Open questions

None.

## References

- ADR-0321 and its implementation, f474bb94; `just check-behavior`'s refusal of the first rows.
