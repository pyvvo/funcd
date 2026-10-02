---
name: fix-review
description: The review gate for a funcd bug fix — independently verify a `/fix` change against its GitHub issue by RUNNING it — the regression test fails without the fix and passes with it, the root cause (not the symptom) is fixed, nothing unrelated changed, the change reuses what the codebase already has instead of duplicating or reinventing it, the conventions hold, no Accepted ADR is contradicted, and the checks are green. Produces an evidence-cited verdict (pass / changes-requested / fail), writes `docs/reviews/issue-<N>-fix-<model>.md`, and records a ledger row so the per-model scorecard covers fixes. Use for "review the fix for issue 24", "check this fix", "run the fix review". It reviews and records; it never edits the work.
---

# Fix review

The counterpart of [`adr-impl-review`](../adr-impl-review/SKILL.md) for work that has an issue instead of
an ADR. The same discipline applies: **run the verification, don't eyeball it**, attribute every finding
fairly, and **review and record — never fix**. A `changes-requested` verdict goes back to
[`/fix`](../fix/SKILL.md).

## Step 0 — Inputs

1. **The issue** (`#N`, or a `GHSA-…` id for an advisory fix): `python3 .claude/skills/issue-management/driver.py show <N> --body`.
2. **The model that produced the fix** — required; the ledger is keyed on it.
3. **The change**: the fix branch against `origin/main` (`git diff origin/main...HEAD`).

## Step 1 — Orient

Read the issue (its steps, expected behavior and claimed cause), the diff, and the ADRs that govern the
touched code. The bar is the **fix checklist** below plus the governing ADRs' Contracts.

## Step 2 — Run the verification

Through `scripts/agent/d <cmd>` (the cached pinned dev shell); capture real output for every claim, filtered to what matters:

1. **The regression test fails without the fix.** Overlay the `origin/main` version of each changed
   non-test file (`git show origin/main:<file>` into a scratch file, `go test -overlay`) and run the
   `TestIssue<N>_…` test: it must fail, for the issue's reason. Passing without the fix → Blocker.
2. **It passes with the fix**, un-skipped, under `-race` for its package.
3. **The user-visible behavior is fixed**: rerun the issue's own steps when that is cheap (a probe, a CLI
   sequence, a real daemon), not only the unit test.
4. **Cause, not symptom**: the change removes the cause named in the issue (or a better-supported one).
   A longer timeout, an extra retry, a swallowed error or a skipped test that hides the defect → Blocker.
5. **Mutants**: 1–3 overlay mutants on the fix's key lines; each must fail a test. A survivor is a test gap.
6. **Scope**: every hunk serves the issue. An unrelated change → Major; a weakened or deleted test → Blocker.
7. **Reuse, no duplication**: for everything the change adds — a helper, a type, a constant, a test harness,
   a dependency — search the package, its neighbours, `internal/platform`, `api/fault`, `internal/testkit`,
   the existing test harnesses, the module's dependencies and the standard library for what already does
   it. Duplicated logic, a copy-pasted block, or a hand-rolled version of an existing helper or library
   feature → Major (Minor when trivial), naming the existing code to use instead.
8. **Conventions**: ADR-0002 (ports and drivers, `api/fault` errors, typed IDs and enums, ctx-first, slog
   only, no `any` in signatures, the import graph), the `CLAUDE.md` style rules (block-style YAML,
   top-level imports, no comment bloat), and the surrounding code's naming and idiom. A breach → Major
   (Minor when cosmetic).
9. **ADRs**: the fix contradicts no Accepted/Implemented ADR's Decision or Contracts (if it must, that is
   an `adr` finding — the fix needs an ADR), and no Accepted/Implemented ADR file was edited (Blocker).
10. **Checks**: rerun the touched packages' tests (`-race`), vet and lint yourself, through `scripts/agent/d`.
   The repo-wide set (`scripts/agent/gate.sh`, then CI) runs once per PR: read its result, and run it yourself
   only for a single fix that no gate has run on. Never run the e2e suite per review.
11. **Shape**: a conventional `fix(<scope>):` subject, `Fixes #N`, the attribution trailer, one issue per commit.
12. **No dev-machine references** — a silent check, as in `adr-impl-review` Step 2: never write a hygiene
    section, never transcribe a path, username or grep pattern; a leak is a Blocker described generically.

## The fix checklist (the Definition of Done)

Count the items that apply (`--dod-total`) and those that hold (`--dod-passed`):

1. A `TestIssue<N>_…` regression test reproduces the issue's behavior.
2. It fails on the pre-fix code, for the reported reason.
3. It passes with the fix, un-skipped, under `-race`.
4. Reverting or mutating the fix's key lines fails a test.
5. The root cause is fixed, not masked.
6. Only the issue's scope changed; no test was weakened or deleted.
7. No Accepted/Implemented ADR is contradicted or edited; living docs stay true.
8. Build, vet, lint (host and Linux) and tests are green; e2e and the lane too where they cover the path.
9. Conventions hold: ADR-0002, the `CLAUDE.md` style rules, and the surrounding code's naming and idiom.
10. The change reuses what exists: no duplicated logic, and no new helper, type, harness or dependency where
    an existing one, or the standard library, does the job.
11. The commit and PR shape: `fix(<scope>):`, `Fixes #N`, trailers.

## Step 3 — Attribute

- **`model`** — the fixer got it wrong. Counts against the model.
- **`issue`** — the issue itself was wrong (a wrong cause, a step that never reproduced). Recorded, not scored.
- **`adr`** — the right fix needs a decision. Recorded, not scored; routes to `/adr`.
- **`env`** — tooling or environment. Recorded, not scored.

## Step 4 — Verdict and report

**pass** (checklist met, no Blocker/Major), **changes-requested** (back to `/fix`), or **fail** (the
change does not address the issue). Write the report to `docs/reviews/issue-<N>-fix-<model>.md` (append
`-2`, `-3` on re-review) in the format of `adr-impl-review`'s `references/review-method.md`: severity
tiers, ✅ verified correct, recommendation — each finding with its evidence and attribution. For an
advisory fix, the report goes into the private fork's PR instead, until the advisory is published.

## Step 5 — Ledger

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --issue <N> --phase fix --model <model> \
  --verdict <pass|changes-requested|fail> \
  --blockers <n> --majors <n> --minors <n> --model-attributed <n> \
  --dod-passed <n> --dod-total <n> \
  --report docs/reviews/issue-<N>-fix-<model>.md \
  --notes "<one line; tag each finding's attribution>"
```

## Step 6 — Close

No status to stamp: the PR's `Fixes #N` closes the issue when it merges. Report the verdict, the
findings with attribution, the checklist count and the ledger row; on a pass, hand back to `/fix` Step 8.
