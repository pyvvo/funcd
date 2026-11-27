## Verdict: changes requested — 0 blockers, 3 majors, 3 minors (ADR-0362 implementation, model: claude-opus-5-5)

This independent review covers ADR-0362's implementation on branch `docs/adr-0362-app-live-view`: a23206e2 (the server
half: `AppRevisionStatus.deadline`, `gc.Names` with `uses` read over it, `app.Unmet`) and 0c01c576 (the view, its
wiring, the lines, the docs and the behavior rows), on origin/main 18bec323. The reviewer did not write or edit the work.
Mutants and probes ran through `go test -overlay` from scratch files, so the worktree was never changed. The repo-wide
checks (`just ci-full`, the e2e suite, `scripts/agent/gate.sh`, the `app-todo` lane) are left to the PR's gate.

Most of the work is right: the deadline, `Names` and `uses`, the box and its redraw, waits-on, the clocks, hooks, the
delete view and the lines all hold, and 23 of 35 mutants die. Three defects stop sign-off: the plan line comes from a
dry run made after the apply, a plan that holds a ChildNotOwned stop is drawn as a plan, and the box prints
server-supplied text without `termSafe`.

### 🔴 Blockers

None.

### 🟡 Major

1. **The plan's dry run is made after the apply, so the plan line races the reconciler** · here · model. Decision 2
   takes the counts and actions "from a dry-run of the same apply made just before it", and the decider's acceptance
   names "the dry run before each deploy". `deploy` applies first (`cmd/funcdctl/app.go:542`, `applyRendered` calls
   `c.Apply`) and makes the dry run afterwards (`app.go:569`). The commit message says "a dry run before the apply".
   After the apply, `PlanApp` plans against whatever the reconciler has written by then. A scratch test on the
   internal/app harness (install, edit the image as the apply does, then plan before and after one reconcile pass)
   printed `revision="todo-2" parts=[{Function todo-api update}]` before the pass and `revision="" parts=[] hooks=[]`
   after it. The reconciler starts that pass on the App's write (`pkg/funcd/funcd.go:1164` registers it on App
   events). When it finishes before the dry run, the view draws
   `0 create · 0 update · 0 prune`, no action on any row, and `–` for the time of every part that is Ready at its
   first poll, updated ones included. In the same area, `planCounts` counts the App spec's hooks
   (`cmd/funcdctl/appview.go:206-208`) instead of the dry run's `plan.Hooks`. A deploy whose spec is unchanged
   therefore names hooks that will not run. Fix: make the dry run before the apply loop, only when the view will be
   live, and count `plan.Hooks`. Add a test of the live wiring: mutants A3 and A4 below show that nothing covers it.
2. **A plan that holds a ChildNotOwned stop is drawn as a plan** · here · model. Decision 2: when the plan holds a stop,
   "the view draws no plan line and no action". `actions` treats only a part with an empty action as a stop
   (`appview.go:150`). Only the Secret stop has no action, though. The Planner gives the ChildNotOwned stop `update`
   (`internal/app/plan.go:45`, pinned by `internal/app/plan_test.go:189`) and the stop of a revision name that
   another owner holds `create` (`plan.go:34`, pinned by `plan_test.go:204`). `PlanPart.Reason` is the field that marks the stop line: it is "set
   only on the line where the rollout would stop". A scratch test fed the Planner's shapes to `actions` and drew the
   view. The ChildNotOwned stop drew `plan  0 create · 1 update · 0 prune · 0 hooks` and the namesake stop drew
   `plan  1 create · …`. Only the Secret stop drew no plan line. `TestAppViewPlanFallback`
   (`cmd/funcdctl/appview_test.go:236`) passes because its ChildNotOwned line has no action, which the Planner never
   sends. Fix: treat a part with a `Reason` as the stop, and test with the Planner's three shapes.
