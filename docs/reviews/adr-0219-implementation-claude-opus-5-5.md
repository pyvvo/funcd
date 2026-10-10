# ADR-0219 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 4 minors (ADR-0219 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0219`, head `ad8c587c`, on `origin/main` at `9077c8f2`. Six commits: the types, the wait with
the admission, the unit tests, the e2e tests, the removal of a log line, and the status bump. The diff is 15 files,
+1576/−34. The non-test Go code is `api/types/v1alpha1/{app,apprevision}.go`, `internal/app/{requires,reconcile,revision}.go`,
`internal/controlplane/admission/links.go` and `pkg/funcd/funcd.go`, plus the generated OpenAPI. `go.mod` and `go.sum`
are unchanged.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed one |
| `just test-e2e` (the second half of `just ci-full`) | `EXIT=0`, `ok` for `./pkg/funcd` (served from the Go test cache for this exact tree) |
| `go test -tags e2e -race -count=1 -run TestScenarioAppRequires -v ./pkg/funcd` | `EXIT=0`, 6/6 Scenarios `--- PASS`, 0 skipped, no data race; the slowest is `TestScenarioAppRequiresWaits` at 4.35 s, which holds the wait for 4 s by design |
| `go test -tags e2e -count=1 -run 'TestScenarioApp\|TestApp' ./pkg/funcd` | `EXIT=1` (210 s): every App e2e test of ADR-0199 to ADR-0219 passes except one `--- FAIL` of `TestScenarioAppIdleFunctionStaysCurrent`, a pre-existing test race (Minor 4). Reruns of that test: 1 failure in 5, then 0 in 20 on this branch; 0 in 28 on the base tree |
| `go test -race -count=1` of `./internal/app`, `./api/types/v1alpha1`, `./internal/controlplane/...` | all `ok`, uncached |
| `go test -race -count=3 -run 'Require\|MapRequires' ./internal/app` | `ok`, no flake in 3 runs |
| 11 mutants through `go test -overlay` (listed below) | 9 killed, 2 survived (1 equivalent) |
| `scripts/agent/audit.py --base 9077c8f2 --head HEAD --report-only` | `PASS`, 0 hard flags; soft flags under Minor 2 |
| `git diff 9077c8f2..HEAD -- docs/` | `docs/adr/0219-app-requirements.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F122 status cell, `accepted` → `reviewing` |
| `grep -rn 'func findCycle\|func FindCycle'` | one definition, `internal/controlplane/admission/links.go:81` |

Mutants. Each killed mutant fails the named tests:

- The stamp always sets `startedAt` (`internal/app/revision.go:102`): `TestScenarioAppRequiresWaits`,
  `TestScenarioAppRequiresVersion`, `TestScenarioAppRequiresAnyVersion`, `TestAppRequirementMessages` and others fail.
- `settle` without the `startedAt` guard, with `deadline()` filling a nil `startedAt` again (`revision.go:142`): the
  same four tests fail.
- `stop.msg()` always prefixes `<Kind>/<Name>: ` (`internal/app/reconcile.go:209`): the same four tests fail.
- The admission treats every dependent as started (`internal/app/requires.go:272`):
  `TestRequiresAdmissionStartedDependentsOnly` fails on the waiting and the created-paused dependents.
- `MapRequires` without the `status.requiredBy` direction (`requires.go:152`): `TestMapRequires` fails.
- No "has not rolled out its spec yet" check (`requires.go:80`): two `TestAppRequirementMessages` cases fail.
- `Matches` with the lenient `semver.NewVersion` (`api/types/v1alpha1/app.go:93`): `TestAppRequirementMatches` fails.
- The met pass does not store `startedAt` (`requires.go:117`): `TestAppRequiresStartedAtBeforeAnyPartWrite` and both
  `TestAppRequiresStartedAtUpdateFails` cases fail.
- The delete refusal skips a requirement without a range (`requires.go:191`): `TestRequiresAdmissionRefusesDelete`
  fails.
- Survivors: the `slices.Sort` of `status.requiredBy` (`requires.go:51`) is equivalent in the tests, because the store
  lists the Apps of a namespace in name order. The removal of the App watch registration (`pkg/funcd/funcd.go:1063`)
  is under Minor 1.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **No test fails when the App watch is not registered** · attribution: `model`. Review-checklist item 8 holds in
   the code: `pkg/funcd/funcd.go:1063` registers `ctrl.Watches(v1.KindApp.GVK(), appReconciler.MapRequires)`, and
   `TestMapRequires` tests the map function. But an overlay that removes the registration passes every test. With the
   watch removed, `TestScenarioAppRequiresVersion` still passes, in 10.07 s instead of 0.31 s: the waiting `billing`
   is requeued by the 10 s supervision period (`internal/controller/controller.go:37`), which is shorter than the
   15 s `requiresWithin` bound (`pkg/funcd/app_requires_e2e_test.go:26`). Decision 6 exists to make a dependent react
   to the required App at once. Fix: in the version scenario, assert that `billing` rolls out within a bound below
   the supervision period after `lakehouse` 2.1.0 is current and Ready.
2. **Complexity growth and a second copy of an e2e retry helper** · attribution: `model`. The bloat audit reports
   soft flags only: `(*Reconciler).Reconcile` grows to gocyclo 36 (from 32), gocognit 37 (from 32) and 67 statements
   (`internal/app/reconcile.go:223`), and the new `(requiresAdmission).Admit` has gocyclo 21
   (`internal/app/requires.go:179`). The e2e helper `update` (`pkg/funcd/app_requires_e2e_test.go:62-71`) repeats
   `applyRetrying` (`pkg/funcd/nested_cap_e2e_test.go:100-107`), with a deadline added, for any kind. Fix (optional):
   split `Admit` into a delete check and an update check, and make `applyRetrying` generic with a deadline so that both
   tests use it.
3. **The admission and the reconciler use two signals for "started"** · attribution: `adr`. Decision 4 makes
   `startedAt` the point after which requirements no longer gate a revision. Decision 7 reads "not started" from the
   dependent App's stored status: no `status.currentRevision`, and `Ready` reason `RequirementNotMet` or no phase. The
   reconciler stores `startedAt` (at the stamp, or in the met pass at `internal/app/requires.go:115-117`) before it
   writes the App status in the same pass (`publish`). In that window, which is one pass long, the stored App still
   reads as waiting or not reconciled. An upgrade of the required App out of the dependent's range is then admitted,
   and the started revision rolls out against it. The implementation follows the ADR exactly. The window is short and
   was not reproduced. If a later ADR revisits the rule, it can read the latest AppRevision's `startedAt` instead of
   the App status.
4. **A pre-existing e2e test fails now and then** · attribution: `env` (not in this change).
   `TestScenarioAppIdleFunctionStaysCurrent` (`pkg/funcd/app_ready_e2e_test.go:71-76`, added with ADR-0199 and
   ADR-0200 in #851) failed once in the uncached App e2e run and once in 5 reruns, with `context canceled` from the
   `get App/todo` inside `require.Never`. testify v1.11.1's `Never` returns when its timer fires while a condition
   call still runs in its own goroutine. The test then ends, its cleanup stops the platform, and the late `Get` fails
   the test. The log shows `platform stopping` 40 ms before the failure. This branch does not touch the test, and the
   mechanism does not depend on it: the test passed 20 of 20 more runs on this branch and 28 of 28 on the base tree.
   Fix (a separate issue): make the condition return false on a cancelled context, or wait with a loop that does not
   leave a call running.

### ✅ Verified correct (keep it)

- **Contracts match the ADR.** `AppSpec.Requires []AppRequirement` right after `Paused`; `AppRequirement{App
  ObjectName, Version string}`; `AppStatus.Requires []AppRequirementState` and `RequiredBy []ObjectName`;
  `AppRequirementState{App, Version, Met}` with `met` always marshalled; `(AppRequirement).Matches`;
  `admission.FindCycle`; `app.NewRequiresAdmission(admission.StoreReader) admission.Admission` named `app-requires`;
  `(*Reconciler).MapRequires(ctx, v1.Object) []controller.Request`. The `Version` and `StartedAt` comments carry the
  ADR's wording. The three refusal messages match the Contracts table verbatim, and so do the e2e assertions.
- **Decision 1.** `Matches` returns true for an empty range and otherwise needs `semver.StrictNewVersion` and
  `Constraints.Check`. The table covers `""`, `beta`, `2.1.0`, `1.9.0`, `3.0.0`, `1.2`, `v2.1.0` and `2.1.0-rc.1`.
  `validateRequires` refuses an empty or invalid `app`, a repeated `app` and a range that `semver.NewConstraint`
  refuses. `spec.version` stays free.
- **Decisions 2 and 5.** `unmet` checks the five conditions in the ADR's order and builds the message with
  `any version` for an empty range and `none` for an unset phase. A required App that is Ready at an older
  `status.version`, or whose latest revision is not current, is not met. `status.requires` follows `spec.requires`
  order with the required App's `spec.version`. `status.requiredBy` is sorted. Both are set on every pass that is
  neither paused nor held, a stopped pass included, and a paused pass lists no App (`TestAppRequiresPausedKeepsStatus`
  counts the Lists).
- **Decisions 3 and 4.** `Reconcile` lists the namespace's Apps once, after the hold and pause checks and before the
  stamp. The stamp sets `startedAt` only when every requirement is met. `wait` stores `startedAt` from the injected
  clock with an AppRevision update before `apply`, continues with the returned revision, and requeues with no part
  written on a `Conflict`. Otherwise it returns the `RequirementNotMet` stop as `apply`'s `halt`, so the ownership
  check, the Secret check and the pre-hooks are never reached (`TestAppRequiresWaitBeforeOwnershipAndSecrets`).
  `settle` neither switches nor fails a revision without `startedAt`, and `deadline()` no longer fills it; its one
  caller is guarded. A hold's `ReleasedAt` starts no deadline. A superseded waiting revision turns `Failed`
  `Superseded`. A requirement lost after the start changes nothing. A new Reconciler reads the stored `startedAt`.
- **Decision 6.** `MapRequires` requeues the Apps that require the changed App, the Apps it requires and the Apps
  whose `status.requiredBy` names it, never the App itself, and the test covers a rename, a removal and a delete.
- **Decision 7.** `app-requires` is Validating, handles App Create, Update and Delete only, and reads the namespace
  (`ReadsNamespace` true). It is registered after `app-parts` (`pkg/funcd/funcd.go:1127-1129`). The upgrade check
  skips a dependent without `status.currentRevision` whose `Ready` reason is `RequirementNotMet` or whose phase is
  unset, and blocks one in its first rollout. A dependent without a range never blocks an upgrade but blocks a delete.
  The cycle check uses the stored edges plus the incoming App's own, so a self-requirement is a cycle. A missing
  required App is never refused.
- **The six Scenarios.** Each has a `TestScenarioAppRequires…` e2e test in `pkg/funcd` that calls `t.Parallel()` and
  scales the 2m `app.upgradeTimeout` and 1m `runtime.bootTimeout` down to 3 s and 1 s; the first three also have a
  reconciler-level test of the same name in `internal/app` on `clock.NewManual`. The upgrade scenario runs the rollback
  through `funcdctl app rollback`, and the delete and cycle scenarios check the HTTP status (409, 400) and that nothing
  is stored. The fixture's `lakehouse` declares the Bucket `lake` in place of the catalog `lake`; the test comment
  gives the reason (a CatalogService needs the duckdb provider container), and the requirement rules do not depend on
  the part's kind.
- **Constraints and scope.** Every time is read through the injected clock (no `time.Now` in non-test code). A
  repeated pass writes nothing (`quietPass` for both Apps). No config key and no new dependency:
  `Masterminds/semver/v3` v3.5.0 was already a direct requirement on the base, so the plan's `go.mod` step had nothing
  to do. The `internal/app/hooks_test.go` change only makes the test hook fire on the AppRevision List, because each
  pass now lists Apps first; it relaxes no assertion.
- **Tracking.** The ADR's substance is unchanged: the only edit is the status line. ADR-0199 and ADR-0200 already
  carry their "Superseded in part by ADR-0219" back-links.

### Definition of Done

19/19 hold: the 10 Review-checklist items, 3 items of plan step 5 (`just ci` and `just ci-full` green, a passing test
per Scenario, no `go.mod` change), and 6 generic items (real behavior with no stubs, Contracts honored, tree against
the plan, conventions, scope, tracking). Checklist item 8 holds in the code; Minor 1 is about its test. For
`just ci-full`, `just test-e2e` exits 0 for this tree, and the one failure of the uncached App e2e run is the
pre-existing race of Minor 4.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0219 (implementation) → pass, 0/0/4, 2 model-attributed, DoD 19/19. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0219 moves `Reviewing → Implemented` and FEAT-0010 F122 `reviewing → implemented` in this commit. The
two `model` Minors are small follow-ups for the builder: a tighter bound in the version scenario, and an optional
split of `Admit` with one shared e2e retry helper. Minor 3 is an input for any later ADR that revisits the admission
rule. Minor 4 needs its own issue for the flaky test. The main session moves the board card to Done.
