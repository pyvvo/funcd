# ADR-0166: Workflow edge typing for skipped branches and void inputs

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, contracts, type-checking, reconcile-gate, expression
- **Realizes**: [FEAT-0005/F65](../feat/0005-feat-workflow-engine.md) (typed edges: a Ready workflow's runs never
  fail on a typing error the reconcile gate could see)
- **Supersedes (in part)** (`Superseded in part by: ADR-0166` back-link at acceptance; status stays `Implemented`):
  [ADR-0095](0095-reference-engine-typed-paths-predicates.md) *The guard rule*, "references whose path **equals or
  extends X** are exempt from the defaults rule" (lines 127–129): when X is an optional root, the guard exempts only
  references whose ident path is X (Decision 1.3; Open question 1) ·
  [ADR-0098](0098-typed-workflow-edges.md) Decision 1, "A **fan-in** B … is checked against the composite schema"
  (lines 109–110): a `join: any` fan-in is checked against each single-branch composite (Decision 3).
- **Refines** (no back-link): ADR-0098 Decision 1, the edge rule (lines 106–109: a void input takes only a void
  output; a fan-in is every step with two or more parents) and the `when:` rule (lines 111–113: a `join: any` step's
  parent roots are optional) · [ADR-0094](0094-workflow-engine-core.md) *Control flow*, "referenced optional fields
  must carry a schema `default` or be guarded with `!== undefined`" (lines 147–148: for a `join: any` step's parent
  roots only the guard suffices, Decision 1.2; no back-link: the rule's text still holds for fields, and only the
  root's requiredness changes, by Decision 1), and Open question (a) (lines 504–506), answered by Decision 3 ·
  ADR-0095 *The defaults rule* (lines 122–125: under an optional root only R's guard suffices), *Static checking*
  (lines 141–144), the `Resolver` contract (lines 167–173: a resolver may report an optional root), and "the root
  documents bound as globals" (lines 82–83: an absent root is `undefined`).
- **Builds on**: PR #606 (f3ccaa5): void-step `spec.params` and void `onFailure` (ADR-0094 lines 134–135, 188–189).
- **Relates to**: [ADR-0090](0090-mandatory-single-io-schema.md) (void is `{"type":"null"}`, lines 56–58) ·
  [ADR-0096](0096-engine-native-builtin-steps.md) (builtins checked at run time) · [ADR-0099](0099-sub-workflows.md)
  (sub-workflow edges use `checkEdge`, line 130)

## Context & Need

The reconcile gate (ADR-0098 lines 14–17, 276–277) promises a Ready Workflow never fails a run on a typing error it
could see. Its model differs from what the engine sends (#121, #542; reproduced on main 1193be6, whose lines are
cited, except PR #606's code, cited at f3ccaa5, which shifts `reconcile_workflow.go` by up to 13 lines):

- **Every parent is assumed to have run.** `whenSchemaResolver` types every parent root as present
  (`internal/workflow/condition.go:45-49`) and `compositeSchema` marks every fan-in key required
  (`contract.go:51-61`), but a `join: any` merge runs with a branch skipped, binding (`condition.go:203-207`) and
  composing (`engine.go:909-914`) only the parents that ran. With `a → hi (v > 5) | lo (v <= 5) → merge (join: any)`
  and `a` returning `{"v":9}`, any `when` reading `step.lo.output` passes reconcile and fails the run (`reference
  "step.lo.output" is not rooted`, `internal/expr/check.go:412-414`); `Eval` binds a missing document as `null`
  (`eval.go:68-73`), so even a guarded read throws `TypeError`. A merge requiring both branches gets `{"hi":{…}}`.
- **A void input is assumed to accept anything.** `checkEdge` returns no diff for a void consumer
  (`contract.go:27-29`), but the engine sends the parent's output verbatim, or a composite for 2+ parents
  (`engine.go:902-918`): `{"rows":3}` into a void `b` is Ready, then 422; two void producers into a void `b` give
  `null` on a fresh run only because the marshal error is dropped (`engine.go:915`), `{}` (422) after a Resume
  (`engine.go:452-453`).

The gate must type what the engine sends: a branch that may be skipped is optional, and a void input takes only null.

## Scenarios

The DAG `a → hi | lo → merge`: `hi` has `when: ${{ step.a.output.v > 5 }}`, `lo` has
`when: ${{ step.a.output.v <= 5 }}`, `merge` has `join: any`; every output declares a required number `v`.

- `scenario: join-any-unguarded-branch-read-refused` — Given `merge` with `when: ${{ step.lo.output.v > 0 }}` (or
  `${{ step.lo.output.v !== undefined }}`, or a read of a defaulted field of `lo`), When it reconciles, Then it is
  not Ready: `SchemaMismatch`/`WhenTypeError`, the message naming step `merge` and `step.lo.output`.
- `scenario: join-any-guarded-branch-read-runs` — Given `a` that echoes its input, and `merge` with
  `when: ${{ (step.hi.output !== undefined && step.hi.output.v > 0) || (step.lo.output !== undefined &&
  step.lo.output.v > 0) }}` and an input that requires no branch, When it reconciles and a run starts with `{"v":9}`,
  Then it is Ready, `lo` is Skipped, `merge` is dispatched once with `{"hi":{"v":…}}` only, and the run Succeeds.
- `scenario: join-any-merge-requiring-both-branches-refused` — Given `merge` whose input requires `hi` and `lo`
  (or only `lo`), When it reconciles, Then `SchemaMismatch`/`EdgeTypeMismatch` naming `merge` and the branch.
- `scenario: object-into-void-step-refused` — Given `a` with output `{rows: integer}` and `b` (dependsOn `a`) with
  input `{"type":"null"}`, When it reconciles, Then `SchemaMismatch`/`EdgeTypeMismatch`:
  `edge into step "b": input is object, want null`.
- `scenario: void-fan-in-into-void-step-refused` — Given `x` and `y` with output `{"type":"null"}` and `b` (dependsOn
  `x`, `y`; `join: all` or `any`) with input `{"type":"null"}`, When it reconciles, Then
  `SchemaMismatch`/`EdgeTypeMismatch` naming `b`.

## Scope

In: typing a `join: any` step's parents (`when` roots, input composite) at reconcile and run time; the void-input rule;
the `internal/expr` rules this needs.

Out: `spec.params` on a void step and a void `onFailure` handler (PR #606); ADR-0094 Open question (b) (stays open);
ordering-only edges (B2; a later ADR if users need them); builtin `pass`/`wait` expressions, checked at run time
only (ADR-0096); and these adjacent gaps:
1. a nested probe `a.b.c !== undefined` with optional `a.b` passes Check and fails at run time (a /fix, ADR-0095);
2. a void producer into an object input that requires nothing passes `checkEdge` (a /fix, ADR-0098 lines 107–108);
3. `compositeSchema` types a void parent's key as a required `object`, but `flowingInput` drops the marshal error
   (`engine.go:915`) and sends `null`; after a Resume the key is absent (a /fix);
4. a field read under a null-typed root passes Check (`schemaResolver` answers a schema without `properties` with a
   permissive `string`, `condition.go:81-83`) (a /fix);
5. a guard on a required root or on a field still exempts every extension (ADR-0095 lines 127–129, `check.go:50-57`),
   so an optional unguarded field fails at run time (`check.go:432-435`); a follow-up ADR (Open question 1);
6. accepted gap: an untyped producer into a void input — a lone builtin parent (ADR-0096), or a `workflow:` step whose
   child has an untyped leaf and so no output schema (`contract.go:117-118`), which counts as void;
   the consumer answers 422 (Open question 3).

## Constraints & Decision drivers

- ADR-0098: typing errors surface at reconcile, from cached contracts, in O(edges × fields), with no registry I/O.
- ADR-0095: one positional guard (`X !== undefined && …`), no truthiness, no three-valued logic; goja's parser AST.
- ADR-0094: `join: any` fires once all parents are terminal and one Succeeded; its composite holds only the parents
  that ran (lines 149–152); data flows verbatim (lines 131–133). ADR-0090: a void input takes only null.
- Tekton, Argo and Step Functions (References) settle a missing branch at run time; funcd settles it at reconcile.

## Alternatives considered

| Option | Outcome |
|---|---|
| **A1. Optional roots: guard required at reconcile, `undefined` at run time** | **chosen**: merge conditions on branch data keep working; reuses ADR-0095's guard |
| A2. A `join: any` step's `when` reads only `input` | rejected: no merge condition on branch data |
| A3. Skip the merge when its `when` reads a skipped branch (Tekton) | rejected: silent; contradicts ADR-0094 lines 149–152 |
| Keep binding an absent root as `null` | rejected: `null !== undefined` is true, so the guarded read throws |
| Optional chaining `?.` as a guard | rejected: relies on three-valued logic ADR-0095 excludes |
| A field `default` stands in for a skipped branch | rejected: a skipped branch reads as if it had produced its defaults |
| Optional only for a parent that may be skipped (skippability analysis) | rejected: more code; flips when an upstream `when` is added |
| **B1. An object or composite into a void input is refused** | **chosen**: conforms to ADR-0090, ADR-0094, ADR-0098; smallest change |
| B2. Send null to a void step (ordering-only edge, Argo, Step Functions) | rejected: drops data silently; changes ADR-0094 lines 131–133 |

## Decision

1. **Optional roots (A1).** On a `join: any` step with two or more parents, every parent root `step.<p>.output` is
   optional at reconcile; a single-parent `join: any` step runs only when its parent Succeeded, so its root stays
   required. For an optional root R, `Check` enforces:
   1. R itself may be probed anywhere: `R !== undefined`, `R === undefined`.
   2. Any longer reference (a field, an index, or a deeper existence probe) is valid only in the right operand of
      `R !== undefined && …` (ADR-0095's positional guard, X = R); a field `default` never stands in for an absent R.
      Elsewhere `Check` fails: at reconcile `SchemaMismatch`/`WhenTypeError`.
   3. The guard on R exempts only references whose ident path is R (`R`, `R[i]`, `R.length`), not its extensions; a
      field under R still needs to be required, to declare a `default`, or to carry its own guard.

   `!== undefined` is the only guard form; `?.` stays refused; `R !== undefined && A && B` leaves `B` unguarded
   (write `R !== undefined && (A && B)`). A schema-backed resolver reports an optional root with a non-empty `Type`
   (the schema's own, or `object` for `{}`, a bare `{properties:…}`, a `oneOf`) and `HasDefault` false, even with a
   top-level `default` (`condition.go:97-100`): a zero `Type` reads as absent (`expr.go:80`) and ADR-0095's
   short-circuit (`check.go:164-167`) would skip the guarded operand.
2. **`undefined` at run time.** The run-time resolver exposes every direct parent; one with no recorded output (a
   Skipped branch, or a Succeeded void parent after a Resume or replay, `engine.go:452-453`, `409-410`) is an absent
   root: `Resolve(R, nil)` returns the zero `Field`, a longer path is `NotFound`. `Check` makes one extra
   `Resolve(root, nil)` per longer reference; an error leaves the root required (`condition.go:256`, `261-264`,
   `283-284`); only a nil error with `Required: false` and no default marks it optional. `Eval` binds a referenced
   root with no document as JS `undefined`, not `null`; every current caller (`docResolver`, sensor `eventResolver`)
   passes a document for every root it exposes, so no existing expression changes. `docResolver` answers
   `Resolve(root, nil)` for a document starting (after spaces) with `{` as a required `object` without decoding it
   (`inferField` decodes the whole document, `condition.go:256-264`).
3. **Per-branch composite.** Reconcile checks a `join: any` merge input against each single-branch composite
   `{<p>: object}`, one per parent (adding keys only removes misses, so this covers every set of survivors); a merge
   input requiring a branch key not supplied by `spec.params` is `EdgeTypeMismatch`. A `join: all` fan-in keeps one
   composite with every key required. A fan-in is every step with two or more parents, typed or not, as the engine
   builds it (`engine.go:906-917`; `producerSchema` counts only typed ones, `reconcile_workflow.go:501-520`).
   This answers ADR-0094 Open question (a).
4. **Void input (B1).** In `checkEdge`, a `{"type":"null"}` input accepts only a producer whose `type` is `null` or
   whose output schema is absent (ADR-0098 lines 107–108) — not `SchemaView.IsVoid()`, which also counts `{}`
   (`api/types/v1alpha1/contract_check.go:64-66`). Any other producer, every fan-in composite included, gives
   `FieldDiff{Want: "null", Got: <its type, or object when it declares none>}` (`EdgeTypeMismatch`). The short-circuit
   at `contract.go:27-29` is removed; an absent or `{}` input still requires nothing; there are no ordering-only
   edges. This rule now refuses the FailureContext into a void `onFailure` handler, so #606's `objectIntoVoid` call
   there (f3ccaa5 `reconcile_workflow.go:410-413`) is dropped; the `spec.params` call stays (the overlay makes the
   input an object, f3ccaa5 `reconcile_workflow.go:391`).
5. **No new condition reason.** The messages are in Contracts.

## Temporary workarounds

None.

## Contracts

`internal/expr` (no exported change):

```go
// Resolver (unchanged): Resolve(root, nil) with Required: false and no default marks an OPTIONAL root (ADR-0166);
// only a document-backed resolver reports an absent root, as the zero Field.
// Check: one extra Resolve(root, nil) per longer reference; applies Decision 1.2 and 1.3.
// run (eval.go:68-73), for each referenced root:
raw, ok := docs[root]
if !ok {
	setNested(globals, splitRoot(root), goja.Undefined()) // an absent root is undefined, never null
	continue
}
val, err := docValue(raw, e.defaultsFor(root))
```

`internal/workflow` (new names, grepped unused: `optionalRoots`, `absentRoots`, `producerCase`, `onlyBranch`,
`producerSchemas`):

```go
// schemaResolver (condition.go:57-59): whenSchemaResolver fills optionalRoots (Decision 1); the positional
// literal schemaResolver{r.schemas} (condition.go:246) becomes schemaResolver{schemas: r.schemas}.
type schemaResolver struct {
	schemas       map[string]json.RawMessage
	optionalRoots map[string]bool
}
// docResolver (condition.go:220-223): runtimeResolver fills absentRoots; Roots() includes them (Decision 2).
type docResolver struct {
	docs        map[string]json.RawMessage
	schemas     map[string]json.RawMessage
	absentRoots []string
}
// producerCase is one input a step can receive from its parents.
type producerCase struct {
	schema     json.RawMessage
	onlyBranch v1.ObjectName // join: any: the one branch that ran; "" otherwise
}
// producerSchemas replaces producerSchema (reconcile_workflow.go:501-520): none for a root or a lone untyped
// parent; a lone typed parent's output; for 2+ parents one full composite (join: all) or one per parent (any).
func producerSchemas(n *stepNode, contracts map[v1.ObjectName]v1.WorkflowContract) []producerCase

// checkEdge (contract.go:25): signature unchanged; Decision 4 replaces the void short-circuit.
func checkEdge(producer, consumer json.RawMessage, providedByParams map[string]bool) []v1.FieldDiff
```

| Case | Reason | Message |
|---|---|---|
| Unguarded read of an optional root | `WhenTypeError` | ``step "merge" when: expr.check: "step.lo.output.v" reads "step.lo.output", which may be absent: guard it as `step.lo.output !== undefined && (…)` (position N)`` |
| Optional field under a guarded root | `WhenTypeError` | the existing message (`check.go:460`), ``optional field "step.lo.output.w" must declare a default or be guarded with `!== undefined` `` |
| Merge input requires a branch | `EdgeTypeMismatch` | `edge into step "merge" when only "hi" ran (join: any): "lo" (want object) is missing` |
| Object or composite into a void input | `EdgeTypeMismatch` | `edge into step "b": input is object, want null`; a `join: any` fan-in names its first parent in `dependsOn` order: `edge into step "b" when only "x" ran (join: any): input is object, want null` |

Consumes the cached contracts (ADR-0098), the ADR-0095 checker and goja; exposes no new field, reason or dependency.

## Implementation plan

1. `internal/expr`: `check.go` (extra root lookup, optional-root rule in `resolveRefExpr`, guard exemption),
   `eval.go` (`undefined` binding), `expr.go` (`Resolver` comment). `expr_test.go`: `TestCheckOptionalRoot` (each
   Decision 1 case; `R !== undefined && R[0] > 0` and the parenthesized conjunction accepted; `?.` refused; a lookup
   error leaves the root required) and `TestEvalAbsentRootIsUndefined`.
2. `internal/workflow`: `condition.go` (`optionalRoots`, `absentRoots`, the object shortcut, the named literal);
   `contract.go` (void rule); `reconcile_workflow.go` (`producerSchemas`, the edge loop and messages, the onFailure
   `objectIntoVoid` call dropped). `contract_test.go:57-60` flips to `input is object, want null`; `TestCheckEdgeVoid`
   adds a `{}` producer into a void input (`input is object, want null`) and an object into a `{}` consumer (no diff).
   `reconcile_contract_test.go`: `TestWhenTypelessOptionalRoot` (`{}`, bare `{properties:…}` and top-level-`default`
   branch outputs; `step.lo.output !== undefined && step.lo.output.nope > 0` and an unguarded read of the defaulted
   root refused; a single-parent `join: any` step reading `step.p.output.v` unguarded is Ready) and
   `TestFanInWithUntypedParent` (typed `a` and builtin `pass` `p` into `c`: requiring `a` and `p` is Ready, requiring
   `a`'s `rows` is `EdgeTypeMismatch`).
3. Scenario tests in `internal/workflow/reconcile_contract_test.go` (`fakeContracts`, `reconcileSpec`, `newTestEngine`):
   `TestScenarioJoinAnyUnguardedBranchReadRefused` (a subtest per variant), `TestScenarioJoinAnyGuardedBranchReadRuns`
   (reconcile, then `Execute`), `TestScenarioJoinAnyMergeRequiringBothBranchesRefused`,
   `TestScenarioObjectIntoVoidStepRefused`, `TestScenarioVoidFanInIntoVoidStepRefused`.
4. Documents: this ADR's change edits the F65 row in `docs/feat/0005-feat-workflow-engine.md` — ADR cell adds
   `(+ [ADR-0166](../adr/0166-workflow-branch-and-void-typing.md) — skipped branches and void inputs)`, status
   `contract gate: implemented · branch/void typing: adr` (precedent F13, F57, F85), then follows this ADR. At
   acceptance ADR-0095 and ADR-0098 get the `Superseded in part by: ADR-0166` line; F73 adds ADR-0166 to its ADR cell,
   status unchanged. No blueprint change.
5. Definition of done: the scenario and unit tests pass; `TestCheckEdgeVoid`'s void→void case,
   `TestFanInCompositeTypechecks`, `TestWhenTypecheckedAtReconcile`, `TestJoinAnyExclusiveBranch`,
   `TestIssue542_VoidInputRejectsFailureContextAndParams` (subtest `void steps without params` is the control) and
   `TestScenarioWorkflowEndToEnd` (`just ci-full`) stay green; `just ci` green; no `go.mod` change.

## Review checklist

- [ ] Decision 1: roots optional only on a `join: any` step with two or more parents; a schema-backed optional root has
      a non-empty `Type` and `HasDefault` false; a lookup error leaves it required; `R === undefined` alone and
      `R !== undefined && R[0] > 0` accepted; deeper probes, defaulted fields and `?.` refused.
- [ ] Decision 2: only a referenced root with no document is bound `undefined`; no other `Eval` caller changes;
      `docResolver` answers an object root lookup without decoding it.
- [ ] Decision 3: a `join: any` input is checked per branch; a fan-in is any step with two or more parents.
- [ ] Decision 4: a `{"type":"null"}` input accepts only a `null`-typed or schema-less producer (not `IsVoid()`);
      `Got` falls back to `object`; the onFailure `objectIntoVoid` call is gone and #606's tests stay green.
- [ ] Each scenario has one named, passing test; the comment at `reconcile_workflow.go:429-430` (f3ccaa5)
      states what reconcile now guarantees and names the run-time-only cases (builtins; Scope Out (1), (4), (5)).

## Consequences

- Positive: the gate keeps ADR-0098's promise for these cases; a merge condition can read branch data; a fan-in
  requiring a typed and an untyped parent's keys, refused today, becomes Ready.
- Negative: Ready workflows hitting a refusal in Contracts stop being Ready (their runs already failed); none is
  deployed (`v1alpha1`); runs in flight keep pinned contracts; merge conditions get longer (one
  `R !== undefined && (…)` per branch read).
- Risks accepted: a builtin `pass`/`wait` reading a skipped branch, and Scope Out (6), still fail at run time.

## Open questions (for the decider to confirm at acceptance)

1. Decision 1.3 supersedes ADR-0095 lines 127–129 only for an optional root. Keeping "equals or extends" lets an
   optional field in a branch that ran fail at run time; the general rule (Scope Out (5)) needs requiredness relative
   to the guarded path, not the root (`condition.go:74-89`). Proposed: 1.3, the general rule a follow-up ADR.
2. Decision 1: only `!== undefined` guards (plus the `=== undefined` probe); `?.` and an unparenthesized
   `R !== undefined && A && B` are refused; no skippability analysis, so a single-parent `join: any` root stays
   required. Decision 3: a fan-in is every step with two or more parents, typed or not. Proposed as written.
3. Decision 4: only an explicit `{"type":"null"}` input is void; a schema-less consumer stays permissive; an absent
   producer stays void (ADR-0098 lines 107–108). Proposed as written.

## References

- Issues [#121](https://github.com/pyvvo/funcd/issues/121), [#542](https://github.com/pyvvo/funcd/issues/542);
  [PR #606](https://github.com/pyvvo/funcd/pull/606).
- Prior art (read 2026-10-04; each settles a missing branch at run time): <https://tekton.dev/docs/pipelines/pipelines/>,
  <https://argo-workflows.readthedocs.io/en/latest/enhanced-depends-logic/>, <https://docs.aws.amazon.com/step-functions/latest/dg/input-output-inputpath-params.html>.
- goja `v0.0.0-20260701091749-b07b74453ea9`: `vm.Set` of a Go `nil` reads as `null`; `goja.Undefined()` as `undefined`.