3. **The live view prints server-supplied text without `termSafe`** · here · model. funcdctl passes the
   server-supplied fields it prints through `termSafe`, so that they cannot send escape sequences to a terminal
   (`cmd/funcdctl/logs.go:163-166`). This deploy's
   own lines do it (`app.go:869`, `:896`, `:906`), and so does `app history` (`app.go:134`). The box applies it only
   to the App's Ready line (`appview.go:195`). It prints these as received: a part's `status.children[].reason`, which
   can be "the part's own reason", such as an add-on object's (`appview.go:324`); a required App's `spec.version`,
   which is a free label (`appview.go:254`), and the `Unmet` text built from it (`:260`); the App's own version in the
   title (`:165`); and, on delete, an object's own Ready reason (`holdReason`, `:394`). A scratch test put a terminal
   control sequence in a part's reason and in a required App's version. The box wrote it unescaped twice, and the
   delete view wrote one from an object's Ready reason unescaped. The lines path wrote none. Fix: pass these fields
   through `termSafe`, as the lines do, and add a test that the box contains no control byte from a server-supplied
   field.

### Minor

1. **`done` returns no error** · here · model. The Contracts declare `func (v *appView) done() error`. The code has
   `func (v *appView) done()` (`appview.go:480`), and both callers defer it. Return the error the Contract names (nil
   is enough), or let the decider settle the signature.
2. **The required-App wait depends only on `Unmet`, not on whether the revision has started** · here · model.
   Decision 2 cites ADR-0219 Decision 3, under which only a revision without `startedAt` waits. The view instead
   counts unmet requirements on every poll. (a) The ⋯ line is drawn only when the App has no children
   (`appview.go:189`, a condition the Decision does not have). An upgrade that waits for a required App therefore
   draws the previous revision's rows, with the plan's actions, under "waiting for 1 of 1 required Apps", where
   Decision 2 says "⋯ <n> parts wait for the required Apps; none is written and no hook runs". (b) The header
   (`appview.go:176-178`) reads "no deadline yet" and hides the countdown when a required App turns unmet after the
   rollout started, while its deadline runs. Fix: use the waiting header and the ⋯ line only when the followed
   revision is nil or has no `startedAt`.
3. **Some items of the Decisions and the Implementation plan have no test** · here · model. 11 of the 35 mutants
   survive. The gaps, each with its surviving mutants, are:
   - the Review-checklist item "a Lagging part counts as settled and keeps its running time" (V3, V4: no test draws a
     Lagging part);
   - `InUse` "over a table of every kind it protects", which misses a SQLDatabase that a Function's
     `bindings.sql` names (G3 survives the internal/gc and internal/app tests; the e2e
     `TestScenarioPruningKeepsBoundDatabase` covers it in CI);
   - "`Names` for each kind and field", where the reflection test checks the field paths but not the kinds (G9);
   - Decision 6's `App/<app> requires App/<name>: <text>` lines (A1);
   - Decision 1's `TERM=dumb` (V15);
   - Decision 2's Ready reason past the deadline (V7) and a failed hook's ✗ (V9);
   - Decision 3's own Ready reason (V11);
   - the live wiring in `app.go` (A3, A4).

### Routed (not this work's)

