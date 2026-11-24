## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0357 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the branch `feat/infrastack-apps` at 8686bc1d, rebased onto origin/main 87f928c3. Round 1's report is
`docs/reviews/adr-0357-implementation-claude-opus-5-5.md`.

- `git range-diff` shows that the five commits of round 1 are unchanged: bdd4e5aa, 9b7933fd, 62e150af (the add-on fix,
  formerly 8ff56453), adf5018b and ac7027dd. The nine accepted ADR commits under them are unchanged too.
- The six new commits change only tests and docs: 3394cb60, 0dde0108, f54c78bf, 2bbbf561, 8a53a233 and 8686bc1d.
  `internal/app`, `internal/ownedstore`, `internal/services/addon` and `api/types` hold the same code as in round 1.

Every check below ran at 8686bc1d in a detached worktree; none was read only.

### Round 1's findings: all closed

- **Major 1, the post-hook gate's terms: closed.**
  - `TestPostHookWaitsForEveryLaggingPart` now counts the calls right after each pass, and holds each term alone once:
    a part lagging, a reader Pending while nothing lags, and a pass that writes back a ConfigMap deleted by hand.
  - M11, M12 and M13 each fail it.
  - `TestScenarioAppPostHookAfterStack` gained the order a real run gives (subtest "the approved run writes the key"):
    the key is written as the stack is approved, and relay's boot is held by a file until after bunny is Ready. M12
    fails it with "warm is not called while relay boots". M11 fails both subtests.
- **Major 2, the pre-hook's cold start: closed.**
  - migrate and warm keep a replica, and their scenarios run on `hookPacing`: a boot of 4 s and a wake of 3 s, as the
    hooks suite paces them (`pkg/funcd/app_stacks_e2e_test.go:54`).
  - `TestScenarioAppPreHookIgnoresStack` passed 20 of 20 runs: 4 quiet, 6 with three copies of the scenarios running
    at once, and 10 with five copies. Round 1 saw it fail 1 of 3 runs with three copies.
- **Minor 1, the rows: closed.** Each clause that ADR-0357 adds has its own row, citing its scenario
  (`docs/behavior/apps.md`). The rows it changed read as they did before it. `just check-behavior` passes, and the
  written part is 16348 bytes, under the cap of 16384.
- **Minor 2, the reason check of `endsWait`: closed.** `TestHandEditOfALaggingStackIsASelfHeal` holds it, and M15
  fails it.
- **Minor 3, the AddonRevision half of the add-on fix: closed.** `TestEnginelessPinFollowsTheCurrentRevisionUnderDev`
  holds it, and F2 fails it.

### What the implementer reported

- **8a53a233's race (a read can show a write before the store's watch delivers it). Verdict: `accepted`, the store's
  decided design, so no store issue.**
  - ADR-0006 chose an in-process channel stream for the watch.
  - `publish` runs after the engine's transaction commits and hands the event to each watcher's buffered channel
    (`internal/store/watch.go`). So a `Get` can see a write before a watcher's goroutine has received its event.
  - The old checks read the watch right after a read-based wait, and assumed the event had arrived.
  - `seenRV` now waits until the watch has recorded the write. The checks stay as strong: the watch records a prefix
    of the store's order, so the first match is the true first. M14 still fails the prune scenario, and M5 the
    pre-hook scenario.
  - The stack-change scenario now also waits for the switch's write before it reads every App write, so it checks more
    of them than before.
