# Review: ADR-0181 implementation (claude-opus-5-5), loop 1

- **ADR**: [ADR-0181](../adr/0181-edge-forwards-bodiless-upgrades.md), the edge forwards a bodiless upgrade request
  untouched, and an idle tunnel closes
- **Work**: branch `feat/adr-0181-bodiless-upgrades-idle-tunnel`, one commit `e1021635` on `origin/main` (10 files,
  +722/-58)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**. No Blocker and no Major. Three Minor items: one is attributed to the ADR and two are
  model nits. Three DoD items are pending by the agreed process: the soak stages (run later by the main loop) and
  the docs edits (made by the wave's docs PR).

## Verification run

All checks ran in the branch worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `go build ./...` (darwin, then `GOOS=linux`) | exit 0, exit 0 |
| `go vet` on `internal/dataplane`, `internal/activator`, `internal/testkit/wsupstream`, `pkg/funcd` | exit 0 |
| `go vet -tags 'e2e soak'` on the same packages (darwin and `GOOS=linux`); `-tags e2e ./pkg/funcd/` | exit 0 on each |
| `golangci-lint run --build-tags e2e,soak` on the four packages (darwin, and Linux with `GOOS=linux`) | `0 issues.` on both |
| `go test -race -count=1` on `internal/dataplane`, `internal/activator`, `internal/testkit/wsupstream` | `ok`, `ok`, no test files |
| Scenario tests, `-race -v` | all 7 non-e2e scenario tests, `TestBodilessUpgrade` and `TestIdleTunnelPassesNon101` pass |
| Flake check: `-race -count=5` on the activator tunnel tests, `-count=3` on the dataplane scenario tests | `ok` on both |
| `go.mod` / `go.sum` versus `origin/main` | unchanged |

The run did not include the e2e subtest `TestScenarioE2EFullEdgeChainStreaming/edge-chain-websocket-real-handler`,
because the orchestrator said to run no e2e. It compiles under `-tags e2e` and under `-tags 'e2e soak'`, and it
passes vet and lint. The PR gate (`just ci-full`) runs it. The soak test `TestSoakUpgradeTunnel` was compiled only:
each stage waits for the real 5-minute idle close, so a `1m` stage takes more than 5 minutes, which is beyond the
brief's 2-minute limit.

### Revert checks and mutants (`go test -overlay`; the work was not edited)

| Overlay | Expected | Observed |
|---|---|---|
| R1: `internal/dataplane/dataplane.go` from `origin/main` (the condition reverted to `if !internal {`) | the two non-e2e main-failing tests fail | `TestScenarioExternalWebSocketRoundtrips` FAIL (expected `[]int64{0}`, actual `[]int64{120}`, which is issue #727); `TestScenarioUpgradeTunnelOutlivesDeadline` FAIL |
| R2: `idleTunnel` returns `rt` (the ADR's revert check) | `TestScenarioIdleTunnelClosed` fails | FAIL after 5.00 s: "the client's pending read fails when funcd closes the tunnel" |
| M3: `idleBody.Read` does not store the activity time | the case where only the upstream writes fails | `TestScenarioOneDirectionIsTraffic/upstream-writes` FAIL at 0.30 s, so the tunnel was cut while the upstream was sending; the other cases pass (the Write path keeps them open) |
| M4: `bodilessUpgrade` without `r.ContentLength == 0` (the rejected option A without the narrowing) | the capped-body tests fail | `TestScenarioUpgradeWithBodyStillCapped/{sized,chunked}` and `TestBodilessUpgrade/{sized_body,chunked_body}` FAIL |

All four overlays were caught by a failing test. The revert check of the third main-failing test (the e2e subtest)
is left to the PR gate, because this review runs no e2e. The builder's revert overlays are in the impl scratch area
(`dp.json`, `A.json`, `B.json`, `act.json`), together with a run of the activator's importers (`internal/function`,
`internal/sensor`, `internal/workflow`, `internal/workernode/local`, `internal/edge/authn`), all `ok`.

## Contracts and Decision versus the code

- **Decision 1, `bodilessUpgrade`** (`internal/dataplane/normalize.go:21-28`) matches the Contracts code exactly:
  `ContentLength == 0`, `httpguts.HeaderValuesContainsToken(r.Header["Connection"], "upgrade")` and a non-empty
  `Upgrade` header. The `httpguts` import already existed in the module, so `go.mod` is unchanged.
- **Decisions 2 and 3, `serveFunction`** (`internal/dataplane/dataplane.go:189-195`): the diff is the one
  condition `if !internal && !bodilessUpgrade(r) {` and its comment. The PEP, the throttle, the credential strip
  and the deadline are not touched.
- **Decision 4**: the real-handler test is in `internal/dataplane/upgrade_test.go`. The `pkg/funcd` subtest now
  goes through `edgeChainServer` (`pkg/funcd/edge_shaping_e2e_test.go:140-193`), which sends `/function/ws` to
  `dataplane.Handler` over `activator.New`, a test-local `warmEndpoint` and a `failScaler`. The stand-in still
  serves only SSE and the normal path. The old inline `wsEcho` was removed.
- **Decision 5, `idleTunnel`** (`internal/activator/tunnel.go`): `tunnelIdleTimeout = 5 * time.Minute` is
  unexported. The transport wraps only a 101 whose body is an `io.ReadWriteCloser`. `idleBody` holds the inner
  body in a field (it is not embedded), and its only interface methods are `Read`, `Write` and `Close`. The clock
  is an `atomic.Int64` of `time.Since(start)`, which is monotonic. `time.AfterFunc` re-arms for the remainder.
  `Close` uses `sync.Once` and stops the timer under `mu`. The mutex held in `newIdleBody` correctly orders a
  timer that fires early against the assignment of `b.timer`. The wiring is one line, placed after
  `DeadlineTransport` in `activator.New` (`internal/activator/activator.go:149`). `DeadlineTransport` and
  `CallTracker.Wrap` (`countedConn`) both keep the 101 body writable, so the wrapper also applies in production,
  where `Calls` is set.
- **`internal/testkit/wsupstream`**: `New(tb)`, `ContentLengths`, `Ended`, and the modes `echo`, `sink` and `push`
  with `every`, as the Contracts specify. Concurrent sends from the push goroutine and the echo loop are safe,
  because `x/net/websocket` serializes them on its write mutex.

## Review checklist (ADR-0181)

1. The `serveFunction` diff is the one condition and its comment: **holds**.
2. `bodilessUpgrade` requires the token, a non-empty `Upgrade` and `ContentLength == 0`, and the capped test
   covers a sized and a chunked body: **holds** (M4 is caught).
3. The `pkg/funcd` WebSocket subtest goes through the real `dataplane.Handler`: **holds** by reading the code; it
   compiles and lints but was not run here (no e2e).
4. `idleTunnel` is applied once, after `DeadlineTransport`; it wraps only a 101 with an `io.ReadWriteCloser` body,
   has only `Read`, `Write` and `Close`, an atomic clock and an idempotent `Close`: **holds**.
5. `internal/testkit/wsupstream` is the only WebSocket test upstream, with no copy: **holds within the ADR's named
   scope**; see m1.
6. The soak test has the tag `e2e && soak` and takes its baseline after setup: **holds**. "The PR lists all four
   stages" is **pending**: the brief assigns the stages to the main loop.
7. The revert checks were run, `go.mod` and `go.sum` are unchanged, and the diff has no absolute path or
   username: **holds**. Two of the three dataplane revert checks and the idle-tunnel revert check were rerun
   here; the e2e one is left to the gate.

## Definition of done

- Each scenario has a passing test of the same name: 7 of 8 were run and pass; the e2e subtest compiles.
- The main-failing tests fail on the unfixed code (2 of 3 confirmed here), and `TestScenarioIdleTunnelClosed`
  fails when `idleTunnel` passes the transport through (confirmed).
- No new dependency and no config key: confirmed.
- ADR-0134 carries the back-link: present (`docs/adr/0134-…md:4`).
- The FEAT-0006 F78 row links ADR-0181 with `upgrade passthrough: accepted`. It agrees with the ADR, which is
  still `Accepted` because the wave's docs PR makes the status moves.
- **Pending (process, not the model)**: the PR records the four soak stages (main loop), and FEAT-0006 line 262
  still cites only the `gatewaycontract` case. The ADR requires it to cite `TestScenarioExternalWebSocketRoundtrips`
  and the `edge-chain-websocket-real-handler` subtest, and the docs PR must make that edit.

## Findings

### Blocker

None.

### Major

None.

### Minor

- **m1 [adr]**: Review-checklist item 5 says `wsupstream` is "the only WebSocket test upstream", but the
  Implementation plan and the brief limit the change to the dataplane, activator and `pkg/funcd` tests. A
  pre-existing inline echo upstream remains in `internal/gateway/gatewaycontract/contract.go:109`, outside the
  files that the ADR names. The checklist wording is broader than the plan, so this finding is not scored
  against the model. Moving that suite onto `wsupstream` is optional follow-up work.
- **m2 [model]**: `TestScenarioIdleTunnelClosed` (`internal/activator/tunnel_test.go:66-77`) measures the close
  time from before the dial instead of from the last byte (the 101). The lower bound "300 ms after the last byte"
  is therefore checked against an origin that is earlier by the handshake time, a few milliseconds on loopback. A
  slightly early close would still pass. The upper bound is stricter than the ADR requires, which is safe.
- **m3 [model]**: the doc comment of `wsupstream.Server.Ended` (`internal/testkit/wsupstream/wsupstream.go:53`)
  says it counts connections that ended "because their pending read failed". The deferred counter also counts an
  end after a failed send and the early return on an invalid `every`. The tests use the count correctly; only the
  comment is imprecise.

## Verified correct: keep these

- Prove-first: the overlay of `origin/main` reproduces issue #727 exactly (`CL=120`), and the narrowed predicate
  passes all four bypass and normalization mutants.
- The `idleBody` design is careful. The inner body is held in a field, so `io.Copy` cannot bypass the clock. The
  monotonic atomic clock is set only when bytes move. The re-arm uses the remaining time, not a timer reset on
  every byte, which keeps `Read` and `Write` cheap. The `sync.Once` close is guarded by `stopped`, so the timer
  cannot fire again after `Close`.
- The tunnel tests cover each direction separately (M3 shows that the upstream-only case catches a `Read` that
  does not update the clock), and they are stable under `-race -count=5`.
- Reuse: the dataplane tests reuse `readyEndpoints`, `spyScaler`, `seedFn`, `echoUpstream`, `newHandler` and
  `seedFunction`; the activator test reuses `roundTripFunc`; the `pkg/funcd` chain assembly is extracted once into
  `edgeChainServer` and shared with the soak test.
- The soak test follows the plan step by step: the baseline is taken after setup and before either tunnel opens,
  the active tunnel gets a push every 60 s and a client frame offset by 30 s, the idle window is 5 min to 5 min
  10 s with both ends checked, the run lasts until both the stage length and the idle close are reached, a 2 s
  settle follows, and the limits are goroutines +5 and open files +2.
- The change stays inside the files the ADR names; no doc is edited, and `go.mod`/`go.sum` are unchanged.

## Recommendation

Pass. The PR gate's `just ci-full` must run the `edge-chain-websocket-real-handler` subtest. The wave's docs PR
moves the ADR and the F78 row (`Accepted → Reviewing → Implemented` / `reviewing → implemented`), adds the
FEAT-0006 line 262 citation, and records the four soak stages in the PR when the main loop has run them. m2 and m3
can be fixed in a later touch of these tests.

```json
{
  "date": "2026-10-05",
  "adr": "0181",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 2,
  "dod_passed": 12,
  "dod_total": 15,
  "report": "docs/reviews/adr-0181-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (e1021635): all 5 Decisions + Contracts hold; build/vet/lint clean also Linux and with e2e,soak tags; 3 touched pkgs -race ok, tunnel tests stable at -count=5; 7/8 scenario tests run and pass (e2e real-handler subtest compiled, left to the gate); revert checks R1 (CL=120, #727) and R2 (idleTunnel pass-through) fail as required; mutants 2/2 killed (Read clock, ContentLength narrowing). Pending by process: 4 soak stages (main loop), FEAT-0006 line 262 citation (docs PR). m1 [adr] checklist 'only WebSocket test upstream' is broader than the plan (gatewaycontract echo remains); m2 [model] idle-close lower bound measured from before the dial; m3 [model] wsupstream.Ended doc says read failure only."
}
```
