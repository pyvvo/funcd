# ADR-0000: ADR process, template, and workflow gates

- **Status**: Accepted
- **Date**: 2026-06-13
- **Deciders**: green-0-rabbit
- **Tags**: meta, process

## Context & Need

funcd is designed in [blueprint.md](../../blueprint.md) but built **gradually**: each
component or feature goes through a decision phase before any code exists. We need a
single, repeatable decision format that (a) records why a choice was made, and (b) carries
enough contract detail that an LLM — given only the blueprint and one ADR — can scaffold
the corresponding interfaces, facades, and dependencies without inventing anything.

## Decision

### Location, naming, statuses

- ADRs live in `docs/adr/NNNN-kebab-title.md`, numbered sequentially from 0000.
- Statuses: `Draft` → `Proposed` (ready for review) → `Accepted` → `Implemented`;
  terminal alternative: `Superseded by ADR-XXXX` (never delete or rewrite history — write
  a new ADR and link both ways).
- An accepted ADR is the source of truth for its topic. The blueprint is kept in sync;
  if they disagree, the newest accepted ADR wins and the blueprint gets updated.

### Template (every section, in this order)

| Section | Purpose |
|---|---|
| Header | status, date, deciders, tags, relates-to/supersedes links |
| **Context & Need** | the problem, why now, what breaks without a decision |
| **Scope** | explicitly in / out — keeps the ADR at one altitude |
| **Constraints & Decision drivers** | hard requirements (licensing, platforms, blueprint rules) and the criteria used to judge alternatives |
| **Alternatives considered** | each option with pros/cons and the reason it lost |
| **Decision** | the final solution, stated plainly |
| **Temporary workarounds** | accepted stopgaps, each with an explicit exit criterion |
| **Contracts** | the LLM-handoff heart: Go interfaces, CRD-like resource definitions, and a dependencies & I/O table (what the component consumes — ports, config keys, events, files — and what it exposes) |
| **Scaffold plan** | machine-actionable: files to create, dependencies to add, commands to run, definition of done |
| **Review checklist** | what the gate reviewer verifies, point by point |
| **Consequences** | positive / negative / risks accepted |
| **Open questions** | known unknowns, each with the ADR or milestone where it gets answered |
| **References** | upstream docs, issues, prior art |

Sections that don't apply may say "None", but must be present — their absence is what
review gates catch.

### Workflow gates

1. **Brainstorm** a topic; clarify unknowns with the human before drafting.
2. **Draft the ADR** (`Proposed`); human review → `Accepted`.
3. **Scaffold from the ADR + blueprint**: interfaces, API facades, `go.mod` additions,
   config stubs — declarations only, no business logic.
4. **Review gate**: a high-capability reviewer (e.g. Opus-class / "ultra" code review)
   validates the scaffold against the ADR's *Review checklist* and *Contracts*. Findings
   loop back to step 3 (or amend the ADR if the decision itself was wrong).
5. **Implement** the feature and its tests; ADR moves to `Implemented`.

## Consequences

- (+) Decisions are auditable; scaffolding is reproducible from text; no big-bang code.
- (+) The review gate has an objective checklist instead of vibes.
- (−) Process overhead for trivial choices — mitigated: tiny decisions can be a one-line
  entry in an existing ADR's *Open questions* resolution rather than a new file.

## References

- MADR (template inspiration), Michael Nygard's original ADR article.
- [blueprint.md](../../blueprint.md) — architecture the ADRs decide *into*.
