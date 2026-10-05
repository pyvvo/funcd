## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0182 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0182-timer-creation-grid`, one commit `e1202e46` on `origin/main` `65386fe0`
(`fix(eventing): keep timer schedules across daemon restarts`, carries `Fixes #713`). Diff: 3 files, all under
`internal/eventing/` (`eventing.go` +26/−7 net, `run_test.go` 2 call sites, new `timer_restart_test.go` 155 lines).

### Verification run (scoped, through `scripts/agent/d`, in the work's worktree)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go test -race -count=1 ./internal/eventing/` | `ok` (1.8s) |
| the 6 Scenario tests + `TestGridFloor` + `TestIssue114_TimerFiresOncePerInterval` + `TestReconcileTimerPurgeErrorKeepsTimer`, `-race -v` | all `--- PASS` |
| `go vet ./internal/eventing/` (darwin, Linux) | exit 0, exit 0 |
| `golangci-lint run ./internal/eventing/...` (darwin; Linux via the tool binary under `GOOS=linux`) | `0 issues.` both |
| `gofmt -l internal/eventing/` | empty |
| tree | clean; nothing outside `internal/eventing/` changed |

No e2e, no `go test ./...`, no Lima (per the run's rules; the repo-wide gate runs once per PR).

### Mutants (`go test -overlay` on `internal/eventing/eventing.go`)

| # | Mutation | Result |
|---|---|---|
| M1 | ADR revert check: seed back to `lastFire: s.clock.Now()` | **killed** — `TestIssue713_TimerScheduleSurvivesRestart` fails with `actual: []time.Duration(nil)`, `expected: [24h 48h]` (0 fires, want 2: the #713 symptom), plus MissedFireSkipped, AddedEvent, IntervalChange, ClockBehindCreation |
| M2 | `gridFloor` truncating division (drop the `k--` round toward −∞; the ADR's rejected alternative) | **killed** — `TestGridFloor`, `TestScenarioClockBehindCreation` |
| M3 | `Reconcile` passes `time.Time{}` instead of `es.CreationTime` (the wiring at the `registerTimer` call) | **killed** — the same five scenario tests as M1 |
| M4 | `Run` loop reads `time.Now()` instead of `s.clock.Now()` | survived — equivalent in production (no caller injects `Clock`; the default is `clock.System()`); see Observations |

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### Observations (not scored)
- **Run-loop clock read is not pinned by a test** (M4 survives). The ADR's plan drives every scenario through
  `dueTimers(now)` directly and asks for no `Run`-level clock test, and the mutant is behavior-equivalent with the
  default clock, so this is not counted against the model. If a later ADR injects `Clock` from `pkg/funcd`, a
  `Run` test on a manual clock becomes worth adding.
- **Tracking deferred by the run's rules.** The ADR still reads `Accepted` and the FEAT-0005 F72 row reads
  "timer grid: accepted"; the `Reviewing`/`Implemented` stamps and the feat row are written by the wave's docs PR,
  so the absent `Accepted → Reviewing` bump is not a finding. The ADR file is untouched on the branch.
- Prove-first evidence: M1 is exactly the plan's step-1 state (seed `s.clock.Now()` with the clock seam in place)
  and reproduces 0 fires (want 2).

### ✅ Verified correct (keep it)
- **Decision 1–2 / Contracts.** `gridFloor(created, now, interval)` (`eventing.go`, new func after
  `registerTimer`) matches the Contracts signature and doc: zero `created` returns `now`; `since / interval` with
  `k--` when `since % interval < 0` floors toward −∞, so the seed is never after now. `registerTimer` has the
  Contracts signature `(ns, source, created time.Time, t)` and seeds only a new or re-intervaled entry; the
  unchanged-interval `continue` that keeps `lastFire` is untouched.
- **Wiring.** `Reconcile` passes `es.CreationTime`; `st.Update` keeps `CreationTime` (`internal/store/store.go`
  copies `curMeta.CreationTime`), which `TestScenarioAddedEventOnCreationGrid` relies on through a real
  `st.Update` + `Reconcile`.
- **Decision 4.** `Deps.Clock clock.Clock` added with an ADR-citing comment; `NewSource` defaults nil to
  `clock.System()`; the seed and `Run` read `s.clock`. No `time.Now` remains in `eventing.go` (grep empty).
- **Decision 5.** `dueTimers` body unchanged (only its `Run` call site's argument changed); no field under
  `api/types`; no store or KV write on the timer path; `pkg/funcd/funcd.go` untouched.
- **No divide-by-zero path.** `gridFloor` divides by `interval`, but `store.Create`/`store.Update` run
  `Validate`, which bounds every timer interval to 100ms–24h (`api/types/v1alpha1/eventsource.go:125`), so a zero
  interval cannot reach `registerTimer` from the store.
- **Scenarios.** One test per scenario, named exactly as the plan's table; each asserts exact fire times as
  offsets from the stored `creationTimestamp`: restart-keeps-schedule (24h, 48h over three 23h lives),
  uninterrupted (24h, 48h), missed-fire-skipped (only 48h; nothing at the 30h boot), added event (`other` 48h,
  72h; `daily` 24/48/72h), interval change (24h then 30/36/42/48h), clock behind creation (−24h, 0, 24h). The boot
  on a grid point (t0) does not fire, covering Decision 3. The harness follows the plan: one
  `store.New(memory.New())`, `st.Create` with resource group `rg1`, t0 read from the stored `CreationTime`, a new
  `NewSource` on `clock.NewManual` per life, one-minute steps through `dueTimers` + `Fire`.
- **`TestGridFloor`** covers all six rows the plan names (equal, later grid point, between, before by a
  non-multiple, before by an exact multiple, zero created).
- **Existing tests unchanged in substance.** `run_test.go` changes only the two `registerTimer` call sites
  (zero creation time, so the seed stays `now` and `TestIssue114` keeps its assertions);
  `TestReconcileTimerPurgeErrorKeepsTimer` passes unchanged.
- **Conventions.** No `any`, no `panic`, no logging change; comments state the why (the −∞ rounding, the ADR);
  the commit message maps every scenario to its test and carries `Fixes #713`.

### Definition of Done
12 / 12 hold (Review checklist 8/8 + plan step 5 DoD 4/4: tests pass; revert check fails as stated; diff only in
`internal/eventing`; `Fixes #713` in the commit for the integrator's PR). Generic DoD: scoped checks green; the
repo-wide `just ci-full` gate is run once per PR by the integrator, not here.

### Model scorecard
Recorded row (below): claude-opus-5-5 on ADR-0182 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 12/12.
The ledger and `docs/reviews/model-scorecard.md` are written by the wave's docs PR.

### Recommendation
Pass. Hand to the integrator in the agreed merge order (after 0181). The ADR stamp `Reviewing → Implemented`, the
F72 row and the board card move belong to the wave's docs PR.

```json
{
  "date": "2026-10-05",
  "adr": "0182",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0182-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (e1202e46 on 65386fe0); all 5 Decisions + Contracts hold (gridFloor floors toward -inf, registerTimer(ns, source, created, t), Deps.Clock default System, dueTimers unchanged, no API field / store write); build/vet/lint clean also Linux; eventing -race ok; 6/6 scenario tests + TestGridFloor pass; mutants 3/4 killed (seed revert = #713 0 fires want 2, truncating division, CreationTime wiring); survivor Run-loop time.Now is production-equivalent, not scored. Tracking stamps deferred to the wave docs PR."
}
```
