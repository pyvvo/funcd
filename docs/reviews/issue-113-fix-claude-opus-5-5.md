## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #113 fix, model: claude-opus-5-5)

The fix is commit `9ed79a0` (`fix(sensor): deliver a function action's projected input to the function`) on the
eventing group branch. It touches `internal/sensor/sensor.go` and `internal/sensor/sensor_test.go`. This review
covers only that commit. The review worktree was detached at the group head `39e677e`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 9ed79a0` on
  `39e677e` merged `internal/sensor/sensor.go` cleanly and conflicted only in `internal/sensor/sensor_test.go`
  (later group commits append tests there). The test file was restored from `39e677e`, so only the fix was
  reverted. `TestIssue113_FunctionActionReceivesProjectedInput` then failed under `-race`:
  `expected: {"file":"drop/a.parquet","lit":"x"}`, `actual: {"key":"drop/a.parquet","size":3}`, message
  "the function receives the projected input as the event data". This is the issue's case 1 exactly: the
  function receives the unchanged event data.
- **It passes with the fix.** After `git reset --hard 39e677e`: `--- PASS: TestIssue113_FunctionActionReceivesProjectedInput`
  under `-race`, not skipped. The test also asserts that the envelope (`source`, `type`) is unchanged.
- **The user-visible behavior is fixed through the real HTTP path.** A scratch probe (overlay-added, not
  committed) built the reconciler with the real `sensor.HTTPInvoker` posting to an `httptest` server and
  decoded the posted body. Three cases passed under `-race`:
  - input `{file: ${{ event.data.key }}, lit: x}`, data `{"key":"drop/a.parquet","size":3}` → posted CloudEvent
    `data` is `{"file":"drop/a.parquet","lit":"x"}`, `datacontenttype` `application/json`, same `source`/`type`;
  - no input, data `{"key":"k"}` → `data` is `{"key":"k"}` (ADR-0109 scenario `input-absent-passes-event-data`);
  - no input, timer event (`{}`) → `data` is `{}`.
  The issue's case 2 (a failing projection dead-letters a delivery that never used it) is resolved by the same
  change: the input is now used, so a projection failure is a real delivery failure recorded on the Invocation,
  as ADR-0109 Decision 4 states ("A per-firing `Eval` error is recorded on the Invocation (Failed)").
- **Cause, not symptom.** The issue names `deliver` discarding `buildInput`'s result on the `function:` branch.
  The fix uses that result: it copies the CloudEvent value and sets its `Data` to the projected input. `d.ce` is
  not mutated (a value copy; `Data` is reassigned, not written through), so retries and the dead-letter payload
  (`json.Marshal(d.ce)` in `attemptDelivery`'s DLQ path) keep the original event, and a replay re-projects it.
  `deliver` is the only caller of `Invoker.Invoke`.
- **Mutants are killed.** Two overlay mutants on the fix lines, each run against the whole `internal/sensor` suite:
  - `ce := eventing.CloudEvent{}` (drop the envelope) → `TestIssue113_…` fails;
  - `ce.Data = d.action.Input` (send the unprojected template) → `TestIssue113_…` and `TestIssue173_…` fail.
  The revert itself is the third mutant (no assignment) and fails `TestIssue113_…`.
- **Scope.** Every hunk serves the issue: the two-line fix, the `deliver` doc comment updated to match, and the
  existing `fakeInvoker` extended to record the delivered events plus one regression test. No test was weakened
  or deleted.
- **Reuse.** The fix reuses `buildInput` and the existing `CloudEvent` envelope; the test reuses the package
  harness (`harness`, `createSensor`, `dep`, `fire`, `reqOf`) and extends the existing `fakeInvoker` stub instead
  of adding a new one.
- **ADRs and living docs.** No ADR file was edited. The fix follows ADR-0109 Decision 3 ("invoke the function with
  the CloudEvent") and Decision 4 ("The result is the JSON object handed to the target"): the target receives a
  CloudEvent whose `data` is the projected object. The `api/types/v1alpha1/sensor.go` `Action` doc and the
  FEAT-0005/F69 row ("`input` absent ⇒ event data verbatim") stay true.
- **Conventions.** ctx-first, no new exported surface, no `any`, no comment bloat; the test name follows
  `TestIssue<N>_…`.
- **Checks.** `gofmt -l internal/sensor` → clean; `go build ./...` → ok; `go vet ./internal/sensor/... ./pkg/funcd/...`
  and `GOOS=linux go vet ./internal/sensor/...` → ok; `golangci-lint run ./internal/sensor/...` → `0 issues.` on
  host and with `GOOS=linux`; `go test -race -count=1 ./internal/sensor/... ./internal/eventing/... ./cmd/funcdctl/...`
  → all `ok`; e2e `go test -tags e2e -count=1 ./pkg/funcd/...` → `ok github.com/pyvvo/funcd/pkg/funcd 114.356s`
  (it covers the sensor, dead-letter and blob-eventsource paths). Lima lanes were not run (a later stage owns them).
- **Shape.** Subject `fix(sensor): …`, body names the regression test, `Fixes #113`, the Co-Authored-By trailer;
  one issue per commit.

Observation (not a finding): with no input and an event whose `data` is empty, `buildInput` returns `{}`, so the
function would now receive `data: {}` instead of no `data` field. No producer emits such an event
(`NewNamedEvent` sets `{}`, `NewBlobEvent` sets the descriptor), so the case is unreachable today.

### Definition of Done

11 / 11 items hold (the fix checklist in `.claude/skills/fix-review/SKILL.md`). Misses: none.

### Model scorecard

Not recorded by this stage: claude-opus-5-5 on issue #113 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.
A later stage records the ledger row.

### Recommendation

Pass. The fix can ship with the eventing group PR; no rework is needed.
