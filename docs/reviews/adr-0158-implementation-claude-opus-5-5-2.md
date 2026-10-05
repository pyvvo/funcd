## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0158 implementation, model: claude-opus-5-5, loop 2)

Work reviewed: funcd `feat/adr-0158-pool-member-identity` (ebf76918), funcd-typescript (949c956), funcd-python
(4ff9061), each `git diff origin/main...HEAD`. Since loop 1 (funcd f73ee95a), the funcd commit was amended to add one
file, `internal/function/pool_access_test.go` (+141 lines); `git diff f73ee95a ebf76918` shows nothing else. Both
language commits are unchanged. funcd was verified against both language worktrees through a local `go.work`, which
was removed afterwards; all three trees are clean at the end. The ADR is HELD, so no tracking or status check applies
and nothing was stamped.

### Loop-1 findings

| Loop-1 finding | Status now |
|---|---|
| 🟡 Major 1: four plan-named units have no test (model) | **Resolved.** See the table below. All four tests pass under `-race -count=3`, and each one fails on a mutant of its rule. |
| Minor: `go.mod` does not pin both language releases yet (env) | **Still open, carried forward.** `go.mod`/`go.sum` are not in the diff. The tags exist only after the language PRs merge and release. |
| Minor: `TestReclaimDuringRepairBackoffIsNotAShapeFailure` failed once (env) | **Not reproduced.** `go test -race ./internal/function/` passed on the first run (12.3 s). |
| Minor: stale cached dev env in both language repos (env) | **Resolved.** `scripts/agent/d just ci` exited 0 in both repos this time. |

| Plan-named unit (missing in loop 1) | New test in `internal/function/pool_access_test.go` | Mutant (via `-overlay`) → result |
|---|---|---|
| owned table vs prefix | `TestPoolKeyCountsOwnedBoundTablesAndOwnedPrefixes`: an owned and bound table counts; a table that another Function owns counts as a binding only; an owned table that is not bound does not count; an owned prefix counts even when it is not bound | `poolKeyFor` counts every owned table → FAIL "an owned table not bound does not count"; `poolKeyFor` drops owned prefixes → FAIL "an owned prefix counts unbound" |
| five Lists | `TestPoolAccessListErrorFailsThePassAndReclaimsNothing`, with one subtest per kind (KVStore, Bucket, RolesAssignment, EgressPolicy, Policy): the pass errors, the old pool is not removed, `status.pool` is not written, and the old pool is reclaimed once the List answers | `accessIn` swallows a Policy List error → FAIL `/Policy` "a List error fails the pass" |
| `failed` → `ready` | `TestPooledFailedMemberIsReadyAfterALaterPoolStart`: Failed, then the pool worker exits, it is created again (creates+1), and the member is Ready with the same generation and ShapeValid=True | not mutated (the rule has no single guard to flip) |
| `failed` serving → Degraded | `TestPooledFailedServingMemberIsDegraded`: Degraded, Ready reason not ShapeInvalid, ShapeValid not False, no new pool create | drop `&& !v.serving` from `convergePooled` (`internal/function/pool.go:184`) → FAIL |

### Minor
- **The `go.mod` pin of both language releases is still pending** · attribution: env (sequencing, Decision 7). Plan
  step 3 and "Done" ask for a funcd PR that `go get`s both tags. `GOWORK=off go build ./...` exits 0, so the branch
  builds against the tags it pins today. The integrator must add the pin before the funcd PR is queued, then run
  `just ci-full` once in the PR gate.

### ✅ Verified correct (keep it)
- **funcd checks, with `go.work` over both language worktrees**:
  - `go build ./...`: exit 0. `go vet ./...`: exit 0 on darwin and exit 0 with `GOOS=linux`. `go vet -tags e2e ./pkg/funcd/`: exit 0, so the e2e scenario tests compile.
  - `golangci-lint run ./...`: 0 issues. The Linux lint (the host linter binary with `GOOS=linux` over `./internal/... ./pkg/... ./cmd/...`): 0 issues.
  - `go test -race -count=1` exits 0 on every touched package: `internal/funclog/...`, `internal/function`, `internal/pooling`, `internal/testkit/bench`, `internal/workernode/local`, `pkg/funcd`, `cmd/funcd` and `api/types/v1alpha1`.
  - The four new tests passed 3/3 under `-race`.
