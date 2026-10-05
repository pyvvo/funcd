# ADR-0190 implementation review — slice 1 of 3 (pinning and dispatch), loop 1

- **ADR**: [ADR-0190](../adr/0190-run-bound-to-its-revision.md) — a workflow run is bound to the revision it started with
- **Producing model**: claude-opus-5-5
- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision` (`83bfffbd`, "pin a run's step revisions at
  start and dispatch by pin (ADR-0190, part 1 of 3)"); earlier commits are out of scope
- **Slice scope (S1)**: plan steps 1-3, the `internal/revhold` part of step 5, and `Upstream` per pin; scenarios
  edit-mid-run-keeps-old-code, removed-step-still-runs, two-revisions-side-by-side, start-waits-for-step-code,
  replay-pins-fresh-steps, child-step-bound, ref-step-bound, cancel-releases-revision, workflow-delete-cancels-runs,
  resume-keeps-pins. Out of scope: per-revision activator/scaler state (step 4), the solo boot of a held revision,
  pools (Decision 8), broken-edit-spares-old-run and pooled-edit-isolated (S2/S3).
- **Verdict**: **pass** (slice) — 0 Blockers, 0 Majors, 3 Minors

## Verification (run in the worktree through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Build, Linux | `GOOS=linux go build ./...` | exit 0 |
| Touched packages, race | `go test -race -count=1 ./internal/workflow/... ./internal/revhold/... ./internal/function/... ./internal/gc/... ./internal/activator/... ./api/types/...` | exit 0, every package `ok` |
| pkg/funcd scenarios, race | `go test -race -count=1 -tags e2e -run 'TestIssue776_EditMidRunKeepsOldCode\|TestScenarioRemovedStepStillRuns\|TestScenarioTwoRevisionsSideBySide\|TestScenarioRefStepBound\|TestScenarioWorkflowDeleteCancelsRuns\|TestStoreGranterPins' ./pkg/funcd/` | exit 0, 6 PASS |
| Vet | `go vet` on the 7 touched package trees, plus `-tags e2e ./pkg/funcd/` | exit 0 |
| Vet, Linux | `GOOS=linux go vet` on the same | exit 0 |
| Lint | `golangci-lint run` on the same | `0 issues.`, exit 0 |
| Lint, Linux | `GOOS=linux golangci-lint run` on the same (binary resolved with `go tool -n`, as `scripts/agent/gate.sh` does) | `0 issues.`, exit 0 |
| Generated OpenAPI | `go run ./internal/controlplane/cmd/specgen/ -out <tmp>` then `diff -q` against `api/openapi/funcd.v1alpha1.yaml` | identical |
| ADR substance | `git show --stat HEAD` | no file under `docs/` touched; ADR-0190 unchanged |

### Issue test against the reverted slice

Overlay of every non-test Go file of the commit at `HEAD~1` (`internal/revhold/revhold.go` and
`pkg/funcd/granter_internal_test.go` dropped), then `go test -tags e2e -overlay … -run TestIssue776_EditMidRunKeepsOldCode ./pkg/funcd/`:

```
--- FAIL: TestIssue776_EditMidRunKeepsOldCode (0.68s)
    run_revision_e2e_test.go:189: expected: "v1\n"  actual: "v2\n"
        Messages: r1's b runs the code it started with
```

It fails for the reason plan step 1 names (the run's `b` answers with `v2`) and passes with the slice.

### Mutants (`go test -overlay`, each must fail a test)

| # | Mutation | Killed by |
|---|---|---|
| m1 | `internal/workflow/dispatch.go`: drop `fn.Revision, fn.UID = req.Revision.Revision, req.Revision.FunctionUID` (dispatch ignores the pin) | `TestIssue776_EditMidRunKeepsOldCode` ("r1's b runs the code it started with"), `TestScenarioTwoRevisionsSideBySide` ("each run's b is answered once, by its own revision") |
| m2 | `internal/workflow/reconcile_run.go`: disable the started-run Workflow UID check (`if false && run.Status.WorkflowUID != ""`) | `TestScenarioWorkflowDeleteCancelsRuns` ("r1 ends Cancelled", 60 s timeout), `TestWorkflowUIDCancel` |
| m3 | `internal/workflow/reconcile_workflow.go`: `pruneFunctions` ignores `held.Function` | `TestScenarioRemovedStepStillRuns` ("the removed step's Function waits for r1") |

All three mutants are killed.

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minor

1. **A replay pins the current Workflow's steps, but runs the source run's spec** — `model`.
   `pinRevisions` collects targets from `wf.Spec` (`internal/workflow/reconcile_run.go`, `add(wf.Name, wf.Spec)`), while
   `Engine.replay` builds the record from `spec := src.Spec` (`internal/workflow/engine.go`, replay) and dispatches by
   `stepTarget(workflow, run.spec, …)` over that spec. When the two disagree on a fresh step's target (a step absent
   from the current Workflow, or a `function.ref` step turned into an image step), the replay record has no pin for
   that target and `targetPin` fails the step `Forbidden` "has no revision pin", instead of the gate naming the
   Function (Decision 2: "A replay gates its fresh steps and pins them fresh"). The ADR-0107 drift gate rejects most
   such replays first, and before this change the same step failed `NotFound`, so the effect is an edge-case fault
   kind. Suggested fix: derive the replay's pin targets from the source spec's steps (with the current Function
   content), or the union of both.
2. **The `spared` flag is in memory only** — `model` (plausible, not exercised).
   `Reconciler.spared` (`internal/function/function.go`) makes `steadyState` run the full pass while a held revision
   keeps workers, so `drain` retires them once the run ends (proven by `TestScenarioCancelReleasesRevision`). It is
   private to the Function reconciler, so the "no shared map" constraint holds, but a restart empties it: a Function
   that is otherwise steady skips `drain`, and a held revision's worker that a runtime kept across the restart is not
   retired after its run ends until the Function leaves steady state (an edit, a scale to zero). A store-derived
   check (for example, any listed instance whose revision is neither serving nor current) would survive a restart.
   S2 reworks this area (the solo boot of a held revision), so it can be settled there.
3. **No `file://` case in the pin unit tests** — `model`.
   Plan step 6 lists `file://` among the `pinRevisions` unit cases (Decision 11: the pin carries no `ImageDigest`, the
   gate compares the artifact reference). `TestPinRevisions` (`internal/workflow/revision_pin_test.go`) has no
   `file://` case, and the pkg/funcd scenarios push OCI-layout images that carry digests. The code path
   (`fn.Spec.ImageDigest != "" && …` in `pinFunction`; `rev.Spec.ImageDigest == pin.ImageDigest` in `storeGranter`)
   looks right; it is unproven by a test.

