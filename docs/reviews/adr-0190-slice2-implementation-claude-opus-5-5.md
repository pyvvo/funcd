# ADR-0190 implementation review — slice 2 of 3 (per-revision state and wake), loop 1

- **ADR**: [ADR-0190](../adr/0190-run-bound-to-its-revision.md) — a workflow run is bound to the revision it started with
- **Producing model**: claude-opus-5-5
- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision` (`42030bd0`, "wake a run's held revision solo
  and keep its state on the Revision (ADR-0190, part 2 of 3)"); earlier commits are slice 1, already reviewed
- **Slice scope (S2)**: plan step 4 and the `Revision.status` part of step 5 — `Revision.status` written by the
  Function reconciler and watched; the store scaler writes only a held Revision's Idle→Deploying intent; a held
  revision boots solo through `revisionTemplate` and `convergeRevision`; `failed` judges a pinned ref by its Revision;
  per-revision single-flight and idle reclaim. Scenario broken-edit-spares-old-run, plus a pinned call to a held
  revision scaled to zero waking it solo. Out of scope: pools (Decision 8), prune and GC waits (S3).
- **Verdict**: **changes-requested** (slice) — 0 Blockers, 1 Major, 3 Minors

## Verification (run in the worktree through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Build, Linux | `GOOS=linux go build ./...` | exit 0 |
| Vet | `go vet ./internal/activator/... ./internal/function/... ./pkg/funcd/...` | exit 0 |
| Vet, Linux, e2e tag | `GOOS=linux go vet -tags e2e` on the same | exit 0 |
| Lint | `golangci-lint run` on the same | `0 issues.` |
| Lint, Linux, e2e tag | `GOOS=linux golangci-lint run --build-tags e2e` on the same (binary from `go tool -n`) | `0 issues.` |
| Touched packages, race | `go test -race -count=1 ./internal/activator/... ./internal/function/` | `ok` x3 (activator 2.0 s, storescaler 1.7 s, function 12.2 s) |
| Scenario tests, race | `go test -race -count=1 -tags e2e -run 'TestScenarioBrokenEditSparesOldRun\|TestScenarioHeldRevisionWakesSolo' ./pkg/funcd/` | 2 PASS, `ok` |
| ADR substance | `git show --stat HEAD` | no file under `docs/` touched |

### Slice reverted and mutants (`go test -overlay`, sources from `git show HEAD~1:<file>`)

| Check | Overlay | Result |
|---|---|---|
| Slice reverted | the four non-test files (`activator.go`, `storescaler.go`, `function.go`, `funcd.go`) at `HEAD~1`; the two `pkg/funcd` scenario tests | both FAIL for the slice's reason: `TestScenarioBrokenEditSparesOldRun` — `r1`'s `b` fails with `storescaler.ScaleTo: function default/flow-b is Failed (ShapeInvalid)`; `TestScenarioHeldRevisionWakesSolo` — `r1`'s `b` fails with `function default/flow-b did not become ready within 30s` |
| M1 | `activator.failed` always returns `FailedFault` (Function phase, not the Revision) | killed: `TestWakeFailsOnFailedHeldRevision` waits out the 10 s activation instead of naming the Failed revision |
| M2 | `storescaler.ScaleTo` never routes a held ref to `scaleRevision` | killed: `TestScaleToHeldRevisionWritesRevisionIntent`, `TestScaleToPinnedRefs` |
| M3 | `convergeHeld` always returns before reading the Revisions | killed: `TestHeldRevisionWakesSoloAndIsReclaimed` (an Idle held revision keeps its worker) |

The e2e runs of the mutants were stopped after the unit kills to stay in the time budget; the reverted-slice e2e run
above already shows both scenario tests depend on this slice.

## Findings

### 🟡 Major 1 — a gate failure of the current revision strands a woken held revision (model)

`convergeHeld` runs at step 3e (`internal/function/function.go:663`), after every gate of the current revision:
`return r.gateFailed(...)` at `:552` (ArtifactUnresolved), `:580` (NoMatchingPlatform), `:587` (ShapeInvalid from the
validator), `:593` (RuntimeUnavailable), `:631` (Secret/ConfigResolveFailed), `:644` and `:658` (Pending referent
waits), `:654` (CatalogResolveFailed). `gateFailed` (`:802-849`) touches only the current and serving revisions.
Failure: `R1` is pinned to `flow-b` v1, scaled to zero; `b` is edited to an image tag that does not resolve (or is built
for another platform, or names a runtime no shim runs), so `flow-b` turns Failed at a gate. `R1`'s call to `b` makes
the scaler write v1's Revision Deploying (correctly), but every Function pass returns at the gate before 3e, so v1 never
boots; `failed` sees a non-Failed Revision and the wake polls until the activation timeout, and `R1` fails at `b`.
This breaks the review-checklist item "a Failed Function does not fail a wake of another revision" and the
Consequence "broken deploys never touch open runs"; the crash-loop scenario test passes only because a load failure is
judged after 3e. For the binding gates, Decision 9 asks for the step to fail with the binding's fault; today it fails
by timeout. Fix direction: converge held revisions on the gate-failure paths too (the image, platform, shape and
runtime gates concern the current revision only), and write a held revision Failed with the binding fault when a
binding gate fails; add a test with a held wake under an ArtifactUnresolved current revision.

### Minor 1 — a held revision's calls count as its Function's activity (model)

`FunctionRef.keys` (`internal/activator/activator.go:48`) returns the Function key for every pinned ref, so a call to a
held revision refreshes the Function's idle window (`active`) and adds the held upstream to the Function's
`upstreams` (`handedOut`); the Function's `claimIdle` then waits for it. An old run's calls keep the current
revision's worker up, though Decision 5 makes activity and idle reclaim per revision, and the comment on `keys`
justifies only the serving-revision case. Restrict the Function key to refs that are not `HeldRevision`.

### Minor 2 — an always-on Function's held revision is never reclaimed while its run is open (model)

`ReclaimIdle` reclaims a held revision only when `fn.Spec.Scaling.IdleTimeout > 0` (`activator.go:595`). A
`function.ref` target with no idle timeout keeps a woken held revision's solo worker until the run ends, so a paused run
with no timeout holds RAM, against Decision 10 ("holds disk, never RAM"). Either reclaim held revisions on a default
window or record the exception.

### Minor 3 — no test shows a held wake leaves an Idle current revision at zero (model)

`TestScenarioHeldRevisionWakesSolo` lets `r2` wake the current revision v2 before `r1`'s held wake, and
`TestHeldRevisionWakesSoloAndIsReclaimed` starts with the current revision serving, so neither shows "solo" (only
that revision, not the current one) end to end. Only `TestScaleToHeldRevisionWritesRevisionIntent` shows it, as an
unchanged Function resource version. Add an assertion that `flow-b` stays Idle with no worker of its current revision
after a held wake from an Idle Function.

## ✅ Verified correct (keep)

- Writer partition: the scaler writes a held Revision's phase only through `scaleRevision` with the same edges
  (`transition` over a phase, `ReclaimablePhase`), never the Function (rv unchanged in the tests); a pinned Function
  with another UID is `fault.NotFound`, so no namesake wakes; a Revision another Function controls is refused.
- `failed` judges a held ref by `RevisionFailedFault`, so a Failed Function no longer fails an older revision's wake,
  and a Failed held revision answers at once naming the revision (`TestWakeFailsOnFailedHeldRevision`).
- Single-flight and reclaim per revision: `FunctionRef` as the key; `ReclaimIdle` reclaims a held revision whatever
  the replica floor and forgets refs whose Function or hold is gone.
- The Function reconciler stays the only writer of `Revision.status` readiness and failure (`writeHeld`, conflict
  tolerant); the boot reuses `revisionTemplate`, `convergeRevision`, `readyReplicas` and `stopUnlistened`; the
  `MapRevision` watch plus the `held` hint keep `steadyState` from skipping a pending held revision; a run's end
  writes the Revision Idle and drain retires its worker.
- `pinned` (`function.go:2549`) judges a held revision by its own phase and keeps a pre-switch worker (phase "")
  usable.

## Recommendation

Changes requested for slice 2: fix Major 1 (converge held revisions before or despite the current revision's gates,
with a test); the Minors can be folded into the same loop. No status is advanced: this is a slice review.

## Ledger row

```json
{
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "slice": "S2 per-revision state and wake (loop 1)",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 3,
  "model_attributed": 4,
  "dod_passed": 7,
  "dod_total": 8,
  "report": "docs/reviews/adr-0190-slice2-implementation-claude-opus-5-5.md",
  "notes": "S2 loop 1: both scenario tests fail with the slice reverted for its reason, 3/3 mutants killed, build/vet/lint green incl Linux, touched pkgs -race ok. M1 [model] convergeHeld (function.go:663) runs after the current revision's gates, so an ArtifactUnresolved/NoMatchingPlatform/RuntimeUnavailable/binding gate failure strands a woken held revision and the old run times out. m1 [model] held-revision calls also count as Function activity (keys()); m2 [model] held revision never reclaimed when IdleTimeout<=0; m3 [model] no test that a held wake leaves an Idle current revision at zero."
}
```
