# ADR-0165 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0165](../adr/0165-fn-to-fn-trace-propagation.md), fn-to-fn trace propagation
- **Producing model**: claude-opus-5-5
- **Work reviewed** (`git diff origin/main...HEAD`, one commit per repo):
  - funcd `feat/adr-0165-fn-to-fn-trace-propagation` (8c576cf9): `internal/workernode/local/{local.go,invoker.go,traceparent_internal_test.go}`, `pkg/funcd/funcd.go`, `pkg/funcd/fn_to_fn_trace_e2e_test.go`, and comments in `internal/funclog/{span.go,route.go}`.
  - funcd-typescript (90dc67f): `shim/src/{tracespan.ts,invoke.ts,shim.ts,pool.ts}`, `shim/test/invoke.test.ts`, and the rebuilt `shim/shim.mjs` and `shim/pool.mjs`.
  - funcd-python (5b5813e): `shim/src/funcd_shim/{tracespan.py,invoke.py,shim.py,_poolworker.py}`, `shim/tests/test_invoke.py`.
- **Bar**: the ADR's Contracts, Scenarios, Review checklist and Definition of done. Departures are judged against the preflight brief.
- **Verdict**: **pass**. There are no Blockers, Majors or Minors.

## Verification run

| Check | Result |
|---|---|
| funcd with a local `go.work` over both language worktrees (removed afterwards; all three trees are clean) | built |
| `go build ./...` on darwin and on Linux (`GOOS=linux`) | exit 0 on both |
| `go vet` for `internal/workernode/local`, `internal/funclog` and `pkg/funcd`, also with `-tags e2e`, on darwin and Linux | exit 0 on all |
| `golangci-lint run` for the same packages, also with `--build-tags e2e`, on darwin and Linux | 0 issues on all |
| `go test -race -count=1` for `internal/workernode/local` and `internal/funclog` | ok on both |
| `go test -race -count=1 ./pkg/funcd/` (fast lane) | ok (9.7 s) |
| `TestBrokerForwardsTraceparent` (4 subtests) and `TestScenarioNoHeaderBehavesAsToday` under `-race` | PASS |
| funcd-typescript `scripts/agent/d just ci` (install, lint, typecheck, test, build, go-check) | exit 0; the 7 new tests pass; the tree is clean after `build`, so the committed bundles are current |
| funcd-python `scripts/agent/d just ci` (ruff, mypy, pytest for the shim, bundle and examples, go-check) | exit 0; shim tests: 195 passed; the tree is clean |
| e2e scenario tests and Lima lanes | not run, as instructed (they compile; see "Not scored") |

### Mutants (9 of 9 killed)

| Side | Mutant | Killed by |
|---|---|---|
| funcd (overlay) | `invoker.go` drops the `traceparent` header set | `TestBrokerForwardsTraceparent` |
| funcd (overlay) | `NewHandler` passes `r.Context()` instead of `withTraceparent(...)` | `TestBrokerForwardsTraceparent` |
| funcd (overlay) | the broker forwards only a well-formed (55-character) value | `TestBrokerForwardsTraceparent/*/not-a-traceparent` |
| TS (scratch copy) | the CLIENT span's parent is the caller's parent, not the caller's span | "client span stamps traceparent and emits CLIENT" |
| TS (scratch copy) | an unset socket rejects without ending the span (the span effectively opens after the check) | "unset invoke socket: ERROR client span …" |
| TS (scratch copy) | a non-JSON 2xx ends the span without `http.status_code` | "a 2xx reply that is not JSON …" |
| Py (scratch copy) | `reply(status)` moves after `json.loads` | `test_non_json_2xx_emits_error_client_span_with_status_code` |
| Py (scratch copy) | the CLIENT span's parent is the caller's parent | `test_client_span_stamps_traceparent_and_emits_client` |
| Py (scratch copy) | `__exit__` ignores the exception (always OK) | 3 tests (failed call, unset socket, non-JSON 2xx) |

