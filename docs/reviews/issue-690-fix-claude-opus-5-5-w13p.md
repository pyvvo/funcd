# Fix review — issue #690 (fix, model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #690 fix, model: claude-opus-5-5)

Change: `fix/w13p-i690`, commit eeb6f391 `fix(function): keep a pooled member serving through a failed gate`
(`internal/function/function.go`, `internal/function/catalog_restart_test.go`). Judged against the decided outcome:
ADR-0161 Decision 2 applied to a pooled member through its pool instance, as `listeningCount` already did for
`failPass` (ADR-0158 `/health/members`), with no ADR-0143 4.6 stop for a pooled member.

### 🟡 Minor 1 — the pooled exemption from the C != S stop is not pinned by a test  ·  attribution: model

`gateFailed` now skips `stopRevision` for a pooled member (`&& !r.pooled(fn)`), as the decision requires. Mutant m1
(guard removed) survives every pool, member and gate test in the package. `stopRevision` lists only instances named
`fn.Name`, which a pooled member normally has none of, so the mutant is close to equivalent; a short case with a
pooled member whose current revision differs from its serving one would still pin the decision.

### 🟡 Minor 2 — a pool worker that does not answer or does not list the member counts as not running  ·  attribution: model

`countWorkers` returns `0, 0` when `memberIn` reports `!ok` (pool silent on `/health/members`, or no entry for the
member). The decision reads "running = the pool worker is running"; under it a running but unanswering pool worker
would make the member Degraded (as a running, non-listening solo worker does), while the fix lets the gate's own
writes apply (Pending, route dropped). The doc comment states the choice explicitly ("runs for fn only while its
/health/members lists fn"), the listening side keeps the earlier `listeningCount` behavior, and no test covers the
`!ok` row through a gate. A side effect: `memberIn` is now probed whenever the pool worker runs, not only when it
listens; it skips a worker without a port, so this costs at most one extra probe.

### ✅ Verified correct (keep it)

- **Revert check**: with `origin/main`'s `function.go` overlaid, `TestIssue690_PooledMemberServesThroughAFailedGate`
  fails in both subtests for the issue's reason: `catalog-not-ready` expected Ready, got Pending; `config-missing`
  expected Ready, got Failed (the second gate is not catalog-specific, so the fix is in `gateFailed`).
- **With the fix**: the test passes under `-race -count=3`; the whole `internal/function` package passes under
  `-race`; `go vet` clean; golangci-lint 0 issues.
- **Mutants**: m2 (keep `listening` when the member entry is not ready) fails the Issue690 test and
  `TestFailedPassCountsPooledMemberOnlyWhileReady`; m3 (no pool-instance resolution) fails the Issue690 test,
  `TestFailedPassCountsPooledMemberOnlyWhileReady` and `TestIssue38_ServingMemberKeepsItsRevisionOnBrokenUpdate/outage`.
  m1: see Minor 1.
- **Cause, not symptom**: `servingWorkers` now resolves a pooled member to its pool instance (revision `""`) through
  `pooling.ParsePool` and checks the member's entry with the existing `memberIn` (no second client), exactly the
  path `listeningCount` used. Running pool worker + non-ready entry → Degraded / `Ready=False Restarting`, asserted.
- **Reuse**: the duplicated loops of `listeningCount` and `servingWorkers` are folded into one `countWorkers`;
  `listeningCount` keeps its serving-else-current fallback, `servingWorkers` its no-fallback rule. Test helpers
  (`configMap`, `deleteConfigMap`, `setMember`, `requireCondition`, `routes`, `upstream`) are existing ones.
- **Scope**: two files, every hunk serves the issue. The ADR-0162 test that pinned the bug and cited #690 is
  inverted into the regression test as the issue asked; it asserts more than before (Ready, replicas 1,
  RevisionReady False with the gate's reason, route kept, handed out, then Degraded), so no coverage is lost.
- **ADRs**: no ADR file edited; ADR-0161 Decision 2, ADR-0158 and ADR-0143 4.6 hold.
- **Shape**: `fix(function):` subject, `Fixes #690`, attribution trailer, one issue in one commit.

### Definition of Done

10 of 11. Item 4 (mutating the key lines fails a test) holds for the counting lines and not for the pooled stop
guard (Minor 1). Item 8 is met for the touched package (host build, vet, lint, `-race` tests); the Linux lint, e2e
and the repo-wide set run in the group gate.

### Model scorecard

claude-opus-5-5 · pass · 0 blockers · 0 majors · 2 minors · 2 model-attributed · DoD 10/11.

### Recommendation

Pass. Optionally add a pooled case with current != serving revision through a gate, and decide in a follow-up
whether a silent pool worker should read as running (Degraded) per the decision's wording.
