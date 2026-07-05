# ADR-0094 implementation review — workflow engine core (model: claude-opus-4-8)

## Verdict: changes requested — 2 blockers, 3 majors (ADR-0094 implementation, model: claude-opus-4-8)

Verification executed (Nix dev shell): `go build ./...` exit 0 · `go vet ./...` exit 0 ·
`go tool golangci-lint run ./...` exit 0 (0 issues) · `go mod verify` exit 0 ("all modules
verified") · `go test ./...` exit 1 — the sole failure is `TestPythonPoolSmoke`
(`internal/testkit/bench`, Python-shim readiness), a pre-existing environmental failure untouched
by this work (**env**-attributed, not scored). Every workflow/artifact/controlplane/api package is
`ok`. `just ci`'s git-diff gate is satisfied per-commit (six green checkpoints,
`10dc17b…7776b06`).

The core is real and strong — the state machine, recovery, materialization, and the
decider-directed declarative-cancel restructure all verify. The gaps are at the edges the ADR's
own checklist names: four in-gate scenarios shipped without tests, and spec/revision pinning is
not actually held across pause/crash boundaries.

### 🔴 Blocker 1 — four in-gate scenarios have no named test · attribution: model

Repo-wide grep for the scenario names finds no test for:

- `run-timeout-fails` — the behavior exists (`engine.go:182` `timeoutOf` → ctx deadline →
  `fail`) but is never exercised.
- `onfailure-handler-runs` — `fail()` dispatches the handler once with the FailureContext
  (`engine.go:341-357`); no test asserts the dispatch, the once-ness, or that the phase stays
  `Failed`.
- `duplicate-run-name-rejected` — relies on the generic `store.Create` AlreadyExists conflict;
  no named test binds the scenario.
- `revision-pinned-mid-run-repush` — untested *and* unimplemented (see Blocker 2).

The ADR's checklist item 1 requires every scenario **not marked (lands with F65)** to carry a
named, passing, un-skipped test: 13 of 17 do. Fix: the builder adds the four tests (the first
three are small — the behavior already exists).

### 🔴 Blocker 2 — spec/revision pinning at run start is not implemented · attribution: model

Contract: *"in-flight runs are unaffected (pinned at start)"* (Mutability, ADR §Decision);
checklist item 10: *"Revision/digest pinning at run start; in-flight runs immune to
re-push/spec edits."*

Evidence: `runstate.Record` pins only `Input` — not the step graph and no digests
(`internal/workflow/runstate/runstate.go:23`). `RunReconciler.drive`
(`internal/workflow/reconcile_run.go:113-118`) passes the **freshly fetched** `wf.Spec` to
`engine.Resume`, and `rebuildState` rebuilds the DAG from it — so a run paused (or crashed)
before a spec edit resumes on the **new** graph. Dispatch resolves the target function's
current upstream at dispatch time; nothing pins a digest per run. Within one synchronous
`drive` the snapshot holds, but the pause/resume and crash/recovery paths — both first-class in
this ADR — are exactly where the contract is violated. Fix: persist the pinned spec (or its
step graph + digests) in the `Record` at `Execute` and resume from the record, not the live
Workflow.

### 🟡 Major 1 — `workflow.payloadLimit` and `workflow.retention` declared but unenforced · attribution: model

Checklist item 11: *"Payload cap enforced at admission and on step outputs"* — no enforcement
exists anywhere (repo-wide grep: the key appears only in `internal/platform/config/config.go`).
The Decision also specifies terminal runs *"swept after `workflow.retention` (default 720h)"* —
no sweep exists. The builder deferred both unilaterally (noted in a config comment), but the
ADR sanctions no such deferral. Fix: enforce the input cap at admission + output cap in the
engine; implement (or get decider sign-off to defer via a documented follow-up) the retention
sweep.

### 🟡 Major 2 — `Config.DefaultStepTimeout` is dead code; default deviates · attribution: model

`internal/workflow/engine.go:53` declares it; no code applies it (`dispatchStep` runs each
attempt under the run-level ctx only). The ADR also specifies the default as 300s
(§Implementation notes: *"workflow.defaultStepTimeout (300s)"*); the shipped default is 0
(none). Fix: wrap each dispatch attempt in a per-step deadline and set the 300s default.

### 🟡 Major 3 — `deletion: retain|delete` not differentiated by the materializer · attribution: model

`WorkflowKVStore.Deletion` exists (`api/types/v1alpha1/workflow.go:146-157`, retain default)
but `buildKVStore` (`internal/workflow/reconcile_workflow.go`) attaches identical
owner-references (with `BlockOwnerDeletion: true`) and the workflow's resourceGroup regardless
of policy — the field is read nowhere. Today this is latent (no owner-based GC cascades yet, so
everything behaves as retain), but the moment cascade lands, `retain` stores will be deleted.
Fix: skip the owner reference (or mark it non-cascading) when `Deletion == retain`.

