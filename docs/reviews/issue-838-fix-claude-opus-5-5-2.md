## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #838 fix, round 2, model: claude-opus-5-5)

Change: branch `fix/838-pooled-sibling-degraded`, three commits on `origin/main`:

- 2baabb6b `fix(function): keep a gated pooled member Ready when its pool host misses one health probe` (reviewed in
  round 1, pass, 0/0/1);
- b082bcd4 `test(function): pin that an unanswered members probe is not ready on a converging pass` (closes round 1's
  Minor 1);
- a8900669 `fix(function): count a pooled member's pool worker as running before it writes its port` (a second case of
  the same cause).

Files: `internal/function/function.go`, `internal/function/pool.go`, `internal/function/ready_test.go`,
`internal/function/shim_test.go` (+98/-12). The whole branch was reviewed again; this report focuses on the two new
commits.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — `countWorkers` now branches on an unstated property of `memberIn`'s result** · attribution: `model`.
  a8900669 adds `case w.ID == "": return running, 0, nil` to `countWorkers` (`internal/function/function.go:1287`).
  That case relies on `memberIn` returning a zero `runtime.Instance` exactly when no running pool worker in `insts` has
  a port (`internal/function/pool.go:281`). The doc comment on `memberIn` says only that "ok is false when there is
  none, it does not answer or it has no entry for member, and err is set only when it does not answer". It does not
  say what the returned instance is in each case. The behavior is correct today and the mutants below pin it, but
  the contract that a caller now depends on is stated nowhere. A future change to `memberIn` could return the
  candidate instance in the no-port case, and the regression test would catch it, but the reader would not see why. Fix:
  one clause in `memberIn`'s doc, such as "the instance is zero when no pool worker has a port to ask".

### Observations (not scored)

- **A pool worker before its port is counted as running for any member of its key.** Before the port is written,
  `/health/members` cannot be read, so `countWorkers` cannot know whether the worker's manifest lists the member.
  The fix counts the worker as running for the member and not listening. This matches ADR-0161 Decision 2's
  "one runs; none listens" row and the treatment of a solo replica before its port. A member whose manifest left it
  (for example, a member past `PoolLimit`) may therefore show `Degraded`/`Restarting` for one more period before the
  worker answers without its entry and the gate's phase is written. Before the fix, the old pool worker served until
  the new one listened, and the same transient happened, so nothing new is introduced. A gate failure does not
  change `admittedMembers` (it reads the stored members, not their gates), so in the issue's case the new worker's
  manifest still lists the gated member. No action is needed.
- **Commit shape across two fix commits**: 2baabb6b carries `Fixes #838`, and a8900669 carries `Refs #838`. Both
  commits are for issue #838, so the rule "one issue per commit" holds, and the PR closes the issue once. This is
  correct.

### ✅ Verified correct (keep it)

- **The second regression test fails without its commit, for the reported reason.** I overlaid `function.go` as it
  was at 2baabb6b (`git show 2baabb6b:<file>` into a scratch file, then `go test -overlay`); `pool.go` did not change
  after 2baabb6b. `go test -race -run TestIssue838 -v ./internal/function/` returned
  `--- FAIL: TestIssue838_PoolWorkerWithoutPortCountsAsRunning` at `ready_test.go:579`, `expected: "Degraded"`,
  `actual: "Failed"`, with the message "the pool worker runs". The test holds the running pool worker before its
  port, and the gate (`ShapeInvalid`) fails. The pre-fix code read the running worker as "none runs" and wrote the
  gate's phase. `TestIssue838_UnansweredMembersProbeKeepsGatedMemberReady` still passed with that overlay, as
  expected.
- **The first regression test still fails without the whole branch.** An overlay of `origin/main`'s `function.go`
  and `pool.go` made both `TestIssue838_…` tests fail: `ready_test.go:553` (`expected: "Ready"`,
  `actual: "Failed"`) and `ready_test.go:579` (as above).
- **Both pass with the branch**, un-skipped, under `-race`, together with the new
  `TestUnansweredMembersProbeDegradesServingMember` (each `--- PASS ... (0.01s)`).
- **Round 1's Minor 1 is closed.** Mutant M3 (`convergePooled` propagates `memberIn`'s error:
  `if perr != nil { return verdict{}, 0, perr }`) now fails `TestUnansweredMembersProbeDegradesServingMember` in the
  full `internal/function` suite. The test checks a serving member on a converging pass with the probe down. It
  requires `Degraded`, `replicas: 0` and `Ready=False`, which is ADR-0158 Decision 4's mapping for "a failed
  probe ... → not ready (Degraded once serving)".
