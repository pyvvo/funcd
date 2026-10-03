## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #550 fix, model: claude-opus-5-5)

Change: branch `fix/w8-i550`, commit 0eb440e `refactor(workflow): bundle the engine's per-run state into one struct`
(`internal/workflow/engine.go`, `internal/workflow/state.go`). Issue #550 is a `kind/task`: its "Done when" is the
target. The decided shape is a refactor with no behavior change, with the five per-run values in one run struct and
the workflow tests unchanged. The commit adds no `TestIssue550` test, which is right for a pure refactor, so the
revert check does not apply. Mutants and the unchanged suite carry the evidence.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observation (not a finding): `evalWhen`, `runBuiltin`, `evalWait`, `evalPass` and `evalSelect`
(`internal/workflow/condition.go`) still take `rec`, `input` and an outputs map as separate parameters. These
methods are not the five-value tuple. `runBuiltin` and the `eval*` helpers receive the step's *parent* outputs, not
the run's outputs, so passing `activeRun` to them would be wrong. "Done when" item 1 holds as written.

### ✅ Verified correct (keep it)
- **"Done when" 1:** `failAtStart`, `drive`, `recordFailed`, `startReady`, `runStep`, `settle`, `selectRunnable`,
  `dispatchStep`, `fail`, `dropUnrecordedOutputs` and `persist` each take one `*activeRun`. No engine method takes the
  five values as separate parameters. `startReady` went from ten parameters to five.
- **"Done when" 2:** the tests pass unchanged. `git diff origin/main...HEAD -- '*_test.go'` is empty, and
  `go test -race -count=1 ./internal/workflow/...` passes: `ok internal/workflow 9.4s`, `ok runstate/badger 1.4s`.
  The workflow e2e tests run in the group gate, not here.
- **"Done when" 3:** function lengths did not grow. `dispatchStep` is 88 lines on both `origin/main` and the branch,
  and `startReady` is 35 lines on both. The hunks only rename variables, so the statement counts are unchanged.
- **No behavior change** (I read every hunk):
  - The over-cap input path sets `rec.Input, run.input = nil, nil`. This matches the old call, which passed `nil`
    input to `failAtStart` and therefore to `fail`.
  - The `RunRecordTooLarge` retry in `failAtStart` still clears only `rec.Input` and still hands the original input
    to `fail`, as before.
  - `Resume` and `replay` build `activeRun` with the same values they passed before (`rec.Input`, `src.Input`).
  - In `selectRunnable`, the named return changed from `run` to `runnable`. This is needed to avoid shadowing the
    new `run` parameter, and it changes no logic.
- **Mutants**, each run as a `go test -overlay` with only the package's tests, and each killed:
  - m1: drop `run.input = nil` on the over-cap path. Killed by `TestIssue181_RunStartGateCapsInput`.
  - m2: stop recording a step's output in `settle` (`run.outputs[r.n.name] = r.out`). Killed by
    `TestWaitBlocksThenContinues`, `TestPassTransformsInEngine`, `TestPassSelectsParentOutput`,
    `TestIssue495_BuiltinBindsSchemaDefault` and `TestJoinAnyExclusiveBranch`.
  - m3: pass `nil` instead of `run.input` to `evalWhen` in `selectRunnable`. Killed by
    `TestIssue494_WhenOnDefaultedInputFieldBindsDefault`.
- **Concurrency invariant:** the `activeRun` doc names what `rs.mu` guards (`rec`, the step nodes and `outputs`; `spec`
  and `input` are read-only). The `runState.mu` comment now points to it. The lock sites are unchanged, and `-race`
  is clean.
- **Scope:** both files serve the issue, and no unrelated hunk is present.
- **Reuse:** no existing struct bundles these values, and the name `activeRun` collides with nothing else in the
  module. The new type reinvents no helper.
- **Conventions:** the type is unexported, and its doc comment is two sentences. There is no comment narration, and
  `ctx` stays the first parameter. `go vet` is clean, and `golangci-lint` on `./internal/workflow/...` reports 0
  issues. `go build ./...` passes.
- **ADRs:** ADR-0094 (write-ahead persist before each attempt, run-start payload cap) and ADR-0100/0105/0107 stamping
  are unchanged. No ADR file was touched.
- **Shape:** `refactor(workflow):` is the right type for a task, the commit carries `Fixes #550` and the attribution
  trailer, and the commit covers one issue.
- **New defects:** none found next to the change.

### Recommendation
Pass. The change is ready for the group integrator and gate.
