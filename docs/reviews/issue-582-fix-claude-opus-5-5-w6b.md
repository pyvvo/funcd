## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #582 fix, model: claude-opus-5-5)

Change: branch `fix/w6b-i582`, commit 47f0103 `fix(artifact): resolve a cached single-file bundle to its handler, never a directory`
(`internal/artifact/artifact.go` +4/-3, `internal/artifact/artifact_test.go` +29).

Decision under review: fix the root cause in the cache-hit fallback of `OrasMaterializer.Materialize` so it returns
the bundle's single regular file, never a directory, with a regression test that fails on the unfixed code on macOS
too (the test creates `__pycache__/` itself). The change matches that decision.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The fall-through comment is now slightly stale** · attribution: model · `internal/artifact/artifact.go:590`
  still reads "Only dotfiles cached (no handler) — fall through to Pull". After the fix the loop also falls through
  when the cache holds only directories (for example `__pycache__/` with the handler gone), so "no regular file
  cached" is the accurate wording. Cosmetic; behavior is correct (Pull re-materializes). Optional one-word touch-up.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** An overlay of the `origin/main` version of
  `internal/artifact/artifact.go` (`go test -overlay`), test file kept:
  `TestIssue582_CachedSingleFileResolvesHandlerNotPycache` FAILS with `expected: …/handler.py`,
  `actual: …/__pycache__` — exactly the cache-hit path returning the directory. It reproduces on macOS (darwin-arm64
  cache key), as the decision required, because the test creates `__pycache__/` and a `.pyc` inside it rather than
  relying on Python.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/artifact/` → `ok` (16.0 s);
  `-race -count=5 -run TestIssue582_` → 5/5 PASS, not skipped.
- **Mutants (2, both killed).** `&& e.Type().IsDir()` (inverted) → FAIL (returns `__pycache__`);
  `|| e.Type().IsRegular()` (wrong operator) → FAIL (returns `__pycache__`). The revert overlay above is the third
  (condition removed) → FAIL.
- **Cause, not symptom.** The defect is the fallback accepting any non-dotfile entry; the fix adds the
  regular-file condition the comment always promised. No retry, timeout, skip or swallowed error. The first
  `Materialize` (miss) still returns `handler.py`; the multi-file path (`bundleEntryFromCache`, ADR-0089) and the
  dotfile skip for `.funcd-entry` / `.funcd-contract.json` (ADR-0123) are untouched.
- **Scope.** Two hunks: the one-condition fix plus a one-sentence why-comment, and the regression test. No test
  weakened or deleted.
- **Reuse.** `fs.DirEntry.Type().IsRegular()` is the standard library, matching the `Mode().IsRegular()` idiom in
  `internal/artifact/bundle.go`. The test reuses the package's `layoutRef`, `mkFunction` and `artifact.Push`
  helpers; no new helper or harness. No other resolver in the repo uses the same dotfile-skip fallback.
- **Conventions.** ADR-0002 holds (no new signatures, `fault` errors unchanged); the added comment states the why
  (Python writes `__pycache__/` on import), not the what; test name follows `TestIssue<N>_…`; `t.Parallel()`,
  `require`, file modes 0o600/0o750 as elsewhere in the file.
- **ADRs.** No ADR file touched; ADR-0030 (materializer seam), ADR-0089 (entry sidecar), ADR-0123 (contract
  sidecar) and ADR-0145 (platform-keyed cache) remain satisfied.
- **Checks (touched package only).** `go vet ./internal/artifact/` → clean; `golangci-lint run ./internal/artifact/...`
  → `0 issues.` Tree clean after the review. Repo-wide gate, Linux lint and the e2e reproduction from the issue
  (`TestIssue183_|TestIssue81_PooledPython` on Linux) are left to the group gate and CI.
- **Shape.** `fix(artifact):` subject, body names the cause and the test, `Fixes #582`, attribution trailer, one
  issue in one commit.

### Checklist (Definition of Done)

10 of 10 applicable items hold (1 regression test, 2 fails pre-fix for the reason, 3 passes under -race, 4 mutants
killed, 5 root cause, 6 scope, 7 ADRs, 9 conventions, 10 reuse, 11 shape). Item 8 (host and Linux lint, e2e, lane)
is deferred to the group gate; its host-side part for the touched package is green.

### Recommendation

Pass. Hand back to `/fix` Step 8. Optionally reword the fall-through comment to "no regular file cached".
