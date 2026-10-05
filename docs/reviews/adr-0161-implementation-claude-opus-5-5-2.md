# ADR-0161 implementation review — claude-opus-5-5 (loop 2)

- **ADR**: docs/adr/0161-truthful-function-ready.md (Accepted; Realizes FEAT-0000/F13)
- **Work**: one commit `db1d4630` on `origin/main` (`45f90a4a`, which carries ADR-0158 from #677); 12 files,
  +1046/−162, all under `internal/function/` plus the `Replicas` doc comment in `api/types/v1alpha1/function.go`.
- **Model**: claude-opus-5-5
- **Decider context**: ADR-0158 is merged, so Decision 2's `/health/members` clause is required here (0161 lands second).
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model-attributed).

## Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| Minor 1: `TestScenarioHungReplicaNeverHandedOut` held replica 1, so the solo listening check survived it | resolved | The test now holds replica 0 (`ready_test.go`); mutant m2 below (hand out any running worker) now fails this scenario test as well as `TestUnlistenedPoolWorkerIsNotHandedOut`. |
| Minor 2: old-rule doc comments | resolved | `switchSolo`'s comment reads "s's listening workers serve until the switch"; `readyReplicas`' comment reads "each listening replica's health endpoint". `runningReplicas` still says "running", which is what it counts (the converge step's count), so it is correct. |

## New in this loop: the `/health/members` read (Decision 2)

