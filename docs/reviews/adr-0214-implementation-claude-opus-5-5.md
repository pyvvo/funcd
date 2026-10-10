# ADR-0214 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 4 minors (ADR-0214 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0214`, head `7be2916f`, on `origin/main` at `f5ba199f`. Eight commits: the types, the hook
runner, the unit tests, the retry route, the wiring with the SDK and CLI, the e2e tests, the status bump and a
parallel e2e change. The diff is 25 files, +2537/−60. The non-test Go code is `api/types/v1alpha1/{app,apprevision}.go`,
`internal/app/{hooks,reconcile,revision,status}.go`, `internal/controlplane/{appretry,server}.go`, `internal/gc/gc.go`,
`pkg/funcd/funcd.go`, `pkg/sdk/sdk.go` and `cmd/funcdctl/app.go`, plus the generated OpenAPI. `go.mod` and `go.sum`
are unchanged.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed one |
| `go test -tags e2e -race -count=1 -run 'TestScenarioAppPreHook\|TestScenarioAppPostHook\|TestScenarioAppHook' -v ./pkg/funcd` | `EXIT=0`, 5/5 `--- PASS`, 0 skipped, no data race (16 s) |
| `go test -tags e2e -count=1 -run 'TestScenarioApp\|TestApp' ./pkg/funcd` | `EXIT=0`, `ok` (198 s): every App e2e test of ADR-0199, ADR-0200, ADR-0212, ADR-0213 and this ADR, so the hooks regress none of them |
| `go test -race -count=1` of `./internal/app`, `./internal/controlplane`, `./internal/gc`, `./api/types/v1alpha1`, `./pkg/sdk`, `./cmd/funcdctl` | all `ok`, uncached |
| `go test -race -count=4 -run 'Hook\|Retry\|TestScenarioApp' ./internal/app` | `ok` (38 s), no flake in 4 runs |
| 22 mutants through `go test -overlay` (listed below) | 19 killed, 3 survived |
| `scripts/agent/audit.py --base f5ba199f --head HEAD --report-only` | `PASS`, 0 hard flags; soft flags listed under Minor 3 |
| `git diff f5ba199f..HEAD -- docs/` | `docs/adr/0214-app-hooks.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F117 status cell, `accepted` → `reviewing` |

Mutants. Each killed mutant fails the named tests:

- All parts written before the pre-hooks (`internal/app/reconcile.go:249`): `TestScenarioAppPreHookMigrates`,
  `TestAppPreHookPartsFirst`, `TestScenarioAppPreHookFailsThenRetry` and four others fail.
- A pre-hook call that ignores `busy` (`internal/app/hooks.go:227`): `TestAppCallRecordedAfterBusyReadNotRepeated`
  fails. A pre-hook call in a stopped pass: `TestAppStoppedPassStartsNoHookCall` and
  `TestAppDeadlineFailedRevisionCallsNoHook` fail.
- Prune without the post-hook gate (`reconcile.go:279`): `TestScenarioAppPostHookFails` fails.
- A busy or starting pass failed by the deadline (`internal/app/revision.go:149`): `TestScenarioAppHookRepeatsAfterRestart`
  and `TestAppFailedRecordWriteCallsAgain` fail. No `endTime` deadline term (`revision.go:204`): three tests fail.
- A switch before the pre-hooks are done (`revision.go:143`): four tests fail. No `HookFailed` for a Function not ready at
  the deadline (`revision.go:150`): `TestAppHookFunctionNotReadyFailsRetryably` fails.
- A held pass not skipped (`reconcile.go:215`), a call recorded while held (`hooks.go:373`) or while paused
  (`hooks.go:378`): `TestAppHeldOrPausedRecordsNothing` fails.
- A `Ready` retry that does not reopen the revision (`hooks.go:397`): `TestScenarioAppPreHookFailsThenRetry` and
  `TestAppHookFunctionNotReadyFailsRetryably` fail. A `Conflict` not retried (`hooks.go:400`): three tests fail.
- `Retry` without the reservation (`hooks.go:443`): `TestAppConcurrentRetriesStartOneCall` fails.
- No `rollback` event (`hooks.go:72`): `TestScenarioAppHookEvent` fails. `spec.timeout` ignored (`hooks.go:340`):
  `TestAppHookCallBounds` fails.
- A failed post-hook that does not degrade the App (`internal/app/status.go:201`): `TestScenarioAppPostHookFails` and
  `TestAppPostHooksInOrder` fail.
- No pre-hook link refusal (`api/types/v1alpha1/app.go`, `validateHooks`): `TestAppValidateHookRefusals` fails. No
  `(AppRevision, Invocation)` pair (`internal/gc/gc.go:43`): `TestPairsOrderHookInvocationAfterAppRevision` and three
  GC tests fail.
- Survivors: the `wrote == nil` term of a pre-hook call (`hooks.go:227`) and the `Requeue` of the switching pass
  (`reconcile.go:297`), both under Minor 1. The third survivor, a call recorded after funcd's shutdown ended it
  (`hooks.go:355`), is equivalent in the tests: the memory engine refuses a cancelled context
  (`internal/store/memory/memory.go:34`), so the record fails anyway. The guard is defensive, which is acceptable.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **Two Decision 4 rules have no test** · attribution: `model`. The code is correct, but no test fails when either
   rule is removed. (a) A pre-hook call requires a pass that wrote no part (`internal/app/hooks.go:227`, Review
   checklist line 2). The mutant without `wrote == nil` passes the `internal/app` suite. No e2e test covers it either,
   because every tested pass that writes also rewrites the hook Function, and a rewritten Function is not ready.
   Without the rule, an upgrade that changes only a store the hook binds, such as a new table on `todo-store`, would
   call an unchanged and `Ready` hook before the store change is applied. (b) The pass that switches returns `Requeue`
   (`internal/app/reconcile.go:297`, "The pass that switches returns `Requeue`"). The mutant without the requeue
   passes, because the unit harness drives the passes by hand. Fix: add a unit test in which a pass writes only a
   bound store and starts no call, and assert `Result.Requeue` on the switching pass of a hooked App.
2. **A test for a neighboring ADR is in this change** · attribution: `model`.
   `TestAppFunctionServingUnderGateIsChildNotReady` (`internal/app/reconcile_test.go:594-610`) tests ADR-0221, which is
   already Implemented. Commit `e06a9f94` adds it next to the hook tests. The test is harmless and its commit message
   names it, but it is outside ADR-0214's Implementation plan. Fix: move it to its own change, or leave it and note it
   in the PR description.
3. **Bloat-audit soft flags** · attribution: `model`. `(*Reconciler).Reconcile` grows to gocyclo 32 (from 22),
   gocognit 32 and 59 statements (`internal/app/reconcile.go:214`), and `settle` reaches gocyclo 24
   (`internal/app/revision.go:117`). `internal/app/hooks.go:488` adds a third copy of `randHex`, next to
   `internal/sensor/sensor.go:619` and `internal/edge/observ/observ.go:150`. None of these is a hard flag. Fix
   (optional): move the hook branch of `Reconcile` (the `writes` choice, the `hooks` call and the switch requeue) into
   one helper.
4. **The restart Scenario restarts with the default `app.upgradeTimeout`** · attribution: `adr` (plausible, not
   reproduced). `TestScenarioAppHookRepeatsAfterRestart` (`pkg/funcd/app_hooks_e2e_test.go:314-357`) runs the
   interrupted call past a 10 s `app.upgradeTimeout`, which matches the Scenario. The restarted funcd then runs with
   the default timeout. The test's comment explains why: Decision 4 fails a due pre-hook whose Function is neither
   `Ready` nor `NotStarted` at the deadline, and it has no exception for the first pass after a restart. The restart
   path past the deadline is covered in `internal/app` (`TestScenarioAppHookRepeatsAfterRestart`, `Advance(2m)` past a
   1-minute timeout, with the Function `Ready`). For this review, an overlay kept the 10 s timeout after the restart,
   and the e2e test passed 4 of 4 runs when run alone. The race is therefore possible under the rule as written, but
   it was not observed. If a later ADR revisits the deadline, it can make the boot time a start term of the deadline,
   as `ReleasedAt` is.

### ✅ Verified correct (keep it)

- **Contracts match the ADR.** `AppSpec.Hooks *AppHooks` after the ADR-0213 sections; `AppHooks{PreApply, PostApply}`,
  `AppHook{Function}`, `AppHookInput{Event, App, From, To, FromVersion, ToVersion}` with `event` as an enum;
  `AppRevisionSpec.HookInput` and `AppRevisionStatus.Hooks []AppHookCall{Point, Function, Invocation, Phase, EndTime}`;
  `internal/app.Invoker` with the Sensor's signature; `Deps` gains `Invoker`, `InvokeTimeout` (0 ⇒
  `v1.DefaultInvokeTimeout`, negative refused) and `Enqueue`; `Deps.Hold` is the inline ADR-0206 interface;
  `(*Reconciler).Retry`; `controlplane.AppRetrier` and `RegisterAppRetry` (POST `…/apps/{name}/retry`, operation
  `retryApp`, `RejectUnknownQueryParameters`, `auth.VerbUpdate` on App); `sdk.Client.RetryApp`; `funcdctl app retry
  <app> [-n ns]`, added to the group's `Short` next to the existing verbs; `gc.Pairs()` holds `(AppRevision, Invocation)`
  right after `(App, AppRevision)`.
- **Decision 1.** `validateHooks` refuses a name that is not a functions entry, a `ref` entry, a name repeated in one
  list, and a pre-hook that links to a non-`ref` entry that is not a pre-hook, each with the ADR's message. It accepts
  one name in both lists, a link to a `ref` entry, a link to another pre-hook, and a link outside the App.
- **Decision 2.** Only the stamp sets `spec.hookInput` (`internal/app/revision.go`, through `hookInput`). The value
  is `install` when there is no current revision, and `rollback` when the declared spec equals a retained `Ready`
  revision's `spec.spec`, compared byte for byte with the stamp's `json.Marshal`. Otherwise it is `upgrade`, and a
  rollback beyond the history reads `upgrade`. The e2e tests check all three inputs end to end through
  `context.kv`.
- **Decision 4.** While the pre-hooks are not done, a pass writes only `preHookParts`: in section order, the
  ConfigMaps, KV stores, Buckets and CatalogServices that the pre-hooks bind, plus the catalog's own buckets and
  ConfigMaps, then the pre-hook Functions. The ownership check and the Secret check still read every part first.
  The switch requires `pre == nil`. The deadline's start terms include the last `preApply` `endTime`. A `busy` or
  starting pass fails nothing by the deadline. A due pre-hook whose Function is not ready at the deadline gives a
  retryable `HookFailed`. A post-hook failure makes the App `Degraded` `HookFailed`, ranked after a stop. Prune waits
  for every post-hook, and a dropped object meanwhile shows as `Pruning` with no reason. The e2e test shows that
  `todo-api` changes only after the call is recorded (resourceVersion order).
- **Decisions 5 and 6.** The CloudEvent fields match. The call runs in a goroutine under the controller's run context.
  Its deadline is the Function's `spec.timeout` or `invoke.defaultTimeout`. The Invocation carries the AppRevision's
  controller reference and `Deps.Clock` times. The `AppHookCall` is appended at the revision's resourceVersion and is
  re-read on a `Conflict`. A `Ready` pre-hook call reopens a `HookFailed` revision to `Deploying`. Nothing is recorded
  after a shutdown, while funcd is held, while the App is paused, or when the revision is no longer the latest. The
  App is released and then requeued.
- **Decisions 7 and 8.** `Retry` checks the hold, reserves the App before it reads the revisions, and releases the
  App when it starts nothing. It starts only the first not-done pre-hook of a `Failed` `HookFailed` latest revision,
  or the first failed post-hook of a current revision. Otherwise it answers `Conflict` with the ADR's messages,
  `Unavailable` while held, or `NotFound`. `Reconcile` asks `Held()` first, before the paused check. `serve` passes no
  hold yet, because the hold gate belongs to ADR-0206, which is not implemented. The ADR allows nil here.
- **Constraints.** `internal/app` imports no Workflow, engine or run-store package (`go list` of its imports). The
  hook code reads no `time.Now`. No `Invoke` runs on the controller worker. `pkg/funcd` builds a second
  `sensor.HTTPInvoker` with `workerClient(calls, 0)`. There is no shim, `go.mod` or config-key change.
- **Tracking.** The ADR's substance is unchanged: the only edit is the status line. ADR-0199 and ADR-0200 already
  carry their "Superseded in part by ADR-0214" back-links.

### Definition of Done

16/16 hold: the 7 Review-checklist items, 3 items of plan step 4 (`just ci` green, a passing test per Scenario, no
`go.mod` or language-module change), and 6 generic items (real behavior with no stubs, Contracts honored, tree
against the plan, conventions, scope, tracking). The two rules without tests (Minor 1) are implemented. The
ADR-0221 test (Minor 2) adds no decision. For `just ci-full`, this gate ran `just ci` (exit 0), the 5 Scenarios with
`-race`, and every App e2e test. The full `test-e2e` lane runs once at the PR gate (`scripts/agent/gate.sh`) and was
not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0214 (implementation) → pass, 0/0/4, 3 model-attributed, DoD 16/16. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0214 moves `Reviewing → Implemented` and FEAT-0010 F117 `reviewing → implemented` in this commit. The
three `model` Minors are small follow-ups for the builder: the two missing tests, the out-of-scope test, and an
optional split of `Reconcile`. Minor 4 is an input for any later ADR that revisits the rollout deadline. The main
session moves the board card to Done. The PR gate still owes the full `just ci-full` run.
