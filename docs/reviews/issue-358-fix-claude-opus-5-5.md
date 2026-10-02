## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #358 fix, model: claude-opus-5-5)

Change: branch `fix/i358`, commit 444ebc0 `fix(function): keep a Function out of Ready when its local API socket cannot be provisioned` (`git diff origin/main...HEAD`: 7 files, +124/-42; one production file, `internal/function/function.go`).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 444ebc0`, keeping the HEAD `internal/function/supervision_test.go` (the internal test files revert with the old `workerSpec` signature, so the package compiles), then `go test -run TestIssue358 ./internal/function/`: both subtests fail with `expected: "Failed"` / `actual: "Ready"` — the Function goes Ready without its local API, exactly the issue's report. `FAIL github.com/pyvvo/funcd/internal/function`.
- **It passes with the fix under `-race`.** After `git reset --hard 444ebc0`: `go test -race -count=1 -run TestIssue358 ./internal/function/` → `ok`. The worktree was left at 444ebc0 and clean.
- **The test covers both runtime modes and the recovery.** `TestIssue358_SocketFailureBlocksReady` runs the process branch and the container branch (`EndpointNetnsFixedPort`), asserts phase `Failed`, `Ready=False` with reason `StartFailed` and the error text in the message, zero `Create` calls, and a requeue at the supervision period; then it clears the fault and asserts `Ready` with `FUNCD_INVOKE_SOCKET` set in the created spec.
- **Mutants — all three killed:**
  - M1, drop the `startErr = serr` record in `convergeRevision` (keep the `continue`): both subtests fail (`actual: "Idle"`).
  - M2, swallow the `SocketFor` error in `invokeSocket` (`return "", nil`): both subtests fail (`actual: "Ready"`).
  - M3, ignore the `invokeSocket` error in the container branch of `workerSpec`: the container subtest fails (`actual: "Ready"`).
- **Cause, not symptom.** The issue names the two log-and-continue sites (`addInvokeSocket` and the container branch of `workerSpec`). Both now return the error; `convergeRevision` records it as the replica's start error and creates no worker. Nothing is retried harder or hidden: the pass still requeues at the supervision period, and the next pass provisions the socket and starts the worker.
- **Reuse.** The fix routes the failure through the existing issue-#73 start-error path (`verdict.startErr` → `StartFailed` conditions in `finish`), not a new status mechanism. The new `invokeSocket` helper removes the duplicated `SocketFor` call that the process and container branches each had. No existing fake implements the `InvokeSockets` port in `internal/function` tests, so `brokenSockets` is not a duplicate; `mustWorkerSpec` replaces the repeated `workerSpec(..., 0, ...)` call in the internal tests after the signature change, and no equivalent helper existed. `scheduler.Schedule` (single-node) holds no reservation, so a replica skipped after scheduling leaks nothing.
- **Scope.** Every hunk serves the issue: the production change in `function.go`, and the internal-test call sites updated for the new `(WorkerSpec, error)` signature without weakening an assertion. No test was deleted or weakened.
- **Conventions (ADR-0002).** The error is wrapped with `fault.Wrapf(err, fault.KindOf(err), op, …)`, the idiom of the surrounding code; logging stays on the reconciler's `slog` logger; imports are at the top level; comments state the why and cite the issues (#73, #358); no YAML touched.
- **ADRs.** ADR-0064 and ADR-0069 say every function gets the local API socket; the fix enforces that (no worker starts without it) and contradicts no Decision or Contract. No ADR file was edited.
- **Checks (touched package).** `go build ./...` ok; `go vet ./internal/function/` ok; `go tool golangci-lint run ./internal/function/...` → `0 issues.`; `go test -race -count=1 ./internal/function/` → `ok`.
- **Shape.** Subject `fix(function): …`, body names the regression test, `Fixes #358`, the attribution trailer, one issue in one commit.

### Definition of Done

11 / 11 items hold. Item 8 holds for the touched package (build, vet, host lint, race tests); Linux lint, the e2e suite and the lanes are left to the group gate, as this review's scope sets. The issue's own reproduction (an unwritable socket dir under a real daemon) was not rerun: the regression test drives the same code path through the reconciler with a failing `SocketFor`, in both runtime modes.

### Model scorecard

Not recorded by this review (the group step records it): claude-opus-5-5 on issue #358 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Ship as is. Hand back to `/fix` Step 8 for the PR.
