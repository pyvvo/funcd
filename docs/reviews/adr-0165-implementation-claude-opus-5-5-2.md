# ADR-0165 implementation review — claude-opus-5-5 (loop 2)

- **ADR**: [ADR-0165](../adr/0165-fn-to-fn-trace-propagation.md), fn-to-fn trace propagation
- **Producing model**: claude-opus-5-5
- **Work reviewed** (`git diff origin/main...HEAD`, one commit, 54291b84 on origin/main 5881659b):
  `internal/workernode/local/{local.go,invoker.go,traceparent_internal_test.go}`, `pkg/funcd/funcd.go`,
  `pkg/funcd/fn_to_fn_trace_e2e_test.go`, comments in `internal/funclog/{span.go,route.go}`, and now `go.mod`/`go.sum`.
- **What changed since loop 1**: `git range-diff` against the loop-1 commit (8c576cf9) shows that the code and tests
  are identical. The commit adds only the two pins (funcd-typescript v0.7.0 → v0.8.0, funcd-python v0.4.0 → v0.5.0),
  and the commit message drops the "the last three need the releases" caveat. The commit was rebased onto the newer
  main, which now has ADR-0147 and ADR-0158 `Implemented`.
- **Verdict**: **pass**. There are no Blockers, Majors or Minors.

## Loop-1 findings

Loop 1 raised no findings. Its "Not scored" items are now in this state:

| Loop-1 item | State now |
|---|---|
| Pins and e2e: `go.mod` still pinned v0.7.0 / v0.4.0, and the e2e scenarios had not been run | **Resolved.** Both pins are bumped. All four e2e scenarios (8 subtests) pass under `-race`, and two mutants show that they bite (below). |
| Docs follow-through: the `Accepted → Reviewing` bump, the F51 row, and the ADR-0101/ADR-0114 back-links | Still pending, by design. This workflow assigns them to the wave's docs PR (process). The ADR still reads `Accepted` on the branch. |
| Wiring coverage note: no shim-local test pins the solo and pool sink/channel wiring | **Resolved.** The `nodejs22`, `python314`, `nodejs22-pooled` and `python314-pooled` subtests each find the `CLIENT` span `call greeter` from the released shim in the stored trace. |
| Test-fidelity note on "no greeter span" in the failed-call scenario | Unchanged, and not a finding. The data plane answers the 422 and the 403 before dispatch, and this ADR does not change that. |

## Verification run

| Check | Result |
|---|---|
| `git status` in the worktree | clean |
| `go mod verify` | all modules verified |
| The pinned releases contain the reviewed shim work (module cache) | v0.8.0: `startClientSpan` in `shim/src/tracespan.ts`, `invoke.ts`, and both bundles; `shim.ts` passes `makeInvoke({ sink: traceSink, … })` and `pool.ts` passes `{ member: spec.name, sink: channel, … }`. v0.5.0: `ClientSpan` in `tracespan.py` and `invoke.py`; `shim.py` builds `_Context(channel)` and `_poolworker.py` passes `channel=_channel`. The remote tags v0.8.0 and v0.5.0 exist. |
| `go build ./...` on darwin and on Linux (`GOOS=linux`) | exit 0 on both |
| `go vet` for `internal/workernode/local`, `internal/funclog` and `pkg/funcd`, and `pkg/funcd` with `-tags e2e`, on darwin and Linux | exit 0 on all |
| `golangci-lint run --build-tags e2e` for the same packages, on darwin and Linux (`GOOS=linux` with the host binary) | 0 issues on both |
| `go test -race -count=1 ./internal/workernode/local/ ./internal/funclog/` on darwin | ok, ok |
| The same in a Linux container (`golang:1.26.4` on colima, worktree piped in by `git archive`), plus `go vet -tags e2e ./pkg/funcd/` | ok, ok, vet exit 0 |
| `TestBrokerForwardsTraceparent` (4 subtests) and `TestScenarioNoHeaderBehavesAsToday` under `-race` | PASS |
| `go test -race -count=1 ./pkg/funcd/` (fast lane) | ok (11.3 s) |
| `go test -race -count=1 -tags e2e -run 'TestScenario(FnToFnJoinsOneTrace\|FailedCallErrorClientSpan\|WorkflowLogsIncludeCallee\|InternalCallSkipsEdgeObserv)$' ./pkg/funcd/` | **ok (10.0 s)**. All 8 subtests pass, and none is skipped, the Python pool included. |
| `just check-hygiene` (only `go.mod`/`go.sum` name a language-module version) | clean |
| `just ci-full`, `go test ./...` and the `fn-to-fn` and `funclog` Lima lanes | not run, as instructed; they belong to the PR gate |

