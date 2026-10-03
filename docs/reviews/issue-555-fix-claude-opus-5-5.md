# Fix review — issue #555 (claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #555 fix, model: claude-opus-5-5)

Issue #555 is a `kind/task`: replace the bare 200 ms sleeps in `TestIssue130_InvokeOfNonJSONReplyRejectsCatchably`
and `TestIssue132_StrayNodeFaultKeepsSoloWorkerServing` (`pkg/funcd/shim_regression_e2e_test.go`) with a wait on
evidence that the slow calls are in flight. The change is commit `bcaa5be`, one file, +32/−8.

The slow handlers now append one byte to a marker file named in the event, and a new `inFlight` helper waits
(`require.Eventually`, 15 s, 20 ms tick) until the file holds exactly one byte per slow call before the trigger
call goes out. This is the deterministic signal the issue proposed ("a marker file under a temp dir passed in the
event").

### ✅ Verified correct (keep it)

- **Done when, item 1**: neither test contains a `time.Sleep`. A grep of the file finds no `time.Sleep` left.
- **Done when, item 2, current pins**: `go test -tags e2e -race ./pkg/funcd/ -run 'TestIssue13[02]_' -count=3` → `ok` (17.8 s).
- **Done when, item 2, old pins**: with a scratch modfile that downgrades `funcd-typescript` v0.4.4 → v0.4.0
  (`go get -modfile`, then `go test -modfile`), both tests fail for their issue's reason:
  #130 gets `503 … upstream call failed: EOF` on the invoke call; #132 gets `unhandled: a concurrent call is
  answered: … 503 …`. The #132 failure is on the slow concurrent call, which shows that the in-flight property
  is now exercised rather than raced.
- **Mutants** (test-file overlays, `-run 'TestIssue13[02]_'`), each fails a test:
  1. drop `appendFileSync` from the #130 handler → #130 fails, "1 slow calls are in flight";
  2. drop `appendFileSync` from the #132 handler → #132 fails, "2 slow calls are in flight";
  3. `inFlight` waits for `n+1` bytes → both tests fail.
  The wait is load-bearing: the trigger cannot go out before the handlers record that they started.
- **Scope**: every hunk serves the issue; no assertion was weakened or removed; no production code changed.
- **Reuse**: `require.Eventually` from testify (already used in the file); no new dependency. The helper is
  local to the one file that needs it; no equivalent exists in `internal/testkit` or the neighbouring tests.
- **Conventions**: top-level `import` in the embedded JS; one short why-comment on the helper; no comment
  narration. `t.TempDir()` is used only for the marker file, not for a platform data dir, so the Unix socket
  path limit does not apply (the rig still uses `shortDataDir`).
- **ADRs**: no ADR touched or contradicted.
- **Checks** (touched package, host): `go vet -tags e2e ./pkg/funcd/` clean;
  `golangci-lint run --build-tags e2e ./pkg/funcd/...` → 0 issues; `-race` tests green as above.
- **Shape**: `test(funcd): …` subject (correct type for a test-only task), `Fixes #555`, attribution trailer,
  one issue per commit. The message records the old-pin evidence.

### Definition of Done

Item 1 (a new `TestIssue<N>_…` test) does not apply: the task changes the existing regression tests and adds
no new behavior. Items 2–11 hold, read for a task (item 2 = the tests still fail on the old shim pins).
**10 / 10.**

### Model scorecard

claude-opus-5-5 · issue #555 · fix · pass · 0 B / 0 M / 0 m · model-attributed 0 · DoD 10/10.

### Recommendation

Pass. Hand back to `/fix` for integration; the group gate runs the repo-wide checks and Linux lint.
