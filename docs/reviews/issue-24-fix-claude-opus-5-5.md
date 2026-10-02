# Issue #24 Fix Review — a broken redeploy spins the Function reconciler

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason and passes with the fix
under `-race`. Three mutants of the fix line each fail a test. The real-engine probe goes from about 13,000 reconciles
in 5 s to none. The change removes the cause the issue names, touches nothing else, and conforms to ADR-0143,
ADR-0047 and ADR-0142. There are two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #24 · ADR-0143 Decisions 4.5–4.7 and 5 · ADR-0047 Decision 1 · ADR-0142 Decision 9 ·
ADR-0002 · `CLAUDE.md` style rules

The change is one commit on `fix/24-broken-redeploy-storm` on top of `origin/main` (`cde618f`). It touches
`internal/function/function.go` (`finish()`) and `internal/function/switch_test.go` (one new test).

## Verdict: pass — 0 blockers, 0 majors  (issue #24 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — the regression test sets a knob it does not use.** The test's harness option sets
  `d.HandOutSettle = time.Millisecond`, which it copied from `withSwitch`. No switch and no drain happen in the test,
  so the setting has no effect. With that line removed, the test still passes on the fix (3/3) and still fails on the
  pre-fix code (rv 7→10 in both subtests). The `SupervisionPeriod = time.Hour` beside it is needed: it keeps the crashed
  serving worker in its backoff without a sleep. **Fix**: drop the `HandOutSettle` line.
