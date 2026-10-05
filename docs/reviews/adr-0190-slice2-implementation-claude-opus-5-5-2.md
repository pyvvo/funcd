# ADR-0190 implementation review — slice 2 of 3 (per-revision state and wake), loop 2

- **ADR**: [ADR-0190](../adr/0190-run-bound-to-its-revision.md) — a workflow run is bound to the revision it started with
- **Producing model**: claude-opus-5-5
- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision` (`825e33ba`, "wake a run's held revision solo
  and keep its state on the Revision (ADR-0190, part 2 of 3)"); `83bfffbd` is slice 1, already reviewed
- **Slice scope (S2)**: plan step 4 and the `Revision.status` part of step 5 — `Revision.status` written by the
  Function reconciler and watched; the store scaler writes only a held Revision's phase intent; a held revision boots
  solo through `revisionTemplate` and `convergeRevision`; `failed` judges a pinned ref by its Revision; per-revision
  single-flight and idle reclaim. Scenario broken-edit-spares-old-run, plus a pinned call to a held revision scaled to
  zero waking it solo. Out of scope: pools (Decision 8), prune and GC waits (S3).
- **Verdict**: **changes-requested** (slice) — 0 Blockers, 1 Major (new), 0 Minors; the 4 loop-1 findings are resolved

## Verification (run in the worktree through `scripts/agent/d`)

| Check | Command (abridged) | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Build, Linux | `GOOS=linux go build ./...` | exit 0 |
| Vet | `go vet ./internal/activator/... ./internal/function/... ./pkg/funcd/...` | exit 0 |
| Vet, Linux, e2e tag | `GOOS=linux go vet -tags e2e` on the same | exit 0 |
| Lint | `go tool golangci-lint run` on the same | `0 issues.` |
| Lint, Linux, e2e tag | `GOOS=linux golangci-lint run --build-tags e2e` on the same (binary from `go tool -n`) | `0 issues.` |
| Touched packages, race | `go test -race -count=1 ./internal/activator/... ./internal/function/` | `ok` x3 (activator 1.9 s, storescaler 1.4 s, function 12.2 s) |
| Scenario tests, race | `go test -race -count=1 -tags e2e -run 'TestScenarioBrokenEditSparesOldRun\|TestScenarioHeldRevisionWakesSolo' ./pkg/funcd/` | 2 PASS (1.05 s, 1.09 s), `ok` |
| ADR substance | `git show --stat HEAD` | no file under `docs/` touched |

### Slice reverted and mutants (`go test -overlay`, sources from `git show HEAD~1:<file>`)

| Check | Overlay | Result |
|---|---|---|
| Slice reverted | the four non-test files (`activator.go`, `storescaler.go`, `function.go`, `funcd.go`) at `HEAD~1`; the two `pkg/funcd` scenario tests, `-race -tags e2e` | both FAIL for the slice's reason: `TestScenarioBrokenEditSparesOldRun` — `r1`'s `b` fails with `storescaler.ScaleTo: function default/flow-b is Failed (ShapeInvalid)`; `TestScenarioHeldRevisionWakesSolo` — `r1`'s `b` fails with `function default/flow-b did not become ready within 30s` |
| M1 | `heldThenGate` writes the gate without converging the held revisions | killed: `TestHeldWakeUnderCurrentRevisionGate` |
| M2 | the Function's reclaim counts every pinned ref of its UID as its activity (`!HeldRevision` dropped, `activator.go:581`) | killed: `TestPinnedCallCountsForItsRevision` |
| M3 | a held revision of a Function with no idle timeout is skipped instead of taking `heldIdleTimeout` | killed: `TestReclaimIdleReclaimsHeldRevision` |

## Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| Major 1 — a gate failure of the current revision strands a woken held revision | **resolved** | every current-revision gate now goes through `heldThenGate` (`function.go:713`): ArtifactUnresolved, RevisionMissing, RevisionStampFailed, NoMatchingPlatform, ShapeInvalid, RuntimeUnavailable, PoolFull; the binding gates are folded into `bindings` and reach the held revisions as `env.gate`, which fails a woken held revision with the binding's reason (Failed) or keeps it Deploying with the wait's reason (Decision 9). `TestHeldWakeUnderCurrentRevisionGate` covers artifact, shape, runtime, secret, config, data reference and catalog; M1 kills it. |
| Minor 1 — a held revision's calls count as the Function's activity | **resolved** | `Wake` touches and hands out under the pinned ref; the Function's `claimIdle` adds only pinned refs that `serves` admits (not `HeldRevision`, `activator.go:581`); `touch` waits out both the ref's and its Function's reclaim (`reclaims`, `activator.go:48`). `TestPinnedCallCountsForItsRevision`; M2 kills it. |
| Minor 2 — an always-on Function's held revision is never reclaimed | **resolved** | `heldIdleTimeout` (5 min, `activator.go:57`) applies when the Function's `IdleTimeout` is 0, matching Decision 10 and the step Functions' window. M3 kills `TestReclaimIdleReclaimsHeldRevision`. |
| Minor 3 — no test that a held wake leaves an Idle current revision at zero | **resolved** | `TestHeldWakeLeavesIdleFunctionAtZero` (`held_test.go:130`): the held revision runs solo, the current revision keeps no running worker, and the Function stays Idle. |

## Findings

### 🟡 Major 1 (new) — a held revision's boot error drops the wake: the revision stays Deploying with no worker and no retry (model)

`convergeHeld` (`internal/function/function.go:1852`) clears the Revision-watch hint first (`takeHeld`, `:1853`) and
re-marks it only after a successful pass with a requeue (`:1892`). Any error from a held revision's boot returns
before that: `revisionTemplate` (`:1935`), `convergeRevision` (`:1940`, which returns an error for a materialize,
artifact-platform, schedule or `runtime.Create` failure, including `ErrImageUnavailable`), `readyReplicas`,
`stopUnlistened` and a non-conflict `writeHeld`. The next pass then skips the held revisions, because a held revision
with no worker is not spared (`drain`/`stopAll` set `spared` only when they keep a held worker), and nothing marks the
hint again: a later wake of the revision is a no-op write in the scaler (`transition` keeps Deploying,
`storescaler.go:142`), so it raises no Revision event, and Deploying is not a reclaimable phase. The Revision stays
Deploying with no worker until another Revision of that Function moves or funcd restarts; `failed` sees no Failed
Revision, so every pinned call waits out the activation timeout.

Evidence: an overlay probe test (scratch file, not committed) with `heldAtZero`, a materializer that fails for the
held revision's handler, then recovers:

```
pass with registry down: err=function.converge: materialize artifact: probe: registry down
function after: phase=Ready RevisionReady=False/StartFailed
retry pass err=<nil>   (x3, materializer recovered)
held revision after registry back: phase=Deploying states=map[]
--- FAIL: TestProbeHeldWakeTransientError
```

The same probe shows a second effect of the 3e error path (`:624-626`): the held revision's error is returned as a
`convergeError`, so `failPass` writes it to the **Function's** status (`RevisionReady` False `StartFailed`) and that
pass skips step 4 for the current revision, although Decision 5 keeps a held revision's failure on its
`Revision.status`. This breaks the slice's "boots a held revision solo" with per-revision state, and the Consequence
"broken deploys never touch open runs" for any transient registry, scheduler or runtime error during an old run's
wake. Fix direction: keep the hint when `convergeHeld` fails (re-mark it on error, or mark while any held revision is
Deploying), and record a held revision's boot error on its Revision (for example `ErrImageUnavailable` as the current
revision's RuntimeUnavailable gate does, with a supervision-period retry) instead of failing the Function's pass; add a
test where a held revision's materialize fails once and the wake then succeeds.

## ✅ Verified correct (keep)

- Gate coverage: no current-revision gate returns before the held revisions converge; the binding gate decides a
  held revision's state by its phase (a serving one keeps its worker, a woken one fails or waits with the binding's
  reason), and `RevisionFailedFault` then answers a pinned call at once.
- Writer partition: the scaler writes a held Revision's phase only through `scaleRevision`, with the Function's
  edges over the phase (`transition(cur, target)`, `ReclaimablePhase`), never the Function's phase
  (`TestHeldWakeLeavesIdleFunctionAtZero` checks the Function stays Idle); a pinned ref with another UID is
  `fault.NotFound`, and a Revision another Function controls is refused. The scaler's reclaim write to Idle on a held
  Revision follows Decision 6 ("idle reclaim scales it back to 0") and the Function's partition.
- Per-revision activity: `FunctionRef` keys single-flight, activity and hand-outs; refs pinned to the current or
  serving revision still count for the Function's reclaim and are kept live; refs whose Function or UID is gone are
  forgotten.
- The Function reconciler is the only writer of a held revision's readiness and failure (`writeHeld`, conflict
  tolerant); the boot reuses `revisionTemplate`, `convergeRevision`, `readyReplicas` and `stopUnlistened`; the
  `MapRevision` watch plus the `heldPending` hint keep `steadyState` from skipping a pending held revision; a run's
  end writes the Revision Idle and retires its worker.
- `pinned` judges a held revision by its own phase and keeps a pre-switch worker (phase "") usable; the
  non-held cases keep their previous readiness rule.

## Recommendation

Changes requested for slice 2: fix the new Major 1 (keep the held hint across a failed pass and record a held boot
error on its Revision, with a test). The loop-1 findings are all resolved and their fixes are pinned by killed
mutants. No status is advanced: this is a slice review.

## Ledger row

```json
{
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "slice": "S2 per-revision state and wake (loop 2)",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 0,
  "model_attributed": 1,
  "dod_passed": 7,
  "dod_total": 8,
  "report": "docs/reviews/adr-0190-slice2-implementation-claude-opus-5-5-2.md",
  "notes": "S2 loop 2: loop-1 M1 and m1-m3 resolved (heldThenGate on every gate + binding gate via env.gate; per-ref activity; 5 min held idle window; Idle-at-zero test). Both scenario tests fail with the slice reverted for its reason, 3/3 new mutants killed, build/vet/lint green incl Linux, touched pkgs -race ok. New M1 [model] convergeHeld (function.go:1852) clears the held hint before booting and never re-marks it on error, so a transient materialize/schedule/Create error leaves the held Revision Deploying with no worker and no retry (probe test), and failPass writes the held error onto the Function's status."
}
```
