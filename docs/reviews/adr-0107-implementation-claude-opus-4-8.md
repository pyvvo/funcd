# ADR-0107 implementation review — Run replay from a step (F71)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0107 implementation, model: claude-opus-4-8)

The implementation realizes ADR-0107 faithfully: a replay is a declarative new `WorkflowRun`
(`spec.replay = {run, from, allowDrift}`) that the engine seeds from the source run's checkpoint,
gates on digest drift, and drives as an ordinary run. Every run now records the resolved
digest-pinned image per step, and that digest survives persist/Resume and copied replay steps.
All verification is green and every one of the 11 Scenarios has a named, un-skipped, passing test.

### Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| tests | `go test ./internal/workflow/... ./api/types/... ./cmd/funcdctl/... ./internal/controlplane/...` | exit 0 |
| lint | `go tool golangci-lint run` (touched pkgs incl. `pkg/funcd`) | `0 issues`, exit 0 |
| mod | `go mod verify` | `all modules verified`, exit 0 |
| OpenAPI golden | `internal/controlplane` `TestSpecGeneratedFromGo` / `TestOpenAPIDocServed` / `TestSpecReflectsGoShape` | PASS (additive `spec.replay` regenerated) |
| in-process e2e | `go test ./pkg/funcd -run TestScenarioE2EWorkflowReplay` | `--- PASS (1.17s)` — replay mints its own trace, step reused not re-invoked |
| replay scenarios | `go test ./internal/workflow -run 'Replay\|Revision\|DigestPinned\|DigestDrift\|Subworkflow' -count=1` | all PASS |
| containerd Venom lane | (already-verified) workflow lane incl. new `run-replay…ADR-0107` case | 13/13, 0 failures, PASS |

### 🔴 Blockers
None.

### 🟡 Major
None.

### Minor / advisory
- **Uncommitted binary build artifacts in the working tree** · attribution: `env` · the working
  tree carries large modified `internal/runtime/embedimg/*.tar` blobs (from a local
  `build-runtime-images`), unrelated to ADR-0107. They are not part of this ADR's surface and must
  **not** be included in the replay commit (CLAUDE.md's guard against committing build artifacts).
  Advisory to the committer; not a code defect and not model-attributed.

### ✅ Verified correct (keep it)
- **Replay never mutates the source.** `Engine.Replay` `Get`s the source record read-only, builds a
  *new* `runstate.Record` (fresh `TraceID`/`RootSpanID`, `Depth=0`, `SourceRun`/`SourceFrom`
  provenance) and persists only that — the source's steps are read, never written.
- **Digest-pinning fidelity fix** lives where the ADR mandates: `stepNode.revision`, stamped at run
  start via `stampRevisions`/`revisionFor` from `StartOptions.StepImages` (fallback to the pinned
  spec ref for bare-engine tests / not-yet-materialized steps; builtin/`workflow:` steps stay empty),
  written by `persist` (`ss.Revision = n.revision`), and restored verbatim by `rebuildState` — with a
  pinned-spec back-fill for pre-ADR records. Copied replay steps keep the **source's** revision, so
  replay chains stay gateable. Confirmed by `TestDigestPinnedAtStart` + `TestRevisionSurvivesResume`.
- **Coverage classification** matches the Decision exactly: replay set = `{from} ∪ descendants(from)`
  (reverse-`dependsOn` closure in `runState.descendants`); outside the set
  `Succeeded`/`Skipped` copied, `Pending`/absent seeded-and-run, `Failed`/`Cancelled` ⇒ `SeedInvalid`;
  the `onFailure` handler excluded from both set and check. `TestReplayUncoveredFailureRejected` +
  `TestReplayCompletesPendingBranches`.
- **Drift gate** covers only `image:` function steps in the replay set, compares
  `source.Revision != current[step]`, names the step in a `DigestDrift` fault, and honors
  `allowDrift`. Steps outside the set are not gated. `TestDigestDriftRejected`.
- **Fresh trace + no foreign-parent edges**: copied steps clear their `spanID` so a re-run successor
  parents on the replay's run root; `TestReplayFreshTrace` verifies the new trace-id and parent edge.
- **Self-contained after creation**: `TestReplayIsSelfContained` deletes the source record then
  resumes the replay to completion (copy-forward, no lazy reference).
- **Widened `ChildResolver`** returns the child's cached step images (`map[v1.ObjectName]string`),
  threaded through `runChild` into the inline child's record — a concrete-typed signature, no `any`.
  Prod `childResolver.Child` projects `wf.Status.Steps[].Image`. `TestSubworkflowCopiedNotRerun`.
- **Admission gap fix present**: `workflowRunContract.Admit` returns early when `spec.replay != nil`
  (the replay's input comes from the already-validated source; `spec.input` is empty by design) —
  the exact gap the Venom lane's first run caught. Covered by the added admission test.
- **Validation at the edge**: `WorkflowRun.Validate` requires `replay.run`/`replay.from` DNS-1123 and
  rejects a non-empty `spec.input` when `spec.replay` is set; `Replay` additionally rejects a
  non-terminal source, a workflow mismatch, and a `from` that is not a DAG step.
- **Behavior-neutral `Execute → StartOptions` refactor**: the variadic contract param is consolidated
  into `StartOptions{Contract, StepImages}` on the private `execute` (public `Execute` wraps it);
  all pre-existing engine, reconcile, subworkflow, trace, and lineage tests pass unchanged.
- **CLI**: `funcdctl workflow replay <run> --from <step> [--name --allow-drift -n]` reads the source
  for its workflow, applies a validated `spec.replay` run, defaults the name to `<source>-r-<hex>`;
  `describe` prints the `replay of: <run> (from <step>)` provenance line.
- **Additive API**: `spec.replay` optional; OpenAPI regenerated and golden test green; no `go.mod`
  change (`go mod verify` clean).

### Definition of Done
9 / 9 ADR Review-checklist items hold (new-run/no-source-mutation; upstream not dispatched;
byte-identical re-run inputs; digest-pinned Revision incl. inline child + survives Resume + copied
keeps source; drift gate + allowDrift; SeedInvalid vs seeded-Pending vs onFailure-exempt;
survives source sweep; fresh trace + cleared span-ids + describe provenance; input-rejected +
workflow-match; additive API + OpenAPI regen + no new dep). Generic DoD also holds: full suite
green, every scenario a named passing test, no stubs, contracts matched exactly, tree matches the
ADR surface, ADR-0002 conventions honored (no `any` in port/exported sigs, `api/fault` errors),
feat row at `reviewing`, ADR at `Reviewing`. No misses.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0107 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
0 model-attributed, DoD 9/9. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. Stamp ADR-0107 `Reviewing → Implemented` and F71 → `implemented`; move the board card to
Done. One advisory to the committer: exclude the unrelated `embedimg/*.tar` build artifacts from the
replay commit.
