# ADR-0190 implementation review — slice 2 of 3 (per-revision state and wake), loop 3

- **ADR**: [ADR-0190](../adr/0190-run-bound-to-its-revision.md) — a workflow run is bound to the revision it started with
- **Producing model**: claude-opus-5-5
- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision` (`43b7e63e`, "wake a run's held revision solo
  and keep its state on the Revision (ADR-0190, part 2 of 3)"); `83bfffbd` is slice 1, already reviewed. Against the
  loop-2 commit (`825e33ba`) it changes only `internal/function/function.go` (+33/-14) and
  `internal/function/held_test.go` (+75).
- **Slice scope (S2)**: plan step 4 and the `Revision.status` part of step 5 — `Revision.status` written by the
  Function reconciler and watched; the store scaler writes only a held Revision's phase intent; a held revision boots
  solo through `revisionTemplate` and `convergeRevision`; `failed` judges a pinned ref by its Revision; per-revision
  single-flight and idle reclaim. Scenario broken-edit-spares-old-run, plus a pinned call to a held revision scaled to
  zero waking it solo. Out of scope: pools (Decision 8), prune and GC waits (S3).
- **Verdict**: **pass** (slice) — 0 Blockers, 0 Majors, 1 Minor; the loop-2 Major is resolved

## Verification (run in the worktree through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Build, Linux | `GOOS=linux go build ./...` | exit 0 |
| Vet | `go vet ./internal/activator/... ./internal/function/... ./pkg/funcd/...` | exit 0 |
| Vet, Linux, e2e tag | `GOOS=linux go vet -tags e2e` on the same | exit 0 |
| Lint | `go tool golangci-lint run` on the same | `0 issues.`, exit 0 |
| Lint, Linux, e2e tag | `GOOS=linux golangci-lint run --build-tags e2e` on the same (binary from `go tool -n`) | `0 issues.`, exit 0 |
| Touched packages, race | `go test -race -count=1 ./internal/activator/... ./internal/function/` | `ok` x3 (activator 2.0 s, storescaler 1.3 s, function 12.4 s) |
| Scenario tests, race | `go test -race -count=1 -tags e2e -run 'TestScenarioBrokenEditSparesOldRun\|TestScenarioHeldRevisionWakesSolo' ./pkg/funcd/` | 2 PASS (1.16 s, 1.15 s), `ok` |
| New test, race, repeated | `go test -race -count=3 -run TestHeldWakeRetriesBootError ./internal/function/` | 3 x 2 subtests PASS |
| ADR substance | `git show --stat HEAD -- docs` | no file under `docs/` touched |
| Condition reasons | the reasons the slice writes (`StartFailed`, `ShimNotReady`, `NoReplicas`, `Restarting`, `ShapeInvalid`, `RuntimeUnavailable`, crash loop) | all exist at `HEAD~1`; no new reason (Review checklist item 4) |

### Slice reverted and mutants (`go test -overlay`, sources from `git show HEAD~1:<file>`)

| Check | Overlay | Result |
|---|---|---|
| Slice reverted | the four non-test files (`activator.go`, `storescaler.go`, `function.go`, `funcd.go`) at `HEAD~1`; the two scenario tests, `-race -tags e2e` | both FAIL for the slice's reason: `TestScenarioBrokenEditSparesOldRun` — `r1`'s `b` fails with `storescaler.ScaleTo: function default/flow-b is Failed (ShapeInvalid)`; `TestScenarioHeldRevisionWakesSolo` — `r1`'s `b` fails with `function default/flow-b did not become ready within 30s` |
| M4 | `heldBootFailed` returns requeue 0 (the held hint is not kept after a boot error) | killed: `TestHeldWakeRetriesBootError/{materialize,image}` — "a later pass boots the held revision, with no Revision event" |
| M5b | the `convergeRevision` error of a held boot returns the error again instead of `heldBootFailed` (the loop-2 behaviour) | killed: `TestHeldWakeRetriesBootError/{materialize,image}` — unexpected error from the pass |
| M5 | the `revisionTemplate` error of a held boot returns the error instead of `heldBootFailed` | survived (see Minor 1) |
| M6 | step 3e returns the held error as a `convergeError` again instead of through `afterHeld` | survived (see Minor 1) |

## Loop-2 finding

| Loop-2 finding | Status | Evidence |
|---|---|---|
| Major 1 — a held revision's boot error drops the wake (Deploying, no worker, no retry) and `failPass` writes it to the Function's status | **resolved** | `convergeHeld` (`function.go:1858`) now re-marks the held hint in a deferred check whenever it returns an error or a requeue, so a held revision with no worker comes back without a Revision event. A boot error from `revisionTemplate` or `convergeRevision` goes to `heldBootFailed` (`function.go:1987`), which writes it to the Revision's Ready condition (`RuntimeUnavailable` for `runtime.ErrImageUnavailable`, as the current revision's gate names it, otherwise `StartFailed`), keeps the Revision's phase and requeues on the supervision period. Step 3e no longer returns early: the current revision's step 4 and status write run first, and `afterHeld` (`function.go:721`) returns any remaining held error as a `routeError` after that write, so `failPass` never writes it to the Function. `TestHeldWakeRetriesBootError` (`held_test.go:211`) covers a materialize outage and an absent image: the Revision carries the reason, the Function stays `Ready` with `RevisionReady` True, and once the cause clears a later pass boots the held revision with no Revision event; M4 and M5b kill it. |

## Findings

### Minor 1 — two of the held error paths have no test (model)

The fix guards two more paths that no test reaches, so their mutants survive:

- **M5**: a `revisionTemplate` error of a held boot (`function.go:1943`, a Revision get error, a stamp conflict) can go
  back to failing the pass and no test fails. The materialize and image cases both come from `convergeRevision`.
- **M6**: `afterHeld` in the main path (`function.go:641` and `:648`) can be reverted to the loop-2 `convergeError`,
  which writes the held error to the Function's status and skips step 4. No test fails, because every error left in
  `convergeHeld` after `heldBootFailed` (a Revision list, `readyReplicas`, `stopUnlistened`, `retireRevision`, a
  non-conflict `writeHeld`) needs a store or runtime failure that the harness does not inject.

These are defensive paths, and the loop-2 failure mode they guard is pinned for the boot errors that occur in
practice. A test with a store that fails one Revision list, which asserts that the Function's status is unchanged and
that a later pass boots the held revision, would pin both.

## ✅ Verified correct (keep)

- **The hint survives a failed pass.** The deferred `markHeld` sits after the `takeHeld`/`isSpared` check and reads
  the named results, so a loop error inside `convergeHeld` (a shadowed `err`, returned through the named result) still
  re-marks; a Revision event that arrives during the pass stays marked because `takeHeld` cleared the hint before the
  list.
- **A held boot error stays on its Revision.** Keeping the phase (Deploying) and not writing Failed is consistent
  with "a Failed one stays Failed while held" in `convergeHeldRevision`: writing Failed for a transient registry or
  image error would make the old run's revision unrecoverable. A pinned call therefore waits out its activation timeout
  during an outage instead of failing at once, and the supervision-period retry boots the revision when the cause
  clears.
- **The current revision's pass runs regardless.** The held error is reported after `finish` or `gateFailed` has
  written the current revision's status, including on the RuntimeUnavailable gate path, so Decision 5's partition
  holds and the current revision is never skipped because of an old run's revision.
- From loops 1 and 2, still in place: every current-revision gate goes through `heldThenGate`; the scaler writes only
  a held Revision's Idle->Deploying intent and its reclaim to Idle, never the Function's phase; `FunctionRef` keys
  single-flight, activity and idle reclaim per revision, with the 5-minute `heldIdleTimeout` for a Function without
  an idle timeout; `failed` judges a pinned ref by its Revision through `RevisionFailedFault`; the Revision watch
  (`MapRevision`) queues the Function; the boot reuses `revisionTemplate`, `convergeRevision`, `readyReplicas` and
  `stopUnlistened`.

## Recommendation

Pass for slice 2. The loop-2 Major is resolved and pinned by two killed mutants (M4, M5b); build, vet and lint are
green on macOS and Linux (e2e tag included), the touched packages pass under `-race`, and both scenario tests fail
with the slice reverted for its reason. Minor 1 (tests for the `revisionTemplate` held error and for `afterHeld` on a
non-boot error) can land with slice 3. No status is advanced: this is a slice review, and the ADR stays `Reviewing`
until the whole implementation passes.

## Ledger row

```json
{
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "slice": "S2 per-revision state and wake (loop 3)",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0190-slice2-implementation-claude-opus-5-5-3.md",
  "notes": "S2 loop 3: loop-2 M1 resolved (convergeHeld re-marks the held hint on error or requeue; heldBootFailed writes a held boot error to the Revision, RuntimeUnavailable or StartFailed, with a supervision-period retry; afterHeld returns the held error after the Function's status write). TestHeldWakeRetriesBootError added; M4 and M5b killed. Both scenario tests fail with the slice reverted for its reason; build/vet/lint green incl Linux and e2e tag; touched pkgs -race ok. m1 [model] the revisionTemplate held error path and afterHeld on a non-boot error are untested (M5, M6 survive)."
}
```
