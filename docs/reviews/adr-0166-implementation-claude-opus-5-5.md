## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0166 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0166-branch-void-typing`, one commit (`e05eae90`, `feat(workflow): type skipped join-any
branches and void inputs at reconcile (ADR-0166)`), 9 files, +438/-58, reviewed as `git diff origin/main...HEAD`.

### Verification run (all through `scripts/agent/d`, in the worktree)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/expr/ ./internal/workflow/` (host and `GOOS=linux`) | exit 0 / exit 0 |
| `golangci-lint run ./internal/expr/... ./internal/workflow/...` (host) | `0 issues.` exit 0 |
| same, Linux (host-built binary via `go tool -n`, `GOOS=linux`, as `scripts/agent/gate.sh` does) | `0 issues.` exit 0 |
| `gofmt -l internal/expr internal/workflow` | empty |
| `go mod verify`; `go.mod`/`go.sum` in the diff | ok; untouched |
| `go test -race -count=1 ./internal/expr/ ./internal/workflow/` | ok / ok, exit 0 |
| the 5 scenario tests + `TestWhenTypelessOptionalRoot`, `TestFanInWithUntypedParent`, `TestCheckOptionalRoot`, `TestEvalAbsentRootIsUndefined` (`-race -v`) | all PASS |
| the DoD regression set: `TestCheckEdgeVoid`, `TestFanInCompositeTypechecks`, `TestWhenTypecheckedAtReconcile`, `TestJoinAnyExclusiveBranch`, `TestIssue542_VoidInputRejectsFailureContextAndParams` | all PASS |
| other `Eval` callers / importers: `go test -race ./internal/sensor/`; `go test ./internal/function/ ./pkg/funcd/` (fast lane) | ok / ok / ok |

Overlay mutants (`go test -overlay`, `./internal/expr/ ./internal/workflow/`), 3/3 killed:

| Mutant | Killed by |
|---|---|
| M1 `eval.go`: bind an absent root as before (`null`), not `undefined` (Decision 2) | `TestEvalAbsentRootIsUndefined` (internal/workflow survives, see Minor 1) |
| M2 `contract.go`: restore the `c.IsVoid()` short-circuit (Decision 4) | `TestCheckEdgeVoid`, `TestIssue542_VoidInputRejectsFailureContextAndParams`, `TestScenarioObjectIntoVoidStepRefused`, `TestScenarioVoidFanInIntoVoidStepRefused` |
| M3 `condition.go`: never mark `join: any` parent roots optional (Decision 1) | `TestScenarioJoinAnyUnguardedBranchReadRefused`, `TestWhenTypelessOptionalRoot` |

Not run, by instruction: `just ci-full` (so `TestScenarioWorkflowEndToEnd`), repo-wide `go test ./...`, Lima lanes. A
grep of `pkg/`, `cmd/` and the other `internal/` packages finds no workflow fixture that feeds an object into a
`{"type":"null"}` input, so the stricter void rule should not move the e2e suite; the PR gate confirms it.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **Minor 1: the end-to-end guarded-branch scenario does not exercise Decision 2** · attribution: `adr` · evidence: M1
  (absent root bound as `null`) leaves `internal/workflow` green; only `internal/expr`'s `TestEvalAbsentRootIsUndefined`
  kills it. The scenario fixes the run input at `{"v":9}` and puts the `hi` disjunct first, so
  `(hi !== undefined && hi.v > 0) || (lo …)` short-circuits before the skipped `lo` root is read.
  `TestScenarioJoinAnyGuardedBranchReadRuns` follows the scenario exactly, and the unit test the Implementation plan
  names covers the binding, so nothing ships unguarded. Fix (optional, next touch): add a `{"v":1}` run (hi Skipped,
  lo ran) to the scenario test, which fails under M1 with a `TypeError` on `null.v`.

### ✅ Verified correct (keep it)

- **Tree = Implementation plan**: exactly `internal/expr/{check,eval,expr,expr_test}.go` and
  `internal/workflow/{condition,contract,contract_test,reconcile_contract_test,reconcile_workflow}.go`; nothing
  missing or extra. The one helper outside the Contracts' name list, `checkStepEdges`, is unexported and keeps
  `deriveAndCheck` flat.
- **Contracts, line for line**: `schemaResolver{schemas, optionalRoots}` and the named literal
  `schemaResolver{schemas: r.schemas}`; `docResolver{docs, schemas, absentRoots}` with `Roots()` including absent roots;
  `producerCase{schema, onlyBranch}`; `producerSchemas(n, contracts) []producerCase`; `checkEdge` signature
  unchanged; the `run` snippet (`raw, ok := docs[root]` → `setNested(…, goja.Undefined())`) verbatim; the
  `Resolver` doc comment states the optional-root rule; no exported change in `internal/expr`.
