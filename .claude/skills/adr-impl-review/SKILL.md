---
name: adr-impl-review
description: Review the WORK an LLM produced implementing a funcd ADR — against the ADR's Contracts, Scenarios, Review checklist, and Definition of Done, the blueprint, and ADR-0002 conventions. Runs the actual verification (build/lint/test, tree diff, identity grep), produces an evidence-cited, severity-tiered verdict (Blocker/Major/Minor + what's strong to keep), and records a per-model quality entry so models (sonnet-4.6, deepseek-v4-pro, gpt-5.4-mini, …) can be compared at ADR-implementation quality. Use whenever the user wants to review, check, grade, verify, or sign off on work done for an ADR — "review the implementation", "did the model implement ADR-0002 right", "check the work against the ADR", "is this done", "grade deepseek's work on ADR-0003", "run the review gate". This is the ADR-0000 review gate (#5) — distinct from `adr-judge`, which reviews the ADR *document* before acceptance; this reviews the *code* after the implement gate. On a pass it stamps the ADR `Reviewing → Implemented` and the feat row → `implemented`; on changes-requested it loops back to `adr-impl` (the ADR stays `Reviewing`). It reviews and records; it does not fix.
---

# ADR work review

Run the ADR-0000 **review gate** (gate #5) on the work an LLM produced implementing an ADR: does
the implementation actually conform to its ADR — Contracts, Scenarios, Review checklist,
Definition of Done — and to the blueprint + [ADR-0002](../../../docs/adr/0002-source-code-conventions-and-patterns.md)
conventions? Produce an evidence-cited verdict, and record a per-model quality entry so model
quality at implementing ADRs is tracked and comparable.

The discipline that makes this real: **run the verification, don't eyeball it.** Reading files and
asserting "looks good" is how defects ship. The high-value findings come from actually executing
`just ci`, diffing the tree against the ADR's repository surface, and grepping for leaks — see
`references/review-method.md`.

You **review and record; you do not fix.** A reviewer that also rewrites the work loses
independence. Hand Blocker/Major findings back to the builder (the `adr-impl` skill); offer to
apply fixes only as a separate, explicit step the user asks for.

## Step 0 — Inputs

1. **Which ADR** (e.g. `0002`). The work is an implementation — working code with passing scenario
   tests, produced by the `adr-impl` gate; the ADR should be at status `Reviewing`.
2. **Which model produced the work** — required (`sonnet-4.6`, `deepseek-v4-pro`, `gpt-5.4-mini`,
   `claude-opus-4-8`, …). The scorecard is keyed on it; if not given, ask before recording.

## Step 1 — Orient (what "done" means here)

Read, and treat as the bar:
1. The ADR — especially **Contracts**, **Scenarios**, **Implementation plan**, **Review checklist**,
   and **Definition of done** (ADRs predating the rename title the plan *Scaffold plan* — same
   section). The ADR's own checklist + DoD are the *specific* bar.
2. `references/definition-of-done.md` (in this skill) — the *generic* Definition of Done every ADR's
   implementation must also meet, even where the ADR was silent.
3. `blueprint.md` (the architecture the work slots into) and `docs/adr/0002-*` (binding code
   conventions). For a feature ADR, also its realized `docs/feat/` row.

## Step 2 — Run the verification (evidence, not opinion)

Execute the checks in `references/review-method.md` and **capture real output** (exit codes,
file:line, command transcripts). At minimum:
- **Builds/lints/tests**: `just ci` (or `go build/vet/test`, `just lint`) — record exit codes
  verbatim. A green claim without a captured exit 0 is not evidence.
- **Tree vs ADR**: diff the produced tree against the ADR's *Repository surface* — nothing missing,
  nothing unexplained-extra.
- **Conventions**: spot-check the ADR-0002 rules that apply (no `any` in APIs, import graph,
  `api/fault`, one-file drivers, no `panic`, slog-only, ctx-first).
- **Behavior rule**: real business logic conforming to the Contracts (no `not implemented` stubs in
  the shipped path); every Scenario has a named test that is **un-skipped and passing**, none
  weakened or deleted; `just ci` exits 0.
- **Tracking**: the realized feat row at `reviewing`, the ADR at `Reviewing`, and the **ADR's substance
  unchanged** — the builder's *only* permitted ADR edit is the `Accepted → Reviewing` status bump, so any
  change to Context/Scenarios/Decision/Contracts is itself a Blocker.
- **No dev-machine references (verify silently — do NOT write a "Hygiene" section).** Confirm no absolute
  OS path / local username / personal email leaked into the work (the [CLAUDE.md absolute rule](../../CLAUDE.md)).
  This is a silent pass/fail check: **the verdict must not contain a Hygiene/identity-grep section at all**, and
  must **never transcribe the real value or the grep pattern** — that documentation line is itself how the real
  username has leaked into tracked review docs. If a leak exists, raise it as a Blocker described generically
  ("an absolute path / the local username leaked in `<file:line>`"); if not, say nothing.

## Step 3 — Attribute every finding (this is what makes the model score fair)

For each finding decide **whose fault it is** — this is the crux:
- **`model`** — the implementer got it wrong (missed a file, broke a convention, left a stub that
  fails, skipped a scenario). Counts against the model's score.
- **`adr`** — the ADR itself is wrong or self-contradictory (e.g. a Definition of done that can't be
  satisfied as written). Recorded, but **does not** count against the model; it loops back to a
  superseding ADR. (Real example: ADR-0001 demanded both "no Go files" and "`just ci` exits 0" —
  impossible on an empty Go module; the model that produced the empty skeleton was *right* to, so
  that Blocker is `adr`-attributed, not `model`.)
- **`env`** — tooling/environment (no Nix to run `nix flake lock`, no network). Recorded, not scored.

Mis-attributing an ADR defect to the model poisons the cross-model comparison — get this right.

## Step 4 — Write the verdict

Use the template + house style in `references/review-method.md`: a severity-tiered, evidence-cited
report — 🔴 Blocker / 🟡 Major / Minor, then **✅ Verified correct (what's strong — keep it)**, then
a recommendation. Tie each finding to its evidence (a captured command/exit code or `file:line`) and
its attribution. Naming what's *right* matters as much as what's wrong — it tells the builder what
not to regress.

Save the verdict as a standalone **review report** at
`docs/reviews/adr-<NNNN>-<phase>-<model>.md` (phase is now always `implementation`; e.g.
`docs/reviews/adr-0002-implementation-sonnet-4.6.md`).
The one-line `--notes` in the ledger is the at-a-glance summary; this doc is the full, evidence-cited
record, and Step 5 links it from the scorecard via `--report`. Keep the path unique (append `-2`,
`-3`, … if the same model re-reviews the same ADR/phase).

A verdict is one of: **pass** (DoD met, no Blockers/Majors), **changes-requested** (Blockers or
Majors — loop back), **fail** (fundamentally off the ADR).

## Step 5 — Record the model scorecard

Append the review to the ledger and regenerate the rollup (this is the "judge the model" file):

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr <NNNN> --phase implementation --model <model-name> \
  --verdict <pass|changes-requested|fail> \
  --blockers <n> --majors <n> --minors <n> \
  --model-attributed <n>   # of those, how many are the MODEL's fault (rest = adr/env) \
  --dod-passed <n> --dod-total <n>   # from the ADR's Review checklist / DoD \
  --report docs/reviews/adr-<NNNN>-<phase>-<model>.md   # the Step 4 verdict doc \
  --notes "<one line; tag each finding's attribution>"
```

`--dod-total` is the count of ADR Review-checklist + DoD items; `--dod-passed` how many hold.
`--model-attributed` excludes `adr`/`env` findings — that's what keeps the per-model comparison fair.
`--report` is the Step 4 verdict doc; the scorecard links each row to it, so the full review is one
click from the ledger. The script rewrites `docs/reviews/model-scorecard.md` (per-model rollup +
chronological log) — point the user at it.

## Step 6 — Advance status (only on a `pass`)

A `pass` means the work is *done and verified*, so the review gate is the sole authority that
stamps `Implemented`. **Only on a `pass`:**

- Set the realizing `docs/feat/` row `reviewing → implemented`.
- Bump the ADR file's status `Reviewing → Implemented` (add the implemented date). That
  `Reviewing → Implemented` bump is the *one* forward edit this gate makes to the ADR — make no
  other change to it. (The `adr-impl` gate already moved the ADR `Accepted → Reviewing`.)
- **Move the feature ADR's board card `In Progress → Done`** (the card opened at draft and moved to
  `In Progress` at acceptance; see CLAUDE.md → *Backlog* for the `/project-management` `status` command).
  This gate is the sole stamper of `Implemented`, so it owns the `→ Done` move. A pure-infra / process /
  refactor ADR has no card and skips this.

Find the row via the ADR's `Realizes: FEAT-NNNN/Fxx` header; the move is **forward-only** — never
walk a status backward. On **changes-requested** or **fail**, advance nothing: the ADR stays
`Reviewing`, `model` findings loop back to the `adr-impl` gate for rework, `adr` findings to a
superseding ADR. If you find a status was *prematurely* advanced (e.g. an ADR already `Implemented`
before any passing review), flag it rather than silently leaving a false status. This is status
*tracking*, not fixing — you still never edit the *work* (code).

## Step 7 — Close

Summarize: the verdict, the must-fix Blockers (with attribution), the scorecard entry, the status
you advanced (or why you didn't), and the next move (builder fixes `model` findings; `adr` findings
trigger a superseding ADR). Offer to apply fixes only if asked — keep the review independent of the
fix.
