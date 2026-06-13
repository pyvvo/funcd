---
name: adr
description: Brainstorm a funcd design topic and drive it to a completed ADR (Draft → Proposed → Accepted) in docs/adr/. Use whenever the user wants to decide, design, or brainstorm any funcd component, feature, dependency choice, or cross-cutting concern — "next ADR", "let's decide the store port", "brainstorm the gateway", "which library for X", "create an ADR about Y" — even if they never say the word "ADR". Stops at acceptance; scaffolding and implementation are separate later phases.
---

# Brainstorm → ADR

Drive one design topic from open question to an **Accepted** ADR. The ADR is the source
of truth for its topic and must carry enough contract detail that an LLM — given only
[blueprint.md](../../../blueprint.md) and the ADR — can scaffold interfaces, facades, and
dependencies without inventing anything. That bar shapes every step below.

The topic usually arrives as the skill argument (`/adr store port`). If there is no topic,
ask for one before doing anything else.

## Step 0 — Orient (before asking the user anything)

1. Read `docs/adr/0000-adr-process.md` — the canonical template, section order, and
   status lifecycle. Do not improvise a different structure.
2. List `docs/adr/` to find the next sequential number and any related or conflicting
   ADRs. If an existing Accepted ADR covers part of the topic, plan to *supersede* or
   *relate to* it — never silently rewrite it.
3. Read the relevant section(s) of `blueprint.md` — the architecture the decision must
   slot into. Constraints stated there (library-first, embed-first, single binary,
   Apache-2.0/MIT-only deps, Makefile-only, import discipline) are inherited, not re-asked.
4. Check `docs/legacy/IMPLEMENTATION.md` for pre-ADR raw material on the topic (gateway
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

## Step 2 — Draft the ADR

Write `docs/adr/NNNN-kebab-title.md` with **Status: Draft** and every template section
from ADR-0000, in order — a section that does not apply says "None" but still appears
(its absence is what review gates catch):

Header (status, date, deciders, tags, relates-to/supersedes) · Context & Need · Scope
(in/out) · Constraints & Decision drivers · Alternatives considered · Decision ·
Temporary workarounds · Contracts · Scaffold plan · Review checklist · Consequences ·
Open questions · References.

Quality bar per section:

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
- **Scaffold plan**: machine-actionable — files to create, `go.mod` additions, commands
  to run, and a checkable definition of done. No business logic ever belongs in it.
- **Review checklist**: objective, checkbox-form items the review-gate model can verify
  mechanically against the scaffold.
- **Open questions**: each one names where it gets answered (a future ADR, a milestone,
  the scaffold PR).

## Step 3 — Review to acceptance

1. Present the user a short summary: the decision itself, the alternatives that lost and
   why, the workarounds, and anything you flagged as an open question. Link the file —
   don't paste the whole ADR into chat.
2. Revise on feedback. When the user is satisfied with the content, set
   **Status: Proposed**; when they explicitly accept, set **Status: Accepted** with the
   date. Acceptance is the user's call — never self-accept.
3. An Accepted ADR is immutable in substance: later changes happen by writing a new ADR
   that supersedes it (link both ways), never by editing history.

## Step 4 — Sync the blueprint

If the accepted decision refines or contradicts `blueprint.md`, update the blueprint in
the same session (focused edits, not rewrites). The rule from ADR-0000: the newest
accepted ADR wins and the blueprint follows.

## Step 5 — Stop

This skill ends at acceptance. Do **not** scaffold, create packages, or touch `go.mod`.
Close with a handoff note: the ADR number/title, its status, and a one-line pointer that
the next phase is executing its *Scaffold plan* followed by the high-capability review
gate against its *Review checklist*.

## Project conventions (apply silently throughout)

- **Identity**: `Deciders: green-0-rabbit`; module paths use
  `github.com/green-0-rabbit/funcd`; copyright "The funcd Authors". Never write the
  local machine username or local filesystem paths into repo files — before finishing,
  grep the changed files for the local username to verify nothing leaked.
- Filenames: `NNNN-kebab-case-title.md`, numbers sequential, never reused.
- All dependencies proposed in an ADR must be Apache-2.0/MIT-compatible — check the
  license during Step 1, not after acceptance.
