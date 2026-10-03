## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #557 fix, model: claude-opus-5-5)

Change: `9f8b165 test(workflow): share reconcileRun and seedReplay helpers in the run tests` (kind/task, test-only refactor of `internal/workflow/reconcile_run_test.go`; +48 −88).

### 🟡 Minor
- **Other inline reconcile-then-reread blocks remain in the file** · attribution: issue · `internal/workflow/reconcile_run_test.go` still holds about a dozen inline `rr.Reconcile(ctx, controller.Request{…})` + `t.Fatalf` + `s.Get(…WorkflowRun…)` sequences (for example `:124-127`, `:179-183`, `:212-215`, `:449-452`, `:567-570`, `:607-610`) that `reconcileRun` now covers. The issue scoped the work to the five closures and three replay literals, dupl does not flag the remaining sites, and folding them in here would widen the diff beyond the issue, so this is a follow-up rather than a Done-when miss.

### ✅ Verified correct (keep it)
- **Done when, item 1**: `grep 'reconcile := func'` finds nothing in the file; the only `ReplaySeed{` left is inside `seedReplay` itself (`:41`). The five closures (issues #122, #123, #176, #346 x2) and the three replay literals are gone.
- **Done when, item 2**: `golangci-lint run -c scripts/agent/audit.golangci.yml --enable-only dupl ./internal/workflow/...` reports no clone in `reconcile_run_test.go` (the 2 reported issues are in other files). `seedRun` and `seedReplay` share `createRun`, so the two seeders are not clones of each other.
- **Done when, item 3**: `go test -race -count=1 ./internal/workflow/` → ok (9.4s). No test function removed; every assertion in the five touched tests is unchanged.
- **No behavior change**: `reconcileRun` keeps the closures' exact sequence (Reconcile in `default` with the WorkflowRun GVK via the existing `runReq`, Fatalf on error, re-read). The sweep test still builds a fresh `at(base)` reconciler per call, as its closure did. The only delta is diagnostic text: the #176 closure's Fatalf wording "(a requeue), want a terminal status" became the shared "Reconcile %s: %v", and the seed Fatalf now names the run.
- **Mutants (overlay, 3/3 killed, `-run 'TestIssue122|TestIssue123|TestIssue176|TestIssue346'`)**: A — `seedReplay` drops the `Replay` seed: #176 and both #346 tests FAIL. B — `seedReplay` seeds source `"x"` instead of `source`: #176 and #346 replay test FAIL. C — `reconcileRun` skips the Reconcile call: all five tests FAIL. Worktree left clean.
- **Reuse**: the new helper reuses the package's existing `runReq` (`run_root_span_test.go:38`) instead of rebuilding the request; the helpers sit beside `seedWorkflow`/`seedRun` as the issue asked. No new dependency.
- **Conventions**: `go vet ./internal/workflow/` clean; `golangci-lint run ./internal/workflow/...` → 0 issues. Imports unchanged at top level; the two doc comments are one-liners on non-obvious defaults ("wf", "default"); no YAML touched. `t` before `ctx` in the helper signature follows the Go test-helper idiom and passes the linter.
- **Scope / ADRs**: one file, every hunk serves the issue; no ADR or doc touched.
- **Shape**: `test(workflow):` subject fits a kind/task test-only change; the body restates the Done-when; `Fixes #557` and the attribution trailer are present; one issue, one commit.

### Definition of Done
9 / 9 applicable items hold (items 1-2 — a new `TestIssue557` regression test failing pre-fix — do not apply to a kind/task refactor; the issue's Done when was verified instead). Host checks only; Linux lint and the repo-wide set run in the group gate.

### Model scorecard
Ledger fields (not recorded here): claude-opus-5-5 on issue #557 (fix) → pass, 0/0/1, 0 model-attributed, DoD 9/9.

### Recommendation
Pass. Optionally move the remaining inline reconcile-then-reread blocks in the file onto `reconcileRun` in a later cleanup.
