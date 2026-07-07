# ADR-0094 implementation review, round 2 — workflow engine core (model: claude-opus-4-8)

## Verdict: pass — 0 blockers, 0 majors (ADR-0094 implementation re-review, model: claude-opus-4-8)

Round 2 after the `changes-requested` verdict in
[adr-0094-implementation-claude-opus-4-8.md](adr-0094-implementation-claude-opus-4-8.md); the
rework landed as one commit. Verification executed (Nix dev shell): `go build ./...` exit 0 ·
`go vet ./...` exit 0 · `go tool golangci-lint run ./...` exit 0 (0 issues) · `go mod verify`
exit 0 · `go test ./...` exit 1 solely from the pre-existing environmental
`TestPythonPoolSmoke` (`internal/testkit/bench`, Python-shim readiness — untouched by this work,
**env**-attributed, not scored). All workflow/controlplane/config/artifact/pkg-funcd/cmd
packages `ok`; zero `t.Skip(` in the touched test surface.

### Prior findings — all five model-attributed items verified closed

- **🔴 B1 (4 missing scenario tests) — CLOSED.** Named, un-skipped, passing (verbose run
  captured): `TestRunTimeoutFails`, `TestOnFailureHandlerRuns` (dispatched exactly once, phase
  stays `Failed`), `TestDuplicateRunNameRejected` (Conflict/AlreadyExists),
  `TestRevisionPinnedMidRunRepush` (in-flight step keeps its pinned `@v1` image while the live
  Workflow serves `@v2`). Scenario tally: **17/17 in-gate scenarios grep to a test** (the 3
  *(lands with F65)* remain deferred per the ADR's own sequencing).
- **🔴 B2 (spec/revision pinning) — CLOSED.** `runstate.Record.Spec` snapshots the WorkflowSpec
  at `Execute` (`engine.go:107`); `Resume(ctx, ns, name)` dropped its spec parameter and rebuilds
  from `rec.Spec` (`engine.go:121,129`); the run reconciler passes no live spec on the resume
  path (`reconcile_run.go:116`). Each step's pinned image ref mirrors to `StepState.Revision`.
  This matches the ADR's own durable-state contract ("run record (input, pinned step graph, …)").
- **🟡 M1 (payloadLimit + retention unenforced) — CLOSED.** Input cap at admission
  (`admission.NewWorkflowRunPayloadAdmission`, registered in the server pipeline,
  `pkg/funcd/funcd.go:563`; `TestWorkflowRunPayloadAdmission` covers admit/deny/disabled).
  Output cap in the engine (over-cap ⇒ **permanent** failure, no retry;
  `TestPayloadLimitCapsStepOutput`). Retention: `Engine.SweepExpired` (terminal-only,
  clock-injected; `TestSweepExpired` proves old-terminal swept, fresh + non-terminal kept) driven
  by a periodic pkg/funcd loop gated on `workflowRetention > 0` (`funcd.go:680`).
- **🟡 M2 (DefaultStepTimeout dead + default) — CLOSED.** Each dispatch attempt runs under a
  per-step deadline — the step's `timeout`, else the engine default (`engine.go:325-334`);
  config default is now `300s` (`config.go:154`), with `retention=720h`, `payloadLimit=1MiB`,
  `defaultRetry=1` alongside. `TestPerStepTimeoutIsStepFailure` proves a step timeout is a
  *step* failure, distinct from `RunTimedOut`.
- **🟡 M3 (deletion policy ignored) — CLOSED.** `buildKVStore` attaches the cascading owner
  reference only for `deletion: delete`; `retain` (default) attaches none
  (`reconcile_workflow.go:181-183`). `TestMaterializeDeletionPolicy` asserts both directions.
- **Minor (RunTimedOut reason) — CLOSED.** The run-deadline path reports `RunTimedOut`
  (`runTimedOut` wrapper), asserted by `TestRunTimeoutFails`.
- **Minor (join-any engine level) — CLOSED.** `TestJoinAnyExclusiveBranch` drives the full
  if/else + `join: any` merge through the engine and asserts the composite carries the surviving
  branch and **not** the skipped one.

### Remaining

- **Minor · attribution: adr** (carried from round 1, not the builder's to fix): the Contracts
  block still lists `func (e *Engine) Describe(…)` while its own decider-authorized amendment
  states describe is served by a plain GET of WorkflowRun (which is what funcdctl does). Tidy
  belongs to the F65 ADR or a future superseding edit — does not block this gate.

### ✅ Verified correct (keep it)

- No regressions against round 1's verified-strong list: state machine, crash recovery (fresh
  attempt IDs over the same Badger store — now also seeding the pinned spec), fail-closed
  dispatcher + audit, materialization cycle + pooling/warmth, declarative cancel/pause/resume on
  the controller workqueue, validation layer, `status.runs` links.
- The rework touched no ADR text and added no dependencies (`git diff` over the rework range:
  `docs/adr/0094-*` and `go.mod`/`go.sum` both empty); `internal/workflow` still imports no bus.
- ADR Review checklist: **13/13 items hold** (round 1: 8/13).

### Recommendation

**pass** — DoD met in full. This gate stamps ADR-0094 `Reviewing → Implemented`, advances feat
F64 → `implemented`, and moves the board card → Done. The one surviving `adr`-attributed Minor
(stale `Describe` signature) rides to the F65 ADR.
