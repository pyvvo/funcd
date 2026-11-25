## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0358 implementation, round 2, model: claude-opus-5-5)

This report reviews the branch at dcc4f48c, one commit above ce68555f, which round 1 reviewed. The reviewer did not write
the work. Every check below ran in a detached worktree of dcc4f48c; scratch tests and mutants went in with
`go test -overlay`, and none is committed.

Round 1's four Majors and seven Minors are closed, each by a rerun. The commit's two departures from the fixes that round 1
proposed are right. The commit itself brings three Minors.

### Round 1's findings

| Finding | Now | Evidence at dcc4f48c |
|---|---|---|
| Major 1, the Conflict and the current revision | closed | the reproductions pass, 5 of 5 as a unit test and 5 of 5 on a platform; a Conflict that counts as stored, and a pass that goes on after it, die by `TestAddonWrittenDuringAReadKeepsItsCurrentRevision` |
| Major 2, the first call of a created Function | closed | the three cases are refused and the engine or the Job gets nothing, 6 of 6 each, and so is the sleeping Function of `checked`, 6 of 6 |
| Major 3, twelve rules with no test | closed | all twelve mutants die, each by a test that reaches its line |
| Major 4, the flake | closed | `TestScenarioAddonContractOfThePin` passed 360 of 360 in six copies, and 1,200 of 1,200 in twelve at a load average of about 20 |
| Minor 1, a contract that says nothing | closed | its rules judge each write, 400; both mutants die |
| Minor 2, call state never dropped | closed | it goes with the object (mutant dies); how it goes is Minor 1 below |
| Minor 3, the Platform page | closed | no sentence says that a pass waits on no registry |
| Minor 4, ADR-0341's two lines | closed | both are edited and marked |
| Minor 5, the dev scenario's test | closed | it registers a settings kind on a Docker engine; the former test is `TestEnginelessFunctionDev` |
| Minor 6, fifteen unpinned lines | closed | 20 mutants of them die |
| Minor 7, two rows' citations | closed | the rows cite tests that hold their clauses |

### The two departures

1. **`checked`, not "it has a revision"** (`internal/services/addon/settings.go:379`): right, and round 1's proposal was
   not. With `answering(fn) != ""` in its place, `TestScenarioAddonConfigNoLongerFitsASleepingFunction` fails 4 of 4 and
   the scratch test of the same case 4 of 4: the call wakes the new image's code with a list that its contract refuses.
   Each of its two terms is pinned: without the serving one or the stamped one, a unit test fails. It leaves an object
   waiting in no case found: the Function's pass stamps the revision before every gate, and a failed pass stores it, so a
   bundle that cannot be pulled and a handler that is not exported read `ReconcileFailed` with their cause within 0.3 s on
   a running platform. By reading, only `RevisionStampFailed` keeps the call back, which the API cannot bring about.
   `FunctionRevisionName` is one definition over `revisionLabel`, and `internal/function` holds no second copy.
2. **A Conflict on the AddonRevision's status fails the pass**: right. At ce68555f the pass ended with no error, and no event
   of a revision brings the Addon's pass, so an image could stay unread. The mutant that swallows it and the one that ends
   the pass quietly both die. `retire` still skips a Conflict of an older revision, and requeues after the supervision
   period, which is enough.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **`forgetTarget` drops the state of a call that still runs, so a group can have two calls at once** · `model` ·
   `internal/services/addon/settings.go:200-204`. ADR-0345's checklist says "one call at most runs per group", and so does
   the row "An engineless kind's call"; at ce68555f it held. The loop matches a group by name, whoever created its Function.
   - Unit test, with a created Function: a stack deleted and created again while its call runs has two calls in flight.
     With the loop removed, as at ce68555f, one (3 of 3).
   - On a real Function the deletion cuts the first call before the second starts: no overlap in 3 runs, nor in 12 quick
     rounds of delete and apply.
   - When the stack names its own Function and that Function has the name funcd would give (`bunny-edge-infrastack`),
     nothing cuts it. The second call started at 0.96 s while the first ran until 3.8 s, 2 of 2. The first one's end then
     marks the second as ended, and a third call starts beside it.
   - The effect is bounded: an answer is written at the uid and generation sent, and the next call or resync applies the
     current list. So it is a Minor. Issue and board searches for it: not done.

   Fix (builder): skip a group whose call is in flight (`&& !r.groups[k].inflight`). The call's end requeues its objects,
   and the gone one's pass then drops the state. Tried as an overlay: the unit and the own-Function probes pass, all unit
   tests of the package pass, and an object created again after that end starts with cause `start`.
