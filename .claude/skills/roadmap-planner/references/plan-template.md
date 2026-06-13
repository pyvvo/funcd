# Delivery-plan template

Write the plan to `docs/roadmap/<version>-delivery-plan.md` with these sections in order. Fill the
computed sections (graph, waves, critical path, leaves) from `scripts/plan_waves.py` output — do not
re-derive them by hand. Prose sections carry the judgment.

```markdown
# <VERSION> delivery plan — ADR sequencing to ship FEAT-NNNN

- **Status**: Active (living — update as ADRs are created/accepted/implemented)
- **Date**: <YYYY-MM-DD>
- **Realizes**: [FEAT-NNNN (<version> — <name>)](../feat/NNNN-feat-<version>.md)
- **Process**: [ADR-0000](../adr/0000-adr-process.md) · skills `/adr` → `/adr-judge` → `/adr-impl` → `/adr-impl-review`

## Purpose & how to read this

<one paragraph: this sequences the ADRs that realize FEAT-NNNN's N features so the version becomes
implementable without dead-ends; it is a plan, not a decision. State the exact feature count and any
numbering gaps.>

**Two tracks — the core idea** (this design/build framing is this plan's own lens, not ADR-0000
vocabulary): <2–3 lines on design track vs build track and "keep design one wave ahead".>

> **ADR numbers here are placeholders** (`P-A …`). `/adr` assigns the real number at creation.
> Track by **feature code** (stable). Accepted ADRs use their real number.

## Proposed ADR slate

| Plan id | Proposed ADR working title | Realizes | Build-depends on |
|---|---|---|---|
<one row per item. "Build-depends on" lists the item ids it needs built first — and this column is
the authoritative dependency table the tool consumes. Note splits/merges and their reasons.>

## Build dependency graph

<paste the Mermaid block emitted by plan_waves.py, AFTER validating it with the mermaid tool. Add
the line: "The slate table is authoritative; this graph is generated from it.">

## Build waves (computed)

<paste the wave table from plan_waves.py. If you present coarser hand-grouped phases, say so and
confirm they don't violate the computed tiers.> For each wave add a short "why this tier" note.

### Cross-cutting sequencing notes

<call out the test-harness / logger-root / error-kernel ordering constraints that apply (see
methodology rule #5), so the plan doesn't contradict the implement process.>

## Design track — what to create + accept ahead

<wave-by-wave: which ADRs to draft + judge + accept while the prior build wave runs, keeping design
one wave ahead. Name where to spend the most review attention (riskiest ADRs).>

## Critical path & the exit-criterion spine

<paste the critical path from plan_waves.py.> Then the spine table — REQUIRED:

| Exit-criterion clause | Needs (items) |
|---|---|
<one row per clause of FEAT-NNNN's exit criterion → the items that satisfy it. A clause with no
item is a gap: stop and flag it.>

<one line: at which wave the exit criterion is satisfied, and which trailing items are polish.>

## Parallelization & sequencing notes

- <the leaf items from the tool — safe to parallelize / defer.>
- <the riskiest ADRs and why they deserve extra design+review budget.>
- <items off the exit-criterion spine that can trail.>

## Caveats (living doc)

- Real ADR numbers are assigned by `/adr` at creation; reconcile placeholders as ADRs land.
- Waves are dependency tiers, not a schedule — within a wave, sequence by review bandwidth.
- If a drafted ADR reveals a missed dependency, update `plan.json` and re-run the tool; re-validate
  the graph. (The accepted ADR still wins for architecture; this plan only tracks ordering.)
- Later versions get their own delivery plan when their feat doc is scoped.
```
