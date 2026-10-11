## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0229 implementation, model: claude-opus-5-5)

Scope: `git log 52316ce3..43f513bb` on `feat/adr-0229-step-store-at-apply`: the acceptance commit `082c6152` and the
implementation commit `43f513bb`. The branch was rebased onto `origin/main` (`52316ce3`) before the gate run;
`git range-diff` shows both commits identical to the earlier `3a81f941` and `37d83cdf`. The production changes are
in `api/types/v1alpha1/workflow.go`, `internal/workflow/reconcile_workflow.go`, the new
`internal/controlplane/admission/workflow.go` and one registration line in `pkg/funcd/funcd.go`. The tests are two
unit tests and four scenario tests in `pkg/funcd`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| touched packages, race | `go test -race -count=1 ./api/types/v1alpha1/ ./internal/workflow/ ./internal/controlplane/admission/ ./pkg/funcd/` | 4 × `ok`, exit 0 |
| new unit tests | `-run 'TestWorkflowSpecUndeclaredStepStore\|TestWorkflowStepStoresAdmission' -v` | both PASS, exit 0 |
| scenario tests, race | `go test -race -count=1 -tags e2e` on the 4 new tests, `TestScenarioAppAdmissionRefuses` and `TestScenarioRecreatedWorkflowNeedsHandover` | 6 × PASS, exit 0 |
| ADR-0178's other scenario tests | the 10 tests in `internal/workflow`, `internal/controlplane` and `internal/gc`, `-race -v` | 10 × PASS, exit 0 |
| vet | `go vet` on the 4 packages and `go vet -tags e2e ./pkg/funcd/`, on darwin and with `GOOS=linux` | exit 0 (all four runs) |
| lint | `go tool golangci-lint run` on the 4 packages, and with `--build-tags e2e` on `./pkg/funcd/` | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the 8 changed Go files | empty, exit 0 |
| revert check | `go test -overlay`: the base's `workflow.go`, `reconcile_workflow.go` and `funcd.go`, the new admission file removed | the two apply-time scenarios FAIL (below); the other two pass, as they must |
| gate | `scripts/agent/gate.sh`, run by the orchestrator; its logs were read after it ended | audit PASS (0 hard flags); `ci-full` green through `go test -tags e2e ./pkg/funcd/...` (`ok … 364.533s`); Linux build, vet and lint `0 issues.`; tree unchanged |

Results of the four scenario tests without the change:
- `TestScenarioUndeclaredStepStoreRefusedAtApply`: `expected: 400, actual: 200` at `stepstores_e2e_test.go:37`.
- `TestScenarioUndeclaredStepStoreRefusedInApp`: `expected: 400, actual: 200` at `stepstores_e2e_test.go:52`.
- `TestScenarioDeclaredStepStoreAccepted` and `TestScenarioStoredWorkflowStillRefused` pass. The first asserts that
  nothing more is refused, and the second that the materializer still refuses.

Targeted mutants (run with `go test -overlay`, so the work tree was never edited):
- **M1**: `UndeclaredStepStore` also checks a step without `function.image`. **Killed** by
  `TestWorkflowSpecUndeclaredStepStore` ("no image") and `TestScenarioDeclaredStepStoreAccepted` (the `ref` step).
- **M2**: the admission handles Create only. **Killed** by `TestWorkflowStepStoresAdmission`.
- **M3**: `checkStepStores` never refuses (`reconcile_workflow.go:363`). **Survived**
  `TestScenarioStoredWorkflowStillRefused` and all 243 tests of `internal/workflow` (Minor 1).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1: the `stored-workflow-still-refused` test does not pin `checkStepStores`** · attribution: model
  - Evidence: mutant M3 keeps the test green. `patchFunctionKV` (`reconcile_workflow.go:602-614`) re-reads each bound
    store and refuses a store it did not check, with the same `KVStoreNotOwned` message. It does so only after
    `ensureKVStore` (`:274`) has written the declared stores, and one step at a time.
  - The test's Workflow has one step and an empty `spec.kv`, so the scenario's "no binding written" holds on both
    paths. Only `checkStepStores` guarantees it when an earlier image step binds a declared store.
  - ADR-0178's `TestScenarioStepBindingToUnownedStoreRefused` (`internal/workflow/kvadoption_test.go:201-216`) has the
    same blind spot, because its undeclared step `s` comes before `s2`. The gap predates this change, and the
    refactored guard behaves exactly as the old loop did (see below).
  - Fix (builder, optional): put an image step that binds a declared store before the undeclared one, and assert that
    its Function has no binding and that the declared store was not made.