The baseline of each scratch copy passed before the mutation: TS 10/10, Py 9/9, with `funcd_shim` imported from the scratch copy.

## Contracts

| Contract | Holds | Evidence |
|---|---|---|
| `traceparentHeader`, `traceparentKey`, `withTraceparent` (an empty value leaves ctx unchanged), `traceparentFrom` | yes | `internal/workernode/local/local.go`, with the exact names and semantics; all unexported |
| `NewHandler`: `inv.Invoke(withTraceparent(r.Context(), r.Header.Get(traceparentHeader)), target, input, timeout)` | yes | The line is verbatim. `Invoker.Invoke` is unchanged. |
| `proxyInvoker.Invoke` sets the header after `X-Funcd-Namespace` when the value is not empty | yes | `invoker.go`. The test also shows `Content-Type` and the namespace header are unchanged, and that the forwarded request differs only by `traceparent`. |
| Internal chain `gateway.Chain(dpCore, gateway.Recover(p.logger), gateway.RequestID, edgeShape)` | yes | `pkg/funcd/funcd.go`. The listener chain (`dpHandler`) is unchanged, and the comment above it now says that the internal chain also has no edge observ. |
| TS `ClientSpan`, `startClientSpan(sink, alias, member?)`, `SpanRecord.kind` `'SERVER' \| 'CLIENT'` | yes, with the agreed departure | The added `member?` is the preflight's handling of the contradiction between Decision 1 and the signatures. Without it, RoutePool would drop a pooled CLIENT record. |
| TS `makeInvoke(opts: { member?; sink? })`; `shim.ts` passes `{ sink: traceSink }` declared before `ctx`; `pool.ts` passes `{ member, sink: channel }` | yes | `InvokeOptions` gains `sink`. The source and both bundles carry the wiring (`makeInvoke({ sink: traceSink })` in `shim.mjs`, `sink: channel` in `pool.mjs`). The pool's `channel` is a `const` opened before `ctx`. |
| Py `ClientSpan(channel, alias, member=None)` with `traceparent`, `reply`, `__enter__` and `__exit__`; `invoke(alias, payload, *, member=None, channel=None)` | yes, with the same agreed departure | `tracespan.py`, `invoke.py`. `shim.py` builds `_Context(channel)`, and `_poolworker.py` passes `channel=_channel`, the module global opened in `init()`. |
| The CLIENT line's wire shape (ADR-0101 wire with `kind` widened) | yes | Both shims' stamp tests assert the full record field by field: trace, parent, name `call greeter`, kind, status, empty `status_msg` on OK, `attrs` `{"http.status_code":"200"}`, `inv` and empty `links`. |
| No config key, no new Go dependency, no change to `internal/funclog` decoding | yes | `go.mod` is untouched. The `internal/funclog` diff changes only comments. |

## Decisions

- **D1, the CLIENT span**: Both shims mint a fresh span-id under `currentInv()` / `current_inv()` and send `00-<T>-<S>-01`. They emit one record through the shared helper (TS `emitSpan`, now parameterised by ids, kind and attrs; Py `_emit_span`, which `InvocationSpan.__exit__` also uses), and a pool member's record carries `funcd.member`. Outside an invocation they send no header and emit no span. With no channel they send the header and write no line. All of this is pinned by tests on both sides.
- **D2/D3, the broker**: It forwards the value verbatim, including a malformed one, which the callee's shim rejects as ADR-0101 already does. The value passes through the ADR-0147 `NewNestedCapInvoker` (tested). With no header, the broker forwards none.
- **D4, edge observ**: `edgeObserv` is removed from the internal chain only; `edgeShape` and the broker's log lines stay.
- **D5, ERROR**: The span is ERROR on every failure path: non-2xx, transport error on the request or the response, unset socket, and non-JSON 2xx. In TS the span opens before the socket check. In Py `ClientSpan` wraps everything from the socket check on, and `reply()` runs before `json.loads`. `status_msg` is the message of the error the handler receives. `http.status_code` is present only when a reply arrived. The cut of `status_msg` to ADR-0168's record bound is correctly absent: ADR-0168 is not on either shim's main (no `FUNCD_FUNCLOG_MAX_RECORD_BYTES` there), and the preflight assigns the cut to ADR-0168's change to the shared helper.
- **D6, timing**: TS uses a `Date.now()*1e6` base plus an `hrtime` delta, and Py uses `time.time_ns()` plus a `monotonic_ns` delta. `InvContext` is unchanged. The "failed call" tests on both sides assert that the span covers the stub's 50 ms delay.