- **Decision 1**: optional only when `effectiveJoin(n.join) == JoinAny && len(n.dependsOn) >= 2`; an optional root
  reports a non-empty `Type` (`object` fallback for `{}`/bare `properties`) and drops `Required`/`HasDefault` even with
  a top-level `default`; `guardedOptionalRoot` makes the one extra `Resolve(root, nil)` per longer reference, and a
  lookup error or `Required`/`HasDefault` leaves the root required; `exempt` lets a guard on the optional root exempt
  only the root's own ident path (`R`, `R[i]`, `R.length`) while field guards keep prefix exemption (1.3).
  `R === undefined`, `R !== undefined && R[0] > 0` and the parenthesized conjunction pass; unguarded field reads,
  deeper probes, defaulted fields, the unparenthesized `R !== undefined && A && B` and `?.` are refused, each tested.
- **Decision 2**: only a referenced root with no document binds `undefined`, and the `continue` skips
  `defaultsFor`, so no default is written into an absent root; `runtimeResolver` lists every direct parent without
  a recorded output as absent; `docResolver` answers an object root lookup without decoding; a `null` document
  still errors, which leaves the root required. Sensor tests (the other `Eval` caller) are green.
- **Decision 3**: two or more parents (typed or not) build a composite as the engine does; `join: all` uses one
  composite with every key required; `join: any` uses one single-branch composite per parent in `dependsOn` order
  with `paramsKeys` honored. Messages match the Contracts table exactly
  (`edge into step "merge" when only "hi" ran (join: any): "lo" (want object) is missing`; the void fan-in names
  `"x"`). The old one-typed-parent-of-two verbatim case is gone (`TestFanInWithUntypedParent`).
- **Decision 4**: the void branch keys on `c.Type == "null"`, not `IsVoid()`; it accepts an absent or
  `null`-typed producer, and `Got` falls back to `object`. An absent or `{}` consumer still requires nothing.
  The onFailure `objectIntoVoid` call is dropped, the `spec.params` call stays (`reconcile_workflow.go:392`), and
  #606's tests stay green.
- **Checklist item 5**: the step-4 comment in `deriveAndCheck` states the new guarantee and names the run-time-only
  cases (builtins, Scope Out (1), (4), (5)). Each scenario has one named, un-skipped test with exact-message
  assertions (a subtest per variant where the ADR lists variants).
- **Conventions**: stdlib `slices`/`bytes` only, no new dependency, no `panic`, `fault` errors, top-level imports,
  comments carry the why plus the ADR reference, and nothing narrates. The commit is conventional and lists the
  scenario tests.
- **Tracking**: the ADR's substance is unchanged (no `docs/` file in the diff). The status bump, feat row F65/F73 and
  the ADR-0095/0098 back-links belong to the wave's docs PR, per the orchestrator. They are not scored.

### Definition of Done

9 / 10 hold (5 Review-checklist items + 5 Implementation-plan DoD items). Deferred, not failed: `TestScenarioWorkflowEndToEnd` under
`just ci-full`, which belongs to the PR gate (`env`). `just ci` is covered here by its sub-checks: build ./..., vet and lint
on the touched packages for host and Linux, gofmt, mod verify, and the touched and importing packages' tests. The
repo-wide run is left to the gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0166 (implementation) → pass, 0/0/1, 0 model-attributed, DoD 9/10 (e2e deferred to
the PR gate). The ledger row is below; the wave's docs PR writes it.

### Recommendation

Pass. Integrate as is; reference #121 and #542 in the PR body. Let the PR gate run `just ci-full`. Optionally add the
`{"v":1}` run from Minor 1 at the next touch of the scenario test.

```json
{
  "date": "2026-10-05",
  "adr": "0166",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 0,
  "dod_passed": 9,
  "dod_total": 10,
  "report": "docs/reviews/adr-0166-implementation-claude-opus-5-5.md",
  "notes": "Contracts match line for line (optionalRoots/absentRoots/producerCase/producerSchemas, undefined binding, void rule on type null not IsVoid, onFailure objectIntoVoid dropped); all 5 scenario tests + 4 named unit tests pass under -race; build/vet/lint green on darwin and linux; 3/3 overlay mutants killed; guarded-branch scenario's {v:9} run short-circuits before the skipped root, so the undefined binding is caught only by the expr unit test (adr); ci-full e2e deferred to the PR gate (env)"
}
```
