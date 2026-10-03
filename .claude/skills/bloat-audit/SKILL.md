---
name: bloat-audit
description: The bloat audit of funcd fix work — `scripts/agent/audit.py` checks that a change does not bloat the codebase or bring in disorder (size per package and commit, new duplicated code, complexity growth, masking patterns such as a production time.Sleep or an unexplained //nolint, comment narration, new dependencies, and in full mode flaky tests), and fails on a hard flag that no justified `audit-allow:` line waives. It runs once per PR as a step of `scripts/agent/gate.sh` (after the per-issue reviews in `/fix-batch`), and in full mode at the end of a fix campaign. Use to run or read the audit, to act on its flags, or to turn a campaign report into cleanup issues.
---

# Bloat audit

A fixer can pass its regression test and its review while it copies a helper, sleeps instead of waiting, or
narrates the code. Each review sees one issue's diff; the audit measures the whole PR, and in full mode the whole
campaign. Paths are repo-root-relative; run it through `scripts/agent/d`.

## Where it runs

| When | Command | Cost |
|---|---|---|
| Once per PR, in the gate: the `/fix-batch` integrator's, after the per-issue reviews, or `/fix` Step 5 | the `audit` step of `scripts/agent/gate.sh`: `scripts/agent/audit.py --base origin/main`; report in `.cache/gate/audit.log`, data in `.cache/gate/audit.json` | 2–4 s, scoped to the diff |
| Once at the end of a fix campaign, after its last PR merged | `scripts/agent/audit.py --base <first commit>^ --head origin/main --full --report-only --json <scratch>/audit.json` | minutes: adds `go test -count=3` on the touched packages |

Never per fixer or per reviewer: `/fix-review` cites the gate's output when it exists and runs nothing extra. Other
flags: `--pr-body <file>` (also read waivers from a PR body), `--no-lint` (skip golangci-lint).

## Reading the report

| Section | What it measures | How to read it |
|---|---|---|
| Size | lines added/removed per package, split production / test / docs / other (test: a `_test.go`, `testdata/`, `tests/`, `e2e/`, `bench/`, `internal/testkit/`, or a file that imports `testing`, such as the shared contract suites); commit outliers (over 300 production lines and 3× the median commit) | in a fix, test lines should outweigh production lines; a large production delta for one issue is a scope question |
| Lint deltas | golangci-lint with `scripts/agent/audit.golangci.yml` on the touched packages, base against head, from each package's own module root (`bench/badger` and `bench/expr-engine` have their own `go.mod`): `dupl` (a clone at least half made of new lines; an edit inside a clone that already existed at the base is not new), `gocyclo`, `gocognit`, `funlen` (a new function over the threshold, "new"; an existing one that crossed it, "crossed N"; one that grew, "was N"), `godox` and `nolintlint` on added lines | a fix that adds a branch to an already complex function is the usual cause; split it when the growth is large |
| Cross-package clones | 8 identical consecutive production lines (comments, blanks and lines such as `}` or `return err` ignored), at least half new, that also exist in another package | report-only: the same block added to two packages calls for one shared helper; the reviewer weighs it |
| Masking patterns | added lines, without their comments and string literals (also a block comment or raw string that spans lines): `time.Sleep` or a bare `<-time.After` (production / test), `t.Skip`, an `_ =` discard of a call's result (not `_ = x.Close()` or `io.Copy(io.Discard, …)`), `//nolint` directives, retry loops, a timeout or wait raised on the same line | a test sleep or skip needs its reason in the test; a raised timeout must not hide a slow path |
| Comments | added comment lines against code lines (production and test), and blocks of 6+ comment lines (`doc` above a declaration, `inline` inside a body); the comment lines a hunk removed net out, so a rewrapped comment is not new | an inline block is the narration the house style forbids; a doc block says *why*, once |
| Dependencies | new `require` or `tool` lines in any `go.mod` (direct and indirect), indirect ones made direct, and version bumps | a new direct dependency is a hard flag; a promoted one is already in the module graph, so it is reported (four reviewed fixes of the 2026-10 campaign promoted one, such as `golang.org/x/sync`), and so are bumps |
| Flakiness (full mode) | tests that pass and fail across `go test -count=3`, and tests that fail every run | a flaky test is a `kind/flake` issue |

The thresholds (`dupl` 75 tokens, `gocyclo` 20, `gocognit` 30, `funlen` 80 lines or 50 statements) were calibrated
on the 74 squash commits of the 2026-10 fix campaign: none of its reviewed fixes trips a hard flag, while a copied
22-line function (the fixture in `scripts/audit_test.go`) does. `dupl` at 50 would have failed one reviewed fix
(an 8-line error switch repeated in two readers), and the main tree has 427 production clone sides at 50 tokens
against 173 at 75, so a lower value fails fixes on idiom rather than on copying. Recalibrate the same way (the
audit on each squash commit against its parent) before changing them.

## Hard flags

These fail the gate (exit 1). Everything else is reported, never failed.

| Flag id | Flag | Fix it by |
|---|---|---|
| `dupl:<file>` | a new `dupl` clone in production code | reusing the existing code: call it, or extract one helper both use |
| `sleep:<file>` | `time.Sleep` (a call or a function value) or a bare `<-time.After(d)` statement added to production code | waiting on the event: a channel, `ctx.Done()`, a `time.Timer` in a `select`, an existing backoff helper |
| `nolint:<file>` | a `//nolint` without `// <reason>`: nolintlint's require-explanation, or the line pattern in a file the lint run skips (it runs with `GOOS=linux` and the `dev`, `e2e` and `integration` tags, so a `//go:build !linux` file is never analyzed) and under `--no-lint` | fixing the lint, or `//nolint:<linter> // <why it is a false positive>` |
| `dep:<module>` | a new direct dependency in a `go.mod` | the standard library or a pinned dependency; a new one must be essential and Apache-2.0/MIT/BSD, and the user decides |

A flag that is right to keep is waived by one line in the message of the commit that added the flagged lines
(`git blame` over the range finds it), or in the PR body, which covers the whole PR; the id is the one the report
prints:

```
audit-allow: sleep:internal/example/poll.go the poll interval is part of the driver's contract
```

A bare kind (`audit-allow: dupl <reason>`) waives every flag of that kind in its commit; avoid it. The reason says
why the flag is right, not that it is inconvenient. A waiver is a review point: the fixer adds it in a rework
round, the integrator never writes one, and the report lists every waiver it applied.

## From a full-mode report to cleanup issues

1. Run full mode with `--report-only --json` once the campaign's last group PR has merged.
2. Triage each finding against the code and keep only real defects. A clone, a complexity hot spot, a narration
   block or a masking pattern is a `kind/task` (area from the path, usually `priority/low`); a flaky test is a
   `kind/flake` with the `-count=3` output as its Failure section.
3. One defect per issue, grouped by package; related issues under one tracking issue, most relevant first.
4. File them with [`/issue-management`](../issue-management/SKILL.md) after the user's go: `driver.py list --state
   all` first for duplicates, then `create --dry-run`, then `create`, citing the report's `file:line` locations.
