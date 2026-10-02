# Issue #144 Fix Review — held cold requests outlive the platform's shutdown

**Verdict**: **changes requested**. The fix itself is correct and removes the cause the issue names: a real
InMemory platform with a 20 s booting Node function now returns from `Run` in about 4 ms instead of 15 s, no
activator `drive` goroutine is left, and the held caller gets a 503 at 1.0 s instead of 30 s. The regression test,
however, does not bound how long `Run` takes to return, so a mutant that restores the issue's exact cause on the
fix's key line still passes the test (it only takes 60 s). That is one Major; there is also one Minor.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #144 · ADR-0016 (C3, Decisions 2–4) · ADR-0033 (M4) · ADR-0028 crash-only lifecycle ·
ADR-0002 · `CLAUDE.md` style rules

The change is commit `4f2295e` on `fix/202-graceful-shutdown` (the group branch, reviewed at `d1614a1`). It touches
`internal/activator/activator.go` and `internal/activator/activator_test.go` only. The branch's other commits belong to
other issues of the graceful-shutdown group and were not reviewed here.

## Verdict: changes requested — 0 blockers, 1 major  (issue #144 fix, model: claude-opus-5-5)

### 🟡 Major 1 — the regression test passes when the fix's key line is reverted  ·  attribution: model

The key line of the fix is `drive`'s context: `context.WithTimeout(a.life, a.activationTimeout)`. A mutant that puts
back the pre-fix `context.WithTimeout(context.Background(), a.activationTimeout)`, with everything else of the fix
kept, passes `TestIssue144_RunStopReleasesHeldColdRequest`:

```
--- PASS: TestIssue144_RunStopReleasesHeldColdRequest (60.05s)
ok  	github.com/pyvvo/funcd/internal/activator	61.259s
```

With that mutant, `halt` cancels a context that the activation does not use, then `drives.Wait()` blocks until the
activation times out (the test's `ActivationTimeout` is one minute). The test reads `require.NoError(t, <-ran)` with
no time bound, so it waits the full minute. Only then does it start its 2 s check on the held request, which by then
has already been answered. This is the issue's own defect — shutdown waits out the activation timeout — and the test
accepts it. The test fails on a full revert only because the pre-fix `Run` returns at once; it does not assert the
property the issue reports.

**Fix (builder)**: bound the wait for `Run` as well, for example a `select` on `ran` with a `time.After` of a few
seconds and a `t.Fatal` that names the issue's symptom, before the check on `served`.

### Minor

- **Minor · model — two guards of the fix survive mutation.** Deleting `a.drives.Wait()` from `halt` passes the
  package's tests (`-race -count=3`); so does deleting the `if a.stopped { … return "", errStopped(fn) }` guard in
  `activate`. The first is the issue's second expected behavior ("no activator goroutine outlives Run"); the
  `require.Never` check on the poll count only catches it by chance. The second is the only thing that stops a cold
  request arriving after `Run` returned from sending a wake (`ScaleTo(fn, 1)`, a store write) during shutdown. Both
  guards are correct as written. **Fix (builder)**: after `Run` returns, assert that a new cold request gets a 503 at
  once and that the scaler is not called again; the bounded wait from Major 1 plus a `drive`-exited signal would cover
  the first guard.

### ✅ Verified correct (keep it)

- **The regression test fails on the pre-fix code, for the reported reason.** With `4f2295e` reverted and the fixed
  test file kept: `activator_test.go:303: the held cold request was not released when Run stopped: it waits out its
  activation timeout` — `FAIL` after 2.00 s. The revert of `4f2295e` was clean; no later commit touches these files.
- **It passes with the fix**, un-skipped, under `-race`, 5/5 runs at 0.05 s each; the whole `internal/activator/...`
  tree passes under `-race`.
- **The user-visible behavior is fixed.** I reran the issue's probe as a scratch e2e test through `-overlay` (an
  InMemory platform with the Node shim, a function with a 20 s top-level-await boot and `minReplicas: 0`, a cold call,
  the platform context cancelled 1 s later): `Run returned after 3.695167ms`, `activator drive goroutines=0`, held
  caller `code=503 elapsed=1.00046425s` with detail `activator stopped before default/slow became ready`. The issue
  reported 15.007 s, one goroutine and a 503 at 30.002 s.
- **Cause, not symptom.** The issue names the `context.Background()` root of `drive`'s context. The fix roots every
  activation in a lifetime context that `Run` cancels on return, and `Run` waits for the activations. No timeout was
  shortened, no error was swallowed, and the shutdown bound in `pkg/funcd` is unchanged.
- **Concurrency is sound.** `drives.Add(1)` happens under `a.mu` only while `stopped` is false, and `halt` sets
  `stopped` under the same lock before `Wait`, so no `Add` can race the `Wait`. A drive that ends on `ScaleTo` failing
  under the cancelled context still resolves its waiters (wrapped as Unavailable → 503).
- **Scope.** Two files; every hunk serves the issue. The test adds a resolve counter to the existing `fakeEndpoints`
  fake instead of a new fake. No test was weakened or deleted.
- **Reuse.** The fix uses `sync.WaitGroup`, `context.WithCancel` and `fault.Unavailablef`; the test reuses the
  package's `newActivator`, `serve`, `fakeScaler` hook and `fakeEndpoints`. Nothing duplicates an existing helper.
- **ADRs.** No ADR file is edited. ADR-0016 C3 allows a 503 only for "a genuine activation failure"; an activation
  the stopping platform can no longer complete is one, and the problem type stays `fault.Unavailable` as Decision 2
  requires. The single-flight (C4) and the clear-on-resolve behavior (Decision 3) are unchanged. ADR-0033 M4 (data
  plane drained before the ports close, `activator.Run` wg-tracked) now holds within the drain instead of at its 15 s
  bound.
- **Conventions.** ctx-first, `api/fault` errors, no `any` in signatures, no new imports, comments explain the why.
- **Checks.** `gofmt -l` clean; `go build ./...` ok; `go vet` on `internal/activator/...` and `pkg/funcd/...` ok;
  `golangci-lint` 0 issues on the host and with `GOOS=linux`; `go test -race` on `pkg/funcd/...`,
  `internal/eventing/...`, `internal/dataplane/...` ok; the e2e suite `go test -tags e2e ./pkg/funcd/...` ok
  (110.9 s). The Lima lanes were not run (a later stage owns the VM).
- **Shape.** `fix(activator): …`, `Fixes #144`, the attribution trailer, one issue in the commit.

### Definition of Done

10 / 11 items hold. Miss: item 4 (reverting or mutating the fix's key lines fails a test) — the key-line mutant
survives (Major 1, model).

### Model scorecard

Not recorded by this stage. Ledger fields: claude-opus-5-5 on issue #144 (fix) → changes-requested, 0/1/1,
2 model-attributed, DoD 10/11.

### Recommendation

Back to `/fix`: bound the wait for `Run` in `TestIssue144_RunStopReleasesHeldColdRequest` so the key-line mutant fails,
and add the post-stop cold-request assertion for the Minor. The production change in `activator.go` needs no rework.
