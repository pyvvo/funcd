## Verdict: changes-requested — 1 blocker, 0 majors, 0 minors  (ADR-0297 implementation, round 2, model: claude-opus-5-5)

This round covers 15868bbe on `fix/limits-followup`: the folded Minors of round 1 (cce5fad8, 9f418b4c). The checks ran in
a worktree detached at 15868bbe.

### Checks run
- `go test -race -count=1 ./cmd/funcd/`: **FAIL**. `TestIssue1149_TestsThatReopenBadgerRunSerially` names
  `TestStoresOpenBytes`.
- `go test -race` on `internal/eventing/...` and the floor tests of `pkg/funcd`, vet (darwin and Linux) and lint: ok.

### Blocker
- **`TestStoresOpenBytes` breaks the #1149 guard** (`model`). The new test opens Badger directories and calls
  `t.Parallel()` (`cmd/funcd/space_test.go:113`). The guard test fails the package because a test that reopens a Badger
  directory must not run in parallel with the package's forking tests. The fix review ran only filtered `cmd/funcd`
  tests. Drop `t.Parallel()` from the new test.

### ✅ Verified correct (keep)
- `TestScenarioStartBelowFloor` covers the read, the apply and the refused workload write.
- `TestScenarioPlatformWritesContinue` also records a workflow run's state.
- `TestStoresOpenBytes` measures the stores' newest files against `storesOpenBytes`.
- The seen list's part write and the dead-letter sweep use `diskfull.Wrapf`.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0297 (implementation, round 2) → changes-requested, 1/0/0, 1 model-attributed.

### Recommendation
Drop the `t.Parallel()` call and rerun the whole `cmd/funcd` package once.