`listeningCount` (function.go:1036) resolves a pooled Function's pool key with `pooling.ParsePool`, counts the pool
worker's listening instances through `namedInstances`, and returns 0 unless the member's own entry, read through the new
`memberIn` (pool.go:219, factored out of ADR-0158's `memberState` with no behaviour change), reads `memberReady`. The
read runs only in shim mode (`r.materializer != nil`) and only when a worker listens. `upstreamForFn`, `readyReplicas`
and the pool liveness probe are unchanged by it, as the ADR scopes the clause to `listeningCount` only.
`TestFailedPassCountsPooledMemberOnlyWhileReady` fails only `ensurePool`'s `List` (via `calledFrom`), so `failPass`'s own
read succeeds: the member reads ready → `Ready`, `replicas: 1`, `RevisionReady=False/StartFailed`; the member reads
`loading` → `Degraded`, `Ready=False/StartFailed`. Mutant m1 proves the test pins the read.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/function/... ./internal/activator/... ./api/...` | exit 0 |
| `GOOS=linux go vet` (same packages) | exit 0 |
| `go tool golangci-lint run ./internal/function/... ./internal/activator/... ./api/...` | 0 issues, exit 0 |
| `GOOS=linux` golangci-lint (host-built binary from `go tool -n`), same packages | 0 issues, exit 0 |
| `go test -race -count=1 ./internal/function/... ./internal/activator/... ./api/types/...` | exit 0 (function 12.3 s, activator, storescaler, v1alpha1 `ok`) |
| `go test -race -count=3 -run 'TestScenario\|TestIssue353\|TestIssue309\|TestIssue354\|TestGateFailedWritesListeningCount\|TestUnlistenedPool\|TestFailedPassCountsPooled\|TestServingRevisionComesBack\|TestADR0149_Registry' ./internal/function/` | exit 0 |
| Linux, Docker on colima (`golang:1.26.4`, tar of the worktree): `go test -race ./internal/function/...`, `-count=1` and then `-count=6` | `ok` both times (13 clean runs) |
| `go.mod` / `go.sum` | unchanged |
| skipped tests added | none |
| docs / blueprint / ADR | untouched by the commit (the ADR is still `Accepted`; F13 reads `truthful Ready: accepted`) |

Not run, by instruction: e2e, `go test ./...`, Lima lanes, `just ci`.

### Overlay mutants (`go test -overlay`, `./internal/function/`)

| # | Mutation (function.go) | Result |
|---|---|---|
| m1 | `listeningCount` skips the `/health/members` read (`if false && pooled && …`) | killed by `TestFailedPassCountsPooledMemberOnlyWhileReady` |
| m2 | `upstreamOf` hands out any `Running` worker instead of `r.listening(in)` | killed by `TestScenarioHungReplicaNeverHandedOut` and `TestUnlistenedPoolWorkerIsNotHandedOut` |
| m3 | `failPass`: `case started == v1.PhaseReady && n >= 1` → `case n >= 1` | **survives** (Minor 1) |
| m4 | `gateFailed`: `case listening >= 1 && fn.Status.Phase == v1.PhaseReady` → `case listening >= 1` | **survives** (Minor 1) |

m1 and m2 cover the two key lines of this loop. m3 and m4 were run to probe the guards behind the rejected
alternative "`Degraded → Ready` from a failed pass or gate".

## Findings

### Blocker

None.

### Major

None.

### Minor

1. **The "read phase `Ready`" guard of `failPass` and `gateFailed` is not tested** (model).
   `function.go:642` (`case started == v1.PhaseReady && n >= 1`) and `function.go:746`
   (`case listening >= 1 && fn.Status.Phase == v1.PhaseReady`) are the code behind "Only `finish` makes a `Degraded`
   Function `Ready`" (Decision 1) and the table row "read phase not `Ready` → `Degraded`" (Decision 2). The ADR rejects
   that alternative because it causes #309-style flapping. When either condition is dropped (m3, m4), the whole package
   still passes. The code is correct. No test reaches a failed pass or gate on a `Degraded` Function that still has a
   listening worker. For example, `TestIssue309_ListenedHungReplicaIsReplaced` has a listened replica that answers 503;
   a registry outage or a gate at that point would cover it. Under m3, that case writes phase `Ready` with
   `Ready=False` and programs a route to the replica that answers 503. The `dead` case of
   `TestScenarioRegistryOutageNotReady` covers "any other is `Degraded`" only when no worker listens.

### Noted, not a finding

- **Timing flakes under host load (pre-existing, env).** When the host runs a parallel `go test -race -count=4` and
  Docker runs `go test -race -count=12` on Linux, `internal/function` fails on the work and on `origin/main` alike. On
  the work, `TestScenarioPooledFailedMemberNeverIdle/handler_cannot_load` (pool_test.go:577, from ADR-0158) and
  `TestReclaimDuringRepairBackoffIsNotAShapeFailure` (supervision_test.go:275) fail. On the base,
  `TestScenarioKilledWhileBootingIsRetried/fake` and `TestScenarioBootCrashBesideReadyReplica` (ADR-0160) fail. The
  commit does not touch any of these tests, and the unloaded runs are clean. These are wall-clock-sensitive tests
  that predate this work. They are worth a flaky-test issue, but they do not count against the model.
- `TestScenarioHungReplicaNeverHandedOut` holds replica 0 and expects replica 1, the reverse of the scenario's wording
  ("replica 1 never listens … all get replica 0"). The loop-1 review asked for this deliberately: because
  `upstreamOf` hands out the lowest listening replica, only this direction discriminates. The scenario's rule (a
  replica that never listens is never handed out) is what the test asserts.
- `listeningCount` returns 0 when the pooled Function's `status.pool` does not parse or its member entry cannot be
  read. This follows "counts … only while its `/health/members` entry reads `ready`".

## Contracts — checked against the code

Every contract item listed in the loop-1 report still holds at the same functions (rebased; line numbers shifted by
the ADR-0158 merge). This loop re-checked these items:

| Contract item | Where | Holds |
|---|---|---|
| `listeningCount(ctx, fn) (int, error)` — serving (else current) revision, or the pool worker; with ADR-0158 the pool worker counts only while the member reads `ready` | function.go:1036, pool.go:219 | yes |
| `listening(in)` — `Running` + `Listened` + `IP` + `Port`; legacy: `Running` | function.go:1027 | yes |
| `upstreamOf`/`upstreamForFn` return the `List` error; lowest listening replica; `programAllRoutes` returns the error and programs nothing; `endpoints.Upstream` reads it as not ready | function.go:1816, 1850, 1875, 2062 | yes |
| `failPass`, `sameIgnoringMessages`, `convergeError`/`routeError`, single wrap in `Reconcile` | function.go:403, 435, 607, 625, 670 | yes |
| `gateFailed` per the Decision 2 table; `zeroReplicas` gone from all 10 `gateFailed` call sites | function.go:716 | yes |
| `stopUnlistened`/`unlistened`, `verdict.pollAt`, `requeueFor`, `bootBackoff.timedOut` message | function.go:1190, 1201, 916; bootbackoff.go:133 | yes |
| `readyReplicas` probes only listening replicas; solo limit only on a listening replica; pool path unchanged; `r.clock` | function.go:1915 | yes |
| Both `planReplicas` call sites pass `r.clock.Now()` | function.go:1404-1405; pool.go:317, 342 | yes |

## Review checklist

- [x] Every error up to the pass's own status write goes through `failPass`. No site writes the status itself:
      `store.Update` is called only in `failPass`, `gateFailed` and `record`. A `List` error programs no routes.
- [x] One `listening` rule, applied through `namedInstances`, now also for the pooled member's count. The steady state
      makes one `runtime.Status` per replica. `gateFailed` follows the table (the read-phase rows are untested, see
      Minor 1). `stopUnlistened` and `pollAt` follow Decision 3. Every clock read listed in plan step 1 uses `r.clock`.
      No username or absolute path.

## Verified correct — keep it

- The `/health/members` read sits in one place (`listeningCount`). `memberIn` is factored out of ADR-0158's
  `memberState` rather than duplicated, so the member entry is parsed by one code path.
- The new test fails only `ensurePool`'s `List` and leaves `failPass`'s own read working, so it isolates the clause
  exactly. m1 proves the test pins it.
- The loop-1 test gap is closed: the solo resolver rule is now pinned by its own scenario test (m2).
- The single wrap point in `Reconcile`, the byte-identical `restoreCondition`, and the reuse of ADR-0160's
  `bootBackoff` for hangs are unchanged from loop 1.
- The Linux and darwin build, vet and lint are clean. No new dependency, API field, config key or port method.

## Recommendation

Pass. Minor 1 is a test to add in a follow-up: a failed pass and a failed gate on a `Degraded` Function whose
replica listens but answers 503 must stay `Degraded`. It does not block `Reviewing → Implemented` in the wave's
docs PR. File the load-sensitive timing tests (present on `origin/main` too) as a flaky-test issue.

```json
{
  "date": "2026-10-05",
  "adr": "0161",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0161-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: both loop-1 minors resolved; ADR-0158 /health/members read added in listeningCount and pinned by TestFailedPassCountsPooledMemberOnlyWhileReady; darwin+Linux build/vet/lint green, race tests green (Linux in Docker x13); mutants m1/m2 killed, m3/m4 survive: read-phase-Ready guard of failPass/gateFailed untested (model); load-sensitive timing flakes also on origin/main (env, not scored)"
}
```
