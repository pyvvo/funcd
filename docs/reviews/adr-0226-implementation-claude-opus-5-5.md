## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0226 implementation, model: claude-opus-5-5)

Work reviewed, in three repos:

- funcd, branch `feat/adr-0226-stuck-calls`: `b7b852a3` (acceptance, F125 row, blueprint, back-links), `1ce1933d` (the
  scenario tests in `pkg/funcd/stuck_call_e2e_test.go`) and `3144e9ec` (`Accepted → Reviewing`).
- pyvvo/funcd-typescript, branch `feat/adr-0226-deadline` (PR #60): `1f529d1` (the ADR's TypeScript side) and `38784dc`
  (a separate fix: the shim exits on SIGTERM as PID 1). These replace `124e86f` and `263dbc3`. The rewrite only moves
  the deadline tests of the pool host into `test/pool-deadline.test.ts` (helpers in `test/poolapp.ts`); `src/`,
  `shim.mjs` and `pool.mjs` are byte-identical.
- pyvvo/funcd-python, branch `feat/adr-0226-deadline` (PR #74): `9ab8643` (the ADR's Python side) and `1c13988` (the
  same SIGTERM fix).

The shims are not released yet. funcd reaches them through an untracked, git-ignored `go.work`; the `go.mod` bump
comes after this review (plan step 3).

The ADR's own work is complete and exact: every contract item, scenario and test of the plan is there and passes, and
`TestIssue928_StuckPythonCallStops` fails on the old pins for its reason. One defect blocks sign-off. It is in the
separate TypeScript SIGTERM fix, which ships in the release funcd will pin (Major 1).

### Verification run

| Check | Command | Result |
|---|---|---|
| scenario tests, new shims | `scripts/agent/d go test -race -count=1 -v -tags e2e -run '<the 8 tests>' ./pkg/funcd/` with the `go.work` | at TS `263dbc3`: 8/8 PASS, exit 0 (HandlerReadsDeadline 0.46/0.55 s, Issue928 2.92 s, AsyncCancelled 2.92 s, SpinningAsync 4.02 s, PooledPython 2.98 s, PooledNode 12.57 s, LateAwait 13.43 s, Unstoppable 13.31 s). At TS `38784dc` (same bundles): 7 PASS, and `TestScenarioStuckPooledNodeMemberRestarted` FAIL at its `waitReady` ("a reconciles to Ready"), before any late call, while another session's gate held the host at load 14 to 19. The same test with `-count=3` right after: 3/3 PASS (12.8, 14.5, 13.0 s). Not attributed (env) |
| scenario tests, old pins | the same command with `GOWORK=off` (funcd-python `v0.7.0`, funcd-typescript `v0.10.0`) | exit 1. `TestIssue928_StuckPythonCallStops` FAIL: "loops logs \"caught DeadlineExceeded\"" never satisfied, the handler is never stopped. HandlerReadsDeadline: "ctx.remainingMs is not a function" and "'_Context' object has no attribute 'remaining_ms'". The async, spinning, pooled and unstoppable tests fail on their stop or liveness wait. `TestScenarioLateAwaitKeepsNodeWorker` passes, as it should: the old pool never ends a worker |
| funcd test files | `gofmt -l`, `go vet -tags e2e ./pkg/funcd/` | clean, exit 0 |
| TS lint | `biome ci .` | exit 0. The 24 warnings are all on lines the branch does not touch |
| TS types | `yarn typecheck` (shim) | exit 0 |
| TS bundles | `esbuild` of `src/shim.ts` and `src/pool.ts` with the build script's flags, into a scratch dir | byte-identical to the committed `shim.mjs` and `pool.mjs`, on both heads |
| TS tests | `yarn test` (shim, Node 24 as CI runs it) | `263dbc3`: 149/149 pass. `38784dc`: 148/149, `issue r25` failed (Minor 2) |
| Python checks | `ruff format --check .`, `ruff check .`, `uv run --locked --python 3.14 mypy`, `… pytest -q` (shim) | all exit 0; mypy clean on 33 files; 263 passed in 71 s |
| Go embed packages | `go vet ./... && go build ./... && go test ./...` in each language repo | ok in both. `embed_test.go` covers the new `deadline.py` |
| SIGTERM revert, TS | `test/sigterm.test.ts` with `src/shim.ts`, `pool.ts`, `runtime.ts` from the feat commit, in a scratch copy | the two "as PID 1" tests FAIL ("still running" after 2 s). The two "not PID 1" tests pass. At the head all 4 pass on Node 24 and Node 22 |
| SIGTERM revert, Python | the two `…exits_143_on_sigterm_with_a_call_in_flight` tests with `shim.py` and `pool.py` from the feat commit | both FAIL `assert -15 == 143`; at the head both pass |
| SIGTERM probe, blocked loop | a solo shim on Node 22 (not PID 1) with a `for (;;) {}` call in flight, then SIGTERM | base `80db066`: ends by the signal in 0.01 s. Head: still running 3 s later, ended by SIGKILL (Major 1). The Python solo shim with a spinning call: base ends by the signal in 0.02 s, head exits 143 in 0.12 s |
| hung worker in funcd | `TestScenarioAppHungWorkerRestarted` (ADR-0215) with and without the `go.work` | old pins 2.05 s, new shims 5.04 s, both PASS |
| ADR substance, tracking | `git diff b7b852a3 3144e9ec`; FEAT-0010 F125 | the bump changes only the Status line; F125 is `reviewing`; the branch changes only docs and tests |
| language PRs | `gh pr view` (read only) | funcd-typescript #60 at `38784dc` and funcd-python #74 at `1c13988`: `changes`, `pr-title` and `ci` all SUCCESS |

### 🟡 Major 1 — with the TypeScript SIGTERM fix, a solo shim whose event loop is blocked no longer ends on SIGTERM  ·  attribution: model

Evidence: funcd-typescript `shim/src/runtime.ts:49-54` (`exitOnSigterm`, called from `shim.ts:137` and `pool.ts:554`).
A Node signal listener runs on the main event loop. In the solo shim the handler runs on that same loop, so a stuck
synchronous call blocks the listener. This is ADR-0226's TypeScript-solo case ("a blocked event loop cannot answer
liveness, so ADR-0215 restarts the replica"). With a listener installed, the signal is caught but never acted on.
Before the commit, SIGTERM's default action ended the process at once.

- The probe above: base ends by the signal in 0.01 s; the head is still running 3 s later and needs SIGKILL.
- In funcd, `TestScenarioAppHungWorkerRestarted` (`pkg/funcd/health_e2e_test.go:69`, handler `for (;;) {}`) goes from
  2.05 s to 5.04 s. The 3 s is the process driver's stop grace (`defaultStopGrace`,
  `internal/runtime/process/process.go:31-33`): `terminate` sends SIGTERM, waits the grace, then sends SIGKILL
  (`process.go:454-462`).
