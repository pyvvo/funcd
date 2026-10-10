# Fix review — issue #852 (watch-prs.py ignores feat/ and impl/ pull requests)

- **Branch**: `fix/852-watch-prs-prefixes`, one commit `5298e761` — `fix(agents): report PRs of every branch in the PR watcher`
- **Producing model**: claude-opus-5-5
- **Reviewer date**: 2026-10-08
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (1 model, 1 issue). Definition of Done 12/12.

## What changed

- `scripts/agent/watch-prs.py`: the `headRefName.startswith(("fix/", "chore/", "docs/", "release-please--"))`
  filter in `snapshot()` is removed, so every open PR is classified and reported. The docstring now says
  "whatever its branch", drops the "merged" state the watcher never reported (it queries `states:OPEN` only),
  and names the script correctly (`watch-prs.py`, not `watch.py`).
- `scripts/watch_prs_test.go` (new): `TestIssue852_WatchPRsReportsEveryBranchPrefix` puts a fake `gh` on
  `PATH` that prints a GraphQL snapshot with a green `feat/` PR, a red `impl/` PR and a conflicting `fix/` PR,
  runs the script with `max_minutes=0`, and asserts all three event lines.

## Verification run

| Check | Result |
|---|---|
| Regression test on pre-fix code (scratch worktree of `origin/main` with the test file copied in; the script is read at runtime, so an overlay does not reach it) | **FAIL**, for the issue's reason: output holds only `CONFLICT #900 [fix/900-thing]`, the `feat/` and `impl/` PRs are missing |
| Regression test with the fix, `-race`, darwin | PASS |
| Regression test with the fix, Linux (cross-compiled test binary in a `python:3.14-slim-bookworm` container, non-root `--user 1000:1000`) | PASS |
| `go test -race -count=1 ./scripts/` (darwin) | ok (25.3 s) |
| `go vet ./scripts/`, darwin and `GOOS=linux` | clean |
| `golangci-lint run ./scripts/`, darwin and `GOOS=linux` | 0 issues |
| Mutant A: re-add a filter with `fix/` + `feat/` only | killed (`RED #851 [impl/...]` missing) |
| Mutant B: re-add a filter with `fix/` + `feat/` + `impl/` | **survived** (see Minor 1) |

No e2e suite, no repo-wide test, no Lima lane (none covers this path).

## Callers (no regression)

Grepped every reference in the repo (justfile, `scripts/`, `.github/`, `.claude/`, `docs/`):

- `.claude/CLAUDE.md` and `.github/copilot-instructions.md` ("exits when a PR turns green, red or conflicted") —
  still true.
- `.claude/skills/fix-batch/SKILL.md` (lines 29, 97) — still true; it now also wakes for ADR-pipeline PRs, which
  is the requested behavior. Events are keyed in `handled.json`, so each wakes the caller once.
- `scripts/agent/queue.sh` — writes `<n>:<sha>:green` keys into `handled.json`; the key format is unchanged.
- `docs/method/README.md` — a mention only.
- No justfile recipe or workflow calls it. The repo's recent branch prefixes are `docs/ feat/ fix/ impl/
  release-please--`; there is no bot (e.g. dependabot) whose PRs would now add noise.

## Findings

### Blocker
None.

### Major
None.

### Minor

1. **The test does not pin "every branch"** — `model`. The docstring and the test name promise a report
   "whatever its branch", but the fixture only uses `feat/`, `impl/` and `fix/`; an allowlist extended with
   `feat/` and `impl/` (Mutant B) passes the test. A fourth PR with an arbitrary prefix (e.g. `misc/x`) would
   pin the removal of the filter rather than its extension. Low impact: the fix itself is correct.
2. **The issue's expected behavior includes "or when it merges"** — `issue`. The watcher never reported a merge
   (it queries open PRs only, before and after this fix); the fixer correctly removed "merged" from the docstring
   and said so in the commit message rather than widening scope. Recorded, not scored.

## ✅ Verified correct

- Cause, not symptom: the prefix filter named in the issue's root cause is the line removed; no timeout or retry
  added.
- Scope: two files; every hunk serves the issue (the docstring name fix is a one-word correction in the
  docstring the issue cites).
- Reuse: the test reuses the package's `exitCode` helper (`scripts/guards_test.go`) and the same fake-binary-on-PATH
  idiom as `lanes_test.go` / `guards_test.go`; no new helper, dependency or harness.
- Conventions: top-level imports, no comment narration (one why-comment naming the issue), no YAML.
- ADRs: tooling only; no Accepted/Implemented ADR touched or contradicted.
- Shape: `fix(agents):` subject, `Fixes #852`, the attribution trailer, one issue in one commit.
- Siblings: no other agent script filters PRs by branch prefix (`queue.sh`, `triage.sh` take PR numbers).

## Definition of Done — 12/12

1 regression test ✓ · 2 fails pre-fix for the reason ✓ · 3 passes under `-race` ✓ · 4 revert fails a test ✓
(Mutant B noted as Minor 1) · 5 root cause ✓ · 6 scope, no weakened test ✓ · 7 ADRs ✓ · 8 build/vet/lint host and
Linux, tests ✓ · 9 conventions ✓ · 10 reuse ✓ · 11 commit shape ✓ · 12 every case tested, no sibling ✓

## Recommendation

Pass. Optionally add a PR with an arbitrary branch prefix to the fixture to close Minor 1. Hand back to `/fix`
Step 8.
