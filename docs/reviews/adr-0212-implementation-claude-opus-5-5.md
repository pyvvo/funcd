# ADR-0212 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 3 minors (ADR-0212 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0212`, head `f3fab6a1`, on `origin/main` at `0cd67ef5` (the acceptance of ADR-0211 to
ADR-0220). Seven commits: types, server, unit tests, CLI, e2e, the status bump, a comment reflow. The diff is 15
files, +1249/−56; the non-test Go code is `api/types/v1alpha1/app.go`, `internal/app/{reconcile,revision,status}.go`
and `cmd/funcdctl/app.go` (+255/−56 with the generated OpenAPI). `go.mod` and `go.sum` are unchanged.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed one |
| `go test -tags e2e -race -count=1 -run '^TestScenarioApp' -v ./pkg/funcd` | `EXIT=0`, 31/31 `--- PASS`, 0 skipped, no data race (155 s): the 6 ADR-0212 Scenarios, the 10 of ADR-0200 and the 15 of ADR-0199 |
| `go test -race -count=1 ./internal/app ./internal/gc ./api/types/v1alpha1 ./cmd/funcdctl` | all `ok`, uncached |
| 15 mutants through `go test -overlay` (listed below) | 13 killed, 2 equivalent |
| `git diff 0cd67ef5..HEAD -- docs/` | `docs/adr/0212-app-self-heal-and-pause.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F115 status cell, `accepted` → `reviewing` |

Mutants (each fails the named tests, so the guard is pinned):

- `heal` always true (`internal/app/reconcile.go:203`): `TestScenarioAppRolloutIsNotSelfHeal`,
  `TestAppRetryOfAStoppedRolloutIsNotSelfHeal` and `TestAppWriteBackAfterFailed` fail.
- `drift` always true, or ignored at the record (`reconcile.go:358`, `:326`): `TestScenarioAppRolloutIsNotSelfHeal`
  fails (the resource-group and owner-reference rewrites).
- No paused pass (`reconcile.go:188-190`): `TestAppPausedPassWritesNothing`, `TestScenarioAppPausedKeepsHotfix` and
  `TestAppCreatedPausedInstallsNothing` fail.
- The paused pass requeues: `TestAppPausedPassWritesNothing` and `TestScenarioAppPausedRolloutFullTimeout` fail.
- No resume (`reconcile.go:191`): `TestScenarioAppPausedKeepsHotfix`, `TestScenarioAppApplyWithoutPausedResumes` and
  `TestScenarioAppPausedRolloutFullTimeout` fail.
- `lastSelfHeal` takes the first healed part instead of the last (`reconcile.go:208`): `TestAppWritesBackAHandEdit`
  fails.
- `deadline` without `resumedAt`, or without `ReleasedAt` (`internal/app/revision.go:175-183`):
  `TestScenarioAppPausedRolloutFullTimeout` fails.
- `appPhase` with the old generation rule back (`internal/app/status.go:188`): `TestAppDegradedStaysDegradedAcrossAPause`
  and `TestAppStampStoppedByANamesakeDegrades` fail.
- Rollback drops `paused`, `sameAppSpec` compares the pause, `applyAppChange` without the retry
  (`cmd/funcdctl/app.go:163`, `:177`, `:228`): `TestCLIAppRollbackKeepsPaused` (twice) and
  `TestCLIAppPauseRetriesAConflict` fail.
- Equivalent: the stamp comparing `a.Spec` instead of `WithoutPause()` (`revision.go:58`) and the frozen copy taking
  `a.Spec` (`revision.go:96`) pass every test. The stamp runs only in a pass that is not paused, where `Paused` is
  false and `omitempty` drops the key, so both forms give the same bytes. Decision 3 holds through the paused pass's
  early return; `WithoutPause()` there is what the Contracts require and keeps it true if the order ever changes.
  This is not a finding.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **A second retry bound, `appApplyAttempts`** · attribution: `model`. `cmd/funcdctl/app.go:215` declares
  `const appApplyAttempts = 5` beside the existing `devApplyAttempts = 5` (`cmd/funcdctl/dev.go:113`). ADR-0210
  Decision 4, which this ADR's Decision 9 builds on, names that bound for every read-change-apply retry ("the
  `devApplyAttempts` bound"). Two constants with one meaning can drift apart. Fix: reuse `devApplyAttempts`, or
  rename it to one shared bound that the dev re-apply and `applyAppChange` both read.
- **`funcdctl app resume --help` says it sets `spec.paused`** · attribution: `model`. `cmd/funcdctl/app.go:194`
  builds one `Short` for both verbs, so `resume` reads "Resume an App (sets spec.paused: a paused App writes no
  part)", while resume clears the field. `workflowPauseCmd` uses the neutral "patches spec.paused". Fix: a neutral
  wording, or one text per verb.
- **The ADR's Scope puts the pause retry out, while Decision 9 and the plan require it** · attribution: `adr`. Scope
  lists "the conditional write of pause and resume and its retry (ADR-0210 Decision 4)" as out. Decision 9, plan
  step 4 ("retry a conflict") and the Review checklist ("through ADR-0210's helper") require the retry through a
  helper that ADR-0210 has not built (ADR-0210 is Accepted, not implemented). The model built the helper,
  `applyAppChange` (`cmd/funcdctl/app.go:217-233`), and tested it with an injected transport. That is the right
  reading, and it is not idle today: the PUT path reads the stored version and updates at it
  (`internal/controlplane/handlers.go:218`, `:238`), so a reconciler status write in between already answers 409.
  No rework for the model. When ADR-0210 is implemented, it should move `workflow pause|resume|cancel` and
  `app rollback` onto this helper instead of adding a second one.

### ✅ Verified correct (keep it)

- **Contracts match the ADR.** `AppSpec.Paused` follows `Version` with `json:"paused,omitempty"`;
  `WithoutPause()` has a value receiver and clears only `Paused` (`TestAppSpecWithoutPause` checks the rest of a full
  spec with `reflect.DeepEqual`, and that the receiver keeps its value); `AppStatus.LastSelfHeal *AppSelfHeal` and
  `AppSelfHeal{Kind, Name, At Timestamp}` carry the contract's JSON names, and `at` marshals as UTC milliseconds
  from a CEST time (`TestAppSelfHealJSON`). The OpenAPI gains `paused`, `lastSelfHeal` and `AppSelfHeal` with the
  ADR-0196 timestamp pattern. `condPaused`, `reasonPaused`, `reasonResumed`, the `apply` signature with `heal` and
  the healed list, `deadline(a, rev)`, `resumedAt(a)` and `appPauseCmd(verb, paused)` match the Contracts.
  `Deps.Hold` is ADR-0206's inline `interface{ Held() bool; ReleasedAt() time.Time }`, and the Reconciler keeps
  only its `ReleasedAt` view (`reconcile.go:61`), so no code path can call `Held()`.
- **Decision 1.** `healing` reads the latest revision's `Applied` from the `stored` snapshot taken after the stamp,
  so a stamping pass never records; `write` reports `drift` only for a create or a differing spec, so a pure
  owner-reference or resource-group rewrite does not record. The tests cover the stamping pass, the retry of a
  `ChildInvalid` stop on a `Deploying` revision, a write-back after `Failed` with `Applied` True and False, the
  resource-group and owner-reference rewrites, and a hand-edited `ref` object that keeps its `resourceVersion`.
- **Decision 2.** One Info `self-healed` line per healed part, right after the write, with exactly `component`,
  `kind`, `namespace`, `name` and `app` (the tests compare whole attribute maps, so no image value can slip in); one
  `at` per pass from `r.clock.Now()`; the later part in section order wins (`TestAppWritesBackAHandEdit`: Function
  then Route). Nothing clears `lastSelfHeal`, and a repeated pass writes nothing (`h.quiet()`).
- **Decisions 3 and 9.** Pause and resume stamp nothing and take no history slot; the frozen `spec.spec` holds no
  `paused` key; rollback copies `spec.spec` and keeps the App's `paused`, and `sameAppSpec` compares
  `WithoutPause()`, so a paused App already on the revision's spec reports no change and writes nothing.
  `funcdctl app pause|resume` change only `spec.paused`, print `paused <app>` / `resumed <app>`, default `-n` to
  `default`, and retry a Conflict at most 5 times (`TestCLIAppPauseRetriesAConflict`: 2 Conflicts then the write;
  5 Conflicts then `fault.Conflict` with nothing written). The group's `Short` lists `pause|resume`.
- **Decision 4.** The paused pass returns before `revisions`, reads only the App, sets `Paused=True SpecPaused`
  with the contract's message and the condition's own `observedGeneration`, writes only the App status and returns
  `Result{}`. `TestAppPausedPassWritesNothing` holds the whole list: no create, update or delete of a part after a
  hand edit, a deleted Route and a dropped Workflow; no stamp on a spec change; no `Failed` an hour past the
  deadline; no AppRevision write; no history deletion with `RevisionHistory` 1; phase, `Ready` and
  `status.observedGeneration` kept; a repeated pass keeps every `resourceVersion`. An App created paused installs
  nothing and has no phase. `internal/gc` collects a deleted paused App's tree and keeps the retained store.
- **Decisions 5 and 6.** The first pass not paused sets `Paused=False Resumed` before the stamp, its transition time
  from `Deps.Clock`; the condition stays False afterwards and is absent on an App never paused. `deadline` takes the
  latest of `startedAt`, `resumedAt` and `ReleasedAt`; a release before the stamp does not count, a resume and a
  later release each give a full timeout, the revision fails at exactly the deadline, and a revision stamped later
  keeps its `startedAt` while a `Failed` one stays `Failed`.
- **Decision 7.** Once the latest revision is current, a Pending part or a stopped pass gives `Degraded` whatever
  the generation; a `Degraded` App stays `Degraded` across a pause and a resume, and a stamp stopped by a foreign
  namesake reads `Degraded` with `ChildNotOwned`. ADR-0199's and ADR-0200's e2e Scenarios are not regressed.
- **Decision 8.** `internal/app` imports nothing new and adds no hold check; nothing reads or writes `spec.paused`
  outside the pause code, the CLI and the stamp.
- **Scenarios.** Each of the 6 Scenarios has a `TestScenarioApp…` e2e test in `pkg/funcd/app_pause_e2e_test.go`
  whose assertions follow the ADR text: the 5 s write-back with one line and `lastSelfHeal` for the edit and for the
  delete; the hot fix kept for 10 s while paused, history `1` before and after, the declared spec back and one line
  after the resume; no line on a rollout or a resource-group move of every part; nothing stamped while paused with
  `paused: true` in the manifest, `todo-2` serving after the resume; an apply without `paused` resuming and rolling
  out; `todo-2` `Deploying` through 30 s of pause and `Failed` between 20 s and 23 s after the resume, with its
  `startedAt` kept. Five of them also have a reconciler-level twin, and every item of plan step 4 has a named test.
- **Conventions.** No `any` or `interface{}` beyond the inline `Hold` interface the ADR prescribes, no `panic`,
  `fmt.Print*`, `t.Skip` or production `time.Sleep`, `log/slog` only, ctx-first, `api/fault` kinds throughout, and
  every time in `internal/app` from `Deps.Clock`.
- **Scope.** Nothing outside Decisions 1 to 9 and the Implementation plan was added; `app retry`, `app test`,
  `app deploy`, the hold wiring and the dry run stay with their own ADRs.

### Definition of Done

20/20 hold: the 11 Review-checklist items, 3 items of plan step 5 (`just ci` green, a passing test per Scenario, no
`go.mod` change), and 6 generic items (real behavior with no stubs, Contracts honoured, tree versus the plan,
conventions, scope, tracking). For `just ci-full`, this gate ran `just ci` (exit 0) and the 31 App e2e Scenarios
with `-race`. The full `test-e2e` lane runs once at the PR gate (`scripts/agent/gate.sh`) and was not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0212 (implementation) → pass, 0/0/3, 2 model-attributed, DoD 20/20. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0212 moves `Reviewing → Implemented` and FEAT-0010 F115 `reviewing → implemented` in this commit. The
two `model` Minors (one retry bound, the resume help text) are small follow-ups for the builder; the `adr` Minor goes
to ADR-0210's implementation, which should reuse `applyAppChange`. The main session moves the board card to Done.
The PR gate still owes the full `just ci-full` run.