- **Minor · env — `internal/artifact` has a flaky test that this change does not cause.** My first `go test -count=1 ./...`
  run, made while other probes loaded the host, failed `TestScenarioSiteArtifactRoundtrip` ("an identical tree yields an
  identical digest"). A rerun of that test with `-count=40` passed, and a second full run passed. The cause is in
  `internal/artifact/site.go`: `PushSite` calls `oras.PackManifest` without a fixed `org.opencontainers.image.created`
  annotation, so oras stamps the current second into the manifest. Two pushes that straddle a second boundary then get
  different digests. The fix does not touch this code path. **Fix**: file it as a `kind/flake` issue (outside this fix).

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** I built an overlay from `git show origin/main:internal/function/function.go`
  (byte-identical to the fixer's revert file) and ran `go test -race -overlay … -run TestIssue24_`. Both subtests fail at
  `switch_test.go:765`, which asserts that three passes write nothing: `expected: "7"`, `actual: "10"`. The earlier
  assertions pass on the pre-fix code: phase Ready or Degraded, RevisionReady `ShapeInvalid`, ShapeValid False. So the
  test fails for the reason the issue reports, which is that every pass writes.
- **It passes with the fix**: `-race -count=5` gives 5/5 for both subtests, and `-race -count=50` passes. Nothing is
  skipped and the test does not sleep.
- **The user-visible storm is gone.** I ran the investigator's real-engine probes (the real controller engine, the
  process driver and the Node shim) through `-overlay`:
  - `TestChaos_supervision_BrokenRedeployStorm` with the fix, in a 5 s window with a 10 s period: `reconciles 8→8, store
    events 6→6, resourceVersion 8→8, runtime lists 50→50`. ShapeValid's `lastTransitionTime` did not move. The status
    is `phase=Ready … current=bad-2 serving=bad-1 … revisionReady=False/ShapeInvalid`.
  - The same probe on `origin/main`: `reconciles 1333→14699, store events 1333→14698, resourceVersion 1335→14700,
    runtime lists 11984→132275`. `lastTransitionTime` moved by 5 s. The host had about 14,700 TIME_WAIT sockets
    afterwards.
  - `TestChaos_supervision_KillCurrentDuringSwitch` with the fix, over 10 s with a 1 s period: `reconciles=17
    storeEvents=6`, phase Ready, and sc-1 still serving. I did not rerun the main variant of this probe, because the
    issue records that it nearly exhausted the host's ephemeral ports.
- **The cause is fixed, not the symptom.** Before the fix, `finish()` set ShapeValid True in the first switch and then
  False in the second (`switching && currentFailed`). `Conditions.Set` (`api/types/v1alpha1/status.go`) moves
  `LastTransitionTime` on each status flip, which defeated the store's no-op-write coalescing (ADR-0047). The fix sets
  ShapeValid once, from `v.shapeFailed || (v.switching && v.currentFailed)`. That predicate gives the same final value as
  the old code in every case: the shapeFailed arm and the switch arm were the only False writers, and both used the
  same message. Condition order in the slice is also unchanged. The fix adds no timeout, retry or suppressed write.
- **Mutants**: each mutant was killed by a test.
  - The predicate reduced to `v.shapeFailed` fails `TestScenarioFailedRevisionKeepsOldServing` and
    `TestIssue24_BrokenRedeployPassWritesNothing`.
  - The predicate reduced to `v.switching && v.currentFailed` fails `TestScenarioShimShapeFailureBlocksReady` and
    `TestScenarioBootFailureStaysFailed`.
  - The `else` branch (ShapeValid True) dropped fails four supervision and readiness tests.
- **Scope**: both hunks serve the issue, and no test was weakened or deleted. The issue also names an amplifier:
  `probeReady` closes the response body without reading it, so each probe opens a new connection. The fix rightly
  leaves that alone.
- **Reuse**: the test is built entirely from existing package helpers (`newShimHarness`, `createFn`, `apply`,
  `reconcile`, `getFn`, `condition`, `shapeValid`, `fakeRuntime.failRevision` / `exitRevision`). It follows the shape of
  `TestScenarioSteadyFunctionStaysQuiescent`. It uses `createFn` + `reconcile` instead of `deployReady` because
  `deployReady` asserts the 50 ms requeue. The production change adds no helper, type or dependency.
- **Conventions**: there is no new API surface. The one code comment explains why the condition is set only once, and
  the test's doc comment cites ADR-0143 and ADR-0047. The test is named `TestIssue24_BrokenRedeployPassWritesNothing`.
- **ADRs**: the fix keeps ADR-0143 Decision 5: phase and `Ready` follow S, and ShapeValid and RevisionReady describe C.
  It keeps the Decision 4.7 requeue (the supervision period while S serves a failed C). It implements the quiescence
  rule of ADR-0047 Decision 1 and ADR-0142 Decision 9. No ADR file was edited.
- **Checks**, all run by me through `nix develop -c`:
  - `gofmt -l`: clean.
  - `go build ./...` and `GOOS=linux go build ./...`: ok.
  - `go vet ./...`, on the host and for Linux: ok.
  - `go tool golangci-lint run ./...`, on the host and for Linux: "0 issues." both times.
  - `go test -race -count=1 ./internal/function/`: ok.
  - `go test -count=1 ./...`: rc 0 with 79 packages ok, on the rerun (see the env Minor).
  - `go test -tags e2e -count=1 ./pkg/funcd/...`: ok in 105.6 s.
  - `just check-hygiene`: "hygiene: clean".
  - No Lima lane covers `internal/function`, and `api/types` is unchanged.
- **Shape**: the commit has a `fix(function):` subject and a body that states the cause and names the regression test.
  It carries `Fixes #24` and the attribution trailer, and the branch holds one commit for one issue.

### Definition of Done
11 / 11 items hold (the fix checklist). The PR is not open yet, and its description must carry `Fixes #24`.

### Model scorecard
Recorded: claude-opus-5-5 on issue #24 (fix) → pass, 0/0/2, 1 model-attributed, DoD 11/11. See
docs/reviews/model-scorecard.md.

### Recommendation
Sign off and hand back to `/fix` Step 8 to open the PR. Dropping the unused `HandOutSettle` line is optional. Two
follow-ups are worth filing as separate issues with the user's go-ahead:
- the `PushSite` created-timestamp flake;
- the `probeReady` amplifier, which no longer causes a storm because a pass now writes nothing.
