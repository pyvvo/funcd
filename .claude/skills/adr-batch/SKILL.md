---
name: adr-batch
description: Autonomously drive MANY funcd ADRs through the FULL lifecycle in one run — for each plan id / topic: brainstorm→draft (`adr`) → judge (`adr-judge`) → apply the judge's Blocker/Major fixes → SELF-ACCEPT (no human-acceptance stop) → implement (`adr-impl`) → review (`adr-impl-review`) → loop impl/review until the review passes → next item; then reconcile the roadmap. Use when the user wants to batch-build several ADRs end-to-end with auto-approval / self-acceptance — "run the whole lifecycle for P-J and P-I", "implement P-M, P-N, P-Q end to end and self-accept", "auto-cycle these plan ids", "batch the next tier", "do P-x..P-y on autopilot". This is the batch + self-accepting counterpart to `adr-cycle` (which does ONE ADR and STOPS at human acceptance). Invoking it IS the user's standing authorization to self-accept. It does not auto-commit (commit only when the user asks).
---

# ADR batch autopilot

Drive a **list** of ADRs through the complete ADR-0000 lifecycle in dependency order, **self-accepting**
each one, until every requested item is `Implemented` and the roadmap is reconciled. This is the
formalized version of running `adr` → `adr-judge` → (fix) → accept → `adr-impl` → `adr-impl-review`
back-to-back for several items — with the human-acceptance gate replaced by **(a) the judge gate run to
completion with all Blocker/Major findings applied, and (b) the implementation review gate as the real
quality backstop.**

> **Acceptance authority.** ADR-0000 and the `adr` skill say acceptance is the human's call. This skill
> exists *because the user explicitly delegated that call by invoking it.* That delegation is the whole
> point — do not stop to re-ask "may I accept?" for each item. But the delegation is **bounded**: never
> self-accept an ADR with an **open judge Blocker**, and surface to the human on the genuine blockers in
> Step 5. Self-acceptance is a convenience over a sound, judged decision — not a licence to skip rigor.

It orchestrates the four single-purpose skills; it does not replace them. Each gate still runs in full.

## Step 0 — Resolve and order the batch

1. **Parse the input** into a list of items. Each item is either a **roadmap plan id** (`P-J`, `P-I`, …
   — resolve its topic + build-deps from `docs/roadmap/v1-plan.json` + the slate table in the delivery
   plan) or a **free topic** (treat like an `adr` argument). A range like "P-O..P-T" expands to the
   plan ids in that span.
2. **Read the roadmap** (`docs/roadmap/v1-plan.json` + `v1-delivery-plan.md`) and the active feat doc.
   For each item, get its **build-dependency set** (the `depends_on` ids).
3. **Topologically order** the batch by build deps, and **verify every dependency is satisfiable**: a
   dep must be either already built (in `accepted`/tier 0) **or** earlier in this same batch. If an item
   depends on something that is neither, **stop and report it** — you cannot implement P-M before P-J
   exists. Present the resolved, ordered list and the per-item dep check, then proceed.
4. **Assign ADR numbers** in processing order from the next free `docs/adr/NNNN`. Track the mapping
   (plan id → ADR-NNNN) so later items' `depends_on` and the roadmap reconcile use real numbers.
5. **Status-aware resume**: if an item's ADR already exists, pick up from its current status
   (`Draft`/`Proposed` → judge/accept; `Accepted` → implement; `Reviewing` → review; `Implemented` →
   skip, already done). Never redo a completed phase; never walk a status backward.

## Step 1 — Per item: draft (the `adr` gate)

Invoke the **`adr`** skill for the item's topic. Drive it to a complete **Proposed** ADR with every
ADR-0000 template section present and a contract detailed enough to implement from blueprint + the ADR
alone. **Autonomy rule:** this is an unattended run, so do **not** block on `AskUserQuestion` for
user-owned scope choices — make the **defensible default**, **document the choice + its rationale in the
ADR** (Decision / Alternatives / a header note), and proceed. Only the genuine blockers in Step 5 stop
the run. Update the feat row (`idea → adr`, link the ADR).

## Step 2 — Per item: judge + apply fixes (the `adr-judge` gate)

