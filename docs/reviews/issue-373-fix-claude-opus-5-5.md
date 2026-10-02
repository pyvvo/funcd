## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #373 fix, model: claude-opus-5-5)

Change: `daf8e83 fix(provider): remove stopped engine instances on teardown and recreate` (`internal/provider/runtime.go`, `internal/provider/runtime_test.go`).

### 🟡 Minor
- **Stale `Runtime` interface doc** · attribution: model · `internal/provider/runtime.go:20-21` still says "Teardown stops the engine (the driver owns netns cleanup) and removes any programmed route"; the method's own comment was updated to "stops and removes", the interface's was not. Fix: say "stops and removes the engine" there too. Cosmetic, does not block.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit daf8e83` with the HEAD test file restored, `go test -race -run TestIssue373 ./internal/provider/` → FAIL. `teardown`: "Should be empty, but was [{default/lake/r0 … stopped …}]" (the stopped instance is still listed). `recreate`: calls `[create create]`, want `[create remove create]`.
- **Passes with the fix**: after `git reset --hard daf8e83`, `go test -race -count=1 ./internal/provider/` → ok. The worktree is left at `daf8e83`, clean.
- **User-visible behavior**: the regression test is the issue's own probe (#106 part b): the real process driver, Teardown, then List. After the fix the driver lists no instance and the engine's log file is gone.
- **Cause, not symptom**: both sites the issue names now call `Remove` after `Stop` through one `retire` helper: Teardown (`runtime.go:169-172`) and the recreate path in Converge (`runtime.go:108-112`). There is no retry, timeout, or swallowed error. A `Remove` failure is wrapped with its fault kind and returned. Stop releases an instance even when it exited on its own (`internal/runtime/process/process.go` sets `released` on both Stop paths), so `Remove` after `Stop` meets the ADR-0143 contract (`internal/runtime/runtime.go`: "An instance Stop has not released … is fault.Conflict").
- **Mutants (3/3 killed)**: (1) `retire` skips `Remove` → TestIssue373 FAIL; (2) Teardown calls `Stop` instead of `retire` → FAIL; (3) the recreate path calls `Stop` instead of `retire` → FAIL. Each mutant was restored with `git checkout`.
- **Scope**: every hunk serves the issue: the `retire` helper, its two call sites, a doc touch-up on `Deps.Runtime`, and the new test. No test was weakened or deleted. The existing `fakeRuntime.Remove` no-op was already in place.
- **Reuse**: `retire` follows the existing `function.retire` (`internal/function/function.go:1037-1047`): the same Stop-then-Remove steps, the same fault wrapping and the same ADR-0143 citation. The two live in separate packages, are each a few lines, and work over different values (an `Instance` and an id), so sharing one helper would mean adding a new `internal/runtime` API. Mirroring the precedent is the right call. The test reuses `engineServer`/`specFor` and the real `process.New()` driver. It wraps the port by embedding it instead of adding another hand-written fake.
- **Conventions**: errors use `api/fault` (`Wrapf` + `KindOf`), calls take ctx first, there is no `any` in a signature, imports are at the top level, comments are not bloated, and the names match the surrounding code (`rerr`, `op` consts).
- **ADRs**: conforms to ADR-0087 (Teardown still removes the route and is idempotent; `Remove` of an unknown instance is a no-op per ADR-0143) and ADR-0143 (Remove after Stop). No ADR file was touched.
- **Checks (touched package)**: `go test -race -count=1 ./internal/provider/` ok; `go vet ./internal/provider/` clean; `golangci-lint run ./internal/provider/` → 0 issues. The repo-wide, Linux-lint and e2e checks run at the group gate.
- **Shape**: the subject is `fix(provider): …`, the body gives cause, fix and test, it carries `Fixes #373` and the attribution trailer, and the commit covers one issue.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 holds for the touched package (race tests, vet, host lint). The repo-wide checks, Linux lint and e2e are left to the group gate.

### Model scorecard
To record: claude-opus-5-5 on issue #373 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. Fixing the one-line interface doc is optional and can be folded into the group PR. Hand back to `/fix` Step 8.
