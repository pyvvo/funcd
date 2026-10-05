# ADR-0190 implementation review — slice 3 of 3 (pools, Decision 8) — claude-opus-5-5 — loop 2

- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision`, `b0c34bdb` ("drain a pool rebuild and run a
  non-current pinned member solo"). It amends the loop-1 commit `2ba44e0b` on the same parent (`43b7e63e`, slice 2).
  The loop-2 delta (`git diff 2ba44e0b b0c34bdb`) touches `internal/function/function.go`, `internal/function/pool.go`,
  `internal/function/pool_drain_test.go`, `internal/function/pool_test.go` and `pkg/funcd/run_revision_pool_e2e_test.go`.
- **Scope judged**: S3 only (Decision 8): a pinned non-current pooled member runs solo; the rebuild starts a second
  pool worker named by the manifest signature; the rebuild is due when no listed worker holds the current signature;
  the resolver hands out the newest listening worker; the drain clock starts at listen and retires the old worker on
  `CallTracker.Idle(upstream, HandOutSettle)` or `DrainGrace`; the socket's member set is the union until the old
  worker retires; the stopped and liveness-silent restarts stay stop and create. Scenario `pooled-edit-isolated`, plus
  the resolver-after-restart unit test.
- **Verdict**: **pass**. 0 Blockers, 0 Majors, 2 Minors (1 model, 1 adr carried over). All loop-1 model findings are
  resolved and proven by tests that fail on the loop-1 code.

## Loop-1 findings

| Loop-1 finding | Status | Evidence |
|---|---|---|
| **M1** (Major, model): a call pinned to a pooled member's current revision got the old pool worker, with the member's previous code, while the new worker booted | **Resolved** | `endpoints.pinned` now calls `pinnedPoolUpstream` (`internal/function/function.go`). It hands out only the newest listening worker whose signature equals the manifest last built for the key (`poolHolds`, recorded by `holdPool` in `ensurePool`), and only while that manifest holds the member at the pinned Revision's image, digest and handler. The new `TestPoolPinnedCurrentWaitsForCurrentManifest` is the loop-1 probe as a committed test. With `function.go` and `pool.go` overlaid from `2ba44e0b` it fails: `Should not be: "http://127.0.0.1:<port>/function/b"` / "the old pool worker holds b's previous code". It passes on HEAD. Mutant B (below) also shows the signature filter is load-bearing |
| **m1** (Minor, model): the liveness-silent restart (`:350`) had no in-place assertion | **Resolved** | `TestIssue422_NeverReadyPoolWorkerIsReplaced` ("silent" subtest) now asserts `Len(before, 1)` and that the pool workers after the reconcile equal the ones before it ("in place: no second pool worker beside it"). It passes on HEAD and with the slice reverted, as a regression guard should |
| **m2** (Minor, model): the e2e proved "solo" only through the Revision phase | **Resolved** | `bPoolImage` logs `pool` or `solo` from `process.env.FUNCD_POOL_MANIFEST`, which the pool worker spec sets (`internal/function/pool.go`, `env["FUNCD_POOL_MANIFEST"]`) and a solo worker does not. The scenario now asserts `"v1 solo\n"` for r1's b and `"v1 solo\nv2 pool\n"` after r2. It passes |
| **m3** (Minor, adr): the upgrade cost (every pool takes one drained rebuild, since old workers carry revision `""`) is not stated in the ADR's Consequences | **Open, adr** | Not a model defect, and an Accepted ADR is not edited. A later ADR can state it |

## Verification run (all through `scripts/agent/d`, in the slice worktree)

| Check | Result |
|---|---|
| `go build ./...` (darwin) and `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet ./internal/function/ ./internal/activator/ ./pkg/funcd/` (darwin and `GOOS=linux`), and `go vet -tags e2e ./pkg/funcd/` | exit 0 on all three runs |
| `golangci-lint run` on the three packages (darwin; `GOOS=linux` with the host-built binary from `go tool -n`), and `--build-tags e2e ./pkg/funcd/` (darwin and linux) | `0 issues.`, exit 0 on all four runs |
| `go test -race -count=1 ./internal/function/ ./internal/activator/` | `ok` (12.4 s) / `ok` (2.3 s), exit 0 |
| `go test -race -count=1 -tags e2e -run 'TestScenarioPooledEditIsolated$' ./pkg/funcd/` | `--- PASS (1.02s)`, exit 0 |
| **Revert check, scenario**: the e2e run with the four production files (`activator.go`, `function.go`, `pool.go`, `poolaccess.go`) overlaid from `HEAD~1` | `--- FAIL (60.60s)`: r1's step `a` failed with `invoke default/flow-a: … EOF`. The restart in place killed the in-flight call, which is the defect the scenario targets |
| **Revert check, unit**: the slice's pool tests with the same overlay | FAIL: `TestPoolPinnedCurrentWaitsForCurrentManifest`, `TestPoolRebuildSocketKeepsDepartingMember`, `TestPoolResolverAfterRestart`, `TestPoolRebuildKeepsOldUntilNewListens`, `TestPoolRebuildServesInFlightCall`. `TestPoolCrashedWorkerRestartsInPlace` and `TestIssue422_NeverReadyPoolWorkerIsReplaced` pass on both sides |
| **Revert check, M1 fix**: `function.go` and `pool.go` overlaid from `2ba44e0b` | `TestPoolPinnedCurrentWaitsForCurrentManifest` FAILS (the old worker's URL is handed out) |
| Mutant A: `pinnedPoolUpstream` drops the member-code comparison (`if !held {`) | **survives**: `internal/function` `ok` and the e2e scenario `ok`. See n1 |
| Mutant B: `pinnedPoolUpstream` uses every listed worker (`cur := insts`) instead of `splitPool(insts, hold.sig)` | killed: `TestPoolPinnedCurrentWaitsForCurrentManifest` fails |
| ADR substance unchanged by the slice | `git diff HEAD~1 HEAD -- docs/` is empty |

## Minor

- **n1 — The member-code guard in `pinnedPoolUpstream` is not pinned by a test (model, test coverage).** The check
  `hold.codes[fn.Name] != memberCode{rev.Spec.Image, rev.Spec.ImageDigest, rev.Spec.Handler}` is load-bearing in
  reachable paths. One example: the member's new Revision is stamped, but its artifact cannot be materialized, so the
  manifest keeps the serving revision's code (`servingMember`) under the old signature. Another example: a sibling's
  pass builds the manifest from a stale member view and overwrites the hold. In both cases, the old listening worker
  holds the hold's signature, and only the code comparison stops it from running the pinned revision with the
  previous code (Decision 4). Mutant A removes the comparison, and both the `internal/function` suite and the e2e
  scenario still pass. A unit test should hold a manifest whose row for the member is at the serving revision's code
  while the pin names the current revision, and assert `ready == false`.
- **m3 — carried over (adr)**: see the table above.

Observation (not scored): during a rebuild window, a call pinned to an *unedited* sibling's current revision also
waits for the new worker. The old worker is not handed out for it, even though it holds the sibling at the same code.
This is the safe reading of Decision 4. It costs one pool-host boot of latency, and the activator's wait is bounded by
`ActivationTimeout`. If the new worker never listens, pinned calls of every member of the key time out, while
unpinned calls stay on the old worker. Before this slice, a rebuild that never listened broke every call, so this is
not a regression.

Tracking note (not scored, workflow): `docs/adr/0190-*.md` still reads `Accepted (2026-10-05)`. The `adr-impl` exit
duties (ADR `Accepted → Reviewing`, feat row → `reviewing`) are owed before the gate can stamp `Implemented`. This
review therefore advanced no status.

## ✅ Verified correct — keep

- **The M1 fix uses the right truth.** The hold records the manifest that the key's workers were last built to hold:
  the signature plus each row's code, as a Revision snapshots it. The code is an unexported field, so it does not
  change the manifest JSON or its signature. The pinned path filters workers by that signature and requires the
  member's row to equal the pinned Revision. An unpinned call keeps `servingPool` (the newest listening worker), so
  siblings keep their old-worker service during the window, which the new test asserts.
- **Fail-closed after a restart or reclaim.** An empty hold (after a funcd restart, or after `forgetPool` drops it at
  reclaim) hands out nothing for a pinned-current call. The activator's wake then runs `ensurePool`, which records the
  hold again. Nothing falls back to an arbitrary worker.
- **The `poolHolds` lifecycle mirrors the other per-key maps.** It is guarded by `poolMu` and dropped by `forgetPool`
  alongside drains, liveness and member sets.
- **Everything verified in loop 1 still holds and still fails when reverted.** The drain is driven by the runtime's
  signature, Idle or `DrainGrace` retirement with the clock at listen, the socket and member union, the stop and
  create path for crashed and silent workers, per-worker liveness, and `HeldRevision` for non-current pooled pins.
- **The tests were strengthened, not weakened.** The e2e assertions became stricter (`solo`/`pool`), and
  `TestIssue422` gained the in-place assertion. No test was removed or relaxed.
- **Mutation testing:** 1 of 2 new mutants killed (B). Mutant A survives (n1).

## Recommendation

Pass for slice 3. Optionally add the unit test from n1 in a follow-up, so the member-code guard cannot regress
silently. Status advance is left to the workflow once the `adr-impl` exit duties (`Accepted → Reviewing`) are done.

```json
{
  "date": "2026-10-05",
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 10,
  "dod_total": 10,
  "report": "docs/reviews/adr-0190-slice3-implementation-claude-opus-5-5-2.md",
  "notes": "slice 3/3 loop 2 (b0c34bdb, pools Decision 8): build/vet/lint clean also Linux and e2e tags; internal/function+activator -race ok; e2e PooledEditIsolated passes, fails with the slice reverted (in-flight EOF); 5 pool unit tests fail reverted. Loop-1 M1 resolved (pinnedPoolUpstream: newest listening worker of the last-built manifest whose row holds the pinned code; new test fails on 2ba44e0b), m1 resolved (silent restart in-place assertion), m2 resolved (e2e logs solo/pool). n1 [model] member-code guard untested: mutant dropping it survives unit+e2e; m3 [adr] upgrade rebuild cost unstated (carried). Mutants: 1/2 killed."
}
```
