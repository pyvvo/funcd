## Verdict: pass — 0 blockers, 0 majors, 5 minors, all fixed in 2980fe1a and 4e63d147  (ADR-0259 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0259-late-health-answer` at 4e63d147, with four commits on main d2f6a92f:

- 31539fc0 docs: ADR-0259 Accepted, the four "Superseded in part by" back-links, F13 accepted, the blueprint and the
  summary row;
- ab90e645 feat (`Fixes #1127`): the code, with ADR-0259 and F13 moved to Reviewing;
- 2980fe1a fix: the five Minors this review found on ab90e645;
- 4e63d147 test: the requeue half of the fourth Minor, a held revision's pass by its window's end.

The change is `internal/function/readiness.go` (new: the read record, `settleRead`, `readReadiness`, `lateRead`,
`notAnswering`, `pollBy`), its seams in `function.go`, `pool.go` and `liveness.go`, the key in
`internal/platform/config/config.go`, `cmd/funcd/main.go`, `pkg/funcd` and `examples/funcdconfig.yaml`, and the tests.

### Minor (found on ab90e645, fixed in 2980fe1a and 4e63d147)
- **A 503 whose body arrives after the deadline was judged as an answer, not as late** · attribution: model ·
  evidence on ab90e645: `internal/function/liveness.go:167-169`.
  - `probeReadiness` decoded the body of a 503 and returned `false, nil, false` on any decode error.
  - The probe client's `Timeout` also bounds the body read (`internal/platform/httpx/httpx.go:53`), so a body that ran
    past `probeTimeout` failed the decode with a timeout error that nothing checked.
  - Decision 1 makes such a read late. `probeMembers` already handled the same case with `lateRead(pctx.Err())`
    (`function.go:2909`).
  - Fixed in 2980fe1a: the 503 branch returns `late` when the decode error is a timeout.
    `TestReadiness503BodyPastDeadlineIsLate` sends the 503 headers and holds the body. Mutant M9, which treats that
    timeout as an answer again, fails it.
- **No test held "a kept answer never marks liveness"** · attribution: model · evidence on ab90e645: mutant M4 marks
  liveness on any member list the read counts by, kept ones included (`pool.go:346`), and it passed the whole
  `internal/function` package.
  - Fixed in 2980fe1a by `TestKeptAnswerIsNotLiveness`. It gives the pool host its own endpoint and a one-hour window,
    turns the host's liveness to 503 while its member reads are late, and requires a restart after three periods.
  - M4 now fails it.
- **No test held "the switch's real answer is recorded"** · attribution: model · evidence on ab90e645: mutant M5
  drops the `settleRead` in `switchDue` (`pool.go:1133`), and it passed.
  - Fixed in 2980fe1a by `TestSwitchDueRecordsItsAnswer`, which calls `switchDue` and requires the record of `next` to
    hold a listed answer. M5 now fails it.
- **No test covered a held revision's path** · attribution: model · evidence on ab90e645: mutant M7 restores main's
  held path, which reads `Restarting` past the window and requeues a Ready held revision after the period
  (`function.go:2258`, `:2279`), and it passed.
  - The `NotAnswering` half is fixed in 2980fe1a by `TestHeldRevisionLatePastWindowIsNotAnswering`. It wakes a held
    revision, stalls its readiness under a 1 ns window, and requires `Degraded`, `NotAnswering` and "…for 1ns". M7
    now fails it.
  - The requeue half is fixed in 4e63d147 by `TestHeldRevisionComesBackByTheWindowsEnd`. With a 20 ms window and the
    harness's 50 ms period, one pass must leave the held revision Ready with `0 < RequeueAfter <= 20ms`.
  - Mutant M7b keeps only the requeue half: it requeues a Ready held revision after the period. It passed the package
    on 2980fe1a and fails the new test on 4e63d147.
- **The doc comment of `reasonNotAnswering` stated the rule for one worker** · attribution: model · evidence on
  ab90e645: `internal/function/readiness.go:16-17` said "a worker of it is unanswered".
  - Fixed in 2980fe1a: the comment now says that every worker it judges runs and is late past
    `runtime.readinessTimeout`, as Decision 3 and `function.go:2837` require.

### ✅ Verified correct (keep it)
- **The record and the window (Decision 2).**
  - `settleRead` keys the record by id and `CreatedAt`, and starts a new record for a new `CreatedAt`.
  - It opens the window at the first late read after the last answer, counts by the last answer while `now` is
    before the window's end, and closes the window on any answer.
  - A worker with no recorded answer gets no window and counts as not ready.
  - A kept answer keeps its `listed` flag, so a kept non-200, or a body with no member list, makes `memberIn` fail as
    it did when the answer arrived.
- **Late reads (Decision 1).** `lateRead` is true for `context.DeadlineExceeded` or a `net.Error` whose `Timeout()` is
  true. A refused or reset connection is an answer: `TestResetReadIsJudgedAtOnce` reads `Restarting`, and mutant M3
  (every failed read is late) fails it.
