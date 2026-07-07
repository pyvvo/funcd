# ADR-0107: Run replay — re-run a finished run from a chosen step (F71)

- **Status**: Implemented
- **Date**: 2026-07-07
- **Implemented**: 2026-07-07
- **Deciders**: green-0-rabbit
- **Tags**: workflow, replay, lineage, checkpoint, troubleshooting, funcdctl
- **Acceptance note**: judge Blockers folded — B1 (Revision must live in `stepNode.revision`, restored by `rebuildState` and preserved for copied steps, so digests survive persist/Resume; new scenario `revision-survives-resume`) + B2 (coverage rule classifies DAG steps: Succeeded/Skipped copied, Pending seeded-and-run, only Failed/Cancelled rejected; `onFailure` handler exempt; new scenario `replay-completes-pending-branches`). Majors folded — M3 (`ChildResolver` widened to return the child's step-image cache so inline child runs are digest-pinned) + M4 (a replay-set `workflow:` subtree runs the live child ungated — named workaround with exit). Minors folded (descendants = reverse-`dependsOn` closure; pre-ADR records fail-closed; stamping domain = function steps; the private `execute` carries `StartOptions`, not the public `Execute`).
- **Realizes**: [FEAT-0005/F71](../feat/0005-feat-workflow-engine.md) (run replay / re-run from a step)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine, pinned spec, run record, Resume — the checkpoint machinery replay builds on), [ADR-0098](0098-typed-workflow-edges.md) (the cached contract graph + per-step **resolved digest-pinned image** the drift gate reads), [ADR-0099](0099-sub-workflows.md) (a `workflow:` step in the replay set re-runs its child fresh), [ADR-0100](0100-run-step-lineage.md) (the per-step record `describe` surfaces; replay provenance shows there), [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md)–[0105](0105-nested-dag-span-parenting.md) (a replay is a new run ⇒ a fresh trace).

## Context & Need

When a run fails at step N (or a finished run must be re-executed with the same data), the operator
today can only start a **brand-new run from the beginning** — re-executing every upstream step, paying
their cost and risking different upstream results. Temporal solves this with an append-only event
history because it must replay *code*; funcd's workflows are **declarative DAGs interpreted by the
engine**, so the whole orchestration state is already a persisted **checkpoint**: the run record pins
the spec, contract, input, and every step's phase + output (ADR-0094/0098/0100). Replay is therefore
*data replay over a checkpoint*: a **new run** seeded from a source run's record that re-runs only a
chosen step and its descendants, reusing recorded upstream outputs verbatim.

One latent gap blocks fidelity: `runstate.StepState.Revision` *claims* "pinned digest" but the engine
records the spec's image **ref** (`persist` writes `fn.Image`) — so today nothing proves a replay
executes the *same artifact*. This ADR fixes that (record the resolved digest-pinned image from the
ADR-0098 cache) and gates replay on it.

Callers: the operator/agent author troubleshooting a failed run (`funcdctl workflow replay`), or
re-running a succeeded pipeline stage over the same inputs.

## Scenarios

Each becomes a named acceptance test.

- `replay-from-failed-step` — Given a run failed at step `score` (upstream `ingest` Succeeded), When
  `funcdctl workflow replay <run> --from score` creates the replay, Then a **new** run executes `score`
  and its descendants to completion and **`ingest` is not re-dispatched** (0 dispatch calls).
- `replay-preserves-inputs` — Given a replay, Then each re-run step receives **byte-identical input**
  to what the source's recorded outputs derive (same `stepInput` over the copied outputs + pinned params).
- `replay-rerun-succeeded` — Given a **Succeeded** source run, When replayed `--from <mid-step>`, Then
  the step + descendants re-run and the run completes (re-run is not failure-only).
- `digest-pinned-at-start` — Given a normal run of a Ready workflow, Then each image step's
  `StepState.Revision` records the **resolved digest-pinned image** (the ADR-0098 cache value), not the
  bare spec ref — fixing the Revision comment/reality gap.
- `digest-drift-rejected` — Given the workflow re-materialized to a new digest for a step in the replay
  set, When a replay is created without `allowDrift`, Then it is **rejected** (run Failed, condition
  reason `DigestDrift` naming the step(s)); With `spec.replay.allowDrift: true` it runs.
