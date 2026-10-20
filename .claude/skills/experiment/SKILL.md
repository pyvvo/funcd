---
name: experiment
description: Answer one question about funcd by measuring it, and record the answer in pyvvo/experiment as a folder anyone can rerun — a pinned flake, a justfile, scripts that measure, and a one-page report with generated numbers, findings, refs and a diagram. Use for "experiment with X", "spike X and measure it", "try option A before the ADR", "rerun experiment 0001", "/experiment". It measures and reports; it decides nothing (the decision is an ADR that cites the experiment).
---

# Experiment

An experiment answers one question by measuring it, so the answer outlives the session and anyone can rerun it. It
lives in [pyvvo/experiment](https://github.com/pyvvo/experiment) (private), one folder each. That repo's README holds
the rules and the results format, and its `tools/check.py` enforces them. Paths below are relative to a clone of it
beside funcd (`../experiment`).

## Step 0 — The repo

Clone it once beside funcd (`git clone https://github.com/pyvvo/experiment.git ../experiment`). Work on a branch from a
fresh main: `git -C ../experiment fetch && git -C ../experiment switch -c exp/<slug> origin/main`.

## Step 1 — The question

One question that a measurement or a yes/no check answers ("can dev run a whole App and reload in under a second?",
not "explore dev"), and the decision it informs: an issue, a card or an ADR.

## Step 2 — Scaffold

`python3 tools/new.py <slug> "<question>"` copies `template/` to the next `NNNN-<slug>/` and adds its index row.

## Step 3 — Pin everything

- funcd is a flake input at a commit (`github:pyvvo/funcd/<sha>`), with `nixpkgs.follows = "funcd/nixpkgs"` so Go is
  funcd's own. Sibling repos are inputs at their tags, with `flake = false` when only their sources are needed.
- Code that is not merged is a patch on that commit: the spike worktree's `git diff`, new files included with
  `git add -N`. `scripts/setup.sh` applies it with `patch -p1` (inside the experiment repo, `git apply` resolves the
  paths against that repo and skips them). Never push a spike branch to funcd for an experiment.
- Every tool the run needs comes from the flake: Go, just, Node, Python. Nothing comes from the machine.
- `git add` the folder, then `nix flake lock`: a flake sees only the files git tracks.

## Step 4 — Scripts that measure

- `scripts/setup.sh` builds what is pinned into `work/` (gitignored), once per pin and patch.
- `scripts/run.py` runs each scenario and writes `results/results.json` and `results/env.json` in the README's format.
  A timing has its samples; a check has pass and the detail it saw. `env.json` records the machine without identity.
- Time a change from the edit to its observable outcome (an HTTP answer, a log line) by polling. Take `SAMPLES`
  samples per timing (default 5). A tool's own "done in" line can measure its poll rather than the work.
- When the samples of one timing spread, find the cause in the logs' timestamps before reporting a median, then
  split the scenario. In 0001, back-to-back changes waited for the previous revision's drain (ADR-0143's 2 s
  `runtime.handOutSettle`), so isolated and back-to-back edits became two timings.
- The processes a run starts are its children. Stop them with SIGINT and touch no other process.

## Step 5 — Run, then report

`nix develop -c just run`, then `nix develop -c just report`, which renders `results/` into the README's results
block. A failed check is a result: keep it and explain it in Findings.

## Step 6 — Write the page

Question; Answer (one or two sentences, from Results); Approach (a few sentences and a mermaid diagram); Setup;
Results (generated); Findings (each with its evidence); Refs (the issue, the ADRs, `file:line` links at the pinned
commit); Limits (what was not tested). Short plain sentences. Outside Results, refer to a number by its timing's
name rather than typing it.

## Step 7 — Check and land

- `python3 tools/check.py NNNN-<slug>` passes, and the index row carries the answer.
- Commit `exp(NNNN): <the question, short>` with the attribution trailer, push the branch, open a PR in
  pyvvo/experiment and merge it once it is reviewed.
- Cite the experiment where it informs the decision: the issue, then the ADR's References, at its commit. The repo is
  private, so paste no result into a public issue without the decider's go.

## Rerun

`cd NNNN-<slug> && nix develop -c just run && nix develop -c just report`. Commit the new results when they changed.

## What this skill does not do

It decides nothing (that is `/adr`). A defect it finds goes to `/fix` or `/issue-management`, and a security finding
to a private advisory, never into the experiment repo.
