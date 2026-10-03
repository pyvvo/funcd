# Fix review — issue #547 (keep the failing run's output in the flake report)

- **Change**: branch `fix/i547`, commit 7fad9d1 `fix(audit): keep the failing run's output in the flake report`
- **Files**: `scripts/agent/audit.py` (+7/-4), `scripts/audit_test.go` (helper extraction + `TestIssue547_FlakyOutputKeepsFailingRun`)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Blockers

None.

## Majors

None.

## Minors

None.

## Verification (run, not eyeballed)

| Check | Result |
|---|---|
| Revert check — the test reads `scripts/agent/audit.py` at runtime, so an overlay cannot reach it; ran in a scratch worktree of `origin/main` with the branch's `audit_test.go` copied in (`/fix` Step 5.2) | `TestIssue547_FlakyOutputKeepsFailingRun` **FAILS** for the issue's reason: the reported output is the last 15 lines of the passing runs (`passing line 6` … `--- PASS: TestFlip`) and lacks `first run fails` |
| With the fix, `-race` | `TestIssue547_FlakyOutputKeepsFailingRun` PASS, `TestBloatAudit` PASS |
| Package tests `./scripts/` with `-race` | ok |
| `go vet ./scripts/`, `golangci-lint run ./scripts/` | clean, 0 issues |
| `python3 -m py_compile scripts/agent/audit.py` | ok |
| Mutant: report `run[k]` (the last per-run buffer) instead of `failed[k]` | killed by `TestIssue547` |
| Mutant: collect the lines of runs that **pass** instead of runs that fail | killed by `TestIssue547` |

Mutants ran in a scratch worktree of the branch; both scratch worktrees were removed and the review worktree is clean.

## Cause, not symptom

The issue names the cause: one `deque(maxlen=15)` per test spans all three `-count` runs, so passing runs push
the failure out. The fix buffers output per run (`run`), pops the buffer at each top-level `pass`/`fail` event,
and keeps the popped lines only for runs that failed (`failed`). The report now carries the failing run's
lines, which is the "Done when" target. Behavior for a package-level failure (key `(package)`) and for
subtests (folded into the parent key, their own pass/fail events skipped) is unchanged.

## Scope

Every hunk serves the issue. The `fixtureRepo` extraction moves the existing fixture-repository setup out of
`TestBloatAudit` unchanged so the regression test can reuse it; `TestBloatAudit` still passes. The issue
suggested adding the fixture to `TestBloatAudit`; a separate `TestIssue547_…` test follows the fix
pipeline's naming rule instead, which is the better choice. No test was weakened or deleted.

## Reuse and conventions

- No new helper duplicates existing code: the test helper is the existing setup, extracted; the Python change
  reuses the `collections.defaultdict`/`deque` idiom already in `flake_run`.
- Go imports at top level (`encoding/json` added there); doc comments state the why, no narration; no YAML touched.
- No ADR file touched and no Accepted ADR contradicted (tooling script only).
- Commit shape: `fix(audit):` subject, `Fixes #547`, attribution trailer, one issue.

## ✅ Verified correct — keep

- Per-run buffering with a pop at the top-level outcome event: small, exact, and keeps the 15-line cap per run.
- A regression fixture whose passing runs print more lines (20) than the cap, so it fails on the old code for
  exactly the reported reason.

## Checklist (Definition of Done)

11 of 11 apply and hold (item 8 checked on the touched package; the repo-wide and Linux checks run at the group gate).

## Recommendation

Pass. Hand back to `/fix` Step 8.