- `replay-uncovered-failure-rejected` — Given the source has a Failed/Cancelled **DAG** step
  **outside** `{from} ∪ descendants(from)`, Then the replay is rejected (condition reason
  `SeedInvalid` naming the step) — a replay may not silently adopt a failure it does not re-run.
- `replay-completes-pending-branches` — Given a Failed source whose fail-fast left a parallel branch
  persisted **Pending**, When replayed `--from` the failed step, Then the Pending branch runs too (it
  never executed — there is no recorded output to adopt) and the run completes.
- `revision-survives-resume` — Given a resumed run (and a replay that persists after seeding), Then
  every step's recorded digest-pinned `Revision` is unchanged — copied steps keep the **source's**
  Revision, so replay chains stay gateable.
- `replay-is-self-contained` — Given a created replay, When the **source record is retention-swept**,
  Then the replay still resumes/completes (the seed was copied forward at creation).
- `replay-fresh-trace` — Given a replay, Then it mints its **own trace** (one run = one trace), its
  re-run root steps parent on the **replay's run root** (never a source-trace span), and
  `describe` shows the provenance (`replay of <run> from <step>`).
- `subworkflow-copied-not-rerun` — Given a `workflow:` step upstream of `from`, Then its recorded
  output is copied and the child is **not** re-run; a `workflow:` step **in** the replay set re-runs
  its child fresh (normal ADR-0099 path).

## Scope

**In:** the `spec.replay` seed on `WorkflowRun` (declarative, like `paused`/`cancel`); engine-side seed
validation + copy-forward + drive; recording the resolved digest-pinned image per step at every run
start; the digest-drift gate (image steps in the replay set) + `allowDrift`; `funcdctl workflow replay
<run> --from <step> [--name] [--allow-drift]`; provenance in the record (`SourceRun`/`SourceFrom`) and
in `describe`. **Out (named follow-ons):**
- **By-ref output GC alignment** — V1 step outputs are inline and size-bounded (ADR-0094); when the
  by-ref payload model lands (the data-lineage F67 increment), blob refs copied into a replay seed must
  outlive the source's sweep — decided there, not here.
- **Digest-gating `function.ref` steps** — a `ref:` step targets a *shared live* Function by design;
  its artifact was never run-pinned. The gate covers `image:` (owned) steps; ref steps replay against
  the live function, documented below.
- **Replaying a child run into its parent** — a sub-workflow child run is replayable *standalone* (it
  has its own record); the parent's recorded `sub` output is not retro-updated. V2 if ever needed.
- **Scheduled/automatic replay** — Sensor territory (F69).

## Constraints & Decision drivers

- **A replay is a NEW run.** Never mutate the finished source (status is forward-only; records of
  terminal runs are immutable troubleshooting evidence). Provenance links the two.
- **Same data, same code — by default, provably.** Inputs derive from copied outputs (pure
  `stepInput`); artifacts are digest-gated. Drift is an explicit opt-in, never silent.
