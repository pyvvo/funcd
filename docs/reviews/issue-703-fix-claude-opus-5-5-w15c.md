## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #703 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i703`, commit f5d44472 `fix(function): re-check an ArtifactUnresolved Function every supervision period`.
Files: `internal/function/function.go` (+3/-1), `internal/function/digest_test.go` (+43).

The fix adds `requeue: r.supervisionPeriod` to the `ArtifactUnresolved` gate in `reconcileFunction`. Before it,
`gateFailed` returned no requeue when no worker ran, the status write coalesced (ADR-0047), and the controller
forgot the Function, so it stayed `Failed` after the registry recovered.

### Proof first (the decision for this issue)

`TestIssue703_TransientResolveErrorIsRetried` was run against the current `origin/main` (a394c6f1) version of
`internal/function/function.go` through `go test -overlay`, with the test file kept:

```
--- FAIL: TestIssue703_TransientResolveErrorIsRetried/pass        expected: 50ms  actual: 0s  "the gate is re-checked every supervision period"
--- FAIL: TestIssue703_TransientResolveErrorIsRetried/controller   Condition never satisfied  "the Function recovers without a re-apply"
FAIL  github.com/pyvvo/funcd/internal/function
```

Both cases of the issue fail on current main for the stated reason: the single pass returns `RequeueAfter: 0`,
and in the real controller loop the Function never reaches `Ready` after the resolver recovers. The issue's
third probe (a coalesced identical re-apply) has the same cause, and the `controller` subtest covers its outcome
(recovery with no re-apply).

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- With the fix: `go test -race -count=5 -run TestIssue703 ./internal/function/` → `ok`.
- Cause, not symptom: the requeue is added at the gate that dropped the Function. The status mapping of ADR-0035
  (`Failed` + `ArtifactUnresolved`, scenario `unresolvable-ref-fails`) is unchanged, and
  `TestScenarioUnresolvableRefFails` still passes. The value matches the sibling gates on the same switch
  (`RevisionMissing`, `RevisionStampFailed`, `RuntimeUnavailable`) and ADR-0143 Decision 7 / ADR-0149 Decision 5.
  The issue rules out the alternative (reporting `ReconcileFailed`, reclassifying `artifact.go`), which would need an ADR.
- Mutants (overlay, `-run TestIssue703|TestScenarioUnresolvableRefFails`):
  - The `ArtifactUnresolved` case disabled → both subtests and `TestScenarioUnresolvableRefFails` fail.
  - `requeue: 2 * r.supervisionPeriod` → the `pass` subtest fails (`expected: 50ms, actual: 100ms`).
  - `requeue: r.referentPoll` → survives. This mutant is equivalent in the harness, because `referentPoll` is
    `min(referentPoll, period)` = 50 ms = `testPeriod`. It is not a test gap.
- Scope: one production line and its one-line *why* comment, plus the regression test. Nothing unrelated changed,
  and no test was weakened.
- Reuse: the test reuses `newShimHarness`, `withSwitch`, `pinning`, `fakeResolver`, `pinRecorder`, `testPeriod` and
  `controller.New`. The fix reuses the existing `gateFailure.requeue` field. Nothing is reinvented.
- Siblings: `pinDigest` is the only producer of `errArtifactUnresolved`, and the one `gateFailed` call that consumes
  it now requeues. The other `Failed` gates in the switch already requeue.
- Conventions: the code follows ADR-0002 and the surrounding gate idiom, imports stay at the top level, and the
  comment states the reason without narration.
- ADRs: no ADR file was edited, and no Accepted or Implemented decision is contradicted (ADR-0035 mapping kept;
  ADR-0161 Decision 2 leaves the requeue to the gate).
- Checks (touched package only): `go test -race ./internal/function/` → ok; `go vet` → clean; `golangci-lint run
  ./internal/function/` → 0 issues. The repo-wide and Linux checks run once in the group gate.
- Commit shape: `fix(function):` subject, `Fixes #703`, attribution trailer, one issue in one commit.

Observation, not a finding: a ref that never resolves now costs one registry resolve per supervision period
(10 s by default). That is the same cost as the absent-image gate (ADR-0149) and the cost the issue accepts.

### Definition of Done

11 / 11 applicable items hold. Item 8 is limited to the touched package here: the e2e suite, the lanes and the
Linux lint are delegated to the group gate.

### Model scorecard

Not recorded by this review (the batch's ledger PR records it): claude-opus-5-5 on issue #703 (fix) → pass,
0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Sign off. Hand back to `/fix` Step 8 for integration into the group PR.
