## Verdict: pass — 0 blockers, 0 majors, 4 minors  (issue #73 fix, model: claude-opus-5-5)

Change: branch `fix/i73`, commit 2ca443f `fix(function): write a Failed status when a worker cannot start`
(`internal/function/function.go`, `internal/function/supervision_test.go`, `internal/runtime/process/process.go`;
90 insertions, 20 deletions).

The issue: when `runtime.Start` fails (for example, the interpreter is missing), `convergeRevision` returns the
error before `finish()`, the only writer of the status and of `observedGeneration`. A new Function keeps
`status: {}`, its generation stays untried, and each pass replaces the replica at the engine's 1 s error backoff.
Each replacement leaks one temp log on the process driver.

The fix has two parts. First, `convergeRevision` records the first Start error and returns it beside the
fatal error, instead of failing the pass. `finish()` then writes the status: a Function with no running
replica ends `Failed` with Ready and RevisionReady `False/StartFailed` naming the error, and `requeueFor`
retries it once per supervision period. A serving Function stays `Degraded`, and its Ready message names the
error. A revision switch reports RevisionReady `StartFailed`. Second, the process driver no longer marks an
instance `Failed` when its process never ran. The instance stays `Created`, so the next pass starts the same
instance again (ADR-0142 Decision 4: "`Created` (a failed `Start` can leave it) | start") instead of
replacing it. This removes the per-retry `Create` and its temp log.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

1. **The serving and switching branches have no test.** (attribution: `model`) The new `Degraded` message
   ("…its replacement could not start: …") and the `v.switching && v.startErr != nil` RevisionReady branch are
   not exercised. The regression test covers only the new-Function `Failed` path. A mutant that removes either
   branch would survive. The issue's own serving variant (interpreter removed after Ready) is the missing case.
2. **The pooled path still fails before the status write.** (attribution: `model`) `pool.go` `ensurePool` and
   `startPoolInstance` still return a `runtime.Start` error out of Reconcile, so a pooled Function whose pool
   worker cannot start keeps the old behavior (no status, 1 s error backoff). The issue's reproduction is a
   solo Function, so this is not a regression. The commit message should name the gap, or a follow-up issue
   should cover it.
3. **`convergeRevision` returns `(int, time.Time, error, error)`.** (attribution: `model`) Two `error`
   results in one tuple, one fatal and one recorded, are easy to swap at a call site. The three call sites are
   correct today. A small result struct, or naming the results in the signature, would make the split
   explicit. This is a cosmetic point and does not block the change.
4. **The crash-replacement log leak remains.** (attribution: `issue`) The issue also reports that the process
   driver's `Create` overwrites an exited instance without removing its temp log, one file per period for every
   ordinary crash replacement. The fix stops the leak on the start-failure path, which is this issue's path,
   because the instance is no longer re-created. The crash path is a separate defect and needs its own issue
   (one defect per issue).

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 2ca443f`
  and the new test file restored from HEAD, `go test -race -run TestIssue73 ./internal/function/` fails:
  `Received unexpected error: function.converge: start worker: runtime.process.Start: start process: fork/exec
  /nonexistent/bin/node: no such file or directory`. That is the error the issue's daemon logged in place of a
  status.
- **It passes with the fix under `-race`.** After `git reset --hard 2ca443f`:
  `--- PASS: TestIssue73_StartFailureWritesFailedStatus`. The worktree was left at 2ca443f and clean.
- **The test checks the cause, not only the symptom.** It uses the real process driver, wrapped in a
  `createCounter`, with a missing interpreter and two replicas. It asserts phase `Failed`,
  `observedGeneration == generation`, Ready `False/StartFailed` with the interpreter path in the message,
  ShapeValid `True` (a start failure is not a shape failure), and a requeue of one period. A second pass must
  keep `creates == 2` (the instances are started again, not replaced, so no temp log leaks) and must not write
  to the store (the resourceVersion does not change, ADR-0047 quiescence).
- **Mutants: 3 of 3 killed.**
  - Restore `inst.state = runtime.StateFailed` in the process driver's Start error path → fails:
    `expected: "StartFailed", actual: "ShapeInvalid"`.
  - Make the `PhaseFailed` requeue in `requeueFor` unreachable → fails: `expected: 50ms, actual: 0s`.
  - Remove the `v.startErr != nil && v.running == 0` case in `finish` → fails: `expected: "Failed", actual: "Idle"`.
- **The root cause is fixed, not masked.** The error no longer short-circuits `finish()`. No timeout, retry, or
  swallowed error hides the failure: the error is written to the status and logged at WARN once per pass.
- **It conforms to ADR-0142.** The `Created` state after a failed Start is exactly ADR-0142 Decision 4's
  "`Created` (a failed `Start` can leave it) → start" row, and it matches the runtime port's definition of
  `StateFailed` ("the worker process exited abnormally"). containerd already reports a task whose Start failed
  as `Created` (`mapState`), so the two drivers now agree. A `Failed (StartFailed)` Function retried once per
  period matches the blueprint's `Deploying --> Failed : pull / worker / route error` and ADR-0142's
  at-most-once-per-period retry. `boot-failure-stays-failed` (ShapeInvalid, not restarted) is unchanged:
  `requeueFor` returns 0 for `Failed` without a start error.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted, and no ADR file was touched.
- **Reuse.** The existing `fakeRuntime` cannot return a Start error (its `failed`/`failRev` flags mark an
  instance `Failed` after a successful Start), and the driver change must be tested on the real driver. A
  thin embedding wrapper that counts `Create` calls is therefore the minimal harness, and it reuses
  `newShimHarness`, `withPeriod`, and `shapeValid`. `StartFailed` is a new reason that collides with no
  existing name.
- **Conventions.** The change uses slog through `r.logger`, `fault` wrapping is unchanged, the imports are
  at the top level, and the comments are short and state the reason.
- **Checks (touched packages).** `gofmt -l` is clean. `go build ./...` passes. `go vet` passes on
  `internal/function/...` and `internal/runtime/process/...`. `golangci-lint` reports `0 issues.`.
  `go test -race` passes for `internal/function`, `internal/runtime/process`, and the rest of `internal/runtime/...`.
  The e2e suite, Linux lint, and the lanes were left to the group gate, as instructed.
- **Shape.** The subject is `fix(function): …`, the body has `Fixes #73` and the attribution trailer, and the
  commit covers one issue.

### Not run

- The issue's real-daemon reproduction was not rerun. The regression test drives the real reconciler with
  the real process driver and the issue's missing interpreter, which covers the same path.

### Recommendation

Pass. Before the PR, consider a test for the serving `Degraded` message (Minor 1), and name the pooled gap in
the PR description or file a follow-up (Minor 2). File the crash-replacement log leak as its own issue
(Minor 4).