- **Self-contained after creation.** The seed is copied forward once; the replay must survive the
  source being swept (retention window bounds *creation*, not the replay's lifetime).
- **Reuse the checkpoint machinery.** Seeding = the `rebuildState` restore pattern; driving, recovery,
  pause/cancel, traces, and `describe` work on a replay unchanged because it *is* an ordinary run.
- **No event sourcing.** The engine stays a state machine; no append-only history, no deterministic-code
  constraints, no new store (the rejected-alternatives record in ADR-0100 stands).

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Temporal-style append-only event history + code replay** | Solves a problem funcd doesn't have: there is no user orchestration code whose in-memory state needs reconstructing — the DAG is engine-interpreted and the checkpoint (pinned spec + input + per-step outputs) already exists in Badger. Event sourcing would import determinism/versioning constraints for zero added recovery power. |
| **Reset the source run in place** (walk its steps back to Pending) | Violates forward-only status + record immutability, destroys the failure evidence `describe`/logs point at, and conflates two executions in one trace/history. |
| **Client-side seeding** (funcdctl reads the record and crafts a pre-filled run) | Leaks engine record internals to the CLI, bypasses server-side validation/RBAC, and can't gate drift atomically. The declarative `spec.replay` keeps funcdctl thin (the established `paused`/`cancel` pattern). |
| **Reference-the-source seed** (resolve lazily per rebuild) | Cheaper storage but the replay breaks if the source is swept mid-flight and recovery gains a cross-record dependency. Copy-forward costs one duplication of bounded outputs. |
| **Per-run pinned materialization** (each run gets its own function revisions) | The absolute-fidelity endgame, but heavy (per-run Functions/cleanup). The strict digest **gate** buys the same guarantee ("refuse to lie") at ~zero cost; revisit only if drift-rejection proves operationally painful. |

## Decision

A replay is a **declarative new WorkflowRun** carrying `spec.replay = {run, from, allowDrift}`. On its
first reconcile the engine seeds it from the source's record (copy-forward), gates on digest drift,
and then drives it as an ordinary run.

1. **Digest pinning at every run start (the fidelity fix).** The engine's private `execute` gains
   `StartOptions{Contract, StepImages}` (the public `Execute` wraps it; the variadic contract param is
   consolidated away). The run reconciler passes `StepImages` = the ADR-0098 cache's per-step
   **resolved digest-pinned image** (`wf.Status.Steps[].Image`). **Revision lives in the scheduling
   state**: `stepNode` gains `revision string` — stamped at run start for **function steps** (from
   `StepImages`; fallback `fn.Image` when absent — bare-engine tests; builtin/`workflow:` steps stay
   empty), **restored verbatim by `rebuildState`** on Resume, and `persist` writes `ss.Revision =
   n.revision` — so the digest survives every persist/resume cycle, and a replay's **copied** steps
   keep the **source's** Revision forever (replay chains stay gateable). ADR-0098 recorded this cache
   "for observability"; this ADR promotes it to the gate's *comparison key* — an honesty check;
   ADR-0035's pinned Function Revision remains the execution byte-identity authority.
2. **Seed (engine `Replay`).** `Engine.Replay(ctx, ns, runName, seed, current)` — `current` is the
   workflow's current resolved step-image map for the gate. It: (a) `Get`s the **source record** (same
   namespace; must be `Terminal()`); (b) computes the **replay set** = `{from} ∪ descendants(from)`
   over the source's pinned spec (`from` must be a DAG step); (c) **classifies every DAG step outside
   the replay set** (the `onFailure` handler is excluded from both the set and the check — it is
   seeded `Pending` and fires normally iff the *replay* fails): `Succeeded`/`Skipped` ⇒ **copied**;
   `Pending` (fail-fast left it unrun) ⇒ seeded `Pending` and **runs normally** — it never executed,
   so there is no recorded output to adopt; this is what makes a parallel-branch failure replayable;
   `Failed`/`Cancelled` ⇒ reject `SeedInvalid` naming the step (a replay may not silently adopt a
   failure it does not re-run); (d) **drift gate**: for each `image:` function step
   in the replay set, `source.Revision != current[step]` ⇒ reject `DigestDrift` naming the steps,
   unless `seed.AllowDrift`; (e) builds the new record: `Workflow/Spec/Contract/Input` copied from the
   source, `SourceRun`/`SourceFrom` set, **fresh** `TraceID`/`RootSpanID` minted, `Depth=0`; **copied**
   steps keep `Phase/Output/Revision` with **cleared `SpanID` and zeroed execution facts**
   (timings/attempts/error — they did not run *here*; the cleared span-id makes a re-run step whose
   predecessor was copied parent on the **replay's run root**, never a foreign-trace span — the
   existing `stepSpanID`-empty fallback); replay-set and Pending-seeded steps start `Pending` with
   fresh pre-minted span-ids and stamp `revision` from `StepImages` when they run; (f) persists and
   hands off to the normal `drive`.
