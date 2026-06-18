---
name: adr
description: Brainstorm a funcd design topic and drive it to a completed ADR (Draft → Proposed → Accepted) in docs/adr/. Use whenever the user wants to decide, design, or brainstorm any funcd component, feature, dependency choice, or cross-cutting concern — "next ADR", "let's decide the store port", "brainstorm the gateway", "which library for X", "create an ADR about Y" — even if they never say the word "ADR". Stops at acceptance; implementation and review are separate later phases.
---

# Brainstorm → ADR

Drive one design topic from open question to an **Accepted** ADR. The ADR is the source
of truth for its topic and must carry enough contract detail that an LLM — given only
[blueprint.md](../../../blueprint.md) and the ADR — can implement interfaces, facades, and
dependencies without inventing anything. That bar shapes every step below.

The topic usually arrives as the skill argument (`/adr store port`). If there is no topic,
ask for one before doing anything else.

## Step 0 — Orient (before asking the user anything)

1. Read `docs/adr/0000-adr-process.md` — the canonical template, section order, and
   status lifecycle. Do not improvise a different structure.
2. Read the active feature-version document (`docs/feat/`, highest-numbered Active file)
   and locate the feature row the topic realizes. If the topic is **not** in the active
   version's scope, say so and let the user choose: amend the feat doc deliberately, or
   defer the ADR. The ADR header gets a `Realizes: FEAT-NNNN/Fxx` line.
3. List `docs/adr/` to find the next sequential number and any related or conflicting
   ADRs. If an existing Accepted ADR covers part of the topic, plan to *supersede* or
   *relate to* it — never silently rewrite it.
4. Read the relevant section(s) of `blueprint.md` — the architecture the decision must
   slot into. Constraints stated there (library-first, embed-first, single binary,
   Apache-2.0/MIT-only deps, one task runner — `just`, import discipline) are inherited,
   not re-asked.
5. Check `docs/legacy/IMPLEMENTATION.md` for pre-ADR raw material on the topic (gateway
   rendering mechanics, scale-to-zero ordering, lifecycle sequences) worth mining.

## Step 1 — Brainstorm

Research **before** questioning, so questions are informed rather than lazy:

- Verify external facts the decision hinges on (library maintenance status, license,
  feature claims, release cadence) with WebFetch/WebSearch — never from memory. Record
  what was checked and when; it feeds *Alternatives considered* and *References*.
- Derive everything derivable from the blueprint, existing ADRs, and the repo. Only the
  genuinely open, user-owned decisions become questions.

Then ask the user the open questions in one batch (AskUserQuestion, 2–4 questions,
recommended option first with "(Recommended)" suffix and honest trade-offs in the
descriptions). Iterate with a second batch only if an answer opens a new branch. Good
questions decide the ADR's *Decision* section; bad questions ask the user to do the
research you skipped.

**Capture the scenarios.** Part of the brainstorm — not an afterthought — is collecting
the concrete situations that led to requesting this feature: who calls it, with what, and
what observable outcome they expect. Propose the scenarios you can derive (from the feat
doc, blueprint, and the conversation) and ask the user only for the ones you can't.
These become the ADR's *Scenarios* section and later its e2e tests, so each must be
phrased as observable behavior (Given/When/Then), never as implementation steps. A
feature whose purpose can't be expressed as scenarios isn't understood well enough to
decide on.

## Step 2 — Draft the ADR

Write `docs/adr/NNNN-kebab-title.md` with **Status: Draft** and every template section
from ADR-0000, in order — a section that does not apply says "None" but still appears
(its absence is what review gates catch):

Header (status, date, deciders, tags, realizes, relates-to/supersedes) · Context & Need ·
Scenarios · Scope (in/out) · Constraints & Decision drivers · Alternatives considered ·
Decision · Temporary workarounds · Contracts · Implementation plan · Review checklist ·
Consequences · Open questions · References.

**Be concise — the overriding style rule.** An ADR records a *decision* and the contracts to
implement it, not an essay. Every sentence must change what gets built or how it's judged; if it
doesn't, cut it. No restating the same point across Decision/Consequences/Constraints, no
background the reader already has, no hedging or motivational prose, no "precision" that adds
words but not decisions. Prefer a tight sentence to a paragraph, a table or signature to prose.
Completeness means *every template section present and every contract specified* — not verbose.
A bloated ADR is a defect the judge should flag, the same as a missing section.

