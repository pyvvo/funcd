## Verdict: pass — 0 blockers, 0 majors  (ADR-0305 implementation, model: claude-opus-5-5)

Reviewed on branch `claude/adr-0305-impl-80d41b` at `f815a789`: four commits on main `248c4c58` (`d0784838` the
implementation, `a70eb0a2` the tests, `fd04f75c` doc comments, `f815a789` the `Reviewing` stamp). The reviewer ran the
checks named below and changed none of the work.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **m1 · The lifecycle docs and one test comment lag the code** · attribution: `model`. The `Provider` doc
  (`internal/lifecycle/lifecycle.go:17`) lists Starter, BeforeStopper and Stopper but not `Runner`: `fd04f75c` fixed the
  same gap in the package comment only. The `Phases` doc (`lifecycle.go:49`) says that once stopping has begun, `Start`
  starts nothing more; `Run` also starts nothing more then (`lifecycle.go:83-88`,
  `TestPhases_RunAfterStopStartsNothing`). The comment on `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop`
  (`pkg/funcd/start_stop_order_internal_test.go:303-305`) says that the cold call starts no worker because the
  controller has stopped. The test asserts the actual reason, the 503 from the activator, which has stopped. `Hooks`
  also renders a provider with no hook as `secrets: ` with a trailing blank (`lifecycle.go:150`) in the boot log. Fix:
  one comment-only commit; the rendering is optional polish.