- **Past the window (Decision 3).**
  - `readyReplicas` counts every judged replica, an exited one included. It sets `silent` only when all of them run
    and are late past the window (`function.go:2837`). Mutant M2 (count only the late ones) fails
    `TestExitBesideALateReplicaKeepsRestarting`.
  - `finish` checks for `NotAnswering` after the start error, the crash loop, the repair, the liveness restart and the
    dependency report (`function.go:1132`).
  - The messages match the ADR's table. `TestReadinessTimeoutSetsTheWindow` shows a set window in the message
    (`for 2s`).
- **Pacing (Decision 4).**
  - The steady return (`function.go:564`), `record` (`:1194`) and a Ready held revision (`:2279`) come back by the
    window's end through `pollBy`. The gate path keeps its pacing.
  - `TestScenarioLateAnswerKeepsReady` checks the 5 s and the 1 ms requeues on both placements: the steady pass for
    solo, the full pass for pooled. `TestHeldRevisionComesBackByTheWindowsEnd` checks the held revision.
  - `steadyState` ends past the window (`:1339`). Mutant M8, which ignores that, fails the solo half of
    `TestScenarioUnansweredPastWindow`.
- **Liveness and the pool switch.** `memberIn` marks liveness only on a member list that has just arrived, which
  `TestKeptAnswerIsNotLiveness` holds. `switchDue` decides on its own fresh read and records a real answer, which
  `TestSwitchDueRecordsItsAnswer` holds.
- **Records dropped at each `Create`.**
  - `forgetRead` runs after each `Create` (`function.go:1899`, `pool.go:864`), and `forgetWorker` and `forgetLive`
    drop records too.
  - Mutant M6 (no `forgetRead` at `Create`) fails `TestScenarioExitSeenAtOnce` and
    `TestLateAnswerKeepsSoloReadyInAFullPass`. In the harness, the manual clock can give a re-created worker the same
    `CreatedAt`, so the drop at `Create` is what resets the record there.
- **Revert check.** Mutant M1 has no kept answer and no window, which is main's judgment. It fails
  `TestIssue1127_LateMembersAnswerKeepsReady`, both scenario tests, the window test and the solo full-pass test. The
  exit test, the hidden-exit test and the reset test pass on it, as they should.
- **Contracts and config.**
  - These match the ADR: the `Config.Runtime.ReadinessTimeout` field and its comment, the `Pacing` field, the `Deps`
    field (a negative value is refused in `NewReconciler`), `reasonNotAnswering` and the commented example line.
  - `pacing` parses the key with a 5 s default and `minPositive`.
  - `TestScenarioReadinessTimeoutConfig` covers the default, the file, the env variable, and the refusal of `0s`,
    `-1s` and `abc` with the key named.
- **Tests.**
  - There is one test per scenario, named after it. The three behavioural scenarios run for both placements
    (`eachPlacement`), and every new test calls `t.Parallel()`.
  - The changed tests weaken nothing. They add the new `Pacing` field to two expectations, extract `stallPastProbe`,
    add the `silent` and `membersLate` switches, let `servePoolWorker` stall its member reads while `setMembersLate`
    is on, and adapt one call to the new signature.
- **Docs.**
  - ADR-0259's words are unchanged since 31539fc0 except its Status line (word diff). ab90e645 re-wraps three
    paragraphs, which changes only whitespace.
  - ADR-0142, ADR-0161, ADR-0215 and ADR-0221 gain only their "Superseded in part by" back-links. F13 reads
    `reviewing`.
- **Checks on ab90e645.**
  - `go test -race -count=1` passes for `./internal/function/` (12.1 s), `./pkg/funcd/` (21.4 s) and
    `./internal/platform/config/` (1.6 s).
  - In `./cmd/funcd/`, `TestScenarioRestoreAnyPoint` failed once on a Badger directory lock in its temporary data
    directory. That is the kvbackup restore code, which this change does not touch. A rerun of the whole package
    passed (51.8 s). This is an environment flake, not a finding.
  - `go vet` exits 0, and `golangci-lint` reports 0 issues on the four packages.
- **Checks on 2980fe1a,** which changes only `internal/function`.
  - `go test -race -count=1 ./internal/function/` passes (11.5 s).
  - `go vet` exits 0, and `golangci-lint` reports 0 issues.
  - Mutants M4, M5, M7 and M9 each fail the test that the fix added.
  - `scripts/agent/gate.sh` on 2980fe1a ends GATE PASS: audit, ci-full (596 s), linux, stress (285 s) and
    tree-unchanged pass. linux-tests is skipped because its VM was not running; the change touches no Linux-only
    package, and CI runs Linux on the PR.
- **Checks on 4e63d147,** which changes only `internal/function/readiness_test.go`.
  - `go test -race -count=1 ./internal/function/` passes (11.9 s).
  - `go vet` exits 0, and `golangci-lint` reports 0 issues.
  - Mutant M7b fails `TestHeldRevisionComesBackByTheWindowsEnd`.
  - This test-only commit gets no second gate run; CI runs it on the PR.

### Definition of Done
9 / 9: the 8 checklist items plus Done-when. Every item is now held by a test.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0259 (implementation) → pass, 0/0/5, 5 model-attributed (found on ab90e645; fixed
in 2980fe1a and 4e63d147), DoD 9/9. See docs/reviews/model-scorecard.md.

### Recommendation
Pass. 2980fe1a and 4e63d147 fix the five Minors, and each fix is held by a test that fails without it.
