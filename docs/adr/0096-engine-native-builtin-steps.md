# ADR-0096: Workflow step model (`function{image|ref}` · `builtin{wait|pass}` · `workflow`) + engine-native built-in steps

- **Status**: Implemented (2026-07-06)
- **Date**: 2026-07-06 (accepted 2026-07-06 via /adr-batch; judged — reshaped to the three kind-keyed structs, the paused-excluding run-timeout fix folded, plus three Minors: params-vs-builtin semantics, explicit onFailure migration, and the scoped-supersession clarification. Revised 2026-07-06 on decider direction: a `wait` is a **plain blocking builtin step** — no special `Waiting` phase, no yield/`RequeueAfter` park, no `wakeAt`; it runs in-engine to its deadline like any other step. The step model reshape and the `pass` transform are unchanged.)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, orchestration, step-model, built-in-steps, timers, goja
- **Realizes**: FEAT-0005/F64 (the workflow-engine-core row — this reshapes its step contract and adds engine-native steps; tracked by this ADR's own lifecycle)
- **Supersedes**: the **`WorkflowStep` kind-union contract** of [ADR-0094](0094-workflow-engine-core.md) only (the flat `image`/`function`/`workflow` string arms → the three kind-keyed structs below). ADR-0094's orchestration — drive/dispatch mechanics, run state, status mirror, pause/cancel, retention, materialization order, one-drive-to-terminal execution — **stands unchanged** (relates-to). This is a **scoped** supersession: ADR-0094 keeps status `Implemented` and receives a one-line back-link note ("WorkflowStep contract superseded by ADR-0096; orchestration retained") — it is **not** given a terminal `Superseded by` status, because the engine it decided still stands.
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine this reshapes the step model of), [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (the goja evaluator; this adds an object-literal case to its **Select** mode, additively — ADR-0095's substance is unchanged). F66 (governance gates) will reuse the built-in seam for its `gate` step; F70 (sub-workflows) fills the reserved `workflow` kind.

## Context & Need

ADR-0094 modelled a step as a flat kind-union of three strings — `image` (owned, materialized),
`function` (referenced), `workflow` (reserved) — and hung the dispatch knobs (retry, timeout, kv,
blob, secrets, config, catalogs, pooling) directly on the step. Two problems surfaced once the engine
was exercised end-to-end:

1. **`image` alone is not a step kind.** An image and a ref are two *ways to source a function*
   (owned vs. existing), not two different things a step does. Flat strings also can't carry a
   sub-shape, and every new kind adds a top-level arm.
2. **No home for engine-native work.** Waiting (a timer/backoff/delay) and pure data shaping
   (reformat a parent's output) have no representation — a user would have to push and cold-start a
   whole container just to sleep or rename a field. Every serious engine has engine-native states
   (AWS Step Functions `Wait`/`Pass`, Argo `suspend`, Temporal timers).

This ADR reshapes the step into **three kind-keyed structs** (the EventSource-v2/F72 idiom) —
`function{image|ref}`, `builtin{wait|pass}`, `workflow` (reserved) — and adds the **built-in** kind the
engine executes **in-process** (no artifact, no container, no dispatch). A `builtin` step is just a
normal step whose work the engine does itself: `wait` blocks the driver for its duration then Succeeds
(like every other engine's inline `Wait`), `pass` computes a value and Succeeds. There is **no special
run/step state** — a `wait` is `Running` then `Succeeded`, exactly like a dispatched step.

Callers: workflow authors (a `function:`/`builtin:` step in `Workflow.spec.steps`) and the run engine
(which branches on step kind).

## Scenarios

Each becomes a named acceptance test.

- `function-image-materializes` — Given a step `function: { image: <ref> }`, Then reconcile
  materializes the owned Function `<workflow>-<step>` and the run dispatches to it (ADR-0094 behavior,
  new shape).
- `function-ref-dispatches` — Given a step `function: { ref: mailer }`, Then the run dispatches to the
  existing Function `mailer` (no materialization).
- `dispatch-knobs-on-function-only` — Given `function: { image: …, retry: {maxAttempts: 2}, kv: […] }`,
  Then the retry/binding apply; and a `builtin` step **cannot** express retry/timeout/bindings/pooling
  (they are not in its struct — a type/shape rejection, not a runtime check).
- `step-kind-union-validated` — Given a step with both `function:` and `builtin:` (or a `builtin` with
  both `wait:` and `pass:`), Then admission rejects the Workflow (Invalid) — exactly one top-level kind,
  and within `builtin` exactly one of `wait`/`pass`.
- `wait-blocks-then-continues` — Given `a → builtin{wait:"2s"} → b`, When a run starts, Then `a` runs,
  the wait step delays in-engine for ~2s (no container dispatched, `b` not run before then), and the run
  ends `Succeeded` with the wait step `Succeeded` (its output = its flowing input, verbatim).
- `wait-duration-from-expression` — Given `builtin: { wait: "${{ step.throttle.output.retryAfter }}" }`
  whose parent output holds `retryAfter: 1`, Then the engine waits ~1s (the goja Select expression
  evaluates to a number of seconds) and continues.
- `wait-counts-toward-run-timeout` — Given `spec.timeout: 1s` and a step `wait: "5s"`, Then the run
  ends `Failed`/`RunTimedOut` at ~1s: the wait blocks on the run-timeout context, so it is interrupted
  at the deadline rather than sleeping the full 5s.
- `pass-transforms-in-engine` — Given `builtin: { pass: "${{ { total: step.a.output.x + step.b.output.y } }}" }`,
  Then the step produces that object as its output with **no** dispatch (no container), and a
  downstream step receives it verbatim.
- `pass-selects-parent-output` — Given a fan-in `builtin: { pass: "${{ step.review.output }}" }`, Then
  it forwards that parent's output object unchanged (selection, not construction).
- `builtin-reconcile-check-rejects-bad-expression` — Given a `pass` (or dynamic `wait`) referencing an
  undeclared field, Then the run fails with the checker error (fail-fast, ADR-0095) — the same
  static-then-eval path as `when`.

Pause/resume/cancel are **unchanged** from ADR-0094 (declarative `spec.paused`/`spec.cancel` observed at
reconcile boundaries) and covered by its scenarios; a `builtin` step adds no new pause/cancel surface —
a blocking `wait` runs to completion, so pause/cancel take effect at the next step boundary.

## Scope

**In:** the three kind-keyed step structs (`FunctionStep{image|ref}`, `BuiltinStep{wait|pass}`,
`WorkflowRef`) replacing ADR-0094's flat arms; moving all dispatch knobs onto `FunctionStep`; the
`builtin` engine-native kind (`wait`, `pass`) run in-process; the additive ADR-0095 Select-mode
object-literal case; migrating every `WorkflowStep` call site + the `examples/js/workflow` example +
the venom lane to the new shape, adding a `wait`/`pass` demonstration.

**Out (follow-ons):** the `gate`/approval built-in + approve verb (**F66** — reuses this seam);
sub-workflow *execution* (**F70** — this only reserves the `workflow` kind); absolute `until:<timestamp>`
waits (relative durations only); a **durable-timer / yield execution model** (a `wait` that unloads the
run and resumes on a `RequeueAfter` timer instead of blocking — the Temporal model; deferred with the
async-execution work, since V1's runs are short and one-drive-to-terminal); a wire-compatible migration
of already-stored Workflows (the engine is unreleased — no stored Workflows to migrate).

## Constraints & Decision drivers

- **ADR-0094's engine is frozen; its step *shape* is not shipped.** The workflow engine landed this
  cycle and is unreleased, so reshaping the step contract now (before any stored Workflow exists) is
  cheap and correct; a superseding ADR is the process-honest way to change a frozen contract.
- **No new dependency.** goja (ADR-0095) is already in; `wait` needs only the standard library timer.
- **A builtin is a normal step.** No new run/step state vocabulary: `wait`/`pass` are `Running` then
  `Succeeded` like any step. This keeps the engine's state machine and the status mirror unchanged.
- **One evaluator.** `wait`/`pass` reuse the ADR-0095 goja engine (same checker, same runtime
  resolver), not a second grammar.
- **Type-enforce the kind rules.** Dispatch knobs live only on `FunctionStep`, so "a builtin has no
  retry/bindings" is a compile-time/shape fact, not a hand-written admission rule.

## Alternatives considered

- **Keep the flat string arms; add `wait`/`pass` as two more top-level strings.** Rejected: leaves
  `image` as a pseudo-kind, proliferates a top-level field per built-in, and can't type-enforce the
  no-dispatch-knobs rule. The kind-keyed structs fix all three.
- **A `builtin:` scheme on the `image` string (`image: builtin:wait`).** Rejected: overloads one
  field with two meanings, needs a companion arg field anyway, and still leaves `image` as a kind.
- **A durable-timer / yield `wait` (park the run between reconciles, `RequeueAfter`).** This is the
  Temporal model and the eventual target for async execution, but **deferred**: it introduces a special
  `Waiting` state, a persisted `wakeAt`, a start-relative run-timeout, and a requeue path — real
  machinery whose payoff (idle waits hold no worker; pause/cancel interrupt a wait) only matters for
  long/scheduled runs, which V1 doesn't have. V1 keeps the engine's one-drive-to-terminal model: a
  `wait` blocks the driver inline. Revisit with the async-execution work.
- **A gate *function* (a container that sleeps).** Rejected: a cold-started container to do nothing is
  the waste built-ins exist to avoid.
- **`pass` via a new mini-language.** Rejected: goja Select already computes values; `pass` reuses it
  (with a small, additive object-literal case).

## Decision

Reshape `WorkflowStep` into a **kind-keyed union of three dedicated structs** — exactly one of
`function` / `builtin` / `workflow`; the orchestration fields (`name`, `dependsOn`, `join`, `when`,
`params`) are common to all kinds:

1. **`function: FunctionStep`** — a **dispatched** step, sourced from an OWNED `image:` (materialized
   into `<workflow>-<step>`, ADR-0094) **or** a `ref:` to an existing Function (exactly one). **All
   dispatch-only knobs move here**: `retry`, `timeout`, `kv`, `blob`, `secrets`, `config`, `catalogs`,
   `pooling`. Dispatch/materialization mechanics are unchanged from ADR-0094 (only the field path
   changes: `st.Image` → `st.Function.Image`, `st.Function` → `st.Function.Ref`).

2. **`builtin: BuiltinStep`** — an **engine-native** step run in-process (no dispatch), a nested
   kind-union (exactly one):
   - **`wait: string`** — a timer that **blocks the driver** for its duration, then Succeeds. A Go
     duration string (`"30s"`) or a `${{ … }}` goja **Select** expression evaluating to a **number of
     seconds** (float; sub-second allowed). The wait blocks on the run's context, so a run-timeout
     interrupts it (wait time counts toward the run timeout); its output is its flowing input, verbatim
     (a pass-through in time). No special phase — the step is `Running` then `Succeeded`.
   - **`pass: string`** — an in-engine data transform: a `${{ … }}` goja **Select** expression over the
     step's flowing input + prior step outputs (ADR-0094 `docResolver`); its result is the step's
     output. No dispatch.

3. **`workflow: WorkflowRef{ ref }`** — a child-workflow reference, **reserved** and rejected until F70.

**Run-timeout keeps ADR-0094's paused-excluding guarantee.** At each drive the run's absolute deadline
is `runDeadline = StartedAt + spec.timeout + PausedNanos` (unbounded when `spec.timeout == 0`), enforced
by a `context.WithDeadline` over the drive; a `wait` blocks on that context, so it counts toward the
timeout and is interrupted at the deadline rather than sleeping past it.

**`wait`/`pass` expressions reuse the ADR-0095 runtime path** (parse → check against the actual
documents → eval), exactly as `when.condition` does: a bad path/type surfaces as a step failure. To let
`pass` construct objects, the ADR-0095 **Select-mode** checker gains an **object-literal** case
(`{ k: expr }`, typed like the already-supported array literals; **Select only** — Condition mode still
rejects object literals). This is purely additive; `Eval` already returns objects via `Export()`.

The **type system enforces the kind rules**: dispatch knobs exist only on `FunctionStep`, so a
`builtin` step cannot carry them, and the materializer only ever sees `st.Function.Image` steps.

`params` stays on `WorkflowStep` (orchestration-common): for a `function` step it overlays the
dispatched input (ADR-0094, unchanged); for a `pass` step the goja expression sees the params-overlaid
input; for a `wait` step it is a no-op (the output is the flowing input verbatim).

## Temporary workarounds

- **No absolute `until:`** — relative `wait` only. Exit: F68/F69 add the clock-source/Sensor scheduling.
- **A `wait` blocks a driver goroutine for its duration** — fine for V1's short runs; a long `wait`
  holds a goroutine and cannot be paused/cancelled mid-wait (pause/cancel land at the next step). Exit:
  the durable-timer/yield model, deferred with async execution (see Alternatives).
- **`pass` cannot call functions or use control flow** — a pure ADR-0095 Select expression (member
  selection + the computation subset + object/array literals). Exit: none — richer transforms are a
  `function` step by design.

## Contracts

### Resource — `WorkflowStep` (reshaped; supersedes ADR-0094's flat arms)

```go
type WorkflowStep struct {
	Name      ObjectName      `json:"name"`
	Function  *FunctionStep   `json:"function,omitempty"` // ─┐ exactly one kind
	Builtin   *BuiltinStep    `json:"builtin,omitempty"`  //  ├
	Workflow  *WorkflowRef    `json:"workflow,omitempty"` // ─┘ (reserved, F70)
	DependsOn []ObjectName    `json:"dependsOn,omitempty"` // orchestration — common to all kinds
	Join      JoinMode        `json:"join,omitempty"`
	When      *StepWhen       `json:"when,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// FunctionStep — a dispatched step: an OWNED image (materialized) OR a ref to an existing Function.
type FunctionStep struct {
	Image    string            `json:"image,omitempty"` // ─┐ exactly one source
	Ref      ObjectName        `json:"ref,omitempty"`   // ─┘
	Retry    *StepRetry        `json:"retry,omitempty"`
	Timeout  time.Duration     `json:"timeout,omitempty" minimum:"0" maximum:"86400000000000"`
	KV       []FunctionKV      `json:"kv,omitempty"`
	Blob     []FunctionBlob    `json:"blob,omitempty"`
	Secrets  []ObjectName      `json:"secrets,omitempty"`
	Config   []ObjectName      `json:"config,omitempty"`
	Catalogs []FunctionCatalog `json:"catalogs,omitempty"`
	Pooling  *WorkflowPooling  `json:"pooling,omitempty"`
}

// BuiltinStep — engine-native, a nested kind-union (exactly one). Room for Gate (F66).
type BuiltinStep struct {
	Wait string `json:"wait,omitempty"` // Go duration string OR ${{ }} Select → seconds
	Pass string `json:"pass,omitempty"` // ${{ }} Select → the step's output
}

type WorkflowRef struct {
	Ref ObjectName `json:"ref,omitempty"` // reserved (F70)
}
```

Validation (`Workflow.Validate`, replacing ADR-0094's `validateKind`): exactly one of
`function|builtin|workflow` is set; if `function`, exactly one of `image|ref`; if `builtin`, exactly
one of `wait|pass`; `workflow` is rejected (reserved). Field-shape (durations, retry bounds, non-empty
strings) is huma tags; the kind-union rules are `Validate()`. **No new step phase or run-state field** —
`StepPhase` and `RunStepStatus`/`StepState` are unchanged from ADR-0094 (a `wait` is `Running` then
`Succeeded`).

### Engine — the builtin contract (internal/workflow)

```go
// drive() branches on step kind: dispatch a *FunctionStep (unchanged mechanics), else run the builtin
// in-engine. runBuiltin blocks for a wait (on ctx, so the run-timeout interrupts it) or evaluates a
// pass, then the step Succeeds — the same control flow as a dispatched step (no new return value, no
// park). One drive still runs to a terminal phase (ADR-0094).
func (e *Engine) runBuiltin(ctx context.Context, st *v1.WorkflowStep, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error)
func (e *Engine) evalWait(n *stepNode, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (time.Duration, error) // duration string or Select→seconds
func (e *Engine) evalPass(n *stepNode, raw string, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) // Select → output
```

`RunReconciler.Reconcile` is unchanged from ADR-0094 (no `RequeueAfter` path): a run drives to a
terminal phase in one reconcile; pause/cancel are the declarative `spec.paused`/`spec.cancel` checks it
already has. Run-timeout at each drive: `runDeadline = StartedAt + spec.timeout + PausedNanos`, enforced
by `context.WithDeadline`.

### Reference engine — Select-mode object literals (internal/expr, extends ADR-0095 additively)

`checkObjectLiteral(*ast.ObjectLiteral, ctx) (typ, error)` — mirrors the existing `checkArrayLiteral`:
each value checked in-context, keys are string/identifier literals, node types to `kindObject`.
Admitted **only in Select mode** (Condition still rejects); an empty object literal is rejected. To make
a bare `{…}` parse as an expression (JS parses it as a block otherwise), `Parse` wraps a leading-`{`
source in parens — the `() => ({…})` idiom; safe because a leading `{` is only ever an intended object
literal here. `Eval` is unchanged (returns objects via `Export()`). ADR-0095 behavior is otherwise
untouched.

### Example (illustrative) — the reshaped shape with `wait`/`pass`

```yaml
apiVersion: funcd.io/v1alpha1
kind: Workflow
metadata: { name: orders, namespace: default, resourceGroup: rg1 }
spec:
  timeout: 30m
  steps:
    - name: charge
      function: { image: oci-layout:///mnt/funcd-deps/registry:charge }   # owned, materialized
    - name: settle-window
      builtin: { wait: "3s" }                                             # engine-native timer (blocks)
      dependsOn: [charge]
    - name: receipt
      builtin: { pass: "${{ { id: step.charge.output.id, settled: true } }}" }   # engine-native transform
      dependsOn: [settle-window]
    - name: notify
      function: { ref: mailer, retry: { maxAttempts: 3 } }                # existing Function
      dependsOn: [receipt]
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0094 engine (drive loop, run record, status mirror, materializer, dispatcher, pause/cancel) | the reshaped `WorkflowStep` (`function`/`builtin`/`workflow` structs) |
| ADR-0095 goja `Select` evaluator (+ the additive object-literal case) | the `builtin` kind (`wait` blocking timer, `pass` transform) |
| the standard-library timer + the drive context (run-timeout) | no new resource kind, no new step state, no new dependency |

## Implementation plan

Files: `api/types/v1alpha1/workflow.go` (`FunctionStep`/`BuiltinStep`/`WorkflowRef` + the reshaped
kind-union validation + huma `Schema`), `internal/workflow/engine.go` (drive branch on kind → dispatch
vs `runBuiltin`; field-path migration; run-timeout unchanged), `internal/workflow/condition.go`
(`runBuiltin`/`evalWait`/`evalPass` reusing the Select `docResolver`),
`internal/workflow/reconcile_workflow.go` (materializer reads `st.Function.Image`),
`internal/workflow/state.go` / `dispatch.go` (field-path migration), `internal/expr/{expr,check}.go`
(`checkObjectLiteral` + the bare-`{` parse wrap). No `go.mod` additions, **no** `workflowrun.go` /
`runstate.go` change (no new phase or state field).

Migration (call sites): every `st.Image`/`st.Function`/`st.Retry`/`st.Timeout`/`st.KV`/`st.Blob`/
`st.Secrets`/`st.Config`/`st.Catalogs`/`st.Pooling` reference in the engine, reconcilers, and tests →
the `st.Function.*` / `st.Builtin.*` paths — **including `stepTarget` and the `onFailure` handler**,
which is itself a `function` step now (image or ref); the `examples/js/workflow` manifests
(`workflow.yaml`, `run-pause`, `run-cancel`) to `function: { image: … }`; the `pkg/funcd` in-process
workflow e2e handlers/steps to the new shape.

Example + e2e (in scope): rework `examples/js/workflow` to the new step shape and add a `builtin` `wait`
+ `pass` step; extend `e2e/workflow.venom.yml` + `scripts/lanes.yaml` to assert the wait/pass steps run
and Succeed. Pause/resume/cancel venom coverage is the existing declarative case (unchanged).

Test plan — one named test per Scenario (`function-image-materializes`, `function-ref-dispatches`,
`dispatch-knobs-on-function-only`, `step-kind-union-validated`, `wait-blocks-then-continues`,
`wait-duration-from-expression`, `wait-counts-toward-run-timeout`, `pass-transforms-in-engine`,
`pass-selects-parent-output`, `builtin-reconcile-check-rejects-bad-expression`). Engine/reconciler tests
use a fake dispatcher (builtins never dispatch) and short `wait` durations so they run fast;
`internal/expr` gets an object-literal check/eval test (Select admits, Condition rejects); the
containerd lane is the real e2e. Definition of done: all scenario tests green; `go build/lint/test/mod`
green; ADR-0094's step contract back-linked as superseded; feat F64 row links this ADR; no identity leak.

## Review checklist

- [ ] `WorkflowStep` is the three kind-keyed structs; exactly one of `function|builtin|workflow`;
      `function` exactly one of `image|ref`; `builtin` exactly one of `wait|pass`; `workflow` rejected.
- [ ] Dispatch knobs (retry/timeout/kv/blob/secrets/config/catalogs/pooling) live on `FunctionStep`
      only; a `builtin` step cannot express them (shape-enforced).
- [ ] Function-image materialization + function-ref dispatch behave as ADR-0094 (new field paths); the
      materializer reads `st.Function.Image`.
- [ ] `wait` blocks in-engine for its duration then Succeeds (output = flowing input); no container
      dispatched; the post-wait step doesn't run before the delay elapses.
- [ ] `wait` accepts a duration string and a goja Select expression (→ seconds); it blocks on the run
      context so the **paused-excluding** run-timeout (`StartedAt + timeout + PausedNanos`) interrupts a
      wait that would outlast it (wait counts toward the timeout).
- [ ] No new step phase or run-state field — `StepPhase`, `RunStepStatus`, `StepState` unchanged; a
      `wait` is `Running` then `Succeeded`; pause/cancel are ADR-0094's declarative path, unchanged.
- [ ] `pass` evaluates a Select expression to its output with no dispatch; object literals admitted in
      Select mode only (Condition rejects); ADR-0095 otherwise unchanged.
- [ ] `wait`/`pass` bad-expression fails the run (the runtime check path, as `when`).
- [ ] ADR-0094's step contract back-linked as superseded, its orchestration untouched; example + venom
      lane reworked to the new shape with a `wait`/`pass` case.

## Consequences

- **Cleaner, typed step model**: a step is one of three kinds, each a struct; `image`/`ref` are two
  ways to source a `function`; dispatch knobs are where they belong. New kinds (F66 `gate`, F70
  `workflow` execution) slot into `BuiltinStep`/`WorkflowRef` without touching the top-level union.
- **New capability**: timers/backoff/delays and in-engine data shaping without a container — cold-start-free.
- **No execution-model change**: a run still drives to a terminal phase in one reconcile (ADR-0094); a
  `builtin` is just a step the engine runs itself. The state machine, status mirror, and pause/cancel
  are untouched.
- **Trade-off recorded**: a `wait` blocks a driver goroutine and can't be interrupted mid-wait (pause/
  cancel land at the next step). Acceptable for V1's short runs; the durable-timer/yield model is the
  documented exit for long/scheduled waits (deferred with async execution).
- **Reference engine slightly wider**: Select mode constructs objects (additive; Condition unchanged) —
  every `${{ }}` consumer inherits it.
- **Supersession, not additive**: this changes ADR-0094's step contract. Because the engine is
  unreleased (no stored Workflows), there is no wire-migration burden; the cost is re-touching the
  engine field paths, the example, and the e2e — all in this ADR's implement gate.

## Open questions

- **Very long waits** — a `wait: 720h` blocks a goroutine for a month and can't be cancelled mid-wait;
  V1 has no such runs, and the durable-timer model (Out of scope) is the answer when it does.
- **`pass` object-literal depth** — nested objects type structurally; bounding nesting depth is an
  implementation detail (default: the array-literal limits).

## References

- [ADR-0094](0094-workflow-engine-core.md) — the workflow engine (step contract superseded here;
  orchestration retained).
- [ADR-0095](0095-reference-engine-typed-paths-predicates.md) — the goja evaluator (Select/Condition;
  extended with an additive object-literal case).
- FEAT-0005/F64 (workflow engine core), F66 (governance gates — reuses the built-in seam), F70
  (sub-workflows — fills the reserved `workflow` kind).
- AWS Step Functions `Wait`/`Pass`; Argo `suspend`; Temporal timers — prior art for engine-native
  states + kind-keyed step models (verified 2026-07-06).
```