Quality bar per section:

- **Context & Need**: states the component's *purpose* plainly — what it is for and who
  calls it. Purpose is what tells the implementer what to test. A few sentences, not a history.
- **Scenarios**: Given/When/Then, from the caller's point of view, observable outcomes
  only. Give each a short stable name (`scenario: cold-start-wake`) — the acceptance test
  written at implementation time carries the same name, so scenario ↔ test traceability is
  grep-able.
- **Scope**: one altitude per ADR. If the draft starts deciding a neighboring topic,
  split it out and note it as a follow-up ADR instead.
- **Alternatives considered**: each option gets honest pros/cons and the concrete reason
  it lost — "rejected" without a reason is not a record.
- **Temporary workarounds**: every workaround carries an explicit exit criterion; a
  workaround without one is undocumented debt.
- **Contracts**: Go interfaces with full signatures (compilable, not pseudocode),
  CRD-like resource YAML where applicable, and a dependencies & I/O table — what the
  component consumes (ports, config keys, events, files) and exposes. Write for a
  cold-start reader: no references to "as discussed".
- **Implementation plan**: machine-actionable — files to create, `go.mod` additions, commands
  to run, the **test plan** (contract/unit tests derived from *Contracts*, plus one acceptance
  test per *Scenario*, each written to pass — the implement gate ends green on `just ci`), and a
  checkable definition of done. The tests are the executable form of the ADR; the plan lists
  them so the implementer builds them against the Contracts, never from scratch. No business
  logic ever belongs in the plan itself. (ADRs predating the rename title this section *Scaffold
  plan* — same section.)
- **Review checklist**: objective, checkbox-form items the review-gate model can verify
  mechanically against the implementation.
- **Open questions**: each one names where it gets answered (a future ADR, a milestone,
  the implementation PR).

## Step 3 — Review to acceptance

1. Present the user a short summary: the decision itself, the alternatives that lost and
   why, the workarounds, and anything you flagged as an open question. Link the file —
   don't paste the whole ADR into chat.
2. When the draft is ready for review, set **Status: Proposed**, then run the **judge gate**
   (ADR-0000 gate #3): hand the ADR to the `adr-judge` skill for an evidence-cited,
   severity-tiered verdict — inconsistency, bias, scope creep, contract bugs, and the
   strengths worth keeping. Loop its Blocker/Major findings back into the draft and revise.
3. When the user is satisfied (judge findings addressed), set **Status: Accepted** with the
   date on their explicit go-ahead. Acceptance is the user's call — never self-accept; the
   judge advises but never accepts.
4. An Accepted ADR is immutable in substance: later changes happen by writing a new ADR
   that supersedes it (link both ways), never by editing history.

## Step 4 — Sync the blueprint and the feat tracking

- If the accepted decision refines or contradicts `blueprint.md`, update the blueprint in
  the same session (focused edits, not rewrites). The rule from ADR-0000: the newest
  accepted ADR wins and the blueprint follows.
- Update the feature row in the active `docs/feat/` document: link the ADR in the
  `ADR(s)` column and advance the status (`idea → adr` at draft, `→ accepted` at
  acceptance).

## Step 5 — Stop

This skill ends at acceptance. Do **not** implement, create packages, or touch `go.mod`.
Close with a handoff note: the ADR number/title, its status, and a one-line pointer to
the remaining gates from ADR-0000 — implement (working code + passing scenario tests, via
`adr-impl`) then the review gate (`adr-impl-review`), which runs the verification and stamps
the ADR `Implemented` on a pass.

## Project conventions (apply silently throughout)

- **Identity**: `Deciders: green-0-rabbit`; module paths use
  `github.com/green-0-rabbit/funcd`; copyright "The funcd Authors". Never write the
  local machine username or local filesystem paths into repo files — before finishing,
  grep the changed files for the local username to verify nothing leaked.
- Filenames: `NNNN-kebab-case-title.md`, numbers sequential, never reused.
- All dependencies proposed in an ADR must be Apache-2.0/MIT-compatible — check the
  license during Step 1, not after acceptance.