## Review checklist

| Item | Status |
|---|---|
| Both shims, solo and pool, follow Decisions 1, 5 and 6 through the shared emit helper | Holds. The D5 cut is deferred to ADR-0168, as the preflight agreed. |
| The broker forwards `traceparent` verbatim via ctx (no header in, none out); `Invoker.Invoke` and the other headers are unchanged | Holds |
| No new exported Go name; the internal chain has no `edgeObserv`; the listener chain and `internal/funclog` decoding are unchanged; `go.mod` changes only the two language pins | Holds on this branch: `go.mod` is untouched, and the pin bump follows the releases |
| Each scenario has one named, passing test; ADR-0101 and ADR-0114 get only the back-link; the F51 row matches | Partly pending. All five named tests exist and compile. `TestScenarioNoHeaderBehavesAsToday` passes here. The four e2e tests were not run (as instructed), and three of them need the shim releases. The back-links and the F51 row belong to the wave's docs PR. |

## Scenarios

| Scenario | Test | State |
|---|---|---|
| fn-to-fn-joins-one-trace | `TestScenarioFnToFnJoinsOneTrace` (`nodejs22`, `python314`, `nodejs22-pooled`, `python314-pooled`) | It compiles and vets with `-tags e2e`. It asserts three spans in T, the expected parent chain, an OK CLIENT span with `http.status_code` 200, and containment within 1 ms. It needs the releases. |
| failed-call-error-client-span | `TestScenarioFailedCallErrorClientSpan` (422 input contract, 403 undeclared alias) | It compiles. It asserts ERROR, `failed: <code>` in the message, the code attribute and no greeter span. It needs the releases. |
| workflow-logs-include-callee | `TestScenarioWorkflowLogsIncludeCallee` | It compiles. It asserts that `RunLogs` includes greeter's `greeting x` line. It needs the releases. |
| internal-call-skips-edge-observ | `TestScenarioInternalCallSkipsEdgeObserv` | It compiles. It filters to the edge signals (scope `funcd.edge`, message `edge request`, counter `funcd.edge.requests`), as the preflight asked, and asserts exactly one of each, for front. It does not depend on the new shims. |
| no-header-behaves-as-today | `TestScenarioNoHeaderBehavesAsToday` | **PASS** under `-race`. The reply and every forwarded header are the same with and without the header, except `traceparent`. |

## safeBeforeFuncd claims

Both claims are **true**:

- **funcd-typescript: true.** A shim that sends `traceparent` and writes CLIENT lines is harmless on today's funcd. Main's `NewHandler` ignores the header, because it passes `r.Context()` and its invoker sets only `Content-Type` and the namespace. Main's funclog already accepts and marshals `CLIENT` (`SpanKind.valid()` includes `SpanClient`). Pooled CLIENT records carry `funcd.member`, so main's RoutePool keeps them. No existing funcd test or Venom suite counts spans on a fn-to-fn trace (the `funclog` lane's trace step uses `log-burst`, which makes no call), so pinning the release early breaks nothing. Each callee keeps its own fresh trace until the funcd half lands, as it does today.
- **funcd-python: true**, for the same reasons. `_Context()` keeps a default `channel=None`, and `invoke`'s new parameter is keyword-only with a default.

## Not scored

- **Pins and e2e** (attribution: `env`/process). `go.mod` still pins funcd-typescript v0.7.0 and funcd-python v0.4.0. The three e2e scenarios that need the CLIENT span can pass only after both shim releases and the pin bump; the funcd commit message says so. That bump, `just ci-full` and the `fn-to-fn` and `funclog` lanes belong to the later steps of this workflow. No e2e test or Lima lane was run here, as instructed.
- **Docs follow-through** (attribution: process). The ADR still reads `Accepted` on the branch, and the F51 row is not advanced. This workflow assigns the `Accepted → Reviewing` bump, the F51 row and the `Superseded in part by: ADR-0165` back-links in ADR-0101 and ADR-0114 to the wave's docs PR, so this gate stamped nothing.
- **Test-fidelity note** (no finding). In `TestScenarioFailedCallErrorClientSpan`, the "no greeter span" check reads the trace as soon as two spans exist. It would not wait for a late greeter span. The guarantee rests on the data plane answering the 422 and the 403 before dispatch, which this ADR does not change.
- **Wiring coverage note** (no finding). No shim-local test checks the solo and pool wiring of the sink and channel (`shim.ts`, `pool.ts`, `shim.py`, `_poolworker.py`). As the plan intends, the four `TestScenarioFnToFnJoinsOneTrace` subtests cover it once the pins land.

## ✅ Verified correct — keep it

- The funcd change is the Contracts, line for line, and minimal: about 30 production lines with no new exported name. The header travels in ctx, so `Invoker.Invoke` and the ADR-0147 wrapper are untouched, and a test proves the value passes through the wrapper.
- The broker test forwards a malformed value verbatim. It therefore pins the ADR's decision that validation belongs to the callee's shim, and it kills a "helpful" broker-side validation mutant.
- `TestScenarioNoHeaderBehavesAsToday` compares the whole forwarded header set with and without the header, which is stronger than an "absent" check.
- Both shims route SERVER and CLIENT records through one emit helper, so ADR-0168's later cut lands in one place. Every failure path settles the span exactly once (the TS `fail` closure and `ended` guard, the Py context manager).
- Each shim adds the two gap tests the preflight asked for, unset socket and non-JSON 2xx, plus a pooled-member stamp. Each of them kills a mutant.
- Both shims' bundles were rebuilt and committed (the tree is clean after `just build`). No repo hand-bumps a version or changelog. All three commits are scoped and conventional, use the house identity, and say `Refs #86`.
- The e2e scenario file follows the preflight: a new `postTraced` rig helper (test-only), edge-signal filtering, and `NewFromProviders` with a `ManualReader` and a `SpanRecorder`.

## Recommendation

**Pass.** The work meets every Contract and Decision, with the two agreed departures: `member` on the CLIENT-span constructors, and the D5 cut deferred to ADR-0168. Three of the four checklist items hold now, and the fourth waits on the release, pin and docs steps that this workflow runs later. Both shims' `just ci` and funcd's build, vet and lint pass on darwin and Linux (vet and lint also with the e2e tag), and the race tests of the touched packages pass. All 9 mutants are killed, and both safeBeforeFuncd claims are true. Next: release both shims, bump the two pins in the funcd PR, and run the four e2e scenarios with `just ci-full` and the `fn-to-fn` and `funclog` lanes at the PR gate. The wave's docs PR then advances the ADR, the F51 row and the back-links.

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
  "dod_passed": 4,
  "dod_total": 6,
  "report": "docs/reviews/adr-0165-implementation-claude-opus-5-5.md",
  "notes": "all Contracts match (agreed member arg on startClientSpan/ClientSpan; D5 status_msg cut deferred to ADR-0168, not on shim main); broker forwards traceparent verbatim via ctx incl. through NewNestedCapInvoker; internal chain drops edgeObserv only; TS+Py just ci green (bundles fresh), funcd build/vet/lint green darwin+Linux incl. e2e tag, -race ok for local/funclog/pkg/funcd; 9/9 mutants killed (3 funcd overlay, 3 TS, 3 Py); safeBeforeFuncd true for both; e2e scenarios compile but not run (instructed), 3 need the releases; pins + F51 row + Reviewing bump + back-links pending release/docs PR (process)"
}
```
