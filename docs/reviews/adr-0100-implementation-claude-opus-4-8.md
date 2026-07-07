# ADR-0100 implementation review — per-step troubleshooting lineage (F67)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0100 implementation, model: claude-opus-4-8)

The implementation records each step's `startedAt`/`endedAt`/`attempts` and the raw step-level
failure cause (capped), mirrors those small facts plus the run `trace_id` to `WorkflowRun.status`,
and turns `funcdctl workflow describe` into a rendered troubleshooting view with `-o json` for the
raw object. All four verification checks are green, all eight ADR Scenarios have named passing
tests, and the ADR's Review-checklist items all hold.

### Verification (captured exit codes)

- `nix develop -c go build ./...` → **exit 0**
- `nix develop -c go test ./internal/workflow/... ./cmd/funcdctl/... ./api/...` → **exit 0**
  (`internal/workflow`, `.../runstate/badger`, `cmd/funcdctl`, `api/openapi`, `api/types/v1alpha1`,
  `api/fault` all `ok`)
- `nix develop -c go tool golangci-lint run ./internal/workflow/... ./cmd/funcdctl/... ./api/types/...` → **exit 0**
- `nix develop -c go mod verify` → **exit 0** (all modules verified)

`TestPythonPoolSmoke` (internal/testkit/bench) is a known pre-existing environmental flake and is
out of this change's scope — not run, not attributed.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- `cmd/funcdctl/workflow.go:199` — the `-o/--output` flag help reads "rendered (default) or json",
  but only the literal `json` is special-cased; any other value (e.g. `-o yaml`) silently falls to
  the rendered view rather than erroring. Cosmetic; the ADR contract only specifies `-o json`.
  Attribution: `model`. Non-blocking.

### ✅ Verified correct (keep it)

- **Raw step-level cause, not the run-level wrap** (`internal/workflow/engine.go:577`). `dispatchStep`
  stamps `n.errMsg = capErr(lastErr.Error())` at the retry-loop break — *before* its own
  `step %q failed after retries` wrap and before `fail()`'s `run %q failed` wrap. `markFailed`
  (engine.go:94) only fills `errMsg` when unset, so a function step keeps the bare dispatch cause
  while builtins/sub-workflows pass their raw cause straight in. Exactly the judge-M1 contract.
  Proven by `TestFailedStepRecordsCause` (asserts `b.Error == "scorer returned 503"`).
- **Attempts populated (the latent bug fix)** — `n.attempts = attempt` is recorded on every dispatch
  iteration (engine.go:553), covering both the success return and the retry-exhausted break.
  `TestAttemptsRecorded` asserts a 3-retry step records `Attempts == 3` and a single-shot success
  records `1`. Builtins/sub-workflows leave it 0, and the render suppresses `attempts` when `≤ 0`,
  so no misleading `attempts: 0` line.
- **Timings** — `setRunning` stamps `startedAt` at the Running transition (engine.go:79);
  `markSucceeded`/`markFailed` stamp `endedAt`. All three step kinds route through these helpers in
  the drive loop (engine.go:419–452). `TestTimingsRecorded` asserts `endedAt ≥ startedAt`.
- **Error is capped and status stays bounded** — `capErr` (engine.go:68) keeps the first line and
  truncates to `maxStatusError = 512` with an ellipsis. `TestErrorIsCapped` verifies both the length
  cap (4000-char cause → ≤ cap) and first-line-preferred behavior. `mirror` (reconcile_run.go:167)
  copies only the small facts (name/phase/attempts/revision/startedAt/endedAt/error) — no payload
  output is mirrored to `status`, so the CRD cannot bloat.
- **Run trace-id mirrored** — `mirror` copies `rec.TraceID` into `status.traceId`
  (reconcile_run.go:172). `TestRunTraceIDInStatus` asserts the trace-id and the per-step facts land
  in `WorkflowRun.status`.
- **Resume keeps history** — `rebuildState` (engine.go:300–305) restores `attempts`/`startedAt`/
  `endedAt`/`errMsg` for already-terminal steps and leaves an in-flight (Running→Pending) step's
  lineage at zero so it re-runs fresh. `TestResumeKeepsHistory` asserts terminal step `a` keeps its
  recorded history and does not re-run, while in-flight `b` re-dispatches once.
- **Sub-workflow step recorded** — a `workflow:` step routes through `setRunning`/`markSucceeded`/
  `markFailed` (engine.go:431–440) like any step; on child failure the raw cause is recorded.
  `TestSubworkflowStepRecorded` covers both the success (timings) and failure (non-empty cause) paths.
- **`describe` render** — `renderRunDescribe` (workflow.go:205) prints `RUN <name> phase:`, a per-step
  line (`phase · attempts · duration · error`), the `trace:` line (omitted for a traceless/legacy
  run), and the static `full logs: funcdctl workflow logs <run>` pointer (plain informational text,
  invokes nothing — degrades gracefully if ADR-0106 slips). `-o json` preserves the prior raw object.
  `TestRenderRunDescribe` and `TestRenderRunDescribeNoTrace` cover both paths, including the
  `duration: 500ms`/`250ms` human formatting.
- **Additive, `omitempty` status schema** — `runstate.StepState`, `v1.RunStepStatus`, and
  `v1.WorkflowRunStatus` gain `omitempty` fields only; OpenAPI regenerated additively; `go mod verify`
  clean. No `any`/`interface{}` in the exported or port signatures (the new fields are `int64`/
  `string`); no `panic`, `log/slog` respected, ctx-first preserved.
- **Contracts honoured** — `maxStatusError`, `capErr`, `markSucceeded`, `markFailed(n, cause)` match
  the ADR Contracts signatures exactly; no `go.mod` change, as the ADR specified.
- **Tracking** — the ADR is at `Reviewing` with the accept-time acceptance note and no post-accept
  substance mutation; the F67 feat row is at `reviewing` for the ADR-0100 marker.

### Definition of Done

7 / 7 ADR Review-checklist items hold (raw cause + step-named; attempts populated; timings on every
step; Resume restores terminal lineage / in-flight re-runs fresh; describe render + trace + logs hint
+ `-o json`; error capped + only small facts + trace mirrored; additive `omitempty` schema + OpenAPI
regen). Generic DoD (build/lint/test/mod green; every Scenario a named passing test; real behaviour,
no stubs; conventions; no scope creep; tracking) also holds. Misses: none.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0100 (implementation) → pass, 0/0/1, 1 model-attributed,
DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. The single Minor (the `-o` flag accepting unrecognized values silently) is cosmetic and
does not block the gate; it can be tightened opportunistically or left as-is. No `model` findings
loop back to the builder; no `adr` findings trigger a superseding ADR. Advance ADR-0100
`Reviewing → Implemented`, the F67 row's ADR-0100 marker to `implemented`, and the board card to Done.