### Minor

- **Stale `Engine.Describe` contract signature · attribution: adr** — the decider-authorized
  in-place amendment states describe is *"served by a plain GET of WorkflowRun"* (and funcdctl
  does exactly that), yet the Contracts block still lists
  `func (e *Engine) Describe(…) (*RunRecord, error)`, which is unimplemented. The contract
  contradicts its own amendment note; a follow-up in-place tidy (decider-authorized, same
  session lineage) or the F65 ADR should drop the signature.
- **Run-timeout reason string · attribution: model** — the ADR names the failure
  `RunTimedOut`; the engine reports `"run deadline"` wrapped `Unavailable`
  (`engine.go:210`). Cosmetic until the reason surfaces in status; align when adding the
  Blocker-1 test.
- **`join-any-exclusive-branch` verified at state-machine level only · attribution: model** —
  `TestJoinAny` (`state_test.go:76`) covers the join semantics, but the scenario's engine-level
  assertion (the merge step's composite carries the *surviving* branch's output) has no test.

### ✅ Verified correct (keep it)

- **State machine**: implicit chaining, fan-out/join-all, join-any, skip cascade, run-phase
  derivation — 8 focused unit tests green (`state_test.go`), exactly the fake-invoker level the
  ADR's test plan prescribes.
- **Crash recovery**: `TestCrashRecoveryResumesRun` proves a Running step is reset to Pending
  and re-dispatched with a fresh attempt ID against a *new* engine over the same Badger store —
  the at-least-once write-ahead contract, genuinely exercised.
- **Fail-closed dispatch**: `TestDispatchFailClosed` — undeclared target ⇒ `Forbidden`, zero
  wake, audit-logged (`dispatch.go:79-83`); 4xx⇒permanent / 5xx⇒retryable classification tested
  both ways; `X-Funcd-Attempt` asserted by the echo server.
- **Materialization**: `TestMaterializeOwnedFunctionsAndKV` verifies `<workflow>-<step>`
  naming, runtime resolution via the manifest seam, the ADR-0073 cycle engine-internal
  (fn-without-kv → store → patch kv), owner refs, shared-pool vs isolated pooling, and
  `MinReplicas` warmth; idempotency tested separately.
- **Declarative verbs done right**: cancel/pause/resume are all `spec` markers observed on the
  ADR-0015 controller workqueue (decider-directed restructure, ADR amended in place with
  authorization) — no imperative endpoint, the control-plane server stays store-CRUD-only, and
  `Engine.Resume` short-circuits terminal records so a cancelled run is never re-driven.
  `TestRunReconcilerCancel`/`Pause`/`DrivesAndLinks` cover the reconciler paths including
  `status.runs` (active + terminal counts, active-only enumeration).
- **Validation layer**: kind-union (exactly one of image/function, `workflow:` rejected), DFS
  acyclicity, onFailure-outside-the-DAG, owner-is-a-step, total-defaults, pooling mode — all
  tested in `api/types/v1alpha1/workflow_test.go`; two-tier split (huma tags at the edge,
  `Validate()` at admission + store) honored.
- **Self-describing artifacts**: `dev.funcd.runtime.v1` written by both push paths, read by
  `InspectRuntime` from the manifest alone; `TestScenarioRuntimeAnnotationRoundtrips` covers
  set/unset. All pre-existing artifact tests still green (no weakened assertions — the added
  `runtime` parameter is `""` at every legacy call site).
- **Conventions**: no `any` in exported/port surfaces (lint 0 incl. forbidigo), no bus import
  in `internal/workflow` (imports: activator/controller/store/expr/runstate only), `api/fault`
  throughout, ctx-first, slog-only, one-file Badger driver behind the `runstate.Store` port
  with a shared contract suite run by both backends, zero new `go.mod` dependencies for this
  ADR.
- **Tracking**: ADR at `Reviewing`, feat F64 row at `reviewing`; every ADR substance edit is
  decider-authorized and documented in the header amendment notes (native-JS `when`, pooling
  addition, declarative cancel) — nothing silently mutated.

### Recommendation

**changes-requested** — loop the five model-attributed Blocker/Major findings back to the
`adr-impl` gate. The fixes are well-scoped: four tests (three over existing behavior), spec
pinning in the run record, per-step timeout wiring, payload/retention enforcement (or a
decider-sanctioned deferral recorded properly), and a one-line retain guard in the
materializer. The architecture needs no rework.
