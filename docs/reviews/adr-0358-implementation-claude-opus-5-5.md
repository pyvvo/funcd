## Verdict: changes requested — 0 blockers, 4 majors, 7 minors (ADR-0358 implementation, model: claude-opus-5-5)

This report reviews the branch at ce68555f: three commits (37f8630d the code and its tests, 1387a393 the e2e scenarios
split one test each, ce68555f ADR-0341's amendment) on the accepted ADR, 7c16825a. The reviewer did not write the work.
Every check below ran in a detached worktree of ce68555f; scratch tests and mutants went in with `go test -overlay`, and
none is committed.

The design is built as the ADR decides, and most of it holds under mutants, races and real Functions. Two defects break
decided rules: the Addon's pass can remove the current revision, and the first call to a Function that funcd creates
passes unchecked. The other two Majors are in the tests.

### 🔴 Blockers

None.

### 🟡 Majors

**1. A Conflict on the Addon's status no longer ends its pass, so the pass removes the revision that the stored status
still names current** · attribution: `model` · home: here.

- `report` swallows the Conflict (`internal/services/addon/revisions.go:99`), and `Reconcile` goes on to `retire` with
  the new name as current (`:73`, `:76`). At 7c16825a the pass returned there: `return controller.Result{}, nil // the
  Addon changed: its event brings the next pass`.
- The window is new too. The pass now reads images, for up to 10 s, between its read of the Addon and that write, so a
  second write by the admin lands in it easily.
- **It happens.** A unit test whose `ImageReader` writes the Addon, and a platform test (3 of 3 runs): `rauthy-1` is
  current and unused, an admin applies generation 2, whose pass waits on a slow registry, a developer creates `login`,
  and the admin applies generation 3. The log:

  ```text
  removed an unused revision addon=rauthy revision=rauthy-1
  current rauthy-3; login pins "rauthy-1", phase Pending, reason "AddonRevisionNotFound": the AddonRevision rauthy-1 it pins is gone
  ```

  `login` stays so after the Addon's next passes, until someone edits it. By reading, an App stamped in that window
  records the removed revision (`internal/app/revision.go:127-128`), so its part cannot start either.
- **Nothing decided it.** ADR-0339 Decision 6 and its checklist ("no pinned, recorded or current revision is ever
  removed"), the model's `PinsExist`, and the row "An unused revision" (`docs/behavior/addons.md:43`, "the current one
  stays") say the opposite. No issue and no card holds it.

Fix (builder): end the pass at that Conflict, as before. With that edit alone, both scratch tests pass (the platform one
3 of 3). Pin it with a test whose `ImageReader` writes the Addon during the read.

**2. The first call to a Function that funcd just created is checked against no contract** · attribution: `model` ·
home: here.

- The pass creates the Function and starts its call at once (`internal/services/addon/settings.go:261`, `:316-323`;
  `objects.go:150`, `:211-220`). That Function has no revision yet, so `ContractOf` returns none
  (`internal/function/contracts.go:157`), the input plugin checks nothing, the wake waits for the worker, and the handler
  runs with whatever the object holds. The same list is sent again, cause `change`, once the revision is stamped, and only
  that call is refused.
- **It happens**, 6 runs of 6 for each case, on real Functions (process driver, Node shim):

  | Case | What ADR-0358 says | What happened |
  |---|---|---|
  | A policy with `lengthMin: "eight"`, stored before its image was read | its call meets the contract "at once" (Consequences) | the engine got `{"provider":"login","lengthMin":"eight","lengthMax":64}`, then `ReconcileFailed` |
  | A stack with a property off its contract, stored before its image was read | the same | its Function ran and started the JobRun `bunny-edge-1`, then `ReconcileFailed` |
  | A policy that the pinned contract refuses, whose Function is deleted by hand and comes back | refused before the Function runs, and the engine keeps its policy (Decision 5) | the engine got the refused policy |

  The same follows when `spec.functions` is taken off, or a kind moves from a `ref` to an image, since no contract judged
  those objects' writes.
- **Nothing decided it.** Decision 5: such a list "is refused before the Function runs". ADR-0303's Risks accept the
  revision-switch window only, and `docs/behavior/calls.md:53` says the input is refused before any wake. No issue and no
  card holds it. No test of this work sees it: the image-unread scenarios run no Function, and the e2e ones start from
  objects that fit.
- A `ref`'s Function applied after its object is checked at its first call (3 of 3): its own pass stamps the revision
  first.

Fix (builder): call a Function only once it has a revision (`answering(fn) != ""`); its stamp's event brings the next
pass. Tried as an overlay in `syncSettings` and `syncGroup`: the three cases are refused, the new e2e tests pass, and the
second call at each creation goes. Pin it with the first two cases.

**3. Twelve decided rules have no test that sees them break, among them the ones that keep a user's Function** ·
attribution: `model` · home: here.

Each line below is one edit, run against the tests of `internal/services/addon`, `internal/controlplane`, `internal/gc`,
`internal/app`, `cmd/funcdctl` and the add-on tests of `pkg/funcd` (`-run 'Addon|Settings|Engineless|Infra|Stack'`), and
F8, S9 and O2 against the new e2e tests too. Every committed test passes. The code itself is right: a scratch test of each
passes on the branch and fails on its mutant.

| Mutant | Line | The rule that no test holds |
|---|---|---|
| F8 the marker is not asked | `functions.go:161` | Decision 2, checklist 4: with no object of the kind, a user's Function of that name is left as it is. Under the mutant funcd calls it, then deletes it |
| S9 `created &&` dropped | `settings.go:309` | Decision 1: a `ref`'s Function is not funcd's. Under the mutant funcd deletes it once its empty list is applied |
| C13 constraints of the Addon as it is now | `internal/controlplane/addon.go:379` | Decision 5, checklist 3: the constraints are the pinned revision's |
| S27 resync of the Addon as it is now | `settings.go:316` | Decision 5: so is the resync |
| C2 the pin alone judges a write | `addon.go:683` | Decision 5: while the pinned revision lacks the kind, the current one judges |
| C18, C19 an App's settings or engineless part is not judged | `addon.go:324-339` | Decision 5: an App's 400, naming the property |
| C4 an engineless object's own Function is judged | `addon.go:672` | Decision 5: such a write is checked as a `ref`'s |
| C12 a schema that does not compile is used | `addon.go:732` | Decision 4: it checks nothing at a write (under the mutant, 500) |
| R1 the revision is current before its images are read | `revisions.go:69-73` | Decision 4: a deploy's App is checked against them |
| O2 funcd's own Function stays beside an engineless object's own | `objects.go:154` | Decision 2 |
| V1 AddonRevisions are not watched | `pkg/funcd/addon_provider.go:260` | Decision 4: an object that pins an older revision, read late, goes on. Under the mutant a stack waits for ever |

Fix (builder): one test each. F8 and S9 are a few passes of an existing rig.

**4. `TestScenarioAddonContractOfThePin` fails at random under load** · attribution: `model` · home: here.

- It reads the App, sets that `resourceVersion` on its own copy, and applies it once (`pkg/funcd/
  addon_kinds_internal_test.go:186-190`), while the App reconciler still writes the App's status. It is the one test of
  the package that applies an App that way.
- It failed 2 of 40 runs at a load average of 15, 1 of 360 in six copies at 20, and in two mutant runs whose mutant
  cannot reach it: `App default/web-auth has changed since resourceVersion …` and `store.Update: App "web-auth"
  resourceVersion mismatch`. Without load, 100 of 100 passed, so the gate's stress run would not see it.

Fix (builder): apply the App without a `resourceVersion`, as `TestScenarioAddonPartWithFunction` does.

### Minor

1. **A contract that says nothing lets no rule judge a write** · `model` · `settings_kinds.go:161`. With `{}`, an
   Addon whose rule is `${{ others.length < 1 }}` is `Ready=True` and the rule never runs. One whose rule names a field is
   `SettingsInvalid`, but the write is stored where the scenario `addon-constraint-unfit` says 400. Decision 5 spares the
   rules of a contract that is not read, not of one that says nothing. Fix: tell "not read" from "says nothing".
2. **An engineless object's call state is never dropped** · `model` · `r.groups`, made at `objects.go:275`. A group is now
   one object, so each stack ever created leaves an entry until funcd stops, and `MapJobRun` walks them all at each run's
   end. A settings target's state goes with it (`forgetTarget`). Fix: drop the groups of a removed object's created
   Function.
3. **The Platform page says "no write and no pass waits on a registry"** · `model` ·
   `docs/guide/platform/services/addons.md:72-73`. The Addon's pass does, on the controller's one worker. Measured with a
   registry that accepts and never answers: the Addon's write took 3 ms, and two engines turned Ready after 10.1 s and
   10.0 s, as the ADR's Consequences say.
4. **ADR-0341's amendment leaves two lines with the former rule** · `model` · the scenario `settings-function-change`
   (`:63`, the next pass sends the list when the Function gets a new revision) and the Contracts row (`:258`, its current
   revision). Earlier amendments edit and mark each such line.
5. **`TestScenarioAddonFunctionDev` is not its scenario** · `model` · `cmd/funcdctl/dev_addon_scenarios_test.go:271`. The
   scenario registers rauthy's Addon and `strict` turns Ready; the test runs an engineless kind. The path works: a scratch
   test with an engine on Docker, a settings kind and `self` turned `strict` Ready in 1.7 s, and removed the Function with
   the last object.
6. **Fifteen more lines have no test** · `model`: the target that the contract must require as a string (K1, K5:
   `settings_kinds.go:101`; `settings-kind-refused` lost its case "target not required" with no replacement); one unread
   image that must not drop another that was read (F24); a union's `const` tag, the form ADR-0058 names, and its
   discriminator (F15, F14); the 63-character name (T10); `bindings.job`'s checks on an object (T6); a kind that the Addon
   dropped (S5); `Progressing` before the first revision (S13); a Function's event mapped by the pin (S17); the end of an
   `ArtifactNotReady` or `FunctionNotOwned` wait (S19); no call while the new pin's image is unread (S25); the Addon's
   phase (R6); the engineless digest's answering revision (O4); the target's kind (C11); the Addon's constraints before
   any revision (C16).
7. **Two behavior rows cite tests that do not hold their clause** · `model` · "A Function from an image goes"
   (`docs/behavior/settings-kinds.md`: "the pinned revision names another" has no test, and "with its owner" is
   `TestScenarioAddonFunctionRepaired`), and "An engineless object's config" (`docs/behavior/engineless-kinds.md`:
   `TestScenarioAddonFunctionRefused` holds its 422, not the three cases that judge no write).

### Routed (not this work's)

- **The in-place amendment of ADR-0341** (ce68555f) · elsewhere: the decider. The code matches it, and ADR-0358 needs it:
  with the former digest, `addon-config-no-longer-fits` waits for the resync (mutant S11: its test fails after 20 s). But it cites "the decider's go
  to merge ADR-0358 (#1576)", and #1576 holds no comment, so the choice itself is recorded nowhere. ADR-0345 Decision 2
  ("the Function's revision") is now read the same way, unamended.
- **Decision 5's "so one field finds an object's target in every revision" does not hold** (`adr`) · elsewhere: the
  decider. The rule refuses one replace that changes a `target`; a replace that drops the kind and one that adds it again
  with another target both pass (run: 200). Until the target moves, its old objects read `TargetNotFound` while their
  Function still applies them, and a new one gets 422. Rare; one sentence would state it.
- **`ContractOf` gives a Function with no revision no contract** for any caller (ADR-0303's code) · elsewhere:
  ADR-0303. Only an add-on's created Function hit it here; whether another caller can is not checked.
- **The guide's register command names funcd-addons v0.3.0** (`docs/guide/develop/services/addons/index.md:19`), whose
  manifest holds `settings[].schema`, which this version refuses · elsewhere: funcd-addons' release and the docs bump
  after it, as the ADR plans.
- **A defect found on the way, fixed in 37f8630d**: an App with a settings part never turned Ready, because
  `settingsObject` never recorded `status.appRevision` (ADR-0339 Decision 4). Without the line, `TestAppWithASettingsPart
  TurnsReady` fails after 10 s. By the decider's standing rule the PR carries the comment that names it.
- **`just ci-full`, the stress run, the Linux tests and the lanes** · elsewhere: `scripts/agent/gate.sh` and the author,
  by the brief.

### ✅ Verified correct (keep it)

- **The checks.**
  - `go build ./...` exited 0 on the host and with `GOOS=linux`; `go vet` exited 0 on `./...`, on `./pkg/funcd` with
    `-tags e2e` and on `./cmd/funcdctl` with `-tags dev`.
  - `golangci-lint`: 0 issues on the touched packages and with `dev`; with `e2e`, two `gochecknoglobals` in files this
    work does not touch.
  - `go test -race`: `internal/services/addon` (2.6 s, and 30 times over), `internal/ownedstore`, `internal/gc`,
    `internal/workflow`, `internal/app`, `internal/controlplane`, `api/…`, `pkg/funcd` (217.7 s) and `scripts`, all ok.
    Without race, `cmd/…`, and the whole of `./cmd/funcdctl` with `-tags dev` (223.7 s).
  - `just check-behavior` and `check-hygiene` exited 0. `just generate` changed no file. The bloat audit of the diff has
    0 hard flags.
  - `just check-specs`: 29 specs, 0 failing. `settings.fizz` passes with 25,944 states, as the ADR records, its ten
    mutants fail as named, and `settings.md` is current. Its five assertions each have an oracle marker on a passing test.
- **The scenarios.** Each of the sixteen has a named test, none skipped, all passing (Major 4 and Minor 5 noted). The four
  on a real Function and `TestAppWithASettingsPartTurnsReady` took 0.3 to 0.8 s each, call `t.Parallel()`, and passed
  18 times of 18 under `-race` in three copies at once. The three dev tests pass. No timeout was raised.
- **69 mutants die**, among them: exactly one of `image` and `ref`, the digest, the registry, the 40 characters and the
  rules of a `ref` (T1 to T4, C7, C8); the address, Secrets, Jobs, `self`, digest, idle time, runtime, resource group and
  references of a created Function (F1 to F4, F18 to F21, F23); on demand and gone only after the empty list ended well
  (S1, S2, S7, S8); the pin for the Function and the contract (S6, S14, C1); the answering revision (S11, S12); the own
  Function (F17, C3); `ArtifactNotReady`, `FunctionNotOwned` and their order (F5, F6, S20 to S22); the in-use rule, the
  pair and the marker (G1 to G3); `app delete`'s walk (L1); dev's pull (L2). A further fourteen survivors change nothing
  a rule names, or a line this work did not write.
- **Races and lifecycle.**
  - Four seeds of 160 random writes each (a policy created, edited and deleted, the target edited with its engine
    replaced, the Addon's image switched) on real Functions under `-race`: after each 40, what funcd left matched what
    was stored, 16 of 16.
  - An App's stack from an image, deleted and dropped with its Job: neither the Function nor the Job went before the
    stack, 16 of 16, and its destroy run Succeeded.
  - Two targets on two revisions, a pin that names a `ref`, a kind that the Addon drops, an older revision read late, one
    image of two unread, a restart before the empty list: all as decided.
- **Decisions 1 to 3.** The types and the refusals are as written; `KindSchema` reads a call's shape and a `kind` union, by
  `enum` and by `const`. The created Function answers 404 at its own address, runs and calls its engine through `self`.
- **Decision 4.** Only the Addon's pass reads an image (`functions.go:235`), by a digest that the Addon pins, before it
  names the revision current, and again for older revisions in use.
- **Decision 5.** On a running platform: the contract and the constraints of the pin, the current revision where the pin
  lacks the kind, 422 alone and 400 in an App naming the entry and the property, nothing for an own Function, a `ref` or a
  contract that does not compile.
- **Contracts.** `AddonFunction`, `AddonObjectBindings.Job`, `AddonRevisionStatus`, `AddonKindContract`, `ImageReader` and
  `KindSchema` are as written, and the OpenAPI is regenerated.
- **One mechanism.** `ownedstore.Ensure`, `revisionLabel`, the Workflow's two resolvers, its handler and idle time (moved to
  `api/types`), and the contract check are reused; the three reads of an AddonRevision became one.
- **The guide.** `build.md`'s contract passes the profile, compiles, takes a call and the empty list, and fits the page's
  constraints. The reasons tables match the code.
- **Tracking.** Since 7c16825a, ADR-0358 changed only in its Status line and the issue's number. F275 reads `reviewing`.
  The PR closes #1576.

### Definition of Done

15 of 18 items hold: 4 of the 6 review-checklist items, the design model, the behavior rows, and 9 of the 10 generic items.

| Review checklist item | Holds |
|---|---|
| No field of an Addon describes an object that only a Function reads | yes: `schema` is gone, an Engineless `config` is 400 |
| No write and no object's pass calls a registry; an engine's start waits for no read to succeed | yes |
| A list, its constraints and its call come from one revision, the target's | no: the first call of a created Function is checked against none (Major 2), and two of its terms have no test (Major 3) |
| A created Function has its object's marker and bindings and no address; another's is never written | no: the code holds it, no test does where no object names the target, nor for a `ref` (Major 3) |
| No Function and no call for a target that no object of the kind names, once its engine's default is back | yes |
| An App drops an add-on part whose kind has an image | yes |

The third miss is the scenario tests (Major 4). Not run here, by the brief: `just ci-full`, the whole e2e suite,
`scripts/agent/gate.sh` and the lanes.

### Counts

Blockers 0, majors 4, minors 7; all 11 are the model's. Review checklist: 4 of 6.

### Model scorecard

To record in the ledger commit: claude-opus-5-5 on ADR-0358 (implementation) → changes-requested, 0 blockers, 4 majors,
7 minors, 11 model-attributed, DoD 15/18.

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0358 --phase implementation --model claude-opus-5-5 \
  --verdict changes-requested \
  --blockers 0 --majors 4 --minors 7 \
  --model-attributed 11 \
  --dod-passed 15 --dod-total 18 \
  --report docs/reviews/adr-0358-implementation-claude-opus-5-5.md \
  --notes "changes-requested; Decisions 1 to 5 built as written, 69 mutants die, random lifecycle runs on real Functions converge; Major 1 (model): a Conflict on the Addon's status no longer ends its pass, which then removes the stored current revision (ADR-0339 D6); Major 2 (model): the first call of a created Function meets no contract, so an off-contract object reaches the handler (D5); Major 3 (model): 12 decided rules unpinned, the ones that keep a user's Function among them; Major 4 (model): TestScenarioAddonContractOfThePin flakes on a stale resourceVersion; Minors (model): {} judges no rule, group state kept, a guide sentence, ADR-0341's amendment, the dev scenario, 15 unpinned lines, 2 rows; routed: ADR-0341's recorded go, the target over two replaces (adr), ContractOf with no revision, funcd-addons' release, the App fix's comment"
```

### Recommendation

The work goes back to the builder (`adr-impl`). The ADR stays `Reviewing` and F275 `reviewing`, and nothing is stamped.
The two defects are small edits, and each has a reproduction above. The next round reruns the four reproductions (the
Conflict at unit and platform level, the three first calls), the twelve mutants of Major 3, F24, K1 and T10, and the flake
in six copies.
