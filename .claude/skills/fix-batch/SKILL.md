---
name: fix-batch
description: Work through MANY funcd GitHub issues on autopilot — a tracker issue's sub-issues (e.g. a chaos-campaign group tracker), a list of issue numbers, or a label query. Every issue is fixed (`/fix`) and independently reviewed (`/fix-review`) as its own parallel job in its own worktree; then a cheap integrator per group cherry-picks the passing fixes onto the latest main, runs the repo-wide gate once, and opens one PR per group; one ledger PR records the batch's reviews. Issues that need an ADR are set aside for `/adr`; security advisories keep their private flow. Use for "fix all the issues in tracker 195", "fix the workflow group", "batch-fix 25, 26 and 27", "work through the high-priority bugs".
---

# Fix batch

The batch counterpart of [`/fix`](../fix/SKILL.md), as [`adr-batch`](../adr-batch/SKILL.md) is for ADRs. Each
issue still gets the full test-first fix and its own independent [`/fix-review`](../fix-review/SKILL.md). Read
`CLAUDE.md` → *Running subagents and workflows efficiently* first: the shape below follows from it (measured:
3.0 agent-minutes per fix and 1.5 per review, against 6.3 and 7.1 with per-group chunks and repeated checks).

## Running it

The pipeline is committed (issue #566): `.claude/workflows/fix-batch.js` for funcd issues and
`.claude/workflows/shim-fix.js` for issues whose root cause is in pyvvo/funcd-typescript or pyvvo/funcd-python.
After Step 0, run it with the Workflow tool (`name: "fix-batch"` or `name: "shim-fix"`; each file's header lists its args, and `wave` must be new for each run, since it goes into every branch and report name); it carries
Steps 1–3, the wave check and the lanes, and returns per group the PR, the fixed and parked issues and the new
defects noted. Each issue carries its kind: a bug or flake is fixed test-first; a task (a cleanup from an audit)
is done to its "Done when", with a failing-first test only where it changes behavior. An issue can also carry a
`note`, a decision the person already made for it, which the fixer follows and the reviewer judges against.
Around it:

- `scripts/agent/triage.sh <pr>`: the one-screen view before queueing (checks, size, the gate and audit lines, the
  masking-pattern counts of the added lines);
- `scripts/agent/queue.sh <state-dir> <pr>...`: enqueue green PRs, or hold the ones that need a lane (and any
  PR it cannot read); after a held PR's lanes pass, add the `<pr>:<12-character head sha>` line its held message prints to `<state-dir>/lane-passed.txt` and rerun;
- `scripts/agent/lanes.sh <branch>:<lane|all>...`: the lanes of a held PR, one after the other;
- `scripts/agent/watch-prs.py <state-dir>/handled.json`: wait in the background for the next PR event.

## Step 0 — Resolve and order

1. **Collect the issues**:
   - a tracker: `gh api --paginate repos/pyvvo/funcd/issues/<T>/sub_issues --jq '.[] | {number, state, title, labels: [.labels[].name]}'`;
   - a list: as given;
   - a label query: `python3 .claude/skills/issue-management/driver.py list --label <label>`.
2. **Filter**: drop closed issues; set aside every `needs-adr` issue (for `/adr`) and any security advisory
   (fixed one at a time with `/fix`'s advisory flow). Note whether a group keeps open issues, so its PR does not
   close the tracker.
3. **Order**: `priority/critical` → `low`, then by issue number. Larger batches go in waves, merged between waves,
   so later work starts from a main that already has the earlier fixes.

## Step 1 — One job per issue, in parallel

For every issue at once, each in its own worktree from `origin/main` under the session scratchpad (never the
shared checkout: `git worktree add -q -b fix/w<wave>-i<N> <scratch>/wt/i<N> origin/main`):

1. **Fix** — `/fix` Steps 2–6: regression test first, root-cause fix, revert check, one commit with `Fixes #N`.
   Checks on the touched packages only; no e2e suite, no `go test ./...`, no lanes — the gate runs those once.
2. **Review** — as soon as the fix is committed, `/fix-review` by an independent agent in the same worktree: the
   revert check of `/fix-review` Step 2.1 (an `origin/main` overlay of the changed non-test files, or a
   scratch worktree for a test that reads a file at runtime; never a revert of the commit, which also removes
   the test), 1–3 targeted mutants, reuse and conventions, the touched packages' checks. Its report goes to a scratch reports dir; it returns the ledger
   fields instead of editing the ledger.
3. **Rework** on `changes-requested`: one new commit per round in the same worktree, then a re-review; at most 3
   reviews. An issue that still fails, does not reproduce, or turns out to need a decision is **parked** with the
   reason (and `needs-adr` when it applies) — never forced.

## Step 2 — Integrate each group (a fast model is enough)

When all of a group's jobs are done:
1. A branch `fix/w<wave>-<T>-<group-key>` from the **latest** `origin/main`; cherry-pick the passing issues' commits in
   priority order. A mechanical conflict is resolved; any other skips that issue as parked.
2. Copy the group's review reports into `docs/reviews/` and commit them. Do **not** touch
   `docs/reviews/model-ledger.json` or `model-scorecard.md`: parallel PRs appending to them conflict.
3. Run `scripts/agent/gate.sh` once: the [bloat audit](../bloat-audit/SKILL.md) of the group's diff, `just ci-full`,
   the Linux checks and a clean tree. When `just ci-full`, the Linux checks or the tree check fail, revert the
   commits of the issue that caused it, remove its review reports and rerun; if the rerun fails too, open no PR and
   report it. A hard audit flag is removed with a follow-up commit when that is mechanical (reuse the existing
   helper, explain the nolint); otherwise that issue is reverted and parked, and goes back to its fixer as a rework
   round (a fix, or a justified `audit-allow:` line in its commit message) in the next run. The integrator never
   writes a waiver. A `host` failure means the ports are exhausted: wait and rerun.
4. Push and open the PR: a Conventional-Commit title naming the group (it becomes the squash commit and the
   release note), typed `fix` when any applied commit changes product behavior (the notes list only fix and feat); a table (issue, cause, regression test, review report); the parked issues; the gate result; one
   `Fixes #N` line per fixed issue, plus `Fixes #<T>` only when the group closes every open sub-issue; a
   "Lima lane pending" note when a fix touches `internal/runtime/containerd`, `internal/network`, `e2e/` or
   `scripts/lanes.yaml`; the attribution line.

## Step 3 — One ledger PR

Record every review of the batch, in order, with `scorecard.py record --issue <N> --phase fix …` (see
`/fix-review` Step 5) on one branch from `origin/main`, and open one PR, merged after the group PRs (its report
links point at files they add).

## Step 4 — Wave check, lanes, then merging

1. Before queueing any group PR, run `scripts/agent/wave-check.sh <branch>...` on all of the wave's group branches,
   in queue order. A `CONFLICT` line names a branch that conflicts with the ones before it: rebase it on them, rerun
   its gate, push, and rerun the check. When no branch conflicts, it gates the merged wave once (issue #563).
2. Groups flagged "Lima lane pending" run their lanes one VM at a time, from a checkout under `$HOME`; the lane
   recipes take a host lock (`scripts/lane-lock.sh`), so lane jobs started at once run one after the other.
3. Read every check's conclusion, then enqueue green PRs with the GraphQL `enqueuePullRequest` mutation
   (`gh pr merge` fails: auto-merge is off) with `scripts/agent/queue.sh`, which holds a lane-pending PR until its
   lane passes and is recorded in `lane-passed.txt`. A merge-group run that
   fails on a known flake: rerun or re-enqueue. A PR left conflicting by an earlier merge: rebase it on main,
   rerun the gate, push.
4. Wait in the background with `scripts/agent/watch-prs.py <handled.json>` (it exits when a PR turns green, red
   or conflicted), not in a foreground loop.
5. Leave the release-please PR open until the batch's groups have merged, then merge it once: one release per
   resolved batch.
6. Once the groups have merged, run the bloat audit in full mode over the batch's range and list the cleanup
   issues it suggests, for filing after the user's go.

## When to stop and ask

A fix needs an ADR; the issue appears wrong (it does not reproduce); a review loop does not converge; a fix would
touch a flaw that is tracked privately as an advisory. Report the issue, the reason and the options, and continue
with the rest of the batch.

## Report

Per group: the PR, the fixed issues (review verdicts and reports), the parked ones with reasons, the gate and
lane results; the issues set aside for `/adr` or the advisory flow; the ledger PR; the full-mode audit's summary;
any new defects the fixers listed or the audit suggests, for filing with `/issue-management`.
