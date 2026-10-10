# ADR-0220 implementation review: claude-opus-5-5 (round 2)

## Verdict: pass, 0 blockers, 0 majors (ADR-0220 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0220`, head `96bf914f`, on `origin/main` at `9bdbaf1f`. This round reviews the rework of
the first review (`docs/reviews/adr-0220-implementation-claude-opus-5-5.md`, changes requested). The rework is one
commit, `96bf914f`, which changes two files: `pkg/funcd/dryrun_e2e_test.go` (Major 1) and `internal/app/plan.go`
(Minor 1). The whole change against the base is still 30 code and doc files. `go.mod` and `go.sum` are unchanged.

The flaky Scenario is fixed in the test alone, and the fix keeps the assertion strict. The 9 Scenarios passed 135
times out of 135 under `-race`, in the same run shape where round 1 saw 2 failures. `PlanApp` is now split into its
revision step and its parts step, and the planner tests kill every mutant tried. Every Review-checklist item holds.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed file |
| `go test -tags e2e -race -count=15 -run 'TestScenario(ApplyDryRunRefused\|…\|DryRunUnsupported)$' -v ./pkg/funcd` (the 9 Scenarios) | `EXIT=0`: 135 `--- PASS`, 0 `--- FAIL`, 0 data races. The slowest passes: `AppRollbackDryRun` 8.3 s, `AppApplyDryRun` 6.1 s, `AppDryRunUnchanged` 5.9 s; `DryRunStoresNothing` at most 0.6 s |
| 5 new mutants through `go test -overlay` (listed below) | 5 killed |
| `scripts/agent/audit.py --base 9bdbaf1f --head HEAD --report-only` | `PASS`, 0 hard flags. Soft flags: `registerKVStore` is 81 lines (`internal/controlplane/routes_rest.go:621`), which round 1 accepted; a 5-line test clone (`internal/app/plan_test.go:180-186`); two discarded writes in test HTTP handlers. `PlanApp` is no longer flagged |
| `git diff 9bdbaf1f..HEAD -- docs/adr docs/feat` | `docs/adr/0220-dry-run-engine.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F123 status cell, `accepted` → `reviewing` |
| `go list -deps ./internal/controlplane \| grep -c 'funcd/internal/app$'` | `0` |
| the `Create*` and `Replace*` methods of `storeHandlers` | 52, each only `createObj` or `replaceObj`/`replaceObjIf` and a type conversion, so no write route has a side effect outside the store call that a dry run would skip |

Mutants. Each one fails the named tests:

- A dry-run replace that also calls `store.Update` (`internal/controlplane/handlers.go:250`):
  `TestScenarioDryRunStoresNothing`, at the check that a `GET` still returns the old spec (`dryrun_e2e_test.go:290`).
- `dryRunReplace` that answers no `resourceVersion` (`internal/controlplane/dryrun.go:72`):
  `TestScenarioDryRunStoresNothing`, at the check that the answer's version is the stored one (`:287`).
- `planRevision` that drops the namesake stop (`internal/app/plan.go:86`): `TestPlanRevisionNamesake`.
- `changedParts` that lists no `update` (`plan.go:105`): `TestPlanUpgradeMatchesOnePass`, `TestPlanPrune`.
- `PlanApp` that ignores the Secret stop (`plan.go:46`): `TestPlanSecretStop`.

The first two show that the reworked Scenario still catches a dry run that writes and a wrong answered version.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None. The `registerKVStore` soft flag stays as round 1 accepted it: the two extra lines are the `DryRunParams` field
and the `withDryRun` call, the same lines as in every other route.

### Round 1 findings

- **Major 1, the flaky `TestScenarioDryRunStoresNothing`: fixed.** The Scenario now reads `g`'s `resourceVersion`,
  sends the dry-run `PUT`, and reads the version again (`pkg/funcd/dryrun_e2e_test.go:271-284`). It accepts the answer
  only from a dry run whose two reads agree, and then requires the answer's version to equal that version, the
  answer's `uid` to be `g`'s, the answer's handler to be the new one, and a later `GET` to return the old handler. The
  Function reconciler can still write `g`'s status before the bracket, but not inside an accepted one. So the check is
  as strict as Decision 3 requires: the answer carries the stored version at the time of the dry run. A dry run that
  writes never gives two equal reads unless the store coalesces the write, and then the final `GET` fails (mutant 1).
  The engine is unchanged.
- **Minor 1, the function length: fixed for `PlanApp`.** `planRevision` holds the list, `nextStamp` and the namesake
  stop; `changedParts` holds the create and update loop over `differs`. The order of the steps and the stops is
  unchanged, and the planner tests pass with no edit.

### ✅ Verified correct (keep it)

- **Wire (Decision 1).** `DryRunParams` is embedded in all 52 create and replace inputs, the generic CRUD inputs
  included. One `OnAddOperation` hook (`controlplane.go:231`, `dryrun.go:45-49`) sets
  `RejectUnknownQueryParameters` on every operation that is not `GET` or `HEAD`. `TestDryRunOperations` checks both
  rules over `v1.AllKinds()` and every registered operation, with no fixed count. A `DELETE` with `?dryRun=true`
  answers 422 and `g` remains.
- **Engine (Decisions 2 and 3).** The dry-run branch is only at the store call: after `withServerMeta` in `createObj`
  (`handlers.go:126`) and after the `resourceVersion` copy in `replaceObjIf` (`:250`). A create `Get`s its name and
  answers the store's own 409 text, then clears `uid`, `generation`, `resourceVersion` and `creationTimestamp`. A
  replace copies `uid`, `generation` and `creationTimestamp` from `Old`. `authorize` keeps `VerbCreate` and
  `VerbUpdate`, and no new `auth.Verb` exists.
- **Lock and plan (Decisions 4 and 5).** `withPlan` wraps the result of `createObj` and `replaceObj`, so the plan is
  computed after the unlock. The planner is a seam: `internal/controlplane` defines `AppPlanner` and does not import
  `internal/app`. The pass and `PlanApp` call the same helpers (`nextStamp`, `namesake`, `readParts`, `notOwned`,
  `checkSecrets`, `differs`, `desired`, `partsOf`, `dropped`). The reconciler tests pass unchanged. A nil planner
  answers 503 for an App only.
- **SDK and CLI (Decisions 6 and 7).** `DryRun()` adds `dryRun=true` to the `PUT`, the fallback `POST` and the
  `generateName` `POST`, after the client checks the served OpenAPI once. `Create` is unchanged. `apply`,
  `app rollback` and `app deploy` take `--dry-run`, and the plan lines follow the Contracts format.
- **Scenarios.** All 9 have a test of the same name, each with `t.Parallel()`, and none raised a timeout. None of
  them saw a `resourceVersion` move in 135 runs.
- **Conventions.** No `any` in a new API, no `panic` or `fmt.Print*`, `fault` errors with the Contracts' texts,
  block-style YAML in tests, no new dependency.

### Definition of Done

18/18 items hold (9 Review-checklist items and 9 applicable generic items; the driver contract-suite item does not
apply). `just ci-full` was not run here: it runs once, in the PR gate. This ADR's 9 e2e Scenarios ran above.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0220 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 18/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0220 moves from `Reviewing` to `Implemented`, and FEAT-0010 F123 moves to `implemented`. The PR gate
runs `just ci-full` once.
