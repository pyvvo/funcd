# ADR-0000: ADR process, template, and workflow gates

- **Status**: Accepted
- **Date**: 2026-06-13 (amended 2026-06-13: added the feature-version layer `docs/feat/`;
  added the *Scenarios* template section; added the ADR-judge gate that reviews the
  decision itself before acceptance — see the `adr-judge` skill; named the skill that runs
  each gate (`roadmap-planner`, `adr`, `adr-judge`, `adr-impl`, `adr-impl-review`) and added
  the per-model quality scorecard recorded by the review gate. Further amended 2026-06-13:
  **collapsed the former separate *Scaffold* and *Implement* gates into a single *Implement*
  gate** (the `adr-impl` skill) that produces working code with passing scenario tests —
  there is no skeleton-with-skipped-tests phase anymore; added the `Reviewing` ADR status and
  the `reviewing` feat status for the window between implementation and review sign-off; made
  the review gate the sole authority that stamps `Implemented`; renamed the template's
  *Scaffold plan* section to *Implementation plan* — ADR-0001/0002 predate the rename and keep
  the old heading, which the build/review skills treat as the same section. Feat rows formerly
  at `scaffolded` remap to `accepted`, since the new *Implement* gate produces working code the
  old scaffold phase did not.)
- **Deciders**: green-0-rabbit
- **Tags**: meta, process

## Context & Need

funcd is designed in [blueprint.md](../../blueprint.md) but built **gradually**: each
component or feature goes through a decision phase before any code exists. We need a
single, repeatable decision format that (a) records why a choice was made, and (b) carries
enough contract detail that an LLM — given only the blueprint and one ADR — can implement
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
  `idea → adr → accepted → reviewing → implemented`. Feat docs are **living**
  documents (tracking tables update as work progresses) — unlike ADRs, which are
  immutable once accepted.
- Every ADR names the feature(s) it realizes in its header (`Realizes: FEAT-0000/F05`);
  an ADR for a topic outside the active version's scope is a signal to either amend the
  feat doc deliberately or defer the ADR.

### Location, naming, statuses

- ADRs live in `docs/adr/NNNN-kebab-title.md`, numbered sequentially from 0000.
- Statuses: `Draft` → `Proposed` (ready for review) → `Accepted` → `Reviewing` (code
  written, awaiting the review gate) → `Implemented`; terminal alternative:
  `Superseded by ADR-XXXX` (never delete or rewrite history — write a new ADR and link
  both ways).
- An accepted ADR is the source of truth for its topic. The blueprint is kept in sync;
  if they disagree, the newest accepted ADR wins and the blueprint gets updated.

### Template (every section, in this order)

| Section | Purpose |
|---|---|
| Header | status, date, deciders, tags, realizes (feat row), relates-to/supersedes links |
| **Context & Need** | the problem, why now, what breaks without a decision — and the **purpose** of the component stated plainly (what it is for, who calls it): purpose is what tells the implementer *what to test* |
| **Scenarios** | the concrete situations that led to requesting this feature, written Given/When/Then from the user's (or caller's) point of view. Each scenario is observable behavior — no implementation detail — and becomes a named acceptance test when the ADR is implemented. If a behavior matters and no scenario covers it, the brainstorm isn't done |
| **Scope** | explicitly in / out — keeps the ADR at one altitude |
| **Constraints & Decision drivers** | hard requirements (licensing, platforms, blueprint rules) and the criteria used to judge alternatives |
| **Alternatives considered** | each option with pros/cons and the reason it lost |
| **Decision** | the final solution, stated plainly |
| **Temporary workarounds** | accepted stopgaps, each with an explicit exit criterion |
| **Contracts** | the LLM-handoff heart: Go interfaces, CRD-like resource definitions, and a dependencies & I/O table (what the component consumes — ports, config keys, events, files — and what it exposes) |
| **Implementation plan** | machine-actionable: files to create, dependencies to add, commands to run, the **test plan** (contract/unit tests against the Contracts + one acceptance test per Scenario, each written to pass), and the definition of done. (ADR-0001/0002 predate the rename and title this section *Scaffold plan* — same section.) |
| **Review checklist** | what the gate reviewer verifies, point by point |
| **Consequences** | positive / negative / risks accepted |
| **Open questions** | known unknowns, each with the ADR or milestone where it gets answered |
| **References** | upstream docs, issues, prior art |

Sections that don't apply may say "None", but must be present — their absence is what
review gates catch.

### Workflow gates

0. **Scope the version**: `docs/feat/NNNN-feat-<version>.md` captures the initial need
   and the high-level feature list (see above). The `roadmap-planner` skill then sequences
   those features' ADRs into a computed delivery plan under `docs/roadmap/` (build waves,
   critical path, design-track order). Topics come from this list, in that order.