### Mutants (2 of 2 killed by the e2e scenarios)

Loop 1 killed 9 mutants with unit and shim tests. These two `-overlay` mutants check that the new e2e scenarios
also catch a regression of the funcd half.

| Mutant | Killed by |
|---|---|
| `funcd.go`: the internal chain takes `edgeObserv` back (`…, gateway.RequestID, edgeObserv, edgeShape`) | `TestScenarioInternalCallSkipsEdgeObserv`: the edge spans and the access log read `["greeter","front"]`, and the counts read `{"front":1,"greeter":1}` |
| `invoker.go`: the broker never sets `traceparent` (`false && tp != ""`) | all four `TestScenarioFnToFnJoinsOneTrace` subtests (the trace never holds 3 spans) and `TestScenarioWorkflowLogsIncludeCallee` (greeter's line is not in the run's logs) |

## Contracts

The code is unchanged since loop 1, which checked every Contract line by line. The results still hold:
`traceparentHeader`, `traceparentKey`, `withTraceparent` and `traceparentFrom` are unexported in `local.go`. The
`NewHandler` line is verbatim. `proxyInvoker.Invoke` sets the header after `X-Funcd-Namespace`. The internal chain
is `gateway.Chain(dpCore, gateway.Recover(p.logger), gateway.RequestID, edgeShape)`, and the listener chain is
unchanged. There is no config key and no new Go dependency. The `internal/funclog` diff changes only comments.

The shim Contracts now reach funcd through the pins, and the released modules hold the wiring that the Contracts
name (see the verification table). One point from loop 1 is now settled. Decision 5's cut of `status_msg` to
ADR-0168's record bound was deferred in loop 1, because ADR-0168 was not on either shim's main. It is present in
both pinned releases: TS `emitSpan` writes the `CLIENT` record through `boundSpan(rec, bound)`, and Py `_emit_span`
takes the same bound for `ClientSpan` as for the `SERVER` span.

## Review checklist

| Item | Status |
|---|---|
| Both shims, solo and pool, follow Decisions 1, 5 and 6 through the shared emit helper | **Holds.** The D5 cut is now in the pinned releases, and the four join-one-trace subtests show solo and pool for both languages. |
| The broker forwards `traceparent` verbatim via ctx (no header in, none out); `Invoker.Invoke` and the other headers are unchanged | Holds |
| No new exported Go name; the internal chain has no `edgeObserv`; the listener chain and `internal/funclog` decoding are unchanged; `go.mod` changes only the two language pins | **Holds.** The `go.mod` diff is the two pin lines, and the `go.sum` diff is their four hash lines. |
| Each scenario has one named, passing test; ADR-0101 and ADR-0114 get only the back-link; the F51 row matches | Scenario half: **holds**, because all five named tests pass. Docs half: pending in the wave's docs PR (process). |

## Scenarios

| Scenario | Test | Result |
|---|---|---|
| fn-to-fn-joins-one-trace | `TestScenarioFnToFnJoinsOneTrace` (`nodejs22`, `python314`, `nodejs22-pooled`, `python314-pooled`) | **PASS** under `-race` |
| failed-call-error-client-span | `TestScenarioFailedCallErrorClientSpan` (422 input contract, 403 undeclared alias) | **PASS** |
| workflow-logs-include-callee | `TestScenarioWorkflowLogsIncludeCallee` | **PASS** |
| internal-call-skips-edge-observ | `TestScenarioInternalCallSkipsEdgeObserv` | **PASS** |
| no-header-behaves-as-today | `TestScenarioNoHeaderBehavesAsToday` | **PASS** (darwin, and Linux in a container) |

