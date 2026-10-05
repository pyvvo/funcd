# ADR-0193 implementation review: claude-opus-5-5 (loop 3, rebase onto ADR-0190)

## Verdict: pass, 0 blockers, 0 majors, 1 minor (ADR-0193 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0193-asleep-gate-every-placement`, one commit `281b0fb5` on origin/main `a4f9ef09` (merge base
equals origin/main; the tree is clean). It replaces the loop-2 commit `5095d37f`, which was on `2a327dd2`. Since then
main has merged ADR-0190 (a run bound to its revision). ADR-0190 restructured `reconcileFunction` (`heldThenGate`,
`boundEnv`) and `ensurePool` (a pool worker per manifest signature, `splitPool`, `drainPool`, `poolHolds`; no
`forgetPoolSig`).

`git range-diff` shows that the patch content of `asleep_internal_test.go`, `poolaccess.go`, `pkg/funcd/funcd.go`,
`issue769_internal_test.go` and `issue796_internal_test.go` is identical to the loop-2 commit. The only differences
are the two files that had to be re-threaded, `function.go` and `pool.go`, and the new
`TestMapPoolDisplaced` (+50 lines in `internal/function/pool_access_test.go`, which closes loop 2's m2). The commit
message gains one line in its Tests list. Everything else, including the `Co-Authored-By` trailer, is unchanged.

### Check 1: does every gate still release the pool with the pass's access index?

Yes.

- `heldThenGate` now takes `idx` (`internal/function/function.go:718`) and passes it to `gateFailed` (`:720`). All 9
  of its call sites pass the pass's `idx`: ArtifactUnresolved, RevisionMissing, RevisionStampFailed, NoMatchingPlatform,
  ShapeInvalid, RuntimeUnavailable, PoolFull and `env.gate` (`:555-623`). `env.gate` covers the secret, ref and catalog
  gates that ADR-0190 moved into `bindings`. The converge step's `ErrImageUnavailable` path calls `gateFailed` directly
  with `idx` (`:645`). No other caller of `gateFailed` exists.
- `idx` is the access index that `reconcileFunction` builds before any gate (`:518-523`, only for a pooled Function).
  This is the index Decision 3 names, so `releasePool` and `ensurePool` count the same set.
- `gateFailed`'s asleep branch calls `stopAsleep` and then `releasePool(ctx, fn, idx)` (`:866-872`), as at loop 2.
- `asleep` no longer excludes a pooled member (`:913-914`). Main has the same three `r.asleep` callers as the loop-2
  base: `gateFailed`, `finish` and `desiredReplicas`. ADR-0190 added no caller that could now behave differently for
  a pooled member.
- Mutants run through `-overlay`: a `releasePool` that returns at once fails `TestIssue796/alone/*` and
  `TestScenarioPooledDependencyReturnsStaysAsleep`. A `releasePool` that skips the "an admitted member wants a worker"
  check fails `TestIssue796/sibling`, `TestScenarioPooledCallWhileGatedGoesPending` and
  `TestScenarioPooledDependencyReturnsBesideSibling`. So the rebased release path is still guarded in both directions.

### Check 2: `reclaimPool` and `releasePool` against ADR-0190

- **`reclaimPool` (`internal/function/pool.go:481-494`)** is main's `desired == 0` branch (main `pool.go:340-352`)
  moved verbatim: it stops `cur` if one runs, retires every `old` worker and calls `endPoolDrain(key)`. `ensurePool`
  now calls `return poolPass{}, r.reclaimPool(ctx, key, cur, old)` (`:346`). The order, the conditions and the error
  returns are unchanged. Dropping `forgetPoolSig` is correct: main removed it, because ADR-0190 Decision 8 reads the
  current signature from the runtime.
- **`releasePool` (`:500-526`)** keeps loop 2's membership rule: it returns at once unless the max of
  `desiredReplicas` over `admittedMembers` is 0. It then splits the key's workers by `poolHolds[key].sig`, read under
  `poolMu` as `pinnedPoolUpstream` reads it (`function.go:2443-2451`), and calls `reclaimPool`.
- **The design choice the rebaser flagged (no hold: every worker counts as current, so all are stopped and none is
  removed).** It is sound. The split decides only whether a worker is stopped or removed, and every worker of the key
  stops in both cases. A worker that is stopped but still listed is harmless. `drainingMembers` and
  `pinnedPoolUpstream` ignore it, and `servingPool` hands out only a listening worker. The next `ensurePool` puts it
  in `cur` if its signature is current, where main's restart path handles it as after its own `desired == 0` branch.
  Otherwise it puts it in `old`, and `drainPool` retires it at once because it does not run. Building the manifest
  instead is not an option here: `poolManifest` needs `self`'s resolved secret and catalog env, which a gated member
  may not have. A stale hold is equally harmless. `holdPool` runs before every `createPool`, so the newest worker
  always carries the hold's signature, and a stale hold changes only whether a worker is stopped or removed.
- **ADR-0190 Decisions 7 and 8 are not weakened.** `releasePool` acts only when no admitted member wants a worker.
  That is exactly when main's own `desired == 0` branch would retire the old workers without waiting for their drain.
  It skips the socket narrowing, as main's branch does. Decision 7's holds apply to solo revisions: a pinned member
  whose revision is not current runs solo. `stopAsleep` stops only the serving and current revisions, so a held
  revision's solo worker is spared, as for a solo Function. The early return of an asleep member in `convergePooled`
  (`pool.go:205-208`, the check at `:206`) now returns `pass.drainAfter`, so a pass of an asleep member that drives a rebuild still comes
  back to finish the old worker's drain. Loop 2's `return verdict{pooled: true}, nil` predates the drain and had no
  such value.

### Check 3: tests, with `-race`

All runs used `scripts/agent/d` in the worktree.

| Check | Result |
|---|---|
| `go build ./...` (darwin and `GOOS=linux`) | exit 0 |
| `go vet` on `internal/function`, `pkg/funcd`, `internal/controller`, `internal/pooling` (darwin); `internal/function`, `pkg/funcd` (`GOOS=linux`) | exit 0 |
| `gofmt -l internal pkg` | no output |
| `go tool golangci-lint run ./internal/function/ ./pkg/funcd/` | `0 issues.` |
| `go test -race -count=1 ./internal/function/ ./internal/controller/ ./internal/pooling/` | `ok` (12.7 s, 2.8 s, 1.6 s). This includes ADR-0190's in-package tests (`pool_drain_test.go`, `held_test.go`, `revhold_test.go`, `shim_test.go`) |
| `go test -race -count=2 ./pkg/funcd -run 'TestIssue796\|TestScenarioPool\|TestScenarioPooled\|TestIssue769\|TestScenarioGateHeld\|TestScenarioDependency\|TestScenarioHeld\|Drain'` | `ok` (15.9 s): `TestIssue796`, `TestScenarioPoolFull`, the three `TestScenarioPooled*` of ADR-0193, `TestIssue769_*` and `TestScenarioDependencyReturnsStaysAsleep` pass in both iterations |
| `go test -race -count=1 ./pkg/funcd/` (untagged, the whole package) | `ok` (16.5 s) |
| ADR-0190's e2e-tagged scenarios, run on their own: `go test -race -tags e2e ./pkg/funcd -run 'TestScenarioPooledEditIsolated\|TestScenarioHeldRevisionWakesSolo\|TestIssue776_EditMidRunKeepsOldCode\|TestScenarioBrokenEditSparesOldRun\|TestScenarioTwoRevisionsSideBySide'` | all 5 `PASS`, `ok` (7.1 s). These are targeted runs, not the e2e suite |
| `TestMapPoolDisplaced`, `-count=3` | 6 rows pass in all 3 iterations |
| Identity and absolute-path grep over `git show HEAD` (local username, home prefixes) | no hit |
| Commit trailer | one `Co-Authored-By` line |

**`TestMapPoolDisplaced` kills a mutant of each filter** (`go test -overlay` on `poolaccess.go`):

| Mutant | Row that fails |
|---|---|
| `ObservedGeneration >= Generation` filter removed | `newcomer-already-observed` |
| `r.asleep(m)` filter removed | `member-awake` (also `asleep-member-past-the-limit` and `member-already-pool-full`, which then map the awake newcomer) |
| "not yet `PoolFull=True`" filter made always true | `member-already-pool-full` |
| the past-the-limit slice replaced by every member | `member-admitted` |

The `newcomer-not-pooled` row does not kill the removal of the early `Spec.Pooling.Worker == ""` return. That is
expected and is not a gap: `poolKeyFor` returns false for such a Function, so the early return only skips
`accessIn`, as loop 2 noted. The behaviour is the same with or without it. The table follows the shape of the
`MapAccess` test next to it.

### Check 4: did any ADR-0190 behaviour regress?

No regression found. I read ADR-0190 Decisions 1-11. The rebase touches only Decisions 6 and 8.

- Decision 6 (a held revision wakes): `heldThenGate` still runs `convergeHeld` before the gate's write, and threading
  `idx` changes neither its order nor its error handling. `TestHeldWakeUnderCurrentRevisionGate` and
  `TestScenarioHeldRevisionWakesSolo` pass.
- Decision 8 (pools): `ensurePool`'s rebuild, restart and drain branches are untouched. Its `desired == 0` branch
  behaves exactly as before (above). `TestPoolRebuild*`, `TestPoolResolverAfterRestart`,
  `TestPoolPinned*` and `TestScenarioPooledEditIsolated` pass.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **m1 (carried from loops 1 and 2, still open): two scenario clauses are asserted only indirectly.** Attribution:
  `model`. `TestScenarioPoolFull/newcomer-not-asleep` does not record zulu's writes, and
  `requireIdleAsleepCleared` does not check `Ready=False`. Neither hides a defect.

Loop 2's m2 is closed: `TestMapPoolDisplaced` (`internal/function/pool_access_test.go:180`) has one row per filter,
and each filter's mutant fails it (table above).

Observation (not a finding): `releasePool`'s split by `poolHolds[key].sig` (`pool.go:521-524`) has no test. A mutant
that never splits (every worker counts as current) passes every `internal/function` test and the pooled scenarios of
`pkg/funcd`. The only effect of the mutant is that an old worker of a fully idle key is stopped instead of
removed, and the next `ensurePool` retires it. No ADR promise depends on that, so a test is optional.

### ✅ Verified correct (keep it)

- The re-threading is minimal. `idx` is added to `heldThenGate` and passed through, and every gate path still reaches
  `gateFailed` with it. Nothing else in `function.go` differs from main except the loop-2 changes (`gateFailed`'s
  release and `asleep`).
- `reclaimPool` is main's ADR-0190 reclaim, moved verbatim, so `ensurePool` and `releasePool` share one reclaim and
  do not each carry a copy.
- The no-hold fallback fails safe: it never removes a worker that might be current.
- The rebaser also kept `pass.drainAfter` on the asleep early return. A literal port of loop 2's return would have
  dropped it.
- `TestMapPoolDisplaced` is tight: each of its four real filters is proven by a mutant.

### Does the loop-2 verdict still hold?

Yes. Loop 2's analysis of the `MapPoolDisplaced` cause and fix carries over unchanged (identical patch). The two
re-threaded files keep ADR-0193 Decisions 1-4 on top of ADR-0190, and the guards that loop 1 and loop 2 proved
(the release mutants and the displaced-member scenario) still fail their mutants and pass on the work.

### Definition of Done

12 / 12. The 9 Review-checklist items and the 3 DoD clauses hold as in loop 2. The release mutants and the
scenario run above re-prove them on the rebased code. The full e2e suite was not run in this loop, as instructed;
the gate runs it.

### Recommendation

Pass. The branch can go to the gate. The PR description must still say `Fixes #796`, because the commit body names
neither #796 nor ADR-0193 (handoff note from loop 2).

```json
{
  "date": "2026-10-06",
  "adr": "0193",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0193-implementation-claude-opus-5-5-3.md",
  "notes": "loop 3 (281b0fb5 on a4f9ef09), rebase onto ADR-0190. The patch is identical to 5095d37f except function.go (idx threaded through heldThenGate; all 9 call sites and the ErrImageUnavailable gateFailed pass it), pool.go (reclaimPool = main's desired==0 branch moved verbatim: stop cur, retire old, endPoolDrain; releasePool splits by poolHolds sig, and with no hold stops all and removes none, which is safe because the next ensurePool retires stale stopped workers; the asleep early return keeps pass.drainAfter) and the new TestMapPoolDisplaced. Release mutants (no-op, no member check) fail TestIssue796 alone and sibling plus the pooled scenarios; the 4 mapper filter mutants each fail a TestMapPoolDisplaced row (closes m2). -race: internal/function, controller and pooling ok; pkg/funcd untagged ok; ADR-0193 scenarios -count=2 ok; ADR-0190 e2e-tagged scenarios (PooledEditIsolated, HeldRevisionWakesSolo, Issue776, BrokenEditSparesOldRun, TwoRevisionsSideBySide) ok, run on their own. Build, vet (darwin and linux), gofmt and lint clean; no identity leak. m1 [model] carried. Observation: the hold split in releasePool is untested (only stop vs remove of an idle key's old worker)."
}
```
