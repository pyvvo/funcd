## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (ADR-0194 implementation, model: claude-opus-5-5)

Branch `feat/adr-0194-duration-strings`, 7 commits on `origin/main` (ec274537), head f6b05673. Loop 1.

### 🟡 Major 1 — the funcd-typescript pin was not moved, so the `workflow` lane cannot pass  ·  attribution: model

- `go.mod:40` still pins `github.com/pyvvo/funcd-typescript v0.8.1`. In that version,
  `examples/workflow/schedule-source.yaml:15` reads `interval: 5000000000`, an integer. The new codec refuses
  integers (`TestDurationUnmarshalJSON` checks this), so the `workflow` Lima lane (`scripts/lanes.yaml`,
  `e2e/workflow.venom.yml`) fails when it applies the example.
- Tag `v0.8.2` exists. It was released at 21:01 UTC, about 15 minutes before the branch's first commit at 21:16 UTC,
  from funcd-typescript PR #53 ("write the timer interval as a duration string").
- This misses Decision 11, Implementation plan step 6 (`go get github.com/pyvvo/funcd-typescript@<that tag>`), the
  checklist item "the funcd-typescript pin is the new tag" and the DoD item "the `workflow` lane (new funcd-typescript
  pin) green". As a result, the scenario `schedule-source-example-runs` cannot pass.
- The `!` / `BREAKING CHANGE:` footers are on the commits. No commit carries `Fixes #816`. The PR description may
  carry it instead.
- Fix (builder): `go get github.com/pyvvo/funcd-typescript@v0.8.2`, `go mod tidy`, then commit. If the workflow gave
  the pin bump to the integrator rather than the implementer, re-attribute this finding to orchestration.

### Minor

- **Two copies of the wait-expression test** · model · `internal/workflow/condition.go:184`
  (`strings.HasPrefix(strings.TrimSpace(raw), "${{")`) restates `isWaitExpression` (`api/types/v1alpha1/workflow.go:368`).
  The apply-time check and the run-time check can drift. Fix: export one predicate and call it from both places.

### ✅ Verified correct (keep it)

- **Runs** (captured exit codes): `go build ./...` darwin 0 and linux 0; `go vet ./...` darwin 0 and linux 0;
  `go vet -tags e2e ./pkg/funcd/ ./tests/e2e/` 0; golangci-lint darwin 0 issues and linux (`GOOS=linux`) 0 issues;
  gofmt reports no changed files.
- **Tests with `-race -count=1`, all ok**: `api/types/v1alpha1`, `cmd/funcd`, `cmd/funcdctl`, `internal/activator`,
  `controlplane`, `dataplane`, `eventing`, `function`, `platform/config`, `sensor`, `workernode/local`,
  `workflow/...`, `store/...`, `pkg/sdk` and `testkit/bench`.
- **Scenario tests**, named after their scenarios, all un-skipped and passing:
  - `TestScenarioStringDurationApplies`
  - `TestScenarioIntegerDurationRefused` (funcdctl refuses offline; a raw `30` gets 422 "expected string" at
    `body.spec.timeout`; a raw `"30"` gets a 422 that names the grammar; nothing is stored)
  - `TestScenarioSubMillisecondRefused`
  - `TestScenarioShortestFormOutput` (including `0s` omitted on read)
  - `TestScenarioBoundsEnforcedAtCreate` (offline pre-flight; 400 `urn:funcd:problem:invalid`; `-1s` gets 422;
    `store.Create` returns `fault.Invalid`; `1h` is stored)
  - `TestScenarioWaitLiteralCheckedAtApply`
  - `TestWaitExpressionYieldsString` (`"50ms"` waits; `0.05`, `true` and `"1.5s"` fail the step and name the grammar)
  - `TestScenarioConfigSharesGrammar`
  - `schedule-source-example-runs` is a lane scenario and is blocked by Major 1.
- **Contract tests**:
  - The Decision 2 table runs through both `ParseDuration` and `regexp.MustCompile(DurationPattern)`, which agree.
  - The overflow pair holds: `MaxDuration` = `2562047h47m16s854ms`, and one millisecond more is refused although it
    matches the pattern.
  - Also covered: the `CheckDuration` messages ("at least lo" when `hi` is `MaxDuration`), `String` round trips,
    the `MarshalJSON` refusals (negative, sub-millisecond), `UnmarshalJSON` (string, number, bool, `null`, `{}`) and
    `Schema` (a new pointer on each call, no min/max/format).
- **Bounds table**: `TestDurationFieldBounds` covers all seven Contracts rows at lo − 1ms, lo, hi, hi + 1ms and
  lo + 1ns (set from Go). Each message names the field path, the value and `[lo, hi]`.
  `TestDurationFieldDocNamesBounds` ties each `doc` tag to the same constants and asserts there is no
  `minimum`/`maximum`/`format` tag. The tests that the plan said to replace became these tests.