1. Invoke the **`adr-judge`** skill on the Proposed ADR. Capture its severity-tiered verdict.
2. **Apply every Blocker and Major** to the still-Proposed ADR (it is editable until accepted), plus any
   Minors that are clearly correct and cheap. Re-judge only if a fix opened a new branch.
3. An ADR may **not** be accepted with an unresolved **Blocker**. If a Blocker is `adr`-attributed and
   genuinely unresolvable within scope (contradictory contracts, an unsatisfiable scenario, a
   security-model violation no default fixes), **stop and surface it** (Step 5) — do not paper over it.

## Step 3 — Per item: self-accept (the delegated decision)

With Blocker/Major findings folded in, **self-accept**:
- Set the ADR header `Proposed → Accepted` with the date and a one-line note of what the judge changed.
- **Propagate (same session, per CLAUDE.md):** sync `blueprint.md` **iff** the decision refines/contradicts
  it (newest accepted ADR wins); advance the realizing feat row `→ accepted` and link the ADR.
- Reconcile any roadmap placeholder for this item to its real ADR number (full tier-0 graduation waits
  for Step 6, after `Implemented`).
- **Move the feature ADR's board card `Backlog → In Progress`** now (a feature ADR carries a card from
  draft; if this batch also drafted it, ensure the card exists first — `list`, else `create` — see
  CLAUDE.md → *Backlog* for the `/project-management` commands). The `→ Done` move happens in Step 4 when the
  review gate stamps `Implemented`. A pure-infra / process / refactor ADR has no card and skips both moves.

## Step 4 — Per item: implement + review until green (the `adr-impl` / `adr-impl-review` loop)

1. Invoke **`adr-impl`** on the Accepted ADR — real behavior conforming to the Contracts, every Scenario
   a named **passing** test, ending green. Verify with the four sub-checks (`go build` · `go tool
   golangci-lint run` · `go test` · `go mod verify`) — `just ci`'s git-diff gate will fail on the
   uncommitted tracked files (expected; do not treat as a defect). For Linux-only / inherently-e2e
   scenarios, ship contract/unit tests and **defer** the e2e ones per the roadmap's test-sequencing note,
   recording the deferral. On exit `adr-impl` sets ADR `Accepted → Reviewing` + feat row `→ reviewing`.
2. Invoke **`adr-impl-review`** with the producing model name (`claude-opus-4-8` unless told otherwise).
   It runs the verification, writes the `docs/reviews/` report, and records the scorecard.
3. **Loop on the verdict:**
   - **pass** → it stamps ADR `Reviewing → Implemented` + feat row `→ implemented`. Item done.
   - **changes-requested (`model` findings)** → re-run **`adr-impl`** to fix exactly those findings, then
     re-review. Cap at **3** impl/review loops; if it still hasn't converged, **stop and surface** (Step 5).
   - **`adr`-attributed findings** → an accepted ADR is frozen, so the fix is a **superseding ADR** — stop
     and surface it rather than editing history (unless the defect was caught pre-accept, in which case it
     was already handled in Step 2).
4. Grep changed files for identity leaks (local username / absolute home-dir paths / email).
5. **Commit the item — a per-phase checkpoint (required, not optional).** As soon as it is `Implemented`,
   green (the four sub-checks pass), its four layers propagated, and identity-clean, **commit that item's
   work before starting the next one** — one commit per implemented ADR, so each phase is a self-contained,
   green, bisectable checkpoint and `just ci`'s git-diff gate is satisfied. Committing incrementally is what
   keeps it clean: the shared files (feat doc, `blueprint.md`, the review ledger/scorecard) hold only *this*
   item's delta at commit time, so a per-item `git add` of the item's files (its ADR + `docs/reviews/`
   report + code + the feat-row/blueprint/scorecard edits it made) is unambiguous — never let the next item
   start until the current one is committed. Message: `feat: implement ADR-NNNN — <title> (FEAT-0000/Fxx)`,
   ending with the `Co-Authored-By: Claude Opus 4.8` trailer. **If on the default branch, branch first**
   (never commit a batch straight to `main`); don't push or open a PR (that's Step 7).

Then advance to the next item (its deps now include any item just Implemented).

## Step 5 — When to stop and surface to the human (autonomy has limits)

