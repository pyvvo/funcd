## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #503 fix, model: claude-opus-5-5)

Change: branch `fix/i503`, commit 9ba385c `fix(funcdctl): fail dev startup when a node handler finds no node`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_interpreter_test.go`, `cmd/funcdctl/dev_phase2_test.go`).

### 🟡 Major / Minor

- **Minor — the python-family test is spelled out once more** · attribution: model · `cmd/funcdctl/dev.go:579`
  adds `isPython := func(pf plannedFunc) bool { return strings.HasPrefix(string(pf.m.Runtime), "python") }`;
  the same prefix test already appears at `cmd/funcdctl/dev.go:1680` and in `defaultEntry`
  (`cmd/funcdctl/dev.go:1825`). The reconciler's `isPythonFamily` (`internal/function/function.go:1621`) is
  unexported in another package, so it cannot be reused directly. The change only names an expression that
  was already inline, so this is cosmetic; a single file-local `isPythonRuntime(v1.RuntimeName)` helper would
  remove the repetition. Not blocking.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 9ba385c`, test file restored,
  `go test -tags dev -run TestIssue503 ./cmd/funcdctl/` →
  `--- FAIL: TestIssue503_MissingNodeFailsAtStartup/node-handler` with
  "An error is expected but got nil … a node handler with no node found fails at startup". With no node and
  a python that loads the shim, startup succeeded, which is what the issue describes. The python-handler
  subtest passed on the old code, as expected. The worktree was then reset to 9ba385c and is clean.
- **Passes with the fix under `-race`.** `go test -tags dev -race -run 'TestIssue503|TestIssue430'` → both
  subtests of TestIssue503 and of TestIssue430 PASS, none skipped.
- **Mutants — 3/3 killed.**
  1. `if node == "" && needNode` → `if node == ""`: the python-handler subtest fails (a missing node would
     block a python run).
  2. `needNode` predicate → `return false`: the node-handler subtest fails.
  3. `fault.NotFoundf` → `fault.Invalidf` for the node error: the node-handler subtest fails on the kind check.
- **Cause, not symptom.** The issue's root cause is that no check asked whether a handler needs node, so
  python became the default shim and a node handler was routed to it (`shimFor`,
  `internal/function/function.go:1541-1546`). The fix computes `needNode` from the planned functions
  (any runtime outside the python family runs on the default shim, which matches `shimFor`) and fails
  startup with a `NotFound` error that names `FUNCD_NODE` and `dev.node`. This mirrors the #430 python
  check exactly. Nothing is masked: no retry, no swallowed error.
- **Scope.** Every hunk serves the issue. `requireRuntime` (`cmd/funcdctl/dev_phase2_test.go:37`) now asks
  for the node handler that its gated lanes declare, so those lanes skip instead of booting into the
  now-fatal state; the skip predicate is still `fault.NotFound`. No test was weakened or deleted.
- **ADRs.** No ADR file was touched. The change is consistent with ADR-0125 (funcdctl dev) and ADR-0049
  (shim selection): the python fallback to default shim for a python-only run is kept. The broader
  fallback question for runtimes outside every family stays with pyvvo/funcd#457 (needs-adr).
- **Conventions.** `api/fault` errors with the caller's `op`, ctx-first signature, no new dependency,
  top-level imports, no comment bloat (the doc comment on `devShimOptions` gained one clause), test placed
  beside the #430 test with the same harness (`devProject`, `startDev`, `permissiveContract`).
- **Reuse.** The test reuses the existing #430 harness; the check reuses the existing interpreter
  resolution and `fault.NotFoundf`. No new helper, type or dependency.
- **Checks (touched package).** `go test -tags dev -race ./cmd/funcdctl/` → ok (16.5s);
  `go vet -tags dev ./cmd/funcdctl/` → clean; `golangci-lint run --build-tags dev ./cmd/funcdctl/...` →
  0 issues; the same lint without the tag → 0 issues; `go build ./cmd/funcdctl/` → ok. The repo-wide set,
  Linux lint and e2e are left to the group gate.
- **Shape.** Subject `fix(funcdctl): …`, body explains cause and fix, `Fixes #503`, attribution trailer,
  one issue in one commit.

### Definition of Done

11 / 11 items hold. Item 8 holds for the host checks of the touched package; Linux lint, the repo-wide
tests and e2e are run by the group gate, not this review.

### Model scorecard

To record: claude-opus-5-5 on issue #503 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation

Ship as is. The Minor (one shared python-family helper in `cmd/funcdctl/dev.go`) can be folded in later
or left; it does not block.
