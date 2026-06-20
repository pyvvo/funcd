---
name: project-management
description: Manage the funcd GitHub Project board (Project #4, owner green-0-rabbit) — create a backlog item, change an item's status (Backlog / In Progress / Done), or refine an existing item's title/body. Use whenever asked to add/triage a backlog ticket, move a card, edit a project item, or list the board. All project ids are baked into driver.py, so there is nothing to discover and no `gh` invocation to hand-assemble.
---

# project-management — drive the funcd Project board

The backlog for this repo lives on **GitHub Project #4** (`@green-0-rabbit's funcd`,
<https://github.com/users/green-0-rabbit/projects/4>). This skill drives it through one
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

## Status follows the ADR lifecycle (when an item is scoped into an ADR)

Per the repo's working agreement (`.claude/CLAUDE.md`), an item that gets scoped into an ADR
**stays on the board** and its Status tracks the ADR's lifecycle — the ADR gates move it:

- fresh idea / through ADR `Draft`/`Proposed` → **Backlog**
- ADR **Accepted** → `status "<item>" "In Progress"` (done by the `adr` / `adr-batch` accept step)
- ADR **Implemented** → `status "<item>" "Done"` (done by the `adr-impl-review` gate)

Only items that **originated on the board** have a card to move; a normal roadmap ADR with no
backlog item skips this.

## Identity / hygiene

Never write the local machine username, home-dir path, or personal email into an item title
or body — the only identity the board knows is `green-0-rabbit`. The driver enforces nothing
here; it's on the caller, same as every other tracked artifact in this repo.

## If `ids` reports DRIFT

The ids are stable, but if the board is ever recreated the option ids change. `driver.py ids`
re-queries the live Status options and prints `MATCH` or `DRIFT`. On `DRIFT`, update the
`PROJECT_ID` / `STATUS_FIELD_ID` / `STATUS_OPTIONS` constants at the top of `driver.py` from
its printed live values — that is the single place the coordinates live.