2. **`settings.fizz` lacks the step that the gate adds** · `model` · `docs/design/addon/settings.fizz`. Its `Pass` creates
   the Function and sends the call in one step. The code now creates it, waits for the stamp, and then calls.
   - A unit test shows what opens: a policy created and deleted before the stamp gets, as the Function's first call ever,
     `start` with no object. Then the Function goes.
   - In a scratch copy of the spec with creation, stamp and call as three steps, `NoObjectNoCall` fails: Write, Create,
     Delete, Stamp, Pass. The user-visible end is what `addon-function-on-demand` says ("created and deleted": the default
     is back and the Function goes), so no behavior needs to change.
   - With `asked` set when an object brings the Function, that copy passes (27,410 states) and its ten mutants fail as named.
   - ADR-0272 Decision 5 and `.claude/skills/fix/SKILL.md` ask that a fix of a modeled behavior update the spec in the same
     commit. `just check-specs` passes today because the spec was left as it was.

   Fix (builder): add the step and that line, then `just spec-diagrams`.
3. **Three lines of the commit have no test** · `model`.
   - `k.ns == ns` in the new loop: without it, removing `bunny-edge` in one namespace drops the calls known for a
     `bunny-edge` in another.
   - The Function's name in the same condition: without it, removing one object drops every group of its namespace.
   - The `continue` of the settings gate (`settings.go:323`): a `return` there holds back the target's other kinds and the
     prune.

   Each mutant passes the unit tests, the platform tests and the e2e tests. Fix (builder): a removal that leaves another
   object's calls known, and a target with two kinds of which one waits.

### Routed (not this work's)

- **The settings half of the same loop** · elsewhere: ADR-0341's code, unchanged by this commit. A target deleted and
  created again while its `ref`'s call runs has two calls at once in a unit test (2 of 2), against ADR-0341's "one call at
  most runs per target and kind". On a platform: not checked. The guard of Minor 1 would close it too.
- **ADR-0303's accepted window** · no finding. In 20 random runs with two contracts, five calls reached newer code that
  refuses them. Each timeline is consistent with a call that was checked while the older revision woke and landed after the
  image changed, which ADR-0303's Risks accept. The gate neither closes nor widens it.
- **Carried over from round 1, unchanged and not rechecked**: the decider's recorded go for ADR-0341's in-place amendment;
  a settings kind's `target` changed over two replaces (`adr`); `ContractOf` with no revision for callers other than the
  add-ons; the guide's funcd-addons version; the PR comment for the App with a settings part.
- **`just ci-full`, the stress run, the Linux tests and the lanes** · elsewhere: `scripts/agent/gate.sh` and the author, by
  the brief.

### ✅ Verified correct (keep it)

- **The checks.**
  - `go build ./...` exited 0 on the host and with `GOOS=linux`; `go vet` exited 0 on `./...`, on `./pkg/funcd` with
    `-tags e2e` and on `./cmd/funcdctl` with `-tags dev`.
  - `golangci-lint`: 0 issues on the touched packages and with `dev`; with `e2e`, the two `gochecknoglobals` of round 1, in
    files this work does not touch.
  - `go test -race`: `internal/services/addon` (2.6 s, and 30 times over), `internal/ownedstore`, `internal/gc`,
    `internal/function`, `internal/controlplane`, `internal/app`, `internal/workflow`, `api/…` and `pkg/funcd` (183.0 s), all
    ok. Without race: `cmd/…`, `scripts/…` and the whole of `./cmd/funcdctl` with `-tags dev` (241.5 s).
  - `just check-specs`: 29 specs, 0 failing. `just check-behavior` and `check-hygiene` exited 0. `just generate` changed no
    file. The bloat audit has 0 hard flags, on the branch and on the commit.
