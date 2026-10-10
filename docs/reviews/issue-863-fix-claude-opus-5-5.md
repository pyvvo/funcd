# Fix review — issue #863 — claude-opus-5-5

- **Issue**: #863 — a redeployed pooled member reads `Degraded` while its new pool worker boots, although the old pool
  worker still serves it at its serving revision; it loses its route and the resolver hands it out as not ready.
- **Model**: claude-opus-5-5
- **Branch**: `fix/863-pooled-redeploy-follows-serving`, one commit `7bf122b8` on `origin/main` (`ec274537`)
- **Change**: `internal/function/pool.go` (+15/−4), `internal/function/pool_drain_test.go` (+92)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor; checklist 12/12.

## What the fix does

`convergePooled` judged the member on the current manifest's pool worker (`pass.current`) whenever S ≠ C, so while the
old pool worker was the one the resolver hands out (ADR-0190 Decision 8: the newest listening worker) the member had no
readable entry and `finish` took the `v.serving` row → `Degraded`/`Restarting`. The fix adds one case: when the
handed-out worker is an old one (not in `pass.current`) and the member has served, the member's phase and `Ready` follow
its entry on that worker, and the verdict is marked `switching` with `booting = pass.running > 0`, as the solo switch
pass reports S while C boots (`function.go` `verdict{… switching: true …}`, ADR-0143 Decision 5). The existing switch
(`ServingRevision = CurrentRevision` once the current worker reports the member ready) is unchanged.

## Verification (all run through `scripts/agent/d`)

| Check | Result |
|---|---|
| `TestIssue863_*` (2 tests), `-race`, with the fix | `ok internal/function 1.6s` |
| Same tests with `origin/main`'s `pool.go` overlaid (`go test -overlay`) | both FAIL: `expected "Ready" actual "Degraded"` at the first redeploy pass — the issue's reason |
| Mutant: drop `v.ready = 1` in the new case | killed (both #863 tests) |
| Mutant: invert `!slices.ContainsFunc` (apply the case to the current worker) | killed (#863 ×2, `TestPooledMemberServesItsCurrentRevision`, `TestPoolRebuildServesInFlightCall`, `TestPoolRebuildKeepsOldUntilNewListens`) |
| Mutant: `v.switching = false` | killed (both #863 tests: `RevisionReady` reason) |
| Mutant: `v.booting = false` | killed (`TestIssue863_PooledRedeployFollowsServingRevision`: requeue 200 ms) |
| `go test -race ./internal/function/` | `ok 10.6s` |
| `go build ./...`, `go vet ./internal/function/`, `golangci-lint run ./internal/function/...` (darwin) | green, `0 issues` |
| `GOOS=linux` build, vet, lint (host lint binary against the linux build, as `scripts/agent/gate.sh` does) | green, `0 issues` |
| `gofmt -l` on the package | clean |
| Repo-wide gate / CI | not run here (once per PR); no `.cache/gate` in the worktree |

## Checklist

1. ✅ `TestIssue863_PooledRedeployFollowsServingRevision` reproduces the issue's three steps (`pooledPair`, `setHoldNew(true)`,
   new image on `b`, three passes) and asserts phase, `Ready`, `RevisionReady` False/Progressing, the route, the
   resolver's upstream (the old worker's URL), the 200 ms requeue, a call answered by the old worker, and the switch
   once the new worker is released. `TestIssue863_SilentNewPoolWorkerLeavesOldServing` covers the boot-timeout path.
2. ✅ Fails pre-fix for the reported reason (Degraded instead of Ready).
3. ✅ Passes with the fix under `-race`, un-skipped.
4. ✅ Four mutants on the fix's lines each fail a test.
5. ✅ Cause, not symptom: the case the issue names (`pass.current` judged while S ≠ C, `switching` never set) is the one
   changed; no timeout, retry or swallowed error.
6. ✅ Scope: two hunks in `pool.go` (code + the doc comment) and the tests; no test weakened or deleted.
7. ✅ No ADR edited. Conforms to ADR-0143 Decision 5 (phase/Ready follow S; `RevisionReady` describes C), ADR-0190
   Decision 8 (old worker serves until the new one listens; a new worker that never listens leaves the old one serving —
   the second test), ADR-0158 Decision 4 (the silent worker is restarted on `bootTimeout`; member mapping unchanged for
   the S = C case).
8. ✅ Build, vet, lint and the package's tests green on darwin and linux; gate/CI per PR.
9. ✅ Conventions: ctx-first, no `any`, no new deps, imports at top level, comments carry the why and cite the ADRs;
   the `//nolint:noctx` on the test's `http.Get` is justified inline and mirrors the package's plain-HTTP fake workers.
10. ✅ Reuse: the test uses the existing harness (`pooledPair`, `serveWorker`, `setHoldNew`, `hold`, `poolWorkers`,
    `upstream`, `requireCondition`, `otherArtifact`, `counts`, `wasRemoved`); the only new helper, `routeFor`, is a
    one-line name check on `h.routes(t)`, which no existing test did by name (they use `Len`/`Empty`). The code reuses
    `servingPool`, `memberIn` and the `verdict` switching fields the solo path already drives; the supervision path's
    `countWorkers` already judges a pooled member on `servingPool`, so the fix brings `convergePooled` in line with it
    rather than adding a second mechanism.
11. ✅ `fix(function): …`, `Fixes #863`, attribution trailer, one issue in one commit.
12. ✅ Every case the issue describes (status, condition, route, upstream, held call) is asserted; the sibling path
    (`countWorkers`, used by the supervision passes) already used the handed-out worker, so no sibling is left.

## Observations (no finding)

- Once the new pool worker listens, ADR-0190 Decision 8 hands it out and the member is judged on it; while its entry
  reads `loading` the member is `Degraded`, which ADR-0158 Decision 4 states explicitly (`loading … → not ready
  (Degraded once serving)`). That window is outside this issue (the old worker no longer serves then) and is
  ADR-sanctioned; a change would be an ADR, not a fix.
- The `Restarting` message in that remaining window ("a replica exited and is being replaced") is pre-existing wording.

## Recommendation

Pass. Merge through the per-PR gate; nothing to hand back to `/fix`.
