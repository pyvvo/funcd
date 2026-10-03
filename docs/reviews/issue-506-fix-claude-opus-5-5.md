## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #506 fix, model: claude-opus-5-5)

Change: `fix/i506`, one commit `bf28964 fix(testkit): share the real Node shim test setup between the function and artifact tests`.
The two copies of the real Node shim setup (`seamFunction` + `reconcileUntilSettled` in
`internal/artifact/seam_test.go`, `bringUpRealShim` in `internal/function/shim_test.go`) are replaced by one
helper, `realshim.Ready(t, mat, image, digest)` in the new `internal/testkit/realshim` package. Both callers now
only write the handler and pass their materializer and image reference, which is the issue's "Done when".

### 🟡 Minor
- **The regression test does not pin the digest pass-through** · attribution: model · evidence: mutant 2
  (`fn.Spec.ImageDigest = image, ""` in `realshim.go`) leaves `TestIssue506_OneSetupRunsTheCallersMaterializerAndImage`
  green; it is caught only by `TestScenarioMaterializerSatisfiesADR0030SeamNode` and `TestIssue393_SeamWaitsOutASlowShimBoot`
  in `internal/artifact`. The helper's contract is still fully guarded across the touched packages, so this is not a
  gap in coverage, only in the issue test's own reach. Optional fix: have the recording materializer also record
  `fn.Spec.ImageDigest` and pass a non-empty digest.

### ✅ Verified correct (keep it)
- **Revert check.** `git revert --no-commit bf28964` with the issue test copied back in: the test fails to build
  (`internal/testkit/realshim: no non-test Go files`), i.e. there is no shared setup to call — the issue's reason.
  For a duplication defect a build failure is the only possible failure mode; accepted. Reset to the starting HEAD,
  worktree clean.
- **With the fix, under `-race`:** `TestIssue506_…` PASS (real shim, not skipped — node on PATH), `TestScenarioMaterializerSatisfiesADR0030SeamNode`
  PASS, `TestIssue393_SeamWaitsOutASlowShimBoot` PASS (6.1 s), `TestScenarioShimEndToEndNode` PASS,
  `TestIssue452_RealShimWaitsOutASlowBoot` PASS.
- **Mutants** on `internal/testkit/realshim/realshim.go` (each restored after):
  1. ignore the caller's materializer (`Materializer: function.NewFileMaterializer()`) → `TestIssue506_…` FAIL
     (recorded images empty) and the seam test FAIL. Killed.
  2. drop the digest → seam tests FAIL. Killed (see Minor).
  3. stop waiting (`if …; true {`) → `TestIssue393_…` and `TestIssue452_…` FAIL (phase Deploying, not Ready). Killed.
- **Cause, not symptom.** The cause named in the issue (the setup copied into two `_test` packages) is removed:
  both bodies are deleted and replaced by calls to the one helper. The no-deadline wait from #393/#452 is kept,
  once, in the helper.
- **Scope.** Four files, every hunk serves the issue. No test was weakened or deleted: both callers still require
  Ready (now inside the helper, with the same message), and the #393 and #452 regression tests still run the slow boot.
- **Reuse.** No other copy exists: the other `ShimCommand:` sites in `internal/function` tests use fake runtimes
  (`newFakeRuntime`, `newShimHarness`, `fakeMat`), not the real shim. The helper reuses `langmod.NodeShim`,
  `function.NewFileMaterializer` and the existing ports; the test embeds `function.FileMaterializer` rather than
  re-implementing file resolution. Placing it under `internal/testkit` follows the `langmod` precedent (a non-test
  testkit package that takes `testing.TB` and uses testify).
- **Conventions.** Ctx-first, no `any`, top-level imports, comments state the why (ADR-0030 §4b boot bound, #393/#452);
  `realshim` is imported only by `_test.go` files, so it never reaches a production binary; the import direction
  (testkit → function, consumed from `function_test`/`artifact_test`) creates no cycle.
- **ADRs.** No ADR file touched; ADR-0030 node-lane behavior (skip when node is absent, boot bounded by the reconciler)
  preserved. No living doc lists the testkit packages, so none needs updating.
- **Checks (touched packages).** `go vet` clean; `golangci-lint run` on `internal/testkit/realshim/...`,
  `internal/artifact/...`, `internal/function/...` → `0 issues.`; `go test -race -count=1` on the three packages → all `ok`.
  Repo-wide gate, Linux lint and e2e left to the group gate.
- **Shape.** `fix(testkit):` subject, `Fixes #506`, the attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist; item 8 covers the touched packages, the repo-wide and Linux runs belong to the group gate). Misses: none.

### Model scorecard
To record: claude-opus-5-5 on issue #506 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. The Minor is optional polish for the issue test; it does not block the group PR.