3. **Reconciler routing.** In `RunReconciler.drive`: existing record ⇒ `Resume` (unchanged); no record
   and `run.Spec.Replay != nil` ⇒ `engine.Replay` (with `current` from `wf.Status.Steps`); else
   `Execute`. The new run's `spec.workflow` must equal the source record's workflow (so re-run steps
   dispatch to the same materialized `<workflow>-<step>` functions) — mismatch ⇒ `SeedInvalid`. On any
   seed rejection the reconciler sets the run `Failed` with status condition
   `ReplaySeeded=False, reason=DigestDrift|SeedInvalid` and a capped message (ADR-0100 cap).
4. **CLI.** `funcdctl workflow replay <source-run> --from <step> [--name <new>] [--allow-drift]
   [-n ns]`: reads the source WorkflowRun (for `spec.workflow`), creates the new WorkflowRun with
   `spec.replay`; `--name` defaults to `<source>-r-<4 random hex>`. `describe` on a replay prints a
   `replay of: <run> (from <step>)` line (read from `spec.replay`; no new status field).
5. **Sub-workflows.** A copied `workflow:` step contributes its recorded output; its child run is not
   touched. A replay-set `workflow:` step runs a **fresh child** (normal `runChild` — new child record
   `<replay>-<step>`, shared trace per ADR-0104) against the **live** child workflow (see the
   workaround below). **Child runs are digest-pinned too**: `ChildResolver.Child` is widened to also
   return the child workflow's cached step images, threaded into the child's record — without this,
   child records would keep bare-ref Revisions and a *standalone* child replay would false-positive
   `DigestDrift` every time. Replaying *from inside* a child = replay the child run itself
   (standalone; out-of-scope note above).
6. **Validation (`WorkflowRun.Validate`).** When `spec.replay` is set: `replay.run`/`replay.from` are
   DNS-1123 labels and `spec.input` must be empty (the input comes from the source — two sources of
   truth rejected at the edge).

## Temporary workarounds

- **`function.ref` steps are not digest-gated.** They target a shared live Function (never run-pinned);
  a replay re-invokes the current one. *Exit:* gate them iff ref-pinning ever lands (named out-of-scope).
- **A replay-set `workflow:` subtree is not digest-gated.** `runChild` resolves the child at execution
  time, so a re-run sub-workflow executes the child's *current* spec/artifacts — the same honesty limit
  as `ref:` steps, one level up. *Exit:* recursive gating over the child's `status.steps` cache (or
  per-run materialization) in a follow-up ADR.
- **Records predating this ADR carry bare-ref Revisions.** Replaying one trips `DigestDrift` by
  construction (`ref` ≠ `ref@digest`) — fail-closed, not a bug; `--allow-drift` is the escape. *Exit:*
  pre-ADR records age out via retention.
- **Replay window = retention window (creation-time only).** The source must exist when the replay is
  created; after copy-forward the replay is independent. *Exit:* durable audit lineage (the named F67
  follow-on) extends the window; nothing changes here.
- **`current` map staleness.** The drift gate reads `wf.Status.Steps` as of the seeding reconcile; a
  concurrent re-materialization between gate and first dispatch is not re-checked (same TOCTOU class as
  every reconcile decision; the gate is an honesty check, not a lock). *Exit:* per-run pinned
  materialization (rejected alternative) if fidelity must be transactional.

## Contracts

### Resource (api/types/v1alpha1)

```go
// WorkflowRunSpec gains:
Replay *ReplaySeed `json:"replay,omitempty"`

// ReplaySeed seeds this run from a finished source run's checkpoint (ADR-0107): the engine copies the
// source's pinned spec/contract/input and the terminal steps outside the replay set, then re-runs
// `from` + its descendants. Input must be empty when set (it comes from the source).
type ReplaySeed struct {
	Run        ObjectName `json:"run"`                  // the source run (same namespace)
	From       ObjectName `json:"from"`                 // the step to re-run from
	AllowDrift bool       `json:"allowDrift,omitempty"` // opt into re-running steps whose artifact digest moved
}
```

### Run record (internal/workflow/runstate)

```go
// Record gains (replay provenance):
SourceRun  v1.ObjectName `json:"sourceRun,omitempty"`  // the run this record was seeded from
SourceFrom v1.ObjectName `json:"sourceFrom,omitempty"` // the step the replay re-ran from
// StepState.Revision now truly holds the resolved digest-pinned image — via stepNode.revision
// (stamped from StartOptions.StepImages, restored on Resume, preserved for copied replay steps).
```

