---
name: project-management
description: Manage the funcd GitHub Project board (pyvvo Project #1) — create a backlog item, change an item's status (Backlog / In Progress / Done), or refine an existing item's title/body. Use whenever asked to add/triage a backlog ticket, move a card, edit a project item, or list the board. All project ids are baked into driver.py, so there is nothing to discover and no `gh` invocation to hand-assemble.
---

# project-management — drive the funcd Project board

The backlog for this repo lives on **GitHub Project #1** of the pyvvo org (`funcd`,
<https://github.com/orgs/pyvvo/projects/1>). This skill drives it through one
script — **[driver.py](driver.py)** — so no agent has to guess the `gh` CLI surface or
rediscover which project / field / option ids to target. The driver has the project node
id, the Status field id, and the option ids **baked in** (verified against the live board);
it resolves items by title substring or node id and refuses to act on an ambiguous match.

`gh` (system PATH) + `python3` are the only requirements — the Nix dev shell is **not**
needed for this skill. Paths below are relative to the repo root.

## The board's vocabulary

Status is a single-select with exactly three options: **Backlog** (not started) →
**In Progress** (being worked) → **Done** (completed). A new item must always be given a
status (the driver defaults to **Backlog**) so it never lands in the "No Status" column.

## The three operations (pick one per request)

Run `list` first to see exact titles, then act. `<query>` is a case-insensitive **substring
of the item title** OR an exact item node id (`PVTI_…`).

```bash
# 0. see the board (do this first to get exact titles / ids)
python3 .claude/skills/project-management/driver.py list

# 1. CREATE a new item (defaults to Backlog; pass a rich body — see "Writing a good item")
python3 .claude/skills/project-management/driver.py create \
  --title "<short idea title>" \
  --body "$(cat <<'EOF'
<why · key trade-offs · what it depends on · scope-when-picked-up>
EOF
)"

# 2. CHANGE STATUS (move a card)
python3 .claude/skills/project-management/driver.py status "<title substring>" "In Progress"

# 3. REFINE an existing item's content (title and/or body)
python3 .claude/skills/project-management/driver.py refine "<title substring>" \
  --body "$(cat <<'EOF'
<the updated body>
EOF
)"
```

Read helpers: `show <query>` prints one item's full body + url; `ids` prints the baked-in
coordinates and verifies them against the live board.

## Writing a good item (the body convention)

A backlog item is a **scoped idea**, not a one-liner. The body should carry: **why** it
matters, the **key trade-offs / chosen shape**, **what it depends on** (link ADRs by number),
and **scope-when-picked-up** (the rough ADR slate or first slice). This keeps the idea from
rotting before it's scoped into a version. Match the depth of the existing items (run `show`
on one to see the house style).

## Status follows the ADR lifecycle (every feature ADR carries a card)

Per the repo's working agreement (`.claude/CLAUDE.md`), a **feature ADR** — one realizing a genuine
deliverable feat-row (a user-facing capability, e.g. FEAT-0001, FEAT-0003) — **carries a board card
created when the ADR is first drafted**, and its Status tracks the ADR's lifecycle (the gates move it):

- ADR `Draft` → **`create`** the card (Backlog) — the `adr` skill, at draft. If it was scoped from a
  pre-existing idea, **reuse that card** (`list` first); don't create a duplicate.
- through `Draft`/`Proposed` (+ judge) → stays **Backlog**
- ADR **Accepted** → `status "<item>" "In Progress"` (the `adr` / `adr-batch` accept step)
- ADR **Reviewing** → stays **In Progress** (no move)
- ADR **Implemented** → `status "<item>" "Done"` (the `adr-impl-review` gate)

**Pure-infra / process / refactor ADRs skip the card** (a rename, a tooling/e2e ADR, the process ADR) —
and a free idea that never becomes an ADR just lives in Backlog. When unsure, read the ADR's `Realizes:`
header: a user-facing feat-row gets a card; an infra/process/refactor row does not.

## Identity / hygiene

Never write the local machine username, home-dir path, or personal email into an item title
or body — the only identity the board knows is `green-0-rabbit`. The driver enforces nothing
here; it's on the caller, same as every other tracked artifact in this repo.

## If `ids` reports DRIFT

The ids are stable, but if the board is ever recreated the option ids change. `driver.py ids`
re-queries the live Status options and prints `MATCH` or `DRIFT`. On `DRIFT`, update the
`PROJECT_ID` / `STATUS_FIELD_ID` / `STATUS_OPTIONS` constants at the top of `driver.py` from
its printed live values — that is the single place the coordinates live.
