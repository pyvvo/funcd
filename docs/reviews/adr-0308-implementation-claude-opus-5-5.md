## Verdict: pass — 0 blockers, 0 majors  (ADR-0308 implementation, model: claude-opus-5-5)

Reviewed at `9b063daa`: the implementation commit `b88079a2` and the `Reviewing` stamp, on the ADR-0307 stamp and the
#1370 fix (merged as #1373). The review ran in the parent session of the batch, as the decider chose for this batch's
reviews, and changed none of the work. The commits ship in the provider-lifecycle batch PR, where the gate and the
Lima lanes run once.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 · Consequences' line count is the experiment's** · attribution: `adr`. The ADR says "143 more production code
  lines". This implementation adds 124 net (+235 −111), because it also removes the typed `CheckerDeps` fields, as plan
  step 2 asks and the experiment did not. Not a code defect; the frozen ADR stays as written.

### ✅ Verified correct (keep it)

- **Contracts.** `quotaHook`, `admissionHook` and `dependencyHook` live in `pkg/funcd/providers.go`, each embedding
  `lifecycle.Provider`, and kv, blob, sql and invoke assert theirs at compile time. `internal/lifecycle` gains `Each`,
  `Named`, `Hook` and `Hooks(shared ...Hook)`, and imports only `context`, `errors`, `slices`, `strings`, `sync` and
  `api/fault`. `internal/health` has `Binding` with `KV`, `Blob`, `SQL` and `Links`; `CheckerDeps` is
  `{Store, Bindings}`.
- **Review checklist.**
  - No per-kind wiring is left: the only `quotas.Register` call is in `registerQuotas`; the count caps and deletion
    protections of KVStores, Buckets and SQLDatabases exist only in the providers' `Admissions`; `internal/health` has
    no per-kind binding loop.
  - `registerQuotas` and the dependency list run once in `New`, after the SQL facade and long after the default KV
    driver is set (#1370). `Admissions` runs in `partAdmissions`, at chain build and on each App write, and only
    builds values.
  - `partAdmissions` keeps the link rules first, then the providers' rules, then the rest, as before. The dependency
    check runs kv, blob, sql, links.
  - The golden test reads the `platform provider hooks` line that `Run` logs and lists every shared hook of every
    provider.
- **Scenario tests**, run here with `scripts/agent/d go test -race -count=1`: `./internal/lifecycle/` and
  `./internal/health/` ok; `TestIssue1370_EveryStoreKindIsCappedOnTheDefaultDrivers`, `TestKVStoreRulesRunOnTheWritePath`,
  `TestIssue58_BucketWithObjectsIsDeletionProtected`, `TestScenarioDatabaseQuota`,
  `TestScenarioDeleteRefusedWhileHoldingData`, `TestDependencyCheck_ReadsTheProvidersInListOrder`,
  `TestDependencyCheckReadsThePolicies` and `TestPhases_ListTheProvidersWithTheirHooks` pass. Each Scenario has its
  `// scenario:` line.
- **Mutants.** Run here, restored: kv's `Admissions` without the KVStore deletion protection is caught by
  `TestKVStoreRulesRunOnTheWritePath`. The implementer's twelve: eleven caught; A-order (the store rules before the
  link rules) survives because no write can observe it, the two sets acting on different kinds; the checklist line,
  checked here by reading, guards it.
- **Notes, no change needed.** The Bucket count cap is pinned only by the e2e `TestScenarioApplyDryRunRefused`, which
  the gate's `ci-full` runs. The four Platform fields `kvFacade`, `blobFacade`, `sqlFacade` and `pdp` are the ADR's
  Temporary workaround; ADR-0309 removes them in this batch.

### Definition of Done

15 items: the ADR's six Review-checklist items, its "Done when" and the eight generic items that apply. All hold, with
one in progress: the gate, the 13 Lima lanes and the app-todo lane run once on the batch branch, by the decider's plan,
and the batch PR opens only after they pass.

### Model scorecard

Recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0308 (implementation) → pass, 0/0/1, 0
model-attributed.

### Recommendation

Stamp ADR-0308 `Implemented` and F222 `implemented`.
