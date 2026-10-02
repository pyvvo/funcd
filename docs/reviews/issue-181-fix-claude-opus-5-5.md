## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #181 fix, model: claude-opus-5-5)

Commit reviewed: `1b27118` (apply the payload cap to a run's input at run start), on the workflow group
branch at `fbcb9ff`. The later commit `fbcb9ff` (another issue of the group) also edits
`internal/workflow/reconcile_run_test.go`, so `git revert --no-commit 1b27118` conflicts on that file. The
revert check therefore reverted `internal/workflow/engine.go` only and kept the `fbcb9ff` test file.

### Minor 1 — a replay of a run that failed the payload gate runs with an empty input  ·  attribution: model

The fix stores the gate-failed run with `rec.Input = nil`, which is necessary: the over-cap input is the
reason the in-memory record could not be stored. But `Engine.Replay` (`internal/workflow/engine.go`, the
`Input: src.Input` record and `e.drive(…, src.Input)`) accepts that record as a source. All of its DAG
steps are `Pending`, so `--from` any step passes the seed checks, and the replay runs the workflow with an
empty input and can end `Succeeded`. Before the fix this record did not exist (the disk run Succeeded with
the full input; the in-memory run was never stored).

Evidence: a scratch probe (not committed) with `PayloadLimit: 100` and a 210-byte input:
`source: … input 210 bytes exceeds the payload limit 100 … phase=Failed inputLen=0`, then
`Replay(… From: "a")` → `replay: phase=Succeeded a calls=1 a input=""`.

Fix (builder, or a follow-up issue): make `Replay` refuse, as `SeedInvalid`, a source run that failed at
the run-start gate (for example, a failed source with no recorded input whose steps never ran). The
contract gate's failed records reach the same replay path with their mismatched input, so the guard
belongs in `Replay` rather than in the gate. Not blocking: the issue's path (a Sensor-created over-cap run)
is fixed, and this needs an explicit operator replay of a run that already reports the payload error.

### Minor 2 — the regression test's last failure message prints the whole run record  ·  attribution: model

`t.Fatalf("run record = %v (err %v), …", rec, err)` in `TestIssue181_RunStartGateCapsInput` formats
`*runstate.Record` with `%v`, so a failure prints the 1.3 MB input as a byte list. Under the mutant that
keeps `rec.Input`, the test output was 5 MB. Fix (builder): print `rec.Phase` and `len(rec.Input)` instead.

### Verified correct (keep it)

- **Revert check (engine.go reverted, test kept).** `go test -race -count=1 -run TestIssue181_
  ./internal/workflow/` FAILS on both sub-tests, for the issue's two reported reasons:
  - `in-memory`: "Reconcile returned runstate.badger: run "big-1" record is 1311245 bytes, over the run
    store's 1048576-byte value limit (a requeue), want the run to fail";
  - `disk`: "phase="Succeeded" … step a dispatched 1 times, want Failed naming the payload limit and no
    step run".
- **With the fix.** After `git reset --hard fbcb9ff`, `go test -race -count=3 -run TestIssue181_
  ./internal/workflow/` → `ok` (both sub-tests pass three times, none skipped).
- **The issue's own probe shape.** The scratch probe above calls `Execute` directly with an over-cap input:
  the run fails with the payload-limit reason and stores no input, which is the behavior the issue expects.
  The regression test covers the issue's real path: a WorkflowRun seeded on the internal store (as a
  Sensor action creates it) and driven by `RunReconciler` with the real engine on both run-store drivers.
- **Cause, not symptom.** The issue names the cause: `Engine.execute` checked only the pinned contract,
  and `Config.PayloadLimit` was applied only to step outputs. The fix adds the cap at the run-start gate,
  before the first persist and before the contract gate. It does not raise a limit, add a retry, or
  swallow an error. The error is `fault.Invalid` with the same wording as the admission check
  (`internal/controlplane/admission/workflowrun.go`), so both entry points report the cap the same way.
  ADR-0109 §Firing and the `startWorkflow` comment in `internal/sensor/sensor.go` now describe true
  behavior.
- **The over-cap input stays out of storage and the handler.** `rec.Input = nil` and the `nil` input
  passed to `failAtStart` keep the input out of the run record and the FailureContext, so the Failed
  record fits the in-memory store's value limit and onFailure fires exactly once over two reconciles.
- **Mutants (overlay).** Keeping `rec.Input` → caught on both drivers (in-memory requeue; disk record
  holds the input). Passing `input` instead of `nil` to `failAtStart` → caught ("onFailure dispatched 1
  times with 1310936 input bytes"). Skipping the persist before `fail()` in `failAtStart` → caught by
  `TestIssue118_InputMismatchHandlerNotRefiredWhenRecordTooLarge`.
- **Reuse.** `failAtStart` extracts the existing record-then-fail sequence from the contract gate instead
  of copying it; both gates share it. No new type, dependency or test harness: the test reuses `newStore`,
  `seedRun`, `step`, `newFake` and the real `wbadger` drivers.
- **Scope.** Only `internal/workflow/engine.go` and `internal/workflow/reconcile_run_test.go` changed. The
  `Config.PayloadLimit` field comment was corrected to name the run-start gate. No test was weakened or
  deleted, and no ADR file was touched. Sub-workflow children also pass through `execute`, so a child
  input over the cap now fails at start too, which matches ADR-0094 ("Run input is … capped by
  `workflow.payloadLimit`").
- **ADRs.** The fix conforms to ADR-0094 (run input is capped), ADR-0098 (the run-start gate fails a run
  fast and records it), and ADR-0109 (the run-start gate is the backstop for internal-store runs).
- **Checks.** `gofmt -l internal/workflow` → empty; `go build ./...` → ok; `go vet` (host and
  `GOOS=linux`) on `./internal/workflow/... ./internal/sensor/...` → ok; `golangci-lint run
  ./internal/workflow/...` host and `GOOS=linux` → "0 issues."; `go test -race -count=1
  ./internal/workflow/... ./internal/sensor/... ./internal/controlplane/admission/...` → all `ok`;
  `go test -count=1 -tags e2e ./pkg/funcd/...` → `ok` (164.9s); `just check-hygiene` → clean. No Lima lane
  was run (out of scope for this stage).
- **Shape.** Subject `fix(workflow): apply the payload cap to a run's input at run start`, `Fixes #181`,
  the attribution trailer, one issue in the commit.

### Observation (not scored)

The `Workflow` config comment in `internal/platform/config/config.go` still says `PayloadLimit` is
"reserved for the run-GC / admission gates, not yet enforced". That text was already untrue before this fix
(admission enforces the cap). It is outside #181's diff; a later cleanup can correct it.

### Recommendation

Pass. Minor 1 is worth a follow-up issue (`Replay` should refuse a source run that failed at the run-start
gate). Minor 2 is a one-line polish in the test.