- **Mutants on the new lines**, each run against the full `internal/function` suite:
  - M4: the no-port case counts the worker as listening (`return running, running, nil`). This fails
    `TestIssue838_PoolWorkerWithoutPortCountsAsRunning`.
  - M6: `memberIn` returns an `Unavailable` error when no pool worker has a port, which treats "no port yet" as
    "did not answer". This fails the same test, so the two cases stay separate.
  - The revert itself (the 2baabb6b overlay) fails the test, as shown above. All three were killed.
- **Cause, not symptom.** `memberIn`'s `ok=false` had three meanings: no pool worker with a port, no answer, and no
  entry for the member. `countWorkers` read all three as "no worker runs". Round 1 separated "no answer", which is
  now an error that goes to `failPass`. This commit separates "no port yet", which now counts as running and not
  listening. Only "answered without the member's entry" still counts as none, and that is the only case in which
  the pool worker is known not to run the member. There is no timeout, retry, skip or swallowed error.
- **ADRs.** No file under `docs/` changed.
  - **ADR-0161 (Implemented)**: Decision 2's `gateFailed` table maps "one runs; none listens" to `Degraded`/`Restarting`
    with `replicas: 0` and the period requeue. The test asserts exactly that, including `RequeueAfter == testPeriod`.
    `failPass` reads `listeningCount`, which is 0 for a pool worker without a port both before and after the change,
    so it is unchanged.
  - **ADR-0158 (Implemented)**: Decision 4 still holds on the converging path. `convergePooled` is untouched by
    a8900669 and still reads no entry, no port or a failed probe as not ready; b082bcd4 now pins that.
  - **ADR-0142 (Implemented)**: the `Degraded` member is requeued after the supervision period and read again.
    Once the pool worker writes its port, the next pass probes `/health/members` and restores or judges the member.
- **Scope**: every hunk serves #838. The new commits change one `switch` in `countWorkers`, update its doc comment,
  and add two tests. The fake runtime's existing `hold` helper is reused to make the running pool worker portless
  (`snapshot` drops `IP`/`Port` while the worker is held). No test was weakened or deleted.
- **Siblings**: the no-port case is the remaining sibling of the conflation round 1 fixed. Under Step 13, round 1
  should have noted it, and the fixer found and fixed it in the same run. `memberIn` has two callers: `countWorkers`
  (fixed) and `convergePooled` (correct as is, and now pinned). `probeMembers` has one caller.
- **Reuse**: `hold`, `poolOf`, `newShimHarness` with `withSwitch`/`withNodePool`, `requireCondition`, and the
  round-1 `setMembersDown` switch. There is no new helper, type or dependency.
- **User-visible behavior**: `go test -race -count=1 -run 'TestIssue796|TestScenarioPooled|TestScenarioPoolFull'
  ./pkg/funcd/` → `ok` (8.0s). This holds the issue's scenario and the pooled scenarios on a real assembled
  platform. The issue's failure was a one-off under host load, so this confirms that nothing regressed. The unit
  tests reproduce both causes deterministically.
- **Checks**:
  - `go test -race -count=1 ./internal/function/` → `ok` (10.5s).
  - `go build ./...` and `go vet ./internal/function/` pass on darwin and with `GOOS=linux`.
  - `golangci-lint run ./internal/function/...` → `0 issues.` on darwin and with `GOOS=linux`.
  - `gofmt -l internal/function/` is clean.

  No e2e suite or Lima lane was run, and `go test ./...` was not run for the whole repository; these run once in the
  PR gate and in CI.
- **Conventions**: `ctx`-first, a `switch` in the surrounding idiom, and doc comments that state the why with the ADR
  and the issue. The tests have no narration.
- **Shape**: both fix commits use `fix(function):`. 2baabb6b carries `Fixes #838`, a8900669 carries `Refs #838`, and
  b082bcd4 is a `test(function):` commit. Each carries the attribution trailer.

### Definition of Done

12 / 12 items hold.

- Items 1–4: both regression tests reproduce their cases, each fails without its fix, each passes under `-race`, and
  every mutant on the key lines was killed.
- Item 8: e2e and the lanes were not run per review; they run once in the PR gate and CI.
- Item 9: Minor 1 is a doc gap and does not breach a convention.

### Model scorecard

Recorded as one ledger row (written by the caller): claude-opus-5-5 on issue #838 (fix, round 2) → pass, 0/0/1,
1 model-attributed, DoD 12/12, report `docs/reviews/issue-838-fix-claude-opus-5-5-2.md`.

### Recommendation

Ship. Minor 1 is a one-clause doc fix on `memberIn`. It can land with this PR or later, and it does not block.
