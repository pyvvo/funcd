# PROJECT-SUMMARY shape

The canonical outline of `docs/PROJECT-SUMMARY.md` — the structure to author when
creating it from scratch, and the section names to reconcile against when updating.

**Headings are UNNUMBERED** so a section can be inserted anywhere without renumbering.
Two sections are **load-bearing** — the driver (`state.py`), the skill, and any CI gate
key on them **by name**, so keep their headings exactly:

- `## Build status (<version>)` — required; recomputed from the roadmap on every run.
- `## Decision log (ADRs)` — required; append-only, one row per ADR.

(The `(<version>)` suffix tracks the current feature-version — e.g. `(V1)` today, `(V2)` later.
**Don't hardcode the version**: the tooling matches the `## Build status` **stem**, so bump the
suffix with the version and keep the stem.)

## Section outline

A leading block-quote intro: one paragraph, "read this first," links to the deeper docs
(blueprint, ADRs), and a `**Status: Active**` marker.

| Section (heading) | Holds |
|---|---|
| `## What <project> is` | what it is, who it's for, the headline properties. |
| `## The <substrate> / foundation` | the core engine/storage/runtime decision + any benchmark or evidence table. |
| `## Repository shape` | the top-level directories and what each holds; the ports-vs-engine split. |
| `## Resource model & lifecycle` | the typed model + a deploy-lifecycle table + the resource **state-machine** mermaid (copied from the architecture doc). |
| `## Features` | a table `\| Feature \| What it is \| Status \|`; **each feature name links to the file that implements it**. |
| `## Observability` | logging/telemetry/audit posture. |
| `## Network` | control-plane / data-plane / east-west; CNI. |
| `## Security` | tenancy, sandbox, secrets, egress, identity, authz + the **PEP→PDP** authorization mermaid. |
| `## Architecture` | the **global-architecture** mermaid + the **invocation/flow** mermaid (copied from the architecture doc). |
| `## Build status (<version>)` | **required** — the state-of-the-project summary: how many decisions decided/accepted/implemented, the remaining items (id + title), and an honest "out of scope" note. Recomputed from the roadmap every run. |
| `## Decision log (ADRs)` | **required, append-only** — see the row shape below. |

## Decision-log row shape

A table `| Title | Recap | Verdict |`, one row per ADR file, **in ADR-number order**:

- **Title** — `[ADR-NNNN — short name](adr/<file>.md)` (linked to the ADR).
- **Recap** — one short line. If the ADR's own `**Status**` line says it is superseded,
  add a "*superseded by ADR-NNNN*" note (the **only** allowed edit to an existing row).
- **Verdict** — a **numbered list of the locked-in choices, no rationale**, rendered with
  `<ol><li>…</li></ol>`. (Rationale lives in the ADR; the verdict is just *what* was decided.)

**Never rewrite, reorder, or delete an existing row** — ADRs are immutable, so their log
entries are too. A new ADR only ever **appends** a row.

## Rules carried by the shape

- **Diagrams are copied verbatim** from the architecture doc (`blueprint.md` here) — the
  summary embeds them, it does not redraw them.
- **The two required sections are matched by name** — never reintroduce ordinal numbers in
  their headings.
- **Everything is derived** from the append-only sources (ADRs · roadmap · resource kinds /
  code) — nothing in the summary should assert a fact not traceable to one of them.