- **The harness's `infrastack-reconciler` went `Failed` (`ShapeInvalid`) at its first boot, once. Verdict: `accepted`
  for funcd, decided behavior, so no issue and no platform fix.**
  - ADR-0161 Decision 2 makes a first boot a shape failure when a listening replica is not ready `runtime.bootTimeout`
    after its start, with no retry. The decided row "A worker listens, never ready" in `docs/behavior/workers.md` says
    the same. The code is `internal/function/function.go:2975`.
  - A readiness read that runs past its probe timeout gives a replica with no earlier answer no answer at all
    (`readReadiness`, `internal/function/readiness.go:84-95`; ADR-0259 Decision 1).
  - So a loaded host can fail a 1 s first boot for good. Six scenarios boot the reconciler under `stackPacing`'s 1 s.
  - Not reproduced here: 0 failures in 144 loaded runs of those six scenarios (with three, five and eight copies at
    once) and in every quiet run.
  - What did show: with five copies, the reconciler's first objects call once failed with `function
    default/infrastack-reconciler did not become ready within 500ms`, and the retry then served it. Its first boot
    thus runs near the 1 s limit under load. This is the Minor below.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **`stackPacing`'s 1 s boot limit can fail the harness's own reconciler for good under load** · `model` · here ·
   `pkg/funcd/app_stacks_e2e_test.go:46-49`.
   - Five scenarios start on `stackPacing()`, and `TestScenarioAppOtherSecretStillStops` on a copy of it (`:638`).
     So `infrastack-reconciler` (`:102-107`, one replica) boots under a 1 s `runtime.bootTimeout` in six scenarios.
   - Under load, a late first readiness read past that second fails it `ShapeInvalid` with no retry, as above. Every
     stack call then fails, and the scenario times out.
   - It was seen once by the implementer and not here.
   - Fix: give `stackPacing` a longer boot. `app.upgradeTimeout` 3 s > `runtime.bootTimeout` 2 s > activation 500 ms
     keeps ADR-0200 Decision 10's order with no longer scenario; `hookPacing`'s values also work.
   - The suites `requiresPacing` and `secretPacing` on main use the same 1 s boot, so the exposure is not new to the
     suite. This scenario set adds one more Function that boots under it.

### Routed (not this work's)

- **`just ci-full`, the bloat audit, the stress run and the Linux tests** · elsewhere: `scripts/agent/gate.sh`, once
  for the branch, as the brief says.
- **The frp-bunny lane, named by the Done-when** (`e2e/addon-frp-bunny.venom.yml`, the App installed with the outputs
  Secret holding no key) · elsewhere: the session that runs it. The stamp below rests on that run and on the gate.

### The add-on fix 62e150af (formerly 8ff56453): the fix-review checks

- **The commit is unchanged** (`git range-diff` shows `=`). Round 1's verdict stands: `bug`, a real defect, against
  ADR-0339 Decision 3 as ADR-0345 Decision 1 applies it, with the cause fixed.
- **It fails without the fix and passes with it.** `objects.go` on origin/main 87f928c3 is the pre-fix file. With it
  overlaid:
  - `TestEnginelessPartPinFollowsItsApp` fails: expected `edge-2`, actual `edge-1`;
  - `TestEnginelessPinFollowsTheCurrentRevisionUnderDev` fails: expected `infrastack-2`, actual `infrastack-1`.

  With the fix, both pass under `-race`.
- **The mutants are killed:** F1 fails the first test and F2 the second.
- **The fix checklist:** 13 of 13 items hold. Items 1 and 11 are read for a fix with no issue: the test is named for
  the behavior, and the commit carries no `Fixes #N`.

### ✅ Verified correct (keep it)

- **The checks, through the pinned dev shell with `GOWORK=off`:**
  - `go build ./...` exited 0.
  - `go vet` exited 0 on the four packages and on `pkg/funcd` with `-tags e2e`.
  - `golangci-lint` reported 0 issues on the four packages, and none in this work's e2e file with `--build-tags e2e`.
  - `go test -race` passed: `internal/app` 19.0 s, `internal/services/addon` 2.7 s, `internal/ownedstore` and
    `api/types/v1alpha1`.
  - `just check-behavior check-hygiene` exited 0 (`hygiene: clean`).
  - `just check-specs`: 29 specs, 0 failing. `stack-rollout.fizz` passes with 356 states, reused from a cache keyed by
    the unchanged spec and script. Its 8 mutants fail as named, and the diagram is current.
  - The six oracle markers sit on the tests the ADR names.
  - `just generate` exited 0 with the tree unchanged.
- **The scenarios, under `-race`:**
  - Quiet, all passed: PreHookIgnoresStack 1.47 s, StackChangeKeepsAppliedState 2.38 s, PartWaitsForOutputs 3.30 s,
    PruneWaitsForLagging 3.37 s, DroppedStackDestroyed 3.38 s, OtherSecretStillStops 4.37 s, StackApprovalNeverFails
    5.25 s, PostHookAfterStack 3.90 s and 5.58 s for its two subtests, and LateReaderGetsFullTimeout 8.06 s.
  - Then all passed with `-count=3`.
  - Under load, all passed, in 192 runs: 6 copies of the nine run three at a time, 10 copies run five at a time, and 8
    copies of the six `stackPacing` scenarios run at once. No data race was reported, and no `-timeout` was raised.
