# ADR-0193 implementation review: claude-opus-5-5 (loop 1)

## Verdict: pass, 0 blockers, 0 majors, 1 minor (ADR-0193 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0193-asleep-gate-every-placement`, one commit `738a6848` on origin/main `2a327dd2` (the
merge base equals origin/main). Five files change: `internal/function/function.go`, `internal/function/pool.go`,
`internal/function/asleep_internal_test.go`, `pkg/funcd/issue769_internal_test.go` (the rig gains `opts` and an
`asleepPacing` helper, with no change to any assertion) and the new `pkg/funcd/issue796_internal_test.go`. No doc,
ADR, blueprint or config file changes.

### Verification run (all through `scripts/agent/d`, in the worktree)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go test -race -count=1 ./internal/function/` | `ok` (12.1 s), exit 0; includes `TestAsleepDesiredReplicas` with the pooled rows flipped |
| `go test -race -count=1 -v -run 'TestIssue796\|TestScenarioPooled…\|TestScenarioPoolFull\|TestIssue769\|TestAsleep' ./pkg/funcd/` | exit 0; every leaf passes: `TestIssue796/alone/{catalog-deleted,engine-down,secret-deleted}`, `TestIssue796/sibling/{catalog-deleted,secret-deleted}`, `TestScenarioPooledCallWhileGatedGoesPending`, `TestScenarioPooledDependencyReturnsStaysAsleep`, `TestScenarioPooledDependencyReturnsBesideSibling`, `TestScenarioPoolFull/{newcomer-not-asleep,asleep-member-displaced}`, and the ADR-0192 `TestIssue769_*` regressions |
| Same new tests, `-race -count=3` | `ok` (20.9 s), exit 0, no flake |
| `go vet ./internal/function/ ./pkg/funcd/` (darwin and `GOOS=linux`) | exit 0, exit 0 |
| `golangci-lint run ./internal/function/ ./pkg/funcd/` (darwin, and `GOOS=linux` with a host-built linter binary as `gate.sh` does) | `0 issues.`, exit 0 both |

No e2e, no `go test ./...` and no Lima lane were run, per the brief. The per-PR gate runs them.

### Revert check and overlay mutants (`git show origin/main:<file>` + `go test -overlay`)