## Not scored

- **The pins also carry ADR-0168's shim half** (process; no finding). The funcd-typescript v0.8.0 changelog also
  lists "bound each log record and write the listening line to stdout", and the funcd-python v0.5.0 changelog lists
  "bound each log record and line-buffer stdout". These releases are the only tags that hold this ADR's `CLIENT`
  span, so Decision 7 (pin after the releases) cannot be met without them. ADR-0168's rollout asks for "both tags
  pinned in one PR, after ADR-0158's funcd PR". ADR-0158 is `Implemented` on this base, so that order holds. No
  non-test funcd code reads the shim's listening line, so moving that line to stdout does not affect this PR, and the
  e2e run confirms it. ADR-0168's funcd PR sets the same two pin lines. Whichever PR merges second carries an
  identical `go.mod`/`go.sum` hunk, so a rebase resolves it trivially.
- **Docs follow-through** (process): the `Accepted → Reviewing` bump, the F51 row, and the back-links in ADR-0101
  and ADR-0114 belong to the wave's docs PR, as in loop 1. This gate stamped nothing and edited no doc.
- **PR gate** (process): `just ci-full` and the `fn-to-fn` and `funclog` Lima lanes run at the PR gate.

## ✅ Verified correct — keep it

- The pin commit is minimal. The range-diff shows that only the two pin lines, their `go.sum` hashes and the
  commit message changed. The code that loop 1 reviewed and mutation-tested has not moved.
- The e2e scenarios are real regression guards. Restoring edge observ on the internal chain fails the edge-signal
  test on all three signals. Dropping the broker's header fails the trace join in every runtime and pool mode, and
  it also fails the workflow-logs scenario.
- `TestScenarioInternalCallSkipsEdgeObserv` filters to the edge signals by scope, message and metric name. A
  mutant therefore shows exactly the extra `greeter` entries, with no noise from other telemetry.
- The pooled Python subtest runs instead of skipping, so the ADR-0158 pool socket and `_poolworker.py`'s
  `channel=_channel` are exercised end to end.
- The commit message states the pins and lists every named test, with the house identity and `Refs #86`.

## Recommendation

**Pass.** Loop 1's only open process item, the pins and the first e2e run, is resolved. Both releases are pinned,
`go mod verify` passes, and the build, vet and lint are green on darwin and Linux (vet and lint also with the e2e
tag). The race tests of the touched packages pass on darwin and in a Linux container. All five scenarios have
passing named tests, with all four join-one-trace modes running, and both e2e-level mutants are killed. Next step:
run the PR gate (`just ci-full`, plus the `fn-to-fn` and `funclog` lanes). Then the wave's docs PR moves the ADR to
`Reviewing` and then to `Implemented`, advances the F51 row, and adds the ADR-0101 and ADR-0114 back-links. Switch
the commit or PR trailer to `Fixes #86` at merge, as Implementation plan step 4 says.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0165",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 5,
  "dod_total": 6,
  "report": "docs/reviews/adr-0165-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: code unchanged since loop 1 (range-diff), adds only the funcd-typescript v0.8.0 / funcd-python v0.5.0 pins; go mod verify ok; build/vet/lint green darwin+Linux incl. e2e tag; -race ok for local+funclog (darwin and Linux container) and pkg/funcd fast lane; all 4 e2e scenarios pass under -race (8 subtests, Python pool not skipped) + NoHeader; 2/2 overlay mutants killed by e2e (edgeObserv back on internal chain; broker drops traceparent); D5 status_msg cut now present in the pinned shims; pins also carry ADR-0168's shim half (process note, order OK); pending: docs PR (Reviewing bump, F51 row, back-links) and PR gate (ci-full, fn-to-fn/funclog lanes) (process)"
}
```
