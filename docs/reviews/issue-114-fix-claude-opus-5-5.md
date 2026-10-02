## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #114 fix, model: claude-opus-5-5)

Commit `cd86617` `fix(eventing): fire timer events once per interval instead of once per 250ms tick`
on the eventing group branch. It touches `internal/eventing/eventing.go` (the Run tick and `dueTimers`)
and `internal/eventing/run_test.go` (the regression test).

### Minor
- **The skip-instead-of-burst behavior has no test** · attribution: `model` · The new `dueTimers` doc
  comment says that "periods missed while a publish was in flight are skipped rather than fired in a
  burst". The regression test calls `fireDue` synchronously, so `firing` is never true when `dueTimers`
  runs, and that path is never exercised. Overlay mutant m3
  (`e.lastFire = e.lastFire.Add(e.interval)`, which catches up) passes the whole package
  (`ok internal/eventing 8.999s`). A scratch probe through the real `Run` loop, with a first publish
  that blocks for 1s on a 100ms timer, shows that the fix behaves as the comment says:
  `fires=6 gaps=[1.027s 75ms 98ms 101ms 99ms]`. Under m3 the same probe bursts:
  `fires=15 gaps=[1.025s 26ms 24ms 25ms ...]`. Fix: add a case to `TestIssue114_…` (or a sibling test)
  that holds one publish across several periods and asserts that no burst follows. This does not block
  sign-off, because the behavior is correct and the pre-fix code did not burst either.

### ✅ Verified correct (keep it)
- **It fails without the fix, for the issue's reason.** `git revert --no-commit cd86617`, with the
  HEAD `run_test.go` restored, gives `Max difference between 100 and 39`, `33 and 19`, `10 and 8`, and
  `9 and 7`. These are the investigator's numbers in the issue (39/19/8/7), which match the 250ms and
  500ms effective periods and the 1s fires lost to jitter. The revert applied cleanly, because no later
  commit on the branch touches `internal/eventing`.
- **It passes with the fix** under `-race`: `go test -race -count=1 -run TestIssue114 ./internal/eventing/`
  passes all four subtests (100ms, 300ms, 1s, 1.1s), with none skipped.
- **The user-visible behavior is fixed.** The issue's own probe shape is a real `Source.Run` for 10s
  with timers at 100ms, 300ms, 1s and 1.1s, run as a scratch overlay and not committed. It gives
  `100ms=99 300ms=33 1s=9 1.1s=9`, against roughly 100/33/10/9 expected. The pre-fix result was
  40/20/8/8. The 1s count of 9 is a window-edge effect: the 10th fire lands at 10s plus up to one tick,
  after the 10s context ends.
- **The cause is fixed, not masked.** Both causes that the issue names are removed. (1) The cap of one
  fire per tick: the tick is now 25ms, below the 100ms floor that ADR-0048 and the `TimerEvent.Interval`
  bound set. (2) Jitter that accumulated because `lastFire = now`: `lastFire` now advances to the latest
  period boundary (`now - elapsed % interval`). `time.Time.Add` keeps the monotonic reading, so
  `Sub` stays monotonic. No timeout, retry or swallowed error was added.
- **Mutants are killed.** m1 (25ms tick, `lastFire = now`) fails with `100 and 88`. m2 (250ms tick,
  boundary logic) fails with `100 and 39`. Each half of the fix is therefore needed and tested.
  m4 (a 50ms tick) survives, as expected: once `lastFire` advances by boundary, any tick at or below
  the 100ms floor keeps the period correct, and the tick size only bounds how late a fire can be,
  which the comment states ("a firing is at most one tick late").
- **Scope**: two files, and every hunk serves #114. No test was weakened or deleted, and the existing
  `TestIssue35_…` timer test still passes.
- **Reuse**: the test reuses the package's `capturePub`, `store.New(memory.New())`, `registerTimer`,
  `dueTimers` and `fireDue`. It adds no new helper, harness or dependency.
- **Conventions**: ctx-first, no `any`, no new imports, top-level imports, and a short doc comment that
  explains why. The one inline test comment explains the synthetic tick grid (offset and jitter), which
  is not obvious from the code.
- **ADRs**: ADR-0023 says "`Run(ctx)` ticks every registered timer on its interval", and the fix now
  meets that. ADR-0108 keeps the tick engine and defers catch-up, so skipping missed periods is
  consistent with it. The 100ms floor (ADR-0048) is unchanged. No ADR file was edited, and no living
  doc mentions the 250ms tick.
- **Checks**: `gofmt -l internal/eventing` is clean; `go build ./...` passes on the host and for
  `GOOS=linux`; `go vet` passes for `internal/eventing` and `pkg/funcd`, on the host and for Linux;
  `golangci-lint run ./internal/eventing/...` reports `0 issues.` on the host and for `GOOS=linux`.
  `go test -race -count=1` passes for `internal/eventing/...`, `internal/sensor`, `cmd/funcdctl` and
  `pkg/funcd`, which are all the importers of the package. `go test -tags e2e -count=1 ./pkg/funcd/...`
  passes (`ok ... 104.877s`). No Lima lane was run, by batch rule; the full group check set runs later.
- **Shape**: the subject is `fix(eventing): …`, the body names the regression test and has
  `Fixes #114` and the attribution trailer, and the commit covers one issue.

Observation, not a finding: the Run loop now wakes 40 times a second instead of 4, even when no timer
is registered. Each wake takes the lock and walks the timer map, which is negligible on the target
hardware. A deadline-driven timer (sleep until the earliest due entry) would avoid the polling, but it
is a larger change and is outside this fix.

### Definition of Done
11 / 11 items hold (the fix checklist): the regression test reproduces the issue, fails on the pre-fix
code for the reported reason, and passes under `-race`; the revert and the key mutants fail; the cause
is fixed; the scope is clean; the ADRs hold; the checks are green, including e2e; the conventions hold;
existing code is reused; and the commit shape is correct. The m3 test gap is a Minor and does not
negate item 4, because both key lines are covered.

### Model scorecard
Not recorded by this stage (batch rule): claude-opus-5-5 on issue #114 (fix) → pass, 0/0/1,
1 model-attributed, DoD 11/11. A later stage records the ledger row.

### Recommendation
Sign off. Optionally, add a held-publish case that pins the skip-not-burst claim, as follow-up polish
on the same branch.
