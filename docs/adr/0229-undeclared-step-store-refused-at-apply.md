# ADR-0229: A Workflow step that binds an undeclared KV store is refused at apply; the materializer keeps its check

- **Status**: Accepted (2026-10-11; decided by the decider, judged twice: the first round moved the App case onto
  `app-parts` and completed the superseded entry, the second found no Blocker or Major)
- **Date**: 2026-10-11
- **Deciders**: green-0-rabbit
- **Tags**: workflow, app, kv, admission
- **Realizes**: [FEAT-0010/F126](../feat/0010-feat-apps.md) (A binding that cannot work is refused at apply; a new row)
- **Source**: the app-todo lane's first draft (2026-10-11): an App whose Workflow image step bound a KV store the
  Workflow did not declare passed apply, and its revision failed only when `app.upgradeTimeout` ran out, with
  `KVStoreNotOwned`. Decider, 2026-10-11: "refusing at apply ok but we shouldn't remove it at reconcile because it's a
  second guard".
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0229` back-link at acceptance:
  [ADR-0178](0178-a-workflow-adopts-only-its-own-kv-stores.md) (Implemented): Decision 2, "Step bindings", "The rule
  lives in the materializer, where the marker is read; admission is unchanged": admission now also refuses a step
  store that `spec.kv` does not declare, and the materializer's rule is unchanged. Scenario
  `step-binding-to-unowned-store-refused` (83–87): an API write of that spec is now refused at apply; the scenario
  still holds for a Workflow that reaches the store without admission.
- **Relates to** (unchanged): [ADR-0063](0063-admission-framework.md) (admission) ·
  [ADR-0199](0199-app-resource.md) Decision 3 (`app-parts` runs each part admission on an App's parts) ·
  [ADR-0213](0213-app-config-and-secret-declarations.md) Decisions 7 and 9 (the precedent: a rule kept out of
  `Validate` so stored objects can still write their status) · [ADR-0220](0220-dry-run-engine.md) (a dry run passes
  admission) · ADR-0121 (no write-time existence gate) · ADR-0094.

## Context & Need

An image step binds KV stores with `steps[].function.kv[].store`. ADR-0178 lets a step bind only a store that its
Workflow declares in `spec.kv`, and checks it only in the materializer (`checkStepStores`,
`internal/workflow/reconcile_workflow.go:359-377`, called at `:269`): the Workflow is stored, then reports NotReady
`KVStoreNotOwned`. Inside an App the mistake shows only when the upgrade times out. Whether a step's store is
declared depends on the spec alone, so apply can refuse it, with the field that is wrong.

## Scenarios

- `scenario: undeclared-step-store-refused-at-apply` — Given no Workflow `w`, When a client applies `w` whose image
  step binds store `shared` in `function.kv` and `spec.kv` does not declare `shared`, Then the API answers 400 naming
  `spec.steps[0].function.kv[0].store` and stores nothing; a dry run of the same apply answers the same.
- `scenario: undeclared-step-store-refused-in-app` — Given an App whose Workflow part's image step binds a store that
  the part does not declare, When it is applied, Then the API answers 400 with a message that names
  `spec.workflows[0]` and `spec.steps[0].function.kv[0].store`, and stores nothing.
- `scenario: declared-step-store-accepted` — When every step store is declared in `spec.kv`, Then the apply succeeds;
  a step without `function.image` (a `ref` to an existing Function, or a builtin) is not checked.
- `scenario: stored-workflow-still-refused` — Given a Workflow that binds an undeclared store and reached the store
  without admission (stored before this change, or by an in-process writer), When it reconciles, Then it is NotReady
  `KVStoreNotOwned` with no binding written, as ADR-0178 says, and that status write succeeds.

## Scope

**In**: one spec check shared by the materializer and a new Validating admission on Workflow creates and updates,
which `app-parts` also runs on an App's Workflow parts.

**Out**: ADR-0178's ownership checks (the store's marker and UID), which need the store's state and stay in the
materializer; `Workflow.Validate` and `App.Validate`.

## Constraints & Decision drivers

- The materializer's check stays as a second guard (decider).
- `store.Update` runs `Validate` on every write, status included (`internal/store/store.go:376, 438`). A rule in
  `Validate` would fail every status write of a Workflow stored before it, which then could not record
  `KVStoreNotOwned`; ADR-0213 kept its undeclared-Secret rule out of `Validate` for the same reason
  (`internal/app/admission.go:103-106`).
- Admission (ADR-0063) runs on API creates and updates, dry runs included
  (`internal/controlplane/handlers.go:115-127, 256`), and never on a write a controller makes through the store; the
  App controller writes its parts that way (ADR-0199 Decision 9).
- Not an existence check (ADR-0121): the rule reads only the written spec, and the store need not exist.

## Alternatives considered

1. **A rule in `Workflow.Validate`**, as #936 (a store named twice) and #942 (the stores' and step Functions' own
   checks) did: it reaches every writer and needs no admission, but a Workflow stored before the change fails every
   status write and loses the materializer's report. Rejected: it would silence the second guard.
2. **Move the check from the materializer to admission**: one place. Rejected by the decider (second guard).
3. **No change**: the App fails only after its upgrade timeout. Rejected.

## Decision

1. **One check, two callers.** `WorkflowSpec.UndeclaredStepStore` returns the first image step (`function.image` set)
   whose `function.kv` names a store that `kv` does not declare. `checkStepStores` uses it and keeps its
   `KVStoreNotOwned` refusal, so the materializer's guard (ADR-0178 Decision 2) is unchanged.
2. **Refused at apply.** A Validating admission, `workflow-step-stores`, handles Workflow creates and updates and
   answers a hit with `fault.Invalid` (400): `spec.steps[i].function.kv[j].store "<store>" is not declared in
   spec.kv`. It is registered among the part admissions (`partAdmissions`, `pkg/funcd/funcd.go:1150`), so
   `app-parts` (ADR-0199 Decision 3, `internal/app/admission.go:84-98`) also runs it on each Workflow part an App
   declares (`ref` entries excluded) and wraps its refusal as for any part: `spec.workflows[k] (Workflow/<name>):
   <message>`.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/workflow.go

// UndeclaredStepStore reports the first image step binding a KV store that s.KV does not declare: its index in
// s.Steps, the binding's index in its function.kv, and the store (ADR-0229); ok is false when there is none.
func (s WorkflowSpec) UndeclaredStepStore() (step, binding int, store ObjectName, ok bool)

// internal/controlplane/admission/workflow.go

// NewWorkflowStepStoresAdmission returns the Validating admission "workflow-step-stores" (ADR-0229).
func NewWorkflowStepStoresAdmission() Admission
```

