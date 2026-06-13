---
description: Drive one funcd ADR through its lifecycle — brainstorm → draft → judge → (your acceptance) → implement → review — invoking each gate's skill in order and stopping at the human-acceptance checkpoint. Status-aware and resumable.
argument-hint: <topic to decide | ADR number e.g. 0003 | path to an ADR>
---

# /adr-cycle — conduct one ADR from idea to `Implemented`

Run the ADR-0000 workflow for **$ARGUMENTS**, gate by gate. You are the **conductor**: every gate
is an existing skill — *invoke it, never reimplement it*. The authority is
[ADR-0000](docs/adr/0000-adr-process.md); the cross-document propagation rules are in
[CLAUDE.md](.claude/CLAUDE.md). Each skill discharges its own obligations on exit (feat row,
blueprint, roadmap, ADR status) — your job is **sequencing** and the **human checkpoint**, not
re-doing their work.

**Model per gate (your call):** each gate runs in whatever model the session is on; the pins below
are the recommended ones — switch with `/model` between gates if you're pinning models per gate.
The review gate is scored against the *implementing* model, not the reviewing one.

## Step 0 — Locate the ADR in its lifecycle, then run forward

Resolve `$ARGUMENTS`:
- **A number/path** (`0003`, `docs/adr/0003-*.md`) → read the ADR's `Status` header.
- **Free-text topic** with no ADR yet → fresh start (status = none).

Jump to the matching entry point and proceed **forward only**. Announce where you are before each
gate ("Gate 4 — implementing ADR-0003"). Never walk a status backward; never skip the acceptance
checkpoint.

| ADR status | Entry point |
|---|---|
| none (new topic) | Gate 1–2 |
| `Draft` / `Proposed` | Gate 3 |
| `Accepted` | Gate 4 |
| `Reviewing` | Gate 5 (→ loop back to Gate 4 on `changes-requested`) |
| `Implemented` | **Stop** — frozen. A change is a new *superseding* ADR; offer to start `/adr-cycle <new topic>`. |

## Gate 1–2 · Brainstorm → Draft  ·  *recommended: Opus 4.8 MAX*

Invoke the **`adr`** skill on the topic. It brainstorms with me, writes the Draft, and marks it
`Proposed` when ready (advancing the feat row `idea → adr` and reconciling the roadmap placeholder).
When it returns, continue to Gate 3.

## Gate 3 · Judge the ADR *document*  ·  *recommended: Opus 4.8 Extra*

Invoke the **`adr-judge`** skill on the `Proposed` ADR. It emits an evidence-cited, severity-tiered
verdict and **edits nothing**. Present its verdict to me.

## ⏸ Acceptance checkpoint — **mine to make** (the one mandatory human stop)

Per ADR-0000, acceptance is *always* the human's call — **never self-accept**. This is the
"(optionally) wait for my acceptance" pause:

- Judge raised **Blockers/Majors** → summarize them and **stop**. I decide: loop them back into the
  Draft (re-run the **`adr`** skill — it stays editable until accepted) or accept anyway.
- Verdict clean → **stop and ask me to accept.** On my explicit go-ahead, the **`adr`** skill's
  acceptance step runs: ADR `→ Accepted` (+date); **blueprint synced** iff the decision refined it;
  **roadmap** number/placeholder reconciled; feat row `→ accepted`. Then continue to Gate 4.

> Resuming is just re-running `/adr-cycle <ADR>` once it is `Accepted` — the Step 0 table sends you
> straight to Gate 4. So the acceptance pause never blocks an unattended run from picking back up.

## Gate 4 · Implement from the ADR + blueprint  ·  *recommended: Sonnet 4.6 medium*

Invoke the **`adr-impl`** skill on the `Accepted` ADR. It produces the working code — port
interfaces, one-file drivers, facades, deps — **and the scenario tests written and passing**, green
`just ci`. On exit it advances ADR `Accepted → Reviewing` and feat row `→ reviewing`. If it reports
the ADR can't go green within its sanctioned scope (an **`adr`** gap), don't force it — that breaks
out to a superseding ADR. Otherwise continue to Gate 5.

## Gate 5 · Review gate  ·  *recommended: Opus 4.8 Extra*

Invoke the **`adr-impl-review`** skill on the implementation, passing the **implementing model's
name** (e.g. `sonnet-4.6`) for the scorecard. Branch on its verdict:

- **pass** → it stamps ADR `Reviewing → Implemented` and feat row `→ implemented`. **Cycle done.**
- **changes-requested / fail** → ADR stays `Reviewing`. Loop:
  - **`model`**-attributed findings → back to **Gate 4** (`adr-impl` reads the review report and
    reworks them). Re-run Gate 5. Repeat until it **passes**.
  - **`adr`**-attributed findings → **stop**: the decision itself was wrong → start a **new
    superseding ADR** with `/adr-cycle <new topic>` (it carries its own feat row + roadmap
    follow-through).

## On completion

Report the final state: ADR `Implemented`, feat row `implemented`, the green `just ci`, and the
scorecard entry. Confirm the blueprint and roadmap were already synced **at acceptance** — an
implementation pass owes them nothing further. Before closing, sanity-check that no layer is
half-updated (stale status, orphan ADR, placeholder still pointing at a now-real number) and that no
local username/path leaked into a changed file — never leave the four layers inconsistent.
