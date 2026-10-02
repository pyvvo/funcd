---
name: fix-batch
description: Work through MANY funcd GitHub issues on autopilot — a tracker issue's sub-issues (e.g. a chaos-campaign group tracker), a list of issue numbers, or a label query — running `/fix` → `/fix-review` (looping until each passes) for every issue, most relevant first, and opening one PR per tracker group (one commit per issue) or one per issue. Issues that need an ADR are set aside for `/adr`; security advisories keep their private flow. Use for "fix all the issues in tracker 195", "fix the workflow group", "batch-fix 25, 26 and 27", "work through the high-priority bugs".
---

# Fix batch

The batch counterpart of [`/fix`](../fix/SKILL.md), as [`adr-batch`](../adr-batch/SKILL.md) is for ADRs. Each
issue still gets the full test-first fix and its own independent [`/fix-review`](../fix-review/SKILL.md);
the batch adds ordering, one branch per unit, and a single report.

## Step 0 — Resolve and order

1. **Collect the issues**:
   - a tracker: `gh api --paginate repos/pyvvo/funcd/issues/<T>/sub_issues --jq '.[] | {number, state, title, labels: [.labels[].name]}'`;
   - a list: as given;
   - a label query: `python3 .claude/skills/issue-management/driver.py list --label <label>`.
2. **Filter**: drop closed issues; set aside every `needs-adr` issue (for `/adr`) and any security advisory
   (fixed one at a time with `/fix`'s advisory flow).
3. **Order**: `priority/critical` → `low`, then by issue number. Present the ordered list and proceed.

## Step 1 — Branch

- A tracker group: one branch `fix/<T>-<group-slug>` from a fresh `origin/main`, one commit per issue.
- A plain list: one branch and PR per issue (`/fix` as is), unless the user asks for one PR.

## Step 2 — Per issue, in order

1. `/fix` Steps 2–6 on the batch branch: regression test first, root-cause fix, revert check, checks,
   one `fix(<scope>): …` commit with `Fixes #N` in its body. Each fix starts from the branch's HEAD, so it
   includes the earlier ones; breaking an earlier issue's regression test is this fix's defect.
2. `/fix-review` by an independent agent — never the fixer's context. Loop on `changes-requested`, at most
   3 times; an issue that still fails is **parked**: drop its changes from the branch, record why, move on.
3. An issue whose regression test cannot reproduce it, or whose fix turns out to need a decision, is parked
   with the reason (and `needs-adr` when it applies) — never forced.

## Step 3 — Close the batch

1. Rerun the full check set once on the branch, including e2e and the Lima lanes the fixes touch.
2. Push and open the PR. With squash merge the title becomes the one release note, so make it name the
   group: `fix(<scope>): <group> defects from #<T>`. The body is a table — issue, cause, regression
   test, review report — then one `Fixes #N` line per fixed issue, ending with the attribution line.
3. Follow the session's PR tools for CI; merge only on the user's go, through the merge queue
   (`enqueuePullRequest`; `gh pr merge` fails because auto-merge is off).

## When to stop and ask

A fix needs an ADR; the issue appears wrong (it does not reproduce); a review loop does not converge;
a fix would touch a flaw that is tracked privately as an advisory. Report the issue, the reason and the
options, and continue with the rest of the batch.

## Report

Per issue: fixed (review verdict and report) or parked (why), plus the issues set aside for `/adr` or
the advisory flow; the PR link; and the ledger rows the reviews recorded.