1. **Brainstorm** a topic; clarify unknowns with the human before drafting — including
   the *scenarios* that motivated the feature (they become the ADR's Scenarios section
   and, later, its e2e tests).
2. **Draft the ADR**: write every template section; mark `Proposed` once it is ready for
   review. No code yet — the decision and its *Contracts* must stand on their own.
3. **Judge gate (the ADR itself)**: before the human accepts, the ADR *document* is judged
   against the blueprint goal, its feat row, the related/Accepted ADRs, and this template —
   goal alignment, internal & cross-document consistency, bias in *Alternatives*, contract
   bugs, scope creep, and the strengths worth keeping. The `adr-judge` skill makes this
   repeatable: it produces an evidence-cited, severity-tiered verdict (Blocker / Major /
   Minor / Nit) and names what to keep as-is, not only what is wrong. Blocker/Major
   findings loop back to step 2; the human weighs the verdict and makes the call →
   `Accepted`. Update the feat doc's tracking row (`idea → adr → accepted`). Acceptance is
   always the human's decision — the judge advises, it never accepts.
4. **Implement from the ADR + blueprint** (the `adr-impl` skill): turn the Accepted ADR into
   *working* code — port interfaces and one-file drivers, functional-options facades,
   `api/fault` errors, `go.mod` additions — **and the scenario tests, written to pass**: a
   contract/unit test per *Contract* and one acceptance test per *Scenario*, all green, with
   the full `just ci` exiting 0. Real business logic conforming to the ADR's *Contracts*; no
   skeleton-with-skipped-tests phase. On exit, advance the ADR `Accepted → Reviewing` and the
   feat row to `reviewing`, then hand to the review gate. If the ADR cannot be made green
   within its sanctioned scope (contradictory Contracts, an unsatisfiable Scenario), that is
   an ADR defect to flag — not scope to invent.
5. **Review gate** — the `adr-impl-review` skill: it *runs* the verification (build/lint/test,
   tree-vs-*Repository surface* diff, identity grep) and validates the implementation against
   the ADR's *Review checklist*, *Contracts*, *Scenarios* (every scenario has a named, passing
   test — none weakened or deleted), and the generic Definition of Done — the same
   goal-anchored, evidence-cited discipline as the judge gate, pointed at code. It emits a
   severity-tiered verdict (Blocker / Major / Minor / ✅ keep) and **records a per-model
   quality entry** (see below). On a **pass** it advances the ADR `Reviewing → Implemented`
   and the feat row to `implemented` — the review gate is the sole authority that stamps
   `Implemented`. On **changes-requested** / **fail** it advances nothing (the ADR stays
   `Reviewing`): `model` findings loop back to step 4 for rework, `adr` findings loop back to
   step 2 with a superseding ADR (a finding caused by an ADR defect is attributed to the ADR,
   not the model). It reviews and records but never fixes the code — that keeps the score honest.

### The model scorecard (a process artifact, not a document layer)

The review gate (gate 5) takes the **name of the model that produced the work**
(`sonnet-4.6`, `deepseek-v4-pro`, `gpt-5.4-mini`, `claude-opus-4-8`, …) and appends a record
to `docs/reviews/model-ledger.json`, regenerating `docs/reviews/model-scorecard.md` — a
per-model rollup (reviews, pass rate, avg findings, DoD pass rate) so models are comparable
at ADR-implementation quality over time. Fairness rule: **only findings attributed to the
model count against its score**; findings caused by an ADR defect or the environment are
recorded but excluded. This ledger informs nothing in the four planning layers — it is a
quality-tracking byproduct, append-only.

## Consequences

- (+) Decisions are auditable; implementation is reproducible from text; no big-bang code.
- (+) The review gate has an objective checklist instead of vibes.
- (+) The decision itself gets an independent, goal-anchored review *before* it is frozen
  (the judge gate): inconsistency, bias, and scope creep are caught while the ADR is still
  cheap to change, and genuine strengths are flagged to keep rather than accidentally lost.
- (+) The review gate *runs* the verification rather than eyeballing it, and records a
  per-model scorecard — so "is it done?" is evidence-backed, and model quality at
  implementing ADRs becomes measurable and comparable instead of anecdotal.
- (−) Process overhead for trivial choices — mitigated: tiny decisions can be a one-line
  entry in an existing ADR's *Open questions* resolution rather than a new file.

## References

- MADR (template inspiration), Michael Nygard's original ADR article.
- [blueprint.md](../../blueprint.md) — architecture the ADRs decide *into*.
