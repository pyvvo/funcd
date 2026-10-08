## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (ADR-0194 implementation, model: claude-opus-5-5)

Branch `feat/adr-0194-duration-strings`, 8 commits on `origin/main` (ec274537), head 511215cc. Loop 2.

### Loop-1 findings

| Loop-1 finding | Status at 511215cc |
|---|---|
| Major 1: funcd-typescript is still pinned at v0.8.1 | **Open.** Commit 511215cc does not touch `go.mod` or `go.sum`. |
| Minor: the wait-expression test exists in two copies | **Resolved.** `v1alpha1.IsWaitExpression` (`api/types/v1alpha1/workflow.go:386`) is exported, and both `validateDurations` (line 377) and `evalWait` (`internal/workflow/condition.go:185`) call it. The now-unused `strings` import was removed from `condition.go`. |

### 🟡 Major 1 (carried over from loop 1, unchanged) — the funcd-typescript pin was not moved, so the `workflow` lane cannot pass  ·  attribution: model

- `go.mod` still pins `github.com/pyvvo/funcd-typescript v0.8.1`. In the module cache,
  `examples/workflow/schedule-source.yaml:15` of v0.8.1 reads `interval: 5000000000`, an integer. The new codec
  refuses integers, so the `workflow` Lima lane fails when it applies the example.
- `git ls-remote --tags` on funcd-typescript lists `v0.8.2` (8c8dc95a). That release is the one that writes the
  interval as a duration string, and it is not in the local module cache.
- This misses Decision 11 and Implementation plan step 6 (`go get github.com/pyvvo/funcd-typescript@<that tag>`).
  It also misses checklist item 5 ("the funcd-typescript pin is the new tag") and DoD item 1 (the `workflow` lane
  green with the new pin). The scenario `schedule-source-example-runs` therefore cannot pass.
- Fix (builder): run `go get github.com/pyvvo/funcd-typescript@v0.8.2` and `go mod tidy`, then commit `go.mod`
  and `go.sum`. If the workflow assigns the pin bump to the integrator rather than to the implementer, re-attribute
  this finding to orchestration. Two loops have now passed without the bump, which suggests that the fix loop is not
  passing this item to the builder.

### ✅ Verified correct (keep it)

- **Runs at 511215cc** (captured exit codes):
  - `go build ./...`: darwin 0, linux 0.
  - `go vet ./...`: darwin 0, linux 0.
  - `go vet -tags e2e ./pkg/funcd/ ./tests/e2e/`: 0.
  - gofmt: no changed files.
  - golangci-lint: darwin 0 issues. For linux, the host-built lint binary ran with `GOOS=linux`: 0 issues.
- **Tests with `-race -count=1`, all ok**: `api/types/v1alpha1`, `cmd/funcd`, `cmd/funcdctl`, `internal/activator`,
  `controlplane`, `dataplane`, `eventing`, `function`, `platform/config`, `sensor`, `workernode/local`,
  `workflow/...`, `pkg/sdk` and `testkit/bench`.
- **Scenario tests**, all un-skipped and passing:
  - `TestScenarioStringDurationApplies`
  - `TestScenarioIntegerDurationRefused`
  - `TestScenarioSubMillisecondRefused`
  - `TestScenarioShortestFormOutput`
  - `TestScenarioBoundsEnforcedAtCreate`
  - `TestScenarioWaitLiteralCheckedAtApply`
  - `TestWaitExpressionYieldsString`
  - `TestScenarioConfigSharesGrammar`
  - `schedule-source-example-runs` is a lane scenario and is blocked by Major 1.
- **Bounds tables**: `TestDurationFieldBounds`, `TestDurationFieldDocNamesBounds` and `TestConfigDurationBoundsPerKey`
  pass.
- **Mutants** (applied with `go test -overlay`; the work was not edited):
  - m1, `IsWaitExpression` stops trimming and needs `"${{ "`: failed `TestScenarioWaitLiteralCheckedAtApply`.
    The new shared predicate is guarded.
  - m3, the pattern makes the `m` unit optional (`[0-9]+m?(…)`): failed `TestParseDurationGrammar`.
  - m4, `MaxLinkTimeout` changed from 5m to 6m: failed `TestDurationFieldDocNamesBounds`.
  - m5, the `CheckDuration` whole-millisecond check forced to `true`: failed `TestCheckDurationMessages` and
    `TestDurationFieldBounds`.
  - m2, the `d%time.Millisecond` clause removed from `ParseDuration`, survived. This mutant is equivalent: the
    pattern admits only the h, m, s and ms units, so every value that matches it is a whole number of
    milliseconds. The clause is defensive and cannot be reached. This is not a finding.
- **Conformance**: all the loop-1 conformance evidence still holds, because 511215cc changes only the predicate's
  visibility and its one caller. The ADR's only change is `Accepted → Reviewing`, and the FEAT-0000 F02 sub-status
  "duration strings" reads `reviewing`. The fix commit carries `Refs #816`.

### Definition of Done

6 of 8 items hold (5 Review-checklist items and 3 ADR DoD items).

- Missed: checklist item 5, partly. The pin is not the new tag. Attribution: model.
- Missed: DoD item 1, the `workflow` lane green with the new pin. It cannot pass on v0.8.1. Attribution: model.
- `just ci` itself is left to the gate. Its sub-checks are green above.

### Model scorecard

Not recorded by this gate run. The integrator records the row below.

### Recommendation

The only remaining work is the one-line pin bump to funcd-typescript v0.8.2 (`go get`, then `go mod tidy`). After
it, the gate must run the `workflow` lane. The loop-3 review can be limited to `go.mod`, `go.sum` and that lane.

```json
{"date": "2026-10-07", "adr": "0194", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "changes-requested", "blockers": 0, "majors": 1, "minors": 0, "model_attributed": 1, "dod_passed": 6, "dod_total": 8, "report": "docs/reviews/adr-0194-implementation-claude-opus-5-5-2.md", "notes": "loop 2 (511215cc on ec274537). Loop-1 minor resolved (IsWaitExpression exported and shared by apply and evalWait). M1 [model] still open: go.mod pins funcd-typescript v0.8.1 (schedule-source.yaml interval: 5000000000, refused by the codec); v0.8.2 exists; plan step 6 / Decision 11 / checklist 5 / DoD 1 missed. Build, vet (darwin and linux, plus e2e tag), lint (darwin and linux) and gofmt clean; touched packages -race ok; 8 in-process scenario tests and the bounds tables pass; mutants on IsWaitExpression, the pattern, MaxLinkTimeout and the CheckDuration whole-ms check each fail a test (the ParseDuration whole-ms clause is an equivalent mutant)."}
```