### Engine (internal/workflow)

```go
// StartOptions consolidates run-start inputs, threaded through the private execute (the public
// Execute wraps it; replaces the variadic contract parameter).
type StartOptions struct {
	Contract   *v1.WorkflowContract      // pinned contract (ADR-0098); nil ⇒ no run-start input check
	StepImages map[v1.ObjectName]string  // per-step resolved digest-pinned image (the ADR-0098 cache); stamps stepNode.revision
}
func (e *Engine) Execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, opts StartOptions) (*runstate.Record, error)

// Replay seeds runName from seed.Run's terminal record (copy-forward), gates digest drift against
// current (the workflow's live resolved step images), and drives. Faults: NotFound (source absent),
// Invalid with reason token SeedInvalid or DigestDrift (message names the offending steps).
func (e *Engine) Replay(ctx context.Context, ns v1.NamespaceName, runName v1.ObjectName, seed v1.ReplaySeed, current map[v1.ObjectName]string) (*runstate.Record, error)

// stepNode gains (state.go): revision string — the resolved digest-pinned image this step executes.
// Stamped at start (StepImages), restored verbatim by rebuildState, preserved for copied replay
// steps; persist writes it to StepState.Revision (the lifecycle that survives persist/Resume).

// runState gains (state.go, pure):
// descendants returns the steps that transitively DEPEND ON name (the REVERSE dependsOn closure —
// name's downstream subtree, never its ancestors), computed over the post-implicit-chaining
// runState graph, so linear list-order chains are already edges.
func (rs *runState) descendants(name v1.ObjectName) []v1.ObjectName

// ChildResolver (the ADR-0099 seam) is WIDENED: Child also returns the child workflow's cached
// per-step resolved images (its ADR-0098 status cache), so an inline child run's record is
// digest-pinned like a top-level run's.
Child(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error)
```

### CLI

`funcdctl workflow replay <source-run> --from <step> [--name <new-run>] [--allow-drift] [-n <ns>]` —
creates the `spec.replay` WorkflowRun. `describe` prints `replay of: <run> (from <step>)` when set.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0094 run record (pinned spec/contract/input, per-step outputs) + `Resume` restore pattern | `Engine.Replay` + the `spec.replay` declarative seed |
| the ADR-0098 cache `wf.Status.Steps[].Image` (resolved digests) | truly digest-pinned `StepState.Revision` on **every** run |
| the ADR-0100 status mirror + `describe` render | replay provenance in `describe`; `ReplaySeeded` condition on rejection |
| no new store, no new dependency, no `go.mod` change | a fresh trace per replay (ADR-0102–0105 unchanged) |

## Implementation plan

- **Files**: `api/types/v1alpha1/workflowrun.go` (`ReplaySeed`, `Validate` rules);
  `internal/workflow/runstate/runstate.go` (`SourceRun`/`SourceFrom`; fix the `Revision` comment);
  `internal/workflow/state.go` (`descendants`, `stepNode.revision`); `internal/workflow/engine.go`
  (`StartOptions` threaded through the private `execute`, `Replay`, `persist` writes `n.revision`,
  `rebuildState` restores it); `internal/workflow/subworkflow.go` (widened `ChildResolver`; `runChild`
  passes the child's step images); `internal/workflow/reconcile_run.go` (routing, `current` map,
  `ReplaySeeded` condition); `cmd/funcdctl/workflow.go` (`replay` verb + the `describe` provenance
  line). OpenAPI regen (additive spec field). No `go.mod` change.
- **Call-site refactor**: the **private `execute`** threads `StartOptions` (the public `Execute`
  wraps; `runChild` calls `execute`, not `Execute`); reconciler, `ChildResolver` implementers, and
  tests updated — behavior-neutral for existing runs.
