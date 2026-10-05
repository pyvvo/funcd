## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0173 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0173-container-execution-solo`, one commit (5ca07bd, `fix(funcd): refuse worker pooling
combined with container execution`, body ends `Fixes #609`). Diff against origin/main: `pkg/funcd/funcd.go` (+7/-2),
`pkg/funcd/options.go` (+6/-2), `pkg/funcd/funcd_test.go` (+199). No other file changed; no go.mod change.

### Verification run (captured)

All commands ran in the worktree through `scripts/agent/d`.

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (darwin) | `go vet ./pkg/funcd/` | exit 0 |
| Vet (linux) | `GOOS=linux go vet ./pkg/funcd/ ./internal/testkit/bench/` | exit 0 |
| Lint (darwin) | `go tool golangci-lint run ./pkg/funcd/...` | `0 issues.`, exit 0 |
| Lint (linux) | host-built golangci-lint, `GOOS=linux … run ./pkg/funcd/... ./internal/testkit/bench/...` | `0 issues.`, exit 0 |
| gofmt | `gofmt -l pkg/funcd/` | no output, exit 0 |
| Scenario tests | `go test -race -count=1 -run 'TestScenario(PoolShimWith…|ContainerExecutionWithout…|ProcessModePooling…|PooledFunctionRunsSolo…)' -v ./pkg/funcd/` | 4/4 PASS (plus 3/3 subtests), `ok`, exit 0 |
| Touched package | `go test -race -count=1 ./pkg/funcd/` (fast lane, e2e tagged out) | `ok … 8.5s`, exit 0 |

Overlay mutants (`git show origin/main:<file>` / `go test -overlay`, the four scenario tests):

| Mutant | Change | Result |
|---|---|---|
| m1 | `pkg/funcd/funcd.go` reverted to origin/main (check removed) | killed: `TestScenarioPoolShimWithContainerExecutionRefused` fails in all three subtests |
| m2 | drop `\|\| len(c.poolShimsByFamily) > 0` | killed: subtest `WithPoolShimFor` fails |
| m3 | drop the `c.imageFor != nil &&` guard (check fires in process mode too) | killed: `TestScenarioProcessModePoolingUnchanged` fails |

3/3 mutants killed.

Not run here, by the task's rules: `just ci` / `just ci-full` (the e2e `pkg/funcd/pooling_e2e_test.go` included) and
`go test ./...`. They belong to the per-PR gate.

### 🔴 Blocker

None.

### 🟡 Major / Minor

None.

Process notes (not findings, not scored):
- The ADR file still reads `Status: Accepted` on the branch. The `Accepted → Reviewing` bump was not made in the
  work. This workflow assigns all doc edits to one docs PR per wave, so this is not attributed to the model.
- `Fixes #609` is in the commit message. The PR that must carry it does not exist yet; the integrator opens it.

### ✅ Verified correct (keep it)

- **Contract check, exact.** `pkg/funcd/funcd.go:285-288`: the check sits in `config.validate`, after the
  required-dependency switch, as the Contracts require. The condition is
  `c.imageFor != nil && (len(c.poolShim) > 0 || len(c.poolShimsByFamily) > 0)`. It returns `fault.Invalidf(op, …)`
  with `op = "funcd.New"`, and the message matches the Contracts text byte for byte. `validate` runs before any
  build step (`New` calls it right after applying the options). The deferred Shutdown from issue #94 releases what
  the options acquired, so a refused `New` starts nothing and returns a nil `*Platform`.
- **"The option was given" semantics.** The condition tests `len(...) > 0` on both fields. Any `WithPoolShimFor` call
  fills `poolShimsByFamily`, and a bare `WithPoolShim()` leaves `poolShim` empty. Both behaviors match the ADR's
  stated rule.
- **Doc comments.** `WithPoolShim` (`options.go:228`), `WithPoolShimFor` (`:237-238`) and `WithContainerExecution`
  (`:391-392`) each state the exclusion and cite ADR-0173. `validate`'s comment now covers invalid combinations.
  The comment at the check states the reason (no pool worker can run in a curated image), not what the code does.
  `options_doc_internal_test` passes.
- **Scenario `pool-shim-with-container-execution-refused`.** `TestScenarioPoolShimWithContainerExecutionRefused`
  (`funcd_test.go:341`) runs a table over `WithPoolShim`, `WithPoolShimFor` and both. It asserts a nil platform,
  `fault.KindOf(err) == fault.Invalid`, `fe.Op == "funcd.New"` and the exact message, as the Implementation plan
  asks. Mutants m1 and m2 prove that it guards both fields.
