# ADR-0199 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 1 minor (ADR-0199 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0199-final`, head `bcd48e94`, 11 commits on merge base `acdd8518` (the ADR-0199 acceptance,
#841). The diff is 46 files, +5375/−140. `go.mod` and `go.sum` are unchanged. The tree is clean after `just ci`,
whose `generate` step rewrote the OpenAPI spec with no diff.

### Verification run

All commands ran in the worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `internal/app`, `internal/gc`, `internal/controlplane`, `api/types/v1alpha1` ok |
| `go test -tags e2e -run TestScenarioApp -v -count=1 ./pkg/funcd/` | `EXIT=0`, 15/15 `--- PASS`, 0 skipped (25.1 s) |
| The e2e tests that seed the `s3/` substrate or exercise the GC (the boot reclaim of Decision 7 changes start-up): `TestScenarioBlobEventSourceEndToEnd`, the 6 pooled-member tests, `TestScenarioE2EStaticServing`, the 13 `gc_e2e_test.go` scenarios, `TestScenarioE2ESite`, `TestIssue464_*`, `TestScenarioE2EPathMountedSites` and the matched workflow scenarios | `EXIT=0`, 26 tests pass (38 `--- PASS` lines with subtests), 0 fail or skip (64.9 s) |
| Six mutants through `go test -overlay` (listed below) | 6/6 killed |
| `git diff origin/main...HEAD -- docs/adr/0199-app-resource.md` | one line: `Accepted` → `Reviewing` |
| `docs/feat/0010-feat-apps.md` | the F113 row moved `accepted` → `reviewing`, nothing else changed |

Mutants (each fails the named test, so the guard is pinned):

- The ChildNotOwned stop removed (`internal/app/reconcile.go:192`): `TestScenarioAppChildNotOwned` fails.
- Prune allowed while a part is Pending (`reconcile.go:158`): `TestScenarioAppPruneDroppedPart` (unit) fails.
- A waking Function, phase `Deploying`, no longer counted (`internal/app/status.go:104`): `TestAppReadiness` fails.
- `app-parts` admitting each part over the bare store instead of the view (`internal/app/admission.go:91`):
  `TestScenarioAppAdmissionRefusesOverQuota`, `TestAdmissionViewHoldsTheOtherParts` and the e2e
  `TestScenarioAppAdmissionRefuses` fail.
- The GC's used-store wait removed (`internal/gc/gc.go`, `remove`): `TestAppStoreInUseWaitsForALaterSweep` fails.
- `DeleteBucket`'s second purge removed: `TestDeleteBucket` (gc) and `TestDeleteBucketPurgesTheSubstratePrefix`
  (real purger, an object written between the first purge and the delete) fail.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **m1: a doc comment moved to the wrong declaration** · attribution: model. In `pkg/funcd/kvstore_reclaim_test.go:90-97`
  the new `noPurge` type sits between `stalledDrop`'s doc comment and the `stalledDrop` type. The two comment blocks now
  form one doc comment on `noPurge`, and `stalledDrop` has none. Fix: move `noPurge` above the `stalledDrop` comment.

### ✅ Verified correct (keep it)

- **Contracts match the ADR exactly.** `App`, `AppSpec` (nine sections plus `version`), the nine entry types with
  `name`, `ref` and, for the stores, `deletion` (`api/types/v1alpha1/app.go:31-110`), `AppStatus`/`AppChild` and the
  four `AppChildState` values with an OpenAPI enum. `Parts()`, `Refs()` and `Validate()` are present. The
  `internal/app` signatures (`NewAdmission`, `Deps`, `NewReconciler`, `Reconcile`, `MapPart`) and the
  `internal/gc` additions (`BucketPurger`, `DeleteBucket`, `InUse`, `Deps.Purger` required, nine App pairs before
  `(Function, Revision)`) all match.
- **Decision 3.** `App.Validate` is store-free and refuses each case with its field path: the 52-character name, a
  missing name or ref, ref plus another field, a repeated name, a part failing its kind's `Validate`, and the
  shared-writer clashes (Site Route and Bucket, Workflow step Function and kv store). `partError` re-roots a part's
  `spec.` paths under the entry, which gives `spec.kv[0].tables[0].name`; `KVStore.Validate` gained the table index
  for this. `app-parts` admits each part as a create or an update over the stored object, with the writer's identity,
  over a `view` that holds the App's other parts. It holds the namespace lock when a part admission reads the
  namespace, and it refuses a `ref` to an object this App controls. The only admission the server adds itself is
  `validate` (`internal/controlplane/server.go:72`), which `App.Validate` already covers for the parts.
