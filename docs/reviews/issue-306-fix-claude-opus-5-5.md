## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #306 fix, model: claude-opus-5-5)

Change: branch `fix/i306`, commit f51f601 `fix(workflow): end a run whose first record overflows the run store Failed`
(`internal/workflow/engine.go`, `internal/workflow/reconcile_run.go`, `internal/workflow/reconcile_run_test.go`,
`internal/workflow/engine_scenarios_test.go`).

### 🟡 Minor

- **The Ready=False reason says `StepFailed` for a run that failed before any step ran** · attribution: model ·
  `internal/workflow/reconcile_run.go:144` passes the store error to `failureReason`, which has no token for a
  record overflow and falls back to `StepFailed` (`reconcile_run.go:239-241`); the input case reaches the same
  fallback through `mirror` (`reconcile_run.go:276`). The issue asks for "a reason that names the size limit": the
  condition *message* does (the test asserts `value limit`), the machine-readable reason does not. Same behaviour
  as the #116 precedent, so cosmetic. Fix: a reason token for the record overflow, if a follow-up wants one.
- **The contract-mismatch path's new input-drop is not tested directly** · attribution: model ·
  the `failAtStart` retry without input (`engine.go:280-283`) changes the #118 scenario (an InputSchemaMismatch
  run whose 1 MiB input overflows the record is now recorded Failed without its input, and onFailure fires once).
  The #118 test was retargeted to an oversize pinned spec (`engine_scenarios_test.go:186`) so it keeps testing the
  unstorable case — justified and stated in the commit message — but no test now pins the new behaviour for that
  gate. The shared line is covered: mutant M2 below is killed by `TestIssue306_…/input`.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit f51f601` with both test files kept at
  HEAD: `TestIssue306_OversizeFirstRecordFailsRunOnce/input` and `/spec` FAIL with
  `Reconcile = runstate.badger: run "big-1" record is 1049042 bytes, over the run store's 1048576-byte value limit,
  want the run ended Failed, not requeued` — the issue's exact symptom. The #118 tests pass on the pre-fix code too
  (the retarget does not weaken them).
- **Passes with the fix under -race**: after `git reset --hard f51f601`, `go test -race -run
  'TestIssue306|TestIssue118|TestIssue116'` PASS; the worktree was left at f51f601 and clean.
- **Mutants, each killed**: M1 disabling the reconciler's `failUnrecorded` branch (`reconcile_run.go:143`) → `/spec`
  FAIL; M2 disabling the input-drop retry (`engine.go:280`) → `/input` FAIL; M3 returning every first-persist error
  as is (`engine.go:266`) → `/input` FAIL (notify 0, want 1).
- **Cause, not symptom**: the issue's cause is that `execute`/`failAtStart` returned `payload_too_large` and the
  reconciler treated it as an infra error. The fix turns the first-record overflow into a run outcome (Failed, one
  onFailure) in the engine, and ends a run unrecorded only when even the input-less record cannot fit. No retry,
  timeout or swallowed error; the reconciler branch is gated on `rec == nil && !started`, so a started run's store
  errors still requeue.
- **ADR conformance**: ADR-0094 (runs end in a terminal phase, onFailure fires once, payload cap keeps an
  over-cap input out of the record) — the input drop mirrors the existing over-cap path (`engine.go:253-256`). No
  ADR file touched.
- **Reuse**: `failUnrecorded` is extracted from the existing ADR-0107 replay-rejection block and reused by both
  callers rather than duplicated; the engine reuses `failAtStart`/`fail`; the test reuses `newStore`,
  `seedWorkflow`, `seedRun`, `newFake` and the real in-memory Badger run store (`wbadger`), the same harness as
  `TestIssue116_…`. Nothing new where something existing would do.
- **Conventions**: `api/fault` kinds (`fault.KindOf`, `fault.Wrapf` with `fault.Invalid`, matching `recordFailed`),
  ctx-first, no `any`, no new imports, comments state the why only; table-driven test in the surrounding idiom.
- **Scope**: every hunk serves the issue; the one existing-test edit is the justified #118 retarget.
- **Checks (touched packages)**: `go build ./...` OK; `go test -race ./internal/workflow/...` ok (workflow,
  runstate/badger); `go vet ./internal/workflow/...` OK; `golangci-lint run ./internal/workflow/...` 0 issues.
  Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(workflow):` subject, Cause/Fix/Test body, `Fixes #306`, attribution trailer, one issue per commit.

### Definition of Done

11 / 11 items hold (fix checklist; item 8 for the host-side checks in scope here, the group gate runs the rest;
item 11 for the commit — the PR is opened later). Misses: none.

### Model scorecard

Not recorded here (the orchestrator records the ledger row): claude-opus-5-5 on issue #306 (fix) → pass, 0/0/2,
2 model-attributed, DoD 11/11.

### Recommendation

Sign off. The two Minors are optional follow-ups (a record-overflow reason token; a direct test of the
InputSchemaMismatch gate with an overflowing input) and do not block the PR.
