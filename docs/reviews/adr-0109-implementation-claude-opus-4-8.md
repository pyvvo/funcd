# Review — ADR-0109 (Sensor — the event→action binder) implementation

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0109 implementation, model: claude-opus-4-8)

The Sensor (F69, delivering F68) is implemented faithfully to the ADR's Contracts, Scenarios,
Review checklist, and Definition of done. All verification was **run**, not eyeballed; exit codes
captured below.

### Verification (evidence)

| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | exit 0 |
| tests | `go test ./internal/sensor/ ./api/types/v1alpha1/ ./pkg/funcd/ -count=1` | exit 0 — `internal/sensor` ok 0.324s, `api/types/v1alpha1` ok 0.163s, `pkg/funcd` ok 57.6s |
| lints | `go tool golangci-lint run ./internal/sensor/ ./api/types/v1alpha1/ ./internal/controlplane/ ./pkg/funcd/` | `0 issues`, exit 0 |
| deps | `go mod verify` | `all modules verified`, exit 0 |
| containerd e2e | `just lima-example workflow` (scheduled F68/F69 case) | PASS — env-verified-by-builder (not re-run here) |

The `pkg/funcd` e2e `TestScenarioE2EScheduledWorkflow` ran the full timer→Fanout→Sensor→WorkflowRun
chain against the real Node shim (node present on PATH — the test did not skip) and reached
`Succeeded`.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

- **Minor 1 — the `DanglingDependency` NotReady reason in Decision §2 is unreachable.**
  attribution: **adr** (not scored).
  The ADR's Decision §2 says a dangling `do[].on` sets a `Ready=False` condition with reason
  `DanglingDependency` at reconcile. But §1 places the dangling-dep / exactly-one-kind checks in
  `Validate` (admission-time `fault.Invalid`), and the implementation follows §1 faithfully
  (`api/types/v1alpha1/sensor.go:99`, `:115`). A dangling dependency is therefore *rejected at
  admission* and never reaches reconcile, so `staticCheck` (which only emits `InvalidInput`) never
  emits `DanglingDependency`. This is a stronger form of "caught before any event fires", and the
  `dangling-dependency-not-ready` scenario is covered by `TestSensorValidateMatrix`
  (`validate_test.go:234-238`: dangling / both-kinds / neither-kind cases). The unreachable reason
  string is a harmless internal ambiguity in the ADR text, not a model defect — no fix required for
  sign-off; a future superseding ADR could drop the `DanglingDependency` mention from §2.

- **Minor 2 — the working tree bundles an unrelated dev-tooling change.**
  attribution: **model** (non-blocking; tree hygiene).
  `justfile` (new `embedimg-pin` / `embedimg-unpin` recipes; `build-runtime-images: embedimg-pin`)
  and `internal/runtime/embedimg/README.md` are modified in the same working tree but fall outside
  the ADR-0109 surface (they concern the embed-image `skip-worktree` guard, not the Sensor). It does
  not affect ADR-0109 correctness or any check above. Recommend committing it as a separate changeset
  so the ADR-0109 commit stays focused on the feature.

### ✅ Verified correct (keep it)

- **Contracts match exactly.** `Sensor`/`SensorSpec`/`Dependency`/`Action`/`SensorStatus`
  (`sensor.go:13-49`) mirror the ADR verbatim; `GroupVersionKind`/`GetStatus`/`Validate` present.
  `Deps{Store, Subscriber, Invoker, Logger}` with `Subscriber` and `Invoker` as single-method typed
  ports (no concrete lock-bearing struct, no `any`) — `internal/sensor/sensor.go:33-49`.
  `NewReconciler`/`Reconcile`/`buildInput` signatures as specified.
