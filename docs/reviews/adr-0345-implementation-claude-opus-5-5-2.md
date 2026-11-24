## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0345 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the fix commit `3ce5f0ff` on `feat/addon-runtimes`, one commit on top of `9322d4dc`, against the
round-1 report (`docs/reviews/adr-0345-implementation-claude-opus-5-5.md`). Every check below was run at `3ce5f0ff`,
not read. This time the dependent packages ran too, `pkg/funcd` and `cmd/funcdctl` among them.

All three round-1 findings are closed. The commit also carries the rulings of 2026-11-24, which the decider delegated
and which close round 1's routed questions. Three of them are sound as they stand. The fourth holds in one direction
only (Minor 1).

### Round 1's findings

- **Major 1, a forced ResourceGroup delete: closed.**
  - `deleteMember` now deletes through `deleteMemberAt` (`internal/controlplane/handlers.go:533`). An add-on object
    goes through `gc.DeleteAddonObject`, and a member it holds stops the force with 409 naming it, as ADR-0170 does
    for a protected member.
  - Round 1's probes now see the forced DELETE answer 409 (`InfraStack "bunny-edge" is held at its deletion until its
    Function has destroyed what it holds`), and the stack stays, deleting.
  - `TestResourceGroupForceHoldsAnEnginelessMember` pins it: the held member stays, an unprotected member goes, and
    the group remains. Mutant F1, the generic delete for every member, fails it.
- **Minor 1, six rules that no test reached: closed.** I rewrote round 1's six survivors against the new
  `objects.go`, and each one now fails a test of `internal/services/addon/objects_rules_test.go`:
  - the unmarked object in a call fails `TestEnginelessCallCarriesOnlyMarkedObjects`;
  - the uid check fails `TestEnginelessOkRemovesOnlyTheObjectItCarried`;
  - the Function's revision in the digest fails `TestEnginelessNewFunctionRevisionCallsAgain`;
  - `ReconcileFailed` and `NoResult` fail `TestEnginelessCallOutcomes`;
  - the backoff fails `TestEnginelessFailedCallBacksOff`.
- **Minor 2, the Function watch: closed.** `MapEnginelessFunction` also maps an object by the `function.ref` of the
  revision it pins (`internal/services/addon/objects.go:491`). Round 1's probe passes now. Mutant F2, the current
  Addon only, fails `TestEnginelessFunctionChangeRequeuesByPinnedRevision`.

### The rulings of 2026-11-24, judged against the ADRs and the code

- **Engine fields on an engineless object, refused 400: sound.** ADR-0340 Decision 3 refuses them on a settings
  object, which runs no engine either, and ADR-0345 Decision 3 gives the Function no binding. `spec.deletion` and
  `spec.functions` stay. `engineFields` (`internal/controlplane/addon.go:706`) serves both the API's write and an App's
  part. Mutant F4 fails `TestEnginelessObjectRefusesEngineFields`.
- **Engineless objects counted under `addons.maxObjectsPerNamespace`: sound.** ADR-0332 Decision 2 capped every add-on
  object when all of them ran an engine, and ADR-0340 Decision 6 set only settings objects apart. The 400, its error
  case, the config's doc and its Spec page now say "engine and engineless kinds".
- **A later event of an ended JobRun, one more `run` call: sound as accepted.** A call carries the group's full list
  (Decision 2), and the Function applies each list idempotently (Consequences).
- **A create for a kind whose stored object ran the other way, refused 409: sound in intent, short in one
  direction.** It keeps how objects run, as ADR-0344 Decision 10 and ADR-0345 Decision 1 do for a replace. It holds for
  an object marked Engineless: mutant F3 fails `TestEnginelessKindKeepsItsWorkloadAcrossDeletion`. It does not hold for
  an engine object (Minor 1).

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **Minor 1 — An engine object does not bar an Engineless Addon of its kind once its Addon is gone** · model.
  - The code: `objectsKeepTheirWorkload` (`internal/controlplane/addon.go:722`) tells an engine object by the
    AddonRevision it pins, and skips the object when that revision is not stored (`:745-747`). An Addon's revisions
    are its children (`internal/gc/gc.go:43`), so they go with it.
  - The probe: a test I did not commit, at `3ce5f0ff`. `login` runs an engine, pinned to `rauthy-1` with no mark. Then
    `rauthy` is deleted (204), and `rauthy-1` is collected within the probe's poll.
  - The result: an Addon `idp` that serves `Engineless` for the kind `IdentityProvider` gets 200. The behavior row "A
    kind's workload" (`docs/behavior/addons.md`) says such a create is refused.
  - The effect: `login` waits `AddonRevisionNotFound`, and an edit would move it onto the engineless kind while its
    engine storage stays on disk.
  - A pin alone does not tell an engine object apart: an App's part pins its revision before its first pass
    (`TestEnginelessCallCarriesOnlyMarkedObjects`).
  - Fix (builder): give the bar a signal that outlives the Addon, such as a mark an engine object stores too, or state
    in the row that only a marked engineless object bars the create.

### Routed (not this work's)

- **Where the rulings live**: they are in the behavior rows and the commit message, not in ADR-0345's text. By the
  decider's practice for rulings on a shipped ADR, an in-place amendment records them (an `Amended in place` header
  line and the lines marked amended). Home: the decider.
- **The lanes**: as in round 1, no lane case covers ADR-0345 and its plan names none. This commit also changes the
  forced group delete and the admission of add-on objects, so the PR's gate should run the add-on lanes once. Home: the
  integration step.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`:
  - `go build ./...` passes.
  - `go vet` is clean on `api/types/v1alpha1`, `internal/controlplane/...`, `internal/services/addon`,
    `internal/platform/config`, `internal/gc`, `internal/app`, `pkg/sdk`, `cmd/funcd` and `pkg/funcd`, and also with
    `-tags e2e` on `pkg/funcd` and `-tags dev` on `cmd/funcdctl`.
  - `go tool golangci-lint run` reports 0 issues, also with `--build-tags dev`, and gofmt is clean.
  - The new tests pass under `-race`.
  - `go test` passes on those packages (`pkg/funcd` 90 s, `cmd/funcd` 91 s) and on `cmd/funcdctl -tags dev` (244 s).
  - `TestEnginelessKindOnARealFunction` passes in 0.47 s.
  - `go run ./scripts/behavior -check`, `just check-hygiene` and `go mod tidy -diff` are clean, and the spec generator
    leaves no drift.
  - `TestBackup_GoesOnWhenItsListingFails` passed in this full `pkg/funcd` run.
- **The mutants**: all 10 of this round are caught, round 1's six survivors and F1 to F4 above.
- **`TestAddonObjectAnswersMatrix`**: it passes here. With `9322d4dc`'s rows, it fails
  (`createAddonObject: one case holds "spec.config breaks the schema"`). ADR-0344's round 2 missed that break, and
  its report now says so.
- **Tracking**: the ADR's diff is still its status line alone, and the design spec, its markers and its tests are
  unchanged since round 1, where they passed.

### Definition of Done

16 of 16 items hold: the 7 Review-checklist items, every delete path holding now among them, and 9 generic items. The
open Minor concerns a ruling outside the ADR's text.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0345 (implementation, round 2) → pass, 0 blockers, 0 majors and 1 minor,
model-attributed, DoD 16/16. See `docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0345 is stamped `Implemented`, and F270 is `implemented`. The builder may close Minor 1 in the same PR, in the code
or in the row. The PR that ships it closes #1533.
