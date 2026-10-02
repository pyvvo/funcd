## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #398 fix, model: claude-opus-5-5)

Change: branch `fix/398-dev-reapply-conflict`, commit efe3b34 `fix(funcdctl): retry a dev re-apply that loses
its update to a controller` (`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_phase2_test.go`; +77/-1). The package
builds only with `-tags dev`, so every check below ran with that tag.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — no test pins that only a Conflict is retried** · attribution: `model`.
  Mutant M3 replaced the Conflict filter in `applyDesired` with "retry on any error, return on success"
  (`if _, err = c.Apply(ctx, obj); err == nil { return nil }`). The whole package still passed under `-race`
  (`ok … 7.176s`). With that mutant, a permanent error such as an Invalid manifest is sent 5 times before
  `startDev` fails. The returned error is the same, so the impact is small, but the Conflict-only contract is
  unguarded. Fix: a sibling case that answers the first PUT with a non-Conflict problem (for example
  `fault.Invalidf`) through the same `conflictOnFirstPut`-style transport, and requires that the boot fails
  after exactly one PUT.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** The `origin/main` version of
  `cmd/funcdctl/dev.go` was overlaid with `go test -overlay`, and the fixed test file was kept.
  `--- FAIL: TestIssue398_DevPersistReapplyRetriesConflict (0.35s)` at `dev_phase2_test.go:210`:
  `funcdctl dev: apply Function "001": sdk: store.Update: Function "001" resourceVersion mismatch`. This is the
  issue's error text.
- **It passes with the fix under `-race`.** `go test -tags dev -race -count=1 ./cmd/funcdctl/` → `ok` (9.411s).
  A second run with `-shuffle=on -v` gave 61 top-level tests passed, 0 skipped, 0 failed and no data race.
- **The issue's own test reproduces the real race, and the fix removes it.** The issue's failing test,
  `TestScenarioDevPersistSurvivesRestart`, was run 5 times under `-race` on the pre-fix code. It failed 2 of 5
  times with no injection: `funcdctl dev: apply KVStore "cache-kv": sdk: store.Update: KVStore "cache-kv"
  resourceVersion mismatch`. The race is therefore not limited to the Function reconciler that the issue
  names. Any controller that writes status during the re-apply can trigger it, and the fix covers every
  resource group (`resObjs`, `fnObjs`, `extraObjs`). A diagnostic overlay on the fixed code logged each
  Conflict. Over 5 runs of the issue's test and the regression test, there were 4 real KVStore conflicts and
  5 injected Function conflicts. Every one was resolved on the second attempt, and all 10 tests passed. The
  bound of 5 attempts leaves wide margin.
- **The root cause is fixed, not masked.** The control plane's `Replace` reads the current resourceVersion
  and then updates (`internal/controlplane/handlers.go` `replaceObj`, ADR-0018). A status write between the
  read and the update returns a 409, and `bootDev` treated that 409 as fatal. A dev boot applies the whole
  desired state, so applying it again after a lost optimistic update is correct, not a workaround. The
  timeouts are unchanged, no error is swallowed (after 5 Conflicts the last error is returned and wrapped as
  before), and no test was skipped. The issue's other option, applying before the controllers start, is not
  available: `p.Run` serves the API that the apply calls. A server-side retry inside `Replace` would change
  ADR-0018's Contract (store kinds pass through, Conflict→409), so a client-side retry is the right scope.
- **Mutants on the key lines fail a test.** M1 (`devApplyAttempts = 1`) → `TestIssue398_…` FAIL, and
  `TestScenarioDevPersistSurvivesRestart` also failed on the real KVStore race. M2 (filter on
  `fault.Unavailable` instead of `fault.Conflict`) → `TestIssue398_…` FAIL. M3 survived (Minor 1).
- **The test's transport swap cannot leak into other tests.** `TestIssue398_…` is a top-level test that does not
  call `t.Parallel()`. The Go test runner releases the package's parallel top-level tests (in `cli_test.go`
  and `workflow_test.go`) only after every sequential top-level test has returned, so none of them runs during
  the swap. Cleanups run in LIFO order: first the cleanup that cancels the context and calls `inst2.stop()`,
  which waits for `p.Run` and the hot-reload watcher, then `cancel2`, then the restore of
  `http.DefaultClient.Transport`. No goroutine of the instance
  can read the transport during the restore. The fake intercepts only the first PUT to one Function path, so
  any other user of `http.DefaultClient` in the platform passes through to the real transport. Neither race
  run (ordered or shuffled) reported a data race. The swap is the only available seam: `startDev` builds its
  client with `sdk.New`, which defaults to `http.DefaultClient`, and adding a seam to production code only
  for this test would be worse.
- **Scope.** Every hunk serves #398: the `devApplyAttempts` constant, `applyDesired`, its single call site in
  `bootDev`, and the regression test with its fake transport. Both `startDev` entry paths (functions and
  Workflow) reach the fix through `bootDev`. No test was weakened or deleted. The hot-reload `reload` apply is
  correctly left alone: on a Conflict it does not advance `h.fn`, so the next poll applies the edit again.
- **Reuse.** The loop is the codebase's existing idiom for a bounded retry on Conflict. It matches
  `linkAttempts` in `internal/workflow/reconcile_run.go` and `maxAttempts` in
  `internal/activator/storescaler/storescaler.go`, including the immediate retry with no backoff, because each
  attempt re-reads the resourceVersion. No shared client-side retry helper exists in `pkg/sdk`,
  `internal/platform` or `api/fault`. The `retryOnConflict` functions in the reconcilers swallow a Conflict
  so that the controller requeues, which is a different job. `cenkalti/backoff` is only an indirect
  dependency. The test reuses `requireRuntime`, `devProject` and `permissiveContract`, and it builds the 409
  with the server's own `fault.WriteProblem`, so the SDK decodes it exactly like a real one.
- **Conventions.** ADR-0002 holds: the function is ctx-first, uses `api/fault` kinds, adds no exported surface
  and no `any`. The constant name follows `devPersistDir` and `devReloadPoll`. Imports are at the top level, and
  the comments state the why (ADR-0018 read-then-update, why a re-apply is safe) without narration.
  `go vet -tags dev` passes on the host and with `GOOS=linux`. `golangci-lint run --build-tags dev` reports
  0 issues on the host and with `GOOS=linux`. `gofmt -l` is clean.
- **ADRs.** No ADR file was touched. The change agrees with ADR-0018 (`Replace` is read-then-update, and a
  Conflict reaches the client as 409) and with ADR-0125 (`funcdctl dev --persist` restores state and
  re-applies the desired resources).
- **Shape.** The subject is `fix(funcdctl): …`, the body names the regression test and says `Fixes #398`, the
  attribution trailer is present, and the branch has one commit for one issue.

Not run here, by design: e2e, the repo-wide tests and the Lima lanes. The group gate and CI run them.

### Recommendation

Pass. Minor 1 is an optional follow-up: a test case that pins the Conflict-only retry by failing the boot after
one PUT on a non-Conflict error.