- **Every unit mutant is killed**, each one edit applied with `go test -overlay`, against all of `internal/app`,
  `internal/ownedstore` and `internal/services/addon`: M1, M2, M3, M5, M6, M7b, M8, M9, M10, M11, M12, M13, M14, M15,
  M16, F1 and F2. Round 1's survivors die as follows:

  | Mutant | Killed by |
  |---|---|
  | M11, M12, M13 | `TestPostHookWaitsForEveryLaggingPart` |
  | M15 | `TestHandEditOfALaggingStackIsASelfHeal` |
  | F2 | `TestEnginelessPinFollowsTheCurrentRevisionUnderDev` |

  In the scenarios: M11 and M12 fail `TestScenarioAppPostHookAfterStack`, M14 `TestScenarioAppPruneWaitsForLagging`, M5
  `TestScenarioAppPreHookIgnoresStack`, and M2 three scenarios. M13 survives the scenarios only, since a written part
  is Pending in the pass that writes it; the unit test covers that case.
- **The new tests**:
  - The unit test makes each post-hook term the only one that holds the call. The ConfigMap step isolates "wrote no
    part", since a status-less part is Ready once it exists.
  - The e2e subtest's held boot cannot fail relay at its first boot. The Node shim imports the handler before it
    listens, so a held import is a worker not listening yet, which the boot crash rule retries, and the 8 s boot limit
    outlasts the hold. 8686bc1d's comment says so.
  - `TestEnginelessPinFollowsTheCurrentRevisionUnderDev` builds the reconciler as `funcdctl dev` does
    (`FollowCurrent`), and moves only the AddonRevision.
- **Tracking**: since acceptance (415a3789), the ADR changed only in its status line. F145 read `reviewing` before the
  stamp. The feature issue #1023 is open.
- **The commits**: conventional subjects and the trailer on each.

### Definition of Done

15 of 15 items hold:
- the ADR's 6 review-checklist items;
- the behavior rows;
- the checks of the touched packages;
- 7 generic items: real behavior, the contracts, the tree, the conventions, no new dependency, no scope creep and
  tracking.

Not run here, by the brief: `just ci-full`, the whole e2e suite, `scripts/agent/gate.sh` and the frp-bunny lane.

### Status advanced

- ADR-0357: `Reviewing → Implemented (2026-11-24)`, linking this report. This is the only edit to the ADR.
- `docs/feat/0012-feat-infrastack.md` F145: `reviewing → implemented`.
- One commit on 8686bc1d in the review checkout, not pushed. The PR that merges the branch carries `Fixes #1023`, and
  the comment recording the defect fixed in 62e150af. This report goes to `docs/reviews/` with the ledger row, where
  the stamp's link points.

### Model scorecard

To record in the ledger commit: claude-opus-5-5 on ADR-0357 (implementation, round 2) → pass, 0 blockers, 0 majors,
1 minor, 1 model-attributed, DoD 15/15.

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0357 --phase implementation --model claude-opus-5-5 \
  --verdict pass \
  --blockers 0 --majors 0 --minors 1 \
  --model-attributed 1 \
  --dod-passed 15 --dod-total 15 \
  --report docs/reviews/adr-0357-implementation-claude-opus-5-5-2.md \
  --notes "pass in round 2; round 1's 2 Majors and 3 Minors closed: each post-hook gate term pinned (M11-M13 killed by unit tests, M11-M12 by the e2e), hook Functions keep a replica on hookPacing (0 failures in 20 runs, 16 loaded), rows split, M15 and F2 killed; 8a53a233's watch race is the decided store design (ADR-0006), and seenRV keeps the order checks; the reported ShapeInvalid of the harness's reconciler is decided first-boot behavior (ADR-0161 D2), not reproduced in 144 loaded runs; Minor (model): stackPacing's 1 s boot limit can fail that reconciler for good under load; routed: ci-full to the gate, the frp-bunny lane to its session"
```
