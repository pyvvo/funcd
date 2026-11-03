## Verdict: pass — 0 blockers, 0 majors  (ADR-0307 implementation, model: claude-opus-5-5)

Reviewed at `d236944c`: the implementation commit `261d4304` and the `Reviewing` stamp, on main `25317415` (#1371).
The review ran in the parent session of the batch, as the decider chose for this batch's reviews, and changed none of
the work. The commits ship in the provider-lifecycle batch PR, where the gate and the Lima lanes run once.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 · The ADR says a Scenario pins an order that none of its cited tests pinned** · attribution: `adr`. Risks says
  that scenario `deleted-data-quarantined-at-boot` pins "every reclaim runs before any controller". The implementer's
  mutant M6, which moves the reclaim after the controller and the loops start, survived all four cited tests: each
  applies the same-name store only after `start` returns. The implementation adds `TestRun_ReclaimsBeforeAnyController`,
  which catches M6, so the code is pinned. The ADR is frozen and stays as written.

### ✅ Verified correct (keep it)

- **Contracts.** `lifecycle.Reclaimer` embeds `Provider` and adds `Reclaim(ctx) error`; `Phases.Reclaim` runs every
  `Reclaimer` in list order through `forEach`, the walk it shares with `Start` (first error for `Start`, every error
  joined for `Reclaim`), wraps each error `reclaim <name>` with its kind, and checks `isStopping()` before each
  provider. `Hooks` names `reclaim` between `start` and `run`.
- **Review checklist.**
  - `Reclaimer` is asserted at compile time by kv, blob and sql only (`pkg/funcd/binding_providers.go`), and the golden
    test lists `kv: start, reclaim, stop`, `blob: start, reclaim, stop`, `sql: reclaim, run, stop`.
  - `Run` calls `Phases.Reclaim` once, in the branch that is not held, after `Phases.Start` and before the Route table
    load, the first probe, `phases.Run` and the loops (`pkg/funcd/funcd.go`, Run).
  - No provider's `Start` reads the hold: kv's `Start` keeps the KV mark, blob's the S3 gateway, and sql has none.
  - A failed reclaim logs one `reclaim of deleted resources incomplete` warning, checked against the context once,
    and the boot goes on; the held branch, which books the Function volumes, is unchanged.
- **Scenario tests**, run here with `scripts/agent/d go test -race -count=1`: `./internal/lifecycle/` ok;
  `TestIssue708_DeletedKVStoreDataReclaimedOnRestart`, `TestPhases_ListTheProvidersWithTheirHooks`,
  `TestRun_AFailedReclaimWarnsOnce`, `TestEveryRunnerConsultsHold`, `TestEveryRunnerConsultsHoldInNamespace`,
  `TestScenarioSQLAndVolumeQuarantined`, `TestScenarioSameNameStartsEmpty`, `TestScenarioOrphanQuarantined`,
  `TestScenarioDeleteReclaimsEngineAndDataAfterHold`, `TestIssue1204_LeftoverEmptyDirsGoOnceTheHoldLifts` and
  `TestScenarioCrashDuringMove` all pass. Each Scenario has its `// scenario:` lines.
- **Mutants run here**, each restored: `Reclaim` passing `first = true` (a failure stops the walk) is caught by
  `TestPhases_ReclaimRunsEveryReclaimerInOrder`; a `Phases.Reclaim` call added before the held check is caught by
  `TestEveryRunnerConsultsHold`. The implementer's six mutants (held, after a stop, the stop read once, a failure that
  stops the walk, no warning, after the loops) were all caught, M6 only by the added test.
- **Gate**, from the hand-over: `GATE PASS` on `261d4304` in 1087 s (audit 0 hard flags, ci-full 845 s, linux-tests
  47 s, 12 changed tests stress-run 20 times with no failure).
- **Notes, no change needed.** sql's reconciler still returns early while held (`internal/services/sql/reconcile.go`),
  a guard older than this ADR and inside what ADR-0288 owns. `TestPhases_ReclaimRunsEveryReclaimerInOrder` checks more
  than the experiment's version: two failures joined, and a stop that begins in the middle of the walk.

### Definition of Done

16 items: the ADR's six Review-checklist items, its "Done when" and the nine generic items that apply. All hold, with
one in progress: the 13 Lima lanes and the app-todo lane of "Done when" run once on the batch branch, by the
decider's plan, and the batch PR opens only after they pass.

### Model scorecard

Recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0307 (implementation) → pass, 0/0/1, 0
model-attributed.

### Recommendation

Stamp ADR-0307 `Implemented` and F221 `implemented`. m1 needs no superseding ADR: the added test pins what the ADR
claimed.