- **Test plan** — one named test per Scenario (engine tests over the fake dispatcher:
  `replay-from-failed-step`, `replay-preserves-inputs`, `replay-rerun-succeeded`,
  `digest-pinned-at-start`, `digest-drift-rejected`, `replay-uncovered-failure-rejected`,
  `replay-is-self-contained` (delete the source record, then Resume the replay),
  `replay-completes-pending-branches`, `revision-survives-resume`,
  `subworkflow-copied-not-rerun`; a trace test for `replay-fresh-trace` (new trace-id; re-run root
  parents on the replay root); a CLI unit for the verb + describe line). **In-process e2e**
  (`pkg/funcd`): a real 2-step run fails at step 2 → `funcdctl workflow replay` (SDK) → the replay
  succeeds re-running only step 2. **Venom** (containerd workflow lane): replay `run-hi` from a
  mid-step in-VM → new run Succeeds and the upstream step's function is not re-invoked (assert
  attempts via `describe -o json`).
- **Definition of done**: all scenario tests green; `go build/test/lint/mod` green; existing engine +
  workflow suites pass (the `Execute` refactor is behavior-neutral); the in-process e2e + workflow
  Venom lane green; F71 row advanced; OpenAPI regenerated; no identity/path leak.

## Review checklist

- [ ] A replay is a **new** WorkflowRun; the source record/status is never mutated.
- [ ] Steps outside the replay set are **not dispatched** (verified by dispatcher call counts).
- [ ] Re-run steps receive inputs derived **byte-identically** from the source's recorded outputs.
- [ ] `StepState.Revision` records the resolved **digest-pinned** image on every run — **including
      inline child runs** (widened `ChildResolver`) — and **survives Resume**; a replay's copied steps
      keep the **source's** Revision across the replay's own persists.
- [ ] Drift on a replay-set `image:` step ⇒ rejected with `DigestDrift` naming the step; `allowDrift`
      overrides; steps outside the set are not gated.
- [ ] A Failed/Cancelled source **DAG** step outside the replay set ⇒ rejected with `SeedInvalid`; a
      **Pending** one is seeded Pending and runs; the `onFailure` handler is exempt (seeded Pending,
      fires iff the *replay* fails).
- [ ] The replay survives source sweep after creation (copy-forward, no lazy reference).
- [ ] Fresh trace; copied steps' span-ids cleared (no foreign-trace parent edges); provenance visible
      in `describe`.
- [ ] `spec.input` rejected when `spec.replay` is set; `spec.workflow` must match the source's.
- [ ] Additive API (`spec.replay` optional); OpenAPI regenerated; no new dependency.

## Consequences

- **(+) Failure recovery without re-paying the pipeline** — re-run exactly the failed subtree over the
  same data; upstream results are reused verbatim.
- **(+) Reproducibility becomes provable** — every run now records true digests, and a replay refuses
  to silently run different code (the DVC-replacement promise F49 wanted).
- **(+) Zero new machinery** — no event log, no new store; the replay is an ordinary run to every
  other subsystem (recovery, traces, describe, retention).
- **(−) Bounded fidelity** — `ref:` steps and the gate's TOCTOU window are documented honesty limits,
  not locks; absolute fidelity remains the per-run-materialization follow-on.
- **(−) One-time output duplication** per replay (bounded by the ADR-0094 payload cap).

## Open questions

- **By-ref outputs in seeds** — answered by the data-lineage F67 increment (blob-ref lifetime rules).
- **Replay chains** (`replay of a replay`) — works by construction: the replay's record is a normal
  checkpoint **and copied steps keep source Revisions across persists**, so the second-generation gate
  compares real digests. Whether `describe` renders the full ancestry chain is a display choice at impl.
- **Auto-replay on failure policy** — Sensor/F69 territory, not this ADR.

## References

- [ADR-0094](0094-workflow-engine-core.md) — run record, pinned spec, Resume, retention, payload cap.
- [ADR-0098](0098-typed-workflow-edges.md) — the resolved digest cache the gate + pinning read.
- [ADR-0100](0100-run-step-lineage.md) — per-step lineage; the rejected event-sourcing alternative.
- [ADR-0099](0099-sub-workflows.md) / [ADR-0104](0104-cross-subworkflow-trace-linking.md) — child-run
  and trace semantics a replay inherits.
- Temporal docs (event-history replay model) — the contrast motivating the checkpoint design.