| Mutant | Tests run | Result |
|---|---|---|
| m0: `function.go` and `pool.go` reverted to origin/main | `TestIssue796` | **FAIL**, all 5 subtests: `reader goes Pending/CatalogNotReady with Asleep=True … writes [Ready Ready Idle Degraded Idle Degraded …]` (the #796 Idle/Degraded alternation, about 100 writes in 3 s, as the ADR measured) |
| m1: `releasePool` call dropped from `gateFailed` | `TestIssue796` | **FAIL**, `alone/*` ×3: `the pool worker stops: Should be zero, but was 1` |
| m2: `convergePooled`'s asleep return dropped | `TestScenarioPooledDependencyReturns{StaysAsleep,BesideSibling}` | **FAIL**, `BesideSibling`: `reader goes Idle/NoReplicas with Asleep=False` is never satisfied (it turns `Ready` without a call). `StaysAsleep` passes, as expected: with no pool worker there is nothing to count. |
| m3: `releasePool` ignores the members' `desiredReplicas` (always reclaims) | `TestIssue796` | **FAIL**, `sibling/*` ×2: `writer stays Ready: Not equal` |

The ADR's prove-first rule holds: `TestIssue796` fails on main for the issue's reason and passes with the change. All
three key lines (release, the stays-asleep return, the max-over-members check) are guarded by a test.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **m1: two scenario clauses are asserted only indirectly.** Attribution: `model`. In
  `TestScenarioPoolFull/newcomer-not-asleep` (`pkg/funcd/issue796_internal_test.go:345-353`), the scenario says the
  newcomer "writes `Pending/PoolFull` once". The test waits for `Pending/PoolFull` and then requires the
  resourceVersion to hold for 1 s, but it does not record zulu's writes. More than one write before it settles would
  pass, whereas the displaced-member subtest does assert exactly one write (`:374-377`). Also,
  `requireIdleAsleepCleared` (`:289-297`) checks the `Ready` reason `NoReplicas` but not `Ready=False`, which the
  scenario states. Neither gap hides a defect in the current code. The fix is to add a `phases` recorder to the
  newcomer subtest, assert one `Pending` write, and assert `ready.Status == False`.

Handoff note for the integrator (not a finding): the commit body names neither #796 nor ADR-0193. ADR step 6 requires
the PR description to say `Fixes #796`.

### ✅ Verified correct (keep it)

- **Decision 1 / checklist 1.** `asleep` (`function.go:843-856`) no longer has `|| r.pooled(fn)`. It is false only
  for `MinReplicas != 0` and for a non-asleep phase, and the switch is unchanged. The doc comment matches the ADR's
  Contracts text.
- **Decision 2, release row / checklist 2.** `gateFailed`'s asleep branch (`function.go:796-803`) calls `stopAsleep`
  for every placement, then `releasePool`, then sets `Asleep=True/ScaledToZero`. Since `running` stays 0, the
  `Degraded` row cannot be reached on that branch. This matches the Contracts bullet exactly.
- **Checklist 3.** `releasePool` (`pool.go:383-402`) matches the Contracts step for step: `poolKeyFor` (returns nil
  for solo), `admittedMembers` (stored records), early return when any `desiredReplicas(m) > 0`, then
  `namedInstances(…, poolInstanceName(key))` and `reclaimPool`. Mutant m3 proves the max-over-members check.
- **Checklist 4.** `reclaimPool` (`pool.go:370-379`) is now the only stop-and-forget path. `ensurePool`'s
  `desired == 0` case calls it (`pool.go:318-320`), and its semantics equal main's inline block
  (`runningCount(insts) > 0` was main's `running`). No stop logic is duplicated.
- **Decision 3 / checklist 5.** All 12 `gateFailed` call sites in `reconcileFunction` pass `idx`. A grep for sites
  that do not end in `idx, drainAfter)` returns 0. `idx` is the index that `reconcileFunction` builds at
  `function.go:497-501`. For a solo Function it is zero and is never consulted, because `poolKeyFor` returns
  `!ok` first.
- **Decision 2, stays-asleep row / checklist 6.** `convergePooled` (`pool.go:190-192`) returns `verdict{pooled: true}`
  right after `ensurePool` for an asleep member, as the Contracts state. The pool still serves the siblings, and the
  member writes `Idle/NoReplicas` until a call. Mutant m2 proves this line.
- **Decision 4 / checklist 9.** The diff adds no config key, phase, gate reason or condition. `PoolFull` for a
  non-asleep newcomer carries no `Asleep` condition and stays quiet. A displaced asleep member writes one
  `Pending/PoolFull` with `Asleep=True` (`TestScenarioPoolFull`, both subtests).
- **Scenarios / checklist 7-8.** Every scenario has the named test from the Implementation plan, in plan order, not
  skipped and passing. They run on a faithful pooled rig: a real assembled platform, the real admission and
  activator, and the fake pool host the ADR specifies (`/health/members` lists the last `FUNCD_POOL_MANIFEST`, and
  running pool workers are counted). The `TestAsleepDesiredReplicas` rows `pooled-failed-asleep`,
  `pooled-pending-asleep` and `pooled-idle` now expect asleep and 0. The doc comment drops "and a pooled member". No
  existing assertion is weakened: the `issue769` change only threads options through the rig.
- **Scope.** The change stays inside the files the ADR names. `Reclaimable` and the activator are untouched. The
  pooled e2e `TestScenarioPoolWakeKeepsSiblings` (`pkg/funcd/pooling_e2e_test.go:202-215`) was read but not run: its
  `sleepy` member is never reclaimed (idle timeout 1 h, phase never `Idle`), so `asleep` stays false and it still
  turns `Ready` beside `warm`. The per-PR gate confirms this by running the e2e suite.
- **Conventions.** The comments state the reason, not the action (one line on the `convergePooled` return, with its
  ADR). Errors are returned and never swallowed, nothing panics, and every new function takes `ctx` first.

### Definition of Done

12 / 12 items hold: the ADR's Review checklist has 9 items, and its Definition of done has 3 clauses (`TestIssue796`
failed on main and passes with steps 2-3, every scenario and contract test passes under `-race`, and nothing new is
added). The generic DoD also holds: build, vet and lint are green on darwin and Linux, and there are no stubs. ADR and
feat-row status are not checked here: the ADR is not in the repo yet, and the wave's docs PR stamps it.

### Model scorecard

Not recorded in this run. The ledger row is below, for the wave's ledger PR. Verdict for claude-opus-5-5 on ADR-0193
(implementation): pass, 0/0/1, 1 model-attributed, DoD 12/12.

### Recommendation

Pass. Folding m1 into a later touch of the test file is optional. The integrator carries `Fixes #796` into the PR
body.

```json
{
  "date": "2026-10-05",
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
  "report": "docs/reviews/adr-0193-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (738a6848 on 2a327dd2); Decisions 1-4 + Contracts hold (asleep drops pooled clause, gateFailed takes idx at all 12 sites and calls stopAsleep then releasePool, reclaimPool shared with ensurePool desired-0, convergePooled asleep return); build/vet/lint clean also Linux; internal/function -race ok; 5 TestIssue796 subtests + 4 TestScenario* pass, -count=3 clean; revert check fails all 5 TestIssue796 subtests with the Idle/Degraded alternation; mutants 3/3 killed (releasePool call, convergePooled asleep return, max-over-members check). m1 [model] PoolFull newcomer 'writes once' and Ready=False on Idle/NoReplicas asserted only indirectly."
}
```
