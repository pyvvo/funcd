---
name: project-summary
description: Create or update docs/PROJECT-SUMMARY.md — the single-page, accurate map of what funcd is and its current state. Use whenever the user wants to write, refresh, reconcile, or regenerate the repo/project summary, or after new ADRs land and the summary may be stale — "update the project summary", "summarize the repo", "the summary is out of date", "regenerate PROJECT-SUMMARY", "reflect the new ADRs in the summary", "what's the current build status". The summary is DERIVED from the ADRs (the append-only source of truth) + the roadmap + the resource kinds. This skill drives a reconciler (state.py) that reports exactly what drifted, so edits stay minimal and ADR-accurate; it always keeps a "Build status" state-of-the-project section.
---

# project-summary — create / update the repo summary

`docs/PROJECT-SUMMARY.md` is the one-page map of the project: what it is, how the parts
fit, the feature/status table, and a **decision log of every ADR**. It is **derived**,
not authored from scratch each time — the **ADRs are the append-only source of truth**.
Its structure is the outline in **[summary-shape.md](summary-shape.md)**.

The driver is **[.claude/skills/project-summary/state.py](state.py)** — it reads the
ADRs, the roadmap, and the resource kinds, and prints exactly how the summary drifts.
It **never edits** the summary; **you** make the minimal edits it points at. Paths below
are relative to the repo root.

## The rules (do not break these)

1. **ADR-driven & append-only.** The summary changes **only** because an ADR changed the
   project. A **new ADR → APPEND one row** to the **Decision log (ADRs)** table (and reconcile any
   section that ADR actually changed). **Never rewrite, reorder, or delete existing ADR
   rows** — ADRs are immutable once Implemented, so their log entries are too.
2. **Supersession is the one allowed edit** to an existing row: when an ADR's own `**Status**`
   line says `Superseded by ADR-NNNN`, mark that row superseded. Nothing else edits history.
3. **The "Build status (<version>)" section always exists** and is **recomputed every run** from the roadmap
   (`docs/roadmap/v1-plan.json`): how many ADRs accepted/implemented, and the remaining items.
4. **Don't invent.** If a fact isn't in an ADR / the roadmap / the code, it doesn't go in.
   The driver surfaces the resource kinds and roadmap items verbatim — use those.

## Run the driver (agent path) — do this FIRST

```bash
python3 .claude/skills/project-summary/state.py
```

It prints a report ending in a **VERDICT**:

- **`IN SYNC`** — every ADR is already logged. Only re-run the Build status section if the roadmap moved; otherwise done.
- **`DRIFT`** — new ADR(s) and/or stale links. Apply the `[APPEND]` / `[VERIFY]` edits, then recompute Build status.
- **`CREATE`** — no summary yet; author all sections (see *Create from scratch* below).

The report gives you, deterministically:

- **`[APPEND]`** — each new ADR's number, status, title, and `adr/<file>` link (the exact Decision-log row to add).
- **`[VERIFY]`** — ADRs whose Status line marks them superseded (the row that may need a note).
- **`[NOTE]`** — ADRs not yet `Implemented` (their recap must not claim "built").
- **Build-status inputs** — accepted/pinned count, implemented count, and every remaining roadmap item.
- **Resource kinds** — the current set, to reconcile the **Resource model** kind list and the **Features** rows.

CI / pre-commit gate (exit 1 on drift):

```bash
python3 .claude/skills/project-summary/state.py --check
```

## Update workflow (VERDICT: DRIFT)

1. Run the driver. For **each `[APPEND]` ADR**, read that ADR file (`docs/adr/<file>`) and **append
   one row** to the **Decision log** table, in ADR-number order, following the row shape in
   **[summary-shape.md](summary-shape.md)** (linked title · one-line recap · a `<ol><li>…</li></ol>`
   numbered list of the locked-in choices, no rationale). Match the existing rows. If the ADR is a
   roadmap-item successor, note it (e.g. "(P-V-3)").
2. **Reconcile only what that ADR changed** elsewhere: a new resource kind → add it to the **Resource model** kind list
   and a **Features** row (link the implementing file); a new component → **Repository shape**; a decision that refines a
   prior one → the relevant prose sentence (don't rewrite the section). When unsure what an ADR
   touched, read its *Consequences* / *Relates to* headers.
3. For each **`[VERIFY]`** superseded ADR, add a short "*superseded by ADR-NNNN*" note to its recap.
4. **Recompute the Build status section** from the driver's "Build status" block: state `ADR-0001…NNNN`, the accepted
   count, and the remaining items by id+title. Keep the "out of scope" note honest.
5. Re-run the driver until **`IN SYNC`** (and `--check` exits 0).

## Create from scratch (VERDICT: CREATE)

Author `docs/PROJECT-SUMMARY.md` by following the section outline + row shapes in
**[summary-shape.md](summary-shape.md)** (the canonical structure — section names,
the Features-table and Decision-log row shapes, the two required sections). Copy the
mermaid diagrams verbatim from the architecture doc (`blueprint.md`); fill the
**Decision log** rows and the **Build status** section from the driver's report.

## Gotchas

- **The driver is read-only by design.** It tells you what to change; it never writes the
  summary. That's what keeps edits ADR-accurate and append-only — don't "fix" it to auto-write.
- **`accepted` (roadmap) ≠ ADR file count.** The process ADR (ADR-0000) and any superseded ADRs
  aren't in the roadmap's `accepted`/tier-0 list, so the accepted count is **lower** than the
  ADR-file count — expected, not a bug. Build status quotes the roadmap; the Decision log lists
  every ADR file.
- **Supersession is read from the Status line only.** A title's "supersedes ADR-XXXX" is the
  *other* direction — the driver ignores it, so an ADR that supersedes another isn't itself
  flagged superseded.
- **mermaid diagrams are copied from `blueprint.md`** (the source of truth for architecture
  pictures); the summary embeds them verbatim — don't redraw them here.
- **Run from anywhere in the repo** — `state.py` walks up to the root (the dir containing
  `docs/adr/`); or pass `--root <dir>`.
- **No identity/path leak in the summary.** `PROJECT-SUMMARY.md` is a tracked file: never write the
  local machine username or an absolute OS path into it. Every path is project-root-relative
  (`docs/adr/…`, `pkg/funcd/…`), never `/Users/<user>/…` or `/home/<user>/…` — even inside an
  example or a quoted command, write the token generically (`<user>`, `/Users/`). Grep the file for
  an absolute-path prefix and the local username before finishing.

## Verify

```bash
python3 .claude/skills/project-summary/state.py --check && echo "summary reflects the ADRs"
grep -nE '/Users/[a-z]|/home/[a-z]' docs/PROJECT-SUMMARY.md && echo "LEAK — fix before finishing" || echo "no absolute-path/username leak"
```
