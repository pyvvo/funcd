## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #362 fix, model: claude-opus-5-5)

Change: `fix/i362`, commit 91ccf48 `fix(artifact): pack the target directory when a site or bundle root is a symlink`
(`internal/artifact/bundle.go` +9/-4, `internal/artifact/site_test.go` +42).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Test locals shadow the predeclared `real`** · attribution: model · `internal/artifact/site_test.go`
  (`real := siteDir(t)`, `real, entry := goodBundle(t)`). Lint does not flag it and it is harmless, but a
  name such as `target` reads better and avoids the shadow. Optional.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 91ccf48` with
  the test file kept: `TestIssue362_SymlinkedRootPacksTarget/site` fails at the pulled `index.html` read
  ("the pulled site holds the files the link points to": the pushed layer was empty), and `/bundle` fails
  with the misleading entry error from `PackBundle`. Worktree then reset to 91ccf48, clean.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/artifact/` → `ok` (13.5 s).
- **User-visible behavior fixed**: the issue's own steps with a freshly built `funcdctl`
  (`mkdir real`, `ln -s real dist`, `funcdctl push --site dist oci-layout://./layout:site`) push a digest
  whose gzip layer lists `index.html`; before the fix it was empty.
- **Cause, not symptom**: the issue names `filepath.WalkDir` not descending into a symlinked root
  (`bundle.go` walk). `packDir` now resolves the root once with `filepath.EvalSymlinks` and walks, relativizes
  and reads under the resolved root. Both callers (`PushSite`, `PackBundle`) go through `packDir`, so the
  site and bundle paths are fixed at the single shared point. Symlinks *inside* the tree are still skipped,
  so the #155 entry gate and the no-symlink-packing rule are unchanged.
- **Mutants** (run with `-run 'TestIssue362|TestScenario'`, restored after each):
  1. `root := dir` (no `EvalSymlinks`) → `TestIssue362_SymlinkedRootPacksTarget` FAILS.
  2. `filepath.Rel(dir, p)` instead of `root` → many `TestScenario…` FAIL (the temp dir's own symlinked
     ancestor makes the resolved and literal paths differ).
  3. `os.ReadFile(filepath.Join(dir, …))` instead of `root` → survives; an equivalent mutant (reading through
     the link reaches the same file), not a test gap.
- **Scope**: two hunks, both for the issue; no test weakened or deleted; the existing
  `TestScenarioPackBundleStillGatesEntry` still passes.
- **Reuse**: uses the standard library's `filepath.EvalSymlinks`; no new helper, type or dependency, and
  no existing helper in `internal/artifact` or `internal/platform` resolves a root path. The test reuses
  the package's `siteDir`, `goodBundle` and `layoutRef` fixtures.
- **Conventions**: the error is `fault.Wrapf(…, fault.Internal, op, …)` like its neighbours (a dangling
  link is already refused as `Invalid` by the callers' `os.Stat`, so this path is only reached on a race);
  the doc-comment addition states the why in one clause; no YAML, no in-function imports.
- **ADRs**: consistent with ADR-0089 (deterministic packer: the symlinked root packs byte-identical to its
  target, which the test asserts for both the site digest and the bundle bytes) and ADR-0139 (site
  artifact); no ADR file touched.
- **Checks (touched package)**: `go vet ./internal/artifact/` clean; `golangci-lint run ./internal/artifact/...`
  → `0 issues.`
- **Shape**: `fix(artifact):` subject, body names the cause and the test, `Fixes #362`, attribution trailer,
  one issue in one commit.

Observation (not scored): a site directory whose only entries are symlinks still passes `PushSite`'s
non-empty check and packs an empty layer. The issue references that as a separate issue, so it is out of
this fix's scope.

### Definition of Done
11 / 11 applicable items hold. Item 8 covers the touched package (build, vet, host lint, `-race` tests); the
repo-wide set, Linux lint and e2e are run by the group gate, not here.

### Model scorecard
To record: claude-opus-5-5 on issue #362 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship as is. The `real` rename is optional polish for `/fix` if the group is reworked anyway.