Note for S2 (not scored here): `Activator.Wake` records activity and hand-outs under `fn.function()`, so activity and
idle reclaim are per Function, while Decision 5 says they are per revision. Step 4 belongs to S2; it must either
move them per revision or state why per-Function activity is the intended reading.

## ✅ Verified correct — keep it

- **Pins before the goroutine** (Decision 3): `writePins` writes `status.pins` and `status.workflowUID` with one
  `store.Update` before `r.engine.start`; a `Conflict` returns `Requeue` without starting; `before` becomes the
  written status, so the following sync does not see a phantom diff.
- **Content gate** (Decisions 1-2): `pinFunction` waits `WorkflowNotReady` naming the Function while it is absent,
  has another image than the step, has no current revision, its Revision is missing or owned by another UID, or the
  Revision does not hold the Function's image, runtime, handler and (when set) digest. No generation counter is
  compared. Children of the ADR-0189 tree and `function.ref` targets are pinned; the replay pins its children from the
  live specs (`replayChildren`).
- **Dispatch carries the pin** (Decision 4): `targetPin` returns nil for a nil `Record.Pins` (the pre-ADR fallback,
  `TestTargetPinFallback`) and `Forbidden` for a missing pin, both for steps and for the `onFailure` handler.
  `HTTPDispatcher.Dispatch` rejects a pin for another target, passes the pin to `Granter.Allow` and maps a denied
  pinned grant to `fault.NotFound` naming the pin; `FunctionRef.Revision/UID` reach `Upstream`, which returns a worker
  of exactly that revision through `upstreamOf`, or `NotFound` for a deleted Function, another UID, a missing
  Revision or another Function's Revision. Nothing falls back to the serving revision (mutant m1).
- **`storeGranter`**: nil pin keeps the by-name check; a pin is granted only when it names the target and its
  Revision is controlled by the pinned name and UID with the pinned digest (`TestStoreGranterPins`).
- **Child records** get their subtree's pins (`subtreePins`); `TestScenarioChildStepBound` checks both the
  dispatched pin and the child record.
- **`internal/revhold`** is a leaf package importing only `api/fault`, `api/types/v1alpha1` and `internal/store`;
  `Held` does one List per decision and skips terminal and cancelled runs; holds are keyed by Function UID, so a
  re-created namesake is never held by an old pin. `drain`, `stopAll`, `pruneFunctions` (with a requeue while
  postponed) and both GC paths consult it (mutant m3, `TestHeldChildrenWaitForTheirRun`).
- **Decision 10**: a started run (non-empty `status.workflowUID`) whose Workflow is NotFound or has another UID is
  cancelled; an open started run requeues every `waitRequeue`, so no Workflow event is needed (mutant m2). Terminal
  runs return before the check.
- Edits to existing tests are signature adaptations and step-Function seeding for the new gate; none is weakened.
- Every in-scope scenario has a named, passing test; the issue test fails with the slice reverted.

## Recommendation

Pass for slice 1. The three Minors can go to the S2 loop: Minor 1 and Minor 3 are small; Minor 2 fits the S2 work
on held revisions. No status is advanced: this is a slice review, and the ADR moves on only after the last slice.

## Ledger row

```json
{
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "slice": "S1 pinning and dispatch (loop 1)",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 10,
  "dod_total": 10,
  "report": "docs/reviews/adr-0190-slice1-implementation-claude-opus-5-5.md",
  "notes": "S1 pass: issue test fails reverted (v2 not v1), 3/3 mutants killed, build/vet/lint green incl Linux; minors (model): replay pins current wf steps but runs source spec; in-memory spared flag lost on restart; no file:// pin unit test"
}
```
