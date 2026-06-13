---
name: roadmap-planner
description: Plan a realistic delivery roadmap for a funcd feature-version — sequence the ADRs that realize a FEAT-NNNN doc into build waves, a critical path, and a design-track schedule, written to docs/roadmap/. Use whenever the user wants to plan, sequence, or order the work for a version — "plan the V2 roadmap", "what order do we build the ADRs", "sequence the work for FEAT-0001", "which ADRs block which", "make a delivery plan", "how do we get to the V1 exit criterion", "plan the build order" — even if they don't say "roadmap". Derives REAL build dependencies from the blueprint + accepted ADRs, computes waves/critical-path/graph with a bundled analyzer (never hand-drawn), and maps every exit-criterion clause to the items that deliver it. Produces a living plan, not an ADR; it sequences decisions, it doesn't make them.
---

# Roadmap planner

Turn a feature-version doc (`docs/feat/NNNN-feat-<version>.md`) into a delivery roadmap that is
*true*: real build dependencies, computed waves, a critical path, and an exit-criterion spine that
guarantees the plan ends in a working feature. Output lives in `docs/roadmap/<version>-delivery-plan.md`.

This is a **planning** artifact, not a decision — it commits to no architecture (ADRs do that). It
answers: in what order must things be built, what runs in parallel, and which ADRs to create+accept
ahead so the build track never stalls.

Read **`references/methodology.md` first** — it is the realism discipline (the five rules + the
failure-mode checklist). The mechanics live in **`scripts/plan_waves.py`**; the output structure in
**`references/plan-template.md`**.

The version usually arrives as the argument (`/roadmap-planner v2` or a FEAT path). If none, ask
which version — and confirm its feat doc exists (`ls docs/feat/`).

## Step 1 — Orient

1. Read the target `docs/feat/NNNN` doc in full: the **feature list** (your items-to-sequence) and
   the **exit criterion** (your spine, and the definition of "done").
2. Read `blueprint.md` — the architecture and component relationships. This is where build
   dependencies are *grounded* (who calls whom, which port implements which interface, what the
   layout implies). Do not invent edges you can't trace to it or to an accepted ADR.
3. Read `docs/adr/0000-adr-process.md` (the gate workflow this plan feeds) and skim
   `docs/adr/` + `docs/roadmap/` — what's already Accepted, the ADR numbering, and any prior plan to
   stay consistent with.

## Step 2 — Derive the dependency table (the realism core)

For each feature, decide the ADR-item(s) that realize it (one ADR = one coherent decision —
methodology rule #3), then determine its **build** dependencies: what must already be built for its
code to compile/run. Every edge must be groundable in one line from the blueprint or an accepted ADR
(rule #1). Distinguish build edges (hard, go in the table) from design/acceptance order (a note, not
an edge). Where a dependency is genuinely ambiguous or a scoping choice is the user's, ask — in one
batched `AskUserQuestion` (recommended option first), not a guess.

Group features into items and assign placeholders (`P-A`, `P-B`, …); accepted ADRs keep their real
number. If a single item's features clearly belong to different build tiers, plan to **split** it.

## Step 3 — Compute (don't hand-draw) the structure

Write the table as `plan.json` (schema in `scripts/plan_waves.py`'s header — `accepted`, then
`items` with `id`/`title`/`features`/`depends_on`) and run it:

```bash
python3 <skill>/scripts/plan_waves.py plan.json
```

It will either reject a **cycle** (fix the edges — a cycle is unbuildable) or emit the **build
waves**, **critical path**, **leaf items**, and a **Mermaid graph generated from the edges**. If an
item's features landed in different tiers, split the item in `plan.json` and re-run. Keep `plan.json`
alongside the plan (or inline in a fenced block) so the tool can be re-run when deps change.

## Step 4 — Validate the graph

Validate the emitted Mermaid block with the mermaid validation tool before pasting it. Because the
graph is generated from the table, it cannot drift from it — but it still must parse.

## Step 5 — Write the plan

Follow `references/plan-template.md` section-for-section. Paste the **computed** waves / critical
path / leaves / graph from Step 3; write the judgment sections (two-track framing, design-track
schedule one wave ahead, riskiest-ADR call-outs, parallelization) yourself.

**The exit-criterion spine is required**: map every clause of the feat doc's exit criterion to the
item(s) that deliver it. A clause with no item is a gap — stop and flag it (the plan or the feat doc
is incomplete) rather than papering over it.

Add the cross-cutting sequencing notes (methodology rule #5): the test-harness / logger-root /
error-kernel ordering, so the plan doesn't silently contradict the implement gate.

## Step 6 — Realism self-audit + finish

Run the failure-mode checklist in `references/methodology.md` against the draft: edges grounded, no
cycle, graph ≡ table (guaranteed by the tool), no item straddles tiers, no step assumes an unbuilt
prerequisite, every exit-criterion clause covered, design kept a wave ahead, numbering uses
placeholders. Then:

- Status is **Active** (a living plan — say so; it updates as ADRs land, unlike immutable ADRs).
- Identity rules: never write the local machine username or local filesystem paths into the file;
  module paths use `github.com/green-0-rabbit/funcd`. Grep the file before finishing.
- Close by pointing at the first unblocked move (the wave-1 items whose deps are already Accepted)
  and the highest-leverage design start.

## What this skill does NOT do

It does not create or accept ADRs (that's `/adr`), judge them (`/adr-judge`), or implement code
(`/adr-impl`). It sequences them. If planning reveals a missing decision, note it as an item to
be decided — don't decide it here.
