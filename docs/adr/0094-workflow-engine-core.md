# ADR-0094: Workflow engine core — `Workflow`/`WorkflowRun` resources and the state-machine orchestrator

- **Status**: Accepted
- **Date**: 2026-07-05 (accepted 2026-07-05; judged twice — 2 Blockers + 6 Majors folded: companion sequencing, the `exists()` guard rule lives in ADR-0095, onFailure trigger set, `image` asymmetry justified + transitional, two typing rules assigned to F65, scalar equality, root matching; `status.runs` run-link added post-judge. **In-place update 2026-07-05 (process bypass, decider-authorized):** `when.condition` now uses **native JavaScript** on the goja engine (ADR-0095, likewise updated) — a bench (`bench/expr-engine`) showed goja beats the hand-rolled evaluator on every axis and keeps reconcile-time type-checking by walking goja's parser AST; the postfix `.greaterThan(0)` method syntax is replaced by native operators `>`/`===`/`&&` with `!== undefined` as the guard. The engine seam and `StepWhen.Condition string` contract are unchanged. **In-place update 2026-07-05 (process bypass, decider-authorized): cancel is now declarative, not an imperative endpoint.** `funcdctl workflow cancel` sets a new `WorkflowRun.spec.cancel: true` (the same shape as `spec.paused`); the run reconciler observes it on the ADR-0015 controller workqueue (the queue *is* the dispatch FIFO) and calls `Engine.Cancel` + mirrors `Cancelled`. No synchronous `POST …/cancel` route, no `RunCanceller` server dep — the control-plane server stays store-CRUD-only. Consequence: cancel is eventually-consistent (terminal on the next reconcile), consistent with pause. `Engine.Cancel` is unchanged; it is invoked by the reconciler rather than an API handler.)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, orchestration, controller, state-machine, badger, scale-to-zero
- **Realizes**: [FEAT-0005/F64](../feat/0005-feat-workflow-engine.md)
- **Relates to**: [ADR-0015](0015-controller-engine.md) (controller) · [ADR-0016](0016-activator-scale-to-zero.md)/[ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (wake+invoke) · [ADR-0031](0031-oci-artifact-distribution-oras.md)/[ADR-0035](0035-artifact-digest-resolution-at-revision.md) (artifact, digest-at-revision) · [ADR-0059](0059-contract-as-oci-metadata.md)/[ADR-0090](0090-mandatory-single-io-schema.md) (contracts) · [ADR-0064](0064-fn-to-fn-rpc-links.md)/[ADR-0072](0072-kv-as-a-declarative-resource.md)/[ADR-0073](0073-kv-bindings-and-subdomains.md)/[ADR-0091](0091-function-catalog-consumer-binding.md) (binding-as-grant) · [ADR-0065](0065-metastore-badger-engine.md)/[ADR-0066](0066-kv-service-durable-engine.md) (Badger). **Companion ADRs**: [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (the reference engine, F73 — drafted alongside) and the contract gate (F65, next) — this ADR defines the seams they fill and consumes their contracts.

## Context & Need

FEAT-0005 needs an orchestrator that composes functions into durable, typed, multi-step runs — the
DVC replacement (FEAT-0003/F49) is its first use case. Callers are `funcdctl workflow run`, a
hand-applied `WorkflowRun`, and (later) the F69 Sensor; each start door produces one `WorkflowRun`
driven to a terminal phase by a state-machine reconciler. The engine's differentiators over
Temporal/Step Functions/n8n: statically type-checked edges (via the F65 gate this ADR seams),
zero cost between steps (activator scale-to-zero), and declarative resources instead of
workflows-as-code. The blueprint's "rely on NATS" sentence is refined by this decision: the V1
core has **no bus dependency** (sync dispatch; the bus enters with the F69 Sensor) — the blueprint
is synced at acceptance.

## Scenarios

Each becomes a named acceptance test.

- `sequential-run-succeeds` — Given a Ready 3-step chain workflow, When a run starts with valid input, Then steps execute in order, each receiving its parent's output, and the run ends `Succeeded` with the leaf output as run output.
- `fanout-parallel-and-join` — Given steps C and D both `dependsOn` B, When B succeeds, Then C and D dispatch concurrently and E (`dependsOn` C,D) starts only after both succeed, receiving a composite input keyed by parent name.
- `join-any-exclusive-branch` — Given if/else branches with complementary `when` and a merge step with `join: any`, When one branch runs and the other is Skipped, Then the merge step runs with the surviving branch's output in its composite.
- `when-skips-step` — Given a step whose `when.condition` evaluates false, Then it is `Skipped`, its `join: all` descendants are Skipped transitively, and the run still ends `Succeeded`.
- `retry-then-permanent-failure` — Given a step with `retry.maxAttempts: 2` whose function always 500s, Then two attempts are made with distinct attempt IDs, the step goes `Failed`, running siblings are cancelled (fail-fast), and the run ends `Failed`.
- `input-rejected-at-admission` *(lands with the F65 ADR — exercises the contract cache this ADR only seams)* — Given a Ready workflow with a cached contract, When a `WorkflowRun` is applied whose input misses a required field, Then admission rejects it (Invalid) naming the field — zero registry I/O.
- `input-mismatch-fails-run` *(lands with F65)* — Given a `WorkflowRun` admitted while its workflow was not yet Ready, When the run starts and its input violates the now-cached contract, Then the run ends `Failed` fast with `InputSchemaMismatch` — never a silent drop.
- `duplicate-run-name-rejected` — Given an existing `WorkflowRun` name, When a second with the same name is applied, Then admission rejects it (AlreadyExists).
- `contract-defaults-required` — Given a declared `spec.contract` whose input (or output) has an optional property without a `default`, Then admission rejects the Workflow naming the property (total-defaults rule).
- `crash-recovery-resumes-run` — Given a run with one step in flight when the engine restarts, Then recovery re-dispatches that step with a fresh attempt ID and the run completes; no state is lost.
- `cancel-terminates-run` — Given a running step, When `funcdctl workflow cancel <run>` sets `spec.cancel`, Then on the next reconcile the in-flight invocation is abandoned and the step and run end `Cancelled` (declarative, via the controller workqueue — the same path as pause).
- `pause-and-resume-run` — Given a running fan-out, When `funcdctl workflow pause <run>`, Then in-flight steps complete and are recorded but nothing new dispatches and the run shows `Paused`; When `resume`, Then scheduling continues and the run ends `Succeeded` (the run timeout excluded the paused interval).
- `run-timeout-fails` — Given `spec.timeout: 1s` on the workflow and a slow step, Then on expiry the run ends `Failed` with `RunTimedOut`.
- `onfailure-handler-runs` — Given `spec.onFailure` naming a handler step, When the run fails, Then the handler is invoked once with the engine's FailureContext, its outcome is recorded, and the run phase stays `Failed` regardless of the handler's result.
- `scale-to-zero-step-wakes` — Given a step function at zero replicas, When the step dispatches, Then the engine wakes it (ADR-0033) and the step succeeds.
- `undeclared-target-impossible` — Given the engine's dispatcher, Then it can only invoke functions materialized/referenced from the run's pinned spec — an internal request for any other target returns Forbidden and is audit-logged (fail-closed).
- `revision-pinned-mid-run-repush` — Given a running run, When the step's artifact tag is re-pushed and the Workflow re-reconciled, Then the in-flight run keeps executing its pinned digests; only new runs see the new resolution.
- `workflow-status-links-runs` — Given a workflow with one running and one previously succeeded run, Then `Workflow.status.runs.active` lists exactly the running run and the counts read succeeded=1; When the running run finishes, Then `active` empties and its terminal count increments.
- `owned-kv-materialization` — Given `spec.kv` declaring a store whose table owner names a step, Then reconcile materializes function-without-kv → KVStore → function-with-kv (the ADR-0073 cycle, engine-internal), and the step's handler reaches its table via `context.kv`.
- `workflow-contract-derived-and-cached` *(lands with F65)* — Given a Ready workflow, Then `status.contract` holds the derived I/O (combined roots / leaf composite) and `status.steps[]` each step's resolved digest + contract.

## Scope

**In**: the `Workflow`/`WorkflowRun` resources; owned-Function materialization from OCI refs; the
run state machine (phases, joins, skips, retries, timeouts, on-failure, cancel); durable
engine state + recovery; sync dispatch over the wake+invoke seam; spec-as-grant; the
`workflow.*` config keys; `funcdctl workflow run|runs|pause|resume|cancel|describe`; the `runtime`
push-annotation enabler; the *seams* for F65 (contract cache shape) and the reference engine
([ADR-0095](0095-reference-engine-typed-paths-predicates.md), `when.condition`).

**Out** (each a named follow-up): F65 contract-gate rules (companion ADR); F66 **governance
gates / approval** — deferred post-core (no `gate:` step kind, no `Parked` phase, no approve verb
in V1; the step-kind union leaves room); F67 lineage detail; F69/F72 Sensor + EventSource v2;
F70 child-run semantics (the `workflow:` step kind is reserved here, rejected by admission until
F70's ADR); F71 replay; F74 OCI-packaged workflows/promotion; dynamic per-item fan-out; saga;
durable timers; Cedar `workflow::invoke`; run quotas; cron.

## Constraints & Decision drivers

Blueprint: single binary, pure-Go daemon (no cgo), library-first, ports+drivers, default-deny,
`log/slog`, crash-only. Drivers: reuse over rebuild (controller, activator, Badger, artifact,
binding patterns); every run costs zero between steps; every declared shape statically checkable;
at-least-once semantics stated honestly; no new dependencies.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Temporal-style workflows-as-code + deterministic replay | SDK determinism constraints on users, replay machinery, always-on workers; contradicts declarative-resource platform grain. |
| Bus-choreographed steps (F49's original wording) | No bus→function delivery path exists; sync HTTP response *is* the completion signal; a queue adds dedup/consumer machinery with no V1 benefit. Bus enters with F69. |
| Steps reference pre-deployed Functions only | Two-phase deploy UX, dangling name refs, nothing self-contained. Kept only as the `function:` escape hatch for shared services. |
| Run state in `WorkflowRun.status` only (metastore) | Per-transition status churn on the metastore; no transactional write-ahead. Badger = truth, status = coarse view. |
| Sensor-side joins (Argo model) | Duplicates the engine's `dependsOn` join one layer down and needs correlation state V1 doesn't otherwise need. |
| Untyped expression conditions (raw CEL/JS, no checker) | A general evaluator that only runs at runtime defeats reconcile-time checking; ADR-0095 keeps a type-checker over goja's parser AST so a bad condition still fails at reconcile. |
| `go-playground/validator/v10` struct tags for spec validation | Duplicates the established single source (huma schema tags → OpenAPI, ADR-0048 + semantic `Validate()` methods with `api/fault`); its reflection-tag dialect cannot express the cross-field/cross-resource rules these specs need (kind-union exactly-one, step-name edges, owner-is-a-step), and adds a dependency for no new capability. |

No new Go dependencies — license gate trivially satisfied.

## Decision

### Resources & materialization (the Deployment analogy)

`Workflow` owns and materializes its step Functions; `WorkflowRun` is one execution.

- A step is a **kind-as-key union** — exactly one of `image:` (owned Function, the primary
  path) · `function:` (reference an existing Function) · `workflow:` (child run — **reserved**,
  admission-rejected until F70). Further kinds (F66's `gate:`, when it lands) extend the union.
- `image:` is a **single string**: the full artifact reference with the **tag at the string end**
  (container-image style); tag→digest resolves at Revision stamp (ADR-0035). The asymmetry with
  `Function.spec.artifact` (an `ArtifactRef` struct) is **deliberate and transitional**: a step
  declares intent in container idiom, the materializer maps `image` → the owned Function's
  `ArtifactRef.URI` (one direction, no round-trip), and a follow-up ADR converges the resources
  by adding the same `image` string form to `Function.spec` (decider-confirmed intent). Materialized
  Functions are named `<workflow>-<step>`, carry the step's bindings, share the workflow's
  resourceGroup (cascade delete), are **per-workflow not per-run**, and scale to zero.
- **Self-describing artifacts**: `funcdctl push` writes a `dev.funcd.runtime.v1` manifest
  annotation (the runtime name; `--entry` already covers bundle handlers); materialization reads
  runtime/handler/contract from the manifest alone.
- Step bindings reuse the Function.spec shapes verbatim: `kv []FunctionKV`, `blob []FunctionBlob`,
  `secrets`/`config []ObjectName`, `catalogs []FunctionCatalog`.
- **Workflow-level owned state**: `spec.kv` declares KVStores whose table `owner` names a *step*
  (resolved to the materialized Function). Reconcile sequences the ADR-0073 cycle internally:
  Functions without `kv` → KVStore → patch `kv`. `deletion: retain` (default) leaves the store on
  workflow delete — its table owners then name deleted Functions, so it is a **read-only orphan**
  until a workflow (or Function) re-declares ownership; `delete` cascades it.
- **Mutability**: spec edits stamp Revisions via `metadata.generation` (existing machinery);
  in-flight runs are unaffected (pinned at start).
- **Pooling & warmth** (decider-directed addition): `spec.pooling` governs how the materialized
  step Functions are pooled — `mode: shared` (default: same-runtime image steps co-locate in one
  worker pool, ADR-0046) or `mode: isolated` (each step its own solo worker/container) — plus
  `minReplicas` (≥1 keeps warm workers, avoiding step cold-start; 0 scales to zero). A step's own
  `pooling` overrides the workflow default. Materialization maps this onto each owned Function's
  `spec.pooling.worker` + `spec.scaling.minReplicas`.
- **Declared contract — inline, total defaults**: `spec.contract` is authored **inline in the
  spec** (the ADR-0090 ContractBlob shape; no sidecar file, no flag — the manifest is
  self-contained). Admission enforces **total type safety on the declared contract**: every optional property in
  `contract.input` *and* `contract.output` must declare a `default` — so boundary fields are
  always present (absent inputs are default-filled at run start; absent leaf outputs are
  default-filled before the run output is recorded) and ADR-0095 references against the workflow
  boundary never need `exists()`.

### Input model

Run input is `json.RawMessage`, capped by `workflow.payloadLimit` (default 256 KiB) — payloads are
control-plane metadata; bytes travel by reference (blob/S3 keys). Flow: roots get the run input
verbatim; a single-parent step gets its parent's output verbatim; a multi-parent step gets a
composite keyed by parent step name; the run output is the leaf
output (single) or leaf-keyed composite. `params` is a static overlay (static wins), checked against
the step's input schema at reconcile. Mid-DAG parameters thread through step contracts (V1 idiom;
computed mapping is F73-V2). Admission validates run input against the cached `status.contract`
(zero registry I/O); a run admitted before the workflow was Ready validates at start and fails
fast with `InputSchemaMismatch`.

### Control flow

List order chains implicitly (no `dependsOn` ⇒ previous step). `dependsOn` declares edges;
fan-out **is** parallel dispatch. `join: all` (default) requires every parent `Succeeded`;
`join: any` requires one (exclusive-branch merges). `when.condition` holds an ADR-0095 expression
(Condition mode: a native-JavaScript boolean over the goja engine) — roots `step.<name>.output`
(direct parents only) and `input`; the expression must type as boolean against the cached
contracts (no truthiness — `&&`/`||` require booleans, `===` same-type scalars); referenced
optional fields must carry a schema `default` or be guarded with `!== undefined`. `when` false ⇒ `Skipped`; skip cascades through `join: all`, does not block
`join: any`; a `join: all` step with any `Skipped` parent is `Skipped`. **`join: any` fires only
once all parents are terminal and ≥ 1 `Succeeded`** (deterministic — no first-past-the-post race
when branch conditions are not mutually exclusive); all parents `Skipped` ⇒ the join step is
`Skipped`; its fan-in composite contains only the parent(s) that ran.

### Execution, state, recovery

Dispatch reuses the eventing seam: wake (ADR-0033) then HTTP POST at the upstream; the response is
the completion signal. The engine is the **sole retry owner**: retryable = transport errors,
timeouts, 5xx; permanent = 4xx (contract rejection, Forbidden). Per-step `retry` (`maxAttempts`,
`backoff`, exponential), per-step `timeout`, and `spec.timeout` for the whole run (`RunTimedOut`). Semantics are **at-least-once**: a write-ahead intent precedes every dispatch
and the attempt ID reaches the function (`X-Funcd-Attempt` header; also CloudEvent extension) so
steps can dedupe. State lives in an engine-owned Badger instance at `<dataDir>/workflow` (Badger =
truth); `WorkflowRun.status` mirrors coarse phase + per-step summaries per transition. The
engine also maintains the **run link on the parent**: `Workflow.status.runs` (the CronJob
`status.active` pattern) — the names of all `Pending`/`Running`/`Paused` runs, newest first,
plus lifetime terminal-phase counts; updated on every run transition and **bounded** (only
active runs are enumerated; history stays in engine state behind `workflow runs`/retention). Recovery
scans open runs on boot and re-dispatches in-flight steps with fresh attempt IDs. Closed runs are
swept after `workflow.retention` (default 720h).

**Pause/resume** is declarative: `WorkflowRun.spec.paused: true` (set by `funcdctl workflow
pause`, cleared by `resume`) moves a `Running` run to `Paused` — no new steps dispatch; in-flight
invocations run to completion and are recorded (graceful, unlike cancel). Resume re-enters
`Running` and schedules ready steps. Pause also blocks **retry re-dispatches** (a pending backoff
freezes and resumes with the run). The run-level timeout clock **excludes** time spent Paused;
`cancel` is allowed from Paused; a Paused run survives restarts (it is just state).

### Failure, cancel, on-failure

Step failure is **fail-fast**: running siblings are cancelled, downstream is skipped, the run ends
`Failed`. `cancel` is **declarative** (`spec.cancel: true`, the pause shape): the run reconciler
observes it on the controller workqueue and **abandons** in-flight invocations (idempotency is
already required by at-least-once), terminating the run `Cancelled` on that reconcile. `spec.onFailure` names a handler
step (defined in `steps`, excluded from the DAG: no `dependsOn` into/out of it, no `when`);
it fires **iff the run ends `Failed`** — step exhaustion, `RunTimedOut`, and an
`InputSchemaMismatch` fast-fail (then `failedStep` is empty) all qualify; **`Cancelled` does
not** (user intent). It dispatches **after** fail-fast settles (siblings cancelled and
recorded), once, with the engine-defined FailureContext (Contracts); its outcome is recorded
but never changes the run phase; its input schema must be satisfiable by FailureContext
(checked at reconcile).

### Grants & identity

The pinned spec is the grant: the dispatcher invokes only Functions materialized from or referenced
by the run's pinned spec — anything else is Forbidden + audit-logged. The acting principal is the
Workflow ref (never ambient daemon identity). All references are same-namespace. Cedar
`workflow::invoke` governance is V2.

### Wiring

`internal/workflow` hosts the engine + both reconcilers, registered on the ADR-0015 controller.
`pkg/funcd` gains options + lifecycle (start engine, stop on shutdown). Config: `workflow.retention`,
`workflow.payloadLimit`, `workflow.defaultStepTimeout` (300s), `workflow.defaultRetry` (maxAttempts 1
= no retry), data dir fixed at `<dataDir>/workflow`. `funcdctl workflow
run|runs|pause|resume|cancel|describe` are sugar over the WorkflowRun CRUD surface: `pause`/`resume`
patch `spec.paused`, `cancel` patches `spec.cancel` (all declarative — the reconciler acts on the
controller workqueue), `describe` reads engine state through the API server (never Badger directly),
and `runs <workflow>` lists that workflow's WorkflowRuns (a `spec.workflow`-filtered list,
newest first, `--phase` filterable). `funcdctl get workflows|workflowruns` work like any
resource (the workflow list shows an ACTIVE column from `status.runs`; the run list shows a
WORKFLOW column from `spec.workflow`).

## Temporary workarounds

| Workaround | Exit criterion |
|---|---|
| Mid-DAG parameters thread through step contracts | F73-V2 computed field-mapping ADR |
| Dispatcher-internal grant enforcement (no PDP) | Cedar `workflow::invoke` consumer ADR (V2) |
| `workflow:` step kind admission-rejected | F70 child-run ADR |

## Contracts

### Resource shapes

```yaml
apiVersion: funcd.io/v1alpha1
kind: Workflow
metadata:
  name: orders-report
  namespace: default
  resourceGroup: rg1
spec:
  timeout: 3600s                # optional run-level bound
  onFailure: notify-ops         # optional handler step (defined below, outside the DAG)
  contract: {}                  # optional declared I/O (validated by the F65 gate; omitted ⇒ derived)
  kv:
    - name: counters-kv
      deletion: retain
      tables:
        - name: table-counters
          owner: ingest         # a STEP name, resolved to <workflow>-<step>
  steps:
    - name: ingest
      image: oci-layout:///mnt/funcd-deps/registry:ingest-v1   # full ref, tag at the end
      kv:
        - alias: counters
          store: counters-kv
          table: table-counters
    - name: stats
      image: oci-layout:///mnt/funcd-deps/registry:stats-v2
      dependsOn: [ingest]
      retry:
        maxAttempts: 3
        backoff: 10s
      timeout: 120s
    - name: publish
      image: oci-layout:///mnt/funcd-deps/registry:publish-v1
      dependsOn: [stats]
      when:
        condition: ${{ step.stats.output.rows > 0 }}
      params:
        format: parquet
    - name: notify-ops          # the onFailure handler — outside the DAG: no edges, no when
      image: oci-layout:///mnt/funcd-deps/registry:notify-v1
status:
  phase: Ready
  runs:                         # the run link (CronJob status.active pattern) — bounded
    active:
      - orders-report-01jb9
    succeeded: 412
    failed: 3
  contract:                     # effective I/O (declared, else derived) — F65 fills it
    input: {}
    output: {}
  steps:                        # resolved graph: digest + contract per step
    - name: ingest
      image: oci-layout:///mnt/funcd-deps/registry:ingest-v1@sha256:7be1...
      contract:
        input: {}
        output: {}
---
apiVersion: funcd.io/v1alpha1
kind: WorkflowRun
metadata:
  name: orders-report-01j9c
  namespace: default
  resourceGroup: rg1
spec:
  workflow: orders-report
  # paused: true                — set by `funcdctl workflow pause`, cleared by `resume`
  input:
    day: "2026-07-04"
status:
  phase: Running                # Pending → Running ⇄ Paused → Succeeded | Failed | Cancelled
  steps:
    - name: ingest
      phase: Succeeded          # Pending | Running | Succeeded | Failed | Skipped | Cancelled
      attempts: 1
      revision: "sha256:7be1..."
```

### Go API (api/types/v1alpha1, new files `workflow.go`, `workflowrun.go`)

```go
type Workflow struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       WorkflowSpec   `json:"spec"`
	Status     WorkflowStatus `json:"status,omitempty"`
}

type WorkflowSpec struct {
	Steps     []WorkflowStep      `json:"steps"`
	KV        []WorkflowKVStore   `json:"kv,omitempty"`
	Contract  *WorkflowContract   `json:"contract,omitempty"` // declared I/O; nil ⇒ derived (F65)
	Timeout   time.Duration       `json:"timeout,omitempty"`
	OnFailure ObjectName          `json:"onFailure,omitempty"`
}

// WorkflowStep: exactly one of Image/Function/Workflow set (Validate enforces).
type WorkflowStep struct {
	Name      ObjectName        `json:"name"`
	Image     string            `json:"image,omitempty"` // full artifact ref, TAG AT THE END; digest-resolved at Revision (ADR-0035)
	Function  ObjectName        `json:"function,omitempty"`
	Workflow  ObjectName        `json:"workflow,omitempty"` // reserved — rejected until F70
	DependsOn []ObjectName      `json:"dependsOn,omitempty"`
	Join      JoinMode          `json:"join,omitempty"` // "" ⇒ all
	When      *StepWhen         `json:"when,omitempty"`
	Params    json.RawMessage   `json:"params,omitempty"`
	Retry     *StepRetry        `json:"retry,omitempty"`
	Timeout   time.Duration     `json:"timeout,omitempty"`
	KV        []FunctionKV      `json:"kv,omitempty"`
	Blob      []FunctionBlob    `json:"blob,omitempty"`
	Secrets   []ObjectName      `json:"secrets,omitempty"`
	Config    []ObjectName      `json:"config,omitempty"`
	Catalogs  []FunctionCatalog `json:"catalogs,omitempty"`
}

type JoinMode string // "all" | "any"
type StepWhen struct {
	Condition string `json:"condition"` // ADR-0095 expression, Condition mode
}
type StepRetry struct {
	MaxAttempts int           `json:"maxAttempts,omitempty"`
	Backoff     time.Duration `json:"backoff,omitempty"`
}
type WorkflowKVStore struct {
	Name     ObjectName     `json:"name"`
	Deletion DeletionPolicy `json:"deletion,omitempty"` // "retain" (default) | "delete"
	Tables   []KVTable      `json:"tables"`             // Owner = a step name here
}
type DeletionPolicy string
// WorkflowContract: authored inline in the spec (ADR-0090 ContractBlob shape); admission
// enforces the TOTAL-DEFAULTS rule — every optional property in Input and Output must declare
// a `default` (boundary fields are always present).
type WorkflowContract struct {
	Dialect string          `json:"dialect,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}
type WorkflowStatus struct {
	Status   `json:",inline"`
	Contract *WorkflowContract    `json:"contract,omitempty"`
	Steps    []WorkflowStepStatus `json:"steps,omitempty"`
	Runs     *WorkflowRunLinks    `json:"runs,omitempty"` // the CronJob status.active pattern
}

// WorkflowRunLinks links a Workflow to its runs: active (in-flight) names + lifetime counts.
type WorkflowRunLinks struct {
	Active    []ObjectName `json:"active,omitempty"` // Pending|Running|Paused, newest first
	Succeeded int          `json:"succeeded,omitempty"`
	Failed    int          `json:"failed,omitempty"`
	Cancelled int          `json:"cancelled,omitempty"`
}
type WorkflowStepStatus struct {
	Name     ObjectName        `json:"name"`
	Image    string            `json:"image,omitempty"` // resolved, digest-pinned ref
	Contract *WorkflowContract `json:"contract,omitempty"`
}

type WorkflowRun struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       WorkflowRunSpec   `json:"spec"`
	Status     WorkflowRunStatus `json:"status,omitempty"`
}
type WorkflowRunSpec struct {
	Workflow ObjectName      `json:"workflow"`
	Input    json.RawMessage `json:"input,omitempty"`
	Paused   bool            `json:"paused,omitempty"` // declarative pause/resume (funcdctl sugar)
	Cancel   bool            `json:"cancel,omitempty"` // declarative one-way cancel intent (funcdctl sugar)
}
type WorkflowRunStatus struct {
	Status `json:",inline"`
	Steps  []RunStepStatus `json:"steps,omitempty"`
}
type RunStepStatus struct {
	Name     ObjectName `json:"name"`
	Phase    StepPhase  `json:"phase,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	Revision string     `json:"revision,omitempty"` // pinned digest it executed
}
type StepPhase string // Pending|Running|Succeeded|Failed|Skipped|Cancelled
```

### Engine (internal/workflow)

```go
// Deps wires the engine (internal component, ADR-0002 §1).
type Deps struct {
	Store     store.Store         // metastore: resources + status mirror
	Endpoints activator.Endpoints // resolve ready upstreams
	Waker     eventing.Waker      // wake scaled-to-zero targets (ADR-0033)
	DataDir   string              // <dataDir>/workflow (Badger)
	Config    Config              // retention, payload limit, defaults
	Clock     clock.Clock
	Logger    *slog.Logger
}

type Engine struct{ /* unexported */ }

func New(d Deps) (*Engine, error)
func (e *Engine) Run(ctx context.Context) error   // recovery scan + scheduler + run timers + GC
func (e *Engine) Close() error

// Reconcilers (registered on the ADR-0015 controller).
func (e *Engine) ReconcileWorkflow(ctx context.Context, req controller.Request) (controller.Result, error)
func (e *Engine) ReconcileRun(ctx context.Context, req controller.Request) (controller.Result, error)

// Engine actions invoked by the run reconciler (NOT API handlers). Cancel is triggered by the
// declarative spec.cancel marker the reconciler observes on the controller workqueue; Describe
// is served by a plain GET of WorkflowRun (its status mirrors engine state).
func (e *Engine) Cancel(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName) error
func (e *Engine) Describe(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName) (*RunRecord, error)
```

### FailureContext (the onFailure handler's input — engine-defined, stable)

```yaml
workflow: orders-report
run: orders-report-01j9c
failedStep: stats
reason: "attempts exhausted: 502 from upstream"
input: {}          # the run's original input, verbatim
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `store.Store` (resources, status writes) | `Workflow`/`WorkflowRun` kinds + admission rules |
| `activator.Endpoints` + `eventing.Waker` (dispatch) | `WorkflowRun` CRUD + `spec.cancel`/`spec.paused` markers (declarative verbs) |
| `internal/artifact.Inspect` (manifest contract/runtime — via the F65 gate) | `status.contract` + `status.steps[]` (the resolved cache F65 fills) |
| Badger at `<dataDir>/workflow` | `X-Funcd-Attempt` header to step functions |
| the ADR-0095 evaluator (`when.condition`, Condition mode) | audit log lines for denied dispatch |
| config `workflow.retention` · `workflow.payloadLimit` · `workflow.defaultStepTimeout` · `workflow.defaultRetry` | `dev.funcd.runtime.v1` manifest annotation (written by `funcdctl push`) |

## Implementation plan

Files: `api/types/v1alpha1/workflow.go`, `workflowrun.go` (+ validation, enums, OpenAPI regen);
`internal/workflow/{engine.go,reconcile_workflow.go,reconcile_run.go,dispatch.go,state.go,recovery.go}`;
`cmd/funcdctl` workflow verbs; push runtime-annotation in `internal/artifact` + `cmd/funcdctl`;
`pkg/funcd` wiring + config keys; no `go.mod` additions. **Sequencing**: [ADR-0095](0095-reference-engine-typed-paths-predicates.md)
is a build prerequisite — accepted alongside this ADR and implemented first (it is a
dependency-free leaf); `when.condition` scenarios therefore run against the real evaluator. The
F65 contract-gate checks are consumed behind an interface defined here; the three scenarios
marked *(lands with F65)* exercise that seam and are implemented with the F65 ADR — they are
listed here because the cache shape (`status.contract`/`status.steps[]`) is this ADR's contract.

Test plan: one named acceptance test per Scenario — 17 in this ADR's implement gate + the 3
marked *(lands with F65)* — in-process over `pkg/funcd` with the shim platform (the
ADR-0089/0093 e2e pattern); unit tests for the state machine (joins, skips,
fail-fast, timers) against a fake invoker; recovery test kills/restarts the engine mid-run.
Definition of done: the 17 in-gate scenario tests green; `go build ./... && go tool golangci-lint run ./... &&
go test ./... && go mod verify` green; OpenAPI regenerated; feat row `reviewing`; no identity leaks.

## Review checklist

- [ ] Every scenario **not marked *(lands with F65)*** has a named, passing, un-skipped test (the 3 marked ones are verified at the F65 ADR's gate).
- [ ] Step kind-union validated: exactly one of image/function/workflow; `workflow:` rejected; no gate kind exists (F66 deferred).
- [ ] Pause/resume: `spec.paused` honored (graceful, in-flight completes, timeout clock excludes Paused); declared-contract total-defaults rule enforced at admission.
- [ ] Materialized Functions: `<workflow>-<step>` naming, owned, cascade, per-workflow, bindings verbatim.
- [ ] ADR-0073 cycle is engine-internal (no user apply-order); `deletion: retain` default honored.
- [ ] Badger is the run-state truth; status mirror is coarse; recovery re-dispatches with fresh attempt IDs.
- [ ] `Workflow.status.runs` tracks active run names + terminal counts on every transition, active-only (bounded).
- [ ] Fail-fast, abandon-cancel, reject-duplicate-name, run timeout, onFailure semantics as decided.
- [ ] Dispatcher fail-closed (undeclared target Forbidden + audited); same-namespace enforced.
- [ ] Revision/digest pinning at run start; in-flight runs immune to re-push/spec edits.
- [ ] Payload cap enforced at admission and on step outputs; no `any` in exported APIs.
- [ ] Config keys + defaults as specified; `funcdctl workflow` verbs work against the control plane.
- [ ] No new dependencies; no bus import in `internal/workflow`.

## Consequences

- (+) FEAT-0005's keystone lands with zero new dependencies, entirely on existing substrate.
- (+) Runs cost nothing between steps; every declared shape fails at reconcile/admission, not runtime.
- (+) The F65/F73 companion seams are explicit interfaces — companions slot in without reshaping.
- (−) At-least-once means steps must be idempotent (attempt ID provided) — documented, not hidden.
- (−) Owned functions duplicate across workflows (accepted: scale-to-zero makes idle copies ~free).
- (−) The blueprint's "rely on NATS" workflow sentence is refined (sync core, bus at the Sensor) — blueprint synced at acceptance.
- Risk: state-machine semantics (skip cascade, join:any) are the hardest part to get right — mitigated by the dedicated unit-test matrix and the scenario set.

## Open questions

- F65 contract-gate rules (derivation, virtual edges, defaults rule) — the next ADR. It also owes
  two rules this ADR surfaces: (a) a `join: any` merge step's composite has **per-branch-optional**
  keys — its input schema must accept any single surviving branch; (b) a **skippable leaf** (one
  carrying `when`) whose key a declared `contract.output` requires is a reconcile-time rejection
  candidate (else the run output can violate the declared contract at runtime).
- Reference-engine grammar/evaluator — [ADR-0095](0095-reference-engine-typed-paths-predicates.md): accepted alongside, implemented first (see Implementation plan sequencing).
- F66 governance gates (`gate:` step kind, `Parked` phase, approve verb) — deferred post-core; a future ADR extends the step-kind union.
- F70 child-run semantics — its own ADR; the `workflow:` kind stays reserved.
- F74 OCI-packaged workflow + tag promotion — post-core ADR.
- Per-run parallelism cap — deferred to the implementation PR (default: dispatch all ready steps).
- `Function.spec` gains the `image` string form (tag-at-end), converging Function and step artifact idioms — a small follow-up ADR.

## References

- FEAT-0005 (the epoch, capability map, shapes) · blueprint *Services → Workflow engine*.
- Prior art verified 2026-07-04: Argo Events docs (architecture, sensors, HA, trigger conditions — argoproj.github.io/argo-events); Temporal docs (durable execution, activities, schedules); comparison analysis recorded in FEAT-0005.
- Kubernetes Deployment → ReplicaSet ownership model (the materialization analogy).
