# Review: ADR-0192 implementation (claude-opus-5-5), loop 1

- **ADR**: docs/adr/0192-asleep-function-gate-stops-worker.md (Accepted; status and feat row are left to the wave's docs PR)
- **Work**: branch `feat/adr-0192-asleep-gate-stops-worker`, one commit `7484a578` on top of `52521476` (ADR-0185's
  `Reclaimable` commit #783 is an ancestor, so ADR-0192 Decision 2's ordering holds)
- **Files**: `internal/function/function.go` (+84/-12), `internal/function/asleep_internal_test.go` (new),
  `pkg/funcd/issue769_internal_test.go` (new). Matches the ADR's Implementation plan; nothing extra.
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model).

## Verification run (all through `scripts/agent/d`, in the worktree)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/function/ ./pkg/funcd/` (darwin and `GOOS=linux`) | exit 0 / exit 0 |
| `go tool golangci-lint run ./internal/function/ ./pkg/funcd/` | `0 issues.` exit 0 |
| same lint with `GOOS=linux` | `0 issues.` (env: `GOOS=linux go tool golangci-lint` builds the tool itself for Linux and fails with `exec format error`; rerun with a host-built golangci-lint binary under `GOOS=linux`) |
| `go test -race -count=1 ./internal/function/` | `ok … 12.361s` |
| `go test -race -count=1 -v -run 'TestIssue769\|TestScenarioDependencyReturnsStaysAsleep\|TestScenarioMinReplicasOneUnaffected' ./pkg/funcd/` | `ok … 6.085s`; every subtest PASS: `deleted-via-api`, `engine-down`, Secret, `pending`, `failed`, `woken-while-asleep`, min-replicas-one |

### Overlay mutants (`go test -race -overlay`, the work untouched)

| Mutant | Tests run | Result |
|---|---|---|
| m1: `function.go` = `git show origin/main:` (the "prove first" on main with ADR-0185) | `TestIssue769` | FAIL, all 3: `running 1, writes [Ready Idle Degraded Idle Degraded …]` — the #769 loop exactly |
| m2: gateFailed's asleep branch disabled (`if false && r.asleep(fn)`) | `TestIssue769` | FAIL, all 3, same `Idle/Degraded` loop, one worker running |
| m3: `clearAsleep(fn)` removed from `finish` | `TestScenarioDependencyReturnsStaysAsleep` | FAIL, all 3 subtests (`the woken pass clears Asleep`; `Idle/NoReplicas with Asleep=False` never reached) |
| m4: `&& !sleeping` removed from `finish` | `TestScenarioDependencyReturnsStaysAsleep` | FAIL `failed`: `no worker is created before a call` |
| m5: `if r.asleep(fn) { return 0 }` removed from `desiredReplicas` | `TestAsleepDesiredReplicas`; `TestScenarioDependencyReturnsStaysAsleep` | FAIL `pending-asleep: desiredReplicas`; FAIL `failed` only — `pending` survives (Minor 1) |

## Findings

### Minor

1. **(model) The `pending` subtest of `dependency-returns-stays-asleep` cannot see a reconciler boot.**
   `stayAsleep` (`pkg/funcd/issue769_internal_test.go:288-300`) snapshots `created := r.creates()` only after the
   dependency is back, and for the catalog `applyCatalog` (`:108-115`) first waits for `lake` to be Ready, during which
   the Function's decisive pass has already run. Probe (log-only overlay of the test): on the real code the snapshot
   reads `creates 1, phase Idle`; under m5 it reads `creates 2, phase Ready` — the reconciler booted a worker no call
   asked for, idle reclaim then put it back to `Idle`, and the subtest passed. The scenario's "no worker is created" is
   therefore enforced only by the `failed` subtest (the Secret path, where `r.secret` returns before the pass) and by
   `TestAsleepDesiredReplicas`. The implementation is correct; the guard is weaker than it reads. Fix: take the
   `creates()` snapshot before re-applying the dependency and pass it into `stayAsleep`.

## Contracts and Review checklist (against the code)

| Item | Holds | Evidence |
|---|---|---|
| `condAsleep` constant, `asleep(fn)` exactly as the Contract | yes | `function.go:51`, `:835-848` — identical body |
| `gateFailed`: if asleep, stop S and C (C ≠ S), clear Serving/Draining/DrainingSince, `Asleep=True` (`ScaledToZero`, the Contract's message), `running, listening = 0, 0` → default row | yes | `:793-805`, `stopAsleep` `:851-866` mirrors convergeSolo's desired-0 clear (`:1245`); m1/m2 killed |
| `asleep` false for `minReplicas ≥ 1` and a pooled member | yes | `:839`; contract cases `min-replicas-one-*`, `pooled-*` |
| `gateFailed` never writes `Degraded` when asleep | yes | running/listening are 0 on that path; `requireAsleep` asserts no `Degraded` write |
| `Asleep=True` on the asleep row and `False` otherwise, only on change | yes | `Conditions.Set` keeps `LastTransitionTime` on equal status (`api/types/v1alpha1/status.go:35`) and reason/message are constant; `clearAsleep` writes only when `True`; `resourceVersion` holds 3 s in all asleep tests |
| `desiredReplicas` 0 when asleep; ADR-0169's `max(1, replicas)` otherwise | yes | `:1213-1215`; contract `failed` → 2, `replicas: 0` → 1; m5 killed |
| `finish`: `asleep` once at entry before any condition write, clears `Asleep=True` every pass, `holdsFailed && !sleeping` | yes | `:908-911`; m3, m4 killed; `woken-while-asleep` ends `Ready`, `Asleep=False` |
| `finish` writes `Idle/NoReplicas` for an asleep Function whose gates pass; no worker created | yes | `failed` subtest and probe (`creates 1`); see Minor 1 for the `pending` variant |
| Both `TestIssue769_*` fail with the `gateFailed` change reverted and pass with it | yes | m1, m2 |
| Both `TestScenario*` and `TestAsleepDesiredReplicas` exist and pass | yes | run above |
| No config key, phase or gate reason; pooled path of `gateFailed` unchanged | yes | only `condAsleep` added; pooled path differs only by the no-op `clearAsleep` |
| DoD: step-1 tests failed on main (with ADR-0185) and pass with step 2 | yes | m1 |
| DoD: scenario and contract tests pass under `-race` | yes | run above |
| DoD: no config key, phase or gate reason | yes | diff |

## Verified correct (keep it)

- The asleep row is a minimal, contract-exact insertion before `servingWorkers`; the non-asleep table (ADR-0161
  Decision 2) is untouched, and `stopAsleep` reuses `stopRevision` and convergeSolo's clear rather than a new stop path.
- `finish` computes `sleeping` before `clearAsleep`, so the order the ADR requires is enforced, and the comment states
  the why (Decision 3) once.
- Doc comments of `gateFailed` and `desiredReplicas` are rewritten as the plan asks, including the ADR-0169 sentence
  now scoped to "not asleep".
- The scenario rig is real: assembled platform, applies and deletes through the sdk so admission runs, a toggle engine
  for the `engine-down` trigger, a store Watch recording every write's phase, a 3 s `resourceVersion` hold, and the
  Secret scenario asserts the `Failed` fault and the "refused at once" bound.
- `woken-while-asleep` uses a Bucket with a one-minute referent poll so the call's wake is the first pass to see the
  dependency back; the store delete is commented (admission protects a bound Bucket). The gate's requeue is the
  referent poll (`function.go:624`), so the comment holds.
- The contract test covers every row the plan lists plus `deploying-asleep` and the `replicas: 0` floor.

## Recommendation

Pass. The ADR can be stamped `Implemented` by the wave's docs PR. Minor 1 is a test-only hardening the builder may fold
into a later change; it does not block.

```json
{
  "adr": "0192",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0192-implementation-claude-opus-5-5.md",
  "notes": "pass: asleep row, desiredReplicas and finish match the Contracts exactly; 5 overlay mutants all killed (main's function.go reproduces the #769 Idle/Degraded loop); Minor(model): dependency-returns 'pending' subtest snapshots creates() after the decisive pass, so a reconciler boot (m5) survives it; Linux lint via go tool is env-blocked (exec format), rerun with a host binary: 0 issues"
}
```