| Request | Outcome |
|---|---|
| Workflow create or update; an image step binds a store `spec.kv` does not declare | 400, `spec.steps[i].function.kv[j].store "<s>" is not declared in spec.kv` |
| App create or update; a declared Workflow part has such a step | 400, `spec.workflows[k] (Workflow/<name>): <the Workflow refusal>` (through `app-parts`) |
| anything else | allowed (unchanged) |

| Consumes | Exposes |
|---|---|
| `admission.Admission`, the `partAdmissions` registration, `app-parts` | the admission `workflow-step-stores` |
| `WorkflowSpec` (`kv`, `steps[].function`) | `WorkflowSpec.UndeclaredStepStore` |

## Implementation plan

1. `api/types/v1alpha1/workflow.go`: `UndeclaredStepStore`; `internal/workflow/reconcile_workflow.go`:
   `checkStepStores` calls it (same error).
2. `internal/controlplane/admission/workflow.go`: the admission; register it in `partAdmissions`
   (`pkg/funcd/funcd.go`, beside `NewWorkflowRunPayloadAdmission` at `:1177`).
3. Tests: unit tests of `UndeclaredStepStore` (declared, undeclared, no image, first hit wins) and of the admission
   (Workflow create and update, delete untouched); one `TestScenario<Name>` per scenario with `// scenario: <name>`
   in `pkg/funcd`; `stored-workflow-still-refused` writes the Workflow through the store, so admission does not run.

**Definition of done**: the tests pass; ADR-0178's scenario tests and `TestScenarioAppAdmissionRefuses` pass
unchanged; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] One check, used by the admission and by `checkStepStores`; no second copy of the loop.
- [ ] The admission handles Workflow create and update only, is registered in `partAdmissions`, and answers 400 with
      the field path; an App's part refusal comes through `app-parts` unchanged.
- [ ] `Workflow.Validate`, `App.Validate` and the materializer's behavior are unchanged.
- [ ] One test per scenario, named for it; ADR-0178's scenario tests unchanged and green.

## Consequences

- A Workflow or App that binds an undeclared store is refused when it is applied, with the field to fix, instead of
  after a reconcile or an App upgrade timeout.
- An API update of a Workflow or App stored before this change that still binds an undeclared store is refused until
  the update fixes the binding; so is an App rollback to an AppRevision with such a binding. The same spec failed at
  reconcile anyway.
- A writer that bypasses the API still meets only the materializer's check.

## Open questions

None.

## References

- ADR-0178 Decision 2; ADR-0063; ADR-0199 Decisions 3 and 9; ADR-0213 Decisions 7 and 9; ADR-0220; ADR-0121.
- The #942 review (`docs/reviews/issue-927-fix-claude-opus-5-5-w13.md`), which found that ADR-0178 places the rule in
  the materializer on purpose.
