# Fix review — issue #155 (a bundle push accepts a symlinked entry and leaves the handler out)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #155 fix, model: claude-opus-5-5)

Change: branch `fix/i155`, commit `c6dd581` — `internal/artifact/bundle.go`, `internal/artifact/site.go`,
`internal/artifact/bundle_test.go`. Governing ADRs: ADR-0089 (multi-file bundles), ADR-0139 (site bundles),
ADR-0035 (deterministic digests), ADR-0002 (conventions).

### Verification run

| Check | Result |
|---|---|
| Revert `c6dd581` (keeping the new test) and run `TestIssue155_SymlinkedEntryRefused` | FAIL in both subtests: `expected: "invalid"`, `actual: ""`. `PackBundle` returns success for a symlinked entry and for an entry under a symlinked dir, which is the reported defect. |
| Reset to `c6dd581`, run the test with `-race` | PASS (both subtests) |
| `go test -race ./internal/artifact/` | ok |
| `go build ./...`, `go vet ./internal/artifact/ ./cmd/funcdctl/` | clean |
| `golangci-lint run ./internal/artifact/...` | 0 issues |
| `gofmt -l internal/artifact` | clean |
| Worktree after the review | at `c6dd581`, clean |

Mutants (each run against the whole `internal/artifact` package, then restored):

| Mutant | Result |
|---|---|
| M2: disable the new entry gate in `packDir` (`if entry != ""` → `if false`) | killed: `TestIssue155_…`, `TestScenarioPackBundleStillGatesEntry`, `TestScenarioPackBundleDeterministic` |
| M1: drop `&& n.info.Mode().IsRegular()` from the match | **survived** |
| M3: drop `filepath.Clean` from `want` | **survived** |
| M4: drop the `entry == ""` refusal in `PackBundle` | **survived** |

### 🟡 Minor 1 — the rewritten entry gate's non-symlink cases are untested  ·  attribution: model

The fix replaces the `os.Stat` gate with a check against the walked nodes. Three cases that `os.Stat` +
`IsRegular` used to cover are now held only by new code that no test exercises:
- an entry that names a directory (M1 survives — only the `IsRegular()` conjunct refuses it);
- an entry written as `./handler.py` (M3 survives — only `filepath.Clean` keeps that form working);
- an empty entry (M4 survives — only the new `entry == ""` refusal in `PackBundle` stops it from silently
  packing like a site bundle).

The behavior is correct today; a future edit to any of these lines would pass CI. Add table cases to
`TestIssue155_…` or a neighbouring scenario: a directory entry and `""` → `fault.Invalid`, and `./<entry>`
→ success.

### ✅ Verified correct (keep it)

- **Root cause, not symptom.** The issue names the mismatch between the `os.Stat` gate (follows symlinks)
  and the lstat-semantics walk (skips symlinks). The fix moves the gate into `packDir` and checks it
  against the files the walk actually packs, so the gate and the packed set can no longer disagree. This
  also covers an entry under a symlinked directory and an entry that escapes the dir with `..`; both
  were accepted before.
- **Fails closed with the right kind.** `fault.Invalid` with a message that names the symlink cause; both
  `PackBundle` and `PushBundle` are asserted, which covers both CLI paths (`cmd/funcdctl/cli.go` and
  `cmd/funcdctl/manifest.go` both call `PushBundle`).
- **Scope.** Every hunk serves the issue. `PushSite` passes `""` and keeps its entry-less behavior (ADR-0139).
  No test was weakened or deleted.
- **Reuse.** It adds no helper, type or dependency. `slices.ContainsFunc` comes from the standard library
  and `slices` is already imported. The test reuses `goodBundle` and `layoutRef`.
- **Conventions.** `api/fault` errors, no `any`, no new imports, and comments state why the code does
  what it does without narrating it. The `PackBundle` doc contract ("entry must name an existing regular
  file … else fault.Invalid") still holds.
- **ADRs.** The fix conforms to the ADR-0089 bundle contract ("only regular files and directories are
  packed"), and determinism (ADR-0035) is unchanged. No ADR file was edited.
- **Commit shape.** The subject is `fix(artifact): …`, the body has `Fixes #155` and the attribution
  trailer, and the commit covers one issue.

### Definition of Done — 10 / 11

1 ✅ regression test · 2 ✅ fails pre-fix for the reported reason · 3 ✅ passes under `-race` ·
4 ⚠️ revert and the core-gate mutant fail a test, but 3 of 4 mutants on the gate's supporting lines survive (Minor 1) ·
5 ✅ root cause · 6 ✅ scope · 7 ✅ ADRs · 8 ✅ build/vet/lint/tests for the touched packages (the group gate runs Linux lint and e2e) ·
9 ✅ conventions · 10 ✅ reuse · 11 ✅ commit shape.

### Model scorecard

claude-opus-5-5: 0 blockers, 0 majors, 1 minor (model-attributed: 1).

### Recommendation

Pass. Before the PR, consider folding in the three gate cases from Minor 1 so that the regression test
also covers the lines that took over from `os.Stat`. This is optional.
