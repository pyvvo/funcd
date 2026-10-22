# ADR-0259: A late health answer keeps a worker's last readiness for `runtime.readinessTimeout`

- **Status**: Implemented (2026-10-22), review passed on its first round, its five Minors fixed before the stamp
  (docs/reviews/adr-0259-implementation-claude-opus-5-5.md); Reviewing (2026-10-22), implemented in the PR that closes
  #1127; Accepted (2026-10-22). The decider chose option A on #1127 with the window as a config key, 5 s by default,
  and accepted this text ("accept, ship it"), confirming the drafting agent's four choices: the rule on the solo steady
  path too, only a timeout as late, the names, and no window for a worker with no recorded answer. Judged twice: round
  1's three Majors and six Minors and round 2's four Minors are folded in; round 2's verdict: accept.
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: function, health, readiness, pooling, status, config
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — truthful Ready) (#1127)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0259` back-link at acceptance, all Implemented
  (origin/main lines). Each clause still holds for a read that is not late, and once the window has passed:
  - [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 3 (128, 130-131): the steady pass "returns
    `Result{RequeueAfter: period}`", and a full pass "that ends `Ready` also returns `RequeueAfter: period`". Within a
    window, both come back by its end (Decision 4).
  - [ADR-0161](0161-truthful-function-ready.md) Decision 2 (144): "`readyReplicas` counts a replica ready only when
    listening and its probe returns 200".
  - [ADR-0215](0215-built-in-health.md) Decision 4 (124): "past which `memberIn` fails every member".
  - [ADR-0221](0221-degraded-recovers-under-gate.md) Constraints & Decision drivers (95-96): "unanswered = not ready …
    unanswered = an error"; Decision 2 (128): "an unanswered pool host keeps the status as read"; Decision 4
    (137-138): "A solo probe that times out … counts not ready" and "An unanswered `/health/members` is an error".
- **Relates to**: ADR-0215 Decision 1 (liveness, unchanged) · ADR-0224 (the pool switch, unchanged) · ADR-0163,
  ADR-0194 (durations) · ADR-0047 (writes only on change) · #75 (why a probe has 100 ms) · #838, #1126

## Context & Need

On every pass funcd reads each worker's readiness: a solo replica's `/health/readiness`, and a pool host's
`/health/members` for all its members. Each read is bounded by `probeTimeout` (100 ms,
`internal/function/function.go:1271`), so a hung worker never slows the controller's shared worker (#75). A read past
that bound fails. A pooled member then has no ready worker and takes the exit path (`function.go:1102`): the Function
goes Ready → Degraded `Restarting` "a replica exited and is being replaced" → Ready, with five status writes for one
late answer, though nothing exited (#1127, seen in #1126's rig with a host that answers in 150 ms). A solo replica's
full pass counts a late answer as not ready in the same way. The solo steady pass, by contrast, ends only on a
dependency report (`function.go:1317`), so a solo replica that never answers stays Ready there while its liveness
answers.
Liveness tolerates `runtime.livenessTimeout` (30 s) of silence. On 2026-10-22 the decider chose, on #1127, that a late
answer is no answer rather than a failure, for a window set in funcd's config, 5 s by default.

## Scenarios

Fixture: a Ready Function, either pooled (one pool host) or solo (one replica, or two for the full-pass case), with
`runtime.readinessTimeout` at its 5 s default. A late read is one that runs past `probeTimeout`.

- **scenario: late-answer-keeps-ready** — *When* the pool host's `/health/members` (pooled) or the replica's
  `/health/readiness` (solo) answers late for less than 5 s and then on time, *Then* the Function stays `Ready`, its
  status is not written, and each pass comes back by the window's end. A full pass in the window, such as one after a
  sibling replica exited, counts the replica by its last answer, so the Function stays `Ready` (the exit still
  changes `status.replicas`).
- **scenario: unanswered-past-window** — *When* the reads stay late for 5 s, *Then* the phase is `Degraded`, with
  `Ready=False`, reason `NotAnswering` and the message `the pool host has not answered /health/members for 5s`
  (pooled) or `a replica has not answered /health/readiness for 5s` (solo). *When* the worker answers again, *Then*
  the Function is `Ready`.
- **scenario: exit-seen-at-once** — *When* the pool host or the replica exits during the window, *Then* the next pass
  reports `Restarting` "a replica exited and is being replaced", as today. Its replacement has no recorded answer, so
  its late reads keep that message past the window.
- **scenario: readiness-timeout-config** — *Given* `runtime.readinessTimeout: 2s`, *Then* the window is 2 s and the
  message says `for 2s`. *Given* `0s`, `-1s` or `abc`, *Then* funcd refuses to start and names the key.

## Scope

- **In**: how a late readiness read is judged, for solo replicas and pool members; the config key; the status wording
  past the window.
- **Out**: `probeTimeout`, which stays 100 ms; liveness and its restart (ADR-0215 Decisions 1-2); the pool switch's
  read of `next` (ADR-0224 Decision 3); funcdctl dev's `platform` keys (ADR-0254). A read that is not late is judged
  by what it says, as today.

## Constraints & Decision drivers

- A probe never costs the shared worker more than `probeTimeout` (#75).
- The rule is the same for solo and pooled Functions; placement differs only in the reconciler.
- The decider wants chosen numbers as config keys with defaults.
- A worker that exits is still seen at once.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen (A): a late read counts by the worker's last answer for `runtime.readinessTimeout` (5 s) | No flap; probes stay at 100 ms; operators can tune the window | A worker that hangs while it still runs keeps its last readiness for up to 5 s | Chosen by the decider |
| A, with the solo steady pass unchanged (the window in full passes only) | A steady solo Function never demotes on readiness, as today | Two rules by placement; a solo replica that never answers stays Ready while its liveness answers | Rejected: one rule (confirmed by the decider) |
| A, with any failed read counted as late (refused, reset, non-200) | Simpler | A dead port keeps its last readiness for the window | Rejected: "late" is what the decider chose |
| A, with the window set to `runtime.livenessTimeout` | Reuses one existing key | 30 s is too long (decider); its lower bound of twice `runtime.supervisionPeriod` rules out short windows | Rejected by the decider |
| B: fix only the message | Smallest change | Still flaps, with five writes per late answer | Rejected |
| C: raise `probeTimeout` to about 1 s | Simple | A hung worker slows every pass by 1 s (#75); a busier host still exceeds it | Rejected |

## Decision

1. **A late read.** A readiness read is late when its deadline, `probeTimeout`, passes before a full answer arrives.
   Any other outcome is an answer, judged as today: a refused or reset connection, a non-200, or, for
   `/health/members`, a body that is no member list (`memberIn` then fails, as today).
2. **The window.** For each worker, funcd keeps in memory, beside its liveness record (ADR-0215 Decision 1), the
   worker's last answer and the time of the first late read after it. The record is keyed by the worker's id and
   `CreatedAt`, and dropped whenever funcd creates a worker under that id, retires it or forgets its pool. For less than
   `runtime.readinessTimeout` after that first late read, every readiness read of the worker counts by its last answer:
   a solo replica by its verdict (ready, a dependency report, or not ready), a pool member by its entry in the host's
   last member list. A kept answer is judged as it was when it arrived, so a kept non-200 or a body with no member list
   makes `memberIn` fail. Any answer closes the window. A worker with no recorded answer counts as not ready, as today,
   with today's reason and message. A kept answer never counts as liveness. The pool switch (ADR-0224 Decision 3), a
   placement step, keeps its own read of `next` and records that answer, but never counts by a kept one; a revision
   switch, solo or pooled, counts a kept answer as every readiness judgment does, since a kept "ready" exists only after
   a real one.
3. **Past the window.** The read counts as not ready, as today, with one change to the status: where a serving
   Function or held revision has no ready worker and every worker it judges runs and is late past its window, the
   phase is `Degraded`, with `Ready=False`, reason `NotAnswering` and the placement's message (table below), instead of
   `Restarting` "a replica exited and is being replaced". A start error, a crash loop, a repair, a liveness restart or
   a dependency report keeps its own reason and message.
4. **Pacing.** While a worker is within its window, the steady pass, `finish`'s pass and a held revision's pass come
   back no later than the window's end, so the window ends on a read. The gate path (ADR-0221) keeps its pacing; past
   the window it keeps the status as read. A solo `steadyState` returns false for a replica late past its window.
   Status is written only when it changes (ADR-0047).
5. **Config.** `runtime.readinessTimeout` (env `FUNCD_RUNTIME_READINESS_TIMEOUT`) is the window, not the bound of one
   probe: a positive duration parsed by `parseDuration` (ADR-0194), 5 s by default, with no ordering against other
   keys.

| Placement (reconciler) | Read | Last answer kept | Message past the window |
|---|---|---|---|
| Solo | `/health/readiness` per replica (`probeReadiness`) | the replica's verdict: ready, a dependency report, or not ready | `a replica has not answered /health/readiness for <window>` |
| Pooled | `/health/members` per pool host (`memberIn`), shared by its members | the host's member list | `the pool host has not answered /health/members for <window>` |

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config, Config.Runtime:
// ReadinessTimeout is the window in which a worker whose readiness reads run late keeps its last answer, not the
// bound of one probe (ADR-0259): a positive duration; empty ⇒ 5s.
ReadinessTimeout string `json:"readinessTimeout,omitempty" env:"FUNCD_RUNTIME_READINESS_TIMEOUT"`

// pkg/funcd, Pacing:
ReadinessTimeout time.Duration // zero ⇒ 5 s

// internal/function, Deps:
ReadinessTimeout time.Duration // 0 ⇒ 5 s; negative is refused

const reasonNotAnswering = "NotAnswering" // Ready's reason past the window (ADR-0259 Decision 3)
```

| Direction | What |
|---|---|
| Consumes | `runtime.readinessTimeout`, `probeTimeout`, the injected clock |
| Exposes | Ready reason `NotAnswering` with its two messages; the commented key in `examples/funcdconfig.yaml` |

## Implementation plan

- **funcd**, one PR that closes #1127 (`Fixes #1127`):
  - `internal/platform/config/config.go`: the key. `cmd/funcd/main.go`: a `pacing` entry for
    `runtime.readinessTimeout` (default 5 s, `minPositive`). `pkg/funcd`: a `Pacing` field passed to
    `function.Deps`. `examples/funcdconfig.yaml`: a commented line beside `livenessTimeout`.
  - `internal/function/readiness.go`: the record (`reads`, guarded by its own mutex), `settleRead` (records a read and
    returns the answer it counts by and its window: the end of a kept read's window, or the window when late past it),
    `readReadiness` for solo replicas, and `lateRead` (a deadline passed: `context.DeadlineExceeded` or a `net.Error`
    whose `Timeout()` is true).
  - Seams: `probeReadiness` and `probeMembers` also return whether the read was late; `memberIn` returns a `memberRead`
    (worker, entry, ok, window); `steadyState` returns `(steady, until)`; `verdict` and `readiness` gain `readUntil` and
    `silent`. `readyReplicas` sets `silent` only when every replica it judges, an exited one waiting to be created again
    included, runs and is late past its window. `finish`, `writeHeld`, `record` and the steady return in `Reconcile`
    follow Decisions 3 and 4. `switchDue` records its answer. `forgetRead` runs at each worker `Create`; `forgetWorker`
    and `forgetLive` also drop records.
  - Tests on the fake clock, with workers that answer only after `probeTimeout`: one per scenario, named after it,
    for both placements; `TestIssue1127_LateMembersAnswerKeepsReady` (no status write for one late answer); a solo
    full pass in the window; an exit beside a replica late past its window, which keeps `Restarting`; a reset
    connection judged at once; a set window that changes the message; a `pacing` test for the key's default and its
    refusals. The harness's `Deps.ProbeTimeout` (#1126) stays at 500 ms, so each late
    read costs that much.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass and `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] Only a read past `probeTimeout` is late; a refused or reset connection, a non-200 or a body with no member list
      is judged at once, as before.
- [ ] A late read counts by the worker's last answer for less than `runtime.readinessTimeout` after the first late
      read; any answer closes the window; a worker with no recorded answer counts as not ready with today's message.
- [ ] The record is keyed by id and `CreatedAt` and dropped at each `Create`, at retire and with the pool; an exit is
      reported at once.
- [ ] Past the window, a serving Function or held revision whose every judged worker runs and is late reads `Degraded`
      `NotAnswering` with its placement's message; an exited worker or another cause keeps its own.
- [ ] `probeTimeout` is unchanged; a kept answer never marks liveness and never lets the pool switch happen; the
      switch's real answer is recorded.
- [ ] Within the window the steady pass, `finish` and a held revision's pass come back by the window's end; the
      status is not written while a kept answer holds; `steadyState` ends past the window.
- [ ] `runtime.readinessTimeout` defaults to 5 s, must be positive, and is refused otherwise with the key named; the
      key is in `examples/funcdconfig.yaml`.
- [ ] One test per scenario, for both placements.

## Consequences

- **Positive**: a late answer no longer flaps a Function or writes its status; the status names the real cause; an
  operator sets the window.
- **Negative**: a worker that hangs while it still runs keeps its last readiness for up to the window, so calls can
  reach it meanwhile and wait out their own timeout. A steady solo Function whose readiness stays late for the window
  turns `Degraded`, where today it stays Ready while its liveness answers; an answered non-200 with no report still does
  not end its steady state. Each window costs one extra pass, at its end. funcd keeps one more record per worker in
  memory.
- **Risks accepted**: a worker with no recorded answer, none since it was created or since funcd started, keeps
  today's judgment for as long as its reads stay late: a pooled member reads `Degraded` with today's message "a
  replica exited and is being replaced", and a steady solo replica stays Ready. This is no worse than today.

## Open questions

None. The two the drafting agent chose, the rule on the solo steady path and no window for a worker with no recorded
answer, were confirmed at acceptance.

## References

- #1127, #1126, #838, #75; ADR-0142 Decision 3; ADR-0161 Decision 2; ADR-0215 Decisions 1 and 4; ADR-0221 Decisions
  2 and 4; ADR-0224 Decision 3.
- `internal/function/pool.go` (`memberIn`, `switchDue`); `internal/function/liveness.go` (`probeReadiness`, `live`);
  `internal/function/function.go` (`probeTimeout`, `probeMembers`, `finish`, `requeueFor`, `steadyState`,
  `readyReplicas`, `convergeHeldRevision`).
