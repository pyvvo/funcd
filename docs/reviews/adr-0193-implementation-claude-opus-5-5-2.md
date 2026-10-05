# ADR-0193 implementation review: claude-opus-5-5 (loop 2, gate-failure rework)

## Verdict: pass, 0 blockers, 0 majors, 2 minors (ADR-0193 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0193-asleep-gate-every-placement`, one amended commit `5095d37f` on origin/main `2a327dd2`.
Compared with the loop-1 commit `738a6848`, the rework touches four files (+49/-3):
`internal/function/poolaccess.go` (new `MapPoolDisplaced`), `internal/function/pool.go` (`rankedMembers` split out
of `admittedMembers`), `pkg/funcd/funcd.go` (one `ctrl.Watches(v1.KindFunction.GVK(), fnReconciler.MapPoolDisplaced)`
line) and `pkg/funcd/issue796_internal_test.go` (a settle wait before `alpha` is applied). `function.go` and the rest
of the loop-1 work are unchanged.

### Stated cause: confirmed

The gate failed `TestScenarioPoolFull/asleep-member-displaced` with `reader is Pending/PoolFull with Asleep=True;
writes [Idle]`. The rework says nothing reaches an asleep displaced member once its own passes have settled. I
checked each link:

- `requeueFor` (`internal/function/function.go:1035-1080`) has no `PhaseIdle` case and returns 0, so an asleep
  member's pass schedules no requeue.
- The only Function watches before the rework are `kvReconciler.MapFunction` and `catalogReconciler.MapFunction`
  (`pkg/funcd/funcd.go:877`, `:925`). `MapAccess` is registered only for the access kinds (`funcd.go:810-812`).
  Nothing maps a newcomer's event to its pool siblings.
- Revert check (my run): `pkg/funcd/funcd.go` replaced by its origin/main version through `go test -overlay` (the
  only difference is the `Watches` line), with the new settle wait in the test:
  `go test -race -count=3 -run TestScenarioPoolFull ./pkg/funcd/` failed 3 of 3 with the gate's exact message
  `reader is Pending/PoolFull with Asleep=True; writes [Idle]`. With the fix, the same command passed 3 of 3
  (`asleep-member-displaced` 1.90-1.96 s).

So the loop-1 pass was a race win: one of reader's own trailing passes read `alpha`. The defect is in the code, as
the rework says. ADR-0193's pool-full scenario promises the outcome ("When an asleep admitted member is displaced by
a newcomer whose name sorts earlier, Then it writes `Pending/PoolFull` once with `Asleep=True`"), and so do
Decision 4 and the last Review-checklist item.

### Does the fix match the ADR?

- Decision 4 forbids a new config key, phase, gate reason or condition. The mapper adds none. It reuses `asleep`,
  `poolKeyFor`, `accessIn` and the `condPoolFull` condition that already exist.
- "Takes the asleep branch like any gate" (Decision 4): every other gate of an asleep member reaches it through a
  watch mapper (the catalog mapper, `MapAccess`). The pool gate's trigger is another Function's event, and
  `MapPoolDisplaced` is the matching mapper. It is event-driven and adds no requeue, so Decision 1's "then stays
  quiet" holds.
- Membership stays one rule (Decision 3): `MapPoolDisplaced` and `admittedMembers` share `rankedMembers`
  (`internal/function/pool.go:404-420`). `admittedMembers` returns `all[:min(len(all), r.poolLimit)]`, the same set
  as main's inline truncation. The sort is not duplicated.
- Filters (`internal/function/poolaccess.go:191-217`): the mapper acts only on a pooled Function whose
  `ObservedGeneration < Generation` (a create, or a spec change that can move it into a key). It returns only
  members past `PoolLimit` that are asleep and not yet `PoolFull=True`. A non-asleep member is never mapped, so the
  ADR's "`PoolFull` unchanged for a member that is not asleep" holds. Once reader shows `PoolFull=True`, later
  newcomers do not map it again. The early `Spec.Pooling.Worker == ""` return duplicates the check in `pooled`, but
  it skips `accessIn` for every solo Function event, which is a sound reason.
- The ADR's Contracts and Implementation plan name only `function.go`, `pool.go` and the tests. The mapper is a
  mechanism they do not list, in two more files. It is the smallest way to keep a promise the ADR makes and
  contradicts no Accepted text, so it is not a finding. The ADR's Negative consequences do not mention its cost: an
  `accessIn` plus a namespace Function list per event of a pooled Function whose generation is unobserved. That
  cost is bounded, because each create or spec change produces only a few such events.

### Is the test weakened?

No. The test diff since loop 1 is seven added lines (`issue796_internal_test.go:364-370`): it waits until reader's
resourceVersion holds across two polls 300 ms apart, before `alpha` is applied. Every assertion after it is
byte-identical to loop 1: `Pending`, `PoolFull=True`, `Asleep=True`, the resourceVersion holds for 1 s, and exactly
one `Pending` write. The wait removes the race that let the old code pass, so the test is now stricter. The revert
check above shows that the old behaviour fails it every time.

