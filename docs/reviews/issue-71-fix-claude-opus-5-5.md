## Verdict: pass — 0 blockers, 0 majors  (issue #71 fix, model: claude-opus-5-5)

Fix under review: commit ece416e, `fix(function): re-admit a PoolFull member when a slot of its pool frees`, on the
pooling group branch (head 2500327). Touched files: `internal/function/function.go` (one gate argument and one comment
line) and `internal/function/pool_test.go` (the regression test).

The issue: a member rejected with `PoolFull` returned `RequeueAfter: 0`, and the controller reconciles an object only
when it changes. When an admitted sibling is deleted, a sibling's pass puts the rejected member into the pool manifest,
but nothing reconciles that member again. It stays `Pending` with `Ready=False/PoolFull` and has no route, although the
pool serves it.

The fix: the PoolFull `gateFailure` now carries `requeue: r.supervisionPeriod`, so a rejected member comes back every
supervision period (ADR-0142). A pass that finds the pool still full writes nothing, because the store's `Update` of an
unchanged object is a no-op. Once a slot frees, the next pass admits the member, clears `PoolFull` to `Admitted`, makes
it Ready and gives it a route.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** A full `git revert --no-commit` conflicts in
  `pool_test.go` with later commits on the branch. So only the `function.go` hunk was reverse-applied, and the test was
  kept. `go test -race -run TestIssue71_ ./internal/function/` then fails at `pool_test.go:275` with
  `expected: 50ms, actual: 0s — the rejected member comes back on the supervision period`. That is the issue's root
  cause, `RequeueAfter=0`.
- **The test passes with the fix.** It runs un-skipped and passes under `-race` (`-count=3`).
- **The behavior users see is fixed with the real controller engine.** A scratch probe (not committed) registered the
  Function reconciler on `controller.New`. It used PoolLimit 2 and a 50 ms period, applied p1, p2 and p3, waited for p3
  to reach `PoolFull`, then deleted p1.
  - With the fix, p3 became `Ready` with `PoolFull=Admitted` and a programmed route within 70 ms.
  - Pre-fix (overlay), p3 was still `Pending` with `PoolFull` and had no route after 2 s. This is the issue's report.
- **The cause is fixed, not masked.** The missing requeue is the cause the issue names. The fix uses the reconciler's
  existing periodic re-convergence (ADR-0142 option A, `RequeueAfter`), not an engine-wide resync (ADR-0142 rejected
  alternative D). It does not use a longer timeout or a swallowed error.
- **Mutants are killed.**
  - `requeue: 0` (the revert) fails the test.
  - `requeue: 2 * r.supervisionPeriod` fails the test (`expected 50ms, actual 100ms`).
  - The `ResourceVersion` assertion pins the "still full writes nothing" claim.
- **Scope is limited to the issue.** There is one argument change and one comment clause in the gate, plus one new
  test. No test was weakened or deleted, and no unrelated hunk was added.
- **The fix reuses existing code.** It reuses the existing `gateFailure.requeue` field and `r.supervisionPeriod`. Gates
  that wait on something outside the object already requeue the same way (the data-reference and catalog gates use
  `requeue: 2 * time.Second`). The test reuses `newShimHarness`, `withSwitch`, `withNodePool`, `testPeriod`, `h.upstream`
  and `h.condition`. No new helper or dependency was added.
- **The cost is bounded.** A rejected member's periodic pass costs the same as a Ready pooled member's full pass: list
  the key, write a no-op `Update`, and program the routes. ADR-0142 Decision 3 already requires pooled members to take
  the full pass.
- **The ADRs hold.**
  - ADR-0046 Decision 3 (admission is a pure function of declared membership; the first `limit` by name are admitted)
    is what the fix restores. Re-admission is a recomputation of declared membership, not a hidden migration.
  - The fix contradicts no ADR-0142 Decision. The supervision period is the existing periodic re-convergence tool.
  - No ADR file was touched.
- **The conventions hold.** The change is ctx-first and uses no new error path. Imports are unchanged. The test has
  a single-line doc comment that names ADR-0046, and the gate comment states the reason. The added text is minimal.
- **The checks are green.**
  - gofmt is clean on `internal/function`.
  - `go build ./...` passes.
  - `go vet` passes on host and Linux.
  - `golangci-lint` reports 0 issues on host and on Linux.
  - `go test -race` passes for `./internal/function/...`, `./internal/pooling/...` and `./internal/controller/...`.
  - The e2e suite `go test -tags e2e ./pkg/funcd/...` passes (113 s).
  - Lima lanes were not run. A later stage runs them.
- **The commit shape is correct.** The subject is `fix(function): …` and the body contains `Fixes #71` and the
  attribution trailer. The commit fixes one issue. The commit body names the regression test.

Observation (not a finding): after the deletion, the test reconciles p1, p2 and p3 manually, so that half of the test
also passes pre-fix. The reproduction rests on the `RequeueAfter` assertion. This is the same contract-level pattern
the supervision tests use (`supervision_test.go`), and the controller-engine probe above confirms the end-to-end
effect.

The issue also describes an eviction facet: a member that loses its slot keeps Ready for up to one period. The issue
calls it transient and self-correcting, and the fix leaves it unchanged. The commit message states this. A Ready member
already requeues on the period, and its next pass corrects its status.

### Definition of Done
11 / 11 items hold (the fix checklist). Lima lanes are out of scope for this stage.

### Model scorecard
Not recorded by this stage. The fields are returned to the orchestrator: claude-opus-5-5 on issue #71 (fix) → pass,
0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ship as is. The fix is a one-line root-cause change with a regression test that fails pre-fix for the reported reason.
A real-controller probe confirms the change, and the checks are green.
