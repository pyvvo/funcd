## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #53 fix, model: claude-opus-5-5)

Change: `4ad27ba fix(function): finish a switch after the serving Revision is deleted` (`internal/function/function.go`, `internal/function/switch_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Mutant on the not-found branch's running count survives** · attribution: model ·
  Mutant M1 replaced `runningS, err = r.runningReplicas(ctx, fn.Namespace, fn.Name, s, sIdx)` with
  `runningS, err = 0, nil` in `switchSolo`; `go test ./internal/function/ -count=1` → `ok`. A probe log
  under the mutant showed `phase=Ready replicas=0` instead of `replicas=1`: the regression test checks
  `observedGeneration`, `servingRevision` and the upstream, but never `status.replicas`. Fix: assert
  `fn.Status.Replicas == 1` after the pass that runs with the Revision deleted.
- **Ready message is misleading when the deleted serving revision has no running worker** · attribution: model ·
  `switchSolo` (function.go:737-764): with s's Revision gone and every s worker dead, `runningS` is 0 and the
  verdict keeps `serving: true`, so `finish` writes Degraded / `Restarting` "a replica exited and is being
  replaced" (function.go:559-562), but no replacement can happen without the Revision. The Function is no longer
  wedged: status is written, later applies deploy, and the switch completes once c is ready, so this does not
  block the issue. A more accurate reason (or a doc comment on the edge) would help an operator.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 4ad27ba` with the new test kept →
  `TestIssue53_DeletedServingRevisionStillSwitches` FAIL at switch_test.go:781 with
  `function.revisionTemplate: get revision "echo-1": store.Get: Revision "echo-1" not found`, the issue's exact error.
  After `git reset --hard 4ad27ba` → PASS, `-race -count=3`.
- **Root cause fixed, not masked**: a NotFound from `revisionTemplate` in `switchSolo` no longer aborts the pass;
  s's still-running workers are counted and c keeps converging, so `finish` writes status and the switch completes
  (ADR-0143 Decision 4.4). Other errors still propagate (`switch` falls through with `err` set). No retry, timeout or
  swallowed error was added.
- **Mutants M2 and M3**: M2 (drop `in.Revision == rev` in `runningReplicas`) → `TestScenarioFailedRevisionKeepsOldServing`
  and `TestScenarioScaleChangesReplicas` FAIL. M3 (replica-index filter neutralized) survives, but that filter is the
  pre-existing `want[in.Replica]` semantics moved unchanged into the helper, not new behavior.
- **Reuse**: `runningReplicas` is extracted from `convergeRevision`'s tail, which now calls it, so the count exists
  once. No neighbour (`servingWorkerRuns`, `readyReplicas`, `namedInstances`) already counted running workers per
  revision and index set; `slices.Contains` is the standard library.
- **Scope**: two files, every hunk serves the issue; no test weakened or deleted; no ADR file touched.
- **ADRs**: ADR-0143 Decision 4.3 (replace a dead s worker from s's Revision) still holds whenever the Revision exists;
  the fix covers only the case the ADR leaves open, and ADR-0139 (no Revision GC) is untouched.
- **Conventions**: ctx-first, `fault.KindOf` for the kind check, typed `v1.ObjectName`/`v1.NamespaceName`, top-level
  import, one-line doc comments, no comment bloat.
- **Checks (touched package)**: `gofmt -l` clean, `go build ./...` ok, `go vet ./internal/function/` ok,
  `golangci-lint run ./internal/function/` → `0 issues.`, `go test -race -count=1 ./internal/function/` → `ok`.
  e2e, Linux lint and lanes are left to the group gate.
- **Shape**: `fix(function):` subject, `Fixes #53`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Optionally, as a follow-up, add a `status.replicas` assertion to the regression test and give the
deleted-serving-Revision, no-running-worker case a clearer Ready reason.