Self-acceptance is delegated; these are **not** — pause the batch and report:
- a **dependency cycle** or an unsatisfiable build-dep (an item needs something neither built nor in the batch);
- a judge **Blocker** that is `adr`-attributed and unresolvable within scope (contradiction, unsatisfiable
  scenario, **security-model violation** — e.g. default-allow where the blueprint says default-deny);
- a dep that **can't be fetched** (offline) — do not hand-edit `go.mod` with a guessed version;
- the impl/review loop **not converging** within the cap;
- anything that would need to **walk a status backward**, **edit an `Implemented` ADR**, or **renumber** —
  all forbidden;
- a **decider-level change** the user clearly didn't pre-authorize (e.g. swapping a blueprint-mandated
  library/runtime). Make a note and ask, the way the crun change was handled.
Report the item, the phase, the reason, and the safe options — then await direction.

## Step 6 — After the batch: reconcile the roadmap

Once every requested item is `Implemented`:
1. Move each newly-Implemented ADR from `items` to `accepted` in `docs/roadmap/v1-plan.json`; update every
   remaining item's `depends_on` (placeholder → real ADR number); update the mirrored slate table.
2. Re-run the analyzer and the wave check:
   ```bash
   python3 .claude/skills/roadmap-planner/scripts/plan_waves.py docs/roadmap/v1-plan.json
   python3 .claude/skills/roadmap-planner/scripts/plan_waves.py docs/roadmap/v1-plan.json --check-waves
   ```
3. **Mermaid-validate** the regenerated graph, then repaste the computed graph / waves / critical path /
   leaves into `v1-delivery-plan.md`, and reconcile the prose (tier-0 list, counts, design-track front,
   exit-criterion spine, parallelization notes, caveats). The computed sections are generated, never
   hand-drawn — graph ≡ table. (Or invoke the `roadmap-planner` skill to do this.)
4. **Commit the reconcile** as the batch's final phase checkpoint — `docs(roadmap): reconcile V1 plan after
   ADR-NNNN…<list>` (the `docs/roadmap/` files + any trailing feat/blueprint sync the reconcile made), with
   the `Co-Authored-By: Claude Opus 4.8` trailer.

## Step 7 — Finish

- **The batch commits per phase, not at the end.** By the time you reach here, each item is already its own
  commit (Step 4.5) and the roadmap reconcile is committed (Step 6.4) — the working tree is clean and green,
  with one commit per implemented ADR plus the reconcile. Do **not** push or open a PR unless the user asks;
  never commit straight to the default branch (branch first if you started on it). (If a *prior* batch left
  uncommitted work piled up from before this rule, commit it as cohesive units now — per-item if the files
  separate cleanly, else one batch commit — rather than fabricating green intermediate states.)
- Close with a batch report: per item the ADR number/title, the judge findings folded in, the review
  verdict + scorecard line, the status reached + **its commit**; the roadmap reconcile result + new
  tiers/critical path + its commit; any items that stopped (Step 5) and why; and the next unblocked move.

## What this skill does NOT do

- It does **not** invent new gates — it sequences `adr` / `adr-judge` / `adr-impl` / `adr-impl-review`,
  which each run in full and own their rules.
- It does **not** decide architecture (that's each `adr`) or reverse a judge's sound finding to force a pass.
- It **commits per phase** (one commit per implemented ADR in Step 4.5 + one for the roadmap reconcile in
  Step 6.4), but does **not** push or open PRs on its own, and **never** commits straight to the default
  branch (it branches first).
- It does **not** override the immutability rules: `Accepted` is frozen in substance (fix via a superseding
  ADR), `Implemented` is fully frozen, numbers are permanent.

## Project conventions (apply silently throughout)

- **Identity**: `Deciders: green-0-rabbit`; module `github.com/green-0-rabbit/funcd`; author "The funcd
  Authors". Never write the local machine username or local filesystem paths into any tracked file —
  grep changed files before finishing.
- **Propagation in the same session** (CLAUDE.md): an ADR status move owes its feat row (and blueprint, if
  it refined it); the roadmap is computed from `plan.json`, never hand-drawn.
- **One ADR = one topic at one altitude**; a `Draft`/`Proposed` ADR may not contradict an `Accepted` one;
  newest Accepted wins and the blueprint follows.
- All proposed dependencies must be Apache-2.0/MIT-compatible — checked during drafting, not after.