- **m2 · Decision 5 lists only the steps that cross the runtime's Close, not every order change** · attribution: `adr`.
  This covers the implementer's gaps 1 and 3. The Executor is first in the list, providers stop in reverse
  (Decisions 3 and 4), and the Contracts table puts the S3 gateway in blob's `Stop` and the fence, the egress gateway
  and the DNS forwarder in the Executor's `Stop`. So the S3 gateway now stops before the fence comes down and before the
  egress gateway and the DNS forwarder stop. On main, the fence went first, then the three services ended together,
  then the egress gateway closed, then the S3 gateway. The design model orders it the same way: `S3Stop` runs before
  the Executor's `Stop` (`docs/design/platform/start-stop.fizz`). At boot, the same Contracts start the egress gateway
  and the DNS forwarder right after the fence, before the KV mark, the S3 gateway and the loops. They also run the KV
  reclaim before the S3 gateway binds. **None of this is a code defect.** Every stop step still runs after the
  runtime's Close, so the rule that what a worker uses stays until the runtime stopped every worker holds
  (`TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` passes). The fence's `Remove` does not read its
  context (`internal/network/nftables_linux.go:94`), so waiting behind the S3 gateway cannot cut it. No worker runs
  before the loops start, so the earlier egress services change nothing a worker sees. A failed start stops them in
  the Executor's `Stop`, which runs for every provider (`TestPhases_AFailedStartStillStopsEveryProvider`). The KV
  reclaim neither needs nor feeds the S3 gateway. ADR-0307 (#1369, accepted) moves the reclaims after every `Start`,
  as on main. The gap is in the wording: Decision 5 and the Constraints say that "the four steps that move are
  listed", but they do not say that the list order also reorders the steps inside the post-Close tail and at boot.
  The next ADR that touches the stop or boot order should say so. Review-checklist item 6 holds as Decision 5 defines
  a move (a step that crosses the runtime's Close or `Stop`).
- **m3 · Decision 5's argument for the invoke listeners names the wrong mechanism** · attribution: `adr`. This is the
  implementer's gap 2. Decision 5 says that a stopping worker's call to another function "only records a wake in the
  store (ADR-0016), which the stopped controller never acts on". In fact, Run's drain has already stopped the activator:
  `Activator.Run` defers `halt()` (`internal/activator/activator.go:752`), and `activate` then refuses at once with
  `errStopped`, an Unavailable fault (503), before it drives a wake (`activator.go:335`). No wake is recorded. The
  conclusion, that no worker starts, holds and is tested: the new test asserts the 503 and that the runtime gets no
  Create or Start after its Close began. The real behavior is stricter than the ADR's text, because no wake waits in
  the store for the next boot. No code change is needed.

The implementer's gap 4 (an explicit `Shutdown` while `Run` still runs) is not a gap of this ADR. No production caller
does that, and on main the controller could already create workers in that window.

### ✅ Verified correct (keep it)

- **The frame (Contracts).** `internal/lifecycle` matches the Contracts block: the five interfaces, `New`, `Start`,
  `Run`, `BeforeStop`, `Stop`, `Providers` and `Hooks`, with errors wrapped as `start <name>`, `before stop <name>` and
  `stop <name>` and their kind kept. It imports `context`, `errors`, `slices`, `strings`, `sync` and `api/fault` only
  (`go list`), and has no `init()`. `stopEach` sets the stopping flag under the mutex before it walks in reverse, and
  `Run` checks that flag under the same mutex, so the runners start either all or none.
- **One list, Executor first, nothing wrapped (Decisions 2 and 3).** `New` builds the list once
  (`pkg/funcd/funcd.go:588-590`, the only `lifecycle.New` call outside tests), before any option runs, so a failed New
  still closes the runtime (#94). Every provider asserts each hook it has and `describer` at compile time, and the
  golden test `TestPhases_ListTheProvidersWithTheirHooks` matches the Contracts table line for line.
- **Shutdown (Decision 5).** The four listed moves are exactly these: the SQL feed's port (sql's `Stop`), the invoke
  listeners (after `BeforeStop`), the KV driver (kv's `Stop`) and the pool manifests (after `Stop`). The Platform's
  own closes keep main's order: bus, gateway, blob, store, workflow runs, the event store, the invoke socket directory
  and telemetry last. `drainTimeout` and `closeTimeout()` are unchanged. The Executor closes the runtime once
  (`closeOnce`), and its `Stop` keeps the fence when that Close failed without reporting the error twice. The
  defensive Close in `Stop` keeps `RuntimeClosedAtExit` true even with no `BeforeStop`: under mutant M2 below,
  `TestIssue94_FailedNewReleasesResources` still passes.
- **Boot (Decisions 4 and 8).** Run logs `platform provider hooks` before `Start`. A failed Start aborts Run with an
  error that names its provider, and a Start that the stop signal cuts is a clean stop (`err != nil && ctx.Err() ==
  nil`). The volume reclaim stays in Run after the providers' `Start`, as the Contracts table leaves it. Every goroutine
  of main's Run is accounted for in a provider's `Start` or `Run` or in the Platform's own loops, and `funcd.go`
  starts 3 goroutines inline instead of 19.
- **Catalog (Decision 7).** `providerCatalog` draws the entries from the providers plus the Platform's ingress entry.
  `TestCatalog_NamesEveryFunctionBinding` walks every field of `v1.FunctionBindings`, and `config`, `sql` and `secret`
  are now named. The catalog sorts by name, so its log and its readers see the same order as before.
- **Order rules unchanged.** All 14 *Order rules* tests exist on main and on the branch. The branch removes no line
  from any existing test file and adds only `// scenario:` and `// oracle:` lines to existing tests.
- **Scenarios and oracles.** Each Scenario's named tests and each Design-model oracle test carry their marker lines.
  The 19 `pkg/funcd` tests of the Scenarios and Order rules and the 8 `internal/lifecycle` tests pass with `-v`; none
  is skipped, and none takes more than 0.32 s. The new `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` proves the
  KV write (204), the refused cold call (503) and no late Create or Start.
- **Mutants (the reviewer's own; each was restored by copying the original back, and `git diff` was clean after
  each).** M1, `stopEach` walks in list order: 4 `TestPhases_*` tests and
  `TestShutdown_ClosesLakeProxiesBeforeTheRuntime` fail. M2, the Executor's `BeforeStop` does nothing:
  `TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` fails at `funcd_test.go:302`, and the new test
  fails at `start_stop_order_internal_test.go:345` (the KV write cannot reach the local API). M3, the fence goes
  although the runtime's Close failed: `TestShutdown_KeepsEgressFenceWhenRuntimeCloseFails` fails. M4, kv's `Start`
  skips its reclaim: `TestIssue708_DeletedKVStoreDataReclaimedOnRestart` fails.
- **Checks run by the reviewer on `f815a789`.** `scripts/agent/d go build ./...` exit 0. `go vet` exit 0 on
  `./internal/lifecycle/` and `./pkg/funcd/`, also with `-tags e2e`. `golangci-lint run` on both packages reported 0
  issues, also with `--build-tags e2e`. `go test -race -count=1 ./internal/lifecycle/ ./pkg/funcd/` exit 0
  (lifecycle 1.3 s, pkg/funcd 45.9 s). `just check-specs` exit 0: 14 specs, 0 failing; `start-stop.fizz` passes
  (58 states, 0.3 s), each of its 6 mutants fails its assertion, and none of its assertions lacks an oracle test.
- **Checks reported by the implementer, verified from what is visible.** `scripts/agent/gate.sh` ran on `747a7828`.
  Outside `docs/`, its tree differs from `a70eb0a2` only in `blueprint.md`, from main's ADR-0306 docs (#1368). Its
  logs show the audit PASS with 0 hard flags, `go test -tags e2e ./pkg/funcd/...` ok in 396 s with no FAIL line,
  Linux lint 0 issues, the Linux tests ok, the stress run of 24 tests × 20 runs with 0 failed, and an empty
  tree-unchanged log. The one masking flag, a discarded error at `executor.go:94`, is main's fence `Remove` line,
  moved unchanged. The comment-only `fd04f75c` came after the gate, and the reviewer's own build, vet, lint and race
  run cover it. `just lima-example-all` (13/13) and `just lima-example app-todo` (50/50) ran on `68a5676b`, which
  likewise differs from `fd04f75c` only in docs and `blueprint.md`. No lane log of this branch was available to the
  reviewer, so these two results stand as reported. In the boot-and-stop timing (5 rounds of 20 runs), the stop
  medians differ in both directions, and boot is 0.007–0.029 ms (at most 4.6 %) slower in 4 of the 5 rounds. The
  reviewer reads both as within noise at a load of 4 to 5, as the ADR asks.
- **Conventions.** No `any`, `panic`, global or logging import other than `log/slog`; errors go through `api/fault`;
  every hook takes `ctx` first. No new dependency, config key, flag or API field; `go.mod` is unchanged.
- **Tracking.** ADR-0305 went `Accepted → Reviewing` with only its Status line changed, F219 reads `reviewing`, and
  ADR-0082 carries its `Superseded in part by` line. F219 is a refactor row with no feature issue, so there is no
  issue to close and no card to move.

### Definition of Done

18 / 18 items hold: the ADR's eight Review-checklist items, its *Done when* (gate, 13 lanes, app-todo and timing), and
the nine generic items that apply. The contract-suite item does not apply, because this ADR adds no port. Misses: none.
m2 and m3 are wording gaps in the ADR, not misses of the code.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0305 (implementation) → pass, 0/0/3, 1 model-attributed, DoD 18/18. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp ADR-0305 `Implemented` and F219 `implemented`. m1 is a comment-only follow-up for the builder. m2 and m3 go to
the next ADR that touches the boot or stop order.
