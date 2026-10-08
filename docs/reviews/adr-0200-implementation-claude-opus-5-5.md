# ADR-0200 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 0 minors (ADR-0200 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0200-final`, head `dad871ee`. On top of `origin/main` (`044bfa86`), the branch holds the
ADR-0199 implementation (`2fc6d767`, already reviewed and `Implemented`), the ADR-0200 acceptance (`a6e60996`) and 7
ADR-0200 implementation commits. The ADR-0200 diff (`a6e60996..dad871ee`) is 40 files, +2861/−178, of which 19
non-test Go files carry +811/−84. `go.mod` and `go.sum` are unchanged. The tree is clean after `just ci`, whose
`generate` step rewrote the OpenAPI spec with no diff.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `git status` clean afterwards |
| `go test -tags e2e -count=1 -run TestScenarioApp -v ./pkg/funcd/` | `EXIT=0`, 25/25 `--- PASS`, 0 skipped (81.2 s): the 10 ADR-0200 Scenarios and the 15 ADR-0199 Scenarios |
| The 13 `pkg/funcd/gc_e2e_test.go` scenarios (the GC gained the pair `(App, AppRevision)`) | `EXIT=0`, 13/13 pass |
| `go test -race -count=1` on `internal/app`, `cmd/funcdctl`, `pkg/sdk`, `api/types/v1alpha1`, `internal/platform/config`, `internal/gc`, `internal/controlplane`, and the pacing tests of `cmd/funcd` and `pkg/funcd` | all `ok`, uncached |
| Nine mutants through `go test -overlay` (listed below) | 8 killed, 1 equivalent |
| `git diff a6e60996..HEAD -- docs/adr/` | one line in `docs/adr/0200-app-revisions.md`: `Accepted` → `Reviewing` |
| `docs/feat/0010-feat-apps.md` | the F114 row moved `accepted` → `reviewing`, nothing else changed |

Mutants (each fails the named test, so the guard is pinned):

- The switch no longer waits for a pass that wrote no part (`internal/app/revision.go:123`):
  `TestScenarioAppPruneAfterCurrent` fails.
- The prune gate without "current" and "wrote no part" (`internal/app/reconcile.go:202`):
  `TestScenarioAppPruneAfterCurrent` fails.
- The deadline read from `time.Now()` instead of `Deps.Clock` (`revision.go:121`): six tests fail, among them
  `TestScenarioAppFailedUpgradeKeepsServing` and `TestAppRevisionDeadlineSurvivesRestart`.
- History counts the current revision among the kept others (`revision.go:223`): `TestScenarioAppHistoryKept` fails.
- The AppRevision statuses written after a Conflict on the App status (`reconcile.go:217`):
  `TestAppRevisionConvergesAfterAFailedWrite` fails.
- A `Failed` latest revision evaluated again for a switch (`revision.go:117`): `TestScenarioAppFailedUpgradeKeepsServing`
  ("Failed is final") fails.
- `funcdctl app rollback` without the controller-UID refusal (`cmd/funcdctl/app.go:149`):
  `TestScenarioCLIAppRollback` fails.
- `WithPacing` without the `AppUpgradeTimeout <= BootTimeout` refusal (`pkg/funcd/options.go:718`):
  `TestWithPacingRefusesInvalidFields` fails.
- Equivalent: the stamp comparing the specs with `reflect.DeepEqual` instead of the bytes of `json.Marshal`
  (`revision.go:67`) passes every test. Both specs reach the reconciler decoded from the store's JSON, so an omitted
  and an empty section are already equal before the comparison. The behavior that Decision 3 requires holds; this is
  not a finding.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### ✅ Verified correct (keep it)

- **Contracts match the ADR.** `AppRevision`, `AppRevisionSpec` (`number` with `minimum: 1`, `maximum: 9999999999`
  in the OpenAPI) and `AppRevisionStatus` with `startedAt` (`api/types/v1alpha1/apprevision.go`); `AppRevisionName`;
  `Validate` checks the envelope, the number bounds and the name, not the spec. `AppStatus` gains `currentRevision`,
  `latestRevision` and `version` in the contract's order. `sdk.ReadOnlyKind` now derives from the new
  `sdk.ReadOnlyWriter`, which names "the Function reconciler" and "the App reconciler"; the Revision text is
  unchanged. `Pacing.AppUpgradeTimeout`, `WithAppRevisionHistory` (1 to 100), `app.Deps` (`Clock`, `UpgradeTimeout`,
  `RevisionHistory` with their zero defaults), the config group with only `revisionHistory` defaulted to 10, and the
  two `Handlers` methods all match.
- **The new kind is wired at every site.** `Kind.Validate`, `NewObject`, `AllKinds` (count test 27 → 28), the
  `Handlers` interface, `storeHandlers`, `StubHandlers`, `stampTypeMeta`, `registerAppRevision`, the SDK path table and
  the generated OpenAPI. `registerNamespacedRead` registers only list and get, and `registerNamespacedCRUD` now builds on
  it, so the App routes are unchanged.
- **Decision 2.** POST, PUT and DELETE answer 405 with `Allow: GET` at the handler level
  (`internal/controlplane/apprevision_test.go`) and on a running platform (e2e `app-revision-read-only`).
  `funcdctl apply -f` and `funcdctl delete apprevision` refuse with "AppRevision is read-only: the App reconciler
  writes it" before any request, and the stored revision keeps its `resourceVersion`.
