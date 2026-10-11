## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0226 implementation, round 2, model: claude-opus-5-5)

Round 1 ([report](adr-0226-implementation-claude-opus-5-5.md)) found the ADR's own work exact and asked for one fix:
Major 1, in the TypeScript SIGTERM commit. Both SIGTERM commits were amended; the feat commits are unchanged.

- pyvvo/funcd-typescript PR #60: `b6fef95` replaces `38784dc`, on top of the same `1f529d1`.
- pyvvo/funcd-python PR #74: `d2cc970` replaces `1c13988`, on top of the same `9ab8643`.

funcd still reaches both heads through the untracked `go.work`. The `go.mod` bump to the released tags comes after
this review.

### Verification run

| Check | Command | Result |
|---|---|---|
| scenario tests and hung worker | `scripts/agent/d go test -race -count=1 -v -tags e2e -run '^(<the 8 tests>\|TestScenarioAppHungWorkerRestarted)$' ./pkg/funcd/` with the `go.work` | 9/9 PASS, exit 0: HandlerReadsDeadline 0.46/0.53 s, Issue928 2.96 s, AsyncCancelled 2.92 s, SpinningAsync 3.98 s, PooledPython 3.14 s, PooledNode 12.54 s, LateAwait 13.48 s, Unstoppable 13.22 s. `TestScenarioAppHungWorkerRestarted` takes 2.22 s again (round 1: 5.04 s with the fix, 2.05 s on the old pins) |
| TS SIGTERM tests | `node --test test/sigterm.test.ts` at `b6fef95` | 5/5 pass on Node 24 and on Node 22 |
| TS revert, round-1 fix | the same tests with `src/runtime.ts` from `38784dc` | the new test "a shim that is not PID 1 ends by SIGTERM at once while a call blocks its event loop" FAILS (still running after 2 s) |
| TS revert, no fix | the same tests with `src/runtime.ts`, `shim.ts`, `pool.ts` from `1f529d1` | both "as PID 1" tests FAIL (still running); the three others pass |
| TS build and lint | rebuild of `shim.mjs` and `pool.mjs` with the build script's flags; `biome ci .`; `yarn typecheck` | both bundles byte-identical to the committed ones; biome exit 0 (the same 24 warnings on untouched lines); typecheck exit 0 |
| Python SIGTERM tests | the three SIGTERM tests at `d2cc970` | 3 passed |
| Python revert, round-1 fix | the same tests with `shim.py` from `1c13988` | `test_shim_not_pid_1_dies_by_sigterm` FAILS (143, not -15) |
| Python revert, no fix | the same tests with `shim.py` and `pool.py` from `9ab8643` | both "as PID 1" tests FAIL (-15, not 143) |
| Python lint and types | `ruff format --check .`, `ruff check .`, `uv run --locked --python 3.14 mypy` | clean; mypy finds no issues in 33 files |
| language PRs | `gh pr view` (read only) | #60 at `b6fef95` and #74 at `d2cc970`: `changes`, `pr-title` and `ci` SUCCESS |

### Round-1 findings

- **Major 1 (model): resolved.** funcd-typescript `shim/src/runtime.ts` `exitOnSigterm()` returns at once unless
  `process.pid === 1`. As PID 1 it installs `process.on('SIGTERM', () => process.exit(143))`. Any other process keeps
  Node's default action, which ends it even while a stuck call blocks the event loop. The new test in
  `test/sigterm.test.ts` covers that case (a `for (;;)` call in flight, then SIGTERM; the shim must end by the signal
  within 2 s), and it fails on the round-1 fix. The PID-1 tests now emulate PID 1 with a preload that sets
  `process.pid` to 1 and swallows the signal, as the kernel does.
- **Minor 1 (model): resolved.** funcd-python `shim/src/funcd_shim/shim.py` `exit_on_sigterm()` installs the
  `os._exit(143)` handler only when `os.getpid() == 1`; the pool host calls the same function. On the process driver a
  SIGTERM that Stop did not send ends the shim by signal 15 again, so `exitOf` records `ExitBySignal` as before. The
  new test `test_shim_not_pid_1_dies_by_sigterm` fails on the round-1 fix. The two PID-1 tests use a seam that makes
  `os.getpid()` return 1 in the test's subprocess only.

### Minor 1 — the TypeScript suite fails now and then on an fd race in `pool.test.ts` that predates this ADR  ·  attribution: env (pre-existing, carried over)

Round 1's Minor 2 is unchanged. `pool.test.ts` sometimes aborts on a Node fd assertion after `pooled-member-logs`, and
`issue r25` then finds a temp dir left behind. Both tests predate ADR-0226, and the fd warnings occur on main as well.
Owner: a funcd-typescript issue. It is a merge-queue risk for PR #60, and a retry clears it.

### ✅ Verified correct (keep it)

- **The ADR's own work.** Round 1 verified it, and none of it changed: rung 1 in both shims, the Python watchdog with
  its lock and its escalation for async calls, the TypeScript ping bound to the worker object, liveness 503 only in
  Decision 3's cases, and nothing without a valid header. There is no config key, no new dependency, and one passing
  test per scenario. `TestIssue928_StuckPythonCallStops` fails on the old pins.
- **The two SIGTERM fixes now agree.** Both shims act only as PID 1, exit 143 there, and keep the default action
  everywhere else. funcd's ADR-0160 record is therefore the same as before outside a container. In a container, 143
  reads as signal 15 on containerd, and Stop's own end is `ExitByStop` on either driver.
- **Each new test fails without its fix.** The checks are in the table above.

### Definition of Done

8 / 11 ADR items hold: the 6 Review-checklist items, "every scenario test passes, and
`TestIssue928_StuckPythonCallStops` fails with the previous pins", and "no username or absolute path in a changed
file". The other three come after this review in the plan: both shims released (their tests pass, and both PRs are
green), funcd pins the tags, and the gate passes. They are attributed to sequencing, not to the model. This branch
cannot merge before them: its scenario tests fail on the old pins.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0226 (implementation, round 2) → pass, 0 blockers / 0 majors / 1 minor,
0 model-attributed, DoD 8/11. See `docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0226 moves to `Implemented`, F125 to `implemented`, and the board card to Done. Next, merge PRs #60 and
#74 and release both shims. Then `go get` both tags on this branch, rerun the 8 scenario tests on the pins, and run
`scripts/agent/gate.sh` once before the funcd PR. Minor 1 goes to a funcd-typescript issue.
