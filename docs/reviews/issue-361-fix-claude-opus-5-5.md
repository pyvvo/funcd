## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #361 fix, model: claude-opus-5-5)

Change: branch `fix/i361`, commit 661f63a `fix(artifact): stop a read of a missing oci-layout path from creating a layout`
(`internal/artifact/{artifact.go,platform.go,site.go,artifact_test.go}`).

### 🟡 Minor

- **The read-path layout lock is not covered by a test** · attribution: model · evidence: a mutant that drops
  `lockLayout` from `resolveReadTarget` (so `oci.NewFromFS` reads `index.json` without the lock) survives
  `go test -race -count=1 ./internal/artifact/` (`ok`). The lock keeps the pre-fix behavior (reads were opened
  through `withLayout` before), so this gap existed already and is not a regression. Fix (optional, builder): a
  test that holds the layout lock and asserts a read waits for it.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 661f63a` with the new test kept:
  all 16 subtests of `TestIssue361_ReadOfMissingLayoutWritesNothing` fail. Every `/absent` case fails on
  `directory ".../no-such-layout" exists`, and every `/empty` case fails on
  `Should be empty, but was [d blobs/ - index.json - oci-layout]`. That is the layout that the issue reports.
  After `git reset --hard 661f63a`, the test passes under `-race` (16 subtests plus the parent), and the worktree is clean.
- **User-visible behavior.** A freshly built `funcdctl inspect oci-layout://./no-such-layout:v1`, run in an
  empty scratch directory, exits 1 with `artifact.resolveReadTarget: no OCI layout at "./no-such-layout"`. The
  directory stays empty.
- **Cause, not symptom.** The issue names `os.MkdirAll` plus `oci.New` (which writes `oci-layout`/`index.json`)
  on the shared resolver. Read callers now go through `resolveReadTarget`, which uses oras-go's read-only
  `oci.NewFromFS(os.DirFS(dir))` and maps `fs.ErrNotExist` to `fault.NotFound`. Nothing is created. All eight
  read entry points switched: `Pull`, `Inspect`, `InspectContract`, `InspectRuntime`, `Platforms`,
  `OrasMaterializer.Resolve`, `ResolveSite` and `PullSite`. The four remaining `resolveTarget` callers are pushes
  (`Push`, the bundle push, `PushIndex`, `PushSite`). `PushIndex` reads its sources from the target it pushes to,
  so it correctly keeps the write resolver.
- **Mutants.** M1, which disables the `fs.ErrNotExist → NotFound` mapping, fails the test. M2, which routes
  `OrasMaterializer.Resolve` back to `resolveTarget`, fails `Resolve/absent` and `Resolve/empty`. M3, which makes
  `resolveReadTarget` always delegate to `resolveTarget`, fails the test. M4 survives (the Minor above).
- **Scope.** Every hunk serves the issue. No test was weakened or removed. The test covers both an absent
  directory and an empty one across all eight read APIs.
- **Reuse.** The new `lockLayout` is extracted from `withLayout`, which now calls it. The lock code is not
  duplicated. The read-only store is oras-go's own `oci.NewFromFS`, not a hand-rolled reader. No new dependency.
- **Conventions (ADR-0002, CLAUDE.md).** `api/fault` kinds (`NotFoundf`, `Wrapf`), a `const op` per function,
  imports at the top level (`io/fs` added there), and doc comments that explain why (the oras-go write behavior,
  the lock reason) without narration. The naming follows `resolveTarget`/`withLayout`.
- **ADRs.** ADR-0031 (Implemented) maps a local layout to the oras-go OCI store. A read-only oras-go OCI store
  for reads is consistent with that mapping. No ADR file is touched, and the #97 lock discipline is kept on both
  paths.
- **Checks (touched package).** `go test -race -count=1 ./internal/artifact/` is ok. `go vet ./internal/artifact/`
  is clean. `golangci-lint run ./internal/artifact/` reports 0 issues. The repo-wide gate, the Linux lint and the
  e2e tests are left to the group gate.
- **Shape.** The subject has the `fix(artifact):` form. The body has `Fixes #361`, the Co-Authored-By trailer,
  and one issue in one commit.

### Recommendation

Pass. The Minor is optional follow-up test coverage and does not block the merge.
