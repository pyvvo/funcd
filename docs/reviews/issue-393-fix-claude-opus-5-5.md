## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #393 fix, model: claude-opus-5-5)

Change: branch `fix/i393`, commit `dabb3a3` `fix(artifact): let the ADR-0030 seam test wait out a slow shim boot`.
It touches one file, `internal/artifact/seam_test.go`, and changes test code only.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The same fixed 5 s wait remains in a sibling package.** · attribution: `issue` (outside the scope the issue
  names; not scored) · evidence: `internal/function/shim_test.go:613` and `:672` still poll a real shim with
  `deadline := time.Now().Add(5 * time.Second)`. On a loaded runner, these tests can flake in the same way. · fix:
  file a follow-up issue that applies the same "reconcile until the Function leaves Deploying" wait there. It does
  not belong in this commit.

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix wait, for the reason the issue gives.** The fix changes only test
  code, so `git revert --no-commit dabb3a3` also removes `TestIssue393_…` ("no tests to run"). I therefore
  applied an overlay that puts the pre-fix fixed 5 s deadline back into `reconcileUntilSettled` (mutant m0). With
  that overlay, `TestIssue393_SeamWaitsOutASlowShimBoot` fails after 5.08 s with `expected: "Ready" / actual:
  "Deploying"`, which is the CI failure in the issue word for word. After `git reset --hard dabb3a3`, the worktree
  was clean again.
- **The regression test passes with the fix under `-race`.** `go test -race -count=1 -run
  'TestIssue393_|TestScenarioMaterializerSatisfiesADR0030SeamNode' ./internal/artifact/` returned PASS. The
  regression test took 6.17 s and the original seam test took 0.18 s. Neither was skipped (node is on PATH). The
  whole `internal/artifact` package passed with `-race` (`ok`, 19.4 s).
- **The regression test reproduces the slow boot itself.** The handler runs a top-level `await` of 6 s before it
  exports `handle`, so the shim's readiness is delayed past the old deadline. The 6.17 s run time confirms that
  the wait covered the full boot.
- **Mutants: all 3 were killed.** m0 (the pre-fix 5 s deadline) failed after 5.08 s. m1 (return after the first
  reconcile) failed after 0.01 s with "Deploying". m2 (settle on any non-empty phase) failed after 0.01 s with
  "Deploying".
- **The root cause is fixed, not masked.** The issue blames the test's own fixed deadline, and the fix removes
  it. The wait now ends on the reconciler's verdict, and the product still enforces its own bound:
  `internal/function/function.go:683` sets `bootTimeout = time.Minute`, which is the ADR-0030 §4b timeout, and a
  replica past it becomes `Failed`. The loop therefore always ends with a phase it can assert on. It does not
  hang, and it does not swallow a failure. A `Failed` result still fails the test with a clear message. The
  product code is unchanged.
- **Scope is limited to the issue.** Only `seam_test.go` changed. The original seam test keeps all of its
  assertions: Ready, one route, and HTTP 200 from the oras-pulled handler. The setup moved into `seamFunction`
  unchanged, and no assertion was weakened.
- **Reuse:** no shared phase-wait helper exists for an in-package reconciler. `internal/testkit/bench`'s
  `waitPhase` goes through the SDK client and a running daemon, and the other in-package tests inline their loops.
  The new helpers are file-local and are used by both tests, so the change duplicates nothing.
- **Conventions:** imports are at the top level, and the one new import is `internal/gateway`, for the
  `gateway.Gateway` return type. Comments state why the code does what it does (the reconciler's ADR-0030 §4b
  boot bound, issue #393), and none narrates the code. The removed `// pulled by digest` remark now lives in the
  doc comment of `seamFunction`. There is no YAML in the change, and `t.Helper()` is set on both helpers.
- **ADRs:** no ADR file changed. The change conforms to ADR-0030 §4b (readiness, and a timeout leads to `Failed`)
  and to §5 (the L3-runtime tier, gated on `exec.LookPath("node")`).
- **Checks on the touched package:** `go vet ./internal/artifact/` passed. `golangci-lint run
  ./internal/artifact/...` reported 0 issues. The first lint run showed a stale `gochecknoglobals` hit from
  another worktree's path in the shared lint cache, an `env` artifact. A rerun with a fresh cache reported 0
  issues, and `bundle.go` has the `//nolint` directive on that line.
- **Commit shape:** the subject is `fix(artifact): …`, the body has `Fixes #393` and names the regression test,
  the trailer is `Co-Authored-By`, and the commit covers one issue.

### Definition of Done
11 / 11 items that apply hold. Item 8 holds for the host checks of the touched package (build, vet, lint and
tests with `-race`). The Linux lint and the repo-wide gate are left to the group gate, by design. Item 11 holds
for the commit. No PR is open yet, so the PR shape is checked when the PR is opened.

### Model scorecard
Not recorded by this gate run (orchestrated). Ledger fields: issue 393, phase fix, model claude-opus-5-5,
verdict pass, 0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Pass, so the fix can go to the group PR. As a separate issue, file a follow-up for the same fixed 5 s shim waits
in `internal/function/shim_test.go`.
