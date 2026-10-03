---
name: fix
description: Fix ONE funcd GitHub issue that needs no design decision (a bug or a flaky test), test-first — reproduce it with a regression test that fails on the unfixed code, make the smallest root-cause fix, prove the test fails again when the fix is reverted, run the checks, hand off to the independent `/fix-review` gate, and on a pass open the PR that closes the issue. Use for "fix issue 24", "work on issue 31", "fix this bug", or a rework after `/fix-review` returned changes-requested. An issue labelled `needs-adr` — or a fix that would change an Accepted ADR's decision — goes to `/adr` instead; a new feature or a refactor concept is an ADR, not a fix. A security advisory (GHSA) is fixed in its private fork. For many issues at once, use `/fix-batch`.
---

# Fix one issue

The ADR pipeline (`/adr` → `/adr-impl` → `/adr-impl-review`) builds decisions. This skill repairs
what is already built: the issue is the input, a **regression test** is the proof, and the
independent [`/fix-review`](../fix-review/SKILL.md) gate is the sign-off. Paths are repo-root-relative.

## Step 0 — Preconditions

1. **Read the issue**: `python3 .claude/skills/issue-management/driver.py show <N> --body`. It must be
   open and `kind/bug` or `kind/flake`. A `kind/feature` goes to `/adr`; a `kind/task` is not a fix.
2. **No design decision.** Stop and hand to `/adr` if the issue carries `needs-adr`, or if the only sound
   fix changes the Decision or Contracts of an Accepted/Implemented ADR (label it first:
   `driver.py relabel <N> --add needs-adr`). Never edit an Accepted or Implemented ADR.
3. **Rework?** If `docs/reviews/issue-<N>-fix-*.md` exists with `changes-requested`, its `model`
   findings are the work list; don't regress what it marked verified.
4. **Security advisory** (a `GHSA-…` id instead of an issue number): follow *Advisory fixes* below.

## Step 1 — Branch

From a fresh main, never on main: `git fetch origin && git switch -c fix/<N>-<slug> origin/main`.

## Step 2 — Orient

Read the code the issue points at and the ADRs that govern it (`docs/adr`, `blueprint.md`); the fix
must conform to them. The issue's cause is the reporter's claim — verify it in the code before
relying on it, and correct it in the handoff if it is wrong.

## Step 3 — Reproduce first

Write the regression test **before** touching the code:

- Name it `TestIssue<N>_<Behavior>` so the issue ↔ test link is grep-able; put it beside the package's
  existing tests and reuse their harnesses (fakes, fake clocks, e2e helpers).
- Make it deterministic: a hook, a fake clock or an injected race, not a sleep. A flaky-test issue gets
  either a deterministic reproduction or `-count=N` evidence that the nondeterminism is gone.
- Run it on the unfixed tree. It must **fail for the reported reason** — keep the output. If it passes,
  the issue does not reproduce as written: stop and report it (never close the issue yourself).

## Step 4 — Fix the root cause, smallest change

- Remove the cause; never mask the symptom (no longer timeout, extra retry, swallowed error, `t.Skip`,
  or weakened assertion).
- Reuse before you write: search the package, its neighbours, `internal/platform`, `api/fault`,
  `internal/testkit`, the existing test harnesses, the module's dependencies and the standard library for
  what the fix needs. Never duplicate logic or hand-roll what already exists; `/fix-review` checks this.
- Change only what this issue needs. A second defect found on the way is a new issue: note it in the
  handoff and file it with `/issue-management` after the user's go.
- Keep living docs true (blueprint, feat, READMEs, CLI help) if the fix changes what they describe.
- House style holds (ADR-0002, `CLAUDE.md`): block-style YAML, top-level imports, no comment bloat.

## Step 5 — Prove it

Run Go and `just` through `scripts/agent/d <cmd>` (the pinned dev shell, cached; see `CLAUDE.md` → *Running
subagents and workflows efficiently*); keep each command's real output, filtered to what matters:

1. The regression test passes, un-skipped, under `-race` for its package.
2. **Revert check**: build the pre-fix version of each changed non-test file as an overlay
   (`git show origin/main:<file>` into a scratch file, `go test -overlay`) and confirm the regression
   test fails again. A test that passes without the fix proves nothing. An overlay changes only what the
   build reads, not a file the test reads at runtime (`os.ReadFile`, `parser.ParseFile` with a path).
   Embed a Go source the test checks with `//go:embed`, which the overlay does replace
   (`internal/function/doc_test.go`); for a file the test cannot embed (a doc, a file outside the
   package), run the test in a scratch worktree of `origin/main` with the test file copied in
   (`git worktree add --detach <scratch> origin/main`, then `git worktree remove --force <scratch>`).
3. The touched packages: their tests (`-race`), `go vet` and `go tool golangci-lint run` on them, and
   `go build ./...`.
4. The repo-wide checks, once, when the branch is ready for its PR: `scripts/agent/gate.sh` (the
   [bloat audit](../bloat-audit/SKILL.md), `just ci-full` with e2e, the Linux build/vet/lint, a clean tree).
   In a `/fix-batch` the group's integrator runs it instead.
   Add the Lima lane when the fix touches `internal/runtime/containerd`, `internal/network`, `e2e/` or
   `scripts/lanes.yaml`. Never run the e2e suite or `go test ./...` more than this once.

## Step 6 — Commit

One commit per issue on the branch:

```
fix(<scope>): <what is fixed, as the release note should read>

<cause, in a sentence or two> <the fix> Regression test: TestIssue<N>_<Behavior>.

Fixes #<N>

Co-Authored-By: <the trailer the session's attribution reminder gives>
```

## Step 7 — Review gate

Hand the branch to [`/fix-review`](../fix-review/SKILL.md) with the issue number and the producing
model — an independent agent, never this context. On `changes-requested`, fix the `model` findings and
re-review; stop and surface after 3 loops. `issue`/`adr`/`env` findings are reported, not fixed here.

## Step 8 — PR (after a pass)

Add the review report and the ledger change the gate wrote (`docs/reviews/`) to the branch — in a
`/fix-batch`, only the report: one ledger PR records the batch, since parallel PRs appending to the ledger
conflict — push, and open the PR: the title is the commit subject; the body has **Summary**, **Cause**, **Fix**, **Test** (the
regression test and the revert-check evidence), **Checks** (what ran), the review report link, and
`Fixes #<N>`, ending with the attribution line. Then follow the session's PR tools for CI. Merge only on
the user's go, through the merge queue: repo auto-merge is off, so `gh pr merge` fails — enqueue with
the GraphQL `enqueuePullRequest` mutation after reading every check's conclusion.

## Advisory fixes (GHSA)

A security advisory is fixed without public traces until it is published:

- Work in the advisory's temporary private fork (`gh api -X POST repos/pyvvo/funcd/security-advisories/<GHSA>/forks`);
  never push the branch to the public repo, open a public PR, or describe the flaw in public text.
- The private fork may not run CI: the local checks of Step 5 are the evidence (confirm on first use).
- `/fix-review` runs as usual, but its report goes into the private fork's PR, not `docs/reviews/`,
  until the advisory is published.
- The decider merges from the advisory page and cuts the release; the advisory is then published with
  the patched version, and the review report and ledger row are committed afterwards.

## Close

Report: the issue, the cause (and any correction to the reported one), the regression test and its
revert-check result, the checks run, the review verdict, the PR link, and any new defect noted for filing.
