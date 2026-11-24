## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0345 implementation, model: claude-opus-5-5)

This report reviews `feat/addon-runtimes` from `1e098771` (ADR-0344's tip) to `41f280a7`: the feature `bcfb6fc1`, the
guide, example and behavior rows `1377b038`, and the status bump `41f280a7`. The reviewer did not write the code.
Every check below was run at `41f280a7`, not read.

The call, its groups and causes, the status, the hold on the three paths the ADR names, retain, the restart and an
App's order all hold, and the design spec passes with each mutant caught. One delete path skips the hold: a forced
ResourceGroup delete removes a held stack at once.

### 🔴 Blockers

None.

### 🟡 Major 1 — A forced ResourceGroup delete removes an engineless object past its hold  ·  attribution: adr

- `forceDeleteResourceGroup` (`internal/controlplane/handlers.go:444`) deletes each member with `deleteMember`
  (`:498`). That function calls the generic `deleteObjIf` (`:500`, `:522`), never `gc.DeleteAddonObject`.
  `admission.Members` lists every namespaced kind, so a standalone add-on object of the group is one of those members.
- A probe test (not committed) on the platform at `41f280a7` registers the engineless kind, applies a stack with
  `deletion: delete` in the group `stacks`, and waits for its first pass to store `status.workload`.
  - In the first case, a plain DELETE of the stack answers 202 and holds it. Then a forced DELETE of `stacks` answers
    204, and the stack is gone.
  - In the second case, a forced DELETE alone answers 204 and removes the marked stack.
  - No call carried it as deleting, so nothing destroyed what it holds. That is what `NoStackGoesUndestroyed` forbids.
- Design check:
  - It happens at `41f280a7` (above).
  - Decision 6 holds such an object "at its deletion", and the checklist asks that every delete path hold. ADR-0170's
    force keeps "each member's protections" (the `force` parameter's doc, `internal/controlplane/routes_rest.go:31`).
    Its scenario `resourcegroup-force-stops-at-protected-member` answers 409 naming the protected member.
  - No issue matches, and no board card holds it.
- Attribution: Decision 6, Implementation plan item 3 and the checklist all name the same three paths (the object's
  DELETE, the collector, an App's prune), and the builder covered all three. The fix needs no new decision.

Fix (builder): `deleteMember` removes an add-on object through `gc.DeleteAddonObject`. A held member stays, and the
forced delete answers 409 naming it, as for a protected Bucket.

### Minor

- **Minor 1 — Six rules have no test that reaches them** · model. Each mutant below survives the whole
  `internal/services/addon` suite:
  - Decision 1, a call carries no unmarked object. Removing the `status.workload` filter from `groupObjects`
    (`internal/services/addon/objects.go:202`) survives: no test calls a group that holds a marked and an unmarked
    object.
  - The uid check, which the Design model leaves to tests. Removing `o.UID != uid` from `removeHeld` (`:375`) survives.
  - Decision 2, the Function's revision is in the digest. Dropping it (`:229`) survives.
  - Decision 5, a failed call is `ReconcileFailed` and a result left out is `NoResult`. Writing Ready in either case
    (`:321`, `:323`) survives.
  - The backoff, which the Design model leaves to tests. `groupDone` without the call's error (`:279`) survives.

  Fix: one test each, with the rig in `internal/services/addon/objects_test.go`.
- **Minor 2 — The Function watch maps an object by its Addon as it is now, not by its pinned revision** · model.
  Decision 1 takes an object's Function from the revision it pins, but `MapEnginelessFunction`
  (`internal/services/addon/objects.go:471`, `:481`) reads the current Addon's `function.ref`.
  - A probe test (not committed) pins `bunny-edge` to the first revision. It then moves `function.ref` in a second
    revision and deletes `infrastack-reconciler`. `bunny-edge` waits `FunctionNotFound` for that Function.
  - Once the Function is created again, `MapEnginelessFunction` returns no request. A wait returns no requeue
    (`:115`), so the object waits until some other event: an edit, an Addon change, or a JobRun's end.
  - It takes a standalone object on an older revision whose Function goes and comes back, so it is rare.
  - Fix: map through each object's pinned revision, as `groupObjects` does.

### Routed (not this work's)

- **Moving a kind between Engine and Engineless** is refused with 400 (`internal/controlplane/addon.go:183`,
  `TestEnginelessKindKeepsItsWorkload`). Both ADRs leave the move unnamed, and `docs/behavior/addons.md` marks the
  refusal **uncovered**. The refusal is the safe choice, since each object keeps how it runs. Home: the decider, to
  confirm it in an amendment or allow the move.
- **The per-namespace quota counts engineless objects with the ones that run an engine**. Its 400 says "add-on objects
  that run an engine" (`internal/controlplane/admission/addon.go:48`). `docs/behavior/limits.md` marks it
  **uncovered**. Home: the decider.
- **An engineless object's engine fields** (`spec.ingress`, `spec.bindings.secret`, `spec.resources`) are accepted and
  ignored: `engineFields` (`internal/controlplane/addon.go:700`) refuses them only on a settings object. No ADR says
  either way. Home: the decider, as a question.
- **The lanes**: no lane case covers ADR-0345, and its plan names none (item 6 puts the tests in
  `internal/services/addon` and `pkg/funcd`, with the fixture Function in `e2e/fixtures`). The addon-runtimes
  worktree has no `.cache/lanes/`. The main checkout's add-on lane logs end at 02:13, before the ADR-0345 commits at
  02:25 to 02:26. This change touches paths those lanes run: the add-on object's DELETE route, the settings call's
  shared state and the collector's and the prune's remove. Home: the PR's gate, which should run the add-on lanes once.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`:
  - `go build ./...` passes.
  - `go vet` is clean on the 10 touched packages, plus `-tags dev` on `cmd/funcdctl` and `-tags e2e` on `pkg/funcd`.
  - `go tool golangci-lint run` reports 0 issues, also with `--build-tags dev`, and gofmt is clean.
  - `go test` passes on the touched packages (`pkg/funcd` 84 s, `cmd/funcdctl -tags dev` 211 s), and the engineless
    tests pass under `-race`.
  - `TestEnginelessKindOnARealFunction` (e2e, on a real node Function, `t.Parallel`) passes in 0.48 s.
  - `scripts/specs.py check` on `engineless-delete.fizz` passes. Its six mutants each fail their assertion, and its
    diagram is current.
  - `go run ./scripts/behavior -check`, `just check-hygiene` and `go mod tidy -diff` are clean, and the spec
    generator left no drift.
- **The scenarios**, each a named, passing test:
  - `TestScenarioEnginelessApply`, `…Groups`, `…Resync`, `…RunEnds`, `…Waits`, `…DeleteHeld`, `…DeleteInFlight`,
    `…Retain` and `…Restart` in `internal/services/addon`.
  - `TestScenarioEnginelessAppOrder` in `internal/gc`.
  - `TestScenarioEnginelessShapeRefused` in `pkg/funcd`.
  - The oracle markers of `PartsOutliveTheStack`, `NoStackGoesUndestroyed` and `DeletedAppGoes` sit on the tests the
    Design model names.
- **The mutants**: 11 of 17 are caught:

  | Mutant | Caught by |
  |---|---|
  | any answer removes a deleting object | `…DeleteHeld`, `…Restart` |
  | a live call's ok removes an object deleted since | `…DeleteInFlight` |
  | `DeleteAddonObject` never holds | eight tests in three packages |
  | the collector takes other parts first | `TestScenarioEnginelessAppOrder` |
  | the prune keeps no part waiting | `TestEnginelessPartHoldsThePrune` |
  | `retain` ignored on a deleting object | `…Retain`, `…Waits` |
  | a JobRun's end makes nothing due | `…RunEnds` |
  | no namespace pass when the last part goes | `TestEnginelessPartGoneCollectsItsNamespace` |
  | a held object's DELETE answers 204 | `TestEnginelessDeleteAnswersHeld`, `TestAddonObjectAnswersMatrix` |
  | the digest ignores the deleting state | `…DeleteInFlight` |
  | no `Deleting` status | `…DeleteHeld` |

  The six survivors are Minor 1.
- **The design**:
  - The first pass stores the mark and calls nothing, and only marked objects join a group.
  - One call runs per group, on a goroutine, under `addons.settingsTimeout`, and its end requeues the group.
  - The `start`, `change`, `run`, `retry` and `resync` causes share ADR-0341's state machine, with `run` due ahead of
    a backoff.
  - A call sent live removes nothing. Only an ok to a deleting call at its uid removes an object, and `retain` lets
    it go with no call.
  - A write keeps `deletionTimestamp` (`withServerMeta`), and the App's write of a part keeps it too.
  - An App's other parts wait on both a deletion and a prune, and the last part's going asks for a namespace pass.
- **The contracts** match the ADR's block: `WorkloadEngineless`, the `function` and `resync` attributes,
  `ObjectsCaller`, `ObjectsCall`, `CallObject`, `status.workload`, `gc.DeleteAddonObject` and the handler's
  signature.
  - The DELETE route answers 202 with the object, and 204 otherwise, in the OpenAPI too.
  - The CloudEvent's type, source and data are as Decision 2 says, and an http Function fails the call
    (`TestAddonObjectsCallerSendsTheCall`).
  - `funcdctl delete` prints `deleting <Kind>/<name>` (`TestCLIDeleteHeldAddonObject`).
- **Tracking**: the ADR's diff is its status line alone, and FEAT-0012's F270 row reads `reviewing`. The back-links of
  ADR-0332, 0341, 0170, 0199 and 0337 are in place. The behavior rows are the ones the ADR lists, plus a new
  `engineless-kinds.md`, and two rows honestly mark behavior that no ADR decides.

### Definition of Done

15 of 16 items hold: 6 of the 7 Review-checklist items and 9 generic items (the touched packages' checks, with the
gate's `just ci-full` still to run; every scenario; real behavior; the contracts; the tree; the conventions; the deps;
no scope creep; tracking). The miss is the checklist's "Every delete path … holds through `DeleteAddonObject`"
(Major 1, adr).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0345 (implementation) → changes-requested, 0 blockers, 1 major and 2 minors, of which
2 are model-attributed, DoD 15/16. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to the builder: Major 1, so the forced group delete holds a stack as the other paths do, and the two Minors.
Nothing is stamped: ADR-0345 stays `Reviewing`, and F270 stays `reviewing`. The PR that ships it closes #1533.