### ✅ Verified correct (keep it)
- **Decision 1, one check**: `WorkflowSpec.UndeclaredStepStore` (`api/types/v1alpha1/workflow.go:424-443`) has the
  signature in the Contracts and is the only copy of the loop. Its two callers are the admission
  (`admission/workflow.go:33`) and `checkStepStores` (`reconcile_workflow.go:361-366`). The skip of a step without
  `function.image`, the order, the first hit and the `kvNotOwned` error are those of the old loop, so the materializer
  behaves as before.
- **Decision 2, the admission**: `workflow-step-stores` is Validating and handles only a Workflow Create or Update
  (`admission/workflow.go:20-25`). It answers `fault.Invalid` with
  `spec.steps[i].function.kv[j].store "<s>" is not declared in spec.kv` (`:34`). It reads no store (ADR-0121) and does
  not implement `NamespaceReading`, so the namespace lock of `app-parts` is unchanged.
- **Registration**: the admission is in `partAdmissions` (`pkg/funcd/funcd.go:1177`, the list at `:1150`), which feeds
  both the direct pipeline and `app-parts` (`:1231`). `internal/app/admission.go` is untouched, and the App test
  asserts the message that `:97` wraps, word for word.
- **`Validate` unchanged**: `workflow.go` has no removed line and `app.go` is untouched. The unit test asserts that
  `Validate` returns nil in every case, so a stored Workflow can still write its status.
- **Scenarios**: four tests, one per scenario, each with its `// scenario:` line.
  - The apply test also sends the dry run (`?dryRun=true`, the `DryRunParams` query) and asserts the same refusal.
  - The App test asserts that neither the App nor any of its parts is stored.
  - The declared test covers a `ref` step, a builtin step and an update.
  - The stored test writes through the store, so admission does not run. It asserts the condition, the message,
    `ObservedGeneration` (the status write succeeded), no binding on `w-s` and no store.
- **Unit tests** follow Implementation plan item 3: the four `UndeclaredStepStore` cases, and the admission on Create
  and Update, an update that fixes the binding, a delete and three other kinds.
- **ADR-0178's scenario tests and `TestScenarioAppAdmissionRefuses`**: no test file of theirs changed, and all 12 pass.
- **Conventions**: the file name, the op name (`admission.workflow-step-stores`) and the section header follow the
  package. The comments state why the rule stays out of `Validate` and do not narrate the code.
- **Tracking**: ADR-0229 moved `Accepted → Reviewing`, and the Status line is its only change in the implementation
  commit. F126 moved `accepted → reviewing`. ADR-0178 received the `Superseded in part by: ADR-0229` back-link in the
  acceptance commit, the one touch that an Implemented ADR allows.

### Definition of Done
8 / 8 items hold: the 4 Review-checklist items and the 4 DoD clauses (the tests pass; ADR-0178's scenario tests and
`TestScenarioAppAdmissionRefuses` pass unchanged; the gate passes; no username or absolute path in a changed file).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0229 (implementation) → pass, 0 blockers / 0 majors / 1 minor, 1 model-attributed,
DoD 8/8. See `docs/reviews/model-scorecard.md`.

### Recommendation
Sign off. ADR-0229 moves to `Implemented`, F126 to `implemented`, and the board card to Done. Minor 1 is optional: one
more step in the stored-Workflow test would pin the guard that the decider asked to keep.
