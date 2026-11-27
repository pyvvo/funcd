## Verdict: pass — 0 blockers, 0 majors, 2 minors (ADR-0362 implementation, round 2, model: claude-opus-5-5)

This independent review covers round 2 of ADR-0362's implementation, b2b062e0 on branch `docs/adr-0362-app-live-view`,
on top of round 1's a23206e2 and 0c01c576 and origin/main 18bec323, which has not moved. Round 1
([report](adr-0362-implementation-claude-opus-5-5.md)) asked for changes: 3 Majors and 3 Minors. The reviewer did not
write or edit the work. Mutants and round 1's probes ran again through `go test -overlay`, so the worktree was not
changed. The repo-wide checks (`just ci-full`, the e2e suite, `scripts/agent/gate.sh`, the `app-todo` lane) are left
to the PR's gate.

Every round-1 finding is fixed and pinned by a test. All 33 of round 1's mutants that still apply now die, including
the 11 that survived round 1. Round 2 also settled two points that round 1 routed to the decider. The two Minors below
record where those settlements leave the ADR's text behind, and four lines that no test pins.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

1. **Two settled points depart from the ADR's text, and nothing records it** · here · model.
   - The delete view's `InUse` now names only a noted object that `gc.Uses` reads as holding the object, which are
     the GC's own protections (`cmd/funcdctl/appview.go:448`). Decision 3 says "a noted object still present that
     names it (Decision 4)".
   - A box taller than the terminal now folds its Ready and NotStarted rows into `✓ N more ready`, and a delete box
     folds its deleted rows into `✓ N more deleted` (`appview.go:356`, `:415`, `:491`). Decisions 2 and 3 draw "a row
     per" child and per noted object.

   Both rules are sound: one is what `InUse` means, and the other is what keeps Decision 5's redraw on the screen.
   But `docs/behavior/README.md` says "Where the code differs from an ADR, the row says what the code does, with
   `differs`". The view's row in `docs/behavior/funcdctl.md:43` still reads `decided` and "a row per required App,
   hook and part". Two details sharpen this:
   - The last draw is folded as well. A large App's deploy or delete on a short terminal therefore ends on
     `✓ N more ready` without the per-part times the Context asks for, though no redraw follows that draw.
   - The comment at `appview.go:489` says a redraw "never" moves past the top of the screen. A box whose unsettled rows
     alone are taller than the terminal still does, for example the first polls of a 20-part install on 24 rows.

   Fix: give the departures a `differs` row or clause in `funcdctl.md`, and draw the last box unfolded. Optionally,
   soften the comment. The ADR's own text is for the decider (Routed).
2. **Four lines of the round have no test** · here · model. Two mutants among the six survivors matter:
   - B5: dropping `termSafe` on the delete view's reason (`appview.go:422`). That reason can be an object's own Ready
     reason, which round 1's probe sent there unescaped.
   - B16: folding Lagging rows too (`appview.go:356`), against Decision 5's "a Lagging row keeps ◐, its reason and a
     running time".

   B15, the delete box never folding, and B6, the App's own Ready reason past the deadline left unescaped, survive
   too, but they matter less. B7 and B10 are equivalent on admitted input: `Unmet`'s text embeds only a strict SemVer
   version or a phase, and a range must parse as a SemVer constraint (`validateRequires`).

### Round 1's findings

- **Major 1, the dry run after the apply**: fixed. The dry run now runs before the apply loop, only for a live view
  (`cmd/funcdctl/app.go:542-553`), and the plan line counts `plan.Hooks`. `TestScenarioAppViewDeployLive`
  (`app_template_test.go:639`) drives `app deploy` live through `newRootCmdOf` with a Planner that plans a create only
  while the App is not stored. That proves the order, and both the no-dry-run mutant (B1) and the dry-run-after-the-apply
  mutant (B2) die. The `cli.live` seam is nil in production, so `liveTerminal` decides there.
- **Major 2, a stop drawn as a plan**: fixed. `actions` treats a line with a reason as the stop (`appview.go:155`).
  Round 1's probe now draws no plan line for each of the Planner's three shapes, and `TestAppViewPlanFallback` uses
  those shapes (V6, V6b killed).
- **Major 3, server text unescaped**: fixed. `termSafe` covers the title's version, a required App's version and
  range, `Unmet`'s text, a child's reason, the delete reason and the reason past the deadline. Round 1's probe now
  finds no unescaped control sequence in the deploy box, the delete view or the lines. `TestAppViewEscapesServerText`
  pins three of those fields (B4, B8, B9 killed); the other lines are in Minor 2.
- **Minor 1**: `done() error` matches the Contract. It returns nil, and its two deferred callers discard it.
- **Minor 2**: the waiting header and the ⋯ line now show only while the followed revision has not started
  (`appview.go:180`), which is ADR-0219 Decision 3's wait. `TestAppViewRequiresWaitOnlyBeforeTheStart` covers a
  started rollout and an upgrade that waits (B11, B12 killed).
