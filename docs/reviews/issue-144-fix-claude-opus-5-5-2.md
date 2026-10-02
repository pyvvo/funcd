# Issue #144 Fix Review (round 2) — held cold requests outlive the platform's shutdown

**Verdict**: **pass**. The rework commit closes the Major and the Minor of round 1. The regression test now bounds
the wait for `Run`, so the key-line mutant (the `context.Background()` root of `drive`'s context) fails it in 2 s.
A new test holds an activation inside its wake and fails when `halt` does not wait for it. A post-stop cold
request is now checked for a 503 without a second wake. Two Minors remain: a message-only branch survives
mutation, and the rework landed as a second commit for the issue.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #144 · round-1 report `issue-144-fix-claude-opus-5-5.md` · ADR-0016 (C3, Decisions 2–4) ·
ADR-0033 (M4) · ADR-0028 crash-only lifecycle · ADR-0002 · `CLAUDE.md` style rules

The change is commits `4f2295e` (the fix) and `01bcbcf` (the rework, tests only) on `fix/202-graceful-shutdown`,
reviewed at `01bcbcf`. Together they touch `internal/activator/activator.go` and
`internal/activator/activator_test.go` only. `activator.go` is byte-identical between `4f2295e` and `01bcbcf`. The
branch's other commits belong to other issues of the graceful-shutdown group and were not reviewed here.

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #144 fix, model: claude-opus-5-5)

### Round-1 findings

| Round-1 finding | Status | Evidence |
|---|---|---|
| Major 1 — the key-line mutant passes the test after 60 s | resolved | the mutant now fails: `activator_test.go:293: Run did not return when the platform stopped: shutdown waits out the held request's activation timeout` (2.00 s) |
| Minor — deleting `a.drives.Wait()` survives | resolved | the mutant fails `TestIssue144_RunWaitsForRunningActivation`: `Error: Condition satisfied` (Run returned while the activation was still in its wake) |
| Minor — deleting the `if a.stopped` guard survives | resolved | the mutant fails `TestIssue144_RunStopReleasesHeldColdRequest` on `require.Equal(t, 1, sc.count(), …)`: `Error: Not equal` |

### Minor

- **Minor · model — the "activator stopped" message branch survives mutation.** Deleting
  `if a.life.Err() != nil { err = errStopped(fn) }` in `drive` passes the package's tests under `-race`. Without it,
  a request held during shutdown still gets the 503, but its detail reads "did not become ready within 1m0s"
  instead of "activator stopped before … became ready". The status code, which is the issue's expected behavior,
  is covered. **Fix (builder, optional)**: assert the problem detail in
  `TestIssue144_RunStopReleasesHeldColdRequest`.
- **Minor · model — the rework is a second commit for the issue.** `/fix` Step 6 and `/fix-batch` Step 1 ask for
  one commit per issue on the branch. `01bcbcf` (`fix(activator): address review of #144`, `Refs #144`) is a
  second commit, and its subject does not read as a release note. Squash merge limits the effect, but the PR's
  commit list shows the rework. **Fix (builder)**: fold `01bcbcf` into `4f2295e` before the PR opens.

### ✅ Verified correct (keep it)

- **The regression tests fail on the pre-fix code, for the reported reason.** With `01bcbcf` and `4f2295e`
  reverted (newest first, no conflicts, since no later commit touches `internal/activator`) and the `01bcbcf`
  test file restored:
  `activator_test.go:300: the held cold request was not released when Run stopped: it waits out its activation timeout`
  (`TestIssue144_RunStopReleasesHeldColdRequest`, FAIL after 2.00 s), and
  `TestIssue144_RunWaitsForRunningActivation` FAIL at `activator_test.go:331` (`Condition satisfied`: Run
  returned while the activation was still running).
- **They pass with the fix**, un-skipped, under `-race`: 10/10 runs of each (0.00 s and 0.05 s). The whole
  `internal/activator/...` tree passes under `-race -count=3`.
- **Mutants.** Five overlay mutants of `activator.go` were run. Four fail a test: the `context.Background()`
  root in `drive`, `a.drives.Wait()` deleted, the `stopped` guard deleted, and `a.cancel()` deleted from `halt`
  (the last fails both tests at their 2 s bounds). The fifth, the message branch, survives (Minor above).
- **The user-visible behavior is fixed.** The production code is byte-identical to round 1, where the issue's
  probe (an InMemory platform, the Node shim, a 20 s boot, a cancel 1 s after the cold call) gave
  `Run returned after 3.695167ms`, zero drive goroutines, and a 503 at 1.00 s. The e2e suite passes again on
  `01bcbcf` (below).
- **Cause, not symptom.** Every activation runs under a lifetime context that `Run` cancels on return, and `Run`
  waits for the activations. No timeout was shortened, no error was swallowed, and the `pkg/funcd` shutdown
  bound is unchanged.
- **Scope.** The net test-file diff against the pre-fix code removes no line. The resolve counter that `4f2295e`
  added to `fakeEndpoints` is gone again, so the existing fake is back to its original shape. No test was
  weakened or deleted.
- **Reuse.** The tests reuse `newActivator`, `serve`, `fakeScaler` (its `hook` and `count`) and
  `fakeEndpoints`. No new helper or fake.
- **Test soundness.** `TestIssue144_RunWaitsForRunningActivation` blocks the wake in the scaler hook, which
  ignores the context, so `Run` can return only through `drives.Wait()` once the hook is released. `len(ran)` is
  read without a race. In the first test, the post-stop cold request reuses the `sync.Once` hook, so a second wake
  shows up only in `sc.count()`, which is the assertion that catches it.
- **ADRs.** No ADR file is edited. ADR-0016 C3 and Decision 2 hold: a 503 with `fault.Unavailable` for an
  activation that the stopping platform cannot complete. The single-flight (C4) and clear-on-resolve
  (Decision 3) are unchanged. ADR-0033 M4 holds within the drain.
- **Conventions.** ctx-first, `api/fault` errors, no `any` in signatures, top-level imports, and short why-comments.
- **Checks** (on `01bcbcf`). `gofmt -l internal/activator` is clean. `go build ./...` ok.
  `go vet ./internal/activator/... ./pkg/funcd/...` ok. `golangci-lint` reports 0 issues on the host and with
  `GOOS=linux`. `go test -race` passes on `internal/activator/...`, `internal/dataplane/...` and `pkg/funcd/...`.
  The e2e suite `go test -tags e2e ./pkg/funcd/...` passes (105.0 s). The Lima lanes were not run, because a
  later stage owns the VM.
- **Shape.** Both commits use `fix(activator): …` and carry the attribution trailer, with one issue per commit.
  `Fixes #144` is in `4f2295e`.

### Definition of Done

11 / 11 items hold. Item 4 holds because every key-line mutant fails a test. The surviving mutant changes only a
message (Minor). The second commit is a Minor shape deviation. It does not break item 11, because each commit has
the `fix(<scope>):` subject and the trailers, and `Fixes #144` is present.

### Model scorecard

This stage does not record the scorecard. Ledger fields: claude-opus-5-5 on issue #144 (fix), round 2:
verdict pass, 0 blockers, 0 majors, 2 minors, 2 model-attributed, DoD 11/11.

### Recommendation

Pass. Before the PR, fold `01bcbcf` into `4f2295e` so the issue has one commit. Optionally, assert the
"activator stopped" detail. The production change needs no rework.