- **All 11 Scenarios → named, un-skipped, passing tests.** `TestSensorReconcilesReady`,
  `TestEventStartsWorkflow`, `TestEventInvokesFunction`, `TestInputProjection`,
  `TestInputAbsentPassesEventData`, `TestScheduledWorkflowStart`, `TestBadStaticInputNotReady`,
  `TestDeleteUnsubscribes`, `TestReconcileIsIdempotent`, `TestStatelessIndependentFirings`
  (`internal/sensor/sensor_test.go`); `dangling-dependency-not-ready` via `TestSensorValidateMatrix`.
  F68 has triple coverage: unit (`TestScheduledWorkflowStart`) + in-process e2e
  (`TestScenarioE2EScheduledWorkflow`) + the containerd Venom `scheduled workflow` case
  (`e2e/workflow.venom.yml:300`).
- **Idempotency (judge B1) implemented as accepted.** Per-Sensor registry keyed by `(ns,name)` with
  `ObservedGeneration`; a same-generation resync is a no-op, a spec change cancels-and-replaces, and
  delete/static-defect cancels all (`sensor.go:118-129`, `cancelAll`). `TestReconcileIsIdempotent`
  proves one firing ⇒ exactly one run after three resyncs.
- **`runName` is 63-char-bounded (M2).** `-<8 hex>` suffix, truncated `<sensor>-<action>` prefix,
  trailing-dash trim, `"run"` fallback (`sensor.go:328-339`).
- **`eventResolver` reports every field `Required:true` (M3);** `event.data` opaque object admits any
  deep path (`resolver.go`), so the F73 defaults rule never trips a projection.
- **Action side restored at the right layer.** `workflow:` creates a `WorkflowRun` on the internal
  `store.Store` in the Sensor's ns+ResourceGroup (admission-skipping, run-start-gate backstopped);
  `function:` invokes via `HTTPInvoker` which resolves the upstream and wakes a cold target
  (`invoker.go`); an `Invocation` is recorded per action (Ready/Failed) — the ADR-0023 guarantee.
- **Conventions (ADR-0002).** `api/fault` for all errors, `log/slog` only, ctx-first, ports not
  concretes, one-file drivers (`sensor.go`/`invoker.go`/`resolver.go`), no `panic`/`fmt.Print*`,
  no `any` in exported/port signatures.
- **Registration complete + additive.** `KindSensor` const + `Kind.Validate` + `NewObject` +
  `AllKinds` (`metadata.go`); control-plane `Handlers` interface + `storeHandlers` +
  `registerSensor` route + `StubHandlers` + `stampTypeMeta` case; `pkg/funcd` reconciler wiring
  (`Subscriber: eventFanout`, `Invoker: HTTPInvoker{Endpoints, Waker: activator, Client(30s)}`) +
  `ctrl.Register(KindSensor)`; SDK plural `sensors`; kind count bumped to 22; OpenAPI regenerated
  (23 sensor references); no `go.mod` change.
- **Tracking.** ADR at `Reviewing` with substance intact (new file, only the status header differs
  from a draft); F68 + F69 rows at `reviewing`. No dev-machine reference in any reviewed file.

### Definition of Done

7 / 7 ADR Review-checklist items hold (binds on→do + Validate; subscribe-on-Ready/cancel-on-delete
+ firing runs actions; workflow-run/function-invoke/Invocation; input projection + absent-verbatim;
bad-overlay/dangling ⇒ fail-before-firing; F68 scenario + e2e; stateless + additive + OpenAPI + no
new dep). Generic phase DoD also holds (full suite green, real behaviour no stubs, contracts honoured,
tree matches surface, conventions, deps tidy, tracking). No `model`-attributed misses.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0109 (implementation) → pass, 0/0/2, 1 model-attributed,
DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Stamp ADR-0109 `Reviewing → Implemented`, advance F68 + F69 to `implemented`, move the
board card to Done. The one model-attributed Minor (bundled embedimg tooling change) is a tree-hygiene
note — commit it separately; it does not block. The one `adr` Minor (unreachable `DanglingDependency`
reason) loops to an eventual superseding ADR if ever worth tidying, not to the builder.