### Verification run (all through `scripts/agent/d`, in the worktree)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./internal/function/ ./pkg/funcd/` (darwin and `GOOS=linux`) | exit 0 |
| `go tool golangci-lint run ./internal/function/ ./pkg/funcd/` | `0 issues.` |
| `go test -count=1 -tags e2e ./pkg/funcd/...` (the gate's `test-e2e` step, no `-race`) | `ok` (355.0 s), exit 0 |
| `go test -count=2 ./pkg/funcd/` (untagged, as in `just test`) | `ok` (21.9 s), exit 0 |
| `go test -race -count=3 -v -run TestScenarioPoolFull ./pkg/funcd/` | both subtests pass in all 3 iterations |
| Revert check (`funcd.go` from origin/main through `-overlay`), same command | FAIL 3 of 3, the gate's message |
| `go test -race ./internal/controller/` | `ok` |
| `go test -race ./internal/function/` | see the observation below; `ok` (12.5 s) alone, `ok` in 2 parallel `-count=4` runs (45 s each) |
| Identity and absolute-path grep over `git show HEAD` (local username, home prefixes) | no hit |
| Commit trailer | one `Co-Authored-By` line, kept |

The first e2e run and the `-count=2` run ran at the same time as the revert-check runs. That load is comparable to
the gate's.

Observation (not attributed): one `go test -race ./internal/function/` run reported FAIL at 660.6 s. That is the
default 10-minute timeout, and it happened while the e2e suite and the revert-check runs loaded the host. The output
was cut to its last lines, so no goroutine trace was captured. The failure did not recur in 9 later iterations: one
alone, and 2 × `-count=4` run in parallel with each other. The rework changes nothing that `internal/function`'s
tests run except the `admittedMembers` split, which has identical semantics. I therefore do not tie it to this change.
If the gate shows an `internal/function` timeout, capture its trace before rerunning.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **m1 (carried from loop 1, still open): two scenario clauses are asserted only indirectly.** Attribution: `model`.
  `TestScenarioPoolFull/newcomer-not-asleep` does not record zulu's writes, so it cannot show "writes
  `Pending/PoolFull` once". `requireIdleAsleepCleared` checks the `NoReplicas` reason but not `Ready=False`. The
  rework did not touch either, and neither hides a defect in the current code.
- **m2 (new): `MapPoolDisplaced` has no unit test of its filters.** Attribution: `model`. `MapAccess` has one
  (`internal/function/pool_access_test.go:173`). The scenario test guards only the mapper's positive path: the
  revert check proves that it is needed. Dropping the `ObservedGeneration` filter, the `asleep` filter or the
  "not yet `PoolFull`" filter would pass every test. With the last two filters dropped, a non-asleep member, or an
  already displaced one, would be enqueued by every newcomer. The fix is a table test next to the `MapAccess` test:
  a newcomer with an unobserved generation maps an asleep member past the limit; an observed generation, a
  non-asleep member, and a member that already shows `PoolFull=True` map nothing.

Handoff note for the integrator (not a finding): the commit body still names neither #796 nor ADR-0193. The PR
description must say `Fixes #796` (ADR step 6).

### ✅ Verified correct (keep it)

- The diagnosis is precise: the requeue gap, the missing watch, and the race that hid it. It is backed by a
  deterministic reproduction, not by a guess.
- The fix is the smallest event-driven one. It adds no polling, and an asleep member still costs nothing until a
  call or a displacing newcomer arrives.
- One membership rule: `rankedMembers` is shared, and no sort or truncation is duplicated.
- Loop 1's verified items still hold. `function.go` is unchanged since `738a6848`, and the `pool.go` change only
  extracts `rankedMembers`, so Decisions 1-3, the Contracts, the 12 `gateFailed` call sites and the mutant results
  from loop 1 (m0-m3) are unaffected.

### Does the loop-1 verdict still hold?

Yes. Loop 1 passed the work on a test run that the race let pass. The rework closes the code gap behind it, and the
loop-1 analysis stays valid with one correction: the claim that a displaced asleep member writes `Pending/PoolFull`
with `Asleep=True` was not reliably true at `738a6848`. It is true at `5095d37f` and is now proven against a
revert.

### Definition of Done

12 / 12. The 9 Review-checklist items hold, including the last one (`PoolFull` unchanged for a non-asleep member; a
displaced asleep member writes `Pending/PoolFull` with `Asleep=True`), which is now proven against a revert. The 3
DoD clauses also hold: `TestIssue796` failed on main (loop 1), every scenario and contract test passes under `-race`,
and nothing new is added in the ADR's sense. The gate's e2e step passes.

### Recommendation

Pass. The branch can go back to the gate. m2 is a cheap follow-up that would protect the mapper's filters. m1 stays
optional.

```json
{
  "date": "2026-10-06",
  "adr": "0193",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0193-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2 (5095d37f on 2a327dd2), rework of the gate failure in TestScenarioPoolFull/asleep-member-displaced. Cause confirmed: requeueFor returns 0 for Idle and no watch maps a Function event to its pool siblings, so the displaced asleep member learned it only by a race; revert check (funcd.go from main via -overlay, settle wait in) fails 3/3 with the gate message, fix passes 3/3. Fix = MapPoolDisplaced watch + shared rankedMembers; adds no key/phase/reason/condition (Decision 4 holds); test change is a settle wait only, assertions unchanged. e2e-tagged pkg/funcd ok (355 s), untagged ok, vet/lint clean. One internal/function -race 10-min timeout under heavy load, not reproduced in 9 runs, not attributed. m1 [model] carried: PoolFull newcomer 'writes once' and Ready=False asserted indirectly. m2 [model] MapPoolDisplaced has no unit test of its generation/asleep/PoolFull filters (MapAccess has one)."
}
```
