## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #106 fix, model: claude-opus-5-5)

Change: branch `fix/i106`, commit `98b1984` — `fix(provider): start a catalog engine left in Created after a failed Start`.
Touched: `internal/provider/runtime.go` (+9/-5), `internal/provider/runtime_test.go` (+36).

### Minor 1 — the issue bundles a second defect that this fix does not address  ·  attribution: issue
The issue's Expected behavior has two parts: (a) Converge should start a `Created` instance, and (b) Converge
should `Remove` a released instance so driver resources are reclaimed ("Actual behavior" (b): one log file
leaked per recreate, the instance never Removed after Teardown). The title and the "Main defect (a)" summary
cover only (a), and the commit fixes only (a). `internal/provider` still never calls `Remove`
(`grep -n Remove internal/provider/*.go` hits only the test double). Because the commit carries `Fixes #106`,
merging it closes the issue and loses (b). The issue breaks the "one defect per issue" rule, so this is not
scored against the model; the `/fix` rule ("a second defect is a new issue: note it in the handoff") applies.
Fix: file (b) as its own issue (provider never Removes released engine instances; related to #46 for the
process driver's log leak) before the PR merges.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `go test -overlay` with the `origin/main` `runtime.go` and
  the new test: `TestIssue106_StartsEngineLeftInCreated` FAILS at `runtime_test.go:369` —
  `"[default/lake/r0]" should have 2 item(s), but has 1` / "the engine left Created is started on the next
  pass". The Created engine was adopted and never re-Started, exactly as reported.
  (`git revert --no-commit 98b1984` removes the test with the fix, giving "no tests to run"; the overlay is the
  meaningful pre-fix run. The worktree was reset to `98b1984` and is clean.)
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/provider/` → `ok`; the test runs
  un-skipped (`--- PASS`).
- **Root cause, not symptom.** The adopt branch only handled missing/terminal replicas; a `Created` replica fell
  through as "not terminal". The new `case inst.State == containerrt.StateCreated` starts it, and the create
  path falls through into the same `Start`, so there is one Start call site. No timeout, retry, or swallowed
  error; a failing Start still returns its wrapped `fault` error.
- **Mutants (3), all caught:**
  1. `case inst.State == StateCreated` → `case false` — FAILS `TestIssue106_…`.
  2. `fallthrough` deleted — FAILS `TestConverge_provider_deploys` and `TestIssue106_…`.
  3. `inst = created` deleted — build fails (`created` unused), so the assignment cannot silently disappear.
- **Scope.** Two files, every hunk serves (a): the switch, the doc comment, a one-shot `startErr` and a
  `Created → Running` transition on `Start` in the package's existing `fakeRuntime`. No test weakened or
  deleted; the existing provider tests pass unchanged with the fake's new Start transition.
- **Reuse.** Mirrors `internal/function/function.go` (`case in.State == runtime.StateCreated: start`), the same
  ADR-0142 rule; extends the existing `fakeRuntime` instead of adding a new double; uses `fault.Unavailablef`
  and the existing `engineServer`/`specFor` helpers. No new helper, type, or dependency.
- **Conventions.** `fault.Wrapf` with `fault.KindOf`, ctx-first, typed `containerrt.State`, no `any`, top-level
  imports, short comments that state the why (ADR-0142). `gofmt -l` clean.
- **ADRs.** Consistent with ADR-0142's table (`Created` (a failed `Start` can leave it) → start) and with
  ADR-0087's supervision = re-convergence. No ADR file touched.
- **Checks (touched package).** `go build ./...` ok; `go vet ./internal/provider/` ok;
  `golangci-lint run ./internal/provider/...` → `0 issues.`; package tests with `-race` ok. Linux lint, e2e and
  the lanes are left to the group gate.
- **Shape.** `fix(provider):` subject, `Fixes #106`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Before the PR merges, file defect (b) (the provider never `Remove`s a released engine instance) as a
separate issue so `Fixes #106` does not close it.