- So on the process driver, every Stop of a stuck TypeScript solo replica now waits one full grace. That covers the
  ADR-0215 restart (the restart ADR-0226 relies on for TypeScript solo), a drain, and the shutdown close and the reap
  of ADR-0167 ("a worker that ignores SIGTERM holds the shutdown close for one full grace").
- The exit class is unchanged (Stop records `ExitByStop`). Containerd is unchanged too: PID 1 dropped SIGTERM before,
  and a blocked loop cannot run the listener now.

The commit message says that a shim that is not PID 1 "still ends by the signal". That holds only while the loop is
free, and `test/sigterm.test.ts:36` tests only an idle shim. Fix (builder, PR #60): keep the default disposition when
the shim is not PID 1, for example by installing the listener only when `process.pid === 1`. Add a test in which a shim
that is not PID 1 and has a blocked event loop ends on SIGTERM.

### Minor 1 — the Python shim records a SIGTERM that Stop did not send as exit code 143 on the process driver  ·  attribution: model

Evidence: funcd-python `shim/src/funcd_shim/shim.py:295-299` always ends with `os._exit(128 + signum)`. On the process
driver (not PID 1), a SIGTERM that Stop did not send, such as an operator's `kill`, used to end the shim by the
signal. It now ends it with code 143, so `exitOf` (`internal/runtime/process/process.go:496-507`) records
`ExitByCode` 143 instead of `ExitBySignal` 15.

The ADR-0160 class is unchanged:

- The driver records an end that Stop caused as `ExitByStop`, whatever the status (`process.go:301-303`), and
  `classifyExit` (`internal/function/bootbackoff.go:39-48`) reads that as stopped.
- 143 is not the shape-error code 3. An end before listening is still a boot crash, and an end after listening is
  still "after serving".
- Containerd reads 143 as signal 15 either way (`internal/runtime/containerd/helpers_linux.go:54-62`).

Only the status text of `describeExit` (`bootbackoff.go:261-268`) changes: "exited with code 143" instead of "was
killed by signal 15". The TypeScript shim raises the signal again to keep the old record, so the two shims now
differ. Fix (optional, PR #74): restore `SIG_DFL` and raise the signal again before `os._exit(143)`, as the
TypeScript shim does.

### Minor 2 — the TypeScript suite fails now and then on an fd race in `pool.test.ts` that predates this ADR  ·  attribution: env (pre-existing)

Evidence: `yarn test` at `38784dc` failed `issue r25: the shim tests leave nothing in the OS temp dir`
(`test/tempdir.test.ts:11`), because a `funcd-pool-test-*` dir was left. A harness that runs the sibling test files as
r25 does, but keeps their output, traced the cause:

- The `pool.test.ts` process aborts on a Node assertion, `Assertion failed: (0) == (uv_fs_close(...))` in
  `node::fs::ReadFileUtf8`. It comes amid "File descriptor N closed but not opened in unmanaged mode" warnings, right
  after `scenario pooled-member-logs` (`test/pool.test.ts:441`).
- Because of the abort, the after hook of the running test (`pool-member-load-failure`, `pool.test.ts:476`) never
  removes its dir. Another time, the same race made member `a` of that test fail to load.

Both tests predate ADR-0226, and the fd warnings occur on main as well (about 4350 per suite run on `80db066`, about
5100 on the head). The new pool paths open no fd. In clean runs during this session, the head failed 3 times in 34
runs (both commit sets) and main failed 0 times in 24. The added load of the new deadline tests seems to make the old
race show more often. It is a merge-queue risk for PR #60. Owner: a funcd-typescript issue about the fd handling of
`pooled-member-logs`, or about workers that are terminated during synchronous fs work.

### ✅ Verified correct (keep it)

- **Rung 1, exact in both shims.** The header is parsed once, as a decimal integer in 1..2147482647 (TS
  `parseTimeoutMs`, `invcontext.ts:35`, now shared with `callTimeoutMs` and `loadTimeoutMs`; Python `deadline_from`,
  `deadline.py:41`, which also rejects a repeated header as Node's join does). The deadline is taken on receipt on a
  monotonic clock: `process.hrtime`, which is the same in every thread, and `time.monotonic()`, which is the same in
  every interpreter. It lives in the existing carrier (`invcontext.ts:17`, set through `tracespan.ts`; `invcontext.py`).
  `remainingMs()` and `remaining_ms()` floor at 0 and return `null`/`None` without a valid header. The pool hosts pass
  the deadline with the call (`Req.deadline`, `_poolworker.invoke(…, deadline)`). The `FunctionContext` contracts and
  `DeadlineExceeded(BaseException)` in `__all__` match the ADR.
- **Rung 2, Python.** `Call` (`deadline.py:81-149`) runs a per-call watchdog thread in the call's own interpreter. It
  is a non-daemon thread in a subinterpreter, because a subinterpreter refuses daemon threads. At deadline + 1 s it
  raises in a synchronous call. For an async call it first runs `call_soon_threadsafe(task.cancel, "deadline
  exceeded")` and raises 1 s later only if the call is still in flight. The raise and the end share one lock. The end
  deregisters the call, clears a raise that has not fired, tolerates a raise that fires inside it, and joins the
  watchdog. `test_end_finishes_when_the_raise_fires_inside_it` builds that interleaving and checks that the thread's
  next call runs clean. A pooled call that starts after deadline + 1 s raises in `Call.__init__`, so its handler
  never runs. A stopped call answers 500 `{"error":"deadline exceeded"}` through the existing mapping, and so does a
  cancelled async call.
- **Rung 2, TypeScript pool.** At the call's timer the host pings the worker object captured at dispatch
  (`pool.ts:435-440`). After 10 s without a pong it calls `terminate()` on that object (`pool.ts:358-376`). The exit
  takes the existing `fault()` path, and `forget()` drops the pings and the end of an exited worker. A test checks
  that a worker which exits inside its window neither ends its successor nor counts as stuck.
- **Rung 3.** Liveness answers 503 `stuck call` only in Decision 3's cases. Python solo checks a call in flight 11 s
  after its deadline (`stuck()`). The Python pool host counts only a running member call, not a queued one
  (`pool.py:124`). The TypeScript host checks for a worker it ended that has not exited 10 s later (`pool.ts:351`).
  Liveness never calls funcd, and TypeScript solo liveness is unchanged.
- **Nothing without a valid header.** No deadline, watchdog or ping is set up, and the 30 s answer of the TypeScript
  pool stays. There is no config key: the delays are module constants, and the tests patch them or pass
  `stuckDelayMs`. No dependency was added: `package.json`, `yarn.lock`, `pyproject.toml`, `uv.lock` and `go.mod` are
  untouched.
- **Scenario tests.** There is one test per scenario, each with `// scenario: <name>` and `t.Parallel()`. They run on
  the process runtime with the real shims, and the unstoppable test uses `WithPacing` as the plan asks.
  `stuck-python-call-stopped` is `TestIssue928_StuckPythonCallStops`, which fails on the old pins because the handler
  is never stopped. Each assertion follows its scenario's wording: within 2 s, 3 s or 12 s; liveness 503 from 11 s and
  before 12 s; the same PID or a new one.
- **Language-repo tests cover plan steps 1 and 2.** TypeScript covers `remainingMs` solo and pooled, with and without
  the header, a blocked worker ended and restarted while its sibling serves, a late await that is not ended, and a
  worker that does not exit after `terminate()` (blocked on a FIFO) giving liveness 503. Python has unit tests and
  subprocess tests for a looping synchronous handler, an async cancel, a spinning async handler, a `time.sleep`
  handler with liveness 503 (solo and pool), a pool member stopped while its sibling serves, and a pooled call that
  starts late and never runs.
- **The SIGTERM commits prove themselves.** Each new test fails without its fix and passes with it. Without a blocked
  loop, the TypeScript listener keeps a signal end outside PID 1 (tested). Python exits 143 at once with a stuck call
  in flight.
- **funcd side.** The branch changes only docs and tests. The ADR's substance is unchanged and F125 is `reviewing`.
  The blueprint health line and the five back-links were written at acceptance.

### Definition of Done

8 / 11 ADR items hold: the 6 Review-checklist items, "every scenario test passes, and
`TestIssue928_StuckPythonCallStops` fails with the previous pins", and "no username or absolute path in a changed
file". Three items follow this review in the plan and are not done yet. They are attributed to sequencing and not scored: both shims released (their tests pass
and both PRs are green), funcd pins the tags, and the gate (`scripts/agent/gate.sh`) passes. Major 1 is outside the
ADR's own checklist, but it ships in the release that funcd will pin.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0226 (implementation) → changes-requested, 0 blockers / 1 major / 2 minors, 2
model-attributed, DoD 8/11. See `docs/reviews/model-scorecard.md`.

### Recommendation

Changes requested, for Major 1 only. Fix the SIGTERM listener in funcd-typescript PR #60 and add a test with a blocked
event loop. Minor 1 is optional, in PR #74. Minor 2 goes to a funcd-typescript issue. Then run a short round 2: the
TypeScript SIGTERM change, the TypeScript suite and the 8 scenario tests. After it come the releases, the `go get` of
both tags, and the gate. ADR-0226 stays `Reviewing`, F125 stays `reviewing`, and the board card stays In Progress.