- **Mutants** (applied with `go test -overlay`; the work was not edited). Every mutant failed at least one test:
  - m1, the pattern admits `[0-9.]+s`: failed `TestParseDurationGrammar`, `TestDurationUnmarshalJSON` and
    `TestScenarioWaitLiteralCheckedAtApply`.
  - m2, the whole-millisecond check made `whole := true`: failed `TestCheckDurationMessages` and
    `TestDurationFieldBounds`.
  - m3, `MaxLinkTimeout` changed to 6m: failed `TestDurationFieldDocNamesBounds`.
  - m4, the `spec.timeout` `CheckDuration` call (the 400 path) removed: failed `TestDurationFieldBounds` and
    `TestScenarioBoundsEnforcedAtCreate`.
- **Conformance**:
  - `DurationPattern` is the only copy of the grammar. `ParseDuration` matches the pattern first, then calls
    `time.ParseDuration`, then runs the whole-millisecond guard. There is no new module.
  - `UnmarshalJSON` refuses anything that is not a string, and `null` leaves the value unchanged.
  - `Schema` is `{string, pattern, patternDescription}`.
  - The seven fields take `Duration` with `doc` tags. The generated spec writes all seven as `type: string` with
    the pattern and no `format`/`minimum`/`maximum`, and the drift test passes.
  - The bound constants sit beside `MaxInvokeTimeout`, which stays `time.Hour`.
  - `EventSource`, `FunctionSpec` and `Workflow` `Validate` call `CheckDuration`. The literal `wait` is parsed at apply.
    `evalWait` accepts only a string result and wraps errors as `fault.Invalid`.
  - `parseDuration(key, s, def, lo, hi)` replaces `parseDurationOr`. The per-key lo/hi values match Decision 9:
    1ms for positive keys, 0s where zero keeps its meaning, `minRetryBackoffMax`, `maxStopGrace`, `MaxRetryBackoff`,
    `MaxInvokeTimeout` and `MaxDuration`. The cross-key orderings stay. `TestConfigDurationBoundsPerKey` covers lo and
    hi for each key.
  - `workflow describe` rounds to 1 ms and prints `String()`.
  - No `time.ParseDuration` is left in non-test code under `cmd/`, `internal/platform` or `pkg/funcd`.
  - The DoD grep finds `time.Duration` only in `duration.go`.
  - No lane suite or script carries an integer duration.
- **Tracking**: the ADR's only change is `Accepted → Reviewing`; its substance is unchanged. The FEAT-0000 F02
  sub-status "duration strings" reads `reviewing`. The commits are split by concern, with `!` and `BREAKING CHANGE:`
  on the breaking commits.

### Definition of Done

6 of 8 items hold (5 Review-checklist items and 3 ADR DoD items).

- Missed: checklist item 5, partly (scenario tests and the regenerated spec hold; the pin does not). Attribution: model.
- Missed: DoD item 1, the `workflow` lane green with the new pin. It was not run here, the gate runs it, and it
  cannot pass on v0.8.1. Attribution: model.
- `just ci` itself is left to the gate. Its sub-checks (build, vet, lint, touched tests) are green above.

### Model scorecard

Not recorded by this gate run; the integrator records the row below.
Planned row: claude-opus-5-5 on ADR-0194 (implementation) → changes-requested; 0 blockers, 1 major, 1 minor;
2 model-attributed; DoD 6/8.

### Recommendation

The fix is a one-line pin bump to funcd-typescript v0.8.2 (`go get`, then `go mod tidy`). After it, re-run the
`workflow` lane through the gate. The code, the tests and the conformance to the ADR are otherwise sound, and the
loop-2 review can be limited to the pin and the lane. The minor finding is optional polish.

```json
{"date": "2026-10-07", "adr": "0194", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "changes-requested", "blockers": 0, "majors": 1, "minors": 1, "model_attributed": 2, "dod_passed": 6, "dod_total": 8, "report": "docs/reviews/adr-0194-implementation-claude-opus-5-5.md", "notes": "loop 1 (f6b05673 on ec274537). M1 [model]: go.mod still pins funcd-typescript v0.8.1, whose schedule-source.yaml has interval: 5000000000, now refused, so the workflow lane cannot pass; v0.8.2 (PR #53) existed before the commits; plan step 6 / Decision 11 / checklist 5 / DoD 1 missed. m1 [model]: the ${{ prefix test is duplicated in evalWait and isWaitExpression. Build, vet (darwin and linux, plus e2e tag), lint (darwin and linux) and gofmt clean; touched packages -race ok; all 8 in-process scenario tests and the 7-row bounds/doc tables pass; 4 mutants (pattern, whole-ms, MaxLinkTimeout, spec.timeout CheckDuration) each fail a test. ADR substance unchanged; feat row at reviewing; no identity leak."}
```
