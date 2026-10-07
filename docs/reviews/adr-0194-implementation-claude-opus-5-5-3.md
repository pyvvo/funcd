## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (ADR-0194 implementation, model: claude-opus-5-5)

Branch `feat/adr-0194-duration-strings`, head 511215cc, 8 commits on `origin/main` (now eeeb97f4; merge base
2505bde6; `git merge-tree` against `origin/main` is clean). Loop 3.

### Loop-2 findings

| Loop-2 finding | Status at 511215cc |
|---|---|
| Major 1: funcd-typescript is still pinned at v0.8.1 | **Open.** No commit was added after loop 2: the head is still 511215cc, the same commit loop 2 reviewed, and the worktree is clean. `go.mod` and `go.sum` still name `github.com/pyvvo/funcd-typescript v0.8.1`, and `origin/main` also pins v0.8.1. |

### 🟡 Major 1 (carried over from loops 1 and 2, unchanged) — the funcd-typescript pin was not moved, so the `workflow` lane cannot pass  ·  attribution: model

- `go.mod` pins `github.com/pyvvo/funcd-typescript v0.8.1`. In the module cache, line 15 of that release's
  `examples/workflow/schedule-source.yaml` reads `interval: 5000000000`, an integer. The new codec refuses integers
  (mutant a2 below confirms that the refusal is tested), so the `workflow` Lima lane fails when it applies the example.
- `git ls-remote --tags` on funcd-typescript lists `v0.8.2` (8c8dc95a), the release that writes the interval as a
  duration string.
- This misses Decision 11 and Implementation plan step 6 (`go get github.com/pyvvo/funcd-typescript@<that tag>`),
  checklist item 5 ("the funcd-typescript pin is the new tag") and DoD item 1 (the `workflow` lane green with the
  new pin). The scenario `schedule-source-example-runs` cannot pass.
- Fix (builder): `go get github.com/pyvvo/funcd-typescript@v0.8.2`, `go mod tidy`, then commit `go.mod` and `go.sum`.
  Three loops have now ended without this bump, and loop 3 received no new commit at all. The fix loop is not
  delivering this item to the builder. If the workflow means the integrator to do the bump, re-attribute this
  finding to orchestration; otherwise the orchestrator should hand the builder this exact command.

### ✅ Verified correct (keep it)

- **Runs at 511215cc** (captured exit codes):
  - `go build ./...`: darwin 0, linux (`GOOS=linux`) 0.
  - `go vet ./...`: darwin 0, linux 0. `go vet -tags e2e ./pkg/funcd/ ./tests/e2e/`: 0.
  - gofmt over `api cmd internal pkg tests`: no files listed.
  - golangci-lint: darwin "0 issues", exit 0. Linux: a host-built golangci-lint binary run with `GOOS=linux`,
    "0 issues", exit 0. (`GOOS=linux go tool golangci-lint` builds a linux binary that cannot run on the host;
    that is an environment detail, not a finding.)
- **Touched packages with `-race -count=1`, all ok**: `api/types/v1alpha1`, `cmd/funcd`, `cmd/funcdctl`,
  `internal/activator`, `controlplane`, `dataplane`, `eventing`, `function`, `platform/config`, `sensor`,
  `workernode/local`, `workflow/...`, `pkg/sdk` and `testkit/bench`.
- **Scenario tests** (`-race`, run by name over `./...`), all un-skipped and passing:
  `TestScenarioStringDurationApplies`, `TestScenarioIntegerDurationRefused`, `TestScenarioSubMillisecondRefused`,
  `TestScenarioShortestFormOutput`, `TestScenarioBoundsEnforcedAtCreate`, `TestScenarioWaitLiteralCheckedAtApply`,
  `TestWaitExpressionYieldsString`, `TestScenarioConfigSharesGrammar`. The lane scenario
  `schedule-source-example-runs` is blocked by Major 1.
- **Bounds tables**: `TestDurationFieldBounds`, `TestDurationFieldDocNamesBounds`, `TestConfigDurationBoundsPerKey`,
  `TestParseDurationGrammar` and `TestCheckDurationMessages` pass.
- **Mutants** (applied with `go test -overlay`; the work was not edited). Loop 3 used new mutants, not the loop-2 set:
  - a2, `UnmarshalJSON` accepts a bare JSON integer as nanoseconds (the integer path that the API answers with 400):
    failed `TestDurationUnmarshalJSON` and `TestScenarioIntegerDurationRefused`.
  - b, the pattern drops the `ms` group from the hours branch (`1h500ms` refused): failed `TestParseDurationGrammar`,
    `TestParseDurationOverflow`, `TestDurationStringRoundTrips` and `TestSpecGeneratedFromGo`.
  - c, `CheckDuration` makes the lower bound exclusive (`d > lo`): failed `TestDurationFieldBounds`,
    `TestScenarioWaitLiteralCheckedAtApply`, `TestFunctionBlobValidate` and others.
  - a, a weaker first attempt that only skipped the JSON-string check, survived because it is equivalent: an integer
    then reaches `ParseDuration("")`, which the pattern refuses. This is not a finding.
- **Conformance**: the loop-1 and loop-2 conformance evidence still holds, because the code is unchanged.
  `git diff origin/main...HEAD` on the ADR shows only `Accepted → Reviewing` (ADR-0194 was accepted on main in
  af21c637), and the FEAT-0000 F02 sub-status "duration strings" moves only `accepted → reviewing`.

### Definition of Done

6 of 8 items hold (5 Review-checklist items and 3 ADR DoD items).

- Missed: checklist item 5, partly. The pin is not the new tag. Attribution: model.
- Missed: DoD item 1, the `workflow` lane green with the new pin. It cannot pass on v0.8.1. Attribution: model.
- `just ci` itself is left to the gate. Its sub-checks are green above.

### Model scorecard

Not recorded by this gate run. The integrator records the row below.

### Recommendation

Nothing changed since loop 2. The only remaining work is the pin bump to funcd-typescript v0.8.2 (`go get`, then
`go mod tidy`, then commit `go.mod` and `go.sum`), followed by the `workflow` lane in the gate. A loop-4 review needs
to check only `go.mod`, `go.sum` and that lane. Re-running this review without a new commit will give the same verdict.

```json
{"date": "2026-10-07", "adr": "0194", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "changes-requested", "blockers": 0, "majors": 1, "minors": 0, "model_attributed": 1, "dod_passed": 6, "dod_total": 8, "report": "docs/reviews/adr-0194-implementation-claude-opus-5-5-3.md", "notes": "loop 3 (511215cc, unchanged since loop 2; merges clean on eeeb97f4). M1 [model] still open: go.mod pins funcd-typescript v0.8.1 (schedule-source.yaml interval: 5000000000, refused by the codec); v0.8.2 exists; plan step 6 / Decision 11 / checklist 5 / DoD 1 missed; no commit was added in loop 3. Build, vet (darwin and linux, plus e2e tag), lint (darwin and linux) and gofmt clean; touched packages -race ok; 8 in-process scenario tests and the bounds tables pass; new mutants (integer accepted by UnmarshalJSON, ms group dropped from the pattern, exclusive lower bound in CheckDuration) each fail a test."}
```