- **Decision 3.** The stamp runs before ADR-0199's ownership check (`reconcile.go:176-183`), compares the bytes of
  `json.Marshal`, numbers from the latest revision of this App's UID with no gap (A→B→A gives `todo-3`), stamps a
  reordered entry, drops a namesake of a deleted incarnation at its `resourceVersion` and stops on any other owner
  with `ChildNotOwned` before any part write (`TestAppRevisionNamesake`).
- **Decisions 5 and 6.** `settle` derives the record on every pass, a stopped one too. The latest revision switches
  only in a pass that was not stopped, wrote no part and left no part Pending; the App status is written before any
  AppRevision status, and a pass that fails after any of its writes (App status, new, previous or superseded
  revision, or a Conflict on the App) converges on the next pass. Before the deadline the pass requeues for the time
  left, at least 1 ms; at the deadline the latest turns `Failed` with `ChildrenReady=False ChildNotReady` naming the
  first Pending part or the stop reason, and a part Ready later neither switches nor clears it. A new Reconciler
  keeps the deadline from `startedAt`. Every time read in `internal/app` goes through `Deps.Clock`, including each
  condition's transition time (`setCondition`, `internal/app/status.go:221`); a grep finds no `time.Now` there.
  The App phase follows Decision 6 (`appPhase`, `status.go:162`), including a failed install without
  `currentRevision`.
- **Decisions 7 and 8.** Prune runs only when the latest revision is current and the pass wrote no part; before
  that a dropped object is a `Pruning` child with reason `NotCurrent`. `sectionKinds` and the App's part watches
  leave out `AppRevision`. History keeps the current revision plus the newest `RevisionHistory` others, `Failed`
  ones counted, and deletes each other one at its `resourceVersion`. The GC pair `(App, AppRevision)` follows the App
  section pairs and precedes `(Function, Revision)` (`TestPairsOrderAppRevisionAfterTheAppSections`).
- **Decision 9.** `funcdctl app` has only `history` and `rollback`. History orders by number (10 after 2), prints
  `REVISION VERSION PHASE STAMPED`, passes the version through `termSafe`, filters by the App's UID while the App
  exists and supports `-o json`. Rollback refuses a non-positive or non-integer `n`, an absent revision (naming it
  and `app.revisionHistory`), a revision of another UID and a missing App, reports an equal spec as no change, and
  otherwise writes only the App through `sdk.Client.Apply`.
- **Decision 10.** `pacing()` refuses a set `app.upgradeTimeout` at or below `runtime.bootTimeout` with the
  contract's message and derives max(5m, 2 × `runtime.bootTimeout`) when it is unset, saturating for a very large
  boot timeout; `WithPacing` makes the same check on the effective `BootTimeout`. `funcd` exits at start with
  `app.upgradeTimeout: 1m` (e2e `app-upgrade-timeout-config`).
- **Scenarios.** Each of the 10 Scenarios has a `TestScenarioApp…` e2e test in `pkg/funcd` whose assertions follow
  the ADR text: the switch is ordered after the new Revision serves by store `resourceVersion`; the one-part change
  keeps every other part's `resourceVersion` and `todo-api`'s worker PID, and the running WorkflowRun finishes on the
  Revision it pinned; the failed upgrade fails between 20 s and 23 s after its stamp with the scenario's pacing;
  rollback lists `Ready, Ready, Failed, Ready`. Most Scenarios also have a reconciler-level unit test, and every
  item of the plan's step 3 has a named test.
- **ADR-0199 is not regressed.** Its 15 e2e Scenarios and its unit tests pass. The changed expectations follow
  ADR-0200: four `TestAppReadiness` rows now expect `Progressing` for a Deploying App (Decision 6: `ChildNotReady` is
  for `Failed` and `Degraded` only), and `TestScenarioAppPruneDroppedPart` expects reason `NotCurrent` (Decision 7).
  The `app-degraded-recovers` assertion of `ChildNotReady` is unchanged. The e2e watch helper became the shared
  `watchKind` and still fails the test when the store drops the watch.
- **Conventions.** No `any` or `interface{}` in the new APIs, no `panic`, `fmt.Print*`, `time.Sleep` or `t.Skip`,
  `log/slog` only, ctx-first, `api/fault` kinds throughout, block-style YAML in the test manifest and the commented
  example config.
- **Scope.** Nothing outside Decisions 1 to 10 and the Implementation plan was added; the read-route split and the
  shared e2e helpers serve the new kind and its scenarios.

### Definition of Done

17/17 hold: the 8 Review-checklist items, the 3 items of plan step 4 (`just ci` and `just ci-full` green, a passing
test per Scenario, no `go.mod` change), and 6 generic items (real behavior with no stubs, Contracts honoured, tree
versus the plan, conventions, scope, tracking). For `just ci-full`, this gate ran `just ci` (exit 0), the 25 App
e2e Scenarios and the 13 GC e2e scenarios. The full `test-e2e` lane runs once at the PR gate
(`scripts/agent/gate.sh`) and was not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0200 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 17/17. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0200 moves `Reviewing → Implemented` and FEAT-0010 F114 `reviewing → implemented` in this commit. The
main session moves the board card to Done. The PR gate still owes the full `just ci-full` run.