- **The behavior row is not in `docs/behavior/apps.md`** · elsewhere: the decider (the ADR's *Behavior rows*) · adr.
  The ADR asks for a new row in `apps.md` and for its `waiting` row to name the view's reasons. On main, the written
  part of `apps.md` is 16354 bytes, and `scripts/behavior` caps a chapter at 16384 (`scripts/behavior/main.go:34`,
  `:1034`). The row is 374 bytes, so it cannot be added without cutting other decided rows or splitting the chapter,
  which is the check's own advice. The model put the row in `docs/behavior/funcdctl.md`, next to that file's existing
  `funcdctl app delete` row. It used 26 of the 30 bytes left in `apps.md` to name the view in the `waiting` row, and
  `-check` passes. The decider can split the apps chapter or accept the new place. Nothing stays here, although the `funcdctl.md` row
  could also name the view's waiting reasons (`InUse: <Kind>/<name>`, the required-App wait).
- **`Names` reads every field that names an object, and the view treats every such name as a dependency** ·
  elsewhere: the decider (Decisions 3 and 4) · adr. Decision 4's list leaves out fields that its own claim, "every
  object `u`'s spec names", includes: owners of tables, prefixes and databases, a BackupSchedule's scope and target, an
  add-on object's functions and bindings, a Lake's location, ingress auth providers and declared Secrets. The
  Implementation plan's reflection test fails on any `ObjectName` field that is neither read nor listed as naming no
  object, so reading them all is the faithful reading. That is no scope creep, and `uses` keeps `InUse` as it was.
  Decision 3 ("a noted object still present that names it") and Decision 4's waits-on rule ("the first part … that
  `u` names") then count names that hold nothing. A scratch test showed three results. A Function still present
  beside its Route reads `waiting InUse: Route/web`, though a Route's Function is no protected field. A Function that a
  KVStore names as a table's owner reads `InUse: KVStore/cart`. A Pending store "waits on" its Pending owner Function
  while that Function waits on the store. The decider can rule whether a delete reason reads only the fields that
  `InUse` protects, and whether waits-on skips owners.
- **A box taller than the terminal** · elsewhere: the decider (Decision 5) · adr. Decision 5 cuts each row to the
  terminal's width, "so a redraw moves up exactly the lines it drew". The height has no bound. A cursor-up stops at
  the screen's top line, so a box taller than the screen leaves its top lines in the scrollback on every redraw. The
  guide's todo delete (30 objects and 2 kept stores) draws a box of 36 lines, which is taller than an 80×24 window. The
  decider could cap the rows, for example by folding settled rows into a count, as the ⋯ line does.
- **The repo-wide checks and the lane** · elsewhere: the PR's gate. *Done when* names `just ci`, the gate and the
  `app-todo` lane. This review ran their parts on the touched packages only, as the gate runs the rest once. Read
  against the change, the lane's assertions are substrings and prefixes that still hold
  (`e2e/app-todo.venom.yml:448-452`, `:974`), and its one deploy that has a requirement uses `--no-wait`.
- **Feature issue**: #1606. The PR that ships the work, after a passing round, carries `Fixes #1606`. Nothing is
  stamped this round.

### ✅ Verified correct (keep it)

- **Build, vet, lint**: `go vet` on `./internal/gc/ ./internal/app/ ./cmd/funcdctl/` and with `-tags dev` on
  `./cmd/funcdctl/`, exit 0. `go tool golangci-lint run` on the three packages and with `--build-tags dev` on
  `./cmd/funcdctl/`: `0 issues.` each.
- **Tests**: `go test -race ./internal/gc/ ./internal/app/ ./cmd/funcdctl/` passed (2.4 s, 19.7 s, 10.3 s).
  `go test -race -tags dev ./cmd/funcdctl/ -run 'DevLAN|Banner|AppView|AppDeploy|AppDelete'` ran 28 tests, all passed
  and none skipped. Every Scenario has its named test: `TestScenarioAppViewDeploy`, `…Lines`, `…WaitsOn`,
  `…Requires`, `…Hooks`, `…Delete` and `…Deadline`.
- **Generated files and hygiene**: rerunning the spec generator and both Spec-page tests with `-update` changed nothing
  (`scripts/check-generated.sh` exit 0). `go mod tidy -diff` and `gofmt -l` were clean, and `just check-hygiene` and
  `go run ./scripts/behavior -check` exited 0.
- **Mutants**: 35 ran through `go test -overlay`. Each one below is listed with the test that killed it.
  - The deadline never written, written once, or published as startedAt plus the timeout (S1, S2, S4):
    `TestScenarioAppViewDeadline`.
  - `uses` ignoring the protected fields, a Site protecting every blob binding instead of `blob[0]`, the addon binding
    unprotected, a self-link counted, or an add-on client copy keeping its kind (G1, G2, G4–G6):
    `TestUsesReadsTheProtectedFields`.
  - A Sensor action's Function dropped from `Names` (G7): `TestNamesCoversEveryField`.
  - `Names` listing an object twice, or naming a Workflow as a Function (G8, G10): `TestNamesInFieldOrder`.
  - Waits-on picking a settled part, or always empty (V1, V2): `TestScenarioAppViewDeploy` and `…WaitsOn`.
  - Clocks counted from the view's start, the countdown dropped, rows not cut to the width, or the redraw one line
    short (V5, V8, V13, V14): `TestScenarioAppViewDeploy`.
  - A Secret stop drawn as a plan (V6): `TestAppViewPlanFallback`.
  - A retried hook showing its first call (V10): `TestScenarioAppViewHooks`.
  - No `InUse` on delete (V12): `TestScenarioAppViewDelete`.
  - The ⋯ line never drawn (V16): `TestScenarioAppViewRequires`.
  - The ` after <d>` suffix dropped on deploy (A2): `TestCLIAppDeployFollowsRevision` and `TestScenarioAppViewLines`.
  - The ` after <d>` suffix dropped on delete (A5): `TestCLIAppDeleteReports` and `TestScenarioAddonAppDelete`.
  - S3, the deadline's equality guard removed, is equivalent: an equal value writes nothing. The 11 survivors are in
    Minor 3.
- **The deadline (Decision 5)**: `settle` writes `deadline(a, latest)` (`internal/app/revision.go:260`), the value its
  own fail decision uses in the same pass. So startedAt, a resume, a hold's release, a pre-hook's end and a lag's end
  each move it, and a final revision keeps it. The field's doc matches the Contract word for word, and the OpenAPI
  document and the Spec page carry it.
- **`InUse` is unchanged**: for each user kind, `protected` is exactly the set of fields the old `uses` read. Taking
  the first field that names an object never hides a protected field, because in each kind's order the protected
  fields come first (bindings before root refs, a Lake's blob before its location). `partKinds` covers all 14 part
  kinds that `App.entries` builds. `Unmet` is exported unchanged, and each of its answers is pinned by
  `internal/app/requires_test.go:209-223`.
- **The view**: a rounded box in the banner's palette, with `palette()` now shared by `printBanner`. The REQUIRES,
  HOOKS and PARTS sections come in that order. A hook is due until its call is recorded, and a recorded hook shows its
  Invocation's `endTime − startTime`. A Lagging part counts as settled in the bar and keeps ◐ and a running time
  (code read; see Minor 3). The deadline countdown changes to the Ready reason once the deadline has passed (code
  read). The
  delete view shows each noted object's time from the delete, waiting objects with a reason, and kept stores at the
  end.
- **Lines and exits (Decisions 6 and 7)**: off a terminal, ADR-0217's lines gain ` after <d>` when a part is Ready or
  NotStarted and `deleted … after <d>` on delete, and each line passes through `termSafe`. A requirement line prints
  `met` for `Unmet`'s empty answer. `--no-wait` and `--dry-run` return before any view is made. The loop's exits are
  untouched, and the last draw stays on screen. `funcdctl dev` passes no view (`cmd/funcdctl/dev_app.go:1143`), so it
  keeps its plain lines, in line with the Scope.
- **The dry run only on a terminal**: only the live view draws a plan line or actions (Decisions 2 and 6), so a pipe
  needs no dry run. The Consequences' "one dry-run admission pass" per deploy then applies to terminals only.
- **Docs and tracking**: the guide's App page describes the box and the lines. Its delete example now pipes through
  `cat`, which is right, because a terminal would draw the box. Its times follow the 1 s poll. The funcdctl Spec page
  is regenerated. ADR-0217 carries its `Superseded in part by` line, and F279 is at `reviewing`. Since acceptance, the
  ADR's only change is the status line. `charmbracelet/x/term` became a direct dependency at the version the
  Constraints name. `Names` lives in a new `internal/gc/names.go` rather than in `gc.go`, a split that leaves the
  collector's file to the collector.

### Definition of Done

12 of 14 items hold: the 6 Review-checklist items, "the tests pass", and 5 of the 7 applicable generic items. The two
generic items that miss are both `model`: "Contracts honoured" (Majors 1 and 2 and Minor 1) and the codebase's
conventions (Major 3, `termSafe`). `just ci`, the gate and the `app-todo` lane are left to the PR's gate and are not
counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0362 (implementation) → changes-requested, 0/3/3, 6 model-attributed, DoD 12/14. See
docs/reviews/model-scorecard.md.

### Recommendation

Loop back to `adr-impl` for Majors 1 to 3 and the three Minors. The fixes are small: move the dry run, count
`plan.Hooks`, detect a stop by its `Reason`, apply `termSafe` in the box, return an error from `done`, base the
required-App wait on `startedAt`, and add the missing tests. The three `adr` findings go to the decider and do not
block this work. The ADR stays `Reviewing` and F279 stays `reviewing`.