- **Scenario `container-execution-without-pool-shim-accepted`.** `TestScenarioContainerExecutionWithoutPoolShimAccepted`
  (`:363`) asserts `NoError` and a clean Shutdown. This is stronger than the plan's "does not fail with this
  message".
- **Scenario `process-mode-pooling-unchanged`.** The plan relied only on the e2e-tagged `pooling_e2e_test.go`. The
  builder also added a fast-lane test, `TestScenarioProcessModePoolingUnchanged` (`:491`). In it, two Functions that
  share `spec.pooling.worker: shared` produce exactly one worker, `__pool__nodejs22__shared`. That worker runs the
  command `["node", "pool.mjs"]`, and its `FUNCD_POOL_MANIFEST` lists both members. Mutant m3 proves that the
  test guards process mode against an over-broad check. The e2e test is left to the gate.
- **Scenario `pooled-function-runs-solo-in-container-mode`.** `TestScenarioPooledFunctionRunsSoloInContainerMode`
  (`:510`) uses InMemory, `WithContainerExecution`, a recording runtime and `function.NewFileMaterializer()`, as
  the plan specifies. It asserts exactly two workers (`alpha`, `beta`) and no pool worker. Each worker has
  `Image == imageFor("nodejs22")`, an empty `Command` and no `FUNCD_POOL_MANIFEST`. This is a characterization
  test of today's behavior (Decision 2). It is expected to pass with or without the check, and it does.
- **Test fake is justified.** `recordingRuntime` (`:371`) implements the full `runtime.Runtime` port without
  launching processes. The existing fakes (`internal/function/shim_test.go`, `internal/provider/runtime_test.go`)
  are in other packages' test files, and the same-package `captureRuntime` wraps the real process driver. None of
  them can record specs without starting a real `node`, so the fake does not duplicate reusable code. The
  `obj, _ := v1.NewObject(v1.KindFunction)` line follows the existing pattern in `pkg/funcd` tests.
- **Behavior of the shipped binary unchanged.** `cmd/funcd/main.go:709` (containerd mode) passes only
  `WithRuntime` and `WithContainerExecution`. `:733` and `:763` add pool shims in process mode only. The other
  in-tree container-execution caller, `internal/testkit/bench/containerd_linux.go:97`, passes no pool shim. No
  caller is broken; the Linux build, vet and lint of that package are green.
- **Scope.** No API type, CLI flag or `spec.pooling` field changed, and the diff adds no new dependency. The ADR's
  substance is unchanged on the branch.
- **Tracking (draft and acceptance obligations, already on origin/main).** The F28 row in
  `docs/feat/0000-feat-v1.md:87` links ADR-0173 and reads `pooling: implemented · container solo: accepted`.
  ADR-0050 carries the "Superseded in part by ADR-0173" back-link (line 5).

### Definition of Done

7 of 9 items are verified: the 6 Review-checklist items plus the 3 DoD lines of Implementation plan step 4.
- Hold: the check, its location, op and message; each option alone triggers it; process mode and `cmd/funcd`
  unchanged; each scenario has one named passing test (checklist and DoD line); the F28 row and the ADR-0050
  back-link.
- Deferred to the PR gate, not misses: `just ci` + `just ci-full` green (forbidden here; every sub-check for the
  touched package is green); the PR carries `Fixes #609` (in the commit, and the PR is not opened yet).
  Attribution: env/process, not model.

### Model scorecard

To record (by the wave's docs PR): claude-opus-5-5 on ADR-0173 (implementation) → pass, 0/0/0, 0 model-attributed,
DoD 7/9 (2 deferred to the gate).

### Recommendation

Pass. The work matches every Contract and Scenario. Hand the branch to the integrator for the gate (`just ci-full`
runs the e2e pooling test) and a PR with `Fixes #609`. The wave's docs PR then makes the `Accepted → Reviewing →
Implemented` stamps, moves F28 to `container solo: implemented` and moves the ADR-0173 card to Done.

```json
{
  "date": "2026-10-05",
  "adr": "0173",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 7,
  "dod_total": 9,
  "report": "docs/reviews/adr-0173-implementation-claude-opus-5-5.md",
  "notes": "pass; check in config.validate matches the Contracts exactly (fault.Invalid, op funcd.New, message byte-equal), 4/4 scenario tests pass under -race incl. a new fast-lane process-mode pooling test, pkg/funcd -race green, darwin+linux build/vet/lint clean, 3/3 overlay mutants killed (check removed, poolShimsByFamily dropped, imageFor guard dropped); just ci/ci-full and the PR's Fixes #609 deferred to the gate (env/process); ADR Reviewing bump left to the wave docs PR"
}
```
