# Review of the ADR-0163 implementation (claude-opus-5-5, loop 1)

- **ADR**: docs/adr/0163-retry-times-in-config.md (Accepted; the status bump and the F31 row move are left to the wave's docs PR, by arrangement)
- **Work**: branch `feat/adr-0163-retry-times-in-config`, one commit `88158cd6` on origin/main; 36 files, +1068 / -115
- **Model**: claude-opus-5-5
- **Verdict**: **changes-requested**. The production code conforms to every Contract and to the preflight's agreed handling. Two key lines can be reverted without any test failing: Decision 8's default retry backoff, and the `runtime.drainPollInterval` bound.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on the 14 touched/wired packages, darwin and `GOOS=linux` | exit 0 / exit 0 |
| `golangci-lint run` on the touched packages, darwin and `GOOS=linux` (host-built linter binary) | `0 issues.` exit 0 / `0 issues.` exit 0 |
| `go test -race -count=1` on cmd/funcd, internal/{controller, eventing, function, gc, platform/config, provider, route, sensor, services/catalog, services/identity, site, workflow}, pkg/funcd | all `ok`, exit 0 (cmd/funcd 19.1 s, internal/workflow 22.1 s, pkg/funcd 16.0 s) |
| `TestIssue341` (cmd/funcd/main_test.go, internal/platform/config/example_test.go) and `TestIssue333` | green inside the race run |
| ADR file and docs/ | untouched by the branch (`git diff --stat origin/main...HEAD -- docs` is empty) |
| `just ci` / e2e | not run here, by instruction. The per-PR gate runs them |

Overlay mutants (`go test -count=1 -overlay`, mutant built from the branch file):

| # | Mutation | Tests run | Outcome |
|---|---|---|---|
| m1 | internal/workflow/engine.go `dispatchStep`: delete `if backoff == 0 { backoff = e.cfg.DefaultRetryBackoff }` | all of internal/workflow; pkg/funcd `-run 'Pacing\|Scenario.*Workflow'` | **survives** (ok / ok) |
| m2 | internal/function/function.go `drain`: `min(r.drainPoll, …)` → `min(time.Second, …)` | all of internal/function; pkg/funcd `-run 'Pacing\|Scenario.*Runtime'` | **survives** (ok / ok) |
| m3 | pkg/funcd/funcd.go sensor wiring: `c.pacing.deliveryBackoffMax()` → `c.pacing.DeliveryBackoffMax` | pkg/funcd `-run 'Pacing\|Scenario.*Eventing'` | killed: `TestScenarioEventingKeysPaceRecheckAndDeliveryRetry` FAIL |

## 🔴 Blockers

None.

## 🟡 Majors

**M1 [model]: Decision 8 (`workflow.defaultRetryBackoff`) has no test of its behavior. Mutant m1 survives.**
The only test, `TestScenarioWorkflowKeysPaceArtifactWaitAndRetry/defaultRetryBackoff` (pkg/funcd/pacing_internal_test.go:205-208), reads `workflowEngine.cfg.DefaultRetryBackoff` through reflection. Deleting the three-line fallback in `dispatchStep` (internal/workflow/engine.go:1126-1128) passes every test in internal/workflow and in pkg/funcd's pacing tests. Decision 8 is the one new piece of logic in the engine. Its scenario states timings ("starts attempt 2 at least 300 ms and attempt 3 at least 600 ms after the previous failure"), and nothing asserts them. The plan allows "the component directly where the platform has no trigger". An engine-level test can do this: a step with no `retry.backoff` that fails twice, `Config.DefaultRetryBackoff` set, and the attempt gaps asserted, plus the `retry.backoff` set case, which must win.

**M2 [model]: the runtime scenario checks wiring only. Its comment claims that internal/function covers the behavior, but `runtime.drainPollInterval` and the solo-path `runtime.bootTimeout` have no behavior test. Mutant m2 survives.**
`TestScenarioRuntimeKeysPaceSupervisionBootAndDrain` (pkg/funcd/pacing_internal_test.go:155-183) asserts only that each value reaches an unexported reconciler field, and says "its pacing behavior is covered in internal/function". internal/function/pacing_test.go covers `ReferentPollInterval` and the pool host's `FUNCD_POOL_LOAD_TIMEOUT_MS`, and nothing else. Reverting the drain bound to the old 1 s constant (m2) passes the whole internal/function package. Nothing with a non-default `BootTimeout` exercises the solo uses of `r.bootTimeout` (`readyReplicas`, `stopNeverReady`, `stopUnlistened`, `notReadyError`). Needed: an internal/function test where a draining revision is re-checked at `DrainPollInterval` (the result of `drain` is at most the set interval), and one where a never-ready solo replica is judged and stopped at a short `BootTimeout`. Otherwise the comment must be corrected and the gap justified.

## Minor

- **m1 [model]: several other scenario subtests read an unexported field by name through reflection rather than exercising the use site.** These are `eventing.bucketRecheckInterval` (`recheck`), `workflow.artifactPollInterval` (`contractRequeue`), and `server.shutdownTimeout`, where only `p.drainTimeout` is asserted, not its uses at funcd.go:392/1297/1304/1379. The use sites are one-token changes, and I verified them by reading. However, a renamed field breaks these tests at runtime rather than at compile time, and the scenarios' timing claims stay unexercised. Driving `Reconcile` and asserting `RequeueAfter` would be cheap for the first two, as the route and WorkflowRun subtests already do.
- **m2 [model]: the brief's extra item was not done.** The "pool host exits at once" subtest in internal/function/pool_test.go (line 594) is unchanged. It still crosses no period and does not assert the restart step (carried over from the ADR-0158 review, minor m2).
- **m3 [model], nit: a stale comment at pkg/funcd/funcd.go:366.** `drainTimeout` is documented as "shutdownTimeout, unless a test exhausts it". It is now `server.shutdownTimeout` and also bounds the workflow-run and Sensor drains and the cleanup after a failed `New`.

## ✅ Verified correct (keep)

- **19 keys, exactly per the Contracts table.** The keys, `json`/`env` tags, groups and placement in internal/platform/config/config.go match the table. There are no `validate` tags. `defaults()` sets each key to today's value and leaves `DeliveryBackoffMax` empty, as ADR-0160 does. No sibling key is redefined. The kept constants have no key: readiness poll, probe timeout, 1 ms floors, 5 ms base, 25 ms wake poll, 1 h cap, and `RestartSec`/`TimeoutStopSec`.
- **The old constants are only zero defaults.** A grep of every replaced constant in internal/ and pkg/ finds each one only in its `<= 0`/`== 0` fallback: `maxBackoff`, `backendRequeue`, `resyncPeriod`, `routeRequeue`, `waitRequeue`, `contractRequeue`, `bucketRecheckInterval`, `retryBaseDelay`/`retryMaxDelay`, `defaultBootTimeout`, `defaultProbeTimeout`. No bare `bootTimeout` constant read remains in internal/function. The ADR-0161 sites (`timedOut`, `stopUnlistened`) and the ADR-0158 sites (`poolSilent`, the load message, the pool env) are all converted, as the preflight asked.
- **Validation (Decision 5).** `pacing()` in cmd/funcd/main.go parses each key through `parseDuration`, with `zeroOK` only for `workflow.defaultRetryBackoff`. It resolves an unset delivery max to `max(10s, initial)` and checks the five orderings, naming the first key with the other bound. `TestScenarioInvalidValueRefusedNamingKey` covers every key with `-1s`, `soon` and `0s`, from both file and env, plus the five orderings, an env ordering, the case where a 20 s initial value alone is accepted, and a `buildOptions` run that refuses before serving. `WithPacing` repeats the checks on effective values (`TestWithPacingRefusesInvalidFields`, including the equality boundary of bootTimeout against activationTimeout). Mutant m3 shows that the effective-max handoff to the sensor is guarded.
- **Decision 7 and ADR-0142 Decision 9.** The catalog engine wait is `min(enginePoll, period)`, tested with 20 s clamped to 10 s. The Function referent gates use `min(referentPoll, period)`, as the preflight recommended, tested in internal/function. `NewMaterializer` and `identity.ReconcilerDeps` receive the supervision period. The one existing assertion that changed (catalog reconcile_test.go `TestScenarioCrashedCatalogEngineRestarts`, `2s` → `period`) follows directly from Decision 7, because that test's period is below 2 s.
- **Shutdown wiring follows the preflight's gap handling.** `p.drainTimeout` is set after the options loop, and the failed-`New` cleanup falls back to 15 s when an option fails. The engine drain, the Sensor retry workers and the HTTP drain all read it.
- **The pool host's load bound** is rounded up to at least 1 ms (`max(r.bootTimeout.Milliseconds(), 1)`), which closes the preflight's gap where the shim would read 0 as invalid.
- **Behavior tests that are real:** the Route and WorkflowRun referent waits, the Route resync, the saturated controller retry gap, the egress sync cadence, the catalog engine and referent waits, the sensor backoff sequence (50, 100, 100 ms), and the pool env.
- **The example file** has one commented line per key, in its group's block, with its default. The `shutdownTimeout` line states the `TimeoutStopSec` coupling. The 47 mechanical test call-site edits append `0` and change no assertions.

## Checklist / Definition of done

| Item | Holds |
|---|---|
| 19 keys per the table; no sibling key redefined; kept constants have no key; old constants only zero defaults | yes |
| Bad values and the five orderings refused (file and env) naming the key; engine requeue ≤ `supervisionPeriod` | yes |
| Each scenario test passes with a subtest per key; F31 updated; no absolute path or user name | **no**: subtests exist and pass, but two keys' behavior is untested (M1, M2). F31 is deferred to the wave's docs PR by arrangement |
| Done when: the tests pass under `-race` on the touched packages and `just ci` is green | yes on the evidence here (race, build, vet and lint on both OSes all exit 0); `just ci` runs in the per-PR gate |

## Recommendation

Loop back to `adr-impl` for M1 and M2. Both are test-only additions; the production code needs no change. Fix m1-m3 in the same pass where it is cheap. Advance nothing: the ADR stays at its current status, and the wave's docs PR moves it only after a passing review.

```json
{
  "date": "2026-10-05",
  "adr": "0163",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 2,
  "minors": 3,
  "model_attributed": 5,
  "dod_passed": 3,
  "dod_total": 4,
  "report": "docs/reviews/adr-0163-implementation-claude-opus-5-5.md",
  "notes": "loop 1: production code conforms to all Contracts + preflight handling; build/vet/lint darwin+linux clean, -race on 14 pkgs ok. M1 [model] Decision 8 defaultRetryBackoff untested (mutant removing the dispatchStep fallback survives internal/workflow + pkg/funcd); M2 [model] runtime scenario is wiring-only, drainPollInterval (mutant survives internal/function) and solo bootTimeout untested despite a comment claiming coverage; m1 [model] reflection-only subtests for bucketRecheck/artifactPoll/shutdownTimeout use sites; m2 [model] brief's pool_test host-exit extension not done; m3 [model] stale drainTimeout comment. Mutant on sensor effective-max wiring killed."
}
```
