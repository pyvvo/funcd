# Issue #28 Fix Review — the workflow dispatcher's fixed 30 s HTTP timeout overrides per-step timeouts

**Verdict**: **pass**. Both regression tests fail on the pre-fix code for the reported reason and pass with the fix
under `-race`. Three mutants of the key lines each fail a test. The change removes the cause the issue names (the
`http.Client.Timeout` on the step dispatcher), closes the gap that removal would open (the onFailure handler dispatch
had no per-step bound), and conforms to ADR-0094 and ADR-0002. There is one Minor, attributed to the issue.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #28 · ADR-0094 Execution (per-step `timeout`) and Wiring (`workflow.defaultStepTimeout`) ·
ADR-0002 · `CLAUDE.md` style rules

The fix is two commits on `fix/198-workflow`, reviewed at the group HEAD `fbcb9ff`:

- `41c9c77` — `pkg/funcd/funcd.go` (the dispatcher client drops `Timeout: 30 * time.Second`) and
  `pkg/funcd/workflow_e2e_test.go` (`TestIssue28_StepLongerThan30sHonorsStepTimeout`).
- `5c045ed` — `internal/workflow/engine.go` (a shared `stepTimeout` helper; `fail()` bounds the onFailure handler
  dispatch with it) and `internal/workflow/engine_scenarios_test.go` (`TestIssue28_OnFailureHandlerHonorsStepTimeout`).

## Verdict: pass — 0 blockers, 0 majors  (issue #28 fix, model: claude-opus-5-5)

### Minor
- **Minor · issue — the issue's Sensor facet is not the same defect, and the PR should say why it is left.** The issue
  says the Sensor `HTTPInvoker` (`pkg/funcd/funcd.go`, the `sensor.NewReconciler` wiring) has "the same hard 30 s
  client timeout". It does keep `Timeout: 30 * time.Second`, but nothing in `internal/sensor` sets a context deadline
  (`grep WithTimeout|Deadline internal/sensor/*.go` finds nothing outside tests), and a Sensor action has no
  per-invocation timeout to override. So for the Sensor the 30 s cap is the only bound, not a cap that overrides a
  longer one; removing it would leave Sensor invokes unbounded. The fix correctly leaves it alone, but neither commit
  message says so. **Fix**: one sentence in the PR description noting that the Sensor's 30 s is its only invoke bound
  and stays; a configurable Sensor invoke timeout, if wanted, is a separate issue.

### ✅ Verified correct (keep it)
- **The e2e regression test fails without the fix.** A clean `git revert --no-commit 5c045ed 41c9c77` conflicts with
  later commits of the group in `internal/workflow/engine.go` and its scenario test, so I overlaid pre-fix versions of
  the touched files instead. With `pkg/funcd/funcd.go` overlaid with the client's `Timeout: 30 * time.Second`
  restored, `go test -tags e2e -overlay … -run TestIssue28_ ./pkg/funcd/` fails: phase `Failed` instead of
  `Succeeded`; step `slow` failed on attempt 1 after 30.0 s (StartedAt→EndedAt) with
  `workflow.dispatch: invoke default/long-slow: Post "http://127.0.0.1:…": context deadline exceeded` — the issue's
  exact failure, while the step's own timeout is 60 s.
- **The onFailure regression test fails without its fix.** With `internal/workflow/engine.go` overlaid with the
  `fail()` bound removed (handler dispatched on the bare run ctx, as before `5c045ed`), the unit test fails:
  `handler dispatch deadline in 0s, want within its 2s step timeout`.
- **Both pass with the fix, under `-race`**: `TestIssue28_StepLongerThan30sHonorsStepTimeout` passes in 32.7 s with
  `-tags e2e -race`; `TestIssue28_OnFailureHandlerHonorsStepTimeout` passes with `-race`. Neither is skipped.
- **The user-visible behavior is fixed**: the e2e test is the issue's own reproduction on the real in-process
  platform (real Node shim worker, OCI-pushed step image): a step that answers after 32 s, past the old cap, now
  succeeds within its 60 s timeout.
- **Mutants** (overlay on `internal/workflow/engine.go`), each fails a test:
  1. `stepTimeout` ignores `fn.Timeout` and returns only the engine default → `TestIssue28_OnFailureHandlerHonorsStepTimeout`
     fails.
  2. `dispatchStep` uses no step timeout (`stepTimeout := time.Duration(0)`) → the package hangs on an existing
     step-timeout test and fails at the 10 min test deadline. Killed, though by a hang rather than a fast assertion;
     that coverage predates this fix.
  3. The handler bound is 10× its step timeout → `TestIssue28_OnFailureHandlerHonorsStepTimeout` fails.
  The `funcd.go` line itself is covered by the pre-fix overlay above (restoring the 30 s fails the e2e test).
- **Cause, not symptom**: the 30 s client-wide `Timeout` is removed rather than raised, so the engine's per-attempt
  `context.WithTimeout` (the step's `timeout`, else `workflow.defaultStepTimeout`, 300 s by default) is the one bound,
  as ADR-0094 specifies. The step dispatcher's `http.Client` is used only by `HTTPDispatcher` and only through
  `Dispatch` calls that now all carry a step-bounded context. The follow-up commit is the right companion: without the
  client cap, the onFailure dispatch in `fail()` had no bound at all.
- **No new unbounded path beyond what the config documents**: a step with no `timeout` under
  `workflow.defaultStepTimeout: 0` is now unbounded, which is what `Config.DefaultStepTimeout` documents ("0 = none")
  and what the 300 s default prevents.
- **Scope**: every hunk serves #28. The `stepTimeout` helper replaces the inline computation in `dispatchStep`
  verbatim and is reused by `fail()` (no duplicated logic). No test was weakened or deleted.
- **Reuse**: the e2e test uses the existing harness (`shimPlatformOCI`, `writeStep`, `pushStepImage`,
  `waitMaterializedReady`, `getRun`); the unit test uses `newTestEngine`, `step`, `spec`. The new `handlerDeadline`
  fake follows the package's idiom of one small fake dispatcher per scenario; none of the existing fakes records the
  context deadline.
- **Conventions**: ctx-first, no `any` in signatures, imports at top level (`errors` added to the test's import
  block), comments state the why (the `funcd.go` comment names ADR-0094 and why no client cap), gofmt clean.
- **ADRs**: no Accepted/Implemented ADR file touched; the change implements ADR-0094's per-step timeout semantics
  rather than altering them.
- **Checks** (all through `nix develop -c`): `gofmt -l internal/workflow pkg/funcd` empty; `go build ./...` and
  `GOOS=linux go build ./...` ok; `go vet` (host, Linux, and `-tags e2e`) clean for both packages;
  `golangci-lint run ./internal/workflow/... ./pkg/funcd/...` 0 issues on host and Linux;
  `go test -race ./internal/workflow/... ./pkg/funcd/...` ok (3 packages);
  `go test -tags e2e ./pkg/funcd/...` ok (170 s). No Lima lane was run (owned by a later stage).
- **Shape**: both commits are `fix(workflow): …`, carry `Fixes #28` and the attribution trailer, and touch only #28.

### Definition of Done
11 / 11 items hold. Misses: none.

### Model scorecard
Not recorded here; a later stage records: claude-opus-5-5 on issue #28 (fix) → pass, 0/0/1, 0 model-attributed,
DoD 11/11.

### Recommendation
Sign off. When the group PR is written, add one sentence that the Sensor invoker keeps its 30 s because it is that
path's only invoke bound.
