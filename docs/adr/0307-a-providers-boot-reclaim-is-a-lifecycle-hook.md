# ADR-0307: A provider's boot reclaim is a lifecycle hook

- **Status**: Accepted (2026-11-03, under the decider's delegation: "pick whatever ADR you want and ship them";
  judged twice under the adr-judge skill: round 1's Major, the Scenario tests, and five Minors folded; round 2 accept)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, lifecycle, disaster-recovery, refactor
- **Realizes**: [FEAT-0001/F221](../feat/0001-feat-v1.1.md) (each provider's boot reclaim in one place)
- **Supersedes (in part)**: [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) — the "unless held,
  reclaim deleted … (warn)" steps of its Contracts table's kv, blob and sql `Start` rows: they become `Reclaim` hooks
- **Relates to**: [ADR-0288](0288-a-boot-reclaim-quarantines-data-no-object-names.md) (the reclaims' rules: quarantine,
  crash-safe moves) · [ADR-0206](0206-restore-and-held-boot.md) Decision 6 (a held platform reclaims nothing) ·
  [ADR-0262](0262-function-owned-volumes.md) Decision 8 (volumes) · #708 ·
  [ADR-0302](0302-every-call-to-a-worker-runs-one-chain-of-invoke-plugins.md) (the hook idiom)

## Context & Need

ADR-0305 put each binding provider's boot steps in its `Start`. Three of them, kv, blob and sql, carry the same three
steps: return if the platform is held, reclaim what resources deleted before their reconcile ran still hold, and log a
failure as a warning. The hold check is copied three times, and a new provider that keeps data would copy it a fourth.
ADR-0305 left "shared hooks (reclaim and hold, …)" as an open question. An unmerged experiment (2026-11-02) built a
reclaim hook on ADR-0305's frame; the gate, the 13 Lima lanes and the app-todo lane passed on it.

Purpose: one place that decides when a provider reclaims, so each provider keeps only what it reclaims. Callers: the
Platform's `Run`, once per boot.

## Scenarios

- **scenario: deleted-data-quarantined-at-boot** — *Given* a KVStore, a Bucket and a SQLDatabase deleted before their
  reconcile ran, *When* the daemon starts and is not held, *Then* their data moves into the quarantine before any
  controller runs, and a store applied again under the same name starts empty. Tests:
  `TestIssue708_DeletedKVStoreDataReclaimedOnRestart`, `TestScenarioOrphanQuarantined`,
  `TestScenarioSameNameStartsEmpty`, `TestScenarioSQLAndVolumeQuarantined`.
- **scenario: a-held-boot-reclaims-nothing** — *Given* a held platform, *When* it starts, *Then* no reclaim runs until
  the first start after the release, and the Function volumes are booked, not reclaimed. Tests:
  `TestEveryRunnerConsultsHold`, `TestScenarioDeleteReclaimsEngineAndDataAfterHold`,
  `TestIssue1204_LeftoverEmptyDirsGoOnceTheHoldLifts`.
- **scenario: a-failed-reclaim-warns-and-boots** — *Given* a reclaim that fails, *When* the daemon starts, *Then* it
  logs one warning that names the provider, the other reclaims still run, and the boot goes on. Tests:
  `TestPhases_ReclaimRunsEveryReclaimerInOrder`, `TestScenarioCrashDuringMove`, `TestRun_AFailedReclaimWarnsOnce`.
- **scenario: a-stop-during-boot-reclaims-nothing-more** — *Given* the stop phases began during boot (`BeforeStop` or
  `Stop`), *When* `Phases.Reclaim` runs, *Then* no further provider reclaims. Test:
  `TestPhases_ReclaimRunsEveryReclaimerInOrder`.

## Scope

**In**: the `Reclaimer` hook and `Phases.Reclaim`; when the Platform runs it; kv, blob and sql as reclaimers.

**Out**: what each reclaim does and how it quarantines (ADR-0288, unchanged); the Function volumes' boot step, which
runs held or not (held, it books the volumes instead, ADR-0262 Decision 8) and so stays a Platform step; the other
shared hooks (backup, quota, restore, health, events, dev), which an experiment is studying now.

## Constraints & Decision drivers

- Behavior-preserving: the same reclaims, their ADR-0288 rules and quarantine, the hold of ADR-0206 Decision 6, and,
  as on main, every reclaim after every `Start` and before any controller or loop runs (#708). Two log lines change:
  the three reclaim warnings become one, and it checks the context once, after every reclaim ran, so a kv failure
  followed by a stop signal during blob's reclaim is no longer logged.
- The idiom of ADR-0302 and ADR-0305: an optional one-method interface found by a type assertion; no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep the three reclaims in `Run`'s held branch, beside the volumes' step (main today) | The cheapest, but each new provider that keeps data would edit `Run`, against ADR-0305's wiring in one place. |
| Keep the reclaims in each `Start` (ADR-0305 as written) | Three copies of the hold check and the warning; a new data provider copies them again. |
| `Start` takes the hold as a parameter | Every provider's `Start` would learn the hold, though only three reclaim; the hook keeps the hold in one place. |
| One hook for the whole hold family (reclaims, backups, runners) | The other families are not built or measured yet; an experiment studies them, and a later ADR can add hooks beside this one. |
| Make the volumes' step a reclaimer too | It runs while held (it books the volumes then), so it does not fit a hook the hold skips. |

## Decision

1. **The hook.** A provider that reclaims at boot implements `lifecycle.Reclaimer`: `Reclaim` reclaims what its
   provider's resources deleted before their reconcile ran still hold, under ADR-0288's rules.
2. **The phase.** `Phases.Reclaim` runs every `Reclaimer` in list order, past a failed one, joins the errors with each
   provider's name, and starts nothing more once the stop phases have begun (`BeforeStop` or `Stop`, ADR-0305
   Decision 4). A stop signal only cancels the context, which each reclaim honors, as on main.
3. **When.** `Run` calls `Phases.Reclaim` once, after `Phases.Start` and before any controller or loop runs, and only
   when the platform is not held (ADR-0206 Decision 6). A failure is one warning, and the boot goes on.
4. **Who.** kv reclaims the KVStores (#708), blob the Bucket prefixes (ADR-0199 Decision 7), sql the engine
   directories (ADR-0232 Decision 7), each as ADR-0288 now words it. Their `Start` keeps only its fatal step: kv marks
   its stores, blob serves the S3 gateway, and sql has none left.
5. **The volumes.** The Function volumes' boot step stays a Platform step after `Phases.Reclaim`, as today: it
   reclaims when the platform is not held and books when it is.
6. **The boot log** names the hook: `kv: start, reclaim, stop`.

## Design model

None: `Reclaim` adds one boot step between `Start` and the loops but no state the stop path reads, and a stop that cuts
it leaves the rest for the next start that is not held, where ADR-0288 Decision 3 keeps each move crash-safe
(its model, `docs/design/storage/quarantine.fizz`, covers a held start and a cut move). ADR-0305's model of the start
and stop, [docs/design/platform/start-stop.fizz](../design/platform/start-stop.fizz), is unchanged.

## Temporary workarounds

None.

## Contracts

```go
// internal/lifecycle
type Reclaimer interface {
	Provider
	Reclaim(ctx context.Context) error // at boot, after every Start; never while the platform is held
}

func (ph *Phases) Reclaim(ctx context.Context) error // list order; errors joined, "reclaim <name>: …"
```

| Provider | `Start` keeps | `Reclaim` |
|---|---|---|
| kv | mark the KV stores (fatal) | the KVStores deleted before their reconcile ran (#708) |
| blob | serve the S3 gateway until bound (fatal) | the Bucket prefixes no Bucket names |
| sql | — | the engine directories no SQLDatabase names |

| Dependencies & I/O | |
|---|---|
| Consumes | the providers' reconcilers and the hold marker, as today |
| Exposes | the hook; the boot warning `reclaim of deleted resources incomplete`, with each provider's error |
| Config keys, dependencies | none new |

## Implementation plan

On top of ADR-0305's implementation, as the experiment built it (commit `41bdf1d9`, unmerged):
1. `internal/lifecycle/lifecycle.go`: `Reclaimer`, `Phases.Reclaim`, and one forward walk shared by `Start` and
   `Reclaim`; `Hooks` names `reclaim`. Test `TestPhases_ReclaimRunsEveryReclaimerInOrder` (order, past a failure,
   joined errors, nothing after a stop).
2. `pkg/funcd/binding_providers.go`: kv, blob and sql gain `Reclaim`, and their `Start` drops the hold check and the
   reclaim; `pkg/funcd/funcd.go`: `Run` calls `Phases.Reclaim` when not held, before the volumes' step.
3. `TestPhases_ListTheProvidersWithTheirHooks` expects `kv: start, reclaim, stop`, `blob: start, reclaim, stop` and
   `sql: reclaim, run, stop`; new `TestRun_AFailedReclaimWarnsOnce`: a blob reclaim that fails leaves exactly one
   `reclaim of deleted resources incomplete` warning, naming `reclaim blob`, and the boot goes on. The other Scenario
   tests pass unchanged, each with a `// scenario:` line.
4. Run `scripts/agent/d go test -race ./internal/lifecycle/ ./pkg/funcd/`, lint, `scripts/agent/gate.sh`,
   `just lima-example-all` and `just lima-example app-todo`.

Done when the gate, the 13 lanes and the app-todo lane pass.

## Review checklist

- [ ] `Reclaimer` is optional and asserted at compile time by kv, blob and sql; no other provider implements it.
- [ ] `Run` calls `Phases.Reclaim` once, after `Phases.Start`, before any controller or loop, and never while held.
- [ ] No provider's `Start` reads the hold, and sql has no `Start`.
- [ ] A failed reclaim warns once and the boot goes on; the others still run.
- [ ] The Function volumes' step is unchanged and still runs when held.
- [ ] The hook golden test lists `reclaim` for kv, blob and sql.

## Consequences

- **Positive**: the hold check lives in one place instead of three, a new provider that keeps data adds one method,
  and the boot log shows which providers reclaim.
- **Negative**: one more hook in the frame; in the experiment it cost 27 more production lines than three inline
  checks.
- **Risks**: a reclaim that must run before another provider's `Start` cannot use the hook; none does today, and the
  scenario `deleted-data-quarantined-at-boot` pins that every reclaim runs before any controller.

## Open questions

- The other shared hooks (backup, quota, restore, health, events, dev) → the shared-hooks ADR, after its experiment.
- The volumes' step as a hook of its own → the same ADR, if the held booking gets one.

## References

- [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) · ADR-0288 · ADR-0206 Decision 6 · ADR-0262
  Decision 8 · #708 · ADR-0302.