- **Minor 3**: every round-1 survivor dies now:
  - Lagging (V3, V4) and the reason past the deadline (V7): `TestAppViewLaggingAndPastTheDeadline`.
  - A failed hook (V9): `TestAppViewFailedHook`.
  - The own Ready reason on delete (V11): `TestAppViewDeleteReasons`.
  - `TERM=dumb` (V15): `TestAppViewLiveFile`.
  - The requires lines (A1): `TestAppViewRequirementLines`.
  - The SQLDatabase rows (G3): `TestUsesReadsTheProtectedFields`.
  - Each `Names` field's kind (G9): `TestNamesCoversEveryField`, through `kindOfField`.
  - The live delete (A4): `TestScenarioAppViewDeleteLive`.
  - The live deploy (B1): `TestScenarioAppViewDeployLive`.

### Round 1's routed points, settled in this round

- **Owners are no Names**: the owners of a SQLDatabase, a Bucket prefix and a KV table are now `notNames` entries,
  with the reason "the workload the store serves". This is consistent with Decision 4's list, which never named them,
  and `uses` never read them, so `InUse` is unchanged. Round 1's probe now shows that a store no longer waits on its
  owner Function. Restoring a KV table's owner (G11) fails `TestNamesCoversEveryField`.
- **The delete view's InUse reads `gc.Uses`** (`uses`, exported, `internal/gc/names.go:243`): round 1's probe no
  longer prints `InUse: Route/web` or `InUse: KVStore/cart`. `TestAppViewDeleteReasons` pins this, and reading every
  name again (B17) fails it. The departure from Decision 3's wording is Minor 1.
- **The fold of a box taller than the terminal**: `TestAppViewFoldsToTheTerminal` sits on the boundary. The
  never-fold mutant (B13, B18) and the one-line-late mutant (B14) die. The departure is Minor 1, and the untested
  lines are Minor 2.

### Routed (not this work's)

- **Decisions 2 and 3's text** · elsewhere: the decider · adr. The decider may amend in place, as was done for
  ADR-0290 and ADR-0361, to record two rules: the delete reason names what holds the object (`gc.Uses`), and a box
  taller than the terminal folds its settled rows. The decider may instead ask for either rule to be reverted. Until
  then, Minor 1's `differs` row keeps the registry honest.
- **The behavior row lives in `funcdctl.md`** (round 1, adr): unchanged. The written part of `docs/behavior/apps.md` is
  still 4 bytes under its cap.
- **The repo-wide checks and the lane** · elsewhere: the PR's gate.
- **Feature issue**: #1606. The PR that ships this work carries `Fixes #1606`.

### ✅ Verified correct (keep it)

- **Build, vet, lint**: `go vet` on `./internal/gc/ ./internal/app/ ./cmd/funcdctl/` and on `-tags dev ./cmd/funcdctl/`,
  exit 0. `go tool golangci-lint run` on the three packages and with `--build-tags dev` on `./cmd/funcdctl/`:
  `0 issues.` each.
- **Tests**: `go test -race -count=1 ./internal/gc/ ./internal/app/ ./cmd/funcdctl/` passed (1.6 s, 18.8 s, 10.1 s).
  The `-tags dev -run 'DevLAN|Banner|AppView|AppDeploy|AppDelete'` run passed all 39 tests, none skipped.
  `TestScenarioAppViewDeployLive` took 2.8 s and `…DeleteLive` 2.4 s. The view's tests passed 5 times in a row under
  `-race` (`-count=5`).
- **Generated files and hygiene**: rerunning the spec generator and both Spec-page tests with `-update` changed nothing
  (`scripts/check-generated.sh` exit 0). `go mod tidy -diff` and `gofmt -l` were clean, and `just check-hygiene` and
  `go run ./scripts/behavior -check` exited 0.
- **Mutants**: 53 ran.
  - Killed (47): all 33 of round 1's that still apply (S3 was equivalent in round 1 and was not rerun; A3 became
    B1), and 14 of the 20 new ones (G11, V6b, B1–B4, B8, B9, B11–B14, B17, B18).
  - Survived (6): B5, B6, B7, B10, B15 and B16. B7 and B10 are equivalent on admitted input; the others are in Minor 2.
- **Unchanged and still right**: the deadline (S1, S2, S4 killed by `TestScenarioAppViewDeadline`), `uses` and `InUse`
  (G1–G6), the box, its redraw and cut (V13, V14), waits-on (V1, V2), the clocks (V5), hooks (V10), the lines (A2, A5),
  and `funcdctl dev`'s plain lines.
- **Tracking**: b2b062e0 changes no ADR and no feat row. Since acceptance, the ADR's only change is round 1's status
  line.

### Definition of Done

14 of 14 items hold: the 6 Review-checklist items, "the tests pass", and the 7 applicable generic items. "Contracts
honoured" holds now that `done` returns an error, the plan line follows Decision 2, and the conventions hold with
`termSafe` in the box. `just ci`, the gate and the `app-todo` lane are left to the PR's gate and are not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0362 (implementation, round 2) → pass, 0/0/2, 2 model-attributed, DoD 14/14. See
docs/reviews/model-scorecard.md.

### Status

On this pass, ADR-0362 is stamped `Reviewing → Implemented`, and F279 in `docs/feat/0010-feat-apps.md` moves to
`implemented`. Minor 1's `differs` row and the last draw drawn unfolded can land in the same PR, before the gate.
