# Fix review: issue #1535 (model: claude-opus-5-5)

This report reviews branch `fix/1535-config-invalid-waits-for-the-stop`, commit 8ab82a02 `test(funcd): the ConfigInvalid
add-on test waits for the hold to stop the engine`, one commit on `origin/main` f352341c (`git diff origin/main...HEAD`:
`pkg/funcd/addon_internal_test.go`, 55 lines added, 11 removed). Issue: "TestScenarioAddonConfigInvalid checks the engine
before the hold stops it" (kind/flake, under #1142). Governing: ADR-0332 Decision 3 (an object waits `Pending` with no
engine, as a Lake waits, ADR-0121 Decision 2) and the Lake's hold it mirrors, which ADR-0321 Decision 5 cites. The review
ran in its own worktree, outside the fix's session; every claim below comes from a command that ran.

## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (issue #1535 fix, model: claude-opus-5-5)

### 🟡 Major 1 — The Lake's hold test has the same race, and the fix leaves it  ·  attribution: model

`TestScenarioLakeSecretForbidWhileRunning` (`pkg/funcd/secret_policy_e2e_internal_test.go:326`, build tag `e2e`, the
scenario lake-secret-forbid-while-running of ADR-0321) waits until the Lake's Ready reason is `BindingResolveFailed`
(line 334), then checks at once that the engine was stopped: `require.Greater(t, engine.teardowns.Load(), stopsBefore,
"the hold stops the running engine")` (line 335). The Lake's `holdNotReady` writes `Pending`, `BindingResolveFailed`
(`internal/services/lake/reconcile.go:342`) and only then calls `Teardown` (line 345). The add-on hold copies that order
(`internal/services/addon/reconcile.go:388` then `:391`), and the new `stopped` helper's own comment names it ("as a
Lake's does"). A check between the status write and the stop fails, exactly as in #1535.

