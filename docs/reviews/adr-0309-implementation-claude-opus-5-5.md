## Verdict: pass — 0 blockers, 0 majors  (ADR-0309 implementation, model: claude-opus-5-5)

Reviewed the implementation commit `e85a7e62`, on the ADR-0308 stamp `9b063daa`. The review ran in the parent session
of the batch, as the decider chose for this batch's reviews, and changed none of the work. The commit ships in the
provider-lifecycle batch PR, where the gate and the Lima lanes run once.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 · Consequences' numbers are the experiment's** · attribution: `adr`. The ADR measured before ADR-0308 and with
  the route hook. Measured here with `scripts/agent/audit.golangci.yml`, `b88079a2` → `e85a7e62`: `buildControlPlane`
  384 → 245 statements, cognitive complexity 158 → 82, cyclomatic 121 → 71; production +816 −685 (net +131). The
  direction and size match; the frozen ADR stays as written.

### ✅ Verified correct (keep it)

- **Review checklist.**
  - Only `eventingProvider` holds a `*Platform`; the Executor, kv, blob, sql, lake and invoke hold the config and their
    parts, and their hooks read only those.
  - Each `build` takes a typed `<name>Deps`. `New` builds kv, blob, sql and lake in list order, then invoke, then
    registers the quotas and builds the dependency check from the providers' hooks, then the Executor, the workload
    core and `captureLogs` (`pkg/funcd/funcd.go`, New). That order closes the judge's Blocker: the link check keeps its
    policy decision point.
  - The only `quotas.Register` call is in `registerQuotas` (`pkg/funcd/providers.go`); blob and sql no longer take
    the quota book.
  - Every provider is listed, holding the config, before the options run; the stop hooks check their fields, and kv's
    `Stop` closes the config's KV driver whether or not `build` ran.
  - The Executor's `BeforeStop` closes the runtime, then the listeners; its `Stop` removes only the directory its
    `build` made.
  - No Platform field is left for a part that moved, the four ADR-0308 fields (`kvFacade`, `blobFacade`, `sqlFacade`,
    `pdp`) included.
  - `internal/workernode/local` is unchanged; the Executor calls `NewManager` with the kv and blob facades, and sql
    and secrets through `WithSQL` and `WithSecrets`, as main did.
  - ADR-0305's order tests change their field paths only.
- **Scenario and order tests**, run here with `scripts/agent/d go test -race -count=1`:
  `TestIssue94_FailedNewReleasesResources`, `TestIssue94_AFailedNewClosesTheOptionsKVDriver`,
  `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop`, `TestScenarioBoundFunctionReads`,
  `TestDependencyCheck_AForbiddenLinkFails`, `TestShutdown_ClosesTheLocalAPIListeners`,
  `TestIssue330_TempInvokeSocketDirRemoved`, `TestBuild_TheLakeEngineGetsTheKeysOfBlobsDeriver`,
  `TestShutdown_ClosesLakeProxiesBeforeTheRuntime`, `TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` and
  `TestPhases_ListTheProvidersWithTheirHooks` pass. The two e2e Scenario tests ran in the implementer's `-tags e2e`
  run and run again in the gate's `ci-full`.
- **Mutants.** Run here, restored: lake's `BeforeStop` leaving its proxies open is caught by
  `TestShutdown_ClosesLakeProxiesBeforeTheRuntime`. The implementer's eight: seven caught, among them invoke built
  after the Executor (only by `TestDependencyCheck_AForbiddenLinkFails`) and kv's `Stop` skipping an unbuilt provider
  (only by `TestIssue94_AFailedNewClosesTheOptionsKVDriver`). The survivor closes the listeners at the end of the
  Executor's `Stop`: by then every worker has stopped, so no test can tell, and the checklist line, checked here by
  reading, is its only guard, as the ADR's plan allows.
- **Notes, no change needed.** The blob facade is now built whether or not a blob substrate is set; main's nil branch
  was dead, since `validate()` refuses a nil blob. The diff-scoped audit passed with no hard flag.

### Definition of Done

17 items: the ADR's eight Review-checklist items, its "Done when" and the eight generic items that apply. All hold,
with one in progress: the gate, the 13 Lima lanes and the app-todo lane run once on the batch branch, by the decider's
plan, and the batch PR opens only after they pass.

### Model scorecard

Recorded in the batch's ledger commit: claude-opus-5-5 on ADR-0309 (implementation) → pass, 0/0/1, 0
model-attributed.

### Recommendation

Stamp ADR-0309 `Implemented` and F223 `implemented`.
