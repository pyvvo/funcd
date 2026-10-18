## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1053 fix, round 3, model: claude-opus-5-5)

Change: round 3 of branch `fix/1053-dev-persist-temp-bundle`, `git diff a19a7e18..HEAD`. That is one commit,
bdec7f2b `fix(funcdctl): deliver a --persist session's contracts before its platform starts` (`cmd/funcdctl/dev.go`,
`cmd/funcdctl/dev_phase2_test.go`; +106 −11). It sits on a19a7e18, the round-2 review files, which are unchanged since.
Against origin/main, the whole PR changes the same two code files by +256 −17, and adds the three review reports, their
ledger rows and the scorecard. Earlier rounds: [round 1](issue-1053-fix-claude-opus-5-5.md),
[round 2](issue-1053-fix-claude-opus-5-5-2.md).

Round 3 fixes the round-2 Major and Minor:
- the bundles and contracts are delivered before the platform starts again;
- a kept bundle's dir now carries a digest of its source dir;
- the two link transitions have a test.

Each fix is verified below. What remains is one cosmetic test comment. The probes are scratch test files added to the
package with `go test -overlay`; nothing was written to the worktree.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — the second-session test's comment overstates** · attribution: `model`. The doc comment of
  `TestIssue1053_FailedSecondSessionKeepsTheBundle` (`dev_phase2_test.go:329-330`) says that the refused session fails
  "without touching the bundle of the session that runs". The bundles are delivered before the lock again, so a
  refused session of the same project rewrites that bundle in place. Probe P7 (session 1 serves; the same project
  starts again over the same root):

  ```
  session 2 refused: true; bundle entry still there: true; its mtime changed: true; session 1 after: 200 {"n":1}
  ```

  It writes the same bytes and leaves the bundle there, so the assertions hold. Only the comment is wrong. Say "rewrites
  but never removes". By reading, a worker of the running session that starts during that write could read a partial
  file, and a write to a temp file followed by a rename would close even that gap.

### Round-2 findings — status

- **Major 1 (the contract after a --persist restart): fixed.**
  - `prepareHandlers` runs before `startPlatform` again (`dev.go:581`, then `dev.go:585`), with a comment on why. The
    bootDev doc comment (`dev.go:537-542`) matches that order again.
  - `TestIssue1053_PersistRestartKeepsTheContract` fails with round 2's `dev.go` in 3 of 3 runs (`expected: 422,
    actual: 200`, "boot 2 enforces the contract"), and it passes on HEAD (1.36 s).
  - Round 2's probe P6 now answers 422 on boot 2 for 500, 2000 and 5000 files, and so does P4 for an in-place function
    and an isolated one.
  - The order that round 2's mutant m2 could change unseen is now pinned: going back to round 2's order is exactly what
    fails the contract test.
- **Minor 1 (two untested guards): fixed.** `TestIssue1053_KeptBundleSwitchesBetweenCopyAndLinks` runs `prepareBundle`
  on one kept root, copy → linked → copy. Without the entry-link guard (`dev.go:1653`, mutant m3) it fails
  `expected: false, actual: true` ("linked=false"): the copy wrote through the link. Without the replace in
  `linkSources` (`dev.go:1705`, mutant m4) it fails `link api.mjs into the bundle: symlink … file exists`. Probe P5 (two
  linked functions, then one copied, then a restart) keeps both generations at 2, and the source is untouched.
- **The stale bootDev doc comment: resolved** by the restored order.

### New in round 3 — verified

- **A per-project kept bundle.** `privateBundleDir` (`dev.go:1623-1641`) keeps
  `<persist-to>/bundles/<name>-<first 8 hex digits of the sha256 of the absolute srcDir>`. It follows `devStateDir`'s
  pattern (`dev.go:1035-1036`). `TestIssue1053_KeptBundleDirIsPerProject` pins one path per project across calls and
  different paths for two projects. Mutant m5 (no digest) fails it ("Should not be: …/api") and fails the
  second-session test's glob. By reading, a srcDir whose absolute path changes between runs gets a new dir, and the kept
  old dir still serves the stored Revision until the switch.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix.** With origin/main's `dev.go`, the restart test fails at boot 2 with
  the reported `503 … did not become ready within 30s`. The second-session test and the reload test fail too. The
  contract test passes there, because origin/main also delivers before the platform starts. To make the file compile
  against origin/main's code, the tests and asserts that use the new code were taken out of it.
- **On HEAD under `-race`** every test passes:
  - restart 1.73 s;
  - contract 1.36 s;
  - second session 1.34 s;
  - copy/links 0.00 s and per-project 0.00 s;
  - reload 3.75 s and watch skips 0.00 s;
  - the four `TestResolvePersistPlan*`.
- **Mutants**: m1 (a kept bundle removed again by its cleanup) now fails both the restart test (503) and the
  second-session test. m3, m4 and m5 fail as described above.
- **Every earlier probe passes on HEAD**:
  - P1: `200 {"n":2}` after the restart that sets `main`;
  - P2: session 1's artifact intact, its edit served (`{"n":102}`), no hot-reload warning;
  - P3: generation 1 for 6 s;
  - P4, P5 and P6 as above.
- **Scope**: across the whole PR, the test file only gains lines; no test was removed or weakened. c28df3ac is the
  defect that round 1 found, fixed in its own commit with its own tests.
- **Reuse and conventions**: the digest follows `devStateDir`, the tests reuse `devProject`, `addNote`,
  `requireRuntime` and `poll.Never`, `fault.Wrapf` and `0o700` match the surrounding code, and the comments give the
  why. The audit's soft note `discard-prod` at `dev.go:1705` is benign: if that `Remove` fails, the `Symlink` on the
  next line returns the error.
- **ADRs**: no ADR file changed. ADR-0125's dev-run-function scenario holds after a --persist restart (422 above), and
  ADR-0167's process dir stays out of the hot-reload walk.
- **Checks run here**: `go test -race ./cmd/funcdctl/` ok (6.6 s); `go test -tags dev -race ./cmd/funcdctl/` ok
  (37.0 s); `go vet` ok with and without `-tags dev`; `golangci-lint` 0 issues on `./cmd/funcdctl/...` and with
  `--build-tags dev`; the #950 hygiene test `TestIssue950_NeverChecksDoNotUseTheTest` (`tests/e2e`) ok.
- **The gate on bdec7f2b** (I read its logs and did not rerun it): the audit (diff mode, 6 commits) has 0 hard flags;
  ci-full passed, with the `pkg/funcd` e2e result cached because this change does not touch that package; Linux build,
  vet, lint and tests passed, and the tree was unchanged.
- **Shape**: `fix(funcdctl): …`, `Fixes #1053`, the Co-Authored-By trailer.

### Definition of Done
12 / 12 hold. Minor 1 is a wording fix in a test comment; it does not fail any item.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1053 (fix, round 3) → pass, 0/0/1, 1 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass; hand back to `/fix` Step 8 to open the PR. Reword the second-session test's comment when convenient. The PR body
should give the recovery for state that v0.14.0 wrote (verified in round 2: delete the Function, then restart). It
should also carry the PR comment that the repo's rule asks for when a defect found while fixing is fixed in the same
PR (c28df3ac).