What ran, on 8ab82a02 (its one change from `origin/main` is the add-on test file, so the Lake test and the Lake
reconciler are main's):

- `go test -tags e2e -count=1 -run '^TestScenarioLakeSecretForbidWhileRunning$' ./pkg/funcd/`: PASS.
- The same, `-count=3`, with an overlay of the test file whose `countingEngine.Teardown` waits 200 ms before it counts
  (the issue's own technique of holding the stop): 3 of 3 FAIL, `"0" is not greater than "0"`, `the hold stops the
  running engine`.

Nothing decides the stricter order: the scenario says "within one `runtime.supervisionPeriod` the Lake is held and its
engine stopped" (`docs/adr/0321-cedar-decides-each-secret-a-workload-reads.md:73`), its design property `AForbidReachesARunningEngine` is `always eventually`
(`docs/design/lake/secret-forbid.fizz:69`, which leaves "the pass's order inside funcd" to tests), Decision 5 cites the
hold's code as it is, and the Services row (`docs/behavior/services.md:34`) states the held state. No issue names the
test (searched open and closed), and no board card holds it.

The fix skill (Step 4) puts a sibling with the same cause in this issue's scope. The commit neither fixes nor names it.
Fix (builder): make that check wait for the stop (an `Eventually` on the counter, as `stopped` does here), with a
deterministic case, for example a Teardown the rig can hold, as `holdTeardowns` does here; run it alone with
`-tags e2e`.

### Minor

- **Minor 1 — The regression test does not pin the scenario's own check** · model · Mutant M3 puts
  `TestScenarioAddonConfigInvalid`'s `e.stopped` call back to the pre-fix `require.False(t, running, "no engine")`: the
  scenario and `TestIssue1535_ConfigInvalidWaitsForTheHoldsStop` both pass 30 of 30 runs under `-race`. The regression
  test repeats the scenario's six steps instead of running them, so a revert of the line the issue is about shows only as
  the rare flake it was. Fix: one body for both tests, with the hold as a parameter, as #1259's fix shares the scenario
  body between the plain and the pinned test; that also drops the copied steps.
- **Minor 2 — The regression test does not check that its hold held** · model · Mutant M2 makes the fake's `Teardown`
  ignore `held`: the regression test passes 10 of 10, because the engine is stopped before the check and nothing asks
  whether the stop was held. A check that the engine still runs right after the `ConfigInvalid` wait, before the
  release, pins the precondition the issue names (the status is written while the stop is pending) and kills M2.

### ✅ Verified correct (keep it)

- **The regression test reproduces the issue, for its reason (revert check).** The change has no non-test file to
  overlay, so the revert replaces the regression test's `e.stopped` call with the pre-fix immediate check (an overlay of
  the test file): 3 of 3 runs fail at that check with `Should be false`, `no engine`, the issue's CI failure text.
- **It passes with the fix**, un-skipped, under `-race`: the regression test, `TestScenarioAddonConfigInvalid` and
  `TestScenarioAddonRemoved` pass, 20 of 20 each with `GOMAXPROCS=2 -parallel 8 -race`; every test that uses the changed
  fake runtime (`-run 'Addon|Settings|Issue1535'`) passes under `-race`.
- **Mutant M1** (the `stopped` helper checks once instead of waiting): the regression test fails 3 of 3, `the engine of
  default/login stops`.
- **Cause, not symptom.** The hold's order (edge entry, status, then the stop) is decided: ADR-0332 Decision 3 holds an
  object "as a Lake waits for its Bucket", and the Lake's hold does the same. The scenario's "waits `Pending`,
  `ConfigInvalid`, with no engine" and the Add-ons row "Pending, with no engine or edge entry"
  (`docs/behavior/addons.md:45`) describe a held state, and `TestScenarioAddonRemoved` already waited for the stop for
  this reason. The old check asked for an atomicity nothing promised; the new wait asserts what the ADR says, polling the
  observable condition with the file's own bounds (5 s, 10 ms). No timeout raised, no retry, nothing skipped, no
  production code touched.
- **Scope.** One test file, every hunk serving the issue: the fake's hold serves the regression test; `stopped` replaces
  `TestScenarioAddonRemoved`'s inline wait with the same condition and bounds, so nothing is weakened;
  `rauthyWithRealm` is shared by the scenario and the regression test.
- **The hold is safe.** `held` is read and written under the runtime's mutex (the race detector is clean); `release` is a
  `sync.OnceFunc` registered with `t.Cleanup` after the platform's own cleanup, so it runs first and a failing test leaves
  no Teardown blocked under the platform's stop; a late timer call is harmless.
- **Reuse.** testify's `require.Eventually`, as the file's `waitReason` uses it, and the standard library's
  `sync.OnceFunc`. `internal/testkit` holds no call-holding helper (`fakeaddon` has no hold, `poll` has only `Never`),
  and the Lake package's `slowStop` is that package's own test type.
- **The other add-on and engine tests.** The add-on holds' other tests (`RuntimeUnavailable`, `SecretNotFound`,
  `BindingResolveFailed`) check `starts == 0`, which the order does not touch; `internal/services/addon`'s unit tests
  call `Reconcile` synchronously; the Function gate stops its workers before it writes its status
  (`internal/function/function.go`, `gateFailed`); a Restore's `StopEngine` returns only once the engine is gone, and
  `TestIssue1384…` reads after it.
- **Conventions.** The named-number rule skips test files (`scripts/behavior/main.go`); why-comments only; imports
  unchanged; `t.Parallel()`; the regression test runs in about 0.4 s.
- **Shape.** `test(funcd):`, the subject the earlier test-only flake fixes took (PRs #1242, #1250 and #1260, for #1241,
  #1246 and #1259), `Fixes #1535`, the attribution trailer, one commit; `scripts/commit-msg-lint.sh` passes.
- **Checks.** `go vet ./pkg/funcd/` and `golangci-lint run ./pkg/funcd/...` on darwin and linux (0 issues),
  `GOOS=linux go build ./pkg/funcd/` and `just check-behavior` pass. No ADR and no behavior row is edited, and none needs
  a change. `scripts/agent/gate.sh`, with the bloat audit, has not run on the branch: the author runs it once for the PR.

### Definition of Done

10 / 12 items hold; item 13 does not apply (no design spec models the add-on hold, and only tests changed). Misses: item 4
(two mutants on the fix's lines survive: Minors 1 and 2, model) and item 12 (a sibling with the same cause is left:
Major 1, model).

### Model scorecard

Recorded: claude-opus-5-5 on #1535 (fix) → changes-requested, 0/1/2, 3 model-attributed, DoD 10/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Back to `/fix`: make the Lake scenario wait for its hold's stop, with a deterministic case, on this branch; then the two
Minors on the regression test. Everything listed as verified stays.