- **The tests see their rules break.** Of 54 mutants, 49 die: the twelve of Major 3, 20 for Minor 6 and 17 of the commit's
  own 22, among them both gates, the gate on a `ref`, the groups never dropped, and both Conflicts. Three of the five
  survivors are Minor 3; the other two change nothing a rule names.
  - Without the gate the e2e tests fail: the first call of a policy 6 of 6, of a stack 6 of 6, the sleeping Function 6 of
    6, and the re-created Function 25 of 30. The last one's 500 ms window can end before a slow cold start; the unit test
    `TestAddonFunctionIsCalledOnceItsRevisionIsStamped` fails each time.
- **Load.** The ten e2e tests that `OnARealFunction|NoLongerFits|AppWithASettingsPart|MeetsItsContract` names passed 3 of 3
  under `-race` (0.9 to 3.3 s each, on a loaded host), and 16 of 16 in four copies at a load average of 25 to 30 beside the
  seven new platform tests, with no data race.
- **Lifecycle.** Random writes on real Functions, 8 seeds of 160 each with a check after every 40: 32 of 32 matched what was
  stored. An App's stack from an image, deleted and dropped with its Job: 12 of 12, its destroy run Succeeded. A `ref`'s
  Function applied after its object: its first call is checked, 3 of 3.
- **One call per creation.** The second call at each creation is gone: one `start` in both real-Function scenarios.
- **Tracking.** The commit touches neither ADR-0358 nor the feat doc. Since 7c16825a, ADR-0358 changed only in its Status
  line and the issue's number, and F275 reads `reviewing`. The PR closes #1576.

**Not checked**: a stack deleted and created again while its first call still wakes its Function (the guard of Minor 1 closes it either way); whether the platform test of the Conflict alone fails without its fix; whether `TestScenarioAddonFunctionDev` ran or skipped in the non-verbose dev run; why a call to a Function deleted by hand during its wake waits out `addons.settingsTimeout` (30 s), which is not this commit's; and what the brief leaves to the author.

### Definition of Done

18 of 18 items hold by their own text: the 6 review-checklist items, the design model (`just check-specs` passes and each
assertion has its marker on a passing test; Minor 2 is ADR-0272's rule for a fix), the behavior rows, and the 10 generic
items.

| Review checklist item | Holds |
|---|---|
| No field of an Addon describes an object that only a Function reads | yes |
| No write and no object's pass calls a registry; an engine's start waits for no read to succeed | yes |
| A list, its constraints and its call come from one revision, the target's | yes: the first call is gated, and the constraints and the resync of the pin have tests |
| A created Function has its object's marker and bindings and no address; another's is never written | yes: the user's Function and the `ref`'s have tests |
| No Function and no call for a target that no object of the kind names, once its engine's default is back | yes |
| An App drops an add-on part whose kind has an image | yes |

Not run here, by the brief: `just ci-full`, the whole e2e suite, `scripts/agent/gate.sh` and the lanes.

### Counts

Blockers 0, majors 0, minors 3; all 3 are the model's. Review checklist: 6 of 6.

### Model scorecard

To record in the ledger commit: claude-opus-5-5 on ADR-0358 (implementation, round 2) → pass, 0 blockers, 0 majors, 3 minors,
3 model-attributed, DoD 18/18.

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0358 --phase implementation --model claude-opus-5-5 \
  --verdict pass \
  --blockers 0 --majors 0 --minors 3 \
  --model-attributed 3 \
  --dod-passed 18 --dod-total 18 \
  --report docs/reviews/adr-0358-implementation-claude-opus-5-5-2.md \
  --notes "pass (round 2, dcc4f48c); round 1's 4 Majors and 7 Minors closed by reruns, 49 of 54 mutants die, the flake 1560 of 1560; the two departures from round 1's fixes are right (checked holds the sleeping Function, a Conflict on the revision is retried); Minor 1 (model): forgetTarget drops a running group's state, so a stack re-created during its call can have two calls with an own Function of funcd's name (ADR-0345); Minor 2 (model): settings.fizz lacks the stamp, with which NoObjectNoCall fails; Minor 3 (model): three lines unpinned; routed: the same in ADR-0341's target state, ADR-0303's accepted window, round 1's list"
```

### Recommendation

The work passes. The gate stamps ADR-0358 `Reviewing → Implemented` and F275 `implemented`, and the PR carries `Fixes #1576`.
The three Minors are small and belong in the same PR: one condition, one step of the spec and its diagram, and three tests.
They need no third round; the author's gate and CI run on the result.
