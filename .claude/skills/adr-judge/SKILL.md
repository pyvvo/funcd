---
name: adr-judge
description: Judge / review / critique / red-team a funcd ADR against the platform's actual goals — produces an evidence-cited verdict that flags inconsistency, bias, scope creep, contract bugs, and anything that could break the platform's development, while explicitly naming what is strong and must be kept. Use whenever the user wants an ADR evaluated, audited, stress-tested, or sanity-checked — "judge ADR-0002", "review this ADR", "what do you think of the gateway ADR", "poke holes in it", "is this decision sound", "red-team the store port ADR" — even if they never say the word "judge". This is the ADR-0000 judge gate (#3) — the decision review on the ADR document, run before acceptance — and the same goal-anchored discipline backs the implementation review gate (#5). It evaluates and reports; it does not author ADRs (that is the `adr` skill) and it does not implement.
---

# Judge an ADR

Drive one ADR through a rigorous, goal-anchored critique and produce a calibrated verdict.
The judge's job is **not** taste enforcement and **not** rubber-stamping — it is to protect
the platform's development from decisions that are inconsistent, biased, under-justified, or
quietly corner-painting, *and* to protect the good parts from being "fixed" away. A report
that finds only problems is as much a failure as one that finds only praise: both mean the
judge didn't calibrate against the actual goal.

**Who runs it.** A subagent or a subsession, never the session that wrote the ADR: an author judging its own ADR has
the conflict of interest Step 5 names (the decider, 2026-11-09). The judge gets the files (the ADR, its spec, the code
and the experiment it cites), never the author's own findings or verdict. One judge serves every round of the same
ADR: the author sends it the revised ADR, so the judge's orientation is paid once. A new ADR gets a new judge.

The target ADR usually arrives as the skill argument (`/adr-judge 0002` or a path). If no
ADR is named, ask which one before doing anything else.

## Step 0 — Orient (read before judging anything)

Judge against the project's reality, never from memory or generic Go-style priors:

1. Read the target ADR in full.
2. Read `docs/adr/0000-adr-process.md` — the canonical template, section order, status
   lifecycle, and workflow gates — with ADR-0272, which adds the *Design model* section and the written form of a
   partial supersession. The ADR is measured against *this* structure, not an improvised one.
3. Read the active feature-version document (`docs/feat/`, highest-numbered Active file)
   and find the row the ADR claims to realize (`Realizes: FEAT-NNNN/Fxx`). The feat doc is
   the **what/why**; confirm the ADR actually serves it and stays inside the active
   version's scope (V1 must not silently pull in V2/V3 work).
4. Read the relevant section(s) of `blueprint.md` — the target architecture and the
   platform's purpose. This is the **main goal** the decision must serve. Inherited
   constraints (library-first, embed-first, single binary, ports with ≥2 drivers,
   Apache-2.0/MIT-only deps, one task runner `just`, depguard import discipline,
   `log/slog`-only, crash-only, RFC 9457 on the wire) are the yardstick — an ADR that
   contradicts them needs an explicit, justified reason or it is a defect.
5. List `docs/adr/` and read any **related, superseded, or depended-on** ADRs, each whole and following each
   `Superseded in part by` line: an accepted cost lives in its Scope, Alternatives, Consequences, Risks accepted and
   Open questions, not only in its Decision. Cross-ADR
   contradiction is one of the most damaging defect classes — an ADR that conflicts with an
   *Accepted* ADR is wrong by default (ADR-0000: the newest accepted ADR wins, but a Draft
   does not get to silently override an Accepted one).
6. Read the behavior registry, [docs/behavior/](../../../docs/behavior/README.md): grep the component, the phase, the
   reason and the key the ADR touches, and read each row it hits with its citation. A row says what the platform does
   today and who decided it, so it shows what is already decided and what already exists. Read `.claude/CLAUDE.md` of
   the checkout too: its bug rule, *Grounding* and *Strict rules* bind the report.
7. If the ADR cites external facts the decision hinges on (library maintenance, license,
   feature claims, release cadence), spot-verify the load-bearing ones with WebFetch —
   bias and staleness hide here. Don't re-verify everything; verify what the decision rests on.

## Step 1 — Anchor to the main goal

Before listing findings, state — for yourself — the one-paragraph answer to: *what is this
ADR for, who calls it, and how does it advance the V1 exit criterion / blueprint goal?*
Every finding is then judged by its effect on **that**, not on abstract elegance. This is
what keeps the critique from drifting into bikeshedding. If you cannot articulate the ADR's
purpose from its own Context & Need, that is itself the first (Major) finding.

## Step 2 — Judge along the axes

Evaluate the ADR through every lens below. For each finding, capture **evidence** (section
name and, where possible, a line reference), the **goal impact** (what downstream breaks or
degrades), and a **concrete direction** for the fix. No unsourced claims — "this feels off"
is not a finding.

**A. Goal alignment & scope.** Does the decision advance the feat row and the blueprint
goal? Is it at one altitude (ADR-0000 *Scope*), or is it deciding a neighboring topic that
deserves its own ADR? Does it stay inside the active version's scope, or import deferred
V2/V3 work? Is anything in the feat row's intent left undecided?

**B. Internal consistency.** Do the Decision, Contracts, Implementation plan, and Review checklist
agree with each other? The highest-value defects live here: a Contracts signature that
violates a Decision rule, a depguard rule that forbids an import the same ADR's own
file-placement requires, a scenario with no matching test in the plan, a package named one thing in
prose and another in the file tree. Trace every contract back to a stated rule and every
rule forward to its contract.

**C. Cross-document consistency.** Against the blueprint, the feat doc, and other ADRs:
naming, layering, port boundaries, library choices, terminology. A two-letter near-homonym
for two different concepts (e.g. `store` vs `storage`) is a real defect in a codebase that
will be LLM-implemented — flag it.

**D. Bias & motivated reasoning.** Audit the *Alternatives considered*: is each rejection
backed by a concrete reason, or is the losing option strawmanned so the author's preference
wins? Are pros/cons symmetric, or is the chosen option's cost suspiciously absent? Is a
library picked from familiarity rather than the stated drivers? Is license/maintenance
asserted rather than checked? "Rejected" without a real reason is a documented defect per
ADR-0000 — call it out by name.

**E. Things that could break development (the consequential lens).** Weight findings by blast
radius, not by how easy they are to spot:
   - **Corner-painting / reversibility**: does a choice quietly foreclose the multi-node,
     embed-first, or ports-and-drivers future the blueprint promises? A decision that isn't
     behind a port when the blueprint says it should be is high-severity.
   - **Contract bugs**: signatures that won't compile, an interface that leaks `any` where
     the ADR bans it, an error/edge mapping that can't actually produce the asserted result.
   - **LLM-implementability**: can an LLM implement from *blueprint + this ADR alone* without
     inventing? Ambiguity here directly breaks the ADR-0000 implementation handoff (gate #4).
   - **Missing exit criteria**: a *Temporary workaround* with no exit is undocumented debt.
   - **Security / isolation / multi-tenancy**: for any ADR touching the data plane, sandbox,
     egress, secrets, or identity, hold it against the blueprint's security model — a
     default-allow where the blueprint says default-deny is a Blocker.

**F. Template & contract completeness.** Every ADR-0000 section present (a missing section,
not "None", is a gate failure); Scenarios observable Given/When/Then with stable names;
Implementation plan machine-actionable with a test plan; Review checklist mechanically
checkable. **Also judge concision**: an ADR that restates the same point across sections, pads
with background the reader has, or adds "precision" that changes no decision is bloated — flag the
specific redundant passages (Minor, or Major if the bloat obscures the actual decision/contract)
and name what to cut. Concise ≠ incomplete: every section must still be present and every contract
specified; the target is *no wasted sentence*, not fewer sections.

**G. Strengths worth protecting.** Mandatory, not optional. Name what is done well —
especially deliberate, correctly-justified deviations from idiom (the kind a future reviewer
might "fix" by mistake), sharp alternative analysis, scenarios that will make good tests,
and decisions that buy reversibility cheaply. If a strength is the kind of thing that gets
accidentally undone later, say "keep as-is" explicitly and why.

**H. Design model (ADR-0272).** If the Decision adds or changes a trigger of the
[`fizzbee`](../fizzbee/SKILL.md) skill and the section reads `None`, that is a Blocker. Otherwise rerun the spec
(`scripts/agent/d python3 scripts/specs.py check <spec>`) and read the trace of at least one mutant yourself. Major:
an assertion that protects no Scenario at risk; a guard the Decision adds with no mutant; bounds not justified as
ADR-0272 Decision 2 asks (the smallest scenario in which every mutant fails, as many concurrent actions as actors
racing through yield points, the wider run with one more item recorded in the ADR); a wait or retry with no bound in
modeled time and no progress assertion; an assertion with no named oracle test; an environment assumption a property
rests on (the runtime's stop order, the network or storage a step needs) neither modeled nor named under *Left to
tests* with its real-platform test.
A decision that relies on how the runtime treats a worker during a stop, a crash or a restart, with no real-platform
lane case in its definition of done, is a Major (axis F): tests without funcd's runtime cannot show it.

**I. Behavior rows (ADR-0313).** A Decision that changes what a user sees, with the section reading `None`, or a row it changes and
does not list, is a Major.

## Step 3 — Calibrate before writing

Apply these rules to the findings so the verdict is trustworthy, not just long:

- **Place every finding before you tier it.** A real problem is not always this ADR's problem. Give each finding a
  home:
  - **here**: this ADR's own decision, contracts, scenarios or text must change. Only these get a severity tier and
    can block the ADR.
  - **elsewhere**: another feature or ADR owns it (an Accepted ADR, a feat row, an open issue), or the decider, for a
    finding against a choice they recorded; name it.
  - **above**: the ADR runs into a concept the platform has not decided yet, a cross-cutting policy or objective (a
    recovery objective such as an RPO, a tenancy model, a security model); name the layer that should decide it (the
    blueprint, a feat row, a new ADR).

  The test: would fixing it inside this ADR decide something outside its Scope? Then it is elsewhere or above, and
  it is routed, not tiered. One part stays here: the ADR must state plainly how it behaves against today's platform
  (an honest consequence, a named open question), and hiding or overstating that is a finding here. Example
  (ADR-0273, 2026-10-24): "a kill during the stop loses commits" was tiered Major; its home was partly here (the ADR
  had to state the loss honestly, and keep the Lake's existing one-second save interval) and partly above (how much
  loss a Lake may take is a recovery objective, FEAT-0009 F112), which went to its own ADR.
- **Not a finding of this ADR.** Cite each of these where a reader needs it, and tier none:
  - a behavior an Accepted ADR decided or a registry row records as `decided`, `accepted` or `deferred`, another
    component's included: the memory KV engine keeps no key across a restart by ADR-0066, which is no limit of a KV
    feature;
  - what the ADR's Scope leaves out or its Open questions hold, unless leaving it out breaks a promise the ADR makes;
  - a choice the decider recorded (the ADR's *Constraints* name it): a finding against one goes under *Routed*, to
    the decider, as information, and never blocks;
  - existing code that surprises you: it is "surprising" until the two checks of the bug rule (`.claude/CLAUDE.md`)
    give the verdict `bug`;
  - a migration path, an alias or an upgrade step for an older on-disk or backup format: no funcd node is deployed and
    no workload runs anywhere (the decider, 2026-10-29), so a format change, a rename or a changed default ships
    without one, stated as a consequence;
  - the numbers of a private experiment: an ADR names pyvvo/experiment's timings and pastes no number without the
    decider's go.
- **Judge this ADR, not the platform around it.** A defect of other code or of another ADR that does not bear on this
  decision is neither hunted nor reported. A possible vulnerability goes into no report: tell it only to whoever runs
  the judge, for a private advisory (CLAUDE.md → *Issues*).
- **Severity is about blast radius, not confidence.** Tier every finding placed here:
  - **Blocker** — a contradiction, contract bug, goal/Accepted-ADR conflict, or
    security-model violation within this ADR's scope that will break the implementation or the platform. Must fix
    before the ADR advances (Draft→Proposed, Proposed→Accepted).
  - **Major** — a real weakness (biased/empty alternative analysis, scope creep, missing
    scenario, reversibility risk) that materially weakens the ADR without strictly blocking.
  - **Minor** — clarity/quality improvement, also a correct decision whose fix is one sentence of text (ADR-0091
    and ADR-0128 had such findings tiered Major).
  - **Nit** — cosmetic or taste. Label taste *as* taste.
- **Separate "wrong" from "I'd have chosen differently."** Only defects (inconsistency,
  contradiction, unjustified claim, goal miss) are Blocker/Major. Pure preference goes to
  Nits, explicitly marked — the judge does **not** re-litigate a decision that is in scope
  and honestly justified (this mirrors the review gate's rule: don't reopen a sound decision
  on taste).
- **Don't manufacture findings to look thorough.** If a section is sound, the correct output
  is to say so under Strengths. A short report on a strong ADR is a valid result.
- **Don't rubber-stamp either.** Before concluding "looks good", confirm you actually traced
  contracts↔rules and checked the blueprint/feat/related ADRs — absence of findings must be
  earned, not assumed.
- **Respect immutability.** If the ADR is *Accepted*, judge it for a **superseding** ADR or
  for implementation conformance — never ask to rewrite accepted history.

## Step 4 — Write the report

Produce the report in this shape (sections with nothing to say still appear, marked "None"
— their emptiness is a signal):

```
# ADR-NNNN Judge Report — <title>

**Verdict**: <one honest sentence: is the decision sound, and what stands between it and the
            next status — e.g. "Right decision, two blocking contract bugs before Proposed">
**Judged against**: blueprint <sections> · FEAT-NNNN/Fxx · ADR-0000 template · related ADRs <list>
**ADR status**: <Draft | Proposed | Accepted>

## Goal alignment
<2–4 sentences: the ADR's purpose in its own words, and whether it serves the V1/blueprint goal>

## Strengths — keep as-is
<bulleted, specific, evidence-cited. At least the genuinely strong parts; never empty on a
 real ADR. Mark anything a later edit might wrongly "fix".>

## Findings
### Blockers
### Major
### Minor
### Nits
<each finding placed here: **evidence** (section/line) → **goal impact** → **direction to fix**.
 Omit a tier with the line "None." if it is empty.>

## Routed (not this ADR's)
<each finding placed elsewhere or above: **evidence** → **home** (the ADR, feat row or issue that owns it, or the
 concept not yet decided) → **proposed destination** (a feat row, a board card, an issue, a follow-up ADR) → what this
 ADR should say about it meanwhile (an open question, a consequence). Routed findings never change the verdict.
 "None." if empty.>

## Template & scenario conformance
<which ADR-0000 sections are missing/thin; whether every Scenario is observable and has a
 named test in the Implementation plan; whether Contracts are compilable and complete>

## Recommendation
<advance as-is / advance after fixing Blockers / needs another draft / supersede — and the
 single most important thing to do next; list the routed findings' destinations separately>
```

Keep the prose terse and concrete (match the repo's voice). Cite the file with real links so
the author can jump straight to each finding. Do not paste the whole ADR back.

## Step 5 — Hand off (stop here)

Present the verdict and link the report location (or render it inline if short). Then **stop**
— the judge evaluates, it does not edit the ADR it just judged (conflict of interest, and
ADR-0000 routes findings back to the author/implementer, not the reviewer). Offer, as a
separate explicit step, to apply the Blocker/Major fixes *if the user asks* — and if they do,
that work follows the `adr` skill's rules (Draft is editable; an Accepted ADR is changed only
by a new superseding ADR, never in place).

## Project conventions (apply silently throughout)

- **Identity**: `Deciders: green-0-rabbit`; module paths use
  `github.com/pyvvo/funcd`; copyright "The funcd Authors". Never write the local
  machine username or local filesystem paths into repo files.
- **License gate**: any dependency the ADR proposes must be Apache-2.0/MIT-compatible —
  if the ADR didn't check, that omission is itself a Major finding.
- **Evidence or it didn't happen**: every finding cites a section/line. Vibes are not a verdict.
