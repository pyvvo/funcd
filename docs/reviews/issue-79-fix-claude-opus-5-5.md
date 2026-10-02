## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #79 fix, model: claude-opus-5-5)

Change: branch `fix/i79`, commit `6ba98c7 fix(function): carry the shim's load error in ShapeValid=False`
(`internal/function/function.go`, `internal/function/pool.go`, `internal/function/shim_test.go`,
`internal/function/supervision_internal_test.go`).

The fix makes `readyReplicas` return the failed instance instead of a bool, and the verdict carries the last
non-empty line of that instance's captured stdout+stderr (`runtime.Logs`). `finish` writes that line into
`ShapeValid=False`; the old fixed text stays as the fallback when the log cannot be read. This removes the
cause named in the issue's Root cause (the hard-coded message in `finish`) and meets ADR-0030 §4b ("the
shim's error").

Scope note: the issue body also describes crash output (uncaught exceptions) that never reaches
`funcdctl logs`. That half is tracked separately as #78 ("Raw stdout/stderr (Path A) is never ingested"),
and this issue's title is the ShapeValid half only. So `Fixes #79` is correct and the fix is not incomplete.

### 🟡 Minor

- **The pooled path has no test** · attribution: `model` · evidence: a mutant in `internal/function/pool.go`
  that sets `loadErr: ""` in `convergePooled` survives (`go test ./internal/function/` → `ok`). The commit
  message says the fix covers "a pooled worker", but only the solo first-deploy and the redeploy-beside-serving
  paths are tested. Fix: add a pooled sub-test to `TestIssue79_ShapeValidCarriesShimLoadError`.
- **The lowest-ID rule in `readyReplicas` has no test** · attribution: `model` · evidence: a mutant that
  replaces `if failed == "" || in.ID < failed {` with `if true {` survives. The rule exists so that the
  message does not change with `List`'s order. A changing message rewrites the status on every pass, which is
  the issue #24 class of write loop. Fix: a test with two Failed replicas whose logs differ, which checks that
  the message stays the same across passes.

### ✅ Verified correct (keep it)

- **Revert check**: with `git revert --no-commit 6ba98c7` and the new test file kept, both sub-tests fail
  for the issue's reason:
  `expected: "funcd-shim: shape error: export \"handle\" is not a function"` /
  `actual  : "the runtime shim could not load the handler"`. After `git reset --hard 6ba98c7`,
  `go test -race -run 'TestIssue79_|TestReadyReplicas'` passes (both sub-tests, un-skipped). The worktree was
  left clean at `6ba98c7`.
- **User-visible behavior, real shim**: a scratch overlay probe (not committed) used the real process driver
  and the pinned Node shim against a module with no `handle` export. It reached
  `phase=Failed ShapeValid=False message="funcd-shim: shape error: export \"handle\" is not a function"`,
  which is the issue's expected output.
- **Mutants killed**: (1) `loadError` keeps the first non-empty line instead of the last → the first-deploy
  sub-test fails. (2) `switchSolo` passes `""` to `loadError` → the redeploy sub-test fails.
- **Cause, not symptom**: the message now comes from the shim's own output. Both shims write the load error
  as their final stderr line before they exit: the Node shim (`funcd-shim: shape error: …` followed by
  `process.exit(3)`), and the Python shim and the pools in the same shape. So the last-line rule matches the
  producers. A Failed instance is kept, not replaced, while the generation has been tried and is not serving
  (`convergeRevision` default branch), so its log stays readable on later passes and the message is stable.
- **Both drivers**: `process` and `containerd` both implement `Logs` over the per-instance stdout+stderr file
  (the `runtime.Runtime` port contract, `internal/runtime/runtime.go`). No new port and no new dependency.
- **Scope**: every hunk serves the issue. The change to `supervision_internal_test.go` only adapts the
  assertions to the new return type, and they are now stricter (`require.Equal(t, inst.ID, failed)`). No test
  was weakened or deleted.
- **Reuse**: no existing helper reads the tail of a worker log. The `funclog` scanners decode the fd-3
  record channel, which is a different format. `bufio.Scanner` from the standard library is the right tool,
  and the generic message is reused as the fallback rather than duplicated as a new constant.
- **Conventions**: ctx-first; no `any`; the errors from `Logs` degrade to the documented fallback; imports
  are at the top level; the comments are short and cite ADR-0030 §4b, ADR-0142 and issue #24. Typed
  `runtime.InstanceID` is used instead of a string.
- **ADRs**: the fix conforms to ADR-0030 §4b (the condition carries the shim's error), ADR-0142 (a Failed
  replica in a serving pass is still treated as a crash under repair; `failed = ""`), and ADR-0143 Decision 5
  (ShapeValid describes the current revision during a switch). No ADR file was touched.
- **Checks (touched package)**: `gofmt -l internal/function` is empty; `go build ./...` is OK;
  `go vet ./internal/function/` is OK; `golangci-lint run ./internal/function/` reports `0 issues.`;
  `go test -race ./internal/function/` is `ok`.
- **Commit shape**: `fix(function):` subject, `Fixes #79`, the attribution trailer, one issue in one commit.

### Recommendation

Pass. The two Minors are test gaps on secondary paths. They can be added in a follow-up or folded in before
the PR, and neither blocks the fix.
