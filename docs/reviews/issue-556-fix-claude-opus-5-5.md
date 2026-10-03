## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #556 fix, model: claude-opus-5-5)

Change: `056df5a test(funcdctl): share one tryStartDev helper across the dev start tests` (kind/task, test-only refactor of `cmd/funcdctl/dev_test.go`, `dev_phase3_test.go`, `dev_interpreter_test.go`; +40 −81).

### 🟡 Minor
- **One more copy of the start-and-cleanup block remains** · attribution: model · `cmd/funcdctl/dev_phase2_test.go:55-65` still inlines `context.WithCancel` + `startDev(… devConfig{})` + the guarded cancel/stop cleanup over the exact node project map that `nodeHandlerProject()` now provides. It is the same body as `tryStartDev(t, devProject(t, nodeHandlerProject()))`. The issue did not list this site and dupl does not flag it, so it is not a Done-when miss; folding it in is a one-line follow-up. The other direct `startDev` callers in `dev_test.go` / `dev_phase2_test.go` use non-default `devConfig` (persist, s3port) or a restart sequence and are rightly left alone.

### ✅ Verified correct (keep it)
- **Done when, item 1**: `dev_interpreter_test.go` defines no local `start` closure. dupl with `scripts/agent/audit.golangci.yml` (`--enable-only dupl`, build tag `dev`) on a scratch worktree of `origin/main` reports 5 clones (`dev_interpreter_test.go` 37-49, 76-88, 123-135; `dev_test.go` 50-61; `dev_phase3_test.go` 27-38); on the branch it reports none in these files.
- **Done when, item 2**: `runDev` and `startDevPath` are now thin `tryStartDev` + `require.NoError` wrappers; the three interpreter tests call `tryStartDev` directly.
- **Done when, item 3**: `go test -race -tags dev -count=1 -run 'TestIssue(322|430|503)' ./cmd/funcdctl/` → ok; all six subtests PASS, none skipped on this host.
- **No behavior change**: whole package `go test -race -tags dev -count=1 ./cmd/funcdctl/` → ok (16.0s). The only semantic delta is an improvement: the cleanup is now registered before the start error is checked, so a failed `runDev`/`startDevPath` start no longer leaks the context cancel, and the `inst != nil` guard keeps the stop nil-safe.
- **Mutants (overlay, 3/3 killed)**: A — guard `if inst != nil` → `if true`: nil-pointer panic in TestIssue322. B — `tryStartDev` returns `nil` error: TestIssue322/430/503 FAIL. C — `pythonHandlerProject` runtime → `nodejs22`: all three python-handler subtests FAIL. Worktree left clean.
- **Reuse**: the helper lives next to `runDev` in `dev_test.go`, as the issue asked; it reuses `devProject`, `permissiveContract` and the existing `cli.startDev`. The fixtures are functions rather than package-level maps, justified by `gochecknoglobals` and it also prevents shared-map mutation across tests.
- **Conventions**: imports stay at top level (unused `context`/`io` removed); doc comments are short and say why; no YAML touched; build tag `dev` matches across the three files. `go vet -tags dev ./cmd/funcdctl/` clean; `golangci-lint run --build-tags dev ./cmd/funcdctl/...` → 0 issues.
- **Scope / ADRs**: every hunk serves the issue; no assertion weakened or removed (all `require` lines in the three tests are unchanged); no ADR or doc touched.
- **Shape**: `test(funcdctl):` subject fits a kind/task test-only change; `Fixes #556` and the attribution trailer are present; one issue, one commit.

### Definition of Done
9 / 9 applicable items hold (items 1-2 — a new `TestIssue556` regression test failing pre-fix — do not apply to a kind/task refactor; the issue's Done when was verified instead). Host checks only; Linux lint and the repo-wide set run in the group gate.

### Model scorecard
Ledger fields (not recorded here): claude-opus-5-5 on issue #556 (fix) → pass, 0/0/1, 1 model-attributed, DoD 9/9.

### Recommendation
Pass. Optionally fold `dev_phase2_test.go:55-65` onto `tryStartDev` + `nodeHandlerProject` in a later cleanup.