- **Language repos**, `scripts/agent/d just ci` (lint, typecheck, tests, build, Go embed check, clean tree):
  - funcd-typescript: CI-EXIT 0, 102/102 shim tests.
  - funcd-python: CI-EXIT 0, 188 shim tests plus every example's tests.
- **Mutants: 6 of 6 killed this loop** (7 of 7 in loop 1).
  - Go: the four listed above.
  - TS: `funclog.ts` never stamps `funcd.member` → "a pool member name is stamped as funcd.member; the solo capture omits it" and "scenario pooled-member-logs" FAIL.
  - Python: `FuncLogHandler` never stamps `funcd.member` → `test_pool_member_is_stamped_and_solo_omits_it` and `test_scenario_pooled_member_logs` FAIL.
- **Contracts and Decisions 1–7**: unchanged since loop 1, which checked them all item by item. The only new code is
  tests, which pin `poolKeyFor`'s owned-and-bound rule, `accessIn`'s fail-the-pass rule, and two rows of the
  `convergePooled` member-state mapping exactly as Decisions 4 and 5 state them.
- **safeBeforeFuncd** (`pyvvo/funcd-typescript` true, `pyvvo/funcd-python` true): **true for both**, under Decision 7's
  condition. Neither language commit changed. funcd needs no new Go API from them (`GOWORK=off` build exits 0). Each
  repo's own CI is green. A solo shim passes no member and funcd ignores unknown JSON keys. No funcd PR may pin the new
  tags before this one, because a pre-0158 funcd reads an unloaded member as Ready.

### Definition of Done
3 / 5 items hold. Counted: Review checklist item 1 and item 2, plus the plan's "Done" items: scenario tests pass,
`just ci-full` green, `go.mod` pins both releases.
- Hold:
  - Review checklist item 1: Decisions 1–5 hold as stated.
  - Review checklist item 2: every test the plan names exists and asserts its rule; no leak was found.
  - Every unit and scenario test that ran here passes under `-race`.
- Pending (env, by the brief and the release sequence):
  - `just ci-full` and the e2e scenarios were not run (no e2e, no Lima).
  - The `go.mod` pin waits for the language releases.

### Model scorecard
Ledger row below (not recorded in `docs/reviews/`; this is loop 2 of a held ADR). 0 majors and 1 minor; none is
model-attributed; DoD 3/5. The two pending items are env-attributed.

### Recommendation
Pass. The loop-1 Major is fixed with four focused tests, and each one fails on a mutant of its rule. Next: merge and
release the two language PRs, then the integrator `go get`s both tags in this funcd PR and runs `just ci-full` once in
the gate before queueing.

```json
{
 "date": "2026-10-05",
 "adr": "0158",
 "phase": "implementation",
 "model": "claude-opus-5-5",
 "verdict": "pass",
 "blockers": 0,
 "majors": 0,
 "minors": 1,
 "model_attributed": 0,
 "dod_passed": 3,
 "dod_total": 5,
 "report": "docs/reviews/adr-0158-implementation-claude-opus-5-5-2.md",
 "notes": "loop 2: loop-1 major resolved, four plan-named units added (owned table vs prefix, five-List error per kind, failed->ready, failed serving->Degraded), 4 Go mutants killed; build/vet/lint green darwin+Linux, touched packages pass -race, TS 102/102 and Py 188+examples green via scripts/agent/d; TS+Py funclog member mutants killed; go.mod pin pending releases and ci-full/e2e not run per brief (env); safeBeforeFuncd true for both under Decision 7"
}
```
