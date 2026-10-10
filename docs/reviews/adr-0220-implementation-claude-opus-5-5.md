# ADR-0220 implementation review: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 1 minor (ADR-0220 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0220`, head `901dff95`, on `origin/main` at `9bdbaf1f`. Eight commits: the API types, the
control-plane engine and wire, the App planner, the SDK option, the funcdctl flags, a fix to the template render, the
e2e scenarios, the status bump, and a block-style fix to a test. The diff is 30 files, +2615/−146. The non-test Go code
is `api/types/v1alpha1/app.go`, `internal/controlplane/{controlplane,dryrun,handlers,routes,routes_rest,server}.go`,
`internal/app/{plan,reconcile,revision,hooks}.go`, `internal/app/template/render.go`, `pkg/sdk/sdk.go`,
`cmd/funcdctl/{cli,app,workflow}.go` and `pkg/funcd/funcd.go`, plus the generated OpenAPI. `go.mod` and `go.sum` are
unchanged.

The engine, the planner, the SDK and the CLI conform to the ADR, and every mutant tried was killed. One scenario test
is flaky: it reads a resourceVersion that the Function reconciler can still move.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed one |
| `go test -tags e2e -race -count=1 -run 'TestScenario(ApplyDryRunRefused\|AppApplyDryRun\|…\|DryRunUnsupported)$' -v ./pkg/funcd` (the 9 Scenarios) | `EXIT=1`: 8 `--- PASS`, 1 `--- FAIL: TestScenarioDryRunStoresNothing` at `dryrun_e2e_test.go:277` (Major 1) |
| the same 9 Scenarios, `-count=4`, then `-count=15` | `-count=4`: 36/36 pass. `-count=15`: `EXIT=1`, 133/135 pass; both failures are `TestScenarioDryRunStoresNothing` at line 277, `expected: "…-2"`, `actual: "…-4"` |
| `TestScenarioDryRunStoresNothing` alone, `-count=8` | 8/8 pass; the race needs the load of the parallel Scenarios |
| `go test -race -count=1` of `./internal/controlplane`, `./internal/app/...`, `./pkg/sdk`, `./cmd/funcdctl`, `./api/types/v1alpha1` | all `ok`, uncached |
| 6 mutants through `go test -overlay` (listed below) | 6 killed |
| `scripts/agent/audit.py --base 9bdbaf1f --head HEAD --report-only` | `PASS`, 0 hard flags; soft flags under Minor 1 |
| `git diff 9bdbaf1f..HEAD -- docs/` | `docs/adr/0220-dry-run-engine.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F123 status cell, `accepted` → `reviewing` |
| `go list -deps ./internal/controlplane \| grep -c 'internal/app$'` | `0` |

Mutants. Each one fails the named tests:

- No dry-run branch in `createObj` (`internal/controlplane/handlers.go:126`): `TestDryRunEveryWritableKind`,
  `TestDryRunWithoutPlanner`.
- `dryRunReplace` does not copy the stored `uid` (`internal/controlplane/dryrun.go:72`): `TestDryRunEveryWritableKind`.
- `rejectUnknownQuery` sets nothing (`dryrun.go:47`): `TestDryRunOperations`, `TestDryRunUndeclaredOnAWrite`.
- `PlanApp` lists no prune (`internal/app/plan.go:95`): `TestPlanPrune`, `TestPlanUpgradeMatchesOnePass`.
- `PlanApp` skips the ChildNotOwned stop (`plan.go:58`): `TestPlanPartOfAnotherOwner`.
- `Apply` drops `DryRun` on the fallback `POST` (`pkg/sdk/sdk.go:128`): `TestDryRunApplySendsTheFlag`.

### 🔴 Blockers

None.

### 🟡 Majors

1. **`TestScenarioDryRunStoresNothing` is flaky: it races the Function reconciler** · attribution: `model`.
   The test creates Function `g` (`pkg/funcd/dryrun_e2e_test.go:266`), reads it at once (`:269`), sends the dry-run
   `PUT` (`:272`) and requires the answer's `resourceVersion` to equal the one it read (`:277`). The Function reconciler
   writes `g` after a create (`r.store.Update(ctx, fn)`, `internal/function/function.go:815`, `:939`, `:1136`), so
   under the load of the parallel Scenarios the stored version moves between the read and the dry run: 3 failures in
   28 runs here (`expected "…-2"`, `actual "…-4"`), each at line 277. The engine is right: Decision 3 answers the
   *stored* version at the time of the dry run, and the later `GET` still shows the old spec. Only the test is wrong.
   It breaks Review-checklist item 9 ("none of them sees a `resourceVersion` move") and would fail `just ci-full` and
   CI at random. Fix (builder): read the stored `g` once it has settled, as `appState` and `settledList` do for the App
   Scenarios, or compare the answer with a `GET` made right after the dry run, and keep the `uid` and spec assertions.
   Then rerun the 9 Scenarios with `-race -count=15`.

### Minor

1. **Two functions grew past the lint thresholds** · attribution: `model`. The bloat audit's soft flags:
   `PlanApp` has 54 statements (`internal/app/plan.go:25`); `registerKVStore` crossed 80 lines
   (`internal/controlplane/routes_rest.go:621`) with the mechanical `DryRunParams` lines. `PlanApp` reads as one
   sequence that mirrors the pass, so a split into the revision step and the parts step is optional. No action is
   needed for `registerKVStore`.

### ✅ Verified correct (keep it)

- **Wire (Decision 1).** `DryRunParams` is embedded in every create and replace input, generic CRUD included
  (`routes_rest.go:1666-1678`), and each closure puts the flag into the context with `withDryRun`. One
  `OnAddOperation` hook (`controlplane.go:231`, `dryrun.go:45-49`) sets `RejectUnknownQueryParameters` on every
  operation that is not `GET` or `HEAD`, so a later write route gets it without code. `TestDryRunOperations` checks
  both rules over `v1.AllKinds()` and every registered operation with no fixed count, and also checks that read-only
  kinds have neither route. `DELETE …?dryRun=true` answers 422 and `g` remains (`TestDryRunUndeclaredOnAWrite` and the
  Scenario). A misspelt `dryrun` is refused, not ignored.
- **Existing callers.** The only query parameter a write route declares is `force` on the ResourceGroup delete
  (`routes.go:164`), and the SDK sends only that one (`pkg/sdk/sdk.go:322`). No Go client, Venom suite or lane script
  sends any other query parameter on a write.
- **Engine (Decisions 2 and 3).** The branch sits only at the store call: after `withServerMeta` in `createObj`
  (`handlers.go:126`) and after the `resourceVersion` copy in `replaceObjIf` (`:250`). `authorize`, the lock, the `Old`
  fetch, the `If-Match` check, the KVStore guard and `Admit` are unchanged. A create `Get`s its name and answers the
  store's own 409 text; `TestDryRunTakenNameMatchesTheStore` compares it with the real store's text. A replace copies
  `uid`, `generation` and `creationTimestamp` from `Old`. `TestDryRunEveryWritableKind` runs every writable kind
  against a store that counts and refuses every write. It sees zero writes, an empty `uid` on a create whose body
  carried one, and the stored `uid` on a replace. No new `auth.Verb` exists.
- **Lock (Decision 4).** `withPlan` runs on the result of `createObj` and `replaceObj`, so it runs after the unlock.
  `TestDryRunWaitsForTheLockAndPlansAfter` holds the namespace lock, sees that the dry run waits, and then sees that
  the planner can take the lock.
- **Planner (Decision 5).** It is a seam: `AppPlanner` lives in `internal/controlplane`, and `internal/app` implements
  it, with no import of `internal/app` from the control plane. The pass and `PlanApp` share `reader`, `nextStamp`,
  `namesake`, `readParts`, `notOwned`, `checkSecrets`, `differs`, `desired`, `partsOf` and `dropped`. The stamp and
  `write` now call those helpers, and the existing reconciler tests pass with no edit. The planner tests compare the
  plan with what one real `Reconcile` pass writes: create, upgrade, pre-hook, Secret stop and foreign part. They also
  check that the planner writes nothing. A nil planner answers 503 with `controlplane.dryRun: no App planner is wired`
  for an App only (`TestDryRunWithoutPlanner`).
- **SDK (Decision 6).** `DryRun()` adds `dryRun=true` to the `PUT`, the fallback `POST` and the `generateName`
  `POST`. The client reads `/openapi.json` once under a mutex, and refuses when the operation does not declare the
  parameter or when the server serves no document (`TestDryRunUnsupportedServerWritesNothing`, both cases).
  `Create` is unchanged.
- **CLI (Decision 7).** `apply`, `app rollback` and `app deploy` take `--dry-run`. The plan printer follows the
  Contracts line format (`TestCLIPlanLines`). Rollback keeps the 409 re-read and the `no change` answer. Deploy sends
  an equal spec too and does not wait.
- **Scenarios.** All 9 have a test of the same name, with `t.Parallel()`. The slowest pass is 6.6 s, and no timeout
  was raised. The App Scenarios start from `todo-1` instead of the fixture's `todo-4`; the `n+1` naming they check is
  the same.
- **The template fix.** `ef232aac` makes Render append a file's `hooks` lists (ADR-0217 Decision 7). Before, a
  template's hooks never reached the App. The `app-deploy-dry-run` Scenario needs the fix for its `hook todo-migrate`
  line. It has its own commit and `TestRenderAppendsHooks`.
- **Conventions.** There is no `any` in a new API, no `panic` or `fmt.Print*`, and `fault` errors carry the
  Contracts' texts. YAML in tests is block style. No dependency was added.

### Definition of Done

16/18 items hold (9 Review-checklist items + 9 applicable generic items; the driver contract-suite item does not
apply). Misses: Review-checklist item 9 and the generic "every Scenario passes", both from Major 1 (`model`).
`just ci-full` was not run here; it belongs to the PR gate.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0220 (implementation) → changes-requested, 0/1/1, 2 model-attributed, DoD 16/18.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Fix Major 1 in the test only: read `g`'s `resourceVersion` once it has settled, or right after the dry run. Then rerun
the 9 Scenarios with `-race -count=15` and send the work back to this gate. The ADR stays `Reviewing`. No `adr`
finding.
