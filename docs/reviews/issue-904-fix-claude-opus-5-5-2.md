# Fix review — issue #904, re-review round 2 (claude-opus-5-5)

- **Issue**: #904, `TestScenarioAppDriftSelfHealed` misses `status.lastSelfHeal` under `-race`
- **Branch**: `fix/904-app-drift-self-heal-race`, one commit `d11dfe52` (amended from `01496b2e`) —
  `test(app): stop the App drift scenario reading status.lastSelfHeal before the pass writes it`
- **Model**: claude-opus-5-5
- **Previous report**: round 1, pass with 1 model Minor (mutant m2 survived)
- **Verdict**: **pass**
- **Checklist**: 12 / 12
- **Findings**: 0 Blocker · 0 Major · 0 Minor

## What changed since round 1

Still test-only, one file (`pkg/funcd/app_pause_e2e_test.go`, now +68 −10). The commit touches no other file.

- `TestIssue904_SelfHealRecordAwaitsStatusWrite` now holds **both** healing passes. The first is held until a release
  1 s later. The test checks that the App reads Ready with no record while the pass is held. The second pass is held
  the same way, and the test checks that the App still carries the first record (`At` unchanged). The test then
  releases each pass and asserts the record through `waitSelfHeal`.
- The hold is now a gate fed by `time.AfterFunc` releases. A `stop` channel and a `WaitGroup` let the cleanup drain
  pending releases before it closes the gate, so a failing run cannot leave a send blocked or deadlock shutdown.
  The cleanup is registered after `startGC`, so it runs before the platform's cleanups (LIFO).
- Commit subject is now `test(app):` (the change is test-only), and the body ends with `Fixes #904` and the
  attribution trailer.

## Verification run (through `scripts/agent/d`)

| Check | Result |
|---|---|
| Mutant m1 / pre-fix single read: `waitSelfHeal` reads the App once (overlay of a scratch copy of the test file) | **killed** — TestIssue904 fails 3 / 3 on `status.lastSelfHeal is set` |
| Mutant m2: `return h != nil` in `waitSelfHeal` (the round-1 survivor) | **killed** — TestIssue904 fails 3 / 3 on `lastSelfHeal.at … is the write time` (the first record is returned for the second phase) |
| `TestIssue904_` + `TestScenarioAppDriftSelfHealed` + `TestScenarioAppPausedKeepsHotfix`, `-tags e2e -race -count=10` | 30 / 30 pass |
| `gofmt -l pkg/funcd` | clean |
| `go vet -tags e2e ./pkg/funcd/`, darwin and linux | ok · ok |
| `golangci-lint run --build-tags e2e ./pkg/funcd/`, darwin and linux | 0 issues · 0 issues |
| Identity / absolute-path grep on the commit diff | no hit |

No whole e2e package, repo-wide `go test` or Lima lane was run (per the gate's rules); the repo-wide gate runs once
per PR. The round-1 pre-fix scenario evidence (48 of 50 failures with the `origin/main` test file) and the root-cause
analysis (ADR-0212 Decision 2: the `self-healed` line precedes the App status write) still apply: the scenario and
helper code is unchanged from round 1.

## Findings

### Blocker
None.

### Major
None.

### Minor
None. Round-1 Minor 1 (the `since` arm of `waitSelfHeal` had no failing test) is **resolved**: m2 now fails 3 / 3.

## Observations (not scored)

- **Branch base is behind `origin/main`** — `env`. The branch sits on `6de90c76`; `origin/main` has two newer
  commits that touch `pkg/funcd/funcd.go`, `options.go` and add hold tests. `git merge-tree` merges the branch onto
  `origin/main` without conflict. The integrator rebases and the PR gate runs on the result.
- The round-1 uncaptured paused-hotfix failure did not recur in these 10 runs (it is still attributed to #916's
  `buildCmd` under `-race`, unproven).

## ✅ Verified correct — keep

- Both phases of the log-then-write window are now pinned deterministically by the hold, not by scheduling luck.
- The cleanup ordering (stop → wait for releases → close gate) is sound: no send to a closed channel, no blocked
  release, and later passes proceed once the gate closes.
- Everything kept from round 1: cause not symptom, the 5 s ADR bound, one helper reused by three scenarios, no
  production change, ADR-0212 unedited, conventions (top-level imports, why-comments only, block style n/a).

## Checklist (12 / 12)

1 ✅ TestIssue904 reproduces the window (both phases) · 2 ✅ pre-fix single read fails (m1 3/3) · 3 ✅ passes `-race` ·
4 ✅ mutants m1 and m2 killed · 5 ✅ root cause · 6 ✅ scope (one test file), nothing weakened · 7 ✅ ADRs ·
8 ✅ fmt/vet/lint darwin+linux, targeted tests · 9 ✅ conventions · 10 ✅ reuse · 11 ✅ shape (`test(app):`,
`Fixes #904`, trailer, one commit) · 12 ✅ the one case is covered

## Recommendation

**pass** — hand back to `/fix` Step 8.

## Ledger row

```json
{"date": "2026-10-11", "issue": "904", "phase": "fix", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 0, "model_attributed": 0, "dod_passed": 12, "dod_total": 12, "report": "docs/reviews/issue-904-fix-claude-opus-5-5-2.md", "notes": "re-review round 2 pass; round-1 model minor (m2 survivor on waitSelfHeal's since arm) resolved: TestIssue904 now holds both passes and kills m2 3/3; pre-fix single read (m1) fails 3/3; 30/30 -race; commit retitled test(app) with Fixes #904; test-only, one file"}
```