- **Decision 4.** All parts are read first, and a pass stops before any write when a store lacks this incarnation's
  marker or another part lacks an `App/<name>` controller reference (any UID). Owner references are re-derived on
  every pass. Unit tests show that a change of `deletion`, resource group or UID alone rewrites the part (the
  "another incarnation controls is taken back" subtest). A write carries the stored status, and a hand edit or a
  hand delete is written back (`TestAppWritesBackAHandEdit`). A store refusal gives `ChildInvalid` and stops later
  writes (`TestAppChildInvalid`). The store coalesces no-op writes (ADR-0047), so a converged pass writes nothing.
  The unit and e2e `app-spec-change-applies` tests confirm this.
- **Decision 5.** `judgeFunction` follows the rule literally, and `TestAppReadiness` has 17 rows. They include the
  activator's wake write (`Deploying`, `RevisionReady=True`), `Progressing` waking, `NotStarted`, a replaced replica,
  an earlier generation, a KVStore, a CatalogService and a timer EventSource at a new generation, and a Bucket.
  Every non-Function section kind stamps `observedGeneration` on `Ready`: KVStore, CatalogService and the timer
  EventSource in this branch, as the plan asks, and Workflow, Route, Sensor, Site (`internal/site/reconcile.go:364`)
  and the blob EventSource already did. The e2e idle scenario watches every write of the App and shows that `Ready`
  never leaves True.
- **Decisions 6 and 7.** Prune runs only in a pass that was not stopped and has no Pending part. It deletes only
  what this UID controls, skips held Functions (`RunHeld`), finds `InUse` by a fixpoint that counts kept objects as
  not deleted, deletes a Bucket through `DeleteBucket` and requeues after the supervision period. `InUse` has one
  test row per field of Decision 6. The GC applies the marker rule to Buckets and leaves a used App store for a
  later sweep. `bucketPurger` shares `bucketPrefix` with `s3BucketFor`. The boot reclaim purges only `s3/<ns>/<name>/`
  prefixes with valid names and no Bucket (tested on the memory and file substrates). It is safe because every
  writer of that prefix resolves an existing Bucket through `s3BucketFor`.
- **Decision 8.** `IsKVMarker` accepts an App marker. `liveMarker` reads the marker's own kind, and both 409
  messages name `<Kind>/<name>`, which the e2e `app-store-not-taken-over` test shows.
- **Scenarios.** Each of the 15 Scenarios has a `TestScenarioApp…` in `pkg/funcd` (e2e tag) whose assertions match
  the ADR text, including the 30 s and 5 s bounds (`appWithin`, `gcWithin`). Several Scenarios also have a
  reconciler-level unit test. `TestScenarioCLIApplyApp` covers the CLI `apply` through `controlplane.NewServer`, as
  the plan asks.
- **Changes to existing tests are justified, not weakened.** `TestIssue148`'s timer row now expects
  `Ready=True` at its generation, as the plan requires ("sets `Ready` True … rather than removing it"), and still
  shows that the blob reason and message are gone. The Site e2e seeds its gold objects after the Bucket exists,
  because the new boot reclaim purges a prefix that has no Bucket. The other edits add `Purger: noPurge{}` and the
  kind count 26 → 27.
- **Conventions.** No `any` or `interface{}` in the new APIs, no `panic` or `fmt.Print*`, `log/slog` only,
  ctx-first, `api/fault` kinds throughout, a stateless reconciler, block-style YAML in the test manifests.
- **Scope.** The generic CRUD helpers (`crudKind`, `registerNamespacedCRUD`, `stubGet`/`stubPut`/…) serve only the
  App. They replaced copied per-kind code that the bloat audit flagged (commit `7f3e1312`), and the OpenAPI spec
  is unchanged. Nothing outside Decisions 1-10 and the Implementation plan was added.

### Definition of Done

16/16 hold: the 6 Review-checklist items, the 3 items of plan step 5 (`just ci` and `just ci-full` green, a passing
test per Scenario, no `go.mod` change), and 7 generic items (real behavior with no stubs, Contracts honoured, the
purger driver tested on two substrates, tree versus the plan, conventions, scope, tracking). For `just ci-full`, this
gate ran `just ci` (exit 0) and the e2e half for the 15 App Scenarios plus 26 related e2e tests. The full
`test-e2e` lane runs once at the PR gate (`scripts/agent/gate.sh`) and was not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0199 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 16/16. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0199 moves `Reviewing → Implemented` and FEAT-0010 F113 `reviewing → implemented` in this commit. The
main session moves the board card to Done. m1 is a one-line move that can ride with the PR or a later cleanup. The
PR gate still owes the full `just ci-full` run.
