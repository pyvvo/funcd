# ADR-0226: Stuck calls — the handler reads its deadline, the shim stops a late call, liveness fails one that will not stop

- **Status**: Implemented (2026-10-11; the implementation review passed on its second round,
  docs/reviews/adr-0226-implementation-claude-opus-5-5-2.md; accepted 2026-10-11, self-accepted under adr-batch after
  two judge rounds)
- **Date**: 2026-10-11
- **Deciders**: green-0-rabbit
- **Tags**: shim, pool, deadline, timeout, liveness, python, typescript
- **Realizes**: [FEAT-0010/F125](../feat/0010-feat-apps.md) (A late call ends; a new row)
- **Source**: issue #928 (decider, 2026-10-11: the handler's deadline in its context, then the stop ladder "in the
  current shims").
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0226` back-link at acceptance:
  - [ADR-0151](0151-external-invoke-deadline.md) (Implemented) Decision 7, "Other shims unchanged": the Python shims
    read `X-Funcd-Timeout-Ms` too, and the TypeScript pool also ends a blocked worker (Decision 2).
  - [ADR-0030](0030-function-execution-runtime-shim-node.md) (Implemented) §1, liveness "200 while the process is up"
    (119, 207), the shared shim contract; [ADR-0049](0049-python-runtime-shim.md) (Implemented), the same for Python
    (45, 196); [ADR-0044](0044-worker-pooling-threads.md) (Implemented), "200 while the host is up" (123); and
    [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (Implemented), the pool host's liveness row (157): 503
    in Decision 3's cases. The TypeScript solo shim's liveness is unchanged.
- **Relates to** (unchanged): [ADR-0215](0215-built-in-health.md) Decisions 1–2 (a replica that does not answer 200
  for `runtime.livenessTimeout` is restarted; `probeOK`, `internal/function/liveness.go:139-145`) · ADR-0037 · ADR-0101
  (the per-call context) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (shims are pinned modules) ·
  ADR-0044's deferred event-loop-lag watchdog (57, 113), which this does not provide: only a late call triggers a ping.

## Context & Need

funcd stops waiting for a call at its deadline (ADR-0151: 504) and sends the time left to the worker in
`X-Funcd-Timeout-Ms` (whole milliseconds, rounded up; `internal/activator/deadline.go:16-18`). Only the TypeScript
pool reads it, and only to free the caller's entry (`pool.ts:439`); the handler runs on. A handler stuck in a loop
then holds its thread for ever. In the Python shim liveness is answered on another thread, so it stays 200 and
ADR-0215's restart never fires (#928, the L13 probe). In a pool, a stuck member holds every later call to it (Python:
`max_workers=1`) or its worker thread (TypeScript).

The purpose: a handler can budget its work against the time it has left, and a call that runs past its deadline
ends — the stuck call alone where the language allows it, the replica otherwise. The callers are funcd's invoke
paths, which already send the header (ADR-0151 Decision 6).

## Scenarios

- `handler-reads-deadline`: Given a Ready replica of a TypeScript and of a Python Function with `spec.timeout: 5s`
  whose handler returns its context's remaining time, When each is invoked, Then the value is a whole number above
  3000 and at most 5000.
- `stuck-python-call-stopped` (#928): Given a Python solo handler looping in Python code, `spec.timeout: 1s`, When it
  is invoked, Then funcd answers 504, the handler sees `DeadlineExceeded` within 2 s of the deadline (its log line),
  and the replica keeps its process, answers liveness 200 and serves the next call.
- `async-python-call-cancelled`: Given a Python solo async handler awaiting `asyncio.sleep(3600)`, timeout 1 s, When
  it is invoked, Then its task is cancelled within 2 s of the deadline and the replica keeps its process.
- `spinning-async-python-call-stopped`: Given a Python solo async handler looping without an `await`, timeout 1 s,
  When it is invoked, Then it sees `DeadlineExceeded` within 3 s of the deadline and the replica keeps its process.
- `stuck-pooled-python-member-stopped`: Given a Python pool with member `a` (a Python loop, timeout 1 s) and member
  `b`, When `a` is invoked, Then `a`'s call ends with `DeadlineExceeded` within 2 s of the deadline and `a` serves its
  next call, `b` serves throughout, and the pool host keeps its process.
- `stuck-pooled-node-member-restarted`: Given a TypeScript pool with member `a` (a synchronous loop, timeout 1 s) and
  member `b`, When `a` is invoked, Then `a`'s worker is ended within 12 s of the deadline and restarted, `a` serves
  again, `b` serves throughout, and the host keeps its process.
- `late-await-keeps-node-worker`: Given a TypeScript pool member whose handler awaits a 5 s timer, timeout 1 s, When it
  is invoked while a second call to the same member runs, Then the worker is not ended and the second call succeeds.
- `unstoppable-call-restarts-replica`: Given a Python solo handler blocked in C (`time.sleep(3600)`), timeout 1 s,
  When it is invoked, Then liveness answers 503 from 11 s after the deadline and funcd restarts the replica (a new
  process) after `runtime.livenessTimeout`.

## Scope

**In**: the handler's remaining time (both shims, solo and pool); the stop of a late call (Python solo and pool,
TypeScript pool); liveness 503 for a call that will not stop (Python solo and pool host, TypeScript pool host); both
shim releases and funcd's bump.

**Out**: carrying the deadline into `ctx.invoke` calls (Open question 1); a setting for any delay; any funcd code
change (the header and the restart exist); a stop in the TypeScript solo shim (every call shares its event loop, so a
blocked loop already fails liveness, and a pending promise cannot be ended); a call without a valid header (from
funcd only an unbounded step): no deadline, no stop.

## Constraints & Decision drivers

- Stop only what is stuck: the calls beside it go on (the Python solo shim runs each call on its own thread; a
  TypeScript pool worker serves several calls at once, `pool.ts:143-197`).
- funcd answers first: every stop comes after ADR-0151's 1 s margin (`timeoutMarginMs`, `pool.ts:72`).
- Python cannot kill a thread. `PyThreadState_SetAsyncExc` raises an exception in a thread at its next bytecode; it
  does not interrupt a C call or a blocking call, and the handler can catch it. It must come from the thread's own
  interpreter: from the main interpreter it did not reach a subinterpreter's thread; from a thread inside that
  subinterpreter it did (probe on Python 3.14.4, 2026-10-11). PEP 734 has no interrupt, and closing a running
  interpreter fails. An exception raised inside asyncio's own code can wedge the loop, so `asyncio.Runner` turns a
  first SIGINT into a cancel of the main task and raises only on a second one.
- A Node worker thread can be ended from its parent (`worker.terminate()`); every call in it ends with it.
- No new dependency and no config key (decider).

## Alternatives considered

1. **Liveness only** (503 while any call is past its deadline): one rule everywhere, but every late call restarts the
   replica and the calls beside it. Rejected as the first answer; it is the last rung.
2. **funcd-side harakiri** (funcd restarts a replica whose call outlives its deadline): no shim change, but funcd
   cannot tell a stoppable call from a stuck one, so it always restarts. Rejected, as 1.
3. **A process per call or per pool member**: always killable, but it multiplies memory on a RAM-bound host. Rejected.
4. **`sys.settrace`/`sys.monitoring` checks in the handler's thread**: no C API, but every call slows and C code
   still cannot be interrupted. Rejected.
5. **One event loop for all calls (asyncio or uvloop)**, measured on 3.14.4: async handlers run fine, but synchronous
   ones serialize (five 2 s calls took 10.1 s) and block liveness for about 10 s; uvloop 0.23.0 does not load in a
   subinterpreter; uvloop as the entry point handing calls to threads or subinterpreters kept liveness under 4 ms and
   served 12 % more calls per second, but a stuck call stays invisible to it. Rejected here: a new shim architecture
   that does not stop a stuck call; an idea for later.
6. **Document "handlers must return"**: no change, and #928 stays. Rejected.

## Decision

1. **Rung 1 — the handler reads its deadline.** On receipt the shim turns a valid `X-Funcd-Timeout-Ms` (a decimal
   integer in 1..2 147 482 647, as `callTimeoutMs`, `pool.ts:87-92`) into a deadline on a monotonic clock; a pool host
   passes it to the member with the call. The context's `remaining_ms()` (Python) and `remainingMs()` (TypeScript)
   return `max(0, deadline − now)` in whole milliseconds, floored, or `None`/`null` without a valid header; the value
   lives in the existing per-call carrier (`invcontext.ts`, set in `tracespan.ts:149`; `invcontext.py`). A call
   without a valid header has no deadline and no rung below applies.
2. **Rung 2 — the shim stops the late call**, after funcd has answered 504:
   - **Python, solo and pool.** At deadline + 1 s a per-call watchdog timer in the call's interpreter (the main one
     when solo, the member's subinterpreter in a pool), which the call cancels when it ends, raises
     `funcd_shim.DeadlineExceeded`, a `BaseException` subclass that `except Exception` does not catch, in a
     synchronous handler's thread with `PyThreadState_SetAsyncExc`. For an async handler it first cancels the task
     through its loop (`loop.call_soon_threadsafe(task.cancel, "deadline exceeded")`) and raises 1 s later only if the
     call still runs, as `asyncio.Runner` treats a second SIGINT. The raise and the call's end (deregister, then
     clear a raise that has not fired) happen under one lock, and the end tolerates a `DeadlineExceeded`, so a raise
     never reaches another call. The call answers as any raise does (ADR-0049's mapping): 500 `{"error": "deadline
     exceeded"}`. In a pool a call counts from its start in the member; one that starts after its deadline + 1 s is
     answered so without running the handler.
   - **TypeScript pool.** When the call's timer fires (deadline + 1 s, unchanged), the host answers 503 as today and
     pings the worker that served the call. No pong within 10 s means its event loop is blocked: the host ends that
     worker (`terminate()` on the pinged object, never a successor), and its exit takes the existing fault path
     (`fault()`: its other calls answer 503, and it restarts with backoff). A pong leaves it serving.
   - **TypeScript solo.** Nothing: a blocked event loop cannot answer liveness, so ADR-0215 restarts the replica.
3. **Rung 3 — liveness fails a call that will not stop.** `/health/liveness` answers 503 `stuck call` while a Python
   call that started before its deadline + 1 s is still running 11 s after its deadline, or while a TypeScript pool worker the host
   ended has not exited 10 s later; otherwise 200, as before. funcd then restarts the replica after
   `runtime.livenessTimeout`, as `probeOK` counts only a 200. Liveness still never calls funcd.
4. **Constants, not settings**: the stop margin 1 s (TypeScript reuses `timeoutMarginMs`), the async escalation 1 s,
   the stuck delay 10 s (also the TypeScript ping window).
5. **Release**: each language repo releases a minor version; funcd bumps both pins (ADR-0141).

## Temporary workarounds

None.

## Contracts

TypeScript, `shim/src/types.ts` (new method):

```ts
export interface FunctionContext {
  // log, invoke, kv, blob unchanged
  /** Milliseconds left before the call's deadline (X-Funcd-Timeout-Ms), floored at 0; null without one (ADR-0226). */
  remainingMs(): number | null;
}
```

Python, `shim/src/funcd_shim/types.py` and `__init__.py` (new; `DeadlineExceeded` joins `__all__`):

```python
class FunctionContext(Protocol):
    # log, invoke, kv, blob unchanged
    def remaining_ms(self) -> int | None:
        """Milliseconds left before the call's deadline (X-Funcd-Timeout-Ms), floored at 0; None without one."""
        ...


class DeadlineExceeded(BaseException):
    """Raised in a handler's thread after its deadline (ADR-0226). Catch it only to clean up."""
```

HTTP (the response mapping of ADR-0037 and ADR-0049 is unchanged; a stopped Python call is a raise):

| Answer | Before | After |
|---|---|---|
| TypeScript pool call at its timer | 503 `function <name> timed out` | unchanged; a blocked worker is also ended |
| `GET /health/liveness`, Python solo and pool host, TypeScript pool host | 200 while up | 503 `stuck call` in Decision 3's cases, else 200 |
| `GET /health/liveness`, TypeScript solo | 200 while up | unchanged |

Inside a pool (new fields and messages):

- TypeScript: the host's `Req {id, event, traceparent?, spanId?, links?}` (`pool.ts:52-58`) gains `deadline?: number`,
  a `process.hrtime` value in milliseconds (one monotonic clock for every thread of the process); host → worker
  `{ping: n}`, worker → host `{pong: n}`.
- Python: `_poolworker.invoke(...)` gains `deadline: float | None`, a `time.monotonic()` value (one clock for every
  interpreter of the process).

Dependencies and I/O:

| Consumes | Exposes |
|---|---|
| `X-Funcd-Timeout-Ms` (ADR-0151 Decision 6) | `remaining_ms()` / `remainingMs()` |
| `ctypes.pythonapi.PyThreadState_SetAsyncExc` (CPython C API, standard library `ctypes`) | `funcd_shim.DeadlineExceeded` |
| `Worker.terminate()`, `postMessage` (Node `worker_threads`) | liveness 503 `stuck call` |

## Implementation plan

1. **pyvvo/funcd-typescript** (`shim/src`): `types.ts`; `invcontext.ts` and `tracespan.ts` (the call's deadline);
   `shim.ts` (solo: the deadline from the header); `pool.ts` (the deadline to the worker, ping and pong, `terminate()`
   of a blocked worker, liveness 503); the rebuilt `shim.mjs` and `pool.mjs`. Tests beside the existing ones:
   `remainingMs` with and without the header, solo and pooled; a blocked worker is ended and restarted while its
   sibling serves; a late await is not ended; a worker that does not exit after `terminate()` makes liveness 503. A
   PR, then the release.
2. **pyvvo/funcd-python** (`shim/src/funcd_shim`): `types.py`, `__init__.py`, `invcontext.py`; `runtime.py`
   (`call_handler` records the call's thread, or its loop and task); a module for the watchdog and the in-flight
   registry that liveness reads; `shim.py` (solo); `pool.py` and `_poolworker.py`. Tests: unit tests, and the shim run
   as a subprocess (the delays are module constants a test may patch): a looping synchronous handler stops, an async
   one is cancelled, a spinning async one is stopped, a `time.sleep` handler makes liveness 503, a pool member stops
   while its sibling serves, a pooled call that starts late does not run. A PR, then the release.
3. **funcd**: `go get github.com/pyvvo/funcd-typescript@<tag> github.com/pyvvo/funcd-python@<tag>`. In `pkg/funcd`
   (the process runtime and the real shims, `WithPacing` as `health_e2e_test.go:74`), one `TestScenario<Name>` per
   scenario with `// scenario: <name>`, in parallel, except `stuck-python-call-stopped`, whose test is
   `TestIssue928_StuckPythonCallStops`. Docs: the FEAT-0010 F125 row, the blueprint's health line, the back-links.
4. Verify: each language repo's checks in its PR; in funcd, the scenario tests, then `scripts/agent/gate.sh` once.

**Definition of done**: both shims are released with their tests passing; funcd pins the tags; every scenario test
passes, and `TestIssue928_StuckPythonCallStops` fails with the previous pins; the gate passes; no username or absolute
path in a changed file.

## Review checklist

- [ ] `remaining_ms()` / `remainingMs()` count down on a monotonic clock, floored, `None`/`null` without a valid
      header, solo and pooled, from the existing per-call carrier.
- [ ] Python: at deadline + 1 s a synchronous call gets `DeadlineExceeded` from a per-call timer in its own
      interpreter; an async call is cancelled, then raised 1 s later if it still runs; raise and end share one lock;
      no raise reaches another call; no watchdog thread outlives its call; a pooled call that starts late is not run.
- [ ] TypeScript pool: at the call's timer, a ping to that worker; no pong within 10 s ends that worker through
      `fault()`; a pong ends nothing.
- [ ] Liveness answers 503 `stuck call` only in Decision 3's cases and never calls funcd; TypeScript solo liveness is
      unchanged.
- [ ] No deadline, watchdog or ping for a call without a valid header (the TypeScript pool's 30 s answer stays); no
      config key; no new dependency.
- [ ] funcd changes only `go.mod`, `go.sum`, tests and docs; one test per scenario, named for it.

## Consequences

- A stuck Python call that runs Python code is stopped in place; its replica or pool member serves on with no restart.
- A blocked TypeScript pool worker is replaced without restarting the host, at most 11 s after the late call's timer.
- A call that cannot be stopped now ends with its replica, about `runtime.livenessTimeout` + 11 s after its deadline,
  instead of never. funcd still logs that restart as "restarting a replica silent on its liveness" (ADR-0215).
- Handlers can budget their work, as Lambda's `get_remaining_time_in_millis` allows.
- A handler can catch `DeadlineExceeded` and go on; it then reaches rung 3.
- Rung 3 restarts the whole replica, so the calls beside the stuck one end too. Ending a blocked TypeScript worker ends
  its other calls, including one that blocked it while within its own deadline.

## Open questions

1. Should a call's remaining time follow it into its `ctx.invoke` calls, as gRPC propagates deadlines? A later ADR, if
   wanted.

## References

- Issue #928 (the L13 probe); ADR-0151; ADR-0215; ADR-0030; ADR-0044; ADR-0049; ADR-0050.
- CPython: [`PyThreadState_SetAsyncExc`](https://docs.python.org/3/c-api/init.html#c.PyThreadState_SetAsyncExc);
  [PEP 734](https://peps.python.org/pep-0734/); [asyncio Runner and SIGINT](https://docs.python.org/3/library/asyncio-runner.html#handling-keyboard-interruption).
- Node.js: [`worker.terminate()`](https://nodejs.org/api/worker_threads.html#workerterminate).
- Precedents: [AWS Lambda context](https://docs.aws.amazon.com/lambda/latest/dg/python-context.html) (remaining
  time); [uWSGI harakiri](https://uwsgi-docs.readthedocs.io/en/latest/Options.html#harakiri);
  [Celery soft and hard time limits](https://docs.celeryq.dev/en/stable/userguide/workers.html#time-limits);
  [gRPC deadlines](https://grpc.io/docs/guides/deadlines/).
