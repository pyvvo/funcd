## Verdict: changes requested — 0 blockers, 2 majors, 3 minors (ADR-0357 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/infrastack-apps` at 426de2e6, on origin/main 6f060256. The work is five commits:
0878d5bd (the code and its tests), c8d7b51a and d55774a2 (the behavior rows, the platform guide, the status moves),
8ff56453 (a fix of ADR-0345's code) and 426de2e6 (the developer guide). The nine commits before them are the accepted
documents of ADR-0355 to ADR-0357, judged already, and are not reviewed here. The reviewer did not write the work. Every
check below ran in a detached worktree of 426de2e6; none was read only.

The code does what the ADR decides. The two Majors are in the tests: one rule of Decision 4 has no test that sees it
break, and one new scenario fails under load.

### 🔴 Blockers

None.

### 🟡 Majors

**1. No test pins two of the post-hook gate's three terms, and the unit test for the gate bites on none** · attribution:
`model` · home: here.

The gate is `!busy && wrote == nil && !anyPending(vs) && !anyLagging(vs)` (`internal/app/hooks.go:242`). Each mutant
below ran as a `go test -overlay` against every test of `internal/app` and against the nine e2e scenarios:

- M12, without `!anyPending(vs)`: every test passes.
- M13, without `wrote == nil`: every test passes.
- M11, without `!anyLagging(vs)`: only `TestScenarioAppPostHookAfterStack` fails. `TestPostHookWaitsForEveryLaggingPart`
  passes.

Why the unit test misses all three:

- `h.step()` waits for a call that its pass started, and `h.noCall()` then counts calls from that moment
  (`internal/app/hooks_test.go:123-141`). A call that starts inside the step is never seen by the next `noCall`, and the
  final `require.Len(..., 1)` also passes when that one call came early (`internal/app/stacks_test.go:327-361`).
- The stack lags until the last step, so the Pending term and the wrote term are never tested alone.

The e2e scenario writes `FRP_TOKEN` before it approves bunny, so relay is Ready long before bunny is. A real approved run
writes the key while it applies the stack, and the stack turns Ready when the run ends, so the reader boots while the
stack turns Ready. The Pending term exists for that case. Checklist item 4 names it, and the spec's mutant `hook-before-ready` guards it (`PostHookAfterStacks`).
The rule is new with this ADR: before it, a post-hook started once the revision was current.

A scratch unit test, applied with `go test -overlay` and not committed, shows the fix. It checks that
`h.inv.received()` is empty after each step, and covers each term:

- no call while the reader lags;
- no call in the pass that writes the reader;
- no call while the reader is Pending and nothing lags;
- no call in a pass that writes back a ConfigMap deleted by hand while nothing is Pending or Lagging;
- one call in the next pass.

It passes on the branch, and M11, M12 and M13 each fail it at their own step.

Fix (builder): count the calls after each step in `TestPostHookWaitsForEveryLaggingPart` instead of using `noCall`, and
add the Pending and wrote cases. Optionally, make the e2e scenario approve bunny and write `FRP_TOKEN` in one step, with
a slow-booting relay.

**2. `TestScenarioAppPreHookIgnoresStack` fails under load: its pre-hook must cold-start within 500 ms** · attribution:
`model` · home: here.

- Three copies of the nine scenarios ran at once, with a load average of about 5 on 14 cores. One of the three failed
  this test after 21 s. The log read `pre-hook failed ... function default/migrate did not become ready within 500ms`,
  and the test's last children were `bunny Pending Progressing`, `migrate Ready` and `stack Pending Progressing`.
- The same failure came once more in the eleven e2e mutant runs whose mutant leaves the pre-hook path alone, with a load
  average of about 6. That run's mutant (M7b) only changes which writes count as self-heals, and a first install heals
  nothing, so the load caused it.
- Without other load, it passed 4 of 4 runs under `-race` (once alone, then with `-count=3`), in about 1.3 s.

The cause: migrate is scale-to-zero (`migrate.Scaling.MinReplicas = 0`, `pkg/funcd/app_stacks_e2e_test.go:412`), so the
hook call wakes it, and `stackPacing` gives a wake 500 ms (`:45-48`). The hooks suite gives a hook's wake 3 s
(`pkg/funcd/app_hooks_e2e_test.go:324`) or the default 30 s. CI runs the whole e2e suite in parallel (`just test-e2e
-v`), while the gate's stress run starts each test alone, so the gate would not catch this. The post-hook `warm` of
`TestScenarioAppPostHookAfterStack` sets no scaling, so it is scale-to-zero too. It has the same exposure but did not
fail here.

Fix (builder): keep both hook Functions warm (`minReplicas: 1`, as `readsOutputs` does), or give these two scenarios a
wake of a few seconds, as the hooks suite does. Both scenarios stay short.

### Minor

1. **Three changed behavior rows cite no test for their new clause** · `model` · `docs/behavior/apps.md:45`, `:48` and
   `:53`. The Post-hooks row, the deadline row and the dropped-part row each gained ADR-0357's clause and cite its
   Decision. d55774a2 put back their old tests (`TestScenarioAppPostHookFails`,
   `TestScenarioAppFailedUpgradeKeepsServing`, `TestScenarioAppStoreInUseKept`) to stay under the 400-byte cap.
   `TestScenarioAppPostHookAfterStack`, `TestScenarioAppLateReaderGetsFullTimeout` and
   `TestScenarioAppPruneWaitsForLagging` are now cited by no row. The registry splits a row that holds two facts
   (behavior-registry skill, "A row"). Fix: give each new clause its own row, citing its test.
2. **The reason check of the self-heal exception is not pinned** · `model` · `endsWait`,
   `internal/app/reconcile.go:630-634`.
   - M15, where any `Lagging` line ends a wait, passes every test.
   - A scratch test shows the case it guards: on the branch, a stack edited by hand while it lags `AwaitingApproval` is
     written back as a self-heal (ADR-0212 Decision 1). Under M15 it is not.
   - Fix: commit that case.
3. **The AddonRevision half of 8ff56453's condition is not pinned** · `model` · `internal/services/addon/objects.go:111`.
   - F2, which stores the pin only when `appRevision` moves, passes every test of `internal/services/addon`.
   - F1, which stores it only when `addonRevision` moves, fails `TestEnginelessPartPinFollowsItsApp`.
   - The AddonRevision moves alone under `funcdctl dev`, where every object follows its Addon's current revision
     (ADR-0339 Decision 7, `followCurrent`).
   - Fix: a case in which only the AddonRevision moves.

### Routed (not this work's)

- **`just ci-full`, the bloat audit, the stress run and the Linux tests** · elsewhere: `scripts/agent/gate.sh`, which
  runs once for the branch. The brief kept them out of this review.
- **The frp-bunny lane, named by the Done-when** (`e2e/addon-frp-bunny.venom.yml`, the App installed with the outputs
  Secret holding no key) · elsewhere: the session that runs it. Out of scope here. The `Implemented` stamp also waits for
  its result.

### The fix in 8ff56453 (the fix-review checks)

- **Is it a bug? Verdict: `bug`, a real defect.**
  - **It happens.** The package `internal/services/addon` is the same on origin/main and on the branch, apart from this
    commit. With main's `objects.go` overlaid, `TestEnginelessPartPinFollowsItsApp` fails at the moved pin: expected
    `edge-2`, actual `edge-1`.
  - **The user sees it.** A scratch e2e test ran on the branch with main's `objects.go`: a stack applied, then an
    upgrade that changes only `spec.version`. `edge-2` never switched, and the App logged
    `rollout failed ... reason="InfraStack/bunny: Progressing: runs the record of edge-1, not of edge-2"`. With the fix,
    `edge-2` switched about 0.15 s after `edge-1`.
  - **Nothing decided it.**
    - ADR-0339 Decision 3 moves an App's part to the record of the latest Applied revision.
    - ADR-0345 Decision 1 says an engineless object "pins its AddonRevision as an engine's object does".
    - ADR-0339 Decision 4 makes the App wait for that move, so a pin that never moves fails the revision.
    - The decided row "Which revision an object runs" in `docs/behavior/addons.md` says the same.
    - No issue (searched: engineless pin, appRevision engineless, the test name), no board card and no row accepts a pin
      left behind.
- **It fails without the fix and passes with it.** The revert fails as above. The test passes under `-race` with the
  fix.
- **It fixes the cause.** The pass moved the pin in memory but stored it only together with another status change.
  `writeObjectResult` (`objects.go:340`) reads the stored object and writes only the phase and the condition, so the
  moved pin was lost. The pass now stores a moved pin at once (`objects.go:111-115`).
- **Its scope is small.** It changes `objects.go` and its test only. `storeStatus` is the former tail of
  `objectStatus`, now shared, so nothing is duplicated.
- **No sibling has the same cause.** The engine path writes the whole status on every pass
  (`internal/services/addon/reconcile.go:146`, and its hold at `:396`). A settings object pins no revision:
  `settingsObject` never calls `pinned`.
- **The ADRs and the design model hold.** The fix conforms to ADR-0339 Decision 3 and ADR-0345 Decision 1, and edits no
  ADR. `docs/design/addon/revisions.fizz` is unchanged, and `just check-specs` passes.
- **The commit shape is right.** A `fix(addons):` subject and the trailer. There is no issue and no `Fixes #N`, by the
  decider's standing rule that a defect found on the way is fixed without an issue. The PR still needs the comment that
  rule asks for, naming the defect, its cause, the commit and the test.
- **The fix checklist:** 12 of 13 items hold. The miss is item 4, since mutant F2 survives (Minor 3). Items 1 and 11
  are read for a fix with no issue: its regression test is named for the behavior, not `TestIssue<N>_`, and its commit
  carries no `Fixes #N`.

### ✅ Verified correct (keep it)

- **The checks, through the pinned dev shell with `GOWORK=off`:**
  - `go build ./...` exited 0, on the host and with `GOOS=linux`.
  - `go vet` exited 0 on `api/types/v1alpha1`, `internal/app`, `internal/ownedstore` and `internal/services/addon`, on
    the host and on Linux, and on `pkg/funcd` with `-tags e2e`.
  - `golangci-lint` reported 0 issues on those four packages. With `--build-tags e2e`, it reported none in this work's
    files.
  - `go test -race` passed on those four packages and on the callers of `ownedstore.CheckSecrets` and of the child
    states: `internal/function` 12.9 s, `internal/workflow` 22.1 s, `internal/services/lake` 2.7 s and `cmd/funcdctl`
    10.9 s. `internal/app` took 19.1 s.
  - `just check-behavior check-hygiene` exited 0 (`hygiene: clean`).
  - `just check-specs`: 29 specs, 0 failing. `stack-rollout.fizz` passes with 356 states in 0.4 s, as the ADR records.
    Its 8 mutants each fail the assertion they name, and `stack-rollout.md` is current.
  - `just generate` exited 0, and `git status` was empty before and after.
- **The nine scenarios, under `-tags e2e -race`, all calling `t.Parallel()`:**
  - In one run, each passed in under 10 s: PreHookIgnoresStack 1.31 s, StackChangeKeepsAppliedState 1.96 s,
    PartWaitsForOutputs 3.02 s, DroppedStackDestroyed 3.28 s, PruneWaitsForLagging 3.29 s, OtherSecretStillStops
    4.04 s, StackApprovalNeverFails 5.06 s, PostHookAfterStack 5.50 s and LateReaderGetsFullTimeout 7.62 s, the last
    with the reason its comment gives.
  - Then 27 of 27 passed with `-count=3`. Under load, see Major 2.
  - No `-timeout` was raised.
- **The unit tests,** run by name: `TestEnginelessPartLags`, `TestPartWaitsForItsStacksOutputs`,
  `TestPreHookNeverWaitsForOutputs`, `TestOtherSecretStillStops` (6 cases), `TestLagEndRestartsTheDeadline`,
  `TestLagEndSurvivesACutPass`, `TestPostHookWaitsForEveryLaggingPart`, `TestPruneWaitsForLagging`,
  `TestPlanListsTheWaitingParts`, `TestSecretGaps`, `TestAppChildStateSchema` and `TestEnginelessPartPinFollowsItsApp`.
  All passed, and none is skipped. The only test edit is the enum test, which now also checks `Lagging`.
- **Mutants that a test kills.** Each was one edit, applied with `go test -overlay`; the e2e column ran the nine
  scenarios:

  | Mutant | Line | Unit tests that fail | Scenarios that fail |
  |---|---|---|---|
  | An engineless part counts as Pending (M1) | `status.go:79` | `TestEnginelessPartLags` and 2 more | 7 of 9 |
  | A reader is written before its key exists (M2) | `reconcile.go:466` | `TestPartWaitsForItsStacksOutputs` and 1 more | 3 |
  | Any owner's kind lets a Secret through (M3) | `reconcile.go:581` | `TestOtherSecretStillStops` | none |
  | A pre-hook Function waits for the key (M5) | `reconcile.go:614` | `TestPreHookNeverWaitsForOutputs` | PreHookIgnoresStack |
  | The owning Job waits for the key (M6) | `reconcile.go:618` | `TestPartWaitsForItsStacksOutputs` | 4 |
  | The write that ends a wait counts as a self-heal (M7b) | `reconcile.go:469` | `TestPartWaitsForItsStacksOutputs` | PartWaitsForOutputs |
  | `lagEndedAt` is never set (M8) | `reconcile.go:360` | both `TestLagEnd…` tests | LateReaderGetsFullTimeout |
  | `lagEndedAt` is set in any phase (M9) | `reconcile.go:360` | `TestLagEndRestartsTheDeadline` | — |
  | The deadline drops the lag term (M10) | `revision.go:327` | `TestLagEndRestartsTheDeadline` | LateReaderGetsFullTimeout |
  | Prune runs while a part lags (M14) | `reconcile.go:375` | `TestPruneWaitsForLagging` | PruneWaitsForLagging |
  | A gap carries no owner (M16) | `ownedstore.go:188` | `TestSecretGaps` and 6 more | — |
  | The fix reverted | `objects.go` | `TestEnginelessPartPinFollowsItsApp` | none: no scenario upgrades an applied stack unchanged |
  | The pin stored only when its AddonRevision moves (F1) | `objects.go:111` | `TestEnginelessPartPinFollowsItsApp` | — |

  The survivors are M11, M12 and M13 (Major 1), M15 (Minor 2) and F2 (Minor 3).
- **Decision 1** (`internal/app/status.go:76-84`): an engineless object that judges Pending becomes `Lagging` with its
  judgment's reason. That is the condition's reason at its generation, else `Progressing`. A Ready one still has its pin
  judged (ADR-0339 Decision 4). `readyCondition`, `appPhase`, the switch and the deadline read only Pending and
  NotStarted, so a `Lagging` part counts as settled. `funcdctl app deploy` prints any state with its reason.
- **Decision 2:**
  - `checkSecrets` (`reconcile.go:573-598`) lets through a Secret that exists, lacks a key and is owned by a Job that
    the App's `jobs` name, by name or by ref. It stops on the first other gap.
  - `waitingParts` reuses the walk of ADR-0213 Decision 7, now `namedSecrets` in `admission.go`. It skips the owning
    Job and the pre-hook Functions.
  - The waiting parts are filled in before a stop on another Secret returns, and `apply` skips them. `Applied` stays
    True, and `endsWait` keeps the write that ends a wait out of the self-heal record.
  - Two scratch tests show the rule for kinds beyond a Function. A Workflow image step and a second Job that read the
    Job's Secret lag and are not written, while `todo-tf` is, and both are written with no self-heal once the key
    exists. A post-hook Function that reads the outputs lags and is not called until the key exists and it is Ready.
- **Decision 3** (`reconcile.go:360-363`, `revision.go:325-333`):
  - `lagEndedAt` is set only while the stored latest revision is `Deploying`, by comparing with the stored children.
    It goes out in the same status write that drops the `Lagging` line.
  - The deadline takes it as one more start term, so an older value moves nothing.
  - `TestLagEndSurvivesACutPass` loses the App status write to a Conflict, and the next pass then sets the time.
- **Decision 4** (`hooks.go:242`, `reconcile.go:375`): the code holds the rule. Only the tests miss it (Major 1).
- **Decision 5** (`plan.go`): the plan ignores the Secrets let through and lists their readers as `create` or `update`,
  as any part.
- **The contracts** match the ADR: `AppChildLagging` in the enum, `AppStatus.LagEndedAt`, `SecretGap.Owner`,
  `SecretGaps` (`CheckSecrets` keeps its first-gap answer through one shared loop), `checkSecrets`' signature and
  `anyLagging`. The OpenAPI and the kinds' reference are regenerated.
- **The tree** follows the implementation plan. The extra files are explained: `admission.go` shares the walk, the
  developer guide is a doc follow, and 8ff56453 is a commit of its own. No new dependency, no production
  `time.Sleep`, no `//nolint` and no `panic`.
- **Tracking:**
  - Since acceptance (3901c5c3), the ADR changed only in its status line, now `Reviewing`.
  - F145 reads `reviewing`. The seven `Superseded in part by` back-links and the blueprint's mention were already in
    place.
  - The feature issue #1023 is open.
- **The guides** (`docs/guide/platform/workloads/app.md`, `docs/guide/develop/workloads/app/index.md`) say what the ADR
  decides.

### Definition of Done

13 of 15 items hold:
- 4 of the ADR's 6 review-checklist items;
- the behavior rows (Minor 1 noted);
- the checks of the touched packages, with the full suite left to the gate;
- 7 generic items: real behavior, the contracts, the tree, the conventions, no new dependency, no scope creep and
  tracking.

The two misses, both `model`:
- checklist item 1, since one scenario test fails under load (Major 2);
- checklist item 4, since its rule holds in the code but no test pins two of its three terms (Major 1).

Not run here, by the brief: `just ci-full`, the whole e2e suite, `scripts/agent/gate.sh` and the frp-bunny lane.

### Model scorecard

To record in the ledger commit: claude-opus-5-5 on ADR-0357 (implementation) → changes-requested, 0 blockers,
2 majors, 3 minors, 5 model-attributed, DoD 13/15.

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0357 --phase implementation --model claude-opus-5-5 \
  --verdict changes-requested \
  --blockers 0 --majors 2 --minors 3 \
  --model-attributed 5 \
  --dod-passed 13 --dod-total 15 \
  --report docs/reviews/adr-0357-implementation-claude-opus-5-5.md \
  --notes "changes-requested; code conforms to Decisions 1-5, nine scenarios pass under -race; 8ff56453's defect is real (bug: ADR-0339 D3 via ADR-0345 D1) and its fix sound; Major 1 (model): post-hook gate's Pending and wrote terms unpinned (M12, M13 survive every test; the unit test misses M11 too); Major 2 (model): TestScenarioAppPreHookIgnoresStack fails under load (500 ms cold-start wake); Minors (model): 3 behavior rows cite no test for their new clause; endsWait's reason check unpinned (M15); the fix's AddonRevision term unpinned (F2); routed: ci-full to the gate, the frp-bunny lane to its session"
```

### Recommendation

The work goes back to the builder (`adr-impl`). The ADR stays `Reviewing` and F145 `reviewing`, and nothing is
stamped. The builder should:
- fix the post-hook tests and the hook Functions' wake (the two Majors);
- fix the three Minors.

The next round reruns M11 to M13, M15 and F2, and the three-copy load run of the nine scenarios. When the PR opens, it
carries `Fixes #1023` and the comment recording 8ff56453's defect.
