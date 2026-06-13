---
name: adr-impl-review
description: Review the WORK an LLM produced for a funcd ADR — the scaffold or the implementation — against the ADR's Contracts, Scenarios, Review checklist, and Definition of Done, the blueprint, and ADR-0002 conventions. Runs the actual verification (build/lint/test, tree diff, identity grep), produces an evidence-cited, severity-tiered verdict (Blocker/Major/Minor + what's strong to keep), and records a per-model quality entry so models (sonnet-4.6, deepseek-v4-pro, gpt-5.4-mini, …) can be compared at ADR-implementation quality. Use whenever the user wants to review, check, grade, verify, or sign off on work done for an ADR — "review the scaffold", "did the model implement ADR-0002 right", "check the work against the ADR", "is this scaffold done", "grade deepseek's work on ADR-0003", "run the review gate". This is the ADR-0000 review gate (#5 scaffold / #8 implementation) — distinct from `adr-judge`, which reviews the ADR *document* before acceptance; this reviews the *code* after the work is done. It reviews and records; it does not fix (loop fixes back to the builder).
---

# ADR work review

Run the ADR-0000 **review gate** on work an LLM produced for an ADR: does the scaffold (gate #5)
or the implementation (gate #8) actually conform to its ADR — Contracts, Scenarios, Review
checklist, Definition of Done — and to the blueprint + [ADR-0002](../../../docs/adr/0002-source-code-conventions-and-patterns.md)
conventions? Produce an evidence-cited verdict, and record a per-model quality entry so model
quality at implementing ADRs is tracked and comparable.

The discipline that makes this real: **run the verification, don't eyeball it.** Reading files and
asserting "looks good" is how defects ship. The high-value findings come from actually executing
`just ci`, diffing the tree against the ADR's repository surface, and grepping for leaks — see
`references/review-method.md`.

You **review and record; you do not fix.** A reviewer that also rewrites the work loses
independence. Hand Blocker/Major findings back to the builder (the `adr-scaffold` skill or the
implementer); offer to apply fixes only as a separate, explicit step the user asks for.

## Step 0 — Inputs

1. **Which ADR** (e.g. `0001`) and **which phase**: `scaffold` (no business logic; skeletons skipped)
   or `implementation` (scenarios pass; logic present). Infer from the work if obvious; ask if not.
2. **Which model produced the work** — required (`sonnet-4.6`, `deepseek-v4-pro`, `gpt-5.4-mini`,
   `claude-opus-4-8`, …). The scorecard is keyed on it; if not given, ask before recording.

## Step 1 — Orient (what "done" means here)

Read, and treat as the bar:
1. The ADR — especially **Contracts**, **Scenarios**, **Scaffold plan**, **Review checklist**, and
   **Definition of done**. The ADR's own checklist + DoD are the *specific* bar.
2. `references/definition-of-done.md` (in this skill) — the *generic* phase DoD (scaffold vs
   implementation) every ADR's work must also meet, even where the ADR was silent.
3. `blueprint.md` (the architecture the work slots into) and `docs/adr/0002-*` (binding code
   conventions). For a feature ADR, also its realized `docs/feat/` row.

## Step 2 — Run the verification (evidence, not opinion)

Execute the checks in `references/review-method.md` and **capture real output** (exit codes,
file:line, command transcripts). At minimum:
- **Builds/lints/tests**: `just ci` (or `go build/vet/test`, `just lint`) — record exit codes
  verbatim. A green claim without a captured exit 0 is not evidence.
- **Tree vs ADR**: for a scaffold, diff the produced tree against the ADR's *Repository surface* —
  nothing missing, nothing unexplained-extra.
- **Conventions**: spot-check the ADR-0002 rules that apply (no `any` in APIs, import graph,
  `api/fault`, one-file drivers, no `panic`, slog-only, ctx-first).
- **Phase rule**: scaffold → no business logic, scenario tests present and **skipped**;
  implementation → scenario tests **un-skipped and passing**, none weakened or deleted vs the
  scaffold, business logic present.
- **Hygiene**: identity grep (no local username/paths), the realized feat row advanced
  (`scaffolded`/`implemented`), and the **ADR file itself unchanged** if it was Accepted (accepted
  ADRs are immutable — a diff on it is itself a Blocker).

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

A verdict is one of: **pass** (DoD met, no Blockers/Majors), **changes-requested** (Blockers or
Majors — loop back), **fail** (fundamentally off the ADR).

## Step 5 — Record the model scorecard

Append the review to the ledger and regenerate the rollup (this is the "judge the model" file):

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr <NNNN> --phase <scaffold|implementation> --model <model-name> \
  --verdict <pass|changes-requested|fail> \
  --blockers <n> --majors <n> --minors <n> \
  --model-attributed <n>   # of those, how many are the MODEL's fault (rest = adr/env) \
  --dod-passed <n> --dod-total <n>   # from the ADR's Review checklist / DoD \
  --notes "<one line; tag each finding's attribution>"
```

`--dod-total` is the count of ADR Review-checklist + DoD items; `--dod-passed` how many hold.
`--model-attributed` excludes `adr`/`env` findings — that's what keeps the per-model comparison fair.
The script rewrites `docs/reviews/model-scorecard.md` (per-model rollup + chronological log) — point
the user at it.

## Step 6 — Close

Summarize: the verdict, the must-fix Blockers (with attribution), the scorecard entry, and the
next move (builder fixes `model` findings; `adr` findings trigger a superseding ADR). Offer to apply
fixes only if asked — keep the review independent of the fix.
