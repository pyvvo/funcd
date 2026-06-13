# ADR-0000: ADR process, template, and workflow gates

- **Status**: Accepted
- **Date**: 2026-06-13 (amended 2026-06-13: added the feature-version layer `docs/feat/`;
  added the *Scenarios* template section and test skeletons in the scaffold phase; added
  the validation and optional LLM-judge gates)
- **Deciders**: green-0-rabbit
- **Tags**: meta, process

## Context & Need

funcd is designed in [blueprint.md](../../blueprint.md) but built **gradually**: each
component or feature goes through a decision phase before any code exists. We need a
single, repeatable decision format that (a) records why a choice was made, and (b) carries
enough contract detail that an LLM — given only the blueprint and one ADR — can scaffold
the corresponding interfaces, facades, and dependencies without inventing anything.

## Decision

### Feature-version documents (`docs/feat/`) — the layer above ADRs

Before any ADR exists for a topic, the **initial need** and the **high-level
feature/component list per version** live in `docs/feat/NNNN-feat-<version>.md`
(e.g. `0000-feat-v1.md`). Rules:

- A feat doc captures **what** a version must contain and **why** — never how. If a
  sentence describes an interface, a library choice, or a mechanism, it belongs in an
  ADR, not here.
- Each feature row maps to the ADR(s) that realize it, with a status:
  `idea → adr → accepted → scaffolded → implemented`. Feat docs are **living**
  documents (tracking tables update as work progresses) — unlike ADRs, which are
  immutable once accepted.
- Every ADR names the feature(s) it realizes in its header (`Realizes: FEAT-0000/F05`);
  an ADR for a topic outside the active version's scope is a signal to either amend the
  feat doc deliberately or defer the ADR.

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
| Header | status, date, deciders, tags, realizes (feat row), relates-to/supersedes links |
| **Context & Need** | the problem, why now, what breaks without a decision — and the **purpose** of the component stated plainly (what it is for, who calls it): purpose is what tells the implementer *what to test* |
| **Scenarios** | the concrete situations that led to requesting this feature, written Given/When/Then from the user's (or caller's) point of view. Each scenario is observable behavior — no implementation detail — and becomes a named e2e/acceptance test at scaffold time. If a behavior matters and no scenario covers it, the brainstorm isn't done |
| **Scope** | explicitly in / out — keeps the ADR at one altitude |
| **Constraints & Decision drivers** | hard requirements (licensing, platforms, blueprint rules) and the criteria used to judge alternatives |
| **Alternatives considered** | each option with pros/cons and the reason it lost |
| **Decision** | the final solution, stated plainly |
| **Temporary workarounds** | accepted stopgaps, each with an explicit exit criterion |
| **Contracts** | the LLM-handoff heart: Go interfaces, CRD-like resource definitions, and a dependencies & I/O table (what the component consumes — ports, config keys, events, files — and what it exposes) |
| **Scaffold plan** | machine-actionable: files to create, dependencies to add, commands to run, **test skeletons** (contract/unit stubs against the Contracts + one e2e skeleton per Scenario — compiling, marked skipped/failing until implementation), definition of done |
| **Review checklist** | what the gate reviewer verifies, point by point |
| **Consequences** | positive / negative / risks accepted |
| **Open questions** | known unknowns, each with the ADR or milestone where it gets answered |
| **References** | upstream docs, issues, prior art |

Sections that don't apply may say "None", but must be present — their absence is what
review gates catch.

### Workflow gates

0. **Scope the version**: `docs/feat/NNNN-feat-<version>.md` captures the initial need
   and the high-level feature list (see above). Topics come from this list.
1. **Brainstorm** a topic; clarify unknowns with the human before drafting — including
   the *scenarios* that motivated the feature (they become the ADR's Scenarios section
   and, later, its e2e tests).
2. **Draft the ADR** (`Proposed`); human review → `Accepted`. Update the feat doc's
   tracking row (`idea → adr → accepted`).
3. **Scaffold from the ADR + blueprint**: interfaces, API facades, `go.mod` additions,
   config stubs — declarations only, no business logic — **plus test skeletons**:
   contract/unit stubs derived from *Contracts* and one e2e skeleton per *Scenario*,
   compiling but skipped/failing. The skeletons are the executable form of the ADR; the
   implementer's job in step 5 is to make them pass and extend them, never to start
   testing from scratch.
4. **Review gate**: a high-capability reviewer (e.g. Opus-class / "ultra" code review)
   validates the scaffold against the ADR's *Review checklist*, *Contracts*, and
   *Scenarios* (every scenario has a named skeleton). Findings loop back to step 3 (or
   amend the ADR if the decision itself was wrong).
5. **Implement** the feature, un-skip and complete the test skeletons, extend them as the
   implementation reveals edge cases.
6. **Validate**: run the full suite — unit, contract, integration, e2e — and confirm every
   scenario skeleton from the ADR now passes. Failures loop back to step 5. On green:
   ADR moves to `Implemented`, feat row to `implemented`.
7. **LLM judge (optional)**: an independent high-capability model audits the
   implementation *and* its tests against the ADR — every Scenario covered honestly (no
   weakened or deleted assertions), Contracts respected, Review checklist still true —
   and files a short conformance report. Discrepancies either fix the code/tests or, if
   the decision itself proved wrong, trigger a superseding ADR. The judge reads the ADR
   and the diff; it does not re-litigate the decision.

## Consequences

- (+) Decisions are auditable; scaffolding is reproducible from text; no big-bang code.
- (+) The review gate has an objective checklist instead of vibes.
- (−) Process overhead for trivial choices — mitigated: tiny decisions can be a one-line
  entry in an existing ADR's *Open questions* resolution rather than a new file.

## References

- MADR (template inspiration), Michael Nygard's original ADR article.
- [blueprint.md](../../blueprint.md) — architecture the ADRs decide *into*.
